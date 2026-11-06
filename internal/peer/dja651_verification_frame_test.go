package peer_test

import (
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/peer"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestDJA651ReceiptVisibleChangesInsideVerification(t *testing.T) {
	for _, kind := range []string{"queue", "composer", "dialog", "unreadable", "unknown"} {
		t.Run(kind, func(t *testing.T) {
			id, prompt := stalledID(t)
			pre := codexPreScreen(t)
			clear := strings.Replace(pre, "35m 51s", "35m 52s", 1)
			post := clear
			read := []string{"agent", "read", "w0test:p0a", "--source", "visible"}
			switch kind {
			case "queue":
				post = "↳ " + peer.PeerOpenTag(id) + " pending\n" + clear
			case "composer":
				post = strings.Replace(clear, "› Ask Codex to do anything", "› "+peer.PeerOpenTag(id)+" pending", 1)
			case "dialog":
				post = "Do you trust this workspace? [y/N]\n" + clear
			case "unknown":
				post = "An unsupported renderer\n› Ask Codex to do anything\n"
			}
			final := fakecli.Rule{Argv: read, Stdout: post}
			if kind == "unreadable" {
				final.Stdout, final.Stderr, final.Code = "", "fixture visible unavailable", 1
			}
			p := dja651PromptRule(prompt)
			p.Delay = 100
			f := newFixture(t, []fakecli.Rule{
				{Argv: []string{"agent", "get", "w0test:p0a"}, Stdout: dja651Agent(true)},
				{Argv: read, Call: 1, Stdout: pre}, {Argv: read, Call: 2, Stdout: clear}, final,
				{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: "history without receipt\n"}, p,
				{Argv: []string{"agent", "send-keys", "w0test:p0a", "enter"}},
			})
			dja651Isolate(f)
			journal := dja651Journal(t, f, `{"type":"session_meta","payload":{"id":"`+dja651Session+`"}}`+"\n")
			join := dja651Append(t, f, journal, id, dja651Record(t, "user", prompt))
			defer join()
			code, out, stderr := f.run([]string{"send", "w0test:p0a", "--now", "--timeout", "1s", "hello"})
			join()
			status, enters := "uncertain", 0
			switch kind {
			case "queue":
				status = "queued"
				if code != 0 || out != "queued for local/w0test:p0a: it takes the message when its current turn ends\n" || stderr != "" {
					t.Errorf("queue appeared inside verification: code=%d out=%q stderr=%q", code, out, stderr)
				}
			case "dialog":
				status = "dialog"
				if code != 17 || out != "" {
					t.Errorf("dialog appeared inside verification: code=%d out=%q stderr=%q", code, out, stderr)
				}
			default:
				if kind == "composer" {
					enters = 1
				} else if kind == "unreadable" {
					status = "unverified"
					post = clear
				}
				if code != 15 || out != "" {
					t.Errorf("unsafe verification frame became sent: code=%d out=%q stderr=%q", code, out, stderr)
				}
				dja651Screen(t, dja651StateDir(f), id, post, out, stderr, true)
			}
			dja651Log(t, dja651StateDir(f), status, id, "local/-", "local/w0test:p0a")
			dja651Submission(t, f, prompt, enters)
		})
	}
}
