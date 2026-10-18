package peer_test

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/djalmajr/soho/internal/peer"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// rosterSender writes the fixture's roster so that the sender's pane carries
// the given role (role "" writes no roster line at all).
func rosterSender(t *testing.T, f *fixture, name, role string) {
	t.Helper()
	stateDir := filepath.Join(f.env["SOHO_DIR"], "ws-test")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if role == "" {
		return
	}
	line := name + "\tw0test:p0a\tgrok\t" + role + "\n"
	if err := os.WriteFile(filepath.Join(stateDir, "agents.tsv"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSendWorkerLookupFailureOrIncompleteRegistrationNeverBypassesGuard(t *testing.T) {
	for _, incomplete := range []bool{false, true} {
		t.Run(fmt.Sprint(incomplete), func(t *testing.T) {
			f := newFixture(t, []fakecli.Rule{{Argv: []string{"agent", "get", "w0test:p0a"}, Code: 1, Stderr: "unavailable"}})
			f.env["HERDR_PANE_ID"] = "w0test:p0a"
			rosterSender(t, f, "worker", "implementer")
			if incomplete {
				if err := os.WriteFile(filepath.Join(f.env["SOHO_DIR"], "ws-test", "agents.tsv"), []byte("worker\tw0test:p0a\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			code, _, _ := f.run([]string{"send", "target", "hello"})
			if code != 2 {
				t.Fatalf("unverified worker was allowed: %d", code)
			}
			calls, err := fakecli.ReadCallsForConfig(filepath.Join(f.env["SOHO_FAKECLI_CONFIG"], "herdr.json"))
			if err != nil {
				t.Fatal(err)
			}
			for _, call := range calls {
				if len(call.Argv) > 1 && (call.Argv[1] == "prompt" || (call.Argv[1] == "get" && call.Argv[2] == "target")) {
					t.Fatalf("target was reached: %v", call.Argv)
				}
			}
		})
	}
}

// paneSenderRules is the rule set of a successful local send whose sender
// pane is w0test:p0a: get Call 1 is the guard's sender identity lookup, and
// Calls 2–5 follow the working-target flow (resolveTarget, screenGet,
// preGet, arrival proof).
func paneSenderRules(t *testing.T, name, prompt string) []fakecli.Rule {
	t.Helper()
	nameJSON := fmt.Sprintf(`{"result":{"agent":{"name":%q,"agent":"pi","agent_status":"idle","state_change_seq":"1"}}}`, name)
	rules := []fakecli.Rule{{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 1, Stdout: nameJSON}}
	rules = append(rules, sendRulesWithScreen(prompt, "before prompt\n", []fakecli.Rule{
		{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 2, Stdout: agentJSON("working", "1")},
		{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 3, Stdout: agentJSON("idle", "1")},
		{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 4, Stdout: agentJSON("idle", "1")},
		{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 5, Stdout: agentJSON("working", "2")},
	})...)
	rules = append(rules, fakecli.Rule{Argv: []string{"agent", "wait", "w0test:p0a", "--until", "idle", "--until", "done", "--timeout", "600000"}})
	return rules
}

func TestSendRefusesTheWorkerSender(t *testing.T) {
	t.Run("a rostered worker (role implementer) is refused before any send", func(t *testing.T) {
		f := newFixture(t, []fakecli.Rule{{Argv: []string{"agent", "get", "w0test:p0a"}, Stdout: `{"result":{"agent":{"name":"worker-b","agent":"pi","agent_status":"idle","state_change_seq":"1"}}}`}})
		f.env["HERDR_PANE_ID"] = "w0test:p0a"
		rosterSender(t, f, "worker-b", "implementer")
		code, _, stderr := f.run([]string{"send", "w0test:p0b", "got the brief"})
		if code != 2 {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		want := "soho: send: 'worker-b' is a worker of this team (role implementer): a worker reports through its report file, not by message (nothing was sent)\n"
		if stderr != want {
			t.Fatalf("stderr=%q want %q", stderr, want)
		}
		// The first herdr call is the caller-pane check's process-info read
		// (DJA-394: the fake has no rule for it, so it reads unknown and the
		// check passes silently); the second is the sender identity lookup.
		// No target resolution, no prompt, no screen read.
		calls, err := fakecli.ReadCallsForConfig(filepath.Join(filepath.Dir(f.bin), "herdr.json"))
		if err != nil {
			t.Fatal(err)
		}
		if len(calls) != 2 {
			t.Fatalf("calls=%#v", calls)
		}
		if got := calls[0]; len(got.Argv) != 4 || got.Argv[1] != "process-info" || got.Argv[3] != "w0test:p0a" {
			t.Fatalf("call=%#v", got)
		}
		if got := calls[1]; len(got.Argv) != 3 || got.Argv[1] != "get" || got.Argv[2] != "w0test:p0a" {
			t.Fatalf("call=%#v", got)
		}
	})
	t.Run("a name known to herdr but absent from the roster is not a worker", func(t *testing.T) {
		t.Setenv("SOHO_SEND_WINDOW_MS", "1")
		t.Setenv("SOHO_SEND_POLL_MS", "1")
		id := "01020304"
		oldReader := rand.Reader
		rand.Reader = bytes.NewReader([]byte{1, 2, 3, 4})
		t.Cleanup(func() { rand.Reader = oldReader })
		prompt := peer.PeerHeader("local/w0test:p0a", "worker-c", "pi", "-", id) + "\n\n> got the brief\n" + peer.PeerEndLine(id)
		f := newFixture(t, paneSenderRules(t, "worker-c", prompt))
		f.env["HERDR_PANE_ID"] = "w0test:p0a"
		rosterSender(t, f, "worker-c", "")
		code, out, stderr := f.run([]string{"send", "w0test:p0a", "got the brief"})
		if code != 0 || out != "sent to local/w0test:p0a\n" || stderr != "" {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
	})
}

func TestSendKeepsTheNonWorkerRoles(t *testing.T) {
	// The orchestrator (role -) and a sub-orchestrator keep sending: the
	// refusal only targets rostered workers.
	for _, tc := range []struct{ role string }{{"-"}, {"sub-orchestrator"}} {
		t.Run("role "+tc.role, func(t *testing.T) {
			t.Setenv("SOHO_SEND_WINDOW_MS", "1")
			t.Setenv("SOHO_SEND_POLL_MS", "1")
			id := "01020304"
			oldReader := rand.Reader
			rand.Reader = bytes.NewReader([]byte{1, 2, 3, 4})
			t.Cleanup(func() { rand.Reader = oldReader })
			prompt := peer.PeerHeader("local/w0test:p0a", "orchestrator-a", "pi", tc.role, id) + "\n\n> go\n" + peer.PeerEndLine(id)
			f := newFixture(t, paneSenderRules(t, "orchestrator-a", prompt))
			f.env["HERDR_PANE_ID"] = "w0test:p0a"
			rosterSender(t, f, "orchestrator-a", tc.role)
			code, out, stderr := f.run([]string{"send", "w0test:p0a", "go"})
			if code != 0 || out != "sent to local/w0test:p0a\n" || stderr != "" {
				t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
			}
			calls, err := fakecli.ReadCallsForConfig(filepath.Join(filepath.Dir(f.bin), "herdr.json"))
			if err != nil {
				t.Fatal(err)
			}
			prompted := false
			for _, call := range calls {
				if len(call.Argv) > 3 && call.Argv[1] == "prompt" {
					prompted = true
					if call.Argv[3] != prompt {
						t.Fatalf("prompt=%q want %q", call.Argv[3], prompt)
					}
				}
			}
			if !prompted {
				t.Fatalf("prompt call missing: %#v", calls)
			}
		})
	}
	t.Run("no HERDR_PANE_ID: the send proceeds as today", func(t *testing.T) {
		id := "01020304"
		oldReader := rand.Reader
		rand.Reader = bytes.NewReader([]byte{1, 2, 3, 4})
		t.Cleanup(func() { rand.Reader = oldReader })
		prompt := peer.PeerHeader("local/-", "-", "-", "-", id) + "\n\n> go\n" + peer.PeerEndLine(id)
		f := newFixture(t, sendRules(prompt, []fakecli.Rule{
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 2, Stdout: agentJSON("idle", "1")},
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 3, Stdout: agentJSON("idle", "1")},
			{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 4, Stdout: agentJSON("working", "2")},
		}))
		code, out, stderr := f.run([]string{"send", "w0test:p0a", "go"})
		if code != 0 || out != "sent to local/w0test:p0a\n" || stderr != "" {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
	})
}
