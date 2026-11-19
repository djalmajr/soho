package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestOwnedPathsQuotedPathShape(t *testing.T) {
	cases := []struct {
		name, body string
		want       []string
	}{
		{
			name: "prose identifiers around one real path",
			body: "## Owned files\n- `apps/desktop/src/app.tsx`, only in the handlers `ai.openChat` and `ai.inlineAction` and in `attachSelectionToChat`\n",
			want: []string{"apps/desktop/src/app.tsx"},
		},
		{"single bare backticked name", "## Owned files\n- `Makefile`\n", []string{"Makefile"}},
		{"single backticked README", "## Owned files\n- `README`\n", []string{"README"}},
		{"two prose identifiers own nothing", "## Owned files\n- `Makefile` and `README`\n", []string{}},
		{"uppercase extension among other tokens is prose", "## Owned files\n- `NOTES.MD` and `src/a.ts`\n", []string{"src/a.ts"}},
		{"dotfile and digit extension are paths", "## Owned files\n- `.env` and `clip.mp4`\n", []string{".env", "clip.mp4"}},
		{"globs are paths", "## Owned files\n- `*.go` and `src/**`\n", []string{"*.go", "src/**"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ownedPaths(tc.body, nil); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ownedPaths=%q, want %q", got, tc.want)
			}
		})
	}
}

func TestOwnedLabelsPaths(t *testing.T) {
	cases := []struct {
		name, body string
		want       []string
	}{
		{"vpn", "## Arquivos\nPode tocar:\n- `src/ui/**`\nProibido:\n- `src/**` (outro implementer edita Rust)\n", []string{"src/ui/**"}},
		{"negative only", "## Owned files\nForbidden:\n- `src/a.go`\n", []string{}},
		{"positive after negative", "## Owned files\nForbidden: `src/no.go`\n- `src/no2.go`\nMay edit: `src/yes.go`\n- `src/also.go`\n", []string{"src/yes.go", "src/also.go"}},
		{"bold", "## Owned files\n- `src/a.go`\n**Proibido:**\n- `src/no.go`\n", []string{"src/a.go"}},
		{"list same line", "## Owned files\n- `src/a.go`\n- Forbidden: `src/no.go`\n- `src/no2.go`\n", []string{"src/a.go"}},
		{"accent uppercase", "## Owned files\n- `src/a.go`\nNÃO TOCAR:\n- `src/no.go`\n", []string{"src/a.go"}},
		{"english", "## Owned files\n- `src/a.go`\nDo not touch:\n- `src/no.go`\n", []string{"src/a.go"}},
		{"non labels", "## Owned files\nProibidos\n- `src/forbidden:file.go`\n- `src/yes.go`\n", []string{"src/forbidden:file.go", "src/yes.go"}},
		{"last line", "## Owned files\n- `src/a.go`\nForbidden:", []string{"src/a.go"}},
		{"consecutive and deeper", "## Owned files\n- `src/a.go`\nForbidden:\n### detail\nProibido:\n- `src/no.go`\nOwned: `src/b.go`\n## End\n- `src/no2.go`\n", []string{"src/a.go", "src/b.go"}},
		{"crlf", "## Arquivos\r\nPode tocar:\r\n- `src/a.go`\r\nProibido:\r\n- `src/no.go`\r\nOwned: `src/b.go`\r\n", []string{"src/a.go", "src/b.go"}},
		{"positive label own marker", "## Owned files\nOwned: - src/ui/app.go\n", []string{"src/ui/app.go"}},
		{"positive label emphasis pair", "## Owned files\nOwned: **src/ui/app.go**\n", []string{"src/ui/app.go"}},
		{"emphasized label", "## Owned files\n**Owned:** src/a.go\n", []string{"src/a.go"}},
		{"positive label globs kept", "## Owned files\nOwned: src/**\nMay edit: **/x.go\n", []string{"src/**", "**/x.go"}},
		{"positive label none", "## Owned files\nOwned: **None.**\n", []string{}},
		{"positive label bare path", "## Owned files\n- Owned: src/ui/app.go\nPode tocar: src/b.go\nMay edit: arquivos de UI\n", []string{"src/ui/app.go", "src/b.go"}},
		{"fenced label", "## Owned files\n- `src/a.go`\n```md\n## example\nForbidden:\n```\n- `src/b.go`\n", []string{"src/a.go", "src/b.go"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ownedPaths(tc.body, nil); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ownedPaths=%q, want %q", got, tc.want)
			}
		})
	}
}

func TestOwnedLabelsVariants(t *testing.T) {
	for _, label := range []string{"Proibido:", "Forbidden:", "Não tocar:", "Nao tocar:", "Do not touch:", "Fora do escopo:", "Out of scope:"} {
		t.Run("negative "+label, func(t *testing.T) {
			body := "## Owned files\n- `src/a.go`\n" + label + " `src/no.go`\n- `src/no2.go`\n"
			if got := ownedPaths(body, nil); !reflect.DeepEqual(got, []string{"src/a.go"}) {
				t.Fatalf("ownedPaths=%q", got)
			}
		})
	}
	for _, label := range []string{"Pode tocar:", "Owned:", "May edit:", "Arquivos próprios:", "Arquivos de posse:"} {
		t.Run("positive "+label, func(t *testing.T) {
			body := "## Owned files\nForbidden:\n- `src/no.go`\n" + label + " `src/a.go`\n"
			if got := ownedPaths(body, nil); !reflect.DeepEqual(got, []string{"src/a.go"}) {
				t.Fatalf("ownedPaths=%q", got)
			}
		})
	}
}

func TestOwnedLabelsOverlap(t *testing.T) {
	mine := func(string) string { return "Pode tocar:\n- `src/ui/**`\nProibido:\n- `src/**`" }
	no := dja661OverlapFixture(t, mine, func(string) string { return "- `src/connection/ssl.rs`" })
	if strings.Contains(no, "owns files") {
		t.Fatalf("false warning: %s", no)
	}
	yes := dja661OverlapFixture(t, mine, func(string) string { return "- `src/ui/app.go`" })
	if !strings.Contains(yes, "owns files") {
		t.Fatalf("missing control warning: %s", yes)
	}
	// Review R377: a bare path after a positive label is owned too.
	bare := dja661OverlapFixture(t, func(string) string { return "- Owned: src/ui/app.go" }, func(string) string { return "- `src/ui/app.go`" })
	if !strings.Contains(bare, "owns files") {
		t.Fatalf("missing warning for a bare path after a positive label: %s", bare)
	}
}

// F1, F2 and F3: the overlap reader calls the same plain and backticked token
// decisions as the audit and the lint, and reads the brief's declared tree for
// an unshaped backticked name.
func TestOwnedPathsSharedTokenDecision(t *testing.T) {
	// F1: '?' is a glob character, so a root-level wildcard crosses the file
	// it matches even beside another path.
	glob := dja661OverlapFixture(t,
		func(string) string { return "`tool?` and `go.mod`" },
		func(string) string { return "- `tool1`" })
	if !strings.Contains(glob, "owns files that 'peer' is still editing: tool?") {
		t.Fatalf("question glob did not cross tool1: %s", glob)
	}
	// F2: a plain token keeps the case-insensitive extension grammar.
	upper := dja661OverlapFixture(t,
		func(string) string { return "- NOTES.MD" },
		func(string) string { return "- NOTES.MD" })
	if !strings.Contains(upper, "owns files that 'peer' is still editing: NOTES.MD") {
		t.Fatalf("plain uppercase path did not cross: %s", upper)
	}
	// F3: a real root entry listed beside another path is owned through the
	// tree the brief declares; the same item without that entry owns only the
	// path-shaped token.
	withEntry := dja661OverlapFixture(t,
		func(root string) string {
			if err := os.WriteFile(filepath.Join(root, "Dockerfile"), []byte("fixture\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return "`Dockerfile` and `go.mod`"
		},
		func(string) string { return "- `Dockerfile`" })
	if !strings.Contains(withEntry, "owns files that 'peer' is still editing: Dockerfile") {
		t.Fatalf("a real root entry did not cross: %s", withEntry)
	}
	without := dja661OverlapFixture(t,
		func(string) string { return "`ai.openChat` and `go.mod`" },
		func(string) string { return "- ai.openChat" })
	if strings.Contains(without, "owns files") {
		t.Fatalf("an identifier without a root entry crossed: %s", without)
	}
	// The root lookup reads the tree it is given, never the caller's own
	// directory: the same item owns Dockerfile only under the tree that has it.
	rootWith, rootWithout := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(rootWith, "Dockerfile"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := "## Owned files\n- `Dockerfile` and `go.mod`\n"
	if got := ownedPathsIn(body, nil, rootWith, false); !reflect.DeepEqual(got, []string{"Dockerfile", "go.mod"}) {
		t.Fatalf("ownedPathsIn with the entry = %q, want [Dockerfile go.mod]", got)
	}
	if got := ownedPathsIn(body, nil, rootWithout, false); !reflect.DeepEqual(got, []string{"go.mod"}) {
		t.Fatalf("ownedPathsIn without the entry = %q, want [go.mod]", got)
	}
	if got := ownedPathsIn(body, nil, "", false); !reflect.DeepEqual(got, []string{"go.mod"}) {
		t.Fatalf("ownedPathsIn without a tree = %q, want [go.mod]", got)
	}
}
