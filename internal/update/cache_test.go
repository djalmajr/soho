package update

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCacheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	attempted := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	checked := attempted.Add(-time.Hour)
	want := Cache{AttemptedAt: attempted, AttemptOK: true, CheckedOKAt: checked, Current: "v0.3.1-rc.18", Channel: ChannelRC, Newest: "v0.3.1-rc.19"}
	if err := WriteCache(dir, want); err != nil {
		t.Fatalf("WriteCache: %v", err)
	}
	got, ok := ReadCache(dir)
	if !ok {
		t.Fatal("ReadCache found no entry after WriteCache")
	}
	if !got.AttemptedAt.Equal(want.AttemptedAt) || got.AttemptOK != want.AttemptOK || !got.CheckedOKAt.Equal(want.CheckedOKAt) || got.Current != want.Current || got.Channel != want.Channel || got.Newest != want.Newest {
		t.Fatalf("ReadCache = %+v, want %+v", got, want)
	}
	// The document is the documented object, not an encoding surprise, and
	// the round-2 timestamp is not written any more.
	raw, err := os.ReadFile(filepath.Join(dir, CacheFileName))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("cache is not JSON: %v", err)
	}
	for _, key := range []string{"attempted_at", "attempt_ok", "checked_ok_at", "current", "channel", "newest"} {
		if _, ok := doc[key]; !ok {
			t.Fatalf("cache document misses %q: %s", key, raw)
		}
	}
	if _, ok := doc["checked_at"]; ok {
		t.Fatalf("cache document still writes round 2's checked_at: %s", raw)
	}
	// Nothing else is left behind: the temp file was renamed.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != CacheFileName {
		t.Fatalf("state dir entries = %v, want only %s", entries, CacheFileName)
	}
}

func TestReadCacheRefusesWhatItCannotUse(t *testing.T) {
	dir := t.TempDir()
	if _, ok := ReadCache(dir); ok {
		t.Fatal("ReadCache found an entry in an empty dir")
	}
	if _, ok := ReadCache(""); ok {
		t.Fatal("ReadCache found an entry without a state dir")
	}
	path := filepath.Join(dir, CacheFileName)
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadCache(dir); ok {
		t.Fatal("ReadCache accepted a malformed file")
	}
	// A document with one field of the wrong type is rejected whole, not
	// read as an attempt with a zero timestamp.
	if err := os.WriteFile(path, []byte(`{"attempted_at":123,"channel":"rc"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if entry, ok := ReadCache(dir); ok {
		t.Fatalf("ReadCache accepted a mistyped document as %+v", entry)
	}
	// A round-2 document (one checked_at, no attempt fields) reads as one
	// successful attempt at checked_at.
	if err := os.WriteFile(path, []byte(`{"checked_at":"2026-11-20T10:00:00Z","channel":"rc"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	entry, ok := ReadCache(dir)
	stamped := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	if !ok || !entry.AttemptOK || entry.Newest != "" || !entry.AttemptedAt.Equal(stamped) || !entry.CheckedOKAt.Equal(stamped) {
		t.Fatalf("a round-2 entry = %+v, %t; want one successful attempt at checked_at", entry, ok)
	}
	// It stands inside the TTL, so the next doctor makes no request.
	if !entry.Fresh(ChannelRC, stamped.Add(30*time.Minute)) {
		t.Fatal("a round-2 attempt inside the TTL is stale")
	}
	if entry.Fresh(ChannelRC, stamped.Add(-time.Hour)) {
		t.Fatal("an entry stamped in the future is fresh")
	}
}

func TestCacheFreshHonorsTheTTL(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	entry := Cache{AttemptedAt: now.Add(-time.Hour), AttemptOK: true, Current: "v0.3.1-rc.18", Channel: ChannelRC, Newest: "v0.3.1-rc.19"}
	if !entry.Fresh(ChannelRC, now) {
		t.Fatal("an entry one hour old is stale")
	}
	// Just inside the TTL is still fresh; at the TTL it is stale.
	inside := Cache{AttemptedAt: now.Add(-CacheTTL + time.Minute), AttemptOK: true, Channel: ChannelRC, Newest: "v0.3.1-rc.19"}
	if !inside.Fresh(ChannelRC, now) {
		t.Fatal("an entry inside the 24 h TTL is stale")
	}
	atBoundary := Cache{AttemptedAt: now.Add(-CacheTTL), AttemptOK: true, Channel: ChannelRC, Newest: "v0.3.1-rc.19"}
	if atBoundary.Fresh(ChannelRC, now) {
		t.Fatal("an entry at the 24 h TTL is still fresh")
	}
	// Another channel, a future timestamp and no attempt are stale.
	if entry.Fresh(ChannelStable, now) {
		t.Fatal("an rc entry is fresh for the stable channel")
	}
	if future := (Cache{AttemptedAt: now.Add(time.Hour), AttemptOK: true, Channel: ChannelRC, Newest: "v0.3.1-rc.19"}); future.Fresh(ChannelRC, now) {
		t.Fatal("an entry stamped in the future is fresh")
	}
	if empty := (Cache{AttemptOK: true, Channel: ChannelRC, Newest: "v0.3.1-rc.19"}); empty.Fresh(ChannelRC, now) {
		t.Fatal("an entry with no attempt timestamp is fresh")
	}
	// A pending attempt (no result yet) is still the attempt of the last
	// 24 h, and it announces nothing.
	claim := Cache{AttemptedAt: now, Channel: ChannelRC, Newest: "v0.3.1-rc.19"}
	if !claim.Fresh(ChannelRC, now) {
		t.Fatal("a claim inside the TTL is stale")
	}
	if tag := claim.announce("v0.3.1-rc.18"); tag != "" {
		t.Fatalf("a pending attempt announced %q", tag)
	}
	if tag := entry.announce("v0.3.1-rc.18"); tag != "v0.3.1-rc.19" {
		t.Fatalf("a successful attempt announced %q, want v0.3.1-rc.19", tag)
	}
}

// noticeFixture serves one release list and counts requests.
func noticeFixture(t *testing.T, body string) (string, *int32) {
	t.Helper()
	srv, reqs := releasesServer(t, body)
	return srv.URL, reqs
}

const rcListBody = `[{"tag_name":"v0.3.1-rc.18","prerelease":true,"published_at":"2026-11-18T10:00:00Z"},{"tag_name":"v0.3.1-rc.19","prerelease":true,"published_at":"2026-11-20T10:00:00Z"}]`

func TestNoticeCachesOneCheckPer24h(t *testing.T) {
	api, reqs := noticeFixture(t, rcListBody)
	stateDir := t.TempDir()
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	opts := NoticeOptions{Current: "v0.3.1-rc.18", StateDir: stateDir, API: api, Now: clock}

	tag, err := Notice(context.Background(), opts)
	if err != nil || tag != "v0.3.1-rc.19" {
		t.Fatalf("Notice = %q, %v; want v0.3.1-rc.19", tag, err)
	}
	if n := atomic.LoadInt32(reqs); n != 1 {
		t.Fatalf("requests = %d, want 1", n)
	}
	if cached, ok := ReadCache(stateDir); !ok || cached.Newest != "v0.3.1-rc.19" || cached.Channel != ChannelRC || !cached.AttemptOK || !cached.AttemptedAt.Equal(now) || !cached.CheckedOKAt.Equal(now) {
		t.Fatalf("cache = %+v, %t", cached, ok)
	}

	// Within 24 h the cache answers: no second request, same line.
	now = now.Add(23 * time.Hour)
	tag, err = Notice(context.Background(), opts)
	if err != nil || tag != "v0.3.1-rc.19" {
		t.Fatalf("second Notice = %q, %v", tag, err)
	}
	if n := atomic.LoadInt32(reqs); n != 1 {
		t.Fatalf("requests after the cached check = %d, want 1", n)
	}

	// Past 24 h it checks again and rewrites the cache.
	now = now.Add(2 * time.Hour)
	tag, err = Notice(context.Background(), opts)
	if err != nil || tag != "v0.3.1-rc.19" {
		t.Fatalf("third Notice = %q, %v", tag, err)
	}
	if n := atomic.LoadInt32(reqs); n != 2 {
		t.Fatalf("requests after the TTL = %d, want 2", n)
	}
	if cached, _ := ReadCache(stateDir); !cached.AttemptedAt.Equal(now) {
		t.Fatalf("cache attempted_at = %s, want the refresh at %s", cached.AttemptedAt, now)
	}
}

func TestNoticeSilence(t *testing.T) {
	t.Run("a stale channel cache is re-checked", func(t *testing.T) {
		api, reqs := noticeFixture(t, rcListBody)
		stateDir := t.TempDir()
		now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
		if err := WriteCache(stateDir, Cache{AttemptedAt: now.Add(-time.Hour), AttemptOK: true, Channel: ChannelStable, Newest: "v0.3.0"}); err != nil {
			t.Fatal(err)
		}
		tag, err := Notice(context.Background(), NoticeOptions{Current: "v0.3.1-rc.18", StateDir: stateDir, API: api, Now: func() time.Time { return now }})
		if err != nil || tag != "v0.3.1-rc.19" {
			t.Fatalf("Notice = %q, %v", tag, err)
		}
		if n := atomic.LoadInt32(reqs); n != 1 {
			t.Fatalf("requests = %d, want the stable cache to be re-checked", n)
		}
	})
	t.Run("a cached tag that is not newer is silent", func(t *testing.T) {
		api, reqs := noticeFixture(t, rcListBody)
		stateDir := t.TempDir()
		now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
		if err := WriteCache(stateDir, Cache{AttemptedAt: now.Add(-time.Hour), AttemptOK: true, Channel: ChannelRC, Newest: "v0.3.1-rc.17"}); err != nil {
			t.Fatal(err)
		}
		tag, err := Notice(context.Background(), NoticeOptions{Current: "v0.3.1-rc.18", StateDir: stateDir, API: api, Now: func() time.Time { return now }})
		if err != nil || tag != "" {
			t.Fatalf("Notice = %q, %v; want silence", tag, err)
		}
		if n := atomic.LoadInt32(reqs); n != 0 {
			t.Fatalf("requests = %d, want 0 for a fresh cache", n)
		}
	})
	t.Run("disabled, and without a state dir", func(t *testing.T) {
		api, reqs := noticeFixture(t, rcListBody)
		for _, opts := range []NoticeOptions{
			{Current: "v0.3.1-rc.18", StateDir: t.TempDir(), Disabled: true, API: api},
			{Current: "v0.3.1-rc.18", StateDir: "", API: api},
		} {
			if tag, err := Notice(context.Background(), opts); err != nil || tag != "" {
				t.Fatalf("Notice = %q, %v; want silence", tag, err)
			}
		}
		if n := atomic.LoadInt32(reqs); n != 0 {
			t.Fatalf("requests = %d, want 0", n)
		}
	})
	t.Run("a failed check is silent and counts as the attempt", func(t *testing.T) {
		stateDir := t.TempDir()
		now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
		opts := NoticeOptions{Current: "v0.3.1-rc.18", StateDir: stateDir, API: closedPortURL(t), Now: func() time.Time { return now }}
		tag, err := Notice(context.Background(), opts)
		if err == nil || tag != "" {
			t.Fatalf("Notice = %q, %v; want silence and an error", tag, err)
		}
		cached, ok := ReadCache(stateDir)
		if !ok || !cached.AttemptedAt.Equal(now) || cached.AttemptOK || cached.Newest != "" || cached.Channel != ChannelRC {
			t.Fatalf("cache after the failure = %+v, %t; want the attempt claimed", cached, ok)
		}
		// The next run inside the TTL pays no network and stays silent,
		// even with a releases API that would answer.
		api, reqs := noticeFixture(t, rcListBody)
		opts.API = api
		if tag, err := Notice(context.Background(), opts); err != nil || tag != "" {
			t.Fatalf("second Notice = %q, %v; want silence", tag, err)
		}
		if n := atomic.LoadInt32(reqs); n != 0 {
			t.Fatalf("requests = %d, want 0 inside the TTL after a failed attempt", n)
		}
	})
	t.Run("a failed refresh keeps the newest but stays silent", func(t *testing.T) {
		api, _ := noticeFixture(t, rcListBody)
		stateDir := t.TempDir()
		first := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
		if tag, err := Notice(context.Background(), NoticeOptions{Current: "v0.3.1-rc.18", StateDir: stateDir, API: api, Now: func() time.Time { return first }}); err != nil || tag != "v0.3.1-rc.19" {
			t.Fatalf("first Notice = %q, %v", tag, err)
		}
		later := first.Add(25 * time.Hour)
		opts := NoticeOptions{Current: "v0.3.1-rc.18", StateDir: stateDir, API: closedPortURL(t), Now: func() time.Time { return later }}
		if tag, err := Notice(context.Background(), opts); err == nil || tag != "" {
			t.Fatalf("failing refresh Notice = %q, %v; want silence and an error", tag, err)
		}
		if cached, _ := ReadCache(stateDir); !cached.AttemptedAt.Equal(later) || cached.AttemptOK || !cached.CheckedOKAt.Equal(first) || cached.Newest != "v0.3.1-rc.19" {
			t.Fatalf("cache after the failing refresh = %+v; want the failed attempt and the previous success", cached)
		}
		// The failed attempt is the latest one: the next call inside the TTL
		// spends the budget and announces nothing, even though the older
		// successful result is preserved. This is round-3 F3.
		next := later.Add(time.Hour)
		opts.Now = func() time.Time { return next }
		if tag, err := Notice(context.Background(), opts); err != nil || tag != "" {
			t.Fatalf("Notice after the failed refresh = %q, %v; want silence", tag, err)
		}
	})
	t.Run("an up-to-date check is cached and silent", func(t *testing.T) {
		api, reqs := noticeFixture(t, rcListBody)
		stateDir := t.TempDir()
		now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
		opts := NoticeOptions{Current: "v0.3.1-rc.19", StateDir: stateDir, API: api, Now: func() time.Time { return now }}
		if tag, err := Notice(context.Background(), opts); err != nil || tag != "" {
			t.Fatalf("Notice = %q, %v; want silence", tag, err)
		}
		if n := atomic.LoadInt32(reqs); n != 1 {
			t.Fatalf("requests = %d, want 1", n)
		}
		// The cache answers the next call without the network.
		if tag, err := Notice(context.Background(), opts); err != nil || tag != "" {
			t.Fatalf("second Notice = %q, %v", tag, err)
		}
		if n := atomic.LoadInt32(reqs); n != 1 {
			t.Fatalf("requests = %d, want 1 (the cache answers)", n)
		}
		if cached, ok := ReadCache(stateDir); !ok || cached.Newest != "v0.3.1-rc.19" {
			t.Fatalf("cache = %+v, %t", cached, ok)
		}
	})
	t.Run("a dev build is told about the newest stable", func(t *testing.T) {
		api, _ := noticeFixture(t, `[{"tag_name":"v0.3.1-rc.19","prerelease":true},{"tag_name":"v0.3.0"}]`)
		tag, err := Notice(context.Background(), NoticeOptions{Current: DevVersion, StateDir: t.TempDir(), API: api})
		if err != nil || tag != "v0.3.0" {
			t.Fatalf("Notice = %q, %v; want v0.3.0", tag, err)
		}
	})
	t.Run("an unclaimable state dir makes no request", func(t *testing.T) {
		api, reqs := noticeFixture(t, rcListBody)
		blocker := filepath.Join(t.TempDir(), "state")
		if err := os.WriteFile(blocker, []byte("not a dir"), 0o600); err != nil {
			t.Fatal(err)
		}
		// The attempt cannot be recorded, so the notice spends no request
		// and stays silent (round-3 F2).
		tag, err := Notice(context.Background(), NoticeOptions{Current: "v0.3.1-rc.18", StateDir: blocker, API: api})
		if err != nil || tag != "" {
			t.Fatalf("Notice = %q, %v; want silence", tag, err)
		}
		if n := atomic.LoadInt32(reqs); n != 0 {
			t.Fatalf("requests = %d, want 0 without a claim", n)
		}
		if err := WriteCache(blocker, Cache{Channel: ChannelRC, Newest: "x"}); err == nil || !strings.Contains(err.Error(), "state dir") {
			t.Fatalf("WriteCache error = %v, want a state-dir failure", err)
		}
	})
	t.Run("a cache file that is a directory makes no request", func(t *testing.T) {
		api, reqs := noticeFixture(t, rcListBody)
		stateDir := t.TempDir()
		if err := os.Mkdir(filepath.Join(stateDir, CacheFileName), 0o700); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			if tag, err := Notice(context.Background(), NoticeOptions{Current: "v0.3.1-rc.18", StateDir: stateDir, API: api}); err != nil || tag != "" {
				t.Fatalf("run %d: Notice = %q, %v; want silence", i+1, tag, err)
			}
		}
		if n := atomic.LoadInt32(reqs); n != 0 {
			t.Fatalf("an unclaimable cache allowed %d requests", n)
		}
		// A rename that failed leaves no temp file behind.
		entries, err := os.ReadDir(stateDir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 || entries[0].Name() != CacheFileName {
			t.Fatalf("state dir entries = %v, want only %s", entries, CacheFileName)
		}
	})
}

// TestWriteCacheNeverCreatesTheDirectory is round-3 F1 at the writer: the
// passive write goes only into an already existing state dir.
func TestWriteCacheNeverCreatesTheDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "absent")
	entry := Cache{AttemptedAt: time.Now(), AttemptOK: true, Channel: ChannelRC, Newest: "v0.3.1-rc.19"}
	if err := WriteCache(dir, entry); err == nil || !strings.Contains(err.Error(), "state dir") {
		t.Fatalf("WriteCache error = %v, want a state-dir failure", err)
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatalf("WriteCache created %s: %v", dir, err)
	}
}

// round2Document is a cache as round 2 wrote it: one checked_at for the
// attempt and the result.
func round2Document(checkedAt time.Time, newest string) []byte {
	return []byte(`{"checked_at":"` + checkedAt.UTC().Format(time.RFC3339) + `","current":"v0.3.1-rc.18","channel":"rc","newest":"` + newest + `"}` + "\n")
}

// TestNoticeReadsARound2Cache is round-3 F3's compatibility case: a round-2
// entry is one successful attempt at checked_at, announced inside the TTL and
// re-checked after it, and the refresh rewrites it in the new shape.
func TestNoticeReadsARound2Cache(t *testing.T) {
	api, reqs := noticeFixture(t, rcListBody)
	stateDir := t.TempDir()
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	checkedAt := now.Add(-time.Hour)
	if err := os.WriteFile(filepath.Join(stateDir, CacheFileName), round2Document(checkedAt, "v0.3.1-rc.19"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := NoticeOptions{Current: "v0.3.1-rc.18", StateDir: stateDir, API: api, Now: func() time.Time { return now }}
	tag, err := Notice(context.Background(), opts)
	if err != nil || tag != "v0.3.1-rc.19" {
		t.Fatalf("Notice from a round-2 cache = %q, %v; want v0.3.1-rc.19", tag, err)
	}
	if n := atomic.LoadInt32(reqs); n != 0 {
		t.Fatalf("requests = %d, want 0 for a fresh round-2 cache", n)
	}
	// Past the TTL the round-2 entry is re-checked like any other, and the
	// rewritten cache carries the separated attempt and result.
	now = checkedAt.Add(CacheTTL + time.Minute)
	tag, err = Notice(context.Background(), opts)
	if err != nil || tag != "v0.3.1-rc.19" {
		t.Fatalf("second Notice = %q, %v", tag, err)
	}
	if n := atomic.LoadInt32(reqs); n != 1 {
		t.Fatalf("requests after the TTL = %d, want 1", n)
	}
	cached, ok := ReadCache(stateDir)
	if !ok || !cached.AttemptOK || !cached.AttemptedAt.Equal(now) || !cached.CheckedOKAt.Equal(now) {
		t.Fatalf("cache after the refresh = %+v, %t; want one successful new-shape attempt", cached, ok)
	}
}

// TestNoticeSkipsALaterAttempt is the review's overlapping-callers probe: the
// caller that sampled an earlier time must not spend a second request once a
// later caller has recorded the attempt. No cross-process lock is used; the
// later attempt owns the budget.
func TestNoticeSkipsALaterAttempt(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	stateDir := t.TempDir()
	sampled := make(chan struct{})
	resume := make(chan struct{})
	finished := make(chan string, 1)
	older := time.Now()
	go func() {
		tag, _ := Notice(context.Background(), NoticeOptions{Current: "v0.3.1-rc.18", StateDir: stateDir, API: srv.URL, Now: func() time.Time {
			close(sampled)
			<-resume
			return older
		}})
		finished <- tag
	}()
	<-sampled
	newer := older.Add(time.Millisecond)
	if tag, err := Notice(context.Background(), NoticeOptions{Current: "v0.3.1-rc.18", StateDir: stateDir, API: srv.URL, Now: func() time.Time { return newer }}); err == nil || tag != "" {
		t.Fatalf("later caller = %q, %v; want silence from the failed check", tag, err)
	}
	close(resume)
	if tag := <-finished; tag != "" {
		t.Fatalf("earlier caller announced %q", tag)
	}
	if n := requests.Load(); n != 1 {
		t.Fatalf("overlapping notices made %d requests, want 1", n)
	}
}

// TestNoticeRefusesAReplacedStateDir is the identity boundary of F1: a state
// dir renamed away and recreated during the request is no longer the admitted
// one, so the result is not written into the replacement.
func TestNoticeRefusesAReplacedStateDir(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	retired := filepath.Join(root, "retired")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if err := os.Rename(stateDir, retired); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if err := os.Mkdir(stateDir, 0o700); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(rcListBody))
	}))
	defer srv.Close()
	tag, err := Notice(context.Background(), NoticeOptions{Current: "v0.3.1-rc.18", StateDir: stateDir, API: srv.URL})
	if err != nil || tag != "v0.3.1-rc.19" {
		t.Fatalf("Notice = %q, %v; want the announced v0.3.1-rc.19", tag, err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, CacheFileName)); !os.IsNotExist(err) {
		t.Fatalf("the replacement directory received the result (stat error %v)", err)
	}
	// The admitted directory kept the claim written before the request.
	claim, ok := ReadCache(retired)
	if !ok || claim.AttemptOK {
		t.Fatalf("retired dir cache = %+v, %t; want the pre-request claim", claim, ok)
	}
}

// TestNoticeRechecksAFarFutureAttempt bounds the later-attempt skip: a cache
// stamped more than the TTL ahead (a skewed clock, a tampered file) does not
// silence the notice; the check runs and the cache is rewritten at now.
func TestNoticeRechecksAFarFutureAttempt(t *testing.T) {
	api, reqs := noticeFixture(t, rcListBody)
	stateDir := t.TempDir()
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	if err := WriteCache(stateDir, Cache{AttemptedAt: now.AddDate(1, 0, 0), AttemptOK: true, CheckedOKAt: now.AddDate(1, 0, 0), Current: "v0.3.1-rc.18", Channel: ChannelRC, Newest: "v0.3.1-rc.18"}); err != nil {
		t.Fatal(err)
	}
	opts := NoticeOptions{Current: "v0.3.1-rc.18", StateDir: stateDir, API: api, Now: func() time.Time { return now }}
	tag, err := Notice(context.Background(), opts)
	if err != nil || tag != "v0.3.1-rc.19" {
		t.Fatalf("Notice = %q, %v; want v0.3.1-rc.19 from a fresh check", tag, err)
	}
	if n := atomic.LoadInt32(reqs); n != 1 {
		t.Fatalf("requests = %d, want 1", n)
	}
	if cached, ok := ReadCache(stateDir); !ok || !cached.AttemptedAt.Equal(now) || !cached.AttemptOK {
		t.Fatalf("cache = %+v, %t; want the attempt rewritten at %s", cached, ok, now)
	}
}
