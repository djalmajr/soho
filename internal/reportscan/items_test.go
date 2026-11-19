package reportscan

import (
	"reflect"
	"strings"
	"testing"
)

func TestItemsMatchLimitCountsOutsideFences(t *testing.T) {
	text := strings.Join([]string{
		"# report",
		"- [done] first",
		"- [partial] real",
		"| [env] | note |",
		"- [env] --",
		"- [skipped] last",
		"```text",
		"- [partial] fenced",
		"```",
		"## Follow-ups",
		"- [partial] follow-up, not an item",
		"## Open questions",
		"- [partial] counted again",
		"",
	}, "\n")
	items := Items(text)
	want := []Item{
		{Line: 2, Mark: "done", Text: "- [done] first"},
		{Line: 3, Mark: "partial", Text: "- [partial] real"},
		{Line: 4, Mark: "env", Text: "| [env] | note |"},
		{Line: 5, Mark: "partial", Text: "- [env] --"},
		{Line: 6, Mark: "skipped", Text: "- [skipped] last"},
		{Line: 13, Mark: "partial", Text: "- [partial] counted again"},
	}
	if !reflect.DeepEqual(items, want) {
		t.Fatalf("items:\n got %#v\nwant %#v", items, want)
	}
	partial, env := 0, 0
	for _, item := range items {
		if item.Mark == "partial" {
			partial++
		}
		if item.Mark == "env" {
			env++
		}
	}
	if partial != PartialCount(text) || env != EnvCount(text) {
		t.Fatalf("item counts partial=%d env=%d; limits partial=%d env=%d", partial, env, PartialCount(text), EnvCount(text))
	}
}

func TestItemsFenceAndHeadingHostileText(t *testing.T) {
	text := "# report\r\n" +
		"```md\r\n" +
		"## Test layers\r\n" +
		"- [partial] inside fence\r\n" +
		"```\r\n" +
		"## Follow-ups\r\n" +
		"<!-- ## Open questions -->\r\n" +
		"- [partial] still in follow-ups\r\n" +
		"## Open questions\r\n" +
		"- `[partial]` inline code in a bullet\r\n" +
		strings.Repeat("x", 400) + " [partial]\r\n"
	items := Items(text)
	marks := map[int]string{}
	for _, item := range items {
		marks[item.Line] = item.Mark
	}
	want := map[int]string{10: "partial"}
	if !reflect.DeepEqual(marks, want) {
		t.Fatalf("marks by line: got %v, want %v", marks, want)
	}
	partial, env := 0, 0
	for _, item := range items {
		if item.Mark == "partial" {
			partial++
		}
		if item.Mark == "env" {
			env++
		}
	}
	if partial != PartialCount(text) || env != EnvCount(text) {
		t.Fatalf("item counts partial=%d env=%d; limits partial=%d env=%d", partial, env, PartialCount(text), EnvCount(text))
	}
}

func TestItemsEmptyAndOnlyFence(t *testing.T) {
	for _, text := range []string{"", "```\n- [partial] fenced\n```\n"} {
		if got := Items(text); len(got) != 0 {
			t.Fatalf("Items(%q) = %#v, want empty", text, got)
		}
	}
}

// TestItemsOnePerEligibleMark pins the per-line classification shared with
// PartialCount/EnvCount: a line with a partial and an env mark yields both
// items, done and skipped need the position predicate too (an inline-code
// mention in prose is no item), CRLF and fences change nothing, and the
// projected totals equal the limit scanners.
func TestItemsOnePerEligibleMark(t *testing.T) {
	text := "# Report\r\n" +
		"- [partial] needs a rerun | [env] | Linux host only |\r\n" +
		"- mentions `[done]` and nothing else\r\n" +
		"- mentions `[skipped]` and nothing else\r\n" +
		"~~~md\r\n" +
		"- [env] fenced\r\n" +
		"~~~\r\n" +
		"- `[partial]` real inline-code marker\r\n"
	want := []Item{
		{Line: 2, Mark: "partial", Text: "- [partial] needs a rerun | [env] | Linux host only |"},
		{Line: 2, Mark: "env", Text: "- [partial] needs a rerun | [env] | Linux host only |"},
		{Line: 8, Mark: "partial", Text: "- `[partial]` real inline-code marker"},
	}
	items := Items(text)
	if !reflect.DeepEqual(items, want) {
		t.Fatalf("items:\n got %#v\nwant %#v", items, want)
	}
	totals := map[string]int{}
	for _, item := range items {
		totals[item.Mark]++
	}
	wantTotals := map[string]int{"partial": 2, "env": 1}
	if !reflect.DeepEqual(totals, wantTotals) {
		t.Fatalf("totals: got %v, want %v (no done or skipped item)", totals, wantTotals)
	}
	if totals["partial"] != PartialCount(text) || totals["env"] != EnvCount(text) {
		t.Fatalf("items partial=%d env=%d; limits partial=%d env=%d", totals["partial"], totals["env"], PartialCount(text), EnvCount(text))
	}
}

// TestItemsDoneAndSkippedNeedPosition: a positioned done or skipped mark is an
// item; prose that merely mentions the inline-code mark is not.
func TestItemsDoneAndSkippedNeedPosition(t *testing.T) {
	text := "- [done] positioned\n" +
		"| [skipped] | in a table cell |\n" +
		"- `[done]` inline code positioned by the bullet\n" +
		"- a sentence that mentions `[done]` mid-line\n" +
		"- [done] and [skipped] on one line is ambiguous\n"
	want := []Item{
		{Line: 1, Mark: "done", Text: "- [done] positioned"},
		{Line: 2, Mark: "skipped", Text: "| [skipped] | in a table cell |"},
		{Line: 3, Mark: "done", Text: "- `[done]` inline code positioned by the bullet"},
	}
	if got := Items(text); !reflect.DeepEqual(got, want) {
		t.Fatalf("items:\n got %#v\nwant %#v", got, want)
	}
}

func TestTestLayerEntries(t *testing.T) {
	text := "## Test layers\n\n- unit: PASS\n- race: NOT RUN: host gate\n- other: FAIL — failed for a reason\n- bad: x\n| --- |\n- [partial] an item, not a layer\n```md\n- docs: n/a\n```\n## Open questions\n- unit: PASS\n"
	got := TestLayerEntries(text)
	want := []LayerEntry{
		{Name: "unit", Result: "PASS"},
		{Name: "race", Result: "NOT RUN", Reason: "host gate"},
		{Name: "other", Result: "FAIL", Reason: "failed for a reason"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("entries:\n got %#v\nwant %#v", got, want)
	}
}
