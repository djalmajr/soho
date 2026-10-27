package wait

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func review546RunMany(t *testing.T, kind string, polls int, step time.Duration, screen func(int) string) (int, int, string, string) {
	t.Helper()
	root := t.TempDir()
	sd, bin := filepath.Join(root, "state"), filepath.Join(root, "bin")
	if e := os.MkdirAll(filepath.Join(sd, "wait"), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(sd, "agents.tsv"), []byte("worker\tp0a\t"+kind+"\timplementer\n"), 0600); e != nil {
		t.Fatal(e)
	}
	var rules []fakecli.Rule
	for i := 1; i <= polls; i++ {
		rules = append(rules, fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Call: i, Stdout: `{"result":{"agent":{"agent_status":"working","state_change_seq":1}}}`})
	}
	rules = append(rules, fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle","state_change_seq":2}}}`})
	for i := 1; i <= 3*polls+5; i++ {
		rules = append(rules, fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Call: i, Stdout: screen(i)})
	}
	rules = append(rules, fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible", "--lines", "20"}, Stdout: "command output"}, fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped", "--lines", "40"}, Stdout: "command output"})
	if _, e := fakecli.Install(t, bin, "herdr", rules); e != nil {
		t.Fatal(e)
	}
	env := platform.Env{"PATH": bin, "SOHO_FAKECLI_CONFIG": bin, "GORACE": "atexit_sleep_ms=0", "SOHO_WAIT_POLL_MS": "1", "HOME": root, "USERPROFILE": root, "XDG_CONFIG_HOME": root, "TMPDIR": root, "HERDR_SOCKET_PATH": filepath.Join(root, "none.sock")}
	calls := func() []fakecli.Call {
		cs, e := fakecli.ReadCalls(filepath.Join(bin, "herdr.calls.jsonl"))
		if e != nil && !os.IsNotExist(e) {
			t.Fatal(e)
		}
		return cs
	}
	oldNow, oldOut, oldErr, oldFriction := platform.Now, platform.Stdout, platform.Stderr, frictionLogFile
	var out, stderr bytes.Buffer
	friction := filepath.Join(root, "friction.log")
	platform.Stdout, platform.Stderr = &out, &stderr
	SetFrictionLogFile(friction)
	platform.Now = func() time.Time {
		n := 0
		for _, c := range calls() {
			if strings.Join(c.Argv, " ") == "agent get worker" {
				n++
			}
		}
		return time.Unix(1700000000, 0).Add(time.Duration(n) * step)
	}
	defer func() {
		platform.Now, platform.Stdout, platform.Stderr = oldNow, oldOut, oldErr
		SetFrictionLogFile(oldFriction)
	}()
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{"idle_alert_minutes": {Value: "20"}, "stuck_warn_minutes": {Value: "0"}, "settled_grace": {Value: "0"}}}
	code := WaitFor([]string{"worker"}, sd, ctx, env, float64((100*time.Minute)/time.Millisecond), false, root)
	if code != 6 {
		t.Fatalf("did not settle: exit=%d out=%s err=%s", code, out.String(), stderr.String())
	}
	for _, c := range calls() {
		if len(c.Argv) > 1 && (c.Argv[1] == "prompt" || c.Argv[1] == "send-keys") {
			t.Fatalf("input: %v", c.Argv)
		}
	}
	var obj map[string]any
	if e := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &obj); e != nil {
		t.Fatal(e)
	}
	count := strings.Count(stderr.String(), "has been working for")
	logged, e := os.ReadFile(friction)
	if e != nil && !os.IsNotExist(e) {
		t.Fatal(e)
	}
	if strings.Count(string(logged), "has been working for") != count {
		t.Fatalf("friction/stderr discrepancy")
	}
	if count > 0 && obj["idle_alerts"] != float64(count) {
		t.Fatal("count missing from JSON")
	}
	if count == 0 && obj["idle_alerts"] != nil {
		t.Fatal("quiet JSON changed")
	}
	t.Logf("exit=%d alerts=%d friction_alerts=%d JSON=%s stderr=%q; zero prompt/send-keys", code, count, strings.Count(string(logged), "has been working for"), out.String(), stderr.String())
	n := 0
	for _, c := range calls() {
		if strings.Join(c.Argv, " ") == "agent read worker --source visible" {
			n++
		}
	}
	t.Logf("full_visible_reads=%d for %d working polls plus settling", n, polls)
	return code, count, out.String(), stderr.String()
}
func TestReview546CursorAllFrames(t *testing.T) {
	for _, kind := range []string{"cursor", "claude"} {
		t.Run(kind, func(t *testing.T) {
			_, count, _, _ := review546RunMany(t, kind, 27, time.Minute, func(i int) string {
				if i%2 == 1 {
					return fmt.Sprintf("command output\nprocessed item %d", i)
				}
				return "command output\nwaiting on command"
			})
			if count != 0 {
				t.Errorf("first already-read frame contained substantive progress but was overwritten: kind=%s alerts=%d", kind, count)
			}
		})
	}
}
