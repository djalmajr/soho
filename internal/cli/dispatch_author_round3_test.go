package cli

import (
	"github.com/djalmajr/soho/internal/dispatch"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/taskreport"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReview539ReportIsNotBrief(t *testing.T) {
	for _, mode := range []string{"composed-present", "composed-absent"} {
		t.Run(mode, func(t *testing.T) {
			f := roleDispatchFixture(t)
			sd := filepath.Join(f.state, "ws")
			scratch := filepath.Join(f.root, "external-reports")
			if e := os.Mkdir(scratch, 0700); e != nil {
				t.Fatal(e)
			}
			p := filepath.Join(scratch, "author-20200101T100000.md")
			body := "Worktree: " + filepath.Join(f.root, "slice") + "\nType: backend\n"
			writeRoleFixture(t, p, "# Implementation report\n"+body+"[done] implementation evidence\n")
			if mode == "composed-present" {
				writeRoleFixture(t, strings.TrimSuffix(p, ".md")+".brief.md", "You are running as the `implementer` role\n"+body)
			}
			side := strings.TrimSuffix(p, ".md") + ".dispatch.json"
			if e := writeDispatchSidecarWithTask(side, "pi", "pointed-model", "high", "task", "hash", "accepted", "", "session", "implementer"); e != nil {
				t.Fatal(e)
			}
			if e := taskreport.WriteTaskReportPointer(sd, "author", jsonjs.O("version", 1, "task_report", filepath.Join(sd, "reports", "author.current.md"), "current", p, "history", []any{})); e != nil {
				t.Fatal(e)
			}
			writeRoleFixture(t, f.brief, "Type: review\nWorktree: "+filepath.Join(f.root, "slice")+"\nReview it.\n")
			code, out, msg := f.run(t, "worker", f.brief, "--role", "reviewer", "--no-wait")
			if code != 0 {
				t.Fatalf("code=%d out=%s err=%s", code, out, msg)
			}
			composed, _ := dispatchOutputField(t, out, "composed_prompt")
			got := authorString(authorObject(dispatch.DispatchSidecar(composed)), "author_model")
			t.Logf("mode=%s exit=%d author_model=%q pointed_report=%s", mode, code, got, p)
			want := "pointed-model"
			if mode == "composed-absent" {
				want = ""
			}
			if got != want {
				t.Fatalf("report body used as composed brief: got author %q want %q", got, want)
			}
		})
	}
}
func TestReview539OldWrapperFreshReview(t *testing.T) {
	for _, mode := range []string{"context", "old-wrapper"} {
		t.Run(mode, func(t *testing.T) {
			f := roleDispatchFixture(t)
			sd := filepath.Join(f.state, "ws")
			dir := filepath.Join(sd, "briefs")
			a := filepath.Join(dir, "author-20200101T100000")
			writeRoleFixture(t, a+".md", "You are running as the `implementer` role\nWorktree: "+filepath.Join(f.root, "slice")+"\nType: backend\n")
			if e := writeDispatchSidecarWithTask(a+".dispatch.json", "pi", "author", "high", "task", "hash", "accepted", "", "session", "implementer"); e != nil {
				t.Fatal(e)
			}
			writeRoleFixture(t, f.brief, "Type: review\nWorktree: "+filepath.Join(f.root, "slice")+"\nReview it.\n")
			code, out, msg := f.run(t, "worker", f.brief, "--role", "reviewer", "--no-wait")
			if code != 0 {
				t.Fatalf("code=%d out=%s err=%s", code, out, msg)
			}
			composed, _ := dispatchOutputField(t, out, "composed_prompt")
			if got := authorString(authorObject(strings.TrimSuffix(composed, ".md")+".dispatch.json"), "author_model"); got != "author" {
				t.Fatalf("initial producer failed: %q", got)
			}
			p := filepath.Join(dir, "review-again-20990101T100000.dispatch.json")
			writeRoleFixture(t, strings.TrimSuffix(p, ".dispatch.json")+".md", "Worktree: "+filepath.Join(f.root, "slice")+"\n")
			defer func() {
				if p := recover(); p != nil {
					t.Errorf("mode=%s retained wrapper panicked while classifying existing reviewer history: %v", mode, p)
				}
			}()
			var err error
			if mode == "context" {
				err = writeDispatchSidecarWithAuthorContext(dispatchAuthorContext{stateDir: sd, env: f.env, cwd: f.root}, p, "codex", "reviewer", "high", "task", "hash", "accepted", "", "session", "reviewer")
			} else {
				err = writeDispatchSidecarWithTask(p, "codex", "reviewer", "high", "task", "hash", "accepted", "", "session", "reviewer")
			}
			if err != nil {
				t.Fatal(err)
			}
			if authorString(authorObject(p), "author_model") != "author" {
				t.Fatal("author lost")
			}
			t.Logf("mode=%s fresh review recorded expected author", mode)
		})
	}
}

func TestDispatchAuthorUnresolvedRoleStillWritesSidecar(t *testing.T) {
	for _, mode := range []string{"context", "old-wrapper"} {
		t.Run(mode, func(t *testing.T) {
			f := roleDispatchFixture(t)
			sd := filepath.Join(f.state, "ws")
			dir := filepath.Join(sd, "briefs")
			writeRoleFixture(t, filepath.Join(f.root, "roles", "custom-author.md"), "---\nname: Custom author\nmode: edit\n---\nEdit this project.\n")
			a := filepath.Join(dir, "author-20200101T100000")
			writeRoleFixture(t, a+".md", "Worktree: "+filepath.Join(f.root, "slice")+"\nType: backend\n")
			if err := writeDispatchSidecarWithTask(a+".dispatch.json", "pi", "custom-model", "high", "task", "hash", "accepted", "", "session", "custom-author"); err != nil {
				t.Fatal(err)
			}
			p := filepath.Join(dir, "review-20990101T100000.dispatch.json")
			writeRoleFixture(t, strings.TrimSuffix(p, ".dispatch.json")+".md", "Worktree: "+filepath.Join(f.root, "slice")+"\n")
			var err error
			if mode == "context" {
				err = writeDispatchSidecarWithAuthorContext(dispatchAuthorContext{stateDir: sd, env: f.env, cwd: f.root}, p, "codex", "reviewer", "high", "task", "hash", "accepted", "", "session", "reviewer")
			} else {
				err = writeDispatchSidecarWithTask(p, "codex", "reviewer", "high", "task", "hash", "accepted", "", "session", "reviewer")
			}
			if err != nil {
				t.Fatal(err)
			}
			o := authorObject(p)
			if o == nil {
				t.Fatal("sidecar not written")
			}
			for key, want := range map[string]string{"kind": "codex", "model": "reviewer", "task_report": "task", "brief_sha256": "hash", "submission": "accepted", "role": "reviewer"} {
				if got := authorString(o, key); got != want {
					t.Fatalf("%s=%q want %q", key, got, want)
				}
			}
			want := "custom-model"
			if mode == "old-wrapper" {
				want = ""
				for _, key := range authorFields {
					if _, ok := o.Get(key); ok {
						t.Fatalf("unresolved candidate supplied %s", key)
					}
				}
			}
			if got := authorString(o, "author_model"); got != want {
				t.Fatalf("author_model=%q want %q", got, want)
			}
			t.Logf("mode=%s sidecar written; author_model=%q", mode, want)
		})
	}
}
