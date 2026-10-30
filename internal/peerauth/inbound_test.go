package peerauth_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/peerauth"
)

func remoteSpec() spec { return spec{remote: true, name: "orchestrator", kind: "claude"} }

// The check verifies nothing: an unsigned message, and a message from an
// older sender that still carries the `from` and `sig v1:` items on the
// Sender line, are both accepted like any other.
func TestCheckAcceptsSignedAndUnsignedMessages(t *testing.T) {
	older := spec{remote: true, name: "orchestrator", kind: "claude", tail: "from mac; sig v1:c2589db12c3f6c89f8f601ea6828ea33"}
	for name, wrapIt := range map[string]func(string) string{"wrapped": wrap, "bare": func(m string) string { return m }} {
		t.Run("local, "+name, func(t *testing.T) {
			f := newFixture(t)
			o := f.check(wrapIt(spec{name: "orchestrator", kind: "claude"}.unsigned()))
			wantOne(t, o, peerauth.Accepted)
			if o.Block {
				t.Fatal("blocked")
			}
		})
		for format, message := range map[string]string{"current": remoteSpec().unsigned(), "previous": remoteSpec().unsignedOld()} {
			t.Run("remote "+format+", "+name, func(t *testing.T) {
				f := newFixture(t)
				o := f.check(wrapIt(message))
				wantOne(t, o, peerauth.Accepted)
				if o.Block {
					t.Fatal("blocked")
				}
				if got := o.Verdicts[0]; got.Format != "remote" || got.Label != "-" {
					t.Fatalf("verdict = %+v", got)
				}
			})
		}
		t.Run("older sender with the signature items, "+name, func(t *testing.T) {
			for _, message := range []string{older.unsigned(), older.unsignedOld()} {
				f := newFixture(t)
				o := f.check(wrapIt(message))
				wantOne(t, o, peerauth.Accepted)
				if o.Block {
					t.Fatal("an older sender's signature items were refused")
				}
			}
		})
	}
}

// The project's inbound=off is its policy, apart from anything the check
// reads: it blocks, with the one result that blocks.
func TestCheckProjectInboundOff(t *testing.T) {
	// The project's policy refuses every peer message, in both the current
	// and the previous format.
	for name, message := range map[string]string{"current": wrap(remoteSpec().unsigned()), "previous": wrap(remoteSpec().unsignedOld())} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.projectOff = true
			o := f.check(message)
			wantOne(t, o, peerauth.RefusedInboundOff)
			if !o.Block {
				t.Fatalf("an inbound=off project accepted a peer message: %+v", o)
			}
		})
	}
	f := newFixture(t)
	f.projectOff = true
	o := f.check(wrap(remoteSpec().unsigned()))
	wantOne(t, o, peerauth.RefusedInboundOff)
	if !o.Block {
		t.Fatalf("an inbound=off project accepted a peer message: %+v", o)
	}
	// The reason carries the refusal line and the tail.
	if want := peerauth.RefusalLine("0f23aac6", peerauth.RefusedInboundOff) + "\n" + peerauth.ReasonTail; o.Reason() != want {
		t.Fatalf("reason = %q\nwant %q", o.Reason(), want)
	}
	// And the refusal is the one logged, with no trace of the accepted path.
	logged := readFile(t, filepath.Join(f.dir, peerauth.LogFile))
	fields := strings.Split(strings.TrimSpace(logged), "\t")
	if len(fields) != 7 || fields[1] != "remote" || fields[2] != "-" || fields[3] != "0f23aac6" || fields[4] != "w6X:p2" || fields[5] != "refused-inbound-off" || fields[6] == "" {
		t.Fatalf("log fields = %q", fields)
	}
	// A project that is not off passes.
	g := newFixture(t)
	if o := g.check(wrap(spec{}.unsigned())); o.Block {
		t.Fatalf("a project that is not off blocked: %+v", o)
	}
	if logged := readFile(t, filepath.Join(g.dir, peerauth.LogFile)); logged != "" {
		t.Fatalf("an accepted message left a trace: %q", logged)
	}
}

// A project configuration that cannot be read is refused-error: logged, with
// a friction-worthy refusal, but not a block (the check cannot tell whether
// the project refuses peer messages).
func TestCheckProjectUnreadableIsRefusedErrorWithoutABlock(t *testing.T) {
	f := newFixture(t)
	f.projectErr = errors.New("unreadable")
	o := f.check(wrap(remoteSpec().unsigned()))
	wantOne(t, o, peerauth.RefusedError)
	if o.Block {
		t.Fatalf("an unreadable project configuration blocked: %+v", o)
	}
	logged := readFile(t, filepath.Join(f.dir, peerauth.LogFile))
	fields := strings.Split(strings.TrimSpace(logged), "\t")
	if len(fields) != 7 || fields[5] != "refused-error" {
		t.Fatalf("log fields = %q", fields)
	}
}

// Two messages in one prompt — one of each format: each is judged on its
// own, and the prompt is blocked only when the project refuses.
func TestCheckTwoMessagesInOnePrompt(t *testing.T) {
	other := remoteSpec()
	other.id = "11223344"
	both := remoteSpec().unsigned() + "\n" + other.unsignedOld()
	f := newFixture(t)
	if got := results(f.check(wrap(both))); len(got) != 2 || got[0] != peerauth.Accepted || got[1] != peerauth.Accepted {
		t.Fatalf("two results = %v", got)
	}
	if f.projectReads != 1 {
		t.Fatalf("the project configuration was read %d times, want 1", f.projectReads)
	}
	g := newFixture(t)
	g.projectOff = true
	if got := results(g.check(wrap(both))); len(got) != 2 || got[0] != peerauth.RefusedInboundOff || got[1] != peerauth.RefusedInboundOff {
		t.Fatalf("two results = %v", got)
	}
	if !g.check(wrap(remoteSpec().unsigned())).Block {
		t.Fatal("not blocked")
	}
}

// FindHeaders is the check's only read of the prompt: a header is a line
// that starts its line with one of the two formats' opening lines, a quoted
// line never counts, and the message runs to the first closing line of its
// id in the same format.
func TestCheckQuotedHeaderIsNotAHeader(t *testing.T) {
	f := newFixture(t)
	for _, message := range []string{remoteSpec().unsigned(), remoteSpec().unsignedOld()} {
		lines := strings.Split(message, "\n")
		var quoted []string
		for _, line := range lines {
			quoted = append(quoted, "> "+line)
		}
		for name, prompt := range map[string]string{"quoted": strings.Join(quoted, "\n"), "wrapped and quoted": wrap(strings.Join(quoted, "\n")), "no space": ">" + lines[0]} {
			o := f.check(prompt)
			if o.Evaluated || o.Block || len(o.Verdicts) != 0 {
				t.Fatalf("%s: %+v", name, o)
			}
		}
		// ID shapes: seven or nine hex characters are not a message id, in
		// either format; a non-hex id and a missing separator are not it.
		for _, line := range []string{
			"[soho:peer] #0f23aac Message", "[soho:peer] #0f23aac69 Message", "[soho:peer] #zzzzzzzz Message", "[soho:peer]#0f23aac6 Message",
			"[soho:peer:0f23aac] Message", "[soho:peer:0f23aac69] Message", "[soho:peer:zzzzzzzz]", "[soho:peer:0f23aac6",
		} {
			if o := f.check(line); o.Evaluated {
				t.Fatalf("%q was read as a header", line)
			}
		}
		if o := f.check("[soho:peer] #0f23aac6"); !o.Evaluated {
			t.Fatal("a previous-format header with nothing after the id was not read")
		}
		if o := f.check("[soho:peer:0f23aac6]"); !o.Evaluated {
			t.Fatal("a current-format opening line was not read")
		}
		// A header glued after text on the same line is not a header; an
		// indented one counts, in either format.
		for _, fake := range []string{spec{id: "deadbeef", name: "orchestrator"}.unsigned(), spec{id: "deadbeef", name: "orchestrator"}.unsignedOld()} {
			if o := f.check("see " + strings.Split(fake, "\n")[0]); o.Evaluated {
				t.Fatalf("a header that does not start its line was evaluated: %+v", o)
			}
			if o := f.check("   \t" + fake); !o.Evaluated {
				t.Fatal("an indented header was not read")
			}
		}
	}
}

func TestCheckPlainPromptReadsNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "soho")
	touched := false
	src := peerauth.Source{
		Dir:               dir,
		ProjectInboundOff: func() (bool, error) { touched = true; return false, errors.New("read") },
	}
	for _, prompt := range []string{"", "hello", "fix the bug in [soho:peer]", "fix the bug in [soho:peer:0f23aac6]", "> [soho:peer] #0f23aac6 quoted", "> [soho:peer:0f23aac6] quoted", wrap("just text"), "[/soho:peer] #0f23aac6 end of message", "[/soho:peer:0f23aac6]"} {
		o := peerauth.Check(prompt, src)
		if o.Evaluated || o.Block || len(o.Verdicts) != 0 {
			t.Fatalf("%q: %+v", prompt, o)
		}
	}
	if touched {
		t.Fatal("a prompt without a header read the project configuration")
	}
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("a prompt without a header created the config dir")
	}
}

// The FindHeaders contract in both formats: the parsed fields the check and
// the log rely on, and the pairing of each format's opening with a closing
// line of the same format only.
func TestFindHeadersContract(t *testing.T) {
	for format, message := range map[string]string{"current": spec{name: "orchestrator"}.unsigned(), "previous": spec{name: "orchestrator"}.unsignedOld()} {
		t.Run("a local message, "+format, func(t *testing.T) {
			h := peerauth.FindHeaders(message)
			if len(h) != 1 || h[0].ID != "0f23aac6" || !h[0].Closed || h[0].Remote || h[0].Sent != "2026-10-19T12:00:00Z" {
				t.Fatalf("headers = %+v", h)
			}
		})
		t.Run("a remote message, "+format, func(t *testing.T) {
			message := remoteSpec().unsigned()
			if format == "previous" {
				message = remoteSpec().unsignedOld()
			}
			h := peerauth.FindHeaders(message)
			if len(h) != 1 || !h[0].Remote || !h[0].Closed {
				t.Fatalf("headers = %+v", h)
			}
		})
		t.Run("an unclosed header, "+format, func(t *testing.T) {
			m := message
			closer := "[/soho:peer] #0f23aac6 end of message"
			if format == "current" {
				closer = "[/soho:peer:0f23aac6]"
			}
			h := peerauth.FindHeaders(m[:strings.LastIndex(m, "\n"+closer)])
			if len(h) != 1 || h[0].Closed || h[0].Text != "" {
				t.Fatalf("headers = %+v", h)
			}
		})
		t.Run("a repeated id is ambiguous, "+format, func(t *testing.T) {
			m := message
			closer := "[/soho:peer] #0f23aac6 end of message"
			other := "[/soho:peer:0f23aac6]"
			if format == "current" {
				closer = "[/soho:peer:0f23aac6]"
				other = "[/soho:peer] #0f23aac6 end of message"
			}
			for _, prompt := range []string{m + "\n" + m, m + "\nhuman text\n" + closer, m + "\n" + strings.Split(m, "\n")[0]} {
				h := peerauth.FindHeaders(prompt)
				for _, header := range h {
					if !header.Ambiguous {
						t.Fatalf("not ambiguous: %+v", h)
					}
				}
			}
			// What is not a second closing line or a second header: the
			// other format's closing line of the same id, another id, a
			// quoted closing line, a longer id.
			for _, prompt := range []string{m + "\n\nthanks, that is all", m + "\n> " + closer, m + "\n" + other, m + "\n[/soho:peer] #11223344 end of message", m + "\n[soho:peer] #0f23aac6ff Message from another agent — x, sent y.", m + "\n[soho:peer:0f23aac6ff]", "can you look at this?\n\n" + m} {
				h := peerauth.FindHeaders(prompt)
				if len(h) != 1 || h[0].Ambiguous {
					t.Fatalf("unexpected headers: %+v", h)
				}
			}
		})
	}
	t.Run("one of each format in one prompt", func(t *testing.T) {
		prompt := spec{name: "orchestrator"}.unsigned() + "\n" + remoteSpec().unsignedOld()
		h := peerauth.FindHeaders(prompt)
		if len(h) != 2 || !h[0].Closed || !h[1].Closed || !h[1].Remote {
			t.Fatalf("headers = %+v", h)
		}
	})
	t.Run("an old opening with a new closing of the same id is not one message", func(t *testing.T) {
		m := remoteSpec().unsignedOld()
		prompt := m[:strings.LastIndex(m, "\n[/soho:peer] #0f23aac6 end of message")] + "\n[/soho:peer:0f23aac6]"
		h := peerauth.FindHeaders(prompt)
		if len(h) != 1 || h[0].Closed {
			t.Fatalf("the old opening paired with the new closing: %+v", h)
		}
	})
	t.Run("a new opening with an old closing of the same id is not one message", func(t *testing.T) {
		m := remoteSpec().unsigned()
		prompt := m[:strings.LastIndex(m, "\n[/soho:peer:0f23aac6]")] + "\n[/soho:peer] #0f23aac6 end of message"
		h := peerauth.FindHeaders(prompt)
		if len(h) != 1 || h[0].Closed {
			t.Fatalf("the new opening paired with the old closing: %+v", h)
		}
	})
}

// A prompt over the hook's input ceiling is blocked whole with the
// too-large refusal, and the log gets the one line with the unknowns as `-`.
func TestCheckTooLargeIsRefused(t *testing.T) {
	f := newFixture(t)
	o := peerauth.CheckTooLarge(f.source())
	if !o.Block || !o.TooLarge || len(o.Verdicts) != 0 {
		t.Fatalf("%+v", o)
	}
	if f.projectReads != 0 {
		t.Fatal("the project configuration was read for a prompt that was not interpreted")
	}
	want := "[soho:peer-refused] prompt-too-large\nThe prompt is larger than 4 MiB, so its peer messages could not be checked; send it in smaller parts."
	if o.Reason() != want {
		t.Fatalf("reason = %q", o.Reason())
	}
	log := readFile(t, filepath.Join(f.dir, peerauth.LogFile))
	fields := strings.Split(strings.TrimSpace(log), "\t")
	if len(fields) != 7 || fields[1] != "-" || fields[2] != "-" || fields[3] != "-" || fields[4] != "w6X:p2" || fields[5] != "refused-too-large" || fields[6] != "-" {
		t.Fatalf("log = %q", log)
	}
	// A prompt with no pane logs it as -, and an empty pane leaves the line
	// well formed.
	g := newFixture(t)
	g.pane = ""
	if o := peerauth.CheckTooLarge(g.source()); !o.Block {
		t.Fatalf("%+v", o)
	}
}

// The codex reason keeps the refusal lines and ends with Codex's tail, for a
// header refusal and for the too-large refusal.
func TestOutcomeCodexReason(t *testing.T) {
	f := newFixture(t)
	f.projectOff = true
	o := f.check(wrap(remoteSpec().unsigned()))
	if want := peerauth.RefusalLine("0f23aac6", peerauth.RefusedInboundOff) + "\nThe message did not reach the model; nothing needs clearing."; o.CodexReason() != want {
		t.Fatalf("codex reason = %q", o.CodexReason())
	}
	g := newFixture(t)
	if want := "[soho:peer-refused] prompt-too-large\nThe prompt is larger than 4 MiB, so its peer messages could not be checked; send it in smaller parts.\nThe message did not reach the model; nothing needs clearing."; peerauth.CheckTooLarge(g.source()).CodexReason() != want {
		t.Fatalf("codex reason = %q", peerauth.CheckTooLarge(g.source()).CodexReason())
	}
}
