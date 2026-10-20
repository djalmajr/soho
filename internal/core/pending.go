package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/djalmajr/soho/internal/platform"
)

// The pending queues (DJA-323). A worker whose sandbox cannot write the
// state dir (a codex worker in a worktree, the state dir in the main
// checkout) records its copy and process lines in a queue below the soho
// scratch dir, <TMPDIR (or os.TempDir())>/soho/<workspace>, where its
// reports already go. A queue line is the full registry line with the
// absolute path of the state dir it is for in front (field 1). The
// commands that read a registry to act move the lines for their state dir
// into it first (IncorporatePendingCopies, IncorporatePendingProcs); the
// read-only commands only count them (PendingCopiesFor, PendingProcsFor).
const (
	copiesPendingName = "copies.pending.tsv"
	procsPendingName  = "procs.pending.tsv"

	pendingCopiesHeader = "state_dir\tpath\towner\tpane\tcreated\tsource\torigin\ttask\treport"
	pendingProcsHeader  = "state_dir\t" + procsHeader
)

// pendingIncorporateHook runs inside an incorporation between the queue
// read and its rewrite, with both locks held; tests hold the incorporation
// there to race a queue writer against it.
var pendingIncorporateHook func(queue string)

// pendingLockHook runs right after an incorporation takes each of its two
// locks, with "registry" or "queue"; tests record the order.
var pendingLockHook func(lock string)

// PendingDir is the workspace scratch dir the queues live in.
func PendingDir(env platform.Env, workspaceID string) string {
	tmp := env.Get("TMPDIR")
	if tmp == "" {
		tmp = os.TempDir()
	}
	return filepath.Join(tmp, "soho", workspaceID)
}

// PendingCopiesFile is the copy queue of the workspace.
func PendingCopiesFile(env platform.Env, workspaceID string) string {
	return filepath.Join(PendingDir(env, workspaceID), copiesPendingName)
}

// PendingProcsFile is the process queue of the workspace.
func PendingProcsFile(env platform.Env, workspaceID string) string {
	return filepath.Join(PendingDir(env, workspaceID), procsPendingName)
}

// QueueCopy appends row, for stateDir, to the copy queue under the queue's
// own lock (never the registry's), and returns the queue path.
func QueueCopy(env platform.Env, workspaceID, stateDir string, row CopyRow) (string, error) {
	queue := PendingCopiesFile(env, workspaceID)
	return queue, appendPending(env, workspaceID, queue, pendingCopiesHeader, "copies queue", stateDir, row.fields())
}

// QueueProc appends row, for stateDir, to the process queue under the
// queue's own lock (never the registry's), and returns the queue path.
func QueueProc(env platform.Env, workspaceID, stateDir string, row ProcRow) (string, error) {
	queue := PendingProcsFile(env, workspaceID)
	return queue, appendPending(env, workspaceID, queue, pendingProcsHeader, "procs queue", stateDir, row.fields())
}

func appendPending(env platform.Env, workspaceID, queue, header, kind, stateDir string, fields []string) error {
	if !filepath.IsAbs(stateDir) {
		return fmt.Errorf("%s: the state dir %q is not absolute", kind, stateDir)
	}
	dir, err := pendingQueueDir(env, workspaceID, pendingDirCreate)
	if err != nil {
		return err
	}
	unlock, err := lockRegistry(dir, filepath.Base(queue)+".lock", kind)
	if err != nil {
		return err
	}
	defer unlock()
	lines, err := readPendingLines(queue)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	lines = append(lines, filepath.Clean(stateDir)+"\t"+strings.Join(fields, "\t"))
	return writePending(queue, header, lines)
}

// readPendingLines reads a queue's lines, without the header and the empty
// lines, in file order, through readPendingFile (never through a link).
func readPendingLines(queue string) ([]string, error) {
	raw, err := readPendingFile(queue)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, line := range strings.Split(raw, "\n") {
		if line == "" || line == pendingCopiesHeader || line == pendingProcsHeader {
			continue
		}
		out = append(out, line)
	}
	return out, nil
}

// writePending rewrites a queue through writePendingFile (a link in its
// place is replaced, not followed), or removes it when no line is left.
func writePending(queue, header string, lines []string) error {
	if len(lines) == 0 {
		if err := os.Remove(queue); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return writePendingFile(queue, header+"\n"+strings.Join(lines, "\n")+"\n")
}

// pendingTarget splits a queue line into its state dir and the registry
// fields; ok is false for a line with no absolute state dir in front.
func pendingTarget(line string) (stateDir string, fields []string, ok bool) {
	fields = strings.Split(line, "\t")
	if len(fields) < 2 || !filepath.IsAbs(fields[0]) {
		return "", nil, false
	}
	return fields[0], fields[1:], true
}

// sameStateDir reports whether a queue line's state dir is stateDir: the
// same cleaned text, or the same directory.
func sameStateDir(target, stateDir string) bool {
	if filepath.Clean(target) == filepath.Clean(stateDir) {
		return true
	}
	a, errA := os.Stat(target)
	b, errB := os.Stat(stateDir)
	return errA == nil && errB == nil && os.SameFile(a, b)
}

// parsePendingCopy reads the registry fields of a copy queue line: the
// eight columns of a copies.tsv line, none missing.
func parsePendingCopy(fields []string) (CopyRow, bool) {
	if len(fields) != 8 || fields[0] == "" {
		return CopyRow{}, false
	}
	return CopyRow{Path: fields[0], Owner: fields[1], Pane: fields[2], Created: fields[3], Source: fields[4], Origin: fields[5], Task: fields[6], Report: fields[7]}, true
}

// parsePendingProc reads the registry fields of a process queue line: the
// six columns of a procs.tsv line, none missing, with a positive pid.
func parsePendingProc(fields []string) (ProcRow, bool) {
	if len(fields) != 6 {
		return ProcRow{}, false
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil || pid <= 0 {
		return ProcRow{}, false
	}
	return ProcRow{Pid: pid, Started: fields[1], Name: fields[2], Owner: fields[3], Pane: fields[4], Created: fields[5]}, true
}

// PendingDrop is a queue line an incorporation took out of the queue
// without registering it: the copy path (or the raw line when it is
// malformed) and why.
type PendingDrop struct {
	What      string
	Cause     string
	Malformed bool
}

// PendingResult is what an incorporation did: the lines it registered and
// the ones it dropped.
type PendingResult struct {
	Queue   string
	Moved   int
	Dropped []PendingDrop
}

// IncorporatePendingCopies moves the copy queue lines for stateDir into
// copies.tsv. It takes the registry lock, then the queue's (always in that
// order; a queue writer takes only the queue's), re-reads the queue, runs
// check on each line for this state dir (the caller's copies add check,
// with the row to register or the refusal cause), upserts the accepted rows
// by path, and rewrites the queue with the lines for other state dirs
// only, removing it when none is left. A refused or malformed line leaves
// the queue as a PendingDrop. On an error the registry and the queue stay
// as they were (a registry already written keeps its lines in the queue
// too, and the next incorporation upserts them again).
func IncorporatePendingCopies(stateDir, workspaceID string, env platform.Env, check func(CopyRow) (CopyRow, string)) (PendingResult, error) {
	queue := PendingCopiesFile(env, workspaceID)
	if _, err := pendingQueueDir(env, workspaceID, pendingDirFix); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return PendingResult{Queue: queue}, nil
		}
		return PendingResult{Queue: queue}, err
	}
	return incorporatePending(stateDir, queue, pendingCopiesHeader, "copies queue", LockCopies, func(mine [][]string) (int, []PendingDrop, error) {
		accepted := []CopyRow{}
		dropped := []PendingDrop{}
		for _, fields := range mine {
			row, ok := parsePendingCopy(fields)
			if !ok {
				dropped = append(dropped, PendingDrop{What: strings.Join(fields, "\t"), Cause: "malformed line", Malformed: true})
				continue
			}
			checked, cause := check(row)
			if cause != "" {
				dropped = append(dropped, PendingDrop{What: row.Path, Cause: cause})
				continue
			}
			accepted = append(accepted, checked)
		}
		if len(accepted) == 0 {
			return 0, dropped, nil
		}
		rows, err := ReadCopies(stateDir)
		if err != nil {
			return 0, nil, err
		}
		for _, row := range accepted {
			replaced := false
			for i, existing := range rows {
				if existing.Path == row.Path {
					rows[i], replaced = row, true
					break
				}
			}
			if !replaced {
				rows = append(rows, row)
			}
		}
		if err := WriteCopies(stateDir, rows); err != nil {
			return 0, nil, err
		}
		return len(accepted), dropped, nil
	})
}

// IncorporatePendingProcs moves the process queue lines for stateDir into
// procs.tsv, with the locks, the order and the rewrite of
// IncorporatePendingCopies. A process line only has to be well formed: a
// process that has ended is registered too, and gc drops it.
func IncorporatePendingProcs(stateDir, workspaceID string, env platform.Env) (PendingResult, error) {
	queue := PendingProcsFile(env, workspaceID)
	if _, err := pendingQueueDir(env, workspaceID, pendingDirFix); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return PendingResult{Queue: queue}, nil
		}
		return PendingResult{Queue: queue}, err
	}
	return incorporatePending(stateDir, queue, pendingProcsHeader, "procs queue", LockProcs, func(mine [][]string) (int, []PendingDrop, error) {
		accepted := []ProcRow{}
		dropped := []PendingDrop{}
		for _, fields := range mine {
			row, ok := parsePendingProc(fields)
			if !ok {
				dropped = append(dropped, PendingDrop{What: strings.Join(fields, "\t"), Cause: "malformed line", Malformed: true})
				continue
			}
			accepted = append(accepted, row)
		}
		if len(accepted) == 0 {
			return 0, dropped, nil
		}
		rows, err := ReadProcs(stateDir)
		if err != nil {
			return 0, nil, err
		}
		for _, row := range accepted {
			replaced := false
			for i, existing := range rows {
				if existing.key() == row.key() {
					rows[i], replaced = row, true
					break
				}
			}
			if !replaced {
				rows = append(rows, row)
			}
		}
		if err := WriteProcs(stateDir, rows); err != nil {
			return 0, nil, err
		}
		return len(accepted), dropped, nil
	})
}

func incorporatePending(stateDir, queue, header, kind string, lockRegistryOf func(string) (func(), error), apply func([][]string) (int, []PendingDrop, error)) (PendingResult, error) {
	result := PendingResult{Queue: queue}
	// A first read without any lock: with nothing for this state dir (no
	// queue, or only other state dirs' lines), no lock is taken and
	// nothing is written.
	lines, err := readPendingLines(queue)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return result, nil
		}
		return result, err
	}
	due := false
	for _, line := range lines {
		target, _, ok := pendingTarget(line)
		if !ok || sameStateDir(target, stateDir) {
			due = true
			break
		}
	}
	if !due {
		return result, nil
	}
	unlockRegistry, err := lockRegistryOf(stateDir)
	if err != nil {
		return result, err
	}
	defer unlockRegistry()
	if pendingLockHook != nil {
		pendingLockHook("registry")
	}
	unlockQueue, err := lockRegistry(filepath.Dir(queue), filepath.Base(queue)+".lock", kind)
	if err != nil {
		return result, err
	}
	defer unlockQueue()
	if pendingLockHook != nil {
		pendingLockHook("queue")
	}
	lines, err = readPendingLines(queue)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return result, nil
		}
		return result, err
	}
	mine := [][]string{}
	others := []string{}
	for _, line := range lines {
		target, fields, ok := pendingTarget(line)
		switch {
		case !ok:
			result.Dropped = append(result.Dropped, PendingDrop{What: line, Cause: "malformed line", Malformed: true})
		case sameStateDir(target, stateDir):
			mine = append(mine, fields)
		default:
			others = append(others, line)
		}
	}
	if pendingIncorporateHook != nil {
		pendingIncorporateHook(queue)
	}
	moved, dropped, err := apply(mine)
	if err != nil {
		return PendingResult{Queue: queue}, err
	}
	result.Moved = moved
	result.Dropped = append(result.Dropped, dropped...)
	if err := writePending(queue, header, others); err != nil {
		return result, err
	}
	return result, nil
}

// PendingCopiesFor reads, without any lock or write, the well-formed copy
// queue lines for stateDir (the footprint's read-only count). A queue
// whose directory or file is refused reads as no lines, silently.
func PendingCopiesFor(stateDir, workspaceID string, env platform.Env) []CopyRow {
	out := []CopyRow{}
	if _, err := pendingQueueDir(env, workspaceID, pendingDirRead); err != nil {
		return out
	}
	lines, err := readPendingLines(PendingCopiesFile(env, workspaceID))
	if err != nil {
		return out
	}
	for _, line := range lines {
		target, fields, ok := pendingTarget(line)
		if !ok || !sameStateDir(target, stateDir) {
			continue
		}
		if row, ok := parsePendingCopy(fields); ok {
			out = append(out, row)
		}
	}
	return out
}

// PendingProcsFor reads, without any lock or write, the well-formed process
// queue lines for stateDir (the footprint's read-only count).
func PendingProcsFor(stateDir, workspaceID string, env platform.Env) []ProcRow {
	out := []ProcRow{}
	if _, err := pendingQueueDir(env, workspaceID, pendingDirRead); err != nil {
		return out
	}
	lines, err := readPendingLines(PendingProcsFile(env, workspaceID))
	if err != nil {
		return out
	}
	for _, line := range lines {
		target, fields, ok := pendingTarget(line)
		if !ok || !sameStateDir(target, stateDir) {
			continue
		}
		if row, ok := parsePendingProc(fields); ok {
			out = append(out, row)
		}
	}
	return out
}
