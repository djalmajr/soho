package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestHcChildRosterHint(t *testing.T) {
	for _, tc := range []struct {
		name            string
		parentState     string
		parentSession   string
		redirectSession string
		parentRoster    bool
		parentGetFails  bool
		noParent        bool
		workerGone      bool
		ownRoster       bool
		wantHint        bool
	}{
		{name: "live-parent", parentRoster: true, wantHint: true},
		{name: "live-parent-without-roster", wantHint: true},
		{name: "live-parent-with-gone-worker", workerGone: true, wantHint: true},
		{name: "finished-parent", parentState: "finished", parentRoster: true},
		{name: "parent-lookup-failed", parentGetFails: true, parentRoster: true},
		{name: "no-parent", noParent: true},
		{name: "unusable-parent-record", parentState: "unknown", parentRoster: true},
		{name: "parent-session-ignored", parentSession: "../outside", wantHint: true},
		{name: "parent-state-dir", redirectSession: "parent-session", parentRoster: true, wantHint: true},
		{name: "child-state-dir", redirectSession: "child-session", parentRoster: true, wantHint: true},
		{name: "own-roster", parentRoster: true, ownRoster: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newHcGuardFixture(t)
			delete(f.env, "SOHO_DIR")
			bin := filepath.Join(f.root, "bin")
			f.env["HC_TERMINAL_ID"] = "tchild"
			f.env["SOHO_FAKECLI_CONFIG"] = bin
			f.env["XDG_CONFIG_HOME"] = filepath.Join(f.root, "config")
			f.env["SOHO_WAIT_POLL_MS"] = "1"

			child := fakecli.Rule{Argv: []string{"get", "tchild", "--json"}, Stdout: `{"address":"tchild","parent":"tparent","state":"live","labels":{"soho.session":"child-session"}}`}
			if tc.noParent {
				child.Stdout = `{"address":"tchild","state":"live","labels":{"soho.session":"child-session"}}`
			}
			parentState, parentSession := tc.parentState, tc.parentSession
			if parentState == "" {
				parentState = "live"
			}
			if parentSession == "" {
				parentSession = "parent-session"
			}
			parent := fakecli.Rule{Argv: []string{"get", "tparent", "--json"}, Stdout: fmt.Sprintf(`{"address":"tparent","state":%q,"labels":{"soho.session":%q}}`, parentState, parentSession)}
			if tc.parentGetFails {
				parent.Stdout, parent.Stderr, parent.Code = "", "hc is unavailable", 1
			}
			worker := fakecli.Rule{Argv: []string{"get", "worker", "--json"}, Stdout: `{"address":"tworker","state":"live","status":{"state":"idle"}}`}
			read := fakecli.Rule{Argv: []string{"read", "worker", "--lines", "120"}, ArgvPrefix: true, Stdout: `{"text":"child command's terminal fallback\n"}`}
			if tc.workerGone {
				worker.Stdout, worker.Code = "", 3
				read.Stdout, read.Code = "", 3
			}
			if _, err := fakecli.InstallWithOptions(t, bin, "hc", []fakecli.Rule{
				child, parent, worker, read,
				{Argv: []string{"ls"}, ArgvPrefix: true, Stdout: `{"terminals":[]}`},
				{Argv: []string{"wait", "worker", "--timeout"}, ArgvPrefix: true, Stdout: `{"ended":false}`},
			}, fakecli.InstallOptions{CaptureEnv: []string{"HC_TERMINAL_ID"}}); err != nil {
				t.Fatal(err)
			}

			state, redirected := filepath.Join(f.root, ".soho"), filepath.Join(f.root, "redirected")
			parentDir, childDir := filepath.Join(state, "parent-session"), filepath.Join(state, "child-session")
			files := map[string]string{}
			if tc.redirectSession != "" {
				files[filepath.Join(state, tc.redirectSession, "session.conf")] = "state_dir=redirected\n"
				if tc.redirectSession == "parent-session" {
					parentDir = filepath.Join(redirected, "parent-session")
				} else {
					childDir = filepath.Join(redirected, "child-session")
				}
			}
			const roster = "worker\ttworker\tclaude\timplementer\n"
			if tc.parentRoster {
				parentReport := filepath.Join(parentDir, "worker.md")
				files[filepath.Join(parentDir, "agents.tsv")] = roster
				files[filepath.Join(parentDir, "last-report-worker")] = parentReport + "\n"
				files[parentReport] = "Only the parent command can collect this report.\n"
			}
			childReport := filepath.Join(childDir, "worker.md")
			const childReportBody = "This is the child's own report.\n"
			if tc.ownRoster {
				files[filepath.Join(childDir, "agents.tsv")] = roster
				files[filepath.Join(childDir, "last-report-worker")] = childReport + "\n"
				files[childReport] = childReportBody
			}
			for _, dir := range []string{state, redirected} {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			for path, body := range files {
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			before, beforeRedirected := treePaths(t, state), treePaths(t, redirected)
			statusName := "not-in-roster"
			collectCode, collectOut := 6, "child command's terminal fallback\n"
			waitCode, waitOut := 3, ""
			if tc.workerGone {
				statusName = "unknown-agent"
				collectCode, collectOut = 4, ""
			}
			statusOut := "worker\t" + statusName + "\t\t-\t-\n"
			if tc.ownRoster {
				statusOut = "worker\tdone\t" + childReport + "\t"
				collectCode, collectOut = 0, "<!-- report: "+childReport+" -->\n"+childReportBody
				waitCode = 0
				// The report path is JSON-encoded (no HTML escaping, as the CLI's
				// JSON lines), so a literal U+00A0 or backslash from TMPDIR
				// matches where %q would escape it.
				var report bytes.Buffer
				enc := json.NewEncoder(&report)
				enc.SetEscapeHTML(false)
				if err := enc.Encode(childReport); err != nil {
					t.Fatal(err)
				}
				waitOut = "{\"agent\":\"worker\",\"status\":\"done\",\"report\":" + strings.TrimSuffix(report.String(), "\n") + ",\"observe\":true}\n"
			}
			for _, command := range []struct {
				name, stdout string
				code         int
			}{
				{name: "status", code: 0, stdout: statusOut},
				{name: "collect", code: collectCode, stdout: collectOut},
				{name: "wait", code: waitCode, stdout: waitOut},
			} {
				t.Run(command.name, func(t *testing.T) {
					args := []string{command.name, "worker"}
					if command.name == "wait" {
						args = append(args, "--observe", "--timeout", "5s")
					}
					code, stdout, stderr := f.run(t, args...)
					matchesStdout := stdout == command.stdout
					if tc.ownRoster && command.name == "status" {
						matchesStdout = strings.HasPrefix(stdout, command.stdout)
					}
					if code != command.code || !matchesStdout {
						t.Errorf("code=%d stdout=%q stderr=%q; want code=%d stdout=%q", code, stdout, stderr, command.code, command.stdout)
					}
					hint := "soho: this terminal (tchild) has no 'worker' in its roster; it is a child of tparent: if tparent started that worker, run 'soho " + command.name + " worker' in tparent's terminal"
					if command.name == "wait" && !tc.ownRoster {
						base := "soho: agent 'worker' is not in the roster (state dir: " + childDir + ")"
						hint = base + "; this terminal (tchild) is a child of tparent: if tparent started that worker, run 'soho wait worker --wake' in tparent's terminal (--wake creates its own child)"
						if !tc.wantHint && stderr != base+"\n" {
							t.Errorf("stderr=%q; want original roster error %q", stderr, base)
						}
					}
					if tc.wantHint {
						if !strings.Contains("\n"+stderr, "\n"+hint+"\n") || strings.Contains(stderr, "whose roster holds") {
							t.Errorf("stderr=%q; want conditional parent hint %q", stderr, hint)
						}
					} else if strings.Contains(stderr, "is a child of") || strings.Contains(stderr, "whose roster holds") {
						t.Errorf("unexpected parent hint in stderr=%q", stderr)
					}
				})
			}
			if after := treePaths(t, state); !tc.ownRoster && !reflect.DeepEqual(before, after) {
				t.Errorf("state tree changed: before=%v after=%v", before, after)
			}
			if after := treePaths(t, redirected); !reflect.DeepEqual(beforeRedirected, after) {
				t.Errorf("redirected state tree changed: before=%v after=%v", beforeRedirected, after)
			}
			for path, want := range files {
				if raw, err := os.ReadFile(path); err != nil || string(raw) != want {
					t.Errorf("fixture file %s changed: bytes=%q err=%v", path, raw, err)
				}
			}
			calls, err := fakecli.ReadCalls(filepath.Join(bin, "hc.calls.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			for _, call := range calls {
				if call.Env["HC_TERMINAL_ID"] != "tchild" || len(call.Argv) == 0 {
					t.Errorf("want a read-only hc call from tchild, got %+v", call)
					continue
				}
				switch call.Argv[0] {
				case "get", "read", "ls":
				case "wait":
					if !tc.ownRoster {
						t.Errorf("unexpected polling after the roster refusal: %+v", call)
					}
				default:
					t.Errorf("unexpected hc call: %+v", call)
				}
				if (tc.ownRoster || tc.noParent) && reflect.DeepEqual(call.Argv, parent.Argv) {
					t.Errorf("unexpected parent lookup: %+v", call)
				}
			}
		})
	}
}

func TestHcChildRosterHintFriction(t *testing.T) {
	for _, tc := range []struct {
		command, stdout string
		code            int
	}{
		{command: "wait", code: 3},
		{command: "status", code: 0, stdout: "worker\tnot-in-roster\t\t-\t-\n"},
		{command: "collect", code: 6, stdout: "recent terminal output\n"},
	} {
		t.Run(tc.command, func(t *testing.T) {
			f := newHcGuardFixture(t)
			delete(f.env, "SOHO_DIR")
			bin := filepath.Join(f.root, "bin")
			f.env["HC_TERMINAL_ID"] = "tchild"
			f.env["SOHO_FAKECLI_CONFIG"] = bin
			f.env["XDG_CONFIG_HOME"] = filepath.Join(f.root, "config")
			if _, err := fakecli.InstallWithOptions(t, bin, "hc", []fakecli.Rule{
				{Argv: []string{"get", "tchild", "--json"}, Stdout: `{"address":"tchild","parent":"tparent","state":"live","labels":{"soho.session":"child-session"}}`},
				{Argv: []string{"get", "tparent", "--json"}, Stdout: `{"address":"tparent","state":"live","labels":{"soho.session":"parent-session"}}`},
				{Argv: []string{"get", "worker", "--json"}, Stdout: `{"address":"tworker","state":"live","status":{"state":"idle"}}`},
				{Argv: []string{"read", "worker", "--lines", "120", "--json"}, Stdout: `{"text":"recent terminal output\n"}`},
				{Argv: []string{"ls"}, ArgvPrefix: true, Stdout: `{"terminals":[]}`},
			}, fakecli.InstallOptions{CaptureEnv: []string{"HC_TERMINAL_ID"}}); err != nil {
				t.Fatal(err)
			}

			state := filepath.Join(f.root, ".soho")
			childDir := filepath.Join(state, "child-session")
			if err := os.MkdirAll(childDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(childDir, "agents.tsv"), []byte("other\ttother\tclaude\timplementer\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			code, stdout, stderr := f.run(t, tc.command, "worker")
			if code != tc.code || stdout != tc.stdout {
				t.Errorf("code=%d stdout=%q stderr=%q; want code=%d stdout=%q", code, stdout, stderr, tc.code, tc.stdout)
			}
			const condition = "if tparent started that worker"
			hint := "soho: this terminal (tchild) has no 'worker' in its roster; it is a child of tparent: " + condition + ", run 'soho " + tc.command + " worker' in tparent's terminal"
			wantEntry := ""
			switch tc.command {
			case "wait":
				refusal := "agent 'worker' is not in the roster (state dir: " + childDir + ")"
				hint = "soho: " + refusal + "; this terminal (tchild) is a child of tparent: " + condition + ", run 'soho wait worker --wake' in tparent's terminal (--wake creates its own child)"
				wantEntry = "error(exit 3)\twait\t" + refusal + "\n"
			case "collect":
				wantEntry = "warning\tcollect\tno report file yet for 'worker' (expected <none dispatched>); falling back to recent terminal output\n"
			}
			if !strings.Contains("\n"+stderr, "\n"+hint+"\n") {
				t.Errorf("stderr=%q; want line %q", stderr, hint)
			}

			friction := filepath.Join(childDir, "friction.log")
			raw, err := os.ReadFile(friction)
			if wantEntry == "" {
				if !os.IsNotExist(err) {
					t.Errorf("status created a friction log: bytes=%q err=%v", raw, err)
				}
			} else {
				_, entry, ok := strings.Cut(string(raw), "\t")
				if err != nil || !ok || entry != wantEntry {
					t.Errorf("friction entry=%q err=%v; want %q", entry, err, wantEntry)
				}
			}
			for _, path := range treePaths(t, state) {
				full := filepath.Join(state, path)
				info, err := os.Stat(full)
				if err != nil {
					t.Fatal(err)
				}
				if !info.Mode().IsRegular() || full == friction {
					continue
				}
				raw, err := os.ReadFile(full)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(raw), condition) {
					t.Errorf("parent advice was persisted in %s", full)
				}
			}
			calls, err := fakecli.ReadCalls(filepath.Join(bin, "hc.calls.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			for _, call := range calls {
				if call.Env["HC_TERMINAL_ID"] != "tchild" {
					t.Errorf("unexpected hc caller: %+v", call)
				}
			}
		})
	}
}
