package peer_test

import (
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/peer"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestDJA651NormalTypedReceiptControls(t *testing.T) {
	for _, kind := range []string{"old_match", "assistant", "tool", "quoted", "nested", "prefix", "suffix", "duplicate_key", "wrong_header"} {
		t.Run(kind, func(t *testing.T) {
			id, prompt := stalledID(t)
			pre := codexPreScreen(t)
			p := dja651PromptRule(prompt)
			p.Delay = 100
			f := newFixture(t, []fakecli.Rule{
				{Argv: []string{"agent", "get", "w0test:p0a"}, Stdout: dja651Agent(true)},
				{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Stdout: pre},
				{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: "history without receipt\n"}, p,
			})
			dja651Isolate(f)
			base := `{"type":"session_meta","payload":{"id":"` + dja651Session + `"}}` + "\n"
			line := dja651Record(t, "user", prompt)
			switch kind {
			case "old_match":
				base += line + "\n"
				line = dja651Record(t, "user", "unrelated new input")
			case "assistant", "tool":
				line = dja651Record(t, kind, prompt)
			case "quoted":
				line = dja651Record(t, "user", "> "+peer.PeerEndLine(id))
			case "nested":
				line = `{"type":"event_msg","payload":{"quoted":` + line + `}}`
			case "prefix":
				line = dja651Record(t, "user", "prefix "+peer.PeerEndLine(id))
			case "suffix":
				line = dja651Record(t, "user", peer.PeerEndLine(id)+" suffix")
			case "duplicate_key":
				line = strings.Replace(line, `"role":"user"`, `"role":"assistant","role":"user"`, 1)
			case "wrong_header":
				base = strings.Replace(base, dja651Session, "aaaaaaaa-2222-4333-8444-555555555555", 1)
			}
			journal := dja651Journal(t, f, base)
			join := dja651Append(t, f, journal, id, line)
			defer join()
			code, out, stderr := f.run([]string{"send", "w0test:p0a", "--now", "--timeout", "1s", "hello"})
			join()
			if code != 15 || out != "" {
				t.Errorf("%s became positive receipt: code=%d out=%q stderr=%q", kind, code, out, stderr)
			}
			dja651Log(t, dja651StateDir(f), "uncertain", id, "local/-", "local/w0test:p0a")
			dja651Screen(t, dja651StateDir(f), id, pre, out, stderr, true)
			dja651Submission(t, f, prompt, 0)
		})
	}
}
