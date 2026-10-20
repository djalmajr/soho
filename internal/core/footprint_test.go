package core

import (
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/platform"
)

// footprintHelperRoleEnv is the role of the re-executed test binary that
// stands in for a registered worker process: sleeper blocks forever (until
// killed), exit returns right away.
const footprintHelperRoleEnv = "SOHO_FOOTPRINT_HELPER_ROLE"

// TestFootprintHelper is the re-executed helper entry: with the role env
// set it runs the role and exits; in the main test run the env is unset
// and the function returns without running anything.
func TestFootprintHelper(t *testing.T) {
	switch os.Getenv(footprintHelperRoleEnv) {
	case "":
		return
	case "sleeper":
		// Block inside a real blocking syscall (a read on a pipe whose
		// write end this process keeps open, so the read never returns);
		// the default SIGTERM/SIGKILL still stop it.
		r, w, err := os.Pipe()
		if err != nil {
			os.Exit(1)
		}
		_ = w
		buf := make([]byte, 1)
		_, _ = r.Read(buf)
		os.Exit(1) // unreachable
	case "exit":
		os.Exit(0)
	}
	t.Fatal("unexpected helper role")
}

// startFootprintHelper starts the re-executed test binary in a role.
func startFootprintHelper(t *testing.T, role string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestFootprintHelper$", "-test.count=1")
	cmd.Env = append(os.Environ(), footprintHelperRoleEnv+"="+role)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return cmd
}

// waitRunning polls the by-pid read until the pid reads as running (a just
// started process can miss the first ps snapshot).
func waitRunning(t *testing.T, pid int, env platform.Env) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		started, _, live := platform.ReadProcFull(pid, env)
		if live == platform.ProcRunning {
			return started
		}
		if time.Now().After(deadline) {
			t.Fatalf("child %d never read as running (last liveness %v)", pid, live)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// seedFile writes a file of exactly n bytes under a new directory tree.
func seedFile(t *testing.T, path string, n int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, n), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestMeasureFootprintCopies(t *testing.T) {
	t.Run("the existing copies count, the vanished one does not, the inside copy counts once", func(t *testing.T) {
		tmp := t.TempDir()
		env := platform.Env{"TMPDIR": tmp, "PATH": os.Getenv("PATH")}
		stateDir := filepath.Join(tmp, "state")
		if err := os.MkdirAll(stateDir, 0o700); err != nil {
			t.Fatal(err)
		}
		c1 := filepath.Join(tmp, "copies", "c1")
		c2 := filepath.Join(tmp, "copies", "c2")
		c3 := filepath.Join(tmp, "copies", "c3") // registered but gone
		seedFile(t, filepath.Join(c1, "f.bin"), 3072)
		seedFile(t, filepath.Join(c2, "f.bin"), 2048)
		cIn := filepath.Join(stateDir, "copies", "in") // registered inside the state dir
		seedFile(t, filepath.Join(cIn, "f.bin"), 4096)
		seedFile(t, filepath.Join(stateDir, "note.txt"), 512)
		if err := WriteCopies(stateDir, []CopyRow{
			{Path: c1, Owner: "a", Pane: "p", Created: "2026-01-01T00:00:00Z", Source: "s", Origin: "add"},
			{Path: c2, Owner: "b", Pane: "p", Created: "2026-01-01T00:00:00Z", Source: "s", Origin: "add"},
			{Path: c3, Owner: "c", Pane: "p", Created: "2026-01-01T00:00:00Z", Source: "s", Origin: "add"},
			{Path: cIn, Owner: "d", Pane: "p", Created: "2026-01-01T00:00:00Z", Source: "s", Origin: "add"},
		}); err != nil {
			t.Fatal(err)
		}
		seedFile(t, filepath.Join(tmp, "soho", "ws", "reports", "r.md"), 128)

		fp := MeasureFootprint(stateDir, "ws", env)
		if fp.Copies != 3 {
			t.Fatalf("copies=%d, want 3 (the vanished copy does not count)", fp.Copies)
		}
		if fp.CopiesBytes != 3072+2048+4096 {
			t.Fatalf("copies bytes=%d, want %d", fp.CopiesBytes, 3072+2048+4096)
		}
		info, err := os.Stat(filepath.Join(stateDir, "copies.tsv"))
		if err != nil {
			t.Fatal(err)
		}
		if fp.StateBytes != 512+info.Size() {
			t.Fatalf("state bytes=%d, want %d (the inside copy is kept out of the state)", fp.StateBytes, 512+info.Size())
		}
		if fp.ScratchBytes != 128 {
			t.Fatalf("scratch bytes=%d, want 128", fp.ScratchBytes)
		}
		if fp.Total != 3072+2048+4096+512+info.Size()+128 {
			t.Fatalf("total=%d, want %d (the inside copy counts once, as a copy)", fp.Total, 3072+2048+4096+512+info.Size()+128)
		}
		if fp.Running != 0 || fp.RSS != 0 || fp.Unreadable != 0 {
			t.Fatalf("processes with no registry: running=%d rss=%d unreadable=%d", fp.Running, fp.RSS, fp.Unreadable)
		}
	})
	t.Run("an empty state dir and an empty workspace measure zeros", func(t *testing.T) {
		env := platform.Env{"TMPDIR": t.TempDir()}
		fp := MeasureFootprint("", "", env)
		if fp.Copies != 0 || fp.CopiesBytes != 0 || fp.StateBytes != 0 || fp.ScratchBytes != 0 || fp.Total != 0 || fp.Running != 0 {
			t.Fatalf("empty measurement is not zero: %+v", fp)
		}
	})
}

func TestMeasureFootprintProcesses(t *testing.T) {
	env := platform.Env{"TMPDIR": t.TempDir(), "PATH": os.Getenv("PATH")}
	stateDir := t.TempDir()

	cmd := startFootprintHelper(t, "sleeper")
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	started := waitRunning(t, pid, env)
	if err := WriteProcs(stateDir, []ProcRow{{Pid: pid, Started: started, Name: "sleeper", Owner: "a", Pane: "p", Created: "2026-01-01T00:00:00Z"}}); err != nil {
		t.Fatal(err)
	}
	fp := MeasureFootprint(stateDir, "ws", env)
	if fp.Running != 1 {
		t.Fatalf("running=%d, want 1 (the child with its real identity)", fp.Running)
	}
	if fp.RSS <= 0 {
		t.Fatalf("rss=%d unreadable=%d: a running child must read an RSS above zero", fp.RSS, fp.Unreadable)
	}
	if fp.Unreadable != 0 {
		t.Fatalf("unreadable=%d, want 0", fp.Unreadable)
	}

	// A gone line does not count: a child that has already exited.
	gone := startFootprintHelper(t, "exit")
	_ = gone.Wait()
	gonePid := gone.Process.Pid
	rows, err := ReadProcs(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteProcs(stateDir, append(rows, ProcRow{Pid: gonePid, Started: "gone-start", Name: "exit", Owner: "b", Pane: "p", Created: "2026-01-01T00:00:00Z"})); err != nil {
		t.Fatal(err)
	}
	fp = MeasureFootprint(stateDir, "ws", env)
	if fp.Running != 1 {
		t.Fatalf("running=%d, want 1 (the gone line does not count)", fp.Running)
	}
}

func TestMeasureFootprintUnreadableRSS(t *testing.T) {
	// The seam: an RSS that cannot be read counts as unreadable, and the
	// process still counts.
	old := readRSS
	readRSS = func(pid int, env platform.Env) (int64, bool) { return 0, false }
	defer func() { readRSS = old }()

	env := platform.Env{"TMPDIR": t.TempDir(), "PATH": os.Getenv("PATH")}
	stateDir := t.TempDir()
	cmd := startFootprintHelper(t, "sleeper")
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	started := waitRunning(t, pid, env)
	if err := WriteProcs(stateDir, []ProcRow{{Pid: pid, Started: started, Name: "sleeper", Owner: "a", Pane: "p", Created: "2026-01-01T00:00:00Z"}}); err != nil {
		t.Fatal(err)
	}
	fp := MeasureFootprint(stateDir, "ws", env)
	if fp.Running != 1 || fp.Unreadable != 1 || fp.RSS != 0 {
		t.Fatalf("running=%d unreadable=%d rss=%d, want 1/1/0", fp.Running, fp.Unreadable, fp.RSS)
	}
}

func TestFootprintDiskText(t *testing.T) {
	t.Run("the disk part, plural and singular", func(t *testing.T) {
		fp := &Footprint{Copies: 2, CopiesBytes: 3072, StateBytes: 512, ScratchBytes: 0, Total: 3584}
		if got := FootprintDiskText(fp); got != "3.5 KB logical size (copies 3.0 KB in 2, state 512 B, scratch 0 B)" {
			t.Fatalf("disk text=%q", got)
		}
		fp1 := &Footprint{Copies: 1, CopiesBytes: 100, StateBytes: 200, ScratchBytes: 300, Total: 600}
		if got := FootprintDiskText(fp1); got != "600 B logical size (copies 100 B in 1, state 200 B, scratch 300 B)" {
			t.Fatalf("singular disk text=%q", got)
		}
	})
	t.Run("the human sizes keep the copies.go base (1 GB = 1024^3)", func(t *testing.T) {
		for n, want := range map[int64]string{
			0:             "0 B",
			1023:          "1023 B",
			104857600:     "100.0 MB",
			5 << 30:       "5.0 GB",
			3 * (1 << 40): "3.0 TB",
		} {
			if got := HumanSize(n); got != want {
				t.Fatalf("HumanSize(%d)=%q, want %q", n, got, want)
			}
		}
	})
}

func TestDirSizeWalksRegularFilesOnly(t *testing.T) {
	root := t.TempDir()
	seedFile(t, filepath.Join(root, "a", "f1"), 1000)
	seedFile(t, filepath.Join(root, "f2"), 500)
	if got := DirSize(root); got != 1500 {
		t.Fatalf("DirSize=%d, want 1500", got)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(filepath.Join(root, "f2"), link); err != nil {
		t.Skipf("symlinks unsupported here: %v", err)
	}
	if got := DirSize(root); got != 1500 {
		t.Fatalf("DirSize follows the symlink=%d, want 1500 (no symlink following)", got)
	}
	if got := DirSize(filepath.Join(root, "missing")); got != 0 {
		t.Fatalf("DirSize(missing)=%d, want 0", got)
	}
}

func TestDirSizeHardlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows keeps the existing logical size behavior")
	}
	root := t.TempDir()
	original := filepath.Join(root, "original")
	seedFile(t, original, 1<<20)
	for _, name := range []string{"link-a", "link-b", "link-c"} {
		if err := os.Link(original, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if got := DirSize(root); got != 1<<20 {
			t.Fatalf("measurement %d: got=%d want=%d for one inode with four names", i, got, 1<<20)
		}
	}
	// Equal bytes in another inode must still count.
	seedFile(t, filepath.Join(root, "independent"), 1<<20)
	if got := DirSize(root); got != 2<<20 {
		t.Fatalf("independent inode: got=%d want=%d", got, 2<<20)
	}
}

// A footprint_warn_gb too large to express in bytes never reads as reached:
// the GB-to-bytes shift must not overflow into a negative bound.
func TestFootprintLimitReached(t *testing.T) {
	cases := []struct {
		total, limit int64
		want         bool
	}{
		{5 << 30, 5, true},
		{5<<30 - 1, 5, false},
		{1 << 40, 0, false},
		{1 << 40, -1, false},
		{1 << 40, 99999999999, false},
		{math.MaxInt64, math.MaxInt64, false},
	}
	for _, tc := range cases {
		if got := FootprintLimitReached(tc.total, tc.limit); got != tc.want {
			t.Fatalf("FootprintLimitReached(%d, %d) = %v, want %v", tc.total, tc.limit, got, tc.want)
		}
	}
}
