package wait

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

const capacityCauseScreen = "■ Error: 529 overloaded\n"

// installCapacityWaitFake replaces the fixture's transport rules for the
// next WaitFor call: the real fake binary is unchanged, only its script is.
func installCapacityWaitFake(t *testing.T, bin, state, screen string, extra ...fakecli.Rule) {
	t.Helper()
	rules := []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: fmt.Sprintf(`{"result":{"agent":{"agent_status":%q,"state_change_seq":5}}}`, state)},
		{Argv: []string{"agent", "read", "worker", "--source", "visible"}, ArgvPrefix: true, Stdout: screen},
		{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: screen},
	}
	if _, err := fakecli.Install(t, bin, "herdr", append(rules, extra...)); err != nil {
		t.Fatal(err)
	}
}

func runCapacityWait(t *testing.T, f *queuedProbeFixture, timeout float64) (int, string) {
	t.Helper()
	var out bytes.Buffer
	old := platform.Stdout
	platform.Stdout = &out
	t.Cleanup(func() { platform.Stdout = old })
	rc := WaitFor([]string{"worker"}, f.sd, f.ctx, f.env, timeout, false, t.TempDir())
	platform.Stdout = old
	return rc, out.String()
}

func assertWaitFileAbsent(t *testing.T, f *queuedProbeFixture, name string) {
	t.Helper()
	if _, err := os.Stat(f.marker(name)); !os.IsNotExist(err) {
		t.Fatalf("%s still exists (err=%v)", name, err)
	}
}

func assertWaitFilePresent(t *testing.T, f *queuedProbeFixture, name string) {
	t.Helper()
	if _, err := os.Stat(f.marker(name)); err != nil {
		t.Fatalf("%s missing (err=%v)", name, err)
	}
}

// SOH-461 acceptance: a 529 stop writes the capacity markers but leaves no
// cause while it can still recover; the next working observation retires all
// three provider files. Keep either half wrong and this test fails: keeping
// the early cause write leaves a cause after the first phase, and dropping
// the cause removal in clearProvider leaves one after the second.
func TestCapacityCauseClearedOnRecovery(t *testing.T) {
	f := newQueuedProbeFixture(t, "idle", "5", capacityCauseScreen, nil)

	rc, out := runCapacityWait(t, f, 300)
	if rc != 9 {
		t.Fatalf("capacity wait exit=%d stdout=%q, want the timeout 9 (no terminal status)", rc, out)
	}
	if !bytes.Contains([]byte(out), []byte(`"status":"timeout"`)) || bytes.Contains([]byte(out), []byte(`"status":"capacity"`)) {
		t.Fatalf("capacity wait stdout=%q, want a non-terminal timeout", out)
	}
	assertWaitFilePresent(t, f, "provider")
	assertWaitFilePresent(t, f, "capacity-at")
	assertWaitFileAbsent(t, f, "provider-cause")

	// Seed a stale cause from an earlier terminal outcome: the working
	// observation must retire it even when this capacity run never wrote
	// one. This is the control for the clearProvider half of the fix.
	writeWaitFile(f.sd, "worker", "provider-cause", "stale provider cause\n")
	assertWaitFilePresent(t, f, "provider-cause")

	recoveredBin := t.TempDir()
	installCapacityWaitFake(t, recoveredBin, "working", "still working on the brief\n")
	f.env["PATH"] = recoveredBin
	f.env["SOHO_FAKECLI_CONFIG"] = recoveredBin
	rc, out = runCapacityWait(t, f, 300)
	if rc != 9 {
		t.Fatalf("recovered wait exit=%d stdout=%q, want the timeout 9", rc, out)
	}
	assertWaitFileAbsent(t, f, "provider")
	assertWaitFileAbsent(t, f, "capacity-at")
	assertWaitFileAbsent(t, f, "provider-cause")
}

// SOH-461: the cause survives only to the terminal capacity outcome, where
// the report line reads it back. Retries stay as they were (one continue for
// provider_retries=1).
func TestCapacityExhaustionWritesCauseForReport(t *testing.T) {
	f := newQueuedProbeFixture(t, "idle", "5", capacityCauseScreen, nil)
	bin := t.TempDir()
	installCapacityWaitFake(t, bin, "idle", capacityCauseScreen, fakecli.Rule{
		Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"submitted":true}}`,
	})
	f.env["PATH"] = bin
	f.env["SOHO_FAKECLI_CONFIG"] = bin
	f.env["SOHO_PROVIDER_RETRIES"] = "1"
	f.env["SOHO_PROVIDER_RETRY_DELAY"] = "0"

	rc, out := runCapacityWait(t, f, 3000)
	if rc != 14 {
		t.Fatalf("exhausted wait exit=%d stdout=%q, want 14", rc, out)
	}
	if !bytes.Contains([]byte(out), []byte(`"status":"capacity"`)) || !bytes.Contains([]byte(out), []byte(`"cause":"Error: 529 overloaded"`)) {
		t.Fatalf("exhausted wait stdout=%q, want the capacity status and its cause", out)
	}
	cause, err := os.ReadFile(f.marker("provider-cause"))
	if err != nil || string(cause) != "Error: 529 overloaded\n" {
		t.Fatalf("provider-cause=%q err=%v", cause, err)
	}
	calls, err := fakecli.ReadCalls(filepath.Join(bin, "herdr.calls.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	prompts := 0
	for _, call := range calls {
		if len(call.Argv) >= 2 && call.Argv[0] == "agent" && call.Argv[1] == "prompt" {
			prompts++
		}
	}
	if prompts != 1 {
		t.Fatalf("continue count=%d, want 1 for provider_retries=1", prompts)
	}
}

// The Kashes shape: a working observation clears the marker and the cause,
// and a refusal still on the same screen in the same wait run writes the
// cause again before returning provider-refusal. The shared classifier
// suppresses refusals while the terminal itself reports working, so the
// terminal transition is part of the fixture.
func TestWorkingThenRefusalRewritesCause(t *testing.T) {
	const refusal = "■ We take extra care with some cybersecurity requests\n"
	f := newQueuedProbeFixture(t, "working", "5", refusal, nil)
	writeWaitFile(f.sd, "worker", "provider-cause", "old cause\n")
	rc, _ := runCapacityWait(t, f, 300)
	if rc != 9 {
		t.Fatalf("working wait exit=%d, want the timeout 9", rc)
	}
	assertWaitFileAbsent(t, f, "provider-cause")

	refusalBin := t.TempDir()
	installCapacityWaitFake(t, refusalBin, "idle", refusal)
	f.env["PATH"] = refusalBin
	f.env["SOHO_FAKECLI_CONFIG"] = refusalBin
	rc, out := runCapacityWait(t, f, 3000)
	if rc != 14 {
		t.Fatalf("refusal wait exit=%d stdout=%q, want 14", rc, out)
	}
	if !bytes.Contains([]byte(out), []byte(`"status":"provider-refusal"`)) {
		t.Fatalf("refusal wait stdout=%q, want provider-refusal", out)
	}
	cause, err := os.ReadFile(f.marker("provider-cause"))
	if err != nil || string(cause) != "We take extra care with some cybersecurity requests\n" {
		t.Fatalf("provider-cause=%q err=%v", cause, err)
	}
}
