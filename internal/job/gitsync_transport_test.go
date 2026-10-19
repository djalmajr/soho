package job

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// gitsync_transport_test.go keeps a push the remote refused apart from a
// push that never reached it: only a rejected ref line (`!` in the
// --porcelain output) is Rejected and blocks the job; the network, the
// credentials or a missing repository are a git sync error the supervisor
// logs and retries on the next cycle. The transport failure is a
// remote.origin.pushurl that points nowhere: ls-remote (the fetch url) keeps
// working, the push exits 128 and prints no ref line.

// breakPushURL points the push url of origin at a path that is no
// repository.
func breakPushURL(t *testing.T, j *jobSyncFixture) {
	t.Helper()
	missing := filepath.Join(j.base.root, "nonexistent.git")
	if out, err := j.base.git(j.dir, "config", "remote.origin.pushurl", missing); err != nil {
		t.Fatalf("git config pushurl: %s: %v", out, err)
	}
}

// fixPushURL puts the push back on the fetch url.
func fixPushURL(t *testing.T, j *jobSyncFixture) {
	t.Helper()
	if out, err := j.base.git(j.dir, "config", "--unset", "remote.origin.pushurl"); err != nil {
		t.Fatalf("git config --unset pushurl: %s: %v", out, err)
	}
}

// advanceRemoteBranch makes another clone push a commit to the job branch
// and returns the remote head, so a local commit is a non-fast-forward.
func advanceRemoteBranch(t *testing.T, j *jobSyncFixture) string {
	t.Helper()
	other := filepath.Join(j.base.root, "other")
	if out, err := j.base.git(j.base.root, "clone", "-q", j.base.bare, other); err != nil {
		t.Fatalf("clone the second clone: %s: %v", out, err)
	}
	for _, args := range [][]string{
		{"checkout", "-q", "-b", gitSyncBranch, "refs/remotes/origin/main"},
		{"commit", "-q", "--allow-empty", "-m", "other"},
		{"push", "-q", "origin", gitSyncBranch},
	} {
		if out, err := j.base.git(other, args...); err != nil {
			t.Fatalf("setup git %v in the second clone: %s: %v", args, out, err)
		}
	}
	return j.remoteRef(t)
}

func TestGitSyncPushTransport(t *testing.T) {
	t.Run("a push the remote hook declines is rejected without an error", func(t *testing.T) {
		f := newSupGitFixture(t, nil)
		installRemoteHook(t, f, []fakecli.Rule{{AnyArgs: true, Code: 1}})
		sha := f.job.commitIn(t, "feat: first")
		res, err := f.sup.Git.Push()
		if err != nil || !res.Rejected || res.Pushed || res.Head != sha {
			t.Fatalf("Push = %+v err=%v, want rejected without error", res, err)
		}
		if got := f.job.remoteRef(t); got != "" {
			t.Fatalf("remote ref = %q, want nothing landed", got)
		}
		requireRemoteHookCalls(t, f, 1)
	})

	t.Run("a push that fails on the transport is an error, never a rejection", func(t *testing.T) {
		j := newJobSyncFixture(t, nil)
		sha := j.commitIn(t, "first")
		breakPushURL(t, j)
		// The fetch side still works: the head comparison reaches the remote
		// and only the push fails.
		if _, err := j.sync.RemoteHead(); err != nil {
			t.Fatalf("RemoteHead with the broken push url: %v", err)
		}
		res, err := j.sync.Push()
		wantExit(t, err, ExitFailed, "job: git sync failed: push")
		if res.Rejected || res.Pushed || res.Head != sha {
			t.Fatalf("Push result = %+v, want neither rejected nor pushed, head %s", res, sha)
		}
		exitErr := err.(*ExitError)
		if !strings.HasPrefix(exitErr.Detail, "exit 128") {
			t.Fatalf("detail = %q, want it to start with exit 128", exitErr.Detail)
		}
		if got := j.remoteRef(t); got != "" {
			t.Fatalf("remote ref = %q, want nothing landed", got)
		}
		// The failure leaves nothing behind: the next push, once the url
		// is right, lands.
		fixPushURL(t, j)
		res, err = j.sync.Push()
		if err != nil || !res.Pushed || res.Rejected || res.Head != sha {
			t.Fatalf("Push after the fix = %+v err=%v, want pushed", res, err)
		}
		assertSyncPostconditions(t, j)
	})
}

func TestSupervisorPushTransportFailure(t *testing.T) {
	t.Run("a transport failure keeps the job running and the next cycle pushes", func(t *testing.T) {
		f := newSupGitFixture(t, supGitGhRules)
		sha := f.job.commitIn(t, "feat: first")
		breakPushURL(t, f.job)

		f.tickN(t, 6)
		if st := f.state(t); st.Status != StatusRunning || st.Motivo != nil {
			t.Fatalf("state = %+v, want running without a motivo", st)
		}
		// The commit is announced; no push, blocked or push_rejected event.
		want := []string{"accepted", "worker_spawned", "commit"}
		if got := f.tipes(t); !reflect.DeepEqual(got, want) {
			t.Fatalf("event types = %v, want %v", got, want)
		}
		for _, e := range f.events(t) {
			if e.Refs["motivo"] == "push_rejected" {
				t.Fatalf("event %+v carries push_rejected", e)
			}
		}
		if len(*f.friction) != 2 || (*f.friction)[0] != "job: git sync failed: push" || !strings.HasPrefix((*f.friction)[1], "job: git sync push failed: exit 128: ") {
			t.Fatalf("friction = %v, want the push step and its exit 128 detail", *f.friction)
		}
		if got := f.job.remoteRef(t); got != "" {
			t.Fatalf("remote ref = %q, want nothing landed", got)
		}

		// The network is back: the next cycle pushes and opens the PR.
		fixPushURL(t, f.job)
		f.tickN(t, 6)
		if got := f.job.remoteRef(t); got != sha {
			t.Fatalf("remote ref = %q, want the pushed head %q", got, sha)
		}
		want = append(want, "push", "pr_opened")
		if got := f.tipes(t); !reflect.DeepEqual(got, want) {
			t.Fatalf("event types = %v, want %v", got, want)
		}
		if push := f.events(t)[3]; push.Resumo != "pushed" || push.Refs["sha"] != sha {
			t.Fatalf("push event = %+v, want pushed at %s", push, sha)
		}
		if st := f.state(t); st.Status != StatusRunning {
			t.Fatalf("state = %+v, want running", st)
		}
		assertSyncPostconditions(t, f.job)
	})

	t.Run("a refused push keeps the job blocked through a later transport failure", func(t *testing.T) {
		f := newSupGitFixture(t, supGitGhRules)
		remoteSHA := advanceRemoteBranch(t, f.job)
		f.job.commitIn(t, "local")

		f.tickN(t, 6)
		st := f.state(t)
		if st.Status != StatusBlocked || st.Motivo == nil || *st.Motivo != "push_rejected" {
			t.Fatalf("state = %+v, want blocked for push_rejected", st)
		}
		want := []string{"accepted", "worker_spawned", "commit", "push", "blocked"}
		if got := f.tipes(t); !reflect.DeepEqual(got, want) {
			t.Fatalf("event types = %v, want %v", got, want)
		}
		frictionBefore := len(*f.friction)

		// The next cycle cannot reach the remote: the block neither lifts
		// nor changes, and the failure is only logged.
		breakPushURL(t, f.job)
		f.tickN(t, 6)
		st = f.state(t)
		if st.Status != StatusBlocked || st.Motivo == nil || *st.Motivo != "push_rejected" {
			t.Fatalf("state after the transport failure = %+v, want still blocked for push_rejected", st)
		}
		if got := f.tipes(t); !reflect.DeepEqual(got, want) {
			t.Fatalf("event types after the transport failure = %v, want %v", got, want)
		}
		if got := (*f.friction)[frictionBefore:]; len(got) != 2 || got[0] != "job: git sync failed: push" || !strings.HasPrefix(got[1], "job: git sync push failed: exit 128: ") {
			t.Fatalf("friction after the transport failure = %v, want the push step and its exit 128 detail", got)
		}
		if got := f.job.remoteRef(t); got != remoteSHA {
			t.Fatalf("remote ref = %q, want %q", got, remoteSHA)
		}
		assertSyncPostconditions(t, f.job)
	})
}
