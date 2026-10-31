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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/taskreport"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

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
					sends++
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
// status hc does not report (cursor, opencode) is refused by kind before
// anything is read or sent — exit 2, the exact line, zero reads and zero
// sends, even when the hc state says idle.
func TestDispatchHcKindRefusedBeforeAnyReadOrSend(t *testing.T) {
	for _, kind := range []string{"cursor", "opencode"} {
		t.Run(kind, func(t *testing.T) {
			f := newDispatchHcFixture(t, []fakecli.Rule{
				{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(7, "idle")},
				{Argv: []string{"read", "worker"}, ArgvPrefix: true, Stdout: `{"text":""}`},
				{Argv: []string{"send"}, ArgvPrefix: true, Stdout: `{"turn":7,"started":true}`},
			})
			roster := filepath.Join(f.sd, "agents.tsv")
			raw, err := os.ReadFile(roster)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(roster, []byte(strings.Replace(string(raw), "\tcodex\t", "\t"+kind+"\t", 1)), 0o600); err != nil {
				t.Fatal(err)
			}
			code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
			if want := fmt.Sprintf("soho: dispatch: kind %s does not report status under transport=hc; use transport=herdr\n", kind); stderr != want {
				t.Fatalf("exit=%d stderr=%q, want exactly %q", code, stderr, want)
			}
			if code != 2 {
				t.Fatalf("exit=%d want 2 out=%q stderr=%q", code, out, stderr)
			}
			reads, sends, keys := 0, 0, 0
			for _, c := range f.hcCalls(t) {
				if len(c.Argv) == 0 {
					continue
				}
				switch c.Argv[0] {
				case "read":
					reads++
				case "send":
					sends++
				case "keys":
					keys++
				}
			}
			t.Logf("authoritative hc idle; kind=%s; reads=%d sends=%d keys=%d exit=%d", kind, reads, sends, keys, code)
			if reads != 0 || sends != 0 || keys != 0 {
				t.Error("the kind refusal read or sent something")
			}
			f.assertHcNeverTouchedHerdr(t)
		})
	}
}

// assertWorkerGetSendOrder pins the probe's operation order when the
// post-send get did run: the first worker get precedes the send and the
// delayed get is the first post-send call, so the deadline under test is
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
		if c.Argv[0] == "send" {
			ops = append(ops, "send")
		}
		if c.Argv[0] == "get" && len(c.Argv) > 1 && c.Argv[1] == "worker" {
			ops = append(ops, "get")
		}
	}
	if len(ops) < 3 {
		t.Logf("worker get/send order=%v; the deadline passed before the post-send get launched", ops)
		return
	}
	if ops[0] != "get" || ops[1] != "send" || ops[2] != "get" {
		t.Fatalf("worker get/send order=%v; the first post-send get is not the delayed response", ops)
	}
	t.Logf("worker get/send order=%v; get #2 is the delayed response", ops)
}

// TestDispatchHcBusyRetryRefusedAfterDeadline is the ported BusyDeadline
// probe: a busy send whose idle get only arrives after the dispatch
// timeout makes one send and ends with the submission failure (exit 4) —
// the late result does not authorize the retry.
func TestDispatchHcBusyRetryRefusedAfterDeadline(t *testing.T) {
	prefix := []string{"send", "--if", "idle", "--json", "--", "worker"}
	f := newDispatchHcFixture(t, []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, Call: 1, Stdout: hcWorkerGet(7, "idle")},
		{Argv: []string{"get", "worker", "--json"}, Call: 2, Delay: 250, Stdout: hcWorkerGet(7, "idle")},
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(7, "idle")},
		{Argv: prefix, ArgvPrefix: true, Call: 1, Code: 10, Stderr: `{"error":{"kind":"status_mismatch","message":"busy"}}`},
		{Argv: prefix, ArgvPrefix: true, Call: 2, Stdout: `{"turn":8,"started":true}`},
		{Argv: []string{"read", "worker"}, ArgvPrefix: true, Stdout: `{"text":""}`},
	})
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
	f := newDispatchHcFixture(t, []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, Call: 1, Stdout: hcWorkerGet(7, "idle")},
		{Argv: []string{"get", "worker", "--json"}, Call: 2, Delay: 1300, Stdout: hcWorkerGet(8, "idle")},
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(8, "idle")},
		{Argv: []string{"send"}, ArgvPrefix: true, Stdout: `{"turn":7,"started":false}`},
		{Argv: []string{"read", "worker"}, ArgvPrefix: true, Stdout: `{"text":""}`},
	})
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
	f := newDispatchHcFixture(t, []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: `{"address":"t7","name":"worker","state":"live","turn":9223372036854775809,"reported":true,"status":{"state":"idle"}}`},
		{Argv: []string{"send"}, ArgvPrefix: true, Stdout: `{"turn":9223372036854775808,"started":false}`},
		{Argv: []string{"read", "worker"}, ArgvPrefix: true, Stdout: `{"text":""}`},
	})
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
			f := newDispatchHcFixture(t, []fakecli.Rule{
				{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(7, "idle")},
				{Argv: []string{"send"}, ArgvPrefix: true, Code: 9, Stderr: `{"error":{"kind":"blocked","message":"blocked"}}`},
			})
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
	f := newDispatchHcFixture(t, []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(7, "idle")},
		{Argv: []string{"send"}, ArgvPrefix: true, Stdout: `{"turn":7,"started":"true"}`},
		{Argv: []string{"read", "worker"}, ArgvPrefix: true, Stdout: `{"text":""}`},
	})
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
	f := newDispatchHcFixture(t, []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(7, "idle")},
		{Argv: []string{"send"}, ArgvPrefix: true, Code: 10, Stderr: `{"error":{"kind":"status_mismatch","message":"busy"}}`},
	})
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
