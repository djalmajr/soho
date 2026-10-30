package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"unicode/utf16"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	herdrtext "github.com/djalmajr/soho/internal/text"
	"github.com/djalmajr/soho/internal/transport"
)

func valueString(v any, fallback string) string {
	if v == nil {
		return fallback
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}
func field(m any, name string) any {
	if obj, ok := m.(map[string]any); ok {
		return obj[name]
	}
	if obj, ok := m.(*jsonjs.Object); ok {
		v, _ := obj.Get(name)
		return v
	}
	return nil
}
func strictEqual(a, b any) bool {
	return reflect.TypeOf(a) == reflect.TypeOf(b) && reflect.DeepEqual(a, b)
}
func padRight(s string, width int) string {
	n := len(utf16.Encode([]rune(s)))
	if n >= width {
		return s
	}
	return s + strings.Repeat(" ", width-n)
}
func cutUTF16(s string, limit int) string {
	u := utf16.Encode([]rune(s))
	if len(u) <= limit {
		return s
	}
	return string(utf16.Decode(u[:limit]))
}

func tabLabelString(label any) string {
	if text, ok := label.(string); ok {
		return cutUTF16(text, 16)
	}
	return cutUTF16(jsonjs.Stringify(label), 16)
}

// windowObjects re-projects a window listing back into the raw JSON objects
// the roster reader consumes: View.Raw carries the backend's original object
// re-stringified, so re-parsing it restores the exact field access the
// reader has always done (a parse failure is unreachable: Raw is the
// backend's own stringify of the same object).
func windowObjects(views []transport.View) []any {
	out := make([]any, 0, len(views))
	for _, view := range views {
		parsed, _ := jsonjs.Parse(view.Raw)
		out = append(out, parsed)
	}
	return out
}

// rosterArgs parses the roster arguments: no args, an optional --json and
// an optional --scope <workspace|server>, each in any order, at most once;
// every other shape is invalid.
func rosterArgs(argv []string) (scope string, jsonMode bool, ok bool) {
	scope = "workspace"
	sawJSON, sawScope := false, false
	for i := 0; i < len(argv); i++ {
		switch argv[i] {
		case "--json":
			if sawJSON {
				return "", false, false
			}
			sawJSON = true
			jsonMode = true
		case "--scope":
			if sawScope || i+1 >= len(argv) || (argv[i+1] != "workspace" && argv[i+1] != "server") {
				return "", false, false
			}
			sawScope = true
			scope = argv[i+1]
			i++
		default:
			return "", false, false
		}
	}
	return scope, jsonMode, true
}

func cmdRoster(argv []string, ctx *core.Config, env platform.Env, cwd string) {
	scope, jsonMode, ok := rosterArgs(argv)
	if !ok {
		platform.Die("usage: roster [--scope workspace|server] [--json]", 2)
	}
	sd := core.StateDir(ctx, env, cwd)
	svc := herdr.New(env)
	live := liveAgentsFriction(env)
	ws := core.WorkspaceID(ctx, env, cwd)
	panes := windowObjects(svc.Window.ListPanes(context.Background(), transport.Scope{WorkspaceID: ws}, transport.Call{}).Value)
	tabs := windowObjects(svc.Window.ListTabs(context.Background(), ws, transport.Call{}).Value)
	workspaceOf := map[string]string{}
	for _, p := range panes {
		workspaceOf[valueString(field(p, "pane_id"), "")] = ws
	}
	if scope == "server" {
		for _, p := range windowObjects(svc.Window.ListPanes(context.Background(), transport.Scope{All: true}, transport.Call{}).Value) {
			workspaceOf[valueString(field(p, "pane_id"), "")] = valueString(field(p, "workspace_id"), "unknown")
		}
	}
	// tabValue resolves the pane's tab label without the 16-cut, shared by
	// the text and the JSON output; found is false when the pane has no tab
	// (the text shows "-", the JSON shows "").
	tabValue := func(pane string) (string, bool) {
		var tab any
		for _, p := range panes {
			if strictEqual(field(p, "pane_id"), pane) {
				tab = field(p, "tab_id")
				break
			}
		}
		if tab == nil {
			return "", false
		}
		var label any
		for _, t := range tabs {
			if strictEqual(field(t, "tab_id"), tab) {
				label = field(t, "label")
				break
			}
		}
		if label == nil || label == false {
			label = tab
		}
		if text, found := label.(string); found {
			return text, true
		}
		return jsonjs.Stringify(label), true
	}
	tabOf := func(pane string) string {
		if value, found := tabValue(pane); found {
			return cutUTF16(value, 16)
		}
		return "-"
	}
	// The four values of the last text line, read once for the text and the
	// JSON output.
	layoutValue := core.Cfg(ctx, "layout", "split", env)
	reuseWorkersValue := core.Cfg(ctx, "reuse_workers", "on", env)
	multiRoleValue := core.Cfg(ctx, "multi_role", "on", env)
	autoApproveValue := core.Cfg(ctx, "auto_approve", "off", env)
	rosterPanes := map[string]bool{}
	if jsonMode {
		agents := []any{}
		for _, line := range core.RosterRows(sd) {
			f := strings.Split(line, "\t")
			col := func(i int) string {
				if i < len(f) {
					return f[i]
				}
				return ""
			}
			name, pane, kind, role, cwdCol := col(0), col(1), col(2), col(3), col(6)
			if pane != "" {
				rosterPanes[pane] = true
			}
			if name == "" {
				continue
			}
			var agent any
			for _, a := range live {
				if strictEqual(field(a, "name"), name) && (pane == "" || strictEqual(field(a, "pane_id"), pane)) {
					agent = a
					break
				}
			}
			state := valueString(field(agent, "agent_status"), "gone")
			report := "none"
			if reportPath := core.LastReport(sd, name); reportPath != "" {
				if st, err := os.Stat(reportPath); err == nil && st.Size() > 0 {
					report = "ready"
				} else {
					report = "pending"
				}
			}
			previousRole := ""
			if history := col(10); history != "" && history != role {
				previousRole = history
			}
			task := ""
			if raw, err := os.ReadFile(filepath.Join(sd, "task-"+name)); err == nil {
				taskText := herdrtext.ToWellFormedUTF8(string(raw))
				taskText = strings.TrimRight(taskText, "\n")
				if taskText != "" {
					task = taskText
				}
			}
			tab, _ := tabValue(pane)
			agents = append(agents, jsonjs.O("name", name, "role", role, "previous_role", previousRole, "kind", kind, "pane", pane, "tab", tab, "state", state, "report", report, "cwd", cwdCol, "task", task))
		}
		others := []any{}
		for _, agent := range live {
			// The membership check and the scope rule use the text's "null"
			// fallback, so the JSON lists exactly the agents the text lists;
			// the printed pane falls back to "" like every absent value.
			p := valueString(field(agent, "pane_id"), "null")
			if rosterPanes[p] {
				continue
			}
			workspace, found := workspaceOf[p]
			if scope == "workspace" && (!found || workspace != ws) {
				continue
			}
			if workspace == "" {
				workspace = "unknown"
			}
			tab, _ := tabValue(p)
			others = append(others, jsonjs.O("name", valueString(field(agent, "name"), ""), "kind", valueString(field(agent, "agent"), ""), "pane", valueString(field(agent, "pane_id"), ""), "tab", tab, "state", valueString(field(agent, "agent_status"), ""), "workspace", workspace))
		}
		_, _ = fmt.Fprintln(platform.Stdout, jsonjs.Stringify(jsonjs.O(
			"workspace", ws,
			"scope", scope,
			"settings", jsonjs.O("layout", layoutValue, "reuse_workers", reuseWorkersValue, "multi_role", multiRoleValue, "auto_approve", autoApproveValue),
			"agents", agents,
			"others", others,
		)))
		return
	}
	fmt.Fprintf(platform.Stdout, "%s %s %s %s %s %s %s CWD TASK\n", padRight("NAME", 20), padRight("ROLE", 18), padRight("KIND", 8), padRight("PANE", 8), padRight("TAB", 16), padRight("STATE", 9), padRight("REPORT", 16))
	rows := core.RosterRows(sd)
	for _, line := range rows {
		f := strings.Split(line, "\t")
		col := func(i int) string {
			if i < len(f) {
				return f[i]
			}
			return ""
		}
		name, pane, kind, role, cwdCol := col(0), col(1), col(2), col(3), col(6)
		if pane != "" {
			rosterPanes[pane] = true
		}
		if name == "" {
			continue
		}
		var agent any
		for _, a := range live {
			if strictEqual(field(a, "name"), name) && (pane == "" || strictEqual(field(a, "pane_id"), pane)) {
				agent = a
				break
			}
		}
		state := valueString(field(agent, "agent_status"), "gone")
		report := "none"
		if reportPath := core.LastReport(sd, name); reportPath != "" {
			if st, err := os.Stat(reportPath); err == nil && st.Size() > 0 {
				report = "ready"
			} else {
				report = "pending"
			}
		}
		if history := col(10); history != "" && history != role {
			candidate := role + " (" + history + ")"
			if len(utf16.Encode([]rune(candidate))) <= 18 {
				role = candidate
			}
		}
		task := "-"
		if raw, err := os.ReadFile(filepath.Join(sd, "task-"+name)); err == nil {
			taskText := herdrtext.ToWellFormedUTF8(string(raw))
			taskText = strings.TrimRight(taskText, "\n")
			if taskText != "" {
				task = taskText
				if len(utf16.Encode([]rune(task))) > 40 {
					task = cutUTF16(task, 39) + "…"
				}
			}
		}
		fmt.Fprintf(platform.Stdout, "%s %s %s %s %s %s %s %s %s\n", padRight(name, 20), padRight(role, 18), padRight(kind, 8), padRight(pane, 8), padRight(tabOf(pane), 16), padRight(state, 9), padRight(report, 16), cwdCol, task)
	}
	groups := map[string][]any{}
	for _, agent := range live {
		p := valueString(field(agent, "pane_id"), "null")
		if rosterPanes[p] {
			continue
		}
		workspace, found := workspaceOf[p]
		if scope == "workspace" && (!found || workspace != ws) {
			continue
		}
		if workspace == "" {
			workspace = "unknown"
		}
		groups[workspace] = append(groups[workspace], agent)
	}
	if scope == "workspace" {
		fmt.Fprintf(platform.Stdout, "\n# other live agents in workspace %s (not spawned by this skill)\n", ws)
	} else {
		fmt.Fprintln(platform.Stdout, "\n# other live agents on this server (not spawned by this skill)")
	}
	keys := make([]string, 0, len(groups))
	for workspace := range groups {
		keys = append(keys, workspace)
	}
	sort.Strings(keys)
	for _, workspace := range keys {
		if scope == "server" {
			fmt.Fprintf(platform.Stdout, "# workspace %s\n", workspace)
		}
		for _, agent := range groups[workspace] {
			p := valueString(field(agent, "pane_id"), "null")
			fmt.Fprintf(platform.Stdout, "%s %s %s %s %s %s\n", padRight(valueString(field(agent, "name"), "-"), 20), padRight("-", 18), padRight(valueString(field(agent, "agent"), "null"), 8), padRight(p, 8), padRight(tabOf(p), 16), padRight(valueString(field(agent, "agent_status"), "null"), 9))
		}
	}
	fmt.Fprintf(platform.Stdout, "\nlayout=%s reuse_workers=%s multi_role=%s auto_approve=%s\n", layoutValue, reuseWorkersValue, multiRoleValue, autoApproveValue)
}
