package setup

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/setuptext"
)

func TestSetupClaudeLocal(t *testing.T) {
	// Mutation captured: selecting shared settings for local setup exposes them to Git.
	root, env, ctx := tm5LocalFixture(t)
	env["TMPDIR"] = t.TempDir()
	code, out, msg := runSetupWrite([]string{"--local"}, ctx, env, root)
	if code != 0 {
		t.Fatalf("code=%d out=%s msg=%s", code, out, msg)
	}
	b, err := os.ReadFile(filepath.Join(root, ".claude/settings.local.json"))
	if err != nil || !strings.Contains(string(b), "soho hook doctor") {
		t.Fatalf("local=%s err=%v", b, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".claude/settings.json")); !os.IsNotExist(err) {
		t.Fatalf("shared settings created: %v", err)
	}
	cmd := exec.Command("git", "-C", root, "status", "--porcelain", "--untracked-files=all", "--", ".claude")
	cmd.Env = env.List()
	status, err := cmd.CombinedOutput()
	if err != nil || len(status) != 0 {
		t.Fatalf("status=%q err=%v", status, err)
	}
	t.Logf("setup_code=%d git_status=%q local hooks present; shared absent", code, status)
}

func TestSetupClaudeLocalMigration(t *testing.T) {
	for _, tracked := range []bool{false, true} {
		t.Run(map[bool]string{false: "untracked", true: "tracked"}[tracked], func(t *testing.T) {
			root, env, ctx := tm5LocalFixture(t)
			seed := `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"echo foreign"}]}]},"custom":true}`
			merged := setuptext.SettingsHooksResult(&seed)
			writeLocalSetupFixtureFile(t, root, ".claude/settings.json", *merged)
			if tracked {
				cmd := exec.Command("git", "-C", root, "add", ".claude/settings.json")
				cmd.Env = env.List()
				if b, e := cmd.CombinedOutput(); e != nil {
					t.Fatalf("add: %s %v", b, e)
				}
			}
			var firstLocal, firstShared string
			for i := 0; i < 2; i++ {
				code, out, msg := runSetupWrite([]string{"--local"}, ctx, env, root)
				if code != 0 {
					t.Fatalf("code=%d out=%s msg=%s", code, out, msg)
				}
				shared, e := os.ReadFile(filepath.Join(root, ".claude/settings.json"))
				if e != nil {
					t.Fatal(e)
				}
				local, e := os.ReadFile(filepath.Join(root, ".claude/settings.local.json"))
				if e != nil {
					t.Fatal(e)
				}
				if tracked {
					if string(shared) != *merged || !strings.Contains(msg, "manual commit or removal") {
						t.Fatalf("shared=%s warning=%q", shared, msg)
					}
				} else if strings.Contains(string(shared), "soho hook") || !strings.Contains(string(shared), "echo foreign") || !strings.Contains(string(shared), `"custom": true`) {
					t.Fatalf("shared=%s", shared)
				}
				if !strings.Contains(string(local), "soho hook reminder") || !strings.Contains(string(local), "soho hook doctor") {
					t.Fatalf("local=%s", local)
				}
				if i == 1 && (string(local) != firstLocal || string(shared) != firstShared) {
					t.Fatal("second run changed hooks")
				}
				firstLocal, firstShared = string(local), string(shared)
			}
		})
	}
}

func TestSetupClaudeLocalPreviewAndValidation(t *testing.T) {
	for _, mode := range []string{"--plan", "--dry-run"} {
		t.Run(mode, func(t *testing.T) {
			root, env, ctx := tm5LocalFixture(t)
			writeLocalSetupFixtureFile(t, root, ".claude/settings.json", "invalid shared JSON")
			code, out, msg := runSetupWrite([]string{"--local", mode}, ctx, env, root)
			if code != 0 || !strings.Contains(out, ".claude/settings.local.json") {
				t.Fatalf("code=%d out=%s msg=%s", code, out, msg)
			}
			writeLocalSetupFixtureFile(t, root, ".claude/settings.local.json", "invalid local JSON")
			code, _, msg = runSetupWrite([]string{"--local", mode}, ctx, env, root)
			if code != 4 || !strings.Contains(msg, "settings.local.json") {
				t.Fatalf("code=%d msg=%s", code, msg)
			}
		})
	}
}

func TestSetupClaudeLocalMigrationEmptyAndLegacy(t *testing.T) {
	for _, command := range []string{setuptext.SetupHookDoctor(), setuptext.PreviousHookDoctor(), setuptext.LegacyHookDoctor()} {
		t.Run(command, func(t *testing.T) {
			root, env, ctx := tm5LocalFixture(t)
			seed := jsonjs.StringifyIndent(jsonjs.O("hooks", jsonjs.O("SessionStart", []any{jsonjs.O("hooks", []any{jsonjs.O("command", command)})})), 2)
			writeLocalSetupFixtureFile(t, root, ".claude/settings.json", seed)
			code, out, msg := runSetupWrite([]string{"--local"}, ctx, env, root)
			b, err := os.ReadFile(filepath.Join(root, ".claude/settings.json"))
			if code != 0 || err != nil || strings.TrimSpace(string(b)) != "{}" {
				t.Fatalf("code=%d out=%s msg=%s shared=%s err=%v", code, out, msg, b, err)
			}
		})
	}
}

func TestSetupClaudeLocalTrackedDestination(t *testing.T) {
	root, env, ctx := tm5LocalFixture(t)
	writeLocalSetupFixtureFile(t, root, ".claude/settings.local.json", "{\"custom\":true}\n")
	cmd := exec.Command("git", "-C", root, "add", ".claude/settings.local.json")
	cmd.Env = env.List()
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("add: %s %v", b, err)
	}
	code, out, msg := runSetupWrite([]string{"--local"}, ctx, env, root)
	b, err := os.ReadFile(excludePath(root, env))
	if code != 0 || err != nil || strings.Contains(string(b), "/.claude/settings.local.json") {
		t.Fatalf("code=%d out=%s msg=%s exclude=%s err=%v", code, out, msg, b, err)
	}
	if _, tracked := gitOutput(env, root, "ls-files", "--error-unmatch", "--", ".claude/settings.local.json"); !tracked {
		t.Fatal("local file untracked")
	}
}
