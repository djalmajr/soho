package wait

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// wait and status read the wait files in a loop while a wait run rewrites them
// (activity-at, stuck-hash, provider-cause, ...): they must see the old value
// or the new one, never an empty or mixed file.
func TestWaitFilesAreNeverReadPartial(t *testing.T) {
	short := "the assistant reported a question\n"
	long := "the assistant reported a question about the report path and the brief hash of worker-20261105T101005\n"
	sd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sd, "wait"), 0o755); err != nil {
		t.Fatal(err)
	}
	agent, name := "worker", "provider-cause"
	path := filepath.Join(sd, "wait", agent+"."+name)
	writeWaitFile(sd, agent, name, short)
	var writePanic string
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			if r := recover(); r != nil {
				writePanic = fmt.Sprintf("%v", r)
			}
		}()
		for i := 0; i < 500; i++ {
			value := long
			if i%2 == 1 {
				value = short
			}
			writeWaitFile(sd, agent, name, value)
		}
	}()
	reads, bad := 0, ""
	for running := true; running && bad == ""; {
		select {
		case <-done:
			running = false
		default:
		}
		raw, err := os.ReadFile(path)
		switch {
		case err != nil && runtime.GOOS == "windows":
			// Windows can refuse the open during the replacement; the next
			// poll reads the file.
		case err != nil:
			bad = err.Error()
		case string(raw) != short && string(raw) != long:
			bad = fmt.Sprintf("%q", raw)
		default:
			reads++
		}
		if runtime.GOOS == "windows" {
			// A tight loop would hold the file open and make every
			// replacement wait.
			time.Sleep(time.Millisecond)
		}
	}
	<-done
	if bad != "" {
		t.Fatalf("read a partial wait file: %s", bad)
	}
	if writePanic != "" {
		t.Fatal(writePanic)
	}
	if reads < 10 {
		t.Fatalf("only %d reads overlapped the writes, so this proved nothing", reads)
	}
}

// A write that cannot succeed must keep panicking: the callers rely on the
// panic surfacing the friction instead of leaving a half-written marker.
func TestWriteWaitFilePanicsOnWriteFailure(t *testing.T) {
	sd := t.TempDir() // no wait/ subdir: the write cannot succeed.
	defer func() {
		if recover() == nil {
			t.Fatal("writeWaitFile did not panic when the wait dir is missing")
		}
	}()
	writeWaitFile(sd, "worker", "blocked", "x")
}

// An empty value is a deliberate empty file (writeWaitFile(..., "blocked", "")):
// it keeps existing and stays empty.
func TestWriteWaitFileEmptyValueKeepsEmptyFile(t *testing.T) {
	sd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sd, "wait"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeWaitFile(sd, "worker", "blocked", "")
	raw, err := os.ReadFile(filepath.Join(sd, "wait", "worker.blocked"))
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 0 {
		t.Fatalf("an empty value must leave an empty file, got %q", raw)
	}
}
