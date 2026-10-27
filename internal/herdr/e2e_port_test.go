package herdr

import (
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	"path/filepath"
	"reflect"
	"testing"
)

func TestE2EPortCreationArguments(t *testing.T) {
	for _, mode := range []string{"split", "tab"} {
		t.Run(mode, func(t *testing.T) {
			want := []string{"tab", "create", "--workspace", "ws", "--cwd", "/worker repo", "--label", "workers", "--no-focus"}
			output := `{"result":{"tab":{"tab_id":"t"},"root_pane":{"pane_id":"p"}}}`
			if mode == "split" {
				want = []string{"pane", "split", "anchor", "--direction", "right", "--cwd", "/worker repo", "--no-focus"}
				output = `{"result":{"pane":{"pane_id":"p"}}}`
			}
			want = append(want, "--env", "SOHO_REGISTRY_DIR=/state root", "--env", "E2E_PORT=47000")
			env := newFake(t, "herdr", []fakecli.Rule{{Argv: want, Stdout: output}})
			env["SOHO_REGISTRY_DIR"] = "/state root"
			env["SOHO_SPAWN_E2E_PORT"] = "47000"
			if mode == "split" {
				if !PaneSplit("anchor", "right", "/worker repo", env, nil).Ok {
					t.Fatal("split failed")
				}
			} else {
				if !TabCreate(env, "ws", "/worker repo", "workers").Ok {
					t.Fatal("tab failed")
				}
			}
			calls, err := fakecli.ReadCallsForConfig(filepath.Join(env.Get("SOHO_FAKECLI_CONFIG"), "herdr.json"))
			if err != nil || len(calls) != 1 || !reflect.DeepEqual(calls[0].Argv, want) {
				t.Fatalf("calls=%+v err=%v want=%v", calls, err, want)
			}
		})
	}
}
