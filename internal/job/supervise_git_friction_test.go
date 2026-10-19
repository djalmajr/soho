package job

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// supervise_git_friction_test.go groups the friction of a long network
// outage (DJA-409): the first failure of a cause writes its two lines, the
// same cause right after it writes nothing and counts, a new cause, the
// first clean pass and the terminal procedure write the count, and the
// clean pass writes the recovery. The remote is the fixture's local bare
// repository, broken by pointing a url of origin at a missing path.

const (
	frictionPushMsg = "job: git sync failed: push"
	frictionHeadMsg = "job: git sync failed: remote head"
)

// setOriginURL points remote.origin.<key> (url or pushurl) at a missing
// repository named name and returns that path.
func setOriginURL(t *testing.T, j *jobSyncFixture, key, name string) string {
	t.Helper()
	missing := filepath.Join(j.base.root, name+".git")
	if out, err := j.base.git(j.dir, "config", "remote.origin."+key, missing); err != nil {
		t.Fatalf("git config %s: %s: %v", key, out, err)
	}
	return missing
}

// setPushHelper points remote.origin.pushurl at a scheme git has no remote
// helper for: the push fails at once, with a stderr other than the missing
// repository's, and reaches no network.
func setPushHelper(t *testing.T, j *jobSyncFixture) {
	t.Helper()
	if out, err := j.base.git(j.dir, "config", "remote.origin.pushurl", "nosuchhelper::nowhere"); err != nil {
		t.Fatalf("git config pushurl: %s: %v", out, err)
	}
}

// originURL is remote.origin.url as the fixture set it.
func originURL(t *testing.T, j *jobSyncFixture) string {
	t.Helper()
	out, err := j.base.git(j.dir, "config", "--get", "remote.origin.url")
	if err != nil {
		t.Fatalf("git config --get remote.origin.url: %s: %v", out, err)
	}
	return strings.TrimSpace(string(out))
}

// restoreOriginURL puts remote.origin.url back.
func restoreOriginURL(t *testing.T, j *jobSyncFixture, url string) {
	t.Helper()
	if out, err := j.base.git(j.dir, "config", "remote.origin.url", url); err != nil {
		t.Fatalf("git config url: %s: %v", out, err)
	}
}

// syncPasses runs n watch-loop sync passes.
func syncPasses(t *testing.T, f *supGitFixture, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if code, done, err := f.sup.syncGitOnce(); err != nil || done || code != 0 {
			t.Fatalf("syncGitOnce = %d %v %v", code, done, err)
		}
	}
}

// gitFriction is the friction lines of the git sync, from line from on.
func gitFriction(f *supGitFixture, from int) []string {
	var lines []string
	for _, line := range (*f.friction)[from:] {
		if strings.HasPrefix(line, "job: git sync") {
			lines = append(lines, line)
		}
	}
	return lines
}

// wantGitFriction compares lines with want, where a want ending in "…" is a
// prefix.
func wantGitFriction(t *testing.T, got, want []string) {
	t.Helper()
	ok := len(got) == len(want)
	for i := 0; ok && i < len(want); i++ {
		if prefix, cut := strings.CutSuffix(want[i], "…"); cut {
			ok = strings.HasPrefix(got[i], prefix)
		} else {
			ok = got[i] == want[i]
		}
	}
	if !ok {
		t.Fatalf("friction:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func TestSupervisorGitSyncFrictionGrouping(t *testing.T) {
	t.Run("10 identical push failures and a success: two lines, the count, the recovery", func(t *testing.T) {
		f := newSupGitFixture(t, supGitGhRules)
		sha := f.job.commitIn(t, "feat: first")
		setOriginURL(t, f.job, "pushurl", "nowhere")
		syncPasses(t, f, 10)
		fixPushURL(t, f.job)
		syncPasses(t, f, 1)
		wantGitFriction(t, *f.friction, []string{
			frictionPushMsg,
			"job: git sync push failed: exit 128: …",
			"job: git sync push failed 9 more time(s) with the same cause",
			"job: git sync recovered after 10 failed attempt(s)",
		})
		if got := f.job.remoteRef(t); got != sha {
			t.Fatalf("remote ref = %q, want %q", got, sha)
		}
	})

	t.Run("alternating remote head and push failures write each new cause, with no count", func(t *testing.T) {
		f := newSupGitFixture(t, nil)
		f.job.commitIn(t, "feat: first")
		url := originURL(t, f.job)
		for i := 0; i < 2; i++ {
			setOriginURL(t, f.job, "url", "no-fetch")
			syncPasses(t, f, 1)
			restoreOriginURL(t, f.job, url)
			setOriginURL(t, f.job, "pushurl", "no-push")
			syncPasses(t, f, 1)
			fixPushURL(t, f.job)
		}
		cycle := []string{
			frictionHeadMsg, "job: git sync remote head failed: exit 128: …",
			frictionPushMsg, "job: git sync push failed: exit 128: …",
		}
		wantGitFriction(t, *f.friction, append(append([]string{}, cycle...), cycle...))
	})

	t.Run("the same step with another detail is a new cause, and the count restarts", func(t *testing.T) {
		f := newSupGitFixture(t, nil)
		f.job.commitIn(t, "feat: first")
		setOriginURL(t, f.job, "pushurl", "missing")
		syncPasses(t, f, 1)
		setPushHelper(t, f.job)
		syncPasses(t, f, 2)
		setOriginURL(t, f.job, "pushurl", "missing")
		syncPasses(t, f, 2)
		// The count starts again at each new cause.
		setPushHelper(t, f.job)
		syncPasses(t, f, 1)
		got := *f.friction
		wantGitFriction(t, got, []string{
			frictionPushMsg, "job: git sync push failed: exit 128: …",
			frictionPushMsg, "job: git sync push failed: …",
			"job: git sync push failed 1 more time(s) with the same cause",
			frictionPushMsg, "job: git sync push failed: exit 128: …",
			"job: git sync push failed 1 more time(s) with the same cause",
			frictionPushMsg, "job: git sync push failed: …",
		})
		if got[1] == got[3] || got[1] != got[6] || got[3] != got[9] {
			t.Fatalf("details = %q, %q, %q, %q, want the missing repository, the helper, the missing repository, the helper", got[1], got[3], got[6], got[9])
		}
	})

	t.Run("a single failure and a success: two lines and the recovery, no count", func(t *testing.T) {
		f := newSupGitFixture(t, supGitGhRules)
		f.job.commitIn(t, "feat: first")
		breakPushURL(t, f.job)
		syncPasses(t, f, 1)
		fixPushURL(t, f.job)
		syncPasses(t, f, 1)
		wantGitFriction(t, *f.friction, []string{
			frictionPushMsg,
			"job: git sync push failed: exit 128: …",
			"job: git sync recovered after 1 failed attempt(s)",
		})
	})

	t.Run("a success with no failure before writes nothing", func(t *testing.T) {
		f := newSupGitFixture(t, supGitGhRules)
		f.job.commitIn(t, "feat: first")
		syncPasses(t, f, 2)
		if len(*f.friction) != 0 {
			t.Fatalf("friction = %q, want none", *f.friction)
		}
		if got := f.tipes(t); got[len(got)-1] != "pr_opened" {
			t.Fatalf("event types = %v, want the pass to reach the pull request", got)
		}
	})

	t.Run("3 identical failures and the end of the job: the count is written by the terminal procedure", func(t *testing.T) {
		f := newSupGitFixture(t, nil)
		f.job.commitIn(t, "feat: first")
		breakPushURL(t, f.job)
		syncPasses(t, f, 3)
		before := len(*f.friction)
		if _, err := f.store.Transition(supGitID, StatusFinishing); err != nil {
			t.Fatal(err)
		}
		if _, _, err := f.sup.finishOutcome(StatusDone, "", 0); err != nil {
			t.Fatal(err)
		}
		wantGitFriction(t, (*f.friction)[:before], []string{
			frictionPushMsg, "job: git sync push failed: exit 128: …",
		})
		// The final sync's push fails with the same cause: it only counts,
		// and the job's last git steps write that count.
		wantGitFriction(t, gitFriction(f, before), []string{
			"job: git sync push failed 2 more time(s) with the same cause",
			"job: git sync push failed 1 more time(s) with the same cause",
		})
		if got := gitFriction(f, before)[0]; (*f.friction)[before] != got {
			t.Fatalf("first terminal friction = %q, want the count %q first", (*f.friction)[before], got)
		}
	})

	t.Run("a cancel in the middle of a streak writes nothing and keeps the count", func(t *testing.T) {
		f := newSupGitFixture(t, supGitGhRules)
		f.job.commitIn(t, "feat: first")
		breakPushURL(t, f.job)
		syncPasses(t, f, 2)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		f.sup.Git.Context = ctx
		if _, _, err := f.sup.syncGitPass(nil); err != nil {
			t.Fatal(err)
		}
		f.sup.Git.Context = nil
		if len(*f.friction) != 2 {
			t.Fatalf("friction after the cut pass = %q, want the first failure's two lines only", *f.friction)
		}
		syncPasses(t, f, 1)
		fixPushURL(t, f.job)
		syncPasses(t, f, 1)
		wantGitFriction(t, *f.friction, []string{
			frictionPushMsg,
			"job: git sync push failed: exit 128: …",
			"job: git sync push failed 2 more time(s) with the same cause",
			"job: git sync recovered after 3 failed attempt(s)",
		})
	})

	t.Run("a refused push after a failure still blocks the job and is no recovery", func(t *testing.T) {
		f := newSupGitFixture(t, supGitGhRules)
		advanceRemoteBranch(t, f.job)
		f.job.commitIn(t, "local")
		breakPushURL(t, f.job)
		syncPasses(t, f, 1)
		fixPushURL(t, f.job)
		syncPasses(t, f, 1)
		st := f.state(t)
		if st.Status != StatusBlocked || st.Motivo == nil || *st.Motivo != "push_rejected" {
			t.Fatalf("state = %+v, want blocked for push_rejected", st)
		}
		if got := f.tipes(t); got[len(got)-1] != "blocked" {
			t.Fatalf("event types = %v, want blocked last", got)
		}
		wantGitFriction(t, *f.friction, []string{
			frictionPushMsg,
			"job: git sync push failed: exit 128: …",
		})
	})
}
