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
	noHost bool   // remote opening without a hostname: just `<pane>`
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

// kindItems is the Sender line's items in the current format: the name is
// carried by the line's head, so only kind/role/model are items.
func (s spec) kindItems() []string {
	var items []string
	for _, kv := range [][2]string{{"kind", s.kind}, {"role", s.role}, {"model", s.model}} {
		if kv[1] != "" {
			items = append(items, kv[0]+" "+kv[1])
		}
	}
	return items
}

func (s spec) sentTime() string {
	at := s.sent
	if at.IsZero() {
		at = testNow
	}
	return sentAt(at)
}

func (s spec) messageID() string {
	id := s.id
	if id == "" {
		id = "0f23aac6"
	}
	return id
}

// remoteRef is the spec's remote reference: `<pane> on <host>`, or just
// `<pane>` when noHost (spec.host defaults like the old helper did).
func (s spec) remoteRef() (ref, host string) {
	pane := s.pane
	if pane == "" {
		pane = "w6X:p1"
	}
	if s.noHost {
		return pane, ""
	}
	host = s.host
	if host == "" {
		host = "mac.local"
	}
	return pane + " on " + host, host
}

// replyLine is the spec's reply command line, the same in both formats.
func (s spec) replyLine() string {
	if s.remote {
		ref, host := s.remoteRef()
		if host != "" {
			return fmt.Sprintf("Reply with: soho send <this machine's name in your herdr machine list>/%s \"<your reply>\" (this machine is %s)", ref, host)
		}
		return fmt.Sprintf("Reply with: soho send <this machine's name in your herdr machine list>/%s \"<your reply>\"", strings.SplitN(ref, " on ", 2)[0])
	}
	ref := s.ref
	if ref == "" {
		ref = "local/w6X:p1"
	}
	return fmt.Sprintf("Reply with: soho send %s \"<your reply>\"", ref)
}

// newSenderLine is the current format's Sender line: the name with the
// reference in parentheses, or the reference alone when no name is known,
// then the kind/role/model items; spec.tail models an older sender's items.
func (s spec) newSenderLine(ref string) string {
	head := ""
	switch {
	case s.name != "" && ref != "":
		head = s.name + " (" + ref + ")"
	case s.name != "":
		head = s.name
	case ref != "":
		head = ref
	}
	items := s.kindItems()
	if head == "" && len(items) == 0 {
		return ""
	}
	var line string
	switch {
	case head != "" && len(items) > 0:
		line = "Sender: " + head + "; " + strings.Join(items, "; ")
	case head != "":
		line = "Sender: " + head
	default:
		line = "Sender: " + strings.Join(items, "; ")
	}
	if s.tail != "" {
		line += "; " + s.tail
	}
	return line + "."
}

// quotedBody is the spec's body quoted the way `soho send` quotes it.
func (s spec) quotedBody() []string {
	body := s.body
	if len(body) == 0 {
		body = []string{"hello", "second line"}
	}
	var lines []string
	for _, line := range body {
		lines = append(lines, "> "+line)
	}
	return lines
}

// unsigned is the message as `soho send` assembles it in the current format
// (the sender no longer adds a signature; spec.tail carries one when it
// models an older sender).
func (s spec) unsigned() string {
	id := s.messageID()
	var ref string
	if s.remote {
		ref, _ = s.remoteRef()
	} else {
		ref = s.ref
		if ref == "" {
			ref = "local/w6X:p1"
		}
	}
	lines := []string{"[soho:peer:" + id + "]"}
	if sender := s.newSenderLine(ref); sender != "" {
		lines = append(lines, sender)
	}
	lines = append(lines, "Sent: "+s.sentTime(), s.replyLine(), `A message from another agent follows, each line quoted with "> ".`, "")
	lines = append(lines, s.quotedBody()...)
	lines = append(lines, "[/soho:peer:"+id+"]")
	return strings.Join(lines, "\n")
}

// unsignedOld is the previous format of the same message: the id after the
// marker on the long opening line (with the send time), and the old closing
// line. Receivers accept it for one release.
func (s spec) unsignedOld() string {
	id := s.messageID()
	var opening string
	if s.remote {
		ref, host := s.remoteRef()
		if host != "" {
			pane := strings.SplitN(ref, " on ", 2)[0]
			opening = fmt.Sprintf("[soho:peer] #%s Message from another agent — %s on %s, sent %s.", id, pane, host, s.sentTime())
		} else {
			opening = fmt.Sprintf("[soho:peer] #%s Message from another agent — %s, sent %s.", id, ref, s.sentTime())
		}
	} else {
		ref := s.ref
		if ref == "" {
			ref = "local/w6X:p1"
		}
		opening = fmt.Sprintf("[soho:peer] #%s Message from another agent — %s, sent %s.", id, ref, s.sentTime())
	}
	lines := []string{opening}
	if items := s.senderItems(); len(items) > 0 || s.tail != "" {
		line := "Sender: "
		if len(items) > 0 {
			line += strings.Join(items, "; ")
		}
		if s.tail != "" {
			if line != "Sender: " {
				line += "; "
			}
			line += s.tail
		}
		lines = append(lines, line+".")
	}
	lines = append(lines, s.replyLine(), `The message follows, each line quoted with "> ".`, "")
	lines = append(lines, s.quotedBody()...)
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
