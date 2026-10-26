package cli

import (
	"bytes"
	"fmt"
	"github.com/djalmajr/soho/internal/dispatch"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type claim338Child struct {
	cmd         *exec.Cmd
	out, stderr bytes.Buffer
	done        chan struct{}
	err         error
}
type claim338Run struct {
	f        *dispatchArrivalFixture
	release  string
	children []*claim338Child
}

func newClaim338Run(t *testing.T, f *dispatchArrivalFixture, release string) *claim338Run {
	t.Helper()
	r := &claim338Run{f: f, release: release}
	t.Cleanup(func() {
		if release != "" {
			_ = os.WriteFile(release, []byte("release"), 0600)
		}
		for _, child := range r.children {
			<-child.done
		}
	})
	return r
}
func (r *claim338Run) start(t *testing.T) *claim338Child {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	c := &claim338Child{cmd: exec.Command(self, "review338-collect"), done: make(chan struct{})}
	c.cmd.Dir = r.f.root
	c.cmd.Env = r.f.env.List()
	c.cmd.Stdout = &c.out
	c.cmd.Stderr = &c.stderr
	if err := c.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	r.children = append(r.children, c)
	started := time.Now()
	id, ie := exec.Command("/bin/ps", "-p", fmt.Sprint(c.cmd.Process.Pid), "-o", "lstart=,comm=").CombinedOutput()
	t.Logf("owned pid=%d started=%s command=%q identity=%s err=%v", c.cmd.Process.Pid, started.Format(time.RFC3339Nano), c.cmd.Args, id, ie)
	go func() { c.err = c.cmd.Wait(); close(c.done) }()
	return c
}
func (c *claim338Child) wait(t *testing.T, limit time.Duration) {
	t.Helper()
	select {
	case <-c.done:
	case <-time.After(limit):
		t.Fatalf("owned collect pid=%d did not finish within %s; cleanup releases barrier and reaps it", c.cmd.Process.Pid, limit)
	}
	t.Logf("reaped pid=%d exit=%d err=%v stdout=%s stderr=%s", c.cmd.Process.Pid, c.cmd.ProcessState.ExitCode(), c.err, c.out.String(), c.stderr.String())
	if c.err != nil {
		t.Fatalf("collect failed: %v", c.err)
	}
}
func claim338CheckGitCalls(t *testing.T, f *dispatchArrivalFixture) []fakecli.Call {
	t.Helper()
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(f.bin, "git.json"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	// Public collect also resolves Git roots before reaching the task check.
	// Count the exact identity/containment commands owned by gittree.Check.
	var checked []fakecli.Call
	for _, call := range calls {
		a := call.Argv
		if len(a) >= 3 && a[0] == "-C" && a[1] == f.root && (a[2] == "symbolic-ref" || a[2] == "for-each-ref" || (a[2] == "rev-parse" && len(a) == 4 && a[3] == "HEAD")) {
			checked = append(checked, call)
		}
	}
	return checked
}

func review338ConcurrentClaim(t *testing.T) {
	f, composed, _, old := review338Fixture(t)
	headGit(t, f.env, f.root, "commit", "--allow-empty", "-m", "worker commit")
	next := headGit(t, f.env, f.root, "rev-parse", "HEAD")
	release := filepath.Join(f.root, "release-git")
	if _, err := fakecli.Install(t, f.bin, "git", []fakecli.Rule{
		{Argv: []string{"-C", f.root, "rev-parse", "HEAD"}, Stdout: next + "\n"},
		{Argv: []string{"-C", f.root, "symbolic-ref", "-q", "--short", "HEAD"}, Stdout: "main\n"},
		{Argv: []string{"-C", f.root, "for-each-ref", "--contains", next, "refs/remotes", "--count=1", "--format=x"}, WaitFile: release},
		{AnyArgs: true, Code: 1},
	}); err != nil {
		t.Fatal(err)
	}
	r := newClaim338Run(t, f, release)
	first := r.start(t)
	deadline := time.Now().Add(3 * time.Second)
	for {
		ready := false
		for _, call := range claim338CheckGitCalls(t, f) {
			if len(call.Argv) > 2 && call.Argv[2] == "for-each-ref" {
				ready = true
			}
		}
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first collect did not reach containment while holding its claim")
		}
		time.Sleep(10 * time.Millisecond)
	}
	path := dispatch.DispatchSidecar(composed)
	before := mustRead(t, path)
	second := r.start(t)
	second.wait(t, 3500*time.Millisecond)
	if second.stderr.Len() != 0 || mustRead(t, path) != before {
		t.Fatalf("contender changed marker or warned: stderr=%s", second.stderr.String())
	}
	if calls := claim338CheckGitCalls(t, f); len(calls) != 3 {
		t.Fatalf("contender queried git under held claim: %#v", calls)
	}
	reopenDispatchWrite(t, release, "go")
	first.wait(t, 3*time.Second)
	count := strings.Count(first.stderr.String()+second.stderr.String(), "left a local commit")
	checked := headSidecar(t, path)["git_checked"]
	t.Logf("old=%s new=%s warnings=%d checked=%v", old, next, count, checked)
	if count != 1 || checked != true {
		t.Fatalf("warnings=%d checked=%v; want exactly one and true", count, checked)
	}
	if _, err := os.Stat(path + ".git-checked"); err != nil {
		t.Fatalf("permanent marker missing after completion: %v", err)
	}
}

func TestReview338HeldClaim(t *testing.T) {
	f, composed, _, _ := review338Fixture(t)
	path := dispatch.DispatchSidecar(composed)
	if _, err := fakecli.Install(t, f.bin, "git", []fakecli.Rule{{AnyArgs: true}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".git-checked", []byte("already claimed"), 0600); err != nil {
		t.Fatal(err)
	}
	before := mustRead(t, path)
	r := newClaim338Run(t, f, "")
	child := r.start(t)
	child.wait(t, 3500*time.Millisecond)
	if child.stderr.Len() != 0 || mustRead(t, path) != before || len(claim338CheckGitCalls(t, f)) != 0 {
		t.Fatalf("held claim was not silent: stderr=%s", child.stderr.String())
	}
}

func TestAudit338LegacyCollectDoesNotTouchCwdLock(t *testing.T) {
	f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
	report := filepath.Join(f.state, "ws", "reports", "legacy.md")
	reopenDispatchWrite(t, report, "Completed legacy task.\n")
	reopenDispatchWrite(t, filepath.Join(f.state, "ws", "last-report-worker"), report+"\n")
	for _, name := range []string{".git-check.lock", ".git-checked"} {
		path := filepath.Join(f.root, name)
		reopenDispatchWrite(t, path, "unrelated fixture sentinel\n")
		old := time.Now().Add(-2 * time.Minute)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	code, out, msg := reopenDispatchRun(t, f.env, "collect", "worker")
	if code != 0 || !strings.Contains(out, "Completed legacy task.") {
		t.Fatalf("legacy collect code=%d out=%s stderr=%s", code, out, msg)
	}
	for _, name := range []string{".git-check.lock", ".git-checked"} {
		body, err := os.ReadFile(filepath.Join(f.root, name))
		if err != nil || string(body) != "unrelated fixture sentinel\n" {
			t.Fatalf("cwd %s changed: %q %v", name, body, err)
		}
	}
}

func TestAudit338CleanMarkerLifetime(t *testing.T) {
	f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
	dir := filepath.Join(f.state, "ws", "briefs")
	oldSide := filepath.Join(dir, "old.dispatch.json")
	newSide := filepath.Join(dir, "new.dispatch.json")
	for _, path := range []string{oldSide, oldSide + ".git-checked", newSide, newSide + ".git-checked"} {
		reopenDispatchWrite(t, path, "keep\n")
	}
	old := time.Now().Add(-30 * 24 * time.Hour)
	// A new marker follows an old sidecar; an old marker must survive with
	// a new sidecar even when it individually satisfies the age cutoff.
	for _, path := range []string{oldSide, newSide + ".git-checked"} {
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	code, out, msg := reopenDispatchRun(t, f.env, "clean", "--older-than", "7")
	if code != 0 {
		t.Fatalf("clean=%d out=%s stderr=%s", code, out, msg)
	}
	for _, path := range []string{oldSide, oldSide + ".git-checked"} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("old pair survived: %s %v", path, err)
		}
	}
	for _, path := range []string{newSide, newSide + ".git-checked"} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("live pair removed: %s %v", path, err)
		}
	}
}
