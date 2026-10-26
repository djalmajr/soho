package peer_test

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/peer"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestSendLoggingScope(t *testing.T) {
	for _, tc := range []struct {
		name, workspace       string
		pane, existing, short bool
	}{
		{name: "unknown absent"},
		{name: "unknown existing", existing: true},
		{name: "unknown short timeout", existing: true, short: true},
		{name: "explicit workspace", workspace: "explicit-workspace"},
		{name: "explicit short timeout", workspace: "explicit-workspace", short: true},
		{name: "verified pane", pane: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldReader := rand.Reader
			rand.Reader = bytes.NewReader([]byte{1, 2, 3, 4})
			t.Cleanup(func() { rand.Reader = oldReader })
			from := "local/-"
			if tc.pane {
				from = "local/w1:p9"
			}
			prompt := peer.PeerHeader(from, "-", "-", "-", "01020304") + "\n\n> go\n" + peer.PeerEndLine("01020304")
			focused := "foreign-workspace"
			if tc.pane {
				focused = "caller-workspace"
			}
			rules := append([]fakecli.Rule{{Argv: []string{"pane", "current", "--current"}, Stdout: `{"result":{"pane":{"pane_id":"foreign:p9","workspace_id":"` + focused + `"}}}`}}, sendRules(prompt, []fakecli.Rule{
				{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 2, Stdout: agentJSON("idle", "1")},
				{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 3, Stdout: agentJSON("idle", "1")},
				{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 4, Stdout: agentJSON("working", "2")},
			})...)
			f := newFixture(t, rules)
			delete(f.env, "HERDR_PANE_ID")
			delete(f.env, "SOHO_DELEGATED_PANE")
			delete(f.env, "HERDR_WORKSPACE_ID")
			if tc.workspace != "" {
				f.env["HERDR_WORKSPACE_ID"] = tc.workspace
			}
			if tc.pane {
				f.env["HERDR_PANE_ID"] = "w1:p9"
				f.env["SOHO_DELEGATED_PANE"] = "w1:p9"
			}
			f.env["USERPROFILE"] = f.env["HOME"]
			f.env["PATH"] = filepath.Dir(f.bin)
			for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "TMPDIR", "PATH"} {
				t.Setenv(key, f.env[key])
			}
			t.Setenv("SOHO_SEND_WINDOW_MS", "1")
			t.Setenv("SOHO_SEND_POLL_MS", "1")
			foreign := filepath.Join(f.env["SOHO_DIR"], "foreign-workspace")
			if tc.workspace != "" && tc.short {
				if err := os.MkdirAll(filepath.Join(f.env["SOHO_DIR"], tc.workspace), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if tc.existing {
				if err := os.MkdirAll(foreign, 0o700); err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{peer.PeerLogFile, "friction.log", "agents.tsv"} {
					if err := os.WriteFile(filepath.Join(foreign, name), []byte("existing\n"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			args := []string{"send", "w0test:p0a", "go"}
			if tc.short {
				args = append(args, "--timeout", "999")
			}
			code, out, stderr := f.run(args)
			if code != 0 || out != "sent to local/w0test:p0a\n" || sendCallCount(t, f, "agent", "prompt") != 1 {
				t.Fatalf("code=%d stdout=%q stderr=%q env=%v", code, out, stderr, f.env)
			}
			unknown := tc.workspace == "" && !tc.pane
			line := "soho: send: no caller workspace; the message was delivered but not logged\n"
			if strings.Count(stderr, line) != map[bool]int{true: 1, false: 0}[unknown] {
				t.Fatalf("stderr=%q unknown=%v", stderr, unknown)
			}
			if !tc.short && stderr != map[bool]string{true: line, false: ""}[unknown] {
				t.Fatalf("stderr=%q", stderr)
			}
			if !tc.pane && sendCallCount(t, f, "pane", "current", "--current") != 0 {
				t.Fatal("logging inferred the focused workspace")
			}
			if tc.pane && herdr.CheckCallerPane(f.env).State != herdr.CallerVerified {
				t.Fatal("caller was not verified")
			}
			if tc.existing {
				entries, err := os.ReadDir(foreign)
				if err != nil || len(entries) != 3 {
					t.Fatalf("foreign entries=%v err=%v", entries, err)
				}
				for _, name := range []string{peer.PeerLogFile, "friction.log", "agents.tsv"} {
					data, err := os.ReadFile(filepath.Join(foreign, name))
					if err != nil || string(data) != "existing\n" {
						t.Fatalf("foreign %s=%q err=%v", name, data, err)
					}
				}
			} else if _, err := os.Stat(foreign); !os.IsNotExist(err) {
				t.Fatalf("foreign workspace touched: %v", err)
			}
			if !unknown {
				workspace := tc.workspace
				if tc.pane {
					workspace = "caller-workspace"
				}
				sd := filepath.Join(f.env["SOHO_DIR"], workspace)
				data, err := os.ReadFile(filepath.Join(sd, peer.PeerLogFile))
				if err != nil || !strings.Contains(string(data), "\t"+from+"\tlocal/w0test:p0a\tsent\t2\t01020304\n") {
					t.Fatalf("peer log=%q err=%v", data, err)
				}
				if tc.short {
					data, err = os.ReadFile(filepath.Join(sd, "friction.log"))
					if err != nil || !strings.Contains(string(data), "\twarning\t") {
						t.Fatalf("friction=%q err=%v", data, err)
					}
				}
			}
		})
	}
}
