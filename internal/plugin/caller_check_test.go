package plugin

import (
	contextpkg "context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// TestTeamCallSetsDelegatedPane: the plugin is the only code that runs soho
// in the name of another pane, so teamCall must mark the child's
// environment with SOHO_DELEGATED_PANE=<target pane> next to the injected
// HERDR_PANE_ID, for reads and writes alike. The fake CLI captures the
// child's environment (never a process listing).
func TestTeamCallSetsDelegatedPane(t *testing.T) {
	for _, nowrite := range []bool{true, false} {
		name := "write"
		if nowrite {
			name = "read"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			cwd := t.TempDir()
			cli, err := fakecli.InstallWithOptions(t, dir, "cli", []fakecli.Rule{
				{Argv: []string{"status"}, Stdout: "ok\n"},
			}, fakecli.InstallOptions{CaptureEnv: []string{"HERDR_PANE_ID", "HERDR_WORKSPACE_ID", "HERDR_TAB_ID", "SOHO_DELEGATED_PANE", "SOHO_NOWRITE"}})
			if err != nil {
				t.Fatal(err)
			}
			env := platform.Env{}
			for _, entry := range fakecli.Env(os.Environ(), dir, fakecli.EnvOptions{IncludeBasePath: true}) {
				key, value, ok := strings.Cut(entry, "=")
				if ok {
					env[key] = value
				}
			}
			env["HERDR_BIN_PATH"] = cli
			// The shell's decoy ids must lose to the focused target's.
			env["HERDR_WORKSPACE_ID"] = "shell-ws"
			env["HERDR_PANE_ID"] = "shell-pane"
			env["HERDR_TAB_ID"] = "shell-tab"
			target := teamTarget{WorkspaceID: "w1", PaneID: "p1", TabID: "t1", Cwd: cwd}
			_, failure := teamCall(contextpkg.Background(), cli, env, platform.Current(), target, []string{"status"}, nowrite)
			if failure != "" {
				t.Fatalf("teamCall failed: %s", failure)
			}
			calls, err := fakecli.ReadCallsForConfig(filepath.Join(dir, "cli.json"))
			if err != nil {
				t.Fatal(err)
			}
			if len(calls) != 1 {
				t.Fatalf("calls=%d want 1", len(calls))
			}
			captured := calls[0].Env
			if captured["SOHO_DELEGATED_PANE"] != "p1" {
				t.Fatalf("SOHO_DELEGATED_PANE=%q want the focused target's pane p1: %#v", captured["SOHO_DELEGATED_PANE"], captured)
			}
			if captured["HERDR_PANE_ID"] != "p1" || captured["HERDR_WORKSPACE_ID"] != "w1" || captured["HERDR_TAB_ID"] != "t1" {
				t.Fatalf("the child's target ids are wrong: %#v", captured)
			}
			if nowrite {
				if captured["SOHO_NOWRITE"] != "1" {
					t.Fatalf("the read must keep SOHO_NOWRITE=1: %#v", captured)
				}
			} else if _, ok := captured["SOHO_NOWRITE"]; ok {
				t.Fatalf("the write must strip SOHO_NOWRITE: %#v", captured)
			}
		})
	}
}
