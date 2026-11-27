package kinds

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
)

// GrokHooksRisk is the result of the shared Grok/Claude hook compatibility
// check. Inconclusive means the effective Boolean cannot be proved; consumers
// stay quiet rather than guessing.
type GrokHooksRisk uint8

const (
	GrokHooksQuiet GrokHooksRisk = iota
	GrokHooksWarn
	GrokHooksInconclusive
)

// GrokClaudeHooksWarningText names both documented opt-outs.
const GrokClaudeHooksWarningText = "grok may execute the same configured Claude hooks; set [compat.claude] hooks=false in Grok config or GROK_CLAUDE_HOOKS_ENABLED=0 to opt out"

// GrokClaudeHooksRisk checks documented user config and visible Claude hook
// files. It reads only; no executor, config or hook file is changed.
func GrokClaudeHooksRisk(env platform.Env, projectRoot string) GrokHooksRisk {
	enabled, conclusive := grokClaudeCompatibility(env)
	if !conclusive {
		return GrokHooksInconclusive
	}
	if !enabled {
		return GrokHooksQuiet
	}
	configured, conclusive := claudeHooksConfigured(env, projectRoot)
	if !conclusive {
		return GrokHooksInconclusive
	}
	if configured {
		return GrokHooksWarn
	}
	return GrokHooksQuiet
}

// grokClaudeCompatibility resolves the documented compatibility Boolean. The
// environment layer wins over config.toml; other injected config layers stay
// inconclusive because this narrow reader cannot prove their effective value.
func grokClaudeCompatibility(env platform.Env) (bool, bool) {
	if env.Get("GROK_CONFIG") != "" || env.Get("GROK_CONFIG_PATH") != "" {
		return false, false
	}
	if raw, set := env["GROK_CLAUDE_HOOKS_ENABLED"]; set {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return false, false
		}
		return value, true
	}
	grokHome := env.Get("GROK_HOME")
	if grokHome == "" {
		grokHome = filepath.Join(platform.HomeDir(platform.Current(), env), ".grok")
	}
	path := filepath.Join(grokHome, "config.toml")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return true, true
	} else if err != nil {
		return false, false
	}
	raw, err := platform.ReadTextFile(path)
	if err != nil {
		return false, false
	}
	return configClaudeHooks(raw)
}

// configClaudeHooks reads only [compat.claude] hooks = true|false. A present
// key with another value, or conflicting values, is inconclusive.
func configClaudeHooks(raw string) (bool, bool) {
	section := ""
	set := false
	value := false
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") {
			// A different header ends the compatibility table even when its
			// name contains a hash inside a quoted key.
			section = ""
			header, _, _ := strings.Cut(trimmed, "#")
			header = strings.TrimSpace(header)
			if strings.HasSuffix(header, "]") {
				section = strings.TrimSpace(header[1 : len(header)-1])
			}
			continue
		}
		if section != "compat.claude" {
			continue
		}
		at := strings.IndexByte(trimmed, '=')
		if at < 0 {
			continue
		}
		if strings.TrimSpace(trimmed[:at]) != "hooks" {
			continue
		}
		rest := strings.TrimSpace(trimmed[at+1:])
		if comment := strings.IndexByte(rest, '#'); comment >= 0 {
			rest = strings.TrimSpace(rest[:comment])
		}
		parsed, err := strconv.ParseBool(rest)
		if err != nil {
			return false, false
		}
		if set && parsed != value {
			return false, false
		}
		set, value = true, parsed
	}
	if !set {
		return true, true
	}
	return value, true
}

// claudeHooksConfigured checks global and project Claude settings files named
// by the Grok hook documentation. It returns only whether any hook is
// configured, never hook commands or settings.
func claudeHooksConfigured(env platform.Env, projectRoot string) (bool, bool) {
	roots := []string{platform.HomeDir(platform.Current(), env)}
	if projectRoot != "" {
		roots = append(roots, projectRoot)
	}
	configured := false
	for _, root := range roots {
		for _, rel := range []string{".claude/settings.json", ".claude/settings.local.json"} {
			path := filepath.Join(root, filepath.FromSlash(rel))
			info, err := os.Stat(path)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil || !info.Mode().IsRegular() {
				return false, false
			}
			raw, err := platform.ReadTextFile(path)
			if err != nil {
				return false, false
			}
			found, valid := claudeHooksInJSON(raw)
			if !valid {
				return false, false
			}
			configured = configured || found
		}
	}
	return configured, true
}

func claudeHooksInJSON(raw string) (bool, bool) {
	value, err := jsonjs.Parse([]byte(raw))
	if err != nil {
		return false, false
	}
	root, ok := value.(*jsonjs.Object)
	if !ok {
		return false, false
	}
	hooksValue, ok := root.Get("hooks")
	if !ok {
		return false, true
	}
	hooks, ok := hooksValue.(*jsonjs.Object)
	if !ok {
		return false, true
	}
	for _, event := range hooks.Keys() {
		rows, ok := hooks.Get(event)
		if !ok {
			continue
		}
		eventRows, ok := rows.([]any)
		if !ok || len(eventRows) == 0 {
			continue
		}
		for _, row := range eventRows {
			entry, ok := row.(*jsonjs.Object)
			if !ok {
				continue
			}
			list, ok := entry.Get("hooks")
			if !ok {
				continue
			}
			items, ok := list.([]any)
			if ok && len(items) > 0 {
				return true, true
			}
		}
	}
	return false, true
}
