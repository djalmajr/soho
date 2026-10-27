package gittree_test

import (
	"fmt"
	"github.com/djalmajr/soho/internal/core"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReview543R5OriginalUnavailable(t *testing.T) {
	for _, mode := range []string{"missing", "unreadable", "readable-control"} {
		t.Run(mode, func(t *testing.T) {
			f := newDiffFixture(t, "# Owned files\n- src/\n", "Original.\n", "implementer")
			original := filepath.Join(f.state, "briefs", "worker-original.md")
			if mode != "missing" {
				diffWrite(t, original, "# Brief\n# Owned files\n- src/\n")
				if e := os.Chtimes(original, time.Unix(1700000100, 0), time.Unix(1700000100, 0)); e != nil {
					t.Fatal(e)
				}
				if mode == "unreadable" {
					if e := os.Chmod(original, 0000); e != nil {
						t.Fatal(e)
					}
					if _, e := core.ReadScratchReport(original); e == nil {
						t.Skip("environment can read mode-000 file")
					} else {
						t.Logf("original read error=%v", e)
					}
					if _, e := core.StatScratchReport(original); e != nil {
						t.Fatalf("original stat=%v", e)
					}
				}
			}
			stem := filepath.Join(f.state, "briefs", "worker-amended")
			diffWrite(t, stem+".md", "# Amendment\nReview it again.\n")
			report := filepath.Join(f.state, "reports", "worker-amended.md")
			diffWrite(t, report, "Completed.\n")
			diffWrite(t, stem+".dispatch.json", fmt.Sprintf(`{"role":"implementer","git_tree":%q,"git_head":%q,"git_branch":"main","task_report":%q}`, f.tree, f.head, filepath.Join(f.state, "reports", "worker-original.current.md")))
			diffWrite(t, filepath.Join(f.state, "last-report-worker"), report+"\n")
			diffWrite(t, filepath.Join(f.tree, "outside.txt"), "new\n")
			value, out, stderr := f.wait(t)
			t.Logf("mode=%s out=%s stderr=%s", mode, out, stderr)
			if mode == "readable-control" {
				if value["diff_stat"] == nil || !strings.Contains(stderr, "outside its Owned files: outside.txt") {
					t.Fatal("control did not audit")
				}
				return
			}
			for _, file := range []string{stem + ".dispatch.json.diff-checked", strings.TrimSuffix(report, ".md") + ".diff-stat.txt"} {
				if _, err := os.Stat(file); !os.IsNotExist(err) {
					t.Errorf("unavailable audit left %s: %v", file, err)
				}
			}
			if b, err := os.ReadFile(stem + ".dispatch.json"); err != nil || strings.Contains(string(b), "diff_checked") {
				t.Errorf("unavailable audit changed flag: %s %v", b, err)
			}
			if value["diff_stat"] != nil || value["outside_owned"] != nil || value["cited_unchanged"] != nil || stderr != "" {
				t.Errorf("unavailable original must report nothing; audit=%v stderr=%s", value, stderr)
			}
			if mode == "unreadable" {
				if err := os.Chmod(original, 0600); err != nil {
					t.Fatal(err)
				}
				retry, _, warning := f.wait(t)
				if retry["diff_stat"] == nil || !strings.Contains(warning, "outside its Owned files: outside.txt") {
					t.Fatalf("readable retry failed: %v %s", retry, warning)
				}
			}
		})
	}
}
