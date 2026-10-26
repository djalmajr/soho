package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/taskreport"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// TestDispatchAmendRefusesRetiredTask: after a fresh spawn under the same
// name retires the pointer, --amend has no open task to amend. It is refused
// before anything is typed, and the pointer and the prior stable report stay
// byte-identical.
func TestDispatchAmendRefusesRetiredTask(t *testing.T) {
	f := newReopenFixture(t, "idle")
	for k, v := range map[string]string{"SOHO_LAYOUT": "split", "SOHO_PROMPT_CHECK_SECONDS": "0", "SOHO_PROMPT_SETTLE_SECONDS": "0", "SOHO_BRIEF_LINT": "off", "SOHO_PRESSURE_REFUSE": "off", "SOHO_MAX_WORKERS": "0"} {
		f.env[k] = v
	}
	rules := append([]fakecli.Rule{{Argv: []string{"agent", "prompt"}, ArgvPrefix: true, Stdout: `{"result":{"submitted":true}}`}}, reopenRules(t, f)...)
	b, err := json.Marshal(struct {
		Log   string         `json:"log"`
		Rules []fakecli.Rule `json:"rules"`
	}{filepath.Join(f.fakeDir, "herdr.calls.jsonl"), rules})
	if err != nil {
		t.Fatal(err)
	}
	reopenDispatchWrite(t, f.configFile, string(b))
	brief := filepath.Join(f.cwd, "brief.md")
	reopenDispatchWrite(t, brief, "original task\n")
	code, out, stderr := reopenDispatchRun(t, f.env, "dispatch", "worker", brief, "--no-wait")
	if code != 0 {
		t.Fatalf("dispatch: %d %s %s", code, out, stderr)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	reopenDispatchWrite(t, result["report"].(string), "completed before fresh spawn\n")
	if code, out, stderr = reopenDispatchRun(t, f.env, "collect", "worker"); code != 0 {
		t.Fatalf("collect: %d %s %s", code, out, stderr)
	}
	pointer := taskreport.ReadTaskReportPointer(f.state, "worker")
	value, _ := pointer.Get("task_report")
	stable := value.(string)
	before := mustRead(t, stable)
	if code, out, stderr = reopenDispatchRun(t, f.env, "spawn", "implementer", "--fresh", "--name", "worker", "--kind", "grok", "--cwd", f.cwd, "--force"); code != 0 {
		t.Fatalf("spawn: %d %s %s", code, out, stderr)
	}
	prior := mustRead(t, taskreport.TaskReportPointerPath(f.state, "worker"))
	amend := filepath.Join(f.cwd, "amend.md")
	reopenDispatchWrite(t, amend, "amend after fresh spawn\n")
	code, out, stderr = reopenDispatchRun(t, f.env, "dispatch", "worker", amend, "--amend", "--no-wait")
	if code != 2 || !strings.Contains(stderr, "its last task was retired") {
		t.Fatalf("amend of a retired task: code=%d stdout=%q stderr=%q; want 2 and the retired-task refusal", code, out, stderr)
	}
	if mustRead(t, taskreport.TaskReportPointerPath(f.state, "worker")) != prior {
		t.Fatal("the refused amendment changed the pointer")
	}
	if after, err := os.ReadFile(stable); err != nil || string(after) != before {
		t.Fatalf("the refused amendment changed the stable report: %q err=%v; want %q", after, err, before)
	}
}
