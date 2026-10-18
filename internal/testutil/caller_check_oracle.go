// Oracle for the DJA-394 caller_check key over the frozen legacy parity
// goldens.
//
// The frozen goldens (internal/testdata/legacy: parity-config.json) predate
// the caller_check scalar key: their expected `soho config` output lists
// the scalar keys with inbound and no line after it. The implementation now
// lists caller_check (default warn, since DJA-406) right after inbound, so this
// exact-string adaptation inserts the new listing line after the frozen
// anchor line in every expected output.
// Every other frozen expectation passes through byte-identical.
package testutil

import "strings"

// callerCheckAnchor is the exact frozen config listing line the new line
// follows in every expected output (key padded to 18, value padded to 30,
// single-space columns). Frozen as inert test data.
const callerCheckAnchor = "inbound            auto                           defaults"

// callerCheckInsert is the anchor plus the exact listing line the current
// `soho config` prints right after it (caller_check, the warn default
// from skills/soho/config.defaults, source defaults). If the row format or
// the default drifts, the parity comparison fails on purpose.
const callerCheckInsert = callerCheckAnchor + "\n" +
	"caller_check       warn                           defaults"

// DJA-394 intentional contract adaptation: soho config lists caller_check
// after inbound; the frozen bytes are unchanged.
//
// RewriteCallerCheck applies the adaptation to one string: after every
// exact occurrence of the frozen anchor line it inserts the new listing
// line. A string without the anchor passes through byte-identical, so an
// expected output the adaptation was meant for fails the comparison
// instead of being silently adapted.
func RewriteCallerCheck(s string) string {
	return strings.ReplaceAll(s, callerCheckAnchor, callerCheckInsert)
}

// ApplyCallerCheckOracle recursively walks a decoded JSON value tree
// (objects, arrays and scalar strings) and rewrites every string through
// RewriteCallerCheck. Non-string values and strings without the anchor
// pass through unchanged.
func ApplyCallerCheckOracle(value any) any {
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			v[key] = ApplyCallerCheckOracle(item)
		}
		return v
	case []any:
		for i := range v {
			v[i] = ApplyCallerCheckOracle(v[i])
		}
		return v
	case string:
		return RewriteCallerCheck(v)
	default:
		return value
	}
}

// DJA-394 intentional contract adaptation for the recorded Herdr calls:
// a spawn whose caller has HERDR_PANE_ID now reads that pane's shell pid
// (`pane process-info --pane <id>`) before any other Herdr call, to check
// the caller really runs in it. The frozen parity-spawn scenario that sets
// HERDR_PANE_ID ("overflow") predates the check; its frozen bytes are
// unchanged.
//
// RewriteCallerCheckLog prepends that call to one expected step log. A nil
// log (a step that pins no log) stays nil.
func RewriteCallerCheckLog(log *string, pane string) *string {
	if log == nil {
		return nil
	}
	adapted := "pane process-info --pane " + pane + "\n" + *log
	return &adapted
}
