package cli

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/dispatch"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	textpkg "github.com/djalmajr/soho/internal/text"
	waitpkg "github.com/djalmajr/soho/internal/wait"
)

func dispatchSidecarRole(path string) string {
	raw, err := core.ReadScratchReport(path)
	if err != nil {
		return ""
	}
	v, err := jsonjs.Parse(raw)
	if err != nil {
		return ""
	}
	if obj, ok := v.(*jsonjs.Object); ok {
		if role, _ := obj.Get("role"); role != nil {
			if s, ok := role.(string); ok {
				return s
			}
		}
	}
	return ""
}

func openDispatchRole(sd, agent string) string {
	return waitpkg.RecordedTaskRole(sd, agent)
}

// readOnlyEditSignals returns the signals that make a brief an edit request
// (the Type line and the literal Owned files items), the edit role to suggest
// and the remedy text. The dispatch refusal and `lint --role` share it.
func readOnlyEditSignals(body string) (signals []string, suggested, remedy string) {
	typeValue := dispatch.BriefType(body)
	suggested = "implementer"
	switch typeValue {
	case "mechanical", "backend", "ui", "docs":
		signals = append(signals, "Type: "+typeValue)
		suggested = map[string]string{"mechanical": "tasker", "backend": "implementer", "ui": "designer", "docs": "documenter"}[typeValue]
	}
	// DJA-694: the read-only refusal counts only the literal "Owned files"
	// title - a scope list under a synonym like "Arquivos" is not an edit
	// request. Edit roles keep counting the synonyms (ownedPaths, section
	// lint), which is why this signal uses the literal matcher instead of
	// BriefHasOwnedItems.
	if line, text := dispatch.BriefFirstLiteralOwnedItem(body); line > 0 {
		signal := "Owned files: line " + strconv.Itoa(line) + ": " + quoteRefusalLine(text)
		if ownedClosingPhrase(text) {
			signal += "; that line belongs in Forbidden or Report, not in the file list"
		}
		signals = append(signals, signal)
	}
	if len(signals) == 0 {
		return nil, "", ""
	}
	// Renaming a title cannot lift a refusal the Type line caused.
	remedy = "or rename the 'Owned files' title, or drop the edit sections from the brief"
	if !strings.HasPrefix(signals[0], "Owned files") {
		remedy = "or drop the '" + signals[0] + "' line and the edit sections from the brief"
	}
	return signals, suggested, remedy
}

// quoteRefusalLine makes one printable line for the refusal: control
// characters are escaped (text.QuoteControls) and the line is cut at 80
// characters, never inside a character, so a long Owned line stays short.
func quoteRefusalLine(text string) string {
	quoted := []rune(textpkg.QuoteControls(text))
	if len(quoted) > 80 {
		return "'" + string(quoted[:80]) + "…'"
	}
	return "'" + string(quoted) + "'"
}

// closingListMarkerRE is the Owned list-marker grammar of
// internal/gittree/diffreport.go's diffOwnedLabelMarkerRE: -, * or +, or a
// number followed by . or ), then spaces or tabs.
var closingListMarkerRE = regexp.MustCompile(`^(?:[-*+]|[0-9]+[.)])[ \t]+`)

// closingPairs are the decorations the phrase rule removes only when they
// enclose the whole remaining line: markdown emphasis, parentheses and
// ASCII or Unicode quotes. An unpaired mark stays, so it cannot make a line
// that is not a closing phrase look like one.
var closingPairs = [][2]string{{"**", "**"}, {"__", "__"}, {"*", "*"}, {"_", "_"}, {"(", ")"}, {"“", "”"}, {"‘", "’"}, {"«", "»"}, {"\"", "\""}, {"'", "'"}}

// ownedClosingPhrase recognizes the lines people leave in Owned files when
// they meant to forbid a commit or report it. A backtick span marks a path
// item, so it is never a closing phrase; otherwise the item is reduced by the
// Owned list-marker grammar, enclosing decoration pairs, trailing punctuation
// and case before the whole line is compared with the phrase list.
func ownedClosingPhrase(text string) bool {
	line := strings.TrimSpace(text)
	if strings.Contains(line, "`") {
		return false
	}
	line = closingListMarkerRE.ReplaceAllString(line, "")
	for {
		before := line
		line = strings.TrimRight(line, ".!;:")
		for _, pair := range closingPairs {
			if len(line) > len(pair[0])+len(pair[1]) && strings.HasPrefix(line, pair[0]) && strings.HasSuffix(line, pair[1]) {
				line = strings.TrimSpace(line[len(pair[0]) : len(line)-len(pair[1])])
				break
			}
		}
		if line == before {
			break
		}
	}
	line = strings.ToLower(line)
	switch line {
	case "sem commit", "não commite", "nao commite", "no commit",
		"do not commit", "don't commit", "never commit":
		return true
	}
	return false
}

func refuseReadOnlyEditBrief(body, brief, agent, role string, explicit bool, ctx *core.Config, env platform.Env, cwd string) {
	if core.RoleIsEdit(role, env, cwd) || !roleDeclaredReadOnly(role, env, cwd) {
		return
	}
	signals, suggested, remedy := readOnlyEditSignals(body)
	if len(signals) == 0 {
		return
	}
	wording := "runs"
	if explicit {
		wording = "was given"
	}
	core.DieFriction(fmt.Sprintf("dispatch: brief %s asks for edits (%s) but '%s' %s the read-only role '%s'; nothing was sent. Pass --role %s to give it an edit role, %s", brief, strings.Join(signals, ", "), agent, wording, role, suggested, remedy), 2, frictionLogPath, "dispatch")
}

func amendmentRoleHeader(roleFile, role, agent string) string {
	return fmt.Sprintf("The role instructions below replace the role instructions of your previous dispatch.\n\n# Role: %s\n\nYou are running as the `%s` role, agent name `%s`.\n\n%s\n\n", core.FmGet(roleFile, "name"), role, agent, core.RoleBody(roleFile))
}

// roleDeclaredReadOnly reports a role whose file declares mode: read-only.
// Only that explicit header contradicts an edit brief; a role without a
// declared mode (a custom project role) is not refused.
func roleDeclaredReadOnly(role string, env platform.Env, cwd string) bool {
	file := core.RoleFile(role, env, cwd)
	return file != "" && core.FmGet(file, "mode") == "read-only"
}

// warnSecurityBriefOnBuildLane is advisory and never blocks: a brief that
// declares Type: security routed to a build-lane worker points at the
// critical lane. It reads only the declared Type value (never free text),
// only the roster's lane column, only for the security type, and it stays
// silent when the critical lane has no capacity (strict panes=2 has no
// critical lane, flex panes=2 has one at capacity 0, and a custom team may
// have none; lanes=off rows carry no lane).
func warnSecurityBriefOnBuildLane(body, brief, agent, lane string, ctx *core.Config, env platform.Env) {
	if dispatch.BriefType(body) != "security" || lane != "build" {
		return
	}
	if !core.LaneUsable(ctx, "critical", env) {
		return
	}
	core.Warn(fmt.Sprintf("brief %s declares Type: security but '%s' is in lane build: security work goes to the critical lane (spawn critical-implementer)", brief, agent), frictionLogPath, "dispatch")
}
