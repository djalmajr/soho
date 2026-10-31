// F11: the compact-phase marker. A dispatch --compact claims the record at
// wait/<agent>.compact-pending.json before its compaction phase sends any
// command and removes the claimed token when the phase exits, so the marker
// stands only while the phase runs or after an interruption that skipped the
// cleanup (SIGKILL). status reads the marker without modifying it and
// classifies the owner with the same three-way by-pid read the claim uses.
package core

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
)

// CompactPendingVersion is the compact-pending marker version.
const CompactPendingVersion = 1

// compactIntegerPID reports whether v is an exact positive integer the int
// and the platform's by-pid API both hold without truncation: no fractional
// value, the exclusive int bound math.Ldexp(1, IntSize-1) (one past int's
// maximum) keeps the int conversion defined, and the API bound keeps the
// value inside the native pid type: unix pid_t is a signed 32-bit
// (math.MaxInt32); the windows pid is a DWORD (math.MaxUint32), still
// limited by the int on 32-bit builds.
func compactIntegerPID(v any) (int, bool) {
	n, ok := v.(float64)
	if !ok || n < 1 || n != math.Trunc(n) || n >= math.Ldexp(1, strconv.IntSize-1) {
		return 0, false
	}
	// The by-pid API bounds, kept explicitly in float64 (no int
	// conversion): unix pid_t is a signed 32-bit; the windows pid is a
	// DWORD (math.MaxUint32), still limited by the int on 32-bit builds.
	max := float64(math.MaxInt32)
	if runtime.GOOS == "windows" && strconv.IntSize == 64 {
		max = float64(math.MaxUint32)
	}
	if n > max {
		return 0, false
	}
	return int(n), true
}

// CompactPendingPath is the marker path below the state dir:
// wait/<agent>.compact-pending.json. release removes every wait/<agent>.*
// entry, which covers the marker.
func CompactPendingPath(dir, agent string) string {
	return filepath.Join(dir, "wait", agent+".compact-pending.json")
}

// CompactPending is one compact-pending record: the claimer's process
// identity (so a later read can tell a phase still running from one whose
// process died), the pane the phase runs in, and the brief the dispatch was
// about to send — the absolute path only, never the content.
type CompactPending struct {
	Version   int
	Token     string
	Pane      string
	Brief     string
	PID       int
	Started   string
	CreatedAt string
}

// CompactPendingRead reads and validates the marker at path. The error is
// os.ErrNotExist when the file is absent; every other failure (an unreadable
// file, broken JSON, a non-object, an unsupported version or a missing field)
// is a corrupted record: claim and check refuse (4) without replacing it, and
// status reports a visible error (4) without removing it.
func CompactPendingRead(path string) (*CompactPending, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return compactPendingParse(path, raw)
}

// compactInspectionMax is the private metadata cap of the inspection read:
// a marker larger than it is refused as unverifiable, so a per-machine scan
// can never hold a reader on one oversized state record.
const compactInspectionMax = 64 << 10

// CompactPendingReadInspect is the bounded nonblocking inspection read:
// it confines the marker to its opened parent, refuses linked entries and
// verifies their identity against the nonblocking opened regular handle,
// reads at most
// compactInspectionMax bytes, and shares compactPendingParse's validation.
// It writes nothing and repairs nothing; absence is os.ErrNotExist like the
// command reader, and a marker over the cap is a corrupted record.
func CompactPendingReadInspect(path string) (*CompactPending, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	name := filepath.Base(path)
	before, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, platform.ErrNotRegular
	}
	f, err := root.OpenFile(name, platform.ReadOnlyNonBlocking, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	after, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() || !after.Mode().IsRegular() || !os.SameFile(before, opened) || !os.SameFile(opened, after) {
		return nil, platform.ErrNotRegular
	}
	raw, err := io.ReadAll(io.LimitReader(f, compactInspectionMax+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > compactInspectionMax {
		return nil, fmt.Errorf("the marker %s exceeds the %d-byte inspection cap", path, compactInspectionMax)
	}
	return compactPendingParse(path, raw)
}

// CompactPendingParse validates the marker bytes read by the
// inspection's source read (Source.ReadMetadata) with the exact
// validation the path readers share: broken JSON, a non-object, an
// unsupported version or a missing field is a corrupted record. It
// opens no path.
func CompactPendingParse(path string, raw []byte) (*CompactPending, error) {
	return compactPendingParse(path, raw)
}

// compactPendingParse is the marker validation the command and inspection
// readers share: broken JSON, a non-object, an unsupported version or a
// missing field is a corrupted record.
func compactPendingParse(path string, raw []byte) (*CompactPending, error) {
	value, err := jsonjs.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("the marker %s is not JSON: %v", path, err)
	}
	obj, ok := value.(*jsonjs.Object)
	if !ok {
		return nil, fmt.Errorf("the marker %s is not a JSON object", path)
	}
	version, _ := obj.Get("version")
	// An exact integer: the numbers parse to float64, so 1.9 must not read
	// as the version 1 (int(1.9) would truncate it).
	if v, ok := version.(float64); !ok || v != 1 {
		return nil, fmt.Errorf("the marker %s has an unsupported version %v", path, version)
	}
	token, _ := obj.Get("token")
	if s, ok := token.(string); !ok || s == "" {
		return nil, fmt.Errorf("the marker %s has no token", path)
	}
	pane, _ := obj.Get("pane")
	if s, ok := pane.(string); !ok || s == "" {
		return nil, fmt.Errorf("the marker %s has no pane", path)
	}
	brief, _ := obj.Get("brief")
	if s, ok := brief.(string); !ok || s == "" {
		return nil, fmt.Errorf("the marker %s has no brief path", path)
	}
	pidValue, _ := obj.Get("pid")
	// An exact positive integer: a fractional or out-of-range pid would be
	// truncated into a different process's identity, and this validation
	// runs before any by-pid consultation.
	pid, ok := compactIntegerPID(pidValue)
	if !ok {
		return nil, fmt.Errorf("the marker %s has no pid", path)
	}
	started, _ := obj.Get("started")
	if s, ok := started.(string); !ok || s == "" {
		return nil, fmt.Errorf("the marker %s has no start identity", path)
	}
	createdAt, _ := obj.Get("created_at")
	if s, ok := createdAt.(string); !ok || s == "" {
		return nil, fmt.Errorf("the marker %s has no created_at", path)
	}
	if _, err := time.Parse(time.RFC3339, createdAt.(string)); err != nil {
		return nil, fmt.Errorf("the marker %s created_at is not RFC3339: %s", path, createdAt)
	}
	return &CompactPending{
		Version:   CompactPendingVersion,
		Token:     token.(string),
		Pane:      pane.(string),
		Brief:     brief.(string),
		PID:       int(pid),
		Started:   started.(string),
		CreatedAt: createdAt.(string),
	}, nil
}

// CompactOwner is the classification of a marker's owner process.
type CompactOwner int

const (
	// CompactOwnerAlive: the pid runs with the recorded start; the phase is
	// in progress.
	CompactOwnerAlive CompactOwner = iota
	// CompactOwnerDead: the pid is gone, or it restarted as another process
	// (a different start); the phase was interrupted.
	CompactOwnerDead
	// CompactOwnerUnknown: the read failed; a failed read is not an absence.
	CompactOwnerUnknown
)

// CompactPendingOwner classifies the marker's owner with the platform's
// by-pid three-way read (ReadProc) and start-time comparison (SameStarted);
// no TTL or clock math counts as proof of death.
func CompactPendingOwner(rec *CompactPending, env platform.Env) CompactOwner {
	started, liveness := platform.ReadProc(rec.PID, env)
	switch liveness {
	case platform.ProcRunning:
		if platform.SameStarted(started, rec.Started) {
			return CompactOwnerAlive
		}
		return CompactOwnerDead
	case platform.ProcGone:
		return CompactOwnerDead
	default:
		return CompactOwnerUnknown
	}
}

// CompactPendingSelf reads this process's identity (pid and start) with the
// same by-pid read the owner classification uses. ok is false when the read
// does not come back running with a start: the claim then fails before any
// send (exit 4), because a record without a verified owner identity could
// never be classified again.
func CompactPendingSelf(env platform.Env) (pid int, started string, ok bool) {
	pid = os.Getpid()
	started, liveness := platform.ReadProc(pid, env)
	return pid, started, liveness == platform.ProcRunning && started != ""
}

// CompactPhaseClaimOptions names the claim parameters: the state dir and the
// agent with its roster pane, the brief's absolute path, and the env the
// process reads use.
type CompactPhaseClaimOptions struct {
	SD    string
	Agent string
	Pane  string
	Brief string
	Env   platform.Env
}

func compactToken() string {
	nameBytes := make([]byte, 16)
	if _, err := rand.Read(nameBytes); err != nil {
		panic(err)
	}
	return hex.EncodeToString(nameBytes)
}

// compactOwnEnv returns env with the system paths appended to PATH when
// they are absent: the writing process's own identity must be readable
// even when the command's PATH is hermetic (the test fixtures do), while
// the owner reads — claims about other processes — keep the passed env
// untouched. The windows by-pid read is a direct kernel32 call that
// ignores the env, so the adjustment is unix-only.
func compactOwnEnv(env platform.Env) platform.Env {
	if runtime.GOOS == "windows" {
		return env
	}
	const system = "/usr/bin:/bin"
	path := env.Get("PATH")
	if strings.Contains(path, system) {
		return env
	}
	out := env.Clone()
	if path == "" {
		out["PATH"] = system
	} else {
		out["PATH"] = path + string(os.PathListSeparator) + system
	}
	return out
}

// CompactPhaseClaim claims the compact-pending marker under the roster lock
// (held only for the marker file transition, never across the compaction) and
// returns the claimed token. It reads this process's own identity first and
// fails before any send when it cannot (exit 4). Under the lock it also
// refuses before any send a same-pane record whose owner is alive or
// unverifiable (exit 10) and a corrupted record (exit 4, never replaced); a
// record whose owner is proven dead (a different start included) or whose
// pane is no longer the agent's is replaced.
func CompactPhaseClaim(opts CompactPhaseClaimOptions) (token string) {
	pid, selfStarted, ok := CompactPendingSelf(compactOwnEnv(opts.Env))
	if !ok {
		DieFriction(fmt.Sprintf("dispatch: could not read this process's identity before the compact phase of '%s'; the brief was not sent", opts.Agent), 4, "", "")
	}
	token = compactToken()
	WithRosterLock(opts.SD, func() {
		path := CompactPendingPath(opts.SD, opts.Agent)
		rec, err := CompactPendingRead(path)
		switch {
		case err == nil:
			if rec.Pane == opts.Pane {
				switch owner := CompactPendingOwner(rec, opts.Env); owner {
				case CompactOwnerAlive:
					DieFriction(fmt.Sprintf("dispatch: the compact phase of '%s' (pane %s) is in progress (owner pid %d is alive); the brief was not sent", opts.Agent, opts.Pane, rec.PID), 10, "", "")
				case CompactOwnerUnknown:
					DieFriction(fmt.Sprintf("dispatch: the compact phase of '%s' (pane %s) has an unverifiable owner (pid %d, liveness unknown); the brief was not sent", opts.Agent, opts.Pane, rec.PID), 10, "", "")
				}
			}
		case !errors.Is(err, os.ErrNotExist):
			DieFriction(fmt.Sprintf("dispatch: the compact-phase marker %s of '%s' is unreadable; it was not replaced; the brief was not sent", path, opts.Agent), 4, "", "")
		}
		if err := os.MkdirAll(filepath.Join(opts.SD, "wait"), 0o777); err != nil {
			panic(err)
		}
		record := &CompactPending{
			Version:   CompactPendingVersion,
			Token:     token,
			Pane:      opts.Pane,
			Brief:     opts.Brief,
			PID:       pid,
			Started:   selfStarted,
			CreatedAt: platform.Now().UTC().Format(time.RFC3339),
		}
		content := jsonjs.Stringify(record.json()) + "\n"
		if err := platform.AtomicWrite(path, content); err != nil {
			panic(err)
		}
	})
	return token
}

// json renders the record in the field order of the contract.
func (r *CompactPending) json() *jsonjs.Object {
	return jsonjs.O(
		"version", r.Version,
		"token", r.Token,
		"pane", r.Pane,
		"brief", r.Brief,
		"pid", r.PID,
		"started", r.Started,
		"created_at", r.CreatedAt,
	)
}

// CompactPhaseRemove removes the marker only when its token is the claimed
// one: a record a later claim wrote and a corrupted file (its token cannot
// be verified) are left in place. The read-check-remove runs under the same
// roster lock the claim uses, only for the file transition; a missing marker
// is a no-op success.
func CompactPhaseRemove(sd, agent, token string) error {
	var result error
	WithRosterLock(sd, func() {
		path := CompactPendingPath(sd, agent)
		rec, err := CompactPendingRead(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			return
		case err != nil:
			result = fmt.Errorf("the marker %s is unreadable; it was left in place: %v", path, err)
			return
		}
		if rec.Token != token {
			result = fmt.Errorf("the marker %s carries another token: the phase it records is not this claim's; it was left in place", path)
			return
		}
		result = os.Remove(path)
	})
	return result
}

// CompactPhaseCheck runs at the start of every dispatch (--compact or not),
// before any state change or send: a same-pane record whose owner is alive or
// unverifiable refuses the send (exit 10); an unreadable marker refuses it
// (exit 4) and is never removed; a record whose owner is proven dead (a
// different start included) or whose pane is no longer the agent's is
// removed so the send proceeds, and a removal that fails refuses the send
// (exit 4) with the file kept. The refusal is the whole serialization
// guarantee: dispatches are never otherwise queued against each other.
func CompactPhaseCheck(sd, agent, pane string, env platform.Env) {
	WithRosterLock(sd, func() {
		path := CompactPendingPath(sd, agent)
		rec, err := CompactPendingRead(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return
			}
			DieFriction(fmt.Sprintf("dispatch: the compact-phase marker %s of '%s' is unreadable; the brief was not sent", path, agent), 4, "", "")
		}
		if rec.Pane != pane {
			removeStale(path, agent, fmt.Sprintf("foreign pane %s", rec.Pane))
			return
		}
		switch owner := CompactPendingOwner(rec, env); owner {
		case CompactOwnerAlive:
			DieFriction(fmt.Sprintf("dispatch: the compact phase of '%s' (pane %s) is in progress (owner pid %d is alive); the brief was not sent; wait for it to end (soho status %s)", agent, pane, rec.PID, agent), 10, "", "")
		case CompactOwnerUnknown:
			DieFriction(fmt.Sprintf("dispatch: the compact phase of '%s' (pane %s) has an unverifiable owner (pid %d, liveness unknown); the brief was not sent; wait for it to end (soho status %s)", agent, pane, rec.PID, agent), 10, "", "")
		case CompactOwnerDead:
			removeStale(path, agent, "dead owner")
		}
	})
}

// removeStale refuses the send (exit 4) when a stale marker cannot be
// removed: the removal error keeps the file, and the marker would otherwise
// keep claiming "brief was not sent" in status after the brief went out.
func removeStale(path, agent, cause string) {
	if err := os.Remove(path); err != nil {
		DieFriction(fmt.Sprintf("dispatch: the compact-phase marker %s of '%s' (%s) could not be removed: %v; the brief was not sent", path, agent, cause, err), 4, "", "")
	}
}
