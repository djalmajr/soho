package reportscan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPartialCount(t *testing.T) {
	t.Run("partialCount: a table, a list and a header count their [partial] lines", func(t *testing.T) { // JS: "partialCount: a table, a list and a header count their [partial] lines"
		table := "# Report\n\n| item | state |\n| --- | --- |\n| read the brief | [done] |\n| fact A | [partial] |\n| fact B | [partial] |\n"
		if got := PartialCount(table); got != 2 {
			t.Fatalf("table=%d", got)
		}
		list := "## Results\n- fact A: [partial] — the source was not reachable\n- fact B: [done]\n- fact C: [skipped] — not in scope"
		if got := PartialCount(list); got != 1 {
			t.Fatalf("list=%d", got)
		}
		if got := PartialCount("# [partial] — could not verify the external fact\n\ndone.\n"); got != 1 {
			t.Fatalf("header=%d", got)
		}
	})
	t.Run("partialCount: the marker is case-insensitive", func(t *testing.T) { // JS: "partialCount: the marker is case-insensitive"
		for _, tc := range []struct {
			text string
			want int
		}{{"[PARTIAL]: the doc is missing\n", 1}, {"[Partial] — left for later\n[partial] again\n", 2}} {
			if got := PartialCount(tc.text); got != tc.want {
				t.Errorf("%q: %d want %d", tc.text, got, tc.want)
			}
		}
	})
	t.Run("partialCount: a tilde fence closed with a tab still closes: the marker after it counts", func(t *testing.T) {
		text := "## Items\n\n~~~\ncode\n~~~\t\n| item | state |\n| --- | --- |\n| step | [partial] |\n"
		if got := PartialCount(text); got != 1 {
			t.Fatalf("got %d want 1", got)
		}
	})
	t.Run("partialCount: a line with two markers counts once", func(t *testing.T) { // JS: "partialCount: a line with two markers counts once"
		if got := PartialCount("**Estado:** [partial] — e o segundo: [partial]\n"); got != 1 {
			t.Fatalf("got %d", got)
		}
		if got := PartialCount("a [partial] item: [partial] again\none more\n"); got != 1 {
			t.Fatalf("got %d", got)
		}
	})
	t.Run("partialCount: bare-word prose does not count (the P1 regression)", func(t *testing.T) { // JS: "partialCount: bare-word prose does not count (the P1 regression)"
		report := "# Report\n\nFor every item in the brief: `done` / `partial` / `skipped` + reason.\nWhen the brief does not decide something, mark the item partial and list the gap.\nNo item is partial except the one below.\nThe review was partially done; the verdict was impartial.\n\n## Items\n\n- read the brief [done]\n- the external fact: [partial] — could not verify it\n- re-run the checks [skipped] — not in scope"
		if got := PartialCount(report); got != 1 {
			t.Fatalf("got %d", got)
		}
		for _, text := range []string{"done / partial / skipped\n", "the review was partially done\n", "the verdict was impartial\n"} {
			if got := PartialCount(text); got != 0 {
				t.Errorf("%q: %d", text, got)
			}
		}
	})
	t.Run("partialCount: a quote of the state list is not an item state", func(t *testing.T) { // JS: "partialCount: a quote of the state list is not an item state"
		if got := PartialCount("`[done]`, `[partial]` or `[skipped]`\n"); got != 0 {
			t.Fatalf("quote=%d", got)
		}
		if got := PartialCount("Estados: [done] / [partial] / [skipped] + reason\n"); got != 0 {
			t.Fatalf("list=%d", got)
		}
		if got := PartialCount("| 2 | fact | [partial] | depends on item 1, which is [done] |\n| 3 | `[partial]` | not verified |\n"); got != 2 {
			t.Fatalf("items=%d", got)
		}
	})
	t.Run("partialCount: fenced code blocks do not count, outside lines still do", func(t *testing.T) { // JS: "partialCount: fenced code blocks do not count, outside lines still do"
		if got := PartialCount("before: [partial]\n```\na code block with [partial] inside\nand another [partial] line\n```\nafter: [partial]"); got != 2 {
			t.Fatalf("backticks=%d", got)
		}
		if got := PartialCount("~~~\n[partial] in a tilde fence\n~~~\n[partial] outside"); got != 1 {
			t.Fatalf("tildes=%d", got)
		}
		if got := PartialCount("```\n[partial]\n~~~\n[partial]\n```"); got != 0 {
			t.Fatalf("mixed=%d", got)
		}
		if got := PartialCount("```[partial]\n```\ndone\n"); got != 0 {
			t.Fatalf("fence line=%d", got)
		}
	})
	t.Run("partialCount: shorter fence runs do not close a longer fence", func(t *testing.T) {
		text := "````\n[partial] inside\n```\n[partial] still inside\n````\n[partial] outside\n"
		if got := PartialCount(text); got != 1 {
			t.Fatalf("got %d", got)
		}
	})
	t.Run("partialCount: table state can be the final cell without a trailing pipe", func(t *testing.T) {
		if got := PartialCount("| 1 | fact | [partial]\n"); got != 1 {
			t.Fatalf("got %d", got)
		}
	})
	t.Run("partialCount: a suffixed fence line inside a block does not close it (the P2 case)", func(t *testing.T) { // JS: "partialCount: a suffixed fence line inside a block does not close it (the P2 case)"
		if got := PartialCount("```\n[partial] in the block\n```js\nstill inside — the suffixed fence does not close\n```\n[partial] after the real close"); got != 1 {
			t.Fatalf("suffixed=%d", got)
		}
		if got := PartialCount("~~~~\n[partial]\n```\n[partial]\n~~~~"); got != 0 {
			t.Fatalf("short close=%d", got)
		}
		if got := PartialCount("```\n[partial]\n[partial]\n"); got != 0 {
			t.Fatalf("unclosed=%d", got)
		}
	})
	t.Run("partialCount: the marker counts only in a state position, not when mentioned", func(t *testing.T) { // JS: "partialCount: the marker counts only in a state position, not when mentioned"
		cases := []struct {
			text string
			want int
		}{
			{"soma de `[partial]` (via `partialCount` de reportscan)\n", 0}, {"a mention of [partial] in prose\n", 0},
			{"- [partial] item\n", 1}, {"| 2 | fact | [partial] | …\n", 1}, {"| 3 | `[partial]` | …\n", 1},
			{"**Estado:** [partial] — …\n", 1}, {"### B.3 — **[partial]**\n", 1}, {"1. [partial] …\n", 1}, {"> [partial] …\n", 1},
			{"* [partial] star list\n", 1}, {"+ [partial] plus list\n", 1}, {"[ ] [partial] task box\n", 1}, {"# [partial] heading\n", 1},
			{"### B.4 – [partial]\n", 1}, {"item - [partial] fallback\n", 1},
			{"menção a `[partial]` no texto; o estado real: [partial]\n", 1},
			{"`[done]`, `[partial]` or `[skipped]`\n", 0}, {"```\n`[partial]` inside a fence does not count\n```\n[partial]\n", 1},
		}
		for _, tc := range cases {
			if got := PartialCount(tc.text); got != tc.want {
				t.Errorf("%q: got %d want %d", tc.text, got, tc.want)
			}
		}
	})
	t.Run("partialCount: empty text counts 0", func(t *testing.T) { // JS: "partialCount: empty text counts 0"
		for _, text := range []string{"", "\n\n  \n"} {
			if got := PartialCount(text); got != 0 {
				t.Errorf("%q: %d", text, got)
			}
		}
	})
	t.Run("partialCountFile: a readable file counts, a missing or unreadable file counts 0", func(t *testing.T) { // JS: "partialCountFile: a readable file counts, a missing or unreadable file counts 0"
		root := t.TempDir()
		file := filepath.Join(root, "report.md")
		if err := os.WriteFile(file, []byte("| x | [partial] |\n| y | [partial] |\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := PartialCountFile(file); got != 2 {
			t.Fatalf("file=%d", got)
		}
		if got := PartialCountFile(filepath.Join(root, "absent.md")); got != 0 {
			t.Fatalf("missing=%d", got)
		}
		if got := PartialCountFile(root); got != 0 {
			t.Fatalf("directory=%d", got)
		}
	})
}

func TestReviewHeader(t *testing.T) {
	t.Run("case folding matches ASCII JavaScript without Unicode folds", func(t *testing.T) { // JS: "reportscan case folding stays ASCII-only"
		// Mutation captured: RE2 Unicode folding treats long-s as ASCII s.
		if got := PartialCount("- [PARTIAL] item\n"); got != 1 {
			t.Fatalf("ASCII case fold count=%d", got)
		}
		if got := PartialCount("- [partial] [done] [ſkipped]\n"); got != 1 {
			t.Fatalf("long-s must not trigger skipped state: %d", got)
		}
		if got := ReviewHeader("findings: 1 (P0 1, P1 0, P2 0, P3 0) | verdict: paſs\n"); got != nil {
			t.Fatalf("long-s header accepted: %#v", got)
		}
		if got := ReviewHeader("FINDINGS: 1 (P0 1, P1 0, P2 0, P3 0) | VERDICT: PASS\n"); got == nil || got.Verdict != "pass" {
			t.Fatalf("ASCII case variant: %#v", got)
		}
		if got := ReviewHeader("findings: 1 (P0 1, P1 0, P2 0, P3 0) | verdict: paKs\n"); got != nil {
			t.Fatalf("Kelvin header accepted: %#v", got)
		}
	})
	t.Run("reviewHeader: the first non-empty line only, tolerant of case and whitespace", func(t *testing.T) { // JS: "reviewHeader: the first non-empty line only, tolerant of case and whitespace"
		for _, text := range []string{"", "   \n\n  \n", "# Report\n\ndone.\n", "# Report\n\ndone.\nfindings: 1 (P0 1, P1 0, P2 0, P3 0) | verdict: fail\n", "findings: 2  (P0 1, P1 1, P2 0, P3 0) | verdict: pass\n", "findings: 2 (P0 1, P1 1, P2 0, P3 0) | verdict: blocked\n", "findings: 2 (P0 1, P1 1) | verdict: pass\n"} {
			if got := ReviewHeader(text); got != nil {
				t.Errorf("%q: %#v", text, got)
			}
		}
		want := &ReviewHeaderResult{Findings: 1, Severity: map[string]float64{"P0": 1, "P1": 0, "P2": 0, "P3": 0}, Verdict: "fail"}
		if got := ReviewHeader("findings: 1 (P0 1, P1 0, P2 0, P3 0) | verdict: fail\n# Report\n"); !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v", got)
		}
		want = &ReviewHeaderResult{Findings: 3, Severity: map[string]float64{"P0": 0, "P1": 1, "P2": 2, "P3": 0}, Verdict: "fail"}
		if got := ReviewHeader("  Findings: 3 (P0 0, P1 1, P2 2, P3 0) | Verdict: FAIL  \n\nrest of the report\n"); !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v", got)
		}
	})
	t.Run("differential corpus generated by the JS reportscan module", func(t *testing.T) {
		type partialCase struct {
			Name string `json:"name"`
			Text string `json:"text"`
			Want int    `json:"want"`
		}
		type reviewCase struct {
			Name string              `json:"name"`
			Text string              `json:"text"`
			Want *ReviewHeaderResult `json:"want"`
		}
		var corpus struct {
			Partial []partialCase `json:"partial"`
			Review  []reviewCase  `json:"review"`
		}
		data, err := os.ReadFile("testdata/reportscan.json")
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(data, &corpus); err != nil {
			t.Fatal(err)
		}
		for _, tc := range corpus.Partial {
			t.Run("partial/"+tc.Name, func(t *testing.T) {
				if got := PartialCount(tc.Text); got != tc.Want {
					t.Fatalf("got %d want %d", got, tc.Want)
				}
			})
		}
		for _, tc := range corpus.Review {
			t.Run("review/"+tc.Name, func(t *testing.T) {
				if got := ReviewHeader(tc.Text); !reflect.DeepEqual(got, tc.Want) {
					t.Fatalf("got %#v want %#v", got, tc.Want)
				}
			})
		}
	})
}

// Test layers report section and validator line (DJA-695 fatia 4).

const testLayersHeader = "findings: 0 (p0 0, p1 0, p2 0, p3 0) | verdict: pass"

func TestTestLayers(t *testing.T) {
	for _, tc := range []struct {
		name     string
		text     string
		found    bool
		pass     int
		fail     int
		notRun   int
		blocked  int
		na       int
		unmarked int
		nonPass  []LayerResult
	}{
		{
			name:  "all five results count and the non-pass layers come back in order",
			text:  "## Test layers\n- unit: PASS\n- integration: n/a\n- contract: NOT RUN: sh not in PATH\n- e2e: BLOCKED: the browser is not installed\n- mutation: FAIL: the suite failed\n",
			found: true, pass: 1, fail: 1, notRun: 1, blocked: 1, na: 1,
			nonPass: []LayerResult{{Name: "contract", Result: "NOT RUN"}, {Name: "e2e", Result: "BLOCKED"}, {Name: "mutation", Result: "FAIL"}},
		},
		{
			name:  "missing or punctuation-only reasons are unmarked",
			text:  "## Test layers\n- unit: FAIL\n- contract: NOT RUN:\n- e2e: BLOCKED: ...\n- mutation: FAIL: ---\n",
			found: true, unmarked: 4,
		},
		{
			name:  "lowercase pass is unmarked while n/a with a reason counts",
			text:  "## Test layers\n- unit: pass\n- integration: n/a: not a CLI project\n",
			found: true, na: 1, unmarked: 1,
		},
		{
			name:  "pass and n/a accept ignored text after the colon",
			text:  "## Test layers\n- unit: PASS: the full suite\n- contract: n/a\n",
			found: true, pass: 1, na: 1,
		},
		{
			name: "a section inside a fenced block does not count",
			text: "```\n## Test layers\n- unit: PASS\n```\n",
		},
		{
			name:  "a fenced heading is skipped and a later real section counts",
			text:  "```\n## Test layers\n- unit: PASS\n```\n## Test layers\n- unit: PASS\n",
			found: true, pass: 1,
		},
		{
			name:  "a tilde fence closed with a tab still closes: the FAIL after it counts",
			text:  "## Test layers\n- unit: PASS\n~~~\nexample\n~~~\t\n- e2e: FAIL: the suite failed\n",
			found: true, pass: 1, fail: 1,
			nonPass: []LayerResult{{Name: "e2e", Result: "FAIL"}},
		},
		{
			name:  "the next h2 title ends the section",
			text:  "## Test layers\n- unit: PASS\n## Next\n- integration: FAIL: after the title\n",
			found: true, pass: 1,
		},
		{
			name:  "an h1 title ends the section",
			text:  "## Test layers\n- unit: PASS\n# Report\n- integration: FAIL: after the title\n",
			found: true, pass: 1,
		},
		{
			name: "a report without the section is not found",
			text: "# Report\n- unit: PASS\n",
		},
		{
			name:  "formatting marks are unmarked because the form is exact",
			text:  "## Test layers\n- unit: **PASS**\n- integration: `PASS`\n",
			found: true, unmarked: 2,
		},
		{
			name:  "a portuguese reason is a reason",
			text:  "## Test layers\n- mutation: FAIL: o teste não passou\n",
			found: true, fail: 1,
			nonPass: []LayerResult{{Name: "mutation", Result: "FAIL"}},
		},
		{
			name:  "a layer name outside VALIDATION.md counts normally",
			text:  "## Test layers\n- design: NOT RUN: sem comando\n",
			found: true, notRun: 1,
			nonPass: []LayerResult{{Name: "design", Result: "NOT RUN"}},
		},
		{
			name:  "a name without the colon-space separator is unmarked",
			text:  "## Test layers\n- unit:PASS\n- integration: n/a\n",
			found: true, na: 1, unmarked: 1,
		},
		{
			name:  "a dangling colon after pass is not the form",
			text:  "## Test layers\n- unit: PASS:\n",
			found: true, unmarked: 1,
		},
		{
			name:  "numbered and table data rows are list lines without the form; a separator row is structure",
			text:  "## Test layers\n1. unit: PASS\n| unit | PASS |\n| --- | --- |\n",
			found: true, unmarked: 2,
		},
		{
			name:  "prose inside the section is ignored",
			text:  "## Test layers\nsome prose\n- unit: PASS\n",
			found: true, pass: 1,
		},
		{
			name:  "bullet variants are list lines",
			text:  "## Test layers\n* unit: PASS\n+ integration: n/a\n",
			found: true, pass: 1, na: 1,
		},
		{
			name:  "a digit-only reason is a reason",
			text:  "## Test layers\n- e2e: NOT RUN: 0\n",
			found: true, notRun: 1,
			nonPass: []LayerResult{{Name: "e2e", Result: "NOT RUN"}},
		},
		{
			name: "empty text is not found and has no lines",
			text: "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := TestLayers(tc.text)
			if got.Found != tc.found {
				t.Fatalf("found=%v want %v", got.Found, tc.found)
			}
			if got.Pass != tc.pass || got.Fail != tc.fail || got.NotRun != tc.notRun || got.Blocked != tc.blocked || got.NA != tc.na || got.Unmarked != tc.unmarked {
				t.Fatalf("counts got %+v want pass=%d fail=%d notRun=%d blocked=%d na=%d unmarked=%d",
					got, tc.pass, tc.fail, tc.notRun, tc.blocked, tc.na, tc.unmarked)
			}
			if !reflect.DeepEqual(got.NonPass, tc.nonPass) {
				t.Fatalf("nonPass got %+v want %+v", got.NonPass, tc.nonPass)
			}
		})
	}
}

func TestValidationLine(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want string
		ok   bool
	}{
		{"header then validation fail", testLayersHeader + "\nvalidation: FAIL\n", "FAIL", true},
		{"blank lines between header and line", testLayersHeader + "\n\n\nvalidation: NOT RUN\n", "NOT RUN", true},
		{"pass as the second non-empty line", testLayersHeader + "\nvalidation: PASS\n", "PASS", true},
		{"blocked as the second non-empty line", testLayersHeader + "\nvalidation: BLOCKED\n", "BLOCKED", true},
		{"crlf endings", testLayersHeader + "\r\nvalidation: FAIL\r\n", "FAIL", true},
		{"a later validation line does not count", testLayersHeader + "\nsome other line\nvalidation: PASS\n", "", false},
		{"without a review header", "first line prose\nvalidation: PASS\n", "", false},
		{"lowercase result", testLayersHeader + "\nvalidation: pass\n", "", false},
		{"capital v does not match the form", testLayersHeader + "\nValidation: PASS\n", "", false},
		{"a reason suffix is not the form", testLayersHeader + "\nvalidation: FAIL: because\n", "", false},
		{"only the header", testLayersHeader + "\n", "", false},
		{"empty text", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ValidationLine(tc.text)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("got (%q, %v) want (%q, %v)", got, ok, tc.want, tc.ok)
			}
		})
	}
}
