package peer_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/peer"
	"github.com/djalmajr/soho/internal/peerauth"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// The receiver's hook refuses a prompt over its size limit with a line that
// names no message: `[soho:peer-refused] prompt-too-large`. The send reads it
// as the refusal of its own message only when the line is new: more of them
// on the screen than before the prompt, or a system entry in the transcript
// written after the send began.

const tooLargeText = peerauth.TooLargeLine + "\n" + peerauth.TooLargeReason

// signID is the fixed message id the transcript fixtures bind to.
const signID = "01020304"

func peerMessagesLog(t *testing.T, f *fixture) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.env["SOHO_DIR"], "ws-test", peer.PeerLogFile))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// tooLargeScreen is the claude composer under the given lines, with the
// prompt in the box when prompt is not empty.
func tooLargeScreen(above, prompt string) string {
	box := "❯"
	if prompt != "" {
		box = "❯ " + prompt
	}
	if above != "" {
		above += "\n"
	}
	return above + claudeBoxSep + "\n" + box + "\n" + claudeBoxSep + "\nfooter\n"
}

// tooLargeRules is the claude send of the review: stalled is the prompt call
// that does not confirm (the screen path), otherwise the state sequence
// moves. pre is the visible screen before the prompt, post the one after,
// recent the recent-unwrapped read.
func tooLargeRules(prompt string, stalled bool, pre, post, recent string) []fakecli.Rule {
	promptRule := fakecli.Rule{Argv: []string{"agent", "prompt", "w0test:p0a", prompt, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000"}}
	seq := "2"
	if stalled {
		promptRule.Code = 1
		promptRule.Stderr = stalledPromptErr
		seq = "1"
	}
	var rules []fakecli.Rule
	for i := 1; i <= 4; i++ {
		rules = append(rules, fakecli.Rule{Argv: []string{"agent", "get", "w0test:p0a"}, Call: i, Stdout: agentJSONKind("claude", "done", "1")})
	}
	return append(rules,
		fakecli.Rule{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 1, Stdout: pre},
		promptRule,
		fakecli.Rule{Argv: []string{"agent", "get", "w0test:p0a"}, ArgvPrefix: true, Stdout: agentJSONKind("claude", "done", seq)},
		fakecli.Rule{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, ArgvPrefix: true, Stdout: post},
		fakecli.Rule{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: recent},
		fakecli.Rule{Argv: []string{"agent", "send-keys", "w0test:p0a", "enter"}, Code: 0},
	)
}

func runTooLarge(t *testing.T, stalled bool, pre, post, recent string) (int, string, string, *fixture) {
	t.Helper()
	_, prompt := stalledID(t)
	f := newFixture(t, tooLargeRules(prompt, stalled, pre, post, recent))
	f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
	code, out, stderr := f.run([]string{"send", "w0test:p0a", "hello"})
	return code, out, stderr, f
}

const tooLargeStderr = "soho: send: local/w0test:p0a refused the message (prompt-too-large); the refused text stays in its input until someone clears it\n"

func requireTooLargeRefusal(t *testing.T, code int, out, stderr string, f *fixture) {
	t.Helper()
	if code != 18 || out != "" || stderr != tooLargeStderr {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	if enters := countSendKeyEnters(t, f); enters != 0 {
		t.Fatalf("a refused message got %d Enter", enters)
	}
	log := peerMessagesLog(t, f)
	if strings.Contains(log, "\tsent\t") || !strings.Contains(log, "\trefused\t") {
		t.Fatalf("peer-messages.tsv = %q", log)
	}
}

func requireNoTooLargeRefusal(t *testing.T, code int, stderr string, f *fixture) {
	t.Helper()
	if code == 18 || strings.Contains(stderr, "refused the message") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if strings.Contains(peerMessagesLog(t, f), "\trefused\t") {
		t.Fatalf("peer-messages.tsv records a refusal: %q", peerMessagesLog(t, f))
	}
}

func TestSendPiTooLargeRefusal(t *testing.T) {
	line := " Error: " + peerauth.TooLargeLine
	for _, tc := range []struct {
		name                  string
		before, after, recent string
		stalled, composer     bool
		refused               bool
	}{
		{"consumed", "", line, line, true, false, true},
		{"stalled-composer", "", line, line, true, true, true},
		{"composer-error", "", "› Error: " + peerauth.TooLargeLine, line, true, false, true},
		{"recent-only", "", "done", line, false, false, true},
		{"already-visible", line, line, line, true, true, false},
		{"new-count", line, line + "\n" + line, line + "\n" + line, true, true, true},
		{"quoted", "", ">" + line, ">" + line, true, true, false},
		{"error-quoted", "", "Error: > " + peerauth.TooLargeLine, "Error: > " + peerauth.TooLargeLine, true, true, false},
		{"other-prefix", "", "Warning: " + peerauth.TooLargeLine, "Warning: " + peerauth.TooLargeLine, true, true, false},
		{"bare", "", peerauth.TooLargeLine, peerauth.TooLargeLine, true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, prompt := stalledID(t)
			composer := ""
			if tc.composer {
				composer = prompt
			}
			rules := tooLargeRules(prompt, tc.stalled, piSendScreen(tc.before, ""), piSendScreen(tc.after, composer), tc.recent)
			for i := range rules {
				if len(rules[i].Argv) >= 2 && rules[i].Argv[0] == "agent" && rules[i].Argv[1] == "get" {
					seq := "1"
					if rules[i].Call == 0 && !tc.stalled {
						seq = "2"
					}
					rules[i].Stdout = agentJSONKind("pi", "done", seq)
				}
			}
			f := newFixture(t, rules)
			f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
			code, out, stderr := f.run([]string{"send", "w0test:p0a", "hello"})
			if !tc.refused {
				requireNoTooLargeRefusal(t, code, stderr, f)
				return
			}
			const want = "soho: send: local/w0test:p0a refused the message (prompt-too-large); it did not reach the model\n"
			if code != 18 || out != "" || stderr != want {
				t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
			}
			if enters := countSendKeyEnters(t, f); enters != 0 {
				t.Fatalf("a refused pi message got %d extra Enter", enters)
			}
			log := peerMessagesLog(t, f)
			if strings.Contains(log, "\tsent\t") || !strings.Contains(log, "\trefused\t") {
				t.Fatalf("peer-messages.tsv = %q", log)
			}
		})
	}
}

func TestSendReadsTheTooLargeRefusal(t *testing.T) {
	_, prompt := stalledID(t)
	clean := tooLargeScreen("", "")
	withReason := tooLargeScreen(tooLargeText, prompt)

	t.Run("the stalled composer: the new line on the screen exits 18 and presses nothing", func(t *testing.T) {
		code, out, stderr, f := runTooLarge(t, true, clean, withReason, withReason)
		requireTooLargeRefusal(t, code, out, stderr, f)
	})
	t.Run("the moved sequence: the new line on the visible screen exits 18", func(t *testing.T) {
		code, out, stderr, f := runTooLarge(t, false, clean, withReason, withReason)
		requireTooLargeRefusal(t, code, out, stderr, f)
	})
	t.Run("the moved sequence: the new line only in the recent history exits 18", func(t *testing.T) {
		code, out, stderr, f := runTooLarge(t, false, clean, tooLargeScreen("done, the refusal scrolled out", ""), "earlier output\n"+tooLargeText+"\n")
		requireTooLargeRefusal(t, code, out, stderr, f)
	})
	t.Run("one more line than before the prompt is a new refusal", func(t *testing.T) {
		before := tooLargeScreen(tooLargeText, "")
		after := tooLargeScreen(tooLargeText+"\n"+tooLargeText, prompt)
		code, out, stderr, f := runTooLarge(t, true, before, after, after)
		requireTooLargeRefusal(t, code, out, stderr, f)
	})
	t.Run("an old refusal already on the screen before the prompt is not this send's", func(t *testing.T) {
		before := tooLargeScreen(tooLargeText, "")
		for _, stalled := range []bool{true, false} {
			name := "sequence"
			if stalled {
				name = "stalled composer"
			}
			t.Run(name, func(t *testing.T) {
				code, _, stderr, f := runTooLarge(t, stalled, before, tooLargeScreen(tooLargeText, prompt), tooLargeScreen(tooLargeText, prompt))
				requireNoTooLargeRefusal(t, code, stderr, f)
			})
		}
	})
	t.Run("an old refusal in the recent history with the same count is not this send's", func(t *testing.T) {
		before := tooLargeScreen(tooLargeText, "")
		code, _, stderr, f := runTooLarge(t, false, before, tooLargeScreen("done", ""), "earlier output\n"+tooLargeText+"\n")
		requireNoTooLargeRefusal(t, code, stderr, f)
	})
	t.Run("a quoted refusal line is body, not a refusal", func(t *testing.T) {
		quoted := "> " + peerauth.TooLargeLine
		for _, stalled := range []bool{true, false} {
			name := "sequence"
			if stalled {
				name = "stalled composer"
			}
			t.Run(name, func(t *testing.T) {
				code, _, stderr, f := runTooLarge(t, stalled, clean, tooLargeScreen(quoted, prompt), "earlier output\n"+quoted+"\n")
				requireNoTooLargeRefusal(t, code, stderr, f)
			})
		}
	})
	t.Run("the control: without the line the stalled message in the box gets its Enter", func(t *testing.T) {
		code, _, stderr, f := runTooLarge(t, true, clean, tooLargeScreen("", prompt), tooLargeScreen("", prompt))
		requireNoTooLargeRefusal(t, code, stderr, f)
		if enters := countSendKeyEnters(t, f); enters != 1 {
			t.Fatalf("the control needs its Enter to be meaningful: %d", enters)
		}
	})
}

// A local claude's transcript holds the hook's block as a `system` entry. Only
// one written after the send began is this send's refusal.
func TestSendReadsTheTooLargeRefusalFromTheTranscript(t *testing.T) {
	cwd := "/tmp/claude-work"
	systemLine := `{"type":"system","subtype":"informational","content":"UserPromptSubmit operation blocked by hook:\n` + peerauth.TooLargeLine + `\n` + strings.ReplaceAll(peerauth.TooLargeReason, "\n", `\n`) + `"}`
	userLine := `{"type":"user","message":{"content":"earlier conversation"}}`
	claudeRules := func(prompt string) []fakecli.Rule {
		return []fakecli.Rule{
			{Argv: []string{"agent", "get", "w0test:p0a"}, ArgvPrefix: true, Stdout: claudeSessionAgentJSON("working", "1", cwd)},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, ArgvPrefix: true, Stdout: "✻ Cranking out changes…\n"},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stderr: notIdleErr, Code: 1},
			{Argv: []string{"agent", "prompt", "w0test:p0a", prompt, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000"}, Delay: 100},
		}
	}
	// run seeds the transcript with initial, appends appended after the prompt
	// call and sends.
	run := func(t *testing.T, initial, appended string) (int, string, string, *fixture) {
		_, prompt := stalledID(t)
		configRoot := t.TempDir()
		transcriptPath := claudeTranscriptPath(t, configRoot, cwd)
		writeClaudeTranscript(t, transcriptPath, initial)
		f := newFixture(t, claudeRules(prompt))
		f.env["CLAUDE_CONFIG_DIR"] = configRoot
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		done := appendAfterPrompt(t, filepath.Join(filepath.Dir(f.bin), "herdr.calls.jsonl"), transcriptPath, signID, appended)
		code, out, stderr := f.run([]string{"send", "w0test:p0a", "--now", "--timeout", "1000", "hello"})
		if !<-done {
			t.Fatal("the transcript line was not appended after the prompt call")
		}
		return code, out, stderr, f
	}
	t.Run("a system entry written after the send began exits 18 and presses nothing", func(t *testing.T) {
		code, out, stderr, f := run(t, userLine+"\n", systemLine)
		requireTooLargeRefusal(t, code, out, stderr, f)
	})
	t.Run("an entry before the send began is an old refusal", func(t *testing.T) {
		code, out, stderr, f := run(t, userLine+"\n"+systemLine+"\n", `{"type":"user","message":{"content":"`+peer.PeerEndLine(signID)+`"}}`)
		if code != 0 || out != "sent to local/w0test:p0a\n" {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
		requireNoTooLargeRefusal(t, code, stderr, f)
	})
	t.Run("a new entry after an old one is still the refusal", func(t *testing.T) {
		code, out, stderr, f := run(t, userLine+"\n"+systemLine+"\n", systemLine)
		requireTooLargeRefusal(t, code, out, stderr, f)
	})
	t.Run("an assistant line that merely cites the refusal is not one", func(t *testing.T) {
		cite := `{"type":"assistant","message":{"content":"` + peerauth.TooLargeLine + `"}}`
		code, _, stderr, f := run(t, userLine+"\n", cite)
		requireNoTooLargeRefusal(t, code, stderr, f)
	})
}

// The baseline of the transcript is the file's identity, its size and the
// time the send began: a file truncated or replaced after the prompt is read
// by the timestamp of its entries instead of by an offset that no longer
// means anything.
func TestSendReadsTheTooLargeRefusalFromAReplacedTranscript(t *testing.T) {
	cwd := "/tmp/claude-work-replaced"
	system := func(timestamp string) string {
		entry := map[string]any{"type": "system", "subtype": "informational", "content": tooLargeText}
		if timestamp != "" {
			entry["timestamp"] = timestamp
		}
		raw, err := json.Marshal(entry)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	user := `{"type":"user","message":{"content":"` + peer.PeerEndLine(signID) + `"}}`
	now := func() string { return time.Now().UTC().Format(time.RFC3339Nano) }
	old := func() string { return time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano) }
	// run seeds a long transcript, then, once the prompt call is logged, either
	// truncates it to fresh or renames it away and creates fresh at the same
	// path.
	run := func(t *testing.T, replace string, fresh string) (int, string, string, *fixture) {
		_, prompt := stalledID(t)
		root := t.TempDir()
		path := claudeTranscriptPath(t, root, cwd)
		writeClaudeTranscript(t, path, `{"type":"user","message":{"content":"`+strings.Repeat("old padding ", 500)+`"}}`+"\n")
		rules := []fakecli.Rule{
			{Argv: []string{"agent", "get", "w0test:p0a"}, ArgvPrefix: true, Stdout: claudeSessionAgentJSON("working", "1", cwd)},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, ArgvPrefix: true, Stdout: "✻ Cranking out changes…\n"},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stderr: notIdleErr, Code: 1},
			{Argv: []string{"agent", "prompt", "w0test:p0a", prompt, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000"}, Delay: 100},
		}
		f := newFixture(t, rules)
		f.env["CLAUDE_CONFIG_DIR"] = root
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "10", "1"
		done := make(chan error, 1)
		callsLog := filepath.Join(filepath.Dir(f.bin), "herdr.calls.jsonl")
		go func() {
			until := time.Now().Add(5 * time.Second)
			for time.Now().Before(until) {
				calls, _ := fakecli.ReadCalls(callsLog)
				for _, c := range calls {
					for i := 0; i+3 < len(c.Argv); i++ {
						if c.Argv[i] == "agent" && c.Argv[i+1] == "prompt" && strings.Contains(c.Argv[i+3], "#"+signID) {
							var err error
							if replace == "rotate" {
								err = os.Rename(path, path+".before")
							}
							if err == nil {
								err = os.WriteFile(path, []byte(fresh), 0o600)
							}
							done <- err
							return
						}
					}
				}
				time.Sleep(time.Millisecond)
			}
			done <- os.ErrDeadlineExceeded
		}()
		code, out, stderr := f.run([]string{"send", "w0test:p0a", "--now", "--timeout", "1000", "hello"})
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		return code, out, stderr, f
	}
	// A file rewritten in place (truncate) or replaced (rotate) that grows past
	// the baseline's length before the read: its bytes before the old offset
	// no longer end as they did, so the offset is not trusted.
	for _, replace := range []string{"truncate", "rotate"} {
		t.Run(replace+" to a longer file: a new refusal is seen", func(t *testing.T) {
			longer := `{"type":"user","message":{"content":"` + peer.PeerEndLine(signID) + `"},"pad":"` + strings.Repeat("new padding ", 1000) + `"}`
			code, out, stderr, f := run(t, replace, system(now())+"\n"+longer+"\n")
			requireTooLargeRefusal(t, code, out, stderr, f)
		})
		t.Run(replace+" to a longer file: a refusal from before the send began does not count", func(t *testing.T) {
			longer := `{"type":"user","message":{"content":"` + peer.PeerEndLine(signID) + `"},"pad":"` + strings.Repeat("new padding ", 1000) + `"}`
			code, out, stderr, f := run(t, replace, system(old())+"\n"+longer+"\n")
			if code != 0 || out != "sent to local/w0test:p0a\n" {
				t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
			}
			requireNoTooLargeRefusal(t, code, stderr, f)
		})
	}
	for _, replace := range []string{"truncate", "rotate"} {
		t.Run(replace+": a new refusal is seen", func(t *testing.T) {
			code, out, stderr, f := run(t, replace, system(now())+"\n"+user+"\n")
			requireTooLargeRefusal(t, code, out, stderr, f)
		})
		t.Run(replace+": a refusal from before the send began does not count", func(t *testing.T) {
			code, out, stderr, f := run(t, replace, system(old())+"\n"+user+"\n")
			if code != 0 || out != "sent to local/w0test:p0a\n" {
				t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
			}
			requireNoTooLargeRefusal(t, code, stderr, f)
		})
		t.Run(replace+": a refusal without a timestamp counts", func(t *testing.T) {
			code, out, stderr, f := run(t, replace, system("")+"\n"+user+"\n")
			requireTooLargeRefusal(t, code, out, stderr, f)
		})
	}
}
