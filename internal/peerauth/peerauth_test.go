package peerauth_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/peerauth"
)

var testNow = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

func sentAt(at time.Time) string { return at.UTC().Format(time.RFC3339) }

// spec describes a peer message the way the sender builds it.
type spec struct {
	id     string
	remote bool
	host   string // remote opening: `<pane> on <host>`
	ref    string // local opening: the sender reference
	pane   string
	name   string
	kind   string
	role   string
	model  string
	sent   time.Time
	body   []string
	// tail is appended to the Sender line, after the last item, before the
	// period: the items an older sender adds (`from <label>`,
	// `sig v1:<hex>`).
	tail string
}

func (s spec) senderItems() []string {
	var items []string
	for _, kv := range [][2]string{{"name", s.name}, {"kind", s.kind}, {"role", s.role}, {"model", s.model}} {
		if kv[1] != "" {
			items = append(items, kv[0]+" "+kv[1])
		}
	}
	return items
}

// unsigned is the message as `soho send` assembles it (the sender no longer
// adds a signature; spec.tail carries one when it models an older sender).
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
		opening = fmt.Sprintf("[soho:peer] #%s Message from another agent — %s on %s, sent %s.", id, pane, host, sentAt(at))
		reply = fmt.Sprintf("Reply with: soho send <this machine's name in your herdr machine list>/%s \"<your reply>\" (this machine is %s)", pane, host)
	} else {
		ref := s.ref
		if ref == "" {
			ref = "local/w6X:p1"
		}
		opening = fmt.Sprintf("[soho:peer] #%s Message from another agent — %s, sent %s.", id, ref, sentAt(at))
		reply = fmt.Sprintf("Reply with: soho send %s \"<your reply>\"", ref)
	}
	lines := []string{opening}
	if items := s.senderItems(); len(items) > 0 {
		line := "Sender: " + strings.Join(items, "; ")
		if s.tail != "" {
			line += "; " + s.tail
		}
		lines = append(lines, line+".")
	}
	lines = append(lines, reply, `The message follows, each line quoted with "> ".`, "")
	body := s.body
	if len(body) == 0 {
		body = []string{"hello", "second line"}
	}
	for _, line := range body {
		lines = append(lines, "> "+line)
	}
	lines = append(lines, fmt.Sprintf("[/soho:peer] #%s end of message", id))
	return strings.Join(lines, "\n")
}

// wrap puts a message the way Claude Code delivers a paste: with the \n\n
// prefix and the pasted_content wrapper whose id repeats.
func wrap(message string) string {
	return "\n\n<pasted_content id=\"c967\">\n" + message + "\n</pasted_content id=\"c967\">\n"
}

type fixture struct {
	t   *testing.T
	dir string
	// projectOff is the target project's inbound=off.
	projectOff   bool
	projectErr   error
	now          time.Time
	pane         string
	projectReads int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	return &fixture{t: t, dir: filepath.Join(t.TempDir(), "soho"), now: testNow, pane: "w6X:p2"}
}

func (f *fixture) source() peerauth.Source {
	return peerauth.Source{
		Dir: f.dir,
		ProjectInboundOff: func() (bool, error) {
			f.projectReads++
			return f.projectOff, f.projectErr
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
