// Package spawn implements opening and reusing Herdr workers.
package spawn

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/backend"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/kinds"
	"github.com/djalmajr/soho/internal/layout"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/provider"
	"github.com/djalmajr/soho/internal/spawn/codexhooks"
	"github.com/djalmajr/soho/internal/startup"
	"github.com/djalmajr/soho/internal/taskreport"
	"github.com/djalmajr/soho/internal/text"
	"github.com/djalmajr/soho/internal/transport"
	waitpkg "github.com/djalmajr/soho/internal/wait"
)

var busyRetryDelay = time.Second

const (
	herdrStartTimeout = 30 * time.Second
	startPollWindow   = 5 * time.Second
	// mcpCodexListTimeoutMs caps the `codex mcp list --json` run the
	// per-server MCP-off translation needs (DJA-392 fatia 2 R2).
	mcpCodexListTimeoutMs = 10000
	// contextWindowSettle is the pause between typing /context-window and its
	// Enter, giving grok's menu time to appear before the selection.
	contextWindowSettle = time.Second
	// contextWindowWindow is how long spawn waits for grok's
	// "Context window set to" line on the visible screen.
	contextWindowWindow = 10 * time.Second
)

// AgentNameRE is the agent-name rule spawn enforces. The single regexp
// definition lives in wait, which cannot import spawn (spawn depends on wait
// through dispatch); spawn only re-exports it.
var AgentNameRE = waitpkg.AgentNameRE

func Warn(message string, ctx *core.Config, env platform.Env, command string) {
	core.Warn(message, filepath.Join(core.StateDirPath(ctx, env, mustGetwd()), "friction.log"), command)
}

// warnOnce retains the spawn adapter while the marker implementation is shared.
var warnOnce = func(key, message string, ctx *core.Config, env platform.Env, cwd string, admissionOnly bool, warn func(string, *core.Config, platform.Env, string)) {
	core.WarnOnce(key, func() string { return core.StateDirPath(ctx, env, cwd) }, env, admissionOnly, func() { warn(message, ctx, env, "spawn") })
}

func mustGetwd() string { cwd, _ := os.Getwd(); return cwd }

// mcpCodexOffArgs builds codex's per-server MCP-off args (DJA-392 fatia 2
// R2): it runs `codex mcp list --json` in the spawn's own PATH and env, in
// the worker's cwd, and returns, for every server the listing reports with
// enabled=true, the pair -c mcp_servers.<name>.enabled=false — in list order.
// Only name and enabled are read from the listing: the transport command
// lines may carry credentials and never leave this function. ok is false
// (with a short reason) when codex is not on the PATH, the listing times out,
// exits nonzero, or prints non-JSON output; the spawn then warns once and the
// agent keeps the user's MCP servers. Zero enabled servers are success with
// no args and no reason.
//
// DJA-418: codex 0.163.0's TUI dies at bootstrap when an override names a
// server defined only in the project's .codex/config.toml, and `codex mcp
// list` accepts the same override. codex reads the project's config from its
// process cwd, so the listing runs a second time in an empty directory made
// with os.MkdirTemp and removed on return (the neutral listing). An enabled
// server that the neutral listing does not report is the project's own: it
// gets no override and no check, and projectOnly lists it in list order. A
// neutral listing that fails is ok=false with "neutral listing <reason>".
//
// DJA-414: codex 0.163.0 refuses the whole config when an override names a
// server that is not under [mcp_servers.*] of config.toml, and the pane dies
// before drawing. So the overrides are checked with one
// `codex <overrides> mcp list --json` run (same PATH, env, cwd and timeout).
// A check that fails with a line naming `mcp_servers.<name>` of one of the
// overrides drops that override and checks again (at most one check per
// override, plus one); stayOn lists the dropped names in list order. A check
// that names no override, or more than one (mcpCodexLineName reads only a
// line that is exactly codex's), and any other failure of the check is
// ok=false ("override check exit <n>" or "override check timed out"). Only the
// server name is read from the check's output.
func mcpCodexOffArgs(env platform.Env, cwd string, keep []string) (args []string, ok bool, reason string, stayOn, projectOnly, listed []string) {
	servers, reason := mcpCodexList(env, cwd)
	if reason != "" {
		return nil, false, reason, nil, nil, nil
	}
	// listed is every name the listing reports, in list order: the caller
	// warns for a list choice whose name is not among them.
	listed = make([]string, 0, len(servers))
	for _, server := range servers {
		if server.Name != "" {
			listed = append(listed, server.Name)
		}
	}
	// DJA-693: keep is the list choice's names (nil for mcp=none); an enabled
	// server in keep is never turned off.
	keepSet := map[string]bool{}
	for _, name := range keep {
		keepSet[name] = true
	}
	var enabled []string
	for _, server := range servers {
		if server.Enabled && server.Name != "" && !keepSet[server.Name] {
			enabled = append(enabled, server.Name)
		}
	}
	if len(enabled) == 0 {
		return nil, true, "", nil, nil, listed
	}
	// A relative CODEX_HOME resolves against each process's cwd, so the
	// neutral listing would read another user layer than the worker.
	if home := env.Get("CODEX_HOME"); home != "" && !filepath.IsAbs(home) {
		return nil, false, "neutral listing: CODEX_HOME is not an absolute path", nil, nil, nil
	}
	neutralDir, err := os.MkdirTemp("", "soho-codex-mcp-")
	if err != nil {
		return nil, false, "neutral listing: no empty directory", nil, nil, nil
	}
	defer os.RemoveAll(neutralDir)
	if codexProjectAbove(neutralDir, env) {
		return nil, false, "neutral listing: the temporary directory is inside a project with its own .codex/config.toml", nil, nil, nil
	}
	neutral, reason := mcpCodexList(env, neutralDir)
	if reason != "" {
		return nil, false, "neutral listing " + reason, nil, nil, nil
	}
	inNeutral := map[string]bool{}
	for _, server := range neutral {
		inNeutral[server.Name] = true
	}
	var names []string
	for _, name := range enabled {
		if inNeutral[name] {
			names = append(names, name)
		} else {
			projectOnly = append(projectOnly, name)
		}
	}
	dropped := map[string]bool{}
	// stayOnList is the dropped names in list order, each once.
	stayOnList := func() (out []string) {
		seen := map[string]bool{}
		for _, name := range names {
			if dropped[name] && !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
		return out
	}
	for checks := 0; checks <= len(names); checks++ {
		var kept []string
		args = nil
		for _, name := range names {
			if !dropped[name] {
				kept = append(kept, name)
				args = append(args, "-c", "mcp_servers."+name+".enabled=false")
			}
		}
		if len(kept) == 0 {
			// Every override was refused (or none was left after the
			// project's own servers): nothing is left to check.
			return nil, true, "", stayOnList(), projectOnly, listed
		}
		check := platform.RunCli("codex", append(append([]string(nil), args...), "mcp", "list", "--json"), platform.RunOptions{Env: env, TimeoutMs: mcpCodexListTimeoutMs, Cwd: cwd})
		switch {
		case check.NotFound:
			return nil, false, "codex not found in PATH", nil, nil, nil
		case check.TimedOut:
			return nil, false, "override check timed out", nil, nil, nil
		case check.Status != nil && *check.Status == 0:
			return args, true, "", stayOnList(), projectOnly, listed
		}
		code := -1
		if check.Status != nil {
			code = *check.Status
		}
		bad := mcpCodexNamedServer(check.Stderr+"\n"+check.Stdout, kept)
		if bad == "" {
			return nil, false, fmt.Sprintf("override check exit %d", code), nil, nil, nil
		}
		dropped[bad] = true
	}
	return nil, false, "override check exit -1", nil, nil, nil
}

// codexMcpTranslate runs the codex listing and returns the off args (and the
// projectOnly/stayOn names to warn about) for keep=nil (mcp=none) or the
// list choice's names (DJA-693), plus the list names the listing does not
// report. ok=false means the listing failed: the caller prints the one
// listing-failure warning and keeps the user's servers.
func codexMcpTranslate(env platform.Env, cwd string, keep []string) (args []string, ok bool, reason string, stayOn, projectOnly, missing []string) {
	args, ok, reason, stayOn, projectOnly, listed := mcpCodexOffArgs(env, cwd, keep)
	if !ok {
		return nil, false, reason, nil, nil, nil
	}
	present := map[string]bool{}
	for _, name := range listed {
		present[name] = true
	}
	for _, name := range keep {
		if !present[name] {
			missing = append(missing, name)
		}
	}
	return args, true, "", stayOn, projectOnly, missing
}

// mcpPiListNames runs `pi mcp list --json` in the spawn's own PATH and env,
// in the worker's cwd (the same 10 s timeout as mcpCodexList) and returns
// the server names in list order. Only name is read: the rest of the
// listing never leaves this function. The reason is set when pi is not on
// the PATH, the run times out, exits nonzero, or prints non-JSON output.
func mcpPiListNames(env platform.Env, cwd string) ([]string, string) {
	res := platform.RunCli("pi", []string{"mcp", "list", "--json"}, platform.RunOptions{Env: env, TimeoutMs: mcpCodexListTimeoutMs, Cwd: cwd})
	switch {
	case res.NotFound:
		return nil, "pi not found in PATH"
	case res.TimedOut:
		return nil, "timed out after 10 s"
	case res.Status == nil || *res.Status != 0:
		code := -1
		if res.Status != nil {
			code = *res.Status
		}
		return nil, fmt.Sprintf("exit %d", code)
	}
	var out struct {
		Servers []struct {
			Name string `json:"name"`
		} `json:"servers"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(res.Stdout)), &out); err != nil {
		return nil, "non-JSON output"
	}
	names := make([]string, 0, len(out.Servers))
	for _, server := range out.Servers {
		if server.Name != "" {
			names = append(names, server.Name)
		}
	}
	return names, ""
}

// mcpNamePresent reports whether any of want is in have.
func mcpNamePresent(want, have []string) bool {
	set := map[string]bool{}
	for _, name := range have {
		set[name] = true
	}
	for _, name := range want {
		if set[name] {
			return true
		}
	}
	return false
}

type mcpCodexServer struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

// codexProjectAbove reports whether dir, or a directory above it, holds a
// .codex/config.toml other than codex's own home (CODEX_HOME, or ~/.codex):
// codex reads such a file as a project layer for any process below it, so a
// listing run there is not neutral (a TMPDIR inside the project, say). The
// home is told apart by directory identity (os.SameFile), not by spelling, so
// a case-insensitive filesystem or a symlink does not make it a project. Only
// the file's existence is checked; it is never read.
func codexProjectAbove(dir string, env platform.Env) bool {
	home := env.Get("CODEX_HOME")
	if home == "" {
		base := env.Get("HOME")
		if platform.Current() == "win32" {
			base = env.Get("USERPROFILE")
		}
		if base == "" {
			base, _ = os.UserHomeDir()
		}
		if base != "" {
			home = filepath.Join(base, ".codex")
		}
	}
	var homeInfo os.FileInfo
	if home != "" {
		homeInfo, _ = os.Stat(home)
	}
	d := canonicalPath(dir)
	for {
		layer := filepath.Join(d, ".codex")
		isHome := false
		if homeInfo != nil {
			if info, err := os.Stat(layer); err == nil && os.SameFile(info, homeInfo) {
				isHome = true
			}
		}
		if !isHome {
			if info, err := os.Stat(filepath.Join(layer, "config.toml")); err == nil && !info.IsDir() {
				return true
			}
		}
		parent := filepath.Dir(d)
		if parent == d {
			return false
		}
		d = parent
	}
}

// canonicalPath is path with its symlinks resolved, or cleaned when it does
// not resolve.
func canonicalPath(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return filepath.Clean(path)
}

// mcpCodexList runs `codex mcp list --json` in cwd and returns each server's
// name and enabled flag, nothing else; reason is non-empty when the listing
// fails (codex missing, timeout, nonzero exit, non-JSON output).
func mcpCodexList(env platform.Env, cwd string) ([]mcpCodexServer, string) {
	res := platform.RunCli("codex", []string{"mcp", "list", "--json"}, platform.RunOptions{Env: env, TimeoutMs: mcpCodexListTimeoutMs, Cwd: cwd})
	switch {
	case res.NotFound:
		return nil, "codex not found in PATH"
	case res.TimedOut:
		return nil, "timed out after 10 s"
	case res.Status == nil || *res.Status != 0:
		code := -1
		if res.Status != nil {
			code = *res.Status
		}
		return nil, fmt.Sprintf("exit %d", code)
	}
	var servers []mcpCodexServer
	if err := json.Unmarshal([]byte(strings.TrimSpace(res.Stdout)), &servers); err != nil {
		return nil, "non-JSON output"
	}
	return servers, ""
}

// mcpCodexNamedServer returns the override from kept that a line of the
// check's output names (mcpCodexLineName). No override named, or more than
// one, is "": the check's failure path. Nothing else of the output is kept.
func mcpCodexNamedServer(output string, kept []string) string {
	found := ""
	for _, line := range strings.Split(output, "\n") {
		token := mcpCodexLineName(line)
		if token == "" {
			continue
		}
		for _, name := range kept {
			if token != name {
				continue
			}
			if found != "" && found != name {
				return ""
			}
			found = name
		}
	}
	return found
}

// mcpCodexLineName returns the server a line of codex 0.163.0's error names:
// the line, without its leading and trailing white space, is exactly
// "in `mcp_servers.<name>`", and the name has no backtick and no backslash
// (which may escape a backtick that belongs to the name). Any other line names
// no server, so a name whose end is not certain never drops an override: the
// check takes its failure path instead.
func mcpCodexLineName(line string) string {
	const prefix, suffix = "in `mcp_servers.", "`"
	line = strings.TrimSpace(line)
	if len(line) <= len(prefix)+len(suffix) || !strings.HasPrefix(line, prefix) || !strings.HasSuffix(line, suffix) {
		return ""
	}
	name := line[len(prefix) : len(line)-len(suffix)]
	if strings.ContainsAny(name, "`\\") {
		return ""
	}
	return name
}

func approvalsRank(mode string) int {
	switch mode {
	case "ask":
		return 1
	case "edits":
		return 2
	case "auto":
		return 3
	case "full":
		return 4
	case "unsandboxed":
		return 5
	default:
		return 0
	}
}

func liveAgents(env platform.Env) []transport.Terminal {
	return backend.New(env).Runtime.List(context.Background(), transport.Scope{}, transport.Call{}).Value
}

// agentGetState reads one agent's state through the transport, mirroring the
// adapter's AgentStateResult: the reported state, and the cause the backend
// carried on the failure when the state read is unavailable.
func agentGetState(rt transport.Transport, name string) (state, cause string) {
	reply := rt.Get(context.Background(), transport.Target{ID: name}, transport.Call{})
	if reply.Err != nil {
		return reply.Value.State, reply.Err.Message
	}
	return reply.Value.State, ""
}

// agentReadText returns the agent screen the transport read, "" on a failed
// read — the adapter's AgentRead result.
func agentReadText(rt transport.Transport, agent, source string, lines *int) string {
	return rt.Read(context.Background(), transport.Target{ID: agent}, transport.Read{Source: source, Lines: lines}, transport.Call{}).Value
}

// paneReadText returns the pane screen the transport read and whether the
// read succeeded — the adapter's PaneRead result.
func paneReadText(rt transport.Transport, pane, source string, lines *int) (string, bool) {
	reply := rt.Read(context.Background(), transport.Target{ID: pane}, transport.Read{Source: source, Lines: lines, Pane: true}, transport.Call{})
	return reply.Value, reply.Err == nil
}

// agentKeys sends raw keys to the agent channel — the adapter's AgentSendKeys.
func agentKeys(rt transport.Transport, agent, key string) bool {
	return rt.Keys(context.Background(), transport.Target{ID: agent}, key, false, transport.Call{}).Value
}

func AgentNameTaken(name string, env platform.Env) bool {
	return AgentNameTakenIn(name, liveAgents(env))
}

// AgentNameTakenIn is AgentNameTaken over an already-read live list, so the
// read can be shared with the lane-orphan warning that runs after the name
// check without making a second agent list call.
func AgentNameTakenIn(name string, agents []transport.Terminal) bool {
	for _, a := range agents {
		if a.Name == name {
			return true
		}
	}
	return false
}

func UniqueName(base string, env platform.Env) string {
	name := base
	for i := 2; AgentNameTaken(name, env); i++ {
		name = fmt.Sprintf("%s-%d", base, i)
	}
	return name
}

func ResolvedRoleKind(role string, ctx *core.Config, env platform.Env, cwd string) string {
	return core.ResolvedRoleKind(role, ctx, env, cwd)
}

// ResolveSpawnEffort resolves the configured effort for a worker of a known kind.
func ResolveSpawnEffort(role, lane, kind string, _ int, ctx *core.Config, env platform.Env, cwd, model string) string {
	return clampResolvedEffort(core.ConfiguredRoleEffort(role, lane, kind, ctx, env, cwd), kind, model, ctx, env)
}

// legacyLaneSessionEffort preserves the original precedence for roster rows
// written before opening effort was recorded.
func legacyLaneSessionEffort(role, lane, kind string, kindLayer int, ctx *core.Config, env platform.Env, cwd, model string) string {
	effort := ""
	if lane != "" {
		layer := kindLayer
		effort = core.LaneAttr(ctx, lane, "effort", &layer, env)
	}
	if effort == "" {
		effort = core.Cfg(ctx, "role_"+strings.ReplaceAll(role, "-", "_")+"_effort", "", env)
	}
	if effort == "" {
		effort = core.Cfg(ctx, "effort_"+kind, "", env)
	}
	if effort == "" {
		if file := core.RoleFile(role, env, cwd); file != "" {
			effort = core.FmGet(file, "effort")
		}
	}
	return clampResolvedEffort(effort, kind, model, ctx, env)
}

func clampResolvedEffort(effort, kind, model string, ctx *core.Config, env platform.Env) string {
	if text.HasWord(strings.Join(core.EffortLadder, " "), effort) {
		effort = kinds.ClampTo(kinds.ClampTo(effort, kinds.KindEffortCeiling(kind)), core.Cfg(ctx, "max_effort", "", env))
		if kind == "codex" {
			effort = kinds.ClampTo(effort, kinds.CodexEffortCeiling(model, env))
		}
	}
	return effort
}

// ChooseSpawnModel decides where the spawn's model comes from. A lane's
// models list rotates by the live count of each resolved model in the roster
// of the whole state root; every other path keeps its existing model and only
// warns when it is already at the cap. The returned spec still goes through
// the kind's normal model resolution before argv and the roster see it.
func ChooseSpawnModel(flagModel string, res core.RoleSettings, ctx *core.Config, env platform.Env, cwd, kind, effort string, warn func(string, *core.Config, platform.Env, string)) (string, string) {
	capN, capped := core.ModelCap(ctx, env)
	lane := res.Lane
	if models := res.LaneModels; len(models) > 0 {
		if !capped {
			return models[0], "list"
		}
		counts := core.LiveModelCounts(ctx, env, cwd)
		full := make([]string, 0, len(models))
		for _, spec := range models {
			resolved, _ := kinds.ResolveModel(kind, spec, effort, env, func(string) {})
			count := counts[resolved]
			if count < capN {
				return spec, "list"
			}
			full = append(full, fmt.Sprintf("%s (%d)", resolved, count))
		}
		core.DieFriction(fmt.Sprintf("spawn: every model of lane '%s' is at the cap of %d live workers: %s; release a worker or raise model_cap", lane, capN, strings.Join(full, ", ")), 13, "", "")
	}
	spec, choice := modelForRequest(flagModel, res)
	if spec != "" && capped {
		resolved, _ := kinds.ResolveModel(kind, spec, effort, env, func(string) {})
		if count := core.LiveModelCounts(ctx, env, cwd)[resolved]; count >= capN {
			warn(fmt.Sprintf("spawn: model '%s' already has %d live workers (model_cap %d)", resolved, count, capN), ctx, env, "spawn")
		}
	}
	return spec, choice
}

// modelForRequest labels the unrotated model from the shared resolver.
func modelForRequest(flagModel string, res core.RoleSettings) (string, string) {
	switch {
	case len(res.LaneModels) != 0:
		return res.ModelSpec, "list"
	case flagModel != "":
		return res.ModelSpec, "flag"
	case res.Lane != "" && strings.HasPrefix(res.ModelFrom, "lane "):
		return res.ModelSpec, "lane"
	default:
		return res.ModelSpec, "default"
	}
}

func ResumeArg(kind string, args []string) string {
	var bad []string
	switch kind {
	case "claude":
		bad = []string{"-c", "--continue", "-r", "--resume"}
	case "cursor":
		bad = []string{"-c", "--cloud", "--resume"}
	}
	for _, arg := range args {
		for _, b := range bad {
			if arg == b || strings.HasPrefix(b, "--") && strings.HasPrefix(arg, b+"=") {
				return arg
			}
		}
	}
	return ""
}

// FindReusable selects an idle compatible worker from the roster, whatever
// MCP choice it was opened with.
func FindReusable(role, kind, workerCwd, name, wantModel, wantApprovals, wantEffort string, ctx *core.Config, env platform.Env, cwd string) (string, string, bool) {
	return FindReusableMcp(role, kind, workerCwd, name, wantModel, wantApprovals, "", wantEffort, ctx, env, cwd)
}

// mcpMatches reports whether a roster row's MCP choice (column 15,
// DJA-392) fits the request. A row written before that column carries no
// choice and matches any request, as reopen treats it; an empty request
// matches any row.
func mcpMatches(f []string, want string) bool {
	got := fieldAt(f, 15)
	return got == "" || want == "" || got == want
}

// FindReusableMcp is FindReusable that also requires the worker's MCP
// choice to match wantMcp (DJA-392): a session opened without the user's
// MCP servers cannot serve a role that needs them, nor the reverse.
func FindReusableMcp(role, kind, workerCwd, name, wantModel, wantApprovals, wantMcp, wantEffort string, ctx *core.Config, env platform.Env, cwd string) (string, string, bool) {
	policy := ""
	if kind == "omp" {
		var err error
		policy, err = managedOMPPolicy(env, cwd)
		if err != nil {
			return "", "", false
		}
	}
	row, unavailable, blocked := findReusableMcpAt(role, kind, workerCwd, name, wantModel, wantApprovals, wantMcp, wantEffort, policy, ctx, env, cwd, core.StateDirPath(ctx, env, cwd))
	selected, _, _ := strings.Cut(row, "\t")
	return selected, unavailable, blocked
}

// Keep the same last row per name as RosterLine, in retained roster order.
func lastRosterRows(rows []string) []string {
	last := make(map[string]int, len(rows))
	for i, row := range rows {
		name, _, _ := strings.Cut(row, "\t")
		last[name] = i
	}
	retained := rows[:0]
	for i, row := range rows {
		name, _, _ := strings.Cut(row, "\t")
		if last[name] == i {
			retained = append(retained, row)
		}
	}
	return retained
}

// Return the exact row that passed selection, so emission can verify it again.
func findReusableMcpAt(role, kind, workerCwd, name, wantModel, wantApprovals, wantMcp, wantEffort, ompPolicy string, ctx *core.Config, env platform.Env, cwd, sd string) (string, string, bool) {
	svc := backend.New(env)
	multi := core.Cfg(ctx, "multi_role", "on", env)
	blockedName, blockedCause, crossHit := "", "", ""
	for _, line := range lastRosterRows(core.RosterRows(sd)) {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		at := func(i int) string {
			if i < len(f) {
				return f[i]
			}
			return ""
		}
		nm := at(0)
		if nm == "" || name != "" && nm != name || at(2) != kind || at(6) != workerCwd || envDropMarker(f) != "" {
			continue
		}
		if !effortMatches(f, wantEffort) {
			continue
		}
		if kind == "omp" && (!ompPolicyRecorded(f) || at(9) != wantApprovals) {
			continue
		}
		isSame := at(3) == role
		wModel, wApprovals := "", ""
		if len(f) >= 9 {
			wModel = at(8)
		}
		if len(f) >= 10 {
			wApprovals = at(9)
		}
		if isSame {
			if len(f) >= 9 && wModel != wantModel {
				continue
			}
			// DJA-445: a truncated historical row cannot establish approval
			// capability. Only an explicit ask request may reuse it.
			if len(f) < 10 && wantApprovals != "ask" {
				continue
			}
			if len(f) >= 10 && approvalsRank(wApprovals) < approvalsRank(defaultValue(wantApprovals, "auto")) {
				continue
			}
		} else {
			if multi != "on" || len(f) < 11 || wantModel == "" || wModel == "" || wModel != wantModel || wApprovals == "" || approvalsRank(defaultValue(wantApprovals, "auto")) == 0 || approvalsRank(wApprovals) < approvalsRank(defaultValue(wantApprovals, "auto")) {
				continue
			}
			if core.IsReviewRole(role) && (core.RoleIsEdit(at(3), env, cwd) || core.HistoryHasEdit(at(10), env, cwd)) {
				continue
			}
		}
		args := ""
		if len(f) >= 14 {
			args = at(13)
		}
		if args != ConfigNativeArgs(kind, "", role, ctx, env, cwd, nil) {
			continue
		}
		if !mcpMatches(f, wantMcp) {
			continue
		}
		report := core.LastReport(sd, nm)
		if report != "" {
			st, err := os.Stat(report)
			if err != nil || st.Size() == 0 {
				continue
			}
		}
		state, cause := agentGetState(svc.Runtime, nm)
		if state == "unavailable" {
			if isSame && blockedName == "" {
				blockedName, blockedCause = nm, cause
			}
			continue
		}
		if state == "idle" || state == "done" {
			if kind == "omp" && !ompReuseReady(svc.Legacy, f, ompPolicy) {
				if name != "" {
					refuseOMPReuse()
				}
				continue
			}
			if isSame {
				return line, "", false
			}
			if crossHit == "" {
				crossHit = line
			}
		}
	}
	if blockedName != "" {
		return "", blockedName + "\x00" + blockedCause, true
	}
	return crossHit, "", false
}

// effortMatches requires the opening effort column, including a recorded
// empty value when the CLI chose its default. Historical rows cannot prove it.
func effortMatches(f []string, want string) bool {
	return len(f) > 14 && f[14] == want
}

func EmitReuse(name, role, kind string, ctx *core.Config, env platform.Env, cwd string) bool {
	sd := core.StateDirPath(ctx, env, cwd)
	return emitReuse(sd, core.RosterLine(sd, name), role, kind, "", "default", env, nil)
}

func emitReuse(sd, line, role, kind, qualifiedProfile, modelChoice string, env platform.Env, verify func()) bool {
	f := strings.Split(line, "\t")
	name, prev := fieldAt(f, 0), fieldAt(f, 3)
	if name == "" || envDropMarker(f) != "" || kind == "omp" && verify == nil {
		return false
	}
	if env.Get("SOHO_TRANSPORT") == "hc" {
		unlock, err := core.LockWorkerLifecycle(sd, name)
		if err != nil {
			core.DieFriction("spawn: "+err.Error(), 4, "", "spawn")
		}
		defer unlock()
	}
	if !commitReuseRole(sd, name, line, role, verify) {
		return false
	}
	// model, effort and agent_args are the roster columns the spawn recorded
	// (8, 14, 13); a missing value comes out as "".
	out := jsonjs.O("name", name, "pane_id", fieldAt(f, 1), "kind", kind, "role", role, "family", fieldAt(f, 4), "reused", true, "previous_role", prev, "effort", fieldAt(f, 14), "model", fieldAt(f, 8), "model_choice", modelChoice, "agent_args", fieldAt(f, 13), "status", "ready")
	if kind == "opencode" && env.Get("SOHO_TRANSPORT") == "hc" {
		// SOH-201 phase B: the reuse gate proved this worker reports the
		// primary profile; disclose the known profile name only, never its
		// environment.
		out.Set("launch_profile", kinds.OpenCodePrimaryProfile)
	}
	if qualifiedProfile != "" {
		out.Set("launch_profile", qualifiedProfile)
	}
	if port := core.RosterE2EPort(f); port != "" {
		out.Set("e2e_port", port)
	}
	_, _ = fmt.Fprintln(platform.Stdout, jsonjs.Stringify(out))
	return true
}

// Compare and update the last row for the name under the roster lock.
// Roster writers such as append do not take the worker lifecycle lock.
func commitReuseRole(sd, name, expected, role string, verify func()) (matched bool) {
	core.WithRosterLock(sd, func() {
		path := filepath.Join(sd, "agents.tsv")
		content, err := platform.ReadTextFile(path)
		if err != nil {
			if !os.IsNotExist(err) {
				core.DieFriction("spawn: could not read the roster for reuse: "+text.SanitizeCause(err.Error()), 4, "", "spawn")
			}
			return
		}
		lines := strings.Split(content, "\n")
		for i := len(lines) - 1; i >= 0; i-- {
			line := lines[i]
			rowName, _, _ := strings.Cut(line, "\t")
			if rowName != name || strings.HasPrefix(line, "#") {
				continue
			}
			if line != expected {
				return
			}
			if verify != nil {
				verify()
			}
			f := strings.Split(line, "\t")
			if previous := fieldAt(f, 3); previous != role {
				history := fieldAt(f, 10)
				if history == "" {
					history = previous
				} else if !slices.Contains(strings.Split(history, ","), previous) {
					history += "," + previous
				}
				if history != "" {
					history += ","
				}
				for len(f) < 11 {
					f = append(f, "")
				}
				f[3], f[10] = role, history+role
				lines[i] = strings.Join(f, "\t")
				updated := strings.Join(lines, "\n")
				if !strings.HasSuffix(updated, "\n") {
					updated += "\n"
				}
				if err := platform.AtomicWrite(path, updated); err != nil {
					core.DieFriction("spawn: could not update reused worker role: "+text.SanitizeCause(err.Error()), 4, "", "spawn")
				}
			}
			matched = true
			return
		}
	})
	return matched
}

func refuseStaleReuse(name string) {
	platform.Die(fmt.Sprintf("spawn: roster changed while checking reusable worker '%s'; retry spawn", name), 4)
}

func fieldAt(values []string, i int) string {
	if i >= 0 && i < len(values) {
		return values[i]
	}
	return ""
}

// warnStartupBlocked prints the reason the start was refused and leaves the
// pane open for the operator.
func warnStartupBlocked(name, kind, state string, ev startup.Evaluation, ctx *core.Config, env platform.Env) {
	what := "the startup is unverified"
	switch {
	case ev.Reason == "mcp-approval":
		what = "Cursor requires approval of MCP servers; the operator must answer in the pane"
	case ev.Reason == startup.ReasonSignInRequired:
		Warn(fmt.Sprintf("agent '%s' (%s) startup is not ready: Agy requires Google OAuth sign-in (state %s); no input was sent; complete sign-in in the pane", name, kind, state), ctx, env, "spawn")
		return
	case ev.Verdict == startup.VerdictBlocked:
		what = "it is showing the workspace trust dialog"
	case ev.Reason == startup.ReasonEmptyScreen:
		what = "the visible screen is empty (the TUI did not draw)"
	case ev.Reason == startup.ReasonLaunchOnly:
		what = "the visible screen shows only the shell prompt/launch line"
	case ev.Reason == startup.ReasonScreenUnavailable:
		what = "the visible screen could not be read"
	case ev.Reason == startup.ReasonStateUnavailable:
		what = "the agent state could not be read"
	}
	if hint := ev.TrustHint(name); hint != "" {
		Warn(fmt.Sprintf("agent '%s' (%s) startup is not ready: %s (state %s); no input was sent — read the pane (herdr agent read %s --source visible); %s", name, kind, what, state, name, hint), ctx, env, "spawn")
		return
	}
	Warn(fmt.Sprintf("agent '%s' (%s) startup is not ready: %s (state %s); no input was sent — read the pane (herdr agent read %s --source visible) and answer it yourself (herdr agent send-keys %s <keys>; herdr agent wait %s --timeout 60000)", name, kind, what, state, name, name, name), ctx, env, "spawn")
}

// gateReuse assesses the visible screen before an eligible reuse emits ready.
// A refusal keeps the existing roster registration and pane.
func gateReuse(svc transport.Services, name, role, kind string, ctx *core.Config, env platform.Env, cwd string) {
	sd := core.StateDirPath(ctx, env, cwd)
	gateReuseRow(svc, name, role, kind, sd, core.RosterLine(sd, name), ctx, env, cwd)
}

func gateReuseRow(svc transport.Services, name, role, kind, sd, line string, ctx *core.Config, env platform.Env, cwd string) {
	// SOH-201 phase B: a reusable native opencode worker is qualified by its
	// own terminal record, never by its display name. One Get on the
	// roster's terminal id; the record's identity must match the queried
	// target and report the primary profile, a reported status from the
	// opencode app, the live lifecycle and idle-or-done activity. Anything
	// else refuses the reuse with the fixed exit-4 line, before any role
	// change, ready emission, input or close.
	if kind == "opencode" && env.Get("SOHO_TRANSPORT") == "hc" {
		queried := transport.Target{ID: fieldAt(strings.Split(line, "\t"), 1)}
		ready := queried.ID != ""
		if ready {
			reply := svc.Runtime.Get(context.Background(), queried, transport.Call{})
			ready = reply.Valid && reply.Err == nil && reply.Value.Target == queried && kinds.OpenCodePrimaryReady(reply.Value)
		}
		if !ready {
			core.DieFriction("spawn: reusable opencode worker is not a reported primary-profile worker ready for input", 4, "", "spawn")
		}
		return
	}
	agyGate := kind == "agy" && env.Get("SOHO_TRANSPORT") != "hc"
	if !startup.Enabled(kind) && !agyGate {
		return
	}
	if line == "" {
		return
	}
	state, _ := agentGetState(svc.Runtime, name)
	reply := svc.Runtime.Read(context.Background(), transport.Target{ID: name}, transport.Read{Source: "visible"}, transport.Call{})
	screen, screenOK := reply.Value, reply.Err == nil
	ev := EvaluateCursorStartup(kind, state, screen, screenOK)
	if agyGate {
		ev = startup.EvaluateAgyLogin(screen, screenOK && reply.Valid)
	}
	if !ev.Refuses() {
		return
	}
	f := strings.Split(line, "\t")
	if !commitReuseRole(sd, name, line, fieldAt(f, 3), nil) {
		refuseStaleReuse(name)
	}
	out := jsonjs.O("name", name, "pane_id", fieldAt(f, 1), "kind", kind, "role", role, "family", fieldAt(f, 4), "reused", true, "previous_role", fieldAt(f, 3), "effort", fieldAt(f, 14), "model", fieldAt(f, 8), "agent_args", fieldAt(f, 13), "status", "blocked_at_startup")
	if port := core.RosterE2EPort(f); port != "" {
		out.Set("e2e_port", port)
	}
	out.Set("startup_evidence", jsonjs.O(startup.FieldState, state, startup.FieldReason, ev.Reason, startup.FieldSource, ev.Source))
	_, _ = fmt.Fprintln(platform.Stdout, jsonjs.Stringify(out))
	warnStartupBlocked(name, kind, state, ev, ctx, env)
	if !agyGate {
		screen = agentReadText(svc.Runtime, name, "visible", nil)
	}
	_, _ = fmt.Fprint(platform.Stdout, screen)
	platform.Die("", 7)
}

func reuseSelected(svc transport.Services, sd, line string, o spawnOptions, kind, modelChoice string, configuredArgs []string, ctx *core.Config, env platform.Env, cwd string) {
	name, _, _ := strings.Cut(line, "\t")
	if current := core.RosterLine(sd, name); current != line {
		if envDropMarker(strings.Split(current, "\t")) != "" {
			refuseSpawnEnvDrop("worker has an environment removal policy; use --fresh")
		}
		refuseStaleReuse(name)
	}
	qualifiedProfile := gateConfiguredReuse(svc.Terminal, line, o, kind, configuredArgs, env)
	gateReuseRow(svc, name, o.role, kind, sd, line, ctx, env, cwd)
	var verify func()
	if kind == "omp" {
		fields := strings.Split(line, "\t")
		verify = func() {
			if !ompReuseReady(svc.Legacy, fields, o.ompPolicyPath) {
				refuseOMPReuse()
			}
		}
	}
	if !emitReuse(sd, line, o.role, kind, qualifiedProfile, modelChoice, env, verify) {
		refuseStaleReuse(name)
	}
}

// Qualify the selected worker before any rename or report/role mutation. Reuse
// keeps its current process/configuration; this observation applies no overrides
// and does not make the subsequent operations an atomic identity reservation.
func gateConfiguredReuse(rt transport.Runtime, line string, o spawnOptions, kind string, args []string, env platform.Env) string {
	f := strings.Split(line, "\t")
	if envDropMarker(f) != "" {
		refuseSpawnEnvDrop("worker has an environment removal policy; use --fresh")
	}
	if o.launchProfile.Name == "" || env.Get("SOHO_TRANSPORT") == "hc" {
		return ""
	}
	s := configuredSpawnRequest(o, kind, args)
	s.Name, s.PaneID = fieldAt(f, 0), fieldAt(f, 1)
	if preflight, ok := rt.(transport.ConfiguredLaunchReuse); ok {
		r := preflight.PreflightConfiguredReuse(context.Background(), s, configuredSpawnCall(o))
		if r.Valid && r.Err == nil && r.Value {
			return s.LaunchProfile
		}
	}
	core.DieFriction("spawn: reusable configured worker profile is unverified", 4, "", "")
	return ""
}

func defaultValue(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

type spawnOptions struct {
	role, name, kind, direction, ratio, cwd, pane, timeout, effort, model, approvals, reuse, tabLabel, mcp, card string
	nameSet, cardSet, cwdSet, reuseSet, paneSet                                                                  bool
	force                                                                                                        bool
	native                                                                                                       []string
	envFrom                                                                                                      []string
	envDrop                                                                                                      []string
	envSnapshot                                                                                                  map[string]string
	registryDir                                                                                                  string
	blockedAtStartup                                                                                             bool
	startupDiag                                                                                                  string
	hooksBlocked                                                                                                 string
	launchProfile                                                                                                kinds.LaunchProfile
	nativeArgsSnapshot                                                                                           string
	loginShell                                                                                                   string
	// ompLaunch pins the legacy agent.start terminal until startup confirms
	// the session. The confirmed observation supplies the reuse receipt.
	ompLaunch     *ompStartupAgent
	ompPolicyPath string
	// piStartScreen is hc's post-ready screen read, reused for the thinking
	// footer so a fresh pi terminal keeps its established single-read start.
	piStartScreen      string
	piStartScreenOK    bool
	piStartScreenTried bool
}

// spawnValueOption is the authoritative option/value grammar; values may look like flags.
func spawnValueOption(arg string) bool {
	switch arg {
	case "--env-from", "--env-drop", "--tab-label", "--effort", "--model", "--approvals", "--name", "--card", "--kind", "--direction", "--ratio", "--cwd", "--pane", "--timeout", "--mcp":
		return true
	}
	return false
}

func hasSpawnEnvOption(argv []string) bool {
	for i := 1; i < len(argv); i++ {
		arg := argv[i]
		if arg == "--" {
			break
		}
		if arg == "--env-from" || arg == "--env-drop" {
			return true
		}
		if spawnValueOption(arg) {
			i++
		}
	}
	return false
}
func parseArgs(argv []string, cwd string) spawnOptions {
	o := spawnOptions{role: fieldAt(argv, 0), cwd: cwd}
	if o.role == "" {
		core.DieFriction("spawn: missing role", 2, "", "")
	}
	for i := 1; i < len(argv); i++ {
		a := argv[i]
		if a == "--" {
			o.native = append(o.native, argv[i+1:]...)
			break
		}
		switch a {
		case "--reuse":
			o.reuse, o.reuseSet = "on", true
			continue
		case "--fresh":
			o.reuse = "off"
			continue
		case "--force":
			// DJA-392: skips only the pressure refusal below (the warning
			// line still goes out); never skips any other refusal.
			o.force = true
			continue
		default:
			if !spawnValueOption(a) {
				core.DieFriction("spawn: unknown option "+a, 2, "", "")
			}
		}
		if i+1 >= len(argv) {
			if a == "--env-drop" {
				refuseSpawnEnvDrop("--env-drop expects a name")
			}
			if a == "--env-from" {
				refuseSpawnEnv()
			}
			core.DieFriction("spawn: "+a+" expects a value", 2, "", "")
		}
		v := argv[i+1]
		i++
		switch a {
		case "--env-from":
			o.envFrom = append(o.envFrom, v)
		case "--env-drop":
			o.envDrop = append(o.envDrop, v)
		case "--tab-label":
			o.tabLabel = v
		case "--mcp":
			if detail := core.McpValueError(v); detail != "" {
				core.DieFriction("spawn: invalid --mcp '"+text.SanitizeCause(v)+"': "+detail, 2, "", "")
			}
			o.mcp = v
		case "--effort":
			o.effort = v
		case "--model":
			o.model = v
		case "--approvals":
			o.approvals = v
		case "--name":
			o.name, o.nameSet = v, true
		case "--card":
			o.card, o.cardSet = cardValue(o.card, o.cardSet, v), true
		case "--kind":
			if v == "" {
				core.DieFriction("spawn: --kind expects a kind", 2, "", "")
			}
			o.kind = v
		case "--direction":
			o.direction = v
		case "--ratio":
			o.ratio = v
		case "--cwd":
			o.cwdSet = true
			o.cwd = filepath.Clean(filepath.Join(cwd, v))
			if filepath.IsAbs(v) {
				o.cwd = filepath.Clean(v)
			}
		case "--pane":
			o.pane, o.paneSet = v, true
		case "--timeout":
			o.timeout = v
		}
	}
	return o
}

var ompModelRef = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._:/@+-]*$`)

func approvalsOk(value string) bool {
	return value == "ask" || value == "edits" || value == "auto" || value == "full" || value == "unsandboxed"
}

func effortOk(value string) bool {
	return text.HasWord(strings.Join(core.EffortLadder, " "), value)
}

func CmdSpawn(argv []string, ctx *core.Config, env platform.Env, cwd string) {
	cmdSpawnWithHcStarter(argv, ctx, env, cwd, startHcAgent)
}

func cmdSpawnWithHcStarter(argv []string, ctx *core.Config, env platform.Env, cwd string, startHC func(ctx *core.Config, o *spawnOptions, kind string, env platform.Env, args []string)) {
	cmdSpawnPrepared(argv, ctx, env, cwd, startHC, false, false)
}

// PreflightFreshSpawn observes the same preparation as a real spawn, without
// allocating or retiring worker state. Admission is not a reservation: the
// actual spawn must prepare and validate again after release.
func PreflightFreshSpawn(argv []string, ctx *core.Config, env platform.Env, cwd string) {
	cmdSpawnPrepared(argv, ctx, env, cwd, startHcAgent, true, false)
}

func cmdSpawnPrepared(argv []string, ctx *core.Config, env platform.Env, cwd string, startHC func(ctx *core.Config, o *spawnOptions, kind string, env platform.Env, args []string), admissionOnly, reopenName bool) {
	callerGatePhase := false
	if admissionOnly {
		defer func() {
			value := recover()
			if value == nil {
				return
			}
			if exit, ok := value.(*platform.ExitError); ok && exit != nil && !callerGatePhase {
				// Preparation refusals must not trigger Run's deferred state write; keep caller audit metadata and shared errors intact.
				refusal := *exit
				refusal.Friction = false
				panic(&refusal)
			}
			panic(value)
		}()
	}
	var o spawnOptions
	modelChoice := "default"
	// Explicit handoff admission and snapshot precede even warning writes,
	// caller checks, naming and reuse. The snapshot map itself stays private.
	explicitEnv := hasSpawnEnvOption(argv)
	if explicitEnv {
		o = parseArgs(argv, cwd)
		prepareSpawnEnvDrop(&o, env)
		prepareSpawnEnv(&o, ctx, env, cwd)
	}
	// Classify and clean up first; only the text handed to the outer CLI
	// recovery/friction sink changes. No snapshot escapes this invocation.
	defer func() {
		if value := recover(); value != nil {
			if exit, ok := value.(*platform.ExitError); ok && exit != nil {
				if message := o.outputText(exit.Msg); message != exit.Msg {
					public := *exit
					public.Msg = message
					panic(&public)
				}
			}
			panic(value)
		}
	}()
	Warn := func(message string, ctx *core.Config, env platform.Env, command string) {
		Warn(o.outputText(message), ctx, env, command)
	}
	svc := backend.New(env)
	// Preserve the warning before ordinary argument parsing and usage errors.
	if !admissionOnly {
		core.PressureWarn(ctx, env, cwd)
	}
	if !explicitEnv {
		o = parseArgs(argv, cwd)
	}
	// Refuse invalid native arguments before starting a worker or writing its state.
	early := core.ResolveRoleSettings(o.role, ctx, env, cwd, core.RoleFlags{Kind: o.kind, Model: o.model, Effort: o.effort, Approvals: o.approvals})
	if early.Kind != "" && early.Kind != "codex" {
		native := strings.Fields(ConfigNativeArgs(early.Kind, early.Lane, o.role, ctx, env, cwd, nil))
		native = append(native, o.native...)
		if message := continueArgsError(early.Kind, o.reuse == "off", native); message != "" {
			platform.Die(message, 2)
		}
	}
	warn := func(message string, ctx *core.Config, env platform.Env, command string) {
		if !admissionOnly {
			Warn(message, ctx, env, command)
		}
	}
	if o.name = resolveSpawnName(o, reopenName, warn, ctx, env); o.name != "" && !AgentNameRE.MatchString(o.name) {
		core.DieFriction(fmt.Sprintf("invalid agent name '%s' (must match [a-z][a-z0-9_-]{0,31})", o.name), 2, "", "")
	}
	info, err := os.Stat(o.cwd)
	if err != nil || !info.IsDir() {
		core.DieFriction("spawn: --cwd "+o.cwd+" is not a directory", 2, "", "")
	}
	// The values of the flags that only the argv decides are checked here,
	// before the caller-pane check: a usage error never calls Herdr. The
	// resolved values (role, lane, config) are checked where they resolve,
	// below, with the same messages.
	if o.approvals != "" && !approvalsOk(o.approvals) {
		core.DieFriction(fmt.Sprintf("invalid approvals '%s' (ask|edits|full)", o.approvals), 2, "", "")
	}
	if o.timeout != "" {
		if _, ok := core.ParseTimeoutMS(o.timeout); !ok {
			core.DieFriction(fmt.Sprintf("spawn: --timeout expects milliseconds or a duration such as 90s, 30m or 2h, got '%s'", o.timeout), 2, "", "spawn")
		}
	}
	if o.effort != "" && !effortOk(o.effort) {
		core.DieFriction(fmt.Sprintf("invalid effort '%s' (low|medium|high|xhigh|max)", o.effort), 2, "", "")
	}
	// DJA-604 2b-2b: transport=hc has no panes, so the pane and tab flags
	// have nothing to point at. They are dropped with one warning before
	// anything can act on them; the worker is a new hc terminal of the
	// session, opened by Runtime.Start below.
	if env.Get("SOHO_TRANSPORT") == "hc" && (o.pane != "" || o.tabLabel != "") {
		warn("spawn: transport=hc has no panes; --pane and --tab-label are ignored", ctx, env, "spawn")
		o.pane = ""
		o.tabLabel = ""
	}
	if o.role == "planner" {
		core.DieFriction("spawn planner: the orchestrator is the planner and does not open a pane. Plan in this session.", 12, "", "")
	}
	// The caller-pane check runs after the arguments are valid and before
	// the first Herdr call that acts on the claimed pane (DJA-394): a
	// usage error never calls Herdr, and a forked session must not open
	// panes in the name of the pane its environment claims. It also
	// covers the run path and, for reopen, the second look of the same
	// memo (the reopen checks before it releases the worker).
	callerGatePhase = true
	core.CallerGate("spawn", ctx, env, cwd)
	callerGatePhase = false
	core.ResolveRole(o.role, env, cwd)
	flagModel := o.model
	res := core.ResolveRoleSettings(o.role, ctx, env, cwd, core.RoleFlags{Kind: o.kind, Model: o.model, Effort: o.effort, Approvals: o.approvals})
	if res.Kind == "omp" && env.Get("SOHO_TRANSPORT") == "hc" {
		core.DieFriction("spawn: managed OMP workers require the legacy Herdr transport. Use transport=herdr", 2, "", "spawn")
	}
	if env.Get("SOHO_TRANSPORT") == "hc" {
		// DJA-604 2b-2b: hc has no rename; the orchestrator keeps its
		// terminal name and nothing else happens.
		warnOnce("hc-no-rename", "spawn: transport=hc has no rename; the orchestrator keeps its terminal name", ctx, env, cwd, admissionOnly, warn)
	}
	deferRename := res.Kind == "omp"
	if res.Kind == "opencode" && env.Get("SOHO_TRANSPORT") != "hc" {
		selector, err := core.OpenCodeTUIProfile(ctx, env)
		if err != nil {
			core.DieFriction(err.Error(), 2, "", "")
		}
		deferRename = selector == "v2"
	}
	if !admissionOnly && env.Get("SOHO_TRANSPORT") != "hc" && !deferRename {
		EnsureOrchestratorName(ctx, env, cwd, "spawn")
	}
	// Explicit TUI admission precedes even warning-log writes. Legacy paths
	// still emit warnings at their original points.
	var admittedWarnings []func()
	warnBeforeAdmission := func(emit func()) {
		if admissionOnly {
			return
		} else if deferRename {
			admittedWarnings = append(admittedWarnings, emit)
		} else {
			emit()
		}
	}
	lane, kind := res.Lane, res.Kind
	for _, warning := range core.LegacyPresetGroupWarnings(ctx, env, o.role) {
		warnBeforeAdmission(func() { warn("spawn: "+warning.Message, ctx, env, "spawn") })
	}
	if lane == "" && core.LanesEnabled(ctx, env) {
		if core.PanesValue(ctx, env) == "2" && core.IsReviewRole(o.role) && !core.CustomLanesPresent(ctx, env) {
			core.DieFriction(fmt.Sprintf("spawn: with panes=2 the orchestrator reviews (pick its model family by hand); role '%s' has no lane. Use panes=3 or 4, or pane_mode=flex for a temporary reviewer.", o.role), 3, "", "")
		}
		core.DieFriction(fmt.Sprintf("spawn: role '%s' is not in any lane (panes=%s). Add it with lane.<name>.roles, or set lanes=off.", o.role, core.PanesValue(ctx, env)), 3, "", "")
	}
	if kind == "" {
		core.DieFriction(fmt.Sprintf("role %s has no default kind; pass --kind", o.role), 3, "", "")
	}
	// R316: login_shell=on starts a native worker through the caller's login
	// shell. Refused here, before any warning, reuse or terminal; the roster
	// and the JSON keep the agent's own argv.
	o.loginShell = resolveLoginShell(ctx, env, kind)
	var sources laneReuseSources
	kindSetting := sources.changeSetting("kind", o.role, res)
	if _, ok := platform.FindExecutable(kinds.KindExe(kind), env, platform.Current()); !ok {
		msg := fmt.Sprintf("executable '%s' not found in PATH; install it or change %s to select another assistant", kinds.KindExe(kind), kindSetting)
		warnBeforeAdmission(func() { Warn(msg, ctx, env, "spawn") })
	}
	if o.role == "sub-orchestrator" && kind == "codex" && !strings.Contains(core.Cfg(ctx, "args_codex", "", env), "danger-full-access") {
		warn(fmt.Sprintf("sub-orchestrator on codex: its sandbox blocks the Herdr socket (every 'herdr' call fails with Operation not permitted). Change %s to claude, or set args.codex=-s danger-full-access if you accept that.", kindSetting), ctx, env, "spawn")
	}
	o.effort, o.model, o.approvals = res.Effort, res.ModelSpec, res.Approvals
	if !approvalsOk(o.approvals) {
		core.DieFriction(fmt.Sprintf("invalid approvals '%s' (ask|edits|full)", o.approvals), 2, "", "")
	}
	// MCP settings follow the flag, the role and then the group, before
	// falling back to the role frontmatter or inherit.
	o.mcp, sources.mcp = resolveMcpSource(o.mcp, lane, o.role, ctx, env, cwd)
	sources.cwd = "default (current directory)"
	if o.cwdSet {
		sources.cwd = "flag --cwd"
	}
	// DJA-693: the value may come from a layer the flag parser never saw
	// (lane.<n>.mcp, role.<r>.mcp, the frontmatter); an invalid name exits 2
	// before any pane is opened.
	if detail := core.McpValueError(o.mcp); detail != "" {
		core.DieFriction("spawn: invalid mcp '"+text.SanitizeCause(o.mcp)+"': "+detail, 2, "", "")
	}
	o.mcp = core.McpCanonical(o.mcp)
	ompApproval := ""
	if kind == "omp" {
		if o.mcp != "inherit" || sources.mcp == "default" {
			core.DieFriction("spawn: omp requires explicit MCP inherit; this launch cannot enforce none or a server list", 2, "", "spawn")
		}
		approvalArgs, err := kinds.OMPApprovalArgs(o.approvals)
		if err != nil {
			core.DieFriction("spawn: "+err.Error(), 2, "", "spawn")
		}
		ompApproval = strings.Join(approvalArgs, " ")
		models := res.LaneModels
		if len(models) == 0 {
			models = []string{o.model}
		}
		for _, model := range models {
			if !ompModelRef.MatchString(model) {
				core.DieFriction("spawn: omp requires an exact provider/model ID", 2, "", "spawn")
			}
		}
		if o.effort == "" {
			core.DieFriction("spawn: omp requires an explicit effort (low|medium|high|xhigh|max)", 2, "", "spawn")
		}
	}
	if o.timeout == "" {
		o.timeout = core.Cfg(ctx, "spawn_timeout", "60000", env)
	} else if ms, ok := core.ParseTimeoutMS(o.timeout); !ok {
		core.DieFriction(fmt.Sprintf("spawn: --timeout expects milliseconds or a duration such as 90s, 30m or 2h, got '%s'", o.timeout), 2, "", "spawn")
	} else {
		// The value goes to `herdr agent start --timeout`, also in
		// milliseconds: a unit is converted here, so herdr never sees one.
		// A unit names the intent, so only a unitless integer warns.
		if core.IsPlainTimeoutMS(o.timeout) {
			warnBeforeAdmission(func() {
				core.WarnShortTimeout("spawn", filepath.Join(core.StateDirPath(ctx, env, cwd), "friction.log"), ms)
			})
		}
		o.timeout = strconv.FormatInt(ms, 10)
	}
	if o.effort != "" {
		if !effortOk(o.effort) {
			core.DieFriction(fmt.Sprintf("invalid effort '%s' (low|medium|high|xhigh|max)", o.effort), 2, "", "")
		}
		clamped := clampSpawnEffort(o.effort, kind, core.Cfg(ctx, "max_effort", "max", env))
		if clamped != o.effort {
			message := fmt.Sprintf("effort '%s' clamped to '%s' (kind ceiling %s, max_effort %s, requested by %s)", o.effort, clamped, kinds.KindEffortCeiling(kind), core.Cfg(ctx, "max_effort", "max", env), sources.changeSetting("effort", o.role, res))
			warnBeforeAdmission(func() { Warn(message, ctx, env, "spawn") })
			o.effort = clamped
		}
	}
	// Observe placement before applying model caps. A compatible worker
	// may match any model in an inherited list without rotating it.
	reuseDir := ""
	if !admissionOnly {
		reuseDir = core.StateDirForRead(ctx, env, cwd)
	}
	if o.reuse == "" {
		o.reuse = core.Cfg(ctx, "reuse_workers", "on", env)
	}
	native, argsSource := configNativeArgsSource(kind, lane, o.role, ctx, env, cwd, nil)
	sources.args = argsSource
	if kind == "omp" && (native != "" || len(o.native) != 0) {
		core.DieFriction("spawn: omp does not accept extra native args; set model, effort and approvals with Soho options", 2, "", "spawn")
	}
	if kind == "omp" {
		policy, err := managedOMPPolicy(env, cwd)
		if err != nil {
			core.DieFriction("spawn: "+err.Error(), 2, "", "spawn")
		}
		o.ompPolicyPath = policy
		if o.reuse == "on" && o.name != "" && o.pane == "" && reuseDir != "" {
			guardOMPReuseName(svc, reuseDir, o)
		}
	}
	request := newLaneReuseRequest(o, res, reuseDir, native, ctx, env, cwd)
	request.ompLegacy = svc.Legacy
	modelSpec, modelChoice := modelForRequest(flagModel, res)
	request.model, _ = kinds.ResolveModel(kind, modelSpec, o.effort, env, func(string) {})
	var plan laneSpawnPlan
	var capacity core.WorkerCapacity
	var refuseParts []string
	var reuseRow, unavailable string
	if !admissionOnly {
		if o.pane == "" && !o.force {
			refuseParts = core.PressureRefuseParts(ctx, env, cwd)
		}
		if lane != "" && o.pane == "" {
			plan = request.planLaneSpawn(o, res.LaneModels, svc.Runtime)
		} else if lane == "" && o.reuse == "on" && o.pane == "" && reuseDir != "" {
			fixed := request.forModel(request.model)
			reuseRow, unavailable, _ = findReusableMcpAt(o.role, kind, o.cwd, o.name, fixed.model, o.approvals, o.mcp, fixed.effort, o.ompPolicyPath, ctx, env, cwd, reuseDir)
		}
	}
	// Fixed-model profiles can be validated before taking the global
	// capacity snapshot. List rotation may change the profile's model, so
	// those requests resolve admission first and then validate that model.
	selectOpenModel := func() {
		if admissionOnly {
			return
		}
		if plan.action == "different-name" || (plan.action == "" || plan.action == "open") && reuseRow == "" && unavailable == "" && len(refuseParts) == 0 {
			capacity = core.WorkerCapacityAt(ctx, env, reuseDir, plan.decision.Gone...)
		}
		if plan.action == "different-name" {
			plan.resolveName(capacity)
		}
		if plan.action == "reuse" && plan.candidate.modelSpec != "" {
			modelSpec = plan.candidate.modelSpec
		}
		opens := (plan.action == "" || plan.action == "open") && reuseRow == "" && unavailable == ""
		nativeProfile := ""
		if kind == "opencode" {
			nativeProfile = kinds.OpenCodePrimaryProfile
		}
		canStart := env.Get("SOHO_TRANSPORT") != "hc" || kinds.NativeStatusSupported(kind, nativeProfile)
		if opens && canStart && len(refuseParts) == 0 && !capacity.Full {
			modelSpec, modelChoice = ChooseSpawnModel(flagModel, res, ctx, env, cwd, kind, request.effort, warn)
		}
	}
	if len(res.LaneModels) != 0 {
		selectOpenModel()
	}
	o.model = modelSpec
	if modelSpec != "" {
		o.model, _ = kinds.ResolveModel(kind, modelSpec, o.effort, env, func(msg string) {
			warnBeforeAdmission(func() { Warn(msg+"; check "+sources.changeSetting("model", o.role, res), ctx, env, "spawn") })
		})
	}
	if kind == "codex" && o.effort != "" {
		mc := kinds.CodexEffortCeiling(o.model, env)
		if kinds.EffortRank(o.effort) > kinds.EffortRank(mc) {
			if kinds.CodexModelCeiling(o.model, env) != "" {
				warn(fmt.Sprintf("codex model %s supports up to '%s'; effort '%s' clamped (requested by %s)", o.model, mc, o.effort, sources.changeSetting("effort", o.role, res)), ctx, env, "spawn")
			} else {
				label := o.model
				if label == "" {
					label = "(CLI default)"
				}
				warn(fmt.Sprintf("codex model %s is not in ~/.codex/models_cache.json; effort '%s' clamped to '%s' (requested by %s)", label, o.effort, mc, sources.changeSetting("effort", o.role, res)), ctx, env, "spawn")
			}
			o.effort = mc
		}
	}
	request = request.forModel(o.model)
	// SOH-201 phase B: the native hc+opencode primary profile is prepared
	// here, before any reuse decision or terminal creation, with the
	// explicitly resolved requested model and the caller's inline
	// configuration and configuration directory: empty or malformed data
	// refuses, and no other model is ever defaulted in. The configured
	// native args string is snapshotted with it, and the complete final
	// argv (the base args, the profile's extra args, the configured and
	// the user args) is validated through the shared native validator
	// before any creation or reuse emission. The launch builder runs with
	// its warnings suppressed here: the fresh path below is the single
	// source of the canonical context and effort warnings, as for every
	// other kind.
	profile, profileErr := resolveSpawnLaunchProfile(kind, o.model, o.effort, ctx, env)
	if profileErr != nil {
		core.DieFriction(profileErr.Error(), 2, "", "spawn")
	}
	preflightSpawnEnvDrop(o, kind, profile, env)
	var configuredArgs []string
	if profile.Name != "" {
		o.launchProfile = profile
		o.nativeArgsSnapshot = ConfigNativeArgs(kind, lane, o.role, ctx, env, cwd, nil)
		args := launchArgs(kind, o.approvals, o.model, o.effort, ctx, env, profile, func(msg string) {})
		if o.nativeArgsSnapshot != "" {
			args = append(args, strings.Fields(o.nativeArgsSnapshot)...)
		}
		args = append(args, o.native...)
		configuredArgs = args
		if err := kinds.ValidateNativeLaunchProfile(kind, profile.Name, args, profile.EnvOverrides); err != nil {
			core.DieFriction(err.Error(), 2, "", "spawn")
		}
		if env.Get("SOHO_TRANSPORT") != "hc" {
			preflightConfiguredSpawn(svc.Terminal, o, kind, args)
		}
	}
	if admissionOnly {
		if profile.Name == "" {
			// Legacy requests share their final argv preparation too, including
			// the existing refusal of accidental resume arguments.
			freshSpawnArgs(o, lane, kind, ctx, env, cwd, res, true)
		}
		return
	}
	if len(res.LaneModels) == 0 {
		selectOpenModel()
	}
	reused, _, _ := strings.Cut(reuseRow, "\t")
	finishAdmission := func() {
		if deferRename {
			EnsureOrchestratorName(ctx, env, cwd, "spawn")
		}
		for _, emit := range admittedWarnings {
			emit()
		}
	}
	// A managed OMP reuse or refusal keeps ancillary writes behind the
	// identity check and the exact-row commit. Stale rows are pruned when
	// a later spawn opens a worker.
	ompReadOnly := kind == "omp" && (reused != "" || unavailable != "" || capacity.Full ||
		len(refuseParts) > 0 || plan.action != "" && plan.action != "open")
	sd := reuseDir
	if !ompReadOnly {
		finishAdmission()
		sd = core.StateDir(ctx, env, cwd)
	}
	// A native registration hint must be absolute.
	if filepath.IsAbs(sd) {
		o.registryDir = sd
	}
	burst := plan.burst
	var explainMismatch func()
	var occupiedNames []string
	if plan.action != "" {
		d := plan.decision
		goneRows := d.GoneRows
		if ompReadOnly {
			goneRows = nil
		}
		for _, row := range goneRows {
			fields := strings.Split(row, "\t")
			gone := fieldAt(fields, 0)
			removed, err := core.RosterRemoveIf(sd, fieldAt(fields, 1), row)
			if err != nil {
				core.DieFriction(fmt.Sprintf("spawn: could not remove stale worker '%s': %s", gone, text.SanitizeCause(err.Error())), 4, "", "")
			}
			if core.RosterLine(sd, gone) != "" {
				core.DieFriction(fmt.Sprintf("spawn: roster changed while checking stale worker '%s'; retry spawn", gone), 4, "", "")
			}
			if removed && len(refuseParts) == 0 {
				Warn(fmt.Sprintf("lane '%s' worker '%s' is gone; removed its roster row", lane, gone), ctx, env, "spawn")
			}
		}
		line, differences := plan.candidate.line, plan.candidate.differences
		f := strings.Split(line, "\t")
		selected, actual := fieldAt(f, 0), fieldAt(f, 2)
		switch plan.action {
		case "reuse":
			reuseSelected(svc, sd, line, o, actual, modelChoice, configuredArgs, ctx, env, cwd)
			if ompReadOnly {
				finishAdmission()
			}
			sameTreeEditors(selected, o.role, fieldAt(f, 6), sd, env, cwd, ctx)
			Warn(fmt.Sprintf("reusing idle lane '%s' worker '%s' as %s; existing session context is retained", lane, selected, o.role), ctx, env, "spawn")
			return
		case "busy":
			if plan.newNameRefused {
				var limits []string
				if d.N >= d.Capacity {
					limits = append(limits, fmt.Sprintf("lane '%s' has no room for a new worker (%d of %d)", lane, d.N, d.Capacity))
				}
				if capacity.Full {
					limits = append(limits, fmt.Sprintf("max_workers=%s is at capacity (%d of %s)", capacity.Limit, capacity.Count, capacity.Limit))
				}
				reason := "an explicit new name requires a new worker"
				if env.Get("SOHO_TRANSPORT") == "hc" {
					reason = "native Core cannot rename it"
				}
				plan.busyHint = fmt.Sprintf("spawn: idle worker '%s' was not reused as '%s' because %s, and %s. Dispatch to '%s' or reuse it by its existing name, or release a slot before retrying with --fresh.", selected, o.name, reason, strings.Join(limits, " and "), selected)
			}
			busyLane(lane, plan.busyName, plan.busyHint, o.role, ctx, env, cwd, o)
		case "mismatch":
			request.reportMismatch(o, res, sources, line, differences, true)
		case "open":
			if line != "" {
				occupiedNames = d.Occupants
			}
			if len(differences) != 0 {
				explainMismatch = func() { request.reportMismatch(o, res, sources, line, request.differences(f), false) }
			} else if line != "" && env.Get("SOHO_TRANSPORT") == "hc" && o.name != "" && o.name != selected {
				warnOnce("hc-idle-not-reused:"+lane, fmt.Sprintf("spawn: idle worker '%s' was not reused as '%s' because native Core cannot rename it; starting a new worker '%s'", selected, o.name, o.name), ctx, env, cwd, admissionOnly, Warn)
			}
		case "unavailable":
			core.DieFriction(fmt.Sprintf("lane '%s' worker '%s' matches but herdr agent get failed (%s). Not spawning a replacement; it may still be live.", lane, d.Name, d.Cause), 4, "", "")
		case "locked":
			core.DieFriction(fmt.Sprintf("lane '%s' worker '%s' has edited and cannot take review role '%s'.", lane, d.Name, o.role), 5, "", "")
		default:
			core.DieFriction(fmt.Sprintf("spawn: unexpected lane decision '%s'", plan.action), 4, "", "")
		}
	}
	if reused != "" {
		previous := fieldAt(strings.Split(reuseRow, "\t"), 3)
		reuseSelected(svc, sd, reuseRow, o, kind, modelChoice, configuredArgs, ctx, env, cwd)
		if ompReadOnly {
			finishAdmission()
		}
		sameTreeEditors(reused, o.role, o.cwd, sd, env, cwd, ctx)
		if previous == o.role {
			Warn(fmt.Sprintf("reusing idle worker '%s' (%s, %s; dispatch to '%s'); existing session context is retained", reused, kind, o.role, reused), ctx, env, "spawn")
		} else {
			Warn(fmt.Sprintf("reusing idle worker '%s' (%s, was %s, now %s; dispatch to '%s'); existing session context is retained", reused, kind, previous, o.role, reused), ctx, env, "spawn")
		}
		return
	}
	if unavailable != "" {
		parts := strings.SplitN(unavailable, "\x00", 2)
		core.DieFriction(fmt.Sprintf("worker '%s' matches this role, kind and cwd but herdr agent get failed (%s). Not spawning a replacement; it may still be live.", parts[0], fieldAt(parts, 1)), 4, "", "")
	}
	// DJA-392: the harder pressure limits refuse to open a new pane (exit
	// 25). Both reuse paths returned above, so anything that reaches here
	// creates a new pane (o.pane == "") and no herdr call that creates a
	// pane or agent has run yet; a given --pane creates no pane and is
	// never refused. --force skips only this refusal (the pressure warning
	// line at the top still went out); a measurement failure never
	// refuses, and nothing is written (no friction line, no roster line).
	if o.pane == "" && len(refuseParts) > 0 {
		platform.Die(fmt.Sprintf("spawn: refusing to open a new pane under resource pressure: %s; close idle panes or run 'soho gc', or pass --force", strings.Join(refuseParts, "; ")), 25)
	}
	capacity.Enforce()
	var orphanLive []transport.Terminal
	// A worker that was just observed live still owns its name when the
	// later list is incomplete.
	nameTaken := func(name string) bool {
		return has(occupiedNames, name) || AgentNameTaken(name, env)
	}
	uniqueName := func(base string) string {
		name := base
		for i := 2; nameTaken(name); i++ {
			name = fmt.Sprintf("%s-%d", base, i)
		}
		return name
	}
	if o.name != "" {
		if nameTaken(o.name) {
			taken := o.name
			o.name = uniqueName(o.name)
			if !AgentNameRE.MatchString(o.name) {
				core.DieFriction(fmt.Sprintf("invalid agent name '%s' (must match [a-z][a-z0-9_-]{0,31})", o.name), 2, "", "")
			}
			Warn(fmt.Sprintf("agent name '%s' is taken by another pane or workspace; using '%s'", taken, o.name), ctx, env, "spawn")
		}
	} else {
		base := o.role
		o.name = uniqueName(base)
		if !AgentNameRE.MatchString(o.name) {
			core.DieFriction(fmt.Sprintf("invalid agent name '%s' (must match [a-z][a-z0-9_-]{0,31})", o.name), 2, "", "")
		}
		orphanLive = liveAgents(env)
		if AgentNameTakenIn(o.name, orphanLive) {
			core.DieFriction(fmt.Sprintf("agent name '%s' is already live", o.name), 3, "", "")
		}
		if o.name != base {
			_, _ = fmt.Fprint(platform.Stderr, o.outputText(fmt.Sprintf("soho: spawn: the new worker is '%s' ('%s' is already live); dispatch to '%s'\n", o.name, base, o.name)))
		}
	}
	native, agentArgs := freshSpawnArgs(o, lane, kind, ctx, env, cwd, res, false)
	warnFreshSpawnModelOrigin(o, kind, modelSpec, lane, res, ctx, env, cwd, warn)
	if explainMismatch != nil {
		explainMismatch()
	}
	e2ePort := ""
	var unlockE2E func()
	defer func() {
		if unlockE2E != nil {
			unlockE2E()
		}
	}()
	created, placement, autoRegrid := false, "given", false
	configuredOwned := false
	layoutMode := core.Cfg(ctx, "layout", "split", env)
	focusBefore := ""
	if env.Get("SOHO_TRANSPORT") != "hc" {
		focusBefore = layout.UIFocusedPane(env)
	}
	if o.tabLabel != "" && o.pane != "" {
		Warn("--tab-label ignored: --pane places the worker in a given pane", ctx, env, "spawn")
	}
	if env.Get("SOHO_TRANSPORT") == "hc" {
		// DJA-604 2b-2b: transport=hc has no panes to place the worker in:
		// the worker is a new hc terminal of the session that Runtime.Start
		// opens below. No focus read, no split, no herd tab, no regrid.
		created, placement = true, "hc"
	} else if o.pane == "" {
		// Pin only resource registrations, without changing the worker's
		// configuration or session lookup when its cwd is another project.
		paneEnv := env.Clone()
		paneEnv["SOHO_REGISTRY_DIR"] = sd
		// An inherited caller port is not a reservation for this worker.
		paneEnv["SOHO_SPAWN_E2E_PORT"] = ""
		delete(paneEnv, "SOHO_CREATED_E2E_PANE")
		reserveForNewShell := func() {
			if e2ePort != "" || core.Cfg(ctx, "e2e_port_base", "", env) == "" {
				return
			}
			// Placement has selected creation, not adoption. Keep the port
			// selection serialized until the worker's roster row records it.
			unlockE2E = lockE2EPorts(sd)
			e2ePort = reserveE2EPort(svc, sd, ctx, env)
			paneEnv["SOHO_SPAWN_E2E_PORT"] = e2ePort
		}
		if lane != "" {
			// The M1 idle-other-kind scan and the released-orphan scan share
			// the live read the name check just made; a named spawn (o.name
			// set) fetches it here once instead of per warning.
			if orphanLive == nil {
				orphanLive = liveAgents(env)
			}
			if explainMismatch == nil {
				warnLaneIdleOtherKind(sd, lane, kind, sources.requested("kind", o.role, res), core.LaneAttr(ctx, lane, "kind", nil, env), ctx, env, orphanLive)
			}
			// An idle pane released by this project without --close is not
			// re-adopted: the spawn only names it (from the released-panes
			// registry) before opening another pane. It reuses the live read
			// the name check just made, and spends a workspace read only when
			// a live agent matches a registry row.
			warnLaneOrphans(ctx, env, cwd, orphanLive)
		}
		anchor, autoDir := "overflow", "layout"
		if layoutMode != "tab" && o.tabLabel == "" {
			picked := layout.PickSplitAnchor(ctx, env, cwd)
			if picked != nil {
				if picked.Overflow != "" {
					anchor, autoDir = "overflow", picked.Overflow
				} else {
					anchor, autoDir = picked.Anchor, picked.Direction
				}
			}
		}
		if anchor == "overflow" && o.direction != "" && o.tabLabel == "" {
			anchor = svc.Runtime.Caller().PaneID
		}
		if anchor == "overflow" || anchor == "" {
			if layoutMode != "tab" && o.tabLabel == "" {
				Warn(fmt.Sprintf("caller tab has no room for another pane (%s); placing '%s' in a herd tab", autoDir, o.name), ctx, env, "spawn")
			}
			t := layout.HerdTabPane(ctx, o.cwd, o.tabLabel, o.role, paneEnv, cwd, func() {
				configuredOwned = true
				reserveForNewShell()
			})
			o.pane = t.Pane
			created = true
			o.direction = ""
			placement = "herd"
			autoRegrid = true
		} else {
			if o.direction == "" && o.ratio == "" {
				autoRegrid = true
			}
			if o.direction == "" {
				o.direction = autoDir
			}
			ratio := o.ratio
			if ratio == "" {
				ratio = "0.5"
			}
			reserveForNewShell()
			// The split runs with the pane's registry/E2E env, and its
			// SOHO_CREATED_E2E_PANE write-back lands in that same env.
			s := backend.New(paneEnv).Window.Split(context.Background(), transport.Open{Anchor: anchor, Direction: o.direction, Cwd: o.cwd, Ratio: &ratio}, transport.Call{})
			if s.Err != nil {
				core.DieFriction("pane split failed", 4, "", "")
			}
			o.pane = s.Value.PaneID
			created = true
			placement = "split"
			configuredOwned = true
		}
		// Herd placement can adopt an existing empty root without creating a
		// shell. Only an actual create/split exported this new reservation.
		if e2ePort != "" && paneEnv.Get("SOHO_CREATED_E2E_PANE") != o.pane {
			e2ePort = ""
		}
	}
	// Reuse paths returned above. This path starts a new agent, including
	// fresh spawns reusing an old name whose pointer predates retirement.
	if err := taskreport.RetireTaskReportPointer(sd, o.name); err != nil {
		core.DieFriction("spawn: could not retire task pointer: "+err.Error(), 4, "", "")
	}
	blocked := false
	if env.Get("SOHO_TRANSPORT") == "hc" {
		// DJA-604 2b-2b: Runtime.Start opens the new terminal and waits for
		// the worker's first reported status. Confirmed blocked starts stay
		// managed; ordinary failures close the terminal before exiting.
		startHC(ctx, &o, kind, env, agentArgs)
		if o.blockedAtStartup {
			blocked = true
		}
	} else if o.launchProfile.Name != "" {
		blocked = startConfiguredSpawn(svc, o, kind, configuredOwned, agentArgs)
	} else {
		if kind == "omp" {
			o.ompLaunch = &ompStartupAgent{}
		}
		blocked = startAgent(o, kind, env, created, agentArgs)
	}
	layout.RestoreFocusIfStolen(focusBefore, o.pane, o.direction, env)
	family := kinds.AgentFamily(kind, o.model)
	lastState, lastScreen := "", ""
	if !blocked && env.Get("SOHO_TRANSPORT") != "hc" && o.launchProfile.Name == "" {
		lastState, lastScreen, blocked = checkStartWindow(o, kind, env, created, agentArgs, ctx)
	}
	ompSetup := kind == "omp" && isOMPSetupWizard(lastScreen)
	blocked = blocked || ompSetup
	if kind == "codex" && !o.blockedAtStartup {
		if blocked {
			lastScreen = agentReadText(svc.Runtime, o.name, "visible", intPtr(40))
		}
		if isCodexUpdatePrompt(lastScreen) {
			versions := codexUpdateVersions(lastScreen)
			sent := agentKeys(svc.Runtime, o.name, "Escape")
			time.Sleep(pollMillis(env))
			lastState, lastScreen, blocked = checkStartWindow(o, kind, env, created, agentArgs, ctx)
			blocked = blocked || !sent || strings.TrimSpace(lastScreen) == "" || isCodexUpdatePrompt(lastScreen) || lastState == "blocked" || lastState == "unavailable"
			if sent && !isCodexUpdatePrompt(lastScreen) && strings.TrimSpace(lastScreen) != "" {
				_, _ = fmt.Fprintf(platform.Stderr, "soho: spawn: skipped codex's update prompt for '%s'%s; update codex yourself when no codex agent is running\n", o.name, versions)
			}
		}
	}
	if !blocked && lastState != "unavailable" {
		if p := provider.ProviderDetectTextsWithStreamGate(lastState, lastScreen, "", "", provider.NoStreamProvenance); p != nil && p.Status == "provider-error" && p.Auth {
			core.DieFriction(fmt.Sprintf("agent '%s' (%s) hit a terminal provider authentication error right after start: %s; pane %s stays open for inspection", o.name, kind, p.Cause, o.pane), 14, "", "")
		}
	}
	// #360: a fresh legacy Pi with an effective --no-mcp must still exist,
	// under the same identity, after the observation window. This runs before
	// stale-row cleanup, roster append and the ready result.
	// gh541 F3: a transport-confirmed blocked start is already a managed
	// result (screen, live row, exit 7). The ready-only Pi identity gate
	// must not turn it into an exit-4 refusal when a blocked worker has not
	// published a session identifier yet. Ready starts keep the check.
	if !blocked {
		legacyPiStartupAdmission(o, kind, env, agentArgs, lastScreen)
	}
	for _, stale := range staleRosterLines(sd, o.name, o.pane) {
		core.RosterRemove(sd, stale[0])
		Warn(fmt.Sprintf("replaced the stale roster line of '%s' (pane %s)", stale[0], stale[1]), ctx, env, "spawn")
	}
	// DJA-392 fatia 2 R2: the effective MCP choice (inherit, none or a
	// canonical list, DJA-693) has a fixed column. Reopen passes it to the
	// respawn, so a worker keeps the MCP it was opened with.
	row := []string{o.name, o.pane, kind, o.role, family, boolInt(created), o.cwd, core.NowStamp(platform.Now()), o.model, defaultValue(o.approvals, "auto"), o.role, lane, boolText(burst), native, o.effort, o.mcp}
	if e2ePort != "" || ompApproval != "" || len(o.envDrop) != 0 {
		row = append(row, e2ePort)
	}
	if ompApproval != "" || len(o.envDrop) != 0 {
		row = append(row, ompApproval)
	}
	envDrop := ""
	if len(o.envDrop) != 0 {
		envDrop = envDropPrefix + strings.Join(o.envDrop, ",")
	}
	receipt := ""
	if o.ompLaunch != nil {
		receipt = ompPolicyReceipt(o.ompPolicyPath, o.approvals, *o.ompLaunch)
	}
	if envDrop != "" || receipt != "" {
		row = append(row, envDrop)
	}
	if receipt != "" {
		row = append(row, receipt)
	}
	core.RosterAppend(sd, row)
	// A spawn that re-rosters a released pane (spawn --pane on it) retires
	// the pane's released-panes row.
	if placement == "given" {
		core.ReleasedPanesForget(ctx, env, cwd, o.pane)
	}
	sameTreeEditors(o.name, o.role, o.cwd, sd, env, cwd, ctx, o)
	if placement == "herd" {
		func() {
			defer func() {
				if recover() != nil {
					Warn("relabel of the herd tabs failed (see friction)", ctx, env, "spawn")
				}
			}()
			layout.HerdTabsRelabel(ctx, env, cwd)
		}()
	}
	// grok's first-run trust dialog must take no typed text: herdr reports the
	// agent ready while the dialog is on its screen, and the "n" of the next
	// typed line — "/context-window <value>" or a dispatch prompt — is "No,
	// quit", so the agent exits. This is the new-grok path only (every reuse
	// path returned before this point), before any text is typed, with or
	// without a configured window. A dialog on the screen blocks the start
	// like the other startup dialogs (screen printed, exit 7) — the user
	// must answer it, spawn never does. The first check reuses the visible
	// screen the start window just read of this new agent, so a grok without
	// a configured window gains no extra read; the typing path re-reads the
	// screen right before it types, because that read is one start-window
	// iteration old and a dialog can land on the screen in the meantime.
	if !blocked && kind == "grok" && isGrokTrustDialog(lastScreen) {
		blocked = true
	}
	// codex's hooks review screen (DJA-434): herdr reports the new codex
	// ready while it is on screen, and the Enter of the first dispatch
	// confirms "1. Review hooks". The check reuses the screen the start
	// window just read, so a codex without the screen gains no read. With
	// hooks_review=trust (the default) the spawn approves the hooks and
	// re-reads the screen; an approval that did not take, or
	// hooks_review=block, takes the blocked path (screen printed, exit 7)
	// with its own warning.
	hooksBlocked := o.hooksBlocked
	if !blocked && kind == "codex" && codexhooks.IsReview(lastScreen) {
		mode := core.HooksReviewMode(ctx, env)
		if mode == "trust" {
			io := codexhooks.IO{
				SendKey: func(key string) bool { return agentKeys(svc.Runtime, o.name, key) },
				Read:    func() string { return agentReadText(svc.Runtime, o.name, "visible", intPtr(40)) },
				Pause:   pollMillis(env),
			}
			if _, ok := codexhooks.Approve(lastScreen, io); ok {
				_, _ = fmt.Fprintf(platform.Stderr, "soho: %s\n", codexhooks.TrustedLine("spawn", o.name, codexhooks.Count(lastScreen)))
			} else {
				blocked = true
			}
		} else {
			blocked = true
		}
		if blocked {
			hooksBlocked = codexhooks.BlockedLine("spawn", o.name, mode)
		}
	}
	// The cursor/Windows startup gate (issue #59): herdr reports the agent
	// ready (idle, interactive_ready) while the TUI never draws — the
	// verified workspace trust dialog, or historically only the shell
	// launch line on the screen. The start window observed the screen; the
	// shared assessment takes the final verdict, and a refusing verdict
	// (blocked or unverified) takes the existing blocked path —
	// blocked_at_startup, the screen printed, exit 7 — so the spawn never
	// claims ready for those cases and never types or answers the dialog.
	// Every other kind or platform keeps the window's verdict and the JSON
	// without the evidence field.
	startupGate := false
	startupGateEv := startup.Evaluation{}
	if !blocked && startup.Enabled(kind) {
		screen, screenOK := lastScreen, true
		if screen == "" {
			// The window never saw a non-empty screen: one fresh read tells
			// an empty screen (the TUI did not draw) from a failed read.
			reply := svc.Runtime.Read(context.Background(), transport.Target{ID: o.name}, transport.Read{Source: "visible"}, transport.Call{})
			screen, screenOK = reply.Value, reply.Err == nil
		}
		startupGateEv = EvaluateCursorStartup(kind, lastState, screen, screenOK)
		startupGate = true
		if startupGateEv.Refuses() {
			warnStartupBlocked(o.name, kind, lastState, startupGateEv, ctx, env)
			blocked = true
		}
	}
	if ompSetup {
		startupGate, startupGateEv = true, startup.Evaluation{Verdict: startup.VerdictBlocked, Reason: "setup-required", Source: startup.SourceVisible}
		Warn(fmt.Sprintf("agent '%s' (omp) startup is blocked in pane %s; no input was sent; complete OMP setup in the pane before sending tasks", o.name, o.pane), ctx, env, "spawn")
	}
	agyLoginScreen := ""
	if kind == "agy" && env.Get("SOHO_TRANSPORT") != "hc" {
		reply := svc.Runtime.Read(context.Background(), transport.Target{ID: o.name}, transport.Read{Source: "visible"}, transport.Call{})
		if ev := startup.EvaluateAgyLogin(reply.Value, reply.Err == nil && reply.Valid); ev.Refuses() {
			if lastState == "" {
				lastState, _ = agentGetState(svc.Runtime, o.name)
			}
			agyLoginScreen = reply.Value
			startupGate, startupGateEv, blocked = true, ev, true
			warnStartupBlocked(o.name, kind, lastState, ev, ctx, env)
		}
	}
	contextWindow := ""
	if !blocked && kind == "grok" {
		// context_window.grok opens the fresh grok with the requested window
		// (grok has no CLI flag for it): the command goes to this new spawn
		// only — every reuse path returned before reaching it, so a reused or
		// working grok never takes the command. An invalid value is ignored
		// here; doctor names it.
		value := core.Cfg(ctx, core.ContextWindowKey("grok"), "", env)
		typing := value != "" && core.ContextWindowValueOk(value)
		if env.Get("SOHO_TRANSPORT") == "hc" && typing {
			// DJA-604 2b-2b: hc has no keys to type the command with; the
			// context window stays at grok's default, so nothing is typed.
			Warn("spawn: transport=hc cannot type /context-window into grok; the context window stays at grok's default", ctx, env, "spawn")
			typing = false
		}
		if typing {
			screen := agentReadText(svc.Runtime, o.name, "visible", intPtr(40))
			if isGrokTrustDialog(screen) {
				// The pre-typing re-read sees the trust dialog: the same block as
				// the first check — no text is typed and the start exits 7 with
				// the screen. Only the path that types the command spends this
				// read, so a grok without a configured window keeps its call
				// pattern and the frozen parity goldens stay untouched.
				lastScreen = screen
				blocked = true
			}
		}
		if !blocked && typing {
			if confirmed := confirmGrokContextWindow(svc, o, value, env); confirmed {
				contextWindow = value
			} else if state, _ := agentGetState(svc.Runtime, o.name); state == "gone" {
				// The confirmation failed and the agent is gone
				// (agent_not_found), whatever the confirmation window's reads
				// showed: it quit during the window — the typed text answered
				// a dialog. Say that, instead of the generic no-confirmation
				// warning; the status stays ready, the dispatch will name the
				// missing agent. A live grok or a transient state read keeps
				// the old warning.
				Warn(fmt.Sprintf("spawn: grok '%s' exited after /context-window; it may have been showing a dialog — read its pane", o.name), ctx, env, "spawn")
			} else {
				Warn(fmt.Sprintf("spawn: could not confirm grok's context window %s for '%s' (no \"Context window set to\" line); set it by hand with /context-window", value, o.name), ctx, env, "spawn")
			}
		}
	}
	// SOH-454: a fresh pi worker may draw a thinking level different from
	// the requested one. Read only its own screen, after the start checks
	// have established readiness, and never hold the spawn past two reads
	// inside a 2 s window. The text on screen is matched, never echoed.
	// hc already took that post-ready read during terminal start; it is
	// reused here, with a retry only for a footer caught mid-draw.
	effortEffective := ""
	if !blocked && kind == "pi" {
		readTarget := o.name
		if env.Get("SOHO_TRANSPORT") == "hc" {
			// hc addresses a transport by its terminal address, not its name.
			readTarget = o.pane
		}
		if env.Get("SOHO_TRANSPORT") == "hc" && o.piStartScreenTried {
			if o.piStartScreenOK {
				effortEffective = text.ThinkingFooterLevel(o.piStartScreen)
				if effortEffective == "" && piThinkingPartialFooter(o.piStartScreen) {
					effortEffective = readPiThinkingLevel(svc.Runtime, readTarget, time.Now, time.Sleep)
				}
			}
		} else {
			effortEffective = readPiThinkingLevel(svc.Runtime, readTarget, time.Now, time.Sleep)
		}
		if effortEffective != "" && o.effort != "" && effortEffective != o.effort {
			warnOnce("pi-thinking:"+o.model+":"+o.effort, fmt.Sprintf("spawn: pi shows thinking '%s' for '%s' where '%s' was asked; this model may not offer '%s'", effortEffective, o.name, o.effort, o.effort), ctx, env, cwd, admissionOnly, warn)
		}
	}
	out := jsonjs.O("name", o.name, "pane_id", o.pane, "kind", kind, "role", o.role, "family", family, "created_pane", created, "layout", layoutMode, "placement", placement, "effort", defaultValue(o.effort, "default"), "model", defaultValue(o.model, "default"), "model_spec", modelSpec, "model_choice", modelChoice, "approvals", defaultValue(o.approvals, "auto"), "agent_args", strings.Join(agentArgs, " "), "status", func() string {
		if blocked {
			return "blocked_at_startup"
		}
		return "ready"
	}())
	if o.launchProfile.Name == kinds.OpenCodePrimaryProfile {
		// SOH-201 phase B: disclose the known launch profile name only; the
		// profile's environment never reaches the JSON or the roster.
		out.Set("launch_profile", o.launchProfile.Name)
	}
	if e2ePort != "" {
		out.Set("e2e_port", e2ePort)
	}
	if contextWindow != "" {
		out.Set("context_window", contextWindow)
	}
	if effortEffective != "" {
		out.Set("effort_effective", effortEffective)
	}
	if burst {
		out.Set("burst", true)
	}
	if startupGate {
		out.Set("startup_evidence", jsonjs.O(startup.FieldState, lastState, startup.FieldReason, startupGateEv.Reason, startup.FieldSource, startupGateEv.Source))
	}
	_, _ = fmt.Fprintln(platform.Stdout, jsonjs.Stringify(o.outputObject(out)))
	if autoRegrid && core.Cfg(ctx, "regrid", "on", env) == "on" {
		layout.AutoRegrid(ctx, env, cwd, "regrid after spawn failed; panes left as inserted (see friction)")
	}
	if blocked {
		if o.launchProfile.Name != "" && env.Get("SOHO_TRANSPORT") != "hc" && agyLoginScreen == "" {
			core.DieFriction("spawn: configured launch not ready; target remains managed", 7, "", "")
		}
		if hooksBlocked != "" {
			Warn(hooksBlocked, ctx, env, "spawn")
		} else if !startupGate {
			// The gate already printed the precise warning when it refused
			// the start; the generic line names the other startup dialogs.
			if env.Get("SOHO_TRANSPORT") == "hc" {
				Warn(fmt.Sprintf("agent '%s' is blocked during startup (update prompt, login, trust dialog…). Screen follows; ask the user before answering it, then: hc read %s", o.name, o.pane), ctx, env, "spawn")
			} else if hint := startup.TrustHint(o.name, kind, lastScreen); hint != "" {
				Warn(fmt.Sprintf("agent '%s' is blocked during startup (update prompt, login, trust dialog…). Screen follows; %s", o.name, hint), ctx, env, "spawn")
			} else {
				Warn(fmt.Sprintf("agent '%s' is blocked during startup (update prompt, login, trust dialog…). Screen follows; ask the user before answering it, then: herdr agent send-keys %s <keys>; herdr agent wait %s --timeout 60000", o.name, o.name, o.name), ctx, env, "spawn")
			}
		}
		lines := intPtr(40)
		if kind == "cursor" {
			lines = nil
		}
		var screen string
		if ompSetup {
			// Print the same identity-checked viewport that proved the
			// refusal. There is no second diagnostic read after the window.
			screen = o.outputText(lastScreen)
		} else if agyLoginScreen != "" {
			screen = agyLoginScreen
		} else if env.Get("SOHO_TRANSPORT") == "hc" {
			reply := svc.Runtime.Read(context.Background(), transport.Target{ID: o.pane}, transport.Read{Source: "visible", Lines: lines}, transport.Call{})
			if reply.Err != nil || !reply.Valid {
				screen = o.outputText(o.startupDiag)
				if screen != "" && !strings.HasSuffix(screen, "\n") {
					screen += "\n"
				}
			} else {
				screen = o.outputText(reply.Value)
				if kind == "agy" && o.pane != "" && isAgyPromoBanner(reply.Value) {
					Warn(fmt.Sprintf("agy '%s' is showing a promotional banner (esc to dismiss); dismiss it with 'hc keys %s esc', then dispatch", o.name, o.pane), ctx, env, "spawn")
				}
			}
		} else {
			screen = agentReadText(svc.Runtime, o.name, "visible", lines)
		}
		_, _ = fmt.Fprint(platform.Stdout, screen)
		platform.Die("", 7)
	}
}

func clampSpawnEffort(effort, kind, maxEffort string) string {
	return kinds.ClampTo(kinds.ClampTo(effort, kinds.KindEffortCeiling(kind)), maxEffort)
}

// warnLaneOrphans notices the idle orphans — the panes this project released
// without --close, recorded in the released-panes registry — before the
// spawn opens a new pane; it only warns, the spawn proceeds and the orphan is
// left alone. With an empty registry it makes no extra herdr reads at all.
func warnLaneOrphans(ctx *core.Config, env platform.Env, cwd string, live []transport.Terminal) {
	if live == nil {
		live = liveAgents(env)
	}
	released := core.ReleasedPanes(ctx, env, cwd)
	if len(released) == 0 {
		return
	}
	// A workspace read is spent only when a live agent matches a registry
	// row by pane and name.
	candidate := false
	for _, row := range released {
		for _, agent := range live {
			if agent.Name == row.Name && agent.PaneID == row.Pane {
				candidate = true
				break
			}
		}
		if candidate {
			break
		}
	}
	if !candidate {
		return
	}
	// core.OrphansOf still reads the live list as field objects: rebuild it
	// from the transport terminals with the field names that function reads.
	rows := make([]any, 0, len(live))
	for _, a := range live {
		rows = append(rows, jsonjs.O("name", a.Name, "pane_id", a.PaneID, "agent_status", a.State))
	}
	for _, orphan := range core.OrphansOf(ctx, env, cwd, rows) {
		if orphan.State == "idle" || orphan.State == "done" {
			_, _ = fmt.Fprintf(platform.Stderr, "soho: spawn: '%s' is idle outside the roster (%s); close it with: soho release %s --close\n", orphan.Name, orphan.Pane, orphan.Name)
		}
	}
}

// warnLaneIdleOtherKind reports idle workers that occupy a slot and use a
// different CLI from this spawn. It uses the existing live list when available.
func warnLaneIdleOtherKind(sd, lane, kind, source, laneKind string, ctx *core.Config, env platform.Env, live []transport.Terminal) {
	if live == nil {
		live = liveAgents(env)
	}
	statusByName := map[string]string{}
	for _, a := range live {
		statusByName[a.Name] = a.State
	}
	for _, row := range core.LaneWorkers(sd, lane, ctx, env) {
		f := strings.Split(row, "\t")
		name, wk := fieldAt(f, 0), fieldAt(f, 2)
		if name == "" || wk == "" || wk == kind {
			continue
		}
		if st := statusByName[name]; st == "idle" || st == "done" {
			detail := " from " + source
			if laneKind != "" {
				detail += ", with lane." + lane + ".kind=" + diagnosticValue(laneKind)
			}
			_, _ = fmt.Fprintf(platform.Stderr, "soho: warning: lane '%s' still holds idle '%s' (%s), not the requested kind '%s'%s; release it with: soho release %s --close\n", lane, name, wk, kind, detail, name)
		}
	}
}

func has(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
func intPtr(v int) *int { return &v }
func boolInt(v bool) string {
	if v {
		return "1"
	}
	return "0"
}
func boolText(v bool) string {
	if v {
		return "burst"
	}
	return ""
}

// launchArgs is the shared launch argv builder: the kind's base args
// (context, approvals, model, effort through the shared kind policy, with
// the caller's warnings) plus the resolved launch profile's extra args. A
// named profile contributes its own model args; the native primary profile
// contributes none, because it pins the exact requested model through its
// environment instead of a generated -m/--model flag. The zero profile
// keeps the legacy buildArgs output byte for byte, for every kind.
func launchArgs(kind, approvals, model, effort string, ctx *core.Config, env platform.Env, profile kinds.LaunchProfile, warn func(string)) []string {
	out := kinds.KindContextArgs(kind, core.Cfg(ctx, "worker_context", "full", env))
	a, err := kinds.KindApprovalArgs(kind, approvals, warn)
	if err != nil {
		core.DieFriction(err.Error(), 2, "", "")
	}
	out = append(out, a...)
	if profile.Name == "" {
		out = append(out, kinds.KindModelArgs(kind, model, effort, warn)...)
	} else {
		out = append(out, profile.ModelArgs...)
	}
	out = append(out, kinds.KindEffortArgs(kind, effort, model, env, warn)...)
	out = append(out, profile.ExtraArgs...)
	return out
}

// buildArgs is the legacy launch argv for a spawn without a resolved launch
// profile (every kind but the native hc+opencode primary), kept for its
// existing callers byte for byte.
func buildArgs(kind, approvals, model, effort string, ctx *core.Config, env platform.Env) []string {
	return launchArgs(kind, approvals, model, effort, ctx, env, kinds.LaunchProfile{}, func(msg string) { Warn(msg, ctx, env, "spawn") })
}

// ResolveMcp applies the per-role MCP server precedence (DJA-392): flag >
// role.<r>.mcp > lane.<n>.mcp > the role file's mcp: > inherit.
func ResolveMcp(flag, lane, role string, ctx *core.Config, env platform.Env, cwd string) string {
	value, _ := resolveMcpSource(flag, lane, role, ctx, env, cwd)
	return value
}

func resolveMcpSource(flag, lane, role string, ctx *core.Config, env platform.Env, cwd string) (value, source string) {
	if flag != "" {
		return flag, "flag --mcp"
	}
	if v := core.Cfg(ctx, "role_"+strings.ReplaceAll(role, "-", "_")+"_mcp", "", env); v != "" {
		return v, "role." + role + ".mcp"
	}
	if lane != "" {
		if v := core.LaneAttr(ctx, lane, "mcp", nil, env); v != "" {
			return v, "lane." + lane + ".mcp"
		}
	}
	if file := core.RoleFile(role, env, cwd); file != "" {
		// DJA-693: the mcp list keeps its commas; the ordinary FmGet
		// collapses them into spaces for the other keys.
		if v := core.FmGetMcp(file); v != "" {
			return v, "role file"
		}
	}
	return "inherit", "default"
}

func busyLane(lane, name, hint, role string, ctx *core.Config, env platform.Env, cwd string, output ...spawnOptions) {
	var public spawnOptions
	if len(output) != 0 {
		public = output[0]
	}
	_, _ = fmt.Fprintln(platform.Stdout, jsonjs.Stringify(public.outputObject(jsonjs.O("status", "busy", "lane", lane, "name", name))))
	if hint == "" {
		roles := core.Cfg(ctx, "flex_roles", "reviewer,documenter", env)
		if !has(core.SplitRoles(roles), role) {
			hint = fmt.Sprintf("role '%s' may not use the temporary panel (flex_roles=%s)", role, roles)
		} else {
			live := core.LiveBurstWorkers(core.StateDirPath(ctx, env, cwd), env)
			remedy := "Raise flex_extra."
			if len(live) > 0 {
				remedy = fmt.Sprintf("Release one (release %s), or raise flex_extra.", live[0])
			}
			hint = fmt.Sprintf("lane '%s' only takes a temporary worker (pane_mode=flex) and none is free: flex_extra=%d, live temporary workers: %s. %s", lane, core.FlexExtra(ctx, env), func() string {
				if len(live) == 0 {
					return "none"
				}
				return strings.Join(live, ", ")
			}(), remedy)
		}
		if role == "" {
			// The generic lane-capacity path uses the lane occupant hint below.
			hint = fmt.Sprintf("lane '%s' is full", lane)
		}
	}
	core.Warn(public.outputText(hint), filepath.Join(core.StateDirPath(ctx, env, cwd), "friction.log"), "spawn")
	platform.Die("", 10)
}
func startAgent(o spawnOptions, kind string, env platform.Env, created bool, args []string) bool {
	svc := backend.New(env)
	if o.launchProfile.Name != "" {
		return startConfiguredSpawn(svc, o, kind, created, args)
	}
	rt := svc.Terminal
	for tries := 0; ; tries++ {
		if kind != "omp" && startup.CurrentPlatform() != "win32" && (!created || tries > 0) {
			// Send the raw character as text: a key event can still be
			// encoded by a departing agent's kitty keyboard mode.
			_ = rt.SendText(context.Background(), transport.Target{ID: o.pane}, "\x15", transport.Call{})
		}
		rep := rt.Start(context.Background(), transport.Start{Name: o.name, Kind: kind, PaneID: o.pane, Timeout: o.timeout, Args: args}, transport.Call{Timeout: time.Duration(startWrapperTimeoutMs(o.timeout)) * time.Millisecond})
		out := rep.Value
		proc := rep.Process
		if !proc.NotFound && proc.Status != nil && *proc.Status == 0 {
			if o.ompLaunch != nil {
				*o.ompLaunch = decodeOMPStartupAgent(out)
			}
			return false
		}
		if strings.Contains(out, "agent_not_ready") {
			return true
		}
		if strings.Contains(out, "agent_pane_busy") && tries < 15 {
			time.Sleep(busyRetryDelay)
			continue
		}
		if strings.TrimRight(out, "\n") != "" {
			_, _ = fmt.Fprintln(platform.Stderr, strings.TrimRight(out, "\n"))
		}
		if created {
			// DJA-416: an agent that never registered leaves nothing for
			// `agent read`; the pane read shows what the CLI printed.
			scr, read := paneReadText(svc.Runtime, o.pane, "recent-unwrapped", intPtr(40))
			if !read || strings.TrimSpace(scr) == "" {
				scr = agentReadText(svc.Runtime, o.pane, "visible", intPtr(40))
			}
			startDiagnoseShellNotFound(scr, kind)
			tail := lastLines(scr, 5)
			closed := svc.Runtime.Close(context.Background(), transport.Target{ID: o.pane}, transport.Call{}).Value
			if closed {
				core.DieFriction(fmt.Sprintf("agent start failed for %s (%s) in pane %s; closed the pane this spawn opened (last screen lines: %s)", o.name, kind, o.pane, tail), 4, "", "")
			}
			if rt.GetGone(context.Background(), transport.Target{ID: o.pane}, transport.Call{}).Value {
				core.DieFriction(fmt.Sprintf("agent start failed for %s (%s) in pane %s; the pane close failed but the pane is already gone (last screen lines: %s)", o.name, kind, o.pane, tail), 4, "", "")
			}
			core.DieFriction(fmt.Sprintf("agent start failed for %s (%s) in pane %s; pane close failed and the pane is still open, check it for a running agent (last screen lines: %s)", o.name, kind, o.pane, tail), 4, "", "")
		}
		core.DieFriction(fmt.Sprintf("agent start failed for %s (%s) in pane %s; the pane was given (--pane) and stays open: check it for a running agent", o.name, kind, o.pane), 4, "", "")
	}
}

// startHcAgent runs the worker through hc (DJA-604 2b-2b). Runtime.Start
// opens the new terminal of the session with the worker's exact argv and
// waits for its first reported status. Ready and confirmed blocked starts
// set o.pane to the returned address; blocked starts return to managed
// finalization without cleanup. name_taken moves to the next free suffix
// under the UniqueName rule, up to 10 tries. Other known failures close the
// created terminal and exit 4 without a roster row; uncertain creation
// outcomes are preserved for inspection. Retries update o.name so the row
// and JSON use the final selected name.
func startHcAgent(ctx *core.Config, o *spawnOptions, kind string, env platform.Env, args []string) {
	startHcAgentUsingServices(ctx, o, kind, env, args, backend.New(env))
}

func startHcAgentUsingServices(ctx *core.Config, o *spawnOptions, kind string, env platform.Env, args []string, svc transport.Services) {
	rt := svc.Terminal
	base := o.name
	for tries := 0; ; tries++ {
		// SOH-201 phase B: forward the resolved launch profile's name and its
		// explicit per-child env overrides; the zero profile and nil
		// overrides leave the child unchanged, and the caller's whole
		// environment never travels here.
		overrides := o.launchProfile.EnvOverrides
		if o.envSnapshot != nil {
			overrides = make(map[string]string, len(o.envSnapshot))
			for name, value := range o.envSnapshot {
				overrides[name] = value
			}
		}
		start := transport.Start{Name: o.name, Kind: kind, Cwd: o.cwd, Timeout: o.timeout, Args: args, LaunchProfile: o.launchProfile.Name, EnvOverrides: overrides, EnvDrop: o.envDrop, LoginShell: o.loginShell, RegistryDir: o.registryDir, Agent: o.name}
		if kind == "codex" {
			approved := false
			start.StartupDialog = func(callCtx context.Context, address, screen string) transport.StartupDialogAction {
				if !codexhooks.IsReview(screen) {
					return transport.StartupDialogContinue
				}
				mode := core.HooksReviewMode(ctx, env)
				if mode == "trust" && !approved {
					approved = true
					target := transport.Target{ID: address}
					call := transport.Call{Timeout: 5 * time.Second}
					io := codexhooks.IO{
						SendKey: func(key string) bool {
							r := svc.Runtime.Keys(callCtx, target, key, false, call)
							return r.Err == nil && r.Valid && r.Value
						},
						Read: func() string {
							r := svc.Runtime.Read(callCtx, target, transport.Read{Source: "visible"}, call)
							if r.Err != nil || !r.Valid {
								return screen
							}
							return r.Value
						},
						Wait: func(delay time.Duration) bool {
							timer := time.NewTimer(delay)
							defer timer.Stop()
							select {
							case <-callCtx.Done():
								return false
							case <-timer.C:
								return callCtx.Err() == nil
							}
						},
						Pause: pollMillis(env),
					}
					if _, ok := codexhooks.Approve(screen, io); ok {
						_, _ = fmt.Fprintf(platform.Stderr, "soho: %s\n", codexhooks.TrustedLine("spawn", o.name, codexhooks.Count(screen)))
						return transport.StartupDialogHandled
					}
				}
				o.hooksBlocked = codexhooks.BlockedLine("spawn", o.name, mode)
				return transport.StartupDialogBlocked
			}
		}
		if kind == "pi" {
			start.StartupDialog = func(_ context.Context, _, screen string) transport.StartupDialogAction {
				if !isPiTrustDialog(screen) {
					return transport.StartupDialogContinue
				}
				o.hooksBlocked = fmt.Sprintf("spawn: pi '%s' is waiting at its \"Trust project folder?\" dialog for %s; answer it in that terminal, or pass --approve after -- to trust project-local files for this run", o.name, o.cwd)
				return transport.StartupDialogBlocked
			}
		}
		rep := rt.Start(context.Background(), start, transport.Call{Timeout: time.Duration(startWrapperTimeoutMs(o.timeout)) * time.Millisecond})
		if rep.Err == nil {
			if kind == "pi" {
				screen := svc.Runtime.Read(context.Background(), transport.Target{ID: rep.Value}, transport.Read{Source: "visible", Lines: intPtr(40)}, transport.Call{})
				keyFailure := false
				o.piStartScreenTried = true
				if screen.Err == nil {
					o.piStartScreen, o.piStartScreenOK = screen.Value, true
				}
				if screen.Err == nil && screen.Valid {
					for _, line := range strings.Split(screen.Value, "\n") {
						if strings.Contains(line, "No API key found for") || strings.Contains(line, "Failed to resolve API key") || strings.Contains(line, "API key auth failed for provider") {
							keyFailure = true
							break
						}
					}
				}
				if keyFailure {
					closed := svc.Runtime.Close(context.Background(), transport.Target{ID: rep.Value}, transport.Call{})
					if closed.Err != nil || !closed.Value {
						Warn(o.outputText(fmt.Sprintf("spawn: could not close hc terminal %s after the failed start; it may still hold the name '%s'", rep.Value, o.name)), ctx, env, "spawn")
					}
					Warn("spawn: pi worker could not load its provider key from the Core environment; use soho config set login_shell on (or --user), or a key command that starts a shell which loads the profile. Screen follows", ctx, env, "spawn")
					_, _ = fmt.Fprint(platform.Stdout, o.outputText(screen.Value))
					platform.Die("", 7)
				}
			}
			o.pane = rep.Value
			return
		}
		if rep.Err.Kind == transport.FailureRunOutcomeUnknown {
			if rep.Value != "" {
				core.DieFriction(fmt.Sprintf("spawn: terminal creation outcome is uncertain; terminal %s preserved for manual inspection", rep.Value), 4, "", "spawn")
			}
			core.DieFriction(fmt.Sprintf("spawn: terminal creation outcome is uncertain; requested terminal '%s' preserved for manual inspection", o.name), 4, "", "spawn")
		}
		if rep.Err.Kind == transport.FailureConflict {
			if tries+1 >= 10 {
				core.DieFriction(fmt.Sprintf("spawn: no free hc terminal name for '%s' after 10 tries", base), 4, "", "spawn")
			}
			taken := o.name
			next, found := nextHcName(taken, env)
			if !found {
				core.DieFriction(fmt.Sprintf("spawn: no free hc terminal name for '%s' among the next %d suffixes", base, hcNameCandidates), 4, "", "spawn")
			}
			o.name = next
			Warn(o.outputText(fmt.Sprintf("agent name '%s' is taken by another pane or workspace; using '%s'", taken, o.name)), ctx, env, "spawn")
			continue
		}
		if rep.Err.Kind == transport.FailureBlocked {
			if rep.Value == "" {
				core.DieFriction(rep.Err.Error(), 4, "", "spawn")
			}
			o.pane = rep.Value
			o.blockedAtStartup = true
			o.startupDiag = rep.Err.Message
			return
		}
		if rep.Value != "" {
			// The readiness failure left a terminal behind; close it before
			// the die, the way the herdr path closes the pane it opened. A
			// terminal that survives holds the name, so say which one.
			closed := svc.Runtime.Close(context.Background(), transport.Target{ID: rep.Value}, transport.Call{})
			if closed.Err != nil || !closed.Value {
				Warn(o.outputText(fmt.Sprintf("spawn: could not close hc terminal %s after the failed start; it may still hold the name '%s'", rep.Value, o.name)), ctx, env, "spawn")
			}
		}
		message := rep.Err.Error()
		if rep.Err.Screen != "" && !core.Nowrite(env) {
			dir := filepath.Join(core.StateDirPath(ctx, env, o.cwd), "spawn-screens")
			if os.MkdirAll(dir, 0o700) == nil {
				path := filepath.Join(dir, o.name+"-"+time.Now().UTC().Format("20060102T150405Z")+".txt")
				if f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600); err == nil {
					_, writeErr := f.WriteString(o.outputText(rep.Err.Screen))
					closeErr := f.Close()
					if writeErr == nil && closeErr == nil {
						message += "; screen saved to " + path
					} else {
						_ = os.Remove(path)
					}
				}
			}
		}
		core.DieFriction(message, 4, "", "spawn")
	}
}

// nextHcName is the next free suffix after the taken name, under the
// UniqueName rule (base, base-2, base-3, …): when the taken name ends in a
// numeric suffix of 2 or more the sequence continues from there, otherwise
// -2 is appended. Each candidate is checked against the session's live
// names (hc names are unique on the whole core, so a taken name may belong
// to another session and not appear in the list).
func nextHcName(taken string, env platform.Env) (string, bool) {
	base, suffix := taken, 1
	if i := strings.LastIndexByte(taken, '-'); i > 0 {
		if n, err := strconv.Atoi(taken[i+1:]); err == nil && n >= 2 {
			base, suffix = taken[:i], n
		}
	}
	// Bounded: the live list is read again for each candidate and may keep
	// changing, so the search gives up instead of chasing it.
	for range hcNameCandidates {
		suffix++
		candidate := fmt.Sprintf("%s-%d", base, suffix)
		if !AgentNameTaken(candidate, env) {
			return candidate, true
		}
	}
	return "", false
}

// hcNameCandidates bounds one nextHcName search.
const hcNameCandidates = 50

func startWrapperTimeoutMs(raw string) float64 {
	n, ok := text.ParseJSNumber(raw)
	if !ok || math.IsNaN(n) || math.IsInf(n, 0) {
		return 30_000
	}
	return n + 30_000
}

// grokTrustDialogQuestion is grok's first-run trust question line and
// grokTrustDialogYes / grokTrustDialogNo its answer lines, quoted from the
// dialog on a real machine: "Do you trust the contents of this directory?"
// with "Yes, proceed" (y) and "No, quit" (n).
const (
	grokTrustDialogQuestion = "do you trust the contents of this directory"
	grokTrustDialogYes      = "yes, proceed"
	grokTrustDialogNo       = "no, quit"
)

// isGrokTrustDialog reports whether the visible screen of a fresh grok shows
// its first-run trust dialog: the question line, or both answer lines on
// distinct lines. The match is case-insensitive and tolerant of the extra
// spaces the TUI padding adds to the lines. One answer line alone is not a
// dialog, and neither is a normal sentence that cites both answers on one
// line — the dialog draws one answer per line, so a line that holds both
// answers contributes neither.
func isGrokTrustDialog(screen string) bool {
	question, yes, no := false, false, false
	for _, line := range strings.Split(screen, "\n") {
		norm := strings.Join(strings.Fields(text.ASCIILower(line)), " ")
		if strings.Contains(norm, grokTrustDialogQuestion) {
			question = true
		}
		hasYes, hasNo := strings.Contains(norm, grokTrustDialogYes), strings.Contains(norm, grokTrustDialogNo)
		if hasYes && !hasNo {
			yes = true
		}
		if hasNo && !hasYes {
			no = true
		}
	}
	return question || (yes && no)
}

// confirmGrokContextWindow opens the requested context window on the fresh
// grok: it types /context-window <value> into the pane, pauses for the menu,
// sends the Enter, and then reads the visible screen until grok's
// "Context window set to <value>" line appears (up to the window). It is
// true only when the line was seen — spawn records the window in its JSON;
// otherwise it warns and the spawn still succeeds. When it is false the
// caller spends one agent state read: a gone agent (agent_not_found) gets
// the exit warning, a live one or a transient state read keeps the generic
// warning. Callers run it on the new-spawn path only, after the start
// window; a reused or working grok never gets it.
func confirmGrokContextWindow(svc transport.Services, o spawnOptions, value string, env platform.Env) bool {
	_ = svc.Terminal.SendText(context.Background(), transport.Target{ID: o.pane}, "/context-window "+value, transport.Call{})
	time.Sleep(contextWindowSettle)
	_ = svc.Runtime.Keys(context.Background(), transport.Target{ID: o.pane}, "Enter", true, transport.Call{})
	needle := "Context window set to " + value
	until := time.Now().Add(contextWindowWindow)
	for {
		if strings.Contains(agentReadText(svc.Runtime, o.name, "visible", intPtr(40)), needle) {
			return true
		}
		if !time.Now().Before(until) {
			return false
		}
		time.Sleep(pollMillis(env))
	}
}

func updateGoneWaitMs(raw string) float64 {
	n, ok := text.ParseJSNumber(raw)
	if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n <= 0 {
		return 60_000
	}
	return n
}
func pollMillis(env platform.Env) time.Duration {
	n, err := strconv.Atoi(env.Get("SOHO_WAIT_POLL_MS"))
	if err == nil && n >= 1 && n <= 3000 {
		return time.Duration(n) * time.Millisecond
	}
	return 500 * time.Millisecond
}

type legacyPiIdentity struct {
	name, pane, kind, session string
}

// legacyPiNoMcpOption reads the assembled options, stopping at the first
// end-of-options marker. A literal flag in native args is not an effective
// spawn option.
func legacyPiNoMcpOption(args []string) bool {
	for _, token := range args[:optionsEnd(args)] {
		if token == "--no-mcp" {
			return true
		}
	}
	return false
}

func legacyPiErrorCode(raw string) string {
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(raw), &envelope) != nil {
		return ""
	}
	return envelope.Error.Code
}

func readLegacyPiIdentity(svc transport.Services, o spawnOptions, deadline time.Time) (legacyPiIdentity, string) {
	var id legacyPiIdentity
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	rep := svc.Legacy.AgentGetRaw(ctx, transport.Target{ID: o.name}, transport.Call{Timeout: herdr.Timeout})
	raw := rep.Process.Stderr
	if raw == "" {
		raw = rep.Value
	}
	code := legacyPiErrorCode(raw)
	if code == "agent_not_found" {
		return id, "gone"
	}
	if !rep.Valid || rep.Err != nil || code != "" {
		return id, "unavailable"
	}
	var body struct {
		Result struct {
			Agent struct {
				Name    string `json:"name"`
				Pane    string `json:"pane_id"`
				Kind    string `json:"agent"`
				Session struct {
					Value string `json:"value"`
				} `json:"agent_session"`
			} `json:"agent"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(rep.Value), &body); err != nil {
		return id, "unavailable"
	}
	live := body.Result.Agent
	if live.Name != o.name || live.Kind != "pi" || o.pane != "" && live.Pane != o.pane {
		return id, "mismatch"
	}
	if live.Session.Value == "" {
		return id, "Herdr reported no Pi session identity. Load the Herdr Pi integration and retry. Install it with 'herdr integration install pi' if needed"
	}
	return legacyPiIdentity{name: live.Name, pane: live.Pane, kind: live.Kind, session: live.Session.Value}, ""
}

// legacyPiStartupAdmission is a bounded two-read check of the launched
// worker. It never kills, closes, unlinks or relaunches: on an unproved
// identity the pane is left for inspection and no roster row is written.
func legacyPiStartupAdmission(o spawnOptions, kind string, env platform.Env, agentArgs []string, lastScreen string) {
	if env.Get("SOHO_TRANSPORT") == "hc" || kind != "pi" || o.launchProfile.Name != "" || !legacyPiNoMcpOption(agentArgs) {
		return
	}
	gone := func() {
		core.DieFriction(fmt.Sprintf("agent '%s' (%s) exited right after start; last screen lines: %s", o.name, kind, lastLines(lastScreen, 5)), 4, "", "")
	}
	unknown := func(reason string) {
		core.DieFriction(fmt.Sprintf("spawn: pi worker '%s' could not be confirmed after start in pane %s (%s); nothing was registered; the pane stays open for inspection", o.name, o.pane, reason), 4, "", "")
	}
	svc := backend.New(env)
	deadline := time.Now().Add(startPollWindow)
	first, reason := readLegacyPiIdentity(svc, o, deadline)
	if reason == "gone" {
		gone()
	}
	if reason != "" {
		unknown(reason)
	}
	if remaining := time.Until(deadline); remaining > 0 {
		pause := pollMillis(env)
		if pause > remaining {
			pause = remaining
		}
		if pause > 0 {
			time.Sleep(pause)
		}
	}
	second, reason := readLegacyPiIdentity(svc, o, deadline)
	if reason == "gone" {
		gone()
	}
	if reason != "" {
		unknown(reason)
	}
	if second != first {
		unknown("the worker identity changed during the startup check")
	}
}

func checkStartWindow(o spawnOptions, kind string, env platform.Env, created bool, args []string, ctx *core.Config) (string, string, bool) {
	svc := backend.New(env)
	if kind == "omp" {
		return observeOMPStart(o, svc, time.Now().Add(startPollWindow))
	}
	rt := svc.Runtime
	poll := time.Duration(PollIntervalMs(env)) * time.Millisecond
	canRelaunch := true
	blocked := false
	for {
		end := time.Now().Add(startPollWindow)
		lastScreen, lastState := "", ""
		marker := false
		for {
			lastState, _ = agentGetState(rt, o.name)
			lines := intPtr(40)
			if kind == "cursor" {
				lines = nil
			}
			screen := agentReadText(rt, o.name, "visible", lines)
			if screen != "" {
				lastScreen = screen
			}
			if kind == "codex" && (strings.Contains(screen, "Updating Codex via") || strings.Contains(screen, "Please restart Codex")) {
				marker = true
			}
			if lastState == "gone" {
				if canRelaunch && marker {
					break
				}
				if len(strings.TrimSpace(lastScreen)) == 0 {
					lastScreen = agentReadText(rt, o.name, "visible", intPtr(40))
				}
				// DJA-416: still nothing from the agent; read the pane.
				// Only a pane this spawn opened: a given --pane may show
				// another occupant's screen.
				if len(strings.TrimSpace(lastScreen)) == 0 && created && o.pane != "" {
					if pane, read := paneReadText(rt, o.pane, "recent-unwrapped", intPtr(40)); read {
						lastScreen = pane
					}
				}
				providerErr := provider.ProviderDetectTextsWithStreamGate("gone", lastScreen, "", "", provider.NoStreamProvenance)
				if providerErr != nil && providerErr.Status == "provider-error" && providerErr.Auth {
					core.DieFriction(fmt.Sprintf("agent '%s' (%s) hit a terminal provider authentication error right after start: %s; pane %s stays open for inspection", o.name, kind, providerErr.Cause, o.pane), 14, "", "")
				}
				core.DieFriction(fmt.Sprintf("agent '%s' (%s) exited right after start; last screen lines: %s", o.name, kind, lastLines(lastScreen, 5)), 4, "", "")
			}
			if marker && canRelaunch {
				break
			}
			// The cursor/Windows startup gate (issue #59): the verified
			// trust dialog stops the observation immediately, and a blank or
			// launch-only screen is re-probed until the window ends (the TUI
			// can draw late) instead of ending it on the first probe; every
			// other kind or platform returns exactly as before.
			if startup.Enabled(kind) && startup.ActiveTrustDialog(screen) {
				return lastState, lastScreen, blocked
			}
			hold := startup.Enabled(kind) && (strings.TrimSpace(screen) == "" || startup.LaunchOnly(screen))
			if (!marker && !hold) || !time.Now().Before(end) {
				return lastState, lastScreen, blocked
			}
			time.Sleep(poll)
		}
		to := updateGoneWaitMs(o.timeout)
		until := time.Now().Add(time.Duration(to * float64(time.Millisecond)))
		state, _ := agentGetState(rt, o.name)
		for state != "gone" && time.Now().Before(until) {
			time.Sleep(poll)
			state, _ = agentGetState(rt, o.name)
		}
		if state != "gone" {
			return state, lastScreen, blocked
		}
		blocked = startAgent(o, kind, env, created, args)
		Warn(fmt.Sprintf("'%s' (%s) updated itself at start and exited; started it again", o.name, kind), ctx, env, "spawn")
		canRelaunch = false
	}
}
func lastLines(s string, n int) string {
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		if v := strings.TrimSpace(line); v != "" {
			lines = append(lines, v)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " / ")
}
func staleRosterLines(sd, name, pane string) [][2]string {
	seen := map[string]bool{}
	var out [][2]string
	for _, line := range core.RosterRows(sd) {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		nm, pn := fieldAt(f, 0), fieldAt(f, 1)
		if nm == "" || seen[nm] {
			continue
		}
		if nm == name || pane != "" && pn == pane {
			seen[nm] = true
			out = append(out, [2]string{nm, pn})
		}
	}
	return out
}

func sameTreeEditors(thisName, role, workerCwd, sd string, env platform.Env, cwd string, ctx *core.Config, output ...spawnOptions) {
	if workerCwd == "" || !core.RoleIsEdit(role, env, cwd) {
		return
	}
	type candidate struct{ name, pane string }
	var candidates []candidate
	for _, line := range core.RosterRows(sd) {
		f := strings.Split(line, "\t")
		name := fieldAt(f, 0)
		if name == "" || name == thisName || !core.RoleIsEdit(fieldAt(f, 3), env, cwd) || fieldAt(f, 6) != workerCwd {
			continue
		}
		candidates = append(candidates, candidate{name, fieldAt(f, 1)})
	}
	if len(candidates) == 0 {
		return
	}
	var agents []transport.Terminal
	func() {
		defer func() { _ = recover() }()
		agents = liveAgents(env)
	}()
	for _, cand := range candidates {
		for _, agent := range agents {
			if agent.Name != cand.name || cand.pane != "" && agent.PaneID != cand.pane {
				continue
			}
			if !sharedTreeWarnDue(sd, thisName, cand.name, workerCwd, ctx, env, output...) {
				break
			}
			warnSpawnOutput(output, fmt.Sprintf("'%s' and '%s' both edit %s: builds and test runs see each other's changes in progress; give each a git worktree (spawn --cwd <worktree>) to isolate them", thisName, cand.name, workerCwd), ctx, env)
			break
		}
	}
}

// sharedTreeWarnDue records the shared-tree warn of one unordered pair in
// one cwd as a wait marker (shared-tree-<hash>.warned, written like the
// other state markers) and reports whether the warn is still due: the same
// pair in the same cwd stays silent, a new pair or another cwd warns again.
// The marker lives in the workspace's wait dir; the state dir only reached
// spawn through core.StateDir, whose inside-the-skill refusal dies before any
// write, so a marker is never created inside the skill.
func sharedTreeWarnDue(sd, a, b, workerCwd string, ctx *core.Config, env platform.Env, output ...spawnOptions) bool {
	first, second := a, b
	if second < first {
		first, second = second, first
	}
	stored := first + "\x00" + second + "\x00" + workerCwd
	if len(output) != 0 {
		stored = output[0].outputText(stored)
	}
	sum := fnv.New64a()
	_, _ = sum.Write([]byte(stored))
	marker := filepath.Join(sd, "wait", "shared-tree-"+fmt.Sprintf("%016x", sum.Sum64())+".warned")
	if prev, err := platform.ReadTextFile(marker); err == nil && strings.TrimRight(prev, "\n") == stored {
		return false
	}
	if err := os.MkdirAll(filepath.Join(sd, "wait"), 0o777); err != nil {
		warnSpawnOutput(output, fmt.Sprintf("spawn: could not record the shared-tree warning marker: %s", text.SanitizeCause(err.Error())), ctx, env)
		return true
	}
	if err := os.WriteFile(marker, []byte(stored+"\n"), 0o666); err != nil {
		warnSpawnOutput(output, fmt.Sprintf("spawn: could not record the shared-tree warning marker: %s", text.SanitizeCause(err.Error())), ctx, env)
		return true
	}
	return true
}

// PollIntervalMs is the shared wait poll override used by spawn start checks.
func PollIntervalMs(env platform.Env) int {
	d := pollMillis(env)
	if d > 500*time.Millisecond {
		return 500
	}
	return int(d.Milliseconds())
}

// e2ePortAvailable is the native host-free probe; tests may inject host outcomes
// while exercising the public spawn path when the sandbox forbids listeners.
var e2ePortAvailable = func(port int) error {
	listener, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		return err
	}
	return listener.Close()
}

// lockE2EPorts takes the bounded e2e-ports.lock directory: an existing
// lock, and on Windows only a delete-pending permission error, are
// retried until the bound, and the bound exits 4 naming the last creation
// error. The delete-pending rule is shared with the other soho locks
// through platform.DeletePending.
func lockE2EPorts(sd string) func() {
	unlock, err := acquireE2EPortLock(sd, os.Mkdir, runtime.GOOS, time.Sleep)
	if err != nil {
		core.DieFriction("spawn: "+err.Error(), 4, "", "spawn")
	}
	return unlock
}

// acquireE2EPortLock runs the bounded e2e-ports lock acquisition with the
// named creation, pause and platform; production passes os.Mkdir, the 50 ms
// poll and the host GOOS. An existing lock and, on Windows only, a
// delete-pending permission error are retried until the 199-try bound, which
// returns the original creation error.
func acquireE2EPortLock(sd string, mkdir func(string, os.FileMode) error, goos string, pause func(time.Duration)) (func(), error) {
	path := filepath.Join(sd, "e2e-ports.lock")
	for tries := 0; ; tries++ {
		err := mkdir(path, 0700)
		if err == nil {
			return func() { _ = os.Remove(path) }, nil
		}
		pending := platform.DeletePending(err, goos)
		if (!os.IsExist(err) && !pending) || tries >= 199 {
			return nil, err
		}
		pause(50 * time.Millisecond)
	}
}

func reserveE2EPort(svc transport.Services, sd string, ctx *core.Config, env platform.Env) string {
	raw := core.Cfg(ctx, "e2e_port_base", "", env)
	if !core.ConfigValueOk("e2e_port_base", raw, env, "") {
		core.DieFriction("spawn: invalid e2e_port_base", 2, "", "spawn")
	}
	base, _ := strconv.Atoi(raw)
	used := map[string]bool{}
	for _, row := range core.RosterRows(sd) {
		fields := strings.Split(row, "\t")
		if port := core.RosterE2EPort(fields); port != "" {
			state, _ := agentGetState(svc.Runtime, fieldAt(fields, 0))
			if state != "gone" {
				used[port] = true
			}
		}
	}
	for port := base; port < base+200; port++ {
		if used[strconv.Itoa(port)] {
			continue
		}
		if err := e2ePortAvailable(port); err == nil {
			return strconv.Itoa(port)
		}
	}
	core.DieFriction("spawn: no free E2E port in the 200-port range from e2e_port_base="+raw, 4, "", "spawn")
	return ""
}

// freshSpawnArgs is the shared final legacy/native argv preparation.
func freshSpawnArgs(o spawnOptions, lane, kind string, ctx *core.Config, env platform.Env, cwd string, res core.RoleSettings, admissionOnly bool) (string, []string) {
	warn := func(message string, ctx *core.Config, env platform.Env, command string) {
		if !admissionOnly {
			Warn(o.outputText(message), ctx, env, command)
		}
	}
	if kind == "grok" && kinds.GrokClaudeHooksRisk(env, platform.ProjectRoot(env, o.cwd)) == kinds.GrokHooksWarn {
		warnOnce("grok-claude-hooks", kinds.GrokClaudeHooksWarningText, ctx, env, o.cwd, admissionOnly, warn)
	}
	var native string
	if o.launchProfile.Name != "" || kind == "omp" {
		// The profile snapshots its native args. OMP admitted only empty
		// native args. Neither path re-reads them after admission.
		native = o.nativeArgsSnapshot
	} else {
		native = ConfigNativeArgs(kind, lane, o.role, ctx, env, cwd, func(key, own string) {
			if own == "" {
				own = "?"
			}
			warn(fmt.Sprintf("%s not passed: those args belong to kind %s and this spawn runs %s", key, own, kind), ctx, env, "spawn")
		})
	}
	approvalsForArgs := o.approvals
	if kind == "codex" {
		nativeArgs := append(strings.Fields(native), o.native...)
		if flag := kinds.CodexApprovalOverrideFlag(nativeArgs); flag != "" {
			approvalsForArgs = "ask"
			warn(fmt.Sprintf("spawn: codex args already set the sandbox or approvals (%s); approvals=%s adds nothing for '%s'", flag, o.approvals, o.name), ctx, env, "spawn")
		}
	}
	// Only spawn's sandbox fallback is a once-per-session note: kinds keeps
	// warning on its other callers. The exact text of this fallback is the
	// only handle the warn callback has to distinguish it.
	sandboxNote := kind + " has no separate sandbox switch; approvals=unsandboxed runs as full"
	launchWarn := func(msg string) {
		if msg == sandboxNote {
			warnOnce("unsandboxed-no-switch:"+kind, msg, ctx, env, cwd, admissionOnly, warn)
			return
		}
		var sources laneReuseSources
		if strings.HasPrefix(msg, "cursor ignores --effort without --model") {
			msg += "; set " + sources.changeSetting("model", o.role, res)
		} else if strings.HasPrefix(msg, "no effort mapping for kind ") {
			msg += "; requested by " + sources.changeSetting("effort", o.role, res)
		}
		warn(msg, ctx, env, "spawn")
	}
	args := launchArgs(kind, approvalsForArgs, o.model, o.effort, ctx, env, o.launchProfile, launchWarn)
	if o.ompPolicyPath != "" {
		args = append(args, "--config", o.ompPolicyPath)
	}
	// DJA-392/DJA-693: the mcp choice translates right after the effort args
	// (buildArgs ends with them) and before the config and user args; a kind
	// without a per-process flag warns once and the agent keeps the user's
	// MCP servers. inherit and an absent choice change no argv, byte for
	// byte. Codex builds its off args per server from its live listing (R2):
	// -c mcp_servers=<name>.enabled=false for every enabled server not kept,
	// in list order; a failed listing warns once and the agent keeps the
	// user's MCP servers. A list keeps the named servers (DJA-693): codex
	// turns the others off with the same technique (a name the listing does
	// not report warns and the spawn continues), pi reads `pi mcp list
	// --json` (--no-mcp when none of the names is present, the cannot-limit
	// warning when at least one is, the listing-failure warning when the
	// read fails), claude needs the server definition in JSON (not proved) so
	// it keeps the servers with the cannot-limit warning, and every other
	// kind keeps the servers with the mcp=none no-flag warning.
	switch {
	case o.mcp == "inherit":
		// inherit changes nothing.
	case kind == "codex" && (o.mcp == "none" || len(core.McpNames(o.mcp)) > 0):
		keep := core.McpNames(o.mcp) // nil for mcp=none
		// SOH-253: Apps/connector tools are a source of worker context apart
		// from mcp_servers.*; a restricted launch turns them off per process,
		// before the per-server args and whatever the listing returns.
		args = append(args, "-c", "features.apps=false")
		mcpArgs, ok, reason, stayOn, projectOnly, missing := codexMcpTranslate(env, o.cwd, keep)
		stayOnTail := "they stay on, the others are off"
		if len(keep) > 0 {
			stayOnTail = "they stay on; the others outside the list are off"
		}
		if !ok {
			warn(fmt.Sprintf("spawn: codex could not list its MCP servers (%s); '%s' keeps the user's MCP servers", reason, o.name), ctx, env, "spawn")
		} else {
			args = append(args, mcpArgs...)
			if len(projectOnly) > 0 {
				warnOnce("codex-mcp-project-only:"+o.cwd+":"+strings.Join(projectOnly, ","), fmt.Sprintf("spawn: codex cannot turn off MCP server(s) %s for '%s' (they are defined only in the project's .codex/config.toml); %s", strings.Join(projectOnly, ", "), o.name, stayOnTail), ctx, env, cwd, admissionOnly, warn)
			}
			if len(stayOn) > 0 {
				warnOnce("codex-mcp-stay-on:"+strings.Join(stayOn, ","), fmt.Sprintf("spawn: codex cannot turn off MCP server(s) %s for '%s' (they are not defined in its config.toml); %s", strings.Join(stayOn, ", "), o.name, stayOnTail), ctx, env, cwd, admissionOnly, warn)
			}
			for _, name := range missing {
				warn(fmt.Sprintf("spawn: %s has no MCP server named %s for '%s'", kind, name, o.name), ctx, env, "spawn")
			}
		}
	case len(core.McpNames(o.mcp)) > 0:
		// DJA-693: a list on a kind without codex's per-server overrides.
		// SOH-453: the cannot-limit warnings repeat once per session per
		// requested list, independent of the order of its names and of the
		// worker name in the message.
		names := slices.Clone(core.McpNames(o.mcp))
		slices.Sort(names)
		keyNames := strings.Join(names, ",")
		switch kind {
		case "pi":
			present, reason := mcpPiListNames(env, o.cwd)
			if reason != "" {
				warn(fmt.Sprintf("spawn: pi could not list its MCP servers (%s); '%s' keeps the user's MCP servers", reason, o.name), ctx, env, "spawn")
			} else if !mcpNamePresent(core.McpNames(o.mcp), present) {
				args = append(args, "--no-mcp")
			} else {
				warnOnce("pi-mcp-list:"+keyNames, fmt.Sprintf("spawn: pi cannot limit its MCP servers to the requested list per process; '%s' keeps the user's MCP servers", o.name), ctx, env, cwd, admissionOnly, warn)
			}
		case "claude":
			warnOnce("claude-mcp-list:"+keyNames, fmt.Sprintf("spawn: claude cannot limit its MCP servers to the requested list per process; '%s' keeps the user's MCP servers", o.name), ctx, env, cwd, admissionOnly, warn)
		default:
			warn(fmt.Sprintf("spawn: %s cannot turn MCP servers off per process; '%s' keeps the user's MCP servers", kind, o.name), ctx, env, "spawn")
		}
	default:
		// mcp=none on a kind with a static flag (claude, pi), or without any
		// (the kind keeps the user's servers with the one-line warning).
		if mcpArgs, ok := kinds.KindMcpArgs(kind, o.mcp); !ok {
			warn(fmt.Sprintf("spawn: %s cannot turn MCP servers off per process; '%s' keeps the user's MCP servers", kind, o.name), ctx, env, "spawn")
		} else {
			args = append(args, mcpArgs...)
		}
	}
	if kind == "codex" && core.TrustCwdMode(ctx, env) == "on" {
		root, reason := trustRootEnv(o.cwd, env, trustOwned)
		if reason == "" {
			args = append(args, "-c", trustCwdArgument(root))
		} else {
			if !admissionOnly {
				_, _ = fmt.Fprintf(platform.Stderr, "spawn: trust_cwd: %s; '%s' starts without a trust override (codex may ask to trust the folder)\n", reason, o.name)
			}
		}
	}
	if kind == "codex" && !hasCodexUpdateConfig(append(strings.Fields(native), o.native...)) {
		args = append(args, "-c", "check_for_update_on_startup=false")
	}
	if native != "" {
		tokens := strings.Fields(native)
		// Trailing native args win: a --remote there cannot run with
		// --no-daemon, and a --no-daemon there needs no second one. Only
		// the options go; a configured prompt after "--" stays as written.
		if kind == "codex" && (hasNativeToken(o.native, "--remote") || hasNativeToken(o.native, "--no-daemon")) {
			end := optionsEnd(tokens)
			tokens = append(slices.DeleteFunc(slices.Clone(tokens[:end]), func(token string) bool { return token == "--no-daemon" }), tokens[end:]...)
		}
		args = append(args, tokens...)
	}
	agentArgs := append(args, o.native...)
	if resume := ResumeArg(kind, agentArgs); resume != "" {
		core.DieFriction(fmt.Sprintf("spawn: '%s' would make %s resume an earlier session instead of starting a new one (for claude, -c is --continue: it resumes the orchestrator's conversation in this cwd). Remove it from the native args; codex-only flags belong in args.codex", resume, kind), 2, "", "")
	}
	return native, agentArgs
}

// CmdSpawnReopen starts the replacement worker. Its recorded name does not
// need the naming advisory for a new name.
func CmdSpawnReopen(argv []string, ctx *core.Config, env platform.Env, cwd string) {
	cmdSpawnPrepared(argv, ctx, env, cwd, startHcAgent, false, true)
}

// PreflightFreshSpawnReopen checks the replacement without starting a worker.
func PreflightFreshSpawnReopen(argv []string, ctx *core.Config, env platform.Env, cwd string) {
	cmdSpawnPrepared(argv, ctx, env, cwd, startHcAgent, true, true)
}

// warnFreshSpawnModelOrigin runs on the fresh-worker path. It states only
// what soho itself does: whether it passes a resolved model argument. It does
// not read native arguments or launch profiles and predicts neither the
// assistant's model nor a lane's selection.
func warnFreshSpawnModelOrigin(o spawnOptions, kind, modelSpec, lane string, res core.RoleSettings, ctx *core.Config, env platform.Env, cwd string, warn func(string, *core.Config, platform.Env, string)) {
	if o.model == "" {
		message := fmt.Sprintf("spawn: soho passes no model to this %s worker ('%s'); it starts on the model its native arguments or the assistant's own settings choose", kind, o.role)
		if modelSpec != "" {
			message = fmt.Sprintf("spawn: the configured model '%s' does not resolve for %s, so soho passes no model to this worker ('%s'); it starts on the model its native arguments or the assistant's own settings choose", text.QuoteControls(modelSpec), kind, o.role)
		}
		if !core.LanesEnabled(ctx, env) {
			message += laneConfigSuffix(o.role, ctx, env)
		}
		warn(message, ctx, env, "spawn")
	}
}

// laneConfigSuffix reports the first configured lane model key for a lane
// whose roles include this role. The value is printed as configured, with
// control characters escaped (text.QuoteControls) so the warning stays one
// line; nothing is resolved, rotated or drawn from the cap or the effective
// kind.
func laneConfigSuffix(role string, ctx *core.Config, env platform.Env) string {
	for _, name := range core.LaneNames(ctx, env) {
		member := false
		for _, laneRole := range core.SplitRoles(core.LaneRolesCSV(ctx, name, env)) {
			if laneRole == role {
				member = true
				break
			}
		}
		if !member {
			continue
		}
		for _, attr := range []string{"models", "model"} {
			key := core.LaneKey(name, attr)
			if value := core.Cfg(ctx, key, "", env); value != "" {
				return fmt.Sprintf("; lanes are off: lane '%s' configures lane.%s.%s=%s; pass --model to choose", name, name, attr, text.QuoteControls(value))
			}
		}
	}
	return ""
}
