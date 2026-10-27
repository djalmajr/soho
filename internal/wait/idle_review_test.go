package wait

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func reviewClaudeCapture(t *testing.T) string {
	t.Helper()
	file, e := parser.ParseFile(token.NewFileSet(), "../provider/input_regions_claude_label_test.go", nil, 0)
	if e != nil {
		t.Fatal(e)
	}
	value := ""
	ast.Inspect(file, func(n ast.Node) bool {
		v, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for i, name := range v.Names {
			if name.Name == "claudeLabeledScreen" {
				lit, ok := v.Values[i].(*ast.BasicLit)
				if !ok {
					t.Fatal("capture is not literal")
				}
				s, e := strconv.Unquote(lit.Value)
				if e != nil {
					t.Fatal(e)
				}
				value = s
			}
		}
		return true
	})
	if value == "" {
		t.Fatal("reported Claude fixture missing")
	}
	return value
}
func TestReviewFingerprintBoundary(t *testing.T) {
	base := reviewClaudeCapture(t)
	changed := strings.Replace(base, "2h4m", "2h3m", 1)
	t.Run("claude-countdown", func(t *testing.T) {
		if base == changed {
			t.Fatal("fixture anchor")
		}
		same := idleFingerprint(base, "claude") == idleFingerprint(changed, "claude")
		t.Logf("reported Claude footer countdown 2h4m -> 2h3m fingerprint_equal=%v", same)
		if !same {
			t.Fatal("volatile provider countdown retained")
		}
	})
	t.Run("path-progress", func(t *testing.T) {
		a, b := "command output\nprocessed src/5m.go item 1", "command output\nprocessed src/5m.go item 2"
		same := idleFingerprint(a, "claude") == idleFingerprint(b, "claude")
		t.Logf("before=%q after=%q fingerprint_equal=%v", a, b, same)
		if same {
			t.Fatal("substantive tool output was discarded as a timer")
		}
	})
}

type reviewIdleResult struct {
	out, stderr, friction string
	calls                 []fakecli.Call
	code                  int
	workingPolls          int
}

func reviewRunIdle(t *testing.T, mode string, threshold, polls int) reviewIdleResult {
	t.Helper()
	root := t.TempDir()
	sd, bin := filepath.Join(root, "state"), filepath.Join(root, "bin")
	if e := os.MkdirAll(filepath.Join(sd, "wait"), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(sd, "agents.tsv"), []byte("worker\tp0a\tclaude\timplementer\n"), 0600); e != nil {
		t.Fatal(e)
	}
	var rules []fakecli.Rule
	for i := 1; i <= polls; i++ {
		rules = append(rules, fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Call: i, Stdout: `{"result":{"agent":{"agent_status":"working","state_change_seq":1}}}`})
	}
	rules = append(rules, fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle","state_change_seq":2}}}`})
	capture := reviewClaudeCapture(t)
	for i := 1; i <= 2*polls+12; i++ {
		screen := "command output"
		switch mode {
		case "ctx-footer":
			screen = strings.Replace(backgroundShellScreen, "ctx 31%", fmt.Sprintf("ctx %d%%", i), 1)
		case "claude-countdown":
			screen = strings.Replace(capture, "2h4m", fmt.Sprintf("2h%dm", i), 1)
		case "path-progress":
			screen += fmt.Sprintf("\nprocessed src/5m.go item %d", i)
		case "timer-control":
			screen += fmt.Sprintf("\nWorking (%ds)", i)
		}
		rules = append(rules, fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Call: i, Stdout: screen})
	}
	rules = append(rules, fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Stdout: "finished output"}, fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible", "--lines", "20"}, Stdout: "command output"}, fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped", "--lines", "40"}, Stdout: "command output"})
	if _, e := fakecli.Install(t, bin, "herdr", rules); e != nil {
		t.Fatal(e)
	}
	env := platform.Env{"PATH": bin, "SOHO_FAKECLI_CONFIG": bin, "GORACE": "atexit_sleep_ms=0", "SOHO_WAIT_POLL_MS": "1", "HOME": root, "USERPROFILE": root, "XDG_CONFIG_HOME": root, "TMPDIR": root, "HERDR_SOCKET_PATH": filepath.Join(root, "none.sock")}
	ctx := &core.Config{Entries: map[string]core.ConfigEntry{"idle_alert_minutes": {Value: strconv.Itoa(threshold)}, "settled_grace": {Value: "0"}, "stuck_warn_minutes": {Value: "0"}}}
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
		return time.Unix(1700000000, 0).Add(time.Duration(n) * time.Minute)
	}
	t.Cleanup(func() {
		platform.Now, platform.Stdout, platform.Stderr = oldNow, oldOut, oldErr
		SetFrictionLogFile(oldFriction)
	})
	code := WaitFor([]string{"worker"}, sd, ctx, env, float64((100*time.Minute)/time.Millisecond), false, root)
	logged, _ := os.ReadFile(friction)
	cs := calls()
	for _, c := range cs {
		if len(c.Argv) > 1 && (c.Argv[1] == "prompt" || c.Argv[1] == "send-keys") {
			t.Fatalf("alert injected input %v", c.Argv)
		}
	}
	return reviewIdleResult{out.String(), stderr.String(), string(logged), cs, code, polls}
}

func TestReviewReadReuse(t *testing.T) {
	r := reviewRunIdle(t, "stable", 20, 3)
	poll, reads, total := 0, 0, 0
	finish := func() {
		if poll > 0 && poll <= 3 {
			t.Logf("working_poll=%d visible_reads=%d", poll, reads)
			if reads != 1 {
				t.Errorf("working probe already supplied a visible screen; alert must reuse it")
			}
		}
	}
	for _, c := range r.calls {
		s := strings.Join(c.Argv, " ")
		if s == "agent get worker" {
			finish()
			poll++
			reads = 0
		}
		if s == "agent read worker --source visible" {
			reads++
			if poll <= 3 {
				total++
			}
		}
	}
	finish()
	t.Logf("first_three_working_polls_visible_reads=%d exit=%d", total, r.code)
	if total != 3 {
		t.Fatalf("working read budget: got %d, want main's three visible reads", total)
	}
	if r.code != 6 {
		t.Fatal("fixture did not settle")
	}
}
func TestReviewClockAndTaskReset(t *testing.T) {
	a := &idleAlert{}
	n := time.Unix(1700000000, 0)
	for _, step := range []struct {
		task, state, screen string
		minute              int
		want                int
	}{{"A", "working", "one", 0, 0}, {"A", "working", "two", 19, 0}, {"A", "working", "two", 38, 0}, {"A", "working", "two", 39, 1}, {"A", "working", "three", 40, 1}, {"A", "working", "three", 61, 1}, {"B", "working", "three", 62, 1}, {"B", "working", "three", 82, 2}} {
		at := n.Add(time.Duration(step.minute) * time.Minute)
		if a.sampleDue(step.task, step.state, at, 20) {
			a.observe(step.screen, "worker", at, 20, "claude")
		}
		t.Logf("task=%s minute=%d count=%d", step.task, step.minute, a.count)
		if a.count != step.want {
			t.Fatalf("want %d", step.want)
		}
	}
}

func TestReviewWaitTransitions(t *testing.T) {
	for _, tc := range []struct {
		mode string
		want int
	}{{"stable", 1}, {"timer-control", 1}, {"claude-countdown", 1}, {"path-progress", 0}, {"ctx-footer", 1}} {
		t.Run(tc.mode, func(t *testing.T) {
			r := reviewRunIdle(t, tc.mode, 20, 27)
			var obj map[string]any
			if e := json.Unmarshal(bytes.TrimSpace([]byte(r.out)), &obj); e != nil {
				t.Fatal(e)
			}
			n := strings.Count(r.stderr, "has been working for")
			want := any(float64(tc.want))
			if tc.want == 0 {
				want = nil // idle_alerts is absent on a quiet run
			}
			if r.code != 6 || n != tc.want || obj["idle_alerts"] != want {
				t.Fatalf("mode=%s exit=%d alerts=%d JSON=%s stderr=%s", tc.mode, r.code, n, r.out, r.stderr)
			}
			t.Logf("mode=%s exit=%d alerts=%d idle_alerts=%v; zero prompt/send-keys", tc.mode, r.code, n, obj["idle_alerts"])
		})
	}
}
func TestReviewFooterMask(t *testing.T) {
	a := backgroundShellScreen
	b := strings.Replace(a, "ctx 31%", "ctx 32%", 1)
	if a == b || idleFingerprint(a, "claude") != idleFingerprint(b, "claude") {
		t.Fatal("ctx footer retained")
	}
	if idleFingerprint(a, "claude") == idleFingerprint(strings.Replace(a, "Gates are running", "Substantive progress", 1), "claude") {
		t.Fatal("ordinary output discarded")
	}
}
func TestReviewProviderFooters(t *testing.T) {
	raw, e := os.ReadFile("../cli/testdata/codex-0.164-compacted-windowless.txt")
	if e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct{ name, screen, from, to string }{{"codex", string(raw), "Context 0% used", "Context 71% used"}, {"claude", reviewClaudeCapture(t), "2h4m", "2h3m"}, {"claude-ctx", backgroundShellScreen, "ctx 31%", "ctx 32%"}} {
		t.Run(tc.name, func(t *testing.T) {
			changed := strings.Replace(tc.screen, tc.from, tc.to, 1)
			kind := strings.Split(tc.name, "-")[0]
			if changed == tc.screen || idleFingerprint(tc.screen, kind) != idleFingerprint(changed, kind) {
				t.Fatal("volatile fixture footer retained")
			}
			if idleFingerprint(tc.screen, kind) == idleFingerprint("processed src/5m.go item 2\n"+tc.screen, kind) {
				t.Fatal("substantive output lost")
			}
		})
	}
}
func TestReviewOrdinaryTimerSyntax(t *testing.T) {
	if idleFingerprint("processed (12s item 1)", "claude") == idleFingerprint("processed (12s item 2)", "claude") {
		t.Fatal("ordinary parenthetical output discarded")
	}
	for _, line := range []string{"processed src/5m.go item ", "processed at 10:40 item ", "Working on src/5m.go item ", "output contains ✻ item ", "[copy] ███ 10% item ", "Worked for 5m processed src/5m.go item ", "Reconnecting… 1/2 processed item "} {
		if idleFingerprint(line+"1", "claude") == idleFingerprint(line+"2", "claude") {
			t.Errorf("substantive progress lost: %q", line)
		}
	}
}
