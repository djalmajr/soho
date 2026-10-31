package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/backend"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/transport"
)

// copiesHeader is the first line of the per-workspace copy registry,
// copies.tsv. It names only the first six columns: the task and report
// columns follow them unnamed, so an earlier binary, which skips this
// header and keeps the first six fields of a line, reads the file as before.
const copiesHeader = "path\towner\tpane\tcreated\tsource\torigin"

// namedTaskCopiesHeader is the eight-name header an intermediate build
// wrote; the reader skips it, and nothing writes it any more.
const namedTaskCopiesHeader = "path\towner\tpane\tcreated\tsource\torigin\ttask\treport"

const (
	// copiesLockName sits next to the registry: the exclusive creation of
	// this file is the inter-process lock of the registry transactions.
	copiesLockName = "copies.tsv.lock"
	// New lock attempts every 50 ms, for at most CopiesLockTimeout.
	registryLockPoll = 50 * time.Millisecond
	// A lock older than 60 s by mtime is taken as abandoned and removed.
	registryLockStale = 60 * time.Second
)

// CopiesLockTimeout is how long a caller waits for the registry lock before
// giving up (copy and process registries alike). Tests shorten it so they
// never wait the real 10 s.
var CopiesLockTimeout = 10 * time.Second

// RegistryLockedError is the registry lock timeout error; its message is
// the exact text the commands print with exit 4, with the registry name in
// front.
type RegistryLockedError struct {
	Kind string
	Path string
}

func (e *RegistryLockedError) Error() string {
	return fmt.Sprintf("%s: registry is locked by another soho (%s)", e.Kind, e.Path)
}

// CopiesLockedError is the copies flavor of the registry lock error.
type CopiesLockedError = RegistryLockedError

// LockCopies takes the inter-process copies.tsv lock: the exclusive
// creation (O_CREATE|O_EXCL) of copies.tsv.lock next to the registry, with
// the PID and the time inside. It retries every 50 ms until the timeout; a
// lock older than 60 s by mtime is abandoned and taken. It returns the
// unlock function (defer it, it removes the lock) or a *CopiesLockedError.
func LockCopies(stateDir string) (func(), error) {
	return lockRegistry(stateDir, copiesLockName, "copies")
}

// lockRegistry is the shared inter-process registry lock (copies and
// procs): the exclusive file creation next to the registry, with the same
// retry, stale and timeout rules.
func lockRegistry(stateDir, lockName, kind string) (func(), error) {
	lockPath := filepath.Join(stateDir, lockName)
	deadline := time.Now().Add(CopiesLockTimeout)
	for {
		err := takeRegistryLock(lockPath)
		if err == nil {
			return func() { _ = os.Remove(lockPath) }, nil
		}
		// On Windows an exclusive create of a lock file that another
		// process has just removed (its deletion still pending) fails with
		// ERROR_ACCESS_DENIED instead of "exists": that is contention too,
		// retried until the timeout, which then reports this error.
		pendingDelete := runtime.GOOS == "windows" && errors.Is(err, os.ErrPermission)
		if !errors.Is(err, os.ErrExist) && !pendingDelete {
			return nil, err
		}
		if pendingDelete {
			if time.Now().After(deadline) {
				return nil, err
			}
			time.Sleep(registryLockPoll)
			continue
		}
		if held, statErr := os.Stat(lockPath); statErr == nil && time.Since(held.ModTime()) > registryLockStale {
			_ = os.Remove(lockPath)
			continue
		}
		if time.Now().After(deadline) {
			return nil, &RegistryLockedError{Kind: kind, Path: lockPath}
		}
		time.Sleep(registryLockPoll)
	}
}

func takeRegistryLock(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(f, "%d %s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339))
	return f.Close()
}

// CopyRow is one line of copies.tsv: a copy directory and who made it.
type CopyRow struct {
	Path    string // the canonical path
	Owner   string // the roster name, or "-"
	Pane    string // the pane id, or "-"
	Created string // ISO-8601 UTC
	Source  string // the copied origin, or "-"
	Origin  string // "mutation-copy" or "add"
	// Task and Report are the owner's task pointer at registration time
	// (task_report and current), or "-" without an owner or a pointer. A
	// six-column line from an earlier binary reads them as "".
	Task   string
	Report string
}

func (r CopyRow) fields() []string {
	return []string{r.Path, r.Owner, r.Pane, r.Created, r.Source, r.Origin, dashIfEmpty(r.Task), dashIfEmpty(r.Report)}
}

func dashIfEmpty(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

// CopiesFile returns the copy registry path inside the state dir.
func CopiesFile(stateDir string) string { return filepath.Join(stateDir, "copies.tsv") }

// ReadCopies reads the copy registry without the header, in file order. A
// registry that does not exist yet reads as no rows.
func ReadCopies(stateDir string) ([]CopyRow, error) {
	raw, err := platform.ReadTextFile(CopiesFile(stateDir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []CopyRow{}, nil
		}
		return nil, err
	}
	out := []CopyRow{}
	for _, line := range strings.Split(raw, "\n") {
		if line == "" || line == copiesHeader || line == namedTaskCopiesHeader {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 6 {
			return nil, fmt.Errorf("copies.tsv: malformed line %q", line)
		}
		row := CopyRow{Path: fields[0], Owner: fields[1], Pane: fields[2], Created: fields[3], Source: fields[4], Origin: fields[5]}
		if len(fields) >= 8 {
			row.Task, row.Report = fields[6], fields[7]
		}
		out = append(out, row)
	}
	return out, nil
}

// WriteCopies rewrites the copy registry atomically (temporary file plus
// rename, like the other state files).
func WriteCopies(stateDir string, rows []CopyRow) error {
	lines := []string{copiesHeader}
	for _, row := range rows {
		lines = append(lines, strings.Join(row.fields(), "\t"))
	}
	return platform.AtomicWrite(CopiesFile(stateDir), strings.Join(lines, "\n")+"\n")
}

// UpsertCopies records row, replacing the line with the same path when it
// exists, so re-registering a path never duplicates it. The whole
// read-modify-write runs under the inter-process registry lock, so two
// concurrent registrations never lose a line.
func UpsertCopies(stateDir string, row CopyRow) error {
	unlock, err := LockCopies(stateDir)
	if err != nil {
		return err
	}
	defer unlock()
	rows, err := ReadCopies(stateDir)
	if err != nil {
		return err
	}
	for i, existing := range rows {
		if existing.Path == row.Path {
			rows[i] = row
			return WriteCopies(stateDir, rows)
		}
	}
	return WriteCopies(stateDir, append(rows, row))
}

// DropCopiesLines re-reads the registry under the inter-process lock and
// rewrites it without the lines whose path is in removed, keeping every
// other line, including lines added meanwhile. The slow removals happen
// outside the lock, by the caller.
func DropCopiesLines(stateDir string, removed map[string]bool) error {
	unlock, err := LockCopies(stateDir)
	if err != nil {
		return err
	}
	defer unlock()
	return DropCopiesLinesHeld(stateDir, removed)
}

// DropCopiesLinesHeld is DropCopiesLines for a caller that already holds
// the registry lock (LockCopies): it re-reads and rewrites the registry
// without taking the lock again.
func DropCopiesLinesHeld(stateDir string, removed map[string]bool) error {
	rows, err := ReadCopies(stateDir)
	if err != nil {
		return err
	}
	out := make([]CopyRow, 0, len(rows))
	for _, row := range rows {
		if !removed[row.Path] {
			out = append(out, row)
		}
	}
	return WriteCopies(stateDir, out)
}

// WorkspaceIDOrError is the non-dying variant of WorkspaceID for callers
// that must keep running when the workspace cannot be resolved: a missing
// herdr CLI or a failing pane current comes back as an error.
func WorkspaceIDOrError(env platform.Env) (string, error) {
	svc := backend.New(env)
	if id := svc.Runtime.Caller().WorkspaceID; id != "" {
		return id, nil
	}
	if env.Get("SOHO_TRANSPORT") == "hc" {
		// hc has no pane current: the session is the workspace, and when it
		// cannot be resolved that comes back as the hc error, never a Herdr
		// error (DJA-604 stage 2b).
		if svc.Runtime.Caller().Inside {
			return "", errors.New("transport=hc: the caller terminal's soho session could not be read (hc get failed for HC_TERMINAL_ID)")
		}
		return "", errors.New("transport=hc: not inside an hc terminal (HC_TERMINAL_ID is not set)")
	}
	rep := svc.Legacy.PaneCurrentRaw(nil, transport.Call{Timeout: 30 * time.Second})
	if rep.Process.NotFound {
		return "", errors.New("herdr CLI not found in PATH")
	}
	if rep.Process.Status == nil || *rep.Process.Status != 0 {
		code := 0
		if rep.Process.Status != nil {
			code = *rep.Process.Status
		}
		return "", fmt.Errorf("herdr pane current failed (exit %d)", code)
	}
	v, err := jsonjs.Parse([]byte(rep.Value))
	if err != nil {
		return "", errors.New("herdr pane current returned invalid JSON")
	}
	root, ok := v.(*jsonjs.Object)
	if !ok {
		return "", errors.New("herdr pane current returned a non-object result")
	}
	result, _ := root.Get("result")
	resultObject, ok := result.(*jsonjs.Object)
	if !ok {
		return "", errors.New("herdr pane current returned a non-object result")
	}
	pane, _ := resultObject.Get("pane")
	paneObject, ok := pane.(*jsonjs.Object)
	if !ok {
		return "", errors.New("herdr pane current returned a non-object pane")
	}
	id, ok := paneObject.Get("workspace_id")
	if !ok || id == nil {
		return "null", nil
	}
	if s, ok := id.(string); ok {
		return s, nil
	}
	switch id.(type) {
	case *jsonjs.Object, []any:
		return "", errors.New("herdr pane current returned an unsupported workspace_id type")
	}
	return jsString(id), nil
}

// StateDirResolved is the non-dying variant of StateDir for callers that
// must keep running when the state dir cannot be resolved or created (the
// mutation-copy registration). The state-inside-skill refusal and every I/O
// failure come back as an error, decided before anything is written.
func StateDirResolved(ctx *Config, env platform.Env, cwd string) (string, error) {
	if !Nowrite(env) {
		if skill := StateInSkill(ctx, env, cwd); skill != "" {
			return "", fmt.Errorf("the state dir '%s' would be inside the soho skill ('%s'); run soho from the project's directory (nothing was written)", StateRootPath(ctx, env, cwd), skill)
		}
	}
	workspace, err := WorkspaceIDOrError(env)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(StateRootPath(ctx, env, cwd), workspace)
	if Nowrite(env) {
		return dir, nil
	}
	for _, name := range []string{"briefs", "reports", "wait"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o777); err != nil {
			return "", err
		}
	}
	roster := filepath.Join(dir, "agents.tsv")
	if _, err := os.Stat(roster); os.IsNotExist(err) {
		if err := os.WriteFile(roster, []byte(rosterHeader), 0o666); err != nil {
			return "", err
		}
	}
	return dir, nil
}
