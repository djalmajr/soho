package cli

import (
	"bytes"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func init() {
	if len(os.Args) > 1 && os.Args[1] == "review338-collect" {
		env := platform.Env{}
		for _, kv := range os.Environ() {
			k, v, ok := strings.Cut(kv, "=")
			if ok {
				env[k] = v
			}
		}
		os.Exit(Run([]string{"collect", "worker"}, env))
	}
}

func review338Fixture(t *testing.T) (*dispatchArrivalFixture, string, string, string) {
	t.Helper()
	f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
	f.env["PATH"] += string(os.PathListSeparator) + waitingGitOnlyDir(t)
	f.env["SOHO_SETTLED_GRACE"], f.env["SOHO_WAIT_POLL_MS"] = "0", "1"
	f.env["SOHO_PRESSURE_REFUSE"], f.env["SOHO_PRESSURE_WARN"] = "off", "off"
	f.env["SOHO_METRICS"] = "off"
	old := headInit(t, f.env, f.root)
	composed, report := firstDispatch(t, f)
	reopenDispatchWrite(t, report, "Completed.\n")
	return f, composed, report, old
}

func TestReview338ConcurrentCollectWarnsAtMostOnce(t *testing.T) {
	review338ConcurrentClaim(t)
}

func TestReview338DuplicateDispatchPreservesHeadMoved(t *testing.T) {
	root := t.TempDir()
	move := jsonjs.O("from", "old-head", "to", "new-head", "branch_from", "main", "branch_to", "other")
	last := jsonjs.O("status", "done", "head_moved", move)
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
	obj, ok := v.(*jsonjs.Object)
	if !ok {
		t.Fatalf("unexpected JSON: %s", out.String())
	}
	got, present := obj.Get("head_moved")
	status, _ := obj.Get("wait_status")
	duplicate, _ := obj.Get("duplicate_of")
	if code != 0 || status != "done" || duplicate != "prior-brief" || !present || jsonjs.Stringify(got) != jsonjs.Stringify(move) {
		t.Fatalf("exit=%d stdout=%s", code, out.String())
	}
	t.Logf("exit=%d stdout=%s", code, out.String())
}

func TestReview338BlockingDispatchPreservesHeadMoved(t *testing.T) {
	f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
	f.env["PATH"] += string(os.PathListSeparator) + waitingGitOnlyDir(t)
	f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
	f.env["SOHO_SETTLED_GRACE"], f.env["SOHO_WAIT_POLL_MS"] = "0", "1"
	old := headInit(t, f.env, f.root)
	reopenDispatchWrite(t, f.brief, "Worktree: `"+f.root+"`\nDo the task.\n")
	realWait := dispatchWaitFor
	t.Cleanup(func() { dispatchWaitFor = realWait })
	var nested bytes.Buffer
	dispatchWaitFor = func(agents []string, sd string, ctx *core.Config, env platform.Env, timeout float64, any bool, cwd string) int {
		// Fake worker completes only after dispatch has recorded its snapshot.
		headGit(t, f.env, f.root, "commit", "--allow-empty", "-m", "worker commit during blocking dispatch")
		reopenDispatchWrite(t, core.LastReport(sd, "worker"), "Completed.\n")
		out := platform.Stdout
		platform.Stdout = io.MultiWriter(out, &nested)
		defer func() { platform.Stdout = out }()
		return realWait(agents, sd, ctx, env, timeout, any, cwd)
	}
	code, out, stderr := f.run(t, "worker", f.brief, "--timeout", "1s")
	t.Logf("initial HEAD=%s nested real wait=%s blocking dispatch exit=%d stdout=%s stderr=%s", old, nested.String(), code, out, stderr)
	if code != 0 || !strings.Contains(nested.String(), "head_moved") || !strings.Contains(stderr, "left a local commit") {
		t.Fatalf("fixture did not settle with movement")
	}
	secondCode, secondOut, secondErr := reopenDispatchRun(t, f.env, "wait", "worker", "--timeout", "1s")
	t.Logf("subsequent wait exit=%d stdout=%s stderr=%s", secondCode, secondOut, secondErr)
	if !strings.Contains(out, "head_moved") {
		t.Fatalf("blocking dispatch discarded head_moved from its real settled wait; subsequent wait cannot recover the consumed once-only movement")
	}
	returned, err := jsonjs.Parse([]byte(strings.TrimSpace(out)))
	if err != nil {
		t.Fatal(err)
	}
	waited, err := jsonjs.Parse([]byte(strings.TrimSpace(nested.String())))
	if err != nil {
		t.Fatal(err)
	}
	result := returned.(*jsonjs.Object)
	movement, _ := result.Get("head_moved")
	waitMovement, _ := waited.(*jsonjs.Object).Get("head_moved")
	status, _ := result.Get("wait_status")
	if status != "done" || jsonjs.Stringify(movement) != jsonjs.Stringify(waitMovement) || secondCode != 0 || secondErr != "" || strings.Contains(secondOut, "head_moved") {
		t.Fatalf("dispatch changed settled semantics: exit=%d stdout=%s next_exit=%d next_out=%s next_err=%s", code, out, secondCode, secondOut, secondErr)
	}
}
