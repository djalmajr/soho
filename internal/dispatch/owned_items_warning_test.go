package dispatch

// SOH-410 round 4: dispatch warns, without listing the tree, for a bare
// Owned name absent at the worktree root (one os.Stat per name) and for an
// item that names no path. Both are warnings, never strict refusals.

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

func ownedLintTree(t *testing.T) (string, platform.Env) {
	t.Helper()
	root := t.TempDir()
	tree := filepath.Join(root, "tree")
	if err := os.MkdirAll(tree, 0o700); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{
		"PATH":                bin,
		"HOME":                root,
		"USERPROFILE":         root,
		"XDG_CONFIG_HOME":     root,
		"TMPDIR":              root,
		"GIT_CONFIG_NOSYSTEM": "1",
		"SOHO_BRIEF_LINT":     "warn",
	}
	return tree, env
}

func ownedLintWarnings(t *testing.T, tree string, env platform.Env, item string) []string {
	t.Helper()
	brief := filepath.Join(filepath.Dir(tree), "brief.md")
	body := "# Goal\nDo it.\n# Expected result\nDone.\n# Owned files\n" + item + "\n# Forbidden\nNo commit or push.\n# Report\nDone.\n"
	if err := os.WriteFile(brief, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
	got := BriefLintFindings(brief, ctx, env, BriefLintOptions{WorkerCwd: tree})
	var out []string
	for _, warning := range got.Warnings {
		if strings.Contains(warning, "Owned files item ") {
			out = append(out, warning)
		}
	}
	return out
}

func ownedLintWarningsWant(brief, item string, suffixes ...string) []string {
	if len(suffixes) == 0 {
		return nil
	}
	out := make([]string, 0, len(suffixes))
	for _, suffix := range suffixes {
		out = append(out, "brief "+brief+": Owned files item "+suffix)
	}
	return out
}

func TestBriefLintOwnedItemWarnings(t *testing.T) {
	tree, env := ownedLintTree(t)
	brief := filepath.Join(filepath.Dir(tree), "brief.md")
	rootFile := filepath.Join(tree, "root.ts")
	if err := os.WriteFile(rootFile, []byte("package root\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"NOTES.MD", "Dockerfile", "go.mod"} {
		if err := os.WriteFile(filepath.Join(tree, name), []byte("fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(tree, "assets"), 0o700); err != nil {
		t.Fatal(err)
	}
	bare := func(name string) string {
		return "'" + name + "' is not a file at the worktree root; write its path from the root"
	}
	cases := []struct {
		name, item string
		want       []string
	}{
		{"bare name absent at root", "- `ghost.ts`", []string{bare("ghost.ts")}},
		{"two bare names absent at root", "- `pane-view.tsx` e `editor.tsx`", []string{bare("pane-view.tsx"), bare("editor.tsx")}},
		{"bare name present at root", "- `root.ts`", nil},
		{"directory at root exists", "- `assets`", nil},
		{"plain bare name absent at root", "- config.ts", []string{bare("config.ts")}},
		{"trailing dot is part of the name", "- `sample.ts.`", []string{bare("sample.ts.")}},
		{"full path", "- src/a.ts", nil},
		{"two full paths", "- src/a.ts e src/b.ts (nota)", nil},
		{"glob is not a bare name", "- `*.ts`", nil},
		{"balanced parentheses", "- src/(setup)", nil},
		{"inherit", "- inherit: previous brief", nil},
		{"new tests", "- new tests", nil},
		{"explicit empty scope", "- None.", nil},
		{"Portuguese empty scope", "- Nenhum. Só leitura.", nil},
		{"prose item", "- O vtest dele (crie se não existir)", []string{"'O vtest dele (crie se não existir)' names no path"}},
		{"long prose is cut at 60", "- " + strings.Repeat("x", 75), []string{"'" + strings.Repeat("x", 60) + "' names no path"}},
		{"bare name and prose in separate items", "- `ghost.ts`\n- O vtest dele", []string{"'O vtest dele' names no path", bare("ghost.ts")}},
		{
			name: "prose identifiers inside backticks are not paths",
			item: "- `apps/desktop/src/app.tsx`, only in the handlers `ai.openChat` and `ai.inlineAction` and in `attachSelectionToChat`",
		},
		{"a single backticked Makefile is checked at the root", "- `Makefile`", []string{bare("Makefile")}},
		{"an uppercase extension is prose, not a bare name", "- `NOTES.MD` and `src/a.ts`", nil},
		// F2: a plain token keeps the case-insensitive extension grammar.
		{"a plain uppercase path names a path at the root", "- NOTES.MD", nil},
		{"a plain uppercase path is checked at the root", "- README.MD", []string{bare("README.MD")}},
		// F3: a real root entry listed beside another path is a path.
		{"a real root file in a mixed item", "- `Dockerfile` and `go.mod`", nil},
		{"a labeled single path", "- Owned: `Dockerfile`", nil},
		// F1: '?' is a glob character, so a wildcard item names a path.
		{"a question glob with a note", "- `tool?` (generated)", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ownedLintWarnings(t, tree, env, tc.item)
			want := ownedLintWarningsWant(brief, tc.item, tc.want...)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("warnings for %q = %q, want %q", tc.item, got, want)
			}
		})
	}
}

// With no worker tree the lint cannot check the root, so it emits no Owned
// warnings at all.
func TestBriefLintOwnedItemWarningsNeedTree(t *testing.T) {
	root := t.TempDir()
	brief := filepath.Join(root, "brief.md")
	body := "# Goal\nDo it.\n# Expected result\nDone.\n# Owned files\n- `ghost.ts`\n- O vtest dele\n# Forbidden\nNo commit or push.\n# Report\nDone.\n"
	if err := os.WriteFile(brief, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{"PATH": filepath.Join(root, "bin"), "HOME": root, "USERPROFILE": root, "XDG_CONFIG_HOME": root, "TMPDIR": root, "SOHO_BRIEF_LINT": "warn"}
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
	got := BriefLintFindings(brief, ctx, env, BriefLintOptions{})
	for _, warning := range got.Warnings {
		if strings.Contains(warning, "Owned files item ") {
			t.Fatalf("unexpected owned warning without a tree: %q", warning)
		}
	}
}

// The warnings never enter UnseenPaths: strict cannot refuse a brief for a
// bare name or a prose Owned item.
func TestBriefLintOwnedItemWarningsAreNotStrictRefusals(t *testing.T) {
	tree, env := ownedLintTree(t)
	env["SOHO_BRIEF_LINT"] = "strict"
	brief := filepath.Join(filepath.Dir(tree), "brief.md")
	body := "# Goal\nDo it.\n# Expected result\nDone.\n# Owned files\n- O vtest dele\n- crie sem caminho\n- `ghost.ts`\n# Forbidden\nNo commit or push.\n# Report\nDone.\n"
	if err := os.WriteFile(brief, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
	got := BriefLintFindings(brief, ctx, env, BriefLintOptions{WorkerCwd: tree})
	owned := 0
	for _, warning := range got.Warnings {
		if strings.Contains(warning, "Owned files item ") {
			owned++
			for _, refusal := range got.UnseenPaths {
				if refusal == warning {
					t.Fatalf("owned warning is a strict refusal: %q", warning)
				}
			}
		}
	}
	if owned != 3 {
		t.Fatalf("owned warnings = %d, want 3: %q", owned, got.Warnings)
	}
}

// R587: a stale declared Worktree does not hide the bare-name warning when
// the worker's own directory is a usable tree.
func TestBriefLintOwnedItemWarningsStaleDeclaredTree(t *testing.T) {
	tree, env := ownedLintTree(t)
	brief := filepath.Join(filepath.Dir(tree), "brief-stale.md")
	stale := filepath.Join(filepath.Dir(tree), "gone-worktree")
	body := "# Goal\nDo it.\n\nWorktree: `" + stale + "`\n# Expected result\nDone.\n# Owned files\n- `notes.md`\n# Forbidden\nNo commit or push.\n# Report\nDone.\n"
	if err := os.WriteFile(brief, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
	got := BriefLintFindings(brief, ctx, env, BriefLintOptions{WorkerCwd: tree})
	want := "brief " + brief + ": Owned files item 'notes.md' is not a file at the worktree root; write its path from the root"
	found := false
	for _, w := range got.Warnings {
		if w == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("warnings=%q; want %q", got.Warnings, want)
	}
}
