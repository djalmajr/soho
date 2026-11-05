package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/taskreport"
)

// DJA-713: the worker works in the tree its brief declares, not in the
// directory its pane opened in. A reused pane stays in an old worktree, so
// testing a cited path against the pane warned about paths the declared tree
// holds, and brief_lint=strict refused those briefs.
func TestDispatchLintUsesTheDeclaredWorktree(t *testing.T) {
	const cited = "cited-only-here.txt"
	const warning = "which the worker in"
	brief := func(worktree string) string {
		body := "# Goal\nrun\n# Expected result\nok\n# Owned files\nnone\n# Forbidden\nno commit or push\n# Report\ndone\n\nRead `" + cited + "`.\n"
		if worktree != "" {
			body = "Worktree: `" + worktree + "`\n\n" + body
		}
		return body
	}
	setup := func(t *testing.T, lint string) (*dispatchArrivalFixture, string) {
		t.Helper()
		f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
		cwd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		// The orchestrator's checkout holds the file; the pane's directory does not.
		outside := filepath.Join(cwd, cited)
		if err := os.WriteFile(outside, []byte("fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		roster := "worker\tw0test:p0a\tcodex\timplementer\topenai\t0\t" + t.TempDir() + "\tnow\tgpt-5\ttask\timplementer\n"
		if err := os.WriteFile(filepath.Join(f.state, "ws", "agents.tsv"), []byte(roster), 0o600); err != nil {
			t.Fatal(err)
		}
		f.env["SOHO_BRIEF_LINT"] = lint
		f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
		return f, outside
	}
	treeWith := func(t *testing.T) string {
		t.Helper()
		tree := t.TempDir()
		if err := os.WriteFile(filepath.Join(tree, cited), []byte("fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return tree
	}

	t.Run("a path the declared tree holds does not warn", func(t *testing.T) {
		f, _ := setup(t, "warn")
		writeRoleFixture(t, f.brief, brief(treeWith(t)))
		code, _, stderr := f.run(t, "worker", f.brief, "--no-wait")
		if code != 0 || strings.Contains(stderr, warning) {
			t.Fatalf("code=%d stderr=%s", code, stderr)
		}
	})

	t.Run("strict does not refuse a path the declared tree holds", func(t *testing.T) {
		f, _ := setup(t, "strict")
		writeRoleFixture(t, f.brief, brief(treeWith(t)))
		code, _, stderr := f.run(t, "worker", f.brief, "--no-wait")
		if code != 0 || strings.Contains(stderr, warning) {
			t.Fatalf("code=%d stderr=%s", code, stderr)
		}
	})

	t.Run("a path the declared tree lacks warns and names that tree", func(t *testing.T) {
		f, outside := setup(t, "warn")
		tree := t.TempDir()
		writeRoleFixture(t, f.brief, brief(tree))
		code, _, stderr := f.run(t, "worker", f.brief, "--no-wait")
		if code != 0 || !strings.Contains(stderr, warning+" "+tree+" ") || !strings.Contains(stderr, outside) {
			t.Fatalf("code=%d stderr=%s", code, stderr)
		}
	})

	t.Run("strict refuses a path the declared tree lacks", func(t *testing.T) {
		f, _ := setup(t, "strict")
		writeRoleFixture(t, f.brief, brief(t.TempDir()))
		code, _, stderr := f.run(t, "worker", f.brief, "--no-wait")
		if code != 2 || !strings.Contains(stderr, warning) {
			t.Fatalf("code=%d stderr=%s", code, stderr)
		}
	})

	t.Run("without a line the pane's directory still decides", func(t *testing.T) {
		f, outside := setup(t, "warn")
		writeRoleFixture(t, f.brief, brief(""))
		code, _, stderr := f.run(t, "worker", f.brief, "--no-wait")
		if code != 0 || !strings.Contains(stderr, warning) || !strings.Contains(stderr, outside) {
			t.Fatalf("code=%d stderr=%s", code, stderr)
		}
	})

	amendment := func(t *testing.T, f *dispatchArrivalFixture, original string) {
		t.Helper()
		dir := filepath.Join(f.state, "ws", "briefs")
		report := filepath.Join(f.state, "ws", "reports", "worker-20200101T100000.md")
		stable := filepath.Join(f.state, "ws", "reports", "worker-20200101T100000.current.md")
		writeRoleFixture(t, filepath.Join(dir, "worker-20200101T100000.md"), "You are running as the `implementer` role\nWorktree: "+original+"\n")
		pointer, err := json.Marshal(map[string]any{"version": 1, "task_report": stable, "current": report, "history": []string{}})
		if err != nil {
			t.Fatal(err)
		}
		writeRoleFixture(t, taskreport.TaskReportPointerPath(filepath.Join(f.state, "ws"), "worker"), string(pointer)+"\n")
		writeRoleFixture(t, filepath.Join(f.state, "ws", "last-report-worker"), report+"\n")
	}

	t.Run("an amendment without a line uses the original brief's tree", func(t *testing.T) {
		f, _ := setup(t, "warn")
		amendment(t, f, treeWith(t))
		amend := filepath.Join(f.root, "amend.md")
		writeRoleFixture(t, amend, "# Amendment\n\nRead `"+cited+"`.\n")
		code, _, stderr := f.run(t, "worker", amend, "--amend", "--no-wait")
		if code != 0 || strings.Contains(stderr, warning) {
			t.Fatalf("code=%d stderr=%s", code, stderr)
		}
	})

	t.Run("an amendment's own line wins over the original's", func(t *testing.T) {
		f, _ := setup(t, "warn")
		amendment(t, f, treeWith(t))
		own := t.TempDir()
		amend := filepath.Join(f.root, "amend.md")
		writeRoleFixture(t, amend, "# Amendment\n\nWorktree: `"+own+"`\n\nRead `"+cited+"`.\n")
		code, _, stderr := f.run(t, "worker", amend, "--amend", "--no-wait")
		if code != 0 || !strings.Contains(stderr, warning+" "+own+" ") {
			t.Fatalf("code=%d stderr=%s", code, stderr)
		}
	})
}
