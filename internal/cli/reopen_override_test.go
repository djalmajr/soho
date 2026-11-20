package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/taskreport"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func setReopenRulesWith(t *testing.T, f *releaseFixture, extra []fakecli.Rule) {
	t.Helper()
	rules := append([]fakecli.Rule{}, extra...)
	rules = append(rules, reopenRules(t, f)...)
	config := struct {
		Log   string         `json:"log"`
		Rules []fakecli.Rule `json:"rules"`
	}{Log: filepath.Join(f.fakeDir, "herdr.calls.jsonl"), Rules: rules}
	b, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.configFile, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReopenOverridesKindModelAndEffort(t *testing.T) {
	for _, tc := range []struct {
		name, kind, model, effort string
		wantArgv                  []string
		wantRoster                []string
	}{
		{"kind only", "codex", "", "", []string{"--kind", "codex"}, []string{"worker\tp-new\tcodex\timplementer"}},
		{"model only", "", "gpt-5.2", "", []string{"--model", "gpt-5.2"}, []string{"\tgpt-5.2\t"}},
		{"effort only", "", "", "low", []string{"--reasoning-effort", "low"}, []string{"low\tnone"}},
		{"all three", "codex", "gpt-5.2", "low", []string{"--kind", "codex", "--model", "gpt-5.2"}, []string{"worker\tp-new\tcodex\timplementer", "\tgpt-5.2\t", "low\tnone"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newReopenFixture(t, "idle")
			reopenAddReport(t, f)
			setReopenRules(t, f)
			f.env["SOHO_LAYOUT"] = "split"
			args := []string{"worker"}
			if tc.kind != "" {
				args = append(args, "--kind", tc.kind)
			}
			if tc.model != "" {
				args = append(args, "--model", tc.model)
			}
			if tc.effort != "" {
				args = append(args, "--effort", tc.effort)
			}
			run := f.reopen(t, args...)
			if run.code != 0 {
				t.Fatalf("exit=%d stdout=%q stderr=%q", run.code, run.stdout, run.stderr)
			}
			argv := reopenStartArgv(run)
			if strings.Contains(tc.effort, "low") {
				joined := strings.Join(argv, " ")
				if !strings.Contains(joined, "low") {
					t.Fatalf("agent start argv=%#v, missing the effort override", argv)
				}
			} else {
				for i := 0; i+1 < len(tc.wantArgv); i += 2 {
					if !containsReopenArgPair(argv, tc.wantArgv[i], tc.wantArgv[i+1]) {
						t.Fatalf("agent start argv=%#v, missing %s %s", argv, tc.wantArgv[i], tc.wantArgv[i+1])
					}
				}
			}
			if tc.model != "" {
				found := false
				for _, arg := range argv {
					if strings.Contains(arg, tc.model) {
						found = true
					}
				}
				if !found {
					t.Fatalf("agent start argv=%#v, missing model %s", argv, tc.model)
				}
			}
			if tc.kind != "" && tc.model == "" && containsReopenArgPair(argv, "--model", "grok-4.7") {
				t.Fatalf("a kind override carried the old kind's model: %#v", argv)
			}
			roster := f.rosterFile(t)
			for _, want := range tc.wantRoster {
				if !strings.Contains(roster, want) {
					t.Fatalf("roster=%q, missing %q", roster, want)
				}
			}
		})
	}
}

func reopenAddReport(t *testing.T, f *releaseFixture) {
	t.Helper()
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
}

func reopenRecordBrief(t *testing.T, f *releaseFixture) {
	t.Helper()
	reopenAddReport(t, f)
	base := "worker-20261119T200000"
	briefs := filepath.Join(f.state, "briefs")
	if err := os.MkdirAll(briefs, 0o700); err != nil {
		t.Fatal(err)
	}
	composed := "# Role: implementer\n\nrole body\n\n# Brief\n\n# Goal\n\nRedispatch this brief.\n\n# Report contract\n\nwrite the report\n"
	if err := os.WriteFile(filepath.Join(briefs, base+".md"), []byte(composed), 0o600); err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(f.state, "reports", base+".md")
	if err := os.WriteFile(report, []byte("# done\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pointer := map[string]any{
		"version":     1,
		"task_report": filepath.Join(f.state, "reports", base+".current.md"),
		"current":     report,
		"history":     []string{},
	}
	b, err := json.Marshal(pointer)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(taskreport.TaskReportPointerPath(f.state, "worker"), append(b, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(core.LastReportPath(f.state, "worker"), []byte(report+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReopenRedispatchSendsTheLastBriefAfterTheSpawnJSON(t *testing.T) {
	f := newReopenFixture(t, "idle")
	reopenRecordBrief(t, f)
	setReopenRulesWith(t, f, []fakecli.Rule{
		{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"submitted":true}}`},
		{Argv: []string{"pane", "title"}, ArgvPrefix: true, Code: 1},
	})
	f.env["SOHO_LAYOUT"] = "split"
	f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
	run := f.reopen(t, "worker", "--redispatch")
	if run.code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", run.code, run.stdout, run.stderr)
	}
	idx := strings.Index(run.stdout, "\n{\"wait_status\"")
	if idx < 0 {
		t.Fatalf("stdout=%q, want the spawn JSON then the dispatch JSON", run.stdout)
	}
	spawnOut, dispatchOut := run.stdout[:idx], strings.TrimSpace(run.stdout[idx+1:])
	if !spawnJSONHasFieldCLI(t, spawnOut, `"name": "worker"`) || !spawnJSONHasFieldCLI(t, spawnOut, `"created_pane": true`) {
		t.Fatalf("spawn JSON=%q", spawnOut)
	}
	if strings.Contains(dispatchOut, "\n") || !strings.Contains(dispatchOut, `"agent":"worker"`) {
		t.Fatalf("dispatch JSON=%q", dispatchOut)
	}
	var dispatched struct {
		Composed string `json:"composed_prompt"`
	}
	if err := json.Unmarshal([]byte(dispatchOut), &dispatched); err != nil || dispatched.Composed == "" {
		t.Fatalf("dispatch JSON=%q err=%v", dispatchOut, err)
	}
	prompt, err := os.ReadFile(dispatched.Composed)
	if err != nil || !strings.Contains(string(prompt), "Redispatch this brief.") {
		t.Fatalf("redispatched prompt=%q err=%v", prompt, err)
	}
	sent := false
	for _, call := range run.calls {
		if len(call.Argv) >= 2 && call.Argv[0] == "agent" && call.Argv[1] == "prompt" {
			sent = true
		}
	}
	if !sent {
		t.Fatal("no prompt was sent to the worker")
	}
	for _, call := range run.calls {
		if len(call.Argv) >= 2 && call.Argv[0] == "wait" {
			t.Fatalf("redispatch waited: %#v", call)
		}
	}
}

func TestReopenRedispatchWithoutARecordedBriefClosesNothing(t *testing.T) {
	f := newReopenFixture(t, "idle")
	setReopenRules(t, f)
	before := f.rosterFile(t)
	run := f.reopen(t, "worker", "--redispatch")
	if run.code != 2 || !strings.Contains(run.stderr, "reopen: 'worker' has no brief to redispatch") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", run.code, run.stdout, run.stderr)
	}
	if after := f.rosterFile(t); after != before {
		t.Fatalf("the roster changed\nbefore:\n%s\nafter:\n%s", before, after)
	}
	for _, call := range run.calls {
		if len(call.Argv) >= 2 && call.Argv[0] == "pane" && call.Argv[1] == "close" {
			t.Fatalf("a worker was closed without a brief: %#v", run.calls)
		}
	}
}

func TestReopenFailedSpawnReportsWhatWasClosedAndNeverRedispatches(t *testing.T) {
	f := newReopenFixture(t, "idle")
	reopenRecordBrief(t, f)
	setReopenRulesWith(t, f, []fakecli.Rule{
		{Argv: []string{"agent", "start"}, ArgvPrefix: true, Stderr: `{"error":{"code":"start_failed","message":"no capacity"}}`, Code: 1},
		{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"submitted":true}}`},
	})
	f.env["SOHO_LAYOUT"] = "split"
	f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
	run := f.reopen(t, "worker", "--redispatch")
	if run.code == 0 {
		t.Fatalf("a failed spawn returned 0: stdout=%q stderr=%q", run.stdout, run.stderr)
	}
	if !strings.Contains(run.stderr, "reopen already closed 'worker' (pane p-worker)") {
		t.Fatalf("stderr=%q, want the closed worker named", run.stderr)
	}
	for _, call := range run.calls {
		if len(call.Argv) >= 2 && call.Argv[0] == "agent" && call.Argv[1] == "prompt" {
			t.Fatalf("a redispatch ran after a failed spawn: %#v", run.calls)
		}
	}
	closed := false
	for _, call := range run.calls {
		if len(call.Argv) >= 2 && call.Argv[0] == "pane" && call.Argv[1] == "close" {
			closed = true
		}
	}
	if !closed {
		t.Fatalf("the failed spawn did not report a real close: %#v", run.calls)
	}
}

func TestReopenKindOverrideWithLanesOnKeepsTheSlot(t *testing.T) {
	f := reopenLaneKindFixture(t, "codex")
	run := f.reopen(t, "worker", "--kind", "codex")
	if run.code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", run.code, run.stdout, run.stderr)
	}
	roster := f.rosterFile(t)
	for _, want := range []string{"worker\tp-new\tcodex\timplementer", "\tbuild\t"} {
		if !strings.Contains(roster, want) {
			t.Fatalf("roster=%q, missing %q", roster, want)
		}
	}
	if strings.Contains(roster, "grok-4.7") {
		t.Fatalf("the old kind's model was carried into the new roster row: %q", roster)
	}
	argv := reopenStartArgv(run)
	if !containsReopenArgPair(argv, "--kind", "codex") {
		t.Fatalf("spawn did not get the explicit kind: %#v", argv)
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
}

func TestReopenKindOverrideSwapsAWorkerInAPinnedLane(t *testing.T) {
	f := reopenLaneKindFixture(t, "grok")
	run := f.reopen(t, "worker", "--kind", "claude")
	if run.code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", run.code, run.stdout, run.stderr)
	}
	roster := f.rosterFile(t)
	for _, want := range []string{"worker\tp-new\tclaude\timplementer", "\tbuild\t"} {
		if !strings.Contains(roster, want) {
			t.Fatalf("roster=%q, missing %q", roster, want)
		}
	}
	if strings.Contains(roster, "grok-4.7") {
		t.Fatalf("the old kind's model was carried into the new roster row: %q", roster)
	}
	argv := reopenStartArgv(run)
	if !containsReopenArgPair(argv, "--kind", "claude") {
		t.Fatalf("spawn did not get the explicit kind: %#v", argv)
	}
}

func reopenRecordAmendment(t *testing.T, f *releaseFixture) (briefPath, amendmentPath string) {
	t.Helper()
	reopenAddReport(t, f)
	briefs := filepath.Join(f.state, "briefs")
	if err := os.MkdirAll(briefs, 0o700); err != nil {
		t.Fatal(err)
	}
	originalBase, amendmentBase := "worker-20261119T200000", "worker-20261119T201000"
	briefPath, amendmentPath = filepath.Join(briefs, originalBase+".md"), filepath.Join(briefs, amendmentBase+".md")
	if err := os.WriteFile(briefPath, []byte("# Role: implementer\n\n# Brief\n\n# Goal\n\nOriginal brief.\n\n# Report contract\n\nwrite it\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(amendmentPath, []byte("# Amendment to your current brief\n\nChange it.\n\n# Report contract\n\nwrite it\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	originalReport, amendmentReport := filepath.Join(f.state, "reports", originalBase+".md"), filepath.Join(f.state, "reports", amendmentBase+".md")
	for _, path := range []string{originalReport, amendmentReport} {
		if err := os.WriteFile(path, []byte("# done\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	pointer := map[string]any{
		"version":     1,
		"task_report": filepath.Join(f.state, "reports", originalBase+".current.md"),
		"current":     amendmentReport,
		"history":     []string{originalReport},
	}
	b, err := json.Marshal(pointer)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(taskreport.TaskReportPointerPath(f.state, "worker"), append(b, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(core.LastReportPath(f.state, "worker"), []byte(amendmentReport+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return briefPath, amendmentPath
}

func TestReopenRedispatchOfAnAmendmentNamesBothPathsAndClosesNothing(t *testing.T) {
	f := newReopenFixture(t, "idle")
	briefPath, amendmentPath := reopenRecordAmendment(t, f)
	setReopenRules(t, f)
	before := f.rosterFile(t)
	run := f.reopen(t, "worker", "--redispatch")
	want := "reopen: the last task of 'worker' was an amendment; after reopening, run: soho dispatch worker " + briefPath + " and then soho dispatch worker " + amendmentPath + " --amend"
	if run.code != 2 || !strings.Contains(run.stderr, want) {
		t.Fatalf("exit=%d stdout=%q stderr=%q, want %q", run.code, run.stdout, run.stderr, want)
	}
	if after := f.rosterFile(t); after != before {
		t.Fatalf("the roster changed\nbefore:\n%s\nafter:\n%s", before, after)
	}
	for _, call := range run.calls {
		if len(call.Argv) >= 2 && call.Argv[0] == "pane" && call.Argv[1] == "close" {
			t.Fatalf("an amendment reopen closed the pane: %#v", run.calls)
		}
	}
}
