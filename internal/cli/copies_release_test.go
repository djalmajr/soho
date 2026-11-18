package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestCopiesCaseFoldedContainment(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("case folding applies on macOS; Windows requires native execution")
	}
	for _, mode := range []string{"copies", "release", "gc"} {
		t.Run(mode, func(t *testing.T) {
			f := newCopiesFixture(t, []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: agentIdleJSON}, {Argv: []string{"agent", "get", "other"}, Stdout: agentIdleJSON}, {Argv: []string{"pane", "report-metadata", "p-worker", "--source", "soho", "--clear-title"}}, {Argv: []string{"pane", "list", "--workspace", "ws"}, Stdout: `{"result":{"panes":[]}}`}})
			parent := filepath.Join(f.tmp, "Copy")
			child := filepath.Join(f.tmp, "copy", "child")
			if err := os.MkdirAll(filepath.Join(parent, "child"), 0700); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(child); err != nil {
				t.Skipf("temporary volume is case-sensitive: %s: %v", child, err)
			}
			owner := "other"
			if mode == "same-owner" {
				owner = "worker"
			}
			f.writeRoster(t, copiesRosterRow("worker", "p-worker"), copiesRosterRow("other", "p-other"))
			f.writeCopies(t, []core.CopyRow{{Path: parent, Owner: "worker", Pane: "p-worker", Created: "2020-01-01T00:00:00Z", Source: "-", Origin: "add"}, {Path: child, Owner: owner, Pane: "p-other", Created: "2020-01-01T00:00:00Z", Source: "-", Origin: "add"}})
			f.env["SOHO_AGENT"] = "worker"
			var code int
			var out, stderr string
			switch mode {
			case "copies", "same-owner":

				code, out, stderr = f.run(t, f.env, "copies", "release", parent)
			case "release":
				addReleaseReport(t, f.stateDir(t), "worker", false)
				code, out, stderr = f.run(t, f.env, "release", "worker")
			case "gc":
				// The parent is orphaned, the child's owner remains live.
				f.writeRoster(t, copiesRosterRow("other", "p-other"))
				hint := filepath.Join(f.root, "alternate-registry")
				if err := os.Mkdir(hint, 0700); err != nil {
					t.Fatal(err)
				}
				f.env["SOHO_REGISTRY_DIR"] = hint
				code, out, stderr = f.run(t, f.env, "gc", "--yes")
			}

			if !strings.Contains(out+stderr, "holds "+child+" registered by other") || len(f.copiesRows(t)) != 2 {
				t.Fatalf("mode=%s code=%d out=%q stderr=%q rows=%v", mode, code, out, stderr, f.copiesRows(t))
			}
			if (mode == "copies" && code != 2) || (mode != "copies" && code != 0) {
				t.Fatalf("mode=%s code=%d out=%q stderr=%q", mode, code, out, stderr)
			}
			if _, err := os.Stat(child); err != nil {
				t.Fatalf("child missing: %v", err)
			}
		})
	}
}

func TestCopiesReleaseProtectsLiveSnapshot(t *testing.T) {
	f := newSnapshotFixture(t)
	snapshot := f.freeze(t, "worker")
	parent := filepath.Dir(snapshot["patch"])
	if err := core.UpsertCopies(f.sd, core.CopyRow{Path: parent, Owner: "worker", Pane: "p-worker", Created: "2020-01-01T00:00:00Z", Source: "-", Origin: "add"}); err != nil {
		t.Fatal(err)
	}
	f.env["SOHO_AGENT"] = "worker"
	code, out, stderr := f.run(t, "copies", "release", parent)
	if code != 2 || !strings.Contains(stderr, "holds "+snapshot["patch"]+" registered by worker") {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	if _, err := os.Stat(snapshot["snapshot"]); err != nil {
		t.Fatalf("snapshot missing: %v", err)
	}
}

func TestCopiesRemovalSnapshotRegistryError(t *testing.T) {
	for _, mode := range []string{"copies", "gc"} {
		t.Run(mode, func(t *testing.T) {
			f := newCopiesFixture(t, nil)
			parent := filepath.Join(f.tmp, "copy")
			if err := os.Mkdir(parent, 0700); err != nil {
				t.Fatal(err)
			}
			f.writeCopies(t, []core.CopyRow{{Path: parent, Owner: "worker", Pane: "p", Created: "2020-01-01T00:00:00Z", Source: "-", Origin: "add"}})
			if err := os.WriteFile(filepath.Join(f.stateDir(t), "snapshots.tsv"), []byte("invalid\n"), 0600); err != nil {
				t.Fatal(err)
			}
			f.env["SOHO_AGENT"] = "worker"
			var code int
			var out, stderr string
			if mode == "copies" {
				code, out, stderr = f.run(t, f.env, "copies", "release", parent)
			} else {
				code, out, stderr = f.run(t, f.env, "gc", "--yes")
			}
			if code != 4 || !strings.Contains(stderr, "cannot read snapshot registry") || len(f.copiesRows(t)) != 1 {
				t.Fatalf("code=%d out=%q stderr=%q rows=%v", code, out, stderr, f.copiesRows(t))
			}
			if _, err := os.Stat(parent); err != nil {
				t.Fatalf("parent removed: %v", err)
			}
		})
	}
}

func TestGcProtectsLiveSnapshot(t *testing.T) {
	f := newSnapshotFixture(t)
	snapshot := f.freeze(t, "worker")
	parent := filepath.Dir(snapshot["patch"])
	if err := core.UpsertCopies(f.sd, core.CopyRow{Path: parent, Owner: "ghost", Pane: "-", Created: "2020-01-01T00:00:00Z", Source: "-", Origin: "add"}); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := f.run(t, "gc", "--yes")
	if code != 0 || !strings.Contains(out+stderr, "holds "+snapshot["patch"]+" registered by worker") {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	if _, err := os.Stat(snapshot["snapshot"]); err != nil {
		t.Fatalf("snapshot missing: %v", err)
	}
}

func TestCopiesRemovalRejectsRedirectedAncestor(t *testing.T) {
	for _, mode := range []string{"release", "worker-release", "gc"} {
		t.Run(mode, func(t *testing.T) {
			f := newCopiesFixture(t, nil)
			ancestor := filepath.Join(f.tmp, "ancestor")
			path := filepath.Join(ancestor, "copy")
			target := filepath.Join(f.tmp, "target")
			if err := os.MkdirAll(path, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(target, "copy"), 0700); err != nil {
				t.Fatal(err)
			}
			f.writeCopies(t, []core.CopyRow{{Path: path, Owner: "worker", Pane: "p", Created: "2020-01-01T00:00:00Z", Source: "-", Origin: "add"}})
			if err := os.Rename(ancestor, filepath.Join(f.tmp, "original")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, ancestor); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}
			f.env["SOHO_AGENT"] = "worker"
			var code int
			var out, stderr string
			if mode == "release" {
				code, out, stderr = f.run(t, f.env, "copies", "release", path)
			} else if mode == "worker-release" {
				f.writeRoster(t, copiesRosterRow("worker", "p-worker"))
				code, out, stderr = f.run(t, f.env, "release", "worker", "--force")
			} else {
				f.writeCopies(t, []core.CopyRow{{Path: path, Owner: "-", Created: "2020-01-01T00:00:00Z"}})
				code, out, stderr = f.run(t, f.env, "gc", "--yes")
			}
			if (mode == "release" && code != 2) || ((mode == "gc" || mode == "worker-release") && code != 0) || !strings.Contains(out+stderr, "an ancestor is a symbolic link ("+ancestor+")") || len(f.copiesRows(t)) != 1 {
				t.Fatalf("mode=%s code=%d out=%q stderr=%q rows=%v", mode, code, out, stderr, f.copiesRows(t))
			}
			if _, err := os.Stat(filepath.Join(target, "copy")); err != nil {
				t.Fatalf("target removed: %v", err)
			}
		})
	}
}

func TestCopiesRemovalMacOSTempAlias(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS system alias")
	}
	f := newCopiesFixture(t, nil)
	if !strings.HasPrefix(f.tmp, "/private/var/") {
		t.Skipf("temporary root is not /private/var: %s", f.tmp)
	}
	path := filepath.Join(f.tmp, "copy")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	alias := strings.TrimPrefix(path, "/private")
	f.writeCopies(t, []core.CopyRow{{Path: alias, Owner: "worker", Pane: "p", Created: "2020-01-01T00:00:00Z", Source: "-", Origin: "add"}})
	f.env["SOHO_AGENT"] = "worker"
	code, out, stderr := f.run(t, f.env, "copies", "release", alias)
	if code != 0 || len(f.copiesRows(t)) != 0 {
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

func TestReleaseSnapshotRegistryPreflight(t *testing.T) {
	for _, mode := range []string{"release", "gc-hint"} {
		t.Run(mode, func(t *testing.T) {
			f := newCopiesFixture(t, nil)
			parent := filepath.Join(f.tmp, "copy")
			if err := os.Mkdir(parent, 0700); err != nil {
				t.Fatal(err)
			}
			f.writeCopies(t, []core.CopyRow{{Path: parent, Owner: "worker", Pane: "p-worker", Created: "2020-01-01T00:00:00Z", Source: "-", Origin: "add"}})
			f.writeRoster(t, copiesRosterRow("worker", "p-worker"))
			sd := f.stateDir(t)
			if mode == "gc-hint" {
				f.writeCopies(t, []core.CopyRow{{Path: parent, Owner: "ghost", Pane: "p", Created: "2020-01-01T00:00:00Z", Source: "-", Origin: "add"}})
				sd = filepath.Join(f.root, "hint")
				if err := os.Mkdir(sd, 0700); err != nil {
					t.Fatal(err)
				}
				f.env["SOHO_REGISTRY_DIR"] = sd
			}
			if err := os.WriteFile(filepath.Join(sd, "snapshots.tsv"), []byte("invalid\n"), 0600); err != nil {
				t.Fatal(err)
			}
			var code int
			var out, stderr string
			if mode == "release" {
				code, out, stderr = f.run(t, f.env, "release", "worker", "--force")
			} else {
				code, out, stderr = f.run(t, f.env, "gc", "--yes")
			}
			if code != 4 || !strings.Contains(stderr, "cannot read snapshot registry") || len(f.copiesRows(t)) != 1 || core.RosterLine(f.stateDir(t), "worker") == "" {
				t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
			}
			if _, err := os.Stat(parent); err != nil {
				t.Fatalf("copy removed: %v", err)
			}
		})
	}
}

func TestCopyGuardSelectedRegistry(t *testing.T) {
	f := newCopiesFixture(t, nil)
	parent := filepath.Join(f.tmp, "parent")
	child := filepath.Join(parent, "child")
	if err := os.MkdirAll(child, 0700); err != nil {
		t.Fatal(err)
	}
	selected := filepath.Join(f.root, "selected")
	if err := os.Mkdir(selected, 0700); err != nil {
		t.Fatal(err)
	}
	for _, row := range []core.CopyRow{{Path: parent, Owner: "worker", Pane: "p", Created: "2020-01-01T00:00:00Z", Source: "-", Origin: "add"}, {Path: child, Owner: "other", Pane: "p", Created: "2020-01-01T00:00:00Z", Source: "-", Origin: "add"}} {
		if err := core.UpsertCopies(selected, row); err != nil {
			t.Fatal(err)
		}
	}
	cause, _ := copyDeletionGuard(parent, "worker", selected, f.env, f.cwd)
	if !strings.Contains(cause, "registered by other") {
		t.Fatalf("selected registry ignored: %q", cause)
	}
}

func TestCopiesReleaseSnapshotMacAlias(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS system alias")
	}
	f := newSnapshotFixture(t)
	snapshot := f.freeze(t, "worker")
	parent := filepath.Dir(snapshot["patch"])
	if !strings.HasPrefix(parent, "/private/var/") {
		t.Skipf("snapshot root is not /private/var: %s", parent)
	}
	alias := strings.TrimPrefix(parent, "/private")
	if err := core.UpsertCopies(f.sd, core.CopyRow{Path: alias, Owner: "worker", Pane: "p-worker", Created: "2020-01-01T00:00:00Z", Source: "-", Origin: "add"}); err != nil {
		t.Fatal(err)
	}
	f.env["SOHO_AGENT"] = "worker"
	code, out, stderr := f.run(t, "copies", "release", alias)
	if code != 2 || !strings.Contains(stderr, "holds "+snapshot["patch"]) {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	if _, err := os.Stat(snapshot["snapshot"]); err != nil {
		t.Fatalf("snapshot removed: %v", err)
	}
}

func TestGcSymlinkedScanRoot(t *testing.T) {
	f := newCopiesFixture(t, nil)
	link := filepath.Join(f.root, "tmp-link")
	if err := os.Symlink(f.tmp, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	f.env["TMPDIR"] = link
	path := filepath.Join(f.tmp, "soho-mutation-old")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	old := platform.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	writeTestOriginMarker(t, path, gitCommonDirOf(t, f.repo, f.env), f.repo)
	// gc names the entry as it enumerated it, through the TMPDIR link; the
	// removal itself is what this test proves.
	code, out, stderr := f.run(t, f.env, "gc", "--yes", "--include-unregistered")
	if code != 0 || !strings.Contains(out, "removed ") || !strings.Contains(out, "soho-mutation-old") {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("copy remains: %v", err)
	}
}

func TestOrphanScopeIncludesParentWithForeignChild(t *testing.T) {
	f := newCopiesFixture(t, nil)
	parent := filepath.Join(f.tmp, "parent")
	child := filepath.Join(parent, "child")
	if err := os.MkdirAll(child, 0700); err != nil {
		t.Fatal(err)
	}
	f.writeCopies(t, []core.CopyRow{{Path: parent, Owner: "worker", Pane: "p", Created: "2020-01-01T00:00:00Z", Source: "-", Origin: "add"}, {Path: child, Owner: "other", Pane: "p", Created: "2020-01-01T00:00:00Z", Source: "-", Origin: "add"}})
	for _, p := range orphanScope(f.stateDir(t), f.env, f.cwd) {
		if p.kind == orphanKindCopy && p.path == parent {
			return
		}
	}
	t.Fatal("parent missing from orphan process scope")
}

func TestCopiesReleaseNestedMacAlias(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS system alias")
	}
	f := newCopiesFixture(t, nil)
	if !strings.HasPrefix(f.tmp, "/private/var/") {
		t.Skipf("temporary root is not /private/var: %s", f.tmp)
	}
	parent := filepath.Join(f.tmp, "parent")
	child := filepath.Join(parent, "child")
	if err := os.MkdirAll(child, 0700); err != nil {
		t.Fatal(err)
	}
	alias := strings.TrimPrefix(parent, "/private")
	f.writeCopies(t, []core.CopyRow{{Path: alias, Owner: "worker", Pane: "p", Created: "2020-01-01T00:00:00Z", Source: "-", Origin: "add"}, {Path: child, Owner: "other", Pane: "p", Created: "2020-01-01T00:00:00Z", Source: "-", Origin: "add"}})
	f.env["SOHO_AGENT"] = "worker"
	code, out, stderr := f.run(t, f.env, "copies", "release", alias)
	if code != 2 || !strings.Contains(stderr, "holds "+child+" registered by other") || len(f.copiesRows(t)) != 2 {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	if _, err := os.Stat(child); err != nil {
		t.Fatalf("child removed: %v", err)
	}
}

func TestCopiesReleaseDistinctCaseDirectories(t *testing.T) {
	for _, owner := range []string{"worker", "other"} {
		t.Run(owner, func(t *testing.T) {
			f := newCopiesFixture(t, nil)
			upper := filepath.Join(f.tmp, "Copy")
			lower := filepath.Join(f.tmp, "copy")
			if err := os.Mkdir(upper, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(lower, 0700); os.IsExist(err) {
				t.Skipf("case-insensitive volume: upper=%s lower=%s", upper, lower)
			} else if err != nil {
				t.Fatal(err)
			}
			child := filepath.Join(lower, "child")
			if err := os.Mkdir(child, 0700); err != nil {
				t.Fatal(err)
			}
			f.writeCopies(t, []core.CopyRow{{Path: upper, Owner: "worker", Pane: "p", Created: "2020-01-01T00:00:00Z", Source: "-", Origin: "add"}, {Path: child, Owner: owner, Pane: "p", Created: "2020-01-01T00:00:00Z", Source: "-", Origin: "add"}})
			f.env["SOHO_AGENT"] = "worker"
			code, out, stderr := f.run(t, f.env, "copies", "release", upper)
			rows := f.copiesRows(t)
			if code != 0 || len(rows) != 1 || rows[0].Path != child || rows[0].Owner != owner {
				t.Fatalf("code=%d out=%q stderr=%q rows=%v", code, out, stderr, rows)
			}
			if _, err := os.Stat(child); err != nil {
				t.Fatalf("child %s missing: %v", child, err)
			}
			if _, err := os.Stat(upper); !os.IsNotExist(err) {
				t.Fatalf("upper remains: %v", err)
			}
		})
	}
}

func TestCopiesReleaseUnregisteredSymlinkOperand(t *testing.T) {
	f := newCopiesFixture(t, nil)
	parent := filepath.Join(f.tmp, "parent")
	alias := filepath.Join(f.tmp, "alias")
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(parent, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	f.writeCopies(t, []core.CopyRow{{Path: parent, Owner: "worker", Pane: "p", Created: "2020-01-01T00:00:00Z", Source: "-", Origin: "add"}})
	f.env["SOHO_AGENT"] = "worker"
	code, out, stderr := f.run(t, f.env, "copies", "release", alias)
	if code != 2 || !strings.Contains(stderr, "not registered") || len(f.copiesRows(t)) != 1 {
		t.Fatalf("code=%d out=%q stderr=%q rows=%v", code, out, stderr, f.copiesRows(t))
	}
	if _, err := os.Stat(parent); err != nil {
		t.Fatalf("registered directory missing: %v", err)
	}
}

// This helper creates a confirmed binding for its own real supervisor PID,
// then runs the consumer command as that supervisor's child.
func init() {
	parent := len(os.Args) == 5 && os.Args[1] == "job" && os.Args[2] == "supervise" && os.Args[3] == "--id" && os.Args[4] == "job-copyguard"
	child := len(os.Args) == 2 && os.Args[1] == "--soho-copy-guard-child"
	if !parent && !child {
		return
	}
	data, err := os.ReadFile(".copy-guard-test-env.json")
	if err != nil {
		panic(err)
	}
	env := platform.Env{}
	if err := json.Unmarshal(data, &env); err != nil {
		panic(err)
	}
	if child {
		code := Run([]string{"gc", "--yes"}, env)
		fmt.Printf("managed gc exit=%d\n", code)
		os.Exit(0)
	}
	started, _, live := platform.ProcStartIdentity(os.Getpid(), env)
	exe, _, err := platform.ProcExec(os.Getpid())
	if err != nil || live != platform.ProcRunning {
		fmt.Printf("identity error=%v state=%v\n", err, live)
		os.Exit(1)
	}
	root := env.Get("SOHO_COPY_GUARD_TEST_ROOT")
	launch := map[string]any{"v": 1, "id": "job-copyguard", "store": root, "workspace": "ws", "pane": "p-worker", "nonce": "0123456789abcdef0123456789abcdef", "state": "confirmed", "pid": os.Getpid(), "started": started, "exe": exe, "config_base": env.Get(testLaunchBaseVar)}
	data, err = json.Marshal(launch)
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(filepath.Join(root, "jobs", "job-copyguard", "launch.json"), data, 0600); err != nil {
		panic(err)
	}
	self, err := os.Executable()
	if err != nil {
		panic(err)
	}
	cmd := exec.Command(self, "--soho-copy-guard-child")
	cmd.Env, cmd.Stdout, cmd.Stderr = env.List(), os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Printf("managed child: %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}

func TestManagedGcUsesBoundCopyRegistry(t *testing.T) {
	f := newCopiesFixture(t, []fakecli.Rule{{Argv: []string{"agent", "get", "other"}, Stdout: agentIdleJSON}, {Argv: []string{"pane", "list", "--workspace", "ws"}, Stdout: `{"result":{"panes":[]}}`}})
	base := filepath.Join(f.root, "bound-config")
	if err := os.MkdirAll(filepath.Join(base, "soho"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "soho", "config"), []byte("state_dir=.soho-bound\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ambient := filepath.Join(f.root, "conf", "soho")
	if err := os.MkdirAll(ambient, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ambient, "config"), []byte("state_dir=.soho-ambient\n"), 0600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(f.repo, ".soho-bound")
	sd := filepath.Join(root, "ws")
	jobDir := filepath.Join(root, "jobs", "job-copyguard")
	if err := os.MkdirAll(jobDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sd, 0700); err != nil {
		t.Fatal(err)
	}
	state, _ := json.Marshal(map[string]string{"status": "running", "dir": f.repo, "workspace_id": "ws", "root_pane": "p-worker"})
	if err := os.WriteFile(filepath.Join(jobDir, "state.json"), state, 0600); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(f.tmp, "parent")
	child := filepath.Join(parent, "child")
	if err := os.MkdirAll(child, 0700); err != nil {
		t.Fatal(err)
	}
	for _, row := range []core.CopyRow{{Path: parent, Owner: "ghost", Pane: "p", Created: "2020-01-01T00:00:00Z", Source: "-", Origin: "add"}, {Path: child, Owner: "other", Pane: "p-other", Created: "2020-01-01T00:00:00Z", Source: "-", Origin: "add"}} {
		if err := core.UpsertCopies(sd, row); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(sd, "agents.tsv"), []byte(copiesRosterRow("other", "p-other")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	env := f.env.Clone()
	delete(env, "SOHO_DIR")
	env[testLaunchBaseVar] = base
	env["SOHO_COPY_GUARD_TEST_ROOT"] = root
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.repo, ".copy-guard-test-env.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self, "job", "supervise", "--id", "job-copyguard")
	cmd.Dir = f.repo
	cmd.Env = env.List()
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "managed gc exit=0") || !strings.Contains(string(out), "holds "+child+" registered by other") {
		t.Fatalf("managed command err=%v output=%q", err, out)
	}
	rows, err := core.ReadCopies(sd)
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows=%v err=%v output=%q", rows, err, out)
	}
	if _, err := os.Stat(child); err != nil {
		t.Fatalf("protected child missing: %v", err)
	}
}

func TestCopiesReleaseAtConfiguredScanRoot(t *testing.T) {
	f := newCopiesFixture(t, nil)
	path := filepath.Join(f.root, "copies-root")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	f.env["SOHO_COPIES_DIR"] = path
	f.env["SOHO_AGENT"] = "worker"
	f.writeCopies(t, []core.CopyRow{{Path: path, Owner: "worker", Pane: "p", Created: "2020-01-01T00:00:00Z", Source: "-", Origin: "add"}})
	code, out, stderr := f.run(t, f.env, "copies", "release", path)
	if code != 0 || len(f.copiesRows(t)) != 0 {
		t.Fatalf("code=%d out=%q stderr=%q rows=%v", code, out, stderr, f.copiesRows(t))
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("copy remains: %v", err)
	}
}

func TestGcPreviewUnregisteredGuard(t *testing.T) {
	for _, mode := range []string{"held", "unreadable", "eligible"} {
		t.Run(mode, func(t *testing.T) {
			f := newCopiesFixture(t, []fakecli.Rule{{Argv: []string{"agent", "get", "other"}, Stdout: agentIdleJSON}})
			parent := filepath.Join(f.tmp, "soho-mutation-old")
			child := filepath.Join(parent, "child")
			if err := os.MkdirAll(child, 0700); err != nil {
				t.Fatal(err)
			}
			old := platform.Now().Add(-3 * time.Hour)
			if err := os.Chtimes(parent, old, old); err != nil {
				t.Fatal(err)
			}
			writeTestOriginMarker(t, parent, gitCommonDirOf(t, f.repo, f.env), f.repo)
			if mode == "held" {
				f.writeRoster(t, copiesRosterRow("other", "p-other"))
				f.writeCopies(t, []core.CopyRow{{Path: child, Owner: "other", Pane: "p-other", Created: "2020-01-01T00:00:00Z", Source: "-", Origin: "add"}})
			}
			if mode == "unreadable" {
				if err := os.WriteFile(filepath.Join(f.stateDir(t), "snapshots.tsv"), []byte("invalid\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			code, out, stderr := f.run(t, f.env, "gc")
			hint := strings.Contains(out, "gc --yes --include-unregistered")
			if code != 0 || hint != (mode == "eligible") || mode == "held" && !strings.Contains(out, "holds "+child) || mode == "unreadable" && !strings.Contains(out, "cannot read snapshot registry") {
				t.Fatalf("mode=%s code=%d out=%q stderr=%q", mode, code, out, stderr)
			}
			if _, err := os.Stat(child); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// R537 F1: a copies_dir that is a link inside TMPDIR, pointing outside it,
// is the root for its own entries: gc collects an old marked copy there.
func TestGcConfiguredRootLinkInsideTmpdir(t *testing.T) {
	f := newCopiesFixture(t, nil)
	outside := filepath.Join(f.root, "outside-copies")
	if err := os.Mkdir(outside, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(f.tmp, "copies-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	f.env["SOHO_COPIES_DIR"] = link
	path := filepath.Join(link, "soho-mutation-old")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	old := platform.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	writeTestOriginMarker(t, path, gitCommonDirOf(t, f.repo, f.env), f.repo)
	code, out, stderr := f.run(t, f.env, "gc", "--yes", "--include-unregistered")
	if code != 0 || !strings.Contains(out, "removed ") || !strings.Contains(out, "soho-mutation-old") {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	if _, err := os.Stat(filepath.Join(outside, "soho-mutation-old")); !os.IsNotExist(err) {
		t.Fatalf("copy remains: %v", err)
	}
}
