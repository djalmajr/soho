package wait

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
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

// aliveInOtherPane reports whether the roster name runs in another pane than
// the roster pane: alive is the comparison result, observed whether the
// listing itself succeeded (a failed or panicking listing reports
// observed=false with alive=false, as the die-and-recover path always did).
func aliveInOtherPane(name, pane string, svc transport.Services, scope transport.Scope) (alive, observed bool) {
	defer func() {
		if recover() != nil {
			alive, observed = false, false
		}
	}()
	reply := svc.Runtime.List(context.Background(), scope, transport.Call{Timeout: herdr.Timeout})
	if reply.Err != nil {
		return false, false
	}
	for _, term := range reply.Value {
		if term.Name != name {
			continue
		}
		if term.PaneID == pane {
			return false, true
		}
		alive = true
	}
	return alive, true
}

// statusJsonLine builds the JSON status line exactly as status has always
// printed it: the same key order and the same trailing newline.
func statusJsonLine(agent, state, report string, fields ...any) string {
	pairs := []any{"agent", agent, "status", state, "report", report}
	pairs = append(pairs, fields...)
	return jsonjs.Stringify(jsonjs.O(pairs...)) + "\n"
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
	// The lazy session derivation status always had: the scratch-mirror
	// observation and the collaboration workspace context resolve the
	// caller's session on first need (core.WorkspaceID), never per agent.
	session := func() string { return core.WorkspaceID(ctx, env, cwd) }
	workspaceEnv := derivedWorkspaceEnv(ctx, env, cwd)
	rc := 0
	for _, agent := range argv {
		var res agentStatusResult
		res, rc = resolveAgentStatus(agentStatusRequest{
			stateDir:     sd,
			session:      session,
			agent:        agent,
			config:       ctx,
			env:          env,
			services:     svc,
			workspaceEnv: workspaceEnv,
		}, rc)
		// The write side of the resolution: the friction warnings, the
		// not-in-roster hint and the report mirror are decided by the shared
		// resolver and applied here, in the same order the command always
		// applied them.
		if res.compactUnreadable {
			core.Warn(fmt.Sprintf("status: the compact-pending marker %s of '%s' is unreadable; it was left in place", res.compactPath, agent), frictionLogFile, "status")
		}
		if res.notInRosterCmd != "" {
			fmt.Fprintf(platform.Stderr, "soho: status: '%s' is not a worker of this workspace's team; %s shows its state\n", agent, res.notInRosterCmd)
		}
		if res.unavailable {
			core.Warn(fmt.Sprintf("agent '%s': herdr agent get failed: %s", agent, res.unavailableCause), frictionLogFile, "status")
		}
		if res.quota {
			core.Warn(fmt.Sprintf("quota: agent '%s' lane=%s kind=%s model=%s : %s%s", agent, showOr(res.quotaLane, "?"), res.quotaKind, showOr(res.quotaModel, "?"), res.quotaMatch, renewalSuffix(res.quotaRenewal)), frictionLogFile, "status")
		}
		if res.mirror {
			mirrorReport(sd, agent, res.report, ctx, env, cwd)
		}
		if res.line != "" {
			fmt.Fprint(platform.Stdout, res.line)
		}
		if res.restartValid {
			restartStates[agent] = res.restart
		}
	}
	restartNotices(sd, env, platform.Stderr, argv, restartStates, svc)
	return rc
}

// AgentStatusRequest is the explicit known-session input to a read-only
// status inspection: the state dir, session id, agent and caller cwd the
// producer already knows, plus the config and env the caller carries and
// the transport services to probe through. Nothing is auto-discovered in
// this API: the caller supplies the session it inspects. Config is the
// loaded config, as CmdStatus takes it. Source is the safely acquired
// known-session metadata source (nil keeps the ordinary path readers and
// the request's StateDir; a non-nil source confines every metadata read
// under its opened roots and supersedes StateDir).
type AgentStatusRequest struct {
	StateDir  string
	SessionID string
	Agent     string
	Cwd       string
	Config    *core.Config
	Env       platform.Env
	Services  transport.Services
	Source    *core.SessionSource
}

// AgentStatusView is the read-only result of InspectAgentStatus: the
// effective status the status command renders (question/quota/provider
// detection overrides the underlying state just as CmdStatus prints it),
// the underlying probe's state word ("" when the deciding branch ran no
// probe) and whether a nonempty current report was observed. No report
// path, cause or dialog text, screen, model, argv or command output
// leaves the resolver: it is an internal Go API, not a wire contract.
type AgentStatusView struct {
	State         string
	TerminalState string
	ReportReady   bool
}

// InspectAgentStatus resolves one agent's status through the exact
// resolver CmdStatus renders, without printing or writing anything: no
// stdout or stderr, no friction line, no report mirror, no marker or
// state write, no mkdir, no clock advance, no state repair or cleanup.
// It stays silent even when the package friction file was configured by
// another caller. The request's explicit session is used for the
// scratch-report mirror observation and the collaboration workspace
// context; no caller, current-pane or session discovery call runs, and
// the caller's Env is never mutated.
func InspectAgentStatus(request AgentStatusRequest) AgentStatusView {
	res, _ := resolveAgentStatus(agentStatusRequest{
		stateDir:       request.StateDir,
		session:        func() string { return request.SessionID },
		agent:          request.Agent,
		config:         request.Config,
		env:            request.Env,
		services:       request.Services,
		workspaceEnv:   explicitWorkspaceEnv(request.Env, request.SessionID),
		knownWorkspace: request.SessionID,
		listScope:      transport.Scope{All: true},
		inspect:        true,
		source:         request.Source,
	}, 0)
	return AgentStatusView{State: res.state, TerminalState: res.terminal, ReportReady: res.reportReady}
}

// agentStatusRequest is the per-agent input to the shared status
// resolution. session and workspaceEnv are lazy: session is consulted
// only when the scratch-mirror check needs it and workspaceEnv only when
// an active collaboration actually exists, exactly the lazy derivation
// the status command always had. knownWorkspace and listScope are the
// read-only inspect's explicit inputs ("" and Scope{} for every current
// caller): the known workspace is verified by the collaboration check
// instead of resolving the caller, and the stale-pane comparison lists
// the unfiltered local terminals instead of the caller-scoped default.
// inspect is the read-only marker: it selects the bounded nonblocking
// compact-marker read, the read-only report observation (no skill
// materialization, no Die) and the unavailable outcome when the
// inspection's stale-pane listing is unverifiable. source is the safely
// acquired known-session metadata source (nil for every current caller):
// when set, the state dir is the source's, the roster comes from its
// acquired snapshot and every local metadata read goes through its
// confined bounded readers.
type agentStatusRequest struct {
	stateDir       string
	session        func() string
	agent          string
	config         *core.Config
	env            platform.Env
	services       transport.Services
	workspaceEnv   func() platform.Env
	knownWorkspace string
	listScope      transport.Scope
	inspect        bool
	source         *core.SessionSource
}

// agentStatusResult is the shared per-agent resolution: the effective
// status the status command renders, the underlying probe's state word,
// the observed report, the exact output line, the write side effects the
// wrapper applies (the friction warnings, the not-in-roster hint and the
// report mirror) and the state probe the restart notices may reuse. The
// resolver performs no write of its own: it records what the wrapper
// must do, and the wrapper does it.
type agentStatusResult struct {
	state             string // the effective rendered status
	terminal          string // the underlying probe's state word ("" when none)
	report            string // the observed report path
	reportReady       bool   // a nonempty current report was observed
	line              string // the exact line the status command prints
	compactUnreadable bool
	compactPath       string
	notInRosterCmd    string
	unavailable       bool
	unavailableCause  string
	quota             bool
	quotaLane         string
	quotaKind         string
	quotaModel        string
	quotaMatch        string
	quotaRenewal      string
	mirror            bool
	restart           stateProbe
	restartValid      bool
}

// resolveAgentStatus is the per-agent status resolution CmdStatus renders
// and the read-only inspect shares: the exact precedence (the compact
// phase marker, the active collaboration, the report, the roster/agent
// get, the not-received and queued markers, the blocked screen,
// quota/provider, the working activity and retry marks), the rc ranking
// and the printed line. It performs no write of any kind — the warnings,
// the mirror and the line it decides are recorded in the result and
// applied by the caller — so the read-only caller can run it against an
// explicit foreign session and stay silent.
func resolveAgentStatus(req agentStatusRequest, rc int) (agentStatusResult, int) {
	sd := req.stateDir
	if req.source != nil {
		sd = req.source.StateDir()
	}
	ctx := req.config
	env := req.env
	svc := req.services
	agent := req.agent
	var res agentStatusResult
	if req.source != nil && !platform.ValidStateComponent(agent) {
		// The inspection's candidate name is not a safe state component:
		// the candidate is suppressed as unknown without any file read or
		// transport call (the command path keeps its ordinary behavior).
		res.state = "unknown"
		return res, rc
	}
	report, reportUnsafe := reportPointer(req, agent)
	if reportUnsafe {
		// Poisoned required report evidence (an unsafe or oversize
		// last-report pointer): the inspection outcome is unknown, never a
		// fabricated settled report — no probe and no report-ready.
		res.state = "unknown"
		return res, rc
	}
	if req.source != nil {
		var unsafe bool
		report, unsafe = observeReportInspect(req, agent, report)
		if unsafe {
			res.state = "unknown"
			return res, rc
		}
	} else {
		report = observeReportSession(sd, agent, report, req.session)
	}
	res.report = report
	res.reportReady = reportNonEmptyFor(req, agent, report)
	// F11: a recorded compact phase outranks every other state, including a
	// stale complete report: while its marker stands, the brief was not
	// sent, so no report for it can exist. The marker is only read; an
	// agent outside the roster, or a roster pane that no longer matches the
	// record's pane, falls through to the usual state.
	row, inRoster := rosterRow(req, agent)
	if inRoster {
		pane := field(row.Fields, 1)
		if out, code, state, unreadable, ok := compactPhaseStatusFor(pane, sd, agent, env, req.inspect, req.source); ok {
			// 9 ranks 0 (4 does not): a first non-zero code still
			// replaces the zero rc; the existing precedence then
			// decides the rest.
			if code != 0 && (rc == 0 || WaitRank(code) > WaitRank(rc)) {
				rc = code
			}
			res.state = state
			res.line = out
			if unreadable {
				res.compactUnreadable = true
				res.compactPath = core.CompactPendingPath(sd, agent)
			}
			return res, rc
		}
		// An active collaboration outranks the usual state, including a
		// stale complete report: while its assignment is active the
		// participants are still working, so the old report is an artifact
		// only. An unreadable or unverifiable registration and an off or
		// unreadable policy are visible (code 4), never the report's done.
		if cs, ok := readCollaboration(sd, pane, ctx, req.workspaceEnv, req.knownWorkspace, req.source); ok {
			res.state = cs.state
			res.line = collaborationStatusLine(agent, cs, report)
			if cs.code != 0 && (rc == 0 || WaitRank(cs.code) > WaitRank(rc)) {
				rc = cs.code
			}
			return res, rc
		}
	}
	state, cause := "", ""
	var terminal string // the probe's state word when the branch probed (F3)
	match, renewal, kind, model, lane, question := "", "", "", "", "", ""
	quota := false
	var p *provider.Detection
	if reportNonEmptyFor(req, agent, report) {
		state, cause, terminal = reportObservation(sd, agent, report, ctx, svc, env, req.inspect, req.source)
		res.terminal = terminal
		if state == "done" {
			res.mirror = true
		}
	} else if !inRoster {
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
			res.notInRosterCmd = cmd
		} else {
			state = "unknown-agent"
		}
	} else {
		fields := row.Fields
		pane := field(fields, 1)
		st := readState(svc, agent)
		res.terminal = st.State
		state, cause = st.State, st.Cause
		orig := state
		stale := false
		var unverified bool
		if pane != "" && state != "gone" && state != "unavailable" {
			alive, observed := aliveInOtherPane(agent, pane, svc, req.listScope)
			stale = alive
			// The inspection's unfiltered listing failed: the stale-pane
			// identity is unverifiable, and the view keeps the existing
			// unavailable inspection outcome instead of claiming a verified
			// working identity. The command's default scope (die-and-recover
			// or caller-scoped listing) keeps its semantics unchanged.
			unverified = !observed && req.inspect
		}
		if stale {
			state = "gone"
			cause = ""
		} else if unverified {
			state = "unavailable"
			cause = "the agent list that verifies the pane identity failed"
		}
		if state == "idle" || state == "done" {
			state = "no-report-yet"
		}
		// Evaluate the queued consumer only at its existing precedence point.
		// Its error belongs to this resolution, never a shared error slot.
		var queuedErr error
		queuedUnresolved := func() bool {
			var unresolved bool
			unresolved, queuedErr = queuedPromptIsUnresolved(req, agent, orig, ctx, env, svc)
			return unresolved || queuedErr != nil
		}
		if state == "unavailable" {
			if rc != 11 {
				rc = 4
			}
			res.unavailable = true
			res.unavailableCause = cause
		} else if !stale && orig != "working" && orig != "blocked" && requiredMarkerPoisoned(req, agent, "not-received") {
			// The required not-received marker exists but cannot be read as
			// safe metadata (the inspection's confined read refused it):
			// the conservative outcome is unknown, never the absence it
			// would otherwise read as — the not-received decision cannot
			// fire on unreadable evidence, and neither can a fabricated
			// settled state. The command path keeps its ordinary reader.
			state = "unknown"
			cause = "the not-received marker exists but cannot be read as safe metadata"
			if WaitRank(4) > WaitRank(rc) {
				rc = 4
			}
		} else if !stale && waitMarkerExists(req, agent, "not-received") && orig != "working" && orig != "blocked" && !MarkerSeqChanged(waitMarkerValue(req, agent, "not-received"), st.Seq) {
			state = "not-received"
			if WaitRank(15) > WaitRank(rc) {
				rc = 15
			}
		} else if !stale && orig != "working" && orig != "blocked" && requiredMarkerPoisoned(req, agent, "queued") {
			// The required queued marker exists but cannot be read as safe
			// metadata: the same conservative unknown (the queued prompt
			// decision cannot fire on unreadable evidence).
			state = "unknown"
			cause = "the queued marker exists but cannot be read as safe metadata"
			if WaitRank(4) > WaitRank(rc) {
				rc = 4
			}
		} else if !stale && waitMarkerExists(req, agent, "queued") && orig != "working" && orig != "blocked" && queuedUnresolved() {
			if queuedErr != nil {
				state = "unknown"
				cause = queuedErr.Error()
				if WaitRank(4) > WaitRank(rc) {
					rc = 4
				}
			} else {
				state = "not-received"
				if WaitRank(15) > WaitRank(rc) {
					rc = 15
				}
			}
		} else if !stale && orig == "blocked" {
			stop, _ := classifyTerminal(svc, agent, orig, field(fields, 2), ctx, env)
			if stop.question != "" {
				question = stop.question
				if WaitRank(7) > WaitRank(rc) {
					rc = 7
				}
			} else if stop.p != nil && ((stop.p.Status == "provider-error" && stop.p.Auth) || stop.p.Status == "provider-refusal" || stop.p.Cause == provider.LostConnectionCause) {
				if WaitRank(14) > WaitRank(rc) {
					rc = 14
				}
			}
			p = stop.p
		} else if !stale && orig != "working" && orig != "gone" && orig != "blocked" && orig != "unavailable" {
			stop, _ := classifyTerminal(svc, agent, orig, field(fields, 2), ctx, env)
			if stop.quota {
				quota = true
				match, renewal = stop.quotaMatch, stop.quotaRenewal
				kind, model, lane = field(fields, 2), field(fields, 8), field(fields, 11)
				if lane == "" {
					lane = core.LaneOfRole(ctx, taskRoleFor(req, agent, fields), env)
				}
				res.quota = true
				res.quotaLane, res.quotaKind, res.quotaModel = lane, kind, model
				res.quotaMatch, res.quotaRenewal = match, renewal
				rc = 11
			} else {
				p = stop.p
				if p != nil && WaitRank(14) > WaitRank(rc) {
					rc = 14
				}
			}
		}
		if !stale && !quota && p == nil && question == "" {
			res.restart = st
			res.restartValid = true
		}
	}
	var taskS, activityS any
	var pointerInfo fs.FileInfo
	if req.source != nil {
		pointerInfo, _ = req.source.ObserveFile("last-report-" + agent)
	} else {
		pointerInfo, _ = os.Stat(core.LastReportPath(sd, agent))
	}
	if pointerInfo != nil {
		end := platform.Now()
		if reportNonEmptyFor(req, agent, report) {
			if reportInfo, e := observedReportInfo(req, report); e == nil {
				end = reportInfo.ModTime()
			}
		}
		taskS = math.Round(end.Sub(pointerInfo.ModTime()).Seconds())
	}
	activityChanged := false
	if state == "working" {
		// The shared terminal classification's working branch holds the one
		// visible screen read the activity and retry marks also consume.
		stop, screen := classifyTerminal(svc, agent, "working", "", ctx, env)
		p = stop.p
		if p != nil && WaitRank(14) > WaitRank(rc) {
			rc = 14
		}
		age, changed := activityAge(req, agent, screen, platform.Now().Unix())
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
		idleMinutes = idleAlertMarkFor(req, agent, report)
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
		res.line = statusJsonLine(agent, "quota", report, append([]any{"lane", lane, "kind", kind, "model", model, "match", match, "renewal", renewal}, ageFields...)...)
		res.state = "quota"
	} else if p != nil {
		if p.Status == "provider-refusal" || p.Cause == provider.LostConnectionCause {
			lane = field(row.Fields, 11)
			if lane == "" {
				lane = core.LaneOfRole(ctx, field(row.Fields, 3), env)
			}
			res.line = statusJsonLine(agent, p.Status, report, append([]any{"cause", p.Cause, "lane", lane, "kind", field(row.Fields, 2), "model", field(row.Fields, 8)}, ageFields...)...)
		} else {
			res.line = statusJsonLine(agent, p.Status, report, append([]any{"cause", p.Cause}, ageFields...)...)
		}
		res.state = p.Status
	} else if question != "" {
		res.line = statusJsonLine(agent, "question", report, append([]any{"question", question}, ageFields...)...)
		res.state = "question"
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
			res.line = fmt.Sprintf("%s\t%s\t%s\t%s\t%s\t%s\n", agent, state, report, cause, taskCol, activityCol)
		} else {
			res.line = fmt.Sprintf("%s\t%s\t%s\t%s\t%s\n", agent, state, report, taskCol, activityCol)
		}
		res.state = state
	}
	return res, rc
}

// requiredMarkerPoisoned reports whether a required delivery marker
// (not-received or queued) exists but cannot be read as safe metadata
// through the inspection's confined read: the entry is present (not
// os.ErrNotExist) and the read refused (an oversize, linked, nonregular
// or unreadable marker). The command path's ordinary reader stands and
// its markers are never poisoned. A poisoned required marker is the
// conservative unknown, never the absence it would otherwise read as.
func requiredMarkerPoisoned(req agentStatusRequest, agent, name string) bool {
	if req.source == nil {
		return false
	}
	_, err := req.source.ReadMetadata("wait", agent+"."+name)
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return false
	}
	return true
}

func markerExists(sd, agent, name string) bool {
	_, err := os.Stat(filepath.Join(sd, "wait", agent+"."+name))
	return err == nil
}

// rosterRow is the safe snapshot row lookup: the shared selection over the
// acquired bounded roster on the inspection path, the ordinary RosterLine
// on the command path. The pane and every other column the resolver
// consumes come from this one lookup on both paths — the inspection never
// reopens the roster by ordinary path.
func rosterRow(req agentStatusRequest, agent string) (core.RosterRow, bool) {
	if req.source != nil {
		return req.source.Roster().Row(agent)
	}
	line := core.RosterLine(req.stateDir, agent)
	if line == "" {
		return core.RosterRow{}, false
	}
	return core.RosterRow{Fields: strings.Split(line, "\t")}, true
}

// reportPointer is the last-report pointer read: the ordinary path reader
// on the command path, the confined bounded source read on the inspection
// path. unsafe marks a present-but-unreadable or oversize pointer (the
// evidence is poisoned); an absent pointer is the existing normal absence
// (empty, safe).
func reportPointer(req agentStatusRequest, agent string) (string, bool) {
	if req.source == nil {
		return core.LastReport(req.stateDir, agent), false
	}
	raw, err := req.source.ReadMetadata("last-report-" + agent)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false
		}
		return "", true
	}
	return strings.TrimRight(raw, "\n"), false
}

// observeReportInspect resolves the pointer to the approved report
// observation roots for the inspection: the exact session's state reports
// dir and the known scratch reports dir, with the existing protected
// mirror semantics when the scratch report is gone. The pointer is
// recognized in every base alias, and only a regular nonlinked report
// reached through an approved root is accepted; an entry that is linked,
// nonregular or otherwise unreadable at an approved location, and a
// pointer that names any other path, is unsafe evidence (the inspection
// outcome is unknown, never a fabricated settled report). The returned
// path is the pointer's own form, the markers' identity quotes it.
func observeReportInspect(req agentStatusRequest, agent, report string) (string, bool) {
	if report == "" {
		return "", false
	}
	s := req.source
	name := filepath.Base(report)
	root, approved := s.ApprovedReportRoot(report)
	if !approved {
		return "", true
	}
	if root == "state" {
		info, err := s.ObserveStateReport(name)
		if err == nil && info.Mode().IsRegular() {
			return report, false
		}
		if errors.Is(err, os.ErrNotExist) {
			return "", false
		}
		return "", true
	}
	info, err := s.ObserveScratchReport(name)
	if err == nil && info.Mode().IsRegular() {
		return report, false
	}
	if errors.Is(err, os.ErrNotExist) {
		// Scratch is gone: the protected mirror, observed under the
		// source's retained session root (never a raw reopened state
		// path), else the pointer's own form as the normal absence.
		return s.StateReportMirror(report, name), false
	}
	return "", true
}

// reportNonEmptyFor reports a nonempty observed report: the ordinary stat
// on the command path, the confined metadata observation on the
// inspection path (size only, never the report bytes).
func reportNonEmptyFor(req agentStatusRequest, agent, report string) bool {
	if report == "" {
		return false
	}
	if req.source == nil {
		return reportNonEmpty(report)
	}
	info, err := observedReportInfo(req, report)
	if err != nil {
		return false
	}
	return info.Size() > 0
}

// waitMarker is the marker read the resolver uses: the ordinary wait file
// on the command path, the confined bounded source read on the
// inspection path — an unsafe, linked or oversize marker reads as absent,
// the way the ordinary marker readers treat a missing marker.
func waitMarker(req agentStatusRequest, agent, name string) (string, bool) {
	if req.source != nil {
		raw, err := req.source.ReadMetadata("wait", agent+"."+name)
		if err != nil {
			return "", false
		}
		return strings.TrimRight(raw, "\n"), true
	}
	return readWaitFile(req.stateDir, agent, name)
}

// waitMarkerValue is the single-value form of the resolver's marker read
// (an unsafe or absent marker is the empty string, as readWaitFile's).
func waitMarkerValue(req agentStatusRequest, agent, name string) string {
	raw, _ := waitMarker(req, agent, name)
	return raw
}

// waitMarkerExists is the resolver's marker presence: the ordinary stat on
// the command path, the confined capped read succeeding on the inspection
// path — an oversize, linked or nonregular marker reads as absent there,
// so presence and content never disagree (the not-received decision
// cannot fire on poisoned evidence).
func waitMarkerExists(req agentStatusRequest, agent, name string) bool {
	if req.source != nil {
		_, err := req.source.ReadMetadata("wait", agent+"."+name)
		return err == nil
	}
	return markerExists(req.stateDir, agent, name)
}

// taskRoleFor is the resolver's role source: the recorded role with its
// roster fallback on the command path, the snapshot row's role on the
// inspection path (the row is the shared safe lookup; the recorded-role
// sidecar walk is a taskreport read the inspection seam does not take —
// the role only feeds the quota lane text, which the view never exposes).
func taskRoleFor(req agentStatusRequest, agent string, fields []string) string {
	if req.source == nil {
		return TaskRole(req.stateDir, agent)
	}
	return field(fields, 3)
}

// activityAge is the resolver's activity read: the ordinary wait file
// reads on the command path, the confined source reads on the inspection
// path, the same shared computation (activityAgeFrom).
func activityAge(req agentStatusRequest, agent, screen string, now int64) (*int64, bool) {
	var hashRaw string
	var hashOK bool
	var at int64
	var atOK bool
	if req.source != nil {
		hashRaw, hashOK = waitMarker(req, agent, "stuck-hash")
		if raw, ok := waitMarker(req, agent, "activity-at"); ok {
			at, atOK = parseWaitInt(raw)
		}
	} else {
		hashRaw, hashOK = readWaitFile(req.stateDir, agent, "stuck-hash")
		at, atOK = positiveWaitInt(req.stateDir, agent, "activity-at")
	}
	return activityAgeFrom(hashRaw, hashOK, screen, at, atOK, now)
}

// parseWaitInt is the positive wait integer parse the marker reads share
// (a non-positive or non-numeric marker is invalid).
func parseWaitInt(raw string) (int64, bool) {
	raw = strings.TrimSpace(raw)
	if !uintPattern.MatchString(raw) {
		return 0, false
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	return n, err == nil && n > 0
}

// idleAlertMarkFor reads the wait's idle-alert marker (<task>\t<minutes>\t
// <unix>, DJA-565) and returns its minutes when the marker's task equals
// the worker's current task; a missing, unsafe or malformed marker is no
// mark. On the inspection path the marker is the confined read and the
// current task is the observed report path (the pointer's task-report
// variant is a taskreport read the inspection seam does not take — the
// mark only feeds the cause text, which the view never exposes). It is a
// local file read only: status adds no screen read or transport call for
// the mark.
func idleAlertMarkFor(req agentStatusRequest, agent, report string) int64 {
	var raw string
	var ok bool
	var task string
	if req.source != nil {
		raw, ok = waitMarker(req, agent, "idle-alert")
		task = report
	} else {
		raw, ok = readWaitFile(req.stateDir, agent, "idle-alert")
		task = idleTask(req.stateDir, agent, report)
	}
	if !ok || raw == "" {
		return 0
	}
	return idleAlertMinutesOf(raw, task)
}

// idleAlertMinutesOf is the shared idle-alert marker parse the command
// and inspection paths use: the marker's task must equal the current
// task, and both numbers must parse.
func idleAlertMinutesOf(raw, task string) int64 {
	fields := strings.Split(raw, "\t")
	if len(fields) != 3 || fields[0] != task {
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

// compactPhaseStatusFor routes the compact-marker read: the source's
// confined bounded read on the inspection path (the marker is the
// source's wait marker, never the raw state path), the path reader on
// the command path; the shared decision (compactPhaseDecide) stands.
func compactPhaseStatusFor(pane, sd, agent string, env platform.Env, inspect bool, source *core.SessionSource) (line string, code int, state string, unreadable bool, ok bool) {
	if source != nil {
		return compactPhaseStatusForSource(pane, source, agent, env)
	}
	return compactPhaseStatus(pane, sd, agent, env, inspect)
}

// compactPhaseStatusForSource is the inspection's compact-marker read
// through the source's confined bounded reader: the shared parse
// validation (CompactPendingParse) and the shared owner decision
// (CompactPendingOwner) stand; a present marker the reader refuses is
// the same compact-unknown the path reader reports for a nonregular or
// oversize marker, and an absent marker leaves the usual state logic.
func compactPhaseStatusForSource(pane string, source *core.SessionSource, agent string, env platform.Env) (line string, code int, state string, unreadable bool, ok bool) {
	path := core.CompactPendingPath(source.StateDir(), agent)
	raw, err := source.ReadMetadata("wait", agent+".compact-pending.json")
	var rec *core.CompactPending
	if err == nil {
		rec, err = core.CompactPendingParse(path, []byte(raw))
	}
	return compactPhaseDecide(pane, agent, path, env, rec, err)
}

// compactPhaseStatus reports the F11 compact-phase marker for a roster
// line whose pane matches the record's pane, without modifying the marker
// or writing anything: compacting (an alive owner, exit 0),
// compact-interrupted (a dead owner or a different start, exit 9) and
// compact-unknown (an unverifiable owner, exit 4); a corrupted or
// unreadable marker is a visible error (exit 4) that is never removed —
// its warning is recorded (unreadable) and applied by the caller. state
// is the rendered state word of the line. ok is false when there is no
// marker or the record's pane no longer matches (the usual state logic
// stands). inspect selects the bounded nonblocking inspection read
// (CompactPendingReadInspect: a FIFO or other nonregular marker is refused
// without blocking, and the marker is capped); every other caller keeps the
// command read (CompactPendingRead) and its behavior.
func compactPhaseStatus(pane, sd, agent string, env platform.Env, inspect bool) (line string, code int, state string, unreadable bool, ok bool) {
	path := core.CompactPendingPath(sd, agent)
	read := core.CompactPendingRead
	if inspect {
		read = core.CompactPendingReadInspect
	}
	rec, err := read(path)
	return compactPhaseDecide(pane, agent, path, env, rec, err)
}

// compactPhaseDecide is the shared compact-marker decision the path and
// source reads feed: absent is the usual state logic (ok=false), a
// corrupted or unreadable marker is compact-unknown (exit 4, recorded
// unreadable) and a pane mismatch leaves the usual state logic.
func compactPhaseDecide(pane, agent, path string, env platform.Env, rec *core.CompactPending, err error) (line string, code int, state string, unreadable bool, ok bool) {
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", 0, "", false, false
		}
		return fmt.Sprintf("%s\tcompact-unknown\t\tcompact-pending marker unreadable: %s; brief was not sent\t-\t-\n", agent, path), 4, "compact-unknown", true, true
	}
	if rec.Pane != pane {
		return "", 0, "", false, false
	}
	cause := fmt.Sprintf("brief was not sent: %s", rec.Brief)
	switch core.CompactPendingOwner(rec, env) {
	case core.CompactOwnerAlive:
		return fmt.Sprintf("%s\tcompacting\t\t%s\t-\t-\n", agent, cause), 0, "compacting", false, true
	case core.CompactOwnerDead:
		return fmt.Sprintf("%s\tcompact-interrupted\t\t%s\t-\t-\n", agent, cause), 9, "compact-interrupted", false, true
	default:
		return fmt.Sprintf("%s\tcompact-unknown\t\t%s\t-\t-\n", agent, cause), 4, "compact-unknown", false, true
	}
}

// compactPhaseStatusLine keeps the command-entry form of
// compactPhaseStatus: the unreadable-marker warning is applied on the
// caller side, and the line, code and ok are unchanged.
func compactPhaseStatusLine(rosterLine, sd, agent string, env platform.Env) (string, int, bool) {
	pane := field(strings.Split(rosterLine, "\t"), 1)
	line, code, _, unreadable, ok := compactPhaseStatus(pane, sd, agent, env, false)
	if ok && unreadable {
		core.Warn(fmt.Sprintf("status: the compact-pending marker %s of '%s' is unreadable; it was left in place", core.CompactPendingPath(sd, agent), agent), frictionLogFile, "status")
	}
	return line, code, ok
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
// pane (if any): its participants verified against the roster and Herdr,
// and the worker_messages mode read from the single policy engine
// (communication.Load, used only for the mode). env is a function so the
// HERDR_WORKSPACE_ID derivation (at most one herdr call) happens only when
// an active collaboration actually exists. knownWorkspace ("" for every
// current caller) switches the participant verification to the read-only
// explicit-workspace check: the assignment's workspace is compared to the
// known session instead of resolving the caller's. source (nil for every
// current caller) confines the collaboration store and the participant
// roster rows under its retained roots and snapshot. ok is false when no
// active collaboration hosts the pane: the usual state logic then stands,
// and an agent without a pane never reaches the collaboration store.
func readCollaboration(sd, pane string, ctx *core.Config, env func() platform.Env, knownWorkspace string, source *core.SessionSource) (collaborationState, bool) {
	if pane == "" {
		return collaborationState{}, false
	}
	var a *collaboration.Assignment
	var err error
	if source != nil {
		a, err = collaboration.ActiveForSource(source, pane)
	} else {
		store := collaboration.Store{StateDir: sd}
		a, err = store.ActiveFor(pane)
	}
	if err != nil {
		return collaborationState{state: "collaboration-unavailable", code: 4, cause: err.Error()}, true
	}
	if a == nil {
		return collaborationState{}, false
	}
	callerEnv := env()
	var checkErr error
	switch {
	case source != nil:
		checkErr = collaboration.CheckParticipantsForRoster(*a, snapshotRowFor(source), knownWorkspace, callerEnv)
	case knownWorkspace != "":
		checkErr = collaboration.CheckParticipantsForWorkspace(*a, sd, knownWorkspace, callerEnv)
	default:
		checkErr = collaboration.CheckParticipants(*a, sd, callerEnv)
	}
	if checkErr != nil {
		// The assignment is kept in the state (and in the view exported for
		// collect) when it was read: even a failing identity check or an off
		// policy names the collaboration the pane is hosting.
		return collaborationState{state: "collaboration-unavailable", code: 4, cause: checkErr.Error(), assign: a}, true
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

// collaborationStatusLine builds the collaboration line in the JSON form
// the decision asks for, with the stale report carried as an artifact
// only: the active line names the assignment, phase, round, revision and
// the last delivery; the failures carry the cause.
// snapshotRowFor adapts the inspection's acquired roster snapshot to the
// row lookup the collaboration participant checks take: the row the
// snapshot recorded for the name, or no row (the same registration
// refusal the absent state roster line yields). The inspection never
// reopens the state roster by ordinary path.
func snapshotRowFor(source *core.SessionSource) func(name string) []string {
	return func(name string) []string {
		row, ok := source.Roster().Row(name)
		if !ok {
			return nil
		}
		return row.Fields
	}
}

func collaborationStatusLine(agent string, cs collaborationState, report string) string {
	switch cs.state {
	case "collaborating":
		return statusJsonLine(agent, "collaborating", report, "assignment", cs.assign.ID, "phase", cs.assign.Phase, "round", cs.assign.Round, "revision", cs.assign.Revision, "delivery", cs.delivery)
	case "collaboration-unavailable":
		return statusJsonLine(agent, "collaboration-unavailable", report, "cause", cs.cause)
	default:
		return statusJsonLine(agent, "collaboration-disabled", report, "cause", cs.cause)
	}
}

// printCollaborationStatus prints the collaboration line through the
// shared builder (the status and wait paths print the same JSON).
func printCollaborationStatus(agent string, cs collaborationState, report string) {
	_, _ = platform.Stdout.Write([]byte(collaborationStatusLine(agent, cs, report)))
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
	cs, ok := readCollaboration(sd, pane, ctx, env, "", nil)
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

// explicitWorkspaceEnv is the explicit-session twin of derivedWorkspaceEnv
// for the read-only inspect: ALWAYS a clone with HERDR_WORKSPACE_ID
// replaced by the known session, even when the inherited value is nonempty
// and conflicting. It is a compatibility context for the existing Herdr
// readers, not a discovered identity: the caller's env is never mutated
// and no discovery call runs — the value comes from the request, not from
// core.WorkspaceID.
func explicitWorkspaceEnv(env platform.Env, session string) func() platform.Env {
	var derived platform.Env
	return func() platform.Env {
		if derived == nil {
			clone := env.Clone()
			clone["HERDR_WORKSPACE_ID"] = session
			derived = clone
		}
		return derived
	}
}

// idleAlertMark is the command form of the idle-alert read: the ordinary
// wait file and the pointer-aware current task, the shared parse.
func idleAlertMark(sd, agent, report string) int64 {
	raw, ok := readWaitFile(sd, agent, "idle-alert")
	if !ok || raw == "" {
		return 0
	}
	return idleAlertMinutesOf(raw, idleTask(sd, agent, report))
}
func queuedPromptIsUnresolved(req agentStatusRequest, agent, state string, ctx *core.Config, env platform.Env, svc transport.Services) (bool, error) {
	marker, _ := waitMarker(req, agent, "queued")
	visible, _ := readScreen(svc, agent, "visible", nil)
	recent, _ := readScreen(svc, agent, "recent-unwrapped", intPtr(40))
	path, err := queuedPromptPathFor(req, agent, marker)
	if err != nil {
		return false, err
	}
	if !queuedPromptMatches(path, recent) {
		return false, nil
	}
	if provider.QuotaDetect(state, visible) != nil {
		return false, nil
	}
	unresolvedRecent, _ := readScreen(svc, agent, "recent-unwrapped", intPtr(40))
	return provider.ProviderDetectTexts(state, unresolvedRecent, core.Cfg(ctx, "provider_capacity_texts", "", env), core.Cfg(ctx, "provider_error_texts", "", env)) == nil, nil
}

// queuedPromptPathFor confines the legacy fallback to the retained source.
// Candidate bodies are never opened; only plain regular metadata counts.
func queuedPromptPathFor(req agentStatusRequest, agent, marker string) (string, error) {
	if req.source == nil {
		return QueuedPromptPath(marker, req.stateDir, agent), nil
	}
	source := req.source
	return queuedPromptPath(marker, source.StateDir(), func() (string, error) {
		raw, err := source.ReadMetadata("last-report-" + agent)
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return raw, err
	}, func(path string, stateBrief bool) (bool, error) {
		var info fs.FileInfo
		var err error
		if stateBrief {
			info, err = source.ObserveFile("briefs", filepath.Base(path))
		} else {
			info, err = source.ObserveReport(path)
		}
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if !info.Mode().IsRegular() {
			return false, fmt.Errorf("the queued brief candidate %q is not a plain regular file", path)
		}
		return true, nil
	})
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

// terminalStop is the shared terminal/screen classification result the
// status resolver and the terminal-only orchestrator inspection consume:
// the question text of a blocked screen, the provider detection of a
// blocked or non-working terminal (or of a working terminal's lost
// connection) and the quota match and renewal. Zero values mean the
// terminal shows no classified stop.
type terminalStop struct {
	question     string
	p            *provider.Detection
	quota        bool
	quotaMatch   string
	quotaRenewal string
}

// classifyTerminal applies the status resolver's exact detection order to
// one probed terminal state: the blocked screen's question dialog or
// provider cause, the non-working terminal's quota or provider
// detection, and the working terminal's lost connection, reusing the
// existing provider detection functions; no other classifier exists. The
// visible screen is returned only for the working branch, where the
// worker resolver's activity and retry marks also consume that same read;
// gone and unavailable probes classify nothing (they stay conservative).
func classifyTerminal(svc transport.Services, target, orig, kind string, ctx *core.Config, env platform.Env) (terminalStop, string) {
	var stop terminalStop
	capacity := core.Cfg(ctx, "provider_capacity_texts", "", env)
	errorTexts := core.Cfg(ctx, "provider_error_texts", "", env)
	switch {
	case orig == "blocked":
		visible, _ := readScreen(svc, target, "visible", intPtr(40))
		if provider.DialogKind(kind, visible) == "question" {
			stop.question = provider.QuestionText(visible)
			return stop, ""
		}
		recent, _ := readScreen(svc, target, "recent-unwrapped", intPtr(40))
		stop.p = provider.ProviderDetectTexts(orig, recent, capacity, errorTexts)
	case orig != "working" && orig != "gone" && orig != "unavailable":
		visible, _ := readScreen(svc, target, "visible", intPtr(20))
		if q := provider.QuotaDetect(orig, visible); q != nil {
			stop.quota = true
			stop.quotaMatch, stop.quotaRenewal = q[0], q[1]
			return stop, ""
		}
		recent, _ := readScreen(svc, target, "recent-unwrapped", intPtr(40))
		stop.p = provider.ProviderDetectTexts(orig, recent, capacity, errorTexts)
	case orig == "working":
		visible, _ := readScreen(svc, target, "visible", nil)
		stop.p = provider.LostConnection(visible)
		return stop, visible
	}
	return stop, ""
}

// effectiveTerminalState is the shared effective state of a probed
// terminal after the classification: the quota, the provider detection
// and the question outrank the probe word, in exactly the order the
// status line build applies them.
func effectiveTerminalState(orig string, stop terminalStop) string {
	if stop.quota {
		return "quota"
	}
	if stop.p != nil {
		return stop.p.Status
	}
	if stop.question != "" {
		return "question"
	}
	return orig
}

// TerminalStatusRequest is the terminal-only read-only inspection input
// for a registered orchestrator: the live terminal to probe, its
// provider kind and the config, env and transport services the resolver
// shares. It takes no state dir, session, roster or report input: the
// orchestrator has no worker report, delivery or roster logic and no
// fabricated row.
type TerminalStatusRequest struct {
	Terminal string
	Kind     string
	Config   *core.Config
	Env      platform.Env
	Services transport.Services
}

// TerminalStatusView is the terminal-only inspection result: the
// effective terminal state word (done, idle, working, blocked, question,
// quota, provider-error, provider-refusal, unavailable, unknown or gone,
// exactly the words the status resolver renders) and the probe's state
// word. The orchestrator's report-ready is always false and is not a
// field here; screen contents and causes never leave as fields.
type TerminalStatusView struct {
	State         string
	TerminalState string
}

// InspectTerminalStatus probes one registered orchestrator's terminal and
// classifies it through the exact shared terminal/screen classification
// the worker resolver uses: done stays done, idle stays idle, working
// stays working, a classified stop keeps its existing effective word,
// and unavailable/unknown/gone stay conservative. It makes only the
// terminal's transport reads (the state get and the branch's screen
// reads): no roster, report, delivery, marker or file read, no print and
// no write of any kind.
func InspectTerminalStatus(req TerminalStatusRequest) TerminalStatusView {
	st := readState(req.Services, req.Terminal)
	stop, _ := classifyTerminal(req.Services, req.Terminal, st.State, req.Kind, req.Config, req.Env)
	return TerminalStatusView{State: effectiveTerminalState(st.State, stop), TerminalState: st.State}
}
