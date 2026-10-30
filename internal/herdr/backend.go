package herdr

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"time"

	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/sessionref"
	"github.com/djalmajr/soho/internal/transport"
)

// backend is the stage-1 Herdr implementation of the transport contracts.
// Each method delegates to the existing adapter function in this package with
// the same argv, timeout, and parsing, so nothing observable changes. It is
// built per invocation (New); there is no package singleton and no global
// active backend. A method is present only when an existing adapter function
// backs it; a per-call context is threaded only where the wrapped function
// accepts one, and a wrapped function with a fixed timeout ignores
// Call.Timeout.
type backend struct {
	env platform.Env
}

// New builds the Herdr transport services for one invocation over env. It
// returns the Transport, Window, LegacyTransport, and Runtime implementations;
// the caller holds them for the duration of the invocation.
func New(env platform.Env) transport.Services {
	b := &backend{env: env}
	return transport.Services{Runtime: b, Window: b, Legacy: b, Terminal: b}
}

// Compile-time proof that the backend implements the contracts.
var (
	_ transport.Transport       = (*backend)(nil)
	_ transport.Window          = (*backend)(nil)
	_ transport.LegacyTransport = (*backend)(nil)
	_ transport.Runtime         = (*backend)(nil)
)

// timeoutFor returns call.Timeout when it is positive, else the adapter's
// default Timeout.
func timeoutFor(call transport.Call) time.Duration {
	if call.Timeout > 0 {
		return call.Timeout
	}
	return Timeout
}

// ctxArgs adapts a per-call context to the optional variadic call context of
// the adapter functions that accept one; it is empty when ctx is nil.
func ctxArgs(ctx context.Context) []context.Context {
	if ctx == nil {
		return nil
	}
	return []context.Context{ctx}
}

// processResult mirrors a platform.RunResult into the transport value type.
func processResult(r platform.RunResult) transport.ProcessResult {
	return transport.ProcessResult{
		NotFound: r.NotFound,
		Resolved: r.Resolved,
		Status:   r.Status,
		Signal:   r.Signal,
		Stdout:   r.Stdout,
		Stderr:   r.Stderr,
		TimedOut: r.TimedOut,
		Error:    r.Error,
	}
}

// seqRaw renders an AgentStateResult.Seq (an integer, a number, or "") as
// JSON, leaving it empty when it carries no sequence.
func seqRaw(v any) json.RawMessage {
	if v == "" || v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

// revisionInt renders a pane revision (a non-negative integer, or absent) as
// an int, zero when it is not a whole number.
func revisionInt(v any) int {
	switch n := jsSequence(v).(type) {
	case int64:
		return int(n)
	case float64:
		return int(n)
	default:
		return 0
	}
}

// terminalFromAgent projects one parsed agent object (an entry of LiveAgents)
// into a Terminal.
func terminalFromAgent(v any) (transport.Terminal, bool) {
	obj, ok := v.(*jsonjs.Object)
	if !ok {
		return transport.Terminal{}, false
	}
	// Target.ID is the agent name Get takes, so a listed terminal can be
	// read back through the same target.
	name := str(get(obj, "name"))
	return transport.Terminal{
		Target:   transport.Target{ID: name},
		Name:     name,
		PaneID:   str(get(obj, "pane_id")),
		State:    str(get(obj, "agent_status")),
		Sequence: seqRaw(jsSequence(get(obj, "state_change_seq"))),
	}, true
}

// viewFromPane projects one parsed pane object (an entry of PaneList) into a
// View, keeping the raw object.
func viewFromPane(v any) (transport.View, bool) {
	obj, ok := v.(*jsonjs.Object)
	if !ok {
		return transport.View{}, false
	}
	return transport.View{
		ID:          str(get(obj, "pane_id")),
		PaneID:      str(get(obj, "pane_id")),
		TabID:       str(get(obj, "tab_id")),
		WorkspaceID: str(get(obj, "workspace_id")),
		Label:       str(get(obj, "label")),
		RootPane:    str(get(obj, "root_pane", "pane_id")),
		Revision:    revisionInt(get(obj, "revision")),
		Raw:         json.RawMessage(jsonjs.Stringify(v)),
	}, true
}

// viewFromTab projects one parsed tab object (an entry of TabList) into a
// View, keeping the raw object.
func viewFromTab(v any) (transport.View, bool) {
	obj, ok := v.(*jsonjs.Object)
	if !ok {
		return transport.View{}, false
	}
	return transport.View{
		ID:       str(get(obj, "tab_id")),
		TabID:    str(get(obj, "tab_id")),
		Label:    str(get(obj, "label")),
		RootPane: str(get(obj, "root_pane", "pane_id")),
		Raw:      json.RawMessage(jsonjs.Stringify(v)),
	}, true
}

// processInfo runs `herdr pane process-info --pane <pane>` with the same
// bound the caller-pane check uses, and returns the raw result. CheckCaller
// derives shell_pid from the same call.
func processInfo(pane string, env platform.Env) platform.RunResult {
	return runWithTimeout([]string{"pane", "process-info", "--pane", pane}, env, 10000*time.Millisecond)
}

// Transport.

// Caller returns the identity claimed from the inherited environment. It is
// pure and never queries a server.
func (b *backend) Caller() transport.Caller {
	return transport.Caller{
		Inside:      b.env.Get("HERDR_ENV") == "1",
		PaneID:      b.env.Get("HERDR_PANE_ID"),
		WorkspaceID: b.env.Get("HERDR_WORKSPACE_ID"),
		TabID:       b.env.Get("HERDR_TAB_ID"),
	}
}

// Current resolves the calling pane's actual identity through the native CLI.
func (b *backend) Current(ctx context.Context, call transport.Call) transport.Reply[transport.Caller] {
	res := PaneCurrent(b.env)
	if !res.Ok {
		return transport.Reply[transport.Caller]{Err: &transport.Failure{Kind: transport.FailureUnavailable, Message: "current pane unresolved"}}
	}
	return transport.Reply[transport.Caller]{
		Value: transport.Caller{
			Inside:      true,
			PaneID:      res.PaneID,
			WorkspaceID: res.WorkspaceID,
			TabID:       res.TabID,
		},
		Valid: true,
	}
}

// CheckCaller verifies the calling process runs in the claimed pane.
func (b *backend) CheckCaller(ctx context.Context, call transport.Call) transport.Reply[transport.CallerCheck] {
	res := CheckCallerPane(b.env)
	return transport.Reply[transport.CallerCheck]{
		Value: transport.CallerCheck{
			State:       string(res.State),
			Pane:        res.Pane,
			ShellPID:    res.ShellPID,
			Reason:      res.Reason,
			Session:     res.Session,
			PaneSession: res.PaneSession,
			ShownIn:     res.ShownIn,
		},
		Valid: true,
	}
}

// List returns the registered terminals (agents).
func (b *backend) List(ctx context.Context, scope transport.Scope, call transport.Call) transport.Reply[[]transport.Terminal] {
	agents := LiveAgents(b.env, timeoutFor(call))
	out := make([]transport.Terminal, 0, len(agents))
	for _, a := range agents {
		if term, yes := terminalFromAgent(a); yes {
			out = append(out, term)
		}
	}
	return transport.Reply[[]transport.Terminal]{Value: out, Valid: true}
}

// Get reads one terminal's state. An unavailable read (or an empty state)
// still returns the terminal with the state and sequence the adapter
// reported, plus the cause as an error; a gone terminal is a valid state
// with no error.
func (b *backend) Get(ctx context.Context, target transport.Target, call transport.Call) transport.Reply[transport.Terminal] {
	res := AgentState(target.ID, b.env, timeoutFor(call), nil, ctxArgs(ctx)...)
	term := transport.Terminal{Target: target, State: res.State, Sequence: seqRaw(res.Seq)}
	if res.State == "" || res.State == "unavailable" {
		return transport.Reply[transport.Terminal]{Value: term, Valid: true, Err: &transport.Failure{Kind: transport.FailureUnavailable, Message: res.Cause}}
	}
	return transport.Reply[transport.Terminal]{Value: term, Valid: true}
}

// Send submits a prompt to a terminal and returns the merged output.
func (b *backend) Send(ctx context.Context, target transport.Target, send transport.Send, call transport.Call) transport.Reply[string] {
	res := AgentPrompt(target.ID, send.Text, b.env)
	if !res.Ok {
		return transport.Reply[string]{Err: &transport.Failure{Kind: transport.FailureCommand, Message: res.Raw}}
	}
	return transport.Reply[string]{Value: res.Raw, Valid: true}
}

// Keys sends raw keys; pane selects the pane channel, else the agent channel.
func (b *backend) Keys(ctx context.Context, target transport.Target, keys string, pane bool, call transport.Call) transport.Reply[bool] {
	var yes bool
	if pane {
		yes = PaneSendKeys(target.ID, keys, b.env, ctxArgs(ctx)...)
	} else {
		yes = AgentSendKeys(target.ID, keys, b.env)
	}
	return transport.Reply[bool]{Value: yes, Valid: true}
}

// Read returns a terminal's screen, or a pane's screen when Read.Pane.
func (b *backend) Read(ctx context.Context, target transport.Target, read transport.Read, call transport.Call) transport.Reply[string] {
	if read.Pane {
		text, yes := PaneRead(b.env, target.ID, read.Source, read.Lines)
		if !yes {
			return transport.Reply[string]{Err: &transport.Failure{Kind: transport.FailureUnavailable, Message: "pane read failed"}}
		}
		return transport.Reply[string]{Value: text, Valid: true}
	}
	text, yes := AgentReadOK(b.env, target.ID, read.Source, read.Lines, ctxArgs(ctx)...)
	if !yes {
		return transport.Reply[string]{Err: &transport.Failure{Kind: transport.FailureUnavailable, Message: "agent read failed"}}
	}
	return transport.Reply[string]{Value: text, Valid: true}
}

// Close closes a terminal's pane.
func (b *backend) Close(ctx context.Context, target transport.Target, call transport.Call) transport.Reply[bool] {
	return transport.Reply[bool]{Value: PaneClose(target.ID, b.env), Valid: true}
}

// Window.

// Layout returns the raw layout for the current pane, or for pane.
func (b *backend) Layout(ctx context.Context, pane string, call transport.Call) transport.Reply[json.RawMessage] {
	r := PaneLayout(b.env, pane)
	if !ok(r) {
		return transport.Reply[json.RawMessage]{Process: processResult(r), Err: &transport.Failure{Kind: transport.FailureCommand, Message: r.Stderr}}
	}
	return transport.Reply[json.RawMessage]{Value: json.RawMessage(r.Stdout), Valid: true, Process: processResult(r)}
}

// ListPanes lists panes, scoped as Scope selects.
func (b *backend) ListPanes(ctx context.Context, scope transport.Scope, call transport.Call) transport.Reply[[]transport.View] {
	var panes []any
	if scope.All {
		panes = PaneListAll(b.env)
	} else {
		panes = PaneList(b.env, scope.WorkspaceID)
	}
	out := make([]transport.View, 0, len(panes))
	for _, p := range panes {
		if view, yes := viewFromPane(p); yes {
			out = append(out, view)
		}
	}
	return transport.Reply[[]transport.View]{Value: out, Valid: true}
}

// ListTabs lists the tabs of a workspace.
func (b *backend) ListTabs(ctx context.Context, workspace string, call transport.Call) transport.Reply[[]transport.View] {
	tabs := TabList(b.env, workspace)
	out := make([]transport.View, 0, len(tabs))
	for _, tab := range tabs {
		if view, yes := viewFromTab(tab); yes {
			out = append(out, view)
		}
	}
	return transport.Reply[[]transport.View]{Value: out, Valid: true}
}

// Tab reads one tab's label and root pane.
func (b *backend) Tab(ctx context.Context, tab string, call transport.Call) transport.Reply[transport.View] {
	res := TabGet(tab, b.env)
	if !res.Ok {
		return transport.Reply[transport.View]{Err: &transport.Failure{Kind: transport.FailureNotFound, Message: "tab not found"}}
	}
	return transport.Reply[transport.View]{Value: transport.View{ID: tab, Label: res.Label, RootPane: res.Root}, Valid: true}
}

// CreateTab creates a tab and reports it.
func (b *backend) CreateTab(ctx context.Context, open transport.Open, call transport.Call) transport.Reply[transport.View] {
	res := TabCreate(b.env, open.WorkspaceID, open.Cwd, open.Label)
	if !res.Ok {
		return transport.Reply[transport.View]{Err: &transport.Failure{Kind: transport.FailureCommand, Message: "tab create failed"}}
	}
	return transport.Reply[transport.View]{Value: transport.View{ID: res.Tab, RootPane: res.Root}, Valid: true}
}

// RenameTab renames a tab.
func (b *backend) RenameTab(ctx context.Context, tab, label string, call transport.Call) transport.Reply[bool] {
	return transport.Reply[bool]{Value: TabRename(tab, label, b.env), Valid: true}
}

// Split splits a pane and reports the new pane.
func (b *backend) Split(ctx context.Context, open transport.Open, call transport.Call) transport.Reply[transport.View] {
	res := PaneSplit(open.Anchor, open.Direction, open.Cwd, b.env, open.Ratio)
	if !res.Ok {
		return transport.Reply[transport.View]{Err: &transport.Failure{Kind: transport.FailureCommand, Message: "pane split failed"}}
	}
	return transport.Reply[transport.View]{Value: transport.View{PaneID: res.Pane}, Valid: true}
}

// moveArgs encodes a Move into the `pane move` argument vector the existing
// caller passes unchanged.
func moveArgs(m transport.Move) []string {
	args := []string{m.Pane}
	if m.NewTab {
		args = append(args, "--new-tab")
	}
	if m.Tab != "" {
		args = append(args, "--tab", m.Tab)
	}
	if m.Split != "" {
		args = append(args, "--split", m.Split)
	}
	if m.TargetPane != "" {
		args = append(args, "--target-pane", m.TargetPane)
	}
	if m.Ratio != "" {
		args = append(args, "--ratio", m.Ratio)
	}
	if m.Label != "" {
		args = append(args, "--label", m.Label)
	}
	if m.NoFocus {
		args = append(args, "--no-focus")
	}
	return args
}

// Move moves a pane and reports where it landed.
func (b *backend) Move(ctx context.Context, m transport.Move, call transport.Call) transport.Reply[transport.View] {
	res := PaneMove(moveArgs(m), b.env)
	if !res.Ok {
		return transport.Reply[transport.View]{Err: &transport.Failure{Kind: transport.FailureCommand, Message: "pane move failed"}}
	}
	return transport.Reply[transport.View]{Value: transport.View{PaneID: res.Pane, TabID: res.Tab}, Valid: true}
}

// Focus focuses an agent, or a pane by direction.
func (b *backend) Focus(ctx context.Context, f transport.Focus, call transport.Call) transport.Reply[bool] {
	var yes bool
	if f.Direction != "" {
		yes = PaneFocusBack(f.Direction, f.PaneID, b.env)
	} else {
		yes = AgentFocus(f.Target.ID, b.env)
	}
	return transport.Reply[bool]{Value: yes, Valid: true}
}

// Title sets or clears (nil) a pane's title.
func (b *backend) Title(ctx context.Context, pane string, title *string, call transport.Call) transport.Reply[bool] {
	return transport.Reply[bool]{Value: PaneTitle(pane, title, b.env), Valid: true}
}

// Notify shows a system notification with the fixed "done" sound.
func (b *backend) Notify(ctx context.Context, title, body string, call transport.Call) transport.Reply[bool] {
	r := notificationShow(title, body, b.env)
	if !ok(r) {
		return transport.Reply[bool]{Process: processResult(r), Err: &transport.Failure{Kind: transport.FailureCommand, Message: r.Stderr}}
	}
	return transport.Reply[bool]{Value: true, Valid: true, Process: processResult(r)}
}

// LegacyTransport.

// ProcessInfo returns the raw process info a backend reports for a pane.
func (b *backend) ProcessInfo(ctx context.Context, target transport.Target, call transport.Call) transport.Reply[json.RawMessage] {
	r := processInfo(target.ID, b.env)
	if !ok(r) {
		return transport.Reply[json.RawMessage]{Process: processResult(r), Err: &transport.Failure{Kind: transport.FailureCommand, Message: r.Stderr}}
	}
	return transport.Reply[json.RawMessage]{Value: json.RawMessage(r.Stdout), Valid: true, Process: processResult(r)}
}

// Rename renames the agent the backend resolves for the target's id with the
// exact `agent rename` call; the raw process result stays on the reply for
// callers that parse its error output.
func (b *backend) Rename(ctx context.Context, target transport.Target, name string, call transport.Call) transport.Reply[bool] {
	r := runWithTimeout([]string{"agent", "rename", target.ID, name}, b.env, timeoutFor(call), ctxArgs(ctx)...)
	if !ok(r) {
		return transport.Reply[bool]{Process: processResult(r), Err: &transport.Failure{Kind: transport.FailureCommand, Message: r.Stderr}}
	}
	return transport.Reply[bool]{Value: true, Valid: true, Process: processResult(r)}
}

// AgentGetOK is the raw one-attempt existence check: one agent get on the
// target's id with the same timeout, Value true only when the call is not a
// lookup failure and exits 0. It never retries and does not parse the body,
// so an exit-0 answer without an agent_status still reports true.
func (b *backend) AgentGetOK(ctx context.Context, target transport.Target, call transport.Call) transport.Reply[bool] {
	r := runWithTimeout([]string{"agent", "get", target.ID}, b.env, timeoutFor(call), ctxArgs(ctx)...)
	if !ok(r) {
		return transport.Reply[bool]{Process: processResult(r), Err: &transport.Failure{Kind: transport.FailureCommand, Message: r.Stderr}}
	}
	return transport.Reply[bool]{Value: true, Valid: true, Process: processResult(r)}
}

// AgentGetKnown is the raw one-attempt knowledge check: one agent get, with
// the machine prefix the session reference names for a non-local target's
// machine, Value true only when the call exits 0 and its body carries a
// non-empty string agent_status. A timeout, a lookup failure and every
// failed call report false with the cause; an exit-0 body without a usable
// agent_status is a well-formed observation of false, not an error.
func (b *backend) AgentGetKnown(ctx context.Context, target transport.Target, call transport.Call) transport.Reply[bool] {
	args := append(append([]string{}, sessionref.HerdrMachineArgs(target.Machine)...), "agent", "get", target.ID)
	r := runWithTimeout(args, b.env, timeoutFor(call), ctxArgs(ctx)...)
	if r.TimedOut || r.Error == "ETIMEDOUT" || r.NotFound || r.Status == nil || *r.Status != 0 {
		return transport.Reply[bool]{Process: processResult(r), Err: &transport.Failure{Kind: transport.FailureCommand, Message: r.Stderr}}
	}
	status, _ := get(parsed(r.Stdout), "result", "agent", "agent_status").(string)
	if status == "" {
		return transport.Reply[bool]{Valid: true, Process: processResult(r)}
	}
	return transport.Reply[bool]{Value: true, Valid: true, Process: processResult(r)}
}

// AgentReadRaw runs the exact `agent read <id> --source <source>
// --lines <lines>` call once with the call's timeout and returns the raw
// process result: Value carries the stdout (possibly partial) and the
// caller owns the fallback output and exit-code handling.
func (b *backend) AgentReadRaw(ctx context.Context, target transport.Target, source, lines string, call transport.Call) transport.Reply[string] {
	r := runWithTimeout([]string{"agent", "read", target.ID, "--source", source, "--lines", lines}, b.env, timeoutFor(call), ctxArgs(ctx)...)
	if !ok(r) {
		return transport.Reply[string]{Value: r.Stdout, Process: processResult(r), Err: &transport.Failure{Kind: transport.FailureCommand, Message: r.Stderr}}
	}
	return transport.Reply[string]{Value: r.Stdout, Valid: true, Process: processResult(r)}
}

// AgentListRaw runs `agent list` once with the call's timeout and returns
// the raw agent objects the adapter parses (one JSON value per agent), so
// callers that read fields beyond the terminal projection keep them. The
// adapter's die-on-failure behavior happens inside the wrapped call, exactly
// as the caller's recover used to catch it.
func (b *backend) AgentListRaw(ctx context.Context, call transport.Call) transport.Reply[[]json.RawMessage] {
	agents := LiveAgents(b.env, timeoutFor(call))
	out := make([]json.RawMessage, 0, len(agents))
	for _, a := range agents {
		out = append(out, json.RawMessage(jsonjs.Stringify(a)))
	}
	return transport.Reply[[]json.RawMessage]{Value: out, Valid: true}
}

// AgentGetRaw runs one `agent get <id>` with the call's timeout and returns
// the raw process result: Value carries the stdout (possibly empty) and the
// caller owns the fail-open and body-parsing handling. It never retries and
// does not parse the body.
func (b *backend) AgentGetRaw(ctx context.Context, target transport.Target, call transport.Call) transport.Reply[string] {
	r := runWithTimeout([]string{"agent", "get", target.ID}, b.env, timeoutFor(call), ctxArgs(ctx)...)
	if !ok(r) {
		return transport.Reply[string]{Value: r.Stdout, Process: processResult(r), Err: &transport.Failure{Kind: transport.FailureCommand, Message: r.Stderr}}
	}
	return transport.Reply[string]{Value: r.Stdout, Valid: true, Process: processResult(r)}
}

// PaneCurrentRaw runs `pane current --current` once with the call's timeout
// and returns the raw process result: Value carries the stdout and the
// caller owns the exit-status handling. It never retries and does not parse
// the body.
func (b *backend) PaneCurrentRaw(ctx context.Context, call transport.Call) transport.Reply[string] {
	r := runWithTimeout([]string{"pane", "current", "--current"}, b.env, timeoutFor(call), ctxArgs(ctx)...)
	if !ok(r) {
		return transport.Reply[string]{Value: r.Stdout, Process: processResult(r), Err: &transport.Failure{Kind: transport.FailureCommand, Message: r.Stderr}}
	}
	return transport.Reply[string]{Value: r.Stdout, Valid: true, Process: processResult(r)}
}

// PaneGetRaw runs one `pane get <id>` with the call's timeout and returns
// the raw process result: Value carries the stdout (possibly empty) and the
// caller owns the exit-status and body-parsing handling. It never retries
// and does not parse the body.
func (b *backend) PaneGetRaw(ctx context.Context, target transport.Target, call transport.Call) transport.Reply[string] {
	r := runWithTimeout([]string{"pane", "get", target.ID}, b.env, timeoutFor(call), ctxArgs(ctx)...)
	if !ok(r) {
		return transport.Reply[string]{Value: r.Stdout, Process: processResult(r), Err: &transport.Failure{Kind: transport.FailureCommand, Message: r.Stderr}}
	}
	return transport.Reply[string]{Value: r.Stdout, Valid: true, Process: processResult(r)}
}

// ClaudeTranscriptPath resolves the target's Claude Code session transcript
// path: the adapter's one agent get (its fixed timeout) plus the local file
// derivation; "" when any data is missing.
func (b *backend) ClaudeTranscriptPath(ctx context.Context, target transport.Target) transport.Reply[string] {
	return transport.Reply[string]{Value: ClaudeTranscriptPath(target.ID, b.env, ctxArgs(ctx)...), Valid: true}
}

// PiSessionPath resolves the target's pi session file path: the adapter's
// one agent get (its fixed timeout) plus the local file derivation; "" when
// any data is missing. The adapter does not take a per-call context, so ctx
// is not forwarded.
func (b *backend) PiSessionPath(ctx context.Context, target transport.Target) transport.Reply[string] {
	return transport.Reply[string]{Value: PiSessionPath(target.ID, b.env), Valid: true}
}

// CountClaudeCompactBoundaries counts the compact-boundary lines of a
// Claude Code session transcript file — a local read with no herdr call —
// bounded by the per-call context; Err is set when the file cannot be read.
func (b *backend) CountClaudeCompactBoundaries(ctx context.Context, path string) transport.Reply[int] {
	n, ok := CountClaudeCompactBoundaries(path, ctxArgs(ctx)...)
	if !ok {
		return transport.Reply[int]{Err: &transport.Failure{Kind: transport.FailureCommand, Message: "the transcript is unreadable"}}
	}
	return transport.Reply[int]{Value: n, Valid: true}
}

// CountClaudeUserMarkerLines counts the lines of a Claude Code session
// transcript file that carry the user record marker and the given peer
// marker — a local read with no herdr call; Err is set when the file cannot
// be read.
func (b *backend) CountClaudeUserMarkerLines(ctx context.Context, path, marker string) transport.Reply[int] {
	n, ok := CountClaudeUserMarkerLines(path, marker)
	if !ok {
		return transport.Reply[int]{Err: &transport.Failure{Kind: transport.FailureCommand, Message: "the transcript is unreadable"}}
	}
	return transport.Reply[int]{Value: n, Valid: true}
}

// CountPiUserMarkerLines counts the lines of a pi session file that carry
// the given peer marker in a message entry — a local read with no herdr
// call; Err is set when the file cannot be read.
func (b *backend) CountPiUserMarkerLines(ctx context.Context, path, marker string) transport.Reply[int] {
	n, ok := CountPiUserMarkerLines(path, marker)
	if !ok {
		return transport.Reply[int]{Err: &transport.Failure{Kind: transport.FailureCommand, Message: "the transcript is unreadable"}}
	}
	return transport.Reply[int]{Value: n, Valid: true}
}

// timeoutMsUp is the subprocess deadline in whole milliseconds with the job
// semantics: a positive duration is rounded up (never truncated) and a zero
// or negative one stays 0, which the platform reads as "no deadline". It is
// the same conversion the job's runStep applies to its subprocess timeouts.
func timeoutMsUp(d time.Duration) int {
	if d <= 0 {
		return 0
	}
	ms := d / time.Millisecond
	if d%time.Millisecond != 0 {
		ms++
	}
	return int(ms)
}

// jobStep is the raw one-step run of the job's Herdr adapter: one herdr CLI
// call with exactly the given argv under the call's timeout rounded up to
// whole milliseconds, never retried and never parsed. It returns the raw
// stdout plus the raw process result and reports Err exactly when the job's
// one-step run fails: a not-found binary, a timeout, a nil status or a
// non-zero status. An explicit zero timeout keeps "no deadline", the job's
// reading of a zero millisecond bound.
func (b *backend) jobStep(args []string, call transport.Call, ctx context.Context) transport.Reply[string] {
	r := platform.RunCli("herdr", args, platform.RunOptions{Env: b.env, Context: ctx, TimeoutMs: timeoutMsUp(call.Timeout)})
	if r.NotFound || r.TimedOut || r.Status == nil || *r.Status != 0 {
		return transport.Reply[string]{Value: r.Stdout, Process: processResult(r), Err: &transport.Failure{Kind: transport.FailureCommand, Message: r.Stderr}}
	}
	return transport.Reply[string]{Value: r.Stdout, Valid: true, Process: processResult(r)}
}

// WorkspaceCreateRaw runs one `workspace create --cwd <cwd> --label <label>
// [--env KEY=VALUE ...] --no-focus` with the --env pairs sorted by key and
// returns the raw process result (Value carries the stdout); the caller
// keeps its own exit-status and body-parsing handling. It never retries and
// does not parse the body; see jobStep for the timeout and failure contract.
func (b *backend) WorkspaceCreateRaw(ctx context.Context, cwd, label string, env map[string]string, call transport.Call) transport.Reply[string] {
	args := []string{"workspace", "create", "--cwd", cwd, "--label", label}
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		args = append(args, "--env", key+"="+env[key])
	}
	args = append(args, "--no-focus")
	return b.jobStep(args, call, ctx)
}

// WorkspaceCloseRaw runs one `workspace close <id>` and returns the raw
// process result (Value carries the stdout); the caller keeps its own
// exit-status handling. It never retries and does not parse the body; see
// jobStep for the timeout and failure contract.
func (b *backend) WorkspaceCloseRaw(ctx context.Context, id string, call transport.Call) transport.Reply[string] {
	return b.jobStep([]string{"workspace", "close", id}, call, ctx)
}

// WorkspaceListRaw runs one `workspace list` and returns the raw process
// result (Value carries the stdout); the caller keeps its own exit-status
// and body-parsing handling. It never retries and does not parse the body;
// see jobStep for the timeout and failure contract.
func (b *backend) WorkspaceListRaw(ctx context.Context, call transport.Call) transport.Reply[string] {
	return b.jobStep([]string{"workspace", "list"}, call, ctx)
}

// PaneListRaw runs one `pane list --workspace <id>` and returns the raw
// process result (Value carries the stdout); the caller keeps its own
// exit-status and body-parsing handling. It never retries and does not parse
// the body; see jobStep for the timeout and failure contract.
func (b *backend) PaneListRaw(ctx context.Context, workspaceID string, call transport.Call) transport.Reply[string] {
	return b.jobStep([]string{"pane", "list", "--workspace", workspaceID}, call, ctx)
}

// PaneRunRaw runs one `pane run <pane> <argv...>` and returns the raw
// process result (Value carries the stdout); the caller keeps its own
// exit-status handling. It never retries and does not parse the body; see
// jobStep for the timeout and failure contract.
func (b *backend) PaneRunRaw(ctx context.Context, paneID string, argv []string, call transport.Call) transport.Reply[string] {
	args := make([]string, 0, len(argv)+3)
	args = append(args, "pane", "run", paneID)
	args = append(args, argv...)
	return b.jobStep(args, call, ctx)
}

// AgentGetMachineRaw runs one `agent get <id>` with the machine prefix the
// target's machine names and the call's timeout, and returns the raw process
// result (Value carries the stdout, possibly empty): the caller keeps its
// own fail-open and body-parsing handling. It never retries and does not
// parse the body.
func (b *backend) AgentGetMachineRaw(ctx context.Context, target transport.Target, call transport.Call) transport.Reply[string] {
	args := append(append([]string{}, sessionref.HerdrMachineArgs(target.Machine)...), "agent", "get", target.ID)
	return b.rawStep(args, call, ctx)
}

// AgentWaitRaw runs one `agent wait <id> --until idle --until done --timeout
// <timeoutMS>` with the machine prefix the target's machine names, under the
// call's timeout as the process timeout, and returns the raw process result
// (Value carries the stdout): the caller keeps its own TimedOut,
// structured-error and exit-status handling. timeoutMS is the logical wait
// bound rendered into the argv only.
func (b *backend) AgentWaitRaw(ctx context.Context, target transport.Target, timeoutMS int, call transport.Call) transport.Reply[string] {
	args := append(append([]string{}, sessionref.HerdrMachineArgs(target.Machine)...), "agent", "wait", target.ID, "--until", "idle", "--until", "done", "--timeout", strconv.Itoa(timeoutMS))
	return b.rawStep(args, call, ctx)
}

// AgentPromptWaitRaw runs one `agent prompt <id> <body> --wait --until working
// --until blocked --until idle --until done --timeout <timeoutMS>` with the
// machine prefix the target's machine names, the call's timeout as the
// process timeout, and merged output (the raw result's stdout holds the
// merged stream). The caller keeps its own TimedOut, structured-error and
// exit-status handling. timeoutMS is the logical wait bound rendered into
// the argv only.
func (b *backend) AgentPromptWaitRaw(ctx context.Context, target transport.Target, body string, timeoutMS int, call transport.Call) transport.Reply[string] {
	args := append(append([]string{}, sessionref.HerdrMachineArgs(target.Machine)...), "agent", "prompt", target.ID, body, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", strconv.Itoa(timeoutMS))
	r := platform.RunCli("herdr", args, platform.RunOptions{Env: b.env, Context: ctx, TimeoutMs: int(timeoutFor(call) / time.Millisecond), MergeOutput: true})
	return rawReply(r)
}

// AgentPromptRaw runs one bare `agent prompt <id> <body>` (no wait flags)
// with the machine prefix the target's machine names and the call's timeout,
// and returns the raw process result (Value carries the stdout): the caller
// keeps its own exit-status, timeout and structured-error handling.
func (b *backend) AgentPromptRaw(ctx context.Context, target transport.Target, body string, call transport.Call) transport.Reply[string] {
	args := append(append([]string{}, sessionref.HerdrMachineArgs(target.Machine)...), "agent", "prompt", target.ID, body)
	return b.rawStep(args, call, ctx)
}

// AgentReadSourceRaw runs one `agent read <id> --source <source>` with the
// machine prefix the target's machine names, an optional `--lines <n>` when
// lines is non-nil, and the call's timeout, and returns the raw process
// result (Value carries the stdout): the caller keeps its own fallback and
// exit-status handling.
func (b *backend) AgentReadSourceRaw(ctx context.Context, target transport.Target, source string, lines *int, call transport.Call) transport.Reply[string] {
	args := append(append([]string{}, sessionref.HerdrMachineArgs(target.Machine)...), "agent", "read", target.ID, "--source", source)
	if lines != nil {
		args = append(args, "--lines", strconv.Itoa(*lines))
	}
	return b.rawStep(args, call, ctx)
}

// AgentSendKeysRaw runs one `agent send-keys <id> <key>` with the machine
// prefix the target's machine names and the call's timeout, and returns the
// raw process result: Value is true only when the call is not a lookup
// failure and exits 0, and the raw result stays on the reply for the
// caller's own exit-status judgment.
func (b *backend) AgentSendKeysRaw(ctx context.Context, target transport.Target, key string, call transport.Call) transport.Reply[bool] {
	args := append(append([]string{}, sessionref.HerdrMachineArgs(target.Machine)...), "agent", "send-keys", target.ID, key)
	r := runWithTimeout(args, b.env, timeoutFor(call), ctxArgs(ctx)...)
	if !ok(r) {
		return transport.Reply[bool]{Process: processResult(r), Err: &transport.Failure{Kind: transport.FailureCommand, Message: r.Stderr}}
	}
	return transport.Reply[bool]{Value: true, Valid: true, Process: processResult(r)}
}

// SnapshotRaw runs one `api snapshot` with the machine prefix the target's
// machine names and the call's timeout, and returns the raw process result
// (Value carries the stdout): the caller keeps its own exit-status and
// body-parsing handling. The per-call context bounds the run.
func (b *backend) SnapshotRaw(ctx context.Context, target transport.Target, call transport.Call) transport.Reply[string] {
	args := append(append([]string{}, sessionref.HerdrMachineArgs(target.Machine)...), "api", "snapshot")
	return b.rawStep(args, call, ctx)
}

// MachineListRaw runs one `machine list --json` with the call's timeout and
// returns the raw process result (Value carries the stdout): the caller
// keeps its own exit-status and body-parsing handling. The per-call context
// bounds the run.
func (b *backend) MachineListRaw(ctx context.Context, call transport.Call) transport.Reply[string] {
	return b.rawStep([]string{"machine", "list", "--json"}, call, ctx)
}

// rawStep is the raw one-step run of the peer's Herdr steps: one herdr CLI
// call with exactly the given argv under the call's timeout (the backend's
// default when the call names none), the per-call context forwarded, and no
// output merging. It returns the raw stdout plus the raw process result and
// reports Err exactly when the one-step run fails: a not-found binary, a
// timeout, a nil status or a non-zero status. The caller keeps its own
// TimedOut and structured-error handling on the raw result.
func (b *backend) rawStep(args []string, call transport.Call, ctx context.Context) transport.Reply[string] {
	r := runWithTimeout(args, b.env, timeoutFor(call), ctxArgs(ctx)...)
	return rawReply(r)
}

// rawReply maps one raw process outcome into a string reply: Value carries
// the raw stdout (possibly empty) and the raw result stays on the reply for
// the caller's own handling; Err is set exactly when the run is a lookup
// failure, a timeout, a nil status or a non-zero status.
func rawReply(r platform.RunResult) transport.Reply[string] {
	if !ok(r) {
		return transport.Reply[string]{Value: r.Stdout, Process: processResult(r), Err: &transport.Failure{Kind: transport.FailureCommand, Message: r.Stderr}}
	}
	return transport.Reply[string]{Value: r.Stdout, Valid: true, Process: processResult(r)}
}

// Runtime.

// Start starts the requested agent in its pane:
// `agent start <name> --kind <kind> --pane <pane> --timeout <timeout>`, with
// the request's args after a "--" when there are any, under the per-call
// wrapper timeout. The merged output comes back on Value so the caller can
// read the backend's start markers from it.
func (b *backend) Start(ctx context.Context, start transport.Start, call transport.Call) transport.Reply[string] {
	args := []string{"agent", "start", start.Name, "--kind", start.Kind, "--pane", start.PaneID, "--timeout", start.Timeout}
	if len(start.Args) > 0 {
		args = append(args, "--")
		args = append(args, start.Args...)
	}
	r := platform.RunCli("herdr", args, platform.RunOptions{Env: b.env, Context: ctx, TimeoutMs: int(timeoutFor(call) / time.Millisecond), MergeOutput: true})
	if !ok(r) {
		return transport.Reply[string]{Value: r.Stdout + r.Stderr, Process: processResult(r), Err: &transport.Failure{Kind: transport.FailureCommand, Message: r.Stderr}}
	}
	return transport.Reply[string]{Value: r.Stdout + r.Stderr, Valid: true, Process: processResult(r)}
}

// CallerAgentName returns the agent that hosts the caller's claimed pane, with
// the same agent get on that pane the adapter's CallerAgentName runs.
func (b *backend) CallerAgentName(ctx context.Context, call transport.Call) transport.Reply[string] {
	name := CallerAgentName(b.env)
	if name == "" {
		return transport.Reply[string]{Err: &transport.Failure{Kind: transport.FailureNotFound, Message: "the caller pane hosts no readable agent"}}
	}
	return transport.Reply[string]{Value: name, Valid: true}
}

// SendText sends raw text to the pane the target names, without submitting.
func (b *backend) SendText(ctx context.Context, target transport.Target, text string, call transport.Call) transport.Reply[bool] {
	return transport.Reply[bool]{Value: PaneSendText(target.ID, text, b.env, ctxArgs(ctx)...), Valid: true}
}

// GetGone re-queries the pane the target names: true only when the query
// fails with the pane-not-found code.
func (b *backend) GetGone(ctx context.Context, target transport.Target, call transport.Call) transport.Reply[bool] {
	return transport.Reply[bool]{Value: PaneGetGone(target.ID, b.env), Valid: true}
}
