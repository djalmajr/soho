package wait

import (
	"bytes"
	"encoding/json"
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

func TestIdleFingerprint(t *testing.T) {
	for _, volatile := range []string{"(1m 23s)", "12s", "• 5m", "Worked for 22m 9s • 10:40", "Context 52% used", "125 tokens", "↓ 20", "10:40", "⠋", "✻"} {
		if idleFingerprint("output 123\n"+volatile, "codex") != idleFingerprint("output 123", "codex") {
			t.Errorf("volatile line: %q", volatile)
		}
	}
	if idleFingerprint("step 1", "codex") == idleFingerprint("step 2", "codex") {
		t.Fatal("real numeric progress discarded")
	}
	// Existing real Codex capture; the other provider cases below are synthetic.
	screen, err := os.ReadFile("../peer/testdata/evidence-rc11-codex-history.txt")
	if err != nil {
		t.Fatal(err)
	}
	raw := string(screen)
	if !strings.Contains(raw, "35m 51s") {
		t.Fatal("real working timer fixture changed")
	}
	if idleFingerprint(raw, "codex") != idleFingerprint(strings.ReplaceAll(raw, "35m 51s", "36m 52s"), "codex") {
		t.Fatal("Codex working timer changed fingerprint")
	}
	for _, screen := range []string{"claude output\n✻ Thinking… (12s)\nContext 32% used", "pi output\n⠋ 12s\n125 tokens"} {
		if idleFingerprint(screen, "codex") != idleFingerprint(strings.ReplaceAll(screen, "12s", "13s"), "codex") {
			t.Fatal("synthetic provider timer changed fingerprint")
		}
	}
}

func TestIdleWait(t *testing.T) {
	for _, tc := range []struct {
		name, state, config, mode string
		want                      int
	}{
		{"constant", "working", "20", "constant", 1}, {"timer", "working", "20", "timer", 1},
		{"progress", "working", "20", "progress", 0}, {"disabled", "working", "0", "constant", 0},
		{"idle", "idle", "20", "constant", 0}, {"done", "done", "20", "constant", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			sd := filepath.Join(root, "state")
			bin := filepath.Join(root, "bin")
			if err := os.MkdirAll(filepath.Join(sd, "wait"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(sd, "agents.tsv"), []byte("worker\tp0a\tclaude\timplementer\n"), 0600); err != nil {
				t.Fatal(err)
			}
			var rules []fakecli.Rule
			for i := 1; i <= 27; i++ {
				rules = append(rules, fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Call: i, Stdout: fmt.Sprintf(`{"result":{"agent":{"agent_status":%q,"state_change_seq":1}}}`, tc.state)})
			}
			rules = append(rules, fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle","state_change_seq":2}}}`})
			for _, argv := range [][]string{{"agent", "read", "worker", "--source", "visible"}, {"agent", "read", "worker", "--source", "visible", "--lines", "40"}, {"agent", "read", "worker", "--source", "visible", "--lines", "20"}, {"agent", "read", "worker", "--source", "recent-unwrapped", "--lines", "40"}} {
				for i := 1; i <= 54; i++ {
					screen := "command output"
					if tc.mode == "timer" {
						screen += fmt.Sprintf("\n• Working (%dm %ds)", i, i)
					}
					if tc.mode == "progress" {
						screen += fmt.Sprintf("\nstep %d", i/4)
					}
					rules = append(rules, fakecli.Rule{Argv: argv, Call: i, Stdout: screen})
				}
				rules = append(rules, fakecli.Rule{Argv: argv, Stdout: "finished output"})
			}
			if _, err := fakecli.Install(t, bin, "herdr", rules); err != nil {
				t.Fatal(err)
			}
			env := platform.Env{"PATH": bin, "SOHO_FAKECLI_CONFIG": bin, "GORACE": "atexit_sleep_ms=0", "SOHO_WAIT_POLL_MS": "1", "HOME": root, "USERPROFILE": root, "XDG_CONFIG_HOME": root, "TMPDIR": root, "HERDR_SOCKET_PATH": filepath.Join(root, "none.sock")}
			ctx := &core.Config{Entries: map[string]core.ConfigEntry{"idle_alert_minutes": {Value: tc.config}, "settled_grace": {Value: "0"}, "stuck_warn_minutes": {Value: "0"}}}
			var out, stderr bytes.Buffer
			oldNow, oldOut, oldErr, oldFriction := platform.Now, platform.Stdout, platform.Stderr, frictionLogFile
			friction := filepath.Join(root, "friction.log")
			platform.Stdout, platform.Stderr = &out, &stderr
			SetFrictionLogFile(friction)
			calls := func() []fakecli.Call {
				cs, err := fakecli.ReadCalls(filepath.Join(bin, "herdr.calls.jsonl"))
				if err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				return cs
			}
			platform.Now = func() time.Time {
				n := 0
				for _, c := range calls() {
					if strings.Join(c.Argv, " ") == "agent get worker" {
						n++
					}
				}
				return time.Unix(1700000000, 0).Add(time.Duration(n) * time.Minute)
			}
			t.Cleanup(func() {
				platform.Now, platform.Stdout, platform.Stderr = oldNow, oldOut, oldErr
				SetFrictionLogFile(oldFriction)
			})
			if rc := WaitFor([]string{"worker"}, sd, ctx, env, float64((100*time.Minute)/time.Millisecond), false, root); rc != 6 {
				t.Fatalf("rc %d stderr %s output %s", rc, stderr.String(), out.String())
			}
			needle := "has been working for 20 min with no new output"
			if got := strings.Count(stderr.String(), needle); got != tc.want {
				t.Fatalf("alerts %d want %d: %s", got, tc.want, stderr.String())
			}
			var line struct {
				Status string `json:"status"`
				Alerts *int   `json:"idle_alerts"`
			}
			if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &line); err != nil {
				t.Fatal(err)
			}
			// idle_alerts is additive: present with the count when an alert
			// fired, absent on a quiet run.
			if line.Status != "settled-no-report" || (tc.want == 0) != (line.Alerts == nil) || (line.Alerts != nil && *line.Alerts != tc.want) {
				t.Fatalf("settled JSON: %s", out.String())
			}
			logged, _ := os.ReadFile(friction)
			if strings.Count(string(logged), needle) != tc.want {
				t.Fatalf("friction: %s", logged)
			}
			readsThisPoll, poll := 0, 0
			for _, c := range calls() {
				if strings.Join(c.Argv, " ") == "agent get worker" {
					readsThisPoll = 0
					poll++
				}
				if strings.Join(c.Argv, " ") == "agent read worker --source visible" && poll <= 27 && tc.state == "working" {
					readsThisPoll++
					limit := 1 // The existing working probe supplies the alert frame.
					if readsThisPoll > limit {
						t.Fatalf("poll %d: visible reads %d exceed %d", poll, readsThisPoll, limit)
					}
				}
				if len(c.Argv) > 1 && (c.Argv[1] == "prompt" || c.Argv[1] == "send-keys") {
					t.Fatalf("unexpected input: %v", c.Argv)
				}
			}
			t.Logf("alerts=%d settled=%s; zero prompt/send-keys", tc.want, bytes.TrimSpace(out.Bytes()))
		})
	}
}

func TestIdleAlertResetsAndSampleLimit(t *testing.T) {
	a := &idleAlert{}
	now := time.Unix(1700000000, 0)
	if !a.sampleDue("task", "working", now, 20) {
		t.Fatal("first sample missing")
	}
	a.observe("output", "worker", now, 20, "codex")
	if a.sampleDue("task", "working", now.Add(59*time.Second), 20) {
		t.Fatal("sample faster than once/minute")
	}
	a.sampleDue("task", "idle", now.Add(19*time.Minute), 20)
	a.sampleDue("task", "working", now.Add(20*time.Minute), 20)
	a.observe("output", "worker", now.Add(20*time.Minute), 20, "codex")
	if a.count != 0 || !a.since.Equal(now.Add(20*time.Minute)) {
		t.Fatal("state did not reset timer")
	}
	a.sampleDue("other", "working", now.Add(21*time.Minute), 20)
	a.observe("output", "worker", now.Add(21*time.Minute), 20, "codex")
	if !a.since.Equal(now.Add(21 * time.Minute)) {
		t.Fatal("task did not reset timer")
	}
}
