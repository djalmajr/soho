package cli

// SOH-458 under hc: only a live terminal parented by this caller and recorded
// in the released-panes registry earns the close hint. The same name
// parented elsewhere, a wake child and an unregistered assistant child keep
// the original refusal.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func addHcUnfilteredList(t *testing.T, f *dispatchHcFixture, listing string) {
	t.Helper()
	config := filepath.Join(f.bin, "hc.json")
	raw, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	var script struct {
		Log   string         `json:"log"`
		Rules []fakecli.Rule `json:"rules"`
	}
	if err := json.Unmarshal(raw, &script); err != nil {
		t.Fatal(err)
	}
	script.Rules = append(script.Rules, fakecli.Rule{Argv: []string{"ls", "--json"}, Stdout: listing})
	out, err := json.Marshal(script)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, out, 0o600); err != nil {
		t.Fatal(err)
	}
}

func runCommandOutput(t *testing.T, argv []string, env platform.Env) (int, string, string) {
	t.Helper()
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, stderr strings.Builder
	platform.Stdout, platform.Stderr = &out, &stderr
	code := Run(argv, env)
	platform.Stdout, platform.Stderr = oldOut, oldErr
	return code, out.String(), stderr.String()
}

func writeHcReleasedPanes(t *testing.T, f *dispatchHcFixture, rows ...string) {
	t.Helper()
	content := "# name\tpane\tkind\treleased\n" + strings.Join(rows, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(f.sd, "released-panes.tsv"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDispatchWaitNameLiveChildTerminal(t *testing.T) {
	for _, tc := range []struct {
		caseName, name, pane, parent, record, hint string
		registry                                   []string
	}{
		{
			caseName: "released child keeps the hint",
			name:     "child", pane: "t7", parent: "t1",
			record:   `"status":{"state":"idle","app":"claude-code"}`,
			registry: []string{"child\tt7\tcodex\tnow"},
			hint:     "; terminal t7 named 'child' is still open outside the roster (released without --close); close it with: soho release child --close",
		},
		{
			caseName: "foreign parent keeps no hint",
			name:     "foreign", pane: "t7", parent: "t9",
			record:   `"status":{"state":"idle","app":"claude-code"}`,
			registry: []string{"foreign\tt7\tcodex\tnow"},
		},
		{
			caseName: "wake child keeps no hint",
			name:     "wake-w1", pane: "t8", parent: "t1",
			record: `"status":{"state":"idle","app":"claude-code"},"argv":["/bin/sh","-c","soho wait w1 --wake"]`,
		},
		{
			caseName: "unregistered assistant child keeps no hint",
			name:     "assistant-1", pane: "t9", parent: "t1",
			record: `"status":{"state":"idle","app":"claude-code"}`,
		},
		{
			caseName: "registry row with another pane keeps no hint",
			name:     "child", pane: "t7", parent: "t1",
			record:   `"status":{"state":"idle","app":"claude-code"}`,
			registry: []string{"child\tt8\tcodex\tnow"},
		},
	} {
		t.Run(tc.caseName, func(t *testing.T) {
			f := newDispatchHcFixture(t, nil)
			if len(tc.registry) > 0 {
				writeHcReleasedPanes(t, f, tc.registry...)
			}
			addHcUnfilteredList(t, f, `{"terminals":[{"address":"`+tc.pane+`","name":"`+tc.name+`","state":"live","parent":"`+tc.parent+`",`+tc.record+`,"labels":{"soho.session":"sX"}}]}`)

			code, _, stderr := runCommandOutput(t, []string{"dispatch", tc.name, f.brief}, f.env)
			base := "soho: agent '" + tc.name + "' is not in this skill's roster (spawn it first, or pass a name you spawned)"
			if code != 3 || stderr != base+tc.hint+"\n" {
				t.Fatalf("dispatch: code=%d stderr=%q want 3 %q", code, stderr, base+tc.hint+"\n")
			}

			code, _, stderr = runCommandOutput(t, []string{"wait", tc.name}, f.env)
			base = "soho: agent '" + tc.name + "' is not in the roster (state dir: " + f.sd + ")"
			if code != 3 || stderr != base+tc.hint+"\n" {
				t.Fatalf("wait: code=%d stderr=%q want 3 %q", code, stderr, base+tc.hint+"\n")
			}
			calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "hc.calls.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			for _, call := range calls {
				for _, verb := range []string{"kill", "rm", "run"} {
					if len(call.Argv) > 0 && call.Argv[0] == verb {
						t.Fatalf("the read-only hint called hc %v", call.Argv)
					}
				}
			}
		})
	}
}

func TestDispatchWaitNoTerminalKeepsMessage(t *testing.T) {
	f := newDispatchHcFixture(t, nil)
	addHcUnfilteredList(t, f, `{"terminals":[]}`)
	code, _, stderr := runCommandOutput(t, []string{"dispatch", "absent", f.brief}, f.env)
	want := "soho: agent 'absent' is not in this skill's roster (spawn it first, or pass a name you spawned)\n"
	if code != 3 || stderr != want {
		t.Fatalf("dispatch: code=%d stderr=%q want 3 %q", code, stderr, want)
	}
	code, _, stderr = runCommandOutput(t, []string{"wait", "absent"}, f.env)
	want = "soho: agent 'absent' is not in the roster (state dir: " + f.sd + ")\n"
	if code != 3 || stderr != want {
		t.Fatalf("wait: code=%d stderr=%q want 3 %q", code, stderr, want)
	}
}

// TestDispatchHintCloseRoundTrip is the end-to-end proof of the hint: release
// without --close writes the registry row and the stays-open line, dispatch
// then offers a close command that works, and release --close closes the
// terminal and removes the row. The fake hc records every effect.
func TestDispatchHintCloseRoundTrip(t *testing.T) {
	f := newDispatchHcFixture(t, []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcReleaseWorkerLive},
		{Argv: []string{"kill", "t7", "--json"}, ArgvPrefix: true, Stdout: `{"address":"t7","state":"finished"}`},
		{Argv: []string{"rm", "t7", "--json"}, ArgvPrefix: true, Stdout: `{"removed":"t7"}`},
		{Argv: []string{"get", "t7", "--json"}, ArgvPrefix: true, Code: 3, Stderr: `{"error":{"kind":"not_found","message":"no such terminal"}}`},
	})
	// A skill-created terminal, so release without --close may register it.
	roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\tmode\targs\teffort\nworker\tt7\tcodex\timplementer\topenai\t1\t" + f.root + "\tnow\tgpt-5\ttask\timplementer\t\t\t--no-daemon\thigh\n"
	if err := os.WriteFile(filepath.Join(f.sd, "agents.tsv"), []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(f.sd, "reports", "worker.md")
	if err := os.WriteFile(report, []byte("done\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(core.LastReportPath(f.sd, "worker"), []byte(report+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	addHcUnfilteredList(t, f, `{"terminals":[{"address":"t7","name":"worker","state":"live","parent":"t1","labels":{"soho.session":"sX"},"status":{"state":"idle"}}]}`)

	code, _, stderr := runCommandOutput(t, []string{"release", "worker"}, f.env)
	if code != 0 || !strings.Contains(stderr, "soho: release: terminal t7 stays open outside the roster; close it later with: soho release worker --close") {
		t.Fatalf("release without --close: code=%d stderr=%q", code, stderr)
	}
	registry := filepath.Join(f.sd, "released-panes.tsv")
	rows, err := os.ReadFile(registry)
	if err != nil {
		t.Fatalf("registry missing after release: %v", err)
	}
	if !strings.Contains(string(rows), "worker\tt7\t") {
		t.Fatalf("registry=%q, want the worker row", rows)
	}

	code, _, stderr = runCommandOutput(t, []string{"dispatch", "worker", f.brief}, f.env)
	hint := "; terminal t7 named 'worker' is still open outside the roster (released without --close); close it with: soho release worker --close"
	want := "soho: agent 'worker' is not in this skill's roster (spawn it first, or pass a name you spawned)" + hint + "\n"
	if code != 3 || stderr != want {
		t.Fatalf("dispatch after release: code=%d stderr=%q want 3 %q", code, stderr, want)
	}

	code, out, stderr := runCommandOutput(t, []string{"release", "worker", "--close"}, f.env)
	if code != 0 || !strings.Contains(out, "released worker (terminal t7 closed; it was no longer in the roster)\n") {
		t.Fatalf("release --close: code=%d stdout=%q stderr=%q", code, out, stderr)
	}
	calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "hc.calls.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	kills, rms := hcCallIndexes(calls, []string{"kill", "t7"}), hcCallIndexes(calls, []string{"rm", "t7"})
	if len(kills) != 1 || len(rms) != 1 || rms[0] < kills[0] {
		t.Fatalf("kill/rm calls: kills=%v rms=%v", kills, rms)
	}
	rows, err = os.ReadFile(registry)
	if err != nil {
		t.Fatalf("registry missing after close: %v", err)
	}
	if strings.Contains(string(rows), "worker\tt7\t") {
		t.Fatalf("registry row kept after close: %q", rows)
	}
}
