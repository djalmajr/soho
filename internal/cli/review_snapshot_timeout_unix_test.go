//go:build !windows

package cli

import (
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func init() { review545TimeoutHelper() }

// Runs before all normal TestMain routing, only in the dedicated Git fixture.
func review545TimeoutHelper() {
	if os.Getenv("REVIEW545_TIMEOUT_HELPER") != "1" {
		return
	}
	if strings.Join(os.Args[1:], "|") == "worktree|list|--porcelain|-z" {
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGTERM)
		<-signals
		if e := os.WriteFile(os.Getenv("REVIEW545_TIMEOUT_MARKER"), []byte("SIGTERM received; exiting 0 without a listing"), 0600); e != nil {
			os.Exit(125)
		}
		os.Exit(0)
	}
	c := exec.Command(os.Getenv("REVIEW545_REAL_GIT"), os.Args[1:]...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Env = os.Environ()
	if e := c.Run(); e != nil {
		if x, ok := e.(*exec.ExitError); ok {
			os.Exit(x.ExitCode())
		}
		os.Exit(125)
	}
	os.Exit(0)
}

func TestReview545TimedOutListingExitZero(t *testing.T) {
	f := newSnapshotFixture(t)
	obj := f.freeze(t, "worker")
	// The path is absent, but the real source repository still registers it.
	if e := os.Rename(obj["snapshot"], obj["snapshot"]+"-held"); e != nil {
		t.Fatal(e)
	}
	listing := fixtureGit(t, f.mutationCopyFixture, f.source, "worktree", "list", "--porcelain", "-z")
	if !strings.Contains(listing, "worktree "+obj["snapshot"]+"\x00") {
		t.Fatal("fixture is not registered")
	}
	original := map[string]string{}
	for _, p := range []string{obj["patch"], obj["patch"] + ".sha256", filepath.Join(f.sd, "snapshots.tsv")} {
		b, e := os.ReadFile(p)
		if e != nil {
			t.Fatal(e)
		}
		original[p] = string(b)
	}
	git, e := exec.LookPath("git")
	if e != nil {
		t.Fatal(e)
	}
	bin := filepath.Join(f.root, "timeout-zero-git")
	if e := os.Mkdir(bin, 0700); e != nil {
		t.Fatal(e)
	}
	self, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(self, filepath.Join(bin, "git")); e != nil {
		t.Fatal(e)
	}
	marker := filepath.Join(f.root, "received-term.txt")
	f.env["PATH"] = bin + string(os.PathListSeparator) + f.env["PATH"]
	f.env["REVIEW545_TIMEOUT_HELPER"] = "1"
	f.env["REVIEW545_REAL_GIT"] = git
	f.env["REVIEW545_TIMEOUT_MARKER"] = marker
	f.env["GORACE"] = "atexit_sleep_ms=0"
	if e := os.WriteFile(filepath.Join(f.sd, "agents.tsv"), []byte("# no owners\n"), 0600); e != nil {
		t.Fatal(e)
	}
	code, out, msg := f.run(t, "gc", "--yes")
	rows, e := readReviewSnapshots(f.sd)
	m, me := os.ReadFile(marker)
	t.Logf("timed-out listing public gc exit=%d rows=%d marker=%q marker_error=%v stdout=%s stderr=%s", code, len(rows), m, me, out, msg)
	if me != nil || string(m) != "SIGTERM received; exiting 0 without a listing" {
		t.Fatal("timeout with successful termination was not exercised")
	}
	if code != 0 || e != nil || len(rows) != 1 || !strings.Contains(msg, "kept snapshot") {
		t.Error("timed-out Git listing authorized cleanup of a still-registered snapshot")
	}
	for p, want := range original {
		b, e := os.ReadFile(p)
		t.Logf("retained path=%s error=%v", p, e)
		if e != nil || string(b) != want {
			t.Errorf("timeout deleted artifact or registry row: %s", p)
		}
	}
}
