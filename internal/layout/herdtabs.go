package layout

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
)

type TabEntry struct{ Tab, Label, Mode string }

var roleAbbreviations = map[string]string{"implementer": "impl", "reviewer": "rev", "inspector": "insp", "designer": "des", "scouter": "scout", "researcher": "res", "tasker": "task", "security-reviewer": "sec", "sub-orchestrator": "sub", "planner": "plan"}
var legacyHerdLabel = regexp.MustCompile(`^herd(-[0-9]+)?$`)
var trailingAutoLabel = regexp.MustCompile("[\t\n\v\f\r \u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000\ufeff+·]+$")

func RoleAbbrev(role string) string {
	if v, ok := roleAbbreviations[role]; ok {
		return v
	}
	return role
}
func HerdLabelMax(ctx *core.Config, env platform.Env) int {
	v := core.Cfg(ctx, "herd_label_max", "16", env)
	if !regexp.MustCompile(`^[0-9]+$`).MatchString(v) {
		return 16
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 16
	}
	return n
}
func storedLabel(label string) string {
	if label == "" {
		return "-"
	}
	return label
}
func entryLines(file string) []string {
	raw, err := platform.ReadTextFile(file)
	if err != nil || raw == "" {
		return []string{}
	}
	lines := strings.Split(raw, "\n")
	return lines[:len(lines)-1]
}
func parseEntry(line string) TabEntry {
	rest := strings.Trim(line, "\t")
	take := func() string {
		i := strings.IndexByte(rest, '\t')
		if i < 0 {
			f := rest
			rest = ""
			return f
		}
		f := rest[:i]
		rest = strings.TrimLeft(rest[i:], "\t")
		return f
	}
	return TabEntry{Tab: take(), Label: take(), Mode: rest}
}
func tabFile(ctx *core.Config, env platform.Env, cwd string) string {
	return filepath.Join(core.StateDirPath(ctx, env, cwd), "herd-tab")
}

// HerdTabEntries migrates legacy entries, detects manual renames, prunes dead tabs, and persists the normalized file.
func HerdTabEntries(ctx *core.Config, env platform.Env, cwd string) []TabEntry {
	file := tabFile(ctx, env, cwd)
	if _, err := os.Stat(file); err != nil {
		return []TabEntry{}
	}
	out := []TabEntry{}
	for _, line := range entryLines(file) {
		e := parseEntry(line)
		if e.Tab == "" {
			continue
		}
		label := e.Label
		if label == "-" {
			label = ""
		}
		g := herdr.TabGet(e.Tab, env)
		if !g.Ok {
			continue
		}
		mode := e.Mode
		if mode == "" {
			mode = "manual"
			if g.Label == "" || legacyHerdLabel.MatchString(g.Label) {
				mode = "auto"
			}
			label = g.Label
		} else if mode == "auto" && label != "" && g.Label != "" && g.Label != label {
			mode = "manual"
			label = g.Label
		}
		out = append(out, TabEntry{e.Tab, storedLabel(label), mode})
	}
	lines := make([]string, len(out))
	for i, e := range out {
		lines[i] = e.Tab + "\t" + e.Label + "\t" + e.Mode + "\n"
	}
	if err := platform.AtomicWrite(file, strings.Join(lines, "")); err != nil {
		panic(err)
	}
	return out
}

func HerdTabSet(ctx *core.Config, tab, label, mode string, env platform.Env, cwd string) {
	file := tabFile(ctx, env, cwd)
	out := []string{}
	found := false
	for _, line := range entryLines(file) {
		p := parseEntry(line)
		if p.Tab == "" {
			continue
		}
		if p.Tab == tab {
			out = append(out, tab+"\t"+storedLabel(label)+"\t"+mode)
			found = true
		} else {
			out = append(out, p.Tab+"\t"+storedLabel(p.Label)+"\t"+p.Mode)
		}
	}
	if !found {
		out = append(out, tab+"\t"+storedLabel(label)+"\t"+mode)
	}
	for i := range out {
		out[i] += "\n"
	}
	if err := platform.AtomicWrite(file, strings.Join(out, "")); err != nil {
		panic(err)
	}
}

func ComposeHerdLabel(template string, roles []string, index int, orchestrator string) string {
	seen := map[string]bool{}
	names := []string{}
	for _, role := range roles {
		abbrev := RoleAbbrev(role)
		if !seen[abbrev] {
			seen[abbrev] = true
			if len(names) == 0 || names[len(names)-1] == "" {
				names = append(names, abbrev)
			} else {
				names = append(names, "+"+abbrev)
			}
		}
	}
	iText := ""
	if index > 1 {
		iText = strconv.Itoa(index)
	}
	result := strings.ReplaceAll(template, "{roles}", strings.Join(names, ""))
	result = strings.ReplaceAll(result, "{n}", strconv.Itoa(len(roles)))
	result = strings.ReplaceAll(result, "{i}", iText)
	result = strings.ReplaceAll(result, "{orch}", orchestrator)
	return strings.TrimSpace(result)
}

func HerdAutoLabel(base string, taken []string, ctx *core.Config, env platform.Env) string {
	if base == "" {
		base = "herd"
	}
	chars := []rune(base)
	max := HerdLabelMax(ctx, env)
	if max < 0 {
		max = 0
	}
	cut := func(n int) string {
		if n < 0 {
			n = 0
		}
		if n > len(chars) {
			n = len(chars)
		}
		return trailingAutoLabel.ReplaceAllString(string(chars[:n]), "")
	}
	cand := cut(max)
	k := 2
	for containsString(taken, cand) {
		suffix := " " + strconv.Itoa(k)
		cand = cut(max-len([]rune(suffix))) + suffix
		k++
	}
	return cand
}
func containsString(values []string, target string) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}

func rosterPanes(ctx *core.Config, live []any, tab string, env platform.Env, cwd string) []string {
	out := []string{}
	for _, line := range core.RosterRows(core.StateDirPath(ctx, env, cwd)) {
		f := strings.Split(line, "\t")
		if len(f) < 2 || f[0] == "" {
			continue
		}
		pane := f[1]
		present := false
		for _, v := range live {
			o, ok := v.(*jsonjs.Object)
			if !ok {
				continue
			}
			pid, _ := o.Get("pane_id")
			tid, _ := o.Get("tab_id")
			if pid == pane && tid == tab {
				present = true
				break
			}
		}
		if !present {
			continue
		}
		if herdr.AgentState(f[0], env, herdr.Timeout, nil).State == "gone" {
			continue
		}
		out = append(out, pane)
	}
	return out
}
func rosterRoles(ctx *core.Config, live []any, tab string, env platform.Env, cwd string) []string {
	out := []string{}
	for _, line := range core.RosterRows(core.StateDirPath(ctx, env, cwd)) {
		f := strings.Split(line, "\t")
		if len(f) < 2 || f[0] == "" {
			continue
		}
		present := false
		for _, v := range live {
			o, ok := v.(*jsonjs.Object)
			if !ok {
				continue
			}
			pid, _ := o.Get("pane_id")
			tid, _ := o.Get("tab_id")
			if pid == f[1] && tid == tab {
				present = true
				break
			}
		}
		if present {
			role := ""
			if len(f) > 3 {
				role = f[3]
			}
			out = append(out, role)
		}
	}
	return out
}
func RosterPanesInTab(ctx *core.Config, live []any, tab string, env platform.Env, cwd string) []string {
	return rosterPanes(ctx, live, tab, env, cwd)
}

func HerdTabsRelabel(ctx *core.Config, env platform.Env, cwd string) {
	ws := core.WorkspaceID(ctx, env, cwd)
	live := herdr.PaneList(env, ws)
	entries := HerdTabEntries(ctx, env, cwd)
	tpl := core.Cfg(ctx, "herd_label", "{roles}", env)
	orch := ""
	if strings.Contains(tpl, "{orch}") {
		orch = herdr.CallerAgentName(env)
		if orch == "" {
			orch = core.Cfg(ctx, "orchestrator_name", "orchestrator", env)
		}
	}
	taken := []string{}
	index := 0
	for _, e := range entries {
		if e.Tab == "" {
			continue
		}
		label := e.Label
		if label == "-" {
			label = ""
		}
		index++
		if e.Mode == "auto" {
			roles := rosterRoles(ctx, live, e.Tab, env, cwd)
			want := HerdAutoLabel(ComposeHerdLabel(tpl, roles, index, orch), taken, ctx, env)
			cur := herdr.TabGet(e.Tab, env).Label
			if want != cur && !herdr.TabRename(e.Tab, want, env) {
				core.Warn(fmt.Sprintf("tab rename %s → '%s' failed", e.Tab, want), frictionPath(ctx, env, cwd))
			}
			label = want
		}
		taken = append(taken, label)
		HerdTabSet(ctx, e.Tab, label, e.Mode, env, cwd)
	}
}
func frictionPath(ctx *core.Config, env platform.Env, cwd string) string {
	return filepath.Join(core.StateDirPath(ctx, env, cwd), "friction.log")
}

func HerdTabSplit(ctx *core.Config, tab, workerCwd string, env platform.Env, cwd string, beforeCreate ...func()) herdr.PaneSplitResult {
	live := herdr.PaneList(env, core.WorkspaceID(ctx, env, cwd))
	anchor := ""
	for _, v := range live {
		o, ok := v.(*jsonjs.Object)
		if !ok {
			continue
		}
		tid, _ := o.Get("tab_id")
		if tid == tab {
			pid, _ := o.Get("pane_id")
			anchor = ""
			if pid != nil && !jsonjs.IsUndefined(pid) {
				anchor = fmt.Sprint(pid)
			}
		}
	}
	if anchor == "" {
		return herdr.PaneSplitResult{Ok: true, Pane: herdr.TabGet(tab, env).Root}
	}
	for _, prepare := range beforeCreate {
		prepare()
	}
	s := herdr.PaneSplit(anchor, AutoDirectionFor(env, anchor), workerCwd, env, nil)
	if !s.Ok {
		platform.DieFriction("pane split failed", 4)
	}
	return s
}

// beforeCreate runs only after placement selects a new shell, before its create
// call. Adopting an existing empty tab root does not invoke it.
func HerdTabPane(ctx *core.Config, workerCwd, label, role string, env platform.Env, cwd string, beforeCreate ...func()) herdr.PaneSplitResult {
	ws := core.WorkspaceID(ctx, env, cwd)
	cap := SplitCap(ctx, env)
	live := herdr.PaneList(env, ws)
	newLabel, newMode := "", ""
	if label != "" {
		cand := label
		for k := 2; ; k++ {
			var match *TabEntry
			for _, e := range HerdTabEntries(ctx, env, cwd) {
				if e.Label == cand {
					copy := e
					match = &copy
					break
				}
			}
			if match == nil {
				break
			}
			if len(rosterPanes(ctx, live, match.Tab, env, cwd)) < cap {
				HerdTabSet(ctx, match.Tab, cand, "manual", env, cwd)
				return HerdTabSplit(ctx, match.Tab, workerCwd, env, cwd, beforeCreate...)
			}
			cand = fmt.Sprintf("%s ·%d", label, k)
		}
		newLabel, newMode = cand, "manual"
	} else {
		entries := HerdTabEntries(ctx, env, cwd)
		for _, e := range entries {
			if e.Tab != "" && len(rosterPanes(ctx, live, e.Tab, env, cwd)) < cap {
				return HerdTabSplit(ctx, e.Tab, workerCwd, env, cwd, beforeCreate...)
			}
		}
		taken := make([]string, len(entries))
		for i, e := range entries {
			taken[i] = e.Label
		}
		abbr := ""
		if role != "" {
			abbr = RoleAbbrev(role)
		}
		newLabel, newMode = HerdAutoLabel(abbr, taken, ctx, env), "auto"
	}
	for _, prepare := range beforeCreate {
		prepare()
	}
	t := herdr.TabCreate(env, ws, workerCwd, newLabel)
	if !t.Ok {
		platform.DieFriction("tab create failed", 4)
	}
	HerdTabSet(ctx, t.Tab, newLabel, newMode, env, cwd)
	return herdr.PaneSplitResult{Ok: true, Pane: t.Root}
}

func jsStringLen(s string) int { return len(utf16.Encode([]rune(s))) }
func padJS(s string, n int) string {
	if l := jsStringLen(s); l < n {
		return s + strings.Repeat(" ", n-l)
	}
	return s
}
func CmdTabLabel(args []string, ctx *core.Config, env platform.Env, cwd string) int {
	text, tab := "", ""
	auto := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--tab" {
			if i+1 >= len(args) {
				core.DieFriction("tab-label: --tab expects a value", 2, frictionPath(ctx, env, cwd), "tab-label")
			}
			i++
			tab = args[i]
		} else if a == "--auto" {
			auto = true
		} else if strings.HasPrefix(a, "--") {
			core.DieFriction("tab-label: unknown option "+a, 2, frictionPath(ctx, env, cwd), "tab-label")
		} else if text == "" {
			text = a
		} else {
			text += " " + a
		}
	}
	entries := HerdTabEntries(ctx, env, cwd)
	if text == "" && !auto {
		_, _ = fmt.Fprintf(platform.Stdout, "%s %s MODE\n", padJS("TAB", 10), padJS("LABEL", 18))
		for _, e := range entries {
			_, _ = fmt.Fprintf(platform.Stdout, "%s %s %s\n", padJS(e.Tab, 10), padJS(e.Label, 18), e.Mode)
		}
		if len(entries) == 0 {
			_, _ = fmt.Fprintln(platform.Stdout, "(no herd tab yet)")
		}
		return 0
	}
	if len(entries) == 0 {
		core.DieFriction("tab-label: no herd tab yet (workers overflow into one when the caller's tab is full)", 3, frictionPath(ctx, env, cwd), "tab-label")
	}
	if tab == "" {
		caller := env.Get("HERDR_TAB_ID")
		for _, e := range entries {
			if e.Tab == caller && caller != "" {
				tab = caller
				break
			}
		}
		if tab == "" {
			tab = entries[len(entries)-1].Tab
		}
	}
	found := false
	for _, e := range entries {
		if e.Tab == tab {
			found = true
			break
		}
	}
	if !found {
		core.DieFriction(fmt.Sprintf("tab-label: %s is not a herd tab of this workspace (see: tab-label)", tab), 3, frictionPath(ctx, env, cwd), "tab-label")
	}
	label, mode := text, "manual"
	if auto {
		HerdTabSet(ctx, tab, "", "auto", env, cwd)
		HerdTabsRelabel(ctx, env, cwd)
		for _, e := range HerdTabEntries(ctx, env, cwd) {
			if e.Tab == tab {
				label = e.Label
				break
			}
		}
		mode = "auto"
	} else {
		if len([]rune(text)) > HerdLabelMax(ctx, env) {
			core.Warn(fmt.Sprintf("label '%s' is longer than %d characters; the sidebar will cut it", text, HerdLabelMax(ctx, env)), frictionPath(ctx, env, cwd))
		}
		if !herdr.TabRename(tab, text, env) {
			core.DieFriction("tab rename failed", 4, frictionPath(ctx, env, cwd), "tab-label")
		}
		HerdTabSet(ctx, tab, text, "manual", env, cwd)
	}
	obj := jsonjs.O("tab", tab, "label", label, "mode", mode)
	_, _ = fmt.Fprintln(platform.Stdout, jsonjs.StringifyIndent(obj, 2))
	return 0
}
