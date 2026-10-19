// Oracle for the DJA-405 background_patterns.claude key over the frozen
// legacy parity goldens.
//
// The frozen goldens (internal/testdata/legacy: parity-config.json) predate
// the background_patterns.<kind> key: their expected `soho config` output
// lists the dotted keys of the defaults layer starting with effort.cursor.
// The implementation now ships background_patterns.claude in the defaults
// and `soho config` sorts the dotted keys, so the new row comes right before
// effort.cursor (background_patterns < effort; no frozen scenario holds an
// args.* or context_window.* row, which would sort around it). This
// exact-string adaptation inserts the new listing line before the frozen
// anchor line in every expected output.
// Every other frozen expectation passes through byte-identical.
package testutil

import "strings"

// backgroundPatternsAnchor is the exact frozen config listing line the new
// line precedes in every expected output (key padded to 18, value padded to
// 30, single-space columns). Frozen as inert test data.
const backgroundPatternsAnchor = "effort.cursor      xhigh                          defaults"

// backgroundPatternsRow is the exact listing line the current `soho config`
// prints for the shipped default. The key and the value are longer than their
// columns, so each is followed by a single space. If the row format or the
// default drifts, the parity comparison fails on purpose.
const backgroundPatternsRow = `background_patterns.claude \d+ (shells?|monitors?) still running|· \d+ (shells?|monitors?)( ·|$) defaults`

// DJA-405 intentional contract adaptation: soho config lists
// background_patterns.claude before effort.cursor; the frozen bytes are
// unchanged.
//
// RewriteBackgroundPatterns applies the adaptation to one string: before
// every exact occurrence of the frozen anchor line it inserts the new listing
// line. A string without the anchor passes through byte-identical, so an
// expected output the adaptation was meant for fails the comparison instead
// of being silently adapted.
func RewriteBackgroundPatterns(s string) string {
	return strings.ReplaceAll(s, backgroundPatternsAnchor, backgroundPatternsRow+"\n"+backgroundPatternsAnchor)
}
