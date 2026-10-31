package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/backend"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

const procsUsage = "usage: soho procs | procs add <pid> [--agent <name>] | procs stop <pid>… [--agent <name>]\nprocs stop exits: 0 stopped/already gone; 2 usage/refusal; 4 stop failed (worst code wins)"

// cmdProcs lists, registers, or stops registered worker processes. The
// command line of a process is never read or stored: it may carry
// credentials.
func cmdProcs(argv []string, ctx *core.Config, env platform.Env, cwd string) int {
	if len(argv) == 0 {
		return procsList(ctx, env, cwd)
	}
	switch argv[0] {
	case "add":
		return procsAdd(argv[1:], ctx, env, cwd)
	case "stop":
		return procsStop(argv[1:], ctx, env, cwd)
	default:
		return procsUsageError()
	}
}

// procsStop never discovers processes: only the caller's registered rows
// can reach the existing identity-checked stop operation.
func procsStop(argv []string, ctx *core.Config, env platform.Env, cwd string) int {
	var pids []string
	var agent string
	agentSeen := false
	for i := 0; i < len(argv); i++ {
		if argv[i] == "--agent" {
			if agentSeen || i+1 >= len(argv) || argv[i+1] == "" {
				return procsUsageError()
			}
			agent, agentSeen = argv[i+1], true
			i++
		} else if strings.HasPrefix(argv[i], "--") {
			return procsUsageError()
		} else {
			pids = append(pids, argv[i])
		}
	}
	if len(pids) == 0 {
		return procsUsageError()
	}
	sd := registryDestination(env)
	if sd == "" {
		sd = core.StateDir(ctx, env, cwd)
	}
	incorporatePendingProcs("procs stop", sd, env)
	paneAgent := rosterAgentForPane(sd, backend.New(env).Runtime.Caller().PaneID)
	owner := paneAgent
	if agentSeen {
		if core.RosterLine(sd, agent) == "" || (paneAgent != "" && paneAgent != agent) {
			for _, arg := range pids {
				_, _ = fmt.Fprintf(platform.Stderr, "procs stop: refusing %s: --agent %s does not match the calling pane's agent %s\n", arg, agent, paneAgent)
			}
			return 2
		}
		owner = agent
	}
	rows, err := core.ReadProcs(sd)
	if err != nil {
		_, _ = fmt.Fprintf(platform.Stderr, "procs stop: cannot read the process registry (%v)\n", err)
		return 4
	}
	code := 0
	for _, arg := range pids {
		refuse := func(cause string) {
			_, _ = fmt.Fprintf(platform.Stderr, "procs stop: refusing %s: %s\n", arg, cause)
			if code < 2 {
				code = 2
			}
		}
		decimal := arg != ""
		for _, c := range arg {
			if c < '0' || c > '9' {
				decimal = false
			}
		}
		pid, err := strconv.Atoi(arg)
		if !decimal || err != nil || pid <= 0 {
			refuse("not a positive decimal integer pid")
			continue
		}
		if pid == 1 || pid == os.Getpid() || pid == platform.ParentPID() {
			refuse("pid is system init, this soho process, or its parent")
			continue
		}
		if owner == "" {
			pane := backend.New(env).Runtime.Caller().PaneID
			if pane == "" {
				pane = "unset"
			}
			refuse(fmt.Sprintf("no calling agent in the roster (pane %s, state dir %s)", pane, sd))
			continue
		}
		matched, owned := false, false
		for _, row := range rows {
			if row.Pid != pid {
				continue
			}
			matched = true
			if row.Owner != owner {
				continue
			}
			owned = true
			result := core.StopProcRow(sd, row, env)
			if result.Line != "" {
				_, _ = fmt.Fprintln(platform.Stdout, result.Line)
			}
			if result.Err != nil {
				_, _ = fmt.Fprintf(platform.Stderr, "procs stop: %v\n", result.Err)
				code = 4
			}
		}
		if !matched {
			refuse("pid is not registered")
		} else if !owned {
			refuse("registered process belongs to another agent")
		}
	}
	return code
}

// procsList is read-only: <pid>  <name>  <owner>  <age>  <state>, the
// state re-checked against the system at read time (running = the pid
// exists with the same start time; gone = the pid is not there; reused =
// the pid exists with another start time).
func procsList(ctx *core.Config, env platform.Env, cwd string) int {
	sd := core.StateDir(ctx, env, cwd)
	incorporatePendingProcs("procs", sd, env)
	rows, err := core.ReadProcs(sd)
	if err != nil {
		core.DieFriction(fmt.Sprintf("procs: cannot read the process registry (%v)", err), 4, frictionLogPath, "procs")
	}
	now := platform.Now()
	if len(rows) == 0 {
		// An empty registry says so, apart from a silent failure (G3).
		_, _ = fmt.Fprintln(platform.Stdout, "no registered processes")
	}
	for _, row := range rows {
		_, _ = fmt.Fprintf(platform.Stdout, "%d  %s  %s  %s  %s\n", row.Pid, row.Name, row.Owner, humanAge(procAge(row, now)), core.ProcRowState(row, env))
	}
	orphans, err := projectOrphans(sd, rows, env, cwd)
	switch {
	case errors.Is(err, platform.ErrOrphansNotDetected):
		_, _ = fmt.Fprintln(platform.Stdout, orphanWindowsLine)
	case err != nil:
		_, _ = fmt.Fprintf(platform.Stderr, "procs: could not list the unregistered processes (%v)\n", err)
	case len(orphans) > 0:
		_, _ = fmt.Fprintln(platform.Stdout, orphanHeader)
		for _, orphan := range orphans {
			_, _ = fmt.Fprintln(platform.Stdout, orphan.line(now))
		}
	}
	return 0
}

// procsAdd registers one pid. The owner is the roster agent of
// HERDR_PANE_ID (or "-"); --agent takes the name, and a name outside the
// roster exits 3. Refusals exit 2 and write nothing: a pid that is not a
// positive integer, pid 1, this process, the invoking process, or a
// proven-gone pid (the absence is kernel-confirmed). A pid whose identity
// the single snapshot read could not resolve (the ps query failed, or its
// line did not parse, and the kernel did not report the absence) is not
// refused as a dead process: it exits 4 with a clear diagnostic and is
// not registered — a failed read is not an absence, and no second read
// races the first to name its failure.
func procsAdd(argv []string, ctx *core.Config, env platform.Env, cwd string) int {
	var pidArg, agent string
	agentSeen := false
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if a == "--agent" {
			if agentSeen || i+1 >= len(argv) || argv[i+1] == "" {
				return procsUsageError()
			}
			agent = argv[i+1]
			agentSeen = true
			i++
			continue
		}
		if strings.HasPrefix(a, "--") {
			return procsUsageError()
		}
		if pidArg == "" {
			pidArg = a
			continue
		}
		return procsUsageError()
	}
	if pidArg == "" {
		return procsUsageError()
	}
	refuse := func(cause string) int {
		_, _ = fmt.Fprintf(platform.Stderr, "procs add: refusing %s: %s\n", pidArg, cause)
		return 2
	}
	pid, err := strconv.Atoi(pidArg)
	if err != nil || pid <= 0 {
		return refuse("not a positive integer pid")
	}
	if pid == 1 {
		return refuse("pid 1 is the system init process")
	}
	if pid == os.Getpid() {
		return refuse("that is this soho process")
	}
	if parent := platform.ParentPID(); parent > 0 && pid == parent {
		return refuse("that is the process that invoked soho")
	}
	sd := registryDestination(env)
	if sd == "" {
		sd = core.StateDir(ctx, env, cwd)
	}
	incorporatePendingProcs("procs add", sd, env)
	if agentSeen && core.RosterLine(sd, agent) == "" {
		core.DieFriction(fmt.Sprintf("procs add: agent '%s' is not in the roster (state dir: %s)", agent, sd), 3, frictionLogPath, "procs")
	}
	started, name, live, cause := readProcFull(pid, env)
	switch live {
	case platform.ProcGone:
		return refuse("no process with that pid is running")
	case platform.ProcUnknown:
		// The one snapshot did not resolve the pid: the ps query failed
		// (or its line did not parse) and the kernel did not report the
		// absence. It is a friction, not the dead-process refusal. A
		// permission denial appends the probable cause and the ways out;
		// the exit, the friction and the refusal to register stay the
		// same, and any other failure keeps today's text.
		message := "procs add: the identity of pid %s is unreadable (the system state read failed and the kernel did not report it gone); not registered"
		if errors.Is(cause, platform.ErrProcReadPermissionDenied) {
			agentHint := "--agent <name>"
			if agentSeen {
				agentHint = "--agent " + agent
			}
			message += ": the process table is not readable from this shell (a sandbox may deny it); run soho procs add outside the sandbox (an approved command), or ask the orchestrator to register the pid with " + agentHint
		}
		core.DieFriction(fmt.Sprintf(message, pidArg), 4, frictionLogPath, "procs")
	}
	pane := backend.New(env).Runtime.Caller().PaneID
	owner := "-"
	if agentSeen {
		owner = agent
		if fields := strings.Split(core.RosterLine(sd, agent), "\t"); len(fields) > 1 && fields[1] != "" {
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
	row := core.ProcRow{Pid: pid, Started: started, Name: name, Owner: owner, Pane: pane, Created: core.FrictionISO(platform.Now())}
	if err := upsertProc(sd, row); err != nil {
		if errors.Is(err, fs.ErrPermission) {
			queue, queueErr := core.QueueProc(env, filepath.Base(sd), sd, row)
			if queueErr == nil {
				pendingQueuedLine("procs add", sd, queue)
				return 0
			}
			err = fmt.Errorf("%v; the queue failed too: %v", err, queueErr)
		}
		var locked *core.RegistryLockedError
		if errors.As(err, &locked) {
			core.DieFriction(err.Error(), 4, frictionLogPath, "procs")
		}
		core.DieFriction(fmt.Sprintf("procs add: could not register pid %s (%v)", pidArg, err), 4, frictionLogPath, "procs")
	}
	return 0
}

// upsertProc is the registry write of procs add; tests swap it to inject
// the permission error of a sandbox that cannot write the state dir.
var upsertProc = core.UpsertProc

// readProcFull is the by-pid identity read of procs add; tests swap it
// to inject an unreadable read and its cause.
var readProcFull = platform.ReadProcFullCause

func procsUsageError() int {
	_, _ = fmt.Fprintf(platform.Stderr, "soho: %s\n", procsUsage)
	return 2
}

// procAge is how long the line has been registered; an unparseable or
// future created reads as 0 (same rule as the copies age).
func procAge(row core.ProcRow, now time.Time) time.Duration {
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

const (
	// orphanHeader opens the block of unregistered orphan processes whose
	// cwd is in this project, in procs and in gc.
	orphanHeader = "unregistered orphan processes in this project:"
	// orphanWindowsLine replaces the block where the cwd of another
	// process cannot be read without its PEB (decision 2).
	orphanWindowsLine = "unregistered processes: not detected on Windows (only registered processes are listed)"
)

// The orphan scope kinds. A cwd in a registered copy (one that passes the
// copy removal check) or the state dir may be stopped by gc; one in the
// soho scratch is only listed (the scratch depends only on the workspace
// and can be another project's), and so is one in the repository root or
// one of its worktrees (it can be the user's editor or terminal).
const (
	orphanKindCopy     = "copy"
	orphanKindState    = "state"
	orphanKindScratch  = "scratch"
	orphanKindWorktree = "worktree"
	orphanKindRoot     = "root"
)

// The orphan seams: the platform reads behind the block and gc's re-check
// before each signal, and the worktree list; tests replace them.
var (
	orphanProcesses    = platform.UserOrphanProcesses
	orphanProcCwd      = platform.ProcCwd
	orphanProcIdentity = platform.ProcStartIdentity
	orphanWorktrees    = gitWorktreePaths
)

// projectOrphan is one unregistered orphan process whose cwd is in this
// project, with the scope kind its cwd falls in.
type projectOrphan struct {
	platform.OrphanProc
	kind string
}

// stoppable is the scope rule: a copy or the state dir; never the
// scratch, the root or a worktree.
func (o projectOrphan) stoppable() bool { return orphanKindStoppable(o.kind) }

func orphanKindStoppable(kind string) bool {
	switch kind {
	case orphanKindCopy, orphanKindState:
		return true
	}
	return false
}

func (o projectOrphan) age(now time.Time) time.Duration {
	if o.Started.IsZero() {
		return 0
	}
	if age := now.Sub(o.Started); age > 0 {
		return age
	}
	return 0
}

// line is <pid>  <name>  <age>  <cwd>  (<kind>).
func (o projectOrphan) line(now time.Time) string {
	return fmt.Sprintf("%d  %s  %s  %s  (%s)", o.Pid, o.Name, humanAge(o.age(now)), o.Cwd, o.kind)
}

// orphanPrefix is one directory of the project scope, symlinks resolved.
type orphanPrefix struct {
	kind string
	path string
}

// projectOrphans lists the user's orphan processes (platform) whose cwd
// falls in this project's scope, leaving out the pids procs.tsv holds. A
// cwd outside every prefix is left out. The platform error (Windows:
// platform.ErrOrphansNotDetected) is returned as is. The scope is built
// only when the platform listed at least one orphan.
func projectOrphans(sd string, registered []core.ProcRow, env platform.Env, cwd string) ([]projectOrphan, error) {
	list, err := orphanProcesses()
	if err != nil || len(list) == 0 {
		return nil, err
	}
	scope := orphanScope(sd, env, cwd)
	known := map[int]bool{}
	for _, row := range registered {
		known[row.Pid] = true
	}
	out := []projectOrphan{}
	for _, proc := range list {
		if known[proc.Pid] {
			continue
		}
		if kind := orphanKind(proc.Cwd, scope); kind != "" {
			out = append(out, projectOrphan{OrphanProc: proc, kind: kind})
		}
	}
	return out, nil
}

// orphanScope is the project's prefixes: the registered copies that pass
// orphanCopyPrefix, the state dir, the soho scratch (<TMPDIR>/soho/
// <workspace>, the explain footprint's rule), the worktrees of `git
// worktree list --porcelain` (the root's own entry aside) and the
// repository root. A prefix that does not resolve is left out.
func orphanScope(sd string, env platform.Env, cwd string) []orphanPrefix {
	out := []orphanPrefix{}
	resolve := func(path string) string {
		if path == "" {
			return ""
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return ""
		}
		return filepath.Clean(resolved)
	}
	add := func(kind, path string) {
		if resolved := resolve(path); resolved != "" {
			out = append(out, orphanPrefix{kind: kind, path: resolved})
		}
	}
	root := platform.StateProjectRoot(env, cwd)
	resolvedRoot := resolve(root)
	trees := []string{}
	for _, tree := range orphanWorktrees(root, env) {
		// The list's first entry is the main worktree, the root itself.
		if resolved := resolve(tree); resolved != "" && resolved != resolvedRoot {
			trees = append(trees, resolved)
		}
	}
	protected := append([]string{resolvedRoot, resolve(sd)}, trees...)
	if rows, err := core.ReadCopies(sd); err == nil {
		for _, row := range rows {
			if resolved, ok := orphanCopyPrefix(row.Path, protected, env, cwd); ok {
				out = append(out, orphanPrefix{kind: orphanKindCopy, path: resolved})
			}
		}
	}
	add(orphanKindState, sd)
	tmp := env.Get("TMPDIR")
	if tmp == "" {
		tmp = os.TempDir()
	}
	add(orphanKindScratch, filepath.Join(tmp, "soho", filepath.Base(sd)))
	for _, tree := range trees {
		out = append(out, orphanPrefix{kind: orphanKindWorktree, path: tree})
	}
	add(orphanKindRoot, root)
	return out
}

// orphanCopyPrefix admits a copies.tsv line as a copy prefix only when its
// path passes the check gc makes before removing a copy (checkedCopy: an
// absolute, existing, non-symlink path that is not the filesystem root,
// HOME or above it, the project root, inside it or above it, nor holds a
// .git), and is none of the protected paths (the root, the state dir, a
// worktree) nor above one of them. A refused line is no prefix at all.
func orphanCopyPrefix(path string, protected []string, env platform.Env, cwd string) (string, bool) {
	resolved, _, cause := checkedCopy(path, env, cwd)
	if cause != "" {
		return "", false
	}
	resolved = filepath.Clean(resolved)
	for _, dir := range protected {
		if dir != "" && inside(resolved, dir) {
			return "", false
		}
	}
	return resolved, true
}

// orphanKind is the kind of the deepest prefix that holds cwd, compared
// by path component; on the same depth a listed-only kind (scratch,
// root, worktree) wins, so a prefix shared with one of them never makes
// it stoppable. "" is outside the project.
func orphanKind(cwd string, scope []orphanPrefix) string {
	best, bestLen := "", -1
	for _, prefix := range scope {
		if !orphanWithin(cwd, prefix.path) {
			continue
		}
		listed := !orphanKindStoppable(prefix.kind)
		if len(prefix.path) > bestLen || (len(prefix.path) == bestLen && listed) {
			best, bestLen = prefix.kind, len(prefix.path)
		}
	}
	return best
}

// orphanWithin reports whether path is dir or under it, by path
// component: /a/copy-2 is not within /a/copy.
func orphanWithin(path, dir string) bool {
	rel, err := filepath.Rel(dir, filepath.Clean(path))
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// gitWorktreePaths lists the worktree paths of `git worktree list
// --porcelain` run in root, or none when git fails.
func gitWorktreePaths(root string, env platform.Env) []string {
	r := platform.RunCli("git", []string{"-C", root, "worktree", "list", "--porcelain"}, platform.RunOptions{Env: env, TimeoutMs: 10_000})
	if r.NotFound || r.TimedOut || r.Status == nil || *r.Status != 0 {
		return nil
	}
	out := []string{}
	for _, line := range strings.Split(strings.ReplaceAll(r.Stdout, "\r\n", "\n"), "\n") {
		if path, ok := strings.CutPrefix(line, "worktree "); ok && path != "" {
			out = append(out, path)
		}
	}
	return out
}

// orphanStill is gc's re-check before a signal: the pid still reads the
// start identity and the cwd of the listing.
func orphanStill(orphan projectOrphan, env platform.Env) func() bool {
	return func() bool {
		identity, _, live := orphanProcIdentity(orphan.Pid, env)
		if live != platform.ProcRunning || identity != orphan.Identity {
			return false
		}
		now, err := orphanProcCwd(orphan.Pid)
		return err == nil && now == orphan.Cwd
	}
}
