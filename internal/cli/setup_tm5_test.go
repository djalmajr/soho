package cli

import (
	"bytes"

	"os"
	"os/exec"
	"path/filepath"

	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
)

func setupTM5CommandFixture(t *testing.T) (platform.Env, string) {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is required for setup TM5 CLI fixtures")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "repo")
	home := filepath.Join(root, "home")
	conf := filepath.Join(root, "conf")
	state := filepath.Join(root, "state")
	for _, dir := range []string{repo, home, conf, state} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	init := exec.Command(git, "init", "-q")
	init.Dir = repo
	if output, err := init.CombinedOutput(); err != nil {
		t.Fatalf("git init: %s: %v", output, err)
	}
	env := platform.Env{}
	for _, item := range testutil.CleanEnv(t) {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			env[key] = value
		}
	}
	for key := range env {
		if key == "SOHO_FAKECLI_CONFIG" {
			continue
		}
		if strings.HasPrefix(strings.ToUpper(key), "HERDR_") || strings.HasPrefix(key, "SOHO_") {
			delete(env, key)
		}
	}
	env["HOME"], env["USERPROFILE"] = home, home
	env["XDG_CONFIG_HOME"], env["SOHO_DIR"] = conf, state
	env["SOHO_SKILL_DIR"] = setupTM5SkillDir(t)
	env["HERDR_WORKSPACE_ID"], env["PATH"] = "ws", ""
	return env, repo
}

func setupTM5SkillDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join(packageDir, "..", "..", "skills", "soho"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err != nil {
		t.Fatalf("skill directory %q: %v", dir, err)
	}
	return dir
}

func TestSetupTM5LocalDoctorCases(t *testing.T) {
	t.Run(`// JS: "doctor: a valid local block is enough; setup_target=local without one points at setup --local"`, func(t *testing.T) {
		env, repo := setupTM5CommandFixture(t)
		env["HERDR_ENV"] = "1"
		env["SOHO_SKILL_DIR"] = setupTM5SkillDir(t)
		old, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chdir(repo); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chdir(old) })
		conf := filepath.Join(repo, ".agents", "soho.conf")
		if err := os.MkdirAll(filepath.Dir(conf), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(conf, []byte("setup_target=local\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		run := func() string {
			t.Helper()
			oldOut, oldErr := platform.Stdout, platform.Stderr
			var out, stderr bytes.Buffer
			platform.Stdout, platform.Stderr = &out, &stderr
			t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
			if code := Run([]string{"doctor"}, env); code != 0 || stderr.Len() != 0 {
				t.Fatalf("doctor code=%d stderr=%q", code, stderr.String())
			}
			return out.String()
		}
		missing := run()
		if !strings.Contains(missing, "no soho block in CLAUDE.local.md") || !strings.Contains(missing, "setup --local") || strings.Contains(missing, "no soho block in AGENTS.md/CLAUDE.md") {
			t.Fatalf("local setup target warning=%s", missing)
		}
		if err := os.WriteFile(filepath.Join(repo, "CLAUDE.local.md"), []byte("<!-- soho:start -->\n<!-- soho:end -->\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		valid := run()
		if !strings.Contains(valid, "instruction block present in CLAUDE.local.md") || strings.Contains(valid, "no soho block") {
			t.Fatalf("valid local block status=%s", valid)
		}
	})
	t.Run(`// JS: "doctor: missing hooks point at setup --local for local setups, canonical otherwise (exact text, no tracked mutation)"`, func(t *testing.T) {
		env, repo := setupTM5CommandFixture(t)
		env["HERDR_ENV"] = "1"
		env["SOHO_SKILL_DIR"] = setupTM5SkillDir(t)
		old, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chdir(repo); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chdir(old) })
		tracked := filepath.Join(repo, "CLAUDE.md")
		if err := os.WriteFile(tracked, []byte("upstream\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		local := filepath.Join(repo, "CLAUDE.local.md")
		if err := os.WriteFile(local, []byte("<!-- soho:start -->\n<!-- soho:end -->\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		conf := filepath.Join(repo, ".agents", "soho.conf")
		if err := os.MkdirAll(filepath.Dir(conf), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(conf, []byte("setup_target=local\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		beforeTracked, _ := os.ReadFile(tracked)
		// The setup guidance names the native executable (the running test
		// binary; no SOHO_BIN override in this fixture env), not the
		// obsolete skill scripts launcher.
		entry := platform.LauncherPath(env)
		run := func() string {
			t.Helper()
			oldOut, oldErr := platform.Stdout, platform.Stderr
			var out, stderr bytes.Buffer
			platform.Stdout, platform.Stderr = &out, &stderr
			code := Run([]string{"doctor"}, env)
			platform.Stdout, platform.Stderr = oldOut, oldErr
			if code != 0 || stderr.Len() != 0 {
				t.Fatalf("doctor code=%d stderr=%q", code, stderr.String())
			}
			return out.String()
		}
		localOutput := run()
		localWant := "warn   no soho hooks in .claude/settings.json: run '" + entry + " setup --local' (UserPromptSubmit reminder + SessionStart doctor)"
		if !strings.Contains(normalizeGoldenRootPathSeparators(localOutput), normalizeGoldenRootPathSeparators(localWant)) {
			t.Fatalf("missing local hooks warning %q in %s", localWant, localOutput)
		}
		if err := os.WriteFile(conf, []byte("# canonical default\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(local); err != nil {
			t.Fatal(err)
		}
		canonicalOutput := run()
		canonicalWant := "warn   no soho hooks in .claude/settings.json: run '" + entry + " setup' (UserPromptSubmit reminder + SessionStart doctor)"
		if !strings.Contains(normalizeGoldenRootPathSeparators(canonicalOutput), normalizeGoldenRootPathSeparators(canonicalWant)) {
			t.Fatalf("missing canonical hooks warning %q in %s", canonicalWant, canonicalOutput)
		}
		afterTracked, _ := os.ReadFile(tracked)
		if !bytes.Equal(beforeTracked, afterTracked) {
			t.Fatalf("doctor mutated tracked instruction: before=%q after=%q", beforeTracked, afterTracked)
		}
	})
}
