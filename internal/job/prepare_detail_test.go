package job

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// prepare_detail_test.go keeps the diagnostic of a failed preparation step
// (DJA-403): the exit and the stderr tail travel in the step error's Detail,
// `job start` records them in the job's friction log when the job directory
// exists, and no credential reaches either. The git and the gh are the
// package's fake CLI, so nothing here touches the network.

// prepareFakeGit is a preparation scenario whose git is a fake: the checkout
// is an empty directory, and the git calls that come before the step under
// test answer from rules.
type prepareFakeGit struct {
	checkout string
	env      platform.Env
	machine  Machine
}

// newPrepareFakeGit installs a fake gh that authenticates and a fake git that
// answers the toplevel and common dir probes of an existing checkout, then
// the extra rules.
func newPrepareFakeGit(t *testing.T, extra func(checkout string) []fakecli.Rule) *prepareFakeGit {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("canonicalize the fixture root: %v", err)
	}
	reposRoot := filepath.Join(root, "repos")
	checkout := filepath.Join(reposRoot, prepOrg, prepRepo)
	if err := os.MkdirAll(checkout, 0o700); err != nil {
		t.Fatal(err)
	}
	common := filepath.Join(root, "common")
	if err := os.MkdirAll(common, 0o700); err != nil {
		t.Fatal(err)
	}
	rules := append([]fakecli.Rule{
		{Argv: []string{"-C", checkout, "rev-parse", "--show-toplevel"}, Stdout: checkout + "\n"},
		{Argv: []string{"-C", checkout, "rev-parse", "--path-format=absolute", "--git-common-dir"}, Stdout: common + "\n"},
	}, extra(checkout)...)
	env := detailFakeEnv(t, rules, []fakecli.Rule{{Argv: []string{"auth", "status"}}})
	return &prepareFakeGit{
		checkout: checkout,
		env:      env,
		machine:  Machine{Orgs: []string{prepOrg}, ReposRoot: reposRoot},
	}
}

func (g *prepareFakeGit) prepare(base string) error {
	_, err := Preparer{Machine: g.machine, Env: g.env}.Prepare(PrepareRequest{ID: prepID, Org: prepOrg, Repo: prepRepo, Base: base, Mode: "worktree"})
	return err
}

func TestPrepareStepDetail(t *testing.T) {
	t.Run("a failed gh auth status keeps its exit and the last stderr line in the detail, not in the message", func(t *testing.T) {
		f := newPrepareFixture(t, []fakecli.Rule{{
			Argv: []string{"auth", "status"}, Code: 1,
			Stderr: "github.com\n  X Failed to log in to github.com account example\n  - The token in keyring is invalid.\n",
		}})
		_, err := f.preparer().Prepare(f.req("main", ""))
		wantExit(t, err, ExitPrepare, "job: preparation failed: gh authentication")
		detail := err.(*ExitError).Detail
		want := "exit 1: github.com | X Failed to log in to github.com account example | - The token in keyring is invalid."
		if detail != want {
			t.Fatalf("detail = %q, want %q", detail, want)
		}
		if strings.Contains(err.Error(), "keyring") {
			t.Fatalf("the message carries the detail: %q", err.Error())
		}
	})

	t.Run("a failed fetch keeps its exit and a redacted stderr: no piece of the token", func(t *testing.T) {
		g := newPrepareFakeGit(t, func(checkout string) []fakecli.Rule {
			return []fakecli.Rule{{
				Argv: []string{"-C", checkout, "fetch", "--prune", "origin"}, Code: 128,
				Stderr: "fatal: unable to access '" + detailTokenURL + "': Could not resolve host: github.com\n",
			}}
		})
		err := g.prepare("main")
		wantExit(t, err, ExitPrepare, "job: preparation failed: fetch")
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

	t.Run("a step that runs no subprocess to failure has no detail", func(t *testing.T) {
		f := newPrepareFixture(t, []fakecli.Rule{{Argv: []string{"auth", "status"}}})
		checkout := f.cloneCheckout()
		if err := os.WriteFile(filepath.Join(checkout, "untracked.txt"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := f.preparer().Prepare(f.req("main", "workspace"))
		wantExit(t, err, ExitPrepare, "job: preparation failed: checkout dirty")
		if detail := err.(*ExitError).Detail; detail != "" {
			t.Fatalf("detail = %q, want none", detail)
		}
	})

	t.Run("a base the remote does not resolve says the output was unexpected", func(t *testing.T) {
		g := newPrepareFakeGit(t, func(checkout string) []fakecli.Rule {
			return []fakecli.Rule{
				{Argv: []string{"-C", checkout, "fetch", "--prune", "origin"}},
				{Argv: []string{"-C", checkout, "ls-remote", "--symref", "origin", "HEAD"}, Stdout: "not a symref\n"},
			}
		})
		err := g.prepare("")
		wantExit(t, err, ExitPrepare, "job: preparation failed: unknown base")
		if detail := err.(*ExitError).Detail; detail != unexpectedOutput {
			t.Fatalf("detail = %q, want %q", detail, unexpectedOutput)
		}
	})

	t.Run("a base the checkout does not know keeps the exit of the failed rev-parse", func(t *testing.T) {
		f := newPrepareFixture(t, []fakecli.Rule{{Argv: []string{"auth", "status"}}})
		f.cloneCheckout()
		_, err := f.preparer().Prepare(f.req("nope", ""))
		wantExit(t, err, ExitPrepare, "job: preparation failed: unknown base")
		if detail := err.(*ExitError).Detail; detail != "exit 1" {
			t.Fatalf("detail = %q, want %q", detail, "exit 1")
		}
	})
}

// TestStartPrepareDetailFriction: a start whose preparation fails records the
// step's detail in the job's friction when the job directory exists, and
// writes nothing when it does not; the events and the report do not change.
func TestStartPrepareDetailFriction(t *testing.T) {
	const wantDetail = "exit 1: You are not logged into any GitHub hosts. To log in, run: gh auth login"
	ghDown := []fakecli.Rule{{
		Argv: []string{"auth", "status"}, Code: 1,
		Stderr: "You are not logged into any GitHub hosts. To log in, run: gh auth login\n",
	}}

	t.Run("a job directory that exists gets a second friction line; the events stay as they were", func(t *testing.T) {
		f := newPrepareFixture(t, ghDown)
		f.cloneCheckout()
		fake := startFake()
		stateRoot := t.TempDir()
		var friction []string
		s := startStarter(t, f, fake, stateRoot, &friction)

		_, store, err := s.Start(startRequest("main", briefA))
		wantExit(t, err, ExitPrepare, "job: preparation failed: gh authentication")
		if store == nil {
			t.Fatal("no store returned")
		}
		// The line goes to the job's friction.log and not to the sink, which
		// the CLI echoes to the stderr that carries the same line already.
		if len(friction) != 0 {
			t.Fatalf("friction sink = %q, want none", friction)
		}
		lines := frictionLogLines(t, stateRoot)
		if len(lines) != 1 || !strings.HasSuffix(lines[0], "\tjob: preparation gh authentication failed: "+wantDetail) ||
			!strings.Contains(lines[0], "\terror(exit 22)\t") {
			t.Fatalf("friction.log = %q", lines)
		}
		const motivo = "job: preparation failed: gh authentication"
		failure, terminal := failEvents(t, stateRoot, "job-1")
		if failure.Refs["motivo"] != motivo || terminal.Refs["exit"] != "22" {
			t.Fatalf("failure refs = %v terminal refs = %v", failure.Refs, terminal.Refs)
		}
		assertFailedReport(t, stateRoot, "job-1", motivo)
		raw, err := os.ReadFile(filepath.Join(stateRoot, "jobs", "job-1", "events.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "not logged into") {
			t.Fatalf("the detail reached the events: %s", raw)
		}
	})

	t.Run("a missing checkout fails before the job directory exists: nothing is written", func(t *testing.T) {
		f := newPrepareFixture(t, ghDown)
		fake := startFake()
		stateRoot := t.TempDir()
		var friction []string
		s := startStarter(t, f, fake, stateRoot, &friction)

		_, store, err := s.Start(startRequest("main", briefA))
		wantExit(t, err, ExitPrepare, "job: preparation failed: gh authentication")
		if detail := err.(*ExitError).Detail; detail != wantDetail {
			t.Fatalf("detail = %q, want %q", detail, wantDetail)
		}
		if store != nil || len(friction) != 0 {
			t.Fatalf("store = %v friction = %q, want nothing written", store, friction)
		}
		if _, statErr := os.Stat(filepath.Join(stateRoot, "jobs", "job-1")); !os.IsNotExist(statErr) {
			t.Fatalf("job directory exists: %v", statErr)
		}
	})

	t.Run("a step without a detail adds no friction line", func(t *testing.T) {
		f := newPrepareFixture(t, []fakecli.Rule{{Argv: []string{"auth", "status"}}})
		checkout := f.cloneCheckout()
		if err := os.WriteFile(filepath.Join(checkout, "untracked.txt"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		fake := startFake()
		stateRoot := t.TempDir()
		var friction []string
		s := startStarter(t, f, fake, stateRoot, &friction)
		req := startRequest("main", briefA)
		req.Mode = "workspace"

		_, store, err := s.Start(req)
		wantExit(t, err, ExitPrepare, "job: preparation failed: checkout dirty")
		if store == nil {
			t.Fatal("no store returned")
		}
		if lines := frictionLogLines(t, stateRoot); len(lines) != 0 || len(friction) != 0 {
			t.Fatalf("friction.log = %q sink = %q, want none", lines, friction)
		}
	})
}

// frictionLogLines reads the lines of job-1's friction.log; a job without one
// has none.
func frictionLogLines(t *testing.T, stateRoot string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(stateRoot, "jobs", "job-1", "friction.log"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
}
