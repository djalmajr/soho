package update

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const (
	// CacheFileName is the passive check's cache inside the state dir.
	CacheFileName = "update-check.json"
	// CacheTTL is how long a cached check stands: at most one network check
	// per 24 h.
	CacheTTL = 24 * time.Hour
)

// Cache is the cached outcome of the passive check, keeping the last
// successful result apart from the last attempt so a failed refresh cannot
// announce the tag it preserved: AttemptedAt and AttemptOK describe the latest
// attempt for the channel whatever its outcome, and CheckedOKAt and Newest the
// last completed check. An entry is also written before the request, to claim
// the attempt. Current and Channel are recorded so a cache written by another
// channel is re-checked instead of announced. An entry written by round 2
// (checked_at, current, channel, newest) is read as one successful attempt at
// checked_at.
type Cache struct {
	AttemptedAt time.Time `json:"attempted_at"`
	AttemptOK   bool      `json:"attempt_ok"`
	CheckedOKAt time.Time `json:"checked_ok_at"`
	Current     string    `json:"current"`
	Channel     string    `json:"channel"`
	Newest      string    `json:"newest"`
}

// cacheDocument is the on-disk shape: Cache plus the round-2 timestamp, which
// is only read, never written.
type cacheDocument struct {
	AttemptedAt time.Time `json:"attempted_at"`
	AttemptOK   bool      `json:"attempt_ok"`
	CheckedOKAt time.Time `json:"checked_ok_at"`
	Current     string    `json:"current"`
	Channel     string    `json:"channel"`
	Newest      string    `json:"newest"`
	CheckedAt   time.Time `json:"checked_at"`
}

// cache folds the document into one entry, reading a round-2 cache as one
// successful attempt at its single checked_at.
func (d cacheDocument) cache() Cache {
	entry := Cache{
		AttemptedAt: d.AttemptedAt,
		AttemptOK:   d.AttemptOK,
		CheckedOKAt: d.CheckedOKAt,
		Current:     d.Current,
		Channel:     d.Channel,
		Newest:      d.Newest,
	}
	if entry.AttemptedAt.IsZero() && !d.CheckedAt.IsZero() {
		entry.AttemptedAt = d.CheckedAt
		entry.CheckedOKAt = d.CheckedAt
		entry.AttemptOK = true
	}
	return entry
}

// Fresh reports whether the entry's latest attempt still stands for channel at
// now: at most one network check per CacheTTL whatever its outcome. A claim
// written before a request and a failed check both stand, so an outage costs
// one request per TTL; another channel and a future timestamp are stale.
func (c Cache) Fresh(channel string, now time.Time) bool {
	if c.Channel != channel || c.AttemptedAt.IsZero() {
		return false
	}
	age := now.Sub(c.AttemptedAt)
	return age >= 0 && age < CacheTTL
}

// announce reports the tag this entry can announce for current: only the last
// successful check, and only when its tag is newer. A fresh pending or failed
// attempt prints nothing.
func (c Cache) announce(current string) string {
	if !c.AttemptOK {
		return ""
	}
	if Newer(c.Newest, current) {
		return c.Newest
	}
	return ""
}

// ReadCache reads the cached check. A missing file, an unreadable one and one
// that does not parse all read as "no cache": the notice re-checks silently.
func ReadCache(stateDir string) (Cache, bool) {
	if stateDir == "" {
		return Cache{}, false
	}
	data, err := os.ReadFile(filepath.Join(stateDir, CacheFileName))
	if err != nil {
		return Cache{}, false
	}
	var doc cacheDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return Cache{}, false
	}
	return doc.cache(), true
}

// WriteCache writes the entry atomically in stateDir: a temp file in the same
// directory, then one rename, so a reader never sees a half-written file. It
// never creates the directory: the passive notice calls this only after
// core.StateDirForRead admitted an already existing state dir, and a missing
// one is an error.
func WriteCache(stateDir string, entry Cache) error {
	return writeCache(stateDir, nil, entry)
}

// writeCache writes entry inside an already existing stateDir and nowhere
// else. The directory is never created, and when admitted is not nil it must
// still be the same directory (os.SameFile): one renamed or replaced during a
// check never receives the entry, so the passive notice cannot recreate state
// that a cleanup or a move removed. The temp file is opened with
// O_CREATE|O_EXCL in that directory (os.CreateTemp) and renamed over
// update-check.json, so a reader never sees a half-written file; a failed
// rename removes the temp file.
func writeCache(stateDir string, admitted os.FileInfo, entry Cache) error {
	if stateDir == "" {
		return fmt.Errorf("update: no state dir for the update cache")
	}
	current, err := os.Stat(stateDir)
	if err != nil {
		return fmt.Errorf("update: state dir unavailable: %w", err)
	}
	if !current.IsDir() {
		return fmt.Errorf("update: state dir is not a directory")
	}
	if admitted != nil && !os.SameFile(admitted, current) {
		return fmt.Errorf("update: state dir is no longer the admitted one")
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("update: encoding the update cache: %w", err)
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(stateDir, ".update-check-*.json")
	if err != nil {
		return fmt.Errorf("update: creating the update cache: %w", err)
	}
	path := tmp.Name()
	_, writeErr := tmp.Write(data)
	closeErr := tmp.Close()
	if writeErr == nil {
		writeErr = closeErr
	}
	if writeErr == nil {
		writeErr = os.Rename(path, filepath.Join(stateDir, CacheFileName))
	}
	if writeErr != nil {
		_ = os.Remove(path)
		return fmt.Errorf("update: writing the update cache: %w", writeErr)
	}
	return nil
}

// NoticeOptions configures the passive check of doctor and init.
type NoticeOptions struct {
	Current  string
	Channel  string
	StateDir string
	API      string
	Disabled bool
	HTTP     *http.Client
	Now      func() time.Time
}

// Notice returns the tag to announce, or "" when there is nothing to say. A
// fresh cache is answered without the network; only the last successful check
// of the latest attempt is announced. Otherwise the attempt is claimed in the
// cache and one bounded check runs. The claim is written only into the state
// dir admitted before it, and an attempt that cannot be recorded makes no
// request: the budget is never spent without being counted. A failed check
// counts as the attempt (it keeps the previous newest and stays silent), so a
// second doctor or init inside the TTL makes no request whether the last
// attempt succeeded or not. A disabled notice or a missing state dir returns
// "" with no request.
func Notice(ctx context.Context, opts NoticeOptions) (string, error) {
	if opts.Disabled || opts.StateDir == "" {
		return "", nil
	}
	channel, err := ParseChannel(opts.Channel)
	if err != nil {
		return "", err
	}
	if channel == "" {
		channel = ChannelFor(opts.Current)
	}
	now := time.Now()
	if opts.Now != nil {
		now = opts.Now()
	}
	// The passive write never creates state: the admitted directory must
	// already exist, and this identity is what every write of this call
	// compares against.
	admitted, err := os.Stat(opts.StateDir)
	if err != nil || !admitted.IsDir() {
		return "", nil
	}
	entry, cached := ReadCache(opts.StateDir)
	if cached && entry.Fresh(channel, now) {
		return entry.announce(opts.Current), nil
	}
	previous := Cache{}
	if cached && entry.Channel == channel {
		previous = entry
	}
	// A claim stamped before the recorded attempt would move the budget
	// back; the caller that recorded the later attempt already owns it. The
	// skip is bounded by the TTL: a stamp further ahead than that comes from a
	// skewed clock or a tampered file, and is checked again instead of
	// silencing the notice until the wall clock catches up.
	if previous.AttemptedAt.After(now) && previous.AttemptedAt.Sub(now) < CacheTTL {
		return "", nil
	}
	// Claim the attempt before the request: a run that fails, or one that is
	// killed mid-request, still spends the budget. The previous newest is
	// kept, and marked as not the latest attempt, so it is not announced.
	claim := Cache{
		AttemptedAt: now,
		AttemptOK:   false,
		CheckedOKAt: previous.CheckedOKAt,
		Current:     opts.Current,
		Channel:     channel,
		Newest:      previous.Newest,
	}
	if err := writeCache(opts.StateDir, admitted, claim); err != nil {
		return "", nil
	}
	// Another caller may have recorded a later attempt between the claim and
	// the request: its budget is the one that counts.
	if current, ok := ReadCache(opts.StateDir); ok && current.AttemptedAt.After(now) {
		return "", nil
	}
	client := opts.HTTP
	if client == nil {
		client = &http.Client{Timeout: NoticeTimeout}
	}
	check, err := CheckChannel(ctx, CheckOptions{Current: opts.Current, Channel: channel, API: opts.API, HTTP: client})
	if err != nil {
		return "", err
	}
	// The result is best effort too: the check is already counted, and a
	// state dir that moved during the request keeps nothing.
	_ = writeCache(opts.StateDir, admitted, Cache{
		AttemptedAt: now,
		AttemptOK:   true,
		CheckedOKAt: now,
		Current:     opts.Current,
		Channel:     channel,
		Newest:      check.Newest.Tag,
	})
	if !check.UpdateAvailable {
		return "", nil
	}
	return check.Newest.Tag, nil
}
