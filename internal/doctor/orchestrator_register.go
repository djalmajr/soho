package doctor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/djalmajr/soho/internal/backend"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/dispatch"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/kinds"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/sessionmeta"
	"github.com/djalmajr/soho/internal/transport"
)

// orchestratorTitlePrefix is the UI prefix the title command and the init's
// fresh title put in front of the objective; the private record stores the
// objective without it.
const orchestratorTitlePrefix = "orchestrator: "

// gitRunnerFor is the per-call Git runner of the shared strict root entry:
// one observed cwd, the reference Git runner, and the failure mapping —
// NotFound, Error, TimedOut, nil or nonzero status all map to a failed
// query; success maps to stdout and zero. It copies no Git cleaning and
// touches no global root cache or fallback.
func gitRunnerFor(env platform.Env, cwd string) func(args ...string) (string, int) {
	return func(args ...string) (string, int) {
		res := dispatch.ReferenceGit(env, append([]string{"-C", cwd}, args...)...)
		if res.NotFound || res.Error != "" || res.TimedOut || res.Status == nil || *res.Status != 0 {
			return "", 1
		}
		return res.Stdout, 0
	}
}

// strictProjectRoot resolves the canonical Git-common project root of cwd
// through the shared strict entry, with the reference Git runner.
func StrictProjectRoot(env platform.Env, cwd string) (string, error) {
	return platform.StateProjectRootWithGit(cwd, gitRunnerFor(env, cwd))
}

// rosterWorker reports whether the terminal (pane) holds a worker row of
// the session's roster, with the error-aware distinction the normal reader
// does not keep: a missing base, session or roster is NOT a worker with a
// nil error, while an unsafe, unreadable or corrupt roster is an error,
// never a failed read mistaken for permission. Membership is the actual
// roster row of the pane (the normal reader's columns), never a name
// prefix.
func rosterWorker(stateBase, sessionID, pane string) (bool, error) {
	if pane == "" {
		return false, nil
	}
	if st, err := os.Stat(stateBase); err == nil && !st.IsDir() {
		return false, fmt.Errorf("the state base %s is not a directory", stateBase)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	} else if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	source, err := core.OpenSessionSource(stateBase, sessionID, "")
	if err != nil {
		return false, err
	}
	defer func() { _ = source.Close() }()
	for _, row := range source.Roster().Rows {
		// A pane remains a worker even when later metadata is missing.
		if len(row.Fields) > 1 && row.Fields[1] == pane {
			return true, nil
		}
		if !row.Valid {
			return false, errors.New("the session roster has a malformed row; caller membership cannot be resolved")
		}
	}
	return false, nil
}

// matchedCallerTerminal selects, from one successful typed All=true
// listing, the actual caller terminal: the row whose terminal id, opaque
// workspace and canonical cwd root all match. ok is false with the reason
// when the listing failed, no row matches, the terminal's cwd root does
// not resolve to the same root, or the row reports no cwd.
func matchedCallerTerminal(rep transport.Reply[[]transport.Terminal], env platform.Env, pane, session, root string) (*transport.Terminal, string) {
	if rep.Err != nil || !rep.Valid {
		return nil, "the live terminal listing failed"
	}
	for i := range rep.Value {
		t := &rep.Value[i]
		if t.PaneID != pane || t.WorkspaceID != session {
			continue
		}
		if t.Cwd == "" {
			return nil, "the caller terminal reports no cwd"
		}
		termRoot, err := StrictProjectRoot(env, t.Cwd)
		if err != nil || termRoot != root {
			return nil, "the caller terminal's root does not match the project root"
		}
		return t, ""
	}
	return nil, "the caller terminal was not observed live in the session"
}

// registerInitRecord is the auxiliary private orchestrator registration of
// an explicit Herdr init (DJA-611 D4): it validates the caller's live
// terminal identity (one All=true observation, opaque session, canonical
// root, catalog kind) and registers the session's private record. It never
// changes init's success or output: every refusal is one explicit
// warning, a roster worker or sub-orchestrator never creates or replaces
// the orchestrator record (actual roster membership, never a name
// prefix), and an unreadable roster fails the registration rather than
// granting permission.
func registerInitRecord(ctx *core.Config, env platform.Env, cwd string, svc transport.Services, pane, name, title, session, friction string) {
	if !sessionmeta.ValidSessionID(session) {
		warnInitRecord(friction, "the session is not a safe state component; no orchestrator record was registered")
		return
	}
	root, err := StrictProjectRoot(env, cwd)
	if err != nil {
		warnInitRecord(friction, "the canonical project root could not be resolved: "+err.Error()+"; no orchestrator record was registered")
		return
	}
	stateBase := core.StateRootPathForProject(ctx, env, root)
	inRoster, err := rosterWorker(stateBase, session, pane)
	if err != nil {
		warnInitRecord(friction, "the session roster is unreadable: "+err.Error()+"; no orchestrator record was registered")
		return
	}
	if inRoster {
		// Roster membership wins over any orchestrator-like name: a worker
		// (or sub-orchestrator) another orchestrator opened never touches
		// the session's orchestrator record.
		return
	}
	if pane == "" || name == "" {
		warnInitRecord(friction, "the caller terminal is not live; no orchestrator record was registered")
		return
	}
	rep := svc.Runtime.List(context.Background(), transport.Scope{All: true}, transport.Call{})
	matched, reason := matchedCallerTerminal(rep, env, pane, session, root)
	if matched == nil {
		warnInitRecord(friction, reason+"; no orchestrator record was registered")
		return
	}
	kind := kinds.KindFromProgramHints(matched.ProgramHints)
	if kind == "" {
		warnInitRecord(friction, "the caller terminal names no known kind; no orchestrator record was registered")
		return
	}
	// Seed the task from the preserved objective (the final title without
	// its UI prefix), or the default project objective when the title
	// carries none; a same-terminal re-init preserves the existing task,
	// including an explicit clear.
	task := strings.TrimPrefix(title, orchestratorTitlePrefix)
	if task == "" {
		task = filepath.Base(root)
	}
	store := sessionmeta.Store{StateBase: stateBase, SessionID: session}
	if _, err := store.Register(sessionmeta.Record{
		Schema:      sessionmeta.Schema,
		ProjectRoot: root,
		SessionID:   session,
		Terminal:    pane,
		Name:        name,
		Kind:        kind,
		StartedMS:   platform.Now().UTC().UnixMilli(),
		Task:        task,
	}); err != nil {
		warnInitRecord(friction, "the orchestrator record could not be registered: "+err.Error())
		return
	}
}

func warnInitRecord(friction, reason string) {
	core.Warn("init: "+reason, friction, "init")
}

// SetTaskAfterTitle updates the session's private orchestrator record
// after a successful Herdr UI title change (DJA-611 D4): SetTask only for
// the matching canonical project root, observed session and current
// terminal. A no-record, missing-base, nonmatching or unresolvable-root
// title changes nothing and returns nil (the normal UI behavior); a
// matching record's failure returns the explicit error.
func SetTaskAfterTitle(cfg *core.Config, env platform.Env, cwd, pane, objective string) error {
	if pane == "" {
		return nil
	}
	session := core.WorkspaceID(cfg, env, cwd)
	if !sessionmeta.ValidSessionID(session) {
		return nil
	}
	root, err := StrictProjectRoot(env, cwd)
	if err != nil {
		return nil
	}
	store := sessionmeta.Store{StateBase: core.StateRootPathForProject(cfg, env, root), SessionID: session}
	if _, err := store.SetTask(root, pane, objective); err != nil {
		return fmt.Errorf("the orchestrator record could not be updated: %w", err)
	}
	return nil
}

// cmdInitHc is the explicit HC branch of an explicit init (DJA-611 D4): it
// validates the real HC terminal, session, canonical root and roster
// before any state write, safely initializes the minimum session state,
// and registers the caller's actual name and catalog kind with the
// default project objective. It never enters the Herdr naming/title/
// doctor/provider flow, never renames or titles anything, and fails
// explicitly on a validation or registration failure.
func cmdInitHc(ctx *core.Config, env platform.Env, cwd string) {
	svc := backend.New(env)
	caller := svc.Runtime.Caller()
	if !caller.Inside || caller.PaneID == "" {
		platform.Die("transport=hc: not inside an hc terminal (HC_TERMINAL_ID is not set); run soho from a terminal started with hc", 2)
	}
	session := caller.WorkspaceID
	if session == "" {
		platform.Die("transport=hc: the caller terminal's soho session could not be read (hc get failed for HC_TERMINAL_ID); check that hc is reachable", 2)
	}
	if !sessionmeta.ValidSessionID(session) {
		platform.Die("init: the session is not a safe state component", 2)
	}
	root, err := StrictProjectRoot(env, cwd)
	if err != nil {
		platform.Die("init: the canonical project root could not be resolved: "+err.Error(), 2)
	}
	stateBase := core.StateRootPathForProject(ctx, env, root)
	inRoster, err := rosterWorker(stateBase, session, caller.PaneID)
	if err != nil {
		platform.Die("init: the session roster is unreadable: "+err.Error(), 2)
	}
	if inRoster {
		platform.Die("init: this terminal holds a worker row of the session roster; a roster worker does not register the orchestrator record", 2)
	}
	rep := svc.Runtime.List(context.Background(), transport.Scope{All: true}, transport.Call{})
	matched, reason := matchedCallerTerminal(rep, env, caller.PaneID, session, root)
	if matched == nil {
		platform.Die("init: "+reason, 2)
	}
	kind := kinds.KindFromProgramHints(matched.ProgramHints)
	if kind == "" {
		platform.Die("init: the caller terminal names no known kind", 2)
	}
	nameReply := svc.Terminal.CallerAgentName(context.Background(), transport.Call{})
	if nameReply.Err != nil {
		platform.Die("init: the caller terminal's name could not be read: "+nameReply.Err.Error(), 2)
	}
	record := sessionmeta.Record{
		Schema:      sessionmeta.Schema,
		ProjectRoot: root,
		SessionID:   session,
		Terminal:    caller.PaneID,
		Name:        nameReply.Value,
		Kind:        kind,
		StartedMS:   platform.Now().UTC().UnixMilli(),
		Task:        filepath.Base(root),
	}
	if err := sessionmeta.ValidateRecord(record); err != nil {
		platform.Die("init: the orchestrator record could not be registered: "+err.Error(), 2)
	}
	if err := core.EnsureSessionStateDirectory(stateBase, session); err != nil {
		platform.Die("init: the session state could not be initialized: "+err.Error(), 2)
	}
	store := sessionmeta.Store{StateBase: stateBase, SessionID: session}
	if _, err := store.Register(record); err != nil {
		platform.Die("init: the orchestrator record could not be registered: "+err.Error(), 2)
	}
	obj := jsonjs.O(
		"orchestrator", nameReply.Value,
		"pane_id", caller.PaneID,
		"tab_id", caller.TabID,
		"workspace_id", session,
		"layout", core.Cfg(ctx, "layout", "split", env),
		"state_dir", filepath.Join(stateBase, session),
		"first_run", ProjectIsFirstRun(ctx, env, cwd),
		"title", "",
	)
	_, _ = fmt.Fprintln(platform.Stdout, jsonjs.StringifyIndent(obj, 2))
}
