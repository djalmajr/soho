package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

func TestSetupReadFlagRouting(t *testing.T) {
	// Mutation captured: routing setup read flags through JS needs an unavailable runtime instead of Go's parser error.
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, errOut bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &errOut
	t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
	env := setupReadFlagEnv(t)
	code := Run([]string{"setup", "--probe", "--invalid"}, env)
	if code != 2 || out.Len() != 0 || errOut.String() != "soho: setup --probe: unknown option '--invalid'\n" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
}

func TestSetupDetectValidatesFullArgv(t *testing.T) {
	for _, tc := range []struct {
		name, arg, want string
	}{
		{name: "unknown option", arg: "--bogus", want: "soho: setup: unknown option '--bogus'\n"},
		{name: "missing value", arg: "--panes", want: "soho: setup: --panes expects a value\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldOut, oldErr := platform.Stdout, platform.Stderr
			var out, errOut bytes.Buffer
			platform.Stdout, platform.Stderr = &out, &errOut
			t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
			home := t.TempDir()
			env := platform.Env{"PATH": t.TempDir(), "HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "SOHO_SKILL_DIR": t.TempDir()}
			code := Run([]string{"setup", "--detect", tc.arg}, env)
			if code != 2 || out.Len() != 0 || errOut.String() != tc.want {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
			}
		})
	}
	t.Run("valid detect still emits JSON", func(t *testing.T) {
		oldOut, oldErr := platform.Stdout, platform.Stderr
		var out, errOut bytes.Buffer
		platform.Stdout, platform.Stderr = &out, &errOut
		t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
		home := t.TempDir()
		env := platform.Env{"PATH": t.TempDir(), "HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "SOHO_SKILL_DIR": t.TempDir()}
		code := Run([]string{"setup", "--detect"}, env)
		if code != 0 || errOut.Len() != 0 || !strings.HasPrefix(out.String(), "{\n  \"kinds\"") {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
		}
	})
}

func TestSetupWriteRejectsSetBeforeAnyWrites(t *testing.T) {
	// Mutation captured: performing setup writes before rejecting --set leaves files despite the CLI error.
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is required for setup CLI integration test")
	}
	root := t.TempDir()
	cmd := exec.Command(git, "init", "-q", root)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	oldCwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCwd) })
	skill, err := filepath.Abs(filepath.Join(packageDir, "..", "..", "skills", "soho"))
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	env := platform.Env{"PATH": filepath.Dir(git), "HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "SOHO_SKILL_DIR": skill, "SOHO_DIR": ".herdr-soho"}
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, errOut bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &errOut
	t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
	code := Run([]string{"setup", "--target", "GUIDE.md", "--no-hooks", "--panes", "3", "--lane", "build=pi", "--set", "max_workers", "5"}, env)
	if code != 2 || out.Len() != 0 || errOut.String() != "soho: setup: unknown option '--set'\n" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	for _, file := range []string{"GUIDE.md", ".agents/herdr-soho.conf", ".gitignore", ".claude/settings.json"} {
		if _, err := os.Stat(filepath.Join(root, file)); !os.IsNotExist(err) {
			t.Errorf("write-mode refusal left %s: %v", file, err)
		}
	}
}

func setupReadFlagEnv(t *testing.T) platform.Env {
	t.Helper()
	return isolatedUserEnv(platform.Env{"PATH": t.TempDir(), "HOME": t.TempDir(), "SOHO_SKILL_DIR": t.TempDir()})
}
