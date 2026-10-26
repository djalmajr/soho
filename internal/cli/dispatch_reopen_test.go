package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func reopenDispatchRun(t *testing.T, env platform.Env, args ...string) (int, string, string) {
	t.Helper()
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, errOut bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &errOut
	defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
	code := Run(args, env)
	return code, out.String(), errOut.String()
}

func reopenDispatchWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Mutation captured: disabling task retirement suppresses the next dispatch.
func TestDispatchReopenSameBrief(t *testing.T) {
	f := newReopenFixture(t, "idle")
	for k, v := range map[string]string{"SOHO_LAYOUT": "split", "SOHO_PROMPT_CHECK_SECONDS": "0", "SOHO_PROMPT_SETTLE_SECONDS": "0", "SOHO_BRIEF_LINT": "off", "SOHO_SETTLED_GRACE": "0", "SOHO_WAIT_POLL_MS": "1", "SOHO_PRESSURE_REFUSE": "off"} {
		f.env[k] = v
	}
	rules := append([]fakecli.Rule{{Argv: []string{"agent", "prompt"}, ArgvPrefix: true, Stdout: `{"result":{"submitted":true}}`}}, reopenRules(t, f)...)
	raw, err := json.Marshal(struct {
		Log   string         `json:"log"`
		Rules []fakecli.Rule `json:"rules"`
	}{filepath.Join(f.fakeDir, "herdr.calls.jsonl"), rules})
	if err != nil {
		t.Fatal(err)
	}
	reopenDispatchWrite(t, f.configFile, string(raw))
	brief := filepath.Join(f.cwd, "reopen-brief.md")
	reopenDispatchWrite(t, brief, "# Goal\nDo the task.\n")
	code, out, stderr := reopenDispatchRun(t, f.env, "dispatch", "worker", brief, "--no-wait")
	if code != 0 {
		t.Fatalf("first=%d out=%s err=%s", code, out, stderr)
	}
	r := f.reopen(t, "worker", "--force")
	if r.code != 0 {
		t.Fatalf("reopen=%d out=%s err=%s", r.code, r.stdout, r.stderr)
	}
	before, err := fakecli.ReadCallsForConfig(f.configFile)
	if err != nil {
		t.Fatal(err)
	}
	code, out, stderr = reopenDispatchRun(t, f.env, "dispatch", "worker", brief, "--no-wait")
	after, err := fakecli.ReadCallsForConfig(f.configFile)
	if err != nil {
		t.Fatal(err)
	}
	prompts := countArgvPrefix(after[len(before):], []string{"agent", "prompt"})
	if code != 0 || strings.Contains(out, `"duplicate_of"`) || prompts != 1 {
		t.Fatalf("second=%d out=%s err=%s new_prompt_calls=%d", code, out, stderr, prompts)
	}
	t.Logf("second dispatch exit=%d new_prompt_calls=%d duplicate_of=false", code, prompts)
}

// Mutation captured: removing pointer retirement leaves it after every successful release variant.
func TestReleaseRetiresTaskPointer(t *testing.T) {
	for _, args := range [][]string{{"worker"}, {"worker", "--close"}, {"worker", "--force"}, {"worker", "--close", "--force"}, {"worker", "--keep-copies", "--keep-procs"}} {
		t.Run(strings.Join(args, "-"), func(t *testing.T) {
			f := newReleaseFixture(t, "idle", "")
			pointer := filepath.Join(f.state, "task-report-worker.json")
			reopenDispatchWrite(t, pointer, `{"version":1,"task_report":"stable","current":"report","history":["earlier"],"custom":"kept"}`)
			r := f.run(t, args...)
			if r.code != 0 {
				t.Fatalf("exit=%d out=%s err=%s", r.code, r.stdout, r.stderr)
			}
			var retired map[string]any
			if err := json.Unmarshal([]byte(mustRead(t, pointer)), &retired); err != nil {
				t.Fatal(err)
			}
			if retired["current"] != "" || retired["task_report"] != "stable" || retired["custom"] != "kept" {
				t.Fatalf("retired=%v", retired)
			}
			history, ok := retired["history"].([]any)
			if !ok || len(history) != 2 || history[0] != "earlier" || history[1] != "report" {
				t.Fatalf("history=%v", retired["history"])
			}
			t.Logf("current empty, file retained, history=%v custom=%v", history, retired["custom"])
		})
	}
}
