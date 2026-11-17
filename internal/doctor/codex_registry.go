package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/djalmajr/soho/internal/codexenv"
	"github.com/djalmajr/soho/internal/platform"
)

func doctorCodexRegistryPolicy(env platform.Env, s *Say) {
	home := env.Get("CODEX_HOME")
	if home == "" {
		home = filepath.Join(platform.HomeDir(platform.Current(), env), ".codex")
	}
	path := filepath.Join(home, "config.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	patterns, present, valid := registryIncludeOnly(string(data))
	if !valid {
		s.Warning("codex: invalid TOML in " + path + "; cannot check shell_environment_policy include_only")
		return
	}
	if !present {
		return
	}
	if !includeOnlyAdmits(patterns, "SOHO_REGISTRY_DIR") {
		s.Warning(fmt.Sprintf(`codex: [shell_environment_policy] include_only in %s drops SOHO_* variables, so codex workers do not see SOHO_REGISTRY_DIR and register copies and processes in the wrong state dir; add "SOHO_*" to include_only`, path))
	}
	// On native Herdr Core a worker identifies its terminal through
	// HC_TERMINAL_ID; without it, mutation-copy, copies add and procs add
	// fall back to SOHO_AGENT, and register an unverified row when that names
	// no roster agent either.
	if env.Get("SOHO_TRANSPORT") == "hc" && !includeOnlyAdmits(patterns, "HC_TERMINAL_ID") {
		s.Warning(fmt.Sprintf(`codex: [shell_environment_policy] include_only in %s drops HC_TERMINAL_ID, so codex workers cannot identify their terminal; their registrations fall back to SOHO_AGENT and are unverified when that is missing too; add "HC_TERMINAL_ID" to include_only (not "HC_*": it also passes HC_SOCKET, which lets the codex shell drive Core)`, path))
	}
	// SOH-308: an HC_ pattern broader than HC_TERMINAL_ID hands the codex
	// shell the hc socket. Name the HC_ variables it would pass from this
	// environment (names only, never values).
	if env.Get("SOHO_TRANSPORT") == "hc" {
		if pattern := broadHcPattern(patterns); pattern != "" {
			line := fmt.Sprintf(`codex: [shell_environment_policy] include_only in %s has %q, which passes HC_SOCKET too: a codex shell that sees the hc socket can drive Core; list "HC_TERMINAL_ID" instead`, path, pattern)
			if names := admittedHcNames(patterns, env); len(names) > 0 {
				line += "; HC_ variables it would pass from this environment: " + strings.Join(names, ", ")
			}
			s.Warning(line)
		}
	}
}

// broadHcPattern returns the first include_only pattern that is specific to
// hc (it starts with HC_, case-insensitively) and admits more than
// HC_TERMINAL_ID, or "". A catch-all such as "*" is the user's choice to pass
// everything and is not reported here.
func broadHcPattern(patterns []string) string {
	for _, pattern := range patterns {
		if !strings.HasPrefix(strings.ToUpper(pattern), "HC_") || strings.EqualFold(pattern, "HC_TERMINAL_ID") {
			continue
		}
		if codexenv.GlobMatch(pattern, "HC_SOCKET") {
			return pattern
		}
	}
	return ""
}

// admittedHcNames lists, sorted, the names of HC_ variables in env other than
// HC_TERMINAL_ID that the patterns admit. Values are never read.
func admittedHcNames(patterns []string, env platform.Env) []string {
	var names []string
	for name := range env {
		if strings.HasPrefix(name, "HC_") && name != "HC_TERMINAL_ID" && includeOnlyAdmits(patterns, name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func includeOnlyAdmits(patterns []string, name string) bool {
	for _, pattern := range patterns {
		if codexenv.GlobMatch(pattern, name) {
			return true
		}
	}
	return false
}

func registryIncludeOnly(content string) ([]string, bool, bool) {
	return codexenv.RegistryIncludeOnly(content)
}
