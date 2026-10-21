package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

func TestLintCommandReadOnlyAndExitModes(t *testing.T) {
	t.Run("// JS: \"lint: supports read-only roles, modes, and the defined error exits\"", func(t *testing.T) {
		root := t.TempDir()
		skillDir, err := filepath.Abs(filepath.Join(packageDir, "..", "..", "skills", "soho"))
		if err != nil {
			t.Fatal(err)
		}
		oldCwd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chdir(root); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = os.Chdir(oldCwd) }()
		for _, dir := range []string{"home", "config", "state"} {
			if err := os.Mkdir(filepath.Join(root, dir), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		brief := filepath.Join(root, "brief with spaces.md")
		valid := "# Goal\nDo the slice.\n# Expected result\nDone.\n# Owned files\nsrc/a.go\n# Forbidden\nNo commit/push.\n# Report\nDone.\n"
		if err := os.WriteFile(brief, []byte(valid), 0o600); err != nil {
			t.Fatal(err)
		}
		env := platform.Env{
			"PATH":            os.Getenv("PATH"),
			"HOME":            filepath.Join(root, "home"),
			"XDG_CONFIG_HOME": filepath.Join(root, "config"),
			"SOHO_DIR":        filepath.Join(root, "state"),
			"SOHO_SKILL_DIR":  skillDir,
			"SOHO_BRIEF_LINT": "warn",
		}
		oldOut, oldErr := platform.Stdout, platform.Stderr
		var stdout, stderr bytes.Buffer
		platform.Stdout, platform.Stderr = &stdout, &stderr
		defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()

		if code := Run([]string{"lint", brief}, env); code != 0 || stdout.String() != "brief "+brief+": ok\n" || stderr.Len() != 0 {
			t.Fatalf("clean lint: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
		stdout.Reset()
		stderr.Reset()
		if code := Run([]string{"lint", brief, "--role", "reviewer"}, env); code != 0 || stdout.String() != "brief "+brief+": ok\n" || stderr.Len() != 0 {
			t.Fatalf("read-only role: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}

		noOwned := strings.Replace(valid, "# Owned files\nsrc/a.go\n", "", 1)
		if err := os.WriteFile(brief, []byte(noOwned), 0o600); err != nil {
			t.Fatal(err)
		}
		stdout.Reset()
		stderr.Reset()
		if code := Run([]string{"lint", brief, "--role", "reviewer"}, env); code != 0 || stdout.String() != "brief "+brief+": ok\n" || stderr.Len() != 0 {
			t.Fatalf("read-only role without owned files: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
		stdout.Reset()
		stderr.Reset()
		if code := Run([]string{"lint", brief}, env); code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "warning: brief "+brief+" is missing sections: [Owned files]") {
			t.Fatalf("warn edit role: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
		stdout.Reset()
		stderr.Reset()
		strict := env.Clone()
		strict["SOHO_BRIEF_LINT"] = "strict"
		if code := Run([]string{"lint", brief}, strict); code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), " (brief_lint=strict)\n") {
			t.Fatalf("strict mode: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
		stdout.Reset()
		stderr.Reset()
		off := env.Clone()
		off["SOHO_BRIEF_LINT"] = "off"
		if code := Run([]string{"lint", brief}, off); code != 0 || stdout.String() != "brief "+brief+": lint off (brief_lint=off)\n" || stderr.Len() != 0 {
			t.Fatalf("off mode: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}

		if _, err := os.Stat(filepath.Join(root, "state", "friction.log")); !os.IsNotExist(err) {
			t.Fatalf("lint created friction log: %v", err)
		}
		entries, err := os.ReadDir(filepath.Join(root, "state"))
		if err != nil || len(entries) != 0 {
			t.Fatalf("lint changed state dir: entries=%v err=%v", entries, err)
		}
	})
}

func TestLintCommandUsageAndErrors(t *testing.T) {
	t.Run("// JS: \"lint: supports read-only roles, modes, and the defined error exits\"", func(t *testing.T) {
		root := t.TempDir()
		skillDir, err := filepath.Abs(filepath.Join(packageDir, "..", "..", "skills", "soho"))
		if err != nil {
			t.Fatal(err)
		}
		oldCwd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chdir(root); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = os.Chdir(oldCwd) }()
		for _, dir := range []string{"home", "config", "state"} {
			if err := os.Mkdir(filepath.Join(root, dir), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		env := platform.Env{"PATH": os.Getenv("PATH"), "HOME": filepath.Join(root, "home"), "XDG_CONFIG_HOME": filepath.Join(root, "config"), "SOHO_DIR": filepath.Join(root, "state"), "SOHO_SKILL_DIR": skillDir}
		oldOut, oldErr := platform.Stdout, platform.Stderr
		var stdout, stderr bytes.Buffer
		platform.Stdout, platform.Stderr = &stdout, &stderr
		defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
		for _, tc := range []struct {
			args []string
			code int
			want string
		}{
			{[]string{"lint"}, 2, "usage: lint <brief.md> [--role <role>]"},
			{[]string{"lint", "--role"}, 2, "usage: lint <brief.md> [--role <role>]"},
			{[]string{"lint", "absent.md"}, 2, "lint: brief not found: absent.md"},
			{[]string{"lint", "missing-role.md", "--role", "unknown-role"}, 2, "lint: brief not found: missing-role.md"},
		} {
			stdout.Reset()
			stderr.Reset()
			if got := Run(tc.args, env); got != tc.code || !strings.Contains(stderr.String(), tc.want) {
				t.Errorf("Run(%v) = %d stderr=%q; want %d containing %q", tc.args, got, stderr.String(), tc.code, tc.want)
			}
		}
		brief := filepath.Join(root, "ok.md")
		if err := os.WriteFile(brief, []byte("# Goal\n# Expected result\n# Owned files\n# Forbidden\nNo push\n# Report\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"lint", brief, "--role"}, {"lint", brief, "--role", "--bogus"}, {"lint", brief, "--bogus"}} {
			stdout.Reset()
			stderr.Reset()
			if got := Run(args, env); got != 2 || stdout.Len() != 0 || stderr.String() != "soho: usage: lint <brief.md> [--role <role>]\n" {
				t.Errorf("invalid args %v: code=%d stdout=%q stderr=%q", args, got, stdout.String(), stderr.String())
			}
		}
		stdout.Reset()
		stderr.Reset()
		if got := Run([]string{"lint", brief, "--role", "unknown-role"}, env); got != 3 || stderr.String() != "soho: lint: unknown role 'unknown-role'\n" {
			t.Fatalf("unknown role: code=%d stderr=%q", got, stderr.String())
		}
	})
}
