package gittree_test

import (
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/djalmajr/soho/internal/gittree"
)

func TestSecondTaskBaselines(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		again, inherit, snapshot bool
		outside, cited           []any
	}{
		{"earlier only", false, false, true, nil, []any{"src/a.go"}},
		{"changed again", true, false, true, []any{"README.md"}, []any{"src/a.go"}},
		{"inherited", true, true, true, nil, []any{"src/a.go"}},
		{"old sidecar", true, false, false, []any{"README.md"}, []any{"src/a.go"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owned := "# Owned files\n- src/b.go\n"
			if tc.inherit {
				owned += "- inherit: previous brief\n"
			}
			f := newDiffFixture(t, owned, "## Files changed\n- `README.md`\n- `src/a.go`\n- `src/b.go`\n", "implementer")
			previous := filepath.Join(f.state, "briefs", "worker-previous.dispatch.json")
			diffWrite(t, previous, fmt.Sprintf(`{"git_tree":%q,"git_head":%q,"role":"implementer"}`, f.tree, f.head))
			diffWrite(t, filepath.Join(f.state, "briefs", "worker-previous.md"), "# Brief\n## Owned files\n- README.md\n# Report contract\n")
			diffWrite(t, filepath.Join(f.tree, "README.md"), "round one\n")
			if tc.snapshot {
				fields := gittree.Snapshot(f.env, f.tree)
				if fields == nil {
					t.Fatal("snapshot unavailable")
				}
				patterns, ok := gittree.PreviousOwned(previous, "worker", f.tree)
				if !ok {
					t.Fatal("previous scope unavailable")
				}
				fields.Set("git_previous_owned", patterns)
				gittree.Preserve(f.sidecar, fields)
			}
			if tc.again {
				diffWrite(t, filepath.Join(f.tree, "README.md"), "round two\n")
			}
			diffWrite(t, filepath.Join(f.tree, "src", "b.go"), "round two\n")
			value, _, _ := f.wait(t)
			if tc.outside == nil && value["outside_owned"] != nil || tc.outside != nil && !reflect.DeepEqual(value["outside_owned"], tc.outside) {
				t.Errorf("outside=%v want %v", value["outside_owned"], tc.outside)
			}
			if !reflect.DeepEqual(value["cited_unchanged"], tc.cited) {
				t.Errorf("cited=%v want %v", value["cited_unchanged"], tc.cited)
			}
		})
	}
}
