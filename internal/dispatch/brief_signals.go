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
	return briefHasOwnedItems(body, func(title string) bool {
		return BriefSectionTitleMatches(title, "Owned files", aliases)
	})
}

// BriefHasLiteralOwnedItems is the read-only role's edit-request signal:
// only the literal "Owned files" title counts (the lint's own title
// comparison, so it keeps the normalized prefix behavior and the
// case-insensitivity) - never the synonyms or the configured aliases, so a
// scope list under "Arquivos" refuses nothing. Edit roles keep the
// synonyms: ownedPaths and the section lint match every heading.
func BriefHasLiteralOwnedItems(body string) bool {
	return briefHasOwnedItems(body, func(title string) bool {
		return startsWithHeadingPrefix(title, []string{"Owned files"}, false)
	})
}

func briefHasOwnedItems(body string, matches func(title string) bool) bool {
	lines := strings.Split(body, "\n")
	mask := briefFenceMask(lines)
	owned := false
	for i, line := range lines {
		if mask[i] {
			continue
		}
		if title, ok := briefHeadingTitle(line); ok {
			owned = matches(title)
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
