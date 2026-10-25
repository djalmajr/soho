package cli

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// Verbatim saved after-screen from the DJA-531 incident. The history is
// fixture data, not a path the test accesses. No pre-send capture was supplied.
const codex531SavedAfter = `
• Explored
  └ Search finalizer-v6|global-notes|Trecho da data|Manifests|
           narrative-v4 in review-5-20261025T052407.probe.finalize-
           v6.go
    + Show details

• Os manifests confirmaram que os arquivos inventariados da origem
  e da cópia não mudaram. Vou publicar o relatório com um achado
  P3, verdict pass para o diff e os resultados e limites dos gates
  explicitados.

• Ran GOCACHE=$TMPDIR/gocache-review GOTMPDIR=$TMPDIR go run /User…
  └ (no output)
    + Show details

• /Users/djalmajr/Developer/djalmajr/herdr-soho/.soho/w6X/reports/
  review-5-20261025T052407.md

  Worked for 35m 39s • 05:59

               New activity · ↓ Back to bottom · esc

› Ask Codex to do anything

  GPT-6.1-Sol xhigh · Context 6% used · 475K window · weekly 88% l…
  New activity · Earlier messages available.  enter/esc latest
`

func codex531SyntheticBefore() string {
	// 49 is a controlled high baseline, not an observed incident value.
	return strings.Replace(codex531SavedAfter, "Context 6% used", "Context 49% used", 1)
}

func TestCompactCodexScrolledEvidenceReads(t *testing.T) {
	for _, tc := range []struct {
		name, screen string
		want         int
	}{
		{"saved_after", codex531SavedAfter, 6},
		{"synthetic_before", codex531SyntheticBefore(), 49},
		{"extra_shortcuts", strings.TrimSuffix(codex531SyntheticBefore(), "\n") + " · ? shortcuts\n", 49},
		{"clipped_after_used", strings.Replace(codex531SyntheticBefore(), "Context 49% used · 475K window · weekly 88% l…", "Context 49% used · …", 1), 49},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, ok := compactCodexContext(tc.screen)
			t.Logf("footer=(%d, %v), want=(%d, true)", n, ok, tc.want)
			if !ok || n != tc.want {
				t.Fatalf("footer=(%d, %v), want=(%d, true)", n, ok, tc.want)
			}
		})
	}
}

func TestCompactCodexScrolledSavedScreen(t *testing.T) {
	for _, tc := range []struct {
		name, before, after string
		code                int
	}{
		{"drop", codex531SyntheticBefore(), codex531SavedAfter, 0},
		{"extra_shortcuts", strings.TrimSuffix(codex531SyntheticBefore(), "\n") + " · ? shortcuts\n", codex531SavedAfter, 0},
		{"clipped_base", strings.Replace(codex531SyntheticBefore(), "Context 49% used · 475K window · weekly 88% l…", "Context 49% used · …", 1), codex531SavedAfter, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeFastClock(t, 6*time.Second)
			f := newCompactFixture(t, "codex", []fakecli.Rule{
				{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("idle", 1)},
				{Argv: compactReadArgv("worker"), Call: 1, Stdout: tc.before},
				{Argv: compactReadArgv("worker"), Stdout: tc.after},
				{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
				{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
			})
			code, out, errText := f.run(t, "compact", "worker", "--timeout", "500")
			if code != tc.code {
				t.Fatalf("code=%d want %d out=%s stderr=%s", code, tc.code, out, errText)
			}
			want := "compacted"
			if tc.code == 9 {
				want = "timeout"
			}
			if value := compactJSON(t, out); value["status"] != want {
				t.Fatalf("json=%v want %s", value, want)
			}
			calls := f.calls(t)
			if countArgv(calls, []string{"pane", "send-text", "p1", "/compact"}) != 1 || countArgv(calls, []string{"pane", "send-keys", "p1", "Enter"}) != 1 || countArgv(calls, []string{"pane", "send-keys", "p1", "ctrl+u"}) != 0 {
				t.Fatalf("unexpected pane effects: %#v", calls)
			}
			read, send, enter, poll := -1, -1, -1, -1
			for i, call := range calls {
				if len(call.Argv) >= 2 && call.Argv[0] == "agent" && call.Argv[1] == "read" {
					if read < 0 {
						read = i
					} else if poll < 0 {
						poll = i
					}
				}
				if len(call.Argv) >= 2 && call.Argv[0] == "pane" {
					if call.Argv[1] == "send-text" {
						send = i
					}
					if call.Argv[1] == "send-keys" {
						enter = i
					}
				}
			}
			if !(read >= 0 && read < send && send < enter && enter < poll) {
				t.Fatalf("expected base read < send-text < Enter < proof read: %#v", calls)
			}
		})
	}
}

func TestDispatchCompactCodexScrolledSavedScreenSendsBrief(t *testing.T) {
	f := newDispatchArrivalFixture(t, "working", 1, 2, "$CURRENT_PATHS", "0",
		fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
		fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Call: 2, Stdout: compactStateJSON("idle", 1)},
		fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Call: 3, Stdout: compactStateJSON("idle", 1)},
		fakecli.Rule{Argv: compactReadArgv("worker"), Call: 1, Stdout: codex531SyntheticBefore()},
		fakecli.Rule{Argv: compactReadArgv("worker"), Call: 2, Stdout: codex531SavedAfter},
		fakecli.Rule{Argv: []string{"pane", "send-text", "w0test:p0a", "/compact"}, Stdout: `{"result":{}}`},
		fakecli.Rule{Argv: []string{"pane", "send-keys", "w0test:p0a", "Enter"}, Stdout: `{"result":{}}`},
	)
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait", "--compact")
	if code != 0 || dispatchLastLineStatus(t, out) != "submitted" || !strings.Contains(errText, "dispatch: compacted 'worker' in ") {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(f.bin, "herdr.json"))
	if err != nil {
		t.Fatal(err)
	}
	if countArgvPrefix(calls, []string{"agent", "prompt", "worker"}) != 1 {
		t.Fatalf("expected one brief prompt after compaction: %#v", calls)
	}
}
