package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRosterSetModelUpdatesModelAndEffortInOneRow(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "agents.tsv")
	header := "# name\tpane\tkind\trole\n"
	row := func(name, model, effort string) string {
		return strings.Join([]string{name, "p-" + name, "pi", "implementer", "family", "0", "/work", "now", model, "task", "implementer", "build", "", "native-args", effort}, "\t")
	}
	original := header + row("worker", "codex/old", "high") + "\n" + row("other", "codex/keep", "low") + "\n"
	if err := os.WriteFile(file, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	RosterSetModel(dir, "worker", "codex/new", "medium")
	rows := RosterRows(dir)
	if len(rows) != 2 || !strings.HasPrefix(string(mustReadRoster(t, file)), header) {
		t.Fatalf("roster=%q", mustReadRoster(t, file))
	}
	got := strings.Split(rows[0], "\t")
	if len(got) != 15 || got[8] != "codex/new" || got[14] != "medium" {
		t.Fatalf("worker row=%q", got)
	}
	if got[1] != "p-worker" || got[3] != "implementer" || got[11] != "build" || got[13] != "native-args" {
		t.Fatalf("other columns changed=%q", got)
	}
	if other := strings.Split(rows[1], "\t"); other[8] != "codex/keep" || other[14] != "low" {
		t.Fatalf("other row changed=%q", other)
	}
}

func TestRosterSetModelExpandsShortRowsAndUsesLastMatch(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "agents.tsv"), []byte("w\tp1\tpi\timplementer\nw\tp2\tpi\treviewer\n"), 0o600)
	RosterSetModel(dir, "w", "codex/new", "xhigh")
	rows := RosterRows(dir)
	if len(rows) != 2 {
		t.Fatalf("rows=%q", rows)
	}
	// Like RosterSetRole, every row with the name is rewritten; the last one
	// is the roster's selected row.
	if first := strings.Split(rows[0], "\t"); len(first) != 15 || first[8] != "codex/new" || first[14] != "xhigh" {
		t.Fatalf("first row=%q", first)
	}
	last := strings.Split(rows[1], "\t")
	if len(last) != 15 || last[1] != "p2" || last[8] != "codex/new" || last[14] != "xhigh" {
		t.Fatalf("last row=%q", last)
	}
}

func mustReadRoster(t *testing.T, file string) []byte {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
