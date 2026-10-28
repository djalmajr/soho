package cli

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

func TestReview543R5ScopeChain(t *testing.T) {
	for _, mode := range []string{"intermediate-replacement", "most-recent-replacement", "current-explicit-control", "all-omitted-control"} {
		t.Run(mode, func(t *testing.T) {
			f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
			f.env["PATH"] += string(os.PathListSeparator) + waitingGitOnlyDir(t)
			f.env["SOHO_SETTLED_GRACE"], f.env["SOHO_WAIT_POLL_MS"], f.env["SOHO_METRICS"] = "0", "1", "off"
			_ = headInit(t, f.env, f.root)
			reopenDispatchWrite(t, f.brief, "# Goal\nImplement.\n# Owned files\n- `src/`\n")
			composed, _ := firstDispatch(t, f)
			original, e := os.Stat(composed)
			if e != nil {
				t.Fatal(e)
			}
			amends := []string{"# Owned files\n- `lib/`\n", "Check the result.\n", "Check once more.\n"}
			owned := "lib/allowed.txt"
			if mode == "most-recent-replacement" {
				amends[1] = "# Owned files\n- `newscope/`\n"
				owned = "newscope/allowed.txt"
			}
			if mode == "current-explicit-control" {
				amends[2] = "# Owned files\n- `lib/`\n"
			}
			if mode == "all-omitted-control" {
				amends[0] = "Check it.\n"
				owned = "src/allowed.txt"
			}
			inputs := t.TempDir()
			var report string
			for i, body := range amends {
				input := filepath.Join(inputs, fmt.Sprintf("amend%d.md", i))
				reopenDispatchWrite(t, input, fmt.Sprintf("Amendment number %d.\n", i)+body)
				code, out, msg := f.run(t, "worker", input, "--amend", "--no-wait")
				if code != 0 {
					t.Fatalf("amend %d code=%d out=%s stderr=%s", i, code, out, msg)
				}
				report, _ = dispatchOutputField(t, out, "report")
				t.Logf("amendment=%d report=%s", i+1, report)
			}
			for _, name := range []string{owned, "outside.txt"} {
				p := filepath.Join(f.root, name)
				if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
					t.Fatal(e)
				}
				reopenDispatchWrite(t, p, "changed\n")
				when := original.ModTime().Add(time.Second)
				if e := os.Chtimes(p, when, when); e != nil {
					t.Fatal(e)
				}
			}
			reopenDispatchWrite(t, report, "Completed.\n")
			reportAfterFixtureEdits(t, report, original.ModTime().Add(time.Second))
			code, out, msg := reopenDispatchRun(t, f.env, "wait", "worker", "--timeout", "2s")
			t.Logf("wait code=%d out=%s stderr=%s", code, out, msg)
			var v map[string]any
			if e := json.Unmarshal([]byte(out), &v); e != nil {
				t.Fatal(e)
			}
			if code != 0 || v["diff_stat"] == nil || !reflect.DeepEqual(v["outside_owned"], []any{"outside.txt"}) || !strings.Contains(msg, "outside its Owned files: outside.txt") {
				t.Fatalf("effective scope lost: allowed=%s outside_owned=%v; want only outside.txt", owned, v["outside_owned"])
			}
			_, again, err2 := reopenDispatchRun(t, f.env, "wait", "worker", "--timeout", "2s")
			if strings.Contains(again, "outside_owned") || err2 != "" {
				t.Fatalf("audit repeated out=%s stderr=%s", again, err2)
			}
		})
	}
}
