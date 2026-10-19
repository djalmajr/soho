package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/taskreport"
)

const (
	copiesUsage = "usage: soho copies [add <path> [--agent <name>]]"
	gcUsage     = "usage: soho gc [--yes] [--older-than HOURS] [--include-unregistered] [--include-unregistered-procs]"

	// copiesTimeLayout is the ISO-8601 UTC format the registry stores in the
	// created column (core.FrictionISO).
	copiesTimeLayout = "2006-01-02T15:04:05Z"

	// gcUnregisteredHeader is the separate output block for mutation copies
	// that the registry does not know.
	gcUnregisteredHeader = "unregistered mutation copies (another project or an older version may own them):"
)

// copyRefusal is the single safety check (decision 7) that release, gc and
// copies add run before removing or registering a copy path. It returns the
// refusal cause, or "" when the path passes.
func copyRefusal(path string, env platform.Env, cwd string) string {
	_, cause := checkCopyPath(path, env, cwd)
	return cause
}

// checkCopyPath is copyRefusal with the resolved path on success. The
// identity comparison runs before the textual checks, which stay as the
// second barrier.
func checkCopyPath(path string, env platform.Env, cwd string) (string, string) {
	if !filepath.IsAbs(path) {
		return "", "not an absolute path"
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", "does not exist"
	}
	if info, lstatErr := os.Lstat(path); lstatErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", "is a symlink"
	}
	if isFilesystemRoot(resolved) {
		return "", "is the filesystem root"
	}
	if cause := protectedIdentityRefusal(resolved, env, cwd); cause != "" {
		return "", cause
	}
	if home := mutationCopyHome(env); home != "" && (resolved == home || inside(resolved, home)) {
		return "", "is HOME or above it"
	}
	if root := platform.StateProjectRoot(env, cwd); root != "" && (inside(root, resolved) || inside(resolved, root)) {
		return "", "is the project root or inside it"
	}
	if _, gitErr := os.Lstat(filepath.Join(resolved, ".git")); gitErr == nil {
		return "", "contains a .git (another repository or a worktree)"
	}
	return resolved, ""
}

// protectedIdentityRefusal compares the identity of the protected
// directories (os.SameFile), not their text: EvalSymlinks does not normalize
// the case of the components on the current macOS volume, so a textual path
// can be the same directory as the HOME or the project root. It refuses
// when the path is the same file as the HOME or any of the HOME's ancestors
// (up to the root), or as the project root, one of its ancestors, or an
// ancestor of the path (it is inside it). A HOME or a root that cannot be
// read with Stat refuses: when in doubt, nothing is deleted.
func protectedIdentityRefusal(resolved string, env platform.Env, cwd string) string {
	resolvedInfo, err := os.Stat(resolved)
	if err != nil {
		return "cannot verify the copy path"
	}
	home := mutationCopyHome(env)
	if home != "" {
		found, verified := sameDirChain(home, resolvedInfo)
		if !verified {
			return "cannot verify HOME"
		}
		if found {
			return "is HOME or above it"
		}
	}
	if root := platform.StateProjectRoot(env, cwd); root != "" {
		rootInfo, err := os.Stat(root)
		if err != nil {
			return "cannot verify the project root"
		}
		atOrAbove, verifiedAbove := sameDirChain(root, resolvedInfo)
		inside, verifiedInside := sameDirChain(resolved, rootInfo)
		if !verifiedAbove || !verifiedInside {
			return "cannot verify the project root"
		}
		if atOrAbove || inside {
			return "is the project root or inside it"
		}
	}
	return ""
}

// sameDirChain reports whether dir or one of its textual ancestors up to
// the root is the same file (os.SameFile) as target. The second result is
// false when a directory along the chain cannot be read, so the caller
// refuses when in doubt.
func sameDirChain(dir string, target fs.FileInfo) (found bool, verified bool) {
	for {
		info, err := os.Stat(dir)
		if err != nil {
			return false, false
		}
		if os.SameFile(info, target) {
			return true, true
		}
		if dir == filepath.Dir(dir) {
			return false, true
		}
		dir = filepath.Dir(dir)
	}
}

// checkedCopy runs the decision-7 check and, when the path passes, also
// captures the identity of the resolved path's parent. The removal helper
// proves the directory it opens is still that parent, so the removal stays
// bound to the validated directory.
func checkedCopy(path string, env platform.Env, cwd string) (resolved string, parentInfo fs.FileInfo, cause string) {
	resolved, cause = checkCopyPath(path, env, cwd)
	if cause != "" {
		return "", nil, cause
	}
	parentInfo, err := statByHandle(filepath.Dir(resolved))
	if err != nil {
		return "", nil, "cannot verify the copy path"
	}
	return resolved, parentInfo, ""
}

// statByHandle stats an open handle of path, not the path. On Windows a
// FileInfo from os.Stat loads its file id lazily, by path, when os.SameFile
// first compares it: a parent swapped after the check would then compare as
// the same file. A handle's Stat carries the id of the directory that was
// opened.
func statByHandle(path string) (fs.FileInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.Stat()
}

var errCopyChanged = errors.New("changed after the check")

// removeCopyHook runs between the decision-7 check and the removal; tests
// swap the copy's parent there to prove the removal stays bound to the
// validated directory.
var removeCopyHook func(path string)

// removeMarkerHook runs between the copy's removal and its marker's; tests
// swap the parent there to prove the marker's removal stays bound to the
// verified directory too.
var removeMarkerHook func(parent string)

// removeCopy runs the (test-only) hook and the descriptor-bound removal.
func removeCopy(resolved string, parentInfo fs.FileInfo) error {
	if removeCopyHook != nil {
		removeCopyHook(resolved)
	}
	return removeVerifiedCopy(resolved, parentInfo)
}

// removeVerifiedCopy deletes the copy directory relative to a descriptor of
// its parent (os.OpenRoot + Root.RemoveAll), which a parent swapped for a
// symlink cannot redirect. Before the removal it proves the opened parent
// is the same file (os.SameFile) as the parent the check validated and the
// entry inside it is a directory, not a symlink; a failed proof removes
// nothing and returns errCopyChanged (the copy stays, reported as
// "changed after the check"). The copy's origin marker goes with it through
// the same verified parent descriptor, best effort and only when it is a
// regular file: a parent swapped after the copy's removal cannot redirect
// the marker's removal outside that directory either.
func removeVerifiedCopy(resolved string, parentInfo fs.FileInfo) error {
	parent := filepath.Dir(resolved)
	base := filepath.Base(resolved)
	root, err := os.OpenRoot(parent)
	if err != nil {
		return errCopyChanged
	}
	defer root.Close()
	// The opened parent's identity comes from a handle too (see statByHandle).
	dir, err := root.Open(".")
	if err != nil {
		return errCopyChanged
	}
	opened, err := dir.Stat()
	_ = dir.Close()
	if err != nil || !os.SameFile(opened, parentInfo) {
		return errCopyChanged
	}
	entry, err := root.Lstat(base)
	if err != nil || entry.Mode()&fs.ModeSymlink != 0 || !entry.IsDir() {
		return errCopyChanged
	}
	if err := root.RemoveAll(base); err != nil {
		return err
	}
	if removeMarkerHook != nil {
		removeMarkerHook(parent)
	}
	marker := originMarkerPath(base)
	if info, err := root.Lstat(marker); err == nil && info.Mode().IsRegular() {
		_ = root.Remove(marker)
	}
	return nil
}

// rosterAgentForPane returns the roster name whose pane is pane, or "".
func rosterAgentForPane(stateDir, pane string) string {
	if pane == "" {
		return ""
	}
	for _, line := range core.RosterRows(stateDir) {
		fields := strings.Split(line, "\t")
		if len(fields) > 1 && fields[1] == pane && fields[0] != "" {
			return fields[0]
		}
	}
	return ""
}

// cmdCopies lists the copy registry, or registers a copy made by hand.
func cmdCopies(argv []string, ctx *core.Config, env platform.Env, cwd string) int {
	if len(argv) == 0 {
		return copiesList(ctx, env, cwd)
	}
	if argv[0] != "add" {
		_, _ = fmt.Fprintf(platform.Stderr, "soho: %s\n", copiesUsage)
		return 2
	}
	return copiesAdd(argv[1:], ctx, env, cwd)
}

// copiesList prints one line per registered copy, "<path>  <owner>  <age>
// <state>". It is read-only: it removes nothing and writes nothing, and it
// does not show sizes.
func copiesList(ctx *core.Config, env platform.Env, cwd string) int {
	sd := core.StateDir(ctx, env, cwd)
	rows, err := core.ReadCopies(sd)
	if err != nil {
		core.DieFriction(fmt.Sprintf("copies: cannot read the copy registry (%v)", err), 4, frictionLogPath, "copies")
	}
	if len(rows) == 0 {
		// An empty registry says so, apart from a silent failure (G3).
		_, _ = fmt.Fprintln(platform.Stdout, "no registered copies")
		return 0
	}
	now := platform.Now()
	for _, row := range rows {
		_, _ = fmt.Fprintf(platform.Stdout, "%s  %s  %s  %s\n", row.Path, row.Owner, humanAge(copyAge(row, now)), copyRowState(row, sd, env))
	}
	return 0
}

// copyRowState is the state of a registry line: missing when the path is
// gone, owned when the owner is a live roster agent, orphan otherwise (the
// owner is "-", is not in the roster, or is gone). An owner Herdr cannot
// answer for stays owned: gc must not drop a copy it cannot prove orphaned.
func copyRowState(row core.CopyRow, sd string, env platform.Env) string {
	if _, err := os.Stat(row.Path); err != nil {
		return "missing"
	}
	if row.Owner != "-" && core.RosterLine(sd, row.Owner) != "" && herdr.AgentState(row.Owner, env, herdr.Timeout, nil).State != "gone" {
		return "owned"
	}
	return "orphan"
}

// gcProcState classifies one process line for gc: owned (running, the
// owner is a live roster agent — the copies' rule, never touched), orphan
// (running, the owner is not live), or gone/reused (any owner: --yes drops
// the line, and a reused pid is never signalled).
func gcProcState(row core.ProcRow, sd string, env platform.Env) string {
	if st := core.ProcRowState(row, env); st != "running" {
		return st
	}
	if row.Owner != "-" && core.RosterLine(sd, row.Owner) != "" && herdr.AgentState(row.Owner, env, herdr.Timeout, nil).State != "gone" {
		return "owned"
	}
	return "orphan"
}

// gcOrphanProcs is the dry-run list: the running lines gc would stop.
func gcOrphanProcs(rows []core.ProcRow, sd string, env platform.Env) []core.ProcRow {
	out := []core.ProcRow{}
	for _, row := range rows {
		if gcProcState(row, sd, env) == "orphan" {
			out = append(out, row)
		}
	}
	return out
}

// gcProjectOrphans is gc's read of the unregistered orphan processes of
// the project (procs' block). Windows detects none, with no line; a read
// that failed is a warning on stderr and lists none.
func gcProjectOrphans(sd string, procRows []core.ProcRow, env platform.Env, cwd string) []projectOrphan {
	orphans, err := projectOrphans(sd, procRows, env, cwd)
	if err != nil {
		if !errors.Is(err, platform.ErrOrphansNotDetected) {
			_, _ = fmt.Fprintf(platform.Stderr, "gc: could not list the unregistered processes (%v)\n", err)
		}
		return nil
	}
	return orphans
}

// gcOrphanWouldStop is the age gate of a stoppable orphan: its start is
// at least threshold ago.
func gcOrphanWouldStop(orphan projectOrphan, threshold time.Duration, now time.Time) bool {
	return orphan.stoppable() && orphan.age(now) >= threshold
}

func gcOrphansWouldStop(orphans []projectOrphan, threshold time.Duration, now time.Time) bool {
	for _, orphan := range orphans {
		if gcOrphanWouldStop(orphan, threshold, now) {
			return true
		}
	}
	return false
}

// gcPrintOrphanProcs is the unregistered processes' block without a stop:
// "would stop <line>" for a stoppable one past --older-than, "listed only
// <line>" for one in the scratch, the root or a worktree; a stoppable one
// younger than --older-than is not listed (the orphan copies' rule). The
// closing hint names the flag that stops them. Nothing to show prints nothing.
func gcPrintOrphanProcs(orphans []projectOrphan, threshold time.Duration, now time.Time) {
	lines := []string{}
	for _, orphan := range orphans {
		switch {
		case gcOrphanWouldStop(orphan, threshold, now):
			lines = append(lines, "would stop "+orphan.line(now))
		case !orphan.stoppable():
			lines = append(lines, "listed only "+orphan.line(now))
		}
	}
	if len(lines) == 0 {
		return
	}
	_, _ = fmt.Fprintln(platform.Stdout, orphanHeader)
	for _, line := range lines {
		_, _ = fmt.Fprintln(platform.Stdout, line)
	}
	if gcOrphansWouldStop(orphans, threshold, now) {
		_, _ = fmt.Fprintln(platform.Stdout, "run 'soho gc --yes --include-unregistered-procs' to stop them")
	}
}

// gcStopOrphanProcs is --include-unregistered-procs --yes: each stoppable
// orphan past --older-than is stopped through core.StopUnregisteredProc,
// which re-reads the start identity and the cwd (orphanStill) before each
// signal round and keeps a process that changed; the scratch, root and
// worktree ones are only listed. It returns how many were stopped.
func gcStopOrphanProcs(orphans []projectOrphan, threshold time.Duration, now time.Time, env platform.Env) int {
	header := false
	stopped := 0
	for _, orphan := range orphans {
		listedOnly := !orphan.stoppable()
		if !listedOnly && !gcOrphanWouldStop(orphan, threshold, now) {
			continue
		}
		if !header {
			_, _ = fmt.Fprintln(platform.Stdout, orphanHeader)
			header = true
		}
		if listedOnly {
			_, _ = fmt.Fprintln(platform.Stdout, "listed only "+orphan.line(now))
			continue
		}
		res := core.StopUnregisteredProc(orphan.Pid, orphan.Name, orphanStill(orphan, env), env)
		if res.Line != "" {
			_, _ = fmt.Fprintln(platform.Stdout, res.Line)
		}
		if res.Err != nil {
			core.Warn(fmt.Sprintf("gc: could not stop process %d (%v)", orphan.Pid, res.Err), frictionLogPath, "gc")
			continue
		}
		if res.Stopped {
			stopped++
		}
	}
	return stopped
}

// copyTaskColumns are the task and report columns of a new registry line:
// the task_report and current of the owner's task pointer, or "-" without
// an owner or a pointer.
func copyTaskColumns(sd, owner string) (string, string) {
	if owner == "" || owner == "-" {
		return "-", "-"
	}
	pointer := taskreport.ReadTaskReportPointer(sd, owner)
	if pointer == nil {
		return "-", "-"
	}
	task, _ := pointer.Get("task_report")
	current, _ := pointer.Get("current")
	return dashIfBlank(task.(string)), dashIfBlank(current.(string))
}

func dashIfBlank(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

// gcIdleOwnerMark closes the dry-run line of a copy of a live owner that gc
// offers because the owner is idle and its tasks are closed.
const gcIdleOwnerMark = "(idle owner, task closed)"

// gcIdleOwnerChanged is the kept-line cause when the owner's state or task
// pointer changed between the listing and the removal.
const gcIdleOwnerChanged = "the owner's state or task changed"

// gcIdleClosed is what gc saw when it offered a live owner's copy: the
// owner's state and its current task pointer, compared again before the
// removal.
type gcIdleClosed struct {
	state   string
	task    string
	current string
}

// gcIdleOwnerCopy decides whether gc offers the copy of a live owner: the
// owner is in the roster and idle or done; the line carries the task and
// report it was registered under (never a line from an earlier binary) and
// that report is a non-empty file; the owner's current task is closed too
// (its current report is a non-empty file); and both the line's created and
// the newest modification of any entry of the copy, symlinks not followed,
// are at least threshold old. ok is false when any of them fails.
func gcIdleOwnerCopy(row core.CopyRow, sd string, env platform.Env, now time.Time, threshold time.Duration) (gcIdleClosed, bool) {
	if row.Owner == "-" || core.RosterLine(sd, row.Owner) == "" {
		return gcIdleClosed{}, false
	}
	if row.Task == "" || row.Task == "-" || row.Report == "" || row.Report == "-" {
		return gcIdleClosed{}, false
	}
	if copyAge(row, now) < threshold {
		return gcIdleClosed{}, false
	}
	state := herdr.AgentState(row.Owner, env, herdr.Timeout, nil).State
	if state != "idle" && state != "done" {
		return gcIdleClosed{}, false
	}
	if !nonEmptyFile(row.Report) {
		return gcIdleClosed{}, false
	}
	task, current := copyTaskColumns(sd, row.Owner)
	if current == "-" || !nonEmptyFile(current) {
		return gcIdleClosed{}, false
	}
	newest, ok := newestModification(row.Path)
	if !ok || now.Sub(newest) < threshold {
		return gcIdleClosed{}, false
	}
	return gcIdleClosed{state: state, task: task, current: current}, true
}

// gcIdleOwnerStillClosed re-reads the owner's state and task pointer right
// before the removal: the copy goes only when the rule still holds and
// neither the state nor the pointer changed since gc offered it.
func gcIdleOwnerStillClosed(row core.CopyRow, seen gcIdleClosed, sd string, env platform.Env, now time.Time, threshold time.Duration) bool {
	fresh, ok := gcIdleOwnerCopy(row, sd, env, now, threshold)
	return ok && fresh == seen
}

// sameCopyLine is true when two registry lines of a copy agree on path,
// owner, pane, created, task and report.
func sameCopyLine(a, b core.CopyRow) bool {
	return a.Path == b.Path && a.Owner == b.Owner && a.Pane == b.Pane && a.Created == b.Created && a.Task == b.Task && a.Report == b.Report
}

// gcCurrentCopyLine is the registry line of path as copies.tsv holds it
// now; ok is false when the line is gone or the registry cannot be read.
func gcCurrentCopyLine(sd, path string) (core.CopyRow, bool) {
	rows, err := core.ReadCopies(sd)
	if err != nil {
		return core.CopyRow{}, false
	}
	for _, row := range rows {
		if row.Path == path {
			return row, true
		}
	}
	return core.CopyRow{}, false
}

// gcRemoveIdleOwnerCopy removes the copy of a live, idle owner that gc
// offered, holding the copies.tsv lock (core.LockCopies, the lock copies
// add takes through UpsertCopies) from the check to the line's drop: the
// line copies.tsv holds now must be the one gc listed, then the owner's
// state and task pointer are re-read; any change keeps the copy. A copies
// add of the same path waits for the section to end. It reports whether
// the copy was removed.
func gcRemoveIdleOwnerCopy(cand gcCandidate, sd string, env platform.Env, cwd string, threshold time.Duration) bool {
	unlock, err := core.LockCopies(sd)
	if err != nil {
		var locked *core.CopiesLockedError
		if errors.As(err, &locked) {
			core.DieFriction(err.Error(), 4, frictionLogPath, "gc")
		}
		core.DieFriction(fmt.Sprintf("gc: could not take the copy registry lock (%v)", err), 4, frictionLogPath, "gc")
	}
	defer unlock()
	current, ok := gcCurrentCopyLine(sd, cand.path)
	if !ok || !sameCopyLine(current, cand.row) || !gcIdleOwnerStillClosed(current, *cand.idle, sd, env, platform.Now(), threshold) {
		_, _ = fmt.Fprintf(platform.Stdout, "kept %s (%s)\n", cand.path, gcIdleOwnerChanged)
		return false
	}
	resolved, parentInfo, cause := checkedCopy(cand.path, env, cwd)
	if cause != "" {
		_, _ = fmt.Fprintf(platform.Stdout, "kept %s (%s)\n", cand.path, cause)
		return false
	}
	if err := removeCopy(resolved, parentInfo); err != nil {
		keptCopyError(cand.path, err)
		return false
	}
	if err := core.DropCopiesLinesHeld(sd, map[string]bool{cand.path: true}); err != nil {
		core.DieFriction(fmt.Sprintf("gc: cannot update the copy registry (%v)", err), 4, frictionLogPath, "gc")
	}
	_, _ = fmt.Fprintf(platform.Stdout, "removed %s  %s\n", cand.path, humanSize(cand.size))
	return true
}

func nonEmptyFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Size() > 0
}

// newestModification is the newest mtime of the copy root and every entry
// below it, read without following symlinks. ok is false when the walk
// fails anywhere, so an unreadable copy never reads as old.
func newestModification(root string) (time.Time, bool) {
	var newest time.Time
	err := filepath.WalkDir(root, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		return nil
	})
	return newest, err == nil
}

// copyAge is how long the copy has existed; an unparseable or future created
// reads as 0, so gc never treats an unknown age as old.
func copyAge(row core.CopyRow, now time.Time) time.Duration {
	created, err := time.Parse(copiesTimeLayout, row.Created)
	if err != nil {
		return 0
	}
	age := now.Sub(created)
	if age < 0 {
		return 0
	}
	return age
}

// copiesAdd registers a copy made by hand (a reviewer's copy or the
// orchestrator's own) with origin add.
func copiesAdd(argv []string, ctx *core.Config, env platform.Env, cwd string) int {
	var path, agent string
	agentSeen := false
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		if arg == "--agent" {
			if agentSeen || i+1 >= len(argv) || argv[i+1] == "" {
				return copiesAddUsageError()
			}
			agent = argv[i+1]
			agentSeen = true
			i++
			continue
		}
		if strings.HasPrefix(arg, "-") {
			return copiesAddUsageError()
		}
		if path != "" {
			return copiesAddUsageError()
		}
		path = arg
	}
	if path == "" {
		return copiesAddUsageError()
	}
	if cause := copyRefusal(path, env, cwd); cause != "" {
		_, _ = fmt.Fprintf(platform.Stderr, "copies add: refusing '%s': %s\n", path, cause)
		return 2
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || !isExistingDir(resolved) {
		_, _ = fmt.Fprintf(platform.Stderr, "copies add: refusing '%s': not an existing directory\n", path)
		return 2
	}
	sd := core.StateDir(ctx, env, cwd)
	pane := env.Get("HERDR_PANE_ID")
	owner := "-"
	if agentSeen {
		line := core.RosterLine(sd, agent)
		if line == "" {
			core.DieFriction(fmt.Sprintf("copies add: agent '%s' is not in the roster (state dir: %s)", agent, sd), 3, frictionLogPath, "copies")
		}
		owner = agent
		if fields := strings.Split(line, "\t"); len(fields) > 1 && fields[1] != "" {
			pane = fields[1]
		}
	} else if pane != "" {
		owner = rosterAgentForPane(sd, pane)
		if owner == "" {
			owner = "-"
		}
	}
	if pane == "" {
		pane = "-"
	}
	task, report := copyTaskColumns(sd, owner)
	row := core.CopyRow{Path: resolved, Owner: owner, Pane: pane, Created: core.FrictionISO(platform.Now()), Source: "-", Origin: "add", Task: task, Report: report}
	if err := core.UpsertCopies(sd, row); err != nil {
		var locked *core.CopiesLockedError
		if errors.As(err, &locked) {
			core.DieFriction(err.Error(), 4, frictionLogPath, "copies")
		}
		core.DieFriction(fmt.Sprintf("copies add: could not register '%s' (%v)", path, err), 4, frictionLogPath, "copies")
	}
	return 0
}

func copiesAddUsageError() int {
	_, _ = fmt.Fprintf(platform.Stderr, "soho: %s\n", copiesUsage)
	return 2
}

func isExistingDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// gcCandidate is one registry line gc may act on: an orphan old enough, a
// copy of an idle owner whose tasks are closed (idle, with what gc saw), or
// a missing line that only leaves the registry.
type gcCandidate struct {
	path    string
	owner   string
	age     time.Duration
	size    int64
	missing bool
	row     core.CopyRow
	idle    *gcIdleClosed
}

// gcUnregisteredCopy is a soho-mutation-* directory below TMPDIR that
// the registry does not know, with the class of its origin marker.
type gcUnregisteredCopy struct {
	path   string
	size   int64
	age    time.Duration
	origin string
}

// cmdGc lists the orphan copies the registry holds (dry run), or removes the
// ones at least --older-than hours old. Unregistered mutation copies are
// always listed in their own block and are only removed with
// --include-unregistered --yes. Unregistered orphan processes of the
// project are listed in their own block and only stopped with
// --include-unregistered-procs --yes.
func cmdGc(argv []string, ctx *core.Config, env platform.Env, cwd string) int {
	yes, includeUnregistered, includeProcs := false, false, false
	hours := "2"
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		switch arg {
		case "--yes":
			yes = true
		case "--include-unregistered":
			includeUnregistered = true
		case "--include-unregistered-procs":
			includeProcs = true
		case "--older-than":
			if i+1 >= len(argv) || argv[i+1] == "" {
				return gcUsageError()
			}
			hours = argv[i+1]
			i++
		default:
			return gcUsageError()
		}
	}
	thresholdHours, err := parseGcHours(hours)
	if err != nil {
		_, _ = fmt.Fprintf(platform.Stderr, "soho: gc: --older-than expects a non-negative integer (%s)\n", hours)
		return 2
	}
	sd := core.StateDir(ctx, env, cwd)
	// The pressure line goes out first, on every run (stdout): the s85
	// warning text when the disk or the swap passed the configured limits
	// (same keys, defaults and strictness), or a "none" line with the
	// measured values; a metric that could not be measured shows as
	// "<metric> not measured". No throttle, no stamp.
	_, _ = fmt.Fprintln(platform.Stdout, gcPressureLine(ctx, env, sd))
	rows, err := core.ReadCopies(sd)
	if err != nil {
		core.DieFriction(fmt.Sprintf("gc: cannot read the copy registry (%v)", err), 4, frictionLogPath, "gc")
	}
	procRows, err := core.ReadProcs(sd)
	if err != nil {
		core.DieFriction(fmt.Sprintf("gc: cannot read the process registry (%v)", err), 4, frictionLogPath, "gc")
	}
	now := platform.Now()
	threshold := time.Duration(thresholdHours) * time.Hour

	candidates := []gcCandidate{}
	for _, row := range rows {
		switch copyRowState(row, sd, env) {
		case "missing":
			candidates = append(candidates, gcCandidate{path: row.Path, owner: row.Owner, age: copyAge(row, now), missing: true})
		case "orphan":
			age := copyAge(row, now)
			if age >= threshold {
				candidates = append(candidates, gcCandidate{path: row.Path, owner: row.Owner, age: age, size: copyDirSize(row.Path)})
			}
		case "owned":
			// A live owner's copy is offered only when the owner is idle
			// and the copy's task and its current one are closed.
			if seen, ok := gcIdleOwnerCopy(row, sd, env, now, threshold); ok {
				candidates = append(candidates, gcCandidate{path: row.Path, owner: row.Owner, age: copyAge(row, now), size: copyDirSize(row.Path), row: row, idle: &seen})
			}
		}
		// the other owned lines are never touched
	}
	unregistered := gcUnregistered(env, rows, now, threshold, cwd)
	orphanProcs := gcProjectOrphans(sd, procRows, env, cwd)

	total := int64(0)
	for _, cand := range candidates {
		total += cand.size
	}
	if !yes {
		// Orphan processes: the running lines whose owner is not a live
		// roster agent (the copies' rule). Gone and reused lines are not
		// listed; --yes drops them with a note. Processes of live owners
		// are never candidates.
		orphan := gcOrphanProcs(procRows, sd, env)
		// Nothing to remove or stop: only the line itself, no total and no
		// hint (G1).
		if len(candidates) == 0 && len(unregistered) == 0 && len(orphan) == 0 && !gcOrphansWouldStop(orphanProcs, threshold, now) {
			_, _ = fmt.Fprintln(platform.Stdout, "nothing to remove")
			gcPrintOrphanProcs(orphanProcs, threshold, now)
			return 0
		}
		if len(candidates) > 0 {
			for _, cand := range candidates {
				if cand.idle != nil {
					_, _ = fmt.Fprintf(platform.Stdout, "would remove %s  %s  %s  %s  %s\n", cand.path, humanSize(cand.size), cand.owner, humanAge(cand.age), gcIdleOwnerMark)
					continue
				}
				_, _ = fmt.Fprintf(platform.Stdout, "would remove %s  %s  %s  %s\n", cand.path, humanSize(cand.size), cand.owner, humanAge(cand.age))
			}
			_, _ = fmt.Fprintf(platform.Stdout, "total: %s\n", humanSize(total))
			_, _ = fmt.Fprintln(platform.Stdout, "run 'soho gc --yes' to remove them")
		}
		if len(unregistered) > 0 {
			gcPrintUnregistered(unregistered, false)
			if len(candidates) == 0 {
				// Only unregistered copies: name the flag that actually removes
				// them, but only when gc may remove at least one of them (this
				// project's); otherwise say what gc will not touch.
				if unregisteredHasThisProject(unregistered) {
					_, _ = fmt.Fprintln(platform.Stdout, "run 'soho gc --yes --include-unregistered' to remove the unregistered ones")
				} else {
					_, _ = fmt.Fprintln(platform.Stdout, "the unregistered copies belong to another project or have no origin marker; gc does not remove them (check each one and remove it by hand)")
				}
			}
		}
		if len(orphan) > 0 {
			now := platform.Now()
			_, _ = fmt.Fprintln(platform.Stdout, "orphan processes:")
			for _, row := range orphan {
				_, _ = fmt.Fprintf(platform.Stdout, "%d  %s  %s  %s\n", row.Pid, row.Name, row.Owner, humanAge(procAge(row, now)))
			}
			if len(candidates) == 0 {
				_, _ = fmt.Fprintln(platform.Stdout, "run 'soho gc --yes' to stop them")
			}
		}
		gcPrintOrphanProcs(orphanProcs, threshold, now)
		return 0
	}

	// The lock gates the destructive phase: without it within the timeout
	// the command exits 4 before deleting anything. The removals then run
	// outside the lock; it is taken again around the re-read and the
	// re-write (DropCopiesLines).
	if unlock, lockErr := core.LockCopies(sd); lockErr != nil {
		var locked *core.CopiesLockedError
		if errors.As(lockErr, &locked) {
			core.DieFriction(lockErr.Error(), 4, frictionLogPath, "gc")
		}
		core.DieFriction(fmt.Sprintf("gc: could not take the copy registry lock (%v)", lockErr), 4, frictionLogPath, "gc")
	} else {
		unlock()
	}

	removedLines := map[string]bool{}
	freed := int64(0)
	removedAny := false
	procsHandled := 0
	for _, cand := range candidates {
		if cand.missing {
			// Nothing is left on disk: the line only leaves the registry.
			removedLines[cand.path] = true
			removedAny = true
			_, _ = fmt.Fprintf(platform.Stdout, "removed %s  %s\n", cand.path, humanSize(0))
			continue
		}
		if cand.idle != nil {
			// The check, the removal and the line's drop run in one section
			// under the registry lock, apart from the batch below.
			if gcRemoveIdleOwnerCopy(cand, sd, env, cwd, threshold) {
				removedAny = true
				freed += cand.size
			}
			continue
		}
		resolved, parentInfo, cause := checkedCopy(cand.path, env, cwd)
		if cause != "" {
			_, _ = fmt.Fprintf(platform.Stdout, "kept %s (%s)\n", cand.path, cause)
			continue
		}
		if err := removeCopy(resolved, parentInfo); err != nil {
			keptCopyError(cand.path, err)
			continue
		}
		removedLines[cand.path] = true
		removedAny = true
		freed += cand.size
		_, _ = fmt.Fprintf(platform.Stdout, "removed %s  %s\n", cand.path, humanSize(cand.size))
	}
	if len(removedLines) > 0 {
		if err := core.DropCopiesLines(sd, removedLines); err != nil {
			var locked *core.CopiesLockedError
			if errors.As(err, &locked) {
				core.DieFriction(err.Error(), 4, frictionLogPath, "gc")
			}
			core.DieFriction(fmt.Sprintf("gc: cannot update the copy registry (%v)", err), 4, frictionLogPath, "gc")
		}
	}
	if len(unregistered) > 0 {
		_, _ = fmt.Fprintln(platform.Stdout, gcUnregisteredHeader)
		if includeUnregistered {
			for _, entry := range unregistered {
				// Only this project's unregistered copies are removal
				// candidates; the others stay, with the reason in the line.
				if entry.origin != originThisProject {
					_, _ = fmt.Fprintf(platform.Stdout, "kept %s (%s)\n", entry.path, entry.origin)
					continue
				}
				resolved, parentInfo, cause := checkedCopy(entry.path, env, cwd)
				if cause != "" {
					_, _ = fmt.Fprintf(platform.Stdout, "kept %s (%s)\n", entry.path, cause)
					continue
				}
				if err := removeCopy(resolved, parentInfo); err != nil {
					keptCopyError(entry.path, err)
					continue
				}
				removedAny = true
				freed += entry.size
				_, _ = fmt.Fprintf(platform.Stdout, "removed %s  %s\n", entry.path, humanSize(entry.size))
			}
		} else {
			for _, entry := range unregistered {
				_, _ = fmt.Fprintf(platform.Stdout, "%s  %s  %s  %s\n", entry.path, humanSize(entry.size), humanAge(entry.age), entry.origin)
			}
		}
	}
	// Processes: stop the running orphan lines (no age limit) and drop the
	// gone/reused ones, whatever their owner. The lock gates the phase: a
	// held lock exits 4 before any process is stopped (s92); the stops run
	// outside it, the re-read of the registry under it.
	if len(procRows) > 0 {
		cands := procRows
		if unlock, lockErr := core.LockProcs(sd); lockErr != nil {
			var locked *core.RegistryLockedError
			if errors.As(lockErr, &locked) {
				core.DieFriction(lockErr.Error(), 4, frictionLogPath, "gc")
			}
			core.DieFriction(fmt.Sprintf("gc: could not take the process registry lock (%v)", lockErr), 4, frictionLogPath, "gc")
		} else {
			if fresh, err := core.ReadProcs(sd); err == nil {
				cands = fresh
			}
			unlock()
		}
		for _, row := range cands {
			if gcProcState(row, sd, env) == "owned" {
				continue
			}
			res := core.StopProcRow(sd, row, env)
			if res.Line != "" {
				_, _ = fmt.Fprintln(platform.Stdout, res.Line)
			}
			if res.Err != nil {
				core.Warn(fmt.Sprintf("gc: could not stop process %d (%v); the registry line was kept", row.Pid, res.Err), frictionLogPath, "gc")
				continue
			}
			if res.Dropped {
				procsHandled++
			}
		}
	}
	orphansStopped := 0
	if includeProcs {
		orphansStopped = gcStopOrphanProcs(orphanProcs, threshold, now, env)
	} else {
		gcPrintOrphanProcs(orphanProcs, threshold, now)
	}
	if removedAny {
		_, _ = fmt.Fprintf(platform.Stdout, "freed: %s\n", humanSize(freed))
	}
	if procsHandled > 0 {
		_, _ = fmt.Fprintf(platform.Stdout, "process lines handled: %d\n", procsHandled)
	}
	if !removedAny && procsHandled == 0 && orphansStopped == 0 {
		// --yes with nothing removed or stopped: the dry run's closing line.
		_, _ = fmt.Fprintln(platform.Stdout, "nothing to remove")
	}
	return 0
}

// gcPressure*Key/Default are the s85 limits (internal/core/pressure.go):
// same keys and defaults, read the same way; only the presentation differs
// (stdout, printed on every run, no throttle, no stamp).
const (
	gcPressureDiskKey     = "pressure_disk_free_percent"
	gcPressureSwapKey     = "pressure_swap_percent"
	gcPressureDiskDefault = 15
	gcPressureSwapDefault = 80
)

// gcPressureDecimalRE mirrors the s85 validity check (core's decimalRE): a
// non-negative decimal integer is the only accepted shape.
var gcPressureDecimalRE = regexp.MustCompile(`^[0-9]+$`)

// gcPressureLine renders the gc pressure line from the s85 measurements
// (platform.DiskFree/platform.SwapUsage). A metric contributes the s85
// warning part when its configured limit passed (0 disables it), shows its
// measured value on the "none" line, and shows as "<metric> not measured"
// when it could not be measured.
func gcPressureLine(ctx *core.Config, env platform.Env, stateDir string) string {
	diskLimit := gcPressureThreshold(ctx, gcPressureDiskKey, gcPressureDiskDefault, env)
	swapLimit := gcPressureThreshold(ctx, gcPressureSwapKey, gcPressureSwapDefault, env)

	diskMeasured := false
	diskPct := int64(0)
	diskPart := ""
	if free, total, ok := platform.DiskFree(stateDir); ok && total > 0 {
		diskMeasured = true
		diskPct = free * 100 / total
		if diskLimit > 0 && diskPct < int64(diskLimit) {
			diskPart = fmt.Sprintf("disk %s has %d%% free (%s of %s)", stateDir, diskPct, platform.HumanSize(free), platform.HumanSize(total))
		}
	}
	swapMeasured := false
	swapPct := int64(0)
	swapPart := ""
	if used, total, ok := platform.SwapUsage(env); ok && total > 0 {
		swapMeasured = true
		swapPct = used * 100 / total
		if swapLimit > 0 && swapPct > int64(swapLimit) {
			swapPart = fmt.Sprintf("swap %d%% used (%s of %s)", swapPct, platform.HumanSize(used), platform.HumanSize(total))
		}
	}

	if diskPart != "" || swapPart != "" {
		// The s85 warning line; a metric that could not be measured is
		// named as "<metric> not measured" (a measured metric that is not
		// under pressure, or one whose limit is 0, stays out of the line).
		parts := []string{}
		if diskPart != "" {
			parts = append(parts, diskPart)
		} else if !diskMeasured {
			parts = append(parts, "disk not measured")
		}
		if swapPart != "" {
			parts = append(parts, swapPart)
		} else if !swapMeasured {
			parts = append(parts, "swap not measured")
		}
		return "resource pressure: " + strings.Join(parts, "; ") + "; run 'soho gc' and close idle panes before opening more"
	}
	diskText, swapText := "disk not measured", "swap not measured"
	if diskMeasured {
		diskText = fmt.Sprintf("disk %d%% free", diskPct)
	}
	if swapMeasured {
		swapText = fmt.Sprintf("swap %d%% used", swapPct)
	}
	return fmt.Sprintf("resource pressure: none (%s, %s)", diskText, swapText)
}

// gcPressureThreshold reads the key through the usual layers (env override
// included), exactly like the s85 pressureThreshold: only a non-negative
// decimal integer is valid, anything else falls back to the default with
// the s85 warning line, so only 0 turns a metric off.
func gcPressureThreshold(ctx *core.Config, key string, limit int, env platform.Env) int {
	raw := core.Cfg(ctx, key, strconv.Itoa(limit), env)
	if gcPressureDecimalRE.MatchString(raw) {
		if value, err := strconv.Atoi(raw); err == nil {
			return value
		}
	}
	_, _ = fmt.Fprintf(platform.Stderr, "soho: warning: invalid value '%s' for %s; using the default %d\n", raw, key, limit)
	return limit
}

// gcPrintUnregistered lists unregistered copies in their own block without
// acting on them, each with the class of its origin marker.
func gcPrintUnregistered(entries []gcUnregisteredCopy, _ bool) {
	_, _ = fmt.Fprintln(platform.Stdout, gcUnregisteredHeader)
	for _, entry := range entries {
		_, _ = fmt.Fprintf(platform.Stdout, "%s  %s  %s  %s\n", entry.path, humanSize(entry.size), humanAge(entry.age), entry.origin)
	}
}

// unregisteredHasThisProject reports whether at least one unregistered copy
// may be removed (its marker names the current project).
func unregisteredHasThisProject(entries []gcUnregisteredCopy) bool {
	for _, entry := range entries {
		if entry.origin == originThisProject {
			return true
		}
	}
	return false
}

// The origin classes of an unregistered mutation copy.
const (
	originThisProject = "this project"
	originNoMarker    = "no origin marker"
)

// originMarkerPath is the marker beside a copy: the copy's path with the
// .soho-origin suffix.
func originMarkerPath(copyPath string) string {
	return copyPath + ".soho-origin"
}

// readOriginMarker reads the marker beside a copy. It is valid only when it
// is a regular file (not a symlink) with exactly the three lines
// project=, source= and created= in that order, with non-empty project and
// source values; anything else — missing, unreadable or malformed — reads
// as no origin marker.
func readOriginMarker(markerPath string) (project, source string, ok bool) {
	info, err := os.Lstat(markerPath)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", "", false
	}
	data, err := os.ReadFile(markerPath)
	if err != nil {
		return "", "", false
	}
	// Exactly three lines: only the third line's own terminator is dropped,
	// so a blank fourth line (or any extra line) is malformed.
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != 3 {
		return "", "", false
	}
	project, foundProject := strings.CutPrefix(lines[0], "project=")
	source, foundSource := strings.CutPrefix(lines[1], "source=")
	_, foundCreated := strings.CutPrefix(lines[2], "created=")
	if !foundProject || !foundSource || !foundCreated || project == "" || source == "" {
		return "", "", false
	}
	return project, source, true
}

// copyOrigin classifies the marker beside an unregistered copy against the
// current project's identity (the gc's cwd's git common dir, "" when the
// cwd is not in a git repository): this project when the marker is valid
// and its project= matches (case-insensitively on Windows), another
// project: <source> when the marker names a different project, and no
// origin marker otherwise. Outside a git repository no copy is this
// project: a valid marker still classifies the copy as another project
// (it names one), and the marker-less reads as no origin marker.
func copyOrigin(copyPath, current string) string {
	project, source, ok := readOriginMarker(originMarkerPath(copyPath))
	if !ok {
		return originNoMarker
	}
	if current != "" && sameOriginProject(project, current) {
		return originThisProject
	}
	return "another project: " + source
}

func sameOriginProject(marker, current string) bool {
	if platform.Current() == "win32" {
		return strings.EqualFold(marker, current)
	}
	return marker == current
}

// gcUnregistered lists the soho-mutation-* directories directly below
// TMPDIR (or os.TempDir() without TMPDIR) that are not in the registry and
// whose mtime is older than the threshold, sorted by path. Each entry
// carries the class of the marker beside it, computed against the git
// common dir of cwd (the current project's identity; "" when cwd is not in
// a git repository).
func gcUnregistered(env platform.Env, rows []core.CopyRow, now time.Time, threshold time.Duration, cwd string) []gcUnregisteredCopy {
	tmpDir := env.Get("TMPDIR")
	if tmpDir == "" {
		tmpDir = os.TempDir()
	}
	registered := map[string]bool{}
	for _, row := range rows {
		registered[row.Path] = true
		if resolved, err := filepath.EvalSymlinks(row.Path); err == nil {
			registered[resolved] = true
		}
	}
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		return nil
	}
	current := ""
	if project, err := gitCommonDirResolved(cwd, env); err == nil {
		current = project
	}
	out := []gcUnregisteredCopy{}
	for _, entry := range entries {
		path := filepath.Join(tmpDir, entry.Name())
		if !strings.HasPrefix(entry.Name(), "soho-mutation-") {
			continue
		}
		info, statErr := os.Stat(path)
		if statErr != nil || !info.IsDir() {
			continue
		}
		resolved, resolveErr := filepath.EvalSymlinks(path)
		if resolveErr != nil || registered[resolved] {
			continue
		}
		age := now.Sub(info.ModTime())
		if age <= threshold {
			continue
		}
		out = append(out, gcUnregisteredCopy{path: path, size: copyDirSize(path), age: age, origin: copyOrigin(path, current)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out
}

func gcUsageError() int {
	_, _ = fmt.Fprintf(platform.Stderr, "soho: %s\n", gcUsage)
	return 2
}

// keptCopyError prints the kept line for a failed removal: the
// "changed after the check" cause when the removal proved the directory
// moved since the check, the error otherwise.
func keptCopyError(path string, err error) {
	if errors.Is(err, errCopyChanged) {
		_, _ = fmt.Fprintf(platform.Stdout, "kept %s (changed after the check)\n", path)
		return
	}
	_, _ = fmt.Fprintf(platform.Stdout, "kept %s (%v)\n", path, err)
}

func parseGcHours(value string) (int64, error) {
	if !cleanDaysPattern.MatchString(value) {
		return 0, fmt.Errorf("invalid hour count")
	}
	var n int64
	for _, r := range value {
		n = n*10 + int64(r-'0')
		if n > 1<<60 {
			return 0, fmt.Errorf("hour count too large")
		}
	}
	return n, nil
}

// humanAge renders an age for the copies and gc output: 0m, 3h, 2d.
func humanAge(d time.Duration) string {
	if d < time.Minute {
		return "0m"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int64(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int64(d.Hours()))
	}
	return fmt.Sprintf("%dd", int64(d.Hours())/24)
}

// humanSize renders a byte count in base-1024 human units (1.2 GB).
// humanSize is core.HumanSize: the shared byte formatting of the copy and
// gc reports (moved to core so the footprint line of explain and the
// doctor warning format the sizes in one place).
func humanSize(n int64) string { return core.HumanSize(n) }

// copyDirSize sums the sizes of the regular files under root without
// following symlinks: the shared walk moved to core (DirSize) so the
// footprint measure and the copy measure cannot drift apart.
func copyDirSize(root string) int64 { return core.DirSize(root) }
