package validation

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestParse covers the "## Test layers" grammar: the three accepted forms,
// the invalid cases (brief DJA-695 fatia 2, scenario 1), and the section
// boundaries (fences, other sections, CRLF, section at the end, absent
// section).
func TestParse(t *testing.T) {
	tests := []struct {
		name          string
		text          string
		wantHas       bool
		wantLayers    []Layer
		wantMalformed []int
	}{
		{
			name: "the three forms",
			text: "## Test layers\n" +
				"- unit: `go test -timeout=30m ./...`\n" +
				"- integration: n/a: no separate integration layer\n" +
				"- e2e: manual: the procedure of the docs, with the candidate binary\n",
			wantHas: true,
			wantLayers: []Layer{
				{Name: "unit", Kind: "command", Value: "go test -timeout=30m ./...", Line: 2},
				{Name: "integration", Kind: "na", Value: "no separate integration layer", Line: 3},
				{Name: "e2e", Kind: "manual", Value: "the procedure of the docs, with the candidate binary", Line: 4},
			},
		},
		{
			name: "invalid layer names are malformed",
			text: "## Test layers\n" +
				"- Unit: `go test`\n" +
				"- unit_x: `go test`\n" +
				"- unit: go test\n",
			wantHas:       true,
			wantMalformed: []int{2, 3, 4},
		},
		{
			name: "empty n/a reason and empty manual text are malformed",
			text: "## Test layers\n" +
				"- integration: n/a:\n" +
				"- contract: n/a:    \n" +
				"- e2e: manual:\n",
			wantHas:       true,
			wantMalformed: []int{2, 3, 4},
		},
		{
			name: "duplicated layer: the second is malformed",
			text: "## Test layers\n" +
				"- unit: `go test`\n" +
				"- unit: `go vet`\n",
			wantHas:       true,
			wantLayers:    []Layer{{Name: "unit", Kind: "command", Value: "go test", Line: 2}},
			wantMalformed: []int{3},
		},
		{
			name: "an inner backtick makes the command malformed",
			text: "## Test layers\n" +
				"- e2e: `a`b` | c`\n",
			wantHas:       true,
			wantMalformed: []int{2},
		},
		{
			name: "text after the last backtick is malformed",
			text: "## Test layers\n" +
				"- e2e: `go test` trailing\n",
			wantHas:       true,
			wantMalformed: []int{2},
		},
		{
			name: "text before the first backtick is malformed",
			text: "## Test layers\n" +
				"- e2e: lead `go test`\n",
			wantHas:       true,
			wantMalformed: []int{2},
		},
		{
			name: "a backtick inside the command is malformed",
			text: "## Test layers\n" +
				"- e2e: `a`b`\n",
			wantHas:       true,
			wantMalformed: []int{2},
		},
		{
			name: "a whitespace-only command is malformed",
			text: "## Test layers\n" +
				"- e2e: `   `\n",
			wantHas:       true,
			wantMalformed: []int{2},
		},
		{
			name: "spaces around the command inside the backticks are trimmed",
			text: "## Test layers\n" +
				"- e2e: ` go test ` \n",
			wantHas:    true,
			wantLayers: []Layer{{Name: "e2e", Kind: "command", Value: "go test", Line: 2}},
		},
		{
			name: "a pipe inside the command is valid",
			text: "## Test layers\n" +
				"- e2e: `go test | tee log`\n",
			wantHas:    true,
			wantLayers: []Layer{{Name: "e2e", Kind: "command", Value: "go test | tee log", Line: 2}},
		},
		{
			name: "a fenced heading inside the section does not end it",
			text: "## Test layers\n" +
				"- unit: `go test`\n" +
				"```\n" +
				"## Example\n" +
				"- fake: `no`\n" +
				"```\n" +
				"- integration: n/a: after the fence\n",
			wantHas:    true,
			wantLayers: []Layer{{Name: "unit", Kind: "command", Value: "go test", Line: 2}, {Name: "integration", Kind: "na", Value: "after the fence", Line: 7}},
		},
		{
			name: "a fenced title before the real section does not open it",
			text: "## Scope\n" +
				"```\n" +
				"## Test layers\n" +
				"- fake: `example`\n" +
				"```\n" +
				"## Test layers\n" +
				"- unit: `go test`\n",
			wantHas:    true,
			wantLayers: []Layer{{Name: "unit", Kind: "command", Value: "go test", Line: 7}},
		},
		{
			name: "an open fence that never closes fences the rest of the file",
			text: "## Test layers\n" +
				"- unit: `go test`\n" +
				"```\n" +
				"- integration: n/a: never declared\n",
			wantHas:    true,
			wantLayers: []Layer{{Name: "unit", Kind: "command", Value: "go test", Line: 2}},
		},
		{
			name: "a tilde fence hides an example title",
			text: "" +
				"~~~\n" +
				"## Test layers\n" +
				"- e2e: `example`\n" +
				"~~~\n" +
				"## Test layers\n" +
				"- unit: `go test`\n",
			wantHas:    true,
			wantLayers: []Layer{{Name: "unit", Kind: "command", Value: "go test", Line: 6}},
		},
		{
			name: "an indented fence hides an example title",
			text: "" +
				"  ```\n" +
				"## Test layers\n" +
				"- e2e: `example`\n" +
				"  ```\n" +
				"## Test layers\n" +
				"- unit: `go test`\n",
			wantHas:    true,
			wantLayers: []Layer{{Name: "unit", Kind: "command", Value: "go test", Line: 6}},
		},
		{
			name: "a shorter fence line does not close a longer fence",
			text: "" +
				"````\n" +
				"```\n" +
				"## Test layers\n" +
				"- e2e: `example`\n" +
				"```\n" +
				"````\n" +
				"## Test layers\n" +
				"- unit: `go test`\n",
			wantHas:    true,
			wantLayers: []Layer{{Name: "unit", Kind: "command", Value: "go test", Line: 8}},
		},
		{
			name: "a closing line with text after it does not close the fence",
			text: "" +
				"```\n" +
				"``` not a close\n" +
				"## Test layers\n" +
				"- e2e: `example`\n" +
				"```\n" +
				"## Test layers\n" +
				"- unit: `go test`\n",
			wantHas:    true,
			wantLayers: []Layer{{Name: "unit", Kind: "command", Value: "go test", Line: 7}},
		},
		{
			name: "the other fence character does not close the fence",
			text: "" +
				"```\n" +
				"~~~\n" +
				"## Test layers\n" +
				"- e2e: `example`\n" +
				"```\n" +
				"## Test layers\n" +
				"- unit: `go test`\n",
			wantHas:    true,
			wantLayers: []Layer{{Name: "unit", Kind: "command", Value: "go test", Line: 7}},
		},
		{
			name: "a fence opened with an info string still hides the title",
			text: "" +
				"```markdown\n" +
				"## Test layers\n" +
				"- e2e: `example`\n" +
				"```\n" +
				"## Test layers\n" +
				"- unit: `go test`\n",
			wantHas:    true,
			wantLayers: []Layer{{Name: "unit", Kind: "command", Value: "go test", Line: 6}},
		},
		{
			name: "four spaces of indent do not open a fence",
			text: "## Test layers\n" +
				"    ```\n" +
				"- unit: `go test`\n",
			wantHas:    true,
			wantLayers: []Layer{{Name: "unit", Kind: "command", Value: "go test", Line: 3}},
		},
		{
			name: "a backtick in the info string does not open a backtick fence",
			text: "## Test layers\n" +
				"``` a`b\n" +
				"- unit: `go test`\n",
			wantHas:    true,
			wantLayers: []Layer{{Name: "unit", Kind: "command", Value: "go test", Line: 3}},
		},
		{
			name: "a non-breaking space after the delimiter does not close the fence",
			text: "```\n" +
				"```\u00a0\n" +
				"## Test layers\n" +
				"- e2e: `example`\n" +
				"```\n" +
				"## Test layers\n" +
				"- unit: `go test`\n",
			wantHas:    true,
			wantLayers: []Layer{{Name: "unit", Kind: "command", Value: "go test", Line: 7}},
		},
		{
			name: "CRLF line endings parse like LF",
			text: "## Test layers\r\n" +
				"- unit: `go test ./...`\r\n" +
				"- integration: n/a: no separate integration layer\r\n" +
				"- e2e: manual: the procedure of the docs\r\n",
			wantHas: true,
			wantLayers: []Layer{
				{Name: "unit", Kind: "command", Value: "go test ./...", Line: 2},
				{Name: "integration", Kind: "na", Value: "no separate integration layer", Line: 3},
				{Name: "e2e", Kind: "manual", Value: "the procedure of the docs", Line: 4},
			},
		},
		{
			name: "a list item inside a code fence is ignored",
			text: "## Test layers\n" +
				"- unit: `go test`\n" +
				"```\n" +
				"- fake: `should not count`\n" +
				"```\n" +
				"- integration: n/a: fenced\n",
			wantHas:    true,
			wantLayers: []Layer{{Name: "unit", Kind: "command", Value: "go test", Line: 2}, {Name: "integration", Kind: "na", Value: "fenced", Line: 6}},
		},
		{
			name: "list items in other sections are ignored",
			text: "## Scope\n" +
				"- pre: `before the title`\n" +
				"## Test layers\n" +
				"- unit: `go test`\n" +
				"## Next\n" +
				"- post: `after the section`\n",
			wantHas:    true,
			wantLayers: []Layer{{Name: "unit", Kind: "command", Value: "go test", Line: 4}},
		},
		{
			name:          "absent section",
			text:          "# Validation\n\n## Scope\n\nno layers here\n\n## Items\n",
			wantHas:       false,
			wantMalformed: nil,
		},
		{
			name:          "a different level is not the section",
			text:          "### test layers\n- unit: `go test`\n### Test layers\n- unit: `go test`\n",
			wantHas:       false,
			wantMalformed: nil,
		},
		{
			name:       "section at the end of the file",
			text:       "## Test layers\n- unit: `go test`",
			wantHas:    true,
			wantLayers: []Layer{{Name: "unit", Kind: "command", Value: "go test", Line: 2}},
		},
		{
			name: "an empty command is malformed",
			text: "## Test layers\n" +
				"- unit: ``\n" +
				"- integration: `go test`\n",
			wantHas:       true,
			wantLayers:    []Layer{{Name: "integration", Kind: "command", Value: "go test", Line: 3}},
			wantMalformed: []int{2},
		},
		{
			name: "a single backtick is malformed",
			text: "## Test layers\n" +
				"- unit: `go test\n" +
				"- integration: n/a: reason\n",
			wantHas:       true,
			wantLayers:    []Layer{{Name: "integration", Kind: "na", Value: "reason", Line: 3}},
			wantMalformed: []int{2},
		},
		{
			name: "a non-list line that looks like a layer is ignored",
			text: "## Test layers\n" +
				"unit: `go test`\n" +
				"- integration: n/a: reason\n",
			wantHas:    true,
			wantLayers: []Layer{{Name: "integration", Kind: "na", Value: "reason", Line: 3}},
		},
		{
			name: "a malformed first occurrence does not block a later valid one",
			text: "## Test layers\n" +
				"- unit: `\n" +
				"- unit: `go test`\n",
			wantHas:       true,
			wantLayers:    []Layer{{Name: "unit", Kind: "command", Value: "go test", Line: 3}},
			wantMalformed: []int{2},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Parse(tt.text)
			if got.HasLayers != tt.wantHas {
				t.Fatalf("HasLayers = %v, want %v", got.HasLayers, tt.wantHas)
			}
			if !reflect.DeepEqual(got.Layers, tt.wantLayers) {
				t.Fatalf("Layers = %+v, want %+v", got.Layers, tt.wantLayers)
			}
			if !reflect.DeepEqual(got.Malformed, tt.wantMalformed) {
				t.Fatalf("Malformed = %v, want %v", got.Malformed, tt.wantMalformed)
			}
		})
	}
}

// TestIsPlaceholder reports which values still carry the template marks.
func TestIsPlaceholder(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{`<command>`, true},
		{`go test <command> extra`, true},
		{`<reason>`, true},
		{`<what runs it>`, true},
		{`go test ./...`, false},
		{`no separate integration layer`, false},
		{`the procedure of the docs`, false},
		{``, false},
	}
	for _, tt := range tests {
		if got := IsPlaceholder(tt.value); got != tt.want {
			t.Errorf("IsPlaceholder(%q) = %v, want %v", tt.value, got, tt.want)
		}
	}
}

// TestRepoValidationHasNoMalformedPlaceholderOrMissingLayer parses the
// repository's own VALIDATION.md (brief DJA-695 fatia 2, scenario 2): the
// file is filled, so it carries no malformed line, no placeholder and no
// missing required layer.
func TestRepoValidationHasNoMalformedPlaceholderOrMissingLayer(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "VALIDATION.md"))
	if err != nil {
		t.Fatalf("reading the repository VALIDATION.md: %v", err)
	}
	doc := Parse(string(raw))
	if !doc.HasLayers {
		t.Fatal("the repository VALIDATION.md lost its \"## Test layers\" section")
	}
	if len(doc.Malformed) != 0 {
		t.Fatalf("the repository VALIDATION.md has malformed layer lines: %v", doc.Malformed)
	}
	for _, layer := range doc.Layers {
		if IsPlaceholder(layer.Value) {
			t.Errorf("layer %s (line %d) still has the template placeholder", layer.Name, layer.Line)
		}
	}
	declared := make(map[string]bool, len(doc.Layers))
	for _, layer := range doc.Layers {
		declared[layer.Name] = true
	}
	for _, name := range RequiredLayers {
		if !declared[name] {
			t.Errorf("required layer %s is not declared", name)
		}
	}
}

// TestParseSectionTitles covers the titles that open the layers section: the
// exact English title and the pt-BR title in any ASCII letter case. A fenced
// title, another title (including a lookalike and an English title in
// another case) and a missing title do not open it.
func TestParseSectionTitles(t *testing.T) {
	body := "- unit: `go test`\n- integration: n/a: none\n"
	tests := []struct {
		name       string
		title      string
		wantHas    bool
		wantLayers int
	}{
		{"English title", "## Test layers", true, 2},
		{"pt-BR title", "## Camadas de teste", true, 2},
		{"pt-BR title in lower case", "## camadas de teste", true, 2},
		{"pt-BR title in upper case", "## CAMADAS DE TESTE", true, 2},
		{"English title in another case", "## test layers", true, 2},
		{"translated heading with items", "## 1. Camadas de teste", true, 2},
		{"numbered English heading", "## 2. Test layers", true, 2},
		{"pt-BR title with a different level", "### Camadas de teste", false, 0},
		{"another heading with items", "## Items", true, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := Parse("# Validation\n\n" + tt.title + "\n" + body)
			if doc.HasLayers != tt.wantHas || len(doc.Layers) != tt.wantLayers {
				t.Fatalf("HasLayers=%v layers=%d, want %v and %d", doc.HasLayers, len(doc.Layers), tt.wantHas, tt.wantLayers)
			}
		})
	}
	t.Run("a fenced pt-BR title opens nothing", func(t *testing.T) {
		doc := Parse("```\n## Camadas de teste\n" + body + "```\n")
		if doc.HasLayers {
			t.Fatal("a fenced title opened the section")
		}
	})
	t.Run("the pt-BR section ends at the next title", func(t *testing.T) {
		doc := Parse("## Camadas de teste\n" + body + "## Itens\n- e2e: `x`\n")
		if !doc.HasLayers || len(doc.Layers) != 2 {
			t.Fatalf("HasLayers=%v layers=%d, want true and 2", doc.HasLayers, len(doc.Layers))
		}
	})
}

func TestFallbackRequiresValidRequiredLayer(t *testing.T) {
	for _, line := range []string{"- note: `command`", "- unit: arbitrary", "- unit: n/a:", "- unit: manual:", "- unit: ``"} {
		if doc := Parse("## Notes\n" + line + "\n"); doc.HasLayers {
			t.Errorf("%q selected section: %+v", line, doc)
		}
	}
	for _, name := range RequiredLayers {
		for _, value := range []string{"`command`", "n/a: reason", "manual: procedure"} {
			doc := Parse("## Translated\n- " + name + ": " + value + "\n- custom: `check`\n")
			if !doc.HasLayers || len(doc.Layers) != 2 {
				t.Errorf("%s %s: %+v", name, value, doc)
			}
		}
	}
	doc := Parse("## Notes\n- unit: `earlier`\n## 2. Test layers\n- unit: `later`\n")
	if len(doc.Layers) != 1 || doc.Layers[0].Value != "later" {
		t.Fatalf("recognized title did not win: %+v", doc)
	}
}
