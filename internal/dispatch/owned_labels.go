package dispatch

import (
	"strings"

	"github.com/djalmajr/soho/internal/gittree"
)

// ownedLabel and OwnedSectionLines are thin wrappers over the one Owned label
// normalizer. gittree is the lower package the audit lives in, so it owns the
// implementation; dispatch keeps its fence grammar in briefFenceMask.
func ownedLabel(line string) (negative, positive bool, remainder string) {
	return gittree.OwnedLabel(line)
}

func ownedLabelValue(value, labelEmphasis string) string {
	return gittree.OwnedLabelValue(value, labelEmphasis)
}

// OwnedSectionLines filters one Owned section, retaining its line positions.
// A negative label excludes itself and subsequent lines until a positive label;
// fenced lines never contribute or change the state.
func OwnedSectionLines(lines []string) ([]string, []bool) {
	mask := briefFenceMask(lines)
	return gittree.OwnedSectionLinesWithMask(lines, mask), mask
}

func hasForbiddenLabel(body string) bool {
	lines := strings.Split(body, "\n")
	mask := briefFenceMask(lines)
	for i, line := range lines {
		if !mask[i] {
			negative, _, _ := ownedLabel(line)
			if negative {
				return true
			}
		}
	}
	return false
}
