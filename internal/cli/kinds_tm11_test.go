package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

type tm11GoldenStep struct {
	Args []string `json:"args"`
	Err  string   `json:"err"`
	Out  string   `json:"out"`
	RC   int      `json:"rc"`
}

type tm11Golden struct {
	Files []struct {
		Rel     string  `json:"rel"`
		Content *string `json:"content"`
	} `json:"files"`
	Steps []tm11GoldenStep `json:"steps"`
}

func TestTM11KindsCommandParity(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "testdata", "legacy", "parity-kinds.json"))
	if err != nil {
		t.Fatal(err)
	}
	var goldens map[string]tm11Golden
	if err := json.Unmarshal(data, &goldens); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ title, golden string }{
		{`parity: kinds table with fake CLIs on PATH`, "kinds-table"},
		{`parity: models <kind> (grok, cursor, agy with fakes; codex, pi; errors)`, "models-listing"},
		{`parity: model exact and regex (grok, codex with per-model ceiling)`, "model-exact-regex"},
		{`parity: model cursor effort suffixes (rank ceiling, pass-through suffix)`, "model-cursor"},
		{`parity: model cursor with no match dies 2 (strict CLI)`, "model-cursor-die"},
		{`parity: model generic kinds (pi, opencode — no list, by-model family)`, "model-generic"},
		{`parity: model usage errors (missing kind/spec) are rc 1`, "model-usage"},
		{`parity: model agy alternates a|b and gemini effort suffix`, "model-agy-alternates"},
	}
	for _, tc := range cases {
		t.Run(`JS: "`+tc.title+`"`, func(t *testing.T) {
			golden, ok := goldens[tc.golden]
			if !ok {
				t.Fatalf("missing parity-kinds golden %q", tc.golden)
			}
			env, cwd := commandFixture(t)
			root := filepath.Dir(cwd)
			bin := t.TempDir()
			installKindFakes(t, bin)
			clean := envFrom(testutil.CleanEnv(t))
			for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "SOHO_SKILL_DIR", "HERDR_WORKSPACE_ID"} {
				clean[key] = env.Get(key)
			}
			clean["PATH"] = bin
			clean["TMPDIR"] = t.TempDir()
			clean["USERPROFILE"] = env.Get("HOME")
			env = withFakeCLI(clean, bin)
			if err := os.MkdirAll(filepath.Join(env.Get("HOME"), ".codex"), 0o700); err != nil {
				t.Fatal(err)
			}
			codexModels := `{"models":[{"slug":"gpt-5","supported_reasoning_levels":[{"effort":"low"},{"effort":"high"},{"effort":"xhigh"}]},{"slug":"gpt-5.1","supported_reasoning_levels":[{"effort":"medium"}]},{"slug":"codex-astra","supported_reasoning_levels":[{"effort":"low"},{"effort":"max"}]}]}`
			if err := os.WriteFile(filepath.Join(env.Get("HOME"), ".codex", "models_cache.json"), []byte(codexModels), 0o600); err != nil {
				t.Fatal(err)
			}
			for i, want := range golden.Steps {
				stepEnv := env.Clone()
				stepEnv["PATH"] = bin
				code, out, errOut := runIn(t, want.Args, stepEnv, cwd)
				out = strings.ReplaceAll(out, root, "<ROOT>")
				errOut = strings.ReplaceAll(normalizeParityError(errOut), root, "<ROOT>")
				// DJA-392 intentional contract adaptation: the kinds table
				// gains the MCP column; the frozen bytes are unchanged.
				wantOut, adErr := testutil.RewriteMcpKindsTable(want.Out)
				if adErr != nil {
					t.Fatalf("DJA-392 kinds adaptation: %s", adErr)
				}
				if code != want.RC || out != wantOut || errOut != want.Err {
					t.Fatalf("step %d args=%v\ncode=%d want=%d\nstdout=%q\nwant=%q\nstderr=%q\nwant=%q", i, want.Args, code, want.RC, out, wantOut, errOut, want.Err)
				}
			}
			for _, expected := range golden.Files {
				path := filepath.Join(root, filepath.FromSlash(expected.Rel))
				actual, readErr := os.ReadFile(path)
				if expected.Content == nil {
					if !os.IsNotExist(readErr) {
						t.Fatalf("file %s should be absent, read err=%v", expected.Rel, readErr)
					}
				} else if readErr != nil || string(actual) != *expected.Content {
					t.Fatalf("file %s=%q err=%v want=%q", expected.Rel, actual, readErr, *expected.Content)
				}
			}
		})
	}
}

func installKindFakes(t *testing.T, bin string) {
	t.Helper()
	for name, rules := range map[string][]fakecli.Rule{
		"claude": nil, "codex": nil, "pi": nil, "opencode": nil,
		"grok":         {{Argv: []string{"models"}, Stdout: "Available models:\ngrok-4.7 - xAI Grok 4.7 (default)\ngrok-4.7-build-fast - quick variant\ngrok-4.6 - older release\n"}},
		"agy":          {{Argv: []string{"models"}, Stdout: "gemini-3.8-flash  Google Gemini 3.8 Flash\ngemini-3.8-flash-high  Google Gemini 3.8 Flash (high)\nclaude-opus-4-6  Anthropic Claude Opus 4.6\ngpt-oss-120b  OpenAI GPT-OSS 120B\n"}},
		"cursor-agent": {{Argv: []string{"--list-models"}, Stdout: "grok-4.7-max - xAI Grok 4.7 (max)\ngrok-4.7-high - xAI Grok 4.7 (high)\ngrok-4.6 - xAI Grok 4.6\nclaude-opus-4-8-max - Anthropic Claude Opus 4.8 (max)\n"}},
	} {
		if _, err := fakecli.Install(t, bin, name, rules); err != nil {
			t.Fatal(err)
		}
	}
}
