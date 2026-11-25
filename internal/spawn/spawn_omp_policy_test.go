package spawn_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/cli"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestReopenOMPManagedPolicy(t *testing.T) {
	const policy = "retry:\n  modelFallback: false\ntask:\n  maxRecursionDepth: 0\n"
	const oldPolicy = "retry:\n  modelFallback: false\ntools:\n  approval:\n    task: deny\n"
	for _, tc := range []struct {
		name, body  string
		priorPolicy bool
		wantCode    int
	}{
		{name: "missing", wantCode: 2},
		{name: "invalid", body: strings.Replace(policy, "maxRecursionDepth: 0", "maxRecursionDepth: 1", 1), wantCode: 2},
		{name: "replace historical worker", body: policy},
		{name: "replace worker with path-only policy", body: policy, priorPolicy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			cwd, bin, skill, state := filepath.Join(root, "work"), filepath.Join(root, "bin"), filepath.Join(root, "skill with spaces"), filepath.Join(root, "state")
			env := platform.Env{
				"HOME": root, "USERPROFILE": root, "XDG_CONFIG_HOME": root, "TMPDIR": root, "PATH": bin,
				"HERDR_ENV": "1", "HERDR_WORKSPACE_ID": "ws", "SOHO_CALLER_CHECK": "off", "SOHO_DIR": state,
				"SOHO_TRANSPORT": "herdr", "SOHO_SKILL_DIR": skill, "SOHO_LANES": "off", "SOHO_REGRID": "off", "SOHO_LAYOUT": "split",
			}
			sd := filepath.Join(state, "ws")
			for _, dir := range []string{cwd, bin, filepath.Join(skill, "roles"), filepath.Join(skill, "templates"), sd} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			t.Chdir(cwd)
			write := func(path, body string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			policyPath := filepath.Join(skill, "templates", "omp-managed-v2.yml")
			oldPath := filepath.Join(skill, "templates", "omp-managed-v1.yml")
			write(oldPath, oldPolicy)
			if tc.body != "" {
				write(policyPath, tc.body)
			}
			write(filepath.Join(skill, "roles", "worker.md"), "---\nkind: omp\napprovals: ask\n---\n")
			const model = "fixture/model"
			row := []string{"worker", "p-old", "omp", "worker", "", "1", cwd, "now", model, "full", "worker", "", "", "", "xhigh", "inherit", "", "--approval-mode yolo"}
			if tc.priorPolicy {
				row = append(row, "", "omp-policy:"+oldPath)
			}
			roster := filepath.Join(sd, "agents.tsv")
			before := "# header\n" + strings.Join(row, "\t") + "\n"
			write(roster, before)
			report := filepath.Join(root, "done.md")
			write(report, "done\n")
			pointer := core.LastReportPath(sd, "worker")
			write(pointer, report+"\n")
			newAgent := `{"result":{"agent":{"name":"worker","agent":"omp","pane_id":"p2","terminal_id":"term_new","agent_status":"idle","state_change_seq":1,"agent_session":{"agent":"omp","kind":"path","source":"herdr:omp","value":"new-session"}}}}`
			rules := []fakecli.Rule{
				{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"name":"worker","agent_status":"idle","pane_id":"p-old"}}}`},
				{Argv: []string{"pane", "close", "p-old"}, Stdout: `{"result":{}}`},
				{Argv: []string{"agent", "list"}, ArgvPrefix: true, Stdout: `{"result":{"agents":[]}}`},
				{Argv: []string{"pane", "list"}, ArgvPrefix: true, Stdout: `{"result":{"panes":[]}}`},
				{Argv: []string{"pane", "layout"}, ArgvPrefix: true, Stdout: `{"result":{"layout":{"area":{"width":1000,"height":700},"panes":[{"pane_id":"p1","rect":{"x":0,"y":0,"width":1000,"height":700}}]}}}`},
				{Argv: []string{"pane", "split"}, ArgvPrefix: true, Stdout: `{"result":{"pane":{"pane_id":"p2"}}}`},
				{Argv: []string{"tab", "create"}, ArgvPrefix: true, Stdout: `{"result":{"tab":{"tab_id":"t-herd","label":"herd"},"root_pane":{"pane_id":"p2"}}}`},
				{Argv: []string{"agent", "start", "worker"}, ArgvPrefix: true, Stdout: newAgent},
				{Argv: []string{"agent", "get", "p2"}, Stdout: newAgent},
				{Argv: []string{"agent", "read", "p2", "--source", "visible"}, Stdout: "π > fixture\n╰─ ⇧⇥ to change thinking effort\n"},
			}
			for exe, cases := range map[string][]fakecli.Rule{"herdr": rules, "omp": nil} {
				if _, err := fakecli.Install(t, bin, exe, cases); err != nil {
					t.Fatal(err)
				}
			}
			for _, item := range fakecli.Env(env.List(), bin) {
				key, value, ok := strings.Cut(item, "=")
				if ok {
					env[key] = value
				}
			}
			oldOut, oldErr := platform.Stdout, platform.Stderr
			var out, stderr bytes.Buffer
			platform.Stdout, platform.Stderr = &out, &stderr
			t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
			code := cli.Run([]string{"reopen", "worker", "--force"}, env)
			calls, err := fakecli.ReadCalls(filepath.Join(bin, "herdr.calls.jsonl"))
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			rowBytes, err := os.ReadFile(roster)
			if err != nil {
				t.Fatal(err)
			}
			if code != tc.wantCode {
				t.Fatalf("exit=%d want %d stdout=%q stderr=%q roster=%q calls=%+v", code, tc.wantCode, out.String(), stderr.String(), rowBytes, calls)
			}
			if code != 0 {
				pointerBody, err := os.ReadFile(pointer)
				if err != nil || string(pointerBody) != report+"\n" || string(rowBytes) != before || out.Len() != 0 || !strings.Contains(stderr.String(), "managed OMP policy omp-managed-v2.yml") {
					t.Fatalf("refusal altered old worker: out=%q stderr=%q roster=%q pointer=%q err=%v", out.String(), stderr.String(), rowBytes, pointerBody, err)
				}
				if len(calls) != 0 {
					t.Fatalf("preflight touched Herdr: %+v", calls)
				}
				return
			}
			var effects []string
			want := []string{"--approval-mode", "yolo", "--model", model, "--thinking", "xhigh", "--config", policyPath}
			for _, call := range calls {
				if len(call.Argv) < 2 {
					continue
				}
				switch strings.Join(call.Argv[:2], " ") {
				case "pane close":
					effects = append(effects, "close "+call.Argv[2])
				case "agent start":
					effects = append(effects, "start "+call.Argv[2])
					i := slices.Index(call.Argv, "--")
					if i < 0 || !slices.Equal(call.Argv[i+1:], want) {
						t.Fatalf("native launch argv=%q want %q", call.Argv, want)
					}
				}
			}
			if !slices.Equal(effects, []string{"close p-old", "start worker"}) {
				t.Fatalf("ordered effects=%q", effects)
			}
			var result struct {
				Name, Status string
				PaneID       string `json:"pane_id"`
			}
			err = json.Unmarshal(out.Bytes(), &result)
			fields := strings.Split(core.RosterLine(sd, "worker"), "\t")
			if err != nil || result.Name != "worker" || result.PaneID != "p2" || result.Status != "ready" ||
				!strings.Contains(string(rowBytes), "worker\tp2\tomp\tworker\t") || strings.Contains(string(rowBytes), "p-old") ||
				len(fields) != 20 || !strings.HasPrefix(fields[19], "omp-policy:v2:") {
				t.Fatalf("replacement not ready: stdout=%q stderr=%q roster=%q", out.String(), stderr.String(), rowBytes)
			}
		})
	}
}
