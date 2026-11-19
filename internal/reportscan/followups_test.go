package reportscan

import "testing"

// The stop criterion puts behavior identical to main under ## Follow-ups, so
// those items must not lower the effective verdict. Only a level-2 heading
// opens the section; heading text is trimmed and lowercased. Emphasis such as
// **Follow-ups** is part of the title and deliberately does not match.
func TestFollowupsSectionCounts(t *testing.T) {
	for _, tc := range []struct {
		name, text   string
		partial, env int
	}{
		{
			name:    "partial under follow-ups",
			text:    "## Follow-ups\n\n- [partial] identical to main\n",
			partial: 0,
		},
		{
			name:    "same line under findings",
			text:    "## Findings\n\n- [partial] identical to main\n",
			partial: 1,
		},
		// R574: a heading inside an HTML comment does not move the section.
		{
			name:    "commented heading does not open",
			text:    "## Findings\n\n<!--\n## Follow-ups\n-->\n\n- [partial] candidate check failed\n",
			partial: 1,
		},
		{
			name:    "commented heading does not close",
			text:    "## Follow-ups\n\n<!--\n## Findings\n-->\n\n- [partial] identical to main\n",
			partial: 0,
		},
		// R572: ATX heading syntax, not trimmed text.
		{
			name:    "indented code does not open",
			text:    "## Findings\n\n    ## Follow-ups\n\n- [partial] candidate check failed\n",
			partial: 1,
		},
		{
			name:    "indented code does not close",
			text:    "## Follow-ups\n\n    ## Findings\n\n- [partial] identical to main\n",
			partial: 0,
		},
		{
			name:    "three spaces still open",
			text:    "   ## Follow-ups\n\n- [partial] identical to main\n",
			partial: 0,
		},
		{
			name:    "closing sequence opens",
			text:    "## Follow-ups ##\n\n- [partial] identical to main\n",
			partial: 0,
		},
		{
			name:    "empty level-two heading closes",
			text:    "## Follow-ups\n\n##\n\n- [partial] failed check\n",
			partial: 1,
		},
		{
			name:    "hashtag is not a heading",
			text:    "## Follow-ups\n\n#tag\n- [partial] identical to main\n",
			partial: 0,
		},
		{
			name:    "level three stays inside",
			text:    "## Follow-ups\n\n### Detail\n\n- [partial] identical to main\n",
			partial: 0,
		},
		{
			name:    "level two closes",
			text:    "## Follow-ups\n\n## Test layers\n\n- [partial] failed check\n",
			partial: 1,
		},
		{
			name:    "fenced heading does not open",
			text:    "```md\n## Follow-ups\n```\n\n- [partial] failed check\n",
			partial: 1,
		},
		{
			name:    "fenced heading does not close",
			text:    "## Follow-ups\n\n```md\n## Test layers\n```\n\n- [partial] identical to main\n",
			partial: 0,
		},
		{
			name: "env under follow-ups",
			text: "## Follow-ups\n\n- [env] no native Windows; host: go test ./...\n",
			env:  0,
		},
		{
			name: "env under findings",
			text: "## Findings\n\n- [env] no native Windows; host: go test ./...\n",
			env:  1,
		},
		{
			name:    "section at end",
			text:    "## Findings\n\n- [partial] failed check\n\n## Follow-ups\n\n- [partial] identical to main",
			partial: 1,
		},
		{
			name:    "trailing spaces",
			text:    "## Follow-ups   \n\n- [partial] identical to main\n",
			partial: 0,
		},
		{
			name:    "emphasis is not the title",
			text:    "## **Follow-ups**\n\n- [partial] failed check\n",
			partial: 1,
		},
		{
			name:    "level three does not open",
			text:    "### Follow-ups\n\n- [partial] failed check\n",
			partial: 1,
		},
		{
			name:    "level one closes",
			text:    "## Follow-ups\n\n# Report\n\n- [partial] failed check\n",
			partial: 1,
		},
		{
			name:    "space variant",
			text:    "## follow ups\n\n- [partial] identical to main\n",
			partial: 0,
		},
		{
			name:    "compact variant uppercase",
			text:    "## FOLLOWUPS\n\n- [partial] identical to main\n",
			partial: 0,
		},
		{
			name:    "name in another section counts",
			text:    "## Findings\n\n- [partial] Follow-up failed check\n",
			partial: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			partial, env := PartialCount(tc.text), EnvCount(tc.text)
			if partial != tc.partial || env != tc.env {
				t.Fatalf("partial=%d env=%d want %d/%d text=%q", partial, env, tc.partial, tc.env, tc.text)
			}
		})
	}
}
