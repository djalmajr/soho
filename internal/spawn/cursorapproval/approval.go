package cursorapproval

import (
	"strings"

	"github.com/djalmajr/soho/internal/startup"
)

// CursorMCPApproval recognizes the active approval, not a transcript above
// a later composer. As with startup.ActiveTrustDialog, the choices must be
// the last content on the visible screen. Server descriptions between the
// exact title and choices are deliberately not interpreted.
func CursorMCPApproval(screen string) bool {
	lines := []string{}
	for _, line := range strings.Split(screen, "\n") {
		line = strings.TrimSpace(strings.Trim(strings.TrimSpace(line), "│┃║"))
		if strings.Trim(line, " ┌┐└┘╭╮╯╰─━═┬┴├┤┼+") != "" {
			lines = append(lines, strings.Join(strings.Fields(line), " "))
		}
	}
	title := -1
	for i, line := range lines {
		if line == "MCP Server Approval Required" {
			title = i
		}
	}
	if title < 0 || title == len(lines)-1 {
		return false
	}
	// The reported options may share a line or occupy separate lines.
	// Only option lines may follow the last choice: a composer, report or
	// shell below the quote supersedes the dialog.
	choice := false
	for i := len(lines) - 1; i > title; i-- {
		line := lines[i]
		optionLine := false
		for _, option := range []string{"[a] Approve all", "[c] Continue", "[q] Quit"} {
			if strings.Contains(line, option) {
				optionLine = true
				line = strings.ReplaceAll(line, option, "")
			}
		}
		if strings.TrimSpace(line) != "" {
			if optionLine {
				return false
			}
			return choice
		}
		choice = choice || optionLine
	}
	return choice
}

// Evaluate shares the Cursor startup assessment across spawn, dispatch and
// wait. The active MCP approval is never permission to send input.
func Evaluate(kind, state, screen string, screenOK bool) startup.Evaluation {
	if kind == "cursor" && screenOK && CursorMCPApproval(screen) {
		return startup.Evaluation{Verdict: startup.VerdictBlocked, Reason: "mcp-approval", Source: startup.SourceVisible}
	}
	return startup.Evaluate(kind, startup.CurrentPlatform(), state, screen, screenOK)
}
