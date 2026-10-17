package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// SOHO_NOWRITE read contract (the plugin's reads): every accepted
// invocation runs read-only — no file or directory in the project or the
// state tree is created or changed (paths, sizes and mtimes), no friction
// line is recorded, and a state dir that does not exist appears empty.

const (
	nowriteTotalBytes = int64(100) * 1024 * 1024 * 1024
	nowriteFreeBytes  = int64(60) * 1024 * 1024 * 1024
	nowriteSwapBytes  = int64(30) * 1024 * 1024 * 1024
)

const nowriteRosterHeader = "NAME                 ROLE               KIND     PANE     TAB              STATE     REPORT           CWD TASK\n"
const nowriteRosterTail = "\n# other live agents in workspace ws (not spawned by this skill)\n\nlayout=split reuse_workers=on multi_role=on auto_approve=off\n"

const nowriteRosterRow = "worker               implementer        grok     p-worker -                gone      ready            /tmp/work -\n"

const nowriteExplainIdle = "soho runs a small team of agents in Herdr panels. You stay in this panel and lead. Each other panel is one agent with one job: writing code (research included) or reviewing. Those agents never commit or push. You can watch a panel or close it. Each assistant spends the quota of its own account. Nothing is running yet. To start, describe the work here. The first time, you are asked how many panels to open and which assistant each job should use, and nothing opens until you agree. Four panels are recommended: two write code in parallel and one reviews; that uses more quota. Three panels are lighter: one writes and one reviews. With two panels one writes and the review happens here.\n"

const nowriteExplainRunning = "Panels: 4.\nbuild: implementer, grok, model grok-4.7, idle\nreview: not started\n\nRecommendation: 4 panels - two write code (research included) in parallel and one reviews. Uses more quota. 3 panels are lighter: one writes and one reviews. With 2 panels one writes and the review happens here.\n"

const nowriteGcNoneLine = "resource pressure: none (disk 60% free, swap 30% used)\n"

type nowriteTreeEntry struct {
	size int64
	mod  time.Time
}

// nowriteReads is a copiesFixture plus the fake herdr rules and the fake s85
// measurements; withState seeds the sample state (roster, report, friction
// log, copy registry, task-report pointer, an old mutation copy under
// TMPDIR).
type nowriteReads struct {
	f        *copiesFixture
	sd       string
	report   string
	orphan   string
	owned    string
	mutation string
}

// fakeNowriteMeasurements fakes the s85 measurements (platform.DiskFree /
// platform.SwapUsage) at values away from the limits, so the read tests see
// the "none" pressure line.
func fakeNowriteMeasurements(t *testing.T) {
	t.Helper()
	oldDisk, oldSwap := platform.DiskFree, platform.SwapUsage
	platform.DiskFree = func(string) (int64, int64, bool) { return nowriteFreeBytes, nowriteTotalBytes, true }
	platform.SwapUsage = func(platform.Env) (int64, int64, bool) { return nowriteSwapBytes, nowriteTotalBytes, true }
	t.Cleanup(func() {
		platform.DiskFree, platform.SwapUsage = oldDisk, oldSwap
	})
}

func newNowriteReads(t *testing.T, withState bool) *nowriteReads {
	t.Helper()
	rules := []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: agentIdleJSON, Code: 0},
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`, Code: 0},
		{Argv: []string{"pane", "list", "--workspace", "ws"}, Stdout: `{"result":{"panes":[]}}`, Code: 0},
		{Argv: []string{"tab", "list", "--workspace", "ws"}, Stdout: `{"result":{"tabs":[]}}`, Code: 0},
		{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped", "--lines", "120"}, Stdout: "recent output line\n", Code: 0},
		{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped", "--lines", "5"}, Stdout: "recent output line\n", Code: 0},
	}
	nr := &nowriteReads{f: newCopiesFixture(t, rules)}
	fakeNowriteMeasurements(t)
	if !withState {
		return nr
	}
	nr.sd = nr.f.stateDir(t)
	nr.f.writeRoster(t, copiesRosterRow("worker", "p-worker"))
	nr.report = filepath.Join(nr.sd, "reports", "build-worker-20260101T000000.md")
	if err := os.MkdirAll(filepath.Dir(nr.report), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nr.report, []byte("report body line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nr.sd, "last-report-worker"), []byte(nr.report+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nr.sd, "friction.log"), []byte(
		"2026-01-01T10:00:00Z\terror(exit 4)\tspawn\tworker spawn failed\n"+
			"2026-01-01T11:00:00Z\twarning\tfriction\ta warning note\n"+
			"2026-01-01T12:00:00Z\terror(exit 2)\tcollect\tno report file yet for 'worker'\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	nr.orphan = filepath.Join(nr.f.root, "orphan-copy")
	nr.owned = filepath.Join(nr.f.root, "owned-copy")
	for _, dir := range []string{nr.orphan, nr.owned} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(nr.orphan, "big.bin"), make([]byte, 2048), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nr.owned, "small.bin"), make([]byte, 1024), 0o600); err != nil {
		t.Fatal(err)
	}
	nr.f.writeCopies(t, []core.CopyRow{
		{Path: nr.orphan, Owner: "ghost", Pane: "p-x", Created: time.Now().Add(-26 * time.Hour).UTC().Format(copiesTimeLayout), Source: "s", Origin: "add"},
		{Path: nr.owned, Owner: "worker", Pane: "p-worker", Created: time.Now().Add(-5 * time.Hour).UTC().Format(copiesTimeLayout), Source: "s", Origin: "add"},
	})
	// A valid task-report pointer: without the NOWRITE guard, collect would
	// run SyncTaskReport and rewrite the stable file below the state dir.
	stable := filepath.Join(nr.sd, "task-stable-worker.md")
	pointer := `{"version":1,"task_report":"` + stable + `","current":"` + nr.report + `","history":["` + nr.report + `"]}`
	if err := os.WriteFile(filepath.Join(nr.sd, "task-report-worker.json"), []byte(pointer), 0o600); err != nil {
		t.Fatal(err)
	}
	nr.mutation = filepath.Join(nr.f.tmp, "soho-mutation-zz")
	if err := os.MkdirAll(nr.mutation, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nr.mutation, "f.txt"), make([]byte, 100), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(nr.mutation, old, old); err != nil {
		t.Fatal(err)
	}
	return nr
}

// nowriteTreeSnapshot records (path, size, mtime) of every entry below root.
func nowriteTreeSnapshot(t *testing.T, root string) map[string]nowriteTreeEntry {
	t.Helper()
	snap := map[string]nowriteTreeEntry{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		snap[path] = nowriteTreeEntry{size: info.Size(), mod: info.ModTime()}
		return nil
	})
	if err != nil {
		t.Fatalf("NOWRITE snapshot of %s: %v", root, err)
	}
	return snap
}

// runNowrite runs argv with SOHO_NOWRITE=1 and proves the whole fixture
// tree (paths, sizes, mtimes) is identical before and after.
func (nr *nowriteReads) runNowrite(t *testing.T, argv ...string) (int, string, string) {
	t.Helper()
	before := nowriteTreeSnapshot(t, nr.f.root)
	env := platform.Env{}
	for k, v := range nr.f.env {
		env[k] = v
	}
	env["SOHO_NOWRITE"] = "1"
	code, out, errOut := nr.f.run(t, env, argv...)
	after := nowriteTreeSnapshot(t, nr.f.root)
	if !reflect.DeepEqual(before, after) {
		changed := []string{}
		for path, entry := range after {
			if prev, ok := before[path]; !ok || prev != entry {
				changed = append(changed, path)
			}
		}
		for path := range before {
			if _, ok := after[path]; !ok {
				changed = append(changed, path+" (removed)")
			}
		}
		t.Fatalf("%v under SOHO_NOWRITE=1 changed the tree: %v", argv, changed)
	}
	return code, out, errOut
}

type nowriteReadCase struct {
	name    string
	argv    []string
	code    int
	wantOut string
	wantErr string
	// skipOut skips the exact stdout check (doctor's advisory output names
	// fakecli internals); the snapshot still proves the read wrote nothing.
	skipOut bool
}

// TestNowriteReadCommandsWriteNothing runs every accepted NOWRITE read in a
// project without a state dir and in one with the sample state; each run must
// print the expected output and leave the tree untouched.
func TestNowriteReadCommandsWriteNothing(t *testing.T) {
	for _, withState := range []bool{false, true} {
		flavor := "without state dir"
		if withState {
			flavor = "with sample state dir"
		}
		t.Run(flavor, func(t *testing.T) {
			nr := newNowriteReads(t, withState)
			sd := filepath.Join(nr.f.state, "ws")
			frictionLog := filepath.Join(sd, "friction.log")
			cases := []nowriteReadCase{
				{name: "doctor", argv: []string{"doctor"}, code: 0, skipOut: true},
				{name: "roster", argv: []string{"roster"}, code: 0},
				{name: "explain", argv: []string{"explain"}, code: 0},
				{name: "friction", argv: []string{"friction"}, code: 0},
				{name: "friction --level error", argv: []string{"friction", "--level", "error"}, code: 0},
				{name: "friction --summary", argv: []string{"friction", "--summary"}, code: 0},
				{name: "collect worker", argv: []string{"collect", "worker"}, code: 0},
				{name: "copies", argv: []string{"copies"}, code: 0},
				{name: "gc", argv: []string{"gc"}, code: 0},
				{name: "gc --older-than 1 --include-unregistered", argv: []string{"gc", "--older-than", "1", "--include-unregistered"}, code: 0},
			}
			if withState {
				cases[1].wantOut = nowriteRosterHeader + nowriteRosterRow + nowriteRosterTail
				cases[2].wantOut = nowriteExplainRunning
				cases[3].wantOut = fmt.Sprintf("friction log (%s): timestamp, level, command, message\n"+
					"2026-01-01T10:00:00Z\terror(exit 4)\tspawn\tworker spawn failed\n"+
					"2026-01-01T11:00:00Z\twarning\tfriction\ta warning note\n"+
					"2026-01-01T12:00:00Z\terror(exit 2)\tcollect\tno report file yet for 'worker'\n", frictionLog)
				cases[4].wantOut = fmt.Sprintf("friction log (%s): timestamp, level, command, message\n"+
					"2026-01-01T10:00:00Z\terror(exit 4)\tspawn\tworker spawn failed\n"+
					"2026-01-01T12:00:00Z\terror(exit 2)\tcollect\tno report file yet for 'worker'\n", frictionLog)
				cases[5].wantOut = "count  level  command\n1  error(exit 2)  collect\n1  error(exit 4)  spawn\n1  warning  friction\n"
				cases[6].wantOut = fmt.Sprintf("<!-- report: %s -->\nreport body line\n", nr.report)
				cases[7].wantOut = fmt.Sprintf("%s  ghost  1d  orphan\n%s  worker  5h  owned\n", nr.orphan, nr.owned)
				cases[8].wantOut = nowriteGcNoneLine +
					fmt.Sprintf("would remove %s  2.0 KB  ghost  1d\ntotal: 2.0 KB\nrun 'soho gc --yes' to remove them\n", nr.orphan) +
					"unregistered mutation copies (another project or an older version may own them):\n" +
					fmt.Sprintf("%s  100 B  3h\n", nr.mutation)
				cases[9].wantOut = cases[8].wantOut
			} else {
				cases[1].wantOut = nowriteRosterHeader + nowriteRosterTail
				cases[2].wantOut = nowriteExplainIdle
				cases[3].wantOut = fmt.Sprintf("no friction recorded under %s\n", frictionLog)
				cases[4].wantOut = fmt.Sprintf("no friction matches under %s\n", frictionLog)
				cases[5].wantOut = fmt.Sprintf("no friction matches under %s\n", frictionLog)
				cases[6].code = 6
				cases[6].wantOut = "recent output line\n"
				cases[6].wantErr = "soho: warning: no report file yet for 'worker' (expected <none dispatched>); falling back to recent terminal output\n"
				cases[7].wantOut = "no registered copies\n"
				cases[8].wantOut = nowriteGcNoneLine + "nothing to remove\n"
				cases[9].wantOut = cases[8].wantOut
			}
			for _, c := range cases {
				t.Run(c.name, func(t *testing.T) {
					code, out, errOut := nr.runNowrite(t, c.argv...)
					if code != c.code {
						t.Fatalf("code=%d want %d; out=%q err=%q", code, c.code, out, errOut)
					}
					if !c.skipOut && out != c.wantOut {
						t.Fatalf("stdout:\n got:  %q\nwant: %q", out, c.wantOut)
					}
					if errOut != c.wantErr {
						t.Fatalf("stderr:\n got:  %q\nwant: %q", errOut, c.wantErr)
					}
				})
			}
		})
	}
}

// TestNowriteRejectsWritingInvocations keeps every writing invocation refused
// with exit 2 and the new message that lists the accepted reads, before any
// command runs (nothing is written).
func TestNowriteRejectsWritingInvocations(t *testing.T) {
	nr := newNowriteReads(t, true)
	const messagePrefix = "soho: soho: SOHO_NOWRITE=1 is read-only: only these invocations run (the plugin's reads): the exact 'doctor', 'roster [--scope workspace|server] [--json]', 'explain', 'config [get <key>]', 'roles [--json]', 'friction' with the read options --since, --level, --command, --agent, --summary, 'collect <agent> [--lines N] [--verify]', 'copies', 'procs', 'status [agents...]', 'gc' without --yes, 'collaborate status <assignment> [--json]', 'capabilities --json'; rejected: "
	const messageTail = " — unset SOHO_NOWRITE to write\n"
	cases := []struct {
		name     string
		argv     []string
		rejected string
	}{
		{name: "friction add", argv: []string{"friction", "add", "x"}, rejected: "friction (2 extra arguments not allowed)"},
		{name: "copies add", argv: []string{"copies", "add", nr.f.repo}, rejected: "copies (2 extra arguments not allowed)"},
		{name: "gc --yes", argv: []string{"gc", "--yes"}, rejected: "gc (1 extra argument not allowed)"},
		{name: "release", argv: []string{"release", "worker"}, rejected: "release (1 extra argument not allowed)"},
		{name: "explain extra", argv: []string{"explain", "extra"}, rejected: "explain (1 extra argument not allowed)"},
		{name: "config set panes 3", argv: []string{"config", "set", "panes", "3"}, rejected: "config (3 extra arguments not allowed)"},
		{name: "config --project", argv: []string{"config", "--project"}, rejected: "config (1 extra argument not allowed)"},
		{name: "role reviewer", argv: []string{"role", "reviewer"}, rejected: "role (1 extra argument not allowed)"},
		{name: "collect without agent", argv: []string{"collect"}, rejected: "collect"},
		{name: "friction without value", argv: []string{"friction", "--since"}, rejected: "friction (1 extra argument not allowed)"},
		{name: "gc without hours", argv: []string{"gc", "--older-than"}, rejected: "gc (1 extra argument not allowed)"},
		{name: "spawn", argv: []string{"spawn", "implementer"}, rejected: "spawn (1 extra argument not allowed)"},
		{name: "no command", argv: []string{}, rejected: "(none)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, out, errOut := nr.runNowrite(t, c.argv...)
			if code != 2 {
				t.Fatalf("code=%d want 2; out=%q err=%q", code, out, errOut)
			}
			if out != "" {
				t.Fatalf("rejected invocation printed stdout: %q", out)
			}
			want := messagePrefix + c.rejected + messageTail
			if errOut != want {
				t.Fatalf("stderr:\n got:  %q\nwant: %q", errOut, want)
			}
		})
	}
}

// TestNowriteAcceptsExactConfigAndRoles proves the exact config and the exact
// roles run under SOHO_NOWRITE=1 (exit 0, non-empty output) and write
// nothing: the whole fixture tree — project and user config tree included —
// keeps the same paths, sizes and mtimes, starting from a project without a
// state dir and without a .gitignore line; a project that has only the legacy
// config file is read but never migrated.
func TestNowriteAcceptsExactConfigAndRoles(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		flavor := "without a legacy config"
		if legacy {
			flavor = "with only the legacy config"
		}
		t.Run(flavor, func(t *testing.T) {
			nr := newNowriteReads(t, false)
			if legacy {
				legacyConf := filepath.Join(nr.f.repo, ".agents", "herdr-soho.conf")
				if err := os.MkdirAll(filepath.Dir(legacyConf), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(legacyConf, []byte("panes=4\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			for _, name := range []string{"config", "roles"} {
				t.Run(name, func(t *testing.T) {
					code, out, errOut := nr.runNowrite(t, name)
					if code != 0 {
						t.Fatalf("code=%d want 0; out=%q err=%q", code, out, errOut)
					}
					if out == "" {
						t.Fatalf("empty stdout; err=%q", errOut)
					}
					if _, err := os.Stat(filepath.Join(nr.f.repo, ".soho")); !os.IsNotExist(err) {
						t.Fatalf(".soho/ appeared in the project: %v", err)
					}
					if _, err := os.Stat(filepath.Join(nr.f.repo, ".gitignore")); !os.IsNotExist(err) {
						t.Fatalf(".gitignore appeared in the project: %v", err)
					}
					if _, err := os.Stat(filepath.Join(nr.f.repo, ".agents", "soho.conf")); !os.IsNotExist(err) {
						t.Fatalf("the legacy config was migrated: %v", err)
					}
					if legacy {
						legacyConf := filepath.Join(nr.f.repo, ".agents", "herdr-soho.conf")
						data, err := os.ReadFile(legacyConf)
						if err != nil || string(data) != "panes=4\n" {
							t.Fatalf("legacy config changed: %q err=%v", data, err)
						}
					}
				})
			}
		})
	}
}

// TestGcShowsThePressureLine proves the gc pressure line, printed first on
// every run from the s85 measurements and limits: the warning text under
// pressure, the "none" line otherwise, and "<metric> not measured" for a
// metric that could not be measured.
func TestGcShowsThePressureLine(t *testing.T) {
	nr := newNowriteReads(t, true)
	sd := nr.sd
	cases := []struct {
		name       string
		diskFreeGB int64
		diskOK     bool
		swapUsedGB int64
		swapOK     bool
		env        platform.Env
		want       string
	}{
		{
			name: "under pressure", diskFreeGB: 3, diskOK: true, swapUsedGB: 95, swapOK: true,
			want: "resource pressure: disk " + sd + " has 3% free (3 GB of 100 GB); swap 95% used (95 GB of 100 GB); run 'soho gc' and close idle panes before opening more",
		},
		{
			name: "none", diskFreeGB: 60, diskOK: true, swapUsedGB: 30, swapOK: true,
			want: "resource pressure: none (disk 60% free, swap 30% used)",
		},
		{
			name: "disk not measured", diskFreeGB: 0, diskOK: false, swapUsedGB: 30, swapOK: true,
			want: "resource pressure: none (disk not measured, swap 30% used)",
		},
		{
			name: "swap not measured", diskFreeGB: 60, diskOK: true, swapUsedGB: 0, swapOK: false,
			want: "resource pressure: none (disk 60% free, swap not measured)",
		},
		{
			name: "nothing measured", diskFreeGB: 0, diskOK: false, swapUsedGB: 0, swapOK: false,
			want: "resource pressure: none (disk not measured, swap not measured)",
		},
		{
			name: "pressure with swap not measured", diskFreeGB: 3, diskOK: true, swapUsedGB: 0, swapOK: false,
			want: "resource pressure: disk " + sd + " has 3% free (3 GB of 100 GB); swap not measured; run 'soho gc' and close idle panes before opening more",
		},
		{
			name: "the configured limit applies", diskFreeGB: 60, diskOK: true, swapUsedGB: 30, swapOK: true,
			env:  platform.Env{"SOHO_PRESSURE_DISK_FREE_PERCENT": "70"},
			want: "resource pressure: disk " + sd + " has 60% free (60 GB of 100 GB); run 'soho gc' and close idle panes before opening more",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			oldDisk, oldSwap := platform.DiskFree, platform.SwapUsage
			platform.DiskFree = func(string) (int64, int64, bool) {
				return c.diskFreeGB * 1024 * 1024 * 1024, nowriteTotalBytes, c.diskOK
			}
			platform.SwapUsage = func(platform.Env) (int64, int64, bool) {
				return c.swapUsedGB * 1024 * 1024 * 1024, nowriteTotalBytes, c.swapOK
			}
			t.Cleanup(func() { platform.DiskFree, platform.SwapUsage = oldDisk, oldSwap })

			env := platform.Env{}
			for k, v := range nr.f.env {
				env[k] = v
			}
			env["SOHO_NOWRITE"] = "1"
			for k, v := range c.env {
				env[k] = v
			}
			before := nowriteTreeSnapshot(t, nr.f.root)
			code, out, errOut := nr.f.run(t, env, "gc")
			after := nowriteTreeSnapshot(t, nr.f.root)
			if code != 0 || errOut != "" {
				t.Fatalf("code=%d want 0; err=%q out=%q", code, errOut, out)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("gc under SOHO_NOWRITE=1 changed the tree")
			}
			i := strings.IndexByte(out, '\n')
			if i < 0 {
				t.Fatalf("gc stdout has no pressure line: %q", out)
			}
			if got := out[:i]; got != c.want {
				t.Fatalf("pressure line:\n got:  %q\nwant: %q", got, c.want)
			}
		})
	}
}
