package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/djalmajr/soho/internal/backend"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/doctor"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/sessionmeta"
	"github.com/djalmajr/soho/internal/transport"
)

func normalizeObjective(value string) string {
	value = strings.NewReplacer("\r", " ", "\t", " ", "\n", " ").Replace(value)
	value = strings.TrimFunc(value, isJSTrimSpace)
	runes := []rune(value)
	if len(runes) > 60 {
		value = string(runes[:60])
	}
	return value
}

func isJSTrimSpace(r rune) bool {
	return r == '\u0009' || r == '\u000a' || r == '\u000b' || r == '\u000c' || r == '\u000d' || r == '\u0020' || r == '\u00a0' || r == '\u1680' || (r >= '\u2000' && r <= '\u200a') || r == '\u2028' || r == '\u2029' || r == '\u202f' || r == '\u205f' || r == '\u3000' || r == '\ufeff'
}

func cmdTitle(argv []string, cfg *core.Config, env platform.Env, cwd string) {
	clear := false
	rest := []string{}
	for _, arg := range argv {
		if arg == "--clear" {
			clear = true
		} else if strings.HasPrefix(arg, "--") {
			core.DieFriction("title: unknown option "+arg, 2, frictionLogPath, "title")
		} else {
			rest = append(rest, arg)
		}
	}
	objective := ""
	if clear {
		if len(rest) > 0 {
			core.DieFriction("title: --clear takes no objective", 2, frictionLogPath, "title")
		}
	} else {
		objective = normalizeObjective(strings.Join(rest, " "))
		if objective == "" {
			core.DieFriction("title: give the current objective, or --clear", 2, frictionLogPath, "title")
		}
	}
	// The explicit HC branch updates only the matching initialized record;
	// it never calls the unsupported HC window title (DJA-611 D4).
	if env.Get("SOHO_TRANSPORT") == "hc" {
		cmdTitleHc(cfg, env, cwd, objective)
		return
	}
	svc := backend.New(env)
	pane := svc.Runtime.Caller().PaneID
	var title *string
	if !clear {
		value := "orchestrator: " + objective
		title = &value
	}
	// The caller-pane check runs after the arguments are valid and before
	// the first Herdr call that acts on the caller's pane (DJA-394): a
	// usage error never touches Herdr, and a forked session must not
	// write the title of the pane its environment claims.
	core.CallerGate("title", cfg, env, cwd)
	if !svc.Window.Title(context.Background(), pane, title, transport.Call{}).Value {
		core.DieFriction("title: herdr pane report-metadata failed", 4, frictionLogPath, "title")
	}
	// After the UI change, update the matching private orchestrator record
	// (DJA-611 D4): a no-record or nonmatching title keeps the normal UI
	// behavior, and a matching record's failure is explicit even though
	// the UI already changed.
	if err := doctor.SetTaskAfterTitle(cfg, env, cwd, pane, objective); err != nil {
		core.DieFriction("title: "+err.Error(), 4, frictionLogPath, "title")
	}
	value := ""
	if title != nil {
		value = *title
	}
	out := jsonjs.StringifyIndent(jsonjs.O("pane_id", pane, "title", value), 2)
	_, _ = fmt.Fprintln(platform.Stdout, out)
}

// cmdTitleHc updates the objective of the session's already matching
// initialized orchestrator record (DJA-611 D4): it requires an
// initialized record whose canonical root, session and terminal match the
// caller, and updates the task only — an absent, nonmatching, unsafe or
// corrupt record is an explicit failure without a false success, without
// creating state, and without any unsupported HC window title call.
func cmdTitleHc(cfg *core.Config, env platform.Env, cwd, objective string) {
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
		platform.Die("title: the session is not a safe state component", 2)
	}
	root, err := doctor.StrictProjectRoot(env, cwd)
	if err != nil {
		platform.Die("title: the canonical project root could not be resolved: "+err.Error(), 2)
	}
	store := sessionmeta.Store{StateBase: core.StateRootPathForProject(cfg, env, root), SessionID: session}
	record, present, err := store.Read()
	if err != nil {
		platform.Die("title: the orchestrator record is unreadable: "+err.Error(), 4)
	}
	if !present {
		platform.Die("title: no initialized orchestrator record for this session; run 'soho init' first", 4)
	}
	if record.ProjectRoot != root || record.Terminal != caller.PaneID {
		platform.Die("title: the orchestrator record does not match this session's current terminal", 4)
	}
	if _, err := store.SetTask(root, caller.PaneID, objective); err != nil {
		platform.Die("title: the orchestrator record could not be updated: "+err.Error(), 4)
	}
	out := jsonjs.StringifyIndent(jsonjs.O("pane_id", caller.PaneID, "title", objective), 2)
	_, _ = fmt.Fprintln(platform.Stdout, out)
}
