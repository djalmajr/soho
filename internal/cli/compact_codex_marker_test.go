package cli

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

const codexRunningMarker = "  • Compacting context (21s • esc to interrupt)"

func codexIncidentScreen(composer string) string {
	return "earlier reply\n› /compact\n" + codexRunningMarker + "\n" + composer + "\nContext 0% used\n"
}

func TestCompactCodexRunningMarkerIncident(t *testing.T) {
	for _, composer := range []string{"›", "", "› /compact"} {
		for _, markerRead := range []int{2, 3, 4} {
			t.Run(composer+"/read="+strconv.Itoa(markerRead), func(t *testing.T) {
				fakeFastClock(t, 6*time.Second)
				screen := codexIncidentScreen(composer)
				line, ok := compactCodexComposerLine(screen)
				wantLine := composer
				if composer == "" {
					wantLine = "› /compact" // The history echo is misread as the composer.
				}
				if !ok || line != wantLine {
					t.Fatalf("composer=%q found=%v want %q", line, ok, wantLine)
				}
				// Inject the marker in each consumer: proof poll, confirm, and
				// the re-read immediately before the second Enter.
				rules := []fakecli.Rule{
					{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("idle", 1)},
					{Argv: compactReadArgv("worker"), Call: 1, Stdout: codexClearedComposer},
					{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
					{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
					{Argv: []string{"pane", "send-keys", "p1", "ctrl+u"}, Stdout: `{"result":{}}`},
				}
				if markerRead == 4 {
					// The re-read sees acceptance and a busy worker together.
					rules = append([]fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Call: 3, Stdout: compactStateJSON("working", 2)}}, rules...)
				}
				for read := 2; read < markerRead; read++ {
					rules = append(rules, fakecli.Rule{Argv: compactReadArgv("worker"), Call: read, Stdout: codexStuckComposer})
				}
				// Keep the marker visible long enough that ignoring it would
				// run the entire retry-and-clear protocol before proof arrives.
				for read := markerRead; read <= 5; read++ {
					rules = append(rules, fakecli.Rule{Argv: compactReadArgv("worker"), Call: read, Stdout: screen})
				}
				rules = append(rules, fakecli.Rule{Argv: compactReadArgv("worker"), Stdout: codexExecutedComposer})
				f := newCompactFixture(t, "codex", rules)
				code, out, errText := f.run(t, "compact", "worker", "--timeout", "600000")
				if code != 0 || errText != "" {
					t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
				}
				if value := compactJSON(t, out); value["status"] != "compacted" {
					t.Fatalf("json=%v", value)
				}
				assertCodexMarkerEffects(t, f.calls(t), [][]string{
					{"pane", "send-text", "p1", "/compact"},
					{"pane", "send-keys", "p1", "Enter"},
				})
				if reads := countArgv(f.calls(t), compactReadArgv("worker")); reads != 6 {
					t.Fatalf("reads=%d want 6: marker must wait for the following proof", reads)
				}
			})
		}
	}
}

func assertCodexMarkerEffects(t *testing.T, calls []fakecli.Call, want [][]string) {
	t.Helper()
	var effects [][]string
	for _, call := range calls {
		if len(call.Argv) > 0 && call.Argv[0] == "pane" {
			effects = append(effects, call.Argv)
		}
	}
	if !reflect.DeepEqual(effects, want) {
		t.Fatalf("pane calls=%q want exactly %q; all calls=%#v", effects, want, calls)
	}
}

func TestCompactCodexRunningMarkerAboveLastEchoStillRetries(t *testing.T) {
	fakeFastClock(t, 6*time.Second)
	screen := "earlier reply\n" + codexRunningMarker + "\n› /compact\n› /compact\nContext 0% used\n"
	// An older marker above the history echo must not accept the command,
	// even when the composer holds the same text as that echo.
	if compactCodexComposerRunning(screen) {
		t.Fatal("marker above the last echo was accepted")
	}
	f := newCompactFixture(t, "codex", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("idle", 1)},
		{Argv: compactReadArgv("worker"), Call: 1, Stdout: codexClearedComposer},
		{Argv: compactReadArgv("worker"), Stdout: screen},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-keys", "p1", "ctrl+u"}, Stdout: `{"result":{}}`},
	})
	code, out, errText := f.run(t, "compact", "worker", "--timeout", "120000")
	if code != 4 || out != "" || !strings.Contains(errText, "after two Enters; cleared it") {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	assertCodexMarkerEffects(t, f.calls(t), [][]string{
		{"pane", "send-text", "p1", "/compact"},
		{"pane", "send-keys", "p1", "Enter"},
		{"pane", "send-keys", "p1", "Enter"},
		{"pane", "send-keys", "p1", "ctrl+u"},
	})
}

func TestCompactCodexRunningMarkerWithoutProofTimesOut(t *testing.T) {
	fakeFastClock(t, 6*time.Second)
	f := newCompactFixture(t, "codex", []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("idle", 1)},
		{Argv: compactReadArgv("worker"), Call: 1, Stdout: codexClearedComposer},
		{Argv: compactReadArgv("worker"), Stdout: codexIncidentScreen("")},
		{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
		{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
	})
	code, out, errText := f.run(t, "compact", "worker", "--timeout", "30000")
	if code != 9 {
		t.Fatalf("code=%d want 9 out=%s stderr=%s", code, out, errText)
	}
	if value := compactJSON(t, out); value["status"] != "timeout" {
		t.Fatalf("json=%v", value)
	}
	assertCodexMarkerEffects(t, f.calls(t), [][]string{
		{"pane", "send-text", "p1", "/compact"},
		{"pane", "send-keys", "p1", "Enter"},
	})
}

func TestCompactCodexRunningMarkerRegion(t *testing.T) {
	for _, tc := range []struct {
		name, screen string
		want         bool
	}{
		{"no_echo_eighth", codexRunningMarker + "\n" + strings.Repeat("\nline\n", 7), true},
		{"no_echo_ninth", codexRunningMarker + "\n" + strings.Repeat("\nline\n", 8), false},
		{"composer_only_eighth", codexRunningMarker + "\n" + strings.Repeat("line\n", 6) + "› /compact\n", true},
		{"composer_only_ninth", codexRunningMarker + "\n" + strings.Repeat("line\n", 7) + "› /compact\n", false},
		{"below_echo_beyond_eight", "› /compact\n" + codexRunningMarker + "\n" + strings.Repeat("line\n", 9), true},
		{"older_than_last_echo", "› /compact\n" + codexRunningMarker + "\n› /compact\n›\n", false},
		{"pi_string", "› /compact\nCompacting context...\n", false},
		{"embedded_marker", "› /compact\nquoted " + codexRunningMarker + "\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := compactCodexComposerRunning(tc.screen); got != tc.want {
				t.Fatalf("running=%v want %v screen=%q", got, tc.want, tc.screen)
			}
		})
	}
}
