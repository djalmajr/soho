package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestHcCommandHelpWithoutTransport(t *testing.T) {
	f := newMutationGuardHcFixture(t)
	f.env["SOHO_TRANSPORT"] = "hc"
	f.env["HC_TERMINAL_ID"] = "t1"
	before := fileSnapshot(t, f.root)
	for command := range knownCommands {
		for _, flag := range []string{"--help", "-h"} {
			code, out, stderr := runMutationGuardHc(t, []string{command, flag}, f.env.Clone())
			if code != 0 || out != commandHelp(command) || stderr != "" {
				t.Fatalf("%s %s: exit=%d stdout=%q stderr=%q", command, flag, code, out, stderr)
			}
		}
	}
	assertZeroCliCalls(t, filepath.Join(f.bin, "hc.calls.jsonl"), "hc")
	assertZeroCliCalls(t, filepath.Join(f.bin, "herdr.calls.jsonl"), "herdr")
	if after := fileSnapshot(t, f.root); !reflect.DeepEqual(before, after) {
		t.Fatalf("help changed fixture: before=%v after=%v", before, after)
	}
	code, out, stderr := runMutationGuardHc(t, []string{"reopen", "--help", "extra"}, f.env.Clone())
	if code != 2 || out != "" || !strings.Contains(stderr, hcRefusalLine) {
		t.Fatalf("non-exact help: exit=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestHcSessionConfigLayer(t *testing.T) {
	f := newMutationGuardHcFixture(t)
	registrantReinstallHc(t, f.bin, fakecli.Rule{Argv: []string{"get", "t1", "--json"}, Stdout: `{"address":"t1","state":"live","status":{"state":"idle","turn":1}}`})
	f.env["SOHO_TRANSPORT"] = "hc"
	f.env["HC_TERMINAL_ID"] = "t1"
	t.Chdir(f.repo)
	code, out, stderr := runMutationGuardHc(t, []string{"session", "set", "max_workers", "9"}, f.env.Clone())
	if code != 0 || stderr != "" {
		t.Fatalf("session set exit=%d out=%q stderr=%q", code, out, stderr)
	}
	path := filepath.Join(f.state, "hc-t1", "session.conf")
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "max_workers=9") {
		t.Fatalf("session file %s=%q err=%v", path, data, err)
	}
	code, out, stderr = runMutationGuardHc(t, []string{"config", "get", "max_workers"}, f.env.Clone())
	if code != 0 || strings.TrimSpace(out) != "9" || stderr != "" {
		t.Fatalf("config get exit=%d out=%q stderr=%q", code, out, stderr)
	}
	code, out, stderr = runMutationGuardHc(t, []string{"config"}, f.env.Clone())
	if code != 0 || !strings.Contains(out, "max_workers        9                              session\n") {
		t.Fatalf("config layer exit=%d out=%q stderr=%q", code, out, stderr)
	}
	code, out, stderr = runMutationGuardHc(t, []string{"session", "show"}, f.env.Clone())
	if code != 0 || !strings.Contains(out, "max_workers=9") || !strings.Contains(out, path) {
		t.Fatalf("session show exit=%d out=%q stderr=%q", code, out, stderr)
	}
	code, out, stderr = runMutationGuardHc(t, []string{"session", "clear", "max_workers"}, f.env.Clone())
	if code != 0 {
		t.Fatalf("session clear exit=%d out=%q stderr=%q", code, out, stderr)
	}
	code, out, stderr = runMutationGuardHc(t, []string{"config", "get", "max_workers"}, f.env.Clone())
	if code != 0 || strings.TrimSpace(out) == "9" {
		t.Fatalf("cleared config exit=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestHcReviewSnapshotLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name string
		keep bool
	}{{"release", false}, {"keep", true}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSnapshotFixture(t)
			bin := filepath.Join(f.root, "bin")
			rules := []fakecli.Rule{
				{Argv: []string{"get", "t1", "--json"}, Stdout: `{"address":"t1","state":"live","labels":{"soho.session":"ws"},"status":{"state":"idle","turn":1}}`},
				{Argv: []string{"get", "t7", "--json"}, Stdout: `{"address":"t7","state":"live","labels":{"soho.session":"worker-session"},"status":{"state":"idle","turn":1}}`},
				{Argv: []string{"get", "worker", "--json"}, Stdout: `{"address":"t7","name":"worker","state":"live","status":{"state":"idle","turn":1}}`},
				{Argv: []string{"kill", "t7", "--json"}, ArgvPrefix: true, Stdout: `{"address":"t7","state":"finished"}`},
				{Argv: []string{"rm", "t7", "--json"}, ArgvPrefix: true, Stdout: `{"removed":"t7"}`},
				{Argv: []string{"ls"}, ArgvPrefix: true, Stdout: `{"terminals":[]}`},
			}
			if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
				t.Fatal(err)
			}
			f.env["SOHO_TRANSPORT"], f.env["HC_TERMINAL_ID"], f.env["SOHO_FAKECLI_CONFIG"] = "hc", "t7", bin
			f.env["SOHO_REGISTRY_DIR"] = f.sd
			roster := "# header\nworker\tt7\tcodex\timplementer\topenai\t1\t" + f.src + "\tnow\t\tfull\n"
			if err := os.WriteFile(filepath.Join(f.sd, "agents.tsv"), []byte(roster), 0600); err != nil {
				t.Fatal(err)
			}
			code, out, stderr := f.run(t, "review-snapshot", f.src, "--for", "worker", "--name", "worker-copy")
			if code != 0 {
				t.Fatalf("snapshot exit=%d out=%s err=%s", code, out, stderr)
			}
			var worker map[string]string
			if err := json.Unmarshal([]byte(out), &worker); err != nil {
				t.Fatal(err)
			}
			rows, err := readReviewSnapshots(f.sd)
			if err != nil || len(rows) != 1 || rows[0][0] != "worker" || rows[0][1] != worker["snapshot"] {
				t.Fatalf("worker registry=%v err=%v", rows, err)
			}
			f.env["HC_TERMINAL_ID"] = "t1"
			code, out, stderr = f.run(t, "review-snapshot", f.src, "--name", "orchestrator-copy")
			if code != 0 {
				t.Fatalf("snapshot orchestrator exit=%d out=%s err=%s", code, out, stderr)
			}
			var orch map[string]string
			if err := json.Unmarshal([]byte(out), &orch); err != nil {
				t.Fatal(err)
			}
			if orch["owner"] != "" {
				t.Fatalf("orchestrator owner=%q", orch["owner"])
			}
			code, out, stderr = f.run(t, "gc", "--yes")
			if code != 0 {
				t.Fatalf("gc exit=%d out=%s err=%s", code, out, stderr)
			}
			if _, err := os.Stat(orch["snapshot"]); err != nil {
				t.Fatalf("gc removed orchestrator: %v", err)
			}
			args := []string{"release", "worker", "--close"}
			if tc.keep {
				args = append(args, "--keep-copies")
			}
			code, out, stderr = f.run(t, args...)
			if code != 0 {
				t.Fatalf("release exit=%d out=%s err=%s", code, out, stderr)
			}
			calls, err := fakecli.ReadCalls(filepath.Join(bin, "hc.calls.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			kills, removes := hcCallIndexes(calls, []string{"kill", "t7"}), hcCallIndexes(calls, []string{"rm", "t7"})
			if len(kills) != 1 || len(removes) != 1 || kills[0] >= removes[0] {
				t.Fatalf("kill/rm calls=%v / %v", kills, removes)
			}
			for _, p := range []string{worker["snapshot"], worker["patch"], worker["patch"] + ".sha256"} {
				_, err := os.Stat(p)
				if tc.keep && err != nil || !tc.keep && !os.IsNotExist(err) {
					t.Fatalf("keep=%v path=%s err=%v", tc.keep, p, err)
				}
			}
			rows, err = readReviewSnapshots(f.sd)
			if err != nil {
				t.Fatal(err)
			}
			want := 1
			if tc.keep {
				want = 2
			}
			if len(rows) != want {
				t.Fatalf("rows=%v want %d", rows, want)
			}
		})
	}
}
