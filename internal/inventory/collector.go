package inventory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/djalmajr/soho/internal/backend"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/dispatch"
	"github.com/djalmajr/soho/internal/kinds"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/sessionmeta"
	"github.com/djalmajr/soho/internal/transport"
	"github.com/djalmajr/soho/internal/wait"
)

// shownAgentLimit is the contract's display bound: at most this many
// shown agents, in project/session/roster order. Hidden agents never
// consume it.
const shownAgentLimit = 500

// The roster row columns the spawn writer persists
// (internal/spawn: name, pane, kind, role, family, created_pane, cwd,
// started, model, approvals, roles, lane, burst, args, effort, mcp):
// the collector reads only the named ones by their written index.
const (
	rosterColName    = 0
	rosterColPane    = 1
	rosterColKind    = 2
	rosterColRole    = 3
	rosterColStarted = 7
	rosterColModel   = 8
	rosterColEffort  = 14
)

var (
	// ErrTerminalUnavailable is the stable generic refusal of the
	// initial local terminal listing when the selected backend's reply
	// is a failure or invalid. It never carries the backend error text,
	// which can name private values, and nothing is printed.
	ErrTerminalUnavailable = errors.New("the terminal server cannot be read")

	// ErrEmptyVersion refuses a document whose version would be empty:
	// the contract requires soho_version to never be empty.
	ErrEmptyVersion = errors.New("the inventory version is empty")

	// ErrNegativeGeneratedMS refuses a negative observation time: the
	// contract requires generated_ms to be a nonnegative epoch
	// millisecond count.
	ErrNegativeGeneratedMS = errors.New("the inventory generated time is negative")

	// ErrDocumentTooLarge refuses a successful document whose required
	// base fields alone exceed the reader's MaxDocumentBytes wire
	// budget. It carries fixed nonprivate text, never the document
	// content, and nothing is printed.
	ErrDocumentTooLarge = errors.New("the inventory document exceeds the reader byte budget")
)

// Request is one inventory collection over the local machine: the
// environment to load the configuration from, the version the document
// names, the user configuration selection, the observation time and the
// known original TMPDIR whose scratch reports level is approved (""
// approves none).
type Request struct {
	Env         platform.Env
	Version     string
	User        core.UserSelection
	GeneratedMS int64
	ScratchBase string
}

// snapshotList is the private per-invocation runtime wrapper: it answers
// the unfiltered local listing (Scope.All) with the original typed
// snapshot of this invocation, so the shared stale-name inspection never
// issues a repeated unfiltered list call; every other scope and every
// other method is delegated unchanged to the selected backend. It is one
// local value, never a global cache, and it changes no state,
// classification or output.
type snapshotList struct {
	transport.Transport
	all transport.Reply[[]transport.Terminal]
}

func (r snapshotList) List(ctx context.Context, scope transport.Scope, call transport.Call) transport.Reply[[]transport.Terminal] {
	if scope.All {
		return r.all
	}
	return r.Transport.List(ctx, scope, call)
}

// liveTerminal is one observed local live terminal with its resolved
// catalog kind and canonical project root.
type liveTerminal struct {
	term transport.Terminal
	kind string
	root string
}

// paneTuple is one raw observation of a physical local terminal, before
// any validity filtering: the identity the server reported for that
// pane, the known catalog kind resolved from the row's own program
// hints, and the strict canonical root of the row's reported cwd (""
// when unresolvable). Two observations of one pane conflict when their
// tuples differ.
type paneTuple struct {
	name    string
	kind    string
	session string
	root    string
}

// builtSession is one show-mode session with its agents in document
// order (the registered orchestrator first, then the roster workers).
type builtSession struct {
	id     string
	agents []Agent
}

// builtProject is one show-mode project in first-observed order.
type builtProject struct {
	name     string
	sessions []builtSession
}

// collector is the per-invocation state of one Collect: the selected
// services with the snapshot runtime, the observed live terminals (one
// physical identity per pane) and the first-observed project and
// session order. The caches hold only this invocation's successful
// observations.
type collector struct {
	req          Request
	env          platform.Env
	svc          transport.Services
	terms        map[string]liveTerminal // pane id -> one physical identity
	roots        map[string]string       // observed cwd -> canonical root (successful only)
	projectOrder []string
	sessionSeen  map[string]bool
	sessions     map[string][]string // canonical root -> first-observed sessions
}

// Collect assembles one schema 1 document from the local machine: the
// live local terminals of the selected backend, their canonical project
// roots, the projects' opt-in privacy mode and the verified private
// session metadata. It is intrinsically read-only: it writes nothing,
// creates nothing, performs no caller, current-pane or workspace
// discovery and no remote call, and works under SOHO_NOWRITE=1. A failed
// or invalid initial listing is the stable generic refusal with no
// output; version and time misuse are separate refusals.
func Collect(req Request) (Document, error) {
	if req.Version == "" {
		return Document{}, ErrEmptyVersion
	}
	if req.GeneratedMS < 0 {
		return Document{}, ErrNegativeGeneratedMS
	}
	boot, err := core.LoadReadOnlyConfig(req.Env, "", "", req.User)
	if err != nil {
		return Document{}, err
	}
	// Select exactly one backend: the resolved transport mode travels in
	// a cloned environment, so the caller's environment is never mutated.
	mode := core.TransportMode(&boot.Config, req.Env)
	env := req.Env.Clone()
	env["SOHO_TRANSPORT"] = mode
	svc := backend.New(env)
	// The local machine discovery: the unfiltered local listing, with no
	// caller, current-pane or workspace discovery and no remote call.
	reply := svc.Runtime.List(context.Background(), transport.Scope{All: true}, transport.Call{})
	if reply.Err != nil || !reply.Valid {
		return Document{}, ErrTerminalUnavailable
	}
	c := &collector{
		req:         req,
		env:         env,
		svc:         svc,
		terms:       map[string]liveTerminal{},
		roots:       map[string]string{},
		sessionSeen: map[string]bool{},
		sessions:    map[string][]string{},
	}
	svc.Runtime = snapshotList{Transport: svc.Runtime, all: reply}
	c.svc = svc
	c.observe(reply.Value)
	return c.assemble()
}

// observe aggregates every nonremote local observation of each physical
// pane BEFORE any validity filtering, and then binds only the
// unambiguous ones. A pane observed with more than one distinct
// identity tuple is unverifiable and omitted regardless of row order, so
// an unknown or malformed conflicting observation never blesses an
// earlier or later good row and a stale matching identity is never
// accepted from a later row; identical duplicate rows stay one physical
// agent. One physical identity per pane is kept, with no fallback to
// caller or name history. The first-observed project and session order
// is preserved for the unambiguous agents.
func (c *collector) observe(terms []transport.Terminal) {
	tuples := map[string]map[paneTuple]int{}
	for _, term := range terms {
		if term.Target.Machine != "" {
			continue // an explicit remote target, never this machine
		}
		if term.PaneID == "" {
			continue // no physical pane identity to aggregate
		}
		tup := paneTuple{
			name:    term.Name,
			kind:    kinds.KindFromProgramHints(term.ProgramHints),
			session: term.WorkspaceID,
			root:    c.resolveRoot(term.Cwd),
		}
		if tuples[term.PaneID] == nil {
			tuples[term.PaneID] = map[paneTuple]int{}
		}
		tuples[term.PaneID][tup]++
	}
	for _, term := range terms {
		if term.Target.Machine != "" || term.PaneID == "" {
			continue
		}
		if _, done := c.terms[term.PaneID]; done {
			continue // an identical duplicate row of the same physical pane
		}
		seen := tuples[term.PaneID]
		if len(seen) > 1 {
			continue // conflicting observations: the physical candidate is unverifiable
		}
		var tup paneTuple
		for tup = range seen {
		}
		if term.Name == "" || term.Cwd == "" || term.WorkspaceID == "" {
			continue // an unregistered terminal or shell: nothing to bind
		}
		if tup.kind == "" || tup.root == "" {
			continue // a missing known kind or an unresolvable root: no binding, no fallback
		}
		c.terms[term.PaneID] = liveTerminal{term: term, kind: tup.kind, root: tup.root}
		if _, seen := c.sessionSeen[tup.root+"\x00"+term.WorkspaceID]; !seen {
			c.sessionSeen[tup.root+"\x00"+term.WorkspaceID] = true
			if _, hasRoot := c.sessions[tup.root]; !hasRoot {
				c.projectOrder = append(c.projectOrder, tup.root)
			}
			c.sessions[tup.root] = append(c.sessions[tup.root], term.WorkspaceID)
		}
	}
}

// resolveRoot resolves one observed cwd to its strict canonical
// common-Git root, caching successful observations per invocation; a
// failure is "", never a cwd or current-repo fallback.
func (c *collector) resolveRoot(cwd string) string {
	if !filepath.IsAbs(cwd) {
		return "" // an empty or relative observation must never resolve from the caller
	}
	if root, ok := c.roots[cwd]; ok {
		return root
	}
	resolved, err := platform.StateProjectRootWithGit(cwd, gitRunner(c.env, cwd))
	if err != nil {
		return ""
	}
	c.roots[cwd] = resolved
	return resolved
}

// gitRunner is the per-call reference Git runner of the shared strict
// root entry: one observed cwd, the reference runner, and the failure
// mapping — NotFound, Error, TimedOut, nil or nonzero status all map to
// a failed query; success maps to stdout and zero. It touches no global
// root cache or fallback.
func gitRunner(env platform.Env, cwd string) func(args ...string) (string, int) {
	return func(args ...string) (string, int) {
		res := dispatch.ReferenceGit(env, append([]string{"-C", cwd}, args...)...)
		if res.NotFound || res.Error != "" || res.TimedOut || res.Status == nil || *res.Status != 0 {
			return "", 1
		}
		return res.Stdout, 0
	}
}

// assemble resolves each observed project's privacy mode and its
// sessions' agents, in first-observed order, and cuts the shown
// candidates to the display bound and the reader's wire budget: an
// in-order prefix of complete agents, at most the first 500, stopping
// at the first candidate whose encoded cost cannot fit.
func (c *collector) assemble() (Document, error) {
	doc := Document{
		Schema:      Schema,
		SohoVersion: c.req.Version,
		GeneratedMS: c.req.GeneratedMS,
		Projects:    []Project{},
	}
	var built []builtProject
	for _, root := range c.projectOrder {
		// The privacy order: the project loads with the EMPTY session
		// first. Off stops before any session.conf, roster, private
		// record, status or screen read of the project.
		proj, err := core.LoadReadOnlyConfig(c.env, root, "", c.req.User)
		if err != nil {
			continue // a config read error fails closed
		}
		mode := ResolveMode(&proj.Config, c.env)
		if mode == ModeOff {
			continue
		}
		bp := builtProject{name: projectName(&proj.Config, root)}
		for _, session := range c.sessions[root] {
			// Only an opted-in project loads each observed session once;
			// the session layer may only lower the mode.
			sess, err := core.LoadReadOnlyConfig(c.env, root, session, c.req.User)
			if err != nil {
				continue
			}
			smode := ResolveMode(&sess.Config, c.env)
			if smode == ModeOff {
				continue
			}
			// The final effective state root opens through the safe
			// session source; an unsafe or ambiguous session skips,
			// never falling back to path readers.
			source, err := core.OpenSessionSource(sess.StateRoot, session, c.req.ScratchBase)
			if err != nil {
				continue
			}
			agents := c.sessionAgents(root, session, &sess, source)
			_ = source.Close()
			if smode == ModeHidden {
				// Hidden agents contribute exactly one of the five
				// counts; they never consume the shown bound.
				for _, a := range agents {
					doc.Hidden.add(a.State)
				}
				continue
			}
			if len(agents) == 0 {
				continue // an empty session is omitted
			}
			bp.sessions = append(bp.sessions, builtSession{id: session, agents: agents})
		}
		if len(bp.sessions) > 0 {
			built = append(built, bp) // a project without a live agent is left out
		}
	}
	// The display and wire bound: a PREFIX of complete agents in
	// project/session/roster order. The accounting uses the exact
	// escaped length of the incremental parts encoding/json produces —
	// a new project is encoded with its first session and agent, a new
	// session with its first agent plus its comma, another agent with
	// its own encoding plus its comma — so the whole multi-megabyte
	// document is never marshaled per candidate and an oversized final
	// JSON is never materialized. Hidden counts already include every
	// eligible hidden agent and never consume the bound; no allowed
	// field content is shortened or dropped and no candidate after the
	// first non-fitting candidate is kept.
	budget := MaxDocumentBytes - 1 // the CLI's final newline
	base := Document{
		Schema:      Schema,
		SohoVersion: c.req.Version,
		GeneratedMS: c.req.GeneratedMS,
		Projects:    []Project{},
		Hidden:      doc.Hidden,
		Truncated:   true, // reserve the larger truncation form
	}
	baseJSON, err := json.Marshal(base)
	if err != nil {
		return Document{}, ErrDocumentTooLarge
	}
	// head is the prefix through the opening bracket of "projects":[
	// and tail the suffix from the closing bracket, so the final
	// encoding is exactly head + the kept project encodings + tail.
	marker := `"projects":[]`
	at := bytes.Index(baseJSON, []byte(marker))
	if at < 0 || len(baseJSON) > budget {
		// The required base fields alone cannot fit the wire budget:
		// a stable refusal, never a successful invalid document.
		return Document{}, ErrDocumentTooLarge
	}
	head := baseJSON[:at+len(`"projects":[`)]
	tail := baseJSON[at+len(marker)-1:]
	remaining := budget - len(head) - len(tail)

	total := 0
	for _, p := range built {
		for _, s := range p.sessions {
			total += len(s.agents)
		}
	}
	projects := make([]Project, 0, len(built))
	kept := 0
outer:
	for pi, p := range built {
		for _, s := range p.sessions {
			sessionStarted := false
			for _, a := range s.agents {
				newProject := len(projects) == pi
				newSession := !sessionStarted
				var cost int
				switch {
				case newProject:
					part, merr := json.Marshal(Project{Name: p.name, Sessions: []Session{{ID: s.id, Agents: []Agent{a}}}})
					if merr != nil {
						return Document{}, ErrDocumentTooLarge
					}
					cost = len(part)
					if len(projects) > 0 {
						cost++ // the comma before a further project
					}
				case newSession:
					part, merr := json.Marshal(Session{ID: s.id, Agents: []Agent{a}})
					if merr != nil {
						return Document{}, ErrDocumentTooLarge
					}
					cost = len(part) + 1 // the comma before a further session
				default:
					part, merr := json.Marshal(a)
					if merr != nil {
						return Document{}, ErrDocumentTooLarge
					}
					cost = len(part) + 1 // the comma before a further agent
				}
				if cost > remaining {
					break outer // the first candidate that cannot fit
				}
				remaining -= cost
				kept++
				if newProject {
					projects = append(projects, Project{Name: p.name})
				}
				pr := projects[pi]
				if newSession {
					pr.Sessions = append(pr.Sessions, Session{ID: s.id})
					projects[pi] = pr
					sessionStarted = true
				}
				pr = projects[pi]
				pos := len(pr.Sessions) - 1
				pr.Sessions[pos].Agents = append(pr.Sessions[pos].Agents, a)
				projects[pi] = pr
				if kept >= shownAgentLimit {
					break outer
				}
			}
		}
	}
	doc.Projects = projects
	doc.Truncated = kept < total
	// The defensive wire check: the accounting above makes this pass,
	// and a failure is the stable refusal, never an oversized success.
	finalJSON, err := json.Marshal(doc)
	if err != nil || len(finalJSON)+1 > MaxDocumentBytes {
		return Document{}, ErrDocumentTooLarge
	}
	return doc, nil
}

// sessionAgents builds the agents of one opted-in session: the
// registered orchestrator first (when its record is verified live and
// not superseded by a roster row on its terminal), then the roster
// workers in roster order — only the snapshot's last row for each name,
// each matched exactly against a live observed terminal.
func (c *collector) sessionAgents(root, session string, sess *core.ReadOnlyConfig, source *core.SessionSource) []Agent {
	snap := source.Roster()
	var agents []Agent
	for _, row := range snap.Rows {
		if len(row.Fields) == 0 {
			continue
		}
		// Only the snapshot's last row for the name: an earlier valid
		// duplicate is never resurrected after invalid last metadata.
		last, ok := snap.Row(row.Fields[rosterColName])
		if !ok || !reflect.DeepEqual(last, row) {
			continue
		}
		if !row.Valid {
			continue
		}
		f := row.Fields
		name, pane, kind, role := f[rosterColName], f[rosterColPane], f[rosterColKind], f[rosterColRole]
		// The required identity columns carry no control characters.
		if !cleanIdentity(name) || !cleanIdentity(pane) || !cleanIdentity(role) || !cleanIdentity(kind) {
			continue
		}
		// A recognized catalog kind, never a guess from the name.
		if !slices.Contains(kinds.KnownKinds, kind) {
			continue
		}
		// The required started stamp: the writer's column, parsed in the
		// explicit host location; no now or mtime fallback.
		if len(f) <= rosterColStarted {
			continue
		}
		started, ok := ParseStartedMS(f[rosterColStarted], time.Local)
		if !ok {
			continue
		}
		// The exact live binding: pane, name, kind, canonical root and
		// session of one observed local terminal. No historical or
		// name-reuse inference.
		lt, live := c.terms[pane]
		if !live || lt.term.Name != name || lt.kind != kind || lt.root != root || lt.term.WorkspaceID != session {
			continue
		}
		a := Agent{
			Terminal:  pane,
			Name:      name,
			Role:      role,
			Kind:      kind,
			Model:     optionalField(f, rosterColModel, OptionalModel),
			Effort:    optionalField(f, rosterColEffort, OptionalEffort),
			StartedMS: started,
		}
		// The task title is the bounded current task title metadata the
		// dispatch task record maintains, worker-decorated; missing or
		// bad metadata gives null.
		if raw, err := source.ReadMetadata("task-" + name); err == nil {
			a.Task = TaskTitle(raw, true)
		}
		view := wait.InspectAgentStatus(wait.AgentStatusRequest{
			StateDir:  sess.StateDir,
			SessionID: session,
			Agent:     name,
			Cwd:       lt.term.Cwd,
			Config:    &sess.Config,
			Env:       c.env,
			Services:  c.svc,
			Source:    source,
		})
		state, reason, include := FoldStatus(view.State, view.TerminalState)
		if !include {
			continue // a freshly gone terminal is omitted, even with a report
		}
		a.State = state
		a.Reason = reason
		// The orchestrator roles never claim report-ready: a ready nested
		// orchestrator would make the whole document fail strict parsing.
		// Ordinary worker readiness stays the shared resolver's own
		// observation.
		if role == "orchestrator" || role == "sub-orchestrator" {
			a.ReportReady = false
		} else {
			a.ReportReady = view.ReportReady
		}
		agents = append(agents, a)
	}
	if orch := c.orchestrator(root, session, sess, snap); orch != nil {
		agents = append([]Agent{*orch}, agents...)
	}
	return agents
}

// orchestrator verifies the registered orchestrator record against a
// live observed terminal and returns its agent, or nil: a malformed or
// absent record never invents one, and a roster row on the record's
// terminal supersedes the stale record. Its state is the terminal-only
// inspection, never a fabricated worker row; its model and effort are
// null and its report-ready is false.
func (c *collector) orchestrator(root, session string, sess *core.ReadOnlyConfig, snap core.RosterSnapshot) *Agent {
	rec, found, err := sessionmeta.Store{StateBase: sess.StateRoot, SessionID: session}.Read()
	if err != nil || !found {
		return nil
	}
	if rec.ProjectRoot != root || rec.SessionID != session || rec.StartedMS < 0 {
		return nil
	}
	if !cleanIdentity(rec.Name) || !cleanIdentity(rec.Terminal) || !cleanIdentity(rec.Kind) {
		return nil
	}
	if !slices.Contains(kinds.KnownKinds, rec.Kind) {
		return nil
	}
	lt, live := c.terms[rec.Terminal]
	if !live || lt.term.Name != rec.Name || lt.kind != rec.Kind || lt.root != root || lt.term.WorkspaceID != session {
		return nil
	}
	for _, row := range snap.Rows {
		if len(row.Fields) > rosterColPane && row.Fields[rosterColPane] == rec.Terminal {
			// A roster worker or sub-orchestrator holds the terminal:
			// the stale orchestrator record is suppressed.
			return nil
		}
	}
	view := wait.InspectTerminalStatus(wait.TerminalStatusRequest{
		Terminal: rec.Name,
		Kind:     rec.Kind,
		Config:   &sess.Config,
		Env:      c.env,
		Services: c.svc,
	})
	state, reason, include := FoldStatus(view.State, view.TerminalState)
	if !include {
		return nil
	}
	return &Agent{
		Terminal:    rec.Terminal,
		Name:        rec.Name,
		Role:        "orchestrator",
		Kind:        rec.Kind,
		State:       state,
		Reason:      reason,
		Task:        TaskTitle(rec.Task, false),
		ReportReady: false,
		StartedMS:   rec.StartedMS,
	}
}

// projectName is the project's display name: the valid nonempty
// no-control inventory_name of the project layers (the later valid value
// replacing the earlier, like the mode), never a merged user or session
// label, a path or a remote lookup; the canonical basename otherwise.
func projectName(cfg *core.Config, root string) string {
	name := ""
	for _, layer := range cfg.Layers {
		if layer.Source != "project" || layer.ReadError != nil {
			continue
		}
		value, ok := layer.Entries["inventory_name"]
		if !ok {
			continue
		}
		if cleanIdentity(value.Value) {
			name = value.Value
		}
	}
	if name == "" {
		return filepath.Base(root)
	}
	return name
}

// cleanIdentity reports whether one persisted identity column is usable:
// nonempty, valid UTF-8 and free of control characters.
func cleanIdentity(value string) bool {
	if value == "" || !utf8.ValidString(value) {
		return false
	}
	return !strings.ContainsFunc(value, unicode.IsControl)
}

func optionalField(f []string, index int, pick func(string) *string) *string {
	if len(f) > index {
		return pick(f[index])
	}
	return nil
}

// add counts one hidden agent into its folded state.
func (c *Counts) add(state State) {
	switch state {
	case StateWorking:
		c.Working++
	case StateNeedsYou:
		c.NeedsYou++
	case StateDone:
		c.Done++
	case StateIdle:
		c.Idle++
	default:
		c.Unknown++
	}
}
