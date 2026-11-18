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
	"github.com/djalmajr/soho/internal/gittree"
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
	snapshot := headSidecar(t, dispatch.DispatchSidecar(composed)+".snapshot")
	if snapshot["README.md"] == nil {
		t.Fatalf("snapshot=%v", snapshot)
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
	snapshot := headSidecar(t, dispatch.DispatchSidecar(composed)+".snapshot")
	want := fmt.Sprintf("file:false:%x", sha256.Sum256([]byte("before send\n")))
	if snapshot["changed.txt"] != want {
		t.Fatalf("snapshot=%v want=%s", snapshot, want)
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
	snapshot := headSidecar(t, dispatch.DispatchSidecar(composed)+".snapshot")
	if side["submission"] != "failed" || snapshot["changed.txt"] == nil {
		t.Fatalf("failed sidecar=%v", side)
	}
}

func TestDispatchAmendRetainsContentSnapshot(t *testing.T) {
	f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
	f.env["PATH"] += string(os.PathListSeparator) + waitingGitOnlyDir(t)
	headInit(t, f.env, f.root)
	reopenDispatchWrite(t, filepath.Join(f.root, "changed.txt"), "before send\n")
	composed, _ := firstDispatch(t, f)
	first := headSidecar(t, dispatch.DispatchSidecar(composed)+".snapshot")
	reopenDispatchWrite(t, filepath.Join(f.root, "changed.txt"), "after send\n")
	reopenDispatchWrite(t, f.brief, "Amend the task.\n")
	code, out, errText := f.run(t, "worker", f.brief, "--amend", "--no-wait")
	if code != 0 {
		t.Fatalf("amend=%d %s %s", code, out, errText)
	}
	next, _ := dispatchOutputField(t, out, "composed_prompt")
	amended := headSidecar(t, dispatch.DispatchSidecar(next)+".snapshot")
	if !reflect.DeepEqual(first, amended) {
		t.Fatalf("first=%v amended=%v", first, amended)
	}
}

func TestDispatchInheritsLatestAcceptedAmendmentScope(t *testing.T) {
	f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
	f.env["PATH"] += string(os.PathListSeparator) + waitingGitOnlyDir(t)
	headInit(t, f.env, f.root)
	reopenDispatchWrite(t, f.brief, "# Goal\nFirst.\n# Owned files\n- src/\n")
	firstDispatch(t, f)
	amend := filepath.Join(f.root, "amend.md")
	for _, body := range []string{"# Owned files\n- src/a.go\n", "# Goal\nNo scope change.\n"} {
		reopenDispatchWrite(t, amend, body)
		code, out, stderr := f.run(t, "worker", amend, "--amend", "--no-wait")
		if code != 0 {
			t.Fatalf("amend=%d %s %s", code, out, stderr)
		}
	}
	reopenDispatchWrite(t, f.brief, "# Goal\nNext.\n# Owned files\n- inherit: previous brief\n")
	code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
	if code != 0 {
		t.Fatalf("dispatch=%d %s %s", code, out, stderr)
	}
	composed, _ := dispatchOutputField(t, out, "composed_prompt")
	side := headSidecar(t, dispatch.DispatchSidecar(composed))
	if !reflect.DeepEqual(side["git_previous_owned"], []any{"src/a.go"}) {
		t.Fatalf("previous scope=%v", side["git_previous_owned"])
	}
}

func TestDispatchOverlapResolvesInheritedScope(t *testing.T) {
	for _, inheritor := range []string{"mine", "peer"} {
		t.Run(inheritor, func(t *testing.T) {
			mine, peer := "- src/a.go", "- src/a.go"
			if inheritor == "mine" {
				mine = "- inherit: previous brief"
			} else {
				peer = "- inherit: previous brief"
			}
			h := newTM3bHarness(t, "# Brief\n# Owned files\n"+mine+"\n# Report\nDone.\n")
			for _, dir := range []string{filepath.Join(h.ws, "briefs"), filepath.Join(h.ws, "reports")} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			write := func(path, body string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			report := filepath.Join(h.ws, "reports", "peer.md")
			write(filepath.Join(h.ws, "last-report-peer"), report+"\n")
			write(filepath.Join(h.ws, "agents.tsv"), "build\tw0test:p0a\tcodex\timplementer\topenai\t0\t"+h.root+"\tnow\t gpt-5\ttask\timplementer\npeer\tw0test:p0b\tcodex\timplementer\topenai\t0\t"+h.root+"\tnow\n")
			write(filepath.Join(h.ws, "briefs", "peer.md"), "# Brief\n# Owned files\n"+peer+"\n# Report contract\n")
			if inheritor == "mine" {
				prior := filepath.Join(h.ws, "reports", "prior.md")
				write(filepath.Join(h.ws, "task-report-build.json"), fmt.Sprintf(`{"version":1,"task_report":%q,"current":%q,"history":[]}`, prior, prior))
				write(filepath.Join(h.ws, "briefs", "prior.dispatch.json"), fmt.Sprintf(`{"git_tree":%q}`, h.root))
				write(filepath.Join(h.ws, "briefs", "prior.md"), "# Brief\n# Owned files\n- src/a.go\n# Report contract\n")
			} else {
				write(filepath.Join(h.ws, "briefs", "peer.dispatch.json"), fmt.Sprintf(`{"git_tree":%q,"git_previous_owned":["src/a.go"]}`, h.root))
			}
			_, _, stderr := h.run(t, "build", h.brief, "--no-wait")
			if !strings.Contains(stderr, "owns files that 'peer' is still editing: src/a.go") {
				t.Fatalf("warning=%q", stderr)
			}
		})
	}
}

func TestDispatchBusyRetryRefreshesBaseline(t *testing.T) {
	gate := filepath.Join(t.TempDir(), "gate")
	f := newDispatchHcFixture(t, append(guardProbeRules(), []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(8, "idle")},
		{Argv: []string{"send", "--if", "idle", "--json", "--", "worker"}, ArgvPrefix: true, Call: 1, WaitFile: gate, Code: 10, Stderr: `{"error":{"kind":"status_mismatch","message":"busy"}}`},
		{Argv: []string{"send", "--if", "idle", "--json", "--", "worker"}, ArgvPrefix: true, Call: 2, Stdout: `{"turn":8,"started":true}`},
		{Argv: []string{"read", "worker"}, ArgvPrefix: true},
	}...))
	f.env["PATH"] += string(os.PathListSeparator) + waitingGitOnlyDir(t)
	f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
	f.env["FAKECLI_WAIT_FILE_TIMEOUT_MS"] = "10000"
	headInit(t, f.env, f.root)
	file := filepath.Join(f.root, "changed.txt")
	reopenDispatchWrite(t, file, "before\n")
	done, stop := make(chan error, 1), make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				done <- fmt.Errorf("stopped before send")
				return
			case <-time.After(5 * time.Millisecond):
			}
			calls, err := fakecli.ReadCallsForConfig(filepath.Join(f.bin, "hc.json"))
			if err != nil {
				continue
			}
			for _, call := range calls {
				if len(call.Argv) > 0 && call.Argv[0] == "send" && len(call.Argv) > 1 && call.Argv[1] == "--if" {
					if err := os.WriteFile(file, []byte("during wait\n"), 0600); err != nil {
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
	var err error
	join := func() { once.Do(func() { close(stop); err = <-done }) }
	defer join()
	code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
	join()
	if err != nil || code != 0 {
		t.Fatalf("dispatch=%d out=%s stderr=%s producer=%v", code, out, stderr, err)
	}
	if len(hcSends(f.hcCalls(t))) != 2 {
		t.Fatal("busy send was not retried")
	}
	composed, _ := dispatchOutputField(t, out, "composed_prompt")
	snapshot := headSidecar(t, dispatch.DispatchSidecar(composed)+".snapshot")
	want := fmt.Sprintf("file:false:%x", sha256.Sum256([]byte("during wait\n")))
	if snapshot["changed.txt"] != want {
		t.Fatalf("snapshot=%v want=%s", snapshot, want)
	}
}

func TestDispatchSnapshotSidecarCapAndCleanup(t *testing.T) {
	originalCap := gittree.SnapshotMaxBytes
	t.Cleanup(func() { gittree.SnapshotMaxBytes = originalCap })
	for _, limited := range []bool{false, true} {
		name := "normal"
		if limited {
			name = "over-cap"
		}
		t.Run(name, func(t *testing.T) {
			gittree.SnapshotMaxBytes = originalCap
			if limited {
				gittree.SnapshotMaxBytes = 1
			}
			f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
			f.env["PATH"] += string(os.PathListSeparator) + waitingGitOnlyDir(t)
			f.env["SOHO_SETTLED_GRACE"], f.env["SOHO_WAIT_POLL_MS"], f.env["SOHO_METRICS"] = "0", "1", "off"
			headInit(t, f.env, f.root)
			outside := filepath.Join(f.root, "outside.txt")
			owned := filepath.Join(f.root, "owned.txt")
			reopenDispatchWrite(t, outside, "committed\n")
			reopenDispatchWrite(t, owned, "committed\n")
			headGit(t, f.env, f.root, "add", "outside.txt", "owned.txt")
			headGit(t, f.env, f.root, "commit", "-m", "fixture")
			reopenDispatchWrite(t, outside, "earlier dirty contents\n")
			reopenDispatchWrite(t, f.brief, "# Goal\nWork.\n# Owned files\n- owned.txt\n")
			f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
			code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
			if code != 0 {
				t.Fatalf("dispatch=%d out=%s stderr=%s", code, out, stderr)
			}
			capLine := "Git snapshot exceeds the size cap; using mtime for the task audit"
			if strings.Count(stderr, capLine) != map[bool]int{false: 0, true: 1}[limited] {
				t.Fatalf("cap notice=%q", stderr)
			}
			composed, _ := dispatchOutputField(t, out, "composed_prompt")
			report, _ := dispatchOutputField(t, out, "report")
			sidecar := dispatch.DispatchSidecar(composed)
			side := headSidecar(t, sidecar)
			info, err := os.Stat(sidecar)
			if err != nil || info.Size() >= 1<<20 {
				t.Fatalf("sidecar size or stat=%v err=%v", info, err)
			}
			if _, ok := side["git_snapshot"]; ok {
				t.Fatalf("snapshot inline: %v", side)
			}
			if limited {
				if _, err := os.Stat(sidecar + ".snapshot"); !os.IsNotExist(err) {
					t.Fatalf("over cap snapshot err=%v", err)
				}
			} else {
				if side["git_snapshot_file"] != true || headSidecar(t, sidecar+".snapshot")["outside.txt"] == nil {
					t.Fatalf("external snapshot absent: %v", side)
				}
			}
			reopenDispatchWrite(t, outside, "worker changed outside to a different length\n")
			old := time.Unix(946684800, 0)
			if err := os.Chtimes(outside, old, old); err != nil {
				t.Fatal(err)
			}
			reopenDispatchWrite(t, owned, "worker edit\n")
			reopenDispatchWrite(t, report, "## Files changed\n- `owned.txt`\n")
			code, out, stderr = reopenDispatchRun(t, f.env, "wait", "worker", "--timeout", "2s")
			if code != 0 {
				t.Fatalf("wait=%d %s %s", code, out, stderr)
			}
			if got := strings.Contains(out, "outside_owned"); got == limited {
				t.Fatalf("fallback=%t out=%s stderr=%s", limited, out, stderr)
			}
			if !limited {
				aged := time.Now().Add(-30 * 24 * time.Hour)
				if err := os.Chtimes(sidecar+".snapshot", aged, aged); err != nil {
					t.Fatal(err)
				}
				code, out, stderr = reopenDispatchRun(t, f.env, "clean", "--older-than", "7")
				if code != 0 {
					t.Fatalf("clean retained=%d %s %s", code, out, stderr)
				}
				if _, err := os.Stat(sidecar + ".snapshot"); err != nil {
					t.Fatalf("live sidecar lost snapshot: %v", err)
				}
				if err := os.Chtimes(sidecar, aged, aged); err != nil {
					t.Fatal(err)
				}
				code, out, stderr = reopenDispatchRun(t, f.env, "clean", "--older-than", "7")
				if code != 0 {
					t.Fatalf("clean=%d %s %s", code, out, stderr)
				}
				for _, path := range []string{sidecar, sidecar + ".snapshot"} {
					if _, err := os.Stat(path); !os.IsNotExist(err) {
						t.Fatalf("clean left %s: %v", path, err)
					}
				}
			}
		})
	}
}
