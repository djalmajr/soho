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

// procsHeader is the first line of the per-workspace process registry,
// procs.tsv. The command line of a process is never read or stored: it may
// carry credentials.
const procsHeader = "pid\tstarted\tname\towner\tpane\tcreated"

const (
	// procsLockName sits next to the registry: the same exclusive-creation
	// inter-process lock as the copy registry, on its own file.
	procsLockName = "procs.tsv.lock"
)

// LockProcs takes the inter-process procs.tsv lock: the shared registry
// lock (lockRegistry), on the process registry's own lock file, with the
// same retry, stale and timeout rules as the copy registry.
func LockProcs(stateDir string) (func(), error) {
	return lockRegistry(stateDir, procsLockName, "procs")
}

// ProcRow is one line of procs.tsv: a worker process and who registered it.
type ProcRow struct {
	Pid     int
	Started string // the start time, as the system gives it
	Name    string // the base name of the command
	Owner   string // the roster name, or "-"
	Pane    string // the pane id, or "-"
	Created string // ISO-8601 UTC
}

// key is the identity of a registry line: the same pid with the same start
// time is the same registration, so re-adding it replaces the line without
// duplicating it. A pid with another start time (a reused pid) is a
// different line.
func (r ProcRow) key() string { return strconv.Itoa(r.Pid) + "\t" + r.Started }

func (r ProcRow) fields() []string {
	return []string{strconv.Itoa(r.Pid), r.Started, r.Name, r.Owner, r.Pane, r.Created}
}

// ProcsFile returns the process registry path inside the state dir.
func ProcsFile(stateDir string) string { return filepath.Join(stateDir, "procs.tsv") }

// ReadProcs reads the process registry without the header, in file order. A
// registry that does not exist yet reads as no rows.
func ReadProcs(stateDir string) ([]ProcRow, error) {
	raw, err := platform.ReadTextFile(ProcsFile(stateDir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []ProcRow{}, nil
		}
		return nil, err
	}
	out := []ProcRow{}
	for _, line := range strings.Split(raw, "\n") {
		if line == "" || line == procsHeader {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 6 {
			return nil, fmt.Errorf("procs.tsv: malformed line %q", line)
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			return nil, fmt.Errorf("procs.tsv: malformed pid in line %q", line)
		}
		out = append(out, ProcRow{Pid: pid, Started: fields[1], Name: fields[2], Owner: fields[3], Pane: fields[4], Created: fields[5]})
	}
	return out, nil
}

// WriteProcs rewrites the process registry atomically (temporary file plus
// rename, like the other state files).
func WriteProcs(stateDir string, rows []ProcRow) error {
	lines := []string{procsHeader}
	for _, row := range rows {
		lines = append(lines, strings.Join(row.fields(), "\t"))
	}
	return platform.AtomicWrite(ProcsFile(stateDir), strings.Join(lines, "\n")+"\n")
}

// UpsertProc records row, replacing the line with the same pid and the same
// start time when it exists, so re-registering a live process never
// duplicates it. The whole read-modify-write runs under the inter-process
// registry lock, so two concurrent registrations never lose a line.
func UpsertProc(stateDir string, row ProcRow) error {
	unlock, err := LockProcs(stateDir)
	if err != nil {
		return err
	}
	defer unlock()
	rows, err := ReadProcs(stateDir)
	if err != nil {
		return err
	}
	for i, existing := range rows {
		if existing.key() == row.key() {
			rows[i] = row
			return WriteProcs(stateDir, rows)
		}
	}
	return WriteProcs(stateDir, append(rows, row))
}

// DropProcsLines re-reads the registry under the inter-process lock and
// rewrites it without the lines whose pid/start-time key is in removed,
// keeping every other line, including lines added meanwhile.
func DropProcsLines(stateDir string, removed map[string]bool) error {
	unlock, err := LockProcs(stateDir)
	if err != nil {
		return err
	}
	defer unlock()
	rows, err := ReadProcs(stateDir)
	if err != nil {
		return err
	}
	out := make([]ProcRow, 0, len(rows))
	for _, row := range rows {
		if !removed[row.key()] {
			out = append(out, row)
		}
	}
	return WriteProcs(stateDir, out)
}

// ProcRowState is the state of a registry line at the last moment: running
// when the pid exists with the same start time, gone when the absence is
// proven, reused when the pid exists with another start time, unknown
// when the read failed (the line must not be dropped or stopped on an
// unknown read: a failed read is not an absence).
func ProcRowState(row ProcRow, env platform.Env) string {
	started, live := platform.ReadProc(row.Pid, env)
	switch live {
	case platform.ProcGone:
		return "gone"
	case platform.ProcUnknown:
		return "unknown"
	}
	if platform.SameStarted(started, row.Started) {
		return "running"
	}
	return "reused"
}

// StopProcResult is one StopProcRow outcome: the stdout line, whether the
// registry line was dropped, and the error when the stop failed (the line
// is kept then).
type StopProcResult struct {
	Line    string
	Dropped bool
	Err     error
	// Stopped is set by StopUnregisteredProc when the tree was stopped.
	Stopped bool
}

// StopProcRow acts on one registry line at the last moment. A running line
// (the start time re-checked now) has its process tree stopped, with the
// registered start time as the root's expected identity; a gone or reused
// line is dropped without any signal — a reused pid is never signalled, so
// the process behind it is never touched; an unknown line (the read
// failed) is kept, with an error: the stop is not claimed. The registry
// line is dropped under the registry lock; the stop itself runs outside
// it.
func StopProcRow(stateDir string, row ProcRow, env platform.Env) StopProcResult {
	line := ""
	switch ProcRowState(row, env) {
	case "gone":
		line = fmt.Sprintf("process %d already gone", row.Pid)
	case "reused":
		line = fmt.Sprintf("process %d was reused; not stopped", row.Pid)
	case "unknown":
		// The read failed: neither a stop nor a drop is claimed.
		return StopProcResult{Err: fmt.Errorf("process %d is unreadable; not stopped", row.Pid)}
	case "running":
		if err := platform.StopProcessTreeGuarded(row.Pid, row.Started, env, nil); err != nil {
			return StopProcResult{Err: err}
		}
		line = fmt.Sprintf("stopped process %d (%s)", row.Pid, row.Name)
	}
	if err := DropProcsLines(stateDir, map[string]bool{row.key(): true}); err != nil {
		// The line stays registered; gc drops it.
		return StopProcResult{Line: line, Err: err}
	}
	return StopProcResult{Line: line, Dropped: true}
}

// StopUnregisteredProc stops the process tree of a process procs.tsv does
// not hold, with the signal and the wait of StopProcRow
// (platform.StopProcessTreeGuarded, the start time read now as the root's
// expected identity, each member's start time re-read before its signal).
// still re-checks the caller's listing (the start identity and the cwd of
// the listed process) and is the guard of every signal round: it runs
// before the TERM round and again before the KILL round, and when it
// reports a change nothing more is signalled and the process stays. The
// listed process's descendants are stopped with it, under the same guard.
// A process that still matches the listing existed when the start time
// was read, so that start time is its own. A gone pid is not an error; an
// unreadable one is, and nothing is signalled.
func StopUnregisteredProc(pid int, name string, still func() bool, env platform.Env) StopProcResult {
	started, live := platform.ReadProc(pid, env)
	switch live {
	case platform.ProcGone:
		return StopProcResult{Line: fmt.Sprintf("process %d already gone", pid)}
	case platform.ProcUnknown:
		return StopProcResult{Err: fmt.Errorf("process %d is unreadable; not stopped", pid)}
	}
	if err := platform.StopProcessTreeGuarded(pid, started, env, still); err != nil {
		if errors.Is(err, platform.ErrStopGuardChanged) {
			return StopProcResult{Line: fmt.Sprintf("kept process %d (it changed since the listing)", pid)}
		}
		return StopProcResult{Err: err}
	}
	return StopProcResult{Line: fmt.Sprintf("stopped process %d (%s)", pid, name), Stopped: true}
}
