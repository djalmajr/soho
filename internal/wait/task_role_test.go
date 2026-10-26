package wait

import (
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/taskreport"
	"os"
	"path/filepath"
	"testing"
)

func TestWaitRecordedTaskRole(t *testing.T) {
	for _, tc := range []struct {
		name, pointer, sidecar, want string
		beside                       bool
	}{
		{"pointer wins", "implementer", "reviewer", "implementer", false},
		{"sidecar fallback", "", "implementer", "implementer", false},
		{"beside current report", "", "reviewer", "reviewer", true},
		{"legacy roster fallback", "", "", "scouter", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sd := t.TempDir()
			if e := os.MkdirAll(filepath.Join(sd, "briefs"), 0700); e != nil {
				t.Fatal(e)
			}
			report := filepath.Join(t.TempDir(), "current report.md")
			pointer := jsonjs.O("version", 1, "task_report", report+".current.md", "history", []any{}, "role", tc.pointer, "current", report)
			if e := taskreport.WriteTaskReportPointer(sd, "worker", pointer); e != nil {
				t.Fatal(e)
			}
			if e := os.WriteFile(filepath.Join(sd, "agents.tsv"), []byte("worker\tp1\tcodex\tscouter\n"), 0600); e != nil {
				t.Fatal(e)
			}
			path := filepath.Join(sd, "briefs", "current report.dispatch.json")
			if tc.beside {
				path = filepath.Join(filepath.Dir(report), "current report.dispatch.json")
			}
			if e := os.WriteFile(path, []byte(jsonjs.Stringify(jsonjs.O("role", tc.sidecar))), 0600); e != nil {
				t.Fatal(e)
			}
			if got := TaskRole(sd, "worker"); got != tc.want {
				t.Fatalf("TaskRole=%q want=%q", got, tc.want)
			}
			recorded := RecordedTaskRole(sd, "worker")
			if tc.want == "scouter" {
				if recorded != "" {
					t.Fatalf("legacy recorded=%q", recorded)
				}
			} else if recorded != tc.want {
				t.Fatalf("recorded=%q want=%q", recorded, tc.want)
			}
		})
	}
}
