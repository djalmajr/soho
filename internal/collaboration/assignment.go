package collaboration

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

const (
	Preparing            = "preparing"
	Reviewing            = "reviewing"
	Fixing               = "fixing"
	AwaitingOrchestrator = "awaiting-orchestrator"
	Escalated            = "escalated"
	Finished             = "finished"
)

var assignmentID = regexp.MustCompile(`^c-[a-f0-9]{16}$`)

type Participant struct {
	Name    string `json:"name"`
	Pane    string `json:"pane"`
	Role    string `json:"role"`
	Lane    string `json:"lane"`
	Kind    string `json:"kind"`
	Model   string `json:"model"`
	Family  string `json:"family"`
	Started string `json:"started"`
	Session string `json:"session"`
	Cwd     string `json:"cwd"`
}

type Assignment struct {
	Version    int         `json:"version"`
	ID         string      `json:"id"`
	Generation uint64      `json:"generation"`
	Phase      string      `json:"phase"`
	Round      int         `json:"round"`
	MaxRounds  int         `json:"max_rounds"`
	Author     Participant `json:"author"`
	Reviewer   Participant `json:"reviewer"`
	Workspace  string      `json:"workspace"`
	Root       string      `json:"root"`
	Brief      string      `json:"brief"`
	BriefHash  string      `json:"brief_hash"`
	Paths      []string    `json:"paths"`
	Revision   string      `json:"revision"`
	Snapshot   string      `json:"snapshot"`
	Events     []Event     `json:"events"`
	Verdict    string      `json:"verdict,omitempty"`
}

type Event struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Actor      string `json:"actor"`
	Report     string `json:"report"`
	ReportHash string `json:"report_hash"`
	Revision   string `json:"revision"`
	Round      int    `json:"round"`
	Delivery   string `json:"delivery"`
	Cause      string `json:"cause,omitempty"`
}

func (a Assignment) Active() bool { return a.Phase != Finished }

func (a Assignment) Member(pane string) (Participant, bool) {
	if a.Author.Pane == pane {
		return a.Author, true
	}
	if a.Reviewer.Pane == pane {
		return a.Reviewer, true
	}
	return Participant{}, false
}

type Store struct{ StateDir string }

func (s Store) directory() string { return filepath.Join(s.StateDir, "collaboration") }

func (s Store) Path(id string) (string, error) {
	if !assignmentID.MatchString(id) {
		return "", errors.New("invalid collaboration ID")
	}
	return filepath.Join(s.directory(), id, "assignment.json"), nil
}

func (s Store) Read(id string) (Assignment, error) {
	file, err := s.Path(id)
	if err != nil {
		return Assignment{}, err
	}
	// A regular file only, opened without blocking: a FIFO planted at the
	// record's path is refused instead of holding the reader.
	raw, err := platform.ReadRegularFile(file)
	if err != nil {
		return Assignment{}, err
	}
	return ParseAssignment(raw, id)
}

// ParseAssignment is the shared record parse and validation that
// Store.Read and the read-only inspection apply to the record bytes: the
// same JSON parse, the same full validation and the same id agreement
// check; an unreadable, invalid or mismatched record is an error with the
// parsed fields returned. It opens no path.
func ParseAssignment(raw []byte, id string) (Assignment, error) {
	var a Assignment
	if err := json.Unmarshal(raw, &a); err != nil {
		return a, fmt.Errorf("unreadable collaboration %s: %w", id, err)
	}
	if err := validate(a); err != nil {
		return a, err
	}
	if a.ID != id {
		return a, errors.New("collaboration ID does not match its record")
	}
	return a, nil
}

func validate(a Assignment) error {
	if a.Version != 1 || !assignmentID.MatchString(a.ID) || a.Generation == 0 || a.MaxRounds < 1 || a.MaxRounds > 100 || a.Round < 0 || a.Round > a.MaxRounds || a.Workspace == "" || a.Root == "" || a.BriefHash == "" || len(a.Paths) == 0 {
		return errors.New("invalid collaboration record")
	}
	switch a.Phase {
	case Preparing, Reviewing, Fixing, AwaitingOrchestrator, Escalated, Finished:
	default:
		return errors.New("invalid collaboration phase")
	}
	for _, p := range []Participant{a.Author, a.Reviewer} {
		if p.Name == "" || p.Pane == "" || p.Started == "" || p.Session == "" || !filepath.IsAbs(p.Cwd) {
			return errors.New("collaboration participant identity is incomplete")
		}
	}
	if a.Author.Pane == a.Reviewer.Pane || a.Author.Name == a.Reviewer.Name {
		return errors.New("collaboration requires different participants")
	}
	return nil
}

// UnreadableAssignment is one collaboration record that ListActive could
// not read or validate: the id and the read or validation cause.
type UnreadableAssignment struct {
	ID    string
	Cause string
}

// claimReadLimit bounds the raw claim read of one record: the ownership
// proof never reads more than 1 MiB of the record.
const claimReadLimit = 1 << 20

// ReadClaim is the bounded raw ownership claim of one collaboration
// record: it reads at most claimReadLimit bytes of the record's
// assignment.json and unmarshals only the workspace and root fields, so a
// record the full read refuses (a hostile shape, an invalid field, a torn
// write) still proves its ownership when its claim stands. It never locks
// and never writes, and it never validates the rest of the record: an
// invalid id, a missing record, a record that is not a regular file, an
// overlong record or a non-JSON record is an error with the fields left
// empty.
func (s Store) ReadClaim(id string) (workspace, root string, err error) {
	file, err := s.Path(id)
	if err != nil {
		return "", "", err
	}
	f, err := platform.OpenRegularFile(file)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, claimReadLimit))
	if err != nil {
		return "", "", err
	}
	var claim struct {
		Workspace string `json:"workspace"`
		Root      string `json:"root"`
	}
	if err := json.Unmarshal(raw, &claim); err != nil {
		return "", "", fmt.Errorf("unreadable collaboration %s claim: %w", id, err)
	}
	return claim.Workspace, claim.Root, nil
}

// ListActive returns every active assignment in the store directory, with
// the unreadable records: a per-record read or validation failure comes
// back in unreadable, not as an error; a directory-level read error is err
// (a path in the directory's place that is not a directory included); an
// absent directory is (nil, nil, nil). It never locks and never creates
// the directory, and it ignores entries that are not collaboration ids.
func (s Store) ListActive() (active []Assignment, unreadable []UnreadableAssignment, err error) {
	entries, err := os.ReadDir(s.directory())
	if errors.Is(err, os.ErrNotExist) {
		// Only a path that is really absent is "no collaborations": some
		// systems (Windows) report a regular file in the directory's place
		// as not found, and that is a directory-level failure, never an
		// empty store.
		if _, lerr := os.Lstat(s.directory()); lerr == nil {
			return nil, nil, errors.New("collaboration directory is not a directory")
		}
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !assignmentID.MatchString(entry.Name()) {
			continue
		}
		a, err := s.Read(entry.Name())
		if err != nil {
			unreadable = append(unreadable, UnreadableAssignment{ID: entry.Name(), Cause: err.Error()})
			continue
		}
		if a.Active() {
			active = append(active, a)
		}
	}
	return active, unreadable, nil
}

// activeSourceRecordMax and activeSourceTotalMax are the D7 inspection
// caps of the collaboration read: one assignment record's bytes and the
// aggregate assignment bytes of one inspection.
const (
	activeSourceRecordMax = 1 << 20
	activeSourceTotalMax  = 4 << 20
)

// ActiveForSource is the read-only inspection twin of ActiveFor: the
// same walk and the same shared record parse, over the source's bounded
// confined enumeration and capped record reads — the state dir is never
// reopened by raw path, a record the cap refuses or an exhausted
// directory is an error (never an empty success), and an absent
// collaboration dir is no active collaboration.
func ActiveForSource(src *core.SessionSource, pane string) (*Assignment, error) {
	names, err := src.ReadDir("collaboration")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var found *Assignment
	var total int64
	for _, id := range names {
		if !assignmentID.MatchString(id) {
			continue
		}
		raw, err := src.ReadFile(activeSourceRecordMax, "collaboration", id, "assignment.json")
		if err != nil {
			return nil, err
		}
		total += int64(len(raw))
		if total > activeSourceTotalMax {
			return nil, errors.New("the collaboration records exceed the inspection cap")
		}
		a, err := ParseAssignment([]byte(raw), id)
		if err != nil {
			return nil, err
		}
		if _, member := a.Member(pane); member && a.Active() {
			if found != nil {
				return nil, errors.New("participant has multiple active collaborations")
			}
			found = &a
		}
	}
	return found, nil
}

func (s Store) ActiveFor(pane string) (*Assignment, error) {
	entries, err := os.ReadDir(s.directory())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var found *Assignment
	for _, entry := range entries {
		if !entry.IsDir() || !assignmentID.MatchString(entry.Name()) {
			continue
		}
		a, err := s.Read(entry.Name())
		if err != nil {
			return nil, err
		}
		if _, member := a.Member(pane); member && a.Active() {
			if found != nil {
				return nil, errors.New("participant has multiple active collaborations")
			}
			found = &a
		}
	}
	return found, nil
}

func (s Store) Create(a Assignment) (Assignment, error) {
	unlock, err := s.lock()
	if err != nil {
		return a, err
	}
	defer unlock()
	for _, p := range []Participant{a.Author, a.Reviewer} {
		active, err := s.ActiveFor(p.Pane)
		if err != nil {
			return a, err
		}
		if active != nil {
			return a, fmt.Errorf("%s already has active collaboration %s", p.Name, active.ID)
		}
	}
	bytes := make([]byte, 8)
	if _, err := rand.Read(bytes); err != nil {
		return a, err
	}
	a.ID, a.Version, a.Generation, a.Phase = "c-"+hex.EncodeToString(bytes), 1, 1, Preparing
	if a.MaxRounds == 0 {
		a.MaxRounds = 3
	}
	if err := validate(a); err != nil {
		return a, err
	}
	file, _ := s.Path(a.ID)
	if err := os.Mkdir(filepath.Dir(file), 0o700); err != nil {
		return a, err
	}
	return a, s.write(a)
}

// Update compares the generation under a short lock. The callback must only
// change in-memory state; subprocesses and delivery run outside this lock.
func (s Store) Update(id string, generation uint64, change func(*Assignment) error) (Assignment, error) {
	unlock, err := s.lock()
	if err != nil {
		return Assignment{}, err
	}
	defer unlock()
	a, err := s.Read(id)
	if err != nil {
		return a, err
	}
	if a.Generation != generation {
		return a, errors.New("collaboration generation changed; inspect status before retrying")
	}
	if err := change(&a); err != nil {
		return a, err
	}
	a.Generation++
	if err := validate(a); err != nil {
		return a, err
	}
	return a, s.write(a)
}

func (s Store) write(a Assignment) error {
	file, err := s.Path(a.ID)
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	return platform.AtomicWrite(file, string(raw)+"\n")
}

func (s Store) lock() (func(), error) {
	if err := os.MkdirAll(s.directory(), 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(s.directory(), "state.lock")
	deadline := time.Now().Add(time.Second)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, writeErr := fmt.Fprintf(f, "%d\n", os.Getpid())
			closeErr := f.Close()
			if writeErr != nil || closeErr != nil {
				_ = os.Remove(path)
				return nil, errors.Join(writeErr, closeErr)
			}
			return func() { _ = os.Remove(path) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("collaboration lock held: %s; inspect its owner before explicit recovery", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func SameParticipant(expected, live Participant) bool { return expected == live }

func ValidReport(path string) (string, error) {
	if path == "" || !filepath.IsAbs(path) {
		return "", errors.New("report must be an absolute path")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return "", errors.New("report is empty")
	}
	return Hash(raw), nil
}
