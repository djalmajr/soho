package wait

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

// piCompactingWaitScreen is assembled from pi 1.4.0 source (the input box
// top border with the threshold auto-compaction label, the footer with the
// (auto) setting token, and the post-compaction summary line; see
// .soho/w6X/reports/build-6-20261029T212047.md §4). It is not a real
// captured screen.
const piCompactingWaitScreen = `> Run the failing suite and fix it
  ⏵ 81% 138k/200k  (auto)

────────────────────────────────────────────────────────────
[compaction] Compacted from 96,412 tokens (ctrl+o to expand)
────────────────────────────────────────────────────────────

── ⠸ Auto-compacting... (escape to cancel) ─────────────────────────
────────────────────────────────────────────────────────────────────
`

func TestWaitPiCompactingScreen(t *testing.T) {
	t.Run("wait: a pi compacting its context stays working and warns once", func(t *testing.T) { // JS: "wait: a pi compacting its context stays working and warns once"
		// Mutation captured: settling while the compacting border is up
		// would close the wait while pi rewrites its context; warning on
		// every probe would duplicate the friction line.
		f := newQueuedProbeFixture(t, "idle", "5", piCompactingWaitScreen, nil)
		roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\nworker\tp0a\tpi\timplementer\t\t\t\t\t\t\t\t\n"
		if err := os.WriteFile(filepath.Join(f.sd, "agents.tsv"), []byte(roster), 0600); err != nil {
			t.Fatal(err)
		}
		var stderr strings.Builder
		oldErr := platform.Stderr
		platform.Stderr = &stderr
		t.Cleanup(func() { platform.Stderr = oldErr })
		for i := 0; i < 3; i++ {
			if got := f.probe(""); got != "working" {
				t.Fatalf("probe %d state=%q", i+1, got)
			}
		}
		want := "soho: warning: 'worker' is compacting its context\n"
		if got := stderr.String(); got != want {
			t.Fatalf("stderr=%q want=%q", got, want)
		}
		if _, err := os.Stat(f.marker("compacting")); err != nil {
			t.Fatalf("compacting marker: %v", err)
		}
	})
	t.Run("wait: when the compacting border is gone the marker clears and the probe settles", func(t *testing.T) { // JS: "wait: when the compacting border is gone the marker clears and the probe settles"
		// Mutation captured: a stale compacting marker would keep a finished
		// pi working forever.
		f := newQueuedProbeFixture(t, "idle", "5", piCompactingWaitScreen, nil)
		f.env["SOHO_SETTLED_GRACE"] = "0"
		roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\nworker\tp0a\tpi\timplementer\t\t\t\t\t\t\t\t\n"
		if err := os.WriteFile(filepath.Join(f.sd, "agents.tsv"), []byte(roster), 0600); err != nil {
			t.Fatal(err)
		}
		if got := f.probe(""); got != "working" {
			t.Fatalf("compacting probe state=%q", got)
		}
		if _, err := os.Stat(f.marker("compacting")); err != nil {
			t.Fatalf("compacting marker: %v", err)
		}
		writeQueuedProbeRules(t, f, "ordinary output\n")
		if got := f.probe(""); got != "working" {
			t.Fatalf("first settled-screen probe state=%q", got)
		}
		if _, err := os.Stat(f.marker("compacting")); !os.IsNotExist(err) {
			t.Fatalf("stale compacting marker: %v", err)
		}
		if got := f.probe(""); got != "settled" {
			t.Fatalf("settled probe state=%q", got)
		}
	})
}
