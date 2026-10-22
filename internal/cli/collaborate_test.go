package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/collaboration"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func collaborationFixture(t *testing.T, uncertain bool, sameFamily ...bool) (*copiesFixture, string) {
	t.Helper()
	f := newCopiesFixture(t, nil)
	jsonAgent := func(name, pane, kind string) string {
		return fmt.Sprintf(`{"result":{"agent":{"name":%q,"pane_id":%q,"agent":%q,"workspace_id":"ws","cwd":%q,"agent_status":"working","agent_session":{"kind":"id","value":%q}}}}`, name, pane, kind, f.repo, name+"-session")
	}
	reviewerKind, reviewerModel, reviewerFamily := "claude", "claude-opus-5-5", "anthropic"
	if len(sameFamily) > 0 && sameFamily[0] {
		reviewerKind, reviewerModel, reviewerFamily = "codex", "gpt-5.4", "openai"
	}
	rules := []fakecli.Rule{}
	for _, agent := range []struct{ name, pane, kind string }{{"author", "ws:p1", "codex"}, {"reviewer", "ws:p2", reviewerKind}, {"orchestrator", "ws:p0", "codex"}} {
		for _, id := range []string{agent.name, agent.pane} {
			rules = append(rules, fakecli.Rule{Argv: []string{"agent", "get", id}, Stdout: jsonAgent(agent.name, agent.pane, agent.kind)})
		}
	}
	rules = append(rules, fakecli.Rule{Argv: []string{"agent", "read"}, ArgvPrefix: true, Stdout: "Worker is working\n"})
	prompt := fakecli.Rule{Argv: []string{"agent", "prompt"}, ArgvPrefix: true}
	if uncertain {
		prompt.Code, prompt.Stderr = 1, "connection disappeared"
	}
	rules = append(rules, prompt)
	fakeDir := t.TempDir()
	if _, err := fakecli.Install(t, fakeDir, "herdr", rules); err != nil {
		t.Fatal(err)
	}
	f.env["SOHO_FAKECLI_CONFIG"] = fakeDir
	f.env["PATH"] = fakeDir + string(os.PathListSeparator) + f.env.Get("PATH")
	f.env["HERDR_PANE_ID"] = "ws:p0"
	ctx := core.LoadConfig(f.env, f.repo)
	sd := core.StateDir(&ctx, f.env, f.repo)
	core.RosterAppend(sd, []string{"author", "ws:p1", "codex", "implementer", "openai", "0", f.repo, "author-start", "gpt-5.4", "ask", "implementer", "build"})
	core.RosterAppend(sd, []string{"reviewer", "ws:p2", reviewerKind, "reviewer", reviewerFamily, "0", f.repo, "reviewer-start", reviewerModel, "ask", "reviewer", "review"})
	policy := "worker_messages=policy\nworker_messages.rules.request.from=role:implementer\nworker_messages.rules.request.to=role:reviewer\nworker_messages.rules.request.types=review.ready,review.question\nworker_messages.rules.request.scope=assignment\nworker_messages.rules.feedback.from=role:reviewer\nworker_messages.rules.feedback.to=role:implementer\nworker_messages.rules.feedback.types=review.finding,review.result,review.question\nworker_messages.rules.feedback.scope=assignment\n"
	if err := os.WriteFile(core.SessionConfPath(&ctx, f.env, f.repo), []byte(policy), 0600); err != nil {
		t.Fatal(err)
	}
	brief := filepath.Join(f.root, "brief.md")
	body := "# Goal\nImplement a small change.\n# Expected result\nCorrect behavior.\n# Owned files\n- src/app.go\n# Forbidden\nNo commit/push. No other files.\n# Report\nWrite the declared report.\n"
	if err := os.WriteFile(brief, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return f, brief
}

func startCollaboration(t *testing.T, f *copiesFixture, brief string) collaboration.Assignment {
	t.Helper()
	code, out, stderr := f.run(t, f.env, "collaborate", "start", "author", "reviewer", "--brief", brief)
	var a collaboration.Assignment
	if code != 0 || json.Unmarshal([]byte(out), &a) != nil || a.ID == "" {
		t.Fatalf("start code=%d out=%s err=%s", code, out, stderr)
	}
	return a
}

func cliEvent(t *testing.T, f *copiesFixture, a collaboration.Assignment, pane, event, body string) collaboration.Assignment {
	t.Helper()
	report := filepath.Join(f.root, event+".md")
	if err := os.WriteFile(report, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	revision, err := collaboration.Fingerprint(a.Author.Cwd, a.Paths)
	if err != nil {
		t.Fatal(err)
	}
	env := f.env.Clone()
	env["HERDR_PANE_ID"] = pane
	code, out, stderr := f.run(t, env, "collaborate", "event", a.ID, event, "--report", report, "--revision", revision.Fingerprint)
	if code != 0 || json.Unmarshal([]byte(out), &a) != nil {
		t.Fatalf("event %s code=%d out=%s err=%s", event, code, out, stderr)
	}
	return a
}

func TestCollaborateCLIFullCycleAndAuthority(t *testing.T) {
	f, brief := collaborationFixture(t, false)
	a := startCollaboration(t, f, brief)
	a = cliEvent(t, f, a, "ws:p1", "ready", "Implementation ready\n")
	if a.Phase != collaboration.Reviewing || a.Events[0].Delivery != "submitted" {
		t.Fatalf("ready: %+v", a)
	}
	a = cliEvent(t, f, a, "ws:p2", "findings", "findings: 1 (P0 0, P1 0, P2 1, P3 0) | verdict: fail\nFix it.\n")
	if a.Phase != collaboration.Fixing {
		t.Fatalf("findings: %+v", a)
	}
	if err := os.MkdirAll(filepath.Join(f.repo, "src"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.repo, "src", "app.go"), []byte("fixed"), 0600); err != nil {
		t.Fatal(err)
	}
	a = cliEvent(t, f, a, "ws:p1", "corrected", "Corrected and tested\n")
	a = cliEvent(t, f, a, "ws:p2", "approved", "findings: 0 (P0 0, P1 0, P2 0, P3 0) | verdict: pass\nVerified snapshot.\n")
	if a.Phase != collaboration.AwaitingOrchestrator || a.Round != 1 {
		t.Fatalf("approval: %+v", a)
	}
	worker := f.env.Clone()
	worker["HERDR_PANE_ID"] = "ws:p2"
	if code, _, _ := f.run(t, worker, "collaborate", "finalize", a.ID, "--verdict", "accept"); code != 2 {
		t.Fatal("worker finalized collaboration")
	}
	code, out, stderr := f.run(t, f.env, "collaborate", "finalize", a.ID, "--verdict", "accept")
	if code != 0 || json.Unmarshal([]byte(out), &a) != nil || a.Phase != collaboration.Finished {
		t.Fatalf("finalize code=%d out=%s err=%s", code, out, stderr)
	}
}

func TestCollaborateCLIRejectsStaleVersionAndClosesExplicitly(t *testing.T) {
	f, brief := collaborationFixture(t, false)
	a := cliEvent(t, f, startCollaboration(t, f, brief), "ws:p1", "ready", "Ready\n")
	a = cliEvent(t, f, a, "ws:p2", "approved", "findings: 0 (P0 0, P1 0, P2 0, P3 0) | verdict: pass\n")
	if err := os.MkdirAll(filepath.Join(f.repo, "src"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.repo, "src", "app.go"), []byte("changed after approval"), 0600); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := f.run(t, f.env, "collaborate", "finalize", a.ID, "--verdict", "accept"); code != 2 || !strings.Contains(errOut, "stale") {
		t.Fatalf("stale accepted: code=%d err=%s", code, errOut)
	}
	f.env["SOHO_WORKER_MESSAGES"] = "off"
	if code, _, stderr := f.run(t, f.env, "collaborate", "stop", a.ID); code != 0 {
		t.Fatalf("stop disabled policy: %d %s", code, stderr)
	}
	if code, _, stderr := f.run(t, f.env, "collaborate", "finalize", a.ID, "--verdict", "reject"); code != 0 {
		t.Fatalf("reject: %d %s", code, stderr)
	}
}

func TestCollaborateCLIUncertainEventIsRecordedAndNeverResent(t *testing.T) {
	f, brief := collaborationFixture(t, true)
	a := startCollaboration(t, f, brief)
	r, err := collaboration.Fingerprint(a.Author.Cwd, a.Paths)
	if err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(f.root, "ready.md")
	if err := os.WriteFile(report, []byte("Ready"), 0600); err != nil {
		t.Fatal(err)
	}
	env := f.env.Clone()
	env["HERDR_PANE_ID"] = "ws:p1"
	args := []string{"collaborate", "event", a.ID, "ready", "--report", report, "--revision", r.Fingerprint}
	code, out, stderr := f.run(t, env, args...)
	if code != 15 || json.Unmarshal([]byte(out), &a) != nil || a.Phase != collaboration.Escalated || a.Events[0].Delivery != "uncertain" {
		t.Fatalf("code=%d out=%s err=%s", code, out, stderr)
	}
	if code, _, stderr := f.run(t, env, args...); code != 0 {
		t.Fatalf("idempotent read code=%d %s", code, stderr)
	}
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(env.Get("SOHO_FAKECLI_CONFIG"), "herdr.json"))
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, c := range calls {
		if len(c.Argv) > 1 && c.Argv[0] == "agent" && c.Argv[1] == "prompt" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("event submitted %d times", count)
	}
}

func TestCollaborateCLIStatusNowriteAndWorkerStartRefusal(t *testing.T) {
	f, brief := collaborationFixture(t, false)
	worker := f.env.Clone()
	worker["HERDR_PANE_ID"] = "ws:p1"
	if code, _, _ := f.run(t, worker, "collaborate", "start", "author", "reviewer", "--brief", brief); code != 2 {
		t.Fatal("worker started assignment")
	}
	a := startCollaboration(t, f, brief)
	env := f.env.Clone()
	env["SOHO_NOWRITE"] = "1"
	env["SOHO_WORKER_MESSAGES"] = "off"
	before := nowriteTreeSnapshot(t, f.root)
	code, out, stderr := f.run(t, env, "collaborate", "status", a.ID, "--json")
	if code != 0 || !strings.Contains(out, "current_revision") || !strings.Contains(out, "worker_messages=off") || stderr != "" {
		t.Fatalf("status %d %s %s", code, out, stderr)
	}
	if !reflect.DeepEqual(before, nowriteTreeSnapshot(t, f.root)) {
		t.Fatal("status wrote files")
	}
	if code, _, _ := f.run(t, env, "collaborate", "stop", a.ID); code != 2 {
		t.Fatal("NOWRITE stopped assignment")
	}
	if code, out, stderr := f.run(t, platform.Env{"SOHO_NOWRITE": "1"}, "capabilities", "--json"); code != 0 || !strings.Contains(out, `"worker_collaboration":1`) || stderr != "" {
		t.Fatalf("capabilities: %d %s %s", code, out, stderr)
	}
}

func TestCollaborateCLIFinalRoundFindingsAreDeliveredBeforeEscalation(t *testing.T) {
	f, brief := collaborationFixture(t, false)
	code, out, stderr := f.run(t, f.env, "collaborate", "start", "author", "reviewer", "--brief", brief, "--max-rounds", "1")
	var a collaboration.Assignment
	if code != 0 || json.Unmarshal([]byte(out), &a) != nil {
		t.Fatalf("start: %d %s", code, stderr)
	}
	a = cliEvent(t, f, a, "ws:p1", "ready", "Ready")
	a = cliEvent(t, f, a, "ws:p2", "findings", "findings: 1 (P0 0, P1 0, P2 1, P3 0) | verdict: fail\nFix")
	a = cliEvent(t, f, a, "ws:p1", "corrected", "Corrected")
	a = cliEvent(t, f, a, "ws:p2", "findings", "findings: 1 (P0 0, P1 0, P2 1, P3 0) | verdict: fail\nStill open")
	last := a.Events[len(a.Events)-1]
	if a.Phase != collaboration.Escalated || last.Delivery != "submitted" {
		t.Fatalf("final findings not delivered: %+v", a)
	}
}

func TestCollaborateCLIAcceptsSameModelFamily(t *testing.T) {
	f, brief := collaborationFixture(t, false, true)
	a := startCollaboration(t, f, brief)
	if a.Author.Family != "openai" || a.Reviewer.Family != "openai" {
		t.Fatalf("families: %+v %+v", a.Author, a.Reviewer)
	}
}
