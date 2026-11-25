package doctor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/kinds"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestDoctorWarnsAboutGrokClaudeHooksFromSubdirectory(t *testing.T) {
	env, root := paneContextFixture(t, nil)
	if out, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	if _, err := fakecli.Install(t, env.Get("PATH"), "git", []fakecli.Rule{
		{Argv: []string{"rev-parse", "--show-toplevel"}, Stdout: root + "\n"},
	}); err != nil {
		t.Fatal(err)
	}
	settingsDir := filepath.Join(root, ".claude")
	if err := os.MkdirAll(settingsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settingsDir, "settings.json"), []byte(`{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"reminder"}]}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	subdir := filepath.Join(root, "nested", "deeper")
	if err := os.MkdirAll(subdir, 0o700); err != nil {
		t.Fatal(err)
	}
	env["GROK_HOME"] = filepath.Join(env.Get("HOME"), ".grok")
	got := doctorCheckOutput(t, env, subdir)
	if !strings.Contains(got, kinds.GrokClaudeHooksWarningText) {
		t.Fatalf("doctor from %s lacks the Grok hook warning:\n%s", subdir, got)
	}
}

func TestDoctorWarnsAboutGrokClaudeHooks(t *testing.T) {
	env, root := paneContextFixture(t, nil)
	home := env.Get("HOME")
	settings := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(`{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"reminder"}]}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	env["GROK_HOME"] = filepath.Join(home, ".grok")
	got := doctorCheckOutput(t, env, root)
	if !strings.Contains(got, kinds.GrokClaudeHooksWarningText) {
		t.Fatalf("ordinary doctor output lacks the Grok hook warning:\n%s", got)
	}
}
