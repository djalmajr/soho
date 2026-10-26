package startup

import (
	"fmt"
	"strings"

	"github.com/djalmajr/soho/internal/spawn/codexhooks"
)

// Each shortcut is printed by an existing native-screen fixture. A known
// question without its shortcut is not enough to recommend an answer.
var trustAnswers = []struct {
	kind, key, option string
	match             func(string) bool
}{
	{"cursor", "a", "Trust this workspace", func(screen string) bool {
		return ActiveTrustDialog(screen) && answerLine(screen, "[a] trust this workspace")
	}},
	{"grok", "y", "Yes, proceed (y)", func(screen string) bool {
		return activeGrokTrustChoices(screen)
	}},
	{"codex", "2", "Trust all and continue", func(screen string) bool {
		return codexhooks.IsMenu(screen) && answerLine(screen, "2. trust all and continue")
	}},
}

// Grok's two ordered choices must be the last content on screen; a later
// composer or output makes the choices historical rather than actionable.
func activeGrokTrustChoices(screen string) bool {
	var previous, last string
	for _, line := range strings.Split(screen, "\n") {
		// The content boundary comes from the normalized line itself: a
		// later line holding only a selection marker is still later
		// content. Markers are stripped only to compare the choice text.
		norm := strings.TrimSpace(dialogLine(line))
		if norm == "" {
			continue
		}
		previous, last = last, strings.TrimSpace(strings.TrimLeft(norm, "▶› "))
	}
	return previous == "1. yes, proceed (y)" && last == "2. no, quit (n)"
}

func answerLine(screen, want string) bool {
	for _, line := range strings.Split(screen, "\n") {
		if strings.TrimSpace(strings.TrimLeft(dialogLine(line), "▶› ")) == want {
			return true
		}
	}
	return false
}

// AnswerHint returns only fixture-backed folder/hooks trust shortcuts,
// never a permission approval or an answer inferred from an unknown UI.
func AnswerHint(kind, screen string) (key, option string, ok bool) {
	for _, answer := range trustAnswers {
		if answer.kind == kind && answer.match(screen) {
			return answer.key, answer.option, true
		}
	}
	return "", "", false
}

func formatTrustHint(agent, key, option string) string {
	return fmt.Sprintf("ask the user before answering; to trust it: herdr agent send-keys %s %s (answers %q); then herdr agent wait %s --timeout 60000", agent, key, option, agent)
}

// TrustHint describes a manual action; it never sends the shortcut.
func TrustHint(agent, kind, screen string) string {
	key, option, ok := AnswerHint(kind, screen)
	if !ok {
		return ""
	}
	return formatTrustHint(agent, key, option)
}

// TrustHint preserves the screen assessment for consumers without another
// Herdr read. An MCP approval assessment can never acquire a trust hint.
func (e Evaluation) TrustHint(agent string) string {
	if e.Reason == "mcp-approval" || e.answerKey == "" {
		return ""
	}
	return formatTrustHint(agent, e.answerKey, e.answerOption)
}
