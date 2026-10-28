package peer_test

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/peer"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestSendFinishedWorkerWarning(t *testing.T) {
	for _, tc := range []struct {
		name, role, workspace, report, target string
		roster, warn                          bool
	}{
		{"written", "implementer", "ws-test", "done", "w0test:p0a", true, true},
		{"name resolves to roster pane", "implementer", "ws-test", "done", "worker", true, true},
		{"whitespace is nonempty", "implementer", "ws-test", " ", "w0test:p0a", true, true},
		{"missing report", "implementer", "ws-test", "missing", "w0test:p0a", true, false},
		{"empty report", "implementer", "ws-test", "", "w0test:p0a", true, false},
		{"non roster", "implementer", "ws-test", "done", "w0test:p0a", false, false},
		{"other workspace", "implementer", "ws-other", "done", "w0test:p0a", true, false},
		{"remote", "implementer", "ws-test", "done", "remote/w0test:p0a", true, false},
		{"orchestrator", "-", "ws-test", "done", "w0test:p0a", true, false},
		{"sub orchestrator", "sub-orchestrator", "ws-test", "done", "w0test:p0a", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := rand.Reader
			rand.Reader = bytes.NewReader([]byte{0xde, 0xad, 0xbe, 0xef})
			t.Cleanup(func() { rand.Reader = original })
			prompt := peer.PeerHeader("local/-", "-", "-", "-", "deadbeef") + "\n\n> hello\n" + peer.PeerEndLine("deadbeef")
			if strings.HasPrefix(tc.target, "remote/") {
				prompt = peer.PeerHeaderRemote("-", "Run2Biz.local", "-", "-", "-", "deadbeef") + "\n\n> hello\n" + peer.PeerEndLine("deadbeef")
				setSenderHostname(t, "Run2Biz.local")
			}
			initial := `{"result":{"agent":{"pane_id":"w0test:p0a","agent_status":"idle","state_change_seq":"1","workspace_id":"` + tc.workspace + `"}}}`
			rules := successfulSendRules(prompt, fakecli.Rule{Argv: []string{"agent", "get", tc.target}, Call: 1, Stdout: initial})
			if tc.target == "worker" {
				for i := 1; i < len(rules); i++ {
					if len(rules[i].Argv) > 1 && rules[i].Argv[1] == "get" && rules[i].Call > 1 {
						rules[i].Call--
					}
				}
			}
			if strings.HasPrefix(tc.target, "remote/") {
				for i := range rules {
					if i == 0 {
						rules[i].Argv = []string{"agent", "get", "w0test:p0a"}
					}
					rules[i].Argv = append([]string{"--machine", "remote"}, rules[i].Argv...)
				}
			}
			f := newFixture(t, rules)
			t.Logf("fixture_root=%s", f.dir)
			state := filepath.Join(f.env["SOHO_DIR"], "ws-test")
			if err := os.MkdirAll(state, 0700); err != nil {
				t.Fatal(err)
			}
			if tc.roster {
				if err := os.WriteFile(filepath.Join(state, "agents.tsv"), []byte("worker\tw0test:p0a\tcodex\t"+tc.role+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(filepath.Join(state, "agents.tsv"), []byte("worker\tw0test:p9\tcodex\timplementer\n"), 0600); err != nil {
				t.Fatal(err)
			}
			report := filepath.Join(state, "report.md")
			if tc.report != "missing" {
				if err := os.WriteFile(report, []byte(tc.report), 0600); err != nil {
					t.Fatal(err)
				}
			}
			pointer, _ := json.Marshal(map[string]any{"version": 1, "task_report": filepath.Join(state, "stable.md"), "current": report, "history": []string{}})
			if err := os.WriteFile(filepath.Join(state, "task-report-worker.json"), pointer, 0600); err != nil {
				t.Fatal(err)
			}
			code, out, stderr := f.run([]string{"send", tc.target, "hello", "--now"})
			want := ""
			if tc.warn {
				want = "soho: send: 'worker' is a worker of this team and its report is written; this message puts it to work outside a task, and a dispatch to it is refused while it works. For new work use soho dispatch <agent> <brief>, or --amend to add to its task\n"
			}
			if code != 0 || stderr != want {
				t.Fatalf("code=%d out=%q stderr=%q want=%q", code, out, stderr, want)
			}
			calls, err := fakecli.ReadCallsForConfig(filepath.Join(filepath.Dir(f.bin), "herdr.json"))
			if err != nil {
				t.Fatal(err)
			}
			gets, prompts := 0, 0
			for _, c := range calls {
				args := c.Argv
				if len(args) > 2 && args[0] == "--machine" {
					args = args[2:]
				}
				if len(args) > 1 && args[0] == "agent" {
					switch args[1] {
					case "get":
						gets++
					case "prompt":
						prompts++
					}
				}
			}
			if gets != 4 || prompts != 1 || !strings.HasPrefix(out, "sent to ") {
				t.Fatalf("gets=%d prompts=%d out=%q", gets, prompts, out)
			}
		})
	}
}
