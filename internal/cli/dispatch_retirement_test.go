package cli

import (
	"encoding/json"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/taskreport"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Regression: a layout move must not make the same open task eligible for a second delivery.
func TestReview362RegridPreservesDuplicate(t *testing.T) {
	f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0",
		fakecli.Rule{Argv: []string{"pane", "list", "--workspace", "ws"}, Stdout: `{"result":{"panes":[{"pane_id":"C","tab_id":"t0"},{"pane_id":"w0test:p0a","tab_id":"t0"}]}}`},
		fakecli.Rule{Argv: []string{"pane", "list"}, Stdout: `{"result":{"panes":[]}}`},
		fakecli.Rule{Argv: []string{"pane", "move", "w0test:p0a", "--new-tab", "--label", "herd-park", "--no-focus"}, Stdout: `{"result":{"move_result":{"pane":{"pane_id":"w0test:p0m","tab_id":"park"}}}}`},
		fakecli.Rule{Argv: []string{"pane", "move", "w0test:p0m", "--tab", "t0", "--split", "right", "--target-pane", "C", "--ratio", "0.5000", "--no-focus"}, Stdout: `{"result":{"move_result":{"pane":{"pane_id":"w0test:p0m"}}}}`},
	)
	f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
	firstDispatch(t, f)
	sd := filepath.Join(f.state, "ws")
	oldPointer := mustRead(t, filepath.Join(sd, "task-report-worker.json"))
	f.env["HERDR_PANE_ID"], f.env["HERDR_TAB_ID"] = "C", "t0"
	code, out, stderr := reopenDispatchRun(t, f.env, "regrid")
	if code != 0 || !strings.Contains(core.RosterLine(sd, "worker"), "w0test:p0m") {
		t.Fatalf("regrid code=%d out=%s err=%s roster=%s", code, out, stderr, core.RosterLine(sd, "worker"))
	}
	if mustRead(t, filepath.Join(sd, "task-report-worker.json")) != oldPointer {
		t.Fatal("pointer unexpectedly moved")
	}
	t.Logf("regrid exit=%d out=%s roster_pane=w0test:p0m pointer_unchanged=true", code, out)
	before, e := fakecli.ReadCallsForConfig(filepath.Join(f.bin, "herdr.json"))
	if e != nil {
		t.Fatal(e)
	}
	code, out, stderr = f.run(t, "worker", f.brief, "--no-wait")
	after, e := fakecli.ReadCallsForConfig(filepath.Join(f.bin, "herdr.json"))
	if e != nil {
		t.Fatal(e)
	}
	prompts := countArgvPrefix(after[len(before):], []string{"agent", "prompt"})
	t.Logf("redispatch exit=%d duplicate=%t new_prompt_calls=%d stderr=%q", code, strings.Contains(out, `"duplicate_of"`), prompts, stderr)
	if code != 0 || !strings.Contains(out, `"duplicate_of"`) || prompts != 0 {
		t.Fatalf("same live instance was delivered twice: code=%d out=%s prompts=%d", code, out, prompts)
	}
}

func TestDispatchRetiredHistoryDoesNotDuplicate(t *testing.T) {
	f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
	_, report := firstDispatch(t, f)
	sd := filepath.Join(f.state, "ws")
	if err := taskreport.RetireTaskReportPointer(sd, "worker"); err != nil {
		t.Fatal(err)
	}
	retired := mustRead(t, filepath.Join(sd, "task-report-worker.json"))
	if err := taskreport.RetireTaskReportPointer(sd, "worker"); err != nil {
		t.Fatal(err)
	}
	if mustRead(t, filepath.Join(sd, "task-report-worker.json")) != retired {
		t.Fatal("retirement not idempotent")
	}
	before, e := fakecli.ReadCallsForConfig(filepath.Join(f.bin, "herdr.json"))
	if e != nil {
		t.Fatal(e)
	}
	code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
	after, e := fakecli.ReadCallsForConfig(filepath.Join(f.bin, "herdr.json"))
	if e != nil {
		t.Fatal(e)
	}
	n := countArgvPrefix(after[len(before):], []string{"agent", "prompt"})
	if code != 0 || strings.Contains(out, `"duplicate_of"`) || n != 1 {
		t.Fatalf("code=%d out=%s err=%s prompts=%d prior=%s", code, out, stderr, n, report)
	}
}

func TestReopenFreshSpawnRetiresLegacyPointer(t *testing.T) {
	f := newReopenFixture(t, "idle")
	setReopenRules(t, f)
	f.env["SOHO_LAYOUT"] = "split"
	core.RosterRemove(f.state, "worker")
	pointer := filepath.Join(f.state, "task-report-worker.json")
	reopenDispatchWrite(t, pointer, `{"version":1,"task_report":"stable","current":"open","history":["old"],"pane":"metadata"}`)
	code, out, stderr := reopenDispatchRun(t, f.env, "spawn", "implementer", "--fresh", "--name", "worker", "--kind", "grok", "--cwd", f.cwd, "--force")
	if code != 0 {
		t.Fatalf("spawn code=%d out=%s err=%s", code, out, stderr)
	}
	var p map[string]any
	if e := json.Unmarshal([]byte(mustRead(t, pointer)), &p); e != nil {
		t.Fatal(e)
	}
	h, ok := p["history"].([]any)
	if p["current"] != "" || p["pane"] != "metadata" || !ok || len(h) != 2 || h[0] != "old" || h[1] != "open" {
		t.Fatalf("pointer=%v", p)
	}
}

// Regression: deleting only active instance state must not turn completed historical work into lost briefs.
func TestReview362ReleasePreservesLegacyStats(t *testing.T) {
	f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
	f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
	original, _ := firstDispatch(t, f)
	amend := filepath.Join(f.root, "amend.md")
	reopenDispatchWrite(t, amend, "Change it.\n")
	code, out, stderr := f.run(t, "worker", amend, "--amend", "--no-wait")
	if code != 0 {
		t.Fatalf("amend %d %s %s", code, out, stderr)
	}
	composed, _ := dispatchOutputField(t, out, "composed_prompt")
	report, _ := dispatchOutputField(t, out, "report")
	reopenDispatchWrite(t, report, "done\n")
	for _, p := range []string{original, composed} {
		sc := strings.TrimSuffix(p, ".md") + ".dispatch.json"
		var v map[string]any
		b, e := os.ReadFile(sc)
		if e != nil {
			t.Fatal(e)
		}
		if e = json.Unmarshal(b, &v); e != nil {
			t.Fatal(e)
		}
		delete(v, "task_report")
		b, e = json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
		reopenDispatchWrite(t, sc, string(b))
	}
	stats := func() map[string][]string {
		t.Helper()
		code, out, stderr := reopenDispatchRun(t, f.env, "stats", "--json")
		if code != 0 {
			t.Fatalf("stats %d %s %s", code, out, stderr)
		}
		var doc struct {
			LostBriefs map[string][]string `json:"lost_briefs"`
		}
		if e := json.Unmarshal([]byte(out), &doc); e != nil {
			t.Fatal(e)
		}
		t.Logf("stats JSON=%s", out)
		return doc.LostBriefs
	}
	before := stats()
	if len(before) != 0 {
		t.Fatalf("fixture lost before release: %v", before)
	}
	code, out, stderr = reopenDispatchRun(t, f.env, "release", "worker")
	if code != 0 {
		t.Fatalf("release %d %s %s", code, out, stderr)
	}
	t.Logf("release exit=%d stdout=%q stderr=%q", code, out, stderr)
	after := stats()
	if len(after) != 0 {
		t.Fatalf("release turned completed amended work into lost briefs: %v", after)
	}
}
