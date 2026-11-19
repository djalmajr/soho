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
			got := OwnedItemLintMessages([]string{tc.item})
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
			got := OwnedBareNames(tc.lines)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("OwnedBareNames = %q, want %q", got, tc.want)
			}
		})
	}
}
