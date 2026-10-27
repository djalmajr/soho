package gittree_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Exercise actual changes after a real fresh/duplicate dispatch has created
// its composed brief, rather than testing an edit from a previous task.
func diffAfterCurrentDispatch(f *diffFixture, report string, outside bool) error {
	stem := strings.TrimSuffix(filepath.Base(report), ".md")
	var composed os.FileInfo
	for _, candidate := range []string{filepath.Join(filepath.Dir(report), "..", "briefs", stem+".md"), filepath.Join(filepath.Dir(report), stem+".brief.md")} {
		if info, err := os.Stat(candidate); err == nil {
			composed = info
			break
		}
	}
	if composed == nil {
		return fmt.Errorf("composed brief for %s not found", report)
	}
	files := map[string]string{"src/b.go": "changed after dispatch\n"}
	if outside {
		files["outside.txt"] = "modified after dispatch\n"
	}
	for name, body := range files {
		file := filepath.Join(f.tree, filepath.FromSlash(name))
		if err := os.WriteFile(file, []byte(body), 0600); err != nil {
			return err
		}
		when := composed.ModTime().Add(time.Second)
		if err := os.Chtimes(file, when, when); err != nil {
			return err
		}
	}
	return nil
}

func diffTime(t *testing.T, file string, when time.Time) {
	t.Helper()
	if err := os.Chtimes(file, when, when); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(file)
	if err != nil || !info.ModTime().Equal(when) {
		t.Fatalf("mtime file=%s got=%v err=%v want=%v", file, info, err, when)
	}
}

func TestDiffMtimeBeforeDispatchIgnored(t *testing.T) {
	for _, kind := range []string{"untracked", "tracked"} {
		t.Run(kind, func(t *testing.T) {
			f := newDiffFixture(t, "# Owned files\n- src/b.go\n", "Completed.\n", "implementer")
			brief := filepath.Join(f.state, "briefs", "worker-fixture.md")
			info, err := os.Stat(brief)
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(f.tree, "before.txt")
			if kind == "tracked" {
				file = filepath.Join(f.tree, "src", "a.go")
			}
			diffWrite(t, file, "previous round's change\n")
			diffTime(t, file, info.ModTime().Add(-time.Second))
			value, _, stderr := f.wait(t)
			if len(value) != 3 || stderr != "" {
				t.Fatalf("kind=%s before-dispatch file counted: %v stderr=%s", kind, value, stderr)
			}
			if _, err := os.Stat(strings.TrimSuffix(f.report, ".md") + ".diff-stat.txt"); !os.IsNotExist(err) {
				t.Fatalf("before file artifact=%v", err)
			}
			t.Logf("kind=%s file mtime < composed mtime; exit=0, no audit fields/artifact/warning", kind)
		})
	}
}

func TestDiffMtimeAfterDispatchReported(t *testing.T) {
	f := newDiffFixture(t, "# Owned files\n- src/\n", "Completed.\n", "implementer")
	info, err := os.Stat(filepath.Join(f.state, "briefs", "worker-fixture.md"))
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(f.tree, "after.txt")
	diffWrite(t, file, "new after dispatch\n")
	diffTime(t, file, info.ModTime().Add(time.Second))
	value, _, stderr := f.wait(t)
	if value["diff_stat"] == nil || !reflect.DeepEqual(value["outside_owned"], []any{"after.txt"}) || !strings.Contains(stderr, "outside its Owned files: after.txt") {
		t.Fatalf("after file omitted: %v stderr=%s", value, stderr)
	}
	t.Logf("untracked after-dispatch: outside_owned=%v diff_stat=%v", value["outside_owned"], value["diff_stat"])
}

func TestDiffMtimeEqualDispatchIgnored(t *testing.T) {
	f := newDiffFixture(t, "# Owned files\n- src/\n", "Completed.\n", "implementer")
	info, err := os.Stat(filepath.Join(f.state, "briefs", "worker-fixture.md"))
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(f.tree, "equal.txt")
	diffWrite(t, file, "same timestamp\n")
	diffTime(t, file, info.ModTime())
	value, _, stderr := f.wait(t)
	if len(value) != 3 || stderr != "" {
		t.Fatalf("non-strict boundary: %v stderr=%s", value, stderr)
	}
}

func TestDiffMtimeFiltersStatOwnedAndCitations(t *testing.T) {
	f := newDiffFixture(t, "# Owned files\n- src/b.go\n", "Completed `src/a.go` and `src/b.go`.\n", "implementer")
	info, err := os.Stat(filepath.Join(f.state, "briefs", "worker-fixture.md"))
	if err != nil {
		t.Fatal(err)
	}
	diffWrite(t, filepath.Join(f.tree, "src", "a.go"), "previous round\n")
	diffTime(t, filepath.Join(f.tree, "src", "a.go"), info.ModTime().Add(-time.Second))
	diffWrite(t, filepath.Join(f.tree, "src", "b.go"), "this round\n")
	diffTime(t, filepath.Join(f.tree, "src", "b.go"), info.ModTime().Add(time.Second))
	value, _, stderr := f.wait(t)
	statPath, _ := value["diff_stat"].(string)
	stat, err := os.ReadFile(statPath)
	if err != nil || strings.Contains(string(stat), "src/a.go") || !strings.Contains(string(stat), "src/b.go") || value["outside_owned"] != nil || !reflect.DeepEqual(value["cited_unchanged"], []any{"src/a.go"}) {
		t.Fatalf("filter mismatch value=%v stat=%q err=%v stderr=%s", value, stat, err, stderr)
	}
}

func TestDiffMtimeAmendmentUsesOriginalBrief(t *testing.T) {
	f := newDiffFixture(t, "# Owned files\n- src/*.go\n", "Original.\n", "implementer")
	info, err := os.Stat(filepath.Join(f.state, "briefs", "worker-fixture.md"))
	if err != nil {
		t.Fatal(err)
	}
	between := info.ModTime().Add(time.Second)
	diffWrite(t, filepath.Join(f.tree, "src", "a.go"), "changed between dispatch and amendment\n")
	diffTime(t, filepath.Join(f.tree, "src", "a.go"), between)
	stem := filepath.Join(f.state, "briefs", "worker-amended")
	diffWrite(t, stem+".md", "# Amendment\n# Owned files\n- src/*.go\n")
	diffTime(t, stem+".md", between.Add(time.Second))
	report := filepath.Join(f.state, "reports", "worker-amended.md")
	diffWrite(t, report, "Completed `src/a.go`.\n")
	side := map[string]any{"role": "implementer", "git_tree": f.tree, "git_head": f.head, "git_branch": "main", "task_report": filepath.Join(f.state, "reports", "worker-fixture.current.md")}
	data, err := json.Marshal(side)
	if err != nil {
		t.Fatal(err)
	}
	diffWrite(t, stem+".dispatch.json", string(data))
	diffWrite(t, filepath.Join(f.state, "last-report-worker"), report+"\n")
	value, _, stderr := f.wait(t)
	if value["diff_stat"] == nil || value["cited_unchanged"] != nil || stderr != "" {
		t.Fatalf("original cutoff lost: %v stderr=%s", value, stderr)
	}
	t.Log("change after original brief and before amendment included; amendment's newer mtime ignored")
}

func TestDiffMtimeLiteralPathDoesNotWidenStat(t *testing.T) {
	f := newDiffFixture(t, "# Owned files\n- src/\n", "Completed.\n", "implementer")
	info, err := os.Stat(filepath.Join(f.state, "briefs", "worker-fixture.md"))
	if err != nil {
		t.Fatal(err)
	}
	for name, offset := range map[string]time.Duration{"literal[1].txt": time.Second, "literal1.txt": -time.Second} {
		file := filepath.Join(f.tree, name)
		diffWrite(t, file, "new\n")
		diffTime(t, file, info.ModTime().Add(offset))
	}
	value, _, stderr := f.wait(t)
	statPath, _ := value["diff_stat"].(string)
	stat, err := os.ReadFile(statPath)
	if err != nil || !strings.Contains(string(stat), "literal[1].txt") || strings.Contains(string(stat), "literal1.txt") || !reflect.DeepEqual(value["outside_owned"], []any{"literal[1].txt"}) {
		t.Fatalf("literal path widened: value=%v stat=%q err=%v stderr=%s", value, stat, err, stderr)
	}
}

func TestDiffMtimeScratchAmendmentFindsOriginalStateBrief(t *testing.T) {
	f := newDiffFixture(t, "# Owned files\n- src/*.go\n", "Original.\n", "implementer")
	info, err := os.Stat(filepath.Join(f.state, "briefs", "worker-fixture.md"))
	if err != nil {
		t.Fatal(err)
	}
	diffWrite(t, filepath.Join(f.tree, "src", "a.go"), "changed between original and amendment\n")
	diffTime(t, filepath.Join(f.tree, "src", "a.go"), info.ModTime().Add(time.Second))
	stem := filepath.Join(t.TempDir(), "worker-amended")
	diffWrite(t, stem+".brief.md", "# Amendment\n# Owned files\n- src/*.go\n")
	diffTime(t, stem+".brief.md", info.ModTime().Add(2*time.Second))
	diffWrite(t, stem+".md", "Completed `src/a.go`.\n")
	data, err := json.Marshal(map[string]any{"role": "implementer", "git_tree": f.tree, "git_head": f.head, "git_branch": "main", "task_report": filepath.Join(f.state, "reports", "worker-fixture.current.md")})
	if err != nil {
		t.Fatal(err)
	}
	diffWrite(t, stem+".dispatch.json", string(data))
	diffWrite(t, filepath.Join(f.state, "last-report-worker"), stem+".md\n")
	value, _, stderr := f.wait(t)
	if value["diff_stat"] == nil || stderr != "" {
		t.Fatalf("original state brief was not used: %v stderr=%s", value, stderr)
	}
}
