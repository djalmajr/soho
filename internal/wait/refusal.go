package wait

import (
	"fmt"
	"strings"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/transport"
)

// providerRefusalMarkers is the single list of provider texts wait recognizes
// as a refusal. A marker is a case-insensitive substring of the last 40 lines
// of the worker's screen. Only the fixed text from this list is ever printed,
// never the surrounding (untrusted) screen content.
var providerRefusalMarkers = []string{
	"We take extra care with some cybersecurity requests",
	"This content can’t be shown",
	"This content can't be shown",
}

const refusalScreenLines = 40

// ProviderRefusalMarker returns the fixed marker present in the last 40 lines
// of screen, or "" when none of the markers is there. The match is
// case-insensitive; a marker split across lines is not a substring and is not
// recognized.
func ProviderRefusalMarker(screen string) string {
	lines := strings.Split(screen, "\n")
	start := 0
	if len(lines) > refusalScreenLines {
		start = len(lines) - refusalScreenLines
	}
	tail := strings.ToLower(strings.Join(lines[start:], "\n"))
	for _, marker := range providerRefusalMarkers {
		if strings.Contains(tail, strings.ToLower(marker)) {
			return marker
		}
	}
	return ""
}

// fallbackCommand builds the exact command wait prints after a refusal. The
// configured value is `kind model effort`; an empty value names a concrete
// other assistant and lets spawn resolve its model and effort.
func fallbackCommand(agent, kind, configured string) string {
	restart := "soho reopen " + agent
	defaultKind := "claude"
	if kind == "claude" {
		defaultKind = "codex"
	}
	if configured == "" {
		return restart + " --kind " + defaultKind + " --redispatch"
	}
	fields := strings.Fields(configured)
	if len(fields) != 3 {
		return restart + " --kind " + defaultKind + " --redispatch"
	}
	return fmt.Sprintf("%s --kind %s --model %s --effort %s --redispatch", restart, fields[0], fields[1], fields[2])
}

// refusalAtSettlement reads that worker's own screen once. It adds no writes.
// A read failure or a screen without a marker leaves the settled line as it
// was; a match yields the replacement cause, the next command and the marker
// the warning names.
func refusalAtSettlement(sd, agent string, ctx *core.Config, env platform.Env, cwd, role string, svc transport.Services) (marker, cause, next string) {
	screen, ok := readScreen(svc, agent, "recent-unwrapped", intPtr(refusalScreenLines))
	if !ok {
		return "", "", ""
	}
	marker = ProviderRefusalMarker(screen)
	if marker == "" {
		return "", "", ""
	}
	key := "fallback_edit"
	if !core.RoleIsEditReadOnly(role, env, cwd) {
		key = "fallback_review"
	}
	kind := field(strings.Split(core.RosterLine(sd, agent), "\t"), 2)
	return marker, "the provider refused this task: " + marker, fallbackCommand(agent, kind, core.Cfg(ctx, key, "", env))
}
