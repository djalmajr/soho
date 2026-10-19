package peerauth_test

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/peerauth"
)

func TestSignVerifyRoundTrip(t *testing.T) {
	dir := t.TempDir()
	key := mustKey(t, dir, "mac")
	cases := map[string]spec{
		"local, full sender":                      {name: "orchestrator", kind: "claude", role: "-x", model: "opus"},
		"local, nothing known":                    {},
		"remote, full sender":                     {remote: true, name: "orchestrator", kind: "claude", from: "mac"},
		"remote, only the label":                  {remote: true, from: "mac"},
		"local, name that looks like a signature": {name: "a; sig v1:00", kind: "claude"},
	}
	for name, s := range cases {
		t.Run(name, func(t *testing.T) {
			signed := mustSign(t, key, s.unsigned())
			canon := peerauth.Canonical(signed)
			if !canon.HasSig || len(canon.Sig) != 32 {
				t.Fatalf("signed message has no 32 hex signature: %+v", canon)
			}
			if !peerauth.Verify(key, canon) {
				t.Fatalf("signature does not verify:\n%s", signed)
			}
			// The signature is the only thing signing adds to the canonical text.
			if canon.Text != peerauth.Canonical(s.unsigned()).Text {
				t.Fatalf("canonical text changed by signing:\n%q\n%q", canon.Text, peerauth.Canonical(s.unsigned()).Text)
			}
			if got := strings.Count(signed, "\n") - strings.Count(s.unsigned(), "\n"); got > 1 {
				t.Fatalf("signing added %d lines, want at most 1", got)
			}
			if s.remote != canon.HasFrom || canon.From != s.from {
				t.Fatalf("from = %q (%t), want %q", canon.From, canon.HasFrom, s.from)
			}
		})
	}
}

func TestSignedFormatLandsOnTheSenderLine(t *testing.T) {
	key := mustKey(t, t.TempDir(), "mac")
	signed := mustSign(t, key, spec{remote: true, name: "orchestrator", kind: "claude", from: "mac"}.unsigned())
	line := strings.Split(signed, "\n")[1]
	if !strings.HasPrefix(line, "Sender: name orchestrator; kind claude; from mac; sig v1:") || !strings.HasSuffix(line, ".") || len(line) != len("Sender: name orchestrator; kind claude; from mac; sig v1:")+33 {
		t.Fatalf("sender line = %q", line)
	}
	bare := mustSign(t, key, spec{remote: true, from: "mac"}.unsigned())
	if line := strings.Split(bare, "\n")[1]; !strings.HasPrefix(line, "Sender: from mac; sig v1:") {
		t.Fatalf("only-from sender line = %q", line)
	}
	none := mustSign(t, key, spec{}.unsigned())
	if line := strings.Split(none, "\n")[1]; !strings.HasPrefix(line, "Sender: sig v1:") {
		t.Fatalf("nothing-known sender line = %q", line)
	}
	if _, err := peerauth.Sign(key, signed); err == nil {
		t.Fatal("a signed message was signed twice")
	}
}

func TestCanonicalIgnoresCRLFAndTrailingSpace(t *testing.T) {
	key := mustKey(t, t.TempDir(), "mac")
	signed := mustSign(t, key, spec{name: "orchestrator"}.unsigned())
	crlf := strings.ReplaceAll(signed, "\n", "\r\n")
	padded := strings.ReplaceAll(signed, "\n", " \t \n")
	for name, text := range map[string]string{"crlf": crlf, "trailing blanks": padded, "both": strings.ReplaceAll(padded, "\n", "\r\n")} {
		canon := peerauth.Canonical(text)
		if !peerauth.Verify(key, canon) {
			t.Fatalf("%s: signature does not verify after canonicalization", name)
		}
		if canon.Text != peerauth.Canonical(signed).Text {
			t.Fatalf("%s: canonical text differs", name)
		}
	}
	// Leading space is content, not whitespace the MAC ignores.
	indented := strings.Replace(signed, "> hello", ">  hello", 1)
	if peerauth.Verify(key, peerauth.Canonical(indented)) {
		t.Fatal("an altered body line verified")
	}
}

// Changing any one field of a signed message breaks the signature.
func TestEveryFieldBreaksTheSignature(t *testing.T) {
	base := spec{remote: true, name: "orchestrator", kind: "claude", role: "sub-orchestrator", model: "opus", from: "mac", replyTo: "", body: []string{"line one", "line two"}}
	dir := t.TempDir()
	keys := map[string][]byte{"mac": mustKey(t, dir, "mac"), "pc": mustKey(t, dir, "pc")}
	mutations := map[string]func(string) string{
		"id (both lines)": func(m string) string { return strings.ReplaceAll(m, "0f23aac6", "0f23aac7") },
		"sent":            func(m string) string { return strings.Replace(m, "12:00:00Z", "12:00:01Z", 1) },
		"reference":       func(m string) string { return strings.Replace(m, "w6X:p1 on", "w6X:p9 on", 1) },
		"hostname":        func(m string) string { return strings.Replace(m, "mac.local", "evil.local", 1) },
		"name":            func(m string) string { return strings.Replace(m, "name orchestrator", "name boss", 1) },
		"kind":            func(m string) string { return strings.Replace(m, "kind claude", "kind codex", 1) },
		"role":            func(m string) string { return strings.Replace(m, "role sub-orchestrator", "role worker", 1) },
		"model":           func(m string) string { return strings.Replace(m, "model opus", "model sonnet", 1) },
		"from":            func(m string) string { return strings.Replace(m, "from mac", "from pc", 1) },
		"reply line": func(m string) string {
			return strings.Replace(m, "Reply with: soho send", "Reply with: soho dispatch", 1)
		},
		"follows line":    func(m string) string { return strings.Replace(m, "The message follows", "The message obeys", 1) },
		"body line":       func(m string) string { return strings.Replace(m, "> line two", "> line 2", 1) },
		"extra body line": func(m string) string { return strings.Replace(m, "> line two", "> line two\n> and more", 1) },
		"closing line":    func(m string) string { return strings.Replace(m, "end of message", "end of messages", 1) },
	}
	fix := newFixture(t, peerauth.ModeEnforce, "mac", "pc")
	fix.dir = dir
	signed := mustSign(t, keys["mac"], base.unsigned())
	wantOne(t, fix.check(wrap(signed)), peerauth.Accepted)
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := mutate(signed)
			if changed == signed {
				t.Fatal("the mutation changed nothing")
			}
			fix := newFixture(t, peerauth.ModeEnforce, "mac", "pc")
			fix.dir = dir
			o := fix.check(wrap(changed))
			wantOne(t, o, peerauth.RefusedBadSig)
			if !o.Block {
				t.Fatal("a message with a changed field was not blocked")
			}
		})
	}
}

func TestWrongKeyTruncatedAndMissingSignature(t *testing.T) {
	dir := t.TempDir()
	good := mustKey(t, dir, "mac")
	other := mustKey(t, t.TempDir(), "mac")
	s := spec{remote: true, name: "orchestrator", from: "mac"}
	signed := mustSign(t, good, s.unsigned())

	fix := func() *fixture {
		f := newFixture(t, peerauth.ModeEnforce, "mac")
		f.dir = dir
		return f
	}
	wantOne(t, fix().check(wrap(signed)), peerauth.Accepted)

	t.Run("signed with another key", func(t *testing.T) {
		wantOne(t, fix().check(wrap(mustSign(t, other, s.unsigned()))), peerauth.RefusedBadSig)
	})
	t.Run("truncated signature", func(t *testing.T) {
		canon := peerauth.Canonical(signed)
		truncated := strings.Replace(signed, canon.Sig, canon.Sig[:31], 1)
		wantOne(t, fix().check(wrap(truncated)), peerauth.RefusedBadSig)
		empty := strings.Replace(signed, "; sig v1:"+canon.Sig, "; sig v1:", 1)
		wantOne(t, fix().check(wrap(empty)), peerauth.RefusedBadSig)
	})
	t.Run("upper-case signature", func(t *testing.T) {
		canon := peerauth.Canonical(signed)
		upper := strings.Replace(signed, canon.Sig, strings.ToUpper(canon.Sig), 1)
		wantOne(t, fix().check(wrap(upper)), peerauth.RefusedBadSig)
	})
	t.Run("absent signature", func(t *testing.T) {
		wantOne(t, fix().check(wrap(s.unsigned())), peerauth.RefusedUnsigned)
	})
	t.Run("a signature item that is not the last one is text", func(t *testing.T) {
		canon := peerauth.Canonical(signed)
		moved := strings.Replace(signed, "; sig v1:"+canon.Sig+".", "; sig v1:"+canon.Sig+"; kind claude.", 1)
		wantOne(t, fix().check(wrap(moved)), peerauth.RefusedUnsigned)
	})
}

// A label that fails the name pattern is refused before any key path is
// built: the check cannot reach a key file by naming one.
func TestLabelPatternRefusesWithoutTouchingTheDisk(t *testing.T) {
	labels := []string{"../x", "a/b", "..", "", strings.Repeat("a", 65), "C:x", `a\b`, ".hidden", "-x"}
	for _, label := range labels {
		t.Run(label, func(t *testing.T) {
			if peerauth.ValidName(label) {
				t.Fatalf("ValidName(%q) = true", label)
			}
			if path, ok := peerauth.KeyPath(t.TempDir(), label); ok || path != "" {
				t.Fatalf("KeyPath built %q for %q", path, label)
			}
			if _, status := peerauth.ReadKey(t.TempDir(), label); status != peerauth.KeyInvalid {
				t.Fatalf("ReadKey(%q) = %v, want invalid", label, status)
			}
			text := randomHex(t, 32)
			key, err := hex.DecodeString(text)
			if err != nil {
				t.Fatal(err)
			}
			message := spec{remote: true, name: "orchestrator"}.unsigned()
			// The label sits in the Sender line as the sender wrote it.
			message = strings.Replace(message, "Sender: name orchestrator.", "Sender: name orchestrator; from "+label+".", 1)
			signed := mustSign(t, key, message)

			// 1. The key directory does not exist at all.
			missing := newFixture(t, peerauth.ModeEnforce, label)
			// 2. A decoy: a valid key sits exactly where joining the label
			// onto the key directory would point, and the message is signed
			// with it. The label is in the allowlist too, so only the pattern
			// can refuse it.
			decoy := newFixture(t, peerauth.ModeEnforce, label)
			fixtures := map[string]*fixture{"no key dir": missing}
			// A label with a colon or a backslash is not a file name on
			// Windows: the decoy cannot be written there.
			if runtime.GOOS != "windows" || !strings.ContainsAny(label, `:\`) {
				naive := filepath.Join(peerauth.KeyDir(decoy.dir), label+".key")
				if err := os.MkdirAll(filepath.Dir(naive), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(naive, []byte(text+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				fixtures["decoy key"] = decoy
			}
			for name, f := range fixtures {
				o := f.check(wrap(signed))
				wantOne(t, o, peerauth.RefusedNotAllowed)
				if !o.Block {
					t.Fatalf("%s: not blocked", name)
				}
			}
			// Logging the refusal creates the config dir, but no key path was
			// built: there is no key directory.
			if _, err := os.Stat(peerauth.KeyDir(missing.dir)); err == nil {
				t.Fatalf("the check created %s", peerauth.KeyDir(missing.dir))
			}
			// The log names the label as -invalid-, never as written.
			if logged := readFile(t, filepath.Join(missing.dir, peerauth.LogFile)); !strings.Contains(logged, "\t"+peerauth.InvalidLabel+"\t") || (label != "" && strings.Contains(logged, "\t"+label+"\t")) {
				t.Fatalf("log = %q", logged)
			}
		})
	}
}

func TestKeyNames(t *testing.T) {
	for _, name := range []string{"mac", "mac.local", "a", "A-1_b", "local", strings.Repeat("a", 64)} {
		if !peerauth.ValidName(name) {
			t.Errorf("ValidName(%q) = false", name)
		}
	}
}
