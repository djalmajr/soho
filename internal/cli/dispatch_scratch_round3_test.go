package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReview538EmptyScratchMember(t *testing.T) {
	f, first := durableScratchDispatch(t, "codex", "full")
	report, composed := durableField(t, first, "report"), durableField(t, first, "composed_prompt")
	sd := filepath.Join(f.state, "ws")
	if err := os.WriteFile(report, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, withSidecar := range []bool{true, false} {
		if withSidecar {
			got, _, ok := memberTaskPaths(sd, report)
			if !ok || got != composed {
				t.Fatalf("memberTaskPaths=%s want=%s", got, composed)
			}
		} else {
			got, ok := memberComposedPath(sd, report)
			if !ok || got != composed {
				t.Fatalf("memberComposedPath=%s want=%s", got, composed)
			}
		}
	}
	code, out, msg := f.run(t, "worker", f.brief, "--no-wait")
	var obj map[string]any
	if err := json.Unmarshal([]byte(out), &obj); err != nil {
		t.Fatal(err)
	}
	if code != 0 || obj["composed_prompt"] != composed || obj["duplicate_of"] != composed {
		t.Fatalf("duplicate code=%d out=%s stderr=%s", code, out, msg)
	}
	// Removing only this fixture's brief must not promote its empty report.
	if err := os.Remove(composed); err != nil {
		t.Fatal(err)
	}
	if got, _, ok := memberTaskPaths(sd, report); ok || got != "" {
		t.Fatalf("report became composed: %s", got)
	}
	if got, ok := memberComposedPath(sd, report); ok || got != "" {
		t.Fatalf("report became composed without sidecar: %s", got)
	}
}

func TestReview538MirrorKeepsExistingDestination(t *testing.T) {
	for _, mode := range []string{"file", "empty", "settled"} {
		t.Run(mode, func(t *testing.T) {
			f, obj := durableScratchDispatch(t, "codex", "full")
			report := durableField(t, obj, "report")
			mirror := filepath.Join(f.state, "ws", "reports", filepath.Base(report))
			if err := os.WriteFile(report, []byte("original completed report\n"), 0600); err != nil {
				t.Fatal(err)
			}
			want := "operator report\n"
			if mode == "empty" {
				want = ""
			}
			if mode == "settled" {
				code, out, msg := runIn(t, []string{"status", "worker"}, f.env, f.root)
				if code != 0 {
					t.Fatalf("status=%d %s %s", code, out, msg)
				}
				want = "original completed report\n"
			} else {
				if err := os.MkdirAll(filepath.Dir(mirror), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(mirror, []byte(want), 0600); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.Stat(mirror)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(report, []byte("changed completed report\n"), 0600); err != nil {
				t.Fatal(err)
			}
			code, out, msg := runIn(t, []string{"wait", "worker", "--timeout", "1s"}, f.env, f.root)
			body, err := os.ReadFile(mirror)
			after, statErr := os.Stat(mirror)
			if code != 0 || err != nil || statErr != nil || string(body) != want || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) || !strings.Contains(msg, "kept "+mirror) {
				t.Fatalf("wait=%d out=%s stderr=%s mirror=%q err=%v stat=%v", code, out, msg, body, err, statErr)
			}
			if _, err := os.Lstat(mirror + ".mirror.sha256"); !os.IsNotExist(err) {
				t.Fatalf("digest record created: %v", err)
			}
		})
	}
}
