// publication.go is the verified final publication of a publishing worktree
// job: a typed, point-in-time observation of the job's branch captured by the
// terminal procedure after its final sync — the exact job, repository and
// branch, the local head, the remote head, the commits ahead of the base and
// the pull request they require — persisted in the state together with the
// completion marker. A new successful (done) outcome of a worktree job is
// announced only when this observation was captured and saved; the report
// derives its head, head_remoto, sincronizado, commits and pr fields from
// it, so no later diagnostic read, report rewrite, restart or close can
// replace the observation with unknown facts. It is never an authority to
// remove a worktree: the cleanup re-reads the current cleanliness and
// synchronization itself.
package job

import (
	"errors"
	"strconv"
)

// finalPublicationMotivo is the stored motivo of a worktree job whose done
// outcome could not be backed by a verified final publication.
const finalPublicationMotivo = "final publication could not be verified"

// PublicationSnapshot is the verified final publication observation. It is
// internal state data (omitempty, backward-readable): a state written before
// it existed simply has none, and a legacy completion marker alone is never
// a verified publication.
type PublicationSnapshot struct {
	JobID      string       `json:"job_id"`
	Repo       string       `json:"repo,omitempty"`
	Branch     string       `json:"branch"`
	LocalHead  string       `json:"local_head"`
	RemoteHead string       `json:"remote_head"`
	Commits    []Commit     `json:"commits"`
	PR         *PullRequest `json:"pr,omitempty"`
	VerifiedAt string       `json:"verified_at,omitempty"`
}

// verifiedFor reports whether the snapshot is a complete observation tied to
// this job's state: the same job and branch, a non-empty local head that the
// remote carries, every listed commit published, and a pull request whenever
// commits are listed. Anything less is not a verified publication.
func (p *PublicationSnapshot) verifiedFor(st State) bool {
	if p == nil || p.JobID == "" || p.JobID != st.ID {
		return false
	}
	if p.Branch == "" || (st.Branch != "" && p.Branch != st.Branch) {
		return false
	}
	if p.LocalHead == "" || p.LocalHead != p.RemoteHead {
		return false
	}
	for _, commit := range p.Commits {
		if commit.SHA == "" || commit.Push != "ok" {
			return false
		}
	}
	if len(p.Commits) > 0 && (p.PR == nil || p.PR.Numero <= 0) {
		return false
	}
	return true
}

// applyTo overlays the verified observation on the report facts: the head
// fields, the commits and the pull request come from the snapshot, never
// from a later read. The cleanliness fact is left as read now.
func (p *PublicationSnapshot) applyTo(facts ReportFacts) ReportFacts {
	facts.Head = p.LocalHead
	facts.HeadRemoto = p.RemoteHead
	facts.GitFactsFailed = false
	facts.Commits = append([]Commit{}, p.Commits...)
	// A verified absence stands too: a pull request a later read reconstructs
	// from the event history does not belong to an observation without one.
	facts.PR = nil
	if p.PR != nil {
		pr := *p.PR
		facts.PR = &pr
	}
	return facts
}

// verifiedPublication returns the state's snapshot when it is verified for
// the state, nil otherwise (including a legacy state with only the
// completion marker).
func (st State) verifiedPublication() *PublicationSnapshot {
	if st.FinalPublication.verifiedFor(st) {
		return st.FinalPublication
	}
	return nil
}

// capturePublication reads the final publication facts after a clean final
// sync: the local head, the remote head, the commits ahead of the base and
// the pull request. It fails — and nothing is verified — when any read
// fails, when the heads differ or are empty, or when commits exist and no
// pull request was opened for them. The reads run inside the step the final
// sync opened, under its clamped timeouts.
func (s *Supervisor) capturePublication(fin *finSync, events []Event) (*PublicationSnapshot, error) {
	facts, err := s.Git.Facts()
	if err != nil {
		s.frictionGit(err)
		s.cutIfStepEnded(fin)
		return nil, err
	}
	if facts.Head == "" || facts.HeadRemoto == "" || facts.Head != facts.HeadRemoto {
		return nil, errors.New("job: the remote branch does not carry the local head")
	}
	// The listing is pinned to the observed sha, so a commit made after the
	// head reads cannot be counted as published.
	commits, err := s.Git.CommitsAt(facts.Head)
	if err != nil {
		s.frictionGit(err)
		s.cutIfStepEnded(fin)
		return nil, err
	}
	for i := range commits {
		commits[i].Push = "ok"
	}
	snap := &PublicationSnapshot{
		JobID:      s.ID,
		Repo:       s.Git.Repo,
		Branch:     s.Git.Branch,
		LocalHead:  facts.Head,
		RemoteHead: facts.HeadRemoto,
		Commits:    commits,
		VerifiedAt: formatTS(s.clk().Now()),
	}
	// The local head must still be the observed one after the reads: a move
	// in between is a different revision, never inherited as verified.
	head, err := s.Git.Head()
	if err != nil {
		s.frictionGit(err)
		s.cutIfStepEnded(fin)
		return nil, err
	}
	if head != facts.Head {
		return nil, errors.New("job: the local head moved while the final publication was read")
	}
	if len(commits) > 0 {
		pr := s.publishedPR(events)
		if pr == nil {
			return nil, errors.New("job: no pull request is recorded for the published commits")
		}
		snap.PR = pr
	}
	return snap, nil
}

// publishedPR is the pull request the job opened: the number of the first
// pr_opened event (appended only after the forge answered), with the URL of
// this process's EnsureDraftPR when it is the same pull request.
func (s *Supervisor) publishedPR(events []Event) *PullRequest {
	for _, event := range events {
		if event.Tipo != "pr_opened" {
			continue
		}
		numero, err := strconv.Atoi(event.Refs["pr"])
		if err != nil || numero <= 0 {
			return nil
		}
		if s.lastPR != nil && s.lastPR.Numero == numero {
			pr := *s.lastPR
			return &pr
		}
		return &PullRequest{Numero: numero, Estado: "draft"}
	}
	return nil
}

// persistPublication saves the verified snapshot together with the
// completion marker in one atomic state write.
func (s *Supervisor) persistPublication(snap *PublicationSnapshot) error {
	_, err := s.Store.Record(s.ID, func(st *State) {
		st.FinalPublication = snap
		st.FinishingSynced = true
	})
	return err
}

// invalidatePublication drops a snapshot (and the marker) whose local head
// is no longer the current one, so a crash after this write cannot trust it.
func (s *Supervisor) invalidatePublication() error {
	_, err := s.Store.Record(s.ID, func(st *State) {
		st.FinalPublication = nil
		st.FinishingSynced = false
	})
	return err
}

// revalidatePublication re-reads the local head at the terminal settlement
// boundary (after the collaborations ended, when job participants may still
// have committed) inside a finishing step of the existing git bound. It
// reports cut when the budget leaves no time, and an error — after dropping
// the stale observation — when the head is not the verified one or cannot be
// read; nothing is retried.
func (s *Supervisor) revalidatePublication() (cut bool, err error) {
	snap, serr := s.Store.Snapshot(s.ID)
	if serr != nil {
		return false, serr
	}
	pub := snap.State.verifiedPublication()
	if pub == nil {
		return false, errors.New("job: no verified final publication to revalidate")
	}
	s.Git.Limit = s.finCap
	defer func() {
		s.Git.Limit = nil
		s.stepDeadline, s.stepName = 0, ""
	}()
	if !s.beginFinStep("the publication revalidation", s.FinishingLimits.git()) {
		return true, nil
	}
	head, herr := s.Git.Head()
	if herr != nil {
		s.frictionGit(herr)
		if s.stepCut() {
			return true, nil
		}
		return false, herr
	}
	if head == pub.LocalHead {
		return false, nil
	}
	if ierr := s.invalidatePublication(); ierr != nil {
		s.frictionf("job: the supervisor could not invalidate the stale final publication: " + ierr.Error())
	}
	return false, errors.New("job: the local head moved after the final publication was verified")
}
