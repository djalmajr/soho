package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The schema is intentionally asserted independently of the writer: compact
// version-1 JSON comments, with only names and the opaque pane/started tuple.
const dja659HistoryPrefix = "# soho-rename "

func dja659HistoryRow(name, pane, started string) string {
	return strings.Join([]string{name, pane, "codex", "worker", "openai", "1", "", started, "", "ask", "worker", "build", "", "", "high", "none", "4100", "future"}, "\t")
}

func dja659HistoryFile(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "agents.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestDJA659HistoryPublicationAndBytes(t *testing.T) {
	dir := t.TempDir()
	header := "# original header\tunknown\n# unrelated comment\n"
	first := dja659HistoryRow("old", "earlier-pane", "later-clock")
	winner := dja659HistoryRow("old", "selected-pane", "earlier-clock")
	keep := dja659HistoryRow("other", "other-pane", "stamp")
	if err := os.WriteFile(filepath.Join(dir, "agents.tsv"), []byte(header+first+"\n"+keep+"\n"+winner+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	RosterRename(dir, "old", "middle")
	RosterRename(dir, "middle", "new")
	if got := RosterLine(dir, "new"); got != "new"+strings.TrimPrefix(winner, "old") {
		t.Fatalf("last winning row/columns changed: %q", got)
	}
	raw := dja659HistoryFile(t, dir)
	if !strings.HasPrefix(raw, header) || !strings.Contains(raw, keep+"\n") {
		t.Fatalf("unrelated bytes changed: %q", raw)
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(raw, "\n") {
		if !strings.HasPrefix(line, dja659HistoryPrefix) {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, dja659HistoryPrefix)), &record); err != nil {
			t.Fatal(err)
		}
		if len(record) != 5 || record["v"] != float64(1) || record["to"] != "new" || record["pane"] != "selected-pane" || record["started"] != "earlier-clock" {
			t.Fatalf("schema/flattening/identity: %#v", record)
		}
		from, _ := record["from"].(string)
		seen[from] = true
	}
	if !seen["old"] || !seen["middle"] || len(seen) != 2 {
		t.Fatalf("effective mappings=%v", seen)
	}
	// Ordinary append must retain ALL existing bytes, including metadata.
	row := dja659HistoryRow("unrelated", "p", "s")
	fields := strings.Split(row, "\t")
	fields[16] = "" // No schema-header upgrade in the ordinary append case.
	row = strings.Join(fields, "\t")
	RosterAppend(dir, strings.Split(row, "\t"))
	if got := dja659HistoryFile(t, dir); got != raw+row+"\n" {
		t.Fatalf("ordinary append rewrote bytes: %q", got)
	}
}

func TestDJA659HistoryCountAndMetadataBound(t *testing.T) {
	dir := t.TempDir()
	RosterAppend(dir, strings.Split(dja659HistoryRow("n0", "p", "opaque"), "\t"))
	for i := 1; i <= 70; i++ {
		RosterRename(dir, fmt.Sprint("n", i-1), fmt.Sprint("n", i))
	}
	count, size := 0, 0
	for _, line := range strings.Split(dja659HistoryFile(t, dir), "\n") {
		if strings.HasPrefix(line, dja659HistoryPrefix) {
			count++
			size += len(line) + 1
		}
	}
	if count != 64 || size > 64<<10 {
		t.Fatalf("mappings=%d metadata bytes=%d", count, size)
	}
}

func TestDJA659HistoryConditionalPublication(t *testing.T) {
	for _, scenario := range []string{"unchanged", "replaced", "missing", "destination-conflict"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			selected := dja659HistoryRow("old", "selected", "later-clock")
			RosterAppend(dir, strings.Split(selected, "\t"))
			switch scenario {
			case "replaced":
				RosterAppend(dir, strings.Split(dja659HistoryRow("old", "replacement", "earlier-clock"), "\t"))
			case "missing":
				RosterRemove(dir, "old")
			case "destination-conflict":
				RosterAppend(dir, strings.Split(dja659HistoryRow("new", "stale-destination", "stamp"), "\t"))
			}
			before := dja659HistoryFile(t, dir)
			renamed, err := RosterRenameIf(dir, "old", "new", selected)
			if scenario == "unchanged" {
				if !renamed || err != nil || RosterRenameHint(dir, "old") != "new" {
					t.Fatalf("control: renamed=%v err=%v", renamed, err)
				}
			} else {
				if renamed || dja659HistoryFile(t, dir) != before || RosterRenameHint(dir, "old") != "" {
					t.Fatalf("conflict published: renamed=%v err=%v", renamed, err)
				}
				if scenario == "destination-conflict" && err == nil {
					t.Fatal("destination conflict lost its cause")
				}
			}
		})
	}
}

func TestDJA659HistoryAppendInvalidationBytes(t *testing.T) {
	dir := t.TempDir()
	RosterAppend(dir, strings.Split(dja659HistoryRow("old", "p", "stamp"), "\t"))
	RosterRename(dir, "old", "new")
	before := dja659HistoryFile(t, dir)
	row := dja659HistoryRow("old", "other", "earlier-clock")
	RosterAppend(dir, strings.Split(row, "\t"))
	after := dja659HistoryFile(t, dir)
	want := before + `# soho-rename {"v":1,"invalidate":"old"}` + "\n" + row + "\n"
	if after != want {
		t.Fatalf("recreation did not append only its invalidation and row: %q", after)
	}
	RosterRemove(dir, "old")
	if RosterRenameHint(dir, "old") != "" {
		t.Fatal("recreation then release revived history")
	}
	if strings.Contains(dja659HistoryFile(t, dir), dja659HistoryPrefix) {
		t.Fatal("atomic removal did not compact obsolete metadata")
	}
}

// This fixture exercises the actual AtomicWrite error path, after a readable
// selected row. It does not claim native rename or a process-crash execution.
func TestDJA659HistoryPersistenceFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("read-only directory fixture requires POSIX permissions; native Windows publication is a host gate")
	}
	dir, storage := t.TempDir(), t.TempDir()
	row := dja659HistoryRow("old", "p", "stamp")
	target := filepath.Join(storage, "roster")
	if err := os.WriteFile(target, []byte(row+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "agents.tsv")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(storage, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(storage, 0o700); err != nil {
			t.Errorf("restore fixture directory: %v", err)
		}
	})
	renamed, err := RosterRenameIf(dir, "old", "new", row)
	if renamed || err == nil || !strings.Contains(err.Error(), "roster rename write failed") {
		t.Fatalf("persistence failure swallowed: renamed=%v err=%v", renamed, err)
	}
	if got := dja659HistoryFile(t, dir); got != row+"\n" {
		t.Fatalf("failed publication changed the old complete registry: %q", got)
	}
}

func TestDJA659HistoryAtomicSnapshots(t *testing.T) {
	dir := t.TempDir()
	RosterAppend(dir, strings.Split(dja659HistoryRow("n0", "p", "stamp"), "\t"))
	readSnapshot := func() ([]byte, error) {
		// AtomicWrite uses renameReplacing, whose Windows contract permits
		// transient sharing refusals. A failed open observes no snapshot;
		// retry only ERROR_SHARING_VIOLATION, for at most 50 reads/500 ms.
		deadline := time.Now().Add(500 * time.Millisecond)
		for attempt := 1; ; attempt++ {
			raw, err := os.ReadFile(filepath.Join(dir, "agents.tsv"))
			if err == nil || runtime.GOOS != "windows" || !errors.Is(err, syscall.Errno(32)) || attempt >= 50 || !time.Now().Before(deadline) {
				return raw, err
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	checkSnapshot := func(raw []byte) (string, error) {
		name, rows := "", 0
		var metadata []map[string]any
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(line, dja659HistoryPrefix) {
				var record map[string]any
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, dja659HistoryPrefix)), &record); err != nil {
					return "", err
				}
				metadata = append(metadata, record)
			} else if line != "" && !strings.HasPrefix(line, "#") {
				rows++
				name, _, _ = strings.Cut(line, "\t")
				if line != dja659HistoryRow(name, "p", "stamp") {
					return "", fmt.Errorf("incomplete data row: %q", raw)
				}
			}
		}
		if rows != 1 || len(raw) == 0 || raw[len(raw)-1] != '\n' {
			return "", fmt.Errorf("incomplete snapshot: %q", raw)
		}
		step := -1
		for i := 0; i <= 30; i++ {
			if name == fmt.Sprint("n", i) {
				step = i
			}
		}
		if step < 0 || len(metadata) != step {
			return "", fmt.Errorf("incomplete row/history snapshot: %q", raw)
		}
		seen := map[string]bool{}
		for _, record := range metadata {
			from, _ := record["from"].(string)
			if len(record) != 5 || record["v"] != float64(1) || record["to"] != name || record["pane"] != "p" || record["started"] != "stamp" || seen[from] {
				return "", fmt.Errorf("mixed row/history snapshot: %q", raw)
			}
			seen[from] = true
		}
		for i := 0; i < step; i++ {
			if !seen[fmt.Sprint("n", i)] {
				return "", fmt.Errorf("missing history in snapshot: %q", raw)
			}
		}
		return name, nil
	}
	done, ready := make(chan struct{}), make(chan struct{})
	joined := make(chan struct{})
	type observation struct {
		reads int
		err   error
	}
	observed := make(chan observation, 1)
	go func() {
		defer close(joined)
		first := true
		reads := 0
		for {
			raw, err := readSnapshot()
			if err == nil {
				_, err = checkSnapshot(raw)
			}
			if err == nil {
				reads++
			}
			if first {
				close(ready)
				first = false
			}
			if err != nil {
				observed <- observation{reads: reads, err: err}
				return
			}
			select {
			case <-done:
				observed <- observation{reads: reads}
				return
			default:
			}
		}
	}()
	defer func() {
		close(done)
		result := <-observed
		<-joined
		if result.err != nil {
			t.Error(result.err)
		}
		if result.reads == 0 {
			t.Error("no complete snapshot was successfully observed")
		}
	}()
	<-ready
	for i := 1; i <= 30; i++ {
		RosterRename(dir, fmt.Sprint("n", i-1), fmt.Sprint("n", i))
	}
	// The synchronous writer has completed all renames. Require a complete
	// final observation as well as the concurrent reader's successful reads.
	raw, err := readSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if name, err := checkSnapshot(raw); err != nil || name != "n30" {
		t.Fatalf("final snapshot: name=%q err=%v raw=%q", name, err, raw)
	}
}
