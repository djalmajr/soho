package setup

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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

func TestHookSelectionWithOtherGuard(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		for _, event := range []string{"UserPromptSubmit", "SessionStart"} {
			for _, guardFlag := range []bool{false, true} {
				for _, tc := range []struct {
					name  string
					forms []string
				}{{"plain", []string{"plain"}}, {"guarded", []string{"guard"}}, {"plain-first", []string{"plain", "guard"}}, {"guard-first", []string{"guard", "plain"}}} {
					t.Run(provider+"/"+event+"/"+tc.name+map[bool]string{false: "/ordinary", true: "/guarded-setup"}[guardFlag], func(t *testing.T) {
						root, env, ctx := setupWriteFixture(t)
						plain, guarded, other := setuptext.SetupHookReminder(), setuptext.GuardedHookReminder(), setuptext.GuardedHookDoctor()
						file := ".claude/settings.json"
						if event == "SessionStart" {
							plain, guarded, other = setuptext.SetupHookDoctor(), setuptext.GuardedHookDoctor(), setuptext.GuardedHookReminder()
						}
						if provider == "codex" {
							file = ".codex/hooks.json"
							plain, guarded, other = setuptext.SetupHookCodexReminder(), setuptext.GuardedHookCodexReminder(), setuptext.GuardedHookCodexDoctor()
							if event == "SessionStart" {
								plain, guarded, other = setuptext.SetupHookCodexDoctor(), setuptext.GuardedHookCodexDoctor(), setuptext.GuardedHookCodexReminder()
							}
						}
						rows := []any{}
						wantMarker := "plain"
						for _, form := range tc.forms {
							command := plain
							if form == "guard" {
								command, wantMarker = guarded, "guard"
							}
							rows = append(rows, map[string]any{"matcher": form + "-row", "hooks": []any{map[string]any{"type": "command", "command": command, "marker": form, "timeout": 73}}})
						}
						foreign := []any{map[string]any{"matcher": "other-row", "hooks": []any{map[string]any{"type": "command", "command": other, "marker": "other", "timeout": float64(42)}}}, map[string]any{"matcher": "user-row", "hooks": []any{map[string]any{"type": "command", "command": "echo user hook", "marker": "user"}}}}
						rows = append(rows, foreign...)
						body, err := json.Marshal(map[string]any{"hooks": map[string]any{event: rows}})
						if err != nil {
							t.Fatal(err)
						}
						path := filepath.Join(root, file)
						if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(path, body, 0600); err != nil {
							t.Fatal(err)
						}
						var args []string
						if guardFlag {
							args = []string{"--guard-hooks"}
						}
						if rc, out, msg := runSetupWrite(args, ctx, env, root); rc != 0 {
							t.Fatalf("setup=%d %s %s", rc, out, msg)
						}
						raw, err := os.ReadFile(path)
						if err != nil {
							t.Fatal(err)
						}
						var doc struct {
							Hooks map[string][]map[string]any `json:"hooks"`
						}
						if err := json.Unmarshal(raw, &doc); err != nil {
							t.Fatal(err)
						}
						got := doc.Hooks[event]
						if len(got) != 3 {
							t.Fatalf("rows: %s", raw)
						}
						wantCommand := plain
						if wantMarker == "guard" || guardFlag {
							wantCommand = guarded
						}
						entry := map[string]any{"type": "command", "command": wantCommand, "marker": wantMarker, "timeout": float64(73)}
						if provider == "codex" && guardFlag {
							entry["commandWindows"] = plain
						}
						want := map[string]any{"matcher": wantMarker + "-row", "hooks": []any{entry}}
						if !reflect.DeepEqual(got[0], want) || !reflect.DeepEqual(got[1], foreign[0]) || !reflect.DeepEqual(got[2], foreign[1]) {
							t.Fatalf("selection or metadata changed: %s", raw)
						}
						if rc, out, msg := runSetupWrite(args, ctx, env, root); rc != 0 {
							t.Fatalf("repeat=%d %s %s", rc, out, msg)
						}
						again, err := os.ReadFile(path)
						if err != nil || string(again) != string(raw) {
							t.Fatalf("rerun changed: %v\n%s", err, again)
						}
					})
				}
			}
		}
	}
}

func TestGuardHooksUpgradeAndPlan(t *testing.T) {
	root, env, ctx := setupWriteFixture(t)
	if code, out, msg := runSetupWrite(nil, ctx, env, root); code != 0 || !strings.Contains(out, "the versioned hooks call soho") {
		t.Fatalf("plain: %d %s %s", code, out, msg)
	}
	if code, out, msg := runSetupWrite([]string{"--plan", "--guard-hooks"}, ctx, env, root); code != 0 || !strings.Contains(out, setuptext.GuardedHookReminder()) {
		t.Fatalf("plan: %d %s %s", code, out, msg)
	}
	if code, out, msg := runSetupWrite([]string{"--guard-hooks"}, ctx, env, root); code != 0 || strings.Contains(out, "the versioned hooks call soho") {
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
	if code, out, msg := runSetupWrite([]string{"--local"}, ctx, env, root); code != 0 || strings.Contains(out, "the versioned hooks call soho") {
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

func TestHandGuardedHooksReplacement(t *testing.T) {
	for _, guard := range []bool{false, true} {
		for _, codex := range []bool{false, true} {
			suffix := ""
			if codex {
				suffix = " --codex"
			}
			reminder := "command -v soho >/dev/null 2>&1 || exit 0; soho hook reminder" + suffix
			doctor := "sh -c \"command  -v soho > /dev/null 2>&1 || exit 0; exec soho hook doctor" + suffix + "\""
			foreign := "command -v foo >/dev/null 2>&1 || exit 0; foo"
			input, _ := json.Marshal(map[string]any{"hooks": map[string]any{
				"UserPromptSubmit": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": reminder}, map[string]any{"type": "command", "command": foreign}}}},
				"SessionStart":     []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": doctor}}}},
			}})
			text := string(input)
			var result *string
			if codex {
				if guard {
					result = setuptext.CodexHooksGuardedResult(&text)
				} else {
					result = setuptext.CodexHooksResult(&text)
				}
			} else {
				if guard {
					result = setuptext.SettingsHooksGuardedResult(&text)
				} else {
					result = setuptext.SettingsHooksResult(&text)
				}
			}
			if result == nil {
				t.Fatal("merge failed")
			}
			var doc struct {
				Hooks map[string][]struct {
					Hooks []map[string]any `json:"hooks"`
				} `json:"hooks"`
			}
			if err := json.Unmarshal([]byte(*result), &doc); err != nil {
				t.Fatal(err)
			}
			for event, name := range map[string]string{"UserPromptSubmit": "reminder", "SessionStart": "doctor"} {
				count := 0
				for _, row := range doc.Hooks[event] {
					for _, hook := range row.Hooks {
						cmd, _ := hook["command"].(string)
						if cmd == foreign {
							continue
						}
						count++
						want := "soho hook " + name + suffix
						if guard {
							want = "sh -c 'command -v soho >/dev/null 2>&1 || exit 0; exec " + want + "'"
						}
						if cmd != want {
							t.Fatalf("guard=%v codex=%v got %q want %q", guard, codex, cmd, want)
						}
						if guard && codex && hook["commandWindows"] != "soho hook "+name+suffix {
							t.Fatalf("missing commandWindows: %v", hook)
						}
					}
				}
				if count != 1 {
					t.Fatalf("%s count %d: %s", event, count, *result)
				}
			}
			if !strings.Contains(decodeHookText(t, []byte(*result)), foreign) {
				t.Fatalf("foreign lost: %s", *result)
			}
			if guard {
				var again *string
				if codex {
					again = setuptext.CodexHooksGuardedResult(result)
				} else {
					again = setuptext.SettingsHooksGuardedResult(result)
				}
				if again == nil || *again != *result {
					t.Fatalf("non-idempotent: %v", again)
				}
			}
		}
	}
}

func TestHandGuardedPlanAndWindowsPreservation(t *testing.T) {
	root, env, ctx := setupWriteFixture(t)
	path := filepath.Join(root, ".codex", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	before := `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"command -v soho >/dev/null 2>&1 || exit 0; soho hook reminder --codex","commandWindows":"custom windows"}]}],"SessionStart":[{"hooks":[{"type":"command","command":"command -v soho >/dev/null 2>&1 || exit 0; soho hook doctor --codex"}]}]}}`
	if err := os.WriteFile(path, []byte(before), 0600); err != nil {
		t.Fatal(err)
	}
	code, out, msg := runSetupWrite([]string{"--plan", "--guard-hooks"}, ctx, env, root)
	if code != 0 || !strings.Contains(out, setuptext.GuardedHookCodexReminder()) || strings.Count(out, setuptext.GuardedHookCodexReminder()) != 1 {
		t.Fatalf("plan: %d %s %s", code, out, msg)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != before {
		t.Fatalf("plan wrote: %v %s", err, after)
	}
	result := setuptext.CodexHooksResult(&before)
	if result == nil || !strings.Contains(*result, "custom windows") || !setuptext.CodexHooksProtected(*result) || !setuptext.CodexHooksCurrent(*result) {
		t.Fatalf("plain result: %v", result)
	}
}

func TestPlainCodexSetupKeepsWindowsUntouched(t *testing.T) {
	for _, windows := range []string{"", `,"commandWindows":"custom"`} {
		input := `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":` + string(mustJSONForGuardTest(t, setuptext.GuardedHookCodexReminder())) + windows + `}]}],"SessionStart":[{"hooks":[{"type":"command","command":` + string(mustJSONForGuardTest(t, setuptext.GuardedHookCodexDoctor())) + `}]}]}}`
		got := setuptext.CodexHooksResult(&input)
		if got == nil {
			t.Fatal("merge failed")
		}
		var doc struct {
			Hooks map[string][]struct {
				Hooks []map[string]any `json:"hooks"`
			} `json:"hooks"`
		}
		if err := json.Unmarshal([]byte(*got), &doc); err != nil {
			t.Fatal(err)
		}
		up := doc.Hooks["UserPromptSubmit"][0].Hooks[0]
		_, present := up["commandWindows"]
		if present != (windows != "") || (present && up["commandWindows"] != "custom") {
			t.Fatalf("windows changed: %v", up)
		}
		if _, present := doc.Hooks["SessionStart"][0].Hooks[0]["commandWindows"]; present {
			t.Fatal("added commandWindows")
		}
	}
}
func mustJSONForGuardTest(t *testing.T, value string) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestR470PlainRerunPreservesHandGuardedMetadata(t *testing.T) {
	root, env, ctx := setupWriteFixture(t)
	path := filepath.Join(root, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	before := `{"hooks":{"UserPromptSubmit":[{"matcher":"keep","hooks":[{"type":"command","command":"command -v soho >/dev/null 2>&1 || exit 0; soho hook reminder","timeout":73}]}],"SessionStart":[{"hooks":[{"type":"command","command":"command -v soho >/dev/null 2>&1 || exit 0; soho hook doctor"}]}]}}`
	if err := os.WriteFile(path, []byte(before), 0600); err != nil {
		t.Fatal(err)
	}
	var first string
	for pass := 1; pass <= 2; pass++ {
		code, out, msg := runSetupWrite(nil, ctx, env, root)
		if code != 0 {
			t.Fatalf("pass=%d code=%d out=%s msg=%s", pass, code, out, msg)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("pass=%d settings=%s", pass, raw)
		if pass == 1 {
			first = string(raw)
		}
		if !strings.Contains(string(raw), `"matcher": "keep"`) || !strings.Contains(string(raw), `"timeout": 73`) {
			t.Errorf("pass=%d lost matcher or timeout", pass)
		}
		if pass == 2 && first != string(raw) {
			t.Error("second plain setup changed the migrated file")
		}
	}
}

func TestR470GuardTokenBoundary(t *testing.T) {
	for _, guard := range []string{"command -v soho2>/dev/null || exit 0; soho hook reminder", "command -v soho1>/dev/null || exit 0; soho hook reminder"} {
		matched := setuptext.HookCommandMatches(guard, setuptext.SetupHookReminder())
		if matched {
			t.Errorf("false match %q", guard)
		}
		if runtime.GOOS == "windows" {
			continue
		}
		cmd := exec.Command("/bin/sh", "-c", "soho() { printf 'CALLED'; }; "+guard)
		cmd.Env = []string{"PATH=" + t.TempDir()}
		out, err := cmd.CombinedOutput()
		t.Logf("command=%q matcher=%v shell_output=%q error=%v", guard, matched, out, err)
		if err != nil {
			t.Fatal(err)
		}
		if matched && string(out) != "CALLED" {
			t.Error("matcher accepts a guard that skips the available soho function")
		}
	}
}

func TestR470ControlsV2(t *testing.T) {
	commands := []string{
		"command -v soho || exit 0; soho-other",
		"command -v soho || exit 0; soho hook reminder && echo extra",
		"sh -c 'command -v soho || exit 0; soho hook reminder' extra",
		"sh -c \"command -v soho || exit 0; soho hook reminder'",
		"command -v soho || exit 0; C:\\tools\\soho.exe hook reminder",
		"sh -c 'command -v soho || exit 0; soho hook \"reminder\"'",
		"sh -c \"command -v soho || exit 0; soho hook \\\"reminder\\\"\"",
	}
	for _, s := range commands {
		if setuptext.HookCommandMatches(s, setuptext.SetupHookReminder()) {
			t.Errorf("false match %q", s)
		}
		data, _ := json.Marshal(map[string]any{"hooks": map[string]any{"UserPromptSubmit": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": s}}}}}})
		body := string(data)
		got := setuptext.SettingsHooksGuardedResult(&body)
		if got == nil {
			t.Fatal("nil merge")
		}
		decoded := decodeHookText(t, []byte(*got))
		if !strings.Contains(decoded, s+"\n") {
			t.Errorf("foreign command lost %q: %s", s, *got)
		}
		t.Logf("foreign command retained: %q", s)
	}
}

func TestR470GuardConsumer(t *testing.T) {
	before := `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"command -v soho2>/dev/null || exit 0; soho hook reminder --codex"}]}]}}`
	protected := setuptext.CodexHooksProtected(before)
	merged := setuptext.CodexHooksGuardedResult(&before)
	t.Logf("doctor protection consumer=%v setup merge=%s", protected, *merged)
	if protected {
		t.Error("doctor consumer accepts unavailable soho2 guard as peer protection")
	}
	if !strings.Contains(*merged, "soho2") {
		t.Error("setup rewrites an unrelated availability guard")
	}
}

func TestR470GuardTokenPositiveControls(t *testing.T) {
	for _, prefix := range []string{"soho", "soho>/dev/null", "soho >/dev/null", "soho 2>&1", "soho 1>/dev/null"} {
		command := "command -v " + prefix + " || exit 0; soho hook reminder"
		if !setuptext.HookCommandMatches(command, setuptext.SetupHookReminder()) {
			t.Errorf("guard not recognized: %q", command)
		}
	}
}

func TestR470GuardedMetadataIdempotence(t *testing.T) {
	for _, codex := range []bool{false, true} {
		suffix := ""
		if codex {
			suffix = " --codex"
		}
		body := `{"hooks":{"UserPromptSubmit":[{"matcher":"keep","hooks":[{"type":"command","command":"command -v soho >/dev/null 2>&1 || exit 0; exec soho hook reminder` + suffix + `","timeout":73,"commandWindows":"custom"}]}]}}`
		var got, again *string
		if codex {
			got = setuptext.CodexHooksGuardedResult(&body)
			again = setuptext.CodexHooksGuardedResult(got)
		} else {
			got = setuptext.SettingsHooksGuardedResult(&body)
			again = setuptext.SettingsHooksGuardedResult(got)
		}
		if got == nil || again == nil || *got != *again || !strings.Contains(*got, `"timeout": 73`) || !strings.Contains(*got, `"matcher": "keep"`) {
			t.Fatalf("guarded codex=%v metadata or idempotence failed: %v", codex, got)
		}
	}
}

func TestMixedHooksRetainGuardAndMetadata(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		for _, event := range []string{"UserPromptSubmit", "SessionStart"} {
			for _, guardFirst := range []bool{false, true} {
				t.Run(provider+"/"+event+map[bool]string{false: "/plain-first", true: "/guard-first"}[guardFirst], func(t *testing.T) {
					root, env, ctx := setupWriteFixture(t)
					plain, guarded := setuptext.SetupHookReminder(), setuptext.GuardedHookReminder()
					if event == "SessionStart" {
						plain, guarded = setuptext.SetupHookDoctor(), setuptext.GuardedHookDoctor()
					}
					path := ".claude/settings.json"
					if provider == "codex" {
						path = ".codex/hooks.json"
						plain, guarded = setuptext.SetupHookCodexReminder(), setuptext.GuardedHookCodexReminder()
						if event == "SessionStart" {
							plain, guarded = setuptext.SetupHookCodexDoctor(), setuptext.GuardedHookCodexDoctor()
						}
					}
					rows := []any{map[string]any{"matcher": "plain-row", "hooks": []any{map[string]any{"type": "command", "command": plain, "marker": "plain"}}}, map[string]any{"matcher": "guard-row", "hooks": []any{map[string]any{"type": "command", "command": guarded, "marker": "guard"}}}}
					if guardFirst {
						rows[0], rows[1] = rows[1], rows[0]
					}
					body, err := json.Marshal(map[string]any{"hooks": map[string]any{event: rows}})
					if err != nil {
						t.Fatal(err)
					}
					file := filepath.Join(root, path)
					if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(file, body, 0600); err != nil {
						t.Fatal(err)
					}
					if rc, out, msg := runSetupWrite(nil, ctx, env, root); rc != 0 {
						t.Fatalf("setup=%d %s %s", rc, out, msg)
					}
					raw, err := os.ReadFile(file)
					if err != nil {
						t.Fatal(err)
					}
					var doc struct {
						Hooks map[string][]struct {
							Matcher string           `json:"matcher"`
							Hooks   []map[string]any `json:"hooks"`
						} `json:"hooks"`
					}
					if err := json.Unmarshal(raw, &doc); err != nil {
						t.Fatal(err)
					}
					got := doc.Hooks[event]
					if len(got) != 1 || got[0].Matcher != "guard-row" || len(got[0].Hooks) != 1 || got[0].Hooks[0]["command"] != guarded || got[0].Hooks[0]["marker"] != "guard" {
						t.Fatalf("retained rows: %s", raw)
					}
					if runtime.GOOS != "windows" {
						bin := t.TempDir()
						if err := os.Symlink("/bin/sh", filepath.Join(bin, "sh")); err != nil {
							t.Fatal(err)
						}
						cmd := exec.Command("/bin/sh", "-c", guarded)
						cmd.Env = []string{"PATH=" + bin}
						if out, err := cmd.CombinedOutput(); err != nil || len(out) != 0 {
							t.Fatalf("without soho: %v %q", err, out)
						}
					}
					if rc, out, msg := runSetupWrite(nil, ctx, env, root); rc != 0 {
						t.Fatalf("repeat=%d %s %s", rc, out, msg)
					}
					again, err := os.ReadFile(file)
					if err != nil || string(again) != string(raw) {
						t.Fatalf("rerun changed %s: %v", path, err)
					}
				})
			}
		}
	}
}
