package job

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// DJA-559: a reviewer the job's team cannot open is a skipped review item,
// not a pending decision. The role directive (skills/soho/roles/
// job-orchestrator.md) tells the orchestrator that when `soho spawn` refuses
// the reviewer because the job's team configuration holds no lane or role
// for it, the review item is [skipped] with the refusal as the reason, and
// it gets no decision or question event. These tests pin the supervisor's
// outcome for that report: with no [partial] item the job ends done and
// runs the terminal steps the done path runs today. The control keeps the
// boundary discriminated: the same scenario marked [partial] with its open
// decision stays blocked on the decisions, as before the directive.

// The refusal reason is the spawn CLI's own refusal line for a role no lane
// holds (internal/spawn/spawn.go, exit 3), quoted in the report item.
const reviewRefusalReason = "Review: `soho spawn reviewer` was refused: role 'reviewer' is not in any lane (panes=2). Add it with lane.<name>.roles, or set lanes=off. — the job's team configuration holds no lane or role for it and must not change"

// The orchestrator report marks the review [skipped] with the
// reviewer-spawn-refused reason and carries no [partial] item: the job ends
// done, not blocked, and runs the terminal steps the done path runs today
// (the zero-partial worker_done, the terminal event, the cleanup event, the
// done status and the done report).
func TestSuperviseReviewSkippedOnRefusedSpawnEndsDone(t *testing.T) {
	store, jobDir, clock, team := supFixture(t)
	report := writeOrchestratorReport(t,
		"# Report — job\n\n"+
			"## Resumo\nThe slice landed and the gates ran; the review was skipped because the job's team holds no reviewer.\n\n"+
			"- [done] Item 1 — implemented the change and ran the gates\n"+
			"- [skipped] "+reviewRefusalReason+"\n")
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
	st := supState(t, store)
	// Done, not blocked: no motivo stands, the pending-decision crossing
	// never happened.
	if st.Status != StatusDone || st.Motivo != nil {
		t.Fatalf("state = %s motivo=%v, want done without a motivo", st.Status, st.Motivo)
	}
	events, err := readEvents(jobDir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"accepted", "worker_spawned", "worker_done", "terminal", "cleanup"}
	if got := eventTipes(events); !reflect.DeepEqual(got, want) {
		t.Fatalf("event types = %v, want %v (no blocked event)", got, want)
	}
	done := events[2]
	if done.Resumo != "job orchestrator reported, 0 partial" {
		t.Fatalf("worker_done resumo = %q, want the zero-partial report", done.Resumo)
	}
	terminal := events[3]
	if terminal.Refs["exit"] != "0" || len(terminal.Refs) != 1 {
		t.Fatalf("terminal refs = %+v, want only exit=0", terminal.Refs)
	}
	if clock.sleeps != 2 {
		t.Fatalf("sleeps = %d, want 2 (the two working ticks)", clock.sleeps)
	}
	raw, err := os.ReadFile(filepath.Join(jobDir, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"status":"done"`)) || !bytes.Contains(raw, []byte(`"parciais":0`)) {
		t.Fatalf("report.json = %s", raw)
	}
	// The report keeps the skipped review item with its state and the
	// refusal reason, separate from the partial count.
	rep, err := readReport(jobDir)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Parciais != 0 {
		t.Fatalf("parciais = %d, want 0 (the [skipped] item is not partial)", rep.Parciais)
	}
	if len(rep.Itens) != 2 || rep.Itens[1].Estado != "skipped" || !strings.Contains(rep.Itens[1].Item, "not in any lane") {
		t.Fatalf("itens = %+v, want the skipped review item with the refusal reason", rep.Itens)
	}
	if len(friction) != 0 {
		t.Fatalf("friction = %v, want none", friction)
	}
}

// The same scenario marked [partial] with its open decision keeps today's
// handling: the job ends blocked on the pending decision, not done. Without
// this control the done test cannot show that the [skipped] marker and the
// absent decision are what discriminate the outcome.
func TestSuperviseReviewPartialWithRefusalDecisionStaysBlocked(t *testing.T) {
	var friction []string
	store, jobDir, clock, team, sup := nsSup(t, 10*time.Second, &friction)
	// The orchestrator published the decision for the refusal while the
	// job was running, before the report landed.
	if _, err := store.Note("job-1", EventIn{
		Tipo:   "decision",
		Escopo: "projeto",
		Resumo: "reviewer spawn refused and the team configuration must not change",
	}); err != nil {
		t.Fatal(err)
	}
	report := writeOrchestratorReport(t,
		"# Report — job\n\n"+
			"## Resumo\nThe slice landed; the review stayed open because the reviewer could not be opened.\n\n"+
			"- [done] Item 1 — implemented the change and ran the gates\n"+
			"- [partial] "+reviewRefusalReason+"\n")
	team.statusScript = []fakeStatus{{State: "working"}, {State: "done", Report: report}}
	stop := errors.New("stop the watch loop")
	clock.stopAfter = 12
	clock.stop = stop
	func() {
		defer func() {
			r := recover()
			if r == nil {
				t.Error("the watch loop ended before the stop: the job did not stay blocked")
				return
			}
			if !errors.Is(r.(error), stop) {
				panic(r)
			}
		}()
		if _, err := sup.Run(); err != nil {
			t.Errorf("Run: %v", err)
		}
	}()
	st := supState(t, store)
	if st.Status != StatusBlocked || st.Motivo == nil || *st.Motivo != decisionsPendingMotivo {
		t.Fatalf("state = %s motivo=%v, want blocked on the pending decision", st.Status, st.Motivo)
	}
	events := mustEvents(t, jobDir)
	if countTipe(events, "terminal") != 0 {
		t.Fatalf("event types = %v, want no terminal outcome", eventTipes(events))
	}
	var blocked *Event
	for i := range events {
		if events[i].Tipo == "blocked" {
			blocked = &events[i]
		}
	}
	if blocked == nil || blocked.Refs["motivo"] != decisionsPendingMotivo {
		t.Fatalf("blocked event = %+v, want the decisions-pending motivo", blocked)
	}
	// The decision event the orchestrator published stands before the
	// worker events: accepted, decision, worker_spawned, worker_done, blocked.
	var done *Event
	for i := range events {
		if events[i].Tipo == "worker_done" {
			done = &events[i]
		}
	}
	if done == nil || done.Resumo != "job orchestrator reported, 1 partial" {
		t.Fatalf("worker_done = %+v, want the one-partial report", done)
	}
	if len(friction) != 0 {
		t.Fatalf("friction = %v, want none", friction)
	}
}
