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
	if _, err := fakecli.Install(t, f.bin, "hc", []fakecli.Rule{
		{Argv: []string{"get", "t1", "--json"}, ArgvPrefix: true, Stdout: hcWaitCaller},
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWaitWorker},
		{Argv: []string{"read", "worker", "--lines", "0"}, ArgvPrefix: true, Stdout: hcWaitReadRecord},
		{Argv: []string{"read", "worker", "--lines", "40"}, ArgvPrefix: true, Stdout: hcWaitReadRecord},
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
	sends := 0
	for _, c := range calls {
		if len(c.Argv) > 0 && c.Argv[0] == "send" {
			sends++
		}
	}
	return sends
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
