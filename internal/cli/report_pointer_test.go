package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// wait and status read the pointer in a loop while a dispatch replaces it:
// they must see the old value or the new one, never an empty or mixed file.
func TestReportPointerIsNeverReadPartial(t *testing.T) {
	path := filepath.Join(t.TempDir(), "last-report-worker")
	short := "/state/reports/worker-fixture.md\n"
	long := "/state/reports/worker-20261104T173701-000000000000000000633.md\n"
	if err := writeReportPointer(path, short); err != nil {
		t.Fatal(err)
	}
	var writeErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 500; i++ {
			value := long
			if i%2 == 1 {
				value = short
			}
			if writeErr = writeReportPointer(path, value); writeErr != nil {
				return
			}
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
		t.Fatalf("read a partial pointer: %s", bad)
	}
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	if reads < 10 {
		t.Fatalf("only %d reads overlapped the writes, so this proved nothing", reads)
	}
}
