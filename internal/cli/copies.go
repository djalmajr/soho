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
)

const (
	copiesUsage = "usage: soho copies [add <path> [--agent <name>]]"
	gcUsage     = "usage: soho gc [--yes] [--older-than HOURS] [--include-unregistered]"

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
// "changed after the check").
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
	return root.RemoveAll(base)
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
	row := core.CopyRow{Path: resolved, Owner: owner, Pane: pane, Created: core.FrictionISO(platform.Now()), Source: "-", Origin: "add"}
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

// gcCandidate is one registry line gc may act on: an orphan old enough, or a
// missing line that only leaves the registry.
type gcCandidate struct {
	path    string
	owner   string
	age     time.Duration
	size    int64
	missing bool
}

// gcUnregisteredCopy is a soho-mutation-* directory below TMPDIR that
// the registry does not know.
type gcUnregisteredCopy struct {
	path string
	size int64
	age  time.Duration
}

// cmdGc lists the orphan copies the registry holds (dry run), or removes the
// ones at least --older-than hours old. Unregistered mutation copies are
// always listed in their own block and are only removed with
// --include-unregistered --yes.
func cmdGc(argv []string, ctx *core.Config, env platform.Env, cwd string) int {
	yes, includeUnregistered := false, false
	hours := "2"
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		switch arg {
		case "--yes":
			yes = true
		case "--include-unregistered":
			includeUnregistered = true
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
		}
		// owned lines are never touched
	}
	unregistered := gcUnregistered(env, rows, now, threshold)

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
		if len(candidates) == 0 && len(unregistered) == 0 && len(orphan) == 0 {
			_, _ = fmt.Fprintln(platform.Stdout, "nothing to remove")
			return 0
		}
		if len(candidates) > 0 {
			for _, cand := range candidates {
				_, _ = fmt.Fprintf(platform.Stdout, "would remove %s  %s  %s  %s\n", cand.path, humanSize(cand.size), cand.owner, humanAge(cand.age))
			}
			_, _ = fmt.Fprintf(platform.Stdout, "total: %s\n", humanSize(total))
			_, _ = fmt.Fprintln(platform.Stdout, "run 'soho gc --yes' to remove them")
		}
		if len(unregistered) > 0 {
			gcPrintUnregistered(unregistered, false)
			if len(candidates) == 0 {
				// Only unregistered copies: name the flag that actually removes them.
				_, _ = fmt.Fprintln(platform.Stdout, "run 'soho gc --yes --include-unregistered' to remove the unregistered ones")
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
				_, _ = fmt.Fprintf(platform.Stdout, "%s  %s  %s\n", entry.path, humanSize(entry.size), humanAge(entry.age))
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
	if removedAny {
		_, _ = fmt.Fprintf(platform.Stdout, "freed: %s\n", humanSize(freed))
	}
	if procsHandled > 0 {
		_, _ = fmt.Fprintf(platform.Stdout, "process lines handled: %d\n", procsHandled)
	}
	if !removedAny && procsHandled == 0 {
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
// acting on them.
func gcPrintUnregistered(entries []gcUnregisteredCopy, _ bool) {
	_, _ = fmt.Fprintln(platform.Stdout, gcUnregisteredHeader)
	for _, entry := range entries {
		_, _ = fmt.Fprintf(platform.Stdout, "%s  %s  %s\n", entry.path, humanSize(entry.size), humanAge(entry.age))
	}
}

// gcUnregistered lists the soho-mutation-* directories directly below
// TMPDIR (or os.TempDir() without TMPDIR) that are not in the registry and
// whose mtime is older than the threshold, sorted by path.
func gcUnregistered(env platform.Env, rows []core.CopyRow, now time.Time, threshold time.Duration) []gcUnregisteredCopy {
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
		out = append(out, gcUnregisteredCopy{path: path, size: copyDirSize(path), age: age})
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
