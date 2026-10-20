package dispatch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/collaboration"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

func TestPromptExplainsActiveAssignmentWithoutNormalCompletion(t *testing.T) {
	root := t.TempDir()
	env := platform.Env{"SOHO_DIR": t.TempDir(), "HERDR_WORKSPACE_ID": "ws"}
	skill, _ := filepath.Abs(filepath.Join("..", "..", "skills", "soho"))
	env["SOHO_SKILL_DIR"] = skill
	ctx := core.Config{Entries: map[string]core.ConfigEntry{}}
	sd := core.StateDir(&ctx, env, root)
	core.RosterAppend(sd, []string{"author", "ws:p1", "codex", "implementer", "openai", "0", root, "one", "gpt-5.4", "ask", "implementer", "build"})
	a, err := (collaboration.Store{StateDir: sd}).Create(collaboration.Assignment{Author: collaboration.Participant{Name: "author", Pane: "ws:p1", Role: "implementer", Family: "openai", Cwd: root, Started: "one", Session: "a"}, Reviewer: collaboration.Participant{Name: "reviewer", Pane: "ws:p2", Role: "reviewer", Family: "anthropic", Cwd: root, Started: "two", Session: "r"}, Workspace: "ws", Root: root, BriefHash: "brief", Paths: []string{"file.go"}})
	if err != nil {
		t.Fatal(err)
	}
	role := filepath.Join(root, "role.md")
	if err := os.WriteFile(role, []byte("---\nname: implementer\n---\nRole body\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, prompt := range []string{ComposePrompt(role, "implementer", "author", "Brief", "/report.md", &ctx, env, "codex", "", "", false), ComposeAmendment("Amendment", "/report.md", &ctx, env, "codex", "", "", false, "author")} {
		for _, needle := range []string{a.ID, "counterpart: `reviewer`", "--type review.question", "immutable snapshot", "distinct sibling", "only the orchestrator may finalize", "do not resend"} {
			if !strings.Contains(prompt, needle) {
				t.Fatalf("missing collaboration contract: %s", needle)
			}
		}
		if strings.Contains(prompt, "the report file is the only signal") || strings.Contains(prompt, "orchestrator treats its existence as completion") {
			t.Fatal("cooperative prompt retained normal completion")
		}
	}
	normal := ComposePrompt(role, "implementer", "unassigned", "Brief", "/report.md", &ctx, env, "codex", "", "", false)
	if !strings.Contains(normal, "the report file is the only signal") || strings.Contains(normal, "Active collaboration") {
		t.Fatal("unassigned prompt changed completion")
	}
}
