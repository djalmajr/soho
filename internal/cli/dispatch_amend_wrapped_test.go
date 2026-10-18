package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// TestDispatchAmendNoWaitWrappedPiHistory covers the rc10 defect: dispatch
// --amend --no-wait to a working pi whose recent-unwrapped screen shows the
// consumed prompt wrapped across history lines (the path split mid-line) must
// exit 0 (queued), send no key, and report no not-received.
func TestDispatchAmendNoWaitWrappedPiHistory(t *testing.T) {
	base := time.Date(2026, 10, 1, 11, 23, 45, 0, time.UTC)
	oldNow := platform.Now
	clock := base
	platform.Now = func() time.Time {
		value := clock
		clock = clock.Add(2 * time.Second)
		return value
	}
	t.Cleanup(func() { platform.Now = oldNow })
	stamp := base.Format("20060102T150405")
	// The screen is a fake-herdr rule, so $ROOT is substituted with the
	// fixture root: the wrapped path is the dispatch's own composed path.
	screen := func(name string) string {
		// The separators must match the composed path: pi prints the
		// dispatch's own path, backslashed on Windows.
		p := "$ROOT" + string(filepath.Separator) + "state" + string(filepath.Separator) + "ws" + string(filepath.Separator) + "briefs" + string(filepath.Separator) + "worker-" + name + ".md"
		c := len(p) / 2 // the wrap splits the path mid-line, like the real screen
		return "is a simple task. Let's just execute it as is.\n" +
			" $ sleep 45 (timeout 60s)\n" +
			" (no output)\n" +
			" Read the file\n" +
			" " + p[:c] + "\n" +
			" " + p[c:] + " and follow it. Esta é só uma sonda do orquestrador: quando\n" +
			" ler isto, responda apenas 'sonda recebida' e não abra o arquivo.\n"
	}

	newAmendFixture := func(t *testing.T, recent string) *dispatchArrivalFixture {
		t.Helper()
		f := newDispatchArrivalFixture(t, "working", 5, 5, "", "0",
			fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: recent})
		// The target is a pi, and the amend needs an earlier dispatch.
		roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\tmode\targs\teffort\n" +
			"worker\tw0test:p0a\tpi\timplementer\talibaba\t0\t\tnow\tqwen3.8-27b\ttask\timplementer\t\t\t\thigh\n"
		if err := os.WriteFile(filepath.Join(f.state, "ws", "agents.tsv"), []byte(roster), 0o600); err != nil {
			t.Fatal(err)
		}
		previous := filepath.Join(f.state, "ws", "reports", "previous.md")
		if err := os.WriteFile(previous, []byte("old report\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.state, "ws", "last-report-worker"), []byte(previous+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return f
	}

	t.Run("the consumed wrapped screen proves the amend and exits 0 without a key", func(t *testing.T) {
		f := newAmendFixture(t, screen(stamp))
		code, out, errText := f.run(t, "worker", f.brief, "--amend", "--no-wait")
		if code != 0 || dispatchOutputStatus(t, out) != "queued" || !strings.Contains(errText, "prompt queued: 'worker' is working") {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
		}
		if _, err := os.Stat(filepath.Join(f.state, "ws", "wait", "worker.queued")); err != nil {
			t.Fatalf("queued marker missing: %v", err)
		}
		if _, err := os.Stat(filepath.Join(f.state, "ws", "wait", "worker.not-received")); !os.IsNotExist(err) {
			t.Fatalf("a not-received marker must not be written")
		}
		calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if got := countDispatchCalls(calls, "send-keys"); got != 0 {
			t.Fatalf("send-keys calls=%d, want 0 (no Enter); calls=%#v", got, calls)
		}
	})
	t.Run("control: a different path wrapped the same way stays not-received", func(t *testing.T) {
		f := newAmendFixture(t, screen("19990101T000000"))
		code, out, errText := f.run(t, "worker", f.brief, "--amend", "--no-wait")
		if code != 15 || dispatchOutputStatus(t, out) != "not-received" || !strings.Contains(errText, "was typed while it was working") {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
		}
		calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if got := countDispatchCalls(calls, "send-keys"); got != 0 {
			t.Fatalf("send-keys calls=%d, want 0; calls=%#v", got, calls)
		}
	})
}
