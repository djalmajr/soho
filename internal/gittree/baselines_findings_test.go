package gittree_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

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

func TestBaselineDirtyGitlinkChangesCommit(t *testing.T) {
	f := newDiffFixture(t, "# Owned files\n- src/\n", "## Files changed\n- `src/b.go`\n", "implementer")
	sub := filepath.Join(filepath.Dir(f.tree), "sub source")
	if err := os.MkdirAll(sub, 0700); err != nil {
		t.Fatal(err)
	}
	diffFixtureGit(t, f, "init", sub)
	diffWrite(t, filepath.Join(sub, "file"), "one\n")
	diffFixtureGit(t, f, "-C", sub, "add", "file")
	diffFixtureGit(t, f, "-C", sub, "commit", "-m", "one")
	diffWrite(t, filepath.Join(sub, "file"), "two\n")
	diffFixtureGit(t, f, "-C", sub, "commit", "-am", "two")
	a := diffFixtureGit(t, f, "-C", sub, "rev-parse", "HEAD")
	diffWrite(t, filepath.Join(sub, "file"), "three\n")
	diffFixtureGit(t, f, "-C", sub, "commit", "-am", "three")
	b := diffFixtureGit(t, f, "-C", sub, "rev-parse", "HEAD")
	diffFixtureGit(t, f, "-C", sub, "checkout", "--detach", "HEAD~2")
	diffFixtureGit(t, f, "-c", "protocol.file.allow=always", "submodule", "add", sub, "module")
	diffFixtureGit(t, f, "commit", "-am", "add module")
	diffFixtureGit(t, f, "-C", filepath.Join(f.tree, "module"), "checkout", "--detach", a)
	gittree.Preserve(f.sidecar, gittree.Snapshot(f.env, f.tree))
	diffFixtureGit(t, f, "-C", filepath.Join(f.tree, "module"), "checkout", "--detach", b)
	diffWrite(t, filepath.Join(f.tree, "src/b.go"), "worker\n")
	value, _, _ := f.wait(t)
	if !reflect.DeepEqual(value["outside_owned"], []any{"module"}) {
		t.Fatalf("gitlink change missing: %v", value)
	}
}

func TestBaselineDirtyGitlinkRootMtime(t *testing.T) {
	for _, touch := range []bool{false, true} {
		name := "untouched"
		if touch {
			name = "new root file"
		}
		t.Run(name, func(t *testing.T) {
			f := newDiffFixture(t, "# Owned files\n- src/\n", "## Files changed\n- `src/b.go`\n", "implementer")
			sub := filepath.Join(filepath.Dir(f.tree), "sub source")
			if err := os.MkdirAll(sub, 0700); err != nil {
				t.Fatal(err)
			}
			diffFixtureGit(t, f, "init", sub)
			diffWrite(t, filepath.Join(sub, "file"), "one\n")
			diffFixtureGit(t, f, "-C", sub, "add", "file")
			diffFixtureGit(t, f, "-C", sub, "commit", "-m", "one")
			diffWrite(t, filepath.Join(sub, "file"), "two\n")
			diffFixtureGit(t, f, "-C", sub, "commit", "-am", "two")
			a := diffFixtureGit(t, f, "-C", sub, "rev-parse", "HEAD")
			diffFixtureGit(t, f, "-C", sub, "checkout", "--detach", "HEAD~1")
			diffFixtureGit(t, f, "-c", "protocol.file.allow=always", "submodule", "add", sub, "module")
			diffFixtureGit(t, f, "commit", "-am", "add module")
			module := filepath.Join(f.tree, "module")
			diffFixtureGit(t, f, "-C", module, "checkout", "--detach", a)
			diffWrite(t, filepath.Join(module, "file"), "already dirty\n")
			old := time.Unix(1700000000, 0)
			if err := os.Chtimes(module, old, old); err != nil {
				t.Fatal(err)
			}
			gittree.Preserve(f.sidecar, gittree.Snapshot(f.env, f.tree))
			if touch {
				diffWrite(t, filepath.Join(module, "new"), "worker\n")
			}
			diffWrite(t, filepath.Join(f.tree, "src/b.go"), "worker\n")
			value, _, _ := f.wait(t)
			if touch && !reflect.DeepEqual(value["outside_owned"], []any{"module"}) {
				t.Fatalf("new root file missing: %v", value)
			}
			if !touch && value["outside_owned"] != nil {
				t.Fatalf("untouched gitlink reported: %v", value)
			}
		})
	}
}

func TestIncompleteMirrorDefersPermanentAuditAndPrefersSource(t *testing.T) {
	f := newDiffFixture(t, "# Owned files\n- src/\n", "## Files changed\n- `src/b.go`\n", "implementer")
	gittree.Preserve(f.sidecar, gittree.Snapshot(f.env, f.tree))
	snapshot, err := os.ReadFile(f.sidecar + ".snapshot")
	if err != nil {
		t.Fatal(err)
	}
	side, err := os.ReadFile(f.sidecar)
	if err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(filepath.Dir(f.tree), "scratch")
	report := filepath.Join(scratch, "worker-fixture.md")
	source := filepath.Join(scratch, "worker-fixture.dispatch.json")
	diffWrite(t, report, "## Files changed\n- `src/b.go`\n")
	diffWrite(t, source, string(side))
	diffWrite(t, source+".snapshot", string(snapshot))
	if err := os.Remove(f.sidecar + ".snapshot"); err != nil {
		t.Fatal(err)
	}
	if got := gittree.Sidecar(f.state, report); got != source {
		t.Fatalf("source not preferred: %s", got)
	}
	if err := os.Remove(source + ".snapshot"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(f.tree, "README.md")); err != nil {
		t.Fatal(err)
	}
	diffWrite(t, filepath.Join(f.tree, "outside-new.txt"), "worker\n")
	fallback := gittree.DiffReport(gittree.Sidecar(f.state, report), "worker", report, f.env, "", "wait")
	if fallback == nil {
		t.Fatal("mtime fallback did not report changed file")
	}
	outsideFallback, _ := fallback.Get("outside_owned")
	if !reflect.DeepEqual(outsideFallback, []string{"outside-new.txt"}) {
		t.Fatalf("fallback outside=%v", outsideFallback)
	}
	if err := os.Remove(filepath.Join(f.tree, "outside-new.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.sidecar + ".diff-checked"); !os.IsNotExist(err) {
		t.Fatalf("incomplete audit consumed marker: %v", err)
	}
	if err := os.WriteFile(f.sidecar+".snapshot", snapshot, 0600); err != nil {
		t.Fatal(err)
	}
	result := gittree.DiffReport(gittree.Sidecar(f.state, report), "worker", report, f.env, "", "wait")
	if result == nil {
		t.Fatal("completed snapshot did not audit deletion")
	}
	outside, _ := result.Get("outside_owned")
	if !reflect.DeepEqual(outside, []string{"README.md"}) {
		t.Fatalf("outside=%v", outside)
	}
}
