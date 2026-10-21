package herdr

import (
	"reflect"
	"testing"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestWorkerStateCreationArguments(t *testing.T) {
	for _, state := range []string{"", "/orchestrator state 'quoted'"} {
		for _, kind := range []string{"split", "tab"} {
			t.Run(kind+"/"+state, func(t *testing.T) {
				want := []string{"tab", "create", "--workspace", "ws", "--cwd", "/worker repo", "--label", "wave 2", "--no-focus"}
				output := `{"result":{"tab":{"tab_id":"t"},"root_pane":{"pane_id":"p"}}}`
				if kind == "split" {
					want = []string{"pane", "split", "anchor", "--direction", "right", "--cwd", "/worker repo", "--no-focus", "--ratio", "0.4"}
					output = `{"result":{"pane":{"pane_id":"p"}}}`
				}
				if state != "" {
					want = append(want, "--env", "SOHO_REGISTRY_DIR="+state)
				}
				env := newFake(t, "herdr", []fakecli.Rule{{Argv: want, Stdout: output}})
				env["SOHO_REGISTRY_DIR"] = state
				env["SOHO_STATE_DIR"] = "/configuration root"
				if kind == "split" {
					ratio := "0.4"
					if got := PaneSplit("anchor", "right", "/worker repo", env, &ratio); !got.Ok || got.Pane != "p" {
						t.Fatalf("result=%+v want argv=%q", got, want)
					}
				} else {
					if got := TabCreate(env, "ws", "/worker repo", "wave 2"); !got.Ok || got.Root != "p" {
						t.Fatalf("result=%+v want argv=%q", got, want)
					}
				}
				calls, err := fakecli.ReadCallsForConfig(env.Get("SOHO_FAKECLI_CONFIG") + "/herdr.json")
				if err != nil || len(calls) != 1 || !reflect.DeepEqual(calls[0].Argv, want) {
					t.Fatalf("calls=%+v err=%v want=%q", calls, err, want)
				}
			})
		}
	}
}
