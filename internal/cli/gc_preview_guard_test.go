package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

// gcKeptGuardSuffixText is the end DJA-662 gives every kept line whose
// cause is the path guard's, in the dry run and in --yes alike.
const gcKeptGuardSuffixText = "; gc never removes it: delete it by hand if it is yours"

// gcGuardFixture is a copies fixture without a roster (every registered
// copy is an orphan), with the s85 measurements faked the same way
// gcFixture fakes them, so the goldens stay hermetic.
func gcGuardFixture(t *testing.T) *copiesFixture {
	t.Helper()
	f := newCopiesFixture(t, nil)
	f.stateDir(t)
	oldDisk, oldSwap := platform.DiskFree, platform.SwapUsage
	platform.DiskFree = func(string) (int64, int64, bool) {
		return int64(60) * 1024 * 1024 * 1024, int64(100) * 1024 * 1024 * 1024, true
	}
	platform.SwapUsage = func(platform.Env) (int64, int64, bool) {
		return int64(30) * 1024 * 1024 * 1024, int64(100) * 1024 * 1024 * 1024, true
	}
	t.Cleanup(func() { platform.DiskFree, platform.SwapUsage = oldDisk, oldSwap })
	return f
}

// DJA-662 scenario 1: a registered copy inside the project is refused by
// the same guard --yes runs, so the dry run says kept, with the end that
// says how to get rid of it, and --yes says the same and does not delete it.
func TestGcPreviewKeepsAProjectInsideCopyLikeYes(t *testing.T) {
	f := gcGuardFixture(t)
	inside := filepath.Join(f.repo, "inside-copy")
	if err := os.MkdirAll(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inside, "a.txt"), bytes.Repeat([]byte("0"), 25), 0o600); err != nil {
		t.Fatal(err)
	}
	threeHoursAgo := platform.Now().Add(-3 * time.Hour)
	f.writeCopies(t, []core.CopyRow{{Path: inside, Owner: "ghost", Pane: "-", Created: core.FrictionISO(threeHoursAgo), Source: "-", Origin: "mutation-copy"}})
	kept := "kept " + inside + " (is the project root or inside it" + gcKeptGuardSuffixText + ")"

	code, out, errOut := f.run(t, f.env, "gc")
	if code != 0 || errOut != "" {
		t.Fatalf("dry run code=%d out=%q err=%q", code, out, errOut)
	}
	want := strings.Join([]string{gcPressureNone, kept}, "\n") + "\n"
	if out != want {
		t.Fatalf("dry run stdout=%q; want\n%s", out, want)
	}
	if _, err := os.Stat(filepath.Join(inside, "a.txt")); err != nil {
		t.Fatalf("the dry run touched the copy: %v", err)
	}

	code, out, errOut = f.run(t, f.env, "gc", "--yes")
	if code != 0 {
		t.Fatalf("--yes code=%d out=%q err=%q", code, out, errOut)
	}
	if !strings.Contains(out, kept+"\n") {
		t.Fatalf("--yes does not print the same kept line:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(inside, "a.txt")); err != nil {
		t.Fatalf("--yes removed the copy inside the project: %v", err)
	}
	if rows := f.copiesRows(t); len(rows) != 1 || rows[0].Path != inside {
		t.Fatalf("the registry line left: %#v", rows)
	}
}

// DJA-662 scenario 2: a copy outside the project passes the guard in both
// modes: the dry run promises it, and --yes removes it.
func TestGcPreviewRemovesAnOutsideCopyLikeYes(t *testing.T) {
	f := gcGuardFixture(t)
	outside := filepath.Join(f.tmp, "old-orphan")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "a.txt"), bytes.Repeat([]byte("0"), 40), 0o600); err != nil {
		t.Fatal(err)
	}
	threeHoursAgo := platform.Now().Add(-3 * time.Hour)
	f.writeCopies(t, []core.CopyRow{{Path: outside, Owner: "ghost", Pane: "-", Created: core.FrictionISO(threeHoursAgo), Source: "-", Origin: "mutation-copy"}})

	code, out, errOut := f.run(t, f.env, "gc")
	if code != 0 || errOut != "" {
		t.Fatalf("dry run code=%d out=%q err=%q", code, out, errOut)
	}
	want := strings.Join([]string{
		gcPressureNone,
		"would remove " + outside + "  40 B  ghost  3h",
		"total: 40 B",
		"run 'soho gc --yes' to remove them",
	}, "\n") + "\n"
	if out != want {
		t.Fatalf("dry run stdout=%q; want\n%s", out, want)
	}

	code, out, errOut = f.run(t, f.env, "gc", "--yes")
	if code != 0 {
		t.Fatalf("--yes code=%d out=%q err=%q", code, out, errOut)
	}
	want = strings.Join([]string{
		gcPressureNone,
		"removed " + outside + "  40 B",
		"freed: 40 B (logical size; see volume free below)",
		gcUnchangedVolume(outside),
	}, "\n") + "\n"
	if out != want {
		t.Fatalf("--yes stdout=%q; want\n%s", out, want)
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("--yes did not remove the outside copy: %v", err)
	}
	if rows := f.copiesRows(t); len(rows) != 0 {
		t.Fatalf("the registry line was not dropped: %#v", rows)
	}
}

// DJA-662 scenario 3: a mixed list — the dry run's total counts only the
// copy --yes would remove, not the one the guard keeps.
func TestGcPreviewTotalCountsOnlyWhatYesRemoves(t *testing.T) {
	f := gcGuardFixture(t)
	outside := filepath.Join(f.tmp, "outside")
	inside := filepath.Join(f.repo, "inside")
	for _, dir := range []string{outside, inside} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(outside, "a.txt"), bytes.Repeat([]byte("0"), 40), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inside, "a.txt"), bytes.Repeat([]byte("0"), 25), 0o600); err != nil {
		t.Fatal(err)
	}
	threeHoursAgo := platform.Now().Add(-3 * time.Hour)
	f.writeCopies(t, []core.CopyRow{
		{Path: outside, Owner: "ghost", Pane: "-", Created: core.FrictionISO(threeHoursAgo), Source: "-", Origin: "mutation-copy"},
		{Path: inside, Owner: "ghost", Pane: "-", Created: core.FrictionISO(threeHoursAgo), Source: "-", Origin: "mutation-copy"},
	})
	kept := "kept " + inside + " (is the project root or inside it" + gcKeptGuardSuffixText + ")"

	code, out, errOut := f.run(t, f.env, "gc")
	if code != 0 || errOut != "" {
		t.Fatalf("dry run code=%d out=%q err=%q", code, out, errOut)
	}
	want := strings.Join([]string{
		gcPressureNone,
		"would remove " + outside + "  40 B  ghost  3h",
		kept,
		"total: 40 B",
		"run 'soho gc --yes' to remove them",
	}, "\n") + "\n"
	if out != want {
		t.Fatalf("dry run stdout=%q; want\n%s", out, want)
	}

	code, out, errOut = f.run(t, f.env, "gc", "--yes")
	if code != 0 {
		t.Fatalf("--yes code=%d out=%q err=%q", code, out, errOut)
	}
	want = strings.Join([]string{
		gcPressureNone,
		"removed " + outside + "  40 B",
		kept,
		"freed: 40 B (logical size; see volume free below)",
		gcUnchangedVolume(outside),
	}, "\n") + "\n"
	if out != want {
		t.Fatalf("--yes stdout=%q; want\n%s", out, want)
	}
	if _, err := os.Stat(inside); err != nil {
		t.Fatalf("--yes removed the copy inside the project: %v", err)
	}
	if rows := f.copiesRows(t); len(rows) != 1 || rows[0].Path != inside {
		t.Fatalf("registry rows=%#v; want the inside line only", rows)
	}
}

// DJA-662 edge case: a different guard reason (contains a .git) gets the
// same kept line with its own reason, in both modes, and the copy stays.
func TestGcKeptLineCarriesTheOwnReason(t *testing.T) {
	f := gcGuardFixture(t)
	withGit := filepath.Join(f.tmp, "with-git")
	if err := os.MkdirAll(filepath.Join(withGit, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(withGit, "a.txt"), bytes.Repeat([]byte("0"), 40), 0o600); err != nil {
		t.Fatal(err)
	}
	threeHoursAgo := platform.Now().Add(-3 * time.Hour)
	f.writeCopies(t, []core.CopyRow{{Path: withGit, Owner: "ghost", Pane: "-", Created: core.FrictionISO(threeHoursAgo), Source: "-", Origin: "mutation-copy"}})
	kept := "kept " + withGit + " (contains a .git (another repository or a worktree)" + gcKeptGuardSuffixText + ")"

	code, out, errOut := f.run(t, f.env, "gc")
	if code != 0 || errOut != "" {
		t.Fatalf("dry run code=%d out=%q err=%q", code, out, errOut)
	}
	want := strings.Join([]string{gcPressureNone, kept}, "\n") + "\n"
	if out != want {
		t.Fatalf("dry run stdout=%q; want\n%s", out, want)
	}

	code, out, errOut = f.run(t, f.env, "gc", "--yes")
	if code != 0 {
		t.Fatalf("--yes code=%d out=%q err=%q", code, out, errOut)
	}
	if !strings.Contains(out, kept+"\n") {
		t.Fatalf("--yes does not print the same kept line:\n%s", out)
	}
	for _, path := range []string{filepath.Join(withGit, "a.txt"), filepath.Join(withGit, ".git")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("--yes touched %s: %v", path, err)
		}
	}
}

// DJA-662 decision 5: the gc preview runs the same snapshot validation the
// removal runs — a snapshot it refuses is a kept line with its own reason,
// not a would remove; the valid one is still promised.
func TestGcPreviewKeepsARefusedSnapshotLikeYes(t *testing.T) {
	t.Run("a valid snapshot is still promised", func(t *testing.T) {
		f := newSnapshotFixture(t)
		obj := f.freeze(t, "worker")
		if err := os.WriteFile(filepath.Join(f.sd, "agents.tsv"), []byte("# header\n"), 0600); err != nil {
			t.Fatal(err)
		}
		code, out, stderr := f.run(t, "gc")
		if code != 0 {
			t.Fatalf("gc code=%d out=%s err=%s", code, out, stderr)
		}
		if !strings.Contains(out, "would remove snapshot "+obj["snapshot"]+"\n") {
			t.Fatalf("the valid snapshot is no longer promised:\n%s", out)
		}
		for _, p := range []string{obj["snapshot"], obj["patch"]} {
			if _, err := os.Stat(p); err != nil {
				t.Fatalf("the dry run touched %s: %v", p, err)
			}
		}
	})
	t.Run("a refused snapshot is a kept line, not a promise", func(t *testing.T) {
		f := newSnapshotFixture(t)
		head := strings.TrimSpace(fixtureGit(t, f.mutationCopyFixture, f.source, "rev-parse", "HEAD"))
		outside := filepath.Join(f.root, "foreign", ".worktrees", "snap-outside-"+head[:7])
		fixtureGit(t, f.mutationCopyFixture, f.source, "worktree", "add", "--detach", outside, head)
		patch := filepath.Join(f.sd, "snapshots", "outside-"+head[:7]+".patch")
		if err := os.MkdirAll(filepath.Dir(patch), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(patch, []byte("keep patch"), 0600); err != nil {
			t.Fatal(err)
		}
		row := "gone\t" + outside + "\t" + head + "\t" + patch + "\t2026-10-01T12:00:00Z\n"
		if err := os.WriteFile(filepath.Join(f.sd, "snapshots.tsv"), []byte("owner\tworktree\tsha\tpatch\tcreated\n"+row), 0600); err != nil {
			t.Fatal(err)
		}
		code, out, stderr := f.run(t, "gc")
		if code != 0 {
			t.Fatalf("gc code=%d out=%s err=%s", code, out, stderr)
		}
		if strings.Contains(out, "would remove snapshot "+outside) {
			t.Fatalf("the preview promises a snapshot --yes keeps:\n%s", out)
		}
		if !strings.Contains(out, "kept snapshot "+outside+" (outside snapshot prefix)\n") {
			t.Fatalf("missing the kept line with the validation reason:\n%s", out)
		}
		code, out, stderr = f.run(t, "gc", "--yes")
		if code != 0 || !strings.Contains(stderr, "soho: kept snapshot "+outside+": outside snapshot prefix") {
			t.Fatalf("--yes code=%d out=%s err=%s", code, out, stderr)
		}
		if _, err := os.Stat(outside); err != nil {
			t.Fatalf("--yes removed the refused snapshot: %v", err)
		}
		if data, err := os.ReadFile(patch); err != nil || string(data) != "keep patch" {
			t.Fatalf("--yes touched the refused snapshot's patch: %q %v", data, err)
		}
	})
}
