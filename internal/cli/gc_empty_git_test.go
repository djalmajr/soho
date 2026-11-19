package cli

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

// gcEmptyGitKeptSuffix is the ending of a kept line whose cause came from
// the path guard, in the dry run and in --yes alike.
const gcEmptyGitKeptSuffix = "; gc never removes it: delete it by hand if it is yours"

// writeGitInitLayout leaves the directories and files git init creates before
// the first commit. The fixture writes them directly: no git command is run.
func writeGitInitLayout(t *testing.T, gitDir string) {
	t.Helper()
	for _, dir := range []string{"objects/info", "objects/pack", "refs/heads", "refs/tags"} {
		if err := os.MkdirAll(filepath.Join(gitDir, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{
		"HEAD":   "ref: refs/heads/main\n",
		"config": "[core]\n\trepositoryformatversion = 0\n",
	} {
		if err := os.WriteFile(filepath.Join(gitDir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// gcEmptyGitFixture registers one old orphan copy under f.tmp, with the
// given Origin and whatever .git layout setup writes in it.
func gcEmptyGitFixture(t *testing.T, origin string, setup func(*testing.T, *copiesFixture, string)) (*copiesFixture, string) {
	t.Helper()
	f := gcEmptyFixture(t)
	copyPath := filepath.Join(f.tmp, "empty-git-copy")
	if err := os.MkdirAll(copyPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if setup != nil {
		setup(t, f, copyPath)
	}
	old := platform.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(copyPath, old, old); err != nil {
		t.Fatal(err)
	}
	f.writeCopies(t, []core.CopyRow{{
		Path: copyPath, Owner: "ghost", Pane: "-", Created: core.FrictionISO(old), Source: "-", Origin: origin,
	}})
	return f, copyPath
}

// ageCopyTree gives every entry an old mtime: the idle-owner rule reads the
// newest entry under the copy, not just the registry's created column.
func ageCopyTree(t *testing.T, root string, old time.Time) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, _ fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		return os.Chtimes(path, old, old)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func gcEmptyGitKeptLine(path string) string {
	return "kept " + path + " (contains a .git (another repository or a worktree)" + gcEmptyGitKeptSuffix + ")"
}

// SOH-428: an empty git init layout is not another repository for gc. The
// dry run promises the removal, and --yes removes the directory and its line.
func TestGcRemovesMutationCopyWithEmptyGit(t *testing.T) {
	f, copyPath := gcEmptyGitFixture(t, "mutation-copy", func(t *testing.T, _ *copiesFixture, copy string) {
		writeGitInitLayout(t, filepath.Join(copy, ".git"))
	})

	code, out, errOut := f.run(t, f.env, "gc")
	if code != 0 || errOut != "" {
		t.Fatalf("preview: code=%d out=%q err=%q", code, out, errOut)
	}
	if !strings.Contains(out, "would remove "+copyPath+"  ") {
		t.Fatalf("the empty .git copy is not a candidate:\n%s", out)
	}
	if strings.Contains(out, "kept "+copyPath) {
		t.Fatalf("the empty .git copy was kept in the preview:\n%s", out)
	}

	code, out, errOut = f.run(t, f.env, "gc", "--yes")
	if code != 0 || errOut != "" {
		t.Fatalf("--yes: code=%d out=%q err=%q", code, out, errOut)
	}
	if !strings.Contains(out, "removed "+copyPath+"  ") {
		t.Fatalf("--yes did not remove the empty .git copy:\n%s", out)
	}
	if _, err := os.Lstat(copyPath); !os.IsNotExist(err) {
		t.Fatalf("the copy is still on disk: %v", err)
	}
	if rows := f.copiesRows(t); len(rows) != 0 {
		t.Fatalf("the registry line stayed: %#v", rows)
	}
}

// SOH-428 boundary: packed-refs with only blank lines and comments is still
// an empty repository.
func TestGcRemovesMutationCopyWithCommentOnlyPackedRefs(t *testing.T) {
	f, copyPath := gcEmptyGitFixture(t, "mutation-copy", func(t *testing.T, _ *copiesFixture, copy string) {
		gitDir := filepath.Join(copy, ".git")
		writeGitInitLayout(t, gitDir)
		body := "# pack-refs with: peeled fully-peeled sorted\n\n"
		if err := os.WriteFile(filepath.Join(gitDir, "packed-refs"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	})
	code, out, errOut := f.run(t, f.env, "gc")
	if code != 0 || errOut != "" || !strings.Contains(out, "would remove "+copyPath+"  ") {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
}

// SOH-428 identity and boundary: every other .git keeps the main refusal in
// both modes, and no directory is touched.
func TestGcKeepsGitThatIsNotEmptyOrNotADirectory(t *testing.T) {
	cases := []struct {
		name   string
		origin string
		setup  func(*testing.T, *copiesFixture, string)
	}{
		{
			name: "object under objects/ab",
			setup: func(t *testing.T, _ *copiesFixture, copy string) {
				gitDir := filepath.Join(copy, ".git")
				writeGitInitLayout(t, gitDir)
				if err := os.MkdirAll(filepath.Join(gitDir, "objects", "ab"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(gitDir, "objects", "ab", "cdef"), []byte("blob"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "object under objects/pack",
			setup: func(t *testing.T, _ *copiesFixture, copy string) {
				gitDir := filepath.Join(copy, ".git")
				writeGitInitLayout(t, gitDir)
				if err := os.WriteFile(filepath.Join(gitDir, "objects", "pack", "pack-fake.pack"), []byte("blob"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "ref under refs/heads",
			setup: func(t *testing.T, _ *copiesFixture, copy string) {
				gitDir := filepath.Join(copy, ".git")
				writeGitInitLayout(t, gitDir)
				if err := os.WriteFile(filepath.Join(gitDir, "refs", "heads", "main"), []byte("0123456789"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "packed refs line",
			setup: func(t *testing.T, _ *copiesFixture, copy string) {
				gitDir := filepath.Join(copy, ".git")
				writeGitInitLayout(t, gitDir)
				body := "0000000000000000000000000000000000000000 refs/heads/main\n"
				if err := os.WriteFile(filepath.Join(gitDir, "packed-refs"), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "packed refs line without a trailing newline",
			setup: func(t *testing.T, _ *copiesFixture, copy string) {
				gitDir := filepath.Join(copy, ".git")
				writeGitInitLayout(t, gitDir)
				body := "# pack-refs with: peeled fully-peeled sorted\n0000000000000000000000000000000000000000 refs/heads/main"
				if err := os.WriteFile(filepath.Join(gitDir, "packed-refs"), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "git file",
			setup: func(t *testing.T, _ *copiesFixture, copy string) {
				if err := os.WriteFile(filepath.Join(copy, ".git"), []byte("gitdir: /elsewhere\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "git symlink to an empty directory",
			setup: func(t *testing.T, _ *copiesFixture, copy string) {
				if runtime.GOOS == "windows" {
					t.Skip("symlinks need elevation on Windows without developer mode")
				}
				target := filepath.Join(copy, "git-dir")
				writeGitInitLayout(t, target)
				if err := os.Symlink(target, filepath.Join(copy, ".git")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "git symlink to a repository with history",
			setup: func(t *testing.T, f *copiesFixture, copy string) {
				if runtime.GOOS == "windows" {
					t.Skip("symlinks need elevation on Windows without developer mode")
				}
				if err := os.Symlink(filepath.Join(f.repo, ".git"), filepath.Join(copy, ".git")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:   "empty git on an add line",
			origin: "add",
			setup: func(t *testing.T, _ *copiesFixture, copy string) {
				writeGitInitLayout(t, filepath.Join(copy, ".git"))
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			origin := tc.origin
			if origin == "" {
				origin = "mutation-copy"
			}
			f, copyPath := gcEmptyGitFixture(t, origin, tc.setup)
			kept := gcEmptyGitKeptLine(copyPath)

			code, out, errOut := f.run(t, f.env, "gc")
			if code != 0 || errOut != "" || !strings.Contains(out, kept) {
				t.Fatalf("preview: code=%d out=%q err=%q; want %q", code, out, errOut, kept)
			}
			if _, err := os.Lstat(copyPath); err != nil {
				t.Fatalf("the dry run touched the copy: %v", err)
			}

			code, out, errOut = f.run(t, f.env, "gc", "--yes")
			if code != 0 || errOut != "" || !strings.Contains(out, kept) {
				t.Fatalf("--yes: code=%d out=%q err=%q; want %q", code, out, errOut, kept)
			}
			if _, err := os.Lstat(copyPath); err != nil {
				t.Fatalf("--yes touched the kept copy: %v", err)
			}
			if rows := f.copiesRows(t); len(rows) != 1 || rows[0].Path != copyPath {
				t.Fatalf("--yes dropped the registry line: %#v", rows)
			}
		})
	}
}

// The idle-owner path checks the registered copy and then its rename in
// the removal phase; both must accept the same empty .git layout.
func TestGcIdleOwnerCopyWithEmptyGit(t *testing.T) {
	g := newIdleGc(t, idleRules("idle"))
	writeGitInitLayout(t, filepath.Join(g.copy, ".git"))
	ageCopyTree(t, g.copy, platform.Now().Add(-3*time.Hour))
	g.register(t)

	code, out, errOut := g.f.run(t, g.f.env, "gc")
	if code != 0 || errOut != "" || !strings.Contains(out, "would remove "+g.copy+"  ") || !strings.Contains(out, gcIdleOwnerMark) {
		t.Fatalf("preview: code=%d out=%q err=%q", code, out, errOut)
	}
	code, out, errOut = g.f.run(t, g.f.env, "gc", "--yes")
	if code != 0 || errOut != "" || !strings.Contains(out, "removed "+g.copy+"  ") {
		t.Fatalf("--yes: code=%d out=%q err=%q", code, out, errOut)
	}
	if _, err := os.Lstat(g.copy); !os.IsNotExist(err) {
		t.Fatalf("the idle owner copy is still on disk: %v", err)
	}
	if rows := g.f.copiesRows(t); len(rows) != 0 {
		t.Fatalf("the registry line stayed: %#v", rows)
	}
}

// A read error inside objects means "not empty", not "probably empty".
func TestGcKeepsGitWithUnreadableObjectsSubdir(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("an unreadable directory needs POSIX permissions and a non-root user")
	}
	f, copyPath := gcEmptyGitFixture(t, "mutation-copy", func(t *testing.T, _ *copiesFixture, copy string) {
		// The rest of the layout is a complete git init, so only the read
		// error can keep the copy (R591 F2).
		writeGitInitLayout(t, filepath.Join(copy, ".git"))
		secret := filepath.Join(copy, ".git", "objects", "secret")
		if err := os.MkdirAll(secret, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(secret, 0o700) })
		if err := os.Chmod(secret, 0); err != nil {
			t.Fatal(err)
		}
	})
	kept := gcEmptyGitKeptLine(copyPath)
	for _, args := range [][]string{{"gc"}, {"gc", "--yes"}} {
		code, out, errOut := f.run(t, f.env, args...)
		if code != 0 || errOut != "" || !strings.Contains(out, kept) {
			t.Fatalf("%v: code=%d out=%q err=%q; want %q", args, code, out, errOut, kept)
		}
	}
	if _, err := os.Lstat(copyPath); err != nil {
		t.Fatalf("the copy was touched: %v", err)
	}
}

// SOH-429: a missing directory leaves only a line to drop. The dry run says
// so without a size, a total or an age; --yes keeps its main removal line.
func TestGcMissingLineDropTextInPreview(t *testing.T) {
	f := gcEmptyFixture(t)
	missing := filepath.Join(f.tmp, "gone-copy")
	old := platform.Now().Add(-3 * time.Hour)
	f.writeCopies(t, []core.CopyRow{{
		Path: missing, Owner: "worker", Pane: "p-worker", Created: core.FrictionISO(old), Source: "-", Origin: "mutation-copy",
	}})

	code, out, errOut := f.run(t, f.env, "gc")
	want := strings.Join([]string{
		gcPressureNone,
		"would drop the line of " + missing + " (already gone from disk)",
		"run 'soho gc --yes' to remove them",
	}, "\n") + "\n"
	if code != 0 || errOut != "" || out != want {
		t.Fatalf("preview: code=%d out=%q err=%q; want %q", code, out, errOut, want)
	}
	if strings.Contains(out, "total:") {
		t.Fatalf("a line to drop carries no total:\n%s", out)
	}

	code, out, errOut = f.run(t, f.env, "gc", "--yes")
	want = strings.Join([]string{
		gcPressureNone,
		"removed " + missing + "  0 B",
		"freed: 0 B (the copies were already gone from disk)",
	}, "\n") + "\n"
	if code != 0 || errOut != "" || out != want {
		t.Fatalf("--yes: code=%d out=%q err=%q; want %q", code, out, errOut, want)
	}
	if rows := f.copiesRows(t); len(rows) != 0 {
		t.Fatalf("the registry line stayed: %#v", rows)
	}
}

// R591 F1: gc removes a copy recursively, so an empty-git mutation copy
// that contains another registered copy keeps main's guard. The inner copy
// here has a ref; both stay on disk and in the registry, in both modes.
func TestGcKeepsEmptyGitCopyThatContainsARegisteredCopy(t *testing.T) {
	f, parent := gcEmptyGitFixture(t, "mutation-copy", func(t *testing.T, _ *copiesFixture, path string) {
		writeGitInitLayout(t, filepath.Join(path, ".git"))
	})
	child := filepath.Join(parent, "nested-copy")
	gitDir := filepath.Join(child, ".git")
	writeGitInitLayout(t, gitDir)
	ref := filepath.Join(gitDir, "refs", "heads", "main")
	if err := os.WriteFile(ref, []byte("1234567890123456789012345678901234567890\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rows := f.copiesRows(t)
	nested := rows[0]
	nested.Path = child
	f.writeCopies(t, append(rows, nested))

	for _, args := range [][]string{{"gc"}, {"gc", "--yes"}} {
		code, out, errOut := f.run(t, f.env, args...)
		if code != 0 || errOut != "" || !strings.Contains(out, gcEmptyGitKeptLine(parent)) || !strings.Contains(out, gcEmptyGitKeptLine(child)) {
			t.Fatalf("%v: code=%d out=%q err=%q; want both copies kept", args, code, out, errOut)
		}
	}
	if _, err := os.Lstat(ref); err != nil {
		t.Fatalf("the nested repository's ref did not survive: %v", err)
	}
	if got := len(f.copiesRows(t)); got != 2 {
		t.Fatalf("registry has %d lines; want both kept", got)
	}
}

// The inner copy's line may name it through another path to the same
// directory: the containment check compares resolved paths too.
func TestGcKeepsEmptyGitCopyThatContainsACopyRegisteredThroughASymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory symlinks need POSIX semantics here")
	}
	f, parent := gcEmptyGitFixture(t, "mutation-copy", func(t *testing.T, _ *copiesFixture, path string) {
		writeGitInitLayout(t, filepath.Join(path, ".git"))
	})
	child := filepath.Join(parent, "nested-copy")
	if err := os.MkdirAll(child, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(f.tmp, "alias-of-parent")
	if err := os.Symlink(parent, alias); err != nil {
		t.Fatal(err)
	}
	rows := f.copiesRows(t)
	nested := rows[0]
	nested.Path = filepath.Join(alias, "nested-copy")
	nested.Owner = "worker"
	f.writeCopies(t, append(rows, nested))

	code, out, errOut := f.run(t, f.env, "gc")
	if code != 0 || errOut != "" || !strings.Contains(out, gcEmptyGitKeptLine(parent)) {
		t.Fatalf("code=%d out=%q err=%q; want the parent kept", code, out, errOut)
	}
}

// R592: on a case-insensitive volume the inner copy's line may spell the
// outer copy with another case; containment compares identity, not text.
func TestGcKeepsEmptyGitCopyThatContainsACopyRegisteredWithAnotherCase(t *testing.T) {
	f, parent := gcEmptyGitFixture(t, "mutation-copy", func(t *testing.T, _ *copiesFixture, path string) {
		writeGitInitLayout(t, filepath.Join(path, ".git"))
	})
	upperParent := filepath.Join(filepath.Dir(parent), strings.ToUpper(filepath.Base(parent)))
	parentInfo, err := os.Stat(parent)
	if err != nil {
		t.Fatal(err)
	}
	upperInfo, err := os.Stat(upperParent)
	if os.IsNotExist(err) {
		t.Skip("fixture requires a case-insensitive filesystem")
	}
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(parentInfo, upperInfo) {
		t.Fatal("different spellings must name the same directory")
	}

	child := filepath.Join(parent, "nested-copy")
	gitDir := filepath.Join(child, ".git")
	writeGitInitLayout(t, gitDir)
	ref := filepath.Join(gitDir, "refs", "heads", "main")
	if err := os.WriteFile(ref, []byte("1234567890123456789012345678901234567890\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rows := f.copiesRows(t)
	nested := rows[0]
	nested.Path = filepath.Join(upperParent, "nested-copy")
	f.writeCopies(t, append(rows, nested))
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		t.Fatal(err)
	}
	resolvedChild, err := filepath.EvalSymlinks(nested.Path)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("resolved parent %q, resolved child %q", resolvedParent, resolvedChild)

	for _, args := range [][]string{{"gc"}, {"gc", "--yes"}} {
		code, out, errOut := f.run(t, f.env, args...)
		t.Logf("%v (exit %d, stderr %q):\n%s", args, code, errOut, out)
		if code != 0 || errOut != "" || !strings.Contains(out, gcEmptyGitKeptLine(parent)) || !strings.Contains(out, gcEmptyGitKeptLine(nested.Path)) {
			t.Errorf("%v: expected both registered directories to be kept", args)
		}
	}
	if _, err := os.Lstat(ref); err != nil {
		t.Errorf("nested repository ref did not survive: %v", err)
	}
	if got := len(f.copiesRows(t)); got != 2 {
		t.Errorf("registry has %d rows; want 2", got)
	}
}

// R593: the empty-.git decision comes from the registry read at the start
// of gc. A copy registered inside the candidate after that read must still
// keep it: the live deletion guard rereads the registry and, for a
// candidate admitted by the exception, refuses a nested line of any owner.
// The injection runs at the first clock read inside cmdGc, which comes after
// the registry read; the test fails if it never ran.
func TestGcKeepsEmptyGitCopyWhenACopyIsRegisteredInsideAfterTheRead(t *testing.T) {
	f, parent := gcEmptyGitFixture(t, "mutation-copy", func(t *testing.T, _ *copiesFixture, path string) {
		writeGitInitLayout(t, filepath.Join(path, ".git"))
	})
	nested := f.copiesRows(t)[0]
	nested.Path = filepath.Join(parent, "nested-copy")
	if err := os.MkdirAll(nested.Path, 0o700); err != nil {
		t.Fatal(err)
	}
	stateDir := f.stateDir(t)
	gitDir := filepath.Join(nested.Path, ".git")
	ref := filepath.Join(gitDir, "refs", "heads", "main")

	now := platform.Now
	injected := false
	platform.Now = func() time.Time {
		pc, _, _, ok := runtime.Caller(1)
		if ok && !injected && strings.HasSuffix(runtime.FuncForPC(pc).Name(), ".cmdGc") {
			injected = true
			nested.Origin = "add"
			nested.Created = core.FrictionISO(now())
			if err := core.UpsertCopies(stateDir, nested); err != nil {
				t.Fatal(err)
			}
			writeGitInitLayout(t, gitDir)
			if err := os.WriteFile(ref, []byte("1234567890123456789012345678901234567890\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return now()
	}
	t.Cleanup(func() { platform.Now = now })

	code, out, errOut := f.run(t, f.env, "gc", "--yes")
	if !injected {
		t.Fatal("the late registration did not run")
	}
	// The live guard's refusal keeps main's held-copy line on stderr.
	if code != 0 || !strings.Contains(errOut, parent+": kept: holds "+nested.Path+" registered by ") || strings.Contains(out, "removed "+parent) {
		t.Fatalf("code=%d out=%q err=%q; want the parent kept as holding the nested copy", code, out, errOut)
	}
	if _, err := os.Lstat(ref); err != nil {
		t.Fatalf("the late-registered copy's ref did not survive: %v", err)
	}
	if rows := f.copiesRows(t); len(rows) != 2 {
		t.Fatalf("registry has %d lines; want 2", len(rows))
	}
}

// copyDeletionGuardFor with anyOwner refuses a nested line of the same
// owner; without it (every caller but the empty-.git candidates) the guard
// stays as on main and passes it.
func TestCopyDeletionGuardForAnyOwner(t *testing.T) {
	f, parent := gcEmptyGitFixture(t, "mutation-copy", nil)
	nested := f.copiesRows(t)[0]
	nested.Path = filepath.Join(parent, "nested-copy")
	if err := os.MkdirAll(nested.Path, 0o700); err != nil {
		t.Fatal(err)
	}
	stateDir := f.stateDir(t)
	if err := core.UpsertCopies(stateDir, nested); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if held, err := copyDeletionGuardFor(parent, nested.Owner, stateDir, f.env, cwd, false); err != nil || held != "" {
		t.Fatalf("anyOwner=false: held=%q err=%v; want the same-owner line passed as on main", held, err)
	}
	held, err := copyDeletionGuardFor(parent, nested.Owner, stateDir, f.env, cwd, true)
	if err != nil || !strings.Contains(held, "holds "+nested.Path) {
		t.Fatalf("anyOwner=true: held=%q err=%v; want the nested line named", held, err)
	}
}

// R594 control: a mutation copy without any .git never needs the exception,
// so a copy of the same owner registered inside it after the read does not
// keep it: gc removes it as on main.
func TestGcRemovesOrdinaryMutationCopyWhenASameOwnerCopyIsRegisteredInsideAfterTheRead(t *testing.T) {
	f, parent := gcEmptyGitFixture(t, "mutation-copy", nil)
	nested := f.copiesRows(t)[0]
	nested.Path = filepath.Join(parent, "nested-copy")
	if err := os.MkdirAll(nested.Path, 0o700); err != nil {
		t.Fatal(err)
	}
	stateDir := f.stateDir(t)

	now := platform.Now
	injected := false
	platform.Now = func() time.Time {
		pc, _, _, ok := runtime.Caller(1)
		if ok && !injected && strings.HasSuffix(runtime.FuncForPC(pc).Name(), ".cmdGc") {
			injected = true
			nested.Origin = "add"
			nested.Created = core.FrictionISO(now())
			if err := core.UpsertCopies(stateDir, nested); err != nil {
				t.Fatal(err)
			}
		}
		return now()
	}
	t.Cleanup(func() { platform.Now = now })

	code, out, errOut := f.run(t, f.env, "gc", "--yes")
	if !injected {
		t.Fatal("the late registration did not run")
	}
	if code != 0 || errOut != "" || !strings.Contains(out, "removed "+parent+"  ") {
		t.Fatalf("code=%d out=%q err=%q; want the ordinary copy removed as on main", code, out, errOut)
	}
	if _, err := os.Lstat(parent); !os.IsNotExist(err) {
		t.Fatalf("the ordinary copy is still on disk: %v", err)
	}
}
