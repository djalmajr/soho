package setup

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/setuptext"
)

// setupPathList is the name-and-mode snapshot that does not read file
// content: the mode-000 refusal tests compare the tree while a file is
// unreadable, so a content walk would fail on the fixture itself.
func setupPathList(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		out[rel] = info.Mode().String()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func samePathList(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// DJA-701 scenario 1: a mixed Claude hook row keeps the foreign command,
// its matcher and its order; the soho command is removed, never duplicated.
func TestSetupMixedClaudeHookRowKeepsForeign(t *testing.T) {
	for _, mode := range []string{"canonical", "local", "shared-migration"} {
		for _, event := range []string{"UserPromptSubmit", "SessionStart"} {
			t.Run(mode+"/"+event, func(t *testing.T) {
				root, env, ctx := codexSetupFixture(t)
				command := setuptext.SetupHookReminder()
				if event == "SessionStart" {
					command = setuptext.SetupHookDoctor()
				}
				rel := ".claude/settings.json"
				args := []string{"--target", "AGENTS.md"}
				if mode != "canonical" {
					args = []string{"--local"}
				}
				if mode == "local" {
					rel = claudeLocalHookRel
				}
				raw := fmt.Sprintf(`{"custom":true,"hooks":{%q:[{"matcher":"*","hooks":[{"type":"command","command":%q},{"type":"command","command":"project-audit-command"}]}]}}`, event, command)
				writeLocalSetupFixtureFile(t, root, rel, raw)
				if mode == "shared-migration" {
					_, after := sharedHooksMigration(root)
					if after == nil {
						t.Fatal("shared migration refused the mixed row")
					}
					if strings.Contains(*after, command) {
						t.Fatalf("shared file keeps the soho command: %s", *after)
					}
					if !strings.Contains(*after, "project-audit-command") || !strings.Contains(*after, `"matcher": "*"`) || !strings.Contains(*after, `"custom": true`) {
						t.Fatalf("shared migration lost the foreign content: %s", *after)
					}
					return
				}
				code, _, msg := runSetupWrite(args, ctx, env, root)
				if code != 0 {
					t.Fatalf("code=%d msg=%q", code, msg)
				}
				got := mustReadFile(t, filepath.Join(root, rel))
				if strings.Count(got, "project-audit-command") != 1 {
					t.Fatalf("foreign command lost or duplicated: %s", got)
				}
				if strings.Count(got, `"`+command+`"`) != 1 {
					t.Fatalf("soho command not exactly once: %s", got)
				}
				if !strings.Contains(got, `"matcher": "*"`) {
					t.Fatalf("row matcher lost: %s", got)
				}
				if strings.Index(got, "project-audit-command") > strings.Index(got, command) {
					t.Fatalf("order changed: foreign must stay before the appended soho row: %s", got)
				}
			})
		}
	}
}

// DJA-701 scenario 2: the same contract holds for .codex/hooks.json.
func TestSetupMixedCodexHookRowKeepsForeign(t *testing.T) {
	for _, event := range []string{"UserPromptSubmit", "SessionStart"} {
		t.Run(event, func(t *testing.T) {
			root, env, ctx := codexSetupFixture(t)
			command := setuptext.SetupHookCodexReminder()
			if event == "SessionStart" {
				command = setuptext.SetupHookCodexDoctor()
			}
			raw := fmt.Sprintf(`{"hooks":{%q:[{"matcher":"*","hooks":[{"type":"command","command":%q},{"type":"command","command":"project-audit-command"}]}]}}`, event, command)
			writeLocalSetupFixtureFile(t, root, ".codex/hooks.json", raw)
			code, _, msg := runSetupWrite([]string{"--target", "AGENTS.md"}, ctx, env, root)
			if code != 0 {
				t.Fatalf("code=%d msg=%q", code, msg)
			}
			got := mustReadFile(t, codexFile(root))
			if strings.Count(got, "project-audit-command") != 1 || strings.Count(got, `"`+command+`"`) != 1 || !strings.Contains(got, `"matcher": "*"`) {
				t.Fatalf("codex mixed row changed: %s", got)
			}
		})
	}
}

// DJA-701 scenario 3: a second setup over the scenario-1 file is a byte
// for byte no-op, so the mixed row never stacks or drifts.
func TestSetupMixedClaudeHookRowRerunByteEqual(t *testing.T) {
	for _, mode := range []string{"canonical", "local"} {
		t.Run(mode, func(t *testing.T) {
			root, env, ctx := codexSetupFixture(t)
			rel := ".claude/settings.json"
			args := []string{"--target", "AGENTS.md"}
			if mode == "local" {
				rel = claudeLocalHookRel
				args = []string{"--local"}
			}
			raw := fmt.Sprintf(`{"custom":true,"hooks":{"UserPromptSubmit":[{"matcher":"*","hooks":[{"type":"command","command":%q},{"type":"command","command":"project-audit-command"}]}],"SessionStart":[{"matcher":"*","hooks":[{"type":"command","command":%q},{"type":"command","command":"project-session-audit"}]}]}}`, setuptext.SetupHookReminder(), setuptext.SetupHookDoctor())
			writeLocalSetupFixtureFile(t, root, rel, raw)
			if code, _, msg := runSetupWrite(args, ctx, env, root); code != 0 {
				t.Fatalf("first run code=%d msg=%q", code, msg)
			}
			first := mustReadFile(t, filepath.Join(root, rel))
			if code, _, msg := runSetupWrite(args, ctx, env, root); code != 0 {
				t.Fatalf("second run code=%d msg=%q", code, msg)
			}
			second := mustReadFile(t, filepath.Join(root, rel))
			if first != second {
				t.Fatalf("rerun not byte equal:\nfirst=%s\nsecond=%s", first, second)
			}
		})
	}
}

// DJA-701 scenario 4: an existing instruction or Claude settings file that
// exists but cannot be read is refused (exit 4) with the system error, in
// setup, --dry-run and --plan; the file keeps its bytes and mode, and no
// other file is created.
func TestSetupUnreadableInstructionAndSettingsRefuse(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode-000 has no permission semantics on Windows")
	}
	cases := []struct {
		rel  string
		args []string
	}{{"AGENTS.md", []string{"--target", "AGENTS.md", "--no-hooks"}}, {"CLAUDE.md", []string{"--target", "CLAUDE.md", "--no-hooks"}}, {localInstructionFile, []string{"--local", "--no-hooks"}}, {".claude/settings.json", []string{"--target", "AGENTS.md"}}, {claudeLocalHookRel, []string{"--local"}}}
	for _, tc := range cases {
		for _, mode := range []string{"write", "dry-run", "plan"} {
			t.Run(tc.rel+"/"+mode, func(t *testing.T) {
				root, env, ctx := codexSetupFixture(t)
				raw := "PRIVATE_PROJECT_SENTINEL\n"
				if strings.HasSuffix(tc.rel, "json") {
					raw = `{"private_project_sentinel":true}`
				}
				writeLocalSetupFixtureFile(t, root, tc.rel, raw)
				p := filepath.Join(root, filepath.FromSlash(tc.rel))
				if e := os.Chmod(p, 0); e != nil {
					t.Fatal(e)
				}
				if _, e := os.ReadFile(p); e == nil {
					t.Skip("mode-000 remains readable on this host")
				}
				args := append([]string{}, tc.args...)
				if mode == "dry-run" {
					args = append(args, "--dry-run")
				}
				if mode == "plan" {
					args = append([]string{"--plan"}, args...)
				}
				pathsBefore := setupPathList(t, root)
				code, _, msg := runSetupWrite(args, ctx, env, root)
				wantPrefix, wantSuffix := "setup: could not read "+p+": ", " (file left untouched)"
				if mode == "plan" {
					wantPrefix, wantSuffix = "setup --plan: could not read "+p+": ", " (setup would refuse it and leave the file untouched)"
				}
				if code != 4 || !strings.HasPrefix(msg, wantPrefix) || !strings.HasSuffix(msg, wantSuffix) {
					t.Fatalf("code=%d msg=%q, want prefix %q suffix %q", code, msg, wantPrefix, wantSuffix)
				}
				info, err := os.Stat(p)
				if err != nil || info.Mode().Perm() != 0 {
					t.Fatalf("mode changed by the refusal: %v %v", info.Mode(), err)
				}
				after := setupPathList(t, root)
				if e := os.Chmod(p, 0o600); e != nil {
					t.Fatal(e)
				}
				got := mustReadFile(t, p)
				if !strings.Contains(got, "PRIVATE_PROJECT_SENTINEL") && !strings.Contains(got, "private_project_sentinel") {
					t.Fatalf("content lost: %q", got)
				}
				if !samePathList(pathsBefore, after) {
					t.Fatalf("refusal changed the tree: %v vs %v", pathsBefore, after)
				}
			})
		}
	}
}

// The portable member of scenario 4: a directory standing in for the file
// is an unreadable path on every platform and must refuse with exit 4.
func TestSetupDirectoryInPlaceOfFileRefuses(t *testing.T) {
	cases := []struct {
		rel  string
		args []string
	}{{"AGENTS.md", []string{"--no-hooks"}}, {"CLAUDE.md", []string{"--target", "CLAUDE.md", "--no-hooks"}}, {localInstructionFile, []string{"--local", "--no-hooks"}}, {".claude/settings.json", []string{"--target", "AGENTS.md"}}, {claudeLocalHookRel, []string{"--local"}}}
	for _, tc := range cases {
		for _, mode := range []string{"write", "dry-run", "plan"} {
			t.Run(tc.rel+"/"+mode, func(t *testing.T) {
				root, env, ctx := codexSetupFixture(t)
				p := filepath.Join(root, filepath.FromSlash(tc.rel))
				if err := os.MkdirAll(p, 0o755); err != nil {
					t.Fatal(err)
				}
				args := append([]string{}, tc.args...)
				if mode == "dry-run" {
					args = append(args, "--dry-run")
				}
				if mode == "plan" {
					args = append([]string{"--plan"}, args...)
				}
				pathsBefore := setupPathList(t, root)
				code, _, msg := runSetupWrite(args, ctx, env, root)
				wantPrefix, wantSuffix := "setup: could not read "+p+": ", " (file left untouched)"
				if mode == "plan" {
					wantPrefix, wantSuffix = "setup --plan: could not read "+p+": ", " (setup would refuse it and leave the file untouched)"
				}
				if code != 4 || !strings.HasPrefix(msg, wantPrefix) || !strings.HasSuffix(msg, wantSuffix) {
					t.Fatalf("code=%d msg=%q, want prefix %q suffix %q", code, msg, wantPrefix, wantSuffix)
				}
				info, err := os.Stat(p)
				if err != nil || !info.IsDir() {
					t.Fatalf("directory touched: %v %v", info, err)
				}
				if !samePathList(pathsBefore, setupPathList(t, root)) {
					t.Fatalf("refusal changed the tree: %v vs %v", pathsBefore, setupPathList(t, root))
				}
			})
		}
	}
}

// DJA-701 scenario 5: the unpaired-marker shapes of the brief refuse with
// exit 4 and leave the destination byte for byte, in every default
// instruction destination, in setup, --dry-run and --plan.
func TestSetupUnpairedMarkerRefusalLeavesBytesIntact(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{{"start-without-end", "project head\n" + setuptext.SetupStart + "\nPROJECT_TAIL_SENTINEL\n"}, {"end-without-start", "project head\n" + setuptext.SetupEnd + "\nPROJECT_TAIL_SENTINEL\n"}, {"end-before-start", setuptext.SetupEnd + "\n" + setuptext.SetupStart + "\nx\n" + setuptext.SetupEnd + "\n"}, {"start-inside-block", setuptext.SetupStart + "\n1\n" + setuptext.SetupStart + "\n2\n" + setuptext.SetupEnd + "\n"}, {"current-start-legacy-end", setuptext.SetupStart + "\nx\n" + setuptext.LegacySetupEnd + "\n"}, {"legacy-start-current-end", setuptext.LegacySetupStart + "\nx\n" + setuptext.SetupEnd + "\n"}, {"both-markers-one-line", "a\n" + setuptext.SetupStart + setuptext.SetupEnd + "\nb\n"}}
	dests := []struct {
		rel  string
		args []string
	}{{"AGENTS.md", []string{"--target", "AGENTS.md", "--no-hooks"}}, {"CLAUDE.md", []string{"--target", "CLAUDE.md", "--no-hooks"}}, {localInstructionFile, []string{"--local", "--no-hooks"}}}
	for _, c := range cases {
		for _, d := range dests {
			t.Run(c.name+"/"+d.rel, func(t *testing.T) {
				root, env, ctx := codexSetupFixture(t)
				writeLocalSetupFixtureFile(t, root, d.rel, c.text)
				before := mustReadFile(t, filepath.Join(root, d.rel))
				code, _, msg := runSetupWrite(d.args, ctx, env, root)
				if code != 4 || !strings.Contains(msg, "has an unpaired soho marker; fix it by hand and run setup again (file left untouched)") {
					t.Fatalf("code=%d msg=%q", code, msg)
				}
				if before != mustReadFile(t, filepath.Join(root, d.rel)) {
					t.Fatal("destination bytes changed")
				}
			})
		}
	}
	// the plan and dry-run refusals of the same defect class
	t.Run("plan", func(t *testing.T) {
		root, env, ctx := codexSetupFixture(t)
		text := "project head\n" + setuptext.SetupStart + "\nPROJECT_TAIL_SENTINEL\n"
		writeLocalSetupFixtureFile(t, root, "AGENTS.md", text)
		code, _, msg := runSetupWrite([]string{"--plan", "--target", "AGENTS.md", "--no-hooks"}, ctx, env, root)
		if code != 4 || !strings.Contains(msg, "setup --plan: ") || !strings.Contains(msg, "has an unpaired soho marker (setup would refuse it and leave the file untouched)") {
			t.Fatalf("code=%d msg=%q", code, msg)
		}
		if mustReadFile(t, filepath.Join(root, "AGENTS.md")) != text {
			t.Fatal("plan changed the destination")
		}
	})
	t.Run("dry-run", func(t *testing.T) {
		root, env, ctx := codexSetupFixture(t)
		text := "project head\n" + setuptext.SetupStart + "\nPROJECT_TAIL_SENTINEL\n"
		writeLocalSetupFixtureFile(t, root, "AGENTS.md", text)
		code, _, msg := runSetupWrite([]string{"--target", "AGENTS.md", "--no-hooks", "--dry-run"}, ctx, env, root)
		if code != 4 || !strings.Contains(msg, "has an unpaired soho marker; fix it by hand and run setup again (file left untouched)") {
			t.Fatalf("code=%d msg=%q", code, msg)
		}
		if mustReadFile(t, filepath.Join(root, "AGENTS.md")) != text {
			t.Fatal("dry-run changed the destination")
		}
	})
}

// DJA-701 scenario 6: well-formed blocks keep today's behavior end to end.
func TestSetupWellFormedBlocksKeepCurrentBehavior(t *testing.T) {
	t.Run("well-formed current block is replaced in place", func(t *testing.T) {
		root, env, ctx := codexSetupFixture(t)
		in := "before\n" + setuptext.SetupStart + "\nold\n" + setuptext.SetupEnd + "\nafter\n"
		writeLocalSetupFixtureFile(t, root, "AGENTS.md", in)
		code, _, msg := runSetupWrite([]string{"--target", "AGENTS.md", "--no-hooks"}, ctx, env, root)
		got := mustReadFile(t, filepath.Join(root, "AGENTS.md"))
		if code != 0 || got != "before\n"+setuptext.SetupBlock()+"after\n" {
			t.Fatalf("code=%d msg=%q got=%q", code, msg, got)
		}
	})
	t.Run("well-formed legacy block migrates in place", func(t *testing.T) {
		root, env, ctx := codexSetupFixture(t)
		in := "before\n" + setuptext.LegacySetupStart + "\n## workflow herdr-soho\nHERDR_SOHO_DIR\n" + setuptext.LegacySetupEnd + "\nafter\n"
		writeLocalSetupFixtureFile(t, root, "AGENTS.md", in)
		code, _, msg := runSetupWrite([]string{"--target", "AGENTS.md", "--no-hooks"}, ctx, env, root)
		got := mustReadFile(t, filepath.Join(root, "AGENTS.md"))
		if code != 0 || !strings.Contains(got, setuptext.SetupStart) || strings.Contains(got, setuptext.LegacySetupStart) || !strings.Contains(got, "## workflow soho") || !strings.Contains(got, "SOHO_DIR") {
			t.Fatalf("code=%d msg=%q got=%q", code, msg, got)
		}
	})
	t.Run("two complete separate pairs both come back as blocks", func(t *testing.T) {
		root, env, ctx := codexSetupFixture(t)
		in := setuptext.SetupStart + "\nx\n" + setuptext.SetupEnd + "\n" + setuptext.LegacySetupStart + "\ny\n" + setuptext.LegacySetupEnd + "\n"
		writeLocalSetupFixtureFile(t, root, "AGENTS.md", in)
		code, _, msg := runSetupWrite([]string{"--target", "AGENTS.md", "--no-hooks"}, ctx, env, root)
		got := mustReadFile(t, filepath.Join(root, "AGENTS.md"))
		if code != 0 || strings.Count(got, setuptext.SetupStart) != 2 || strings.Count(got, setuptext.SetupEnd) != 2 {
			t.Fatalf("code=%d msg=%q got=%q", code, msg, got)
		}
	})
}

// DJA-701 scenario 7: with an old-named project config in place, every
// refusal that is known before the migrations fires first, so the old file
// keeps its name.
func TestSetupRefusalsPrecedeNameMigration(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode-000 has no permission semantics on Windows")
	}
	legacyRel := ".agents/herdr-soho.conf"
	cases := []struct {
		name         string
		args         []string
		wantCode     int
		wantMsg      string
		agentsExists bool
		agentsText   string
		setup        func(t *testing.T, root string)
	}{
		{"invalid-panes", []string{"--target", "AGENTS.md", "--panes", "99", "--no-hooks"}, 2, "setup: --panes must be 2, 3 or 4", false, "", nil},
		{"invalid-lane", []string{"--target", "AGENTS.md", "--lane", "build=bogus", "--no-hooks"}, 2, "unknown kind", false, "", nil},
		{"local-and-target", []string{"--local", "--target", "AGENTS.md", "--no-hooks"}, 2, "exclusive", false, "", nil},
		{"unreadable-destination", []string{"--target", "AGENTS.md", "--no-hooks"}, 4, "could not read", true, "PRIVATE_PROJECT_SENTINEL\n", func(t *testing.T, root string) {
			writeLocalSetupFixtureFile(t, root, "AGENTS.md", "PRIVATE_PROJECT_SENTINEL\n")
			p := filepath.Join(root, "AGENTS.md")
			if err := os.Chmod(p, 0); err != nil {
				t.Fatal(err)
			}
			if _, err := os.ReadFile(p); err == nil {
				t.Skip("mode-000 remains readable on this host")
			}
		}},
		{"broken-marker", []string{"--target", "AGENTS.md", "--no-hooks"}, 4, "unpaired soho marker", true, "head\n" + setuptext.SetupStart + "\nTAIL\n", func(t *testing.T, root string) {
			writeLocalSetupFixtureFile(t, root, "AGENTS.md", "head\n"+setuptext.SetupStart+"\nTAIL\n")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, env, ctx := codexSetupFixture(t)
			writeLocalSetupFixtureFile(t, root, legacyRel, "panes=3\n")
			if tc.setup != nil {
				tc.setup(t, root)
			}
			code, _, msg := runSetupWrite(tc.args, ctx, env, root)
			if code == 0 || code != tc.wantCode || !strings.Contains(msg, tc.wantMsg) {
				t.Fatalf("code=%d msg=%q, want code=%d containing %q", code, msg, tc.wantCode, tc.wantMsg)
			}
			// Restore a mode-000 destination so its content can be checked;
			// the refusal must have left the bytes exactly as they were.
			agents := filepath.Join(root, "AGENTS.md")
			if tc.agentsExists {
				if err := os.Chmod(agents, 0o600); err != nil {
					t.Fatal(err)
				}
				if got, err := os.ReadFile(agents); err != nil || string(got) != tc.agentsText {
					t.Fatalf("AGENTS.md changed before the refusal: %q %v", got, err)
				}
			} else if _, err := os.Stat(agents); !os.IsNotExist(err) {
				t.Fatal("AGENTS.md written before the refusal")
			}
			if got, err := os.ReadFile(filepath.Join(root, legacyRel)); err != nil || string(got) != "panes=3\n" {
				t.Fatalf("old config renamed or changed: %q %v", got, err)
			}
			if _, err := os.Stat(filepath.Join(root, ".agents", "soho.conf")); !os.IsNotExist(err) {
				t.Fatal("destination config created before the refusal")
			}
		})
	}
}

// DJA-701 scenario 8: setup_target=local in an old-named config file still
// reaches local mode after the migration renames it.
func TestSetupTargetLocalFromLegacyConfigAppliesAfterMigration(t *testing.T) {
	root, env, ctx := codexSetupFixture(t)
	writeLocalSetupFixtureFile(t, root, ".agents/herdr-soho.conf", "setup_target=local\n")
	code, out, msg := runSetupWrite([]string{"--no-hooks"}, ctx, env, root)
	if code != 0 {
		t.Fatalf("code=%d out=%q msg=%q", code, out, msg)
	}
	if _, err := os.Stat(filepath.Join(root, ".agents", "soho.conf")); err != nil {
		t.Fatalf("config not renamed: %v", err)
	}
	local := mustReadFile(t, filepath.Join(root, localInstructionFile))
	if !strings.Contains(local, setuptext.SetupStart) {
		t.Fatalf("local block missing: %q", local)
	}
	if _, err := os.Stat(filepath.Join(root, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatal("canonical AGENTS.md written in local mode")
	}
}

// DJA-701 scenario 9: --lane without --panes applies on its own, in
// canonical and local mode; --dry-run warns and writes nothing; --plan
// already announced it.
func TestSetupStandaloneLane(t *testing.T) {
	t.Run("canonical", func(t *testing.T) {
		root, env, ctx := codexSetupFixture(t)
		code, out, msg := runSetupWrite([]string{"--target", "AGENTS.md", "--lane", "build=codex", "--no-hooks"}, ctx, env, root)
		if code != 0 {
			t.Fatalf("code=%d out=%q msg=%q", code, out, msg)
		}
		conf := mustReadFile(t, filepath.Join(root, ".agents", "soho.conf"))
		if !strings.Contains(conf, "lane.build.kind=codex") {
			t.Fatalf("lane not written: %q", conf)
		}
		if strings.Contains(conf, "panes=") {
			t.Fatalf("panes written without --panes: %q", conf)
		}
		if !strings.Contains(out, "set lane.build.kind=codex") {
			t.Fatalf("stdout missing the lane line: %q", out)
		}
	})
	t.Run("local", func(t *testing.T) {
		root, env, ctx := codexSetupFixture(t)
		code, out, msg := runSetupWrite([]string{"--local", "--lane", "build=codex", "--no-hooks"}, ctx, env, root)
		if code != 0 {
			t.Fatalf("code=%d out=%q msg=%q", code, out, msg)
		}
		conf := mustReadFile(t, filepath.Join(root, ".agents", "soho.conf"))
		if !strings.Contains(conf, "lane.build.kind=codex") {
			t.Fatalf("lane not written: %q", conf)
		}
		if strings.Contains(conf, "panes=") {
			t.Fatalf("panes written without --panes: %q", conf)
		}
	})
	t.Run("dry-run warns and writes nothing", func(t *testing.T) {
		root, env, ctx := codexSetupFixture(t)
		pathsBefore := setupPathList(t, root)
		code, out, msg := runSetupWrite([]string{"--target", "AGENTS.md", "--lane", "build=codex", "--no-hooks", "--dry-run"}, ctx, env, root)
		if code != 0 || !strings.Contains(out, "# would apply --lane build=codex") {
			t.Fatalf("code=%d out=%q msg=%q", code, out, msg)
		}
		if _, err := os.Stat(filepath.Join(root, ".agents", "soho.conf")); !os.IsNotExist(err) {
			t.Fatal("dry-run wrote the config")
		}
		if _, err := os.Stat(filepath.Join(root, "AGENTS.md")); !os.IsNotExist(err) {
			t.Fatal("dry-run wrote the block")
		}
		if !samePathList(pathsBefore, setupPathList(t, root)) {
			t.Fatalf("dry-run changed the tree: %v", pathsBefore)
		}
	})
	t.Run("plan announces the lane", func(t *testing.T) {
		root, env, ctx := codexSetupFixture(t)
		code, out, msg := runSetupWrite([]string{"--plan", "--lane", "build=codex"}, ctx, env, root)
		if code != 0 || !strings.Contains(out, "lane.build.kind") {
			t.Fatalf("code=%d out=%q msg=%q", code, out, msg)
		}
	})
}

// DJA-701 decision 6: a refusal that is only known after the migrations
// must not promise that nothing changed. The local mode itself only
// becomes visible once the old-named config is renamed (setup_target in a
// legacy file is not read before the rename), so the broken local marker
// is the post-migration refusal.
func TestSetupPostMigrationRefusalMessage(t *testing.T) {
	root, env, ctx := codexSetupFixture(t)
	broken := "head\n" + setuptext.SetupStart + "\nTAIL\n"
	writeLocalSetupFixtureFile(t, root, ".agents/herdr-soho.conf", "setup_target=local\n")
	writeLocalSetupFixtureFile(t, root, localInstructionFile, broken)
	code, _, msg := runSetupWrite([]string{"--no-hooks"}, ctx, env, root)
	if code != 4 || !strings.Contains(msg, "has an unpaired soho marker") {
		t.Fatalf("code=%d msg=%q", code, msg)
	}
	if strings.Contains(msg, "no files were changed") {
		t.Fatalf("post-migration refusal promises nothing changed: %q", msg)
	}
	if !strings.Contains(msg, "files may already have changed") {
		t.Fatalf("post-migration refusal lacks the changed-files note: %q", msg)
	}
	// the config rename already ran; the message must not hide it
	if _, err := os.Stat(filepath.Join(root, ".agents", "soho.conf")); err != nil {
		t.Fatalf("config rename did not happen: %v", err)
	}
	if got := mustReadFile(t, filepath.Join(root, localInstructionFile)); got != broken {
		t.Fatalf("local file changed by the refusal: %q", got)
	}
}

// DJA-701 fix (F1 of rev-695a): the first preflight pass is read-only and,
// in both passes, the git exclude write probe runs only after the
// destination and the settings have been read and validated. These
// scenarios pin which refusal wins when the git exclude directory is
// unwritable at the same time as a broken destination, and keep the
// unwritable-exclude refusal alive for a valid destination.
func TestSetupPreflightExcludeOrdering(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode-0555 fixtures do not apply on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses the mode-0555 fixture")
	}
	brokenMarker := "project head\n" + setuptext.SetupStart + "\nPROJECT_TAIL_SENTINEL\n"
	unwritableGitInfo := func(t *testing.T, root string) {
		t.Helper()
		infoDir := filepath.Join(root, ".git", "info")
		if err := os.MkdirAll(infoDir, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(infoDir, 0o755) })
		if err := os.Chmod(infoDir, 0o555); err != nil {
			t.Fatalf("chmod .git/info 0555: %v", err)
		}
	}
	t.Run("marker refusal wins over the exclude refusal; no file changes", func(t *testing.T) {
		root, env, ctx := tm5LocalFixture(t)
		writeLocalSetupFixtureFile(t, root, localInstructionFile, brokenMarker)
		unwritableGitInfo(t, root)
		before := setupPathList(t, root)
		code, _, msg := runSetupWrite([]string{"--local", "--no-hooks"}, ctx, env, root)
		if code != 4 || !strings.Contains(msg, "has an unpaired soho marker; fix it by hand and run setup again (file left untouched)") {
			t.Fatalf("code=%d msg=%q", code, msg)
		}
		if strings.Contains(msg, "cannot write the git exclude file") {
			t.Fatalf("the exclude refusal won: %q", msg)
		}
		if !samePathList(before, setupPathList(t, root)) {
			t.Fatal("the tree changed before the refusal")
		}
	})
	t.Run("valid destination keeps the exclude refusal; destination untouched", func(t *testing.T) {
		root, env, ctx := tm5LocalFixture(t)
		writeLocalSetupFixtureFile(t, root, localInstructionFile, "project head\n")
		unwritableGitInfo(t, root)
		dest := filepath.Join(root, localInstructionFile)
		before := mustReadFile(t, dest)
		code, _, msg := runSetupWrite([]string{"--local", "--no-hooks"}, ctx, env, root)
		if code != 4 || !strings.Contains(msg, "cannot write the git exclude file at ") {
			t.Fatalf("code=%d msg=%q", code, msg)
		}
		if got := mustReadFile(t, dest); got != before {
			t.Fatalf("destination was written: %q", got)
		}
	})
	t.Run("post-migration marker refusal wins over the exclude refusal", func(t *testing.T) {
		root, env, ctx := tm5LocalFixture(t)
		confDir := filepath.Join(root, ".agents")
		if err := os.MkdirAll(confDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(confDir, "herdr-soho.conf"), []byte("setup_target=local\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		writeLocalSetupFixtureFile(t, root, localInstructionFile, brokenMarker)
		unwritableGitInfo(t, root)
		code, _, msg := runSetupWrite([]string{"--no-hooks"}, ctx, env, root)
		if code != 4 || !strings.Contains(msg, "has an unpaired soho marker; fix it by hand and run setup again (file left untouched, but files may already have changed)") {
			t.Fatalf("code=%d msg=%q", code, msg)
		}
		if strings.Contains(msg, "cannot write the git exclude file") {
			t.Fatalf("the exclude refusal won: %q", msg)
		}
	})
}

// stubExcludeProbe replaces the write test and counts its calls, so each
// preflight pass is proved apart from the directory's mode, on every system.
func stubExcludeProbe(t *testing.T, fail bool) *int {
	t.Helper()
	calls := 0
	real := createExcludeProbe
	createExcludeProbe = func(dir, pattern string) (*os.File, error) {
		calls++
		if fail {
			return nil, os.ErrPermission
		}
		return real(dir, pattern)
	}
	t.Cleanup(func() { createExcludeProbe = real })
	return &calls
}

func TestSetupExcludeProbeRunsOnlyInTheLastPass(t *testing.T) {
	brokenMarker := "project head\n" + setuptext.SetupStart + "\nPROJECT_TAIL_SENTINEL\n"
	t.Run("a refused destination is never preceded by the probe", func(t *testing.T) {
		root, env, ctx := tm5LocalFixture(t)
		writeLocalSetupFixtureFile(t, root, localInstructionFile, brokenMarker)
		calls := stubExcludeProbe(t, false)
		code, _, msg := runSetupWrite([]string{"--local", "--no-hooks"}, ctx, env, root)
		if code != 4 || !strings.Contains(msg, "has an unpaired soho marker") {
			t.Fatalf("code=%d msg=%q", code, msg)
		}
		if *calls != 0 {
			t.Fatalf("probe calls = %d, want 0", *calls)
		}
	})
	t.Run("a valid destination probes once, and a failed probe refuses before any write", func(t *testing.T) {
		root, env, ctx := tm5LocalFixture(t)
		writeLocalSetupFixtureFile(t, root, localInstructionFile, "project head\n")
		dest := filepath.Join(root, localInstructionFile)
		calls := stubExcludeProbe(t, true)
		code, _, msg := runSetupWrite([]string{"--local", "--no-hooks"}, ctx, env, root)
		if code != 4 || !strings.Contains(msg, "cannot write the git exclude file at ") {
			t.Fatalf("code=%d msg=%q", code, msg)
		}
		if *calls != 1 {
			t.Fatalf("probe calls = %d, want 1", *calls)
		}
		if got := mustReadFile(t, dest); got != "project head\n" {
			t.Fatalf("destination was written: %q", got)
		}
	})
	t.Run("a successful run probes in the second pass and before the exclude write, never in the first", func(t *testing.T) {
		root, env, ctx := tm5LocalFixture(t)
		writeLocalSetupFixtureFile(t, root, localInstructionFile, "project head\n")
		calls := stubExcludeProbe(t, false)
		code, _, msg := runSetupWrite([]string{"--local", "--no-hooks"}, ctx, env, root)
		if code != 0 {
			t.Fatalf("code=%d msg=%q", code, msg)
		}
		if *calls != 2 {
			t.Fatalf("probe calls = %d, want 2", *calls)
		}
	})
	t.Run("dry-run never probes", func(t *testing.T) {
		root, env, ctx := tm5LocalFixture(t)
		writeLocalSetupFixtureFile(t, root, localInstructionFile, "project head\n")
		calls := stubExcludeProbe(t, true)
		code, _, msg := runSetupWrite([]string{"--local", "--no-hooks", "--dry-run"}, ctx, env, root)
		if code != 0 {
			t.Fatalf("code=%d msg=%q", code, msg)
		}
		if *calls != 0 {
			t.Fatalf("probe calls = %d, want 0", *calls)
		}
	})
}
