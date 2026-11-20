package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

const modelSetHeader = "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\tmode\targs\teffort\n"

func modelSetRow(name, pane, kind, model, effort string) string {
	return strings.Join([]string{name, pane, kind, "implementer", "", "0", "/work", "now", model, "task", "implementer", "build", "", "native-args", effort}, "\t")
}

func newModelSetFixture(t *testing.T, kind string, rules []fakecli.Rule) *compactFixture {
	t.Helper()
	f := newCompactFixture(t, kind, rules)
	roster := modelSetHeader + modelSetRow("worker", "p1", kind, "codex/old-model", "high") + "\n" +
		modelSetRow("other", "p2", "pi", "codex/other-model", "low") + "\n"
	if err := os.WriteFile(filepath.Join(f.root, "state", "ws", "agents.tsv"), []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

func modelSetRules(screen string) []fakecli.Rule {
	return []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("idle", 1)},
		{Argv: compactReadArgv("worker"), Stdout: screen},
		{Argv: []string{"pane", "send-keys", "p1"}, ArgvPrefix: true, Stdout: `{"result":{}}`},
	}
}

func modelSetScreen(model, effort, draft string) string {
	sep := strings.Repeat("─", 66)
	return "scrollback above the box\n" + sep + "\n" + draft + "\n" + sep + "\n" +
		"~/project (main)\n" +
		"↑5.2M ↓43k 40.3%/262k (auto)                         " + model + " • " + effort + "\n"
}

func modelSetShortClock(t *testing.T) {
	t.Helper()
	timeout, interval := modelSetVerifyTimeout, modelSetPollInterval
	modelSetVerifyTimeout, modelSetPollInterval = 300*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { modelSetVerifyTimeout, modelSetPollInterval = timeout, interval })
}

func modelSetKeys(t *testing.T, f *compactFixture, pane string) []string {
	t.Helper()
	var keys []string
	for _, call := range f.calls(t) {
		want := []string{"pane", "send-keys", pane}
		if len(call.Argv) == len(want)+1 && reflect.DeepEqual(call.Argv[:len(want)], want) {
			keys = append(keys, call.Argv[len(want)])
		}
	}
	return keys
}

func modelSetRunRoster(t *testing.T, f *compactFixture) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.root, "state", "ws", "agents.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestModelSetKeySequenceAndRosterSuccess(t *testing.T) {
	model := "codex/gpt-6.2-sol"
	f := newModelSetFixture(t, "pi", modelSetRules(modelSetScreen("(codex) gpt-6.2-sol", "high", "")))
	code, out, stderr := f.run(t, "model-set", "worker", model)
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, stderr)
	}
	value := modelSetJSON(t, out)
	if value["agent"] != "worker" || value["model"] != model || value["effort"] != "high" || value["verified"] != true {
		t.Fatalf("json=%v", value)
	}
	want := []string{"/", "m", "o", "d", "e", "l", "space", "c", "o", "d", "e", "x", "/", "g", "p", "t", "-", "6", ".", "2", "-", "s", "o", "l", "Enter", "Enter"}
	if got := modelSetKeys(t, f, "p1"); !reflect.DeepEqual(got, want) {
		t.Fatalf("keys=%q\nwant=%q", got, want)
	}
	if got := modelSetKeys(t, f, "p2"); len(got) != 0 {
		t.Fatalf("keys went to another worker: %q", got)
	}
	rows := core.RosterRows(filepath.Join(f.root, "state", "ws"))
	if len(rows) != 2 {
		t.Fatalf("roster rows=%d", len(rows))
	}
	fields := strings.Split(rows[0], "\t")
	if len(fields) != 15 || fields[8] != model || fields[14] != "high" {
		t.Fatalf("worker row=%q", fields)
	}
	if got := strings.Split(rows[1], "\t"); got[8] != "codex/other-model" || got[14] != "low" {
		t.Fatalf("other worker row changed=%q", got)
	}
}

func TestModelSetWithEffortKeySequence(t *testing.T) {
	model := "codex/gpt-6.2-sol"
	f := newModelSetFixture(t, "pi", modelSetRules(modelSetScreen("(codex) gpt-6.2-sol", "medium", "")))
	code, out, stderr := f.run(t, "model-set", "worker", model, "--effort", "medium")
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, stderr)
	}
	want := []string{"/", "m", "o", "d", "e", "l", "space"}
	want = append(want, []string{"c", "o", "d", "e", "x", "/", "g", "p", "t", "-", "6", ".", "2", "-", "s", "o", "l", "Enter", "Enter"}...)
	want = append(want, []string{"/", "t", "h", "i", "n", "k", "i", "n", "g", "space", "m", "e", "d", "i", "u", "m", "Enter", "Enter"}...)
	if got := modelSetKeys(t, f, "p1"); !reflect.DeepEqual(got, want) {
		t.Fatalf("keys=%q\nwant=%q", got, want)
	}
	if got := modelSetJSON(t, out)["effort"]; got != "medium" {
		t.Fatalf("effort=%v", got)
	}
	fields := strings.Split(core.RosterRows(filepath.Join(f.root, "state", "ws"))[0], "\t")
	if fields[14] != "medium" {
		t.Fatalf("roster effort=%q", fields[14])
	}
}

func TestModelSetInterruptSendsEscFirst(t *testing.T) {
	f := newModelSetFixture(t, "pi", modelSetRules(modelSetScreen("(openai) gpt-5", "high", "")))
	code, out, stderr := f.run(t, "model-set", "worker", "openai/gpt-5", "--interrupt")
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, stderr)
	}
	keys := modelSetKeys(t, f, "p1")
	if len(keys) == 0 || keys[0] != "esc" {
		t.Fatalf("first key=%q, want esc (all keys %q)", firstKey(keys), keys)
	}
	if strings.Count(strings.Join(keys, "|"), "|Enter|Enter") != 1 {
		t.Fatalf("two Enters missing: %q", keys)
	}
}

func TestModelSetDraftRefusesWithoutTyping(t *testing.T) {
	screen := modelSetScreen("(codex) old-model", "high", "a draft already typed here")
	f := newModelSetFixture(t, "pi", modelSetRules(screen))
	before := modelSetRunRoster(t, f)
	code, out, stderr := f.run(t, "model-set", "worker", "codex/new-model")
	if code != 10 || out != "" || !strings.Contains(stderr, "model-set: 'worker' has text in its input box; nothing was typed") {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, stderr)
	}
	if keys := modelSetKeys(t, f, "p1"); len(keys) != 0 {
		t.Fatalf("typed after a draft refusal: %q", keys)
	}
	if after := modelSetRunRoster(t, f); after != before {
		t.Fatalf("roster changed: %q -> %q", before, after)
	}
}

func TestModelSetNonPiRefuses(t *testing.T) {
	f := newModelSetFixture(t, "codex", nil)
	before := modelSetRunRoster(t, f)
	code, out, stderr := f.run(t, "model-set", "worker", "codex/new-model")
	want := "model-set: 'worker' is codex; only pi switches models live; use soho reopen worker --model codex/new-model"
	if code != 2 || out != "" || !strings.Contains(stderr, want) {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, stderr)
	}
	if calls := f.calls(t); len(calls) != 0 {
		t.Fatalf("transport used after a kind refusal: %#v", calls)
	}
	if after := modelSetRunRoster(t, f); after != before {
		t.Fatalf("roster changed: %q -> %q", before, after)
	}
}

func TestModelSetNeverVerifiedLeavesRosterUnchanged(t *testing.T) {
	modelSetShortClock(t)
	screen := modelSetScreen("(codex) old-model", "high", "")
	f := newModelSetFixture(t, "pi", modelSetRules(screen))
	before := modelSetRunRoster(t, f)
	code, out, stderr := f.run(t, "model-set", "worker", "codex/new-model")
	if code != 1 || out != "" || !strings.Contains(stderr, "never showed (codex) new-model") || !strings.Contains(stderr, "roster unchanged") {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, stderr)
	}
	if after := modelSetRunRoster(t, f); after != before {
		t.Fatalf("roster changed before verification: %q -> %q", before, after)
	}
}

func TestModelSetEffortMismatchLeavesRosterUnchanged(t *testing.T) {
	f := newModelSetFixture(t, "pi", modelSetRules(modelSetScreen("(codex) gpt-6.2-sol", "low", "")))
	before := modelSetRunRoster(t, f)
	code, out, stderr := f.run(t, "model-set", "worker", "codex/gpt-6.2-sol", "--effort", "medium")
	if code != 1 || out != "" || !strings.Contains(stderr, "shows thinking 'low', not 'medium'") {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, stderr)
	}
	if after := modelSetRunRoster(t, f); after != before {
		t.Fatalf("roster changed on a level mismatch: %q -> %q", before, after)
	}
}

func TestModelSetBusyWorkerIsAllowed(t *testing.T) {
	rules := []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("working", 1)}}
	rules = append(rules, modelSetRules(modelSetScreen("(codex) new-model", "high", ""))...)
	f := newModelSetFixture(t, "pi", rules)
	code, out, stderr := f.run(t, "model-set", "worker", "codex/new-model")
	if code != 0 || stderr != "" || modelSetJSON(t, out)["verified"] != true {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, stderr)
	}
	if keys := modelSetKeys(t, f, "p1"); len(keys) == 0 {
		t.Fatal("a busy pi worker received no keys")
	}
}

func TestModelSetWorkerExitsDuringVerification(t *testing.T) {
	modelSetShortClock(t)
	rules := []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
		{Argv: []string{"agent", "get", "worker"}, Call: 2, Stdout: compactStateJSON("gone", 2)},
	}
	rules = append(rules, modelSetRules(modelSetScreen("(codex) new-model", "high", ""))...)
	f := newModelSetFixture(t, "pi", rules)
	before := modelSetRunRoster(t, f)
	code, out, stderr := f.run(t, "model-set", "worker", "codex/new-model")
	if code != 6 || out != "" || !strings.Contains(stderr, "is no longer live") {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, stderr)
	}
	if after := modelSetRunRoster(t, f); after != before {
		t.Fatalf("roster changed after the worker exited: %q -> %q", before, after)
	}
}

func TestModelSetHostileAndUsageRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		argv []string
		want string
	}{
		{"missing", []string{}, modelSetUsage},
		{"unknown flag", []string{"worker", "codex/model", "--bogus"}, "unexpected argument '--bogus'"},
		{"space in model", []string{"worker", "codex/gpt 5"}, "invalid model"},
		{"newline in model", []string{"worker", "codex/gpt\n5"}, "invalid model"},
		{"no provider", []string{"worker", "gpt-5"}, "invalid model"},
		{"unknown effort", []string{"worker", "codex/model", "--effort", "bogus"}, "invalid effort"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newModelSetFixture(t, "pi", nil)
			code, out, stderr := f.run(t, append([]string{"model-set"}, tc.argv...)...)
			if code != 2 || out != "" || !strings.Contains(stderr, tc.want) {
				t.Fatalf("code=%d out=%s stderr=%s", code, out, stderr)
			}
			if calls := f.calls(t); len(calls) != 0 {
				t.Fatalf("transport used after a refusal: %#v", calls)
			}
		})
	}
}

func TestModelSetGoneUnknownAndReadFailure(t *testing.T) {
	t.Run("unknown", func(t *testing.T) {
		f := newModelSetFixture(t, "pi", nil)
		code, out, stderr := f.run(t, "model-set", "missing", "codex/model")
		if code != 3 || out != "" || !strings.Contains(stderr, "is not in this skill's roster") {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, stderr)
		}
		if calls := f.calls(t); len(calls) != 0 {
			t.Fatalf("transport used for an unknown worker: %#v", calls)
		}
	})
	t.Run("gone", func(t *testing.T) {
		f := newModelSetFixture(t, "pi", []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("gone", 1)}})
		code, out, stderr := f.run(t, "model-set", "worker", "codex/model")
		if code != 6 || out != "" || !strings.Contains(stderr, "is no longer live") {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, stderr)
		}
		if keys := modelSetKeys(t, f, "p1"); len(keys) != 0 {
			t.Fatalf("typed into a gone worker: %q", keys)
		}
	})
	t.Run("unreadable screen", func(t *testing.T) {
		f := newModelSetFixture(t, "pi", []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("idle", 1)},
			{Argv: compactReadArgv("worker"), Code: 1, Stderr: "read refused"},
		})
		code, out, stderr := f.run(t, "model-set", "worker", "codex/model")
		if code != 4 || out != "" || !strings.Contains(stderr, "could not read the screen of 'worker'") {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, stderr)
		}
		if keys := modelSetKeys(t, f, "p1"); len(keys) != 0 {
			t.Fatalf("typed after a failed read: %q", keys)
		}
	})
}

func TestModelSetUnavailableWorkerRefuses(t *testing.T) {
	rules := []fakecli.Rule{{
		Argv:   []string{"agent", "get", "worker"},
		Code:   1,
		Stderr: `{"error":{"code":"herdr_failed","message":"boom"}}`,
	}}
	f := newModelSetFixture(t, "pi", rules)
	before := modelSetRunRoster(t, f)
	code, out, stderr := f.run(t, "model-set", "worker", "opencode-go/glm-5.3")
	want := "soho: model-set: agent 'worker' is unavailable: herdr_failed: boom; nothing was typed\n"
	if code != 4 || out != "" || stderr != want {
		t.Fatalf("code=%d out=%q stderr=%q\nwant stderr=%q", code, out, stderr, want)
	}
	gets := 0
	for _, call := range f.calls(t) {
		if reflect.DeepEqual(call.Argv, []string{"agent", "get", "worker"}) {
			gets++
		}
	}
	if gets != 1 {
		t.Fatalf("agent get calls=%d, want 1: %#v", gets, f.calls(t))
	}
	for _, pane := range []string{"p1", "p2"} {
		if keys := modelSetKeys(t, f, pane); len(keys) != 0 {
			t.Fatalf("keys sent to %s after an unavailable refusal: %q", pane, keys)
		}
	}
	if after := modelSetRunRoster(t, f); after != before {
		t.Fatalf("roster changed after an unavailable refusal: %q -> %q", before, after)
	}
}

func TestModelSetKeyFailureAndNowrite(t *testing.T) {
	t.Run("key failure", func(t *testing.T) {
		rules := append([]fakecli.Rule{{Argv: []string{"pane", "send-keys", "p1", "/"}, Code: 1, Stderr: "keys refused"}},
			modelSetRules(modelSetScreen("(codex) new-model", "high", ""))...)
		f := newModelSetFixture(t, "pi", rules)
		before := modelSetRunRoster(t, f)
		code, out, stderr := f.run(t, "model-set", "worker", "codex/new-model")
		if code != 4 || out != "" || !strings.Contains(stderr, "could not type /model") {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, stderr)
		}
		if after := modelSetRunRoster(t, f); after != before {
			t.Fatalf("roster changed after a key failure: %q", before)
		}
	})
	t.Run("nowrite", func(t *testing.T) {
		f := newModelSetFixture(t, "pi", modelSetRules(modelSetScreen("(codex) new-model", "high", "")))
		f.env["SOHO_NOWRITE"] = "1"
		before := modelSetRunRoster(t, f)
		code, out, stderr := f.run(t, "model-set", "worker", "codex/new-model")
		if code != 2 || out != "" || !strings.Contains(stderr, "SOHO_NOWRITE=1 is read-only") {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, stderr)
		}
		if calls := f.calls(t); len(calls) != 0 {
			t.Fatalf("transport used under NOWRITE: %#v", calls)
		}
		if after := modelSetRunRoster(t, f); after != before {
			t.Fatalf("roster changed under NOWRITE: %q", before)
		}
	})
}

func TestModelSetHelp(t *testing.T) {
	f := newModelSetFixture(t, "pi", nil)
	code, out, stderr := f.run(t, "model-set", "--help")
	if code != 0 || stderr != "" || out != "  soho model-set <worker> <provider/model> [--effort <level>] [--interrupt]\n"+
		"                                             # type /model (and /thinking) into a live pi worker through its own terminal, verify the footer, then record the model and effort in the roster; a non-pi worker exits 2, a draft exits 10, an unverified footer exits 1\n" {
		t.Fatalf("help: code=%d out=%q stderr=%q", code, out, stderr)
	}
	code, out, stderr = f.run(t, "help", "model-set")
	if code != 0 || stderr != "" || out != "  soho model-set <worker> <provider/model> [--effort <level>] [--interrupt]\n"+
		"                                             # type /model (and /thinking) into a live pi worker through its own terminal, verify the footer, then record the model and effort in the roster; a non-pi worker exits 2, a draft exits 10, an unverified footer exits 1\n" {
		t.Fatalf("help model-set: code=%d out=%q stderr=%q", code, out, stderr)
	}
}

type modelSetHcFixture struct {
	root  string
	bin   string
	state string
	env   platform.Env
}

func newModelSetHcFixture(t *testing.T, screen string) *modelSetHcFixture {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	state := filepath.Join(root, "state")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(state, "sX"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "skill"), 0o700); err != nil {
		t.Fatal(err)
	}
	roster := modelSetHeader + modelSetRow("worker", "t7", "pi", "codex/old", "high") + "\n" +
		modelSetRow("other", "t8", "pi", "codex/other", "low") + "\n"
	if err := os.WriteFile(filepath.Join(state, "sX", "agents.tsv"), []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	readRec := `{"text":` + strconv.Quote(screen) + `,"complete":true,"how":"screen"}`
	rules := []fakecli.Rule{
		{Argv: []string{"get", "t1", "--json"}, Stdout: `{"address":"t1","state":"live","labels":{"soho.session":"sX"}}`},
		{Argv: []string{"get", "worker", "--json"}, Stdout: `{"address":"t7","name":"worker","state":"live","status":{"state":"idle"}}`},
		{Argv: []string{"read", "worker", "--lines", "0", "--json"}, Stdout: readRec},
		{Argv: []string{"keys", "t7"}, ArgvPrefix: true, Stdout: `{"turn":2}`},
	}
	if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{}
	for _, entry := range fakecli.Env(testutil.CleanEnv(t), bin) {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			env[key] = value
		}
	}
	env["HC_TERMINAL_ID"] = "t1"
	env["SOHO_TRANSPORT"] = "hc"
	env["SOHO_DIR"] = state
	env["HOME"] = root
	env["USERPROFILE"] = root
	env["TMPDIR"] = root
	env["SOHO_SKILL_DIR"] = filepath.Join(root, "skill")
	t.Chdir(root)
	return &modelSetHcFixture{root: root, bin: bin, state: state, env: env}
}

func (f *modelSetHcFixture) run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, stderr bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &stderr
	code := Run(append([]string{"model-set"}, args...), f.env)
	platform.Stdout, platform.Stderr = oldOut, oldErr
	return code, out.String(), stderr.String()
}

func (f *modelSetHcFixture) calls(t *testing.T) []fakecli.Call {
	t.Helper()
	path := filepath.Join(f.bin, "hc.calls.jsonl")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	calls, err := fakecli.ReadCalls(path)
	if err != nil {
		t.Fatal(err)
	}
	return calls
}

func modelSetChars(command string) []string {
	var keys []string
	for _, r := range command {
		if r == ' ' {
			keys = append(keys, "space")
		} else {
			keys = append(keys, string(r))
		}
	}
	return append(keys, "Enter", "Enter")
}

func modelSetHcChars(command string) []string {
	keys := modelSetChars(command)
	keys[len(keys)-1], keys[len(keys)-2] = "enter", "enter"
	return keys
}

func TestModelSetHcTypesEveryCharacter(t *testing.T) {
	f := newModelSetHcFixture(t, modelSetScreen("(opencode-go) glm-5.3", "high", ""))
	code, out, stderr := f.run(t, "worker", "opencode-go/glm-5.3")
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, stderr)
	}
	value := modelSetJSON(t, out)
	if value["agent"] != "worker" || value["model"] != "opencode-go/glm-5.3" || value["effort"] != "high" || value["verified"] != true {
		t.Fatalf("json=%v", value)
	}
	var keys []string
	var readArgv, getArgv int
	for _, call := range f.calls(t) {
		if len(call.Argv) == 4 && call.Argv[0] == "keys" && call.Argv[1] == "t7" {
			keys = append(keys, call.Argv[2])
		}
		if len(call.Argv) == 5 && call.Argv[0] == "read" && call.Argv[1] == "worker" && call.Argv[2] == "--lines" && call.Argv[3] == "0" {
			readArgv++
		}
		if reflect.DeepEqual(call.Argv, []string{"get", "worker", "--json"}) {
			getArgv++
		}
		if len(call.Argv) >= 2 && call.Argv[0] == "keys" && call.Argv[1] != "t7" {
			t.Fatalf("keys went to another terminal: %q", call.Argv)
		}
	}
	want := modelSetHcChars("/model opencode-go/glm-5.3")
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("hc keys=%q\nwant=%q", keys, want)
	}
	if readArgv == 0 || getArgv == 0 {
		t.Fatalf("no hc screen/state read: read=%d get=%d", readArgv, getArgv)
	}
	sd := filepath.Join(f.state, "sX")
	fields := strings.Split(core.RosterRows(sd)[0], "\t")
	if fields[8] != "opencode-go/glm-5.3" || fields[14] != "high" {
		t.Fatalf("worker row=%q", fields)
	}
}

func modelSetHcRoster(t *testing.T, f *modelSetHcFixture) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.state, "sX", "agents.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestModelSetHcNowriteStillRefuses(t *testing.T) {
	f := newModelSetHcFixture(t, modelSetScreen("(opencode-go) glm-5.3", "high", ""))
	f.env["SOHO_NOWRITE"] = "1"
	before := modelSetHcRoster(t, f)
	code, out, stderr := f.run(t, "worker", "opencode-go/glm-5.3")
	if code != 2 || out != "" || !strings.Contains(stderr, "SOHO_NOWRITE=1 is read-only") {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, stderr)
	}
	for _, call := range f.calls(t) {
		if len(call.Argv) > 0 && (call.Argv[0] == "keys" || call.Argv[0] == "read") {
			t.Fatalf("command acted under NOWRITE: %#v", call.Argv)
		}
	}
	if after := modelSetHcRoster(t, f); after != before {
		t.Fatalf("roster changed under NOWRITE: %q", before)
	}
}

func modelSetJSON(t *testing.T, out string) map[string]any {
	t.Helper()
	return compactJSON(t, out)
}

func firstKey(keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	return keys[0]
}
