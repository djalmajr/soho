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
// path: a different address, a different cwd, a finished terminal or a
// failed (not-found) get.
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
