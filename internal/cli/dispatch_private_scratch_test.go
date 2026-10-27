package cli

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// scratchDispatchFixture is the DJA-318 C2b fixture: a worker whose cwd
// (/work) is outside the project, so the dispatch routes the composed
// brief, the sidecar and the report through $TMPDIR/soho/ws/reports. The
// PATH holds only the fake herdr.
type scratchDispatchFixture struct {
	root, repo, tmp, state, bin, brief string
	env                                platform.Env
}

func newScratchDispatchFixture(t *testing.T) *scratchDispatchFixture {
	t.Helper()
	skillDir, err := filepath.Abs(filepath.Join(packageDir, "..", "..", "skills", "soho"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	f := &scratchDispatchFixture{root: root, repo: filepath.Join(root, "repo"), tmp: filepath.Join(root, "tmp"), state: filepath.Join(root, "state"), bin: filepath.Join(root, "bin")}
	roles := filepath.Join(root, "roles")
	for _, dir := range []string{f.repo, f.bin, f.tmp, roles, filepath.Join(f.state, "ws")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(roles, "implementer.md"), []byte("---\nname: Implementer\nmode: edit\n---\nRole body.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.brief = filepath.Join(f.repo, "brief.md")
	if err := os.WriteFile(f.brief, []byte("# Goal\n\nrun the slice\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\nworker\tp1\tcodex\timplementer\tanthropic\t\t" + t.TempDir() + "\tnow\tmodel-x\tfull\timplementer\tbuild\n"
	if err := os.WriteFile(filepath.Join(f.state, "ws", "agents.tsv"), []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	rules := []fakecli.Rule{
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`},
		{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`},
		{Argv: []string{"agent", "prompt", "worker"}, ArgvPrefix: true, Stdout: `{"result":{"submitted":true}}`},
		{AnyArgs: true, Stdout: ""},
	}
	if _, err := fakecli.Install(t, f.bin, "herdr", rules); err != nil {
		t.Fatal(err)
	}
	clean := envFrom(testutil.CleanEnv(t))
	for key := range clean {
		if strings.HasPrefix(key, "SOHO_") {
			delete(clean, key)
		}
	}
	clean = withFakeCLI(clean, f.bin)
	clean["PATH"] = f.bin
	clean["HERDR_ENV"] = "1"
	clean["HERDR_WORKSPACE_ID"] = "ws"
	clean["HERDR_SOCKET_PATH"] = filepath.Join(root, "missing.sock")
	clean["TMPDIR"] = f.tmp
	clean["SOHO_DIR"] = f.state
	clean["SOHO_SKILL_DIR"] = skillDir
	clean["SOHO_ROLES"] = roles
	clean["SOHO_BRIEF_LINT"] = "off"
	clean["SOHO_WAIT_POLL_MS"] = "1"
	clean["SOHO_PROMPT_CHECK_SECONDS"] = "0"
	f.env = clean
	return f
}

func (f *scratchDispatchFixture) reportsDir() string {
	return filepath.Join(f.tmp, "soho", "ws", "reports")
}

func (f *scratchDispatchFixture) levels() []string {
	return []string{filepath.Join(f.tmp, "soho"), filepath.Join(f.tmp, "soho", "ws"), f.reportsDir()}
}

// dispatch runs a no-wait dispatch and returns the code, the composed
// brief and report paths of its JSON (empty on a refusal) and the stderr.
func (f *scratchDispatchFixture) dispatch(t *testing.T) (int, string, string, string) {
	t.Helper()
	code, out, stderr := runIn(t, []string{"dispatch", "worker", f.brief, "--no-wait"}, f.env, f.repo)
	if code != 0 {
		return code, "", "", stderr
	}
	value, err := jsonjs.Parse([]byte(strings.TrimSpace(out)))
	if err != nil {
		t.Fatalf("dispatch JSON %q: %v", out, err)
	}
	composed, _ := value.(*jsonjs.Object).Get("composed_prompt")
	report, _ := value.(*jsonjs.Object).Get("report")
	c, _ := composed.(string)
	r, _ := report.(string)
	return code, c, r, stderr
}

func (f *scratchDispatchFixture) prompts(t *testing.T) int {
	t.Helper()
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(f.env.Get("SOHO_FAKECLI_CONFIG"), "herdr.json"))
	if err != nil {
		return 0
	}
	n := 0
	for _, c := range calls {
		if len(c.Argv) > 1 && c.Argv[0] == "agent" && c.Argv[1] == "prompt" {
			n++
		}
	}
	return n
}

func scratchModeOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func skipScratchOwnerOffUnix(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the scratch's owner and mode are checked on Unix only")
	}
}

func skipScratchLinksOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need elevation on Windows without developer mode")
	}
}

// scratchOwnedByAnother makes the named directory level read as another
// user's (core.ScratchOwnerOf).
func scratchOwnedByAnother(t *testing.T, name string) {
	t.Helper()
	old := core.ScratchOwnerOf
	core.ScratchOwnerOf = func(info fs.FileInfo) (int, bool) {
		if info.IsDir() && info.Name() == name {
			return os.Getuid() + 1, true
		}
		return old(info)
	}
	t.Cleanup(func() { core.ScratchOwnerOf = old })
}

func TestDispatchCreatesTheScratchReportsDirPrivate(t *testing.T) {
	skipScratchOwnerOffUnix(t)
	f := newScratchDispatchFixture(t)
	code, composed, report, stderr := f.dispatch(t)
	if code != 0 || filepath.Dir(composed) != f.reportsDir() || filepath.Dir(report) != f.reportsDir() {
		t.Fatalf("code=%d composed=%q report=%q stderr=%q", code, composed, report, stderr)
	}
	for _, level := range f.levels() {
		if mode := scratchModeOf(t, level); mode != 0o700 {
			t.Fatalf("%s mode=%o; want 0700", level, mode)
		}
	}
	for _, file := range []string{composed, strings.TrimSuffix(composed, ".brief.md") + ".dispatch.json"} {
		info, err := os.Lstat(file)
		if err != nil || !info.Mode().IsRegular() {
			t.Fatalf("%s: %v %v; want a regular file", file, info, err)
		}
	}
}

// The dispatch narrows an own level with any permission for the group or
// the others (perm & 0o077) to 0o700: 0o775 and 0o755 alike (DJA-443,
// amendment 1).
func TestDispatchNarrowsAnOwnScratchLevelOpenToOthers(t *testing.T) {
	skipScratchOwnerOffUnix(t)
	for _, c := range []struct{ mode, want os.FileMode }{{0o775, 0o700}, {0o755, 0o700}} {
		mode, want := c.mode, c.want
		t.Run(mode.String(), func(t *testing.T) {
			f := newScratchDispatchFixture(t)
			if err := os.MkdirAll(f.reportsDir(), 0o700); err != nil {
				t.Fatal(err)
			}
			for _, level := range f.levels() {
				if err := os.Chmod(level, mode); err != nil {
					t.Fatal(err)
				}
			}
			if code, _, _, stderr := f.dispatch(t); code != 0 {
				t.Fatalf("code=%d stderr=%q", code, stderr)
			}
			for _, level := range f.levels() {
				if got := scratchModeOf(t, level); got != want {
					t.Fatalf("%s mode=%o; want %o", level, got, want)
				}
			}
		})
	}
}

func TestDispatchRefusesAScratchLevelThatIsNotTheUsers(t *testing.T) {
	check := func(t *testing.T, f *scratchDispatchFixture, level, cause string) {
		t.Helper()
		code, _, _, stderr := f.dispatch(t)
		want := "dispatch: the scratch reports dir cannot be used (reports: refusing " + level + ": " + cause + "); nothing was sent to 'worker'"
		if code != 4 || !strings.Contains(stderr, want) {
			t.Fatalf("code=%d stderr=%q; want exit 4 and %q", code, stderr, want)
		}
		if n := f.prompts(t); n != 0 {
			t.Fatalf("%d agent prompt calls; the brief must not be sent", n)
		}
	}
	for _, name := range []string{"soho", "ws", "reports"} {
		t.Run("another owner at "+name, func(t *testing.T) {
			skipScratchOwnerOffUnix(t)
			f := newScratchDispatchFixture(t)
			if err := os.MkdirAll(f.reportsDir(), 0o700); err != nil {
				t.Fatal(err)
			}
			scratchOwnedByAnother(t, name)
			level := map[string]string{"soho": f.levels()[0], "ws": f.levels()[1], "reports": f.levels()[2]}[name]
			check(t, f, level, "it belongs to another user")
		})
	}
	t.Run("a link in place of the reports level", func(t *testing.T) {
		skipScratchLinksOnWindows(t)
		f := newScratchDispatchFixture(t)
		if err := os.MkdirAll(filepath.Dir(f.reportsDir()), 0o700); err != nil {
			t.Fatal(err)
		}
		elsewhere := filepath.Join(f.root, "elsewhere")
		if err := os.MkdirAll(elsewhere, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(elsewhere, f.reportsDir()); err != nil {
			t.Fatal(err)
		}
		check(t, f, f.reportsDir(), "it is a symbolic link")
		if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
			t.Fatalf("the dispatch wrote through the link: %v", entries)
		}
	})
}

// A link planted at the composed brief's name (dangling, so the pair
// suffix does not move past it) and at its sidecar's name is replaced by
// the dispatch's write; neither target is written.
func TestDispatchReplacesALinkInPlaceOfItsScratchFiles(t *testing.T) {
	skipScratchLinksOnWindows(t)
	f := newScratchDispatchFixture(t)
	fixed := time.Date(2026, 10, 20, 3, 0, 0, 0, time.Local)
	oldNow := platform.Now
	platform.Now = func() time.Time { return fixed }
	t.Cleanup(func() { platform.Now = oldNow })
	if _, err := core.PrivateScratchReports(f.env, "ws", core.ScratchCreate); err != nil {
		t.Fatal(err)
	}
	stem := filepath.Join(f.reportsDir(), "worker-"+core.NowStamp(fixed))
	absent := filepath.Join(f.root, "victim-absent.txt")
	if err := os.Symlink(absent, stem+".brief.md"); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(f.root, "victim.json")
	content := "{\"victim\":true}\n"
	if err := os.WriteFile(victim, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, stem+".dispatch.json"); err != nil {
		t.Fatal(err)
	}
	code, composed, _, stderr := f.dispatch(t)
	if code != 0 || composed != stem+".brief.md" {
		t.Fatalf("code=%d composed=%q stderr=%q; want the planted name", code, composed, stderr)
	}
	if _, err := os.Lstat(absent); !os.IsNotExist(err) {
		t.Fatalf("the composed brief was written through the link: %v", err)
	}
	if raw, err := os.ReadFile(victim); err != nil || string(raw) != content {
		t.Fatalf("the sidecar's link target changed: %q %v", raw, err)
	}
	for _, file := range []string{stem + ".brief.md", stem + ".dispatch.json"} {
		info, err := os.Lstat(file)
		if err != nil || !info.Mode().IsRegular() {
			t.Fatalf("%s is not a regular file after the dispatch: %v %v", file, info, err)
		}
	}
	if raw, _ := os.ReadFile(stem + ".brief.md"); !strings.Contains(string(raw), "run the slice") {
		t.Fatalf("composed brief=%q", raw)
	}
}

// herdrCalls reads every call the fake herdr logged; a log that was never
// created is no call (the fake's config must exist).
func (f *scratchDispatchFixture) herdrCalls(t *testing.T) []fakecli.Call {
	t.Helper()
	config := filepath.Join(f.env.Get("SOHO_FAKECLI_CONFIG"), "herdr.json")
	if _, err := os.Stat(config); err != nil {
		t.Fatalf("the fake herdr's config is missing: %v", err)
	}
	calls, err := fakecli.ReadCallsForConfig(config)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return calls
}

// stateSnapshot lists every path below the state root with its size.
func stateSnapshot(t *testing.T, root string) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		size := int64(-1)
		if info, infoErr := entry.Info(); infoErr == nil && !entry.IsDir() {
			size = info.Size()
		}
		out[path] = size
		return nil
	})
	return out
}

// The scratch check runs right after the roster line (DJA-443, amendment
// 2): a refused scratch exits 4 before the compact-phase check removes a
// foreign pane's marker, before any herdr call and before any write in the
// state dir.
func TestDispatchScratchRefusalComesBeforeAnyEffect(t *testing.T) {
	refused := func(t *testing.T, refuse func(f *scratchDispatchFixture)) {
		t.Helper()
		f := newScratchDispatchFixture(t)
		sd := filepath.Join(f.state, "ws")
		marker := core.CompactPendingPath(sd, "worker")
		if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
			t.Fatal(err)
		}
		body := `{"version":1,"token":"t","pane":"another-pane","brief":"b","pid":1,"started":"s","created_at":"2026-10-20T00:00:00Z"}`
		if err := os.WriteFile(marker, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		// cli.Run resolves the friction log through core.StateDir before
		// any command, which creates briefs, reports and wait when they are
		// missing: that common step is not the dispatch's, so the dirs exist
		// before the snapshot.
		for _, name := range []string{"briefs", "reports", "wait"} {
			if err := os.MkdirAll(filepath.Join(sd, name), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		refuse(f)
		before := stateSnapshot(t, f.state)
		code, _, _, stderr := f.dispatch(t)
		if code != 4 || !strings.Contains(stderr, "dispatch: the scratch reports dir cannot be used (reports: refusing "+f.reportsDir()+": ") {
			t.Fatalf("code=%d stderr=%q; want the scratch refusal with exit 4", code, stderr)
		}
		if raw, err := os.ReadFile(marker); err != nil || string(raw) != body {
			t.Fatalf("the foreign pane's compact marker was touched: %q %v", raw, err)
		}
		if calls := f.herdrCalls(t); len(calls) != 0 {
			t.Fatalf("herdr was called before the refusal: %+v", calls)
		}
		// cli.Run records the refusal itself in friction.log after the exit
		// (every command's error is recorded there): that line is the only
		// change allowed, and it must be this refusal.
		friction := filepath.Join(sd, "friction.log")
		if raw, err := os.ReadFile(friction); err == nil {
			lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
			if len(lines) != 1 || !strings.Contains(lines[0], "the scratch reports dir cannot be used") {
				t.Fatalf("friction.log=%q; want only the refusal's record", raw)
			}
		}
		after := stateSnapshot(t, f.state)
		delete(after, friction)
		for path, size := range after {
			if old, ok := before[path]; !ok || old != size {
				t.Fatalf("the state dir changed at %s (size %d, before %d, present %v)", path, size, old, ok)
			}
		}
		if len(after) != len(before) {
			t.Fatalf("the state dir lost entries: before %v after %v", before, after)
		}
	}

	t.Run("a reports level of another owner", func(t *testing.T) {
		skipScratchOwnerOffUnix(t)
		refused(t, func(f *scratchDispatchFixture) {
			if err := os.MkdirAll(f.reportsDir(), 0o700); err != nil {
				t.Fatal(err)
			}
			scratchOwnedByAnother(t, "reports")
		})
	})

	t.Run("a link in place of the reports level", func(t *testing.T) {
		skipScratchLinksOnWindows(t)
		refused(t, func(f *scratchDispatchFixture) {
			if err := os.MkdirAll(filepath.Dir(f.reportsDir()), 0o700); err != nil {
				t.Fatal(err)
			}
			elsewhere := filepath.Join(f.root, "elsewhere")
			if err := os.MkdirAll(elsewhere, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(elsewhere, f.reportsDir()); err != nil {
				t.Fatal(err)
			}
		})
	})

	// The control: the same fixture without a refusal calls herdr and
	// clears the foreign marker, so the reads above can see both.
	t.Run("control: an accepted scratch reaches herdr and the marker check", func(t *testing.T) {
		f := newScratchDispatchFixture(t)
		marker := core.CompactPendingPath(filepath.Join(f.state, "ws"), "worker")
		if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(marker, []byte(`{"version":1,"token":"t","pane":"another-pane","brief":"b","pid":1,"started":"s","created_at":"2026-10-20T00:00:00Z"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if code, _, _, stderr := f.dispatch(t); code != 0 {
			t.Fatalf("code=%d stderr=%q", code, stderr)
		}
		if len(f.herdrCalls(t)) == 0 {
			t.Fatal("the control dispatch logged no herdr call: the log reader sees nothing")
		}
		if _, err := os.Lstat(marker); !os.IsNotExist(err) {
			t.Fatalf("the control dispatch kept the foreign marker: %v", err)
		}
	})
}
