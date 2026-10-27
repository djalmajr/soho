package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
)

func TestAudit539AuthorSelection(t *testing.T) {
	for _, mode := range []string{"same", "trailing-slash", "clean-dot", "symlink", "windows-case", "other-tree", "custom-edit", "custom-read-only", "non-review"} {
		t.Run(mode, func(t *testing.T) {
			f := roleDispatchFixture(t)
			tree := filepath.Join(f.root, "slice")
			authorTree, role := tree, "implementer"
			switch mode {
			case "trailing-slash":
				authorTree = tree + string(filepath.Separator)
			case "clean-dot":
				authorTree = tree + string(filepath.Separator) + "."
			case "other-tree":
				authorTree = tree + "-other"
			case "symlink":
				if err := os.Mkdir(tree, 0700); err != nil {
					t.Fatal(err)
				}
				authorTree = filepath.Join(f.root, "linked-slice")
				if err := os.Symlink(tree, authorTree); err != nil {
					t.Fatalf("symlink fixture: %v", err)
				}
			case "windows-case":
				if runtime.GOOS != "windows" {
					t.Skip("requires native Windows case folding")
				}
				authorTree = strings.ToUpper(tree)
			case "custom-edit":
				role = "custom-author"
				writeRoleFixture(t, filepath.Join(f.root, "roles", role+".md"), "---\nname: Custom author\nmode: edit\n---\nEdit this project.\n")
				if !core.RoleIsEdit(role, f.env, f.root) {
					t.Fatal("fixture role is not an edit role")
				}
			case "custom-read-only":
				role = "custom-reader"
				writeRoleFixture(t, filepath.Join(f.root, "roles", role+".md"), "---\nname: Custom reader\nmode: read-only\n---\nRead this project.\n")
				if core.RoleIsEdit(role, f.env, f.root) {
					t.Fatal("fixture role must be read-only")
				}
			}
			base := filepath.Join(f.state, "ws", "briefs", "author-20200101T100000")
			writeRoleFixture(t, base+".md", fmt.Sprintf("You are running as the `%s` role\nWorktree: %s\nType: backend\n", role, authorTree))
			writeRoleFixture(t, base+".dispatch.json", fmt.Sprintf(`{"version":1,"submission":"accepted","kind":"pi","model":"author-model","effort":"","role":%q}`, role))
			writeRoleFixture(t, f.brief, "Type: review\nWorktree: "+tree+"\nReview the completed change.\n")
			dispatchedRole := "reviewer"
			if mode == "non-review" {
				dispatchedRole = "implementer"
				writeRoleFixture(t, f.brief, "Type: backend\nWorktree: "+tree+"\nImplement the change.\n")
			}
			code, out, msg := f.run(t, "worker", f.brief, "--role", dispatchedRole, "--no-wait")
			if code != 0 {
				t.Fatalf("dispatch rc=%d out=%s stderr=%s", code, out, msg)
			}
			composed, _ := dispatchOutputField(t, out, "composed_prompt")
			side := strings.TrimSuffix(composed, ".md") + ".dispatch.json"
			o := authorObject(side)
			got := authorString(o, "author_model")
			t.Logf("mode=%s recorded_edit_role=%s author_tree=%q review_tree=%q exit=%d author_model=%q sidecar=%s", mode, role, authorTree, tree, code, got, mustRead(t, side))
			want := "author-model"
			if mode == "other-tree" || mode == "non-review" || mode == "custom-read-only" {
				want = ""
			}
			if got != want {
				t.Fatalf("author_model=%q want %q", got, want)
			}
		})
	}
}

func TestAudit539LatestTieUsesDispatchSuffix(t *testing.T) {
	f := roleDispatchFixture(t)
	dir := filepath.Join(f.state, "ws", "briefs")
	now := time.Unix(1700000000, 0)
	for _, name := range []string{"author-20200101T100000", "author-20200101T100000-2"} {
		base := filepath.Join(dir, name)
		writeRoleFixture(t, base+".md", "You are running as the `implementer` role\nWorktree: "+filepath.Join(f.root, "slice")+"\nType: backend\n")
		if err := os.Chtimes(base+".md", now, now); err != nil {
			t.Fatal(err)
		}
		writeRoleFixture(t, base+".dispatch.json", fmt.Sprintf(`{"version":1,"submission":"accepted","kind":"pi","model":%q,"role":"implementer"}`, name))
	}
	p := filepath.Join(dir, "review-20200101T100001.dispatch.json")
	writeRoleFixture(t, strings.TrimSuffix(p, ".dispatch.json")+".md", "Worktree: "+filepath.Join(f.root, "slice")+"\n")
	got := lookupDispatchAuthor(p)
	t.Logf("equal-mtime collision result=%v", got)
	if got["author_model"] != "author-20200101T100000-2" {
		t.Fatalf("latest task suffix was not selected: %v", got)
	}
}
