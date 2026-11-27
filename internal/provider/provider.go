package provider

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/djalmajr/soho/internal/text"
	"github.com/djalmajr/soho/internal/transport"
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

// AgyModelCause and CodexConfigCause are fixed diagnostics for narrow
// startup failures. Neither carries a model id, server name or config path:
// the observed screen decides the class, and the redaction rules still own
// anything that could carry a secret.
const (
	AgyModelCause    = "agy rejected the requested model; the first turn did not start"
	CodexConfigCause = "codex cannot load its MCP configuration; run soho reopen <agent> and dispatch the task again"
)

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
	agyConstructLine    = "failed to construct executor: plan model not specified; you must specify a valid model"
	agyUnknownModelRE   = regexp.MustCompile(`^model \S+ is not recognized as a known model or custom model in settings\. ignoring the flag$`)
	codexConfigStart    = "failed to start turn: turn/start failed in tui: turn/start failed: failed to load configuration:"
	codexConfigServerRE = regexp.MustCompile("^in `mcp_servers\\.[A-Za-z0-9_-]+`$")
	authRes             = []*regexp.Regexp{
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
	return ProviderDetectTextsWithStreamGate(state, screen, capacityTexts, errorTexts, nil)
}

// ProviderDetectTextsWithStreamGate is ProviderDetectTexts with a gate on the
// built-in stream signature. The gate is consulted lazily only where that
// pattern competes in the existing bounded scan: an empty answer leaves the
// same line's configured matches and earlier ordinary lines in place, while a
// nonempty answer supplies the detection's cause. Nothing about the line
// budget, chronology, code filters or other rules changes; a nil gate keeps
// the generic behavior for callers with no provenance evidence.
func ProviderDetectTextsWithStreamGate(state, screen, capacityTexts, errorTexts string, streamAllowed func(string) string) *Detection {
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
			streamCause := ""
			if responseStoppedRE.MatchString(lowerLine) {
				// The gate is lazy: only a built-in stream line pays for provenance
				// checks. An empty answer rejects this branch and the scan moves
				// to earlier lines; a nonempty answer supplies the fixed cause.
				streamCause = cause
				if streamAllowed != nil {
					streamCause = streamAllowed(line)
				}
			}
			if matchesAny(providerRes, lowerLine) || providerStatusRE.MatchString(line) || econnRE.MatchString(line) || streamCause != "" {
				if streamCause != "" {
					cause = streamCause
				}
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

var claudeStreamDoneRE = regexp.MustCompile(`^✻ Cogitated for (?:[0-9]+h(?: [0-9]+m)?(?: [0-9]+s)?|[0-9]+m(?: [0-9]+s)?|[0-9]+s) · done (?:[1-9]|1[0-2]):[0-5][0-9] (?:AM|PM)$`)

// ClaudeStreamStop recognizes the signed Claude stream error followed by the
// completed-turn footer and a bare, empty composer in a normalized visible frame.
func ClaudeStreamStop(frame string) (history, cause string) {
	const line = "● API Error: The response stopped arriving. The response above may be incomplete."
	prompt := strings.LastIndexByte(frame, '\n')
	if prompt < 0 || strings.TrimRight(frame[prompt+1:], " \t") != "❯" {
		return "", ""
	}
	above := strings.TrimRight(frame[:prompt], "\n \t")
	footer := strings.LastIndexByte(above, '\n')
	if footer < 0 || !claudeStreamDoneRE.MatchString(above[footer+1:]) {
		return "", ""
	}
	above = strings.TrimRight(above[:footer], "\n \t")
	errorStart := strings.LastIndexByte(above, '\n') + 1
	if above[errorStart:] != line {
		return "", ""
	}
	history = strings.TrimRight(above[:errorStart], "\n \t")
	for _, priorLine := range strings.Split(history, "\n") {
		priorLine = strings.TrimSpace(priorLine)
		if strings.HasPrefix(priorLine, "```") || strings.HasPrefix(priorLine, "~~~") {
			return "", ""
		}
	}
	return history, text.SanitizeCause(text.RedactSecrets(line))
}

// StartupStopKind reports which kinds have a narrow startup diagnosis.
func StartupStopKind(kind string) bool {
	return kind == "agy" || kind == "codex"
}

// StartupStopScreen admits a visible read for startup diagnosis. Native
// Core exposes the viewport provenance and completeness in the read record.
func StartupStopScreen(reply transport.Reply[string], native bool) (string, bool) {
	if reply.Err != nil || !reply.Valid {
		return "", false
	}
	if native {
		var frame struct {
			Complete bool
			How      string
		}
		if json.Unmarshal([]byte(reply.Process.Stdout), &frame) != nil || !frame.Complete || frame.How != "screen" {
			return "", false
		}
	}
	return reply.Value, true
}

// StartupProviderStop matches complete provider-specific frames at the end of
// the current viewport. Previous observations only veto diagnosis.
func StartupProviderStop(kind, state, visible string, previous ...string) *Detection {
	if !StartupStopKind(kind) || (state != "idle" && state != "done") || DialogUI(kind, visible) != "" {
		return nil
	}
	lines := LastNonEmptyLines(visible, 0)
	for i, line := range lines {
		value := strings.TrimSpace(stripBoxPrefix(line))
		// A visible code boundary makes quote provenance ambiguous. Require
		// an unambiguous viewport rather than parsing its prose as Markdown.
		if strings.HasPrefix(value, "```") || strings.HasPrefix(value, "~~~") {
			return nil
		}
		lines[i] = startupLine(line)
	}
	end := len(lines)
	if end > 0 && (lines[end-1] == "›" || lines[end-1] == ">") {
		end--
	}
	if start, _, ok := ComposerRegionBounds(kind, visible); ok {
		raw := strings.Split(strings.ReplaceAll(visible, "\r\n", "\n"), "\n")
		if strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw[start]), "›")) != "" {
			return nil
		}
	}

	var cause string
	switch kind {
	case "agy":
		if end < 2 || !agyUnknownModelRE.MatchString(lines[end-1]) {
			return nil
		}
		start := end - 2
		if lines[start] == "warning" && start > 0 {
			start--
		}
		if lines[start] != agyConstructLine {
			return nil
		}
		lines = lines[start:end]
		cause = AgyModelCause
	case "codex":
		if end > 0 && lines[end-1] == "(code -32600)" {
			end--
		}
		if end < 3 || lines[end-3] != codexConfigStart || lines[end-2] != "invalid transport" || !codexConfigServerRE.MatchString(lines[end-1]) {
			return nil
		}
		lines = lines[end-3 : end]
		cause = CodexConfigCause
	}

	match := "\n" + strings.Join(lines, "\n") + "\n"
	for _, screen := range previous {
		lines := LastNonEmptyLines(screen, 0)
		for i, line := range lines {
			lines[i] = startupLine(line)
		}
		if strings.Contains("\n"+strings.Join(lines, "\n")+"\n", match) {
			return nil
		}
	}
	return &Detection{Status: "provider-error", Cause: cause}
}

// startupLine folds one screen line to the lowercase text used by narrow
// startup detectors. It removes only known status glyphs, box prefixes, a
// trailing period and surrounding space; any other prefix makes the line
// ineligible for an exact match.
func startupLine(line string) string {
	value := strings.TrimSpace(stripBoxPrefix(line))
	for _, glyph := range []string{"⚠", "■", "✗", "✘", "×", "⎿"} {
		if strings.HasPrefix(value, glyph) {
			value = strings.TrimSpace(strings.TrimPrefix(value, glyph))
			break
		}
	}
	return text.ASCIILower(strings.TrimSuffix(strings.TrimSpace(value), "."))
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

// responseStoppedRE is the generic provider diagnosis for the Claude stream
// error. It is consulted at the built-in branch of the scan so provenance-aware
// consumers can gate only that branch without changing other provider rules.
var responseStoppedRE = regexp.MustCompile(`^` + jsWhitespace + `*([■✗✘×⚠●•⎿]` + jsWhitespace + `+)?(api error|error):` + jsWhitespace + `+the response stopped arriving\b`)
