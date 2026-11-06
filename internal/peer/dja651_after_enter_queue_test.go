package peer_test

import (
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/peer"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestDJA651AfterEnterQueueIsQueued(t *testing.T) {
	for _, stalled := range []bool{false, true} {
		name := "normal"
		if stalled {
			name = "stalled"
		}
		t.Run(name, func(t *testing.T) {
			id, prompt := stalledID(t)
			pre := codexPreScreen(t)
			box := strings.Replace(pre, "› Ask Codex to do anything", "› "+peer.PeerOpenTag(id)+" pending", 1)
			queue := "↳ " + peer.PeerOpenTag(id) + " pending\n" + pre
			visible := []string{"agent", "read", "w0test:p0a", "--source", "visible"}
			recent := []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}
			p := dja651PromptRule(prompt)
			if stalled {
				p.Code, p.Stderr = 1, stalledPromptErr
			}
			rules := []fakecli.Rule{
				{Argv: []string{"agent", "get", "w0test:p0a"}, Stdout: agentJSONKind("codex", "working", "1"), Delay: 5},
				{Argv: visible, Call: 1, Stdout: pre}, {Argv: visible, Call: 2, Stdout: box}, {Argv: visible, Stdout: queue}, p,
				{Argv: []string{"agent", "send-keys", "w0test:p0a", "enter"}},
			}
			if !stalled {
				rules = append(rules, fakecli.Rule{Argv: recent, ArgvPrefix: true, Call: 1, Stdout: "history without receipt\n"})
			}
			rules = append(rules, fakecli.Rule{Argv: recent, ArgvPrefix: true, Stdout: prompt + "\n"})
			f := newFixture(t, rules)
			dja651Isolate(f)
			code, out, stderr := f.run([]string{"send", "w0test:p0a", "--now", "--timeout", "1s", "hello"})
			if code != 0 || out != "queued for local/w0test:p0a: it takes the message when its current turn ends\n" || stderr != "" {
				t.Errorf("observed post-Enter queue: code=%d out=%q stderr=%q", code, out, stderr)
			}
			dja651Log(t, dja651StateDir(f), "queued", id, "local/-", "local/w0test:p0a")
			dja651Submission(t, f, prompt, 1)
		})
	}
}
