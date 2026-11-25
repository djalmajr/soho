package spawn

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/kinds"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func seedGrokSpawnHooks(t *testing.T, root string) string {
	t.Helper()
	if out, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	dir := filepath.Join(root, ".claude")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "settings.json")
	body := []byte(`{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"reminder"}]}]}}`)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFreshGrokSpawnWarnsAboutClaudeHooksFromSubdirectory(t *testing.T) {
	f := newSpawnFixture(t, freshSpawnRules())
	configureSpawnFixture(t, &f)
	settings := seedGrokSpawnHooks(t, f.cwd)
	if _, err := fakecli.Install(t, f.bin, "git", []fakecli.Rule{
		{Argv: []string{"rev-parse", "--show-toplevel"}, Stdout: f.cwd + "\n"},
	}); err != nil {
		t.Fatal(err)
	}
	delete(f.env, "GROK_CONFIG")
	delete(f.env, "GROK_CONFIG_PATH")
	delete(f.env, "GROK_CLAUDE_HOOKS_ENABLED")
	f.env["GROK_HOME"] = filepath.Join(f.env.Get("HOME"), ".grok")
	before, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	if got := kinds.GrokClaudeHooksRisk(f.env, f.cwd); got != kinds.GrokHooksWarn {
		t.Fatalf("detector=%v, want warn", got)
	}
	after, err := os.ReadFile(settings)
	if err != nil || string(before) != string(after) {
		t.Fatalf("warning check changed hook settings: %q -> %q err=%v", before, after, err)
	}

	subdir := filepath.Join(f.cwd, "nested", "deeper")
	if err := os.MkdirAll(subdir, 0o700); err != nil {
		t.Fatal(err)
	}
	sub := f
	sub.cwd = subdir
	code, out, stderr, _ := runCmdSpawn(t, sub, []string{"worker", "--kind", "grok"})
	if code != 0 {
		t.Fatalf("spawn exit=%d out=%s stderr=%s", code, out, stderr)
	}
	if got := strings.Count(stderr, kinds.GrokClaudeHooksWarningText); got != 1 {
		t.Fatalf("warning count=%d, want 1: %q", got, stderr)
	}
}

func TestFreshGrokSpawnWarningNegativePaths(t *testing.T) {
	t.Run("non-Grok spawn", func(t *testing.T) {
		f := newSpawnFixture(t, freshSpawnRules())
		configureSpawnFixture(t, &f)
		if _, err := fakecli.Install(t, f.bin, "claude", nil); err != nil {
			t.Fatal(err)
		}
		seedGrokSpawnHooks(t, f.cwd)
		f.env["GROK_HOME"] = filepath.Join(f.env.Get("HOME"), ".grok")
		code, out, stderr, _ := runCmdSpawn(t, f, []string{"worker", "--kind", "claude"})
		if code != 0 || !strings.Contains(out, `"status":"ready"`) {
			t.Fatalf("non-Grok spawn did not start: exit=%d out=%q stderr=%q", code, out, stderr)
		}
		if strings.Contains(stderr, kinds.GrokClaudeHooksWarningText) {
			t.Fatalf("non-Grok spawn warned: %q", stderr)
		}
	})
	t.Run("reused Grok worker", func(t *testing.T) {
		rules := append([]fakecli.Rule{idleHerdrRule("old")}, freshSpawnRules()...)
		f := a12Fixture(t, rules, "1")
		seedGrokSpawnHooks(t, f.cwd)
		f.env["GROK_HOME"] = filepath.Join(f.env.Get("HOME"), ".grok")
		f.roster(t, a12Row("old", "grok", "grok-4.7", f.cwd))
		code, out, stderr, _ := runCmdSpawn(t, f, []string{"worker", "--kind", "grok", "--model", "grok-4.7"})
		if code != 0 || !strings.Contains(out, `"reused":true`) {
			t.Fatalf("Grok spawn did not reuse: exit=%d out=%q stderr=%q", code, out, stderr)
		}
		if strings.Contains(stderr, kinds.GrokClaudeHooksWarningText) {
			t.Fatalf("reused worker warned: %q", stderr)
		}
	})
}
