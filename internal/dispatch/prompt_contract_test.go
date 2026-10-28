package dispatch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
)

func TestComposeEnvironmentAndReferenceContract(t *testing.T) {
	role := filepath.Join(t.TempDir(), "role.md")
	if err := os.WriteFile(role, []byte("---\nname: Tester\n---\nRole.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{"report_language": {Value: "English"}}}
	env := composeEnv(t)
	const environment = "- Never print, log or write the environment as a whole (`env`, `printenv`, `set`, `export -p`, `declare -x`, `Get-ChildItem Env:`, a dump of `os.environ` or `process.env`): it holds credentials. To diagnose, name only the variables you need and leave out any secret value.\n"
	const parity = "- This brief declares `Reference:`: your report must include a `## Reference parity` section with one bullet per comparison, each marked `[same]`, `[diverges: <concrete reason>]` or `[n/a]`.\n"
	// Native absolute roots: a POSIX-only path is not absolute on Windows,
	// and the parser rejects it there.
	ref, other := filepath.Join(t.TempDir(), "reference"), filepath.Join(t.TempDir(), "other")
	for _, tc := range []struct {
		name, body string
		want       int
	}{
		{"none", "# Goal\nReview it.\n", 0},
		{"one", "Reference: " + ref + "@abcdef0:src/a.go\n", 1},
		{"multiple", "Reference: " + ref + "@abcdef0:src/a.go\nReference: " + other + "@1234567\n", 1},
		{"fenced", "```md\nReference: " + ref + "@abcdef0\n```\n", 0},
		{"tilde fenced", "~~~\nReference: " + ref + "@abcdef0\n~~~\n", 0},
		{"invalid", "Reference: missing-pin\n", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for name, prompt := range map[string]string{
				"prompt":    ComposePrompt(role, "reviewer", "worker", tc.body, "/report.md", ctx, env, "codex", "", "", false),
				"amendment": ComposeAmendment(tc.body, "/report.md", ctx, env, "codex", "", "", false),
			} {
				if strings.Count(prompt, environment) != 1 || !strings.Contains(prompt, "- Never invent names, endpoints, flags, credentials, URLs or requirements.\n"+environment+"- Stop only processes") {
					t.Errorf("%s: environment rule missing, duplicated or misplaced", name)
				}
				if got := strings.Count(prompt, parity); got != tc.want {
					t.Fatalf("%s: parity count=%d want=%d", name, got, tc.want)
				}
				if tc.want == 1 && (strings.Index(prompt, parity) < strings.Index(prompt, "- This report path is authoritative:") || strings.Index(prompt, parity) > strings.Index(prompt, "- Write the report in English.")) {
					t.Errorf("%s: parity must follow report paths and precede language", name)
				}
			}
		})
	}
}

func TestBriefScopeIsNotOwnership(t *testing.T) {
	for _, heading := range []string{"Scope", "Escopo", "Scope\n- src/a.go\n## Escopo"} {
		body := "## Goal\nReview.\n## Expected result\nFindings.\n## " + heading + "\n- src/b.go\n## Forbidden\nNo commit/push.\n## Report\nFindings.\n"
		if BriefHasOwnedItems(body, nil) {
			t.Errorf("%q counted as ownership", heading)
		}
		if got := BriefMissingSections(body, false, nil); got != " [Owned files]" {
			t.Errorf("%q: edit lint=%q", heading, got)
		}
		if got := BriefMissingSections(body, true, nil); got != "" {
			t.Errorf("%q: read-only lint=%q", heading, got)
		}
	}
	aliases, ignored := ParseBriefLintAliases("Owned files=Scope|Escopo")
	if len(ignored) != 0 {
		t.Fatal(ignored)
	}
	for _, heading := range []string{"Scope", "Escopo"} {
		if !BriefHasOwnedItems("## "+heading+"\n- src/a.go\n", aliases) {
			t.Errorf("explicit alias %q ignored", heading)
		}
	}
}
