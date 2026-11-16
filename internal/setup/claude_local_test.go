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

func TestSetupCanonicalClaudeHookLocations(t *testing.T) {
	const localNote = "hooks stay in .claude/settings.local.json; setup --local manages them"
	const duplicateWarning = "hooks run twice"
	for _, scenario := range []string{"local only", "shared only", "both", "local with foreign shared"} {
		t.Run(scenario, func(t *testing.T) {
			root, env, ctx := tm5LocalFixture(t)
			local := filepath.Join(root, ".claude", "settings.local.json")
			shared := filepath.Join(root, ".claude", "settings.json")
			if scenario != "shared only" {
				code, out, msg := runSetupWrite([]string{"--local"}, ctx, env, root)
				if code != 0 {
					t.Fatalf("local setup: code=%d out=%s msg=%s", code, out, msg)
				}
				if err := os.Remove(filepath.Join(root, "CLAUDE.local.md")); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "both" {
				localBytes, err := os.ReadFile(local)
				if err != nil {
					t.Fatal(err)
				}
				// Distinct formatting exposes even a byte-preserving logical merge.
				writeLocalSetupFixtureFile(t, root, ".claude/settings.json", " \n"+string(localBytes))
			} else if scenario == "shared only" {
				code, out, msg := runSetupWrite(nil, ctx, env, root)
				if code != 0 {
					t.Fatalf("shared setup: code=%d out=%s msg=%s", code, out, msg)
				}
				b, err := os.ReadFile(shared)
				if err != nil {
					t.Fatal(err)
				}
				old := strings.Replace(string(b), setuptext.SetupHookReminder(), setuptext.LegacyHookReminder(), 1)
				if old == string(b) {
					t.Fatal("fixture did not contain current reminder")
				}
				writeLocalSetupFixtureFile(t, root, ".claude/settings.json", old)
			} else if scenario == "local with foreign shared" {
				writeLocalSetupFixtureFile(t, root, ".claude/settings.json", "{\"custom\": true}\n")
			}
			beforeLocal, localErr := os.ReadFile(local)
			beforeShared, sharedErr := os.ReadFile(shared)
			for _, args := range [][]string{{"--plan"}, {"--dry-run"}, nil} {
				code, out, msg := runSetupWrite(args, ctx, env, root)
				if code != 0 {
					t.Fatalf("args=%v code=%d out=%s msg=%s", args, code, out, msg)
				}
				isPreview := len(args) > 0
				if scenario == "both" {
					if strings.Count(out+msg, duplicateWarning) != 1 || !strings.Contains(out+msg, ".claude/settings.json") {
						t.Fatalf("args=%v missing warning: out=%s msg=%s", args, out, msg)
					}
				} else if scenario != "shared only" {
					if strings.Count(out+msg, localNote) != 1 {
						t.Fatalf("args=%v missing note: out=%s msg=%s", args, out, msg)
					}
				} else if strings.Contains(out+msg, localNote) || strings.Contains(out+msg, duplicateWarning) {
					t.Fatalf("unexpected note: %s %s", out, msg)
				}
				if isPreview && (scenario != "shared only") && strings.Contains(out, "settings.json\n--- a/") {
					t.Fatalf("unexpected shared diff: %s", out)
				}
				if scenario == "shared only" && len(args) > 0 && args[0] == "--plan" && (!strings.Contains(out, "settings.json\n--- a/") || !strings.Contains(out, setuptext.SetupHookReminder()) || !strings.Contains(out, setuptext.LegacyHookReminder())) {
					t.Fatalf("plan did not show the shared refresh: %s", out)
				}
				afterLocal, err := os.ReadFile(local)
				if localErr == nil && (err != nil || string(afterLocal) != string(beforeLocal)) {
					t.Fatalf("local changed: %q %v", afterLocal, err)
				}
				afterShared, err := os.ReadFile(shared)
				if scenario == "shared only" && !isPreview {
					if err != nil || !strings.Contains(string(afterShared), setuptext.SetupHookReminder()) || strings.Contains(string(afterShared), setuptext.LegacyHookReminder()) {
						t.Fatalf("shared not refreshed: %q %v", afterShared, err)
					}
				} else if sharedErr == nil && (err != nil || string(afterShared) != string(beforeShared)) {
					t.Fatalf("args=%v shared changed: %q %v", args, afterShared, err)
				} else if os.IsNotExist(sharedErr) && !os.IsNotExist(err) {
					t.Fatalf("args=%v unexpected shared file: %q %v", args, afterShared, err)
				}
			}
			if b, err := os.ReadFile(filepath.Join(root, "AGENTS.md")); err != nil || !strings.Contains(string(b), setuptext.SetupStart) {
				t.Fatalf("instruction block: %q %v", b, err)
			}
			for _, rel := range []string{".codex/hooks.json", ".pi/extensions/soho-inbound.ts"} {
				if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
					t.Fatalf("other hooks %s: %v", rel, err)
				}
			}
		})
	}
}

func TestSetupCanonicalClaudePartialAndForeignHooks(t *testing.T) {
	for _, event := range []string{"UserPromptSubmit", "SessionStart"} {
		t.Run(event, func(t *testing.T) {
			root, env, ctx := tm5LocalFixture(t)
			command, other, otherCommand := setuptext.SetupHookReminder(), "SessionStart", setuptext.SetupHookDoctor()
			if event == "SessionStart" {
				command, other, otherCommand = setuptext.SetupHookDoctor(), "UserPromptSubmit", setuptext.SetupHookReminder()
			}
			seed := jsonjs.StringifyIndent(jsonjs.O("hooks", jsonjs.O(event, []any{jsonjs.O("hooks", []any{jsonjs.O("command", command)})})), 2) + "\n"
			writeLocalSetupFixtureFile(t, root, ".claude/settings.local.json", seed)
			for _, args := range [][]string{{"--plan"}, nil} {
				code, out, msg := runSetupWrite(args, ctx, env, root)
				if code != 0 || !strings.Contains(out, "setup --local manages them") {
					t.Fatalf("args=%v code=%d out=%s msg=%s", args, code, out, msg)
				}
				if len(args) != 0 {
					if !strings.Contains(out, "+    \""+other+"\"") || strings.Contains(out, "+    \""+event+"\"") {
						t.Fatalf("plan did not show only missing event: %s", out)
					}
				} else {
					shared, err := os.ReadFile(filepath.Join(root, ".claude/settings.json"))
					if err != nil || !strings.Contains(string(shared), otherCommand) || strings.Contains(string(shared), command) {
						t.Fatalf("shared=%s err=%v", shared, err)
					}
				}
			}
			local, err := os.ReadFile(filepath.Join(root, ".claude/settings.local.json"))
			if err != nil || string(local) != seed {
				t.Fatalf("local=%s err=%v", local, err)
			}
		})
	}
	t.Run("single duplicate event", func(t *testing.T) {
		root, env, ctx := tm5LocalFixture(t)
		seed := jsonjs.StringifyIndent(jsonjs.O("hooks", jsonjs.O("SessionStart", []any{jsonjs.O("hooks", []any{jsonjs.O("command", setuptext.SetupHookDoctor())})})), 2) + "\n"
		writeLocalSetupFixtureFile(t, root, ".claude/settings.local.json", seed)
		writeLocalSetupFixtureFile(t, root, ".claude/settings.json", " \n"+seed)
		for _, args := range [][]string{{"--plan"}, nil} {
			code, out, msg := runSetupWrite(args, ctx, env, root)
			if code != 0 || strings.Count(out+msg, "hooks run twice") != 1 {
				t.Fatalf("args=%v code=%d out=%s msg=%s", args, code, out, msg)
			}
			shared, err := os.ReadFile(filepath.Join(root, ".claude/settings.json"))
			if err != nil || string(shared) != " \n"+seed {
				t.Fatalf("shared=%s err=%v", shared, err)
			}
		}
	})
	t.Run("no-hooks does not report or touch hooks", func(t *testing.T) {
		root, env, ctx := tm5LocalFixture(t)
		seed := jsonjs.StringifyIndent(jsonjs.O("hooks", jsonjs.O("SessionStart", []any{jsonjs.O("hooks", []any{jsonjs.O("command", setuptext.SetupHookDoctor())})})), 2)
		writeLocalSetupFixtureFile(t, root, ".claude/settings.local.json", seed)
		writeLocalSetupFixtureFile(t, root, ".claude/settings.json", seed)
		for _, args := range [][]string{{"--plan", "--no-hooks"}, {"--no-hooks"}} {
			code, out, msg := runSetupWrite(args, ctx, env, root)
			if code != 0 || strings.Contains(out+msg, "hooks run twice") || strings.Contains(out+msg, "setup --local manages them") {
				t.Fatalf("args=%v code=%d out=%s msg=%s", args, code, out, msg)
			}
			for _, rel := range []string{".claude/settings.local.json", ".claude/settings.json"} {
				b, err := os.ReadFile(filepath.Join(root, rel))
				if err != nil || string(b) != seed {
					t.Fatalf("%s=%q err=%v", rel, b, err)
				}
			}
		}
	})
	t.Run("lookalike is foreign", func(t *testing.T) {
		root, env, ctx := tm5LocalFixture(t)
		seed := jsonjs.StringifyIndent(jsonjs.O("hooks", jsonjs.O("UserPromptSubmit", []any{jsonjs.O("hooks", []any{jsonjs.O("command", setuptext.SetupHookReminder()+" --other")})})), 2) + "\n"
		writeLocalSetupFixtureFile(t, root, ".claude/settings.local.json", seed)
		for _, args := range [][]string{{"--plan"}, nil} {
			code, out, msg := runSetupWrite(args, ctx, env, root)
			if code != 0 || strings.Contains(out+msg, "setup --local manages them") {
				t.Fatalf("args=%v code=%d out=%s msg=%s", args, code, out, msg)
			}
		}
		shared, err := os.ReadFile(filepath.Join(root, ".claude/settings.json"))
		if err != nil || !strings.Contains(string(shared), setuptext.SetupHookReminder()) || !strings.Contains(string(shared), setuptext.SetupHookDoctor()) {
			t.Fatalf("shared=%s err=%v", shared, err)
		}
	})
}

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
			// SOH-290: a shared file that held only soho hooks is removed,
			// not left as an empty {}.
			b, err := os.ReadFile(filepath.Join(root, ".claude/settings.json"))
			if code != 0 || !os.IsNotExist(err) {
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

// SOH-290: --plan and --dry-run announce the removal of a shared file that
// holds only soho hooks, and leave it in place.
func TestSetupClaudeLocalPreviewAnnouncesTheRemoval(t *testing.T) {
	for _, mode := range []string{"--plan", "--dry-run"} {
		t.Run(mode, func(t *testing.T) {
			root, env, ctx := tm5LocalFixture(t)
			seed := jsonjs.StringifyIndent(jsonjs.O("hooks", jsonjs.O("SessionStart", []any{jsonjs.O("hooks", []any{jsonjs.O("command", setuptext.SetupHookDoctor())})})), 2)
			writeLocalSetupFixtureFile(t, root, ".claude/settings.json", seed)
			code, out, msg := runSetupWrite([]string{"--local", mode}, ctx, env, root)
			if code != 0 || !strings.Contains(out, string(filepath.Separator)+filepath.Join(".claude", "settings.json")+" (it holds only soho-managed hooks)") || !strings.Contains(out, "# would remove ") {
				t.Fatalf("code=%d out=%s msg=%s", code, out, msg)
			}
			if b, err := os.ReadFile(filepath.Join(root, ".claude/settings.json")); err != nil || string(b) != seed {
				t.Fatalf("preview changed the shared file: %s %v", b, err)
			}
		})
	}
}
