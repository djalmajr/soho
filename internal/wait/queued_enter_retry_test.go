package wait

// The queued marker's Enter retry (the second Enter site of the wait, after
// retryNotReceived). Every case keeps a pending queued marker and a
// recognized claude composer holding the stored prompt path, so the probe
// reaches the window, attempt and dialog guards of that path. Each case
// counts the fake's recorded agent send-keys calls: the probe is
// synchronous, so the call log is the event the assertions read, no sleeps.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/provider"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

const queuedRetryPrompt = "/tmp/worker-brief.md"

// queuedRetryProbe sets a fixed clock at 2000000000, captures the wait's
// warnings, and builds the queued probe fixture on the given screen.
func queuedRetryProbe(t *testing.T, screen string, markers map[string]string, extra ...fakecli.Rule) (*queuedProbeFixture, *bytes.Buffer) {
	t.Helper()
	fixed := time.Unix(2_000_000_000, 0)
	oldNow := platform.Now
	platform.Now = func() time.Time { return fixed }
	stderr := &bytes.Buffer{}
	oldErr := platform.Stderr
	platform.Stderr = stderr
	oldFriction := frictionLogFile
	SetFrictionLogFile(filepath.Join(t.TempDir(), "friction.log"))
	t.Cleanup(func() {
		platform.Now = oldNow
		platform.Stderr = oldErr
		SetFrictionLogFile(oldFriction)
	})
	f := newQueuedProbeFixture(t, "idle", "5", screen, markers, extra...)
	f.env["SOHO_PROMPT_CHECK_SECONDS"] = "15"
	return f, stderr
}

// queuedRetryScreen is the claude box holding the stored path. It keeps
// the composer proof (inputRegionHolds), so each case exercises the guard
// it names, not the composer refusal.
func queuedRetryScreen(t *testing.T) string {
	t.Helper()
	screen := claudeBoxScreen("Welcome to the worker\n", "❯ Read the file "+queuedRetryPrompt+" in full and execute it.", "  [Opus 5.5] 67% [main*]\n")
	if !inputRegionHolds("claude", screen, queuedRetryPrompt) {
		t.Fatalf("fixture screen does not hold the stored path in a recognized composer:\n%s", screen)
	}
	return screen
}

func readMarker(t *testing.T, f *queuedProbeFixture, name string) string {
	t.Helper()
	raw, err := os.ReadFile(f.marker(name))
	if err != nil {
		t.Fatalf("%s marker: %v", name, err)
	}
	return strings.TrimSpace(string(raw))
}

// TestQueuedEnterRetryAfterWindowSendsOneEnter: the queued epoch is past the
// prompt window and no attempt was made. Exactly one Enter goes out, the
// success warning names attempt 1 of 3, and enter-retry records 1 at now.
func TestQueuedEnterRetryAfterWindowSendsOneEnter(t *testing.T) {
	f, stderr := queuedRetryProbe(t, queuedRetryScreen(t), map[string]string{
		"queued": "1999999900 5 " + queuedRetryPrompt + "\n",
	})
	if got := f.probe(""); got != "working" {
		t.Fatalf("probe=%q, want working after the queued retry Enter", got)
	}
	if n := countEnterKeys(f); n != 1 {
		t.Fatalf("send-keys calls=%d, want exactly one; calls=%#v", n, f.calls())
	}
	if !hasCall(f.calls(), "agent send-keys worker enter") {
		t.Fatalf("the Enter did not go to the worker: %#v", f.calls())
	}
	want := "soho: warning: prompt to 'worker' was still in its input box; sent Enter again (1 of 3)\n"
	if stderr.String() != want {
		t.Fatalf("stderr=%q, want %q", stderr.String(), want)
	}
	if got := readMarker(t, f, "enter-retry"); got != "1 2000000000" {
		t.Fatalf("enter-retry=%q, want \"1 2000000000\"", got)
	}
}

// TestQueuedEnterRetryFailedSendConsumesAttempt: the send-keys call fails.
// The failure warning names attempt 1 of 3, the attempt is consumed
// (enter-retry 1 at now), and a second probe inside the new interval sends
// nothing more.
func TestQueuedEnterRetryFailedSendConsumesAttempt(t *testing.T) {
	fail := fakecli.Rule{Argv: []string{"agent", "send-keys", "worker", "enter"}, Code: 1, Stderr: `{"error":{"code":"send_failed","message":"the fake send-keys failed"}}`}
	f, stderr := queuedRetryProbe(t, queuedRetryScreen(t), map[string]string{
		"queued": "1999999900 5 " + queuedRetryPrompt + "\n",
	}, fail)
	if got := f.probe(""); got != "working" {
		t.Fatalf("probe=%q, want working after the failed retry", got)
	}
	if n := countEnterKeys(f); n != 1 {
		t.Fatalf("send-keys calls=%d, want one failed attempt; calls=%#v", n, f.calls())
	}
	want := "soho: warning: prompt to 'worker' was still in its input box; Enter retry 1 of 3 did not send\n"
	if stderr.String() != want {
		t.Fatalf("stderr=%q, want %q", stderr.String(), want)
	}
	if got := readMarker(t, f, "enter-retry"); got != "1 2000000000" {
		t.Fatalf("enter-retry=%q, want the attempt consumed as \"1 2000000000\"", got)
	}
}

// TestQueuedEnterRetryLimitReachedSendsNothing: three attempts are already
// spent and their interval has passed. No Enter goes out and the result is
// not-received. The control with two attempts spent sends the third.
func TestQueuedEnterRetryLimitReachedSendsNothing(t *testing.T) {
	t.Run("three spent", func(t *testing.T) {
		f, stderr := queuedRetryProbe(t, queuedRetryScreen(t), map[string]string{
			"queued":      "1999999900 5 " + queuedRetryPrompt + "\n",
			"enter-retry": "3 1999999900\n",
		})
		if got := f.probe(""); got != "not-received" {
			t.Fatalf("probe=%q, want not-received after three attempts", got)
		}
		if n := countEnterKeys(f); n != 0 {
			t.Fatalf("send-keys calls=%d, want none; calls=%#v", n, f.calls())
		}
		if stderr.Len() != 0 {
			t.Fatalf("stderr=%q, want no retry warning", stderr.String())
		}
		if got := readMarker(t, f, "enter-retry"); got != "3 1999999900" {
			t.Fatalf("enter-retry=%q, want unchanged", got)
		}
	})
	t.Run("control: two spent", func(t *testing.T) {
		f, stderr := queuedRetryProbe(t, queuedRetryScreen(t), map[string]string{
			"queued":      "1999999900 5 " + queuedRetryPrompt + "\n",
			"enter-retry": "2 1999999900\n",
		})
		if got := f.probe(""); got != "working" {
			t.Fatalf("probe=%q, want working", got)
		}
		if n := countEnterKeys(f); n != 1 {
			t.Fatalf("send-keys calls=%d, want the third attempt; calls=%#v", n, f.calls())
		}
		if !strings.Contains(stderr.String(), "sent Enter again (3 of 3)") {
			t.Fatalf("stderr=%q, want attempt 3 of 3", stderr.String())
		}
	})
}

// TestQueuedEnterRetryInsideWindowSendsNothing: the queued epoch is 5s old
// under a 15s prompt window. No Enter, the result is working and no attempt
// is recorded. The control at exactly the window's edge (15s) sends one.
func TestQueuedEnterRetryInsideWindowSendsNothing(t *testing.T) {
	t.Run("inside the window", func(t *testing.T) {
		f, stderr := queuedRetryProbe(t, queuedRetryScreen(t), map[string]string{
			"queued": "1999999995 5 " + queuedRetryPrompt + "\n",
		})
		if got := f.probe(""); got != "working" {
			t.Fatalf("probe=%q, want working inside the prompt window", got)
		}
		if n := countEnterKeys(f); n != 0 {
			t.Fatalf("send-keys calls=%d, want none; calls=%#v", n, f.calls())
		}
		if stderr.Len() != 0 {
			t.Fatalf("stderr=%q, want no retry warning", stderr.String())
		}
		if _, err := os.Stat(f.marker("enter-retry")); !os.IsNotExist(err) {
			t.Fatalf("enter-retry must not be written inside the window: %v", err)
		}
	})
	t.Run("control: window elapsed", func(t *testing.T) {
		f, _ := queuedRetryProbe(t, queuedRetryScreen(t), map[string]string{
			"queued": "1999999985 5 " + queuedRetryPrompt + "\n",
		})
		if got := f.probe(""); got != "working" {
			t.Fatalf("probe=%q, want working", got)
		}
		if n := countEnterKeys(f); n != 1 {
			t.Fatalf("send-keys calls=%d, want one at the window's edge; calls=%#v", n, f.calls())
		}
	})
}

// TestQueuedEnterRetryRefusesKnownDialog: the claude box holds the stored
// path and an approval dialog sits below it (provider.DialogUI is not
// empty). inputRegionHolds already refuses a screen with a known dialog,
// so the probe settles not-received in the composer refusal, before the
// window and attempt guards: no Enter goes out and no attempt is recorded.
func TestQueuedEnterRetryRefusesKnownDialog(t *testing.T) {
	screen := claudeBoxScreen("Welcome to the worker\n", "❯ Read the file "+queuedRetryPrompt+" in full and execute it.", "Do you want to make this edit?\n  1. Yes\n  2. No\n")
	if provider.DialogUI("claude", screen) == "" {
		t.Fatalf("fixture screen carries no known dialog:\n%s", screen)
	}
	if region, ok := provider.ComposerRegion("claude", screen); !ok || !provider.ComposerHoldsPathFor("claude", region, queuedRetryPrompt) {
		t.Fatalf("fixture composer does not hold the stored path:\n%s", screen)
	}
	f, stderr := queuedRetryProbe(t, screen, map[string]string{
		"queued": "1999999900 5 " + queuedRetryPrompt + "\n",
	})
	if got := f.probe(""); got != "not-received" {
		t.Fatalf("probe=%q, want not-received under the dialog", got)
	}
	if n := countEnterKeys(f); n != 0 {
		t.Fatalf("the dialog got %d key(s); want none; calls=%#v", n, f.calls())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr=%q, want no retry warning", stderr.String())
	}
	if _, err := os.Stat(f.marker("enter-retry")); !os.IsNotExist(err) {
		t.Fatalf("enter-retry must not be written under a dialog: %v", err)
	}
}
