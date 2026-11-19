package reportscan

import "strings"

// Item is one report line carrying an item state marker outside a fenced
// code block and outside the Follow-ups section. Line is one-based, and Text
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
// Follow-ups rules PartialCount and EnvCount use. A line yields at most one
// item; a line with both done and skipped yields none.
func Items(text string) []Item {
	if text == "" {
		return []Item{}
	}
	items := []Item{}
	inFence, fenceChar, fenceLen := false, byte(0), 0
	inFollowups, inComment := false, false
	for index, raw := range strings.Split(text, "\n") {
		open := fenceOpen.FindStringSubmatch(raw)
		if !inFence && open != nil {
			inFence, fenceChar, fenceLen = true, open[1][0], len(open[1])
			continue
		}
		if inFence {
			close := fenceClose.FindStringSubmatch(raw)
			if close != nil && close[1][0] == fenceChar && len(close[1]) >= fenceLen {
				inFence = false
			}
			continue
		}
		hidden := inComment
		if inComment {
			if strings.Contains(raw, "-->") {
				inComment = false
			}
		} else if at := strings.Index(raw, "<!--"); at >= 0 && !strings.Contains(raw[at+4:], "-->") {
			inComment = true
		}
		var heading []string
		if !hidden {
			heading = limitHeadingRE.FindStringSubmatch(strings.TrimRight(raw, "\r"))
		}
		if heading != nil {
			title := lowerASCII(strings.TrimSpace(heading[2]))
			if len(heading[1]) == 2 && (title == "follow-ups" || title == "follow ups" || title == "followups") {
				inFollowups = true
				continue
			}
			if len(heading[1]) <= 2 {
				inFollowups = false
			}
		} else if inFollowups {
			continue
		}
		line := lowerASCII(raw)
		if anyDone.MatchString(line) && anySkipped.MatchString(line) {
			continue
		}
		mark := ""
		for _, loc := range partialMarker.FindAllStringIndex(line, -1) {
			if markerInPosition(line[:loc[0]], line[loc[1]:]) {
				mark = "partial"
				break
			}
		}
		if mark == "" {
			for _, loc := range envMarker.FindAllStringIndex(line, -1) {
				if !markerInPosition(line[:loc[0]], line[loc[1]:]) {
					continue
				}
				if envReason(line[loc[1]:]) {
					mark = "env"
				} else {
					mark = "partial"
				}
				break
			}
		}
		if mark == "" {
			switch {
			case anyDone.MatchString(line):
				mark = "done"
			case anySkipped.MatchString(line):
				mark = "skipped"
			}
		}
		if mark != "" {
			items = append(items, Item{Line: index + 1, Mark: mark, Text: strings.TrimSpace(strings.TrimRight(raw, "\r"))})
		}
	}
	return items
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
