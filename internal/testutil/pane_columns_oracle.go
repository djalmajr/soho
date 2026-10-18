// Oracle for the DJA-393 pane_columns/pane_overflow keys over the frozen
// legacy parity goldens.
//
// The frozen goldens (internal/testdata/legacy: parity-config.json) predate
// the two declared-columns keys: their expected `soho config` output lists
// the scalar keys with split_min_pane in its pinned row. The implementation
// now lists pane_columns and pane_overflow right after it, so this
// exact-string adaptation inserts the two new listing lines after the
// frozen anchor line in every expected output. Every other frozen
// expectation passes through byte-identical.
package testutil

import (
	"regexp"
	"strings"
)

// paneColumnsAnchor matches the config-listing row for split_min_pane: the
// key padded to 18 (four pad spaces), the separator, then the value (any
// value, padded to 30) and the source (any source). The row is pinned in
// every frozen `soho config` expectation; a string without the row passes
// through unchanged so an expectation the adaptation was meant for fails
// the comparison instead of being silently adapted.
var paneColumnsAnchor = regexp.MustCompile(`(?m)^split_min_pane {4} .*$`)

// paneColumnsInsert is the anchor row plus the two exact listing lines the
// current `soho config` prints right after it: pane_columns with the empty
// default value and pane_overflow with the last-column default, both from
// the defaults source (no frozen scenario defines the keys in another
// layer). Frozen as inert test data — if the row format or the defaults
// drift, the parity comparison fails on purpose.
//
// Row format (core.Cfg/padUTF16): key padded to 18, separator, value padded
// to 30, separator, source.
var paneColumnsInsert = "pane_columns" + strings.Repeat(" ", 38) + "defaults\n" +
	"pane_overflow" + strings.Repeat(" ", 6) + "last-column" + strings.Repeat(" ", 20) + "defaults"

// DJA-393 intentional contract adaptation: soho config lists pane_columns
// and pane_overflow after split_min_pane; the frozen bytes are unchanged.
//
// RewritePaneColumns applies the adaptation to one string: after every
// listing row for split_min_pane it inserts the two new listing lines.
func RewritePaneColumns(s string) string {
	return paneColumnsAnchor.ReplaceAllString(s, "$0\n"+paneColumnsInsert)
}

// ApplyPaneColumnsOracle recursively walks a decoded JSON value tree
// (objects, arrays and scalar strings) and rewrites every string through
// RewritePaneColumns. Non-string values and strings without the anchor
// pass through unchanged.
func ApplyPaneColumnsOracle(value any) any {
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			v[key] = ApplyPaneColumnsOracle(item)
		}
		return v
	case []any:
		for i := range v {
			v[i] = ApplyPaneColumnsOracle(v[i])
		}
		return v
	case string:
		return RewritePaneColumns(v)
	default:
		return value
	}
}
