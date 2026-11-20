package setup

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/setuptext"
)

func noticeCount(message string) int {
	count := 0
	for _, line := range strings.Split(message, "\n") {
		if line == implicitLocalNotice {
			count++
		}
	}
	return count
}

func assertLocalChoice(t *testing.T, root, out, message string) {
	t.Helper()
	if noticeCount(message) != 1 {
		t.Fatalf("stderr must carry one implicit-local line, got %q", message)
	}
	if strings.Contains(message, "keeps the soho block in CLAUDE.local.md") {
		t.Fatalf("the old refusal must not fire: %q", message)
	}
	local, err := os.ReadFile(filepath.Join(root, localInstructionFile))
	if err != nil || !strings.Contains(string(local), setuptext.SetupStart) {
		t.Fatalf("local block missing: %q err=%v stdout=%q", local, err, out)
	}
	if _, err := os.Stat(filepath.Join(root, "CLAUDE.local.md")); err != nil {
		t.Fatalf("local instruction file: %v", err)
	}
}

func assertCanonicalChoice(t *testing.T, root, out, message string) {
	t.Helper()
	if noticeCount(message) != 0 {
		t.Fatalf("stderr must not carry the implicit-local line, got %q", message)
	}
	if _, err := os.Stat(filepath.Join(root, localInstructionFile)); !os.IsNotExist(err) {
		t.Fatalf("canonical setup wrote CLAUDE.local.md: %v; stdout=%q", err, out)
	}
	agents, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if err != nil || !strings.Contains(string(agents), setuptext.SetupStart) {
		t.Fatalf("canonical block missing: %q err=%v stdout=%q", agents, err, out)
	}
}

func TestSetupImplicitLocalFreshRepositoryWritesLocalFiles(t *testing.T) {
	root, env, ctx := setupWriteUntrackedFixture(t)
	// A tracked file that is not a soho file must not change the choice.
	writeLocalSetupFixtureFile(t, root, "README.md", "project readme\n")
	gitIn(t, root, "add", "README.md")
	writeLocalSetupFixtureFile(t, root, "AGENTS.md", "project rules\n")
	writeLocalSetupFixtureFile(t, root, ".claude/settings.json", "{\"custom\":true}\n")
	code, out, message := runSetupWrite(nil, ctx, env, root)
	if code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out, message)
	}
	assertLocalChoice(t, root, out, message)
	if got := mustReadFile(t, filepath.Join(root, "AGENTS.md")); got != "project rules\n" {
		t.Fatalf("canonical instructions changed: %q", got)
	}
	if got := mustReadFile(t, filepath.Join(root, ".claude/settings.json")); got != "{\"custom\":true}\n" {
		t.Fatalf("canonical settings changed: %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, ".claude", "settings.local.json")); err != nil {
		t.Fatalf("local Claude hooks missing: %v", err)
	}
	if _, err := os.Stat(codexFile(root)); err != nil {
		t.Fatalf("Codex hooks missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".pi", "extensions", "soho-inbound.ts")); err != nil {
		t.Fatalf("pi extension missing: %v", err)
	}
}

func TestSetupImplicitLocalTrackedSohoFilesKeepCanonical(t *testing.T) {
	for _, tc := range []struct {
		name, rel, content string
		deleteFromWorktree bool
	}{
		{name: "canonical block", rel: "AGENTS.md", content: setuptext.SetupBlock()},
		{name: "hook", rel: codexHookRel, content: "{}\n"},
		{name: "hook deleted from work tree", rel: codexHookRel, content: "{}\n", deleteFromWorktree: true},
		{name: "project config", rel: ".agents/soho.conf", content: "panes=3\n"},
		{name: "role file", rel: ".agents/soho-roles/implementer.md", content: "fixture role\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, env, ctx := setupWriteUntrackedFixture(t)
			writeLocalSetupFixtureFile(t, root, tc.rel, tc.content)
			gitIn(t, root, "add", tc.rel)
			if tc.deleteFromWorktree {
				if err := os.Remove(filepath.Join(root, filepath.FromSlash(tc.rel))); err != nil {
					t.Fatal(err)
				}
			}
			code, out, message := runSetupWrite([]string{"--no-hooks"}, ctx, env, root)
			if code != 0 {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, out, message)
			}
			assertCanonicalChoice(t, root, out, message)
		})
	}
}

func TestSetupImplicitLocalOutsideGitKeepsCanonical(t *testing.T) {
	_, env, _ := setupWriteFixture(t)
	root := t.TempDir()
	ctx := core.LoadConfig(env, root)
	code, out, message := runSetupWrite([]string{"--no-hooks"}, &ctx, env, root)
	if code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out, message)
	}
	assertCanonicalChoice(t, root, out, message)
}

func TestSetupImplicitLocalTargetKeepsCanonical(t *testing.T) {
	root, env, ctx := setupWriteUntrackedFixture(t)
	code, out, message := runSetupWrite([]string{"--target", "AGENTS.md", "--no-hooks"}, ctx, env, root)
	if code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out, message)
	}
	assertCanonicalChoice(t, root, out, message)
}

func TestSetupImplicitLocalExplicitCanonicalKeepsCanonical(t *testing.T) {
	for _, tc := range []struct {
		name  string
		entry bool
		env   bool
	}{
		{name: "project config", entry: true},
		{name: "environment", env: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, env, ctx := setupWriteUntrackedFixture(t)
			if tc.entry {
				ctx.Entries["setup_target"] = core.ConfigEntry{Value: "canonical", Source: "project"}
			}
			if tc.env {
				env["SOHO_SETUP_TARGET"] = "canonical"
			}
			code, out, message := runSetupWrite([]string{"--no-hooks"}, ctx, env, root)
			if code != 0 {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, out, message)
			}
			assertCanonicalChoice(t, root, out, message)
		})
	}
}

func TestSetupImplicitLocalPlanAndDryRunShowChoiceAndReason(t *testing.T) {
	for _, mode := range []string{"--plan", "--dry-run"} {
		t.Run(mode, func(t *testing.T) {
			root, env, ctx := setupWriteUntrackedFixture(t)
			before := setupFilesystemSnapshot(t, root)
			code, out, message := runSetupWrite([]string{mode}, ctx, env, root)
			if code != 0 {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, out, message)
			}
			if noticeCount(message) != 1 {
				t.Fatalf("missing reason in stderr: %q", message)
			}
			if !strings.Contains(out, localInstructionFile) {
				t.Fatalf("preview does not show the local choice: %q", out)
			}
			if after := setupFilesystemSnapshot(t, root); !reflect.DeepEqual(before, after) {
				t.Fatalf("%s changed the repository", mode)
			}
		})
	}
}

func TestSetupImplicitLocalReplacesLocalBlockRefusal(t *testing.T) {
	root, env, ctx := setupWriteUntrackedFixture(t)
	writeLocalSetupFixtureFile(t, root, localInstructionFile, setuptext.SetupBlock())
	code, out, message := runSetupWrite([]string{"--no-hooks"}, ctx, env, root)
	if code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out, message)
	}
	assertLocalChoice(t, root, out, message)
}
