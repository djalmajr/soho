package cli

// SOH-409: the first dispatch to a fresh codex worker over hc. hc refuses a
// guarded send whose --if status does not match (exit 10) before any byte is
// typed; the existing busy branch already sends again once after the worker
// reads as settled. When that second send is refused the same way, a first
// dispatch (no task recorded for the worker) waits up to 10 s for an idle
// worker with a readable empty composer, checks that the composed prompt path
// is on no recent screen, and sends the same prompt once more.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/taskreport"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	"github.com/djalmajr/soho/internal/transport"
)

// pinnedFixtureStamp is the composed stamp pinComposedStamp holds for the
// hc fixture (a run that stays inside that second names its files with it).
const pinnedFixtureStamp = "20260929T000000"

const (
	firstResendEmptyScreen = "OpenAI Codex\n\n› \n\n  gpt-5 high · Context 0% used\n"
	firstResendDraftScreen = "OpenAI Codex\n\n› fix the failing test\n\n  gpt-5 high · Context 0% used\n"
	firstResendMismatch    = `{"error":{"kind":"status_mismatch","message":"the terminal is not ready"}}`
	firstResendUncertain   = `{"error":{"kind":"unavailable","message":"core went away"}}`
	firstResendStarting    = `{"error":{"kind":"unavailable","message":"worker is still starting: its output has not settled","hint":"nothing was typed; check hc read worker, then retry"}}`
	firstResendBusyWarning = "soho: warning: prompt submission failed (busy: worker worker is not idle; cause: hc exited 10: status_mismatch: the terminal is not ready); inspect with: hc get worker && hc read worker. Do not resend blindly."
	firstResendFriction    = "soho: warning: the first prompt to 'worker' was refused before it was written; sent it once more"
)

func firstResendReadRule(lines string, screen string) fakecli.Rule {
	raw, _ := json.Marshal(map[string]any{"text": screen, "complete": true})
	return fakecli.Rule{Argv: []string{"read", "worker", "--lines", lines}, ArgvPrefix: true, Stdout: string(raw)}
}

func firstResendSendRule(call int, code int, stderr string) fakecli.Rule {
	if code == 0 {
		return fakecli.Rule{Argv: []string{"send", "--if", "idle", "--json", "--", "worker"}, ArgvPrefix: true, Call: call, Stdout: `{"turn":8,"started":true}`}
	}
	return fakecli.Rule{Argv: []string{"send", "--if", "idle", "--json", "--", "worker"}, ArgvPrefix: true, Call: call, Code: code, Stderr: stderr}
}

// firstResendPauses replaces the one-second poll pause and counts it.
func firstResendPauses(t *testing.T) *int {
	t.Helper()
	old := dispatchFirstResendPause
	pauses := 0
	dispatchFirstResendPause = func() { pauses++ }
	t.Cleanup(func() { dispatchFirstResendPause = old })
	return &pauses
}

func (f *dispatchHcFixture) composedPathFor(stamp string) string {
	return filepath.Join(f.sd, "briefs", "worker-"+stamp+".md")
}

func (f *dispatchHcFixture) setKind(t *testing.T, kind string) {
	t.Helper()
	path := filepath.Join(f.sd, "agents.tsv")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(raw), "\tcodex\t", "\t"+kind+"\t", 1)
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func dispatchHcJSON(t *testing.T, out string) *jsonjs.Object {
	t.Helper()
	value, err := jsonjs.Parse([]byte(strings.TrimSpace(out)))
	if err != nil {
		t.Fatalf("dispatch JSON %q: %v", out, err)
	}
	obj, ok := value.(*jsonjs.Object)
	if !ok {
		t.Fatalf("dispatch JSON %q is not an object", out)
	}
	return obj
}

func firstResendBaseRules(extra ...fakecli.Rule) []fakecli.Rule {
	rules := append([]fakecli.Rule{}, extra...)
	rules = append(rules, guardProbeRules()...)
	rules = append(rules,
		fakecli.Rule{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(7, "idle")},
	)
	return rules
}

// assertSameTarget fails unless every real send went to the worker the first
// one addressed, with the same text.
func assertSameTarget(t *testing.T, sends []fakecli.Call) {
	t.Helper()
	for i, s := range sends {
		if len(s.Argv) != 7 || s.Argv[5] != "worker" || s.Argv[6] != sends[0].Argv[6] {
			t.Fatalf("send %d argv=%#v, want the first send's target and text", i, s.Argv)
		}
	}
}

func TestDispatchFirstResendStarting(t *testing.T) {
	const repeated = `{"error":{"kind":"unavailable","message":"t7 is still starting: its output has not settled","hint":"nothing was typed; check hc read t7, then retry"}}`
	for _, tc := range []struct {
		name   string
		first  string
		second string
		sends  int
		raw    string
	}{
		{name: "accepted", first: firstResendStarting, sends: 2},
		{name: "repeated", first: firstResendStarting, second: repeated, sends: 2, raw: "hc exited 5: unavailable: t7 is still starting: its output has not settled (nothing was typed; check hc read t7, then retry)"},
		{name: "no proof", first: firstResendUncertain, sends: 1, raw: "hc exited 5: unavailable: core went away"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			firstResendPauses(t)
			second := firstResendSendRule(2, 0, "")
			if tc.second != "" {
				second = firstResendSendRule(2, 5, tc.second)
			}
			f := newDispatchHcFixture(t, firstResendBaseRules(
				firstResendSendRule(1, 5, tc.first), second, firstResendSendRule(3, 0, ""),
				firstResendReadRule("0", firstResendEmptyScreen), firstResendReadRule("40", ""),
			))
			code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
			calls := f.hcCalls(t)
			sends := hcSends(calls)
			if len(sends) != tc.sends {
				t.Fatalf("sends=%d, want %d (code=%d out=%s stderr=%q)", len(sends), tc.sends, code, out, errText)
			}
			assertSameTarget(t, sends)
			var deliveryCalls []string
			for _, call := range calls {
				if len(call.Argv) >= 2 && call.Argv[0] == "send" && call.Argv[1] == "--if" {
					deliveryCalls = append(deliveryCalls, "send")
				}
				if len(call.Argv) >= 4 && call.Argv[0] == "read" && call.Argv[1] == "worker" && call.Argv[2] == "--lines" {
					deliveryCalls = append(deliveryCalls, "read "+call.Argv[3])
				}
			}
			wantCalls := "send"
			if tc.sends == 2 {
				wantCalls = "send, read 0, read 40, send"
			}
			// Accepted dispatches can make additional reads to prove arrival.
			if tc.raw == "" && len(deliveryCalls) > 4 {
				deliveryCalls = deliveryCalls[:4]
			}
			if got := strings.Join(deliveryCalls, ", "); got != wantCalls {
				t.Fatalf("delivery calls=%q, want %q", got, wantCalls)
			}
			f.assertHcNeverTouchedHerdr(t)

			obj := dispatchHcJSON(t, out)
			sidecars, err := filepath.Glob(filepath.Join(f.sd, "briefs", "*.dispatch.json"))
			if err != nil || len(sidecars) != 1 {
				t.Fatalf("sidecars=%q err=%v, want one attempt sidecar", sidecars, err)
			}
			sidecar, err := os.ReadFile(sidecars[0])
			if err != nil {
				t.Fatal(err)
			}
			pointerPath := taskreport.TaskReportPointerPath(f.sd, "worker")
			failedMarker := filepath.Join(f.sd, "wait", "worker.submit-failed")
			if tc.raw != "" {
				assertTodaysError(t, f, code, out, errText, tc.sends, tc.raw)
				if got, _ := obj.Get("raw"); got != tc.raw {
					t.Fatalf("raw=%q, want the last refused send's cause %q", got, tc.raw)
				}
				if _, err := os.Stat(pointerPath); !os.IsNotExist(err) {
					t.Fatalf("failed dispatch must restore the absent task pointer: %v", err)
				}
				if _, err := os.Stat(failedMarker); err != nil {
					t.Fatalf("failed dispatch must record the submit-failed marker: %v", err)
				}
				if submission, _ := dispatchHcJSON(t, string(sidecar)).Get("submission"); submission != "failed" {
					t.Fatalf("sidecar submission=%v, want failed", submission)
				}
				return
			}
			status, _ := obj.Get("wait_status")
			resent, _ := obj.Get("resent_first")
			if code != 0 || status != "submitted" || resent != true {
				t.Fatalf("code=%d out=%s stderr=%q, want submitted and resent_first", code, out, errText)
			}
			if strings.Count(errText, firstResendFriction) != 1 || strings.Contains(errText, "prompt submission failed") {
				t.Fatalf("stderr=%q, want one resend warning", errText)
			}
			if _, err := os.Stat(failedMarker); !os.IsNotExist(err) {
				t.Fatalf("accepted dispatch must leave no submit-failed marker: %v", err)
			}
			pointers, err := filepath.Glob(filepath.Join(f.sd, "task-report-*.json"))
			if err != nil || len(pointers) != 1 || pointers[0] != pointerPath {
				t.Fatalf("task pointers=%q err=%v, want only %q", pointers, err, pointerPath)
			}
			pointer := taskreport.ReadTaskReportPointer(f.sd, "worker")
			if pointer == nil {
				t.Fatal("accepted dispatch must keep a valid task pointer")
			}
			report, _ := obj.Get("report")
			taskReport, _ := obj.Get("task_report")
			current, _ := pointer.Get("current")
			stable, _ := pointer.Get("task_report")
			if report != current || taskReport != stable {
				t.Fatalf("pointer current=%v task_report=%v, want %v and %v", current, stable, report, taskReport)
			}
			sidecarObj := dispatchHcJSON(t, string(sidecar))
			submission, _ := sidecarObj.Get("submission")
			sidecarReport, _ := sidecarObj.Get("task_report")
			if submission != "accepted" || sidecarReport != stable {
				t.Fatalf("sidecar submission=%v task_report=%v, want accepted and %v", submission, sidecarReport, stable)
			}
		})
	}
}

func TestDispatchFirstResendStartingProof(t *testing.T) {
	const startup = "worker is still starting: its output has not settled"
	const statement = "nothing was typed"
	const advice = statement + "; check hc read worker, then retry"
	for _, tc := range []struct {
		name, message string
		hint          any
		wantResend    bool
	}{
		{"canonical_hint", startup, advice, true},
		{"canonical_inline", startup + " (" + advice + ")", "", true},
		{"bare_statement", startup, statement, true},
		{"resolved_address", "t7 is still starting: its output has not settled", statement + "; check hc read t7, then retry", true},
		{"resolved_address_in_hint", startup, statement + "; check hc read t7, then retry", true},
		{"consistent_proofs", startup + " (" + advice + ")", statement, true},
		{"trimmed_message", " \t" + startup + "\n", statement, true},
		{"other_failure", "core went away", statement, false},
		{"missing_proof", startup, "", false},
		{"quoted_message", "reported earlier: \"" + startup + "\"", statement, false},
		{"quoted_hint", startup, "\"" + statement + "\"", false},
		{"embedded_reason", "a prior report said " + startup, statement, false},
		{"missing_subject", " is still starting: its output has not settled", statement, false},
		{"embedded_inline_statement", startup + " (" + statement + "; old advice) (receipt unknown)", "", false},
		{"conflicting_hint", startup + " (" + statement + ")", "input may have been delivered", false},
		{"numeric_hint", startup + " (" + statement + ")", 5, false},
		{"array_hint", startup + " (" + statement + ")", []string{statement}, false},
		{"wrong_target_in_reason", "another-worker is still starting: its output has not settled", statement, false},
		{"compound_hint", startup, advice + "; input may have been delivered", false},
		{"compound_inline", startup + " (" + statement + "; input may have been delivered)", "", false},
		{"wrong_target_in_hint", startup, statement + "; check hc read t8, then retry", false},
		{"suffixed_reason", startup + ". A previous send may have written", statement, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			firstResendPauses(t)
			stderr, err := json.Marshal(map[string]any{"error": map[string]any{
				"kind": "unavailable", "message": tc.message, "hint": tc.hint,
			}})
			if err != nil {
				t.Fatal(err)
			}
			f := newDispatchHcFixture(t, firstResendBaseRules(
				firstResendSendRule(1, 5, string(stderr)), firstResendSendRule(2, 0, ""),
				firstResendReadRule("0", firstResendEmptyScreen), firstResendReadRule("40", ""),
			))
			code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
			obj := dispatchHcJSON(t, out)
			status, _ := obj.Get("wait_status")
			sends := hcSends(f.hcCalls(t))
			wantCode, wantStatus, wantSends := 4, "error", 1
			if tc.wantResend {
				wantCode, wantStatus, wantSends = 0, "submitted", 2
			}
			if code != wantCode || status != wantStatus || len(sends) != wantSends {
				t.Fatalf("sends=%d code=%d status=%s, want %d/%d/%s", len(sends), code, status, wantSends, wantCode, wantStatus)
			}
			if tc.wantResend {
				assertSameTarget(t, sends)
				if v, _ := obj.Get("resent_first"); v != true {
					t.Fatalf("resent_first=%v, want true", v)
				}
			} else {
				assertTodaysError(t, f, code, out, errText, 1, tc.message)
			}
		})
	}
}

// TestDispatchFirstResendAfterTwoRefusals: both sends of the busy branch are
// refused (nothing written), the worker is idle with an empty composer: one
// more send, accepted, resent_first true and the friction line.
func TestDispatchFirstResendAfterTwoRefusals(t *testing.T) {
	pauses := firstResendPauses(t)
	f := newDispatchHcFixture(t, firstResendBaseRules(
		firstResendSendRule(1, 10, firstResendMismatch),
		firstResendSendRule(2, 10, firstResendMismatch),
		firstResendSendRule(3, 0, ""),
		firstResendReadRule("0", firstResendEmptyScreen),
		firstResendReadRule("40", ""),
	))
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	if code != 0 {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	obj := dispatchHcJSON(t, out)
	if status, _ := obj.Get("wait_status"); status != "submitted" {
		t.Fatalf("status=%v, want submitted (out=%s)", status, out)
	}
	if v, _ := obj.Get("resent_first"); v != true {
		t.Fatalf("resent_first=%v, want true (out=%s)", v, out)
	}
	if !strings.Contains(errText, firstResendFriction+"\n") || strings.Count(errText, "sent it once more") != 1 {
		t.Fatalf("stderr=%q, want the friction line once", errText)
	}
	if strings.Contains(errText, "prompt submission failed") {
		t.Fatalf("stderr=%q, an accepted dispatch carries no failure warning", errText)
	}
	sends := hcSends(f.hcCalls(t))
	if len(sends) != 3 {
		t.Fatalf("sends=%d, want three (the busy branch's two and the resend): %#v", len(sends), sends)
	}
	assertSameTarget(t, sends)
	if *pauses != 0 {
		t.Fatalf("pauses=%d, a ready worker is not waited for", *pauses)
	}
	sidecar, err := os.ReadFile(filepath.Join(f.sd, "briefs", "worker-"+composedStampOf(f, t)+".dispatch.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sidecar), `"submission":"accepted"`) {
		t.Fatalf("sidecar=%s, want submission accepted", sidecar)
	}
	pointer := taskreport.ReadTaskReportPointer(f.sd, "worker")
	if pointer == nil {
		t.Fatalf("the accepted resend keeps the task pointer")
	}
	if _, err := os.Stat(filepath.Join(f.sd, "wait", "worker.submit-failed")); err == nil {
		t.Fatalf("an accepted resend leaves no submit-failed marker")
	}
	f.assertHcNeverTouchedHerdr(t)
}

// TestDispatchFirstResendNotNeededAfterOneRefusal: the busy branch's own
// retry is accepted: two sends, no resent_first, no friction line.
func TestDispatchFirstResendNotNeededAfterOneRefusal(t *testing.T) {
	firstResendPauses(t)
	f := newDispatchHcFixture(t, firstResendBaseRules(
		firstResendSendRule(1, 10, firstResendMismatch),
		firstResendSendRule(2, 0, ""),
		firstResendReadRule("0", firstResendEmptyScreen),
		firstResendReadRule("40", ""),
	))
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	if code != 0 {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if v, ok := dispatchHcJSON(t, out).Get("resent_first"); ok {
		t.Fatalf("resent_first=%v present on the busy branch's own retry (out=%s)", v, out)
	}
	if strings.Contains(errText, "sent it once more") {
		t.Fatalf("stderr=%q carries the friction line", errText)
	}
	if sends := hcSends(f.hcCalls(t)); len(sends) != 2 {
		t.Fatalf("sends=%d, want two", len(sends))
	}
}

// TestDispatchFirstResendSecondRefusalIsTodaysError: the resend is refused
// too — today's error result, no friction line, the failed sidecar and the
// restored (absent) pointer.
func TestDispatchFirstResendSecondRefusalIsTodaysError(t *testing.T) {
	firstResendPauses(t)
	f := newDispatchHcFixture(t, firstResendBaseRules(
		firstResendSendRule(1, 10, firstResendMismatch),
		firstResendSendRule(2, 10, firstResendMismatch),
		firstResendSendRule(3, 10, firstResendMismatch),
		firstResendReadRule("0", firstResendEmptyScreen),
		firstResendReadRule("40", ""),
	))
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	if code != 4 {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	obj := dispatchHcJSON(t, out)
	if status, _ := obj.Get("wait_status"); status != "error" {
		t.Fatalf("status=%v, want error (out=%s)", status, out)
	}
	if raw, _ := obj.Get("raw"); raw != "worker worker is not idle" {
		t.Fatalf("raw=%v, want the refusal message", raw)
	}
	if _, ok := obj.Get("resent_first"); ok {
		t.Fatalf("a refused resend carries no resent_first (out=%s)", out)
	}
	if !strings.Contains(errText, firstResendBusyWarning) || strings.Contains(errText, "sent it once more") {
		t.Fatalf("stderr=%q, want only today's warning", errText)
	}
	if sends := hcSends(f.hcCalls(t)); len(sends) != 3 {
		t.Fatalf("sends=%d, want exactly three (no second resend)", len(sends))
	}
	if taskreport.ReadTaskReportPointer(f.sd, "worker") != nil {
		t.Fatalf("the failed dispatch restores the absent pointer")
	}
	if _, err := os.Stat(filepath.Join(f.sd, "wait", "worker.submit-failed")); err != nil {
		t.Fatalf("the failed dispatch records the submit-failed marker: %v", err)
	}
	sidecar, err := os.ReadFile(filepath.Join(f.sd, "briefs", "worker-"+composedStampOf(f, t)+".dispatch.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sidecar), `"submission":"failed"`) {
		t.Fatalf("sidecar=%s, want submission failed", sidecar)
	}
}

// TestDispatchFirstResendKeepsTodaysResult is the no-resend table: every row
// ends with today's error result (exit 4, the error JSON, the inspection
// warning) and exactly the sends today's code makes.
func TestDispatchFirstResendKeepsTodaysResult(t *testing.T) {
	two := []fakecli.Rule{firstResendSendRule(1, 10, firstResendMismatch), firstResendSendRule(2, 10, firstResendMismatch)}
	for _, tc := range []struct {
		name   string
		rules  []fakecli.Rule
		kind   string
		before func(t *testing.T, f *dispatchHcFixture)
		sends  int
		raw    string
		// waited is the number of one-second pauses the readiness window
		// is expected to use before giving up.
		waited int
	}{
		{
			name:  "an uncertain error on the first send",
			rules: []fakecli.Rule{firstResendSendRule(1, 1, firstResendUncertain), firstResendReadRule("0", firstResendEmptyScreen), firstResendReadRule("40", "")},
			sends: 1,
			raw:   "core went away",
		},
		{
			name:  "an uncertain error on the busy branch's retry",
			rules: []fakecli.Rule{firstResendSendRule(1, 10, firstResendMismatch), firstResendSendRule(2, 1, firstResendUncertain), firstResendReadRule("0", firstResendEmptyScreen), firstResendReadRule("40", "")},
			sends: 2,
			raw:   "core went away",
		},
		{
			name:  "a claude worker",
			kind:  "claude",
			rules: append(append([]fakecli.Rule{}, two...), firstResendReadRule("0", firstResendEmptyScreen), firstResendReadRule("40", ""), firstResendSendRule(3, 0, "")),
			sends: 2,
			raw:   "worker worker is not idle",
		},
		{
			name:  "a second dispatch (task pointer recorded)",
			rules: append(append([]fakecli.Rule{}, two...), firstResendReadRule("0", firstResendEmptyScreen), firstResendReadRule("40", ""), firstResendSendRule(3, 0, "")),
			before: func(t *testing.T, f *dispatchHcFixture) {
				prior := jsonjs.O("version", 1, "task_report", filepath.Join(f.sd, "reports", "worker-old.current.md"), "current", filepath.Join(f.sd, "reports", "worker-old.md"), "history", []any{}, "role", "implementer")
				if err := taskreport.WriteTaskReportPointer(f.sd, "worker", prior); err != nil {
					t.Fatal(err)
				}
			},
			sends: 2,
			raw:   "worker worker is not idle",
		},
		{
			name:  "a second dispatch (last report recorded)",
			rules: append(append([]fakecli.Rule{}, two...), firstResendReadRule("0", firstResendEmptyScreen), firstResendReadRule("40", ""), firstResendSendRule(3, 0, "")),
			before: func(t *testing.T, f *dispatchHcFixture) {
				if err := os.WriteFile(filepath.Join(f.sd, "last-report-worker"), []byte(filepath.Join(f.sd, "reports", "worker-old.md")+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			sends: 2,
			raw:   "worker worker is not idle",
		},
		{
			name:   "a composer that is not empty",
			rules:  append(append([]fakecli.Rule{}, two...), firstResendReadRule("0", firstResendDraftScreen), firstResendReadRule("40", ""), firstResendSendRule(3, 0, "")),
			sends:  2,
			raw:    "worker worker is not idle",
			waited: dispatchFirstResendPolls,
		},
		{
			name:   "a screen that is not a recognized composer",
			rules:  append(append([]fakecli.Rule{}, two...), firstResendReadRule("0", "starting...\n"), firstResendReadRule("40", ""), firstResendSendRule(3, 0, "")),
			sends:  2,
			raw:    "worker worker is not idle",
			waited: dispatchFirstResendPolls,
		},
		{
			name:   "a screen read that fails",
			rules:  append(append([]fakecli.Rule{}, two...), fakecli.Rule{Argv: []string{"read", "worker"}, ArgvPrefix: true, Code: 1, Stderr: firstResendUncertain}, firstResendSendRule(3, 0, "")),
			sends:  2,
			raw:    "worker worker is not idle",
			waited: dispatchFirstResendPolls,
		},
		{
			name:   "a recent screen that cannot be read",
			rules:  append(append([]fakecli.Rule{}, two...), firstResendReadRule("0", firstResendEmptyScreen), fakecli.Rule{Argv: []string{"read", "worker", "--lines", "40"}, ArgvPrefix: true, Code: 1, Stderr: firstResendUncertain}, firstResendSendRule(3, 0, "")),
			sends:  2,
			raw:    "worker worker is not idle",
			waited: dispatchFirstResendPolls,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pauses := firstResendPauses(t)
			f := newDispatchHcFixture(t, firstResendBaseRules(tc.rules...))
			if tc.kind != "" {
				f.setKind(t, tc.kind)
			}
			if tc.before != nil {
				tc.before(t, f)
			}
			code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
			assertTodaysError(t, f, code, out, errText, tc.sends, tc.raw)
			if *pauses != tc.waited {
				t.Fatalf("pauses=%d, want %d", *pauses, tc.waited)
			}
		})
	}
}

func assertTodaysError(t *testing.T, f *dispatchHcFixture, code int, out, errText string, sends int, raw string) {
	t.Helper()
	if code != 4 {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	obj := dispatchHcJSON(t, out)
	if status, _ := obj.Get("wait_status"); status != "error" {
		t.Fatalf("status=%v, want error (out=%s)", status, out)
	}
	if got, _ := obj.Get("raw"); !strings.Contains(fmt.Sprint(got), raw) {
		t.Fatalf("raw=%v, want %q", got, raw)
	}
	if _, ok := obj.Get("resent_first"); ok {
		t.Fatalf("resent_first present (out=%s)", out)
	}
	if !strings.Contains(errText, "soho: warning: prompt submission failed (") || !strings.Contains(errText, "); inspect with: hc get worker && hc read worker. Do not resend blindly.") || strings.Contains(errText, "sent it once more") {
		t.Fatalf("stderr=%q, want today's warning only", errText)
	}
	if got := hcSends(f.hcCalls(t)); len(got) != sends {
		t.Fatalf("sends=%d, want %d: %#v", len(got), sends, got)
	}
}

// TestDispatchFirstResendPromptPathVisible: the composed prompt path on the
// visible or the recent screen means the prompt may have landed: no resend,
// and no waiting for the screen to change.
func TestDispatchFirstResendPromptPathVisible(t *testing.T) {
	for _, where := range []string{"visible", "recent"} {
		t.Run(where, func(t *testing.T) {
			pauses := firstResendPauses(t)
			f := newDispatchHcFixture(t, nil)
			path := f.composedPathFor(pinnedFixtureStamp)
			visible, recent := firstResendEmptyScreen, "history\n"+path+"\n"
			if where == "visible" {
				visible, recent = "OpenAI Codex\n"+path+"\n\n› \n\n  gpt-5 high · Context 0% used\n", ""
			}
			rules := firstResendBaseRules(
				firstResendSendRule(1, 10, firstResendMismatch),
				firstResendSendRule(2, 10, firstResendMismatch),
				firstResendSendRule(3, 0, ""),
				firstResendReadRule("0", visible),
				firstResendReadRule("40", recent),
			)
			f = rebuildFirstResendFixture(t, f, rules)
			code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
			assertTodaysError(t, f, code, out, errText, 2, "worker worker is not idle")
			if *pauses != 0 {
				t.Fatalf("pauses=%d, a visible prompt path ends the wait at once", *pauses)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("the composed path %s the rule named is not the dispatch's: %v", path, err)
			}
		})
	}
}

// rebuildFirstResendFixture installs the rules a test could only write once
// the composed path was known: a second fake hc in a new bin directory
// replaces the first fixture's on PATH and in the call-log location.
func rebuildFirstResendFixture(t *testing.T, f *dispatchHcFixture, rules []fakecli.Rule) *dispatchHcFixture {
	t.Helper()
	bin := filepath.Join(f.root, "bin2")
	all := append([]fakecli.Rule{
		{Argv: []string{"get", "t1", "--json"}, ArgvPrefix: true, Stdout: hcDispatchCaller},
		{Argv: []string{"ls", "--label", "soho.session=sX", "--json"}, ArgvPrefix: true, Stdout: hcDispatchSessionLS},
	}, rules...)
	for name, set := range map[string][]fakecli.Rule{"hc": all, "herdr": nil} {
		if _, err := fakecli.Install(t, bin, name, set); err != nil {
			t.Fatal(err)
		}
	}
	f.bin = bin
	f.env["PATH"], f.env["SOHO_FAKECLI_CONFIG"] = bin, bin
	return f
}

// firstResendGets scripts the fixture worker's state reads: the first five
// (before the dispatch's own sends and the busy branch's settle) idle, every
// later one the given state. The sixth is the readiness window's first.
func firstResendGets(later string) []fakecli.Rule {
	var rules []fakecli.Rule
	for call := 1; call <= 5; call++ {
		rules = append(rules, fakecli.Rule{Argv: []string{"get", "worker", "--json"}, Call: call, Stdout: hcWorkerGet(7, "idle")})
	}
	return append(rules, fakecli.Rule{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(7, later)})
}

// TestDispatchFirstResendWorkerNotIdle: a worker that is working for the
// whole window gets no resend after the full ten pauses; a blocked worker
// ends the window at once.
func TestDispatchFirstResendWorkerNotIdle(t *testing.T) {
	for _, tc := range []struct {
		later  string
		waited int
	}{
		{"working", dispatchFirstResendPolls},
		{"blocked", 0},
	} {
		t.Run(tc.later, func(t *testing.T) {
			pauses := firstResendPauses(t)
			rules := append(firstResendGets(tc.later),
				firstResendSendRule(1, 10, firstResendMismatch),
				firstResendSendRule(2, 10, firstResendMismatch),
				firstResendSendRule(3, 0, ""),
				firstResendReadRule("0", firstResendEmptyScreen),
				firstResendReadRule("40", ""),
			)
			f := newDispatchHcFixture(t, firstResendBaseRules(rules...))
			code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
			assertTodaysError(t, f, code, out, errText, 2, "worker worker is not idle")
			if *pauses != tc.waited {
				t.Fatalf("pauses=%d, want %d", *pauses, tc.waited)
			}
		})
	}
}

// TestDispatchFirstResendWaitsForTheWorker: working at the first reads and
// idle on the third — two pauses, then the resend.
func TestDispatchFirstResendWaitsForTheWorker(t *testing.T) {
	pauses := firstResendPauses(t)
	rules := firstResendGets("idle")
	rules = append([]fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, Call: 6, Stdout: hcWorkerGet(7, "working")},
		{Argv: []string{"get", "worker", "--json"}, Call: 7, Stdout: hcWorkerGet(7, "working")},
	}, rules...)
	rules = append(rules,
		firstResendSendRule(1, 10, firstResendMismatch),
		firstResendSendRule(2, 10, firstResendMismatch),
		firstResendSendRule(3, 0, ""),
		firstResendReadRule("0", firstResendEmptyScreen),
		firstResendReadRule("40", ""),
	)
	f := newDispatchHcFixture(t, firstResendBaseRules(rules...))
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	if code != 0 {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if v, _ := dispatchHcJSON(t, out).Get("resent_first"); v != true {
		t.Fatalf("resent_first=%v (out=%s)", v, out)
	}
	if *pauses != 2 {
		t.Fatalf("pauses=%d, want 2", *pauses)
	}
	if sends := hcSends(f.hcCalls(t)); len(sends) != 3 {
		t.Fatalf("sends=%d, want three", len(sends))
	}
}

// TestDispatchFirstResendDialogOnScreen: a known question or approval UI is a
// stop, not a wait: nothing is typed into it.
func TestDispatchFirstResendDialogOnScreen(t *testing.T) {
	pauses := firstResendPauses(t)
	dialog := "OpenAI Codex\nPick one  enter to submit answer\n\n› \n\n  gpt-5 high · Context 0% used\n"
	f := newDispatchHcFixture(t, firstResendBaseRules(
		firstResendSendRule(1, 10, firstResendMismatch),
		firstResendSendRule(2, 10, firstResendMismatch),
		firstResendSendRule(3, 0, ""),
		firstResendReadRule("0", dialog),
		firstResendReadRule("40", ""),
	))
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	assertTodaysError(t, f, code, out, errText, 2, "worker worker is not idle")
	if *pauses != 0 {
		t.Fatalf("pauses=%d, a dialog ends the window at once", *pauses)
	}
}

// TestDispatchFirstResendNoResendOption: the dispatch called with the
// no-resend option never sends again after the busy branch.
func TestDispatchFirstResendNoResendOption(t *testing.T) {
	pauses := firstResendPauses(t)
	f := newDispatchHcFixture(t, firstResendBaseRules(
		firstResendSendRule(1, 10, firstResendMismatch),
		firstResendSendRule(2, 10, firstResendMismatch),
		firstResendSendRule(3, 0, ""),
		firstResendReadRule("0", firstResendEmptyScreen),
		firstResendReadRule("40", ""),
	))
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, stderr bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &stderr
	code := func() int {
		defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
		return cmdDispatchWithOptions([]string{"worker", f.brief, "--no-wait"}, f.ctx, f.env, f.root, dispatchOptions{noResend: true})
	}()
	assertTodaysError(t, f, code, out.String(), stderr.String(), 2, "worker worker is not idle")
	if *pauses != 0 {
		t.Fatalf("pauses=%d, the no-resend option never waits for a resend", *pauses)
	}
}

// firstResendStub is the transport the readiness window reads: its state and
// screen follow the number of reads, so a test fixes the second the worker
// becomes ready.
type firstResendStub struct {
	transport.Transport
	states     int
	idleAt     int // the state read (0-based) at which the worker reads idle
	stateWord  string
	visible    string
	recent     string
	visibleErr bool
	recentErr  bool
	reads      []string
}

func (s *firstResendStub) Get(_ context.Context, _ transport.Target, _ transport.Call) transport.Reply[transport.Terminal] {
	word := s.stateWord
	if word == "" {
		word = "working"
		if s.states >= s.idleAt {
			word = "idle"
		}
	}
	s.states++
	return transport.Reply[transport.Terminal]{Value: transport.Terminal{State: word}, Valid: true}
}

func (s *firstResendStub) Read(_ context.Context, _ transport.Target, read transport.Read, _ transport.Call) transport.Reply[string] {
	s.reads = append(s.reads, read.Source)
	if read.Source == "visible" {
		if s.visibleErr {
			return transport.Reply[string]{Err: &transport.Failure{Kind: transport.FailureUnavailable, Message: "read failed"}}
		}
		return transport.Reply[string]{Value: s.visible, Valid: true}
	}
	if s.recentErr {
		return transport.Reply[string]{Err: &transport.Failure{Kind: transport.FailureUnavailable, Message: "read failed"}}
	}
	return transport.Reply[string]{Value: s.recent, Valid: true}
}

// TestFirstDispatchResendWindowBoundaries: the readiness window is eleven
// probes with ten one-second pauses between them — ready at 0, 1 and 10 s
// resends, a worker ready only at 11 s does not.
func TestFirstDispatchResendWindowBoundaries(t *testing.T) {
	const composed = "/state/briefs/worker-1.md"
	for _, tc := range []struct {
		name   string
		stub   firstResendStub
		ready  bool
		pauses int
		reads  int // visible reads
	}{
		{"ready at 0 s", firstResendStub{idleAt: 0, visible: firstResendEmptyScreen}, true, 0, 1},
		{"ready at 1 s", firstResendStub{idleAt: 1, visible: firstResendEmptyScreen}, true, 1, 1},
		{"ready at 10 s", firstResendStub{idleAt: 10, visible: firstResendEmptyScreen}, true, 10, 1},
		{"ready only at 11 s", firstResendStub{idleAt: 11, visible: firstResendEmptyScreen}, false, 10, 0},
		{"a draft that never clears", firstResendStub{idleAt: 0, visible: firstResendDraftScreen}, false, 10, 11},
		{"a visible screen that never reads", firstResendStub{idleAt: 0, visibleErr: true}, false, 10, 11},
		{"a recent screen that never reads", firstResendStub{idleAt: 0, visible: firstResendEmptyScreen, recentErr: true}, false, 10, 11},
		{"the path on the recent screen", firstResendStub{idleAt: 0, visible: firstResendEmptyScreen, recent: "x " + composed}, false, 0, 1},
		{"the path on the visible screen", firstResendStub{idleAt: 0, visible: "x " + composed + "\n\n› \n"}, false, 0, 1},
		{"a blocked worker", firstResendStub{stateWord: "blocked", visible: firstResendEmptyScreen}, false, 0, 0},
		{"a worker in error", firstResendStub{stateWord: "error", visible: firstResendEmptyScreen}, false, 10, 0},
		{"a done worker", firstResendStub{stateWord: "done", visible: firstResendEmptyScreen}, false, 10, 0},
		{"an unreadable worker state", firstResendStub{stateWord: "unknown", visible: firstResendEmptyScreen}, false, 10, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pauses := firstResendPauses(t)
			stub := tc.stub
			got := firstDispatchResendReady(transport.Services{Runtime: &stub}, "worker", "codex", composed)
			if got != tc.ready || *pauses != tc.pauses {
				t.Fatalf("ready=%v pauses=%d, want ready=%v pauses=%d", got, *pauses, tc.ready, tc.pauses)
			}
			visible := 0
			for _, source := range stub.reads {
				if source == "visible" {
					visible++
				}
			}
			if visible != tc.reads {
				t.Fatalf("visible reads=%d, want %d (reads=%v)", visible, tc.reads, stub.reads)
			}
		})
	}
}

// TestDispatchFirstResendAmendAndQueueKeepTheirResult: an amendment and a
// queued dispatch keep their results and never take the first-dispatch
// resend. A first dispatch cannot be an amendment (no earlier dispatch to
// amend: refused before any send); a queued codex dispatch is refused as
// still working after its one guarded send.
func TestDispatchFirstResendAmendAndQueueKeepTheirResult(t *testing.T) {
	for _, tc := range []struct {
		flag  string
		code  int
		sends int
		text  string
	}{
		{"--amend", 2, 0, "--amend needs an earlier dispatch to 'worker' (nothing to amend)"},
		{"--queue", 4, 1, "worker worker is still working; queued dispatch was not sent. Wait for it to settle, then dispatch again without --queue"},
	} {
		t.Run(tc.flag, func(t *testing.T) {
			pauses := firstResendPauses(t)
			f := newDispatchHcFixture(t, firstResendBaseRules(
				firstResendSendRule(1, 10, firstResendMismatch),
				firstResendSendRule(2, 10, firstResendMismatch),
				firstResendSendRule(3, 0, ""),
				firstResendReadRule("0", firstResendEmptyScreen),
				firstResendReadRule("40", ""),
			))
			code, out, errText := f.run(t, "worker", f.brief, tc.flag, "--no-wait")
			if code != tc.code || len(hcSends(f.hcCalls(t))) != tc.sends || !strings.Contains(errText, tc.text) {
				t.Fatalf("%s: code=%d sends=%d, want code=%d sends=%d with %q (out=%s stderr=%q)", tc.flag, code, len(hcSends(f.hcCalls(t))), tc.code, tc.sends, tc.text, out, errText)
			}
			if strings.Contains(out, "resent_first") || strings.Contains(errText, "sent it once more") || *pauses != 0 {
				t.Fatalf("%s took the first-dispatch resend (pauses=%d out=%s stderr=%q)", tc.flag, *pauses, out, errText)
			}
		})
	}
}

// firstResendFrictionLog returns the dispatch's friction log, "" when none.
func firstResendFrictionLog(f *dispatchHcFixture) string {
	raw, err := os.ReadFile(filepath.Join(f.sd, "friction.log"))
	if err != nil {
		return ""
	}
	return string(raw)
}

// TestDispatchSubmitFailedWarningCarriesTheCause pins the literal warning
// line for an exit 10 refusal and for an uncertain error, on stderr and in
// the friction log; the JSON raw field keeps its message.
func TestDispatchSubmitFailedWarningCarriesTheCause(t *testing.T) {
	const tail = "; inspect with: hc get worker && hc read worker. Do not resend blindly."
	for _, tc := range []struct {
		name  string
		rules []fakecli.Rule
		want  string
		raw   string
	}{
		{
			name:  "exit 10 refusal",
			rules: []fakecli.Rule{firstResendSendRule(1, 10, firstResendMismatch), firstResendSendRule(2, 10, firstResendMismatch), firstResendSendRule(3, 10, firstResendMismatch), firstResendReadRule("0", firstResendEmptyScreen), firstResendReadRule("40", "")},
			want:  "soho: warning: prompt submission failed (busy: worker worker is not idle; cause: hc exited 10: status_mismatch: the terminal is not ready)" + tail,
			raw:   "worker worker is not idle",
		},
		{
			name:  "uncertain error",
			rules: []fakecli.Rule{firstResendSendRule(1, 1, firstResendUncertain)},
			want:  "soho: warning: prompt submission failed (unavailable: hc exited 1: unavailable: core went away)" + tail,
			raw:   "hc exited 1: unavailable: core went away",
		},
		{
			name:  "message with newlines and control characters",
			rules: []fakecli.Rule{firstResendSendRule(1, 1, `{"error":{"kind":"unavailable","message":"line one\nline two\r\n\u001b[31mred\u0000"}}`)},
			want:  `soho: warning: prompt submission failed (unavailable: hc exited 1: unavailable: line one\nline two\r\n\x1b[31mred\x00)` + tail,
			raw:   "line one",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			firstResendPauses(t)
			f := newDispatchHcFixture(t, firstResendBaseRules(tc.rules...))
			code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
			if code != 4 {
				t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
			}
			if !strings.Contains(errText, tc.want+"\n") {
				t.Fatalf("stderr=%q, want the line %q", errText, tc.want)
			}
			for _, line := range strings.Split(errText, "\n") {
				if strings.Contains(line, "prompt submission failed") && strings.ContainsAny(line, "\r\x1b\x00") {
					t.Fatalf("warning line %q holds a raw control character", line)
				}
			}
			if got, _ := dispatchHcJSON(t, out).Get("raw"); !strings.Contains(fmt.Sprint(got), tc.raw) {
				t.Fatalf("raw=%v, want it to keep %q", got, tc.raw)
			}
			log := firstResendFrictionLog(f)
			t.Logf("friction log bytes=%d", len(log))
			if log != "" && !strings.Contains(log, "\t"+strings.TrimPrefix(tc.want, "soho: warning: ")+"\n") {
				t.Fatalf("friction log=%q, want one line carrying the cause", log)
			}
		})
	}
}

// TestSubmitFailedWarning covers the text builder: no message keeps today's
// line, the cause is bounded at 200 characters, control characters are
// escaped, Cause is added only when it says more than the message.
func TestSubmitFailedWarning(t *testing.T) {
	const probe = "hc get worker && hc read worker"
	const today = "prompt submission failed; inspect with: " + probe + ". Do not resend blindly."
	long := strings.Repeat("x", 250)
	for _, tc := range []struct {
		name string
		err  *transport.Failure
		want string
	}{
		{"nil failure", nil, today},
		{"no message, no cause", &transport.Failure{Kind: transport.FailureBusy}, today},
		{"no message with a cause", &transport.Failure{Kind: transport.FailureBusy, Cause: errors.New("boom")}, today},
		{"kind and message", &transport.Failure{Kind: transport.FailureBusy, Message: "worker w is not idle"}, "prompt submission failed (busy: worker w is not idle); inspect with: " + probe + ". Do not resend blindly."},
		{"code, message and a different cause", &transport.Failure{Kind: transport.FailureCommand, Code: "E7", Message: "no go", Cause: errors.New("underlying")}, "prompt submission failed (command/E7: no go; cause: underlying); inspect with: " + probe + ". Do not resend blindly."},
		{"a cause equal to the message", &transport.Failure{Kind: transport.FailureUnavailable, Message: "down", Cause: errors.New("down")}, "prompt submission failed (unavailable: down); inspect with: " + probe + ". Do not resend blindly."},
		{"newlines, tabs and escapes", &transport.Failure{Kind: transport.FailureBusy, Message: "a\nb\tc\r\x1bd\u0085e\x7ff"}, `prompt submission failed (busy: a\nb\tc\r\x1bd\u0085e\x7ff); inspect with: ` + probe + ". Do not resend blindly."},
		{"exactly 200 characters", &transport.Failure{Kind: transport.FailureBusy, Message: strings.Repeat("y", 200-len("busy: "))}, "prompt submission failed (busy: " + strings.Repeat("y", 200-len("busy: ")) + "); inspect with: " + probe + ". Do not resend blindly."},
		{"201 characters", &transport.Failure{Kind: transport.FailureBusy, Message: strings.Repeat("y", 201-len("busy: "))}, "prompt submission failed (busy: " + strings.Repeat("y", 200-len("busy: ")) + "...); inspect with: " + probe + ". Do not resend blindly."},
		{"over 200 characters", &transport.Failure{Kind: transport.FailureBusy, Message: long}, "prompt submission failed (busy: " + strings.Repeat("x", 200-len("busy: ")) + "...); inspect with: " + probe + ". Do not resend blindly."},
		{"multibyte characters are cut whole", &transport.Failure{Kind: transport.FailureBusy, Message: strings.Repeat("\u00e7", 300)}, "prompt submission failed (busy: " + strings.Repeat("\u00e7", 200-len("busy: ")) + "...); inspect with: " + probe + ". Do not resend blindly."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := submitFailedWarning(tc.err, probe); got != tc.want {
				t.Fatalf("warning=%q, want %q", got, tc.want)
			}
		})
	}
}
