package dispatch

import (
	"regexp"
	"strings"
)

var ownedLabelListMarker = regexp.MustCompile(`^(?:[-*+]|[0-9]+[.)])[ \t]+`)

// ownedLabel identifies a fixed label at the start of a line. The returned
// remainder excludes the label and closing emphasis so an empty label is not
// mistaken for an ownership item.
func ownedLabel(line string) (negative, positive bool, remainder string) {
	line = strings.TrimSpace(line)
	line = ownedLabelListMarker.ReplaceAllString(line, "")
	line = strings.TrimLeft(line, " \t*_")
	colon := strings.IndexByte(line, ':')
	if colon < 0 {
		return false, false, ""
	}
	name := normalizeBriefHeading(strings.TrimSpace(strings.TrimRight(line[:colon], "*_ \t")))
	switch name {
	case "proibido", "forbidden", "nao tocar", "do not touch", "fora do escopo", "out of scope":
		return true, false, ""
	case "pode tocar", "owned", "may edit", "arquivos proprios", "arquivos de posse":
		return false, true, strings.TrimLeft(line[colon+1:], "*_ \t")
	default:
		return false, false, ""
	}
}

// OwnedSectionLines filters one Owned section, retaining its line positions.
// A negative label excludes itself and subsequent lines until a positive label;
// fenced lines never contribute or change the state.
func OwnedSectionLines(lines []string) ([]string, []bool) {
	mask := briefFenceMask(lines)
	out := make([]string, len(lines))
	excluded := false
	for i, line := range lines {
		if mask[i] {
			continue
		}
		negative, positive, rest := ownedLabel(line)
		if negative {
			excluded = true
			continue
		}
		if positive {
			excluded = false
			// The rest keeps a list marker, so a bare path after the label
			// ("- Owned: src/ui/app.go") is still read as an owned item
			// (review R377); a backtick path reads the same either way.
			if strings.TrimSpace(rest) != "" {
				out[i] = "- " + rest
			}
			continue
		}
		if !excluded {
			out[i] = line
		}
	}
	return out, mask
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
