package herdr

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/djalmajr/soho/internal/platform"
)

// caller_session.go is the session half of the caller-pane check (DJA-406).
// A Claude Code background session that continued the session of a pane
// runs outside that pane's shell, so the process tree check cannot see it.
// The pane still names its own Claude Code session (herdr agent_session),
// and that session's transcript ends in a continued-in record that leads
// to the session of this process. The check only ever turns a tree
// mismatch into verified; every failure leaves the mismatch alone.
//
// The formats it reads were measured on Claude Code 2.1.302 and 2.1.303:
// the CLAUDE_CODE_SESSION_ID variable, the herdr agent_session of a Claude
// pane, and the transcript record
//
//	{"type":"continued-in","sessionId":"<old>","continuedInSessionId":"<new>"}
//
// at <claude dir>/projects/<project>/<session>.jsonl. Nothing here is a
// security boundary: whoever can set the environment of a process can set
// this variable too, as with HERDR_PANE_ID itself. The check tells a
// continued session from an id inherited by accident.

const (
	// callerSessionTimeoutMs bounds each herdr read of the session check.
	callerSessionTimeoutMs = 10000
	// continuedTailBytes is how much of the end of a transcript is read.
	continuedTailBytes = 64 * 1024
	// continuedMaxHops is the longest chain followed: A -> B -> S.
	continuedMaxHops = 2
	// continuedMaxTargets caps the destinations followed per hop.
	continuedMaxTargets = 4
	// suggestMaxReads caps the transcripts the pane suggestion of a refusal
	// reads beyond the ones the check already read. Past it the refusal
	// names no pane: with panes left unexamined, a candidate is not proved
	// to be the only one.
	suggestMaxReads = 32
	// suggestMaxTime caps the wall time of the pane suggestion, from its
	// start, under the same rule.
	suggestMaxTime = 2 * time.Second
)

// sessionIDPattern is the only shape of session id that is trusted: a
// lower-case UUID. It is also what keeps an id out of a path.
var sessionIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// paneIDPattern is the shape of a pane id that may be named in a refusal.
var paneIDPattern = regexp.MustCompile(`^[A-Za-z0-9:_.-]{1,64}$`)

func validSessionID(id string) bool { return sessionIDPattern.MatchString(id) }

// ownSession is the Claude Code session of this process: the
// CLAUDE_CODE_SESSION_ID of the environment it was handed, when it is a
// UUID.
func ownSession(env platform.Env) string {
	id := env.Get("CLAUDE_CODE_SESSION_ID")
	if validSessionID(id) {
		return id
	}
	return ""
}

// claudeConfigRoot is CLAUDE_CONFIG_DIR when it has a value, else
// <home>/.claude.
func claudeConfigRoot(env platform.Env) string {
	if root := env.Get("CLAUDE_CONFIG_DIR"); root != "" {
		return root
	}
	home := platform.HomeDir(platform.Current(), env)
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".claude")
}

// agentSessionOf reads the agent_session of a pane object (herdr pane get
// or one entry of pane list). Only a Claude session identified by id with
// a UUID value counts.
func agentSessionOf(pane json.RawMessage) string {
	var parsed struct {
		AgentSession struct {
			Agent string `json:"agent"`
			Kind  string `json:"kind"`
			Value string `json:"value"`
		} `json:"agent_session"`
	}
	if json.Unmarshal(pane, &parsed) != nil {
		return ""
	}
	session := parsed.AgentSession
	if session.Agent != "claude" || session.Kind != "id" || !validSessionID(session.Value) {
		return ""
	}
	return session.Value
}

// paneSession is the Claude Code session herdr reports for one pane, or ""
// on any failure, timeout or unexpected shape.
func paneSession(pane string, env platform.Env) string {
	r := platform.RunCli("herdr", []string{"pane", "get", pane}, platform.RunOptions{Env: env, TimeoutMs: callerSessionTimeoutMs})
	if r.NotFound || r.TimedOut || r.Status == nil || *r.Status != 0 {
		return ""
	}
	var payload struct {
		Result struct {
			Pane json.RawMessage `json:"pane"`
		} `json:"result"`
	}
	if json.Unmarshal([]byte(r.Stdout), &payload) != nil {
		return ""
	}
	return agentSessionOf(payload.Result.Pane)
}

// panesSessions is every pane herdr lists, with its Claude session: the
// panes without a valid one are left out. herdr pane list without
// --workspace covers all workspaces.
func panesSessions(env platform.Env) map[string]string {
	r := platform.RunCli("herdr", []string{"pane", "list"}, platform.RunOptions{Env: env, TimeoutMs: callerSessionTimeoutMs})
	if r.NotFound || r.TimedOut || r.Status == nil || *r.Status != 0 {
		return nil
	}
	var payload struct {
		Result struct {
			Panes []json.RawMessage `json:"panes"`
		} `json:"result"`
	}
	if json.Unmarshal([]byte(r.Stdout), &payload) != nil {
		return nil
	}
	out := map[string]string{}
	for _, raw := range payload.Result.Panes {
		var head struct {
			PaneID string `json:"pane_id"`
		}
		if json.Unmarshal(raw, &head) != nil || !paneIDPattern.MatchString(head.PaneID) {
			continue
		}
		if session := agentSessionOf(raw); session != "" {
			out[head.PaneID] = session
		}
	}
	return out
}

// listProjects and readTranscriptTail are the file reads of the session
// check; the tests count them.
var (
	listProjects       = os.ReadDir
	readTranscriptTail = readTail
)

// sessionChain follows continued-in records from a session. It keeps the
// destinations it already read, so one check never reads a transcript
// twice, and lists <root>/projects once, the failure included.
type sessionChain struct {
	root    string
	targets map[string][]string
	// listed reports that projects holds the listing (nil on a failure).
	listed   bool
	projects []os.DirEntry
	// budget, when set, bounds the transcripts read beyond the cache.
	budget *readBudget
}

// readBudget is what is left to a search: reads of transcripts not read
// yet, and a deadline. spent records that a read was refused.
type readBudget struct {
	reads    int
	deadline time.Time
	spent    bool
}

// expired reports that the deadline has passed.
func (b *readBudget) expired() bool {
	return !platform.Now().Before(b.deadline)
}

func newSessionChain(env platform.Env) *sessionChain {
	return &sessionChain{root: claudeConfigRoot(env), targets: map[string][]string{}}
}

// reaches reports whether own is from itself or at most continuedMaxHops
// continued-in records away from it, and which of the two it is.
func (c *sessionChain) reaches(from, own string) (reason string, ok bool) {
	if from == own {
		return "the pane's Claude Code session is this process's session", true
	}
	seen := map[string]bool{from: true}
	frontier := []string{from}
	for hop := 0; hop < continuedMaxHops && len(frontier) > 0; hop++ {
		var next []string
		for _, id := range frontier {
			for _, target := range c.continuedFrom(id) {
				if len(next) >= continuedMaxTargets {
					break
				}
				if target == own {
					return "the pane's Claude Code session continued in this process's session", true
				}
				if !seen[target] {
					seen[target] = true
					next = append(next, target)
				}
			}
		}
		frontier = next
	}
	return "", false
}

// continuedFrom is the destinations of the continued-in records at the end
// of a session's transcript, the most recent first, at most
// continuedMaxTargets.
func (c *sessionChain) continuedFrom(session string) []string {
	if got, ok := c.targets[session]; ok {
		return got
	}
	if b := c.budget; b != nil && b.expired() {
		b.spent = true
		return nil
	}
	got := c.readContinued(session)
	if c.budget != nil && c.budget.spent {
		// A read the ceiling refused is not a result: it is not cached.
		return nil
	}
	c.targets[session] = got
	return got
}

func (c *sessionChain) readContinued(session string) []string {
	if c.root == "" || !validSessionID(session) {
		return nil
	}
	path, ok := c.transcriptPath(session)
	if !ok {
		return nil
	}
	// Only a transcript that is really read spends the ceiling: a session
	// without one, an empty root or a failed listing does not.
	if b := c.budget; b != nil {
		if b.reads <= 0 {
			b.spent = true
			return nil
		}
		b.reads--
	}
	tail, ok := readTranscriptTail(path, continuedTailBytes)
	if !ok {
		return nil
	}
	lines := bytes.Split(tail, []byte("\n"))
	// Only complete lines: the piece after the last newline is not one.
	lines = lines[:len(lines)-1]
	var out []string
	seen := map[string]bool{}
	for i := len(lines) - 1; i >= 0 && len(out) < continuedMaxTargets; i-- {
		if !bytes.Contains(lines[i], []byte(`"continued-in"`)) {
			continue
		}
		var record struct {
			Type string `json:"type"`
			To   string `json:"continuedInSessionId"`
		}
		if json.Unmarshal(lines[i], &record) != nil || record.Type != "continued-in" || !validSessionID(record.To) || seen[record.To] {
			continue
		}
		seen[record.To] = true
		out = append(out, record.To)
	}
	return out
}

// transcriptPath is the one <root>/projects/<project>/<session>.jsonl that
// exists; zero or more than one end the search. The session is a UUID, so
// it cannot leave the project directory, and the project directories are
// listed rather than matched as a glob, so a root with glob characters is
// safe.
func (c *sessionChain) transcriptPath(session string) (string, bool) {
	projects := filepath.Join(c.root, "projects")
	if !c.listed {
		c.listed = true
		if entries, err := listProjects(projects); err == nil {
			c.projects = entries
		}
	}
	found := ""
	for _, entry := range c.projects {
		candidate := filepath.Join(projects, entry.Name(), session+".jsonl")
		info, err := os.Stat(candidate)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if found != "" {
			return "", false
		}
		found = candidate
	}
	return found, found != ""
}

// readTail returns the last max bytes of a file starting at a line
// boundary: when the file is longer, the first (cut) line is dropped.
func readTail(path string, max int64) ([]byte, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, false
	}
	start := info.Size() - max
	if start < 0 {
		start = 0
	}
	// One byte of context: when it is a newline, the first line is whole.
	skip := false
	if start > 0 {
		start--
		skip = true
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, false
	}
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, false
	}
	if skip {
		cut := bytes.IndexByte(data, '\n')
		if cut < 0 {
			return nil, true
		}
		data = data[cut+1:]
	}
	return data, true
}

// verifiedBySession is the session check proper. It runs only after the
// tree check found a mismatch and needs this process's session.
func verifiedBySession(pane, own string, env platform.Env) (CallerCheck, bool) {
	var res CallerCheck
	res.Session = own
	if own == "" {
		return res, false
	}
	chain := newSessionChain(env)
	if session := paneSession(pane, env); session != "" {
		res.PaneSession = session
		if reason, ok := chain.reaches(session, own); ok {
			res.Reason = reason
			return res, true
		}
	}
	res.ShownIn = shownIn(pane, own, chain, env)
	return res, false
}

// shownIn is the single pane, other than the claimed one, whose Claude
// session is this process's or leads to it. Two candidates, or none, give
// "": the refusal only suggests a pane when there is no doubt. The search
// reads at most suggestMaxReads transcripts the check has not read yet,
// within suggestMaxTime; once either runs out it gives "", even with a
// candidate found. The deadline is checked before each pane, with or
// without a fresh read, and once after the loop: a read under way is not
// cut short, and its result is dropped if the deadline passed meanwhile.
// The panes go in id order, so the outcome does not depend on the order
// of a map.
func shownIn(claimed, own string, chain *sessionChain, env platform.Env) string {
	budget := &readBudget{reads: suggestMaxReads, deadline: platform.Now().Add(suggestMaxTime)}
	sessions := panesSessions(env)
	panes := make([]string, 0, len(sessions))
	for pane := range sessions {
		panes = append(panes, pane)
	}
	sort.Strings(panes)
	chain.budget = budget
	defer func() { chain.budget = nil }()
	found := ""
	for _, pane := range panes {
		if budget.expired() {
			return ""
		}
		if pane == claimed {
			continue
		}
		_, ok := chain.reaches(sessions[pane], own)
		if budget.spent {
			return ""
		}
		if !ok {
			continue
		}
		if found != "" {
			return ""
		}
		found = pane
	}
	if budget.expired() {
		return ""
	}
	return found
}
