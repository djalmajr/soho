package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReview545RollbackAfterPatchFailure(t *testing.T) {
	f := newSnapshotFixture(t)
	obj := f.freeze(t, "worker")
	code, out, msg := f.run(t, "release", "worker", "--force")
	if code != 0 {
		t.Fatalf("setup release=%d %s %s", code, out, msg)
	}
	if _, e := os.Stat(obj["snapshot"]); !os.IsNotExist(e) {
		t.Fatalf("setup did not remove snapshot: %v", e)
	}
	old := snapshotBeforeArtifact
	t.Cleanup(func() { snapshotBeforeArtifact = old })
	reached := false
	snapshotBeforeArtifact = func(_ *os.Root, name string) error {
		if name == filepath.Base(obj["patch"]) {
			reached = true
			if e := os.Mkdir(obj["patch"], 0700); e != nil {
				t.Fatal(e)
			}
			if e := os.WriteFile(filepath.Join(obj["patch"], "blocker"), []byte("preserve existing directory"), 0600); e != nil {
				t.Fatal(e)
			}
		}
		return nil
	}
	code, out, msg = f.run(t, "review-snapshot", f.src, "--name", "Review Slice")
	t.Logf("injected patch-directory conflict: exit=%d stdout=%q stderr=%s", code, out, msg)
	if code != 4 || !reached {
		t.Fatalf("expected patch write failure exit 4, got %d", code)
	}
	_, pathErr := os.Stat(obj["snapshot"])
	rows, e := readReviewSnapshots(f.sd)
	if e != nil {
		t.Fatal(e)
	}
	listing := fixtureGit(t, f.mutationCopyFixture, f.source, "worktree", "list", "--porcelain")
	t.Logf("after failure: snapshot_exists=%t registry_rows=%d git_registered=%t", pathErr == nil, len(rows), strings.Contains(listing, "worktree "+obj["snapshot"]+"\n"))
	if !os.IsNotExist(pathErr) || len(rows) != 0 || strings.Contains(listing, "worktree "+obj["snapshot"]+"\n") {
		t.Fatal("rollback left snapshot directory, registry row or Git registration")
	}
	blocker := filepath.Join(obj["patch"], "blocker")
	if b, e := os.ReadFile(blocker); e != nil || string(b) != "preserve existing directory" {
		t.Fatalf("conflict changed: %q %v", b, e)
	}
	if e := os.Remove(blocker); e != nil {
		t.Fatal(e)
	}
	if e := os.Remove(obj["patch"]); e != nil {
		t.Fatal(e)
	}
	snapshotBeforeArtifact = old
	retry, retryOut, retryErr := f.run(t, "review-snapshot", f.src, "--name", "Review Slice")
	rows, e = readReviewSnapshots(f.sd)
	listing = fixtureGit(t, f.mutationCopyFixture, f.source, "worktree", "list", "--porcelain")
	_, retryPathErr := os.Stat(obj["snapshot"])
	t.Logf("retry exit=%d stdout=%s stderr=%s registry_rows=%d", retry, retryOut, retryErr, len(rows))
	if retry != 0 || e != nil || len(rows) != 1 || retryPathErr != nil || !strings.Contains(listing, "worktree "+obj["snapshot"]+"\n") {
		t.Fatal("retry did not create and register snapshot")
	}
	temps, e := filepath.Glob(filepath.Join(f.env["TMPDIR"], "soho-review-index-*"))
	if e != nil {
		t.Fatal(e)
	}
	t.Logf("remaining temporary-index directories=%v", temps)
	if len(temps) != 0 {
		t.Fatal("temporary index leaked")
	}
}

func TestReview545PatchSymlinkTargetsStayUntouched(t *testing.T) {
	f := newSnapshotFixture(t)
	obj := f.freeze(t, "worker")
	code, out, msg := f.run(t, "release", "worker", "--force")
	if code != 0 {
		t.Fatalf("setup release=%d %s %s", code, out, msg)
	}
	victims := []string{filepath.Join(f.root, "patch-victim"), filepath.Join(f.root, "hash-victim")}
	for i, p := range []string{obj["patch"], obj["patch"] + ".sha256"} {
		if e := os.WriteFile(victims[i], []byte("untouched"), 0600); e != nil {
			t.Fatal(e)
		}
		if e := os.Symlink(victims[i], p); e != nil {
			t.Fatal(e)
		}
	}
	code, out, msg = f.run(t, "review-snapshot", f.src, "--name", "Review Slice")
	if code != 2 || !strings.Contains(msg, "snapshot artifact already exists: "+obj["patch"]) {
		t.Fatalf("snapshot=%d %s %s", code, out, msg)
	}
	for _, p := range victims {
		b, e := os.ReadFile(p)
		if e != nil || string(b) != "untouched" {
			t.Fatalf("symlink target changed: %s %q %v", p, b, e)
		}
	}
	for _, p := range []string{obj["patch"], obj["patch"] + ".sha256"} {
		st, e := os.Lstat(p)
		if e != nil || st.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("artifact link changed: %s %v", p, e)
		}
	}
	assertSnapshotRolledBack(t, f, obj)
	t.Log("existing patch symlinks refused with exit 2; links and targets unchanged")
}

func releasedSnapshot545(t *testing.T) (snapshotFixture, map[string]string) {
	t.Helper()
	f := newSnapshotFixture(t)
	obj := f.freeze(t, "worker")
	if code, out, msg := f.run(t, "release", "worker", "--force"); code != 0 {
		t.Fatalf("release=%d %s %s", code, out, msg)
	}
	return f, obj
}

func assertSnapshotRolledBack(t *testing.T, f snapshotFixture, obj map[string]string) {
	t.Helper()
	rows, err := readReviewSnapshots(f.sd)
	_, pathErr := os.Lstat(obj["snapshot"])
	listing := fixtureGit(t, f.mutationCopyFixture, f.source, "worktree", "list", "--porcelain")
	if err != nil || len(rows) != 0 || !os.IsNotExist(pathErr) || strings.Contains(listing, "worktree "+obj["snapshot"]+"\n") {
		t.Fatalf("rollback: rows=%v err=%v pathErr=%v git=%s", rows, err, pathErr, listing)
	}
}

func TestReview545R3LateArtifact(t *testing.T) {
	for _, phase := range []string{"between-lstat-and-write", "replacement-after-write"} {
		t.Run(phase, func(t *testing.T) {
			f, obj := releasedSnapshot545(t)
			old := snapshotBeforeArtifact
			t.Cleanup(func() { snapshotBeforeArtifact = old })
			cause := errors.New("injected hash publication failure")
			inserted := false
			body := "concurrently created foreign file"
			if phase == "replacement-after-write" {
				body = "foreign replacement"
			}
			snapshotBeforeArtifact = func(_ *os.Root, name string) error {
				if phase == "between-lstat-and-write" && name == filepath.Base(obj["patch"]) {
					file, e := os.OpenFile(obj["patch"], os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
					if e != nil {
						t.Fatal(e)
					}
					if _, e = file.WriteString(body); e != nil {
						t.Fatal(e)
					}
					if e = file.Close(); e != nil {
						t.Fatal(e)
					}
					inserted = true
				}
				if name == filepath.Base(obj["patch"])+".sha256" {
					if phase == "replacement-after-write" {
						peer := filepath.Join(f.root, "peer-artifact")
						if e := os.WriteFile(peer, []byte(body), 0600); e != nil {
							t.Fatal(e)
						}
						if e := os.Rename(peer, obj["patch"]); e != nil {
							t.Fatal(e)
						}
						inserted = true
					}
					return cause
				}
				return nil
			}
			code, out, msg := f.run(t, "review-snapshot", f.src, "--name", "Review Slice")
			b, err := os.ReadFile(obj["patch"])
			t.Logf("phase=%s inserted=%v exit=%d stdout=%s stderr=%s foreign=%q error=%v", phase, inserted, code, out, msg, b, err)
			if code != 4 || !inserted || err != nil || string(b) != body {
				t.Fatal("foreign artifact was changed or removed")
			}
			if phase == "replacement-after-write" && !strings.Contains(msg, cause.Error()) {
				t.Fatal("original error lost")
			}
			if phase == "between-lstat-and-write" && !strings.Contains(msg, "file exists") {
				t.Fatal("exclusive create did not refuse raced-in path")
			}
			assertSnapshotRolledBack(t, f, obj)
		})
	}
}

func TestReview545R3OutsideParentSwap(t *testing.T) {
	f, obj := releasedSnapshot545(t)
	patchDir := filepath.Dir(obj["patch"])
	outside := filepath.Join(f.root, "foreign-directory")
	if e := os.Mkdir(outside, 0700); e != nil {
		t.Fatal(e)
	}
	victim := filepath.Join(outside, filepath.Base(obj["patch"]))
	const body = "foreign data outside snapshot artifact directory"
	if e := os.WriteFile(victim, []byte(body), 0600); e != nil {
		t.Fatal(e)
	}
	old := snapshotBeforeArtifact
	t.Cleanup(func() { snapshotBeforeArtifact = old })
	swapped := false
	snapshotBeforeArtifact = func(_ *os.Root, name string) error {
		if name == filepath.Base(obj["patch"])+".sha256" {
			if e := os.Rename(patchDir, patchDir+"-owned-backup"); e != nil {
				t.Fatal(e)
			}
			if e := os.Symlink(outside, patchDir); e != nil {
				t.Fatal(e)
			}
			swapped = true
			return errors.New("later publication failure after parent swap")
		}
		return nil
	}
	code, out, msg := f.run(t, "review-snapshot", f.src, "--name", "Review Slice")
	b, err := os.ReadFile(victim)
	t.Logf("swapped=%v exit=%d stdout=%s stderr=%s outside=%q error=%v", swapped, code, out, msg, b, err)
	if code != 4 || !swapped || err != nil || string(b) != body {
		t.Fatal("outside-root artifact changed")
	}
	if _, e := os.Lstat(filepath.Join(patchDir+"-owned-backup", filepath.Base(obj["patch"]))); !os.IsNotExist(e) {
		t.Fatalf("owned artifact not rolled back: %v", e)
	}
	assertSnapshotRolledBack(t, f, obj)
}

func TestReview545PreexistingArtifacts(t *testing.T) {
	for _, suffix := range []string{"", ".sha256"} {
		for _, kind := range []string{"regular", "directory", "link", "dangling-link"} {
			t.Run(kind+suffix, func(t *testing.T) {
				f, obj := releasedSnapshot545(t)
				path := obj["patch"] + suffix
				victim := filepath.Join(f.root, "victim")
				switch kind {
				case "regular":
					if e := os.WriteFile(path, []byte("preserve"), 0600); e != nil {
						t.Fatal(e)
					}
				case "directory":
					if e := os.Mkdir(path, 0700); e != nil {
						t.Fatal(e)
					}
					if e := os.WriteFile(filepath.Join(path, "blocker"), []byte("preserve"), 0600); e != nil {
						t.Fatal(e)
					}
				default:
					if kind == "link" {
						if e := os.WriteFile(victim, []byte("preserve"), 0600); e != nil {
							t.Fatal(e)
						}
					}
					if e := os.Symlink(victim, path); e != nil {
						t.Fatal(e)
					}
				}
				before, e := os.Lstat(path)
				if e != nil {
					t.Fatal(e)
				}
				code, out, msg := f.run(t, "review-snapshot", f.src, "--name", "Review Slice")
				after, e := os.Lstat(path)
				t.Logf("kind=%s suffix=%s exit=%d stdout=%s stderr=%s", kind, suffix, code, out, msg)
				if code != 2 || out != "" || !strings.Contains(msg, "snapshot artifact already exists: "+path) || e != nil || !os.SameFile(before, after) {
					t.Fatal("preexisting artifact not refused intact")
				}
				readPath := path
				if kind == "directory" {
					readPath = filepath.Join(path, "blocker")
				}
				if kind != "dangling-link" {
					if b, e := os.ReadFile(readPath); e != nil || string(b) != "preserve" {
						t.Fatalf("bytes changed: %q %v", b, e)
					}
				}
				if kind == "link" || kind == "dangling-link" {
					if link, e := os.Readlink(path); e != nil || link != victim {
						t.Fatalf("link changed: %q %v", link, e)
					}
				}
				assertSnapshotRolledBack(t, f, obj)
				other := obj["patch"]
				if suffix == "" {
					other += ".sha256"
				}
				if _, e := os.Lstat(other); !os.IsNotExist(e) {
					t.Fatalf("preflight created other artifact: %v", e)
				}
			})
		}
	}
}

func TestReview545RollbackRemoveFailure(t *testing.T) {
	f, obj := releasedSnapshot545(t)
	old := snapshotBeforeArtifact
	t.Cleanup(func() { snapshotBeforeArtifact = old })
	cause := errors.New("original hash publication failure")
	locked := false
	snapshotBeforeArtifact = func(_ *os.Root, name string) error {
		if name == filepath.Base(obj["patch"])+".sha256" {
			fixtureGit(t, f.mutationCopyFixture, f.source, "worktree", "lock", "--reason", "concurrent hold", obj["snapshot"])
			locked = true
			return cause
		}
		return nil
	}
	code, out, msg := f.run(t, "review-snapshot", f.src, "--name", "Review Slice")
	t.Logf("locked=%v exit=%d stdout=%s stderr=%s", locked, code, out, msg)
	lines := strings.Split(strings.TrimSpace(msg), "\n")
	if code != 4 || !locked || len(lines) < 2 || !strings.Contains(lines[0], cause.Error()) || !strings.Contains(lines[1], "rollback left worktree "+obj["snapshot"]) {
		t.Fatal("original error or leftover path diagnostic missing")
	}
	listing := fixtureGit(t, f.mutationCopyFixture, f.source, "worktree", "list", "--porcelain")
	if !strings.Contains(listing, "worktree "+obj["snapshot"]+"\n") {
		t.Fatal("locked fixture did not leave registered worktree")
	}
	rows, e := readReviewSnapshots(f.sd)
	if e != nil || len(rows) != 0 {
		t.Fatalf("registry changed: %v %v", rows, e)
	}
	fixtureGit(t, f.mutationCopyFixture, f.source, "worktree", "unlock", obj["snapshot"])
	fixtureGit(t, f.mutationCopyFixture, f.source, "worktree", "remove", "--force", obj["snapshot"])
}
