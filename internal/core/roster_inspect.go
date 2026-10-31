package core

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/djalmajr/soho/internal/platform"
)

// rosterFileName is the shared roster file name (the normal reader and
// the writers use the same name under a state dir).
const rosterFileName = "agents.tsv"

// The D7 private roster bounds of the read-only inspection. They are
// private input bounds, not the public roster grammar: the shared TSV
// splitting, the comment filter and the last-matching-name selection are
// the normal reader's, and the public 500-agent truncation is separate
// and untouched.
const (
	rosterInspectMaxBytes  = 4 << 20
	rosterInspectMaxRows   = 4096
	rosterInspectMaxRowLen = 64 << 10
)

// ErrRosterOversize is the session-level refusal of a roster that
// exceeds one of the inspection bounds (whole file, data-row count or a
// single line). The input is refused, never truncated.
var ErrRosterOversize = errors.New("the roster exceeds the inspection bound")

// ErrRosterAmbiguousIdentity is the session-level refusal of a roster
// with two data rows of the same name and the same nonempty pane: the
// identity is ambiguous, so the whole session's roster is suppressed
// rather than a candidate being invented from one of the rows.
var ErrRosterAmbiguousIdentity = errors.New("the roster has a duplicate agent identity")

// RosterRow is one data row of the inspected roster, in roster order.
// Fields are the row's tab-separated columns, trailing optional ones
// included exactly as persisted. Valid marks the independently
// checkable required identity columns (name, pane, kind, role): present
// and valid UTF-8; a row that is not valid is candidate-invalid for the
// later collector, not a session error.
type RosterRow struct {
	Fields []string
	Valid  bool
}

// RosterSnapshot is the bounded, confined read of one session's
// agents.tsv: the data rows in roster order and the shared
// last-matching-name selection. The empty snapshot (no rows) is what
// absence yields.
type RosterSnapshot struct {
	Rows   []RosterRow
	byName map[string]int
}

// Row selects the LAST data row whose name column equals name, the
// normal reader's selection, or false when the roster has no such row.
func (s RosterSnapshot) Row(name string) (RosterRow, bool) {
	i, ok := s.byName[name]
	if !ok {
		return RosterRow{}, false
	}
	return s.Rows[i], true
}

// rosterRowValid checks the required identity columns of one data row.
func rosterRowValid(fields []string) bool {
	if len(fields) < 4 {
		return false
	}
	for _, f := range fields[:4] {
		if f == "" || !utf8.ValidString(f) {
			return false
		}
	}
	return true
}

// RosterRowsInspect is the error-aware bounded, confined read of one
// session's roster through its already-opened session root. Absence (a
// nil root or a missing roster) is the empty snapshot with no error; an
// unsafe, unreadable, unstable or oversize roster is an error, never an
// empty-success fallback. The roster entry is confined to the opened
// parent, must be a regular nonlinked file, is opened nonblocking with
// its identity verified against the parent, and is read under the D7
// bounds; the comment filter and the row selection are the shared roster
// parse. It creates and writes nothing.
func RosterRowsInspect(sess *os.Root) (RosterSnapshot, error) {
	var snap RosterSnapshot
	if sess == nil {
		return snap, nil
	}
	before, err := sess.Lstat(rosterFileName)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return snap, nil
		}
		return snap, fmt.Errorf("the roster %s cannot be inspected: %v", rosterFileName, err)
	}
	if !before.Mode().IsRegular() {
		return snap, fmt.Errorf("the roster %s is not a regular file", rosterFileName)
	}
	f, err := sess.OpenFile(rosterFileName, platform.ReadOnlyNonBlocking, 0)
	if err != nil {
		return snap, fmt.Errorf("the roster %s cannot be opened: %v", rosterFileName, err)
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil {
		return snap, fmt.Errorf("the roster %s cannot be verified: %v", rosterFileName, err)
	}
	after, err := sess.Lstat(rosterFileName)
	if err != nil {
		return snap, fmt.Errorf("the roster %s cannot be verified: %v", rosterFileName, err)
	}
	if !opened.Mode().IsRegular() || !after.Mode().IsRegular() || !os.SameFile(before, opened) || !os.SameFile(opened, after) {
		return snap, fmt.Errorf("the roster %s is not a stable regular file", rosterFileName)
	}
	raw, err := io.ReadAll(io.LimitReader(f, rosterInspectMaxBytes+1))
	if err != nil {
		return snap, fmt.Errorf("the roster %s cannot be read: %v", rosterFileName, err)
	}
	if len(raw) > rosterInspectMaxBytes {
		return snap, fmt.Errorf("%w: larger than %d bytes", ErrRosterOversize, rosterInspectMaxBytes)
	}
	// The normal reader decodes CRLF to LF (platform.DecodeText); the
	// snapshot shares exactly that line-ending normalization, not the
	// decoder's WHATWG invalid-UTF-8 repair, so invalid UTF-8 stays a
	// candidate refusal and the byte bounds stay raw.
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	rows := rosterDataRows(lines)
	if len(rows) > rosterInspectMaxRows {
		return snap, fmt.Errorf("%w: more than %d data rows", ErrRosterOversize, rosterInspectMaxRows)
	}
	seen := map[string]bool{}
	for _, line := range rows {
		if len(line) > rosterInspectMaxRowLen {
			return snap, fmt.Errorf("%w: a row exceeds %d bytes", ErrRosterOversize, rosterInspectMaxRowLen)
		}
		fields := strings.Split(line, "\t")
		name, pane := fields[0], ""
		if len(fields) > 1 {
			pane = fields[1]
		}
		if name != "" && pane != "" {
			key := name + "\x00" + pane
			if seen[key] {
				return snap, fmt.Errorf("%w: name %q pane %q", ErrRosterAmbiguousIdentity, name, pane)
			}
			seen[key] = true
		}
		row := RosterRow{Fields: fields, Valid: rosterRowValid(fields)}
		snap.Rows = append(snap.Rows, row)
	}
	snap.byName = rosterLastIndices(rows)
	return snap, nil
}
