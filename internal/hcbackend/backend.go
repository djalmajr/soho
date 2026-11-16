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
	"math"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/djalmajr/soho/internal/agentexec"
	"github.com/djalmajr/soho/internal/hc"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/launchprofile"
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
	return NewWithClient(env, hc.New(env))
}

// NewWithClient builds the hc backend's Services using the provided hc client.
func NewWithClient(env platform.Env, client *hc.Client) transport.Services {
	b := &backend{env: env, client: client}
	return transport.Services{Runtime: b, Window: b, Legacy: b, Terminal: b}
}

// backend implements all four transport interfaces. Window methods are
// no-ops (or unsupported where they must produce a pane); the Legacy and the
// remaining Transport/Runtime methods this slice does not implement refuse
// as unsupported.
type backend struct {
	env    platform.Env
	client *hc.Client

	sessionMu    sync.Mutex
	sessionDone  bool
	sessionValue string
	sessionErr   error

	abs func(string) (string, error)
}

func (b *backend) absPath(path string) (string, error) {
	if b.abs != nil {
		return b.abs(path)
	}
	return filepath.Abs(path)
}

// Session.

// Session resolves the soho session id the caller's own terminal carries:
// the soho.session label of `hc get $HC_TERMINAL_ID --json`, or
// "hc-<address>" when the label is absent. Outside an hc terminal it is an
// error.
func Session(env platform.Env) (string, error) {
	b := &backend{env: env, client: hc.New(env)}
	return b.session(context.Background(), transport.Call{})
}

// session resolves the session through the instance's runner, lazily and
// within the request's budget (ctx and call, through run). The value is
// memoized, so no later call issues the session read again; so is a failure
// the read met with the full default budget, judged before the read. A
// failure under a shorter budget, or one the request's cancellation caused,
// is not memoized: a later call with more time reads again. The lock is not
// held across the read, so a caller never waits on another caller's read;
// concurrent first reads may each launch, and the first stored result wins.
func (b *backend) session(ctx context.Context, call transport.Call) (string, error) {
	b.sessionMu.Lock()
	if b.sessionDone {
		value, err := b.sessionValue, b.sessionErr
		b.sessionMu.Unlock()
		return value, err
	}
	b.sessionMu.Unlock()
	full := fullBudget(ctx, call)
	value, err := b.sessionOf(ctx, call)
	if err != nil && !errors.Is(err, ErrNotInHcTerminal) && (!full || (ctx != nil && ctx.Err() != nil)) {
		return value, err
	}
	b.sessionMu.Lock()
	defer b.sessionMu.Unlock()
	if !b.sessionDone {
		b.sessionValue, b.sessionErr, b.sessionDone = value, err, true
	}
	return b.sessionValue, b.sessionErr
}

func (b *backend) sessionOf(ctx context.Context, call transport.Call) (string, error) {
	addr := b.env.Get("HC_TERMINAL_ID")
	if addr == "" {
		return "", ErrNotInHcTerminal
	}
	out, err := b.run(ctx, call, "get", addr)
	if err != nil {
		return "", err
	}
	return sessionFromGet(out)
}

// fullBudget reports whether a launch under ctx and call runs with the
// default 30s: no call timeout, a live parent, and no parent deadline
// sooner than it.
func fullBudget(ctx context.Context, call transport.Call) bool {
	if call.Timeout > 0 {
		return false
	}
	if ctx != nil {
		if ctx.Err() != nil {
			return false
		}
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < hcTimeoutDefault {
			return false
		}
	}
	return true
}

// sessionFromGet extracts the session from one decoded hc get record: the
// soho.session label when it names one, else "hc-<address>".
func sessionFromGet(stdout string) (string, error) {
	obj, ok := terminalObject(stdout)
	if !ok {
		return "", errors.New("hc get returned no terminal record")
	}
	if session := sessionOfRecord(obj); session != "" {
		return session, nil
	}
	return "", errors.New("hc get returned no soho.session label and no address for the session fallback")
}

// sessionLabelOfRecord is one terminal record's explicit soho.session
// label ("" when it has none): the value a labeled listing selects.
func sessionLabelOfRecord(obj *jsonjs.Object) string {
	if labels, ok := obj.Get("labels"); ok {
		if labelsObj, ok := labels.(*jsonjs.Object); ok {
			if v, ok := labelsObj.Get(SessionLabel); ok {
				if label, isString := v.(string); isString && label != "" {
					return label
				}
			}
		}
	}
	return ""
}

// sessionOfRecord is one terminal record's soho session: the soho.session
// label when it names one, else the "hc-<address>" fallback the session
// resolution uses ("" when the record names neither).
func sessionOfRecord(obj *jsonjs.Object) string {
	if label := sessionLabelOfRecord(obj); label != "" {
		return label
	}
	if v, ok := obj.Get("address"); ok {
		if address, isString := v.(string); isString && address != "" {
			return "hc-" + address
		}
	}
	return ""
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
	if ctx == nil {
		// Transport consumers may pass a nil context (core.Orphans does).
		ctx = context.Background()
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
	if word, isString := raw.(string); isString && word != "" {
		return word
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

// nativeStateOf extracts the terminal record's raw string lifecycle
// ("live", "finished", etc.). Missing or non-string values return "".
func nativeStateOf(obj *jsonjs.Object) string {
	if obj == nil {
		return ""
	}
	if v, ok := obj.Get("state"); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// statusMsgOf returns the terminal record's status message (the hc
// status.msg, "" when the record reports none) exactly as hc wrote it, so
// a consumer that maps the state word can also carry the reported cause.
func statusMsgOf(obj *jsonjs.Object) string {
	raw, ok := obj.Get("status")
	if !ok || raw == nil {
		return ""
	}
	status, ok := raw.(*jsonjs.Object)
	if !ok {
		return ""
	}
	if v, ok := status.Get("msg"); ok {
		if msg, isString := v.(string); isString {
			return msg
		}
	}
	return ""
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

// listedAddress is a listing record's terminal address. A record whose
// address is missing, not a string or empty is a protocol failure that
// refuses the whole listing: callers use a valid listing as proof that a
// terminal is absent, and a record without identity proves nothing.
func listedAddress(rec *jsonjs.Object) (string, *transport.Failure) {
	if address := addressOf(rec); address != "" {
		return address, nil
	}
	return "", &transport.Failure{Kind: transport.FailureProtocol, Message: "hc ls returned a terminal without an address"}
}

// listedParent reads the record's parent: "" and known when it is omitted,
// the string when it is one. A parent of any other type is unknown, never
// "no parent", and never fails the listing the other consumers read.
func listedParent(rec *jsonjs.Object) (parent string, known bool) {
	value, ok := rec.Get("parent")
	if !ok {
		return "", true
	}
	parent, ok = value.(string)
	return parent, ok
}

// cwdOf reads the record's cwd ("" when the terminal reports none).
func cwdOf(obj *jsonjs.Object) string {
	if v, ok := obj.Get("cwd"); ok {
		if cwd, isString := v.(string); isString && cwd != "" {
			return cwd
		}
	}
	if raw, ok := obj.Get("foreground"); ok && raw != nil {
		if fg, ok := raw.(*jsonjs.Object); ok {
			if v, ok := fg.Get("cwd"); ok {
				if cwd, isString := v.(string); isString && cwd != "" {
					return cwd
				}
			}
		}
	}
	return ""
}

// currentCwdOf preserves the provenance of a reported foreground directory.
func currentCwdOf(obj *jsonjs.Object) string {
	if raw, ok := obj.Get("foreground"); ok {
		if foreground, ok := raw.(*jsonjs.Object); ok {
			if raw, ok := foreground.Get("cwd"); ok {
				if cwd, ok := raw.(string); ok {
					return cwd
				}
			}
		}
	}
	return ""
}

// programHints renders one terminal record's program hints: the nonempty
// status app first, then foreground.name when present and different, then the
// basename of argv[0] when present and different (both slash styles normalized
// for the basename; no shell parsing and no other argv element is inspected).
// Hints only: canonical kind matching stays in the kinds catalog.
func programHints(obj *jsonjs.Object) []string {
	var hints []string
	if raw, ok := obj.Get("status"); ok && raw != nil {
		if status, ok := raw.(*jsonjs.Object); ok {
			if v, ok := status.Get("app"); ok {
				if app, isString := v.(string); isString && app != "" {
					hints = append(hints, app)
				}
			}
		}
	}
	if raw, ok := obj.Get("foreground"); ok && raw != nil {
		if fg, ok := raw.(*jsonjs.Object); ok {
			if v, ok := fg.Get("name"); ok {
				if name, isString := v.(string); isString && name != "" {
					if len(hints) == 0 || hints[0] != name {
						hints = append(hints, name)
					}
				}
			}
		}
	}
	if raw, ok := obj.Get("argv"); ok {
		if args, ok := raw.([]any); ok && len(args) > 0 {
			if exe, ok := args[0].(string); ok && exe != "" {
				if base := executableBase(exe); base != "" {
					dup := false
					for _, h := range hints {
						if h == base {
							dup = true
							break
						}
					}
					if !dup {
						hints = append(hints, base)
					}
				}
			}
		}
	}
	return hints
}

// executableBase takes the basename of an executable path with both slash
// styles normalized (a bare name is its own basename).
func executableBase(exe string) string {
	return path.Base(strings.ReplaceAll(exe, "\\", "/"))
}

// launchProfileOf reads the record's soho.launch_profile label: an actual
// string at the exact label key ("" when the labels are absent, not a map,
// or the label is missing, null or not a string).
func launchProfileOf(obj *jsonjs.Object) string {
	if labels, ok := obj.Get("labels"); ok {
		if labelsObj, ok := labels.(*jsonjs.Object); ok {
			if v, ok := labelsObj.Get(transport.LaunchProfileLabel); ok {
				if profile, isString := v.(string); isString {
					return profile
				}
			}
		}
	}
	return ""
}

// reportedOf reads the record's top-level reported flag: true only when it
// is the actual bool true (absent, null, false or another type stay false,
// never coerced).
func reportedOf(obj *jsonjs.Object) bool {
	if v, ok := obj.Get("reported"); ok {
		if reported, isBool := v.(bool); isBool && reported {
			return true
		}
	}
	return false
}

// reporterAppOf reads the record's status.app: an actual string ("" when the
// status is absent, null or not a map, or the app is missing, null or not a
// string).
func reporterAppOf(obj *jsonjs.Object) string {
	raw, ok := obj.Get("status")
	if !ok || raw == nil {
		return ""
	}
	status, ok := raw.(*jsonjs.Object)
	if !ok {
		return ""
	}
	if v, ok := status.Get("app"); ok {
		if app, isString := v.(string); isString {
			return app
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
	session, _ := b.session(context.Background(), transport.Call{})
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
// the live roster. Scope.All instead lists every live local terminal of the
// machine (listAllLocal): one unfiltered `hc ls`, no caller session read.
func (b *backend) List(ctx context.Context, scope transport.Scope, call transport.Call) transport.Reply[[]transport.Terminal] {
	if scope.All {
		return b.listAllLocal(ctx, call)
	}
	session, err := b.session(ctx, call)
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
		address, bad := listedAddress(rec)
		if bad != nil {
			return transport.Reply[[]transport.Terminal]{Err: bad}
		}
		parent, parentKnown := listedParent(rec)
		terms = append(terms, transport.Terminal{
			Target:      transport.Target{ID: address},
			Parent:      parent,
			ParentKnown: parentKnown,
			Name:        nameOf(rec),
			PaneID:      address,
			State:       stateOf(rec),
			NativeState: nativeStateOf(rec),
			Sequence:    listTurn(records, i),
		})
	}
	return transport.Reply[[]transport.Terminal]{Value: terms, Valid: true}
}

// listAllLocal lists every live local terminal of the machine for Scope.All:
// one unfiltered `hc ls` (no --label, no --all, one --json) without
// resolving the caller session or calling hc get. A finished (gone) record
// and a machine-prefixed (remote) address are not part of the local
// listing; an unknown state stays listed. A failed launch is unavailable
// and a malformed record refuses the whole list (no partial).
func (b *backend) listAllLocal(ctx context.Context, call transport.Call) transport.Reply[[]transport.Terminal] {
	out, err := b.run(ctx, call, "ls")
	if err != nil {
		// A machine listing names no target, so even an exit 3 is the
		// backend being unavailable, never a target not found.
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
		address, bad := listedAddress(rec)
		if bad != nil {
			return transport.Reply[[]transport.Terminal]{Err: bad}
		}
		parent, parentKnown := listedParent(rec)
		// A finished record is gone, and a machine-prefixed address is a
		// remote terminal, not part of the local listing.
		if stateOf(rec) == "gone" || strings.Contains(address, "/") {
			continue
		}
		terms = append(terms, transport.Terminal{
			Target:       transport.Target{ID: address},
			Parent:       parent,
			ParentKnown:  parentKnown,
			Name:         nameOf(rec),
			PaneID:       address,
			State:        stateOf(rec),
			NativeState:  nativeStateOf(rec),
			Sequence:     listTurn(records, i),
			Msg:          statusMsgOf(rec),
			Cwd:          cwdOf(rec),
			CurrentCwd:   currentCwdOf(rec),
			WorkspaceID:  sessionOfRecord(rec),
			SessionLabel: sessionLabelOfRecord(rec),
			ProgramHints: programHints(rec),
		})
	}
	return transport.Reply[[]transport.Terminal]{Value: terms, Valid: true}
}

// Get reads one terminal's state. Exit 3 (not found) and a finished terminal
// are the gone state with no error, like the Herdr backend; any other
// failure is unavailable with the cause; a live terminal with no status is
// unknown. The reply's Value also carries the record's source metadata: the
// soho.launch_profile label, the top-level reported flag and the status app.
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
	address := addressOf(obj)
	return transport.Reply[transport.Terminal]{
		Value: transport.Terminal{
			Target:        transport.Target{ID: address, Machine: target.Machine},
			Name:          nameOf(obj),
			PaneID:        address,
			State:         stateOf(obj),
			NativeState:   nativeStateOf(obj),
			Sequence:      turnRaw(json.RawMessage(out)),
			Msg:           statusMsgOf(obj),
			Cwd:           cwdOf(obj),
			CurrentCwd:    currentCwdOf(obj),
			WorkspaceID:   sessionOfRecord(obj),
			ProgramHints:  programHints(obj),
			LaunchProfile: launchProfileOf(obj),
			Reported:      reportedOf(obj),
			ReporterApp:   reporterAppOf(obj),
		},
		Valid: true,
	}
}

// Send pastes the prompt into the terminal and presses Enter: `hc send
// --if <guard> -- <target> <text>` (the -- ends the options, so a text that
// starts with a dash is a value), without --force, which is never passed.
// Queue lifts the guard (a blocked worker still refuses it) and runs with
// no capability probes, exactly today's `hc send -- <target> <text>`. The
// ordinary (non-queue) send selects its --if guard through the client's
// SendIfGuard under one deadline shared by every probe and the send: ready
// only when the local parser and the serving enum prove it, idle only when
// both prove idle, and a refusal is the capability failure with zero input.
// When idle is selected and a fresh get reports done, one unguarded send is
// used: hc has no done guard, and done means the turn ended at the prompt.
// This leaves a window for status changes between get and send; it is never
// retried. Otherwise the send retains its proven guard. The reply's Value is the
// raw SendResult record: its started flag is the immediate arrival proof
// and its turn the turn the send started. A worker that sits on a person's
// prompt is FailureBlocked, a worker whose status mismatches the guard that
// was actually used is FailureBusy, and a finished or removed terminal is
// FailureNotFound; anything else takes the backend's usual mapping.
func (b *backend) Send(ctx context.Context, target transport.Target, send transport.Send, call transport.Call) transport.Reply[string] {
	if send.Queue {
		args := []string{"send", "--", addr(target), send.Text}
		out, err := b.run(ctx, call, args...)
		return b.sendReply(target, out, err, "")
	}
	return b.sendGuarded(ctx, target, send, call)
}

// sendGuarded is the ordinary (non-queue) send with the proven --if guard:
// one deadline (the call's timeout, else the 30s default) clamps every
// probe and the send together, so the probes never extend the call's
// budget or outrun its cancellation; a guard refusal is the capability
// failure with zero input — the send is never issued. If the proven guard is
// idle, one get immediately before the send permits an unguarded send only
// for done, because hc lacks a done guard and the agent is at its prompt.
// The status can change in this window; the send is never retried.
func (b *backend) sendGuarded(ctx context.Context, target transport.Target, send transport.Send, call transport.Call) transport.Reply[string] {
	timeout := hcTimeoutDefault
	if call.Timeout > 0 {
		timeout = call.Timeout
	}
	if ctx == nil {
		ctx = context.Background()
	}
	shared, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	guard, err := b.client.SendIfGuard(shared, machineOf(addr(target)))
	if err != nil {
		return transport.Reply[string]{Err: guardRefusal(addr(target), err)}
	}
	address := addr(target)
	args := []string{"send", "--if", guard, "--", address, send.Text}
	if guard == hc.GuardIdle {
		if record, getErr := b.client.Run(shared, "get", address); getErr == nil {
			if obj, ok := terminalObject(record); ok && nativeStateOf(obj) != "finished" {
				if raw, ok := obj.Get("status"); ok {
					if status, ok := raw.(*jsonjs.Object); ok {
						if state, ok := status.Get("state"); ok && state == "done" {
							args = []string{"send", "--", address, send.Text}
						}
					}
				}
			}
		}
	}
	out, err := b.client.Run(shared, args...)
	return b.sendReply(target, out, err, guard)
}

// machineOf names the machine one effective hc address is on: the segment
// before the first slash of a machine/id address, "" when the address is a
// local terminal id. An embedded machine/id in the target's id is a remote
// address too, never a local one.
func machineOf(address string) string {
	i := strings.Index(address, "/")
	if i <= 0 {
		return ""
	}
	return address[:i]
}

// guardRefusal renders one guard refusal (the probes proved no --if guard)
// as the send that never happened: a probe launch failure keeps the
// backend's usual unavailable mapping, metadata that parsed but proved no
// common guard is a protocol failure — neither is busy or blocked, so no
// consumer settles, downgrades or resends.
func guardRefusal(name string, err error) *transport.Failure {
	kind := transport.FailureProtocol
	var hcErr *hc.Error
	if errors.As(err, &hcErr) || errors.Is(err, hc.ErrNotInstalled) {
		kind = transport.FailureUnavailable
	}
	return &transport.Failure{Kind: kind, Message: "cannot prove the --if guard for worker " + name + ": " + err.Error(), Cause: err}
}

// sendReply maps one hc send launch onto the send reply: exit 9 is the
// blocked worker, exit 10 the status mismatch against the guard that was
// actually used (idle stays "not idle", ready is "not ready"), exit 11 the
// finished or removed terminal; every other failure takes the backend's
// usual mapping. The queue path passes no guard, keeping today's message.
func (b *backend) sendReply(target transport.Target, out string, err error, guard string) transport.Reply[string] {
	if err != nil {
		var hcErr *hc.Error
		if errors.As(err, &hcErr) {
			name := addr(target)
			switch hcErr.Exit {
			case hc.ExitBlocked:
				return transport.Reply[string]{Err: &transport.Failure{Kind: transport.FailureBlocked, Message: fmt.Sprintf("worker %s is blocked: a person must answer its prompt", name)}}
			case hc.ExitStatusMismatch:
				want := "idle"
				if guard == hc.GuardReady {
					want = "ready"
				}
				return transport.Reply[string]{Err: &transport.Failure{Kind: transport.FailureBusy, Message: "worker " + name + " is not " + want}}
			case hc.ExitFinished:
				return transport.Reply[string]{Err: &transport.Failure{Kind: transport.FailureNotFound, Message: hcErr.Error(), Cause: err}}
			}
		}
		return transport.Reply[string]{Err: failure(err)}
	}
	return transport.Reply[string]{Value: out, Valid: true}
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

// Close ends the terminal's program and removes its record: `hc kill`
// then `hc rm`. A kill that exits 3, or that reports the program already
// finished, still runs the rm; an rm that exits 3 counts as closed. So a
// Close is safe for a live terminal, an already finished one, and the
// terminal a failed start left behind. Closed means the record is gone:
// a terminal whose record is already removed reports closed.
func (b *backend) Close(ctx context.Context, target transport.Target, call transport.Call) transport.Reply[bool] {
	id := addr(target)
	if _, err := b.run(ctx, call, "kill", id); err != nil {
		// An exit-3 kill found no terminal to end; a kill that reports the
		// program already finished exits 0 with the finished record. Either
		// way the record may still need its rm, so only a kill that failed
		// for another reason stops the close.
		if !notFound(err) {
			return transport.Reply[bool]{Err: failure(err)}
		}
	}
	if _, err := b.run(ctx, call, "rm", id); err != nil {
		// An rm that exits 3 found no record to remove: the terminal is
		// closed.
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

// ListPanes lists the session's terminals as the pane views the core
// helpers read: one view per terminal, its Raw the herdr-shaped pane
// object (its pane_id the terminal address) the way Orphans rebuilds the
// live list from the transport terminals with the field names that
// OrphansOf reads. The soho session is the workspace under transport=hc,
// and the orphan classification is the only consumer that reaches this
// under hc (the layout paths that also read the pane list never run under
// hc). Scope.All lists every live local terminal of the machine instead
// (listAllLocal). A roster query names no target, so a failure is the
// backend being unavailable, never a target not found.
func (b *backend) ListPanes(ctx context.Context, scope transport.Scope, call transport.Call) transport.Reply[[]transport.View] {
	if scope.All {
		// Every live local terminal of the machine, as the contract's
		// Scope.All says, never only the caller's session: a caller terminal
		// that carries no soho.session label is outside its own session
		// listing, and a machine-wide existence check must still see it.
		// Each view's Raw also names the terminal's session as its
		// workspace_id.
		listed := b.listAllLocal(ctx, call)
		if listed.Err != nil {
			return transport.Reply[[]transport.View]{Err: listed.Err}
		}
		views := make([]transport.View, 0, len(listed.Value))
		for _, terminal := range listed.Value {
			raw, _ := json.Marshal(struct {
				PaneID      string `json:"pane_id"`
				WorkspaceID string `json:"workspace_id"`
			}{terminal.PaneID, terminal.WorkspaceID})
			views = append(views, transport.View{ID: terminal.PaneID, PaneID: terminal.PaneID, WorkspaceID: terminal.WorkspaceID, Raw: json.RawMessage(raw)})
		}
		return transport.Reply[[]transport.View]{Value: views, Valid: true}
	}
	session, err := b.session(ctx, call)
	if err != nil {
		return transport.Reply[[]transport.View]{Err: &transport.Failure{Kind: transport.FailureUnavailable, Message: "cannot resolve the soho.session label: " + err.Error(), Cause: err}}
	}
	out, err := b.run(ctx, call, "ls", "--label", SessionLabel+"="+session)
	if err != nil {
		f := failure(err)
		f.Kind = transport.FailureUnavailable
		return transport.Reply[[]transport.View]{Err: f}
	}
	v, err := jsonjs.Parse([]byte(out))
	if err != nil {
		return transport.Reply[[]transport.View]{Err: &transport.Failure{Kind: transport.FailureProtocol, Message: "hc ls returned no JSON: " + err.Error(), Cause: err}}
	}
	obj, ok := v.(*jsonjs.Object)
	if !ok {
		return transport.Reply[[]transport.View]{Err: &transport.Failure{Kind: transport.FailureProtocol, Message: "hc ls returned no list record"}}
	}
	rawList, ok := obj.Get("terminals")
	if !ok {
		return transport.Reply[[]transport.View]{Err: &transport.Failure{Kind: transport.FailureProtocol, Message: "hc ls returned no terminals list"}}
	}
	list, ok := rawList.([]any)
	if !ok {
		return transport.Reply[[]transport.View]{Err: &transport.Failure{Kind: transport.FailureProtocol, Message: "hc ls returned a non-array terminals list"}}
	}
	views := make([]transport.View, 0, len(list))
	for _, item := range list {
		rec, ok := item.(*jsonjs.Object)
		if !ok {
			return transport.Reply[[]transport.View]{Err: &transport.Failure{Kind: transport.FailureProtocol, Message: "hc ls returned a non-object terminal"}}
		}
		address, bad := listedAddress(rec)
		if bad != nil {
			return transport.Reply[[]transport.View]{Err: bad}
		}
		raw, _ := json.Marshal(struct {
			PaneID string `json:"pane_id"`
		}{address})
		views = append(views, transport.View{ID: address, PaneID: address, Raw: json.RawMessage(raw)})
	}
	return transport.Reply[[]transport.View]{Value: views, Valid: true}
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

// AgentListRaw lists the session's raw terminal records: the terminals
// array of `hc ls --label soho.session=<s> --json`, one JSON value each, so
// callers that read fields beyond the terminal projection keep them. A
// roster query names no target, so a failure is the backend being
// unavailable, never a target not found.
func (b *backend) AgentListRaw(ctx context.Context, call transport.Call) transport.Reply[[]json.RawMessage] {
	session, err := b.session(ctx, call)
	if err != nil {
		return transport.Reply[[]json.RawMessage]{Err: &transport.Failure{Kind: transport.FailureUnavailable, Message: "cannot resolve the soho.session label: " + err.Error(), Cause: err}}
	}
	out, err := b.run(ctx, call, "ls", "--label", SessionLabel+"="+session)
	if err != nil {
		f := failure(err)
		f.Kind = transport.FailureUnavailable
		return transport.Reply[[]json.RawMessage]{Err: f}
	}
	if !json.Valid([]byte(out)) {
		return transport.Reply[[]json.RawMessage]{Err: &transport.Failure{Kind: transport.FailureProtocol, Message: "hc ls returned no JSON"}}
	}
	records := listRecords(out)
	if records == nil {
		return transport.Reply[[]json.RawMessage]{Err: &transport.Failure{Kind: transport.FailureProtocol, Message: "hc ls returned no terminals list"}}
	}
	return transport.Reply[[]json.RawMessage]{Value: records, Valid: true}
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

// startPollInterval is the readiness poll period after a successful hc run:
// one hc get per interval until the worker reports (DJA-604 slice 2b-2a).
const startPollInterval = 200 * time.Millisecond

// startTimeoutDefaultMs is the readiness deadline when Start.Timeout is
// empty, zero or invalid: the herdr default the spawn side passes.
const startTimeoutDefaultMs = 60000

// validHcName reports whether name is a valid hc terminal name: 1-32
// characters of a-z 0-9 - _.
func validHcName(name string) bool {
	return transport.ValidTerminalName(name)
}

// startTimeout parses the readiness deadline: milliseconds as a decimal
// string, as herdr takes it; empty, zero or invalid mean the default.
func startTimeout(timeout string) time.Duration {
	ms, err := strconv.Atoi(timeout)
	if err != nil || ms <= 0 {
		return time.Duration(startTimeoutDefaultMs) * time.Millisecond
	}
	return time.Duration(ms) * time.Millisecond
}

// startDeadlineSeconds renders the readiness deadline in seconds for the
// timeout message (60000 ms is "60", 250 is "0.25").
func startDeadlineSeconds(deadline time.Duration) string {
	return strconv.FormatFloat(float64(deadline.Milliseconds())/1000, 'g', -1, 64)
}

// Start starts the worker in a new hc terminal of this session and waits
// for its first reported status. The run is dispatched through the native
// terminal.run JSON-RPC client on the per-instance hc client.
// Value carries the new terminal's address on success. Admission is the
// shared launchprofile native status policy: the five kinds carry the empty
// profile and opencode carries exactly its primary profile; an
// unadmitted combination and an invalid name refuse before any launch.
// The five kinds keep the request's own argv and environment aliases,
// with no snapshot and no native validation; the admitted opencode plan
// is cloned and validated before the session read, and a plan the shared
// native validator refuses is a command failure before it. An admitted
// opencode never launches a bare executable name: a missing lookup or a
// failed or non-absolute normalization is the fixed value-free
// unavailable refusal before the terminal exists. After the ordinary
// readiness success, the admitted primary profile is qualified on the same
// get record against the shared ready policy (no second observation). A
// run that fails with hc's name_taken error is a FailureConflict (spawn
// picks the next suffix; this slice does not retry); readiness is one hc
// get per startPollInterval until the deadline.
func (b *backend) Start(ctx context.Context, start transport.Start, call transport.Call) transport.Reply[string] {
	if ctx == nil {
		ctx = context.Background()
	}
	if !launchprofile.NativeStatusSupported(start.Kind, start.LaunchProfile) {
		message := fmt.Sprintf("kind %s does not report status under transport=hc; use transport=herdr", start.Kind)
		switch {
		case start.Kind == "opencode":
			message = "opencode requires launch profile " + launchprofile.OpenCodePrimaryProfile + " under transport=hc"
		case start.LaunchProfile != "":
			message = "native launch profile is unsupported under transport=hc"
		}
		return transport.Reply[string]{Err: &transport.Failure{
			Kind:    transport.FailureUnsupported,
			Message: message,
		}}
	}
	if !validHcName(start.Name) {
		return transport.Reply[string]{Err: &transport.Failure{
			Kind:    transport.FailureCommand,
			Message: fmt.Sprintf("worker name '%s' is not a valid hc terminal name (1-32 of a-z 0-9 - _)", start.Name),
		}}
	}
	// The five empty-profile kinds keep the request's own argv and
	// environment aliases, exactly as the original start does: no clone
	// and no native validation, so a synchronous callback that mutates
	// the caller's values before the launch still reaches the serialized
	// request.
	args := start.Args
	envOverrides := start.EnvOverrides
	if start.Kind == "opencode" {
		// The admitted opencode plan is owned per-call snapshots of the
		// request's argv and environment, taken before the validation and
		// every session, lookup or launch callback: the start uses only
		// these values from here on, so a synchronous callback that
		// mutates the caller's slice or map cannot change the admitted
		// plan or the serialized launch.
		args = append([]string(nil), start.Args...)
		envOverrides = make(map[string]string, len(start.EnvOverrides))
		for name, value := range start.EnvOverrides {
			envOverrides[name] = value
		}
		if err := launchprofile.ValidateNativeLaunchProfile(start.Kind, start.LaunchProfile, args, envOverrides); err != nil {
			// The shared validator's one fixed value-free error: no
			// supplied argument, configuration or environment text
			// reaches the caller.
			return transport.Reply[string]{Err: &transport.Failure{
				Kind:    transport.FailureCommand,
				Message: "native launch profile is invalid",
			}}
		}
	}
	if start.LoginShell != "" && !filepath.IsAbs(start.LoginShell) {
		return loginShellNotAbsolute()
	}
	session, err := b.session(ctx, call)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return transport.Reply[string]{Err: &transport.Failure{Kind: transport.FailureCancelled, Message: cerr.Error(), Cause: cerr}}
		}
		return transport.Reply[string]{Err: &transport.Failure{Kind: transport.FailureUnavailable, Message: err.Error(), Cause: err}}
	}

	cwd := start.Cwd
	if cwd == "" {
		wd, err := os.Getwd()
		if err != nil {
			return transport.Reply[string]{Err: &transport.Failure{
				Kind:    transport.FailureUnavailable,
				Message: "getwd: " + err.Error(),
				Cause:   err,
			}}
		}
		cwd = wd
	}

	exe := agentexec.Executable(start.Kind)
	program := exe
	resolved, found := platform.FindExecutable(exe, b.env, platform.Current())
	switch {
	case start.Kind == "opencode" && !found:
		// The primary profile never launches a bare executable name: a
		// missing lookup is the fixed value-free refusal before any
		// terminal exists.
		if cerr := ctx.Err(); cerr != nil {
			return transport.Reply[string]{Err: &transport.Failure{Kind: transport.FailureCancelled, Message: cerr.Error(), Cause: cerr}}
		}
		return opencodeExecutableUnavailable()
	case found:
		abs, err := b.absPath(resolved)
		if err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return transport.Reply[string]{Err: &transport.Failure{Kind: transport.FailureCancelled, Message: cerr.Error(), Cause: cerr}}
			}
			if start.Kind == "opencode" {
				return opencodeExecutableUnavailable()
			}
			return transport.Reply[string]{Err: &transport.Failure{
				Kind:    transport.FailureUnavailable,
				Message: "abs: " + err.Error(),
				Cause:   err,
			}}
		}
		if start.Kind == "opencode" && !filepath.IsAbs(abs) {
			return opencodeExecutableUnavailable()
		}
		program = abs
	}

	if start.LoginShell != "" {
		program, args = loginShellLaunch(start.LoginShell, program, args)
	}

	budget := hcTimeoutDefault
	if call.Timeout > 0 {
		budget = call.Timeout
	}
	runCtx, runCancel := context.WithTimeout(ctx, budget)
	address, err := b.client.RunTerminal(runCtx, hc.TerminalRun{
		Name:          start.Name,
		Kind:          start.Kind,
		Program:       program,
		Cwd:           cwd,
		Caller:        b.env.Get("HC_TERMINAL_ID"),
		Session:       session,
		Args:          args,
		EnvOverrides:  envOverrides,
		LaunchProfile: start.LaunchProfile,
		RegistryDir:   start.RegistryDir,
		Agent:         start.Agent,
	})
	runCancel()

	if err != nil {
		if errors.Is(err, hc.ErrRunOutcomeUnknown) {
			return transport.Reply[string]{
				Value: address,
				Err: &transport.Failure{
					Kind:    transport.FailureRunOutcomeUnknown,
					Message: err.Error(),
					Cause:   err,
				},
			}
		}
		if cerr := ctx.Err(); cerr != nil {
			return transport.Reply[string]{
				Err: &transport.Failure{
					Kind:    transport.FailureCancelled,
					Message: cerr.Error(),
					Cause:   cerr,
				},
			}
		}
		return startRunFailure(start.Name, err)
	}

	return b.waitReady(ctx, call, start.Name, address, start.LaunchProfile, startTimeout(start.Timeout))
}

// loginShellLaunch wraps the agent launch in the user's login shell: the
// shell runs its profile (-l) and then replaces itself with the agent
// (exec "$0" "$@"). The agent's executable and arguments travel as the
// shell's positional parameters, never inside the command string, so no
// argument is parsed, expanded or split by the shell.
func loginShellLaunch(shell, program string, args []string) (string, []string) {
	wrapped := make([]string, 0, 4+len(args))
	wrapped = append(wrapped, "-l", "-c", `exec "$0" "$@"`, program)
	wrapped = append(wrapped, args...)
	return shell, wrapped
}

// loginShellNotAbsolute is the backend's one check of the login shell: the
// caller validated the shell, the backend only refuses a relative value.
func loginShellNotAbsolute() transport.Reply[string] {
	return transport.Reply[string]{Err: &transport.Failure{
		Kind:    transport.FailureCommand,
		Message: "login shell must be an absolute path",
	}}
}

// opencodeExecutableUnavailable is the primary profile's fixed value-free
// refusal for its executable requirement: the lookup found no opencode
// executable, or its absolute normalization failed or returned a path that
// is not absolute. No private path or error text crosses the boundary.
func opencodeExecutableUnavailable() transport.Reply[string] {
	return transport.Reply[string]{Err: &transport.Failure{
		Kind:    transport.FailureUnavailable,
		Message: "opencode executable is unavailable for primary launch profile",
	}}
}

// startRunFailure maps one failed hc run onto the transport failure it is:
// hc's name_taken error is a conflict (names are unique on the whole core,
// not only this session), every other hc error is the command having
// failed, and a launch that never resolved is the backend unreachable.
func startRunFailure(name string, err error) transport.Reply[string] {
	var hcErr *hc.Error
	if errors.As(err, &hcErr) {
		if hcErr.Kind == "name_taken" {
			return transport.Reply[string]{Err: &transport.Failure{Kind: transport.FailureConflict, Message: "name_taken: " + name, Cause: err}}
		}
		return transport.Reply[string]{Err: &transport.Failure{Kind: transport.FailureCommand, Message: hcErr.Error(), Cause: err}}
	}
	return transport.Reply[string]{Err: &transport.Failure{Kind: transport.FailureUnavailable, Message: err.Error(), Cause: err}}
}

// waitReady polls one hc get on the new terminal's address per
// startPollInterval until startup is decided: a reported idle or done is
// ready, a blocked status is the blocked failure (the terminal stays; spawn
// decides), a finished terminal or a get that exits 3 is the exited
// failure, and the deadline is the timeout failure carrying the last get
// error it holds. A cancelled ctx stops the poll as a cancellation. Every
// failure reply carries the address in Value, so the caller can close the
// terminal the start left. For the admitted primary launch profile the
// ordinary success is further qualified on the very same get record: the
// strict record metadata must name the created address and satisfy the
// shared ready policy, or the reply is the fixed protocol mismatch. No
// second observation is made, and blocked, exited, cancelled and timeout
// keep their original priority (the qualification runs only after an
// ordinary success).
func (b *backend) waitReady(ctx context.Context, call transport.Call, name, address, expectedProfile string, deadline time.Duration) transport.Reply[string] {
	deadlineAt := time.Now().Add(deadline)
	var lastErr *transport.Failure
	for {
		if err := ctx.Err(); err != nil {
			return transport.Reply[string]{Value: address, Err: &transport.Failure{Kind: transport.FailureCancelled, Message: err.Error(), Cause: err}}
		}
		if time.Until(deadlineAt) <= 0 {
			return b.timeoutReply(ctx, call, name, address, deadline, lastErr)
		}
		getCtx, cancel := context.WithTimeout(ctx, time.Until(deadlineAt))
		out, err := b.run(getCtx, call, "get", address)
		getCtxErr := getCtx.Err()
		cancel()
		if cerr := ctx.Err(); cerr != nil {
			return transport.Reply[string]{Value: address, Err: &transport.Failure{Kind: transport.FailureCancelled, Message: cerr.Error(), Cause: cerr}}
		}
		// A startup deadline interruption is not a completed get result.
		// A shorter per-call timeout leaves getCtx live and stays diagnostic.
		if getCtxErr == context.DeadlineExceeded {
			return b.timeoutReply(ctx, call, name, address, deadline, lastErr)
		}
		if err != nil {
			if notFound(err) {
				return transport.Reply[string]{Value: address, Err: exitedFailure(name, nil)}
			}
			lastErr = failure(err)
		} else {
			rep, perr := readinessReply(name, address, out)
			if rep != nil {
				// Only an ordinary success is qualified, and on the same
				// get record: a record that the strict metadata does not
				// qualify for the admitted profile is a protocol failure
				// with the known address, never a second observation.
				if rep.Err == nil && expectedProfile != "" && !primaryProfileReady(out, address) {
					return transport.Reply[string]{Value: address, Err: &transport.Failure{
						Kind:    transport.FailureProtocol,
						Message: "native primary profile readiness metadata mismatch",
					}}
				}
				return *rep
			}
			lastErr = perr
		}
		wait := startPollInterval
		if remaining := time.Until(deadlineAt); wait > remaining {
			wait = remaining
		}
		if wait <= 0 {
			return b.timeoutReply(ctx, call, name, address, deadline, lastErr)
		}
		select {
		case <-ctx.Done():
			return transport.Reply[string]{Value: address, Err: &transport.Failure{Kind: transport.FailureCancelled, Message: ctx.Err().Error(), Cause: ctx.Err()}}
		case <-time.After(wait):
		}
	}
}

// readinessReply decides one readiness get. The reply is non-nil when the
// get decides startup (ready, blocked or exited); the failure is non-nil
// when the body is not a terminal record at all (a protocol failure the
// poll retries like any other get failure); both are nil when the worker
// is simply not ready yet (working or error, a null status, not yet
// reported).
func readinessReply(name, address, out string) (*transport.Reply[string], *transport.Failure) {
	obj, ok := terminalObject(out)
	if !ok {
		return nil, &transport.Failure{Kind: transport.FailureProtocol, Message: "hc get returned no terminal record"}
	}
	if v, ok := obj.Get("state"); ok {
		if state, isString := v.(string); isString && state == "finished" {
			return &transport.Reply[string]{Value: address, Err: exitedFailure(name, obj)}, nil
		}
	}
	raw, ok := obj.Get("status")
	if !ok || raw == nil {
		return nil, nil
	}
	status, ok := raw.(*jsonjs.Object)
	if !ok {
		return nil, nil
	}
	word := ""
	if v, ok := status.Get("state"); ok {
		if s, isString := v.(string); isString {
			word = s
		}
	}
	switch word {
	case "blocked":
		message := "agent " + name + " is blocked during startup"
		if m, ok := status.Get("msg"); ok {
			if s, isString := m.(string); isString && s != "" {
				message += ": " + s
			}
		}
		return &transport.Reply[string]{Value: address, Err: &transport.Failure{Kind: transport.FailureBlocked, Message: message}}, nil
	case "idle", "done":
		reported := false
		if v, ok := obj.Get("reported"); ok {
			reported = v == true
		}
		if reported {
			return &transport.Reply[string]{Value: address, Valid: true, Process: processRecord(out)}, nil
		}
	}
	return nil, nil
}

// primaryProfileReady reports whether the get record that the ordinary
// readiness parse decided success is exactly the native primary
// observation: the record's strict fields — the launch profile label, the
// top-level reported flag, the status app, the raw native lifecycle and
// the mapped state word — qualify under launchprofile.OpenCodePrimaryReady, and
// the record's address is the created address. A malformed record, a
// foreign address or any missing or foreign field refuses the success.
func primaryProfileReady(out, address string) bool {
	obj, ok := terminalObject(out)
	if !ok || addressOf(obj) != address {
		return false
	}
	return launchprofile.OpenCodePrimaryReady(
		launchProfileOf(obj),
		reportedOf(obj),
		reporterAppOf(obj),
		nativeStateOf(obj),
		stateOf(obj),
	)
}

// exitedFailure renders the startup exit, `agent <name> exited during
// startup (exit <code>, <reason>)`, leaving out the parts hc does not
// report (a get that exits 3 knows neither).
func exitedFailure(name string, obj *jsonjs.Object) *transport.Failure {
	code, hasCode := exitCodeOf(obj)
	reason := ""
	if obj != nil {
		if v, ok := obj.Get("reason"); ok {
			if s, isString := v.(string); isString {
				reason = s
			}
		}
	}
	switch {
	case hasCode && reason != "":
		return &transport.Failure{Kind: transport.FailureCommand, Message: fmt.Sprintf("agent %s exited during startup (exit %d, %s)", name, code, reason)}
	case hasCode:
		return &transport.Failure{Kind: transport.FailureCommand, Message: fmt.Sprintf("agent %s exited during startup (exit %d)", name, code)}
	case reason != "":
		return &transport.Failure{Kind: transport.FailureCommand, Message: fmt.Sprintf("agent %s exited during startup (%s)", name, reason)}
	default:
		return &transport.Failure{Kind: transport.FailureCommand, Message: "agent " + name + " exited during startup"}
	}
}

// exitCodeOf reads the record's exit code: jsonjs decodes JSON numbers as
// float64, and an exit code is an integer of one.
func exitCodeOf(obj *jsonjs.Object) (int, bool) {
	if obj == nil {
		return 0, false
	}
	v, ok := obj.Get("exit_code")
	if !ok {
		return 0, false
	}
	f, ok := v.(float64)
	if !ok {
		return 0, false
	}
	return int(f), true
}

// timeoutReply renders the readiness deadline: `agent <name> still not
// ready after <seconds>s`, with the last get error when the poll holds one,
// then the last non-empty visible line of the terminal's screen. The screen
// is read once here, before the reply returns: the caller removes the
// terminal only after it has the reply. A failed read or a blank screen
// adds nothing. The read never sends input.
func (b *backend) timeoutReply(ctx context.Context, call transport.Call, name, address string, deadline time.Duration, lastErr *transport.Failure) transport.Reply[string] {
	message := fmt.Sprintf("agent %s still not ready after %ss", name, startDeadlineSeconds(deadline))
	var savedScreen string
	if lastErr != nil {
		message += ": " + lastErr.Error()
	}
	if ctx.Err() == nil {
		readCall := call
		if readCall.Timeout <= 0 || readCall.Timeout > timeoutScreenReadCap {
			readCall.Timeout = timeoutScreenReadCap
		}
		screen := b.Read(ctx, transport.Target{ID: address}, transport.Read{Source: "visible"}, readCall)
		if screen.Err == nil && screen.Valid {
			savedScreen = cleanTimeoutScreen(screen.Value)
			if line := lastVisibleLine(screen.Value); line != "" {
				message += fmt.Sprintf("; last visible line: %q", line)
			}
		}
	}
	return transport.Reply[string]{Value: address, Err: &transport.Failure{Kind: transport.FailureCommand, Message: message, Cause: lastErr, Screen: savedScreen}}
}

// cleanTimeoutScreen bounds the saved screen by both lines and UTF-8 bytes.
func cleanTimeoutScreen(screen string) string {
	var out strings.Builder
	for i, line := range strings.Split(screen, "\n") {
		if i >= 200 {
			break
		}
		if i > 0 {
			if out.Len() == 64*1024 {
				break
			}
			out.WriteByte('\n')
		}
		for _, r := range stripTerminalControls(line) {
			if out.Len()+len(string(r)) > 64*1024 {
				return out.String()
			}
			out.WriteRune(r)
		}
	}
	return out.String()
}

// timeoutScreenReadCap bounds the one screen read of a failing start, so a
// Core that hangs on read cannot stretch the failure far past the deadline.
const timeoutScreenReadCap = 5 * time.Second

// maxVisibleLineRunes bounds the screen line a readiness timeout quotes.
const maxVisibleLineRunes = 200

// lastVisibleLine returns the last line of a terminal screen that is not
// blank once cleaned, or "". The screen is untrusted: escape sequences and
// control characters are removed, a tab becomes a space, the line is trimmed
// and cut to maxVisibleLineRunes runes with an ellipsis when cut.
func lastVisibleLine(screen string) string {
	lines := strings.Split(screen, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(stripTerminalControls(lines[i]))
		if line == "" {
			continue
		}
		if r := []rune(line); len(r) > maxVisibleLineRunes {
			line = string(r[:maxVisibleLineRunes]) + "…"
		}
		return line
	}
	return ""
}

// stripTerminalControls drops ANSI escape sequences (CSI, OSC and DCS/SOS/PM/
// APC strings, two-character escapes with their intermediates) and every
// other control character, including the C1 range; a tab becomes a space.
// A string sequence that never terminates is dropped to its end.
func stripTerminalControls(s string) string {
	rs := []rune(s)
	var out strings.Builder
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case r == '\t':
			out.WriteByte(' ')
		case r == 0x1b || r == 0x9b || r == 0x9d || r == 0x90 || r == 0x98 || r == 0x9e || r == 0x9f:
			i = skipEscape(rs, i)
		case r < 0x20 || (r >= 0x7f && r <= 0x9f):
		default:
			out.WriteRune(r)
		}
	}
	return out.String()
}

// skipEscape returns the index of the last rune of the escape sequence that
// starts at rs[i].
func skipEscape(rs []rune, i int) int {
	intro := rs[i]
	if intro == 0x1b {
		if i+1 >= len(rs) {
			return i
		}
		switch rs[i+1] {
		case '[':
			intro = 0x9b
			i++
		case ']':
			intro = 0x9d
			i++
		case 'P', 'X', '^', '_':
			intro = 0x90
			i++
		default:
			// Intermediates (0x20-0x2f) then one final character.
			j := i + 1
			for j < len(rs) && rs[j] >= 0x20 && rs[j] <= 0x2f {
				j++
			}
			if j < len(rs) {
				return j
			}
			return len(rs) - 1
		}
	}
	switch intro {
	case 0x9b:
		// Parameters and intermediates, ended by a final byte 0x40-0x7e.
		j := i + 1
		for j < len(rs) && rs[j] >= 0x20 && rs[j] <= 0x3f {
			j++
		}
		if j < len(rs) && rs[j] >= 0x40 && rs[j] <= 0x7e {
			return j
		}
		return j - 1
	default:
		// A string sequence ends at BEL, ST (ESC \\ or U+009C) or the end.
		for j := i + 1; j < len(rs); j++ {
			if rs[j] == 0x07 || rs[j] == 0x9c {
				return j
			}
			if rs[j] == 0x1b && j+1 < len(rs) && rs[j+1] == '\\' {
				return j + 1
			}
		}
		return len(rs) - 1
	}
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

// GetGone reports whether the terminal is already gone: `hc get` exits 3,
// the record is removed — the herdr meaning, a pane that is not found. A
// finished terminal whose record still exists is not gone: it still holds
// its name and waits for its rm, so the released-panes registry keeps it.
// A live or finished terminal, or a failure for another reason, reports
// false.
func (b *backend) GetGone(ctx context.Context, target transport.Target, call transport.Call) transport.Reply[bool] {
	_, err := b.run(ctx, call, "get", addr(target))
	return transport.Reply[bool]{Value: err != nil && notFound(err), Valid: true}
}

// Wake blocks on the terminal's news — its turn ends, it blocks, it errors
// or it exits — or the bound checks in (DJA-605 stage 2c): `hc wait
// <addr> --timeout <ceil(max) in seconds>` through b.run with --json placed
// by the client, and a process budget of max + 5 s over the call's own
// budget. The reply's Value is the wake reason hc reports (status, exit,
// output, message, not_started, children), or "" when the bound expired
// with no news (hc's check-in reports ended false); a bound that is not a
// positive duration is refused before any launch, and a stdout without a
// well-formed wait result is a protocol failure.
func (b *backend) Wake(ctx context.Context, target transport.Target, max time.Duration, call transport.Call) transport.Reply[string] {
	if max <= 0 {
		return transport.Reply[string]{Err: &transport.Failure{Kind: transport.FailureProtocol, Message: "wake needs a positive bound"}}
	}
	sec := int64(math.Ceil(max.Seconds()))
	if sec < 1 {
		sec = 1
	}
	wakeCall := call
	wakeCall.Timeout = max + 5*time.Second
	out, err := b.run(ctx, wakeCall, "wait", addr(target), "--timeout", strconv.FormatInt(sec, 10))
	if err != nil {
		return transport.Reply[string]{Err: failure(err)}
	}
	reason, ok := wakeReason(out)
	if !ok {
		return transport.Reply[string]{Err: &transport.Failure{Kind: transport.FailureProtocol, Message: "hc wait returned no wait result"}}
	}
	return transport.Reply[string]{Value: reason, Valid: true}
}

// wakeReason maps one hc wait --json stdout onto the wake reason: the
// timeout check-in (ended false) is no news and maps to ""; the news
// carries hc's own reason word. A stdout that is not one well-formed
// WaitResult record is not one.
func wakeReason(stdout string) (string, bool) {
	v, err := jsonjs.Parse([]byte(stdout))
	if err != nil {
		return "", false
	}
	obj, ok := v.(*jsonjs.Object)
	if !ok {
		return "", false
	}
	endedV, okE := obj.Get("ended")
	reasonV, okR := obj.Get("reason")
	reason, isString := reasonV.(string)
	if !okE || !okR || !isString {
		return "", false
	}
	if ended, isBool := endedV.(bool); isBool && !ended {
		return "", true
	}
	return reason, true
}

// childKind is the kind field the terminal.run client requires on every
// request. A child terminal is not an agent: the field is checked for being
// present and is not sent on the wire.
const childKind = "child"

// RunChild runs run.Program as a child terminal of the caller and returns at
// once with the new terminal's address in Value: no readiness poll and no
// probe of the child. The request carries the caller (HC_TERMINAL_ID), the
// caller's soho.session label (so the child reads the same roster and
// state), and the notify and outlive flags the run names, where the zero
// values are the worker spawn's. A name that is not a valid hc terminal name
// is refused before any launch. hc's name_taken error is a FailureConflict,
// as in Start, and a run whose outcome is unknown carries that failure with
// no address.
func (b *backend) RunChild(ctx context.Context, run transport.ChildRun, call transport.Call) transport.Reply[string] {
	if ctx == nil {
		ctx = context.Background()
	}
	if !validHcName(run.Name) {
		return transport.Reply[string]{Err: &transport.Failure{
			Kind:    transport.FailureCommand,
			Message: fmt.Sprintf("terminal name '%s' is not a valid hc terminal name (1-32 of a-z 0-9 - _)", run.Name),
		}}
	}
	session, err := b.session(ctx, call)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return transport.Reply[string]{Err: &transport.Failure{Kind: transport.FailureCancelled, Message: cerr.Error(), Cause: cerr}}
		}
		return transport.Reply[string]{Err: &transport.Failure{Kind: transport.FailureUnavailable, Message: err.Error(), Cause: err}}
	}
	cwd := run.Cwd
	if cwd == "" {
		wd, err := os.Getwd()
		if err != nil {
			return transport.Reply[string]{Err: &transport.Failure{Kind: transport.FailureUnavailable, Message: "getwd: " + err.Error(), Cause: err}}
		}
		cwd = wd
	}
	budget := hcTimeoutDefault
	if call.Timeout > 0 {
		budget = call.Timeout
	}
	runCtx, runCancel := context.WithTimeout(ctx, budget)
	address, err := b.client.RunTerminal(runCtx, hc.TerminalRun{
		Name:          run.Name,
		Kind:          childKind,
		Program:       run.Program,
		Cwd:           cwd,
		Caller:        b.env.Get("HC_TERMINAL_ID"),
		Session:       session,
		Args:          run.Args,
		NotifyCaller:  run.NotifyCaller,
		EndWithCaller: !run.OutliveCaller,
		StateDir:      run.StateDir,
	})
	runCancel()
	if err != nil {
		if errors.Is(err, hc.ErrRunOutcomeUnknown) {
			return transport.Reply[string]{Err: &transport.Failure{Kind: transport.FailureRunOutcomeUnknown, Message: err.Error(), Cause: err}}
		}
		if cerr := ctx.Err(); cerr != nil {
			return transport.Reply[string]{Err: &transport.Failure{Kind: transport.FailureCancelled, Message: cerr.Error(), Cause: cerr}}
		}
		return startRunFailure(run.Name, err)
	}
	return transport.Reply[string]{Value: address, Valid: true}
}

// RemoveFinishedChild removes the record of the finished terminal that has
// exactly this name and whose parent is the caller (HC_TERMINAL_ID): one
// `hc get <name>`, then Close on its address when the record is finished and
// its parent field is the caller's. No such terminal (exit 3), a live
// terminal, a terminal of another parent and a record without a parent field
// are all Value false with no error, and nothing is closed. A get that fails
// for another reason, or a Close that fails, is the failure; the caller
// decides whether it matters.
func (b *backend) RemoveFinishedChild(ctx context.Context, name string, call transport.Call) transport.Reply[bool] {
	caller := b.env.Get("HC_TERMINAL_ID")
	if caller == "" {
		return transport.Reply[bool]{Err: &transport.Failure{Kind: transport.FailureUnavailable, Message: ErrNotInHcTerminal.Error(), Cause: ErrNotInHcTerminal}}
	}
	out, err := b.run(ctx, call, "get", name)
	if err != nil {
		if notFound(err) {
			return transport.Reply[bool]{Value: false, Valid: true}
		}
		return transport.Reply[bool]{Err: failure(err)}
	}
	obj, ok := terminalObject(out)
	if !ok {
		return transport.Reply[bool]{Err: &transport.Failure{Kind: transport.FailureProtocol, Message: "hc get returned no terminal record"}}
	}
	parent, _ := obj.Get("parent")
	if nameOf(obj) != name || nativeStateOf(obj) != "finished" || parent != caller {
		return transport.Reply[bool]{Value: false, Valid: true}
	}
	address := addressOf(obj)
	if address == "" {
		return transport.Reply[bool]{Value: false, Valid: true}
	}
	closed := b.Close(ctx, transport.Target{ID: address}, call)
	if closed.Err != nil {
		return transport.Reply[bool]{Err: closed.Err}
	}
	return transport.Reply[bool]{Value: true, Valid: true}
}
