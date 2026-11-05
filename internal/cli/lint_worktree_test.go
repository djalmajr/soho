package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

// DJA-684: soho lint <brief> --role reviewer prints the no-Worktree advisory
// without refusing: the exit code is the one the lint findings already
// produce, strict mode included.
func TestLintReviewWorktreeAdvisory(t *testing.T) {
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
	brief := filepath.Join(root, "review.md")
	clean := "# Goal\nReview the slice.\n# Expected result\nDone.\n# Owned files\nnone\n# Forbidden\nNo commit/push.\n# Report\nDone.\n"
	writeLintWorktreeFile(t, brief, clean)
	const want = "has no Worktree: line: this review cannot be tied to its author, so 'soho stats --by author-model' will not count it; add 'Worktree: <absolute path>' here and in the implementer's brief"
	run := func(args []string, env platform.Env) (int, string, string) {
		var stdout, stderr bytes.Buffer
		oldOut, oldErr := platform.Stdout, platform.Stderr
		platform.Stdout, platform.Stderr = &stdout, &stderr
		defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
		rc := Run(args, env)
		return rc, stdout.String(), stderr.String()
	}
	base := platform.Env{
		"PATH":            os.Getenv("PATH"),
		"HOME":            filepath.Join(root, "home"),
		"XDG_CONFIG_HOME": filepath.Join(root, "config"),
		"SOHO_DIR":        filepath.Join(root, "state"),
		"SOHO_SKILL_DIR":  skillDir,
	}

	// Reviewer role, clean brief, no Worktree: line: the advisory prints,
	// the exit code stays 0.
	for mode, code := range map[string]int{"warn": 0, "strict": 0} {
		env := base.Clone()
		env["SOHO_BRIEF_LINT"] = mode
		rc, out, errText := run([]string{"lint", brief, "--role", "reviewer"}, env)
		if rc != code {
			t.Fatalf("mode %s: exit %d, want %d: %s%s", mode, rc, code, out, errText)
		}
		if !strings.Contains(errText, "soho: warning: brief "+brief+" "+want) {
			t.Fatalf("mode %s: advisory missing: stderr=%q", mode, errText)
		}
		if strings.Contains(errText, "(brief_lint=strict)") {
			t.Fatalf("mode %s: the advisory must not refuse: %s", mode, errText)
		}
	}

	// A review brief that declares the line is silent.
	writeLintWorktreeFile(t, brief, "Worktree: "+root+"\n"+clean)
	env := base.Clone()
	env["SOHO_BRIEF_LINT"] = "warn"
	rc, out, errText := run([]string{"lint", brief, "--role", "reviewer"}, env)
	if rc != 0 || strings.Contains(errText, "Worktree: line") {
		t.Fatalf("declared worktree: rc=%d out=%q stderr=%q", rc, out, errText)
	}

	// A non-review role stays silent and its exit code is unchanged.
	writeLintWorktreeFile(t, brief, clean)
	rc, out, errText = run([]string{"lint", brief}, env)
	if rc != 0 || strings.Contains(errText, "Worktree: line") || out != "brief "+brief+": ok\n" {
		t.Fatalf("non-review role: rc=%d out=%q stderr=%q", rc, out, errText)
	}
}

func writeLintWorktreeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
