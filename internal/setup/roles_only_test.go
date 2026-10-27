package setup

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupRolesOnly(t *testing.T) {
	for _, mode := range []string{"write", "plan", "dry", "conflict", "already-excluded"} {
		t.Run(mode, func(t *testing.T) {
			root, env, ctx := codexSetupFixture(t)
			old := filepath.Join(root, ".agents", "herdr-roles", "critical-implementer.md")
			next := filepath.Join(root, ".agents", "soho-roles", "critical-implementer.md")
			mustWriteFile(t, old, "custom role\n")
			untouched := map[string]string{"AGENTS.md": "owned AGENTS\n", "CLAUDE.md": "owned CLAUDE\n", ".claude/settings.json": "{\"hooks\":{}}\n", ".claude/settings.local.json": "local hooks\n", ".codex/hooks.json": "codex hooks\n", ".pi/extensions/soho-inbound.ts": "pi hook\n", ".gitignore": ".agents/herdr-roles/\n"}
			for path, body := range untouched {
				mustWriteFile(t, filepath.Join(root, filepath.FromSlash(path)), body)
			}
			cmd := exec.Command("git", "-C", root, "add", "--", ".gitignore")
			cmd.Env = env.List()
			if b, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("track ignore fixture: %v %s", err, b)
			}
			exclude := filepath.Join(root, ".git", "info", "exclude")
			initial := "# preserve\r\n/.agents/herdr-roles/\r\n"
			if mode == "already-excluded" {
				initial += "/.agents/soho-roles/\r\n"
			}
			mustWriteFile(t, exclude, initial)
			if mode == "conflict" {
				mustWriteFile(t, next, "destination role\n")
			}
			args := []string{"--roles"}
			if mode == "plan" {
				args = append(args, "--plan")
			}
			if mode == "dry" {
				args = append(args, "--dry-run")
			}
			code, out, msg := runSetupWrite(args, ctx, env, root)
			t.Logf("mode=%s exit=%d stdout=%s stderr=%s", mode, code, out, msg)
			if code != 0 {
				t.Fatalf("exit=%d stdout=%s stderr=%s", code, out, msg)
			}
			for path, body := range untouched {
				if got := mustReadFile(t, filepath.Join(root, filepath.FromSlash(path))); got != body {
					t.Errorf("roles-only changed %s: %q", path, got)
				}
			}
			if _, err := os.Stat(filepath.Join(root, ".soho")); !os.IsNotExist(err) {
				t.Errorf("roles-only created state: %v", err)
			}
			if mode == "plan" || mode == "dry" {
				if mustReadFile(t, old) != "custom role\n" || mustReadFile(t, exclude) != initial {
					t.Fatal("plan wrote role/exclude")
				}
				if _, err := os.Stat(next); !os.IsNotExist(err) {
					t.Fatalf("plan created destination: %v", err)
				}
				if !strings.Contains(out, "would move ") || strings.Contains(out, "hooks") || strings.Contains(out, "--- a/") {
					t.Fatalf("wrong plan: %s", out)
				}
				return
			}
			if mode == "conflict" {
				if mustReadFile(t, old) != "custom role\n" || mustReadFile(t, next) != "destination role\n" || !strings.Contains(out, "already exists; merge by hand") {
					t.Fatalf("conflict contract: %s", out)
				}
				if mustReadFile(t, exclude) != initial {
					t.Fatal("conflict-only run changed exclude")
				}
				return
			}
			if mustReadFile(t, next) != "custom role\n" {
				t.Fatal("role not moved")
			}
			if _, err := os.Stat(old); !os.IsNotExist(err) {
				t.Fatalf("old role remains: %v", err)
			}
			want := "# preserve\r\n/.agents/herdr-roles/\r\n/.agents/soho-roles/\r\n"
			if got := mustReadFile(t, exclude); got != want {
				t.Fatalf("exclude=%q want=%q", got, want)
			}
			code, again, msg := runSetupWrite([]string{"--roles"}, ctx, env, root)
			if code != 0 || again != "" || msg != "" || mustReadFile(t, exclude) != want {
				t.Fatalf("not idempotent: exit=%d out=%s stderr=%s", code, again, msg)
			}
		})
	}
}

func TestSetupRolesExcludeSpellingsAndWorktree(t *testing.T) {
	for _, rule := range []string{".agents/herdr-roles/", ".agents/herdr-roles", "/.agents/herdr-roles/", "/.agents/herdr-roles"} {
		t.Run(rule, func(t *testing.T) {
			root, env, ctx := codexSetupFixture(t)
			mustWriteFile(t, filepath.Join(root, ".agents", "herdr-roles", "custom.md"), "role\n")
			exclude := filepath.Join(root, ".git", "info", "exclude")
			mustWriteFile(t, exclude, rule)
			code, out, msg := runSetupWrite([]string{"--roles"}, ctx, env, root)
			if code != 0 {
				t.Fatalf("exit=%d out=%s msg=%s", code, out, msg)
			}
			want := rule + "\n" + strings.Replace(rule, "herdr-roles", "soho-roles", 1) + "\n"
			if got := mustReadFile(t, exclude); got != want {
				t.Fatalf("exclude=%q want=%q", got, want)
			}
		})
	}
	t.Run("linked worktree uses common exclude", func(t *testing.T) {
		root, env, ctx := codexSetupFixture(t)
		for _, args := range [][]string{{"-C", root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "fixture"}, {"-C", root, "worktree", "add", "-q", "--detach", filepath.Join(t.TempDir(), "linked")}} {
			cmd := exec.Command("git", args...)
			cmd.Env = env.List()
			if b, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v %s", args, err, b)
			}
			if args[2] == "worktree" {
				worktree := args[len(args)-1]
				mustWriteFile(t, filepath.Join(worktree, ".agents", "herdr-roles", "custom.md"), "role\n")
				exclude := filepath.Join(root, ".git", "info", "exclude")
				mustWriteFile(t, exclude, ".agents/herdr-roles/\n")
				code, out, msg := runSetupWrite([]string{"--roles"}, ctx, env, worktree)
				if code != 0 || !strings.Contains(mustReadFile(t, exclude), ".agents/soho-roles/\n") {
					t.Fatalf("exit=%d out=%s msg=%s", code, out, msg)
				}
			}
		}
	})
}
