package core

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/djalmajr/soho/internal/platform"
)

const renamePrefix = "# soho-rename "
const renameMappingLimit = 64
const renameMetadataLimit = 64 << 10

// Invalidation comments are ordered with append-only recreations. Atomic
// rewrites compact them away. Stamps are opaque identities, never dates.
type renameRecord struct {
	Version    int    `json:"v"`
	From       string `json:"from,omitempty"`
	To         string `json:"to,omitempty"`
	Pane       string `json:"pane,omitempty"`
	Started    string `json:"started,omitempty"`
	Invalidate string `json:"invalidate,omitempty"`
}

func renameName(s string) bool { return len(s) <= 32 && laneNameRE.MatchString(s) }
func renameIdentity(s string) bool {
	return s != "" && len(s) <= 256 && !strings.ContainsAny(s, "\t\r\n\x00")
}

func decodeRename(raw string) (renameRecord, bool) {
	if !utf8.ValidString(raw) {
		return renameRecord{}, false
	}
	// encoding/json normally accepts duplicate keys. Inspect the object's keys
	// first, then enforce the exact typed schema and single-document boundary.
	d := json.NewDecoder(strings.NewReader(raw))
	start, err := d.Token()
	if err != nil || start != json.Delim('{') {
		return renameRecord{}, false
	}
	keys := map[string]bool{}
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return renameRecord{}, false
		}
		name, ok := key.(string)
		if !ok || keys[name] {
			return renameRecord{}, false
		}
		keys[name] = true
		var value json.RawMessage
		if err := d.Decode(&value); err != nil {
			return renameRecord{}, false
		}
	}
	if _, err := d.Token(); err != nil {
		return renameRecord{}, false
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return renameRecord{}, false
	}
	var r renameRecord
	d = json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&r) != nil || r.Version != 1 {
		return renameRecord{}, false
	}
	if r.Invalidate != "" {
		return r, len(keys) == 2 && keys["v"] && keys["invalidate"] && renameName(r.Invalidate)
	}
	return r, len(keys) == 5 && keys["v"] && keys["from"] && keys["to"] && keys["pane"] && keys["started"] && renameName(r.From) && renameName(r.To) && r.From != r.To && renameIdentity(r.Pane) && renameIdentity(r.Started)
}

func invalidateRenames(records []renameRecord, names ...string) []renameRecord {
	out := make([]renameRecord, 0, len(records))
	for _, r := range records {
		keep := true
		for _, name := range names {
			if r.From == name || r.To == name {
				keep = false
				break
			}
		}
		if keep {
			out = append(out, r)
		}
	}
	return out
}

// Any malformed/ambiguous metadata disables the entire diagnostic history.
// The total input bytes and effective mapping count are independently bounded.
func parseRenames(lines []string) []renameRecord {
	var records []renameRecord
	size := 0
	for _, line := range lines {
		if !strings.HasPrefix(line, renamePrefix) {
			continue
		}
		size += len(line) + 1
		if size > renameMetadataLimit {
			return nil
		}
		r, ok := decodeRename(strings.TrimPrefix(line, renamePrefix))
		if !ok {
			return nil
		}
		if r.Invalidate != "" {
			records = invalidateRenames(records, r.Invalidate)
			continue
		}
		for _, old := range records {
			if old.From == r.From {
				return nil
			}
		}
		records = append(records, r)
		if len(records) > renameMappingLimit {
			return nil
		}
	}
	// Writers flatten chains; accepting externally supplied chains/cycles would
	// make the suggested endpoint ambiguous. Neither can produce a hint.
	for _, r := range records {
		for _, other := range records {
			if r.To == other.From {
				return nil
			}
		}
	}
	return records
}

func liveRenames(lines []string, records []renameRecord) []renameRecord {
	rows := rosterDataRows(lines)
	indices := rosterLastIndices(rows)
	out := make([]renameRecord, 0, len(records))
	for _, r := range records {
		if _, active := indices[r.From]; active {
			continue
		}
		i, exists := indices[r.To]
		if !exists {
			continue
		}
		f := strings.Split(rows[i], "\t")
		if len(f) > 7 && f[1] == r.Pane && f[7] == r.Started {
			out = append(out, r)
		}
	}
	return out
}

func renameLine(r renameRecord) string {
	raw, _ := json.Marshal(r) // Only fixed fields of strings/integers.
	return renamePrefix + string(raw)
}

func withRenames(lines []string, records []renameRecord) []string {
	out := make([]string, 0, len(lines)+len(records))
	for _, line := range lines {
		if !strings.HasPrefix(line, renamePrefix) {
			out = append(out, line)
		}
	}
	// Reserve half the byte budget for future append-only invalidations.
	for len(records) > 0 {
		size := 0
		for _, r := range records {
			size += len(renameLine(r)) + 1
		}
		if len(records) <= renameMappingLimit && size <= renameMetadataLimit/2 {
			break
		}
		records = records[1:]
	}
	for _, r := range records {
		out = append(out, renameLine(r))
	}
	return out
}

func compactRenames(lines []string, invalidNames ...string) []string {
	return withRenames(lines, liveRenames(lines, invalidateRenames(parseRenames(lines), invalidNames...)))
}

// renameDataLines uses the same complete-file decoding as RosterLine. In
// particular, only a CR followed by LF is a delimiter; a final lone CR stays
// data. Raw lines remain separate for metadata validation and publication.
func renameDataLines(raw []byte) []string {
	lines := strings.Split(platform.DecodeText(raw), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// RosterRenameHint is diagnostic only. Exact current names always win and
// callers must never use the returned name to deliver or resolve a worker.
func RosterRenameHint(dir, oldName string) string {
	raw, lines, err := readStrictRoster(dir)
	if err != nil {
		return ""
	}
	dataLines := renameDataLines(raw)
	if rosterSelectLast(rosterDataRows(dataLines), oldName) != "" {
		return ""
	}
	for _, r := range liveRenames(dataLines, parseRenames(lines)) {
		if r.From == oldName {
			return r.To
		}
	}
	return ""
}

// RosterRename retains the supported wrapper. Both entry points share one
// locked row/history replacement and propagate persistence failures.
func RosterRename(dir, oldName, newName string) {
	if _, err := rosterRename(dir, oldName, newName, ""); err != nil {
		panic(err)
	}
}

// RosterRenameIf compares the last exact-name row selected before native
// rename, under the roster lock. A conflict writes neither rows nor history.
func RosterRenameIf(dir, oldName, newName, expectedRow string) (bool, error) {
	if expectedRow == "" {
		return false, fmt.Errorf("the selected source row is empty")
	}
	return rosterRename(dir, oldName, newName, expectedRow)
}

func rosterRename(dir, oldName, newName, expectedRow string) (renamed bool, err error) {
	WithRosterLock(dir, func() {
		raw, lines, readErr := readStrictRoster(dir)
		if readErr != nil {
			err = readErr
			return
		}
		dataLines := renameDataLines(raw)
		row := rosterSelectLast(rosterDataRows(dataLines), oldName)
		if row == "" || expectedRow != "" && row != expectedRow {
			return
		}
		if oldName == newName {
			renamed = true
			return
		}
		if rosterSelectLast(rosterDataRows(dataLines), newName) != "" {
			err = fmt.Errorf("the destination name '%s' already has a roster row", newName)
			return
		}
		records := liveRenames(dataLines, parseRenames(lines))
		f := strings.Split(row, "\t")
		next := make([]renameRecord, 0, len(records)+1)
		for _, r := range records {
			if r.From == newName || r.To == newName {
				continue
			}
			if r.To == oldName {
				if len(f) <= 7 || r.Pane != f[1] || r.Started != f[7] {
					continue
				}
				r.To = newName
			}
			next = append(next, r)
		}
		if len(f) > 7 && renameName(oldName) && renameName(newName) && renameIdentity(f[1]) && renameIdentity(f[7]) {
			next = append(next, renameRecord{Version: 1, From: oldName, To: newName, Pane: f[1], Started: f[7]})
		}
		for i, line := range dataLines {
			if strings.HasPrefix(line, "#") {
				continue
			}
			name, _, _ := strings.Cut(line, "\t")
			if name == oldName {
				_, rest, found := strings.Cut(lines[i], "\t")
				if found {
					lines[i] = newName + "\t" + rest
				} else {
					lines[i] = newName
				}
			}
		}
		// Publication terminates retained lines before appending history. If
		// the original last byte was a data CR, add a CRLF delimiter so that
		// ReadTextFile does not consume that data CR as the new delimiter.
		if len(raw) > 0 && raw[len(raw)-1] == '\r' {
			lines[len(lines)-1] += "\r"
		}
		out := withRenames(lines, next)
		if writeErr := platform.AtomicWrite(filepath.Join(dir, "agents.tsv"), strings.Join(out, "\n")+"\n"); writeErr != nil {
			err = fmt.Errorf("the roster rename write failed: %w", writeErr)
			return
		}
		renamed = true
	})
	return renamed, err
}
