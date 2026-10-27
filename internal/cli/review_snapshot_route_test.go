package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReview545DefaultStatePublicCommand(t *testing.T) {
	f := newSnapshotFixture(t)
	delete(f.env, "SOHO_DIR")
	delete(f.env, "SOHO_STATE_DIR")
	var raised any
	code := 0
	out, msg := "", ""
	func() {
		defer func() { raised = recover() }()
		code, out, msg = f.run(t, "review-snapshot", f.src, "--name", "default-state")
	}()
	t.Logf("default state public command panic=%v exit=%d stdout=%s stderr=%s", raised, code, out, msg)
	if raised != nil {
		t.Fatalf("review-snapshot panicked with the default state settings: %v", raised)
	}
	if code != 0 {
		t.Fatalf("default state command exit=%d", code)
	}
	var obj map[string]string
	if e := json.Unmarshal([]byte(out), &obj); e != nil {
		t.Fatal(e)
	}
	expected := filepath.Join(f.source, ".soho", "ws", "snapshots")
	if filepath.Dir(obj["patch"]) != expected {
		t.Fatalf("patch root=%q want %q", filepath.Dir(obj["patch"]), expected)
	}
	rows, e := readReviewSnapshots(filepath.Dir(expected))
	if e != nil || len(rows) != 1 {
		t.Fatalf("default registry=%v err=%v", rows, e)
	}
	for _, p := range []string{obj["snapshot"], obj["patch"], obj["patch"] + ".sha256"} {
		if _, e := os.Lstat(p); e != nil {
			t.Fatal(e)
		}
	}
	t.Log("default .soho/ws registry, detached worktree, patch and hash exist")
}

func TestReview545WindowsMissingRegistered(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("requires native Windows Git; cross-target vet is not execution")
	}
	f := newSnapshotFixture(t)
	obj := f.freeze(t, "worker")
	if e := os.Rename(obj["snapshot"], obj["snapshot"]+"-held"); e != nil {
		t.Fatal(e)
	}
	listing := fixtureGit(t, f.mutationCopyFixture, f.source, "worktree", "list", "--porcelain", "-z")
	t.Logf("registry_native=%q git_listing=%q", obj["snapshot"], listing)
	if !strings.Contains(listing, "worktree "+filepath.ToSlash(obj["snapshot"])+"\x00") {
		t.Fatal("Git producer did not emit the expected registered path")
	}
	if e := os.WriteFile(filepath.Join(f.sd, "agents.tsv"), []byte("# no owners\n"), 0600); e != nil {
		t.Fatal(e)
	}
	code, out, msg := f.run(t, "gc", "--yes")
	rows, e := readReviewSnapshots(f.sd)
	t.Logf("native Windows gc exit=%d rows=%d stdout=%s stderr=%s", code, len(rows), out, msg)
	if code != 0 || e != nil || len(rows) != 1 || !strings.Contains(msg, "missing snapshot worktree is still registered") {
		t.Error("Git slash spelling bypassed registered-worktree refusal")
	}
	for _, suffix := range []string{"", ".sha256"} {
		if _, e := os.Lstat(obj["patch"] + suffix); e != nil {
			t.Error("registered snapshot artifact removed", e)
		}
	}
}

func TestReview545PorcelainPaths(t *testing.T) {
	for _, tc := range []struct {
		name, a, b    string
		windows, want bool
	}{
		{"native", "/tmp/snap", "/tmp/./snap", false, true},
		{"windows separators", "C:/repo/.worktrees/snap-a", `C:\repo\.worktrees\snap-a`, true, true},
		{"windows case", "C:/Repo/Snap", "c:/repo/snap", true, true},
		{"different leaf", "C:/repo/snap-a", "C:/repo/snap-b", true, false},
		{"different root", "C:/repo/snap-a", "D:/repo/snap-a", true, false},
		{"unix case", "/repo/Snap", "/repo/snap", false, false},
		{"unix literal backslash", `/repo/snap\a`, "/repo/snap/a", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := snapshotPathsEqual(tc.a, tc.b, tc.windows); got != tc.want {
				t.Fatalf("a=%q b=%q windows=%t got=%t want=%t", tc.a, tc.b, tc.windows, got, tc.want)
			}
		})
	}
}
