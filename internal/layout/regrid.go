package layout

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
)

type gridResult struct {
	Ok bool
	// Changes lists the ids that a successful move re-idi'd: the old id and
	// the actual returned id. It is an id-change list, not a move-success
	// list: a successful move that keeps the same pane id appears only in
	// Moved.
	Changes [][2]string
	// Moved lists, in move order, the pre-move id of every successful grid
	// move, including moves that returned the same pane id.
	Moved []string
}

func ratio4(n int) string { return fmt.Sprintf("%.4f", math.Round((1/float64(n))*10000)/10000) }
func moveInTab(pane, tab, split, target, ratio string, env platform.Env) herdr.PaneMoveResult {
	return herdr.PaneMove([]string{pane, "--tab", tab, "--split", split, "--target-pane", target, "--ratio", ratio, "--no-focus"}, env)
}
func buildGrid(tab string, cells []string, env platform.Env) gridResult {
	g := GridSizes(len(cells))
	ids := append([]string(nil), cells...)
	heads := []int{}
	idx := 0
	for _, n := range g.Rows {
		heads = append(heads, idx)
		idx += n
	}
	changes := [][2]string{}
	moved := []string{}
	note := func(i int, m herdr.PaneMoveResult) {
		if m.Pane != "" && m.Pane != ids[i] {
			changes = append(changes, [2]string{ids[i], m.Pane})
			ids[i] = m.Pane
		}
	}
	prev := ids[0]
	for c := 1; c < g.Cols; c++ {
		i := heads[c]
		m := moveInTab(ids[i], tab, "right", prev, ratio4(g.Cols-c+1), env)
		if !m.Ok {
			return gridResult{Ok: false, Changes: changes, Moved: moved}
		}
		moved = append(moved, ids[i])
		note(i, m)
		prev = ids[i]
	}
	for c, n := range g.Rows {
		idx := heads[c]
		prev = ids[idx]
		for j := 1; j < n; j++ {
			idx++
			m := moveInTab(ids[idx], tab, "down", prev, ratio4(n-j+1), env)
			if !m.Ok {
				return gridResult{Ok: false, Changes: changes, Moved: moved}
			}
			moved = append(moved, ids[idx])
			note(idx, m)
			prev = ids[idx]
		}
	}
	return gridResult{Ok: true, Changes: changes, Moved: moved}
}

// gridApply runs the grid moves and, after every successful move — even when
// a later move fails — records the returned pane ids in the roster. It
// reports the successful id changes, the pre-move id of every successful
// move (including unchanged-id moves), and whether the whole grid completed.
func gridApply(ctx *core.Config, tab string, cells []string, env platform.Env, cwd string) ([][2]string, []string, bool) {
	r := buildGrid(tab, cells, env)
	dir := core.StateDirPath(ctx, env, cwd)
	for _, change := range r.Changes {
		core.RosterReplacePane(dir, change[0], change[1])
	}
	return r.Changes, r.Moved, r.Ok
}
func applyGrid(ctx *core.Config, tab string, cells []string, env platform.Env, cwd string) bool {
	_, _, ok := gridApply(ctx, tab, cells, env, cwd)
	return ok
}

// parkPanesTracked parks the panes in a temporary herd-park tab. It returns
// the actual pane ids after every successful move (a move can re-id the
// pane), the park tab, and whether all the moves succeeded. When dir is
// non-empty every successful move is recorded in the roster before the next
// one is attempted, so a later failure cannot leave stale ids in state.
func parkPanesTracked(dir string, panes []string, env platform.Env) ([]string, string, bool) {
	if len(panes) == 0 {
		return []string{}, "", false
	}
	first := panes[0]
	m := herdr.PaneMove([]string{first, "--new-tab", "--label", "herd-park", "--no-focus"}, env)
	if !m.Ok || m.Tab == "" {
		return append([]string(nil), panes...), "", false
	}
	parked := []string{first}
	if m.Pane != "" && m.Pane != first {
		if dir != "" {
			core.RosterReplacePane(dir, first, m.Pane)
		}
		parked[0] = m.Pane
	}
	for _, pane := range panes[1:] {
		r := herdr.PaneMove([]string{pane, "--tab", m.Tab, "--split", "down", "--target-pane", parked[0], "--ratio", "0.5", "--no-focus"}, env)
		if !r.Ok {
			return parked, m.Tab, false
		}
		next := pane
		if r.Pane != "" && r.Pane != pane {
			if dir != "" {
				core.RosterReplacePane(dir, pane, r.Pane)
			}
			next = r.Pane
		}
		parked = append(parked, next)
	}
	return parked, m.Tab, true
}
func parkPanes(panes []string, env platform.Env) (bool, string) {
	_, tab, ok := parkPanesTracked("", panes, env)
	return ok, tab
}

// callerContext resolves the caller's actual tab and pane and reports a
// refusal reason. A non-empty refusal refuses the WHOLE regrid before any
// pane move, pane close, tab create, or state mutation: an unresolvable
// caller identity must never fall back to the stale inherited ids, and a
// live roster can already hold the caller's actual pane in a herd tab
// that the herd-tab rebuild would otherwise relocate.
//
// Fast path (no extra reads): the inherited HERDR_PANE_ID is live in this
// workspace's pane list. Slow path: the native alias channel (herdr pane
// current --current) still resolves the old id for the moved process and
// reports the actual pane, tab, and workspace. The slow path requires a
// nonempty workspace_id matching this state scope — missing scope is
// uncertainty, not permission — and the resolved pane/tab must exist in
// the live topology; a mismatch is refused instead of guessed. On success
// the caller's stale roster row is re-identified to the actual pane id.
func callerContext(ctx *core.Config, env platform.Env, live []any, dir, ws string) (tab, pane, refuse string) {
	inherited := env.Get("HERDR_PANE_ID")
	if inherited == "" {
		return "", "", ""
	}
	for _, v := range live {
		o, isObj := v.(*jsonjs.Object)
		if !isObj {
			continue
		}
		pid, _ := o.Get("pane_id")
		if pid != inherited {
			continue
		}
		if tid, _ := o.Get("tab_id"); tid != nil {
			if t, ok := tid.(string); ok && t != "" {
				return t, inherited, ""
			}
		}
	}
	res := herdr.PaneCurrent(env)
	if !res.Ok {
		return "", "", refuseMessage("the inherited caller pane " + inherited + " is not live in workspace " + ws + " and herdr pane current --current could not resolve it")
	}
	if res.PaneID == "" || res.TabID == "" {
		return "", "", refuseMessage("the resolved caller identity is incomplete (pane " + res.PaneID + ", tab " + res.TabID + ")")
	}
	if res.WorkspaceID == "" {
		return "", "", refuseMessage("the resolved caller identity has no workspace scope (missing scope is uncertainty, not permission)")
	}
	if res.WorkspaceID != ws {
		return "", "", refuseMessage("the caller resolves to workspace " + res.WorkspaceID + ", outside this state scope " + ws)
	}
	for _, v := range live {
		o, isObj := v.(*jsonjs.Object)
		if !isObj {
			continue
		}
		pid, _ := o.Get("pane_id")
		if pid != res.PaneID {
			continue
		}
		if tid, _ := o.Get("tab_id"); tid != nil {
			if t, ok := tid.(string); ok && t == res.TabID {
				if dir != "" && inherited != res.PaneID {
					core.RosterReplacePane(dir, inherited, res.PaneID)
				}
				return res.TabID, res.PaneID, ""
			}
		}
	}
	return "", "", refuseMessage("the resolved caller pane " + res.PaneID + " is not live in tab " + res.TabID + " of workspace " + ws)
}

// refuseMessage shapes the whole-command refusal: an explicit
// identity/scope error that names what could not be verified and that the
// regrid is refused before any pane move, pane close, tab create, or
// state mutation.
func refuseMessage(what string) string {
	return "regrid: caller identity unresolved (" + what + "); refusing the whole regrid before any pane move, pane close, tab create or state change"
}

// withoutCaller drops the calling pane from a worker set: the caller is
// never a worker to relocate, but the other genuine workers of the same tab
// still count.
func withoutCaller(panes []string, caller string) []string {
	if caller == "" {
		return panes
	}
	out := make([]string, 0, len(panes))
	for _, p := range panes {
		if p != caller {
			out = append(out, p)
		}
	}
	return out
}

// whereAfterFailedGrid splits the panes of a partially failed grid into the
// ones already at the destination and the ones left at the source. The
// destination side is: the panes placed before the grid, the pre-move id of
// every successful grid move (unchanged-id moves included — r.Changes alone
// is an id-change list, not a move-success list), and re-ided ones under
// their new id.
func whereAfterFailedGrid(panes, placed []string, changes [][2]string, moved []string) (atDest, atSource []string) {
	placedSet := map[string]bool{}
	for _, p := range placed {
		placedSet[p] = true
	}
	newID := map[string]string{}
	for _, ch := range changes {
		newID[ch[0]] = ch[1]
	}
	movedSet := map[string]bool{}
	for _, m := range moved {
		movedSet[m] = true
	}
	for _, p := range panes {
		if next, ok := newID[p]; ok {
			atDest = append(atDest, next)
		} else if movedSet[p] || placedSet[p] {
			atDest = append(atDest, p)
		} else {
			atSource = append(atSource, p)
		}
	}
	return atDest, atSource
}

// recoverParked makes a bounded best-effort attempt to move the still-parked
// workers back into the surviving caller tab: one split move per worker, in
// order, and it stops at the first failed move — no retries, and it never
// closes or kills a worker. It records each successful move in the roster
// and returns the actual ids of the workers it moved back.
func recoverParked(dir, callerTab, callerPane string, stillParked []string, env platform.Env) []string {
	recovered := []string{}
	for _, p := range stillParked {
		r := moveInTab(p, callerTab, "right", callerPane, "0.5", env)
		if !r.Ok {
			break
		}
		next := p
		if r.Pane != "" && r.Pane != p {
			if dir != "" {
				core.RosterReplacePane(dir, p, r.Pane)
			}
			next = r.Pane
		}
		recovered = append(recovered, next)
	}
	return recovered
}

// regridPartialFailureMessage names, by actual pane id, which panes a
// partially failed grid (and the bounded recovery that followed it) left at
// the source and which it placed at the destination, so the error says
// where the live workers actually are. recovered must hold the actual ids
// of the first len(recovered) entries of the source side, in order.
func regridPartialFailureMessage(what, destination, source string, panes, placed []string, changes [][2]string, moved []string, recovered []string) string {
	atDest, atSource := whereAfterFailedGrid(panes, placed, changes, moved)
	still := atSource[len(recovered):]
	inCaller := append(append([]string{}, atDest...), recovered...)
	message := fmt.Sprintf("regrid: %s failed", what)
	if len(still) > 0 {
		message += fmt.Sprintf("; %s are alive in tab %s", strings.Join(still, " "), source)
	}
	if len(inCaller) > 0 {
		if len(still) > 0 {
			message += " and"
		} else {
			message += ";"
		}
		message += fmt.Sprintf(" %s are alive in tab %s", strings.Join(inCaller, " "), destination)
	}
	message += "; the roster already holds their current pane ids"
	return message
}

type RegridEntry struct {
	Tab   string `json:"tab"`
	Label string `json:"label"`
	Panes int    `json:"panes"`
	Cols  int    `json:"cols"`
}
type RegridOutput struct {
	Regridded []RegridEntry `json:"regridded"`
}

func dieRegrid(ctx *core.Config, env platform.Env, cwd, message string) {
	platform.DieFriction(message, 4)
}

func relabelHerdTabs(relabel func(), ctx *core.Config, env platform.Env, cwd string) {
	defer func() {
		if recover() != nil {
			core.Warn("regrid: relabel of the herd tabs failed", frictionPath(ctx, env, cwd))
		}
	}()
	relabel()
}

func canPullBack(workerCount, cap int) bool { return workerCount+1 < cap }

// gridShapeFor is the caller tab's shape: the declared columns when they
// are in effect (non-empty, valid pane_columns), otherwise today's square
// grid. The herd tabs always keep the square grid.
func gridShapeFor(ctx *core.Config, env platform.Env, n int) Grid {
	cols, overflow, declared := PaneColumns(ctx, env)
	if !declared {
		return GridSizes(n)
	}
	if core.Cfg(ctx, "pane_columns", "", env) == "auto" {
		return autoGrid(n-1, PaneColumnRows(ctx, env), overflow)
	}
	return DeclaredGrid(cols, overflow, n)
}

func CmdRegrid(_ []string, ctx *core.Config, env platform.Env, cwd string) int {
	// The caller-pane check runs before touching the claimed pane's tab
	// (DJA-394); the callerContext slow path below keeps its own rule.
	core.CallerGate("regrid", ctx, env, cwd)
	dir := core.StateDirPath(ctx, env, cwd)
	ws := core.WorkspaceID(ctx, env, cwd)
	root := platform.ProjectRoot(env, cwd)
	layout := core.Cfg(ctx, "layout", "split", env)
	focusBefore := UIFocusedPane(env)
	live := herdr.PaneList(env, ws)
	summary := []RegridEntry{}
	callerTab, callerPane, refuse := callerContext(ctx, env, live, dir, ws)
	if refuse != "" {
		dieRegrid(ctx, env, cwd, refuse)
	}
	if layout == "split" && callerTab != "" && callerPane != "" {
		panes := withoutCaller(rosterPanes(ctx, live, callerTab, env, cwd), callerPane)
		cap, unlimited := SplitCapDeclared(ctx, env)
		if unlimited {
			// canPullBack compares with <; the uncapped declared overflow
			// (last-column, new-column) pulls every worker back.
			cap = 1 << 30
		}
	outer:
		for _, e := range HerdTabEntries(ctx, env, cwd) {
			// The caller may itself live in a recorded herd tab: its workers
			// are already in `panes` (the caller tab's set), so this tab must
			// not also be pulled back into itself — that would append and move
			// the same workers again (double park, wrong grid, wrong count).
			if e.Tab == "" || e.Tab == callerTab {
				continue
			}
			for _, hp := range withoutCaller(rosterPanes(ctx, live, e.Tab, env, cwd), callerPane) {
				if !canPullBack(len(panes), cap) {
					break outer
				}
				m := moveInTab(hp, callerTab, "right", callerPane, "0.5", env)
				if !m.Ok {
					core.Warn(fmt.Sprintf("regrid: could not bring %s back into %s", hp, callerTab), frictionPath(ctx, env, cwd))
					continue
				}
				p := hp
				if m.Pane != "" && m.Pane != hp {
					core.RosterReplacePane(dir, hp, m.Pane)
					p = m.Pane
				}
				panes = append(panes, p)
			}
		}
		live = herdr.PaneList(env, ws)
		if len(panes) >= 1 {
			shape := gridShapeFor(ctx, env, len(panes)+1)
			if core.Cfg(ctx, "pane_columns", "", env) == "auto" {
				panes = autoGridPanes(ctx, env, cwd, panes, shape)
			}
			parked, park, ok := parkPanesTracked(dir, panes, env)
			if !ok {
				dieRegrid(ctx, env, cwd, fmt.Sprintf("regrid: could not park the workers of tab %s in a temporary tab", callerTab))
			}
			changes, moved, ok := gridApplyShape(ctx, callerTab, append([]string{callerPane}, parked...), shape, env, cwd)
			if !ok {
				_, stillParked := whereAfterFailedGrid(parked, nil, changes, moved)
				recovered := recoverParked(dir, callerTab, callerPane, stillParked, env)
				dieRegrid(ctx, env, cwd, regridPartialFailureMessage("a move back into "+callerTab, callerTab, park+" (label herd-park)", parked, nil, changes, moved, recovered))
			}
			summary = append(summary, RegridEntry{callerTab, "caller", len(parked) + 1, shape.Cols})
			// Parking and gridding can change the pane ids recorded in the
			// roster. Rebuild against the topology after those moves.
			live = herdr.PaneList(env, ws)
		}
	}
	kept := ""
	for _, e := range HerdTabEntries(ctx, env, cwd) {
		if e.Tab == "" {
			continue
		}
		label := e.Label
		if label == "-" {
			label = ""
		}
		panes := withoutCaller(rosterPanes(ctx, live, e.Tab, env, cwd), callerPane)
		if len(panes) < 1 {
			continue
		}
		tab := e.Tab
		if len(panes) >= 2 {
			createLabel := label
			if createLabel == "" {
				createLabel = "herd"
			}
			created := herdr.TabCreate(env, ws, root, createLabel)
			if !created.Ok {
				dieRegrid(ctx, env, cwd, "tab create failed")
			}
			newtab := created.Tab
			HerdTabSet(ctx, newtab, label, e.Mode, env, cwd)
			m := moveInTab(panes[0], newtab, "right", created.Root, "0.5", env)
			if !m.Ok {
				dieRegrid(ctx, env, cwd, fmt.Sprintf("regrid: move of %s failed; remaining workers are alive in tab %s", panes[0], e.Tab))
			}
			herdr.PaneClose(created.Root, env)
			if m.Pane != "" && m.Pane != panes[0] {
				core.RosterReplacePane(dir, panes[0], m.Pane)
				panes[0] = m.Pane
			}
			changes, moved, ok := gridApply(ctx, newtab, panes, env, cwd)
			if !ok {
				dieRegrid(ctx, env, cwd, regridPartialFailureMessage("a move into "+newtab, newtab, e.Tab, panes, panes[:1], changes, moved, nil))
			}
			display := label
			if display == "" {
				display = "herd"
			}
			summary = append(summary, RegridEntry{newtab, display, len(panes), GridSizes(len(panes)).Cols})
			tab = newtab
		}
		stored := label
		if stored == "" {
			stored = "-"
		}
		kept += tab + "\t" + stored + "\t" + e.Mode + "\n"
	}
	file := filepath.Join(dir, "herd-tab")
	if _, err := os.Stat(file); err == nil {
		if err := os.WriteFile(file, []byte(kept), 0o666); err != nil {
			panic(err)
		}
	}
	relabelHerdTabs(func() { HerdTabsRelabel(ctx, env, cwd) }, ctx, env, cwd)
	RestoreFocusIfStolen(focusBefore, UIFocusedPane(env), "", env)
	items := make([]any, len(summary))
	for i, entry := range summary {
		items[i] = jsonjs.O("tab", entry.Tab, "label", entry.Label, "panes", entry.Panes, "cols", entry.Cols)
	}
	_, _ = fmt.Fprintf(platform.Stdout, "%s\n", jsonjs.Stringify(jsonjs.O("regridded", items)))
	return 0
}

// AutoRegrid is the exported hook for the later spawn and release Go ports.
func AutoRegrid(ctx *core.Config, env platform.Env, cwd, message string) {
	oldOut, oldErr := platform.Stdout, platform.Stderr
	failed := false
	func() {
		defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
		defer func() {
			if v := recover(); v != nil {
				e, ok := v.(*platform.ExitError)
				if !ok {
					panic(v)
				}
				core.RecordFrictionError(e.Msg, e.Code, frictionPath(ctx, env, cwd))
				failed = true
			}
		}()
		platform.Stdout = discardWriter{}
		platform.Stderr = discardWriter{}
		CmdRegrid(nil, ctx, env, cwd)
	}()
	if failed {
		core.Warn(message, frictionPath(ctx, env, cwd))
	}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
