package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
)

// R420 F1: a blocking dispatch returns the committed_during_task list of its
// settled wait unchanged (the audit runs once, so a later wait cannot
// recover a dropped field).
func TestBlockingDispatchForwardsCommittedDuringTask(t *testing.T) {
	f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
	f.env["PATH"] += string(os.PathListSeparator) + waitingGitOnlyDir(t)
	f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
	f.env["SOHO_SETTLED_GRACE"], f.env["SOHO_WAIT_POLL_MS"] = "0", "1"
	headInit(t, f.env, f.root)
	reopenDispatchWrite(t, f.brief, "Worktree: `"+f.root+"`\n\n## Owned files\n- owned.txt\n\nDo the task.\n")
	realWait := dispatchWaitFor
	t.Cleanup(func() { dispatchWaitFor = realWait })
	var nested bytes.Buffer
	dispatchWaitFor = func(agents []string, sd string, ctx *core.Config, env platform.Env, timeout float64, any bool, cwd string) int {
		// The orchestrator commits a file outside Owned while the worker runs.
		if err := os.MkdirAll(filepath.Join(f.root, "scripts"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.root, "scripts", "x.sh"), []byte("echo\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		headGit(t, f.env, f.root, "add", "scripts/x.sh")
		headGit(t, f.env, f.root, "commit", "-m", "orchestrator commit during the task")
		reopenDispatchWrite(t, core.LastReport(sd, "worker"), "Completed.\n")
		out := platform.Stdout
		platform.Stdout = io.MultiWriter(out, &nested)
		defer func() { platform.Stdout = out }()
		return realWait(agents, sd, ctx, env, timeout, any, cwd)
	}
	code, out, stderr := f.run(t, "worker", f.brief, "--timeout", "1s")
	if code != 0 {
		t.Fatalf("dispatch exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	waited, err := jsonjs.Parse([]byte(strings.TrimSpace(nested.String())))
	if err != nil {
		t.Fatalf("nested wait output %q: %v", nested.String(), err)
	}
	waitCommitted, ok := waited.(*jsonjs.Object).Get("committed_during_task")
	if !ok || jsonjs.Stringify(waitCommitted) != `["scripts/x.sh"]` {
		t.Fatalf("the settled wait must report the orchestrator's commit: %s", nested.String())
	}
	returned, err := jsonjs.Parse([]byte(strings.TrimSpace(out)))
	if err != nil {
		t.Fatal(err)
	}
	committed, ok := returned.(*jsonjs.Object).Get("committed_during_task")
	if !ok || jsonjs.Stringify(committed) != jsonjs.Stringify(waitCommitted) {
		t.Fatalf("blocking dispatch dropped or changed committed_during_task: dispatch=%s wait=%s", out, nested.String())
	}
	if strings.Contains(out, "outside_owned") {
		t.Fatalf("the orchestrator's commit must not be outside_owned: %s", out)
	}
}

// R420 F1: the duplicate-dispatch emitter forwards committed_during_task.
func TestDuplicateDispatchForwardsCommittedDuringTask(t *testing.T) {
	root := t.TempDir()
	committed := []any{"scripts/x.sh"}
	last := jsonjs.O("status", "done", "committed_during_task", committed)
	dup := &dispatchDuplicate{composed: filepath.Join(root, "brief.md"), report: filepath.Join(root, "report.md"), taskReport: filepath.Join(root, "task.md"), duplicateOf: "prior-brief"}
	var out bytes.Buffer
	saved := platform.Stdout
	platform.Stdout = &out
	t.Cleanup(func() { platform.Stdout = saved })
	code := emitDuplicateDispatchResult("worker", "implementer", "codex", dup, last, 0, root)
	v, err := jsonjs.Parse(out.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	got, present := v.(*jsonjs.Object).Get("committed_during_task")
	if code != 0 || !present || jsonjs.Stringify(got) != `["scripts/x.sh"]` {
		t.Fatalf("exit=%d stdout=%s", code, out.String())
	}
}
