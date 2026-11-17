package setup

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/setuptext"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestTrackedSetupLocalExtension(t *testing.T) {
	for _, mode := range []string{"", "--plan"} {
		t.Run(mode, func(t *testing.T) {
			root, env, ctx := setupWriteFixture(t)
			rel := ".pi/extensions/soho-inbound.ts"
			old := setuptext.PiInboundExtension() + "\n// old\n"
			writeLocalSetupFixtureFile(t, root, rel, old)
			cmd := exec.Command("git", "-C", root, "add", rel)
			cmd.Env = env.List()
			if b, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git add: %s %v", b, err)
			}
			args := []string{"--local"}
			if mode != "" {
				args = append(args, mode)
			}
			code, out, msg := runSetupWrite(args, ctx, env, root)
			raw, err := os.ReadFile(filepath.Join(root, rel))
			if code != 0 || err != nil || string(raw) != old || !strings.Contains(out, "is tracked; --local leaves it as is") {
				t.Fatalf("code=%d out=%s msg=%s content=%q err=%v", code, out, msg, raw, err)
			}
		})
	}
}

func TestTrackedSetupClaudeBlock(t *testing.T) {
	for _, mode := range []string{"", "--plan"} {
		t.Run(mode, func(t *testing.T) {
			root, env, ctx := setupWriteFixture(t)
			writeLocalSetupFixtureFile(t, root, "CLAUDE.md", setuptext.SetupBlock())
			cmd := exec.Command("git", "-C", root, "add", "CLAUDE.md")
			cmd.Env = env.List()
			if b, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git add: %s %v", b, err)
			}
			args := []string{"--local", "--no-hooks"}
			if mode != "" {
				args = append(args, mode)
			}
			code, out, msg := runSetupWrite(args, ctx, env, root)
			_, err := os.Stat(filepath.Join(root, "CLAUDE.local.md"))
			if code != 0 || !os.IsNotExist(err) || !strings.Contains(out, "soho block already in") {
				t.Fatalf("code=%d out=%s msg=%s stat=%v", code, out, msg, err)
			}
		})
	}
}

func TestTrackedSetupCanonicalBranch(t *testing.T) {
	for _, mode := range []string{"", "--plan"} {
		for _, branch := range []string{"main", "work"} {
			t.Run(mode+branch, func(t *testing.T) {
				root, env, ctx := setupWriteFixture(t)
				writeLocalSetupFixtureFile(t, root, "AGENTS.md", "project\n")
				for _, args := range [][]string{{"checkout", "-b", branch}, {"add", "AGENTS.md"}} {
					cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
					cmd.Env = env.List()
					if b, err := cmd.CombinedOutput(); err != nil {
						t.Fatalf("git: %s %v", b, err)
					}
				}
				// An unborn main is a default branch too.
				args := []string{"--no-hooks"}
				if mode != "" {
					args = append(args, mode)
				}
				code, out, msg := runSetupWrite(args, ctx, env, root)
				has := strings.Contains(out, "is tracked; make this change on a work branch and open a PR")
				if code != 0 || has != (branch == "main") {
					t.Fatalf("code=%d out=%s msg=%s", code, out, msg)
				}
			})
		}
	}
}

func TestTrackedSetupClaudeVariants(t *testing.T) {
	for _, scenario := range []string{"old", "duplicate", "symlink"} {
		t.Run(scenario, func(t *testing.T) {
			root, env, ctx := setupWriteFixture(t)
			name := "CLAUDE.md"
			content := setuptext.SetupBlock()
			if scenario == "old" {
				content = strings.Replace(content, "soho", "soho", 1) + ""
				content = strings.Replace(content, "<!-- soho:end -->", "old\n<!-- soho:end -->", 1)
			}
			if scenario == "symlink" {
				name = "AGENTS.md"
			}
			writeLocalSetupFixtureFile(t, root, name, content)
			if scenario == "symlink" {
				if err := os.Symlink("AGENTS.md", filepath.Join(root, "CLAUDE.md")); err != nil {
					t.Skip(err)
				}
			}
			if scenario == "duplicate" {
				writeLocalSetupFixtureFile(t, root, "CLAUDE.local.md", setuptext.SetupBlock())
			}
			cmd := exec.Command("git", "-C", root, "add", name)
			cmd.Env = env.List()
			if b, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git add: %s %v", b, err)
			}
			code, out, msg := runSetupWrite([]string{"--local", "--no-hooks"}, ctx, env, root)
			if code != 0 || !strings.Contains(out, "soho block already in") {
				t.Fatalf("code=%d out=%s msg=%s", code, out, msg)
			}
			if scenario == "old" && !strings.Contains(out, "its block is older") {
				t.Fatalf("missing old: %s", out)
			}
			if scenario == "duplicate" && (strings.Count(out, "local copy can be removed") != 1 || mustReadFile(t, filepath.Join(root, "CLAUDE.local.md")) != setuptext.SetupBlock()) {
				t.Fatalf("duplicate: %s", out)
			}
		})
	}
}

func TestTrackedClaudeLinkToUntrackedTarget(t *testing.T) {
	for _, mode := range []string{"", "--plan"} {
		t.Run(mode, func(t *testing.T) {
			root, env, ctx := setupWriteFixture(t)
			writeLocalSetupFixtureFile(t, root, "AGENTS.md", setuptext.SetupBlock())
			if err := os.Symlink("AGENTS.md", filepath.Join(root, "CLAUDE.md")); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}
			cmd := exec.Command("git", "-C", root, "add", "CLAUDE.md")
			cmd.Env = env.List()
			if b, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git add: %s %v", b, err)
			}
			args := []string{"--local", "--no-hooks"}
			if mode != "" {
				args = append(args, mode)
			}
			code, out, msg := runSetupWrite(args, ctx, env, root)
			_, err := os.Stat(filepath.Join(root, "CLAUDE.local.md"))
			if code != 0 || !os.IsNotExist(err) || !strings.Contains(out, "soho block already in") {
				t.Fatalf("code=%d out=%s msg=%s err=%v", code, out, msg, err)
			}
		})
	}
}

func TestTrackedClaudeDuplicateLocalSymlink(t *testing.T) {
	for _, mode := range []string{"", "--plan"} {
		t.Run(mode, func(t *testing.T) {
			root, env, ctx := setupWriteFixture(t)
			writeLocalSetupFixtureFile(t, root, "CLAUDE.md", setuptext.SetupBlock())
			writeLocalSetupFixtureFile(t, root, "existing.md", setuptext.SetupBlock())
			if err := os.Symlink("existing.md", filepath.Join(root, "CLAUDE.local.md")); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}
			cmd := exec.Command("git", "-C", root, "add", "CLAUDE.md")
			cmd.Env = env.List()
			if b, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git add: %s %v", b, err)
			}
			args := []string{"--local", "--no-hooks"}
			if mode != "" {
				args = append(args, mode)
			}
			code, out, msg := runSetupWrite(args, ctx, env, root)
			if code != 0 || strings.Count(out, "local copy can be removed") != 1 {
				t.Fatalf("code=%d out=%s msg=%s", code, out, msg)
			}
			if got := mustReadFile(t, filepath.Join(root, "existing.md")); got != setuptext.SetupBlock() {
				t.Fatalf("local target changed: %q", got)
			}
		})
	}
}

func TestTrackingFailureProtectsManagedFile(t *testing.T) {
	for _, mode := range []string{"", "--plan"} {
		for _, local := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/local=%v", mode, local), func(t *testing.T) {
				root, env, ctx := setupWriteFixture(t)
				rel := "AGENTS.md"
				if local {
					rel = localInstructionFile
				}
				writeLocalSetupFixtureFile(t, root, rel, "original\n")
				env["PATH"] = t.TempDir()
				args := []string{"--no-hooks"}
				if local {
					args = append(args, "--local")
				}
				if mode != "" {
					args = append(args, mode)
				}
				code, out, msg := runSetupWrite(args, ctx, env, root)
				got := mustReadFile(t, filepath.Join(root, rel))
				if code != 0 || !strings.Contains(out, "could not tell whether") || (local && got != "original\n") || (!local && mode == "" && !strings.Contains(got, setuptext.SetupStart)) {
					t.Fatalf("code=%d out=%s msg=%s file=%q", code, out, msg, got)
				}
			})
		}
	}
}

func TestSetupTrackingLookupOnce(t *testing.T) {
	root, env, ctx := setupWriteFixture(t)
	writeLocalSetupFixtureFile(t, root, "CLAUDE.md", setuptext.SetupBlock())
	writeLocalSetupFixtureFile(t, root, "CLAUDE.local.md", setuptext.SetupBlock())
	bin := t.TempDir()
	physicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fakecli.Install(t, bin, "git", []fakecli.Rule{{Argv: []string{"-C", physicalRoot, "ls-files", "--error-unmatch", "--", "CLAUDE.md"}, Code: 0}, {AnyArgs: true, Code: 1}})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range fakecli.Env(env.List(), bin) {
		k, v, ok := strings.Cut(item, "=")
		if ok {
			env[k] = v
		}
	}
	code, out, msg := runSetupWrite([]string{"--local", "--no-hooks"}, ctx, env, root)
	calls, err := fakecli.ReadCalls(filepath.Join(bin, "git.calls.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, call := range calls {
		if len(call.Argv) >= 6 && call.Argv[2] == "ls-files" && call.Argv[5] == "CLAUDE.md" {
			count++
		}
	}
	if code != 0 || count != 1 || !strings.Contains(out, "soho block already in") {
		t.Fatalf("code=%d count=%d out=%s msg=%s calls=%v", code, count, out, msg, calls)
	}
}

func TestTrackedMalformedManagedFiles(t *testing.T) {
	for _, rel := range []string{localInstructionFile, claudeLocalHookRel, codexHookRel, piInboundRel} {
		for _, mode := range []string{"", "--plan"} {
			t.Run(rel+mode, func(t *testing.T) {
				root, env, ctx := setupWriteFixture(t)
				value := "{ broken\n"
				if rel == localInstructionFile {
					value = setuptext.SetupStart + "\n"
				}
				if rel == piInboundRel {
					value = "unmanaged\n"
				}
				writeLocalSetupFixtureFile(t, root, rel, value)
				cmd := exec.Command("git", "-C", root, "add", rel)
				cmd.Env = env.List()
				if b, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git add: %s %v", b, err)
				}
				args := []string{"--local"}
				if mode != "" {
					args = append(args, mode)
				}
				code, out, msg := runSetupWrite(args, ctx, env, root)
				got := mustReadFile(t, filepath.Join(root, rel))
				if code != 0 || got != value || !strings.Contains(out, "is tracked; --local leaves it as is") {
					t.Fatalf("code=%d out=%s msg=%s file=%q", code, out, msg, got)
				}
			})
		}
	}
}

func TestTrackedCanonicalManagedKinds(t *testing.T) {
	cases := map[string]string{"AGENTS.md": "project\n", ".claude/settings.json": "{}\n", codexHookRel: "{}\n", piInboundRel: setuptext.PiInboundExtension() + "\n// old\n", ".gitignore": "# project\n"}
	for rel, seed := range cases {
		for _, branch := range []string{"main", "work"} {
			for _, mode := range []string{"", "--plan"} {
				t.Run(rel+branch+mode, func(t *testing.T) {
					root, env, ctx := setupWriteFixture(t)
					writeLocalSetupFixtureFile(t, root, rel, seed)
					cmds := [][]string{{"checkout", "-b", "main"}, {"add", rel}, {"-c", "user.name=test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "base"}, {"symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main"}}
					if branch == "work" {
						cmds = append(cmds, []string{"checkout", "-b", "work"})
					}
					for _, args := range cmds {
						cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
						cmd.Env = env.List()
						if b, err := cmd.CombinedOutput(); err != nil {
							t.Fatalf("git %v: %s %v", args, b, err)
						}
					}
					args := []string{}
					if mode != "" {
						args = append(args, mode)
					}
					code, out, msg := runSetupWrite(args, ctx, env, root)
					warning := filepath.Join(root, filepath.FromSlash(rel)) + " is tracked; make this change on a work branch and open a PR"
					if code != 0 || strings.Contains(out, warning) != (branch == "main") {
						t.Fatalf("code=%d out=%s msg=%s expected=%q", code, out, msg, warning)
					}
				})
			}
		}
	}
}

func TestLocalManagedKindsTrackedAndUntracked(t *testing.T) {
	cases := map[string]string{localInstructionFile: "custom\n", claudeLocalHookRel: "{}\n", codexHookRel: "{}\n", piInboundRel: setuptext.PiInboundExtension() + "\n// old\n"}
	for rel, seed := range cases {
		for _, tracked := range []bool{true, false} {
			for _, mode := range []string{"", "--plan"} {
				t.Run(fmt.Sprintf("%s/%v/%s", rel, tracked, mode), func(t *testing.T) {
					root, env, ctx := setupWriteFixture(t)
					writeLocalSetupFixtureFile(t, root, rel, seed)
					if tracked {
						cmd := exec.Command("git", "-C", root, "add", rel)
						cmd.Env = env.List()
						if b, err := cmd.CombinedOutput(); err != nil {
							t.Fatalf("git add: %s %v", b, err)
						}
					}
					args := []string{"--local"}
					if mode != "" {
						args = append(args, mode)
					}
					code, out, msg := runSetupWrite(args, ctx, env, root)
					after := mustReadFile(t, filepath.Join(root, rel))
					warning := filepath.Join(root, filepath.FromSlash(rel)) + " is tracked; --local leaves it as is"
					if code != 0 || strings.Contains(out, warning) != tracked || ((tracked || mode != "") && after != seed) || (!tracked && mode == "" && after == seed) {
						t.Fatalf("code=%d out=%s msg=%s changed=%v", code, out, msg, after != seed)
					}
				})
			}
		}
	}
}

func TestTrackedClaudePlanOlderAndDuplicate(t *testing.T) {
	for _, variant := range []string{"old", "duplicate"} {
		t.Run(variant, func(t *testing.T) {
			root, env, ctx := setupWriteFixture(t)
			block := setuptext.SetupBlock()
			if variant == "old" {
				block = strings.Replace(block, setuptext.SetupEnd, "old\n"+setuptext.SetupEnd, 1)
			}
			writeLocalSetupFixtureFile(t, root, "CLAUDE.md", block)
			if variant == "duplicate" {
				writeLocalSetupFixtureFile(t, root, localInstructionFile, setuptext.SetupBlock())
			}
			cmd := exec.Command("git", "-C", root, "add", "CLAUDE.md")
			cmd.Env = env.List()
			if b, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git add: %s %v", b, err)
			}
			before := setupFilesystemSnapshot(t, root)
			code, out, msg := runSetupWrite([]string{"--local", "--no-hooks", "--plan"}, ctx, env, root)
			want := "its block is older"
			if variant == "duplicate" {
				want = "local copy can be removed"
			}
			if code != 0 || strings.Count(out, want) != 1 || !reflect.DeepEqual(before, setupFilesystemSnapshot(t, root)) {
				t.Fatalf("code=%d out=%s msg=%s", code, out, msg)
			}
		})
	}
}
