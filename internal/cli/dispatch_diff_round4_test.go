package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReview543AmendmentRetainsOwned(t *testing.T) {
	for _, name := range []string{"inherits", "explicit-control", "replacement-control"} {
		t.Run(name, func(t *testing.T) {
			f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
			f.env["PATH"] += string(os.PathListSeparator) + waitingGitOnlyDir(t)
			f.env["SOHO_SETTLED_GRACE"], f.env["SOHO_WAIT_POLL_MS"] = "0", "1"
			f.env["SOHO_METRICS"] = "off"
			_ = headInit(t, f.env, f.root)
			reopenDispatchWrite(t, f.brief, "# Goal\nImplement.\n# Owned files\n- `src/`\n")
			composed, _ := firstDispatch(t, f)
			before, e := os.Stat(composed)
			if e != nil {
				t.Fatal(e)
			}
			body, e := os.ReadFile(composed)
			if e != nil {
				t.Fatal(e)
			}
			amendment := filepath.Join(f.root, "amendment.md")
			text := "Please check the result again.\n"
			if name == "explicit-control" {
				text += "# Owned files\n- `src/`\n"
			} else if name == "replacement-control" {
				text += "# Owned files\n- `outside.txt`\n- `amendment.md`\n"
			}
			reopenDispatchWrite(t, amendment, text)
			code, out, errText := f.run(t, "worker", amendment, "--amend", "--no-wait")
			if code != 0 {
				t.Fatalf("amend code=%d out=%s stderr=%s", code, out, errText)
			}
			current, _ := dispatchOutputField(t, out, "report")
			file := filepath.Join(f.root, "outside.txt")
			reopenDispatchWrite(t, file, "outside\n")
			cutoff := before.ModTime().Add(time.Second)
			if e := os.Chtimes(file, cutoff, cutoff); e != nil {
				t.Fatal(e)
			}
			reopenDispatchWrite(t, current, "Completed.\n")
			reportAfterFixtureEdits(t, current, cutoff)
			after, e := os.Stat(composed)
			if e != nil {
				t.Fatal(e)
			}
			afterbody, e := os.ReadFile(composed)
			if e != nil {
				t.Fatal(e)
			}
			if !before.ModTime().Equal(after.ModTime()) || string(body) != string(afterbody) {
				t.Fatal("original brief mutated")
			}
			code, out, errText = reopenDispatchRun(t, f.env, "wait", "worker", "--timeout", "2s")
			t.Logf("original_mtime_stable=true wait code=%d out=%s stderr=%s", code, out, errText)
			if code != 0 || !strings.Contains(out, "diff_stat") {
				t.Fatal("audit not reached")
			}
			if name == "replacement-control" {
				if strings.Contains(out, "outside_owned") || strings.Contains(errText, "outside its Owned files:") {
					t.Fatal("explicit amendment scope did not replace the original")
				}
				return
			}
			if !strings.Contains(out, "outside.txt") || (!strings.Contains(errText, "outside its Owned files:") || !strings.Contains(errText, "outside.txt")) {
				t.Fatal("amendment dropped the original task's Owned files scope")
			}
		})
	}
}

// Preserve the fixture's dispatch/edit cutoffs and timestamp only its final
// report, after both the real writes and any synthetic edit mtime.
func reportAfterFixtureEdits(t *testing.T, report string, editedAt time.Time) {
	t.Helper()
	info, err := os.Stat(report)
	if err != nil {
		t.Fatal(err)
	}
	if info.ModTime().After(editedAt) {
		editedAt = info.ModTime()
	}
	final := editedAt.Add(time.Second)
	if err := os.Chtimes(report, final, final); err != nil {
		t.Fatal(err)
	}
}

func TestReview543DiffMarkerLifetime(t *testing.T) {
	for _, kind := range []string{"git", "diff"} {
		t.Run(kind, func(t *testing.T) {
			f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
			dir := filepath.Join(f.state, "ws", "briefs")
			oldSide := filepath.Join(dir, "old.dispatch.json")
			newSide := filepath.Join(dir, "new.dispatch.json")
			ext := "." + kind + "-checked"
			for _, file := range []string{oldSide, oldSide + ext, newSide, newSide + ext} {
				reopenDispatchWrite(t, file, "{}\n")
			}
			aged := time.Now().Add(-30 * 24 * time.Hour)
			for _, file := range []string{oldSide, newSide + ext} {
				if e := os.Chtimes(file, aged, aged); e != nil {
					t.Fatal(e)
				}
			}
			code, out, msg := reopenDispatchRun(t, f.env, "clean", "--older-than", "7")
			t.Logf("kind=%s clean code=%d stdout=%s stderr=%s", kind, code, out, msg)
			if code != 0 {
				t.Fatal("clean failed")
			}
			for _, file := range []string{oldSide, oldSide + ext} {
				_, e := os.Stat(file)
				t.Logf("old pair %s stat=%v", filepath.Base(file), e)
				if !os.IsNotExist(e) {
					t.Errorf("removed sidecar's marker must not survive: %s error=%v", file, e)
				}
			}
			for _, file := range []string{newSide, newSide + ext} {
				_, e := os.Stat(file)
				t.Logf("retained pair %s stat=%v", filepath.Base(file), e)
				if e != nil {
					t.Errorf("marker must share its existing sidecar's lifetime: %s error=%v", file, e)
				}
			}
		})
	}
}
