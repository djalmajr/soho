package reportscan

import "testing"

func TestRuledOutCount(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		want       int
	}{
		{
			name: "three bullets and a nested level-two heading",
			text: "## Ruled out\n\n- first\n- second\n* third\n\n## Findings\n\n- not counted\n",
			want: 3,
		},
		{
			name: "nested level-three heading stays inside",
			text: "## Ruled out\n\n- first\n\n### Detail\n\n- second\n* third\n",
			want: 3,
		},
		{
			name: "fenced lines are ignored",
			text: "## Ruled out\n\n- real one\n\n```md\n- fenced one\n* fenced two\n## Findings\n```\n\n* real two\n",
			want: 2,
		},
		{
			name: "tilde fence hides a fake boundary",
			text: "## Ruled out\n\n- first\n\n~~~\n# Next\n~~~\n\n- second\n",
			want: 2,
		},
		{
			name: "level three heading is not the section",
			text: "### Ruled out\n\n- first\n- second\n",
			want: 0,
		},
		{
			name: "extra heading text is not the section",
			text: "## Ruled out extra\n\n- first\n- second\n",
			want: 0,
		},
		{
			name: "lower-case title and closing hashes open",
			text: "## ruled out ##\n\n- first\n",
			want: 1,
		},
		{
			name: "no section",
			text: "# Report\n\n- first\n- second\n",
			want: 0,
		},
		{
			name: "two sections add up",
			text: "## Ruled out\n\n- first\n\n## Findings\n\n- ignored\n\n## Ruled out \n\n* second\n* third\n",
			want: 3,
		},
		{
			name: "CRLF keeps the same counts",
			text: "## Ruled out\r\n\r\n- first\r\n- second\r\n* third\r\n\r\n## Findings\r\n\r\n- ignored\r\n",
			want: 3,
		},
		{
			name: "top-level candidates only",
			text: "## Ruled out\n\n- dash\n* star\n+\tplus\n one\n  * two\n   + three\n    - four\n\t* tabbed\n1. ordered\n",
			want: 3,
		},
		{
			name: "one candidate with indented evidence counts once",
			text: "## Ruled out\n\n- candidate\n  - evidence one\n    * evidence two\n\n## Findings\n",
			want: 1,
		},
		{
			name: "bullets hidden in an HTML comment do not count",
			text: "## Ruled out\n\n- visible\n\n<!--\n- hidden one\n* hidden two\n-->\n\n+ visible two\n",
			want: 2,
		},
		{
			name: "one-space level-one heading ends the section",
			text: "## Ruled out\n\n- first\n\n # Next\n\n- second\n",
			want: 1,
		},
		{
			name: "three-space Follow-ups heading ends the section",
			text: "## Ruled out\n\n- first\n- second\n\n   ## Follow-ups\n\n- hidden third\n",
			want: 2,
		},
		{
			name: "indented level two with closing hashes ends the section",
			text: "## Ruled out\n\n- first\n\n  ## Findings ##\n\n- second\n",
			want: 1,
		},
		{
			name: "empty level-two heading ends the section",
			text: "## Ruled out\n\n- first\n\n##\n\n- second\n",
			want: 1,
		},
		{
			name: "HTML comment cannot fake an opener",
			text: "<!--\n## Ruled out\n-->\n\n- first\n",
			want: 0,
		},
		{
			name: "HTML comment cannot fake a boundary",
			text: "## Ruled out\n\n- first\n\n<!--\n# Next\n-->\n\n- second\n",
			want: 2,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := RuledOutCount(tc.text); got != tc.want {
				t.Fatalf("RuledOutCount() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestRuledOutExcludesBlockedByScope(t *testing.T) {
	const marker = "blocked-by-scope: internal/parser.go:10 — change called out in earlier output\n"
	for _, tc := range []struct {
		name, text string
		want       int
	}{
		{"inside Ruled out", "## Ruled out\n\n- checked candidate\n- " + marker, 0},
		{"outside after the section", "## Ruled out\n\n- checked candidate\n\n## Active\n\n- " + marker, 1},
		{"inside Follow-ups keeps baseline", "## Follow-ups\n\n- " + marker, 1},
		{"fenced inside Ruled out", "## Ruled out\n\n```md\n- " + marker + "```\n", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := len(BlockedByScope(tc.text)); got != tc.want {
				t.Fatalf("BlockedByScope() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestRuledOutReadersOutsideTheWalker(t *testing.T) {
	text := "findings: 1 (P0 0, P1 0, P2 1, P3 0) | verdict: fail\n" +
		"validation: FAIL\n\n## Ruled out\n\n- candidate\n\n<!--\n## Test layers\n-->\n- unit: FAIL: missing gate\n\n<!--\n## Review responses\n-->\n- F1: fixed\n"
	layers := TestLayers(text)
	if !layers.Found || layers.Fail != 1 {
		t.Fatalf("TestLayers = %#v, want the commented section parsed as on baseline", layers)
	}
	responses := ReviewResponses(text)
	if !responses.Found || len(responses.Fixed) != 1 || responses.Fixed[0] != 1 {
		t.Fatalf("ReviewResponses = %#v, want the commented section parsed as on baseline", responses)
	}
	if validation, ok := ValidationLine(text); !ok || validation != "FAIL" {
		t.Fatalf("ValidationLine = %q, %v, want FAIL from the second line", validation, ok)
	}
}

func TestRuledOutExcludesOpenItemScanners(t *testing.T) {
	const inside = "## Ruled out\n\n" +
		"- partial discarded: [partial] ruled out\n" +
		"- env discarded: [env] no host; host: go test\n" +
		"- [F2] [origin: impl] [class: x] discarded evidence\n"
	const outside = "- partial active: [partial] real\n" +
		"- env active: [env] no ports; host: go test\n" +
		"- [F2] [origin: impl] [class: x] active finding\n"

	if got := RuledOutCount(inside); got != 3 {
		t.Fatalf("ruled_out inside = %d, want 3", got)
	}
	if got := PartialCount(inside); got != 0 {
		t.Fatalf("partial inside = %d, want 0", got)
	}
	if got := EnvCount(inside); got != 0 {
		t.Fatalf("env inside = %d, want 0", got)
	}
	if got := len(Items(inside)); got != 0 {
		t.Fatalf("items inside = %d, want 0", got)
	}
	marks := FindingMarks(inside, ReviewHeader("findings: 2 (P0 0, P1 0, P2 2, P3 0) | verdict: fail\n"))
	if marks.Impl != 0 || len(marks.Numbers) != 0 || marks.Unmarked != 2 {
		t.Fatalf("finding marks inside = %#v, want none counted and 2 unmarked", marks)
	}

	if got := RuledOutCount(outside); got != 0 {
		t.Fatalf("ruled_out outside = %d, want 0", got)
	}
	if got := PartialCount(outside); got != 1 {
		t.Fatalf("partial outside = %d, want 1", got)
	}
	if got := EnvCount(outside); got != 1 {
		t.Fatalf("env outside = %d, want 1", got)
	}
	if got := len(Items(outside)); got != 2 {
		t.Fatalf("items outside = %d, want 2", got)
	}
	marks = FindingMarks(outside, ReviewHeader("findings: 2 (P0 0, P1 0, P2 2, P3 0) | verdict: fail\n"))
	if marks.Impl != 1 || len(marks.Numbers) != 1 || marks.Numbers[0] != 2 || marks.Unmarked != 1 {
		t.Fatalf("finding marks outside = %#v, want one impl finding and 1 unmarked", marks)
	}
}
