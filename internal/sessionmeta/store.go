// Package sessionmeta stores exactly one private per-session orchestrator
// record, orchestrator.json, inside the configured state base's session
// directory. It is a filesystem leaf: no core, kinds, doctor, cli, spawn or
// job imports, no output, no friction, no process calls, no global state.
// The configured base is canonicalized once (its trusted filesystem alias
// is accepted), then the session and record are confined through
// handle-relative os.Root resolution: both components must be plain
// directories or a regular record, never links or nonregular files.
// Readers never create, repair or write anything; writers require the base
// and session to exist already, serialize through their own lock directory,
// and create only their own lock, temp and record there.
package sessionmeta

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/djalmajr/soho/internal/platform"
)

const (
	// RecordName is the only file the store reads or writes in a session.
	RecordName = "orchestrator.json"
	// Schema is the record format version.
	Schema = 1

	lockName  = ".orchestrator.lock"
	maxRecord = 64 << 10 // bounds one complete record read or write
	lockWait  = 5 * time.Second
	lockPoll  = 25 * time.Millisecond
)

var (
	// ErrNotInitialized is a writer result for a missing state base or
	// session directory; a later init owns safe initialization.
	ErrNotInitialized = errors.New("sessionmeta: the session state is not initialized")
	// ErrLocked is a writer result when the lock directory is not free
	// within the bound; no stale lock is deleted.
	ErrLocked = errors.New("sessionmeta: the orchestrator record is locked")
	// ErrRecordChanged refuses a read whose regular record was replaced
	// between the entry check and the handle open. No record is returned.
	ErrRecordChanged = errors.New("sessionmeta: the record identity changed before the read")
)

// Record is the private orchestrator record. Task empty means explicitly
// clear. There are deliberately no role, model, effort, argv or report
// fields.
type Record struct {
	Schema      int    `json:"schema"`
	ProjectRoot string `json:"project_root"`
	SessionID   string `json:"session_id"`
	Terminal    string `json:"terminal"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	StartedMS   int64  `json:"started_ms"`
	Task        string `json:"task"`
}

// Store addresses one session's record under the configured state base.
type Store struct {
	StateBase string
	SessionID string
}

// ValidSessionID reports whether value is a safe, portable ONE state
// component for a session directory: the shared platform validator, which
// rejects empty values, dots, separators, drive forms, controls, invalid
// UTF-8, trailing dot or space, Windows invalid characters and reserved
// device stems, and admits otherwise safe Unicode and ordinary opaque ids.
// It is private storage safety, not a public identifier grammar.
func ValidSessionID(value string) bool {
	return platform.ValidStateComponent(value)
}

// Read returns the session's record. Absence is false with a nil error;
// an unsafe, corrupt, oversized, version-mismatched or identity-mismatched
// record is an error. Read creates nothing, writes nothing, never repairs
// anything and never acquires the lock.
func (s Store) Read() (Record, bool, error) {
	if !ValidSessionID(s.SessionID) {
		return Record{}, false, errors.New("sessionmeta: the session id is not a safe state component")
	}
	sessRoot, missing, err := s.openSession()
	if err != nil {
		return Record{}, false, err
	}
	if missing {
		return Record{}, false, nil
	}
	defer sessRoot.Close()
	return readRecord(sessRoot, s.SessionID)
}

// Register validates rec, requires the configured base and session to
// exist, and publishes the merged record atomically at 0600. The supplied
// SessionID must match the Store. An existing exact project/session match
// preserves its FIRST recorded StartedMS; a same Terminal also preserves
// Task (an explicit clear stays clear); a replacement terminal keeps the
// session start and takes the new input identity and task; a different
// project root starts fresh from the input.
func (s Store) Register(rec Record) (Record, error) {
	if !ValidSessionID(s.SessionID) {
		return Record{}, errors.New("sessionmeta: the session id is not a safe state component")
	}
	if rec.SessionID != s.SessionID {
		return Record{}, errors.New("sessionmeta: the record session does not match the store")
	}
	if err := validateRecord(rec); err != nil {
		return Record{}, err
	}
	sessRoot, missing, err := s.openSession()
	if err != nil {
		return Record{}, err
	}
	if missing {
		return Record{}, ErrNotInitialized
	}
	defer sessRoot.Close()
	var merged Record
	err = withLockedRecord(sessRoot, func() error {
		cur, present, err := readRecord(sessRoot, s.SessionID)
		if err != nil {
			return err
		}
		m := mergeRecord(cur, present, rec)
		data, err := marshalRecord(m)
		if err != nil {
			return err
		}
		if err := publishRecord(sessRoot, data); err != nil {
			return err
		}
		merged = m
		return nil
	})
	if err != nil {
		return Record{}, err
	}
	return merged, nil
}

// SetTask updates the task of the exact current project/session/terminal
// record. Absence or a nonmatching project, session or terminal returns
// false with a nil error and changes nothing, including initializing
// nothing and leaving no lock or temp behind; an unsafe or corrupt record
// remains an error.
func (s Store) SetTask(projectRoot, terminal, task string) (bool, error) {
	if !ValidSessionID(s.SessionID) {
		return false, errors.New("sessionmeta: the session id is not a safe state component")
	}
	if !safeIdentity(projectRoot) || !safeIdentity(terminal) {
		return false, nil
	}
	if !utf8.ValidString(task) {
		return false, errors.New("sessionmeta: the task is not valid UTF-8")
	}
	sessRoot, missing, err := s.openSession()
	if err != nil {
		return false, err
	}
	if missing {
		return false, nil
	}
	defer sessRoot.Close()
	var ok bool
	err = withLockedRecord(sessRoot, func() error {
		cur, present, err := readRecord(sessRoot, s.SessionID)
		if err != nil {
			return err
		}
		if !present || cur.ProjectRoot != projectRoot || cur.Terminal != terminal {
			return nil
		}
		cur.Task = task
		data, err := marshalRecord(cur)
		if err != nil {
			return err
		}
		if err := publishRecord(sessRoot, data); err != nil {
			return err
		}
		ok = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return ok, nil
}

// openSession resolves the configured base once, confines the session
// directory through handles and opens it as the working root. missing is
// true (and err nil) when the base or session directory does not exist;
// unsafe components and any I/O failure are errors. The caller owns the
// returned root.
func (s Store) openSession() (sessRoot *os.Root, missing bool, err error) {
	canonical, missing, err := baseInfo(s.StateBase)
	if err != nil || missing {
		return nil, missing, err
	}
	baseRoot, err := openDirRoot(canonical)
	if err != nil {
		return nil, false, fmt.Errorf("sessionmeta: the state base cannot be opened: %v", err)
	}
	defer baseRoot.Close()
	info, lerr := baseRoot.Lstat(s.SessionID)
	switch {
	case lerr != nil:
		if os.IsNotExist(lerr) {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("sessionmeta: the session directory cannot be checked: %v", lerr)
	case !info.IsDir():
		return nil, false, unsafeSession()
	}
	sessRoot, err = openSessionDir(baseRoot, s.SessionID, info)
	if err != nil {
		return nil, false, err
	}
	return sessRoot, false, nil
}

// baseInfo canonicalizes the configured existing state base once,
// accepting its trusted filesystem alias. Missing bases report missing
// (Read: absence; writers: ErrNotInitialized); a base that exists but is
// not a directory is an error.
func baseInfo(stateBase string) (string, bool, error) {
	if stateBase == "" {
		return "", true, nil
	}
	canonical, err := filepath.EvalSymlinks(stateBase)
	if err != nil {
		if os.IsNotExist(err) {
			return "", true, nil
		}
		return "", false, fmt.Errorf("sessionmeta: the state base cannot be resolved: %v", err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		if os.IsNotExist(err) {
			return "", true, nil
		}
		return "", false, fmt.Errorf("sessionmeta: the state base cannot be checked: %v", err)
	}
	if !info.IsDir() {
		return "", false, errors.New("sessionmeta: the state base is not a directory")
	}
	return canonical, false, nil
}

func unsafeSession() error {
	return errors.New("sessionmeta: the session path is not a plain directory")
}

func safeIdentity(value string) bool {
	return value != "" && utf8.ValidString(value) && !hasControl(value)
}

func hasControl(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// ValidateRecord checks a proposed record without reading or writing state.
func ValidateRecord(rec Record) error {
	return validateRecord(rec)
}

func validateRecord(rec Record) error {
	if rec.Schema != Schema {
		return fmt.Errorf("sessionmeta: the record schema is %d, want %d", rec.Schema, Schema)
	}
	if !safeIdentity(rec.ProjectRoot) {
		return errors.New("sessionmeta: the record project root is not a safe identity")
	}
	if !safeIdentity(rec.SessionID) {
		return errors.New("sessionmeta: the record session is not a safe identity")
	}
	if !safeIdentity(rec.Terminal) {
		return errors.New("sessionmeta: the record terminal is not a safe identity")
	}
	if !safeIdentity(rec.Name) || !safeIdentity(rec.Kind) {
		return errors.New("sessionmeta: the record name or kind is not a safe identity")
	}
	if !utf8.ValidString(rec.Task) {
		return errors.New("sessionmeta: the record task is not valid UTF-8")
	}
	if rec.StartedMS < 0 {
		return errors.New("sessionmeta: the record start is negative")
	}
	return nil
}

// mergeRecord applies the register rules against the record re-read under
// the lock: the session is guaranteed equal by the Store, so the existing
// exact project match preserves its first recorded start; a same terminal
// also preserves the task; everything else takes the input.
func mergeRecord(cur Record, present bool, rec Record) Record {
	if !present || cur.ProjectRoot != rec.ProjectRoot {
		return rec
	}
	out := rec
	out.StartedMS = cur.StartedMS
	if cur.Terminal == rec.Terminal {
		out.Task = cur.Task
	}
	return out
}

// readRecord reads the confined record: a regular file at the fixed name,
// at most the bound, whose opened handle keeps the Lstat identity before
// any byte is consumed. Absence is false with a nil error.
func readRecord(sessRoot *os.Root, session string) (Record, bool, error) {
	pre, err := sessRoot.Lstat(RecordName)
	if err != nil {
		if os.IsNotExist(err) {
			return Record{}, false, nil
		}
		return Record{}, false, fmt.Errorf("sessionmeta: the record cannot be checked: %v", err)
	}
	if !pre.Mode().IsRegular() {
		return Record{}, false, errors.New("sessionmeta: the record is not a regular file")
	}
	if pre.Size() > maxRecord {
		return Record{}, false, errors.New("sessionmeta: the record exceeds the size bound")
	}
	return readRecordHandle(sessRoot, pre, session)
}

// readRecordHandle opens by handle after the caller's identity check, so a
// replacement between the check and the open is refused on the opened
// handle, never by a second path validation.
func readRecordHandle(sessRoot *os.Root, pre os.FileInfo, session string) (Record, bool, error) {
	f, err := sessRoot.OpenFile(RecordName, platform.ReadOnlyNonBlocking, 0)
	if err != nil {
		return Record{}, false, fmt.Errorf("sessionmeta: the record cannot be opened: %v", err)
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return Record{}, false, fmt.Errorf("sessionmeta: the record handle cannot be checked: %v", err)
	}
	if !opened.Mode().IsRegular() {
		return Record{}, false, errors.New("sessionmeta: the opened record is not a regular file")
	}
	if !os.SameFile(pre, opened) {
		return Record{}, false, ErrRecordChanged
	}
	data, err := io.ReadAll(io.LimitReader(f, maxRecord+1))
	if err != nil {
		return Record{}, false, fmt.Errorf("sessionmeta: the record cannot be read: %v", err)
	}
	if int64(len(data)) > maxRecord {
		return Record{}, false, errors.New("sessionmeta: the record exceeds the size bound")
	}
	rec, err := decodeRecord(data, session)
	if err != nil {
		return Record{}, false, err
	}
	return rec, true, nil
}

// decodeRecord parses and validates one record: the bounded raw bytes must
// be valid UTF-8 before decoding (a decoder would replace malformed bytes),
// strict fields only, all fields present, no trailing data, and the identity
// rules with the store's session.
func decodeRecord(data []byte, session string) (Record, error) {
	if !utf8.Valid(data) {
		return Record{}, errors.New("sessionmeta: the record is not valid UTF-8")
	}
	var wire struct {
		Schema      *int    `json:"schema"`
		ProjectRoot *string `json:"project_root"`
		SessionID   *string `json:"session_id"`
		Terminal    *string `json:"terminal"`
		Name        *string `json:"name"`
		Kind        *string `json:"kind"`
		StartedMS   *int64  `json:"started_ms"`
		Task        *string `json:"task"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&wire); err != nil {
		return Record{}, fmt.Errorf("sessionmeta: the record is not valid JSON: %v", err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return Record{}, errors.New("sessionmeta: the record holds trailing data")
	}
	if wire.Schema == nil || wire.ProjectRoot == nil || wire.SessionID == nil || wire.Terminal == nil ||
		wire.Name == nil || wire.Kind == nil || wire.StartedMS == nil || wire.Task == nil {
		return Record{}, errors.New("sessionmeta: the record is missing a field")
	}
	rec := Record{
		Schema:      *wire.Schema,
		ProjectRoot: *wire.ProjectRoot,
		SessionID:   *wire.SessionID,
		Terminal:    *wire.Terminal,
		Name:        *wire.Name,
		Kind:        *wire.Kind,
		StartedMS:   *wire.StartedMS,
		Task:        *wire.Task,
	}
	if rec.SessionID != session {
		return Record{}, errors.New("sessionmeta: the record session does not match the store")
	}
	if err := validateRecord(rec); err != nil {
		return Record{}, err
	}
	return rec, nil
}

func marshalRecord(rec Record) ([]byte, error) {
	data, err := json.Marshal(rec)
	if err != nil {
		return nil, fmt.Errorf("sessionmeta: the record cannot be encoded: %v", err)
	}
	if len(data) > maxRecord {
		return nil, errors.New("sessionmeta: the record exceeds the size bound")
	}
	return data, nil
}

// acquireLock creates the own handle-relative lock directory. It is
// bounded, never deletes a lock it does not own, and hands back the
// identity to retain on release.
func acquireLock(sessRoot *os.Root) (os.FileInfo, error) {
	deadline := time.Now().Add(lockWait)
	for {
		if err := sessRoot.Mkdir(lockName, 0o700); err == nil {
			info, err := sessRoot.Lstat(lockName)
			if err != nil || !info.IsDir() {
				return nil, errors.New("sessionmeta: the lock identity changed")
			}
			return info, nil
		} else if !os.IsExist(err) {
			return nil, fmt.Errorf("sessionmeta: the lock cannot be created: %v", err)
		}
		if time.Now().Add(lockPoll).After(deadline) {
			return nil, ErrLocked
		}
		time.Sleep(lockPoll)
	}
}

// releaseLock removes only the lock this writer created, by retained
// identity. A missing, changed or unowned entry is left exactly as found
// and reports nil; a genuine failure to remove the retained own lock is an
// explicit error, without claiming any published record rolled back.
func releaseLock(sessRoot *os.Root, saved os.FileInfo) error {
	info, err := sessRoot.Lstat(lockName)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("sessionmeta: the own lock identity cannot be checked: %w", err)
	}
	if !info.IsDir() || !os.SameFile(info, saved) {
		return nil
	}
	if err := sessRoot.Remove(lockName); err != nil {
		return fmt.Errorf("sessionmeta: the own lock cannot be released: %v", err)
	}
	return nil
}

// withLockedRecord serializes one writer transaction under the own lock:
// the transaction runs with the retained lock identity, then the lock is
// released; a genuine release failure is reported alongside the
// transaction's result, dropping neither.
func withLockedRecord(sessRoot *os.Root, fn func() error) error {
	saved, err := acquireLock(sessRoot)
	if err != nil {
		return err
	}
	primary := fn()
	release := releaseLock(sessRoot, saved)
	switch {
	case primary != nil && release != nil:
		return errors.Join(primary, release)
	case primary != nil:
		return primary
	default:
		return release
	}
}

// publishRecord writes an exclusive random temp regular file at 0600,
// checks every step, closes it, and atomically replaces the record through
// the confined root. The replacement file keeps 0600 even when an older
// record had broader permissions. On any failure the own temp is removed
// with retained identity and the existing record stays unchanged.
func publishRecord(sessRoot *os.Root, data []byte) error {
	if len(data) > maxRecord {
		return errors.New("sessionmeta: the record exceeds the size bound")
	}
	tmp, err := tempName()
	if err != nil {
		return err
	}
	f, err := sessRoot.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("sessionmeta: the record cannot be prepared: %v", err)
	}
	opened, oerr := f.Stat()
	_, werr := f.Write(data)
	serr := f.Sync()
	cerr := f.Close()
	if oerr != nil || werr != nil || serr != nil || cerr != nil {
		cleanup := removeOwn(sessRoot, tmp, opened)
		var primary error
		switch {
		case oerr != nil:
			primary = fmt.Errorf("sessionmeta: the record identity cannot be retained: %v", oerr)
		case werr != nil:
			primary = fmt.Errorf("sessionmeta: the record cannot be written: %v", werr)
		case serr != nil:
			primary = fmt.Errorf("sessionmeta: the record cannot be synced: %v", serr)
		default:
			primary = fmt.Errorf("sessionmeta: the record cannot be closed: %v", cerr)
		}
		return joinPublish(primary, cleanup)
	}
	if err := sessRoot.Rename(tmp, RecordName); err != nil {
		cleanup := removeOwn(sessRoot, tmp, opened)
		return joinPublish(fmt.Errorf("sessionmeta: the record cannot be published: %v", err), cleanup)
	}
	return nil
}

// joinPublish joins the primary publish failure with a genuine own-temp
// cleanup failure, dropping neither; a nil cleanup is the primary alone.
func joinPublish(primary, cleanup error) error {
	if cleanup != nil {
		return errors.Join(primary, cleanup)
	}
	return primary
}

// removeOwn removes only the temp this writer created, by the retained
// identity of the opened temp handle: the current entry must be a regular
// file with the same identity, otherwise it is left exactly as found and
// nil is reported. A genuine failure to remove the retained own temp is an
// explicit error; with no retained identity nothing may be removed.
func removeOwn(sessRoot *os.Root, name string, opened os.FileInfo) error {
	if opened == nil {
		return nil
	}
	info, err := sessRoot.Lstat(name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("sessionmeta: the own temp identity cannot be checked: %w", err)
	}
	if !info.Mode().IsRegular() || !os.SameFile(opened, info) {
		return nil
	}
	if err := sessRoot.Remove(name); err != nil {
		return fmt.Errorf("sessionmeta: the own temp cannot be removed: %v", err)
	}
	return nil
}

func tempName() (string, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("sessionmeta: the record temp name cannot be randomized: %v", err)
	}
	return fmt.Sprintf(".%s-%d-%s.tmp", RecordName, os.Getpid(), hex.EncodeToString(buf[:])), nil
}
