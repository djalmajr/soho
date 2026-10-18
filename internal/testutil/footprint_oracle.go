// Oracle for the footprint_warn_gb key over the frozen legacy parity
// goldens.
//
// The frozen goldens (internal/testdata/legacy: parity-config.json) predate
// the footprint key: their expected `soho config` output lists the scalar
// keys ending at the two pressure refusal keys that the DJA-392 oracle
// inserts. The implementation now lists footprint_warn_gb right after
// pressure_refuse_swap_percent, so this exact-string adaptation inserts the
// new listing line after the frozen anchor line in every expected output.
// It is chained after RewritePressureRefusal (the anchor is the line that
// oracle inserts). Every other frozen expectation passes through
// byte-identical.
package testutil

import "strings"

// footprintAnchor is the exact frozen config listing line the new line
// follows in every expected output: the second line the DJA-392 pressure
// refusal oracle inserts (key padded to 18, value padded to 30,
// single-space columns). Frozen as inert test data.
const footprintAnchor = "pressure_refuse_swap_percent 95                             defaults"

// footprintInsert is the anchor plus the exact listing line the current
// `soho config` prints right after it. If the row format or the default
// drift, the parity comparison fails on purpose.
const footprintInsert = footprintAnchor + "\n" +
	"footprint_warn_gb  5                              defaults"

// DJA-392 intentional contract adaptation: soho config lists the footprint
// key after pressure_refuse_swap_percent; the frozen bytes are unchanged.
//
// RewriteFootprint applies the adaptation to one string: after every exact
// occurrence of the frozen anchor line it inserts the new listing line. A
// string without the anchor passes through byte-identical, so an expected
// output the adaptation was meant for fails the comparison instead of
// being silently adapted.
func RewriteFootprint(s string) string {
	return strings.ReplaceAll(s, footprintAnchor, footprintInsert)
}

// ApplyFootprintOracle recursively walks a decoded JSON value tree
// (objects, arrays and scalar strings) and rewrites every string through
// RewriteFootprint. Non-string values and strings without the anchor pass
// through unchanged.
func ApplyFootprintOracle(value any) any {
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			v[key] = ApplyFootprintOracle(item)
		}
		return v
	case []any:
		for i := range v {
			v[i] = ApplyFootprintOracle(v[i])
		}
		return v
	case string:
		return RewriteFootprint(v)
	default:
		return value
	}
}
