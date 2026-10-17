package job

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/platform"
)

// fakeTeam is the test-only teamOps: scripted orchestrator states and
// recorded calls, so the supervisor tests run without a subprocess.
type fakeDispatch struct {
	Name  string
	Path  string
	Amend bool
}

type fakeStatus struct {
	State  string
	Report string
	Err    error
}

type fakeSend struct {
	Name string
	Path string
}

type fakeTeam struct {
	ensureCalls      int
	ensureErr        error
	spawnCalls       int
	spawnName        string
	spawnErr         error
	spawnPanic       bool // panics once, after counting: a crash between spawn and Record
	dispatches       []fakeDispatch
	dispatchErr      error
	sends            []fakeSend
	sendErr          error
	statusScript     []fakeStatus
	statusCalls      int
	releaseCalls     int
	releaseTeamCalls int
	gcCalls          int
	// releaseErr, releaseTeamErr and gcErr, when set, are returned by the
	// release-family calls so the finalize release gate can be driven: the
	// cleanup event lands only on a pass whose release ended clean.
	releaseErr     error
	releaseTeamErr error
	gcErr          error
	rosterRows     []rosterRow
	rosterErr      error
	rosterCalls    int
	// recoveryStatus is the one-shot per-name status the recovery candidate
	// read consumes: the watch-loop script is not consumed by that read, and
	// a second read for the same name falls back to the script.
	recoveryStatus map[string]fakeStatus
	// deliveryCtx is the platform context the supervisor's interruptible
	// adapter handed the in-flight delivery (nil when the delivery ran
	// without one).
	deliveryCtx context.Context
	// bounds records the limit each bounded delivery adapter received, in
	// call order.
	bounds []time.Duration
	// statusBounds records the limit of every bounded Status read.
	statusBounds []time.Duration
	// stuckDispatchCall and stuckSendCall name the 1-based delivery call
	// (the brief's dispatch is call 1) that closes entered and waits for
	// the delivery context to be cancelled before failing: the scripted
	// stuck delivery. With no context (a supervisor without the
	// interruptible adapter) it fails at once.
	stuckDispatchCall int
	stuckSendCall     int
	entered           chan struct{}
	// unblockedAt is when the last stuck delivery came back.
	unblockedAt time.Time
	// dispatchCut and sendCut, when set, replace the plain delivery errors
	// with the scripted cut, which may advance the shared clock past the
	// stop deadline before it returns.
	dispatchCut func() error
	sendCut     func() error
}

var _ teamOps = (*fakeTeam)(nil)

func (f *fakeTeam) EnsureJobLane() error {
	f.ensureCalls++
	return f.ensureErr
}

func (f *fakeTeam) SpawnOrchestrator() (string, error) {
	f.spawnCalls++
	if f.spawnPanic {
		f.spawnPanic = false
		panic(crashBetweenSpawnAndRecord)
	}
	if f.spawnErr != nil {
		return "", f.spawnErr
	}
	return f.spawnName, nil
}

func (f *fakeTeam) Dispatch(name, briefPath string, amend bool) error {
	f.dispatches = append(f.dispatches, fakeDispatch{Name: name, Path: briefPath, Amend: amend})
	if f.stuckDispatchCall > 0 && len(f.dispatches) == f.stuckDispatchCall {
		f.stuckDispatchCall = 0
		close(f.entered)
		if f.deliveryCtx != nil {
			<-f.deliveryCtx.Done()
			f.unblockedAt = time.Now()
		}
		return errors.New("the delivery context was cancelled")
	}
	if f.dispatchCut != nil {
		return f.dispatchCut()
	}
	return f.dispatchErr
}

func (f *fakeTeam) Send(name, path string) error {
	f.sends = append(f.sends, fakeSend{Name: name, Path: path})
	if f.stuckSendCall > 0 && len(f.sends) == f.stuckSendCall {
		f.stuckSendCall = 0
		close(f.entered)
		if f.deliveryCtx != nil {
			<-f.deliveryCtx.Done()
			f.unblockedAt = time.Now()
		}
		return errors.New("the delivery context was cancelled")
	}
	if f.sendCut != nil {
		return f.sendCut()
	}
	return f.sendErr
}

func (f *fakeTeam) Status(name string) (string, string, error) {
	if next, ok := f.recoveryStatus[name]; ok {
		delete(f.recoveryStatus, name)
		return next.State, next.Report, next.Err
	}
	f.statusCalls++
	if len(f.statusScript) == 0 {
		panic("fakeTeam: no scripted status")
	}
	next := f.statusScript[0]
	if len(f.statusScript) > 1 {
		f.statusScript = f.statusScript[1:]
	}
	return next.State, next.Report, next.Err
}

func (f *fakeTeam) Release(name string) error {
	f.releaseCalls++
	return f.releaseErr
}

func (f *fakeTeam) ReleaseTeam() error {
	f.releaseTeamCalls++
	return f.releaseTeamErr
}

func (f *fakeTeam) GC() error {
	f.gcCalls++
	return f.gcErr
}

func (f *fakeTeam) Roster() ([]rosterRow, error) {
	f.rosterCalls++
	if f.rosterErr != nil {
		return nil, f.rosterErr
	}
	return f.rosterRows, nil
}

// bounded and interruptible are the supervisor's delivery adapters over
// the fake: bounded records the limit the adapter received, and
// interruptible records the delivery context, so the scripted deliveries
// observe the very values the supervisor used.
func (f *fakeTeam) bounded(d time.Duration) teamOps {
	return &boundFakeTeam{fakeTeam: f, inner: f, bound: d}
}

func (f *fakeTeam) interruptible(ctx context.Context) teamOps {
	return &ctxFakeTeam{fakeTeam: f, inner: f, ctx: ctx}
}

// boundFakeTeam records the limit and hands the delivery to the ops that
// owned it (the fake itself, or a wrapper over it).
type boundFakeTeam struct {
	*fakeTeam
	inner teamOps
	bound time.Duration
}

func (w *boundFakeTeam) Dispatch(name, briefPath string, amend bool) error {
	w.fakeTeam.bounds = append(w.fakeTeam.bounds, w.bound)
	return w.inner.Dispatch(name, briefPath, amend)
}

func (w *boundFakeTeam) Send(name, path string) error {
	w.fakeTeam.bounds = append(w.fakeTeam.bounds, w.bound)
	return w.inner.Send(name, path)
}

func (w *boundFakeTeam) Status(name string) (string, string, error) {
	w.fakeTeam.statusBounds = append(w.fakeTeam.statusBounds, w.bound)
	return w.inner.Status(name)
}

// ctxFakeTeam records the delivery context and hands the delivery to the
// ops that owned it.
type ctxFakeTeam struct {
	*fakeTeam
	inner teamOps
	ctx   context.Context
}

func (w *ctxFakeTeam) Dispatch(name, briefPath string, amend bool) error {
	w.fakeTeam.deliveryCtx = w.ctx
	return w.inner.Dispatch(name, briefPath, amend)
}

func (w *ctxFakeTeam) Send(name, path string) error {
	w.fakeTeam.deliveryCtx = w.ctx
	return w.inner.Send(name, path)
}

// crashBetweenSpawnAndRecord is the sentinel panic of the crash-window test.
var crashBetweenSpawnAndRecord = errors.New("crash between spawn and Record")

// fakeClock drives the supervisor pauses. rewind moves the wall clock
// backwards on every sleep, to prove the start wait counts sleeps, not
// wall-clock differences; stopAfter panics after that many sleeps to stop
// a still-running watch loop.
type fakeClock struct {
	now       time.Time
	sleeps    int
	rewind    bool
	stopAfter int
	stop      error
	onSleep   func()
}

func (c *fakeClock) Now() time.Time   { return c.now }
func (c *fakeClock) Monotonic() int64 { return int64(c.sleeps) }
func (c *fakeClock) Sleep(d time.Duration) {
	c.sleeps++
	if c.onSleep != nil {
		c.onSleep()
	}
	c.now = c.now.Add(d)
	if c.rewind {
		c.now = c.now.Add(-10000 * time.Hour)
	}
	if c.stopAfter > 0 && c.sleeps >= c.stopAfter {
		panic(c.stop)
	}
}

const supTestBrief = `{"schema":1,"id":"job-1","origem":{"tipo":"card","ref":"CARD-1"},"repo":"example-org/example-repo","objetivo":"Ship the change.","aceite":[{"criterio":"tests pass","prova":"go test ./internal/job"}]}`

// supFixture records a running job with a worktree directory and returns
// the store, the job dir, the shared fake clock and the fake team.
func supFixture(t *testing.T) (*Store, string, *fakeClock, *fakeTeam) {
	t.Helper()
	root := t.TempDir()
	clock := &fakeClock{now: time.Date(2026, 10, 9, 10, 0, 0, 0, time.FixedZone("-03:00", -3*3600))}
	store := Open(root)
	store.Clock = clock
	if _, err := store.Start("job-1", []byte(supTestBrief)); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if _, err := store.Transition("job-1", StatusPreparing); err != nil {
		t.Fatal(err)
	}
	// The run facts a started job has: the worktree and the branch facts.
	if _, err := store.Record("job-1", func(st *State) { st.Dir = dir; st.Branch = "job/job-1"; st.Base = "main" }); err != nil {
		t.Fatal(err)
	}
	// The job's Herdr workspace, so the terminal release and close steps
	// have a target.
	if _, err := store.Record("job-1", func(st *State) { st.WorkspaceID = "w1" }); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transition("job-1", StatusRunning); err != nil {
		t.Fatal(err)
	}
	jobDir := filepath.Join(root, "jobs", "job-1")
	return store, jobDir, clock, &fakeTeam{spawnName: "orch-1"}
}

// supSupervisor wires a Supervisor over the fixture store with a zero Poll
// (the 5 s default).
func supSupervisor(store *Store, clock *fakeClock, team *fakeTeam, friction *[]string) *Supervisor {
	return &Supervisor{
		Store:    store,
		ID:       "job-1",
		Ops:      team,
		Clock:    clock,
		Friction: func(message string) { *friction = append(*friction, message) },
	}
}

func writeOrchestratorReport(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "report.md")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func eventTipes(events []Event) []string {
	out := make([]string, 0, len(events))
	for _, event := range events {
		out = append(out, event.Tipo)
	}
	return out
}

func supState(t *testing.T, store *Store) State {
	t.Helper()
	snap, err := store.Snapshot("job-1")
	if err != nil {
		t.Fatal(err)
	}
	return snap.State
}

func TestSuperviseHappyPath(t *testing.T) {
	store, jobDir, clock, team := supFixture(t)
	report := writeOrchestratorReport(t, "# Report — job\n\n- Item 1 [done] did the thing\n")
	team.statusScript = []fakeStatus{{State: "working"}, {State: "working"}, {State: "done", Report: report}}
	var friction []string
	sup := supSupervisor(store, clock, team, &friction)

	exit, err := sup.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if exit != 0 {
		t.Fatalf("exit = %d, want 0", exit)
	}
	if team.ensureCalls != 1 || team.spawnCalls != 1 {
		t.Fatalf("ensure=%d spawn=%d, want 1/1", team.ensureCalls, team.spawnCalls)
	}
	if len(team.dispatches) != 1 || team.dispatches[0] != (fakeDispatch{Name: "orch-1", Path: filepath.Join(jobDir, "orchestrator-brief.md"), Amend: false}) {
		t.Fatalf("dispatches = %+v", team.dispatches)
	}
	// The orchestrator brief is the job brief plus the exact section.
	brief, err := os.ReadFile(filepath.Join(jobDir, "brief.md"))
	if err != nil {
		t.Fatal(err)
	}
	wantBrief := append(append([]byte{}, brief...), []byte("\n## Job\n\n- Job id: job-1\n- Publish decisions, questions, checkpoints and notes with `soho job note --id job-1 --tipo <type> ...`.\n- Ask for an immediate push after a commit with `soho job checkpoint --id job-1`.\n")...)
	gotBrief, err := os.ReadFile(filepath.Join(jobDir, "orchestrator-brief.md"))
	if err != nil {
		t.Fatalf("orchestrator brief: %v", err)
	}
	if string(gotBrief) != string(wantBrief) {
		t.Fatalf("orchestrator brief = %q, want %q", gotBrief, wantBrief)
	}
	mode, err := os.Stat(filepath.Join(jobDir, "orchestrator-brief.md"))
	if err != nil {
		t.Fatalf("orchestrator brief: %v", err)
	}
	// Windows reports 0666 for a writable file; the 0600 contract is the
	// POSIX one.
	if runtime.GOOS != "windows" && mode.Mode().Perm() != 0o600 {
		t.Fatalf("orchestrator brief mode = %o, want 0600", mode.Mode().Perm())
	}
	// The report was copied into the job dir, byte for byte.
	original, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	copied, err := os.ReadFile(filepath.Join(jobDir, "report.md"))
	if err != nil || string(copied) != string(original) {
		t.Fatalf("report.md = %q, want the orchestrator's bytes", copied)
	}
	st := supState(t, store)
	if st.Status != StatusDone || st.Orchestrator != "orch-1" || st.DispatchedAt == "" {
		t.Fatalf("state = %+v", st)
	}
	if _, err := time.Parse(time.RFC3339, st.DispatchedAt); err != nil {
		t.Fatalf("dispatched_at %q is not RFC 3339: %v", st.DispatchedAt, err)
	}
	if strings.HasSuffix(st.DispatchedAt, "Z") {
		t.Fatalf("dispatched_at %q lacks an explicit offset", st.DispatchedAt)
	}
	events, err := readEvents(jobDir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"accepted", "worker_spawned", "worker_done", "terminal", "cleanup"}
	if got := eventTipes(events); !reflect.DeepEqual(got, want) {
		t.Fatalf("event types = %v, want %v", got, want)
	}
	spawned := events[1]
	if spawned.Resumo != "job orchestrator spawned" || spawned.Refs["agente"] != "orch-1" || spawned.Refs["papel"] != "job-orchestrator" {
		t.Fatalf("worker_spawned = %+v", spawned)
	}
	done := events[2]
	if done.Resumo != "job orchestrator reported, 0 partial" || done.Refs["agente"] != "orch-1" || done.Refs["report"] != filepath.Join(jobDir, "report.md") {
		t.Fatalf("worker_done = %+v", done)
	}
	terminal := events[3]
	if terminal.Refs["exit"] != "0" {
		t.Fatalf("terminal refs = %+v", terminal.Refs)
	}
	if len(terminal.Refs) != 1 {
		t.Fatalf("terminal refs = %+v, want only exit", terminal.Refs)
	}
	if clock.sleeps != 2 {
		t.Fatalf("sleeps = %d, want 2 (the two working ticks)", clock.sleeps)
	}
	if len(friction) != 0 {
		t.Fatalf("friction = %v", friction)
	}
	rep, err := os.ReadFile(filepath.Join(jobDir, "report.json"))
	if err != nil {
		t.Fatal("report.json missing")
	}
	if !strings.Contains(string(rep), `"status":"done"`) || !strings.Contains(string(rep), `"parciais":0`) {
		t.Fatalf("report.json = %s", rep)
	}
}

func TestSupervisePartialsNoPendingFails(t *testing.T) {
	store, jobDir, clock, team := supFixture(t)
	report := writeOrchestratorReport(t, "# Report — job\n\n- [partial] Item 1 — the gate was not run\n")
	team.statusScript = []fakeStatus{{State: "done", Report: report}}
	var friction []string
	sup := supSupervisor(store, clock, team, &friction)

	exit, err := sup.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if exit != ExitFailed {
		t.Fatalf("exit = %d, want %d", exit, ExitFailed)
	}
	st := supState(t, store)
	if st.Status != StatusFailed || st.Motivo == nil || *st.Motivo != "itens parciais" {
		t.Fatalf("state = %+v", st)
	}
	events, err := readEvents(jobDir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"accepted", "worker_spawned", "worker_done", "terminal", "cleanup"}
	if got := eventTipes(events); !reflect.DeepEqual(got, want) {
		t.Fatalf("event types = %v, want %v", got, want)
	}
	done := events[2]
	if done.Resumo != "job orchestrator reported, 1 partial" {
		t.Fatalf("worker_done resumo = %q", done.Resumo)
	}
	terminal := events[3]
	if terminal.Refs["exit"] != "19" || terminal.Refs["motivo"] != "itens parciais" {
		t.Fatalf("terminal refs = %+v", terminal.Refs)
	}
	rep, err := os.ReadFile(filepath.Join(jobDir, "report.json"))
	if err != nil {
		t.Fatal("report.json missing")
	}
	if !strings.Contains(string(rep), `"status":"failed"`) || !strings.Contains(string(rep), `"parciais":1`) || !strings.Contains(string(rep), `"motivo":"itens parciais"`) {
		t.Fatalf("report.json = %s", rep)
	}
}

func TestSupervisePartialsPendingDecisionBlocksAndLoops(t *testing.T) {
	store, jobDir, clock, team := supFixture(t)
	if _, err := store.Note("job-1", EventIn{Tipo: "decision", Escopo: "global", Resumo: "keep the queue"}); err != nil {
		t.Fatal(err)
	}
	report := writeOrchestratorReport(t, "# Report — job\n\n- [partial] Item 1 — the gate was not run\n")
	team.statusScript = []fakeStatus{{State: "done", Report: report}}
	clock.stopAfter = 3
	clock.stop = errors.New("stop the watch loop")
	var friction []string
	sup := supSupervisor(store, clock, team, &friction)

	// The outcome derives once and the loop keeps running until the fake
	// clock stops it.
	func() {
		defer func() {
			r := recover()
			if r == nil {
				t.Error("the watch loop ended before the stop: it stopped looping")
				return
			}
			if !errors.Is(r.(error), clock.stop) {
				panic(r)
			}
		}()
		if _, err := sup.Run(); err != nil {
			t.Errorf("Run: %v", err)
		}
	}()

	st := supState(t, store)
	if st.Status != StatusBlocked || st.Motivo == nil || *st.Motivo != "decisions pending" {
		t.Fatalf("state = %+v", st)
	}
	events, err := readEvents(jobDir)
	if err != nil {
		t.Fatal(err)
	}
	// The outcome was derived once: the loop kept running (three ticks,
	// three sleeps) but added no event again.
	if got := eventTipes(events); !reflect.DeepEqual(got, []string{"accepted", "decision", "worker_spawned", "worker_done", "blocked"}) {
		t.Fatalf("event types = %v", got)
	}
	if team.statusCalls != 3 {
		t.Fatalf("status calls = %d, want 3 (the loop kept running)", team.statusCalls)
	}
	if clock.sleeps != 3 {
		t.Fatalf("sleeps = %d, want 3", clock.sleeps)
	}
	if _, err := os.Stat(filepath.Join(jobDir, "report.json")); !os.IsNotExist(err) {
		t.Fatalf("a blocked job publishes no report.json: %v", err)
	}
	blocked := events[len(events)-1]
	if blocked.Refs["agente"] != "orch-1" || blocked.Refs["motivo"] != "decisions pending" {
		t.Fatalf("blocked refs = %+v", blocked.Refs)
	}
}

func TestSuperviseQuestionThenWorking(t *testing.T) {
	store, jobDir, clock, team := supFixture(t)
	report := writeOrchestratorReport(t, "# Report — job\n\n- Item 1 [done] did the thing\n")
	team.statusScript = []fakeStatus{{State: "question"}, {State: "working"}, {State: "done", Report: report}}
	var friction []string
	sup := supSupervisor(store, clock, team, &friction)

	exit, err := sup.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if exit != 0 {
		t.Fatalf("exit = %d, want 0", exit)
	}
	events, err := readEvents(jobDir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"accepted", "worker_spawned", "blocked", "unblocked", "worker_done", "terminal", "cleanup"}
	if got := eventTipes(events); !reflect.DeepEqual(got, want) {
		t.Fatalf("event types = %v, want %v", got, want)
	}
	blocked := events[2]
	if blocked.Refs["agente"] != "orch-1" || blocked.Refs["motivo"] != "question" {
		t.Fatalf("blocked refs = %+v", blocked.Refs)
	}
	if st := supState(t, store); st.Motivo != nil {
		t.Fatalf("the unblock clears the stored motivo: %q", *st.Motivo)
	}
}

func TestSuperviseGoneThreeTicks(t *testing.T) {
	store, jobDir, clock, team := supFixture(t)
	team.statusScript = []fakeStatus{{State: "gone"}, {State: "gone"}, {State: "gone"}}
	var friction []string
	sup := supSupervisor(store, clock, team, &friction)

	exit, err := sup.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if exit != ExitFailed {
		t.Fatalf("exit = %d, want %d", exit, ExitFailed)
	}
	st := supState(t, store)
	if st.Status != StatusFailed || st.Motivo == nil || *st.Motivo != "job: the job orchestrator ended without a report" {
		t.Fatalf("state = %+v", st)
	}
	events, err := readEvents(jobDir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"accepted", "worker_spawned", "failure", "terminal", "cleanup"}
	if got := eventTipes(events); !reflect.DeepEqual(got, want) {
		t.Fatalf("event types = %v, want %v", got, want)
	}
	if terminal := events[3]; terminal.Refs["exit"] != "19" {
		t.Fatalf("terminal refs = %+v", terminal.Refs)
	}
}

func TestSuperviseGoneTwoThenWorking(t *testing.T) {
	store, jobDir, clock, team := supFixture(t)
	report := writeOrchestratorReport(t, "# Report — job\n\n- Item 1 [done] did the thing\n")
	team.statusScript = []fakeStatus{{State: "gone"}, {State: "gone"}, {State: "working"}, {State: "done", Report: report}}
	var friction []string
	sup := supSupervisor(store, clock, team, &friction)

	exit, err := sup.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if exit != 0 {
		t.Fatalf("exit = %d, want 0", exit)
	}
	if st := supState(t, store); st.Status != StatusDone {
		t.Fatalf("state = %+v", st)
	}
	if events, err := readEvents(jobDir); err != nil {
		t.Fatal(err)
	} else if got := eventTipes(events); !reflect.DeepEqual(got, []string{"accepted", "worker_spawned", "worker_done", "terminal", "cleanup"}) {
		t.Fatalf("event types = %v", got)
	}
}

// A working observation between gone ones drops the gone streak: the
// orchestrator came back, so the three-consecutive-ticks count restarts
// and the job is still running when the loop is stopped.
func TestSuperviseGoneStreakDropsOnWorking(t *testing.T) {
	store, jobDir, clock, team := supFixture(t)
	team.statusScript = []fakeStatus{{State: "gone"}, {State: "gone"}, {State: "working"}, {State: "gone"}, {State: "gone"}}
	clock.stopAfter = 5
	clock.stop = errors.New("stop the watch loop")
	var friction []string
	sup := supSupervisor(store, clock, team, &friction)

	func() {
		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("the watch loop ended before the stop: the streak failed the job early")
				return
			}
			if !errors.Is(r.(error), clock.stop) {
				panic(r)
			}
		}()
		if _, err := sup.Run(); err != nil {
			t.Fatalf("Run: %v", err)
		}
	}()
	if st := supState(t, store); st.Status != StatusRunning {
		t.Fatalf("state = %+v", st)
	}
	events, err := readEvents(jobDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := eventTipes(events); !reflect.DeepEqual(got, []string{"accepted", "worker_spawned"}) {
		t.Fatalf("event types = %v", got)
	}
}

func TestSuperviseStatusErrorThreeTicks(t *testing.T) {
	store, jobDir, clock, team := supFixture(t)
	statusErr := errors.New("status failed")
	team.statusScript = []fakeStatus{{Err: statusErr}, {Err: statusErr}, {Err: statusErr}}
	var friction []string
	sup := supSupervisor(store, clock, team, &friction)

	exit, err := sup.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if exit != ExitFailed {
		t.Fatalf("exit = %d, want %d", exit, ExitFailed)
	}
	st := supState(t, store)
	if st.Status != StatusFailed || st.Motivo == nil || *st.Motivo != "job: the job orchestrator ended without a report" {
		t.Fatalf("state = %+v", st)
	}
	if events, err := readEvents(jobDir); err != nil {
		t.Fatal(err)
	} else if got := eventTipes(events); !reflect.DeepEqual(got, []string{"accepted", "worker_spawned", "failure", "terminal", "cleanup"}) {
		t.Fatalf("event types = %v", got)
	}
}

func TestSuperviseRestartSpawnsAndDispatchesNothing(t *testing.T) {
	store, jobDir, clock, team := supFixture(t)
	// The previous run recorded the orchestrator and the dispatch.
	if _, err := store.Record("job-1", func(st *State) {
		st.Orchestrator = "orch-9"
		st.DispatchedAt = "2026-10-09T09:00:00-03:00"
	}); err != nil {
		t.Fatal(err)
	}
	report := writeOrchestratorReport(t, "# Report — job\n\n- Item 1 [done] did the thing\n")
	team.statusScript = []fakeStatus{{State: "done", Report: report}}
	var friction []string
	sup := supSupervisor(store, clock, team, &friction)

	exit, err := sup.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if exit != 0 {
		t.Fatalf("exit = %d, want 0", exit)
	}
	// The lane (and any max_workers bump) was set before the orchestrator
	// was recorded, so a restart does not set it again.
	if team.ensureCalls != 0 {
		t.Fatalf("ensure calls = %d, want 0 on a restart", team.ensureCalls)
	}
	if team.spawnCalls != 0 || len(team.dispatches) != 0 {
		t.Fatalf("spawn=%d dispatches=%v, want nothing (a restart reuses)", team.spawnCalls, team.dispatches)
	}
	// The recorded name is the one watched, and the orchestrator brief is
	// not rewritten.
	if events, err := readEvents(jobDir); err != nil {
		t.Fatal(err)
	} else if got := eventTipes(events); !reflect.DeepEqual(got, []string{"accepted", "worker_done", "terminal", "cleanup"}) {
		t.Fatalf("event types = %v, want no worker_spawned on a restart", got)
	}
	if _, err := os.Stat(filepath.Join(jobDir, "orchestrator-brief.md")); !os.IsNotExist(err) {
		t.Fatalf("a restart must not rewrite the orchestrator brief: %v", err)
	}
	if st := supState(t, store); st.Status != StatusDone {
		t.Fatalf("state = %+v", st)
	}
}

func TestSuperviseCrashBetweenSpawnAndRecord(t *testing.T) {
	store, jobDir, clock, team := supFixture(t)
	team.spawnPanic = true
	// The crashed spawn left this orchestrator alive in the job's own
	// workspace: the restarted run adopts it instead of spawning a second
	// one.
	team.rosterRows = []rosterRow{{Name: "orch-1", Role: "job-orchestrator", Pane: "w1:p2"}}
	team.recoveryStatus = map[string]fakeStatus{"orch-1": {State: "working"}}
	report := writeOrchestratorReport(t, "# Report — job\n\n- Item 1 [done] did the thing\n")
	team.statusScript = []fakeStatus{{State: "done", Report: report}}
	var friction []string

	// First run: the crash leaves the spawn intent without the name.
	sup := supSupervisor(store, clock, team, &friction)
	func() {
		defer func() {
			if r := recover(); !errors.Is(r.(error), crashBetweenSpawnAndRecord) {
				panic(r)
			}
		}()
		if _, err := sup.Run(); err != nil {
			t.Errorf("first Run: %v", err)
		}
	}()
	if st := supState(t, store); st.OrchestratorSpawnAt == "" || st.Orchestrator != "" || st.Status != StatusRunning {
		t.Fatalf("after the crash the state = %+v", st)
	}

	// Second run: it adopts the orchestrator and converges to done without
	// spawning again.
	sup = supSupervisor(store, clock, team, &friction)
	exit, err := sup.Run()
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if exit != 0 {
		t.Fatalf("exit = %d, want 0", exit)
	}
	if team.spawnCalls != 1 {
		t.Fatalf("spawn calls = %d, want 1: the restart adopts the spawned orchestrator", team.spawnCalls)
	}
	if len(team.dispatches) != 1 {
		t.Fatalf("dispatches = %v, want exactly one", team.dispatches)
	}
	if team.ensureCalls != 1 {
		t.Fatalf("ensure calls = %d, want 1 across the crash and the restart", team.ensureCalls)
	}
	if st := supState(t, store); st.Status != StatusDone || st.Orchestrator != "orch-1" {
		t.Fatalf("state = %+v", st)
	}
	events, err := readEvents(jobDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := eventTipes(events); !reflect.DeepEqual(got, []string{"accepted", "worker_spawned", "worker_done", "terminal", "cleanup"}) {
		t.Fatalf("event types = %v", got)
	}
	if n := countTipe(events, "worker_spawned"); n != 1 {
		t.Fatalf("worker_spawned count = %d, want exactly one (the adoption events once)", n)
	}
	if spawned := events[1]; spawned.Refs["agente"] != "orch-1" || spawned.Refs["papel"] != "job-orchestrator" {
		t.Fatalf("worker_spawned = %+v, want the adopted name and role", spawned)
	}
}

func TestSuperviseStartWait(t *testing.T) {
	t.Run("a job still not running fails after the 60 s of polls", func(t *testing.T) {
		store, jobDir, clock, team := supFixture(t)
		// Put the job back to accepted: the start transition has not run.
		if err := os.WriteFile(filepath.Join(jobDir, "state.json"), []byte(`{"schema":1,"id":"job-1","status":"accepted","brief_sha256":"x","decisions_acked_seq":0,"motivo":null}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		var friction []string
		sup := supSupervisor(store, clock, team, &friction)
		exit, err := sup.Run()
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if exit != ExitFailed {
			t.Fatalf("exit = %d, want %d", exit, ExitFailed)
		}
		// 60 s at the 5 s default poll: twelve sleeps, no wall clock read.
		if clock.sleeps != 12 {
			t.Fatalf("sleeps = %d, want 12", clock.sleeps)
		}
		if st := supState(t, store); st.Status != StatusFailed || st.Motivo == nil || *st.Motivo != "job: supervisor started before the job was running" {
			t.Fatalf("state = %+v", st)
		}
		if team.spawnCalls != 0 || team.ensureCalls != 0 {
			t.Fatalf("spawn=%d ensure=%d, want nothing after the wait fails", team.spawnCalls, team.ensureCalls)
		}
	})

	t.Run("a clock that goes backwards does not end the wait early", func(t *testing.T) {
		store, jobDir, clock, team := supFixture(t)
		clock.rewind = true
		if err := os.WriteFile(filepath.Join(jobDir, "state.json"), []byte(`{"schema":1,"id":"job-1","status":"preparing","brief_sha256":"x","decisions_acked_seq":0,"motivo":null}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		var friction []string
		sup := supSupervisor(store, clock, team, &friction)
		exit, err := sup.Run()
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if exit != ExitFailed {
			t.Fatalf("exit = %d, want %d", exit, ExitFailed)
		}
		if clock.sleeps != 12 {
			t.Fatalf("sleeps = %d, want 12: the wait counts sleeps, not wall-clock differences", clock.sleeps)
		}
	})

	t.Run("a terminal state during the wait returns its outcome code", func(t *testing.T) {
		store, jobDir, clock, team := supFixture(t)
		if err := os.WriteFile(filepath.Join(jobDir, "state.json"), []byte(`{"schema":1,"id":"job-1","status":"canceled","brief_sha256":"x","decisions_acked_seq":0,"motivo":null}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		var friction []string
		sup := supSupervisor(store, clock, team, &friction)
		exit, err := sup.Run()
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if exit != ExitCanceled {
			t.Fatalf("exit = %d, want %d", exit, ExitCanceled)
		}
		if clock.sleeps != 0 || team.spawnCalls != 0 {
			t.Fatalf("sleeps=%d spawn=%d, want nothing: the terminal state ends the wait at once", clock.sleeps, team.spawnCalls)
		}
	})

	t.Run("the wait ends when the job starts running", func(t *testing.T) {
		store, jobDir, clock, team := supFixture(t)
		if err := os.WriteFile(filepath.Join(jobDir, "state.json"), []byte(`{"schema":1,"id":"job-1","status":"accepted","brief_sha256":"x","decisions_acked_seq":0,"motivo":null}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		report := writeOrchestratorReport(t, "# Report — job\n\n- Item 1 [done] did the thing\n")
		team.statusScript = []fakeStatus{{State: "done", Report: report}}
		// The start lands two polls in.
		clock.onSleep = func() {
			if clock.sleeps == 2 {
				// The worktree path is escaped into the JSON with %q: a raw
				// splice would leave backslashes that are not valid JSON.
				body := fmt.Sprintf(`{"schema":1,"id":"job-1","status":"running","brief_sha256":"x","decisions_acked_seq":0,"motivo":null,"dir":%q}`+"\n", t.TempDir())
				if err := os.WriteFile(filepath.Join(jobDir, "state.json"), []byte(body), 0o600); err != nil {
					panic(err)
				}
			}
		}
		var friction []string
		sup := supSupervisor(store, clock, team, &friction)
		exit, err := sup.Run()
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if exit != 0 {
			t.Fatalf("exit = %d, want 0", exit)
		}
		if clock.sleeps != 2 {
			t.Fatalf("sleeps = %d, want 2", clock.sleeps)
		}
		if team.spawnCalls != 1 {
			t.Fatalf("spawn calls = %d, want 1 after the wait ends", team.spawnCalls)
		}
	})
}

func TestSuperviseStepFailures(t *testing.T) {
	t.Run("a lane failure fails the job with exit 4", func(t *testing.T) {
		store, jobDir, clock, team := supFixture(t)
		team.ensureErr = errors.New("lane failed")
		var friction []string
		sup := supSupervisor(store, clock, team, &friction)
		exit, err := sup.Run()
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if exit != ExitHerdr {
			t.Fatalf("exit = %d, want %d", exit, ExitHerdr)
		}
		st := supState(t, store)
		if st.Status != StatusFailed || st.Motivo == nil || *st.Motivo != "job: cannot ensure the job lane" {
			t.Fatalf("state = %+v", st)
		}
		if team.spawnCalls != 0 {
			t.Fatalf("spawn calls = %d, want 0", team.spawnCalls)
		}
		events, err := readEvents(jobDir)
		if err != nil {
			t.Fatal(err)
		}
		if got := eventTipes(events); !reflect.DeepEqual(got, []string{"accepted", "failure", "terminal", "cleanup"}) {
			t.Fatalf("event types = %v", got)
		}
		// The cleanup event lands after the terminal one.
		if terminal := events[len(events)-2]; terminal.Refs["exit"] != "4" {
			t.Fatalf("terminal refs = %+v", terminal.Refs)
		}
	})

	t.Run("a spawn failure fails the job with exit 4", func(t *testing.T) {
		store, _, clock, team := supFixture(t)
		team.spawnErr = errors.New("spawn failed")
		var friction []string
		sup := supSupervisor(store, clock, team, &friction)
		exit, err := sup.Run()
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if exit != ExitHerdr {
			t.Fatalf("exit = %d, want %d", exit, ExitHerdr)
		}
		st := supState(t, store)
		if st.Status != StatusFailed || st.Motivo == nil || *st.Motivo != "job: cannot spawn the job orchestrator" {
			t.Fatalf("state = %+v", st)
		}
		if st.Orchestrator != "" {
			t.Fatalf("a failed spawn records no orchestrator: %q", st.Orchestrator)
		}
	})

	t.Run("a spawn failure with a detail writes the detail to the friction log only (DJA-395)", func(t *testing.T) {
		store, _, clock, team := supFixture(t)
		team.spawnErr = &ExitError{Code: ExitHerdr, Msg: "job: cannot spawn the job orchestrator", Detail: "exit 7: agent start failed"}
		var friction []string
		sup := supSupervisor(store, clock, team, &friction)
		if exit, err := sup.Run(); err != nil || exit != ExitHerdr {
			t.Fatalf("Run = %d, %v", exit, err)
		}
		st := supState(t, store)
		if st.Motivo == nil || *st.Motivo != "job: cannot spawn the job orchestrator" {
			t.Fatalf("the motivo must stay fixed: %+v", st)
		}
		found := false
		for _, line := range friction {
			if line == "job: spawn the job orchestrator failed: exit 7: agent start failed" {
				found = true
			}
		}
		if !found {
			t.Fatalf("friction = %v", friction)
		}
	})

	t.Run("a dispatch failure fails the job with exit 4 and keeps the orchestrator", func(t *testing.T) {
		store, jobDir, clock, team := supFixture(t)
		team.dispatchErr = errors.New("dispatch failed")
		var friction []string
		sup := supSupervisor(store, clock, team, &friction)
		exit, err := sup.Run()
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if exit != ExitHerdr {
			t.Fatalf("exit = %d, want %d", exit, ExitHerdr)
		}
		st := supState(t, store)
		if st.Status != StatusFailed || st.Motivo == nil || *st.Motivo != "job: cannot dispatch the job orchestrator" {
			t.Fatalf("state = %+v", st)
		}
		if st.Orchestrator != "orch-1" || st.DispatchedAt != "" {
			t.Fatalf("the spawn stays recorded: orchestrator=%q dispatched_at=%q", st.Orchestrator, st.DispatchedAt)
		}
		if events, err := readEvents(jobDir); err != nil {
			t.Fatal(err)
		} else if got := eventTipes(events); !reflect.DeepEqual(got, []string{"accepted", "worker_spawned", "failure", "terminal", "cleanup"}) {
			t.Fatalf("event types = %v", got)
		}
	})
}

func TestSuperviseUnknownStateFriction(t *testing.T) {
	store, _, clock, team := supFixture(t)
	report := writeOrchestratorReport(t, "# Report — job\n\n- Item 1 [done] did the thing\n")
	team.statusScript = []fakeStatus{{State: "weird-state"}, {State: "weird-state"}, {State: "other-state"}, {State: "done", Report: report}}
	var friction []string
	sup := supSupervisor(store, clock, team, &friction)

	exit, err := sup.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if exit != 0 {
		t.Fatalf("exit = %d, want 0", exit)
	}
	// Friction reports each unknown state once.
	if len(friction) != 2 || !strings.Contains(friction[0], "weird-state") || !strings.Contains(friction[1], "other-state") {
		t.Fatalf("friction = %v", friction)
	}
	if st := supState(t, store); st.Status != StatusDone {
		t.Fatalf("state = %+v", st)
	}
}

func TestSuperviseNewSupervisor(t *testing.T) {
	store, _, _, _ := supFixture(t)
	sup, err := NewSupervisor(store, "job-1", platform.Env{}, "/opt/bin/soho", nil)
	if err != nil {
		t.Fatalf("NewSupervisor: %v", err)
	}
	cli, ok := sup.Ops.(selfCLI)
	if !ok {
		t.Fatalf("Ops = %T, want selfCLI", sup.Ops)
	}
	snap, err := store.Snapshot("job-1")
	if err != nil {
		t.Fatal(err)
	}
	if cli.Exe != "/opt/bin/soho" || cli.Dir != snap.State.Dir {
		t.Fatalf("selfCLI = %+v, want the state dir as working directory", cli)
	}
	if sup.Store != store || sup.ID != "job-1" {
		t.Fatalf("supervisor = %+v", sup)
	}

	// A job without a recorded worktree cannot be supervised.
	if _, err := store.Start("job-2", []byte(strings.Replace(supTestBrief, "job-1", "job-2", 1))); err != nil {
		t.Fatal(err)
	}
	_, err = NewSupervisor(store, "job-2", platform.Env{}, "/opt/bin/soho", nil)
	if err == nil {
		t.Fatal("NewSupervisor accepted a job without a worktree")
	}
	wantExit(t, err, ExitUsage, "job: the job has no worktree directory")

	if _, err := NewSupervisor(store, "nope", platform.Env{}, "/opt/bin/soho", nil); err == nil {
		t.Fatal("NewSupervisor accepted an unknown id")
	}
}

// TestSuperviseRestartTerminalWithoutWorkspace: a restart on a terminal
// job that never recorded a Herdr workspace finalizes nothing: report.json
// and the event log stay as they are.
func TestSuperviseRestartTerminalWithoutWorkspace(t *testing.T) {
	store, jobDir, clock, team := supFixture(t)
	if _, err := store.Record("job-1", func(st *State) { st.WorkspaceID = "" }); err != nil {
		t.Fatal(err)
	}
	for _, to := range []string{StatusFinishing, StatusDone} {
		if _, err := store.Transition("job-1", to); err != nil {
			t.Fatal(err)
		}
	}
	reportPath := filepath.Join(jobDir, "report.json")
	before, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	eventsBefore, err := readEvents(jobDir)
	if err != nil {
		t.Fatal(err)
	}
	var friction []string
	sup := supSupervisor(store, clock, team, &friction)
	if exit, err := sup.Run(); err != nil || exit != 0 {
		t.Fatalf("Run = %d %v, want 0", exit, err)
	}
	after, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("a restart rewrote report.json of a job without a workspace")
	}
	if eventsAfter, err := readEvents(jobDir); err != nil || len(eventsAfter) != len(eventsBefore) {
		t.Fatalf("events %d -> %d (%v), want unchanged", len(eventsBefore), len(eventsAfter), err)
	}
}
