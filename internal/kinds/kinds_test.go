package kinds

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestMain(m *testing.M) { fakecli.RunTests(m) }
func fakeEnv(t *testing.T, rules map[string][]fakecli.Rule) (platform.Env, string) {
	t.Helper()
	bin := t.TempDir()
	for name, items := range rules {
		if _, err := fakecli.Install(t, bin, name, items); err != nil {
			t.Fatal(err)
		}
	}
	env := platform.Env{"PATH": bin, "HOME": t.TempDir(), "USERPROFILE": t.TempDir(), "TMPDIR": t.TempDir(), "SOHO_FAKECLI_CONFIG": bin}
	return env, bin
}
func TestKindRules(t *testing.T) {
	t.Run("effort ceilings and clamp_to", func(t *testing.T) { // JS: "kind effort ceilings and clamp_to"
		for kind, want := range map[string]string{"claude": "max", "pi": "max", "codex": "max", "cursor": "xhigh", "grok": "xhigh", "agy": "high", "gemini": "high", "opencode": ""} {
			if got := KindEffortCeiling(kind); got != want {
				t.Errorf("%s ceiling %q want %q", kind, got, want)
			}
		}
		if got := ClampTo("max", "xhigh"); got != "xhigh" {
			t.Fatalf("clamp=%s", got)
		}
		if got := ClampTo("low", ""); got != "low" {
			t.Fatalf("unknown clamp=%s", got)
		}
	})
	t.Run("native flags: grok, codex, claude, pi, opencode", func(t *testing.T) { // JS: "native flags: grok, codex, claude, pi, opencode"
		cases := map[string][]string{"claude": {"--effort", "high"}, "codex": {"-c", `model_reasoning_effort="high"`}, "grok": {"--reasoning-effort", "high"}, "pi": {"--thinking", "high"}}
		for kind, want := range cases {
			if got := KindEffortArgs(kind, "high", "m", nil, func(string) {}); !reflect.DeepEqual(got, want) {
				t.Errorf("%s args %v", kind, got)
			}
		}
		for kind, want := range map[string][]string{"claude": {"--model", "m"}, "codex": {"-m", "m"}, "opencode": {"-m", "m"}} {
			if got := KindModelArgs(kind, "m", "", nil); !reflect.DeepEqual(got, want) {
				t.Errorf("%s args=%v", kind, got)
			}
		}
	})
	t.Run("agy/gemini: --effort only on empty or gemini ids; suffixed ids carry it", func(t *testing.T) { // JS: "agy/gemini: --effort only on empty or gemini* ids; suffixed ids carry it"
		warn := func(string) {}
		if got := KindEffortArgs("agy", "high", "claude-opus", nil, warn); len(got) != 0 {
			t.Fatal(got)
		}
		if got := KindEffortArgs("gemini", "low", "gemini-3-low", nil, warn); len(got) != 0 {
			t.Fatal(got)
		}
		if got := KindEffortArgs("agy", "low", "", nil, warn); !reflect.DeepEqual(got, []string{"--effort", "low"}) {
			t.Fatal(got)
		}
	})
	t.Run("cursor effort rides in the model id and --model is never doubled", func(t *testing.T) { // JS: "cursor: effort rides in the model id and --model is never doubled"
		env, _ := fakeEnv(t, map[string][]fakecli.Rule{"cursor-agent": {{Argv: []string{"--list-models"}, Stdout: "grok-4-high - Grok\n"}}})
		if got := KindEffortArgs("cursor", "high", "grok-4", env, func(string) {}); !reflect.DeepEqual(got, []string{"--model", "grok-4-high"}) {
			t.Fatal(got)
		}
		if got := KindModelArgs("cursor", "grok-4-high", "high", nil); len(got) != 0 {
			t.Fatal(got)
		}
	})
	t.Run("approval args warn for pi and opencode edits and reject uppercase FULL", func(t *testing.T) { // JS: "approval args per kind/mode (pi and opencode warn, invalid dies 2)"
		got, err := KindApprovalArgs("codex", "full", nil)
		if err != nil || !reflect.DeepEqual(got, []string{"-s", "workspace-write", "-a", "never"}) {
			t.Fatalf("%v %v", got, err)
		}
		for kind, wantWarning := range map[string]string{
			"pi":       "pi has no approval prompts (its tools run as-is); approvals=edits is a no-op (restrict tools with --tools/--exclude-tools after --)",
			"opencode": "opencode has no edits approvals flag; use approvals=full (--auto) or per-tool permissions in opencode.json",
		} {
			var warnings []string
			got, err := KindApprovalArgs(kind, "edits", func(msg string) { warnings = append(warnings, msg) })
			if err != nil || len(got) != 0 {
				t.Fatalf("%s args=%v err=%v", kind, got, err)
			}
			if !reflect.DeepEqual(warnings, []string{wantWarning}) {
				t.Errorf("%s warnings=%q want %q", kind, warnings, wantWarning)
			}
		}
		if _, err = KindApprovalArgs("pi", "FULL", nil); err == nil || err.Error() != "invalid approvals 'FULL' (ask|edits|full)" {
			t.Fatalf("uppercase FULL error=%v", err)
		}
	})
	t.Run("context args (worker_context=lean) per kind", func(t *testing.T) { // JS: "context args (worker_context=lean) per kind"
		if got := KindContextArgs("claude", "lean"); !reflect.DeepEqual(got, []string{"--disable-slash-commands"}) {
			t.Fatal(got)
		}
		if got := KindContextArgs("codex", "lean"); !reflect.DeepEqual(got, []string{"-c", "project_doc_max_bytes=0"}) {
			t.Fatal(got)
		}
	})
	t.Run("kindExe, family display and summaries", func(t *testing.T) { // JS: "kindExe, family display and summaries"
		if KindExe("cursor") != "cursor-agent" || KindFamilyDisplay("opencode") != "by model" || KindSummary("pi") == "" {
			t.Fatal("kind mapping incomplete")
		}
	})
	t.Run("KNOWN_KINDS exact set", func(t *testing.T) { // JS: "KNOWN_KINDS exact set and kind config values (copilot rejected)"
		if !reflect.DeepEqual(KnownKinds, []string{"claude", "codex", "grok", "agy", "gemini", "cursor", "pi", "opencode"}) {
			t.Fatal(KnownKinds)
		}
		env := platform.Env{}
		if core.ConfigValueOk("lane.build.kind", "copilot", env, t.TempDir()) {
			t.Fatal("copilot accepted as a configured kind")
		}
		if !core.ConfigValueOk("lane.build.kind", "codex", env, t.TempDir()) {
			t.Fatal("known kind codex rejected")
		}
	})
	t.Run("agentFamily segment and model rules", func(t *testing.T) { // JS: "agentFamily: the NEW rule (decision cases) + fixed-family kinds"
		for model, want := range map[string]string{"openrouter/anthropic/claude-x": "anthropic", "proxy/gpt-5": "openai", "xai-proxy/x": "unknown", "grok-4": "xai", "applianceai01/qwen3.8-27b": "alibaba", "qwen3-coder": "alibaba", "provider/QWEN-x": "alibaba", "provider/qwenish/llama-3": "unknown"} {
			if got := AgentFamily("pi", model); got != want {
				t.Errorf("%s=%s", model, got)
			}
		}
	})
	t.Run("agentFamily exact test-kinds.sh inputs", func(t *testing.T) { // JS: "agentFamily: the exact test-kinds.sh inputs (ported)"
		if AgentFamily("codex", "claude-x") != "openai" || AgentFamily("opencode", "vendor/google/gemini-2") != "google" {
			t.Fatal("family mismatch")
		}
	})
	t.Run("regex selects newest matching model", func(t *testing.T) { // JS: "resolveModel: exact id, alias, regex and a|b alternates"
		env, _ := fakeEnv(t, map[string][]fakecli.Rule{"grok": {{Argv: []string{"models"}, Stdout: "grok-4.6 - older\ngrok-4.7 - newer\n"}}})
		got, err := ResolveModel("grok", "grok-4", "", env, func(string) {})
		if err != nil || got != "grok-4.7" {
			t.Fatalf("%q %v", got, err)
		}
	})
	t.Run("dotted spec swaps dots for dashes and picks the newest candidate", func(t *testing.T) {
		seed := func(ids ...string) platform.Env {
			tmp := t.TempDir()
			if err := os.WriteFile(filepath.Join(tmp, "soho-models-claude.txt"), []byte(strings.Join(ids, "\n")+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return platform.Env{"TMPDIR": tmp}
		}
		got, err := ResolveModel("claude", "sonnet-5.5", "", seed("claude-opus-5-5", "claude-sonnet-5-5", "claude-sonnet-4-6"), func(string) {})
		if err != nil || got != "claude-sonnet-5-5" {
			t.Fatalf("got %q err %v, want claude-sonnet-5-5", got, err)
		}
		var warnings []string
		got, err = ResolveModel("claude", "sonnet-5.5", "", seed("claude-opus-5-5", "claude-sonnet-4-6"), func(w string) { warnings = append(warnings, w) })
		if err != nil || got != "sonnet-5.5" || len(warnings) != 1 || !strings.Contains(warnings[0], "no claude model matches 'sonnet-5.5'; passing it through unchanged") {
			t.Fatalf("no candidate: got %q warns %v err %v", got, warnings, err)
		}
		got, err = ResolveModel("claude", "sonnet-5.5", "", seed("sonnet-5.5", "claude-sonnet-5-5"), func(string) {})
		if err != nil || got != "sonnet-5.5" {
			t.Fatalf("exact id: got %q err %v, want sonnet-5.5", got, err)
		}
		got, err = ResolveModel("claude", "sonnet-5.5", "", seed("claude-sonnet-5-5", "x-sonnet-5.5.10"), func(string) {})
		if err != nil || got != "claude-sonnet-5-5" {
			t.Fatalf("candidates restricted to the swapped spec: got %q err %v, want claude-sonnet-5-5", got, err)
		}
	})
	t.Run("claude with an empty list maps dotted alias specs to claude ids", func(t *testing.T) {
		env := platform.Env{"TMPDIR": t.TempDir()}
		for spec, want := range map[string]string{"sonnet-5.5": "claude-sonnet-5-5", "Opus-5.5": "claude-opus-5-5", "fable-5.1": "claude-fable-5-1", "sonnet": "sonnet", "claude-sonnet-5-5": "claude-sonnet-5-5", "gpt-5.5": "gpt-5.5"} {
			got, err := ResolveModel("claude", spec, "", env, func(string) {})
			if err != nil || got != want {
				t.Errorf("claude %s: got %q err %v, want %q", spec, got, err, want)
			}
		}
		if got, err := ResolveModel("pi", "sonnet-5.5", "", platform.Env{"TMPDIR": t.TempDir()}, func(string) {}); err != nil || got != "sonnet-5.5" {
			t.Fatalf("pi sonnet-5.5: got %q err %v, want sonnet-5.5", got, err)
		}
	})
	t.Run("regex dialect rejects unsupported constructs and handles classes", func(t *testing.T) { // JS: "model specs: defined regex dialect"
		if _, ok := ERERegExp("grok-(?=5)"); ok {
			t.Fatal("lookahead accepted")
		}
		re, ok := ERERegExp("GROK-[[:digit:]]")
		if !ok || !re.MatchString("grok-4") {
			t.Fatal("POSIX/ascii case folding failed")
		}
	})
	t.Run("POSIX classes and letter ranges match the JavaScript i-flag predicate", func(t *testing.T) { // Mutation captured: folding letters individually inside [] changes ranges and expands punctuation classes.
		cases := []struct {
			pattern string
			matches []string
			rejects []string
		}{
			{"a[[:punct:]]b", []string{"a.b", "a_b", "a[b"}, []string{"a5b", "a b"}},
			{"a[[:alpha:]]b", []string{"axb", "aXb"}, []string{"a_b", "a5b"}},
			{"a\\wb", []string{"a_b", "a5b"}, []string{"a[b"}},
			{"a[a-z]b", []string{"aBb", "azb"}, []string{"a_b", "a[b"}},
			{"a[A-Z]b", []string{"aBb", "azb"}, []string{"a_b", "a[b"}},
		}
		for _, tc := range cases {
			re, ok := ERERegExp(tc.pattern)
			if !ok {
				t.Fatalf("ERERegExp(%q) rejected", tc.pattern)
			}
			for _, value := range tc.matches {
				if !re.MatchString(value) {
					t.Errorf("ERERegExp(%q) did not match JS-positive %q", tc.pattern, value)
				}
			}
			for _, value := range tc.rejects {
				if re.MatchString(value) {
					t.Errorf("ERERegExp(%q) matched JS-negative %q", tc.pattern, value)
				}
			}
		}
	})
	t.Run("aliases and alternates resolve in order", func(t *testing.T) { // JS: "resolveModel: exact id, alias, regex and a|b alternates"
		env, _ := fakeEnv(t, map[string][]fakecli.Rule{"agy": {{Argv: []string{"models"}, Stdout: "gemini-3.8-flash  Flash\nclaude-opus-4-6  Opus\n"}}})
		got, err := ResolveModel("agy", "opus|gemini", "", env, func(string) {})
		if err != nil || got != "claude-opus-4-6" {
			t.Fatalf("%q %v", got, err)
		}
	})
	t.Run("pi and opencode model family comes from model id", func(t *testing.T) { // JS: "model generic kinds (pi, opencode — no list, by-model family)"
		if AgentFamily("pi", "openai/gpt-5") != "openai" || AgentFamily("opencode", "anthropic/claude-x") != "anthropic" {
			t.Fatal("generic family mapping")
		}
	})
}

func TestModelRules(t *testing.T) {
	t.Run("list parsers strip SGR and parse CLI shapes", func(t *testing.T) { // JS: "list parsers mirror the bash awk/grep pipelines"
		if got := ParseCursorModels("\x1b[36mgrok-4-high\x1b[0m - Grok\n"); !reflect.DeepEqual(got, []string{"grok-4-high"}) {
			t.Fatal(got)
		}
		if got := ParseAgyModels("gemini-3.8-flash  Google\none-word\n"); !reflect.DeepEqual(got, []string{"gemini-3.8-flash"}) {
			t.Fatal(got)
		}
		if got := ParseGrokModels("grok-4.7 grok-4.6 grok-4.7"); !reflect.DeepEqual(got, []string{"grok-4.6", "grok-4.7"}) {
			t.Fatal(got)
		}
	})
	t.Run("version sort is numeric and newest first", func(t *testing.T) { // JS: "versionSortDesc: newest first, numeric fields, lexicographic tie-break"
		got := VersionSortDesc([]string{"grok-4.6", "grok-4.7", "model-10", "model-9"})
		want := []string{"model-10", "model-9", "grok-4.7", "grok-4.6"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%v", got)
		}
	})
	t.Run("Codex effort ceiling uses highest advertised level", func(t *testing.T) { // JS: "codexModelCeiling comes from the cached model"
		home := t.TempDir()
		dir := filepath.Join(home, ".codex")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "models_cache.json"), []byte(`{"models":[{"slug":"gpt-5","supported_reasoning_levels":[{"effort":"low"},{"effort":"max"}]}]}`), 0600); err != nil {
			t.Fatal(err)
		}
		env := platform.Env{"HOME": home, "USERPROFILE": home, "TMPDIR": t.TempDir()}
		if got := CodexModelCeiling("gpt-5", env); got != "max" {
			t.Fatal(got)
		}
		if got := CodexEffortCeiling("missing", env); got != "xhigh" {
			t.Fatal(got)
		}
	})
	t.Run("Cursor effort suffix is preserved", func(t *testing.T) { // JS: "cursor: effort rides in the model id and --model is never doubled"
		var warns []string
		got := CursorModelWithEffort("grok-4-high", "low", nil, func(msg string) { warns = append(warns, msg) })
		if got != "grok-4-high" {
			t.Fatal(got)
		}
		if len(warns) != 1 || warns[0] != "cursor model 'grok-4-high' already encodes effort 'high'; --effort low ignored" {
			t.Fatalf("warnings = %v", warns)
		}
	})
	t.Run("Cursor suffix resolution excludes fast and caps effort rank", func(t *testing.T) { // JS: "resolveModel cursor/agy: effort suffix, rank ceiling, -fast exclusion"
		env, _ := fakeEnv(t, map[string][]fakecli.Rule{"cursor-agent": {{Argv: []string{"--list-models"}, Stdout: "grok-4-max - maximum\ngrok-4-high - high\n"}}})
		got, err := ResolveModel("cursor", "grok", "xhigh", env, func(string) {})
		if err != nil || got != "grok-4-high" {
			t.Fatalf("%q %v", got, err)
		}
	})
	t.Run("model ids read Codex cache and unknown kinds empty", func(t *testing.T) { // JS: "modelIds per kind via fake CLIs (grok, cursor, agy, codex, pi)"
		home := t.TempDir()
		_ = os.MkdirAll(filepath.Join(home, ".codex"), 0700)
		_ = os.WriteFile(filepath.Join(home, ".codex", "models_cache.json"), []byte(`{"models":[{"slug":"gpt-5"},{"slug":"gpt-5.1"}]}`), 0600)
		env := platform.Env{"HOME": home, "USERPROFILE": home, "TMPDIR": t.TempDir()}
		if got := ModelIDs("codex", env); !reflect.DeepEqual(got, []string{"gpt-5", "gpt-5.1"}) {
			t.Fatal(got)
		}
		if got := ModelIDs("pi", env); len(got) != 0 {
			t.Fatal(got)
		}
	})
	t.Run("cursor missing matches returns error", func(t *testing.T) { // JS: "model cursor with no match dies 2 (strict CLI)"
		env, _ := fakeEnv(t, map[string][]fakecli.Rule{"cursor-agent": {{Argv: []string{"--list-models"}, Stdout: "grok-4 - Grok\n"}}})
		_, err := ResolveModel("cursor", "missing", "", env, func(string) {})
		if err == nil {
			t.Fatal("expected strict cursor error")
		}
	})
}

func TestTM11KindAndModelCases(t *testing.T) {
	t.Run(`JS: "cursorModelWithEffort consults --list-models (fake cursor-agent)"`, func(t *testing.T) {
		env, _ := fakeEnv(t, map[string][]fakecli.Rule{"cursor-agent": {{Argv: []string{"--list-models"}, Stdout: "grok-4.7-max - Grok max\ngrok-4.7-high - Grok high\ngrok-4.7 - Grok default\n"}}})
		var warnings []string
		warn := func(value string) { warnings = append(warnings, value) }
		for effort, want := range map[string]string{"max": "grok-4.7-max", "high": "grok-4.7-high", "low": "grok-4.7"} {
			if got := CursorModelWithEffort("grok-4.7", effort, env, warn); got != want {
				t.Errorf("effort %s: got %q want %q", effort, got, want)
			}
		}
		if len(warnings) != 1 || !strings.Contains(warnings[0], "using 'grok-4.7'") {
			t.Fatalf("fallback warnings=%q", warnings)
		}
	})
	t.Run(`JS: "shipped defaults (config.defaults and role frontmatter)"`, func(t *testing.T) {
		defaults, err := os.ReadFile(filepath.Join("..", "..", "skills", "soho", "config.defaults"))
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"model.grok.worker=grok"} {
			if !strings.Contains(string(defaults), key) {
				t.Errorf("defaults missing %q", key)
			}
		}
		if strings.Contains(string(defaults), "model_pi_") || strings.Contains(string(defaults), "model_opencode_") {
			t.Fatal("generic kinds must not ship model defaults")
		}
		for role, fields := range map[string][]string{"implementer": {"kind: grok"}, "critical-implementer": {"kind: codex", "mcp: none", "alternatives: [claude]", "effort: medium", "mode: edit"}, "reviewer": {"kind: codex", "alternatives: [claude]"}, "security-reviewer": {"kind: claude"}, "tasker": {"kind: grok"}, "tester": {"kind: grok", "mcp: none", "mode: edit"}, "validator": {"kind: codex", "alternatives: [claude]", "mcp: none", "mode: read-only"}} {
			data, readErr := os.ReadFile(filepath.Join("..", "..", "skills", "soho", "roles", role+".md"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			for _, field := range fields {
				if !strings.Contains(string(data), field) {
					t.Errorf("role %s missing %q", role, field)
				}
			}
		}
	})
	t.Run(`JS: "resolveModel cursor: exact id and early failure (fake cursor-agent)"`, func(t *testing.T) {
		env, _ := fakeEnv(t, map[string][]fakecli.Rule{"cursor-agent": {{Argv: []string{"--list-models"}, Stdout: "grok-4.7-xhigh - Grok\nclaude-opus-4-8-xhigh - Claude\n"}}})
		if got, err := ResolveModel("cursor", "grok-4.7-xhigh", "xhigh", env, nil); err != nil || got != "grok-4.7-xhigh" {
			t.Fatalf("exact model=%q err=%v", got, err)
		}
		if _, err := ResolveModel("cursor", "grok-4.7-xhigh[context=500k]", "xhigh", env, nil); err == nil || !strings.Contains(err.Error(), "no cursor model matches") {
			t.Fatalf("expected strict early failure, got %v", err)
		}
	})
	t.Run(`JS: "model cache: bash location/format, fresh for 60 minutes, short mode skips the write"`, func(t *testing.T) {
		env, _ := fakeEnv(t, map[string][]fakecli.Rule{"grok": {{Argv: []string{"models"}, Stdout: "grok-4.6 - old\ngrok-4.7 - new\n"}}})
		if got := ModelIDs("grok", env); !reflect.DeepEqual(got, []string{"grok-4.6", "grok-4.7"}) {
			t.Fatalf("model ids=%v", got)
		}
		cache := ModelsCacheFile("grok", env)
		if data, err := os.ReadFile(cache); err != nil || string(data) != "grok-4.6\ngrok-4.7\n" {
			t.Fatalf("cache=%q err=%v", data, err)
		}
		noCLI := env.Clone()
		noCLI["PATH"] = t.TempDir()
		if got := ModelIDs("grok", noCLI); !reflect.DeepEqual(got, []string{"grok-4.6", "grok-4.7"}) {
			t.Fatalf("fresh cache not used: %v", got)
		}
		stale := time.Now().Add(-61 * time.Minute)
		if err := os.Chtimes(cache, stale, stale); err != nil {
			t.Fatal(err)
		}
		short, _ := fakeEnv(t, map[string][]fakecli.Rule{"grok": {{Argv: []string{"models"}, Stdout: "grok-4.6 - old\n"}}})
		short["SOHO_MODELS_TIMEOUT"] = "5"
		if got := ModelIDs("grok", short); !reflect.DeepEqual(got, []string{"grok-4.6"}) {
			t.Fatalf("short listing=%v", got)
		}
		if _, err := os.Stat(ModelsCacheFile("grok", short)); !os.IsNotExist(err) {
			t.Fatalf("short mode wrote cache: %v", err)
		}
	})
	t.Run(`JS: "model listing: a CLI slower than SOHO_MODELS_TIMEOUT yields no list and no cache"`, func(t *testing.T) {
		env, _ := fakeEnv(t, map[string][]fakecli.Rule{"grok": {{Argv: []string{"models"}, Stdout: "grok-4.7 - slow\n", Delay: 1200}}})
		env["SOHO_MODELS_TIMEOUT"] = "0.02"
		if got := ModelIDs("grok", env); len(got) != 0 {
			t.Fatalf("timed-out listing=%v", got)
		}
		if _, err := os.Stat(ModelsCacheFile("grok", env)); !os.IsNotExist(err) {
			t.Fatalf("timed-out listing wrote cache: %v", err)
		}
	})
	t.Run(`JS: "codexEffortCeiling: the test-kinds.sh slugs (ported)"`, func(t *testing.T) {
		home := t.TempDir()
		if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
			t.Fatal(err)
		}
		cache := `{"models":[{"slug":"big","supported_reasoning_levels":[{"effort":"high"},{"effort":"max"}]},{"slug":"small","supported_reasoning_levels":[{"effort":"xhigh"}]}]}`
		if err := os.WriteFile(filepath.Join(home, ".codex", "models_cache.json"), []byte(cache), 0o600); err != nil {
			t.Fatal(err)
		}
		env := platform.Env{"HOME": home, "USERPROFILE": home, "TMPDIR": t.TempDir()}
		for model, want := range map[string]string{"big": "max", "small": "xhigh", "not-listed": "xhigh", "": "xhigh"} {
			if got := CodexEffortCeiling(model, env); got != want {
				t.Errorf("model %q ceiling=%q want %q", model, got, want)
			}
		}
	})
}

func TestERERegExpKeepsInvertedLetterRangesInvalid(t *testing.T) {
	// Mutation captured: folding each letter of [a-Z] on its own writes
	// [aA-zZ], a valid A-z range that matches _ and [; the JS RegExp rejects
	// [a-Z] and ereRegExp returns null. Oracle: new RegExp(p, "i") in node.
	for pattern, valid := range map[string]bool{"[a-Z]": false, "[z-a]": false, "[a-z]": true, "[A-Z]": true, "[A-z]": true} {
		if _, ok := ERERegExp(pattern); ok != valid {
			t.Errorf("ERERegExp(%q) ok=%v, want %v", pattern, ok, valid)
		}
	}
}
