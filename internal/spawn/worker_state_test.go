package spawn

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestSpawnWorkerState(t *testing.T) {
	for _, mode := range []string{"split", "tab", "label", "overflow", "herd-split", "given", "reuse"} {
		t.Run(mode, func(t *testing.T) {
			rules := freshSpawnRules()
			if mode == "herd-split" {
				rules = append([]fakecli.Rule{{Argv: []string{"pane", "list"}, ArgvPrefix: true, Stdout: `{"result":{"panes":[{"pane_id":"p-herd","tab_id":"t-herd"}]}}`}}, rules...)
			}
			f := newSpawnFixture(t, rules)
			configureSpawnFixture(t, &f)
			// Resolve the roster through the existing configuration layers; only
			// the resource-registration destination is passed to the worker.
			delete(f.env, "SOHO_DIR")
			root := filepath.Join(f.root, "orchestrator state 'quoted'")
			f.env["SOHO_STATE_DIR"] = root
			if mode == "split" || mode == "tab" {
				delete(f.env, "SOHO_STATE_DIR")
				f.ctx.Entries["state_dir"] = core.ConfigEntry{Value: root, Source: "project"}
			}
			originalState := f.env.Get("SOHO_STATE_DIR")
			sd := core.StateDir(f.ctx, f.env, f.cwd)
			args := []string{"worker", "--name", "worker"}
			wantCreation := 1
			wantKind := "pane"
			switch mode {
			case "split":
				f.env["HERDR_PANE_ID"] = "p1"
				args = append(args, "--direction", "right")
			case "tab":
				f.env["SOHO_LAYOUT"] = "tab"
				wantKind = "tab"
			case "label":
				args = append(args, "--tab-label", "wave 2")
				wantKind = "tab"
			case "overflow":
				f.env["SOHO_SPLIT_MAX_PANES"] = "1"
				wantKind = "tab"
			case "herd-split":
				f.env["SOHO_LAYOUT"] = "tab"
				if err := os.WriteFile(filepath.Join(sd, "herd-tab"), []byte("t-herd\therd\tauto\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "given":
				args = append(args, "--pane", "p-existing")
				wantCreation = 0
			case "reuse":
				args = []string{"worker"}
				wantCreation = 0
				row := strings.Replace(spawnRosterRow("worker", "worker", "grok-4.7", "ask", ""), "/tmp/work", f.cwd, 1)
				if err := os.WriteFile(filepath.Join(sd, "agents.tsv"), []byte("# roster\n"+row+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
				report := filepath.Join(sd, "reports", "worker.md")
				if err := os.WriteFile(report, []byte("done\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(core.LastReportPath(sd, "worker"), []byte(report+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
				// The reusable worker must be idle, unlike the fresh creation fixture.
				rules = append([]fakecli.Rule{idleHerdrRule("worker")}, rules...)
				writeWorkerStateRules(t, f, rules)
			}
			code, out, stderr, calls := runCmdSpawn(t, f, args)
			if code != 0 {
				t.Fatalf("mode=%s code=%d out=%s err=%s calls=%v", mode, code, out, stderr, calls)
			}
			creations := 0
			for _, call := range calls {
				creation := len(call.Argv) > 1 && ((call.Argv[0] == "pane" && call.Argv[1] == "split") || (call.Argv[0] == "tab" && call.Argv[1] == "create"))
				for i, arg := range call.Argv {
					if arg == "--env" {
						if !creation || i+1 >= len(call.Argv) || call.Argv[i+1] != "SOHO_REGISTRY_DIR="+sd {
							t.Fatalf("unexpected worker env: %q", call.Argv)
						}
					}
				}
				if creation {
					creations++
					if call.Argv[0] != wantKind || !workerStatePair(call.Argv, "--env", "SOHO_REGISTRY_DIR="+sd) {
						t.Fatalf("missing state env: mode=%s argv=%q want root=%q", mode, call.Argv, root)
					}
				}
			}
			if creations != wantCreation {
				t.Fatalf("mode=%s creations=%d want=%d out=%s calls=%v", mode, creations, wantCreation, out, calls)
			}
			if mode == "reuse" && !strings.Contains(out, `"reused": true`) {
				t.Fatalf("did not reuse: %s", out)
			}
			if f.env.Get("SOHO_STATE_DIR") != originalState || f.env.Get("SOHO_REGISTRY_DIR") != "" {
				t.Fatal("caller environment changed")
			}
		})
	}
}

func workerStatePair(args []string, key, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key && args[i+1] == value {
			return true
		}
	}
	return false
}

func writeWorkerStateRules(t *testing.T, f spawnFixture, rules []fakecli.Rule) {
	t.Helper()
	b, err := json.Marshal(struct {
		Log   string         `json:"log"`
		Rules []fakecli.Rule `json:"rules"`
	}{filepath.Join(f.bin, "herdr.calls.jsonl"), rules})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(f.bin, "herdr.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
}
