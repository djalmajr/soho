package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/sessionmeta"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestHcChildGuard(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		parentRecord, worker, fail bool
		command                    string
	}{
		{name: "child", parentRecord: true}, {name: "worker", parentRecord: true, worker: true},
		{name: "fresh"}, {name: "get-failure", parentRecord: true, fail: true},
		// The exemptions reach the guard only through a guarded command.
		{name: "worker-init", parentRecord: true, worker: true, command: "init"},
		{name: "fresh-init", command: "init"},
		{name: "get-failure-init", parentRecord: true, fail: true, command: "init"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newHcGuardFixture(t)
			delete(f.env, "SOHO_DIR")
			f.env["HC_TERMINAL_ID"] = "tchild"
			f.env["XDG_CONFIG_HOME"] = filepath.Join(f.root, "config")
			f.env["SOHO_FAKECLI_CONFIG"] = filepath.Join(f.root, "bin")
			if tc.worker {
				f.env["SOHO_AGENT"] = "worker"
				f.env["SOHO_REGISTRY_DIR"] = filepath.Join(f.root, ".soho", "hc-tparent")
			}
			rule := fakecli.Rule{Argv: []string{"get", "tchild", "--json"}, Stdout: `{"address":"tchild","parent":"tparent","state":"live"}`}
			if tc.fail {
				rule = fakecli.Rule{Argv: []string{"get", "tchild", "--json"}, Code: 3}
			}
			if _, err := fakecli.Install(t, filepath.Join(f.root, "bin"), "hc", []fakecli.Rule{rule, {Argv: []string{"get", "tparent", "--json"}, Stdout: `{"address":"tparent","state":"live"}`}}); err != nil {
				t.Fatal(err)
			}
			state := filepath.Join(f.root, ".soho")
			if tc.parentRecord {
				if err := os.MkdirAll(filepath.Join(state, "hc-tparent"), 0700); err != nil {
					t.Fatal(err)
				}
				_, err := (sessionmeta.Store{StateBase: state, SessionID: "hc-tparent"}).Register(sessionmeta.Record{Schema: 1, ProjectRoot: f.root, SessionID: "hc-tparent", Terminal: "tparent", Name: "orchestrator", Kind: "claude"})
				if err != nil {
					t.Fatal(err)
				}
			}
			if tc.name == "child" {
				for _, command := range []string{"spawn", "init"} {
					code, _, stderr := f.run(t, command)
					want := "soho: " + command + ": this terminal (tchild) is a child of the orchestrator tparent; run soho from the orchestrator's terminal (builds and tests can stay in children)\n"
					if code != 2 || stderr != want {
						t.Fatalf("%s: code=%d stderr=%q want=%q", command, code, stderr, want)
					}
				}
				calls, err := fakecli.ReadCalls(filepath.Join(f.root, "bin", "hc.calls.jsonl"))
				if err != nil || len(calls) != 6 {
					t.Fatalf("two refusals require three lookups each: calls=%v err=%v", calls, err)
				}
				if code, _, stderr := f.run(t, "help"); code != 0 {
					t.Fatalf("help: code=%d stderr=%q", code, stderr)
				}
				if _, err := os.Stat(filepath.Join(state, "hc-tchild")); !os.IsNotExist(err) {
					t.Fatalf("child state exists: %v", err)
				}
				return
			}
			if tc.command == "init" {
				if _, _, stderr := f.run(t, "init"); strings.Contains(stderr, "child of the orchestrator") {
					t.Fatalf("init refused: %q", stderr)
				}
				return
			}
			code, _, stderr := f.run(t, "status")
			if tc.fail {
				if strings.Contains(stderr, "child of the orchestrator") || code != 2 {
					t.Fatalf("lookup failure: code=%d stderr=%q", code, stderr)
				}
				return
			}
			if code != 0 {
				t.Fatalf("status: code=%d stderr=%q", code, stderr)
			}
			if _, err := os.Stat(filepath.Join(state, "hc-tchild")); !os.IsNotExist(err) {
				t.Fatalf("status created own state: %v; stderr=%q", err, stderr)
			}
		})
	}
}

func TestHcChildGuardExemptions(t *testing.T) {
	for _, mode := range []string{"existing-own-state", "explicit-dir", "explicit-setting", "project-setting", "other-record", "no-record"} {
		t.Run(mode, func(t *testing.T) {
			f := newHcGuardFixture(t)
			delete(f.env, "SOHO_DIR")
			f.env["HC_TERMINAL_ID"], f.env["SOHO_FAKECLI_CONFIG"] = "tchild", filepath.Join(f.root, "bin")
			f.env["XDG_CONFIG_HOME"] = filepath.Join(f.root, "config")
			if _, err := fakecli.Install(t, filepath.Join(f.root, "bin"), "hc", []fakecli.Rule{{Argv: []string{"get", "tchild", "--json"}, Stdout: `{"address":"tchild","parent":"tparent","state":"live"}`}, {Argv: []string{"get", "tparent", "--json"}, Stdout: `{"address":"tparent","state":"live"}`}}); err != nil {
				t.Fatal(err)
			}
			state := filepath.Join(f.root, ".soho")
			if mode == "explicit-dir" || mode == "explicit-setting" || mode == "project-setting" {
				state = filepath.Join(f.root, "custom")
			}
			if err := os.MkdirAll(filepath.Join(state, "hc-tparent"), 0700); err != nil {
				t.Fatal(err)
			}
			if mode != "no-record" {
				terminal := "tparent"
				if mode == "other-record" {
					terminal = "tother"
				}
				_, err := (sessionmeta.Store{StateBase: state, SessionID: "hc-tparent"}).Register(sessionmeta.Record{Schema: 1, ProjectRoot: f.root, SessionID: "hc-tparent", Terminal: terminal, Name: "orchestrator", Kind: "claude"})
				if err != nil {
					t.Fatal(err)
				}
			}
			if mode == "existing-own-state" {
				if err := os.MkdirAll(filepath.Join(state, "hc-tchild"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "explicit-dir" {
				f.env["SOHO_DIR"] = filepath.Join(f.root, "custom")
			}
			if mode == "explicit-setting" {
				f.env["SOHO_STATE_DIR"] = filepath.Join(f.root, "custom")
			}
			if mode == "project-setting" {
				if err := os.MkdirAll(filepath.Join(f.root, ".agents"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(f.root, ".agents", "soho.conf"), []byte("state_dir=custom\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			code, _, stderr := f.run(t, "init")
			if strings.Contains(stderr, "child of the orchestrator") {
				t.Fatalf("%s: code=%d stderr=%q", mode, code, stderr)
			}
		})
	}
}

func TestHcChildAllowedCommands(t *testing.T) {
	child := fakecli.Rule{Argv: []string{"get", "tchild", "--json"}, Stdout: `{"address":"tchild","parent":"tparent","state":"live","labels":{"soho.session":"hc-tparent"}}`}
	worker := fakecli.Rule{Argv: []string{"get", "worker", "--json"}, Stdout: `{"address":"tworker","name":"worker","state":"live","status":{"state":"idle","turn":7},"labels":{"soho.session":"hc-tparent"}}`}
	f := newHcGuardFixture(t)
	delete(f.env, "SOHO_DIR")
	f.env["HC_TERMINAL_ID"], f.env["SOHO_FAKECLI_CONFIG"] = "tchild", filepath.Join(f.root, "bin")
	f.env["XDG_CONFIG_HOME"] = filepath.Join(f.root, "config")
	if _, err := fakecli.Install(t, filepath.Join(f.root, "bin"), "hc", []fakecli.Rule{child, worker}); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(f.root, ".soho")
	sd := filepath.Join(state, "hc-tparent")
	if err := os.MkdirAll(filepath.Join(sd, "reports"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := (sessionmeta.Store{StateBase: state, SessionID: "hc-tparent"}).Register(sessionmeta.Record{Schema: 1, ProjectRoot: f.root, SessionID: "hc-tparent", Terminal: "tparent", Name: "orchestrator", Kind: "claude"}); err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(sd, "reports", "worker.md")
	for path, body := range map[string]string{report: "Done.\n", filepath.Join(sd, "last-report-worker"): report + "\n", filepath.Join(sd, "agents.tsv"): "worker\ttworker\tclaude\timplementer\n"} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("wake-observer", func(t *testing.T) {
		code, out, stderr := f.run(t, "wait", "worker", "--timeout", "5000", "--observe")
		if code != 0 || !strings.Contains(out, `"observe":true`) || !strings.Contains(out, `"status":"done"`) {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
	})
	t.Run("hook", func(t *testing.T) {
		old := hookStdin
		defer func() { hookStdin = old }()
		hookStdin = strings.NewReader(`{"hook_event_name":"UserPromptSubmit","prompt":"ordinary prompt"}`)
		code, _, stderr := f.run(t, "hook", "inbound", "--codex")
		if code != 0 {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
	})
	t.Run("gate", func(t *testing.T) {
		prefix := filepath.Join(f.root, "evidence")
		t.Setenv("SOHO_GATE_CLI_TEST_EXIT", "0")
		code, _, stderr := f.run(t, append([]string{"gate", prefix, "5s", "--"}, gateProgram()...)...)
		if _, err := os.Stat(prefix + ".result.json"); code != 0 || err != nil {
			t.Fatalf("code=%d stderr=%q result=%v", code, stderr, err)
		}
	})
}

func TestHcChildLabelledSessions(t *testing.T) {
	for _, own := range []bool{false, true} {
		t.Run(fmt.Sprint(own), func(t *testing.T) {
			f := newHcGuardFixture(t)
			delete(f.env, "SOHO_DIR")
			f.env["HC_TERMINAL_ID"], f.env["SOHO_FAKECLI_CONFIG"] = "tchild", filepath.Join(f.root, "bin")
			f.env["XDG_CONFIG_HOME"] = filepath.Join(f.root, "config")
			rules := []fakecli.Rule{{Argv: []string{"get", "tchild", "--json"}, Stdout: `{"address":"tchild","parent":"tparent","labels":{"soho.session":"nested-session"}}`}, {Argv: []string{"get", "tparent", "--json"}, Stdout: `{"address":"tparent","labels":{"soho.session":"team-session"}}`}}
			if _, err := fakecli.Install(t, filepath.Join(f.root, "bin"), "hc", rules); err != nil {
				t.Fatal(err)
			}
			state := filepath.Join(f.root, ".soho")
			if err := os.MkdirAll(filepath.Join(state, "team-session"), 0700); err != nil {
				t.Fatal(err)
			}
			if _, err := (sessionmeta.Store{StateBase: state, SessionID: "team-session"}).Register(sessionmeta.Record{Schema: 1, ProjectRoot: f.root, SessionID: "team-session", Terminal: "tparent", Name: "orchestrator", Kind: "claude"}); err != nil {
				t.Fatal(err)
			}
			if own {
				if err := os.MkdirAll(filepath.Join(state, "nested-session"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			code, _, stderr := f.run(t, "init")
			refused := strings.Contains(stderr, "child of the orchestrator")
			if refused == own || (!own && code != 2) {
				t.Fatalf("own=%t code=%d stderr=%q", own, code, stderr)
			}
		})
	}
}

func TestHcChildWakeObserverWithoutOwnState(t *testing.T) {
	f := newHcGuardFixture(t)
	delete(f.env, "SOHO_DIR")
	f.env["HC_TERMINAL_ID"], f.env["SOHO_FAKECLI_CONFIG"] = "tchild", filepath.Join(f.root, "bin")
	f.env["XDG_CONFIG_HOME"] = filepath.Join(f.root, "config")
	rules := []fakecli.Rule{{Argv: []string{"get", "tchild", "--json"}, Stdout: `{"address":"tchild","parent":"tparent","state":"live"}`}, {Argv: []string{"get", "tparent", "--json"}, Stdout: `{"address":"tparent","state":"live"}`}}
	if _, err := fakecli.Install(t, filepath.Join(f.root, "bin"), "hc", rules); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(f.root, ".soho")
	if err := os.MkdirAll(filepath.Join(state, "hc-tparent"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := (sessionmeta.Store{StateBase: state, SessionID: "hc-tparent"}).Register(sessionmeta.Record{Schema: 1, ProjectRoot: f.root, SessionID: "hc-tparent", Terminal: "tparent", Name: "orchestrator", Kind: "claude"}); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := f.run(t, "wait", "missing", "--timeout", "1", "--observe")
	if strings.Contains(stderr, "child of the orchestrator") {
		t.Fatalf("observer intercepted: code=%d out=%q stderr=%q", code, out, stderr)
	}
}
