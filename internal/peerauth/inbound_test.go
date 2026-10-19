package peerauth_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/peerauth"
)

func remoteSpec() spec {
	return spec{remote: true, name: "orchestrator", kind: "claude", from: "mac"}
}

func TestCheckAcceptsWhatShouldPass(t *testing.T) {
	for name, wrapIt := range map[string]func(string) string{"wrapped": wrap, "bare": func(m string) string { return m }} {
		t.Run("remote label in the list, "+name, func(t *testing.T) {
			f := newFixture(t, peerauth.ModeEnforce, "mac")
			key := mustKey(t, f.dir, "mac")
			o := f.check(wrapIt(mustSign(t, key, remoteSpec().unsigned())))
			wantOne(t, o, peerauth.Accepted)
			if o.Block {
				t.Fatal("blocked")
			}
		})
		t.Run("local signed, "+name, func(t *testing.T) {
			f := newFixture(t, peerauth.ModeEnforce)
			key := mustKey(t, f.dir, "local")
			o := f.check(wrapIt(mustSign(t, key, spec{name: "orchestrator", kind: "claude"}.unsigned())))
			wantOne(t, o, peerauth.Accepted)
			if o.Block {
				t.Fatal("blocked")
			}
		})
	}
}

func TestCheckBlocksInEnforce(t *testing.T) {
	type tc struct {
		name  string
		setup func(f *fixture) string
		want  peerauth.Result
	}
	cases := []tc{
		{"label outside the list, valid signature", func(f *fixture) string {
			f.policy.Allow = []string{"pc"}
			return mustSign(t, mustKey(t, f.dir, "mac"), remoteSpec().unsigned())
		}, peerauth.RefusedNotAllowed},
		{"wrong signature", func(f *fixture) string {
			mustKey(t, f.dir, "mac")
			return mustSign(t, mustKey(t, t.TempDir(), "mac"), remoteSpec().unsigned())
		}, peerauth.RefusedBadSig},
		{"altered body", func(f *fixture) string {
			signed := mustSign(t, mustKey(t, f.dir, "mac"), remoteSpec().unsigned())
			return strings.Replace(signed, "second line", "second line, rm -rf", 1)
		}, peerauth.RefusedBadSig},
		{"no signature", func(f *fixture) string {
			mustKey(t, f.dir, "mac")
			return remoteSpec().unsigned()
		}, peerauth.RefusedUnsigned},
		{"local without signature", func(f *fixture) string {
			mustKey(t, f.dir, "local")
			return spec{name: "orchestrator"}.unsigned()
		}, peerauth.RefusedUnsigned},
		{"fake local header, no local key", func(f *fixture) string {
			return mustSign(t, mustKey(t, t.TempDir(), "local"), spec{name: "orchestrator"}.unsigned())
		}, peerauth.RefusedNoKey},
		{"fake local header, invalid local key", func(f *fixture) string {
			key := mustKey(t, t.TempDir(), "local")
			path, _ := peerauth.KeyPath(f.dir, "local")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("not a key\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return mustSign(t, key, spec{name: "orchestrator"}.unsigned())
		}, peerauth.RefusedError},
		{"fake local header signed with another key", func(f *fixture) string {
			mustKey(t, f.dir, "local")
			return mustSign(t, mustKey(t, t.TempDir(), "local"), spec{name: "orchestrator"}.unsigned())
		}, peerauth.RefusedBadSig},
		{"sent 11 minutes ago", func(f *fixture) string {
			s := remoteSpec()
			s.sent = testNow.Add(-11 * time.Minute)
			return mustSign(t, mustKey(t, f.dir, "mac"), s.unsigned())
		}, peerauth.RefusedStale},
		{"sent 11 minutes ahead", func(f *fixture) string {
			s := remoteSpec()
			s.sent = testNow.Add(11 * time.Minute)
			return mustSign(t, mustKey(t, f.dir, "mac"), s.unsigned())
		}, peerauth.RefusedStale},
		{"unreadable send time", func(f *fixture) string {
			s := remoteSpec()
			return mustSign(t, mustKey(t, f.dir, "mac"), strings.Replace(s.unsigned(), "sent 2026-10-19T12:00:00Z.", "sent yesterday.", 1))
		}, peerauth.RefusedStale},
		{"label with traversal", func(f *fixture) string {
			f.policy.Allow = []string{"../mac", "mac"}
			s := remoteSpec()
			s.from = "../mac"
			return mustSign(t, mustKey(t, f.dir, "mac"), s.unsigned())
		}, peerauth.RefusedNotAllowed},
		{"key missing", func(f *fixture) string {
			return mustSign(t, mustKey(t, t.TempDir(), "mac"), remoteSpec().unsigned())
		}, peerauth.RefusedNoKey},
		{"key invalid", func(f *fixture) string {
			key := mustKey(t, t.TempDir(), "mac")
			path, _ := peerauth.KeyPath(f.dir, "mac")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(strings.Repeat("ab", 31)+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return mustSign(t, key, remoteSpec().unsigned())
		}, peerauth.RefusedError},
		{"remote header without from", func(f *fixture) string {
			mustKey(t, f.dir, "mac")
			s := remoteSpec()
			s.from = ""
			return mustSign(t, mustKey(t, f.dir, "local"), s.unsigned())
		}, peerauth.RefusedUnsigned},
		{"local header with from", func(f *fixture) string {
			s := spec{name: "orchestrator", from: "mac"}
			return mustSign(t, mustKey(t, f.dir, "mac"), s.unsigned())
		}, peerauth.RefusedBadSig},
		{"remote header claiming to be local", func(f *fixture) string {
			f.policy.Allow = []string{"mac", "local"}
			s := remoteSpec()
			s.from = "local"
			return mustSign(t, mustKey(t, f.dir, "local"), s.unsigned())
		}, peerauth.RefusedNotAllowed},
		{"no closing line", func(f *fixture) string {
			signed := mustSign(t, mustKey(t, f.dir, "mac"), remoteSpec().unsigned())
			return signed[:strings.LastIndex(signed, "\n[/soho:peer]")]
		}, peerauth.RefusedBadSig},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, peerauth.ModeEnforce, "mac")
			message := c.setup(f)
			for _, in := range []string{wrap(message), message} {
				o := f.check(in)
				wantOne(t, o, c.want)
				if !o.Block {
					t.Fatalf("not blocked: %+v", o)
				}
				if o.Mode != peerauth.ModeEnforce {
					t.Fatalf("mode = %s", o.Mode)
				}
			}
			// A refused message never enters the replay cache.
			if seen := readFile(t, filepath.Join(f.dir, peerauth.SeenFile)); seen != "" {
				t.Fatalf("refused message entered the replay cache: %q", seen)
			}
		})
	}
}

func TestCheckReplay(t *testing.T) {
	f := newFixture(t, peerauth.ModeEnforce, "mac")
	key := mustKey(t, f.dir, "mac")
	signed := mustSign(t, key, remoteSpec().unsigned())
	wantOne(t, f.check(wrap(signed)), peerauth.Accepted)
	for i := 0; i < 2; i++ {
		o := f.check(wrap(signed))
		wantOne(t, o, peerauth.RefusedReplay)
		if !o.Block {
			t.Fatal("a replayed message was not blocked")
		}
	}
	// Same id and label, another send time: another message.
	later := remoteSpec()
	later.sent = testNow.Add(time.Minute)
	wantOne(t, f.check(wrap(mustSign(t, key, later.unsigned()))), peerauth.Accepted)
	// The send time keeps a 21-minute-old message out by itself, and the next
	// write prunes the entries older than 20 minutes.
	f.now = testNow.Add(21 * time.Minute)
	wantOne(t, f.check(wrap(signed)), peerauth.RefusedStale)
	fresh := remoteSpec()
	fresh.id = "55667788"
	fresh.sent = f.now
	wantOne(t, f.check(wrap(mustSign(t, key, fresh.unsigned()))), peerauth.Accepted)
	seen := readFile(t, filepath.Join(f.dir, peerauth.SeenFile))
	if strings.Contains(seen, "0f23aac6") || !strings.Contains(seen, "55667788") {
		t.Fatalf("the 21-minute-old entries were not pruned by the write: %q", seen)
	}
	// A local message is its own label.
	g := newFixture(t, peerauth.ModeEnforce)
	local := mustSign(t, mustKey(t, g.dir, "local"), spec{name: "orchestrator"}.unsigned())
	wantOne(t, g.check(wrap(local)), peerauth.Accepted)
	wantOne(t, g.check(wrap(local)), peerauth.RefusedReplay)
	if got := strings.Split(strings.TrimSpace(readFile(t, filepath.Join(g.dir, peerauth.SeenFile))), "\t"); got[0] != "local" || got[1] != "0f23aac6" {
		t.Fatalf("seen entry = %v", got)
	}
}

func TestCheckTwoMessagesInOnePrompt(t *testing.T) {
	f := newFixture(t, peerauth.ModeEnforce, "mac")
	key := mustKey(t, f.dir, "mac")
	good := mustSign(t, key, remoteSpec().unsigned())
	badSpec := remoteSpec()
	badSpec.id = "aabbccdd"
	bad := strings.Replace(mustSign(t, key, badSpec.unsigned()), "second line", "tampered", 1)
	prompt := wrap(good + "\n" + bad)
	o := f.check(prompt)
	if got := results(o); len(got) != 2 || got[0] != peerauth.RefusedWithAnother || got[1] != peerauth.RefusedBadSig {
		t.Fatalf("results = %v", got)
	}
	if !o.Block {
		t.Fatal("not blocked")
	}
	if seen := readFile(t, filepath.Join(f.dir, peerauth.SeenFile)); seen != "" {
		t.Fatalf("the valid message entered the replay cache although the prompt was blocked: %q", seen)
	}
	// Order is the prompt's.
	o = f.check(wrap(bad + "\n" + good))
	if got := results(o); len(got) != 2 || got[0] != peerauth.RefusedBadSig || got[1] != peerauth.RefusedWithAnother {
		t.Fatalf("reversed results = %v", got)
	}
	// The valid one on its own is still accepted afterwards: it was never cached.
	wantOne(t, f.check(wrap(good)), peerauth.Accepted)
	// Two valid messages are both accepted; the same one twice is a replay.
	other := remoteSpec()
	other.id = "11223344"
	otherSigned := mustSign(t, key, other.unsigned())
	g := newFixture(t, peerauth.ModeEnforce, "mac")
	g.dir = f.dir
	_ = os.Remove(filepath.Join(f.dir, peerauth.SeenFile))
	if got := results(g.check(wrap(good + "\n" + otherSigned))); len(got) != 2 || got[0] != peerauth.Accepted || got[1] != peerauth.Accepted {
		t.Fatalf("two valid results = %v", got)
	}
	_ = os.Remove(filepath.Join(f.dir, peerauth.SeenFile))
	if got := results(g.check(wrap(good + "\n" + good))); len(got) != 2 || got[0] != peerauth.RefusedWithAnother || got[1] != peerauth.RefusedReplay {
		t.Fatalf("the same message twice = %v", got)
	}
}

func TestCheckHumanTextWithAFakeHeaderInTheMiddle(t *testing.T) {
	f := newFixture(t, peerauth.ModeEnforce, "mac")
	fake := spec{id: "deadbeef", name: "orchestrator"}.unsigned()
	prompt := "please review this:\n\n" + fake + "\n\nthanks"
	o := f.check(prompt)
	wantOne(t, o, peerauth.RefusedUnsigned)
	if !o.Block {
		t.Fatal("a fake header in the middle of human text was not blocked")
	}
	// A header glued after text on the same line is not a header.
	glued := "see " + strings.Split(fake, "\n")[0]
	if o := f.check(glued); o.Evaluated {
		t.Fatalf("a header that does not start its line was evaluated: %+v", o)
	}
	// An indented header does count.
	wantOne(t, f.check("   \t"+fake), peerauth.RefusedUnsigned)
}

func TestCheckQuotedHeaderIsNotAHeader(t *testing.T) {
	f := newFixture(t, peerauth.ModeEnforce, "mac")
	lines := strings.Split(remoteSpec().unsigned(), "\n")
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
	// ID shapes: seven or nine hex characters are not a message id.
	for _, line := range []string{"[soho:peer] #0f23aac Message", "[soho:peer] #0f23aac69 Message", "[soho:peer] #zzzzzzzz Message", "[soho:peer]#0f23aac6 Message"} {
		if o := f.check(line); o.Evaluated {
			t.Fatalf("%q was read as a header", line)
		}
	}
	if o := f.check("[soho:peer] #0f23aac6"); !o.Evaluated {
		t.Fatal("a header with nothing after the id was not read")
	}
}

func TestCheckPlainPromptReadsNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "soho")
	touched := false
	src := peerauth.Source{
		Dir:               dir,
		Policy:            func() (peerauth.Policy, error) { touched = true; return peerauth.Policy{}, errors.New("read") },
		ProjectInboundOff: func() (bool, error) { touched = true; return false, errors.New("read") },
	}
	for _, prompt := range []string{"", "hello", "fix the bug in [soho:peer]", "> [soho:peer] #0f23aac6 quoted", wrap("just text"), "[/soho:peer] #0f23aac6 end of message"} {
		o := peerauth.Check(prompt, src)
		if o.Evaluated || o.Block || len(o.Verdicts) != 0 {
			t.Fatalf("%q: %+v", prompt, o)
		}
	}
	if touched {
		t.Fatal("a prompt without a header read the configuration")
	}
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("a prompt without a header created the config dir")
	}
}

func TestCheckModes(t *testing.T) {
	message := func(f *fixture) string {
		return mustSign(t, mustKey(t, t.TempDir(), "mac"), remoteSpec().unsigned()) // never verifies here
	}
	t.Run("off accepts without a log or a cache", func(t *testing.T) {
		f := newFixture(t, peerauth.ModeOff)
		o := f.check(wrap(message(f)))
		wantOne(t, o, peerauth.Accepted)
		if o.Block {
			t.Fatal("blocked")
		}
		for _, name := range []string{peerauth.LogFile, peerauth.SeenFile} {
			if _, err := os.Stat(filepath.Join(f.dir, name)); err == nil {
				t.Fatalf("off wrote %s", name)
			}
		}
		if f.reads["project"] != 1 {
			t.Fatalf("off must still read the project's inbound: %v", f.reads)
		}
	})
	t.Run("warn passes and logs", func(t *testing.T) {
		f := newFixture(t, peerauth.ModeWarn, "mac")
		o := f.check(wrap(message(f)))
		wantOne(t, o, peerauth.RefusedNoKey)
		if o.Block {
			t.Fatal("warn blocked")
		}
		logged := readFile(t, filepath.Join(f.dir, peerauth.LogFile))
		fields := strings.Split(strings.TrimSpace(logged), "\t")
		if len(fields) != 8 || fields[1] != "warn" || fields[2] != "remote" || fields[3] != "mac" || fields[4] != "0f23aac6" || fields[5] != "w6X:p2" || fields[6] != "refused-no-key" || fields[7] == "" {
			t.Fatalf("log fields = %q", fields)
		}
		if _, err := time.Parse(time.RFC3339, fields[0]); err != nil || !strings.HasSuffix(fields[0], "Z") {
			t.Fatalf("log time %q", fields[0])
		}
	})
	t.Run("warn caches what it accepts", func(t *testing.T) {
		f := newFixture(t, peerauth.ModeWarn, "mac")
		signed := mustSign(t, mustKey(t, f.dir, "mac"), remoteSpec().unsigned())
		wantOne(t, f.check(wrap(signed)), peerauth.Accepted)
		o := f.check(wrap(signed))
		wantOne(t, o, peerauth.RefusedReplay)
		if o.Block {
			t.Fatal("warn blocked a replay")
		}
	})
	t.Run("warn keeps an accepted message of a prompt with a refused one", func(t *testing.T) {
		f := newFixture(t, peerauth.ModeWarn, "mac")
		key := mustKey(t, f.dir, "mac")
		bad := remoteSpec()
		bad.id = "aabbccdd"
		prompt := wrap(mustSign(t, key, remoteSpec().unsigned()) + "\n" + bad.unsigned())
		o := f.check(prompt)
		if got := results(o); got[0] != peerauth.Accepted || got[1] != peerauth.RefusedUnsigned || o.Block {
			t.Fatalf("results = %v block=%t", got, o.Block)
		}
	})
	t.Run("enforce logs the mode and an empty pane as -", func(t *testing.T) {
		f := newFixture(t, peerauth.ModeEnforce, "mac")
		f.pane = ""
		f.check(wrap(spec{}.unsigned()))
		fields := strings.Split(strings.TrimSpace(readFile(t, filepath.Join(f.dir, peerauth.LogFile))), "\t")
		if fields[1] != "enforce" || fields[2] != "local" || fields[3] != "local" || fields[5] != "-" || fields[6] != "refused-unsigned" {
			t.Fatalf("log fields = %q", fields)
		}
	})
}

func TestCheckProjectInboundOff(t *testing.T) {
	// The project's inbound=off is its policy, apart from authentication: it
	// blocks in every mode, warn included.
	for _, mode := range []peerauth.Mode{peerauth.ModeOff, peerauth.ModeWarn, peerauth.ModeEnforce} {
		t.Run(string(mode), func(t *testing.T) {
			f := newFixture(t, mode, "mac")
			f.projectOff = true
			signed := mustSign(t, mustKey(t, f.dir, "mac"), remoteSpec().unsigned())
			o := f.check(wrap(signed))
			wantOne(t, o, peerauth.RefusedInboundOff)
			if !o.Block {
				t.Fatalf("an inbound=off project accepted a peer message in %s", mode)
			}
		})
	}
	t.Run("warn blocks an unsigned message the same way", func(t *testing.T) {
		f := newFixture(t, peerauth.ModeWarn, "mac")
		f.projectOff = true
		o := f.check(wrap(spec{}.unsigned()))
		wantOne(t, o, peerauth.RefusedInboundOff)
		if !o.Block {
			t.Fatal("warn passed an inbound=off project")
		}
	})
	t.Run("a project that is not off still passes in warn", func(t *testing.T) {
		f := newFixture(t, peerauth.ModeWarn, "mac")
		o := f.check(wrap(spec{}.unsigned()))
		wantOne(t, o, peerauth.RefusedUnsigned)
		if o.Block {
			t.Fatal("warn blocked an authentication failure")
		}
	})
}

func TestCheckInternalErrors(t *testing.T) {
	unreadable := errors.New("unreadable")
	t.Run("policy unreadable blocks as enforce", func(t *testing.T) {
		f := newFixture(t, peerauth.ModeOff)
		src := f.source()
		src.Policy = func() (peerauth.Policy, error) { return peerauth.Policy{}, unreadable }
		o := peerauth.Check(wrap(spec{}.unsigned()), src)
		wantOne(t, o, peerauth.RefusedError)
		if !o.Block || o.Mode != peerauth.ModeEnforce {
			t.Fatalf("%+v", o)
		}
	})
	t.Run("project unreadable", func(t *testing.T) {
		f := newFixture(t, peerauth.ModeEnforce)
		src := f.source()
		src.ProjectInboundOff = func() (bool, error) { return false, unreadable }
		o := peerauth.Check(wrap(spec{}.unsigned()), src)
		wantOne(t, o, peerauth.RefusedError)
		if !o.Block {
			t.Fatal("not blocked")
		}
		f.policy.Mode = peerauth.ModeWarn
		src = f.source()
		src.ProjectInboundOff = func() (bool, error) { return false, unreadable }
		o = peerauth.Check(wrap(spec{}.unsigned()), src)
		wantOne(t, o, peerauth.RefusedError)
		if o.Block {
			t.Fatal("warn blocked")
		}
	})
	t.Run("replay cache unreadable", func(t *testing.T) {
		f := newFixture(t, peerauth.ModeEnforce, "mac")
		signed := mustSign(t, mustKey(t, f.dir, "mac"), remoteSpec().unsigned())
		// A directory where the cache file belongs cannot be read as one.
		if err := os.MkdirAll(filepath.Join(f.dir, peerauth.SeenFile), 0o700); err != nil {
			t.Fatal(err)
		}
		o := f.check(wrap(signed))
		wantOne(t, o, peerauth.RefusedError)
		if !o.Block {
			t.Fatal("not blocked")
		}
	})
}

// Nothing of the body, the signature, a key or a label that failed the
// pattern reaches the reason, the log or the cache.
func TestCheckLeaksNothing(t *testing.T) {
	f := newFixture(t, peerauth.ModeEnforce, "mac", "../evil")
	key := mustKey(t, f.dir, "mac")
	evil := remoteSpec()
	evil.from = "../evil"
	good := mustSign(t, key, remoteSpec().unsigned())
	tampered := strings.Replace(mustSign(t, key, remoteSpec().unsigned()), "second line", "second line "+secretMarker, 1)
	prompts := []string{wrap(tampered), wrap(mustSign(t, key, evil.unsigned())), wrap(remoteSpec().unsigned()), wrap(good + "\n" + tampered)}
	keyText := strings.TrimSpace(readFile(t, filepath.Join(f.dir, peerauth.KeyDirName, "mac.key")))
	var reasons []string
	for i, prompt := range prompts {
		o := f.check(prompt)
		if !o.Block {
			t.Fatalf("prompt %d not blocked", i)
		}
		reasons = append(reasons, o.Reason())
	}
	// Add the cache of an accepted message.
	other := remoteSpec()
	other.id = "99999999"
	wantOne(t, f.check(wrap(mustSign(t, key, other.unsigned()))), peerauth.Accepted)
	sig := peerauth.Canonical(good).Sig
	artifacts := append(reasons, readFile(t, filepath.Join(f.dir, peerauth.LogFile)), readFile(t, filepath.Join(f.dir, peerauth.SeenFile)))
	for _, text := range artifacts {
		for _, secret := range []string{secretMarker, sig, keyText, "../evil", "rm -rf", "hello"} {
			if strings.Contains(text, secret) {
				t.Fatalf("%q leaked into %q", secret, text)
			}
		}
	}
	for _, reason := range reasons {
		lines := strings.Split(reason, "\n")
		if lines[len(lines)-1] != peerauth.ReasonTail {
			t.Fatalf("reason does not end with the tail: %q", reason)
		}
		for _, line := range lines[:len(lines)-1] {
			if !strings.HasPrefix(line, "[soho:peer-refused] #") || len(strings.Fields(line)) != 3 {
				t.Fatalf("reason line %q", line)
			}
		}
	}
	// Chars is the message length, not its content.
	if !strings.Contains(readFile(t, filepath.Join(f.dir, peerauth.LogFile)), "\t"+peerauth.InvalidLabel+"\t") {
		t.Fatal("the invalid label is not logged as -invalid-")
	}
}

// Two hooks with the same message: exactly one accepts.
func TestCheckConcurrentSameMessageAcceptsOnce(t *testing.T) {
	f := newFixture(t, peerauth.ModeEnforce, "mac")
	signed := mustSign(t, mustKey(t, f.dir, "mac"), remoteSpec().unsigned())
	const hooks = 8
	var wg sync.WaitGroup
	got := make([]peerauth.Result, hooks)
	start := make(chan struct{})
	for i := 0; i < hooks; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			src := peerauth.Source{
				Dir:               f.dir,
				Policy:            func() (peerauth.Policy, error) { return f.policy, nil },
				ProjectInboundOff: func() (bool, error) { return false, nil },
				Now:               func() time.Time { return testNow },
			}
			<-start
			o := peerauth.Check(wrap(signed), src)
			got[i] = o.Verdicts[0].Result
		}()
	}
	close(start)
	wg.Wait()
	accepted, replays := 0, 0
	for _, r := range got {
		switch r {
		case peerauth.Accepted:
			accepted++
		case peerauth.RefusedReplay:
			replays++
		default:
			t.Fatalf("results = %v", got)
		}
	}
	if accepted != 1 || replays != hooks-1 {
		t.Fatalf("accepted=%d replays=%d, want 1 and %d: %v", accepted, replays, hooks-1, got)
	}
	if lines := strings.Count(readFile(t, filepath.Join(f.dir, peerauth.SeenFile)), "\n"); lines != 1 {
		t.Fatalf("the cache holds %d entries, want 1", lines)
	}
}

func TestPolicyParse(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) string {
		path := filepath.Join(dir, "config")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	cases := []struct {
		body    string
		mode    peerauth.Mode
		invalid bool
		allow   []string
	}{
		{"", peerauth.ModeOff, false, nil},
		{"inbound_auth=off\n", peerauth.ModeOff, false, nil},
		{"inbound_auth=warn\ninbound_allow=mac|pc\n", peerauth.ModeWarn, false, []string{"mac", "pc"}},
		{"inbound_auth = enforce\ninbound_allow = mac | pc |\n", peerauth.ModeEnforce, false, []string{"mac", "pc"}},
		{"inbound_auth=ENFORCE\n", peerauth.ModeEnforce, true, nil},
		{"inbound_auth=on\n", peerauth.ModeEnforce, true, nil},
		{"inbound-auth=warn\n", peerauth.ModeWarn, false, nil},
		{"inbound_allow=\n", peerauth.ModeOff, false, nil},
	}
	for _, c := range cases {
		policy, err := peerauth.LoadPolicy(write(c.body))
		if err != nil {
			t.Fatal(err)
		}
		if policy.Mode != c.mode || policy.ModeInvalid != c.invalid || strings.Join(policy.Allow, "|") != strings.Join(c.allow, "|") {
			t.Errorf("%q: %+v", c.body, policy)
		}
	}
	if policy, err := peerauth.LoadPolicy(filepath.Join(dir, "absent")); err != nil || policy.Mode != peerauth.ModeOff {
		t.Fatalf("absent file: %+v %v", policy, err)
	}
	if _, err := peerauth.LoadPolicy(dir); err == nil {
		t.Fatal("a directory read as a config file")
	}
	// The keys are the same ones config accepts.
	for _, key := range []string{"inbound_auth", "inbound_allow"} {
		if !core.ConfigKeyOk(key) {
			t.Errorf("config does not know %s", key)
		}
	}
	for _, key := range core.ConfigScalarKeys {
		if key == "inbound_auth" || key == "inbound_allow" {
			t.Errorf("%s joined ConfigScalarKeys, which the frozen oracle pins", key)
		}
	}
}

func TestNewKey(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "soho")
	path, err := peerauth.NewKey(dir, "mac")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "peer-keys", "mac.key"); path != want || !filepath.IsAbs(path) {
		t.Fatalf("path = %q, want %q", path, want)
	}
	raw := readFile(t, path)
	if len(raw) != 65 || raw[64] != '\n' || strings.ToLower(raw) != raw {
		t.Fatalf("key file = %d bytes, %q", len(raw), raw[len(raw)-1:])
	}
	if _, err := peerauth.NewKey(dir, "mac"); !errors.Is(err, peerauth.ErrKeyExists) {
		t.Fatalf("second NewKey = %v", err)
	}
	if again := readFile(t, path); again != raw {
		t.Fatal("an existing key was overwritten")
	}
	if _, err := peerauth.NewKey(dir, "../x"); err == nil {
		t.Fatal("NewKey accepted a traversal name")
	}
	other, err := peerauth.NewKey(dir, "pc")
	if err != nil || readFile(t, other) == raw {
		t.Fatalf("two keys are equal: %v", err)
	}
}

func TestReadKeyRejects(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		path := filepath.Join(peerauth.KeyDir(dir), name+".key")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	good := randomHex(t, 32)
	write("ok", good+"\n")
	write("padded", "  "+good+" \r\n\n")
	write("short", good[:62]+"\n")
	write("long", good+"00\n")
	write("nothex", strings.Repeat("zz", 32)+"\n")
	write("empty", "")
	write("huge", strings.Repeat("a", 5000))
	if err := os.MkdirAll(filepath.Join(peerauth.KeyDir(dir), "dir.key"), 0o700); err != nil {
		t.Fatal(err)
	}
	want := map[string]peerauth.KeyStatus{"ok": peerauth.KeyOK, "padded": peerauth.KeyOK, "short": peerauth.KeyInvalid, "long": peerauth.KeyInvalid, "nothex": peerauth.KeyInvalid, "empty": peerauth.KeyInvalid, "huge": peerauth.KeyInvalid, "dir": peerauth.KeyInvalid, "absent": peerauth.KeyMissing}
	for name, status := range want {
		if _, got := peerauth.ReadKey(dir, name); got != status {
			t.Errorf("ReadKey(%s) = %v, want %v", name, got, status)
		}
	}
}
