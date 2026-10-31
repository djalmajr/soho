package wait

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/backend"
	"github.com/djalmajr/soho/internal/collaboration"
	"github.com/djalmajr/soho/internal/communication"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/provider"
	"github.com/djalmajr/soho/internal/sessionref"
	"github.com/djalmajr/soho/internal/taskreport"
	"github.com/djalmajr/soho/internal/transport"
)

// probeNotInRoster runs the D11 agent get for an agent outside the roster
// and reports whether Herdr knows it (the call succeeds with an
// agent_status), plus the command form that worked, for the hint. A
// reference is resolved with sessionref.ParseRef like send: a remote
// machine is queried with `herdr --machine <m> agent get <pane>` (one
// attempt, no retry pause, known only for exit 0 with an agent_status in
// the body), and a local reference (or a bare name) takes the local query
// `herdr agent get <x>` — the pane id for a reference. A value that does
// not parse as a reference keeps the local name behavior.
func probeNotInRoster(agent string, svc transport.Services) (bool, string) {
	target, machine := agent, sessionref.LocalMachine
	if ref := sessionref.ParseRef(agent); ref != nil {
		target, machine = ref.PaneID, ref.Machine
	}
	if args := sessionref.HerdrMachineArgs(machine); len(args) > 0 {
		query := append(append([]string{}, args...), "agent", "get", target)
		known := svc.Legacy.AgentGetKnown(context.Background(), transport.Target{Machine: machine, ID: target}, transport.Call{Timeout: herdr.Timeout}).Value
		return known, "herdr " + strings.Join(query, " ")
	}
	st := readState(svc, target)
	return st.State != "gone" && st.State != "unavailable", "herdr agent get " + target
}

func aliveInOtherPane(name, pane string, svc transport.Services) (alive bool) {
	defer func() {
		if recover() != nil {
			alive = false
		}
	}()
	for _, term := range svc.Runtime.List(context.Background(), transport.Scope{}, transport.Call{Timeout: herdr.Timeout}).Value {
		if term.Name != name {
			continue
		}
		if term.PaneID == pane {
			return false
		}
		alive = true
	}
	return alive
}

func statusJson(agent, state, report string, fields ...any) {
	pairs := []any{"agent", agent, "status", state, "report", report}
	pairs = append(pairs, fields...)
	jsonLine(jsonjs.O(pairs...))
}

func rosterNames(sd string) []string {
	names := []string{}
	for _, row := range core.RosterRows(sd) {
		name := strings.Split(row, "\t")[0]
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

func CmdStatus(argv []string, ctx *core.Config, env platform.Env, cwd string) int {
	sd := core.StateDir(ctx, env, cwd)
	svc := backend.New(env)
	restartStates := map[string]stateProbe{}
	if len(argv) == 0 {
		argv = rosterNames(sd)
		if len(argv) == 0 {
			restartNotices(sd, env, platform.Stderr, nil, restartStates, svc)
			fmt.Fprintf(platform.Stderr, "status: no agents in the roster (state dir: %s)\n", sd)
			return 0
		}
	}
	rc := 0
	workspaceEnv := derivedWorkspaceEnv(ctx, env, cwd)
	for _, agent := range argv {
		report := core.LastReport(sd, agent)
		report = observeReport(sd, agent, report, ctx, env, cwd)
		// F11: a recorded compact phase outranks every other state, including a
		// stale complete report: while its marker stands, the brief was not
		// sent, so no report for it can exist. The marker is only read; an
		// agent outside the roster, or a roster pane that no longer matches the
		// record's pane, falls through to the usual state.
		if line := core.RosterLine(sd, agent); line != "" {
			if out, code, ok := compactPhaseStatusLine(line, sd, agent, env); ok {
				// 9 ranks 0 (4 does not): a first non-zero code still
				// replaces the zero rc; the existing precedence then
				// decides the rest.
				if code != 0 && (rc == 0 || WaitRank(code) > WaitRank(rc)) {
					rc = code
				}
				fmt.Fprint(platform.Stdout, out)
				continue
			}
			// An active collaboration outranks the usual state, including a
			// stale complete report: while its assignment is active the
			// participants are still working, so the old report is an artifact
			// only. An unreadable or unverifiable registration and an off or
			// unreadable policy are visible (code 4), never the report's done.
			if cs, ok := readCollaboration(sd, field(strings.Split(line, "\t"), 1), ctx, workspaceEnv); ok {
				printCollaborationStatus(agent, cs, report)
				if cs.code != 0 && (rc == 0 || WaitRank(cs.code) > WaitRank(rc)) {
					rc = cs.code
				}
				continue
			}
		}
		state, cause := "", ""
		match, renewal, kind, model, lane, question := "", "", "", "", "", ""
		quota := false
		var p *provider.Detection
		if reportNonEmpty(report) {
			state, cause = reportStatus(sd, agent, report, ctx, svc, env)
			if state == "done" {
				mirrorReport(sd, agent, report, ctx, env, cwd)
			}
		} else if core.RosterLine(sd, agent) == "" {
			// D11: an agent outside this workspace's roster that Herdr knows —
			// a local `agent get`, or the reference machine's, succeeds (find
			// showed it: done or blocked) — is `not-in-roster`, not
			// `unknown-agent`, which now stays for what Herdr does not know
			// either (agent_not_found, or a failed agent get). The probe only
			// runs here, on the not-in-roster branch: roster agents take the
			// branch below and spend no extra agent get. A reference
			// (machine/pane) is resolved like send: the query goes to the
			// reference's machine, and a local reference or a bare name takes
			// the local query; the hint cites the command form that worked.
			if known, cmd := probeNotInRoster(agent, svc); known {
				state = "not-in-roster"
				fmt.Fprintf(platform.Stderr, "soho: status: '%s' is not a worker of this workspace's team; %s shows its state\n", agent, cmd)
			} else {
				state = "unknown-agent"
			}
		} else {
			fields := strings.Split(core.RosterLine(sd, agent), "\t")
			pane := field(fields, 1)
			st := readState(svc, agent)
			state, cause = st.State, st.Cause
			orig := state
			stale := pane != "" && state != "gone" && state != "unavailable" && aliveInOtherPane(agent, pane, svc)
			if stale {
				state = "gone"
				cause = ""
			}
			if state == "idle" || state == "done" {
				state = "no-report-yet"
			}
			if state == "unavailable" {
				if rc != 11 {
					rc = 4
				}
				core.Warn(fmt.Sprintf("agent '%s': herdr agent get failed: %s", agent, cause), frictionLogFile, "status")
			} else if !stale && markerExists(sd, agent, "not-received") && orig != "working" && orig != "blocked" && !MarkerSeqChanged(markerRead(sd, agent, "not-received"), st.Seq) {
				state = "not-received"
				if WaitRank(15) > WaitRank(rc) {
					rc = 15
				}
			} else if !stale && markerExists(sd, agent, "queued") && orig != "working" && orig != "blocked" && queuedPromptIsUnresolved(sd, agent, orig, ctx, env, svc) {
				state = "not-received"
				if WaitRank(15) > WaitRank(rc) {
					rc = 15
				}
			} else if !stale && orig == "blocked" {
				kindNow := field(fields, 2)
				visible, _ := readScreen(svc, agent, "visible", intPtr(40))
				if provider.DialogKind(kindNow, visible) == "question" {
					question = provider.QuestionText(visible)
					if WaitRank(7) > WaitRank(rc) {
						rc = 7
					}
				} else {
					blockedRecent, _ := readScreen(svc, agent, "recent-unwrapped", intPtr(40))
					p = provider.ProviderDetectTexts(orig, blockedRecent, core.Cfg(ctx, "provider_capacity_texts", "", env), core.Cfg(ctx, "provider_error_texts", "", env))
					if p != nil && ((p.Status == "provider-error" && p.Auth) || p.Status == "provider-refusal" || p.Cause == provider.LostConnectionCause) {
						if WaitRank(14) > WaitRank(rc) {
							rc = 14
						}
					}
				}
			} else if !stale && orig != "working" && orig != "gone" && orig != "blocked" && orig != "unavailable" {
				quotaVisible, _ := readScreen(svc, agent, "visible", intPtr(20))
				q := provider.QuotaDetect(orig, quotaVisible)
				if q != nil {
					quota = true
					match, renewal = q[0], q[1]
					kind, model, lane = field(fields, 2), field(fields, 8), field(fields, 11)
					if lane == "" {
						lane = core.LaneOfRole(ctx, TaskRole(sd, agent), env)
					}
					core.Warn(fmt.Sprintf("quota: agent '%s' lane=%s kind=%s model=%s : %s%s", agent, showOr(lane, "?"), kind, showOr(model, "?"), match, renewalSuffix(renewal)), frictionLogFile, "status")
					rc = 11
				} else {
					unresolvedRecent, _ := readScreen(svc, agent, "recent-unwrapped", intPtr(40))
					p = provider.ProviderDetectTexts(orig, unresolvedRecent, core.Cfg(ctx, "provider_capacity_texts", "", env), core.Cfg(ctx, "provider_error_texts", "", env))
					if p != nil && WaitRank(14) > WaitRank(rc) {
						rc = 14
					}
				}
			}
			if !stale && !quota && p == nil && question == "" {
				restartStates[agent] = st
			}
		}
		var taskS, activityS any
		if info, e := os.Stat(core.LastReportPath(sd, agent)); e == nil {
			end := platform.Now()
			if reportNonEmpty(report) {
				if reportInfo, e := os.Stat(report); e == nil {
					end = reportInfo.ModTime()
				}
			}
			taskS = math.Round(end.Sub(info.ModTime()).Seconds())
		}
		activityChanged := false
		if state == "working" {
			screen, _ := readScreen(svc, agent, "visible", nil)
			p = provider.LostConnection(screen)
			if p != nil && WaitRank(14) > WaitRank(rc) {
				rc = 14
			}
			age, changed := ActivityAgeSeconds(sd, agent, screen, platform.Now().Unix())
			if age != nil {
				activityS = *age
			}
			activityChanged = changed
			// A retry is not a stop: the state stays working and the exit
			// code is unchanged; only the cause column names the retry.
			if retryLine := provider.RetryLine(state, screen); retryLine != "" {
				cause = "retrying: " + retryLine
			}
		}
		// DJA-565: the wait's idle-alert mark, read from its local marker:
		// shown only when the marker's task is this worker's current task and
		// the worker is working. A local file read — no screen read, no new
		// transport call.
		var idleMinutes int64
		if state == "working" {
			idleMinutes = idleAlertMark(sd, agent, report)
		}
		// Like the retry line, the mark goes in the cause column: the state
		// stays working for every reader that compares it.
		if idleMinutes > 0 {
			idle := fmt.Sprintf("idle: no new output for %d min", idleMinutes)
			if cause == "" {
				cause = idle
			} else {
				cause += "; " + idle
			}
		}
		ageFields := []any{}
		if idleMinutes > 0 {
			ageFields = append(ageFields, "idle_minutes", idleMinutes)
		}
		ageFields = append(ageFields, "task_s", taskS, "activity_s", activityS)
		if activityChanged {
			ageFields = append(ageFields, "activity_changed", true)
		}
		if quota {
			statusJson(agent, "quota", report, append([]any{"lane", lane, "kind", kind, "model", model, "match", match, "renewal", renewal}, ageFields...)...)
		} else if p != nil {
			if p.Status == "provider-refusal" || p.Cause == provider.LostConnectionCause {
				fields := strings.Split(core.RosterLine(sd, agent), "\t")
				lane = field(fields, 11)
				if lane == "" {
					lane = core.LaneOfRole(ctx, field(fields, 3), env)
				}
				statusJson(agent, p.Status, report, append([]any{"cause", p.Cause, "lane", lane, "kind", field(fields, 2), "model", field(fields, 8)}, ageFields...)...)
			} else {
				statusJson(agent, p.Status, report, append([]any{"cause", p.Cause}, ageFields...)...)
			}
		} else if question != "" {
			statusJson(agent, "question", report, append([]any{"question", question}, ageFields...)...)
		} else {
			taskCol, activityCol := "-", "-"
			if taskS != nil {
				taskCol = fmt.Sprint(taskS)
			}
			if activityS != nil {
				activityCol = fmt.Sprint(activityS)
			} else if activityChanged {
				activityCol = "changed"
			}
			if cause != "" {
				fmt.Fprintf(platform.Stdout, "%s\t%s\t%s\t%s\t%s\t%s\n", agent, state, report, cause, taskCol, activityCol)
			} else {
				fmt.Fprintf(platform.Stdout, "%s\t%s\t%s\t%s\t%s\n", agent, state, report, taskCol, activityCol)
			}
		}
	}
	restartNotices(sd, env, platform.Stderr, argv, restartStates, svc)
	return rc
}

func markerExists(sd, agent, name string) bool {
	_, err := os.Stat(filepath.Join(sd, "wait", agent+"."+name))
	return err == nil
}

// compactPhaseStatusLine reports the F11 compact-phase marker for a roster
// line whose pane matches the record's pane, without modifying the marker:
// compacting (an alive owner, exit 0), compact-interrupted (a dead owner or
// a different start, exit 9) and compact-unknown (an unverifiable owner,
// exit 4); a corrupted or unreadable marker is a visible error (exit 4) that
// is never removed. ok is false when there is no marker or the record's pane
// no longer matches (the usual state logic stands).
func compactPhaseStatusLine(rosterLine, sd, agent string, env platform.Env) (string, int, bool) {
	path := core.CompactPendingPath(sd, agent)
	rec, err := core.CompactPendingRead(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", 0, false
		}
		core.Warn(fmt.Sprintf("status: the compact-pending marker %s of '%s' is unreadable; it was left in place", path, agent), frictionLogFile, "status")
		return fmt.Sprintf("%s\tcompact-unknown\t\tcompact-pending marker unreadable: %s; brief was not sent\t-\t-\n", agent, path), 4, true
	}
	if rec.Pane != field(strings.Split(rosterLine, "\t"), 1) {
		return "", 0, false
	}
	cause := fmt.Sprintf("brief was not sent: %s", rec.Brief)
	switch core.CompactPendingOwner(rec, env) {
	case core.CompactOwnerAlive:
		return fmt.Sprintf("%s\tcompacting\t\t%s\t-\t-\n", agent, cause), 0, true
	case core.CompactOwnerDead:
		return fmt.Sprintf("%s\tcompact-interrupted\t\t%s\t-\t-\n", agent, cause), 9, true
	default:
		return fmt.Sprintf("%s\tcompact-unknown\t\t%s\t-\t-\n", agent, cause), 4, true
	}
}

// collaborationState is the result of readCollaboration for one pane: the
// line to print (collaborating, collaboration-unavailable or
// collaboration-disabled), the exit code (0 active, 4 the two failures), the
// assignment when active and the last event's delivery.
type collaborationState struct {
	state    string
	code     int
	cause    string
	assign   *collaboration.Assignment
	delivery string
}

// readCollaboration reports the active collaboration of the agent's roster
// pane (if any): its participants verified against the live roster and
// Herdr, and the worker_messages mode read from the single policy engine
// (communication.Load, used only for the mode). env is a function so the
// HERDR_WORKSPACE_ID derivation (at most one herdr call) happens only when
// an active collaboration actually exists. ok is false when no active
// collaboration hosts the pane: the usual state logic then stands, and an
// agent without a pane never reaches the collaboration store.
func readCollaboration(sd, pane string, ctx *core.Config, env func() platform.Env) (collaborationState, bool) {
	if pane == "" {
		return collaborationState{}, false
	}
	store := collaboration.Store{StateDir: sd}
	a, err := store.ActiveFor(pane)
	if err != nil {
		return collaborationState{state: "collaboration-unavailable", code: 4, cause: err.Error()}, true
	}
	if a == nil {
		return collaborationState{}, false
	}
	callerEnv := env()
	if err := collaboration.CheckParticipants(*a, sd, callerEnv); err != nil {
		// The assignment is kept in the state (and in the view exported for
		// collect) when it was read: even a failing identity check or an off
		// policy names the collaboration the pane is hosting.
		return collaborationState{state: "collaboration-unavailable", code: 4, cause: err.Error(), assign: a}, true
	}
	pol, err := communication.Load(ctx, callerEnv)
	if err != nil {
		return collaborationState{state: "collaboration-disabled", code: 4, cause: err.Error(), assign: a}, true
	}
	if pol.Mode != communication.ModePolicy {
		return collaborationState{state: "collaboration-disabled", code: 4, cause: "worker_messages is " + pol.Mode, assign: a}, true
	}
	return collaborationState{state: "collaborating", assign: a, delivery: lastEventDelivery(a)}, true
}

// lastEventDelivery names the delivery of the assignment's last event, or
// none before any event exists.
func lastEventDelivery(a *collaboration.Assignment) string {
	if len(a.Events) == 0 {
		return "none"
	}
	return a.Events[len(a.Events)-1].Delivery
}

// printCollaborationStatus prints the collaboration line in the JSON form
// the decision asks for, with the stale report carried as an artifact only:
// the active line names the assignment, phase, round, revision and the last
// delivery; the failures carry the cause.
func printCollaborationStatus(agent string, cs collaborationState, report string) {
	switch cs.state {
	case "collaborating":
		statusJson(agent, "collaborating", report, "assignment", cs.assign.ID, "phase", cs.assign.Phase, "round", cs.assign.Round, "revision", cs.assign.Revision, "delivery", cs.delivery)
	case "collaboration-unavailable":
		statusJson(agent, "collaboration-unavailable", report, "cause", cs.cause)
	default:
		statusJson(agent, "collaboration-disabled", report, "cause", cs.cause)
	}
}

// CollaborationView is the read-only collaboration inspection shared by
// status, wait and collect: the active assignment of the agent's roster
// pane (if any), with its participants verified against the live roster and
// Herdr and the worker_messages mode read from the single policy engine
// (communication.Load). The assignment is kept even when the state is a
// failure, whenever it was read, so a caller can still name the
// collaboration. env is a function so the HERDR_WORKSPACE_ID derivation
// (at most one herdr call) happens only on first use.
type CollaborationView struct {
	State      string                    // "collaborating", "collaboration-unavailable" or "collaboration-disabled"
	Code       int                       // 0 when collaborating, 4 for the two failures
	Cause      string                    // the failure cause
	Assignment *collaboration.Assignment // the valid assignment, whenever it was read
	Delivery   string                    // the last event's delivery, or "none"
}

// CollaborationViewForPane exports readCollaboration for collect: the same
// read-only inspection (Store.ActiveFor, CheckParticipants and
// communication.Load for the mode only), never mutating collaboration
// state. ok is false when no active collaboration hosts the pane: the
// caller's usual logic then stands.
func CollaborationViewForPane(sd, pane string, ctx *core.Config, env func() platform.Env) (CollaborationView, bool) {
	cs, ok := readCollaboration(sd, pane, ctx, env)
	if !ok {
		return CollaborationView{}, false
	}
	return CollaborationView{State: cs.state, Code: cs.code, Cause: cs.cause, Assignment: cs.assign, Delivery: cs.delivery}, true
}

// DerivedWorkspaceEnv yields the env the collaboration helpers take: the
// caller's env when it already carries HERDR_WORKSPACE_ID, else a clone with
// the id derived once via core.WorkspaceID (like the rest of the CLI). The
// caller's env is never mutated and the derivation runs at most once per
// command, on first use.
func DerivedWorkspaceEnv(ctx *core.Config, env platform.Env, cwd string) func() platform.Env {
	if env.Get("HERDR_WORKSPACE_ID") != "" {
		return func() platform.Env { return env }
	}
	var derived platform.Env
	return func() platform.Env {
		if derived == nil {
			clone := env.Clone()
			clone["HERDR_WORKSPACE_ID"] = core.WorkspaceID(ctx, env, cwd)
			derived = clone
		}
		return derived
	}
}

func derivedWorkspaceEnv(ctx *core.Config, env platform.Env, cwd string) func() platform.Env {
	return DerivedWorkspaceEnv(ctx, env, cwd)
}

func markerRead(sd, agent, name string) string {
	value, _ := platform.ReadTextFile(filepath.Join(sd, "wait", agent+"."+name))
	return value
}

// idleAlertMark reads the wait's idle-alert marker (<task>\t<minutes>\t<unix>,
// DJA-565) and returns its minutes when the marker's task equals the
// worker's current task; a missing or malformed marker is no mark. It is a
// local file read only: status adds no screen read or transport call for
// the mark.
func idleAlertMark(sd, agent, report string) int64 {
	raw, ok := readWaitFile(sd, agent, "idle-alert")
	if !ok || raw == "" {
		return 0
	}
	fields := strings.Split(raw, "\t")
	if len(fields) != 3 || fields[0] != idleTask(sd, agent, report) {
		return 0
	}
	minutes, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil || minutes < 0 {
		return 0
	}
	if _, err := strconv.ParseInt(fields[2], 10, 64); err != nil {
		return 0
	}
	return minutes
}
func queuedPromptIsUnresolved(sd, agent, state string, ctx *core.Config, env platform.Env, svc transport.Services) bool {
	marker := markerRead(sd, agent, "queued")
	visible, _ := readScreen(svc, agent, "visible", nil)
	recent, _ := readScreen(svc, agent, "recent-unwrapped", intPtr(40))
	if !QueuedPromptSitsInInput(marker, recent, sd, agent) {
		return false
	}
	if provider.QuotaDetect(state, visible) != nil {
		return false
	}
	unresolvedRecent, _ := readScreen(svc, agent, "recent-unwrapped", intPtr(40))
	return provider.ProviderDetectTexts(state, unresolvedRecent, core.Cfg(ctx, "provider_capacity_texts", "", env), core.Cfg(ctx, "provider_error_texts", "", env)) == nil
}

// RestartTask describes the existing task; notices never change its pointer.
type RestartTask struct {
	Agent, Current, Brief, State, Reason string
	Code                                 int
}

var restartReportStamp = regexp.MustCompile(`-(\d{8}T\d{6})(?:-\d+)?\.md$`)

// InspectRestartTask uses the local timestamp dispatch puts in the report name.
// The dispatch sidecar has submission state, but no submission timestamp.
func InspectRestartTask(agent, sd string, boot time.Time, env platform.Env) RestartTask {
	return inspectRestartTask(agent, sd, boot, backend.New(env), nil)
}

func inspectRestartTask(agent, sd string, boot time.Time, svc transport.Services, knownState *stateProbe) RestartTask {
	task := RestartTask{Agent: agent, Code: 2, Reason: "task is not interrupted"}
	if core.RosterLine(sd, agent) == "" {
		task.Reason = "agent is not in the roster"
		return task
	}
	pointer := taskreport.ReadTaskReportPointer(sd, agent)
	if pointer == nil {
		return task
	}
	current, _ := pointer.Get("current")
	task.Current, _ = current.(string)
	if task.Current == "" || reportNonEmpty(task.Current) {
		return task
	}
	stamp := restartReportStamp.FindStringSubmatch(filepath.Base(task.Current))
	if len(stamp) != 2 {
		return task
	}
	dispatched, err := time.ParseInLocation("20060102T150405", stamp[1], time.Local)
	if err != nil {
		return task
	}
	now := platform.Now()
	if boot.After(now) || dispatched.After(now) {
		task.Code = 4
		task.Reason = "the boot time is inconsistent with the current clock; nothing was sent"
		return task
	}
	if !dispatched.Before(boot) {
		return task
	}
	var state stateProbe
	if knownState != nil {
		state = *knownState
	} else {
		state = readState(svc, agent)
	}
	task.State = state.State
	if state.State == "working" || state.State == "blocked" {
		task.Code = 10
		task.Reason = "agent is " + state.State
		return task
	}
	if state.State != "idle" && state.State != "done" {
		task.Reason = "agent is " + state.State
		return task
	}
	base := strings.TrimSuffix(filepath.Base(task.Current), ".md")
	task.Brief = filepath.Join(sd, "briefs", base+".md")
	if filepath.Clean(filepath.Dir(task.Current)) != filepath.Join(sd, "reports") {
		// Scratch artifacts may have disappeared with /tmp; retain their path.
		task.Brief = filepath.Join(filepath.Dir(task.Current), base+".brief.md")
	}
	task.Code = 0
	task.Reason = "task was dispatched before the machine restarted"
	return task
}

// RestartNotices adds advisory text only; stdout status rows remain unchanged.
func RestartNotices(sd string, env platform.Env, out io.Writer, agents []string) {
	restartNotices(sd, env, out, agents, nil, backend.New(env))
}

// A non-nil cache limits status notices to its normal-output state reads.
func restartNotices(sd string, env platform.Env, out io.Writer, agents []string, states map[string]stateProbe, svc transport.Services) {
	boot, err := platform.BootTime()
	if err != nil || boot.IsZero() || boot.After(platform.Now()) {
		return
	}
	if len(agents) == 0 {
		agents = rosterNames(sd)
	}
	for _, agent := range agents {
		var knownState *stateProbe
		if states != nil {
			cached, ok := states[agent]
			if !ok {
				continue
			}
			knownState = &cached
		}
		if task := inspectRestartTask(agent, sd, boot, svc, knownState); task.Code == 0 {
			fmt.Fprintf(out, "'%s': its task was dispatched before the machine restarted (%s); run soho resume %s\n", agent, boot.Local().Format(time.RFC3339), agent)
		}
	}
	rows, err := core.ReadProcs(sd)
	if err != nil {
		return
	}
	for _, row := range rows {
		started, err := restartProcStart(row.Started)
		if err == nil && started.Before(boot) && core.ProcRowState(row, env) == "gone" {
			fmt.Fprintf(out, "proc %d of '%s' (%s) ended with the restart\n", row.Pid, row.Owner, row.Name)
		}
	}
}
func restartProcStart(raw string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return t, nil
	}
	if t, err := time.ParseInLocation(time.ANSIC, raw, time.Local); err == nil {
		return t, nil
	}
	ticks, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || ticks < 116444736000000000 {
		return time.Time{}, fmt.Errorf("unknown process start time")
	}
	ticks -= 116444736000000000
	return time.Unix(ticks/10000000, (ticks%10000000)*100), nil
}
