package reportscan

import (
	"regexp"
	"strings"
)

// Item is one report line carrying an item state marker outside a fenced
// code block and outside the Follow-ups and Ruled out sections. Line is one-based, and Text
// is the line as written, trimmed of surrounding whitespace and CR.
type Item struct {
	Line int
	Mark string
	Text string
}

// LayerEntry is one exact line of the ## Test layers section, in report
// order. Reason is the optional note after the result: a required reason for
// FAIL, NOT RUN and BLOCKED, an ignored note for PASS and n/a.
type LayerEntry struct {
	Name   string
	Result string
	Reason string
}

// Items returns the item lines with the same marker, position, fence and
// section rules PartialCount and EnvCount use. A line yields one item per
// eligible mark: a line carrying both a partial and an env mark yields both,
// while done and skipped are exclusive (a line naming both yields neither).
func Items(text string) []Item {
	if text == "" {
		return []Item{}
	}
	items := []Item{}
	walker := sectionWalker{}
	for index, raw := range strings.Split(text, "\n") {
		section, content := walker.advance(raw)
		if !content || section != sectionOutside {
			continue
		}
		marks := classifyLine(raw)
		text := strings.TrimSpace(strings.TrimRight(raw, "\r"))
		for _, mark := range []string{"partial", "env", "done", "skipped"} {
			if marks[mark] {
				items = append(items, Item{Line: index + 1, Mark: mark, Text: text})
			}
		}
	}
	return items
}

// classifyLine is the one per-line item classification: the marks a line
// carries in item position, using exactly the marker and position rules
// PartialCount and EnvCount apply. partial and env may both be set; done and
// skipped are exclusive, and a line naming both yields no mark at all.
func classifyLine(raw string) map[string]bool {
	marks := map[string]bool{}
	line := lowerASCII(raw)
	if anyDone.MatchString(line) && anySkipped.MatchString(line) {
		return marks
	}
	if markerPositioned(line, partialMarker) {
		marks["partial"] = true
	}
	for _, loc := range envMarker.FindAllStringIndex(line, -1) {
		after := line[loc[1]:]
		if !markerInPosition(line[:loc[0]], after) {
			continue
		}
		if envReason(after) {
			marks["env"] = true
		} else {
			marks["partial"] = true
		}
		break
	}
	if !marks["partial"] && !marks["env"] {
		if markerPositioned(line, doneMarker) {
			marks["done"] = true
		} else if markerPositioned(line, skippedMarker) {
			marks["skipped"] = true
		}
	}
	return marks
}

// markerPositioned reports whether re marks the line in item position.
func markerPositioned(line string, re *regexp.Regexp) bool {
	for _, loc := range re.FindAllStringIndex(line, -1) {
		if markerInPosition(line[:loc[0]], line[loc[1]:]) {
			return true
		}
	}
	return false
}

func markerInPosition(before, after string) bool {
	return stateBefore.MatchString(before) || (cellBefore.MatchString(before) && cellAfter.MatchString(after)) || dashBefore.MatchString(before)
}

// TestLayerEntries returns the exact-result lines of the ## Test layers
// section, outside fences, in report order. It uses the same section and line
// grammar as TestLayers; lines without a valid result are omitted.
func TestLayerEntries(text string) []LayerEntry {
	entries := []LayerEntry{}
	inFence, fenceChar, fenceLen := false, byte(0), 0
	inSection := false
	for _, raw := range strings.Split(text, "\n") {
		if open := fenceOpen.FindStringSubmatch(raw); !inFence && open != nil {
			inFence, fenceChar, fenceLen = true, open[1][0], len(open[1])
			continue
		}
		if inFence {
			if close := fenceClose.FindStringSubmatch(raw); close != nil && close[1][0] == fenceChar && len(close[1]) >= fenceLen {
				inFence = false
			}
			continue
		}
		trim := strings.TrimSpace(raw)
		if m := layerHeadingRE.FindStringIndex(trim); m != nil {
			inSection = strings.TrimSpace(trim[m[0]:]) == "## Test layers"
			continue
		}
		if !inSection || layerIsStructuralRow(trim) || !layerIsListLine(trim) {
			continue
		}
		var probe TestLayersResult
		if !parseLayerLine(trim, &probe) {
			continue
		}
		entry, ok := layerEntry(trim)
		if ok {
			entries = append(entries, entry)
		}
	}
	return entries
}

func layerEntry(trim string) (LayerEntry, bool) {
	m := layerBulletRE.FindStringIndex(trim)
	if m == nil {
		return LayerEntry{}, false
	}
	body := strings.TrimSpace(trim[m[1]:])
	idx := strings.Index(body, ": ")
	if idx <= 0 {
		return LayerEntry{}, false
	}
	name := strings.TrimSpace(body[:idx])
	result, reason, ok := matchLayerResult(strings.TrimSpace(body[idx+2:]))
	if !ok || name == "" {
		return LayerEntry{}, false
	}
	return LayerEntry{Name: name, Result: result, Reason: reason}, true
}
