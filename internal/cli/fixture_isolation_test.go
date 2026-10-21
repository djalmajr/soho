package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

var fixtureIsolationRoot string

// isolatedUserEnv preserves explicit fixture paths and fills absent user paths
// from the process environment isolated by TestMain.
func isolatedUserEnv(env platform.Env) platform.Env {
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME"} {
		if _, exists := env[key]; !exists {
			env[key] = os.Getenv(key)
		}
	}
	return env
}

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
	t.Run("explicit environments", func(t *testing.T) {
		cases := []struct {
			name string
			env  func(*testing.T) platform.Env
		}{
			{"compact phase status", func(t *testing.T) platform.Env { return newCompactPhaseStatusFixture(t, false).env }},
			{"roster", func(t *testing.T) platform.Env { return nowriteRoutingEnv() }},
			{"dispatch context", func(t *testing.T) platform.Env {
				root := t.TempDir()
				return contextWarnDispatchEnv(t, filepath.Join(root, "bin"), filepath.Join(root, "state"), filepath.Join(root, "roles"), root)
			}},
			{"dispatch no wait", func(t *testing.T) platform.Env {
				root := t.TempDir()
				return noWaitDispatchEnv(t, filepath.Join(root, "bin"), filepath.Join(root, "state"), filepath.Join(root, "roles"), root)
			}},
			{"compact phase dispatch", func(t *testing.T) platform.Env { return newCompactPhaseDispatchFixture(t, "", 0).env }},
			{"busy dispatch", func(t *testing.T) platform.Env { return newBusyDispatchFixture(t).env }},
			{"setup read flags", setupReadFlagEnv},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				env := tc.env(t)
				// Reject absent keys before HomeDir could reach os/user.Current.
				for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME"} {
					assertFixturePath(t, key, env.Get(key))
				}
				for _, target := range []string{"linux", "win32"} {
					assertFixturePath(t, target+" HomeDir", platform.HomeDir(target, env))
					assertFixturePath(t, target+" UserConfigPath", platform.UserConfigPath(target, env))
				}
			})
		}
		t.Run("preserves explicit entries", func(t *testing.T) {
			explicit := platform.Env{}
			for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME"} {
				explicit[key] = filepath.Join(t.TempDir(), key)
			}
			got := isolatedUserEnv(explicit.Clone())
			for key, want := range explicit {
				if got.Get(key) != want {
					t.Fatalf("%s=%q; want explicit value %q", key, got.Get(key), want)
				}
			}
			// Presence, including an intentionally empty value, wins over defaults.
			if got := isolatedUserEnv(platform.Env{"HOME": ""}); got.Get("HOME") != "" {
				t.Fatalf("explicit empty HOME overwritten: %q", got.Get("HOME"))
			}
		})
	})
	t.Run("package environment", func(t *testing.T) {
		cwd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		root := fixtureIsolationRoot
		if root == "" || cwd != filepath.Join(root, "cwd") {
			t.Fatalf("package cwd=%q isolation root=%q; want the TestMain cwd", cwd, root)
		}
		repo := filepath.Clean(filepath.Join(packageDir, "..", ".."))
		rel, err := filepath.Rel(repo, cwd)
		sameVolume := strings.EqualFold(filepath.VolumeName(repo), filepath.VolumeName(cwd))
		if sameVolume && (err != nil || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))) {
			t.Fatalf("package cwd=%q is inside repository=%q: rel=%q err=%v", cwd, repo, rel, err)
		}
		for key, want := range map[string]string{
			"HOME": filepath.Join(root, "home"), "USERPROFILE": filepath.Join(root, "home"),
			"XDG_CONFIG_HOME": filepath.Join(root, "conf"),
		} {
			if got := os.Getenv(key); got != want {
				t.Fatalf("package %s=%q; want TestMain directory %q", key, got, want)
			}
		}
		trap := t.TempDir()
		main := filepath.Join(trap, "main checkout")
		linked := filepath.Join(main, ".worktrees", "linked checkout")
		project := filepath.Join(main, ".agents", "soho.conf")
		for _, dir := range []string{linked, filepath.Dir(project)} {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(project, []byte("herd_label=TRAP_PACKAGE_PROJECT\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		bin := t.TempDir()
		if _, err := fakecli.Install(t, bin, "git", []fakecli.Rule{
			{Argv: []string{"rev-parse", "--show-toplevel"}, Call: 1, Stdout: linked + "\n"},
			{Argv: []string{"rev-parse", "--git-dir"}, Call: 1, Stdout: filepath.Join(main, ".git", "worktrees", "linked") + "\n"},
			{Argv: []string{"rev-parse", "--git-common-dir"}, Call: 1, Stdout: filepath.Join(main, ".git") + "\n"},
			{AnyArgs: true, Code: 1},
		}); err != nil {
			t.Fatal(err)
		}
		env := withFakeCLI(envFrom(testutil.CleanEnv(t)), bin)
		env["SOHO_SKILL_DIR"], env["SOHO_DIR"], env["HERDR_WORKSPACE_ID"] = testSkillDir(t), filepath.Join(trap, "state"), "ws"
		// A unique Git environment keeps this control independent of earlier
		// package commands; only synthetic roots are returned by the fake.
		env["GIT_DIR"], env["GIT_COMMON_DIR"], env["GIT_WORK_TREE"] = filepath.Join(main, ".git", "worktrees", "linked"), filepath.Join(main, ".git"), linked
		code, out, stderr := runIn(t, []string{"config"}, env, packageDir)
		if code != 0 || stderr != "" || !strings.Contains(out, "TRAP_PACKAGE_PROJECT") {
			t.Fatalf("package trap control: code=%d stdout=%q stderr=%q", code, out, stderr)
		}
		// No command fixture and no Chdir: exercise Run at the ambient cwd.
		code, out, stderr = isolationConfig(env)
		if code != 0 || stderr != "" || strings.Contains(out, "TRAP_") {
			t.Fatalf("ambient package config: code=%d stdout=%q stderr=%q", code, out, stderr)
		}
	})
	t.Run("dispatch home without XDG", func(t *testing.T) {
		home := filepath.Join(t.TempDir(), "trap home")
		for _, file := range []string{
			filepath.Join(home, ".config", "soho", "config"),
			filepath.Join(home, "AppData", "Roaming", "soho", "config"),
		} {
			if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte("herd_label=TRAP_HOME\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		t.Setenv("HOME", home)
		t.Setenv("USERPROFILE", home)
		f := newDispatchArrivalFixture(t, "idle", 1, 1, "", "0")
		env := f.env.Clone()
		delete(env, "XDG_CONFIG_HOME")
		delete(env, "APPDATA")
		control := env.Clone()
		control["HOME"], control["USERPROFILE"] = home, home
		code, out, stderr := isolationConfig(control)
		if code != 0 || stderr != "" || !strings.Contains(out, "TRAP_HOME") {
			t.Fatalf("home trap control: code=%d stdout=%q stderr=%q", code, out, stderr)
		}
		code, out, stderr = isolationConfig(env)
		if code != 0 || stderr != "" || strings.Contains(out, "TRAP_") {
			t.Fatalf("dispatch HOME config without XDG: code=%d stdout=%q stderr=%q", code, out, stderr)
		}
		for _, key := range []string{"HOME", "USERPROFILE"} {
			if got := env.Get(key); got != filepath.Join(f.root, "home") {
				t.Fatalf("dispatch %s=%q; want fixture home %q", key, got, filepath.Join(f.root, "home"))
			}
		}
	})
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

func assertFixturePath(t *testing.T, label, path string) {
	t.Helper()
	if path == "" || !filepath.IsAbs(path) || fixtureIsolationRoot == "" {
		t.Fatalf("%s=%q; want absolute path inside TestMain root %q", label, path, fixtureIsolationRoot)
	}
	rel, err := filepath.Rel(fixtureIsolationRoot, filepath.Clean(path))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		t.Fatalf("%s=%q outside TestMain root %q: rel=%q err=%v", label, path, fixtureIsolationRoot, rel, err)
	}
}

func isolationConfig(env platform.Env) (int, string, string) {
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, stderr bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &stderr
	defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
	code := Run([]string{"config"}, env)
	return code, out.String(), stderr.String()
}
