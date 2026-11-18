package cli

import (
	"bytes"
	"fmt"
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

func TestGcNestedRegisteredRemovedWithAncestor(t *testing.T) {
	f := gcGuardFixture(t)
	outer := filepath.Join(f.tmp, "outer")
	inner := filepath.Join(outer, "inner")
	if err := os.MkdirAll(inner, 0700); err != nil {
		t.Fatal(err)
	}
	created := core.FrictionISO(platform.Now().Add(-3 * time.Hour))
	f.writeCopies(t, []core.CopyRow{
		{Path: outer, Owner: "ghost", Pane: "-", Created: created, Source: "-", Origin: "add"},
		{Path: inner, Owner: "ghost", Pane: "-", Created: created, Source: "-", Origin: "add"},
	})
	code, out, stderr := f.run(t, f.env, "gc")
	if code != 0 || stderr != "" || !strings.Contains(out, "would remove "+outer+"  0 B  ghost  3h\n") || !strings.Contains(out, "would remove "+inner+"  0 B  ghost  3h\n") || strings.Contains(out, "kept:") {
		t.Fatalf("preview code=%d out=%q stderr=%q", code, out, stderr)
	}
	code, out, stderr = f.run(t, f.env, "gc", "--yes")
	if code != 0 || stderr != "" || !strings.Contains(out, "removed "+inner+" (with "+outer+")\n") || strings.Contains(out, "kept:") {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	if rows := f.copiesRows(t); len(rows) != 0 {
		t.Fatalf("registry rows=%v", rows)
	}
}

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
	want := strings.Join([]string{gcPressureNone, kept, "kept: 1 copy not removed (listed above)"}, "\n") + "\n"
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
		"kept: 1 copy not removed (listed above)",
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
		"kept: 1 copy not removed (listed above)",
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
	want := strings.Join([]string{gcPressureNone, kept, "kept: 1 copy not removed (listed above)"}, "\n") + "\n"
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

func TestGcSummarizesTwoGitCopies(t *testing.T) {
	f := gcGuardFixture(t)
	rows := []core.CopyRow{}
	for _, name := range []string{"git-one", "git-two"} {
		path := filepath.Join(f.tmp, name)
		if err := os.MkdirAll(filepath.Join(path, ".git"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "data"), bytes.Repeat([]byte("x"), 40), 0o600); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, core.CopyRow{Path: path, Owner: "ghost", Pane: "-", Created: core.FrictionISO(platform.Now().Add(-3 * time.Hour)), Source: "-", Origin: "mutation-copy"})
	}
	f.writeCopies(t, rows)
	for _, args := range [][]string{{"gc"}, {"gc", "--yes"}} {
		code, out, errOut := f.run(t, f.env, args...)
		if code != 0 || errOut != "" || !strings.Contains(out, "kept: 2 copies not removed (listed above)\n") || strings.Count(out, "\nkept ") != 2 || strings.Contains(out, "run 'soho gc --yes' to remove them") || strings.Contains(out, "already gone") {
			t.Fatalf("%v: code=%d stdout=%q stderr=%q", args, code, out, errOut)
		}
		for _, row := range rows {
			if _, err := os.Stat(filepath.Join(row.Path, ".git")); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestGcSummarizesNestedGitCopies(t *testing.T) {
	for _, outerGit := range []bool{false, true} {
		for _, yes := range []bool{false, true} {
			t.Run(fmt.Sprintf("outerGit=%t/yes=%t", outerGit, yes), func(t *testing.T) {
				f := gcGuardFixture(t)
				outer := filepath.Join(f.tmp, "outer")
				inner := filepath.Join(outer, "inner")
				if err := os.MkdirAll(filepath.Join(inner, ".git"), 0o700); err != nil {
					t.Fatal(err)
				}
				if outerGit {
					if err := os.MkdirAll(filepath.Join(outer, ".git"), 0o700); err != nil {
						t.Fatal(err)
					}
				}
				for _, path := range []string{outer, inner} {
					if err := os.WriteFile(filepath.Join(path, "data"), bytes.Repeat([]byte("x"), 40), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				rows := []core.CopyRow{}
				for _, path := range []string{outer, inner} {
					rows = append(rows, core.CopyRow{Path: path, Owner: "ghost", Pane: "-", Created: core.FrictionISO(platform.Now().Add(-3 * time.Hour)), Source: "-", Origin: "mutation-copy"})
				}
				f.writeCopies(t, rows)
				args := []string{"gc"}
				if yes {
					args = append(args, "--yes")
				}
				code, out, errOut := f.run(t, f.env, args...)
				if code != 0 || errOut != "" {
					t.Fatalf("code=%d stdout=%q stderr=%q", code, out, errOut)
				}
				if outerGit && (!strings.Contains(out, "kept: 2 copies not removed (listed above)\n") || strings.Count(out, "\nkept ") != 2) {
					t.Fatalf("stdout=%q", out)
				}
				if !outerGit && !yes && !strings.Contains(out, "kept: 1 copy not removed (listed above)\n") {
					t.Fatalf("stdout=%q", out)
				}
				// Parent first: the child and its registry row leave with it.
				if !outerGit && yes && (!strings.Contains(out, "\nremoved "+outer+" ") || !strings.Contains(out, "\nremoved "+inner+" (with "+outer+")\n") || strings.Contains(out, "\nkept ") || strings.Contains(out, "kept:") || len(f.copiesRows(t)) != 0) {
					t.Fatalf("stdout=%q", out)
				}
				if yes {
					_, err := os.Stat(inner)
					if outerGit && err != nil || !outerGit && !os.IsNotExist(err) {
						t.Fatalf("inner stat: %v", err)
					}
				}
			})
		}
	}
}

func TestGcPreviewKeptParentExcludesRemovedChild(t *testing.T) {
	f := gcGuardFixture(t)
	outer := filepath.Join(f.tmp, "outer")
	inner := filepath.Join(outer, "inner")
	if err := os.MkdirAll(filepath.Join(outer, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(inner, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{outer, inner} {
		if err := os.WriteFile(filepath.Join(path, "data"), bytes.Repeat([]byte("x"), 40), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	rows := []core.CopyRow{}
	for _, path := range []string{outer, inner} {
		rows = append(rows, core.CopyRow{Path: path, Owner: "ghost", Pane: "-", Created: core.FrictionISO(platform.Now().Add(-3 * time.Hour)), Source: "-", Origin: "mutation-copy"})
	}
	f.writeCopies(t, rows)
	for _, args := range [][]string{{"gc"}, {"gc", "--yes"}} {
		code, out, errOut := f.run(t, f.env, args...)
		if code != 0 || errOut != "" || !strings.Contains(out, "kept: 1 copy not removed (listed above)\n") {
			t.Fatalf("args=%v code=%d stdout=%q stderr=%q", args, code, out, errOut)
		}
	}
}

func TestGcResolvedDescendantSurvivesScanRootSymlink(t *testing.T) {
	f := gcGuardFixture(t)
	outer := filepath.Join(f.tmp, "outer")
	scan := filepath.Join(outer, "scan")
	inner := filepath.Join(scan, "inner")
	moved := filepath.Join(f.tmp, "moved")
	if err := os.MkdirAll(inner, 0700); err != nil {
		t.Fatal(err)
	}
	created := core.FrictionISO(platform.Now().Add(-3 * time.Hour))
	f.writeCopies(t, []core.CopyRow{{Path: outer, Owner: "ghost", Created: created}, {Path: inner, Owner: "ghost", Created: created}})
	if err := os.Rename(scan, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, scan); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	f.env["SOHO_COPIES_DIR"] = scan
	code, out, stderr := f.run(t, f.env, "gc", "--yes")
	if code != 0 || strings.Contains(out, "removed "+inner+" (with ") {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	if _, err := os.Stat(filepath.Join(moved, "inner")); err != nil {
		t.Fatal(err)
	}
	rows := f.copiesRows(t)
	if len(rows) != 1 || rows[0].Path != inner {
		t.Fatalf("rows=%#v out=%q stderr=%q", rows, out, stderr)
	}
}

func TestGcStagedDescendantReregisteredBeforeBatch(t *testing.T) {
	for _, scenario := range []string{"new-row", "new-owner", "recreated", "reregistered"} {
		t.Run(scenario, func(t *testing.T) {
			f := gcGuardFixture(t)
			outer := filepath.Join(f.tmp, "outer")
			inner := filepath.Join(outer, "inner")
			if err := os.MkdirAll(inner, 0700); err != nil {
				t.Fatal(err)
			}
			old := core.CopyRow{Path: inner, Owner: "ghost", Created: core.FrictionISO(platform.Now().Add(-3 * time.Hour))}
			f.writeCopies(t, []core.CopyRow{{Path: outer, Owner: "ghost", Created: old.Created}, old})
			gcBeforeBatchHook = func() {
				row := old
				if scenario == "recreated" || scenario == "reregistered" {
					if err := os.MkdirAll(inner, 0700); err != nil {
						t.Fatal(err)
					}
				}
				if scenario == "new-owner" {
					row.Owner = "replacement"
				}
				if scenario == "new-row" || scenario == "reregistered" {
					row.Created = core.FrictionISO(platform.Now())
				}
				if err := core.UpsertCopies(f.stateDir(t), row); err != nil {
					t.Fatal(err)
				}
			}
			t.Cleanup(func() { gcBeforeBatchHook = nil })
			code, out, stderr := f.run(t, f.env, "gc", "--yes")
			if code != 0 || !strings.Contains(out, "removed "+inner+" (with "+outer+")") {
				t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
			}
			rows := f.copiesRows(t)
			if len(rows) != 1 || rows[0].Path != inner {
				t.Fatalf("rows=%#v", rows)
			}
			if (scenario == "new-row" || scenario == "reregistered") && rows[0].Created == old.Created {
				t.Fatalf("replacement row lost: %#v", rows[0])
			}
			if scenario == "new-owner" && rows[0].Owner != "replacement" {
				t.Fatalf("replacement owner lost: %#v", rows[0])
			}
			if scenario == "recreated" || scenario == "reregistered" {
				if _, err := os.Stat(inner); err != nil {
					t.Fatalf("recreated child missing: %v", err)
				}
			}
		})
	}
}

func TestGcIdleNestedAttribution(t *testing.T) {
	g := newIdleGc(t, idleRules("idle"))
	child := g.row
	child.Path = filepath.Join(g.copy, "sub")
	g.f.writeCopies(t, []core.CopyRow{g.row, child})
	code, out, stderr := g.f.run(t, g.f.env, "gc", "--yes")
	if code != 0 || stderr != "" || !strings.Contains(out, "removed "+child.Path+" (with "+g.copy+")\n") || strings.Contains(out, "kept:") || strings.Count(out, "freed: 10 B") != 1 {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	for _, path := range []string{g.copy, child.Path} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("path=%s err=%v", path, err)
		}
	}
	if rows := g.f.copiesRows(t); len(rows) != 0 {
		t.Fatalf("rows=%#v", rows)
	}
}
