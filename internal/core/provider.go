package core

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/djalmajr/soho/internal/provider"
	"github.com/djalmajr/soho/internal/text"
)

var modelPlanLine = regexp.MustCompile(`^[ \t]*(?:┃[ \t]*)?(?:(?:[■✗✘×⚠●•⎿][ \t]+)?(?:API Error|ERROR|Error):[ \t]*)?(Model unavailable on [\p{L}][\p{L}\p{N}_-]*(?:[ .,:].*)?)$`)

// ProviderDetectTexts recognizes terminal model-plan refusals without treating
// screen content as configuration. Only a code-owned marker enters the error
// list; the shared classifier still owns the ten-line region and stop ordering.
func ProviderDetectTexts(state, screen, capacityTexts, errorTexts string) *provider.Detection {
	if state == "working" || screen == "" {
		return nil
	}
	marker := "soho-internal-model-plan-refusal"
	// Keep the marker distinct from all input so an original screen line cannot
	// impersonate a replacement or alter which source line supplies the cause.
	inputs := text.ASCIILower(screen + "\n" + capacityTexts + "\n" + errorTexts)
	for n := 0; strings.Contains(inputs, marker); n++ {
		marker = "soho-internal-model-plan-refusal" + strconv.Itoa(n)
	}
	lines := strings.Split(strings.ReplaceAll(screen, "\r\n", "\n"), "\n")
	quoted := pairedPlanCodeLines(lines)
	lastPlanLine := ""
	for i, line := range lines {
		if !modelPlanLine.MatchString(line) {
			continue
		}
		if quoted[i] {
			// Preserve non-empty line positions when excluding a quoted refusal:
			// deleting it would pull older errors into the classifier's ten-line tail.
			lines[i] = "// " + line
			continue
		}
		if provider.QuotaLineIsCode(line) {
			continue
		}
		lastPlanLine = line
		lines[i] = marker
	}
	if lastPlanLine != "" {
		errorTexts += "|" + marker
	}
	detection := provider.ProviderDetectTexts(state, strings.Join(lines, "\n"), capacityTexts, errorTexts)
	if detection != nil && lastPlanLine != "" && detection.Cause == marker {
		detection.Status = "provider-error"
		detection.Cause = text.SanitizeCause(text.RedactSecrets(lastPlanLine))
	}
	return detection
}

// IsModelPlanError identifies the terminal plan refusal in a sanitized cause.
func IsModelPlanError(cause string) bool { return modelPlanLine.MatchString(cause) }

// pairedPlanCodeLines only excludes complete fence pairs visible in this read.
// Pair from the bottom: a leading orphan closing fence in a truncated read
// cannot become an opener that hides a later real refusal.
func pairedPlanCodeLines(lines []string) []bool {
	type fenceLine struct {
		line, size        int
		char              byte
		canOpen, canClose bool
	}
	var fences []fenceLine
	for i, line := range lines {
		value := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "┃"))
		if !strings.HasPrefix(value, "```") && !strings.HasPrefix(value, "~~~") {
			continue
		}
		size := 0
		for size < len(value) && value[size] == value[0] {
			size++
		}
		fences = append(fences, fenceLine{
			line: i, size: size, char: value[0],
			canOpen:  value[0] == '~' || !strings.Contains(value[size:], "`"),
			canClose: strings.TrimSpace(value[size:]) == "",
		})
	}
	quoted := make([]bool, len(lines))
	for end := len(fences) - 1; end >= 0; end-- {
		if !fences[end].canClose {
			continue
		}
		for start := end - 1; start >= 0; start-- {
			if fences[start].canOpen && fences[start].char == fences[end].char && fences[start].size <= fences[end].size {
				for i := fences[start].line + 1; i < fences[end].line; i++ {
					quoted[i] = true
				}
				end = start
				break
			}
		}
	}
	return quoted
}
