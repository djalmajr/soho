// Package spawn implements opening and reusing Herdr workers.
package spawn

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/dispatch"
	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/kinds"
	"github.com/djalmajr/soho/internal/layout"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/provider"
	"github.com/djalmajr/soho/internal/startup"
	"github.com/djalmajr/soho/internal/taskreport"
	"github.com/djalmajr/soho/internal/text"
)

var busyRetryDelay = time.Second

const (
	herdrStartTimeout = 30 * time.Second
	startPollWindow   = 5 * time.Second
	// mcpCodexListTimeoutMs caps the `codex mcp list --json` run the
	// per-server MCP-off translation needs (DJA-392 fatia 2 R2).
	mcpCodexListTimeoutMs = 10000
	// contextWindowSettle is the pause between typing /context-window and its
	// Enter, giving grok's menu time to appear before the selection.
	contextWindowSettle = time.Second
	// contextWindowWindow is how long spawn waits for grok's
	// "Context window set to" line on the visible screen.
	contextWindowWindow = 10 * time.Second
)

var agentNameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

func Warn(message string, ctx *core.Config, env platform.Env, command string) {
	core.Warn(message, filepath.Join(core.StateDirPath(ctx, env, mustGetwd()), "friction.log"), command)
}

func mustGetwd() string { cwd, _ := os.Getwd(); return cwd }

// mcpCodexOffArgs builds codex's per-server MCP-off args (DJA-392 fatia 2
// R2): it runs `codex mcp list --json` in the spawn's own PATH and env and
// returns, for every server the listing reports with enabled=true, the pair
// -c mcp_servers.<name>.enabled=false — in list order. Only name and
// enabled are read from the listing: the transport command lines may carry
// credentials and never leave this function. ok is false (with a short
// reason) when codex is not on the PATH, the listing times out, exits
// nonzero, or prints non-JSON output; the spawn then warns once and the
// agent keeps the user's MCP servers. Zero enabled servers are success with
// no args and no reason.
func mcpCodexOffArgs(env platform.Env) (args []string, ok bool, reason string) {
	res := platform.RunCli("codex", []string{"mcp", "list", "--json"}, platform.RunOptions{Env: env, TimeoutMs: mcpCodexListTimeoutMs})
	switch {
	case res.NotFound:
		return nil, false, "codex not found in PATH"
	case res.TimedOut:
		return nil, false, "timed out after 10 s"
	case res.Status == nil || *res.Status != 0:
		code := -1
		if res.Status != nil {
			code = *res.Status
		}
		return nil, false, fmt.Sprintf("exit %d", code)
	}
	var servers []struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(res.Stdout)), &servers); err != nil {
		return nil, false, "non-JSON output"
	}
	for _, server := range servers {
		if server.Enabled && server.Name != "" {
			args = append(args, "-c", "mcp_servers."+server.Name+".enabled=false")
		}
	}
	return args, true, ""
}

func approvalsRank(mode string) int {
	switch mode {
	case "ask":
		return 1
	case "edits":
		return 2
	case "full":
		return 3
	default:
		return 0
	}
}

func liveAgents(env platform.Env) []any { return herdr.LiveAgents(env, herdr.Timeout) }

func AgentNameTaken(name string, env platform.Env) bool {
	return AgentNameTakenIn(name, liveAgents(env))
}

// AgentNameTakenIn is AgentNameTaken over an already-read live list, so the
// read can be shared with the lane-orphan warning that runs after the name
// check without making a second agent list call.
func AgentNameTakenIn(name string, agents []any) bool {
	for _, a := range agents {
		if fieldString(a, "name") == name {
			return true
		}
	}
	return false
}

func UniqueName(base string, env platform.Env) string {
	name := base
	for i := 2; AgentNameTaken(name, env); i++ {
		name = fmt.Sprintf("%s-%d", base, i)
	}
	return name
}

func ResolvedRoleKind(role string, ctx *core.Config, env platform.Env, cwd string) string {
	return core.ResolvedRoleKind(role, ctx, env, cwd)
}

func ResolveSpawnEffort(role, lane, kind string, kindLayer int, ctx *core.Config, env platform.Env, cwd, model string) string {
	effort := ""
	if lane != "" {
		layer := kindLayer
		effort = core.LaneAttr(ctx, lane, "effort", &layer, env)
	}
	if effort == "" {
		effort = core.Cfg(ctx, "role_"+strings.ReplaceAll(role, "-", "_")+"_effort", "", env)
	}
	if effort == "" {
		effort = core.Cfg(ctx, "effort_"+kind, "", env)
	}
	if effort == "" {
		if file := core.RoleFile(role, env, cwd); file != "" {
			effort = core.FmGet(file, "effort")
		}
	}
	if text.HasWord(strings.Join(core.EffortLadder, " "), effort) {
		effort = kinds.ClampTo(kinds.ClampTo(effort, kinds.KindEffortCeiling(kind)), core.Cfg(ctx, "max_effort", "", env))
		if kind == "codex" {
			effort = kinds.ClampTo(effort, kinds.CodexEffortCeiling(model, env))
		}
	}
	return effort
}

func ResumeArg(kind string, args []string) string {
	var bad []string
	switch kind {
	case "claude":
		bad = []string{"-c", "--continue", "-r", "--resume"}
	case "cursor":
		bad = []string{"-c", "--cloud", "--resume"}
	}
	for _, arg := range args {
		for _, b := range bad {
			if arg == b || strings.HasPrefix(b, "--") && strings.HasPrefix(arg, b+"=") {
				return arg
			}
		}
	}
	return ""
}

// FindReusable selects an idle compatible worker from the roster, whatever
// MCP choice it was opened with.
func FindReusable(role, kind, workerCwd, name, wantModel, wantApprovals string, ctx *core.Config, env platform.Env, cwd string) (string, string, bool) {
	return FindReusableMcp(role, kind, workerCwd, name, wantModel, wantApprovals, "", ctx, env, cwd)
}

// mcpMatches reports whether a roster row's MCP choice (column 15,
// DJA-392) fits the request. A row written before that column carries no
// choice and matches any request, as reopen treats it; an empty request
// matches any row.
func mcpMatches(f []string, want string) bool {
	got := fieldAt(f, 15)
	return got == "" || want == "" || got == want
}

// FindReusableMcp is FindReusable that also requires the worker's MCP
// choice to match wantMcp (DJA-392): a session opened without the user's
// MCP servers cannot serve a role that needs them, nor the reverse.
func FindReusableMcp(role, kind, workerCwd, name, wantModel, wantApprovals, wantMcp string, ctx *core.Config, env platform.Env, cwd string) (string, string, bool) {
	multi := core.Cfg(ctx, "multi_role", "on", env)
	sd := core.StateDirPath(ctx, env, cwd)
	blockedName, blockedCause, crossHit := "", "", ""
	for _, line := range core.RosterRows(sd) {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		at := func(i int) string {
			if i < len(f) {
				return f[i]
			}
			return ""
		}
		nm := at(0)
		if nm == "" || name != "" && nm != name || at(2) != kind || at(6) != workerCwd {
			continue
		}
		isSame := at(3) == role
		wModel, wApprovals := "", ""
		if len(f) >= 9 {
			wModel = at(8)
		}
		if len(f) >= 10 {
			wApprovals = at(9)
		}
		if isSame {
			if len(f) >= 9 && wModel != wantModel {
				continue
			}
			if len(f) >= 10 && approvalsRank(wApprovals) < approvalsRank(defaultValue(wantApprovals, "ask")) {
				continue
			}
		} else {
			if multi != "on" || len(f) < 11 || wantModel == "" || wModel == "" || wModel != wantModel || wApprovals == "" || approvalsRank(defaultValue(wantApprovals, "ask")) == 0 || approvalsRank(wApprovals) < approvalsRank(defaultValue(wantApprovals, "ask")) {
				continue
			}
			if core.IsReviewRole(role) && (core.RoleIsEdit(at(3), env, cwd) || core.HistoryHasEdit(at(10), env, cwd)) {
				continue
			}
		}
		args := ""
		if len(f) >= 14 {
			args = at(13)
		}
		if args != ConfigNativeArgs(kind, "", role, ctx, env, cwd, nil) {
			continue
		}
		if !mcpMatches(f, wantMcp) {
			continue
		}
		report := core.LastReport(sd, nm)
		if report != "" {
			st, err := os.Stat(report)
			if err != nil || st.Size() == 0 {
				continue
			}
		}
		st := herdr.AgentState(nm, env, herdr.Timeout, nil)
		if st.State == "unavailable" {
			if isSame && blockedName == "" {
				blockedName, blockedCause = nm, st.Cause
			}
			continue
		}
		if st.State == "idle" || st.State == "done" {
			if isSame {
				return nm, "", false
			}
			if crossHit == "" {
				crossHit = nm
			}
		}
	}
	if blockedName != "" {
		return "", blockedName + "\x00" + blockedCause, true
	}
	return crossHit, "", false
}

// laneReuseCandidate reports whether the lane worker row (state from
// herdr agent get) is a reuse candidate for this spawn: idle or done, no
// pending report, not locked for a review role, and kind, resolved model
// (roster column 8) and cwd (roster column 6, canonical via
// dispatch.SamePath) all matching the request.
func laneReuseCandidate(f []string, state, role, kind, model, mcp, workerCwd, sd string, env platform.Env, cwd string) bool {
	if state != "idle" && state != "done" {
		return false
	}
	nm := fieldAt(f, 0)
	rep := core.LastReport(sd, nm)
	if rep != "" {
		info, err := os.Stat(rep)
		if err != nil || info.Size() == 0 {
			return false
		}
	}
	if core.IsReviewRole(role) && (core.RoleIsEdit(fieldAt(f, 3), env, cwd) || core.HistoryHasEdit(fieldAt(f, 10), env, cwd)) {
		return false
	}
	return fieldAt(f, 2) == kind && fieldAt(f, 8) == model && mcpMatches(f, mcp) && dispatch.SamePath(fieldAt(f, 6), workerCwd, platform.Current())
}

// laneReusePick scans the lane's live workers in roster order and returns the
// first idle worker that is a reuse candidate for the request, or "" when no
// idle worker matches (each mismatched idle then counts as occupied).
func laneReusePick(sd, lane, role, kind, model, mcp, workerCwd string, env platform.Env, cwd string) string {
	for _, row := range core.LaneWorkers(sd, lane) {
		f := strings.Split(row, "\t")
		nm := fieldAt(f, 0)
		if nm == "" {
			continue
		}
		st := herdr.AgentState(nm, env, herdr.Timeout, nil)
		if laneReuseCandidate(f, st.State, role, kind, model, mcp, workerCwd, sd, env, cwd) {
			return nm
		}
	}
	return ""
}

func EmitReuse(name, role, kind string, ctx *core.Config, env platform.Env, cwd string) bool {
	sd := core.StateDirPath(ctx, env, cwd)
	line := core.RosterLine(sd, name)
	if line == "" {
		return false
	}
	f := strings.Split(line, "\t")
	prev := fieldAt(f, 3)
	if prev != role {
		core.RosterSetRole(sd, name, role)
		line = core.RosterLine(sd, name)
		f = strings.Split(line, "\t")
	}
	// model, effort and agent_args are the roster columns the spawn recorded
	// (8, 14, 13); a missing value comes out as "".
	out := jsonjs.O("name", name, "pane_id", fieldAt(f, 1), "kind", kind, "role", role, "family", fieldAt(f, 4), "reused", true, "previous_role", prev, "effort", fieldAt(f, 14), "model", fieldAt(f, 8), "agent_args", fieldAt(f, 13), "status", "ready")
	_, _ = fmt.Fprintln(platform.Stdout, jsonjs.StringifyIndent(out, 2))
	return true
}

func fieldAt(values []string, i int) string {
	if i >= 0 && i < len(values) {
		return values[i]
	}
	return ""
}

// warnStartupBlocked prints the precise warning of the cursor/Windows
// startup gate: the reason the start is refused (the verified workspace
// trust dialog, or the unverified screen the assessment ended on) and that
// nothing was typed or sent — the pane stays open for the user to read
// and answer; the spawn never answers the dialog itself.
func warnStartupBlocked(name, kind, state string, ev startup.Evaluation, ctx *core.Config, env platform.Env) {
	what := "the startup is unverified"
	switch {
	case ev.Verdict == startup.VerdictBlocked:
		what = "it is showing the workspace trust dialog"
	case ev.Reason == startup.ReasonEmptyScreen:
		what = "the visible screen is empty (the TUI did not draw)"
	case ev.Reason == startup.ReasonLaunchOnly:
		what = "the visible screen shows only the shell prompt/launch line"
	case ev.Reason == startup.ReasonScreenUnavailable:
		what = "the visible screen could not be read"
	case ev.Reason == startup.ReasonStateUnavailable:
		what = "the agent state could not be read"
	}
	Warn(fmt.Sprintf("agent '%s' (%s) startup is not ready: %s (state %s); no input was sent — read the pane (herdr agent read %s --source visible) and answer it yourself (herdr agent send-keys %s <keys>; herdr agent wait %s --timeout 60000)", name, kind, what, state, name, name, name), ctx, env, "spawn")
}

// gateReuse assesses the visible screen of a reused cursor session on
// Windows before the spawn emits ready for it (issue #59). The reuse
// decision itself (identity, capacity, model, family, history) is
// untouched: this only refuses to call a blocked or unverified session
// ready, with the same evidence and result as a fresh start
// (blocked_at_startup, the precise warning, the screen printed, exit 7).
// Every other kind or platform skips it without any herdr read, and a
// name without a roster row is left to the fresh path (emitReuse says the
// same and returns false).
func gateReuse(name, role, kind string, ctx *core.Config, env platform.Env, cwd string) {
	if !startup.Enabled(kind) {
		return
	}
	sd := core.StateDirPath(ctx, env, cwd)
	line := core.RosterLine(sd, name)
	if line == "" {
		return
	}
	st := herdr.AgentState(name, env, herdr.Timeout, nil)
	screen, screenOK := herdr.AgentReadOK(env, name, "visible", intPtr(40))
	ev := startup.Evaluate(kind, startup.CurrentPlatform(), st.State, screen, screenOK)
	if !ev.Refuses() {
		return
	}
	f := strings.Split(line, "\t")
	out := jsonjs.O("name", name, "pane_id", fieldAt(f, 1), "kind", kind, "role", role, "family", fieldAt(f, 4), "reused", true, "previous_role", fieldAt(f, 3), "effort", fieldAt(f, 14), "model", fieldAt(f, 8), "agent_args", fieldAt(f, 13), "status", "blocked_at_startup")
	out.Set("startup_evidence", jsonjs.O(startup.FieldState, st.State, startup.FieldReason, ev.Reason, startup.FieldSource, ev.Source))
	_, _ = fmt.Fprintln(platform.Stdout, jsonjs.StringifyIndent(out, 2))
	warnStartupBlocked(name, kind, st.State, ev, ctx, env)
	_, _ = fmt.Fprint(platform.Stdout, herdr.AgentRead(env, name, "visible", intPtr(40)))
	platform.Die("", 7)
}

// renameLaneWorker renames the reused worker's agent to newName with the same
// herdr agent rename call EnsureOrchestratorName uses; it returns the cause
// extracted from the result when the rename failed, empty on success.
func renameLaneWorker(newName, pane string, env platform.Env) string {
	r := platform.RunCli("herdr", []string{"agent", "rename", pane, newName}, platform.RunOptions{Env: env, TimeoutMs: int(herdr.Timeout.Milliseconds())})
	if !r.NotFound && r.Status != nil && *r.Status == 0 {
		return ""
	}
	raw := r.Stderr
	if raw == "" {
		raw = r.Stdout
	}
	if code, msg := renameErrorInfo(raw); code != "" {
		if cause := text.SanitizeCause(code + ": " + msg); cause != "" {
			return cause
		}
	}
	rc := 1
	if r.NotFound {
		rc = 127
	} else if r.Status != nil {
		rc = *r.Status
	}
	if cause := text.SanitizeCause(raw); cause != "" {
		return cause
	}
	return fmt.Sprintf("herdr agent rename failed (exit %d)", rc)
}

func renameErrorInfo(raw string) (string, string) {
	v, err := jsonjs.Parse([]byte(raw))
	if err != nil {
		return "", ""
	}
	o, ok := v.(*jsonjs.Object)
	if !ok {
		return "", ""
	}
	e, ok := o.Get("error")
	if !ok {
		return "", ""
	}
	eo, ok := e.(*jsonjs.Object)
	if !ok {
		return "", ""
	}
	code, _ := eo.Get("code")
	msg, _ := eo.Get("message")
	cs, _ := code.(string)
	ms, _ := msg.(string)
	if cs == "" {
		return "", ""
	}
	return cs, ms
}

// moveNameStateFiles moves the per-agent state files from oldName to newName
// after a successful lane rename, so the worker's earlier session is read back
// under the new name: the last-report pointer (core.LastReportPath), the task
// title file ("task-" plus the name, as internal/core reads and writes it),
// the task report pointer (taskreport.TaskReportPointerPath) and every wait
// marker the wait package stores as wait/<agent>.<marker>. Missing files are
// not an error; a failing rename becomes a warning and the spawn continues.
func moveNameStateFiles(sd, oldName, newName string, ctx *core.Config, env platform.Env) {
	move := func(display, from, to string) {
		if _, err := os.Stat(from); err != nil {
			return
		}
		if err := os.Rename(from, to); err != nil {
			Warn(fmt.Sprintf("spawn: could not move '%s' to the name '%s': %s", display, newName, text.SanitizeCause(err.Error())), ctx, env, "spawn")
		}
	}
	move("last-report-"+oldName, core.LastReportPath(sd, oldName), core.LastReportPath(sd, newName))
	move("task-"+oldName, filepath.Join(sd, "task-"+oldName), filepath.Join(sd, "task-"+newName))
	oldPointer := taskreport.TaskReportPointerPath(sd, oldName)
	move(filepath.Base(oldPointer), oldPointer, taskreport.TaskReportPointerPath(sd, newName))
	if entries, err := os.ReadDir(filepath.Join(sd, "wait")); err == nil {
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasPrefix(entry.Name(), oldName+".") {
				continue
			}
			rest := strings.TrimPrefix(entry.Name(), oldName+".")
			move("wait/"+entry.Name(), filepath.Join(sd, "wait", entry.Name()), filepath.Join(sd, "wait", newName+"."+rest))
		}
	}
}
func fieldString(value any, key string) string {
	if o, ok := value.(*jsonjs.Object); ok {
		v, _ := o.Get(key)
		s, _ := v.(string)
		return s
	}
	return ""
}
func defaultValue(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

type spawnOptions struct {
	role, name, kind, direction, ratio, cwd, pane, timeout, effort, model, approvals, reuse, tabLabel, mcp string
	kindSet                                                                                                bool
	force                                                                                                  bool
	native                                                                                                 []string
}

func parseArgs(argv []string, cwd string) spawnOptions {
	o := spawnOptions{role: fieldAt(argv, 0), cwd: cwd}
	if o.role == "" {
		core.DieFriction("spawn: missing role", 2, "", "")
	}
	for i := 1; i < len(argv); i++ {
		a := argv[i]
		if a == "--" {
			o.native = append(o.native, argv[i+1:]...)
			break
		}
		switch a {
		case "--reuse":
			o.reuse = "on"
			continue
		case "--fresh":
			o.reuse = "off"
			continue
		case "--force":
			// DJA-392: skips only the pressure refusal below (the warning
			// line still goes out); never skips any other refusal.
			o.force = true
			continue
		case "--tab-label", "--effort", "--model", "--approvals", "--name", "--kind", "--direction", "--ratio", "--cwd", "--pane", "--timeout", "--mcp":
		default:
			core.DieFriction("spawn: unknown option "+a, 2, "", "")
		}
		if i+1 >= len(argv) {
			core.DieFriction("spawn: "+a+" expects a value", 2, "", "")
		}
		v := argv[i+1]
		i++
		switch a {
		case "--tab-label":
			o.tabLabel = v
		case "--mcp":
			if v != "inherit" && v != "none" {
				core.DieFriction("spawn: invalid --mcp '"+v+"' (inherit|none)", 2, "", "")
			}
			o.mcp = v
		case "--effort":
			o.effort = v
		case "--model":
			o.model = v
		case "--approvals":
			o.approvals = v
		case "--name":
			o.name = v
		case "--kind":
			if v == "" {
				core.DieFriction("spawn: --kind expects a kind", 2, "", "")
			}
			o.kind, o.kindSet = v, true
		case "--direction":
			o.direction = v
		case "--ratio":
			o.ratio = v
		case "--cwd":
			o.cwd = filepath.Clean(filepath.Join(cwd, v))
			if filepath.IsAbs(v) {
				o.cwd = filepath.Clean(v)
			}
		case "--pane":
			o.pane = v
		case "--timeout":
			o.timeout = v
		}
	}
	return o
}

func CmdSpawn(argv []string, ctx *core.Config, env platform.Env, cwd string) {
	// Machine under pressure: warn before opening more work (stderr only;
	// the spawn proceeds as usual).
	core.PressureWarn(ctx, env, cwd)
	o := parseArgs(argv, cwd)
	if o.name != "" && !agentNameRE.MatchString(o.name) {
		core.DieFriction(fmt.Sprintf("invalid agent name '%s' (must match [a-z][a-z0-9_-]{0,31})", o.name), 2, "", "")
	}
	info, err := os.Stat(o.cwd)
	if err != nil || !info.IsDir() {
		core.DieFriction("spawn: --cwd "+o.cwd+" is not a directory", 2, "", "")
	}
	EnsureOrchestratorName(ctx, env, cwd, "spawn")
	core.ResolveRole(o.role, env, cwd)
	if o.role == "planner" {
		core.DieFriction("spawn planner: the orchestrator is the planner and does not open a pane. Plan in this session.", 12, "", "")
	}
	res := core.ResolveRoleSettings(o.role, ctx, env, cwd, core.RoleFlags{Kind: o.kind, Model: o.model, Effort: o.effort, Approvals: o.approvals})
	lane, kind := res.Lane, res.Kind
	if lane == "" && core.LanesEnabled(ctx, env) {
		if core.PanesValue(ctx, env) == "2" && core.IsReviewRole(o.role) && !core.CustomLanesPresent(ctx, env) {
			core.DieFriction(fmt.Sprintf("spawn: with panes=2 the orchestrator reviews (pick its model family by hand); role '%s' has no lane. Use panes=3 or 4, or pane_mode=flex for a temporary reviewer.", o.role), 3, "", "")
		}
		core.DieFriction(fmt.Sprintf("spawn: role '%s' is not in any lane (panes=%s). Add it with lane.<name>.roles, or set lanes=off.", o.role, core.PanesValue(ctx, env)), 3, "", "")
	}
	if kind == "" {
		core.DieFriction(fmt.Sprintf("role %s has no default kind; pass --kind", o.role), 3, "", "")
	}
	kindLayer := core.SpawnKindLayer(ctx, o.role, lane, o.kindSet, env)
	if _, ok := platform.FindExecutable(kinds.KindExe(kind), env, platform.Current()); !ok {
		Warn("executable '"+kinds.KindExe(kind)+"' not found in PATH; herdr agent start may fail", ctx, env, "spawn")
	}
	if o.role == "sub-orchestrator" && kind == "codex" && !strings.Contains(core.Cfg(ctx, "args_codex", "", env), "danger-full-access") {
		Warn("sub-orchestrator on codex: its sandbox blocks the Herdr socket (every 'herdr' call fails with Operation not permitted). Use --kind claude, or set args.codex=-s danger-full-access if you accept that.", ctx, env, "spawn")
	}
	o.effort, o.model, o.approvals = res.Effort, res.ModelSpec, res.Approvals
	modelSpec := o.model
	if o.approvals != "ask" && o.approvals != "edits" && o.approvals != "full" {
		core.DieFriction(fmt.Sprintf("invalid approvals '%s' (ask|edits|full)", o.approvals), 2, "", "")
	}
	// DJA-392: the per-role MCP server choice — flag > lane.<n>.mcp >
	// role.<r>.mcp > the role file's mcp: > inherit.
	o.mcp = ResolveMcp(o.mcp, lane, o.role, ctx, env, cwd)
	if o.timeout == "" {
		o.timeout = core.Cfg(ctx, "spawn_timeout", "60000", env)
	} else if ms, ok := core.ParseTimeoutMS(o.timeout); !ok {
		core.DieFriction(fmt.Sprintf("spawn: --timeout expects milliseconds or a duration such as 90s, 30m or 2h, got '%s'", o.timeout), 2, "", "spawn")
	} else {
		// The value goes to `herdr agent start --timeout`, also in
		// milliseconds: a unit is converted here, so herdr never sees one.
		// A unit names the intent, so only a unitless integer warns.
		if core.IsPlainTimeoutMS(o.timeout) {
			core.WarnShortTimeout("spawn", filepath.Join(core.StateDirPath(ctx, env, cwd), "friction.log"), ms)
		}
		o.timeout = strconv.FormatInt(ms, 10)
	}
	if o.effort != "" {
		if !text.HasWord(strings.Join(core.EffortLadder, " "), o.effort) {
			core.DieFriction(fmt.Sprintf("invalid effort '%s' (low|medium|high|xhigh|max)", o.effort), 2, "", "")
		}
		clamped := clampSpawnEffort(o.effort, kind, core.Cfg(ctx, "max_effort", "max", env))
		if clamped != o.effort {
			Warn(fmt.Sprintf("effort '%s' clamped to '%s' (kind ceiling %s, max_effort %s)", o.effort, clamped, kinds.KindEffortCeiling(kind), core.Cfg(ctx, "max_effort", "max", env)), ctx, env, "spawn")
			o.effort = clamped
		}
	}
	if modelSpec != "" {
		o.model, _ = kinds.ResolveModel(kind, modelSpec, o.effort, env, func(msg string) { Warn(msg, ctx, env, "spawn") })
	}
	if kind == "codex" && o.effort != "" {
		mc := kinds.CodexEffortCeiling(o.model, env)
		if kinds.EffortRank(o.effort) > kinds.EffortRank(mc) {
			if kinds.CodexModelCeiling(o.model, env) != "" {
				Warn(fmt.Sprintf("codex model %s supports up to '%s'; effort '%s' clamped", o.model, mc, o.effort), ctx, env, "spawn")
			} else {
				label := o.model
				if label == "" {
					label = "(CLI default)"
				}
				Warn(fmt.Sprintf("codex model %s is not in ~/.codex/models_cache.json; effort '%s' clamped to '%s'", label, o.effort, mc), ctx, env, "spawn")
			}
			o.effort = mc
		}
	}
	if o.reuse == "" {
		o.reuse = core.Cfg(ctx, "reuse_workers", "on", env)
	}
	sd := core.StateDir(ctx, env, cwd)
	// DJA-392: the pressure refusal is measured once, here, so the lane
	// cleanup below and the refusal further down see the same answer.
	var refuseParts []string
	if o.pane == "" && !o.force {
		refuseParts = core.PressureRefuseParts(ctx, env, cwd)
	}
	burst := false
	if lane != "" && o.pane == "" {
		d := core.LaneDecide(ctx, lane, o.role, env, cwd, o.reuse == "on")
		for _, gone := range d.Gone {
			core.RosterRemove(sd, gone)
			if len(refuseParts) > 0 {
				// No pane opens under the refusal: no friction line saying so.
				continue
			}
			Warn(fmt.Sprintf("lane '%s' worker '%s' is gone; opening a new pane", lane, gone), ctx, env, "spawn")
		}
		burst = core.PaneMode(ctx, env) == "flex" && has(core.SplitRoles(core.Cfg(ctx, "flex_roles", "reviewer,documenter", env)), o.role) && (d.Decision == "busy" || d.Capacity == 0 && d.Decision == "absent") && len(core.LiveBurstWorkers(sd, env)) < core.FlexExtra(ctx, env)
		switch d.Decision {
		case "reuse":
			// A12: --name never swaps the names of live lane workers. A live
			// worker already named o.name is reused as-is when it is a
			// candidate; when it exists on the lane and is not, the busy-lane
			// rule applies. Only when no live agent is named o.name does a
			// different candidate get renamed to it (the rename flow below).
			selected := ""
			if o.name != "" {
				named := false
				for _, row := range core.LaneWorkers(sd, lane) {
					rf := strings.Split(row, "\t")
					if fieldAt(rf, 0) != o.name {
						continue
					}
					named = true
					st := herdr.AgentState(o.name, env, herdr.Timeout, nil)
					if laneReuseCandidate(rf, st.State, o.role, kind, o.model, o.mcp, o.cwd, sd, env, cwd) {
						selected = o.name
					}
					break
				}
				if named && selected == "" {
					busyLane(lane, o.name, "", o.role, ctx, env, cwd)
				}
			}
			if selected == "" {
				line := core.RosterLine(sd, d.Name)
				f := strings.Split(line, "\t")
				actual := fieldAt(f, 2)
				if core.LaneAttr(ctx, lane, "kind", nil, env) == "" {
					sessionModel := fieldAt(f, 8)
					sessionEffort := ResolveSpawnEffort(fieldAt(f, 3), lane, actual, kindLayer, ctx, env, cwd, sessionModel)
					if actual != kind || sessionEffort != o.effort {
						mismatchLane(d.Name, lane, actual, kind, sessionModel, o.model, sessionEffort, o.effort, ctx, env, cwd)
					}
				} else if actual != kind {
					mismatchSimple(d.Name, lane, actual, kind, ctx, env, cwd)
				}
				// A12: the resolved model (roster column 8) and the cwd
				// (roster column 6, both canonical via dispatch.SamePath) must
				// match the request too; a mismatched idle is not reused and
				// the lane decision proceeds as if it were busy — a new pane
				// when the lane has a slot, exit 13 when it does not.
				// DJA-392: the MCP choice the worker was opened with (roster
				// column 15) must match too, under the same rule.
				modelCwdOk := fieldAt(f, 8) == o.model && dispatch.SamePath(fieldAt(f, 6), o.cwd, platform.Current())
				if !modelCwdOk || !mcpMatches(f, o.mcp) {
					selected = laneReusePick(sd, lane, o.role, kind, o.model, o.mcp, o.cwd, env, cwd)
					if selected == "" {
						if d.N < d.Capacity {
							break // the lane has a slot: open a new pane
						}
						if !modelCwdOk {
							core.DieFriction(fmt.Sprintf("spawn: lane '%s' has no idle worker matching kind '%s', model '%s' and cwd '%s' (idle: '%s' runs %s %s in %s); release it or raise the lane's panes", lane, kind, o.model, o.cwd, d.Name, actual, fieldAt(f, 8), fieldAt(f, 6)), 13, "", "")
						}
						core.DieFriction(fmt.Sprintf("spawn: lane '%s' has no idle worker matching MCP '%s' (idle: '%s' was opened with MCP '%s'); release it or raise the lane's panes", lane, o.mcp, d.Name, fieldAt(f, 15)), 13, "", "")
					}
				} else {
					selected = d.Name
				}
			}
			line := core.RosterLine(sd, selected)
			f := strings.Split(line, "\t")
			actual := fieldAt(f, 2)
			args := ""
			if len(f) >= 14 {
				args = f[13]
			}
			wanted := ConfigNativeArgs(kind, lane, o.role, ctx, env, cwd, nil)
			if args != wanted {
				obj := jsonjs.O("status", "kind-mismatch", "lane", lane, "name", selected, "session_args", args, "requested_args", wanted)
				_, _ = fmt.Fprintln(platform.Stdout, jsonjs.Stringify(obj))
				Warn(fmt.Sprintf("lane '%s' worker '%s' was started with other native args ('%s'); this spawn wants '%s'. Release the lane, then spawn again.", lane, selected, args, wanted), ctx, env, "spawn")
				platform.Die("", 13)
			}
			name := selected
			renamedTo := ""
			if o.name != "" && o.name != selected {
				if cause := renameLaneWorker(o.name, fieldAt(f, 1), env); cause != "" {
					core.DieFriction(fmt.Sprintf("spawn: could not rename worker '%s' to '%s': %s", selected, o.name, cause), 4, "", "")
				}
				core.RosterRename(sd, selected, o.name)
				moveNameStateFiles(sd, selected, o.name, ctx, env)
				name = o.name
				renamedTo = o.name
			}
			gateReuse(name, o.role, actual, ctx, env, cwd)
			EmitReuse(name, o.role, actual, ctx, env, cwd)
			sameTreeEditors(name, o.role, fieldAt(f, 6), sd, env, cwd, ctx)
			warnMsg := fmt.Sprintf("reusing idle lane '%s' worker '%s' as %s; existing session context is retained", lane, selected, o.role)
			if renamedTo != "" {
				warnMsg = fmt.Sprintf("reusing idle lane '%s' worker '%s' as %s, renamed to '%s'; existing session context is retained", lane, selected, o.role, renamedTo)
			}
			Warn(warnMsg, ctx, env, "spawn")
			return
		case "busy":
			if burst {
				break
			}
			if d.Candidate != "" && o.reuse != "on" {
				busyLane(lane, d.Candidate, fmt.Sprintf("lane '%s' already has idle worker '%s'. Release it before --fresh, or dispatch on it. Run 'wait %s', then dispatch.", lane, d.Candidate, d.Candidate), o.role, ctx, env, cwd)
			}
			if d.Capacity == 0 {
				busyLane(lane, d.Name, "", o.role, ctx, env, cwd)
			}
			busyLane(lane, d.Name, fmt.Sprintf("lane '%s' is full (%d of %d: %s). Run 'wait %s', then dispatch.", lane, d.N, d.Capacity, strings.Join(d.Occupants, " "), d.Name), o.role, ctx, env, cwd)
		case "open":
		case "absent":
			if d.Capacity == 0 && !burst {
				busyLane(lane, d.Name, "", o.role, ctx, env, cwd)
			}
		case "unavailable":
			core.DieFriction(fmt.Sprintf("lane '%s' worker '%s' matches but herdr agent get failed (%s). Not spawning a replacement; it may still be live.", lane, d.Name, d.Cause), 4, "", "")
		case "locked":
			core.DieFriction(fmt.Sprintf("lane '%s' worker '%s' has edited and cannot take review role '%s'.", lane, d.Name, o.role), 5, "", "")
		default:
			core.DieFriction(fmt.Sprintf("spawn: unexpected lane decision '%s'", d.Decision), 4, "", "")
		}
	}
	if lane == "" && o.reuse == "on" && o.pane == "" {
		reused, unavailable, _ := FindReusableMcp(o.role, kind, o.cwd, o.name, o.model, o.approvals, o.mcp, ctx, env, cwd)
		if reused != "" {
			previous := fieldAt(strings.Split(core.RosterLine(sd, reused), "\t"), 3)
			gateReuse(reused, o.role, kind, ctx, env, cwd)
			EmitReuse(reused, o.role, kind, ctx, env, cwd)
			sameTreeEditors(reused, o.role, o.cwd, sd, env, cwd, ctx)
			if previous == o.role {
				Warn(fmt.Sprintf("reusing idle worker '%s' (%s, %s); existing session context is retained", reused, kind, o.role), ctx, env, "spawn")
			} else {
				Warn(fmt.Sprintf("reusing idle worker '%s' (%s, was %s, now %s); existing session context is retained", reused, kind, previous, o.role), ctx, env, "spawn")
			}
			return
		}
		if unavailable != "" {
			parts := strings.SplitN(unavailable, "\x00", 2)
			core.DieFriction(fmt.Sprintf("worker '%s' matches this role, kind and cwd but herdr agent get failed (%s). Not spawning a replacement; it may still be live.", parts[0], fieldAt(parts, 1)), 4, "", "")
		}
	}
	// DJA-392: the harder pressure limits refuse to open a new pane (exit
	// 25). Both reuse paths returned above, so anything that reaches here
	// creates a new pane (o.pane == "") and no herdr call that creates a
	// pane or agent has run yet; a given --pane creates no pane and is
	// never refused. --force skips only this refusal (the pressure warning
	// line at the top still went out); a measurement failure never
	// refuses, and nothing is written (no friction line, no roster line).
	if o.pane == "" && len(refuseParts) > 0 {
		platform.Die(fmt.Sprintf("spawn: refusing to open a new pane under resource pressure: %s; close idle panes or run 'soho gc', or pass --force", strings.Join(refuseParts, "; ")), 25)
	}
	core.EnforceWorkerCap(ctx, env, cwd)
	var orphanLive []any
	if o.name != "" {
		if AgentNameTaken(o.name, env) {
			taken := o.name
			o.name = UniqueName(o.name, env)
			if !agentNameRE.MatchString(o.name) {
				core.DieFriction(fmt.Sprintf("invalid agent name '%s' (must match [a-z][a-z0-9_-]{0,31})", o.name), 2, "", "")
			}
			Warn(fmt.Sprintf("agent name '%s' is taken by another pane or workspace; using '%s'", taken, o.name), ctx, env, "spawn")
		}
	} else {
		base := lane
		if base == "" {
			base = o.role
		}
		o.name = UniqueName(base, env)
		if !agentNameRE.MatchString(o.name) {
			core.DieFriction(fmt.Sprintf("invalid agent name '%s' (must match [a-z][a-z0-9_-]{0,31})", o.name), 2, "", "")
		}
		orphanLive = liveAgents(env)
		if AgentNameTakenIn(o.name, orphanLive) {
			core.DieFriction(fmt.Sprintf("agent name '%s' is already live", o.name), 3, "", "")
		}
		if o.name != base {
			_, _ = fmt.Fprintf(platform.Stderr, "soho: spawn: the new worker is '%s' ('%s' is already live); dispatch to '%s'\n", o.name, base, o.name)
		}
	}
	args := buildArgs(kind, o.approvals, o.model, o.effort, ctx, env)
	// DJA-392: mcp=none translates to the kind's per-process flag, right
	// after the effort args (buildArgs ends with them) and before the
	// config and user args; a kind without a per-process flag warns once
	// and the agent keeps the user's MCP servers. Codex builds its off args
	// per server from its live listing (R2): -c mcp_servers=<name>.enabled
	// =false for every enabled server, in list order; a failed listing warns
	// once and the agent keeps the user's MCP servers.
	if o.mcp == "none" && kind == "codex" {
		mcpArgs, ok, reason := mcpCodexOffArgs(env)
		if !ok {
			Warn(fmt.Sprintf("spawn: codex could not list its MCP servers (%s); '%s' keeps the user's MCP servers", reason, o.name), ctx, env, "spawn")
		} else {
			args = append(args, mcpArgs...)
		}
	} else if mcpArgs, ok := kinds.KindMcpArgs(kind, o.mcp); !ok {
		Warn(fmt.Sprintf("spawn: %s cannot turn MCP servers off per process; '%s' keeps the user's MCP servers", kind, o.name), ctx, env, "spawn")
	} else {
		args = append(args, mcpArgs...)
	}
	native := ConfigNativeArgs(kind, lane, o.role, ctx, env, cwd, func(key, own string) {
		if own == "" {
			own = "?"
		}
		Warn(fmt.Sprintf("%s not passed: those args belong to kind %s and this spawn runs %s", key, own, kind), ctx, env, "spawn")
	})
	if native != "" {
		args = append(args, strings.Fields(native)...)
	}
	agentArgs := append(args, o.native...)
	if resume := ResumeArg(kind, agentArgs); resume != "" {
		core.DieFriction(fmt.Sprintf("spawn: '%s' would make %s resume an earlier session instead of starting a new one (for claude, -c is --continue: it resumes the orchestrator's conversation in this cwd). Remove it from the native args; codex-only flags belong in args.codex", resume, kind), 2, "", "")
	}
	created, placement, autoRegrid := false, "given", false
	layoutMode := core.Cfg(ctx, "layout", "split", env)
	focusBefore := layout.UIFocusedPane(env)
	if o.tabLabel != "" && o.pane != "" {
		Warn("--tab-label ignored: --pane places the worker in a given pane", ctx, env, "spawn")
	}
	if o.pane == "" {
		if lane != "" {
			// The M1 idle-other-kind scan and the released-orphan scan share
			// the live read the name check just made; a named spawn (o.name
			// set) fetches it here once instead of per warning.
			if orphanLive == nil {
				orphanLive = liveAgents(env)
			}
			warnLaneIdleOtherKind(sd, lane, kind, env, orphanLive)
			// An idle pane released by this project without --close is not
			// re-adopted: the spawn only names it (from the released-panes
			// registry) before opening another pane. It reuses the live read
			// the name check just made, and spends a workspace read only when
			// a live agent matches a registry row.
			warnLaneOrphans(ctx, env, cwd, orphanLive)
		}
		anchor, autoDir := "overflow", "layout"
		if layoutMode != "tab" && o.tabLabel == "" {
			picked := layout.PickSplitAnchor(ctx, env, cwd)
			if picked != nil {
				if picked.Overflow != "" {
					anchor, autoDir = "overflow", picked.Overflow
				} else {
					anchor, autoDir = picked.Anchor, picked.Direction
				}
			}
		}
		if anchor == "overflow" && o.direction != "" && o.tabLabel == "" {
			anchor = env.Get("HERDR_PANE_ID")
		}
		if anchor == "overflow" || anchor == "" {
			if layoutMode != "tab" && o.tabLabel == "" {
				Warn(fmt.Sprintf("caller tab has no room for another pane (%s); placing '%s' in a herd tab", autoDir, o.name), ctx, env, "spawn")
			}
			t := layout.HerdTabPane(ctx, o.cwd, o.tabLabel, o.role, env, cwd)
			o.pane = t.Pane
			created = true
			o.direction = ""
			placement = "herd"
			autoRegrid = true
		} else {
			if o.direction == "" && o.ratio == "" {
				autoRegrid = true
			}
			if o.direction == "" {
				o.direction = autoDir
			}
			ratio := o.ratio
			if ratio == "" {
				ratio = "0.5"
			}
			s := herdr.PaneSplit(anchor, o.direction, o.cwd, env, &ratio)
			if !s.Ok {
				core.DieFriction("pane split failed", 4, "", "")
			}
			o.pane = s.Pane
			created = true
			placement = "split"
		}
	}
	blocked := startAgent(o, kind, env, created, agentArgs)
	layout.RestoreFocusIfStolen(focusBefore, o.pane, o.direction, env)
	family := kinds.AgentFamily(kind, o.model)
	lastState, lastScreen := "", ""
	if !blocked {
		lastState, lastScreen, blocked = checkStartWindow(o, kind, env, created, agentArgs, ctx)
	}
	if !blocked && lastState != "unavailable" {
		if p := provider.ProviderDetect(lastState, lastScreen); p != nil && p.Status == "provider-error" && p.Auth {
			core.DieFriction(fmt.Sprintf("agent '%s' (%s) hit a terminal provider authentication error right after start: %s; pane %s stays open for inspection", o.name, kind, p.Cause, o.pane), 14, "", "")
		}
	}
	for _, stale := range staleRosterLines(sd, o.name, o.pane) {
		core.RosterRemove(sd, stale[0])
		Warn(fmt.Sprintf("replaced the stale roster line of '%s' (pane %s)", stale[0], stale[1]), ctx, env, "spawn")
	}
	// DJA-392 fatia 2 R2: the effective MCP choice (inherit|none) is the
	// row's last field; reopen reads it and passes --mcp <value> to the
	// respawn, so a worker keeps the MCP it was opened with.
	core.RosterAppend(sd, []string{o.name, o.pane, kind, o.role, family, boolInt(created), o.cwd, core.NowStamp(platform.Now()), o.model, defaultValue(o.approvals, "ask"), o.role, lane, boolText(burst), native, o.effort, o.mcp})
	// A spawn that re-rosters a released pane (spawn --pane on it) retires
	// the pane's released-panes row.
	if placement == "given" {
		core.ReleasedPanesForget(ctx, env, cwd, o.pane)
	}
	sameTreeEditors(o.name, o.role, o.cwd, sd, env, cwd, ctx)
	if placement == "herd" {
		func() {
			defer func() {
				if recover() != nil {
					Warn("relabel of the herd tabs failed (see friction)", ctx, env, "spawn")
				}
			}()
			layout.HerdTabsRelabel(ctx, env, cwd)
		}()
	}
	// grok's first-run trust dialog must take no typed text: herdr reports the
	// agent ready while the dialog is on its screen, and the "n" of the next
	// typed line — "/context-window <value>" or a dispatch prompt — is "No,
	// quit", so the agent exits. This is the new-grok path only (every reuse
	// path returned before this point), before any text is typed, with or
	// without a configured window. A dialog on the screen blocks the start
	// like the other startup dialogs (screen printed, exit 7) — the user
	// must answer it, spawn never does. The first check reuses the visible
	// screen the start window just read of this new agent, so a grok without
	// a configured window gains no extra read; the typing path re-reads the
	// screen right before it types, because that read is one start-window
	// iteration old and a dialog can land on the screen in the meantime.
	if !blocked && kind == "grok" && isGrokTrustDialog(lastScreen) {
		blocked = true
	}
	// The cursor/Windows startup gate (issue #59): herdr reports the agent
	// ready (idle, interactive_ready) while the TUI never draws — the
	// verified workspace trust dialog, or historically only the shell
	// launch line on the screen. The start window observed the screen; the
	// shared assessment takes the final verdict, and a refusing verdict
	// (blocked or unverified) takes the existing blocked path —
	// blocked_at_startup, the screen printed, exit 7 — so the spawn never
	// claims ready for those cases and never types or answers the dialog.
	// Every other kind or platform keeps the window's verdict and the JSON
	// without the evidence field.
	cursorGate := false
	cursorGateEv := startup.Evaluation{}
	if !blocked && startup.Enabled(kind) {
		screen, screenOK := lastScreen, true
		if screen == "" {
			// The window never saw a non-empty screen: one fresh read tells
			// an empty screen (the TUI did not draw) from a failed read.
			screen, screenOK = herdr.AgentReadOK(env, o.name, "visible", intPtr(40))
		}
		cursorGateEv = startup.Evaluate(kind, startup.CurrentPlatform(), lastState, screen, screenOK)
		cursorGate = true
		if cursorGateEv.Refuses() {
			warnStartupBlocked(o.name, kind, lastState, cursorGateEv, ctx, env)
			blocked = true
		}
	}
	contextWindow := ""
	if !blocked && kind == "grok" {
		// context_window.grok opens the fresh grok with the requested window
		// (grok has no CLI flag for it): the command goes to this new spawn
		// only — every reuse path returned before reaching it, so a reused or
		// working grok never takes the command. An invalid value is ignored
		// here; doctor names it.
		value := core.Cfg(ctx, core.ContextWindowKey("grok"), "", env)
		typing := value != "" && core.ContextWindowValueOk(value)
		if typing && isGrokTrustDialog(herdr.AgentRead(env, o.name, "visible", intPtr(40))) {
			// The pre-typing re-read sees the trust dialog: the same block as
			// the first check — no text is typed and the start exits 7 with
			// the screen. Only the path that types the command spends this
			// read, so a grok without a configured window keeps its call
			// pattern and the frozen parity goldens stay untouched.
			blocked = true
		}
		if !blocked && typing {
			if confirmed := confirmGrokContextWindow(o, value, env); confirmed {
				contextWindow = value
			} else if herdr.AgentState(o.name, env, herdr.Timeout, nil).State == "gone" {
				// The confirmation failed and the agent is gone
				// (agent_not_found), whatever the confirmation window's reads
				// showed: it quit during the window — the typed text answered
				// a dialog. Say that, instead of the generic no-confirmation
				// warning; the status stays ready, the dispatch will name the
				// missing agent. A live grok or a transient state read keeps
				// the old warning.
				Warn(fmt.Sprintf("spawn: grok '%s' exited after /context-window; it may have been showing a dialog — read its pane", o.name), ctx, env, "spawn")
			} else {
				Warn(fmt.Sprintf("spawn: could not confirm grok's context window %s for '%s' (no \"Context window set to\" line); set it by hand with /context-window", value, o.name), ctx, env, "spawn")
			}
		}
	}
	out := jsonjs.O("name", o.name, "pane_id", o.pane, "kind", kind, "role", o.role, "family", family, "created_pane", created, "layout", layoutMode, "placement", placement, "effort", defaultValue(o.effort, "default"), "model", defaultValue(o.model, "default"), "model_spec", modelSpec, "approvals", defaultValue(o.approvals, "ask"), "agent_args", strings.Join(agentArgs, " "), "status", func() string {
		if blocked {
			return "blocked_at_startup"
		}
		return "ready"
	}())
	if contextWindow != "" {
		out.Set("context_window", contextWindow)
	}
	if burst {
		out.Set("burst", true)
	}
	if cursorGate {
		out.Set("startup_evidence", jsonjs.O(startup.FieldState, lastState, startup.FieldReason, cursorGateEv.Reason, startup.FieldSource, cursorGateEv.Source))
	}
	_, _ = fmt.Fprintln(platform.Stdout, jsonjs.StringifyIndent(out, 2))
	if autoRegrid && core.Cfg(ctx, "regrid", "on", env) == "on" {
		layout.AutoRegrid(ctx, env, cwd, "regrid after spawn failed; panes left as inserted (see friction)")
	}
	if blocked {
		if !cursorGate {
			// The gate already printed the precise warning when it refused
			// the start; the generic line names the other startup dialogs.
			Warn(fmt.Sprintf("agent '%s' is blocked during startup (update prompt, login, trust dialog…). Screen follows; ask the user before answering it, then: herdr agent send-keys %s <keys>; herdr agent wait %s --timeout 60000", o.name, o.name, o.name), ctx, env, "spawn")
		}
		_, _ = fmt.Fprint(platform.Stdout, herdr.AgentRead(env, o.name, "visible", intPtr(40)))
		platform.Die("", 7)
	}
}

func clampSpawnEffort(effort, kind, maxEffort string) string {
	return kinds.ClampTo(kinds.ClampTo(effort, kinds.KindEffortCeiling(kind)), maxEffort)
}

// warnLaneOrphans notices the idle orphans — the panes this project released
// without --close, recorded in the released-panes registry — before the
// spawn opens a new pane; it only warns, the spawn proceeds and the orphan is
// left alone. With an empty registry it makes no extra herdr reads at all.
func warnLaneOrphans(ctx *core.Config, env platform.Env, cwd string, live []any) {
	if live == nil {
		live = liveAgents(env)
	}
	released := core.ReleasedPanes(ctx, env, cwd)
	if len(released) == 0 {
		return
	}
	// A workspace read is spent only when a live agent matches a registry
	// row by pane and name.
	candidate := false
	for _, row := range released {
		for _, agent := range live {
			if fieldString(agent, "name") == row.Name && fieldString(agent, "pane_id") == row.Pane {
				candidate = true
				break
			}
		}
		if candidate {
			break
		}
	}
	if !candidate {
		return
	}
	for _, orphan := range core.OrphansOf(ctx, env, cwd, live) {
		if orphan.State == "idle" || orphan.State == "done" {
			_, _ = fmt.Fprintf(platform.Stderr, "soho: spawn: '%s' is idle outside the roster (%s); close it with: soho release %s --close\n", orphan.Name, orphan.Pane, orphan.Name)
		}
	}
}

// warnLaneIdleOtherKind notices the old workers a lane-kind change left
// behind: when this spawn opens a new worker on the lane, each lane worker
// that Herdr reports idle or done and whose roster kind is not the lane's
// effective kind is named once, with the release --close remedy. It only
// warns — the spawn proceeds, nothing is closed, nothing is waited on and
// the exit code is unchanged — and a worker that is not live (or is
// working, blocked, or of the lane's own kind) is not named. It reads the
// live list the name check just made (passed in) and fetches it at most once
// for a named spawn, so the check spends no per-worker agent get.
func warnLaneIdleOtherKind(sd, lane, kind string, env platform.Env, live []any) {
	if live == nil {
		live = liveAgents(env)
	}
	statusByName := map[string]string{}
	for _, a := range live {
		statusByName[fieldString(a, "name")] = fieldString(a, "agent_status")
	}
	for _, row := range core.LaneWorkers(sd, lane) {
		f := strings.Split(row, "\t")
		name, wk := fieldAt(f, 0), fieldAt(f, 2)
		if name == "" || wk == "" || wk == kind {
			continue
		}
		if st := statusByName[name]; st == "idle" || st == "done" {
			_, _ = fmt.Fprintf(platform.Stderr, "soho: warning: lane '%s' still holds idle '%s' (%s), not the lane's kind '%s'; release it with: soho release %s --close\n", lane, name, wk, kind, name)
		}
	}
}

func has(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
func intPtr(v int) *int { return &v }
func boolInt(v bool) string {
	if v {
		return "1"
	}
	return "0"
}
func boolText(v bool) string {
	if v {
		return "burst"
	}
	return ""
}
func buildArgs(kind, approvals, model, effort string, ctx *core.Config, env platform.Env) []string {
	out := kinds.KindContextArgs(kind, core.Cfg(ctx, "worker_context", "full", env))
	a, err := kinds.KindApprovalArgs(kind, approvals, func(msg string) { Warn(msg, ctx, env, "spawn") })
	if err != nil {
		core.DieFriction(err.Error(), 2, "", "")
	}
	out = append(out, a...)
	out = append(out, kinds.KindModelArgs(kind, model, effort, func(msg string) { Warn(msg, ctx, env, "spawn") })...)
	out = append(out, kinds.KindEffortArgs(kind, effort, model, env, func(msg string) { Warn(msg, ctx, env, "spawn") })...)
	return out
}

// ResolveMcp applies the per-role MCP server precedence (DJA-392): flag >
// lane.<n>.mcp > role.<r>.mcp > the role file's mcp: > inherit.
func ResolveMcp(flag, lane, role string, ctx *core.Config, env platform.Env, cwd string) string {
	if flag != "" {
		return flag
	}
	if lane != "" {
		if v := core.LaneAttr(ctx, lane, "mcp", nil, env); v != "" {
			return v
		}
	}
	if v := core.Cfg(ctx, "role_"+strings.ReplaceAll(role, "-", "_")+"_mcp", "", env); v != "" {
		return v
	}
	if file := core.RoleFile(role, env, cwd); file != "" {
		if v := core.FmGet(file, "mcp"); v != "" {
			return v
		}
	}
	return "inherit"
}

func busyLane(lane, name, hint, role string, ctx *core.Config, env platform.Env, cwd string) {
	_, _ = fmt.Fprintln(platform.Stdout, jsonjs.Stringify(jsonjs.O("status", "busy", "lane", lane, "name", name)))
	if hint == "" {
		roles := core.Cfg(ctx, "flex_roles", "reviewer,documenter", env)
		if !has(core.SplitRoles(roles), role) {
			hint = fmt.Sprintf("role '%s' may not use the temporary panel (flex_roles=%s)", role, roles)
		} else {
			live := core.LiveBurstWorkers(core.StateDirPath(ctx, env, cwd), env)
			remedy := "Raise flex_extra."
			if len(live) > 0 {
				remedy = fmt.Sprintf("Release one (release %s), or raise flex_extra.", live[0])
			}
			hint = fmt.Sprintf("lane '%s' only takes a temporary worker (pane_mode=flex) and none is free: flex_extra=%d, live temporary workers: %s. %s", lane, core.FlexExtra(ctx, env), func() string {
				if len(live) == 0 {
					return "none"
				}
				return strings.Join(live, ", ")
			}(), remedy)
		}
		if role == "" {
			// The generic lane-capacity path uses the lane occupant hint below.
			hint = fmt.Sprintf("lane '%s' is full", lane)
		}
	}
	core.Warn(hint, filepath.Join(core.StateDirPath(ctx, env, cwd), "friction.log"), "spawn")
	platform.Die("", 10)
}
func mismatchLane(name, lane, actual, want, sessionModel, requestedModel, sessionEffort, requestedEffort string, ctx *core.Config, env platform.Env, cwd string) {
	obj := jsonjs.O("status", "kind-mismatch", "lane", lane, "name", name, "session_kind", actual, "requested_kind", want, "session_model", sessionModel, "requested_model", requestedModel, "session_effort", sessionEffort, "requested_effort", requestedEffort)
	_, _ = fmt.Fprintln(platform.Stdout, jsonjs.Stringify(obj))
	core.Warn(fmt.Sprintf("lane '%s' worker '%s' is %s (model %s, effort %s); this role wants %s (model %s, effort %s). Set lane.%s.kind or release the lane, then spawn again.", lane, name, actual, defaultValue(sessionModel, "?"), defaultValue(sessionEffort, "?"), want, defaultValue(requestedModel, "?"), defaultValue(requestedEffort, "?"), lane), filepath.Join(core.StateDirPath(ctx, env, cwd), "friction.log"), "spawn")
	platform.Die("", 13)
}
func mismatchSimple(name, lane, actual, want string, ctx *core.Config, env platform.Env, cwd string) {
	obj := jsonjs.O("status", "kind-mismatch", "lane", lane, "name", name, "session_kind", actual, "requested_kind", want)
	_, _ = fmt.Fprintln(platform.Stdout, jsonjs.Stringify(obj))
	core.Warn(fmt.Sprintf("lane '%s' worker '%s' runs %s but lane.%s.kind is %s. Release the lane ('release %s --close'), then spawn again.", lane, name, actual, lane, want, name), filepath.Join(core.StateDirPath(ctx, env, cwd), "friction.log"), "spawn")
	platform.Die("", 13)
}

func startAgent(o spawnOptions, kind string, env platform.Env, created bool, args []string) bool {
	for tries := 0; ; tries++ {
		argv := []string{"agent", "start", o.name, "--kind", kind, "--pane", o.pane, "--timeout", o.timeout}
		if len(args) > 0 {
			argv = append(argv, "--")
			argv = append(argv, args...)
		}
		r := platform.RunCli("herdr", argv, platform.RunOptions{Env: env, TimeoutMs: int(startWrapperTimeoutMs(o.timeout)), MergeOutput: true})
		out := r.Stdout + r.Stderr
		if !r.NotFound && r.Status != nil && *r.Status == 0 {
			return false
		}
		if strings.Contains(out, "agent_not_ready") {
			return true
		}
		if strings.Contains(out, "agent_pane_busy") && tries < 15 {
			time.Sleep(busyRetryDelay)
			continue
		}
		if strings.TrimRight(out, "\n") != "" {
			_, _ = fmt.Fprintln(platform.Stderr, strings.TrimRight(out, "\n"))
		}
		if created {
			scr := herdr.AgentRead(env, o.pane, "visible", intPtr(40))
			tail := lastLines(scr, 5)
			closed := herdr.PaneClose(o.pane, env)
			if closed {
				core.DieFriction(fmt.Sprintf("agent start failed for %s (%s) in pane %s; closed the pane this spawn opened (last screen lines: %s)", o.name, kind, o.pane, tail), 4, "", "")
			}
			if herdr.PaneGetGone(o.pane, env) {
				core.DieFriction(fmt.Sprintf("agent start failed for %s (%s) in pane %s; the pane close failed but the pane is already gone (last screen lines: %s)", o.name, kind, o.pane, tail), 4, "", "")
			}
			core.DieFriction(fmt.Sprintf("agent start failed for %s (%s) in pane %s; pane close failed and the pane is still open, check it for a running agent (last screen lines: %s)", o.name, kind, o.pane, tail), 4, "", "")
		}
		core.DieFriction(fmt.Sprintf("agent start failed for %s (%s) in pane %s; the pane was given (--pane) and stays open: check it for a running agent", o.name, kind, o.pane), 4, "", "")
	}
}
func startWrapperTimeoutMs(raw string) float64 {
	n, ok := text.ParseJSNumber(raw)
	if !ok || math.IsNaN(n) || math.IsInf(n, 0) {
		return 30_000
	}
	return n + 30_000
}

// grokTrustDialogQuestion is grok's first-run trust question line and
// grokTrustDialogYes / grokTrustDialogNo its answer lines, quoted from the
// dialog on a real machine: "Do you trust the contents of this directory?"
// with "Yes, proceed" (y) and "No, quit" (n).
const (
	grokTrustDialogQuestion = "do you trust the contents of this directory"
	grokTrustDialogYes      = "yes, proceed"
	grokTrustDialogNo       = "no, quit"
)

// isGrokTrustDialog reports whether the visible screen of a fresh grok shows
// its first-run trust dialog: the question line, or both answer lines on
// distinct lines. The match is case-insensitive and tolerant of the extra
// spaces the TUI padding adds to the lines. One answer line alone is not a
// dialog, and neither is a normal sentence that cites both answers on one
// line — the dialog draws one answer per line, so a line that holds both
// answers contributes neither.
func isGrokTrustDialog(screen string) bool {
	question, yes, no := false, false, false
	for _, line := range strings.Split(screen, "\n") {
		norm := strings.Join(strings.Fields(text.ASCIILower(line)), " ")
		if strings.Contains(norm, grokTrustDialogQuestion) {
			question = true
		}
		hasYes, hasNo := strings.Contains(norm, grokTrustDialogYes), strings.Contains(norm, grokTrustDialogNo)
		if hasYes && !hasNo {
			yes = true
		}
		if hasNo && !hasYes {
			no = true
		}
	}
	return question || (yes && no)
}

// confirmGrokContextWindow opens the requested context window on the fresh
// grok: it types /context-window <value> into the pane, pauses for the menu,
// sends the Enter, and then reads the visible screen until grok's
// "Context window set to <value>" line appears (up to the window). It is
// true only when the line was seen — spawn records the window in its JSON;
// otherwise it warns and the spawn still succeeds. When it is false the
// caller spends one agent state read: a gone agent (agent_not_found) gets
// the exit warning, a live one or a transient state read keeps the generic
// warning. Callers run it on the new-spawn path only, after the start
// window; a reused or working grok never gets it.
func confirmGrokContextWindow(o spawnOptions, value string, env platform.Env) bool {
	_ = herdr.PaneSendText(o.pane, "/context-window "+value, env)
	time.Sleep(contextWindowSettle)
	_ = herdr.PaneSendKeys(o.pane, "Enter", env)
	needle := "Context window set to " + value
	until := time.Now().Add(contextWindowWindow)
	for {
		if strings.Contains(herdr.AgentRead(env, o.name, "visible", intPtr(40)), needle) {
			return true
		}
		if !time.Now().Before(until) {
			return false
		}
		time.Sleep(pollMillis(env))
	}
}

func updateGoneWaitMs(raw string) float64 {
	n, ok := text.ParseJSNumber(raw)
	if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n <= 0 {
		return 60_000
	}
	return n
}
func pollMillis(env platform.Env) time.Duration {
	n, err := strconv.Atoi(env.Get("SOHO_WAIT_POLL_MS"))
	if err == nil && n >= 1 && n <= 3000 {
		return time.Duration(n) * time.Millisecond
	}
	return 500 * time.Millisecond
}
func checkStartWindow(o spawnOptions, kind string, env platform.Env, created bool, args []string, ctx *core.Config) (string, string, bool) {
	poll := time.Duration(PollIntervalMs(env)) * time.Millisecond
	canRelaunch := true
	blocked := false
	for {
		end := time.Now().Add(startPollWindow)
		lastScreen, lastState := "", ""
		marker := false
		for {
			st := herdr.AgentState(o.name, env, herdr.Timeout, nil)
			lastState = st.State
			screen := herdr.AgentRead(env, o.name, "visible", intPtr(40))
			if screen != "" {
				lastScreen = screen
			}
			if kind == "codex" && (strings.Contains(screen, "Updating Codex via") || strings.Contains(screen, "Please restart Codex")) {
				marker = true
			}
			if st.State == "gone" {
				if canRelaunch && marker {
					break
				}
				if len(strings.TrimSpace(lastScreen)) == 0 {
					lastScreen = herdr.AgentRead(env, o.name, "visible", intPtr(40))
				}
				providerErr := provider.ProviderDetect("gone", lastScreen)
				if providerErr != nil && providerErr.Status == "provider-error" && providerErr.Auth {
					core.DieFriction(fmt.Sprintf("agent '%s' (%s) hit a terminal provider authentication error right after start: %s; pane %s stays open for inspection", o.name, kind, providerErr.Cause, o.pane), 14, "", "")
				}
				core.DieFriction(fmt.Sprintf("agent '%s' (%s) exited right after start; last screen lines: %s", o.name, kind, lastLines(lastScreen, 5)), 4, "", "")
			}
			if marker && canRelaunch {
				break
			}
			// The cursor/Windows startup gate (issue #59): the verified
			// trust dialog stops the observation immediately, and a blank or
			// launch-only screen is re-probed until the window ends (the TUI
			// can draw late) instead of ending it on the first probe; every
			// other kind or platform returns exactly as before.
			if startup.Enabled(kind) && startup.ActiveTrustDialog(screen) {
				return lastState, lastScreen, blocked
			}
			hold := startup.Enabled(kind) && (strings.TrimSpace(screen) == "" || startup.LaunchOnly(screen))
			if (!marker && !hold) || !time.Now().Before(end) {
				return lastState, lastScreen, blocked
			}
			time.Sleep(poll)
		}
		to := updateGoneWaitMs(o.timeout)
		until := time.Now().Add(time.Duration(to * float64(time.Millisecond)))
		st := herdr.AgentState(o.name, env, herdr.Timeout, nil)
		for st.State != "gone" && time.Now().Before(until) {
			time.Sleep(poll)
			st = herdr.AgentState(o.name, env, herdr.Timeout, nil)
		}
		if st.State != "gone" {
			return st.State, lastScreen, blocked
		}
		blocked = startAgent(o, kind, env, created, args)
		Warn(fmt.Sprintf("'%s' (%s) updated itself at start and exited; started it again", o.name, kind), ctx, env, "spawn")
		canRelaunch = false
	}
}
func lastLines(s string, n int) string {
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		if v := strings.TrimSpace(line); v != "" {
			lines = append(lines, v)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " / ")
}
func staleRosterLines(sd, name, pane string) [][2]string {
	seen := map[string]bool{}
	var out [][2]string
	for _, line := range core.RosterRows(sd) {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		nm, pn := fieldAt(f, 0), fieldAt(f, 1)
		if nm == "" || seen[nm] {
			continue
		}
		if nm == name || pane != "" && pn == pane {
			seen[nm] = true
			out = append(out, [2]string{nm, pn})
		}
	}
	return out
}

func sameTreeEditors(thisName, role, workerCwd, sd string, env platform.Env, cwd string, ctx *core.Config) {
	if workerCwd == "" || !core.RoleIsEdit(role, env, cwd) {
		return
	}
	type candidate struct{ name, pane string }
	var candidates []candidate
	for _, line := range core.RosterRows(sd) {
		f := strings.Split(line, "\t")
		name := fieldAt(f, 0)
		if name == "" || name == thisName || !core.RoleIsEdit(fieldAt(f, 3), env, cwd) || fieldAt(f, 6) != workerCwd {
			continue
		}
		candidates = append(candidates, candidate{name, fieldAt(f, 1)})
	}
	if len(candidates) == 0 {
		return
	}
	var agents []any
	func() {
		defer func() { _ = recover() }()
		agents = herdr.LiveAgents(env, herdr.Timeout)
	}()
	for _, cand := range candidates {
		for _, agent := range agents {
			if fieldString(agent, "name") != cand.name || cand.pane != "" && fieldString(agent, "pane_id") != cand.pane {
				continue
			}
			if !sharedTreeWarnDue(sd, thisName, cand.name, workerCwd, ctx, env) {
				break
			}
			Warn(fmt.Sprintf("'%s' and '%s' both edit %s: builds and test runs see each other's changes in progress; give each a git worktree (spawn --cwd <worktree>) to isolate them", thisName, cand.name, workerCwd), ctx, env, "spawn")
			break
		}
	}
}

// sharedTreeWarnDue records the shared-tree warn of one unordered pair in
// one cwd as a wait marker (shared-tree-<hash>.warned, written like the
// other state markers) and reports whether the warn is still due: the same
// pair in the same cwd stays silent, a new pair or another cwd warns again.
// The marker lives in the workspace's wait dir; the state dir only reached
// spawn through core.StateDir, whose inside-the-skill refusal dies before any
// write, so a marker is never created inside the skill.
func sharedTreeWarnDue(sd, a, b, workerCwd string, ctx *core.Config, env platform.Env) bool {
	first, second := a, b
	if second < first {
		first, second = second, first
	}
	stored := first + "\x00" + second + "\x00" + workerCwd
	sum := fnv.New64a()
	_, _ = sum.Write([]byte(stored))
	marker := filepath.Join(sd, "wait", "shared-tree-"+fmt.Sprintf("%016x", sum.Sum64())+".warned")
	if prev, err := platform.ReadTextFile(marker); err == nil && strings.TrimRight(prev, "\n") == stored {
		return false
	}
	if err := os.MkdirAll(filepath.Join(sd, "wait"), 0o777); err != nil {
		Warn(fmt.Sprintf("spawn: could not record the shared-tree warning marker: %s", text.SanitizeCause(err.Error())), ctx, env, "spawn")
		return true
	}
	if err := os.WriteFile(marker, []byte(stored+"\n"), 0o666); err != nil {
		Warn(fmt.Sprintf("spawn: could not record the shared-tree warning marker: %s", text.SanitizeCause(err.Error())), ctx, env, "spawn")
		return true
	}
	return true
}

// PollIntervalMs is the shared wait poll override used by spawn start checks.
func PollIntervalMs(env platform.Env) int {
	d := pollMillis(env)
	if d > 500*time.Millisecond {
		return 500
	}
	return int(d.Milliseconds())
}
