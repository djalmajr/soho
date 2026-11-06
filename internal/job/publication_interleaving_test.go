package job

import (
	"strings"
	"testing"
)

// publication_interleaving_test.go pins the publication observation against
// a real change of the job branch's head between the separate Git reads and
// while the terminal settlement still runs, and against the verified
// absence of a pull request. The head moves through real commits in the job
// worktree, triggered from the supervisor's clock (the seam every bounded
// finishing step calls), so the interleaving is deterministic.

// hookClock is the shared fake clock with a callback on every Monotonic
// reading, which the bounded finishing steps take before each subprocess.
type hookClock struct {
	*fakeClock
	onMono func()
}

func (c hookClock) Monotonic() int64 {
	if c.onMono != nil {
		c.onMono()
	}
	return c.fakeClock.Monotonic()
}

// A commit made after the head reads but before the commit listing must not
// be counted as published: the observation is unverified, the done fails and
// no snapshot (not even one for the old head) is kept.
func TestPublicationHeadMovedBetweenTheReadsIsNotVerified(t *testing.T) {
	f := newSupGitFixture(t, supGitGhRules)
	f.job.commitIn(t, "feat: first")
	reads, fired := 0, false
	f.sup.Clock = hookClock{fakeClock: f.clock, onMono: func() {
		if fired || countTipes(f.events(t), "pr_opened") == 0 || f.state(t).FinalPublication != nil {
			return
		}
		// Reads after the pull request opened: status, head, remote head, then
		// the commit listing — the commit lands right before the listing.
		reads++
		if reads == 4 {
			fired = true
			f.job.commitIn(t, "feat: second")
		}
	}}
	exit, err, _ := finishDone(t, f)
	if !fired {
		t.Fatal("the interleaved commit never ran")
	}
	requirePublicationFailed(t, f, exit, err)
	if remoteHasBranch(f) && f.job.remoteRef(t) == f.job.head(t) {
		t.Errorf("the remote carries the moved head, fixture error")
	}
}

// A commit made while the collaboration settlement still runs (after the
// observation was saved) is not covered by it: the head is checked at the
// settlement boundary, the old observation is dropped and the done fails.
func TestPublicationHeadMovedDuringSettlementFailsTheDone(t *testing.T) {
	f := newSupGitFixture(t, supGitGhRules)
	f.job.commitIn(t, "feat: first")
	fired := false
	f.sup.Clock = hookClock{fakeClock: f.clock, onMono: func() {
		if fired || f.state(t).FinalPublication == nil {
			return
		}
		fired = true
		f.job.commitIn(t, "feat: during settlement")
	}}
	exit, err, _ := finishDone(t, f)
	if !fired {
		t.Fatal("the settlement-time commit never ran")
	}
	requirePublicationFailed(t, f, exit, err)
	rep := f.report(t)
	if rep["sincronizado"] != false || rep["head"] == rep["head_remoto"] {
		t.Errorf("report head=%v remote=%v sincronizado=%v, want the moved head unsynchronized", rep["head"], rep["head_remoto"], rep["sincronizado"])
	}
}

// A job whose pull request was opened earlier and whose published head later
// became part of the base ref has no commit ahead of the base: the verified
// observation records the absence of a pull request, and neither the first
// report, a refresh nor a close brings the historical one back.
func TestPublicationVerifiedAbsenceOfPRSurvivesAnEarlierPREvent(t *testing.T) {
	f := newSupGitFixture(t, supGitGhRules)
	f.job.commitIn(t, "feat: first")
	f.sup.pushNow = true
	f.tickN(t, 1)
	if countTipes(f.events(t), "pr_opened") != 1 {
		t.Fatalf("events = %v, want the earlier pr_opened", f.tipes(t))
	}
	head := f.job.head(t)
	if out, err := f.job.base.git(f.job.base.bare, "update-ref", "refs/heads/main", head); err != nil {
		t.Fatalf("advance the base: %s: %v", out, err)
	}
	if out, err := f.job.base.git(f.job.dir, "fetch", "-q", "origin"); err != nil {
		t.Fatalf("fetch the base: %s: %v", out, err)
	}
	exit, err, _ := finishDone(t, f)
	if err != nil || exit != 0 {
		t.Fatalf("finishOutcome = %d %v, want done with exit 0", exit, err)
	}
	pub := f.state(t).FinalPublication
	if pub == nil || pub.PR != nil || len(pub.Commits) != 0 || pub.LocalHead != head {
		t.Fatalf("publication = %+v, want the verified head with no commits and no pr", pub)
	}
	check := func(stage string) {
		t.Helper()
		rep := f.report(t)
		ids, _ := rep["publico"].(map[string]any)["ids"].([]any)
		if rep["pr"] != nil || len(reportSlice(t, rep, "commits")) != 0 || rep["motivo"] != "sem commits" || strings.Contains(strings.Join(anyStrings(ids), ","), "PR#") {
			t.Errorf("%s: report pr=%v commits=%v motivo=%v ids=%v, want the verified absence", stage, rep["pr"], rep["commits"], rep["motivo"], ids)
		}
	}
	check("terminal")
	if _, err := f.store.Transition(supGitID, StatusCollected); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Close(supGitID, false); err != nil {
		t.Fatal(err)
	}
	check("closed")
}

func anyStrings(in []any) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		s, _ := v.(string)
		out = append(out, s)
	}
	return out
}
