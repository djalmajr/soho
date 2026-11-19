package dispatch

import "strings"

// declaredBaselineValue returns the value of the first Baseline: line outside
// fenced code blocks, read the way declaredReviewValue reads Review:: the ref
// between backticks when the value starts with one (an unclosed quote is
// absent), else the first field. found is true when a Baseline: line was
// present, even when its value is empty. The lint decides what a missing or
// empty value means; the ref is never resolved here.
func declaredBaselineValue(body string) (value string, found bool) {
	lines := strings.Split(body, "\n")
	fenced := briefFenceMask(lines)
	for i, line := range lines {
		if fenced[i] || !strings.HasPrefix(line, "Baseline:") {
			continue
		}
		raw := strings.TrimSpace(strings.TrimPrefix(line, "Baseline:"))
		if raw == "" {
			return "", true
		}
		if rest, quoted := strings.CutPrefix(raw, "`"); quoted {
			ref, _, closed := strings.Cut(rest, "`")
			if !closed {
				return "", true
			}
			return ref, true
		}
		if fields := strings.Fields(raw); len(fields) > 0 {
			return fields[0], true
		}
		return "", true
	}
	return "", false
}
