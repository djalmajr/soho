package cli

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/doctor"
	"github.com/djalmajr/soho/internal/inventory"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestSoh234LiveOtherPaneRegistrationKeepsAdvice(t *testing.T) {
	body := func(root string) string {
		return soh234LiveAgent("orchestrator", "p1", "claude", "ws234", root) + "," + soh234LiveAgent("orchestrator-2", "p234", "claude", "ws234", root)
	}
	env, repo, bin := soh234InventoryFixture(t, "inventory=show\n", "orchestrator-2", body, "working",
		fakecli.Rule{Argv: []string{"agent", "get", "orchestrator"}, Stdout: `{"result":{"agent":{"agent_status":"working","state_change_seq":1}}}`},
		fakecli.Rule{Argv: []string{"agent", "read", "orchestrator", "--source", "visible"}, Stdout: "planning the next move\n"},
		fakecli.Rule{Argv: []string{"--version"}, Stdout: "herdr 1.2.3\n"},
		fakecli.Rule{Argv: []string{"status", "server"}, Stdout: "server 1.2.3\n"},
		fakecli.Rule{Argv: []string{"pane", "get", "p234"}, Stdout: `{"result":{"pane":{"workspace_id":"ws234","title":"fixture"}}}`},
		fakecli.Rule{Argv: []string{"pane", "current", "--current"}, Code: 1, Stderr: "workspace discovery unavailable\n"})
	terms := soh234InventoryPrerequisites(t, env, repo)
	soh234RequireCandidate(t, terms, repo, "orchestrator-2")
	if len(terms) != 2 || terms[0].Name != "orchestrator" || terms[0].PaneID != "p1" || terms[0].Cwd != repo || terms[0].WorkspaceID != "ws234" || !reflect.DeepEqual(terms[0].ProgramHints, []string{"claude"}) {
		t.Fatalf("setup: live registered terminal identity missing: %+v", terms)
	}
	soh234CLIRecord(t, env, repo, "p1")
	before := inventoryCLITree(t, repo)
	code, out, stderr := inventoryCLIRun(t, env, "inventory", "--json")
	decodeInventoryCLI(t, out)
	var doc inventory.Document
	if err := json.Unmarshal([]byte(out), &doc); err != nil || code != 0 || len(doc.Projects) != 1 || len(doc.Projects[0].Sessions) != 1 || len(doc.Projects[0].Sessions[0].Agents) != 1 || doc.Projects[0].Sessions[0].Agents[0].Terminal != "p1" {
		t.Fatalf("bound registration was not retained: exit=%d document=%+v err=%v", code, doc, err)
	}
	if strings.Count(stderr, soh234Advisory) != 1 {
		t.Errorf("live registration on another pane suppressed inventory advice: %q", stderr)
	}
	env["HERDR_ENV"], env["HERDR_PANE_ID"], env["HERDR_WORKSPACE_ID"], env["SOHO_CALLER_CHECK"] = "1", "p234", "ws234", "off"
	ctx := core.LoadConfig(env, repo)
	var diagnostic strings.Builder
	doctor.DoctorCheck(&ctx, env, repo, &diagnostic)
	if strings.Count(diagnostic.String(), "warn   "+soh234Advisory+"\n") != 1 {
		t.Errorf("live registration on another pane suppressed doctor advice: %q", diagnostic.String())
	}
	if !reflect.DeepEqual(before, inventoryCLITree(t, repo)) {
		t.Fatal("the read-only consumers changed the project tree")
	}
	for _, call := range soh234InventoryCalls(t, bin) {
		if len(call.Argv) >= 2 && call.Argv[0] == "agent" && call.Argv[1] == "read" && !reflect.DeepEqual(call.Argv, []string{"agent", "read", "orchestrator", "--source", "visible"}) {
			t.Fatalf("unregistered terminal screen read: %+v", call)
		}
	}
}
