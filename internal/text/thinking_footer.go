package text

import (
	"regexp"
	"strings"
)

// ThinkingFooterRE matches a thinking level at the end of a footer line only
// when a boundary precedes it (start of line, whitespace, bullet or colon),
// so a word like `cutoff` does not read as `off`. Compact and spawn share it.
var ThinkingFooterRE = regexp.MustCompile(`(^|[\s•:])(off|minimal|low|medium|high|xhigh|max)\s*$`)

// ThinkingFooterLevel returns the thinking level the last bullet line of a
// TUI screen ends with, or "" when that line has no level or no bullet exists.
// A level on an earlier line (scrolled history) is not a reading.
func ThinkingFooterLevel(screen string) string {
	lines := strings.Split(strings.ReplaceAll(screen, "\r\n", "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if !strings.Contains(line, "•") {
			continue
		}
		if m := ThinkingFooterRE.FindStringSubmatch(line); m != nil {
			return m[2]
		}
		return ""
	}
	return ""
}
