package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/job"
)

// job_publication_test.go drives the serialized CLI consumers (Run, not a
// subprocess) over jobs whose terminal outcome was backed, or not, by a
// verified final publication.

// publishedJob drives a job to a terminal outcome through the store, the way
// the supervisor's terminal path does: with a verified final publication
// persisted in the state before the done transition (the report is built
// from it), or, when pub is nil, to the failed outcome of an unverified
// publication.
func (f *jobFix) publishedJob(t *testing.T, checkout string, pub *job.PublicationSnapshot) string {
	t.Helper()
	store := f.store(t, checkout)
	f.startJob(t, checkout)
	jobDir := filepath.Join(checkout, ".soho", "jobs", "job-1")
	if err := os.WriteFile(filepath.Join(jobDir, "report.md"), []byte("## Resumo\nShipped.\n\n- [done] shipped\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{job.StatusPreparing, job.StatusRunning, job.StatusFinishing} {
		if _, err := store.Transition("job-1", status); err != nil {
			t.Fatal(err)
		}
	}
	if pub != nil {
		if _, err := store.Record("job-1", func(st *job.State) {
			st.FinalPublication = pub
			st.FinishingSynced = true
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Append("job-1", job.EventIn{Tipo: "terminal", Refs: map[string]string{"exit": "0"}}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Transition("job-1", job.StatusDone); err != nil {
			t.Fatal(err)
		}
		return jobDir
	}
	motivo := "final publication could not be verified"
	if _, err := store.Append("job-1", job.EventIn{Tipo: "terminal", Refs: map[string]string{"exit": "19", "motivo": motivo}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Fail("job-1", motivo, job.ExitFailed); err != nil {
		t.Fatal(err)
	}
	return jobDir
}

// The public consumers of a job whose done was backed by a verified final
// publication: wait is exit 0, status and collect (JSON, Markdown, verify)
// print a successful read with the exact verified heads, commit and pull
// request, and close keeps them. The same consumers on a failed job whose
// publication could not be verified: wait is exit 19, collect exit 0 only
// means a successful read of a failed outcome with unknown heads.
func TestJobPublicationConsumers(t *testing.T) {
	t.Run("a verified done keeps its observation through every consumer", func(t *testing.T) {
		f := newJobFix(t)
		co := f.checkout(t, "example-org", "example-repo")
		pub := &job.PublicationSnapshot{
			JobID: "job-1", Branch: "job/job-1", LocalHead: strings.Repeat("a", 40), RemoteHead: strings.Repeat("a", 40),
			Commits: []job.Commit{{SHA: strings.Repeat("a", 40), Titulo: "feat: first", Push: "ok"}},
			PR:      &job.PullRequest{Numero: 7, URL: "https://example.invalid/pull/7", Estado: "draft"},
		}
		f.publishedJob(t, co, pub)

		if code, out, _ := f.run(t, "wait", "--id", "job-1"); code != 0 || !strings.Contains(out, `"status":"done"`) {
			t.Fatalf("wait code=%d out=%q, want done with exit 0", code, out)
		}
		if code, out, _ := f.run(t, "status", "--id", "job-1"); code != 0 || !strings.Contains(out, "done") {
			t.Fatalf("status code=%d out=%q", code, out)
		}
		check := func(stage string) {
			t.Helper()
			code, out, errOut := f.run(t, "collect", "--id", "job-1", "--verify")
			if code != 0 || errOut != "" {
				t.Fatalf("%s: collect --verify code=%d err=%q", stage, code, errOut)
			}
			var rep map[string]any
			if err := json.Unmarshal([]byte(strings.TrimSuffix(out, "\n")), &rep); err != nil {
				t.Fatalf("%s: %v", stage, err)
			}
			pr, _ := rep["pr"].(map[string]any)
			commits, _ := rep["commits"].([]any)
			if rep["head"] != pub.LocalHead || rep["head_remoto"] != pub.RemoteHead || rep["sincronizado"] != true ||
				pr == nil || pr["numero"] != float64(7) || len(commits) != 1 || commits[0].(map[string]any)["push"] != "ok" {
				t.Fatalf("%s: collect = %s, want the verified heads, commit and pr", stage, out)
			}
		}
		check("done")
		if code, out, _ := f.run(t, "collect", "--id", "job-1", "--md"); code != 0 || !strings.Contains(out, "Shipped.") {
			t.Fatalf("collect --md code=%d out=%q", code, out)
		}
		if code, out, _ := f.run(t, "events", "--id", "job-1"); code != 0 || strings.Count(out, `"tipo":"terminal"`) != 1 {
			t.Fatalf("events code=%d out=%q, want exactly one terminal event", code, out)
		}
		if code, out, errOut := f.run(t, "close", "--id", "job-1"); code != 0 {
			t.Fatalf("close code=%d out=%q err=%q", code, out, errOut)
		}
		check("closed")
		if code, _, _ := f.run(t, "wait", "--id", "job-1"); code != 0 {
			t.Fatalf("wait after close code=%d, want the stored done outcome", code)
		}
	})

	t.Run("an unverified publication is a failed outcome with unknown facts", func(t *testing.T) {
		f := newJobFix(t)
		co := f.checkout(t, "example-org", "example-repo")
		f.publishedJob(t, co, nil)
		code, out, _ := f.run(t, "wait", "--id", "job-1")
		if code != 19 || !strings.Contains(out, `"status":"failed"`) {
			t.Fatalf("wait code=%d out=%q, want failed with exit 19", code, out)
		}
		code, out, errOut := f.run(t, "collect", "--id", "job-1")
		if code != 0 || errOut != "" {
			t.Fatalf("collect code=%d err=%q: a successful read of a failed job", code, errOut)
		}
		var rep map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSuffix(out, "\n")), &rep); err != nil {
			t.Fatal(err)
		}
		if rep["status"] != "failed" || rep["motivo"] != "final publication could not be verified" || rep["head"] != nil || rep["sincronizado"] != false {
			t.Fatalf("collect = %s, want failed with the plain motivo and unknown heads", out)
		}
	})
}

// A job interrupted after the early report write and before the terminal
// event is still finishing: status, wait and collect (JSON and Markdown)
// agree that no outcome was announced, while the report keeps the verified
// facts. Once the event and the done state land, the same consumers read done.
func TestJobPublicationConsumersKeepTheOutcomeProvisionalUntilAnnounced(t *testing.T) {
	f := newJobFix(t)
	co := f.checkout(t, "example-org", "example-repo")
	pub := &job.PublicationSnapshot{
		JobID: "job-1", Branch: "job/job-1", LocalHead: strings.Repeat("a", 40), RemoteHead: strings.Repeat("a", 40),
		Commits: []job.Commit{{SHA: strings.Repeat("a", 40), Titulo: "feat: first", Push: "ok"}},
		PR:      &job.PullRequest{Numero: 7, URL: "https://example.invalid/pull/7", Estado: "draft"},
	}
	store := f.store(t, co)
	f.startJob(t, co)
	jobDir := filepath.Join(co, ".soho", "jobs", "job-1")
	if err := os.WriteFile(filepath.Join(jobDir, "report.md"), []byte("## Resumo\nShipped.\n\n- [done] shipped\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{job.StatusPreparing, job.StatusRunning, job.StatusFinishing} {
		if _, err := store.Transition("job-1", status); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Record("job-1", func(st *job.State) {
		st.FinalPublication = pub
		st.FinishingSynced = true
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PublishReportBeforeTerminal("job-1", job.StatusDone); err != nil {
		t.Fatal(err)
	}

	collect := func(stage, want string) map[string]any {
		t.Helper()
		code, out, errOut := f.run(t, "collect", "--id", "job-1")
		if code != 0 || errOut != "" {
			t.Fatalf("%s: collect code=%d err=%q", stage, code, errOut)
		}
		var rep map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSuffix(out, "\n")), &rep); err != nil {
			t.Fatalf("%s: %v", stage, err)
		}
		if rep["status"] != want || rep["head"] != pub.LocalHead || rep["head_remoto"] != pub.RemoteHead || rep["sincronizado"] != true {
			t.Fatalf("%s: collect = %s, want status %q with the verified facts", stage, out, want)
		}
		return rep
	}
	collect("interrupted", job.StatusFinishing)
	if code, out, _ := f.run(t, "status", "--id", "job-1"); code != 0 || !strings.Contains(out, job.StatusFinishing) || strings.Contains(out, "done") {
		t.Fatalf("status code=%d out=%q, want finishing and no done", code, out)
	}
	if code, out, _ := f.run(t, "wait", "--id", "job-1", "--timeout", "1"); code == 0 || strings.Contains(out, `"status":"done"`) {
		t.Fatalf("wait code=%d out=%q, want no success for a finishing job", code, out)
	}
	if code, out, _ := f.run(t, "collect", "--id", "job-1", "--md"); code != 0 || !strings.Contains(out, "Shipped.") {
		t.Fatalf("collect --md code=%d out=%q", code, out)
	}
	if code, out, _ := f.run(t, "events", "--id", "job-1"); code != 0 || strings.Contains(out, `"tipo":"terminal"`) {
		t.Fatalf("events code=%d out=%q, want no terminal event", code, out)
	}

	if _, err := store.Append("job-1", job.EventIn{Tipo: "terminal", Refs: map[string]string{"exit": "0"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transition("job-1", job.StatusDone); err != nil {
		t.Fatal(err)
	}
	collect("announced", job.StatusDone)
	if code, out, _ := f.run(t, "wait", "--id", "job-1"); code != 0 || !strings.Contains(out, `"status":"done"`) {
		t.Fatalf("wait after the announcement code=%d out=%q, want done", code, out)
	}
}
