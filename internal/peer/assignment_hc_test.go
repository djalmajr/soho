package peer

// DJA-604 follow-up: under transport=hc the assignment delivery decides
// from the recipient's own hc state (no screen read) and submits with the
// queued hc send. These tests drive SendAssignmentMessage directly with a
// fake hc (the dispatch/release hc test technique: fakecli.Install on
// PATH plus SOHO_FAKECLI_CONFIG; the fake records every call and answers
// with scripted JSON) and a fake herdr sentinel that fails the test if it
// is ever called: under transport=hc the delivery must reach the
// terminals only through hc.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/collaboration"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// hcDeliverySpec deviates the recipient terminal record and the send
// result from the happy path: a record address different from the
// roster's pane column, a blocked state, a failed (not-found) get, a
// blocked (exit 9) or finished (exit 11) send, or a failed send
// (exit 1).
type hcDeliverySpec struct {
	recipientAddr  string
	recipientState string
	recipientExit  int
	sendCode       int
	sendStderr     string
}

type hcDeliveryFixture struct {
	root string
	sd   string
	bin  string
	env  platform.Env
	a    collaboration.Assignment
}

// hcDeliveryRecord renders one live terminal record with the given
// address, name, cwd and status; the session label is added when
// non-empty.
func hcDeliveryRecord(t *testing.T, name, addr, cwd, status, session string) string {
	t.Helper()
	cwdJSON, err := json.Marshal(cwd)
	if err != nil {
		t.Fatal(err)
	}
	label := ""
	if session != "" {
		label = `"labels":{"soho.session":"` + session + `"},`
	}
	return `{"address":"` + addr + `","name":"` + name + `","state":"live",` + label + `"cwd":` + string(cwdJSON) + `,"status":{"state":"` + status + `","turn":1}}`
}

// newHcDeliveryFixture sets up one assignment for transport=hc in root:
// the author terminal t7 (the sender) and the reviewer terminal t8 (the
// recipient) as roster rows whose session conf carries the worker-message
// policy, the assignment store record, the fake hc rules and the fake
// herdr sentinel.
func newHcDeliveryFixture(t *testing.T, spec *hcDeliverySpec) *hcDeliveryFixture {
	t.Helper()
	if spec == nil {
		spec = &hcDeliverySpec{}
	}
	if spec.recipientAddr == "" {
		spec.recipientAddr = "t8"
	}
	if spec.recipientState == "" {
		spec.recipientState = "idle"
	}
	raw := t.TempDir()
	root, err := filepath.EvalSymlinks(raw)
	if err != nil {
		t.Fatal(err)
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
	policy := "worker_messages=policy\nworker_messages.rules.request.from=role:implementer\nworker_messages.rules.request.to=role:reviewer\nworker_messages.rules.request.types=review.ready,review.question\nworker_messages.rules.request.scope=assignment\nworker_messages.rules.feedback.from=role:reviewer\nworker_messages.rules.feedback.to=role:implementer\nworker_messages.rules.feedback.types=review.finding,review.result,review.question\nworker_messages.rules.feedback.scope=assignment\n"
	if err := os.WriteFile(filepath.Join(sd, "session.conf"), []byte(policy), 0o600); err != nil {
		t.Fatal(err)
	}
	brief := filepath.Join(root, "brief.md")
	body := "# Goal\nImplement a small change.\n# Expected result\nCorrect behavior.\n# Owned files\n- src/app.go\n# Forbidden\nNo commit/push. No other files.\n# Report\nWrite the declared report.\n"
	if err := os.WriteFile(brief, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	// The sender terminal t7 (the author) carries the session label; the
	// recipient record for address t8 follows the spec.
	authorRecord := hcDeliveryRecord(t, "author", "t7", root, "working", "sX")
	reviewerRecord := hcDeliveryRecord(t, "reviewer", "t8", root, "idle", "sX")
	recipientRule := fakecli.Rule{Argv: []string{"get", "t8", "--json"}, Stdout: hcDeliveryRecord(t, "reviewer", spec.recipientAddr, root, spec.recipientState, "")}
	if spec.recipientExit != 0 {
		recipientRule = fakecli.Rule{Argv: []string{"get", "t8", "--json"}, Code: spec.recipientExit}
	}
	send := fakecli.Rule{Argv: []string{"send"}, ArgvPrefix: true, Stdout: `{"started":true,"turn":2}`}
	if spec.sendCode != 0 {
		send.Code, send.Stdout, send.Stderr = spec.sendCode, "", spec.sendStderr
	}
	rules := []fakecli.Rule{
		{Argv: []string{"get", "t7", "--json"}, Stdout: authorRecord},
		{Argv: []string{"get", "author", "--json"}, Stdout: authorRecord},
		{Argv: []string{"get", "reviewer", "--json"}, Stdout: reviewerRecord},
		recipientRule,
		send,
	}
	if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
		t.Fatal(err)
	}
	if _, err := fakecli.Install(t, bin, "herdr", nil); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{
		"PATH": bin, "HOME": home, "USERPROFILE": home, "XDG_CONFIG_HOME": conf,
		"TMPDIR": root, "SOHO_FAKECLI_CONFIG": bin,
		"SOHO_DIR": state, "SOHO_TRANSPORT": "hc", "HC_TERMINAL_ID": "t7",
	}
	projectRoot := platform.StateProjectRoot(env, root)
	// The participants carry the session stand-in the hc participant
	// resolution computes (Hash of "hc\x00<address>"), so the delivery's
	// SameParticipant recheck compares like with like.
	author := collaboration.Participant{Name: "author", Pane: "t7", Role: "implementer", Lane: "build", Kind: "codex", Model: "gpt-5.4", Family: "openai", Started: "author-start", Session: collaboration.Hash([]byte("hc\x00t7")), Cwd: root}
	reviewer := collaboration.Participant{Name: "reviewer", Pane: "t8", Role: "reviewer", Lane: "review", Kind: "claude", Model: "claude-opus-5-5", Family: "anthropic", Started: "reviewer-start", Session: collaboration.Hash([]byte("hc\x00t8")), Cwd: root}
	briefRaw, err := os.ReadFile(brief)
	if err != nil {
		t.Fatal(err)
	}
	a, err := collaboration.Store{StateDir: sd}.Create(collaboration.Assignment{Author: author, Reviewer: reviewer, Workspace: "sX", Root: projectRoot, Brief: brief, BriefHash: collaboration.Hash(briefRaw), Paths: []string{"src/app.go"}})
	if err != nil {
		t.Fatal(err)
	}
	return &hcDeliveryFixture{root: root, sd: sd, bin: bin, env: env, a: a}
}

// runReadyDelivery persists one ready event for the author (fresh
// fingerprint) and delivers it through SendAssignmentMessage as the
// author's terminal t7.
func (f *hcDeliveryFixture) runReadyDelivery(t *testing.T) (collaboration.DeliveryResult, error) {
	t.Helper()
	report := filepath.Join(f.root, "ready.md")
	if err := os.WriteFile(report, []byte("Implementation ready\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	revision, err := collaboration.Fingerprint(f.root, f.a.Paths)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := collaboration.PrepareEvent(collaboration.Store{StateDir: f.sd}, collaboration.EventRequest{AssignmentID: f.a.ID, Actor: f.a.Author, Type: "ready", Report: report, Revision: revision.Fingerprint})
	if err != nil {
		t.Fatal(err)
	}
	cfg := core.LoadConfig(f.env, f.root)
	return SendAssignmentMessage(AssignmentMessageOptions{
		AssignmentID: f.a.ID,
		Target:       "reviewer",
		Type:         "review.ready",
		Body:         "Implementation ready",
		EventID:      prepared.Event.ID,
		TimeoutMS:    10000,
		Config:       &cfg,
		Env:          f.env,
		Cwd:          f.root,
	})
}

// hcSendCalls returns the recorded hc send invocations.
func (f *hcDeliveryFixture) hcSendCalls(t *testing.T) []fakecli.Call {
	t.Helper()
	calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "hc.calls.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var sends []fakecli.Call
	for _, c := range calls {
		if len(c.Argv) > 0 && c.Argv[0] == "send" {
			sends = append(sends, c)
		}
	}
	return sends
}

// assertNoHerdrCall fails when the fake herdr sentinel recorded a call:
// under transport=hc the delivery must reach the terminals only through
// hc.
func (f *hcDeliveryFixture) assertNoHerdrCall(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(f.bin, "herdr.calls.jsonl")); err == nil {
		calls, _ := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
		t.Fatalf("herdr was called: %#v", calls)
	}
}

// TestPeerHcDeliverySubmitsQueuedSend: a live recipient is submitted with
// exactly one queued hc send (no --if guard) to its address, and the
// delivery is submitted.
func TestPeerHcDeliverySubmitsQueuedSend(t *testing.T) {
	f := newHcDeliveryFixture(t, nil)
	result, err := f.runReadyDelivery(t)
	if err != nil || result.Status != "submitted" {
		t.Fatalf("result=%+v err=%v, want submitted", result, err)
	}
	sends := f.hcSendCalls(t)
	if len(sends) != 1 {
		t.Fatalf("hc send calls = %d: %#v, want exactly one", len(sends), sends)
	}
	argv := sends[0].Argv
	if len(argv) != 5 || argv[0] != "send" || argv[1] != "--json" || argv[2] != "--" || argv[3] != "t8" {
		t.Fatalf("send argv=%q, want send --json -- t8 <message>", argv)
	}
	for _, a := range argv {
		if a == "--if" {
			t.Fatalf("send argv=%q carries the --if guard, want the queued send", argv)
		}
	}
	if !strings.Contains(argv[4], "Assignment: "+f.a.ID) {
		t.Fatalf("send message=%q, want the assignment metadata", argv[4])
	}
	f.assertNoHerdrCall(t)
}

// TestPeerHcDeliveryBlockedRecipientIsRefusedWithoutSend: a recipient
// blocked on a person's prompt refuses the delivery with nothing sent.
func TestPeerHcDeliveryBlockedRecipientIsRefusedWithoutSend(t *testing.T) {
	f := newHcDeliveryFixture(t, &hcDeliverySpec{recipientState: "blocked"})
	result, err := f.runReadyDelivery(t)
	if err == nil || result.Status != "refused" || result.Cause != "recipient is blocked on a prompt; nothing was sent" {
		t.Fatalf("result=%+v err=%v, want the blocked-recipient refusal", result, err)
	}
	if sends := f.hcSendCalls(t); len(sends) != 0 {
		t.Fatalf("hc sent %#v, want nothing sent", sends)
	}
	f.assertNoHerdrCall(t)
}

// TestPeerHcDeliveryAddressMismatchIsRefused: a recipient record whose
// address no longer matches the registered pane takes the
// changed-or-unavailable refusal with nothing sent.
func TestPeerHcDeliveryAddressMismatchIsRefused(t *testing.T) {
	f := newHcDeliveryFixture(t, &hcDeliverySpec{recipientAddr: "t9"})
	result, err := f.runReadyDelivery(t)
	if err == nil || result.Status != "refused" || result.Cause != "recipient changed or became unavailable" {
		t.Fatalf("result=%+v err=%v, want the changed-or-unavailable refusal", result, err)
	}
	if sends := f.hcSendCalls(t); len(sends) != 0 {
		t.Fatalf("hc sent %#v, want nothing sent", sends)
	}
	f.assertNoHerdrCall(t)
}

// TestPeerHcDeliveryMissingRecipientIsRefused: a recipient whose terminal
// is gone (the hc not-found exit) takes the changed-or-unavailable refusal
// with nothing sent.
func TestPeerHcDeliveryMissingRecipientIsRefused(t *testing.T) {
	f := newHcDeliveryFixture(t, &hcDeliverySpec{recipientExit: 3})
	result, err := f.runReadyDelivery(t)
	if err == nil || result.Status != "refused" || result.Cause != "recipient changed or became unavailable" {
		t.Fatalf("result=%+v err=%v, want the changed-or-unavailable refusal", result, err)
	}
	if sends := f.hcSendCalls(t); len(sends) != 0 {
		t.Fatalf("hc sent %#v, want nothing sent", sends)
	}
	f.assertNoHerdrCall(t)
}

// TestPeerHcDeliveryBlockedSendIsRefused: a send that exits the hc
// blocked exit (9) is refused with the failure's message.
func TestPeerHcDeliveryBlockedSendIsRefused(t *testing.T) {
	f := newHcDeliveryFixture(t, &hcDeliverySpec{sendCode: 9})
	result, err := f.runReadyDelivery(t)
	if err == nil || result.Status != "refused" || result.Cause != "worker t8 is blocked: a person must answer its prompt" {
		t.Fatalf("result=%+v err=%v, want the blocked-send refusal", result, err)
	}
	if sends := f.hcSendCalls(t); len(sends) != 1 {
		t.Fatalf("hc send calls = %d: %#v, want the one attempted send", len(sends), sends)
	}
	f.assertNoHerdrCall(t)
}

// TestPeerHcDeliveryFinishedSendIsRefused: a send that exits the hc
// finished exit (11, the recipient no longer exists) is refused.
func TestPeerHcDeliveryFinishedSendIsRefused(t *testing.T) {
	f := newHcDeliveryFixture(t, &hcDeliverySpec{sendCode: 11})
	result, err := f.runReadyDelivery(t)
	if err == nil || result.Status != "refused" {
		t.Fatalf("result=%+v err=%v, want the finished-send refusal", result, err)
	}
	if !strings.Contains(result.Cause, "hc exited 11") {
		t.Fatalf("result=%+v, want the hc failure message", result)
	}
	f.assertNoHerdrCall(t)
}

// TestPeerHcDeliveryFailedSendIsUncertain: a send that fails otherwise
// (exit 1) is uncertain with the failure's message.
func TestPeerHcDeliveryFailedSendIsUncertain(t *testing.T) {
	f := newHcDeliveryFixture(t, &hcDeliverySpec{sendCode: 1, sendStderr: "connection disappeared"})
	result, err := f.runReadyDelivery(t)
	if err == nil || result.Status != "uncertain" {
		t.Fatalf("result=%+v err=%v, want the uncertain send", result, err)
	}
	if result.Cause != "hc exited 1: connection disappeared" {
		t.Fatalf("result=%+v, want the hc failure message", result)
	}
	f.assertNoHerdrCall(t)
}
