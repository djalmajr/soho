package cli

import (
	"reflect"
	"strings"
	"testing"
)

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
