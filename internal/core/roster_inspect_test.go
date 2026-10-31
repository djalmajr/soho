package core

// Focused tests for the bounded, confined roster inspection: the shared
// comment/last-matching-name semantics, the D7 bounds (refused, never
// truncated), the ambiguous-identity refusal and the unsafe-roster
// refusal.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// openInspectRoot opens a session root over a temp dir with one roster
// file written by roster.
func openInspectRoot(t *testing.T, roster string) *os.Root {
	t.Helper()
	dir := t.TempDir()
	if roster != "" {
		if err := os.WriteFile(filepath.Join(dir, "agents.tsv"), []byte(roster), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return root
}

// TestRosterInspectRegular: comments, optional trailing fields and roster
// order are preserved, Row selects the LAST matching name like the normal
// reader, and an independently malformed row is candidate-invalid, not a
// session error.
func TestRosterInspectRegular(t *testing.T) {
	roster := "# header line\n" +
		"worker\tp1\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n" +
		"other\tp2\tcodex\treviewer\topenai\t\t\t\t\n" +
		"worker\tp3\tgemini\tdesigner\tgoogle\n"
	root := openInspectRoot(t, roster)
	snap, err := RosterRowsInspect(root)
	if err != nil {
		t.Fatalf("RosterRowsInspect: %v", err)
	}
	if len(snap.Rows) != 3 {
		t.Fatalf("rows = %d, want 3 data rows (the comment filtered)", len(snap.Rows))
	}
	// The LAST matching name wins, like the normal reader.
	row, ok := snap.Row("worker")
	if !ok || strings.Join(row.Fields, "|") != "worker|p3|gemini|designer|google" {
		t.Fatalf("Row(worker) = %#v ok=%v, want the last worker row", row, ok)
	}
	for _, name := range []string{"worker", "other", "missing"} {
		row, ok := snap.Row(name)
		inspected := ""
		if ok {
			inspected = strings.Join(row.Fields, "\t")
		}
		if normal := RosterLine(root.Name(), name); normal != inspected {
			t.Fatalf("%s normal=%q inspected=%q, want identical last-row selection", name, normal, inspected)
		}
	}
	// Optional trailing fields stay exactly as persisted.
	row, ok = snap.Row("other")
	if !ok || len(row.Fields) != 9 || row.Fields[2] != "codex" {
		t.Fatalf("Row(other) = %#v ok=%v, want 9 fields with the kind column", row, ok)
	}
	// A row without the required identity columns is candidate-invalid.
	root2 := openInspectRoot(t, "broken\t\t\n")
	snap2, err := RosterRowsInspect(root2)
	if err != nil {
		t.Fatalf("RosterRowsInspect(broken row): %v", err)
	}
	if len(snap2.Rows) != 1 || snap2.Rows[0].Valid {
		t.Fatalf("broken row = %#v, want one candidate-invalid row", snap2.Rows)
	}
	if _, ok := snap2.Row("broken"); !ok {
		t.Fatalf("Row(broken) = missing, want the invalid row kept for the later collector")
	}
}

// TestRosterInspectCRLFLineEndings: the snapshot shares the normal
// reader's CRLF-to-LF line-ending normalization before selecting the
// last name. A regular CRLF row keeps no CR in its last field, and the
// literal malformed-last bytes select the malformed last row, never
// reviving the older valid row (the normal reader's last-name choice,
// without the decoder's invalid-UTF-8 repair).
func TestRosterInspectCRLFLineEndings(t *testing.T) {
	for _, tc := range []struct {
		name   string
		roster string
		want   []string
		valid  bool
	}{
		{
			name:   "valid",
			roster: "worker\tp1\tclaude\timplementer\r\n",
			want:   []string{"worker", "p1", "claude", "implementer"},
			valid:  true,
		},
		{
			name:   "malformed-last",
			roster: "worker\tp1\tclaude\timplementer\r\nworker\r\n",
			want:   []string{"worker"},
			valid:  false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := openInspectRoot(t, tc.roster)
			snap, err := RosterRowsInspect(root)
			if err != nil {
				t.Fatalf("RosterRowsInspect: %v", err)
			}
			row, ok := snap.Row("worker")
			if !ok || len(row.Fields) != len(tc.want) {
				t.Fatalf("Row(worker) = %#v ok=%v, want the literal last row %q", row, ok, tc.want)
			}
			for i, f := range tc.want {
				if row.Fields[i] != f {
					t.Fatalf("field %d = %q, want %q (no CR may leak into the last field)", i, row.Fields[i], f)
				}
			}
			if row.Valid != tc.valid {
				t.Fatalf("valid = %v, want %v (the literal last row decides)", row.Valid, tc.valid)
			}
			if normal := RosterLine(root.Name(), "worker"); normal != strings.Join(row.Fields, "\t") {
				t.Fatalf("normal=%q inspected=%q, want identical last-row selection", normal, strings.Join(row.Fields, "\t"))
			}
		})
	}
}

// TestRosterInspectAbsent: a missing roster is the empty snapshot with no
// error (absence creates nothing and proves nothing).
func TestRosterInspectAbsent(t *testing.T) {
	root := openInspectRoot(t, "")
	snap, err := RosterRowsInspect(root)
	if err != nil {
		t.Fatalf("RosterRowsInspect(absent roster): %v", err)
	}
	if len(snap.Rows) != 0 {
		t.Fatalf("absent roster rows = %#v, want none", snap.Rows)
	}
	empty, err := RosterRowsInspect(nil)
	if err != nil || len(empty.Rows) != 0 {
		t.Fatalf("nil root = %#v %v, want the empty snapshot", empty, err)
	}
}

// TestRosterInspectOversizeFile: a roster larger than the 4 MiB bound is
// refused with ErrRosterOversize, never truncated.
func TestRosterInspectOversizeFile(t *testing.T) {
	pad := strings.Repeat("a", 2048) + "\n"
	roster := "# pad\n" + strings.Repeat(pad, (rosterInspectMaxBytes/2048)+1)
	root := openInspectRoot(t, roster)
	if _, err := RosterRowsInspect(root); !errors.Is(err, ErrRosterOversize) {
		t.Fatalf("oversize roster err = %v, want ErrRosterOversize", err)
	}
}

// TestRosterInspectOversizeRows: more than the 4096 data-row bound is
// refused; the bound itself is accepted.
func TestRosterInspectOversizeRows(t *testing.T) {
	make := func(n int) string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("w%04d\tp1\tclaude\timplementer", i)
		}
		return strings.Join(out, "\n")
	}
	root := openInspectRoot(t, make(rosterInspectMaxRows+1))
	if _, err := RosterRowsInspect(root); !errors.Is(err, ErrRosterOversize) {
		t.Fatalf("row-count-oversize roster err = %v, want ErrRosterOversize", err)
	}
	root2 := openInspectRoot(t, make(rosterInspectMaxRows))
	if _, err := RosterRowsInspect(root2); err != nil {
		t.Fatalf("row-bound roster err = %v, want accepted", err)
	}
}

// TestRosterInspectOversizeRow: one line longer than the 64 KiB bound is
// refused; the bound itself is accepted.
func TestRosterInspectOversizeRow(t *testing.T) {
	root := openInspectRoot(t, "w1\tp1\tclaude\timplementer\t"+strings.Repeat("x", rosterInspectMaxRowLen)+"\n")
	if _, err := RosterRowsInspect(root); !errors.Is(err, ErrRosterOversize) {
		t.Fatalf("row-length-oversize roster err = %v, want ErrRosterOversize", err)
	}
	root2 := openInspectRoot(t, "w1\tp1\tclaude\timplementer\t"+strings.Repeat("y", rosterInspectMaxRowLen-40)+"\n")
	if _, err := RosterRowsInspect(root2); err != nil {
		t.Fatalf("row-bound roster err = %v, want accepted", err)
	}
}

// TestRosterInspectAmbiguousIdentity: two data rows with the same name
// and the same nonempty pane suppress the whole session roster; the same
// name with a different pane (and with an empty pane) is not ambiguous.
func TestRosterInspectAmbiguousIdentity(t *testing.T) {
	root := openInspectRoot(t, "dup\tp1\tclaude\timplementer\n"+
		"dup\tp1\tcodex\treviewer\n")
	if _, err := RosterRowsInspect(root); !errors.Is(err, ErrRosterAmbiguousIdentity) {
		t.Fatalf("duplicate identity err = %v, want ErrRosterAmbiguousIdentity", err)
	}
	// The same name in a different pane is a re-record, not an ambiguity:
	// the last row wins, as the normal reader selects it.
	root2 := openInspectRoot(t, "dup\tp1\tclaude\timplementer\n"+
		"dup\tp2\tcodex\treviewer\n")
	snap, err := RosterRowsInspect(root2)
	if err != nil {
		t.Fatalf("different-pane duplicate err = %v, want accepted", err)
	}
	row, ok := snap.Row("dup")
	if !ok || row.Fields[1] != "p2" {
		t.Fatalf("Row(dup) = %#v ok=%v, want the last row (pane p2)", row, ok)
	}
	// An empty pane never forms an identity key.
	root3 := openInspectRoot(t, "dup\t\tclaude\timplementer\n"+
		"dup\t\tcodex\treviewer\n")
	if _, err := RosterRowsInspect(root3); err != nil {
		t.Fatalf("empty-pane duplicate err = %v, want accepted", err)
	}
}

// TestRosterInspectUnsafeRoster: a linked roster, a directory and a
// nonregular roster entry are refused, never read through.
func TestRosterInspectUnsafeRoster(t *testing.T) {
	// A link to a roster outside the session root.
	dir := t.TempDir()
	outside := t.TempDir()
	outsideRoster := filepath.Join(outside, "agents.tsv")
	if err := os.WriteFile(outsideRoster, []byte("w\tp1\tclaude\timplementer\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideRoster, filepath.Join(dir, "agents.tsv")); err != nil {
		t.Skipf("the test user cannot create symlinks: %v", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, err := RosterRowsInspect(root); err == nil {
		t.Fatalf("the linked roster was accepted: %#v", err)
	}
	// A directory at the roster name.
	dir2 := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir2, "agents.tsv"), 0o700); err != nil {
		t.Fatal(err)
	}
	root2, err := os.OpenRoot(dir2)
	if err != nil {
		t.Fatal(err)
	}
	defer root2.Close()
	if _, err := RosterRowsInspect(root2); err == nil {
		t.Fatalf("the directory roster was accepted")
	}
}
