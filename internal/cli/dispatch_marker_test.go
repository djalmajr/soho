package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// waitMarkerRace is the read/write overlap of TestReportPointerIsNeverReadPartial
// for a named marker writer: a loop of replacements against a tight read loop,
// which must only ever see the whole old value or the whole new one.
func waitMarkerRace(t *testing.T, write func(path, value string) error, path, short, long string) {
	t.Helper()
	if err := write(path, short); err != nil {
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
			if writeErr = write(path, value); writeErr != nil {
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
		t.Fatalf("read a partial marker: %s", bad)
	}
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	if reads < 10 {
		t.Fatalf("only %d reads overlapped the writes, so this proved nothing", reads)
	}
}

// wait and the next dispatch read the not-received marker while a dispatch
// replaces it: they must see the old value or the new one, never an empty or
// mixed file (an empty marker would look like a sequence that never changed).
func TestNotReceivedMarkerIsNeverReadPartial(t *testing.T) {
	short := "1764999999 2 /state/ws/briefs/worker-fixture.md\n"
	long := "1764999999 2 /state/ws/briefs/worker-20261105T101005-000000000000000000633.md\n"
	waitMarkerRace(t, writeNotReceivedMarker, filepath.Join(t.TempDir(), "worker.not-received"), short, long)
}

// The queued marker and the task title are read by wait, status and the pane
// title while a dispatch rewrites them.
func TestQueuedAndTaskTitleAreNeverReadPartial(t *testing.T) {
	markerShort := "1764999999 2 /state/ws/briefs/worker-fixture.md\n"
	markerLong := "1764999999 2 /state/ws/briefs/worker-20261105T101005-000000000000000000633.md\n"
	for _, tc := range []struct {
		name, rel, short, long string
		write                  func(path, value string) error
	}{
		{".queued", "worker.queued", markerShort, markerLong, writeQueuedMarker},
		{"task-<agente>", "task-worker", "implementer: worker-fixture.md\n", "implementer: worker-20261105T101005-000000000000000000633.md\n", writeTaskTitleFile},
	} {
		t.Run(tc.name, func(t *testing.T) {
			waitMarkerRace(t, tc.write, filepath.Join(t.TempDir(), tc.rel), tc.short, tc.long)
		})
	}
}
