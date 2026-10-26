package provider

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/djalmajr/soho/internal/text"
)

type Detection struct {
	Status string `json:"status"`
	Cause  string `json:"cause"`
	Auth   bool   `json:"auth"`
}

// HarnessModuleCause is the cause of a CLI that cannot load one of its own
// files (the "Error: Cannot find module ... imported from" report, often right
// after the CLI was updated while it ran). The cause is always this fixed
// text, never the file path.
const HarnessModuleCause = "Error: Cannot find module"

var (
	retryingRE       = regexp.MustCompile(`retrying|retry in|will retry|reconnecting`)
	errorTailRetryRE = regexp.MustCompile(`error.*(·|-)[ \t]*retrying`)
	retryCounterRE   = regexp.MustCompile(`retrying.*\(\d+/\d+`)
	retryDeadlineRE  = regexp.MustCompile(`\bin \d`)
	econnRE          = regexp.MustCompile(`\bECONN(REFUSED|RESET)\b`)
	providerStatusRE = regexp.MustCompile(`\b(500|502|503|504)\b` + jsWhitespace + `*[:{(]`)
	capacity529RE    = regexp.MustCompile(`\b529\b`)
	auth401RE        = regexp.MustCompile(`\b401\b`)
	errorStartRE     = regexp.MustCompile(`^` + jsWhitespace + `*([■✗✘×⚠●•⎿]` + jsWhitespace + `+)?(API Error|ERROR|Error)[: (]`)
	errorGlyphRE     = regexp.MustCompile(`^` + jsWhitespace + `*■` + jsWhitespace)
	outputBulletRE   = regexp.MustCompile(`^` + jsWhitespace + `*[•●⏺✓✔]` + jsWhitespace)
	capacityRes      = []*regexp.Regexp{
		regexp.MustCompile(`"type"` + jsWhitespace + `*:` + jsWhitespace + `*"[a-z0-9_]*(capacity|overload)[a-z0-9_]*"`),
		regexp.MustCompile(`\boverloaded\b`),
		regexp.MustCompile(`\b(at|over) capacity\b`),
	}
	providerRes = []*regexp.Regexp{
		regexp.MustCompile(`request timed out`),
		regexp.MustCompile(`connection error`),
		regexp.MustCompile(`connection (refused|reset)`),
		regexp.MustCompile(`retry failed after \d+ attempts?`),
		regexp.MustCompile(`\b(internal server error|bad gateway|service unavailable|gateway time-?out)\b`),
		regexp.MustCompile(`unexpected status`),
		regexp.MustCompile(`stream disconnected before completion`),
		regexp.MustCompile(`socket hang up`),
		regexp.MustCompile(`fetch failed`),
	}
	authRes = []*regexp.Regexp{
		regexp.MustCompile(`unexpected status[^\r\x{2028}\x{2029}]*401|401[^\r\x{2028}\x{2029}]*unexpected status`),
		regexp.MustCompile(`incorrect api key`),
		regexp.MustCompile(`refresh token[^\r\x{2028}\x{2029}]*revok|revok[^\r\x{2028}\x{2029}]*refresh token`),
		regexp.MustCompile(`failed to refresh[^\r\x{2028}\x{2029}]*token`),
	}
)

// ProviderDetect returns the most recent provider stop from the bottom ten non-empty lines.
func ProviderDetect(state, screen string) *Detection {
	return ProviderDetectTexts(state, screen, "", "")
}

// ProviderDetectTexts is ProviderDetect with the user-configured capacity and
// error texts (pipe-separated): a line containing one of them, case-insensitively
// and without the leading box prefix and spaces, is a capacity or
// provider-error stop even without the Error prefix. The existing rules win;
// an empty text changes nothing.
func ProviderDetectTexts(state, screen, capacityTexts, errorTexts string) *Detection {
	if d := LostConnection(screen); d != nil {
		return d
	}
	if state == "working" || screen == "" {
		return nil
	}
	capacity := splitTexts(capacityTexts)
	errTexts := splitTexts(errorTexts)
	lines := strings.Split(strings.ReplaceAll(screen, "\r\n", "\n"), "\n")
	nonEmpty := make([]string, 0, len(lines))
	for _, line := range lines {
		if !isJSEmpty(line) {
			nonEmpty = append(nonEmpty, line)
		}
	}
	if len(nonEmpty) > 10 {
		nonEmpty = nonEmpty[len(nonEmpty)-10:]
	}
	for i := len(nonEmpty) - 1; i >= 0; i-- {
		line := nonEmpty[i]
		lowerLine := text.ASCIILower(line)
		if strings.HasPrefix(configuredTextLine(line), "we take extra care with some cybersecurity requests") {
			return &Detection{Status: "provider-refusal", Cause: text.SanitizeCause(text.RedactSecrets(line))}
		}
		if retryingRE.MatchString(lowerLine) || QuotaLineIsCode(line) {
			continue
		}
		if outputBulletRE.MatchString(line) && !errorStartRE.MatchString(line) {
			return nil
		}
		if errorGlyphRE.MatchString(line) || errorStartRE.MatchString(line) {
			cause := text.SanitizeCause(text.RedactSecrets(line))
			if matchesAny(capacityRes, lowerLine) || capacity529RE.MatchString(line) {
				return &Detection{Status: "capacity", Cause: cause}
			}
			if auth401RE.MatchString(line) || matchesAny(authRes, lowerLine) {
				return &Detection{Status: "provider-error", Cause: cause, Auth: true}
			}
			if matchesAny(providerRes, lowerLine) || providerStatusRE.MatchString(line) || econnRE.MatchString(line) {
				return &Detection{Status: "provider-error", Cause: cause}
			}
		}
		if harnessModuleCause(line, nonEmpty, i) {
			return &Detection{Status: "provider-error", Cause: HarnessModuleCause}
		}
		stripped := configuredTextLine(line)
		if equalsText(stripped, capacity) {
			return &Detection{Status: "capacity", Cause: text.SanitizeCause(text.RedactSecrets(line))}
		}
		if equalsText(stripped, errTexts) {
			return &Detection{Status: "provider-error", Cause: text.SanitizeCause(text.RedactSecrets(line))}
		}
	}
	return nil
}

const LostConnectionCause = "codex lost its server connection; run soho reopen <agent> --force and dispatch the task again with a resume note (the worktree keeps its diff)"

// LostConnection detects only a terminal reconnect failure, including while working.
// Other provider rules must retain their non-working guard.
func LostConnection(screen string) *Detection {
	lines := strings.Split(strings.ReplaceAll(screen, "\r\n", "\n"), "\n")
	count := 0
	for i := len(lines) - 1; i >= 0; i-- {
		if isJSEmpty(lines[i]) {
			continue
		}
		count++
		if count > 10 {
			break
		}
		line := configuredTextLine(lines[i])
		if strings.HasPrefix(line, "server connection could not be restored") || strings.HasPrefix(line, "automatic reconnect could not restore this session") {
			return &Detection{Status: "provider-error", Cause: LostConnectionCause}
		}
	}
	return nil
}

// RetryLine returns the most recent provider-retry line of a working agent:
// the bottom ten non-empty screen lines, read upward without the leading box
// prefix, the first that is a provider retry (retryLineReports), sanitized
// like a cause. A retry is not a terminal stop (ProviderDetect skips the
// line); this surfaces it. The model's own wording about retrying (no retry
// counter and not the Reconnecting start) is not a provider retry. Empty when
// the state is not working or no line matches.
func RetryLine(state, screen string) string {
	if state != "working" {
		return ""
	}
	lines := strings.Split(strings.ReplaceAll(screen, "\r\n", "\n"), "\n")
	nonEmpty := make([]string, 0, len(lines))
	for _, line := range lines {
		value := stripBoxPrefix(line)
		if !isJSEmpty(value) {
			nonEmpty = append(nonEmpty, value)
		}
	}
	if len(nonEmpty) > 10 {
		nonEmpty = nonEmpty[len(nonEmpty)-10:]
	}
	for i := len(nonEmpty) - 1; i >= 0; i-- {
		if retryLineReports(nonEmpty[i]) {
			return text.SanitizeCause(text.RedactSecrets(nonEmpty[i]))
		}
	}
	return ""
}

func stripBoxPrefix(line string) string {
	value := strings.TrimLeft(line, " \t")
	if strings.HasPrefix(value, "┃") {
		return strings.TrimLeft(value[len("┃"):], " \t")
	}
	return value
}

// stripStatusGlyph removes one leading status glyph (the activity and error
// bullets the TUI prints before a line) and the spaces after it, the way
// stripBoxPrefix removes the box prefix and the spaces. A line without a
// leading glyph is returned unchanged.
func stripStatusGlyph(value string) string {
	if value == "" {
		return value
	}
	r, size := utf8.DecodeRuneInString(value)
	switch r {
	case '•', '●', '⏺', '✓', '✔', '■':
		return strings.TrimLeft(value[size:], " \t")
	}
	return value
}

// retryLineReports says whether the box-stripped line is a provider retry,
// not the model talking about its own retry. It counts only when the line,
// after the box prefix, the spaces and a single status glyph, starts with
// Retrying, Reconnecting or Will retry, or carries a `· Retrying`/`- Retrying`
// tail after an Error, or carries a `retrying` followed later by an `n/m`
// counter right after a `(` (codex's mid-line "retrying sampling request
// (1/5 ...)") (a), AND it carries a digit or starts with Reconnecting (b).
// The line-start Retrying/Will retry forms tighten (b): they need that
// `n/m` counter right after a `(` or a deadline such as `in 4s` or
// `in 30 seconds`, so "retrying the 1/2 migration" and "Retrying 2 files"
// fail. A Reconnecting start counts without a number.
func retryLineReports(line string) bool {
	lower := text.ASCIILower(stripStatusGlyph(line))
	leading := strings.HasPrefix(lower, "retrying") ||
		strings.HasPrefix(lower, "reconnecting") ||
		strings.HasPrefix(lower, "will retry")
	if !leading && !errorTailRetryRE.MatchString(lower) && !retryCounterRE.MatchString(lower) {
		return false
	}
	if strings.HasPrefix(lower, "reconnecting") {
		return true
	}
	// Every other form needs what a provider prints: an (n/m counter or a
	// deadline ("in 4s"). Any digit was not enough: "I hit an error -
	// retrying the 1/2 migration" is the model talking.
	return retryCounterRE.MatchString(lower) || retryDeadlineRE.MatchString(lower)
}

// harnessModuleCause reports whether line is the first line of the stop of a
// CLI that cannot load one of its own files: without its box prefix and spaces
// it starts with the fixed cause, and the line plus the two following lines,
// with the spaces collapsed, carry the "imported from" half of the report.
// Without that half the line is not this stop: a test run or a Require stack
// can print the same first line, and nothing changes then.
func harnessModuleCause(line string, lines []string, i int) bool {
	value := stripBoxPrefix(line)
	if !strings.HasPrefix(value, "Error: Cannot find module") {
		return false
	}
	words := strings.Fields(value)
	for j := i + 1; j < len(lines) && j <= i+2; j++ {
		words = append(words, strings.Fields(stripBoxPrefix(lines[j]))...)
	}
	return strings.Contains(strings.Join(words, " "), "imported from")
}

func splitTexts(value string) []string {
	pieces := []string{}
	for _, piece := range strings.Split(value, "|") {
		piece = text.ASCIILower(strings.TrimSpace(piece))
		if piece != "" {
			pieces = append(pieces, piece)
		}
	}
	return pieces
}

// configuredTextLine is the line as a configured text must equal it: without a
// full-screen TUI's box prefix, an error glyph or an `Error:`-style label, the
// surrounding spaces and a final period, lowercased. The whole line must be the
// text: a sentence that only quotes it (the worker writing about the error, a
// brief on screen) is not the provider's answer.
func configuredTextLine(line string) string {
	value := stripBoxPrefix(line)
	if loc := errorStartRE.FindStringIndex(value); loc != nil && strings.HasSuffix(value[:loc[1]], ":") {
		value = value[loc[1]:]
	} else if errorGlyphRE.MatchString(value) {
		value = strings.TrimLeft(value, " \t■")
	}
	value = strings.TrimSuffix(strings.TrimSpace(value), ".")
	return text.ASCIILower(strings.TrimSpace(value))
}

func equalsText(value string, pieces []string) bool {
	for _, piece := range pieces {
		if value == strings.TrimSuffix(piece, ".") {
			return true
		}
	}
	return false
}

func matchesAny(patterns []*regexp.Regexp, value string) bool {
	for _, pattern := range patterns {
		if pattern.MatchString(value) {
			return true
		}
	}
	return false
}
