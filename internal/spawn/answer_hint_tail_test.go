package spawn

import (
	"github.com/djalmajr/soho/internal/startup"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	"strings"
	"testing"
)

func TestReview322LaterNonemptyMarkerSuppressesHint(t *testing.T) {
	// A marker-only line is still later nonempty content, not a choice line
	// and not a terminal border. Do not require any provider-specific prompt.
	screen := "The report quotes this old dialog:\n" + grokTrustDialogScreen + "\n›\n"
	key, option, ok := startup.AnswerHint("grok", screen)
	t.Logf("nonempty later line: key=%q option=%q ok=%v", key, option, ok)
	f := newSpawnFixture(t, append([]fakecli.Rule{{Argv: grokScreenRead("worker"), Stdout: screen}}, freshSpawnRules()...))
	configureSpawnFixture(t, &f)
	f.env["TMPDIR"] = f.root
	for _, name := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "TMPDIR", "PATH"} {
		t.Setenv(name, f.env[name])
	}
	code, out, stderr, calls := runCmdSpawn(t, f, []string{"worker"})
	t.Logf("spawn exit=%d stdout=%s stderr=%s input=%v", code, out, stderr, sendKeysCalls(calls))
	if code != 7 || len(sendKeysCalls(calls)) != 0 {
		t.Fatal("fixture did not reach blocked/no-input path")
	}
	if ok || key != "" || option != "" || strings.Contains(stderr, "herdr agent send-keys worker y ") {
		t.Fatal("later nonempty marker was discarded and quoted dialog acquired a trust key")
	}
}
