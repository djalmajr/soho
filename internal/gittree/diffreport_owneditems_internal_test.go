package gittree

// SOH-410 round 4: item parsing counts every path token, while the audit
// matcher is exactly the base: a bare name means the file at the tree root.
// No tree listing is involved in parsing, linting or matching.

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

func ownedItemSection(item string, section string) string {
	return "# Brief\n# " + section + "\n" + item + "\n# Report contract\nComplete.\n"
}

func sortedItems(items []string) []string {
	out := append([]string{}, items...)
	sort.Strings(out)
	return out
}

func TestDiffSectionOwnedItemPaths(t *testing.T) {
	cases := []struct {
		name, item string
		want       []string
	}{
		{"two backticked bare names yield literal entries", "- `pane-view.tsx` e `editor.tsx` (só para remover ...) ", []string{"pane-view.tsx", "editor.tsx"}},
		{"prose item owns nothing", "- O vtest dele (crie se não existir)", nil},
		{"two plain paths", "- src/a.ts e src/b.ts (nota)", []string{"src/a.ts", "src/b.ts"}},
		{"backticked bare name keeps its spelling", "- `notes.md`", []string{"notes.md"}},
		{"balanced parentheses stay in a path", "- src/(setup)", []string{"src/(setup)"}},
		{"backticked trailing dot is part of the name", "- `sample.ts.`", []string{"sample.ts."}},
		{"plain trailing punctuation is trimmed", "- src/a.ts, e src/b.ts.", []string{"src/a.ts", "src/b.ts"}},
		{"unbalanced opening parenthesis is prose", "- (src/a.ts", []string{"src/a.ts"}},
		{"inherit is unchanged", "- inherit: previous brief", []string{"\x00inherit previous brief"}},
		{"new tests is unchanged", "- new tests", []string{"\x00new tests"}},
		{"bare glob is unchanged", "- `*`", []string{"*"}},
		{"recursive glob is unchanged", "- `**/*.ts`", []string{"**/*.ts"}},
		{"folder marker is kept", "- `src/`", []string{"src/"}},
		{"plain dotted name", "- a.test.ts", []string{"a.test.ts"}},
		{"full path is literal", "- `src/deep/file.go`", []string{"src/deep/file.go"}},
		{"spaces in a backticked name", "- `my file.ts`", []string{"my file.ts"}},
		{"windows spelling keeps TreeRelative", "- `src\\deep\\file.go`", windowsWant("src/deep/file.go", "src\\deep\\file.go")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, found := diffSection(ownedItemSection(tc.item, "Owned files"), "", platform.Current(), "owned files")
			if !found {
				t.Fatal("Owned files section not found")
			}
			if want := sortedItems(tc.want); !reflect.DeepEqual(sortedItems(got), want) {
				t.Fatalf("diffSection = %q, want %q", got, want)
			}
		})
	}
}

// R586 F1: qualification is normalized by TreeRelative as on the base, and
// the resulting glob keeps glob matching at the root.
func TestDiffSectionBaseQualifiedRootForms(t *testing.T) {
	tree := t.TempDir()
	abs := filepath.Join(tree, "config.ts")
	brief := fmt.Sprintf("# Brief\n# Owned files\n- `./*.ts`\n- `%s`\n- `config.ts`\n# Report contract\n", abs)
	got, found := diffSection(brief, tree, platform.Current(), "owned files")
	want := []string{"*.ts", "config.ts", "config.ts"}
	if !found || !reflect.DeepEqual(sortedItems(got), sortedItems(want)) {
		t.Fatalf("diffSection = %q, want %q (tree %q)", got, want, tree)
	}
	for _, tc := range []struct {
		pattern, file string
		want          bool
	}{
		{"*.ts", "root.ts", true},
		{"*.ts", "src/nested.ts", false},
		{"config.ts", "config.ts", true},
		{"config.ts", "src/config.ts", false},
	} {
		if got := diffOwnedMatch(tc.pattern, tc.file); got != tc.want {
			t.Errorf("diffOwnedMatch(%q, %q) = %t, want %t", tc.pattern, tc.file, got, tc.want)
		}
	}

	// A root entry and a win32 spelling keep the base TreeRelative spelling.
	nested, found := diffSection(fmt.Sprintf("# Brief\n# Owned files\n- src/config.ts\n- `%s`\n", tree), tree, platform.Current(), "owned files")
	if !found || !reflect.DeepEqual(sortedItems(nested), sortedItems([]string{"src/config.ts", diffOwnedRootPattern})) {
		t.Fatalf("nested/root entries = %q", nested)
	}
	win, found := diffSection("# Brief\n# Owned files\n- `C:\\work\\config.ts`\n- `./config.ts`\n- `config.ts`\n", `C:\work`, "win32", "owned files")
	if !found || !reflect.DeepEqual(sortedItems(win), sortedItems([]string{"config.ts", "config.ts", "config.ts"})) {
		t.Fatalf("win32 entries = %q", win)
	}
}

// R586 F3: legacy inherited fields are read verbatim and matched as on the
// base, where the normalized root name is stored in the sidecar.
func TestDiffEffectiveOwnedInheritedBaseFields(t *testing.T) {
	dir := t.TempDir()
	sidecar := filepath.Join(dir, "worker.dispatch.json")
	if err := os.WriteFile(filepath.Join(dir, "worker.brief.md"), []byte("# Brief\n# Owned files\n- inherit: previous brief\n# Report contract\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(dir, "config.ts")
	body := fmt.Sprintf("{\"git_tree\":%q,\"git_previous_owned\":[\"config.ts\",\"./config.ts\",%q]}\n", dir, abs)
	if err := os.WriteFile(sidecar, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	owned, ok := EffectiveOwned(sidecar, "worker")
	want := []string{"config.ts", "./config.ts", abs}
	if !ok || !reflect.DeepEqual(owned, want) {
		t.Fatalf("EffectiveOwned = %q, %t, want %q", owned, ok, want)
	}
	for _, tc := range []struct {
		pattern, file string
		want          bool
	}{
		{owned[0], "config.ts", true},
		{owned[0], "src/config.ts", false},
		{owned[1], "config.ts", false},
		{owned[2], "config.ts", false},
	} {
		if got := diffOwnedMatch(tc.pattern, tc.file); got != tc.want {
			t.Errorf("diffOwnedMatch(%q, %q) = %t, want %t", tc.pattern, tc.file, got, tc.want)
		}
	}
}

// SOH-444: a backticked token counts as a path only when it is path-shaped;
// an item that is only one backticked token keeps it (a bare root name).
func TestDiffSectionOwnedItemQuotedPathShape(t *testing.T) {
	cases := []struct {
		name, item string
		want       []string
	}{
		{
			name: "prose identifiers and prose words around one real path",
			item: "- `apps/desktop/src/app.tsx`, only in the handlers `ai.openChat` and `ai.inlineAction` and in `attachSelectionToChat`",
			want: []string{"apps/desktop/src/app.tsx"},
		},
		{"a single backticked name is still a path", "- `editor.tsx`", []string{"editor.tsx"}},
		{"a single backticked bare name is still a path", "- `Makefile`", []string{"Makefile"}},
		{"a single backticked README is still a path", "- `README`", []string{"README"}},
		{"two prose identifiers own nothing", "- `Makefile` and `README`", nil},
		{"one prose identifier alone is the single-token exception", "- `ai.openChat`", []string{"ai.openChat"}},
		{"a capital extension among other tokens is prose", "- `NOTES.MD` and `src/a.ts`", []string{"src/a.ts"}},
		{"a digit extension is a path", "- `.env` and `clip.mp4`", []string{".env", "clip.mp4"}},
		{"globs are paths", "- `*.go` and `src/**`", []string{"*.go", "src/**"}},
		{"a prose note makes the item more than one token", "- `Makefile` (build)", nil},
		{"balanced parentheses in a quoted path stay", "- `src/(setup)`", []string{"src/(setup)"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, found := diffSection(ownedItemSection(tc.item, "Owned files"), "", platform.Current(), "owned files")
			if !found {
				t.Fatal("Owned files section not found")
			}
			if want := sortedItems(tc.want); !reflect.DeepEqual(sortedItems(got), want) {
				t.Fatalf("diffSection = %q, want %q", got, want)
			}
		})
	}
}

// F2: a plain (unquoted) token keeps the grammar it had before the
// backticked rule: the extension is matched case-insensitively, so a plain
// NOTES.MD names a path while the backticked one stays prose.
func TestDiffSectionPlainTokenPathShape(t *testing.T) {
	got, found := diffSection(ownedItemSection("- NOTES.MD", "Owned files"), "", platform.Current(), "owned files")
	if want := []string{"NOTES.MD"}; !found || !reflect.DeepEqual(got, want) {
		t.Fatalf("plain uppercase extension = %q (found=%t), want %q", got, found, want)
	}
	got, found = diffSection(ownedItemSection("- notes.md", "Owned files"), "", platform.Current(), "owned files")
	if want := []string{"notes.md"}; !found || !reflect.DeepEqual(got, want) {
		t.Fatalf("lowercase extension = %q (found=%t), want %q", got, found, want)
	}
	got, found = diffSection(ownedItemSection("- `NOTES.MD` and `src/a.ts`", "Owned files"), "", platform.Current(), "owned files")
	if want := []string{"src/a.ts"}; !found || !reflect.DeepEqual(sortedItems(got), sortedItems(want)) {
		t.Fatalf("backticked uppercase extension = %q (found=%t), want %q", got, found, want)
	}
}

// F1: '?' is a glob character in the shared shape rule, like '*' and '[', so
// a root-level wildcard beside another path owns the files it matches.
func TestDiffSectionOwnedQuestionGlob(t *testing.T) {
	got, found := diffSection(ownedItemSection("- `tool?` and `go.mod`", "Owned files"), "", platform.Current(), "owned files")
	if want := []string{"go.mod", "tool?"}; !found || !reflect.DeepEqual(sortedItems(got), sortedItems(want)) {
		t.Fatalf("question glob = %q (found=%t), want %q", got, found, want)
	}
	if !diffOwnedMatch("tool?", "tool1") || diffOwnedMatch("tool?", "src/tool1") {
		t.Fatal("the question glob must match a root name and nothing nested")
	}
	for _, item := range []string{"- `tool?` (generated)", "- `tool?`"} {
		if got, found := diffSection(ownedItemSection(item, "Owned files"), "", platform.Current(), "owned files"); !found || len(got) != 1 || got[0] != "tool?" {
			t.Fatalf("item %q = %q (found=%t), want [tool?]", item, got, found)
		}
	}
}

// F3: a real root entry listed beside another path is a path, a positive
// label is stripped before the single-token decision, and an identifier with
// no such root entry stays prose.
func TestDiffSectionOwnedRootEntry(t *testing.T) {
	tree := t.TempDir()
	if err := os.WriteFile(filepath.Join(tree, "Dockerfile"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, item string
		root       string
	}{
		{"real root file in a list", "- `Dockerfile` and `go.mod`", tree},
		{"labeled single path with its tree", "- Owned: `Dockerfile`", tree},
		{"labeled single path without a tree", "- Owned: `Dockerfile`", ""},
		{"single path with its tree", "- `Dockerfile`", tree},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, found := diffSection(ownedItemSection(tc.item, "Owned files"), tc.root, platform.Current(), "owned files")
			if !found {
				t.Fatal("Owned files section not found")
			}
			want := []string{"Dockerfile", "go.mod"}
			if tc.item == "- `Dockerfile`" || tc.item == "- Owned: `Dockerfile`" {
				want = []string{"Dockerfile"}
			}
			if !reflect.DeepEqual(sortedItems(got), sortedItems(want)) {
				t.Fatalf("diffSection = %q, want %q", got, want)
			}
		})
	}
	// Without a tree, a mixed item applies the shape rule only: the name that
	// is not path-shaped stays prose, exactly as before F3.
	got, found := diffSection(ownedItemSection("- `Dockerfile` and `go.mod`", "Owned files"), "", platform.Current(), "owned files")
	if want := []string{"go.mod"}; !found || !reflect.DeepEqual(got, want) {
		t.Fatalf("mixed item without a tree = %q (found=%t), want %q", got, found, want)
	}
	// A prose identifier beside a real path never becomes a path.
	got, found = diffSection(ownedItemSection("- `go.mod`, only in `ai.openChat`", "Owned files"), tree, platform.Current(), "owned files")
	if want := []string{"go.mod"}; !found || !reflect.DeepEqual(got, want) {
		t.Fatalf("identifier beside a path = %q (found=%t), want %q", got, found, want)
	}
}

// SOH-444: the lint readers (OwnedBareNames, OwnedItemLintMessages) share the
// rule: the reported line names no bare name and no prose item.
func TestOwnedItemLintTokensQuotedPathShape(t *testing.T) {
	line := "- `apps/desktop/src/app.tsx`, only in the handlers `ai.openChat` and `ai.inlineAction` and in `attachSelectionToChat`"
	if got := OwnedBareNames([]string{line}, ""); len(got) != 0 {
		t.Fatalf("bare names = %q, want none", got)
	}
	if got := OwnedItemLintMessages([]string{line}, ""); len(got) != 0 {
		t.Fatalf("lint messages = %q, want none", got)
	}
	if got := OwnedBareNames([]string{"- `Makefile` and `README`"}, ""); len(got) != 0 {
		t.Fatalf("two prose names = %q, want none", got)
	}
	if got, want := OwnedBareNames([]string{"- `Makefile`"}, ""), []string{"Makefile"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("single bare name = %q, want %q", got, want)
	}
}

// F3: a backticked token that is not path-shaped counts as a path when an
// entry of exactly that name exists at the tree root, so a real Dockerfile
// listed beside another path survives while an identifier does not. Without a
// tree (root ""), the shape rule alone decides.
func TestOwnedItemLintRootEntry(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Dockerfile"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "vendor"), 0o700); err != nil {
		t.Fatal(err)
	}
	lines := []string{"- `Dockerfile` and `go.mod`", "- `vendor` (generated)"}
	if got, want := ownedItemLintTokensForTest(lines, root), []string{"Dockerfile", "go.mod", "vendor"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tokens with a real root entry = %q, want %q", got, want)
	}
	if got := OwnedItemLintMessages(lines, root); len(got) != 0 {
		t.Fatalf("lint messages with a real root entry = %q, want none", got)
	}
	if got, want := ownedItemLintTokensForTest([]string{"- `ai.openChat` and `go.mod`"}, root), []string{"go.mod"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tokens for an identifier without a root entry = %q, want %q", got, want)
	}
	if got, want := ownedItemLintTokensForTest(lines[1:], ""), []string(nil); !reflect.DeepEqual(got, want) {
		t.Fatalf("tokens without a tree = %q, want %q", got, want)
	}
	for i, tc := range []string{"`Dockerfile` (build)", "Owned: `Dockerfile`"} {
		if got, want := ownedItemLintTokensForTest([]string{"- " + tc}, root), []string{"Dockerfile"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("case %d: tokens = %q, want %q", i, got, want)
		}
	}
}

// ownedItemLintTokensForTest flattens the tokens of several Owned lines.
func ownedItemLintTokensForTest(lines []string, root string) []string {
	var out []string
	for _, line := range lines {
		tokens, skip := ownedItemLintTokens(line, root)
		if skip {
			continue
		}
		out = append(out, tokens...)
	}
	return out
}

func windowsWant(windows, other string) []string {
	if platform.Current() == "win32" {
		return []string{windows}
	}
	return []string{other}
}

// Orchestrator files uses the same item rules as Owned files: every path
// token in a plain item counts, so both exclusions below are recorded.
func TestDiffSectionOrchestratorItemPaths(t *testing.T) {
	got, found := diffSection(ownedItemSection("- src/a.ts and src/b.ts", "Orchestrator files"), "", platform.Current(), "orchestrator files")
	want := []string{"src/a.ts", "src/b.ts"}
	if !found || !reflect.DeepEqual(sortedItems(got), sortedItems(want)) {
		t.Fatalf("orchestrator = %q found=%t, want %q", got, found, want)
	}
}

// Other sections keep the old first-word behavior.
func TestDiffSectionOtherSectionKeepsFirstWord(t *testing.T) {
	got, found := diffSection(ownedItemSection("- O vtest dele (crie se não existir)", "Forbidden"), "", platform.Current(), "forbidden")
	if !found || !reflect.DeepEqual(got, []string{"O"}) {
		t.Fatalf("forbidden = %q found=%t, want [O]", got, found)
	}
}

func TestDiffOwnedMatchBase(t *testing.T) {
	cases := []struct {
		name, pattern, file string
		want                bool
	}{
		{"bare name is a root file", "pane-view.tsx", "pane-view.tsx", true},
		{"bare name does not own a nested file", "pane-view.tsx", "src/components/pane-view.tsx", false},
		{"other file in the same directory", "pane-view.tsx", "other.tsx", false},
		{"a directory segment keeps a bare name outside", "notes.md", "[draft]/notes.md", false},
		{"unrelated file", "notes.md", "src/d/other.md", false},
		// These literal-name assertions run on every platform, including
		// Windows, with no on-disk fixture involved.
		{"trailing dot is literal", "sample.ts.", "sample.ts.", true},
		{"trailing dot is not optional", "sample.ts.", "sample.ts", false},
		{"trailing dot is a root name only", "sample.ts.", "src/sample.ts.", false},
		{"literal parenthesized path", "src/(setup)", "src/(setup)", true},
		{"directory segment is not a file name", "src", "x/src/a.go", false},
		{"folder marker keeps pattern matching", "src/", "src/a.go", true},
		{"glob keeps its old meaning", "*.ts", "a.ts", true},
		{"glob does not match nested paths", "*.ts", "src/a.ts", false},
		{"path glob still matches", "src/*.ts", "src/a.ts", true},
		{"character class is still a pattern", "src/[ab].ts", "src/a.ts", true},
		{"character class rejects other names", "src/[ab].ts", "src/c.ts", false},
		{"bare star keeps its old meaning", "**", "a.ts", true},
		{"tree root covers every file", diffOwnedRootPattern, "src/a.go", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := diffOwnedMatch(tc.pattern, tc.file); got != tc.want {
				t.Fatalf("diffOwnedMatch(%q, %q) = %t, want %t", tc.pattern, tc.file, got, tc.want)
			}
		})
	}
}

func TestOwnedItemLintMessages(t *testing.T) {
	cases := []struct {
		name, item string
		want       []string
	}{
		{"bare name is valid", "- `ghost.ts`", nil},
		{"two bare names are valid", "- `pane-view.tsx` e `editor.tsx`", nil},
		{"full path", "- src/a.ts", nil},
		{"two full paths", "- src/a.ts e src/b.ts (nota)", nil},
		{"trailing dot in a quoted name", "- `sample.ts.`", nil},
		{"inherit", "- inherit: previous brief", nil},
		{"new tests", "- new tests", nil},
		{"explicit empty scope", "- None.", nil},
		{"Portuguese empty scope", "- Nenhum. Só leitura.", nil},
		{"dash empty scope", "- —", nil},
		{"prose item", "- O vtest dele (crie se não existir)", []string{"'O vtest dele (crie se não existir)' names no path"}},
		{"long prose is cut at 60", "- " + strings.Repeat("x", 75), []string{"'" + strings.Repeat("x", 60) + "' names no path"}},
		{"path-shaped token suppresses the warning", "- crie src/a.ts.", nil},
		{"continuation line is not an item", "  se o F5 exigir.", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := OwnedItemLintMessages([]string{tc.item}, "")
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("warnings = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOwnedBareNames(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		want  []string
	}{
		{"two backticked bare names", []string{"- `pane-view.tsx` e `editor.tsx` (nota)"}, []string{"pane-view.tsx", "editor.tsx"}},
		{"plain bare name", []string{"- config.ts"}, []string{"config.ts"}},
		{"trailing dot is part of the name", []string{"- `sample.ts.`"}, []string{"sample.ts."}},
		{"qualified names are not bare", []string{"- src/a.ts e ./b.ts (nota)"}, nil},
		{"glob is not bare", []string{"- `*.ts`", "- `**/*.go`"}, nil},
		{"windows separator is not bare", []string{"- `src\\a.ts`", "- `C:\\work\\a.ts`"}, nil},
		{"empty scope and inherit are exempt", []string{"- None.", "- Nenhum. Só leitura.", "- inherit: previous brief", "- new tests"}, nil},
		{"prose has no tokens", []string{"- O vtest dele (crie se não existir)"}, nil},
		{"continuation line is not an item", []string{"  se o F5 exigir."}, nil},
		{"repeated name is checked once", []string{"- `a.ts`", "- `a.ts`"}, []string{"a.ts"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := OwnedBareNames(tc.lines, "")
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("OwnedBareNames = %q, want %q", got, tc.want)
			}
		})
	}
}
