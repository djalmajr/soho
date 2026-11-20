package doctor

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	"github.com/djalmajr/soho/internal/update"
)

// updateNoticeFixture starts a releases API and points SOHO_RELEASES_API at it.
func updateNoticeFixture(t *testing.T, current bool) (*httptest.Server, *int32) {
	t.Helper()
	var reqs int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&reqs, 1)
		_, _ = w.Write([]byte(`[{"tag_name":"v0.3.1-rc.18","prerelease":true,"published_at":"2026-11-18T10:00:00Z"},
			{"tag_name":"v0.3.1-rc.19","prerelease":true,"published_at":"2026-11-20T10:00:00Z"},
			{"tag_name":"v0.3.0","prerelease":false,"published_at":"2026-10-20T10:00:00Z"}]`))
	}))
	t.Cleanup(srv.Close)
	return srv, &reqs
}

// noticeEnv builds the doctor fixture with the fake herdr the doctor probes.
func noticeEnv(t *testing.T) (platform.Env, string) {
	t.Helper()
	env, root := paneContextFixture(t, []fakecli.Rule{
		{Argv: []string{"--version"}, Stdout: "herdr 1.2.3\n"},
		{Argv: []string{"status", "server"}, Stdout: "server 1.2.3\n"},
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`},
		{Argv: []string{"pane", "list", "--workspace", "ws"}, Stdout: `{"result":{"panes":[]}}`},
	})
	env["HERDR_WORKSPACE_ID"] = "ws"
	env["TMPDIR"] = t.TempDir()
	return env, root
}

// runNoticeDoctor runs DoctorCheck with a released build version and returns
// its output and the state dir the notice caches into.
func runNoticeDoctor(t *testing.T, env platform.Env, root string, ctx *core.Config) (string, string) {
	t.Helper()
	old := BuildVersion
	BuildVersion = "v0.3.1-rc.18"
	t.Cleanup(func() { BuildVersion = old })
	var out strings.Builder
	DoctorCheck(ctx, env, root, &out)
	return out.String(), core.StateDirPath(ctx, env, root)
}

// existingStateDir creates the workspace state dir the notice needs: the
// passive check reads and writes the cache only where the state directory
// already exists (SOH-447 F1).
func existingStateDir(t *testing.T, ctx *core.Config, env platform.Env, root string) string {
	t.Helper()
	dir := core.StateDirPath(ctx, env, root)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestDoctorPrintsTheUpdateNotice(t *testing.T) {
	srv, reqs := updateNoticeFixture(t, true)
	env, root := noticeEnv(t)
	env["SOHO_RELEASES_API"] = srv.URL
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
	existingStateDir(t, ctx, env, root)

	got, state := runNoticeDoctor(t, env, root, ctx)
	if !strings.Contains(got, "update available: v0.3.1-rc.19 (soho update)\n") {
		t.Fatalf("doctor output misses the update line:\n%s", got)
	}
	if n := atomic.LoadInt32(reqs); n != 1 {
		t.Fatalf("requests = %d, want 1", n)
	}
	cached, ok := update.ReadCache(state)
	if !ok || cached.Newest != "v0.3.1-rc.19" || cached.Channel == "" {
		t.Fatalf("cache at %s = %+v, %t", state, cached, ok)
	}
}

func TestDoctorUpdateNoticeUsesTheCacheWithin24h(t *testing.T) {
	srv, reqs := updateNoticeFixture(t, true)
	env, root := noticeEnv(t)
	env["SOHO_RELEASES_API"] = srv.URL
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
	existingStateDir(t, ctx, env, root)

	if got, _ := runNoticeDoctor(t, env, root, ctx); !strings.Contains(got, "update available:") {
		t.Fatalf("first doctor output misses the line:\n%s", got)
	}
	// The second run inside the TTL answers from the cache: same line, no
	// second request.
	got, _ := runNoticeDoctor(t, env, root, ctx)
	if !strings.Contains(got, "update available: v0.3.1-rc.19 (soho update)") {
		t.Fatalf("cached doctor output misses the line:\n%s", got)
	}
	if n := atomic.LoadInt32(reqs); n != 1 {
		t.Fatalf("requests = %d, want 1 (the cache answers within 24 h)", n)
	}
}

func TestDoctorUpdateNoticeRefreshesAfter24h(t *testing.T) {
	srv, reqs := updateNoticeFixture(t, true)
	env, root := noticeEnv(t)
	env["SOHO_RELEASES_API"] = srv.URL
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
	state := existingStateDir(t, ctx, env, root)
	// An entry past the TTL, still naming the running version: the doctor
	// checks again instead of trusting it.
	if err := update.WriteCache(state, update.Cache{
		AttemptedAt: time.Now().Add(-25 * time.Hour),
		AttemptOK:   true,
		CheckedOKAt: time.Now().Add(-25 * time.Hour),
		Current:     "v0.3.1-rc.18",
		Channel:     update.ChannelRC,
		Newest:      "v0.3.1-rc.18",
	}); err != nil {
		t.Fatal(err)
	}
	got, _ := runNoticeDoctor(t, env, root, ctx)
	if !strings.Contains(got, "update available: v0.3.1-rc.19 (soho update)") {
		t.Fatalf("stale cache was trusted:\n%s", got)
	}
	if n := atomic.LoadInt32(reqs); n != 1 {
		t.Fatalf("requests = %d, want 1 after the TTL", n)
	}
	if cached, _ := update.ReadCache(state); cached.Newest != "v0.3.1-rc.19" {
		t.Fatalf("cache was not refreshed: %+v", cached)
	}
}

func TestDoctorUpdateNoticeOptOuts(t *testing.T) {
	t.Run("SOHO_NO_UPDATE_CHECK=1", func(t *testing.T) {
		srv, reqs := updateNoticeFixture(t, true)
		env, root := noticeEnv(t)
		env["SOHO_RELEASES_API"] = srv.URL
		env[update.EnvNoUpdateCheck] = "1"
		ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
		got, state := runNoticeDoctor(t, env, root, ctx)
		if strings.Contains(got, "update available:") {
			t.Fatalf("notice printed despite the env opt-out:\n%s", got)
		}
		if n := atomic.LoadInt32(reqs); n != 0 {
			t.Fatalf("requests = %d, want 0", n)
		}
		if _, ok := update.ReadCache(state); ok {
			t.Fatal("an opted-out doctor wrote the update cache")
		}
	})
	t.Run("update_check=off", func(t *testing.T) {
		srv, reqs := updateNoticeFixture(t, true)
		env, root := noticeEnv(t)
		env["SOHO_RELEASES_API"] = srv.URL
		ctx := &core.Config{Entries: map[string]core.ConfigEntry{
			"update_check": {Value: "off", Source: "project"},
		}}
		got, _ := runNoticeDoctor(t, env, root, ctx)
		if strings.Contains(got, "update available:") {
			t.Fatalf("notice printed despite update_check=off:\n%s", got)
		}
		if n := atomic.LoadInt32(reqs); n != 0 {
			t.Fatalf("requests = %d, want 0", n)
		}
	})
	t.Run("SOHO_NOWRITE=1 never writes the cache", func(t *testing.T) {
		srv, reqs := updateNoticeFixture(t, true)
		env, root := noticeEnv(t)
		env["SOHO_RELEASES_API"] = srv.URL
		env["SOHO_NOWRITE"] = "1"
		ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
		got, state := runNoticeDoctor(t, env, root, ctx)
		if strings.Contains(got, "update available:") {
			t.Fatalf("a read-only doctor ran the notice:\n%s", got)
		}
		if n := atomic.LoadInt32(reqs); n != 0 {
			t.Fatalf("requests = %d, want 0", n)
		}
		if _, ok := update.ReadCache(state); ok {
			t.Fatal("a read-only doctor wrote the update cache")
		}
	})
}

func TestDoctorUpdateNoticeIsSilentOnAFailedCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	env, root := noticeEnv(t)
	env["SOHO_RELEASES_API"] = srv.URL
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
	existingStateDir(t, ctx, env, root)
	got, state := runNoticeDoctor(t, env, root, ctx)
	if strings.Contains(got, "update available:") || strings.Contains(got, "could not check") {
		t.Fatalf("a failed check was not silent:\n%s", got)
	}
	cached, ok := update.ReadCache(state)
	if !ok || cached.Newest != "" || cached.AttemptOK || cached.AttemptedAt.IsZero() {
		t.Fatalf("a failed check must claim the attempt in the cache, got %+v, %t", cached, ok)
	}
}

// TestDoctorUpdateNoticeCountsAFailedAttempt is the round-2 F2 defect: two
// doctor runs inside the TTL after a failure pay one request, not two.
func TestDoctorUpdateNoticeCountsAFailedAttempt(t *testing.T) {
	var reqs int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&reqs, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	env, root := noticeEnv(t)
	env["SOHO_RELEASES_API"] = srv.URL
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
	existingStateDir(t, ctx, env, root)
	for i := 0; i < 2; i++ {
		if got, _ := runNoticeDoctor(t, env, root, ctx); strings.Contains(got, "update available:") {
			t.Fatalf("run %d: a failed check announced an update:\n%s", i+1, got)
		}
	}
	if n := atomic.LoadInt32(&reqs); n != 1 {
		t.Fatalf("two doctor runs inside the TTL made %d requests, want 1", n)
	}
}

func TestDoctorUpdateNoticeIsSilentForADevBuild(t *testing.T) {
	srv, reqs := updateNoticeFixture(t, true)
	env, root := noticeEnv(t)
	env["SOHO_RELEASES_API"] = srv.URL
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
	old := BuildVersion
	BuildVersion = "dev"
	t.Cleanup(func() { BuildVersion = old })
	var out strings.Builder
	DoctorCheck(ctx, env, root, &out)
	if strings.Contains(out.String(), "update available:") {
		t.Fatalf("a dev build got the notice:\n%s", out.String())
	}
	if n := atomic.LoadInt32(reqs); n != 0 {
		t.Fatalf("a dev build reached the network (%d requests)", n)
	}
}

func TestInitPrintsTheUpdateNotice(t *testing.T) {
	// init runs the same doctor check before naming the caller, so the line
	// reaches init's output (stderr) once, from the same cached check.
	srv, reqs := updateNoticeFixture(t, true)
	env, root := paneContextFixture(t, []fakecli.Rule{
		{Argv: []string{"--version"}, Stdout: "herdr 1.2.3\n"},
		{Argv: []string{"status", "server"}, Stdout: "server 1.2.3\n"},
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`},
		{Argv: []string{"pane", "list", "--workspace", "ws"}, Stdout: `{"result":{"panes":[]}}`},
		{Argv: []string{"pane", "get", "w0test:p0a"}, Stdout: `{"result":{"pane":{"title":"orchestrator","workspace_id":"ws"}}}`},
	})
	env["HERDR_WORKSPACE_ID"] = "ws"
	env["HERDR_PANE_ID"] = "w0test:p0a"
	env["SOHO_RELEASES_API"] = srv.URL
	env["TMPDIR"] = t.TempDir()
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
	// The notice only reads and writes an existing state dir: init itself
	// creates it a moment later, so the fixture supplies it first.
	existingStateDir(t, ctx, env, root)

	oldVersion := BuildVersion
	BuildVersion = "v0.3.1-rc.18"
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var stdout, stderr strings.Builder
	platform.Stdout, platform.Stderr = &stdout, &stderr
	t.Cleanup(func() {
		BuildVersion = oldVersion
		platform.Stdout, platform.Stderr = oldOut, oldErr
	})

	CmdInit(ctx, env, root)
	if !strings.Contains(stderr.String(), "update available: v0.3.1-rc.19 (soho update)\n") {
		t.Fatalf("init stderr misses the update line:\n%s", stderr.String())
	}
	if n := atomic.LoadInt32(reqs); n != 1 {
		t.Fatalf("requests = %d, want 1", n)
	}
	// The JSON context still reaches stdout, and the notice is not in it.
	if !strings.HasPrefix(stdout.String(), "{") || strings.Contains(stdout.String(), "update available") {
		t.Fatalf("init stdout = %q", stdout.String())
	}
}

func TestDoctorStateDirPathNeedsAnExistingStateDir(t *testing.T) {
	// The notice resolves the path without creating it and answers "" when
	// the state dir does not exist: a project with no state keeps none from
	// the doctor's read of it (round-2 F1).
	root := filepath.Join(t.TempDir(), "absent")
	env := platform.Env{"HOME": t.TempDir(), "SOHO_DIR": root, "HERDR_WORKSPACE_ID": "ws", "PATH": ""}
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
	if dir := doctorStateDirPath(ctx, env, t.TempDir()); dir != "" {
		t.Fatalf("doctorStateDirPath = %q, want no path for an absent state dir", dir)
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatalf("the state root was created by the path lookup: %v", err)
	}
	// Once the dir exists the same lookup returns it, still without writing.
	existingStateDir(t, ctx, env, t.TempDir())
	dir := doctorStateDirPath(ctx, env, t.TempDir())
	if dir == "" || !strings.HasPrefix(dir, root) {
		t.Fatalf("doctorStateDirPath = %q, want a path under %q", dir, root)
	}
}

func TestDoctorStateDirPathIsEmptyWhenTheWorkspaceCannotResolve(t *testing.T) {
	// No workspace and no herdr: the lookup's refusal stays inside the
	// notice (the doctor's own output and exit are untouched).
	env := platform.Env{"HOME": t.TempDir(), "SOHO_DIR": filepath.Join(t.TempDir(), "absent"), "PATH": ""}
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
	if dir := doctorStateDirPath(ctx, env, t.TempDir()); dir != "" {
		t.Fatalf("doctorStateDirPath = %q, want no path", dir)
	}
}

func TestUpdateNoticeCacheContextIsTheNoticeTimeout(t *testing.T) {
	// The passive check is bounded by internal/update's 2 s: a server that
	// never answers must not hold doctor for longer. The doctor's own checks
	// are slow under -race, so the bound is measured against a doctor run
	// whose server answers at once: an unbounded check would add the slow
	// server's 10 s, a bounded one about 2 s.
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	}))
	defer fast.Close()
	var reqs int32
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&reqs, 1)
		select {
		case <-release:
		case <-time.After(10 * time.Second):
		}
		_, _ = w.Write([]byte(`[{"tag_name":"v0.3.1-rc.19","prerelease":true}]`))
	}))
	defer slow.Close()
	defer close(release)

	run := func(api string) (string, time.Duration) {
		env, root := noticeEnv(t)
		env["SOHO_RELEASES_API"] = api
		ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
		existingStateDir(t, ctx, env, root)
		start := time.Now()
		got, _ := runNoticeDoctor(t, env, root, ctx)
		return got, time.Since(start)
	}
	_, baseline := run(fast.URL)
	got, elapsed := run(slow.URL)
	if strings.Contains(got, "update available:") {
		t.Fatalf("a timed-out check announced an update:\n%s", got)
	}
	if extra := elapsed - baseline; extra > 6*time.Second {
		t.Fatalf("doctor waited %s more than its baseline (%s) for the releases API", extra, baseline)
	}
	if atomic.LoadInt32(&reqs) == 0 {
		t.Fatal("the fixture server was never asked")
	}
}

// TestDoctorUpdateNoticeKeepsOutOfAwaitingFormerState is the round-2 F1
// defect: the doctor prints that the state dir stays uncreated for the setup
// migration, so the passive check must not create it or cache inside it.
func TestDoctorUpdateNoticeKeepsOutOfAwaitingFormerState(t *testing.T) {
	env, root := noticeEnv(t)
	// No pin: the former .herdr-soho dir at the project root is waiting for
	// soho setup to move it to .soho.
	delete(env, "SOHO_DIR")
	if err := os.Mkdir(filepath.Join(root, ".herdr-soho"), 0o700); err != nil {
		t.Fatal(err)
	}
	srv, reqs := updateNoticeFixture(t, true)
	env["SOHO_RELEASES_API"] = srv.URL
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
	if _, _, waiting := core.FormerStateWaiting(ctx, env, root); !waiting {
		t.Fatal("invalid fixture: the former state dir is not waiting")
	}
	got, _ := runNoticeDoctor(t, env, root, ctx)
	if !strings.Contains(got, "state dir not created: 'soho setup' moves ") {
		t.Fatalf("doctor missed the migration advisory:\n%s", got)
	}
	if n := atomic.LoadInt32(reqs); n != 0 {
		t.Fatalf("requests = %d, want 0 while the migration waits", n)
	}
	cache := filepath.Join(root, ".soho", "ws", update.CacheFileName)
	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Fatalf("the notice wrote %s while the migration waits (stat error %v)", cache, err)
	}
	if _, _, waiting := core.FormerStateWaiting(ctx, env, root); !waiting {
		t.Fatal("the notice blocked the setup migration")
	}
}

// TestDoctorUpdateNoticeKeepsOutOfTheInstalledSkill is the round-2 F1
// defect's other half: a state dir inside the skill is refused, so the
// passive check must neither request nor write there.
func TestDoctorUpdateNoticeKeepsOutOfTheInstalledSkill(t *testing.T) {
	env, _ := noticeEnv(t)
	skill := env.Get("SOHO_SKILL_DIR")
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("# fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// No pin: the state root resolves under the current directory, the skill.
	delete(env, "SOHO_DIR")
	// The state dir already exists inside the skill: the existence check
	// alone would let the notice read and write it, so the skill admission is
	// what keeps the cache out.
	state := filepath.Join(skill, ".soho", "ws")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	srv, reqs := updateNoticeFixture(t, true)
	env["SOHO_RELEASES_API"] = srv.URL
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
	got, _ := runNoticeDoctor(t, env, skill, ctx)
	if !strings.Contains(got, "would be inside the soho skill") {
		t.Fatalf("doctor missed the skill warning:\n%s", got)
	}
	if n := atomic.LoadInt32(reqs); n != 0 {
		t.Fatalf("requests = %d, want 0 inside the skill", n)
	}
	cache := filepath.Join(state, update.CacheFileName)
	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Fatalf("the notice wrote %s inside the skill (stat error %v)", cache, err)
	}
}

// TestDoctorReviewRound3 ports the round-2 review's residual probes as tests:
// the passive notice never recreates state, never spends a request it cannot
// record, and never announces a preserved result after a failed refresh.
func TestDoctorReviewRound3(t *testing.T) {
	t.Run("state_disappears_during_check", func(t *testing.T) {
		env, root := noticeEnv(t)
		ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
		state := existingStateDir(t, ctx, env, root)
		renamed := filepath.Join(root, "retired-state")
		move := make(chan error, 1)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			move <- os.Rename(state, renamed)
			_, _ = w.Write([]byte(`[{"tag_name":"v0.3.1-rc.19","prerelease":true}]`))
		}))
		defer srv.Close()
		env["SOHO_RELEASES_API"] = srv.URL
		runNoticeDoctor(t, env, root, ctx)
		select {
		case err := <-move:
			if err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatal("fixture never received the check")
		}
		if _, err := os.Stat(state); !os.IsNotExist(err) {
			_, cacheErr := os.Stat(filepath.Join(state, update.CacheFileName))
			t.Fatalf("workspace state was recreated after rename: state stat=%v, cache stat=%v", err, cacheErr)
		}
	})
	t.Run("cannot_claim_attempt", func(t *testing.T) {
		env, root := noticeEnv(t)
		ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
		state := existingStateDir(t, ctx, env, root)
		if err := os.Mkdir(filepath.Join(state, update.CacheFileName), 0o700); err != nil {
			t.Fatal(err)
		}
		var requests int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			atomic.AddInt32(&requests, 1)
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer srv.Close()
		env["SOHO_RELEASES_API"] = srv.URL
		for i := 0; i < 2; i++ {
			if got, _ := runNoticeDoctor(t, env, root, ctx); strings.Contains(got, "update available:") {
				t.Fatalf("run %d: an unclaimable attempt announced an update:\n%s", i+1, got)
			}
		}
		if n := atomic.LoadInt32(&requests); n != 0 {
			t.Fatalf("unwritable cache allowed %d requests within 24h, want 0", n)
		}
	})
	t.Run("expired_success_then_failure", func(t *testing.T) {
		env, root := noticeEnv(t)
		ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
		state := existingStateDir(t, ctx, env, root)
		stale := time.Now().Add(-25 * time.Hour)
		if err := update.WriteCache(state, update.Cache{
			AttemptedAt: stale,
			AttemptOK:   true,
			CheckedOKAt: stale,
			Current:     "v0.3.1-rc.18",
			Channel:     update.ChannelRC,
			Newest:      "v0.3.1-rc.19",
		}); err != nil {
			t.Fatal(err)
		}
		var requests int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			atomic.AddInt32(&requests, 1)
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer srv.Close()
		env["SOHO_RELEASES_API"] = srv.URL
		first, _ := runNoticeDoctor(t, env, root, ctx)
		second, _ := runNoticeDoctor(t, env, root, ctx)
		if strings.Contains(first, "update available:") {
			t.Fatalf("failure unexpectedly printed an update on the initiating call:\n%s", first)
		}
		if strings.Contains(second, "update available:") || atomic.LoadInt32(&requests) != 1 {
			t.Fatalf("after failed refresh: requests=%d, stale update line=%t", atomic.LoadInt32(&requests), strings.Contains(second, "update available:"))
		}
		// The successful result is preserved apart from the failed attempt
		// that now owns the budget.
		cached, ok := update.ReadCache(state)
		if !ok || cached.Newest != "v0.3.1-rc.19" || cached.AttemptOK || cached.CheckedOKAt.IsZero() {
			t.Fatalf("cache after the failed refresh = %+v, %t", cached, ok)
		}
	})
}
