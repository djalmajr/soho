package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// reopenLaneKindFixture is a reopen fixture whose worker (kind grok, role
// implementer, lane build) is idle with a report, and whose lane "build"
// holds the role "implementer" with lane.build.kind=laneKind.
func reopenLaneKindFixture(t *testing.T, laneKind string) *releaseFixture {
	t.Helper()
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
	f.env["SOHO_LAYOUT"] = "split"
	f.env["SOHO_LANES"] = "on"
	f.env["SOHO_LANE_BUILD_ROLES"] = "implementer"
	if laneKind != "" {
		f.env["SOHO_LANE_BUILD_KIND"] = laneKind
	}
	return f
}

// runCommand runs one command through Run and returns what reopen's helper
// returns: code, output and the herdr calls.
func (f *releaseFixture) runCommand(t *testing.T, argv ...string) releaseRun {
	t.Helper()
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, stderr bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &stderr
	code := Run(argv, f.env)
	platform.Stdout, platform.Stderr = oldOut, oldErr
	calls, err := fakecli.ReadCallsForConfig(f.configFile)
	if os.IsNotExist(err) {
		calls = []fakecli.Call{}
	} else if err != nil {
		t.Fatal(err)
	}
	return releaseRun{code, out.String(), stderr.String(), calls, f.state, f.configFile}
}

func (f *releaseFixture) rosterFile(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(f.state, "agents.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestReopenKeepsTheRecordedKindRegardlessOfGroupDefault(t *testing.T) {
	for _, tc := range []struct {
		name, laneKind, lanes string
	}{
		{"the worker kind differs from the group default", "codex", "on"},
		{"the worker kind is the group default", "grok", "on"},
		{"the lane has no kind", "", "on"},
		{"lanes off for the invocation", "codex", "off"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := reopenLaneKindFixture(t, tc.laneKind)
			f.env["SOHO_LANES"] = tc.lanes
			run := f.reopen(t, "worker")
			if run.code != 0 || strings.Contains(run.stdout, "kind-mismatch") {
				t.Fatalf("exit=%d stdout=%q stderr=%q", run.code, run.stdout, run.stderr)
			}
			for _, want := range []string{`"name": "worker"`, `"kind": "grok"`, `"created_pane": true`} {
				if !spawnJSONHasFieldCLI(t, run.stdout, want) {
					t.Fatalf("spawn JSON missing %s: %s", want, run.stdout)
				}
			}
			closed := false
			for _, c := range run.calls {
				if len(c.Argv) >= 3 && c.Argv[0] == "pane" && c.Argv[1] == "close" && c.Argv[2] == "p-worker" {
					closed = true
				}
			}
			if !closed {
				t.Fatalf("the old pane was not closed: %#v", run.calls)
			}
		})
	}
}
