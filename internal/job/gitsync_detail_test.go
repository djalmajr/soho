package job

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// gitsync_detail_test.go keeps the diagnostic of a failed git sync step
// (DJA-395): the exit and the stderr tail travel in the step error's Detail,
// the supervisor records them in a second friction line, and no credential
// reaches either. The git and the gh are the package's fake CLI, so nothing
// here touches the network.

const (
	detailToken    = "ghp_0123456789abcdefABCDEF0123456789abcd"
	detailPAT      = "github_pat_11ABCDEFG0123456789_abcdefghijklmnopqrstuvwxyz"
	detailTokenURL = "https://x-access-token:" + detailToken + "@github.com/example-org/example-repo.git/"
)

// detailFakeEnv installs fake git and gh with the rules and returns the env
// whose PATH reaches them first.
func detailFakeEnv(t *testing.T, gitRules, ghRules []fakecli.Rule) platform.Env {
	t.Helper()
	dir := t.TempDir()
	if gitRules != nil {
		if _, err := fakecli.Install(t, dir, "git", gitRules); err != nil {
			t.Fatalf("install the fake git: %v", err)
		}
	}
	if ghRules != nil {
		if _, err := fakecli.Install(t, dir, "gh", ghRules); err != nil {
			t.Fatalf("install the fake gh: %v", err)
		}
	}
	return envFromPairs(t, fakecli.Env(nil, dir))
}

func detailRemoteArgv(dir, branch string) []string {
	return []string{"-C", dir, "ls-remote", "origin", "refs/heads/" + branch}
}

func detailHeadArgv(dir string) []string { return []string{"-C", dir, "rev-parse", "HEAD"} }

func detailLogArgv(dir string) []string {
	return []string{"-C", dir, "log", "--reverse", "--format=%H%x09%s", "refs/remotes/origin/main..HEAD"}
}

// detailLines returns the friction lines the sink holds.
func detailLines(f *supGitFixture) []string { return *f.friction }

func TestGitSyncStepDetail(t *testing.T) {
	const dir = "/work/job"
	const branch = "job/abc123"
	netStderr := "fatal: unable to access '" + detailTokenURL + "': Could not resolve host: github.com\n"

	t.Run("a failed ls-remote keeps its exit and a redacted stderr in the detail, not in the message", func(t *testing.T) {
		env := detailFakeEnv(t, []fakecli.Rule{
			{Argv: detailRemoteArgv(dir, branch), Code: 128, Stderr: netStderr},
		}, nil)
		g := &GitSync{Env: env, Dir: dir, Branch: branch, Base: "main", Repo: gitSyncRepo}
		_, err := g.RemoteHead()
		wantExit(t, err, ExitFailed, "job: git sync failed: remote head")
		detail := err.(*ExitError).Detail
		if !strings.HasPrefix(detail, "exit 128: fatal: unable to access 'https://[redacted]@github.com/example-org/example-repo.git/'") {
			t.Fatalf("detail = %q", detail)
		}
		if !strings.Contains(detail, "Could not resolve host: github.com") {
			t.Fatalf("detail lost the cause: %q", detail)
		}
		for _, leak := range []string{detailToken, "0123456789abcdef", "x-access-token"} {
			if strings.Contains(detail, leak) || strings.Contains(err.Error(), leak) {
				t.Fatalf("the credential %q leaked: detail=%q msg=%q", leak, detail, err.Error())
			}
		}
	})

	t.Run("the supervisor records the message and then the detail, and nothing else changes", func(t *testing.T) {
		f := newSupGitFixture(t, nil)
		f.sup.Git.Env = detailFakeEnv(t, []fakecli.Rule{
			{Argv: detailHeadArgv(f.job.dir), Stdout: "1111111111111111111111111111111111111111\n"},
			{Argv: detailLogArgv(f.job.dir)},
			{Argv: detailRemoteArgv(f.job.dir, supGitBranch), Code: 128, Stderr: netStderr},
		}, nil)
		before := len(f.events(t))
		code, done, err := f.sup.syncGitPass(nil)
		if err != nil || done || code != 0 {
			t.Fatalf("syncGitPass = %d %v %v", code, done, err)
		}
		want := []string{
			"job: git sync failed: remote head",
			"job: git sync remote head failed: exit 128: fatal: unable to access 'https://[redacted]@github.com/example-org/example-repo.git/': Could not resolve host: github.com",
		}
		if got := detailLines(f); strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("friction = %q\nwant      %q", got, want)
		}
		for _, line := range detailLines(f) {
			if strings.Contains(line, detailToken) || strings.Contains(line, "x-access-token") {
				t.Fatalf("a credential reached the friction: %q", line)
			}
		}
		if after := len(f.events(t)); after != before {
			t.Fatalf("the failed step changed the events: %d -> %d", before, after)
		}
	})

	t.Run("a failed gh keeps its exit and the token in its stderr is redacted", func(t *testing.T) {
		env := detailFakeEnv(t, nil, []fakecli.Rule{
			{Argv: ghListArgv(), Code: 1, Stderr: "gh: HTTP 401: Bad credentials (token " + detailToken + ")\n"},
		})
		g := &GitSync{Env: env, Dir: "/work/job", Branch: gitSyncBranch, Base: "main", Repo: gitSyncRepo}
		_, _, err := g.EnsureDraftPR("title", "body")
		wantExit(t, err, ExitFailed, "job: git sync failed: pr")
		detail := err.(*ExitError).Detail
		if detail != "exit 1: gh: HTTP 401: Bad credentials (token [redacted])" {
			t.Fatalf("detail = %q", detail)
		}
		if strings.Contains(err.Error(), "401") {
			t.Fatalf("the detail leaked into the message: %q", err.Error())
		}
	})

	t.Run("an ls-remote with the wrong number of fields says unexpected output", func(t *testing.T) {
		env := detailFakeEnv(t, []fakecli.Rule{
			{Argv: detailRemoteArgv(dir, branch), Stdout: "abc def ghi\n"},
		}, nil)
		g := &GitSync{Env: env, Dir: dir, Branch: branch, Base: "main", Repo: gitSyncRepo}
		_, err := g.RemoteHead()
		wantExit(t, err, ExitFailed, "job: git sync failed: remote head")
		if got := err.(*ExitError).Detail; got != "unexpected output" {
			t.Fatalf("detail = %q, want unexpected output", got)
		}
	})

	t.Run("a pr step whose gh prints garbage says unexpected output", func(t *testing.T) {
		env := detailFakeEnv(t, nil, []fakecli.Rule{
			{Argv: ghListArgv(), Stdout: "not json\n"},
		})
		g := &GitSync{Env: env, Dir: "/work/job", Branch: gitSyncBranch, Base: "main", Repo: gitSyncRepo}
		_, _, err := g.EnsureDraftPR("title", "body")
		wantExit(t, err, ExitFailed, "job: git sync failed: pr")
		if got := err.(*ExitError).Detail; got != "unexpected output" {
			t.Fatalf("detail = %q, want unexpected output", got)
		}
	})

	t.Run("the supervisor records the unexpected output as the second line", func(t *testing.T) {
		f := newSupGitFixture(t, nil)
		f.sup.Git.Env = detailFakeEnv(t, []fakecli.Rule{
			{Argv: detailHeadArgv(f.job.dir), Stdout: "1111111111111111111111111111111111111111\n"},
			{Argv: detailLogArgv(f.job.dir)},
			{Argv: detailRemoteArgv(f.job.dir, supGitBranch), Stdout: "a b c\n"},
		}, nil)
		if _, _, err := f.sup.syncGitPass(nil); err != nil {
			t.Fatal(err)
		}
		want := "job: git sync failed: remote head\njob: git sync remote head failed: unexpected output"
		if got := strings.Join(detailLines(f), "\n"); got != want {
			t.Fatalf("friction = %q, want %q", got, want)
		}
	})

	t.Run("a step cut by the cancel returns errCancelled and records no detail line", func(t *testing.T) {
		f := newSupGitFixture(t, nil)
		f.sup.Git.Env = detailFakeEnv(t, []fakecli.Rule{
			{Argv: detailHeadArgv(f.job.dir), Code: 128, Stderr: netStderr},
		}, nil)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		f.sup.Git.Context = ctx
		if _, err := f.sup.Git.Head(); !errors.Is(err, errCancelled) {
			t.Fatalf("Head under a cancelled context = %v, want errCancelled", err)
		}
		if _, _, err := f.sup.syncGitPass(nil); err != nil {
			t.Fatal(err)
		}
		if got := detailLines(f); len(got) != 0 {
			t.Fatalf("friction = %q, want none for a cut step", got)
		}
	})

	t.Run("a step error without a detail records only the message line", func(t *testing.T) {
		f := newSupGitFixture(t, nil)
		f.sup.frictionGit(gitSyncErrorDetail("head", ""))
		if got := detailLines(f); len(got) != 1 || got[0] != "job: git sync failed: head" {
			t.Fatalf("friction = %q, want the message line only", got)
		}
	})

	t.Run("a push whose git is missing says the executable was not found", func(t *testing.T) {
		g := &GitSync{Env: platform.Env{"PATH": t.TempDir()}, Dir: dir, Branch: branch, Base: "main", Repo: gitSyncRepo}
		_, err := g.Push()
		wantExit(t, err, ExitFailed, "job: git sync failed: push")
		if got := err.(*ExitError).Detail; got != "executable not found" {
			t.Fatalf("detail = %q, want executable not found", got)
		}
	})
}

func TestRedactDetail(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"userinfo with a token only", "fatal: https://" + detailToken + "@github.com/o/r", "fatal: https://[redacted]@github.com/o/r"},
		{"userinfo with user and password", "ssh://git:s3cr3t@host.example/repo", "ssh://[redacted]@host.example/repo"},
		{"a password holding an @", "https://user:p@ss@host.example/x", "https://[redacted]@host.example/x"},
		{"every prefix of a github token", "ghp_" + strings.Repeat("a", 20) + " gho_" + strings.Repeat("b", 20) + " ghs_" + strings.Repeat("c", 20) + " ghu_" + strings.Repeat("d", 20) + " ghr_" + strings.Repeat("e", 20), "[redacted] [redacted] [redacted] [redacted] [redacted]"},
		{"a fine-grained token", "token " + detailPAT + " rejected", "token [redacted] rejected"},
		{"a token of 19 characters is kept", "ghp_" + strings.Repeat("a", 19), "ghp_" + strings.Repeat("a", 19)},
		{"the shared patterns still apply", "Authorization: Bearer abc.def-123", "Authorization: [redacted]"},
		{"a plain line is untouched", "Could not resolve host: github.com", "Could not resolve host: github.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactDetail(tc.in)
			if tc.name == "the shared patterns still apply" {
				if strings.Contains(got, "abc.def-123") {
					t.Fatalf("redactDetail(%q) = %q, the bearer survived", tc.in, got)
				}
				return
			}
			if got != tc.want {
				t.Fatalf("redactDetail(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestStepFailureDetailRedactsBeforeTheCut(t *testing.T) {
	// The token starts before byte 400 and ends after it: cutting first
	// would leave a prefix long enough to be a token.
	line := strings.Repeat("x", 380) + " " + detailToken
	got := stepFailureDetail(platform.RunResult{Status: ptrInt(1), Stderr: line + "\n"})
	if strings.Contains(got, "ghp_") {
		t.Fatalf("a token fragment survived the cut: %q", got)
	}
	if !strings.Contains(got, "[redacted]") || len(got) > 400 {
		t.Fatalf("detail = %q (%d bytes)", got, len(got))
	}
}

func TestSelfCLIDetailRedactsGithubToken(t *testing.T) {
	f := newTeamOpsFix(t, []fakecli.Rule{
		{ArgvPrefix: true, Argv: []string{"spawn", "job-orchestrator"}, Code: 7, Stderr: "soho: spawn: clone failed with " + detailPAT + " in the URL\n"},
	})
	_, err := f.cli().SpawnOrchestrator()
	wantExit(t, err, ExitHerdr, "job: cannot spawn the job orchestrator")
	got := err.(*ExitError).Detail
	if got != "exit 7: soho: spawn: clone failed with [redacted] in the URL" {
		t.Fatalf("detail = %q", got)
	}
	if strings.Contains(got, "github_pat_") {
		t.Fatalf("the token leaked: %q", got)
	}
}

func ptrInt(n int) *int { return &n }
