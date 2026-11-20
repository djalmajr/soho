package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// symlinkedParentCopy makes a copy under a real directory and returns its
// registered (resolved) path and the path spelled through a symlinked parent,
// the way /tmp/x resolves to /private/tmp/x on macOS.
func symlinkedParentCopy(t *testing.T, f *copiesFixture, name string) (string, string) {
	t.Helper()
	realParent := filepath.Join(f.tmp, name+"-real")
	copyPath := filepath.Join(realParent, "copy")
	if err := os.MkdirAll(copyPath, 0o700); err != nil {
		t.Fatal(err)
	}
	aliasParent := filepath.Join(f.tmp, name+"-alias")
	if err := os.Symlink(realParent, aliasParent); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	return copyPath, filepath.Join(aliasParent, "copy")
}

func TestCopiesReleaseResolvesSymlinkedParent(t *testing.T) {
	f := newCopiesFixture(t, nil)
	registered, typed := symlinkedParentCopy(t, f, "release")
	f.writeCopies(t, []core.CopyRow{{Path: registered, Owner: "worker", Pane: "p-worker", Created: "2026-01-01T00:00:00Z", Source: "-", Origin: "add"}})
	f.env["SOHO_AGENT"] = "worker"
	code, out, stderr := f.run(t, f.env, "copies", "release", typed)
	if code != 0 || !strings.Contains(out, "removed "+registered) {
		t.Fatalf("code=%d out=%q stderr=%q; want the registered path removed", code, out, stderr)
	}
	if _, err := os.Lstat(registered); !os.IsNotExist(err) {
		t.Fatalf("the copy is still on disk: %v", err)
	}
	if rows := f.copiesRows(t); len(rows) != 0 {
		t.Fatalf("rows=%#v; want the row dropped", rows)
	}
}

func TestCopiesReleaseDoesNotFollowLastElementSymlink(t *testing.T) {
	f := newCopiesFixture(t, nil)
	parent := filepath.Join(f.tmp, "real")
	keep := filepath.Join(parent, "registered")
	target := filepath.Join(parent, "target")
	for _, dir := range []string{keep, target} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(parent, "link-copy")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	rows := []core.CopyRow{
		{Path: keep, Owner: "worker", Pane: "p-worker", Created: "2026-01-01T00:00:00Z", Source: "-", Origin: "add"},
		{Path: target, Owner: "worker", Pane: "p-worker", Created: "2026-01-01T00:00:00Z", Source: "-", Origin: "add"},
	}
	f.writeCopies(t, rows)
	f.env["SOHO_AGENT"] = "worker"
	code, out, stderr := f.run(t, f.env, "copies", "release", link)
	if code != 2 || !strings.Contains(stderr, "copies release: refusing '"+link+"': not registered") {
		t.Fatalf("code=%d out=%q stderr=%q; want the link refused as not registered", code, out, stderr)
	}
	if got := f.copiesRows(t); len(got) != len(rows) {
		t.Fatalf("rows=%#v; want both rows untouched", got)
	}
	for _, dir := range []string{keep, target} {
		if info, err := os.Lstat(dir); err != nil || !info.IsDir() {
			t.Fatalf("dir=%s err=%v; want the copy untouched", dir, err)
		}
	}
}

func TestCopiesReleaseSymlinkedParentKeepsOwnership(t *testing.T) {
	f := newCopiesFixture(t, nil)
	registered, typed := symlinkedParentCopy(t, f, "foreign")
	f.writeCopies(t, []core.CopyRow{{Path: registered, Owner: "other", Pane: "p-other", Created: "2026-01-01T00:00:00Z", Source: "-", Origin: "add"}})
	f.env["SOHO_AGENT"] = "worker"
	code, out, stderr := f.run(t, f.env, "copies", "release", typed)
	if code != 2 || !strings.Contains(stderr, "refusing '"+typed+"'") || !strings.Contains(stderr, "owned by another agent") || !strings.Contains(stderr, "(resolved: "+registered+")") {
		t.Fatalf("code=%d out=%q stderr=%q; want the typed path refused with its resolved spelling", code, out, stderr)
	}
	if rows := f.copiesRows(t); len(rows) != 1 || rows[0].Path != registered {
		t.Fatalf("rows=%#v; want the row kept", rows)
	}
	if _, err := os.Lstat(registered); err != nil {
		t.Fatalf("the copy was removed: %v", err)
	}
}

func TestCopiesReleaseRelativePathIsStillRefused(t *testing.T) {
	f := newCopiesFixture(t, nil)
	copyDir := filepath.Join(f.tmp, "copy")
	if err := os.MkdirAll(copyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	registered := []core.CopyRow{{Path: copyDir, Owner: "worker", Pane: "p-worker", Created: "2026-01-01T00:00:00Z", Source: "-", Origin: "add"}}
	f.writeCopies(t, registered)
	f.env["SOHO_AGENT"] = "worker"
	code, out, stderr := f.run(t, f.env, "copies", "release", "tmp/copy")
	if code != 2 || !strings.Contains(stderr, "not an absolute path") || strings.Contains(stderr, "resolved:") {
		t.Fatalf("code=%d out=%q stderr=%q; want the relative path refused before resolution", code, out, stderr)
	}
	if rows := f.copiesRows(t); len(rows) != 1 {
		t.Fatalf("rows=%#v; want the row kept", rows)
	}
}

func TestCopiesAddPrintsRegisteredLine(t *testing.T) {
	t.Run("written row", func(t *testing.T) {
		f := newCopiesFixture(t, []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: agentIdleJSON}})
		f.writeRoster(t, copiesRosterRow("worker", "p-worker"))
		registered, typed := symlinkedParentCopy(t, f, "add")
		code, out, stderr := f.run(t, f.env, "copies", "add", typed)
		want := "copies add: registered " + registered + " (owner worker)\n"
		if code != 0 || out != want || stderr != "" {
			t.Fatalf("code=%d out=%q stderr=%q; want 0, %q and no stderr", code, out, stderr, want)
		}
		if rows := f.copiesRows(t); len(rows) != 1 || rows[0].Path != registered {
			t.Fatalf("rows=%#v; want the resolved path registered", rows)
		}
	})

	t.Run("queued row", func(t *testing.T) {
		f := newCopiesFixture(t, []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: agentIdleJSON}})
		f.writeRoster(t, copiesRosterRow("worker", "p-worker"))
		sd := f.stateDir(t)
		copyDir := filepath.Join(f.tmp, "queued-add")
		if err := os.MkdirAll(copyDir, 0o700); err != nil {
			t.Fatal(err)
		}
		restore := refuseCopiesRegistry(t, nil)
		code, out, stderr := f.run(t, f.env, "copies", "add", copyDir)
		restore()
		wantLine := "copies add: the state dir " + sd + " is not writable from here; the registration is queued for the orchestrator in " + core.PendingCopiesFile(f.env, "ws") + "\n"
		wantOut := "copies add: registered " + copyDir + " (owner worker)\n"
		if code != 0 || out != wantOut || stderr != wantLine {
			t.Fatalf("code=%d out=%q stderr=%q; want 0, %q and the queue line", code, out, stderr, wantOut)
		}
	})
}

func TestCopiesListAliasesCopies(t *testing.T) {
	f := newCopiesFixture(t, []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: agentIdleJSON}})
	f.writeRoster(t, copiesRosterRow("worker", "p-worker"))
	copyDir := filepath.Join(f.tmp, "listed")
	if err := os.MkdirAll(copyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	f.writeCopies(t, []core.CopyRow{{Path: copyDir, Owner: "worker", Pane: "p-worker", Created: "2020-01-01T00:00:00Z", Source: "-", Origin: "add"}})
	code, plainOut, plainErr := f.run(t, f.env, "copies")
	aliasCode, aliasOut, aliasErr := f.run(t, f.env, "copies", "list")
	if code != 0 || aliasCode != 0 || plainOut != aliasOut || plainErr != aliasErr {
		t.Fatalf("copies: code=%d out=%q err=%q; copies list: code=%d out=%q err=%q", code, plainOut, plainErr, aliasCode, aliasOut, aliasErr)
	}
	if !strings.Contains(aliasOut, copyDir+"  worker  ") {
		t.Fatalf("copies list out=%q; want the registered row", aliasOut)
	}
	code, out, stderr := f.run(t, f.env, "copies", "list", "extra")
	if code != 2 || !strings.Contains(stderr, copiesUsage) || out != "" {
		t.Fatalf("code=%d out=%q stderr=%q; want the usage refusal", code, out, stderr)
	}
}

// A parent spelled with a symlink followed by ".." resolves the symlink first,
// as the EvalSymlinks call in copies add does, so release removes the copy add
// registered and not a sibling that lexical cleaning would name (SOH-464 R1 F1).
func TestCopiesAddThenReleaseThroughSymlinkThenDotDot(t *testing.T) {
	f := newCopiesFixture(t, []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: agentIdleJSON}})
	f.writeRoster(t, copiesRosterRow("worker", "p-worker"))
	base := filepath.Join(f.tmp, "dotdot")
	realDir := filepath.Join(base, "real")
	realCopy := filepath.Join(realDir, "copy")
	sibling := filepath.Join(base, "copy")
	for _, dir := range []string{filepath.Join(realDir, "inner"), realCopy, sibling} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(filepath.Join(realDir, "inner"), link); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	sep := string(filepath.Separator)
	typed := link + sep + ".." + sep + "copy"
	code, out, stderr := f.run(t, f.env, "copies", "add", typed)
	if want := "copies add: registered " + realCopy + " (owner worker)\n"; code != 0 || out != want {
		t.Fatalf("add: code=%d out=%q stderr=%q; want %q", code, out, stderr, want)
	}
	if code, out, stderr = f.run(t, f.env, "copies", "add", sibling); code != 0 {
		t.Fatalf("add sibling: code=%d out=%q stderr=%q", code, out, stderr)
	}
	f.env["SOHO_AGENT"] = "worker"
	code, out, stderr = f.run(t, f.env, "copies", "release", typed)
	if code != 0 || !strings.Contains(out, "removed "+realCopy) {
		t.Fatalf("release: code=%d out=%q stderr=%q; want %s removed", code, out, stderr, realCopy)
	}
	if _, err := os.Lstat(realCopy); !os.IsNotExist(err) {
		t.Fatalf("the copy add registered is still on disk: %v", err)
	}
	if info, err := os.Lstat(sibling); err != nil || !info.IsDir() {
		t.Fatalf("the sibling copy was touched: %v", err)
	}
	if rows := f.copiesRows(t); len(rows) != 1 || rows[0].Path != sibling {
		t.Fatalf("rows=%#v; want only the sibling row left", rows)
	}
}

// A registration still queued under the resolved spelling blocks a release
// typed through a symlinked parent, as it blocks the resolved spelling
// (SOH-464 R1 F2).
func TestCopiesReleasePendingRecheckMatchesResolvedSpelling(t *testing.T) {
	f := newCopiesFixture(t, nil)
	registered, typed := symlinkedParentCopy(t, f, "pending")
	f.writeCopies(t, []core.CopyRow{{Path: registered, Owner: "worker", Pane: "p-worker", Created: "2026-01-01T00:00:00Z", Source: "-", Origin: "add"}})
	f.env["SOHO_AGENT"] = "worker"
	old := copiesReleasePending
	t.Cleanup(func() { copiesReleasePending = old })
	copiesReleasePending = func(string, string, platform.Env) ([]core.CopyRow, error) {
		return []core.CopyRow{{Path: registered, Owner: "other", Pane: "p-other", Created: "2026-01-01T00:00:00Z", Source: "-", Origin: "add"}}, nil
	}
	code, out, stderr := f.run(t, f.env, "copies", "release", typed)
	if code != 4 || !strings.Contains(stderr, "copies release: pending registration \""+registered+"\" overlaps request; nothing removed") {
		t.Fatalf("code=%d out=%q stderr=%q; want the queued registration to block the release", code, out, stderr)
	}
	if _, err := os.Lstat(registered); err != nil {
		t.Fatalf("the copy was removed: %v", err)
	}
	if rows := f.copiesRows(t); len(rows) != 1 {
		t.Fatalf("rows=%#v; want the row kept", rows)
	}
}

func TestCanonicalCopyPathEdges(t *testing.T) {
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	sep := string(filepath.Separator)
	realDir := filepath.Join(dir, "real")
	if err := os.MkdirAll(filepath.Join(realDir, "inner"), 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(filepath.Join(realDir, "inner"), link); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	for _, tc := range []struct {
		in   string
		want string
		ok   bool
	}{
		// An existing directory resolves whole, as copies add resolves it.
		{in: realDir, want: filepath.Join(resolved, "real"), ok: true},
		{in: realDir + sep + ".", want: filepath.Join(resolved, "real"), ok: true},
		{in: realDir + sep + "inner" + sep + "..", want: filepath.Join(resolved, "real"), ok: true},
		{in: link + sep + ".." + sep + "inner", want: filepath.Join(resolved, "real", "inner"), ok: true},
		// The path itself a symlink: never followed, trailing separators or not.
		{in: link, ok: false},
		{in: link + sep, ok: false},
		{in: link + sep + sep, ok: false},
		// A copy already gone: the parent is resolved from the typed text.
		{in: dir + sep + "gone", want: filepath.Join(resolved, "gone"), ok: true},
		{in: dir + sep + "gone" + sep, want: filepath.Join(resolved, "gone"), ok: true},
		{in: link + sep + ".." + sep + "gone", want: filepath.Join(resolved, "real", "gone"), ok: true},
		{in: dir + sep + "missing" + sep + "gone", ok: false},
		{in: dir + sep + "missing" + sep + "..", ok: false},
	} {
		got, ok := canonicalCopyPath(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("canonicalCopyPath(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

// A copy addressed with a terminal "." or "<child>/.." names the directory add
// registered for the same spelling, so release finds its row (SOH-464 R2 F1).
func TestCopiesReleaseTerminalDotSpellings(t *testing.T) {
	for _, suffix := range []string{".", "inner" + string(filepath.Separator) + ".."} {
		t.Run(suffix, func(t *testing.T) {
			f := newCopiesFixture(t, nil)
			copyDir := filepath.Join(f.tmp, "dotted")
			if err := os.MkdirAll(filepath.Join(copyDir, "inner"), 0o700); err != nil {
				t.Fatal(err)
			}
			f.writeCopies(t, []core.CopyRow{{Path: copyDir, Owner: "worker", Pane: "p-worker", Created: "2026-01-01T00:00:00Z", Source: "-", Origin: "add"}})
			f.env["SOHO_AGENT"] = "worker"
			typed := copyDir + string(filepath.Separator) + suffix
			code, out, stderr := f.run(t, f.env, "copies", "release", typed)
			if code != 0 || !strings.Contains(out, "removed "+copyDir) {
				t.Fatalf("code=%d out=%q stderr=%q; want %s removed", code, out, stderr, copyDir)
			}
			if rows := f.copiesRows(t); len(rows) != 0 {
				t.Fatalf("rows=%#v; want the row dropped", rows)
			}
		})
	}
}

// The queued-registration check and the row selection use one resolution: a
// parent link swapped while release waits for the registry lock cannot move
// the removal onto a copy the queue check never compared (SOH-464 R2 F2).
func TestCopiesReleaseResolvesOnceAcrossTheLock(t *testing.T) {
	f := newCopiesFixture(t, nil)
	realA := filepath.Join(f.tmp, "real-a")
	realB := filepath.Join(f.tmp, "real-b")
	copyA := filepath.Join(realA, "copy")
	copyB := filepath.Join(realB, "copy")
	for _, dir := range []string{copyA, copyB} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	alias := filepath.Join(f.tmp, "alias")
	if err := os.Symlink(realA, alias); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	f.writeCopies(t, []core.CopyRow{
		{Path: copyA, Owner: "worker", Pane: "p-worker", Created: "2026-01-01T00:00:00Z", Source: "-", Origin: "add"},
		{Path: copyB, Owner: "worker", Pane: "p-worker", Created: "2026-01-01T00:00:00Z", Source: "-", Origin: "add"},
	})
	f.env["SOHO_AGENT"] = "worker"
	oldLock, oldPending := copiesReleaseLock, copiesReleasePending
	t.Cleanup(func() { copiesReleaseLock, copiesReleasePending = oldLock, oldPending })
	copiesReleaseLock = func(sd string) (func(), error) {
		if err := os.Remove(alias); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(realB, alias); err != nil {
			t.Fatal(err)
		}
		return oldLock(sd)
	}
	copiesReleasePending = func(string, string, platform.Env) ([]core.CopyRow, error) {
		return []core.CopyRow{{Path: copyB, Owner: "other", Pane: "p-other", Created: "2026-01-01T00:00:00Z", Source: "-", Origin: "add"}}, nil
	}
	code, out, stderr := f.run(t, f.env, "copies", "release", filepath.Join(alias, "copy"))
	if code != 0 || !strings.Contains(out, "removed "+copyA) {
		t.Fatalf("code=%d out=%q stderr=%q; want the copy resolved before the lock (%s) removed", code, out, stderr, copyA)
	}
	if info, err := os.Lstat(copyB); err != nil || !info.IsDir() {
		t.Fatalf("the copy with a queued registration was touched: %v", err)
	}
}

// A trailing separator does not turn a symlink into its target: Lstat on
// "link/" follows the link, so release must still refuse it and add must
// still refuse to register it (SOH-464 R3 F1).
func TestCopiesFinalSymlinkWithTrailingSeparator(t *testing.T) {
	f := newCopiesFixture(t, []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: agentIdleJSON}})
	f.writeRoster(t, copiesRosterRow("worker", "p-worker"))
	target := filepath.Join(f.tmp, "slash-target")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(f.tmp, "slash-link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	sep := string(filepath.Separator)
	for _, typed := range []string{link + sep, link + sep + sep} {
		code, out, stderr := f.run(t, f.env, "copies", "add", typed)
		if code != 2 || !strings.Contains(stderr, "is a symlink") || out != "" {
			t.Fatalf("add %q: code=%d out=%q stderr=%q; want the symlink refused", typed, code, out, stderr)
		}
	}
	if rows := f.copiesRows(t); len(rows) != 0 {
		t.Fatalf("rows=%#v; want nothing registered", rows)
	}
	f.writeCopies(t, []core.CopyRow{{Path: target, Owner: "worker", Pane: "p-worker", Created: "2026-01-01T00:00:00Z", Source: "-", Origin: "add"}})
	f.env["SOHO_AGENT"] = "worker"
	for _, typed := range []string{link + sep, link + sep + sep} {
		code, out, stderr := f.run(t, f.env, "copies", "release", typed)
		if code != 2 || !strings.Contains(stderr, "not registered") {
			t.Fatalf("release %q: code=%d out=%q stderr=%q; want it refused as not registered", typed, code, out, stderr)
		}
	}
	if info, err := os.Lstat(target); err != nil || !info.IsDir() {
		t.Fatalf("the link target was touched: %v", err)
	}
	if rows := f.copiesRows(t); len(rows) != 1 || rows[0].Path != target {
		t.Fatalf("rows=%#v; want the target row kept", rows)
	}
}
