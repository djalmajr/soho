package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/djalmajr/soho/internal/codexenv"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/dispatch"
	"github.com/djalmajr/soho/internal/platform"
)

// dispatchDisplayNativePolicy reads the roster's native argument snapshot.
func dispatchDisplayNativePolicy(args string) ([]string, bool) {
	fields := strings.Fields(args)
	var patterns []string
	found := false
	for i := 0; i < len(fields); i++ {
		if fields[i] == "--" {
			break
		}
		var value string
		if fields[i] == "-c" || fields[i] == "--config" {
			i++
			if i >= len(fields) {
				break
			}
			value = fields[i]
		} else if strings.HasPrefix(fields[i], "--config=") {
			value = strings.TrimPrefix(fields[i], "--config=")
		} else if strings.HasPrefix(fields[i], "-c") {
			value = strings.TrimPrefix(strings.TrimPrefix(fields[i], "-c"), "=")
		} else {
			continue
		}
		value = strings.TrimPrefix(value, "'")
		if !strings.HasPrefix(value, "shell_environment_policy.include_only=") {
			continue
		}
		for !strings.Contains(value, "]") && i+1 < len(fields) {
			i++
			value += " " + fields[i]
		}
		value = strings.TrimSuffix(value, "'")
		p, present, valid := codexenv.RegistryIncludeOnly(value)
		if present && valid {
			patterns, found = p, true
		}
	}
	return patterns, found
}

func warnDispatchDisplay(body, kind, agent, args, sd string, env platform.Env) {
	if kind != "codex" || dispatch.BriefType(body) != "ui" {
		return
	}
	var names []string
	for _, name := range []string{"DISPLAY", "WAYLAND_DISPLAY", "HYPRLAND_INSTANCE_SIGNATURE"} {
		if env.Get(name) != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return
	}
	patterns, present := dispatchDisplayNativePolicy(args)
	if !present {
		home := env.Get("CODEX_HOME")
		if home == "" {
			home = filepath.Join(platform.HomeDir(platform.Current(), env), ".codex")
		}
		data, err := os.ReadFile(filepath.Join(home, "config.toml"))
		if err != nil {
			return
		}
		var valid bool
		patterns, present, valid = codexenv.RegistryIncludeOnly(string(data))
		if !valid || !present {
			return
		}
	}
	if len(patterns) == 0 {
		return
	}
	var dropped []string
	for _, name := range names {
		admitted := false
		for _, pattern := range patterns {
			if codexenv.GlobMatch(pattern, name) {
				admitted = true
				break
			}
		}
		if !admitted {
			dropped = append(dropped, name)
		}
	}
	if len(dropped) == 0 {
		return
	}
	core.WarnOnce("dispatch-display:"+agent, func() string { return sd }, env, false, func() {
		fmt.Fprintf(platform.Stderr, "soho: warning: dispatch: '%s' (codex) does not receive %s (shell_environment_policy.include_only): the graphical proof of this ui brief stays with you, or add them to include_only\n", agent, strings.Join(dropped, ", "))
	})
}
