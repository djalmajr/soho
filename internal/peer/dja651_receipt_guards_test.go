package peer_test

import (
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/peer"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestDJA651ReceiptShortcutGuards(t *testing.T) {
	for _, path := range []string{"normal", "stalled", "moved_sequence", "screen_history", "moved_screen", "stalled_screen", "stalled_history"} {
		for _, frame := range []string{"clean", "queue", "composer", "composer_with_history", "dialog", "unknown", "unknown_queue", "unknown_arrow", "unreadable", "refusal", "history_refusal"} {
			t.Run(path+"/"+frame, func(t *testing.T) {
				id, prompt := stalledID(t)
				pre := codexPreScreen(t)
				post := pre
				stalled := strings.HasPrefix(path, "stalled")
				switch frame {
				case "queue":
					post = strings.Replace(pre, "› Ask Codex to do anything", "↳ "+peer.PeerOpenTag(id)+" pending\n\n› Ask Codex to do anything", 1)
				case "composer":
					post = strings.Replace(pre, "› Ask Codex to do anything", "› "+peer.PeerOpenTag(id)+" pending", 1)
				case "composer_with_history":
					post = peer.PeerEndLine(id) + "\n" + strings.Replace(pre, "› Ask Codex to do anything", "› "+peer.PeerOpenTag(id)+" pending", 1)
				case "dialog":
					post = "Do you trust this workspace? [y/N]\n" + pre
				case "unknown":
					post = "unknown renderer with no recognized composer\n"
				case "unknown_queue":
					post = "Pending follow-up for " + peer.PeerOpenTag(id) + "\n" + pre
				case "unknown_arrow":
					post = "→ " + peer.PeerOpenTag(id) + "\n" + pre
				case "refusal":
					post = "  └ [soho:peer-refused:" + id + "] refused-inbound-off\n" + pre
				}
				p := dja651PromptRule(prompt)
				p.Delay = 100
				if stalled {
					p.Code, p.Stderr = 1, stalledPromptErr
				}
				before, after := dja651Agent(true), dja651Agent(true)
				recent := "old history without receipt\n"
				if strings.HasSuffix(path, "_screen") {
					before = agentJSONKind("codex", "idle", "1")
					after = agentJSONKind("codex", "working", "2")
				}
				if path == "screen_history" || path == "stalled_history" {
					before = agentJSONKind("codex", "working", "1")
					after = before
					recent = prompt + "\n"
					post = strings.Replace(post, "35m 51s", "35m 52s", 1)
				}
				if path == "moved_sequence" {
					before = strings.Replace(before, `"working"`, `"idle"`, 1)
					after = strings.Replace(after, `"state_change_seq":"1"`, `"state_change_seq":"2"`, 1)
				}
				if frame == "history_refusal" {
					recent = "[soho:peer-refused:" + id + "] refused-inbound-off\n"
				}
				visible := fakecli.Rule{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Stdout: post}
				if frame == "unreadable" {
					visible.Stdout, visible.Stderr, visible.Code = "", "fixture visible unavailable", 1
				}
				rules := []fakecli.Rule{
					{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 1, Stdout: before},
					{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 2, Stdout: before},
					{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 3, Stdout: before},
					{Argv: []string{"agent", "get", "w0test:p0a"}, Stdout: after},
					{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 1, Stdout: pre},
					visible,
					{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: recent}, p,
					{Argv: []string{"agent", "send-keys", "w0test:p0a", "enter"}},
				}
				f := newFixture(t, rules)
				dja651Isolate(f)
				journal := dja651Journal(t, f, `{"type":"session_meta","payload":{"id":"`+dja651Session+`"}}`+"\n")
				join := dja651Append(t, f, journal, id, dja651Record(t, "user", prompt))
				defer join()
				code, out, stderr := f.run([]string{"send", "w0test:p0a", "--now", "--timeout", "1s", "hello"})
				join()
				status, enters := "uncertain", 0
				if stalled {
					status = "stalled"
				}
				switch frame {
				case "clean":
					status = "sent"
					if code != 0 || out != "sent to local/w0test:p0a\n" || stderr != "" {
						t.Errorf("clean receipt: code=%d out=%q stderr=%q", code, out, stderr)
					}
				case "queue":
					status = "queued"
					if code != 0 || out != "queued for local/w0test:p0a: it takes the message when its current turn ends\n" || stderr != "" {
						t.Errorf("queue shortcut bypass: code=%d out=%q stderr=%q", code, out, stderr)
					}
				case "refusal", "history_refusal":
					status = "refused"
					if code != 18 || out != "" || !strings.Contains(stderr, "refused-inbound-off") {
						t.Errorf("refusal shortcut bypass: code=%d out=%q stderr=%q", code, out, stderr)
					}
				case "dialog":
					if !stalled {
						status = "dialog"
						if code != 17 || out != "" {
							t.Errorf("dialog shortcut bypass: code=%d out=%q stderr=%q", code, out, stderr)
						}
					} else if code != 15 || out != "" {
						t.Errorf("stalled dialog proved receipt: %d %q %q", code, out, stderr)
					}
				default:
					if frame == "composer" || frame == "composer_with_history" {
						enters = 1
					}
					if frame == "unreadable" && !stalled {
						status = "unverified"
					}
					if code != 15 || out != "" {
						t.Errorf("unsafe frame proved receipt: code=%d out=%q stderr=%q", code, out, stderr)
					}
				}
				dja651Log(t, dja651StateDir(f), status, id, "local/-", "local/w0test:p0a")
				dja651Submission(t, f, prompt, enters)
			})
		}
	}
}

func TestDJA651ReceiptTargetBindingChanges(t *testing.T) {
	for _, when := range []int{3, 4, 5} {
		for _, field := range []string{"pane", "provider", "session", "missing", "state", "transient_session"} {
			t.Run(field+string(rune('0'+when)), func(t *testing.T) {
				id, prompt := stalledID(t)
				meta := dja651Agent(true)
				changed := meta
				switch field {
				case "pane":
					changed = strings.Replace(meta, `w0test:p0a`, `w0test:p0b`, 1)
				case "provider":
					changed = strings.ReplaceAll(meta, `codex`, `pi`)
				case "session", "transient_session":
					changed = strings.Replace(meta, dja651Session, "aaaaaaaa-2222-4333-8444-555555555555", 1)
				case "missing":
					changed = agentJSONKind("codex", "working", "1")
				case "state":
					changed = strings.Replace(meta, `"working"`, `"unsupported"`, 1)
				}
				get := []string{"agent", "get", "w0test:p0a"}
				rules := []fakecli.Rule{}
				for n := 1; n < when; n++ {
					rules = append(rules, fakecli.Rule{Argv: get, Call: n, Stdout: meta})
				}
				if field == "transient_session" {
					rules = append(rules, fakecli.Rule{Argv: get, Call: when, Stdout: changed}, fakecli.Rule{Argv: get, Stdout: meta})
				} else {
					rules = append(rules, fakecli.Rule{Argv: get, Stdout: changed})
				}
				rules = append(rules, fakecli.Rule{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Stdout: codexPreScreen(t)}, fakecli.Rule{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: "no receipt\n"})
				p := dja651PromptRule(prompt)
				p.Delay = 100
				rules = append(rules, p)
				f := newFixture(t, rules)
				dja651Isolate(f)
				journal := dja651Journal(t, f, `{"type":"session_meta","payload":{"id":"`+dja651Session+`"}}`+"\n")
				join := dja651Append(t, f, journal, id, dja651Record(t, "user", prompt))
				defer join()
				code, out, stderr := f.run([]string{"send", "w0test:p0a", "--now", "--timeout", "1s", "hello"})
				join()
				if code != 15 || out != "" {
					t.Errorf("changed actual target binding proved receipt: code=%d out=%q stderr=%q", code, out, stderr)
				}
				dja651Log(t, dja651StateDir(f), "uncertain", id, "local/-", "local/w0test:p0a")
				dja651Submission(t, f, prompt, 0)
			})
		}
	}
}
