package dispatch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
)

// composeEnv is the env the CLI always has when it composes a prompt: the
// skill directory resolvable. A throwaway tree stands in for the skill.
func composeEnv(t *testing.T) platform.Env {
	t.Helper()
	return platform.Env{"SOHO_SKILL_DIR": t.TempDir()}
}

func TestDispatchHelpers(t *testing.T) {
	t.Run(`JS: dispatch workspace path comparison accepts Windows slash and case variants`, func(t *testing.T) {
		if !SamePath(`C:\Work\Repo`, `c:/work/repo`, "win32") {
			t.Fatal("case and slash variants did not match")
		}
	})
	t.Run(`JS: dispatch pair gets a suffix when the report pair collides`, func(t *testing.T) {
		got := DispatchPairSuffix(func(s string) string { return "brief" + s }, func(s string) string { return "report" + s }, func(path string) bool { return path == "brief" || path == "report-2" }, "")
		if got != "-3" {
			t.Fatalf("suffix=%q, want -3", got)
		}
	})
	t.Run(`JS: dispatch sidecar is named beside the composed prompt`, func(t *testing.T) {
		got := DispatchSidecar(filepath.Join("state", "briefs", "alice-20260929T033919.brief.md"))
		if got != filepath.Join("state", "briefs", "alice-20260929T033919.dispatch.json") {
			t.Fatalf("sidecar=%q", got)
		}
	})
	t.Run(`JS: dispatch sidecar records the accepted attempt and arrival`, func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "x.dispatch.json")
		if err := WriteSidecar(path, "codex", "gpt-5", "high", "accepted", "queued", ""); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		value, err := jsonjs.Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		obj := value.(*jsonjs.Object)
		for key, want := range map[string]any{"submission": "accepted", "arrival": "queued", "kind": "codex", "model": "gpt-5", "effort": "high"} {
			got, _ := obj.Get(key)
			if got != want {
				t.Errorf("%s=%v, want %v", key, got, want)
			}
		}
		if _, present := obj.Get("session"); present {
			t.Error("an empty session must not be recorded in the sidecar")
		}
	})
	t.Run("the sidecar records the roster session value when present", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "y.dispatch.json")
		if err := WriteSidecar(path, "pi", "m1", "high", "accepted", "", "20260930T085143"); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		value, err := jsonjs.Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		obj := value.(*jsonjs.Object)
		if got, _ := obj.Get("session"); got != "20260930T085143" {
			t.Fatalf("session=%v, want 20260930T085143", got)
		}
		if got, _ := obj.Get("submission"); got != "accepted" {
			t.Fatalf("submission=%v", got)
		}
	})
}

func TestDispatchJavaScriptHelperCases(t *testing.T) {
	const full = "# Goal\n\nDo it.\n# Expected result\n\nDone.\n# Owned files\n\nsrc/a.go\n# Forbidden\n\nDo not commit or push.\n# Report\n\ndone.\n"
	t.Run(`JS: "lint: a full contract brief passes"`, func(t *testing.T) {
		if got := BriefMissingSections(full, false, nil); got != "" {
			t.Fatalf("missing=%q", got)
		}
	})
	t.Run(`JS: "lint: each section absent, each alternative accepted"`, func(t *testing.T) {
		for _, tc := range []struct{ section, replace, want string }{
			{"Goal", "# Goal", " [Goal]"},
			{"Expected result", "# Expected result", " [Expected result]"},
			{"Owned files", "# Owned files", " [Owned files]"},
			{"Forbidden", "# Forbidden", " [Forbidden]"},
			{"Report", "# Report", " [Report]"},
			{"no-git", "Do not commit or push.", " [no-git line: say 'no commit/push']"},
		} {
			t.Run(tc.section, func(t *testing.T) {
				body := strings.Replace(full, tc.replace, "# Unrelated", 1)
				if got := BriefMissingSections(body, false, nil); got != tc.want {
					t.Fatalf("missing=%q want %q", got, tc.want)
				}
			})
		}
		for _, tc := range []struct{ from, to string }{{"Expected result", "Acceptance"}, {"Expected result", "Definition of done"}, {"Owned files", "Owned"}, {"Owned files", "Scope"}, {"Forbidden", "Non-goals"}, {"Forbidden", "Constraints"}} {
			body := strings.Replace(full, "# "+tc.from, "# "+tc.to, 1)
			if got := BriefMissingSections(body, false, nil); got != "" {
				t.Errorf("alternative %s: missing=%q", tc.to, got)
			}
		}
	})
	t.Run(`JS: "lint: built-in Portuguese section headings and accentless headings pass"`, func(t *testing.T) {
		body := "## Objetivo\nDo it.\n## Resultado esperado\nDone.\n## Arquivos\nsrc/a.go\n## Proibido\nNo commit/push.\n## Relatório\nDone.\n"
		if got := BriefMissingSections(body, false, nil); got != "" {
			t.Fatalf("Portuguese headings missing: %s", got)
		}
		body = strings.Replace(body, "Relatório", "Relatorio", 1)
		if got := BriefMissingSections(body, false, nil); got != "" {
			t.Fatalf("accentless heading missing: %s", got)
		}
	})
	t.Run(`JS: "lint and ownership: Portuguese section prefixes reject word continuations"`, func(t *testing.T) {
		for _, heading := range []string{"Arquivos proibidos", "Escopo fora"} {
			body := strings.Replace(full, "# Owned files", "## "+heading, 1)
			if got := BriefMissingSections(body, false, nil); got != " [Owned files]" {
				t.Errorf("%q missing=%q", heading, got)
			}
		}
	})
	t.Run(`JS: "lint: Meta is a Portuguese Goal heading but Metadata and Metadados are not"`, func(t *testing.T) {
		for _, tc := range []struct{ heading, want string }{{"Meta", ""}, {"Metadata", " [Goal]"}, {"Metadados", " [Goal]"}} {
			body := strings.Replace(full, "# Goal", "## "+tc.heading, 1)
			if got := BriefMissingSections(body, false, nil); got != tc.want {
				t.Errorf("%s: missing=%q want %q", tc.heading, got, tc.want)
			}
		}
	})
	t.Run(`JS: "lint: header level 1-3 only, case-insensitive, a word mid-paragraph does not count"`, func(t *testing.T) {
		for _, tc := range []struct{ heading, want string }{{"## gOaL", ""}, {"### Goal", ""}, {"#### Goal", " [Goal]"}, {"#Goal", " [Goal]"}, {"Goal: do it.\n# Expected result", " [Goal]"}} {
			body := strings.Replace(full, "# Goal\n", "", 1)
			body = strings.Replace(body, "# Expected result", tc.heading+"\n# Expected result", 1)
			if got := BriefMissingSections(body, false, nil); got != tc.want {
				t.Errorf("heading %q: missing=%q want %q", tc.heading, got, tc.want)
			}
		}
	})
	t.Run(`JS: "lint: every missing section in order"`, func(t *testing.T) {
		want := " [Goal] [Expected result] [Owned files] [Forbidden] [Report] [no-git line: say 'no commit/push']"
		if got := BriefMissingSections("Just do it.\n", false, nil); got != want {
			t.Fatalf("missing=%q want %q", got, want)
		}
	})
	t.Run(`JS: "lint: warn prints the message and continues; off is silent"`, func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "brief.md")
		if err := os.WriteFile(file, []byte("# Goal\n# Owned files\n# Forbidden\nNo commit/push.\n# Report\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
		warn := BriefLintFindings(file, ctx, platform.Env{}, BriefLintOptions{})
		if warn.Mode != "warn" || !strings.Contains(warn.MissingMessage, "[Expected result]") {
			t.Fatalf("warn=%#v", warn)
		}
		off := BriefLintFindings(file, ctx, platform.Env{"SOHO_BRIEF_LINT": "off"}, BriefLintOptions{})
		if off.Mode != "off" || off.MissingMessage != "" || len(off.Warnings) != 0 {
			t.Fatalf("off=%#v", off)
		}
	})
	t.Run(`JS: "compose: role header, brief verbatim, the report contract in order"`, func(t *testing.T) {
		role := filepath.Join(t.TempDir(), "implementer.md")
		if err := os.WriteFile(role, []byte("---\nname: Implementer\n---\n\nRole text.\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		prompt := ComposePrompt(role, "implementer", "worker", full, "/report.md", &core.Config{Entries: map[string]core.ConfigEntry{}}, composeEnv(t), "codex", "", "", false)
		if !strings.HasPrefix(prompt, "# Role: Implementer") || !strings.Contains(prompt, "# Brief\n\n"+full) || !strings.Contains(prompt, "Write your report as Markdown to `/report.md`") {
			t.Fatalf("composed prompt misses contract: %s", prompt)
		}
		if strings.Index(prompt, "# Brief") > strings.Index(prompt, "# Report contract") {
			t.Fatal("report contract precedes brief")
		}
	})
	t.Run(`JS: "compose: report_language and worker_context=lean add their lines"`, func(t *testing.T) {
		role := filepath.Join(t.TempDir(), "implementer.md")
		if err := os.WriteFile(role, []byte("---\nname: Implementer\n---\n\nRole text.\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		env := platform.Env{"SOHO_REPORT_LANGUAGE": "pt-BR", "SOHO_WORKER_CONTEXT": "lean", "SOHO_SKILL_DIR": t.TempDir()}
		prompt := ComposePrompt(role, "implementer", "worker", full, "/report.md", &core.Config{Entries: map[string]core.ConfigEntry{}}, env, "codex", "", "", false)
		if !strings.Contains(prompt, "- Write the report in pt-BR.\n") || !strings.Contains(prompt, "- This brief is self-contained.") {
			t.Fatalf("prompt missed language/context: %s", prompt)
		}
	})
}

func TestDispatchRemainingHelperCases(t *testing.T) {
	t.Run(`JS: "compose: the report-writer line leads the standing rules in brief and amendment"`, func(t *testing.T) {
		role := filepath.Join(t.TempDir(), "role.md")
		if err := os.WriteFile(role, []byte("---\nname: Tester\n---\nRole.\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
		brief := ComposePrompt(role, "implementer", "agent", "# Goal\nrun", "/report.md", ctx, composeEnv(t), "codex", "", "", false)
		amend := ComposeAmendment("# Amend\nfix", "/report.md", ctx, composeEnv(t), "codex", "", "", false)
		line := "- Only you write this report, once all of the brief is done, including any part you handed to subagents or background tasks; a subagent never writes it. Before you write it, wait until every subagent and background task you started has finished, and change no file after it. Report every item as it stands in the files, not as a subagent summarized it.\n"
		for name, prompt := range map[string]string{"brief": brief, "amendment": amend} {
			if strings.Count(prompt, line) != 1 || strings.Index(prompt, "- Write the report in one go") > strings.Index(prompt, line) || strings.Index(prompt, line) > strings.Index(prompt, "- Nobody watches this terminal") {
				t.Errorf("%s report-writer ordering/count invalid", name)
			}
		}
	})
	t.Run(`JS: "compose: the report covers only the current brief and its amendments (R30, in order)"`, func(t *testing.T) {
		role := filepath.Join(t.TempDir(), "role.md")
		if err := os.WriteFile(role, []byte("---\nname: Tester\n---\nRole.\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
		line := "- Report only the current brief and its explicit amendments; do not import unrelated work from earlier briefs retained in a reused session. Mention prior work only when it directly affects this brief, stating the relationship.\n"
		for name, prompt := range map[string]string{"brief": ComposePrompt(role, "implementer", "agent", "# Goal\nrun", "/report.md", ctx, composeEnv(t), "codex", "", "", false), "amendment": ComposeAmendment("# Amend\nfix", "/report.md", ctx, composeEnv(t), "codex", "", "", false)} {
			if strings.Count(prompt, line) != 1 || strings.Index(prompt, "# Report contract") > strings.Index(prompt, line) || strings.Index(prompt, line) > strings.Index(prompt, "- Command output you put in the report") {
				t.Errorf("%s report-scope rule ordering/count invalid", name)
			}
		}
	})
	t.Run(`JS: "lint: a read-only brief needs no Owned files; the other sections still hold"`, func(t *testing.T) {
		body := "# Goal\nrun\n# Expected result\nok\n# Forbidden\nno commit or push\n# Report\ndone\n"
		if got := BriefMissingSections(body, false, nil); got != " [Owned files]" {
			t.Fatalf("edit missing=%q", got)
		}
		if got := BriefMissingSections(body, true, nil); got != "" {
			t.Fatalf("read-only missing=%q", got)
		}
		for _, tc := range []struct {
			edit string
			want string
		}{{strings.Replace(body, "# Forbidden", "", 1), " [Forbidden]"}, {strings.Replace(body, "no commit or push", "do nothing", 1), " [no-git line: say 'no commit/push']"}} {
			if got := BriefMissingSections(tc.edit, true, nil); got != tc.want {
				t.Errorf("read-only missing=%q want %q", got, tc.want)
			}
		}
	})
	t.Run("compose: the prompt names the native launcher binary instead of PATH", func(t *testing.T) {
		role := filepath.Join(t.TempDir(), "implementer.md")
		if err := os.WriteFile(role, []byte("---\nname: Implementer\n---\n\nRun `soho mutation-guard <copy>` before mutating that copy.\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		// The override is resolved through symlinks before use (the macOS
		// /var -> /private/var link included): assert the resolved path.
		resolved, err := filepath.EvalSymlinks(exe)
		if err != nil {
			t.Fatal(err)
		}
		// The configured override is the native test binary; the skill
		// directory in the env must play no part in the path.
		p := ComposePrompt(role, "implementer", "worker", "# Goal\nrun", "/report.md", &core.Config{Entries: map[string]core.ConfigEntry{}}, platform.Env{"SOHO_BIN": exe, "SOHO_SKILL_DIR": t.TempDir()}, "codex", "", "", false)
		want := "- Run every `soho` command this prompt names through the launcher at `" + resolved + "`, not through PATH.\n"
		if !strings.Contains(p, want) {
			t.Fatalf("composed prompt missed the launcher line:\n%s", p)
		}
		if strings.Count(p, want) != 1 {
			t.Fatalf("launcher line is not exactly once:\n%s", p)
		}
		after := p[strings.Index(p, want)+len(want):]
		if !strings.HasPrefix(after, "- Only you write this report,") {
			t.Fatalf("launcher line must sit right before the standing rules:\n%s", after)
		}
		if strings.Contains(p, "scripts/soho") || strings.Contains(p, "scripts\\soho") {
			t.Fatalf("composed prompt names a skill scripts launcher:\n%s", p)
		}
	})
	t.Run("compose: an invalid SOHO_BIN fails closed before the prompt exists", func(t *testing.T) {
		role := filepath.Join(t.TempDir(), "implementer.md")
		if err := os.WriteFile(role, []byte("---\nname: Implementer\n---\n\nRole.\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			name string
			bin  string
			want string
		}{
			{name: "missing binary", bin: filepath.Join(t.TempDir(), "missing"), want: "SOHO_BIN is missing or not a regular file"},
			{name: "relative binary", bin: "soho", want: "SOHO_BIN must be an absolute path"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				var prompt string
				func() {
					defer func() {
						r := recover()
						if r == nil {
							t.Fatalf("ComposePrompt returned a prompt for the invalid override %q; it must fail closed before the prompt", tc.bin)
						}
						exit, ok := r.(*platform.ExitError)
						if !ok || exit.Code != 2 || !strings.Contains(exit.Msg, tc.want) {
							t.Fatalf("composition panicked %#v; want ExitError code 2 naming %q", r, tc.want)
						}
					}()
					prompt = ComposePrompt(role, "implementer", "worker", "# Goal\nrun", "/report.md", &core.Config{Entries: map[string]core.ConfigEntry{}}, platform.Env{"SOHO_BIN": tc.bin}, "codex", "", "", false)
				}()
				_ = prompt
			})
		}
	})
}

// TestComposeAmendmentAsksForAFreshCheck: an amendment with the same brief made
// a reviewer copy its earlier report and rerun nothing (cinzel); the amendment
// contract now asks for every item to be checked again against the current files.
func TestComposeAmendmentAsksForAFreshCheck(t *testing.T) {
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
	amend := ComposeAmendment("# Amend\nfix", "/report.md", ctx, composeEnv(t), "codex", "", "", false)
	want := "- This amendment overrides your current brief where they differ; the rest of that brief still holds.\n" +
		"- Check every item again against the files as they are now: rerun the checks it needs, and never copy findings, outputs or states from your earlier report.\n"
	if !strings.Contains(amend, want) {
		t.Fatalf("amendment contract without the fresh-check line:\n%s", amend)
	}
}
