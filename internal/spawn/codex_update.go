package spawn

import (
	"regexp"
	"strings"
)

// Both versions are plain version tokens (the target may wrap onto the
// next line); a partial or malformed capture leaves the warning without
// versions.
var codexUpdateVersionRE = regexp.MustCompile(`(?m)Update available[ \t]*·?[ \t]*([0-9][0-9A-Za-z.+-]*)[ \t]*→\s*([0-9][0-9A-Za-z.+-]*)[ \t]*$`)
var codexUpdateSkipRE = regexp.MustCompile(`^(?:›\s*)?[0-9]+\. Skip(?: until next version)?$`)

// Only the active menu matches: its footer or final Skip option must be
// the last non-empty visible line. A composer below a quoted menu refuses it.
func isCodexUpdatePrompt(screen string) bool {
	var lines []string
	for _, line := range strings.Split(screen, "\n") {
		if line = strings.Join(strings.Fields(line), " "); line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) < 2 {
		return false
	}
	last := lines[len(lines)-1]
	if last != "enter continue · esc skip" && last != "esc skip" && !codexUpdateSkipRE.MatchString(last) {
		return false
	}
	for _, line := range lines[:len(lines)-1] {
		if line == "Update available" || strings.HasPrefix(line, "Update available · ") {
			return true
		}
	}
	return false
}

func codexUpdateVersions(screen string) string {
	if m := codexUpdateVersionRE.FindStringSubmatch(screen); m != nil {
		return " (" + m[1] + " → " + m[2] + ")"
	}
	return ""
}
