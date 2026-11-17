package gittree_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/gittree"
	"github.com/djalmajr/soho/internal/jsonjs"
)

func TestBaselineModeOnlyChange(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("executable mode requires a filesystem with executable bits")
	}
	f := newDiffFixture(t, "# Owned files\n- src/\n", "## Files changed\n- `src/b.go`\n", "implementer")
	name := filepath.Join(f.tree, "README.md")
	diffWrite(t, name, "already dirty\n")
	gittree.Preserve(f.sidecar, gittree.Snapshot(f.env, f.tree))
	if err := os.Chmod(name, 0755); err != nil {
		t.Fatal(err)
	}
	diffWrite(t, filepath.Join(f.tree, "src/b.go"), "worker\n")
	value, _, _ := f.wait(t)
	if !reflect.DeepEqual(value["outside_owned"], []any{"README.md"}) {
		t.Fatalf("mode change missing: %v", value)
	}
}

func TestBaselineDirectoryDoesNotDiscardOtherPaths(t *testing.T) {
	f := newDiffFixture(t, "# Owned files\n- src/b.go\n", "## Files changed\n- `src/b.go`\n", "implementer")
	name := filepath.Join(f.tree, "README.md")
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	diffWrite(t, filepath.Join(name, "local"), "local\n")
	fields := gittree.Snapshot(f.env, f.tree)
	if _, ok := fields.Get("git_snapshot"); !ok {
		t.Fatal("directory discarded snapshot")
	}
	gittree.Preserve(f.sidecar, fields)
	if err := os.Remove(filepath.Join(f.tree, "src/a.go")); err != nil {
		t.Fatal(err)
	}
	diffWrite(t, filepath.Join(f.tree, "src/b.go"), "worker\n")
	value, _, _ := f.wait(t)
	if !reflect.DeepEqual(value["outside_owned"], []any{"src/a.go"}) {
		t.Fatalf("deletion missing: %v", value)
	}
}

func TestBaselineCommittedChangeNotAttributed(t *testing.T) {
	f := newDiffFixture(t, "# Owned files\n- src/\n", "## Files changed\n- `src/b.go`\n", "implementer")
	name := filepath.Join(f.tree, "README.md")
	diffWrite(t, name, "A\n")
	gittree.Preserve(f.sidecar, gittree.Snapshot(f.env, f.tree))
	diffWrite(t, name, "B\n")
	diffFixtureGit(t, f, "add", "README.md")
	diffFixtureGit(t, f, "commit", "-m", "orchestrator")
	diffWrite(t, filepath.Join(f.tree, "src/b.go"), "worker\n")
	value, _, _ := f.wait(t)
	if !reflect.DeepEqual(value["committed_during_task"], []any{"README.md"}) || value["outside_owned"] != nil {
		t.Fatalf("attribution: %v", value)
	}
}

func TestBaselineMixedArtifactIncludesRemovedUntracked(t *testing.T) {
	f := newDiffFixture(t, "# Owned files\n- src/\n", "## Files changed\n- `src/b.go`\n", "implementer")
	name := filepath.Join(f.tree, "scratch.txt")
	diffWrite(t, name, "old\n")
	gittree.Preserve(f.sidecar, gittree.Snapshot(f.env, f.tree))
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	diffWrite(t, filepath.Join(f.tree, "src/b.go"), "worker\n")
	value, _, _ := f.wait(t)
	if !reflect.DeepEqual(value["outside_owned"], []any{"scratch.txt"}) {
		t.Fatalf("outside: %v", value)
	}
	data, err := os.ReadFile(value["diff_stat"].(string))
	if err != nil || !strings.Contains(string(data), "scratch.txt") || !strings.Contains(string(data), "src/b.go") {
		t.Fatalf("artifact=%q err=%v", data, err)
	}
}

func TestInlineSnapshotRemainsReadable(t *testing.T) {
	f := newDiffFixture(t, "# Owned files\n- src/\n", "## Files changed\n- `src/b.go`\n", "implementer")
	name := filepath.Join(f.tree, "README.md")
	diffWrite(t, name, "earlier\n")
	fields := gittree.Snapshot(f.env, f.tree)
	snapshot, ok := fields.Get("git_snapshot")
	if !ok {
		t.Fatal("missing fixture snapshot")
	}
	raw, err := os.ReadFile(f.sidecar)
	if err != nil {
		t.Fatal(err)
	}
	var side map[string]any
	if err := json.Unmarshal(raw, &side); err != nil {
		t.Fatal(err)
	}
	encoded := jsonjs.Stringify(snapshot)
	var inline any
	if err := json.Unmarshal([]byte(encoded), &inline); err != nil {
		t.Fatal(err)
	}
	side["git_snapshot"] = inline
	raw, err = json.Marshal(side)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.sidecar, raw, 0600); err != nil {
		t.Fatal(err)
	}
	diffWrite(t, name, "worker changed\n")
	diffWrite(t, filepath.Join(f.tree, "src/b.go"), "worker\n")
	value, _, _ := f.wait(t)
	if !reflect.DeepEqual(value["outside_owned"], []any{"README.md"}) {
		t.Fatalf("inline snapshot not read: %v", value)
	}
}
