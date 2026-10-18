// Oracle for the DJA-399 compact_timeout key over the frozen legacy parity
// goldens.
//
// The frozen goldens (internal/testdata/legacy: parity-config.json) predate
// the compact_timeout key: their expected `soho config` output lists the
// scalar keys with dispatch_timeout right after spawn_timeout. The
// implementation now lists compact_timeout right after dispatch_timeout, so
// this exact-string adaptation inserts the new listing line after the frozen
// anchor line in every expected output.
// Every other frozen expectation passes through byte-identical.
package testutil

import "strings"

// compactTimeoutAnchor is the exact frozen config listing line the new line
// follows in every expected output (key padded to 18, value padded to 30,
// single-space columns). Frozen as inert test data.
const compactTimeoutAnchor = "dispatch_timeout   900000                         defaults"

// compactTimeoutInsert is the anchor plus the exact listing line the current
// `soho config` prints right after it. If the row format or the defaults
// drift, the parity comparison fails on purpose.
const compactTimeoutInsert = compactTimeoutAnchor + "\n" +
	"compact_timeout    300000                         defaults"

// DJA-399 intentional contract adaptation: soho config lists compact_timeout
// after dispatch_timeout; the frozen bytes are unchanged.
//
// RewriteCompactTimeout applies the adaptation to one string: after every
// exact occurrence of the frozen anchor line it inserts the new listing
// line. A string without the anchor passes through byte-identical, so an
// expected output the adaptation was meant for fails the comparison instead
// of being silently adapted.
func RewriteCompactTimeout(s string) string {
	return strings.ReplaceAll(s, compactTimeoutAnchor, compactTimeoutInsert)
}

// ApplyCompactTimeoutOracle recursively walks a decoded JSON value tree
// (objects, arrays and scalar strings) and rewrites every string through
// RewriteCompactTimeout. Non-string values and strings without the anchor
// pass through unchanged.
func ApplyCompactTimeoutOracle(value any) any {
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			v[key] = ApplyCompactTimeoutOracle(item)
		}
		return v
	case []any:
		for i := range v {
			v[i] = ApplyCompactTimeoutOracle(v[i])
		}
		return v
	case string:
		return RewriteCompactTimeout(v)
	default:
		return value
	}
}
