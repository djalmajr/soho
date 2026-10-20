package setuptext

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/djalmajr/soho/internal/jsonjs"

	"reflect"

	"strings"
	"testing"
	"unicode/utf16"
)

func ptr(value string) *string { return &value }

// nativeHookShape guards the generated command's portability as a class: it
// must be exactly the three-token PATH command `soho hook <sub>`, with no
// shell engine, no absolute personal path and no shell expansion.
func nativeHookShape(t *testing.T, event, command, sub string) {
	t.Helper()
	fields := strings.Fields(command)
	if len(fields) != 3 || fields[0] != "soho" || fields[1] != "hook" || fields[2] != sub {
		t.Fatalf("%s hook %q is not the native 'soho hook %s' PATH command", event, command, sub)
	}
	for _, hostile := range []string{"sh -c", "bash -c", "node", "bun", "/Users/", `C:\\Users`, "/home/", "$HOME", "${", "scripts/"} {
		if strings.Contains(command, hostile) {
			t.Fatalf("%s hook %q carries a shell engine or personal path: %q", event, command, hostile)
		}
	}
}
func TestSetupTextCases(t *testing.T) {
	t.Run(`// JS: "setupBlock: markers, heading, no absolute path (test-setup.sh rule)"`, func(t *testing.T) {
		block := SetupBlock()
		if !strings.HasPrefix(block, SetupStart+"\n## Multi-agent workflow (soho)\n") || !strings.HasSuffix(block, SetupEnd+"\n") || strings.Contains(block, "/Users/") || strings.Contains(block, `C:\\Users\\`) {
			t.Fatal("setup block markers or portability changed")
		}
	})
	t.Run("setupHookReminder: exact portable PATH command, no trailing newline", func(t *testing.T) { // JS: "setupHookReminder: exact text, no trailing newline"
		if SetupHookReminder() != "soho hook reminder" || strings.Contains(SetupHookReminder(), "\n") {
			t.Fatalf("reminder literal changed: %q", SetupHookReminder())
		}
		nativeHookShape(t, "UserPromptSubmit", SetupHookReminder(), "reminder")
	})
	t.Run("setupHookDoctor: exact portable PATH command, no trailing newline", func(t *testing.T) { // JS: "setupHookDoctor: exact text, no trailing newline"
		if SetupHookDoctor() != "soho hook doctor" || strings.Contains(SetupHookDoctor(), "\n") {
			t.Fatalf("doctor hook literal changed: %q", SetupHookDoctor())
		}
		nativeHookShape(t, "SessionStart", SetupHookDoctor(), "doctor")
	})
	t.Run("previous herdr-soho shell hooks are the exact pre-native commands, kept inert", func(t *testing.T) {
		// Pinned in the test, not derived from the implementation: if the
		// recognized previous commands drift from the exact pre-native text,
		// a settings file written by the old setup stops being recognized and
		// the merge stacks.
		wantPreviousReminder := `sh -c '[ "${HERDR_ENV:-}" = 1 ] && echo "herdr-soho: this project routes non-trivial work through /herdr-soho — surveys go to a scouter, slices to workers; the orchestrator keeps only one-or-two-file changes."; true'`
		wantPreviousDoctor := `sh -c '[ "${HERDR_ENV:-}" = 1 ] || exit 0; for script in "${CLAUDE_PROJECT_DIR:-$PWD}/.agents/skills/herdr-soho/scripts/herdr-soho" "${CLAUDE_PROJECT_DIR:-$PWD}/.claude/skills/herdr-soho/scripts/herdr-soho" "$HOME/.agents/skills/herdr-soho/scripts/herdr-soho" "$HOME/.claude/skills/herdr-soho/scripts/herdr-soho"; do [ -f "$script" ] || continue; sh "$script" doctor 2>/dev/null | grep -E "^warn" | sed "s/^warn */herdr-soho doctor: /"; exit 0; done; echo "herdr-soho doctor: skill script not found"; true'`
		if PreviousHookReminder() != wantPreviousReminder {
			t.Fatalf("previous reminder drifted: %q", PreviousHookReminder())
		}
		if PreviousHookDoctor() != wantPreviousDoctor {
			t.Fatalf("previous doctor drifted: %q", PreviousHookDoctor())
		}
	})
	t.Run("the generated hooks replace the sh execution without any shell availability skip", func(t *testing.T) { // replaces "setupHookDoctor runs the first available launcher from the four candidates"
		// The obsolete shell execution of the four skill script candidates is
		// gone: the generated command is the native binary on PATH, so no sh
		// lookup, fixture launcher or platform skip is needed or allowed.
		for _, command := range []string{SetupHookReminder(), SetupHookDoctor()} {
			if strings.Contains(command, "sh") || strings.Contains(command, "bash") || strings.Contains(command, "node") || strings.Contains(command, "bun") || strings.Contains(command, "/Users/") || strings.Contains(command, `C:\\`) || strings.Contains(command, "$HOME") || strings.Contains(command, "${") {
				t.Fatalf("generated hook %q is not a plain PATH command without an engine or personal path", command)
			}
		}
		// The exact previous herdr-soho hook is merged away like the current
		// one: the merged file keeps the foreign command in order, carries
		// exactly the native command, and a second merge is a no-op.
		seed, err := json.Marshal(map[string]any{"hooks": map[string]any{
			"UserPromptSubmit": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": PreviousHookReminder()}}}, map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "echo my own reminder"}}}},
			"SessionStart":     []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": PreviousHookDoctor()}}}, map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "bash something-else.sh"}}}},
		}})
		if err != nil {
			t.Fatal(err)
		}
		first := SettingsHooksResult(ptr(string(seed)))
		if first == nil {
			t.Fatal("merge refused the previous herdr-soho hooks")
		}
		second := SettingsHooksResult(first)
		if second == nil || *second != *first {
			t.Fatalf("rerun not stable: %v / %v", first, second)
		}
		var doc struct {
			Hooks map[string][]struct {
				Hooks []struct {
					Command string `json:"command"`
				} `json:"hooks"`
			} `json:"hooks"`
		}
		if err := json.Unmarshal([]byte(*first), &doc); err != nil {
			t.Fatal(err)
		}
		for _, event := range []struct {
			name    string
			entries []struct {
				Hooks []struct {
					Command string `json:"command"`
				} `json:"hooks"`
			}
			foreign, native string
		}{{"UserPromptSubmit", doc.Hooks["UserPromptSubmit"], "echo my own reminder", "soho hook reminder"}, {"SessionStart", doc.Hooks["SessionStart"], "bash something-else.sh", "soho hook doctor"}} {
			if len(event.entries) != 2 {
				t.Fatalf("%s entries=%d, want the foreign command plus the native one: %s", event.name, len(event.entries), *first)
			}
			var got []string
			for _, entry := range event.entries {
				if len(entry.Hooks) != 1 {
					t.Fatalf("%s entry holds %d commands, want 1: %s", event.name, len(entry.Hooks), *first)
				}
				got = append(got, entry.Hooks[0].Command)
			}
			if got[0] != event.foreign || got[1] != event.native {
				t.Fatalf("%s order=%v, want foreign then native: %s", event.name, got, *first)
			}
			if strings.Contains(*first, "sh -c ") && !strings.Contains(*first, `\"bash something-else.sh\"`) {
				t.Fatalf("previous shell hook survived the merge: %s", *first)
			}
		}
	})
	t.Run("setupBlockResult: absent and empty files", func(t *testing.T) { // JS: "setupBlockResult: absent and empty files"
		if got := SetupBlockResult(nil); got == nil || *got != SetupBlock() {
			t.Fatal("absent block mismatch")
		}
		if got := SetupBlockResult(ptr("")); got == nil || *got != "\n"+SetupBlock() {
			t.Fatal("empty block mismatch")
		}
	})
	t.Run("setupBlockResult: append keeps one blank line before the block", func(t *testing.T) { // JS: "setupBlockResult: append keeps one blank line before the block"
		for _, s := range []string{"hello", "hello\n"} {
			if got := SetupBlockResult(ptr(s)); got == nil || *got != "hello\n\n"+SetupBlock() {
				t.Errorf("append %q mismatch", s)
			}
		}
	})
	t.Run("setupBlockResult: replaces the range between the markers (bash awk semantics)", func(t *testing.T) { // JS: "setupBlockResult: replaces the range between the markers (bash awk semantics)"
		cases := []struct{ in, want string }{{"before\n" + SetupStart + "\nold\n" + SetupEnd + "\nafter\n", "before\n" + SetupBlock() + "after\n"}, {"before\n" + SetupStart + "\nold\n" + SetupEnd, "before\n" + SetupBlock()}, {SetupStart + "\nx\n" + SetupEnd + "\n", SetupBlock()}}
		for _, c := range cases {
			if got := SetupBlockResult(ptr(c.in)); got == nil || *got != c.want {
				t.Errorf("replace mismatch: %v", got)
			}
		}
	})
	t.Run("setupBlockResult: no end marker truncates everything after the start line (bash behavior)", func(t *testing.T) { // JS: "setupBlockResult: no end marker truncates everything after the start line (bash behavior)"
		if got := SetupBlockResult(ptr("before\n" + SetupStart + "\nold stuff\n")); got == nil || *got != "before\n"+SetupBlock() {
			t.Fatalf("result=%v", got)
		}
	})
	t.Run("setupBlockResult: end before start is consumed; a second start repeats the block; a line holding both counts as start", func(t *testing.T) { // JS: "setupBlockResult: end before start is consumed; a second start repeats the block; a line holding both counts as start"
		cases := []struct{ in, want string }{{"x\n" + SetupEnd + "\n" + SetupStart + "\ny\n" + SetupEnd + "\n", "x\n" + SetupBlock()}, {"a\n" + SetupStart + "\n1\n" + SetupStart + "\n2\n" + SetupEnd + "\n", "a\n" + SetupBlock() + SetupBlock()}, {"a\n" + SetupStart + SetupEnd + "\nb\n", "a\n" + SetupBlock()}}
		for _, c := range cases {
			if got := SetupBlockResult(ptr(c.in)); got == nil || *got != c.want {
				t.Errorf("markers result=%v, want %q", got, c.want)
			}
		}
	})
	t.Run("setupBlockResult: legacy block is renamed in place and keeps its own text", func(t *testing.T) { // JS: "setupBlockResult: a legacy block is renamed in place and keeps its own text"
		// DJA-318: the recognized legacy generation is the herdr-soho
		// block; it migrates in place and keeps the lines the project
		// added, exactly like the herdr-agents rename did.
		in := "before herdr-soho\n" + LegacySetupStart + "\n## workflow herdr-soho\nHERDR_SOHO_DIR\ncustom\n" + LegacySetupEnd + "\nafter\n"
		want := "before herdr-soho\n" + SetupStart + "\n## workflow soho\nSOHO_DIR\ncustom\n" + SetupEnd + "\nafter\n"
		if got := SetupBlockResult(ptr(in)); got == nil || *got != want {
			t.Fatalf("legacy rename result=%v", got)
		}
		if got := SetupBlockResult(ptr(LegacySetupStart + "\nno end\n")); got != nil {
			t.Fatalf("unterminated legacy=%v", got)
		}
		both := SetupStart + "\nx\n" + SetupEnd + "\n" + LegacySetupStart + "\ny\n" + LegacySetupEnd + "\n"
		if got := SetupBlockResult(ptr(both)); got == nil || *got != SetupBlock()+SetupBlock() {
			t.Fatalf("both marker kinds result=%v", got)
		}
	})
	t.Run("setupBlockResult: a file without the legacy markers is byte-identical to today", func(t *testing.T) { // JS: "setupBlockResult: a file without the legacy markers is byte-identical to today"
		plain := "this project used to run herdr-agents panes\n"
		if got := SetupBlockResult(ptr(plain)); got == nil || *got != plain+"\n"+SetupBlock() {
			t.Fatalf("plain file result=%v", got)
		}
		if got := SetupBlockResult(ptr("x\n" + SetupStart + "\ny\n" + SetupEnd + "\n")); got == nil || *got != "x\n"+SetupBlock() {
			t.Fatalf("current markers result=%v", got)
		}
	})
	t.Run("legacy hooks: pinned sha256 and length of the exact pre-rename commands", func(t *testing.T) { // JS: "legacy hooks: pinned sha256 and length of the exact pre-rename commands"
		// DJA-318: the recognized legacy generation is the pre-rename
		// herdr-soho native PATH command (the herdr-agents generation is
		// retired), so the pins are its exact literals.
		cases := []struct {
			s    string
			n    int
			hash string
		}{{LegacyHookReminder(), 24, "2ef437205b8c0b2991f9e747dece3c976f25f3c5670fb903a391ab0475bd4db3"}, {LegacyHookDoctor(), 22, "04dc429f905559b7aed68d46e632e7dbea4053cafaccebb12fa34409098e5e61"}}
		for _, c := range cases {
			sum := sha256.Sum256([]byte(c.s))
			if len(utf16.Encode([]rune(c.s))) != c.n || hex.EncodeToString(sum[:]) != c.hash {
				t.Errorf("legacy hook hash/length mismatch: %d %x", len(utf16.Encode([]rune(c.s))), sum)
			}
		}
	})
	t.Run("settingsHooksResult: absent file and {} gain both hooks, nothing else", func(t *testing.T) { // JS: "settingsHooksResult: absent file and {} gain both hooks, nothing else"
		a, b := SettingsHooksResult(nil), SettingsHooksResult(ptr("{}"))
		if a == nil || b == nil || *a != *b {
			t.Fatalf("absent={} mismatch: %v %v", a, b)
		}
	})
	t.Run("settingsHooksResult: null hooks and false event entries are replaced", func(t *testing.T) {
		for _, seed := range []string{`{"hooks":null}`, `{"hooks":{"UserPromptSubmit":false,"SessionStart":false}}`} {
			result := SettingsHooksResult(ptr(seed))
			if result == nil {
				t.Fatalf("SettingsHooksResult(%s) = nil", seed)
			}
			var document struct {
				Hooks map[string]json.RawMessage `json:"hooks"`
			}
			if err := json.Unmarshal([]byte(*result), &document); err != nil {
				t.Fatal(err)
			}
			for _, event := range []string{"UserPromptSubmit", "SessionStart"} {
				var entries []json.RawMessage
				if err := json.Unmarshal(document.Hooks[event], &entries); err != nil {
					t.Fatalf("%s: %v", event, err)
				}
				if len(entries) != 1 {
					t.Fatalf("%s entries = %d, want generated hook", event, len(entries))
				}
			}
		}
	})
	t.Run("settingsHooksResult: other hooks and keys stay, same order; new events appended", func(t *testing.T) { // JS: "settingsHooksResult: other hooks and keys stay, same order; new events appended"
		seed := `{"other":{"x":1},"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"echo keep-me"}]}],"SessionStart":[{"hooks":[{"type":"command","command":"bash something-else.sh"}]}]}}`
		got := SettingsHooksResult(ptr(seed))
		if got == nil {
			t.Fatal("merge refused valid settings")
		}
		parsed, err := jsonjs.Parse([]byte(*got))
		if err != nil {
			t.Fatal(err)
		}
		doc := parsed.(*jsonjs.Object)
		if !reflect.DeepEqual(doc.Keys(), []string{"other", "hooks"}) {
			t.Fatalf("top-level key order=%#v", doc.Keys())
		}
		if !strings.Contains(*got, `"other"`) || !strings.Contains(*got, `"PreToolUse"`) || !strings.Contains(*got, "echo keep-me") || !strings.HasSuffix(*got, "}\n") {
			t.Fatalf("preserved settings=%s", *got)
		}
	})
	t.Run("settingsHooksResult: current hooks are replaced and other commands are preserved", func(t *testing.T) { // JS: "settingsHooksResult: current hooks are replaced and other commands are preserved"
		seedObj, err := json.Marshal(map[string]any{"hooks": map[string]any{
			"UserPromptSubmit": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "sh -c herdr-soho setup"}}}, map[string]any{"hooks": []any{map[string]any{"type": "command", "command": SetupHookReminder()}}}},
			"SessionStart":     []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "sh -c herdr-soho doctor"}}}, map[string]any{"hooks": []any{map[string]any{"type": "command", "command": SetupHookDoctor()}}}},
		}})
		if err != nil {
			t.Fatal(err)
		}
		first := SettingsHooksResult(ptr(string(seedObj)))
		if first == nil {
			t.Fatal("merge refused current hooks")
		}
		second := SettingsHooksResult(first)
		if second == nil || *second != *first {
			t.Fatalf("rerun not stable: %v / %v", first, second)
		}
		var doc map[string]any
		if err := json.Unmarshal([]byte(*first), &doc); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(*first, "sh -c herdr-soho setup") || !strings.Contains(*first, `"soho hook reminder"`) || !strings.Contains(*first, `"soho hook doctor"`) {
			t.Fatalf("unexpected merged commands: %s", *first)
		}
	})
	t.Run("settingsHooksResult: the exact legacy commands are removed like the current ones", func(t *testing.T) { // JS: "settingsHooksResult: the exact legacy commands are removed like the current ones"
		seed, err := json.Marshal(map[string]any{"hooks": map[string]any{"UserPromptSubmit": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": LegacyHookReminder()}}}, map[string]any{"hooks": []any{map[string]any{"type": "command", "command": SetupHookReminder()}}}}, "SessionStart": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": LegacyHookDoctor()}}}, map[string]any{"hooks": []any{map[string]any{"type": "command", "command": SetupHookDoctor()}}}}}})
		if err != nil {
			t.Fatal(err)
		}
		first := SettingsHooksResult(ptr(string(seed)))
		if first == nil {
			t.Fatal("legacy hooks merge refused")
		}
		second := SettingsHooksResult(first)
		if second == nil || *second != *first {
			t.Fatalf("legacy hooks stacked: %v / %v", first, second)
		}
		var doc struct {
			Hooks map[string][]any `json:"hooks"`
		}
		if err := json.Unmarshal([]byte(*first), &doc); err != nil {
			t.Fatal(err)
		}
		if len(doc.Hooks["UserPromptSubmit"]) != 1 || len(doc.Hooks["SessionStart"]) != 1 {
			t.Fatalf("old entries kept: %#v", doc.Hooks)
		}
	})
	t.Run("settingsHooksResult: a user command that only contains herdr-agents is kept", func(t *testing.T) { // JS: "settingsHooksResult: a user command that only contains herdr-agents is kept"
		seed, err := json.Marshal(map[string]any{"hooks": map[string]any{"UserPromptSubmit": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "sh -c 'my-herdr-agents-thing run'"}}}}, "SessionStart": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "echo herdr-agents legacy cleanup"}}}}}})
		if err != nil {
			t.Fatal(err)
		}
		got := SettingsHooksResult(ptr(string(seed)))
		if got == nil || !strings.Contains(*got, "my-herdr-agents-thing run") || !strings.Contains(*got, "echo herdr-agents legacy cleanup") || !strings.Contains(*got, `"soho hook reminder"`) || !strings.Contains(*got, `"soho hook doctor"`) {
			t.Fatalf("user commands not preserved: %v", got)
		}
	})
	t.Run("settingsHooksResult: invalid or wrong-shaped documents are refused (null → die 4)", func(t *testing.T) { // JS: "settingsHooksResult: invalid or wrong-shaped documents are refused (null → die 4)"
		for _, bad := range []string{"NOT-JSON", "42", "[1, 2]", `{"hooks": [1]}`, `{"hooks": {"UserPromptSubmit": {}}}`, `{"hooks": {"UserPromptSubmit": [{"hooks": [{"type": "command", "command": 123}]}]}}`} {
			if got := SettingsHooksResult(ptr(bad)); got != nil {
				t.Errorf("accepted %s", bad)
			}
		}
	})
}
