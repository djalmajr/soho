package cli

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/provider"
	"github.com/djalmajr/soho/internal/transport"
)

type explainStateDirResult struct {
	rc  int
	dir string
}
type explainRow struct{ lane, role, kind, model, activity, burst string }

func cmdExplain(args []string, ctx *core.Config, env platform.Env, cwd string) {
	if len(args) > 0 {
		platform.DieFriction("explain: takes no arguments", 2)
	}
	resolved := explainStateDir(ctx, env, cwd)
	if resolved.rc == 2 {
		_, _ = platform.Stdout.Write([]byte("Agents were started in more than one Herdr workspace, and this command is not running inside one of them, so it cannot tell which team you mean.\nRun explain from a panel inside the workspace you are asking about.\n"))
		return
	}
	rows := []explainRow{}
	if resolved.dir != "" {
		if info, e := os.Stat(filepath.Join(resolved.dir, "agents.tsv")); e == nil && info.Mode().IsRegular() {
			rows = explainCollectRows(resolved.dir, ctx, env, cwd)
		}
	}
	if len(rows) == 0 {
		for _, line := range explainIdleParagraph() {
			_, _ = platform.Stdout.Write([]byte(line + "\n"))
		}
		// The footprint line, when it is due, sits after the paragraph with a
		// blank line between the two.
		if line := explainFootprintLine(resolved.dir, ctx, env, cwd); line != "" {
			_, _ = platform.Stdout.Write([]byte("\n" + line + "\n"))
		}
		return
	}
	for _, line := range explainPrintRunning(rows, resolved.dir, ctx, env, cwd) {
		_, _ = platform.Stdout.Write([]byte(line + "\n"))
	}
}

// explainFootprintLine returns the team-footprint line for the explain
// output, or "" when it is not due: it shows when at least one registered
// copy is on disk, or a registered process reads as running, or the total
// on-disk footprint reaches footprint_warn_gb (with the limit above 0;
// an unreadable limit is off). It measures read-only.
func explainFootprintLine(stateDir string, ctx *core.Config, env platform.Env, cwd string) string {
	// The workspace id of the state dir (its directory name): the
	// env-var-only rule, so explain never dies on a missing herdr the way
	// core.WorkspaceID can. Caller() is that same pure environment read.
	workspaceID := herdr.New(env).Runtime.Caller().WorkspaceID
	if stateDir != "" {
		workspaceID = filepath.Base(stateDir)
	}
	fp := core.MeasureFootprint(stateDir, workspaceID, env)
	limitRaw := core.Cfg(ctx, "footprint_warn_gb", "5", env)
	limit, err := strconv.ParseInt(limitRaw, 10, 64)
	if !explainFootprintGate(fp, limit, err == nil) {
		return ""
	}
	return explainFootprintText(fp)
}

// explainFootprintGate is the "is the line due" rule: a copy on disk, a
// running registered process, or the total reaching the limit (above 0).
func explainFootprintGate(fp *core.Footprint, limit int64, limitOK bool) bool {
	if fp.Copies > 0 || fp.Running > 0 {
		return true
	}
	return limitOK && core.FootprintLimitReached(fp.Total, limit)
}

// explainFootprintText is the exact line: the disk part in the shared
// human sizes, then the registered processes with the RSS summed in
// memory (the unreadable count in parentheses when it is above zero), or
// the no-process sentence. Singular counts keep the same text.
func explainFootprintText(fp *core.Footprint) string {
	line := "Footprint: " + core.FootprintDiskText(fp) + "; "
	if fp.Running > 0 {
		line += strconv.Itoa(fp.Running) + " registered processes using " + core.HumanSize(fp.RSS) + " of memory"
		if fp.Unreadable > 0 {
			line += " (" + strconv.Itoa(fp.Unreadable) + " unreadable)"
		}
		line += "."
	} else {
		line += "no registered process running."
	}
	return line
}

func explainStateDir(ctx *core.Config, env platform.Env, cwd string) explainStateDirResult {
	svc := herdr.New(env)
	if svc.Runtime.Caller().WorkspaceID != "" {
		return explainStateDirDirect(ctx, env, cwd)
	}
	if _, ok := platform.FindExecutable("herdr", env, platform.Current()); ok {
		// The exit-status-only check keeps its 30 s bound through the raw
		// call: a body the check does not read cannot change the branch.
		rep := svc.Legacy.PaneCurrentRaw(context.Background(), transport.Call{Timeout: herdr.Timeout})
		if rep.Err == nil {
			return explainStateDirDirect(ctx, env, cwd)
		}
	}
	root := core.StateRootPath(ctx, env, cwd)
	entries, e := os.ReadDir(root)
	if e != nil {
		return explainStateDirResult{rc: 1}
	}
	count := 0
	only := ""
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		if !rosterHasData(filepath.Join(dir, "agents.tsv")) {
			continue
		}
		count++
		only = dir
	}
	if count == 1 {
		return explainStateDirResult{rc: 0, dir: only}
	}
	if count == 0 {
		return explainStateDirResult{rc: 1}
	}
	return explainStateDirResult{rc: 2}
}
func explainStateDirDirect(ctx *core.Config, env platform.Env, cwd string) (out explainStateDirResult) {
	defer func() {
		if recover() != nil {
			out = explainStateDirResult{rc: 1}
		}
	}()
	return explainStateDirResult{rc: 0, dir: core.StateDir(ctx, env, cwd)}
}
func rosterHasData(file string) bool {
	raw, e := platform.ReadTextFile(file)
	if e != nil {
		return false
	}
	for _, line := range strings.Split(raw, "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		if strings.TrimSpace(line) != "" {
			return true
		}
	}
	return false
}

func explainActivity(name, sd string, ctx *core.Config, env platform.Env, cwd string) string {
	report := core.LastReport(sd, name)
	if report != "" {
		if info, e := os.Stat(report); e == nil && info.Size() > 0 {
			return "idle"
		}
	}
	if _, ok := platform.FindExecutable("herdr", env, platform.Current()); !ok {
		if report != "" {
			return "waiting for report"
		}
		return "idle"
	}
	svc := herdr.New(env)
	orig := readState(svc, name, transport.Call{Timeout: herdr.Timeout}).State
	if orig == "working" {
		return "working"
	}
	if orig != "blocked" && orig != "gone" && orig != "unavailable" {
		n := 20
		screen, _ := readScreen(svc, name, "visible", &n)
		if provider.QuotaDetect(orig, screen) != nil {
			return "out of quota"
		}
	}
	if report != "" {
		return "waiting for report"
	}
	switch orig {
	case "idle", "done", "":
		return "idle"
	case "blocked":
		return "waiting for approval"
	case "gone":
		return "closed"
	case "unavailable":
		return "state unknown"
	default:
		return orig
	}
}

func explainCollectRows(sd string, ctx *core.Config, env platform.Env, cwd string) []explainRow {
	raw, e := platform.ReadTextFile(filepath.Join(sd, "agents.tsv"))
	if e != nil {
		return nil
	}
	rows := []explainRow{}
	for _, line := range strings.Split(raw, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		name := ""
		if len(f) > 0 {
			name = f[0]
		}
		if name == "" {
			continue
		}
		get := func(i int) string {
			if i < len(f) {
				return f[i]
			}
			return ""
		}
		lane, role, kind, model := get(11), get(3), get(2), get(8)
		if lane == "" {
			lane = core.LaneOfRole(ctx, role, env)
		}
		if lane == "" {
			lane = name
		}
		if role == "" {
			role = "unspecified"
		}
		if kind == "" {
			kind = "unspecified"
		}
		if model == "" {
			model = "default"
		}
		burst := ""
		if get(12) == "burst" {
			burst = "burst"
		}
		rows = append(rows, explainRow{lane, role, kind, model, explainActivity(name, sd, ctx, env, cwd), burst})
	}
	return rows
}
func explainRowLine(r explainRow) string {
	line := r.lane + ": " + r.role + ", " + r.kind + ", model " + r.model + ", " + r.activity
	if r.burst == "burst" {
		line += " (temporary)"
	}
	return line
}
func explainRecommendation(ctx *core.Config, env platform.Env) []string {
	out := []string{}
	p := core.PanesValue(ctx, env)
	switch p {
	case "2":
		out = append(out, "Recommendation: 2 panels - one writes code (research included) and the review happens here. The lightest choice. 3 panels add a reviewer panel.")
	case "3":
		out = append(out, "Recommendation: 3 panels - one writes code (research included) and one reviews. Lighter on quota. 4 panels add a second writer; with 2 panels the review happens here.")
	default:
		out = append(out, "Recommendation: 4 panels - two write code (research included) in parallel and one reviews. Uses more quota. 3 panels are lighter: one writes and one reviews. With 2 panels one writes and the review happens here.")
	}
	if !core.LanesEnabled(ctx, env) {
		return append(out, "Each agent keeps its own assistant instead of sharing one panel.")
	}
	for _, lane := range core.LaneNames(ctx, env) {
		if lane == "" {
			continue
		}
		kind := core.LaneAttr(ctx, lane, "kind", nil, env)
		if kind == "" {
			continue
		}
		model := core.LaneAttr(ctx, lane, "model", nil, env)
		if model != "" {
			out = append(out, "Chosen for "+lane+": "+kind+", model "+model+".")
		} else {
			out = append(out, "Chosen for "+lane+": "+kind+".")
		}
	}
	return out
}
func explainPrintRunning(rows []explainRow, stateDir string, ctx *core.Config, env platform.Env, cwd string) []string {
	flex := core.LanesEnabled(ctx, env) && core.PaneMode(ctx, env) == "flex"
	head := "Panels: " + core.PanesValue(ctx, env) + "."
	if flex {
		head = "Panels: " + core.PanesValue(ctx, env) + " (+" + strconv.Itoa(core.FlexExtra(ctx, env)) + " temporary)."
	}
	out := []string{head}
	lanes := core.LaneNames(ctx, env)
	if core.LanesEnabled(ctx, env) {
		known := map[string]bool{}
		for _, lane := range lanes {
			known[lane] = true
			matches := 0
			for _, row := range rows {
				if row.lane == lane {
					out = append(out, explainRowLine(row))
					matches++
				}
			}
			if matches == 0 {
				out = append(out, lane+": not started")
			}
		}
		for _, row := range rows {
			if !known[row.lane] {
				out = append(out, explainRowLine(row))
			}
		}
	} else {
		for _, row := range rows {
			out = append(out, explainRowLine(row))
		}
	}
	for _, line := range core.RosterRows(stateDir) {
		fields := strings.Split(line, "\t")
		if port := core.RosterE2EPort(fields); port != "" {
			out = append(out, fields[0]+": E2E_PORT="+port)
		}
	}
	// The footprint line, when it is due, sits after the lane lines and
	// before the blank line.
	if line := explainFootprintLine(stateDir, ctx, env, cwd); line != "" {
		out = append(out, line)
	}
	out = append(out, "")
	out = append(out, explainRecommendation(ctx, env)...)
	return out
}
func explainIdleParagraph() []string {
	return []string{"soho runs a small team of agents in Herdr panels. You stay in this panel and lead. Each other panel is one agent with one job: writing code (research included) or reviewing. Those agents never commit or push. You can watch a panel or close it. Each assistant spends the quota of its own account. Nothing is running yet. To start, describe the work here. The first time, you are asked how many panels to open and which assistant each job should use, and nothing opens until you agree. Four panels are recommended: two write code in parallel and one reviews; that uses more quota. Three panels are lighter: one writes and one reviews. With two panels one writes and the review happens here."}
}
