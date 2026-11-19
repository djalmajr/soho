package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

const (
	gcInUseOne   = "in use: 1 copy held by working agents (soho copies lists them)"
	gcInUseTwo   = "in use: 2 copies held by working agents (soho copies lists them)"
	gcNothingDry = "nothing to remove"
)

// gcInUseSecondRow registers a second, currently used copy of the same
// working owner, beside the one newIdleGc built.
func gcInUseSecondRow(t *testing.T, g *idleGc, name string) core.CopyRow {
	t.Helper()
	path := filepath.Join(g.f.tmp, name)
	old := platform.Now().Add(-3 * time.Hour)
	writeFileAt(t, filepath.Join(path, "active.txt"), "OPEN TASK", old)
	return core.CopyRow{
		Path: path, Owner: "worker", Pane: "p-worker",
		Created: core.FrictionISO(old), Source: "-", Origin: "mutation-copy",
		Task: g.row.Task, Report: g.row.Report,
	}
}

func gcInUseWant(t *testing.T, code int, out, errOut, want string) {
	t.Helper()
	if code != 0 || errOut != "" || out != want {
		t.Fatalf("code=%d out=%q err=%q; want\n%s", code, out, errOut, want)
	}
}

// Two owned rows of a working owner are both counted in preview and in
// --yes, and --yes removes neither.
func TestGcInUseTwoWorkingOwnerCopies(t *testing.T) {
	g := newIdleGc(t, idleRules("working"))
	second := gcInUseSecondRow(t, g, "second-working-copy")
	g.f.writeCopies(t, []core.CopyRow{g.row, second})
	before := g.f.copiesRows(t)

	code, out, errOut := g.f.run(t, g.f.env, "gc")
	gcInUseWant(t, code, out, errOut, strings.Join([]string{gcPressureNone, gcNothingDry, gcInUseTwo}, "\n")+"\n")

	code, out, errOut = g.f.run(t, g.f.env, "gc", "--yes")
	gcInUseWant(t, code, out, errOut, strings.Join([]string{gcPressureNone, gcInUseTwo, gcNothingDry}, "\n")+"\n")
	if raw, err := os.ReadFile(filepath.Join(g.copy, "sub", "a.txt")); err != nil || string(raw) != "0123456789" {
		t.Fatalf("the first copy was touched: %q err=%v", raw, err)
	}
	if raw, err := os.ReadFile(filepath.Join(second.Path, "active.txt")); err != nil || string(raw) != "OPEN TASK" {
		t.Fatalf("the second copy was touched: %q err=%v", raw, err)
	}
	if rows := g.f.copiesRows(t); len(rows) != len(before) {
		t.Fatalf("registry rows=%#v; want %#v", rows, before)
	} else {
		for i := range rows {
			if rows[i] != before[i] {
				t.Fatalf("registry row %d=%#v; want %#v", i, rows[i], before[i])
			}
		}
	}
}

// One such row: singular noun, same stable position in both modes.
func TestGcInUseOneWorkingOwnerCopy(t *testing.T) {
	g := newIdleGc(t, idleRules("working"))
	g.register(t)

	code, out, errOut := g.f.run(t, g.f.env, "gc")
	gcInUseWant(t, code, out, errOut, strings.Join([]string{gcPressureNone, gcNothingDry, gcInUseOne}, "\n")+"\n")
	code, out, errOut = g.f.run(t, g.f.env, "gc", "--yes")
	gcInUseWant(t, code, out, errOut, strings.Join([]string{gcPressureNone, gcInUseOne, gcNothingDry}, "\n")+"\n")
	if rows := g.f.copiesRows(t); len(rows) != 1 || rows[0] != g.row {
		t.Fatalf("registry rows=%#v; want the line kept", rows)
	}
}

// R573: a working owner's copy is named even when the idle check returns
// before reading the owner — a copy younger than --older-than, or a row
// without task columns.
func TestGcInUseWorkingOwnerBeforeIdleRead(t *testing.T) {
	for name, edit := range map[string]func(*idleGc){
		"younger than older-than": func(g *idleGc) { g.row.Created = core.FrictionISO(platform.Now().Add(-1 * time.Hour)) },
		"no task columns":         func(g *idleGc) { g.row.Task, g.row.Report = "-", "-" },
	} {
		t.Run(name, func(t *testing.T) {
			g := newIdleGc(t, idleRules("working"))
			edit(g)
			g.register(t)
			code, out, errOut := g.f.run(t, g.f.env, "gc")
			gcInUseWant(t, code, out, errOut, strings.Join([]string{gcPressureNone, gcNothingDry, gcInUseOne}, "\n")+"\n")
			code, out, errOut = g.f.run(t, g.f.env, "gc", "--yes")
			gcInUseWant(t, code, out, errOut, strings.Join([]string{gcPressureNone, gcInUseOne, gcNothingDry}, "\n")+"\n")
			if rows := g.f.copiesRows(t); len(rows) != 1 || rows[0] != g.row {
				t.Fatalf("registry rows=%#v; want the line kept", rows)
			}
		})
	}
}

// No working owner rows: the whole output stays byte-identical to main's
// for the same orphan fixture, in preview and in --yes.
func TestGcInUseAbsentLeavesOutputUnchanged(t *testing.T) {
	f := gcGuardFixture(t)
	outside := filepath.Join(f.tmp, "in-use-none")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "a.txt"), []byte("0123456789012345678901234567890123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := platform.Now().Add(-3 * time.Hour)
	f.writeCopies(t, []core.CopyRow{{Path: outside, Owner: "ghost", Pane: "-", Created: core.FrictionISO(old), Source: "-", Origin: "mutation-copy"}})

	code, out, errOut := f.run(t, f.env, "gc")
	want := strings.Join([]string{
		gcPressureNone,
		"would remove " + outside + "  40 B  ghost  3h",
		"total: 40 B",
		"run 'soho gc --yes' to remove them",
	}, "\n") + "\n"
	gcInUseWant(t, code, out, errOut, want)

	code, out, errOut = f.run(t, f.env, "gc", "--yes")
	want = strings.Join([]string{
		gcPressureNone,
		"removed " + outside + "  40 B",
		"freed: 40 B (logical size; see volume free below)",
		gcUnchangedVolume(outside),
	}, "\n") + "\n"
	gcInUseWant(t, code, out, errOut, want)
}

// An idle owner's copy that gc offers is a candidate, not an in-use row.
func TestGcInUseIdleOwnerCopyNotCounted(t *testing.T) {
	g := newIdleGc(t, idleRules("idle"))
	g.register(t)
	code, out, errOut := g.f.run(t, g.f.env, "gc")
	want := strings.Join([]string{
		gcPressureNone,
		"would remove " + g.copy + "  10 B  worker  3h  (idle owner, task closed)",
		"total: 10 B",
		"run 'soho gc --yes' to remove them",
	}, "\n") + "\n"
	gcInUseWant(t, code, out, errOut, want)
}

// Boundary: an owned row whose path is already gone is still classified
// "missing" by copyRowState, so it stays a candidate and is not counted here.
func TestGcInUseMissingPathRowStaysAMissingCandidate(t *testing.T) {
	g := newIdleGc(t, idleRules("working"))
	g.register(t)
	if err := os.RemoveAll(g.copy); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := g.f.run(t, g.f.env, "gc")
	want := strings.Join([]string{
		gcPressureNone,
		"would drop the line of " + g.copy + " (already gone from disk)",
		"run 'soho gc --yes' to remove them",
	}, "\n") + "\n"
	gcInUseWant(t, code, out, errOut, want)
}
