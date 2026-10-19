package peerauth_test

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/peerauth"
)

// secretMarker is a fake secret placed in message bodies: it must never reach
// a block reason, the receiver's log or the replay cache.
const secretMarker = "SECRET-DJA196-BODY"

var testNow = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

func sent(at time.Time) string { return at.UTC().Format(time.RFC3339) }

// spec describes a peer message the way the sender builds it.
type spec struct {
	id      string
	remote  bool
	host    string // remote opening: `<pane> on <host>`
	ref     string // local opening: the sender reference
	pane    string
	name    string
	kind    string
	role    string
	model   string
	from    string
	sent    time.Time
	replyTo string
	body    []string
}

func (s spec) senderItems() []string {
	var items []string
	for _, kv := range [][2]string{{"name", s.name}, {"kind", s.kind}, {"role", s.role}, {"model", s.model}, {"from", s.from}} {
		if kv[1] != "" {
			items = append(items, kv[0]+" "+kv[1])
		}
	}
	return items
}

// unsigned is the message as `soho send` assembles it before signing.
func (s spec) unsigned() string {
	id := s.id
	if id == "" {
		id = "0f23aac6"
	}
	at := s.sent
	if at.IsZero() {
		at = testNow
	}
	var opening, reply string
	if s.remote {
		pane := s.pane
		if pane == "" {
			pane = "w6X:p1"
		}
		host := s.host
		if host == "" {
			host = "mac.local"
		}
		opening = fmt.Sprintf("[soho:peer] #%s Message from another agent — %s on %s, sent %s.", id, pane, host, sent(at))
		reply = fmt.Sprintf("Reply with: soho send <this machine's name in your herdr machine list>/%s \"<your reply>\" (this machine is %s)", pane, host)
	} else {
		ref := s.ref
		if ref == "" {
			ref = "local/w6X:p1"
		}
		opening = fmt.Sprintf("[soho:peer] #%s Message from another agent — %s, sent %s.", id, ref, sent(at))
		reply = fmt.Sprintf("Reply with: soho send %s \"<your reply>\"", ref)
	}
	if s.replyTo != "" {
		reply = s.replyTo
	}
	lines := []string{opening}
	if items := s.senderItems(); len(items) > 0 {
		lines = append(lines, "Sender: "+strings.Join(items, "; ")+".")
	}
	lines = append(lines, reply, `The message follows, each line quoted with "> ".`, "")
	body := s.body
	if len(body) == 0 {
		body = []string{"hello " + secretMarker, "second line"}
	}
	for _, line := range body {
		lines = append(lines, "> "+line)
	}
	lines = append(lines, fmt.Sprintf("[/soho:peer] #%s end of message", id))
	return strings.Join(lines, "\n")
}

func mustKey(t *testing.T, dir, name string) []byte {
	t.Helper()
	path, err := peerauth.NewKey(dir, name)
	if err != nil {
		t.Fatalf("NewKey(%s): %v", name, err)
	}
	key, status := peerauth.ReadKey(dir, name)
	if status != peerauth.KeyOK {
		t.Fatalf("ReadKey(%s) after NewKey(%s) = %v", name, path, status)
	}
	return key
}

func mustSign(t *testing.T, key []byte, message string) string {
	t.Helper()
	signed, err := peerauth.Sign(key, message)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

// wrap puts a message the way Claude Code delivers a paste: with the
// \n\n prefix and the pasted_content wrapper whose id repeats.
func wrap(message string) string {
	return "\n\n<pasted_content id=\"c967\">\n" + message + "\n</pasted_content id=\"c967\">\n"
}

type fixture struct {
	t      *testing.T
	dir    string
	policy peerauth.Policy
	// projectOff is the target project's inbound=off.
	projectOff bool
	now        time.Time
	pane       string
	reads      map[string]int
}

func newFixture(t *testing.T, mode peerauth.Mode, allow ...string) *fixture {
	t.Helper()
	return &fixture{t: t, dir: filepath.Join(t.TempDir(), "soho"), policy: peerauth.Policy{Mode: mode, Allow: allow}, now: testNow, pane: "w6X:p2", reads: map[string]int{}}
}

func (f *fixture) source() peerauth.Source {
	return peerauth.Source{
		Dir:    f.dir,
		Policy: func() (peerauth.Policy, error) { f.reads["policy"]++; return f.policy, nil },
		ProjectInboundOff: func() (bool, error) {
			f.reads["project"]++
			return f.projectOff, nil
		},
		Pane: f.pane,
		Now:  func() time.Time { return f.now },
	}
}

func (f *fixture) check(prompt string) peerauth.Outcome {
	f.t.Helper()
	return peerauth.Check(prompt, f.source())
}

func results(o peerauth.Outcome) []peerauth.Result {
	var out []peerauth.Result
	for _, v := range o.Verdicts {
		out = append(out, v.Result)
	}
	return out
}

func wantOne(t *testing.T, o peerauth.Outcome, want peerauth.Result) {
	t.Helper()
	got := results(o)
	if len(got) != 1 || got[0] != want {
		t.Fatalf("results = %v, want [%s]", got, want)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(raw)
}

func randomHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}
