package spawn

import (
	"strings"

	"github.com/djalmajr/soho/internal/text"
)

// agyPromoBannerFooter is the footer of agy's first-run promotional banner,
// quoted from the pdweb 2026-11-18 report: two lines, the second one being
// "esc to dismiss".
const agyPromoBannerFooter = "esc to dismiss"

// agyPromoBannerTexts are the known promotional phrases of agy's first-run
// banner. A footer on a screen with other promotional text is not classified
// as the banner: an Esc there could dismiss a different choice.
var agyPromoBannerTexts = []string{
	"get building today",
	"our best balance of intelligence",
}

// agyPromoBannerRefusals mark a question or a choice on the visible screen.
// Any of them wins over the banner footer: a screen with an active choice
// must not be named as a passive banner in the blocked report. A marker is
// checked on the normalized line, so padding, case and a longer sentence
// cannot hide it.
var agyPromoBannerRefusals = []string{
	"?",
	"(y/n)",
	"[y/n]",
	"yes, ",
	"no, ",
	"enter to confirm",
	"do you trust",
	"allow",
	"approve",
}

// isAgyPromoBanner reports whether the visible screen of a fresh agy shows
// its known promotional text and the "esc to dismiss" footer with no sign
// of a question or a choice. It only chooses the wording of the blocked
// report: spawn sends no key, so a false positive cannot answer a question.
// Every line is normalized the way isGrokTrustDialog normalizes it (ASCII
// lowercase, spaces collapsed), so terminal padding and uppercase do not
// change the verdict. The footer has to fit on one line: a line split by a
// narrow viewport is not recognized. Refusals are also checked on all lines
// joined with single spaces, so a wrapped "Yes, proceed" cannot evade the
// check. Other promotional text is not enough: a screen the classifier does
// not know keeps the ordinary blocked warning.
func isAgyPromoBanner(screen string) bool {
	lines := strings.Split(screen, "\n")
	normalized := make([]string, 0, len(lines))
	footer := false
	for _, line := range lines {
		norm := strings.Join(strings.Fields(text.ASCIILower(line)), " ")
		normalized = append(normalized, norm)
		if strings.Contains(norm, agyPromoBannerFooter) {
			footer = true
		}
		for _, refusal := range agyPromoBannerRefusals {
			if strings.Contains(norm, refusal) {
				return false
			}
		}
	}
	joined := strings.Join(normalized, " ")
	for _, refusal := range agyPromoBannerRefusals {
		if strings.Contains(joined, refusal) {
			return false
		}
	}
	if !footer {
		return false
	}
	for _, phrase := range agyPromoBannerTexts {
		if strings.Contains(joined, phrase) {
			return true
		}
	}
	return false
}
