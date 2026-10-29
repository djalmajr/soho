package gittree_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/gittree"
)

// newNewTestsFixture is the diff fixture over a repository that also
// tracks a test file (y_test.go), so the new-tests Owned item can be
// exercised against a modified tracked test file as well as new
// untracked files.
func newNewTestsFixture(t *testing.T, owned string) *diffFixture {
	t.Helper()
	return newDiffFixtureFiles(t, owned, "Completed.\n", "implementer", map[string]string{
		"src/a.go":     "package a\n",
		"src/b.go":     "package b\n",
		"forbidden.go": "package forbidden\n",
		"README.md":    "fixture\n",
		"y_test.go":    "package root\n",
	})
}

// newTestsOutside runs the audit with one fresh fixture and the given
// Owned section, after the named new untracked files and the modified
// tracked test file, and returns the outside_owned value (nil when the
// audit set none).
func newTestsOutside(t *testing.T, owned string, newFiles ...string) any {
	t.Helper()
	f := newNewTestsFixture(t, owned)
	for _, name := range newFiles {
		diffWrite(t, filepath.Join(f.tree, filepath.FromSlash(name)), "package root\n")
	}
	diffWrite(t, filepath.Join(f.tree, "y_test.go"), "package root\n// changed\n")
	result := gittree.DiffReport(f.sidecar, "worker", f.report, f.env, "", "wait")
	if result == nil {
		t.Fatal("no audit result")
	}
	outside, _ := result.Get("outside_owned")
	return outside
}

// DJA-570 acceptance: with the prose item in Owned, the new untracked
// test file is not outside; the new production file and the modified
// tracked test file still are.
func TestDiffReportNewTestsOwnedItem(t *testing.T) {
	f := newNewTestsFixture(t, "# Owned files\n- new tests\n")
	diffWrite(t, filepath.Join(f.tree, "x_test.go"), "package root\n")
	diffWrite(t, filepath.Join(f.tree, "x.go"), "package root\n")
	diffWrite(t, filepath.Join(f.tree, "y_test.go"), "package root\n// changed\n")
	value, _, stderr := f.wait(t)
	if value["diff_stat"] == nil {
		t.Fatalf("diff_stat missing: %v", value)
	}
	artifact := strings.TrimSuffix(f.report, ".md") + ".diff-stat.txt"
	stat, err := os.ReadFile(artifact)
	if err != nil || !strings.Contains(string(stat), "x_test.go") || !strings.Contains(string(stat), "y_test.go") {
		t.Fatalf("artifact=%q err=%v; the covered test file stays in the diff stat", stat, err)
	}
	if !reflect.DeepEqual(value["outside_owned"], []any{"x.go", "y_test.go"}) {
		t.Fatalf("outside=%v; want x.go and y_test.go (x_test.go is covered by new tests)", value["outside_owned"])
	}
	if value["cited_unchanged"] != nil {
		t.Fatalf("unexpected cited=%v", value)
	}
	if !strings.Contains(stderr, "'worker' changed files outside its Owned files: x.go, y_test.go") || strings.Contains(stderr, "x_test.go") {
		t.Fatalf("stderr=%q", stderr)
	}
}

// The item's surface: the two phrases, case-insensitive, with an
// optional trailing period, each covering the new test file (the
// modified tracked test file stays outside in every case).
func TestDiffReportNewTestsOwnedItemVariants(t *testing.T) {
	for _, owned := range []string{
		"# Owned files\n- new test files\n",
		"# Owned files\n- NEW TESTS.\n",
		"# Owned files\n- New Test Files.\n",
		"# Owned files\n1. new tests\n",
	} {
		outside := newTestsOutside(t, owned, "x_test.go")
		if !reflect.DeepEqual(outside, []string{"y_test.go"}) {
			t.Fatalf("owned=%q outside=%v; the item must cover x_test.go and only y_test.go stays outside", owned, outside)
		}
	}
}

// The negative controls: without the item, with another prose item, and
// with the phrase backticked (a path item, not the prose item), the new
// test file stays outside; the covered and uncovered name sets match the
// contract.
func TestDiffReportNewTestsOwnedItemNegativeControls(t *testing.T) {
	for name, owned := range map[string]string{
		"without the item":      "# Owned files\n- src/*.go\n",
		"another prose item":    "# Owned files\n- new helpers\n",
		"the phrase backticked": "# Owned files\n- `new tests`\n",
		"a longer prose item":   "# Owned files\n- new tests plus the fixtures\n",
	} {
		t.Run(name, func(t *testing.T) {
			outside := newTestsOutside(t, owned, "x_test.go")
			if !reflect.DeepEqual(outside, []string{"x_test.go", "y_test.go"}) {
				t.Fatalf("outside=%v; want x_test.go and y_test.go", outside)
			}
		})
	}
	t.Run("the covered names", func(t *testing.T) {
		outside := newTestsOutside(t, "# Owned files\n- new tests\n",
			"a_test.go", "b.test.js", "c.spec.ts", "test_d.py", "e_test.py", "pkg/f_test.go")
		if !reflect.DeepEqual(outside, []string{"y_test.go"}) {
			t.Fatalf("outside=%v; every new test-file name must be covered", outside)
		}
	})
	t.Run("the uncovered names", func(t *testing.T) {
		outside := newTestsOutside(t, "# Owned files\n- new tests\n",
			"x.go", "test.py", "foo_test", "foo.test", "e_test.py.bak")
		want := []string{"e_test.py.bak", "foo.test", "foo_test", "test.py", "x.go", "y_test.go"}
		if !reflect.DeepEqual(outside, want) {
			t.Fatalf("outside=%v; want %v (only test-file names are covered)", outside, want)
		}
	})
}
