package setup

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/setuptext"
)

func TestGuardHooksSetup(t *testing.T) {
	root, env, ctx := setupWriteFixture(t)
	code, out, msg := runSetupWrite([]string{"--guard-hooks"}, ctx, env, root)
	if code != 0 {
		t.Fatalf("setup: %d %s %s", code, out, msg)
	}
	files := []struct {
		path     string
		commands []string
	}{
		{".claude/settings.json", []string{"sh -c 'command -v soho >/dev/null 2>&1 || exit 0; exec soho hook reminder'", "sh -c 'command -v soho >/dev/null 2>&1 || exit 0; exec soho hook doctor'"}},
		{".codex/hooks.json", []string{"sh -c 'command -v soho >/dev/null 2>&1 || exit 0; exec soho hook reminder --codex'", "sh -c 'command -v soho >/dev/null 2>&1 || exit 0; exec soho hook doctor --codex'"}},
	}
	before := make([]string, len(files))
	for i, f := range files {
		raw, err := os.ReadFile(filepath.Join(root, f.path))
		if err != nil {
			t.Fatal(err)
		}
		before[i] = string(raw)
		decoded := decodeHookText(t, raw)
		for _, c := range f.commands {
			if strings.Count(decoded, c) != 1 {
				t.Fatalf("%s missing or duplicated %q: %s", f.path, c, raw)
			}
		}
	}
	code, out, msg = runSetupWrite(nil, ctx, env, root)
	if code != 0 {
		t.Fatalf("repeat: %d %s %s", code, out, msg)
	}
	for i, f := range files {
		raw, err := os.ReadFile(filepath.Join(root, f.path))
		if err != nil || string(raw) != before[i] {
			t.Fatalf("%s changed: %v\n%s", f.path, err, raw)
		}
	}
}

func TestGuardHooksUpgradeAndPlan(t *testing.T) {
	root, env, ctx := setupWriteFixture(t)
	if code, out, msg := runSetupWrite(nil, ctx, env, root); code != 0 || !strings.Contains(out, "the versioned Claude Code and Codex hooks call soho") {
		t.Fatalf("plain: %d %s %s", code, out, msg)
	} else if !strings.Contains(out, "anyone who opens a clone in those assistants without soho installed") || strings.Contains(out, "public repository") {
		// SOH-348: the hint holds for private repositories too (a
		// teammate or CI without soho), so it names no visibility.
		t.Fatalf("plain hint assumes a public repository: %s", out)
	}
	if code, out, msg := runSetupWrite([]string{"--plan", "--guard-hooks"}, ctx, env, root); code != 0 || !strings.Contains(out, setuptext.GuardedHookReminder()) {
		t.Fatalf("plan: %d %s %s", code, out, msg)
	}
	if code, out, msg := runSetupWrite([]string{"--guard-hooks"}, ctx, env, root); code != 0 || strings.Contains(out, "the versioned Claude Code and Codex hooks call soho") {
		t.Fatalf("guard: %d %s %s", code, out, msg)
	}
	for _, f := range []struct{ path, command string }{{".claude/settings.json", setuptext.GuardedHookReminder()}, {".codex/hooks.json", setuptext.GuardedHookCodexReminder()}} {
		raw, err := os.ReadFile(filepath.Join(root, f.path))
		if err != nil || strings.Count(decodeHookText(t, raw), f.command) != 1 {
			t.Fatalf("upgrade %s: %v %s", f.path, err, raw)
		}
	}
	for _, item := range []struct {
		path  string
		plain []string
	}{
		{".claude/settings.json", []string{setuptext.SetupHookReminder(), setuptext.SetupHookDoctor()}},
		{".codex/hooks.json", []string{setuptext.SetupHookCodexReminder(), setuptext.SetupHookCodexDoctor()}},
	} {
		raw, err := os.ReadFile(filepath.Join(root, item.path))
		if err != nil {
			t.Fatal(err)
		}
		for _, command := range strings.Split(decodeHookText(t, raw), "\n") {
			for _, plain := range item.plain {
				if command == plain {
					t.Fatalf("%s retained plain command %q", item.path, plain)
				}
			}
		}
	}
	root, env, ctx = setupWriteFixture(t)
	if code, out, msg := runSetupWrite([]string{"--local"}, ctx, env, root); code != 0 || strings.Contains(out, "the versioned Claude Code and Codex hooks call soho") {
		t.Fatalf("local: %d %s %s", code, out, msg)
	}
}

func TestGuardHookWithoutSoho(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires sh")
	}
	path := t.TempDir()
	if err := os.Symlink("/bin/sh", filepath.Join(path, "sh")); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", "-c", setuptext.GuardedHookReminder())
	cmd.Env = []string{"PATH=" + path}
	out, err := cmd.CombinedOutput()
	if err != nil || len(out) != 0 {
		t.Fatalf("guard: %v output %q", err, out)
	}
}

func decodeHookText(t *testing.T, raw []byte) string {
	t.Helper()
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	return collectCommands(value)
}
func collectCommands(v any) string {
	var out string
	switch x := v.(type) {
	case map[string]any:
		for k, item := range x {
			if k == "command" {
				if c, ok := item.(string); ok {
					out += c + "\n"
				}
				continue
			}
			out += collectCommands(item)
		}
	case []any:
		for _, item := range x {
			out += collectCommands(item)
		}
	}
	return out
}

func TestGuardHookDuplicateLocations(t *testing.T) {
	root, env, ctx := setupWriteFixture(t)
	if code, out, msg := runSetupWrite([]string{"--guard-hooks"}, ctx, env, root); code != 0 {
		t.Fatalf("%d %s %s", code, out, msg)
	}
	shared := filepath.Join(root, ".claude/settings.json")
	raw, err := os.ReadFile(shared)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".claude/settings.local.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	code, out, msg := runSetupWrite(nil, ctx, env, root)
	if code != 0 || !strings.Contains(out, "hooks run twice") {
		t.Fatalf("duplicate: %d %s %s", code, out, msg)
	}
}

func TestGuardHookMaskEachEvent(t *testing.T) {
	for _, tc := range []struct {
		event, command string
		want           uint8
	}{
		{"UserPromptSubmit", setuptext.GuardedHookReminder(), 1},
		{"SessionStart", setuptext.GuardedHookDoctor(), 2},
	} {
		t.Run(tc.event, func(t *testing.T) {
			encoded, err := json.Marshal(tc.command)
			if err != nil {
				t.Fatal(err)
			}
			input := `{"hooks":{"` + tc.event + `":[{"hooks":[{"type":"command","command":` + string(encoded) + `}]}]}}`
			if got := claudeHookMask(&input); got != tc.want {
				t.Fatalf("mask=%d want=%d", got, tc.want)
			}
		})
	}
}

func TestGuardHooksUpgradeInPlace(t *testing.T) {
	root, env, ctx := setupWriteFixture(t)
	for _, f := range []struct{ path, suffix string }{{".claude/settings.json", ""}, {".codex/hooks.json", " --codex"}} {
		path := filepath.Join(root, f.path)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		body := `{"hooks":{"UserPromptSubmit":[{"matcher":"keep","hooks":[{"type":"command","command":"before"},{"type":"command","command":"soho hook reminder` + f.suffix + `","marker":"keep"},{"type":"command","command":"after"}]}],"SessionStart":[{"hooks":[{"type":"command","command":"soho hook doctor` + f.suffix + `"}]}]}}`
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if code, out, msg := runSetupWrite([]string{"--guard-hooks"}, ctx, env, root); code != 0 {
		t.Fatalf("%d %s %s", code, out, msg)
	}
	for _, f := range []struct{ path, command string }{{".claude/settings.json", setuptext.GuardedHookReminder()}, {".codex/hooks.json", setuptext.GuardedHookCodexReminder()}} {
		raw, err := os.ReadFile(filepath.Join(root, f.path))
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Hooks struct {
				UserPromptSubmit []struct {
					Matcher string `json:"matcher"`
					Hooks   []struct {
						Command string `json:"command"`
						Marker  string `json:"marker"`
					} `json:"hooks"`
				} `json:"UserPromptSubmit"`
			} `json:"hooks"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		rows := doc.Hooks.UserPromptSubmit
		if len(rows) != 1 || rows[0].Matcher != "keep" || len(rows[0].Hooks) != 3 || rows[0].Hooks[0].Command != "before" || rows[0].Hooks[1].Command != f.command || rows[0].Hooks[1].Marker != "keep" || rows[0].Hooks[2].Command != "after" {
			t.Fatalf("%s: %s", f.path, raw)
		}
	}
}

// R443 F1: an ordinary setup over a Codex event that holds both the plain and
// the guarded reminder keeps the guarded one, in either order, and leaves one.
func TestGuardHooksMixedCodexPairKeepsTheGuardedLine(t *testing.T) {
	for _, guardedFirst := range []bool{false, true} {
		plain := map[string]any{"type": "command", "command": setuptext.SetupHookCodexReminder()}
		guarded := map[string]any{"type": "command", "command": setuptext.GuardedHookCodexReminder()}
		entries := []any{plain, guarded}
		if guardedFirst {
			entries = []any{guarded, plain}
		}
		body, _ := json.Marshal(map[string]any{"hooks": map[string]any{"UserPromptSubmit": []any{map[string]any{"hooks": entries}}}})
		input := string(body)
		merged := setuptext.CodexHooksResult(&input)
		if merged == nil {
			t.Fatal("merge refused")
		}
		commands := decodeHookText(t, []byte(*merged))
		if strings.Count(commands, setuptext.GuardedHookCodexReminder()) != 1 || strings.Contains(commands, setuptext.SetupHookCodexReminder()+"\n") {
			t.Fatalf("guarded-first=%v commands=%q; want only the guarded reminder", guardedFirst, commands)
		}
		again := setuptext.CodexHooksResult(merged)
		if again == nil || *again != *merged {
			t.Fatalf("guarded-first=%v: a rerun changed the merged text", guardedFirst)
		}
	}
}
