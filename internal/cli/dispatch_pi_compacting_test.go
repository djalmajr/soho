package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// piCompactingDispatchScreen is assembled from pi 1.4.0 source (the input
// box top border with the threshold auto-compaction label, the footer with
// the (auto) setting token, and the post-compaction summary line; see
// .soho/w6X/reports/build-6-20261029T212047.md §4). It is not a real
// captured screen.
const piCompactingDispatchScreen = `> Run the failing suite and fix it
  ⏵ 81% 138k/200k  (auto)

────────────────────────────────────────────────────────────
[compaction] Compacted from 96,412 tokens (ctrl+o to expand)
────────────────────────────────────────────────────────────

── ⠸ Auto-compacting... (escape to cancel) ─────────────────────────
────────────────────────────────────────────────────────────────────
`

// makePiWorker turns the fixture's codex worker into a pi.
func makePiWorker(t *testing.T, f *busyDispatchFixture) {
	t.Helper()
	path := filepath.Join(f.stateDir, "agents.tsv")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("\tcodex\t")) {
		t.Fatal("fixture roster has no codex kind to replace")
	}
	if err := os.WriteFile(path, bytes.Replace(data, []byte("\tcodex\t"), []byte("\tpi\t"), 1), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDispatchPiCompactingBorder(t *testing.T) {
	t.Run("dispatch --when-idle waits for the compacting border to clear and ends sent", func(t *testing.T) {
		// Mutation captured: a dispatch that treated the compacting pi as
		// idle would type the prompt into the compacting turn instead of
		// waiting for the border to clear.
		f := newBusyDispatchFixture(t)
		t.Logf("fixture_root=%s", f.root)
		f.openPointer(t)
		makePiWorker(t, f)
		idleRecent := "idle screen\n"
		rules := []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: busyStateJSON("idle", 1)},
			{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped"}, Call: 1, ArgvPrefix: true, Stdout: piCompactingDispatchScreen},
			{Argv: []string{"agent", "get", "worker"}, Call: 2, Stdout: busyStateJSON("idle", 1)},
			{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped"}, Call: 2, ArgvPrefix: true, Stdout: piCompactingDispatchScreen},
			{Argv: []string{"agent", "get", "worker"}, Call: 3, Stdout: busyStateJSON("idle", 1)},
			{Argv: []string{"agent", "get", "worker"}, Call: 4, Stdout: busyStateJSON("idle", 1)},
			{Argv: []string{"agent", "read", "worker", "--source", "visible"}, ArgvPrefix: true, Stdout: idleRecent},
			{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: idleRecent},
			{Argv: []string{"agent", "get", "worker"}, Stdout: busyStateJSON("working", 2)},
			{Argv: []string{"agent", "list"}, ArgvPrefix: true, Stdout: `{"result":{"agents":[]}}`},
			{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"sent":true}}`},
		}
		f.install(t, rules)
		code, out, stderr := f.run(t, "worker", f.brief, "--when-idle", "--no-wait", "--timeout", "20s")
		if code != 0 || dispatchOutputStatus(t, out) != "submitted" {
			t.Fatalf("code=%d out=%s stderr=%s", code, out, stderr)
		}
		want := "soho: dispatch: 'worker' is still working; waiting for it to finish before sending (--when-idle)\n"
		if stderr != want {
			t.Fatalf("stderr=%q want=%q", stderr, want)
		}
		calls := f.calls(t)
		promptIdx, sends := -1, 0
		for i, c := range calls {
			if len(c.Argv) >= 2 && reflect.DeepEqual(c.Argv[:2], []string{"agent", "prompt"}) {
				if promptIdx != -1 {
					t.Fatalf("second prompt at call %d", i)
				}
				promptIdx = i
			}
			if len(c.Argv) >= 2 && reflect.DeepEqual(c.Argv[:2], []string{"agent", "send-keys"}) {
				sends++
			}
		}
		// The first two recent-unwrapped reads are the compacting screen;
		// the prompt must be typed only after the border cleared, i.e. after
		// at least three pre-send reads (two compacting + one cleared).
		reads := 0
		for i, c := range calls {
			if i >= promptIdx {
				break
			}
			if len(c.Argv) >= 5 && reflect.DeepEqual(c.Argv[:5], []string{"agent", "read", "worker", "--source", "recent-unwrapped"}) {
				reads++
			}
		}
		if promptIdx == -1 || reads < 3 || sends != 0 {
			t.Fatalf("promptIdx=%d reads-before-prompt=%d sends=%d", promptIdx, reads, sends)
		}
	})
	t.Run("dispatch refuses a fresh brief while the compacting border is up", func(t *testing.T) {
		// Mutation captured: a normal dispatch that ignored the compacting
		// border would type the brief into the compacting turn and trip D25.
		f := newBusyDispatchFixture(t)
		t.Logf("fixture_root=%s", f.root)
		f.openPointer(t)
		makePiWorker(t, f)
		f.install(t, []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: busyStateJSON("idle", 1)},
			{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: piCompactingDispatchScreen},
		})
		before := snapshotTree(t, f.stateDir)
		code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
		if code != 10 || out != "" {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
		if !containsAll(stderr, "is still working on its open task", "--amend", "--queue") {
			t.Fatalf("stderr=%q", stderr)
		}
		for _, c := range f.calls(t) {
			if len(c.Argv) >= 2 && reflect.DeepEqual(c.Argv[:2], []string{"agent", "prompt"}) {
				t.Fatalf("typed while compacting: %v", c.Argv)
			}
			if len(c.Argv) >= 2 && reflect.DeepEqual(c.Argv[:2], []string{"agent", "send-keys"}) {
				t.Fatalf("typed while compacting: %v", c.Argv)
			}
		}
		after := snapshotTree(t, f.stateDir)
		delete(after, "friction.log")
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("task state changed: before=%v after=%v", before, after)
		}
	})
}

func containsAll(haystack string, needles ...string) bool {
	for _, n := range needles {
		if !bytes.Contains([]byte(haystack), []byte(n)) {
			return false
		}
	}
	return true
}
