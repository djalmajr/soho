package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestCopiesReleaseSuffixedOrchestrator(t *testing.T) {
	f := newCopiesFixture(t, nil)
	dir := t.TempDir()
	response := fmt.Sprintf(`{"result":{"agent":{"name":"orchestrator-2","pane_id":"ws:p0","workspace_id":"ws","cwd":%q}}}`, f.repo)
	if _, err := fakecli.Install(t, dir, "herdr", []fakecli.Rule{{Argv: []string{"agent", "get", "ws:p0"}, Stdout: response}}); err != nil {
		t.Fatal(err)
	}
	f.env["SOHO_FAKECLI_CONFIG"] = dir
	f.env["PATH"] = dir + string(os.PathListSeparator) + f.env.Get("PATH")
	f.env["HERDR_PANE_ID"] = "ws:p0"
	f.env["SOHO_AGENT"] = "orchestrator-2"
	path := filepath.Join(f.tmp, "copy")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	f.writeCopies(t, []core.CopyRow{{Path: path, Owner: "worker", Pane: "p-worker", Created: "2026-01-01T00:00:00Z", Source: "-", Origin: "add"}})
	code, out, stderr := f.run(t, f.env, "copies", "release", path)
	if code != 0 || !strings.Contains(out, "removed "+path) || len(f.copiesRows(t)) != 0 {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("copy remains: %v", err)
	}
}

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
		{"orchestrator", "orchestrator", true, false, 0, true, "removed "},
		{"no agent", "", true, false, 0, true, "removed "},
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
