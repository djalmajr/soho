package dispatch

import (
	"github.com/djalmajr/soho/internal/wait"
	"strings"
)

// BriefType returns the first Type line outside fenced code, accepting the
// same closed vocabulary used by settled-report metrics.
func BriefType(body string) string {
	return wait.BriefType(body)
}

// BriefHasOwnedItems applies the lint's heading aliases and ignores fenced
// examples and empty bullet items when deciding whether scope was assigned.
func BriefHasOwnedItems(body string, aliases map[string][]string) bool {
	lines := strings.Split(body, "\n")
	mask := briefFenceMask(lines)
	owned := false
	for i, line := range lines {
		if mask[i] {
			continue
		}
		if title, ok := briefHeadingTitle(line); ok {
			owned = BriefSectionTitleMatches(title, "Owned files", aliases)
			continue
		}
		if owned {
			item := strings.TrimSpace(line)
			if item == "-" || item == "*" || item == "+" {
				continue
			}
			if item != "" {
				return true
			}
		}
	}
	return false
}
