package cursorapproval

import (
	"strings"
	"unicode"

	"github.com/djalmajr/soho/internal/startup"
)

// The real dialog lines of cursor-agent 2026.10.22, after the normalization
// below: the warned title, the option labels with their continuations, the
// selection marker and the navigation footer. The listed servers come from
// the workspace .cursor/mcp.json and are deliberately not interpreted.
const (
	dialogTitle      = "MCP Server Approval Required"
	warnedTitle      = "⚠ " + dialogTitle
	approveOption    = "[a] Approve all"
	continueOption   = "[c] Continue"
	quitOption       = "[q] Quit"
	selectionMarker  = "▶ "
	dialogFooter     = "Use arrow keys to navigate, Enter to select, or press the key shown"
	applyingLinePart = "Applying your selection..."
)

// CursorMCPApproval recognizes the active approval, not a transcript above
// a later composer. After the normalization (borders stripped, spaces
// collapsed, blank lines ignored), the screen is an active dialog when the
// end of the screen is an option block — lines starting with the optional
// "▶ " marker or directly with one of "[a] Approve all", "[c] Continue" or
// "[q] Quit" and any label continuation, the old shared-line shape
// included — followed at most by the permitted final lines (the exact
// footer and a line containing "Applying your selection..."), and either a
// title line stands before the block, with or without the "⚠ " prefix, or,
// with no title on the screen (the short panel), the block carries the [a]
// option and the exact footer is present. Any other line after the block
// (composer, report, shell) means the text is quoted, not active: false, as
// before. An option quoted in the middle of prose is not an option line; it
// must start with the marker or the option. Server descriptions between the
// title and the block are deliberately not interpreted.
func CursorMCPApproval(screen string) bool {
	lines := normalizedLines(screen)
	if len(lines) == 0 {
		return false
	}
	// The permitted final lines follow the block in either order, each at
	// most once; any other line after the block supersedes the dialog.
	end, footer, applying := len(lines), false, false
	for {
		if end == 0 {
			break
		}
		line := lines[end-1]
		if !footer && line == dialogFooter {
			footer, end = true, end-1
			continue
		}
		if !applying && strings.Contains(line, applyingLinePart) {
			applying, end = true, end-1
			continue
		}
		break
	}
	if end == 0 {
		return false
	}
	last := end - 1
	if !isOptionLine(lines[last]) {
		return false
	}
	start := last
	for start > 0 && isOptionLine(lines[start-1]) {
		start--
	}
	// (A) a title line stands before the block, with or without the
	// warning prefix.
	for i := 0; i < start; i++ {
		if lines[i] == dialogTitle || lines[i] == warnedTitle {
			return true
		}
	}
	// (B) no title on the screen (the short panel): the block carries the
	// [a] option and the exact footer is present after the block.
	for i := start; i <= last; i++ {
		if strings.HasPrefix(lines[i], approveOption) || strings.HasPrefix(lines[i], selectionMarker+approveOption) {
			return footer
		}
	}
	return false
}

// isOptionLine reports whether line starts with the selection marker (or
// nothing) followed by one of the three options and any label continuation
// ("servers", "without approval"). The old shared-line shape starts with
// the [a] option; a quote in the middle of prose never matches.
func isOptionLine(line string) bool {
	for _, option := range []string{approveOption, continueOption, quitOption} {
		if strings.HasPrefix(line, option) || strings.HasPrefix(line, selectionMarker+option) {
			return true
		}
	}
	return false
}

// normalizedLines applies the existing normalization: the terminal box
// borders are stripped, spaces are collapsed and blank lines ignored.
func normalizedLines(screen string) []string {
	lines := []string{}
	for _, line := range strings.Split(screen, "\n") {
		line = strings.TrimSpace(strings.Trim(strings.TrimSpace(line), "│┃║"))
		if strings.Trim(line, " ┌┐└┘╭╮╯╰─━═┬┴├┤┼+") != "" {
			lines = append(lines, strings.Join(strings.Fields(line), " "))
		}
	}
	return lines
}

// The screen can end with these suffixes when the dialog is clipped by the
// panel or wrapped so that no whole label survives on one line: the
// navigation footer (whole or cut), the applying line and the three option
// labels.
var partialMCPSuffixes = []string{
	"thekeyshown",
	"Applyingyourselection...",
	"[q]Quit",
	"[c]Continuewithoutapproval",
	"[a]Approveallservers",
}

// PartialMCPDialog follows the startup.PartialTrustDialog precedent: it is
// inconclusive evidence, never permission to send input. It recognizes the
// dialog cut by the panel or wrapped so that no whole label survives: the
// borders are stripped as normalizedLines does, all lines are joined and
// every whitespace removed, which undoes any wrapping, and the compact text
// ends with the footer (whole or cut), the applying line or one of the
// option labels. Only the end of the screen counts: a composer or any other
// text after those suffixes keeps the result false, including a quote of the
// dialog above a composer.
func PartialMCPDialog(screen string) bool {
	compact := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, strings.Join(normalizedLines(screen), ""))
	for _, suffix := range partialMCPSuffixes {
		if strings.HasSuffix(compact, suffix) {
			return true
		}
	}
	return false
}

// Evaluate shares the Cursor startup assessment across spawn, dispatch and
// wait. The active MCP approval is never permission to send input, and a
// clipped or wrapped approval dialog is inconclusive: the shared
// assessment's observed verdict would authorize input, so it is refused as
// an incomplete screen. The trust dialog, active or partial, is settled by
// the shared assessment before this check and keeps its result; no other
// kind changes.
func Evaluate(kind, state, screen string, screenOK bool) startup.Evaluation {
	if kind == "cursor" && screenOK && CursorMCPApproval(screen) {
		return startup.Evaluation{Verdict: startup.VerdictBlocked, Reason: "mcp-approval", Source: startup.SourceVisible}
	}
	eval := startup.Evaluate(kind, startup.CurrentPlatform(), state, screen, screenOK)
	if eval.Verdict == startup.VerdictObserved && kind == "cursor" && PartialMCPDialog(screen) {
		return startup.Evaluation{Verdict: startup.VerdictUnverified, Reason: startup.ReasonScreenIncomplete, Source: startup.SourceVisible}
	}
	return eval
}
