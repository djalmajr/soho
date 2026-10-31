package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/backend"
	"github.com/djalmajr/soho/internal/collaboration"
	"github.com/djalmajr/soho/internal/communication"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/dispatch"
	"github.com/djalmajr/soho/internal/peer"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/transport"
)

func cmdCollaborate(argv []string, cfg *core.Config, env platform.Env, cwd string) int {
	fail := func(err error) int { fmt.Fprintf(platform.Stderr, "soho: collaborate: %s\n", err); return 2 }
	svc := backend.New(env)
	caller := svc.Runtime.Caller()
	if len(argv) == 0 {
		return fail(errors.New("expected start, status, event, stop or finalize"))
	}
	store := collaboration.Store{StateDir: core.StateDirPath(cfg, env, cwd)}
	// The caller-pane check runs after the arguments are valid and before
	// the first Herdr call of the operations that act in the name of the
	// caller pane (DJA-394): a usage error never calls Herdr.
	if argv[0] != "status" && core.StateInSkill(cfg, env, cwd) != "" {
		return fail(errors.New("state inside the skill is forbidden"))
	}
	var a collaboration.Assignment
	var err error
	switch argv[0] {
	case "start":
		if len(argv) < 5 {
			return fail(errors.New("start requires author reviewer --brief <path>"))
		}
		brief, rounds := "", 3
		for i := 3; i < len(argv); i++ {
			if i+1 >= len(argv) {
				return fail(errors.New("missing start option value"))
			}
			switch argv[i] {
			case "--brief":
				if brief != "" {
					return fail(errors.New("duplicate brief"))
				}
				brief = argv[i+1]
			case "--max-rounds":
				rounds, err = strconv.Atoi(argv[i+1])
				if err != nil || rounds < 1 || rounds > 100 {
					return fail(errors.New("max-rounds must be 1..100"))
				}
			default:
				return fail(fmt.Errorf("unknown start option %s", argv[i]))
			}
			i++
		}
		if brief == "" {
			return fail(errors.New("start requires --brief"))
		}
		core.CallerGate("collaborate", cfg, env, cwd)
		if err := collaborationOrchestrator(store.StateDir, cfg, env, svc, cwd); err != nil {
			return fail(err)
		}
		brief, err = filepath.Abs(brief)
		if err != nil {
			return fail(err)
		}
		raw, err := os.ReadFile(brief)
		if err != nil {
			return fail(err)
		}
		aliases, ignored := dispatch.ParseBriefLintAliases(core.Cfg(cfg, "brief_lint_aliases", "", env))
		if len(ignored) != 0 {
			return fail(fmt.Errorf("invalid brief aliases: %s", strings.Join(ignored, ", ")))
		}
		if missing := dispatch.BriefMissingSections(string(raw), false, aliases); missing != "" {
			return fail(fmt.Errorf("incomplete collaboration brief:%s", missing))
		}
		paths := ownedPaths(string(raw), aliases)
		if len(paths) == 0 {
			return fail(errors.New("brief needs concrete owned input files"))
		}
		author, err := collaboration.ResolveParticipant(store.StateDir, argv[1], env)
		if err != nil {
			return fail(err)
		}
		reviewer, err := collaboration.ResolveParticipant(store.StateDir, argv[2], env)
		if err != nil {
			return fail(err)
		}
		if !core.RoleIsEdit(author.Role, env, author.Cwd) || !core.IsReviewRole(reviewer.Role) {
			return fail(errors.New("start requires an edit author and a review worker"))
		}
		root := platform.StateProjectRoot(env, cwd)
		if platform.StateProjectRoot(env, author.Cwd) != root || platform.StateProjectRoot(env, reviewer.Cwd) != root {
			return fail(errors.New("participants belong to another logical repository"))
		}
		if _, err := collaboration.Fingerprint(author.Cwd, paths); err != nil {
			return fail(err)
		}
		a = collaboration.Assignment{Author: author, Reviewer: reviewer, Workspace: caller.WorkspaceID, Root: root, Brief: brief, BriefHash: collaboration.Hash(raw), Paths: paths, MaxRounds: rounds}
		// Validate both directions needed by a review cycle before publishing.
		for _, request := range []struct {
			from, to collaboration.Participant
			typ      string
		}{{author, reviewer, "review.ready"}, {reviewer, author, "review.finding"}, {reviewer, author, "review.result"}} {
			if err := collaborationPermission(cfg, env, a, request.from, request.to, request.typ); err != nil {
				return fail(err)
			}
		}
		a, err = store.Create(a)
	case "status":
		if len(argv) < 2 || len(argv) > 3 || (len(argv) == 3 && argv[2] != "--json") {
			return fail(errors.New("status requires assignment [--json]"))
		}
		a, err = store.Read(argv[1])
		if err != nil {
			return fail(err)
		}
		if caller.WorkspaceID != a.Workspace || platform.StateProjectRoot(env, cwd) != a.Root {
			return fail(errors.New("assignment belongs to another workspace or repository"))
		}
		var revision string
		var cause string
		if err := collaboration.CheckParticipants(a, store.StateDir, env); err != nil {
			cause = err.Error()
		}
		if pol, err := communication.Load(cfg, env); err != nil {
			cause = err.Error()
		} else if pol.Mode == communication.ModeOff && a.Active() {
			cause = "worker_messages=off; new worker messages are disabled"
		}
		if r, err := collaboration.Fingerprint(a.Author.Cwd, a.Paths); err == nil {
			revision = r.Fingerprint
		} else {
			cause = err.Error()
		}
		view := struct {
			collaboration.Assignment
			CurrentRevision string `json:"current_revision"`
			Cause           string `json:"cause,omitempty"`
		}{a, revision, cause}
		if len(argv) == 3 {
			return collaborationJSON(view)
		}
		fmt.Fprintf(platform.Stdout, "%s\t%s\tround=%d\trevision=%s\t%s\n", a.ID, a.Phase, a.Round, revision, cause)
		return 0
	case "event":
		if len(argv) != 7 || argv[3] != "--report" || argv[5] != "--revision" {
			return fail(errors.New("event requires assignment ready|findings|corrected|approved|escalate --report <path> --revision <fingerprint>"))
		}
		typ := map[string]string{"ready": "review.ready", "corrected": "review.ready", "findings": "review.finding", "approved": "review.result"}[argv[2]]
		if typ == "" && argv[2] != "escalate" {
			return fail(errors.New("unknown collaboration event"))
		}
		// Before anything is read or persisted, and also for `escalate`
		// (no delivery): PrepareEvent writes the transition. The delivery
		// below checks again through the same memo.
		core.CallerGate("collaborate", cfg, env, cwd)
		a, err = store.Read(argv[1])
		if err != nil {
			return fail(err)
		}
		if platform.StateProjectRoot(env, cwd) != a.Root {
			return fail(errors.New("event sender belongs to another repository"))
		}
		if err := collaboration.CheckParticipants(a, store.StateDir, env); err != nil {
			return fail(err)
		}
		actor, member := a.Member(caller.PaneID)
		if !member {
			return fail(errors.New("event sender is not an assigned worker"))
		}
		target := a.Reviewer
		if actor.Pane == target.Pane {
			target = a.Author
		}
		if typ != "" {
			if err := collaborationPermission(cfg, env, a, actor, target, typ); err != nil {
				return fail(err)
			}
		}
		report, err := filepath.Abs(argv[4])
		if err != nil {
			return fail(err)
		}
		prepared, err := collaboration.PrepareEvent(store, collaboration.EventRequest{AssignmentID: a.ID, Actor: actor, Type: argv[2], Report: report, Revision: argv[6]})
		if err != nil {
			return fail(err)
		}
		if !prepared.Fresh || prepared.Event.Delivery == "not-required" {
			return collaborationJSON(prepared.Assignment)
		}
		result, sendErr := peer.SendAssignmentMessage(peer.AssignmentMessageOptions{AssignmentID: a.ID, Target: target.Name, Type: typ, Body: collaboration.EventMessage(prepared.Assignment, prepared.Event), EventID: prepared.Event.ID, Config: cfg, Env: env, Cwd: cwd})
		updated, err := collaboration.CompleteDelivery(store, prepared, result)
		if err != nil {
			return fail(err)
		}
		if err := collaborationJSON(updated); err != 0 {
			return err
		}
		if sendErr != nil {
			fmt.Fprintf(platform.Stderr, "soho: collaborate: delivery %s: %s; inspect before any new attempt\n", result.Status, result.Cause)
			return 15
		}
		return 0
	case "stop", "finalize":
		if (argv[0] == "stop" && len(argv) != 2) || (argv[0] == "finalize" && (len(argv) != 4 || argv[2] != "--verdict")) {
			return fail(errors.New("stop requires assignment; finalize requires assignment --verdict accept|reject"))
		}
		if argv[0] == "finalize" && argv[3] != "accept" && argv[3] != "reject" {
			return fail(errors.New("verdict must be accept or reject"))
		}
		core.CallerGate("collaborate", cfg, env, cwd)
		if err := collaborationOrchestrator(store.StateDir, cfg, env, svc, cwd); err != nil {
			return fail(err)
		}
		a, err = store.Read(argv[1])
		if err != nil {
			return fail(err)
		}
		if a.Workspace != caller.WorkspaceID || a.Root != platform.StateProjectRoot(env, cwd) {
			return fail(errors.New("assignment belongs to another workspace or repository"))
		}
		if argv[0] == "stop" {
			a, err = store.Stop(a.ID)
		} else {
			if argv[3] == "accept" {
				if err := collaboration.CheckParticipants(a, store.StateDir, env); err != nil {
					return fail(err)
				}
			}
			a, err = store.Finalize(a.ID, argv[3])
		}
	default:
		return fail(errors.New("expected start, status, event, stop or finalize"))
	}
	if err != nil {
		return fail(err)
	}
	return collaborationJSON(a)
}

func collaborationJSON(value any) int {
	if err := json.NewEncoder(platform.Stdout).Encode(value); err != nil {
		fmt.Fprintln(platform.Stderr, err)
		return 4
	}
	return 0
}

func collaborationPermission(cfg *core.Config, env platform.Env, a collaboration.Assignment, from, to collaboration.Participant, typ string) error {
	policy, err := communication.Load(cfg, env)
	if err != nil {
		return err
	}
	id := a.ID
	if id == "" {
		id = "new-assignment"
	}
	return policy.Authorize(communication.Request{From: communication.Participant{Name: from.Name, Role: from.Role, Lane: from.Lane}, To: communication.Participant{Name: to.Name, Role: to.Role, Lane: to.Lane}, Type: typ, AssignmentID: id, Assigned: true, Inbound: "auto"})
}

func collaborationOrchestrator(sd string, cfg *core.Config, env platform.Env, svc transport.Services, cwd string) error {
	pane := svc.Runtime.Caller().PaneID
	if pane == "" {
		return errors.New("orchestrator requires a verified current pane")
	}
	registeredName := ""
	for _, row := range core.RosterRows(sd) {
		fields := strings.Split(row, "\t")
		if len(fields) >= 4 && fields[1] == pane {
			if fields[3] != "sub-orchestrator" {
				return errors.New("operation belongs to the orchestrator, not a worker")
			}
			registeredName = fields[0]
		}
	}
	r := svc.Legacy.AgentGetRaw(context.Background(), transport.Target{ID: pane}, transport.Call{Timeout: 5 * time.Second})
	var response struct {
		Result struct {
			Agent struct {
				Name      string `json:"name"`
				Pane      string `json:"pane_id"`
				Workspace string `json:"workspace_id"`
				Cwd       string `json:"cwd"`
			} `json:"agent"`
		} `json:"result"`
	}
	if r.Err != nil || r.Process.TimedOut || json.Unmarshal([]byte(r.Value), &response) != nil {
		return errors.New("cannot verify current orchestrator")
	}
	ag := response.Result.Agent
	if ag.Pane != pane || ag.Workspace != svc.Runtime.Caller().WorkspaceID || !filepath.IsAbs(ag.Cwd) || platform.StateProjectRoot(env, ag.Cwd) != platform.StateProjectRoot(env, cwd) {
		return errors.New("current orchestrator context changed")
	}
	if registeredName != "" {
		if ag.Name == registeredName {
			return nil
		}
		return errors.New("sub-orchestrator identity changed")
	}
	base := core.Cfg(cfg, "orchestrator_name", "orchestrator", env)
	if ag.Name == base {
		return nil
	}
	if suffix := strings.TrimPrefix(ag.Name, base+"-"); suffix != ag.Name {
		if n, err := strconv.Atoi(suffix); err == nil && n > 0 {
			return nil
		}
	}
	return errors.New("current pane is not the registered orchestrator; run init in its pane")
}
