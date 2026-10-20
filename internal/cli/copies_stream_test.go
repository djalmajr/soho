package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

type gcStreamOutput struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	changed chan struct{}
}

func (o *gcStreamOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	n, err := o.buf.Write(p)
	o.mu.Unlock()
	select {
	case o.changed <- struct{}{}:
	default:
	}
	return n, err
}

func (o *gcStreamOutput) text() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.String()
}

func (o *gcStreamOutput) wait(t *testing.T, text string) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for !strings.Contains(o.text(), text) {
		select {
		case <-o.changed:
		case <-deadline.C:
			t.Fatalf("stdout never contained %q; got %q", text, o.text())
		}
	}
}

// Run through the same public entry point as copiesFixture.run, but keep
// both output streams readable while the command is still running.
func gcStreamRun(t *testing.T, f *copiesFixture, args ...string) (*gcStreamOutput, *gcStreamOutput, <-chan int) {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(f.cwd); err != nil {
		t.Fatal(err)
	}
	out := &gcStreamOutput{changed: make(chan struct{}, 1)}
	errOut := &gcStreamOutput{changed: make(chan struct{}, 1)}
	oldOut, oldErr := platform.Stdout, platform.Stderr
	platform.Stdout, platform.Stderr = out, errOut
	done := make(chan int, 1)
	finished := make(chan struct{})
	go func() {
		done <- Run(append([]string{"gc"}, args...), f.env)
		close(finished)
	}()
	t.Cleanup(func() {
		select {
		case <-finished:
		case <-time.After(10 * time.Second):
			t.Fatal("gc did not finish after its barriers were released")
		}
		platform.Stdout, platform.Stderr = oldOut, oldErr
		_ = os.Chdir(cwd)
	})
	return out, errOut, done
}

func gcSizeSeam(t *testing.T, measure func(string) int64) {
	t.Helper()
	old := gcCopyDirSize
	gcCopyDirSize = measure
	t.Cleanup(func() { gcCopyDirSize = old })
}

func gcStreamFixture(t *testing.T, registered, unregistered int) (*copiesFixture, []string) {
	t.Helper()
	f := gcEmptyFixture(t)
	old := platform.Now().Add(-3 * time.Hour)
	var paths []string
	var rows []core.CopyRow
	for i := range registered + unregistered {
		name := fmt.Sprintf("registered-%02d", registered-i)
		if i >= registered {
			name = fmt.Sprintf("soho-mutation-%02d", i-registered)
		}
		path := filepath.Join(f.tmp, name)
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
		if i < registered {
			rows = append(rows, core.CopyRow{Path: path, Owner: "ghost", Pane: "-", Created: core.FrictionISO(old), Source: "-", Origin: "mutation-copy"})
		}
	}
	f.writeCopies(t, rows)
	return f, paths
}

func TestGcMeasurementOrderAndFormat(t *testing.T) {
	f, paths := gcStreamFixture(t, 5, 0)
	second := make(chan struct{})
	gcSizeSeam(t, func(path string) int64 {
		if path == paths[0] {
			select {
			case <-second:
				time.Sleep(50 * time.Millisecond)
			case <-time.After(5 * time.Second):
				t.Error("the second measurement did not run concurrently")
			}
		}
		if path == paths[1] {
			close(second)
		}
		for i, p := range paths {
			if path == p {
				return int64(i + 1)
			}
		}
		t.Errorf("unexpected measurement: %s", path)
		return 0
	})
	code, out, errOut := f.run(t, f.env, "gc")
	want := "resource pressure: none (disk 60% free, swap 30% used)\n"
	for i, path := range paths {
		want += fmt.Sprintf("would remove %s  %d B  ghost  3h\n", path, i+1)
	}
	want += "total: 15 B\nrun 'soho gc --yes' to remove them\n"
	if code != 0 || errOut != "" || out != want {
		t.Fatalf("code=%d stderr=%q\nstdout=%q\nwant=%q", code, errOut, out, want)
	}
}

func TestGcMeasurementStreamsInOrder(t *testing.T) {
	for _, unregistered := range []bool{false, true} {
		t.Run(fmt.Sprintf("unregistered=%t", unregistered), func(t *testing.T) {
			registered, unreg := 5, 0
			if unregistered {
				registered, unreg = 0, 5
			}
			f, paths := gcStreamFixture(t, registered, unreg)
			release := make(chan struct{})
			third := make(chan struct{})
			gcSizeSeam(t, func(path string) int64 {
				if path == paths[2] {
					close(third)
					<-release
				}
				return 7
			})
			out, errOut, done := gcStreamRun(t, f)
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			select {
			case <-third:
			case <-time.After(5 * time.Second):
				t.Fatal("third measurement never started")
			}
			line := func(path string) string { return "would remove " + path + "  7 B  ghost  3h\n" }
			if unregistered {
				line = func(path string) string { return path + "  7 B  3h  no origin marker\n" }
			}
			out.wait(t, line(paths[1]))
			prefix := "resource pressure: none (disk 60% free, swap 30% used)\n"
			if unregistered {
				prefix += gcUnregisteredHeader + "\n"
			}
			prefix += line(paths[0]) + line(paths[1])
			if got := out.text(); got != prefix {
				t.Fatalf("while third measurement is blocked: got %q; want %q", got, prefix)
			}
			unblock()
			if code := <-done; code != 0 || errOut.text() != "" {
				t.Fatalf("code=%d stderr=%q", code, errOut.text())
			}
		})
	}
}

// The limit is proven with barriers, not a stopwatch: four measurements
// start and stay held, a fifth does not start while they are held, and
// releasing one lets the fifth in. The timeouts only guard against a hang,
// so a slow runner cannot fail a correct pool.
func TestGcMeasurementParallelLimit(t *testing.T) {
	f, _ := gcStreamFixture(t, 8, 0)
	var active, peak, calls atomic.Int32
	entered := make(chan struct{}, 8)
	release := make(chan struct{}, 8)
	gcSizeSeam(t, func(string) int64 {
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old; old = peak.Load() {
			if peak.CompareAndSwap(old, n) {
				break
			}
		}
		calls.Add(1)
		entered <- struct{}{}
		<-release
		return 1
	})
	out, errOut, done := gcStreamRun(t, f)
	// A failed check below must not leave measurements held: the cleanup
	// releases every one before gcStreamRun's cleanup waits for gc.
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	waitEntered := func(what string) {
		t.Helper()
		select {
		case <-entered:
		case <-time.After(30 * time.Second):
			t.Fatalf("%s did not start (calls=%d active=%d)", what, calls.Load(), active.Load())
		}
	}
	for i := 1; i <= 4; i++ {
		waitEntered(fmt.Sprintf("measurement %d", i))
	}
	select {
	case <-entered:
		t.Fatalf("a fifth measurement started while four were held (active=%d)", active.Load())
	case <-time.After(200 * time.Millisecond):
	}
	if n := active.Load(); n != 4 {
		t.Fatalf("active=%d while four measurements are held; want 4", n)
	}
	release <- struct{}{}
	waitEntered("the fifth measurement, after one was released")
	for i := 0; i < 7; i++ {
		release <- struct{}{}
	}
	for i := 6; i <= 8; i++ {
		waitEntered(fmt.Sprintf("measurement %d", i))
	}
	var code int
	select {
	case code = <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("gc did not finish after every measurement was released")
	}
	if code != 0 || errOut.text() != "" || calls.Load() != 8 || active.Load() != 0 || peak.Load() != 4 {
		t.Fatalf("code=%d calls=%d active=%d peak=%d GOOS=%s PATH=%q TMPDIR=%q stdout=%q stderr=%q", code, calls.Load(), active.Load(), peak.Load(), platform.Current(), f.env.Get("PATH"), f.tmp, out.text(), errOut.text())
	}
}

func TestGcMeasurementWarningThreshold(t *testing.T) {
	for _, count := range []int{19, 20} {
		for _, yes := range []bool{false, true} {
			t.Run(fmt.Sprintf("count=%d/yes=%t", count, yes), func(t *testing.T) {
				// Both sources count; a missing registry row does not need a walk.
				f, _ := gcStreamFixture(t, count-2, 2)
				rows := f.copiesRows(t)
				rows = append(rows, core.CopyRow{Path: filepath.Join(f.tmp, "missing"), Owner: "ghost", Pane: "-", Created: core.FrictionISO(platform.Now().Add(-3 * time.Hour))})
				f.writeCopies(t, rows)
				want := ""
				if count >= 20 {
					want = fmt.Sprintf("gc: measuring the size of %d copies; each line appears as soon as its copy is measured\n", count)
				}
				// Run synchronously so the seam can assert stderr before the
				// first walk, with no scheduling assumptions about the caller.
				var calls atomic.Int32
				var first sync.Once
				gcSizeSeam(t, func(string) int64 {
					calls.Add(1)
					first.Do(func() {
						if got := platform.Stderr.(*bytes.Buffer).String(); got != want {
							t.Errorf("before first measurement stderr=%q; want %q", got, want)
						}
						if got := platform.Stdout.(*bytes.Buffer).String(); got != "resource pressure: none (disk 60% free, swap 30% used)\n" {
							t.Errorf("before first measurement stdout=%q", got)
						}
					})
					return 1
				})
				args := []string{"gc"}
				if yes {
					args = append(args, "--yes")
				}
				code, out, errOut := f.run(t, f.env, args...)
				if code != 0 || errOut != want || calls.Load() != int32(count) {
					t.Fatalf("code=%d calls=%d stderr=%q want=%q stdout=%q", code, calls.Load(), errOut, want, out)
				}
			})
		}
	}
}

func TestGcYesStreamsAfterEachRemoval(t *testing.T) {
	f, paths := gcStreamFixture(t, 3, 0)
	gcSizeSeam(t, func(string) int64 { return 7 })
	release, second := make(chan struct{}), make(chan struct{})
	oldHook := removeCopyHook
	removeCopyHook = func(path string) {
		if path == paths[1] {
			close(second)
			<-release
		}
	}
	t.Cleanup(func() { removeCopyHook = oldHook })
	out, errOut, done := gcStreamRun(t, f, "--yes")
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	select {
	case <-second:
	case <-time.After(5 * time.Second):
		t.Fatal("second removal never started")
	}
	want := "resource pressure: none (disk 60% free, swap 30% used)\nremoved " + paths[0] + "  7 B\n"
	if got := out.text(); got != want {
		t.Fatalf("before second removal stdout=%q; want %q", got, want)
	}
	if _, err := os.Stat(paths[0]); !os.IsNotExist(err) {
		t.Fatalf("first copy still exists: %v", err)
	}
	if _, err := os.Stat(paths[1]); err != nil {
		t.Fatalf("second copy changed before removal: %v", err)
	}
	unblock()
	if code := <-done; code != 0 || errOut.text() != "" {
		t.Fatalf("code=%d stderr=%q", code, errOut.text())
	}
	for _, path := range paths[1:] {
		want += "removed " + path + "  7 B\n"
	}
	want += "freed: 21 B\n"
	if got := out.text(); got != want || len(f.copiesRows(t)) != 0 {
		t.Fatalf("stdout=%q want=%q registry=%#v", got, want, f.copiesRows(t))
	}
	for _, path := range paths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("copy not removed: %s: %v", path, err)
		}
	}
}
