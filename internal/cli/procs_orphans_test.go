package cli

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

func init() {
	// No cli test reads this host's orphan processes: the seam lists none
	// (on every platform, so the Windows line stays out of the other
	// tests' output too). The orphan tests below set their own list.
	orphanProcesses = func() ([]platform.OrphanProc, error) { return nil, nil }
}

// orphanScopeFixture is a procs fixture with one directory of each scope
// kind, plus two outside the project: an unrelated one and a sibling of
// the copy whose name only extends the copy's (copy-2).
type orphanScopeFixture struct {
	*procsFixture
	copy, state, scratch, worktree, root, outside, sibling string
}

func orphanResolvedDir(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func newOrphanScopeFixture(t *testing.T) *orphanScopeFixture {
	t.Helper()
	f := newProcsFixture(t)
	base := t.TempDir()
	o := &orphanScopeFixture{
		procsFixture: f,
		copy:         orphanResolvedDir(t, filepath.Join(base, "copy")),
		sibling:      orphanResolvedDir(t, filepath.Join(base, "copy-2")),
		state:        orphanResolvedDir(t, f.state),
		scratch:      orphanResolvedDir(t, filepath.Join(f.env.Get("TMPDIR"), "soho", "ws")),
		worktree:     orphanResolvedDir(t, filepath.Join(base, "wt")),
		root:         orphanResolvedDir(t, f.cwd),
		outside:      orphanResolvedDir(t, filepath.Join(base, "elsewhere")),
	}
	// A copy of a live owner: gc never removes it, whatever the age.
	if err := core.WriteCopies(f.state, []core.CopyRow{{Path: o.copy, Owner: "worker", Pane: "p-worker", Created: core.FrictionISO(platform.Now()), Source: "-", Origin: "add"}}); err != nil {
		t.Fatal(err)
	}
	oldTrees := orphanWorktrees
	orphanWorktrees = func(string, platform.Env) []string { return []string{o.root, o.worktree} }
	t.Cleanup(func() { orphanWorktrees = oldTrees })
	return o
}

// stubOrphans makes the platform list return list, and the cwd re-read
// return each listed cwd (cwds overrides it per pid).
func stubOrphans(t *testing.T, list []platform.OrphanProc, cwds map[int]string) {
	t.Helper()
	oldList, oldCwd := orphanProcesses, orphanProcCwd
	orphanProcesses = func() ([]platform.OrphanProc, error) { return list, nil }
	orphanProcCwd = func(pid int) (string, error) {
		if cwd, ok := cwds[pid]; ok {
			return cwd, nil
		}
		for _, proc := range list {
			if proc.Pid == pid {
				return proc.Cwd, nil
			}
		}
		return "", os.ErrNotExist
	}
	t.Cleanup(func() { orphanProcesses, orphanProcCwd = oldList, oldCwd })
}

// realOrphan is a live process this test owns (killed in the cleanup),
// presented as an orphan with cwd and the given age; its identity is the
// real one, so the re-check before the signal passes unless the test
// changes it.
func (o *orphanScopeFixture) realOrphan(t *testing.T, cwd string, age time.Duration) platform.OrphanProc {
	t.Helper()
	pid := o.sleeper(t)
	identity, name, live := platform.ProcStartIdentity(pid, o.env)
	if live != platform.ProcRunning {
		t.Fatalf("the sleeper %d is not running", pid)
	}
	return platform.OrphanProc{Pid: pid, Identity: identity, Name: name, Cwd: cwd, Started: platform.Now().Add(-age)}
}

func fakeOrphan(pid int, name, cwd string, age time.Duration) platform.OrphanProc {
	return platform.OrphanProc{Pid: pid, Identity: "fake:" + strconv.Itoa(pid), Name: name, Cwd: cwd, Started: platform.Now().Add(-age)}
}

// TestProcsListsProjectOrphansByScope pins the procs block: the five
// scope kinds, in the platform's order, with the line layout; a cwd
// outside the project, one in a sibling whose name only extends the
// copy's, and a pid procs.tsv holds are left out.
func TestProcsListsProjectOrphansByScope(t *testing.T) {
	o := newOrphanScopeFixture(t)
	o.writeProcs(t, core.ProcRow{Pid: 4000009, Started: "Sun Oct  3 00:00:00 2026", Name: "sleep", Owner: "-", Pane: "-", Created: core.FrictionISO(platform.Now())})
	stubOrphans(t, []platform.OrphanProc{
		fakeOrphan(4000001, "node", filepath.Join(o.copy, "web"), 3*time.Hour),
		fakeOrphan(4000002, "go", o.state, 3*time.Hour),
		fakeOrphan(4000003, "vite", filepath.Join(o.scratch, "x"), 30*time.Minute),
		fakeOrphan(4000004, "zsh", o.worktree, 3*time.Hour),
		fakeOrphan(4000005, "nvim", filepath.Join(o.root, "src"), 3*time.Hour),
		fakeOrphan(4000006, "other", o.outside, 3*time.Hour),
		fakeOrphan(4000007, "sib", o.sibling, 3*time.Hour),
		fakeOrphan(4000009, "sleep", o.copy, 3*time.Hour),
	}, nil)
	code, out, errOut := o.run(t, "procs")
	if code != 0 || errOut != "" {
		t.Fatalf("procs: code=%d err=%q out=%q", code, errOut, out)
	}
	want := "4000009  sleep  -  0m  gone\n" +
		"unregistered orphan processes in this project:\n" +
		"4000001  node  3h  " + filepath.Join(o.copy, "web") + "  (copy)\n" +
		"4000002  go  3h  " + o.state + "  (state)\n" +
		"4000003  vite  30m  " + filepath.Join(o.scratch, "x") + "  (scratch)\n" +
		"4000004  zsh  3h  " + o.worktree + "  (worktree)\n" +
		"4000005  nvim  3h  " + filepath.Join(o.root, "src") + "  (root)\n"
	if out != want {
		t.Fatalf("procs output = %q; want %q", out, want)
	}
}

// TestProcsOrphanBlockAbsentAndWindowsLine: no orphan in the project
// prints no block; the platform's not-detected answer (Windows) prints
// the one line, after the registry lines.
func TestProcsOrphanBlockAbsentAndWindowsLine(t *testing.T) {
	o := newOrphanScopeFixture(t)
	t.Run("no orphan in the project, no block", func(t *testing.T) {
		stubOrphans(t, []platform.OrphanProc{fakeOrphan(4000006, "other", o.outside, 3*time.Hour)}, nil)
		code, out, errOut := o.run(t, "procs")
		if code != 0 || errOut != "" || out != "no registered processes\n" {
			t.Fatalf("procs: code=%d err=%q out=%q; want only the empty registry line", code, errOut, out)
		}
	})
	t.Run("Windows detects none and says so", func(t *testing.T) {
		old := orphanProcesses
		orphanProcesses = func() ([]platform.OrphanProc, error) { return nil, platform.ErrOrphansNotDetected }
		t.Cleanup(func() { orphanProcesses = old })
		code, out, errOut := o.run(t, "procs")
		want := "no registered processes\nunregistered processes: not detected on Windows (only registered processes are listed)\n"
		if code != 0 || errOut != "" || out != want {
			t.Fatalf("procs: code=%d err=%q out=%q; want %q", code, errOut, out, want)
		}
		code, out, errOut = o.run(t, "gc")
		if code != 0 || errOut != "" || strings.Contains(out, "unregistered orphan") || strings.Contains(out, "not detected") {
			t.Fatalf("gc: code=%d err=%q out=%q; gc shows no orphan block on Windows", code, errOut, out)
		}
	})
}

// TestGcOrphanDryRun pins the dry-run block: would stop for a copy, state
// or scratch orphan past --older-than, listed only for the root and the
// worktree whatever their age, a young stoppable one left out, the hint
// naming the new flag; --older-than 0 makes the young one a would stop.
func TestGcOrphanDryRun(t *testing.T) {
	o := newOrphanScopeFixture(t)
	stubOrphans(t, []platform.OrphanProc{
		fakeOrphan(4000001, "node", o.copy, 3*time.Hour),
		fakeOrphan(4000002, "go", o.state, 3*time.Hour),
		fakeOrphan(4000003, "vite", o.scratch, 30*time.Minute),
		fakeOrphan(4000004, "zsh", o.worktree, 3*time.Hour),
		fakeOrphan(4000005, "nvim", o.root, 30*time.Minute),
		fakeOrphan(4000006, "other", o.outside, 3*time.Hour),
	}, nil)
	pressure := "resource pressure: none (disk 60% free, swap 30% used)\n"
	t.Run("default --older-than", func(t *testing.T) {
		code, out, errOut := o.run(t, "gc")
		want := pressure + "unregistered orphan processes in this project:\n" +
			"would stop 4000001  node  3h  " + o.copy + "  (copy)\n" +
			"would stop 4000002  go  3h  " + o.state + "  (state)\n" +
			"listed only 4000004  zsh  3h  " + o.worktree + "  (worktree)\n" +
			"listed only 4000005  nvim  30m  " + o.root + "  (root)\n" +
			"run 'soho gc --yes --include-unregistered-procs' to stop them\n"
		if code != 0 || errOut != "" || out != want {
			t.Fatalf("gc: code=%d err=%q\nout=%q\nwant=%q", code, errOut, out, want)
		}
	})
	t.Run("--older-than 0", func(t *testing.T) {
		code, out, errOut := o.run(t, "gc", "--older-than", "0")
		if code != 0 || errOut != "" || !strings.Contains(out, "would stop 4000003  vite  30m  "+o.scratch+"  (scratch)\n") {
			t.Fatalf("gc --older-than 0: code=%d err=%q out=%q; want the young scratch orphan as would stop", code, errOut, out)
		}
	})
	t.Run("only listed-only orphans: nothing to remove, then the block", func(t *testing.T) {
		stubOrphans(t, []platform.OrphanProc{fakeOrphan(4000005, "nvim", o.root, 3*time.Hour)}, nil)
		code, out, errOut := o.run(t, "gc")
		want := pressure + "nothing to remove\nunregistered orphan processes in this project:\n" +
			"listed only 4000005  nvim  3h  " + o.root + "  (root)\n"
		if code != 0 || errOut != "" || out != want {
			t.Fatalf("gc: code=%d err=%q\nout=%q\nwant=%q", code, errOut, out, want)
		}
	})
}

// TestGcStopsOrphansOnlyWithTheFlag runs real processes: --yes alone and
// the flag alone stop nothing; --include-unregistered-procs --yes stops
// the copy, state and scratch orphans past --older-than, and never the
// root or worktree ones nor a young one.
func TestGcStopsOrphansOnlyWithTheFlag(t *testing.T) {
	o := newOrphanScopeFixture(t)
	inCopy := o.realOrphan(t, o.copy, 3*time.Hour)
	inState := o.realOrphan(t, o.state, 3*time.Hour)
	inScratch := o.realOrphan(t, filepath.Join(o.scratch, "x"), 3*time.Hour)
	young := o.realOrphan(t, o.copy, 10*time.Minute)
	inRoot := o.realOrphan(t, o.root, 3*time.Hour)
	inTree := o.realOrphan(t, o.worktree, 3*time.Hour)
	stubOrphans(t, []platform.OrphanProc{inCopy, inState, inScratch, young, inRoot, inTree}, nil)
	all := []platform.OrphanProc{inCopy, inState, inScratch, young, inRoot, inTree}
	allAlive := func(t *testing.T, when string) {
		t.Helper()
		for _, proc := range all {
			if !procAlive(t, o.env, proc.Pid) {
				t.Fatalf("%s stopped process %d (%s)", when, proc.Pid, proc.Cwd)
			}
		}
	}
	for _, argv := range [][]string{{"gc", "--yes"}, {"gc", "--include-unregistered-procs"}} {
		code, out, errOut := o.run(t, argv...)
		if code != 0 || errOut != "" || !strings.Contains(out, "would stop "+strconv.Itoa(inCopy.Pid)+"  ") || strings.Contains(out, "stopped process") {
			t.Fatalf("%v: code=%d err=%q out=%q; want the block listed and nothing stopped", argv, code, errOut, out)
		}
		allAlive(t, strings.Join(argv, " "))
	}
	code, out, errOut := o.run(t, "gc", "--yes", "--include-unregistered-procs")
	if code != 0 || errOut != "" {
		t.Fatalf("gc --yes --include-unregistered-procs: code=%d err=%q out=%q", code, errOut, out)
	}
	for _, proc := range []platform.OrphanProc{inCopy, inState, inScratch} {
		if !strings.Contains(out, "stopped process "+strconv.Itoa(proc.Pid)+" ("+proc.Name+")\n") {
			t.Fatalf("output = %q; want process %d stopped", out, proc.Pid)
		}
		if procAlive(t, o.env, proc.Pid) {
			t.Fatalf("process %d (%s) survived", proc.Pid, proc.Cwd)
		}
	}
	for _, proc := range []platform.OrphanProc{young, inRoot, inTree} {
		if !procAlive(t, o.env, proc.Pid) {
			t.Fatalf("process %d (%s) was stopped; a young orphan, the root and a worktree are never stopped", proc.Pid, proc.Cwd)
		}
	}
	for _, want := range []string{"listed only " + strconv.Itoa(inRoot.Pid) + "  ", "listed only " + strconv.Itoa(inTree.Pid) + "  "} {
		if !strings.Contains(out, want) {
			t.Fatalf("output = %q; want %q", out, want)
		}
	}
	if strings.Contains(out, "nothing to remove") || strings.Contains(out, strconv.Itoa(young.Pid)) {
		t.Fatalf("output = %q; the young orphan is not listed and the stops are not nothing", out)
	}
}

// TestGcOrphanRecheckKeepsAChangedProcess: right before the signal, a
// start identity or a cwd that no longer matches the listing keeps the
// process, with no signal.
func TestGcOrphanRecheckKeepsAChangedProcess(t *testing.T) {
	o := newOrphanScopeFixture(t)
	t.Run("identity changed", func(t *testing.T) {
		proc := o.realOrphan(t, o.copy, 3*time.Hour)
		proc.Identity = "another start"
		stubOrphans(t, []platform.OrphanProc{proc}, nil)
		code, out, errOut := o.run(t, "gc", "--yes", "--include-unregistered-procs")
		if code != 0 || errOut != "" || !strings.Contains(out, "kept process "+strconv.Itoa(proc.Pid)+" (it changed since the listing)\n") {
			t.Fatalf("gc: code=%d err=%q out=%q; want the kept line", code, errOut, out)
		}
		if !procAlive(t, o.env, proc.Pid) {
			t.Fatalf("process %d was stopped although its identity changed", proc.Pid)
		}
	})
	t.Run("cwd changed", func(t *testing.T) {
		proc := o.realOrphan(t, o.copy, 3*time.Hour)
		stubOrphans(t, []platform.OrphanProc{proc}, map[int]string{proc.Pid: o.outside})
		code, out, errOut := o.run(t, "gc", "--yes", "--include-unregistered-procs")
		if code != 0 || errOut != "" || !strings.Contains(out, "kept process "+strconv.Itoa(proc.Pid)+" (it changed since the listing)\n") {
			t.Fatalf("gc: code=%d err=%q out=%q; want the kept line", code, errOut, out)
		}
		if !procAlive(t, o.env, proc.Pid) {
			t.Fatalf("process %d was stopped although its cwd changed", proc.Pid)
		}
	})
}

// TestOrphanWithinComparesByComponent pins the prefix rule on its own: a
// sibling whose name extends the directory's is outside it.
func TestOrphanWithinComparesByComponent(t *testing.T) {
	dir := filepath.Join(string(filepath.Separator)+"a", "copy")
	cases := map[string]bool{
		dir:                                true,
		filepath.Join(dir, "x", "y"):       true,
		dir + "-2":                         false,
		filepath.Join(dir+"-2", "x"):       false,
		filepath.Dir(dir):                  false,
		filepath.Join(dir, "..", "copy-3"): false,
	}
	for path, want := range cases {
		if got := orphanWithin(path, dir); got != want {
			t.Fatalf("orphanWithin(%q, %q) = %v; want %v", path, dir, got, want)
		}
	}
}
