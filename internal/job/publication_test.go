package job

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// publication_test.go pins the verified final publication of a worktree job
// (publication.go): the snapshot is complete and tied to the job, it is
// persisted with the completion marker before a done is announced, it stands
// in the report against every later diagnostic read, refresh, restart and
// close, and it is never the authority to remove a worktree.

func sha256Hex(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// pubDone runs the done outcome of the git fixture with one commit and
// returns the persisted snapshot.
func pubDone(t *testing.T, f *supGitFixture) *PublicationSnapshot {
	t.Helper()
	f.job.commitIn(t, "feat: first")
	exit, err, _ := finishDone(t, f)
	if err != nil || exit != 0 {
		t.Fatalf("finishOutcome = %d %v, want done with exit 0", exit, err)
	}
	pub := f.state(t).FinalPublication
	if pub == nil {
		t.Fatal("the done job carries no final publication snapshot")
	}
	return pub
}

// requireVerifiedReport asserts the serialized report carries exactly the
// verified observation: the heads, the synchronization, the commit and the
// pull request.
func requireVerifiedReport(t *testing.T, f *supGitFixture, pub *PublicationSnapshot) {
	t.Helper()
	rep := f.report(t)
	if rep["status"] != "done" {
		t.Errorf("report status = %v, want done", rep["status"])
	}
	if rep["head"] != pub.LocalHead || rep["head_remoto"] != pub.RemoteHead || rep["sincronizado"] != true {
		t.Errorf("report head=%v head_remoto=%v sincronizado=%v, want the verified %q synchronized", rep["head"], rep["head_remoto"], rep["sincronizado"], pub.LocalHead)
	}
	commits := reportSlice(t, rep, "commits")
	if len(commits) != len(pub.Commits) || len(commits) == 0 || commits[0]["sha"] != pub.Commits[0].SHA || commits[0]["push"] != "ok" {
		t.Errorf("report commits = %v, want the verified %+v published", commits, pub.Commits)
	}
	pr, _ := rep["pr"].(map[string]any)
	if pub.PR == nil || pr == nil || int(pr["numero"].(float64)) != pub.PR.Numero {
		t.Errorf("report pr = %v, want the verified %+v", rep["pr"], pub.PR)
	}
}

// A verified terminal publication is a point-in-time fact: a diagnostic git
// failure in a later finalize, a report refresh, the collect and close
// transitions and a reload all keep the exact heads, commit and pull
// request in the serialized report, and the artifact hashes still verify.
func TestPublicationSnapshotSurvivesLaterDiagnosticFailureRefreshAndReload(t *testing.T) {
	f := newSupGitFixture(t, supGitGhRules)
	if err := os.WriteFile(filepath.Join(f.jobDir, "report.md"), []byte("## Resumo\nShipped.\n\n- [done] shipped\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pub := pubDone(t, f)
	requireVerifiedReport(t, f, pub)

	// A later pass whose git reads all fail (the worktree is unreadable):
	// the diagnostic failure goes to friction and the report keeps the fact.
	good := f.sup.Git.Dir
	f.sup.Git.Dir = filepath.Join(t.TempDir(), "missing")
	f.sup.finalize(StatusDone)
	f.sup.Git.Dir = good
	requireVerifiedReport(t, f, pub)
	if rep := f.report(t); rep["motivo"] != nil {
		t.Errorf("report motivo = %v, want none after a diagnostic failure of a verified job", rep["motivo"])
	}

	// A restarted supervisor over the terminal job, twice (duplicate
	// resume), changes neither the observation nor the single terminal event.
	for i := 0; i < 2; i++ {
		if exit, err := f.supRestarted(t).Run(); err != nil || exit != 0 {
			t.Fatalf("restart %d Run = %d %v, want 0", i+1, exit, err)
		}
	}
	if n := countTipes(f.events(t), "terminal"); n != 1 {
		t.Errorf("terminal events = %d, want exactly one", n)
	}
	// The collected and closed transitions refresh the report from the state.
	if _, err := f.store.Transition(supGitID, StatusCollected); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Close(supGitID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Close(supGitID, false); err != nil {
		t.Fatal(err)
	}
	requireVerifiedReport(t, f, pub)
	if !reflect.DeepEqual(f.state(t).FinalPublication, pub) {
		t.Errorf("state publication = %+v, want the persisted %+v", f.state(t).FinalPublication, pub)
	}

	// The artifact hash of the report.md still verifies.
	rep := f.report(t)
	raw, err := os.ReadFile(filepath.Join(f.jobDir, "report.md"))
	if err != nil {
		t.Fatal(err)
	}
	artifacts := reportSlice(t, rep, "artefatos")
	if len(artifacts) != 1 || artifacts[0]["path"] != "report.md" || artifacts[0]["sha256"] != sha256Hex(raw) {
		t.Errorf("artefatos = %v, want the report.md hash %s", artifacts, sha256Hex(raw))
	}
}

// Truly unverified input stays unknown: a legacy terminal (the completion
// Boolean alone, no snapshot) is rebuilt from what the git read gives and is
// not upgraded to a verified publication by a refresh.
func TestPublicationLegacyBooleanOnlyTerminalStaysUnknown(t *testing.T) {
	f := newSupGitFixture(t, supGitGhRules)
	f.job.commitIn(t, "feat: first")
	if _, err := f.store.Record(supGitID, func(st *State) {
		st.TerminalIntent = &TerminalIntent{Status: StatusDone}
		st.FinishingSynced = true
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Transition(supGitID, StatusFinishing); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Append(supGitID, EventIn{Tipo: "terminal", Refs: map[string]string{"exit": "0"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Transition(supGitID, StatusDone); err != nil {
		t.Fatal(err)
	}
	f.sup.Git.Dir = filepath.Join(t.TempDir(), "missing")
	f.sup.finalize(StatusDone)
	if _, err := f.store.Transition(supGitID, StatusCollected); err != nil {
		t.Fatal(err)
	}
	rep := f.report(t)
	if rep["status"] != "done" || rep["head"] != nil || rep["head_remoto"] != nil || rep["sincronizado"] != false {
		t.Errorf("legacy report status=%v head=%v remote=%v sincronizado=%v, want done with unknown heads and false", rep["status"], rep["head"], rep["head_remoto"], rep["sincronizado"])
	}
	if f.state(t).FinalPublication != nil {
		t.Errorf("publication = %+v, want none: a Boolean is not a snapshot", f.state(t).FinalPublication)
	}
}

// A failure to persist the snapshot with its marker is not an optimistic
// success: the state write fails, nothing is verified, and the done fails
// truthfully with the cause in friction.
func TestPublicationPersistFailureIsNotOptimisticSuccess(t *testing.T) {
	f := newSupGitFixture(t, supGitGhRules)
	f.job.commitIn(t, "feat: first")
	fired := failStateWrite(t, errors.New("injected snapshot write error"), func(p State) bool { return p.FinalPublication != nil })
	exit, err, _ := finishDone(t, f)
	if !*fired {
		t.Fatal("the snapshot write error was never injected")
	}
	requirePublicationFailed(t, f, exit, err)
	frictionLine(t, f, "injected snapshot write error")
}

// A snapshot is complete or it is nothing: every incomplete or mismatched
// observation is rejected by the verification the report and the restart use.
func TestPublicationSnapshotVerificationRejectsIncompleteObservations(t *testing.T) {
	st := State{ID: supGitID, Branch: supGitBranch}
	good := func() *PublicationSnapshot {
		return &PublicationSnapshot{
			JobID: supGitID, Branch: supGitBranch, LocalHead: "aaa", RemoteHead: "aaa",
			Commits: []Commit{{SHA: "aaa", Titulo: "t", Push: "ok"}}, PR: &PullRequest{Numero: 7},
		}
	}
	if !good().verifiedFor(st) {
		t.Fatal("the control snapshot is not verified")
	}
	for name, mutate := range map[string]func(*PublicationSnapshot){
		"another job":           func(p *PublicationSnapshot) { p.JobID = "other" },
		"another branch":        func(p *PublicationSnapshot) { p.Branch = "job/other" },
		"empty local head":      func(p *PublicationSnapshot) { p.LocalHead, p.RemoteHead = "", "" },
		"remote head differs":   func(p *PublicationSnapshot) { p.RemoteHead = "bbb" },
		"commit not published":  func(p *PublicationSnapshot) { p.Commits[0].Push = "pendente" },
		"commits without a pr":  func(p *PublicationSnapshot) { p.PR = nil },
		"commits with a bad pr": func(p *PublicationSnapshot) { p.PR = &PullRequest{} },
	} {
		snap := good()
		mutate(snap)
		if snap.verifiedFor(st) {
			t.Errorf("%s: the incomplete snapshot is verified", name)
		}
	}
	var none *PublicationSnapshot
	if none.verifiedFor(st) {
		t.Error("a missing snapshot is verified")
	}
	noCommits := &PublicationSnapshot{JobID: supGitID, Branch: supGitBranch, LocalHead: "aaa", RemoteHead: "aaa", Commits: []Commit{}}
	if !noCommits.verifiedFor(st) {
		t.Error("a no-commit snapshot with synchronized heads must verify without a pull request")
	}
}

// Cleanup needs a fresh proof: a verified snapshot persisted at the terminal
// outcome is history. A worktree that became dirty, or whose local head moved
// past the published one, is kept by the pass that finalizes it again, while
// the report keeps the historical observation.
func TestPublicationSnapshotIsNeverRemovalAuthority(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(t *testing.T, f *supGitFixture)
		motivo string
	}{
		{"a dirty worktree", func(t *testing.T, f *supGitFixture) {
			if err := os.WriteFile(filepath.Join(f.job.dir, "dirty.txt"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, WorktreeRemoveDirty},
		{"a local head ahead of the remote", func(t *testing.T, f *supGitFixture) {
			f.job.commitIn(t, "feat: unpublished")
		}, WorktreeRemoveNotPushed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSupGitFixture(t, supGitGhRules)
			acFixture(t, f)
			f.job.commitIn(t, "feat: first")
			publicationRunOne(t, f, true)
			pub := f.state(t).FinalPublication
			tc.change(t, f)
			sup := f.supRestarted(t)
			sup.Herdr = f.herdr
			sup.Poll = f.sup.Poll
			if exit, err := sup.Run(); err != nil || exit != 0 {
				t.Fatalf("restart Run = %d %v, want the announced done", exit, err)
			}
			assertWorktreeKept(t, f)
			if f.state(t).WorktreeRemoved {
				t.Error("the worktree is recorded removed on a snapshot alone")
			}
			needs := false
			for _, event := range f.events(t) {
				if event.Tipo == "needs_attention" && event.Refs["motivo"] == tc.motivo {
					needs = true
				}
			}
			if !needs {
				t.Errorf("events = %v, want a needs_attention with motivo %q", f.tipes(t), tc.motivo)
			}
			rep := f.report(t)
			if rep["head"] != pub.LocalHead || rep["sincronizado"] != true {
				t.Errorf("report head=%v sincronizado=%v, want the historical verified observation %q", rep["head"], rep["sincronizado"], pub.LocalHead)
			}
			if limpeza, _ := rep["limpeza"].(map[string]any); limpeza["removivel"] != false || limpeza["worktree"] != "kept" {
				t.Errorf("limpeza = %v, want kept and not removable", rep["limpeza"])
			}
		})
	}
}

// A Git-free (workspace) job introduces no git requirement: the done is
// announced with exit 0, no snapshot is captured and no git command runs.
func TestPublicationGitFreeWorkspaceStaysGitFree(t *testing.T) {
	f := newSupGitFixture(t, supGitGhRules)
	f.sup.Git = nil
	if _, err := f.store.Transition(supGitID, StatusFinishing); err != nil {
		t.Fatal(err)
	}
	exit, done, err := f.sup.finishOutcome(StatusDone, "", 0)
	if err != nil || !done || exit != 0 {
		t.Fatalf("finishOutcome = %d done=%v err=%v, want done with exit 0", exit, done, err)
	}
	st := f.state(t)
	if st.Status != StatusDone || st.FinalPublication != nil {
		t.Errorf("state = %s publication=%+v, want done without a snapshot", st.Status, st.FinalPublication)
	}
	if remoteHasBranch(f) {
		t.Error("the job branch reached the remote, want no git at all")
	}
	if rep := f.report(t); rep["status"] != "done" || rep["head"] != nil || rep["sincronizado"] != false {
		t.Errorf("report status=%v head=%v sincronizado=%v, want done with no git facts", rep["status"], rep["head"], rep["sincronizado"])
	}
}

// A canceled or timed-out job keeps its outcome and its time bounds even
// when the publication cannot be verified: the required-publication gate is
// the prospective done's alone.
func TestPublicationCanceledAndTimeoutKeepTheirOutcomeWhenUnverified(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status string
		motivo string
		exit   int
	}{
		{"canceled", StatusCanceled, "cancelado", ExitCanceled},
		{"timeout", StatusTimeout, "tempo esgotado", ExitTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSupGitFixture(t, supGitGhRules)
			f.job.commitIn(t, "feat: first")
			rejectFirstRemotePush(t, f)
			if _, err := f.store.Transition(supGitID, StatusFinishing); err != nil {
				t.Fatal(err)
			}
			exit, done, err := f.sup.finishOutcome(tc.status, tc.motivo, tc.exit)
			if err != nil || !done || exit != tc.exit {
				t.Fatalf("finishOutcome = %d done=%v err=%v, want %s with exit %d", exit, done, err, tc.status, tc.exit)
			}
			st := f.state(t)
			if st.Status != tc.status || st.FinalPublication != nil || st.FinishingSynced {
				t.Errorf("state = %s publication=%+v synced=%v, want %s with nothing verified", st.Status, st.FinalPublication, st.FinishingSynced, tc.status)
			}
		})
	}
}

// A prospective done whose finishing budget is spent keeps the existing
// budget outcome (the cut step, the budget motivo), not the publication one.
func TestPublicationBudgetCutKeepsTheBudgetOutcome(t *testing.T) {
	f := newSupGitFixture(t, supGitGhRules)
	f.job.commitIn(t, "feat: first")
	anchor := f.clock.Now().Add(-11 * time.Minute).Format(time.RFC3339Nano)
	if _, err := f.store.Record(supGitID, func(st *State) {
		st.TerminalIntent = &TerminalIntent{Status: StatusDone}
		st.FinishingStartedAt = anchor
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Transition(supGitID, StatusFinishing); err != nil {
		t.Fatal(err)
	}
	exit, err := f.supRestarted(t).Run()
	requireBudgetFailed(t, f, exit, err, "the final push")
	if strings.Contains(strings.Join(*f.friction, "\n"), finalPublicationMotivo) {
		t.Errorf("friction = %q, want only the budget cut line", *f.friction)
	}
}
