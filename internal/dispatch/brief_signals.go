package dispatch

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/djalmajr/soho/internal/wait"
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
// synonyms: ownedPaths and the section lint match every heading. An item
// that declares no files ("None.", "Nenhum. Só leitura.") is not an edit
// request either; one other item in the section still is.
func BriefHasLiteralOwnedItems(body string) bool {
	return briefHasOwnedItems(body, func(title string) bool {
		return startsWithHeadingPrefix(title, []string{"Owned files"}, false)
	}, ownedItemDeclaresNoFiles)
}

var ownedNoneWords = []string{"none", "nenhum", "nenhuma", "nada", "nothing", "n/a"}

// ownedItemDeclaresNoFiles reports an Owned files item that states there are
// no files: after a leading list marker and surrounding whitespace it is
// exactly "—" or "-", or it starts (case-insensitively) with one of the
// none words, which must end there or be followed by a non-letter. A
// backtick-quoted path or a name such as nonexistent.go does not match.
func ownedItemDeclaresNoFiles(item string) bool {
	item = strings.TrimSpace(item)
	if item != "" && strings.ContainsRune("-*+", rune(item[0])) {
		item = strings.TrimSpace(item[1:])
	}
	if item == "—" || item == "-" {
		return true
	}
	lower := strings.ToLower(item)
	for _, word := range ownedNoneWords {
		if !strings.HasPrefix(lower, word) {
			continue
		}
		rest := lower[len(word):]
		if rest == "" {
			return true
		}
		if r, _ := utf8.DecodeRuneInString(rest); !unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

func briefHasOwnedItems(body string, matches func(title string) bool, skip ...func(item string) bool) bool {
	lines := strings.Split(body, "\n")
	mask := briefFenceMask(lines)
	for i := 0; i < len(lines); i++ {
		if mask[i] {
			continue
		}
		heading := strings.TrimSuffix(lines[i], "\r")
		title, ok := briefHeadingTitle(heading)
		if !ok || !matches(title) {
			continue
		}
		level, _ := lintHeadingPrefix(heading)
		end := i + 1
		for end < len(lines) {
			if !mask[end] {
				if next, ok := lintHeadingPrefix(strings.TrimSuffix(lines[end], "\r")); ok && next <= level {
					break
				}
			}
			end++
		}
		filtered, _ := OwnedSectionLines(lines[i+1 : end])
		for _, line := range filtered {
			if _, ok := lintHeadingPrefix(strings.TrimSuffix(line, "\r")); ok {
				continue
			}
			item := strings.TrimSpace(line)
			if item == "" || item == "-" || item == "*" || item == "+" {
				continue
			}
			if len(skip) == 0 || !skip[0](item) {
				return true
			}
		}
		i = end - 1
	}
	return false
}
