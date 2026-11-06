package inventory

import (
	"context"
	"path/filepath"
	"slices"
	"strings"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/kinds"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/sessionmeta"
	"github.com/djalmajr/soho/internal/transport"
)

// MissingRegistrationAdvisory is heuristic advice only. It carries no
// private identity and grants no caller, dispatch or registration authority.
const MissingRegistrationAdvisory = "an apparent orchestrator is not registered; run soho init in its pane"

// registrationObservation separates an absent or unbound record from a
// failed read. Both rendering and advice consume the same single read and
// exact live binding; an acquisition error can never authorize advice.
type registrationObservation struct {
	record sessionmeta.Record
	bound  bool
	err    error
}

func (c *collector) readRegistration(root, session string, sess *core.ReadOnlyConfig) registrationObservation {
	rec, found, err := (sessionmeta.Store{StateBase: sess.StateRoot, SessionID: session}).Read()
	observation := registrationObservation{record: rec, err: err}
	if err != nil || !found {
		return observation
	}
	if rec.ProjectRoot != root || rec.SessionID != session || rec.StartedMS < 0 {
		return observation
	}
	if !cleanIdentity(rec.Name) || !cleanIdentity(rec.Terminal) || !cleanIdentity(rec.Kind) || !slices.Contains(kinds.KnownKinds, rec.Kind) {
		return observation
	}
	lt, live := c.terms[rec.Terminal]
	observation.bound = live && lt.term.Name == rec.Name && lt.kind == rec.Kind && lt.root == root && lt.term.WorkspaceID == session
	return observation
}

func rosterClaimsPane(snap core.RosterSnapshot, pane string) bool {
	for _, row := range snap.Rows {
		if len(row.Fields) > rosterColPane && row.Fields[rosterColPane] == pane {
			return true
		}
	}
	return false
}

// missingRegistration is shared by both consumers, after their project
// and session privacy gates and confined source acquisition. It uses the
// existing unambiguous local binding and only a fresh state Get for an
// unregistered candidate: never a screen read or a repeated listing.
func (c *collector) missingRegistration(root, session string, sess *core.ReadOnlyConfig, snap core.RosterSnapshot, registration registrationObservation) bool {
	if registration.err != nil {
		return false
	}
	want := core.Cfg(&sess.Config, "orchestrator_name", "orchestrator", c.env)
	if !cleanIdentity(want) {
		return false
	}
	for pane, lt := range c.terms {
		if lt.root != root || lt.term.WorkspaceID != session || !cleanIdentity(lt.term.Name) || !cleanIdentity(pane) {
			continue
		}
		if rosterClaimsPane(snap, pane) {
			continue
		}
		if registration.bound && registration.record.Terminal == pane {
			continue
		}
		// This is the same naming convention EnsureOrchestratorName keeps,
		// used solely as an advisory trigger after the identity exclusions.
		if lt.term.Name != want && !strings.HasPrefix(lt.term.Name, want+"-") {
			continue
		}
		fresh := c.svc.Runtime.Get(context.Background(), lt.term.Target, transport.Call{})
		if fresh.Err != nil || !fresh.Valid {
			continue
		}
		state, _, include := FoldStatus(fresh.Value.State, fresh.Value.State)
		if include && state != StateUnknown {
			return true
		}
	}
	return false
}

// CurrentMissingRegistration is the doctor's minimal read-only entry. It
// uses the existing runtime caller's session and the strict current Git
// root, without current-pane/workspace discovery. Listed cwd Git metadata
// participates in ambiguity checks; soho configuration, private session
// sources, records and status are read only for the current root/session.
// Failure or unavailable evidence suppresses this optional advice.
func CurrentMissingRegistration(env platform.Env, cwd string, user core.UserSelection, svc transport.Services) (missing bool) {
	caller := svc.Runtime.Caller()
	if !caller.Inside || !platform.ValidStateComponent(caller.WorkspaceID) || !filepath.IsAbs(cwd) {
		return false
	}
	root, err := platform.StateProjectRootWithGit(cwd, gitRunner(env, cwd))
	if err != nil {
		return false
	}
	project, err := core.LoadReadOnlyConfig(env, root, "", user)
	if err != nil || ResolveMode(&project.Config, env) == ModeOff {
		return false
	}
	session := caller.WorkspaceID
	sess, err := core.LoadReadOnlyConfig(env, root, session, user)
	if err != nil || ResolveMode(&sess.Config, env) == ModeOff {
		return false
	}
	source, err := core.OpenSessionSource(sess.StateRoot, session, "")
	if err != nil {
		return false
	}
	defer func() {
		if source.Close() != nil {
			missing = false
		}
	}()
	reply := svc.Runtime.List(context.Background(), transport.Scope{All: true}, transport.Call{})
	if reply.Err != nil || !reply.Valid {
		return false
	}
	c := collectorFromSnapshot(Request{Env: env, User: user}, env, svc, reply)
	c.observe(reply.Value)
	registration := c.readRegistration(root, session, &sess)
	return c.missingRegistration(root, session, &sess, source.Roster(), registration)
}
