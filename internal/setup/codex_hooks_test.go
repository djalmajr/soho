package setup

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

const codexCommand = "soho hook inbound --codex"

func codexSetupFixture(t *testing.T) (string, platform.Env, *core.Config) {
	t.Helper()
	root, env, ctx := setupWriteFixture(t)
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	env["USERPROFILE"], env["TMPDIR"] = env["HOME"], t.TempDir()
	return root, env, ctx
}

func codexFile(root string) string { return filepath.Join(root, ".codex", "hooks.json") }
func codexHookCount(t *testing.T, file string) int {
	t.Helper()
	b, e := os.ReadFile(file)
	if e != nil {
		t.Fatal(e)
	}
	var doc map[string]any
	if e = json.Unmarshal(b, &doc); e != nil {
		t.Fatal(e)
	}
	hooks, ok := doc["hooks"].(map[string]any)
	if !ok {
		t.Fatal("missing hooks")
	}
	rows, ok := hooks["UserPromptSubmit"].([]any)
	if !ok {
		t.Fatal("missing UserPromptSubmit")
	}
	n := 0
	for _, row := range rows {
		for _, entry := range row.(map[string]any)["hooks"].([]any) {
			obj := entry.(map[string]any)
			if obj["type"] == "command" && obj["command"] == codexCommand {
				n++
			}
		}
	}
	return n
}

// Tokens retain number lexemes and every object's key order, unlike a map/float64 comparison.
func codexJSONTokens(t *testing.T, raw string) []json.Token {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var tokens []json.Token
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return tokens
		}
		if err != nil {
			t.Fatal(err)
		}
		tokens = append(tokens, token)
	}
}

func TestSetupCodexForeignJSONLossless(t *testing.T) {
	root, env, ctx := codexSetupFixture(t)
	file := codexFile(root)
	prefix := `{"z":9007199254740993,"10":1e+030,"2":-0,"\ud800":"foreign key","custom":{"z":1.234567890123456789,"a":"\u0061\\\""},"hooks":{"SessionStart":[{"z":true,"a":null,"hooks":[{"metadata":{"z":9007199254740993,"a":[-99999999999999999999,1E-009]},"command":"foreign start","type":"command"}]}],"UserPromptSubmit":[{"z":false,"a":{},"hooks":[{"metadata":{"z":9007199254740993,"10":2,"2":1,"a":"foreign"},"command":"foreign prompt","type":"command"}]}`
	suffix := `]},"a":[{"z":0.00000000000000000001,"a":-0.0}]}`
	existing := prefix + suffix
	want := prefix + `,{"hooks":[{"type":"command","command":"soho hook inbound --codex"}]}` + suffix
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}
	code, _, message := runSetupWrite(nil, ctx, env, root)
	got, err := os.ReadFile(file)
	if code != 0 || err != nil {
		t.Fatalf("code=%d read=%v message=%s", code, err, message)
	}
	if !reflect.DeepEqual(codexJSONTokens(t, string(got)), codexJSONTokens(t, want)) {
		t.Fatalf("foreign JSON number literal, value, or object key order changed:\ngot=%s\nwant=%s", got, want)
	}
	if !strings.Contains(string(got), `"\ud800"`) {
		t.Fatalf("foreign key's code unit changed: %s", got)
	}
	if count := codexHookCount(t, file); count != 1 {
		t.Fatalf("got %d soho hooks", count)
	}
}

func TestSetupCodexSynchronousHook(t *testing.T) {
	for _, variant := range []string{`true`, `null`, `"false"`, `0`, `[]`, `{}`} {
		t.Run(variant, func(t *testing.T) {
			root, env, ctx := codexSetupFixture(t)
			file := codexFile(root)
			entry := `{"z":"keep","hooks":[{"async":` + variant + `,"command":"soho hook inbound --codex","type":"command","metadata":{"z":1,"a":2}}]}`
			existing := `{"hooks":{"UserPromptSubmit":[` + entry + `]}}`
			want := `{"hooks":{"UserPromptSubmit":[` + entry + `,{"hooks":[{"type":"command","command":"soho hook inbound --codex"}]}]}}`
			if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte(existing), 0600); err != nil {
				t.Fatal(err)
			}
			var first string
			for i := 0; i < 2; i++ {
				code, _, message := runSetupWrite(nil, ctx, env, root)
				got, err := os.ReadFile(file)
				if code != 0 || err != nil {
					t.Fatalf("run=%d code=%d read=%v message=%s", i, code, err, message)
				}
				if !reflect.DeepEqual(codexJSONTokens(t, string(got)), codexJSONTokens(t, want)) {
					t.Fatalf("async=%s: original entry must remain and synchronous hook must be appended: %s", variant, got)
				}
				if i == 0 {
					first = string(got)
				} else if string(got) != first {
					t.Fatalf("second setup changed bytes: %s", got)
				}
			}
		})
	}
}

func TestSetupCodexHooksMergeAndIdempotence(t *testing.T) {
	for _, existing := range []string{"", `{"custom":{"keep":true},"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"other start"}]}],"UserPromptSubmit":[{"matcher":"custom","hooks":[{"type":"command","command":"other prompt"}]}]}}`, `{"hooks":{"UserPromptSubmit":[{"matcher":"keep","hooks":[{"type":"command","command":"soho hook inbound --codex"},{"type":"command","command":"other prompt"}]}]}}`} {
		t.Run(existing, func(t *testing.T) {
			root, env, ctx := codexSetupFixture(t)
			file := codexFile(root)
			if existing != "" {
				if e := os.MkdirAll(filepath.Dir(file), 0700); e != nil {
					t.Fatal(e)
				}
				if e := os.WriteFile(file, []byte(existing), 0600); e != nil {
					t.Fatal(e)
				}
			}
			for i := 0; i < 2; i++ {
				code, _, msg := runSetupWrite(nil, ctx, env, root)
				if code != 0 {
					t.Fatalf("code=%d message=%s", code, msg)
				}
				if n := codexHookCount(t, file); n != 1 {
					t.Fatalf("run %d: got %d codex hooks", i, n)
				}
			}
			b, _ := os.ReadFile(file)
			if existing != "" {
				assertCodexSettingsPreserved(t, existing, b)
			}
			if strings.Contains(existing, "other prompt") && !strings.Contains(string(b), "other prompt") {
				t.Fatalf("foreign prompt lost: %s", b)
			}
			if strings.Contains(existing, "other start") && (!strings.Contains(string(b), "other start") || !strings.Contains(string(b), `"custom"`)) {
				t.Fatalf("settings lost: %s", b)
			}
			before := string(b)
			runSetupWrite(nil, ctx, env, root)
			after, _ := os.ReadFile(file)
			if string(after) != before {
				t.Fatal("second merge changed bytes")
			}
		})
	}
}

func assertCodexSettingsPreserved(t *testing.T, original string, result []byte) {
	t.Helper()
	var before, after map[string]any
	if err := json.Unmarshal([]byte(original), &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(result, &after); err != nil {
		t.Fatal(err)
	}
	for key, value := range before {
		if key != "hooks" {
			if !reflect.DeepEqual(value, after[key]) {
				t.Fatalf("setting %s changed: %v -> %v", key, value, after[key])
			}
			continue
		}
		for event, rows := range value.(map[string]any) {
			want := rows.([]any)
			got := after["hooks"].(map[string]any)[event].([]any)
			if len(got) < len(want) || !reflect.DeepEqual(want, got[:len(want)]) {
				t.Fatalf("existing %s hooks changed: %v -> %v", event, want, got)
			}
		}
	}
}
func TestSetupCodexInvalidPreserved(t *testing.T) {
	for _, bad := range []string{"", " \n\t", "{broken", `{"hooks":[]}`, `{"hooks":{"UserPromptSubmit":{}}}`} {
		t.Run(bad, func(t *testing.T) {
			root, env, ctx := codexSetupFixture(t)
			file := codexFile(root)
			if e := os.MkdirAll(filepath.Dir(file), 0700); e != nil {
				t.Fatal(e)
			}
			if e := os.WriteFile(file, []byte(bad), 0600); e != nil {
				t.Fatal(e)
			}
			code, _, msg := runSetupWrite(nil, ctx, env, root)
			b, e := os.ReadFile(file)
			if code != 0 || e != nil || string(b) != bad || !strings.Contains(msg, "could not merge hooks into "+file) {
				t.Fatalf("code=%d file=%q err=%v warning=%s", code, b, e, msg)
			}
		})
	}
}

func TestSetupCodexIncompatiblePreserved(t *testing.T) {
	protected := `{"hooks":[{"type":"command","command":"soho hook inbound --codex"}]}`
	for _, tc := range []struct{ name, rows string }{
		{"numeric-command", `{"hooks":[{"type":"command","command":7}]}`},
		{"bad-row-before-protected", `7,` + protected},
		{"bad-row-after-protected", protected + `,7`},
		{"bad-command-before-protected", `{"hooks":[{"command":7},{"type":"command","command":"soho hook inbound --codex"}]}`},
		{"bad-command-after-protected", `{"hooks":[{"type":"command","command":"soho hook inbound --codex"},{"command":7}]}`},
		{"zero-command", `{"hooks":[{"command":0}]}`},
		{"true-command", `{"hooks":[{"command":true}]}`},
		{"array-command", `{"hooks":[{"command":[]}]}`},
		{"object-command", `{"hooks":[{"command":{}}]}`},
		{"object-event-hooks", `{"hooks":{}},` + protected},
		{"array-row", `[],` + protected},
		{"boolean-row", `false,` + protected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, env, ctx := codexSetupFixture(t)
			file := codexFile(root)
			original := `{"hooks":{"UserPromptSubmit":[` + tc.rows + `]}}`
			if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte(original), 0600); err != nil {
				t.Fatal(err)
			}
			code, out, warning := runSetupWrite(nil, ctx, env, root)
			got, err := os.ReadFile(file)
			if code != 0 || err != nil || string(got) != original || !strings.Contains(warning, "could not merge hooks into "+file) || strings.Contains(out, "hooks written: "+file) {
				t.Fatalf("code=%d read=%v original=%s got=%s stdout=%s warning=%s", code, err, original, got, out, warning)
			}
		})
	}
}

func TestSetupCodexCompatibleStructures(t *testing.T) {
	// putHook permits null rows, absent/null/false hook lists, nonobject hooks,
	// and absent/null/false commands; keep those compatibility boundaries.
	for _, rows := range []string{
		`null`,
		`{},{"hooks":null},{"hooks":false}`,
		`{"hooks":[null,7,false,[],{}, {"command":null},{"command":false}]}`,
	} {
		t.Run(rows, func(t *testing.T) {
			root, env, ctx := codexSetupFixture(t)
			file := codexFile(root)
			original := `{"hooks":{"UserPromptSubmit":[` + rows + `]}}`
			want := `{"hooks":{"UserPromptSubmit":[` + rows + `,{"hooks":[{"type":"command","command":"soho hook inbound --codex"}]}]}}`
			if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte(original), 0600); err != nil {
				t.Fatal(err)
			}
			code, _, warning := runSetupWrite(nil, ctx, env, root)
			got, err := os.ReadFile(file)
			if code != 0 || err != nil || strings.Contains(warning, "could not merge hooks into "+file) || !reflect.DeepEqual(codexJSONTokens(t, string(got)), codexJSONTokens(t, want)) {
				t.Fatalf("code=%d read=%v got=%s warning=%s", code, err, got, warning)
			}
		})
	}
}

func TestSetupCodexPlanNoHooksAndLocal(t *testing.T) {
	t.Run("plan", func(t *testing.T) {
		root, env, ctx := codexSetupFixture(t)
		code, out, msg := runSetupWrite([]string{"--plan"}, ctx, env, root)
		want := "# would merge into " + root + "/.codex/hooks.json: UserPromptSubmit hook (codex)"
		if code != 0 || !strings.Contains(out, want) {
			t.Fatalf("code=%d out=%s err=%s", code, out, msg)
		}
		if _, e := os.Stat(codexFile(root)); !os.IsNotExist(e) {
			t.Fatalf("plan wrote hooks: %v", e)
		}
	})
	t.Run("no hooks", func(t *testing.T) {
		root, env, ctx := codexSetupFixture(t)
		code, _, msg := runSetupWrite([]string{"--no-hooks"}, ctx, env, root)
		if code != 0 {
			t.Fatalf("code=%d %s", code, msg)
		}
		if _, e := os.Stat(codexFile(root)); !os.IsNotExist(e) {
			t.Fatalf("no-hooks wrote: %v", e)
		}
	})
	t.Run("local", func(t *testing.T) {
		root, env, ctx := codexSetupFixture(t)
		code, _, msg := runSetupWrite([]string{"--local", "--no-hooks"}, ctx, env, root)
		if code != 0 {
			t.Fatalf("baseline %d %s", code, msg)
		}
		exclude := filepath.Join(root, ".git", "info", "exclude")
		before, e := os.ReadFile(exclude)
		if e != nil {
			t.Fatal(e)
		}
		code, _, msg = runSetupWrite([]string{"--local"}, ctx, env, root)
		after, e := os.ReadFile(exclude)
		// DJA-342 also excludes the untracked local Claude hooks.
		want := string(before) + "/.claude/settings.local.json\n/.pi/extensions/soho-inbound.ts\n/.codex/hooks.json\n"
		if code != 0 || e != nil || want != string(after) || codexHookCount(t, codexFile(root)) != 1 {
			t.Fatalf("code=%d exclude=%s err=%v msg=%s", code, after, e, msg)
		}
		if _, e := os.Stat(filepath.Join(root, ".gitignore")); !os.IsNotExist(e) {
			t.Fatalf("local touched .gitignore: %v", e)
		}
	})
}
