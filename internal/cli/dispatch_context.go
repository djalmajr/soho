package cli

import (
	"github.com/djalmajr/soho/internal/provider"
	"regexp"
	"strconv"
	"strings"
)

var dispatchContextFooterRE = regexp.MustCompile(`\d+(?:\.\d+)?K \((\d+)%\)\s+ctrl\+p commands$`)

// dispatchContextPercent scans the visible screen bottom-up over the last
// non-empty lines and returns the context percent the opencode footer shows
// (<number>K (NN%) on the line ending in ctrl+p commands); -1 when there is
// no such line or the pattern does not match.
func dispatchContextPercent(screen string) int {
	lines := strings.Split(strings.ReplaceAll(screen, "\r\n", "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if !strings.HasSuffix(line, "ctrl+p commands") {
			continue
		}
		if m := dispatchContextFooterRE.FindStringSubmatch(line); m != nil {
			if pct, e := strconv.Atoi(m[1]); e == nil {
				return pct
			}
		}
		return -1
	}
	return -1
}

// dispatchContextPercentFor keeps the opencode footer contract and scopes pi
// to the footer below the same recognized input box used by peer delivery.
func dispatchContextPercentFor(kind, screen string) int {
	switch kind {
	case "opencode":
		return dispatchContextPercent(screen)
	case "pi":
		_, end, ok := provider.ComposerRegionBounds("pi", screen)
		if !ok {
			return -1
		}
		lines := strings.Split(strings.ReplaceAll(screen, "\r\n", "\n"), "\n")
		for i := len(lines) - 1; i > end; i-- {
			if m := dispatchPiContextFooterRE.FindStringSubmatch(lines[i]); m != nil {
				// Parse just the whole part, so truncation is exact even near a bound.
				whole, _, _ := strings.Cut(m[1], ".")
				if pct, err := strconv.Atoi(whole); err == nil {
					return pct
				}
				return -1
			}
		}
	}
	return -1
}

var dispatchPiContextFooterRE = regexp.MustCompile(`(?:^|\s)([0-9]+(?:\.[0-9]+)?)%/[0-9]+[kKmM](?:\s|$)`)
