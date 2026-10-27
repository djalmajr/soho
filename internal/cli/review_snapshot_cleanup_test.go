package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReview545R4CleanupParentSwap(t *testing.T) {
	for _, action := range []string{"release", "gc"} {
		for _, swap := range []bool{false, true} {
			name := action + "-control"
			if swap {
				name = action + "-swap"
			}
			t.Run(name, func(t *testing.T) {
				f := newSnapshotFixture(t)
				obj := f.freeze(t, "worker")
				patchDir := filepath.Dir(obj["patch"])
				outside := filepath.Join(f.root, "foreign-cleanup")
				if e := os.Mkdir(outside, 0700); e != nil {
					t.Fatal(e)
				}
				for _, suffix := range []string{"", ".sha256"} {
					if e := os.WriteFile(filepath.Join(outside, filepath.Base(obj["patch"])+suffix), []byte("foreign bytes"+suffix), 0600); e != nil {
						t.Fatal(e)
					}
				}
				old := snapshotCleanupBeforeRemove
				t.Cleanup(func() { snapshotCleanupBeforeRemove = old })
				reached := false
				snapshotCleanupBeforeRemove = func() {
					reached = true
					if swap {
						review545SwapHeldParent(t, patchDir, patchDir+"-held")
						if e := os.Symlink(outside, patchDir); e != nil {
							t.Fatal(e)
						}
					}
				}
				args := []string{"release", "worker", "--force"}
				if action == "gc" {
					if e := os.WriteFile(filepath.Join(f.sd, "agents.tsv"), []byte("# no owners\n"), 0600); e != nil {
						t.Fatal(e)
					}
					args = []string{"gc", "--yes"}
				}
				original := map[string]string{}
				for _, suffix := range []string{"", ".sha256"} {
					b, e := os.ReadFile(obj["patch"] + suffix)
					if e != nil {
						t.Fatal(e)
					}
					original[suffix] = string(b)
				}
				code, out, msg := f.run(t, args...)
				listing := fixtureGit(t, f.mutationCopyFixture, f.source, "worktree", "list", "--porcelain", "-z")
				rows, e := readReviewSnapshots(f.sd)
				t.Logf("action=%s swap=%v reached=%v exit=%d rows=%d git_registered=%v stdout=%s stderr=%s", action, swap, reached, code, len(rows), review545WorktreeRegistered(listing, obj["snapshot"]), out, msg)
				if code != 0 || !reached || e != nil {
					t.Fatalf("cleanup not reached %v", e)
				}
				if swap {
					if len(rows) != 1 || rows[0][1] != obj["snapshot"] || !strings.Contains(msg, "kept snapshot ") {
						t.Errorf("swapped root must retain registry row and report refusal: rows=%v stderr=%q", rows, msg)
					}
				} else {
					if len(rows) != 0 || !strings.Contains(out, "removed snapshot ") {
						t.Errorf("control must remove row: rows=%v stdout=%q", rows, out)
					}
				}
				for _, suffix := range []string{"", ".sha256"} {
					p := obj["patch"] + suffix
					if swap {
						p = filepath.Join(patchDir+"-held", filepath.Base(obj["patch"])+suffix)
					}
					b, e := os.ReadFile(p)
					if swap {
						if e != nil || string(b) != original[suffix] {
							t.Errorf("swapped root removed original artifact: bytes=%q err=%v", b, e)
						}
					} else if !os.IsNotExist(e) {
						t.Errorf("control left artifact: %s err=%v", p, e)
					}
				}
				for _, suffix := range []string{"", ".sha256"} {
					p := filepath.Join(outside, filepath.Base(obj["patch"])+suffix)
					b, err := os.ReadFile(p)
					t.Logf("foreign_path=%s bytes=%q error=%v", p, b, err)
					if err != nil || string(b) != "foreign bytes"+suffix {
						t.Errorf("cleanup deleted or changed foreign artifact outside original root")
					}
				}
			})
		}
	}
}

func TestReview545R5RetryGcAfterRefusal(t *testing.T) {
	for _, action := range []string{"release", "gc"} {
		t.Run(action, func(t *testing.T) {
			f := newSnapshotFixture(t)
			obj := f.freeze(t, "worker")
			dir := filepath.Dir(obj["patch"])
			outside := filepath.Join(f.root, "retry-foreign")
			if e := os.Mkdir(outside, 0700); e != nil {
				t.Fatal(e)
			}
			for _, suffix := range []string{"", ".sha256"} {
				if e := os.WriteFile(filepath.Join(outside, filepath.Base(obj["patch"])+suffix), []byte("recovery foreign"+suffix), 0600); e != nil {
					t.Fatal(e)
				}
			}
			old := snapshotCleanupBeforeRemove
			t.Cleanup(func() { snapshotCleanupBeforeRemove = old })
			reached := false
			snapshotCleanupBeforeRemove = func() {
				reached = true
				review545SwapHeldParent(t, dir, dir+"-held")
				if e := os.Symlink(outside, dir); e != nil {
					t.Fatal(e)
				}
			}
			args := []string{"release", "worker", "--force"}
			if action == "gc" {
				if e := os.WriteFile(filepath.Join(f.sd, "agents.tsv"), []byte("# no owners\n"), 0600); e != nil {
					t.Fatal(e)
				}
				args = []string{"gc", "--yes"}
			}
			code, out, msg := f.run(t, args...)
			rows, e := readReviewSnapshots(f.sd)
			_, worktreeErr := os.Lstat(obj["snapshot"])
			t.Logf("first action=%s reached=%v exit=%d rows=%d worktree_error=%v stdout=%s stderr=%s", action, reached, code, len(rows), worktreeErr, out, msg)
			if !reached || code != 0 || e != nil || len(rows) != 1 || !strings.Contains(msg, "kept snapshot ") || !os.IsNotExist(worktreeErr) {
				t.Fatal("required refusal state not reached")
			}
			listing := fixtureGit(t, f.mutationCopyFixture, f.source, "worktree", "list", "--porcelain", "-z")
			if review545WorktreeRegistered(listing, obj["snapshot"]) {
				t.Fatal("first cleanup left worktree registered")
			}
			snapshotCleanupBeforeRemove = old
			// Preserve the fixture link by moving it aside, then restore the exact original directory.
			if e := os.Rename(dir, dir+"-refused-link"); e != nil {
				t.Fatal(e)
			}
			if e := os.Rename(dir+"-held", dir); e != nil {
				t.Fatal(e)
			}
			for _, suffix := range []string{"", ".sha256"} {
				if _, e := os.ReadFile(obj["patch"] + suffix); e != nil {
					t.Fatal(e)
				}
			}
			code, out, msg = f.run(t, "gc", "--yes")
			rows, e = readReviewSnapshots(f.sd)
			t.Logf("retry restored original parent: exit=%d rows=%d stdout=%s stderr=%s", code, len(rows), out, msg)
			if code != 0 || e != nil || len(rows) != 0 || !strings.Contains(out, "removed snapshot ") {
				t.Error("later gc cannot finish retained snapshot cleanup after the original parent is restored")
			}
			for _, suffix := range []string{"", ".sha256"} {
				_, e := os.Lstat(obj["patch"] + suffix)
				t.Logf("retry artifact suffix=%q error=%v", suffix, e)
				if !os.IsNotExist(e) {
					t.Error("retry left snapshot artifact")
				}
				b, e := os.ReadFile(filepath.Join(outside, filepath.Base(obj["patch"])+suffix))
				if e != nil || string(b) != "recovery foreign"+suffix {
					t.Errorf("retry changed foreign artifact: bytes=%q err=%v", b, e)
				}
			}
		})
	}
}

func TestReview545R6MissingWorktree(t *testing.T) {
	for _, state := range []string{"registered", "unregistered", "patch-missing", "hash-missing", "both-missing"} {
		t.Run(state, func(t *testing.T) {
			f := newSnapshotFixture(t)
			obj := f.freeze(t, "worker")
			if state == "registered" {
				if err := os.Rename(obj["snapshot"], obj["snapshot"]+"-held"); err != nil {
					t.Fatal(err)
				}
			} else {
				fixtureGit(t, f.mutationCopyFixture, f.source, "worktree", "remove", "--force", obj["snapshot"])
			}
			root, err := openSnapshotRoot(filepath.Dir(obj["patch"]))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { root.Close() })
			for _, suffix := range []string{"", ".sha256"} {
				if state == "both-missing" || state == "patch-missing" && suffix == "" || state == "hash-missing" && suffix == ".sha256" {
					if err := root.Remove(filepath.Base(obj["patch"]) + suffix); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := os.WriteFile(filepath.Join(f.sd, "agents.tsv"), []byte("# no owners\n"), 0600); err != nil {
				t.Fatal(err)
			}
			code, out, msg := f.run(t, "gc", "--yes")
			rows, err := readReviewSnapshots(f.sd)
			t.Logf("state=%s exit=%d rows=%d stdout=%s stderr=%s", state, code, len(rows), out, msg)
			if code != 0 || err != nil {
				t.Fatalf("exit=%d err=%v", code, err)
			}
			if state == "registered" {
				if len(rows) != 1 || !strings.Contains(msg, "missing snapshot worktree is still registered") {
					t.Fatal("registered missing worktree was not refused")
				}
			} else if len(rows) != 0 || !strings.Contains(out, "removed snapshot ") {
				t.Fatal("unregistered missing worktree cleanup failed")
			}
			for _, suffix := range []string{"", ".sha256"} {
				_, err := os.Lstat(obj["patch"] + suffix)
				if state == "registered" {
					if err != nil {
						t.Fatal(err)
					}
				} else if !os.IsNotExist(err) {
					t.Fatalf("artifact remains: %v", err)
				}
			}
		})
	}
}

func TestReview545R5LateCleanupParentSwap(t *testing.T) {
	for _, action := range []string{"release", "gc"} {
		t.Run(action, func(t *testing.T) {
			f := newSnapshotFixture(t)
			obj := f.freeze(t, "worker")
			dir := filepath.Dir(obj["patch"])
			outside := filepath.Join(f.root, "late-foreign")
			if e := os.Mkdir(outside, 0700); e != nil {
				t.Fatal(e)
			}
			for _, suffix := range []string{"", ".sha256"} {
				if e := os.WriteFile(filepath.Join(outside, filepath.Base(obj["patch"])+suffix), []byte("foreign"+suffix), 0600); e != nil {
					t.Fatal(e)
				}
			}
			old := snapshotCleanupAfterIdentity
			t.Cleanup(func() { snapshotCleanupAfterIdentity = old })
			reached := false
			snapshotCleanupAfterIdentity = func() {
				reached = true
				review545SwapHeldParent(t, dir, dir+"-held")
				if e := os.Symlink(outside, dir); e != nil {
					t.Fatal(e)
				}
			}
			args := []string{"release", "worker", "--force"}
			if action == "gc" {
				if e := os.WriteFile(filepath.Join(f.sd, "agents.tsv"), []byte("# no owners\n"), 0600); e != nil {
					t.Fatal(e)
				}
				args = []string{"gc", "--yes"}
			}
			code, out, msg := f.run(t, args...)
			rows, e := readReviewSnapshots(f.sd)
			t.Logf("late action=%s reached=%v exit=%d rows=%d stdout=%s stderr=%s", action, reached, code, len(rows), out, msg)
			if !reached || code != 0 || e != nil || len(rows) != 0 || !strings.Contains(out, "removed snapshot ") {
				t.Fatal("late cleanup not reached")
			}
			for _, suffix := range []string{"", ".sha256"} {
				p := filepath.Join(outside, filepath.Base(obj["patch"])+suffix)
				b, e := os.ReadFile(p)
				t.Logf("foreign suffix=%q bytes=%q error=%v", suffix, b, e)
				if e != nil || string(b) != "foreign"+suffix {
					t.Error("late swap deleted foreign artifact")
				}
				_, e = os.Lstat(filepath.Join(dir+"-held", filepath.Base(obj["patch"])+suffix))
				if !os.IsNotExist(e) {
					t.Error("late swap left original artifact")
				}
			}
		})
	}
}
