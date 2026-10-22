package peer_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/peer"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

type workspaceTargetAgent struct{ pane, name, status string }

func workspaceTargetSnapshot(t *testing.T, agents []workspaceTargetAgent) string {
	t.Helper()
	panes, records := []map[string]string{}, []map[string]string{}
	for _, a := range agents {
		workspace, _, _ := strings.Cut(a.pane, ":")
		panes = append(panes, map[string]string{"pane_id": a.pane, "workspace_id": workspace, "agent_status": a.status})
		records = append(records, map[string]string{"pane_id": a.pane, "workspace_id": workspace, "name": a.name, "agent": "pi", "agent_status": a.status})
	}
	raw, err := json.Marshal(map[string]any{"result": map[string]any{"snapshot": map[string]any{"panes": panes, "agents": records}}})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestSendWorkspaceTarget(t *testing.T) {
	for _, tc := range []struct {
		name, target, machine, lookup, selected, resolvedName string
		agents                                                []workspaceTargetAgent
		direct                                                bool
		hint                                                  string
	}{
		{name: "numbered", target: "local/w6Y", machine: "local", lookup: "w6Y", selected: "w6Y:p1", resolvedName: "orchestrator-7", agents: []workspaceTargetAgent{{"w6Y:p1", "orchestrator-7", "idle"}, {"w6Y:p2", "worker", "idle"}}},
		{name: "exact", target: "w6Y", machine: "local", lookup: "w6Y", selected: "w6Y:p1", resolvedName: "orchestrator", agents: []workspaceTargetAgent{{"w6Y:p1", "orchestrator", "idle"}}},
		{name: "ambiguous", target: "local/w6Y", machine: "local", lookup: "w6Y", agents: []workspaceTargetAgent{{"w6Y:p1", "orchestrator-7", "idle"}, {"w6Y:p2", "orchestrator", "idle"}}, hint: "; agents in w6Y: local/w6Y:p1 (orchestrator-7, pi), local/w6Y:p2 (orchestrator, pi)"},
		{name: "workers", target: "local/w6Y", machine: "local", lookup: "w6Y", agents: []workspaceTargetAgent{{"w6Y:p2", "worker", "idle"}, {"w6Y:p3", "", "working"}}, hint: "; agents in w6Y: local/w6Y:p2 (worker, pi), local/w6Y:p3 (-, pi)"},
		{name: "empty", target: "local/w6Y", machine: "local", lookup: "w6Y"},
		{name: "invalid-suffix", target: "local/w6Y", machine: "local", lookup: "w6Y", agents: []workspaceTargetAgent{{"w6Y:p1", "orchestrator-x", "idle"}}, hint: "; agents in w6Y: local/w6Y:p1 (orchestrator-x, pi)"},
		{name: "empty-suffix", target: "local/w6Y", machine: "local", lookup: "w6Y", agents: []workspaceTargetAgent{{"w6Y:p1", "orchestrator-", "idle"}}, hint: "; agents in w6Y: local/w6Y:p1 (orchestrator-, pi)"},
		{name: "unicode-suffix", target: "local/w6Y", machine: "local", lookup: "w6Y", agents: []workspaceTargetAgent{{"w6Y:p1", "orchestrator-٧", "idle"}}, hint: "; agents in w6Y: local/w6Y:p1 (orchestrator-٧, pi)"},
		{name: "non-live", target: "local/w6Y", machine: "local", lookup: "w6Y", agents: []workspaceTargetAgent{{"w6Y:p1", "orchestrator-7", ""}, {"w6Y:p2", "worker", "idle"}}, hint: "; agents in w6Y: local/w6Y:p2 (worker, pi)"},
		{name: "other-workspace", target: "local/w6Y", machine: "local", lookup: "w6Y", agents: []workspaceTargetAgent{{"w7:p1", "orchestrator-7", "idle"}}},
		{name: "remote", target: "alien/w16", machine: "alien", lookup: "w16", selected: "w16:p1", resolvedName: "orchestrator-7", agents: []workspaceTargetAgent{{"w16:p1", "orchestrator-7", "idle"}}},
		{name: "pane", target: "local/w6Y:p1", machine: "local", lookup: "w6Y:p1", selected: "w6Y:p1", direct: true},
		{name: "agent-name", target: "build", machine: "local", lookup: "build", selected: "w6Y:p1", direct: true},
		{name: "workspace-shaped-name", target: "w6Y", machine: "local", lookup: "w6Y", selected: "w6Y:p1", direct: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prefix := []string{}
			if tc.machine != "local" {
				prefix = []string{"--machine", tc.machine}
			}
			argv := func(a ...string) []string { return append(append([]string{}, prefix...), a...) }
			first := fakecli.Rule{Argv: argv("agent", "get", tc.lookup), Code: 1, Stderr: noAgentNotRef}
			info := fmt.Sprintf(`{"result":{"agent":{"pane_id":%q,"workspace_id":%q,"agent":"pi","agent_status":"idle","state_change_seq":"1"}}}`, tc.selected, strings.Split(tc.selected, ":")[0])
			if tc.direct {
				first.Code = 0
				first.Stderr = ""
				first.Stdout = info
				first.Call = 1
			}
			rules := []fakecli.Rule{first, {Argv: argv("api", "snapshot"), Stdout: workspaceTargetSnapshot(t, tc.agents)}}
			if tc.selected != "" {
				get := argv("agent", "get", tc.selected)
				postPromptGet := 4
				if tc.direct && tc.lookup != tc.selected {
					postPromptGet = 3
				}
				for i := 1; i <= postPromptGet; i++ {
					seq := "1"
					status := "idle"
					if i == postPromptGet {
						seq = "2"
						status = "working"
					}
					body := strings.Replace(info, `"state_change_seq":"1"`, `"state_change_seq":"`+seq+`"`, 1)
					body = strings.Replace(body, `"agent_status":"idle"`, `"agent_status":"`+status+`"`, 1)
					rules = append(rules, fakecli.Rule{Argv: get, Call: i, Stdout: body})
				}
				rules = append(rules, fakecli.Rule{Argv: argv("agent", "read", tc.selected, "--source", "visible"), Stdout: "ready\n"}, fakecli.Rule{Argv: argv("agent", "read", tc.selected, "--source", "recent-unwrapped"), ArgvPrefix: true, Stdout: "ready\n"}, fakecli.Rule{Argv: argv("agent", "prompt", tc.selected), ArgvPrefix: true})
			}
			f := newFixture(t, rules)
			f.env["USERPROFILE"] = f.env["HOME"]
			f.env["PATH"] = filepath.Dir(f.bin)
			f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "100", "1"
			code, out, stderr := f.run([]string{"send", tc.target, "hello"})
			calls, err := fakecli.ReadCallsForConfig(filepath.Join(filepath.Dir(f.bin), "herdr.json"))
			if err != nil {
				t.Fatal(err)
			}
			if len(calls) == 0 || !reflect.DeepEqual(calls[0].Argv, first.Argv) {
				t.Fatalf("lookup order=%+v", calls)
			}
			for _, call := range calls {
				if tc.machine != "local" && (len(call.Argv) < 2 || call.Argv[0] != "--machine" || call.Argv[1] != tc.machine) {
					t.Fatalf("wrong machine: %+v", calls)
				}
			}
			snapshots := 0
			for _, call := range calls {
				if reflect.DeepEqual(call.Argv, argv("api", "snapshot")) {
					snapshots++
				}
			}
			if tc.direct {
				if snapshots != 0 {
					t.Fatalf("direct reference listed sessions: %+v", calls)
				}
			} else if snapshots != 1 {
				t.Fatalf("snapshot count=%d calls=%+v", snapshots, calls)
			}
			if tc.selected == "" {
				want := "soho: send: no agent in " + tc.target + tc.hint + "\n"
				if code != 4 || out != "" || stderr != want {
					t.Fatalf("code=%d out=%q stderr=%q want=%q", code, out, stderr, want)
				}
				assertNoAgentHintCalls(t, f, 2, strings.Join(argv("api", "snapshot"), " "))
				return
			}
			shown := tc.machine + "/" + tc.selected
			if tc.direct && tc.target == "build" || tc.direct && tc.target == "w6Y" {
				shown = tc.target
			}
			wantErr := ""
			if !tc.direct {
				wantErr = fmt.Sprintf("soho: send: %s resolved to %s (%s)\n", tc.machine+"/"+tc.lookup, shown, tc.resolvedName)
			}
			if code != 0 || out != "sent to "+shown+"\n" || stderr != wantErr {
				t.Fatalf("code=%d out=%q stderr=%q wantErr=%q calls=%+v", code, out, stderr, wantErr, calls)
			}
			prompts := 0
			for _, call := range calls {
				p := argv("agent", "prompt", tc.selected)
				if len(call.Argv) >= len(p) && reflect.DeepEqual(call.Argv[:len(p)], p) {
					prompts++
				}
			}
			if prompts != 1 || countSendKeyEnters(t, f) != 0 {
				t.Fatalf("delivery prompts=%d calls=%+v", prompts, calls)
			}
			promptSeen, proofGets := false, 0
			for _, call := range calls {
				p := argv("agent", "prompt", tc.selected)
				if len(call.Argv) >= len(p) && reflect.DeepEqual(call.Argv[:len(p)], p) {
					promptSeen = true
				}
				if promptSeen && reflect.DeepEqual(call.Argv, argv("agent", "get", tc.selected)) {
					proofGets++
				}
			}
			if proofGets != 1 {
				t.Fatalf("the first get after prompt must prove the state change: got %d gets; calls=%+v", proofGets, calls)
			}
			if !tc.direct && (len(calls) < 3 || !reflect.DeepEqual(calls[2].Argv, argv("agent", "get", tc.selected))) {
				t.Fatalf("missing pane revalidation: %+v", calls)
			}
		})
	}
}

func TestSendWorkspaceRetrySharedDeadline(t *testing.T) {
	snapshot := workspaceTargetSnapshot(t, []workspaceTargetAgent{{"w6Y:p1", "orchestrator-7", "idle"}})
	f := newFixture(t, []fakecli.Rule{
		{Argv: []string{"agent", "get", "w6Y"}, Call: 1, Code: 1, Stderr: noAgentNotRef},
		{Argv: []string{"api", "snapshot"}, Call: 1, Stdout: snapshot},
		{Argv: []string{"agent", "get", "w6Y:p1"}, Call: 1, Code: 1, Stderr: notReadyErr},
		{Argv: []string{"agent", "get", "w6Y"}, Call: 2, Delay: 1200, Code: 1, Stderr: noAgentNotRef},
		{Argv: []string{"api", "snapshot"}, Call: 2, Delay: 1200, Stdout: snapshot},
		{Argv: []string{"agent", "get", "w6Y:p1"}, Call: 2, Delay: 1200, Code: 1, Stderr: notReadyErr},
	})
	f.env["PATH"], f.env["USERPROFILE"] = filepath.Dir(f.bin), f.env["HOME"]
	f.env["SOHO_SEND_POLL_MS"] = "1"
	start := time.Now()
	code, out, stderr := f.run([]string{"send", "local/w6Y", "--timeout", "1500", "hello"})
	workspace := callStartsOf(t, f, []string{"agent", "get", "w6Y"})
	snapshots := callStartsOf(t, f, []string{"api", "snapshot"})
	selected := callStartsOf(t, f, []string{"agent", "get", "w6Y:p1"})
	t.Logf("code=%d stdout=%q stderr=%q elapsed=%s workspace_gets=%d snapshots=%d selected_gets=%d", code, out, stderr, time.Since(start), len(workspace), len(snapshots), len(selected))
	if code != 4 || out != "" || !strings.Contains(stderr, "still not ready after 1.5s") {
		t.Fatalf("unexpected timeout path: code=%d out=%q stderr=%q", code, out, stderr)
	}
	if len(workspace) != 2 || len(snapshots) != 2 || len(selected) < 1 {
		t.Fatalf("fixture did not enter the workspace retry and slow snapshot: workspace=%v snapshots=%v", workspace, snapshots)
	}
	for _, calls := range [][]time.Time{workspace[1:], snapshots[1:], selected[1:]} {
		for _, began := range calls {
			late := began.Sub(workspace[1])
			t.Logf("retry call start after first workspace retry=%s budget=1.5s", late)
			if late > 1500*time.Millisecond {
				t.Fatalf("new resolution call began after the shared deadline: %s > 1.5s", late)
			}
		}
	}
	if len(selected) != 1 {
		t.Fatalf("expired snapshot budget must prevent pane revalidation: selected=%v", selected)
	}
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(filepath.Dir(f.bin), "herdr.json"))
	if err != nil {
		t.Fatal(err)
	}
	if countCalls(calls, "agent", "prompt") != 0 || countSendKeyEnters(t, f) != 0 || peerLogResult(t, f) != "error" {
		t.Fatalf("timeout sent input or recorded a wrong result: %+v", calls)
	}
}

func TestWorkspaceExpiredDeadlineStartsNoLookup(t *testing.T) {
	f := newFixture(t, nil)
	if !peer.ResolveTargetWithinTimedOut("local/w6Y", f.env, 0) {
		t.Fatal("an exhausted resolution budget must report timeout")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(f.bin), "herdr.calls.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("lookup started with an exhausted budget: %v", err)
	}
}

func TestSendWorkspaceTargetErrors(t *testing.T) {
	for _, code := range []string{"permission_denied", "server_not_running"} {
		t.Run(code, func(t *testing.T) {
			f := newFixture(t, []fakecli.Rule{{Argv: []string{"agent", "get", "w6Y"}, Stderr: fmt.Sprintf(`{"error":{"code":%q,"message":"unavailable"}}`, code), Code: 1}})
			status, out, stderr := f.run([]string{"send", "local/w6Y", "hello"})
			if status != 4 || out != "" || !strings.Contains(stderr, code) {
				t.Fatalf("code=%d out=%q stderr=%q", status, out, stderr)
			}
			assertNoAgentHintCalls(t, f, 1, "")
		})
	}
	for _, malformed := range []bool{false, true} {
		t.Run(fmt.Sprintf("snapshot-failure-%v", malformed), func(t *testing.T) {
			rule := fakecli.Rule{Argv: []string{"api", "snapshot"}, Code: 1, Stderr: "unavailable"}
			if malformed {
				rule.Code = 0
				rule.Stderr = ""
				rule.Stdout = "{broken"
			}
			f := newFixture(t, []fakecli.Rule{{Argv: []string{"agent", "get", "w6Y"}, Stderr: noAgentNotRef, Code: 1}, rule})
			code, out, stderr := f.run([]string{"send", "local/w6Y", "hello"})
			if code != 4 || out != "" || stderr != "soho: send: no agent in local/w6Y\n" {
				t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
			}
			assertNoAgentHintCalls(t, f, 2, "api snapshot")
		})
	}
}

func TestSendWorkspaceTargetHintLimit(t *testing.T) {
	f := newFixture(t, []fakecli.Rule{{Argv: []string{"agent", "get", "w12"}, Stderr: noAgentNotRef, Code: 1}, {Argv: []string{"api", "snapshot"}, Stdout: w12CrowdedSnapshot}})
	code, out, stderr := f.run([]string{"send", "local/w12", "hello"})
	want := "soho: send: no agent in local/w12; agents in w12: local/w12:p12 (orchestrator-12, claude), local/w12:p15 (orchestrator-3, claude), local/w12:p11 (planner-12, pi), local/w12:p13 (reviewer-12, codex), local/w12:p14 (implementer-12, pi)\n"
	if code != 4 || out != "" || stderr != want {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	assertNoAgentHintCalls(t, f, 2, "api snapshot")
}
