package cli

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAudit539LatestThirdCollisionValidSidecars(t *testing.T) {
	f := roleDispatchFixture(t)
	dir := filepath.Join(f.state, "ws", "briefs")
	now := time.Unix(1700000000, 0)
	body := "You are running as the `implementer` role\nWorktree: " + filepath.Join(f.root, "slice") + "\nType: backend\n"
	for _, name := range []string{"author-20200101T100000", "author-20200101T100000-2", "author-20200101T100000-3"} {
		base := filepath.Join(dir, name)
		writeRoleFixture(t, base+".md", body)
		if e := os.Chtimes(base+".md", now, now); e != nil {
			t.Fatal(e)
		}
		h := sha256.Sum256([]byte(body))
		if e := writeDispatchSidecarWithTask(base+".dispatch.json", "pi", name, "high", filepath.Join(f.state, "ws", "reports", name+".current.md"), fmt.Sprintf("%x", h), "accepted", "", "session", "implementer"); e != nil {
			t.Fatal(e)
		}
	}
	writeRoleFixture(t, f.brief, "Type: review\nWorktree: "+filepath.Join(f.root, "slice")+"\nReview this slice.\n")
	code, out, msg := f.run(t, "worker", f.brief, "--role", "reviewer", "--no-wait")
	if code != 0 {
		t.Fatalf("rc=%d out=%s err=%s", code, out, msg)
	}
	composed, _ := dispatchOutputField(t, out, "composed_prompt")
	side := strings.TrimSuffix(composed, ".md") + ".dispatch.json"
	got := authorString(authorObject(side), "author_model")
	t.Logf("sidecars created by production writer; three same-second author tasks with equal brief mtime; selected=%q sidecar=%s", got, mustRead(t, side))
	if got != "author-20200101T100000-3" {
		t.Fatalf("selected %q; want latest recorded collision suffix -3", got)
	}
}

func TestDispatchAuthorOrder(t *testing.T) {
	for _, tc := range []struct {
		name, first, second string
		secondOffset        time.Duration
		want                string
	}{
		{"mtime-before-stamp", "author-20200103T100000", "author-20200101T100000", time.Second, "author-20200101T100000"},
		{"stamp-on-equal-mtime", "author-20200101T100000", "author-20200102T100000", 0, "author-20200102T100000"},
		{"numeric-suffix-on-equal-mtime", "author-20200101T100000-2", "author-20200101T100000-10", 0, "author-20200101T100000-10"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := roleDispatchFixture(t)
			dir := filepath.Join(f.state, "ws", "briefs")
			baseTime := time.Unix(1700000000, 0)
			body := "You are running as the `implementer` role\nWorktree: " + filepath.Join(f.root, "slice") + "\nType: backend\n"
			for i, name := range []string{tc.first, tc.second} {
				base := filepath.Join(dir, name)
				writeRoleFixture(t, base+".md", body)
				mtime := baseTime
				if i == 1 {
					mtime = mtime.Add(tc.secondOffset)
				}
				if err := os.Chtimes(base+".md", mtime, mtime); err != nil {
					t.Fatal(err)
				}
				if err := writeDispatchSidecarWithTask(base+".dispatch.json", "pi", name, "high", "task", "hash", "accepted", "", "session", "implementer"); err != nil {
					t.Fatal(err)
				}
			}
			writeRoleFixture(t, f.brief, "Type: review\nWorktree: "+filepath.Join(f.root, "slice")+"\nReview this slice.\n")
			code, out, msg := f.run(t, "worker", f.brief, "--role", "reviewer", "--no-wait")
			if code != 0 {
				t.Fatalf("rc=%d out=%s err=%s", code, out, msg)
			}
			composed, _ := dispatchOutputField(t, out, "composed_prompt")
			got := authorString(authorObject(strings.TrimSuffix(composed, ".md")+".dispatch.json"), "author_model")
			if got != tc.want {
				t.Fatalf("order=%s author=%q want=%q", tc.name, got, tc.want)
			}
			t.Logf("order=%s author=%q", tc.name, got)
		})
	}
}
