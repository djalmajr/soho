package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDispatchOrchestratorFilesOutsideOwned(t *testing.T) {
	for _, tc := range []struct {
		name, owned, orchestrator, file string
		outside                         bool
	}{
		{"declared", "src/", "notes.txt", "notes.txt", false},
		{"undeclared", "src/", "", "notes.txt", true},
		{"recursive glob", "src/", "docs/**/notes.txt", "docs/deep/notes.txt", false},
		{"owned overlap", "src/**", "src/**", "src/notes.txt", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
			f.env["PATH"] += string(os.PathListSeparator) + waitingGitOnlyDir(t)
			f.env["SOHO_SETTLED_GRACE"], f.env["SOHO_WAIT_POLL_MS"], f.env["SOHO_METRICS"] = "0", "1", "off"
			_ = headInit(t, f.env, f.root)
			body := "# Goal\nImplement.\n## Owned files\n- `" + tc.owned + "`\n"
			if tc.orchestrator != "" {
				body += "## Orchestrator files\n- `" + tc.orchestrator + "`\n"
			}
			reopenDispatchWrite(t, f.brief, body)
			composed, report := firstDispatch(t, f)
			info, err := os.Stat(composed)
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(f.root, tc.file)
			if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
				t.Fatal(err)
			}
			reopenDispatchWrite(t, file, "changed\n")
			cutoff := info.ModTime().Add(time.Second)
			if err := os.Chtimes(file, cutoff, cutoff); err != nil {
				t.Fatal(err)
			}
			reopenDispatchWrite(t, report, "## Files changed\n- `"+tc.file+"`\n")
			reportAfterFixtureEdits(t, report, cutoff)
			code, out, stderr := reopenDispatchRun(t, f.env, "wait", "worker", "--timeout", "2s")
			var result map[string]any
			if err := json.Unmarshal([]byte(out), &result); err != nil {
				t.Fatalf("code=%d out=%s stderr=%s: %v", code, out, stderr, err)
			}
			if code != 0 || result["diff_stat"] == nil || result["cited_unchanged"] != nil {
				t.Fatalf("code=%d out=%s stderr=%s", code, out, stderr)
			}
			if tc.outside {
				if !reflect.DeepEqual(result["outside_owned"], []any{tc.file}) || !strings.Contains(stderr, "outside its Owned files: "+tc.file) {
					t.Fatalf("out=%s stderr=%s", out, stderr)
				}
			} else if result["outside_owned"] != nil || strings.Contains(stderr, "outside its Owned files:") {
				t.Fatalf("out=%s stderr=%s", out, stderr)
			}
		})
	}
}
