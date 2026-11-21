package job

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/dispatch"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/spawn"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

const briefA = `{"schema":1,"id":"job-1","origem":{"tipo":"card","ref":"CARD-1"},"repo":"example-org/example-repo","objetivo":"Ship the change.","aceite":[{"criterio":"tests pass","prova":"go test ./internal/job"}]}`

const briefB = `{
  "aceite": [{"prova": "go test ./internal/job", "criterio": "tests pass"}],
  "objetivo": "Ship the change.",
  "repo": "example-org/example-repo",
  "origem": {"ref": "CARD-1", "tipo": "card"},
  "id": "job-1",
  "schema": 1
}`

const briefChanged = `{"schema":1,"id":"job-1","origem":{"tipo":"card","ref":"CARD-1"},"repo":"example-org/example-repo","objetivo":"Ship another change.","aceite":[{"criterio":"tests pass","prova":"go test ./internal/job"}]}`

const briefEscaped = `{"schema":1,"id":"job-2","origem":{"tipo":"card","ref":"CARD-1"},"repo":"example-org/example-repo","objetivo":"Ship the \u003cchange.","aceite":[{"criterio":"tests pass","prova":"go test ./internal/job"}]}`

const briefRawAngle = `{"aceite":[{"criterio":"tests pass","prova":"go test ./internal/job"}],"id":"job-2","objetivo":"Ship the <change.","origem":{"tipo":"card","ref":"CARD-1"},"repo":"example-org/example-repo","schema":1}`

func TestStartIdempotency(t *testing.T) {
	t.Run("reordered keys are the same brief", func(t *testing.T) {
		root := t.TempDir()
		s := Open(root)
		first, err := s.Start("job-1", []byte(briefA))
		if err != nil {
			t.Fatalf("start: %v", err)
		}
		if first.DuplicateOf != "" || first.Status != StatusAccepted || first.ID != "job-1" {
			t.Fatalf("first start: %+v", first)
		}
		second, err := s.Start("job-1", []byte(briefB))
		if err != nil {
			t.Fatalf("duplicate start: %v", err)
		}
		if second.DuplicateOf != "job-1" || second.BriefSHA256 != first.BriefSHA256 || second.Status != StatusAccepted {
			t.Fatalf("duplicate start: %+v", second)
		}
		stored, err := os.ReadFile(filepath.Join(root, "jobs", "job-1", "state.json"))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(stored, []byte("duplicate_of")) {
			t.Fatalf("duplicate_of was persisted: %s", stored)
		}
		dir, err := Dir(root, "job-1")
		if err != nil || dir != filepath.Join(root, "jobs", "job-1") {
			t.Fatalf("Dir = %q err=%v", dir, err)
		}
		raw, err := os.ReadFile(filepath.Join(dir, "brief.json"))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != first.BriefSHA256 {
			t.Fatalf("brief.json hash %x, state %s", sum, first.BriefSHA256)
		}
		if !bytes.Contains(raw, []byte(`"aceite"`)) || bytes.Contains(raw, []byte("\n")) {
			t.Fatalf("brief.json is not compact canonical JSON: %s", raw)
		}
		if !bytes.Contains(raw, []byte(`{"ref":"CARD-1","tipo":"card"}`)) {
			t.Fatalf("nested keys were not sorted: %s", raw)
		}
		events, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Count(events, []byte("\n")) != 1 || !bytes.Contains(events, []byte(`"tipo":"accepted"`)) {
			t.Fatalf("accepted events = %s", events)
		}
		md, err := os.ReadFile(filepath.Join(dir, "brief.md"))
		if err != nil {
			t.Fatal(err)
		}
		if missing := dispatch.BriefMissingSections(string(md), false, nil); missing != "" {
			t.Fatalf("brief.md lint gaps:%s", missing)
		}
		third, err := s.Start("job-1", []byte(briefA))
		if err != nil || third.DuplicateOf != "job-1" {
			t.Fatalf("retry start: %+v err=%v", third, err)
		}
		events, err = os.ReadFile(filepath.Join(dir, "events.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Count(events, []byte("\n")) != 1 {
			t.Fatalf("retry appended an event: %s", events)
		}
	})

	t.Run("escaped and raw JSON share one hash", func(t *testing.T) {
		root := t.TempDir()
		s := Open(root)
		first, err := s.Start("job-2", []byte(briefEscaped))
		if err != nil {
			t.Fatalf("start: %v", err)
		}
		second, err := s.Start("job-2", []byte(briefRawAngle))
		if err != nil || second.DuplicateOf != "job-2" || second.BriefSHA256 != first.BriefSHA256 {
			t.Fatalf("duplicate: %+v err=%v", second, err)
		}
		raw, err := os.ReadFile(filepath.Join(root, "jobs", "job-2", "brief.json"))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte(`\u003c`)) || !bytes.Contains(raw, []byte(`Ship the <change.`)) {
			t.Fatalf("canonical brief = %s", raw)
		}
	})

	t.Run("changed field reuses the id", func(t *testing.T) {
		root := t.TempDir()
		s := Open(root)
		if _, err := s.Start("job-1", []byte(briefA)); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "jobs", "job-1", "state.json")
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.Start("job-1", []byte(briefChanged))
		if code := exitCode(t, err); code != ExitBriefConflict {
			t.Fatalf("code %d, err %v", code, err)
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, after) {
			t.Fatalf("state changed on hash mismatch\nbefore %s\nafter %s", before, after)
		}
		entries, err := os.ReadDir(filepath.Join(root, "jobs"))
		if err != nil || len(entries) != 1 {
			t.Fatalf("jobs = %v err=%v", entries, err)
		}
	})

	t.Run("concurrent starts create one job", func(t *testing.T) {
		root := t.TempDir()
		s := Open(root)
		const n = 8
		var wg sync.WaitGroup
		var mu sync.Mutex
		created, dups := 0, 0
		errs := make([]error, 0)
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				st, err := s.Start("job-1", []byte(briefA))
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					errs = append(errs, err)
					return
				}
				if st.DuplicateOf == "" {
					created++
				} else if st.DuplicateOf == "job-1" {
					dups++
				}
			}()
		}
		wg.Wait()
		if len(errs) != 0 || created != 1 || dups != n-1 {
			t.Fatalf("created=%d dups=%d errs=%v", created, dups, errs)
		}
		entries, err := os.ReadDir(filepath.Join(root, "jobs"))
		if err != nil || len(entries) != 1 || entries[0].Name() != "job-1" {
			t.Fatalf("jobs = %v err=%v", entries, err)
		}
		events, err := os.ReadFile(filepath.Join(root, "jobs", "job-1", "events.jsonl"))
		if err != nil || bytes.Count(events, []byte("\n")) != 1 {
			t.Fatalf("events = %s err=%v", events, err)
		}
	})

	t.Run("concurrent different briefs still create one job", func(t *testing.T) {
		root := t.TempDir()
		s := Open(root)
		var wg sync.WaitGroup
		var mu sync.Mutex
		ok, conflicts := 0, 0
		for i := 0; i < 8; i++ {
			body := briefA
			if i%2 == 1 {
				body = briefChanged
			}
			wg.Add(1)
			go func(raw string) {
				defer wg.Done()
				_, err := s.Start("job-1", []byte(raw))
				mu.Lock()
				defer mu.Unlock()
				if err == nil {
					ok++
					return
				}
				var exit *ExitError
				if errors.As(err, &exit) && exit.Code == ExitBriefConflict {
					conflicts++
				}
			}(body)
		}
		wg.Wait()
		if ok+conflicts != 8 || ok < 1 || conflicts < 1 {
			t.Fatalf("ok=%d conflicts=%d", ok, conflicts)
		}
		entries, err := os.ReadDir(filepath.Join(root, "jobs"))
		if err != nil || len(entries) != 1 {
			t.Fatalf("jobs = %v err=%v", entries, err)
		}
	})

	t.Run("invalid brief writes nothing", func(t *testing.T) {
		root := t.TempDir()
		s := Open(root)
		cases := []struct {
			id   string
			body string
		}{
			{id: "job-1", body: `{}`},
			{id: "job-1", body: `{"schema":1,"id":"other","origem":{"tipo":"card","ref":"CARD-1"},"repo":"example-org/example-repo","objetivo":"Ship the change.","aceite":[]}`},
			{id: "bad id", body: briefA},
			{id: "..", body: `{"schema":1,"id":"..","origem":{"tipo":"card","ref":"CARD-1"},"repo":"example-org/example-repo","objetivo":"Ship the change.","aceite":[]}`},
			{id: "job-1", body: `{"schema":2,"id":"job-1","origem":{"tipo":"card","ref":"CARD-1"},"repo":"example-org/example-repo","objetivo":"Ship the change.","aceite":[]}`},
			{id: "job-1", body: `{"schema":1,"id":"job-1","origem":{"tipo":"email","ref":"CARD-1"},"repo":"example-org/example-repo","objetivo":"Ship the change.","aceite":[]}`},
			{id: "job-1", body: `{"schema":1,"id":"job-1","origem":{"tipo":"card","ref":"CARD-1"},"repo":"example-repo","objetivo":"Ship the change.","aceite":[]}`},
			{id: "job-1", body: `{"schema":1,"id":"job-1","origem":{"tipo":"card","ref":"CARD-1"},"repo":"example-org/example-repo","base":"a/../b","objetivo":"Ship the change.","aceite":[]}`},
			{id: "job-1", body: `{"schema":1,"id":"job-1","origem":{"tipo":"card","ref":"CARD-1"},"repo":"example-org/example-repo","modo":"dirty","objetivo":"Ship the change.","aceite":[]}`},
			{id: "job-1", body: string(bytes.Repeat([]byte("x"), MaxBriefBytes+1))},
		}
		for _, tc := range cases {
			_, err := s.Start(tc.id, []byte(tc.body))
			if code := exitCode(t, err); code != ExitUsage {
				t.Fatalf("id %q code %d err %v", tc.id, code, err)
			}
		}
		if _, err := os.Stat(filepath.Join(root, "jobs")); !os.IsNotExist(err) {
			t.Fatalf("invalid start created jobs: %v", err)
		}
		const withBase = `{"schema":1,"id":"job-3","origem":{"tipo":"cron","ref":"nightly"},"repo":"example-org/example-repo","base":"release/1","modo":"workspace","objetivo":"Read only.","aceite":[]}`
		if _, err := s.Start("job-3", []byte(withBase)); err != nil {
			t.Fatal(err)
		}
	})
}

func TestStartIdempotencyInterrupted(t *testing.T) {
	t.Run("resume after brief.json publication with no events", func(t *testing.T) {
		s, root := startedJob(t)
		dir := filepath.Join(root, "jobs", "job-1")
		if err := os.Remove(filepath.Join(dir, "state.json")); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(dir, "events.jsonl")); err != nil {
			t.Fatal(err)
		}
		briefJSON, err := os.ReadFile(filepath.Join(dir, "brief.json"))
		if err != nil {
			t.Fatal(err)
		}
		briefMD, err := os.ReadFile(filepath.Join(dir, "brief.md"))
		if err != nil {
			t.Fatal(err)
		}
		st, err := s.Start("job-1", []byte(briefA))
		if err != nil || st.Status != StatusAccepted {
			t.Fatalf("resume: %+v err=%v", st, err)
		}
		afterJSON, err := os.ReadFile(filepath.Join(dir, "brief.json"))
		if err != nil || !bytes.Equal(afterJSON, briefJSON) {
			t.Fatalf("brief.json was rewritten:\nbefore %s\nafter %s", briefJSON, afterJSON)
		}
		afterMD, err := os.ReadFile(filepath.Join(dir, "brief.md"))
		if err != nil || !bytes.Equal(afterMD, briefMD) {
			t.Fatalf("brief.md changed:\nbefore %s\nafter %s", briefMD, afterMD)
		}
		events, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
		if err != nil || bytes.Count(events, []byte("\n")) != 1 || !bytes.Contains(events, []byte(`"tipo":"accepted"`)) {
			t.Fatalf("accepted events = %s err=%v", events, err)
		}
		saved, err := readState(dir)
		if err != nil || saved.BriefSHA256 != st.BriefSHA256 || saved.Status != StatusAccepted {
			t.Fatalf("state: %+v err=%v", saved, err)
		}
	})

	t.Run("resume after an accepted event keeps one accepted", func(t *testing.T) {
		s, root := startedJob(t)
		dir := filepath.Join(root, "jobs", "job-1")
		if err := os.Remove(filepath.Join(dir, "state.json")); err != nil {
			t.Fatal(err)
		}
		st, err := s.Start("job-1", []byte(briefA))
		if err != nil || st.Status != StatusAccepted {
			t.Fatalf("resume: %+v err=%v", st, err)
		}
		events, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
		if err != nil || bytes.Count(events, []byte("\n")) != 1 {
			t.Fatalf("accepted events = %s err=%v, want exactly one", events, err)
		}
		saved, err := readState(dir)
		if err != nil || saved.BriefSHA256 != st.BriefSHA256 {
			t.Fatalf("state: %+v err=%v", saved, err)
		}
	})

	t.Run("different brief is refused and changes nothing", func(t *testing.T) {
		s, root := startedJob(t)
		dir := filepath.Join(root, "jobs", "job-1")
		if err := os.Remove(filepath.Join(dir, "state.json")); err != nil {
			t.Fatal(err)
		}
		briefJSON, err := os.ReadFile(filepath.Join(dir, "brief.json"))
		if err != nil {
			t.Fatal(err)
		}
		briefMD, err := os.ReadFile(filepath.Join(dir, "brief.md"))
		if err != nil {
			t.Fatal(err)
		}
		events, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.Start("job-1", []byte(briefChanged))
		if code := exitCode(t, err); code != ExitBriefConflict {
			t.Fatalf("code %d err %v", code, err)
		}
		if _, err := os.Stat(filepath.Join(dir, "state.json")); !os.IsNotExist(err) {
			t.Fatalf("state was written: %v", err)
		}
		afterJSON, err := os.ReadFile(filepath.Join(dir, "brief.json"))
		if err != nil {
			t.Fatal(err)
		}
		afterMD, err := os.ReadFile(filepath.Join(dir, "brief.md"))
		if err != nil {
			t.Fatal(err)
		}
		afterEvents, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(afterJSON, briefJSON) {
			t.Fatalf("brief.json changed:\nbefore %s\nafter %s", briefJSON, afterJSON)
		}
		if !bytes.Equal(afterMD, briefMD) {
			t.Fatalf("brief.md changed:\nbefore %s\nafter %s", briefMD, afterMD)
		}
		if !bytes.Equal(afterEvents, events) {
			t.Fatalf("events.jsonl changed:\nbefore %s\nafter %s", events, afterEvents)
		}
	})
}

func exitCode(t *testing.T, err error) int {
	t.Helper()
	var exit *ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("error %v, want ExitError", err)
	}
	return exit.Code
}

// startStarter wires a Starter against the prepare fixture's machine and a
// hermetic state root, with a fake Herdr in place of the CLI and the
// fixture's git clone seam.
func startStarter(t *testing.T, f *prepareFixture, fake *fakeHerdr, stateRoot string, friction *[]string) Starter {
	t.Helper()
	if fake.OnRun == nil {
		fake.OnRun = fakeSupervisorConfirms(stateRoot, fake)
	}
	return Starter{
		Env:       f.env,
		Machine:   f.machine,
		Preparer:  Preparer{Machine: f.machine, Env: f.env, Clone: f.gitClone()},
		Herdr:     fake,
		Panes:     fake,
		SelfExe:   "/opt/bin/soho",
		StateRoot: func(checkout string) string { return stateRoot },
		Friction:  func(message string) { *friction = append(*friction, message) },
	}
}

// fakeSupervisorConfirms stands in for the supervisor the fake pane run
// launches: it confirms the launch from the created workspace's root pane,
// as `job supervise` does before it runs. It is an implementation-generated
// confirmation, never evidence of a real Herdr launch.
func fakeSupervisorConfirms(stateRoot string, fake *fakeHerdr) func(string, []string) {
	return func(paneID string, argv []string) {
		if len(argv) < 5 || fake.RunErr != nil {
			return
		}
		env := platform.Env{"HERDR_WORKSPACE_ID": fake.CreateWorkspaceID, "HERDR_PANE_ID": paneID}
		// argv is [exe, job, supervise, --id, <id>, --repo, <org>/<repo>].
		_, _ = ConfirmLaunch(Open(stateRoot), argv[4], env)
	}
}

func startRequest(base string, brief string) StartRequest {
	return StartRequest{
		ID: "job-1", Org: prepOrg, Repo: prepRepo, Base: base, Mode: "worktree",
		Brief: []byte(brief), TimeoutMin: 120,
		Team: Team{
			Source:       "teams/example-org/example-repo.conf",
			OverrideKeys: []string{"lane.build.roles"},
			Pairs:        []TeamPair{{Key: "panes", Value: "3"}, {Key: "lane.build.roles", Value: "implementer"}},
		},
	}
}

// startFake is the happy-path fake Herdr every Starter test starts from.
func startFake() *fakeHerdr {
	return &fakeHerdr{Reachable: true, CreateWorkspaceID: "w9", CreateRootPaneID: "w9:p1"}
}

func startWorktree(f *prepareFixture) string {
	return filepath.Join(f.checkout(), ".worktrees", "job-job-1")
}

func TestStartSuccess(t *testing.T) {
	f := newPrepareFixture(t, []fakecli.Rule{{Argv: []string{"auth", "status"}}})
	fake := startFake()
	stateRoot := t.TempDir()
	var friction []string
	s := startStarter(t, f, fake, stateRoot, &friction)

	st, store, err := s.Start(startRequest("main", briefA))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if store == nil {
		t.Fatal("no store returned")
	}
	if st.Status != StatusRunning || st.Motivo != nil {
		t.Fatalf("state = %q motivo %v", st.Status, st.Motivo)
	}
	worktree := startWorktree(f)
	snap, err := store.Snapshot("job-1")
	if err != nil {
		t.Fatal(err)
	}
	st = snap.State
	if st.Checkout != f.checkout() || st.Dir != worktree || st.Branch != "job/job-1" ||
		st.Base != "main" || len(st.BaseSHA) != 40 || st.Modo != "worktree" ||
		st.TimeoutMin != 120 || st.WorkspaceID != "w9" || st.RootPane != "w9:p1" {
		t.Fatalf("run facts = %+v", st)
	}
	// The team source is recorded with the run facts: the source file and
	// the brief override keys, an empty non-nil slice when there are none.
	if st.Equipe == nil || st.Equipe.Fonte != "teams/example-org/example-repo.conf" ||
		!reflect.DeepEqual(st.Equipe.OverrideBrief, []string{"lane.build.roles"}) {
		t.Fatalf("equipe = %+v", st.Equipe)
	}
	// StartedAt is RFC 3339 with an explicit offset, taken from the store
	// clock (the system clock here): fresh within a minute, never Z.
	if strings.HasSuffix(st.StartedAt, "Z") {
		t.Fatalf("StartedAt has no explicit offset: %s", st.StartedAt)
	}
	parsed, err := time.Parse(time.RFC3339, st.StartedAt)
	if err != nil {
		t.Fatalf("StartedAt = %q: %v", st.StartedAt, err)
	}
	if d := time.Since(parsed); d < -time.Minute || d > time.Minute {
		t.Fatalf("StartedAt = %s, not from the store clock", st.StartedAt)
	}
	// The session file equals the team pairs in order, mode 0600.
	session := filepath.Join(stateRoot, "w9", "session.conf")
	raw, err := os.ReadFile(session)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "panes=3\nlane.build.roles=implementer\n" {
		t.Fatalf("session.conf = %q", raw)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(session)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("session.conf mode = %v", info.Mode().Perm())
		}
	}
	// The supervisor launch: exactly one run, exactly this argv.
	if len(fake.RunCalls) != 1 {
		t.Fatalf("RunCalls = %+v", fake.RunCalls)
	}
	if fake.RunCalls[0].PaneID != "w9:p1" {
		t.Fatalf("Run pane = %q", fake.RunCalls[0].PaneID)
	}
	if want := []string{"/opt/bin/soho", "job", "supervise", "--id", "job-1", "--repo", "example-org/example-repo"}; !reflect.DeepEqual(fake.RunCalls[0].Argv, want) {
		t.Fatalf("Run argv = %v", fake.RunCalls[0].Argv)
	}
	// The workspace: exactly one create, cwd = worktree, label job-<id>.
	if len(fake.CreateCalls) != 1 {
		t.Fatalf("CreateCalls = %+v", fake.CreateCalls)
	}
	// The root pane gets exactly the user-configuration locator of this
	// start: the base of the selected user configuration, absolute.
	wantEnv := map[string]string{launchConfigBaseVar: filepath.Dir(filepath.Dir(platform.UserConfigPath(platform.Current(), f.env)))}
	if create := fake.CreateCalls[0]; create.Cwd != worktree || create.Label != "job-job-1" || !reflect.DeepEqual(create.Env, wantEnv) {
		t.Fatalf("Create = %+v, want env %v", create, wantEnv)
	}
	for _, key := range []string{"XDG_CONFIG_HOME", "HOME", "USERPROFILE"} {
		if _, ok := fake.CreateCalls[0].Env[key]; ok {
			t.Fatalf("create env carries the generic selector %q: %v", key, fake.CreateCalls[0].Env)
		}
	}
	// Events: accepted then preparing.
	events, err := readEvents(filepath.Join(stateRoot, "jobs", "job-1"))
	if err != nil {
		t.Fatal(err)
	}
	var tipos []string
	for _, event := range events {
		tipos = append(tipos, event.Tipo)
	}
	if !reflect.DeepEqual(tipos, []string{"accepted", "preparing"}) {
		t.Fatalf("event types = %v", tipos)
	}
	if events[1].Resumo != "preparing checkout and worktree" {
		t.Fatalf("preparing resumo = %q", events[1].Resumo)
	}
	// No report while the job is running, no friction.
	if _, err := os.Stat(filepath.Join(stateRoot, "jobs", "job-1", "report.json")); !os.IsNotExist(err) {
		t.Fatalf("report.json while running: %v", err)
	}
	if len(friction) != 0 {
		t.Fatalf("friction = %v", friction)
	}
}

func TestStartUnreachable(t *testing.T) {
	f := newPrepareFixture(t, []fakecli.Rule{{Argv: []string{"auth", "status"}}})
	fake := &fakeHerdr{}
	stateRoot := t.TempDir()
	var friction []string
	s := startStarter(t, f, fake, stateRoot, &friction)

	_, store, err := s.Start(startRequest("main", briefA))
	wantExit(t, err, ExitHerdr, "job: no reachable Herdr server")
	if store != nil {
		t.Fatalf("store = %v, want nil (nothing written)", store)
	}
	if fake.ListCalls != 1 {
		t.Fatalf("ListCalls = %d, want 1", fake.ListCalls)
	}
	if len(fake.CreateCalls) != 0 || len(fake.RunCalls) != 0 {
		t.Fatalf("CreateCalls = %+v RunCalls = %+v", fake.CreateCalls, fake.RunCalls)
	}
	if _, err := os.Stat(f.checkout()); !os.IsNotExist(err) {
		t.Fatalf("checkout exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stateRoot, "jobs", "job-1", "state.json")); !os.IsNotExist(err) {
		t.Fatalf("job directory exists: %v", err)
	}
}

func TestStartCloneFailure(t *testing.T) {
	f := newPrepareFixture(t, []fakecli.Rule{{Argv: []string{"auth", "status"}}})
	fake := startFake()
	stateRoot := t.TempDir()
	var friction []string
	s := startStarter(t, f, fake, stateRoot, &friction)
	s.Preparer.Clone = func(org, repo, dest string) error { return errors.New("clone down") }

	_, store, err := s.Start(startRequest("main", briefA))
	wantExit(t, err, ExitPrepare, "job: preparation failed: clone")
	if store != nil {
		t.Fatalf("store = %v, want nil (nothing written)", store)
	}
	if _, err := os.Stat(f.checkout()); !os.IsNotExist(err) {
		t.Fatalf("checkout exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stateRoot, "jobs", "job-1", "state.json")); !os.IsNotExist(err) {
		t.Fatalf("job directory exists: %v", err)
	}
	if fake.ListCalls != 1 || len(fake.CreateCalls) != 0 {
		t.Fatalf("ListCalls = %d CreateCalls = %d", fake.ListCalls, len(fake.CreateCalls))
	}
}

func TestStartPrepareFailureAfterRecord(t *testing.T) {
	f := newPrepareFixture(t, []fakecli.Rule{{Argv: []string{"auth", "status"}}})
	f.cloneCheckout()
	fake := startFake()
	stateRoot := t.TempDir()
	var friction []string
	s := startStarter(t, f, fake, stateRoot, &friction)

	_, store, err := s.Start(startRequest("nope", briefA))
	wantExit(t, err, ExitPrepare, "job: preparation failed: unknown base")
	if store == nil {
		t.Fatal("no store returned")
	}
	snap, err := store.Snapshot("job-1")
	if err != nil {
		t.Fatal(err)
	}
	if snap.State.Status != StatusFailed || snap.State.Motivo == nil ||
		*snap.State.Motivo != "job: preparation failed: unknown base" {
		t.Fatalf("state = %q motivo %v", snap.State.Status, snap.State.Motivo)
	}
	failure, terminal := failEvents(t, stateRoot, "job-1")
	if failure.Refs["motivo"] != "job: preparation failed: unknown base" {
		t.Fatalf("failure refs = %v", failure.Refs)
	}
	if terminal.Refs["exit"] != "22" {
		t.Fatalf("terminal refs = %v", terminal.Refs)
	}
	assertFailedReport(t, stateRoot, "job-1", "job: preparation failed: unknown base")
	if len(fake.CreateCalls) != 0 || len(fake.RunCalls) != 0 {
		t.Fatalf("CreateCalls = %+v RunCalls = %+v", fake.CreateCalls, fake.RunCalls)
	}
}

func TestStartWorkspaceCreateFailure(t *testing.T) {
	f := newPrepareFixture(t, []fakecli.Rule{{Argv: []string{"auth", "status"}}})
	f.cloneCheckout()
	fake := startFake()
	fake.CreateErr = &ExitError{Code: ExitHerdr, Msg: "job: herdr workspace create failed"}
	stateRoot := t.TempDir()
	var friction []string
	s := startStarter(t, f, fake, stateRoot, &friction)

	_, store, err := s.Start(startRequest("main", briefA))
	wantExit(t, err, ExitHerdr, "job: herdr workspace create failed")
	if store == nil {
		t.Fatal("no store returned")
	}
	snap, err := store.Snapshot("job-1")
	if err != nil {
		t.Fatal(err)
	}
	if snap.State.Status != StatusFailed || snap.State.Motivo == nil ||
		*snap.State.Motivo != "job: herdr workspace create failed" {
		t.Fatalf("state = %q motivo %v", snap.State.Status, snap.State.Motivo)
	}
	failure, terminal := failEvents(t, stateRoot, "job-1")
	if failure.Refs["motivo"] != "job: herdr workspace create failed" || terminal.Refs["exit"] != "4" {
		t.Fatalf("failure = %+v terminal = %+v", failure, terminal)
	}
	assertFailedReport(t, stateRoot, "job-1", "job: herdr workspace create failed")
	if len(fake.CreateCalls) != 1 || len(fake.CloseIDs) != 0 || len(fake.RunCalls) != 0 {
		t.Fatalf("CreateCalls = %d CloseIDs = %v RunCalls = %d", len(fake.CreateCalls), fake.CloseIDs, len(fake.RunCalls))
	}
}

func TestStartPaneRunFailure(t *testing.T) {
	f := newPrepareFixture(t, []fakecli.Rule{{Argv: []string{"auth", "status"}}})
	f.cloneCheckout()
	fake := startFake()
	fake.RunErr = &ExitError{Code: ExitHerdr, Msg: "job: herdr pane run failed"}
	fake.CloseErr = errors.New("close down")
	stateRoot := t.TempDir()
	var friction []string
	s := startStarter(t, f, fake, stateRoot, &friction)

	_, store, err := s.Start(startRequest("main", briefA))
	wantExit(t, err, ExitHerdr, "job: herdr pane run failed")
	if store == nil {
		t.Fatal("no store returned")
	}
	snap, err := store.Snapshot("job-1")
	if err != nil {
		t.Fatal(err)
	}
	if snap.State.Status != StatusFailed || snap.State.Motivo == nil ||
		*snap.State.Motivo != "job: herdr pane run failed" {
		t.Fatalf("state = %q motivo %v", snap.State.Status, snap.State.Motivo)
	}
	// The start failure paths append the cleanup event after the terminal,
	// so the pair is looked up by tipo, not by position.
	failure, terminal := failAndTerminalEvents(t, stateRoot, "job-1")
	if failure.Refs["motivo"] != "job: herdr pane run failed" || terminal.Refs["exit"] != "4" {
		t.Fatalf("failure = %+v terminal = %+v", failure, terminal)
	}
	assertFailedReport(t, stateRoot, "job-1", "job: herdr pane run failed")
	if !reflect.DeepEqual(fake.CloseIDs, []string{"w9"}) {
		t.Fatalf("CloseIDs = %v, want exactly [w9]", fake.CloseIDs)
	}
	if len(friction) != 1 || !strings.Contains(friction[0], "close down") {
		t.Fatalf("friction = %v, want the close error only to friction", friction)
	}
	// The workspace stayed open: the report says so and the log carries the
	// cleanup event the start failure appends.
	if rep := reportJSON(t, stateRoot, "job-1"); rep.Limpeza.Workspace != "open" {
		t.Fatalf("limpeza = %+v, want open", rep.Limpeza)
	}
	if tipos := eventTipos(t, stateRoot, "job-1"); !reflect.DeepEqual(tipos, []string{"accepted", "preparing", "failure", "terminal", "cleanup"}) {
		t.Fatalf("event tipos = %v", tipos)
	}
}

func TestStartSessionFileFailure(t *testing.T) {
	f := newPrepareFixture(t, []fakecli.Rule{{Argv: []string{"auth", "status"}}})
	f.cloneCheckout()
	fake := startFake()
	fake.CloseErr = errors.New("close down")
	stateRoot := t.TempDir()
	var friction []string
	s := startStarter(t, f, fake, stateRoot, &friction)
	// Block the session directory: <stateRoot>/w9 exists as a regular file.
	if err := os.WriteFile(filepath.Join(stateRoot, "w9"), []byte("blocker"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, store, err := s.Start(startRequest("main", briefA))
	wantExit(t, err, ExitUsage, "job: cannot write the team session")
	if store == nil {
		t.Fatal("no store returned")
	}
	snap, err := store.Snapshot("job-1")
	if err != nil {
		t.Fatal(err)
	}
	if snap.State.Status != StatusFailed || snap.State.Motivo == nil ||
		*snap.State.Motivo != "job: cannot write the team session" {
		t.Fatalf("state = %q motivo %v", snap.State.Status, snap.State.Motivo)
	}
	// The start failure paths append the cleanup event after the terminal,
	// so the terminal is looked up by tipo, not by position.
	_, terminal := failAndTerminalEvents(t, stateRoot, "job-1")
	if terminal.Refs["exit"] != "2" {
		t.Fatalf("terminal refs = %v", terminal.Refs)
	}
	assertFailedReport(t, stateRoot, "job-1", "job: cannot write the team session")
	if !reflect.DeepEqual(fake.CloseIDs, []string{"w9"}) || len(fake.RunCalls) != 0 {
		t.Fatalf("CloseIDs = %v RunCalls = %d", fake.CloseIDs, len(fake.RunCalls))
	}
	if len(friction) != 1 || !strings.Contains(friction[0], "close down") {
		t.Fatalf("friction = %v", friction)
	}
	// The workspace stayed open: the report says so and the log carries the
	// cleanup event the start failure appends.
	if rep := reportJSON(t, stateRoot, "job-1"); rep.Limpeza.Workspace != "open" {
		t.Fatalf("limpeza = %+v, want open", rep.Limpeza)
	}
	if tipos := eventTipos(t, stateRoot, "job-1"); !reflect.DeepEqual(tipos, []string{"accepted", "preparing", "failure", "terminal", "cleanup"}) {
		t.Fatalf("event tipos = %v", tipos)
	}
}

func TestStartRetry(t *testing.T) {
	f := newPrepareFixture(t, []fakecli.Rule{{Argv: []string{"auth", "status"}}})
	fake := startFake()
	stateRoot := t.TempDir()
	var friction []string
	s := startStarter(t, f, fake, stateRoot, &friction)

	first, _, err := s.Start(startRequest("main", briefA))
	if err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if first.Status != StatusRunning || first.DuplicateOf != "" {
		t.Fatalf("first = %+v", first)
	}

	// A second Start with the same brief returns the duplicate; no second
	// workspace, no second supervisor launch.
	second, store, err := s.Start(startRequest("main", briefA))
	if err != nil {
		t.Fatalf("retry Start: %v", err)
	}
	if store == nil || second.DuplicateOf != "job-1" || second.Status != StatusRunning {
		t.Fatalf("retry = %+v", second)
	}
	if len(fake.CreateCalls) != 1 || len(fake.RunCalls) != 1 {
		t.Fatalf("CreateCalls = %d RunCalls = %d, want 1 and 1", len(fake.CreateCalls), len(fake.RunCalls))
	}

	// A changed brief is exit 20 and changes nothing.
	_, _, err = s.Start(startRequest("main", briefChanged))
	wantExit(t, err, ExitBriefConflict, "job: id reused with a different brief")
	if len(fake.CreateCalls) != 1 || len(fake.RunCalls) != 1 {
		t.Fatalf("conflict re-ran steps: CreateCalls = %d RunCalls = %d", len(fake.CreateCalls), len(fake.RunCalls))
	}
}

// TestStartCrashAfterCreate simulates a crash after Herdr.Create and before
// the workspace id is recorded, through the afterWorkspaceCreate hook.
func TestStartCrashAfterCreate(t *testing.T) {
	f := newPrepareFixture(t, []fakecli.Rule{{Argv: []string{"auth", "status"}}})
	f.cloneCheckout()
	fake := startFake()
	stateRoot := t.TempDir()
	var friction []string
	s := startStarter(t, f, fake, stateRoot, &friction)

	afterWorkspaceCreate = func() { panic("simulated crash after workspace create") }
	t.Cleanup(func() { afterWorkspaceCreate = nil })
	// Recover inside a nested function so the assertions below run after
	// the simulated crash.
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("the crash hook did not panic")
			}
		}()
		s.Start(startRequest("main", briefA))
	}()
	afterWorkspaceCreate = nil

	// The job is left preparing, with the run facts but no workspace id.
	store := Open(stateRoot)
	snap, err := store.Snapshot("job-1")
	if err != nil {
		t.Fatal(err)
	}
	if snap.State.Status != StatusPreparing {
		t.Fatalf("crashed job status = %q", snap.State.Status)
	}
	if snap.State.Checkout == "" || snap.State.Dir == "" || snap.State.WorkspaceID != "" {
		t.Fatalf("crashed job state = %+v", snap.State)
	}

	// The retried Start recovers the crashed start: the duplicate returns
	// the failed state with the recovery motivo, the recovered workspace
	// is closed, and no second workspace is created.
	fake.Workspaces = []herdrWorkspace{
		{ID: "w9", Label: "job-job-1", PaneCount: 1},
	}
	second, _, err := s.Start(startRequest("main", briefA))
	if err != nil {
		t.Fatalf("retry Start: %v", err)
	}
	if second.DuplicateOf != "job-1" || second.Status != StatusFailed ||
		second.Motivo == nil || *second.Motivo != "job: start interrupted" {
		t.Fatalf("retry = %+v", second)
	}
	if second.WorkspaceID != "w9" || !second.WorkspaceClosed {
		t.Fatalf("retry workspace = %q closed %v, want w9 / true", second.WorkspaceID, second.WorkspaceClosed)
	}
	if len(fake.CreateCalls) != 1 || len(fake.RunCalls) != 0 {
		t.Fatalf("CreateCalls = %d RunCalls = %d, want 1 and 0", len(fake.CreateCalls), len(fake.RunCalls))
	}
}

// TestStartStorageFailureAfterRecordFails: a storage error after the job is
// recorded (here the preparing event append) still ends the job failed with
// a report, and no workspace is created.
func TestStartStorageFailureAfterRecordFails(t *testing.T) {
	f := newPrepareFixture(t, []fakecli.Rule{{Argv: []string{"auth", "status"}}})
	f.cloneCheckout()
	fake := startFake()
	stateRoot := t.TempDir()
	var friction []string
	s := startStarter(t, f, fake, stateRoot, &friction)

	appendEventFn = func(dir string, now time.Time, in EventIn) (Event, error) {
		if in.Tipo == "preparing" {
			return Event{}, errors.New("simulated disk failure")
		}
		return appendEvent(dir, now, in)
	}
	t.Cleanup(func() { appendEventFn = appendEvent })

	if _, _, err := s.Start(startRequest("main", briefA)); err == nil {
		t.Fatal("Start succeeded despite the storage failure")
	}
	snap, err := Open(stateRoot).Snapshot("job-1")
	if err != nil {
		t.Fatal(err)
	}
	if snap.State.Status != StatusFailed || snap.State.Motivo == nil {
		t.Fatalf("state = %+v, want failed with a motivo", snap.State)
	}
	if _, err := os.Stat(filepath.Join(stateRoot, "jobs", "job-1", "report.json")); err != nil {
		t.Fatalf("report.json: %v", err)
	}
	if len(fake.CreateCalls) != 0 {
		t.Fatalf("CreateCalls = %d, want 0", len(fake.CreateCalls))
	}
}

func TestStartRejectsActorRoleKeysThatReplaceTeamModelRotation(t *testing.T) {
	for _, tc := range []struct {
		name, model string
	}{
		{"no_override", ""},
		{"equal_scalar", "m1"},
		{"different_scalar", "m3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPrepareFixture(t, []fakecli.Rule{{Argv: []string{"auth", "status"}}})
			f.env["USERPROFILE"], f.env["TMPDIR"] = f.env["HOME"], t.TempDir()
			stateRoot := filepath.Join(f.checkout(), ".soho")
			if tc.model != "" {
				user := platform.UserConfigPath(platform.Current(), f.env)
				if err := os.MkdirAll(filepath.Dir(user), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(user, []byte("role.implementer.model="+tc.model+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			fake := startFake()
			var friction []string
			starter := startStarter(t, f, fake, stateRoot, &friction)
			req := startRequest("main", briefA)
			req.Team.Pairs = append(req.Team.Pairs, TeamPair{"lane.build.kind", "grok"}, TeamPair{"lane.build.models", "m1,m2"}, TeamPair{"model_cap", "1"})
			st, store, err := starter.Start(req)
			if tc.model != "" {
				want := "job: role.implementer.model in user config overrides the team's lane.build.model; remove it or set the override in the team"
				var refusal *ExitError
				if !errors.As(err, &refusal) || refusal.Code != ExitUsage || refusal.Msg != want {
					t.Fatalf("Start error=%v, want exit %d and %q", err, ExitUsage, want)
				}
				if len(fake.CreateCalls) != 0 || len(fake.RunCalls) != 0 {
					t.Fatalf("refusal launched a workspace or supervisor: creates=%v runs=%v", fake.CreateCalls, fake.RunCalls)
				}
				return
			}
			if err != nil || store == nil || st.Status != StatusRunning || len(fake.CreateCalls) != 1 || len(fake.RunCalls) != 1 {
				t.Fatalf("start state=%q err=%v creates=%v runs=%v", st.Status, err, fake.CreateCalls, fake.RunCalls)
			}
			env := f.env.Clone()
			env["HERDR_WORKSPACE_ID"] = fake.CreateWorkspaceID
			if _, err := fakecli.Install(t, f.fakeDir, "herdr", []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"name":"worker","agent_status":"working"}}}`}}); err != nil {
				t.Fatal(err)
			}
			other := filepath.Join(stateRoot, "other")
			if err := os.MkdirAll(other, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(other, "agents.tsv"), []byte("worker\tp1\tgrok\timplementer\txai\t1\t"+st.Dir+"\tnow\tm1\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			sup := Supervisor{Store: store, ID: req.ID, PrivateLaunch: true, actorEnv: env, Friction: func(msg string) { friction = append(friction, msg) }}
			if err := sup.pinActorSession(); err != nil {
				t.Fatalf("accepted team did not pass actor parity: %v (%v)", err, friction)
			}
			ctx := core.LoadConfig(env, st.Dir)
			res := core.ResolveRoleSettings("implementer", &ctx, env, st.Dir, core.RoleFlags{})
			if got := core.LiveModelCounts(&ctx, env, st.Dir)["m1"]; got != 1 {
				t.Fatalf("fixture must saturate m1, count=%d, settings=%+v", got, res)
			}
			model, choice := spawn.ChooseSpawnModel("", res, &ctx, env, st.Dir, res.Kind, res.Effort, func(msg string, _ *core.Config, _ platform.Env, _ string) { t.Error(msg) })
			if model != "m2" || choice != "list" {
				t.Fatalf("rotation picked %q (%s), settings=%+v", model, choice, res)
			}
		})
	}
}
