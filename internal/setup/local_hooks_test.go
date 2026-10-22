package setup

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/setuptext"
)

func writeLocalSetupFixtureFile(t *testing.T, root, rel, text string) {
	t.Helper()
	file := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSetupLocalHookExcludes(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		tracked                       []string
		noHooks, unmarkedPi, badCodex bool
	}{
		{name: "created-and-confirmed-hooks"},
		{name: "tracked-codex", tracked: []string{".codex/hooks.json"}},
		{name: "tracked-pi", tracked: []string{".pi/extensions/soho-inbound.ts"}},
		{name: "no-hooks", noHooks: true},
		{name: "unmarked-pi", unmarkedPi: true},
		{name: "unmergeable-codex", badCodex: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, env, ctx := tm5LocalFixture(t)
			env["USERPROFILE"], env["TMPDIR"] = env["HOME"], t.TempDir()
			codex, pi := ".codex/hooks.json", ".pi/extensions/soho-inbound.ts"
			for _, rel := range tc.tracked {
				text := "{}\n"
				if rel == pi {
					text = setuptext.PiInboundExtension()
				}
				writeLocalSetupFixtureFile(t, root, rel, text)
				git, _ := exec.LookPath("git")
				cmd := exec.Command(git, "-C", root, "add", "--", rel)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git add: %v: %s", err, out)
				}
			}
			if tc.unmarkedPi {
				writeLocalSetupFixtureFile(t, root, pi, "// project-owned extension\n")
			}
			if tc.badCodex {
				writeLocalSetupFixtureFile(t, root, codex, "invalid json\n")
			}
			args := []string{"--local"}
			if tc.noHooks {
				args = append(args, "--no-hooks")
			}
			attempts := 2
			if tc.name == "created-and-confirmed-hooks" {
				attempts = 3
			}
			for attempt := 0; attempt < attempts; attempt++ {
				if attempt == 2 {
					// Confirming an unchanged managed extension must restore a missing
					// exclude, independently of the idempotent second run above.
					file := excludePath(root, env)
					b, err := os.ReadFile(file)
					if err != nil {
						t.Fatal(err)
					}
					text := strings.ReplaceAll(string(b), "/"+codex+"\n", "")
					text = strings.ReplaceAll(text, "/"+pi+"\n", "")
					if err := os.WriteFile(file, []byte(text), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				code, out, msg := runSetupWrite(args, ctx, env, root)
				if code != 0 {
					t.Fatalf("attempt=%d code=%d out=%q msg=%q", attempt, code, out, msg)
				}
				excluded, err := os.ReadFile(excludePath(root, env))
				if err != nil {
					t.Fatal(err)
				}
				for _, rel := range []string{codex, pi} {
					tracked := false
					for _, have := range tc.tracked {
						tracked = tracked || have == rel
					}
					want := !tc.noHooks && !tracked && !(tc.unmarkedPi && rel == pi) && !(tc.badCodex && rel == codex)
					count := 0
					for _, line := range strings.Split(string(excluded), "\n") {
						if line == "/"+rel {
							count++
						}
					}
					if count != map[bool]int{true: 1, false: 0}[want] {
						t.Fatalf("attempt=%d %s exclude count=%d want included=%v; exclude=%q", attempt, rel, count, want, excluded)
					}
					if want {
						if status, ok := checkedExcludeStatus(root, rel, env); !ok || status != 0 {
							t.Fatalf("%s not ignored by git: status=%d ok=%v", rel, status, ok)
						}
					}
				}
				if strings.Contains(string(excluded), "/.claude/settings.json") {
					t.Fatalf("Claude settings excluded: %q", excluded)
				}
			}
			if tc.noHooks {
				for _, rel := range []string{codex, pi, ".claude/settings.json"} {
					if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel))); !os.IsNotExist(err) {
						t.Fatalf("--no-hooks touched %s: %v", rel, err)
					}
				}
			}
			if tc.unmarkedPi {
				b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(pi)))
				if err != nil || string(b) != "// project-owned extension\n" {
					t.Fatalf("unmarked pi changed: %q %v", b, err)
				}
			}
		})
	}
}

func TestSetupLocalHookExcludePreviews(t *testing.T) {
	for _, mode := range []string{"--plan", "--dry-run"} {
		for _, noHooks := range []bool{false, true} {
			t.Run(mode+"/no-hooks="+map[bool]string{true: "true", false: "false"}[noHooks], func(t *testing.T) {
				root, env, ctx := tm5LocalFixture(t)
				env["USERPROFILE"], env["TMPDIR"] = env["HOME"], t.TempDir()
				before := setupFilesystemSnapshot(t, root)
				args := []string{"--local", mode}
				if noHooks {
					args = append(args, "--no-hooks")
				}
				code, out, msg := runSetupWrite(args, ctx, env, root)
				if code != 0 {
					t.Fatalf("code=%d out=%q msg=%q", code, out, msg)
				}
				for _, rel := range []string{"/.codex/hooks.json", "/.pi/extensions/soho-inbound.ts"} {
					found := false
					for _, line := range strings.Split(out, "\n") {
						if strings.Contains(line, "exclude") && strings.Contains(line, rel) {
							found = true
						}
					}
					if found == noHooks {
						t.Fatalf("exclude preview %s present=%v no-hooks=%v: %s", rel, found, noHooks, out)
					}
				}
				if !reflect.DeepEqual(before, setupFilesystemSnapshot(t, root)) {
					t.Fatal("preview changed repository files")
				}
			})
		}
	}
}

func TestSetupLocalHookSymlinkedExcludeRefusesBeforeWrites(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require privilege on Windows")
	}
	for _, mode := range []string{"write", "--plan", "--dry-run"} {
		t.Run(mode, func(t *testing.T) {
			root, env, ctx := tm5LocalFixture(t)
			env["USERPROFILE"], env["TMPDIR"] = env["HOME"], t.TempDir()
			excl := excludePath(root, env)
			outside := filepath.Join(t.TempDir(), "exclude")
			writeLocalSetupFixtureFile(t, filepath.Dir(outside), "exclude", "outside sentinel\n")
			if err := os.Remove(excl); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, excl); err != nil {
				t.Fatal(err)
			}
			before := setupFilesystemSnapshot(t, root)
			args := []string{"--local"}
			if mode != "write" {
				args = append(args, mode)
			}
			code, out, msg := runSetupWrite(args, ctx, env, root)
			if code != 4 || !strings.Contains(msg, "symlink") {
				t.Fatalf("code=%d out=%q msg=%q", code, out, msg)
			}
			if !reflect.DeepEqual(before, setupFilesystemSnapshot(t, root)) {
				t.Fatal("symlink refusal changed repository")
			}
			b, err := os.ReadFile(outside)
			if err != nil || string(b) != "outside sentinel\n" {
				t.Fatalf("external exclude changed: %q %v", b, err)
			}
		})
	}
}

func TestSetupImplicitLocalProjectRefusal(t *testing.T) {
	for _, mode := range []string{"write", "--plan", "--dry-run"} {
		t.Run(mode, func(t *testing.T) {
			root, env, ctx := tm5LocalFixture(t)
			env["USERPROFILE"], env["TMPDIR"] = env["HOME"], t.TempDir()
			for rel, text := range map[string]string{"AGENTS.md": "agents sentinel\n", "CLAUDE.md": "claude sentinel\n", "CLAUDE.local.md": setuptext.SetupBlock(), ".claude/settings.json": "{\"custom\":true}\n", ".agents/herdr-soho.conf": "panes=3\n"} {
				writeLocalSetupFixtureFile(t, root, rel, text)
			}
			before := setupFilesystemSnapshot(t, root)
			var args []string
			if mode != "write" {
				args = append(args, mode)
			}
			code, out, msg := runSetupWrite(args, ctx, env, root)
			want := "setup: this project keeps the soho block in CLAUDE.local.md; run 'setup --local', 'setup --target <file>', or 'session set setup_target local' (no files were changed)"
			if code != 2 || msg != want {
				t.Errorf("code=%d out=%q msg=%q; want code=2 msg=%q", code, out, msg, want)
			}
			if !reflect.DeepEqual(before, setupFilesystemSnapshot(t, root)) {
				t.Fatal("refusal changed bytes/paths (including canonical instructions, Claude settings, exclude and migration source)")
			}
		})
	}
}

func TestSetupExplicitLocalProjectAndCanonicalControls(t *testing.T) {
	for _, tc := range []struct {
		name                             string
		args                             []string
		configLocal, both, canonicalOnly bool
	}{
		{name: "local", args: []string{"--local"}},
		{name: "target", args: []string{"--target", "instructions.md"}},
		{name: "configured-local", configLocal: true},
		{name: "both", both: true},
		{name: "canonical-only", canonicalOnly: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, env, ctx := tm5LocalFixture(t)
			env["USERPROFILE"], env["TMPDIR"] = env["HOME"], t.TempDir()
			if !tc.canonicalOnly {
				writeLocalSetupFixtureFile(t, root, "CLAUDE.local.md", setuptext.SetupBlock())
			}
			if tc.both || tc.canonicalOnly {
				writeLocalSetupFixtureFile(t, root, "AGENTS.md", setuptext.SetupBlock())
			}
			if tc.configLocal {
				writeLocalSetupFixtureFile(t, root, ".agents/soho.conf", "setup_target=local\n")
				loaded := core.LoadConfig(env, root)
				ctx = &loaded
			}
			code, out, msg := runSetupWrite(append(tc.args, "--no-hooks"), ctx, env, root)
			if code != 0 {
				t.Fatalf("code=%d out=%q msg=%q", code, out, msg)
			}
			rel := "AGENTS.md"
			if tc.name == "target" {
				rel = "instructions.md"
			}
			if tc.name == "local" || tc.configLocal {
				rel = "CLAUDE.local.md"
				if _, err := os.Lstat(filepath.Join(root, "AGENTS.md")); !os.IsNotExist(err) {
					t.Fatalf("local control touched canonical: %v", err)
				}
			}
			b, err := os.ReadFile(filepath.Join(root, rel))
			if err != nil || !strings.Contains(string(b), setuptext.SetupStart) {
				t.Fatalf("expected block in %s: %q %v", rel, b, err)
			}
		})
	}
}

func TestSetupImplicitCompleteBlockDecision(t *testing.T) {
	current := setuptext.SetupStart + "\nlocal body\n" + setuptext.SetupEnd + "\n"
	legacy := setuptext.LegacySetupStart + "\nlegacy body\n" + setuptext.LegacySetupEnd + "\n"
	for _, tc := range []struct {
		name, local, agents, claude string
		refuse, canonicalDirectory  bool
	}{
		{name: "reversed-local", local: setuptext.SetupEnd + "\nbody\n" + setuptext.SetupStart + "\n"},
		{name: "local-start-only", local: setuptext.SetupStart + "\n"},
		{name: "local-end-only", local: setuptext.SetupEnd + "\n"},
		{name: "canonical-start-only", local: current, agents: setuptext.SetupStart + "\n", refuse: true},
		{name: "legacy-canonical-complete", local: current, claude: legacy},
		{name: "legacy-local-complete", local: legacy, refuse: true},
		{name: "mixed-current-local", local: setuptext.SetupStart + "\nbody\n" + setuptext.LegacySetupEnd + "\n"},
		{name: "mixed-legacy-local", local: setuptext.LegacySetupStart + "\nbody\n" + setuptext.SetupEnd + "\n"},
		{name: "same-line-local", local: setuptext.SetupStart + setuptext.SetupEnd + "\n"},
		{name: "mixed-canonical", local: current, agents: setuptext.SetupStart + "\nbody\n" + setuptext.LegacySetupEnd + "\n", refuse: true},
		{name: "reversed-canonical", local: current, claude: setuptext.SetupEnd + "\nbody\n" + setuptext.SetupStart + "\n", refuse: true},
		{name: "current-canonical-complete", local: current, agents: current},
		{name: "canonical-directory", local: current, canonicalDirectory: true, refuse: true},
	} {
		for _, mode := range []string{"write", "--plan", "--dry-run"} {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				root, env, ctx := tm5LocalFixture(t)
				env["USERPROFILE"], env["TMPDIR"] = env["HOME"], t.TempDir()
				writeLocalSetupFixtureFile(t, root, "CLAUDE.local.md", tc.local)
				writeLocalSetupFixtureFile(t, root, ".claude/settings.json", "{\"custom\":true}\n")
				for name, text := range map[string]string{"AGENTS.md": tc.agents, "CLAUDE.md": tc.claude} {
					if text != "" {
						writeLocalSetupFixtureFile(t, root, name, text)
					}
				}
				if tc.canonicalDirectory {
					if err := os.Mkdir(filepath.Join(root, "AGENTS.md"), 0o700); err != nil {
						t.Fatal(err)
					}
				}
				before := setupFilesystemSnapshot(t, root)
				args := []string{"--no-hooks"}
				if mode != "write" {
					args = append(args, mode)
				}
				code, out, msg := runSetupWrite(args, ctx, env, root)
				after := setupFilesystemSnapshot(t, root)
				changed := !reflect.DeepEqual(before, after)
				wantCode := 0
				if tc.refuse {
					wantCode = 2
				}
				if code != wantCode {
					t.Errorf("code=%d want=%d changed=%v out=%q msg=%q", code, wantCode, changed, out, msg)
				}
				if tc.refuse {
					want := "setup: this project keeps the soho block in CLAUDE.local.md; run 'setup --local', 'setup --target <file>', or 'session set setup_target local' (no files were changed)"
					if msg != want {
						t.Errorf("refusal message=%q want=%q", msg, want)
					}
				}
				if tc.refuse || mode != "write" {
					if changed {
						t.Error("refusal or preview changed repository bytes/paths")
					}
				} else if !changed {
					t.Error("allowed write did not perform canonical setup")
				}
			})
		}
	}
}
