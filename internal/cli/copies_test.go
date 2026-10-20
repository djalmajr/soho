package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

const (
	agentIdleJSON = `{"result":{"agent":{"agent_status":"idle"}}}`
	agentGoneJSON = `{"error":{"code":"agent_not_found","message":"gone"}}`
)

// copiesFixture is a hermetic project: a git repository with a worktree under
// .worktrees/, a fake HOME, a fake TMPDIR, its own state dir and a fake
// herdr CLI on a PATH that holds only the fake herdr, the real git and the
// system directories (never the real herdr).
type copiesFixture struct {
	root     string
	repo     string
	worktree string
	home     string
	tmp      string
	state    string
	skill    string
	env      platform.Env
	cwd      string
}

func newCopiesFixture(t *testing.T, agentRules []fakecli.Rule) *copiesFixture {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not available on PATH")
	}
	root := t.TempDir()
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "repo")
	home := filepath.Join(root, "home")
	tmp := filepath.Join(root, "tmp")
	state := filepath.Join(root, "state")
	skill := filepath.Join(root, "skill")
	for _, dir := range []string{repo, home, tmp, state, skill} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("fixture skill\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(skill, "roles"), 0o700); err != nil {
		t.Fatal(err)
	}
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command(gitPath, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull,
			"GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid",
			"GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git(repo, "init", "-q")
	git(repo, "commit", "-q", "--allow-empty", "-m", "fixture")
	worktree := filepath.Join(repo, ".worktrees", "wt")
	git(repo, "worktree", "add", "-q", worktree)

	fakeDir := t.TempDir()
	if _, err := fakecli.Install(t, fakeDir, "herdr", agentRules); err != nil {
		t.Fatal(err)
	}
	// A PATH with git and without the herdr CLI. On Windows a link named
	// "git" without .exe is not an executable, and git's own directory holds
	// no herdr; elsewhere git's directory may hold herdr (Homebrew), so a
	// link in a directory of its own is used.
	gitBin := filepath.Dir(gitPath)
	if runtime.GOOS != "windows" {
		gitBin = t.TempDir()
		if err := os.Symlink(gitPath, filepath.Join(gitBin, "git")); err != nil {
			t.Fatal(err)
		}
	}
	env := platform.Env{
		"HOME":                home,
		"USERPROFILE":         home,
		"XDG_CONFIG_HOME":     filepath.Join(root, "conf"),
		"TMPDIR":              tmp,
		"HERDR_ENV":           "1",
		"HERDR_WORKSPACE_ID":  "ws",
		"HERDR_PANE_ID":       "p-worker",
		"SOHO_DIR":            state,
		"SOHO_SKILL_DIR":      skill,
		"SOHO_REGRID":         "off",
		"SOHO_FAKECLI_CONFIG": fakeDir,
		"PATH":                strings.Join([]string{fakeDir, gitBin, "/usr/bin", "/bin"}, string(os.PathListSeparator)),
	}
	// A race-instrumented fake child otherwise pauses for one second on
	// exit; the fixture sets only GORACE and leaves the rest of the env as built.
	env["GORACE"] = fakecli.RaceOptions(env.List())
	return &copiesFixture{root: root, repo: repo, worktree: worktree, home: home, tmp: tmp, state: state, skill: skill, env: env, cwd: repo}
}

// run executes argv through Run with the fixture's cwd and swapped stdout/
// stderr, returning the code and both streams.
func (f *copiesFixture) run(t *testing.T, env platform.Env, argv ...string) (int, string, string) {
	t.Helper()
	oldCwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(f.cwd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCwd) })
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, errOut bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &errOut
	t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
	code := Run(argv, env)
	return code, out.String(), errOut.String()
}

// git runs git with dir as cwd, using an isolated config.
func (f *copiesFixture) git(t *testing.T, dir string, args ...string) {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not available on PATH")
	}
	cmd := exec.Command(gitPath, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull,
		"GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid",
		"GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func (f *copiesFixture) stateDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(f.state, "ws")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func (f *copiesFixture) writeRoster(t *testing.T, rows ...string) {
	t.Helper()
	dir := f.stateDir(t)
	content := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\n"
	for _, row := range rows {
		content += row + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "agents.tsv"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f *copiesFixture) writeCopies(t *testing.T, rows []core.CopyRow) {
	t.Helper()
	if err := core.WriteCopies(f.stateDir(t), rows); err != nil {
		t.Fatal(err)
	}
}

func (f *copiesFixture) copiesRows(t *testing.T) []core.CopyRow {
	t.Helper()
	rows, err := core.ReadCopies(f.stateDir(t))
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func (f *copiesFixture) registryPath() string {
	return filepath.Join(f.state, "ws", "copies.tsv")
}

func copiesRosterRow(name, pane string) string {
	return strings.Join([]string{name, pane, "grok", "implementer", "xai", "1", "/tmp/work", "now", "grok-4.7", "full", "implementer", "build"}, "\t")
}

func TestMutationCopyRegistersTheCopy(t *testing.T) {
	f := newMutationCopyFixture(t)
	skill := filepath.Join(f.root, "skill")
	if err := os.MkdirAll(filepath.Join(skill, "roles"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("fixture skill\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(f.root, "state")
	if err := os.MkdirAll(filepath.Join(state, "ws"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "ws", "agents.tsv"), []byte("# roster\n"+releaseRow("worker", "p-worker", "1", "implementer", false, "/tmp/work")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(f.root, "mcopy")
	overrides := platform.Env{"SOHO_DIR": state, "SOHO_SKILL_DIR": skill, "HERDR_WORKSPACE_ID": "ws", "HERDR_PANE_ID": "p-worker"}
	code, out, errOut := runMutationCopyCmd(t, f, f.source, overrides, "--source", f.source, "--dest", dest)
	if code != 0 || errOut != "" {
		t.Fatalf("code=%d out=%q err=%q; want a silent success with today's stdout", code, out, errOut)
	}
	if got := decodedCopy(t, out); got.Copy != dest || got.Source != f.source || got.Files != 3 {
		t.Fatalf("stdout differs from today: %q", out)
	}
	rows, err := core.ReadCopies(filepath.Join(state, "ws"))
	if err != nil || len(rows) != 1 {
		t.Fatalf("registry rows=%#v err=%v; want one line", rows, err)
	}
	row := rows[0]
	created, parseErr := time.Parse(copiesTimeLayout, row.Created)
	if row.Path != dest || row.Owner != "worker" || row.Pane != "p-worker" || row.Source != f.source || row.Origin != "mutation-copy" || parseErr != nil || platform.Now().Sub(created) > time.Minute {
		t.Fatalf("registry row=%#v created=%q; want the copy owned by the pane's agent", row, row.Created)
	}

	// Re-registering the same path replaces the line instead of duplicating
	// it: the roster pane now names a different agent.
	if err := os.RemoveAll(dest); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "ws", "agents.tsv"), []byte("# roster\n"+releaseRow("worker2", "p-worker", "1", "implementer", false, "/tmp/work")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut = runMutationCopyCmd(t, f, f.source, overrides, "--source", f.source, "--dest", dest)
	if code != 0 || errOut != "" {
		t.Fatalf("second run code=%d out=%q err=%q", code, out, errOut)
	}
	rows, err = core.ReadCopies(filepath.Join(state, "ws"))
	if err != nil || len(rows) != 1 || rows[0].Path != dest || rows[0].Owner != "worker2" {
		t.Fatalf("registry rows=%#v err=%v; want the same path with the new owner, no duplicate", rows, err)
	}
}

func TestMutationCopyOutsideHerdrKeepsTodayOutput(t *testing.T) {
	f := newMutationCopyFixture(t)
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not available on PATH")
	}
	// A PATH with git and without the herdr CLI. On Windows a link named
	// "git" without .exe is not an executable, and git's own directory holds
	// no herdr; elsewhere git's directory may hold herdr (Homebrew), so a
	// link in a directory of its own is used.
	gitBin := filepath.Dir(gitPath)
	if runtime.GOOS != "windows" {
		gitBin = t.TempDir()
		if err := os.Symlink(gitPath, filepath.Join(gitBin, "git")); err != nil {
			t.Fatal(err)
		}
	}
	state := filepath.Join(f.root, "state")
	skill := filepath.Join(f.root, "skill")
	if err := os.MkdirAll(filepath.Join(skill, "roles"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("fixture skill\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(f.root, "mcopy")
	// No HERDR_WORKSPACE_ID and a PATH without the herdr CLI: outside Herdr.
	overrides := platform.Env{"PATH": gitBin, "SOHO_DIR": state, "SOHO_SKILL_DIR": skill}
	code, out, errOut := runMutationCopyCmd(t, f, f.source, overrides, "--source", f.source, "--dest", dest)
	if code != 0 {
		t.Fatalf("code=%d err=%q; want 0 outside Herdr", code, errOut)
	}
	got := decodedCopy(t, out)
	if got.Copy != dest || got.Source != f.source || got.Files != 3 {
		t.Fatalf("stdout differs from today: %q", out)
	}
	if !strings.Contains(errOut, "mutation-copy: copy not registered (herdr CLI not found in PATH); remove it yourself when done") {
		t.Fatalf("stderr=%q; want the not-registered warning", errOut)
	}
	if _, err := os.Stat(filepath.Join(state, "ws", "copies.tsv")); !os.IsNotExist(err) {
		t.Fatalf("the registry was written outside Herdr: %v", err)
	}
	if entries, err := os.ReadDir(state); err == nil && len(entries) > 0 {
		t.Fatalf("the state dir was created outside Herdr: %v", entries)
	}
}

func TestCopiesAdd(t *testing.T) {
	t.Run("registers for the pane's agent, with --agent, and as - without a pane", func(t *testing.T) {
		f := newCopiesFixture(t, []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: agentIdleJSON}})
		f.writeRoster(t, copiesRosterRow("worker", "p-worker"))
		src := filepath.Join(f.tmp, "handcopy")
		if err := os.MkdirAll(src, 0o700); err != nil {
			t.Fatal(err)
		}
		code, out, errOut := f.run(t, f.env, "copies", "add", src)
		if code != 0 || out != "" || errOut != "" {
			t.Fatalf("code=%d out=%q err=%q; want a silent success", code, out, errOut)
		}
		rows := f.copiesRows(t)
		if len(rows) != 1 || rows[0].Path != src || rows[0].Owner != "worker" || rows[0].Pane != "p-worker" || rows[0].Source != "-" || rows[0].Origin != "add" {
			t.Fatalf("registry rows=%#v; want the pane's agent with origin add and source -", rows)
		}
		if _, err := time.Parse(copiesTimeLayout, rows[0].Created); err != nil {
			t.Fatalf("created=%q is not ISO-8601 UTC: %v", rows[0].Created, err)
		}

		src2 := filepath.Join(f.tmp, "handcopy2")
		if err := os.MkdirAll(src2, 0o700); err != nil {
			t.Fatal(err)
		}
		code, out, errOut = f.run(t, f.env, "copies", "add", src2, "--agent", "worker")
		if code != 0 || errOut != "" {
			t.Fatalf("--agent code=%d out=%q err=%q", code, out, errOut)
		}
		rows = f.copiesRows(t)
		if len(rows) != 2 || rows[1].Owner != "worker" || rows[1].Pane != "p-worker" || rows[1].Origin != "add" {
			t.Fatalf("registry rows=%#v; want the --agent line with the roster pane", rows)
		}

		envNoPane := f.env.Clone()
		delete(envNoPane, "HERDR_PANE_ID")
		src3 := filepath.Join(f.tmp, "handcopy3")
		if err := os.MkdirAll(src3, 0o700); err != nil {
			t.Fatal(err)
		}
		code, out, errOut = f.run(t, envNoPane, "copies", "add", src3)
		if code != 0 {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
		rows = f.copiesRows(t)
		if len(rows) != 3 || rows[2].Owner != "-" || rows[2].Pane != "-" {
			t.Fatalf("registry rows=%#v; want owner and pane - without HERDR_PANE_ID", rows)
		}
	})

	t.Run("refuses unsafe paths with exit 2 and writes nothing", func(t *testing.T) {
		f := newCopiesFixture(t, []fakecli.Rule{})
		f.writeRoster(t, copiesRosterRow("worker", "p-worker"))
		inner := filepath.Join(f.repo, "inner")
		if err := os.MkdirAll(inner, 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(f.tmp, "plain")
		if err := os.MkdirAll(target, 0o700); err != nil {
			t.Fatal(err)
		}
		var link string
		if runtime.GOOS != "windows" {
			link = filepath.Join(f.tmp, "link")
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
		}
		otherRepo := filepath.Join(f.tmp, "other-repo")
		if err := os.MkdirAll(otherRepo, 0o700); err != nil {
			t.Fatal(err)
		}
		f.git(t, otherRepo, "init", "-q")
		file := filepath.Join(f.tmp, "afile")
		if err := os.WriteFile(file, []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		// The volume of a real path: on Windows "." has none, and a bare
		// "\\" is not absolute there.
		rootPath := filepath.VolumeName(f.tmp) + string(filepath.Separator)
		cases := []struct{ path, wantCause string }{
			{f.repo, "is the project root or inside it"},
			{inner, "is the project root or inside it"},
			{f.worktree, "is the project root or inside it"},
			{f.home, "is HOME or above it"},
			{f.root, "is HOME or above it"},
			{rootPath, "is the filesystem root"},
			{otherRepo, "contains a .git (another repository or a worktree)"},
			{filepath.Join(f.tmp, "nope"), "does not exist"},
			{file, "not an existing directory"},
			{filepath.Join("tmp", "plain"), "not an absolute path"},
		}
		if link != "" {
			cases = append(cases, struct{ path, wantCause string }{link, "is a symlink"})
		}
		for _, c := range cases {
			code, out, errOut := f.run(t, f.env, "copies", "add", c.path)
			if code != 2 || !strings.Contains(errOut, "copies add: refusing '"+c.path+"': "+c.wantCause) || out != "" {
				t.Fatalf("path=%q: code=%d out=%q err=%q; want refusal %q", c.path, code, out, errOut, c.wantCause)
			}
		}
		if _, err := os.Stat(f.registryPath()); !os.IsNotExist(err) {
			t.Fatalf("a refused add wrote the registry: %v", err)
		}
		if _, err := os.Stat(target); err != nil {
			t.Fatalf("the plain target was touched: %v", err)
		}
		// An empty registry says so (G3).
		code, out, errOut := f.run(t, f.env, "copies")
		if code != 0 || out != "no registered copies\n" || errOut != "" {
			t.Fatalf("empty list code=%d out=%q err=%q; want no registered copies", code, out, errOut)
		}
	})

	t.Run("an --agent outside the roster exits 3 and writes nothing", func(t *testing.T) {
		f := newCopiesFixture(t, []fakecli.Rule{})
		f.writeRoster(t, copiesRosterRow("worker", "p-worker"))
		src := filepath.Join(f.tmp, "handcopy")
		if err := os.MkdirAll(src, 0o700); err != nil {
			t.Fatal(err)
		}
		code, out, errOut := f.run(t, f.env, "copies", "add", src, "--agent", "ghost")
		if code != 3 || !strings.Contains(errOut, "agent 'ghost' is not in the roster") {
			t.Fatalf("code=%d out=%q err=%q; want exit 3", code, out, errOut)
		}
		if _, err := os.Stat(f.registryPath()); !os.IsNotExist(err) {
			t.Fatalf("a refused --agent wrote the registry: %v", err)
		}
	})

	t.Run("wrong usage exits 2", func(t *testing.T) {
		f := newCopiesFixture(t, []fakecli.Rule{})
		for _, args := range [][]string{
			{"copies", "add"},
			{"copies", "add", "--agent"},
			{"copies", "add", "--agent", "worker", "--agent", "worker2"},
			{"copies", "add", "--bogus"},
			{"copies", "bogus"},
		} {
			code, out, errOut := f.run(t, f.env, args...)
			if code != 2 || !strings.Contains(errOut, copiesUsage) {
				t.Fatalf("args=%v: code=%d out=%q err=%q; want the usage refusal", args, code, out, errOut)
			}
		}
	})
}

func TestCopiesShowsTheThreeStates(t *testing.T) {
	f := newCopiesFixture(t, []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: agentIdleJSON},
		{Argv: []string{"agent", "get", "worker2"}, Stderr: agentGoneJSON, Code: 1},
	})
	f.writeRoster(t, copiesRosterRow("worker", "p-worker"), copiesRosterRow("worker2", "p-worker2"))
	owned, orphan, goneDir := filepath.Join(f.tmp, "owned"), filepath.Join(f.tmp, "orphan"), filepath.Join(f.tmp, "gone-dir")
	missing := filepath.Join(f.tmp, "missing")
	for _, dir := range []string{owned, orphan, goneDir, missing} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.RemoveAll(missing); err != nil {
		t.Fatal(err)
	}
	age := core.FrictionISO(platform.Now().Add(-3 * time.Hour))
	f.writeCopies(t, []core.CopyRow{
		{Path: owned, Owner: "worker", Pane: "p-worker", Created: age, Source: "-", Origin: "add"},
		{Path: orphan, Owner: "ghost", Pane: "-", Created: age, Source: "-", Origin: "add"},
		{Path: goneDir, Owner: "worker2", Pane: "p-worker2", Created: age, Source: "-", Origin: "add"},
		{Path: missing, Owner: "worker", Pane: "p-worker", Created: age, Source: "-", Origin: "add"},
	})
	code, out, errOut := f.run(t, f.env, "copies")
	if code != 0 || errOut != "" {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	want := strings.Join([]string{
		owned + "  worker  3h  owned",
		orphan + "  ghost  3h  orphan",
		goneDir + "  worker2  3h  orphan",
		missing + "  worker  3h  missing",
	}, "\n") + "\n"
	if out != want {
		t.Fatalf("stdout=%q; want\n%q", out, want)
	}
}

func TestReleaseRemovesTheAgentsCopies(t *testing.T) {
	releaseRules := []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: agentIdleJSON},
		{Argv: []string{"pane", "report-metadata", "p-worker", "--source", "soho", "--clear-title"}},
		{Argv: []string{"pane", "list", "--workspace", "ws"}, Stdout: `{"result":{"panes":[]}}`},
	}
	now := core.FrictionISO(platform.Now())

	t.Run("removes the agent's copies and only those", func(t *testing.T) {
		f := newCopiesFixture(t, releaseRules)
		f.writeRoster(t, copiesRosterRow("worker", "p-worker"), copiesRosterRow("worker2", "p-worker2"))
		copyA, copyB := filepath.Join(f.tmp, "copy-a"), filepath.Join(f.tmp, "copy-b")
		for _, dir := range []string{copyA, copyB} {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		f.writeCopies(t, []core.CopyRow{
			{Path: copyA, Owner: "worker", Pane: "p-worker", Created: now, Source: "-", Origin: "mutation-copy"},
			{Path: copyB, Owner: "worker2", Pane: "p-worker2", Created: now, Source: "-", Origin: "mutation-copy"},
		})
		addReleaseReport(t, filepath.Join(f.state, "ws"), "worker", false)
		code, out, errOut := f.run(t, f.env, "release", "worker")
		if code != 0 {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
		if _, err := os.Stat(copyA); !os.IsNotExist(err) {
			t.Fatalf("the agent's copy was not removed: %v", err)
		}
		if _, err := os.Stat(copyB); err != nil {
			t.Fatalf("the other agent's copy was removed: %v", err)
		}
		rows := f.copiesRows(t)
		if len(rows) != 1 || rows[0].Path != copyB || rows[0].Owner != "worker2" {
			t.Fatalf("registry rows=%#v; want only the other agent's line", rows)
		}
		if !strings.Contains(out, "removed copy "+copyA+"\n") || !strings.Contains(out, "released worker\n") {
			t.Fatalf("stdout=%q; want the removed copy line and released", out)
		}
		if core.RosterLine(filepath.Join(f.state, "ws"), "worker") != "" || core.RosterLine(filepath.Join(f.state, "ws"), "worker2") == "" {
			t.Fatalf("the roster was not cleaned as before")
		}
	})

	t.Run("--keep-copies keeps them", func(t *testing.T) {
		f := newCopiesFixture(t, releaseRules)
		f.writeRoster(t, copiesRosterRow("worker", "p-worker"))
		copyA := filepath.Join(f.tmp, "copy-a")
		if err := os.MkdirAll(copyA, 0o700); err != nil {
			t.Fatal(err)
		}
		f.writeCopies(t, []core.CopyRow{{Path: copyA, Owner: "worker", Pane: "p-worker", Created: now, Source: "-", Origin: "mutation-copy"}})
		addReleaseReport(t, filepath.Join(f.state, "ws"), "worker", false)
		code, out, errOut := f.run(t, f.env, "release", "worker", "--keep-copies")
		if code != 0 {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
		if _, err := os.Stat(copyA); err != nil {
			t.Fatalf("the copy was removed despite --keep-copies: %v", err)
		}
		rows := f.copiesRows(t)
		if len(rows) != 1 || rows[0].Path != copyA {
			t.Fatalf("registry rows=%#v; want the line kept", rows)
		}
		if strings.Contains(out, "removed copy") {
			t.Fatalf("stdout=%q; --keep-copies must not remove anything", out)
		}
		if core.RosterLine(filepath.Join(f.state, "ws"), "worker") != "" {
			t.Fatalf("the roster was not cleaned as before")
		}
	})

	t.Run("a copy that fails the check is kept with a warning", func(t *testing.T) {
		f := newCopiesFixture(t, releaseRules)
		f.writeRoster(t, copiesRosterRow("worker", "p-worker"))
		f.writeCopies(t, []core.CopyRow{{Path: f.home, Owner: "worker", Pane: "p-worker", Created: now, Source: "-", Origin: "mutation-copy"}})
		addReleaseReport(t, filepath.Join(f.state, "ws"), "worker", false)
		code, out, errOut := f.run(t, f.env, "release", "worker")
		if code != 0 || !strings.Contains(out, "released worker\n") {
			t.Fatalf("code=%d out=%q err=%q; the refusal must not change the release", code, out, errOut)
		}
		if !strings.Contains(errOut, "release: kept copy "+f.home+" (is HOME or above it)") {
			t.Fatalf("stderr=%q; want the kept-copy warning", errOut)
		}
		if _, err := os.Stat(f.home); err != nil {
			t.Fatalf("HOME was removed: %v", err)
		}
		rows := f.copiesRows(t)
		if len(rows) != 1 || rows[0].Path != f.home {
			t.Fatalf("registry rows=%#v; want the line kept", rows)
		}
	})
}

type gcLayout struct {
	oldOrphan   string
	youngOrphan string
	owned       string
	missing     string
	unreg       string
}

// gcFixture seeds the registry with an old orphan, a young orphan, an owned
// copy and a missing line, plus an unregistered mutation copy below TMPDIR.
func gcFixture(t *testing.T) (*copiesFixture, gcLayout) {
	t.Helper()
	f := newCopiesFixture(t, []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: agentIdleJSON}})
	f.writeRoster(t, copiesRosterRow("worker", "p-worker"))
	// gc prints the s85 pressure line first, always; fake the measurements
	// so the goldens stay hermetic (a real machine could print a warning
	// line instead of the "none" one).
	oldDisk, oldSwap := platform.DiskFree, platform.SwapUsage
	platform.DiskFree = func(string) (int64, int64, bool) {
		return int64(60) * 1024 * 1024 * 1024, int64(100) * 1024 * 1024 * 1024, true
	}
	platform.SwapUsage = func(platform.Env) (int64, int64, bool) {
		return int64(30) * 1024 * 1024 * 1024, int64(100) * 1024 * 1024 * 1024, true
	}
	t.Cleanup(func() { platform.DiskFree, platform.SwapUsage = oldDisk, oldSwap })
	layout := gcLayout{
		oldOrphan:   filepath.Join(f.tmp, "old-orphan"),
		youngOrphan: filepath.Join(f.tmp, "young-orphan"),
		owned:       filepath.Join(f.tmp, "owned"),
		missing:     filepath.Join(f.tmp, "missing"),
		unreg:       filepath.Join(f.tmp, "soho-mutation-abc"),
	}
	for _, dir := range []string{layout.oldOrphan, layout.youngOrphan, layout.owned, layout.missing, layout.unreg} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// 10 + 15 bytes under the old orphan: the size the output must carry.
	if err := os.WriteFile(filepath.Join(layout.oldOrphan, "a.txt"), []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(layout.oldOrphan, "b.txt"), []byte("012345678901234"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(layout.missing); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(layout.unreg, "f.txt"), bytes.Repeat([]byte("0"), 40), 0o600); err != nil {
		t.Fatal(err)
	}
	threeHoursAgo := platform.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(layout.unreg, threeHoursAgo, threeHoursAgo); err != nil {
		t.Fatal(err)
	}
	f.writeCopies(t, []core.CopyRow{
		{Path: layout.oldOrphan, Owner: "ghost", Pane: "-", Created: core.FrictionISO(threeHoursAgo), Source: "-", Origin: "mutation-copy"},
		{Path: layout.youngOrphan, Owner: "ghost", Pane: "-", Created: core.FrictionISO(platform.Now().Add(-time.Hour)), Source: "-", Origin: "mutation-copy"},
		{Path: layout.owned, Owner: "worker", Pane: "p-worker", Created: core.FrictionISO(threeHoursAgo), Source: "-", Origin: "mutation-copy"},
		{Path: layout.missing, Owner: "worker", Pane: "p-worker", Created: core.FrictionISO(threeHoursAgo), Source: "-", Origin: "mutation-copy"},
	})
	return f, layout
}

// gcEmptyFixture is the copies fixture with an empty registry and no
// unregistered copies below its TMPDIR, with the s85 measurements faked
// the same way gcFixture fakes them.
func gcEmptyFixture(t *testing.T) *copiesFixture {
	t.Helper()
	f := newCopiesFixture(t, []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: agentIdleJSON}})
	f.stateDir(t)
	if err := core.WriteCopies(f.stateDir(t), []core.CopyRow{}); err != nil {
		t.Fatal(err)
	}
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

func TestGc(t *testing.T) {
	t.Run("dry-run lists the candidates and changes nothing", func(t *testing.T) {
		f, l := gcFixture(t)
		before, err := os.ReadFile(f.registryPath())
		if err != nil {
			t.Fatal(err)
		}
		code, out, errOut := f.run(t, f.env, "gc")
		if code != 0 || errOut != "" {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
		want := strings.Join([]string{
			"resource pressure: none (disk 60% free, swap 30% used)",
			"would remove " + l.oldOrphan + "  25 B  ghost  3h",
			"would remove " + l.missing + "  0 B  worker  3h",
			"total: 25 B",
			"run 'soho gc --yes' to remove them",
			gcUnregisteredHeader,
			l.unreg + "  40 B  3h  no origin marker",
		}, "\n") + "\n"
		if out != want {
			t.Fatalf("stdout=%q; want\n%s", out, want)
		}
		after, err := os.ReadFile(f.registryPath())
		if err != nil || string(after) != string(before) {
			t.Fatalf("the dry-run rewrote the registry:\nbefore %q\nafter  %q", before, after)
		}
		for _, path := range []string{filepath.Join(l.oldOrphan, "a.txt"), l.owned, l.unreg} {
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("the dry-run touched %s: %v", path, err)
			}
		}
	})

	t.Run("--yes removes only the old orphan and drops the missing line", func(t *testing.T) {
		f, l := gcFixture(t)
		code, out, errOut := f.run(t, f.env, "gc", "--yes")
		if code != 0 {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
		want := strings.Join([]string{
			"resource pressure: none (disk 60% free, swap 30% used)",
			"removed " + l.oldOrphan + "  25 B",
			"removed " + l.missing + "  0 B",
			gcUnregisteredHeader,
			l.unreg + "  40 B  3h  no origin marker",
			"freed: 25 B",
		}, "\n") + "\n"
		if out != want {
			t.Fatalf("stdout=%q; want\n%s", out, want)
		}
		if _, err := os.Stat(l.oldOrphan); !os.IsNotExist(err) {
			t.Fatalf("the old orphan was not removed: %v", err)
		}
		for _, path := range []string{l.youngOrphan, l.owned, l.unreg} {
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("%s was touched: %v", path, err)
			}
		}
		rows := f.copiesRows(t)
		if len(rows) != 2 || rows[0].Path != l.youngOrphan || rows[1].Path != l.owned {
			t.Fatalf("registry rows=%#v; want the young orphan and the owned copy only", rows)
		}
	})

	t.Run("unregistered leaves only with --include-unregistered --yes", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlinks need elevation on Windows without developer mode")
		}
		f, l := gcFixture(t)
		// A symlinked mutation copy: listed, and kept by the safety check.
		target := filepath.Join(f.tmp, "link-target")
		if err := os.MkdirAll(target, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(target, "t.txt"), []byte("0123456789"), 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(f.tmp, "soho-mutation-link")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		old := platform.Now().Add(-3 * time.Hour)
		if err := os.Chtimes(target, old, old); err != nil {
			t.Fatal(err)
		}
		// The two unregistered copies carry this project's marker, so the
		// origin check passes and the safety check decides: the real
		// directory is removed with its marker, the symlinked one is kept.
		thisProject := gitCommonDirOf(t, f.repo, f.env)
		writeTestOriginMarker(t, l.unreg, thisProject, f.repo)
		writeTestOriginMarker(t, link, thisProject, f.repo)
		// --yes without --include-unregistered leaves the unregistered copies.
		code, out, errOut := f.run(t, f.env, "gc", "--yes")
		if code != 0 || strings.Contains(out, "removed "+l.unreg) {
			t.Fatalf("code=%d out=%q err=%q; unregistered must stay", code, out, errOut)
		}
		if _, err := os.Stat(l.unreg); err != nil {
			t.Fatalf("the unregistered copy was removed without --include-unregistered: %v", err)
		}
		code, out, errOut = f.run(t, f.env, "gc", "--yes", "--include-unregistered")
		if code != 0 {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
		want := strings.Join([]string{
			"resource pressure: none (disk 60% free, swap 30% used)",
			gcUnregisteredHeader,
			"removed " + l.unreg + "  40 B",
			"kept " + link + " (is a symlink)",
			"freed: 40 B",
		}, "\n") + "\n"
		if out != want {
			t.Fatalf("stdout=%q; want\n%s", out, want)
		}
		if _, err := os.Stat(l.unreg); !os.IsNotExist(err) {
			t.Fatalf("the unregistered copy was not removed: %v", err)
		}
		if _, err := os.Lstat(l.unreg + ".soho-origin"); !os.IsNotExist(err) {
			t.Fatalf("the removed copy's marker was not removed: %v", err)
		}
		if _, err := os.Stat(link); err != nil {
			t.Fatalf("the kept symlink was touched: %v", err)
		}
		if _, err := os.Stat(filepath.Join(target, "t.txt")); err != nil {
			t.Fatalf("the symlink target was touched: %v", err)
		}
		rows := f.copiesRows(t)
		if len(rows) != 2 || rows[0].Path != l.youngOrphan || rows[1].Path != l.owned {
			t.Fatalf("registry rows=%#v; want the young orphan and the owned copy only", rows)
		}
	})

	t.Run("--older-than raises the threshold", func(t *testing.T) {
		f, l := gcFixture(t)
		code, out, errOut := f.run(t, f.env, "gc", "--older-than", "5")
		if code != 0 || errOut != "" {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
		if strings.Contains(out, l.oldOrphan) {
			t.Fatalf("a 3h old copy is a candidate at --older-than 5:\n%s", out)
		}
		if !strings.Contains(out, "would remove "+l.missing+"  0 B  worker  3h") {
			t.Fatalf("the missing line is a candidate regardless of the threshold:\n%s", out)
		}
		if _, err := os.Stat(l.oldOrphan); err != nil {
			t.Fatalf("the dry-run removed the old orphan: %v", err)
		}
	})

	t.Run("wrong usage exits 2", func(t *testing.T) {
		f, _ := gcFixture(t)
		for _, c := range []struct {
			args  []string
			cause string
		}{
			{args: []string{"gc", "--older-than"}, cause: gcUsage},
			{args: []string{"gc", "--older-than", "x"}, cause: "non-negative integer"},
			{args: []string{"gc", "--older-than", "-1"}, cause: "non-negative integer"},
			{args: []string{"gc", "--bogus"}, cause: gcUsage},
		} {
			code, out, errOut := f.run(t, f.env, c.args...)
			if code != 2 || !strings.Contains(errOut, c.cause) {
				t.Fatalf("args=%v: code=%d out=%q err=%q; want the refusal", c.args, code, out, errOut)
			}
		}
	})

	t.Run("dry run with nothing to remove prints only the line", func(t *testing.T) {
		f := gcEmptyFixture(t)
		code, out, errOut := f.run(t, f.env, "gc")
		if code != 0 || errOut != "" {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
		want := "resource pressure: none (disk 60% free, swap 30% used)\nnothing to remove\n"
		if out != want {
			t.Fatalf("stdout=%q; want %q", out, want)
		}
	})

	t.Run("dry run with only unregistered copies names the include flag", func(t *testing.T) {
		f := gcEmptyFixture(t)
		unreg := filepath.Join(f.tmp, "soho-mutation-zeta")
		if err := os.MkdirAll(unreg, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(unreg, "f.txt"), bytes.Repeat([]byte("0"), 40), 0o600); err != nil {
			t.Fatal(err)
		}
		old := platform.Now().Add(-3 * time.Hour)
		if err := os.Chtimes(unreg, old, old); err != nil {
			t.Fatal(err)
		}
		// The copy carries this project's marker: the hint stays because at
		// least one unregistered copy is this project's.
		writeTestOriginMarker(t, unreg, gitCommonDirOf(t, f.repo, f.env), f.repo)
		code, out, errOut := f.run(t, f.env, "gc")
		if code != 0 || errOut != "" {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
		want := "resource pressure: none (disk 60% free, swap 30% used)\n" +
			gcUnregisteredHeader + "\n" +
			unreg + "  40 B  3h  this project\n" +
			"run 'soho gc --yes --include-unregistered' to remove the unregistered ones\n"
		if out != want {
			t.Fatalf("stdout=%q; want\n%s", out, want)
		}
		// The dry run touches nothing.
		if _, err := os.Stat(filepath.Join(unreg, "f.txt")); err != nil {
			t.Fatalf("the dry-run touched the unregistered copy: %v", err)
		}
	})

	t.Run("--yes with nothing removed prints nothing to remove", func(t *testing.T) {
		f := gcEmptyFixture(t)
		code, out, errOut := f.run(t, f.env, "gc", "--yes")
		if code != 0 || errOut != "" {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
		want := "resource pressure: none (disk 60% free, swap 30% used)\nnothing to remove\n"
		if out != want {
			t.Fatalf("stdout=%q; want %q", out, want)
		}
	})
}

// The review case: a case variant of a protected directory is the same
// directory on the current macOS volume. Each subtest skips on a file
// system that distinguishes case.
func TestCopiesRefuseTheCaseVariantOfTheProtectedDirs(t *testing.T) {
	old := time.Now().Add(-3 * time.Hour)
	t.Run("a case variant of the HOME is kept by gc", func(t *testing.T) {
		f := newCopiesFixture(t, nil)
		upper := strings.ToUpper(f.home)
		if _, err := os.Stat(upper); err != nil {
			t.Skip("case sensitive volume")
		}
		if err := os.WriteFile(filepath.Join(f.home, "sentinel"), []byte("protected"), 0o600); err != nil {
			t.Fatal(err)
		}
		f.writeCopies(t, []core.CopyRow{{Path: upper, Owner: "-", Created: core.FrictionISO(old)}})
		code, out, stderr := f.run(t, f.env, "gc", "--yes")
		if _, err := os.Stat(filepath.Join(f.home, "sentinel")); err != nil {
			t.Fatalf("the fake HOME was touched: %v (code=%d out=%q stderr=%q)", err, code, out, stderr)
		}
		if code != 0 || !strings.Contains(out, "kept "+upper+" (is HOME or above it)") {
			t.Fatalf("code=%d out=%q; want kept with the HOME refusal", code, out)
		}
		if rows := f.copiesRows(t); len(rows) != 1 {
			t.Fatalf("the line left the registry: %#v", rows)
		}
	})
	t.Run("a case variant of the project root is kept by gc", func(t *testing.T) {
		f := newCopiesFixture(t, nil)
		upper := strings.ToUpper(f.repo)
		if _, err := os.Stat(upper); err != nil {
			t.Skip("case sensitive volume")
		}
		f.writeCopies(t, []core.CopyRow{{Path: upper, Owner: "-", Created: core.FrictionISO(old)}})
		code, out, stderr := f.run(t, f.env, "gc", "--yes")
		if _, err := os.Stat(filepath.Join(f.repo, ".git")); err != nil {
			t.Fatalf("the project root was touched: %v (code=%d out=%q stderr=%q)", err, code, out, stderr)
		}
		if code != 0 || !strings.Contains(out, "kept "+upper+" (is the project root or inside it)") {
			t.Fatalf("code=%d out=%q; want kept with the project root refusal", code, out)
		}
	})
	t.Run("a case variant of a path inside the project is kept by gc", func(t *testing.T) {
		f := newCopiesFixture(t, nil)
		sub := filepath.Join(f.repo, "sub")
		if err := os.MkdirAll(sub, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sub, "sentinel"), []byte("protected"), 0o600); err != nil {
			t.Fatal(err)
		}
		upper := strings.ToUpper(sub)
		if _, err := os.Stat(upper); err != nil {
			t.Skip("case sensitive volume")
		}
		f.writeCopies(t, []core.CopyRow{{Path: upper, Owner: "-", Created: core.FrictionISO(old)}})
		code, out, stderr := f.run(t, f.env, "gc", "--yes")
		if _, err := os.Stat(filepath.Join(sub, "sentinel")); err != nil {
			t.Fatalf("the path inside the project was touched: %v (code=%d out=%q stderr=%q)", err, code, out, stderr)
		}
		if code != 0 || !strings.Contains(out, "kept "+upper+" (is the project root or inside it)") {
			t.Fatalf("code=%d out=%q; want kept with the project root refusal", code, out)
		}
	})
}

// A live lock held for more than the (shortened) timeout: the commands exit
// 4 with the exact message and remove nothing.
func TestCopiesCommandsGiveUpOnALiveRegistryLock(t *testing.T) {
	lockPath := func(f *copiesFixture) string { return filepath.Join(f.state, "ws", "copies.tsv.lock") }
	heldLock := func(t *testing.T, f *copiesFixture) string {
		t.Helper()
		lock := lockPath(f)
		if err := os.WriteFile(lock, []byte(fmt.Sprintf("%d %s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339))), 0o600); err != nil {
			t.Fatal(err)
		}
		old := core.CopiesLockTimeout
		core.CopiesLockTimeout = 300 * time.Millisecond
		t.Cleanup(func() { core.CopiesLockTimeout = old })
		return lock
	}
	t.Run("copies add exits 4 without registering", func(t *testing.T) {
		f := newCopiesFixture(t, nil)
		_ = f.stateDir(t)
		lock := heldLock(t, f)
		dest := filepath.Join(f.tmp, "copy-locked")
		if err := os.MkdirAll(dest, 0o700); err != nil {
			t.Fatal(err)
		}
		code, out, stderr := f.run(t, f.env, "copies", "add", dest)
		if code != 4 {
			t.Fatalf("code=%d out=%q stderr=%q; want 4", code, out, stderr)
		}
		want := "soho: copies: registry is locked by another soho (" + lock + ")\n"
		if stderr != want {
			t.Fatalf("stderr=%q; want %q", stderr, want)
		}
		if rows := f.copiesRows(t); len(rows) != 0 {
			t.Fatalf("rows=%#v; want nothing registered", rows)
		}
	})
	t.Run("release still releases, keeps the copies and warns", func(t *testing.T) {
		f := newCopiesFixture(t, []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: agentIdleJSON},
			{Argv: []string{"pane", "report-metadata", "p-worker", "--source", "soho", "--clear-title"}},
			{Argv: []string{"pane", "list", "--workspace", "ws"}, Stdout: `{"result":{"panes":[]}}`},
		})
		f.writeRoster(t, copiesRosterRow("worker", "p-worker"))
		copy := filepath.Join(f.tmp, "copy-worker")
		if err := os.MkdirAll(copy, 0o700); err != nil {
			t.Fatal(err)
		}
		f.writeCopies(t, []core.CopyRow{{Path: copy, Owner: "worker", Pane: "p-worker", Created: core.FrictionISO(platform.Now()), Source: "-", Origin: "mutation-copy"}})
		addReleaseReport(t, filepath.Join(f.state, "ws"), "worker", false)
		_ = heldLock(t, f)
		code, out, stderr := f.run(t, f.env, "release", "worker")
		if code != 0 {
			t.Fatalf("code=%d out=%q stderr=%q; want 0", code, out, stderr)
		}
		if !strings.Contains(out, "released worker\n") || !strings.Contains(stderr, "the copies of 'worker' were not removed") {
			t.Fatalf("out=%q stderr=%q; want released and the warning", out, stderr)
		}
		if core.RosterLine(filepath.Join(f.state, "ws"), "worker") != "" {
			t.Fatalf("the roster still lists worker")
		}
		if _, err := os.Stat(copy); err != nil {
			t.Fatalf("the copy was removed despite the lock: %v", err)
		}
		if rows := f.copiesRows(t); len(rows) != 1 || rows[0].Owner != "worker" {
			t.Fatalf("rows=%#v; want the line kept for gc", rows)
		}
	})
	t.Run("gc --yes exits 4 and removes nothing", func(t *testing.T) {
		f := newCopiesFixture(t, nil)
		copy := filepath.Join(f.tmp, "copy-orphan")
		if err := os.MkdirAll(copy, 0o700); err != nil {
			t.Fatal(err)
		}
		f.writeCopies(t, []core.CopyRow{{Path: copy, Owner: "-", Created: core.FrictionISO(time.Now().Add(-3 * time.Hour))}})
		lock := heldLock(t, f)
		code, out, stderr := f.run(t, f.env, "gc", "--yes")
		if code != 4 {
			t.Fatalf("code=%d out=%q stderr=%q; want 4", code, out, stderr)
		}
		want := "soho: copies: registry is locked by another soho (" + lock + ")\n"
		if stderr != want {
			t.Fatalf("stderr=%q; want %q", stderr, want)
		}
		if _, err := os.Stat(copy); err != nil {
			t.Fatalf("the copy was removed despite the lock: %v", err)
		}
		if rows := f.copiesRows(t); len(rows) != 1 {
			t.Fatalf("rows=%#v; want the line to remain", rows)
		}
	})
}

// The removal stays bound to the directory the check validated: a parent
// swapped for a protected tree between the check and the removal is refused
// (the copy stays "changed after the check"), and without the swap the
// removal works.
func TestGcRemovalStaysBoundToTheValidatedParent(t *testing.T) {
	newCase := func(t *testing.T) (*copiesFixture, string) {
		t.Helper()
		f := newCopiesFixture(t, nil)
		copy := filepath.Join(f.tmp, "copy")
		if err := os.MkdirAll(copy, 0o700); err != nil {
			t.Fatal(err)
		}
		protect := filepath.Join(f.root, "protected")
		if err := os.MkdirAll(filepath.Join(protect, "copy"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(protect, "copy", "sentinel"), []byte("protected"), 0o600); err != nil {
			t.Fatal(err)
		}
		f.writeCopies(t, []core.CopyRow{{Path: copy, Owner: "-", Created: core.FrictionISO(time.Now().Add(-3 * time.Hour))}})
		return f, copy
	}
	t.Run("a parent swapped after the check keeps the target intact", func(t *testing.T) {
		f, copy := newCase(t)
		tmpHidden := filepath.Join(f.root, "tmp-hidden")
		protect := filepath.Join(f.root, "protected")
		t.Cleanup(func() { removeCopyHook = nil })
		// The hook runs between the decision-7 check and the removal: the
		// parent is swapped for the protected tree, which holds a "copy"
		// directory of its own.
		removeCopyHook = func(path string) {
			if err := os.Rename(f.tmp, tmpHidden); err != nil {
				t.Error(err)
			}
			if err := os.Rename(protect, f.tmp); err != nil {
				t.Error(err)
			}
		}
		code, out, stderr := f.run(t, f.env, "gc", "--yes")
		if _, err := os.Stat(filepath.Join(f.tmp, "copy", "sentinel")); err != nil {
			t.Fatalf("the protected tree was touched: %v (code=%d out=%q stderr=%q)", err, code, out, stderr)
		}
		if _, err := os.Stat(filepath.Join(tmpHidden, "copy")); err != nil {
			t.Fatalf("the original copy was touched: %v", err)
		}
		if code != 0 || !strings.Contains(out, "kept "+copy+" (changed after the check)") {
			t.Fatalf("code=%d out=%q; want kept (changed after the check)", code, out)
		}
		if rows := f.copiesRows(t); len(rows) != 1 {
			t.Fatalf("rows=%#v; want the line to remain", rows)
		}
	})
	t.Run("without the swap the removal works", func(t *testing.T) {
		f, copy := newCase(t)
		code, out, stderr := f.run(t, f.env, "gc", "--yes")
		if code != 0 || !strings.Contains(out, "removed "+copy) {
			t.Fatalf("code=%d out=%q stderr=%q; want removed", code, out, stderr)
		}
		if _, err := os.Stat(copy); !os.IsNotExist(err) {
			t.Fatalf("the copy still exists (stat err=%v)", err)
		}
		if rows := f.copiesRows(t); len(rows) != 0 {
			t.Fatalf("rows=%#v; want the line gone", rows)
		}
	})
}

// A copy the worker already deleted leaves the registry with its release:
// nothing is on disk to remove (G2).
func TestReleaseDropsTheAgentsMissingCopyLines(t *testing.T) {
	f := newCopiesFixture(t, []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Stdout: agentIdleJSON},
		{Argv: []string{"pane", "report-metadata", "p-worker", "--source", "soho", "--clear-title"}},
		{Argv: []string{"pane", "list", "--workspace", "ws"}, Stdout: `{"result":{"panes":[]}}`},
	})
	f.writeRoster(t, copiesRosterRow("worker", "p-worker"), copiesRosterRow("worker2", "p-worker2"))
	gone := filepath.Join(f.tmp, "deleted-by-the-worker")
	other := filepath.Join(f.tmp, "deleted-other")
	now := core.FrictionISO(platform.Now())
	f.writeCopies(t, []core.CopyRow{
		{Path: gone, Owner: "worker", Pane: "p-worker", Created: now, Source: "-", Origin: "mutation-copy"},
		{Path: other, Owner: "worker2", Pane: "p-worker2", Created: now, Source: "-", Origin: "mutation-copy"},
	})
	addReleaseReport(t, filepath.Join(f.state, "ws"), "worker", false)
	code, out, errOut := f.run(t, f.env, "release", "worker")
	if code != 0 || !strings.Contains(out, "dropped missing copy "+gone+"\n") || strings.Contains(errOut, "kept copy") {
		t.Fatalf("code=%d out=%q err=%q; want the missing line dropped", code, out, errOut)
	}
	if rows := f.copiesRows(t); len(rows) != 1 || rows[0].Path != other {
		t.Fatalf("rows=%#v; want only the other agent's line", rows)
	}
}

// agentStatusJSON is the fake herdr answer for an agent in state.
func agentStatusJSON(state string) string {
	return `{"result":{"agent":{"agent_status":"` + state + `"}}}`
}

// writeTaskPointer writes the agent's task-report pointer the way dispatch
// does (version 1, task_report, current, history).
func writeTaskPointer(t *testing.T, sd, agent, task, current string) {
	t.Helper()
	body := fmt.Sprintf(`{"version":1,"task_report":%q,"current":%q,"history":[]}`, task, current)
	if err := os.WriteFile(filepath.Join(sd, "task-report-"+agent+".json"), []byte(body+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeFileAt(t *testing.T, path, content string, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// idleGc is a live, idle owner "worker" with one copy whose task and the
// owner's current task are closed, everything 3 h old: the copy gc offers.
// Each case changes one thing.
type idleGc struct {
	f       *copiesFixture
	sd      string
	copy    string
	task    string
	report  string
	current string
	row     core.CopyRow
}

func newIdleGc(t *testing.T, rules []fakecli.Rule) *idleGc {
	t.Helper()
	return newIdleGcNamed(t, rules, "idle-copy")
}

// newIdleGcNamed is newIdleGc with the copy named name below TMPDIR.
func newIdleGcNamed(t *testing.T, rules []fakecli.Rule, name string) *idleGc {
	t.Helper()
	f := newCopiesFixture(t, rules)
	oldDisk, oldSwap := platform.DiskFree, platform.SwapUsage
	platform.DiskFree = func(string) (int64, int64, bool) {
		return int64(60) * 1024 * 1024 * 1024, int64(100) * 1024 * 1024 * 1024, true
	}
	platform.SwapUsage = func(platform.Env) (int64, int64, bool) {
		return int64(30) * 1024 * 1024 * 1024, int64(100) * 1024 * 1024 * 1024, true
	}
	t.Cleanup(func() { platform.DiskFree, platform.SwapUsage = oldDisk, oldSwap })
	f.writeRoster(t, copiesRosterRow("worker", "p-worker"))
	sd := f.stateDir(t)
	old := platform.Now().Add(-3 * time.Hour)
	g := &idleGc{
		f:       f,
		sd:      sd,
		copy:    filepath.Join(f.tmp, name),
		task:    filepath.Join(f.root, "reports", "worker.current.md"),
		report:  filepath.Join(f.root, "reports", "task-1.md"),
		current: filepath.Join(f.root, "reports", "task-2.md"),
	}
	writeFileAt(t, filepath.Join(g.copy, "sub", "a.txt"), "0123456789", old)
	for _, dir := range []string{filepath.Join(g.copy, "sub"), g.copy} {
		if err := os.Chtimes(dir, old, old); err != nil {
			t.Fatal(err)
		}
	}
	writeFileAt(t, g.report, "done\n", old)
	writeFileAt(t, g.current, "done\n", old)
	writeTaskPointer(t, sd, "worker", g.task, g.current)
	g.row = core.CopyRow{Path: g.copy, Owner: "worker", Pane: "p-worker", Created: core.FrictionISO(old), Source: "-", Origin: "mutation-copy", Task: g.task, Report: g.report}
	return g
}

func (g *idleGc) register(t *testing.T) {
	t.Helper()
	g.f.writeCopies(t, []core.CopyRow{g.row})
}

func idleRules(state string) []fakecli.Rule {
	return []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: agentStatusJSON(state)}}
}

const gcPressureNone = "resource pressure: none (disk 60% free, swap 30% used)"

func (g *idleGc) wantOffered(t *testing.T) {
	t.Helper()
	code, out, errOut := g.f.run(t, g.f.env, "gc")
	want := strings.Join([]string{
		gcPressureNone,
		"would remove " + g.copy + "  10 B  worker  3h  (idle owner, task closed)",
		"total: 10 B",
		"run 'soho gc --yes' to remove them",
	}, "\n") + "\n"
	if code != 0 || errOut != "" || out != want {
		t.Fatalf("code=%d out=%q err=%q; want the copy offered:\n%s", code, out, errOut, want)
	}
}

func (g *idleGc) wantKept(t *testing.T) {
	t.Helper()
	code, out, errOut := g.f.run(t, g.f.env, "gc")
	if want := gcPressureNone + "\nnothing to remove\n"; code != 0 || errOut != "" || out != want {
		t.Fatalf("code=%d out=%q err=%q; want the copy not offered", code, out, errOut)
	}
	code, out, errOut = g.f.run(t, g.f.env, "gc", "--yes")
	if want := gcPressureNone + "\nnothing to remove\n"; code != 0 || out != want {
		t.Fatalf("--yes code=%d out=%q err=%q; want nothing removed", code, out, errOut)
	}
	if _, err := os.Stat(filepath.Join(g.copy, "sub", "a.txt")); err != nil {
		t.Fatalf("the copy was touched: %v", err)
	}
	if rows := g.f.copiesRows(t); len(rows) != 1 || rows[0].Path != g.copy {
		t.Fatalf("registry rows=%#v; want the line kept", rows)
	}
}

func TestGcIdleOwnerCopies(t *testing.T) {
	// Condition 1: the owner is live in the roster and idle or done.
	t.Run("idle owner with closed tasks: offered, then removed with --yes", func(t *testing.T) {
		g := newIdleGc(t, idleRules("idle"))
		g.register(t)
		g.wantOffered(t)
		code, out, errOut := g.f.run(t, g.f.env, "gc", "--yes")
		want := strings.Join([]string{gcPressureNone, "removed " + g.copy + "  10 B", "freed: 10 B"}, "\n") + "\n"
		if code != 0 || out != want {
			t.Fatalf("code=%d out=%q err=%q; want\n%s", code, out, errOut, want)
		}
		if _, err := os.Stat(g.copy); !os.IsNotExist(err) {
			t.Fatalf("the copy was not removed: %v", err)
		}
		if rows := g.f.copiesRows(t); len(rows) != 0 {
			t.Fatalf("registry rows=%#v; want the line dropped", rows)
		}
	})
	t.Run("done owner: offered", func(t *testing.T) {
		g := newIdleGc(t, idleRules("done"))
		g.register(t)
		g.wantOffered(t)
	})
	for _, state := range []string{"working", "blocked"} {
		t.Run(state+" owner: kept", func(t *testing.T) {
			g := newIdleGc(t, idleRules(state))
			g.register(t)
			g.wantKept(t)
		})
	}
	t.Run("unavailable owner: kept", func(t *testing.T) {
		g := newIdleGc(t, []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Code: 1, Stderr: "herdr is not answering"}})
		g.register(t)
		g.wantKept(t)
	})

	// Condition 2: the line carries the task and the report.
	t.Run("task - : kept", func(t *testing.T) {
		g := newIdleGc(t, idleRules("idle"))
		g.row.Task = "-"
		g.register(t)
		g.wantKept(t)
	})
	t.Run("report - : kept", func(t *testing.T) {
		g := newIdleGc(t, idleRules("idle"))
		g.row.Report = "-"
		g.register(t)
		g.wantKept(t)
	})
	t.Run("an empty task column with a report: kept", func(t *testing.T) {
		g := newIdleGc(t, idleRules("idle"))
		line := strings.Join([]string{g.row.Path, g.row.Owner, g.row.Pane, g.row.Created, g.row.Source, g.row.Origin, "", g.row.Report}, "\t")
		if err := os.WriteFile(g.f.registryPath(), []byte("path\towner\tpane\tcreated\tsource\torigin\n"+line+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		g.wantKept(t)
	})
	t.Run("a six-column line from an earlier binary: kept", func(t *testing.T) {
		g := newIdleGc(t, idleRules("idle"))
		legacy := "path\towner\tpane\tcreated\tsource\torigin\n" +
			strings.Join([]string{g.row.Path, g.row.Owner, g.row.Pane, g.row.Created, g.row.Source, g.row.Origin}, "\t") + "\n"
		if err := os.WriteFile(g.f.registryPath(), []byte(legacy), 0o600); err != nil {
			t.Fatal(err)
		}
		g.wantKept(t)
	})

	// Condition 3: the line's report exists and is not empty.
	t.Run("empty report: kept", func(t *testing.T) {
		g := newIdleGc(t, idleRules("idle"))
		writeFileAt(t, g.report, "", platform.Now().Add(-3*time.Hour))
		g.register(t)
		g.wantKept(t)
	})
	t.Run("missing report: kept", func(t *testing.T) {
		g := newIdleGc(t, idleRules("idle"))
		if err := os.Remove(g.report); err != nil {
			t.Fatal(err)
		}
		g.register(t)
		g.wantKept(t)
	})

	// Condition 4: the owner's current task is closed too.
	t.Run("current task open (no current report yet): kept", func(t *testing.T) {
		g := newIdleGc(t, idleRules("idle"))
		writeTaskPointer(t, g.sd, "worker", g.task, filepath.Join(g.f.root, "reports", "task-3.md"))
		g.register(t)
		g.wantKept(t)
	})
	t.Run("current report empty: kept", func(t *testing.T) {
		g := newIdleGc(t, idleRules("idle"))
		writeFileAt(t, g.current, "", platform.Now().Add(-3*time.Hour))
		g.register(t)
		g.wantKept(t)
	})
	t.Run("no current pointer: kept", func(t *testing.T) {
		g := newIdleGc(t, idleRules("idle"))
		if err := os.Remove(filepath.Join(g.sd, "task-report-worker.json")); err != nil {
			t.Fatal(err)
		}
		g.register(t)
		g.wantKept(t)
	})

	// Condition 5: created at least --older-than ago.
	t.Run("created inside --older-than: kept", func(t *testing.T) {
		g := newIdleGc(t, idleRules("idle"))
		g.row.Created = core.FrictionISO(platform.Now().Add(-time.Hour))
		g.register(t)
		g.wantKept(t)
	})
	t.Run("--older-than 4 against a 3 h copy: kept; --older-than 3: offered", func(t *testing.T) {
		g := newIdleGc(t, idleRules("idle"))
		g.register(t)
		code, out, errOut := g.f.run(t, g.f.env, "gc", "--older-than", "4")
		if want := gcPressureNone + "\nnothing to remove\n"; code != 0 || out != want {
			t.Fatalf("code=%d out=%q err=%q; want nothing offered", code, out, errOut)
		}
		code, out, errOut = g.f.run(t, g.f.env, "gc", "--older-than", "3")
		if code != 0 || !strings.Contains(out, "would remove "+g.copy+"  10 B  worker  3h  (idle owner, task closed)\n") {
			t.Fatalf("code=%d out=%q err=%q; want the copy offered", code, out, errOut)
		}
	})

	// Condition 6: no entry of the copy was modified inside --older-than.
	t.Run("a file of the copy modified inside --older-than: kept", func(t *testing.T) {
		g := newIdleGc(t, idleRules("idle"))
		recent := platform.Now().Add(-time.Hour)
		if err := os.Chtimes(filepath.Join(g.copy, "sub", "a.txt"), recent, recent); err != nil {
			t.Fatal(err)
		}
		g.register(t)
		g.wantKept(t)
	})
	t.Run("the copy root modified inside --older-than: kept", func(t *testing.T) {
		g := newIdleGc(t, idleRules("idle"))
		recent := platform.Now().Add(-time.Hour)
		if err := os.Chtimes(g.copy, recent, recent); err != nil {
			t.Fatal(err)
		}
		g.register(t)
		g.wantKept(t)
	})

	// Before each removal gc re-reads the owner: a change keeps the copy.
	t.Run("the owner turns working between the listing and the removal: kept", func(t *testing.T) {
		// gc --yes asks herdr three times for this line: the live check,
		// the idle rule at the listing, and the re-read before removing.
		g := newIdleGc(t, []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Call: 3, Stdout: agentStatusJSON("working")},
			{Argv: []string{"agent", "get", "worker"}, Stdout: agentStatusJSON("idle")},
		})
		g.register(t)
		code, out, errOut := g.f.run(t, g.f.env, "gc", "--yes")
		want := strings.Join([]string{gcPressureNone, "kept " + g.copy + " (the owner's state or task changed)", "nothing to remove"}, "\n") + "\n"
		if code != 0 || out != want {
			t.Fatalf("code=%d out=%q err=%q; want\n%s", code, out, errOut, want)
		}
		if _, err := os.Stat(filepath.Join(g.copy, "sub", "a.txt")); err != nil {
			t.Fatalf("the copy was removed: %v", err)
		}
		if rows := g.f.copiesRows(t); len(rows) != 1 || rows[0] != g.row {
			t.Fatalf("registry rows=%#v; want the line kept as it was", rows)
		}
		calls, err := fakecli.ReadCallsForConfig(filepath.Join(g.f.env["SOHO_FAKECLI_CONFIG"], "herdr.json"))
		if err != nil {
			t.Fatal(err)
		}
		if got := len(callsTo(calls, "agent", "get", "worker")); got != 3 {
			t.Fatalf("agent get calls=%d; want 3 (live check, rule, re-read)", got)
		}
	})
}

func TestCopiesRecordTheOwnersTask(t *testing.T) {
	f := newCopiesFixture(t, []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: agentIdleJSON}})
	f.writeRoster(t, copiesRosterRow("worker", "p-worker"))
	sd := f.stateDir(t)
	src := filepath.Join(f.tmp, "handcopy")
	if err := os.MkdirAll(src, 0o700); err != nil {
		t.Fatal(err)
	}
	// Without a pointer the two columns are "-".
	if code, out, errOut := f.run(t, f.env, "copies", "add", src); code != 0 || errOut != "" {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	if rows := f.copiesRows(t); len(rows) != 1 || rows[0].Task != "-" || rows[0].Report != "-" {
		t.Fatalf("registry rows=%#v; want task and report -", rows)
	}
	// With the owner's pointer they carry its task_report and current, and a
	// repeated add on the same path updates them.
	for _, n := range []string{"1", "2"} {
		task := filepath.Join(f.root, "reports", "worker.current.md")
		current := filepath.Join(f.root, "reports", "task-"+n+".md")
		writeTaskPointer(t, sd, "worker", task, current)
		if code, out, errOut := f.run(t, f.env, "copies", "add", src); code != 0 || errOut != "" {
			t.Fatalf("task %s: code=%d out=%q err=%q", n, code, out, errOut)
		}
		rows := f.copiesRows(t)
		if len(rows) != 1 || rows[0].Path != src || rows[0].Task != task || rows[0].Report != current {
			t.Fatalf("task %s: registry rows=%#v; want one line with task %q and report %q", n, rows, task, current)
		}
	}
	raw, err := os.ReadFile(f.registryPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(raw), "path\towner\tpane\tcreated\tsource\torigin\n") || !strings.Contains(string(raw), "\tadd\t"+filepath.Join(f.root, "reports", "worker.current.md")+"\t"+filepath.Join(f.root, "reports", "task-2.md")+"\n") {
		t.Fatalf("registry=%q; want the six-name header and the task and report as fields 7 and 8", raw)
	}
	// An owner-less registration (no pane in the roster) is "-" even when
	// some agent has a pointer.
	env := f.env.Clone()
	delete(env, "HERDR_PANE_ID")
	other := filepath.Join(f.tmp, "handcopy-other")
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	if code, out, errOut := f.run(t, env, "copies", "add", other); code != 0 || errOut != "" {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	rows := f.copiesRows(t)
	if len(rows) != 2 || rows[1].Path != other || rows[1].Owner != "-" || rows[1].Task != "-" || rows[1].Report != "-" {
		t.Fatalf("registry rows=%#v; want the owner-less line with task and report -", rows)
	}
}

func TestMutationCopyRecordsTheOwnersTask(t *testing.T) {
	f := newMutationCopyFixture(t)
	skill := filepath.Join(f.root, "skill")
	if err := os.MkdirAll(filepath.Join(skill, "roles"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("fixture skill\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(f.root, "state")
	sd := filepath.Join(state, "ws")
	if err := os.MkdirAll(sd, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sd, "agents.tsv"), []byte("# roster\n"+releaseRow("worker", "p-worker", "1", "implementer", false, "/tmp/work")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(f.root, "mcopy")
	overrides := platform.Env{"SOHO_DIR": state, "SOHO_SKILL_DIR": skill, "HERDR_WORKSPACE_ID": "ws", "HERDR_PANE_ID": "p-worker"}

	// Without the owner's pointer the two columns are "-".
	if code, out, errOut := runMutationCopyCmd(t, f, f.source, overrides, "--source", f.source, "--dest", dest); code != 0 || errOut != "" {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	rows, err := core.ReadCopies(sd)
	if err != nil || len(rows) != 1 || rows[0].Owner != "worker" || rows[0].Task != "-" || rows[0].Report != "-" {
		t.Fatalf("registry rows=%#v err=%v; want task and report -", rows, err)
	}

	// With it, the task_report and the current of the pointer.
	task := filepath.Join(f.root, "reports", "worker.current.md")
	current := filepath.Join(f.root, "reports", "task-1.md")
	writeTaskPointer(t, sd, "worker", task, current)
	if err := os.RemoveAll(dest); err != nil {
		t.Fatal(err)
	}
	if code, out, errOut := runMutationCopyCmd(t, f, f.source, overrides, "--source", f.source, "--dest", dest); code != 0 || errOut != "" {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	rows, err = core.ReadCopies(sd)
	if err != nil || len(rows) != 1 || rows[0].Path != dest || rows[0].Task != task || rows[0].Report != current {
		t.Fatalf("registry rows=%#v err=%v; want task %q and report %q", rows, err, task, current)
	}
}

// pauseAtAgentGet rewrites this fixture's fake herdr so that the call-th
// "agent get worker" waits for a gate file, every call answering idle. A
// goroutine waits for that call to start, runs action, then opens the gate.
// The returned channel carries action's error.
func (g *idleGc) pauseAtAgentGet(t *testing.T, call int, action func() error) <-chan error {
	t.Helper()
	cfg := filepath.Join(g.f.env["SOHO_FAKECLI_CONFIG"], "herdr.json")
	raw, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var script map[string]any
	if err := json.Unmarshal(raw, &script); err != nil {
		t.Fatal(err)
	}
	gate := filepath.Join(g.f.root, "gate")
	script["rules"] = []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Call: call, WaitFile: gate, Stdout: agentStatusJSON("idle")},
		{Argv: []string{"agent", "get", "worker"}, Stdout: agentStatusJSON("idle")},
	}
	if raw, err = json.Marshal(script); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		defer func() { _ = os.WriteFile(gate, []byte("go"), 0o600) }()
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			if calls, err := fakecli.ReadCallsForConfig(cfg); err == nil && len(callsTo(calls, "agent", "get", "worker")) >= call {
				result <- action()
				return
			}
			time.Sleep(time.Millisecond)
		}
		result <- fmt.Errorf("agent get call %d was never reached", call)
	}()
	return result
}

// gc --yes asks herdr about this line three times: the live check (1) and
// the idle rule (2) at the listing, then the re-read (3) before the locked
// removal section.
func TestGcIdleOwnerCopyReadsTheCurrentLine(t *testing.T) {
	// The reviewer's probe (review-3-20261019T203439, TestReview392ReRegistrationDuringGc):
	// another worker re-registers the copy for an open task after the
	// listing; gc keeps the copy and the new line.
	t.Run("re-registered by another worker after the listing: kept with the new line", func(t *testing.T) {
		g := newIdleGc(t, nil)
		g.f.writeRoster(t, copiesRosterRow("worker", "p-worker"), copiesRosterRow("worker2", "p-worker2"))
		writeTaskPointer(t, g.sd, "worker2", filepath.Join(g.f.root, "new-task.current.md"), filepath.Join(g.f.root, "not-yet-written.md"))
		g.register(t)
		fresh := g.row
		fresh.Owner, fresh.Pane = "worker2", "p-worker2"
		fresh.Task, fresh.Report = filepath.Join(g.f.root, "new-task.current.md"), filepath.Join(g.f.root, "not-yet-written.md")
		fresh.Created = core.FrictionISO(platform.Now())
		done := g.pauseAtAgentGet(t, 2, func() error { return core.UpsertCopies(g.sd, fresh) })
		code, out, errOut := g.f.run(t, g.f.env, "gc", "--yes")
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		want := strings.Join([]string{gcPressureNone, "kept " + g.copy + " (the owner's state or task changed)", "nothing to remove"}, "\n") + "\n"
		if code != 0 || out != want {
			t.Fatalf("code=%d out=%q err=%q; want\n%s", code, out, errOut, want)
		}
		if _, err := os.Stat(filepath.Join(g.copy, "sub", "a.txt")); err != nil {
			t.Fatalf("the re-registered copy was removed: %v", err)
		}
		if rows := g.f.copiesRows(t); len(rows) != 1 || rows[0] != fresh {
			t.Fatalf("registry rows=%#v; want only the new line %#v", rows, fresh)
		}
	})
	t.Run("re-registered by the same owner, only created changes: kept", func(t *testing.T) {
		g := newIdleGc(t, nil)
		g.register(t)
		fresh := g.row
		fresh.Created = core.FrictionISO(platform.Now().Add(-150 * time.Minute))
		done := g.pauseAtAgentGet(t, 2, func() error { return core.UpsertCopies(g.sd, fresh) })
		code, out, errOut := g.f.run(t, g.f.env, "gc", "--yes")
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if code != 0 || !strings.Contains(out, "kept "+g.copy+" (the owner's state or task changed)\n") {
			t.Fatalf("code=%d out=%q err=%q; want the copy kept", code, out, errOut)
		}
		if _, err := os.Stat(filepath.Join(g.copy, "sub", "a.txt")); err != nil {
			t.Fatalf("the copy was removed: %v", err)
		}
		if rows := g.f.copiesRows(t); len(rows) != 1 || rows[0] != fresh {
			t.Fatalf("registry rows=%#v; want the new line %#v", rows, fresh)
		}
	})
	t.Run("the line leaves the registry after the listing: kept", func(t *testing.T) {
		g := newIdleGc(t, nil)
		g.register(t)
		done := g.pauseAtAgentGet(t, 2, func() error { return core.DropCopiesLines(g.sd, map[string]bool{g.copy: true}) })
		code, out, errOut := g.f.run(t, g.f.env, "gc", "--yes")
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		want := strings.Join([]string{gcPressureNone, "kept " + g.copy + " (the owner's state or task changed)", "nothing to remove"}, "\n") + "\n"
		if code != 0 || out != want {
			t.Fatalf("code=%d out=%q err=%q; want\n%s", code, out, errOut, want)
		}
		if _, err := os.Stat(filepath.Join(g.copy, "sub", "a.txt")); err != nil {
			t.Fatalf("the copy was removed: %v", err)
		}
		if rows := g.f.copiesRows(t); len(rows) != 0 {
			t.Fatalf("registry rows=%#v; want none", rows)
		}
	})
	// Control: an unchanged line goes, with the same pause in place.
	t.Run("the line unchanged after the listing: removed", func(t *testing.T) {
		g := newIdleGc(t, nil)
		g.register(t)
		done := g.pauseAtAgentGet(t, 2, func() error { return nil })
		code, out, errOut := g.f.run(t, g.f.env, "gc", "--yes")
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		want := strings.Join([]string{gcPressureNone, "removed " + g.copy + "  10 B", "freed: 10 B"}, "\n") + "\n"
		if code != 0 || out != want {
			t.Fatalf("code=%d out=%q err=%q; want\n%s", code, out, errOut, want)
		}
		if rows := g.f.copiesRows(t); len(rows) != 0 {
			t.Fatalf("registry rows=%#v; want the line dropped", rows)
		}
	})
}

// The removal section holds the copies.tsv lock: a registration of the same
// path (UpsertCopies, what copies add runs) cannot take it inside the
// section, and one that waits goes in after gc dropped the old line, so gc
// never drops it.
func TestGcIdleOwnerRemovalHoldsTheRegistryLock(t *testing.T) {
	old := core.CopiesLockTimeout
	core.CopiesLockTimeout = 300 * time.Millisecond
	t.Cleanup(func() { core.CopiesLockTimeout = old })
	g := newIdleGc(t, idleRules("idle"))
	g.register(t)
	fresh := g.row
	fresh.Created = core.FrictionISO(platform.Now())
	waited := make(chan error, 1)
	done := make(chan error, 1)
	g.inSection(t, func(string) {
		// Inside the section: the lock is held, so a registration gives up
		// with the lock error.
		err := core.UpsertCopies(g.sd, fresh)
		var locked *core.CopiesLockedError
		if !errors.As(err, &locked) {
			done <- fmt.Errorf("UpsertCopies inside gc's section returned %v; want the registry lock error", err)
			return
		}
		done <- nil
		// A registration that keeps trying goes in once gc is done.
		go func() {
			deadline := time.Now().Add(20 * time.Second)
			for {
				err := core.UpsertCopies(g.sd, fresh)
				if err == nil || !errors.As(err, &locked) || time.Now().After(deadline) {
					waited <- err
					return
				}
			}
		}()
	})
	code, out, errOut := g.f.run(t, g.f.env, "gc", "--yes")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("gc never reached the locked section: code=%d out=%q err=%q", code, out, errOut)
	}
	if err := <-waited; err != nil {
		t.Fatalf("the waiting registration failed: %v", err)
	}
	want := strings.Join([]string{gcPressureNone, "removed " + g.copy + "  10 B", "freed: 10 B"}, "\n") + "\n"
	if code != 0 || out != want {
		t.Fatalf("code=%d out=%q err=%q; want\n%s", code, out, errOut, want)
	}
	if rows := g.f.copiesRows(t); len(rows) != 1 || rows[0] != fresh {
		t.Fatalf("registry rows=%#v; want only the registration that waited, %#v", rows, fresh)
	}
}

// inSection sets gcIdleSectionHook for the test: fn runs right after the
// removal section takes the registry lock.
func (g *idleGc) inSection(t *testing.T, fn func(path string)) {
	t.Helper()
	old := gcIdleSectionHook
	gcIdleSectionHook = fn
	t.Cleanup(func() { gcIdleSectionHook = old })
}

// atRemoval sets removeCopyHook for the test: fn runs right before the
// directory's removal.
func (g *idleGc) atRemoval(t *testing.T, fn func(path string)) {
	t.Helper()
	old := removeCopyHook
	removeCopyHook = fn
	t.Cleanup(func() { removeCopyHook = old })
}

// lockHeld reports whether copies.tsv.lock exists.
func (g *idleGc) lockHeld() bool {
	_, err := os.Lstat(filepath.Join(g.sd, "copies.tsv.lock"))
	return err == nil
}

// agentGets counts the "agent get worker" calls the fake herdr received.
func (g *idleGc) agentGets(t *testing.T) int {
	t.Helper()
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(g.f.env["SOHO_FAKECLI_CONFIG"], "herdr.json"))
	if err != nil {
		t.Fatal(err)
	}
	return len(callsTo(calls, "agent", "get", "worker"))
}

// setAgentRules rewrites this fixture's fake herdr rules, keeping its calls.
func (g *idleGc) setAgentRules(t *testing.T, rules []fakecli.Rule) {
	t.Helper()
	cfg := filepath.Join(g.f.env["SOHO_FAKECLI_CONFIG"], "herdr.json")
	raw, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var script map[string]any
	if err := json.Unmarshal(raw, &script); err != nil {
		t.Fatal(err)
	}
	script["rules"] = rules
	if raw, err = json.Marshal(script); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// discard is the name this gc process gives the copy first.
func (g *idleGc) discard() string {
	return fmt.Sprintf("%s.soho-gc-%d-1", g.copy, os.Getpid())
}

// The reviewer's probe (review-3-20261019T210329,
// TestReview392LongRemovalKeepsFreshRegistration) paused gc inside the
// locked section until the lock was 61 s old, and a registration then took
// it over. The slow steps now run outside the lock: a pause there leaves no
// lock to age, before the section (the herdr re-read) or after it (the
// removal of the renamed directory).
func TestGcIdleOwnerLongPauseStaysOutsideTheLock(t *testing.T) {
	t.Run("a long pause before the lock: a registration for an open task goes in, the copy stays", func(t *testing.T) {
		old := core.CopiesLockTimeout
		core.CopiesLockTimeout = 300 * time.Millisecond
		t.Cleanup(func() { core.CopiesLockTimeout = old })
		g := newIdleGc(t, nil)
		g.register(t)
		fresh := g.row
		fresh.Created = core.FrictionISO(platform.Now())
		fresh.Task, fresh.Report = filepath.Join(g.f.root, "open.current.md"), filepath.Join(g.f.root, "open.report.md")
		done := g.pauseAtAgentGet(t, 3, func() error {
			if g.lockHeld() {
				return errors.New("the registry lock is held during the herdr re-read")
			}
			return core.UpsertCopies(g.sd, fresh)
		})
		code, out, errOut := g.f.run(t, g.f.env, "gc", "--yes")
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		want := strings.Join([]string{gcPressureNone, "kept " + g.copy + " (the owner's state or task changed)", "nothing to remove"}, "\n") + "\n"
		if code != 0 || out != want {
			t.Fatalf("code=%d out=%q err=%q; want\n%s", code, out, errOut, want)
		}
		if _, err := os.Stat(filepath.Join(g.copy, "sub", "a.txt")); err != nil {
			t.Fatalf("the re-registered copy was removed: %v", err)
		}
		if rows := g.f.copiesRows(t); len(rows) != 1 || rows[0] != fresh {
			t.Fatalf("registry rows=%#v; want only the new line %#v", rows, fresh)
		}
	})
	t.Run("a long pause after the rename: the copy is gone, the registration stays", func(t *testing.T) {
		old := core.CopiesLockTimeout
		core.CopiesLockTimeout = 300 * time.Millisecond
		t.Cleanup(func() { core.CopiesLockTimeout = old })
		g := newIdleGc(t, idleRules("idle"))
		g.register(t)
		fresh := g.row
		fresh.Created = core.FrictionISO(platform.Now())
		fresh.Task, fresh.Report = filepath.Join(g.f.root, "open.current.md"), filepath.Join(g.f.root, "open.report.md")
		problems := []string{}
		g.atRemoval(t, func(path string) {
			if g.lockHeld() {
				problems = append(problems, "the registry lock is held during the removal")
			}
			if path != g.discard() {
				problems = append(problems, "removing "+path+", not the discard "+g.discard())
			}
			if _, err := os.Lstat(g.copy); !os.IsNotExist(err) {
				problems = append(problems, fmt.Sprintf("the copy's path is still there before the removal: %v", err))
			}
			if err := core.UpsertCopies(g.sd, fresh); err != nil {
				problems = append(problems, fmt.Sprintf("registration during the removal: %v", err))
			}
		})
		code, out, errOut := g.f.run(t, g.f.env, "gc", "--yes")
		if len(problems) > 0 {
			t.Fatal(strings.Join(problems, "; "))
		}
		want := strings.Join([]string{gcPressureNone, "removed " + g.copy + "  10 B", "freed: 10 B"}, "\n") + "\n"
		if code != 0 || out != want {
			t.Fatalf("code=%d out=%q err=%q; want\n%s", code, out, errOut, want)
		}
		for _, path := range []string{g.copy, g.discard()} {
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatalf("%s is still there: %v", path, err)
			}
		}
		if rows := g.f.copiesRows(t); len(rows) != 1 || rows[0] != fresh {
			t.Fatalf("registry rows=%#v; want only the registration made after the rename, %#v", rows, fresh)
		}
	})
}

// The locked section runs file operations only: no herdr call and no walk
// of the copy's tree (both ran before it), the line and the task pointer
// re-read, the rename and the line's drop.
func TestGcIdleOwnerLockedSectionRunsFileOperationsOnly(t *testing.T) {
	t.Run("no herdr call and no tree walk under the lock: removed", func(t *testing.T) {
		g := newIdleGc(t, idleRules("idle"))
		g.register(t)
		var sectionAt, removalAt time.Time
		sectionGets, removalGets := -1, -1
		problems := []string{}
		g.inSection(t, func(string) {
			sectionAt = time.Now()
			sectionGets = g.agentGets(t)
			// A herdr call from here on answers working, and a walk from
			// here on finds a fresh file: either one would keep the copy.
			g.setAgentRules(t, idleRules("working"))
			now := time.Now()
			if err := os.Chtimes(filepath.Join(g.copy, "sub", "a.txt"), now, now); err != nil {
				problems = append(problems, err.Error())
			}
		})
		g.atRemoval(t, func(string) {
			removalAt = time.Now()
			removalGets = g.agentGets(t)
			if g.lockHeld() {
				problems = append(problems, "the registry lock is held during the removal")
			}
		})
		code, out, errOut := g.f.run(t, g.f.env, "gc", "--yes")
		if len(problems) > 0 {
			t.Fatal(strings.Join(problems, "; "))
		}
		want := strings.Join([]string{gcPressureNone, "removed " + g.copy + "  10 B", "freed: 10 B"}, "\n") + "\n"
		if code != 0 || out != want {
			t.Fatalf("code=%d out=%q err=%q; want\n%s", code, out, errOut, want)
		}
		if sectionGets != 3 || removalGets != sectionGets {
			t.Fatalf("agent get calls: %d when the section started, %d at the removal; want 3 and 3 (live check, rule, re-read, none under the lock)", sectionGets, removalGets)
		}
		// From the lock taken to the removal of the renamed directory: the
		// section and the discard's check, an upper bound of the section.
		section := removalAt.Sub(sectionAt)
		t.Logf("locked section (lock taken to removal start): %s", section)
		if section <= 0 || section > 2*time.Second {
			t.Fatalf("locked section took %s; want a few milliseconds", section)
		}
	})
	t.Run("the task pointer moves under the lock: kept", func(t *testing.T) {
		g := newIdleGc(t, idleRules("idle"))
		g.register(t)
		g.inSection(t, func(string) {
			writeTaskPointer(t, g.sd, "worker", filepath.Join(g.f.root, "open.current.md"), filepath.Join(g.f.root, "open.report.md"))
		})
		g.wantKeptBySection(t)
	})
	t.Run("the current report is emptied under the lock: kept", func(t *testing.T) {
		g := newIdleGc(t, idleRules("idle"))
		g.register(t)
		g.inSection(t, func(string) {
			if err := os.WriteFile(g.current, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		})
		g.wantKeptBySection(t)
	})
}

// wantKeptBySection runs gc --yes and wants the copy, its line and no
// discard left.
func (g *idleGc) wantKeptBySection(t *testing.T) {
	t.Helper()
	code, out, errOut := g.f.run(t, g.f.env, "gc", "--yes")
	want := strings.Join([]string{gcPressureNone, "kept " + g.copy + " (the owner's state or task changed)", "nothing to remove"}, "\n") + "\n"
	if code != 0 || out != want {
		t.Fatalf("code=%d out=%q err=%q; want\n%s", code, out, errOut, want)
	}
	if _, err := os.Stat(filepath.Join(g.copy, "sub", "a.txt")); err != nil {
		t.Fatalf("the copy was touched: %v", err)
	}
	if _, err := os.Lstat(g.discard()); !os.IsNotExist(err) {
		t.Fatalf("a discard was made: %v", err)
	}
	if rows := g.f.copiesRows(t); len(rows) != 1 || rows[0] != g.row {
		t.Fatalf("registry rows=%#v; want the line kept as it was", rows)
	}
}

// failRemoval makes the removal of the discard fail once: the entry below
// it cannot be unlinked. A read-only directory does that on Unix; on
// Windows, where a directory's mode does not, a handle held open on the
// file does (Go opens files without FILE_SHARE_DELETE). It returns the
// function that lets it succeed.
func (g *idleGc) failRemoval(t *testing.T) func() {
	t.Helper()
	locked := filepath.Join(g.discard(), "sub")
	var held *os.File
	release := func() error {
		if held != nil {
			_ = held.Close()
			held = nil
		}
		return os.Chmod(locked, 0o700)
	}
	t.Cleanup(func() { _ = release() })
	fail := true
	g.atRemoval(t, func(string) {
		if !fail {
			return
		}
		if runtime.GOOS != "windows" {
			_ = os.Chmod(locked, 0o500)
			return
		}
		f, err := os.Open(filepath.Join(locked, "a.txt"))
		if err != nil {
			t.Errorf("hold the file open: %v", err)
			return
		}
		held = f
	})
	return func() {
		fail = false
		if err := release(); err != nil {
			t.Fatal(err)
		}
	}
}

// ageDiscard sets the discard's mtime.
func (g *idleGc) ageDiscard(t *testing.T, at time.Time) {
	t.Helper()
	if err := os.Chtimes(g.discard(), at, at); err != nil {
		t.Fatal(err)
	}
}

// wantDiscardKept fails unless the discard and its marker are still there.
func (g *idleGc) wantDiscardKept(t *testing.T) {
	t.Helper()
	for _, path := range []string{filepath.Join(g.discard(), "sub", "a.txt"), originMarkerPath(g.discard())} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("%s is gone: %v", path, err)
		}
	}
}

// A removal that fails after the rename leaves the discard directory and
// its marker, and its registry line gone. It has no registry line, so only
// the block of the unregistered copies offers it again, and only a
// soho-mutation-* directory below TMPDIR; the kept line says which.
func TestGcIdleOwnerFailedRemovalLeavesTheDiscard(t *testing.T) {
	t.Run("a copy the unregistered block does not list: the line says to remove it by hand, and it stays", func(t *testing.T) {
		g := newIdleGc(t, idleRules("idle"))
		g.register(t)
		writeTestOriginMarker(t, g.copy, gitCommonDirOf(t, g.f.repo, g.f.env), g.f.repo)
		succeed := g.failRemoval(t)
		code, out, errOut := g.f.run(t, g.f.env, "gc", "--yes")
		if code != 0 || !strings.HasPrefix(out, gcPressureNone+"\nkept "+g.discard()+" (") || !strings.HasSuffix(out, "; not offered again; remove it by hand)\nnothing to remove\n") || strings.Contains(out, "removed ") {
			t.Fatalf("code=%d out=%q err=%q; want the discard kept, to remove by hand", code, out, errOut)
		}
		if rows := g.f.copiesRows(t); len(rows) != 0 {
			t.Fatalf("registry rows=%#v; want the line dropped", rows)
		}
		g.wantDiscardKept(t)
		for _, path := range []string{g.copy, originMarkerPath(g.copy)} {
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatalf("%s is still there: %v", path, err)
			}
		}
		succeed()
		g.ageDiscard(t, platform.Now().Add(-3*time.Hour))
		for _, argv := range [][]string{{"gc"}, {"gc", "--yes"}, {"gc", "--yes", "--include-unregistered"}} {
			code, out, errOut = g.f.run(t, g.f.env, argv...)
			if code != 0 || strings.Contains(out, g.discard()) {
				t.Fatalf("%v: code=%d out=%q err=%q; want the discard neither offered nor removed", argv, code, out, errOut)
			}
			g.wantDiscardKept(t)
		}
	})

	t.Run("a soho-mutation-* copy: the line names gc --include-unregistered, which removes it only past --older-than", func(t *testing.T) {
		g := newIdleGcNamed(t, idleRules("idle"), "soho-mutation-idle")
		g.register(t)
		writeTestOriginMarker(t, g.copy, gitCommonDirOf(t, g.f.repo, g.f.env), g.f.repo)
		succeed := g.failRemoval(t)
		code, out, errOut := g.f.run(t, g.f.env, "gc", "--yes")
		if code != 0 || !strings.HasPrefix(out, gcPressureNone+"\nkept "+g.discard()+" (") || !strings.HasSuffix(out, "; gc --include-unregistered removes it once older than --older-than)\nnothing to remove\n") || strings.Contains(out, "removed ") {
			t.Fatalf("code=%d out=%q err=%q; want the discard kept, for gc --include-unregistered", code, out, errOut)
		}
		if rows := g.f.copiesRows(t); len(rows) != 0 {
			t.Fatalf("registry rows=%#v; want the line dropped", rows)
		}
		g.wantDiscardKept(t)
		succeed()

		g.ageDiscard(t, platform.Now().Add(-3*time.Hour))
		code, out, errOut = g.f.run(t, g.f.env, "gc", "--yes")
		if code != 0 || !strings.Contains(out, gcUnregisteredHeader+"\n"+g.discard()+"  10 B  3h  ") || strings.Contains(out, "removed ") {
			t.Fatalf("without --include-unregistered: code=%d out=%q err=%q; want the discard listed apart, kept", code, out, errOut)
		}
		g.wantDiscardKept(t)

		g.ageDiscard(t, platform.Now())
		code, out, errOut = g.f.run(t, g.f.env, "gc", "--yes", "--include-unregistered")
		if code != 0 || strings.Contains(out, g.discard()) {
			t.Fatalf("within --older-than: code=%d out=%q err=%q; want the discard neither offered nor removed", code, out, errOut)
		}
		g.wantDiscardKept(t)

		g.ageDiscard(t, platform.Now().Add(-3*time.Hour))
		code, out, errOut = g.f.run(t, g.f.env, "gc", "--yes", "--include-unregistered")
		want := strings.Join([]string{gcPressureNone, gcUnregisteredHeader, "removed " + g.discard() + "  10 B", "freed: 10 B"}, "\n") + "\n"
		if code != 0 || out != want {
			t.Fatalf("past --older-than: code=%d out=%q err=%q; want\n%s", code, out, errOut, want)
		}
		for _, path := range []string{g.discard(), originMarkerPath(g.discard())} {
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatalf("%s is still there: %v", path, err)
			}
		}
	})
}

// The reviewer's probe (review-3-20261019T212602,
// TestReview392RegisteredDiscardStaysOwned): a discard a worker registered
// again with copies add, for an open task, is a registered copy of a
// working owner. No gc removes it, with --include-unregistered either, even
// past --older-than, and its line stays.
func TestGcRegisteredDiscardStaysOwned(t *testing.T) {
	for _, name := range []string{"reused.soho-gc-12-1", "soho-mutation-reused.soho-gc-12-1"} {
		t.Run(name, func(t *testing.T) {
			g := newIdleGc(t, idleRules("working"))
			path := filepath.Join(g.f.tmp, name)
			writeFileAt(t, filepath.Join(path, "active.txt"), "OPEN TASK", time.Now())
			writeTestOriginMarker(t, path, gitCommonDirOf(t, g.f.repo, g.f.env), g.f.repo)
			writeTaskPointer(t, g.sd, "worker", g.task, filepath.Join(g.f.root, "new-open-report.md"))
			if code, out, errOut := g.f.run(t, g.f.env, "copies", "add", path); code != 0 {
				t.Fatalf("copies add: code=%d out=%q err=%q", code, out, errOut)
			}
			registered := g.f.copiesRows(t)
			if len(registered) != 1 || registered[0].Owner != "worker" {
				t.Fatalf("registry rows=%#v; want the worker's line", registered)
			}
			// Old enough for the unregistered block's age rule.
			old := platform.Now().Add(-3 * time.Hour)
			if err := os.Chtimes(path, old, old); err != nil {
				t.Fatal(err)
			}
			for _, argv := range [][]string{{"gc", "--yes"}, {"gc", "--yes", "--include-unregistered"}} {
				code, out, errOut := g.f.run(t, g.f.env, argv...)
				if code != 0 || strings.Contains(out, path) {
					t.Fatalf("%v: code=%d out=%q err=%q; want the registered copy untouched", argv, code, out, errOut)
				}
				if raw, err := os.ReadFile(filepath.Join(path, "active.txt")); err != nil || string(raw) != "OPEN TASK" {
					t.Fatalf("%v: active.txt=%q err=%v; want it kept", argv, raw, err)
				}
				if rows := g.f.copiesRows(t); len(rows) != 1 || rows[0] != registered[0] {
					t.Fatalf("%v: registry rows=%#v; want %#v", argv, rows, registered)
				}
			}
		})
	}
}

// refuseCopiesRegistry swaps the copy registry write for the permission
// error a sandbox gives when it cannot write the state dir (DJA-323); the
// returned function puts the real write back. No directory mode is changed:
// a chmod does not stop a write on Windows.
func refuseCopiesRegistry(t *testing.T, err error) func() {
	t.Helper()
	old := upsertCopies
	upsertCopies = func(sd string, _ core.CopyRow) error {
		if err != nil {
			return err
		}
		return &fs.PathError{Op: "open", Path: filepath.Join(sd, "copies.tsv.lock"), Err: fs.ErrPermission}
	}
	restore := func() { upsertCopies = old }
	t.Cleanup(restore)
	return restore
}

func readQueue(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the queue %s: %v", path, err)
	}
	return string(raw)
}

func TestCopiesAddQueuesWhenTheStateDirRefusesWrites(t *testing.T) {
	queued := func(t *testing.T, f *copiesFixture, env platform.Env) (string, string, string) {
		t.Helper()
		sd := f.stateDir(t)
		copyDir := filepath.Join(f.tmp, "queued-copy")
		if err := os.MkdirAll(copyDir, 0o700); err != nil {
			t.Fatal(err)
		}
		restore := refuseCopiesRegistry(t, nil)
		code, out, errOut := f.run(t, env, "copies", "add", copyDir)
		restore()
		queue := core.PendingCopiesFile(f.env, "ws")
		wantLine := "copies add: the state dir " + sd + " is not writable from here; the registration is queued for the orchestrator in " + queue + "\n"
		if code != 0 || out != "" || errOut != wantLine {
			t.Fatalf("code=%d out=%q err=%q; want 0 and the queue line %q", code, out, errOut, wantLine)
		}
		if rows := f.copiesRows(t); len(rows) != 0 {
			t.Fatalf("the refused registry was written: %#v", rows)
		}
		return sd, copyDir, queue
	}

	t.Run("the full line goes to the queue with its state dir, and copies lists it after", func(t *testing.T) {
		f := newCopiesFixture(t, []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: agentIdleJSON}})
		f.writeRoster(t, copiesRosterRow("worker", "p-worker"))
		task := filepath.Join(f.root, "reports", "worker.current.md")
		current := filepath.Join(f.root, "reports", "task-1.md")
		writeTaskPointer(t, f.stateDir(t), "worker", task, current)
		sd, copyDir, queue := queued(t, f, f.env)
		raw := readQueue(t, queue)
		lines := strings.Split(strings.TrimSuffix(raw, "\n"), "\n")
		if len(lines) != 2 || lines[0] != "state_dir\tpath\towner\tpane\tcreated\tsource\torigin\ttask\treport" {
			t.Fatalf("queue=%q; want the header and one line", raw)
		}
		fields := strings.Split(lines[1], "\t")
		if len(fields) != 9 || fields[0] != sd || fields[1] != copyDir || fields[2] != "worker" || fields[3] != "p-worker" || fields[5] != "-" || fields[6] != "add" || fields[7] != task || fields[8] != current {
			t.Fatalf("queue line=%q; want the state dir in field 1 and the eight registry fields", lines[1])
		}
		code, out, errOut := f.run(t, f.env, "copies")
		if code != 0 || errOut != "" || !strings.HasPrefix(out, copyDir+"  worker  ") || !strings.HasSuffix(out, "  owned\n") {
			t.Fatalf("copies: code=%d out=%q err=%q; want the queued copy with its owner", code, out, errOut)
		}
		rows := f.copiesRows(t)
		if len(rows) != 1 || rows[0].Path != copyDir || rows[0].Owner != "worker" || rows[0].Task != task || rows[0].Report != current || rows[0].Origin != "add" {
			t.Fatalf("registry rows=%#v; want the queued line with its owner, task and report", rows)
		}
		if _, err := os.Stat(queue); !os.IsNotExist(err) {
			t.Fatalf("the queue is still there after the incorporation: %v", err)
		}
	})

	t.Run("release removes the queued copy of its owner", func(t *testing.T) {
		f := newCopiesFixture(t, []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: agentIdleJSON},
			{Argv: []string{"pane", "report-metadata", "p-worker", "--source", "soho", "--clear-title"}},
			{Argv: []string{"pane", "list", "--workspace", "ws"}, Stdout: `{"result":{"panes":[]}}`},
		})
		f.writeRoster(t, copiesRosterRow("worker", "p-worker"))
		_, copyDir, queue := queued(t, f, f.env)
		addReleaseReport(t, filepath.Join(f.state, "ws"), "worker", false)
		code, out, errOut := f.run(t, f.env, "release", "worker")
		if code != 0 || !strings.Contains(out, "removed copy "+copyDir+"\n") {
			t.Fatalf("release: code=%d out=%q err=%q; want the queued copy removed", code, out, errOut)
		}
		if _, err := os.Stat(copyDir); !os.IsNotExist(err) {
			t.Fatalf("the queued copy is still on disk: %v", err)
		}
		if rows := f.copiesRows(t); len(rows) != 0 {
			t.Fatalf("registry rows=%#v; want none", rows)
		}
		if _, err := os.Stat(queue); !os.IsNotExist(err) {
			t.Fatalf("the queue is still there: %v", err)
		}
	})

	t.Run("gc offers a queued orphan by today's rules", func(t *testing.T) {
		f := gcEmptyFixture(t)
		f.writeRoster(t, copiesRosterRow("worker", "p-worker"))
		env := f.env.Clone()
		env["HERDR_PANE_ID"] = "p-nobody"
		_, copyDir, queue := queued(t, f, env)
		code, out, errOut := f.run(t, f.env, "gc", "--older-than", "0")
		if code != 0 || errOut != "" || !strings.Contains(out, "would remove "+copyDir+"  0 B  -  ") {
			t.Fatalf("gc: code=%d out=%q err=%q; want the queued orphan offered", code, out, errOut)
		}
		if _, err := os.Stat(queue); !os.IsNotExist(err) {
			t.Fatalf("the queue is still there: %v", err)
		}
	})

	t.Run("an error that is not a permission keeps today's exit 4 and queues nothing", func(t *testing.T) {
		f := newCopiesFixture(t, []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: agentIdleJSON}})
		f.writeRoster(t, copiesRosterRow("worker", "p-worker"))
		copyDir := filepath.Join(f.tmp, "copy")
		if err := os.MkdirAll(copyDir, 0o700); err != nil {
			t.Fatal(err)
		}
		refuseCopiesRegistry(t, errors.New("disk full"))
		code, _, errOut := f.run(t, f.env, "copies", "add", copyDir)
		if code != 4 || !strings.Contains(errOut, "copies add: could not register '"+copyDir+"' (disk full)") || strings.Contains(errOut, "queued") {
			t.Fatalf("code=%d err=%q; want today's exit 4 and message", code, errOut)
		}
		if _, err := os.Stat(core.PendingCopiesFile(f.env, "ws")); !os.IsNotExist(err) {
			t.Fatalf("a non-permission error wrote the queue: %v", err)
		}
	})

	t.Run("a held lock keeps today's exit 4 and queues nothing", func(t *testing.T) {
		f := newCopiesFixture(t, []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: agentIdleJSON}})
		f.writeRoster(t, copiesRosterRow("worker", "p-worker"))
		copyDir := filepath.Join(f.tmp, "copy")
		if err := os.MkdirAll(copyDir, 0o700); err != nil {
			t.Fatal(err)
		}
		old := core.CopiesLockTimeout
		core.CopiesLockTimeout = 200 * time.Millisecond
		t.Cleanup(func() { core.CopiesLockTimeout = old })
		lock := filepath.Join(f.stateDir(t), "copies.tsv.lock")
		if err := os.WriteFile(lock, []byte("1 now\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Remove(lock) })
		code, _, errOut := f.run(t, f.env, "copies", "add", copyDir)
		if code != 4 || !strings.Contains(errOut, "copies: registry is locked by another soho") || strings.Contains(errOut, "queued") {
			t.Fatalf("code=%d err=%q; want today's lock exit 4", code, errOut)
		}
		if _, err := os.Stat(core.PendingCopiesFile(f.env, "ws")); !os.IsNotExist(err) {
			t.Fatalf("a held lock wrote the queue: %v", err)
		}
	})

	t.Run("without the refusal nothing goes to the queue", func(t *testing.T) {
		f := newCopiesFixture(t, []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: agentIdleJSON}})
		f.writeRoster(t, copiesRosterRow("worker", "p-worker"))
		copyDir := filepath.Join(f.tmp, "copy")
		if err := os.MkdirAll(copyDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if code, out, errOut := f.run(t, f.env, "copies", "add", copyDir); code != 0 || out != "" || errOut != "" {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
		if rows := f.copiesRows(t); len(rows) != 1 || rows[0].Path != copyDir {
			t.Fatalf("registry rows=%#v", rows)
		}
		if _, err := os.Stat(core.PendingCopiesFile(f.env, "ws")); !os.IsNotExist(err) {
			t.Fatalf("the queue was written without a refusal: %v", err)
		}
	})
}

// mutationQueueSetup is a mutation-copy fixture with a state dir and a
// roster that names the pane's worker; it returns the fixture, the state
// dir, the env overrides and the copy's destination.
func mutationQueueSetup(t *testing.T) (mutationCopyFixture, string, platform.Env, string) {
	t.Helper()
	f := newMutationCopyFixture(t)
	skill := filepath.Join(f.root, "skill")
	if err := os.MkdirAll(filepath.Join(skill, "roles"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("fixture skill\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(f.root, "state")
	sd := filepath.Join(state, "ws")
	if err := os.MkdirAll(sd, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sd, "agents.tsv"), []byte("# roster\n"+releaseRow("worker", "p-worker", "1", "implementer", false, "/tmp/work")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	overrides := platform.Env{"SOHO_DIR": state, "SOHO_SKILL_DIR": skill, "HERDR_WORKSPACE_ID": "ws", "HERDR_PANE_ID": "p-worker"}
	return f, sd, overrides, filepath.Join(f.root, "mcopy")
}

func TestMutationCopyQueuesWhenTheStateDirRefusesWrites(t *testing.T) {
	setup := mutationQueueSetup

	t.Run("a permission error queues the copy instead of leaving it unregistered", func(t *testing.T) {
		f, sd, overrides, dest := setup(t)
		task := filepath.Join(f.root, "reports", "worker.current.md")
		current := filepath.Join(f.root, "reports", "task-1.md")
		writeTaskPointer(t, sd, "worker", task, current)
		restore := refuseCopiesRegistry(t, nil)
		code, out, errOut := runMutationCopyCmd(t, f, f.source, overrides, "--source", f.source, "--dest", dest)
		restore()
		queue := core.PendingCopiesFile(f.env, "ws")
		wantLine := "mutation-copy: the state dir " + sd + " is not writable from here; the registration is queued for the orchestrator in " + queue + "\n"
		if code != 0 || errOut != wantLine {
			t.Fatalf("code=%d err=%q; want 0 and only the queue line %q", code, errOut, wantLine)
		}
		if got := decodedCopy(t, out); got.Copy != dest || got.Source != f.source {
			t.Fatalf("stdout differs from today: %q", out)
		}
		raw := readQueue(t, queue)
		if !strings.Contains(raw, "\n"+sd+"\t"+dest+"\tworker\tp-worker\t") || !strings.HasSuffix(raw, "\t"+f.source+"\tmutation-copy\t"+task+"\t"+current+"\n") {
			t.Fatalf("queue=%q; want the full mutation-copy line for %s", raw, sd)
		}
		// The orchestrator's next copies command registers it. It needs a
		// herdr CLI on PATH: a fake one, ahead of the rest, so the test does
		// not depend on a real herdr installed on the machine.
		env := f.env.Clone()
		for key, value := range overrides {
			env[key] = value
		}
		env["HERDR_ENV"] = "1"
		fakeDir := t.TempDir()
		if _, err := fakecli.Install(t, fakeDir, "herdr", []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: agentIdleJSON}}); err != nil {
			t.Fatal(err)
		}
		env["SOHO_FAKECLI_CONFIG"] = fakeDir
		env["PATH"] = fakeDir + string(os.PathListSeparator) + env.Get("PATH")
		env["GORACE"] = fakecli.RaceOptions(env.List())
		code, out, errOut = runIn(t, []string{"copies"}, env, f.source)
		rows, err := core.ReadCopies(sd)
		if code != 0 || errOut != "" || !strings.HasPrefix(out, dest+"  worker  ") {
			t.Fatalf("copies: code=%d out=%q err=%q; want the queued copy listed", code, out, errOut)
		}
		if err != nil || len(rows) != 1 || rows[0].Path != dest || rows[0].Owner != "worker" || rows[0].Origin != "mutation-copy" || rows[0].Task != task || rows[0].Report != current {
			t.Fatalf("registry rows=%#v err=%v; want the queued copy", rows, err)
		}
	})

	t.Run("a failed queue still says the copy is not registered", func(t *testing.T) {
		f, _, overrides, dest := setup(t)
		refuseCopiesRegistry(t, nil)
		// The queue's directory cannot be created: its parent is a file.
		blocker := filepath.Join(f.root, "tmp-file")
		if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		overrides["TMPDIR"] = blocker
		code, _, errOut := runMutationCopyCmd(t, f, f.source, overrides, "--source", f.source, "--dest", dest)
		if code != 0 || !strings.Contains(errOut, "mutation-copy: copy not registered (") || !strings.Contains(errOut, "the queue failed too") || strings.Contains(errOut, "queued for the orchestrator") {
			t.Fatalf("code=%d err=%q; want the not-registered warning", code, errOut)
		}
	})

	t.Run("an error that is not a permission keeps today's warning and queues nothing", func(t *testing.T) {
		f, _, overrides, dest := setup(t)
		refuseCopiesRegistry(t, errors.New("disk full"))
		code, _, errOut := runMutationCopyCmd(t, f, f.source, overrides, "--source", f.source, "--dest", dest)
		if code != 0 || errOut != "mutation-copy: copy not registered (disk full); remove it yourself when done\n" {
			t.Fatalf("code=%d err=%q; want today's warning", code, errOut)
		}
		if _, err := os.Stat(core.PendingCopiesFile(f.env, "ws")); !os.IsNotExist(err) {
			t.Fatalf("a non-permission error wrote the queue: %v", err)
		}
	})
}

func TestCopiesQueueLinesThatDoNotEnter(t *testing.T) {
	f := newCopiesFixture(t, []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: agentIdleJSON}})
	f.writeRoster(t, copiesRosterRow("worker", "p-worker"))
	sd := f.stateDir(t)
	good := filepath.Join(f.tmp, "good")
	if err := os.MkdirAll(good, 0o700); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(f.tmp, "gone")
	inside := filepath.Join(f.repo, "inside")
	if err := os.MkdirAll(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(f.state, "other-ws")
	line := func(dir, path string) string {
		return dir + "\t" + path + "\tworker\tp-worker\t2026-01-01T00:00:00Z\t-\tadd\t-\t-"
	}
	otherLine := line(other, filepath.Join(f.tmp, "theirs"))
	malformed := sd + "\t" + filepath.Join(f.tmp, "short") + "\tworker"
	queue := core.PendingCopiesFile(f.env, "ws")
	if err := os.MkdirAll(filepath.Dir(queue), 0o700); err != nil {
		t.Fatal(err)
	}
	header := "state_dir\tpath\towner\tpane\tcreated\tsource\torigin\ttask\treport\n"
	content := header + strings.Join([]string{line(sd, good), otherLine, line(sd, gone), line(sd, inside), malformed, "no state dir"}, "\n") + "\n"
	if err := os.WriteFile(queue, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := f.run(t, f.env, "copies")
	if code != 0 || !strings.HasPrefix(out, good+"  worker  ") || strings.Count(out, "\n") != 1 {
		t.Fatalf("copies: code=%d out=%q err=%q; want only the good copy", code, out, errOut)
	}
	for _, want := range []string{
		"copies: dropped the queued copy '" + gone + "' from " + queue + ", not registered: does not exist\n",
		"copies: dropped the queued copy '" + inside + "' from " + queue + ", not registered: is the project root or inside it\n",
		"copies: dropped a malformed line from " + queue + ": " + strconv.Quote(strings.TrimPrefix(malformed, sd+"\t")) + "\n",
		"copies: dropped a malformed line from " + queue + ": \"no state dir\"\n",
	} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("stderr=%q; want %q", errOut, want)
		}
	}
	rows := f.copiesRows(t)
	if len(rows) != 1 || rows[0].Path != good {
		t.Fatalf("registry rows=%#v; want only the good copy", rows)
	}
	if raw := readQueue(t, queue); raw != header+otherLine+"\n" {
		t.Fatalf("queue=%q; want only the other state dir's line, intact", raw)
	}
}

func TestCopiesIncorporationFailureKeepsTheQueue(t *testing.T) {
	f := newCopiesFixture(t, []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: agentIdleJSON}})
	f.writeRoster(t, copiesRosterRow("worker", "p-worker"))
	sd := f.stateDir(t)
	copyDir := filepath.Join(f.tmp, "queued")
	if err := os.MkdirAll(copyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	queue, err := core.QueueCopy(f.env, "ws", sd, core.CopyRow{Path: copyDir, Owner: "worker", Pane: "p-worker", Created: "2026-01-01T00:00:00Z", Source: "-", Origin: "add"})
	if err != nil {
		t.Fatal(err)
	}
	before := readQueue(t, queue)
	old := core.CopiesLockTimeout
	core.CopiesLockTimeout = 200 * time.Millisecond
	t.Cleanup(func() { core.CopiesLockTimeout = old })
	lock := filepath.Join(sd, "copies.tsv.lock")
	if err := os.WriteFile(lock, []byte("1 now\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(lock) })
	code, out, errOut := f.run(t, f.env, "copies")
	if code != 0 || out != "no registered copies\n" || !strings.Contains(errOut, "copies: the queued copy registrations in "+queue+" were not incorporated (copies: registry is locked by another soho") {
		t.Fatalf("code=%d out=%q err=%q; want the registry as it is and the warning", code, out, errOut)
	}
	if after := readQueue(t, queue); after != before {
		t.Fatalf("the queue changed: %q -> %q", before, after)
	}
}

// refuseIncorporation swaps both queue incorporations for err, or for the
// permission error of a sandbox that cannot take the state dir's registry
// lock; the queue itself is not touched.
func refuseIncorporation(t *testing.T, err error) {
	t.Helper()
	oldCopies, oldProcs := incorporateCopies, incorporateProcs
	fail := func(sd string) error {
		if err != nil {
			return err
		}
		return &fs.PathError{Op: "open", Path: filepath.Join(sd, "copies.tsv.lock"), Err: fs.ErrPermission}
	}
	incorporateCopies = func(sd, ws string, env platform.Env, _ func(core.CopyRow) (core.CopyRow, string)) (core.PendingResult, error) {
		return core.PendingResult{Queue: core.PendingCopiesFile(env, ws)}, fail(sd)
	}
	incorporateProcs = func(sd, ws string, env platform.Env) (core.PendingResult, error) {
		return core.PendingResult{Queue: core.PendingProcsFile(env, ws)}, fail(sd)
	}
	t.Cleanup(func() { incorporateCopies, incorporateProcs = oldCopies, oldProcs })
}

func TestSandboxedCopiesStayQuietOnAPermissionIncorporation(t *testing.T) {
	t.Run("a second copies add prints only the queue line, and copies lists without a warning", func(t *testing.T) {
		f := newCopiesFixture(t, []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: agentIdleJSON}})
		f.writeRoster(t, copiesRosterRow("worker", "p-worker"))
		sd := f.stateDir(t)
		refuseCopiesRegistry(t, nil)
		refuseIncorporation(t, nil)
		queue := core.PendingCopiesFile(f.env, "ws")
		wantLine := "copies add: the state dir " + sd + " is not writable from here; the registration is queued for the orchestrator in " + queue + "\n"
		for _, name := range []string{"first", "second"} {
			copyDir := filepath.Join(f.tmp, name)
			if err := os.MkdirAll(copyDir, 0o700); err != nil {
				t.Fatal(err)
			}
			code, out, errOut := f.run(t, f.env, "copies", "add", copyDir)
			if code != 0 || out != "" || errOut != wantLine {
				t.Fatalf("%s add: code=%d out=%q err=%q; want 0 and only %q", name, code, out, errOut, wantLine)
			}
		}
		if lines := strings.Split(strings.TrimSuffix(readQueue(t, queue), "\n"), "\n"); len(lines) != 3 {
			t.Fatalf("queue lines=%q; want the header and both copies", lines)
		}
		code, out, errOut := f.run(t, f.env, "copies")
		if code != 0 || out != "no registered copies\n" || errOut != "" {
			t.Fatalf("copies: code=%d out=%q err=%q; want the registry as it is, no warning", code, out, errOut)
		}
	})

	t.Run("an error that is not a permission keeps the warning", func(t *testing.T) {
		f := newCopiesFixture(t, []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: agentIdleJSON}})
		f.writeRoster(t, copiesRosterRow("worker", "p-worker"))
		f.stateDir(t)
		refuseIncorporation(t, errors.New("disk full"))
		queue := core.PendingCopiesFile(f.env, "ws")
		code, out, errOut := f.run(t, f.env, "copies")
		want := "copies: the queued copy registrations in " + queue + " were not incorporated (disk full); the queue stays and the registry is used as it is\n"
		if code != 0 || out != "no registered copies\n" || errOut != want {
			t.Fatalf("copies: code=%d out=%q err=%q; want %q", code, out, errOut, want)
		}
	})
}

// queueOwnedByAnother makes the queue's workspace level read as another
// user's (core.ScratchOwnerOf), on Unix, where the owner is checked.
func queueOwnedByAnother(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the queue's owner is checked on Unix only")
	}
	old := core.ScratchOwnerOf
	core.ScratchOwnerOf = func(info fs.FileInfo) (int, bool) {
		if info.Name() == "ws" && info.IsDir() {
			return os.Getuid() + 1, true
		}
		return old(info)
	}
	t.Cleanup(func() { core.ScratchOwnerOf = old })
}

func TestARefusedQueueDirectory(t *testing.T) {
	refusal := func(f *copiesFixture) string {
		return "queue: refusing " + core.PendingDir(f.env, "ws") + ": it belongs to another user"
	}
	prepare := func(t *testing.T, f *copiesFixture) {
		t.Helper()
		if err := os.MkdirAll(core.PendingDir(f.env, "ws"), 0o700); err != nil {
			t.Fatal(err)
		}
		queueOwnedByAnother(t)
	}

	t.Run("copies add keeps today's exit 4 with the queue's failure", func(t *testing.T) {
		f := newCopiesFixture(t, []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: agentIdleJSON}})
		f.writeRoster(t, copiesRosterRow("worker", "p-worker"))
		copyDir := filepath.Join(f.tmp, "copy")
		if err := os.MkdirAll(copyDir, 0o700); err != nil {
			t.Fatal(err)
		}
		prepare(t, f)
		refuseCopiesRegistry(t, nil)
		code, _, errOut := f.run(t, f.env, "copies", "add", copyDir)
		if code != 4 || !strings.Contains(errOut, "copies add: could not register '"+copyDir+"' (") || !strings.Contains(errOut, "; the queue failed too: "+refusal(f)+")") {
			t.Fatalf("code=%d err=%q; want exit 4 with the queue refusal", code, errOut)
		}
	})

	t.Run("mutation-copy says the copy is not registered", func(t *testing.T) {
		f, _, overrides, dest := mutationQueueSetup(t)
		dir := core.PendingDir(f.env, "ws")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		queueOwnedByAnother(t)
		refuseCopiesRegistry(t, nil)
		code, _, errOut := runMutationCopyCmd(t, f, f.source, overrides, "--source", f.source, "--dest", dest)
		want := "the queue failed too: queue: refusing " + dir + ": it belongs to another user); remove it yourself when done\n"
		if code != 0 || !strings.HasPrefix(errOut, "mutation-copy: copy not registered (") || !strings.HasSuffix(errOut, want) {
			t.Fatalf("code=%d err=%q; want the not-registered warning with the refusal", code, errOut)
		}
	})

	t.Run("the incorporation warns and the command goes on with the registry", func(t *testing.T) {
		f := newCopiesFixture(t, []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: agentIdleJSON}})
		f.writeRoster(t, copiesRosterRow("worker", "p-worker"))
		prepare(t, f)
		queue := core.PendingCopiesFile(f.env, "ws")
		code, out, errOut := f.run(t, f.env, "copies")
		want := "copies: the queued copy registrations in " + queue + " were not incorporated (" + refusal(f) + "); the queue stays and the registry is used as it is\n"
		if code != 0 || out != "no registered copies\n" || errOut != want {
			t.Fatalf("copies: code=%d out=%q err=%q; want %q", code, out, errOut, want)
		}
	})
}

// TestAQueueOpenToOthersIsNeverTrusted is the review's probe of a queue
// file of the user with write for the group or the others (DJA-323): its
// bytes could have been changed by another user, so neither copies nor
// release takes its lines in, and the copy a line names stays on disk.
func TestAQueueOpenToOthersIsNeverTrusted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the queue's mode is checked on Unix only")
	}
	setup := func(t *testing.T, f *copiesFixture) (string, string, string) {
		t.Helper()
		f.writeRoster(t, copiesRosterRow("worker", "p-worker"))
		sd := f.stateDir(t)
		valuable := filepath.Join(f.tmp, "valuable")
		if err := os.MkdirAll(valuable, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(valuable, "user-data"), []byte("keep me\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		queue, err := core.QueueCopy(f.env, "ws", sd, core.CopyRow{Path: valuable, Owner: "worker", Pane: "p-worker", Created: core.FrictionISO(platform.Now()), Source: "-", Origin: "mutation-copy"})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(queue, 0o666); err != nil {
			t.Fatal(err)
		}
		return valuable, queue, readQueue(t, queue)
	}
	refusal := func(queue string) string {
		return "queue: refusing " + queue + ": it is writable by other users, so its lines cannot be trusted; remove it by hand"
	}

	t.Run("copies warns, registers nothing and leaves the queue's bytes", func(t *testing.T) {
		f := newCopiesFixture(t, []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: agentIdleJSON}})
		_, queue, before := setup(t, f)
		code, out, errOut := f.run(t, f.env, "copies")
		want := "copies: the queued copy registrations in " + queue + " were not incorporated (" + refusal(queue) + "); the queue stays and the registry is used as it is\n"
		if code != 0 || out != "no registered copies\n" || errOut != want {
			t.Fatalf("copies: code=%d out=%q err=%q; want %q", code, out, errOut, want)
		}
		if rows := f.copiesRows(t); len(rows) != 0 {
			t.Fatalf("registry rows=%#v; want none", rows)
		}
		if after := readQueue(t, queue); after != before {
			t.Fatalf("the queue was rewritten: %q -> %q", before, after)
		}
	})

	t.Run("release of the line's owner leaves the copy it names", func(t *testing.T) {
		f := newCopiesFixture(t, []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: agentIdleJSON},
			{Argv: []string{"pane", "report-metadata", "p-worker", "--source", "soho", "--clear-title"}},
			{Argv: []string{"pane", "list", "--workspace", "ws"}, Stdout: `{"result":{"panes":[]}}`},
		})
		valuable, queue, before := setup(t, f)
		addReleaseReport(t, filepath.Join(f.state, "ws"), "worker", false)
		code, out, errOut := f.run(t, f.env, "release", "worker")
		if code != 0 || strings.Contains(out, "removed copy") || !strings.Contains(errOut, refusal(queue)) {
			t.Fatalf("release: code=%d out=%q err=%q; want the warning and no removal", code, out, errOut)
		}
		if raw, err := os.ReadFile(filepath.Join(valuable, "user-data")); err != nil || string(raw) != "keep me\n" {
			t.Fatalf("the copy the queue names was touched: %q %v", raw, err)
		}
		if rows := f.copiesRows(t); len(rows) != 0 {
			t.Fatalf("registry rows=%#v; want none", rows)
		}
		if after := readQueue(t, queue); after != before {
			t.Fatalf("the queue was rewritten: %q -> %q", before, after)
		}
	})
}
