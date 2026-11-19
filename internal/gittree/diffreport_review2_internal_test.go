package gittree

// Review round 2 (forms-r-20261119T180825 F2): the settled audit applies the
// same positive and negative section labels the lint and the overlap reader
// know, and a negative label also disables the root-entry exception.

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
)

func TestDiffSectionNegativeLabelExcludesTokens(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Dockerfile"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Every negative spelling the lint knows excludes its own token, even
	// when the token is a real root entry that the exception would accept.
	for _, label := range diffOwnedNegativeLabels {
		for _, section := range []string{"Owned files", "Orchestrator files"} {
			t.Run(label+"/"+section, func(t *testing.T) {
				brief := "# Brief\n# " + section + "\n- " + label + ": `Dockerfile`\n# Report contract\n"
				got, found := diffSectionNames(brief, root, nil, platform.Current(), sharedSectionName(section))
				if !found {
					t.Fatalf("%s section not found", section)
				}
				if len(got) != 0 {
					t.Fatalf("negative label %q owned %q", label, got)
				}
			})
		}
	}
}

// F2: a positive label ends the exclusion, and the item it opens is owned.
func TestDiffSectionPositiveLabelReopensScope(t *testing.T) {
	brief := "# Brief\n# Owned files\n- Forbidden: `src/a.ts`\n- Owned: `src/b.ts`\n# Report contract\n"
	got, found := diffSectionNames(brief, "", nil, platform.Current(), "owned files")
	if want := []string{"src/b.ts"}; !found || !reflect.DeepEqual(got, want) {
		t.Fatalf("positive label reopened = %q (found=%t), want %q", got, found, want)
	}
}

// F1: the recorded root names answer the exception when the live tree no
// longer has the entry; without them the same item keeps the shape rule.
func TestDiffSectionRecordedRootNames(t *testing.T) {
	brief := "# Brief\n# Owned files\n- `Dockerfile` and `go.mod`\n# Report contract\n"
	got, found := diffSectionNames(brief, "", map[string]bool{"Dockerfile": true}, platform.Current(), "owned files")
	if want := []string{"Dockerfile", "go.mod"}; !found || !reflect.DeepEqual(got, want) {
		t.Fatalf("recorded root name = %q (found=%t), want %q", got, found, want)
	}
	if got, found := diffSectionNames(brief, "", nil, platform.Current(), "owned files"); !found || !reflect.DeepEqual(got, []string{"go.mod"}) {
		t.Fatalf("no recorded root name = %q (found=%t), want [go.mod]", got, found)
	}
}

// sharedSectionName converts a test heading into the section key diffSection
// compares against.
func sharedSectionName(title string) string {
	if title == "Orchestrator files" {
		return "orchestrator files"
	}
	return "owned files"
}

// F1: the audit's extra root names come from the recorded snapshot keys at
// the tree root only (a nested key is not a root entry), and a reader
// without auditRoots keeps the live-tree lookup alone.
func TestRecordedRootNamesSnapshotRoots(t *testing.T) {
	value, err := jsonjs.Parse([]byte(`{"git_tree":"/nonexistent/tree","git_head":"0123456789abcdef0123456789abcdef01234567","git_snapshot":{"Dockerfile":"file:x","src/a.go":"file:y"}}`))
	if err != nil {
		t.Fatal(err)
	}
	o, ok := value.(*jsonjs.Object)
	if !ok {
		t.Fatal("fixture is not an object")
	}
	seams := ownedReadSeams{auditRoots: true}
	if got, want := seams.recordedRootNames("", o, "/nonexistent/tree"), map[string]bool{"Dockerfile": true}; !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot root names = %v, want %v", got, want)
	}
	if got := (ownedReadSeams{}).recordedRootNames("", o, "/nonexistent/tree"); got != nil {
		t.Fatalf("a reader without auditRoots recorded %v", got)
	}
	if diffRootEntryName("src/a.go") || diffRootEntryName("vendor/") || !diffRootEntryName("vendor") {
		t.Fatal("root-entry predicate accepted a nested or empty name")
	}
}
