package cli

// SOH-490: dispatch warns when a run of the caller is live in the worktree
// an edit brief is about to change. The fake Core listing below is a fake
// transport, so every selector has a discriminating case; the two hc CLI
// fixtures prove the warning is wired into dispatch and that a failed
// listing leaves the result unchanged.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/taskreport"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	"github.com/djalmajr/soho/internal/transport"
)

type liveRunListTransport struct {
	transport.Transport
	terms   []transport.Terminal
	failure *transport.Failure
	invalid bool
	lists   int
	scopes  []transport.Scope
}

func (f *liveRunListTransport) List(_ context.Context, scope transport.Scope, _ transport.Call) transport.Reply[[]transport.Terminal] {
	f.lists++
	f.scopes = append(f.scopes, scope)
	if f.failure != nil {
		return transport.Reply[[]transport.Terminal]{Err: f.failure}
	}
	return transport.Reply[[]transport.Terminal]{Value: f.terms, Valid: !f.invalid}
}

func liveRunTerm(address, name, parent, cwd string) transport.Terminal {
	return transport.Terminal{
		Target:      transport.Target{ID: address},
		Name:        name,
		NativeState: "live",
		Parent:      parent,
		ParentKnown: true,
		Cwd:         cwd,
	}
}

func TestDispatchLiveRunWarningSelection(t *testing.T) {
	root := t.TempDir()
	workerTree := filepath.Join(root, "worker")
	declaredTree := filepath.Join(root, "declared")
	outsideTree := filepath.Join(root, "outside")
	workerDeep := filepath.Join(workerTree, "deep")
	for _, dir := range []string{workerTree, declaredTree, outsideTree, workerDeep} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	roster := []core.RosterRow{{Fields: []string{"worker", "t7", "codex", "implementer"}, Valid: true}}
	declaredBrief := "# Goal\n\nEdit.\nWorktree: `" + declaredTree + "` (branch example)\n# Owned files\n\nnone\n"
	child := func(cwd string) transport.Terminal { return liveRunTerm("t9", "gate-run", "t1", cwd) }
	failure := &transport.Failure{Kind: transport.FailureUnavailable, Message: "core unreachable"}
	hostile := child(workerDeep)
	hostile.Name = "gate\r\nrun"
	hostile.Target.ID = "t9\r\nfake"
	current := child(outsideTree)
	current.CurrentCwd = workerDeep
	unknownParent := child(workerDeep)
	unknownParent.ParentKnown = false
	link := filepath.Join(root, "worker-link")
	if err := os.Symlink(workerTree, link); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, brief string
		terms       []transport.Terminal
		failure     *transport.Failure
		invalid     bool
		want        int
		wantParts   []string
	}{
		{name: "a caller live child in roster worktree", terms: []transport.Terminal{child(workerDeep)}, want: 1, wantParts: []string{"gate-run", "t9", "worktree"}},
		{name: "b caller live child in declared worktree only", brief: declaredBrief, terms: []transport.Terminal{child(filepath.Join(declaredTree, "deep"))}, want: 1, wantParts: []string{"gate-run", "t9"}},
		{name: "d finished child", terms: []transport.Terminal{func() transport.Terminal { c := child(workerDeep); c.NativeState = "finished"; return c }()}, want: 0},
		{name: "d other parent", terms: []transport.Terminal{liveRunTerm("t9", "gate-run", "t2", workerDeep)}, want: 0},
		{name: "d unknown parent", terms: []transport.Terminal{unknownParent}, want: 0},
		{name: "d roster worker", terms: []transport.Terminal{liveRunTerm("t7", "worker", "t1", workerDeep)}, want: 0},
		{name: "d wake observer", terms: []transport.Terminal{liveRunTerm("t9", "wake-abcdef", "t1", workerDeep)}, want: 0},
		{name: "d cwd outside both worktrees", terms: []transport.Terminal{child(outsideTree)}, want: 0},
		{name: "e listing failure", failure: failure, terms: []transport.Terminal{child(workerDeep)}, want: 0},
		{name: "e invalid listing", invalid: true, terms: []transport.Terminal{child(workerDeep)}, want: 0},
		{name: "hostile name and address", terms: []transport.Terminal{hostile}, want: 1, wantParts: []string{"gate  run", "t9  fake"}},
		{name: "CurrentCwd overrides Cwd", terms: []transport.Terminal{current}, want: 1},
		{name: "symlinked cwd inside worktree", terms: []transport.Terminal{child(filepath.Join(link, "deep"))}, want: 1},
		{name: "two live children get two warnings", terms: []transport.Terminal{child(workerDeep), liveRunTerm("t10", "gate-second", "t1", workerDeep)}, want: 2, wantParts: []string{"gate-run", "gate-second"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &liveRunListTransport{terms: tc.terms, failure: tc.failure, invalid: tc.invalid}
			var stderr bytes.Buffer
			oldStderr, oldFriction := platform.Stderr, frictionLogPath
			logPath := filepath.Join(t.TempDir(), "friction.log")
			platform.Stderr, frictionLogPath = &stderr, logPath
			t.Cleanup(func() { platform.Stderr, frictionLogPath = oldStderr, oldFriction })

			warnDispatchLiveRun(transport.Services{Runtime: fake}, tc.brief, roster, workerTree, "t1")

			lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")
			if stderr.String() == "" {
				lines = nil
			}
			if len(lines) != tc.want {
				t.Fatalf("warnings=%d want=%d stderr=%q", len(lines), tc.want, stderr.String())
			}
			for _, line := range lines {
				if !strings.HasPrefix(line, "soho: warning: dispatch: a run of yours is live in this worktree (") {
					t.Fatalf("not the live-run warning: %q", line)
				}
				if !strings.Contains(line, "edits now change what it tests") || !strings.Contains(line, "wait for its exit note") {
					t.Fatalf("warning lacks its guidance: %q", line)
				}
				if strings.ContainsAny(line, "\r\n") {
					t.Fatalf("warning carries raw CR/LF: %q", line)
				}
			}
			for _, part := range tc.wantParts {
				if !strings.Contains(stderr.String(), part) {
					t.Fatalf("warning %q lacks %q", stderr.String(), part)
				}
			}
			logData, err := os.ReadFile(logPath)
			if tc.want == 0 {
				if err == nil && strings.Contains(string(logData), "a run of yours is live in this worktree") {
					t.Fatalf("friction log has an unrequested warning: %q", logData)
				}
			} else {
				if err != nil {
					t.Fatalf("friction log absent: %v", err)
				}
				for _, line := range lines {
					if !strings.Contains(string(logData), strings.TrimPrefix(line, "soho: warning: ")) {
						t.Fatalf("friction log %q lacks %q", logData, line)
					}
				}
			}
			if fake.lists != 1 {
				t.Fatalf("List calls=%d want=1", fake.lists)
			}
			for _, scope := range fake.scopes {
				if !scope.All {
					t.Fatalf("List scope=%+v, want All", scope)
				}
			}
		})
	}
}

// addFakeHcListRule appends the unfiltered `hc ls` rule to an installed fake
// hc script, after the session-labelled list rule the shared hc fixtures
// install, so the narrower rule still wins for roster reads.
func addFakeHcListRule(t *testing.T, f *dispatchHcFixture, stdout string, code int, delay ...int) {
	t.Helper()
	listDelay := 0
	if len(delay) > 0 {
		listDelay = delay[0]
	}
	path := filepath.Join(f.bin, "hc.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var script struct {
		Log          string         `json:"log"`
		Rules        []fakecli.Rule `json:"rules"`
		CaptureStdin bool           `json:"capture_stdin,omitempty"`
		CaptureEnv   []string       `json:"capture_env,omitempty"`
	}
	if err := json.Unmarshal(data, &script); err != nil {
		t.Fatal(err)
	}
	script.Rules = append(script.Rules, fakecli.Rule{Argv: []string{"ls"}, ArgvPrefix: true, Stdout: stdout, Code: code, Delay: listDelay})
	updated, err := json.Marshal(script)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, updated, 0o600); err != nil {
		t.Fatal(err)
	}
}

// setLiveRunWorkerCwd puts the fixture worker's roster cwd in the root, so
// a live child of the caller in that tree is eligible for the warning.
func setLiveRunWorkerCwd(t *testing.T, f *dispatchHcFixture) {
	t.Helper()
	rosterPath := filepath.Join(f.sd, "agents.tsv")
	roster, err := os.ReadFile(rosterPath)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(roster), "\t0\t\tnow\t", "\t0\t"+f.root+"\tnow\t", 1)
	if updated == string(roster) {
		t.Fatalf("fixture roster has no empty cwd field: %q", roster)
	}
	if err := os.WriteFile(rosterPath, []byte(updated), 0o600); err != nil {
		t.Fatal(err)
	}
}

func setLiveRunWorkerKind(t *testing.T, f *dispatchHcFixture, kind string) {
	t.Helper()
	if kind == "codex" {
		return
	}
	rosterPath := filepath.Join(f.sd, "agents.tsv")
	roster := mustRead(t, rosterPath)
	updated := strings.Replace(roster, "\tcodex\t", "\t"+kind+"\t", 1)
	if updated == roster {
		t.Fatalf("fixture roster has no codex kind: %q", roster)
	}
	if err := os.WriteFile(rosterPath, []byte(updated), 0o600); err != nil {
		t.Fatal(err)
	}
}

func newLiveRunDispatchFixture(t *testing.T) *dispatchHcFixture {
	t.Helper()
	f := newDispatchHcFixture(t, append(guardProbeRules(), []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(7, "idle")},
		{Argv: []string{"send", "--if", "idle", "--json", "--", "worker"}, ArgvPrefix: true, Stdout: `{"turn":7,"started":true}`},
		{Argv: []string{"read", "worker"}, ArgvPrefix: true},
	}...))
	setLiveRunWorkerCwd(t, f)
	return f
}

func TestDispatchHcLiveRunWarningAndUnfilteredListing(t *testing.T) {
	f := newLiveRunDispatchFixture(t)
	listing := fmt.Sprintf(`{"terminals":[{"address":"t9","name":"gate-run","state":"live","parent":"t1","cwd":%q,"foreground":{"cwd":%q}}]}`, f.root, f.root)
	addFakeHcListRule(t, f, listing, 0)
	code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
	if code != 0 || dispatchHcOutputStatus(t, out) != "submitted" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	want := "soho: warning: dispatch: a run of yours is live in this worktree (gate-run, t9);"
	if !strings.Contains(stderr, want) || strings.Count(stderr, "a run of yours is live in this worktree") != 1 {
		t.Fatalf("stderr=%q, want exactly one %q", stderr, want)
	}
	calls := f.hcCalls(t)
	bareLists := 0
	for _, c := range calls {
		if len(c.Argv) > 0 && c.Argv[0] == "ls" && !containsWord(strings.Join(c.Argv, " "), "--label") {
			bareLists++
		}
	}
	if bareLists != 1 {
		t.Fatalf("unfiltered hc ls calls=%d want 1: %#v", bareLists, calls)
	}
	if len(hcSends(calls)) != 1 {
		t.Fatalf("sends=%d want 1: %#v", len(hcSends(calls)), hcSends(calls))
	}
	logData, err := os.ReadFile(filepath.Join(f.sd, "friction.log"))
	if err != nil || !strings.Contains(string(logData), strings.TrimPrefix(want, "soho: warning: ")) {
		t.Fatalf("friction log=%q err=%v, want the warning", logData, err)
	}
	f.assertHcNeverTouchedHerdr(t)
}

func TestDispatchHcLiveRunListingFailureDoesNotChangeResult(t *testing.T) {
	f := newLiveRunDispatchFixture(t)
	addFakeHcListRule(t, f, "", 1)
	code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
	if code != 0 || dispatchHcOutputStatus(t, out) != "submitted" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	if strings.Contains(stderr, "a run of yours is live in this worktree") {
		t.Fatalf("listing failure warned: %q", stderr)
	}
	if len(hcSends(f.hcCalls(t))) != 1 {
		t.Fatalf("sends=%#v", hcSends(f.hcCalls(t)))
	}
}

// liveRunEventOrder keeps only the delivery and advisory-listing calls, in
// the order the fake Core recorded them. The labelled session list is a
// different read and is left out.
func liveRunEventOrder(calls []fakecli.Call) string {
	var events []string
	for _, call := range calls {
		if len(call.Argv) == 0 {
			continue
		}
		if call.Argv[0] == "ls" {
			if containsWord(strings.Join(call.Argv, " "), "--label") {
				continue
			}
			events = append(events, "list")
			continue
		}
		if call.Argv[0] == "send" && (len(call.Argv) < 2 || call.Argv[1] != "--help") {
			events = append(events, "send")
		}
	}
	return strings.Join(events, ",")
}

// The review's slow-listing boundary: a lookup that outlasts the remaining
// --no-wait window must not consume the delivery budget, and it must happen
// before any send the dispatch makes.
func TestDispatchLiveRunReviewDeliveryBoundary(t *testing.T) {
	const busyReply = `{"error":{"kind":"status_mismatch","message":"busy"}}`
	for _, tc := range []struct {
		name                   string
		margin                 time.Duration
		listDelay, listExit    int
		sendExit               int
		sendReply, kind, flag  string
		wantCode               int
		wantEvents, wantStatus string
	}{
		{"codex-amend-fast-error", 0, 0, 1, 10, busyReply, "codex", "--amend", 4, "list,send", "error"},
		{"codex-amend-slow-error", 0, 1400, 1, 10, busyReply, "codex", "--amend", 4, "list,send", "error"},
		{"codex-amend-slow-valid", 0, 1400, 0, 10, busyReply, "codex", "--amend", 4, "list,send", "error"},
		{"codex-amend-proven", 0, 1400, 1, 0, `{"turn":8,"started":true}`, "codex", "--amend", 0, "list,send", "submitted"},
		{"pi-amend-queue-fast", 0, 0, 1, 10, busyReply, "pi", "--amend", 0, "list,send,send", "queued"},
		{"pi-amend-queue-slow", 0, 1400, 1, 10, busyReply, "pi", "--amend", 0, "list,send,send", "queued"},
		{"pi-queue-slow", 0, 1400, 1, 10, busyReply, "pi", "--queue", 0, "list,send,send", "queued"},
		{"codex-amend-default-margin", 10 * time.Second, 11500, 1, 10, busyReply, "codex", "--amend", 4, "list,send", "error"},
		{"codex-amend-default-margin-late-status-read", 10 * time.Second, 1400, 1, 10, busyReply, "codex", "--amend", 4, "list,send", "error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			list := fakecli.Rule{Argv: []string{"ls", "--json"}, Delay: tc.listDelay, Code: tc.listExit, Stdout: `{"terminals":[]}`}
			guarded := fakecli.Rule{Argv: []string{"send", "--if", "idle", "--json", "--", "worker"}, ArgvPrefix: true, Code: tc.sendExit}
			if tc.sendExit == 0 {
				guarded.Stdout = tc.sendReply
			} else {
				guarded.Stderr = tc.sendReply
			}
			rules := []fakecli.Rule{list}
			if tc.name == "codex-amend-default-margin-late-status-read" {
				rules = append(rules, fakecli.Rule{Argv: []string{"get", "worker", "--json"}, Call: 1, Delay: 10100, Stdout: hcWorkerGet(7, "idle")})
			}
			f, pointer, prior, _ := newHcAmendCeilingFixture(t, append(rules, []fakecli.Rule{
				{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(7, "idle")},
				guarded,
				{Argv: []string{"send", "--json", "--", "worker"}, ArgvPrefix: true, Stdout: `{"turn":7,"started":false}`},
			}...))
			dispatchCeilingMargin = tc.margin
			f.env["SOHO_PROMPT_CHECK_SECONDS"] = "1"
			setLiveRunWorkerCwd(t, f)
			setLiveRunWorkerKind(t, f, tc.kind)
			code, out, stderr := f.run(t, "worker", f.brief, tc.flag, "--no-wait")
			calls := f.hcCalls(t)
			events := liveRunEventOrder(calls)
			t.Logf("code=%d events=%s out=%s stderr=%q", code, events, strings.TrimSpace(out), stderr)
			if code != tc.wantCode || events != tc.wantEvents {
				t.Errorf("want exit=%d events=%s", tc.wantCode, tc.wantEvents)
			}
			if !strings.Contains(out, `"wait_status":"`+tc.wantStatus+`"`) {
				t.Errorf("want %s JSON, got %s", tc.wantStatus, out)
			}
			if tc.wantCode == 4 && tc.kind == "codex" {
				if after := mustRead(t, pointer); after != string(prior) {
					t.Errorf("prior pointer changed after the refused amendment")
				}
				if _, err := os.Stat(filepath.Join(f.sd, "wait", "worker.not-received")); !os.IsNotExist(err) {
					t.Errorf("not-received marker written for a refused amendment: %v", err)
				}
			}
		})
	}
}

// A fresh send without a started flag keeps its immediate turn-rise proof,
// even after a listing slower than the arrival budget.
func TestDispatchLiveRunReviewArrival(t *testing.T) {
	oldMargin := dispatchCeilingMargin
	dispatchCeilingMargin = 0
	t.Cleanup(func() { dispatchCeilingMargin = oldMargin })
	f := newDispatchHcFixture(t, append(guardProbeRules(), []fakecli.Rule{
		{Argv: []string{"ls", "--json"}, Delay: 1400, Code: 1},
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(8, "idle")},
		{Argv: []string{"send", "--if", "idle", "--json", "--", "worker"}, ArgvPrefix: true, Stdout: `{"turn":7,"started":false}`},
	}...))
	f.env["SOHO_PROMPT_SETTLE_SECONDS"] = "0"
	f.env["SOHO_PROMPT_CHECK_SECONDS"] = "1"
	setLiveRunWorkerCwd(t, f)
	code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
	events := liveRunEventOrder(f.hcCalls(t))
	t.Logf("code=%d events=%s out=%s stderr=%q", code, events, strings.TrimSpace(out), stderr)
	if code != 0 || events != "list,send" || dispatchHcOutputStatus(t, out) != "submitted" {
		t.Error("preflight did not preserve the immediate arrival proof")
	}
}

// A duplicate replay repeats the preflight and returns the original result
// without another send.
func TestDispatchLiveRunReviewDuplicate(t *testing.T) {
	f := newLiveRunDispatchFixture(t)
	listing := fmt.Sprintf(`{"terminals":[{"address":"t9","name":"gate-run","state":"live","parent":"t1","cwd":%q}]}`, f.root)
	addFakeHcListRule(t, f, listing, 0)
	first, _, _ := f.run(t, "worker", f.brief, "--no-wait")
	code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
	calls := f.hcCalls(t)
	lists := 0
	for _, call := range calls {
		if len(call.Argv) > 0 && call.Argv[0] == "ls" && !containsWord(strings.Join(call.Argv, " "), "--label") {
			lists++
		}
	}
	t.Logf("first=%d duplicate=%d lists=%d sends=%d out=%s stderr=%q", first, code, lists, len(hcSends(calls)), strings.TrimSpace(out), stderr)
	if first != 0 || code != 0 || liveRunEventOrder(calls) != "list,send,list" || !strings.Contains(stderr, "a run of yours") || !strings.Contains(out, `"duplicate_of":`) {
		t.Error("duplicate should repeat the preflight and observe the accepted task without another send")
	}
}

func TestDispatchLiveRunBlockingPreflight(t *testing.T) {
	f := newLiveRunDispatchFixture(t)
	listing := fmt.Sprintf(`{"terminals":[{"address":"t9","name":"gate-run","state":"live","parent":"t1","cwd":%q}]}`, f.root)
	addFakeHcListRule(t, f, listing, 0)
	oldLock, oldWait := workerLifecycleLock, dispatchWaitFor
	t.Cleanup(func() { workerLifecycleLock, dispatchWaitFor = oldLock, oldWait })
	var atLock, atWait string
	var warnedAtWait bool
	workerLifecycleLock = func(sd, agent string) (func(), error) {
		atLock = liveRunEventOrder(f.hcCalls(t))
		return oldLock(sd, agent)
	}
	dispatchWaitFor = func(_ []string, sd string, _ *core.Config, _ platform.Env, _ float64, _ bool, _ string) int {
		atWait = liveRunEventOrder(f.hcCalls(t))
		warnedAtWait = strings.Contains(platform.Stderr.(*bytes.Buffer).String(), "(gate-run, t9); edits now change what it tests")
		unlock, err := core.TryLockWorkerLifecycle(sd, "worker")
		if err != nil {
			t.Fatalf("lifecycle lock held at report wait: %v", err)
		}
		unlock()
		return 9
	}

	code, out, stderr := f.run(t, "worker", f.brief)
	t.Logf("exit=%d at lock=%q at wait=%q warning at wait=%t", code, atLock, atWait, warnedAtWait)
	if code != 9 || dispatchHcOutputStatus(t, out) != "submitted" {
		t.Fatalf("code=%d out=%s stderr=%q", code, out, stderr)
	}
	if atLock != "list" || atWait != "list,send" || !warnedAtWait {
		t.Errorf("listing must precede the lock and send, and the warning must precede the wait")
	}
}

// liveRunCeilingClock advances logical time only at distinct fake hc
// milestones read from the fixture's call log: the unfiltered advisory list
// before the shared ceiling, the first worker Get, and the first arrival Get
// after the guarded send. No wall-clock sleep drives any transition.
func liveRunCeilingClock(t *testing.T, f *dispatchHcFixture, base time.Time) func() time.Time {
	t.Helper()
	return func() time.Time {
		var listed, preGets, arrivalGets int
		sent := false
		for _, call := range f.hcCalls(t) {
			if len(call.Argv) == 0 {
				continue
			}
			switch call.Argv[0] {
			case "ls":
				labeled := false
				for _, arg := range call.Argv {
					if arg == "--label" {
						labeled = true
					}
				}
				if !labeled {
					listed++
				}
			case "get":
				if len(call.Argv) > 1 && call.Argv[1] == "worker" {
					if sent {
						arrivalGets++
					} else {
						preGets++
					}
				}
			case "send":
				for _, arg := range call.Argv {
					if arg == "--if" {
						sent = true
					}
				}
			}
		}
		spent := time.Duration(0)
		if listed > 0 {
			spent += 1600 * time.Millisecond
		}
		if preGets > 0 {
			spent += 1150 * time.Millisecond
		}
		if arrivalGets > 0 {
			spent += 850 * time.Millisecond
		}
		return base.Add(spent)
	}
}

func TestDispatchLiveRunSoftCeilingPreflight(t *testing.T) {
	// The review's unproven fresh send, scaled to a 1 s fixed margin. The
	// 1.6 s advisory list is credited before the shared ceiling starts; the
	// first worker Get consumes 1.15 s inside it, leaving 850 ms for one
	// arrival Get before the ceiling binds. Fake hc milestones drive time.
	zeroCeilingMargin(t)
	dispatchCeilingMargin = time.Second
	f := newDispatchHcFixture(t, append(guardProbeRules(), []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, Call: 1, Stdout: hcWorkerGet(7, "idle")},
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(7, "idle")},
		{Argv: []string{"send", "--if", "idle", "--json", "--", "worker"}, ArgvPrefix: true, Stdout: `{"turn":7,"started":false}`},
	}...))
	f.env["SOHO_PROMPT_SETTLE_SECONDS"], f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0", "1"
	setLiveRunWorkerCwd(t, f)
	listing := fmt.Sprintf(`{"terminals":[{"address":"t9","name":"gate-run","state":"live","parent":"t1","cwd":%q}]}`, f.root)
	addFakeHcListRule(t, f, listing, 0)
	oldCeilingNow := dispatchCeilingNow
	dispatchCeilingNow = liveRunCeilingClock(t, f, time.Now())
	t.Cleanup(func() { dispatchCeilingNow = oldCeilingNow })

	code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
	if code != 15 || dispatchHcOutputStatus(t, out) != "not-received" {
		t.Fatalf("code=%d out=%s stderr=%q", code, out, stderr)
	}
	assertNoWaitCeilingLine(t, stderr, "2", "prompt arrival", "hc get worker && hc read worker")
	calls := f.hcCalls(t)
	events := liveRunEventOrder(calls)
	if events != "list,send" || !strings.Contains(stderr, "(gate-run, t9); edits now change what it tests") {
		t.Errorf("slow listing should warn before the send: events=%q stderr=%q", events, stderr)
	}
	sent, postSendGets := false, 0
	for _, call := range calls {
		switch {
		case len(call.Argv) > 2 && call.Argv[0] == "send":
			sent = true
		case sent && len(call.Argv) > 2 && call.Argv[0] == "get" && call.Argv[1] == "worker":
			postSendGets++
		}
	}
	// The marker writer itself reads the worker once. Seeing more than one
	// read proves dispatch still had time to poll for the turn to rise.
	t.Logf("exit=%d events=%q post-send reads=%d", code, events, postSendGets)
	if postSendGets < 2 {
		t.Error("slow listing spent the arrival window before the first poll")
	}
	var result struct {
		Report     string `json:"report"`
		TaskReport string `json:"task_report"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(result.Report) != filepath.Join(f.sd, "reports") || result.TaskReport != strings.TrimSuffix(result.Report, ".md")+".current.md" {
		t.Fatalf("unexpected report paths: %+v", result)
	}
	pointer := taskreport.ReadTaskReportPointer(f.sd, "worker")
	if pointer == nil {
		t.Fatal("task pointer is missing")
	}
	current, _ := pointer.Get("current")
	task, _ := pointer.Get("task_report")
	last := strings.TrimSpace(mustRead(t, filepath.Join(f.sd, "last-report-worker")))
	if current != result.Report || task != result.TaskReport || last != result.Report {
		t.Fatalf("task pointer current=%q task_report=%q last-report=%q result=%+v", current, task, last, result)
	}
	marker := strings.Fields(mustRead(t, filepath.Join(f.sd, "wait", "worker.not-received")))
	if len(marker) != 2 || marker[1] != "7" {
		t.Fatalf("not-received marker=%v, want timestamp and turn 7", marker)
	}
	if _, err := strconv.ParseInt(marker[0], 10, 64); err != nil {
		t.Fatalf("invalid not-received timestamp: %v", err)
	}
	for _, suffix := range []string{"queued", "submit-failed"} {
		if _, err := os.Stat(filepath.Join(f.sd, "wait", "worker."+suffix)); !os.IsNotExist(err) {
			t.Errorf("unexpected %s marker or read error: %v", suffix, err)
		}
	}
}

func TestDispatchLiveRunMissingRosterSkipsListing(t *testing.T) {
	f := newLiveRunDispatchFixture(t)
	if err := os.WriteFile(f.brief, []byte("Worktree: `"+f.root+"`\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	listing := fmt.Sprintf(`{"terminals":[{"address":"t9","name":"gate-run","state":"live","parent":"t1","cwd":%q}]}`, f.root)
	addFakeHcListRule(t, f, listing, 0)
	// The existing unknown-name diagnostic lists released children for
	// valid names. An invalid name exercises the refusal without that read.
	for _, tc := range []struct{ name, events string }{{"Missing", ""}, {"missing", "list"}} {
		code, out, stderr := f.run(t, tc.name, f.brief, "--role", "implementer", "--no-wait")
		if code != 3 || out != "" || !strings.Contains(stderr, "is not in this skill's roster") {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
		if events := liveRunEventOrder(f.hcCalls(t)); events != tc.events || strings.Contains(stderr, "a run of yours") {
			t.Fatalf("missing roster row must skip the advisory: name=%q events=%q stderr=%q", tc.name, events, stderr)
		}
	}
}

func TestDispatchLiveRunPreflightInputs(t *testing.T) {
	t.Run("read-only roster role", func(t *testing.T) {
		f := newLiveRunDispatchFixture(t)
		roster := filepath.Join(f.sd, "agents.tsv")
		row := strings.Replace(mustRead(t, roster), "\tcodex\timplementer\t", "\tcodex\treviewer\t", 1)
		if err := os.WriteFile(roster, []byte(row), 0o600); err != nil {
			t.Fatal(err)
		}
		listing := fmt.Sprintf(`{"terminals":[{"address":"t9","name":"gate-run","state":"live","parent":"t1","cwd":%q}]}`, f.root)
		addFakeHcListRule(t, f, listing, 0)
		code, out, stderr := f.run(t, "worker", f.brief, "--role", "implementer", "--no-wait")
		if code != 0 || dispatchHcOutputStatus(t, out) != "submitted" || liveRunEventOrder(f.hcCalls(t)) != "send" || strings.Contains(stderr, "a run of yours") {
			t.Fatalf("code=%d out=%q stderr=%q events=%q", code, out, stderr, liveRunEventOrder(f.hcCalls(t)))
		}
	})
	t.Run("legacy transport", func(t *testing.T) {
		f := newLiveRunDispatchFixture(t)
		f.env["SOHO_TRANSPORT"] = "herdr"
		dispatchLiveRunPreflight("worker", f.brief, f.ctx, f.env, f.root)
		if calls := f.hcCalls(t); len(calls) != 0 {
			t.Fatalf("legacy preflight called Core: %v", calls)
		}
		f.assertHcNeverTouchedHerdr(t)
	})
	t.Run("missing brief uses roster cwd", func(t *testing.T) {
		f := newLiveRunDispatchFixture(t)
		listing := fmt.Sprintf(`{"terminals":[{"address":"t9","name":"gate-run","state":"live","parent":"t1","cwd":%q}]}`, f.root)
		addFakeHcListRule(t, f, listing, 0)
		code, out, stderr := f.run(t, "worker", filepath.Join(f.root, "missing.md"), "--no-wait")
		if code != 2 || out != "" || !strings.Contains(stderr, "brief not found") || !strings.Contains(stderr, "(gate-run, t9);") || liveRunEventOrder(f.hcCalls(t)) != "list" {
			t.Fatalf("code=%d out=%q stderr=%q events=%q", code, out, stderr, liveRunEventOrder(f.hcCalls(t)))
		}
	})
	t.Run("declared worktree", func(t *testing.T) {
		f := newLiveRunDispatchFixture(t)
		tree := t.TempDir()
		body := "Worktree: `" + tree + "`\n\n" + mustRead(t, f.brief)
		if err := os.WriteFile(f.brief, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		listing := fmt.Sprintf(`{"terminals":[{"address":"t9","name":"gate-run","state":"live","parent":"t1","cwd":%q}]}`, tree)
		addFakeHcListRule(t, f, listing, 0)
		code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
		if code != 0 || dispatchHcOutputStatus(t, out) != "submitted" || !strings.Contains(stderr, "(gate-run, t9);") || liveRunEventOrder(f.hcCalls(t)) != "list,send" {
			t.Fatalf("code=%d out=%q stderr=%q events=%q", code, out, stderr, liveRunEventOrder(f.hcCalls(t)))
		}
	})
}

func TestDispatchLiveRunLinkedRoleResolution(t *testing.T) {
	for _, tc := range []struct {
		name, projectMode, fallbackMode string
		wantWarning                     bool
	}{
		{"linked-project-edit", "edit", "", true},
		{"dangling-project-link-with-fallback", "", "edit", true},
		{"linked-project-read-only-shadows-edit-fallback", "read", "edit", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLiveRunDispatchFixture(t)
			const role = "bench-editor"
			roles := filepath.Join(f.root, ".agents", "soho-roles")
			if err := os.MkdirAll(roles, 0o700); err != nil {
				t.Fatal(err)
			}
			writeRole := func(path, mode string) {
				t.Helper()
				body := "---\nname: Bench Editor\nmode: " + mode + "\n---\nCustom role body.\n"
				if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			const target = "shared-role.md"
			if tc.projectMode != "" {
				writeRole(filepath.Join(roles, target), tc.projectMode)
			}
			if err := os.Symlink(target, filepath.Join(roles, role+".md")); err != nil {
				t.Fatal(err)
			}
			if tc.fallbackMode != "" {
				writeRole(filepath.Join(f.env["SOHO_ROLES"], role+".md"), tc.fallbackMode)
			}
			roster := filepath.Join(f.sd, "agents.tsv")
			row := strings.Replace(mustRead(t, roster), "\tcodex\timplementer\t", "\tcodex\t"+role+"\t", 1)
			if err := os.WriteFile(roster, []byte(row), 0o600); err != nil {
				t.Fatal(err)
			}
			listing := fmt.Sprintf(`{"terminals":[{"address":"t9","name":"gate-run","state":"live","parent":"t1","cwd":%q}]}`, f.root)
			addFakeHcListRule(t, f, listing, 0)

			code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
			if code != 0 || dispatchHcOutputStatus(t, out) != "submitted" || !strings.Contains(out, `"role":"`+role+`"`) {
				t.Fatalf("custom role dispatch: code=%d out=%s stderr=%q", code, out, stderr)
			}
			events := liveRunEventOrder(f.hcCalls(t))
			warned := strings.Contains(stderr, "a run of yours is live in this worktree (gate-run, t9);")
			t.Logf("exit=%d status=submitted role=%s events=%q warned=%t", code, role, events, warned)
			wantEvents := "send"
			if tc.wantWarning {
				wantEvents = "list,send"
			}
			if warned != tc.wantWarning || events != wantEvents {
				t.Errorf("warning=%t, want %t; events=%q, want %q; stderr=%q", warned, tc.wantWarning, events, wantEvents, stderr)
			}
		})
	}
}

// The worktree warning can precede a refused delivery.
func TestDispatchLiveRunWarnsBeforeFailedSend(t *testing.T) {
	f := newDispatchHcFixture(t, append(guardProbeRules(), []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(7, "idle")},
		{Argv: []string{"send", "--if", "idle", "--json", "--", "worker"}, ArgvPrefix: true, Code: 1, Stderr: "send did not finish\n"},
	}...))
	setLiveRunWorkerCwd(t, f)
	listing := fmt.Sprintf(`{"terminals":[{"address":"t9","name":"gate-run","state":"live","parent":"t1","cwd":%q}]}`, f.root)
	addFakeHcListRule(t, f, listing, 0)
	code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
	if code != 4 {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	if events := liveRunEventOrder(f.hcCalls(t)); events != "list,send" || !strings.Contains(stderr, "a run of yours is live in this worktree") {
		t.Fatalf("events=%q stderr=%q, want warning before failed send", events, stderr)
	}
}

// The advisory listing owns a two-second transport timeout; a slower Core
// prints nothing and leaves the accepted send alone.
func TestDispatchLiveRunListTimeoutPrintsNothing(t *testing.T) {
	f := newLiveRunDispatchFixture(t)
	listing := fmt.Sprintf(`{"terminals":[{"address":"t9","name":"gate-run","state":"live","parent":"t1","cwd":%q}]}`, f.root)
	addFakeHcListRule(t, f, listing, 0, 2200)
	code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
	if code != 0 || dispatchHcOutputStatus(t, out) != "submitted" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	if strings.Contains(stderr, "a run of yours is live in this worktree") {
		t.Fatalf("timed-out listing warned: %q", stderr)
	}
}

// A hard ceiling in the busy path still exits 10 after the single preflight.
func TestDispatchLiveRunWarnsBeforeHardCeiling(t *testing.T) {
	zeroCeilingMargin(t)
	f := newDispatchHcFixture(t, append(guardProbeRules(), []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(7, "working")},
		{Argv: []string{"send", "--if", "idle", "--json", "--", "worker"}, ArgvPrefix: true, Code: 10, Stderr: `{"error":{"kind":"status_mismatch","message":"the terminal is working"}}`},
	}...))
	f.env["SOHO_PROMPT_SETTLE_SECONDS"] = "1"
	f.env["SOHO_PROMPT_CHECK_SECONDS"] = "1"
	setLiveRunWorkerCwd(t, f)
	listing := fmt.Sprintf(`{"terminals":[{"address":"t9","name":"gate-run","state":"live","parent":"t1","cwd":%q}]}`, f.root)
	addFakeHcListRule(t, f, listing, 0)
	code, _, stderr := f.run(t, "worker", f.brief, "--no-wait")
	if code != 10 {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	assertNoWaitCeilingLine(t, stderr, "2", "worker to become idle", "hc get worker && hc read worker")
	if events := liveRunEventOrder(f.hcCalls(t)); events != "list,send" || !strings.Contains(stderr, "a run of yours is live in this worktree") {
		t.Fatalf("events=%q stderr=%q, want one warning before hard ceiling", events, stderr)
	}
}

// Names and addresses are data on one line: printable Unicode and spaces
// stay, controls become spaces, and more than 120 runes are cut with an
// ellipsis.
func TestDispatchLiveRunReviewDisplayName(t *testing.T) {
	for _, tc := range []struct {
		label, name, address, wantName, wantAddress string
	}{
		{"mixed", "verificação 日本語", "t9", "verificação 日本語", "t9"},
		{"all-unicode", "日本語", "t9", "日本語", "t9"},
		{"controls", "gate\r\n\trun", "t9\r\nfake", "gate   run", "t9  fake"},
		{"long-unicode-name", strings.Repeat("界", 200), "t９", strings.Repeat("界", 120) + "…", "t９"},
		{"long-hostile-address", "gate-run", "t" + strings.Repeat("a", 199) + "\r\nz", "gate-run", "t" + strings.Repeat("a", 119) + "…"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			f := newLiveRunDispatchFixture(t)
			nameJSON, err := json.Marshal(tc.name)
			if err != nil {
				t.Fatal(err)
			}
			addressJSON, err := json.Marshal(tc.address)
			if err != nil {
				t.Fatal(err)
			}
			listing := fmt.Sprintf(`{"terminals":[{"address":%s,"name":%s,"state":"live","parent":"t1","cwd":%q}]}`, addressJSON, nameJSON, f.root)
			addFakeHcListRule(t, f, listing, 0)
			code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
			if code != 0 || dispatchHcOutputStatus(t, out) != "submitted" {
				t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
			}
			entries, err := os.ReadFile(filepath.Join(f.sd, "friction.log"))
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("requested=%q/%q stderr=%q friction=%q", tc.name, tc.address, stderr, entries)
			want := "(" + tc.wantName + ", " + tc.wantAddress + ")"
			if !strings.Contains(stderr, want) || strings.Count(stderr, "\n") != 1 || strings.Contains(stderr, "\r") || !strings.Contains(string(entries), want) {
				t.Errorf("wanted one warning and one friction entry naming %q", want)
			}
		})
	}
}
