package cli

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// DJA-391: the .soho-origin marker beside a mutation copy the command
// created in TMPDIR, and the gc scoping to this project's copies.

// writeTestOriginMarker writes a copy's marker by hand (the test's own
// writer, not the command's), with a fixed created value.
func writeTestOriginMarker(t *testing.T, copyPath, project, source string) {
	t.Helper()
	content := "project=" + project + "\nsource=" + source + "\ncreated=2026-01-01T00:00:00Z\n"
	if err := os.WriteFile(copyPath+".soho-origin", []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// markerProject reads the marker beside copyPath and returns the project=
// value; a missing or malformed marker fails the test.
func markerProject(t *testing.T, copyPath string) string {
	t.Helper()
	data, err := os.ReadFile(copyPath + ".soho-origin")
	if err != nil {
		t.Fatalf("no marker beside %s: %v", copyPath, err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("malformed marker beside %s: %q", copyPath, data)
	}
	project, ok := strings.CutPrefix(lines[0], "project=")
	if !ok || project == "" {
		t.Fatalf("the marker's first line is not project=: %q", data)
	}
	return project
}

// gitCommonDirOf runs git rev-parse --path-format=absolute --git-common-dir
// in dir (the independent oracle the marker's project= must equal) and
// resolves symlinks.
func gitCommonDirOf(t *testing.T, dir string, env platform.Env) string {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	cmd.Env = env.List()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse --git-common-dir in %s: %v", dir, err)
	}
	value := strings.TrimSpace(string(out))
	resolved, err := filepath.EvalSymlinks(value)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// mkOldUnregCopy creates an unregistered soho-mutation-* copy below dir with
// size bytes of content and an mtime three hours in the past (older than the
// gc default threshold).
func mkOldUnregCopy(t *testing.T, dir, name string, size int) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "f.txt"), make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
	old := platform.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOriginMarkerWrittenByMutationCopy(t *testing.T) {
	t.Run("without --dest the copy gets its origin marker beside it", func(t *testing.T) {
		f := newMutationCopyFixture(t)
		code, out, errOut := runMutationCopyCmd(t, f, f.source, nil, "--source", f.source)
		if code != 0 {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
		got := decodedCopy(t, out)
		info, err := os.Lstat(got.Copy + ".soho-origin")
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			t.Fatalf("no regular marker beside %s: info=%v err=%v", got.Copy, info, err)
		}
		data, err := os.ReadFile(got.Copy + ".soho-origin")
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
		if len(lines) != 3 {
			t.Fatalf("the marker has %d lines, want 3: %q", len(lines), data)
		}
		project, okP := strings.CutPrefix(lines[0], "project=")
		source, okS := strings.CutPrefix(lines[1], "source=")
		created, okC := strings.CutPrefix(lines[2], "created=")
		if !okP || !okS || !okC || project == "" || source == "" || created == "" {
			t.Fatalf("the marker's lines are not project=/source=/created=: %q", data)
		}
		if want := gitCommonDirOf(t, f.source, f.env); project != want {
			t.Fatalf("project=%q; want the source's git common dir %q", project, want)
		}
		if source != f.source {
			t.Fatalf("source=%q; want the source root as in the JSON %q", source, f.source)
		}
		when, err := time.Parse(copiesTimeLayout, created)
		if err != nil {
			t.Fatalf("created=%q does not parse: %v", created, err)
		}
		if age := platform.Now().Sub(when); age < 0 || age > time.Minute {
			t.Fatalf("created=%q is not now (age %v)", created, age)
		}
	})

	t.Run("two worktrees of the same repository mark the same project", func(t *testing.T) {
		f := newMutationCopyFixture(t)
		worktree := filepath.Join(f.root, "worktree")
		fixtureGit(t, f, f.source, "worktree", "add", "-q", worktree)
		code, out, errOut := runMutationCopyCmd(t, f, f.source, nil, "--source", f.source)
		if code != 0 {
			t.Fatalf("main run: code=%d out=%q err=%q", code, out, errOut)
		}
		code, out2, errOut := runMutationCopyCmd(t, f, worktree, nil, "--source", worktree)
		if code != 0 {
			t.Fatalf("worktree run: code=%d out=%q err=%q", code, out2, errOut)
		}
		projectMain := markerProject(t, decodedCopy(t, out).Copy)
		projectWorktree := markerProject(t, decodedCopy(t, out2).Copy)
		if projectMain != projectWorktree {
			t.Fatalf("the two worktrees mark different projects: %q vs %q", projectMain, projectWorktree)
		}
		if want := gitCommonDirOf(t, f.source, f.env); projectMain != want {
			t.Fatalf("project=%q; want the common dir %q", projectMain, want)
		}
	})

	t.Run("with --dest no marker is written, even when the command creates the destination", func(t *testing.T) {
		f := newMutationCopyFixture(t)
		dest := filepath.Join(f.root, "dest-created")
		code, out, errOut := runMutationCopyCmd(t, f, f.source, nil, "--source", f.source, "--dest", dest)
		if code != 0 {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
		if got := decodedCopy(t, out); got.Copy != dest {
			t.Fatalf("copy=%q; want %q", got.Copy, dest)
		}
		if _, err := os.Lstat(dest + ".soho-origin"); !os.IsNotExist(err) {
			t.Fatalf("a marker was written for a --dest copy: %v", err)
		}
	})

	t.Run("a failing guard removes the copy and the marker", func(t *testing.T) {
		f := newMutationCopyFixture(t)
		code, out, errOut := runMutationCopyCmd(t, f, f.source, platform.Env{"CARGO_TARGET_DIR": filepath.Join(f.source, "target")})
		if code != 1 {
			t.Fatalf("code=%d out=%q err=%q; want the guard's code 1", code, out, errOut)
		}
		entries, err := os.ReadDir(f.tmp)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			t.Fatalf("the failing guard left %q in TMPDIR (copy or marker)", entry.Name())
		}
	})

	t.Run("a marker write failure warns on stderr and changes nothing else", func(t *testing.T) {
		f := newMutationCopyFixture(t)
		old := writeOriginMarker
		writeOriginMarker = func(path, content string) error { return errors.New("the marker write was refused") }
		t.Cleanup(func() { writeOriginMarker = old })
		code, out, errOut := runMutationCopyCmd(t, f, f.source, nil, "--source", f.source)
		if code != 0 {
			t.Fatalf("code=%d out=%q err=%q; the marker failure must not change the exit", code, out, errOut)
		}
		got := decodedCopy(t, out)
		wantLine := "mutation-copy: origin marker not written (the marker write was refused); gc will not remove this copy\n"
		if !strings.Contains(errOut, wantLine) {
			t.Fatalf("stderr=%q; want the marker warning line %q", errOut, wantLine)
		}
		if _, err := os.Lstat(got.Copy + ".soho-origin"); !os.IsNotExist(err) {
			t.Fatalf("a marker appeared despite the refused write: %v", err)
		}
		if _, err := os.Stat(got.Copy); err != nil {
			t.Fatalf("the copy was affected by the marker failure: %v", err)
		}
	})
}

func TestGcUnregisteredOrigin(t *testing.T) {
	threeCopies := func(t *testing.T, f *copiesFixture) (own, other, bare string) {
		t.Helper()
		bare = mkOldUnregCopy(t, f.tmp, "soho-mutation-bare", 40)
		other = mkOldUnregCopy(t, f.tmp, "soho-mutation-other", 40)
		own = mkOldUnregCopy(t, f.tmp, "soho-mutation-own", 40)
		writeTestOriginMarker(t, own, gitCommonDirOf(t, f.repo, f.env), f.repo)
		writeTestOriginMarker(t, other, "/nonexistent/other/.git", "/nonexistent/other")
		return own, other, bare
	}

	t.Run("the dry run shows the origin of each unregistered copy and the hint", func(t *testing.T) {
		f := gcEmptyFixture(t)
		own, other, bare := threeCopies(t, f)
		code, out, errOut := f.run(t, f.env, "gc")
		if code != 0 || errOut != "" {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
		want := "resource pressure: none (disk 60% free, swap 30% used)\n" +
			gcUnregisteredHeader + "\n" +
			bare + "  40 B  3h  no origin marker\n" +
			other + "  40 B  3h  another project: /nonexistent/other\n" +
			own + "  40 B  3h  this project\n" +
			"run 'soho gc --yes --include-unregistered' to remove the unregistered ones\n"
		if out != want {
			t.Fatalf("stdout:\n got:  %q\nwant: %q", out, want)
		}
		for _, path := range []string{own, other, bare} {
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("the dry run touched %s: %v", path, err)
			}
		}
	})

	t.Run("the dry run names the new line when none is this project", func(t *testing.T) {
		f := gcEmptyFixture(t)
		_, other, bare := threeCopies(t, f)
		if err := os.RemoveAll(filepath.Join(f.tmp, "soho-mutation-own")); err != nil {
			t.Fatal(err)
		}
		code, out, errOut := f.run(t, f.env, "gc")
		if code != 0 || errOut != "" {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
		want := "resource pressure: none (disk 60% free, swap 30% used)\n" +
			gcUnregisteredHeader + "\n" +
			bare + "  40 B  3h  no origin marker\n" +
			other + "  40 B  3h  another project: /nonexistent/other\n" +
			"the unregistered copies belong to another project or have no origin marker; gc does not remove them (check each one and remove it by hand)\n"
		if out != want {
			t.Fatalf("stdout:\n got:  %q\nwant: %q", out, want)
		}
		if strings.Contains(out, "include-unregistered' to remove") {
			t.Fatalf("the hint came out with no this-project copy:\n%s", out)
		}
	})

	t.Run("the removal removes only this project's copy and its marker", func(t *testing.T) {
		f := gcEmptyFixture(t)
		own, other, bare := threeCopies(t, f)
		code, out, errOut := f.run(t, f.env, "gc", "--yes", "--include-unregistered")
		if code != 0 {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
		want := "resource pressure: none (disk 60% free, swap 30% used)\n" +
			gcUnregisteredHeader + "\n" +
			"kept " + bare + " (no origin marker)\n" +
			"kept " + other + " (another project: /nonexistent/other)\n" +
			"removed " + own + "  40 B\n" +
			"freed: 40 B\n"
		if out != want {
			t.Fatalf("stdout:\n got:  %q\nwant: %q", out, want)
		}
		if _, err := os.Stat(own); !os.IsNotExist(err) {
			t.Fatalf("the this-project copy was not removed: %v", err)
		}
		if _, err := os.Lstat(own + ".soho-origin"); !os.IsNotExist(err) {
			t.Fatalf("the this-project marker was not removed: %v", err)
		}
		for _, path := range []string{other, bare} {
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("%s was touched: %v", path, err)
			}
		}
		if _, err := os.Lstat(other + ".soho-origin"); err != nil {
			t.Fatalf("the other project's marker was touched: %v", err)
		}
	})

	t.Run("a symlinked or malformed marker counts as no origin marker", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlinks need elevation on Windows without developer mode")
		}
		f := gcEmptyFixture(t)
		valid := mkOldUnregCopy(t, f.tmp, "soho-mutation-symlinked-marker", 40)
		validMarker := valid + ".soho-origin"
		writeTestOriginMarker(t, valid, gitCommonDirOf(t, f.repo, f.env), f.repo)
		real := filepath.Join(f.tmp, "real-marker.txt")
		if err := os.Rename(validMarker, real); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(real, validMarker); err != nil {
			t.Fatal(err)
		}
		short := mkOldUnregCopy(t, f.tmp, "soho-mutation-two-lines", 40)
		if err := os.WriteFile(short+".soho-origin", []byte("project="+gitCommonDirOf(t, f.repo, f.env)+"\nsource="+f.repo+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		swapped := mkOldUnregCopy(t, f.tmp, "soho-mutation-swapped", 40)
		if err := os.WriteFile(swapped+".soho-origin", []byte("source="+f.repo+"\nproject="+gitCommonDirOf(t, f.repo, f.env)+"\ncreated=2026-01-01T00:00:00Z\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		empty := mkOldUnregCopy(t, f.tmp, "soho-mutation-empty-project", 40)
		if err := os.WriteFile(empty+".soho-origin", []byte("project=\nsource="+f.repo+"\ncreated=2026-01-01T00:00:00Z\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, errOut := f.run(t, f.env, "gc")
		if code != 0 || errOut != "" {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
		for _, path := range []string{valid, short, swapped, empty} {
			if !strings.Contains(out, path+"  40 B  3h  no origin marker\n") {
				t.Fatalf("%s is not no origin marker:\n%s", path, out)
			}
		}
		if !strings.Contains(out, "the unregistered copies belong to another project or have no origin marker; gc does not remove them (check each one and remove it by hand)\n") {
			t.Fatalf("the none-is-this-project line is missing:\n%s", out)
		}
	})

	t.Run("gc outside a git repository removes no unregistered copy", func(t *testing.T) {
		f := gcEmptyFixture(t)
		own := mkOldUnregCopy(t, f.tmp, "soho-mutation-own", 40)
		writeTestOriginMarker(t, own, gitCommonDirOf(t, f.repo, f.env), f.repo)
		plain := filepath.Join(f.root, "plain")
		if err := os.MkdirAll(plain, 0o700); err != nil {
			t.Fatal(err)
		}
		f.cwd = plain
		code, out, errOut := f.run(t, f.env, "gc", "--yes", "--include-unregistered")
		if code != 0 {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
		if !strings.Contains(out, "kept "+own+" (") {
			t.Fatalf("the copy was not kept:\n%s", out)
		}
		if _, err := os.Stat(own); err != nil {
			t.Fatalf("the copy was removed outside a git repository: %v", err)
		}
	})
}

func TestRegisteredCopyRemovalDropsTheMarker(t *testing.T) {
	releaseRules := []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: agentIdleJSON},
		{Argv: []string{"pane", "report-metadata", "p-worker", "--source", "soho", "--clear-title"}},
		{Argv: []string{"pane", "list", "--workspace", "ws"}, Stdout: `{"result":{"panes":[]}}`},
	}

	t.Run("gc --yes removes the marker beside a removed registered copy", func(t *testing.T) {
		f, l := gcFixture(t)
		writeTestOriginMarker(t, l.oldOrphan, gitCommonDirOf(t, f.repo, f.env), f.repo)
		code, out, errOut := f.run(t, f.env, "gc", "--yes")
		if code != 0 {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
		if !strings.Contains(out, "removed "+l.oldOrphan+"  25 B\n") {
			t.Fatalf("the registered copy was not removed:\n%s", out)
		}
		if _, err := os.Lstat(l.oldOrphan + ".soho-origin"); !os.IsNotExist(err) {
			t.Fatalf("the marker beside the removed copy was not removed: %v", err)
		}
	})

	t.Run("release removes the marker beside a removed registered copy", func(t *testing.T) {
		f := newCopiesFixture(t, releaseRules)
		f.writeRoster(t, copiesRosterRow("worker", "p-worker"))
		copyA := filepath.Join(f.tmp, "copy-a")
		if err := os.MkdirAll(copyA, 0o700); err != nil {
			t.Fatal(err)
		}
		writeTestOriginMarker(t, copyA, gitCommonDirOf(t, f.repo, f.env), f.repo)
		f.writeCopies(t, []core.CopyRow{{Path: copyA, Owner: "worker", Pane: "p-worker", Created: core.FrictionISO(platform.Now()), Source: "-", Origin: "mutation-copy"}})
		addReleaseReport(t, filepath.Join(f.state, "ws"), "worker", false)
		code, out, errOut := f.run(t, f.env, "release", "worker")
		if code != 0 {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
		if !strings.Contains(out, "removed copy "+copyA+"\n") {
			t.Fatalf("the copy was not removed:\n%s", out)
		}
		if _, err := os.Lstat(copyA + ".soho-origin"); !os.IsNotExist(err) {
			t.Fatalf("the marker beside the released copy was not removed: %v", err)
		}
	})
}
