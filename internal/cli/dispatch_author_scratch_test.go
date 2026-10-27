package cli

import (
	"crypto/sha256"
	"fmt"
	"github.com/djalmajr/soho/internal/dispatch"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/taskreport"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAudit539ScratchReviewFindsStateAuthor(t *testing.T) {
	f := roleDispatchFixture(t)
	tree := filepath.Join(f.root, "slice")
	if e := os.Mkdir(tree, 0700); e != nil {
		t.Fatal(e)
	}
	dir := filepath.Join(f.state, "ws", "briefs")
	base := filepath.Join(dir, "author-20200101T100000")
	body := "You are running as the `implementer` role\nWorktree: " + tree + "\nType: backend\n"
	writeRoleFixture(t, base+".md", body)
	h := sha256.Sum256([]byte(body))
	if e := writeDispatchSidecarWithTask(base+".dispatch.json", "pi", "state-author", "high", filepath.Join(f.state, "ws", "reports", "author.current.md"), fmt.Sprintf("%x", h), "accepted", "", "session", "implementer"); e != nil {
		t.Fatal(e)
	}
	roster := filepath.Join(f.state, "ws", "agents.tsv")
	rows := strings.Split(mustRead(t, roster), "\n")
	for i, line := range rows {
		if strings.HasPrefix(line, "worker\t") {
			cols := strings.Split(line, "\t")
			cols[6] = tree
			rows[i] = strings.Join(cols, "\t")
		}
	}
	writeRoleFixture(t, roster, strings.Join(rows, "\n"))
	writeRoleFixture(t, f.brief, "Type: review\nWorktree: "+tree+"\nReview this slice.\n")
	code, out, msg := f.run(t, "worker", f.brief, "--role", "reviewer", "--no-wait")
	if code != 0 {
		t.Fatalf("rc=%d out=%s err=%s", code, out, msg)
	}
	composed, _ := dispatchOutputField(t, out, "composed_prompt")
	if !strings.HasSuffix(composed, ".brief.md") {
		t.Fatalf("scratch route not exercised: %s", composed)
	}
	side := dispatch.DispatchSidecar(composed)
	got := authorString(authorObject(side), "author_model")
	t.Logf("state author=%s review worker cwd=%s composed=%s sidecar=%s", base+".dispatch.json", tree, composed, mustRead(t, side))
	if got != "state-author" {
		t.Fatalf("scratch review author_model=%q; want existing matching state author", got)
	}
}

// State pointers, including a retired task's history, authorize external
// sidecars. An unreferenced sibling is deliberately newer and must not win.
func TestDispatchAuthorScratchPointerHistory(t *testing.T) {
	for _, mode := range []string{"current", "history", "unreferenced"} {
		t.Run(mode, func(t *testing.T) {
			f := roleDispatchFixture(t)
			tree := filepath.Join(f.root, "slice")
			if err := os.Mkdir(tree, 0700); err != nil {
				t.Fatal(err)
			}
			scratch := filepath.Join(f.root, "external-reports")
			if err := os.Mkdir(scratch, 0700); err != nil {
				t.Fatal(err)
			}
			authorReport := filepath.Join(scratch, "author-20200101T100000.md")
			authorSide := strings.TrimSuffix(authorReport, ".md") + ".dispatch.json"
			body := "You are running as the `implementer` role\nWorktree: " + tree + "\nType: backend\n"
			writeRoleFixture(t, strings.TrimSuffix(authorReport, ".md")+".brief.md", body)
			if err := writeDispatchSidecarWithTask(authorSide, "pi", "pointer-author", "high", filepath.Join(f.state, "ws", "reports", "author.current.md"), "hash", "accepted", "", "session", "implementer"); err != nil {
				t.Fatal(err)
			}
			// Sidecar exists without a report: lookup reads the pointed task's
			// composed brief and sidecar, never the report contents.
			current := authorReport
			history := []any{}
			if mode == "history" {
				current = ""
				history = append(history, authorReport)
			}
			if mode != "unreferenced" {
				if err := taskreport.WriteTaskReportPointer(filepath.Join(f.state, "ws"), "author", jsonjs.O("version", 1, "task_report", filepath.Join(f.state, "ws", "reports", "author.current.md"), "current", current, "history", history)); err != nil {
					t.Fatal(err)
				}
			}
			decoy := filepath.Join(scratch, "decoy-20200102T100000")
			writeRoleFixture(t, decoy+".brief.md", body)
			if err := writeDispatchSidecarWithTask(decoy+".dispatch.json", "codex", "unindexed-decoy", "high", "task", "hash", "accepted", "", "session", "implementer"); err != nil {
				t.Fatal(err)
			}
			// Exercise a scratch-routed reviewer as in the reviewer's F5 producer.
			roster := filepath.Join(f.state, "ws", "agents.tsv")
			rows := strings.Split(mustRead(t, roster), "\n")
			for i, line := range rows {
				if strings.HasPrefix(line, "worker\t") {
					cols := strings.Split(line, "\t")
					cols[6] = tree
					rows[i] = strings.Join(cols, "\t")
				}
			}
			writeRoleFixture(t, roster, strings.Join(rows, "\n"))
			writeRoleFixture(t, f.brief, "Type: review\nWorktree: "+tree+"\nReview the slice.\n")
			code, out, msg := f.run(t, "worker", f.brief, "--role", "reviewer", "--no-wait")
			if code != 0 {
				t.Fatalf("rc=%d out=%s err=%s", code, out, msg)
			}
			composed, _ := dispatchOutputField(t, out, "composed_prompt")
			if !strings.HasSuffix(composed, ".brief.md") {
				t.Fatalf("scratch route not exercised: %s", composed)
			}
			side := dispatch.DispatchSidecar(composed)
			got := authorString(authorObject(side), "author_model")
			want := "pointer-author"
			if mode == "unreferenced" {
				want = ""
			}
			if got != want {
				t.Fatalf("mode=%s got=%q want=%q sidecar=%s", mode, got, want, mustRead(t, side))
			}
			t.Logf("mode=%s author_model=%q; unreferenced external sibling excluded", mode, got)
			if got != "" {
				if err := writeDispatchSidecarWithTask(authorSide, "pi", "changed-after-freeze", "high", "task", "hash", "accepted", "", "session", "implementer"); err != nil {
					t.Fatal(err)
				}
				markNotReceived("worker", composed, filepath.Join(f.state, "ws"), side, "codex", "reviewer", "high", "task", "session", "hash", f.env, false)
				if after := authorString(authorObject(side), "author_model"); after != want {
					t.Fatalf("arrival changed frozen author: %q", after)
				}
			}
		})
	}
}
