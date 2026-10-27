package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	waitpkg "github.com/djalmajr/soho/internal/wait"
)

// Produce the serializer input through the real WaitFor path, using fake
// minutes and Herdr. No captured output from another run is required.
func idleProducerRecord(t *testing.T) []byte {
	t.Helper()
	root := t.TempDir()
	sd, bin := filepath.Join(root, "state"), filepath.Join(root, "bin")
	if err := os.MkdirAll(filepath.Join(sd, "wait"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sd, "agents.tsv"), []byte("worker\tp0a\tclaude\timplementer\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var rules []fakecli.Rule
	for i := 1; i <= 25; i++ {
		rules = append(rules, fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Call: i, Stdout: `{"result":{"agent":{"agent_status":"working","state_change_seq":1}}}`})
	}
	rules = append(rules, fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle","state_change_seq":2}}}`}, fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, ArgvPrefix: true, Stdout: "command output"}, fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: "command output"})
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
	oldNow, oldOut, oldErr := platform.Now, platform.Stdout, platform.Stderr
	var out, stderr bytes.Buffer
	platform.Now = func() time.Time {
		n := 0
		for _, c := range calls() {
			if strings.Join(c.Argv, " ") == "agent get worker" {
				n++
			}
		}
		return time.Unix(1700000000, 0).Add(time.Duration(n) * time.Minute)
	}
	platform.Stdout, platform.Stderr = &out, &stderr
	waitpkg.SetFrictionLogFile(filepath.Join(root, "friction.log"))
	defer func() {
		platform.Now, platform.Stdout, platform.Stderr = oldNow, oldOut, oldErr
		waitpkg.SetFrictionLogFile(frictionLogPath)
	}()
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{"idle_alert_minutes": {Value: "20"}, "stuck_warn_minutes": {Value: "0"}, "settled_grace": {Value: "0"}}}
	if rc := waitpkg.WaitFor([]string{"worker"}, sd, ctx, env, float64((60*time.Minute)/time.Millisecond), false, root); rc != 6 {
		t.Fatalf("producer exit %d: %s %s", rc, out.String(), stderr.String())
	}
	var obj map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &obj); err != nil {
		t.Fatal(err)
	}
	if obj["idle_alerts"] != 1.0 || strings.Count(stderr.String(), "has been working for") != 1 {
		t.Fatalf("producer did not alert once: %s %s", out.String(), stderr.String())
	}
	for _, c := range calls() {
		if len(c.Argv) > 1 && (c.Argv[1] == "prompt" || c.Argv[1] == "send-keys") {
			t.Fatalf("producer sent input: %v", c.Argv)
		}
	}
	return append([]byte(nil), out.Bytes()...)
}

func TestReviewDispatchIdleCount(t *testing.T) {
	produced := idleProducerRecord(t)
	for _, duplicate := range []bool{false, true} {
		name := "fresh"
		if duplicate {
			name = "duplicate"
		}
		t.Run(name, func(t *testing.T) {
			f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
			f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
			if duplicate {
				firstDispatch(t, f)
			}
			previous := dispatchWaitFor
			calls := 0
			dispatchWaitFor = func(agents []string, _ string, _ *core.Config, _ platform.Env, _ float64, _ bool, _ string) int {
				calls++
				if len(agents) != 1 || agents[0] != "worker" {
					t.Fatalf("wait agents %v", agents)
				}
				if _, err := platform.Stdout.Write(produced); err != nil {
					t.Fatal(err)
				}
				return 6
			}
			t.Cleanup(func() { dispatchWaitFor = previous })
			rc, out, msg := f.run(t, "worker", f.brief, "--timeout", "15000")
			var obj map[string]any
			if err := json.Unmarshal([]byte(out), &obj); err != nil {
				t.Fatal(err)
			}
			_, isDuplicate := obj["duplicate_of"]
			if rc != 6 || calls != 1 || isDuplicate != duplicate || obj["idle_alerts"] != 1.0 {
				t.Fatalf("branch=%s exit=%d calls=%d duplicate=%t out=%s err=%s", name, rc, calls, isDuplicate, out, msg)
			}
			t.Logf("branch=%s wait_calls=1 exit=6 idle_alerts=1 (real WaitFor producer)", name)
		})
	}
}
