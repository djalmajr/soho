package wait

// DJA-604 2b-4/5: wait's provider-capacity continue under transport=hc.
// The continue prompt goes out through hc send; a send that exits 9 (the
// worker sits on a person's prompt) returns blocked with the warning and is
// not counted as exhausted, and one that exits 10 (the idle guard found the
// worker working) returns working with the retry file kept.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

const (
	hcWaitCaller  = `{"address":"t1","state":"live","labels":{"soho.session":"sX"}}`
	hcWaitWorker  = `{"address":"t7","name":"worker","state":"live","turn":5,"reported":true,"status":{"state":"idle"}}`
	hcWaitBlocked = `{"error":{"kind":"blocked","message":"worker is blocked"}}`
	hcWaitBusy    = `{"error":{"kind":"status_mismatch","message":"the terminal is working"}}`
)

// The guard probe fixtures are the read-only capability probes of the
// capacity continue's ordinary (non-queue) guarded send — `hc send --help`
// (the local parser) and `hc schema --json` (the serving core's OpenRPC
// document) — answered with the retained primary's (de97ea5, v0.1.20) OLD
// idle/working capability, regenerated from the primary's generated
// docs/commands.md and schema/hc.openrpc.json.
const (
	hcWaitGuardHelp   = "Paste text into a terminal and press Enter (waits for a person's unfinished draft)\n\nUsage: hc send [OPTIONS] <TERMINAL> <TEXT>\n\nArguments:\n  <TERMINAL>  The terminal: `t7`, a name, or `machine/t7`\n  <TEXT>      Pasted, then submitted with Enter\n\nOptions:\n      --force              Type even if the program is blocked (it may answer its dialog)\n      --json               JSON output even in a terminal\n      --if <STATUS>        Only type if the status is exactly this [possible values: idle, working]\n      --wait               Then wait for the turn this send started\n      --timeout <TIMEOUT>  With wait: seconds before it checks in (default: config, else 180)\n  -h, --help               Print help\n"
	hcWaitGuardSchema = "{\"openrpc\":\"1.3.2\",\"info\":{\"title\":\"herdr-core\",\"version\":\"0.1.20\"},\"components\":{\"schemas\":{\"IdleOrWorking\":{\"enum\":[\"idle\",\"working\"],\"type\":\"string\"}}},\"methods\":[{\"description\":\"Paste text and press Enter after a person sends or clears their draft (at most 60 s since their last key), otherwise after ~3 s of quiet. Keys pressed during delivery are held, then delivered. Refuses a blocked program unless `force`.\",\"errors\":[{\"code\":-32000,\"data\":{\"kind\":\"not_found\"},\"message\":\"not_found\"},{\"code\":-32000,\"data\":{\"kind\":\"machine_unknown\"},\"message\":\"machine_unknown\"},{\"code\":-32000,\"data\":{\"kind\":\"finished\"},\"message\":\"finished\"},{\"code\":-32000,\"data\":{\"kind\":\"unavailable\"},\"message\":\"unavailable\"},{\"code\":-32000,\"data\":{\"kind\":\"blocked\"},\"message\":\"blocked\"},{\"code\":-32000,\"data\":{\"kind\":\"status_mismatch\"},\"message\":\"status_mismatch\"},{\"code\":-32602,\"data\":{\"kind\":\"invalid_params\"},\"message\":\"invalid_params\"},{\"code\":-32000,\"data\":{\"kind\":\"config\"},\"message\":\"config\"},{\"code\":-32000,\"data\":{\"kind\":\"cancelled\"},\"message\":\"cancelled\"}],\"name\":\"terminal.send\",\"paramStructure\":\"by-name\",\"params\":[{\"description\":\"Who is sending (`HC_TERMINAL_ID`, else the calling process's parent), so a\\nbare wait from the same caller waits for this send's turn.\",\"name\":\"caller\",\"required\":false,\"schema\":{\"description\":\"Who is sending (`HC_TERMINAL_ID`, else the calling process's parent), so a\\nbare wait from the same caller waits for this send's turn.\",\"type\":[\"string\",\"null\"]}},{\"description\":\"Type even if the program is blocked (it may answer its dialog).\",\"name\":\"force\",\"required\":false,\"schema\":{\"default\":false,\"description\":\"Type even if the program is blocked (it may answer its dialog).\",\"type\":\"boolean\"}},{\"description\":\"Only type if the status is exactly this.\",\"name\":\"if\",\"required\":false,\"schema\":{\"anyOf\":[{\"$ref\":\"#/components/schemas/IdleOrWorking\"},{\"type\":\"null\"}],\"description\":\"Only type if the status is exactly this.\"}},{\"description\":\"The terminal: `t7`, a name, or `machine/t7`.\",\"name\":\"terminal\",\"required\":true,\"schema\":{\"description\":\"The terminal: `t7`, a name, or `machine/t7`.\",\"type\":\"string\"}},{\"description\":\"Pasted, then submitted with Enter.\",\"name\":\"text\",\"required\":true,\"schema\":{\"description\":\"Pasted, then submitted with Enter.\",\"type\":\"string\"}},{\"description\":\"With wait: seconds before it checks in (default: config, else 180).\",\"name\":\"timeout\",\"required\":false,\"schema\":{\"description\":\"With wait: seconds before it checks in (default: config, else 180).\",\"format\":\"double\",\"type\":[\"number\",\"null\"]}},{\"description\":\"Then wait for the turn this send started.\",\"name\":\"wait\",\"required\":false,\"schema\":{\"default\":false,\"description\":\"Then wait for the turn this send started.\",\"type\":\"boolean\"}}],\"result\":{\"name\":\"result\",\"schema\":{\"$ref\":\"#/components/schemas/SendResult\"}}}]}"
)

// hcWaitReadRecord is the hc read record the backend's Read parses: the
// screen inside its text field (the hc read returns JSON, never bare text).
const hcWaitReadRecord = `{"text":"API Error: 529 {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}"}`

type waitHcFixture struct {
	sd  string
	bin string
	env platform.Env
	ctx *core.Config
}

// newWaitHcFixture sets up a wait probe for transport=hc: the caller
// terminal t1 of session sX, the worker roster row (its pane field is the
// hc address t7), the capacity screen, and the scripted send outcome.
func newWaitHcFixture(t *testing.T, sendCode int, sendStderr string) *waitHcFixture {
	t.Helper()
	base := t.TempDir()
	f := &waitHcFixture{sd: filepath.Join(base, "state", "sX"), bin: filepath.Join(base, "bin"), ctx: &core.Config{Entries: map[string]core.ConfigEntry{}}}
	for _, dir := range []string{filepath.Join(f.sd, "wait"), filepath.Join(base, "home")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(f.sd, "agents.tsv"), []byte("# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\nworker\tt7\tcodex\timplementer\topenai\t1\t\t\tgpt-5\ttask\timplementer\t\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(f.sd, "reports", "worker.md")
	if err := os.MkdirAll(filepath.Dir(report), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.sd, "last-report-worker"), []byte(report+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The enter-retry marker exists before the run, so the blocked and
	// busy outcomes are pinned on a file that must be kept (the exhausted
	// path removes it).
	if err := os.WriteFile(filepath.Join(f.sd, "wait", "worker.enter-retry"), []byte("0 2000000000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The read-only capability probes precede the broad send rule (first
	// match wins): the guarded send proves its --if guard before it is
	// issued, with the primary's OLD idle/working capability.
	if _, err := fakecli.Install(t, f.bin, "hc", []fakecli.Rule{
		{Argv: []string{"get", "t1", "--json"}, ArgvPrefix: true, Stdout: hcWaitCaller},
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWaitWorker},
		{Argv: []string{"read", "worker", "--lines", "0"}, ArgvPrefix: true, Stdout: hcWaitReadRecord},
		{Argv: []string{"read", "worker", "--lines", "40"}, ArgvPrefix: true, Stdout: hcWaitReadRecord},
		{Argv: []string{"send", "--help"}, Stdout: hcWaitGuardHelp},
		{Argv: []string{"schema", "--json"}, Stdout: hcWaitGuardSchema},
		{Argv: []string{"send", "--if", "idle", "--json", "--", "worker"}, ArgvPrefix: true, Code: sendCode, Stderr: sendStderr},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fakecli.Install(t, f.bin, "herdr", nil); err != nil {
		t.Fatal(err)
	}
	f.env = platform.Env{
		"PATH": f.bin, "HOME": filepath.Join(base, "home"), "USERPROFILE": filepath.Join(base, "home"),
		"TMPDIR": base, "SOHO_DIR": filepath.Join(base, "state"),
		"SOHO_FAKECLI_CONFIG": f.bin,
		"SOHO_TRANSPORT":      "hc", "HC_TERMINAL_ID": "t1",
		"SOHO_WAIT_POLL_MS":     "1",
		"SOHO_PROVIDER_RETRIES": "1", "SOHO_PROVIDER_RETRY_DELAY": "0",
	}
	fixed := time.Unix(2_000_000_000, 0)
	oldNow := platform.Now
	platform.Now = func() time.Time { return fixed }
	t.Cleanup(func() { platform.Now = oldNow })
	return f
}

func (f *waitHcFixture) probe(t *testing.T) (string, string) {
	t.Helper()
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, stderr strings.Builder
	platform.Stdout, platform.Stderr = &out, &stderr
	state := ProbeAgent(f.sd, "worker", "", f.ctx, f.env)
	platform.Stdout, platform.Stderr = oldOut, oldErr
	return state, out.String() + stderr.String()
}

func (f *waitHcFixture) hcSends(t *testing.T) int {
	t.Helper()
	calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "hc.calls.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatal(err)
	}
	// The read-only send --help capability probe is not terminal input and
	// is not counted.
	sends := 0
	for _, c := range calls {
		if len(c.Argv) > 0 && c.Argv[0] == "send" && !(len(c.Argv) == 2 && c.Argv[1] == "--help") {
			sends++
		}
	}
	return sends
}

// hcGuardProbeCalls counts the read-only capability probes the guarded
// send issued: the send --help parse probe and the schema --json serving
// probe.
func (f *waitHcFixture) hcGuardProbeCalls(t *testing.T) (help, schema int) {
	t.Helper()
	calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "hc.calls.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0
		}
		t.Fatal(err)
	}
	for _, c := range calls {
		switch {
		case len(c.Argv) == 2 && c.Argv[0] == "send" && c.Argv[1] == "--help":
			help++
		case len(c.Argv) == 2 && c.Argv[0] == "schema" && c.Argv[1] == "--json":
			schema++
		}
	}
	return help, schema
}

// TestWaitHcCapacityBlockedContinue is the blocked outcome: the capacity
// continue's send exits 9 and the probe returns blocked with the warning —
// not capacity, and the attempt is not counted as exhausted.
func TestWaitHcCapacityBlockedContinue(t *testing.T) {
	f := newWaitHcFixture(t, 9, hcWaitBlocked)
	if state, text := f.probe(t); state != "working" {
		t.Fatalf("probe 1 = %q, want working (text=%q)", state, text)
	}
	if state, text := f.probe(t); state != "working" {
		t.Fatalf("probe 2 = %q, want working (the capacity-at marker) (text=%q)", state, text)
	}
	state, text := f.probe(t)
	if state != "blocked" {
		t.Fatalf("probe 3 = %q, want blocked (text=%q)", state, text)
	}
	if want := "provider capacity: 'worker' is blocked; a person must answer its prompt before the continue"; !strings.Contains(text, want) {
		t.Fatalf("output=%q, want the blocked warning %q", text, want)
	}
	if got := f.hcSends(t); got != 1 {
		t.Fatalf("sends=%d, want exactly one", got)
	}
	if help, schema := f.hcGuardProbeCalls(t); help != 1 || schema != 1 {
		t.Fatalf("capability probes help=%d schema=%d, want the one read-only pair before the send", help, schema)
	}
	// Not counted as exhausted: the retry file is kept for the next poll,
	// unchanged, and the retry counter was never written.
	if got, err := os.ReadFile(filepath.Join(f.sd, "wait", "worker.enter-retry")); err != nil || string(got) != "0 2000000000\n" {
		t.Fatalf("the enter-retry file was changed on the blocked continue: %q (%v)", got, err)
	}
	if _, err := os.ReadFile(filepath.Join(f.sd, "wait", "worker.capacity-retries")); err == nil {
		t.Fatal("the blocked continue was counted as a retry")
	}
	if _, err := os.Stat(filepath.Join(f.bin, "herdr.calls.jsonl")); err == nil {
		t.Fatal("herdr was called under transport=hc")
	}
}

// TestWaitHcCapacityBusyContinue is the busy outcome: the capacity
// continue's send exits 10 and the probe returns working with the retry
// file kept for the next poll.
func TestWaitHcCapacityBusyContinue(t *testing.T) {
	f := newWaitHcFixture(t, 10, hcWaitBusy)
	if state, text := f.probe(t); state != "working" {
		t.Fatalf("probe 1 = %q, want working (text=%q)", state, text)
	}
	if state, text := f.probe(t); state != "working" {
		t.Fatalf("probe 2 = %q, want working (the capacity-at marker) (text=%q)", state, text)
	}
	state, text := f.probe(t)
	if state != "working" {
		t.Fatalf("probe 3 = %q, want working (the busy send is not exhausted) (text=%q)", state, text)
	}
	if got := f.hcSends(t); got != 1 {
		t.Fatalf("sends=%d, want exactly one (no retry within the probe)", got)
	}
	if help, schema := f.hcGuardProbeCalls(t); help != 1 || schema != 1 {
		t.Fatalf("capability probes help=%d schema=%d, want the one read-only pair before the send", help, schema)
	}
	// The retry file is kept for the next poll, unchanged.
	if got, err := os.ReadFile(filepath.Join(f.sd, "wait", "worker.enter-retry")); err != nil || string(got) != "0 2000000000\n" {
		t.Fatalf("the enter-retry file was changed on the busy continue: %q (%v)", got, err)
	}
	if _, err := os.ReadFile(filepath.Join(f.sd, "wait", "worker.capacity-retries")); err == nil {
		t.Fatal("the busy continue was counted as a retry")
	}
	if _, err := os.Stat(filepath.Join(f.bin, "herdr.calls.jsonl")); err == nil {
		t.Fatal("herdr was called under transport=hc")
	}
}
