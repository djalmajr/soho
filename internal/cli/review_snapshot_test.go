package cli

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

type snapshotFixture struct {
	mutationCopyFixture
	src, sd string
}

func newSnapshotFixture(t *testing.T) snapshotFixture {
	t.Helper()
	f := newMutationCopyFixture(t)
	src := filepath.Join(f.root, "source tree ; literal")
	fixtureGit(t, f, f.source, "worktree", "add", "--detach", src)
	sd := filepath.Join(f.root, "state", "ws")
	if err := os.MkdirAll(sd, 0700); err != nil {
		t.Fatal(err)
	}
	f.env["SOHO_DIR"] = filepath.Dir(sd)
	f.env["HERDR_WORKSPACE_ID"] = "ws"
	f.env["HERDR_ENV"] = "1"
	delete(f.env, "SOHO_REGISTRY_DIR")
	f.env["SOHO_REGRID"] = "off"
	f.env["SOHO_PRESSURE_DISK_FREE_PERCENT"] = "0"
	f.env["SOHO_PRESSURE_SWAP_PERCENT"] = "0"
	f.env["GIT_AUTHOR_DATE"], f.env["GIT_COMMITTER_DATE"] = "2026-10-01T12:00:00Z", "2026-10-01T12:00:00Z"
	roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\nworker\tp-worker\tcodex\timplementer\topenai\t0\t" + src + "\tnow\t\tfull\n"
	if err := os.WriteFile(filepath.Join(sd, "agents.tsv"), []byte(roster), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(f.root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := fakecli.Install(t, bin, "herdr", []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`}, {AnyArgs: true, Stdout: `{"result":{}}`}}); err != nil {
		t.Fatal(err)
	}
	f.env["PATH"] = bin + string(os.PathListSeparator) + f.env["PATH"]
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "TMPDIR", "PATH"} {
		t.Setenv(key, f.env[key])
	}
	return snapshotFixture{f, src, sd}
}
func (f snapshotFixture) run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	return runIn(t, args, f.env, f.source)
}
func (f snapshotFixture) freeze(t *testing.T, owner string) map[string]string {
	t.Helper()
	args := []string{"review-snapshot", f.src, "--name", "Review Slice"}
	if owner != "" {
		args = append(args, "--for", owner)
	}
	code, out, stderr := f.run(t, args...)
	if code != 0 || stderr != "" {
		t.Fatalf("snapshot exit=%d out=%s err=%s", code, out, stderr)
	}
	var obj map[string]string
	if err := json.Unmarshal([]byte(out), &obj); err != nil {
		t.Fatal(err)
	}
	t.Logf("snapshot=%s", out)
	return obj
}
func TestReviewSnapshotFreeze(t *testing.T) {
	f := newSnapshotFixture(t)
	for path, body := range map[string]string{"tracked.txt": "modified\n", "new file.txt": "new\n", "ignored.txt": "ignored\n"} {
		if err := os.WriteFile(filepath.Join(f.src, path), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	f.env["GIT_OPTIONAL_LOCKS"] = "0"
	status := fixtureGit(t, f.mutationCopyFixture, f.src, "status", "--porcelain")
	index := strings.TrimSpace(fixtureGit(t, f.mutationCopyFixture, f.src, "rev-parse", "--path-format=absolute", "--git-path", "index"))
	before, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	obj := f.freeze(t, "worker")
	if obj["owner"] != "worker" || !strings.HasPrefix(filepath.Base(obj["snapshot"]), "snap-review-slice-") {
		t.Fatalf("JSON=%v", obj)
	}
	for name, want := range map[string]string{"tracked.txt": "modified\n", "new file.txt": "new\n"} {
		data, err := os.ReadFile(filepath.Join(obj["snapshot"], name))
		if err != nil || string(data) != want {
			t.Fatalf("snapshot %s=%q %v", name, data, err)
		}
	}
	if _, err := os.Stat(filepath.Join(obj["snapshot"], "ignored.txt")); !os.IsNotExist(err) {
		t.Fatalf("ignored file included: %v", err)
	}
	after, err := os.ReadFile(index)
	if err != nil || string(after) != string(before) {
		t.Fatalf("source index changed: error=%v", err)
	}
	if got := fixtureGit(t, f.mutationCopyFixture, f.src, "status", "--porcelain"); got != status {
		t.Fatalf("source status changed: before=%q after=%q", status, got)
	}
	patch, err := os.ReadFile(obj["patch"])
	if err != nil {
		t.Fatal(err)
	}
	sum := fmt.Sprintf("%x", sha256.Sum256(patch))
	hashFile, err := os.ReadFile(obj["patch"] + ".sha256")
	if err != nil || string(hashFile) != sum+"  "+obj["patch"]+"\n" || obj["patch_sha256"] != sum {
		t.Fatalf("patch hash mismatch: %s %v JSON=%v", hashFile, err, obj)
	}
	baseTree := filepath.Join(f.root, "base-check")
	fixtureGit(t, f.mutationCopyFixture, f.source, "worktree", "add", "--detach", baseTree, obj["base"])
	fixtureGit(t, f.mutationCopyFixture, baseTree, "apply", "--check", obj["patch"])
	t.Log("modified/new included; ignored excluded; source status/index identical; patch applies and SHA-256 matches")
}
func TestReviewSnapshotUnknownOwner(t *testing.T) {
	f := newSnapshotFixture(t)
	code, out, stderr := f.run(t, "review-snapshot", f.src, "--for", "missing")
	if code != 2 || !strings.Contains(stderr, "not in the roster") {
		t.Fatalf("exit=%d out=%s err=%s", code, out, stderr)
	}
	for _, p := range []string{filepath.Join(f.sd, "snapshots"), filepath.Join(f.source, ".worktrees")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("unknown owner created %s: %v", p, err)
		}
	}
	t.Logf("exit=%d stderr=%s", code, stderr)
}
func TestReviewSnapshotReleaseAndGc(t *testing.T) {
	for _, name := range []string{"release", "keep-copies", "orphan", "orchestrator"} {
		t.Run(name, func(t *testing.T) {
			f := newSnapshotFixture(t)
			owner := "worker"
			if name == "orchestrator" {
				owner = ""
			}
			obj := f.freeze(t, owner)
			args := []string{"release", "worker", "--force"}
			if name == "keep-copies" {
				args = append(args, "--keep-copies")
			}
			if name == "orphan" {
				if err := os.WriteFile(filepath.Join(f.sd, "agents.tsv"), []byte("# header\n"), 0600); err != nil {
					t.Fatal(err)
				}
				args = []string{"gc", "--yes"}
			}
			if name == "orchestrator" {
				args = []string{"gc", "--yes"}
			}
			code, out, stderr := f.run(t, args...)
			if code != 0 {
				t.Fatalf("cleanup exit=%d out=%s err=%s", code, out, stderr)
			}
			kept := name == "keep-copies" || name == "orchestrator"
			for _, p := range []string{obj["snapshot"], obj["patch"], obj["patch"] + ".sha256"} {
				_, err := os.Stat(p)
				if kept && err != nil || !kept && !os.IsNotExist(err) {
					t.Fatalf("kept=%v path=%s error=%v out=%s err=%s", kept, p, err, out, stderr)
				}
			}
			registry, err := os.ReadFile(filepath.Join(f.sd, "snapshots.tsv"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(registry), obj["snapshot"]) != kept {
				t.Fatalf("registry=%s kept=%v", registry, kept)
			}
			t.Logf("action=%s kept=%v stdout=%s stderr=%s", name, kept, out, stderr)
		})
	}
}
func TestReviewSnapshotGcKeepsOutsidePrefix(t *testing.T) {
	f := newSnapshotFixture(t)
	head := strings.TrimSpace(fixtureGit(t, f.mutationCopyFixture, f.source, "rev-parse", "HEAD"))
	outside := filepath.Join(f.root, "foreign", ".worktrees", "snap-outside-"+head[:7])
	fixtureGit(t, f.mutationCopyFixture, f.source, "worktree", "add", "--detach", outside, head)
	patch := filepath.Join(f.sd, "snapshots", "outside-"+head[:7]+".patch")
	if err := os.MkdirAll(filepath.Dir(patch), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(patch, []byte("keep patch"), 0600); err != nil {
		t.Fatal(err)
	}
	row := "gone\t" + outside + "\t" + head + "\t" + patch + "\t2026-10-01T12:00:00Z\n"
	if err := os.WriteFile(filepath.Join(f.sd, "snapshots.tsv"), []byte("owner\tworktree\tsha\tpatch\tcreated\n"+row), 0600); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := f.run(t, "gc", "--yes")
	if code != 0 {
		t.Fatalf("gc=%d %s %s", code, out, stderr)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside worktree removed: %v", err)
	}
	if data, err := os.ReadFile(patch); err != nil || string(data) != "keep patch" {
		t.Fatalf("outside patch removed: %q %v", data, err)
	}
	data, err := os.ReadFile(filepath.Join(f.sd, "snapshots.tsv"))
	if err != nil || !strings.Contains(string(data), outside) {
		t.Fatalf("outside registry lost: %s %v", data, err)
	}
	if !strings.Contains(stderr, "kept snapshot") {
		t.Fatalf("missing refusal warning: %s", stderr)
	}
	t.Logf("outside worktree/patch/line preserved: %s %s", out, stderr)
}

func TestReviewSnapshotBaseAndEnvironment(t *testing.T) {
	f := newSnapshotFixture(t)
	base := strings.TrimSpace(fixtureGit(t, f.mutationCopyFixture, f.src, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(f.src, "tracked.txt"), []byte("next commit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, f.mutationCopyFixture, f.src, "add", "tracked.txt")
	fixtureGit(t, f.mutationCopyFixture, f.src, "commit", "-m", "source commit")
	refs := fixtureGit(t, f.mutationCopyFixture, f.src, "show-ref")
	// Inherited redirects must not control any Git invocation.
	for _, key := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR"} {
		f.env[key] = filepath.Join(f.root, "bogus")
	}
	code, out, stderr := f.run(t, "review-snapshot", f.src, "--base", base)
	if code != 0 {
		t.Fatalf("exit=%d out=%s err=%s", code, out, stderr)
	}
	var obj map[string]string
	if err := json.Unmarshal([]byte(out), &obj); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR"} {
		delete(f.env, key)
	}
	if obj["base"] != base || obj["owner"] != "" {
		t.Fatalf("JSON=%v", obj)
	}
	if got := strings.TrimSpace(fixtureGit(t, f.mutationCopyFixture, f.src, "rev-parse", obj["sha"]+"^")); got != base {
		t.Fatalf("parent=%s base=%s", got, base)
	}
	if got := fixtureGit(t, f.mutationCopyFixture, f.src, "show-ref"); got != refs {
		t.Fatalf("refs changed: %s / %s", refs, got)
	}
	if got := strings.TrimSpace(fixtureGit(t, f.mutationCopyFixture, obj["snapshot"], "rev-parse", "--abbrev-ref", "HEAD")); got != "HEAD" {
		t.Fatalf("snapshot not detached: %s", got)
	}
	code, _, stderr = f.run(t, "review-snapshot", f.src, "--base", base)
	if code != 2 || !strings.Contains(stderr, "already exists") {
		t.Fatalf("existing path exit=%d err=%s", code, stderr)
	}
	t.Log("explicit base is parent; detached snapshot; refs unchanged; inherited Git redirects ignored; existing path refused with exit 2")
}
func TestReviewSnapshotRejectsInputs(t *testing.T) {
	f := newSnapshotFixture(t)
	for _, args := range [][]string{{}, {f.src, "--bad", "x"}, {f.src, "--name"}, {f.src, "--name", "..."}, {f.src, "--for", "worker\tinjected"}, {f.src, "--name", "line\nname"}} {
		code, _, stderr := f.run(t, append([]string{"review-snapshot"}, args...)...)
		if code != 2 {
			t.Fatalf("args=%q exit=%d err=%s", args, code, stderr)
		}
	}
	code, _, stderr := f.run(t, "review-snapshot", f.src, "--base", "--help")
	if code != 4 {
		t.Fatalf("invalid Git base exit=%d err=%s", code, stderr)
	}
	t.Log("malformed/hostile inputs exit 2; invalid Git base exits 4")
}
func TestReviewSnapshotGcKeepsInvalidWorktree(t *testing.T) {
	for _, kind := range []string{"ordinary-directory", "other-repository"} {
		t.Run(kind, func(t *testing.T) {
			f := newSnapshotFixture(t)
			obj := f.freeze(t, "worker")
			fixtureGit(t, f.mutationCopyFixture, f.source, "worktree", "remove", "--force", obj["snapshot"])
			if kind == "ordinary-directory" {
				if err := os.Mkdir(obj["snapshot"], 0700); err != nil {
					t.Fatal(err)
				}
			} else {
				other := newMutationCopyFixture(t)
				fixtureGit(t, other, other.source, "worktree", "add", "--detach", obj["snapshot"])
			}
			if err := os.WriteFile(filepath.Join(f.sd, "agents.tsv"), []byte("# header\n"), 0600); err != nil {
				t.Fatal(err)
			}
			code, out, stderr := f.run(t, "gc", "--yes")
			if code != 0 || !strings.Contains(stderr, "kept snapshot") {
				t.Fatalf("exit=%d out=%s err=%s", code, out, stderr)
			}
			for _, path := range []string{obj["snapshot"], obj["patch"], obj["patch"] + ".sha256"} {
				if _, err := os.Stat(path); err != nil {
					t.Fatal(err)
				}
			}
			registry, err := os.ReadFile(filepath.Join(f.sd, "snapshots.tsv"))
			if err != nil || !strings.Contains(string(registry), obj["snapshot"]) {
				t.Fatalf("registry=%s err=%v", registry, err)
			}
			t.Logf("invalid worktree preserved: %s", stderr)
		})
	}
}

// review545WorktreeRegistered reads unquoted NUL-delimited Git porcelain fields.
func review545WorktreeRegistered(listing, path string) bool {
	for _, field := range strings.Split(listing, "\x00") {
		if candidate, ok := strings.CutPrefix(field, "worktree "); ok && snapshotPathsEqual(candidate, path, runtime.GOOS == "windows") {
			return true
		}
	}
	return false
}

func review545SwapHeldParent(t *testing.T, from, to string) {
	t.Helper()
	err := os.Rename(from, to)
	if runtime.GOOS == "windows" {
		// Windows' open os.Root handle prevents the swap itself (ERROR_SHARING_VIOLATION).
		if !errors.Is(err, syscall.Errno(32)) {
			t.Fatalf("held-root rename must be refused with a sharing violation: from=%q to=%q error=%v", from, to, err)
		}
		t.Skipf("held os.Root prevents the parent swap on Windows; refusal verified: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
}
