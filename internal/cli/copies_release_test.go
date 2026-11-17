package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

func TestCopiesReleaseRejectsAnotherOwnersNestedCopy(t *testing.T) {
	f := newCopiesFixture(t, nil)
	parent := filepath.Join(f.tmp, "parent")
	child := filepath.Join(parent, "child")
	if err := os.MkdirAll(child, 0700); err != nil {
		t.Fatal(err)
	}
	f.writeCopies(t, []core.CopyRow{
		{Path: parent, Owner: "worker", Pane: "p-worker", Created: "2026-01-01T00:00:00Z", Source: "-", Origin: "add"},
		{Path: child, Owner: "other", Pane: "p-other", Created: "2026-01-01T00:00:00Z", Source: "-", Origin: "add"},
	})
	f.env["SOHO_AGENT"] = "worker"
	code, _, stderr := f.run(t, f.env, "copies", "release", parent)
	if code != 2 || !strings.Contains(stderr, "contains a copy owned by another agent") || len(f.copiesRows(t)) != 2 {
		t.Fatalf("code=%d stderr=%q rows=%v", code, stderr, f.copiesRows(t))
	}
	if _, err := os.Stat(child); err != nil {
		t.Fatalf("nested copy missing: %v", err)
	}
}

func TestCopiesRelease(t *testing.T) {
	for _, tc := range []struct {
		name, caller        string
		registered, missing bool
		code                int
		removed             bool
		note                string
	}{
		{"owner", "worker", true, false, 0, true, "removed "},
		{"orchestrator", "orchestrator", true, false, 2, false, "soho release <agent>"},
		{"no agent", "", true, false, 2, false, "soho release <agent>"},
		{"other agent", "other", true, false, 2, false, "owned by another agent"},
		{"unverified orchestrator suffix", "orchestrator-2", true, false, 2, false, "owned by another agent"},
		{"unregistered", "worker", false, false, 2, false, "not registered"},
		{"missing", "worker", true, true, 0, true, "already gone from disk"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCopiesFixture(t, nil)
			path := filepath.Join(f.tmp, "copy")
			if !tc.missing {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(path, "data"), []byte("hello"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.registered {
				f.writeCopies(t, []core.CopyRow{{Path: path, Owner: "worker", Pane: "p-worker", Created: "2026-01-01T00:00:00Z", Source: "-", Origin: "add"}})
			}
			f.env["SOHO_AGENT"] = tc.caller
			code, out, stderr := f.run(t, f.env, "copies", "release", path)
			if code != tc.code || !strings.Contains(out+stderr, tc.note) {
				t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
			}
			rows := f.copiesRows(t)
			if (len(rows) == 0) != tc.removed && tc.registered {
				t.Fatalf("rows=%+v", rows)
			}
			_, err := os.Stat(path)
			if !tc.missing && (os.IsNotExist(err) != tc.removed) {
				t.Fatalf("stat err=%v removed=%v", err, tc.removed)
			}
			if tc.removed && !tc.missing && !strings.Contains(out, "5 B") {
				t.Fatalf("size absent: %q", out)
			}
		})
	}
}

func TestCopiesReleaseNestedAndOverlapping(t *testing.T) {
	for _, tc := range []struct {
		name string
		args func(string, string) []string
	}{
		{"implicit descendant", func(p, c string) []string { return []string{p} }},
		{"child before parent and duplicates", func(p, c string) []string { return []string{c, p, c, p} }},
		{"parent before child", func(p, c string) []string { return []string{p, c} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCopiesFixture(t, nil)
			parent := filepath.Join(f.tmp, "parent")
			child := filepath.Join(parent, "child")
			if err := os.MkdirAll(child, 0700); err != nil {
				t.Fatal(err)
			}
			rows := []core.CopyRow{{Path: parent, Owner: "worker", Pane: "p", Created: "2026-01-01T00:00:00Z", Source: "-", Origin: "add"}, {Path: child, Owner: "worker", Pane: "p", Created: "2026-01-01T00:00:00Z", Source: "-", Origin: "add"}}
			f.writeCopies(t, rows)
			f.env["SOHO_AGENT"] = "worker"
			originalDrop := copiesReleaseDrop
			t.Cleanup(func() { copiesReleaseDrop = originalDrop })
			writes := 0
			copiesReleaseDrop = func(sd string, removed map[string]bool) error {
				writes++
				if !removed[parent] || !removed[child] || len(removed) != 2 {
					t.Errorf("expected parent and child in the same write: %v", removed)
				}
				return core.DropCopiesLinesHeld(sd, removed)
			}
			argv := append([]string{"copies", "release"}, tc.args(parent, child)...)
			code, out, stderr := f.run(t, f.env, argv...)
			if code != 0 || writes != 1 || len(f.copiesRows(t)) != 0 || strings.Count(out, "removed "+child+" (with "+parent+")") != 1 {
				t.Fatalf("code=%d out=%q stderr=%q rows=%v", code, out, stderr, f.copiesRows(t))
			}
			if _, err := os.Stat(parent); !os.IsNotExist(err) {
				t.Fatalf("parent remains: %v", err)
			}
		})
	}
}

func TestCopiesReleaseRegistryFailureRestoresCopy(t *testing.T) {
	f := newCopiesFixture(t, nil)
	path := filepath.Join(f.tmp, "copy")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	marker := originMarkerPath(path)
	if err := os.WriteFile(marker, []byte("marker"), 0600); err != nil {
		t.Fatal(err)
	}
	f.writeCopies(t, []core.CopyRow{{Path: path, Owner: "worker", Pane: "p", Created: "2026-01-01T00:00:00Z", Source: "-", Origin: "add"}})
	f.env["SOHO_AGENT"] = "worker"
	original := copiesReleaseDrop
	t.Cleanup(func() { copiesReleaseDrop = original })
	called := false
	copiesReleaseDrop = func(sd string, removed map[string]bool) error {
		called = true
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("drop before rename: %v", err)
		}
		if !removed[path] {
			t.Errorf("missing row in drop: %v", removed)
		}
		return fmt.Errorf("injected registry write failure")
	}
	code, out, stderr := f.run(t, f.env, "copies", "release", path)
	if !called || code != 4 || !strings.Contains(stderr, "injected registry write failure") || !strings.Contains(out, "restored") || len(f.copiesRows(t)) != 1 {
		t.Fatalf("called=%v code=%d out=%q stderr=%q rows=%v", called, code, out, stderr, f.copiesRows(t))
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("copy not restored: %v", err)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "marker" {
		t.Fatalf("marker=%q err=%v", data, err)
	}
}

func TestCopiesReleaseStateRefusalBeforeRename(t *testing.T) {
	for _, mode := range []string{"unwritable lock", "unreadable registry"} {
		t.Run(mode, func(t *testing.T) {
			f := newCopiesFixture(t, nil)
			path := filepath.Join(f.tmp, "copy")
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
			f.writeCopies(t, []core.CopyRow{{Path: path, Owner: "worker", Pane: "p", Created: "2026-01-01T00:00:00Z", Source: "-", Origin: "add"}})
			f.env["SOHO_AGENT"] = "worker"
			oldLock, oldRead := copiesReleaseLock, copiesReleaseRead
			t.Cleanup(func() { copiesReleaseLock = oldLock; copiesReleaseRead = oldRead })
			if mode == "unwritable lock" {
				copiesReleaseLock = func(string) (func(), error) { return nil, os.ErrPermission }
			} else {
				copiesReleaseRead = func(string) ([]core.CopyRow, error) { return nil, os.ErrPermission }
			}
			code, _, stderr := f.run(t, f.env, "copies", "release", path)
			if code != 4 || !strings.Contains(stderr, "permission denied") || len(f.copiesRows(t)) != 1 {
				t.Fatalf("code=%d stderr=%q rows=%v", code, stderr, f.copiesRows(t))
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("copy moved: %v", err)
			}
		})
	}
}

func TestCopiesReleaseQueuedChild(t *testing.T) {
	f := newCopiesFixture(t, nil)
	parent := filepath.Join(f.tmp, "parent")
	child := filepath.Join(parent, "child")
	if err := os.MkdirAll(child, 0700); err != nil {
		t.Fatal(err)
	}
	f.writeCopies(t, []core.CopyRow{{Path: parent, Owner: "worker", Pane: "p", Created: "2026-01-01T00:00:00Z", Source: "-", Origin: "add"}})
	f.env["SOHO_AGENT"] = "worker"
	if _, err := core.QueueCopy(f.env, "ws", f.stateDir(t), core.CopyRow{Path: child, Owner: "other", Pane: "p", Created: "2026-01-01T00:00:00Z", Source: "-", Origin: "add", Task: "-", Report: "-"}); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := f.run(t, f.env, "copies", "release", parent)
	if code != 2 || !strings.Contains(stderr, "contains a copy owned by another agent") || len(f.copiesRows(t)) != 2 {
		t.Fatalf("code=%d stderr=%q rows=%v", code, stderr, f.copiesRows(t))
	}
	if _, err := os.Stat(child); err != nil {
		t.Fatalf("child missing: %v", err)
	}
}

func TestCopiesReleaseIncorporationFailure(t *testing.T) {
	f := newCopiesFixture(t, nil)
	path := filepath.Join(f.tmp, "copy")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	f.writeCopies(t, []core.CopyRow{{Path: path, Owner: "worker", Pane: "p", Created: "2026-01-01T00:00:00Z", Source: "-", Origin: "add"}})
	f.env["SOHO_AGENT"] = "worker"
	old := incorporateCopies
	t.Cleanup(func() { incorporateCopies = old })
	incorporateCopies = func(string, string, platform.Env, func(core.CopyRow) (core.CopyRow, string)) (core.PendingResult, error) {
		return core.PendingResult{}, fmt.Errorf("queue failure")
	}
	code, _, stderr := f.run(t, f.env, "copies", "release", path)
	if code != 4 || !strings.Contains(stderr, "queue failure") || len(f.copiesRows(t)) != 1 {
		t.Fatalf("code=%d stderr=%q rows=%v", code, stderr, f.copiesRows(t))
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestCopiesReleaseOwnerless(t *testing.T) {
	for _, owner := range []string{"-", ""} {
		t.Run(fmt.Sprintf("owner_%q", owner), func(t *testing.T) {
			f := newCopiesFixture(t, nil)
			path := filepath.Join(f.tmp, "copy")
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
			f.writeCopies(t, []core.CopyRow{{Path: path, Owner: owner, Pane: "p", Created: "2026-01-01T00:00:00Z", Source: "-", Origin: "add"}})
			f.env["SOHO_AGENT"] = "-"
			code, _, stderr := f.run(t, f.env, "copies", "release", path)
			if code != 2 || !strings.Contains(stderr, "soho gc") || len(f.copiesRows(t)) != 1 {
				t.Fatalf("code=%d stderr=%q rows=%v", code, stderr, f.copiesRows(t))
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCopiesReleasePendingRecheckFailure(t *testing.T) {
	f := newCopiesFixture(t, nil)
	path := filepath.Join(f.tmp, "copy")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	f.writeCopies(t, []core.CopyRow{{Path: path, Owner: "worker", Pane: "p", Created: "2026-01-01T00:00:00Z", Source: "-", Origin: "add"}})
	f.env["SOHO_AGENT"] = "worker"
	old := copiesReleasePending
	t.Cleanup(func() { copiesReleasePending = old })
	copiesReleasePending = func(string, string, platform.Env) ([]core.CopyRow, error) { return nil, os.ErrPermission }
	code, _, stderr := f.run(t, f.env, "copies", "release", path)
	if code != 4 || !strings.Contains(stderr, "permission denied") || len(f.copiesRows(t)) != 1 {
		t.Fatalf("code=%d stderr=%q rows=%v", code, stderr, f.copiesRows(t))
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestCopiesReleaseMalformedMixedQueue(t *testing.T) {
	f := newCopiesFixture(t, nil)
	path := filepath.Join(f.tmp, "copy")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	f.writeCopies(t, []core.CopyRow{{Path: path, Owner: "worker", Pane: "p", Created: "2026-01-01T00:00:00Z", Source: "-", Origin: "add"}})
	f.env["SOHO_AGENT"] = "worker"
	queued := filepath.Join(f.tmp, "queued")
	if err := os.Mkdir(queued, 0700); err != nil {
		t.Fatal(err)
	}
	queue, err := core.QueueCopy(f.env, "ws", f.stateDir(t), core.CopyRow{Path: queued, Owner: "worker", Pane: "p", Created: "2026-01-01T00:00:00Z", Source: "-", Origin: "add"})
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(queue, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("malformed\n"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := f.run(t, f.env, "copies", "release", path)
	if code != 4 {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
