package spawn

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/djalmajr/soho/internal/backend"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/text"
	"github.com/djalmajr/soho/internal/transport"
)

var nativeConfigAssignment = regexp.MustCompile(`^[A-Za-z0-9_.-]+=`)

func continueArgsError(kind string, fresh bool, args []string) string {
	for i, arg := range args {
		// The worker's own end-of-options marker: what follows is prompt
		// text for it (pi, claude), not options, so nothing after it is
		// a continue flag or codex config (R567).
		if arg == "--" {
			break
		}
		if (arg == "-c" || arg == "--config") && i+1 < len(args) && nativeConfigAssignment.MatchString(args[i+1]) {
			return fmt.Sprintf("spawn: %s reads %s as --continue or an incompatible option; %s key=value is codex config, pass it only to a codex worker", text.SanitizeCause(kind), arg, arg)
		}
		flag, _, _ := strings.Cut(arg, "=")
		if fresh && (flag == "-c" || flag == "--continue") {
			return fmt.Sprintf("spawn: --fresh starts a new session, but %s reads %s as --continue and would resume the last one; drop one of them", text.SanitizeCause(kind), text.SanitizeCause(arg))
		}
	}
	return ""
}

// ConfigNativeArgs returns configured args for the CLI the worker will run.
func ConfigNativeArgs(kind, lane, role string, ctx *core.Config, env platform.Env, cwd string, onDrop func(key, own string)) string {
	scopedKey := "role." + role + ".args"
	scoped := core.Cfg(ctx, "role_"+strings.ReplaceAll(role, "-", "_")+"_args", "", env)
	if lane != "" {
		scopedKey = "lane." + lane + ".args"
		scoped = core.Cfg(ctx, core.LaneKey(lane, "args"), "", env)
	}
	if scoped != "" {
		own := core.ResolveRoleSettings(role, ctx, env, cwd, core.RoleFlags{}).Kind
		if own != kind {
			if onDrop != nil {
				onDrop(scopedKey, own)
			}
			scoped = ""
		}
	}
	var tokens []string
	for _, value := range []string{core.Cfg(ctx, "args_"+kind, "", env), scoped} {
		if value != "" {
			tokens = append(tokens, strings.Fields(value)...)
		}
	}
	if kind == "codex" && !hasNativeToken(tokens, "--no-daemon") && !hasNativeToken(tokens, "--remote") && CodexSupportsNoDaemon(env, cwd) {
		// An option goes before a configured "--", never into the prompt.
		tokens = slices.Insert(tokens, optionsEnd(tokens), "--no-daemon")
	}
	return strings.Join(tokens, " ")
}

// isCodexUpdateSetting is true for a "key=value" config override of
// check_for_update_on_startup as codex reads it: a token whose literal
// quotes survived the shell is a different key to codex, so it is not one.
func isCodexUpdateSetting(pair string) bool {
	key, _, ok := strings.Cut(pair, "=")
	return ok && strings.TrimSpace(key) == "check_for_update_on_startup"
}

// hasCodexUpdateConfig recognizes explicit config overrides before the prompt.
func hasCodexUpdateConfig(tokens []string) bool {
	for i := 0; i < len(tokens); i++ {
		token := tokens[i]
		if token == "--" || !strings.HasPrefix(token, "-") {
			break
		}
		if token == "-c" || token == "--config" {
			if i+1 >= len(tokens) {
				break
			}
			i++
			if isCodexUpdateSetting(tokens[i]) {
				return true
			}
			continue
		}
		// The attached forms codex also accepts: -c=key=v, --config=key=v
		// and -ckey=v.
		if value, ok := strings.CutPrefix(token, "--config="); ok {
			if isCodexUpdateSetting(value) {
				return true
			}
			continue
		}
		if value, ok := strings.CutPrefix(token, "-c"); ok && value != "" && !strings.HasPrefix(token, "--") {
			if isCodexUpdateSetting(strings.TrimPrefix(value, "=")) {
				return true
			}
			continue
		}
		if token == "-i" || token == "--image" {
			for i+1 < len(tokens) && !strings.HasPrefix(tokens[i+1], "-") {
				i++
			}
			continue
		}
		switch token {
		case "--enable", "--disable", "--remote", "--remote-auth-token-env", "-m", "--model", "--local-provider", "-p", "--profile", "-s", "--sandbox", "-C", "--cd", "--add-dir", "-a", "--ask-for-approval":
			if i+1 < len(tokens) {
				i++
			}
		}
	}
	return false
}

// optionsEnd is the index of the first "--" in tokens, or their length.
func optionsEnd(tokens []string) int {
	if i := slices.Index(tokens, "--"); i >= 0 {
		return i
	}
	return len(tokens)
}

// hasNativeToken reports an option among tokens. A "--" ends the options:
// what follows it is the prompt, never a flag.
func hasNativeToken(tokens []string, want string) bool {
	for _, token := range tokens {
		if token == "--" {
			return false
		}
		if token == want || strings.HasPrefix(token, want+"=") {
			return true
		}
	}
	return false
}

// RosterCaller reports the row of this project's roster that holds pane, when
// one exists: the row's agent name and role. A pane row means the pane is a
// worker (or sub-orchestrator) this project opened for another orchestrator.
func RosterCaller(ctx *core.Config, env platform.Env, cwd, pane string) (name, role string, ok bool) {
	if pane == "" {
		return "", "", false
	}
	// StateRootPath, not StateDirPath: a lookup must not append the state
	// root to .gitignore (StateRoot does).
	dir := filepath.Join(core.StateRootPath(ctx, env, cwd), core.WorkspaceID(ctx, env, cwd))
	for _, line := range core.RosterRows(dir) {
		f := strings.Split(line, "\t")
		if len(f) > 3 && f[0] != "" && f[1] == pane {
			name, role = f[0], f[3]
		}
	}
	return name, role, name != ""
}

// EnsureOrchestratorName names the caller's agent when the current pane hosts
// one. A caller pane that already holds a roster row of this project is a
// worker another orchestrator opened: its agent keeps its name, one line is
// written to stderr naming it (command is the calling command, "init" or
// "spawn"), and the agent's current name is returned.
func EnsureOrchestratorName(ctx *core.Config, env platform.Env, cwd, command string) string {
	svc := backend.New(env)
	pane := svc.Runtime.Caller().PaneID
	if pane == "" {
		return ""
	}
	want := core.Cfg(ctx, "orchestrator_name", "orchestrator", env)
	// The gate is the pre-transport raw existence check, one attempt: it
	// looks only at the exit status — never at the parsed body — so a get
	// that exits 0 without an agent_status still passes, and a killed or
	// failed get leaves the caller unnamed.
	gate := svc.Legacy.AgentGetOK(context.Background(), transport.Target{ID: pane}, transport.Call{})
	if !gate.Value {
		return ""
	}
	cur := svc.Terminal.CallerAgentName(context.Background(), transport.Call{}).Value
	// The roster comes first: a worker named like an orchestrator is still a
	// worker, and it gets the same stderr line.
	if name, role, inRoster := RosterCaller(ctx, env, cwd, pane); inRoster {
		who := "a " + role + " opened by another orchestrator"
		if role == "" {
			who = "opened by another orchestrator"
		}
		_, _ = fmt.Fprintf(platform.Stderr, "soho: %s: this pane is '%s' in the roster (%s); keeping its name\n", command, name, who)
		if cur == "" {
			// A transient failure of the name read must not report an
			// empty name: the roster row names the same agent.
			return name
		}
		return cur
	}
	if cur == want || strings.HasPrefix(cur, want+"-") {
		return cur
	}
	name := UniqueName(want, env)
	if !svc.Legacy.Rename(context.Background(), transport.Target{ID: pane}, name, transport.Call{}).Value {
		Warn("could not rename the caller agent to '"+name+"'", ctx, env, "spawn")
		return ""
	}
	return name
}
