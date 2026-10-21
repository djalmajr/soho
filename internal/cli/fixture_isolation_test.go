package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// warmFixtureTrap reproduces a previous command caching the linked checkout's
// roots. All config/state paths belong to this test, never the real checkout.
func warmFixtureTrap(t *testing.T) (string, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(root, "main checkout")
	linked := filepath.Join(main, ".worktrees", "linked checkout")
	project := filepath.Join(main, ".agents", "soho.conf")
	session := filepath.Join(main, ".soho", "ws", "session.conf")
	for _, dir := range []string{linked, filepath.Dir(project), filepath.Dir(session), filepath.Join(root, "home"), filepath.Join(root, "conf")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(project, []byte("lane.build.kind=codex\nfamily_check=off\nherd_label=TRAP_PROJECT\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(session, []byte("herd_label=TRAP_SESSION\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	user := filepath.Join(root, "conf", "soho", "config")
	if err := os.MkdirAll(filepath.Dir(user), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(user, []byte("herd_label=TRAP_USER\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("USERPROFILE", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "conf"))
	bin := t.TempDir()
	if _, err := fakecli.Install(t, bin, "git", []fakecli.Rule{
		{Argv: []string{"rev-parse", "--show-toplevel"}, Stdout: linked + "\n"},
		{Argv: []string{"rev-parse", "--git-dir"}, Stdout: filepath.Join(main, ".git", "worktrees", "linked") + "\n"},
		{Argv: []string{"rev-parse", "--git-common-dir"}, Stdout: filepath.Join(main, ".git") + "\n"},
	}); err != nil {
		t.Fatal(err)
	}
	t.Chdir(linked)
	env := platform.Env{
		"HOME": filepath.Join(root, "home"), "USERPROFILE": filepath.Join(root, "home"),
		"XDG_CONFIG_HOME": filepath.Join(root, "conf"), "TMPDIR": root,
		"SOHO_SKILL_DIR": testSkillDir(t), "HERDR_WORKSPACE_ID": "ws",
	}
	env = withFakeCLI(env, bin)
	code, out, stderr := runIn(t, []string{"config"}, env, linked)
	if code != 0 || stderr != "" || !strings.Contains(out, "TRAP_SESSION") || !strings.Contains(out, "lane.build.kind") || !strings.Contains(out, "codex") {
		t.Fatalf("trap control: code=%d stdout=%q stderr=%q", code, out, stderr)
	}
	t.Log("trap control: config reads the synthetic main checkout and session; roots are now cached")
	return project, session
}

func TestFixtureIsolation(t *testing.T) {
	for _, fixture := range []string{"release", "dispatch arrival"} {
		t.Run(fixture, func(t *testing.T) {
			project, session := warmFixtureTrap(t)
			beforeProject, err := os.ReadFile(project)
			if err != nil {
				t.Fatal(err)
			}
			beforeSession, err := os.ReadFile(session)
			if err != nil {
				t.Fatal(err)
			}
			var code int
			var out, stderr string
			if fixture == "release" {
				f := newReopenFixture(t, "working")
				config := f.runCommand(t, "config")
				code, out, stderr = config.code, config.stdout, config.stderr
				if strings.Contains(out, "TRAP_") {
					t.Fatalf("release fixture read the trap: code=%d stdout=%q stderr=%q", code, out, stderr)
				}
				setReopenRules(t, f)
				run := f.reopen(t, "worker", "--force")
				if run.code != 0 || !strings.Contains(run.stdout, `"name": "worker"`) {
					t.Fatalf("isolated reopen: code=%d stdout=%q stderr=%q", run.code, run.stdout, run.stderr)
				}
			} else {
				f := releasedForFixture(t, "anthropic")
				code, out, stderr = runArrivalCommand(t, f, "config")
				if strings.Contains(out, "TRAP_") {
					t.Fatalf("dispatch fixture read the trap: code=%d stdout=%q stderr=%q", code, out, stderr)
				}
				addReleasedForSidecar(t, f, "build", "20260927T101001", "pi", "anthropic/claude-3", "accepted", false)
				rc, stdout, errOut := f.run(t, "worker", f.brief, "--for", "build", "--no-wait")
				if rc != 5 || stdout != "" || !strings.Contains(errOut, "shares a model family") {
					t.Fatalf("isolated family guard: code=%d stdout=%q stderr=%q", rc, stdout, errOut)
				}
				calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
				if err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				if countArgvPrefix(calls, []string{"agent", "prompt"}) != 0 {
					t.Fatalf("family refusal sent a prompt: %v", calls)
				}
			}
			if code != 0 || stderr != "" || strings.Contains(out, "TRAP_") {
				t.Fatalf("fixture config: code=%d stdout=%q stderr=%q", code, out, stderr)
			}
			afterProject, err := os.ReadFile(project)
			if err != nil || string(afterProject) != string(beforeProject) {
				t.Fatalf("trap project changed: %q, err=%v", afterProject, err)
			}
			afterSession, err := os.ReadFile(session)
			if err != nil || string(afterSession) != string(beforeSession) {
				t.Fatalf("trap session changed: %q, err=%v", afterSession, err)
			}
		})
	}
}
