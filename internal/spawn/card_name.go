package spawn

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

// cardFunctions is the short function suffix a role contributes to a name
// derived from a card key. Roles outside the table keep their own name.
var cardFunctions = map[string]string{
	"implementer":          "impl",
	"critical-implementer": "crit",
	"reviewer":             "rev",
	"security-reviewer":    "sec",
	"scouter":              "scout",
	"researcher":           "res",
	"tester":               "test",
	"documenter":           "doc",
	"designer":             "ui",
	"ui-reviewer":          "ui",
	"validator":            "val",
	"tasker":               "task",
	"inspector":            "insp",
	"sub-orchestrator":     "orch",
}

// cardNamePattern is the roster naming pattern spawn advises: a subject
// followed by one known function and an optional numeric collision suffix.
var cardNamePattern = regexp.MustCompile(`^[a-z][a-z0-9]*-(impl|crit|rev|sec|scout|res|test|doc|ui|val|task|insp|orch)(-[0-9]+)?$`)

// CardAgentName derives <subject>-<function> from a card key and a role. The
// subject is the key lowercased with every character outside [a-z0-9] removed;
// it must start with a letter. The returned name then follows the ordinary
// --name validation and collision path.
func CardAgentName(card, role string) (string, error) {
	var subject strings.Builder
	for _, r := range strings.ToLower(card) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			subject.WriteRune(r)
		}
	}
	key := subject.String()
	if key == "" || key[0] < 'a' || key[0] > 'z' {
		return "", fmt.Errorf("spawn: --card '%s' gives no usable name; expected a card key such as SOH-427", card)
	}
	function, known := cardFunctions[role]
	if !known {
		function = role
	}
	return key + "-" + function, nil
}

// cardValue records the presence of --card separately from its value: an
// empty operand is still an explicit card and still goes through validation.
// A second --card is a usage error, not a replacement of the first value.
func cardValue(previous string, wasSet bool, value string) string {
	if wasSet {
		core.DieFriction("spawn: --card given more than once", 2, "", "")
	}
	return value
}

// resolveSpawnName applies --card before spawn's ordinary --name validation.
// Presence, not value, decides whether the flag was given: an empty card is
// refused, and the conflict with --name is checked before either is resolved.
// A derived name goes down the same unique-name and roster path as an explicit
// --name; only an explicit choice draws the advisory. Reopen passes the name it
// already recorded, so its spawn half is marked to stay silent. The warn closure
// is spawn's own, keeping admission-only runs quiet.
func resolveSpawnName(o spawnOptions, reopenName bool, warn func(string, *core.Config, platform.Env, string), ctx *core.Config, env platform.Env) string {
	if o.cardSet {
		if o.nameSet {
			core.DieFriction("spawn: pass --card or --name, not both", 2, "", "")
		}
		derived, err := CardAgentName(o.card, o.role)
		if err != nil {
			core.DieFriction(err.Error(), 2, "", "")
		}
		o.name, o.nameSet = derived, false
	}
	if o.nameSet && !reopenName {
		if message := NameOutsidePattern(o.name); message != "" {
			warn(message, ctx, env, "spawn")
		}
	}
	return o.name
}

// NameOutsidePattern returns the advisory for an explicit --name that breaks
// the naming pattern, or "" when the name is fine. A derived name, a lane or
// role default and a reopen's recorded name never reach this function.
func NameOutsidePattern(name string) string {
	for _, prefix := range []string{"wake-", "soho-"} {
		if strings.HasPrefix(name, prefix) {
			return fmt.Sprintf("spawn: agent name '%s' uses the reserved prefix '%s' (soho's own wake and helper terminals); name workers <subject>-<function>, for example soh427-impl, or pass --card <KEY>", name, prefix)
		}
	}
	if cardNamePattern.MatchString(name) {
		return ""
	}
	return fmt.Sprintf("spawn: agent name '%s' is not <subject>-<function>[-<n>] (for example soh427-impl); pass --card <KEY> to derive it", name)
}
