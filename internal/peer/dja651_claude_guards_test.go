package peer_test

import (
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/peer"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestDJA651ClaudeSharedTranscriptGuards(t *testing.T) {
	for _, stalled := range []bool{false, true} {
		path := "normal"
		if stalled {
			path = "stalled"
		}
		for _, kind := range []string{"clean", "composer", "dialog", "history_refusal"} {
			t.Run(path+"/"+kind, func(t *testing.T) {
				id, prompt := stalledID(t)
				cwd, configRoot := t.TempDir(), t.TempDir()
				pre := "✻ Cranking out changes…\n"
				post := pre
				switch kind {
				case "composer":
					sep := strings.Repeat("─", 66)
					post = pre + sep + "\n❯ " + peer.PeerOpenTag(id) + " pending\n" + sep + "\n"
				case "dialog":
					post = "Do you trust this workspace? [y/N]\n" + pre
				}
				recent := "old history without receipt\n"
				if kind == "history_refusal" {
					recent = "[soho:peer-refused:" + id + "] refused-inbound-off\n"
				}
				p := dja651PromptRule(prompt)
				p.Delay = 100
				if stalled {
					p.Code, p.Stderr = 1, stalledPromptErr
				}
				f := newFixture(t, []fakecli.Rule{
					{Argv: []string{"agent", "get", "w0test:p0a"}, Stdout: claudeSessionAgentJSON("working", "1", cwd)},
					{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 1, Stdout: pre},
					{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Stdout: post},
					{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: recent}, p,
					{Argv: []string{"agent", "send-keys", "w0test:p0a", "enter"}},
				})
				dja651Isolate(f)
				f.env["CLAUDE_CONFIG_DIR"] = configRoot
				transcriptPath := claudeTranscriptPath(t, configRoot, cwd)
				writeClaudeTranscript(t, transcriptPath, `{"type":"user","message":{"content":"earlier input"}}`+"\n")
				join := dja651Append(t, f, transcriptPath, id, transcriptUserLine(t, prompt))
				defer join()
				code, out, stderr := f.run([]string{"send", "w0test:p0a", "--now", "--timeout", "1s", "hello"})
				join()
				status, enters := "uncertain", 0
				if stalled {
					status = "stalled"
				}
				switch kind {
				case "clean":
					status = "sent"
					if code != 0 || out != "sent to local/w0test:p0a\n" || stderr != "" {
						t.Errorf("legitimate Claude transcript regressed: code=%d out=%q stderr=%q", code, out, stderr)
					}
				case "composer":
					enters = 1
					if code != 15 || out != "" {
						t.Errorf("Claude composer incorrectly proved receipt: code=%d out=%q stderr=%q", code, out, stderr)
					}
				case "dialog":
					expected := 17
					status = "dialog"
					if stalled {
						expected = 15
						status = "stalled"
					}
					if code != expected || out != "" {
						t.Errorf("Claude dialog incorrectly proved receipt: code=%d out=%q stderr=%q", code, out, stderr)
					}
				case "history_refusal":
					status = "refused"
					if code != 18 || out != "" || !strings.Contains(stderr, "refused-inbound-off") {
						t.Errorf("off-screen Claude refusal lost precedence: code=%d out=%q stderr=%q", code, out, stderr)
					}
				}
				dja651Log(t, dja651StateDir(f), status, id, "local/-", "local/w0test:p0a")
				dja651Submission(t, f, prompt, enters)
			})
		}
	}
}
