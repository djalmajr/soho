package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDispatchAuthor(t *testing.T) {
	for _, mode := range []string{"match", "newest", "no-match", "no-worktree", "failed-newer", "different-newer", "legacy-role"} {
		t.Run(mode, func(t *testing.T) {
			f := roleDispatchFixture(t)
			tree := filepath.Join(f.root, "slice")
			dir := filepath.Join(f.state, "ws", "briefs")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			write := func(name, stamp, role, kind, model, submission, worktree string) {
				base := filepath.Join(dir, name+"-"+stamp)
				writeRoleFixture(t, base+".md", fmt.Sprintf("You are running as the `%s` role\nWorktree: %s\nType: backend\n", role, worktree))
				roleField := fmt.Sprintf(",\"role\":%q", role)
				if mode == "legacy-role" {
					roleField = ""
				}
				writeRoleFixture(t, base+".dispatch.json", fmt.Sprintf(`{"version":1,"submission":%q,"kind":%q,"model":%q,"effort":"","family":"qwen"%s}`, submission, kind, model, roleField))
			}
			if mode != "no-match" {
				write("author", "20200101T100000", "implementer", "pi", "qwen-author", "accepted", tree)
			}
			if mode == "failed-newer" {
				write("bad", "20200102T100000", "implementer", "codex", "wrong", "failed", tree)
			}
			if mode == "different-newer" {
				write("other", "20200102T100000", "implementer", "codex", "wrong", "accepted", tree+"-other")
			}
			if mode == "newest" {
				write("latest", "20200103T100000", "tasker", "pi", "qwen-latest", "accepted", tree)
			}
			body := "Type: review\nReview the slice.\n"
			if mode != "no-worktree" {
				body += "Worktree: " + tree + "\n"
			}
			writeRoleFixture(t, f.brief, body)
			code, out, msg := f.run(t, "worker", f.brief, "--role", "reviewer", "--no-wait")
			if code != 0 {
				t.Fatalf("dispatch rc=%d out=%s err=%s", code, out, msg)
			}
			composed, _ := dispatchOutputField(t, out, "composed_prompt")
			o := authorObject(strings.TrimSuffix(composed, ".md") + ".dispatch.json")
			if o == nil {
				t.Fatal("missing sidecar")
			}
			if mode == "no-match" || mode == "no-worktree" {
				for _, key := range authorFields {
					if _, ok := o.Get(key); ok {
						t.Fatalf("unexpected %s", key)
					}
				}
				return
			}
			wants := map[string]string{"author_agent": "author", "author_kind": "pi", "author_model": "qwen-author", "author_family": "qwen", "author_type": "backend"}
			if mode == "newest" {
				wants["author_agent"], wants["author_model"] = "latest", "qwen-latest"
			}
			for key, want := range wants {
				if got := authorString(o, key); got != want {
					t.Fatalf("%s=%q want %q", key, got, want)
				}
			}
			// Arrival updates must retain the dispatched author's identity even
			// after another accepted edit task appears for that worktree.
			write("later", "20210101T100000", "implementer", "codex", "wrong-later", "accepted", tree)
			side := strings.TrimSuffix(composed, ".md") + ".dispatch.json"
			if err := writeDispatchSidecarWithTask(side, "claude", "reviewer-model", "", "task", "hash", "accepted", "not-received", "session", "reviewer"); err != nil {
				t.Fatal(err)
			}
			updated := authorObject(side)
			for key, want := range wants {
				if got := authorString(updated, key); got != want {
					t.Fatalf("arrival changed %s=%q want %q", key, got, want)
				}
			}
			t.Log("accepted review records author pi/qwen-author, family qwen, type backend")
		})
	}
}

func TestDispatchAuthorSameSecond(t *testing.T) {
	tree := t.TempDir()
	dir := filepath.Join(t.TempDir(), "briefs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0)
	for i, name := range []string{"first", "last", "review"} {
		base := filepath.Join(dir, name+"-20200101T100000")
		writeRoleFixture(t, base+".md", "Worktree: "+tree+"\nType: backend\n")
		if err := os.Chtimes(base+".md", now.Add(time.Duration(i)*time.Second), now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
		if name != "review" {
			writeRoleFixture(t, base+".dispatch.json", fmt.Sprintf(`{"version":1,"submission":"accepted","kind":"pi","model":%q,"role":"implementer"}`, name))
		}
	}
	got := lookupDispatchAuthor(filepath.Join(dir, "review-20200101T100000.dispatch.json"))
	if got["author_model"] != "last" {
		t.Fatalf("same-second attribution: %v", got)
	}
}
