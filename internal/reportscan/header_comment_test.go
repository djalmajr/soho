package reportscan

import "testing"

// SOH-325: a project's report marker in HTML comments may open the report;
// the review header and the validation line are read after it.
func TestReviewHeaderAfterLeadingHTMLComments(t *testing.T) {
	header := "findings: 2 (P0 0, P1 1, P2 1, P3 0) | verdict: fail"
	for name, text := range map[string]string{
		"one-line marker":        "<!-- security-review:report sha=abc123 -->\n" + header + "\n",
		"multi-line comment":     "<!--\nsecurity-review:report\nsha=abc123\n-->\n\n" + header + "\n",
		"two comments":           "<!-- a -->\n<!-- b -->\n" + header + "\n",
		"header after the close": "<!-- a --> " + header + "\n",
		"CRLF":                   "<!-- a -->\r\n" + header + "\r\n",
	} {
		got := ReviewHeader(text)
		if got == nil || got.Findings != 2 || got.Verdict != "fail" || got.Severity["P1"] != 1 {
			t.Fatalf("%s: got %+v", name, got)
		}
	}
	for name, text := range map[string]string{
		"unclosed comment":        "<!-- never closed\n" + header + "\n",
		"comment after text":      "# Review\n<!-- a -->\n" + header + "\n",
		"marker without a header": "<!-- a -->\n# Review\n",
		"only a comment":          "<!-- a -->\n",
	} {
		if got := ReviewHeader(text); got != nil {
			t.Fatalf("%s: want no header, got %+v", name, got)
		}
	}
	if got, ok := ValidationLine("<!-- marker -->\n" + header + "\nvalidation: NOT RUN\n"); !ok || got != "NOT RUN" {
		t.Fatalf("validation after a marker: %q %v", got, ok)
	}
	if got, ok := ValidationLine(header + "\nvalidation: PASS\n"); !ok || got != "PASS" {
		t.Fatalf("validation without a marker: %q %v", got, ok)
	}
}
