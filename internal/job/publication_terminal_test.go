package job

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// publication_terminal_test.go pins the terminal boundaries of the verified
// publication: the report projection is durable before the success event is
// visible, and a failed intent resumed after its terminal event was
// announced neither captures nor replaces a publication.

// failReportWrites makes every report.json write fail while the returned
// switch is on, counting the injected failures.
func failReportWrites(t *testing.T) (on *bool, injected *int) {
	t.Helper()
	enabled, count := true, 0
	previous := renameAtomic
	renameAtomic = func(from, to string) error {
		if enabled && filepath.Base(to) == "report.json" {
			count++
			return errors.New("injected report write error")
		}
		return previous(from, to)
	}
	t.Cleanup(func() { renameAtomic = previous })
	return &enabled, &count
}

// When the terminal event is appended for a prospective done, report.json
// already carries the verified publication: the append boundary is observed
// from the event write itself.
func TestPublicationReportIsDurableBeforeTheSuccessEvent(t *testing.T) {
	f := newSupGitFixture(t, supGitGhRules)
	f.job.commitIn(t, "feat: first")
	var atAppend map[string]any
	previous := appendEventFn
	appendEventFn = func(dir string, now time.Time, in EventIn) (Event, error) {
		if in.Tipo == "terminal" && atAppend == nil {
			atAppend = f.report(t)
		}
		return previous(dir, now, in)
	}
	t.Cleanup(func() { appendEventFn = previous })
	exit, err, _ := finishDone(t, f)
	if err != nil || exit != 0 {
		t.Fatalf("finishOutcome = %d %v, want done with exit 0", exit, err)
	}
	pub := f.state(t).FinalPublication
	if atAppend == nil {
		t.Fatal("report.json did not exist when the terminal event was appended")
	}
	if atAppend["status"] != StatusFinishing || atAppend["head"] != pub.LocalHead || atAppend["head_remoto"] != pub.RemoteHead || atAppend["sincronizado"] != true {
		t.Errorf("report at the terminal append: status=%v head=%v remote=%v sincronizado=%v, want the verified projection with the provisional finishing outcome", atAppend["status"], atAppend["head"], atAppend["head_remoto"], atAppend["sincronizado"])
	}
	if rep := f.report(t); rep["status"] != "done" {
		t.Errorf("report status after the success event = %v, want done", rep["status"])
	}
}

// A terminal append that fails after the early report write leaves the job
// finishing with no terminal event: the durable report keeps the verified
// facts but its outcome stays provisional, never a done nobody announced.
// A resume whose local head did not move ends done, with one terminal event.
func TestPublicationTerminalAppendFailureKeepsTheReportProvisional(t *testing.T) {
	f := newSupGitFixture(t, supGitGhRules)
	f.job.commitIn(t, "feat: first")
	publicationRunOne(t, f, false)
	pub := f.state(t).FinalPublication
	rep := f.report(t)
	if rep["status"] != StatusFinishing || rep["head"] != pub.LocalHead || rep["head_remoto"] != pub.RemoteHead || rep["sincronizado"] != true {
		t.Fatalf("report after the failed append: status=%v head=%v remote=%v sincronizado=%v, want finishing with the verified facts", rep["status"], rep["head"], rep["head_remoto"], rep["sincronizado"])
	}
	exit, err := f.supRestarted(t).Run()
	if err != nil || exit != 0 {
		t.Fatalf("restart Run = %d %v, want done with exit 0", exit, err)
	}
	if n := countTipes(f.events(t), "terminal"); n != 1 {
		t.Errorf("terminal events = %d, want exactly one", n)
	}
	requireVerifiedReport(t, f, f.state(t).FinalPublication)
}

// The resume of that interruption after the local head moved and the remote
// refuses the new commit fails truthfully: exactly one terminal event, the
// failed outcome, and no stale done or verified head left in the report.
func TestPublicationTerminalAppendFailureThenChangedHeadResumesFailed(t *testing.T) {
	f := newSupGitFixture(t, supGitGhRules)
	f.job.commitIn(t, "feat: first")
	installRemoteHook(t, f, []fakecli.Rule{
		{AnyArgs: true, Call: 2, Code: 1, Stderr: "rejected by the fixture\n"},
		{AnyArgs: true},
	})
	publicationRunOne(t, f, false)
	old := f.state(t).FinalPublication
	if rep := f.report(t); rep["status"] != StatusFinishing {
		t.Fatalf("report status before the resume = %v, want finishing", rep["status"])
	}
	f.job.commitIn(t, "feat: second")
	exit, err := f.supRestarted(t).Run()
	if err != nil || exit != ExitFailed {
		t.Fatalf("restart Run = %d %v, want failed with exit 19", exit, err)
	}
	st := f.state(t)
	if st.Status != StatusFailed || st.FinalPublication != nil {
		t.Errorf("state = %s publication=%+v, want failed without a snapshot", st.Status, st.FinalPublication)
	}
	if n := countTipes(f.events(t), "terminal"); n != 1 {
		t.Errorf("terminal events = %d, want exactly one", n)
	}
	rep := f.report(t)
	if rep["status"] != "failed" || rep["sincronizado"] != false || rep["head"] == old.LocalHead || rep["head_remoto"] != old.RemoteHead {
		t.Errorf("report status=%v head=%v remote=%v sincronizado=%v, want failed, the new local head unsynchronized from %q", rep["status"], rep["head"], rep["head_remoto"], rep["sincronizado"], old.RemoteHead)
	}
}

// A report write that fails before the success event leaves the job
// finishing with its intent and no terminal event; the restart converges on
// done with exactly one terminal event and the verified report.
func TestPublicationReportWriteFailureBlocksTheSuccessEvent(t *testing.T) {
	f := newSupGitFixture(t, supGitGhRules)
	f.job.commitIn(t, "feat: first")
	on, injected := failReportWrites(t)
	if _, err := f.store.Transition(supGitID, StatusFinishing); err != nil {
		t.Fatal(err)
	}
	if exit, done, err := f.sup.finishOutcome(StatusDone, "", 0); err == nil || !done || exit != 0 {
		t.Fatalf("finishOutcome = %d done=%v err=%v, want the report write error", exit, done, err)
	}
	if *injected == 0 {
		t.Fatal("the report write error was never injected")
	}
	if n := countTipes(f.events(t), "terminal"); n != 0 {
		t.Fatalf("terminal events = %d, want none: no success is visible without its report", n)
	}
	if st := f.state(t); st.Status != StatusFinishing || st.TerminalIntent == nil || st.TerminalIntent.Status != StatusDone {
		t.Fatalf("state = %s intent=%+v, want finishing with the done intent", st.Status, st.TerminalIntent)
	}
	*on = false
	exit, err := f.supRestarted(t).Run()
	if err != nil || exit != 0 {
		t.Fatalf("restart Run = %d %v, want done with exit 0", exit, err)
	}
	if n := countTipes(f.events(t), "terminal"); n != 1 {
		t.Errorf("terminal events = %d, want exactly one", n)
	}
	requireVerifiedReport(t, f, f.state(t).FinalPublication)
}

// failedIntentRunOne runs process one of a failed outcome that cannot be
// published (the remote refuses the push) and leaves it finishing, either
// before the terminal event (afterAnnouncement false) or after it (true).
func failedIntentRunOne(t *testing.T, f *supGitFixture, afterAnnouncement bool) {
	t.Helper()
	if afterAnnouncement {
		fired := failStateWrite(t, errors.New("injected failed state write error"), func(p State) bool { return p.Status == StatusFailed })
		defer func() {
			if !*fired {
				t.Fatal("the failed state write error was never injected")
			}
		}()
	} else {
		previous := appendEventFn
		appendEventFn = func(dir string, now time.Time, in EventIn) (Event, error) {
			if in.Tipo == "terminal" {
				return Event{}, errors.New("injected terminal append error")
			}
			return previous(dir, now, in)
		}
		defer func() { appendEventFn = previous }()
	}
	if _, err := f.store.Transition(supGitID, StatusFinishing); err != nil {
		t.Fatal(err)
	}
	if err := f.sup.fail("falhou", ExitFailed); err == nil {
		t.Fatal("fail returned nil, want the injected error")
	}
	if st := f.state(t); st.Status != StatusFinishing || st.TerminalIntent == nil || st.TerminalIntent.Status != StatusFailed {
		t.Fatalf("state after process one = %s intent=%+v, want finishing with the failed intent", st.Status, st.TerminalIntent)
	}
	if announced := countTipes(f.events(t), "terminal") == 1; announced != afterAnnouncement {
		t.Fatalf("terminal announced = %v, want %v", announced, afterAnnouncement)
	}
}

// An announced failed outcome whose first process published nothing is never
// upgraded: the resume pushes as before but captures no snapshot. Before the
// announcement the same resume may capture one.
func TestPublicationFailedIntentResumeNeverUpgradesAnAnnouncedOutcome(t *testing.T) {
	for _, tc := range []struct {
		name         string
		announced    bool
		wantSnapshot bool
	}{
		{"announced", true, false},
		{"not announced yet", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSupGitFixture(t, supGitGhRules)
			f.job.commitIn(t, "feat: first")
			rejectFirstRemotePush(t, f)
			failedIntentRunOne(t, f, tc.announced)
			if f.state(t).FinalPublication != nil {
				t.Fatalf("publication after the refused push = %+v, want none", f.state(t).FinalPublication)
			}
			exit, err := f.supRestarted(t).Run()
			if err != nil || exit != ExitFailed {
				t.Fatalf("restart Run = %d %v, want the failed outcome", exit, err)
			}
			st := f.state(t)
			if st.Status != StatusFailed || (st.FinalPublication != nil) != tc.wantSnapshot {
				t.Errorf("state = %s publication=%+v, want failed with snapshot=%v", st.Status, st.FinalPublication, tc.wantSnapshot)
			}
			if n := countTipes(f.events(t), "terminal"); n != 1 {
				t.Errorf("terminal events = %d, want exactly one", n)
			}
		})
	}
}

// A historical observation of an announced failed outcome survives a local
// head change before the resume: it is not invalidated and not replaced.
func TestPublicationFailedIntentResumeKeepsAnAnnouncedObservation(t *testing.T) {
	f := newSupGitFixture(t, supGitGhRules)
	f.job.commitIn(t, "feat: first")
	// Process one verified and persisted the publication, then the failed
	// outcome was announced and its state write failed.
	f.sup.syncGitOnce()
	if _, err := f.store.Transition(supGitID, StatusFinishing); err != nil {
		t.Fatal(err)
	}
	if cut, verified := f.sup.finalSync(StatusFailed, false); cut || !verified {
		t.Fatalf("finalSync = cut %v verified %v, want a verified observation", cut, verified)
	}
	before := f.state(t).FinalPublication
	if before == nil {
		t.Fatal("no observation persisted")
	}
	fired := failStateWrite(t, errors.New("injected failed state write error"), func(p State) bool { return p.Status == StatusFailed })
	if err := f.sup.fail("falhou", ExitFailed); err == nil || !*fired {
		t.Fatalf("fail = %v fired=%v, want the injected failed state write error", err, *fired)
	}
	if n := countTipes(f.events(t), "terminal"); n != 1 {
		t.Fatalf("terminal events = %d, want the announced one", n)
	}
	f.job.commitIn(t, "feat: second")
	exit, err := f.supRestarted(t).Run()
	if err != nil || exit != ExitFailed {
		t.Fatalf("restart Run = %d %v, want the failed outcome", exit, err)
	}
	if st := f.state(t); st.Status != StatusFailed || !reflect.DeepEqual(st.FinalPublication, before) {
		t.Errorf("state = %s publication=%+v, want the announced observation %+v untouched", st.Status, st.FinalPublication, before)
	}
	if n := countTipes(f.events(t), "terminal"); n != 1 {
		t.Errorf("terminal events = %d, want exactly one", n)
	}
}
