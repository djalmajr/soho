// Package transport owns the neutral contracts every backend implements so
// that soho can later run on a second transport (herdr-core, "hc") without
// changing caller behavior. It holds only the interfaces and the value types
// they pass; it imports no other internal package. The Herdr backend in
// internal/herdr is the only implementation in stage 1: each of its methods
// delegates to the existing adapter function with the same argv, timeout, and
// parsing, so nothing observable changes.
//
// A method with no existing adapter function (a terminal wait, the job
// workspace calls, the diagnostic probes) is deliberately not part of these
// interfaces yet. Operations join the contracts as the slices that move
// their callers land: the stage-1 Transport and LegacyTransport keep their
// method sets stable so existing consumers and test fakes keep compiling,
// and each new capability gets its own interface (Runtime today). A future
// backend may return an *Failure of kind FailureUnsupported before any
// action instead of pretending an absent capability succeeded.
package transport

import (
	"context"
	"encoding/json"
	"time"
)

// Target names a terminal or a pane by the opaque ids a backend reports.
// Machine and ID stay opaque to the caller: the backend encodes its own
// addressing (for example a Herdr machine prefix) and never exposes argv.
type Target struct {
	Machine string
	ID      string
}

// Caller is the identity a process claims from its inherited environment
// (the HERDR_* markers), read without querying a server. Inside is true only
// when the environment says the process runs inside Herdr.
type Caller struct {
	Inside      bool
	PaneID      string
	WorkspaceID string
	TabID       string
}

// Call carries the per-call execution options a caller may set. Stage 1 wires
// only Timeout; the per-call context travels through each method's ctx
// argument. Per-call start callbacks and output merging are added by the
// slices that move their callers, so this slice carries no dead fields.
type Call struct {
	Timeout time.Duration
}

// ProcessResult mirrors the raw outcome of one process run: a nil exit status
// is distinct from zero, a timeout from a signal, raw stdout from stderr, a
// lookup failure, and the resolved executable path.
type ProcessResult struct {
	NotFound bool
	Resolved string
	Status   *int
	Signal   string
	Stdout   string
	Stderr   string
	TimedOut bool
	Error    string
}

// FailureKind classifies a failure without conflating a wire error code with
// a CLI exit status.
type FailureKind string

// The finite failure kinds a backend may report.
const (
	// FailureUnsupported: the backend has no equivalent for the operation.
	FailureUnsupported FailureKind = "unsupported"
	// FailureNotFound: the named target does not exist.
	FailureNotFound FailureKind = "not-found"
	// FailureUnavailable: the backend or server is not reachable.
	FailureUnavailable FailureKind = "unavailable"
	// FailureProtocol: the backend's response was missing or malformed.
	FailureProtocol FailureKind = "protocol"
	// FailureCancelled: the per-call context was cancelled.
	FailureCancelled FailureKind = "cancelled"
	// FailureCommand: the underlying command failed for another reason.
	FailureCommand FailureKind = "command"
	// FailureConflict: the request conflicts with existing state, such as
	// a terminal name the hc core already has live (its name_taken error).
	FailureConflict FailureKind = "conflict"
	// FailureBlocked: the target sits on a prompt only a person can answer
	// (hc send's exit 9); the send was not made.
	FailureBlocked FailureKind = "blocked"
	// FailureBusy: the idle guard found the target not idle (hc send's exit
	// 10); the send was not made.
	FailureBusy FailureKind = "busy"
)

// Failure is a structured transport error. Code carries an observed wire code
// (when there is one); Cause keeps a cancellation or other root error.
type Failure struct {
	Kind    FailureKind
	Code    string
	Message string
	Cause   error
}

// Error renders the failure for an error chain.
func (e *Failure) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return string(e.Kind)
}

// Unwrap exposes the root cause for errors.Is/As.
func (e *Failure) Unwrap() error { return e.Cause }

// Reply is the uniform result of a transport method. Value is the parsed
// result; Valid reports that the observation was present and well-formed (a
// valid empty list is Valid with an empty Value); Process is the raw process
// outcome, zero when the backend does not expose it; Err is set when the
// operation failed.
type Reply[T any] struct {
	Value   T
	Valid   bool
	Process ProcessResult
	Err     *Failure
}

// Terminal is one registered agent (a terminal) as a backend reports it.
// Fields the backend does not report are zero. State is the reported
// agent_status (empty when absent); Sequence is the state_change_seq.
type Terminal struct {
	Target   Target
	Name     string
	PaneID   string
	State    string
	Sequence json.RawMessage
}

// Scope selects which panes a window listing covers: All lists panes across
// every workspace; otherwise WorkspaceID scopes the listing.
type Scope struct {
	WorkspaceID string
	All         bool
}

// Read selects the screen source and, optionally, a line count for a read.
// Pane reads the pane itself rather than a registered agent, so it works
// before (or without) one.
type Read struct {
	Source string
	Lines  *int
	Pane   bool
}

// Send carries a prompt for a terminal. The stage-1 backend always submits
// Text as a prompt and returns the merged output. Queue lifts the idle guard
// where the backend has one (the hc backend omits its --if idle, so the text
// is typed even while the worker works; a blocked worker still refuses, and
// --force is never passed); the herdr backend has no such guard and ignores
// it.
type Send struct {
	Text  string
	Queue bool
}

// Start names an agent-start request. The Herdr backend encodes it as
// `agent start <name> --kind <kind> --pane <pane> --timeout <timeout>`, with
// Args after a "--" when there are any. Timeout is the agent timeout in
// milliseconds, passed through unchanged; the wrapper timeout the caller may
// set is the per-call Call.Timeout.
type Start struct {
	Name    string
	Kind    string
	PaneID  string
	Timeout string
	Args    []string

	// Cwd is the worker's working directory. The herdr backend ignores it
	// (its pane already has a cwd); the hc backend passes it as --cwd when
	// it is non-empty.
	Cwd string
}

// CallerCheck is the outcome of the caller-pane check: whether this process
// actually runs in the pane the caller claims. State is "verified",
// "mismatch", or "unknown".
type CallerCheck struct {
	State    string
	Pane     string
	ShellPID int
	Reason   string // short

	// Session, PaneSession and ShownIn describe a mismatch for the refusal
	// text (DJA-406). Session is this process's Claude Code session
	// (CLAUDE_CODE_SESSION_ID), PaneSession the one herdr reports for the
	// claimed pane, ShownIn the single other pane that shows this process's
	// session. Each is "" when it is not known or not valid.
	Session     string
	PaneSession string
	ShownIn     string
}

// View is one pane or tab as a backend reports it. Raw keeps the backend's
// original object for callers that need fields beyond the projection.
type View struct {
	ID          string
	PaneID      string
	TabID       string
	WorkspaceID string
	Label       string
	RootPane    string
	Revision    int
	Raw         json.RawMessage
}

// TabFields is the parsed identity of a tab (its label and root pane),
// kept whatever the exit status: a failed tab get can still print a tab
// with a root pane, and the caller may want it.
type TabFields struct {
	Label string
	Root  string
}

// Open describes how a window split or tab is created.
type Open struct {
	Anchor      string
	Direction   string
	Cwd         string
	WorkspaceID string
	Label       string
	Ratio       *string
	NoFocus     bool
	Env         map[string]string
}

// Move describes a pane move.
type Move struct {
	Pane       string
	Tab        string
	Split      string
	TargetPane string
	Ratio      string
	Label      string
	NewTab     bool
	NoFocus    bool
}

// Focus selects a focus target: an agent by Target, or a pane by Direction
// and PaneID.
type Focus struct {
	Target    Target
	TabID     string
	PaneID    string
	Direction string
}

// Transport is the reusable runtime surface over a backend's terminals:
// identity, roster, per-terminal input and reads, and close. The stage-1
// Herdr backend is the only implementation. A method is present only when an
// existing adapter function backs it; operations without one (a terminal
// wait) join the contracts in the slice that moves their caller.
type Transport interface {
	// Caller returns the identity claimed from the inherited environment.
	// It is pure and never queries a server.
	Caller() Caller
	// Current resolves the calling pane's actual identity from the backend,
	// distinct from the inherited claim.
	Current(ctx context.Context, call Call) Reply[Caller]
	// CheckCaller verifies the calling process runs in the claimed pane.
	CheckCaller(ctx context.Context, call Call) Reply[CallerCheck]
	// List returns the registered terminals (agents).
	List(ctx context.Context, scope Scope, call Call) Reply[[]Terminal]
	// Get reads one terminal's state.
	Get(ctx context.Context, target Target, call Call) Reply[Terminal]
	// Send submits a prompt to a terminal and returns the merged output.
	// The hc backend pastes and submits through `hc send` and returns the
	// raw SendResult record (its turn and started carry the arrival proof)
	// instead of merged output; a worker that sits on a person's prompt is
	// FailureBlocked and a non-idle worker (without Send.Queue) is
	// FailureBusy.
	Send(ctx context.Context, target Target, send Send, call Call) Reply[string]
	// Keys sends raw keys; pane selects the pane channel, otherwise the
	// agent channel.
	Keys(ctx context.Context, target Target, keys string, pane bool, call Call) Reply[bool]
	// Read returns a terminal's screen, or a pane's screen when Read.Pane.
	Read(ctx context.Context, target Target, read Read, call Call) Reply[string]
	// Close closes a terminal's pane.
	Close(ctx context.Context, target Target, call Call) Reply[bool]
}

// Window is a backend's presentation/workspace capability: panes, tabs,
// layout, focus, titles, and notifications. It is optional; a backend without
// a window reports a nil Window rather than coupling the runtime to one.
type Window interface {
	// Layout returns the raw layout for the current pane, or for pane.
	Layout(ctx context.Context, pane string, call Call) Reply[json.RawMessage]
	// ListPanes lists panes, scoped as Scope selects.
	ListPanes(ctx context.Context, scope Scope, call Call) Reply[[]View]
	// ListTabs lists the tabs of a workspace.
	ListTabs(ctx context.Context, workspace string, call Call) Reply[[]View]
	// Tab reads one tab's label and root pane.
	Tab(ctx context.Context, tab string, call Call) Reply[View]
	// CreateTab creates a tab and reports it.
	CreateTab(ctx context.Context, open Open, call Call) Reply[View]
	// RenameTab renames a tab.
	RenameTab(ctx context.Context, tab, label string, call Call) Reply[bool]
	// Split splits a pane and reports the new pane.
	Split(ctx context.Context, open Open, call Call) Reply[View]
	// Move moves a pane and reports where it landed.
	Move(ctx context.Context, move Move, call Call) Reply[View]
	// Focus focuses an agent, or a pane by direction.
	Focus(ctx context.Context, focus Focus, call Call) Reply[bool]
	// Title sets or clears (nil) a pane's title.
	Title(ctx context.Context, pane string, title *string, call Call) Reply[bool]
	// Notify shows a system notification. The stage-1 backend plays the
	// fixed "done" sound; an explicit sound parameter is a later slice.
	Notify(ctx context.Context, title, body string, call Call) Reply[bool]
}

// LegacyTransport holds operations with no equivalent in a second backend.
// Stage 1 exposes the process-info read, the exact rename contract, the raw
// agent existence check, the raw one-attempt knowledge probe, the raw agent
// read with caller-owned exit handling, the raw agent list with the
// projection-free objects, the raw one-attempt agent get with caller-owned
// fail-open handling, the raw pane current and pane get reads with
// caller-owned exit and body handling, the local session-transcript path
// resolutions (one agent get plus a local file), the local transcript
// compact-boundary and user-marker counts, the raw job workspace and pane
// steps with the job's round-up timeout and caller-owned parsing, and the
// raw peer agent steps (the machine-prefixed agent get, the agent wait, the
// waiting and bare agent prompts, the agent read with a source and an
// optional line count, the agent send-keys) plus the raw session snapshot
// and machine list with the caller's context, timeout and parsing, the raw
// doctor version, status-server and skill reads (no deadline when the call
// names none), and the raw herdr plugin-management steps (the plugin list,
// the ref-bounded install, the config-dir, the verb-bounded remove, and the
// server config reload) with the caller's bounded timeout and parsing.
type LegacyTransport interface {
	// ProcessInfo returns the raw process info a backend reports for a pane.
	ProcessInfo(ctx context.Context, target Target, call Call) Reply[json.RawMessage]
	// Rename renames the agent the backend resolves for the target's id with
	// its exact rename call, and reports whether it succeeded. The raw
	// process result stays on the reply for callers that parse its error
	// output.
	Rename(ctx context.Context, target Target, name string, call Call) Reply[bool]
	// AgentGetOK is the raw one-attempt existence check: one agent get on
	// the target's id, Value true only when the call is not a lookup
	// failure and exits 0. It never retries and does not parse the body,
	// so an exit-0 answer without an agent_status still reports true.
	AgentGetOK(ctx context.Context, target Target, call Call) Reply[bool]
	// AgentGetKnown is the raw one-attempt knowledge check for a target on
	// its own machine (the machine prefix the target names): one agent get,
	// Value true only when the call exits 0 and its body carries a
	// non-empty string agent_status. A timeout, a lookup failure and every
	// failed call report false with the cause; an exit-0 body without a
	// usable agent_status is a well-formed observation of false, not an
	// error.
	AgentGetKnown(ctx context.Context, target Target, call Call) Reply[bool]
	// AgentReadRaw runs the exact `agent read <id> --source <source>
	// --lines <lines>` call once and returns the raw process result (Value
	// carries the stdout) so the caller keeps its own fallback output and
	// exit-code handling.
	AgentReadRaw(ctx context.Context, target Target, source, lines string, call Call) Reply[string]
	// AgentListRaw runs `agent list` once with the call's timeout and returns
	// the raw agent objects the adapter parses (one JSON value per agent),
	// so callers that read fields beyond the terminal projection keep them.
	// The adapter's die-on-failure behavior happens inside the wrapped call,
	// exactly as the caller's recover used to catch it.
	AgentListRaw(ctx context.Context, call Call) Reply[[]json.RawMessage]
	// AgentGetRaw runs one `agent get <id>` with the call's timeout and
	// returns the raw process result (Value carries the stdout, possibly
	// empty) so the caller keeps its own fail-open and body-parsing
	// handling. It never retries and does not parse the body.
	AgentGetRaw(ctx context.Context, target Target, call Call) Reply[string]
	// TabGetRaw runs one `tab get <tab>` with the call's timeout and
	// returns the parsed tab fields (label and root pane) whatever the exit
	// status — a failed call can still print a tab with a root pane — with
	// the raw process result so the caller keeps its own exit-status
	// judgment. It never retries and, unlike Tab, keeps the parsed fields
	// on the Value alongside Err.
	TabGetRaw(ctx context.Context, tab string, call Call) Reply[TabFields]
	// PaneCurrentRaw runs `pane current --current` once with the call's
	// timeout and returns the raw process result (Value carries the stdout)
	// so the caller keeps its own exit-status handling. It never retries and
	// does not parse the body.
	PaneCurrentRaw(ctx context.Context, call Call) Reply[string]
	// PaneGetRaw runs one `pane get <id>` with the call's timeout and
	// returns the raw process result (Value carries the stdout, possibly
	// empty) so the caller keeps its own exit-status and body-parsing
	// handling. It never retries and does not parse the body.
	PaneGetRaw(ctx context.Context, target Target, call Call) Reply[string]
	// ClaudeTranscriptPath resolves the Claude Code session transcript path
	// the target's agent get reports (one agent get, the backend's fixed
	// timeout, the backend's path derivation), Value "" when any data is
	// missing.
	ClaudeTranscriptPath(ctx context.Context, target Target) Reply[string]
	// PiSessionPath resolves the pi session file path the target's agent
	// get reports (one agent get, the backend's fixed timeout, the
	// backend's path derivation), Value "" when any data is missing. The
	// backend's call takes no per-call context, so ctx is not forwarded.
	PiSessionPath(ctx context.Context, target Target) Reply[string]
	// WorkspaceCreateRaw runs one `workspace create --cwd <cwd> --label
	// <label> [--env KEY=VALUE ...] --no-focus` with the --env pairs sorted
	// by key, the call's timeout rounded up to whole milliseconds (an
	// explicit zero keeps "no deadline"), and returns the raw process result
	// (Value carries the stdout) so the caller keeps its own exit-status and
	// body-parsing handling. It never retries, does not parse the body, and
	// fails exactly when the job's one-step run fails: a not-found binary,
	// a timeout, a nil status or a non-zero status.
	WorkspaceCreateRaw(ctx context.Context, cwd, label string, env map[string]string, call Call) Reply[string]
	// WorkspaceCloseRaw runs one `workspace close <id>` with the call's
	// timeout rounded up to whole milliseconds (an explicit zero keeps "no
	// deadline") and returns the raw process result (Value carries the
	// stdout) so the caller keeps its own exit-status handling. It never
	// retries, does not parse the body, and fails exactly when the job's
	// one-step run fails: a not-found binary, a timeout, a nil status or a
	// non-zero status.
	WorkspaceCloseRaw(ctx context.Context, id string, call Call) Reply[string]
	// WorkspaceListRaw runs one `workspace list` with the call's timeout
	// rounded up to whole milliseconds (an explicit zero keeps "no
	// deadline") and returns the raw process result (Value carries the
	// stdout) so the caller keeps its own exit-status and body-parsing
	// handling. It never retries, does not parse the body, and fails exactly
	// when the job's one-step run fails: a not-found binary, a timeout, a
	// nil status or a non-zero status.
	WorkspaceListRaw(ctx context.Context, call Call) Reply[string]
	// PaneListRaw runs one `pane list --workspace <id>` with the call's
	// timeout rounded up to whole milliseconds (an explicit zero keeps "no
	// deadline") and returns the raw process result (Value carries the
	// stdout) so the caller keeps its own exit-status and body-parsing
	// handling. It never retries, does not parse the body, and fails exactly
	// when the job's one-step run fails: a not-found binary, a timeout, a
	// nil status or a non-zero status.
	PaneListRaw(ctx context.Context, workspaceID string, call Call) Reply[string]
	// PaneRunRaw runs one `pane run <pane> <argv...>` with the call's
	// timeout rounded up to whole milliseconds (an explicit zero keeps "no
	// deadline") and returns the raw process result (Value carries the
	// stdout) so the caller keeps its own exit-status handling. It never
	// retries, does not parse the body, and fails exactly when the job's
	// one-step run fails: a not-found binary, a timeout, a nil status or a
	// non-zero status.
	PaneRunRaw(ctx context.Context, paneID string, argv []string, call Call) Reply[string]
	// AgentGetMachineRaw runs one `agent get <id>` with the machine prefix
	// the target's machine names (no prefix when the machine is empty or
	// local) and the call's timeout, and returns the raw process result
	// (Value carries the stdout, possibly empty) so the caller keeps its own
	// fail-open, timeout and body-parsing handling. It never retries and
	// does not parse the body.
	AgentGetMachineRaw(ctx context.Context, target Target, call Call) Reply[string]
	// AgentWaitRaw runs one `agent wait <id> --until idle --until done
	// --timeout <timeoutMS>` with the machine prefix the target's machine
	// names and the call's timeout as the process timeout, and returns the
	// raw process result (Value carries the stdout) so the caller keeps its
	// own TimedOut, structured-error and exit-status handling. timeoutMS is
	// the logical wait bound rendered into the argv; the process timeout is
	// the call's, which the caller sums. It never retries and does not parse
	// the body.
	AgentWaitRaw(ctx context.Context, target Target, timeoutMS int, call Call) Reply[string]
	// AgentPromptWaitRaw runs one `agent prompt <id> <body> --wait --until
	// working --until blocked --until idle --until done --timeout
	// <timeoutMS>` with the machine prefix the target's machine names, the
	// call's timeout as the process timeout, and merged output (the raw
	// result's stdout holds the merged stream), and returns the raw process
	// result (Value carries the stdout) so the caller keeps its own TimedOut,
	// structured-error and exit-status handling. timeoutMS is the logical
	// wait bound rendered into the argv; the process timeout is the call's,
	// which the caller sums. It never retries and does not parse the body.
	AgentPromptWaitRaw(ctx context.Context, target Target, body string, timeoutMS int, call Call) Reply[string]
	// AgentPromptRaw runs one bare `agent prompt <id> <body>` (no wait
	// flags) with the machine prefix the target's machine names and the
	// call's timeout, and returns the raw process result (Value carries the
	// stdout) so the caller keeps its own exit-status, timeout and
	// structured-error handling. It never retries and does not parse the
	// body.
	AgentPromptRaw(ctx context.Context, target Target, body string, call Call) Reply[string]
	// AgentReadSourceRaw runs one `agent read <id> --source <source>` with
	// the machine prefix the target's machine names, an optional `--lines
	// <n>` when lines is non-nil, and the call's timeout, and returns the
	// raw process result (Value carries the stdout) so the caller keeps its
	// own fallback and exit-status handling. It never retries and does not
	// parse the body.
	AgentReadSourceRaw(ctx context.Context, target Target, source string, lines *int, call Call) Reply[string]
	// AgentSendKeysRaw runs one `agent send-keys <id> <key>` with the machine
	// prefix the target's machine names and the call's timeout, and returns
	// the raw process result: Value is true only when the call is not a
	// lookup failure and exits 0, and the raw result stays on the reply for
	// the caller's own exit-status judgment. It never retries and does not
	// parse the output.
	AgentSendKeysRaw(ctx context.Context, target Target, key string, call Call) Reply[bool]
	// SnapshotRaw runs one `api snapshot` with the machine prefix the
	// target's machine names and the call's timeout, and returns the raw
	// process result (Value carries the stdout) so the caller keeps its own
	// exit-status and body-parsing handling. The per-call context bounds the
	// run. It never retries and does not parse the body.
	SnapshotRaw(ctx context.Context, target Target, call Call) Reply[string]
	// MachineListRaw runs one `machine list --json` with the call's timeout
	// and returns the raw process result (Value carries the stdout) so the
	// caller keeps its own exit-status and body-parsing handling. The
	// per-call context bounds the run. It never retries and does not parse
	// the body.
	MachineListRaw(ctx context.Context, call Call) Reply[string]
	// VersionRaw runs one `--version` with no deadline (the doctor's legacy
	// contract: a zero or negative call timeout keeps the unbounded run) and
	// returns the raw process result (Value carries the stdout) so the
	// caller keeps its own exit-status and output handling. It never
	// retries and does not parse the body.
	VersionRaw(ctx context.Context, call Call) Reply[string]
	// StatusServerRaw runs one `status server` with no deadline (a zero or
	// negative call timeout keeps the unbounded run, the doctor's legacy
	// contract) and returns the raw process result (Value carries the
	// stdout) so the caller keeps its own exit-status and version parsing.
	// It never retries and does not parse the body.
	StatusServerRaw(ctx context.Context, call Call) Reply[string]
	// SkillRaw runs one `--skill` with no deadline (a zero or negative call
	// timeout keeps the unbounded run, the doctor's legacy contract) and
	// returns the raw process result (Value carries the stdout) so the
	// caller keeps its own exit-status and file comparison. It never
	// retries and does not parse the body.
	SkillRaw(ctx context.Context, call Call) Reply[string]
	// PluginListRaw runs one `plugin list --json` with the call's timeout
	// (a zero or negative one keeps no deadline) and returns the raw process
	// result (Value carries the stdout) so the caller keeps its own
	// exit-status and list parsing. It never retries and does not parse the
	// body.
	PluginListRaw(ctx context.Context, call Call) Reply[string]
	// PluginInstallRefRaw runs one `plugin install djalmajr/soho/plugin
	// --ref <ref> --yes` with the call's timeout (a zero or negative one
	// keeps no deadline) and returns the raw process result (Value carries
	// the stdout) so the caller keeps its own exit-status handling. It
	// never retries and does not parse the body.
	PluginInstallRefRaw(ctx context.Context, ref string, call Call) Reply[string]
	// PluginConfigDirRaw runs one `plugin config-dir <pluginID>` with the
	// call's timeout (a zero or negative one keeps no deadline) and returns
	// the raw process result (Value carries the stdout) so the caller keeps
	// its own exit-status and dir parsing. It never retries and does not
	// parse the body.
	PluginConfigDirRaw(ctx context.Context, pluginID string, call Call) Reply[string]
	// PluginRemoveRaw runs one `plugin <verb> <pluginID>` (verb is the
	// caller's uninstall/unlink choice) with the call's timeout (a zero or
	// negative one keeps no deadline) and returns the raw process result
	// (Value carries the stdout) so the caller keeps its own exit-status
	// handling. It never retries and does not parse the body.
	PluginRemoveRaw(ctx context.Context, verb, pluginID string, call Call) Reply[string]
	// ServerReloadConfigRaw runs one `server reload-config` with the call's
	// timeout (a zero or negative one keeps no deadline) and returns the raw
	// process result (Value carries the stdout) so the caller keeps its own
	// exit-status handling. It never retries and does not parse the body.
	ServerReloadConfigRaw(ctx context.Context, call Call) Reply[string]
}

// Runtime is the second runtime capability: the operations whose caller
// slices are landing, kept apart from Transport so the stage-1 method set
// (and the consumers and test fakes pinned to it) stay stable. The Herdr
// backend implements it; a second backend may report an unsupported failure
// before acting.
type Runtime interface {
	// Start starts an agent of the request's kind in its pane with its
	// timeout and optional args, and returns the merged output the backend
	// captured, so the caller can read the backend's start markers from it.
	// The herdr backend's Value is that merged output; the hc backend
	// returns the new terminal's address (t7) on success, and also carries
	// it on the readiness failures (blocked, exited, timeout) once the
	// terminal exists, so the caller can close the terminal the start left.
	Start(ctx context.Context, start Start, call Call) Reply[string]
	// CallerAgentName returns the agent that hosts the caller's claimed
	// pane, read with one agent get on that pane; a pane without a readable
	// agent is a failure with an empty value.
	CallerAgentName(ctx context.Context, call Call) Reply[string]
	// SendText sends raw text to the pane the target names, without
	// submitting, and reports whether the send succeeded.
	SendText(ctx context.Context, target Target, text string, call Call) Reply[bool]
	// GetGone reports whether the pane the target names is already gone: the
	// pane get fails with the pane-not-found code. A present pane, or a
	// failure for another reason, reports false.
	GetGone(ctx context.Context, target Target, call Call) Reply[bool]
}

// Services is the set of capabilities a backend provides, built once per
// invocation. Window, Legacy or Terminal may be nil when a backend lacks
// them. There is no package singleton and no global active backend: a
// caller holds the Services it was given for this invocation.
type Services struct {
	Runtime  Transport
	Window   Window
	Legacy   LegacyTransport
	Terminal Runtime
}
