package wait

import (
	"bytes"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReview546BlockedReadBudget(t *testing.T) {
	for _, kind := range []string{"claude", "cursor"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			sd, bin := filepath.Join(root, "state"), filepath.Join(root, "bin")
			if e := os.MkdirAll(filepath.Join(sd, "wait"), 0700); e != nil {
				t.Fatal(e)
			}
			if e := os.WriteFile(filepath.Join(sd, "agents.tsv"), []byte("worker\tp0a\t"+kind+"\timplementer\n"), 0600); e != nil {
				t.Fatal(e)
			}
			rules := []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"blocked","state_change_seq":1}}}`}, {Argv: []string{"agent", "read", "worker"}, ArgvPrefix: true, Stdout: "command output"}}
			if _, e := fakecli.Install(t, bin, "herdr", rules); e != nil {
				t.Fatal(e)
			}
			env := platform.Env{"PATH": bin, "SOHO_FAKECLI_CONFIG": bin, "GORACE": "atexit_sleep_ms=0", "SOHO_WAIT_POLL_MS": "1", "HOME": root, "TMPDIR": root}
			ctx := &core.Config{Entries: map[string]core.ConfigEntry{"auto_approve": {Value: "off"}, "idle_alert_minutes": {Value: "20"}, "stuck_warn_minutes": {Value: "0"}}}
			oldOut, oldErr, oldFriction := platform.Stdout, platform.Stderr, frictionLogFile
			var out, stderr bytes.Buffer
			platform.Stdout, platform.Stderr = &out, &stderr
			SetFrictionLogFile(filepath.Join(root, "friction"))
			defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr; SetFrictionLogFile(oldFriction) }()
			code := WaitFor([]string{"worker"}, sd, ctx, env, 3000, false, root)
			calls, e := fakecli.ReadCalls(filepath.Join(bin, "herdr.calls.jsonl"))
			if e != nil {
				t.Fatal(e)
			}
			poll, firstReads := 0, 0
			for _, c := range calls {
				s := strings.Join(c.Argv, " ")
				if s == "agent get worker" {
					poll++
				}
				if poll == 1 && s == "agent read worker --source visible" {
					firstReads++
				}
				if len(c.Argv) > 1 && (c.Argv[1] == "prompt" || c.Argv[1] == "send-keys") {
					t.Fatalf("input %v", c.Argv)
				}
			}
			want := 0
			if kind == "cursor" {
				want = 1
			}
			t.Logf("native_state=blocked exit=%d polls=%d first_poll_full_visible_reads=%d main_budget=%d stdout=%s", code, poll, firstReads, want, out.String())
			if code != 7 || poll != 2 {
				t.Fatalf("unexpected state transition: exit=%d polls=%d stderr=%s", code, poll, stderr.String())
			}
			if firstReads != want {
				t.Errorf("idle monitor added a visible read to the first native-blocked poll: got %d want main's %d", firstReads, want)
			}
		})
	}
}
