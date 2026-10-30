package peer_test

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/collaboration"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/peer"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func workerAgent(name, pane, kind, cwd, session string) string {
	return fmt.Sprintf(`{"result":{"agent":{"name":%q,"pane_id":%q,"agent":%q,"workspace_id":"ws-test","cwd":%q,"agent_status":"working","agent_session":{"kind":"id","value":%q}}}}`, name, pane, kind, cwd, session)
}

func assignedFixture(t *testing.T, additional []fakecli.Rule) (*fixture, collaboration.Store, collaboration.Assignment) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	skill, _ := filepath.Abs(filepath.Join("..", "..", "skills", "soho"))
	rules := append([]fakecli.Rule{}, additional...)
	rules = append(rules,
		fakecli.Rule{Argv: []string{"agent", "get", "author"}, Stdout: workerAgent("author", "ws:p1", "codex", dir, "a1")},
		fakecli.Rule{Argv: []string{"agent", "get", "reviewer"}, Stdout: workerAgent("reviewer", "ws:p2", "claude", dir, "r1")},
		fakecli.Rule{Argv: []string{"agent", "get", "ws:p2"}, Stdout: workerAgent("reviewer", "ws:p2", "claude", dir, "r1")},
		fakecli.Rule{Argv: []string{"agent", "get", "ws:p1"}, Stdout: workerAgent("author", "ws:p1", "codex", dir, "a1")},
		fakecli.Rule{Argv: []string{"agent", "read"}, ArgvPrefix: true, Stdout: "Worker is editing files\n"},
		fakecli.Rule{Argv: []string{"agent", "prompt"}, ArgvPrefix: true},
	)
	f := newFixtureAt(t, dir, rules)
	f.env["SOHO_SKILL_DIR"] = skill
	f.env["HERDR_PANE_ID"] = "ws:p1"
	t.Chdir(dir)
	ctx := core.LoadConfig(f.env, dir)
	sd := core.StateDirPath(&ctx, f.env, dir)
	if err := os.MkdirAll(sd, 0700); err != nil {
		t.Fatal(err)
	}
	roster := fmt.Sprintf("author\tws:p1\tcodex\timplementer\topenai\t\t%s\tfirst\tgpt-5.4\thigh\tmain\tbuild\nreviewer\tws:p2\tclaude\treviewer\tanthropic\t\t%s\tsecond\tclaude-opus-5-5\tmedium\tmain\treview\n", dir, dir)
	if err := os.WriteFile(filepath.Join(sd, "agents.tsv"), []byte(roster), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(core.SessionConfPath(&ctx, f.env, dir), []byte("worker_messages=policy\nworker_messages.rules.request.from=role:implementer\nworker_messages.rules.request.to=role:reviewer\nworker_messages.rules.request.types=review.ready,review.question\nworker_messages.rules.request.scope=assignment\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx = core.LoadConfig(f.env, dir)
	author, err := collaboration.ResolveParticipant(sd, "author", f.env)
	if err != nil {
		t.Fatal(err)
	}
	reviewer, err := collaboration.ResolveParticipant(sd, "reviewer", f.env)
	if err != nil {
		t.Fatal(err)
	}
	s := collaboration.Store{StateDir: sd}
	a, err := s.Create(collaboration.Assignment{Author: author, Reviewer: reviewer, Workspace: "ws-test", Root: dir, BriefHash: "brief", Paths: []string{"file.go"}})
	if err != nil {
		t.Fatal(err)
	}
	return f, s, a
}

func assignmentPrompts(t *testing.T, f *fixture) []fakecli.Call {
	t.Helper()
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(f.env.Get("SOHO_FAKECLI_CONFIG"), "herdr.json"))
	if err != nil {
		t.Fatal(err)
	}
	var prompts []fakecli.Call
	for _, call := range calls {
		if len(call.Argv) >= 2 && call.Argv[0] == "agent" && call.Argv[1] == "prompt" {
			prompts = append(prompts, call)
		}
		if len(call.Argv) >= 2 && call.Argv[0] == "agent" && (call.Argv[1] == "wait" || call.Argv[1] == "keys") {
			t.Fatalf("worker transport waited or pressed keys: %v", call.Argv)
		}
	}
	return prompts
}

func TestAssignedSendSubmitsOnceToWorkingRecipient(t *testing.T) {
	f, s, a := assignedFixture(t, nil)
	body := "First line with \"quotes\" and \\path\nSegunda linha Ω\n\nLast line"
	code, out, stderr := f.run([]string{"send", "reviewer", body, "--assignment", a.ID, "--type", "review.question"})
	if code != 0 || !strings.Contains(out, `"status":"submitted"`) {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, stderr)
	}
	prompts := assignmentPrompts(t, f)
	if len(prompts) != 1 || len(prompts[0].Argv) != 4 || prompts[0].Argv[2] != a.Reviewer.Pane || !strings.Contains(prompts[0].Argv[3], "Assignment: "+a.ID) {
		t.Fatalf("unexpected delivery: %+v", prompts)
	}
	message := prompts[0].Argv[3]
	headerID := strings.TrimSuffix(strings.TrimPrefix(strings.SplitN(message, "\n", 2)[0], "[soho:peer:"), "]")
	// The closing line is the tag with the slash and the id inside it;
	// the assignment message keeps its header, metadata and quoted body.
	wantTail := "> First line with \"quotes\" and \\path\n> Segunda linha Ω\n>\n> Last line\n[/soho:peer:" + headerID + "]"
	if !strings.HasSuffix(message, wantTail) {
		t.Fatalf("multiline body or closing marker was changed: %q", message)
	}
	log, err := os.ReadFile(filepath.Join(s.StateDir, "peer-messages.tsv"))
	if err != nil || !strings.Contains(string(log), fmt.Sprintf("\t%s\treview.question\t%d", a.ID, a.Round)) {
		t.Fatalf("metadata missing: %s %v", log, err)
	}
	current, _ := s.Read(a.ID)
	if current.Phase != collaboration.Preparing || len(current.Events) != 0 {
		t.Fatal("free text advanced the cycle")
	}
}

func TestAssignedSendRefusesBeforePrompt(t *testing.T) {
	for _, kind := range []string{"off", "inbound", "wrong-target", "wrong-type", "outsider", "session-reused", "dialog", "malformed-state", "nowrite"} {
		t.Run(kind, func(t *testing.T) {
			extra := []fakecli.Rule{}
			if kind == "dialog" {
				extra = append(extra, fakecli.Rule{Argv: []string{"agent", "read"}, ArgvPrefix: true, Stdout: "No, exit\nYes, I trust this folder\nEnter to confirm · Esc to cancel\n"})
			}
			f, s, a := assignedFixture(t, extra)
			target, typ := "reviewer", "review.question"
			if kind == "off" {
				f.env["SOHO_WORKER_MESSAGES"] = "off"
			}
			if kind == "inbound" {
				file := filepath.Join(f.dir, ".agents", "soho.conf")
				if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, []byte("inbound=off\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "wrong-target" {
				target = "local/ws:p3"
			}
			if kind == "wrong-type" {
				typ = "review.result"
			}
			if kind == "outsider" {
				f.env["HERDR_PANE_ID"] = "ws:p9"
			}
			if kind == "nowrite" {
				f.env["SOHO_NOWRITE"] = "1"
			}
			if kind == "session-reused" {
				_, err := s.Update(a.ID, a.Generation, func(current *collaboration.Assignment) error { current.Reviewer.Session = "old-session"; return nil })
				if err != nil {
					t.Fatal(err)
				}
			}
			if kind == "malformed-state" {
				file, _ := s.Path(a.ID)
				if err := os.WriteFile(file, []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			code, _, stderr := f.run([]string{"send", target, "question", "--assignment", a.ID, "--type", typ})
			if code == 0 || stderr == "" || len(assignmentPrompts(t, f)) != 0 {
				t.Fatalf("unsafe send accepted: code=%d stderr=%s", code, stderr)
			}
		})
	}
}

func TestAssignedSendUncertainDoesNotRetry(t *testing.T) {
	f, _, a := assignedFixture(t, []fakecli.Rule{{Argv: []string{"agent", "prompt"}, ArgvPrefix: true, Code: 1, Stderr: "connection disappeared"}})
	code, _, stderr := f.run([]string{"send", "reviewer", "question", "--assignment", a.ID, "--type", "review.question"})
	if code != 15 || len(assignmentPrompts(t, f)) != 1 {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
}

func TestAssignedSendCannotImpersonateVersionedEvents(t *testing.T) {
	for _, typ := range []string{"review.ready", "review.finding", "review.result"} {
		t.Run(typ, func(t *testing.T) {
			f, _, a := assignedFixture(t, nil)
			code, _, stderr := f.run([]string{"send", "reviewer", "Pretend event", "--assignment", a.ID, "--type", typ})
			if code != 2 || !strings.Contains(stderr, "collaborate event") || len(assignmentPrompts(t, f)) != 0 {
				t.Fatalf("unversioned event delivered: %d %s", code, stderr)
			}
		})
	}
}

func TestAssignedSendBudgetIncludesLiveIdentityPreflight(t *testing.T) {
	f, _, a := assignedFixture(t, []fakecli.Rule{{Argv: []string{"agent", "get", "author"}, Call: 2, Delay: 2000, Stdout: "{}"}})
	started := time.Now()
	code, _, stderr := f.run([]string{"send", "reviewer", "question", "--assignment", a.ID, "--type", "review.question", "--timeout", "100"})
	if code == 0 || time.Since(started) >= time.Second || len(assignmentPrompts(t, f)) != 0 {
		t.Fatalf("identity escaped the submission budget: code=%d elapsed=%s err=%s", code, time.Since(started), stderr)
	}
}

func TestAssignedSendRefusesUnreadableFreshDestinationPolicy(t *testing.T) {
	f, _, a := assignedFixture(t, nil)
	config := core.LoadConfig(f.env, f.dir)
	// Origin was read successfully. A destination policy becomes unreadable
	// before sending, so defaults cannot silently replace its inbound setting.
	file := filepath.Join(f.env.Get("XDG_CONFIG_HOME"), "soho", "config")
	if err := os.MkdirAll(file, 0700); err != nil {
		t.Fatal(err)
	}
	result, err := peer.SendAssignmentMessage(peer.AssignmentMessageOptions{AssignmentID: a.ID, Target: "reviewer", Type: "review.question", Body: "question", Config: &config, Env: f.env, Cwd: f.dir})
	if err == nil || result.Status != "refused" || !strings.Contains(result.Cause, "inbound configuration is unreadable") || len(assignmentPrompts(t, f)) != 0 {
		t.Fatalf("destination error silently became auto: %+v %v", result, err)
	}
}

func TestAssignedSendRefusesWindowsBatchLauncher(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows batch argument transport")
	}
	f, _, a := assignedFixture(t, nil)
	marker := filepath.Join(f.dir, "batch-was-run")
	batch := filepath.Join(filepath.Dir(f.bin), "herdr.cmd")
	if err := os.WriteFile(batch, []byte("@echo called>\""+marker+"\"\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f.env["PATHEXT"] = ".CMD"
	config := core.LoadConfig(f.env, f.dir)
	result, err := peer.SendAssignmentMessage(peer.AssignmentMessageOptions{AssignmentID: a.ID, Target: "reviewer", Type: "review.question", Body: "first\nsecond", Config: &config, Env: f.env, Cwd: f.dir})
	if err == nil || result.Status != "refused" || !strings.Contains(result.Cause, "native Herdr executable") {
		t.Fatalf("unsafe batch transport was accepted: %+v %v", result, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("batch launcher ran: %v", err)
	}
	if len(assignmentPrompts(t, f)) != 0 {
		t.Fatal("a prompt was delivered after refusing the batch launcher")
	}
}
