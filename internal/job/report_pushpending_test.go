package job

import (
	"path/filepath"
	"testing"
)

// report_pushpending_test.go pins DJA-413: a job that committed but whose
// commit never reached the remote is never reported as sem commits. The
// motivo, when the state carries none, is sem commits for a job without git
// facts, push pendente when a listed commit is pending, fatos de git
// indisponíveis when the facts read failed and the commit list is unknown,
// sem commits for a known empty list with no pull request, and null
// otherwise. A motivo already in the state is never replaced.

// breakFetchURL makes `git ls-remote origin` fail (the network gone) while
// the local history, including refs/remotes/origin/<base>, stays readable.
func breakFetchURL(t *testing.T, f *supGitFixture) {
	t.Helper()
	missing := filepath.Join(f.job.base.root, "nonexistent.git")
	if out, err := f.job.base.git(f.job.dir, "config", "remote.origin.url", missing); err != nil {
		t.Fatalf("git config url: %s: %v", out, err)
	}
}

// writeFacts builds the finalize facts the way the supervisor does and
// writes report.json from them.
func (f *supGitFixture) writeFacts(t *testing.T) (ReportFacts, map[string]any) {
	t.Helper()
	facts := f.sup.reportFacts(f.state(t), f.events(t), f.jobDir)
	if _, err := f.store.WriteReport(supGitID, facts); err != nil {
		t.Fatalf("WriteReport: %v", err)
	}
	return facts, f.report(t)
}

func TestReportPushPendingMotivo(t *testing.T) {
	t.Run("the smoke: the facts read fails and the listed commit is pending", func(t *testing.T) {
		f := newSupGitFixture(t, nil)
		sha := f.job.commitIn(t, "test: add smoke marker")
		breakFetchURL(t, f)
		facts, rep := f.writeFacts(t)
		if facts.Head != "" {
			t.Fatalf("facts.Head = %q, want empty (the facts read failed)", facts.Head)
		}
		if rep["motivo"] != "push pendente" {
			t.Fatalf("motivo = %v, want push pendente", rep["motivo"])
		}
		commits := reportSlice(t, rep, "commits")
		if len(commits) != 1 || commits[0]["sha"] != sha || commits[0]["push"] != "pendente" {
			t.Fatalf("commits = %v, want the one pending commit %s", commits, sha)
		}
		if rep["sincronizado"] != false {
			t.Fatalf("sincronizado = %v, want false", rep["sincronizado"])
		}
		if len(*f.friction) == 0 {
			t.Fatal("friction is empty, want the facts read failure")
		}
	})

	t.Run("the facts are read and the local head differs from the remote", func(t *testing.T) {
		f := newSupGitFixture(t, nil)
		f.job.commitIn(t, "feat: first")
		if res, err := f.sup.Git.Push(); err != nil || !res.Pushed {
			t.Fatalf("Push = %+v err=%v, want pushed", res, err)
		}
		second := f.job.commitIn(t, "feat: second")
		facts, rep := f.writeFacts(t)
		if facts.Head != second || facts.HeadRemoto == facts.Head {
			t.Fatalf("head=%q remote=%q, want the local head ahead of the remote", facts.Head, facts.HeadRemoto)
		}
		if rep["motivo"] != "push pendente" {
			t.Fatalf("motivo = %v, want push pendente", rep["motivo"])
		}
	})

	t.Run("the facts read and the commit listing both fail", func(t *testing.T) {
		f := newSupGitFixture(t, nil)
		f.job.commitIn(t, "feat: first")
		breakFetchURL(t, f)
		f.sup.Git.Base = "no-such-base"
		facts, rep := f.writeFacts(t)
		if facts.Head != "" || facts.Commits != nil {
			t.Fatalf("head=%q commits=%v, want neither known", facts.Head, facts.Commits)
		}
		if rep["motivo"] != "fatos de git indisponíveis" {
			t.Fatalf("motivo = %v, want fatos de git indisponíveis", rep["motivo"])
		}
		if len(*f.friction) < 2 {
			t.Fatalf("friction = %v, want both failures", *f.friction)
		}
	})

	t.Run("a job without git facts (workspace mode) keeps sem commits", func(t *testing.T) {
		f := newSupGitFixture(t, nil)
		f.sup.Git = nil
		_, rep := f.writeFacts(t)
		if rep["motivo"] != "sem commits" {
			t.Fatalf("motivo = %v, want sem commits", rep["motivo"])
		}
		// Even with a commit list in the facts, no git facts at all is
		// still the workspace case, never push pendente.
		rep2, err := f.store.WriteReport(supGitID, ReportFacts{Commits: []Commit{{SHA: "abc", Titulo: "x", Push: "pendente"}}})
		if err != nil {
			t.Fatal(err)
		}
		if rep2.Motivo == nil || *rep2.Motivo != "sem commits" {
			t.Fatalf("motivo = %v, want sem commits without git facts", rep2.Motivo)
		}
	})

	t.Run("a known empty commit list with no pull request keeps sem commits", func(t *testing.T) {
		f := newSupGitFixture(t, nil)
		breakFetchURL(t, f)
		facts, rep := f.writeFacts(t)
		if facts.Commits == nil || len(facts.Commits) != 0 {
			t.Fatalf("commits = %v, want known and empty", facts.Commits)
		}
		if rep["motivo"] != "sem commits" {
			t.Fatalf("motivo = %v, want sem commits", rep["motivo"])
		}
	})

	t.Run("a pushed commit with an open pull request has no motivo", func(t *testing.T) {
		f := newSupGitFixture(t, nil)
		f.job.commitIn(t, "feat: first")
		if res, err := f.sup.Git.Push(); err != nil || !res.Pushed {
			t.Fatalf("Push = %+v err=%v, want pushed", res, err)
		}
		if _, err := f.store.Append(supGitID, EventIn{Tipo: "pr_opened", Resumo: "pr", Refs: map[string]string{"pr": "7"}}); err != nil {
			t.Fatal(err)
		}
		_, rep := f.writeFacts(t)
		if rep["motivo"] != nil {
			t.Fatalf("motivo = %v, want null", rep["motivo"])
		}
		commits := reportSlice(t, rep, "commits")
		if len(commits) != 1 || commits[0]["push"] != "ok" {
			t.Fatalf("commits = %v, want one pushed commit", commits)
		}
	})

	t.Run("a motivo already in the state is never replaced", func(t *testing.T) {
		f := newSupGitFixture(t, nil)
		f.job.commitIn(t, "feat: first")
		breakFetchURL(t, f)
		motivo := "itens parciais"
		if _, err := f.store.Record(supGitID, func(st *State) { st.Motivo = &motivo }); err != nil {
			t.Fatal(err)
		}
		_, rep := f.writeFacts(t)
		if rep["motivo"] != "itens parciais" {
			t.Fatalf("motivo = %v, want the stored itens parciais", rep["motivo"])
		}
	})
}
