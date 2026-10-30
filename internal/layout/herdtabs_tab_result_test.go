package layout

import (
	"fmt"
	"testing"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// The adapter's tab get parses the label and root pane whatever its exit
// status, and the split path returns that root as the worker's destination
// even when the command failed: a nonzero exit must not lose the parsed
// root (DJA-596 review, 2026-10-30).
func TestHerdTabSplitKeepsTheParsedRootWhenTheTabGetFails(t *testing.T) {
	for _, code := range []int{0, 1} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			env, ctx, cwd, _ := layoutFake(t, []fakecli.Rule{
				{Argv: []string{"pane", "list", "--workspace", "ws"}, Stdout: `{"result":{"panes":[]}}`},
				{Argv: []string{"tab", "get", "th"}, Code: code, Stdout: `{"result":{"tab":{"label":"workers"},"root_pane":{"pane_id":"rootp"}}}`},
			})
			got := HerdTabSplit(ctx, "th", cwd, env, cwd)
			t.Logf("tab-get exit=%d HerdTabSplit=%+v", code, got)
			if !got.Ok || got.Pane != "rootp" {
				t.Fatalf("preserve the adapter's parsed root even on nonzero tab-get: got=%+v", got)
			}
		})
	}
}
