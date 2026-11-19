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
