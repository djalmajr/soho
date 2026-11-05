package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/dispatch"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/taskreport"
)

// DJA-684: a review dispatched with --amend to a review whose original
// brief declares a Worktree: line keeps the line in the composed amendment,
// and the sidecar records the author.
// A composed brief that is a link, or over the limit, gives no line to the
// amendment: its target is never read.
func TestAmendmentOriginalBriefRefusesLinksAndLongFiles(t *testing.T) {
	sd := t.TempDir()
	briefs := filepath.Join(sd, "briefs")
	reports := filepath.Join(sd, "reports")
	for _, dir := range []string{briefs, reports} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	report := filepath.Join(reports, "worker-20200101T100000.md")
	composed := filepath.Join(briefs, "worker-20200101T100000.md")
	prior := jsonjs.O("current", report)
	writeRoleFixture(t, composed, "Worktree: /abs/slice\n")
	if got := amendmentOriginalBrief(sd, prior); !strings.Contains(got, "/abs/slice") {
		t.Fatalf("control: a regular composed brief was not read: %q", got)
	}
	if err := os.Remove(composed); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "elsewhere.md")
	writeRoleFixture(t, target, "Worktree: /abs/elsewhere\n")
	if err := os.Symlink(target, composed); err == nil {
		if got := amendmentOriginalBrief(sd, prior); got != "" {
			t.Fatalf("a linked composed brief was read: %q", got)
		}
		if err := os.Remove(composed); err != nil {
			t.Fatal(err)
		}
	}
	writeRoleFixture(t, composed, "Worktree: /abs/slice\n"+strings.Repeat("x", amendmentOriginalBriefLimit))
	if got := amendmentOriginalBrief(sd, prior); got != "" {
		t.Fatalf("a composed brief over the limit was read: %d bytes", len(got))
	}
}

func TestDispatchAmendKeepsWorktreeAndAuthor(t *testing.T) {
	f := roleDispatchFixture(t)
	tree := filepath.Join(f.root, "slice")
	dir := filepath.Join(f.state, "ws", "briefs")
	report := filepath.Join(f.state, "ws", "reports", "worker-20200101T100000.md")
	stable := filepath.Join(f.state, "ws", "reports", "worker-20200101T100000.current.md")

	// The author's accepted edit-role brief declares the worktree.
	writeRoleFixture(t, filepath.Join(dir, "author-20200101T100000.md"), "You are running as the `implementer` role\nType: backend\nWorktree: "+tree+"\n")
	writeRoleFixture(t, filepath.Join(dir, "author-20200101T100000.dispatch.json"), `{"version":1,"submission":"accepted","kind":"pi","model":"qwen-author","role":"implementer"}`)
	// The worker's open task: a review brief that declares the same worktree.
	writeRoleFixture(t, filepath.Join(dir, "worker-20200101T100000.md"), "You are running as the `reviewer` role\nType: review\nWorktree: "+tree+"\n")
	writeRoleFixture(t, filepath.Join(dir, "worker-20200101T100000.dispatch.json"), `{"version":1,"submission":"accepted","kind":"codex","model":"rev-model","role":"reviewer"}`)
	pointer, err := json.Marshal(map[string]any{"version": 1, "task_report": stable, "current": report, "history": []string{}})
	if err != nil {
		t.Fatal(err)
	}
	writeRoleFixture(t, taskreport.TaskReportPointerPath(filepath.Join(f.state, "ws"), "worker"), string(pointer)+"\n")
	writeRoleFixture(t, filepath.Join(f.state, "ws", "last-report-worker"), report+"\n")

	// The amendment declares no Worktree: line.
	amend := filepath.Join(f.root, "amend.md")
	writeRoleFixture(t, amend, "# Amendment\n\nFix the thing.\n")
	code, out, errText := f.run(t, "worker", amend, "--amend", "--no-wait")
	if code != 0 {
		t.Fatalf("dispatch rc=%d out=%s err=%s", code, out, errText)
	}
	composed, ok := dispatchOutputField(t, out, "composed_prompt")
	if !ok {
		t.Fatalf("missing composed_prompt in %s", out)
	}
	body, err := os.ReadFile(composed)
	if err != nil {
		t.Fatal(err)
	}
	if treeGot := dispatch.DeclaredWorktree(string(body)); treeGot != tree {
		t.Fatalf("the composed amendment lost the original's worktree (DeclaredWorktree=%q)", treeGot)
	}
	sidecar := authorObject(strings.TrimSuffix(composed, ".md") + ".dispatch.json")
	if sidecar == nil {
		t.Fatal("missing the amendment sidecar")
	}
	if got := authorString(sidecar, "author_model"); got != "qwen-author" {
		t.Fatalf("amended review has no author: author_model=%q (want qwen-author)", got)
	}
	for _, msg := range []string{"has no Worktree: line", "no author found for this review"} {
		if strings.Contains(errText, msg) {
			t.Fatalf("unexpected warning %q: %s", msg, errText)
		}
	}
}

// DJA-684: the once-per-dispatch review author warnings. The no-line and
// no-candidate messages never block the dispatch and land in friction.log;
// a found author, a preserved one, and non-review roles stay silent.
func TestDispatchReviewAuthorWarnings(t *testing.T) {
	for _, tc := range []struct {
		name          string
		role          string
		author        bool
		worktree      string
		wantWarn      string
		wantNoWarnSub []string
		wantAuthor    bool
	}{
		{name: "no-line", role: "reviewer", wantWarn: "has no Worktree: line", wantNoWarnSub: []string{"no author found"}},
		{name: "no-candidate", role: "reviewer", worktree: "slice", wantWarn: "no author found for this review", wantNoWarnSub: []string{"has no Worktree: line"}},
		{name: "with-author", role: "reviewer", author: true, worktree: "slice", wantNoWarnSub: []string{"has no Worktree: line", "no author found"}, wantAuthor: true},
		{name: "project-root", role: "reviewer", author: true, worktree: "root", wantNoWarnSub: []string{"has no Worktree: line", "no author found"}, wantAuthor: true},
		{name: "scouter", role: "scouter", wantNoWarnSub: []string{"has no Worktree: line", "no author found"}},
		{name: "implementer", role: "implementer", wantNoWarnSub: []string{"has no Worktree: line", "no author found"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := roleDispatchFixture(t)
			var tree string
			switch tc.worktree {
			case "slice":
				tree = filepath.Join(f.root, "slice")
			case "root":
				tree = f.root
			}
			dir := filepath.Join(f.state, "ws", "briefs")
			if tc.author {
				writeRoleFixture(t, filepath.Join(dir, "author-20200101T100000.md"), "You are running as the `implementer` role\nType: backend\nWorktree: "+tree+"\n")
				writeRoleFixture(t, filepath.Join(dir, "author-20200101T100000.dispatch.json"), `{"version":1,"submission":"accepted","kind":"pi","model":"qwen-author","role":"implementer"}`)
			}
			body := "Type: review\nReview the slice.\n"
			if tree != "" {
				body += "Worktree: " + tree + "\n"
			}
			writeRoleFixture(t, f.brief, body)
			code, out, errText := f.run(t, "worker", f.brief, "--role", tc.role, "--no-wait")
			if code != 0 {
				t.Fatalf("dispatch must not be blocked: rc=%d out=%s err=%s", code, out, errText)
			}
			composed, ok := dispatchOutputField(t, out, "composed_prompt")
			if !ok {
				t.Fatalf("missing composed_prompt in %s", out)
			}
			if tc.wantWarn != "" {
				if !strings.Contains(errText, "soho: warning: ") || !strings.Contains(errText, tc.wantWarn) {
					t.Fatalf("warning %q missing: stderr=%q", tc.wantWarn, errText)
				}
				if n := strings.Count(errText, tc.wantWarn); n != 1 {
					t.Fatalf("the warning fired %d times, once per dispatch is the contract: %s", n, errText)
				}
				log, err := os.ReadFile(filepath.Join(f.state, "ws", "friction.log"))
				if err != nil {
					t.Fatalf("friction.log not written: %v", err)
				}
				if !strings.Contains(string(log), tc.wantWarn) {
					t.Fatalf("friction.log lacks the warning: %s", string(log))
				}
			}
			for _, sub := range tc.wantNoWarnSub {
				if strings.Contains(errText, sub) {
					t.Fatalf("unexpected warning %q: %s", sub, errText)
				}
			}
			sidecar := authorObject(strings.TrimSuffix(composed, ".md") + ".dispatch.json")
			if sidecar == nil {
				t.Fatal("missing the sidecar")
			}
			if tc.wantAuthor && authorString(sidecar, "author_model") != "qwen-author" {
				t.Fatalf("author not recorded: %v", sidecar)
			}
			if !tc.wantAuthor && tc.role == "reviewer" {
				for _, key := range authorFields {
					if _, present := sidecar.Get(key); present {
						t.Fatalf("unexpected %s without an author", key)
					}
				}
			}
		})
	}
}
