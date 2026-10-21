package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/taskreport"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	waitpkg "github.com/djalmajr/soho/internal/wait"
)

type waitCommandResult struct {
	Agent      string `json:"agent"`
	Report     string `json:"report"`
	Status     string `json:"status"`
	TaskReport string `json:"task_report"`
}

func readWaitCommandResult(t *testing.T, output string) waitCommandResult {
	t.Helper()
	var result waitCommandResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &result); err != nil {
		t.Fatalf("wait JSON %q: %v", output, err)
	}
	return result
}

func TestStatusParityRemainingCases(t *testing.T) {
	row := "worker\tp0a\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\timplementer\tbuild\n"
	t.Run("parity: status agent get classification (test-status.sh scenarios)", func(t *testing.T) { // JS: "parity: status agent get classification (test-status.sh scenarios)"
		cases := []struct {
			status string
			code   int
			want   string
		}{{"working", 0, "working"}, {"blocked", 0, "blocked"}, {"idle", 0, "no-report-yet"}}
		for _, tc := range cases {
			f := newWaitStatusFixture(t, row, []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"` + tc.status + `"}}}`}})
			code, out, _ := f.run(t, "worker")
			if code != tc.code || !strings.Contains(out, "\t"+tc.want+"\t") {
				t.Fatalf("status %s: code=%d out=%q", tc.status, code, out)
			}
		}
	})
	t.Run("parity: roster table (rows, role history, tabs, other live agents)", func(t *testing.T) { // JS: "parity: roster table (rows, role history, tabs, other live agents)"
		rows := "worker\tp0a\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\tresearcher\tbuild\n"
		f := newWaitStatusFixture(t, rows, []fakecli.Rule{{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[{"name":"worker","pane_id":"p0a","agent_status":"working"},{"name":"outside","pane_id":"p1a","agent_status":"idle"}]}}`}})
		code, out, stderr := f.runCommand(t, "roster")
		if code != 0 || stderr != "" || !strings.Contains(out, "NAME") || !strings.Contains(out, "# other live agents in workspace ws") || strings.Contains(out, "outside") || !strings.Contains(out, "worker") {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
		code, out, stderr = f.runCommand(t, "roster", "--scope", "server")
		if code != 0 || stderr != "" || !strings.Contains(out, "outside") || !strings.Contains(out, "worker") {
			t.Fatalf("explicit server scope lost its inventory: code=%d out=%q stderr=%q", code, out, stderr)
		}
	})
	t.Run("parity: friction empty and seeded", func(t *testing.T) { // JS: "parity: friction empty and seeded"
		f := newWaitStatusFixture(t, row, nil)
		code, out, stderr := f.runCommand(t, "friction")
		if code != 0 || stderr != "" || !strings.Contains(out, "no friction recorded") {
			t.Fatalf("empty friction: code=%d out=%q stderr=%q", code, out, stderr)
		}
		if err := os.WriteFile(filepath.Join(f.state, "friction.log"), []byte("now\twarning\twait\tseeded entry\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, stderr = f.runCommand(t, "friction")
		if code != 0 || stderr != "" || !strings.Contains(out, "seeded entry") {
			t.Fatalf("seeded friction: code=%d out=%q stderr=%q", code, out, stderr)
		}
	})
	t.Run("parity: status env error paths (no names = the roster, outside Herdr, herdr missing)", func(t *testing.T) {
		f := newWaitStatusFixture(t, row, nil)
		code, out, stderr := f.run(t)
		namedCode, namedOut, namedStderr := f.run(t, "worker")
		if code != namedCode || out != namedOut || stderr != namedStderr {
			t.Fatalf("no names differs from the roster names: code=%d out=%q stderr=%q; want code=%d out=%q stderr=%q", code, out, stderr, namedCode, namedOut, namedStderr)
		}
		f.env["HERDR_ENV"] = ""
		code, _, stderr = f.run(t, "worker")
		if code != 2 || !strings.Contains(strings.ToLower(stderr), "herdr") {
			t.Fatalf("outside Herdr: code=%d stderr=%q", code, stderr)
		}
		f.env["HERDR_ENV"] = "1"
		f.env["PATH"] = t.TempDir()
		code, _, stderr = f.run(t, "worker")
		if code != 2 || !strings.Contains(stderr, "herdr CLI not found in PATH") {
			t.Fatalf("missing Herdr: code=%d stderr=%q", code, stderr)
		}
	})
	t.Run("parity: status denied also logs a friction entry (both implementations)", func(t *testing.T) { // JS: "parity: status denied also logs a friction entry (both implementations)"
		f := newWaitStatusFixture(t, row, []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Code: 1, Stderr: `{"error":{"code":"permission_denied","message":"denied"}}`}})
		code, out, _ := f.run(t, "worker")
		log, err := os.ReadFile(filepath.Join(f.state, "friction.log"))
		if err != nil || code != 4 || !strings.Contains(out, "\tunavailable\t") || !strings.Contains(string(log), "status") || !strings.Contains(string(log), "denied") {
			t.Fatalf("code=%d out=%q friction=%q err=%v", code, out, log, err)
		}
	})
	t.Run("node semantics: status columns, cause, no stray herdr queries, quota JSON", func(t *testing.T) { // JS: "node semantics: status columns, cause, no stray herdr queries, quota JSON"
		f := newWaitStatusFixture(t, row, []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`},
			{Argv: []string{"agent", "read", "worker", "--source", "visible", "--lines", "20"}, Stdout: "Individual quota reached\n"},
		})
		code, out, stderr := f.run(t, "worker")
		calls := f.calls(t)
		if code != 11 || stderr == "" || !strings.Contains(out, `"status":"quota"`) || !strings.Contains(out, `"lane":"build"`) || !strings.Contains(out, `"task_s":null,"activity_s":null`) || len(calls) != 3 || strings.Join(calls[0].Argv, " ") != "agent get worker" || strings.Join(calls[2].Argv, " ") != "agent read worker --source visible --lines 20" {
			t.Fatalf("code=%d out=%q stderr=%q calls=%#v", code, out, stderr, calls)
		}
	})
}

func TestWaitParityRemainingCases(t *testing.T) {
	t.Run("parity wait: a worker working until the timeout (test-quota.sh)", func(t *testing.T) { // JS: "parity wait: a worker working until the timeout (test-quota.sh)"
		row := "worker\tp0a\tclaude\timplementer\tanthropic\t1\t\t\tmodel-x\t\timplementer\tbuild\n"
		f := newWaitStatusFixture(t, row, []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`}})
		code, out, _ := f.runWait(t, "worker", "--timeout", "100")
		if code != 9 || !strings.Contains(out, `"status":"timeout"`) || !strings.Contains(out, `"state":"working"`) {
			t.Fatalf("code=%d out=%q", code, out)
		}
	})
	t.Run("parity wait: quota on one lane outranks blocked on the other, both orders (test-quota.sh)", func(t *testing.T) { // JS: "parity wait: quota on one lane outranks blocked on the other, both orders (test-quota.sh)"
		rows := "quota\tp0a\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\timplementer\tbuild\nblocked\tp0b\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\timplementer\tbuild\n"
		f := newWaitStatusFixture(t, rows, []fakecli.Rule{
			{Argv: []string{"agent", "get", "quota"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`},
			{Argv: []string{"agent", "read", "quota", "--source", "visible", "--lines", "20"}, Stdout: "hit your usage limit\ntry again in 2 hours\n"},
			{Argv: []string{"agent", "get", "blocked"}, Stdout: `{"result":{"agent":{"agent_status":"blocked"}}}`},
			{Argv: []string{"agent", "read", "blocked", "--source", "visible", "--lines", "40"}, Stdout: "Allow command?\nPress enter to confirm\n"},
			{Argv: []string{"agent", "read", "blocked", "--source", "visible", "--lines", "20"}, Stdout: "Allow command?\nPress enter to confirm\n"},
		})
		if err := os.WriteFile(filepath.Join(f.state, "wait", "blocked.blocked"), []byte(""), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, names := range [][]string{{"quota", "blocked"}, {"blocked", "quota"}} {
			code, out, _ := f.runWait(t, append(names, "--timeout", "60000")...)
			if code != 11 || !strings.Contains(out, `"status":"quota"`) || !strings.Contains(out, `"status":"blocked"`) {
				t.Fatalf("names=%v code=%d out=%q", names, code, out)
			}
		}
	})
	t.Run("parity wait: the report marks the pane title ✓ (test-quota.sh)", func(t *testing.T) { // JS: "parity wait: the report marks the pane title ✓ (test-quota.sh)"
		row := "worker\tp0a\tclaude\timplementer\tanthropic\t1\t\t\tmodel-x\t\timplementer\tbuild\n"
		f := newWaitStatusFixture(t, row, nil)
		report := filepath.Join(f.state, "reports", "worker.md")
		if err := os.MkdirAll(filepath.Dir(report), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(report, []byte("done\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.state, "last-report-worker"), []byte(report+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.state, "task-worker"), []byte("implementer: porte da config\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		f.env["SOHO_WAIT_POLL_MS"] = "1"
		code, out, _ := f.runWait(t, "worker", "--timeout", "60000")
		if code != 0 || !strings.Contains(out, `"status":"done"`) {
			t.Fatalf("code=%d out=%q", code, out)
		}
		calls := f.calls(t)
		if len(callsTo(calls, "pane", "report-metadata", "p0a", "--source", "soho", "--title", "implementer: porte da config ✓")) != 1 {
			t.Fatalf("completion did not set the report pane title: %#v", calls)
		}
	})
	t.Run("parity wait: denied (unavailable) and gone (test-status.sh)", func(t *testing.T) { // JS: "parity wait: denied (unavailable) and gone (test-status.sh)"
		for _, tc := range []struct {
			state string
			code  int
		}{{"denied", 4}, {"gone", 6}} {
			row := "worker\tp0a\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\timplementer\tbuild\n"
			rules := []fakecli.Rule{}
			if tc.state == "denied" {
				rules = append(rules, fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Code: 1, Stderr: `{"error":{"code":"permission_denied"}}`})
			} else {
				rules = append(rules, fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Code: 137, Stderr: `{"error":{"code":"agent_not_found"}}`})
			}
			f := newWaitStatusFixture(t, row, rules)
			code, out, _ := f.runWait(t, "worker", "--timeout", "60000")
			want := "unavailable"
			if tc.state == "gone" {
				want = "gone"
			}
			if code != tc.code || !strings.Contains(out, `"status":"`+want+`"`) {
				t.Fatalf("state=%s code=%d out=%q", tc.state, code, out)
			}
		}
	})
	t.Run("wait: the rank order 11 > 14 > 7 with quota, provider-error and blocked", func(t *testing.T) { // JS: "wait: the rank order 11 > 14 > 7 with quota, provider-error and blocked"
		rows := "quota\tp0a\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\timplementer\tbuild\n" +
			"provider\tp0b\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\timplementer\tbuild\n" +
			"blocked\tp0c\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\timplementer\tbuild\n"
		rules := []fakecli.Rule{
			{Argv: []string{"agent", "get", "quota"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`},
			{Argv: []string{"agent", "read", "quota", "--source", "visible", "--lines", "20"}, Stdout: "hit your usage limit\n"},
			{Argv: []string{"agent", "get", "provider"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`},
			{Argv: []string{"agent", "read", "provider", "--source", "visible", "--lines", "20"}, Stdout: "Error: Connection error.\n"},
			{Argv: []string{"agent", "read", "provider", "--source", "recent-unwrapped", "--lines", "40"}, Stdout: "Error: Connection error.\n"},
			{Argv: []string{"agent", "get", "blocked"}, Stdout: `{"result":{"agent":{"agent_status":"blocked"}}}`},
			{Argv: []string{"agent", "read", "blocked", "--source", "visible", "--lines", "40"}, Stdout: "Allow command?\nPress enter to confirm\n"},
			{Argv: []string{"agent", "read", "blocked", "--source", "visible", "--lines", "20"}, Stdout: "Allow command?\nPress enter to confirm\n"},
		}
		for _, names := range [][]string{{"quota", "provider", "blocked"}, {"blocked", "provider", "quota"}, {"provider", "blocked", "quota"}} {
			f := newWaitStatusFixture(t, rows, rules)
			advancingWaitClock(t, 5*time.Second)
			if err := os.WriteFile(filepath.Join(f.state, "wait", "blocked.blocked"), []byte(""), 0o600); err != nil {
				t.Fatal(err)
			}
			code, out, _ := f.runWait(t, append(names, "--timeout", "60000")...)
			states := map[string]string{}
			for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
				var record struct {
					Agent  string `json:"agent"`
					Status string `json:"status"`
				}
				if err := json.Unmarshal([]byte(line), &record); err != nil {
					t.Fatalf("line=%q: %v", line, err)
				}
				states[record.Agent] = record.Status
			}
			if code != 11 || !strings.Contains(out, `"quota"`) || states["quota"] != "quota" || states["provider"] != "provider-error" || states["blocked"] != "blocked" {
				t.Fatalf("names=%v code=%d states=%v out=%q", names, code, states, out)
			}
		}
		pairRows := "provider\tp0b\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\timplementer\tbuild\n" +
			"blocked\tp0c\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\timplementer\tbuild\n"
		for _, names := range [][]string{{"provider", "blocked"}, {"blocked", "provider"}} {
			f := newWaitStatusFixture(t, pairRows, rules)
			advancingWaitClock(t, 5*time.Second)
			if err := os.WriteFile(filepath.Join(f.state, "wait", "blocked.blocked"), []byte(""), 0o600); err != nil {
				t.Fatal(err)
			}
			code, out, _ := f.runWait(t, append(names, "--timeout", "60000")...)
			if code != 14 || !strings.Contains(out, `"status":"provider-error"`) || !strings.Contains(out, `"status":"blocked"`) {
				t.Fatalf("pair=%v code=%d out=%q", names, code, out)
			}
		}
	})
}

func TestWaitCommandMirrorParityCases(t *testing.T) {
	t.Run("parity release: the pane title is cleared without --close (test-quota.sh)", func(t *testing.T) { // JS: "parity release: the pane title is cleared without --close (test-quota.sh)"
		f := newReleaseFixture(t, "idle", "")
		addReleaseReport(t, f.state, "worker", false)
		r := f.run(t, "worker")
		if r.code != 0 || len(callsTo(r.calls, "pane", "close", "p-worker")) != 0 || len(callsTo(r.calls, "pane", "report-metadata", "p-worker", "--source", "soho", "--clear-title")) != 1 {
			t.Fatalf("release code=%d stdout=%q stderr=%q calls=%#v", r.code, r.stdout, r.stderr, r.calls)
		}
	})
	t.Run("parity release: the refusal paths and the finished close (test-status.sh)", func(t *testing.T) { // JS: "parity release: the refusal paths and the finished close (test-status.sh)"
		pending := newReleaseFixture(t, "working", "")
		if err := os.WriteFile(filepath.Join(pending.state, "agents.tsv"), []byte("# header\n"+releaseRow("worker", "p-worker", "1", "implementer", true, "/tmp/work")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		addReleaseReport(t, pending.state, "worker", true)
		refused := pending.run(t, "worker")
		if refused.code != 3 || len(callsTo(refused.calls, "pane", "close", "p-worker")) != 0 {
			t.Fatalf("working release code=%d calls=%#v", refused.code, refused.calls)
		}
		finished := newReleaseFixture(t, "idle", "")
		addReleaseReport(t, finished.state, "worker", false)
		closed := finished.run(t, "worker", "--close")
		if closed.code != 0 || len(callsTo(closed.calls, "pane", "close", "p-worker")) != 1 {
			t.Fatalf("finished release code=%d calls=%#v", closed.code, closed.calls)
		}
	})
	t.Run("parity clean: a dead worker is dropped, old files go, pointed reports stay", func(t *testing.T) { // JS: "parity clean: a dead worker is dropped, old files go, pointed reports stay"
		row := "worker\tp0a\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\timplementer\tbuild\n"
		f := newWaitStatusFixture(t, row, []fakecli.Rule{{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`}})
		report := filepath.Join(f.state, "reports", "worker.md")
		if err := os.MkdirAll(filepath.Dir(report), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(report, []byte("kept report\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.state, "last-report-worker"), []byte(report+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, _, stderr := f.runCommand(t, "clean")
		if code != 0 || stderr != "" || core.RosterLine(f.state, "worker") != "" {
			t.Fatalf("clean code=%d stderr=%q roster=%q", code, stderr, core.RosterLine(f.state, "worker"))
		}
		if data, err := os.ReadFile(report); err != nil || string(data) != "kept report\n" {
			t.Fatalf("pointed report=%q err=%v", data, err)
		}
	})
}

func TestWaitMirrorCleanListFailureCase(t *testing.T) {
	t.Run("clean: a herdr agent list without an agent list exits 4 and keeps the roster", func(t *testing.T) { // JS: "clean: a herdr agent list without an agent list exits 4 and keeps the roster"
		row := "worker\tp0a\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\timplementer\tbuild\n"
		for _, answer := range []string{"not json", `{"result":{}}`, `{"result":{"agents":"x"}}`} {
			f := newWaitStatusFixture(t, row, []fakecli.Rule{{Argv: []string{"agent", "list"}, Stdout: answer}})
			before := core.RosterLine(f.state, "worker")
			code, _, stderr := f.runCommand(t, "clean")
			if code != 4 || !strings.Contains(stderr, "herdr agent list returned no agent list") || core.RosterLine(f.state, "worker") != before {
				t.Fatalf("clean answer=%q code=%d stderr=%q roster=%q", answer, code, stderr, core.RosterLine(f.state, "worker"))
			}
		}
	})
}

func TestWaitTaskReportFailureRestoreCase(t *testing.T) {
	t.Run("failed transport restores last-report, pointer and stable copy byte-for-byte", func(t *testing.T) { // JS: "failed transport restores last-report, pointer and stable copy byte-for-byte"
		// Mutation captured: publishing the new last-report pointer before prompt acceptance leaks a failed dispatch into the next wait.
		f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0", fakecli.Rule{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Code: 1, Stderr: "transport failed"})
		f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
		reports := filepath.Join(f.state, "ws", "reports")
		if err := os.MkdirAll(reports, 0o700); err != nil {
			t.Fatal(err)
		}
		oldReport := filepath.Join(reports, "previous.md")
		stable := filepath.Join(reports, "worker.current.md")
		for path, body := range map[string]string{oldReport: "previous report\n", stable: "previous report\n"} {
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		last := filepath.Join(f.state, "ws", "last-report-worker")
		lastBytes := []byte(oldReport + "\n")
		if err := os.WriteFile(last, lastBytes, 0o600); err != nil {
			t.Fatal(err)
		}
		prior := jsonjs.O("version", 1, "task_report", stable, "current", oldReport, "history", []string{})
		if err := taskreport.WriteTaskReportPointer(filepath.Join(f.state, "ws"), "worker", prior); err != nil {
			t.Fatal(err)
		}
		pointerBytes, err := os.ReadFile(taskreport.TaskReportPointerPath(filepath.Join(f.state, "ws"), "worker"))
		if err != nil {
			t.Fatal(err)
		}
		code, _, _ := f.run(t, "worker", f.brief, "--no-wait")
		if code == 0 {
			t.Fatal("failed transport was reported as accepted")
		}
		gotLast, err := os.ReadFile(last)
		if err != nil || string(gotLast) != string(lastBytes) {
			t.Fatalf("last-report=%q err=%v", gotLast, err)
		}
		gotPointer, err := os.ReadFile(taskreport.TaskReportPointerPath(filepath.Join(f.state, "ws"), "worker"))
		if err != nil || string(gotPointer) != string(pointerBytes) {
			t.Fatalf("pointer=%q err=%v", gotPointer, err)
		}
		if got, err := os.ReadFile(stable); err != nil || string(got) != "previous report\n" {
			t.Fatalf("stable copy=%q err=%v", got, err)
		}
	})
}

func TestWaitQuestionAndApprovalRemainingCases(t *testing.T) {
	const question = "  1. Use the local cache\n  2. Fetch from remote\n\nEnter to submit answer, esc to cancel\n"
	const approval = "Allow command? git push\n\n❯ 1. Yes, proceed\n  2. No\n\nPress enter to confirm or esc to cancel\n"
	t.Run("wait: a codex question screen reports question, no key, even with auto_approve=on", func(t *testing.T) { // JS: "wait: a codex question screen reports question, no key, even with auto_approve=on"
		row := "worker\tp0a\tcodex\timplementer\topenai\t\t\t\tmodel-x\t\timplementer\tbuild\n"
		f := newWaitStatusFixture(t, row, []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"blocked"}}}`},
			{Argv: []string{"agent", "read", "worker", "--source", "visible", "--lines", "40"}, Stdout: question},
		})
		f.env["SOHO_AUTO_APPROVE"] = "on"
		code, out, _ := f.runWait(t, "worker", "--timeout", "60000")
		if code != 7 || !strings.Contains(out, `"status":"question"`) || !strings.Contains(out, `"question":"  1. Use the local cache`) {
			t.Fatalf("code=%d out=%q", code, out)
		}
		for _, call := range f.calls(t) {
			if strings.HasPrefix(strings.Join(call.Argv, " "), "agent send-keys worker") {
				t.Fatalf("question was auto-answered: %#v", call)
			}
		}
	})
	t.Run("wait: a codex approval screen keeps the auto-approve key (behavior untouched)", func(t *testing.T) { // JS: "wait: a codex approval screen keeps the auto-approve key (behavior untouched)"
		row := "worker\tp0a\tcodex\timplementer\topenai\t\t\t\tmodel-x\t\timplementer\tbuild\n"
		f := newWaitStatusFixture(t, row, []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"blocked"}}}`},
			{Argv: []string{"agent", "read", "worker", "--source", "visible", "--lines", "40"}, Stdout: approval},
		})
		f.env["SOHO_AUTO_APPROVE"] = "on"
		code, out, _ := f.runWait(t, "worker", "--timeout", "60000")
		count := 0
		for _, call := range f.calls(t) {
			if strings.Join(call.Argv, " ") == "agent send-keys worker y" {
				count++
			}
		}
		if code != 7 || !strings.Contains(out, `"status":"blocked"`) || count < 1 {
			t.Fatalf("code=%d out=%q codex y keys=%d", code, out, count)
		}
	})
	t.Run("wait: the blocked line carries the dialog and the loop warn", func(t *testing.T) { // JS: "wait: the blocked line carries the dialog and the loop warn"
		row := "worker\tp0a\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\timplementer\tbuild\n"
		lines := make([]string, 25)
		for i := range lines {
			lines[i] = "line " + strconv.Itoa(i+1)
		}
		dialog := strings.Join(lines[5:], "\n")
		screen := strings.Join(lines, "\n") + "\n"
		f := newWaitStatusFixture(t, row, []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"blocked"}}}`},
			{Argv: []string{"agent", "read", "worker", "--source", "visible", "--lines", "40"}, Stdout: screen},
			{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Stdout: screen},
		})
		f.env["SOHO_AUTO_APPROVE"] = "on"
		hash := strconv.FormatUint(uint64(waitpkg.CksumField(waitpkg.NormalizeApproveScreen(screen))), 10)
		if err := os.WriteFile(filepath.Join(f.state, "wait", "worker.blocked"), []byte(""), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.state, "wait", "worker.approve-screen"), []byte("2\t"+hash+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, stderr := f.runWait(t, "worker", "--timeout", "60000")
		warn := "auto_approve: the same dialog came back 3 times for 'worker'; leaving it blocked"
		log, err := os.ReadFile(filepath.Join(f.state, "friction.log"))
		var result struct {
			Status string `json:"status"`
			Dialog string `json:"dialog"`
		}
		decodeErr := json.Unmarshal([]byte(out), &result)
		if err != nil || decodeErr != nil || code != 7 || result.Status != "blocked" || result.Dialog != dialog || !strings.Contains(stderr, warn) || !strings.Contains(string(log), "warning\twait\t"+warn) {
			t.Fatalf("code=%d out=%q stderr=%q calls=%#v friction=%q err=%v decodeErr=%v", code, out, stderr, f.calls(t), log, err, decodeErr)
		}
	})
}

func TestWaitProviderRemainingCases(t *testing.T) {
	row := "worker\tp0a\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\timplementer\tbuild\n"
	t.Run("wait: auth exits 14 with the lane, kind, model and cause", func(t *testing.T) { // JS: "wait: auth exits 14 with the lane, kind, model and cause"
		f := newWaitStatusFixture(t, row, []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`},
			{Argv: []string{"agent", "read", "worker", "--source", "visible", "--lines", "20"}},
			{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped", "--lines", "40"}, Stdout: "Error: 401 Unauthorized: invalid API key\n"},
		})
		code, out, _ := f.runWait(t, "worker", "--timeout", "60000")
		for _, field := range []string{`"status":"provider-error"`, `"lane":"build"`, `"kind":"claude"`, `"model":"model-x"`, `"cause":"Error: 401 Unauthorized: invalid API key"`} {
			if !strings.Contains(out, field) {
				t.Errorf("missing %s in %q", field, out)
			}
		}
		if code != 14 {
			t.Fatalf("code=%d out=%q", code, out)
		}
	})
	t.Run("wait: quota wins over an auth failure on the same screen", func(t *testing.T) { // JS: "wait: quota wins over an auth failure on the same screen"
		f := newWaitStatusFixture(t, row, []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`},
			{Argv: []string{"agent", "read", "worker", "--source", "visible", "--lines", "20"}, Stdout: "hit your usage limit\nError: 401 Unauthorized\n"},
			{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped", "--lines", "40"}, Stdout: "Error: 401 Unauthorized\n"},
		})
		code, out, _ := f.runWait(t, "worker", "--timeout", "60000")
		if code != 11 || !strings.Contains(out, `"status":"quota"`) || strings.Contains(out, `"status":"provider-error"`) {
			t.Fatalf("code=%d out=%q", code, out)
		}
	})
}

func TestWaitRepeatedStillScreenCheckpointCase(t *testing.T) {
	t.Run("wait: two short timeouts on a read-only still screen are not active (friction warn each)", func(t *testing.T) { // JS: "wait: two short timeouts on a read-only still screen are not active (friction warn each)"
		// Mutation captured: treating the first static screen read as activity suppresses both timeout warnings.
		row := "worker\tp0a\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\timplementer\tbuild\n"
		f := newWaitStatusFixture(t, row, []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
			{Argv: []string{"agent", "read", "worker", "--source", "visible", "--lines", "20"}, Stdout: "typing on the same line\n"},
			{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Stdout: "typing on the same line\n"},
		})
		for attempt := 1; attempt <= 2; attempt++ {
			code, out, stderr := f.runWait(t, "worker", "--timeout", "100")
			if code != 9 || !strings.Contains(out, `"checkpoint":false`) || !strings.Contains(out, `"activity_age_s":null`) || !strings.Contains(stderr, "timeout waiting for 'worker'") {
				t.Fatalf("attempt %d: code=%d out=%q stderr=%q", attempt, code, out, stderr)
			}
		}
		log, err := os.ReadFile(filepath.Join(f.state, "friction.log"))
		if err != nil || strings.Count(string(log), "timeout waiting for 'worker'") != 2 {
			t.Fatalf("friction warnings=%q err=%v", log, err)
		}
	})
}

func TestCollectParityRemainingCase(t *testing.T) {
	t.Run("parity collect: query failure, the gone fallback, the ready report (test-status.sh)", func(t *testing.T) { // JS: "parity collect: query failure, the gone fallback, the ready report (test-status.sh)"
		row := "worker\tp0a\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\timplementer\tbuild\n"
		denied := newWaitStatusFixture(t, row, []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Code: 1, Stderr: `{"error":{"code":"permission_denied","message":"PermissionDenied"}}`}})
		code, out, stderr := denied.runCommand(t, "collect", "worker")
		if code != 4 || strings.Contains(out, "terminal-fallback") || !strings.Contains(stderr, "PermissionDenied") {
			t.Fatalf("denied collect: code=%d out=%q stderr=%q", code, out, stderr)
		}
		for _, call := range denied.calls(t) {
			if len(call.Argv) >= 3 && call.Argv[0] == "agent" && call.Argv[1] == "read" {
				t.Fatalf("read attempted after unavailable query: %#v", call)
			}
		}

		gone := newWaitStatusFixture(t, row, []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Code: 137, Stderr: `{"error":{"code":"agent_not_found"}}`},
			{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped", "--lines", "120"}, Stdout: "terminal-fallback\n"},
		})
		code, out, _ = gone.runCommand(t, "collect", "worker")
		if code != 6 || !strings.Contains(out, "terminal-fallback") {
			t.Fatalf("gone collect: code=%d out=%q", code, out)
		}

		ready := newWaitStatusFixture(t, row, nil)
		report := filepath.Join(ready.state, "reports", "worker.md")
		if err := os.MkdirAll(filepath.Dir(report), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(report, []byte("report body\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(ready.state, "last-report-worker"), []byte(report+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, _ = ready.runCommand(t, "collect", "worker")
		if code != 0 || !strings.Contains(out, "<!-- report: "+report+" -->") || !strings.Contains(out, "report body") {
			t.Fatalf("ready collect: code=%d out=%q", code, out)
		}
	})
}

func TestWaitDefaultRoleTimeoutCase(t *testing.T) {
	t.Run("wait: without --timeout the wait uses the role timeout (effort-scaled), the largest across agents", func(t *testing.T) { // JS: "wait: without --timeout the wait uses the role timeout (effort-scaled), the largest across agents"
		// Mutation captured: falling back to the global dispatch timeout or missing the xhigh factor changes the derived timeout and doubled retry suggestion.
		rolesDir := t.TempDir()
		for name, content := range map[string]string{
			"t1.md": "---\nname: t1\ntimeout: 500\neffort: high\n---\nrole\n",
			"t2.md": "---\nname: t2\ntimeout: 800\neffort: xhigh\n---\nrole\n",
		} {
			if err := os.WriteFile(filepath.Join(rolesDir, name), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		rows := "w1\tp0a\tclaude\tt1\tanthropic\t\t\t\tmodel-x\t\tt1\tbuild\nw2\tp0b\tclaude\tt2\tanthropic\t\t\t\tmodel-x\t\tt2\tbuild\n"
		f := newWaitStatusFixture(t, rows, []fakecli.Rule{
			{Argv: []string{"agent", "get", "w1"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
			{Argv: []string{"agent", "get", "w2"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
			{Argv: []string{"agent", "read", "w1", "--source", "visible", "--lines", "20"}, Stdout: "busy\n"},
			{Argv: []string{"agent", "read", "w2", "--source", "visible", "--lines", "20"}, Stdout: "busy\n"},
		})
		f.env["SOHO_ROLES"] = rolesDir
		f.env["SOHO_ROLE_T1_EFFORT"] = "high"
		f.env["SOHO_ROLE_T2_EFFORT"] = "xhigh"
		code, out, stderr := f.runWait(t, "w1", "w2")
		if code != 9 || strings.Count(out, `"status":"timeout"`) != 2 || !strings.Contains(stderr, "soho wait w1 --timeout 2400") || !strings.Contains(stderr, "soho wait w2 --timeout 2400") {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
	})
}

func TestWaitCapacityCommandRemainingCases(t *testing.T) {
	row := "worker\tp0a\tgrok\timplementer\txai\t\t\t\tgrok-4.7\t\timplementer\tbuild\n"
	capacityScreen := `API Error: 529 {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}` + "\n"
	makeCapacity := func(t *testing.T) *waitStatusFixture {
		t.Helper()
		f := newWaitStatusFixture(t, row, []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle","state_change_seq":5}}}`, Delay: 30},
			{Argv: []string{"agent", "read", "worker", "--source", "visible", "--lines", "20"}, Stdout: capacityScreen},
			{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped", "--lines", "40"}, Stdout: capacityScreen},
			{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"submitted":true}}`},
		})
		f.env["SOHO_PROVIDER_RETRY_DELAY"] = "0"
		return f
	}
	t.Run("wait: capacity sends the continue prompt and the report settles done", func(t *testing.T) { // JS: "wait: capacity sends the continue prompt and the report settles done"
		f := makeCapacity(t)
		report := filepath.Join(f.state, "reports", "worker-report.md")
		if err := os.WriteFile(filepath.Join(f.state, "last-report-worker"), []byte(report+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		stop, exited := make(chan struct{}), make(chan struct{})
		go func() {
			defer close(exited)
			ticker := time.NewTicker(time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-stop:
					return
				case <-ticker.C:
					calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
					if err == nil {
						for _, call := range calls {
							if len(call.Argv) >= 3 && call.Argv[0] == "agent" && call.Argv[1] == "prompt" && call.Argv[2] == "worker" {
								_ = os.WriteFile(report, []byte("finished\n"), 0o600)
								return
							}
						}
					}
				}
			}
		}()
		code, out, stderr := f.runWait(t, "worker", "--timeout", "60000")
		close(stop)
		<-exited
		want := "The model provider was at capacity and your last request failed. Continue the task from where you stopped; do not redo finished steps. When finished, write your report to " + report + " and reply with only that path."
		calls := f.calls(t)
		log, err := os.ReadFile(filepath.Join(f.state, "friction.log"))
		result := readWaitCommandResult(t, out)
		if code != 0 || result.Agent != "worker" || result.Status != "done" || result.Report != report || err != nil || !strings.Contains(string(log), "provider capacity: sent continue #1 of 3 to 'worker': API Error: 529") || countDispatchCalls(calls, "prompt") != 1 {
			t.Fatalf("code=%d out=%q stderr=%q calls=%#v friction=%q err=%v", code, out, stderr, calls, log, err)
		}
		found := false
		for _, call := range calls {
			if len(call.Argv) == 4 && call.Argv[0] == "agent" && call.Argv[1] == "prompt" && call.Argv[2] == "worker" && call.Argv[3] == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("exact continue prompt absent: %#v", calls)
		}
	})
	t.Run("wait: capacity exhausted at provider_retries=1 exits 14 with the retries", func(t *testing.T) { // JS: "wait: capacity exhausted at provider_retries=1 exits 14 with the retries"
		f := makeCapacity(t)
		f.env["SOHO_PROVIDER_RETRIES"] = "1"
		report := filepath.Join(f.state, "reports", "worker-report.md")
		if err := os.WriteFile(filepath.Join(f.state, "last-report-worker"), []byte(report+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, stderr := f.runWait(t, "worker", "--timeout", "60000")
		calls := f.calls(t)
		log, err := os.ReadFile(filepath.Join(f.state, "friction.log"))
		line := strings.TrimSpace(out)
		if code != 14 || !strings.Contains(line, `"status":"capacity"`) || !strings.Contains(line, `"retries":1`) || !strings.Contains(line, `"lane":"build"`) || !strings.Contains(line, `"model":"grok-4.7"`) || countDispatchCalls(calls, "prompt") != 1 || err != nil || !strings.Contains(string(log), "sent continue #1 of 1 to 'worker'") || !strings.Contains(string(log), "agent 'worker' lane=build kind=grok model=grok-4.7") {
			t.Fatalf("code=%d out=%q stderr=%q calls=%#v friction=%q err=%v", code, out, stderr, calls, log, err)
		}
	})
}

func TestWaitFullRankCommandCase(t *testing.T) {
	t.Run("wait: the rank order 4 > 11 > 14 > 15 > 7 with a not-received agent", func(t *testing.T) { // JS: "wait: the rank order 4 > 11 > 14 > 15 > 7 with a not-received agent"
		rows := "unavailable\tp0a\tgrok\timplementer\txai\t\t\t\tgrok-4.7\t\timplementer\tbuild\n" +
			"quota\tp0b\tgrok\timplementer\txai\t\t\t\tgrok-4.7\t\timplementer\tbuild\n" +
			"provider\tp0c\tgrok\timplementer\txai\t\t\t\tgrok-4.7\t\timplementer\tbuild\n" +
			"lost\tp0d\tgrok\timplementer\txai\t\t\t\tgrok-4.7\t\timplementer\tbuild\n" +
			"blocked\tp0e\tgrok\timplementer\txai\t\t\t\tgrok-4.7\t\timplementer\tbuild\n"
		rules := []fakecli.Rule{
			{Argv: []string{"agent", "get", "unavailable"}, Code: 1, Stderr: `{"error":{"code":"permission_denied","message":"denied"}}`},
			{Argv: []string{"agent", "get", "quota"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`},
			{Argv: []string{"agent", "read", "quota", "--source", "visible", "--lines", "20"}, Stdout: "hit your usage limit\n"},
			{Argv: []string{"agent", "get", "provider"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`},
			{Argv: []string{"agent", "read", "provider", "--source", "visible", "--lines", "20"}, Stdout: "Error: Connection error.\n"},
			{Argv: []string{"agent", "read", "provider", "--source", "recent-unwrapped", "--lines", "40"}, Stdout: "Error: Connection error.\n"},
			{Argv: []string{"agent", "get", "lost"}, Stdout: `{"result":{"agent":{"agent_status":"idle","state_change_seq":5}}}`},
			{Argv: []string{"agent", "get", "blocked"}, Stdout: `{"result":{"agent":{"agent_status":"blocked"}}}`},
			{Argv: []string{"agent", "read", "blocked", "--source", "visible", "--lines", "40"}, Stdout: "Allow command?\nPress enter to confirm\n"},
		}
		cases := []struct {
			names []string
			code  int
			want  map[string]string
		}{
			{[]string{"lost", "blocked"}, 15, map[string]string{"lost": "not-received", "blocked": "blocked"}},
			{[]string{"blocked", "lost"}, 15, map[string]string{"lost": "not-received", "blocked": "blocked"}},
			{[]string{"provider", "lost"}, 14, map[string]string{"provider": "provider-error", "lost": "not-received"}},
			{[]string{"lost", "provider"}, 14, map[string]string{"provider": "provider-error", "lost": "not-received"}},
			{[]string{"quota", "lost"}, 11, map[string]string{"quota": "quota", "lost": "not-received"}},
			{[]string{"lost", "quota"}, 11, map[string]string{"quota": "quota", "lost": "not-received"}},
			{[]string{"unavailable", "lost"}, 4, map[string]string{"unavailable": "unavailable", "lost": "not-received"}},
			{[]string{"lost", "unavailable"}, 4, map[string]string{"unavailable": "unavailable", "lost": "not-received"}},
		}
		for _, tc := range cases {
			f := newWaitStatusFixture(t, rows, rules)
			for name, body := range map[string]string{
				"lost.not-received": fmt.Sprintf("%d 5 /tmp/lost.md\n", time.Now().Unix()-120),
				"lost.enter-retry":  "3 1\n",
				"blocked.blocked":   "",
			} {
				if err := os.WriteFile(filepath.Join(f.state, "wait", name), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			code, out, _ := f.runWait(t, append(tc.names, "--timeout", "60000")...)
			states := map[string]string{}
			for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
				var record struct {
					Agent  string `json:"agent"`
					Status string `json:"status"`
				}
				if err := json.Unmarshal([]byte(line), &record); err != nil {
					t.Fatalf("line=%q: %v", line, err)
				}
				states[record.Agent] = record.Status
			}
			if code != tc.code || len(states) != len(tc.want) {
				t.Fatalf("names=%v code=%d states=%v out=%q", tc.names, code, states, out)
			}
			for name, want := range tc.want {
				if states[name] != want {
					t.Fatalf("names=%v %s=%q want %q; code=%d out=%q", tc.names, name, states[name], want, code, out)
				}
			}
		}
	})
}

func TestWaitRetryThenDoneCommandCase(t *testing.T) {
	t.Run("wait: the retry Enter unblocks the worker and the wait settles done", func(t *testing.T) { // JS: "wait: the retry Enter unblocks the worker and the wait settles done"
		// Adapted contract: the retry Enter needs a recognized composer
		// (the roster kind, claude) holding the stored path — the marker
		// carries the path and seq, and the screen is the claude box with
		// the exact prompt.
		row := "worker\tp0a\tclaude\timplementer\tanthropic\t\t\t\tgrok-4.7\t\timplementer\tbuild\n"
		sep := strings.Repeat("─", 66)
		box := "Welcome to the worker\n" + sep + "\n❯ Read the file /tmp/worker-brief.md in full and execute it.\n" + sep + "\n  [Opus 5.5] 67% [main*]\n"
		// The Enter the worker takes is the one that unblocks it: the
		// send-keys call returns only once the goroutine below has written
		// the report and then the gate, so the wait's next poll finds the
		// report however late the runner schedules the goroutine. With the
		// prompt window at 0 s every poll that still sees no report sends
		// Enter again, which the count below refuses.
		gate := filepath.Join(t.TempDir(), "enter-gate")
		f := newWaitStatusFixture(t, row, []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle","state_change_seq":5}}}`, Delay: 30},
			{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped", "--lines", "40"}, Stdout: box},
			{Argv: []string{"agent", "send-keys", "worker", "enter"}, WaitFile: gate},
		})
		f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
		report := filepath.Join(f.state, "reports", "worker.md")
		if err := os.WriteFile(filepath.Join(f.state, "last-report-worker"), []byte(report+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.state, "wait", "worker.not-received"), []byte(fmt.Sprintf("%d 5 /tmp/worker-brief.md\n", time.Now().Unix()-120)), 0o600); err != nil {
			t.Fatal(err)
		}
		stop, exited := make(chan struct{}), make(chan struct{})
		go func() {
			defer close(exited)
			ticker := time.NewTicker(time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-stop:
					return
				case <-ticker.C:
					calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
					if err == nil {
						for _, call := range calls {
							if strings.Join(call.Argv, " ") == "agent send-keys worker enter" {
								_ = os.WriteFile(report, []byte("finished\n"), 0o600)
								_ = os.WriteFile(gate, nil, 0o600)
								return
							}
						}
					}
				}
			}
		}()
		code, out, stderr := f.runWait(t, "worker", "--timeout", "60000")
		close(stop)
		<-exited
		calls := f.calls(t)
		result := readWaitCommandResult(t, out)
		if code != 0 || result.Agent != "worker" || result.Status != "done" || result.Report != report || countDispatchCalls(calls, "send-keys") != 1 || !strings.Contains(stderr, "prompt to 'worker' was still in its input box; sent Enter again (1 of 3)") {
			t.Fatalf("code=%d out=%q stderr=%q calls=%#v", code, out, stderr, calls)
		}
	})
}

func TestWaitCheckpointCommandRemainingCases(t *testing.T) {
	row := "worker\tp0a\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\timplementer\tbuild\n"
	t.Run("wait: a still screen (counters aside) warns once after stuck_warn_minutes", func(t *testing.T) { // JS: "wait: a still screen (counters aside) warns once after stuck_warn_minutes"
		screen := "Running tests 42% ◐ 3.1s\n"
		f := newWaitStatusFixture(t, row, []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
			{Argv: []string{"agent", "read", "worker", "--source", "visible", "--lines", "20"}, Stdout: screen},
			{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Stdout: screen},
		})
		f.env["SOHO_STUCK_WARN_MINUTES"] = "1"
		hash := strconv.FormatUint(uint64(waitpkg.CksumField(waitpkg.NormalizeScreen(screen))), 10)
		if err := os.WriteFile(filepath.Join(f.state, "wait", "worker.stuck-hash"), []byte(hash+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.state, "wait", "worker.stuck-since"), []byte(strconv.FormatInt(time.Now().Unix()-70, 10)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			code, out, stderr := f.runWait(t, "worker", "--timeout", "100")
			if code != 9 || !strings.Contains(out, `"status":"timeout"`) || strings.Contains(stderr, "send-keys") {
				t.Fatalf("attempt %d code=%d out=%q stderr=%q", i+1, code, out, stderr)
			}
		}
		warn := "agent 'worker' has shown the same screen (apart from counters) for 1 min while working"
		log, err := os.ReadFile(filepath.Join(f.state, "friction.log"))
		if err != nil || strings.Count(string(log), warn) != 1 {
			t.Fatalf("warning count=%d friction=%q err=%v", strings.Count(string(log), warn), log, err)
		}
		if _, err := os.Stat(filepath.Join(f.state, "wait", "worker.stuck-warned")); err != nil {
			t.Fatalf("stuck-warned marker: %v", err)
		}
	})
	t.Run("wait: a move seen across a long gap between waits is dated at the old probe (not active)", func(t *testing.T) { // JS: "wait: a move seen across a long gap between waits is dated at the old probe (not active)"
		screen := "Screen B now, unchanged since\n"
		f := newWaitStatusFixture(t, row, []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
			{Argv: []string{"agent", "read", "worker", "--source", "visible", "--lines", "20"}, Stdout: screen},
			{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Stdout: screen},
		})
		if err := os.WriteFile(filepath.Join(f.state, "wait", "worker.stuck-hash"), []byte(strconv.FormatUint(uint64(waitpkg.CksumField(waitpkg.NormalizeScreen("Screen A of the old wait\n"))), 10)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.state, "wait", "worker.probe-at"), []byte(strconv.FormatInt(time.Now().Unix()-1800, 10)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, stderr := f.runWait(t, "worker", "--timeout", "100")
		var result struct {
			Checkpoint  bool   `json:"checkpoint"`
			ActivityAge *int64 `json:"activity_age_s"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &result); err != nil {
			t.Fatalf("out=%q: %v", out, err)
		}
		log, err := os.ReadFile(filepath.Join(f.state, "friction.log"))
		if code != 9 || result.Checkpoint || result.ActivityAge == nil || *result.ActivityAge < 1798 || err != nil || !strings.Contains(string(log), "timeout waiting for 'worker'") || !strings.Contains(stderr, "timeout waiting for 'worker'") {
			t.Fatalf("code=%d result=%+v out=%q stderr=%q friction=%q err=%v", code, result, out, stderr, log, err)
		}
	})
	t.Run("wait: with stuck_warn_minutes=0 the hash still follows the screen (no endless fresh change)", func(t *testing.T) { // JS: "wait: with stuck_warn_minutes=0 the hash still follows the screen (no endless fresh change)"
		screen := "A still screen after the earlier wait\n"
		f := newWaitStatusFixture(t, row, []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
			{Argv: []string{"agent", "read", "worker", "--source", "visible", "--lines", "20"}, Stdout: screen},
			{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Stdout: screen},
		})
		f.env["SOHO_STUCK_WARN_MINUTES"] = "0"
		if err := os.WriteFile(filepath.Join(f.state, "wait", "worker.stuck-hash"), []byte(strconv.FormatUint(uint64(waitpkg.CksumField(waitpkg.NormalizeScreen("Another screen from before\n"))), 10)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.state, "wait", "worker.stuck-since"), []byte(strconv.FormatInt(time.Now().Unix()-3600, 10)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			code, out, _ := f.runWait(t, "worker", "--timeout", "100")
			if code != 9 || !strings.Contains(out, `"checkpoint":false`) {
				t.Fatalf("attempt %d code=%d out=%q", i+1, code, out)
			}
		}
		want := strconv.FormatUint(uint64(waitpkg.CksumField(waitpkg.NormalizeScreen(screen))), 10) + "\n"
		if got, err := os.ReadFile(filepath.Join(f.state, "wait", "worker.stuck-hash")); err != nil || string(got) != want {
			t.Fatalf("hash=%q err=%v want=%q", got, err, want)
		}
	})
}

func TestWaitReportMirrorCommandCases(t *testing.T) {
	row := "worker\tp0a\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\timplementer\tbuild\n"
	t.Run("wait: a done report under the $TMPDIR routing is mirrored into the state dir", func(t *testing.T) { // JS: "wait: a done report under the $TMPDIR routing is mirrored into the state dir"
		f := newWaitStatusFixture(t, row, nil)
		tmpReports := waitTmpReports(t, f)
		if err := os.MkdirAll(tmpReports, 0o700); err != nil {
			t.Fatal(err)
		}
		report := filepath.Join(tmpReports, "worker-20260925T100000.md")
		composed := filepath.Join(tmpReports, "worker-20260925T100000.brief.md")
		if err := os.WriteFile(report, []byte("# Report\n\ndone.\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(composed, []byte("# Role: implementer\n\nprompt\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.state, "last-report-worker"), []byte(report+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, stderr := f.runWait(t, "worker", "--timeout", "60000")
		result := readWaitCommandResult(t, out)
		if code != 0 || result.Agent != "worker" || result.Status != "done" || result.Report != report || stderr != "" {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
		stateReport := filepath.Join(f.state, "reports", "worker-20260925T100000.md")
		stateBrief := filepath.Join(f.state, "briefs", "worker-20260925T100000.md")
		for path, want := range map[string]string{stateReport: "# Report\n\ndone.\n", stateBrief: "# Role: implementer\n\nprompt\n", report: "# Report\n\ndone.\n"} {
			if got, err := os.ReadFile(path); err != nil || string(got) != want {
				t.Fatalf("%s=%q err=%v", path, got, err)
			}
		}
		last, err := os.ReadFile(filepath.Join(f.state, "last-report-worker"))
		if err != nil || string(last) != report+"\n" {
			t.Fatalf("last-report=%q err=%v", last, err)
		}
		mr, mb := fileMtime(t, stateReport), fileMtime(t, stateBrief)
		code, out, stderr = f.runWait(t, "worker", "--timeout", "60000")
		if code != 0 || !strings.Contains(out, `"status":"done"`) || stderr != "" || !fileMtime(t, stateReport).Equal(mr) || !fileMtime(t, stateBrief).Equal(mb) {
			t.Fatalf("second wait code=%d out=%q stderr=%q report mtime=%v/%v brief mtime=%v/%v", code, out, stderr, fileMtime(t, stateReport), mr, fileMtime(t, stateBrief), mb)
		}
	})
	t.Run("wait: the mirror keeps a different existing state file and warns", func(t *testing.T) { // JS: "wait: the mirror keeps a different existing state file and warns"
		f := newWaitStatusFixture(t, row, nil)
		tmpReports := waitTmpReports(t, f)
		if err := os.MkdirAll(tmpReports, 0o700); err != nil {
			t.Fatal(err)
		}
		report := filepath.Join(tmpReports, "worker-20260925T100000.md")
		composed := filepath.Join(tmpReports, "worker-20260925T100000.brief.md")
		if err := os.WriteFile(report, []byte("new content\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(composed, []byte("new prompt\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		stateReport := filepath.Join(f.state, "reports", "worker-20260925T100000.md")
		stateBrief := filepath.Join(f.state, "briefs", "worker-20260925T100000.md")
		if err := os.MkdirAll(filepath.Dir(stateReport), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(stateBrief), 0o700); err != nil {
			t.Fatal(err)
		}
		for path, body := range map[string]string{stateReport: "older report\n", stateBrief: "older brief\n"} {
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(f.state, "last-report-worker"), []byte(report+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, stderr := f.runWait(t, "worker", "--timeout", "60000")
		log, err := os.ReadFile(filepath.Join(f.state, "friction.log"))
		result := readWaitCommandResult(t, out)
		if code != 0 || result.Report != report || !strings.Contains(stderr, "kept "+stateReport) || !strings.Contains(stderr, "kept "+stateBrief) || err != nil {
			t.Fatalf("code=%d out=%q stderr=%q log=%q err=%v", code, out, stderr, log, err)
		}
		for path, want := range map[string]string{stateReport: "older report\n", stateBrief: "older brief\n"} {
			if got, err := os.ReadFile(path); err != nil || string(got) != want {
				t.Fatalf("%s=%q err=%v", path, got, err)
			}
		}
		for _, msg := range []string{"kept " + stateReport + ": it differs from " + report + ", which was not copied over it", "kept " + stateBrief + ": it differs from " + composed + ", which was not copied over it"} {
			if strings.Count(string(log), msg) != 1 {
				t.Fatalf("warn %q count=%d log=%q", msg, strings.Count(string(log), msg), log)
			}
		}
	})
	t.Run("wait: the mirror copies the dispatch sidecar (identical kept, different stands)", func(t *testing.T) { // JS: "wait: the mirror copies the dispatch sidecar (identical kept, different stands)"
		f := newWaitStatusFixture(t, row, nil)
		tmpReports := waitTmpReports(t, f)
		if err := os.MkdirAll(tmpReports, 0o700); err != nil {
			t.Fatal(err)
		}
		stem := "worker-20260925T100000"
		report, composed, sidecar := filepath.Join(tmpReports, stem+".md"), filepath.Join(tmpReports, stem+".brief.md"), filepath.Join(tmpReports, stem+".dispatch.json")
		sidecarBody := "{\"version\":1,\"kind\":\"grok\",\"model\":\"grok-4.7\",\"effort\":\"high\",\"submission\":\"accepted\"}\n"
		for path, body := range map[string]string{report: "# Report\n\ndone.\n", composed: "# Role: implementer\n\nprompt\n", sidecar: sidecarBody} {
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(f.state, "last-report-worker"), []byte(report+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		stateSidecar := filepath.Join(f.state, "briefs", stem+".dispatch.json")
		code, out, stderr := f.runWait(t, "worker", "--timeout", "60000")
		result := readWaitCommandResult(t, out)
		if code != 0 || result.Report != report || stderr != "" {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
		if got, err := os.ReadFile(stateSidecar); err != nil || string(got) != sidecarBody {
			t.Fatalf("sidecar=%q err=%v", got, err)
		}
		mtime := fileMtime(t, stateSidecar)
		code, out, stderr = f.runWait(t, "worker", "--timeout", "60000")
		if code != 0 || stderr != "" || !strings.Contains(out, `"status":"done"`) || !fileMtime(t, stateSidecar).Equal(mtime) {
			t.Fatalf("identical copy code=%d out=%q stderr=%q", code, out, stderr)
		}
		if err := os.WriteFile(stateSidecar, []byte("different content\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, stderr = f.runWait(t, "worker", "--timeout", "60000")
		log, err := os.ReadFile(filepath.Join(f.state, "friction.log"))
		if code != 0 || !strings.Contains(stderr, "kept "+stateSidecar) || !strings.Contains(out, `"status":"done"`) || err != nil {
			t.Fatalf("different copy code=%d out=%q stderr=%q log=%q err=%v", code, out, stderr, log, err)
		}
		if got, err := os.ReadFile(stateSidecar); err != nil || string(got) != "different content\n" {
			t.Fatalf("sidecar=%q err=%v", got, err)
		}
		warn := "kept " + stateSidecar + ": it differs from " + sidecar + ", which was not copied over it"
		if strings.Count(string(log), warn) != 1 {
			t.Fatalf("warn count=%d log=%q", strings.Count(string(log), warn), log)
		}
	})
	t.Run("wait: mirroring an old pair without a sidecar is silent", func(t *testing.T) { // JS: "wait: mirroring an old pair without a sidecar is silent"
		f := newWaitStatusFixture(t, row, nil)
		tmpReports := waitTmpReports(t, f)
		if err := os.MkdirAll(tmpReports, 0o700); err != nil {
			t.Fatal(err)
		}
		stem := "worker-20260925T100000"
		report, composed := filepath.Join(tmpReports, stem+".md"), filepath.Join(tmpReports, stem+".brief.md")
		for path, body := range map[string]string{report: "# Report\n\ndone.\n", composed: "# Role: implementer\n\nprompt\n"} {
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(f.state, "last-report-worker"), []byte(report+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, stderr := f.runWait(t, "worker", "--timeout", "60000")
		if code != 0 || stderr != "" || !strings.Contains(out, `"status":"done"`) {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
		if got, err := os.ReadFile(filepath.Join(f.state, "reports", stem+".md")); err != nil || string(got) != "# Report\n\ndone.\n" {
			t.Fatalf("report=%q err=%v", got, err)
		}
		if _, err := os.Stat(filepath.Join(f.state, "briefs", stem+".dispatch.json")); !os.IsNotExist(err) {
			t.Fatalf("sidecar invented or stat error: %v", err)
		}
		log, err := os.ReadFile(filepath.Join(f.state, "friction.log"))
		if err == nil && strings.Contains(string(log), ".dispatch.json") {
			t.Fatalf("absent sidecar warned: %q", log)
		}
	})
}

func waitTmpReports(t *testing.T, f *waitStatusFixture) string {
	t.Helper()
	tmp := filepath.Join(f.root, "tmpdir")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	f.env["TMPDIR"] = tmp
	return filepath.Join(tmp, "soho", "ws", "reports")
}

func fileMtime(t *testing.T, path string) time.Time {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.ModTime()
}
