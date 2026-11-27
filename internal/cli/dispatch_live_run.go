package cli

// SOH-490: an edit brief sent while a long run of the caller is live in the
// worker's worktree changes what that run tests. Dispatch only warns: the
// advisory runs before the delivery flow decides whether to send the brief.

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/djalmajr/soho/internal/backend"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/dispatch"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/transport"
)

// dispatchLiveRunTransportTimeout is the advisory lookup's own bound. It is not
// part of the send's delivery window and cannot shorten a dispatch budget.
const dispatchLiveRunTransportTimeout = 2 * time.Second

const dispatchLiveRunNameLimit = 120

// dispatchLiveRunPrintable keeps a terminal name or address as data on one
// line: control characters become spaces, every other rune (including Unicode
// and repeated spaces) survives, and a value over 120 runes is cut there with
// an ellipsis.
func dispatchLiveRunPrintable(value string) string {
	runes := []rune(value)
	if len(runes) > dispatchLiveRunNameLimit {
		runes = append(runes[:dispatchLiveRunNameLimit:dispatchLiveRunNameLimit], '…')
	}
	for i, r := range runes {
		if unicode.IsControl(r) {
			runes[i] = ' '
		}
	}
	return string(runes)
}

// dispatchLiveRunPreflight reads its own advisory inputs before dispatch
// starts its delivery budgets or takes the worker lifecycle lock.
func dispatchLiveRunPreflight(agent, briefFile string, cfg *core.Config, env platform.Env, cwd string) {
	caller := env.Get("HC_TERMINAL_ID")
	if env.Get("SOHO_TRANSPORT") != "hc" || caller == "" {
		return
	}
	svc := newDispatchServices(env)
	call := transport.Call{Timeout: dispatchLiveRunTransportTimeout}
	reply := svc.Runtime.Get(context.Background(), transport.Target{ID: caller}, call)
	if reply.Err != nil || !reply.Valid || reply.Value.WorkspaceID == "" {
		return
	}
	source, err := core.OpenSessionSource(core.StateRootPath(cfg, env, cwd), reply.Value.WorkspaceID, "")
	if err != nil {
		return
	}
	roster := source.Roster()
	if err := source.Close(); err != nil {
		return
	}
	row, ok := roster.Row(agent)
	if !ok || !row.Valid || !core.RoleIsEdit(row.Fields[3], env, cwd) {
		return
	}
	var workerCwd string
	if len(row.Fields) > 6 {
		workerCwd = row.Fields[6]
	}
	briefBody, _ := platform.ReadRegularTextFile(briefFile)
	warnDispatchLiveRun(svc, briefBody, roster.Rows, workerCwd, caller)
}

// warnDispatchLiveRun lists native terminals once and warns per live child
// of this caller working in the target worker's roster cwd or the
// brief's first declared Worktree. Identity comes from Core's parent field,
// never from a terminal name; roster terminals and the caller's wake
// observers are not runs of the caller's own that an edit could invalidate.
// A failed, invalid or timed-out listing produces no warning.
func warnDispatchLiveRun(svc transport.Services, briefBody string, rows []core.RosterRow, workerCwd, caller string) {
	roster := make(map[string]bool)
	for _, row := range rows {
		fields := row.Fields
		if len(fields) > 1 && fields[1] != "" {
			roster[fields[1]] = true
		}
	}
	trees := make([]string, 0, 2)
	if workerCwd != "" {
		trees = append(trees, workerCwd)
	}
	if declared := dispatch.DeclaredWorktree(briefBody); declared != "" {
		trees = append(trees, declared)
	}
	if len(trees) == 0 {
		return
	}
	reply := svc.Runtime.List(context.Background(), transport.Scope{All: true}, transport.Call{Timeout: dispatchLiveRunTransportTimeout})
	if reply.Err != nil || !reply.Valid {
		return
	}
	for _, terminal := range reply.Value {
		if terminal.NativeState != "live" || !terminal.ParentKnown || terminal.Parent != caller {
			continue
		}
		if roster[terminal.Target.ID] || strings.HasPrefix(terminal.Name, "wake-") {
			continue
		}
		cwd := terminal.CurrentCwd
		if cwd == "" {
			cwd = terminal.Cwd
		}
		if cwd == "" {
			continue
		}
		inTree := false
		for _, tree := range trees {
			if dispatch.PathWithin(cwd, tree, platform.Current()) {
				inTree = true
				break
			}
		}
		if !inTree {
			continue
		}
		core.Warn(fmt.Sprintf("dispatch: a run of yours is live in this worktree (%s, %s); edits now change what it tests — wait for its exit note, or run long checks in a worktree pinned to the commit under test", dispatchLiveRunPrintable(terminal.Name), dispatchLiveRunPrintable(terminal.Target.ID)), frictionLogPath, "dispatch")
	}
}

// Tests can script advisory and delivery launches on the same clock.
var newDispatchServices = backend.New
