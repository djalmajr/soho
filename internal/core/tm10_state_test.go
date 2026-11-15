package core

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
)

func TestTM10StateRemaining(t *testing.T) {
	t.Run(`JS: "roster lock: a fresh lock held beyond 200 tries exits 4 with the message"`, func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, "agents.lock"), 0o700); err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		var recovered any
		func() {
			defer func() { recovered = recover() }()
			WithRosterLock(dir, func() { t.Fatal("held lock ran callback") })
		}()
		exitErr, ok := recovered.(*platform.ExitError)
		if !ok || exitErr.Code != 4 || !strings.Contains(exitErr.Msg, "held for too long") || time.Since(started) < 9*time.Second {
			t.Fatalf("recovered=%#v elapsed=%s", recovered, time.Since(started))
		}
	})
	t.Run(`JS: "roster lock: a stale lock that cannot be removed counts as a try and exits 4"`, func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("POSIX directory write permissions do not model Windows ACLs")
		}
		if os.Geteuid() == 0 {
			t.Skip("root bypasses directory write permission bits, so removal failure cannot be exercised")
		}
		dir := t.TempDir()
		lock := filepath.Join(dir, "agents.lock")
		if err := os.Mkdir(lock, 0o700); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-2 * time.Minute)
		if err := os.Chtimes(lock, old, old); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(dir, 0o700)
		var recovered any
		func() {
			defer func() { recovered = recover() }()
			WithRosterLock(dir, func() { t.Fatal("callback ran") })
		}()
		exitErr, ok := recovered.(*platform.ExitError)
		if !ok || exitErr.Code != 4 || !strings.Contains(exitErr.Msg, "held for too long") {
			t.Fatalf("recovered=%#v", recovered)
		}
		if _, err := os.Stat(lock); err != nil {
			t.Fatalf("stale lock removed despite failed removal: %v", err)
		}
	})
	t.Run(`JS: "atomicWrite (platform): 0600 for a new file, existing mode kept, no temp"`, func(t *testing.T) {
		dir := t.TempDir()
		file := filepath.Join(dir, "new")
		if err := platform.AtomicWrite(file, "first"); err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" {
			info, err := os.Stat(file)
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("new mode=%v err=%v", info, err)
			}
		}
		if err := os.Chmod(file, 0o640); err != nil && runtime.GOOS != "windows" {
			t.Fatal(err)
		}
		if err := platform.AtomicWrite(file, "second"); err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" {
			info, _ := os.Stat(file)
			if info.Mode().Perm() != 0o640 {
				t.Fatalf("rewritten mode=%o", info.Mode().Perm())
			}
		}
		if data, err := os.ReadFile(file); err != nil || string(data) != "second" {
			t.Fatalf("data=%q err=%v", data, err)
		}
		entries, _ := os.ReadDir(dir)
		if len(entries) != 1 {
			t.Fatalf("temporary files remain: %#v", entries)
		}
	})
	t.Run(`JS: "nowStamp/nowIso: the exact bash date formats"`, func(t *testing.T) {
		instant := time.Date(2026, 9, 28, 1, 2, 3, 0, time.UTC)
		if got := NowStamp(instant); got != "20260928T010203" {
			t.Fatalf("NowStamp=%q", got)
		}
		if got := NowISO(instant); got != "2026-09-28T01:02:03" {
			t.Fatalf("NowISO=%q", got)
		}
	})
	t.Run(`JS: "state root preserves the main checkout and non-git path behavior"`, func(t *testing.T) {
		gitEnv := platform.Env{"PATH": testutil.HostPath()}
		nonGit := t.TempDir()
		if got := platform.StateProjectRoot(gitEnv, nonGit); got != nonGit {
			t.Fatalf("non-git root=%q", got)
		}
		root := t.TempDir()
		gitRun(t, root, "init", "-q")
		gitRun(t, root, "config", "user.email", "test@example.invalid")
		gitRun(t, root, "config", "user.name", "Test")
		if err := os.WriteFile(filepath.Join(root, "tracked"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		gitRun(t, root, "add", "tracked")
		gitRun(t, root, "commit", "-qm", "initial")
		realRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			t.Fatal(err)
		}
		if got := platform.StateProjectRoot(gitEnv, root); got != realRoot {
			t.Fatalf("main checkout root=%q want=%q", got, realRoot)
		}
	})
	t.Run(`JS: "a linked worktree of a bare repository keeps state in that worktree"`, func(t *testing.T) {
		source := t.TempDir()
		gitRun(t, source, "init", "-q")
		gitRun(t, source, "config", "user.email", "test@example.invalid")
		gitRun(t, source, "config", "user.name", "Test")
		if err := os.WriteFile(filepath.Join(source, "tracked"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		gitRun(t, source, "add", "tracked")
		gitRun(t, source, "commit", "-qm", "initial")
		bare, linked := filepath.Join(t.TempDir(), "bare.git"), filepath.Join(t.TempDir(), "linked")
		gitRun(t, t.TempDir(), "clone", "--bare", source, bare)
		gitRun(t, bare, "worktree", "add", "--detach", linked, "HEAD")
		realLinked, err := filepath.EvalSymlinks(linked)
		if err != nil {
			t.Fatal(err)
		}
		if got := platform.StateProjectRoot(platform.Env{"PATH": testutil.HostPath()}, linked); got != realLinked {
			t.Fatalf("bare worktree root=%q want=%q", got, realLinked)
		}
	})
	t.Run(`JS: "linked and ordinary submodule checkouts use the submodule checkout as state root"`, func(t *testing.T) {
		subSource := t.TempDir()
		gitRun(t, subSource, "init", "-q")
		gitRun(t, subSource, "config", "user.email", "test@example.invalid")
		gitRun(t, subSource, "config", "user.name", "Test")
		if err := os.WriteFile(filepath.Join(subSource, "tracked"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		gitRun(t, subSource, "add", "tracked")
		gitRun(t, subSource, "commit", "-qm", "submodule")
		super := t.TempDir()
		gitRun(t, super, "init", "-q")
		gitRun(t, super, "config", "user.email", "test@example.invalid")
		gitRun(t, super, "config", "user.name", "Test")
		gitRun(t, super, "-c", "protocol.file.allow=always", "submodule", "add", subSource, "nested/sub")
		gitRun(t, super, "commit", "-qam", "submodule")
		sub := filepath.Join(super, "nested", "sub")
		env := platform.Env{"PATH": testutil.HostPath()}
		realSub, err := filepath.EvalSymlinks(sub)
		if err != nil {
			t.Fatal(err)
		}
		if got := platform.StateProjectRoot(env, sub); got != realSub {
			t.Fatalf("submodule root=%q want=%q", got, realSub)
		}
		linked := filepath.Join(t.TempDir(), "sub-linked")
		gitRun(t, sub, "worktree", "add", "-qb", "sub-linked", linked)
		if got := platform.StateProjectRoot(env, linked); got != realSub {
			t.Fatalf("linked submodule root=%q want=%q", got, realSub)
		}
	})
	t.Run(`JS: "absolute core.worktree under superproject .git falls back to projectRoot"`, func(t *testing.T) {
		root := t.TempDir()
		super, source := filepath.Join(root, "repo"), filepath.Join(root, "source")
		if err := os.Mkdir(super, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(source, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, repo := range []string{super, source} {
			gitRun(t, repo, "init", "-q")
			gitRun(t, repo, "config", "user.email", "test@example.invalid")
			gitRun(t, repo, "config", "user.name", "Test")
			if err := os.WriteFile(filepath.Join(repo, "tracked"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			gitRun(t, repo, "add", "tracked")
			gitRun(t, repo, "commit", "-qm", "initial")
		}
		gitRun(t, super, "-c", "protocol.file.allow=always", "submodule", "add", source, "submodule")
		gitRun(t, super, "add", ".gitmodules", "submodule")
		gitRun(t, super, "commit", "-qm", "submodule")
		sub := filepath.Join(super, "submodule")
		worker := filepath.Join(root, "sub-worktree")
		gitRun(t, sub, "worktree", "add", "-qb", "linked", worker, "HEAD")
		common := gitRun(t, worker, "rev-parse", "--git-common-dir")
		if !filepath.IsAbs(common) {
			common = filepath.Join(worker, common)
		}
		gitRun(t, worker, "--git-dir", common, "config", "core.worktree", filepath.Join(super, ".git"))
		realWorker, err := filepath.EvalSymlinks(worker)
		if err != nil {
			t.Fatal(err)
		}
		if got := platform.StateProjectRoot(platform.Env{"PATH": testutil.HostPath()}, worker); got != realWorker {
			t.Fatalf("absolute metadata state root=%q want project root=%q", got, realWorker)
		}
	})
}

func gitRun(t *testing.T, cwd string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %s: %v", strings.Join(args, " "), output, err)
	}
	return strings.TrimSpace(string(output))
}
