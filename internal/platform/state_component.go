package platform

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// ValidStateComponent accepts one portable directory or file component for private state.
func ValidStateComponent(value string) bool {
	if value == "" || value == "." || value == ".." || !utf8.ValidString(value) ||
		strings.ContainsAny(value, `/\:<>"|?*`) || strings.HasSuffix(value, ".") || strings.HasSuffix(value, " ") {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	// Reserve device stems even with extensions so the same state stays usable across Windows versions.
	stem, _, _ := strings.Cut(value, ".")
	stem = strings.ToUpper(strings.TrimRight(stem, " "))
	switch stem {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
		return false
	}
	for _, prefix := range []string{"COM", "LPT"} {
		if tail, ok := strings.CutPrefix(stem, prefix); ok {
			if len(tail) == 1 && tail[0] >= '1' && tail[0] <= '9' || tail == "¹" || tail == "²" || tail == "³" {
				return false
			}
		}
	}
	return true
}
