package gittree_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiffOwnedHistoryIdentityAndPosition(t *testing.T) {
	for _, mode := range []string{"same-task", "foreign-task", "future-member", "unreadable-intermediate", "current-explicit-control"} {
		t.Run(mode, func(t *testing.T) {
			f := newDiffFixture(t, "# Owned files\n- src/\n", "Completed.\n", "implementer")
			task := filepath.Join(f.state, "reports", "worker-fixture.current.md")
			member := filepath.Join(f.state, "reports", "worker-middle.md")
			stem := filepath.Join(f.state, "briefs", "worker-middle")
			identity := task
			if mode == "foreign-task" {
				identity = filepath.Join(f.state, "reports", "another.current.md")
			}
			diffWrite(t, stem+".dispatch.json", fmt.Sprintf(`{"task_report":%q}`, identity))
			diffWrite(t, stem+".md", "# Brief\n# Owned files\n- lib/\n")
			if mode == "unreadable-intermediate" || mode == "current-explicit-control" {
				if err := os.Chmod(stem+".md", 0000); err != nil {
					t.Fatal(err)
				}
				if _, err := os.ReadFile(stem + ".md"); err == nil {
					t.Skip("environment can read mode-000 file")
				}
			}
			current := filepath.Join(f.state, "reports", "worker-current.md")
			currentStem := filepath.Join(f.state, "briefs", "worker-current")
			body := "# Brief\nCheck again.\n"
			if mode == "current-explicit-control" {
				body += "# Owned files\n- lib/\n"
			}
			diffWrite(t, currentStem+".md", body)
			diffWrite(t, currentStem+".dispatch.json", fmt.Sprintf(`{"role":"implementer","git_tree":%q,"git_head":%q,"git_branch":"main","task_report":%q}`, f.tree, f.head, task))
			diffWrite(t, current, "Completed.\n")
			history := fmt.Sprintf(`[%q,%q]`, f.report, member)
			pointerCurrent := current
			if mode == "future-member" {
				history = fmt.Sprintf(`[%q,%q]`, f.report, current)
				pointerCurrent = member
			}
			diffWrite(t, filepath.Join(f.state, "task-report-worker.json"), fmt.Sprintf(`{"version":1,"task_report":%q,"history":%s,"current":%q}`, task, history, pointerCurrent))
			diffWrite(t, filepath.Join(f.state, "last-report-worker"), current+"\n")
			diffWrite(t, filepath.Join(f.tree, "lib", "allowed.txt"), "changed\n")
			value, out, warning := f.wait(t)
			t.Logf("mode=%s output=%s stderr=%s", mode, out, warning)
			if mode == "unreadable-intermediate" {
				if value["diff_stat"] != nil {
					t.Fatal("unknown intermediate scope audited")
				}
				if _, err := os.Stat(currentStem + ".dispatch.json.diff-checked"); !os.IsNotExist(err) {
					t.Fatalf("marker=%v", err)
				}
				return
			}
			if value["diff_stat"] == nil {
				t.Fatal("audit missing")
			}
			wantOutside := mode == "foreign-task" || mode == "future-member"
			if strings.Contains(warning, "outside its Owned files: lib/allowed.txt") != wantOutside {
				t.Fatalf("scope identity/position mismatch: %v %s", value, warning)
			}
		})
	}
}
