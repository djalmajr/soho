package core

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	textutil "github.com/djalmajr/soho/internal/text"
	"github.com/djalmajr/soho/internal/transport"
)

// livePanes re-reads a pane listing as the field objects the core helpers
// read: one entry per view, re-parsed from the raw object the transport kept
// on the view.
func livePanes(views []transport.View) []any {
	out := make([]any, 0, len(views))
	for _, v := range views {
		if len(v.Raw) == 0 {
			continue
		}
		if value, _ := jsonjs.Parse(v.Raw); value != nil {
			out = append(out, value)
		}
	}
	return out
}

// Orphan is a live Herdr agent of the current workspace that this project no
// longer owns: its pane and name both match a row of this project's
// released-panes registry (a release without --close leaves the pane alive
// and idle outside the roster), and it is not a roster pane nor the
// orchestrator's own pane.
type Orphan struct {
	Name  string
	Pane  string
	State string
}

// ReleasedPane is one row of the project's released-panes registry
// (<state>/released-panes.tsv): a pane this project released without --close,
// kept until its pane is closed, re-rostered by a spawn, or gone from Herdr.
type ReleasedPane struct {
	Name     string
	Pane     string
	Kind     string
	Released string
}

const releasedPanesFile = "released-panes.tsv"

const releasedPanesHeader = "# name\tpane\tkind\treleased\n"

// ReleasedPanes reads the registry; a missing or unreadable file reads as no
// released panes. Rows keep file order.
func ReleasedPanes(ctx *Config, env platform.Env, cwd string) []ReleasedPane {
	return readReleasedPanes(StateDirPath(ctx, env, cwd))
}

func readReleasedPanes(dir string) []ReleasedPane {
	raw, err := platform.ReadTextFile(filepath.Join(dir, releasedPanesFile))
	if err != nil {
		return []ReleasedPane{}
	}
	rows := []ReleasedPane{}
	for _, line := range strings.Split(raw, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 4 || f[0] == "" || f[1] == "" {
			continue
		}
		rows = append(rows, ReleasedPane{Name: f[0], Pane: f[1], Kind: f[2], Released: f[3]})
	}
	return rows
}

// releasedPanesWrite applies one registry change the way the other state
// files are written: StateDir refuses a state dir inside the skill before any
// write, the roster lock serializes the read/rewrite, rows whose pane is
// gone from Herdr (herdr pane get reports pane_not_found) are pruned, and
// the file is rewritten atomically. A pane that fails the get for any other
// cause is kept, so a herdr outage never wipes the registry.
func releasedPanesWrite(ctx *Config, env platform.Env, cwd string, mutate func(rows []ReleasedPane) []ReleasedPane) {
	sd := StateDir(ctx, env, cwd)
	svc := herdr.New(env)
	WithRosterLock(sd, func() {
		current := readReleasedPanes(sd)
		pruned := make([]ReleasedPane, 0, len(current))
		for _, row := range current {
			if !svc.Terminal.GetGone(nil, transport.Target{ID: row.Pane}, transport.Call{}).Value {
				pruned = append(pruned, row)
			}
		}
		writeReleasedPanes(sd, mutate(pruned))
	})
}

// ReleasedPanesRecord registers a release without --close: one row per
// (name, pane), a repeated release refreshes the row.
func ReleasedPanesRecord(ctx *Config, env platform.Env, cwd string, name, pane, kind string) {
	releasedPanesWrite(ctx, env, cwd, func(rows []ReleasedPane) []ReleasedPane {
		for i, row := range rows {
			if row.Name == name && row.Pane == pane {
				rows = append(rows[:i], rows[i+1:]...)
				break
			}
		}
		return append(rows, ReleasedPane{Name: name, Pane: pane, Kind: kind, Released: NowStamp(platform.Now())})
	})
}

// ReleasedPanesForget removes the registry rows of a pane: the pane is
// closed, or a spawn re-rostered it.
func ReleasedPanesForget(ctx *Config, env platform.Env, cwd string, pane string) {
	releasedPanesWrite(ctx, env, cwd, func(rows []ReleasedPane) []ReleasedPane {
		out := make([]ReleasedPane, 0, len(rows))
		for _, row := range rows {
			if row.Pane != pane {
				out = append(out, row)
			}
		}
		return out
	})
}

func writeReleasedPanes(dir string, rows []ReleasedPane) {
	lines := make([]string, 0, len(rows)+1)
	lines = append(lines, releasedPanesHeader)
	for _, row := range rows {
		lines = append(lines, row.Name+"\t"+row.Pane+"\t"+row.Kind+"\t"+row.Released)
	}
	content := ""
	if len(lines) != 0 {
		content = strings.Join(lines, "\n") + "\n"
	}
	if err := platform.AtomicWrite(filepath.Join(dir, releasedPanesFile), content); err != nil {
		panic(err)
	}
}

// OrphansOf classifies a herdr agent list into the orphans of the current
// workspace: a live agent whose pane and name both match a row of this
// project's released-panes registry, that is not a roster pane and is not
// the orchestrator's own pane (HERDR_PANE_ID). A failed pane list reads as no
// workspace pane, so nothing is classified (no false orphan).
func OrphansOf(ctx *Config, env platform.Env, cwd string, live []any) []Orphan {
	svc := herdr.New(env)
	rosterPanes := map[string]bool{}
	for _, line := range RosterRows(StateDirPath(ctx, env, cwd)) {
		fields := strings.Split(line, "\t")
		if len(fields) > 1 && fields[1] != "" {
			rosterPanes[fields[1]] = true
		}
	}
	workspacePanes := map[string]bool{}
	for _, pane := range livePanes(svc.Window.ListPanes(nil, transport.Scope{WorkspaceID: WorkspaceID(ctx, env, cwd)}, transport.Call{}).Value) {
		if id := agentField(pane, "pane_id"); id != "" {
			workspacePanes[id] = true
		}
	}
	released := map[string]bool{}
	for _, row := range ReleasedPanes(ctx, env, cwd) {
		released[row.Name+"\x00"+row.Pane] = true
	}
	orchestrator := svc.Runtime.Caller().PaneID
	out := []Orphan{}
	for _, agent := range live {
		name := agentField(agent, "name")
		pane := agentField(agent, "pane_id")
		if name == "" || pane == "" {
			continue
		}
		if rosterPanes[pane] || pane == orchestrator || !workspacePanes[pane] {
			continue
		}
		if !released[name+"\x00"+pane] {
			continue
		}
		out = append(out, Orphan{Name: name, Pane: pane, State: agentField(agent, "agent_status")})
	}
	sort.SliceStable(out, func(i, j int) bool { return textutil.CompareUTF16(out[i].Name, out[j].Name) < 0 })
	return out
}

// Orphans is OrphansOf over herdr.LiveAgents: a failed agent list dies with
// herdr.LiveAgents' own error, the same way the roster's "other live agents"
// section fails.
func Orphans(ctx *Config, env platform.Env, cwd string) []Orphan {
	svc := herdr.New(env)
	// OrphansOf still reads the live list as field objects: rebuild it from
	// the transport terminals with the field names that function reads.
	live := svc.Runtime.List(nil, transport.Scope{}, transport.Call{}).Value
	rows := make([]any, 0, len(live))
	for _, a := range live {
		rows = append(rows, jsonjs.O("name", a.Name, "pane_id", a.PaneID, "agent_status", a.State))
	}
	return OrphansOf(ctx, env, cwd, rows)
}

// FindOrphan reports the orphan named exactly name, when it has one.
func FindOrphan(name string, ctx *Config, env platform.Env, cwd string) (Orphan, bool) {
	for _, orphan := range Orphans(ctx, env, cwd) {
		if orphan.Name == name {
			return orphan, true
		}
	}
	return Orphan{}, false
}
