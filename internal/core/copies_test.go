package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testCopyRow(path string) CopyRow {
	return CopyRow{Path: path, Owner: "-", Pane: "-", Created: "2026-10-03T00:00:00Z", Source: "-", Origin: "mutation-copy"}
}

func writeCopiesLock(t *testing.T, sd string, age time.Duration) string {
	t.Helper()
	lock := filepath.Join(sd, "copies.tsv.lock")
	if err := os.WriteFile(lock, []byte(fmt.Sprintf("%d %s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339))), 0o600); err != nil {
		t.Fatal(err)
	}
	if age > 0 {
		old := time.Now().Add(-age)
		if err := os.Chtimes(lock, old, old); err != nil {
			t.Fatal(err)
		}
	}
	return lock
}

// The review case: 32 concurrent registrations must leave 32 lines, no lost
// update.
func TestUpsertCopiesLocksTheRegistry(t *testing.T) {
	sd := t.TempDir()
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs <- UpsertCopies(sd, testCopyRow(fmt.Sprintf("/copy/%d", i)))
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	rows, err := ReadCopies(sd)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 32 {
		t.Fatalf("retained rows=%d, want 32 (no lost update under the lock)", len(rows))
	}
}

func TestLockTakesAStaleLockAndTimesOutOnALiveOne(t *testing.T) {
	t.Run("a lock older than 60 s is taken as abandoned", func(t *testing.T) {
		sd := t.TempDir()
		writeCopiesLock(t, sd, 2*time.Hour)
		if err := UpsertCopies(sd, testCopyRow("/copy/stale")); err != nil {
			t.Fatalf("the stale lock blocked the registration: %v", err)
		}
		if _, err := os.Stat(filepath.Join(sd, "copies.tsv.lock")); !os.IsNotExist(err) {
			t.Fatalf("the lock file remains after the unlock (stat err=%v)", err)
		}
	})
	t.Run("a live lock past the timeout comes back as the locked error and writes nothing", func(t *testing.T) {
		sd := t.TempDir()
		lock := writeCopiesLock(t, sd, 0)
		old := CopiesLockTimeout
		CopiesLockTimeout = 300 * time.Millisecond
		t.Cleanup(func() { CopiesLockTimeout = old })
		err := UpsertCopies(sd, testCopyRow("/copy/locked"))
		var locked *CopiesLockedError
		if !errors.As(err, &locked) || locked.Path != lock {
			t.Fatalf("err=%v, want the locked error for %s", err, lock)
		}
		want := fmt.Sprintf("copies: registry is locked by another soho (%s)", lock)
		if err.Error() != want {
			t.Fatalf("message=%q, want %q", err.Error(), want)
		}
		rows, err := ReadCopies(sd)
		if err != nil || len(rows) != 0 {
			t.Fatalf("rows=%#v err=%v, want nothing written", rows, err)
		}
		if _, err := os.Stat(lock); err != nil {
			t.Fatalf("the held lock was touched: %v", err)
		}
	})
}

// DropCopiesLines re-reads the registry under the lock: it drops exactly the
// given paths and keeps the lines that were added meanwhile.
func TestDropCopiesLinesKeepsTheLinesAddedMeanwhile(t *testing.T) {
	sd := t.TempDir()
	if err := UpsertCopies(sd, testCopyRow("/copy/one")); err != nil {
		t.Fatal(err)
	}
	if err := UpsertCopies(sd, testCopyRow("/copy/two")); err != nil {
		t.Fatal(err)
	}
	// A line added between the caller's snapshot and the re-write: it must
	// survive.
	if err := UpsertCopies(sd, testCopyRow("/copy/three")); err != nil {
		t.Fatal(err)
	}
	if err := DropCopiesLines(sd, map[string]bool{"/copy/one": true}); err != nil {
		t.Fatal(err)
	}
	rows, err := ReadCopies(sd)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, row := range rows {
		got[row.Path] = true
	}
	if len(rows) != 2 || !got["/copy/two"] || !got["/copy/three"] {
		t.Fatalf("rows=%#v, want /copy/two and /copy/three only", rows)
	}
}

// A registry an earlier binary wrote (six-column header and lines) still
// reads: the task and report columns come back empty, and the next write
// keeps the six-name header and adds "-" in the two unnamed columns.
func TestReadCopiesAcceptsTheSixColumnRegistry(t *testing.T) {
	sd := t.TempDir()
	legacy := "path\towner\tpane\tcreated\tsource\torigin\n/copy/a\tworker\tp1\t2026-10-03T00:00:00Z\t-\tadd\n"
	if err := os.WriteFile(CopiesFile(sd), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	rows, err := ReadCopies(sd)
	want := CopyRow{Path: "/copy/a", Owner: "worker", Pane: "p1", Created: "2026-10-03T00:00:00Z", Source: "-", Origin: "add"}
	if err != nil || len(rows) != 1 || rows[0] != want {
		t.Fatalf("rows=%#v err=%v; want %#v", rows, err, want)
	}
	if err := UpsertCopies(sd, testCopyRow("/copy/b")); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(CopiesFile(sd))
	if err != nil {
		t.Fatal(err)
	}
	wantRaw := "path\towner\tpane\tcreated\tsource\torigin\n" +
		"/copy/a\tworker\tp1\t2026-10-03T00:00:00Z\t-\tadd\t-\t-\n" +
		"/copy/b\t-\t-\t2026-10-03T00:00:00Z\t-\tmutation-copy\t-\t-\n"
	if string(raw) != wantRaw {
		t.Fatalf("registry=%q; want %q", raw, wantRaw)
	}
}

func TestCopiesTaskColumnsRoundTrip(t *testing.T) {
	sd := t.TempDir()
	row := testCopyRow("/copy/a")
	row.Task, row.Report = "/r/worker.current.md", "/r/task-1.md"
	if err := WriteCopies(sd, []CopyRow{row}); err != nil {
		t.Fatal(err)
	}
	rows, err := ReadCopies(sd)
	if err != nil || len(rows) != 1 || rows[0] != row {
		t.Fatalf("rows=%#v err=%v; want %#v", rows, err, row)
	}
}

// legacyReadCopies is ReadCopies as the binaries before the task and report
// columns have it (base 22266df): it skips only the six-column header and
// keeps the first six fields of a line.
func legacyReadCopies(raw string) ([]CopyRow, error) {
	out := []CopyRow{}
	for _, line := range strings.Split(raw, "\n") {
		if line == "" || line == "path\towner\tpane\tcreated\tsource\torigin" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 6 {
			return nil, fmt.Errorf("copies.tsv: malformed line %q", line)
		}
		out = append(out, CopyRow{Path: fields[0], Owner: fields[1], Pane: fields[2], Created: fields[3], Source: fields[4], Origin: fields[5]})
	}
	return out, nil
}

// The earlier parser reads the new registry with no extra line: the header
// is the six-name one it skips, and it keeps the first six fields of every
// line, the same rows the new parser reads apart from task and report.
func TestTheSixColumnParserReadsTheNewRegistry(t *testing.T) {
	sd := t.TempDir()
	a := testCopyRow("/copy/a")
	a.Owner, a.Task, a.Report = "worker", "/r/worker.current.md", "/r/task-1.md"
	b := testCopyRow("/copy/b")
	if err := WriteCopies(sd, []CopyRow{a, b}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(CopiesFile(sd))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(raw), "path\towner\tpane\tcreated\tsource\torigin\n") {
		t.Fatalf("registry=%q; want the six-name header first", raw)
	}
	legacy, err := legacyReadCopies(string(raw))
	if err != nil {
		t.Fatalf("the six-column parser failed: %v", err)
	}
	current, err := ReadCopies(sd)
	if err != nil {
		t.Fatal(err)
	}
	if len(legacy) != 2 || len(current) != 2 {
		t.Fatalf("legacy rows=%#v current rows=%#v; want two rows each, no extra line", legacy, current)
	}
	for i, row := range current {
		six := row
		six.Task, six.Report = "", ""
		if legacy[i] != six {
			t.Fatalf("row %d: legacy=%#v; want the six fields %#v", i, legacy[i], six)
		}
	}
	if current[0].Task != a.Task || current[0].Report != a.Report || current[1].Task != "-" || current[1].Report != "-" {
		t.Fatalf("current rows=%#v; want task and report read back", current)
	}
}

// A registry the intermediate build wrote with the eight-name header reads
// with no extra line, and the next write puts the six-name header back.
func TestReadCopiesSkipsTheEightNameHeader(t *testing.T) {
	sd := t.TempDir()
	raw := "path\towner\tpane\tcreated\tsource\torigin\ttask\treport\n" +
		"/copy/a\tworker\tp1\t2026-10-03T00:00:00Z\t-\tadd\t/r/worker.current.md\t/r/task-1.md\n"
	if err := os.WriteFile(CopiesFile(sd), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	rows, err := ReadCopies(sd)
	want := CopyRow{Path: "/copy/a", Owner: "worker", Pane: "p1", Created: "2026-10-03T00:00:00Z", Source: "-", Origin: "add", Task: "/r/worker.current.md", Report: "/r/task-1.md"}
	if err != nil || len(rows) != 1 || rows[0] != want {
		t.Fatalf("rows=%#v err=%v; want only %#v", rows, err, want)
	}
	if err := WriteCopies(sd, rows); err != nil {
		t.Fatal(err)
	}
	rewritten, err := os.ReadFile(CopiesFile(sd))
	if err != nil {
		t.Fatal(err)
	}
	if want := "path\towner\tpane\tcreated\tsource\torigin\n/copy/a\tworker\tp1\t2026-10-03T00:00:00Z\t-\tadd\t/r/worker.current.md\t/r/task-1.md\n"; string(rewritten) != want {
		t.Fatalf("registry=%q; want %q", rewritten, want)
	}
}
