package wait

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestWaitQueuedAlert(t *testing.T) {
	for _, tc := range []struct {
		name, kind, threshold, replacement string
		age, seq, want                     int
	}{
		{"old-changing-output", "pi", "20", "", 25, 1, 1},
		{"at-threshold", "pi", "20", "", 19, 1, 1},
		{"young", "pi", "20", "", 0, 1, 0},
		{"disabled", "pi", "0", "", 25, 1, 0},
		{"changed-sequence", "pi", "20", "", 25, 2, 0},
		{"new-epoch", "pi", "20", "epoch", 25, 1, 2},
		{"new-path", "pi", "20", "path", 25, 1, 2},
		{"other-kind", "claude", "20", "", 25, 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			sd, bin := filepath.Join(root, "state"), filepath.Join(root, "bin")
			if err := os.MkdirAll(filepath.Join(sd, "wait"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(sd, "agents.tsv"), []byte("worker\tp0a\t"+tc.kind+"\timplementer\n"), 0600); err != nil {
				t.Fatal(err)
			}
			base := time.Unix(1700000000, 0)
			epoch := base.Add(-time.Duration(tc.age) * time.Minute).Unix()
			marker := filepath.Join(sd, "wait", "worker.queued")
			writeMarker := func(epoch int64, path string) {
				if err := os.WriteFile(marker, []byte(fmt.Sprintf("%d 1 %s\n", epoch, path)), 0600); err != nil {
					t.Fatal(err)
				}
			}
			writeMarker(epoch, "/tmp/queued prompt.brief.md")
			var rules []fakecli.Rule
			for i := 1; i <= 4; i++ {
				rules = append(rules, fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Call: i, Stdout: fmt.Sprintf(`{"result":{"agent":{"agent_status":"working","state_change_seq":%d}}}`, tc.seq)})
				rules = append(rules, fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Call: i, Stdout: fmt.Sprintf("processed item %d\n", i)})
			}
			rules = append(rules, fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"gone","state_change_seq":2}}}`})
			if _, err := fakecli.Install(t, bin, "herdr", rules); err != nil {
				t.Fatal(err)
			}
			env := platform.Env{"PATH": bin, "SOHO_FAKECLI_CONFIG": bin, "GORACE": "atexit_sleep_ms=0", "SOHO_WAIT_POLL_MS": "1", "HOME": root, "USERPROFILE": root, "XDG_CONFIG_HOME": root, "TMPDIR": root, "HERDR_SOCKET_PATH": filepath.Join(root, "none.sock")}
			calls := func() []fakecli.Call {
				cs, err := fakecli.ReadCalls(filepath.Join(bin, "herdr.calls.jsonl"))
				if err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				return cs
			}
			oldNow, oldOut, oldErr, oldFriction := platform.Now, platform.Stdout, platform.Stderr, frictionLogFile
			t.Cleanup(func() {
				platform.Now, platform.Stdout, platform.Stderr = oldNow, oldOut, oldErr
				SetFrictionLogFile(oldFriction)
			})
			var out, stderr bytes.Buffer
			platform.Stdout, platform.Stderr = &out, &stderr
			friction := filepath.Join(root, "friction.log")
			SetFrictionLogFile(friction)
			replaced := false
			platform.Now = func() time.Time {
				n := 0
				for _, c := range calls() {
					if strings.Join(c.Argv, " ") == "agent get worker" {
						n++
					}
				}
				if n == 2 && tc.replacement != "" && !replaced {
					replaced = true
					if tc.replacement == "epoch" {
						writeMarker(epoch+60, "/tmp/queued prompt.brief.md")
					} else {
						writeMarker(epoch, "/tmp/second prompt.brief.md")
					}
				}
				return base.Add(time.Duration(n) * time.Minute)
			}
			ctx := &core.Config{Entries: map[string]core.ConfigEntry{"idle_alert_minutes": {Value: tc.threshold}, "stuck_warn_minutes": {Value: "0"}}}
			code := WaitFor([]string{"worker"}, sd, ctx, env, float64((10*time.Minute)/time.Millisecond), false, root)
			if code != 6 || !strings.Contains(out.String(), `"status":"gone"`) {
				t.Fatalf("exit=%d out=%s stderr=%s", code, out.String(), stderr.String())
			}
			needle := "the prompt to 'worker' has been queued for "
			if got := strings.Count(stderr.String(), needle); got != tc.want {
				t.Fatalf("queued warnings=%d want=%d stderr=%s", got, tc.want, stderr.String())
			}
			logged, err := os.ReadFile(friction)
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if strings.Count(string(logged), needle) != tc.want {
				t.Fatalf("friction mismatch: %s", logged)
			}
			hint := " (pi: Esc, then Enter)"
			if got := strings.Count(stderr.String(), hint); got != tc.want*boolInt(tc.kind == "pi") {
				t.Fatalf("hint count=%d stderr=%s", got, stderr.String())
			}
			if tc.want > 0 {
				age := tc.age + 1
				want := fmt.Sprintf("the prompt to 'worker' has been queued for %d min while it works; it reads it when its turn ends. If a long tool call holds the turn, interrupt it in its pane", age)
				if tc.kind == "pi" {
					want += hint
				}
				want += " or wait"
				if !strings.Contains(stderr.String(), want+"\n") || !strings.Contains(string(logged), "\twarning\twait\t"+want+"\n") {
					t.Fatalf("warning text/friction incorrect stderr=%s log=%s", stderr.String(), logged)
				}
			}
			if strings.Contains(stderr.String(), "has been working for") || strings.Contains(out.String(), "idle_alerts") {
				t.Fatalf("changing output triggered idle alert: %s %s", stderr.String(), out.String())
			}
			for _, c := range calls() {
				if len(c.Argv) > 1 && (c.Argv[1] == "prompt" || c.Argv[1] == "send-keys") {
					t.Fatalf("unexpected input argv=%q", c.Argv)
				}
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("marker not removed by terminal state: %v", err)
			}
			t.Logf("exit=%d queued_warnings=%d friction_warnings=%d kind=%s replacement=%s; changing output, no idle alert, zero prompt/send-keys", code, tc.want, strings.Count(string(logged), needle), tc.kind, tc.replacement)
		})
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
