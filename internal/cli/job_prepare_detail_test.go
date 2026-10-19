package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// job_prepare_detail_test.go: a `job start` whose preparation fails says why
// on a second stderr line (DJA-403); the message and the exit 22 are the same
// as before. The gh is a fake that fails its authentication, so nothing here
// touches the network.

const prepareDetailLine = "job: preparation gh authentication failed: exit 1: You are not logged into any GitHub hosts. To log in, run: gh auth login"

// prepareDetailEnv is the start env with a gh that is not authenticated.
func prepareDetailEnv(t *testing.T, ee *jobStartEE) platform.Env {
	t.Helper()
	dir := t.TempDir()
	if _, err := fakecli.Install(t, dir, "gh", []fakecli.Rule{{
		Argv: []string{"auth", "status"}, Code: 1,
		Stderr: "You are not logged into any GitHub hosts. To log in, run: gh auth login\n",
	}}); err != nil {
		t.Fatalf("install the fake gh: %v", err)
	}
	if _, err := fakecli.Install(t, dir, "herdr", []fakecli.Rule{
		{Argv: []string{"workspace", "list"}, Stdout: `{"result":{"workspaces":[]}}`},
	}); err != nil {
		t.Fatalf("install the fake herdr: %v", err)
	}
	env := platform.Env{}
	for _, item := range fakecli.Env(ee.f.env.List(), dir, fakecli.EnvOptions{SystemPath: filepath.Join(ee.f.root, "bin")}) {
		if key, value, ok := strings.Cut(item, "="); ok && key != "" {
			env[key] = value
		}
	}
	return env
}

func TestJobStartPrintsThePreparationDetail(t *testing.T) {
	t.Run("the job directory exists: two stderr lines, exit 22, and the line in the job's friction", func(t *testing.T) {
		ee := newJobStartEE(t)
		env := prepareDetailEnv(t, ee)
		briefFile := ee.f.briefFile(t, jobStartBrief)
		code, _, errOut := ee.f.runEnv(t, env, "start", "--id", "job-1", "--repo", "example-org/example-repo", "--brief", briefFile)
		if code != 22 {
			t.Fatalf("code=%d err=%q, want 22", code, errOut)
		}
		want := "soho: job: preparation failed: gh authentication\nsoho: " + prepareDetailLine + "\n"
		if errOut != want {
			t.Fatalf("stderr = %q, want %q", errOut, want)
		}
		cfg := core.LoadConfig(env, ee.checkout)
		raw, err := os.ReadFile(filepath.Join(core.StateRootPath(&cfg, env, ee.checkout), "jobs", "job-1", "friction.log"))
		if err != nil {
			t.Fatalf("friction.log: %v", err)
		}
		if got := strings.Count(string(raw), prepareDetailLine); got != 1 {
			t.Fatalf("friction.log holds the line %d times: %q", got, raw)
		}
	})

	t.Run("a missing checkout: the same two stderr lines and nothing written for the job", func(t *testing.T) {
		ee := newJobStartEE(t)
		env := prepareDetailEnv(t, ee)
		if err := os.RemoveAll(ee.checkout); err != nil {
			t.Fatal(err)
		}
		briefFile := ee.f.briefFile(t, jobStartBrief)
		code, out, errOut := ee.f.runEnv(t, env, "start", "--id", "job-1", "--repo", "example-org/example-repo", "--brief", briefFile)
		if code != 22 {
			t.Fatalf("code=%d err=%q, want 22", code, errOut)
		}
		want := "soho: job: preparation failed: gh authentication\nsoho: " + prepareDetailLine + "\n"
		if errOut != want || out != "" {
			t.Fatalf("stdout = %q stderr = %q, want %q", out, errOut, want)
		}
		if _, err := os.Stat(ee.checkout); !os.IsNotExist(err) {
			t.Fatalf("the checkout was recreated: %v", err)
		}
	})
}
