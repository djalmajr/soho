package cli

import (
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	"strings"
	"testing"
	"time"
)

func TestReview552ClippedZeroRemainsNoProof(t *testing.T) {
	for _, tail := range []string{"475K win…", "..."} {
		t.Run(tail, func(t *testing.T) {
			fakeFastClock(t, 6*time.Second)
			screen := strings.Replace(codexWindowlessScreen(t), "weekly 95% left · main", tail, 1)
			if screen == codexWindowlessScreen(t) {
				t.Fatal("fixture replacement failed")
			}
			n, ok := compactCodexContext(screen)
			if ok {
				t.Fatalf("fixture unexpectedly has a valid gauge %d", n)
			}
			before := strings.Replace(codexScrolled(codexFooterBefore49), "Context 49%", "Context 38%", 1)
			f := newCompactFixture(t, "codex", []fakecli.Rule{
				{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
				{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("idle", 2)},
				{Argv: compactReadArgv("worker"), Call: 1, Stdout: before},
				{Argv: compactReadArgv("worker"), Stdout: screen},
				{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
				{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
			})
			compactLowContextEnv(t, f.env)
			t.Chdir(f.root)
			code, out, stderr := f.run(t, "compact", "worker", "--timeout", "1000")
			t.Logf("tail=%q parser_reading=%v exit=%d stdout=%s stderr=%s", tail, ok, code, out, stderr)
			if code != 9 || compactJSON(t, out)["status"] != "timeout" {
				t.Fatal("clipped/invalid zero footer was accepted as completed compaction")
			}
		})
	}
}
func TestReview552HistoryAloneCannotLatchProof(t *testing.T) {
	fakeFastClock(t, 6*time.Second)
	f := newCompactFixture(t, "codex", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
		{Argv: []string{"agent", "get", "worker"}, Call: 2, Stdout: compactStateJSON("working", 2)},
		{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("idle", 3)},
		{Argv: compactReadArgv("worker"), Call: 1, Stdout: codexScrolled(codexFooterBefore49)},
		{Argv: compactReadArgv("worker"), Call: 2, Stdout: codexHistoryAboveComposer()},
		{Argv: compactReadArgv("worker"), Stdout: "› Ask Codex\nNo footer or compaction proof\n"},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
	})
	compactLowContextEnv(t, f.env)
	t.Chdir(f.root)
	code, out, stderr := f.run(t, "compact", "worker", "--timeout", "1000")
	t.Logf("history present only while working; then idle without footer: exit=%d stdout=%s stderr=%s", code, out, stderr)
	if code != 9 || compactJSON(t, out)["status"] != "timeout" {
		t.Fatal("history alone latched compaction proof")
	}
}
