package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
)

// Reopen passes the roster's recorded name as an explicit --name to spawn. It
// is not a new naming choice, so neither the pattern advisory for a name such
// as "worker" nor the reserved-prefix advisory may reach stderr.
func TestReopenDoesNotPrintNamePatternWarning(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "plain"},
		{name: "kind override", args: []string{"--kind", "grok"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newReopenFixture(t, "idle")
			report := filepath.Join(f.state, "reports", "worker.md")
			if err := os.MkdirAll(filepath.Dir(report), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(report, []byte("# done\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(core.LastReportPath(f.state, "worker"), []byte(report+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			setReopenRules(t, f)
			run := f.reopen(t, append([]string{"worker"}, tc.args...)...)
			if run.code != 0 {
				t.Fatalf("exit=%d stdout=%q stderr=%q", run.code, run.stdout, run.stderr)
			}
			for _, marker := range []string{"is not <subject>-<function>", "uses the reserved prefix", "pass --card <KEY>"} {
				if strings.Contains(run.stderr, marker) {
					t.Fatalf("reopen printed the naming advisory %q: stderr=%q", marker, run.stderr)
				}
			}
			if !spawnJSONHasFieldCLI(t, run.stdout, `"name": "worker"`) {
				t.Fatalf("spawn JSON=%q", run.stdout)
			}
		})
	}
}
