package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/dispatch"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

const r11Auth = "Error: 401 Unauthorized: Incorrect API key provided"

type r11Case struct {
	title, initial, post, recent string
	mode, check, timeout         string
	seq                          string
	exit                         int
	status                       string
	report, promptEcho, input    bool
	quota, cause                 string
	question, noWait, authScreen bool
	appendAuth                   bool
	noRecent                     bool
	cursorPlan                   bool
}

func TestDispatchR11D58AuthScreens(t *testing.T) {
	// The marker list supplied by the plan contains 23 titles.
	for _, tc := range r11Cases() {
		tc := tc
		// The two dispatch-without-no-wait titles run the wait, as in the JS.
		tc.noWait = tc.title != "dispatch: a stale blocked auth screen ends not-received, never provider-error (R11/D58)" &&
			tc.title != "dispatch: a newly observed auth screen reports provider-error on the first wait probe (R11/D58)"
		if strings.Contains(tc.title, "quota wins over an auth") || strings.Contains(tc.title, "decision question") || strings.Contains(tc.title, "transient provider") || strings.Contains(tc.title, "40-line rollover") || strings.Contains(tc.title, "source-like") || strings.Contains(tc.title, "shifted repeated") || strings.Contains(tc.title, "redrawn old auth") || strings.Contains(tc.title, "redraw with ordinary") || strings.Contains(tc.title, "newly observed auth") || strings.Contains(tc.title, "new auth line still") || strings.Contains(tc.title, "replaying an old auth frame") || strings.Contains(tc.title, "outside the detector tail") || strings.Contains(tc.title, "completed report wins over a new auth") || strings.Contains(tc.title, "completed report wins over quota") {
			tc.authScreen = true
		}
		if strings.Contains(tc.title, "stale blocked auth screen ends") {
			tc.seq = "7"
		}
		if tc.input {
			tc.seq = "1"
		}
		if strings.Contains(tc.title, "appended identical auth line") {
			tc.appendAuth = true
		}
		if strings.Contains(tc.title, "40-line rollover") {
			tc.post = r11RolloverPost()
		}
		if strings.Contains(tc.title, "shifted repeated") {
			tc.post = r11ShiftedPost()
		}
		t.Run(tc.title, func(t *testing.T) {
			verifyR11Case(t, tc)
		})
	}
}

func r11Cases() []r11Case {
	return []r11Case{
		// JS: "dispatch --no-wait: an auth screen that predates the prompt is not attributed (R11/D58)"
		{title: "dispatch --no-wait: an auth screen that predates the prompt is not attributed (R11/D58)", initial: r11Auth, post: r11Auth, recent: r11Auth, mode: "idle", check: "1", exit: 0, status: "submitted", report: true},
		// JS: "dispatch --no-wait: a completed report wins over a retained auth screen (R11/D58)"
		{title: "dispatch --no-wait: a completed report wins over a retained auth screen (R11/D58)", initial: "Error: 401 Unauthorized: old credentials", post: "Error: 401 Unauthorized: old credentials\nprompt received: ok", mode: "idle", check: "1", exit: 0, status: "submitted", report: true, promptEcho: true},
		// JS: "dispatch --no-wait: a completed report wins over a new auth line (R11/D58)"
		{title: "dispatch --no-wait: a completed report wins over a new auth line (R11/D58)", initial: "Error: 401 Unauthorized: old credentials", post: "Error: 401 Unauthorized: old credentials\n■ Failed to refresh access token: the refresh token was revoked, please log in again", mode: "idle", check: "1", exit: 0, status: "submitted", report: true, promptEcho: true},
		// JS: "dispatch --no-wait: a completed report wins over quota (R11/D58)"
		{title: "dispatch --no-wait: a completed report wins over quota (R11/D58)", initial: "Welcome to the worker", post: "Individual quota reached\ntry again in 2 hours", mode: "idle", check: "1", exit: 0, status: "submitted", report: true, promptEcho: true},
		// JS: "dispatch --no-wait: quota wins over an auth failure on the same screen (R11/D58)"
		{title: "dispatch --no-wait: quota wins over an auth failure on the same screen (R11/D58)", initial: "Welcome to the worker", post: "Individual quota reached\ntry again in 2 hours\n" + r11Auth, recent: "Individual quota reached\ntry again in 2 hours\n" + r11Auth, mode: "idle", check: "1", exit: 11, status: "quota", quota: "Individual quota reached"},
		// JS: "dispatch --no-wait: a current decision question skips the auth attribution (R11/D58)"
		{title: "dispatch --no-wait: a current decision question skips the auth attribution (R11/D58)", initial: "Welcome to the worker", post: r11Auth + "\n  1. Retry with a new key\n  2. Abort the run\n\nEnter to submit answer, esc to cancel", recent: r11Auth + "\n  1. Retry with a new key\n  2. Abort the run\n\nEnter to submit answer, esc to cancel", mode: "idle", check: "1", exit: 0, status: "submitted", question: true},
		// JS: "dispatch --no-wait: a transient provider failure is not attributed (R11/D58)"
		{title: "dispatch --no-wait: a transient provider failure is not attributed (R11/D58)", initial: "Welcome to the worker", post: "Error: Connection error.", recent: "Error: Connection error.", mode: "idle", check: "1", exit: 0, status: "submitted"},
		// JS: "dispatch --no-wait: prompt_check_seconds=0 observes nothing on an auth screen (R11/D58)"
		{title: "dispatch --no-wait: prompt_check_seconds=0 observes nothing on an auth screen (R11/D58)", initial: r11Auth, post: r11Auth, recent: r11Auth, mode: "idle", check: "0", exit: 0, status: "submitted"},
		// JS: "dispatch --no-wait: a completed report wins during stale-auth arrival (R11/D58)"
		{title: "dispatch --no-wait: a completed report wins during stale-auth arrival (R11/D58)", initial: r11Auth, post: r11Auth, recent: r11Auth, mode: "blocked", check: "1", exit: 0, status: "submitted", report: true},
		// JS: "dispatch: a stale blocked auth screen ends not-received, never provider-error (R11/D58)"
		{title: "dispatch: a stale blocked auth screen ends not-received, never provider-error (R11/D58)", initial: r11Auth, post: r11Auth, recent: r11Auth, mode: "blocked", check: "1", timeout: "5000", exit: 15, status: "not-received", noRecent: true, noWait: false},
		// JS: "dispatch --no-wait: an identical auth failure across a 40-line rollover remains unconfirmed (R11/D58)"
		{title: "dispatch --no-wait: an identical auth failure across a 40-line rollover remains unconfirmed (R11/D58)", initial: r11Window(40, 31, 0), post: r11Window(40, 0, 1), recent: r11Window(40, 0, 1), mode: "idle", check: "1", exit: 0, status: "submitted", promptEcho: true},
		// JS: "dispatch --no-wait: repeated retained old auth lines plus echo remain submitted (R11/D58)"
		{title: "dispatch --no-wait: repeated retained old auth lines plus echo remain submitted (R11/D58)", initial: r11Auth + "\noutput line 02\n" + r11Auth, post: r11Auth + "\noutput line 02\n" + r11Auth + "\nprompt received: ok", mode: "idle", check: "1", exit: 0, status: "submitted", promptEcho: true},
		// JS: "dispatch --no-wait: source-like auth text does not count as new evidence (R11/D58)"
		{title: "dispatch --no-wait: source-like auth text does not count as new evidence (R11/D58)", initial: r11Auth, post: r11Auth + "\nprompt received: ok\n// " + r11Auth, recent: r11Auth + "\nprompt received: ok\n// " + r11Auth, mode: "idle", check: "1", exit: 0, status: "submitted", promptEcho: true},
		// JS: "dispatch --no-wait: shifted repeated old auth lines remain submitted (R11/D58)"
		{title: "dispatch --no-wait: shifted repeated old auth lines remain submitted (R11/D58)", initial: r11Window(40, 6, 31), post: r11Window(40, 6, 31) + "\nprompt received: ok\noutput line 41\noutput line 42\noutput line 43\noutput line 44", mode: "idle", check: "1", exit: 0, status: "submitted", promptEcho: true},
		// JS: "dispatch --no-wait: a redrawn old auth line remains submitted (R11/D58)"
		{title: "dispatch --no-wait: a redrawn old auth line remains submitted (R11/D58)", initial: r11Auth + "\noutput line 02\noutput line 03\noutput line 04\noutput line 05\noutput line 06\noutput line 07\noutput line 08\noutput line 09\noutput line 10", post: "output line 02\noutput line 03\noutput line 04\noutput line 05\noutput line 06\noutput line 07\noutput line 08\noutput line 09\noutput line 10\n" + r11Auth, mode: "idle", check: "1", exit: 0, status: "submitted", promptEcho: true},
		// JS: "dispatch --no-wait: a redraw with ordinary lines remains submitted (R11/D58)"
		{title: "dispatch --no-wait: a redraw with ordinary lines remains submitted (R11/D58)", initial: r11Auth + "\nprevious screen line A\nprevious screen line B", post: "redrawn screen line X\nredrawn screen line Y\n" + r11Auth, mode: "idle", check: "1", exit: 0, status: "submitted", promptEcho: true},
		// JS: "dispatch: a newly observed auth screen reports provider-error on the first wait probe (R11/D58)"
		{title: "dispatch: a newly observed auth screen reports provider-error on the first wait probe (R11/D58)", initial: "Welcome to the worker", post: r11Auth, recent: r11Auth, mode: "idle", check: "1", timeout: "5000", exit: 14, status: "provider-error", cause: r11Auth},
		// JS: "dispatch --no-wait: a scrollback-only append does not attribute the old auth failure (R11/D58)"
		{title: "dispatch --no-wait: a scrollback-only append does not attribute the old auth failure (R11/D58)", initial: r11Auth, post: r11Auth + "\nprompt received: ok", recent: r11Auth, mode: "idle", check: "1", exit: 0, status: "submitted", promptEcho: true},
		// JS: "dispatch --no-wait: an old auth error plus a new auth line still exits 14 (R11/D58)"
		{title: "dispatch --no-wait: an old auth error plus a new auth line still exits 14 (R11/D58)", initial: r11Auth, post: r11Auth + "\n■ Failed to refresh access token: the refresh token was revoked, please log in again", recent: r11Auth + "\n■ Failed to refresh access token: the refresh token was revoked, please log in again", mode: "idle", check: "1", exit: 14, status: "provider-error", cause: "refresh token was revoked"},
		// JS: "dispatch --no-wait: an appended identical auth line remains unconfirmed (R11/D58)"
		{title: "dispatch --no-wait: an appended identical auth line remains unconfirmed (R11/D58)", initial: r11Auth, post: r11Auth + "\nprompt received: ok\n" + r11Auth, recent: r11Auth + "\n" + r11Auth, mode: "idle", check: "1", exit: 0, status: "submitted", promptEcho: true},
		// JS: "dispatch --no-wait: replaying an old auth frame remains unconfirmed (R11/D58)"
		{title: "dispatch --no-wait: replaying an old auth frame remains unconfirmed (R11/D58)", initial: r11Auth + "\nold frame line A\nold frame line B", post: r11Auth + "\nold frame line A\nold frame line B\nprompt received: ok\n" + r11Auth + "\nold frame line A\nold frame line B", mode: "idle", check: "1", exit: 0, status: "submitted", promptEcho: true},
		// JS: "dispatch --no-wait: Enter does not make a preexisting identical auth cause new (R11/D58)"
		{title: "dispatch --no-wait: Enter does not make a preexisting identical auth cause new (R11/D58)", initial: r11Auth, post: r11Auth, recent: r11Auth, mode: "idle", check: "1", exit: 0, status: "submitted", input: true},
		// JS: "dispatch --no-wait: an auth line outside the detector tail remains unattributed when replayed (R11/D58)"
		{title: "dispatch --no-wait: an auth line outside the detector tail remains unattributed when replayed (R11/D58)", initial: r11Auth + "\nold output 0\nold output 1\nold output 2\nold output 3\nold output 4\nold output 5\nold output 6\nold output 7\nold output 8\nold output 9\nold output 10\nold output 11\nold output 12\nold output 13\nold output 14\nold output 15\nold output 16\nold output 17\nold output 18\nold output 19", post: r11Auth + "\nold output 0\nold output 1\nold output 2\nold output 3\nold output 4\nold output 5\nold output 6\nold output 7\nold output 8\nold output 9\nold output 10\nold output 11\nold output 12\nold output 13\nold output 14\nold output 15\nold output 16\nold output 17\nold output 18\nold output 19\nprompt received: ok\n" + r11Auth, mode: "idle", check: "1", exit: 0, status: "submitted", promptEcho: true},
	}
}

func r11Window(n, authA, authB int) string {
	lines := make([]string, n)
	for i := range lines {
		line := fmt.Sprintf("output line %02d", i+1)
		if i+1 == authA || (authB != 0 && i+1 == authB) {
			line = r11Auth
		}
		lines[i] = line
	}
	return strings.Join(lines, "\n")
}

func r11RolloverPost() string {
	pre := strings.Split(r11Window(40, 31, 0), "\n")
	lines := append([]string{}, pre[31:]...)
	lines = append(lines, "prompt received: ok")
	for n := 41; n <= 69; n++ {
		lines = append(lines, fmt.Sprintf("output line %02d", n))
	}
	lines = append(lines, r11Auth)
	return strings.Join(lines, "\n")
}

func r11ShiftedPost() string {
	pre := strings.Split(r11Window(40, 6, 31), "\n")
	lines := append([]string{}, pre[5:]...)
	lines = append(lines, "prompt received: ok", "output line 41", "output line 42", "output line 43", "output line 44")
	return strings.Join(lines, "\n")
}

func verifyR11Case(t *testing.T, tc r11Case) {
	t.Helper()
	f := newR11Fixture(t, tc)
	args := []string{"worker", f.brief}
	if tc.timeout != "" {
		args = append(args, "--timeout", tc.timeout)
	}
	if tc.noWait {
		args = append(args, "--no-wait")
	}
	if tc.report {
		f.installReportWriter(t)
	}
	if tc.input {
		f.writeSeq(t, "1")
	}
	code, stdout, stderr := f.run(t, args...)
	var got map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &got); err != nil {
		t.Fatalf("code=%d stderr=%q output=%q: %v", code, stderr, stdout, err)
	}
	actualStatus, _ := got["wait_status"].(string)
	if code != tc.exit || actualStatus != tc.status {
		t.Fatalf("code=%d wait_status=%s; want code=%d wait_status=%s; stderr=%q output=%s", code, actualStatus, tc.exit, tc.status, stderr, stdout)
	}
	if actualStatus == "provider-error" {
		if got["lane"] != "build" || got["model"] != "grok-4.7" || !strings.Contains(fmt.Sprint(got["cause"]), tc.cause) || got["report_exists"] != false {
			t.Fatalf("provider fields=%v; want lane=build model=grok-4.7 cause contains %q report_exists=false", got, tc.cause)
		}
		if !strings.Contains(stderr, "stopped on a provider error: "+fmt.Sprint(got["cause"])) || !strings.Contains(stderr, "It is idle without a report; ask the user whether to resend the brief, switch the assistant, or wait.") {
			t.Fatalf("stderr=%q, want provider-error warning", stderr)
		}
	} else if actualStatus == "quota" {
		if got["lane"] != "build" || got["model"] != "grok-4.7" || !strings.Contains(fmt.Sprint(got["match"]), tc.quota) || fmt.Sprint(got["renewal"]) != "try again in 2 hours" {
			t.Fatalf("quota fields=%v", got)
		}
	} else if actualStatus == "not-received" {
		if got["report_exists"] != false {
			t.Fatalf("not-received fields=%v", got)
		}
		marker := filepath.Join(f.state, "ws", "wait", "worker.not-received")
		if _, err := os.Stat(marker); err != nil {
			t.Fatalf("not-received marker: %v", err)
		}
		mark, err := os.ReadFile(marker)
		if err != nil || !strings.HasSuffix(string(mark), " 7\n") || len(strings.Fields(string(mark))) != 2 {
			t.Fatalf("not-received marker content=%q err=%v", mark, err)
		}
		if !strings.Contains(stderr, "was not received after its block on a provider auth error") {
			t.Fatalf("stderr=%q; missing stale-auth arrival explanation", stderr)
		}
		if _, err := os.Stat(filepath.Join(f.state, "ws", "wait", "worker.provider-cause")); !os.IsNotExist(err) {
			t.Fatalf("unexpected provider-cause marker: err=%v", err)
		}
	} else {
		if !tc.input && stderr != "" {
			t.Fatalf("stderr=%q, want no warning", stderr)
		}
		if tc.input && (!strings.Contains(stderr, "sat in the input box; sent Enter") || strings.Contains(stderr, "provider error") || strings.Contains(stderr, "was not received")) {
			t.Fatalf("input-box stderr=%q", stderr)
		}
		if _, err := os.Stat(filepath.Join(f.state, "ws", "wait", "worker.not-received")); !os.IsNotExist(err) {
			t.Fatalf("unexpected not-received marker: err=%v", err)
		}
	}
	if actualStatus == "submitted" && !tc.report && got["report_exists"] != false {
		t.Fatalf("submitted report_exists=%v, want false", got["report_exists"])
	}
	if tc.report && got["report_exists"] != true {
		t.Fatalf("completed report not reflected in JSON: %v", got)
	}
	if tc.report {
		reportPath, _ := got["report"].(string)
		body, err := os.ReadFile(reportPath)
		if err != nil || string(body) != "# Report\n\ndone.\n" {
			t.Fatalf("report content=%q err=%v", body, err)
		}
	}
	value, err := jsonjs.Parse([]byte(strings.TrimSpace(stdout)))
	if err != nil {
		t.Fatalf("dispatch JSON parse: %v", err)
	}
	obj, ok := value.(*jsonjs.Object)
	if !ok {
		t.Fatalf("dispatch JSON has type %T", value)
	}
	wantKeys := []string{"wait_status", "agent", "role", "kind", "composed_prompt", "report", "task_report", "report_exists"}
	switch actualStatus {
	case "submitted":
		wantKeys = append(wantKeys, "auto_approved")
		if tc.input {
			wantKeys = append(wantKeys, "enter_sent")
		}
	case "provider-error":
		wantKeys = append(wantKeys, "auto_approved", "lane", "model", "cause")
	case "quota":
		wantKeys = append(wantKeys, "auto_approved", "lane", "model", "match", "renewal")
	}
	if !reflect.DeepEqual(obj.Keys(), wantKeys) {
		t.Fatalf("JSON keys=%v want=%v", obj.Keys(), wantKeys)
	}
	if tc.input { // Enter is set by the input box flow, even when status stays submitted.
		if got["enter_sent"] != true {
			t.Fatalf("enter_sent=%v", got["enter_sent"])
		}
	}
	composed, _ := got["composed_prompt"].(string)
	sidecar := dispatch.DispatchSidecar(composed)
	data, err := os.ReadFile(sidecar)
	if err != nil {
		t.Fatalf("sidecar %s: %v", sidecar, err)
	}
	var side map[string]any
	if err := json.Unmarshal(data, &side); err != nil {
		t.Fatalf("sidecar JSON: %v", err)
	}
	if side["submission"] != "accepted" {
		t.Fatalf("sidecar=%v", side)
	}
	if actualStatus == "not-received" {
		if side["arrival"] != "not-received" {
			t.Fatalf("not-received sidecar=%v", side)
		}
	} else if _, ok := side["arrival"]; ok {
		t.Fatalf("unexpected arrival mark: %v", side)
	}
	calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	prompts, enters := countDispatchCalls(calls, "prompt"), countDispatchCalls(calls, "send-keys")
	wantPrompts := 1
	if tc.status == "not-received" && !tc.noRecent {
		wantPrompts = 1
	}
	if prompts != wantPrompts || enters != boolInt(tc.input) {
		t.Fatalf("prompts=%d enters=%d; calls=%#v", prompts, enters, calls)
	}
	if tc.check == "0" {
		for _, call := range calls {
			if len(call.Argv) > 1 && (call.Argv[1] == "get" || call.Argv[1] == "read") {
				t.Fatalf("check=0 made probe: %#v", call.Argv)
			}
		}
	}
	if strings.Contains(tc.title, "completed report wins during stale-auth arrival") {
		promptAt := -1
		for i, call := range calls {
			if len(call.Argv) > 1 && call.Argv[1] == "prompt" {
				promptAt = i
				break
			}
		}
		if promptAt < 0 {
			t.Fatal("dispatch prompt missing from call log")
		}
		for _, call := range calls[promptAt+1:] {
			if len(call.Argv) > 1 && call.Argv[0] == "agent" && (call.Argv[1] == "get" || call.Argv[1] == "read") {
				t.Fatalf("completed report triggered a post-submit probe: %#v", call.Argv)
			}
		}
	}
}

type r11Fixture struct {
	root, state, bin, brief string
	env                     platform.Env
	tc                      r11Case
}

func newR11Fixture(t *testing.T, tc r11Case) *r11Fixture {
	t.Helper()
	fixedNow := platform.Now
	baseTime, nowCalls := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC), 0
	platform.Now = func() time.Time {
		nowCalls++
		return baseTime.Add(time.Duration(nowCalls-1) * 2 * time.Second)
	}
	t.Cleanup(func() { platform.Now = fixedNow })
	root := t.TempDir()
	state, roles, bin := filepath.Join(root, "state"), filepath.Join(root, "roles"), filepath.Join(root, "bin")
	for _, dir := range []string{filepath.Join(state, "ws", "wait"), filepath.Join(state, "ws", "briefs"), filepath.Join(state, "ws", "reports"), roles, bin} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(roles, "implementer.md"), []byte("---\nname: Implementer\nmode: edit\n---\nRole body.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	brief := filepath.Join(root, "brief.md")
	if err := os.WriteFile(brief, []byte("# Goal\n\nDispatch a prompt.\n# Expected result\n\nObserved.\n# Owned files\n\nnone\n# Forbidden\n\nNo commit or push.\n# Report\n\ndone\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	model, kind, family := "grok-4.7", "grok", "xai"
	if tc.cursorPlan {
		model, kind = "grok-4.7-xhigh", "cursor"
	}
	if tc.question || tc.input {
		model, kind, family = "gpt-5", "codex", "openai"
	}
	roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\tmode\targs\teffort\nworker\tw0test:p0a\t" + kind + "\timplementer\t" + family + "\t0\t\tnow\t" + model + "\ttask\timplementer\tbuild\t\t\n"
	if err := os.WriteFile(filepath.Join(state, "ws", "agents.tsv"), []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	mode := tc.mode
	if mode == "" {
		mode = "idle"
	}
	composed := filepath.Join(state, "ws", "briefs", "worker-"+core.NowStamp(time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC))+".md")
	initial := tc.initial
	if initial != "" && !strings.HasSuffix(initial, "\n") {
		initial += "\n"
	}
	screen := initial
	if tc.authScreen {
		screen = "Reading " + composed + "\n.\n.\n.\n" + tc.post
	} else if tc.input {
		// Adapted contract: the first Enter needs a recognized composer
		// holding the exact composed path — the codex composer line with the
		// real brief path, not a foreign-path marker line.
		screen = initial + "› Read the file " + composed + " in full and execute it.\n"
	} else if tc.report && tc.promptEcho {
		screen = initial + "prompt received: ok\n"
	} else if tc.appendAuth {
		screen = initial + "Reading " + composed + "\n.\n.\n.\nprompt received: ok\n" + tc.post
	} else if tc.promptEcho {
		screen = initial + "Reading " + composed + "\n.\n.\n.\nprompt received: ok\n"
	}
	if screen != "" && !strings.HasSuffix(screen, "\n") {
		screen += "\n"
	}
	stateJSON := func(currentMode string, seq string) string {
		seqField := ""
		if seq != "" {
			seqField = `,"state_change_seq":` + seq
		}
		return `{"result":{"agent":{"agent_status":"` + currentMode + `"` + seqField + `}}}`
	}
	initialSeq := tc.seq
	// Auth attribution requires actual receipt first. These frames used to
	// pass on a path in scrollback; explicitly model a new accepted turn
	// followed by the fixture's final idle/done state instead of that echo.
	acceptedReceipt := !tc.report && !tc.input && tc.check != "0" && (tc.authScreen || tc.promptEcho || tc.appendAuth)
	if acceptedReceipt && initialSeq == "" {
		initialSeq = "1"
	}
	stateInitial := stateJSON(mode, initialSeq)
	statePost := stateJSON(mode, initialSeq)
	stateAfterPrompt, stateAfterArrival := stateInitial, stateInitial
	if acceptedReceipt {
		statePost = stateJSON(mode, "2")
		stateAfterPrompt = stateJSON("blocked", "2")
		stateAfterArrival = statePost
	}
	if tc.input {
		statePost = stateJSON("blocked", "2")
	}
	visible := []fakecli.Rule{
		{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Call: 1, Stdout: initial},
		{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Call: 2, Stdout: screen},
		{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Call: 3, Stdout: screen},
		{Argv: []string{"agent", "read", "worker", "--source", "visible", "--lines", "20"}, Stdout: screen},
		{Argv: []string{"agent", "read", "worker", "--source", "visible", "--lines", "40"}, Stdout: screen},
	}
	if tc.cursorPlan {
		visible = append(visible, fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Stdout: screen})
	}
	if tc.input {
		afterEnter := tc.post
		if afterEnter != "" && !strings.HasSuffix(afterEnter, "\n") {
			afterEnter += "\n"
		}
		visible[2].Stdout = afterEnter
		visible[3].Stdout = afterEnter
		visible[4].Stdout = afterEnter
	}
	var rules []fakecli.Rule
	rules = append(rules, fakecli.Rule{Argv: []string{"agent", "list"}, ArgvPrefix: true, Stdout: "{\"result\":{\"agents\":[]}}"})
	rules = append(rules, visible...)
	recent := tc.recent
	if recent == "" || tc.promptEcho || tc.authScreen || tc.appendAuth {
		recent = screen
	}
	rules = append(rules,
		fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped", "--lines", "40"}, Call: 1, Stdout: initial},
		fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped", "--lines", "40"}, Call: 2, Stdout: recent},
		fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped", "--lines", "40"}, Stdout: recent},
		fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: stateInitial},
		fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Call: 2, Stdout: stateAfterPrompt},
		fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Call: 3, Stdout: stateAfterArrival},
	)
	reportReady := ""
	if tc.report {
		// The prompt returns only after its report exists. A delay cannot
		// coordinate this writer with dispatch's deliberately fast fake clock.
		reportReady = filepath.Join(root, "report-ready")
	}
	rules = append(rules,
		fakecli.Rule{Argv: []string{"agent", "get", "worker"}, ArgvPrefix: true, Stdout: statePost},
		fakecli.Rule{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Stdout: "{\"result\":{\"submitted\":true}}", WaitFile: reportReady},
		fakecli.Rule{Argv: []string{"agent", "send-keys", "worker", "enter"}, ArgvPrefix: true},
		fakecli.Rule{Argv: []string{"pane", "title"}, ArgvPrefix: true, Code: 1},
	)
	if _, err := fakecli.Install(t, bin, "herdr", rules); err != nil {
		t.Fatal(err)
	}
	entries := fakecli.Env(testutil.CleanEnv(t), bin)
	env := platform.Env{}
	for _, entry := range entries {
		if k, v, ok := strings.Cut(entry, "="); ok {
			env[k] = v
		}
	}
	env["HERDR_ENV"], env["SOHO_DIR"], env["HERDR_WORKSPACE_ID"] = "1", state, "ws"
	env["SOHO_ROLES"], env["SOHO_SKILL_DIR"] = roles, "../../skills/soho"
	check := tc.check
	if check == "" {
		check = "1"
	}
	env["SOHO_PROMPT_CHECK_SECONDS"], env["SOHO_PROMPT_SETTLE_SECONDS"] = check, "0"
	env["SOHO_BRIEF_LINT"], env["TMPDIR"] = "off", root
	env["HERDR_SOCKET_PATH"] = filepath.Join(root, "missing.sock")
	return &r11Fixture{root: root, state: state, bin: bin, brief: brief, env: env, tc: tc}
}

func (f *r11Fixture) run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, stderr bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &stderr
	code := func() int {
		defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
		return Run(append([]string{"dispatch"}, args...), f.env)
	}()
	return code, out.String(), stderr.String()
}

func (f *r11Fixture) installReportWriter(t *testing.T) {
	t.Helper()
	// The generated report path is parsed from the dispatched prompt so the
	// fixture writes exactly the report selected by dispatch, including suffixes.
	stop, done := make(chan struct{}), make(chan struct{})
	var writeErr error
	t.Cleanup(func() {
		close(stop)
		<-done
		if writeErr != nil {
			t.Errorf("report writer: %v", writeErr)
		}
	})
	go func() {
		defer close(done)
		// Release even on a write error: the assertions and cleanup report it
		// without waiting for the fake CLI's deadlock watchdog.
		defer func() {
			if err := os.WriteFile(filepath.Join(f.root, "report-ready"), nil, 0o600); err != nil && writeErr == nil {
				writeErr = err
			}
		}()
		log := filepath.Join(f.bin, "herdr.calls.jsonl")
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			calls, _ := fakecli.ReadCalls(log)
			for _, call := range calls {
				if len(call.Argv) < 4 || call.Argv[0] != "agent" || call.Argv[1] != "prompt" {
					continue
				}
				text := call.Argv[len(call.Argv)-1]
				const marker = "write your report to "
				_, rest, ok := strings.Cut(text, marker)
				if !ok {
					continue
				}
				path, _, ok := strings.Cut(rest, " and reply with exactly that path")
				if !ok {
					continue
				}
				writeErr = os.WriteFile(path, []byte("# Report\n\ndone.\n"), 0o600)
				return
			}
			select {
			case <-stop:
				return
			case <-ticker.C:
			}
		}
	}()
}

func (f *r11Fixture) writeSeq(t *testing.T, value string) {
	t.Helper()
	_ = os.WriteFile(filepath.Join(f.root, "fake-seq"), []byte(value+"\n"), 0o600)
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
