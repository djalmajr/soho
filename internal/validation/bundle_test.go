package validation

import (
	"os"
	"path/filepath"
	"testing"

	skill "github.com/djalmajr/soho/skills/soho"
)

// TestBundleValidationTemplateParsesAsFivePlaceholderLayers checks that the
// shipped template parses with the validation grammar: exactly the five
// required layers, every one still a placeholder, no malformed line
// (brief DJA-695 fatia 2, scenario 3). Moved from skills/soho/assets_test.go
// by the DJA-695 fatia 3 amendment: the test lives in internal/validation so
// the package can import internal/platform (the platform embeds the skill,
// and the skill's test importing this package was the import cycle). The
// template is read through the skill's bundled resources, materialized into
// a fresh cache.
func TestBundleValidationTemplateParsesAsFivePlaceholderLayers(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "skill-cache")
	root, err := skill.Materialize(cache)
	if err != nil {
		t.Fatalf("materialize the bundled skill: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "templates", "VALIDATION.md"))
	if err != nil {
		t.Fatalf("bundle is missing templates/VALIDATION.md: %v", err)
	}
	doc := Parse(string(data))
	if !doc.HasLayers {
		t.Fatal("template lost its \"## Test layers\" section")
	}
	if len(doc.Malformed) != 0 {
		t.Fatalf("template has malformed layer lines: %v", doc.Malformed)
	}
	if len(doc.Layers) != 5 {
		t.Fatalf("template declares %d layers, want 5: %+v", len(doc.Layers), doc.Layers)
	}
	declared := make(map[string]bool, len(doc.Layers))
	for _, layer := range doc.Layers {
		if !IsPlaceholder(layer.Value) {
			t.Errorf("template layer %s is not a placeholder: %q", layer.Name, layer.Value)
		}
		declared[layer.Name] = true
	}
	for _, name := range RequiredLayers {
		if !declared[name] {
			t.Errorf("template is missing the required layer %s", name)
		}
	}
}
