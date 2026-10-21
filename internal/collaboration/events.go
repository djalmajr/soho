package collaboration

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/djalmajr/soho/internal/reportscan"
)

type EventRequest struct {
	AssignmentID string
	Actor        Participant
	Type         string
	Report       string
	Revision     string
}

type PreparedEvent struct {
	Assignment Assignment
	Event      Event
	Fresh      bool
}

func PrepareEvent(store Store, request EventRequest) (PreparedEvent, error) {
	a, err := store.Read(request.AssignmentID)
	if err != nil {
		return PreparedEvent{}, err
	}
	member, ok := a.Member(request.Actor.Pane)
	if !ok || !SameParticipant(member, request.Actor) {
		return PreparedEvent{}, errors.New("event actor is not the assigned live participant")
	}
	hash, err := ValidReport(request.Report)
	if err != nil {
		return PreparedEvent{}, err
	}
	idFields, _ := json.Marshal([]string{a.ID, request.Type, request.Actor.Session, request.Report, hash, request.Revision})
	event := Event{ID: "e-" + Hash(idFields)[:16], Type: request.Type, Actor: request.Actor.Name, Report: request.Report, ReportHash: hash, Revision: request.Revision, Round: a.Round, Delivery: "pending"}
	for _, previous := range a.Events {
		if previous.ID == event.ID {
			if previous.Round != a.Round {
				return PreparedEvent{}, fmt.Errorf("event already published in round %d; write a new report for round %d", previous.Round, a.Round)
			}
			return PreparedEvent{Assignment: a, Event: previous}, nil
		}
	}
	if !a.Active() || a.Phase == Escalated || a.Phase == AwaitingOrchestrator {
		return PreparedEvent{}, errors.New("collaboration awaits an explicit orchestrator decision")
	}
	for _, previous := range a.Events {
		if previous.Delivery == "pending" || previous.Delivery == "uncertain" {
			return PreparedEvent{}, errors.New("previous delivery is unresolved; ask the orchestrator to inspect before continuing")
		}
	}
	nextPhase := ""
	newRevision := false
	switch request.Type {
	case "ready":
		if request.Actor.Pane != a.Author.Pane || a.Phase != Preparing {
			return PreparedEvent{}, errors.New("ready is only for the author in preparing")
		}
		nextPhase, newRevision = Reviewing, true
	case "corrected":
		if request.Actor.Pane != a.Author.Pane || a.Phase != Fixing {
			return PreparedEvent{}, errors.New("corrected is only for the author in fixing")
		}
		if a.Round >= a.MaxRounds {
			return PreparedEvent{}, errors.New("correction round limit reached; escalate to the orchestrator")
		}
		event.Round++
		nextPhase, newRevision = Reviewing, true
	case "findings":
		if request.Actor.Pane != a.Reviewer.Pane || a.Phase != Reviewing {
			return PreparedEvent{}, errors.New("findings is only for the reviewer in reviewing")
		}
		nextPhase = Fixing
		if a.Round >= a.MaxRounds {
			nextPhase = Escalated
		}
	case "approved":
		if request.Actor.Pane != a.Reviewer.Pane || a.Phase != Reviewing {
			return PreparedEvent{}, errors.New("approved is only for the reviewer in reviewing")
		}
		raw, err := os.ReadFile(request.Report)
		if err != nil {
			return PreparedEvent{}, err
		}
		header := reportscan.ReviewHeader(string(raw))
		if header == nil || header.Verdict != "pass" || header.Severity["P0"]+header.Severity["P1"]+header.Severity["P2"] > 0 || reportscan.PartialCount(string(raw)) > 0 {
			return PreparedEvent{}, errors.New("approval needs a passing review report with no open P0-P2 or partial items")
		}
		nextPhase = AwaitingOrchestrator
	case "escalate":
		nextPhase = Escalated
		event.Delivery = "not-required"
	default:
		return PreparedEvent{}, errors.New("unknown collaboration event")
	}
	snapshotDirectory := ""
	if request.Type != "escalate" {
		revision, err := Fingerprint(a.Author.Cwd, a.Paths)
		if err != nil {
			return PreparedEvent{}, err
		}
		if revision.Fingerprint != request.Revision || (!newRevision && a.Revision != request.Revision) {
			return PreparedEvent{}, errors.New("event revision differs from the declared review inputs")
		}
		if newRevision {
			snapshotDirectory, err = os.MkdirTemp(filepath.Join(store.directory(), a.ID), "snapshot-"+event.ID+"-")
			if err != nil {
				return PreparedEvent{}, err
			}
			snapshot := filepath.Join(snapshotDirectory, "view")
			if err := Snapshot(a.Author.Cwd, snapshot, revision); err != nil {
				_ = os.RemoveAll(snapshotDirectory)
				return PreparedEvent{}, err
			}
			a.Snapshot = snapshot
		}
	}
	updated, err := store.Update(a.ID, a.Generation, func(current *Assignment) error {
		current.Phase, current.Round = nextPhase, event.Round
		if newRevision {
			current.Revision, current.Snapshot = request.Revision, a.Snapshot
		}
		current.Events = append(current.Events, event)
		return nil
	})
	if err != nil {
		if snapshotDirectory != "" {
			_ = os.RemoveAll(snapshotDirectory)
		}
		return PreparedEvent{}, err
	}
	return PreparedEvent{Assignment: updated, Event: event, Fresh: true}, nil
}

// finalizeBeforeUpdateHook lets tests interleave an external transition after
// Finalize's read and validation, before its generation-checked write.
var finalizeBeforeUpdateHook func(id string)

func (s Store) Finalize(id, verdict string) (Assignment, error) {
	a, err := s.Read(id)
	if err != nil {
		return a, err
	}
	if verdict != "accept" && verdict != "reject" {
		return a, errors.New("verdict must be accept or reject")
	}
	if a.Phase != AwaitingOrchestrator && a.Phase != Escalated {
		return a, errors.New("collaboration is not awaiting a final decision")
	}
	if verdict == "accept" {
		if a.Phase != AwaitingOrchestrator {
			return a, errors.New("escalated collaboration cannot be accepted without a fresh review")
		}
		if err := CheckRevision(a); err != nil {
			return a, err
		}
		for _, event := range a.Events {
			if event.Delivery == "pending" || event.Delivery == "uncertain" {
				return a, errors.New("cannot accept an unresolved delivery")
			}
			hash, err := ValidReport(event.Report)
			if err != nil || hash != event.ReportHash {
				return a, errors.New("round evidence changed or disappeared; cannot accept this review")
			}
		}
	}
	if finalizeBeforeUpdateHook != nil {
		finalizeBeforeUpdateHook(id)
	}
	return s.Update(id, a.Generation, func(current *Assignment) error { current.Phase, current.Verdict = Finished, verdict; return nil })
}

func CheckRevision(a Assignment) error {
	revision, err := Fingerprint(a.Author.Cwd, a.Paths)
	if err != nil {
		return err
	}
	if a.Revision == "" || revision.Fingerprint != a.Revision {
		return errors.New("reviewed inputs changed; approval is stale")
	}
	return nil
}

func (s Store) Stop(id string) (Assignment, error) {
	a, err := s.Read(id)
	if err != nil {
		return a, err
	}
	if !a.Active() {
		return a, errors.New("collaboration already finished")
	}
	return s.Update(id, a.Generation, func(current *Assignment) error { current.Phase = Escalated; return nil })
}

func EventMessage(a Assignment, e Event) string {
	action := "Inspect the collaboration status and the report."
	switch e.Type {
	case "ready", "corrected":
		action = "Review the immutable snapshot inputs. Publish findings or approved with this revision; do not edit source files."
	case "findings":
		action = "Correct only the authorized files if the phase is fixing. Publish corrected with the new fingerprint; otherwise escalate."
	case "approved":
		action = "The review awaits the orchestrator's final decision; do not integrate or expand the task."
	}
	return fmt.Sprintf("Collaboration %s; event %s; round %d; revision %s.\nReport: %s\nSnapshot inputs: %s\nManifest: %s\n%s\nThe original brief and its forbidden actions remain authoritative. Do not wait synchronously for the other worker. After the event, return the report path.", a.ID, e.Type, e.Round, e.Revision, e.Report, filepath.Join(a.Snapshot, "files"), filepath.Join(a.Snapshot, "manifest.json"), action)
}
