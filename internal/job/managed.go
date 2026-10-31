package job

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/djalmajr/soho/internal/backend"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

// managed.go binds the private launch context (locator.go) to the current
// launch. A job supervisor started with that context runs only when it
// holds the launch's binding (BindLaunch): it confirmed the pending record
// itself, or it took over a confirmed record of the job's current
// workspace and root pane whose supervisor is proven gone. The team
// commands it runs as subprocesses (selfCLI) receive the selection
// explicitly, and each one uses it only when it proves, first from the
// live process table alone and then from the private record, that its
// direct parent is that supervisor (ManagedSelection). Every other command
// ignores the private names.
//
// The binding fences cooperating soho processes; like the launch
// record itself it is not an authentication against another process of
// the same user, which can read or write the private files and the
// environment. A process start identity has the platform's precision
// (platform.ProcStartIdentity); the executable and arguments are read for
// one pid only (platform.ProcExec), and both reads are bracketed by a
// second identity read, so a pid that exits or is reused between them is
// never taken for the supervisor. Where either read is not available, or
// answers anything unexpected, nothing is bound and no command activates.

// maxManagedStateBytes caps the state.json a managed command or a rebind
// reads.
const maxManagedStateBytes = 1 << 20

// managedCommand reports whether args is one of the team commands the job
// supervisor runs through selfCLI (team_ops.go): session set, the bare
// config, spawn, dispatch, send, status, release, roster and gc.
func managedCommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "session":
		return len(args) > 1 && args[1] == "set"
	case "config":
		return len(args) == 1
	case "spawn", "dispatch", "send", "status", "release", "roster", "gc":
		return true
	}
	return false
}

// selfIdentity reads this process's start identity and kernel executable
// path; ok is false when either cannot be read.
func selfIdentity(env platform.Env) (started, exe string, ok bool) {
	started, _, live := platform.ProcStartIdentity(os.Getpid(), env)
	if live != platform.ProcRunning || started == "" {
		return "", "", false
	}
	exe, _, err := platform.ProcExec(os.Getpid())
	if err != nil || exe == "" {
		return "", "", false
	}
	return started, exe, true
}

// BindLaunch is the job supervisor's binding to its launch when it was
// started with the private launch context (sel is that context's
// selection). bound is true when this process confirmed the pending
// record (recording its PID, process start identity, kernel executable
// path and sel), or when it took over the confirmed record of this job's
// current launch from a supervisor proven gone (rebindLaunch). In every
// other case nothing is bound and reason says why: the scoped supervisor
// then does not run.
func BindLaunch(store *Store, id string, env platform.Env, sel core.UserSelection) (bound bool, reason string) {
	started, exe, ok := selfIdentity(env)
	if !ok {
		return false, "this supervisor's process identity is unreadable"
	}
	bind := launchBinding{Started: started, Exe: exe, Sel: sel}
	confirmed, reason := confirmLaunch(store, id, env, bind)
	if confirmed {
		return true, ""
	}
	if reason != "" {
		return false, reason
	}
	return rebindLaunch(store, id, env, bind)
}

// rebindLaunch takes over a confirmed record whose supervisor is proven
// gone, as a restarted `job supervise` in the job's root pane. All of
// these must hold before anything is written:
//
//   - the record is this version, confirmed, with a well-formed nonce, and
//     names this job, store, workspace and root pane;
//   - it was confirmed with the private launch context, with this
//     selection, by a supervisor whose PID, start identity and executable
//     it recorded (a record confirmed without the context, or incomplete,
//     is never adopted);
//   - the job's state.json (read like the record) is the record's epoch:
//     the same workspace and root pane, and a status after the start
//     published running and before the job settled (running, blocked or
//     finishing), so a failed or still-starting launch is never adopted;
//   - the recorded supervisor is proven inert: the process table shows
//     the pid absent or running with another start identity. A pid still
//     running with the recorded identity, or one whose liveness cannot be
//     read, keeps the record.
//
// Under launch.lock the record must still be exactly the one checked
// (another restarted supervisor that won the lock changed it); only the
// PID, start identity and executable then change.
func rebindLaunch(store *Store, id string, env platform.Env, bind launchBinding) (bool, string) {
	dir, err := Dir(store.Root, id)
	if err != nil {
		return false, "the launch record is unreadable"
	}
	prior, ok, err := readLaunch(dir)
	switch {
	case err != nil:
		return false, "the launch record is unreadable"
	case !ok:
		return false, "the job has no launch record"
	case prior.State != launchConfirmed || !validLaunchNonce(prior.Nonce):
		return false, "the launch is not confirmed"
	}
	if why := launchMismatch(prior, id, store, env, bind.Sel); why != "" {
		return false, why
	}
	if prior.ConfigBase == "" || prior.PID <= 0 || prior.Started == "" || prior.Exe == "" {
		return false, "the launch was not bound with the private launch context"
	}
	if prior.PID == os.Getpid() && prior.Started == bind.Started {
		return true, ""
	}
	st, ok := readManagedState(dir)
	if !ok || st.WorkspaceID != prior.Workspace || st.RootPane != prior.Pane {
		return false, "the launch is not the job's current launch"
	}
	if st.Status != StatusRunning && st.Status != StatusBlocked && st.Status != StatusFinishing {
		return false, "the job is not running"
	}
	priorID, _, priorLive := platform.ProcStartIdentity(prior.PID, env)
	gone := priorLive == platform.ProcGone || (priorLive == platform.ProcRunning && priorID != "" && priorID != prior.Started)
	if !gone {
		if priorLive == platform.ProcRunning {
			return false, "the supervisor that confirmed the launch may still be running"
		}
		return false, "the liveness of the supervisor that confirmed the launch is unknown"
	}
	clock := store.clock()
	until := clock.Monotonic() + int64(launchRevokeWait)
	bound, reason := false, ""
	err = withLaunchLock(dir, clock, until, func() error {
		rec, ok, readErr := readLaunch(dir)
		if readErr != nil || !ok || rec != prior {
			reason = "the launch record changed"
			return nil
		}
		rec.PID = os.Getpid()
		rec.Started, rec.Exe = bind.Started, bind.Exe
		if writeErr := writeLaunch(dir, rec); writeErr != nil {
			reason = "the binding could not be written"
			return nil
		}
		bound = true
		return nil
	})
	if err != nil {
		return false, "the launch lock could not be taken"
	}
	return bound, reason
}

// validLaunchNonce reports whether nonce has newLaunchNonce's shape: 32
// lowercase hex digits.
func validLaunchNonce(nonce string) bool {
	if len(nonce) != 32 {
		return false
	}
	for i := 0; i < len(nonce); i++ {
		c := nonce[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// launchMismatch names why rec is not this supervisor's launch, or "".
func launchMismatch(rec launchRecord, id string, store *Store, env platform.Env, sel core.UserSelection) string {
	// The caller identity is the supervisor's inherited environment claim,
	// read through the transport the same way the old direct env.Get did.
	caller := backend.New(env).Runtime.Caller()
	switch {
	case rec.ID != id || rec.Store != filepath.Clean(store.Root):
		return "the launch belongs to another job or store"
	case rec.Workspace == "" || rec.Workspace != caller.WorkspaceID || rec.Pane == "" || rec.Pane != caller.PaneID:
		return "the launch belongs to another workspace or pane"
	case rec.ConfigBase != sel.Base || rec.Home != sel.Home:
		return "the launch carries another configuration selection"
	}
	return ""
}

// UseLaunchSelection marks a supervisor bound to its private launch
// (BindLaunch): waitRunning applies the private start guard and the team
// commands receive sel explicitly under the private names; every other
// process the supervisor starts keeps its environment without them.
func (s *Supervisor) UseLaunchSelection(sel core.UserSelection) {
	s.PrivateLaunch = true
	if ops, ok := s.Ops.(selfCLI); ok {
		s.actorEnv = ops.Env.Clone()
		s.launchSel = sel
		ops.Env = withLaunchLocators(ops.Env, sel)
		s.Ops = ops
	}
}

// ManagedOutcome is what a command that started with the private launch
// names makes of them (ManagedSelection).
type ManagedOutcome int

const (
	// ManagedIgnored: the command is proven not to be a bound team
	// command — not a managed command, or its direct parent was read and
	// is not a `job supervise` process. The private values are ignored and
	// the command selects from its environment exactly as without them.
	ManagedIgnored ManagedOutcome = iota
	// ManagedBound: the binding is proven; the command reads its user
	// configuration from the returned selection.
	ManagedBound
	// ManagedRefused: a managed command whose direct parent was identified
	// as a `job supervise` process of this executable but whose binding
	// does not hold (unusable values, another or no store, a missing,
	// revoked, stale, malformed or mismatched record, another epoch or
	// directory), or whose parent could not be identified either way (no
	// parent, an exited or reused pid, an unreadable process table). The
	// command must stop before any side effect: running it with the
	// environment's configuration could act on the wrong team.
	ManagedRefused
)

// ManagedSelection classifies a command that started with the private
// launch names (launch.Present) and returns the job starter's user
// configuration selection when the outcome is ManagedBound (args is the
// command line after the executable; env the environment without the
// private names). The checks run in this order, so inherited names never
// lead to a read of a candidate configuration, store or record unless the
// direct parent was first identified as a job supervisor of this
// executable:
//
//  1. args is a managed command, else ManagedIgnored;
//  2. the direct parent (managedParent), from the process table alone:
//     arguments read and not `job supervise --id ...`, ManagedIgnored;
//     unreadable, ambiguous or not this executable, ManagedRefused;
//     otherwise the parent runs the file this process runs as
//     `job supervise --id <ID>` or `job supervise --id <ID> --repo
//     <ORG/REPO>` (in that order, the repo valid by the same pattern) for
//     a valid job id, with one start identity before and after the read;
//  3. launch carries a usable selection and HERDR_ENV is 1 with the
//     workspace and pane ids set;
//  4. the selection's configuration resolves, for cwd, the store whose
//     jobs/<ID> is a real directory (no symlink at jobs or at <ID>, no
//     listing of other jobs);
//  5. jobs/<ID>/launch.json (read no-follow and bounded) is confirmed,
//     well formed, and names that job, that store, this workspace and pane,
//     this selection, and the parent's pid, start identity and executable;
//  6. jobs/<ID>/state.json (read the same way) is that launch's epoch (its
//     workspace and root pane) and records cwd as the job directory.
//
// A failure from step 3 on is ManagedRefused.
func ManagedSelection(args []string, launch LaunchLocators, env platform.Env, cwd string) (core.UserSelection, ManagedOutcome) {
	if !managedCommand(args) {
		return core.UserSelection{}, ManagedIgnored
	}
	parent, kind := managedParent(env)
	switch kind {
	case parentNotSupervisor:
		return core.UserSelection{}, ManagedIgnored
	case parentUnknown:
		return core.UserSelection{}, ManagedRefused
	}
	sel := launch.Selection
	if sel.Base == "" || !bindingHolds(parent, sel, env, cwd) {
		return core.UserSelection{}, ManagedRefused
	}
	return sel, ManagedBound
}

// bindingHolds runs steps 3 to 6 of ManagedSelection for an identified
// supervisor parent.
func bindingHolds(parent managedParentInfo, sel core.UserSelection, env platform.Env, cwd string) bool {
	// The caller identity is the supervisor's inherited environment claim,
	// read through the transport the same way the old direct env.Get did.
	caller := backend.New(env).Runtime.Caller()
	workspace, pane := caller.WorkspaceID, caller.PaneID
	if !caller.Inside || workspace == "" || pane == "" {
		return false
	}
	cfg := core.LoadConfigFor(env, cwd, sel)
	root := filepath.Clean(core.StateRootPath(&cfg, env, cwd))
	dir, err := Dir(root, parent.ID)
	if err != nil || !realDirectory(filepath.Dir(dir)) || !realDirectory(dir) {
		return false
	}
	rec, ok, err := readLaunch(dir)
	if err != nil || !ok || rec.State != launchConfirmed || !validLaunchNonce(rec.Nonce) {
		return false
	}
	if rec.ID != parent.ID || rec.Store != root || rec.Workspace != workspace || rec.Pane != pane {
		return false
	}
	if rec.ConfigBase != sel.Base || rec.Home != sel.Home {
		return false
	}
	if rec.PID != parent.PID || rec.Started != parent.Started || rec.Exe != parent.Exe {
		return false
	}
	st, ok := readManagedState(dir)
	return ok && st.WorkspaceID == rec.Workspace && st.RootPane == rec.Pane && sameDirectory(st.Dir, cwd)
}

// managedParentInfo is a direct parent identified as a job supervisor.
type managedParentInfo struct {
	PID     int
	ID      string
	Started string
	Exe     string
}

// parentKind is managedParent's classification of the direct parent.
type parentKind int

const (
	// parentNotSupervisor: the parent's arguments were read and are not
	// `job supervise --id ...`.
	parentNotSupervisor parentKind = iota
	// parentSupervisor: identified (managedParentInfo is filled).
	parentSupervisor
	// parentUnknown: not identified either way.
	parentUnknown
)

// managedParent classifies this process's direct parent from the process
// table alone. It is parentSupervisor only when the parent pid is running
// with a start identity, its arguments are exactly
// `<exe> job supervise --id <ID>` or
// `<exe> job supervise --id <ID> --repo <ORG/REPO>` (the two commands job
// start runs, in that order, the repo valid by the same pattern) with a
// valid job id, its kernel executable path is the file this process runs
// (SameExecutable), and a second read shows the same parent pid with the
// same start identity. Arguments that were read and do not have the
// supervise shape are parentNotSupervisor. Everything else — no parent (an
// orphan reparented to pid 1 included), an exited parent, an unreadable
// identity or argument read, a supervise-shaped command with an invalid id
// or repo, or another executable, a pid reused between the reads — is
// parentUnknown. The arguments are compared and never kept, printed or
// logged.
func managedParent(env platform.Env) (managedParentInfo, parentKind) {
	ppid := os.Getppid()
	if ppid <= 1 {
		return managedParentInfo{}, parentUnknown
	}
	before, _, live := platform.ProcStartIdentity(ppid, env)
	if live != platform.ProcRunning || before == "" {
		return managedParentInfo{}, parentUnknown
	}
	exe, argv, err := platform.ProcExec(ppid)
	if err != nil || exe == "" || len(argv) == 0 {
		return managedParentInfo{}, parentUnknown
	}
	if len(argv) < 3 || argv[1] != "job" || argv[2] != "supervise" {
		return managedParentInfo{}, parentNotSupervisor
	}
	// Exactly the two forms job start runs, in that order: `--id <ID>` and
	// `--id <ID> --repo <ORG/REPO>`; any other variation is the invalid
	// shape.
	var id, repo string
	hasRepo := false
	switch {
	case len(argv) == 5 && argv[3] == "--id":
		id = argv[4]
	case len(argv) == 7 && argv[3] == "--id" && argv[5] == "--repo":
		id, repo, hasRepo = argv[4], argv[6], true
	default:
		return managedParentInfo{}, parentUnknown
	}
	// The id and, in the seven-argument form, the repo (an empty value
	// included) are valid by the same rules (job.Dir's regex; the repo's
	// org/name pattern): an invalid value is the invalid shape.
	if _, err := Dir("", id); err != nil || (hasRepo && !repoPattern.MatchString(repo)) || !SameExecutable(exe) {
		return managedParentInfo{}, parentUnknown
	}
	after, _, live := platform.ProcStartIdentity(ppid, env)
	if live != platform.ProcRunning || after != before || os.Getppid() != ppid {
		return managedParentInfo{}, parentUnknown
	}
	return managedParentInfo{PID: ppid, ID: id, Started: before, Exe: exe}, parentSupervisor
}

// SameExecutable reports whether path is the file this process runs: the
// kernel executable path (platform.ProcExec) and path name one file
// (os.SameFile; a path that cannot be read is not the same).
func SameExecutable(path string) bool {
	self, _, err := platform.ProcExec(os.Getpid())
	if err != nil || self == "" || path == "" {
		return false
	}
	a, errA := os.Stat(self)
	b, errB := os.Stat(path)
	return errA == nil && errB == nil && a.Mode().IsRegular() && os.SameFile(a, b)
}

// realDirectory reports whether path is a directory itself (Lstat: a
// symlink to a directory is not one).
func realDirectory(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.IsDir()
}

// managedState is the part of state.json a managed command or a rebind
// checks.
type managedState struct {
	Status      string `json:"status"`
	Dir         string `json:"dir"`
	WorkspaceID string `json:"workspace_id"`
	RootPane    string `json:"root_pane"`
}

// readManagedState reads <dir>/state.json through readPrivateFile (a
// regular file of at most maxManagedStateBytes at its own path, opened
// without following links or blocking).
func readManagedState(dir string) (managedState, bool) {
	data, ok, err := readPrivateFile(filepath.Join(dir, "state.json"), maxManagedStateBytes)
	if err != nil || !ok {
		return managedState{}, false
	}
	var st managedState
	if err := json.Unmarshal(data, &st); err != nil || st.Dir == "" {
		return managedState{}, false
	}
	return st, true
}

// sameDirectory reports whether a and b name the same directory once both
// are resolved (symlinks included).
func sameDirectory(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	if errA != nil || errB != nil {
		return false
	}
	ia, errA := os.Stat(ra)
	ib, errB := os.Stat(rb)
	return errA == nil && errB == nil && ia.IsDir() && os.SameFile(ia, ib)
}
