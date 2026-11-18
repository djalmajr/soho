package cli

import (
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
	}{
		{name: "child", parentRecord: true}, {name: "worker", parentRecord: true, worker: true},
		{name: "fresh"}, {name: "get-failure", parentRecord: true, fail: true},
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
			if _, err := fakecli.Install(t, filepath.Join(f.root, "bin"), "hc", []fakecli.Rule{rule}); err != nil {
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
				for _, command := range []string{"status", "spawn"} {
					code, _, stderr := f.run(t, command)
					want := "soho: " + command + ": this terminal (tchild) is a child of the orchestrator tparent; run soho from the orchestrator's terminal (builds and tests can stay in children)\n"
					if code != 2 || stderr != want {
						t.Fatalf("%s: code=%d stderr=%q want=%q", command, code, stderr, want)
					}
				}
				calls, err := fakecli.ReadCalls(filepath.Join(f.root, "bin", "hc.calls.jsonl"))
				if err != nil || len(calls) != 2 {
					t.Fatalf("two refusals require one lookup each: calls=%v err=%v", calls, err)
				}
				if code, _, stderr := f.run(t, "help"); code != 0 {
					t.Fatalf("help: code=%d stderr=%q", code, stderr)
				}
				if _, err := os.Stat(filepath.Join(state, "hc-tchild")); !os.IsNotExist(err) {
					t.Fatalf("child state exists: %v", err)
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
			if _, err := os.Stat(filepath.Join(state, "hc-tchild", "agents.tsv")); err != nil {
				t.Fatalf("own state not initialized: %v; stderr=%q", err, stderr)
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
			if _, err := fakecli.Install(t, filepath.Join(f.root, "bin"), "hc", []fakecli.Rule{{Argv: []string{"get", "tchild", "--json"}, Stdout: `{"address":"tchild","parent":"tparent","state":"live"}`}}); err != nil {
				t.Fatal(err)
			}
			state := filepath.Join(f.root, ".soho")
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
			code, _, stderr := f.run(t, "status")
			if code != 0 || strings.Contains(stderr, "child of the orchestrator") {
				t.Fatalf("%s: code=%d stderr=%q", mode, code, stderr)
			}
		})
	}
}
