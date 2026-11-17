package cli

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/dispatch"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestDispatchSecondTaskSnapshotAndInheritance(t *testing.T) {
	f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
	f.env["PATH"] += string(os.PathListSeparator) + waitingGitOnlyDir(t)
	f.env["SOHO_SETTLED_GRACE"], f.env["SOHO_WAIT_POLL_MS"] = "0", "1"
	f.env["SOHO_METRICS"] = "off"
	headInit(t, f.env, f.root)
	reopenDispatchWrite(t, filepath.Join(f.root, "README.md"), "base\n")
	headGit(t, f.env, f.root, "add", "README.md")
	headGit(t, f.env, f.root, "commit", "-m", "fixture")
	reopenDispatchWrite(t, f.brief, "# Goal\nFirst.\n# Owned files\n- README.md\n")
	_, report := firstDispatch(t, f)
	reopenDispatchWrite(t, filepath.Join(f.root, "README.md"), "first\n")
	reopenDispatchWrite(t, report, "## Files changed\n- `README.md`\n")
	if code, out, errText := reopenDispatchRun(t, f.env, "wait", "worker", "--timeout", "2s"); code != 0 {
		t.Fatalf("first wait=%d %s %s", code, out, errText)
	}
	reopenDispatchWrite(t, f.brief, "# Goal\nSecond.\n# Owned files\n- inherit: previous brief\n")
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	if code != 0 || strings.Contains(errText, "no previous task") {
		t.Fatalf("second dispatch=%d %s %s", code, out, errText)
	}
	composed, _ := dispatchOutputField(t, out, "composed_prompt")
	secondReport, _ := dispatchOutputField(t, out, "report")
	second := headSidecar(t, dispatch.DispatchSidecar(composed))
	if !reflect.DeepEqual(second["git_previous_owned"], []any{"README.md"}) {
		t.Fatalf("prior scope=%v", second["git_previous_owned"])
	}
	snapshot, ok := second["git_snapshot"].(map[string]any)
	if !ok || snapshot["README.md"] == nil {
		t.Fatalf("snapshot=%v", second["git_snapshot"])
	}
	reopenDispatchWrite(t, filepath.Join(f.root, "README.md"), "second\n")
	reopenDispatchWrite(t, secondReport, "## Files changed\n- `README.md`\n")
	code, out, errText = reopenDispatchRun(t, f.env, "wait", "worker", "--timeout", "2s")
	if code != 0 || strings.Contains(out, "outside_owned") || strings.Contains(errText, "outside its Owned files") {
		t.Fatalf("second wait=%d %s %s", code, out, errText)
	}
}

func TestDispatchSnapshotsBeforeWorkerCanEdit(t *testing.T) {
	f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
	f.env["PATH"] += string(os.PathListSeparator) + waitingGitOnlyDir(t)
	f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
	f.env["FAKECLI_WAIT_FILE_TIMEOUT_MS"] = "10000"
	headInit(t, f.env, f.root)
	file := filepath.Join(f.root, "changed.txt")
	reopenDispatchWrite(t, file, "before send\n")
	gate := filepath.Join(f.root, "send-gate")
	bin := t.TempDir()
	_, err := fakecli.Install(t, bin, "herdr", []fakecli.Rule{
		{Argv: []string{"agent", "list"}, ArgvPrefix: true, Stdout: `{"result":{"agents":[]}}`},
		{Argv: []string{"agent", "get", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`},
		{Argv: []string{"agent", "read", "worker"}, ArgvPrefix: true},
		{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, WaitFile: gate, Stdout: `{"result":{"submitted":true}}`},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.env["PATH"] = bin + string(os.PathListSeparator) + waitingGitOnlyDir(t)
	f.env["SOHO_FAKECLI_CONFIG"] = bin
	done, stop := make(chan error, 1), make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				done <- fmt.Errorf("stopped before prompt")
				return
			case <-time.After(5 * time.Millisecond):
			}
			calls, err := fakecli.ReadCallsForConfig(filepath.Join(bin, "herdr.json"))
			if err != nil {
				continue
			}
			for _, call := range calls {
				if len(call.Argv) >= 2 && call.Argv[0] == "agent" && call.Argv[1] == "prompt" {
					if err := os.WriteFile(file, []byte("worker edit\n"), 0600); err != nil {
						done <- err
						return
					}
					done <- os.WriteFile(gate, []byte("ready"), 0600)
					return
				}
			}
		}
	}()
	var once sync.Once
	var producerErr error
	join := func() { once.Do(func() { close(stop); producerErr = <-done }) }
	defer join()
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	join()
	if producerErr != nil {
		t.Fatal(producerErr)
	}
	if code != 0 {
		t.Fatalf("dispatch=%d %s %s", code, out, errText)
	}
	composed, _ := dispatchOutputField(t, out, "composed_prompt")
	side := headSidecar(t, dispatch.DispatchSidecar(composed))
	snapshot, ok := side["git_snapshot"].(map[string]any)
	want := fmt.Sprintf("file:%x", sha256.Sum256([]byte("before send\n")))
	if !ok || snapshot["changed.txt"] != want {
		t.Fatalf("snapshot=%v want=%s", side["git_snapshot"], want)
	}
}

func TestDispatchSnapshotSurvivesFailedSend(t *testing.T) {
	f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0", fakecli.Rule{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Code: 7, Stderr: "refused"})
	f.env["PATH"] += string(os.PathListSeparator) + waitingGitOnlyDir(t)
	f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
	headInit(t, f.env, f.root)
	reopenDispatchWrite(t, filepath.Join(f.root, "changed.txt"), "before send\n")
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	if code != 4 {
		t.Fatalf("send=%d %s %s", code, out, errText)
	}
	composed, _ := dispatchOutputField(t, out, "composed_prompt")
	side := headSidecar(t, dispatch.DispatchSidecar(composed))
	snapshot, ok := side["git_snapshot"].(map[string]any)
	if side["submission"] != "failed" || !ok || snapshot["changed.txt"] == nil {
		t.Fatalf("failed sidecar=%v", side)
	}
}

func TestDispatchAmendRetainsContentSnapshot(t *testing.T) {
	f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
	f.env["PATH"] += string(os.PathListSeparator) + waitingGitOnlyDir(t)
	headInit(t, f.env, f.root)
	reopenDispatchWrite(t, filepath.Join(f.root, "changed.txt"), "before send\n")
	composed, _ := firstDispatch(t, f)
	first := headSidecar(t, dispatch.DispatchSidecar(composed))
	reopenDispatchWrite(t, filepath.Join(f.root, "changed.txt"), "after send\n")
	reopenDispatchWrite(t, f.brief, "Amend the task.\n")
	code, out, errText := f.run(t, "worker", f.brief, "--amend", "--no-wait")
	if code != 0 {
		t.Fatalf("amend=%d %s %s", code, out, errText)
	}
	next, _ := dispatchOutputField(t, out, "composed_prompt")
	amended := headSidecar(t, dispatch.DispatchSidecar(next))
	if first["git_snapshot"] == nil || !reflect.DeepEqual(first["git_snapshot"], amended["git_snapshot"]) {
		t.Fatalf("first=%v amended=%v", first["git_snapshot"], amended["git_snapshot"])
	}
}
