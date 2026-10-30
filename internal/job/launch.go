package job

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/platform"
)

// launch.go is the private startup confirmation between `job start` and the
// `job supervise` process it launches in the job workspace's root pane. A
// pane-run acknowledgement only proves that Herdr accepted the command; the
// starter publishes running only after the supervisor process itself has
// found this job in this store, from this workspace's root pane, built its
// supervisor and is about to run it, while this starter still waits. The
// confirmation is evidence of that startup only, not of later health.
//
// The evidence is one private file, <job dir>/launch.json (mode 0600,
// written atomically), never part of state.json, the events, the report or
// any command output. Every read-modify-write of it runs under its own lock
// file, <job dir>/launch.lock, taken only with non-blocking tries retried
// until a monotonic deadline — never the job lock and never start.lock —
// so no launch step can wait without bound on another process. The
// supervisor never waits for start.lock or for the running state before
// confirming, so the confirmation cannot deadlock with the starter's held
// start.lock or with Supervisor.waitRunning.
//
// States: the starter issues a pending record (fresh nonce, store, workspace
// and root pane) before `pane run`; the supervisor turns it confirmed; the
// starter turns a still-pending record revoked when its wait ends without
// a confirmation, and crash recovery does the same for a dead starter.
// Under the launch lock a confirm and a revoke cannot both win. The
// supervisor never refuses to run because of the record: a launch it
// cannot confirm (none, already confirmed, revoked, stale, another pane,
// unreadable, lock busy) leaves the record as it is and the supervisor
// continues exactly as before this record existed.
//
// Files: launch.json and launch.lock are used only when they are regular
// files of at most maxLaunchRecordBytes and maxLaunchBytes, and the opened
// handle must be the file the path names (os.SameFile against an Lstat),
// so a FIFO, a device, a directory, a symlink or an oversized file at
// either path cannot block a step or redirect it outside the job
// directory; such a file is treated
// like an unreadable record. launch.json and start.lock are opened without
// following links or blocking (openNoFollow: the no-follow and
// non-blocking flags on unix systems, the reparse-point open on Windows,
// a refusal elsewhere). launch.lock, which the open may create, gets the
// no-follow and non-blocking flags on unix systems only; elsewhere the
// Lstat pre-check and the post-open regular and SameFile checks apply, and
// an entry swapped after the Lstat may be opened (followed) before the
// identity check refuses it, not proven on Windows.
//
// Threat model: both files are private to the job directory and protected
// only by their 0600 mode and the directory's owner. Any process of the same
// user can read the nonce or write the record. The confirmation therefore
// fences stale, duplicated, mismatched and late launches of cooperating
// soho processes; it is not an authenticated proof against another
// process running as the same user.
//
// Time: every wait on launch.lock is bounded by a deadline read on the
// monotonic clock; once a try succeeds, the guarded step (local reads and
// one atomic write) runs even if the deadline has just passed. The starter's
// decision is made in the revoke critical section: a confirmation written
// before that section takes the lock is accepted even when it landed after
// the nominal wait (an allowed linearization race, not a late expiry), and
// none is accepted after it. The bound is on how long the starter waits,
// not a guarantee about the latest moment a confirmation can be accepted.

// launchFileName is the private record; launchLockName its lock.
const (
	launchFileName = "launch.json"
	launchLockName = "launch.lock"
)

const (
	launchPending   = "pending"
	launchConfirmed = "confirmed"
	launchRevoked   = "revoked"
)

// launchVersion is the record's own version.
const launchVersion = 1

const (
	// defaultLaunchWait bounds the starter's wait for the confirmation,
	// measured on the monotonic clock from the pane-run acknowledgement;
	// 60 s since the measured Windows start under Defender and OneDrive
	// load took 2.5–4 min, outlasting the former 30 s bound (DJA-348).
	defaultLaunchWait = 60 * time.Second
	// defaultLaunchPoll is the pause between two reads of the record.
	defaultLaunchPoll = 100 * time.Millisecond
	// launchLockRetry is the pause between two tries of launch.lock.
	launchLockRetry = 10 * time.Millisecond
	// launchRevokeWait bounds the revoke after the wait ended, and the
	// supervisor's confirmation, when launch.lock is busy.
	launchRevokeWait = 5 * time.Second
)

// maxLaunchBytes caps launch.lock and start.lock (both empty).
const maxLaunchBytes = 4096

// The record's own budget. Every string field of launchRecord has a cap,
// checked by writeLaunch, and maxLaunchRecordBytes, the bounded read of
// launch.json, is computed from those caps, so a record with every field
// at its cap is still readable:
//
//   - the two locator fields (ConfigBase, Home) at maxLocatorBytes, as
//     validated on both sides (locator.go);
//   - the two system path fields (Store, Exe) at launchSystemPathMax, the
//     longest path an operating system here hands out (32768 UTF-16 units
//     on Windows, at most 3 UTF-8 bytes each; unix paths are shorter);
//   - the eight token fields (ID, Workspace, Pane, Nonce, Issued, State,
//     Confirmed, Started) at launchTokenMax (an id is at most 64 bytes, a
//     workspace id 128, a nonce 32, a timestamp or start identity well
//     under 100);
//   - json.Marshal writes at most launchJSONEscape bytes per input byte
//     (\u00XX for a control byte, \u003c for <, \ufffd for an invalid
//     byte);
//   - launchRecordOverhead covers the 14 keys, quotes, separators, the two
//     integers and the newline (measured at 199 bytes).
const (
	launchSystemPathMax  = 3 * 32768
	launchTokenMax       = 256
	launchJSONEscape     = 6
	launchRecordOverhead = 256
	maxLaunchRecordBytes = launchJSONEscape*(2*maxLocatorBytes+2*launchSystemPathMax+8*launchTokenMax) + launchRecordOverhead
)

// errLaunchRecordTooLarge is a record with a field over its cap: it is
// never written.
var errLaunchRecordTooLarge = errors.New("job: launch record field over its cap")

// launchRecordFits reports whether every string field of rec is within its
// cap (see maxLaunchRecordBytes).
func launchRecordFits(rec launchRecord) bool {
	for _, field := range []string{rec.ConfigBase, rec.Home} {
		if len(field) > maxLocatorBytes {
			return false
		}
	}
	for _, field := range []string{rec.Store, rec.Exe} {
		if len(field) > launchSystemPathMax {
			return false
		}
	}
	for _, field := range []string{rec.ID, rec.Workspace, rec.Pane, rec.Nonce, rec.Issued, rec.State, rec.Confirmed, rec.Started} {
		if len(field) > launchTokenMax {
			return false
		}
	}
	return true
}

// errLaunchLockBusy is a launch.lock that stayed held until the deadline.
var errLaunchLockBusy = errors.New("job: the launch lock stayed busy")

// errLaunchFile is a launch.json, launch.lock or start.lock that is not a
// private regular file within its size cap at its own path.
var errLaunchFile = errors.New("job: launch file is not a private regular file")

// openLaunchLock opens <dir>/launch.lock, creating it 0600, only as a
// regular file of at most maxLaunchBytes that is the file the path names:
// an existing non-regular entry (a symlink included) is refused before the
// open, the open neither follows a symlink nor blocks on a FIFO where the
// system has them, and the opened handle is compared with a fresh Lstat.
func openLaunchLock(dir string) (*os.File, error) {
	path := filepath.Join(dir, launchLockName)
	if pre, err := os.Lstat(path); err == nil && !pre.Mode().IsRegular() {
		return nil, errLaunchFile
	}
	f, err := os.OpenFile(path, launchLockOpenFlags, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err == nil && (!info.Mode().IsRegular() || info.Size() > maxLaunchBytes) {
		err = errLaunchFile
	}
	if err == nil {
		post, lerr := os.Lstat(path)
		if lerr != nil || !post.Mode().IsRegular() || !os.SameFile(info, post) {
			err = errLaunchFile
		}
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// launchRecord is the content of launch.json.
type launchRecord struct {
	V         int    `json:"v"`
	ID        string `json:"id"`
	Store     string `json:"store"`
	Workspace string `json:"workspace"`
	Pane      string `json:"pane"`
	Nonce     string `json:"nonce"`
	Issued    string `json:"issued"`
	State     string `json:"state"`
	PID       int    `json:"pid,omitempty"`
	Confirmed string `json:"confirmed,omitempty"`
	// Started and Exe are the bound supervisor's process start identity
	// (platform.ProcStartIdentity) and kernel executable path
	// (platform.ProcExec), and ConfigBase and Home the private user
	// configuration selection it consumed (locator.go): the binding the
	// team commands it runs check (managed.go). All four stay in this
	// private file only.
	Started    string `json:"started,omitempty"`
	Exe        string `json:"exe,omitempty"`
	ConfigBase string `json:"config_base,omitempty"`
	Home       string `json:"home,omitempty"`
}

// sameLaunch reports whether got is the launch want describes: the same
// version, job, store, workspace, root pane and nonce.
func sameLaunch(got, want launchRecord) bool {
	return got.V == launchVersion && got.ID == want.ID && got.Store == want.Store &&
		got.Workspace == want.Workspace && got.Pane == want.Pane && got.Nonce == want.Nonce
}

// newLaunchNonce returns 16 random bytes in hex.
func newLaunchNonce() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// withLaunchLock runs fn holding <dir>/launch.lock (openLaunchLock). The
// lock is only tried without blocking; a held lock is retried every
// launchLockRetry until the clock's monotonic reading reaches until, and
// then errLaunchLockBusy is returned without running fn. A successful try
// runs fn at once, even just after until (see Time above). A lock file
// that cannot be opened as a private regular file fails at once.
func withLaunchLock(dir string, clock Clock, until int64, fn func() error) error {
	for {
		f, err := openLaunchLock(dir)
		if err != nil {
			return err
		}
		err = tryFlock(f)
		if err == nil {
			defer func() {
				_ = funlock(f)
				_ = f.Close()
			}()
			return fn()
		}
		_ = f.Close()
		if !errors.Is(err, errStartLockHeld) {
			return err
		}
		if clock.Monotonic() >= until {
			return errLaunchLockBusy
		}
		clock.Sleep(launchLockRetry)
	}
}

// readLaunch reads the record in dir; ok is false when the file is absent.
// The record is read only through readPrivateFile (a regular file of at
// most maxLaunchRecordBytes that is the file the path names, opened without
// following links or blocking), so a symlink, a FIFO or an oversized file
// is an error like a malformed one.
func readLaunch(dir string) (launchRecord, bool, error) {
	data, ok, err := readPrivateFile(filepath.Join(dir, launchFileName), maxLaunchRecordBytes)
	if err != nil || !ok {
		return launchRecord{}, false, err
	}
	var rec launchRecord
	if err := json.Unmarshal(data, &rec); err != nil || rec.V != launchVersion {
		return launchRecord{}, false, errors.New("job: launch record unreadable")
	}
	return rec, true, nil
}

// writeLaunch writes rec atomically (0600) only when every field is
// within its cap, so what it writes readLaunch can read back.
func writeLaunch(dir string, rec launchRecord) error {
	if !launchRecordFits(rec) {
		return errLaunchRecordTooLarge
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if len(data)+1 > maxLaunchRecordBytes {
		return errLaunchRecordTooLarge
	}
	return writeAtomic0600(filepath.Join(dir, launchFileName), append(data, '\n'))
}

// issueLaunch writes a fresh pending record for the supervisor about to be
// launched in rootPane of workspaceID and returns it. It replaces any
// earlier record, so no earlier confirmation can stand for this launch:
// only the holder of start.lock issues one.
func issueLaunch(store *Store, clock Clock, id, workspaceID, rootPane string) (launchRecord, error) {
	dir, err := Dir(store.Root, id)
	if err != nil {
		return launchRecord{}, err
	}
	nonce, err := newLaunchNonce()
	if err != nil {
		return launchRecord{}, err
	}
	rec := launchRecord{
		V: launchVersion, ID: id, Store: filepath.Clean(store.Root),
		Workspace: workspaceID, Pane: rootPane, Nonce: nonce,
		Issued: clock.Now().UTC().Format(time.RFC3339Nano),
		State:  launchPending,
	}
	until := clock.Monotonic() + int64(launchRevokeWait)
	if err := withLaunchLock(dir, clock, until, func() error { return writeLaunch(dir, rec) }); err != nil {
		return launchRecord{}, err
	}
	return rec, nil
}

// launchOutcome is what the starter reads back from the record.
type launchOutcome int

const (
	// launchWaiting: still this launch's pending record, or the lock was
	// busy for this read.
	launchWaiting launchOutcome = iota
	// launchAccepted: this launch, confirmed.
	launchAccepted
	// launchLost: absent, unreadable, revoked, or not this launch.
	launchLost
)

// checkLaunch reads the record under launch.lock (tried until until) and
// classifies it against want.
func checkLaunch(dir string, clock Clock, until int64, want launchRecord) launchOutcome {
	outcome := launchWaiting
	err := withLaunchLock(dir, clock, until, func() error {
		rec, ok, err := readLaunch(dir)
		switch {
		case err != nil || !ok || !sameLaunch(rec, want):
			outcome = launchLost
		case rec.State == launchPending:
			outcome = launchWaiting
		case rec.State == launchConfirmed:
			outcome = launchAccepted
		default:
			outcome = launchLost
		}
		return nil
	})
	if err != nil && !errors.Is(err, errLaunchLockBusy) {
		return launchLost
	}
	return outcome
}

// revokeLaunch ends the starter's wait under launch.lock: want's pending
// record becomes revoked (accepted=false); want's confirmed record is
// accepted after all (the confirmation won the race). Anything else is not
// accepted and is left as it is. An error (the lock stayed busy for
// launchRevokeWait, or the record could not be read or written) means the
// launch is not fenced: a late confirmation remains possible.
func revokeLaunch(store *Store, clock Clock, want launchRecord) (bool, error) {
	dir, err := Dir(store.Root, want.ID)
	if err != nil {
		return false, err
	}
	accepted := false
	until := clock.Monotonic() + int64(launchRevokeWait)
	err = withLaunchLock(dir, clock, until, func() error {
		rec, ok, err := readLaunch(dir)
		if err != nil {
			return err
		}
		if !ok || !sameLaunch(rec, want) {
			return errors.New("job: launch record replaced")
		}
		switch rec.State {
		case launchConfirmed:
			accepted = true
			return nil
		case launchPending:
			rec.State = launchRevoked
			return writeLaunch(dir, rec)
		}
		return nil
	})
	return accepted, err
}

// launchFence is crash recovery's view of the launch record.
type launchFence int

const (
	// launchNone: no record — the start never issued one, so it never ran
	// the supervisor command.
	launchNone launchFence = iota
	// launchFenced: a pending record was revoked now, or a revoked one was
	// found: a supervisor command may have been run but cannot confirm.
	launchFenced
	// launchUncertain: a confirmed record (a supervisor started and may
	// still be alive), or a record that could not be read or fenced.
	launchUncertain
)

// fenceLaunch is crash recovery's step for a job whose starter is gone
// (start.lock is free and held by the recovery): a pending record becomes
// revoked in the fence's critical section under launch.lock, so no
// confirmation after that section is accepted; when the fence cannot take
// the lock, read or write the record, the launch stays uncertain and a
// late confirmation remains possible.
func fenceLaunch(store *Store, id string) launchFence {
	dir, err := Dir(store.Root, id)
	if err != nil {
		return launchUncertain
	}
	if _, err := os.Lstat(filepath.Join(dir, launchFileName)); os.IsNotExist(err) {
		return launchNone
	}
	clock := store.clock()
	fence := launchUncertain
	until := clock.Monotonic() + int64(launchRevokeWait)
	_ = withLaunchLock(dir, clock, until, func() error {
		rec, ok, err := readLaunch(dir)
		if err != nil {
			return nil
		}
		if !ok {
			fence = launchNone
			return nil
		}
		switch rec.State {
		case launchPending:
			rec.State = launchRevoked
			if writeLaunch(dir, rec) == nil {
				fence = launchFenced
			}
		case launchRevoked:
			fence = launchFenced
		}
		return nil
	})
	return fence
}

// ConfirmLaunch is the supervisor's side, run after the job was located and
// the supervisor built, right before Supervisor.Run. It confirms a pending
// record only when its job, its store, its workspace (HERDR_WORKSPACE_ID)
// and its root pane (HERDR_PANE_ID) are this process's and its starter
// still holds start.lock (a free lock is a starter that died: the record is
// stale); the confirmation records this process id. It never refuses: in
// every other case it changes nothing and confirmed is false, and reason
// says why for the job's friction log when a launch was waiting on it
// (empty when there was nothing to confirm).
func ConfirmLaunch(store *Store, id string, env platform.Env) (confirmed bool, reason string) {
	return confirmLaunch(store, id, env, launchBinding{})
}

// launchBinding is what a confirmation records besides the PID: the
// supervisor's own process identity and the selection it consumed (empty
// for a supervisor without the private launch context).
type launchBinding struct {
	Started string
	Exe     string
	Sel     core.UserSelection
}

func confirmLaunch(store *Store, id string, env platform.Env, bind launchBinding) (confirmed bool, reason string) {
	dir, err := Dir(store.Root, id)
	if err != nil {
		return false, ""
	}
	if _, err := os.Lstat(filepath.Join(dir, launchFileName)); os.IsNotExist(err) {
		return false, ""
	}
	clock := store.clock()
	until := clock.Monotonic() + int64(launchRevokeWait)
	err = withLaunchLock(dir, clock, until, func() error {
		rec, ok, readErr := readLaunch(dir)
		if readErr != nil {
			reason = "the launch record is unreadable"
			return nil
		}
		if !ok || rec.State != launchPending {
			return nil
		}
		if rec.ID != id || rec.Store != filepath.Clean(store.Root) {
			reason = "the launch belongs to another job or store"
			return nil
		}
		// The caller identity is the supervisor's inherited environment claim,
		// read through the transport the same way the old direct env.Get did.
		caller := herdr.New(env).Runtime.Caller()
		if rec.Workspace != caller.WorkspaceID || rec.Pane != caller.PaneID {
			reason = "the launch belongs to another workspace or pane"
			return nil
		}
		// The starter holds start.lock for its whole wait. An absent lock
		// file is a starter that never ran or is gone; a lock file that is
		// not a regular file is not tried at all.
		startPath := filepath.Join(dir, "start.lock")
		startInfo, statErr := os.Lstat(startPath)
		if os.IsNotExist(statErr) {
			reason = "the launch is stale: its starter is gone"
			return nil
		}
		if statErr != nil {
			reason = "the start lock is unreadable"
			return nil
		}
		if !startInfo.Mode().IsRegular() || startInfo.Size() > maxLaunchBytes {
			reason = "the start lock is not a private regular file"
			return nil
		}
		held, lockErr := probeStartLock(startPath, startInfo)
		if lockErr != nil {
			if errors.Is(lockErr, errLaunchFile) {
				reason = "the start lock is not a private regular file"
			} else {
				reason = "the start lock is unreadable"
			}
			return nil
		}
		if !held {
			reason = "the launch is stale: its starter is gone"
			return nil
		}
		rec.State = launchConfirmed
		rec.PID = os.Getpid()
		rec.Confirmed = clock.Now().UTC().Format(time.RFC3339Nano)
		rec.Started, rec.Exe = bind.Started, bind.Exe
		rec.ConfigBase, rec.Home = bind.Sel.Base, bind.Sel.Home
		if writeErr := writeLaunch(dir, rec); writeErr != nil {
			reason = "the confirmation could not be written"
			return nil
		}
		confirmed = true
		return nil
	})
	if err != nil {
		switch {
		case errors.Is(err, errLaunchLockBusy):
			return false, "the launch lock stayed busy"
		case errors.Is(err, errLaunchFile):
			return false, "the launch lock is not a private regular file"
		default:
			return false, "the launch lock is unreadable"
		}
	}
	return confirmed, reason
}

// awaitLaunch waits for the supervisor's confirmation of rec until the
// monotonic deadline (wait from now, read on clock), polling every poll,
// then ends the wait with revokeLaunch. Every lock wait is bounded by the
// same deadline and the revoke's by launchRevokeWait, each overshooting by
// less than one launchLockRetry, so the starter's waiting ends within wait
// plus launchRevokeWait plus one poll plus two launchLockRetry (and the
// local file operations), whatever another process does with launch.lock.
// No goroutine is left behind. A confirmation that wins the revoke's
// critical section is accepted even after the nominal wait (see Time).
// accepted is true only for rec's own confirmed record; err is a launch
// that could not be fenced. The starter calls it with defaultLaunchWait,
// defaultLaunchPoll and the store clock.
func awaitLaunch(store *Store, clock Clock, rec launchRecord, wait, poll time.Duration) (bool, error) {
	dir, err := Dir(store.Root, rec.ID)
	if err != nil {
		return false, err
	}
	deadline := clock.Monotonic() + int64(wait)
	for {
		outcome := checkLaunch(dir, clock, deadline, rec)
		if outcome == launchAccepted {
			return true, nil
		}
		if outcome == launchLost || clock.Monotonic() >= deadline {
			break
		}
		clock.Sleep(poll)
	}
	return revokeLaunch(store, clock, rec)
}
