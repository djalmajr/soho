package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

type dispatchArrivalFixture struct {
	root   string
	state  string
	bin    string
	env    platform.Env
	brief  string
	config *core.Config
}

func newDispatchArrivalFixture(t *testing.T, initialMode string, initialSeq, nextSeq int, recent string, settle string, extra ...fakecli.Rule) *dispatchArrivalFixture {
	t.Helper()
	root := t.TempDir()
	if recent == "$CURRENT_PATHS" {
		recent = dispatchPromptEvidence(root)
	}
	recent = strings.ReplaceAll(recent, "$ROOT", root)
	state, roles, bin := filepath.Join(root, "state"), filepath.Join(root, "roles"), filepath.Join(root, "bin")
	for _, dir := range []string{filepath.Join(state, "ws", "wait"), filepath.Join(state, "ws", "briefs"), filepath.Join(state, "ws", "reports"), roles, bin} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(roles, "implementer.md"), []byte("---\nname: Implementer\nmode: edit\n---\nRole body.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	brief := filepath.Join(root, "brief.md")
	if err := os.WriteFile(brief, []byte("# Goal\n\nDispatch a prompt.\n# Expected result\n\nObserved.\n# Owned files\n\nnone\n# Forbidden\n\nNo commit or push.\n# Report\n\ndone\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\tmode\targs\teffort\nworker\tw0test:p0a\tcodex\timplementer\topenai\t0\t\tnow\tgpt-5\ttask\timplementer\t\t\t\thigh\n"
	if err := os.WriteFile(filepath.Join(state, "ws", "agents.tsv"), []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	stateJSON := func(mode string, seq int) string {
		return `{"result":{"agent":{"agent_status":"` + mode + `","state_change_seq":` + strings.TrimSpace(string(rune('0'+seq))) + `}}}`
	}
	for i := range extra {
		if extra[i].Stdout == "$CURRENT_PATHS" {
			extra[i].Stdout = dispatchPromptEvidence(root)
		}
		extra[i].Stdout = strings.ReplaceAll(extra[i].Stdout, "$ROOT", root)
	}
	rules := []fakecli.Rule{
		{Argv: []string{"agent", "list"}, ArgvPrefix: true, Stdout: `{"result":{"agents":[]}}`},
	}
	rules = append(rules, extra...)
	// Agent get call 1 is the pre-send state; call 2 is the first arrival probe.
	rules = append(rules,
		fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: stateJSON(initialMode, initialSeq)},
		fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Call: 2, Stdout: stateJSON(initialMode, nextSeq)},
	)
	rules = append(rules,
		fakecli.Rule{Argv: []string{"agent", "get", "worker"}, ArgvPrefix: true, Stdout: stateJSON(initialMode, nextSeq)},
		fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, ArgvPrefix: true},
		fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: recent},
		fakecli.Rule{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"submitted":true}}`},
		fakecli.Rule{Argv: []string{"pane", "title"}, ArgvPrefix: true, Code: 1},
	)
	if _, err := fakecli.Install(t, bin, "herdr", rules); err != nil {
		t.Fatal(err)
	}
	entries := fakecli.Env(testutil.CleanEnv(t), bin)
	env := platform.Env{}
	for _, entry := range entries {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			env[key] = value
		}
	}
	env["HERDR_ENV"], env["SOHO_DIR"], env["HERDR_WORKSPACE_ID"] = "1", state, "ws"
	env["SOHO_ROLES"], env["SOHO_SKILL_DIR"] = roles, "../../skills/soho"
	env["SOHO_PROMPT_CHECK_SECONDS"], env["SOHO_PROMPT_SETTLE_SECONDS"] = "1", settle
	env["SOHO_BRIEF_LINT"], env["TMPDIR"] = "off", root
	env["HERDR_SOCKET_PATH"] = filepath.Join(root, "missing.sock")
	return &dispatchArrivalFixture{root: root, state: state, bin: bin, env: env, brief: brief, config: &core.Config{Entries: map[string]core.ConfigEntry{}}}
}

func (f *dispatchArrivalFixture) run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, stderr bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &stderr
	code := func() int {
		defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
		return Run(append([]string{"dispatch"}, args...), f.env)
	}()
	return code, out.String(), stderr.String()
}

func dispatchPromptEvidence(root string) string {
	var out strings.Builder
	base := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 80; i++ {
		stamp := base.Add(time.Duration(i*2) * time.Second).Format("20060102T150405")
		fmt.Fprintf(&out, "Read the file %s in full\n", filepath.Join(root, "state", "ws", "briefs", "worker-"+stamp+".md"))
	}
	return out.String()
}

func dispatchOutputStatus(t *testing.T, out string) string {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &value); err != nil {
		t.Fatalf("dispatch JSON %q: %v", out, err)
	}
	status, _ := value["wait_status"].(string)
	return status
}

// pinComposedStamp pins platform.Now to a real-time clock starting at
// 2026-09-29T00:00:00 UTC and returns the composed brief's stamp, stable for
// at least 300 ms ahead: the dispatch that runs right after composes
// worker-<stamp>.md. The clock must keep advancing for the wait deadlines —
// a fixed clock spins the arrival loops forever.
func pinComposedStamp(t *testing.T) string {
	t.Helper()
	oldNow := platform.Now
	base := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
	began := time.Now()
	platform.Now = func() time.Time { return base.Add(time.Since(began)) }
	t.Cleanup(func() { platform.Now = oldNow })
	stamp := ""
	for {
		cur := core.NowStamp(platform.Now())
		if cur == stamp && core.NowStamp(platform.Now().Add(300*time.Millisecond)) == stamp {
			return stamp
		}
		stamp = cur
		time.Sleep(time.Millisecond)
	}
}

func TestDispatchArrivalPortedCases(t *testing.T) {
	fixedNow := platform.Now
	clock := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
	platform.Now = func() time.Time {
		value := clock
		clock = clock.Add(2 * time.Second)
		return value
	}
	t.Cleanup(func() { platform.Now = fixedNow })
	t.Run(`arrival: working target skips settle and reports queued from the prompt marker`, func(t *testing.T) {
		// JS: "arrival: working target skips settle and reports queued from the prompt marker"
		f := newDispatchArrivalFixture(t, "working", 5, 5, "$CURRENT_PATHS", "20")
		code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
		if code != 0 || dispatchOutputStatus(t, out) != "queued" || !strings.Contains(errText, "prompt queued") {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
		}
		if _, err := os.Stat(filepath.Join(f.state, "ws", "wait", "worker.queued")); err != nil {
			t.Fatalf("queued marker missing: %v", err)
		}
		if !strings.Contains(mustRead(t, filepath.Join(f.state, "ws", "briefs", "worker-20260929T000000.dispatch.json")), `"arrival":"queued"`) {
			t.Fatal("sidecar did not record queued arrival")
		}
	})
	t.Run(`arrival: working target with no prompt evidence is not-received and sends no key`, func(t *testing.T) {
		// JS: "arrival: working target with no prompt evidence is not-received and sends no key"
		f := newDispatchArrivalFixture(t, "working", 5, 5, "old prompt from another dispatch\n", "0")
		code, out, _ := f.run(t, "worker", f.brief, "--no-wait")
		if code != 15 || dispatchOutputStatus(t, out) != "not-received" {
			t.Fatalf("code=%d out=%s", code, out)
		}
		calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		for _, call := range calls {
			if len(call.Argv) > 1 && call.Argv[1] == "send-keys" {
				t.Fatalf("occupied target got a key: %#v", call.Argv)
			}
		}
	})
	t.Run(`arrival: a working target with a truncated queued prompt counts as queued`, func(t *testing.T) {
		// The queue shows the dispatched prompt with the leading directories
		// clipped: the file's exact name stays whole and proves the prompt.
		// The old generic fragment (a shared directory plus a name prefix)
		// no longer proves it.
		stamp := pinComposedStamp(t)
		f := newDispatchArrivalFixture(t, "working", 5, 5, "old work output\nRead the file '.../briefs/worker-"+stamp+".md…' in full\n", "0")
		code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
		if code != 0 || dispatchOutputStatus(t, out) != "queued" || !strings.Contains(errText, "prompt queued: 'worker' is working") {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
		}
		if _, err := os.Stat(filepath.Join(f.state, "ws", "wait", "worker.queued")); err != nil {
			t.Fatalf("queued marker missing: %v", err)
		}
		sidecars, err := filepath.Glob(filepath.Join(f.state, "ws", "briefs", "worker-*.dispatch.json"))
		if err != nil || len(sidecars) == 0 {
			t.Fatalf("dispatch sidecar missing: %v", err)
		}
		if !strings.Contains(mustRead(t, sidecars[0]), `"arrival":"queued"`) {
			t.Fatalf("sidecar did not record queued arrival: %s", mustRead(t, sidecars[0]))
		}
	})
	t.Run(`arrival: a working target with another dispatch's queued prompt stays not-received`, func(t *testing.T) {
		f := newDispatchArrivalFixture(t, "working", 5, 5, "old work output\nRead the file /…/briefs/review-20260928T090000.md in full\n", "0")
		code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
		if code != 15 || dispatchOutputStatus(t, out) != "not-received" || !strings.Contains(errText, "was typed while it was working") {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
		}
		if _, err := os.Stat(filepath.Join(f.state, "ws", "wait", "worker.not-received")); err != nil {
			t.Fatalf("not-received marker missing: %v", err)
		}
	})
	t.Run(`arrival: a working target with a generic or old queue line stays not-received`, func(t *testing.T) {
		// The queue shows only lines from other dispatches: the queue chrome
		// without a file, a shared directory plus a name prefix (the old
		// generic fragment), and an old brief's clipped name. None of them
		// identifies this prompt, so the delivery stays uncertain: no queued
		// receipt, no key, no resend.
		pinComposedStamp(t)
		recent := "old work output\n" +
			"Steering: Read the file /var/folders/f2/r857c16x45z6p82wsq_0d_...\n" +
			"\t↳ Read the file in full and execute it\n" +
			"Read the file '.../briefs/work…' in full\n" +
			"Read the file '.../briefs/worker-19990101T000000.md…' in full\n"
		f := newDispatchArrivalFixture(t, "working", 5, 5, recent, "0")
		code, out, _ := f.run(t, "worker", f.brief, "--no-wait")
		if code != 15 || dispatchOutputStatus(t, out) != "not-received" {
			t.Fatalf("code=%d out=%s", code, out)
		}
		calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		for _, call := range calls {
			if len(call.Argv) > 1 && call.Argv[1] == "send-keys" {
				t.Fatalf("occupied target got a key: %#v", call.Argv)
			}
		}
	})
	t.Run(`arrival: a working target with a longer same-prefix filename stays not-received`, func(t *testing.T) {
		// The recent screen shows this prompt's path as a prefix of a longer
		// file name: the entrypoint whole-path check must not prove the
		// shorter composed path from the longer one.
		stamp := pinComposedStamp(t)
		recent := "old work output\nRead the file $ROOT/state/ws/briefs/worker-" + stamp + ".md-other in full\n"
		f := newDispatchArrivalFixture(t, "working", 5, 5, recent, "0")
		code, out, _ := f.run(t, "worker", f.brief, "--no-wait")
		if code != 15 || dispatchOutputStatus(t, out) != "not-received" {
			t.Fatalf("code=%d out=%s", code, out)
		}
	})
	t.Run(`arrival: a working target with a longer brief-suffix filename stays not-received`, func(t *testing.T) {
		// The shared rule also receives normal .md composed names. Adding
		// .brief.md creates another filename, not an alternative identity.
		stamp := pinComposedStamp(t)
		recent := "old work output\nRead the file $ROOT/state/ws/briefs/worker-" + stamp + ".md.brief.md in full\n"
		f := newDispatchArrivalFixture(t, "working", 5, 5, recent, "0")
		code, out, _ := f.run(t, "worker", f.brief, "--no-wait")
		if code != 15 || dispatchOutputStatus(t, out) != "not-received" {
			t.Fatalf("code=%d out=%s", code, out)
		}
		calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		for _, call := range calls {
			if len(call.Argv) > 1 && call.Argv[1] == "send-keys" {
				t.Fatalf("occupied target got a key: %#v", call.Argv)
			}
		}
	})
	t.Run(`arrival: a working target with quoted prose naming a longer same-prefix file stays not-received`, func(t *testing.T) {
		// Quoted prose that names a longer same-prefix file is not this
		// prompt's queue or path evidence.
		stamp := pinComposedStamp(t)
		recent := "old work output\nThe note says: \"Read the file $ROOT/state/ws/briefs/worker-" + stamp + ".md-other in full and execute it\"\n"
		f := newDispatchArrivalFixture(t, "working", 5, 5, recent, "0")
		code, out, _ := f.run(t, "worker", f.brief, "--no-wait")
		if code != 15 || dispatchOutputStatus(t, out) != "not-received" {
			t.Fatalf("code=%d out=%s", code, out)
		}
	})
	t.Run(`arrival: a working target with quoted prose naming the exact full path stays not-received`, func(t *testing.T) {
		// Quoted prose that names this prompt's exact full path is not a
		// prompt or queue line: the entrypoint must not prove the prompt
		// from a mention of it.
		stamp := pinComposedStamp(t)
		recent := "old work output\nThe note says: \"Read the file $ROOT/state/ws/briefs/worker-" + stamp + ".md in full and execute it\"\n"
		f := newDispatchArrivalFixture(t, "working", 5, 5, recent, "0")
		code, out, _ := f.run(t, "worker", f.brief, "--no-wait")
		if code != 15 || dispatchOutputStatus(t, out) != "not-received" {
			t.Fatalf("code=%d out=%s", code, out)
		}
		calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		for _, call := range calls {
			if len(call.Argv) > 1 && call.Argv[1] == "send-keys" {
				t.Fatalf("occupied target got a key: %#v", call.Argv)
			}
		}
	})
	t.Run(`arrival: a working target with quoted prose naming the exact clipped file stays not-received`, func(t *testing.T) {
		// Quoted prose carrying the exact clipped basename is not a queue
		// line: the queue rule must not prove the prompt from a mention of it.
		stamp := pinComposedStamp(t)
		recent := "old work output\nThey asked: \"Read the file '.../briefs/worker-" + stamp + ".md…' in full\"\n"
		f := newDispatchArrivalFixture(t, "working", 5, 5, recent, "0")
		code, out, _ := f.run(t, "worker", f.brief, "--no-wait")
		if code != 15 || dispatchOutputStatus(t, out) != "not-received" {
			t.Fatalf("code=%d out=%s", code, out)
		}
	})
	t.Run(`arrival: amend ignores a generic marker from the previous prompt`, func(t *testing.T) {
		// JS: "arrival: amend ignores a generic marker from the previous prompt"
		f := newDispatchArrivalFixture(t, "working", 5, 5, "Read the file /tmp/old/previous-brief.md in full\n", "0")
		previous := filepath.Join(f.state, "ws", "reports", "previous.md")
		if err := os.WriteFile(previous, []byte("old report\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.state, "ws", "last-report-worker"), []byte(previous+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, _ := f.run(t, "worker", f.brief, "--amend", "--no-wait")
		if code != 15 || dispatchOutputStatus(t, out) != "not-received" {
			t.Fatalf("code=%d out=%s", code, out)
		}
	})
	t.Run(`arrival: s1 control with no prompt evidence stays not-received`, func(t *testing.T) {
		// JS: "arrival: s1 control with no prompt evidence stays not-received"
		f := newDispatchArrivalFixture(t, "working", 5, 5, "old work output without a prompt marker\n", "0")
		code, out, _ := f.run(t, "worker", f.brief, "--no-wait")
		if code != 15 || dispatchOutputStatus(t, out) != "not-received" {
			t.Fatalf("code=%d out=%s", code, out)
		}
	})
	t.Run(`arrival: working target with a moved seq counts as received`, func(t *testing.T) {
		// JS: "arrival: working target with a moved seq counts as received"
		f := newDispatchArrivalFixture(t, "working", 5, 6, "", "0")
		code, out, _ := f.run(t, "worker", f.brief, "--no-wait")
		if code != 0 || dispatchOutputStatus(t, out) != "submitted" {
			t.Fatalf("code=%d out=%s", code, out)
		}
	})
	t.Run(`arrival: working target that becomes idle after its seq moves remains queued`, func(t *testing.T) { // Mutation captured: treating any moved seq as arrival reports submitted after the previous turn ends.
		f := newDispatchArrivalFixture(t, "working", 5, 6, "$CURRENT_PATHS", "0",
			fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Call: 2, Stdout: `{"result":{"agent":{"agent_status":"idle","state_change_seq":6}}}`},
			fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Call: 3, Stdout: `{"result":{"agent":{"agent_status":"idle","state_change_seq":6}}}`},
		)
		code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
		if code != 0 || dispatchOutputStatus(t, out) != "queued" || !strings.Contains(stderr, "prompt queued") {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, stderr)
		}
	})
	t.Run(`arrival: an input-box prompt sends Enter before stale auth becomes not-received`, func(t *testing.T) { // Mutation captured: checking stale auth before the input box omits the required Enter.
		// Adapted contract: the first Enter needs a recognized composer
		// holding the exact composed path. The screen is the codex composer
		// with the exact path plus the auth error line, and the auth cause
		// stays stale (present before the send): the Enter is sent, the
		// arrival never comes, and the dispatch ends not-received after the
		// Enter — never "after its block on a provider auth error".
		oldNow := platform.Now
		base := time.Date(2026, 9, 29, 12, 0, 0, 0, time.Local)
		began := time.Now()
		platform.Now = func() time.Time { return base.Add(time.Since(began)) }
		t.Cleanup(func() { platform.Now = oldNow })
		composedName := func() string {
			stamp := ""
			for {
				cur := core.NowStamp(platform.Now())
				if cur != stamp {
					stamp = cur
					continue
				}
				if core.NowStamp(platform.Now().Add(300*time.Millisecond)) == stamp {
					return filepath.Join("$ROOT", "state", "ws", "briefs", "worker-"+stamp+".md")
				}
				time.Sleep(time.Millisecond)
			}
		}
		screen := "Error: Incorrect API key\n› Read the file " + composedName() + " in full and execute it.\n"
		f := newDispatchArrivalFixture(t, "blocked", 7, 7, "Error: Incorrect API key\n", "0",
			fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Call: 1, ArgvPrefix: true, Stdout: "boot\n"},
			fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, ArgvPrefix: true, Stdout: screen},
			fakecli.Rule{Argv: []string{"agent", "send-keys", "worker", "enter"}, ArgvPrefix: true},
		)
		code, _, stderr := f.run(t, "worker", f.brief, "--no-wait")
		calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if code != 15 || !strings.Contains(stderr, "sat in the input box; sent Enter") || !strings.Contains(stderr, "after an Enter") || strings.Contains(stderr, "after its block on a provider auth error") {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		if got := countDispatchCalls(calls, "send-keys"); got != 1 {
			t.Fatalf("send-keys calls=%d, want 1; calls=%#v", got, calls)
		}
	})
	t.Run(`arrival (a): idle target with changing screen, loses both prompts -> 1 resend and not-received exit 15`, func(t *testing.T) {
		// JS: "arrival (a): idle target with changing screen, loses both prompts -> 1 resend and not-received exit 15"
		f := newDispatchArrivalFixture(t, "idle", 1, 1, "old output\n", "0",
			fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Call: 1, Stdout: "boot before\n", ArgvPrefix: true},
			fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Call: 2, Stdout: "boot redraw\n", ArgvPrefix: true},
			fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, ArgvPrefix: true, Stdout: "›\n"},
		)
		code, out, _ := f.run(t, "worker", f.brief, "--no-wait")
		if code != 15 || dispatchOutputStatus(t, out) != "not-received" {
			t.Fatalf("code=%d out=%s", code, out)
		}
		calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if got := countDispatchCalls(calls, "prompt"); got != 2 {
			t.Fatalf("prompt attempts=%d, want initial + one resend", got)
		}
	})
	t.Run(`arrival (b): idle target receives resend (seq changes) -> received with 1 resend`, func(t *testing.T) {
		// JS: "arrival (b): idle target receives resend (seq changes) -> received with 1 resend"
		// JS: "dispatch: an ignored prompt is resent once and the JSON carries resent"
		f := newDispatchArrivalFixture(t, "idle", 1, 1, "old output\n", "0",
			fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Call: 1, Stdout: "screen before\n", ArgvPrefix: true},
			fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Call: 2, Stdout: "screen redraw\n", ArgvPrefix: true},
			fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Call: 3, Stdout: "›\n", ArgvPrefix: true},
			fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Call: 4, Stdout: "›\n", ArgvPrefix: true},
			fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, ArgvPrefix: true, Stdout: "›\n"},
			fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Call: 3, Stdout: `{"result":{"agent":{"agent_status":"idle","state_change_seq":1}}}`},
			fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Call: 4, Stdout: `{"result":{"agent":{"agent_status":"idle","state_change_seq":1}}}`},
			fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Call: 5, Stdout: `{"result":{"agent":{"agent_status":"idle","state_change_seq":1}}}`},
			fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Call: 6, Stdout: `{"result":{"agent":{"agent_status":"working","state_change_seq":2}}}`},
		)
		code, out, _ := f.run(t, "worker", f.brief, "--no-wait")
		if code != 0 || dispatchOutputStatus(t, out) != "submitted" || !strings.Contains(out, `"resent":true`) {
			calls, _ := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
			config, _ := os.ReadFile(filepath.Join(f.bin, "herdr.json"))
			t.Fatalf("code=%d out=%s calls=%#v config=%s", code, out, calls, config)
		}
		calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if got := countDispatchCalls(calls, "prompt"); got != 2 {
			t.Fatalf("prompt attempts=%d, want 2", got)
		}
	})
	t.Run(`arrival (c): normal target turns working with changed seq on first prompt -> received without resend`, func(t *testing.T) {
		// JS: "arrival (c): normal target turns working with changed seq on first prompt -> received without resend"
		// JS: "dispatch: arrival confirmed by the state turning working → no resend"
		f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0",
			fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Call: 2, Stdout: `{"result":{"agent":{"agent_status":"working","state_change_seq":2}}}`},
		)
		code, out, _ := f.run(t, "worker", f.brief, "--no-wait")
		if code != 0 || dispatchOutputStatus(t, out) != "submitted" {
			t.Fatalf("code=%d out=%s", code, out)
		}
		calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if got := countDispatchCalls(calls, "prompt"); got != 1 {
			t.Fatalf("prompt attempts=%d, want 1", got)
		}
	})
	t.Run(`arrival (d): settle wait waits for interactive_ready and two identical visible screens before prompt 1, respects settle=0`, func(t *testing.T) {
		// JS: "arrival (d): settle wait waits for interactive_ready and two identical visible screens before prompt 1, respects settle=0"
		f := newDispatchArrivalFixture(t, "idle", 1, 1, "", "1",
			fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Call: 2, Stdout: `{"result":{"agent":{"agent_status":"idle","state_change_seq":1,"interactive_ready":true}}}`},
			fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Call: 3, Stdout: `{"result":{"agent":{"agent_status":"idle","state_change_seq":1}}}`},
			fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Call: 4, Stdout: `{"result":{"agent":{"agent_status":"working","state_change_seq":2}}}`},
		)
		code, out, _ := f.run(t, "worker", f.brief, "--no-wait")
		if code != 0 || dispatchOutputStatus(t, out) != "submitted" {
			t.Fatalf("code=%d out=%s", code, out)
		}
		calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		promptAt, readsBefore := -1, 0
		for i, call := range calls {
			if len(call.Argv) > 1 && call.Argv[1] == "prompt" {
				promptAt = i
				break
			}
		}
		for _, call := range calls[:promptAt] {
			if len(call.Argv) > 1 && call.Argv[1] == "read" {
				readsBefore++
			}
		}
		if readsBefore < 2 {
			t.Fatalf("visible settle reads before prompt=%d, want at least 2", readsBefore)
		}
		zero := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0",
			fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Call: 2, Stdout: `{"result":{"agent":{"agent_status":"working","state_change_seq":2}}}`},
		)
		code, out, _ = zero.run(t, "worker", zero.brief, "--no-wait")
		if code != 0 || dispatchOutputStatus(t, out) != "submitted" {
			t.Fatalf("settle=0 code=%d out=%s", code, out)
		}
		zeroCalls, err := fakecli.ReadCalls(filepath.Join(zero.bin, "herdr.calls.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		zeroPromptAt, zeroReads := -1, 0
		for i, call := range zeroCalls {
			if len(call.Argv) > 1 && call.Argv[1] == "prompt" {
				zeroPromptAt = i
				break
			}
		}
		for _, call := range zeroCalls[:zeroPromptAt] {
			if len(call.Argv) > 1 && call.Argv[1] == "read" && len(call.Argv) > 4 && call.Argv[4] == "visible" {
				zeroReads++
			}
		}
		if zeroReads != 1 {
			t.Fatalf("visible reads before prompt with settle=0=%d, want 1", zeroReads)
		}
	})
	t.Run(`arrival (e): screen changed and composed prompt path visible outside last 3 lines -> received without resend`, func(t *testing.T) {
		// JS: "arrival (e): screen changed and composed prompt path visible outside last 3 lines -> received without resend"
		// The frozen title records the legacy contract. A history echo alone
		// now keeps delivery uncertain, with no Enter or repeated prompt.
		f := newDispatchArrivalFixture(t, "idle", 1, 1, "$CURRENT_PATHS", "0",
			fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Call: 1, Stdout: "boot\n", ArgvPrefix: true},
			fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Call: 2, Stdout: "screen changed\n", ArgvPrefix: true},
		)
		code, out, _ := f.run(t, "worker", f.brief, "--no-wait")
		if code != 15 || dispatchOutputStatus(t, out) != "not-received" {
			t.Fatalf("code=%d out=%s", code, out)
		}
		calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if got := countDispatchCalls(calls, "prompt"); got != 1 {
			t.Fatalf("prompt attempts=%d, want no resend", got)
		}
	})
	t.Run(`arrival (f): working target with prompt visible is queued without Enter or resend, including amend`, func(t *testing.T) {
		// JS: "arrival (f): working target with prompt visible is queued without Enter or resend, including amend"
		f := newDispatchArrivalFixture(t, "working", 5, 5, "$CURRENT_PATHS", "0")
		code, out, _ := f.run(t, "worker", f.brief, "--no-wait")
		if code != 0 || dispatchOutputStatus(t, out) != "queued" {
			t.Fatalf("code=%d out=%s", code, out)
		}
		code, out, _ = f.run(t, "worker", f.brief, "--amend", "--no-wait")
		if code != 0 || dispatchOutputStatus(t, out) != "queued" {
			t.Fatalf("amend code=%d out=%s", code, out)
		}
		calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if got := countDispatchCalls(calls, "prompt"); got != 2 {
			t.Fatalf("prompt attempts=%d, want one per dispatch", got)
		}
	})
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func countDispatchCalls(calls []fakecli.Call, command string) int {
	count := 0
	for _, call := range calls {
		if len(call.Argv) > 1 && call.Argv[0] == "agent" && call.Argv[1] == command {
			count++
		}
	}
	return count
}

// TestDispatchWorkingTargetTakesThePromptAfterItsTurn covers a CLI (Cursor)
// that holds a prompt sent during a turn in a queue it does not show: the
// check window sees no evidence, the turn ends later, and the target then
// starts the prompt. That is not a lost prompt.
func TestDispatchWorkingTargetTakesThePromptAfterItsTurn(t *testing.T) {
	state := func(mode string, seq int) string {
		return fmt.Sprintf(`{"result":{"agent":{"agent_status":"%s","state_change_seq":%d}}}`, mode, seq)
	}
	get := []string{"agent", "get", "worker"}
	var extra []fakecli.Rule
	for call := 1; call <= 10; call++ {
		extra = append(extra, fakecli.Rule{Argv: get, Call: call, Stdout: state("working", 5)})
	}
	extra = append(extra,
		fakecli.Rule{Argv: get, Call: 11, Stdout: state("idle", 6)},
		fakecli.Rule{Argv: get, Stdout: state("working", 7)},
	)
	f := newDispatchArrivalFixture(t, "working", 5, 5, "old work output\n", "10", extra...)
	f.env["SOHO_WAIT_POLL_MS"] = "500"
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	if code != 0 || dispatchOutputStatus(t, out) != "submitted" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if strings.Contains(errText, "was typed while it was working") {
		t.Fatalf("a prompt taken after the turn was reported unconfirmed: %s", errText)
	}
}

// TestDispatchWorkingTargetStillBusyAfterTheSettleWindow keeps the old result
// when the turn does not end within prompt_settle_seconds.
func TestDispatchWorkingTargetStillBusyAfterTheSettleWindow(t *testing.T) {
	f := newDispatchArrivalFixture(t, "working", 5, 5, "old work output\n", "2")
	f.env["SOHO_WAIT_POLL_MS"] = "500"
	start := time.Now()
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	if code != 15 || dispatchOutputStatus(t, out) != "not-received" || !strings.Contains(errText, "was typed while it was working") {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	// The marker carries this prompt's path, so a later wait sends Enter only
	// when the prompt sits in the input, not for the path in the scrollback.
	marker := strings.Fields(mustRead(t, filepath.Join(f.state, "ws", "wait", "worker.not-received")))
	if len(marker) != 3 || !strings.HasPrefix(marker[2], filepath.Join(f.state, "ws", "briefs", "worker-")) {
		t.Fatalf("not-received marker without the prompt path: %q", marker)
	}
	// The verdict comes only after the turn wait (prompt_settle_seconds=2).
	if elapsed := time.Since(start); elapsed < 2*time.Second {
		t.Fatalf("not-received after %v, before the 2 s turn wait", elapsed)
	}
}

// altScreenVisible is the visible screen opencode really shows while it works
// on an amendment (pinar: an amendment to a working opencode came back
// not-received while the pane showed the prompt): the prompt sits in its own
// box, wrapped after the agent name, so the stamp is on the next line.
func altScreenVisible(stamp string) string {
	briefs := filepath.Join("$ROOT", "state", "ws", "briefs") + string(filepath.Separator)
	return "  ┃  [✓] earlier step\n" +
		"     ▣  Build · Qwen3.8-27B NVFP4 (ai01) · 23m 14s\n" +
		"  ┃\n" +
		"  ┃  Read the file " + briefs + "worker-\n" +
		"  ┃  " + stamp + ".md in full and execute it. It amends the brief you are working on. When\n" +
		"  ┃\n" +
		"   ⬝⬝⬝⬝⬝⬝⬝⬝  esc interrupt                                            126.3K (48%)  ctrl+p commands\n"
}

// altScreenProseVisible mirrors the real opencode alt screen whose box holds
// earlier steps and then a line of prose that quotes the composed prompt: the
// marker sits mid-sentence, so no box line opens with the anchored prompt.
func altScreenProseVisible(stamp string) string {
	briefs := filepath.Join("$ROOT", "state", "ws", "briefs") + string(filepath.Separator)
	return "  ┃  [✓] earlier step\n" +
		"     ▣  Build · Qwen3.8-27B NVFP4 (ai01) · 23m 14s\n" +
		"  ┃\n" +
		"  ┃  The note says: \"Read the file " + briefs + "worker-\n" +
		"  ┃  " + stamp + ".md in full and execute it.\"\n" +
		"  ┃\n" +
		"   ⬝⬝⬝⬝⬝⬝⬝⬝  esc interrupt                                            126.3K (48%)  ctrl+p commands\n"
}

// runAltScreenDispatch runs a dispatch to a working target whose recent read
// Herdr refuses (agent_not_idle), with the clock at 2026-09-30 12:54:55 so the
// composed prompt is worker-20260930T125455.md; screen builds the visible
// screen from the given stamp. The clock starts to advance at its first
// reading, as pinDispatchClock does, so a runner slow to build the fixture
// does not move the stamp. It returns the fake herdr's calls with the result.
func runAltScreenDispatch(t *testing.T, screen func(stamp string) string, screenStamp string) (int, string, string, []fakecli.Call) {
	t.Helper()
	base := time.Date(2026, 9, 30, 12, 54, 55, 0, time.Local)
	var once sync.Once
	var began time.Time
	oldNow := platform.Now
	platform.Now = func() time.Time {
		once.Do(func() { began = time.Now() })
		return base.Add(time.Since(began))
	}
	t.Cleanup(func() { platform.Now = oldNow })
	notIdle := `{"error":{"code":"agent_not_idle","message":"cannot read 40 lines while worker is working: its alternate-screen history can only be captured by scrolling while idle. Wait and retry, or use --source visible"}}`
	f := newDispatchArrivalFixture(t, "working", 5, 5, "", "2",
		fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped"}, ArgvPrefix: true, Code: 1, Stderr: notIdle},
		fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, ArgvPrefix: true, Stdout: screen(screenStamp)},
	)
	f.env["SOHO_WAIT_POLL_MS"] = "500"
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return code, out, errText, calls
}

// agentCallsAfterPrompt returns the argv of the `agent` calls made after the
// first `agent prompt`, in order.
func agentCallsAfterPrompt(calls []fakecli.Call) []string {
	var after []string
	sent := false
	for _, call := range calls {
		if len(call.Argv) < 2 || call.Argv[0] != "agent" {
			continue
		}
		if sent {
			after = append(after, strings.Join(call.Argv, " "))
		} else if call.Argv[1] == "prompt" {
			sent = true
		}
	}
	return after
}

// TestDispatchWorkingAltScreenTargetShowsThePromptOnTheVisibleScreen: Herdr
// refuses a recent read of a working full-screen TUI, so the evidence comes
// from the visible screen, with the wrapped path joined back.
func TestDispatchWorkingAltScreenTargetShowsThePromptOnTheVisibleScreen(t *testing.T) {
	code, out, errText, calls := runAltScreenDispatch(t, altScreenVisible, "20260930T125455")
	if code != 0 || dispatchOutputStatus(t, out) != "queued" || strings.Contains(errText, "was typed while it was working") {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if !strings.Contains(out, "worker-20260930T125455.md") {
		t.Fatalf("the clock did not pin the composed name: %s", out)
	}
	// The evidence is on screen within the check window: no wait for the turn.
	// The first probe of the window (its arrival get, the refused recent read
	// and the visible read that replaces it) confirms the prompt, and the only
	// other get is the queued state read; the 2 s turn wait polls get every
	// 500 ms, so any poll of it shows here as an extra get, however slow the
	// runner opens the fake.
	want := []string{
		"agent get worker",
		"agent read worker --source recent-unwrapped --lines 40",
		"agent read worker --source visible",
		"agent get worker",
	}
	if got := agentCallsAfterPrompt(calls); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls after the prompt = %q, want %q: the visible prompt should confirm it before the 2 s turn wait", got, want)
	}
}

// TestDispatchWorkingAltScreenEarlierBriefIsNotThisPrompt (R-RC5B): the box
// line holds only `…/briefs/worker-`, shared by every brief of the agent; an
// earlier brief still on screen is not this prompt.
func TestDispatchWorkingAltScreenEarlierBriefIsNotThisPrompt(t *testing.T) {
	code, out, errText, _ := runAltScreenDispatch(t, altScreenVisible, "19990101T000000")
	if code != 15 || dispatchOutputStatus(t, out) != "not-received" || !strings.Contains(errText, "was typed while it was working") {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
}

// TestDispatchWorkingAltScreenBoxProseQuotingExactFileIsNotThisPrompt: the
// visible screen's box carries earlier steps and prose quoting the exact
// composed prompt; no box line opens with the anchored prompt, so the box
// reassembles no path and the delivery stays not-received.
func TestDispatchWorkingAltScreenBoxProseQuotingExactFileIsNotThisPrompt(t *testing.T) {
	code, out, errText, _ := runAltScreenDispatch(t, altScreenProseVisible, "20260930T125455")
	if code != 15 || dispatchOutputStatus(t, out) != "not-received" || !strings.Contains(errText, "was typed while it was working") {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
}
