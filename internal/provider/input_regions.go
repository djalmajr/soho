package provider

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/djalmajr/soho/internal/text"
)

// ComposerRegionBounds reports the screen line range of the recognized
// composer input (start inclusive, end exclusive), or ok = false when the
// screen is not a recognized composer for the kind. The kind rules are the
// same read-only classifier rules the peer review uses: pi's two '─' input
// borders, the codex '› ' composer prompt within the last 8 non-empty
// lines, and the claude '─' box with its '❯' composer guard. An unknown
// kind or a screen that does not carry the shape is not a recognized
// composer; an unrecognized region is not positive composer proof.
func ComposerRegionBounds(kind, visible string) (start, end int, ok bool) {
	lines := strings.Split(strings.ReplaceAll(visible, "\r\n", "\n"), "\n")
	switch kind {
	case "pi":
		return PiInputRegion(visible)
	case "claude":
		return claudeBoxBounds(lines)
	case "codex":
		if i, found := codexComposerLine(lines); found {
			end := i + 1
			for end < len(lines) && strings.TrimSpace(lines[end]) != "" {
				end++
			}
			return i, end, true
		}
	}
	return 0, 0, false
}

// ComposerRegion reports the lines of the recognized composer input for the
// provider kind and the visible screen, or ok = false when the screen is not
// a recognized composer for the kind (unknown kind, or the kind's composer
// shape not present). Consumers must not treat the visible screen as
// composer input without this recognition. Screen lines are preserved;
// ComposerHoldsPathFor handles Codex continuation indentation and wrap ambiguity.
func ComposerRegion(kind, visible string) ([]string, bool) {
	start, end, ok := ComposerRegionBounds(kind, visible)
	if !ok {
		return nil, false
	}
	lines := strings.Split(strings.ReplaceAll(visible, "\r\n", "\n"), "\n")
	return lines[start:end], true
}

// codexWrappedHoldsPath tries each break as either empty or one space. A screen
// cannot show a separator the renderer hides at a wrap, so a draft differing
// from the typed path only at a wrap point looks identical and is credited.
// This is accepted: the alternative never credits a hard-wrapped pointer, and
// the cost of a wrong Enter is a worker reading a path that does not exist.
func codexWrappedHoldsPath(lines []string, composed string) bool {
	indent := len(lines[0]) - len(strings.TrimLeft(lines[0], " ")) + 2
	parts := append([]string(nil), lines...)
	for i := 1; i < len(parts); i++ {
		n := len(parts[i]) - len(strings.TrimLeft(parts[i], " "))
		if n > indent {
			n = indent
		}
		parts[i] = parts[i][n:]
	}
	for mask := 0; mask < 1<<(len(parts)-1); mask++ {
		input := parts[0]
		inserted := map[int]bool{}
		for i := 1; i < len(parts); i++ {
			if mask&(1<<(i-1)) != 0 {
				inserted[len(input)] = true
				input += " "
			}
			input += parts[i]
		}
		if holdsPathBoundary(input, inserted, composed) {
			return true
		}
	}
	return false
}

// DialogUI reports the name of a known trust, approval, or question UI the
// screen carries for the provider kind ("question", "approval", or "" when
// the screen carries no known dialog UI). The rules reuse the same regexes
// as the blocked-state classification so a dialog refusal never sends a key
// into a dialog input.
func DialogUI(kind, screen string) string {
	lines := strings.Split(strings.ReplaceAll(screen, "\r\n", "\n"), "\n")
	for _, line := range lines {
		l := text.ASCIILower(line)
		if kind == "codex" && codexQuestionRE.MatchString(l) {
			return "question"
		}
		if kind == "claude" {
			if claudeQuestionRE.MatchString(l) {
				return "question"
			}
			if claudeQuestionExtraRE.MatchString(l) {
				return "question"
			}
			if claudeNotQuestionRE.MatchString(l) {
				return "approval"
			}
		}
		if kind == "opencode" {
			if openCodeQuestionRE.MatchString(l) {
				return "question"
			}
			if openCodeQuestionExtraRE.MatchString(l) {
				return "question"
			}
			if openCodeNotQuestionRE.MatchString(l) {
				return "approval"
			}
		}
	}
	return ""
}

// pathContinuationRune reports whether the rune can keep a file path going:
// letters, digits, and the path punctuation - _ . ~ / \. Any other rune —
// whitespace, quotes, parentheses, box glyphs, prose — ends the path.
func pathContinuationRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) ||
		r == '-' || r == '_' || r == '.' || r == '~' || r == '/' || r == '\\'
}

// holdsPathBoundary reports whether text holds composed as a whole path: the
// rune right before and the rune right after the match (when present) must
// both end a path, so a longer path such as
// "…/worker-20261003T152648.md.rejected" never passes for the shorter
// "…/worker-20261003T152648.md". inserted marks the join positions of a
// wrap reassembly; an inserted space is a path boundary only when both
// neighbours are path boundaries — a wrap mid-token is reassembled only by
// the empty join.
func holdsPathBoundary(text string, inserted map[int]bool, composed string) bool {
	if composed == "" {
		return false
	}
	boundaryAt := func(p int) bool {
		if p >= len(text) {
			return true
		}
		r, _ := utf8.DecodeRuneInString(text[p:])
		if pathContinuationRune(r) {
			return false
		}
		if !inserted[p] {
			return true
		}
		if p+1 >= len(text) {
			return true
		}
		r2, _ := utf8.DecodeRuneInString(text[p+1:])
		return !pathContinuationRune(r2)
	}
	boundaryBefore := func(i int) bool {
		if i == 0 {
			return true
		}
		r, _ := utf8.DecodeLastRuneInString(text[:i])
		if pathContinuationRune(r) {
			return false
		}
		if !inserted[i-1] {
			return true
		}
		if i-2 < 0 {
			return true
		}
		r2, _ := utf8.DecodeLastRuneInString(text[:i-1])
		return !pathContinuationRune(r2)
	}
	from := 0
	for {
		i := strings.Index(text[from:], composed)
		if i < 0 {
			return false
		}
		i += from
		if boundaryAt(i+len(composed)) && boundaryBefore(i) {
			return true
		}
		from = i + 1
	}
}

// LineHoldsPath reports whether one screen line holds the exact composed
// prompt path as a whole path (path-boundary aware).
func LineHoldsPath(line, composed string) bool {
	return holdsPathBoundary(line, nil, composed)
}

// ComposerHoldsPathFor reports whether the composer region lines hold the exact
// composed prompt path as a whole path. Codex regions of up to eight lines
// try each break independently after stripping continuation layout spaces.
// Other regions retain the empty join and single-space join. An inserted
// space is a boundary only when both neighbours are path boundaries, so
// a longer path such as "…/worker-…md.rejected" split across lines never
// passes for the shorter path.
func ComposerHoldsPathFor(kind string, lines []string, composed string) bool {
	// Bound the combinations to 128. Larger regions retain the original joins.
	if kind == "codex" && len(lines) > 1 && len(lines) <= 8 && strings.HasPrefix(strings.TrimLeft(lines[0], " "), "› ") {
		return codexWrappedHoldsPath(lines, composed)
	}
	return ComposerHoldsPath(lines, composed)
}

// ComposerHoldsPath checks the original empty and single-space joins without
// provider-specific layout interpretation. Literal input stays literal even
// when its first line happens to begin with a provider's prompt glyph.
func ComposerHoldsPath(lines []string, composed string) bool {
	if holdsPathBoundary(strings.Join(lines, ""), nil, composed) {
		return true
	}
	text := ""
	inserted := map[int]bool{}
	for i, line := range lines {
		if i > 0 {
			inserted[len(text)] = true
			text += " "
		}
		text += line
	}
	return holdsPathBoundary(text, inserted, composed)
}

// ScreenShowsPath reports whether the visible screen shows the path text
// under the wrap reassembly (a substring of the two region-agnostic joins,
// no boundary check). This is the inclusive visibility check for keeping
// the delivery uncertain — not positive composer proof: a longer path that
// contains the path also shows it, and a screen that shows the path keeps
// the delivery uncertain (no Enter, no repeated text).
func ScreenShowsPath(visible, composed string) bool {
	lines := strings.Split(strings.ReplaceAll(visible, "\r\n", "\n"), "\n")
	return strings.Contains(strings.Join(lines, ""), composed) ||
		strings.Contains(strings.Join(lines, " "), composed)
}

// codexComposerLine finds the codex composer prompt line: scanning from the
// end, the first line whose text, without its left spaces, begins with '› '
// (U+203A space) or is just '›', and that line must sit inside the last 8
// non-empty screen lines. The input bounds stop at the first following blank line, which
// separates the input from the status footer. Wrapped input lines remain
// in the region; a footer path cannot authorize Enter on another draft.
// ok is false when no such line is there.
func codexComposerLine(lines []string) (int, bool) {
	nonEmpty := 0
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimFunc(lines[i], isJSWhitespace) == "" {
			continue
		}
		nonEmpty++
		head := strings.TrimLeft(lines[i], " \t")
		if head == "\u203a" || strings.HasPrefix(head, "\u203a ") {
			return i, nonEmpty <= 8
		}
		if nonEmpty >= 8 {
			break
		}
	}
	return -1, false
}

// isClaudeBorderLine reports whether the line is one of Claude Code's
// input-box borders, ignoring surrounding whitespace: only '─' (U+2500),
// or a labeled line starting with '──', ending with '─', with at least
// half of its runes being '─'. Claude's trailing border may be one rune.
func isClaudeBorderLine(line string) bool {
	content := strings.TrimFunc(line, isJSWhitespace)
	if content == "" {
		return false
	}
	runes := []rune(content)
	border := 0
	for _, r := range runes {
		if r == '─' {
			border++
		}
	}
	if border == len(runes) {
		return true
	}
	return border*2 >= len(runes) && strings.HasPrefix(content, "──") && strings.HasSuffix(content, "─")
}

// claudeBoxBounds finds the claude input-box border lines within the last 12
// non-empty lines, where they must be the last two border lines, and returns
// the box line range (start inclusive, end exclusive) after the composer
// guard: the first non-empty line between the borders, without its leading
// whitespace, must start with ❯, and no line below the bottom border may
// start with it — a ❯ below means the composer sits there, and the '─' pair
// is a history separator or a table, not the box.
func claudeBoxBounds(lines []string) (start, end int, ok bool) {
	nonEmpty := 0
	from := len(lines)
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimFunc(lines[i], isJSWhitespace) == "" {
			continue
		}
		nonEmpty++
		from = i
		if nonEmpty == 12 {
			break
		}
	}
	borders := make([]int, 0, 2)
	for i := len(lines) - 1; i >= from; i-- {
		if isClaudeBorderLine(lines[i]) {
			borders = append(borders, i)
			if len(borders) == 2 {
				break
			}
		}
	}
	if len(borders) < 2 {
		return 0, 0, false
	}
	firstInRegion := -1
	for i := borders[1] + 1; i < borders[0]; i++ {
		if strings.TrimFunc(lines[i], isJSWhitespace) != "" {
			firstInRegion = i
			break
		}
	}
	if firstInRegion < 0 || !strings.HasPrefix(strings.TrimFunc(lines[firstInRegion], isJSWhitespace), "\u276f") {
		return 0, 0, false
	}
	for i := borders[0] + 1; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimFunc(lines[i], isJSWhitespace), "\u276f") {
			return 0, 0, false
		}
	}
	return borders[1] + 1, borders[0], true
}
