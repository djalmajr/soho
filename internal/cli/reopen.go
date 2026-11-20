package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/spawn"
	"github.com/djalmajr/soho/internal/taskreport"
)

// cmdReopen closes the worker like release --close and reopens it with spawn --fresh,
// carrying the roster row with explicit overrides; --redispatch resends its last
// brief after the spawn JSON, and the release half's stdout is discarded.
func cmdReopen(argv []string, ctx *core.Config, env platform.Env, cwd string) int {
	if len(argv) == 0 {
		core.DieFriction("reopen: missing agent name", 2, frictionLogPath, "reopen")
	}
	agent := argv[0]
	force, redispatch := false, false
	kindOverride, modelOverride, effortOverride := "", "", ""
	for i := 1; i < len(argv); i++ {
		switch arg := argv[i]; arg {
		case "--force":
			force = true
		case "--redispatch":
			redispatch = true
		case "--kind", "--model", "--effort":
			if i+1 >= len(argv) {
				core.DieFriction("reopen: "+arg+" expects a value", 2, frictionLogPath, "reopen")
			}
			i++
			value := argv[i]
			switch arg {
			case "--kind":
				if !reopenKnownValue(core.KnownKinds, value) {
					core.DieFriction("reopen: unknown kind "+value, 2, frictionLogPath, "reopen")
				}
				kindOverride = value
			case "--model":
				modelOverride = value
			case "--effort":
				if !reopenKnownValue(core.EffortLadder, value) {
					core.DieFriction("reopen: invalid effort "+value, 2, frictionLogPath, "reopen")
				}
				effortOverride = value
			}
		default:
			core.DieFriction("reopen: unknown option "+arg, 2, frictionLogPath, "reopen")
		}
	}
	sd := core.StateDirPath(ctx, env, cwd)
	line := core.RosterLine(sd, agent)
	if line == "" {
		core.DieFriction(fmt.Sprintf("agent '%s' is not in the roster", agent), 3, frictionLogPath, "reopen")
	}
	fields := strings.Split(line, "\t")
	field := func(index int) string {
		if index < len(fields) {
			return fields[index]
		}
		return ""
	}
	role, pane, wkind, model, wdir, native := field(3), field(1), field(2), field(8), field(6), field(13)
	effort, mcp := field(14), field(15)
	kind, newModel, newEffort := wkind, model, effort
	if kindOverride != "" {
		kind = kindOverride
		// A different kind resolves its own model, as an explicit --kind on
		// spawn does. An explicit --model still wins over that resolution.
		if modelOverride == "" && kind != wkind {
			newModel = ""
		}
	}
	if kindOverride != "" && kind != wkind && native != "" {
		// The recorded args belong to the CLI the old worker ran. Like the
		// old kind's model, they must not reach a different CLI after the
		// release half has already closed the worker.
		_, _ = fmt.Fprintf(platform.Stderr, "soho: reopen: '%s' was a %s worker; its native args are not passed to %s\n", agent, wkind, kind)
		native = ""
	}
	if modelOverride != "" {
		newModel = modelOverride
	}
	if effortOverride != "" {
		newEffort = effortOverride
	}
	// The last brief is recorded with the dispatch that opened the current
	// task. Read it before the release half retires the task pointer.
	var briefBody, briefPath string
	if redispatch {
		if amendmentBrief, amendmentPath := reopenAmendmentPaths(sd, agent); amendmentBrief != "" && amendmentPath != "" {
			core.DieFriction(fmt.Sprintf("reopen: the last task of '%s' was an amendment; after reopening, run: soho dispatch %s %s and then soho dispatch %s %s --amend", agent, agent, amendmentBrief, agent, amendmentPath), 2, frictionLogPath, "reopen")
		}
		briefPath = reopenLastBriefPath(sd, agent)
		briefBody = reopenLastBriefBody(sd, agent)
		if briefBody == "" {
			core.DieFriction(fmt.Sprintf("reopen: '%s' has no brief to redispatch", agent), 2, frictionLogPath, "reopen")
		}
	}
	// DJA-394: the caller-pane check runs here, after the arguments and the
	// roster are valid and before the release half closes the worker's
	// pane and drops its roster line: a refused reopen leaves the worker
	// as it was. The spawn half checks again through the same memo, so
	// Herdr is asked once.
	core.CallerGate("reopen", ctx, env, cwd)
	// DJA-392: the spawn half of a reopen always opens a new pane (--fresh,
	// never reuse), so the pressure refusal runs here, before the release
	// half changes any state (roster line, pane, copies): a refused reopen
	// leaves the worker exactly as it was, and the spawn's warning line of
	// today still goes out. --force is the skip and is forwarded to the
	// spawn half, which would otherwise refuse there.
	if !force {
		if parts := core.PressureRefuseParts(ctx, env, cwd); len(parts) > 0 {
			core.PressureWarn(ctx, env, cwd)
			platform.Die(fmt.Sprintf("spawn: refusing to open a new pane under resource pressure: %s; close idle panes or run 'soho gc', or pass --force", strings.Join(parts, "; ")), 25)
		}
	}

	// Without an explicit --kind this is the old DJA-397 reopen: the roster's
	// kind is carried into the lane, and a conflict is refused before the
	// release half closes anything. With --kind it is the one-off one of the
	// follow-up brief: only this replacement spawn skips the lane rule, and
	// the next spawn for the lane follows it again.
	if kindOverride == "" {
		if lane, laneKind, conflict := spawn.LaneKindConflict(role, wkind, ctx, env); conflict {
			spawn.RefuseLaneKind(role, lane, laneKind, wkind, fmt.Sprintf("reopen: worker '%s' runs %s, but lane '%s' is now %s; reopening would put a %s worker in it. Release it with 'soho release %s --close' and spawn the role again.", agent, wkind, lane, laneKind, wkind, agent), "reopen", ctx, env, cwd)
		}
	}
	// DJA-392 fatia 2 R2: the roster's last field carries the worker's
	// effective MCP choice; reopen forwards it so the respawn keeps the MCP
	// it was opened with. A row without the field (predating it) passes no
	// --mcp and the normal resolution decides, as in a fresh open.
	// Prepare the exact replacement before release can retire the old worker.
	// The same argv is revalidated by CmdSpawn after release; this observation
	// does not reserve configuration or backend capability across that window.
	spawnArgs := []string{role, "--fresh", "--kind", kind, "--name", agent, "--cwd", wdir, "--force"}
	if newModel != "" {
		spawnArgs = append(spawnArgs, "--model", newModel)
	}
	if newEffort != "" {
		spawnArgs = append(spawnArgs, "--effort", newEffort)
	}
	if mcp != "" {
		spawnArgs = append(spawnArgs, "--mcp", mcp)
	}
	if native != "" {
		spawnArgs = append(spawnArgs, "--")
		spawnArgs = append(spawnArgs, strings.Fields(native)...)
	}
	if kindOverride != "" {
		spawn.PreflightFreshSpawnWithLaneKindOverride(spawnArgs, ctx, env, cwd)
	} else {
		spawn.PreflightFreshSpawn(spawnArgs, ctx, env, cwd)
	}

	releaseArgs := []string{agent, "--close"}
	if force {
		releaseArgs = append(releaseArgs, "--force")
	}
	oldOut := platform.Stdout
	platform.Stdout = releaseDiscardWriter{}
	defer func() { platform.Stdout = oldOut }()
	code := cmdRelease(releaseArgs, ctx, env, cwd)
	// Successful release retires the task pointer before the replacement
	// starts; spawn also retires legacy pointers on every new-agent path.
	platform.Stdout = oldOut
	if code != 0 {
		return code
	}

	// The pressure admission ran once, above, before the release: the spawn
	// half never measures again, so a pressure rise after the release cannot
	// refuse the new pane once the old worker is gone. A spawn refusal cannot
	// leave a redispatch behind; the warning names what release already
	// closed before re-panicking with the spawn's own code.
	spawnReopenedWorker(agent, pane, briefPath, redispatch, spawnArgs, kindOverride != "", ctx, env, cwd)
	if !redispatch {
		return 0
	}
	return reopenRedispatch(agent, briefBody, ctx, env, cwd)
}

// reopenAmendmentPaths finds the composed prompts dispatch recorded for an
// open task whose last member is an amendment: the original brief member and
// the amendment member. Paths come from the task pointer, before release
// retires it; nothing is opened, sent or closed here.
func reopenAmendmentPaths(sd, agent string) (briefPath, amendmentPath string) {
	pointer := taskreport.ReadTaskReportPointer(sd, agent)
	if pointer == nil {
		return "", ""
	}
	currentValue, ok := pointer.Get("current")
	if !ok {
		return "", ""
	}
	current, ok := currentValue.(string)
	if !ok || current == "" {
		return "", ""
	}
	amendmentPath = recordedComposedPath(sd, current)
	if amendmentPath == "" {
		return "", ""
	}
	raw, err := platform.ReadTextFile(amendmentPath)
	if err != nil || !strings.HasPrefix(raw, "# Amendment to your current brief\n") {
		return "", ""
	}
	members := []string{}
	if value, ok := pointer.Get("history"); ok {
		if items, ok := value.([]any); ok {
			for _, item := range items {
				if s, ok := item.(string); ok && s != current {
					members = append(members, s)
				}
			}
		}
	}
	for i := len(members) - 1; i >= 0; i-- {
		candidate := recordedComposedPath(sd, members[i])
		if candidate == "" {
			continue
		}
		body, err := platform.ReadTextFile(candidate)
		if err == nil && composedBriefSection(body) != "" {
			return candidate, amendmentPath
		}
	}
	return "", amendmentPath
}

func recordedComposedPath(sd, member string) string {
	if composed, ok := memberComposedPath(sd, member); ok {
		return composed
	}
	return pendingBriefPath(member)
}

func reopenKnownValue(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

func spawnReopenedWorker(agent, pane, briefPath string, redispatch bool, spawnArgs []string, laneOverride bool, ctx *core.Config, env platform.Env, cwd string) {
	defer func() {
		value := recover()
		if value == nil {
			return
		}
		exit, ok := value.(*platform.ExitError)
		if !ok {
			panic(value)
		}
		_, _ = fmt.Fprintf(platform.Stderr, "soho: warning: reopen already closed '%s' (pane %s); no replacement was spawned and no brief was redispatched: %s\n", agent, pane, exit.Msg)
		recovery := reopenRecoveryCommand(agent, spawnArgs)
		if laneOverride && reopenRecoveryCrossesLane(spawnArgs, ctx, env) {
			// reopen's swap skipped the lane-kind check; a plain spawn would
			// refuse with exit 13, so the printed command opts out of lanes.
			recovery = "SOHO_LANES=off " + recovery
		}
		_, _ = fmt.Fprintf(platform.Stderr, "soho: reopen: to recover, run: %s\n", recovery)
		if redispatch && briefPath != "" {
			_, _ = fmt.Fprintf(platform.Stderr, "soho: reopen: then run: soho dispatch %s %s\n", reopenShellQuote(agent), reopenShellQuote(briefPath))
		}
		panic(value)
	}()
	if laneOverride {
		spawn.CmdSpawnWithLaneKindOverride(spawnArgs, ctx, env, cwd)
	} else {
		spawn.CmdSpawn(spawnArgs, ctx, env, cwd)
	}
}

// reopenRecoveryCommand rebuilds the spawn the reopen would have run after
// closing the old worker. --name is printed once, next to the role, and the
// remaining argv is the exact argv reopen handed to spawn, in the same order.
// reopenRecoveryCrossesLane reports whether a plain spawn of these arguments
// would refuse because the requested kind differs from the role's lane kind.
func reopenRecoveryCrossesLane(spawnArgs []string, ctx *core.Config, env platform.Env) bool {
	if len(spawnArgs) == 0 {
		return false
	}
	kind := ""
	for i := 1; i+1 < len(spawnArgs); i++ {
		if spawnArgs[i] == "--" {
			break
		}
		if spawnArgs[i] == "--kind" {
			kind = spawnArgs[i+1]
			break
		}
	}
	if kind == "" {
		return false
	}
	_, _, conflict := spawn.LaneKindConflict(spawnArgs[0], kind, ctx, env)
	return conflict
}

func reopenRecoveryCommand(agent string, spawnArgs []string) string {
	if len(spawnArgs) == 0 {
		return "soho spawn " + reopenShellQuote(agent)
	}
	role := spawnArgs[0]
	parts := []string{"soho", "spawn", reopenShellQuote(role), "--name", reopenShellQuote(agent)}
	native := false
	for i := 1; i < len(spawnArgs); i++ {
		if !native && spawnArgs[i] == "--name" && i+1 < len(spawnArgs) {
			i++
			continue
		}
		// reopen's own spawn passes --force because the replacement takes the
		// closed worker's place; the printed command runs after that close,
		// so it leaves the memory-pressure guard in force.
		if !native && spawnArgs[i] == "--force" {
			continue
		}
		if spawnArgs[i] == "--" {
			native = true
		}
		parts = append(parts, reopenShellQuote(spawnArgs[i]))
	}
	return strings.Join(parts, " ")
}

// reopenShellQuote keeps one argv token one shell word: a plain token is
// printed as-is, anything else is single-quoted with escaped inner quotes.
func reopenShellQuote(value string) string {
	if value == "" {
		return "''"
	}
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("@%+=:,./-_", r) {
			continue
		}
		return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
	}
	return value
}

// reopenLastBriefPath resolves the composed brief the agent's last dispatch
// recorded, the same file reopenLastBriefBody reads.
func reopenLastBriefPath(sd, agent string) string {
	report := core.LastReport(sd, agent)
	if pointer := taskreport.ReadTaskReportPointer(sd, agent); pointer != nil {
		if current, ok := pointer.Get("current"); ok {
			if value, ok := current.(string); ok && value != "" {
				report = value
			}
		}
	}
	if report == "" {
		return ""
	}
	if composed, ok := memberComposedPath(sd, report); ok && composed != "" {
		return composed
	}
	return pendingBriefPath(report)
}

// reopenLastBriefBody resolves the composed brief the agent's last dispatch
// recorded and returns its original brief section, without the role and
// report contract that dispatch adds around it.
func reopenLastBriefBody(sd, agent string) string {
	composed := reopenLastBriefPath(sd, agent)
	if composed == "" {
		return ""
	}
	raw, err := platform.ReadTextFile(composed)
	if err != nil {
		return ""
	}
	section := composedBriefSection(raw)
	_, body, found := strings.Cut(section, "\n")
	if !found {
		return ""
	}
	body = strings.TrimLeft(body, "\n")
	if strings.TrimSpace(body) == "" {
		return ""
	}
	return body
}

func reopenRedispatch(agent, body string, ctx *core.Config, env platform.Env, cwd string) (code int) {
	dir := filepath.Join(core.StateDirPath(ctx, env, cwd), "briefs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		core.DieFriction("reopen: could not record the redispatched brief: "+err.Error(), 4, frictionLogPath, "reopen")
	}
	f, err := os.CreateTemp(dir, agent+"-redispatch-*.md")
	if err != nil {
		core.DieFriction("reopen: could not record the redispatched brief: "+err.Error(), 4, frictionLogPath, "reopen")
	}
	path := f.Name()
	defer func() { _ = os.Remove(path) }()
	if _, err = f.WriteString(body); err != nil {
		_ = f.Close()
		core.DieFriction("reopen: could not record the redispatched brief: "+err.Error(), 4, frictionLogPath, "reopen")
	}
	if err = f.Close(); err != nil {
		core.DieFriction("reopen: could not record the redispatched brief: "+err.Error(), 4, frictionLogPath, "reopen")
	}
	defer func() {
		if value := recover(); value != nil {
			exit, ok := value.(*platform.ExitError)
			if !ok {
				panic(value)
			}
			_, _ = fmt.Fprintf(platform.Stderr, "soho: warning: reopen replaced '%s' but its brief was not redispatched: %s\n", agent, exit.Msg)
			panic(value)
		}
	}()
	code = cmdDispatchWithOptions([]string{agent, path, "--no-wait"}, ctx, env, cwd, dispatchOptions{noResend: true})
	if code != 0 {
		_, _ = fmt.Fprintf(platform.Stderr, "soho: warning: reopen replaced '%s' but its brief was not redispatched (dispatch exit %d)\n", agent, code)
	}
	return code
}
