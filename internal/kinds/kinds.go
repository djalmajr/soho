package kinds

import (
	"fmt"
	"strings"

	"github.com/djalmajr/soho/internal/platform"
)

var KnownKinds = []string{"claude", "codex", "grok", "agy", "gemini", "cursor", "pi", "opencode"}
var families = []string{"anthropic", "openai", "xai", "google", "alibaba"}

func KindFamily(kind string) string {
	switch kind {
	case "claude":
		return "anthropic"
	case "codex":
		return "openai"
	case "grok":
		return "xai"
	case "agy", "gemini":
		return "google"
	default:
		return "unknown"
	}
}

func KindFamilyDisplay(kind string) string {
	if kind == "cursor" || kind == "pi" || kind == "opencode" {
		return "by model"
	}
	return KindFamily(kind)
}

func AgentFamily(kind, model string) string {
	fam := KindFamily(kind)
	if fam != "unknown" || model == "" {
		return fam
	}
	parts := strings.Split(model, "/")
	for _, part := range parts {
		for _, known := range families {
			if part == known {
				return part
			}
		}
	}
	last := parts[len(parts)-1]
	switch {
	case strings.HasPrefix(strings.ToLower(last), "qwen"):
		return "alibaba"
	case strings.HasPrefix(last, "claude-"):
		return "anthropic"
	case strings.HasPrefix(last, "gpt-") || strings.Contains(last, "codex"):
		return "openai"
	case strings.HasPrefix(last, "grok-"):
		return "xai"
	case strings.HasPrefix(last, "gemini-"):
		return "google"
	default:
		return "unknown"
	}
}

func KindExe(kind string) string {
	if kind == "cursor" {
		return "cursor-agent"
	}
	return kind
}

func EffortRank(effort string) int {
	switch effort {
	case "low":
		return 1
	case "medium":
		return 2
	case "high":
		return 3
	case "xhigh":
		return 4
	case "max":
		return 5
	default:
		return 0
	}
}

func KindEffortCeiling(kind string) string {
	switch kind {
	case "claude", "pi", "codex":
		return "max"
	case "cursor", "grok":
		return "xhigh"
	case "agy", "gemini":
		return "high"
	default:
		return ""
	}
}

func ClampTo(effort, ceiling string) string {
	if ceiling != "" && EffortRank(effort) > EffortRank(ceiling) {
		return ceiling
	}
	return effort
}

type WarnFunc func(string)

func DefaultWarn(msg string) { _, _ = fmt.Fprintf(platform.Stderr, "soho: warning: %s\n", msg) }

func KindEffortArgs(kind, effort, model string, env platform.Env, warn WarnFunc) []string {
	if effort == "" {
		return nil
	}
	if warn == nil {
		warn = DefaultWarn
	}
	switch kind {
	case "claude":
		return []string{"--effort", effort}
	case "codex":
		return []string{"-c", `model_reasoning_effort="` + effort + `"`}
	case "grok":
		return []string{"--reasoning-effort", effort}
	case "agy", "gemini":
		if effortSuffix(model) {
			return nil
		}
		if model == "" || strings.HasPrefix(model, "gemini") {
			return []string{"--effort", effort}
		}
		warn(fmt.Sprintf("%s model '%s' takes no --effort; effort '%s' ignored", kind, model, effort))
		return nil
	case "cursor":
		if model != "" {
			return []string{"--model", CursorModelWithEffort(model, effort, env, warn)}
		}
		warn("cursor ignores --effort without --model (pick an id from: cursor-agent --list-models)")
	case "pi":
		return []string{"--thinking", effort}
	case "opencode":
		warn(fmt.Sprintf("opencode TUI takes no effort flag (--variant is only in 'opencode run'); effort '%s' ignored", effort))
	default:
		warn(fmt.Sprintf("no effort mapping for kind '%s'; effort ignored (pass the native flag after --)", kind))
	}
	return nil
}

// KindMcpArgs translates the per-spawn MCP choice into the kind's static
// per-process flag (DJA-392): mcp=none turns the user's MCP servers off for
// this process, inherit (or anything else) changes nothing. The ok result
// reports whether the kind has a static flag at all: when it does not, the
// caller warns once and the agent keeps the user's MCP servers. Codex has
// no static flag — its off args are dynamic (one
// -c mcp_servers.<name>.enabled=false pair per enabled server, built by the
// spawn from `codex mcp list --json`) — so it reports not-ok for mcp=none
// and the spawn handles it.
func KindMcpArgs(kind, mcp string) (args []string, ok bool) {
	if mcp != "none" {
		return nil, true
	}
	switch kind {
	case "claude":
		return []string{"--strict-mcp-config"}, true
	case "pi":
		return []string{"--no-mcp"}, true
	default:
		return nil, false
	}
}

// KindMcpOff reports whether the kind can turn the user's MCP servers off
// per process (the `soho kinds` table column): claude and pi through a
// static flag, codex through a per-server override built from its live
// listing. Both count.
func KindMcpOff(kind string) bool {
	switch kind {
	case "claude", "codex", "pi":
		return true
	default:
		return false
	}
}

func KindModelArgs(kind, model, effort string, warn WarnFunc) []string {
	if model == "" {
		return nil
	}
	if warn == nil {
		warn = DefaultWarn
	}
	switch kind {
	case "claude", "agy", "gemini", "grok", "cursor", "pi":
		if kind == "cursor" && effort != "" {
			return nil
		}
		return []string{"--model", model}
	case "codex", "opencode":
		return []string{"-m", model}
	default:
		warn(fmt.Sprintf("no model mapping for kind '%s'; model ignored", kind))
		return nil
	}
}

func KindApprovalArgs(kind, mode string, warn WarnFunc) ([]string, error) {
	if mode == "" || mode == "ask" {
		return nil, nil
	}
	if mode != "edits" && mode != "full" {
		return nil, fmt.Errorf("invalid approvals '%s' (ask|edits|full)", mode)
	}
	if warn == nil {
		warn = DefaultWarn
	}
	values := map[string][]string{
		"claude:edits": {"--permission-mode", "acceptEdits"}, "claude:full": {"--permission-mode", "bypassPermissions", "--settings", `{"enableAllProjectMcpServers":true}`},
		"codex:edits": {"-s", "workspace-write", "-a", "on-request"}, "codex:full": {"-s", "workspace-write", "-a", "never"},
		"grok:edits": {"--permission-mode", "acceptEdits"}, "grok:full": {"--permission-mode", "bypassPermissions", "--always-approve"},
		"agy:edits": {"--mode", "accept-edits"}, "gemini:edits": {"--mode", "accept-edits"},
		"agy:full": {"--dangerously-skip-permissions"}, "gemini:full": {"--dangerously-skip-permissions"},
		"cursor:edits": {"--trust", "--auto-review"}, "cursor:full": {"--trust", "--force", "--approve-mcps"},
		"opencode:full": {"--auto"},
	}
	key := kind + ":" + mode
	if args, ok := values[key]; ok {
		return append([]string(nil), args...), nil
	}
	switch key {
	case "pi:full":
		return nil, nil
	case "pi:edits":
		warn("pi has no approval prompts (its tools run as-is); approvals=edits is a no-op (restrict tools with --tools/--exclude-tools after --)")
	case "opencode:edits":
		warn("opencode has no edits approvals flag; use approvals=full (--auto) or per-tool permissions in opencode.json")
	default:
		warn(fmt.Sprintf("no approvals mapping for kind '%s'; pass the native flag after --", kind))
	}
	return nil, nil
}

func KindContextArgs(kind, mode string) []string {
	if mode != "lean" {
		return nil
	}
	switch kind {
	case "codex":
		return []string{"-c", "project_doc_max_bytes=0"}
	case "claude":
		return []string{"--disable-slash-commands"}
	default:
		return nil
	}
}

func KindSummary(kind string) string {
	switch kind {
	case "grok":
		return "Best at writing code, bulk edits, and research. Recommended for implementation and research."
	case "cursor":
		return "Also runs Grok models. Second choice for implementation and research."
	case "codex":
		return "Strong at review and judgement. Recommended for review when implementation uses Grok or Cursor."
	case "claude":
		return "Strong at security review and at leading the team. Recommended for security review, and for review when Codex is not installed."
	case "agy":
		return "Reads screens well. Recommended for design and visual checks."
	case "gemini":
		return "Same screen-reading family as agy. Use for design and visual checks when agy is not installed."
	case "pi":
		return "Generic multi-model harness (pi). Set a provider/model id in the config; effort via --thinking; it has no approval prompts."
	case "opencode":
		return "Generic multi-model harness (opencode). Set a provider/model id in the config; its TUI maps no effort flag; unattended runs use --auto."
	default:
		return "Installed assistant. Use it when a recommended one is not installed."
	}
}

func CmdKinds(env platform.Env, platformName string) {
	row := func(a, b, c, d, e, f string) string {
		return fmt.Sprintf("%-8s %-13s %-10s %-8s %-9s %s", a, b, c, d, e, f)
	}
	lines := []string{row("KIND", "EXECUTABLE", "FAMILY", "EFFORT", "INSTALLED", "MCP")}
	for _, k := range KnownKinds {
		installed := "no"
		if _, ok := platform.FindExecutable(KindExe(k), env, platformName); ok {
			installed = "yes"
		}
		mcp := "no"
		if KindMcpOff(k) {
			mcp = "yes"
		}
		lines = append(lines, row(k, KindExe(k), KindFamilyDisplay(k), KindEffortCeiling(k), installed, mcp))
	}
	_, _ = fmt.Fprintln(platform.Stdout, strings.Join(lines, "\n"))
}

func effortSuffix(model string) bool {
	for _, suffix := range []string{"-minimal", "-low", "-medium", "-high", "-xhigh", "-max"} {
		if strings.HasSuffix(model, suffix) {
			return true
		}
	}
	return false
}
