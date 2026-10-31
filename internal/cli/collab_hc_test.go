package cli

// DJA-604 follow-up: soho collaborate under transport=hc. The guard lets
// the command through; the participants resolve through the session's own
// terminal records (name, address and cwd) and the one identity notice
// prints per command. The fake-hc technique is the one the spawn,
// dispatch and release hc tests use (fakecli.Install on PATH plus
// SOHO_FAKECLI_CONFIG): the fake records every call and answers with
// scripted JSON. A fake herdr sentinel fails the test if it is ever
// called: under transport=hc the collaborate path must reach the
// terminals only through hc.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/collaboration"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// hcCollabNotice is the one notice per collaborate command under
// transport=hc, with the existing soho warning prefix.
const hcCollabNotice = "soho: warning: collaborate: transport=hc does not verify participant identity yet; participants are trusted by terminal name, address and cwd"

// collabHcSpec deviates the participant terminal records from the happy
// path (the roster rows always say author t7 and reviewer t8, their cwd
// the project root): a record address different from the roster's pane
// column, a different cwd, a finished terminal or a failed (not-found)
// get.
type collabHcSpec struct {
	recordReviewerAddr string
	reviewerCwd        string
	reviewerExit       int
	reviewerFin        bool
}

func (s *collabHcSpec) fill() {
	if s.recordReviewerAddr == "" {
		s.recordReviewerAddr = "t8"
	}
}

type collabHcFixture struct {
	root  string
	sd    string
	bin   string
	env   platform.Env
	brief string
}

// collabHcTerminal renders one live terminal record (or a finished one
// when finished) with the given address, name and cwd.
func collabHcTerminal(t *testing.T, name, addr, cwd, status string, finished bool) string {
	t.Helper()
	cwdJSON, err := json.Marshal(cwd)
	if err != nil {
		t.Fatal(err)
	}
	state := "live"
	if finished {
		state = "finished"
	}
	body := `{"address":"` + addr + `","name":"` + name + `","state":"` + state + `","cwd":` + string(cwdJSON)
	if !finished {
		body += `,"status":{"state":"` + status + `","turn":1}}`
	} else {
		body += `}`
	}
	return body
}

// newCollabHcFixture sets up a collaboration for transport=hc in root:
// the caller terminal t1 of session sX (named orchestrator, cwd the
// project root), the author and reviewer roster rows (their pane column
// is the hc address, their cwd the project root), the worker_messages
// policy the start permission checks need, the collaboration brief, the
// fake hc rules and the fake herdr sentinel.
func newCollabHcFixture(t *testing.T, spec *collabHcSpec) *collabHcFixture {
	t.Helper()
	if spec == nil {
		spec = &collabHcSpec{}
	}
	spec.fill()
	raw := t.TempDir()
	root, err := filepath.EvalSymlinks(raw)
	if err != nil {
		t.Fatal(err)
	}
	if spec.reviewerCwd == "" {
		spec.reviewerCwd = root
	}
	state, bin, home, conf := filepath.Join(root, "state"), filepath.Join(root, "bin"), filepath.Join(root, "home"), filepath.Join(root, "conf")
	sd := filepath.Join(state, "sX")
	for _, dir := range []string{state, sd, home, conf, bin, filepath.Join(root, "src")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "src", "app.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\tmode\targs\teffort\n" +
		"author\tt7\tcodex\timplementer\topenai\t0\t" + root + "\tauthor-start\tgpt-5.4\task\timplementer\tbuild\t\t\t\n" +
		"reviewer\tt8\tclaude\treviewer\tanthropic\t0\t" + root + "\treviewer-start\tclaude-opus-5-5\task\treviewer\treview\t\t\t\n"
	if err := os.WriteFile(filepath.Join(sd, "agents.tsv"), []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	// The worker_messages policy the start permission checks (review.ready
	// author -> reviewer, review.finding/review.result reviewer -> author)
	// need, in the session conf the hc workspace (the session) selects.
	policy := "worker_messages=policy\nworker_messages.rules.request.from=role:implementer\nworker_messages.rules.request.to=role:reviewer\nworker_messages.rules.request.types=review.ready,review.question\nworker_messages.rules.request.scope=assignment\nworker_messages.rules.feedback.from=role:reviewer\nworker_messages.rules.feedback.to=role:implementer\nworker_messages.rules.feedback.types=review.finding,review.result,review.question\nworker_messages.rules.feedback.scope=assignment\n"
	if err := os.WriteFile(filepath.Join(sd, "session.conf"), []byte(policy), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &collabHcFixture{root: root, sd: sd, bin: bin}
	f.brief = filepath.Join(root, "brief.md")
	body := "# Goal\nImplement a small change.\n# Expected result\nCorrect behavior.\n# Owned files\n- src/app.go\n# Forbidden\nNo commit/push. No other files.\n# Report\nWrite the declared report.\n"
	if err := os.WriteFile(f.brief, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	callerCwdJSON, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	// The caller terminal carries the soho.session label: under hc the
	// workspace is that session (state dir, the assignment's workspace
	// column, the session conf).
	caller := `{"address":"t1","name":"orchestrator","state":"live","labels":{"soho.session":"sX"},"cwd":` + string(callerCwdJSON) + `,"status":{"state":"idle","turn":1}}`
	authorRule := fakecli.Rule{Argv: []string{"get", "author", "--json"}, Stdout: collabHcTerminal(t, "author", "t7", root, "working", false)}
	var reviewerRule fakecli.Rule
	if spec.reviewerExit != 0 {
		reviewerRule = fakecli.Rule{Argv: []string{"get", "reviewer", "--json"}, Code: spec.reviewerExit}
	} else {
		reviewerRule = fakecli.Rule{Argv: []string{"get", "reviewer", "--json"}, Stdout: collabHcTerminal(t, "reviewer", spec.recordReviewerAddr, spec.reviewerCwd, "idle", spec.reviewerFin)}
	}
	all := []fakecli.Rule{
		{Argv: []string{"get", "t1", "--json"}, Stdout: caller},
		authorRule,
		reviewerRule,
	}
	if _, err := fakecli.Install(t, bin, "hc", all); err != nil {
		t.Fatal(err)
	}
	if _, err := fakecli.Install(t, bin, "herdr", nil); err != nil {
		t.Fatal(err)
	}
	f.env = platform.Env{
		"PATH": bin, "HOME": home, "USERPROFILE": home, "XDG_CONFIG_HOME": conf,
		"TMPDIR": root, "SOHO_FAKECLI_CONFIG": bin,
		"SOHO_DIR": state, "SOHO_TRANSPORT": "hc", "HC_TERMINAL_ID": "t1",
		// Keep race detection without its per-child exit delay in the bounded delivery.
		"GORACE": fakecli.RaceOptions([]string{"GORACE=" + os.Getenv("GORACE")}),
	}
	t.Chdir(root)
	return f
}

// run swaps the platform writers around one Run call and returns the exit
// code plus the captured stdout and stderr.
func (f *collabHcFixture) run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, stderr bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &stderr
	code := Run(append([]string{}, args...), f.env)
	platform.Stdout, platform.Stderr = oldOut, oldErr
	return code, out.String(), stderr.String()
}

// assertNoHerdrCall fails when the fake herdr sentinel recorded a call:
// under transport=hc the collaborate path must reach the terminals only
// through hc.
func (f *collabHcFixture) assertNoHerdrCall(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(f.bin, "herdr.calls.jsonl")); err == nil {
		calls, _ := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
		t.Fatalf("herdr was called: %#v", calls)
	}
}

// noticeCount counts the identity notice lines in one command's stderr.
func noticeCount(stderr string) int {
	return strings.Count(stderr, hcCollabNotice)
}

// TestCollaborateHcStartAndStatus: the author and reviewer are live
// session terminals with matching names, addresses and cwds; start and
// status succeed, the notice prints exactly once per command, and the
// path never touches herdr.
func TestCollaborateHcStartAndStatus(t *testing.T) {
	f := newCollabHcFixture(t, nil)
	code, out, stderr := f.run(t, "collaborate", "start", "author", "reviewer", "--brief", f.brief)
	var a collaboration.Assignment
	if code != 0 || json.Unmarshal([]byte(out), &a) != nil || a.ID == "" {
		t.Fatalf("start code=%d out=%q stderr=%q, want the assignment JSON", code, out, stderr)
	}
	if n := noticeCount(stderr); n != 1 {
		t.Fatalf("start stderr=%q, want exactly one notice", stderr)
	}
	f.assertNoHerdrCall(t)

	code, out, stderr = f.run(t, "collaborate", "status", a.ID)
	if code != 0 {
		t.Fatalf("status code=%d out=%q stderr=%q, want the status line", code, out, stderr)
	}
	if !strings.HasPrefix(out, a.ID+"\t") || !strings.Contains(out, "revision=") {
		t.Fatalf("status out=%q, want the <id>\t... line with the revision", out)
	}
	if n := noticeCount(stderr); n != 1 {
		t.Fatalf("status stderr=%q, want exactly one notice", stderr)
	}
	f.assertNoHerdrCall(t)
}

// TestCollaborateHcAddressMismatchIsRefused: a participant whose address
// no longer matches its registration takes the existing identity-changed
// refusal.
func TestCollaborateHcAddressMismatchIsRefused(t *testing.T) {
	f := newCollabHcFixture(t, &collabHcSpec{recordReviewerAddr: "t9"})
	code, out, stderr := f.run(t, "collaborate", "start", "author", "reviewer", "--brief", f.brief)
	if code != 2 {
		t.Fatalf("start code=%d out=%q stderr=%q, want the refusal exit 2", code, out, stderr)
	}
	if want := "soho: collaborate: participant pane, workspace or session no longer matches its registration\n"; !strings.Contains(stderr, want) {
		t.Fatalf("start stderr=%q, want the identity-changed refusal", stderr)
	}
	if n := noticeCount(stderr); n != 1 {
		t.Fatalf("start stderr=%q, want exactly one notice", stderr)
	}
	f.assertNoHerdrCall(t)
}

// TestCollaborateHcMissingOrFinishedParticipantIsRefused: a participant
// whose terminal is missing (the hc not-found exit) or finished takes the
// cannot-verify refusal.
func TestCollaborateHcMissingOrFinishedParticipantIsRefused(t *testing.T) {
	want := "soho: collaborate: cannot verify live collaboration participant reviewer\n"
	for _, tc := range []struct {
		name string
		spec collabHcSpec
	}{{"missing", collabHcSpec{reviewerExit: 3}}, {"finished", collabHcSpec{reviewerFin: true}}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCollabHcFixture(t, &tc.spec)
			code, out, stderr := f.run(t, "collaborate", "start", "author", "reviewer", "--brief", f.brief)
			if code != 2 {
				t.Fatalf("start code=%d out=%q stderr=%q, want the refusal exit 2", code, out, stderr)
			}
			if !strings.Contains(stderr, want) {
				t.Fatalf("start stderr=%q, want the cannot-verify refusal", stderr)
			}
			f.assertNoHerdrCall(t)
		})
	}
}

// TestCollaborateHcCwdChangedIsRefused: a participant whose terminal cwd
// is no longer the registered cwd takes the working-directory refusal.
func TestCollaborateHcCwdChangedIsRefused(t *testing.T) {
	// A real directory different from the fixture's project root: the
	// reviewer's terminal reports it, the roster row does not.
	other := t.TempDir()
	f := newCollabHcFixture(t, &collabHcSpec{reviewerCwd: other})
	code, out, stderr := f.run(t, "collaborate", "start", "author", "reviewer", "--brief", f.brief)
	if code != 2 {
		t.Fatalf("start code=%d out=%q stderr=%q, want the refusal exit 2", code, out, stderr)
	}
	if want := "soho: collaborate: participant working directory changed\n"; !strings.Contains(stderr, want) {
		t.Fatalf("start stderr=%q, want the working-directory refusal", stderr)
	}
	f.assertNoHerdrCall(t)
}

// reinstallHc swaps the fixture's fake hc image for a fresh one in a new
// bin (fakecli.Install refuses an existing image): the orchestrator
// caller by address, the author and reviewer by address and name (their
// records carry the session label), the recipient record for address t8
// (or a not-found exit), and the scripted send result.
func (f *collabHcFixture) reinstallHc(t *testing.T, recipientStdout string, recipientCode, sendCode int, sendStderr string) {
	t.Helper()
	cwdJSON, err := json.Marshal(f.root)
	if err != nil {
		t.Fatal(err)
	}
	caller := `{"address":"t1","name":"orchestrator","state":"live","labels":{"soho.session":"sX"},"cwd":` + string(cwdJSON) + `,"status":{"state":"idle","turn":1}}`
	label := func(rec string) string {
		return strings.Replace(rec, `"state":"live"`, `"state":"live","labels":{"soho.session":"sX"}`, 1)
	}
	author := label(collabHcTerminal(t, "author", "t7", f.root, "working", false))
	reviewer := label(collabHcTerminal(t, "reviewer", "t8", f.root, "idle", false))
	if recipientStdout == "" {
		recipientStdout = reviewer
	}
	rules := []fakecli.Rule{
		{Argv: []string{"get", "t1", "--json"}, Stdout: caller},
		{Argv: []string{"get", "t7", "--json"}, Stdout: author},
		{Argv: []string{"get", "author", "--json"}, Stdout: author},
		{Argv: []string{"get", "reviewer", "--json"}, Stdout: reviewer},
		{Argv: []string{"get", "t8", "--json"}, Stdout: recipientStdout, Code: recipientCode},
	}
	if sendCode == 0 {
		rules = append(rules, fakecli.Rule{Argv: []string{"send"}, ArgvPrefix: true, Stdout: `{"started":true,"turn":2}`})
	} else {
		rules = append(rules, fakecli.Rule{Argv: []string{"send"}, ArgvPrefix: true, Code: sendCode, Stderr: sendStderr})
	}
	f.bin = t.TempDir()
	if _, err := fakecli.Install(t, f.bin, "hc", rules); err != nil {
		t.Fatal(err)
	}
	if _, err := fakecli.Install(t, f.bin, "herdr", nil); err != nil {
		t.Fatal(err)
	}
	f.env["PATH"] = f.bin
	f.env["SOHO_FAKECLI_CONFIG"] = f.bin
}

// hcCalls reads the fake hc call log.
func (f *collabHcFixture) hcCalls(t *testing.T) []fakecli.Call {
	t.Helper()
	calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "hc.calls.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return calls
}

// assertHcSendCount asserts that exactly n hc send calls were recorded
// so far, that the last one went to addr as a queued send (no --if guard,
// the message after the options terminator) and that it carries the
// assignment's metadata.
func assertHcSendCount(t *testing.T, f *collabHcFixture, n int, addr, assignmentID string) {
	t.Helper()
	var sends []fakecli.Call
	for _, c := range f.hcCalls(t) {
		if len(c.Argv) > 0 && c.Argv[0] == "send" {
			sends = append(sends, c)
		}
	}
	if len(sends) != n {
		t.Fatalf("hc send calls = %d: %#v, want %d", len(sends), sends, n)
	}
	if n == 0 {
		return
	}
	last := sends[n-1].Argv
	if len(last) != 5 || last[1] != "--json" || last[2] != "--" || last[3] != addr {
		t.Fatalf("send argv=%q, want send --json -- %s <message>", last, addr)
	}
	for _, a := range last {
		if a == "--if" {
			t.Fatalf("send argv=%q carries the --if guard, want the queued send", last)
		}
	}
	if !strings.Contains(last[4], "Assignment: "+assignmentID) {
		t.Fatalf("send message=%q, want the assignment metadata", last[4])
	}
}

// assertNoHcRead fails when the fake hc recorded a screen read: under
// transport=hc the hc state replaces the screen read.
func (f *collabHcFixture) assertNoHcRead(t *testing.T) {
	t.Helper()
	for _, c := range f.hcCalls(t) {
		if len(c.Argv) > 0 && c.Argv[0] == "read" {
			t.Fatalf("hc read argv=%q, want no screen read under hc", c.Argv)
		}
	}
}

// hcCliEvent writes one report, computes the current revision and runs
// collaborate event with it, parsing the updated assignment JSON.
func (f *collabHcFixture) hcCliEvent(t *testing.T, a collaboration.Assignment, pane, event, body string) collaboration.Assignment {
	t.Helper()
	report := filepath.Join(f.root, event+".md")
	if err := os.WriteFile(report, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	revision, err := collaboration.Fingerprint(a.Author.Cwd, a.Paths)
	if err != nil {
		t.Fatal(err)
	}
	f.env["HC_TERMINAL_ID"] = pane
	code, out, stderr := f.run(t, "collaborate", "event", a.ID, event, "--report", report, "--revision", revision.Fingerprint)
	if code != 0 || json.Unmarshal([]byte(out), &a) != nil {
		t.Fatalf("event %s code=%d out=%q stderr=%q", event, code, out, stderr)
	}
	return a
}

// TestCollaborateHcFullCycleDeliversThroughHc: start, the author's ready
// event, the reviewer's approval and the orchestrator's finalize all run
// under transport=hc. Every event is delivered with one queued hc send to
// the recipient's address (no --if guard, no screen read), the assignment
// reaches finished, and the fake herdr is never called. (Ports the
// reviewer probe TestReviewerHcEventReadyDelivery into the committed full
// cycle.)
func TestCollaborateHcFullCycleDeliversThroughHc(t *testing.T) {
	f := newCollabHcFixture(t, nil)
	code, out, stderr := f.run(t, "collaborate", "start", "author", "reviewer", "--brief", f.brief)
	var a collaboration.Assignment
	if code != 0 || json.Unmarshal([]byte(out), &a) != nil || a.ID == "" {
		t.Fatalf("start code=%d out=%q stderr=%q, want the assignment JSON", code, out, stderr)
	}
	f.reinstallHc(t, "", 0, 0, "")

	// The author's ready event, as the author's terminal.
	a = f.hcCliEvent(t, a, "t7", "ready", "Implementation ready\n")
	if a.Phase != collaboration.Reviewing || len(a.Events) != 1 || a.Events[0].Delivery != "submitted" {
		t.Fatalf("ready: phase=%s delivery=%+v, want reviewing/submitted", a.Phase, a.Events)
	}
	assertHcSendCount(t, f, 1, "t8", a.ID)

	// The reviewer's approval, as the reviewer's terminal: the send goes
	// the other way (to the author's address).
	a = f.hcCliEvent(t, a, "t8", "approved", "findings: 0 (P0 0, P1 0, P2 0, P3 0) | verdict: pass\nVerified snapshot.\n")
	if a.Phase != collaboration.AwaitingOrchestrator || len(a.Events) != 2 || a.Events[1].Delivery != "submitted" {
		t.Fatalf("approved: phase=%s events=%+v, want awaiting-orchestrator with the submitted approval", a.Phase, a.Events)
	}
	assertHcSendCount(t, f, 2, "t7", a.ID)

	// The orchestrator's finalize, as the session's orchestrator terminal.
	f.env["HC_TERMINAL_ID"] = "t1"
	code, out, stderr = f.run(t, "collaborate", "finalize", a.ID, "--verdict", "accept")
	if code != 0 || json.Unmarshal([]byte(out), &a) != nil || a.Phase != collaboration.Finished {
		t.Fatalf("finalize code=%d phase=%s stderr=%q, want finished", code, a.Phase, stderr)
	}
	f.assertNoHcRead(t)
	f.assertNoHerdrCall(t)
}

// TestCollaborateHcEventBlockedRecipientIsRefusedWithoutSend: a recipient
// blocked on a person's prompt refuses the event (exit 15) with nothing
// sent.
func TestCollaborateHcEventBlockedRecipientIsRefusedWithoutSend(t *testing.T) {
	f := newCollabHcFixture(t, nil)
	code, out, stderr := f.run(t, "collaborate", "start", "author", "reviewer", "--brief", f.brief)
	var a collaboration.Assignment
	if code != 0 || json.Unmarshal([]byte(out), &a) != nil {
		t.Fatalf("start code=%d out=%q stderr=%q", code, out, stderr)
	}
	f.reinstallHc(t, collabHcTerminal(t, "reviewer", "t8", f.root, "blocked", false), 0, 0, "")
	_, stderr = f.hcCliEventFails(t, a, "t7", "ready", "Implementation ready\n", 15)
	want := "soho: collaborate: delivery refused: recipient is blocked on a prompt; nothing was sent; inspect before any new attempt\n"
	if !strings.Contains(stderr, want) {
		t.Fatalf("event stderr=%q, want the blocked-recipient refusal", stderr)
	}
	assertHcSendCount(t, f, 0, "", "")
	f.assertNoHcRead(t)
	f.assertNoHerdrCall(t)
}

// TestCollaborateHcEventAddressMismatchIsRefused: a recipient record
// whose address no longer matches its registration refuses the event
// with nothing sent.
func TestCollaborateHcEventAddressMismatchIsRefused(t *testing.T) {
	f := newCollabHcFixture(t, nil)
	code, out, stderr := f.run(t, "collaborate", "start", "author", "reviewer", "--brief", f.brief)
	var a collaboration.Assignment
	if code != 0 || json.Unmarshal([]byte(out), &a) != nil {
		t.Fatalf("start code=%d out=%q stderr=%q", code, out, stderr)
	}
	f.reinstallHc(t, collabHcTerminal(t, "reviewer", "t9", f.root, "idle", false), 0, 0, "")
	_, stderr = f.hcCliEventFails(t, a, "t7", "ready", "Implementation ready\n", 15)
	want := "soho: collaborate: delivery refused: recipient changed or became unavailable; inspect before any new attempt\n"
	if !strings.Contains(stderr, want) {
		t.Fatalf("event stderr=%q, want the changed-or-unavailable refusal", stderr)
	}
	assertHcSendCount(t, f, 0, "", "")
	f.assertNoHerdrCall(t)
}

// TestCollaborateHcEventSendBlockedIsRefused: a send that exits the hc
// blocked exit (9) refuses the delivery with the failure's message.
func TestCollaborateHcEventSendBlockedIsRefused(t *testing.T) {
	f := newCollabHcFixture(t, nil)
	code, out, stderr := f.run(t, "collaborate", "start", "author", "reviewer", "--brief", f.brief)
	var a collaboration.Assignment
	if code != 0 || json.Unmarshal([]byte(out), &a) != nil {
		t.Fatalf("start code=%d out=%q stderr=%q", code, out, stderr)
	}
	f.reinstallHc(t, "", 0, 9, "")
	_, stderr = f.hcCliEventFails(t, a, "t7", "ready", "Implementation ready\n", 15)
	if !strings.Contains(stderr, "soho: collaborate: delivery refused: worker t8 is blocked: a person must answer its prompt") {
		t.Fatalf("event stderr=%q, want the blocked-send refusal", stderr)
	}
	assertHcSendCount(t, f, 1, "t8", a.ID)
	f.assertNoHerdrCall(t)
}

// TestCollaborateHcEventSendFailureIsUncertain: a send that fails
// otherwise (exit 1) records the delivery as uncertain.
func TestCollaborateHcEventSendFailureIsUncertain(t *testing.T) {
	f := newCollabHcFixture(t, nil)
	code, out, stderr := f.run(t, "collaborate", "start", "author", "reviewer", "--brief", f.brief)
	var a collaboration.Assignment
	if code != 0 || json.Unmarshal([]byte(out), &a) != nil {
		t.Fatalf("start code=%d out=%q stderr=%q", code, out, stderr)
	}
	f.reinstallHc(t, "", 0, 1, "connection disappeared")
	_, stderr = f.hcCliEventFails(t, a, "t7", "ready", "Implementation ready\n", 15)
	if !strings.Contains(stderr, "soho: collaborate: delivery uncertain: hc exited 1: connection disappeared") {
		t.Fatalf("event stderr=%q, want the uncertain send", stderr)
	}
	updated, err := collaboration.Store{StateDir: f.sd}.Read(a.ID)
	if err != nil || len(updated.Events) != 1 || updated.Events[0].Delivery != "uncertain" {
		t.Fatalf("stored delivery=%+v err=%v, want the uncertain event", updated.Events, err)
	}
	assertHcSendCount(t, f, 1, "t8", a.ID)
	f.assertNoHerdrCall(t)
}

// hcCliEventFails writes one report, computes the current revision and
// runs collaborate event with it, expecting the given refusal exit; the
// updated assignment JSON and the stderr are captured for the caller.
func (f *collabHcFixture) hcCliEventFails(t *testing.T, a collaboration.Assignment, pane, event, body string, wantCode int) (collaboration.Assignment, string) {
	t.Helper()
	report := filepath.Join(f.root, event+".md")
	if err := os.WriteFile(report, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	revision, err := collaboration.Fingerprint(a.Author.Cwd, a.Paths)
	if err != nil {
		t.Fatal(err)
	}
	f.env["HC_TERMINAL_ID"] = pane
	code, out, stderr := f.run(t, "collaborate", "event", a.ID, event, "--report", report, "--revision", revision.Fingerprint)
	if code != wantCode {
		t.Fatalf("event %s code=%d out=%q stderr=%q, want the refusal exit %d", event, code, out, stderr, wantCode)
	}
	if json.Unmarshal([]byte(out), &a) != nil {
		t.Fatalf("event %s out=%q, want the updated assignment JSON", event, out)
	}
	return a, stderr
}
