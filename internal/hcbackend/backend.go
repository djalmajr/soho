// Package hcbackend is the herdr-core (hc) backend for the transport
// contracts (internal/transport): the second implementation alongside the
// Herdr backend (internal/herdr), for transport=hc (DJA-604 stage 2b).
// internal/backend selects it when the resolved transport mode is hc.
//
// Every hc call goes through the per-instance runner of the internal/hc
// client the constructor builds (default: platform.RunCli for "hc"); there
// is no package-global seam. Each launch is clamped to the herdr rule's
// timeout: the call's timeout when it names one, else 30s. Methods this
// slice does not implement return a *transport.Failure of kind
// FailureUnsupported naming the method and "with transport=hc". The Window
// surface is no-ops (hc has no panes, tabs, layout or focus, and herdr-app
// owns them), except Split and CreateTab, which must produce a pane and so
// fail as unsupported.
package hcbackend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/djalmajr/soho/internal/hc"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/transport"
)

// SessionLabel is the terminal label that carries the soho session id the
// backend scopes its roster by.
const SessionLabel = "soho.session"

// defaultRecentLines is the history line count a recent-unwrapped read asks
// for when the request names none: the count every current
// recent-unwrapped caller passes.
const defaultRecentLines = 40

// hcTimeoutDefault is the herdr rule's default (herdr.Timeout) for an hc
// launch that carries no call timeout of its own.
const hcTimeoutDefault = 30 * time.Second

// ErrNotInHcTerminal is returned by Session (and the backend's session
// resolution) when HC_TERMINAL_ID is not set: the process is not inside an
// hc terminal.
var ErrNotInHcTerminal = errors.New("not inside an hc terminal: HC_TERMINAL_ID is not set")

// New builds the hc backend's Services for the environment the process was
// given. It never runs hc itself: the first launch happens on the first
// method that needs one (the session read among them).
func New(env platform.Env) transport.Services {
	b := &backend{env: env, client: hc.New(env)}
	return transport.Services{Runtime: b, Window: b, Legacy: b, Terminal: b}
}

// backend implements all four transport interfaces. Window methods are
// no-ops (or unsupported where they must produce a pane); the Legacy and the
// remaining Transport/Runtime methods this slice does not implement refuse
// as unsupported.
type backend struct {
	env    platform.Env
	client *hc.Client

	sessionOnce  sync.Once
	sessionValue string
	sessionErr   error
}

// Session.

// Session resolves the soho session id the caller's own terminal carries:
// the soho.session label of `hc get $HC_TERMINAL_ID --json`, or
// "hc-<address>" when the label is absent. Outside an hc terminal it is an
// error.
func Session(env platform.Env) (string, error) {
	return sessionOf(hc.New(env), env)
}

// session resolves the session through the instance's runner, computed
// lazily and at most once per Services: the value or the error is memoized,
// so no later call issues the session read again.
func (b *backend) session() (string, error) {
	b.sessionOnce.Do(func() {
		b.sessionValue, b.sessionErr = sessionOf(b.client, b.env)
	})
	return b.sessionValue, b.sessionErr
}

func sessionOf(c *hc.Client, env platform.Env) (string, error) {
	addr := env.Get("HC_TERMINAL_ID")
	if addr == "" {
		return "", ErrNotInHcTerminal
	}
	ctx, cancel := context.WithTimeout(context.Background(), hcTimeoutDefault)
	defer cancel()
	out, err := c.Run(ctx, "get", addr)
	if err != nil {
		return "", err
	}
	return sessionFromGet(out)
}

// sessionFromGet extracts the session from one decoded hc get record: the
// soho.session label when it names one, else "hc-<address>".
func sessionFromGet(stdout string) (string, error) {
	obj, ok := terminalObject(stdout)
	if !ok {
		return "", errors.New("hc get returned no terminal record")
	}
	if labels, ok := obj.Get("labels"); ok {
		if labelsObj, ok := labels.(*jsonjs.Object); ok {
			if v, ok := labelsObj.Get(SessionLabel); ok {
				if label, isString := v.(string); isString && label != "" {
					return label, nil
				}
			}
		}
	}
	if v, ok := obj.Get("address"); ok {
		if address, isString := v.(string); isString && address != "" {
			return "hc-" + address, nil
		}
	}
	return "", errors.New("hc get returned no soho.session label and no address for the session fallback")
}

// Shared helpers.

// run applies the herdr rule's timeout to one hc launch: the call's
// timeout when it names one, else 30s, nested as the context deadline the
// runner inherits — the internal/hc Launch seam carries no timeout
// argument, so the deadline is the clamp the launch runs under.
func (b *backend) run(ctx context.Context, call transport.Call, args ...string) (string, error) {
	timeout := hcTimeoutDefault
	if call.Timeout > 0 {
		timeout = call.Timeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return b.client.Run(ctx, args...)
}

// addr renders one target for an hc call: the id, or the machine-prefixed
// address hc documents for a remote terminal (machine/t7).
func addr(target transport.Target) string {
	if target.Machine != "" {
		return target.Machine + "/" + target.ID
	}
	return target.ID
}

// failure maps one failed hc launch onto a transport failure: exit 3 is the
// named target not existing, a missing binary and every other failure are
// the backend being unreachable; the cause is the hc error's own text.
func failure(err error) *transport.Failure {
	var hcErr *hc.Error
	if errors.As(err, &hcErr) {
		if hcErr.Exit == hc.ExitNotFound {
			return &transport.Failure{Kind: transport.FailureNotFound, Message: hcErr.Error(), Cause: err}
		}
		return &transport.Failure{Kind: transport.FailureUnavailable, Message: hcErr.Error(), Cause: err}
	}
	return &transport.Failure{Kind: transport.FailureUnavailable, Message: err.Error(), Cause: err}
}

// notFound reports whether the failed launch was hc's not-found exit (3).
func notFound(err error) bool {
	var hcErr *hc.Error
	return errors.As(err, &hcErr) && hcErr.Exit == hc.ExitNotFound
}

// unsupported names one method that has no hc equivalent this slice
// implements, the way the contracts expect a second backend to refuse.
func unsupported(method string) *transport.Failure {
	return &transport.Failure{Kind: transport.FailureUnsupported, Message: method + " is not supported with transport=hc"}
}

// terminalObject decodes one hc get stdout as the terminal record; the bool
// is false when it is not a JSON object.
func terminalObject(stdout string) (*jsonjs.Object, bool) {
	v, err := jsonjs.Parse([]byte(stdout))
	if err != nil {
		return nil, false
	}
	obj, ok := v.(*jsonjs.Object)
	return obj, ok
}

// stateOf maps one terminal record onto soho's state words: a finished
// terminal is gone, a live one carries its 7501 word (idle, working,
// blocked, done, error — passed through as reported, never inferred), and a
// live one with no status is unknown.
func stateOf(obj *jsonjs.Object) string {
	if v, ok := obj.Get("state"); ok {
		if state, isString := v.(string); isString && state == "finished" {
			return "gone"
		}
	}
	raw, ok := obj.Get("status")
	if !ok || raw == nil {
		return "unknown"
	}
	status, ok := raw.(*jsonjs.Object)
	if !ok {
		return "unknown"
	}
	if v, ok := status.Get("state"); ok {
		if word, isString := v.(string); isString && word != "" {
			return word
		}
	}
	return "unknown"
}

// turnRaw returns a terminal record's turn counter exactly as hc wrote it:
// an unsigned 64-bit integer literal (the schema's uint64), read from the
// raw JSON and never passed through float64, so every value keeps its
// identity. An absent or malformed turn is no sequence.
func turnRaw(record json.RawMessage) json.RawMessage {
	var rec struct {
		Turn json.RawMessage `json:"turn"`
	}
	if json.Unmarshal(record, &rec) != nil {
		return nil
	}
	text := string(rec.Turn)
	if text == "" {
		return nil
	}
	if _, err := strconv.ParseUint(text, 10, 64); err != nil {
		return nil
	}
	return json.RawMessage(text)
}

// listRecords returns the raw JSON of each terminal in an `hc ls` answer,
// in order, for the fields read byte for byte (turnRaw).
func listRecords(stdout string) []json.RawMessage {
	var list struct {
		Terminals []json.RawMessage `json:"terminals"`
	}
	if json.Unmarshal([]byte(stdout), &list) != nil {
		return nil
	}
	return list.Terminals
}

// listTurn is the turn of the i-th record of an `hc ls` answer, or no
// sequence when the raw list does not line up.
func listTurn(records []json.RawMessage, i int) json.RawMessage {
	if i < 0 || i >= len(records) {
		return nil
	}
	return turnRaw(records[i])
}

// nameOf reads the record's name ("" when the terminal is unnamed).
func nameOf(obj *jsonjs.Object) string {
	if v, ok := obj.Get("name"); ok {
		if name, isString := v.(string); isString {
			return name
		}
	}
	return ""
}

// addressOf reads the record's address ("" when absent).
func addressOf(obj *jsonjs.Object) string {
	if v, ok := obj.Get("address"); ok {
		if address, isString := v.(string); isString {
			return address
		}
	}
	return ""
}

// processRecord keeps one successful hc read's raw record on the reply's
// Process (the exit zero and the record's stdout), so a later slice can read
// how, complete and why out of it.
func processRecord(stdout string) transport.ProcessResult {
	zero := 0
	return transport.ProcessResult{Status: &zero, Stdout: stdout}
}

// readLines maps one read request onto hc's --lines count: visible is the
// screen alone, recent-unwrapped is the history above it with the request's
// Lines (or defaultRecentLines when the request names none). The bool is
// false for a source with no hc equivalent.
func readLines(read transport.Read) (int, bool) {
	switch read.Source {
	case "visible":
		return 0, true
	case "recent-unwrapped":
		if read.Lines != nil {
			return *read.Lines, true
		}
		return defaultRecentLines, true
	default:
		return 0, false
	}
}

// keysHC maps soho's key names onto hc's key names (hc's are lowercase:
// "enter", "ctrl+c", …). Every name soho's commands send today is listed; a
// name not in the map is unsupported, never a guess.
var keysHC = map[string]string{
	"Enter":     "enter",
	"enter":     "enter",
	"esc":       "esc",
	"Esc":       "esc",
	"tab":       "tab",
	"Tab":       "tab",
	"up":        "up",
	"Up":        "up",
	"down":      "down",
	"Down":      "down",
	"ctrl+c":    "ctrl+c",
	"ctrl+u":    "ctrl+u",
	"shift+tab": "shift+tab",
}

// Transport.

// Caller returns the identity claimed from the inherited environment:
// inside when HC_TERMINAL_ID is set, the terminal's address as the pane id,
// the resolved soho session as the workspace id, and no tab. The session
// resolution may issue the instance's one hc get on the first read (it is
// memoized per Services); the environment fields themselves are pure.
func (b *backend) Caller() transport.Caller {
	pane := b.env.Get("HC_TERMINAL_ID")
	session, _ := b.session()
	return transport.Caller{Inside: pane != "", PaneID: pane, WorkspaceID: session, TabID: ""}
}

// Current is not supported with transport=hc: resolving the caller's
// identity beyond the environment needs a backend herdr-core does not offer
// yet.
func (b *backend) Current(ctx context.Context, call transport.Call) transport.Reply[transport.Caller] {
	return transport.Reply[transport.Caller]{Err: unsupported("Current")}
}

// CheckCaller is not supported with transport=hc: the caller-pane
// verification the Herdr backend runs has no hc equivalent this slice
// implements.
func (b *backend) CheckCaller(ctx context.Context, call transport.Call) transport.Reply[transport.CallerCheck] {
	return transport.Reply[transport.CallerCheck]{Err: unsupported("CheckCaller")}
}

// List returns the terminals of the caller's soho session: `hc ls` filtered
// on the soho.session label, which is mandatory — unfiltered, `hc ls` lists
// the user's other terminals too. The request's scope is not honored: the
// session label is the roster scope, and finished terminals are not part of
// the live roster.
func (b *backend) List(ctx context.Context, scope transport.Scope, call transport.Call) transport.Reply[[]transport.Terminal] {
	session, err := b.session()
	if err != nil {
		return transport.Reply[[]transport.Terminal]{Err: &transport.Failure{Kind: transport.FailureUnavailable, Message: "cannot resolve the soho.session label: " + err.Error(), Cause: err}}
	}
	out, err := b.run(ctx, call, "ls", "--label", SessionLabel+"="+session)
	if err != nil {
		// A roster query names no target, so even an exit 3 is the backend
		// being unavailable, never a target not found.
		f := failure(err)
		f.Kind = transport.FailureUnavailable
		return transport.Reply[[]transport.Terminal]{Err: f}
	}
	v, err := jsonjs.Parse([]byte(out))
	if err != nil {
		return transport.Reply[[]transport.Terminal]{Err: &transport.Failure{Kind: transport.FailureProtocol, Message: "hc ls returned no JSON: " + err.Error(), Cause: err}}
	}
	obj, ok := v.(*jsonjs.Object)
	if !ok {
		return transport.Reply[[]transport.Terminal]{Err: &transport.Failure{Kind: transport.FailureProtocol, Message: "hc ls returned no list record"}}
	}
	rawList, ok := obj.Get("terminals")
	if !ok {
		return transport.Reply[[]transport.Terminal]{Err: &transport.Failure{Kind: transport.FailureProtocol, Message: "hc ls returned no terminals list"}}
	}
	list, ok := rawList.([]any)
	if !ok {
		return transport.Reply[[]transport.Terminal]{Err: &transport.Failure{Kind: transport.FailureProtocol, Message: "hc ls returned a non-array terminals list"}}
	}
	records := listRecords(out)
	terms := make([]transport.Terminal, 0, len(list))
	for i, item := range list {
		rec, ok := item.(*jsonjs.Object)
		if !ok {
			return transport.Reply[[]transport.Terminal]{Err: &transport.Failure{Kind: transport.FailureProtocol, Message: "hc ls returned a non-object terminal"}}
		}
		terms = append(terms, transport.Terminal{
			Target:   transport.Target{ID: addressOf(rec)},
			Name:     nameOf(rec),
			PaneID:   addressOf(rec),
			State:    stateOf(rec),
			Sequence: listTurn(records, i),
		})
	}
	return transport.Reply[[]transport.Terminal]{Value: terms, Valid: true}
}

// Get reads one terminal's state. Exit 3 (not found) and a finished terminal
// are the gone state with no error, like the Herdr backend; any other
// failure is unavailable with the cause; a live terminal with no status is
// unknown.
func (b *backend) Get(ctx context.Context, target transport.Target, call transport.Call) transport.Reply[transport.Terminal] {
	out, err := b.run(ctx, call, "get", addr(target))
	if err != nil {
		if notFound(err) {
			return transport.Reply[transport.Terminal]{Value: transport.Terminal{Target: target, State: "gone"}, Valid: true}
		}
		return transport.Reply[transport.Terminal]{Value: transport.Terminal{Target: target, State: "unavailable"}, Valid: true, Err: failure(err)}
	}
	obj, ok := terminalObject(out)
	if !ok {
		return transport.Reply[transport.Terminal]{Value: transport.Terminal{Target: target, State: "unknown"}, Err: &transport.Failure{Kind: transport.FailureProtocol, Message: "hc get returned no terminal record"}}
	}
	return transport.Reply[transport.Terminal]{
		Value: transport.Terminal{
			Target:   target,
			Name:     nameOf(obj),
			PaneID:   addressOf(obj),
			State:    stateOf(obj),
			Sequence: turnRaw(json.RawMessage(out)),
		},
		Valid: true,
	}
}

// Send is not supported with transport=hc: prompt delivery is the next
// slice's `hc send` work.
func (b *backend) Send(ctx context.Context, target transport.Target, send transport.Send, call transport.Call) transport.Reply[string] {
	return transport.Reply[string]{Err: unsupported("Send")}
}

// Keys sends one key on the terminal's channel (hc has one channel; the
// pane flag is not honored). A key soho does not map is
// FailureUnsupported, never a guess at hc's name for it.
func (b *backend) Keys(ctx context.Context, target transport.Target, keys string, pane bool, call transport.Call) transport.Reply[bool] {
	mapped, ok := keysHC[keys]
	if !ok {
		return transport.Reply[bool]{Err: &transport.Failure{Kind: transport.FailureUnsupported, Message: fmt.Sprintf("key %q is not supported with transport=hc", keys)}}
	}
	if _, err := b.run(ctx, call, "keys", addr(target), mapped); err != nil {
		return transport.Reply[bool]{Err: failure(err)}
	}
	return transport.Reply[bool]{Value: true, Valid: true}
}

// Read returns the terminal's screen (source "visible", --lines 0) or its
// history above the screen (source "recent-unwrapped", --lines N: the
// request's Lines, or defaultRecentLines when the request names none). The
// raw hc read record (text, complete, how, why) stays on the reply's
// Process for the later slices; a source with no hc equivalent is
// FailureUnsupported.
func (b *backend) Read(ctx context.Context, target transport.Target, read transport.Read, call transport.Call) transport.Reply[string] {
	lines, ok := readLines(read)
	if !ok {
		return transport.Reply[string]{Err: &transport.Failure{Kind: transport.FailureUnsupported, Message: fmt.Sprintf("read source %q is not supported with transport=hc", read.Source)}}
	}
	out, err := b.run(ctx, call, "read", addr(target), "--lines", strconv.Itoa(lines))
	if err != nil {
		return transport.Reply[string]{Err: failure(err)}
	}
	v, err := jsonjs.Parse([]byte(out))
	if err != nil {
		return transport.Reply[string]{Err: &transport.Failure{Kind: transport.FailureProtocol, Message: "hc read returned no JSON: " + err.Error(), Cause: err}}
	}
	obj, ok := v.(*jsonjs.Object)
	if !ok {
		return transport.Reply[string]{Err: &transport.Failure{Kind: transport.FailureProtocol, Message: "hc read returned no read record"}}
	}
	text := ""
	if v, ok := obj.Get("text"); ok {
		if s, isString := v.(string); isString {
			text = s
		}
	}
	return transport.Reply[string]{Value: text, Valid: true, Process: processRecord(out)}
}

// Close ends the terminal's program and removes its record: `hc kill` then
// `hc rm`; a terminal that is already finished goes straight to `hc rm`.
// Closed means the record is gone: a terminal whose record is already
// removed reports closed.
func (b *backend) Close(ctx context.Context, target transport.Target, call transport.Call) transport.Reply[bool] {
	id := addr(target)
	out, err := b.run(ctx, call, "get", id)
	if err != nil {
		if notFound(err) {
			return transport.Reply[bool]{Value: true, Valid: true}
		}
		return transport.Reply[bool]{Err: failure(err)}
	}
	obj, ok := terminalObject(out)
	if !ok {
		return transport.Reply[bool]{Err: &transport.Failure{Kind: transport.FailureProtocol, Message: "hc get returned no terminal record"}}
	}
	if stateOf(obj) != "gone" {
		if _, err := b.run(ctx, call, "kill", id); err != nil {
			if notFound(err) {
				return transport.Reply[bool]{Value: true, Valid: true}
			}
			return transport.Reply[bool]{Err: failure(err)}
		}
	}
	if _, err := b.run(ctx, call, "rm", id); err != nil {
		if notFound(err) {
			return transport.Reply[bool]{Value: true, Valid: true}
		}
		return transport.Reply[bool]{Err: failure(err)}
	}
	return transport.Reply[bool]{Value: true, Valid: true}
}

// Window.

// Layout is a no-op: hc has no panes or layout, and herdr-app owns them.
func (b *backend) Layout(ctx context.Context, pane string, call transport.Call) transport.Reply[json.RawMessage] {
	return transport.Reply[json.RawMessage]{Valid: true}
}

// ListPanes is a no-op: hc has no panes, and herdr-app owns them.
func (b *backend) ListPanes(ctx context.Context, scope transport.Scope, call transport.Call) transport.Reply[[]transport.View] {
	return transport.Reply[[]transport.View]{Valid: true}
}

// ListTabs is a no-op: hc has no tabs, and herdr-app owns them.
func (b *backend) ListTabs(ctx context.Context, workspace string, call transport.Call) transport.Reply[[]transport.View] {
	return transport.Reply[[]transport.View]{Valid: true}
}

// Tab is a no-op: hc has no tabs, and herdr-app owns them.
func (b *backend) Tab(ctx context.Context, tab string, call transport.Call) transport.Reply[transport.View] {
	return transport.Reply[transport.View]{Valid: true}
}

// CreateTab is unsupported: creating a tab must produce a pane, and hc has
// no panes.
func (b *backend) CreateTab(ctx context.Context, open transport.Open, call transport.Call) transport.Reply[transport.View] {
	return transport.Reply[transport.View]{Err: unsupported("CreateTab")}
}

// RenameTab is a no-op: hc has no tabs, and herdr-app owns them.
func (b *backend) RenameTab(ctx context.Context, tab, label string, call transport.Call) transport.Reply[bool] {
	return transport.Reply[bool]{Valid: true}
}

// Split is unsupported: splitting a pane must produce a pane, and hc has no
// panes.
func (b *backend) Split(ctx context.Context, open transport.Open, call transport.Call) transport.Reply[transport.View] {
	return transport.Reply[transport.View]{Err: unsupported("Split")}
}

// Move is a no-op: hc has no panes, and herdr-app owns them.
func (b *backend) Move(ctx context.Context, move transport.Move, call transport.Call) transport.Reply[transport.View] {
	return transport.Reply[transport.View]{Valid: true}
}

// Focus is a no-op: hc has no focus to move, and herdr-app owns it.
func (b *backend) Focus(ctx context.Context, focus transport.Focus, call transport.Call) transport.Reply[bool] {
	return transport.Reply[bool]{Valid: true}
}

// Title is a no-op: hc has no pane titles to set, and herdr-app owns them.
func (b *backend) Title(ctx context.Context, pane string, title *string, call transport.Call) transport.Reply[bool] {
	return transport.Reply[bool]{Valid: true}
}

// Notify is a no-op: hc offers no notification soho can trigger.
func (b *backend) Notify(ctx context.Context, title, body string, call transport.Call) transport.Reply[bool] {
	return transport.Reply[bool]{Valid: true}
}

// LegacyTransport.

// AgentGetOK is the raw one-attempt existence check: one hc get on the
// target's id, Value true only when the call is not a lookup failure and
// exits 0. It never retries and does not parse the body.
func (b *backend) AgentGetOK(ctx context.Context, target transport.Target, call transport.Call) transport.Reply[bool] {
	if _, err := b.run(ctx, call, "get", addr(target)); err != nil {
		return transport.Reply[bool]{Err: failure(err)}
	}
	return transport.Reply[bool]{Value: true, Valid: true}
}

// ProcessInfo is not supported with transport=hc.
func (b *backend) ProcessInfo(ctx context.Context, target transport.Target, call transport.Call) transport.Reply[json.RawMessage] {
	return transport.Reply[json.RawMessage]{Err: unsupported("ProcessInfo")}
}

// Rename is not supported with transport=hc: a terminal's name is fixed at
// `hc run --name`, and no rename verb exists.
func (b *backend) Rename(ctx context.Context, target transport.Target, name string, call transport.Call) transport.Reply[bool] {
	return transport.Reply[bool]{Err: unsupported("Rename")}
}

// AgentGetKnown is not supported with transport=hc.
func (b *backend) AgentGetKnown(ctx context.Context, target transport.Target, call transport.Call) transport.Reply[bool] {
	return transport.Reply[bool]{Err: unsupported("AgentGetKnown")}
}

// AgentReadRaw is not supported with transport=hc.
func (b *backend) AgentReadRaw(ctx context.Context, target transport.Target, source, lines string, call transport.Call) transport.Reply[string] {
	return transport.Reply[string]{Err: unsupported("AgentReadRaw")}
}

// AgentListRaw is not supported with transport=hc.
func (b *backend) AgentListRaw(ctx context.Context, call transport.Call) transport.Reply[[]json.RawMessage] {
	return transport.Reply[[]json.RawMessage]{Err: unsupported("AgentListRaw")}
}

// AgentGetRaw is not supported with transport=hc.
func (b *backend) AgentGetRaw(ctx context.Context, target transport.Target, call transport.Call) transport.Reply[string] {
	return transport.Reply[string]{Err: unsupported("AgentGetRaw")}
}

// TabGetRaw is not supported with transport=hc.
func (b *backend) TabGetRaw(ctx context.Context, tab string, call transport.Call) transport.Reply[transport.TabFields] {
	return transport.Reply[transport.TabFields]{Err: unsupported("TabGetRaw")}
}

// PaneCurrentRaw is not supported with transport=hc.
func (b *backend) PaneCurrentRaw(ctx context.Context, call transport.Call) transport.Reply[string] {
	return transport.Reply[string]{Err: unsupported("PaneCurrentRaw")}
}

// PaneGetRaw is not supported with transport=hc.
func (b *backend) PaneGetRaw(ctx context.Context, target transport.Target, call transport.Call) transport.Reply[string] {
	return transport.Reply[string]{Err: unsupported("PaneGetRaw")}
}

// ClaudeTranscriptPath is not supported with transport=hc: the hc terminal
// record carries no session id to derive the transcript path from.
func (b *backend) ClaudeTranscriptPath(ctx context.Context, target transport.Target) transport.Reply[string] {
	return transport.Reply[string]{Err: unsupported("ClaudeTranscriptPath")}
}

// PiSessionPath is not supported with transport=hc: the hc terminal record
// carries no session id to derive the session path from.
func (b *backend) PiSessionPath(ctx context.Context, target transport.Target) transport.Reply[string] {
	return transport.Reply[string]{Err: unsupported("PiSessionPath")}
}

// WorkspaceCreateRaw is not supported with transport=hc: hc has no
// workspaces.
func (b *backend) WorkspaceCreateRaw(ctx context.Context, cwd, label string, env map[string]string, call transport.Call) transport.Reply[string] {
	return transport.Reply[string]{Err: unsupported("WorkspaceCreateRaw")}
}

// WorkspaceCloseRaw is not supported with transport=hc: hc has no
// workspaces.
func (b *backend) WorkspaceCloseRaw(ctx context.Context, id string, call transport.Call) transport.Reply[string] {
	return transport.Reply[string]{Err: unsupported("WorkspaceCloseRaw")}
}

// WorkspaceListRaw is not supported with transport=hc: hc has no
// workspaces.
func (b *backend) WorkspaceListRaw(ctx context.Context, call transport.Call) transport.Reply[string] {
	return transport.Reply[string]{Err: unsupported("WorkspaceListRaw")}
}

// PaneListRaw is not supported with transport=hc: hc has no panes.
func (b *backend) PaneListRaw(ctx context.Context, workspaceID string, call transport.Call) transport.Reply[string] {
	return transport.Reply[string]{Err: unsupported("PaneListRaw")}
}

// PaneRunRaw is not supported with transport=hc: hc has no panes.
func (b *backend) PaneRunRaw(ctx context.Context, paneID string, argv []string, call transport.Call) transport.Reply[string] {
	return transport.Reply[string]{Err: unsupported("PaneRunRaw")}
}

// AgentGetMachineRaw is not supported with transport=hc.
func (b *backend) AgentGetMachineRaw(ctx context.Context, target transport.Target, call transport.Call) transport.Reply[string] {
	return transport.Reply[string]{Err: unsupported("AgentGetMachineRaw")}
}

// AgentWaitRaw is not supported with transport=hc.
func (b *backend) AgentWaitRaw(ctx context.Context, target transport.Target, timeoutMS int, call transport.Call) transport.Reply[string] {
	return transport.Reply[string]{Err: unsupported("AgentWaitRaw")}
}

// AgentPromptWaitRaw is not supported with transport=hc.
func (b *backend) AgentPromptWaitRaw(ctx context.Context, target transport.Target, body string, timeoutMS int, call transport.Call) transport.Reply[string] {
	return transport.Reply[string]{Err: unsupported("AgentPromptWaitRaw")}
}

// AgentPromptRaw is not supported with transport=hc.
func (b *backend) AgentPromptRaw(ctx context.Context, target transport.Target, body string, call transport.Call) transport.Reply[string] {
	return transport.Reply[string]{Err: unsupported("AgentPromptRaw")}
}

// AgentReadSourceRaw is not supported with transport=hc.
func (b *backend) AgentReadSourceRaw(ctx context.Context, target transport.Target, source string, lines *int, call transport.Call) transport.Reply[string] {
	return transport.Reply[string]{Err: unsupported("AgentReadSourceRaw")}
}

// AgentSendKeysRaw is not supported with transport=hc.
func (b *backend) AgentSendKeysRaw(ctx context.Context, target transport.Target, key string, call transport.Call) transport.Reply[bool] {
	return transport.Reply[bool]{Err: unsupported("AgentSendKeysRaw")}
}

// SnapshotRaw is not supported with transport=hc.
func (b *backend) SnapshotRaw(ctx context.Context, target transport.Target, call transport.Call) transport.Reply[string] {
	return transport.Reply[string]{Err: unsupported("SnapshotRaw")}
}

// MachineListRaw is not supported with transport=hc.
func (b *backend) MachineListRaw(ctx context.Context, call transport.Call) transport.Reply[string] {
	return transport.Reply[string]{Err: unsupported("MachineListRaw")}
}

// VersionRaw is not supported with transport=hc.
func (b *backend) VersionRaw(ctx context.Context, call transport.Call) transport.Reply[string] {
	return transport.Reply[string]{Err: unsupported("VersionRaw")}
}

// StatusServerRaw is not supported with transport=hc.
func (b *backend) StatusServerRaw(ctx context.Context, call transport.Call) transport.Reply[string] {
	return transport.Reply[string]{Err: unsupported("StatusServerRaw")}
}

// SkillRaw is not supported with transport=hc.
func (b *backend) SkillRaw(ctx context.Context, call transport.Call) transport.Reply[string] {
	return transport.Reply[string]{Err: unsupported("SkillRaw")}
}

// PluginListRaw is not supported with transport=hc.
func (b *backend) PluginListRaw(ctx context.Context, call transport.Call) transport.Reply[string] {
	return transport.Reply[string]{Err: unsupported("PluginListRaw")}
}

// PluginInstallRefRaw is not supported with transport=hc.
func (b *backend) PluginInstallRefRaw(ctx context.Context, ref string, call transport.Call) transport.Reply[string] {
	return transport.Reply[string]{Err: unsupported("PluginInstallRefRaw")}
}

// PluginConfigDirRaw is not supported with transport=hc.
func (b *backend) PluginConfigDirRaw(ctx context.Context, pluginID string, call transport.Call) transport.Reply[string] {
	return transport.Reply[string]{Err: unsupported("PluginConfigDirRaw")}
}

// PluginRemoveRaw is not supported with transport=hc.
func (b *backend) PluginRemoveRaw(ctx context.Context, verb, pluginID string, call transport.Call) transport.Reply[string] {
	return transport.Reply[string]{Err: unsupported("PluginRemoveRaw")}
}

// ServerReloadConfigRaw is not supported with transport=hc.
func (b *backend) ServerReloadConfigRaw(ctx context.Context, call transport.Call) transport.Reply[string] {
	return transport.Reply[string]{Err: unsupported("ServerReloadConfigRaw")}
}

// Runtime.

// Start is not supported with transport=hc: agent start (the argv
// resolution for the 7501 assistants among it) is the next slice's work.
func (b *backend) Start(ctx context.Context, start transport.Start, call transport.Call) transport.Reply[string] {
	return transport.Reply[string]{Err: unsupported("Start")}
}

// CallerAgentName returns the name of the terminal the caller runs in: one
// hc get on HC_TERMINAL_ID, the record's name. An unnamed terminal, like a
// missing one, is a failure with an empty value.
func (b *backend) CallerAgentName(ctx context.Context, call transport.Call) transport.Reply[string] {
	pane := b.env.Get("HC_TERMINAL_ID")
	if pane == "" {
		return transport.Reply[string]{Err: &transport.Failure{Kind: transport.FailureUnavailable, Message: "not inside an hc terminal: HC_TERMINAL_ID is not set"}}
	}
	out, err := b.run(ctx, call, "get", pane)
	if err != nil {
		return transport.Reply[string]{Err: failure(err)}
	}
	obj, ok := terminalObject(out)
	if !ok {
		return transport.Reply[string]{Err: &transport.Failure{Kind: transport.FailureProtocol, Message: "hc get returned no terminal record"}}
	}
	name := nameOf(obj)
	if name == "" {
		return transport.Reply[string]{Err: &transport.Failure{Kind: transport.FailureNotFound, Message: "the caller terminal has no name"}}
	}
	return transport.Reply[string]{Value: name, Valid: true}
}

// SendText is not supported with transport=hc: hc offers no paste without
// Enter, and the next slice merges its callers' sends.
func (b *backend) SendText(ctx context.Context, target transport.Target, text string, call transport.Call) transport.Reply[bool] {
	return transport.Reply[bool]{Err: unsupported("SendText")}
}

// GetGone reports whether the terminal is already gone: `hc get` exits 3
// (the record is removed) or the record's state is finished (the program has
// ended). A live terminal, or a failure for another reason, reports false.
func (b *backend) GetGone(ctx context.Context, target transport.Target, call transport.Call) transport.Reply[bool] {
	out, err := b.run(ctx, call, "get", addr(target))
	if err != nil {
		if notFound(err) {
			return transport.Reply[bool]{Value: true, Valid: true}
		}
		return transport.Reply[bool]{Value: false, Valid: true}
	}
	obj, ok := terminalObject(out)
	if !ok {
		return transport.Reply[bool]{Value: false, Valid: true}
	}
	return transport.Reply[bool]{Value: stateOf(obj) == "gone", Valid: true}
}
