package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

type compactFixture struct {
	root string
	bin  string
	env  platform.Env
}

// newCompactFixture builds a hermetic workspace with one rostered worker of
// the given kind and installs the fake herdr CLI with the given rules. The
// worker's role file resolves the effective effort to `high`.
func newCompactFixture(t *testing.T, kind string, rules []fakecli.Rule) *compactFixture {
	t.Helper()
	root := t.TempDir()
	state := filepath.Join(root, "state")
	roles := filepath.Join(root, "roles")
	skill := filepath.Join(root, "skill")
	home := filepath.Join(root, "home")
	conf := filepath.Join(root, "conf")
	bin := filepath.Join(root, "bin")
	for _, dir := range []string{filepath.Join(state, "ws"), roles, skill, home, conf, bin} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\tmode\targs\teffort\n"
	roster += "worker\tp1\t" + kind + "\timplementer\t\t0\t\t2026\t\ttask\timplementer\t\t\t\t\n"
	if err := os.WriteFile(filepath.Join(state, "ws", "agents.tsv"), []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(roles, "implementer.md"), []byte("---\nname: implementer\nmode: edit\neffort: high\n---\nRole body.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
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
	env["HERDR_ENV"] = "1"
	env["SOHO_DIR"] = state
	env["HERDR_WORKSPACE_ID"] = "ws"
	env["SOHO_ROLES"] = roles
	env["SOHO_SKILL_DIR"] = skill
	env["HERDR_SOCKET_PATH"] = filepath.Join(root, "missing.sock")
	env["HOME"] = home
	env["USERPROFILE"] = home
	env["XDG_CONFIG_HOME"] = conf
	env["TMPDIR"] = root
	return &compactFixture{root: root, bin: bin, env: env}
}

func (f *compactFixture) run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, stderr bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &stderr
	code := Run(args, f.env)
	platform.Stdout, platform.Stderr = oldOut, oldErr
	return code, out.String(), stderr.String()
}

func (f *compactFixture) calls(t *testing.T) []fakecli.Call {
	t.Helper()
	logPath := filepath.Join(f.bin, "herdr.calls.jsonl")
	if _, err := os.Stat(logPath); os.IsNotExist(err) {
		return nil // herdr was never invoked
	}
	calls, err := fakecli.ReadCalls(logPath)
	if err != nil {
		t.Fatal(err)
	}
	return calls
}

func compactStateJSON(state string, seq int) string {
	return `{"result":{"agent":{"agent_status":"` + state + `","state_change_seq":` + strconv.Itoa(seq) + `}}}`
}

// countArgv returns how many recorded calls match argv exactly.
func countArgv(calls []fakecli.Call, argv []string) int {
	n := 0
	for _, c := range calls {
		if reflect.DeepEqual(c.Argv, argv) {
			n++
		}
	}
	return n
}

// countArgvPrefix counts recorded calls whose argv starts with prefix.
func countArgvPrefix(calls []fakecli.Call, prefix []string) int {
	n := 0
	for _, c := range calls {
		if len(c.Argv) < len(prefix) {
			continue
		}
		match := true
		for i, p := range prefix {
			if c.Argv[i] != p {
				match = false
				break
			}
		}
		if match {
			n++
		}
	}
	return n
}

func compactJSON(t *testing.T, out string) map[string]any {
	t.Helper()
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		t.Fatalf("no JSON on stdout")
	}
	var value map[string]any
	if err := json.Unmarshal([]byte(trimmed), &value); err != nil {
		t.Fatalf("stdout %q is not a JSON object: %v", out, err)
	}
	return value
}

var compactScreens = struct {
	piStale           string
	piFresh           string
	piFooterHigh      string
	piFooterMedium    string
	claudeStale       string
	claudeFresh       string
	claudeNothing     string
	claudeNoMessages  string
	claudeFailed      string
	codexProof        string
	noProof           string
	codexLaterMention string
	piFooterCutoff    string
	// The pi and opencode screens from the review, minimized. pi does not
	// echo the /compact it runs; opencode proves the compaction only with the
	// done line that carries the duration (▣ = U+25A3, · = U+00B7).
	piNothing      string
	piNothingTwice string
	piFailed       string
	piCompacting   string
	ocMenuTop      string
	ocMenuEmpty    string
	ocMenuNotFirst string
	ocBefore       string
	ocWorking      string
	ocDone         string
}{
	piStale:          "old output line\n[compaction] Compacted from 99999 tokens\n> /compact\n",
	piFresh:          "old output line\n[compaction] Compacted from 99999 tokens\n> /compact\n[compaction] Compacted from 12345 tokens\n",
	piFooterHigh:     "old output line\n> /compact\n[compaction] Compacted from 12345 tokens\n• thinking: high\n",
	piFooterMedium:   "old output line\n> /compact\n[compaction] Compacted from 12345 tokens\n• thinking: medium\n",
	claudeStale:      "old output line\nCompacted\n> /compact\n",
	claudeFresh:      "old output line\nCompacted\n> /compact\nCompacted the conversation to 42 tokens\n",
	claudeNothing:    "old output line\n> /compact\nNot enough messages to compact.\n",
	claudeNoMessages: "old output line\n> /compact\nNo messages to compact\n",
	claudeFailed:     "old output line\n> /compact\nError compacting conversation\n",
	codexProof:       "> /compact\nContext compacted\n",
	noProof:          "> /compact\n",
	// The review's screens: a later line that only mentions /compact must not
	// become the anchor, and `cutoff` must not read as thinking `off`.
	codexLaterMention: "> /compact\nContext compacted\nthe history mentions /compact again\n",
	piFooterCutoff:    "old output line\n> /compact\n[compaction] Compacted from 12345 tokens\n• cutoff\n",
	// pi shows the answer with no echo of the /compact it runs.
	piNothing: "Thinking level: off\nError: Compaction failed: Nothing to compact (session too small)\n",
	// A second attempt prints the identical line again; pi still does not
	// echo the /compact.
	piNothingTwice: "Thinking level: off\nError: Compaction failed: Nothing to compact (session too small)\nError: Compaction failed: Nothing to compact (session too small)\n",
	piFailed:       "Thinking level: off\nError: Compaction failed: Summarization failed: generation hit the token cap for this model\n",
	piCompacting:   "Thinking level: off\nCompacting context...\n",
	// opencode's command menu above the input box (the box itself pads with
	// two spaces and never matches a menu line).
	ocMenuTop:      "  ┃ /compact      Compact session                    ┃\n  ┃ /review       review changes [commit|branch|pr]  ┃\n             ┃\n             ┃  /compact\n             ┃  Build · Model X Provider Y\n",
	ocMenuEmpty:    "             ┃ /review     review changes [commit|branch|pr]  ┃\n             ┃\n             ┃  /compact\n             ┃  Build · Model X Provider Y\n",
	ocMenuNotFirst: "             ┃ /review     review changes [commit|branch|pr]  ┃\n             ┃ /compact    Compact session                ┃\n             ┃\n             ┃  /compact\n             ┃  Build · Model X Provider Y\n",
	// opencode screens: the before read (no done line yet), the compaction
	// still running (the line without a duration is not proof), and the done
	// screen with the summary and the final line with the duration.
	ocBefore:  "     ▣  Build · Model X · 3.3s\n",
	ocWorking: "     ▣  Build · Model X · 3.3s\n  ──────────────────────────── Compaction ────────────────────────────\n     ⠏ Thinking\n     ▣  Compaction · Model X\n",
	ocDone:    "     Next Move\n     1. (none)\n     ▣  Compaction · Model X · 3.1s\n  ┃  Build · Model X Provider Y\n   /private/tmp/oc-probe        851 (0%)  ctrl+p commands\n",
}

func compactReadArgv(agent string) []string {
	return []string{"agent", "read", agent, "--source", "recent", "--lines", "40"}
}

func TestCompactStaleProofAboveMarkerDoesNotCount(t *testing.T) {
	// A Compacted from line above the sent /compact is an earlier compaction;
	// only the proof below it ends the wait.
	t.Run("pi: the stale proof above /compact is ignored and the new one below counts", func(t *testing.T) {
		f := newCompactFixture(t, "pi", []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
			{Argv: []string{"agent", "get", "worker"}, Call: 2, Stdout: compactStateJSON("idle", 1)},
			{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
			{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
			{Argv: compactReadArgv("worker"), Call: 1, Stdout: compactScreens.piStale},
			{Argv: compactReadArgv("worker"), Call: 2, Stdout: compactScreens.piFresh},
			{Argv: compactReadArgv("worker"), Call: 3, Stdout: compactScreens.piFooterHigh},
			// The polls without a proof also check the worker is alive.
			{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("idle", 2)},
		})
		code, out, errText := f.run(t, "compact", "worker", "--timeout", "30000")
		if code != 0 || errText != "" {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
		}
		value := compactJSON(t, out)
		if value["agent"] != "worker" || value["kind"] != "pi" || value["status"] != "compacted" {
			t.Fatalf("json=%v", value)
		}
		if elapsed, ok := value["elapsed_ms"].(float64); !ok || elapsed <= 0 {
			t.Fatalf("elapsed_ms=%v", value["elapsed_ms"])
		}
		calls := f.calls(t)
		if n := countArgv(calls, []string{"pane", "send-text", "p1", "/compact"}); n != 1 {
			t.Fatalf("send-text calls=%d want 1: %#v", n, calls)
		}
		if n := countArgv(calls, []string{"pane", "send-keys", "p1", "Enter"}); n != 1 {
			t.Fatalf("send-keys calls=%d want 1: %#v", n, calls)
		}
		// Proof polling reads twice (the stale screen, then the fresh one) and
		// the pi footer check reads once more.
		if n := countArgv(calls, compactReadArgv("worker")); n != 3 {
			t.Fatalf("recent reads=%d want 3: %#v", n, calls)
		}
	})
}

func TestCompactClaudeProofBelowMarker(t *testing.T) {
	f := newCompactFixture(t, "claude", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
		{Argv: []string{"agent", "get", "worker"}, Call: 2, Stdout: compactStateJSON("idle", 1)},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
		{Argv: compactReadArgv("worker"), Call: 1, Stdout: compactScreens.claudeStale},
		{Argv: compactReadArgv("worker"), Call: 2, Stdout: compactScreens.claudeFresh},
		// The polls without a proof also check the worker is alive.
		{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("idle", 2)},
	})
	code, out, errText := f.run(t, "compact", "worker", "--timeout", "30000")
	if code != 0 || errText != "" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	value := compactJSON(t, out)
	if value["agent"] != "worker" || value["kind"] != "claude" || value["status"] != "compacted" {
		t.Fatalf("json=%v", value)
	}
	calls := f.calls(t)
	if n := countArgv(calls, compactReadArgv("worker")); n != 2 {
		t.Fatalf("recent reads=%d want 2: %#v", n, calls)
	}
	if n := countArgv(calls, []string{"pane", "send-text", "p1", "/compact"}); n != 1 {
		t.Fatalf("send-text calls=%d want 1", n)
	}
}

// TestCompactClaudeNothingToCompact verifies that claude's answer for a
// conversation with nothing to compact ends the wait as a success without
// compaction: no idle wait, no thinking warning, exit 0 with the
// nothing-to-compact status; the answer already on screen before the send
// does not count.
func TestCompactClaudeNothingToCompact(t *testing.T) {
	t.Run("the answer below the echoed /compact counts", func(t *testing.T) {
		f := newCompactFixture(t, "claude", []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
			{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
			{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
			{Argv: compactReadArgv("worker"), Call: 1, Stdout: "old output line\n"},
			{Argv: compactReadArgv("worker"), Call: 2, Stdout: compactScreens.claudeNothing},
		})
		code, out, errText := f.run(t, "compact", "worker", "--timeout", "30000")
		if code != 0 || errText != "" {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
		}
		value := compactJSON(t, out)
		if value["agent"] != "worker" || value["kind"] != "claude" || value["status"] != "nothing-to-compact" {
			t.Fatalf("json=%v", value)
		}
		if elapsed, ok := value["elapsed_ms"].(float64); !ok || elapsed < 0 {
			t.Fatalf("elapsed_ms=%v", value["elapsed_ms"])
		}
		calls := f.calls(t)
		// The pre-send state check and the transcript lookup are the only
		// state reads: the ending does not wait for the worker back at idle.
		if n := countArgv(calls, []string{"agent", "get", "worker"}); n != 2 {
			t.Fatalf("agent get calls=%d want 2: %#v", n, calls)
		}
		if n := countArgv(calls, compactReadArgv("worker")); n != 2 {
			t.Fatalf("recent reads=%d want 2: %#v", n, calls)
		}
	})
	t.Run("the answer already on screen before the send does not count", func(t *testing.T) {
		screen := "earlier reply\nNot enough messages to compact.\n> /compact\n"
		f := newCompactFixture(t, "claude", []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("idle", 1)},
			{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
			{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
			{Argv: compactReadArgv("worker"), ArgvPrefix: true, Stdout: screen},
		})
		code, out, errText := f.run(t, "compact", "worker", "--timeout", "800")
		if code != 9 || compactJSON(t, out)["status"] != "timeout" {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
		}
	})
}

// TestCompactClaudeStaleEndingBelowOldCompact verifies the endings' anchor:
// an ending that already sat below an older /compact on the stable screen
// (equal before and after the send) does not count, while a new /compact
// with the same phrase repeated below it does.
func TestCompactClaudeStaleEndingBelowOldCompact(t *testing.T) {
	stableNothing := "old output line\n> /compact\nNot enough messages to compact.\n"
	stableFailed := "old output line\n> /compact\nError compacting conversation\n"
	t.Run("a stable screen with the old answer times out", func(t *testing.T) {
		f := newCompactFixture(t, "claude", []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("idle", 1)},
			{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
			{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
			{Argv: compactReadArgv("worker"), ArgvPrefix: true, Stdout: stableNothing},
		})
		code, out, errText := f.run(t, "compact", "worker", "--timeout", "800")
		if code != 9 || compactJSON(t, out)["status"] != "timeout" {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
		}
	})
	t.Run("a stable screen with the old error times out", func(t *testing.T) {
		f := newCompactFixture(t, "claude", []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("idle", 1)},
			{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
			{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}`},
			{Argv: compactReadArgv("worker"), ArgvPrefix: true, Stdout: stableFailed},
		})
		code, out, errText := f.run(t, "compact", "worker", "--timeout", "800")
		if code != 9 || compactJSON(t, out)["status"] != "timeout" {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
		}
	})
	t.Run("a new /compact with the same phrase repeated below it counts", func(t *testing.T) {
		f := newCompactFixture(t, "claude", []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
			{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
			{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
			{Argv: compactReadArgv("worker"), Call: 1, Stdout: stableNothing},
			{Argv: compactReadArgv("worker"), Call: 2, Stdout: stableNothing + "> /compact\nNot enough messages to compact.\n"},
		})
		code, out, errText := f.run(t, "compact", "worker", "--timeout", "30000")
		if code != 0 || compactJSON(t, out)["status"] != "nothing-to-compact" {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
		}
	})
}

// TestCompactClaudeFailedCompaction verifies that claude's compaction error
// ends the wait as a failure: exit 9 with the failed status and a friction
// warning, no idle wait, well before the deadline.
func TestCompactClaudeFailedCompaction(t *testing.T) {
	f := newCompactFixture(t, "claude", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
		{Argv: compactReadArgv("worker"), Call: 1, Stdout: "old output line\n"},
		{Argv: compactReadArgv("worker"), Call: 2, Stdout: compactScreens.claudeFailed},
	})
	start := time.Now()
	code, out, errText := f.run(t, "compact", "worker", "--timeout", "30000")
	elapsed := time.Since(start)
	if code != 9 {
		t.Fatalf("code=%d want 9 out=%s stderr=%s", code, out, errText)
	}
	value := compactJSON(t, out)
	if value["agent"] != "worker" || value["kind"] != "claude" || value["status"] != "failed" {
		t.Fatalf("json=%v", value)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("exited after %v; the reported failure must end the wait long before the 30 s deadline", elapsed)
	}
	want := `compact: 'worker' reported "Error compacting conversation"; nothing was compacted`
	if !strings.Contains(errText, want) {
		t.Fatalf("stderr=%q want %q", errText, want)
	}
	calls := f.calls(t)
	// The failure is found on the first poll, before any liveness or idle
	// wait: only the pre-send state check and the transcript lookup.
	if n := countArgv(calls, []string{"agent", "get", "worker"}); n != 2 {
		t.Fatalf("agent get calls=%d want 2: %#v", n, calls)
	}
}

func TestCompactCodexWaitsForIdleAfterProof(t *testing.T) {
	// Codex stays working for a few seconds after the proof; the wait keeps
	// polling agent get until it is idle again. The pre-send screen has no
	// proof and no echo: the proof on the first poll is a fresh line (D22),
	// so the positive semantics — the idle wait after the proof — are
	// preserved with a legitimate baseline.
	f := newCompactFixture(t, "codex", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
		{Argv: []string{"agent", "get", "worker"}, Call: 2, Stdout: compactStateJSON("working", 1)},
		{Argv: []string{"agent", "get", "worker"}, Call: 3, Stdout: compactStateJSON("idle", 1)},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
		{Argv: compactReadArgv("worker"), Call: 1, Stdout: codexClearedComposer},
		{Argv: compactReadArgv("worker"), Call: 2, Stdout: compactScreens.codexProof},
	})
	code, out, errText := f.run(t, "compact", "worker", "--timeout", "30000")
	if code != 0 || errText != "" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	value := compactJSON(t, out)
	if value["agent"] != "worker" || value["kind"] != "codex" || value["status"] != "compacted" {
		t.Fatalf("json=%v", value)
	}
	calls := f.calls(t)
	// One pre-send state check plus the two idle-wait polls (working, then idle).
	if n := countArgv(calls, []string{"agent", "get", "worker"}); n != 3 {
		t.Fatalf("agent get calls=%d want 3: %#v", n, calls)
	}
}

func TestCompactKindWithoutVerifiedCommandExits2(t *testing.T) {
	f := newCompactFixture(t, "grok", nil)
	code, out, errText := f.run(t, "compact", "worker")
	if code != 2 || out != "" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if !strings.Contains(errText, "compact: kind 'grok' has no verified compact command; release --close and spawn --fresh instead") {
		t.Fatalf("stderr=%q", errText)
	}
	calls := f.calls(t)
	if len(calls) != 0 {
		t.Fatalf("herdr calls before the kind check: %#v", calls)
	}
}

func TestCompactBusyWorkerExits10(t *testing.T) {
	for _, state := range []string{"working", "blocked"} {
		t.Run(state, func(t *testing.T) {
			f := newCompactFixture(t, "pi", []fakecli.Rule{
				{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON(state, 1)},
			})
			code, out, errText := f.run(t, "compact", "worker", "--timeout", "30000")
			if code != 10 || out != "" {
				t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
			}
			want := "compact: agent 'worker' is " + state + "; compact only an idle worker"
			if !strings.Contains(errText, want) {
				t.Fatalf("stderr=%q want %q", errText, want)
			}
			calls := f.calls(t)
			if n := countArgvPrefix(calls, []string{"pane", "send-text"}); n != 0 {
				t.Fatalf("send-text calls on a busy worker: %#v", calls)
			}
		})
	}
}

func TestCompactTimeoutExits9(t *testing.T) {
	f := newCompactFixture(t, "claude", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
		{Argv: compactReadArgv("worker"), ArgvPrefix: true, Stdout: compactScreens.noProof},
		// The polls without a proof also check the worker is alive.
		{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("idle", 2)},
	})
	code, out, errText := f.run(t, "compact", "worker", "--timeout", "500")
	if code != 9 {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	value := compactJSON(t, out)
	if value["agent"] != "worker" || value["kind"] != "claude" || value["status"] != "timeout" {
		t.Fatalf("json=%v", value)
	}
	if elapsed, ok := value["elapsed_ms"].(float64); !ok || elapsed < 500 || elapsed >= 10000 {
		t.Fatalf("elapsed_ms=%v want >= 500", value["elapsed_ms"])
	}
	calls := f.calls(t)
	if n := countArgv(calls, []string{"pane", "send-text", "p1", "/compact"}); n != 1 {
		t.Fatalf("send-text calls=%d want 1: %#v", n, calls)
	}
}

func TestCompactPiThinkingWarning(t *testing.T) {
	// The effective effort is high (the role file); the footer shows medium.
	f := newCompactFixture(t, "pi", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
		{Argv: []string{"agent", "get", "worker"}, Call: 2, Stdout: compactStateJSON("idle", 1)},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
		{Argv: compactReadArgv("worker"), Call: 1, Stdout: "old output line\n"},
		{Argv: compactReadArgv("worker"), Call: 2, Stdout: compactScreens.piFresh},
		{Argv: compactReadArgv("worker"), Call: 3, Stdout: compactScreens.piFooterMedium},
	})
	code, out, errText := f.run(t, "compact", "worker", "--timeout", "30000")
	if code != 0 {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if value := compactJSON(t, out); value["status"] != "compacted" {
		t.Fatalf("json=%v", value)
	}
	want := "compact: 'worker' shows thinking 'medium' after compaction; its effort is 'high'"
	if !strings.Contains(errText, want) {
		t.Fatalf("stderr=%q want %q", errText, want)
	}
}

// TestCompactPiThinkingOffAndRestored verifies the pi thinking handling: a
// footer at a level is turned off with shift+tab before the /compact and the
// level is given back after the compaction; the result JSON carries
// thinking_restored and no warning is emitted.
func TestCompactPiThinkingOffAndRestored(t *testing.T) {
	before := "old output line\n• thinking: high\n"
	off := "old output line\n• thinking: off\n"
	proof := "old output line\n[compaction] Compacted from 12345 tokens\n• thinking: off\n"
	restored := "old output line\n[compaction] Compacted from 12345 tokens\n• thinking: high\n"
	f := newCompactFixture(t, "pi", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
		{Argv: []string{"agent", "get", "worker"}, Call: 2, Stdout: compactStateJSON("idle", 2)},
		{Argv: compactReadArgv("worker"), Call: 1, Stdout: before},
		{Argv: compactReadArgv("worker"), Call: 2, Stdout: off},
		{Argv: compactReadArgv("worker"), Call: 3, Stdout: proof},
		{Argv: compactReadArgv("worker"), Call: 4, Stdout: restored},
		{Argv: compactReadArgv("worker"), Call: 5, Stdout: restored},
		{Argv: []string{"pane", "send-keys", "p1", "shift+tab"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
	})
	code, out, errText := f.run(t, "compact", "worker", "--timeout", "30000")
	if code != 0 || errText != "" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	value := compactJSON(t, out)
	if value["agent"] != "worker" || value["kind"] != "pi" || value["status"] != "compacted" {
		t.Fatalf("json=%v", value)
	}
	if restored, ok := value["thinking_restored"].(bool); !ok || !restored {
		t.Fatalf("thinking_restored=%v want true: %v", value["thinking_restored"], value)
	}
	calls := f.calls(t)
	if n := countArgv(calls, []string{"pane", "send-keys", "p1", "shift+tab"}); n != 2 {
		t.Fatalf("shift+tab calls=%d want 2 (one off, one restore): %#v", n, calls)
	}
	if n := countArgv(calls, compactReadArgv("worker")); n != 5 {
		t.Fatalf("recent reads=%d want 5 (before, off, proof, restore, footer check): %#v", n, calls)
	}
}

// TestCompactPiAlreadyOffNoChange verifies that a pi footer already at off is
// left as is: no shift+tab is sent and the result JSON has no thinking_restored.
func TestCompactPiAlreadyOffNoChange(t *testing.T) {
	off := "old output line\n• thinking: off\n"
	proof := "old output line\n[compaction] Compacted from 12345 tokens\n• thinking: off\n"
	f := newCompactFixture(t, "pi", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
		{Argv: []string{"agent", "get", "worker"}, Call: 2, Stdout: compactStateJSON("idle", 2)},
		{Argv: compactReadArgv("worker"), Call: 1, Stdout: off},
		{Argv: compactReadArgv("worker"), Call: 2, Stdout: proof},
		{Argv: compactReadArgv("worker"), Call: 3, Stdout: proof},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
	})
	code, out, _ := f.run(t, "compact", "worker", "--timeout", "30000")
	if code != 0 {
		t.Fatalf("code=%d out=%s", code, out)
	}
	value := compactJSON(t, out)
	if value["status"] != "compacted" {
		t.Fatalf("json=%v", value)
	}
	if _, present := value["thinking_restored"]; present {
		t.Fatalf("thinking_restored must be absent when the level was not changed: %v", value)
	}
	calls := f.calls(t)
	if n := countArgv(calls, []string{"pane", "send-keys", "p1", "shift+tab"}); n != 0 {
		t.Fatalf("shift+tab calls=%d want 0 (footer already off): %#v", n, calls)
	}
}

// TestCompactPiNeverReachesOff verifies that when the footer never reaches off
// after the eight taps, the compact warns and still compacts; the level is
// given back and the result carries thinking_restored.
func TestCompactPiNeverReachesOff(t *testing.T) {
	stuck := "old output line\n• thinking: high\n"
	proof := "old output line\n[compaction] Compacted from 12345 tokens\n• thinking: high\n"
	rules := []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
		{Argv: []string{"agent", "get", "worker"}, Call: 2, Stdout: compactStateJSON("idle", 2)},
		{Argv: compactReadArgv("worker"), Call: 1, Stdout: stuck},
		// eight taps in the off phase, all re-reading the stuck footer
		{Argv: compactReadArgv("worker"), Call: 2, Stdout: stuck},
		{Argv: compactReadArgv("worker"), Call: 3, Stdout: stuck},
		{Argv: compactReadArgv("worker"), Call: 4, Stdout: stuck},
		{Argv: compactReadArgv("worker"), Call: 5, Stdout: stuck},
		{Argv: compactReadArgv("worker"), Call: 6, Stdout: stuck},
		{Argv: compactReadArgv("worker"), Call: 7, Stdout: stuck},
		{Argv: compactReadArgv("worker"), Call: 8, Stdout: stuck},
		{Argv: compactReadArgv("worker"), Call: 9, Stdout: stuck},
		{Argv: compactReadArgv("worker"), Call: 10, Stdout: proof},
		{Argv: compactReadArgv("worker"), Call: 11, Stdout: stuck},
		{Argv: compactReadArgv("worker"), Call: 12, Stdout: stuck},
		{Argv: []string{"pane", "send-keys", "p1", "shift+tab"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
	}
	f := newCompactFixture(t, "pi", rules)
	code, out, errText := f.run(t, "compact", "worker", "--timeout", "30000")
	if code != 0 {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	value := compactJSON(t, out)
	if value["status"] != "compacted" {
		t.Fatalf("json=%v", value)
	}
	if restored, ok := value["thinking_restored"].(bool); !ok || !restored {
		t.Fatalf("thinking_restored=%v want true (the footer never left its level): %v", value["thinking_restored"], value)
	}
	want := "compact: could not turn 'worker' thinking off before compacting (still 'high'); compacting anyway"
	if !strings.Contains(errText, want) {
		t.Fatalf("stderr=%q want %q", errText, want)
	}
	calls := f.calls(t)
	if n := countArgv(calls, []string{"pane", "send-keys", "p1", "shift+tab"}); n != 9 {
		t.Fatalf("shift+tab calls=%d want 9 (eight off, one restore): %#v", n, calls)
	}
}

// TestCompactPiNotRestored verifies that when the level does not come back
// after the eight restore taps, the result carries thinking_restored false and
// the stderr warning names the level the footer still shows.
func TestCompactPiNotRestored(t *testing.T) {
	before := "old output line\n• thinking: high\n"
	off := "old output line\n• thinking: off\n"
	// After the compact the footer stays at off: it never comes back to high.
	stuckOff := "old output line\n[compaction] Compacted from 12345 tokens\n• thinking: off\n"
	rules := []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
		{Argv: []string{"agent", "get", "worker"}, Call: 2, Stdout: compactStateJSON("idle", 2)},
		{Argv: compactReadArgv("worker"), Call: 1, Stdout: before},
		{Argv: compactReadArgv("worker"), Call: 2, Stdout: off},
		{Argv: compactReadArgv("worker"), Call: 3, Stdout: stuckOff},
		// eight restore taps, all re-reading the stuck-off footer
		{Argv: compactReadArgv("worker"), Call: 4, Stdout: stuckOff},
		{Argv: compactReadArgv("worker"), Call: 5, Stdout: stuckOff},
		{Argv: compactReadArgv("worker"), Call: 6, Stdout: stuckOff},
		{Argv: compactReadArgv("worker"), Call: 7, Stdout: stuckOff},
		{Argv: compactReadArgv("worker"), Call: 8, Stdout: stuckOff},
		{Argv: compactReadArgv("worker"), Call: 9, Stdout: stuckOff},
		{Argv: compactReadArgv("worker"), Call: 10, Stdout: stuckOff},
		{Argv: compactReadArgv("worker"), Call: 11, Stdout: stuckOff},
		{Argv: compactReadArgv("worker"), Call: 12, Stdout: stuckOff},
		{Argv: []string{"pane", "send-keys", "p1", "shift+tab"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
	}
	f := newCompactFixture(t, "pi", rules)
	code, out, errText := f.run(t, "compact", "worker", "--timeout", "30000")
	if code != 0 {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	value := compactJSON(t, out)
	if value["status"] != "compacted" {
		t.Fatalf("json=%v", value)
	}
	if restored, ok := value["thinking_restored"].(bool); !ok || restored {
		t.Fatalf("thinking_restored=%v want false: %v", value["thinking_restored"], value)
	}
	want := "compact: could not restore 'worker' thinking to 'high' (shows 'off'); set it by hand before the next brief"
	if !strings.Contains(errText, want) {
		t.Fatalf("stderr=%q want %q", errText, want)
	}
	calls := f.calls(t)
	if n := countArgv(calls, []string{"pane", "send-keys", "p1", "shift+tab"}); n != 9 {
		t.Fatalf("shift+tab calls=%d want 9 (one off, eight restore): %#v", n, calls)
	}
}

// A hard deadline includes preparation and restoration: it must not buy
// another thinking cycle after the budget expires during the first pause.
func TestCompactPiDeadlineDoesNotExtendForRestore(t *testing.T) {
	before := "old output line\n• thinking: high\n"
	off := "old output line\n• thinking: off\n"
	// After the send the footer shows high (no proof): the compact times out,
	// but the restore read finds the level back at high.
	noProof := "old output line\n• thinking: high\n"
	f := newCompactFixture(t, "pi", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("idle", 1)},
		{Argv: compactReadArgv("worker"), Call: 1, Stdout: before},
		{Argv: compactReadArgv("worker"), Call: 2, Stdout: off},
		{Argv: compactReadArgv("worker"), ArgvPrefix: true, Stdout: noProof},
		{Argv: []string{"pane", "send-keys", "p1", "shift+tab"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
	})
	code, out, stderr := f.run(t, "compact", "worker", "--timeout", "500")
	if code != 9 {
		t.Fatalf("code=%d want 9 out=%s", code, out)
	}
	value := compactJSON(t, out)
	if value["status"] != "timeout" {
		t.Fatalf("json=%v", value)
	}
	if restored, ok := value["thinking_restored"].(bool); !ok || restored || !strings.Contains(stderr, "set it by hand") {
		t.Fatalf("thinking_restored=%v want false with manual restoration warning: %v stderr=%q", value["thinking_restored"], value, stderr)
	}
	calls := f.calls(t)
	if n := countArgv(calls, []string{"pane", "send-keys", "p1", "shift+tab"}); n != 1 {
		t.Fatalf("shift+tab calls=%d want 1 (no restoration after deadline): %#v", n, calls)
	}
	if n := countArgv(calls, []string{"pane", "send-text", "p1", "/compact"}); n != 0 {
		t.Fatalf("sent /compact after preparation exhausted the deadline: %#v", calls)
	}
}

// TestCompactPiFailureAfterOffRestores (R-RC7B): a send that fails after the
// thinking went off exits 4 through DieFriction, and the level goes back first.
func TestCompactPiFailureAfterOffRestores(t *testing.T) {
	before := "old output line\n• thinking: high\n"
	off := "old output line\n• thinking: off\n"
	f := newCompactFixture(t, "pi", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("idle", 1)},
		{Argv: compactReadArgv("worker"), Call: 1, Stdout: before},
		{Argv: compactReadArgv("worker"), Call: 2, Stdout: off},
		{Argv: compactReadArgv("worker"), ArgvPrefix: true, Stdout: before},
		{Argv: []string{"pane", "send-keys", "p1", "shift+tab"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}, Code: 1, Stderr: `{"error":{"code":"herdr_failed","message":"boom"}}`},
	})
	code, _, errText := f.run(t, "compact", "worker", "--timeout", "30000")
	if code != 4 || !strings.Contains(errText, "could not send /compact") {
		t.Fatalf("code=%d stderr=%q", code, errText)
	}
	if strings.Contains(errText, "could not restore") {
		t.Fatalf("the level came back, yet a restore warning: %q", errText)
	}
	calls := f.calls(t)
	if n := countArgv(calls, []string{"pane", "send-keys", "p1", "shift+tab"}); n != 2 {
		t.Fatalf("shift+tab calls=%d want 2 (one off, one restore before the exit): %#v", n, calls)
	}
}

// TestCompactPiTimeoutWhileStillCompacting (cinzel): a short timeout while pi
// shows "Compacting context..." exits 9 with still_compacting and a warning not
// to send /compact again; a timeout without that line has no such field.
func TestCompactPiTimeoutWhileStillCompacting(t *testing.T) {
	run := func(t *testing.T, screen string) (int, map[string]any, string) {
		f := newCompactFixture(t, "pi", []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("idle", 1)},
			{Argv: compactReadArgv("worker"), Call: 1, Stdout: "old output line\n"},
			{Argv: compactReadArgv("worker"), ArgvPrefix: true, Stdout: screen},
			{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
			{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
		})
		code, out, errText := f.run(t, "compact", "worker", "--timeout", "500")
		return code, compactJSON(t, out), errText
	}
	code, value, errText := run(t, "old output line\nCompacting context...\n")
	if code != 9 || value["status"] != "timeout" || value["still_compacting"] != true || !strings.Contains(errText, "is still compacting") {
		t.Fatalf("code=%d json=%v stderr=%q", code, value, errText)
	}
	code, value, errText = run(t, "old output line\n")
	if _, has := value["still_compacting"]; code != 9 || has || strings.Contains(errText, "still compacting") {
		t.Fatalf("code=%d json=%v stderr=%q", code, value, errText)
	}
}

// TestCompactPiNothingToCompact verifies the real pi screen: `Nothing to
// compact (session too small)` ends the wait at once as a success without
// compaction (exit 0, well before the deadline, no liveness probes).
func TestCompactPiNothingToCompact(t *testing.T) {
	f := newCompactFixture(t, "pi", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
		{Argv: compactReadArgv("worker"), Call: 1, Stdout: "old output line\n"},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
		{Argv: compactReadArgv("worker"), Call: 2, Stdout: compactScreens.piNothing},
	})
	start := time.Now()
	code, out, errText := f.run(t, "compact", "worker", "--timeout", "30000")
	elapsed := time.Since(start)
	if code != 0 || errText != "" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	value := compactJSON(t, out)
	if value["agent"] != "worker" || value["kind"] != "pi" || value["status"] != "nothing-to-compact" {
		t.Fatalf("json=%v", value)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("exited after %v; nothing-to-compact must not wait out the deadline", elapsed)
	}
	calls := f.calls(t)
	// The ending is found on the first poll, before any liveness probe: the
	// only state read is the pre-send check.
	if n := countArgv(calls, []string{"agent", "get", "worker"}); n != 1 {
		t.Fatalf("agent get calls=%d want 1: %#v", n, calls)
	}
	if n := countArgv(calls, compactReadArgv("worker")); n != 2 {
		t.Fatalf("recent reads=%d want 2 (before, first poll): %#v", n, calls)
	}
}

// TestCompactPiNothingToCompactRepeat covers the second attempt: pi does not
// echo the /compact, so the retry prints a line identical to the one already
// on screen before the send; the ending counts when the line count grows.
func TestCompactPiNothingToCompactRepeat(t *testing.T) {
	before := "Thinking level: off\nError: Compaction failed: Nothing to compact (session too small)\n"
	f := newCompactFixture(t, "pi", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
		{Argv: compactReadArgv("worker"), Call: 1, Stdout: before},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
		{Argv: compactReadArgv("worker"), ArgvPrefix: true, Stdout: compactScreens.piNothingTwice},
	})
	code, out, errText := f.run(t, "compact", "worker", "--timeout", "30000")
	if code != 0 || errText != "" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	value := compactJSON(t, out)
	if value["kind"] != "pi" || value["status"] != "nothing-to-compact" {
		t.Fatalf("json=%v", value)
	}
	calls := f.calls(t)
	if n := countArgv(calls, []string{"agent", "get", "worker"}); n != 1 {
		t.Fatalf("agent get calls=%d want 1 (the ending must not wait for a liveness probe): %#v", n, calls)
	}
}

// TestCompactPiFailedCompaction verifies the real pi failure screen: exit 9
// with the failed status, and the warning cites the failing screen line
// (sanitized and redacted) instead of a fixed phrase. A `Compaction failed:`
// line that is the nothing-to-compact answer must not read as a failure.
func TestCompactPiFailedCompaction(t *testing.T) {
	f := newCompactFixture(t, "pi", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
		{Argv: compactReadArgv("worker"), Call: 1, Stdout: "old output line\n"},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}`},
		{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
		{Argv: compactReadArgv("worker"), Call: 2, Stdout: compactScreens.piFailed},
	})
	start := time.Now()
	code, out, errText := f.run(t, "compact", "worker", "--timeout", "30000")
	elapsed := time.Since(start)
	if code != 9 {
		t.Fatalf("code=%d want 9 out=%s stderr=%s", code, out, errText)
	}
	value := compactJSON(t, out)
	if value["kind"] != "pi" || value["status"] != "failed" {
		t.Fatalf("json=%v", value)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("exited after %v; the reported failure must end the wait long before the 30 s deadline", elapsed)
	}
	want := `compact: 'worker' reported "Error: Compaction failed: Summarization failed: generation hit the token cap for this model"; nothing was compacted`
	if !strings.Contains(errText, want) {
		t.Fatalf("stderr=%q want %q", errText, want)
	}
	if strings.Contains(errText, "Error compacting conversation") {
		t.Fatalf("the pi failure warning must cite the screen line, not the claude phrase: %q", errText)
	}
}

// TestCompactPiStillCompactingKeepsWaiting verifies that `Compacting context`
// on screen, with no ending, keeps waiting until the deadline and reports
// still_compacting.
func TestCompactPiStillCompactingKeepsWaiting(t *testing.T) {
	f := newCompactFixture(t, "pi", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("idle", 1)},
		{Argv: compactReadArgv("worker"), Call: 1, Stdout: "old output line\n"},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
		{Argv: compactReadArgv("worker"), ArgvPrefix: true, Stdout: compactScreens.piCompacting},
	})
	code, out, errText := f.run(t, "compact", "worker", "--timeout", "500")
	if code != 9 {
		t.Fatalf("code=%d want 9 out=%s stderr=%s", code, out, errText)
	}
	value := compactJSON(t, out)
	if value["status"] != "timeout" || value["still_compacting"] != true {
		t.Fatalf("json=%v", value)
	}
	if !strings.Contains(errText, "is still compacting") {
		t.Fatalf("stderr=%q", errText)
	}
	if elapsed, ok := value["elapsed_ms"].(float64); !ok || elapsed < 500 {
		t.Fatalf("elapsed_ms=%v want >= 500 (the wait ran out the deadline)", value["elapsed_ms"])
	}
}

// TestCompactOpenCodeMenuCompacted verifies opencode's verified compact path
// with a conversation: the command menu with /compact on top gets the Enter,
// and the done line with the duration ends the wait as compacted.
func TestCompactOpenCodeMenuCompacted(t *testing.T) {
	f := newCompactFixture(t, "opencode", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
		{Argv: compactReadArgv("worker"), Call: 1, Stdout: compactScreens.ocBefore},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
		{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Stdout: compactScreens.ocMenuTop},
		{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
		{Argv: compactReadArgv("worker"), Call: 2, Stdout: compactScreens.ocDone},
		{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("idle", 2)},
	})
	code, out, errText := f.run(t, "compact", "worker", "--timeout", "30000")
	if code != 0 || errText != "" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	value := compactJSON(t, out)
	if value["agent"] != "worker" || value["kind"] != "opencode" || value["status"] != "compacted" {
		t.Fatalf("json=%v", value)
	}
	if _, present := value["still_compacting"]; present {
		t.Fatalf("still_compacting is a pi field: %v", value)
	}
	calls := f.calls(t)
	if n := countArgv(calls, []string{"pane", "send-keys", "p1", "Enter"}); n != 1 {
		t.Fatalf("Enter calls=%d want 1: %#v", n, calls)
	}
	if n := countArgvPrefix(calls, []string{"pane", "send-keys", "p1", "ctrl+u"}); n != 0 {
		t.Fatalf("ctrl+u calls on the success path: %#v", calls)
	}
	if n := countArgv(calls, []string{"agent", "read", "worker", "--source", "visible"}); n != 1 {
		t.Fatalf("visible reads=%d want 1 (the menu read): %#v", n, calls)
	}
}

// TestCompactOpenCodeUndatedLineIsNotProof verifies that the compaction line
// without the duration (still running) is not the proof: the wait runs out
// the deadline and times out.
func TestCompactOpenCodeUndatedLineIsNotProof(t *testing.T) {
	f := newCompactFixture(t, "opencode", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("idle", 1)},
		{Argv: compactReadArgv("worker"), Call: 1, Stdout: compactScreens.ocBefore},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
		{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Stdout: compactScreens.ocMenuTop},
		{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
		{Argv: compactReadArgv("worker"), ArgvPrefix: true, Stdout: compactScreens.ocWorking},
	})
	code, out, errText := f.run(t, "compact", "worker", "--timeout", "500")
	if code != 9 {
		t.Fatalf("code=%d want 9 out=%s stderr=%s", code, out, errText)
	}
	value := compactJSON(t, out)
	if value["status"] != "timeout" {
		t.Fatalf("json=%v", value)
	}
	if _, present := value["still_compacting"]; present {
		t.Fatalf("still_compacting is a pi field: %v", value)
	}
	if _, present := value["transcript"]; present {
		t.Fatalf("the opencode timeout has no transcript field: %v", value)
	}
	if elapsed, ok := value["elapsed_ms"].(float64); !ok || elapsed < 500 {
		t.Fatalf("elapsed_ms=%v want >= 500", value["elapsed_ms"])
	}
}

// compactDeadlineAfterReads makes the compact wait's deadline pass after
// exactly reads screen reads, whatever the speed of the fake herdr: the
// clock stands still until the fake has logged that many reads (each read
// is logged before it answers, so the count is final when the read
// returns), then a one-way latch expires it for good and the clock runs on
// in real time an hour later. The poll loop then always makes the same
// reads before the deadline (a slow runner can otherwise spend the whole
// short timeout on its first poll), and any wait after the deadline still
// sees time pass. If the reads never come (or the call log cannot be
// read) within a bounded minute, the latch expires anyway and the test
// fails explicitly, so the run under test always returns. A test that
// uses the helper claims behavior after the deadline, so it also fails
// when the run ended before the latch expired (an early return, or a read
// that arrived inside the deadline).
func compactDeadlineAfterReads(t *testing.T, f *compactFixture, reads int) {
	t.Helper()
	logPath := filepath.Join(f.bin, "herdr.calls.jsonl")
	base := time.Now()
	failsafe := base.Add(time.Minute)
	var mu sync.Mutex
	expired := false
	saved := platform.Now
	platform.Now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		if !expired {
			calls, err := fakecli.ReadCalls(logPath)
			switch {
			case err == nil && countArgv(calls, compactReadArgv("worker")) >= reads:
				expired = true
			case time.Now().After(failsafe):
				expired = true
				t.Errorf("the fake herdr did not log %d screen reads within a minute (last read error: %v); the deadline was forced to pass", reads, err)
			default:
				return base
			}
		}
		return time.Now().Add(time.Hour)
	}
	t.Cleanup(func() {
		platform.Now = saved
		mu.Lock()
		defer mu.Unlock()
		if !expired {
			t.Errorf("the deadline never passed: the run ended before the fake herdr logged %d screen reads", reads)
		}
	})
}

// TestCompactClaudeProofAfterDeadlineStillCompacted (edger): the proof shows
// up only in the read after the deadline; the one last read runs the same
// proofs, the compact ends as compacted (exit 0), and /compact is never sent
// again.
func TestCompactClaudeProofAfterDeadlineStillCompacted(t *testing.T) {
	f := newCompactFixture(t, "claude", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
		{Argv: []string{"agent", "get", "worker"}, Call: 2, Stdout: compactStateJSON("idle", 1)},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
		// The pre-send read and the polls inside the deadline show no proof.
		{Argv: compactReadArgv("worker"), Call: 1, Stdout: compactScreens.claudeStale},
		{Argv: compactReadArgv("worker"), Call: 2, Stdout: compactScreens.claudeStale},
		{Argv: compactReadArgv("worker"), Call: 3, Stdout: compactScreens.claudeStale},
		// Only the read after the deadline shows the proof below /compact.
		{Argv: compactReadArgv("worker"), Call: 4, Stdout: compactScreens.claudeFresh},
		{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("idle", 2)},
	})
	// The pre-send read and two polls run before the deadline.
	compactDeadlineAfterReads(t, f, 3)
	// The synthetic proof deadline advances after three reads; leave the real
	// phase budget available for the final-read protocol under test.
	code, out, errText := f.run(t, "compact", "worker", "--timeout", "30s")
	if code != 0 || withoutShortTimeoutWarning(errText) != "" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	value := compactJSON(t, out)
	if value["kind"] != "claude" || value["status"] != "compacted" {
		t.Fatalf("json=%v", value)
	}
	if elapsed, ok := value["elapsed_ms"].(float64); !ok || elapsed < 500 {
		t.Fatalf("elapsed_ms=%v want >= 500 (the proof came after the deadline): %v", value["elapsed_ms"], value)
	}
	calls := f.calls(t)
	// The last read never sends /compact again.
	if n := countArgv(calls, []string{"pane", "send-text", "p1", "/compact"}); n != 1 {
		t.Fatalf("send-text calls=%d want 1: %#v", n, calls)
	}
	// Pre-send read, two polls inside the deadline, and the last read.
	if n := countArgv(calls, compactReadArgv("worker")); n != 4 {
		t.Fatalf("recent reads=%d want 4: %#v", n, calls)
	}
}

// TestCompactPiEndingOnLastRead verifies that an ending that shows up only in
// the read after the deadline still yields the ending's result.
func TestCompactPiEndingOnLastRead(t *testing.T) {
	f := newCompactFixture(t, "pi", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
		{Argv: compactReadArgv("worker"), Call: 1, Stdout: "old output line\n"},
		{Argv: compactReadArgv("worker"), Call: 2, Stdout: "old output line\n"},
		{Argv: compactReadArgv("worker"), Call: 3, Stdout: "old output line\n"},
		// Only the read after the deadline shows the ending.
		{Argv: compactReadArgv("worker"), Call: 4, Stdout: compactScreens.piNothing},
		{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("idle", 2)},
	})
	// The pre-send read and two polls run before the deadline.
	compactDeadlineAfterReads(t, f, 3)
	// The synthetic proof deadline advances after three reads; leave the real
	// phase budget available for the final-read protocol under test.
	code, out, errText := f.run(t, "compact", "worker", "--timeout", "30s")
	if code != 0 || withoutShortTimeoutWarning(errText) != "" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	value := compactJSON(t, out)
	if value["kind"] != "pi" || value["status"] != "nothing-to-compact" {
		t.Fatalf("json=%v", value)
	}
	if elapsed, ok := value["elapsed_ms"].(float64); !ok || elapsed < 500 {
		t.Fatalf("elapsed_ms=%v want >= 500 (the ending came after the deadline): %v", value["elapsed_ms"], value)
	}
	calls := f.calls(t)
	if n := countArgv(calls, []string{"pane", "send-text", "p1", "/compact"}); n != 1 {
		t.Fatalf("send-text calls=%d want 1: %#v", n, calls)
	}
	if n := countArgv(calls, compactReadArgv("worker")); n != 4 {
		t.Fatalf("recent reads=%d want 4: %#v", n, calls)
	}
}

// TestCompactLastReadNothingTimesOut verifies that a last read without proof
// or ending falls back to the plain timeout, with a single /compact sent.
func TestCompactLastReadNothingTimesOut(t *testing.T) {
	f := newCompactFixture(t, "pi", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("idle", 1)},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
		{Argv: compactReadArgv("worker"), ArgvPrefix: true, Stdout: "old output line\n"},
	})
	// The pre-send read and two polls run before the deadline.
	compactDeadlineAfterReads(t, f, 3)
	// The synthetic proof deadline advances after three reads; leave the real
	// phase budget available for the final-read protocol under test.
	code, out, errText := f.run(t, "compact", "worker", "--timeout", "30s")
	if code != 9 || withoutShortTimeoutWarning(errText) != "" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	value := compactJSON(t, out)
	if value["status"] != "timeout" {
		t.Fatalf("json=%v", value)
	}
	if _, present := value["transcript"]; present {
		t.Fatalf("the pi timeout has no transcript field: %v", value)
	}
	calls := f.calls(t)
	if n := countArgv(calls, []string{"pane", "send-text", "p1", "/compact"}); n != 1 {
		t.Fatalf("send-text calls=%d want 1: %#v", n, calls)
	}
	// Pre-send read, two polls inside the deadline, the last read, and the pi
	// still-compacting check that reads the screen once more.
	if n := countArgv(calls, compactReadArgv("worker")); n != 5 {
		t.Fatalf("recent reads=%d want 5: %#v", n, calls)
	}
}

// TestCompactClaudeTimeoutTranscriptField verifies the timeout's transcript
// field for claude: counted when the session transcript was found and
// counted, missing without an id session (or when the file does not exist),
// and unreadable when the transcript path is not a readable file. No path,
// session id or content reaches the JSON.
func TestCompactClaudeTimeoutTranscriptField(t *testing.T) {
	sessionGet := `{"result":{"agent":{"agent_status":"idle","state_change_seq":1,"agent_session":{"kind":"id","value":"sess-1"},"cwd":"/tmp/px"}}}`
	timeoutRules := func(get string) []fakecli.Rule {
		return []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: get},
			{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
			{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
			{Argv: compactReadArgv("worker"), ArgvPrefix: true, Stdout: "old output line\n> /compact\n"},
		}
	}
	runTimeout := func(t *testing.T, get string, claudeDir string) map[string]any {
		f := newCompactFixture(t, "claude", timeoutRules(get))
		if claudeDir != "" {
			f.env["CLAUDE_CONFIG_DIR"] = claudeDir
		}
		code, out, errText := f.run(t, "compact", "worker", "--timeout", "500")
		if code != 9 || withoutShortTimeoutWarning(errText) != "" {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
		}
		value := compactJSON(t, out)
		if value["status"] != "timeout" {
			t.Fatalf("json=%v", value)
		}
		return value
	}
	t.Run("counted when the transcript was found and counted", func(t *testing.T) {
		claudeDir := t.TempDir()
		projDir := filepath.Join(claudeDir, "projects", herdr.ClaudeProjectDir("/tmp/px"))
		if err := os.MkdirAll(projDir, 0o700); err != nil {
			t.Fatal(err)
		}
		transcript := filepath.Join(projDir, "sess-1.jsonl")
		if err := os.WriteFile(transcript, []byte(`{"type":"summary","subtype":"compact_boundary"}\n`), 0o600); err != nil {
			t.Fatal(err)
		}
		value := runTimeout(t, sessionGet, claudeDir)
		if value["transcript"] != "counted" {
			t.Fatalf("transcript=%v want counted: %v", value["transcript"], value)
		}
	})
	t.Run("missing without a session in agent get", func(t *testing.T) {
		value := runTimeout(t, compactStateJSON("idle", 1), t.TempDir())
		if value["transcript"] != "missing" {
			t.Fatalf("transcript=%v want missing: %v", value["transcript"], value)
		}
	})
	t.Run("missing when the session kind is not id", func(t *testing.T) {
		get := `{"result":{"agent":{"agent_status":"idle","state_change_seq":1,"agent_session":{"kind":"session","value":"sess-1"},"cwd":"/tmp/px"}}}`
		value := runTimeout(t, get, t.TempDir())
		if value["transcript"] != "missing" {
			t.Fatalf("transcript=%v want missing: %v", value["transcript"], value)
		}
	})
	t.Run("missing when the file does not exist", func(t *testing.T) {
		value := runTimeout(t, sessionGet, t.TempDir())
		if value["transcript"] != "missing" {
			t.Fatalf("transcript=%v want missing: %v", value["transcript"], value)
		}
	})
	t.Run("unreadable when the transcript path is a directory", func(t *testing.T) {
		claudeDir := t.TempDir()
		// A directory where the transcript file would sit: found, not readable.
		if err := os.MkdirAll(filepath.Join(claudeDir, "projects", herdr.ClaudeProjectDir("/tmp/px"), "sess-1.jsonl"), 0o700); err != nil {
			t.Fatal(err)
		}
		value := runTimeout(t, sessionGet, claudeDir)
		if value["transcript"] != "unreadable" {
			t.Fatalf("transcript=%v want unreadable: %v", value["transcript"], value)
		}
	})
}

// TestCompactOpenCodeEmptySessionClearsBox verifies the empty-session menu:
// without a /compact item the Enter is never sent (it would run /review), the
// box is cleared with ctrl+u, and the result is nothing-to-compact (0), like
// claude and pi.
func TestCompactOpenCodeEmptySessionClearsBox(t *testing.T) {
	f := newCompactFixture(t, "opencode", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
		{Argv: compactReadArgv("worker"), Call: 1, Stdout: compactScreens.ocBefore},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
		{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Stdout: compactScreens.ocMenuEmpty},
		{Argv: []string{"pane", "send-keys", "p1", "ctrl+u"}, Stdout: `{"result":{}}`},
	})
	code, out, errText := f.run(t, "compact", "worker", "--timeout", "30000")
	if code != 0 || errText != "" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	value := compactJSON(t, out)
	if value["kind"] != "opencode" || value["status"] != "nothing-to-compact" {
		t.Fatalf("json=%v", value)
	}
	calls := f.calls(t)
	if n := countArgvPrefix(calls, []string{"pane", "send-keys", "p1", "Enter"}); n != 0 {
		t.Fatalf("Enter was sent on an empty-session menu: %#v", calls)
	}
	if n := countArgv(calls, []string{"pane", "send-keys", "p1", "ctrl+u"}); n != 1 {
		t.Fatalf("ctrl+u calls=%d want 1 (clear the box): %#v", n, calls)
	}
	if n := countArgv(calls, compactReadArgv("worker")); n != 1 {
		t.Fatalf("recent reads=%d want 1 (only the pre-send read): %#v", n, calls)
	}
}

// TestCompactOpenCodeCompactNotFirstExits4 verifies that a /compact item that
// is not the first menu line is not run: the box is cleared with ctrl+u, no
// Enter is sent, and the compact exits 4.
func TestCompactOpenCodeCompactNotFirstExits4(t *testing.T) {
	f := newCompactFixture(t, "opencode", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
		{Argv: compactReadArgv("worker"), Call: 1, Stdout: compactScreens.ocBefore},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
		{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Stdout: compactScreens.ocMenuNotFirst},
		{Argv: []string{"pane", "send-keys", "p1", "ctrl+u"}, Stdout: `{"result":{}}`},
	})
	code, out, errText := f.run(t, "compact", "worker", "--timeout", "30000")
	if code != 4 || out != "" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	want := "compact: could not select /compact in the command menu of 'worker'; nothing was compacted"
	if !strings.Contains(errText, want) {
		t.Fatalf("stderr=%q want %q", errText, want)
	}
	calls := f.calls(t)
	if n := countArgvPrefix(calls, []string{"pane", "send-keys", "p1", "Enter"}); n != 0 {
		t.Fatalf("Enter was sent although /compact is not on top: %#v", calls)
	}
	if n := countArgv(calls, []string{"pane", "send-keys", "p1", "ctrl+u"}); n != 1 {
		t.Fatalf("ctrl+u calls=%d want 1: %#v", n, calls)
	}
}

// TestCompactOpenCodeMenuNeverAppearsExits4 verifies that when the command
// menu never shows up in the visible screen (the ~2 s window), the box is
// cleared and the compact exits 4.
func TestCompactOpenCodeMenuNeverAppearsExits4(t *testing.T) {
	f := newCompactFixture(t, "opencode", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
		{Argv: compactReadArgv("worker"), Call: 1, Stdout: compactScreens.ocBefore},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
		{Argv: []string{"agent", "read", "worker", "--source", "visible"}, ArgvPrefix: true},
		{Argv: []string{"pane", "send-keys", "p1", "ctrl+u"}, Stdout: `{"result":{}`},
	})
	start := time.Now()
	code, out, errText := f.run(t, "compact", "worker", "--timeout", "30000")
	elapsed := time.Since(start)
	if code != 4 || out != "" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if !strings.Contains(errText, "could not select /compact in the command menu") {
		t.Fatalf("stderr=%q", errText)
	}
	if elapsed < 2*time.Second || elapsed > 10*time.Second {
		t.Fatalf("the menu window is ~2 s: elapsed=%v", elapsed)
	}
	calls := f.calls(t)
	if n := countArgvPrefix(calls, []string{"pane", "send-keys", "p1", "Enter"}); n != 0 {
		t.Fatalf("Enter was sent without a visible menu: %#v", calls)
	}
	if n := countArgv(calls, []string{"pane", "send-keys", "p1", "ctrl+u"}); n != 1 {
		t.Fatalf("ctrl+u calls=%d want 1: %#v", n, calls)
	}
}

// TestCompactOpenCodeWorkingReadFallsBackToVisible verifies that the wait
// loop reads opencode through herdr.AgentRead: a `recent` read of the working
// worker fails with agent_not_idle and the visible screen stands in for the
// proof.
func TestCompactOpenCodeWorkingReadFallsBackToVisible(t *testing.T) {
	f := newCompactFixture(t, "opencode", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
		{Argv: compactReadArgv("worker"), Call: 1, Stdout: compactScreens.ocBefore},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
		{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Call: 1, Stdout: compactScreens.ocMenuTop},
		{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}`},
		{Argv: compactReadArgv("worker"), ArgvPrefix: true, Code: 1, Stderr: `{"error":{"code":"agent_not_idle","message":"cannot read 40 lines while worker is working"}}`},
		{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Stdout: compactScreens.ocDone},
		{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("idle", 2)},
	})
	code, out, errText := f.run(t, "compact", "worker", "--timeout", "30000")
	if code != 0 || errText != "" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if value := compactJSON(t, out); value["status"] != "compacted" {
		t.Fatalf("json=%v", value)
	}
	calls := f.calls(t)
	// The menu read and the visible read the fallback takes after the failed
	// recent read.
	if n := countArgv(calls, []string{"agent", "read", "worker", "--source", "visible"}); n != 2 {
		t.Fatalf("visible reads=%d want 2 (menu plus the agent_not_idle fallback): %#v", n, calls)
	}
	if n := countArgvPrefix(calls, []string{"pane", "send-keys", "p1", "Enter"}); n != 1 {
		t.Fatalf("Enter calls=%d want 1: %#v", n, calls)
	}
}

// TestCompactClaudeNoMessagesToCompact verifies Claude Code 2.1.285's second
// nothing-to-compact string: below the echoed /compact it ends the wait as a
// success without compaction, and the same line already on screen before the
// send does not count.
func TestCompactClaudeNoMessagesToCompact(t *testing.T) {
	t.Run("the answer below the echoed /compact counts", func(t *testing.T) {
		f := newCompactFixture(t, "claude", []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
			{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
			{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
			{Argv: compactReadArgv("worker"), Call: 1, Stdout: "old output line\n"},
			{Argv: compactReadArgv("worker"), Call: 2, Stdout: compactScreens.claudeNoMessages},
		})
		code, out, errText := f.run(t, "compact", "worker", "--timeout", "30000")
		if code != 0 || errText != "" {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
		}
		value := compactJSON(t, out)
		if value["agent"] != "worker" || value["kind"] != "claude" || value["status"] != "nothing-to-compact" {
			t.Fatalf("json=%v", value)
		}
		calls := f.calls(t)
		if n := countArgv(calls, []string{"agent", "get", "worker"}); n != 2 {
			t.Fatalf("agent get calls=%d want 2: %#v", n, calls)
		}
		if n := countArgv(calls, compactReadArgv("worker")); n != 2 {
			t.Fatalf("recent reads=%d want 2: %#v", n, calls)
		}
	})
	t.Run("the same line already on screen before the send does not count", func(t *testing.T) {
		stable := "old output line\n> /compact\nNo messages to compact\n"
		f := newCompactFixture(t, "claude", []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("idle", 1)},
			{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
			{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
			{Argv: compactReadArgv("worker"), ArgvPrefix: true, Stdout: stable},
		})
		code, out, errText := f.run(t, "compact", "worker", "--timeout", "800")
		if code != 9 || compactJSON(t, out)["status"] != "timeout" {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
		}
	})
}

// TestCompactLaterMentionDoesNotAnchor verifies the review's later-mention
// screen: a line that only mentions /compact after the proof is not the
// anchor, so the proof below the sent command still counts (old code: 9).
// The pre-send screen has no proof and no echo: the proof on the first poll
// is a fresh line (D22), keeping the positive semantics the test covers.
func TestCompactLaterMentionDoesNotAnchor(t *testing.T) {
	f := newCompactFixture(t, "codex", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
		{Argv: []string{"agent", "get", "worker"}, Call: 2, Stdout: compactStateJSON("idle", 1)},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
		{Argv: compactReadArgv("worker"), Call: 1, Stdout: codexClearedComposer},
		{Argv: compactReadArgv("worker"), Call: 2, Stdout: compactScreens.codexLaterMention},
	})
	code, out, errText := f.run(t, "compact", "worker", "--timeout", "500")
	if code != 0 || errText != "soho: warning: compact: --timeout is in milliseconds; 500 is under a second (for 500 seconds pass 500000)\n" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	value := compactJSON(t, out)
	if value["agent"] != "worker" || value["kind"] != "codex" || value["status"] != "compacted" {
		t.Fatalf("json=%v", value)
	}
	calls := f.calls(t)
	if n := countArgv(calls, compactReadArgv("worker")); n != 2 {
		t.Fatalf("recent reads=%d want 2 (the pre-send read, then the proof on the first poll): %#v", n, calls)
	}
	if n := countArgv(calls, []string{"pane", "send-text", "p1", "/compact"}); n != 1 {
		t.Fatalf("send-text calls=%d want 1: %#v", n, calls)
	}
}

// TestCompactWorkerDiesDuringWait verifies that gone and unavailable during
// the idle wait exit 6 and 4 right away (well before the deadline) with the
// same messages the pre-send checks use, instead of waiting out a timeout.
func TestCompactWorkerDiesDuringWait(t *testing.T) {
	cases := []struct {
		name    string
		rule    fakecli.Rule
		code    int
		wantErr string
	}{
		{
			name:    "agent_not_found after the proof is gone",
			rule:    fakecli.Rule{Stderr: `{"error":{"code":"agent_not_found","message":"gone"}}`},
			code:    6,
			wantErr: "compact: agent 'worker' is no longer live",
		},
		{
			name:    "herdr_failed after the proof is unavailable",
			rule:    fakecli.Rule{Code: 1, Stderr: `{"error":{"code":"herdr_failed","message":"boom"}}`},
			code:    4,
			wantErr: "compact: agent 'worker' is unavailable:",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rule := tc.rule
			rule.Argv = []string{"agent", "get", "worker"}
			rule.Call = 2
			f := newCompactFixture(t, "codex", []fakecli.Rule{
				{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
				rule,
				{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
				{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
				{Argv: compactReadArgv("worker"), ArgvPrefix: true, Stdout: compactScreens.codexProof},
			})
			start := time.Now()
			code, out, errText := f.run(t, "compact", "worker", "--timeout", "30000")
			elapsed := time.Since(start)
			if code != tc.code {
				t.Fatalf("code=%d want %d out=%s stderr=%s", code, tc.code, out, errText)
			}
			if out != "" {
				t.Fatalf("stdout on a dead worker: %q", out)
			}
			if !strings.Contains(errText, tc.wantErr) {
				t.Fatalf("stderr=%q want %q", errText, tc.wantErr)
			}
			if elapsed > 10*time.Second {
				t.Fatalf("exited after %v; the worker's death must stop the wait long before the 30 s deadline", elapsed)
			}
			calls := f.calls(t)
			if n := countArgv(calls, []string{"agent", "get", "worker"}); n != 2 {
				t.Fatalf("agent get calls=%d want 2 (pre-send plus the one that died): %#v", n, calls)
			}
		})
	}
}

// TestCompactWorkerDiesBeforeProof verifies that a worker that dies while
// the proof has not appeared yet stops the wait at once (6) instead of
// running out the deadline.
func TestCompactWorkerDiesBeforeProof(t *testing.T) {
	f := newCompactFixture(t, "codex", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
		{Argv: []string{"agent", "get", "worker"}, Call: 2, Stderr: `{"error":{"code":"agent_not_found","message":"gone"}}`},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
		{Argv: compactReadArgv("worker"), ArgvPrefix: true, Stdout: compactScreens.noProof},
	})
	start := time.Now()
	code, out, errText := f.run(t, "compact", "worker", "--timeout", "30000")
	if code != 6 || out != "" || !strings.Contains(errText, "compact: agent 'worker' is no longer live") {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, errText)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("exited after %v; the death must stop the wait long before the 30 s deadline", elapsed)
	}
}

// TestCompactFooterThinkingBoundary verifies the review's footer screens:
// `• cutoff` is not a thinking level (no warning) and `• thinking: high`
// against an effective effort of medium warns.
func TestCompactFooterThinkingBoundary(t *testing.T) {
	t.Run("`• cutoff` is not a thinking level: no warning", func(t *testing.T) {
		f := newCompactFixture(t, "pi", []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
			{Argv: []string{"agent", "get", "worker"}, Call: 2, Stdout: compactStateJSON("idle", 1)},
			{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
			{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
			{Argv: compactReadArgv("worker"), Call: 1, Stdout: "old output line\n"},
			{Argv: compactReadArgv("worker"), Call: 2, Stdout: compactScreens.piFresh},
			{Argv: compactReadArgv("worker"), Call: 3, Stdout: compactScreens.piFooterCutoff},
		})
		code, out, errText := f.run(t, "compact", "worker", "--timeout", "30000")
		if code != 0 {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
		}
		if value := compactJSON(t, out); value["status"] != "compacted" {
			t.Fatalf("json=%v", value)
		}
		if strings.Contains(errText, "shows thinking") {
			t.Fatalf("`cutoff` was read as a thinking level: %s", errText)
		}
	})
	t.Run("`• thinking: high` with effort medium warns", func(t *testing.T) {
		f := newCompactFixture(t, "pi", []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
			{Argv: []string{"agent", "get", "worker"}, Call: 2, Stdout: compactStateJSON("idle", 1)},
			{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
			{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
			{Argv: compactReadArgv("worker"), Call: 1, Stdout: "old output line\n"},
			{Argv: compactReadArgv("worker"), Call: 2, Stdout: compactScreens.piFresh},
			{Argv: compactReadArgv("worker"), Call: 3, Stdout: compactScreens.piFooterHigh},
		})
		roleFile := filepath.Join(f.root, "roles", "implementer.md")
		if err := os.WriteFile(roleFile, []byte("---\nname: implementer\nmode: edit\neffort: medium\n---\nRole body.\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, errText := f.run(t, "compact", "worker", "--timeout", "30000")
		if code != 0 {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
		}
		if value := compactJSON(t, out); value["status"] != "compacted" {
			t.Fatalf("json=%v", value)
		}
		want := "compact: 'worker' shows thinking 'high' after compaction; its effort is 'medium'"
		if !strings.Contains(errText, want) {
			t.Fatalf("stderr=%q want %q", errText, want)
		}
	})
}

func TestCompactUnknownAgentExits3(t *testing.T) {
	f := newCompactFixture(t, "pi", nil)
	code, out, errText := f.run(t, "compact", "ghost")
	if code != 3 || out != "" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if !strings.Contains(errText, "agent 'ghost' is not in this skill's roster") {
		t.Fatalf("stderr=%q", errText)
	}
}

func TestCompactUsageErrors(t *testing.T) {
	cases := [][]string{
		{"compact"},
		{"compact", "--timeout"},
		{"compact", "worker", "--timeout", "abc"},
		{"compact", "worker", "--timeout", "0"},
		{"compact", "worker", "extra"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			f := newCompactFixture(t, "pi", nil)
			code, out, errText := f.run(t, args...)
			if code != 2 || out != "" {
				t.Fatalf("args=%v code=%d out=%s stderr=%s", args, code, out, errText)
			}
		})
	}
}

// dispatchLastLineStatus parses the last JSON line of dispatch's stdout; with
// --compact the compact result line precedes it.
func dispatchLastLineStatus(t *testing.T, out string) string {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var value map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &value); err != nil {
		t.Fatalf("dispatch JSON %q: %v", out, err)
	}
	status, _ := value["wait_status"].(string)
	return status
}

// TestDispatchCompactCompactsThenSends verifies that dispatch --compact runs
// the same compact step for an idle worker and then sends the brief.
func TestDispatchCompactCompactsThenSends(t *testing.T) {
	extra := []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
		{Argv: []string{"agent", "get", "worker"}, Call: 2, Stdout: compactStateJSON("idle", 1)},
		{Argv: []string{"agent", "get", "worker"}, Call: 3, Stdout: compactStateJSON("idle", 1)},
		{Argv: []string{"pane", "send-text", "w0test:p0a", "/compact"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-keys", "w0test:p0a", "Enter"}, Stdout: `{"result":{}}`},
		// The pre-send read has no proof and no echo: the proof on the first
		// poll is a fresh line (D22), keeping the compact step positive.
		{Argv: compactReadArgv("worker"), Call: 1, Stdout: codexClearedComposer},
		{Argv: compactReadArgv("worker"), ArgvPrefix: true, Stdout: compactScreens.codexProof},
	}
	f := newDispatchArrivalFixture(t, "working", 1, 2, "$CURRENT_PATHS", "0", extra...)
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait", "--compact")
	if code != 0 {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if status := dispatchLastLineStatus(t, out); status != "submitted" {
		t.Fatalf("wait_status=%s out=%s", status, out)
	}
	// One JSON line on stdout (the dispatch's); the compaction is a stderr note.
	if strings.Contains(out, `"status":"compacted"`) || strings.Count(strings.TrimSpace(out), "\n") != 0 {
		t.Fatalf("stdout must hold only the dispatch line: %s", out)
	}
	if !strings.Contains(errText, "dispatch: compacted 'worker' in ") {
		t.Fatalf("missing the compacted note on stderr: %s", errText)
	}
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(f.bin, "herdr.json"))
	if err != nil {
		t.Fatal(err)
	}
	if n := countArgv(calls, []string{"pane", "send-text", "w0test:p0a", "/compact"}); n != 1 {
		t.Fatalf("send-text calls=%d want 1: %#v", n, calls)
	}
	if n := countArgvPrefix(calls, []string{"agent", "prompt", "worker"}); n != 1 {
		t.Fatalf("prompt calls=%d want 1: %#v", n, calls)
	}
}

// TestDispatchCompactTimeoutKeepsBriefUnsent verifies that a compact timeout
// exits dispatch with 9 and the brief is never sent.
func TestDispatchCompactTimeoutKeepsBriefUnsent(t *testing.T) {
	base := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	fixedNow := platform.Now
	platform.Now = func() time.Time {
		value := base
		base = base.Add(310 * time.Second)
		return value
	}
	t.Cleanup(func() { platform.Now = fixedNow })
	extra := []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
		{Argv: []string{"agent", "get", "worker"}, Call: 2, Stdout: compactStateJSON("idle", 1)},
		{Argv: []string{"pane", "send-text", "w0test:p0a", "/compact"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-keys", "w0test:p0a", "Enter"}, Stdout: `{"result":{}}`},
		{Argv: compactReadArgv("worker"), ArgvPrefix: true, Stdout: compactScreens.noProof},
	}
	f := newDispatchArrivalFixture(t, "working", 1, 2, "$CURRENT_PATHS", "0", extra...)
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait", "--compact")
	if code != 9 {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	// cinzel: the timeout says which phase stopped and that the brief was not sent.
	if !strings.Contains(errText, "the compact step of 'worker' did not finish (exit 9); the brief was not sent") {
		t.Fatalf("stderr without the phase and brief_sent note: %q", errText)
	}
	if !strings.Contains(out, `"status":"timeout"`) {
		t.Fatalf("missing the timeout JSON: %s", out)
	}
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(f.bin, "herdr.json"))
	if err != nil {
		t.Fatal(err)
	}
	if n := countArgvPrefix(calls, []string{"agent", "prompt", "worker"}); n != 0 {
		t.Fatalf("the brief was sent after the compact timeout: %#v", calls)
	}
	// No task state points at a report that will never come.
	if _, err := os.Stat(filepath.Join(f.state, "ws", "last-report-worker")); !os.IsNotExist(err) {
		t.Fatalf("last-report was written before the compact timeout: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(f.state, "ws", "briefs")); len(entries) != 0 {
		t.Fatalf("a composed brief was written before the compact timeout: %v", entries)
	}
	if n := countArgv(calls, []string{"pane", "send-text", "w0test:p0a", "/compact"}); n != 1 {
		t.Fatalf("send-text calls=%d want 1: %#v", n, calls)
	}
}

// TestDispatchCompactBusyWorkerExits10 verifies that a working worker stops
// the dispatch with 10 before the brief is sent.
func TestDispatchCompactBusyWorkerExits10(t *testing.T) {
	extra := []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("working", 1)},
		{Argv: []string{"agent", "get", "worker"}, Call: 2, Stdout: compactStateJSON("working", 1)},
	}
	f := newDispatchArrivalFixture(t, "working", 1, 2, "$CURRENT_PATHS", "0", extra...)
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait", "--compact")
	if code != 10 {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	want := "compact: agent 'worker' is working; compact only an idle worker"
	if !strings.Contains(errText, want) {
		t.Fatalf("stderr=%q want %q", errText, want)
	}
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(f.bin, "herdr.json"))
	if err != nil {
		t.Fatal(err)
	}
	if n := countArgvPrefix(calls, []string{"agent", "prompt", "worker"}); n != 0 {
		t.Fatalf("the brief was sent to a busy worker: %#v", calls)
	}
}

// TestDispatchCompactSkipsUnsupportedKind verifies that dispatch --compact
// warns for a kind without a verified command and still sends the brief.
func TestDispatchCompactSkipsUnsupportedKind(t *testing.T) {
	extra := []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
		{Argv: []string{"agent", "get", "worker"}, Call: 2, Stdout: compactStateJSON("working", 2)},
	}
	f := newDispatchArrivalFixture(t, "working", 1, 2, "$CURRENT_PATHS", "0", extra...)
	roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\tmode\targs\teffort\nworker\tw0test:p0a\tgrok\timplementer\t\t0\t\tnow\tgrok-1\ttask\timplementer\t\t\t\thigh\n"
	if err := os.WriteFile(filepath.Join(f.state, "ws", "agents.tsv"), []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait", "--compact")
	if code != 0 {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if status := dispatchLastLineStatus(t, out); status != "submitted" {
		t.Fatalf("wait_status=%s out=%s", status, out)
	}
	want := "dispatch: --compact skipped: kind 'grok' has no verified compact command"
	if !strings.Contains(errText, want) {
		t.Fatalf("stderr=%q want %q", errText, want)
	}
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(f.bin, "herdr.json"))
	if err != nil {
		t.Fatal(err)
	}
	if n := countArgvPrefix(calls, []string{"pane", "send-text"}); n != 0 {
		t.Fatalf("send-text calls for an unsupported kind: %#v", calls)
	}
	if n := countArgvPrefix(calls, []string{"agent", "prompt", "worker"}); n != 1 {
		t.Fatalf("prompt calls=%d want 1: %#v", n, calls)
	}
}

// TestDispatchCompactClaudeNothingToCompact verifies that a claude with
// nothing to compact inside a dispatch keeps the flow: the compact note is a
// stderr line, the dispatch's own JSON line stays alone on stdout, and the
// brief is sent.
func TestDispatchCompactClaudeNothingToCompact(t *testing.T) {
	// Call 1 is the compact pre-send check and call 2 its transcript lookup
	// (no agent_session in the fixture: the screen path); the ending is
	// found on the first poll, so call 3 is the dispatch's state probe before
	// the send and call 4 its first arrival probe.
	extra := []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
		{Argv: []string{"agent", "get", "worker"}, Call: 2, Stdout: compactStateJSON("idle", 1)},
		{Argv: []string{"agent", "get", "worker"}, Call: 3, Stdout: compactStateJSON("idle", 1)},
		{Argv: []string{"agent", "get", "worker"}, Call: 4, Stdout: compactStateJSON("working", 2)},
		{Argv: []string{"pane", "send-text", "w0test:p0a", "/compact"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-keys", "w0test:p0a", "Enter"}, Stdout: `{"result":{}}`},
		// The pre-send read has no ending; the poll reads the answer below the
		// newly echoed /compact.
		{Argv: compactReadArgv("worker"), Call: 1, Stdout: "old output line\n"},
		{Argv: compactReadArgv("worker"), ArgvPrefix: true, Stdout: compactScreens.claudeNothing},
	}
	f := newDispatchArrivalFixture(t, "working", 1, 2, "$CURRENT_PATHS", "0", extra...)
	roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\tmode\targs\teffort\nworker\tw0test:p0a\tclaude\timplementer\t\t0\t\tnow\tclaude-1\ttask\timplementer\t\t\t\thigh\n"
	if err := os.WriteFile(filepath.Join(f.state, "ws", "agents.tsv"), []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait", "--compact")
	if code != 0 {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if status := dispatchLastLineStatus(t, out); status != "submitted" {
		t.Fatalf("wait_status=%s out=%s", status, out)
	}
	// One JSON line on stdout (the dispatch's); the nothing-to-compact note is
	// a stderr line, not a compact JSON.
	if strings.Contains(out, `"status":"nothing-to-compact"`) || strings.Count(strings.TrimSpace(out), "\n") != 0 {
		t.Fatalf("stdout must hold only the dispatch line: %s", out)
	}
	if !strings.Contains(errText, "dispatch: 'worker' had nothing to compact") {
		t.Fatalf("missing the nothing-to-compact note on stderr: %s", errText)
	}
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(f.bin, "herdr.json"))
	if err != nil {
		t.Fatal(err)
	}
	if n := countArgvPrefix(calls, []string{"agent", "prompt", "worker"}); n != 1 {
		t.Fatalf("prompt calls=%d want 1: %#v", n, calls)
	}
	if n := countArgv(calls, []string{"pane", "send-text", "w0test:p0a", "/compact"}); n != 1 {
		t.Fatalf("send-text calls=%d want 1: %#v", n, calls)
	}
}

// TestCompactPiWithoutEcho uses the screen pi really shows: it does not echo
// the /compact it runs, only a `[compaction]` box with `Compacted from <n>
// tokens`. A proof line that was not on screen before the send counts; the
// same line as before the send does not (rc.3 timed out on every pi worker).
func TestCompactPiWithoutEcho(t *testing.T) {
	before := "earlier reply\n/path/to/report.md\n"
	after := before + "[compaction]\nCompacted from 64,446 tokens (ctrl+o to expand)\n"
	t.Run("a new proof line without the echoed command counts", func(t *testing.T) {
		f := newCompactFixture(t, "pi", []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("idle", 1)},
			{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
			{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
			{Argv: compactReadArgv("worker"), Call: 1, Stdout: before},
			{Argv: compactReadArgv("worker"), ArgvPrefix: true, Stdout: after},
		})
		code, out, errText := f.run(t, "compact", "worker", "--timeout", "30000")
		if code != 0 || compactJSON(t, out)["status"] != "compacted" {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
		}
	})
	t.Run("the proof of an earlier compaction does not count", func(t *testing.T) {
		f := newCompactFixture(t, "pi", []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("idle", 1)},
			{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
			{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
			{Argv: compactReadArgv("worker"), ArgvPrefix: true, Stdout: after},
		})
		code, out, errText := f.run(t, "compact", "worker", "--timeout", "800")
		if code != 9 || compactJSON(t, out)["status"] != "timeout" {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
		}
	})
}

// TestCompactPreSendReadFailureSendsNothing: without the screen before the
// send an earlier proof would count as this one, so nothing is sent (exit 4).
func TestCompactPreSendReadFailureSendsNothing(t *testing.T) {
	f := newCompactFixture(t, "pi", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("idle", 1)},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
		{Argv: compactReadArgv("worker"), Call: 1, Code: 1, Stderr: `{"error":{"code":"herdr_failed","message":"boom"}}`},
		{Argv: compactReadArgv("worker"), ArgvPrefix: true, Stdout: "[compaction]\nCompacted from 64,446 tokens\n"},
	})
	code, out, errText := f.run(t, "compact", "worker", "--timeout", "30000")
	if code != 4 || out != "" || !strings.Contains(errText, "could not read the screen of 'worker' before sending; nothing was sent") {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, errText)
	}
	if n := countArgv(f.calls(t), []string{"pane", "send-text", "p1", "/compact"}); n != 0 {
		t.Fatalf("/compact was sent after a failed pre-send read (%d)", n)
	}
}

// TestDispatchCompactOpenCodeCompacts verifies that dispatch --compact for an
// opencode worker compacts it (the menu is checked and the Enter is sent only
// with /compact on top) and then sends the brief: opencode is a verified
// compact kind, so there is no skip warning.
func TestDispatchCompactOpenCodeCompacts(t *testing.T) {
	extra := []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
		{Argv: []string{"agent", "get", "worker"}, Call: 2, Stdout: compactStateJSON("idle", 1)},
		{Argv: []string{"agent", "get", "worker"}, Call: 3, Stdout: compactStateJSON("idle", 1)},
		// The pre-send read has no done line; the poll reads the done screen.
		{Argv: compactReadArgv("worker"), Call: 1, Stdout: compactScreens.ocBefore},
		{Argv: compactReadArgv("worker"), ArgvPrefix: true, Stdout: compactScreens.ocDone},
		{Argv: []string{"pane", "send-text", "w0test:p0a", "/compact"}, Stdout: `{"result":{}}`},
		{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Call: 1, Stdout: compactScreens.ocMenuTop},
		{Argv: []string{"pane", "send-keys", "w0test:p0a", "Enter"}, Stdout: `{"result":{}}`},
		// The dispatch's arrival probes: the worker goes back to working with a
		// new state_change_seq.
		{Argv: []string{"agent", "get", "worker"}, ArgvPrefix: true, Stdout: compactStateJSON("working", 2)},
		{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"submitted":true}}`},
	}
	f := newDispatchArrivalFixture(t, "working", 1, 2, "$CURRENT_PATHS", "0", extra...)
	roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\tmode\targs\teffort\nworker\tw0test:p0a\topencode\timplementer\t\t0\t\tnow\toc-model\ttask\timplementer\t\t\t\thigh\n"
	if err := os.WriteFile(filepath.Join(f.state, "ws", "agents.tsv"), []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait", "--compact")
	if code != 0 {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if status := dispatchLastLineStatus(t, out); status != "submitted" {
		t.Fatalf("wait_status=%s out=%s", status, out)
	}
	if strings.Contains(errText, "has no verified compact command") {
		t.Fatalf("opencode has a verified compact command: %s", errText)
	}
	// One JSON line on stdout (the dispatch's); the compaction is a stderr note.
	if strings.Contains(out, `"status":"compacted"`) || strings.Count(strings.TrimSpace(out), "\n") != 0 {
		t.Fatalf("stdout must hold only the dispatch line: %s", out)
	}
	if !strings.Contains(errText, "dispatch: compacted 'worker' in ") {
		t.Fatalf("missing the compacted note on stderr: %s", errText)
	}
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(f.bin, "herdr.json"))
	if err != nil {
		t.Fatal(err)
	}
	if n := countArgvPrefix(calls, []string{"agent", "prompt", "worker"}); n != 1 {
		t.Fatalf("prompt calls=%d want 1: %#v", n, calls)
	}
	if n := countArgv(calls, []string{"pane", "send-text", "w0test:p0a", "/compact"}); n != 1 {
		t.Fatalf("send-text calls=%d want 1: %#v", n, calls)
	}
	if n := countArgv(calls, []string{"pane", "send-keys", "w0test:p0a", "Enter"}); n != 1 {
		t.Fatalf("Enter calls=%d want 1 (only the menu's /compact gets the Enter): %#v", n, calls)
	}
	// The menu read is the first visible read, before the Enter.
	var firstVisible, enterIdx = -1, -1
	for i, c := range calls {
		if enterIdx < 0 && reflect.DeepEqual(c.Argv, []string{"pane", "send-keys", "w0test:p0a", "Enter"}) {
			enterIdx = i
		}
		if firstVisible < 0 && reflect.DeepEqual(c.Argv, []string{"agent", "read", "worker", "--source", "visible"}) {
			firstVisible = i
		}
	}
	if firstVisible < 0 || enterIdx < 0 || firstVisible > enterIdx {
		t.Fatalf("the menu read must precede the Enter: firstVisible=%d enter=%d", firstVisible, enterIdx)
	}
}

// withoutShortTimeoutWarning drops the warning a --timeout under a second
// prints, so a test that uses a short timeout for speed still checks that
// nothing else reached stderr.
func withoutShortTimeoutWarning(stderr string) string {
	kept := []string{}
	for _, line := range strings.Split(stderr, "\n") {
		if strings.Contains(line, "--timeout is in milliseconds;") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}
