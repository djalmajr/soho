package cli

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// R424 F1: a review that declares "Author: orchestrator" freezes no author,
// even when an older accepted implementer brief declared the same tree.
func TestOrchestratorAuthorFreezesNoAuthor(t *testing.T) {
	for _, withTree := range []bool{false, true} {
		t.Run(fmt.Sprintf("worktree=%v", withTree), func(t *testing.T) {
			f := roleDispatchFixture(t)
			tree := filepath.Join(f.root, "slice")
			base := filepath.Join(f.state, "ws", "briefs", "author-20200101T100000")
			writeRoleFixture(t, base+".md", "You are running as the `implementer` role\nWorktree: "+tree+"\nType: backend\n")
			writeRoleFixture(t, base+".dispatch.json", `{"version":1,"submission":"accepted","kind":"pi","model":"previous-worker","role":"implementer"}`)
			body := "Type: review\nAuthor: orchestrator\nReview the orchestrator's code.\n"
			if withTree {
				body += "Worktree: " + tree + "\n"
			}
			writeRoleFixture(t, f.brief, body)
			code, out, msg := f.run(t, "worker", f.brief, "--role", "reviewer", "--no-wait")
			if code != 0 {
				t.Fatalf("dispatch rc=%d out=%s stderr=%s", code, out, msg)
			}
			composed, _ := dispatchOutputField(t, out, "composed_prompt")
			if got := authorString(authorObject(strings.TrimSuffix(composed, ".md")+".dispatch.json"), "author_model"); got != "" {
				t.Fatalf("Author: orchestrator attributed to the prior worker %q; want no author snapshot", got)
			}
			if strings.Contains(msg, "no author found") || strings.Contains(msg, "has no Worktree: line") {
				t.Fatalf("Author: orchestrator must not warn: %s", msg)
			}
		})
	}
}
