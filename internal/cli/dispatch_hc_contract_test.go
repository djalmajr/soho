package cli

// DJA-604 2b-3a fixes (the review of the slice): the ported contract probes.
// Under hc the dispatch decides by the hc state alone, inside its
// deadlines: no screen read or key before the send for the kinds hc
// accepts, the kinds whose status hc cannot report are refused before
// anything is read or sent, the busy retry and the arrival proof stay
// inside their budgets, and the turn is a uint64 without narrowing. The
// fake-hc fixture is the one dispatch_hc_test.go defines; the assertions
// here are the reviewer's probes, committed.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/taskreport"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestDispatchHcAmendBusyThenWaitAndNewTask(t *testing.T) {
	f := newDispatchHcFixture(t, nil)
	clock := platform.Now
	var offset time.Duration
	platform.Now = func() time.Time { return clock().Add(offset) }
	t.Cleanup(func() { platform.Now = clock })
	setWorker := func(state string, busy bool) {
		t.Helper()
		bin := t.TempDir()
		send := fakecli.Rule{Argv: []string{"send", "--if", "idle", "--json", "--", "worker"}, ArgvPrefix: true, Stdout: `{"turn":7,"started":true}`}
		if busy {
			send.Code = 10
			send.Stdout = ""
			send.Stderr = `{"error":{"kind":"status_mismatch","message":"busy"}}`
		}
		rules := append([]fakecli.Rule{
			{Argv: []string{"get", "t1", "--json"}, ArgvPrefix: true, Stdout: hcDispatchCaller},
			{Argv: []string{"ls", "--label", "soho.session=sX", "--json"}, ArgvPrefix: true, Stdout: hcDispatchSessionLS},
		}, guardProbeRules()...)
		rules = append(rules, fakecli.Rule{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(7, state)}, send)
		if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
			t.Fatal(err)
		}
		f.env["PATH"], f.env["SOHO_FAKECLI_CONFIG"] = bin, bin
	}
	decode := func(out string) map[string]any {
		t.Helper()
		var obj map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &obj); err != nil {
			t.Fatalf("output=%q: %v", out, err)
		}
		return obj
	}
	setWorker("idle", false)
	code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
	if code != 0 {
		t.Fatalf("first dispatch: exit=%d out=%q stderr=%q", code, out, stderr)
	}
	first := decode(out)
	firstReport, ok := first["report"].(string)
	if !ok || firstReport == "" || first["wait_status"] != "submitted" {
		t.Fatalf("first=%v", first)
	}
	if err := os.WriteFile(firstReport, []byte("# First task report\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	offset += 5 * time.Second
	setWorker("working", true)
	code, out, stderr = f.run(t, "worker", f.brief, "--amend", "--no-wait")
	t.Logf("refused amendment stdout: %s", strings.TrimSpace(out))
	t.Logf("refused amendment stderr: %s", strings.TrimSpace(stderr))
	if code != 4 || !strings.Contains(out, "amendment was not sent") || !strings.Contains(out, "without --amend") {
		t.Fatalf("amend exit=%d out=%q stderr=%q", code, out, stderr)
	}
	amend := decode(out)
	if amend["report"] == firstReport {
		t.Fatalf("amendment reused first report: %v", amend)
	}
	var waitOut, waitErr bytes.Buffer
	oldOut, oldErr := platform.Stdout, platform.Stderr
	platform.Stdout, platform.Stderr = &waitOut, &waitErr
	waitCode := func() int {
		defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
		return Run([]string{"wait", "worker", "--timeout", "5s"}, f.env)
	}()
	if waitCode != 15 || !strings.Contains(waitOut.String(), `"status":"not-received"`) || !strings.Contains(waitOut.String(), `"cause":"submission-failed"`) || !strings.Contains(waitErr.String(), "the report on disk belongs to the previous task") {
		t.Fatalf("wait exit=%d out=%q stderr=%q", waitCode, waitOut.String(), waitErr.String())
	}
	offset += 5 * time.Second
	setWorker("idle", false)
	if err := os.WriteFile(f.brief, []byte("# Goal\n\nNew task.\n# Report\n\ndone\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, stderr = f.run(t, "worker", f.brief, "--no-wait")
	if code != 0 {
		t.Fatalf("new dispatch: exit=%d out=%q stderr=%q", code, out, stderr)
	}
	last := decode(out)
	if last["wait_status"] != "submitted" || last["report"] == firstReport || last["report"] == amend["report"] {
		t.Fatalf("new task=%v first=%v amend=%v", last, first, amend)
	}
	ptr := taskreport.ReadTaskReportPointer(f.sd, "worker")
	if ptr == nil {
		t.Fatal("missing new task pointer")
	}
	current, _ := ptr.Get("current")
	if current != last["report"] {
		t.Fatalf("current=%v new report=%v", current, last["report"])
	}
	if _, err := os.Stat(filepath.Join(f.sd, "wait", "worker.submit-failed")); !os.IsNotExist(err) {
		t.Fatalf("failed marker remains: %v", err)
	}
}

func TestDispatchHcAmendBusyPreservesAttribution(t *testing.T) {
	f := newDispatchHcFixture(t, append(guardProbeRules(), []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(7, "working")},
		{Argv: []string{"send"}, ArgvPrefix: true, Code: 10, Stderr: `{"error":{"kind":"status_mismatch","message":"busy"}}`},
	}...))
	old := filepath.Join(f.sd, "reports", "previous.md")
	pointer := []byte(fmt.Sprintf(`{"version":1,"task_report":%q,"current":%q,"history":[],"role":"implementer","pane":"t7"}`, old, old) + "\n")
	path := taskreport.TaskReportPointerPath(f.sd, "worker")
	if err := os.WriteFile(path, pointer, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.sd, "last-report-worker"), []byte(old+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	code, out, stderr := f.run(t, "worker", f.brief, "--amend", "--no-wait", "--timeout", "1s")
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("dispatch took %v", elapsed)
	}
	if code != 4 || !strings.Contains(out, "amendment was not sent") || !strings.Contains(out, "without --amend") {
		t.Fatalf("exit=%d out=%q stderr=%q", code, out, stderr)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(pointer, after) {
		t.Fatalf("pointer=%q err=%v", after, err)
	}
	if _, err := os.Stat(filepath.Join(f.sd, "wait", "worker.submit-failed")); err != nil {
		t.Fatal(err)
	}
}

func TestDispatchHcAmendUnconfirmedSlowGet(t *testing.T) {
	for _, reply := range []string{`{"turn":7,"started":false}`, `not-json`} {
		t.Run(reply, func(t *testing.T) {
			f := newDispatchHcFixture(t, append(guardProbeRules(), []fakecli.Rule{
				{Argv: []string{"get", "worker", "--json"}, Call: 1, Stdout: hcWorkerGet(7, "working")},
				{Argv: []string{"get", "worker", "--json"}, Call: 2, Stdout: hcWorkerGet(7, "working")},
				{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Delay: 4000, Stdout: hcWorkerGet(7, "working")},
				{Argv: []string{"send"}, ArgvPrefix: true, Stdout: reply},
			}...))
			f.env["SOHO_PROMPT_SETTLE_SECONDS"] = "0"
			old := filepath.Join(f.sd, "reports", "previous.md")
			pointer := []byte(fmt.Sprintf(`{"version":1,"task_report":%q,"current":%q,"history":[],"role":"implementer","pane":"t7"}`, old, old) + "\n")
			path := taskreport.TaskReportPointerPath(f.sd, "worker")
			if err := os.WriteFile(path, pointer, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(f.sd, "last-report-worker"), []byte(old+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			code, out, stderr := f.run(t, "worker", f.brief, "--amend", "--no-wait")
			elapsed := time.Since(start)
			if elapsed > 3*time.Second {
				t.Errorf("budget=1s delayed get=4s elapsed=%v", elapsed)
			}
			if code != 15 || !strings.Contains(out, `"wait_status":"not-received"`) {
				t.Fatalf("exit=%d out=%q stderr=%q", code, out, stderr)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(pointer, after) {
				t.Fatalf("pointer=%q err=%v", after, err)
			}
			for _, marker := range []string{"worker.submit-failed", "worker.not-received"} {
				if _, err := os.Stat(filepath.Join(f.sd, "wait", marker)); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestDispatchHcAmendSendDeadline(t *testing.T) {
	f := newDispatchHcFixture(t, append(guardProbeRules(), []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(7, "working")},
		{Argv: []string{"send"}, ArgvPrefix: true, Delay: 4000, Stdout: `{"turn":7,"started":true}`},
	}...))
	old := filepath.Join(f.sd, "reports", "previous.md")
	pointer := []byte(fmt.Sprintf(`{"version":1,"task_report":%q,"current":%q,"history":[],"role":"implementer","pane":"t7"}`, old, old) + "\n")
	path := taskreport.TaskReportPointerPath(f.sd, "worker")
	if err := os.WriteFile(path, pointer, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.sd, "last-report-worker"), []byte(old+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	code, out, stderr := f.run(t, "worker", f.brief, "--amend", "--no-wait")
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("dispatch took %v", elapsed)
	}
	if code != 4 {
		t.Fatalf("exit=%d out=%q stderr=%q", code, out, stderr)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(pointer, after) {
		t.Fatalf("pointer=%q err=%v", after, err)
	}
	if _, err := os.Stat(filepath.Join(f.sd, "wait", "worker.submit-failed")); err != nil {
		t.Fatal(err)
	}
}

// TestDispatchHcNoPreSendScreenWork is the ported NoPreSendScreens probe:
// a pi under hc makes zero screen reads and sends no key before the send
// on the accepted, not-received, blocked, busy and queue paths (the herdr
// path's context footer, the compaction border and the settle reads do not
// run).
func TestDispatchHcNoPreSendScreenWork(t *testing.T) {
	for _, path := range []string{"accepted", "not-received", "blocked", "busy", "queue"} {
		t.Run(path, func(t *testing.T) {
			prefix := []string{"send", "--if", "idle", "--json", "--", "worker"}
			state := "idle"
			if path == "queue" {
				prefix = []string{"send", "--json", "--", "worker"}
				state = "working"
			}
			send := fakecli.Rule{Argv: prefix, ArgvPrefix: true, Stdout: `{"turn":7,"started":true}`}
			wantCode := 0
			switch path {
			case "not-received":
				send.Stdout = `{"turn":7,"started":false}`
				wantCode = 15
			case "blocked":
				send.Code = 9
				send.Stderr = `{"error":{"kind":"blocked","message":"blocked"}}`
				wantCode = 7
			case "busy":
				send.Call = 1
				send.Code = 10
				send.Stderr = `{"error":{"kind":"status_mismatch","message":"busy"}}`
			}
			rules := []fakecli.Rule{
				{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(7, state)},
				{Argv: []string{"read", "worker"}, ArgvPrefix: true, Stdout: `{"text":""}`},
				send,
			}
			if path == "busy" {
				rules = append(rules, fakecli.Rule{Argv: prefix, ArgvPrefix: true, Call: 2, Stdout: `{"turn":7,"started":true}`})
			}
			if path != "queue" {
				// The ordinary guarded send's read-only capability probes,
				// before the broad send rule (first match wins).
				rules = append(guardProbeRules(), rules...)
			}
			f := newDispatchHcFixture(t, rules)
			roster := filepath.Join(f.sd, "agents.tsv")
			raw, err := os.ReadFile(roster)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(roster, []byte(strings.Replace(string(raw), "\tcodex\t", "\tpi\t", 1)), 0o600); err != nil {
				t.Fatal(err)
			}
			// The percentage that makes the herdr path read the context
			// footer (its default) — proof this path had a reason to read
			// the screen before the send.
			f.env["SOHO_CONTEXT_WARN_PERCENT"] = "60"
			args := []string{"worker", f.brief, "--no-wait"}
			if path == "queue" {
				args = append(args, "--queue")
			}
			code, _, stderr := f.run(t, args...)
			if code != wantCode {
				t.Fatalf("exit=%d want=%d stderr=%q", code, wantCode, stderr)
			}
			pre, keys, sends := 0, 0, 0
			for _, c := range f.hcCalls(t) {
				if len(c.Argv) == 0 {
					continue
				}
				switch c.Argv[0] {
				case "send":
					if isRealSend(c.Argv) {
						sends++
					}
				case "read":
					if sends == 0 {
						pre++
					}
				case "keys":
					keys++
				}
			}
			t.Logf("pre-send reads=%d keys=%d sends=%d exit=%d", pre, keys, sends, code)
			if pre != 0 || keys != 0 {
				t.Error("hc dispatch still performed screen/key work before send")
			}
			f.assertHcNeverTouchedHerdr(t)
		})
	}
}

// TestDispatchHcKindRefusedBeforeAnyReadOrSend is the ported
// CursorScreenStillRefusesHc probe under the decided behavior: a kind whose
// status hc does not report (cursor) is refused by kind before anything is
// read or sent — exit 2, the exact generic line, zero gets, zero reads and
// zero sends, even when the hc state says idle. The native primary's kind
// (opencode) takes the specific refusal instead: a live idle roster record
// that reports app opencode but carries no primary launch profile is
// refused before anything is read or sent, after exactly one Get on the
// roster's own terminal id, with the fixed line and zero screen, input,
// key or pointer side effect.
func TestDispatchHcKindRefusedBeforeAnyReadOrSend(t *testing.T) {
	t.Run("cursor", func(t *testing.T) {
		f := newDispatchHcFixture(t, []fakecli.Rule{
			{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(7, "idle")},
			{Argv: []string{"get", "t7", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(7, "idle")},
			{Argv: []string{"read", "worker"}, ArgvPrefix: true, Stdout: `{"text":""}`},
			{Argv: []string{"send"}, ArgvPrefix: true, Stdout: `{"turn":7,"started":true}`},
		})
		roster := filepath.Join(f.sd, "agents.tsv")
		raw, err := os.ReadFile(roster)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(roster, []byte(strings.Replace(string(raw), "\tcodex\t", "\tcursor\t", 1)), 0o600); err != nil {
			t.Fatal(err)
		}
		const want = "soho: dispatch: kind cursor does not report status under transport=hc; use transport=herdr\n"
		code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
		if stderr != want {
			t.Fatalf("exit=%d stderr=%q, want exactly %q", code, stderr, want)
		}
		if code != 2 {
			t.Fatalf("exit=%d want 2 out=%q stderr=%q", code, out, stderr)
		}
		if out != "" {
			t.Fatalf("stdout=%q, want nothing", out)
		}
		gets, reads, sends, keys := 0, 0, 0, 0
		for _, c := range f.hcCalls(t) {
			if len(c.Argv) == 0 {
				continue
			}
			switch c.Argv[0] {
			case "get":
				if len(c.Argv) > 1 && (c.Argv[1] == "worker" || c.Argv[1] == "t7") {
					gets++
				}
			case "read":
				reads++
			case "send":
				sends++
			case "keys":
				keys++
			}
		}
		t.Logf("authoritative hc idle; kind=cursor; gets=%d reads=%d sends=%d keys=%d exit=%d", gets, reads, sends, keys, code)
		if gets != 0 || reads != 0 || sends != 0 || keys != 0 {
			t.Error("the cursor refusal got, read or sent something")
		}
		f.assertHcNeverTouchedHerdr(t)
	})
	t.Run("opencode", func(t *testing.T) {
		// The real observation the gate refuses: live, idle, reported, app
		// opencode and no launch profile label.
		rec := hcOpencodeRecord{address: "t7", nativeState: "live", state: "idle", reported: true, app: "opencode"}
		f := newDispatchHcOpencodeFixture(t, []fakecli.Rule{
			{Argv: []string{"get", "t7", "--json"}, ArgvPrefix: true, Stdout: hcOpencodeGet(rec)},
			{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcOpencodeGet(rec)},
			{Argv: []string{"read", "worker"}, ArgvPrefix: true, Stdout: `{"text":""}`},
			{Argv: []string{"send"}, ArgvPrefix: true, Stdout: `{"turn":7,"started":true}`},
		})
		const want = "soho: dispatch: opencode requires a live reported primary launch profile under transport=hc\n"
		code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
		if stderr != want {
			t.Fatalf("exit=%d stderr=%q, want exactly %q", code, stderr, want)
		}
		if code != 2 {
			t.Fatalf("exit=%d want 2 out=%q stderr=%q", code, out, stderr)
		}
		if out != "" {
			t.Fatalf("stdout=%q, want nothing", out)
		}
		gets, reads, sends, keys := 0, 0, 0, 0
		for _, c := range f.hcCalls(t) {
			if len(c.Argv) == 0 {
				continue
			}
			switch {
			case c.Argv[0] == "get" && len(c.Argv) > 1 && c.Argv[1] == "t7":
				gets++
			case c.Argv[0] == "get":
				if len(c.Argv) > 1 && c.Argv[1] == "worker" {
					t.Fatalf("a pre-send state get before the refusal: %#v", c)
				}
			case c.Argv[0] == "read":
				reads++
			case c.Argv[0] == "send":
				sends++
			case c.Argv[0] == "keys":
				keys++
			}
		}
		t.Logf("authoritative hc live idle without the primary profile; gets=%d reads=%d sends=%d keys=%d exit=%d", gets, reads, sends, keys, code)
		if gets != 1 {
			t.Fatalf("roster-address gets=%d, want exactly one: %#v", gets, f.hcCalls(t))
		}
		if reads != 0 || sends != 0 || keys != 0 {
			t.Error("the opencode refusal read, sent or keyed something")
		}
		if _, err := os.Stat(taskreport.TaskReportPointerPath(f.sd, "worker")); err == nil {
			t.Fatal("the task report pointer was written before the refusal")
		}
		if _, err := os.Stat(filepath.Join(f.sd, "last-report-worker")); err == nil {
			t.Fatal("the report pointer was written before the refusal")
		}
		f.assertHcNeverTouchedHerdr(t)
	})
}

// assertWorkerGetSendOrder pins the probe's operation order when the
// post-send get did run: the preflight and immediate status get precede the
// send and the delayed get is the first post-send call, so the deadline under test is
// measured on the post-send work. Under host load the budget can expire
// before the get even launches — that also proves the deadline property,
// so the order is pinned only when the get happened.
func assertWorkerGetSendOrder(t *testing.T, f *dispatchHcFixture) {
	t.Helper()
	var ops []string
	for _, c := range f.hcCalls(t) {
		if len(c.Argv) == 0 {
			continue
		}
		if isRealSend(c.Argv) {
			ops = append(ops, "send")
		}
		if c.Argv[0] == "get" && len(c.Argv) > 1 && c.Argv[1] == "worker" {
			ops = append(ops, "get")
		}
	}
	if len(ops) < 4 {
		t.Logf("worker get/send order=%v; the deadline passed before the post-send get launched", ops)
		return
	}
	if ops[0] != "get" || ops[1] != "get" || ops[2] != "send" || ops[3] != "get" {
		t.Fatalf("worker get/send order=%v; the first post-send get is not the delayed response", ops)
	}
	t.Logf("worker get/send order=%v; get #3 is the delayed response", ops)
}

// TestDispatchHcBusyRetryRefusedAfterDeadline is the ported BusyDeadline
// probe: a busy send whose idle get only arrives after the dispatch
// timeout makes one send and ends with the submission failure (exit 4) —
// the late result does not authorize the retry.
func TestDispatchHcBusyRetryRefusedAfterDeadline(t *testing.T) {
	prefix := []string{"send", "--if", "idle", "--json", "--", "worker"}
	f := newDispatchHcFixture(t, append(guardProbeRules(), []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, Call: 1, Stdout: hcWorkerGet(7, "idle")},
		{Argv: []string{"get", "worker", "--json"}, Call: 2, Stdout: hcWorkerGet(7, "idle")},
		{Argv: []string{"get", "worker", "--json"}, Call: 3, Delay: 250, Stdout: hcWorkerGet(7, "idle")},
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(7, "idle")},
		{Argv: prefix, ArgvPrefix: true, Call: 1, Code: 10, Stderr: `{"error":{"kind":"status_mismatch","message":"busy"}}`},
		{Argv: prefix, ArgvPrefix: true, Call: 2, Stdout: `{"turn":8,"started":true}`},
		{Argv: []string{"read", "worker"}, ArgvPrefix: true, Stdout: `{"text":""}`},
	}...))
	f.env["SOHO_PROMPT_SETTLE_SECONDS"] = "0"
	start := time.Now()
	code, _, _ := f.run(t, "worker", f.brief, "--no-wait", "--timeout", "20ms")
	sends := len(hcSends(f.hcCalls(t)))
	t.Logf("dispatch timeout=20ms; delayed idle get=250ms; elapsed=%v sends=%d exit=%d", time.Since(start).Round(time.Millisecond), sends, code)
	assertWorkerGetSendOrder(t, f)
	if sends != 1 || code != 4 {
		t.Error("busy path retried after its dispatch deadline instead of failing submission")
	}
	f.assertHcNeverTouchedHerdr(t)
}

// TestDispatchHcArrivalProofRefusedAfterWindow is the ported
// ArrivalDeadline probe: a started:false send whose first get only arrives
// after the arrival window ends is not-received (exit 15) — evidence read
// outside the window proves nothing, and no poll starts at or after
// expiry.
func TestDispatchHcArrivalProofRefusedAfterWindow(t *testing.T) {
	// The probe rules precede the broad send rule: first match wins, so a
	// send --help after it would take the send's answer instead of the help.
	f := newDispatchHcFixture(t, append(guardProbeRules(), []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, Call: 1, Stdout: hcWorkerGet(7, "idle")},
		{Argv: []string{"get", "worker", "--json"}, Call: 2, Stdout: hcWorkerGet(7, "idle")},
		{Argv: []string{"get", "worker", "--json"}, Call: 3, Delay: 1300, Stdout: hcWorkerGet(8, "idle")},
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(8, "idle")},
		{Argv: []string{"send"}, ArgvPrefix: true, Stdout: `{"turn":7,"started":false}`},
		{Argv: []string{"read", "worker"}, ArgvPrefix: true, Stdout: `{"text":""}`},
	}...))
	f.env["SOHO_PROMPT_SETTLE_SECONDS"] = "0"
	start := time.Now()
	code, _, _ := f.run(t, "worker", f.brief, "--no-wait")
	t.Logf("arrival window=1s; delayed proof get=1300ms; elapsed=%v exit=%d", time.Since(start).Round(time.Millisecond), code)
	assertWorkerGetSendOrder(t, f)
	if code != 15 {
		t.Error("arrival accepted a proof that arrived outside its window")
	}
	f.assertHcNeverTouchedHerdr(t)
}

// TestDispatchHcUint64TurnRiseArrives is the ported Uint64TurnRise probe:
// the schema's turn is a uint64 — a valid turn at or above 2^63 (the
// signed ceiling) followed by one above it is arrival proof, without
// narrowing or a sentinel.
func TestDispatchHcUint64TurnRiseArrives(t *testing.T) {
	f := newDispatchHcFixture(t, append(guardProbeRules(), []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: `{"address":"t7","name":"worker","state":"live","turn":9223372036854775809,"reported":true,"status":{"state":"idle"}}`},
		{Argv: []string{"send"}, ArgvPrefix: true, Stdout: `{"turn":9223372036854775808,"started":false}`},
		{Argv: []string{"read", "worker"}, ArgvPrefix: true, Stdout: `{"text":""}`},
	}...))
	// The window is generous so the acceptance does not depend on the fake
	// process's start time under host load; the test ends as soon as the
	// turn rise is observed.
	f.env["SOHO_PROMPT_CHECK_SECONDS"] = "30"
	code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
	t.Logf("valid uint64 turn rose from 9223372036854775808 to 9223372036854775809; exit=%d", code)
	if code != 0 {
		t.Errorf("valid uint64 turn rise was not accepted (exit=%d out=%s stderr=%s)", code, out, stderr)
	}
}

// TestDispatchHcBlockedPreservesPriorPointers is the ported
// BlockedPreservesPriorPointers probe: a blocked send restores the prior
// task pointer and the last-report bytes byte for byte (both the plain and
// the queue form) and never sends force.
func TestDispatchHcBlockedPreservesPriorPointers(t *testing.T) {
	for _, queue := range []bool{false, true} {
		t.Run(fmt.Sprintf("queue=%v", queue), func(t *testing.T) {
			// The guarded subtest proves the capability through the read-only
			// probes; the queue subtest never sends a guarded send, so the
			// probe rules simply stay unanswered.
			f := newDispatchHcFixture(t, append(guardProbeRules(), []fakecli.Rule{
				{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(7, "idle")},
				{Argv: []string{"send"}, ArgvPrefix: true, Code: 9, Stderr: `{"error":{"kind":"blocked","message":"blocked"}}`},
			}...))
			oldReport := filepath.Join(f.sd, "reports", "previous.md")
			pointer := []byte(fmt.Sprintf(`{"version":1,"task_report":%q,"current":%q,"history":[],"role":"implementer","pane":"t7"}`, oldReport, oldReport) + "\n")
			pointerPath := taskreport.TaskReportPointerPath(f.sd, "worker")
			if err := os.WriteFile(pointerPath, pointer, 0o600); err != nil {
				t.Fatal(err)
			}
			lastPath := filepath.Join(f.sd, "last-report-worker")
			last := []byte(oldReport + "\r\n")
			if err := os.WriteFile(lastPath, last, 0o600); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(pointerPath)
			if err != nil {
				t.Fatal(err)
			}
			args := []string{"worker", f.brief, "--no-wait"}
			if queue {
				args = append(args, "--queue")
			}
			code, out, stderr := f.run(t, args...)
			after, err := os.ReadFile(pointerPath)
			if err != nil {
				t.Fatal(err)
			}
			gotLast, err := os.ReadFile(lastPath)
			if err != nil {
				t.Fatal(err)
			}
			if code != 7 || !strings.Contains(out, `"cause":"hc-blocked"`) {
				t.Fatalf("exit=%d out=%q stderr=%q", code, out, stderr)
			}
			if !bytes.Equal(before, after) || !bytes.Equal(last, gotLast) {
				t.Fatalf("prior pointers were not restored byte for byte: before=%q after=%q last=%q", before, after, gotLast)
			}
			t.Log("blocked exit=7; prior task pointer and CRLF last-report bytes restored")
			for _, c := range f.hcCalls(t) {
				for _, a := range c.Argv {
					if a == "--force" {
						t.Error("force sent")
					}
				}
			}
			f.assertHcNeverTouchedHerdr(t)
		})
	}
}

// TestDispatchHcMalformedSendProvesNothing is the ported
// MalformedSendProvesNothing probe: a SendResult whose started is a string
// is unreadable — it proves nothing and is not resended: exactly one send,
// not-received.
func TestDispatchHcMalformedSendProvesNothing(t *testing.T) {
	f := newDispatchHcFixture(t, append(guardProbeRules(), []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(7, "idle")},
		{Argv: []string{"send"}, ArgvPrefix: true, Stdout: `{"turn":7,"started":"true"}`},
		{Argv: []string{"read", "worker"}, ArgvPrefix: true, Stdout: `{"text":""}`},
	}...))
	code, _, _ := f.run(t, "worker", f.brief, "--no-wait")
	sends := len(hcSends(f.hcCalls(t)))
	t.Logf("malformed started string; idle unchanged; sends=%d exit=%d", sends, code)
	if sends != 1 || code != 15 {
		t.Error("unreadable SendResult counted as proof or caused resend")
	}
}

// TestDispatchHcBusyRetryIsFinal is the ported RetryBusyIsFinal control:
// two busy replies produce exactly two sends (the ceiling) and the
// submission failure with the hc inspection hint — the retry was not
// unbounded and the deadline is not the defect.
func TestDispatchHcBusyRetryIsFinal(t *testing.T) {
	f := newDispatchHcFixture(t, append(guardProbeRules(), []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(7, "idle")},
		{Argv: []string{"send"}, ArgvPrefix: true, Code: 10, Stderr: `{"error":{"kind":"status_mismatch","message":"busy"}}`},
	}...))
	// 30s is generous: the retry happens within the budget right away; the
	// point is the ceiling, not the deadline (the deadline variant is
	// TestDispatchHcBusyRetryRefusedAfterDeadline).
	code, _, stderr := f.run(t, "worker", f.brief, "--no-wait", "--timeout", "30s")
	sends := len(hcSends(f.hcCalls(t)))
	t.Logf("both sends busy: sends=%d exit=%d", sends, code)
	if sends != 2 || code != 4 || !strings.Contains(stderr, "hc get worker && hc read worker") {
		t.Errorf("retry not final or hint wrong: %q", stderr)
	}
	f.assertHcNeverTouchedHerdr(t)
}

// TestDispatchHcFixtureAppliesChildRaceOptions verifies that newDispatchHcFixture
// propagates inherited race detector options to fixture children with
// atexit_sleep_ms=0.
func TestDispatchHcFixtureAppliesChildRaceOptions(t *testing.T) {
	t.Setenv("GORACE", "halt_on_error=1 atexit_sleep_ms=700")
	f := newDispatchHcFixture(t, nil)
	target, err := fakecli.InstallWithOptions(t, f.bin, "race-env-probe", []fakecli.Rule{{AnyArgs: true}}, fakecli.InstallOptions{
		CaptureEnv: []string{"GORACE"},
	})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(target, "probe")
	cmd.Env = f.env.List()
	out, err := cmd.CombinedOutput()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("exec probe child: %v: %s", err, out)
		}
	}
	if exitCode != 0 {
		t.Fatalf("probe exit code = %d, want 0 (output=%s)", exitCode, out)
	}
	calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "race-env-probe.calls.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("observed probe calls = %d, want 1: %#v", len(calls), calls)
	}
	wantOption := "halt_on_error=1 atexit_sleep_ms=0"
	if got := calls[0].Env["GORACE"]; got != wantOption {
		t.Fatalf("received GORACE = %q, want %q", got, wantOption)
	}
}
