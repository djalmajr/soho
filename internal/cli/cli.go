package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/doctor"
	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/job"
	"github.com/djalmajr/soho/internal/kinds"
	"github.com/djalmajr/soho/internal/metrics"
	"github.com/djalmajr/soho/internal/peer"
	"github.com/djalmajr/soho/internal/platform"
	pluginpkg "github.com/djalmajr/soho/internal/plugin"
	"github.com/djalmajr/soho/internal/setup"
	"github.com/djalmajr/soho/internal/spawn"
	"github.com/djalmajr/soho/internal/stats"
	waitpkg "github.com/djalmajr/soho/internal/wait"
)

// knownCommands is the native command inventory: every entry has a native
// case in Run (R1 retired the JS fallback), so any other command dies with
// the unknown-command diagnostic before any probe or side effect.
var knownCommands = map[string]bool{
	"config": true, "session": true, "roles": true, "role": true, "kinds": true,
	"models": true, "model": true, "spawn": true, "dispatch": true, "lint": true,
	"find": true, "run": true, "status": true, "wait": true, "collect": true,
	"send": true, "stats": true, "release": true, "clean": true, "setup": true,
	"doctor": true, "explain": true, "init": true, "title": true, "regrid": true,
	"roster": true, "friction": true, "feedback": true, "tab-label": true,
	"layout-plan": true, "env": true, "mutation-guard": true, "mutation-copy": true,
	"reopen": true, "compact": true, "metrics": true, "copies": true, "gc": true,
	"procs": true, "install": true, "promote": true,
	"collaborate": true, "capabilities": true, "hook": true, "job": true,
}

var frictionLogPath string

// version is set for release builds with -ldflags. Keep dev builds useful by
// appending the VCS revision recorded by the Go toolchain when available.
var version = "dev"

func versionText(buildVersion, revision string) string {
	if buildVersion == "" {
		buildVersion = "dev"
	}
	if buildVersion == "dev" && revision != "" {
		buildVersion += "+" + revision
	}
	return "soho " + buildVersion + "\n"
}

func vcsRevision() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" {
			return setting.Value
		}
	}
	return ""
}

// liveAgentsFriction adapts herdr.LiveAgents' DieError-equivalent panics at
// living command boundaries. The JS entry records non-empty DieErrors for
// these commands, while an empty message only preserves Herdr's exit code.
func liveAgentsFriction(env platform.Env) []any {
	defer func() {
		if value := recover(); value != nil {
			exitErr, ok := value.(*platform.ExitError)
			if !ok {
				panic(value)
			}
			platform.DieFriction(exitErr.Msg, exitErr.Code)
		}
	}()
	return herdr.LiveAgents(env, herdr.Timeout)
}

// Run routes the commands implemented natively; any other command dies 2
// without executing or probing a runtime (R1 retired the JS fallback).
func Run(args []string, env platform.Env) (code int) {
	frictionLogPath = ""
	defer func() {
		value := recover()
		if value == nil {
			return
		}
		exitErr, ok := value.(*platform.ExitError)
		if !ok {
			panic(value)
		}
		if exitErr.Msg != "" {
			_, _ = fmt.Fprintf(platform.Stderr, "soho: %s\n", exitErr.Msg)
			if exitErr.Friction && frictionLogPath != "" {
				core.RecordFrictionError(exitErr.Msg, exitErr.Code, frictionLogPath)
			}
		}
		code = exitErr.Code
	}()
	if len(args) == 1 && args[0] == "--version" {
		_, _ = io.WriteString(platform.Stdout, versionText(version, vcsRevision()))
		return 0
	}
	if len(args) == 2 && args[0] == "capabilities" && args[1] == "--json" {
		_, _ = io.WriteString(platform.Stdout, "{\"schema\":1,\"worker_collaboration\":1,\"ephemeral_job\":1,\"job_events\":1}\n")
		return 0
	}

	// The job start's private launch locators are read once here and
	// dropped from the environment every command uses and hands to its
	// external processes; only the job supervisor and the team commands it
	// runs and binds use them, explicitly (internal/job/locator.go,
	// internal/job/managed.go).
	launch := job.ReadLaunchLocators(env)
	env = job.WithoutLaunchLocators(env)
	decisionEnv := env.Clone()
	command := ""
	if len(args) != 0 {
		command = args[0]
	}
	core.SetFrictionCommand(command)
	var configCtx *core.Config
	commandCwd, _ := os.Getwd()
	// A team command the bound job supervisor runs reads its user
	// configuration from the supervisor's explicit selection, and only
	// when the private launch binding proves it (internal/job/managed.go);
	// every other command selects from its environment as always. A team
	// command whose parent is a job supervisor (or could not be told
	// apart from one) without a binding that holds stops here, before any
	// read or write of its own, instead of acting with the environment's
	// configuration.
	var userSel core.UserSelection
	if launch.Present {
		sel, outcome := job.ManagedSelection(args, launch, decisionEnv, commandCwd)
		if outcome == job.ManagedRefused {
			platform.Die(command+": the job supervisor's launch binding does not hold", 2)
		}
		userSel = sel
	}
	var commandConfig *core.Config
	readOnlyRejectedDoctor := command == "doctor" && decisionEnv.Get("SOHO_NOWRITE") == "1" && len(args) > 1
	if !readOnlyRejectedDoctor && (command == "config" || command == "session" || command == "roles" || command == "role" || command == "explain" || command == "layout-plan" || command == "regrid" || command == "promote" || command == "tab-label" || command == "stats" || command == "metrics" || command == "collect" || command == "setup" || command == "release" || command == "doctor" || command == "init" || command == "spawn" || command == "dispatch" || command == "run" || command == "lint" || command == "wait" || command == "status") {
		ctx := core.LoadConfigFor(decisionEnv, commandCwd, userSel)
		configCtx = &ctx
	}
	if command == "env" {
		ctx := core.LoadConfigFor(decisionEnv, commandCwd, userSel)
		configCtx = &ctx
	}
	if command == "send" || command == "find" || command == "collaborate" {
		ctx := core.LoadConfigFor(decisionEnv, commandCwd, userSel)
		commandConfig = &ctx
	}
	// A writing job subcommand under NOWRITE dies with the job-specific
	// message before the generic refusal; the five read subcommands (and
	// their flags) pass through nowriteReadInvocation below.
	if decisionEnv.Get("SOHO_NOWRITE") == "1" && command == "job" && len(args) > 1 && !nowriteJobRead(args[1:]) {
		platform.Die("SOHO_NOWRITE=1 is read-only: job "+args[1]+" writes; only job status, wait, events, collect and list run", 2)
	}
	if decisionEnv.Get("SOHO_NOWRITE") == "1" && !nowriteReadInvocation(args) {
		shown, extra := command, ""
		if command == "" {
			shown = "(none)"
		} else if len(args) > 1 {
			count := len(args) - 1
			noun := "argument"
			if count > 1 {
				noun = "arguments"
			}
			extra = fmt.Sprintf(" (%d extra %s not allowed)", count, noun)
		}
		platform.Die("soho: SOHO_NOWRITE=1 is read-only: only these invocations run (the plugin's reads): the exact 'doctor', 'roster [--scope workspace|server] [--json]', 'explain', 'config [get <key>]', 'roles [--json]', 'friction' with the read options --since, --level, --command, --agent, --summary, 'collect <agent> [--lines N] [--verify]', 'copies', 'procs', 'status [agents...]', 'gc' without --yes, 'collaborate status <assignment> [--json]', 'capabilities --json'; rejected: "+shown+extra+" — unset SOHO_NOWRITE to write", 2)
	}
	if command == "help" || command == "-h" || command == "--help" || command == "" {
		_, _ = io.WriteString(platform.Stdout, usage)
		return 0
	}
	if command == "plugin" {
		if len(args) < 2 || (args[1] != "bridge" && args[1] != "board" && args[1] != "clipboard" && args[1] != "picker" && args[1] != "team") {
			return pluginpkg.PluginCommand(args[1:], env, platform.Current(), "", "")
		}
		input := ""
		if args[1] == "clipboard" {
			data, err := io.ReadAll(os.Stdin)
			if err != nil {
				fmt.Fprintf(platform.Stderr, "soho plugin: cannot read clipboard input: %s\n", err)
				return 2
			}
			input = string(data)
		}
		executable, err := os.Executable()
		if err != nil && (args[1] == "bridge" || args[1] == "picker" || args[1] == "board" || args[1] == "team") {
			fmt.Fprintf(platform.Stderr, "soho plugin: cannot resolve executable: %s\n", err)
			return 4
		}
		return pluginpkg.PluginCommand(args[1:], env, platform.Current(), executable, input)
	}
	if !knownCommands[command] {
		platform.Die("unknown command '"+command+"'", 2)
	}
	// The first argument after a known command asks for that command's usage
	// lines; --help anywhere else is a regular argument (a send message may
	// contain "--help").
	if len(args) > 1 && (args[1] == "--help" || args[1] == "-h") {
		_, _ = io.WriteString(platform.Stdout, commandHelp(command))
		return 0
	}
	if command == "hook" {
		return cmdHook(args[1:], decisionEnv, commandCwd)
	}
	if command == "roster" || command == "friction" || command == "title" || command == "clean" || command == "feedback" || command == "regrid" || command == "promote" || command == "tab-label" || command == "release" || command == "reopen" || command == "spawn" || command == "dispatch" || command == "run" || command == "collect" || command == "init" || command == "wait" || command == "status" || command == "compact" || command == "copies" || command == "gc" || command == "procs" {
		ctx := core.LoadConfigFor(decisionEnv, commandCwd, userSel)
		commandConfig = &ctx
		herdr.RequireEnv(decisionEnv, platform.Current(), os.Getpid(), nil)
		frictionLogPath = ""
		if !core.Nowrite(decisionEnv) {
			if _, found := platform.FindExecutable("herdr", decisionEnv, platform.Current()); found {
				frictionLogPath = filepath.Join(core.StateDir(commandConfig, decisionEnv, commandCwd), "friction.log")
			}
		}
	}
	if command == "wait" || command == "status" {
		waitpkg.SetFrictionLogFile(frictionLogPath)
	}
	if command == "collect" {
		frictionLogPath = ""
		if !core.Nowrite(decisionEnv) {
			if _, found := platform.FindExecutable("herdr", decisionEnv, platform.Current()); found {
				frictionLogPath = filepath.Join(core.StateDir(configCtx, decisionEnv, commandCwd), "friction.log")
			}
		}
	}
	if command == "mutation-guard" {
		return runMutationGuard(args[1:], decisionEnv)
	}
	if command == "mutation-copy" {
		return runMutationCopy(args[1:], decisionEnv)
	}
	if command == "config" || command == "session" {
		if command == "config" {
			if len(args) > 1 && args[1] == "set" {
				core.CmdConfigSet(args[2:], configCtx, decisionEnv, commandCwd)
			} else if len(args) > 1 && args[1] == "get" {
				cmdConfigGet(args[2:], configCtx, decisionEnv, commandCwd)
			} else {
				core.CmdConfig(configCtx, decisionEnv, commandCwd)
			}
			return 0
		}
		core.CmdSession(args[1:], configCtx, decisionEnv, commandCwd)
		return 0
	}
	if command == "setup" {
		setup.BuildVersion = version
		setup.CmdSetup(args[1:], configCtx, decisionEnv, commandCwd)
		return 0
	}
	if command == "doctor" {
		doctor.CmdDoctor(args[1:], configCtx, decisionEnv, commandCwd)
		return 0
	}
	if command == "init" {
		doctor.CmdInit(configCtx, decisionEnv, commandCwd)
		return 0
	}
	ctx := commandConfig
	if ctx == nil {
		ctx = configCtx
	}
	switch command {
	case "collaborate":
		if decisionEnv.Get("HERDR_WORKSPACE_ID") == "" {
			decisionEnv["HERDR_WORKSPACE_ID"] = core.WorkspaceID(ctx, decisionEnv, commandCwd)
		}
		return cmdCollaborate(args[1:], ctx, decisionEnv, commandCwd)
	case "capabilities":
		platform.Die("usage: capabilities --json", 2)
	case "job":
		// The job family loads no Herdr environment, never calls Herdr and
		// leaves frictionLogPath empty (it runs outside Herdr).
		return cmdJob(args[1:], decisionEnv, commandCwd, launch)
	case "kinds":
		kinds.CmdKinds(decisionEnv, platform.Current())
		return 0
	case "models":
		return cmdModels(args[1:], decisionEnv)
	case "model":
		return cmdModel(args[1:], decisionEnv)
	case "env":
		cmdEnv(configCtx, decisionEnv, commandCwd)
		return 0
	case "roster":
		cmdRoster(args[1:], ctx, decisionEnv, commandCwd)
		return 0
	case "friction":
		cmdFriction(args[1:], ctx, decisionEnv, commandCwd)
		return 0
	case "title":
		cmdTitle(args[1:], decisionEnv)
		return 0
	case "clean":
		cmdClean(args[1:], ctx, decisionEnv, commandCwd)
		return 0
	case "copies":
		return cmdCopies(args[1:], ctx, decisionEnv, commandCwd)
	case "procs":
		return cmdProcs(args[1:], ctx, decisionEnv, commandCwd)
	case "gc":
		return cmdGc(args[1:], ctx, decisionEnv, commandCwd)
	case "release":
		return cmdRelease(args[1:], ctx, decisionEnv, commandCwd)
	case "reopen":
		return cmdReopen(args[1:], ctx, decisionEnv, commandCwd)
	case "feedback":
		return cmdFeedback(args[1:], ctx, decisionEnv, commandCwd)
	case "spawn":
		spawn.CmdSpawn(args[1:], ctx, decisionEnv, commandCwd)
		return 0
	case "dispatch":
		return cmdDispatch(args[1:], ctx, decisionEnv, commandCwd)
	case "compact":
		return cmdCompact(args[1:], ctx, decisionEnv, commandCwd)
	case "run":
		return cmdRun(args[1:], ctx, decisionEnv, commandCwd)
	case "roles":
		cmdRoles(args[1:], decisionEnv, commandCwd)
		return 0
	case "role":
		cmdRole(args[1:], decisionEnv, commandCwd)
		return 0
	case "explain":
		cmdExplain(args[1:], ctx, decisionEnv, commandCwd)
		return 0
	case "send":
		return peer.CmdSend(args[1:], ctx, decisionEnv, commandCwd)
	case "find":
		return peer.CmdFind(args[1:], decisionEnv)
	case "layout-plan":
		return cmdLayoutPlan(args[1:], ctx, decisionEnv, commandCwd)
	case "regrid":
		return cmdRegrid(args[1:], ctx, decisionEnv, commandCwd)
	case "promote":
		return cmdPromote(args[1:], ctx, decisionEnv, commandCwd)
	case "tab-label":
		return cmdTabLabel(args[1:], ctx, decisionEnv, commandCwd)
	case "stats":
		return stats.CmdStats(args[1:], stats.CommandContext{Config: ctx, Env: decisionEnv, Cwd: commandCwd, FrictionLog: frictionLogPath})
	case "metrics":
		return metrics.CmdMetrics(args[1:], metrics.CommandContext{Config: ctx, Env: decisionEnv, Cwd: commandCwd, FrictionLog: frictionLogPath})
	case "collect":
		return stats.CmdCollect(args[1:], stats.CommandContext{Config: ctx, Env: decisionEnv, Cwd: commandCwd, FrictionLog: frictionLogPath})
	case "wait":
		core.PressureWarnThrottled(ctx, decisionEnv, commandCwd)
		return waitpkg.CmdWait(args[1:], ctx, decisionEnv, commandCwd)
	case "status":
		core.PressureWarnThrottled(ctx, decisionEnv, commandCwd)
		return waitpkg.CmdStatus(args[1:], ctx, decisionEnv, commandCwd)
	case "lint":
		return cmdLint(args[1:], ctx, decisionEnv, commandCwd)
	case "install":
		return cmdInstall(args[1:], decisionEnv)
	}
	// A registered command without a native case dies with the same
	// diagnostic as an unknown one: nothing is probed or executed.
	platform.Die("unknown command '"+command+"'", 2)
	return 2
}

// nowriteReadInvocation decides, from the shape of the invocation alone, what
// runs under SOHO_NOWRITE=1: the doctor/roster reads (the plugin's
// actions, roster with its --scope and --json shapes), the exact explain,
// config (the listing) and config get <key>, copies and procs, roles with
// the optional --json, friction with only the read options, collect <agent>
// with its options, status with agent names, and gc without --yes.
// Every other shape is rejected before any command runs, so a rejected
// invocation writes nothing (not even a friction line).
func nowriteReadInvocation(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "hook":
		return len(args) == 2 && (args[1] == "reminder" || args[1] == "doctor")
	case "collaborate":
		return len(args) >= 3 && len(args) <= 4 && args[1] == "status" && args[2] != "" && !strings.HasPrefix(args[2], "-") && (len(args) == 3 || args[3] == "--json")
	case "capabilities":
		return len(args) == 2 && args[1] == "--json"
	case "job":
		return nowriteJobRead(args[1:])
	case "roster":
		_, _, ok := rosterArgs(args[1:])
		return ok
	case "doctor", "explain", "copies", "procs":
		return len(args) == 1
	case "config":
		return len(args) == 1 || (len(args) == 3 && args[1] == "get" && args[2] != "")
	case "roles":
		return len(args) == 1 || (len(args) == 2 && args[1] == "--json")
	case "status":
		for _, agent := range args[1:] {
			if agent == "" || strings.HasPrefix(agent, "-") {
				return false
			}
		}
		return true
	case "friction":
		return nowriteFlagOnly(args[1:], []string{"--since", "--level", "--command", "--agent"}, []string{"--summary"})
	case "collect":
		return nowriteCollectRead(args[1:])
	case "gc":
		return nowriteFlagOnly(args[1:], []string{"--older-than"}, []string{"--include-unregistered"})
	}
	return false
}

// nowriteFlagOnly accepts a flag-only invocation: every token is one of the
// valued flags (consuming the following token, which must not look like a
// flag) or one of the bare flags; anything else is refused.
func nowriteFlagOnly(argv []string, valued, bare []string) bool {
	has := func(list []string, flag string) bool {
		for _, f := range list {
			if f == flag {
				return true
			}
		}
		return false
	}
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if has(valued, a) {
			if i+1 >= len(argv) || strings.HasPrefix(argv[i+1], "--") {
				return false
			}
			i++
		} else if !has(bare, a) {
			return false
		}
	}
	return true
}

// nowriteCollectRead accepts 'collect <agent> [--lines N] [--verify]'.
func nowriteCollectRead(argv []string) bool {
	if len(argv) == 0 || strings.HasPrefix(argv[0], "--") {
		return false
	}
	for i := 1; i < len(argv); i++ {
		switch argv[i] {
		case "--lines":
			if i+1 >= len(argv) || strings.HasPrefix(argv[i+1], "--") {
				return false
			}
			i++
		case "--verify":
		default:
			return false
		}
	}
	return true
}

func withoutArg(args []string, target string) []string {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		if arg != target {
			out = append(out, arg)
		}
	}
	return out
}
