package reportscan

import "testing"

// SOH-319: the first unrecognized line is kept for the warning; a dash
// a plain hyphen before the reason is not the form.
func TestTestLayersFirstUnmarked(t *testing.T) {
	text := "## Test layers\n- Fixture: PASS\n- Native: NOT RUN — no Linux host\n- Race: FAIL: data race in wait\n- Lint: NOT RUN - skipped\n"
	got := TestLayers(text)
	if got.Unmarked != 1 || got.FirstUnmarked != "- Lint: NOT RUN - skipped" || got.Pass != 1 || got.Fail != 1 || got.NotRun != 1 {
		t.Fatalf("got %+v", got)
	}
	if clean := TestLayers("## Test layers\n- Fixture: PASS\n"); clean.Unmarked != 0 || clean.FirstUnmarked != "" {
		t.Fatalf("clean section: %+v", clean)
	}
}
