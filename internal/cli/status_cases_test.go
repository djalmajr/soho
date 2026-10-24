package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	waitpkg "github.com/djalmajr/soho/internal/wait"
)

type waitStatusFixture struct {
	env   platform.Env
	state string
	bin   string
	root  string
}

func TestWaitQuotaOutputContract(t *testing.T) {
	row := "build\tp1\tgrok\timplementer\txai\t1\t/work\tnow\tgrok-4.7\tfull\timplementer\tbuild\n"
	f := newWaitStatusFixture(t, row, []fakecli.Rule{
		{Argv: []string{"agent", "get", "build"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`},
		{Argv: []string{"agent", "read", "build", "--source", "visible", "--lines", "20"}, Stdout: "hit your usage limit\ntry again in 2 hours\n"},
	})
	code, out, stderr := f.runCommand(t, "wait", "build", "--timeout", "2000")
	if code != 11 || !strings.Contains(stderr, "agent 'build' lane=build kind=grok model=grok-4.7") {
		t.Fatalf("wait-quota: code=%d stdout=%q stderr=%q", code, out, stderr)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{"agent": "build", "status": "quota", "report": "", "lane": "build", "kind": "grok", "model": "grok-4.7", "match": "hit your usage limit", "renewal": "try again in 2 hours"} {
		if value, present := got[key]; !present || value != want {
			t.Fatalf("wait JSON %s=%v present=%t want=%v", key, value, present, want)
		}
	}
	friction, err := os.ReadFile(filepath.Join(f.state, "friction.log"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(friction), "\n")
	if len(lines) < 2 {
		t.Fatalf("friction log missing quota entry: %q", friction)
	}
	line := lines[len(lines)-2]
	parts := strings.SplitN(line, "\t", 4)
	if len(parts) != 4 || parts[1] != "warning" || parts[2] != "wait" || !strings.Contains(parts[3], "hit your usage limit; renewal: try again in 2 hours") {
		t.Fatalf("friction entry does not match quota contract: %q", line)
	}
}

func newWaitStatusFixture(t *testing.T, rows string, rules []fakecli.Rule) *waitStatusFixture {
	t.Helper()
	env, cwd := commandFixture(t)
	base := t.TempDir()
	state := filepath.Join(base, "state")
	bin := filepath.Join(base, "bin")
	if err := os.MkdirAll(filepath.Join(state, "ws", "wait"), 0o700); err != nil {
		t.Fatal(err)
	}
	if rows != "" {
		if err := os.WriteFile(filepath.Join(state, "ws", "agents.tsv"), []byte("# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\n"+rows), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	rules = append(rules, fakecli.Rule{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`})
	if _, err := fakecli.Install(t, bin, "herdr", rules); err != nil {
		t.Fatal(err)
	}
	clean := envFrom(testutil.CleanEnv(t))
	for key, value := range env {
		if key == "HOME" || key == "XDG_CONFIG_HOME" || key == "SOHO_SKILL_DIR" || key == "HERDR_WORKSPACE_ID" {
			clean[key] = value
		}
	}
	clean["PATH"] = bin
	clean["HERDR_ENV"] = "1"
	clean["SOHO_DIR"] = state
	clean["HERDR_WORKSPACE_ID"] = "ws"
	clean["HERDR_SOCKET_PATH"] = filepath.Join(base, "missing.sock")
	clean["SOHO_WAIT_POLL_MS"] = "1"
	clean = withFakeCLI(clean, bin)
	return &waitStatusFixture{env: clean, state: filepath.Join(state, "ws"), bin: bin, root: cwd}
}

func (f *waitStatusFixture) run(t *testing.T, args ...string) (int, string, string) {
	return f.runCommand(t, append([]string{"status"}, args...)...)
}

func (f *waitStatusFixture) runWait(t *testing.T, args ...string) (int, string, string) {
	return f.runCommand(t, append([]string{"wait"}, args...)...)
}

func (f *waitStatusFixture) runCommand(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, stderr bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &stderr
	code := Run(args, f.env)
	platform.Stdout, platform.Stderr = oldOut, oldErr
	return code, out.String(), stderr.String()
}

func (f *waitStatusFixture) calls(t *testing.T) []fakecli.Call {
	t.Helper()
	calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return calls
}

// advancingWaitClock makes a wait --timeout deadline deterministic: it
// replaces platform.Now (restored in t.Cleanup) with a clock that starts at
// a fixed instant and advances by budget/20 on every call, so the deadline
// lapses only after a bounded number of probe rounds instead of after real
// host time. The deadline stays live as a regression detector: a "done never
// reached" bug still ends quickly with the wrong status (code 9), never a
// hang.
func advancingWaitClock(t *testing.T, budget time.Duration) {
	t.Helper()
	base := time.Date(2026, 10, 3, 15, 26, 48, 0, time.UTC)
	step := budget / 20
	origNow := platform.Now
	platform.Now = func() time.Time {
		now := base
		base = base.Add(step)
		return now
	}
	t.Cleanup(func() { platform.Now = origNow })
}

func TestStatusJavaScriptCases(t *testing.T) {
	const row = "worker\tp0a\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\timplementer\tbuild\n"
	t.Run("status: no names with an empty roster exits 0 pointing at the state dir", func(t *testing.T) {
		f := newWaitStatusFixture(t, "", nil)
		code, out, stderr := f.run(t)
		if code != 0 || out != "" || !strings.Contains(stderr, "status: no agents in the roster (state dir: "+f.state+")") {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, out, stderr)
		}
	})
	t.Run("status: no names covers the whole roster in roster order, same as the named run", func(t *testing.T) {
		rows := "alpha\tp0a\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\timplementer\tbuild\n" +
			"beta\tp0b\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\timplementer\tbuild\n"
		rules := []fakecli.Rule{
			{Argv: []string{"agent", "get", "alpha"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
			{Argv: []string{"agent", "read", "alpha", "--source", "visible"}, Stdout: "busy\n"},
			{Argv: []string{"agent", "get", "beta"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`},
		}
		f := newWaitStatusFixture(t, rows, rules)
		code, out, stderr := f.run(t)
		named := newWaitStatusFixture(t, rows, rules)
		namedCode, namedOut, namedStderr := named.run(t, "alpha", "beta")
		if code != 0 || namedCode != 0 || stderr != "" || namedStderr != "" || out != namedOut {
			t.Fatalf("code=%d named=%d stderr=%q namedStderr=%q out=%q namedOut=%q", code, namedCode, stderr, namedStderr, out, namedOut)
		}
		want := "alpha\tworking\t\t-\t-\nbeta\tno-report-yet\t\t-\t-\n"
		if out != want {
			t.Fatalf("output=%q want=%q", out, want)
		}
	})
	t.Run("status: an auth screen reports provider-error on the first call, rc 14", func(t *testing.T) { // JS: "status: an auth screen reports provider-error on the first call, rc 14"
		f := newWaitStatusFixture(t, row, []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle","state_change_seq":3}}}`},
			{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped", "--lines", "40"}, Stdout: "Error: 401 Unauthorized: invalid API key\n"},
		})
		code, out, stderr := f.run(t, "worker")
		if code != 14 || stderr != "" || !strings.Contains(out, `"status":"provider-error"`) || !strings.Contains(out, "401 Unauthorized: invalid API key") {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, out, stderr)
		}
	})
	t.Run("status: the quota wins over the provider stop on the same screen, rc 11", func(t *testing.T) { // JS: "status: the quota wins over the provider stop on the same screen, rc 11"
		f := newWaitStatusFixture(t, row, []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`},
			{Argv: []string{"agent", "read", "worker", "--source", "visible", "--lines", "20"}, Stdout: "Error 401: quota exceeded for this account\n"},
		})
		code, out, stderr := f.run(t, "worker")
		if code != 11 || !strings.Contains(stderr, "quota:") || !strings.Contains(out, `"status":"quota"`) || strings.Contains(out, `"status":"provider-error"`) {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, out, stderr)
		}
	})
	t.Run("status: a capacity worker prints the JSON line with the cause, rc 14", func(t *testing.T) { // JS: "status: a capacity worker prints the JSON line with the cause, rc 14"
		f := newWaitStatusFixture(t, row, []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`},
			{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped", "--lines", "40"}, Stdout: "Error: overloaded\n"},
		})
		code, out, stderr := f.run(t, "worker")
		if code != 14 || stderr != "" || !strings.Contains(out, `"status":"capacity"`) || !strings.Contains(out, `"cause":"Error: overloaded"`) {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, out, stderr)
		}
	})
	t.Run("status: the rc rank 11 > 4 > 14 > 0 across agents", func(t *testing.T) { // JS: "status: the rc rank 11 > 4 > 14 > 0 across agents"
		rows := "quota\tp0a\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\timplementer\tbuild\n" +
			"stuck\tp0b\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\timplementer\tbuild\n"
		f := newWaitStatusFixture(t, rows, []fakecli.Rule{
			{Argv: []string{"agent", "get", "quota"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`},
			{Argv: []string{"agent", "read", "quota", "--source", "visible", "--lines", "20"}, Stdout: "quota exceeded for this account\n"},
			{Argv: []string{"agent", "get", "stuck"}, Stderr: `{"error":{"code":"permission_denied"}}`, Code: 1},
		})
		code, out, _ := f.run(t, "quota", "stuck")
		if code != 11 || !strings.Contains(out, `"status":"quota"`) || !strings.Contains(out, "stuck\tunavailable\t") {
			t.Fatalf("code=%d stdout=%q", code, out)
		}
	})
	t.Run("status: a blocked worker on a question screen reports question, rc 7", func(t *testing.T) { // JS: "status: a blocked worker on a question screen reports question, rc 7"
		questionRow := "worker\tp0a\tcodex\timplementer\topenai\t\t\t\tmodel-x\t\timplementer\tbuild\n"
		f := newWaitStatusFixture(t, questionRow, []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"blocked"}}}`},
			{Argv: []string{"agent", "read", "worker", "--source", "visible", "--lines", "40"}, Stdout: "  1. Use the local cache\n  2. Fetch from remote\n\nEnter to submit answer, esc to cancel\n"},
		})
		code, out, stderr := f.run(t, "worker")
		if code != 7 || stderr != "" || !strings.Contains(out, `"status":"question"`) || !strings.Contains(out, "Use the local cache") {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, out, stderr)
		}
		if !strings.Contains(out, `"task_s":null,"activity_s":null`) {
			t.Fatalf("question age keys/order missing: %q", out)
		}
	})
	t.Run("status: a blocked worker on an approval screen keeps the old TSV line", func(t *testing.T) { // JS: "status: a blocked worker on an approval screen keeps the old TSV line"
		f := newWaitStatusFixture(t, row, []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"blocked"}}}`},
			{Argv: []string{"agent", "read", "worker", "--source", "visible", "--lines", "40"}, Stdout: "Allow command?\nPress enter to confirm or esc to cancel\n"},
		})
		code, out, stderr := f.run(t, "worker")
		if code != 0 || stderr != "" || !strings.HasPrefix(out, "worker\tblocked\t") || !strings.Contains(out, "\t-\t-\n") {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, out, stderr)
		}
	})
	t.Run("status: a not-received marker reports not-received, rc 15, no key sent", func(t *testing.T) { // JS: "status: a not-received marker reports not-received, rc 15, no key sent"
		f := newWaitStatusFixture(t, row, []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle","state_change_seq":5}}}`},
		})
		if err := os.WriteFile(filepath.Join(f.state, "wait", "worker.not-received"), []byte("123 5 /tmp/brief.md\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, stderr := f.run(t, "worker")
		if code != 15 || stderr != "" || !strings.HasPrefix(out, "worker\tnot-received\t") || strings.Contains(out, `"status":"not-received"`) {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, out, stderr)
		}
		if !strings.HasSuffix(out, "\t-\t-\n") {
			t.Fatalf("not-received age columns missing: %q", out)
		}
		for _, call := range f.calls(t) {
			if strings.Contains(strings.Join(call.Argv, " "), "send-keys") {
				t.Fatalf("status sent a key: %#v", call.Argv)
			}
		}
	})
	t.Run("status: a line without a pane keeps the name query (any pane)", func(t *testing.T) { // JS: "status: a line without a pane keeps the name query (any pane)"
		noPane := "worker\t\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\timplementer\tbuild\n"
		f := newWaitStatusFixture(t, noPane, []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`}})
		code, out, stderr := f.run(t, "worker")
		if code != 0 || stderr != "" || !strings.HasPrefix(out, "worker\tworking\t") {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, out, stderr)
		}
		calls := f.calls(t)
		if len(calls) == 0 || strings.Join(calls[0].Argv, " ") != "agent get worker" {
			t.Fatalf("query did not use the agent name: %#v", f.calls(t))
		}
	})
	t.Run("status: activity_s is changed on a moved screen, the age on an equal hash, unknown without markers", func(t *testing.T) {
		fixed := time.Unix(2_000_000_000, 0)
		oldNow := platform.Now
		platform.Now = func() time.Time { return fixed }
		t.Cleanup(func() { platform.Now = oldNow })
		f := newWaitStatusFixture(t, row, []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
			{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Stdout: "screen moved\n"},
		})
		wd := filepath.Join(f.state, "wait")
		writeMarker := func(name, value string) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(wd, "worker."+name), []byte(value), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		// (a) no marker: the activity column is unknown.
		code, out, _ := f.run(t, "worker")
		if code != 0 || !strings.HasPrefix(out, "worker\tworking\t") || !strings.HasSuffix(out, "\t-\t-\n") {
			t.Fatalf("no markers: %d %q", code, out)
		}
		// (c) a recorded hash the screen moved away from: changed, no age.
		writeMarker("stuck-hash", "999\n")
		writeMarker("probe-at", "1999999900\n")
		code, out, _ = f.run(t, "worker")
		if code != 0 || !strings.HasPrefix(out, "worker\tworking\t") || !strings.HasSuffix(out, "\tchanged\n") {
			t.Fatalf("changed screen: %d %q", code, out)
		}
		// (b) the equal hash with a change date: the age.
		writeMarker("stuck-hash", strconv.FormatUint(uint64(waitpkg.CksumField(waitpkg.NormalizeScreen("screen moved\n"))), 10)+"\n")
		writeMarker("activity-at", "1999999945\n")
		code, out, _ = f.run(t, "worker")
		if code != 0 || !strings.HasPrefix(out, "worker\tworking\t") || !strings.HasSuffix(out, "\t55\n") {
			t.Fatalf("unchanged screen: %d %q", code, out)
		}
	})
	t.Run("status: task_s is the marker-to-report duration (or to now, or unknown)", func(t *testing.T) { // JS: "status: task_s is the marker-to-report duration (or to now, or unknown)"
		fixed := time.Unix(2_000_000_000, 0)
		oldNow := platform.Now
		platform.Now = func() time.Time { return fixed }
		t.Cleanup(func() { platform.Now = oldNow })
		f := newWaitStatusFixture(t, row, []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`}})
		lastReport := filepath.Join(f.state, "last-report-worker")
		if err := os.WriteFile(lastReport, []byte("missing.md\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		then := fixed.Add(-90 * time.Second)
		if err := os.Chtimes(lastReport, then, then); err != nil {
			t.Fatal(err)
		}
		code, out, _ := f.run(t, "worker")
		if code != 0 || !strings.Contains(out, "\t90\t") {
			t.Fatalf("pending task age: %d %q", code, out)
		}
		if err := os.Remove(lastReport); err != nil {
			t.Fatal(err)
		}
		code, out, _ = f.run(t, "worker")
		if code != 0 || !strings.Contains(out, "\t-\t") {
			t.Fatalf("unknown task age: %d %q", code, out)
		}
	})
	t.Run("status: line whose name is alive in another pane reports gone (and roster shows gone)", func(t *testing.T) { // JS: "status: a line whose name is alive in another pane reports gone (and roster shows gone)"
		f := newWaitStatusFixture(t, row, []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
			{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[{"name":"worker","pane_id":"p-other"}]}}`},
		})
		code, out, stderr := f.run(t, "worker")
		if code != 0 || stderr != "" || !strings.HasPrefix(out, "worker\tgone\t") {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, out, stderr)
		}
	})
}

func TestStatusJavaScriptAgeColumns(t *testing.T) {
	t.Run("status: the quota, provider-error, capacity and question JSON lines end with task_s and activity_s", func(t *testing.T) { // JS: "status: the quota, provider-error, capacity and question JSON lines end with task_s and activity_s"
		cases := []struct {
			name, agentStatus, kind, screenSource, screen string
		}{
			{"quota", "idle", "claude", "visible", "quota exceeded for this account\n"},
			{"provider", "idle", "claude", "recent-unwrapped", "Error: 401 Unauthorized: invalid API key\n"},
			{"capacity", "idle", "claude", "recent-unwrapped", "Error: overloaded\n"},
			{"question", "blocked", "codex", "visible", "  1. Keep local data\n  2. Fetch from remote\n\nEnter to submit answer, esc to cancel\n"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				row := "worker\tp0a\t" + tc.kind + "\timplementer\topenai\t\t\t\tmodel-x\t\timplementer\tbuild\n"
				f := newWaitStatusFixture(t, row, []fakecli.Rule{
					{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"` + tc.agentStatus + `"}}}`},
					{Argv: []string{"agent", "read", "worker", "--source", tc.screenSource, "--lines", "20"}, Stdout: tc.screen},
					{Argv: []string{"agent", "read", "worker", "--source", tc.screenSource, "--lines", "40"}, Stdout: tc.screen},
				})
				code, out, _ := f.run(t, "worker")
				wantCode := 0
				if tc.name == "quota" {
					wantCode = 11
				}
				if tc.name == "provider" || tc.name == "capacity" {
					wantCode = 14
				}
				if tc.name == "question" {
					wantCode = 7
				}
				if code != wantCode || !strings.Contains(out, `"task_s":null,"activity_s":null}`) || strings.Contains(out, "activity_changed") {
					t.Fatalf("code=%d stdout=%q want rc=%d ending in the two age keys, no activity_changed", code, out, wantCode)
				}
			})
		}
	})
}

func TestStatusJavaScriptQueuedCases(t *testing.T) {
	const row = "worker\tp0a\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\timplementer\tbuild\n"
	queued := "123 5 /tmp/worker-brief.md\n"
	readRules := func(state, visible, recent string) []fakecli.Rule {
		return []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"` + state + `","state_change_seq":5}}}`},
			{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Stdout: visible},
			{Argv: []string{"agent", "read", "worker", "--source", "visible", "--lines", "20"}, Stdout: visible},
			{Argv: []string{"agent", "read", "worker", "--source", "visible", "--lines", "40"}, Stdout: visible},
			{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped", "--lines", "40"}, Stdout: recent},
		}
	}
	t.Run("status: a queued marker stays working while the target is working", func(t *testing.T) { // JS: "status: a queued marker stays working while the target is working"
		f := newWaitStatusFixture(t, row, readRules("working", "busy\n", "busy\n"))
		marker := filepath.Join(f.state, "wait", "worker.queued")
		if err := os.WriteFile(marker, []byte(queued), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, _ := f.run(t, "worker")
		if code != 0 || !strings.HasPrefix(out, "worker\tworking\t") {
			t.Fatalf("code=%d out=%q", code, out)
		}
		if _, err := os.Stat(marker); err != nil {
			t.Fatalf("status removed queued marker: %v", err)
		}
	})
	t.Run("status: queued input is not-received, while quota after queued keeps exit 11", func(t *testing.T) { // JS: "status: queued input is not-received, while quota after queued keeps exit 11"
		f := newWaitStatusFixture(t, row, readRules("idle", "quota exceeded for this account\n", "Read the file /tmp/worker-brief.md in full and execute it.\n"))
		marker := filepath.Join(f.state, "wait", "worker.queued")
		if err := os.WriteFile(marker, []byte(queued), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, _ := f.run(t, "worker")
		if code != 11 || !strings.Contains(out, `"status":"quota"`) {
			t.Fatalf("code=%d out=%q", code, out)
		}
	})
	t.Run("status: provider failure wins when the queued prompt path is in the input box", func(t *testing.T) { // JS: "status: provider failure wins when the queued prompt path is in the input box"
		f := newWaitStatusFixture(t, row, readRules("idle", "prompt input\n", "Read the file /tmp/worker-brief.md in full and execute it.\nError: 401 Unauthorized: invalid API key\n"))
		marker := filepath.Join(f.state, "wait", "worker.queued")
		if err := os.WriteFile(marker, []byte(queued), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, _ := f.run(t, "worker")
		if code != 14 || !strings.Contains(out, `"status":"provider-error"`) {
			t.Fatalf("code=%d out=%q", code, out)
		}
		if _, err := os.Stat(marker); err != nil {
			t.Fatalf("read-only status removed queued marker: %v", err)
		}
	})
	t.Run("status: a queued input path never overrides a working state", func(t *testing.T) { // JS: "status: a queued input path never overrides a working state"
		f := newWaitStatusFixture(t, row, readRules("working", "Read the file /tmp/worker-brief.md\n", "Read the file /tmp/worker-brief.md\n"))
		if err := os.WriteFile(filepath.Join(f.state, "wait", "worker.queued"), []byte(queued), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, _ := f.run(t, "worker")
		if code != 0 || !strings.HasPrefix(out, "worker\tworking\t") {
			t.Fatalf("code=%d out=%q", code, out)
		}
	})
	t.Run("status: a state change since the marker is the normal status, marker kept", func(t *testing.T) { // JS: "status: a state change since the marker is the normal status, marker kept"
		f := newWaitStatusFixture(t, row, []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle","state_change_seq":6}}}`}})
		marker := filepath.Join(f.state, "wait", "worker.not-received")
		if err := os.WriteFile(marker, []byte("123 5 /tmp/worker-brief.md\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, _ := f.run(t, "worker")
		if code != 0 || !strings.HasPrefix(out, "worker\tno-report-yet\t") {
			t.Fatalf("code=%d out=%q", code, out)
		}
		if _, err := os.Stat(marker); err != nil {
			t.Fatalf("status removed stale marker: %v", err)
		}
	})
}

func TestWaitJavaScriptRankCases(t *testing.T) {
	t.Run("wait: the rank order 4 > 11 > 7 > 6 in any argument order", func(t *testing.T) { // JS: "wait: the rank order 4 > 11 > 7 > 6 in any argument order"
		roster := "stuck\tp0a\tclaude\timplementer\t\t\t\t\t\t\t\t\n" +
			"dead\tp0b\tclaude\timplementer\t\t\t\t\t\t\t\t\n" +
			"blocked\tp0c\tclaude\ttasker\t\t\t\t\t\t\t\t\n" +
			"quota\tp0d\tclaude\timplementer\t\t\t\t\t\t\t\t\n"
		f := newWaitStatusFixture(t, roster, []fakecli.Rule{
			{Argv: []string{"agent", "get", "stuck"}, Stderr: `{"error":{"code":"permission_denied","message":"denied"}}`, Code: 1},
			{Argv: []string{"agent", "get", "dead"}, Stderr: `{"error":{"code":"agent_not_found"}}`, Code: 137},
			{Argv: []string{"agent", "get", "blocked"}, Stdout: `{"result":{"agent":{"agent_status":"blocked"}}}`},
			{Argv: []string{"agent", "read", "blocked", "--source", "visible", "--lines", "40"}, Stdout: "Allow command?\nPress enter to confirm\n"},
			{Argv: []string{"agent", "get", "quota"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`},
			{Argv: []string{"agent", "read", "quota", "--source", "visible", "--lines", "20"}, Stdout: "hit your usage limit\n"},
		})
		for _, agent := range []string{"blocked", "quota"} {
			if err := os.WriteFile(filepath.Join(f.state, "wait", agent+".blocked"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		for _, args := range [][]string{{"stuck", "dead", "blocked", "quota", "--timeout", "200"}, {"quota", "blocked", "dead", "stuck", "--timeout", "200"}} {
			code, out, _ := f.runWait(t, args...)
			if code != 4 {
				t.Fatalf("rank %v = %d, output %q", args, code, out)
			}
		}
		for _, args := range [][]string{{"blocked", "quota", "--timeout", "200"}, {"quota", "blocked", "--timeout", "200"}} {
			code, _, _ := f.runWait(t, args...)
			if code != 11 {
				t.Fatalf("rank %v = %d, want quota 11", args, code)
			}
		}
		for _, args := range [][]string{{"blocked", "dead", "--timeout", "200"}, {"dead", "blocked", "--timeout", "200"}} {
			code, _, _ := f.runWait(t, args...)
			if code != 7 {
				t.Fatalf("rank %v = %d, want blocked 7", args, code)
			}
		}
	})
}

func TestWaitJavaScriptDoneReportCases(t *testing.T) {
	writeReport := func(t *testing.T, f *waitStatusFixture, agent, role, body string) {
		t.Helper()
		// These cases test report formatting, not a pane that is still working.
		f.env["SOHO_REPORT_WORKING_MAX"] = "0"
		report := filepath.Join(f.state, "reports", agent+".md")
		if err := os.MkdirAll(filepath.Dir(report), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(report, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.state, "last-report-"+agent), []byte(report+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_ = role
	}
	t.Run("wait: a done report with partial items marks the JSON line and warns once", func(t *testing.T) { // JS: "wait: a done report with partial items marks the JSON line and warns once"
		row := "review\tp0a\tclaude\treviewer\tanthropic\t\t\t\tmodel-x\t\t\treview\n"
		f := newWaitStatusFixture(t, row, nil)
		advancingWaitClock(t, 200*time.Millisecond)
		body := "# Report\n\n| item | state |\n| --- | --- |\n| evidence | [partial] |\n"
		writeReport(t, f, "review", "reviewer", body)
		for run := 0; run < 2; run++ {
			code, out, stderr := f.runWait(t, "review", "--timeout", "200")
			if code != 0 || !strings.Contains(out, `"status":"done"`) || !strings.Contains(out, `"partial":1`) {
				t.Fatalf("run %d: code=%d out=%q err=%q", run, code, out, stderr)
			}
		}
		log, err := os.ReadFile(filepath.Join(f.state, "friction.log"))
		if err != nil || strings.Count(string(log), "marks 1 item(s) partial") != 1 {
			t.Fatalf("partial warning count: %q err=%v", log, err)
		}
	})
	t.Run("wait: a clean done report keeps the exact line of today", func(t *testing.T) { // JS: "wait: a clean done report keeps the exact line of today"
		row := "worker\tp0a\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n"
		f := newWaitStatusFixture(t, row, nil)
		advancingWaitClock(t, 200*time.Millisecond)
		writeReport(t, f, "worker", "implementer", "# Report\nfinished\n")
		code, out, stderr := f.runWait(t, "worker", "--timeout", "200")
		wantReport := filepath.Join(f.state, "reports", "worker.md")
		var line struct {
			Agent  string `json:"agent"`
			Report string `json:"report"`
			Status string `json:"status"`
		}
		parseErr := json.Unmarshal([]byte(strings.TrimSpace(out)), &line)
		if code != 0 || parseErr != nil || line.Agent != "worker" || line.Status != "done" || line.Report != wantReport || strings.Contains(stderr, "partial") {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
	})
	t.Run("wait: the header and partial items coexist in the done line", func(t *testing.T) { // JS: "wait: the header and partial items coexist in the done line"
		row := "review\tp0a\tclaude\treviewer\tanthropic\t\t\t\tmodel-x\t\t\t\n"
		f := newWaitStatusFixture(t, row, nil)
		advancingWaitClock(t, 200*time.Millisecond)
		writeReport(t, f, "review", "reviewer", "findings: 1 (P0 0, P1 1, P2 0, P3 0) | verdict: fail\n\n| issue | [partial] |\n")
		code, out, _ := f.runWait(t, "review", "--timeout", "200")
		for _, key := range []string{`"verdict":"fail"`, `"findings":1`, `"severity":{"P0":0,"P1":1,"P2":0,"P3":0}`, `"partial":1`} {
			if !strings.Contains(out, key) {
				t.Errorf("done line lacks %s: %q", key, out)
			}
		}
		if code != 0 {
			t.Fatalf("code=%d out=%q", code, out)
		}
	})
	t.Run("wait: a review report without the header warns for all four review roles", func(t *testing.T) { // JS: "wait: a review report without the header warns for all four review roles"
		rows := "review1\tp1\tclaude\treviewer\t\t\t\t\t\t\t\t\nreview2\tp2\tclaude\tsecurity-reviewer\t\t\t\t\t\t\t\nreview3\tp3\tclaude\tui-reviewer\t\t\t\t\t\t\t\nreview4\tp4\tclaude\tinspector\t\t\t\t\t\t\t\n"
		f := newWaitStatusFixture(t, rows, nil)
		advancingWaitClock(t, 200*time.Millisecond)
		for _, agent := range []string{"review1", "review2", "review3", "review4"} {
			writeReport(t, f, agent, "", "# Review\nfindings below\n")
		}
		code, out, stderr := f.runWait(t, "review1", "review2", "review3", "review4", "--timeout", "200")
		if code != 0 || strings.Count(out, `"status":"done"`) != 4 || strings.Count(stderr, "has no 'findings:") != 4 {
			t.Fatalf("code=%d done=%q warnings=%d stderr=%q", code, out, strings.Count(stderr, "has no 'findings:"), stderr)
		}
	})
}

func TestWaitJavaScriptTimeoutAndAnyCases(t *testing.T) {
	t.Run("wait: timeout 9 for a working agent", func(t *testing.T) { // JS: "wait: timeout 9 for a working agent"
		row := "worker\tp0a\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n"
		f := newWaitStatusFixture(t, row, []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"working","state_change_seq":5}}}`}})
		code, out, stderr := f.runWait(t, "worker", "--timeout", "10")
		if code != 9 || !strings.Contains(out, `"status":"timeout"`) || !strings.Contains(out, `"state":"working"`) || !strings.Contains(stderr, "timeout waiting for 'worker'") {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
	})
	t.Run("wait: --any returns on the first done; notify=on notifies", func(t *testing.T) { // JS: "wait: --any returns on the first done; notify=on notifies"
		rows := "ready\tp0a\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\nworking\tp0b\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n"
		f := newWaitStatusFixture(t, rows, []fakecli.Rule{
			{Argv: []string{"agent", "get", "working"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
			{Argv: []string{"agent", "read", "working", "--source", "visible"}, Stdout: "still running\n"},
			{Argv: []string{"notification", "show"}, ArgvPrefix: true},
		})
		writeReport := filepath.Join(f.state, "reports", "ready.md")
		if err := os.MkdirAll(filepath.Dir(writeReport), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(writeReport, []byte("done\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.state, "last-report-ready"), []byte(writeReport+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		f.env["SOHO_NOTIFY"] = "on"
		code, out, stderr := f.runWait(t, "ready", "working", "--timeout", "2000", "--any")
		if code != 0 || stderr != "" || strings.Count(out, `"status":"done"`) != 1 {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
		calls := f.calls(t)
		found := false
		for _, call := range calls {
			if len(call.Argv) > 1 && call.Argv[0] == "notification" && call.Argv[1] == "show" {
				found = true
			}
		}
		if !found {
			t.Fatalf("notification was not sent: %#v", calls)
		}
	})
}

func TestWaitJavaScriptCheckpointCases(t *testing.T) {
	t.Run("wait: a real screen change observed between probes of one wait is a neutral checkpoint", func(t *testing.T) { // JS: "wait: a real screen change observed between probes of one wait is a neutral checkpoint"
		row := "worker\tp0a\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n"
		f := newWaitStatusFixture(t, row, []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
			{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Call: 1, Stdout: "Starting dependency installation\n"},
			{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Call: 2, Stdout: "Compiling workspace\n"},
			{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Stdout: "Compiling workspace\n"},
		})
		code, out, stderr := f.runWait(t, "worker", "--timeout", "30")
		if code != 9 || !strings.Contains(out, `"checkpoint":true`) || !strings.Contains(stderr, "checkpoint: 'worker' is still working") {
			markers, _ := os.ReadDir(filepath.Join(f.state, "wait"))
			t.Fatalf("code=%d out=%q stderr=%q calls=%#v markers=%#v", code, out, stderr, f.calls(t), markers)
		}
	})
	t.Run("wait: a counter-only screen change is treated as static (not active)", func(t *testing.T) { // JS: "wait: a counter-only screen change is treated as static (not active)"
		row := "worker\tp0a\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n"
		f := newWaitStatusFixture(t, row, []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
			{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Call: 1, Stdout: "Running tests 42%\n"},
			{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Stdout: "Running tests 57%\n"},
		})
		code, out, _ := f.runWait(t, "worker", "--timeout", "20")
		if code != 9 || !strings.Contains(out, `"checkpoint":false`) {
			t.Fatalf("code=%d out=%q", code, out)
		}
	})
}

func TestWaitTimeoutActivityAgeCases(t *testing.T) {
	// --timeout 1: the deadline passes while the first probe runs, so the
	// wait makes exactly one probe read and one timeout read of the visible
	// screen.
	row := "worker\tp0a\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n"
	screen := "Compiling the workspace\n"
	working := []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`}}
	record := func(t *testing.T, out string) map[string]any {
		t.Helper()
		var line map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &line); err != nil {
			t.Fatalf("timeout line %q: %v", out, err)
		}
		return line
	}
	t.Run("wait: a timeout on a moved screen has no age, activity_changed and a checkpoint (R-RC7D)", func(t *testing.T) {
		moved := "Linking the binary\n"
		rules := append(append([]fakecli.Rule{}, working...),
			fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Call: 1, Stdout: screen},
			fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Call: 2, Stdout: moved},
			fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Stdout: screen})
		f := newWaitStatusFixture(t, row, rules)
		code, out, stderr := f.runWait(t, "worker", "--timeout", "1")
		line := record(t, out)
		if code != 9 || line["activity_age_s"] != nil || line["activity_changed"] != true || line["checkpoint"] != true || !strings.Contains(out, `"checkpoint":true,"activity_age_s":null,"activity_changed":true}`) || !strings.Contains(stderr, "is still working (screen changed within the last ") || strings.Contains(stderr, "timeout waiting for 'worker'") {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
	})
	t.Run("wait: a timeout on an equal screen keeps its age without activity_changed", func(t *testing.T) {
		f := newWaitStatusFixture(t, row, append(append([]fakecli.Rule{}, working...),
			fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Stdout: screen}))
		hash := strconv.FormatUint(uint64(waitpkg.CksumField(waitpkg.NormalizeScreen(screen))), 10)
		for name, value := range map[string]string{"worker.stuck-hash": hash + "\n", "worker.activity-at": strconv.FormatInt(time.Now().Unix()-5, 10) + "\n"} {
			if err := os.WriteFile(filepath.Join(f.state, "wait", name), []byte(value), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		code, out, stderr := f.runWait(t, "worker", "--timeout", "1")
		line := record(t, out)
		age, ok := line["activity_age_s"].(float64)
		if code != 9 || !ok || age < 4 || age > 7 || line["checkpoint"] != true || line["activity_changed"] != nil || strings.Contains(out, "activity_changed") || !strings.Contains(stderr, "checkpoint: 'worker' is still working") {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
	})
	t.Run("wait: a timeout with no markers keeps a null age without activity_changed", func(t *testing.T) {
		f := newWaitStatusFixture(t, row, append(append([]fakecli.Rule{}, working...),
			fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Stdout: screen}))
		code, out, stderr := f.runWait(t, "worker", "--timeout", "1")
		line := record(t, out)
		if code != 9 || line["activity_age_s"] != nil || line["activity_changed"] != nil || line["checkpoint"] != false || strings.Contains(out, "activity_changed") || !strings.Contains(stderr, "timeout waiting for 'worker'") {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
	})
}

func TestStatusRemainingProviderAndRankCases(t *testing.T) {
	row := func(name, kind, role string) string {
		return name + "\tp0a\t" + kind + "\t" + role + "\t\t\t\t\tmodel-x\t\t\t\n"
	}
	t.Run("status: a provider-error worker prints the JSON line, rc 14", func(t *testing.T) { // JS: "status: a provider-error worker prints the JSON line, rc 14"
		f := newWaitStatusFixture(t, row("worker", "claude", "implementer"), []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`},
			{Argv: []string{"agent", "read", "worker", "--source", "visible", "--lines", "20"}, Stdout: ""},
			{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped", "--lines", "40"}, Stdout: "Error: Connection error.\n"},
		})
		code, out, stderr := f.run(t, "worker")
		if code != 14 || stderr != "" || !strings.Contains(out, `"status":"provider-error"`) || !strings.Contains(out, `"cause":"Error: Connection error."`) {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
	})
	t.Run("status: an auth screen on a blocked worker reports provider-error, a question still reports question", func(t *testing.T) { // JS: "status: an auth screen on a blocked worker reports provider-error, a question still reports question"
		f := newWaitStatusFixture(t, row("auth", "codex", "implementer"), []fakecli.Rule{
			{Argv: []string{"agent", "get", "auth"}, Stdout: `{"result":{"agent":{"agent_status":"blocked"}}}`},
			{Argv: []string{"agent", "read", "auth", "--source", "visible", "--lines", "40"}, Stdout: "■ Failed to refresh access token: the refresh token was revoked, please log in again\n"},
			{Argv: []string{"agent", "read", "auth", "--source", "recent-unwrapped", "--lines", "40"}, Stdout: "■ Failed to refresh access token: the refresh token was revoked, please log in again\n"},
		})
		code, out, stderr := f.run(t, "auth")
		if code != 14 || !strings.Contains(out, `"status":"provider-error"`) || !strings.Contains(out, "refresh token was revoked") {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
		q := newWaitStatusFixture(t, row("question", "codex", "implementer"), []fakecli.Rule{
			{Argv: []string{"agent", "get", "question"}, Stdout: `{"result":{"agent":{"agent_status":"blocked"}}}`},
			{Argv: []string{"agent", "read", "question", "--source", "visible", "--lines", "40"}, Stdout: "  1. Use the local cache\n  2. Fetch from remote\n\nEnter to submit answer, esc to cancel\n"},
		})
		questionCode, questionOut, questionErr := q.run(t, "question")
		if questionCode != 7 || !strings.Contains(questionOut, `"status":"question"`) || !strings.Contains(questionOut, "Enter to submit answer") {
			t.Fatalf("question code=%d out=%q stderr=%q", questionCode, questionOut, questionErr)
		}
	})
	t.Run("status: the quota wins over an auth failure on the same screen, rc 11", func(t *testing.T) { // JS: "status: the quota wins over an auth failure on the same screen, rc 11"
		f := newWaitStatusFixture(t, row("worker", "claude", "implementer"), []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`},
			{Argv: []string{"agent", "read", "worker", "--source", "visible", "--lines", "20"}, Stdout: "Individual quota reached\nError: 401 Unauthorized\n"},
		})
		code, out, stderr := f.run(t, "worker")
		if code != 11 || !strings.Contains(out, `"status":"quota"`) || !strings.Contains(out, "Individual quota reached") {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
	})
	t.Run("status: a current question wins over an older 401 in the screen history, rc 7", func(t *testing.T) { // JS: "status: a current question wins over an older 401 in the screen history, rc 7"
		f := newWaitStatusFixture(t, row("worker", "codex", "implementer"), []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"blocked"}}}`},
			{Argv: []string{"agent", "read", "worker", "--source", "visible", "--lines", "40"}, Stdout: "Error: 401 Unauthorized: bad credentials, will not retry\n  1. Use the local cache\n  2. Fetch from remote\n\nEnter to submit answer, esc to cancel\n"},
		})
		code, out, stderr := f.run(t, "worker")
		if code != 7 || !strings.Contains(out, `"status":"question"`) || !strings.Contains(out, "Enter to submit answer") {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
	})
}

func TestStatusRemainingMarkerAndColumnCases(t *testing.T) {
	t.Run("status: the marker changes nothing while the agent is working or blocked", func(t *testing.T) { // JS: "status: the marker changes nothing while the agent is working or blocked"
		rows := "working\tp0a\tclaude\timplementer\t\t\t\t\t\t\t\t\nblocked\tp0b\tclaude\timplementer\t\t\t\t\t\t\t\n"
		f := newWaitStatusFixture(t, rows, []fakecli.Rule{
			{Argv: []string{"agent", "get", "working"}, Stdout: `{"result":{"agent":{"agent_status":"working","state_change_seq":5}}}`},
			{Argv: []string{"agent", "get", "blocked"}, Stdout: `{"result":{"agent":{"agent_status":"blocked","state_change_seq":5}}}`},
			{Argv: []string{"agent", "read", "blocked", "--source", "visible", "--lines", "40"}, Stdout: "Allow command?\n"},
		})
		for _, name := range []string{"working", "blocked"} {
			if err := os.WriteFile(filepath.Join(f.state, "wait", name+".not-received"), []byte("1 5 /tmp/brief.md\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		code, _, stderr := f.run(t, "working", "blocked")
		if code != 0 {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		for _, name := range []string{"working", "blocked"} {
			if _, err := os.Stat(filepath.Join(f.state, "wait", name+".not-received")); err != nil {
				t.Errorf("status mutated %s marker: %v", name, err)
			}
		}
	})
	t.Run("status: the rc rank 11 > 15 > 7 across agents", func(t *testing.T) { // JS: "status: the rc rank 11 > 15 > 7 across agents"
		rows := "quota\tp0a\tclaude\timplementer\t\t\t\t\t\t\t\t\nreceived\tp0b\tclaude\timplementer\t\t\t\t\t\t\t\nblocked\tp0c\tcodex\timplementer\t\t\t\t\t\t\t\n"
		f := newWaitStatusFixture(t, rows, []fakecli.Rule{
			{Argv: []string{"agent", "get", "quota"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`},
			{Argv: []string{"agent", "read", "quota", "--source", "visible", "--lines", "20"}, Stdout: "hit your usage limit\n"},
			{Argv: []string{"agent", "get", "received"}, Stdout: `{"result":{"agent":{"agent_status":"idle","state_change_seq":5}}}`},
			{Argv: []string{"agent", "get", "blocked"}, Stdout: `{"result":{"agent":{"agent_status":"blocked"}}}`},
			{Argv: []string{"agent", "read", "blocked", "--source", "visible", "--lines", "40"}, Stdout: "  1. Use the local cache\n  2. Fetch from remote\n\nEnter to submit answer, esc to cancel\n"},
		})
		if err := os.WriteFile(filepath.Join(f.state, "wait", "received.not-received"), []byte("1 5 /tmp/brief.md\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"received", "blocked"}, {"blocked", "received"}, {"quota", "received", "blocked"}} {
			code, out, _ := f.run(t, args...)
			want := 15
			if len(args) == 3 {
				want = 11
			}
			if code != want {
				t.Errorf("status %v code=%d out=%q want=%d", args, code, out, want)
			}
		}
	})
	t.Run("status: the not-received TSV line and the unavailable cause line gain the two age columns", func(t *testing.T) { // JS: "status: the not-received TSV line and the unavailable cause line gain the two age columns"
		rows := "received\tp0a\tclaude\timplementer\t\t\t\t\t\t\t\t\nunavailable\tp0b\tclaude\timplementer\t\t\t\t\t\t\t\n"
		f := newWaitStatusFixture(t, rows, []fakecli.Rule{
			{Argv: []string{"agent", "get", "received"}, Stdout: `{"result":{"agent":{"agent_status":"idle","state_change_seq":5}}}`},
			{Argv: []string{"agent", "get", "unavailable"}, Stderr: `{"error":{"code":"permission_denied","message":"denied"}}`, Code: 1},
		})
		if err := os.WriteFile(filepath.Join(f.state, "wait", "received.not-received"), []byte("1 5 /tmp/brief.md\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, _ := f.run(t, "received", "unavailable")
		if code != 4 || !strings.Contains(out, "received\tnot-received\t\t-\t-\n") || !strings.Contains(out, "unavailable\tunavailable\t\tpermission_denied: denied\t-\t-\n") {
			t.Fatalf("code=%d out=%q", code, out)
		}
	})
}

func TestWaitRemainingExitCases(t *testing.T) {
	row := "worker\tp0a\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n"
	t.Run("wait: quota fields and the friction entry", func(t *testing.T) { // JS: "wait: quota fields and the friction entry"
		f := newWaitStatusFixture(t, row, []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`},
			{Argv: []string{"agent", "read", "worker", "--source", "visible", "--lines", "20"}, Stdout: "hit your usage limit\ntry again in 2 hours\n"},
		})
		code, out, _ := f.runWait(t, "worker", "--timeout", "200")
		if code != 11 || !strings.Contains(out, `"status":"quota"`) || !strings.Contains(out, `"kind":"claude"`) || !strings.Contains(out, `"model":"model-x"`) || !strings.Contains(out, `"renewal":"try again in 2 hours"`) {
			t.Fatalf("code=%d out=%q", code, out)
		}
		log, err := os.ReadFile(filepath.Join(f.state, "friction.log"))
		if err != nil || !strings.Contains(string(log), "warning\twait\tquota: agent 'worker'") {
			t.Fatalf("friction=%q err=%v", log, err)
		}
	})
	t.Run("wait: provider-error exits 14 with the lane, kind, model and cause", func(t *testing.T) { // JS: "wait: provider-error exits 14 with the lane, kind, model and cause"
		f := newWaitStatusFixture(t, row, []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`},
			{Argv: []string{"agent", "read", "worker", "--source", "visible", "--lines", "20"}, Stdout: ""},
			{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped", "--lines", "40"}, Stdout: "Error: 401 Unauthorized: invalid API key\n"},
		})
		code, out, _ := f.runWait(t, "worker", "--timeout", "200")
		for _, field := range []string{`"status":"provider-error"`, `"lane":"build"`, `"kind":"claude"`, `"model":"model-x"`, `"cause":"Error: 401 Unauthorized: invalid API key"`} {
			if !strings.Contains(out, field) {
				t.Errorf("provider result lacks %s: %q", field, out)
			}
		}
		if code != 14 {
			t.Fatalf("code=%d out=%q", code, out)
		}
	})
	t.Run("wait: a stale not-received marker exits 15 with the line and the read-the-pane warning", func(t *testing.T) { // JS: "wait: a stale not-received marker exits 15 with the line and the read-the-pane warning"
		f := newWaitStatusFixture(t, row, []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle","state_change_seq":5}}}`}})
		if err := os.WriteFile(filepath.Join(f.state, "wait", "worker.not-received"), []byte("1 5 /tmp/worker-brief.md\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.state, "wait", "worker.enter-retry"), []byte("3 1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, stderr := f.runWait(t, "worker", "--timeout", "200")
		if code != 15 || !strings.Contains(out, `"status":"not-received"`) || !strings.Contains(stderr, "never reached it") || !strings.Contains(stderr, "herdr agent read worker --source visible") {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
	})
	t.Run("wait: a state change since the marker ends settled-no-report, no Enter", func(t *testing.T) { // JS: "wait: a state change since the marker ends settled-no-report, no Enter"
		f := newWaitStatusFixture(t, row, []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle","state_change_seq":6}}}`}})
		advancingWaitClock(t, 5*time.Second)
		f.env["SOHO_SETTLED_GRACE"] = "0"
		if err := os.WriteFile(filepath.Join(f.state, "wait", "worker.not-received"), []byte("1 5 /tmp/worker-brief.md\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		// Give each fake Herdr subprocess enough time on Windows; the assertion
		// is about the settled state, not the host's process startup speed.
		code, out, _ := f.runWait(t, "worker", "--timeout", "5000")
		sentEnter := false
		for _, call := range f.calls(t) {
			if strings.Join(call.Argv, " ") == "agent send-keys worker enter" {
				sentEnter = true
			}
		}
		if code != 6 || !strings.Contains(out, `"status":"settled-no-report"`) || sentEnter {
			t.Fatalf("code=%d out=%q calls=%#v", code, out, f.calls(t))
		}
	})
}

func TestWaitRemainingReportCases(t *testing.T) {
	write := func(t *testing.T, f *waitStatusFixture, agent, body string) {
		t.Helper()
		// Keep report parsing independent of the fake pane's state probe.
		f.env["SOHO_REPORT_WORKING_MAX"] = "0"
		path := filepath.Join(f.state, "reports", agent+".md")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.state, "last-report-"+agent), []byte(path+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("wait: partial is per agent in a multi-agent wait", func(t *testing.T) { // JS: "wait: partial is per agent in a multi-agent wait"
		rows := "partial\tp0a\tclaude\timplementer\t\t\t\t\t\t\t\t\nclean\tp0b\tclaude\timplementer\t\t\t\t\t\t\t\n"
		f := newWaitStatusFixture(t, rows, nil)
		write(t, f, "partial", "# Report\n\n| item | state |\n| --- | --- |\n| unfinished | [partial] |\n")
		write(t, f, "clean", "# Report\nfinished\n")
		code, out, _ := f.runWait(t, "partial", "clean", "--timeout", "2000")
		if code != 0 || strings.Count(out, `"status":"done"`) != 2 || strings.Count(out, `"partial":1`) != 1 {
			t.Fatalf("code=%d out=%q", code, out)
		}
	})
	t.Run("wait: the partial warn fires once per agent and per report", func(t *testing.T) { // JS: "wait: the partial warn fires once per agent and per report"
		row := "worker\tp0a\tclaude\timplementer\t\t\t\t\t\t\t\t\n"
		f := newWaitStatusFixture(t, row, nil)
		write(t, f, "worker", "# Report\n\n| item | state |\n| --- | --- |\n| unfinished | [partial] |\n")
		for i := 0; i < 2; i++ {
			code, out, _ := f.runWait(t, "worker", "--timeout", "2000")
			if code != 0 || !strings.Contains(out, `"partial":1`) {
				t.Fatalf("run %d: code=%d out=%q", i, code, out)
			}
		}
		log, err := os.ReadFile(filepath.Join(f.state, "friction.log"))
		if err != nil || strings.Count(string(log), "marks 1 item(s) partial") != 1 {
			t.Fatalf("warning log=%q err=%v", log, err)
		}
	})
	t.Run("wait: a review report header populates the done line", func(t *testing.T) { // JS: "wait: a review report header populates the done line"
		row := "review\tp0a\tclaude\treviewer\t\t\t\t\t\t\t\t\n"
		f := newWaitStatusFixture(t, row, nil)
		write(t, f, "review", "findings: 2 (P0 1, P1 1, P2 0, P3 0) | verdict: pass\n\nreview body\n")
		code, out, _ := f.runWait(t, "review", "--timeout", "2000")
		for _, field := range []string{`"status":"done"`, `"verdict":"pass"`, `"findings":2`, `"severity":{"P0":1,"P1":1,"P2":0,"P3":0}`} {
			if !strings.Contains(out, field) {
				t.Errorf("done line lacks %s: %q", field, out)
			}
		}
		if code != 0 {
			t.Fatalf("code=%d out=%q", code, out)
		}
	})
	t.Run("wait: findings that do not add up keep the parsed numbers and warn", func(t *testing.T) { // JS: "wait: findings that do not add up keep the parsed numbers and warn"
		row := "review\tp0a\tclaude\treviewer\t\t\t\t\t\t\t\t\n"
		f := newWaitStatusFixture(t, row, nil)
		write(t, f, "review", "findings: 3 (P0 1, P1 0, P2 0, P3 0) | verdict: fail\n\nreview body\n")
		code, out, stderr := f.runWait(t, "review", "--timeout", "2000")
		if code != 0 || !strings.Contains(out, `"findings":3`) || !strings.Contains(out, `"severity":{"P0":1,"P1":0,"P2":0,"P3":0}`) || !strings.Contains(stderr, "findings 3 but P0..P3 add up to 1") {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
	})
}

func TestWaitStatusRemainingCommandCases(t *testing.T) {
	t.Run("entry: wait/collect/release/clean usage and friction errors", func(t *testing.T) { // JS: "entry: wait/collect/release/clean usage and friction errors"
		row := "worker\tp0a\tclaude\timplementer\t\t\t\t\t\t\t\t\n"
		f := newWaitStatusFixture(t, row, nil)
		cases := []struct {
			args []string
			code int
			want string
		}{
			{[]string{"wait"}, 2, "wait: give at least one agent name"},
			{[]string{"wait", "missing"}, 3, "agent 'missing' is not in the roster"},
			{[]string{"wait", "worker", "--bogus"}, 2, "wait: unknown option --bogus"},
			{[]string{"wait", "worker", "--timeout"}, 2, "wait: --timeout expects a value"},
			{[]string{"collect"}, 1, "agent: Parameter not set"},
			{[]string{"collect", "worker", "--bogus"}, 2, "collect: unknown option --bogus"},
			{[]string{"release"}, 1, "agent: Parameter not set"},
			{[]string{"release", "worker", "--bogus"}, 2, "release: unknown option --bogus"},
			{[]string{"clean", "--bogus"}, 2, "clean: unknown option --bogus"},
			{[]string{"clean", "--older-than"}, 2, "clean: --older-than expects a value"},
		}
		for _, tc := range cases {
			code, _, stderr := f.runCommand(t, tc.args...)
			if code != tc.code || !strings.Contains(stderr, tc.want) {
				t.Errorf("Run(%v) = %d, %q; want %d containing %q", tc.args, code, stderr, tc.code, tc.want)
			}
		}
	})
	t.Run("clean: an empty or invalid --older-than deletes nothing", func(t *testing.T) { // JS: "clean: an empty or invalid --older-than deletes nothing"
		f := newWaitStatusFixture(t, "", nil)
		old := time.Unix(1_800_000_000, 0)
		files := []string{filepath.Join(f.state, "reports", "old.md"), filepath.Join(f.state, "briefs", "old.md")}
		for _, path := range files {
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(path, old, old); err != nil {
				t.Fatal(err)
			}
		}
		for _, value := range []string{"", "invalid"} {
			code, _, stderr := f.runCommand(t, "clean", "--older-than", value)
			if code != 0 || stderr != "" {
				t.Fatalf("clean older-than %q: code=%d stderr=%q", value, code, stderr)
			}
			for _, path := range files {
				if _, err := os.Stat(path); err != nil {
					t.Fatalf("invalid threshold %q deleted %s: %v", value, path, err)
				}
			}
		}
	})
	t.Run("roster: a TASK column shows the task file (no newline, dash, 40-char cut)", func(t *testing.T) { // JS: "roster: a TASK column shows the task file (no newline, dash, 40-char cut)"
		rows := "a\tp-a\tgrok\timplementer\t\t1\t/tmp/work\t\t\t\timplementer\tbuild\nb\tp-b\tgrok\timplementer\t\t1\t/tmp/work\t\t\t\timplementer\tbuild\nc\tp-c\tgrok\timplementer\t\t1\t/tmp/work\t\t\t\timplementer\tbuild\n"
		f := newWaitStatusFixture(t, rows, []fakecli.Rule{{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[{"name":"a","pane_id":"p-a","agent_status":"working"},{"name":"b","pane_id":"p-b","agent_status":"idle"},{"name":"c","pane_id":"p-c","agent_status":"working"}]}}`}})
		for agent, task := range map[string]string{"a": "Refactor the billing tree\n", "c": strings.Repeat("x", 45) + "\n"} {
			if err := os.WriteFile(filepath.Join(f.state, "task-"+agent), []byte(task), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		code, out, stderr := f.runCommand(t, "roster")
		if code != 0 || stderr != "" || !strings.HasSuffix(strings.SplitN(out, "\n", 2)[0], "CWD TASK") {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
		if !strings.Contains(out, "a ") || !strings.Contains(out, "Refactor the billing tree") || !strings.Contains(out, "b ") || !strings.Contains(out, "-\n") || !strings.Contains(out, strings.Repeat("x", 39)+"…") {
			t.Fatalf("TASK values missing or incorrectly truncated: %q", out)
		}
	})
}

func TestStatusAndWaitRetryingCases(t *testing.T) {
	row := "worker\tp0a\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n"
	retryScreen := "Retrying (5/8) in 4s…\n"
	t.Run("status: a working agent on a retry line reports the retrying cause, still working, rc 0", func(t *testing.T) {
		f := newWaitStatusFixture(t, row, []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
			{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Stdout: retryScreen},
		})
		code, out, stderr := f.run(t, "worker")
		if code != 0 || stderr != "" || out != "worker\tworking\t\tretrying: Retrying (5/8) in 4s\t-\t-\n" {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
	})
	t.Run("status: a working agent without the retry line keeps today's five-column output", func(t *testing.T) {
		f := newWaitStatusFixture(t, row, []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
			{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Stdout: "Compiling workspace\n"},
		})
		code, out, stderr := f.run(t, "worker")
		if code != 0 || stderr != "" || out != "worker\tworking\t\t-\t-\n" {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
	})
	t.Run("status: the retry line on the screen does not move the state or the rc of other agents", func(t *testing.T) {
		rows := "retrying\tp0a\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\nplain\tp0b\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n"
		f := newWaitStatusFixture(t, rows, []fakecli.Rule{
			{Argv: []string{"agent", "get", "retrying"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
			{Argv: []string{"agent", "read", "retrying", "--source", "visible"}, Stdout: retryScreen},
			{Argv: []string{"agent", "get", "plain"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
			{Argv: []string{"agent", "read", "plain", "--source", "visible"}, Stdout: "Compiling workspace\n"},
		})
		code, out, stderr := f.run(t, "retrying", "plain")
		if code != 0 || stderr != "" || out != "retrying\tworking\t\tretrying: Retrying (5/8) in 4s\t-\t-\nplain\tworking\t\t-\t-\n" {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
	})
	t.Run("wait: a timeout line of a working agent on a retry line carries the retrying field", func(t *testing.T) {
		f := newWaitStatusFixture(t, row, []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
			{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Stdout: retryScreen},
		})
		code, out, stderr := f.runWait(t, "worker", "--timeout", "10")
		if code != 9 || !strings.Contains(out, `"status":"timeout"`) || !strings.Contains(out, `"state":"working"`) || !strings.Contains(out, `"retrying":"Retrying (5/8) in 4s"`) || !strings.Contains(stderr, "timeout waiting for 'worker'") {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
	})
	t.Run("wait: a timeout line without the retry line has no retrying field and keeps its output", func(t *testing.T) {
		f := newWaitStatusFixture(t, row, []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
			{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Stdout: "Compiling workspace\n"},
		})
		code, out, stderr := f.runWait(t, "worker", "--timeout", "10")
		if code != 9 || !strings.Contains(out, `"status":"timeout"`) || strings.Contains(out, "retrying") || !strings.Contains(stderr, "timeout waiting for 'worker'") {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
	})
}
