package wait

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/backend"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

func TestSubmitFailedWaitAndStatus(t *testing.T) {
	cases := []struct {
		name    string
		marker  string
		pointer bool
		want    string
		code    int
	}{
		{"matching", "match", true, "not-received", 15},
		{"stale", "stale", true, "done", 0},
		{"absent", "absent", true, "done", 0},
		{"other-agent", "other", true, "done", 0},
		{"empty", "empty", true, "unknown", 4},
		{"oversized", "oversized", true, "unknown", 4},
		{"symlink", "symlink", true, "unknown", 4},
		{"no-pointer", "none", false, "not-received", 15},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newMetricsFixture(t, "off", metricsImplementerRoster, "# Previous report\n", "", "")
			isolateReportEnv(t, m.env)
			marker := filepath.Join(m.sd, "wait", "worker.submit-failed")
			ptr := filepath.Join(m.sd, "task-report-worker.json")
			if tc.pointer {
				raw, err := json.Marshal(map[string]any{"version": 1, "task_report": m.report, "current": m.report, "history": []string{}})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(ptr, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			content := fmt.Sprintf("123 %s %s\n", strings.Repeat("a", 64), m.report)
			switch tc.marker {
			case "stale":
				content = fmt.Sprintf("123 %s %s\n", strings.Repeat("a", 64), m.report+"-old")
			case "none":
				content = fmt.Sprintf("123 %s -\n", strings.Repeat("a", 64))
			case "empty":
				content = ""
			case "oversized":
				content = strings.Repeat("x", 65537)
			case "other":
				marker = filepath.Join(m.sd, "wait", "other.submit-failed")
			}
			if tc.marker == "symlink" {
				target := filepath.Join(t.TempDir(), "marker")
				if err := os.WriteFile(target, []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, marker); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			} else if tc.marker != "absent" {
				if err := os.WriteFile(marker, []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			m.env["SOHO_DIR"] = filepath.Dir(m.sd)
			for _, observe := range []bool{false, true} {
				var stdout, stderr bytes.Buffer
				oldOut, oldErr := platform.Stdout, platform.Stderr
				platform.Stdout, platform.Stderr = &stdout, &stderr
				args := []string{"worker", "--timeout", "5s"}
				if observe {
					args = append(args, "--observe")
				}
				code := CmdWait(args, m.ctx, m.env, "")
				platform.Stdout, platform.Stderr = oldOut, oldErr
				if code != tc.code || !strings.Contains(stdout.String(), `"status":"`+tc.want+`"`) {
					t.Fatalf("observe=%v code=%d stdout=%q stderr=%q", observe, code, stdout.String(), stderr.String())
				}
				if tc.want == "not-received" && (!strings.Contains(stdout.String(), `"cause":"submission-failed"`) || !strings.Contains(stderr.String(), "the last dispatch to 'worker' failed and was not received")) {
					t.Fatalf("observe=%v out=%q stderr=%q", observe, stdout.String(), stderr.String())
				}
			}
			var stdout, stderr bytes.Buffer
			oldOut, oldErr := platform.Stdout, platform.Stderr
			platform.Stdout, platform.Stderr = &stdout, &stderr
			code := CmdStatus([]string{"worker"}, m.ctx, m.env, "")
			platform.Stdout, platform.Stderr = oldOut, oldErr
			if code != tc.code || !strings.Contains(stdout.String(), "worker\t"+tc.want+"\t") {
				t.Fatalf("status code=%d out=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			src, err := core.OpenSessionSource(filepath.Dir(m.sd), filepath.Base(m.sd), "")
			if err != nil {
				t.Fatal(err)
			}
			defer src.Close()
			view := InspectAgentStatus(AgentStatusRequest{StateDir: m.sd, SessionID: "ws", Agent: "worker", Config: m.ctx, Env: m.env, Services: backend.New(m.env), Source: src})
			if view.State != tc.want {
				t.Fatalf("source inspection state=%q want=%q", view.State, tc.want)
			}
			if tc.name == "matching" {
				m.env["SOHO_TRANSPORT"] = "hc"
				var hcOut, hcErr bytes.Buffer
				oldOut, oldErr := platform.Stdout, platform.Stderr
				platform.Stdout, platform.Stderr = &hcOut, &hcErr
				code := waitFor([]string{"worker"}, m.sd, m.ctx, m.env, 5000, false, true, "")
				platform.Stdout, platform.Stderr = oldOut, oldErr
				want := "soho: warning: the last dispatch to 'worker' failed and was not received; the report on disk belongs to the previous task — read the terminal (hc read worker), then dispatch again\n"
				if code != 15 || hcErr.String() != want || !strings.Contains(hcOut.String(), `"cause":"submission-failed"`) {
					t.Fatalf("hc observe code=%d out=%q stderr=%q", code, hcOut.String(), hcErr.String())
				}
			}
		})
	}
}
