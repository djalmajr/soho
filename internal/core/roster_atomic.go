package core

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/djalmajr/soho/internal/platform"
)

// rosterReadLimit bounds the agents.tsv the strict roster reader accepts:
// a roster file larger than this is not a roster (a corrupted or hostile
// state file), and the read fails closed instead of parsing an unbounded
// file.
const rosterReadLimit int64 = 1 << 20

// StrictRosterLines reads the roster strictly and boundedly: an absent
// agents.tsv means an empty roster, while an unreadable or non-regular
// file, a file over rosterReadLimit, or a file that grows while read is an
// error. Unlike the legacy tsvLines reader (which the shared legacy state
// paths keep), a read failure is never swallowed into an empty roster. The
// read is bounded by the file's stat size — at most info.Size()+1 bytes —
// and reading more proves the file grew between the stat and the read.
func StrictRosterLines(dir string) ([]string, error) {
	_, lines, err := readStrictRoster(dir)
	return lines, err
}

// readStrictRoster retains the bounded raw snapshot as well as its raw lines.
// Rename needs the original delimiters: the line split alone cannot tell a
// final data CR from the CR in a CRLF delimiter. Existing strict readers keep
// their raw-line policy through StrictRosterLines.
func readStrictRoster(dir string) ([]byte, []string, error) {
	path := filepath.Join(dir, "agents.tsv")
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, []string{}, nil
		}
		return nil, nil, fmt.Errorf("the roster is unreadable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("the roster is unreadable: agents.tsv is not a regular file")
	}
	if info.Size() > rosterReadLimit {
		return nil, nil, fmt.Errorf("the roster is unreadable: agents.tsv holds %d bytes (limit %d)", info.Size(), rosterReadLimit)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("the roster is unreadable: %w", err)
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, info.Size()+1))
	if err != nil {
		return nil, nil, fmt.Errorf("the roster is unreadable: %w", err)
	}
	if int64(len(raw)) > info.Size() {
		return nil, nil, fmt.Errorf("the roster is unreadable: agents.tsv grew while it was read")
	}
	lines := strings.Split(string(raw), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return raw, lines, nil
}

// RosterRemoveRefusal is a beforeRemove refusal: the row was still intact
// under the lock, but the obligation the caller revalidates immediately
// before the removal no longer holds; nothing was written. Msg and Code
// carry the caller's refusal message and exit code.
type RosterRemoveRefusal struct {
	Msg  string
	Code int
}

func (e *RosterRemoveRefusal) Error() string { return e.Msg }

// RosterRemoveIf is the compare-and-remove RosterRemove lacks: under the
// roster lock it re-reads the roster strictly and boundedly (StrictRosterLines)
// and removes the line expectedRow only while it still matches
// byte-for-byte (the caller's identity revalidation: name, pane, kind,
// role, family, created pane, cwd, started, model, approvals, roles and
// lane all together — a row whose owner or session fields changed
// concurrently is a different line and is never removed). A second row for
// the same pane that is not the verified row — a duplicate of the verified
// row, a replaced row, or a row added after the caller's check — is a
// corrupt-roster refusal, never a removal that would leave the caller's
// pane holding a worker row. removed is true only when exactly one
// matching line and no other row for the pane was rewritten out. err is
// non-nil when the roster exists but is unreadable (a read failure is
// propagated, never reported as a vanished row), when the roster is
// corrupt, when beforeRemove refuses, or on a failed rewrite; on a failed
// rewrite the file keeps its previous content, because platform.AtomicWrite
// renames a temp file in the same directory, so the target is either the
// old or the new content, never a mix. An absent roster is an empty
// roster: a no-op with no error.
func RosterRemoveIf(dir, pane, expectedRow string, beforeRemove ...func() (string, int)) (removed bool, err error) {
	check := func() (string, int) { return "", 0 }
	if len(beforeRemove) > 0 && beforeRemove[0] != nil {
		check = beforeRemove[0]
	}
	WithRosterLock(dir, func() {
		lines, rerr := StrictRosterLines(dir)
		if rerr != nil {
			err = rerr
			return
		}
		matched := 0
		other := ""
		for _, line := range lines {
			if strings.HasPrefix(line, "#") {
				continue
			}
			if line == expectedRow {
				matched++
				continue
			}
			f := strings.Split(line, "\t")
			if len(f) > 1 && f[1] == pane {
				other = line
			}
		}
		switch {
		case matched == 0 && other == "":
			return
		case matched > 1:
			err = fmt.Errorf("the roster holds %d identical rows for pane %s; the roster is corrupt", matched, pane)
			return
		case other != "":
			err = fmt.Errorf("the roster holds a row for pane %s that is not the verified row; the roster is corrupt", pane)
			return
		}
		// matched == 1 and no other row for the pane: revalidate the
		// obligations immediately before the write, then remove.
		if msg, code := check(); msg != "" {
			err = &RosterRemoveRefusal{Msg: msg, Code: code}
			return
		}
		out := make([]string, 0, len(lines)-1)
		for _, line := range lines {
			if line == expectedRow {
				continue
			}
			out = append(out, line)
		}
		out = compactRenames(out, strings.Split(expectedRow, "\t")[0])
		content := ""
		if len(out) != 0 {
			content = strings.Join(out, "\n") + "\n"
		}
		if werr := platform.AtomicWrite(filepath.Join(dir, "agents.tsv"), content); werr != nil {
			err = fmt.Errorf("the roster write failed: %w", werr)
			return
		}
		removed = true
	})
	return removed, err
}
