package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/transport"
)

func SessionConfPath(ctx *Config, env platform.Env, cwd string) string {
	svc := herdr.New(env)
	workspace := svc.Runtime.Caller().WorkspaceID
	if workspace == "" && !svc.Runtime.Caller().Inside {
		return ""
	}
	if workspace == "" {
		result := svc.Legacy.PaneCurrentRaw(nil, transport.Call{Timeout: 10 * time.Second})
		if !result.Process.NotFound && result.Process.Status != nil && *result.Process.Status == 0 {
			var payload struct {
				Result struct {
					Pane struct {
						WorkspaceID string `json:"workspace_id"`
					} `json:"pane"`
				} `json:"result"`
			}
			if json.Unmarshal([]byte(result.Value), &payload) == nil {
				workspace = payload.Result.Pane.WorkspaceID
			}
		}
	}
	if workspace == "" {
		return ""
	}
	return filepath.Join(StateRootPath(ctx, env, cwd), workspace, "session.conf")
}

func CmdSession(argv []string, ctx *Config, env platform.Env, cwd string) {
	sub := ""
	if len(argv) != 0 {
		sub = argv[0]
	}
	switch sub {
	case "set":
		CmdSessionSet(argv[1:], ctx, env, cwd)
	case "clear":
		CmdSessionClear(argv[1:], ctx, env, cwd)
	case "show", "":
		CmdSessionShow(ctx, env, cwd)
	default:
		platform.Die(fmt.Sprintf("session: unknown subcommand '%s' (set <key> <value> | clear [key] | show)", sub), 2)
	}
}

// parseSessionPair reads <key> <value> or <key>=<value>. A value may begin
// with hyphens (lane.review.args --add-dir /x): only an argument before the
// key, or after a value that does not begin with a hyphen, is an unknown
// option, and a "--" ends option parsing for what follows.
func parseSessionPair(argv []string, command string) (key, value string, sawValue bool) {
	noOptions := false
	for _, arg := range argv {
		if !noOptions && arg == "--" {
			noOptions = true
			continue
		}
		if !noOptions && strings.HasPrefix(arg, "--") && (key == "" || (sawValue && !strings.HasPrefix(value, "-"))) {
			platform.Die(fmt.Sprintf("%s: unknown option '%s'", command, arg), 2)
		}
		if key == "" {
			if eq := strings.IndexByte(arg, '='); eq > 0 {
				key, value = arg[:eq], arg[eq+1:]
				sawValue = value != ""
			} else {
				key = arg
			}
			continue
		}
		if !sawValue {
			value, sawValue = arg, true
		} else if strings.HasPrefix(value, "-") {
			value += " " + arg
		} else {
			platform.Die(fmt.Sprintf("%s: unexpected argument '%s'", command, arg), 2)
		}
	}
	return key, value, sawValue
}

// refuseSessionStateInSkill makes session set/clear exit 2 with the skill
// refusal before SessionConfPath (no herdr pane current), StateRoot (no
// .gitignore append) or any MkdirAll/write, the same way StateDir does.
func refuseSessionStateInSkill(ctx *Config, env platform.Env, cwd, command string) {
	if skill := StateInSkill(ctx, env, cwd); skill != "" {
		platform.Die(StateInSkillMessage(command, StateRootPath(ctx, env, cwd), skill), 2)
	}
}

func CmdSessionSet(argv []string, ctx *Config, env platform.Env, cwd string) {
	refuseSessionStateInSkill(ctx, env, cwd, "session set")
	key, value, sawValue := parseSessionPair(argv, "session set")
	key, value, sawValue = SplitPairArg(key, value, sawValue, "session set")
	if key == "" || !sawValue {
		platform.Die("usage: session set <key> <value> | <key>=<value>", 2)
	}
	if value == "" {
		platform.Die("session set: empty value", 2)
	}
	if !ConfigKeyOk(key) {
		platform.Die("session set: unknown key '"+key+"'", 2)
	}
	if !ConfigValueOk(key, value, env, cwd) {
		platform.Die("session set: invalid value '"+value+"' for "+key, 2)
	}
	sessionFile := SessionConfPath(ctx, env, cwd)
	if sessionFile == "" {
		platform.Die("session set: no Herdr workspace here (run inside Herdr, or set HERDR_WORKSPACE_ID)", 2)
	}
	func() {
		defer func() { _ = recover() }()
		StateRoot(ctx, env, cwd)
	}()
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o777); err != nil {
		platform.Die(fmt.Sprintf("session set: could not rewrite %s (file left untouched)", sessionFile), 4)
	}
	configWritePair(sessionFile, key, value, env, cwd, "session set")
	fmt.Fprintf(platform.Stdout, "set %s=%s in %s (session: this Herdr workspace only; above project and user, below flags and SOHO_*)\n", key, value, sessionFile)
}

func CmdSessionClear(argv []string, ctx *Config, env platform.Env, cwd string) {
	refuseSessionStateInSkill(ctx, env, cwd, "session clear")
	key := ""
	if len(argv) > 0 {
		key = argv[0]
	}
	if len(argv) > 1 {
		platform.Die("usage: session clear [key]", 2)
	}
	sessionFile := SessionConfPath(ctx, env, cwd)
	if sessionFile == "" {
		platform.Die("session clear: no Herdr workspace here (run inside Herdr, or set HERDR_WORKSPACE_ID)", 2)
	}
	if key == "" {
		if info, err := os.Lstat(sessionFile); err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				if _, err := os.Stat(sessionFile); os.IsNotExist(err) {
					fmt.Fprintln(platform.Stdout, "session is empty (nothing to clear)")
					return
				}
			}
			if info.IsDir() {
				platform.Die(fmt.Sprintf("session clear: could not clear %s (file left untouched)", sessionFile), 4)
			}
			if err := os.Remove(sessionFile); err != nil {
				platform.Die(fmt.Sprintf("session clear: could not clear %s (file left untouched)", sessionFile), 4)
			}
			fmt.Fprintf(platform.Stdout, "session cleared: %s\n", sessionFile)
		} else if os.IsNotExist(err) {
			fmt.Fprintln(platform.Stdout, "session is empty (nothing to clear)")
		} else {
			platform.Die(fmt.Sprintf("session clear: could not clear %s (file left untouched)", sessionFile), 4)
		}
		return
	}
	if !ConfigKeyOk(key) {
		platform.Die("session clear: unknown key '"+key+"'", 2)
	}
	if _, err := os.Stat(sessionFile); os.IsNotExist(err) {
		fmt.Fprintln(platform.Stdout, "session is empty (nothing to clear)")
		return
	}
	ConfigClearKey(sessionFile, key)
	fmt.Fprintf(platform.Stdout, "cleared %s from %s\n", key, sessionFile)
}

func CmdSessionShow(ctx *Config, env platform.Env, cwd string) {
	sessionFile := SessionConfPath(ctx, env, cwd)
	if sessionFile != "" {
		if info, err := os.Stat(sessionFile); err == nil && info.Size() > 0 {
			text, readErr := platform.ReadTextFile(sessionFile)
			if readErr == nil {
				fmt.Fprintf(platform.Stdout, "%s\nsession file: %s\n", text, sessionFile)
				return
			}
		}
	}
	fmt.Fprintln(platform.Stdout, "session is empty (no session overrides for this workspace)")
}
