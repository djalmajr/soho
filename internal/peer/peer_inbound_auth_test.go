package peer_test

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/peer"
	"github.com/djalmajr/soho/internal/peerauth"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// Signed peer messages (DJA-196): the send signs with the key of the target's
// machine, and reads the receiver's refusal off its screen.

const signID = "01020304"

func signedFixtureID(t *testing.T) {
	t.Helper()
	old := rand.Reader
	rand.Reader = bytes.NewReader([]byte{1, 2, 3, 4})
	t.Cleanup(func() { rand.Reader = old })
}

// userConfigDir is the fixture's user config dir (XDG_CONFIG_HOME/soho).
func userConfigDir(f *fixture) string { return filepath.Join(f.env["XDG_CONFIG_HOME"], "soho") }

// confDirOf is the user config dir a fixture rooted at dir gets.
func confDirOf(dir string) string { return filepath.Join(dir, "config", "soho") }

func writeUserConfig(t *testing.T, confDir, body string) {
	t.Helper()
	dir := confDir
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// newPeerKey writes a key file for name without crypto/rand, which the tests
// seed with the message id.
func newPeerKey(t *testing.T, confDir, name string) []byte {
	t.Helper()
	sum := sha256.Sum256([]byte("peer-key-test-" + name))
	path, ok := peerauth.KeyPath(confDir, name)
	if !ok {
		t.Fatalf("invalid key name %q", name)
	}
	mustWrite(t, path, hex.EncodeToString(sum[:])+"\n")
	key, status := peerauth.ReadKey(confDir, name)
	if status != peerauth.KeyOK {
		t.Fatalf("key %s status %v", name, status)
	}
	return key
}

// sentPrompt is the text of the `agent prompt` call the fake herdr recorded.
func sentPrompt(t *testing.T, f *fixture) (string, bool) {
	t.Helper()
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(filepath.Dir(f.bin), "herdr.json"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, call := range calls {
		for i, a := range call.Argv {
			if a == "prompt" && i+2 < len(call.Argv) && (i == 1 || i == 3) {
				return call.Argv[i+2], true
			}
		}
	}
	return "", false
}

func localUnsigned() string {
	return peer.PeerHeader("local/-", "-", "-", "-", signID) + "\n\n> hello\n" + peer.PeerEndLine(signID)
}

func remoteUnsigned(from string) string {
	sender := peer.PeerSender{Name: "-", Kind: "-", Role: "-", From: from}
	return peer.PeerHeaderRemoteFor(sender, "-", "Run2Biz.local", signID) + "\n\n> hello\n" + peer.PeerEndLine(signID)
}

// remoteSendRules is the remote claude short-turn run that ends in `sent`.
func remoteSendRules(prompt string) []fakecli.Rule {
	preScreen := "❯ \n"
	postScreen := peer.PeerEndLine(signID) + "\n✻ Baked for 4s · done 2:55 PM\n❯ \n"
	m := []string{"--machine", "windows", "agent"}
	at := func(more ...string) []string { return append(append([]string{}, m...), more...) }
	return []fakecli.Rule{
		{Argv: at("get", "w0test:p0a"), Call: 1, Stdout: agentJSONKind("claude", "idle", "1")},
		{Argv: at("read", "w0test:p0a", "--source", "visible"), Call: 1, Stdout: preScreen},
		{Argv: at("get", "w0test:p0a"), Call: 2, Stdout: agentJSONKind("claude", "idle", "1")},
		{Argv: at("get", "w0test:p0a"), Call: 3, Stdout: agentJSONKind("claude", "idle", "1")},
		{Argv: at("prompt", "w0test:p0a", prompt, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000")},
		{Argv: at("get", "w0test:p0a"), Call: 4, Stdout: agentJSONKind("claude", "done", "2")},
		{Argv: at("read", "w0test:p0a", "--source", "visible"), Call: 2, Stdout: postScreen},
		{Argv: at("read", "w0test:p0a", "--source", "recent-unwrapped"), ArgvPrefix: true, Stdout: postScreen},
		{Argv: at("read", "w0test:p0a", "--source", "visible"), Stdout: postScreen},
		{Argv: at("get", "w0test:p0a"), Stdout: agentJSONKind("claude", "done", "2")},
	}
}

func localSendRules(prompt string) []fakecli.Rule {
	return successfulSendRules(prompt, fakecli.Rule{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 1, Stdout: agentJSON("idle", "1")})
}

func TestSendSignsLocalMessageWithTheLocalKey(t *testing.T) {
	signedFixtureID(t)
	dir := t.TempDir()
	key := newPeerKey(t, confDirOf(dir), "local")
	want, err := peerauth.Sign(key, localUnsigned())
	if err != nil {
		t.Fatal(err)
	}
	f := newFixtureAt(t, dir, localSendRules(want))
	code, out, stderr := f.run([]string{"send", "w0test:p0a", "hello"})
	if code != 0 || out != "sent to local/w0test:p0a\n" || stderr != "" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	got, ok := sentPrompt(t, f)
	if !ok || got != want {
		t.Fatalf("prompt = %q\nwant     %q", got, want)
	}
	canon := peerauth.Canonical(got)
	if !canon.HasSig || canon.HasFrom || !peerauth.Verify(key, canon) {
		t.Fatalf("local signature: %+v", canon)
	}
	if line := strings.Split(got, "\n")[1]; !strings.HasPrefix(line, "Sender: sig v1:") || strings.Contains(got, "; from ") {
		t.Fatalf("sender line = %q", line)
	}
	if n := strings.Count(got, "\n") + 1; n != strings.Count(localUnsigned(), "\n")+2 {
		t.Fatalf("the signed message has %d lines", n)
	}
}

func TestSendLocalWithoutAKeyKeepsTodaysBytes(t *testing.T) {
	signedFixtureID(t)
	dir := t.TempDir()
	// A key for another machine does not sign a local message.
	newPeerKey(t, confDirOf(dir), "mac")
	f := newFixtureAt(t, dir, localSendRules(localUnsigned()))
	code, out, stderr := f.run([]string{"send", "w0test:p0a", "hello"})
	if code != 0 || out != "sent to local/w0test:p0a\n" || stderr != "" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	if got, _ := sentPrompt(t, f); got != localUnsigned() || strings.Contains(got, "sig v1") {
		t.Fatalf("prompt = %q", got)
	}
}

func TestSendSignsRemoteMessageWithFromAndTheMachineKey(t *testing.T) {
	signedFixtureID(t)
	setSenderHostname(t, "Run2Biz.local")
	dir := t.TempDir()
	key := newPeerKey(t, confDirOf(dir), "windows")
	newPeerKey(t, confDirOf(dir), "local")
	writeUserConfig(t, confDirOf(dir), "machine_label=mac\n")
	want, err := peerauth.Sign(key, remoteUnsigned("mac"))
	if err != nil {
		t.Fatal(err)
	}
	f := newFixtureAt(t, dir, remoteSendRules(want))
	f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
	code, out, stderr := f.run([]string{"send", remotePane, "hello"})
	if code != 0 || out != "sent to windows/w0test:p0a\n" || stderr != "" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	got, _ := sentPrompt(t, f)
	if got != want {
		t.Fatalf("prompt = %q\nwant     %q", got, want)
	}
	canon := peerauth.Canonical(got)
	if !canon.HasSig || !canon.HasFrom || canon.From != "mac" || !peerauth.Verify(key, canon) {
		t.Fatalf("remote signature: %+v", canon)
	}
	if line := strings.Split(got, "\n")[1]; !strings.HasPrefix(line, "Sender: from mac; sig v1:") {
		t.Fatalf("sender line = %q", line)
	}
}

func TestSendRemoteWithoutALabelOrAKeyGoesOutUnsigned(t *testing.T) {
	for _, tc := range []struct {
		name, config string
		key          bool
		note         string
	}{
		{"key, empty label", "job_orgs=x\n", true, "send: not signed: machine_label is empty or invalid\n"},
		{"key, label outside the pattern", "machine_label=a/b\n", true, "send: not signed: machine_label is empty or invalid\n"},
		{"key, label from the project only", "", true, "send: not signed: machine_label is empty or invalid\n"},
		{"label, no key", "machine_label=mac\n", false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			signedFixtureID(t)
			setSenderHostname(t, "Run2Biz.local")
			dir := t.TempDir()
			if tc.key {
				newPeerKey(t, confDirOf(dir), "windows")
			}
			writeUserConfig(t, confDirOf(dir), tc.config)
			f := newFixtureAt(t, dir, remoteSendRules(remoteUnsigned("")))
			f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
			// The label belongs to the user file only: not to the environment.
			f.env["SOHO_MACHINE_LABEL"] = "mac"
			code, out, stderr := f.run([]string{"send", remotePane, "hello"})
			if code != 0 || out != "sent to windows/w0test:p0a\n" || stderr != tc.note {
				t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
			}
			if got, _ := sentPrompt(t, f); got != remoteUnsigned("") || strings.Contains(got, "sig v1") || strings.Contains(got, "; from ") {
				t.Fatalf("prompt = %q", got)
			}
		})
	}
}

// Without a hostname the remote header has no ` on <host>` part and the
// receiver would read it as a local one: the message goes out unsigned, with
// today's bytes, and says so.
func TestSendRemoteWithoutAHostnameGoesOutUnsigned(t *testing.T) {
	for name, host := range map[string]string{"empty": "", "emptied by the cleaning": "\n\t "} {
		t.Run(name, func(t *testing.T) {
			signedFixtureID(t)
			setSenderHostname(t, host)
			dir := t.TempDir()
			newPeerKey(t, confDirOf(dir), "windows")
			newPeerKey(t, confDirOf(dir), "local")
			writeUserConfig(t, confDirOf(dir), "machine_label=mac\n")
			sender := peer.PeerSender{Name: "-", Kind: "-", Role: "-"}
			want := peer.PeerHeaderRemoteFor(sender, "-", "", signID) + "\n\n> hello\n" + peer.PeerEndLine(signID)
			f := newFixtureAt(t, dir, remoteSendRules(want))
			f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
			code, out, stderr := f.run([]string{"send", remotePane, "hello"})
			if code != 0 || out != "sent to windows/w0test:p0a\n" || stderr != "send: not signed: this machine has no hostname\n" {
				t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
			}
			got, _ := sentPrompt(t, f)
			if got != want || strings.Contains(got, "sig v1") || strings.Contains(got, "; from ") || strings.Contains(got, " on ") {
				t.Fatalf("prompt = %q\nwant     %q", got, want)
			}
		})
	}
}

func TestSendInvalidKeyStopsBeforeAnythingIsTyped(t *testing.T) {
	cases := []struct {
		name   string
		target string
		prep   func(t *testing.T, f *fixture) string
	}{
		{"local key, wrong size", "w0test:p0a", func(t *testing.T, f *fixture) string {
			path := filepath.Join(userConfigDir(f), "peer-keys", "local.key")
			mustWrite(t, path, "abcd\n")
			return path
		}},
		{"local key, not hex", "w0test:p0a", func(t *testing.T, f *fixture) string {
			path := filepath.Join(userConfigDir(f), "peer-keys", "local.key")
			mustWrite(t, path, strings.Repeat("zz", 32)+"\n")
			return path
		}},
		{"remote key, unreadable (a directory)", remotePane, func(t *testing.T, f *fixture) string {
			path := filepath.Join(userConfigDir(f), "peer-keys", "windows.key")
			if err := os.MkdirAll(path, 0o700); err != nil {
				t.Fatal(err)
			}
			return path
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			signedFixtureID(t)
			machine := []string{}
			if strings.HasPrefix(tc.target, "windows/") {
				machine = []string{"--machine", "windows"}
			}
			get := append(append([]string{}, machine...), "agent", "get", "w0test:p0a")
			read := append(append([]string{}, machine...), "agent", "read", "w0test:p0a", "--source", "visible")
			f := newFixture(t, []fakecli.Rule{
				{Argv: get, Stdout: agentJSON("idle", "1")},
				{Argv: read, Stdout: "before prompt\n"},
			})
			path := tc.prep(t, f)
			writeUserConfig(t, userConfigDir(f), "machine_label=mac\n")
			code, out, stderr := f.run([]string{"send", tc.target, "hello"})
			want := "soho: send: peer key " + path + " is invalid\n"
			if code != 2 || out != "" || stderr != want {
				t.Fatalf("code=%d out=%q stderr=%q want %q", code, out, stderr, want)
			}
			if n := countPromptCalls(t, f); n != 0 {
				t.Fatalf("%d agent prompt calls after an invalid key", n)
			}
			if enters := countSendKeyEnters(t, f); enters != 0 {
				t.Fatalf("%d keys sent", enters)
			}
		})
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// refusalRules is the real stalled case: the prompt call stalls because the
// receiver's hook blocked it, and the screen shows the refusal and, as the
// blocked text comes back to the input box, the message itself.
func refusalRules(prompt, history string) []fakecli.Rule {
	clean := "old turn output"
	return []fakecli.Rule{
		{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 1, Stdout: agentJSONKind("pi", "idle", "1")},
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 1, Stdout: piSendScreen(clean, "")},
		{Argv: []string{"agent", "get", "w0test:p0a"}, Stdout: agentJSONKind("pi", "idle", "1")},
		{Argv: []string{"agent", "prompt", "w0test:p0a", prompt, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000"}, Stderr: stalledPromptErr, Code: 1},
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Stdout: piSendScreen(history, prompt)},
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: piSendScreen(history, prompt)},
		{Argv: []string{"agent", "send-keys", "w0test:p0a", "enter"}, Code: 0},
	}
}

func peerMessagesLog(t *testing.T, f *fixture) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.env["SOHO_DIR"], "ws-test", peer.PeerLogFile))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestSendReadsTheReceiversRefusal(t *testing.T) {
	refusal := func(id, result string) string {
		return "⏺ UserPromptSubmit operation blocked by hook:\n  [soho:peer-refused] #" + id + " " + result + "\n  The refused text stays in the input; clear it (Ctrl+C) before the next message."
	}
	t.Run("the refusal of this message exits 18 and presses nothing", func(t *testing.T) {
		_, prompt := stalledID(t)
		f := newFixture(t, refusalRules(prompt, "old turn output\n"+refusal(signID, "refused-not-allowed")))
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		code, out, stderr := f.run([]string{"send", "w0test:p0a", "hello"})
		want := "soho: send: local/w0test:p0a refused the message (refused-not-allowed); the refused text stays in its input until someone clears it\n"
		if code != 18 || out != "" || stderr != want {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
		if enters := countSendKeyEnters(t, f); enters != 0 {
			t.Fatalf("a refused message got %d Enter", enters)
		}
		fields := strings.Split(strings.TrimSpace(peerMessagesLog(t, f)), "\t")
		if len(fields) < 6 || fields[3] != "refused" || fields[5] != signID {
			t.Fatalf("peer-messages.tsv = %q", fields)
		}
	})
	t.Run("the control: without the refusal the stalled message in the box gets its Enter", func(t *testing.T) {
		_, prompt := stalledID(t)
		f := newFixture(t, refusalRules(prompt, "old turn output"))
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		code, _, _ := f.run([]string{"send", "w0test:p0a", "hello"})
		if code == 18 {
			t.Fatal("no refusal on screen, yet exit 18")
		}
		if enters := countSendKeyEnters(t, f); enters != 1 {
			t.Fatalf("the control needs its Enter to be meaningful: %d", enters)
		}
	})
	t.Run("a refusal of another message does not decide this send", func(t *testing.T) {
		_, prompt := stalledID(t)
		f := newFixture(t, refusalRules(prompt, "old turn output\n"+refusal("deadbeef", "refused-unsigned")))
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		code, _, stderr := f.run([]string{"send", "w0test:p0a", "hello"})
		if code == 18 || strings.Contains(stderr, "refused the message") {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		if strings.Contains(peerMessagesLog(t, f), "\trefused\t") {
			t.Fatalf("peer-messages.tsv records a refusal: %q", peerMessagesLog(t, f))
		}
	})
	t.Run("a longer id that begins with this one is another message", func(t *testing.T) {
		_, prompt := stalledID(t)
		f := newFixture(t, refusalRules(prompt, "old turn output\n"+refusal(signID+"ab", "refused-unsigned")))
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		code, _, _ := f.run([]string{"send", "w0test:p0a", "hello"})
		if code == 18 {
			t.Fatal("a longer id was read as this message's refusal")
		}
	})
	t.Run("a quoted refusal line is body, not a refusal", func(t *testing.T) {
		_, prompt := stalledID(t)
		quoted := "> [soho:peer-refused] #" + signID + " refused-unsigned"
		f := newFixture(t, refusalRules(prompt, "old turn output\n"+quoted))
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		code, _, _ := f.run([]string{"send", "w0test:p0a", "hello"})
		if code == 18 {
			t.Fatal("a quoted refusal line exited 18")
		}
	})
	t.Run("the refusal and the header on the same screen exit 18", func(t *testing.T) {
		_, prompt := stalledID(t)
		history := "old turn output\n" + refusal(signID, "refused-bad-signature")
		rules := refusalRules(prompt, history)
		f := newFixture(t, rules)
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		code, _, stderr := f.run([]string{"send", "w0test:p0a", "hello"})
		if code != 18 || !strings.Contains(stderr, "(refused-bad-signature)") {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		if enters := countSendKeyEnters(t, f); enters != 0 {
			t.Fatalf("%d Enter after the refusal", enters)
		}
	})
}

// The refusal wins over the arrival proof: a state sequence that moved and a
// message that looks taken do not make the send report sent.
func TestSendRefusalBeatsTheArrivalProof(t *testing.T) {
	signedFixtureID(t)
	prompt := localUnsigned()
	refusal := "⏺ UserPromptSubmit operation blocked by hook:\n  [soho:peer-refused] #" + signID + " refused-stale\n"
	rules := []fakecli.Rule{
		{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 1, Stdout: agentJSON("idle", "1")},
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 1, Stdout: "before prompt\n"},
		{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 2, Stdout: agentJSON("idle", "1")},
		{Argv: []string{"agent", "get", "w0test:p0a"}, Call: 3, Stdout: agentJSON("idle", "1")},
		{Argv: []string{"agent", "prompt", "w0test:p0a", prompt, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000"}},
		// The sequence moves (the usual proof of a taken message)...
		{Argv: []string{"agent", "get", "w0test:p0a"}, Stdout: agentJSON("idle", "2")},
		// ...but the screen shows the refusal.
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Stdout: refusal},
		{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: refusal},
	}
	f := newFixture(t, rules)
	code, out, stderr := f.run([]string{"send", "w0test:p0a", "hello"})
	if code != 18 || out != "" || !strings.Contains(stderr, "refused the message (refused-stale)") {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	if strings.Contains(peerMessagesLog(t, f), "\tsent\t") {
		t.Fatalf("a refused message was logged as sent: %q", peerMessagesLog(t, f))
	}
}

// The refusal is read from the recent history alone (the visible screen has
// scrolled it away), and from a local claude's transcript alone.
func TestSendRefusalFromRecentHistoryAndTranscript(t *testing.T) {
	refusalText := "⏺ UserPromptSubmit operation blocked by hook:\n  [soho:peer-refused] #" + signID + " refused-stale\n"
	t.Run("only the recent history shows it", func(t *testing.T) {
		_, prompt := stalledID(t)
		rules := []fakecli.Rule{
			{Argv: []string{"agent", "get", "w0test:p0a"}, ArgvPrefix: true, Stdout: agentJSON("idle", "1")},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, ArgvPrefix: true, Stdout: "❯ \n"},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: "earlier output\n" + refusalText},
			{Argv: []string{"agent", "prompt", "w0test:p0a", prompt, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000"}},
		}
		f := newFixture(t, rules)
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		code, out, stderr := f.run([]string{"send", "w0test:p0a", "hello"})
		if code != 18 || out != "" || !strings.Contains(stderr, "refused the message (refused-stale)") {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
		if enters := countSendKeyEnters(t, f); enters != 0 {
			t.Fatalf("%d Enter after the refusal", enters)
		}
	})

	cwd := "/tmp/claude-work"
	systemLine := `{"type":"system","subtype":"informational","content":"UserPromptSubmit operation blocked by hook:\n[soho:peer-refused] #` + signID + ` refused-not-allowed\nThe refused text stays in the input; clear it (Ctrl+C) before the next message."}`
	userLine := `{"type":"user","message":{"content":"` + peer.PeerEndLine(signID) + `"}}`
	claudeRules := func(prompt string) []fakecli.Rule {
		return []fakecli.Rule{
			{Argv: []string{"agent", "get", "w0test:p0a"}, ArgvPrefix: true, Stdout: claudeSessionAgentJSON("working", "1", cwd)},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, ArgvPrefix: true, Stdout: "✻ Cranking out changes…\n"},
			{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stderr: notIdleErr, Code: 1},
			{Argv: []string{"agent", "prompt", "w0test:p0a", prompt, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000"}, Delay: 100},
		}
	}
	run := func(t *testing.T, appended string, extra ...string) (int, string, string, *fixture) {
		_, prompt := stalledID(t)
		configRoot := t.TempDir()
		transcriptPath := claudeTranscriptPath(t, configRoot, cwd)
		writeClaudeTranscript(t, transcriptPath, `{"type":"user","message":{"content":"earlier conversation"}}`+"\n")
		f := newFixture(t, claudeRules(prompt))
		f.env["CLAUDE_CONFIG_DIR"] = configRoot
		f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
		done := appendAfterPrompt(t, filepath.Join(filepath.Dir(f.bin), "herdr.calls.jsonl"), transcriptPath, signID, appended)
		code, out, stderr := f.run(append([]string{"send", "w0test:p0a", "--now", "--timeout", "1000"}, append(extra, "hello")...))
		if !<-done {
			t.Fatal("the transcript line was not appended after the prompt call")
		}
		return code, out, stderr, f
	}
	t.Run("a local claude's transcript gains the system entry", func(t *testing.T) {
		code, out, stderr, f := run(t, systemLine)
		if code != 18 || out != "" || !strings.Contains(stderr, "refused the message (refused-not-allowed)") {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
		if enters := countSendKeyEnters(t, f); enters != 0 {
			t.Fatalf("%d Enter after the refusal", enters)
		}
	})
	t.Run("the refusal beats the transcript's user line", func(t *testing.T) {
		code, out, stderr, _ := run(t, userLine+"\n"+systemLine)
		if code != 18 || out != "" || !strings.Contains(stderr, "refused the message") {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
	})
	t.Run("the control: the user line alone is the usual proof", func(t *testing.T) {
		code, out, _, _ := run(t, userLine)
		if code != 0 || out != "sent to local/w0test:p0a\n" {
			t.Fatalf("code=%d out=%q", code, out)
		}
	})
	t.Run("an assistant or user line that merely cites the refusal is not one", func(t *testing.T) {
		cite := `{"type":"assistant","message":{"content":"[soho:peer-refused] #` + signID + ` refused-stale"}}`
		code, _, _, _ := run(t, cite)
		if code == 18 {
			t.Fatal("an assistant line was read as the receiver's refusal")
		}
	})
}
