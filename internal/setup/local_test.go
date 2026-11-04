package setup

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	_ "unsafe"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

//go:linkname platformRenameFile github.com/djalmajr/soho/internal/platform.renameFile
var platformRenameFile func(string, string) error

func localPlanFixture(t *testing.T) (string, platform.Env, *core.Config) {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is required for local setup plan tests")
	}
	root := t.TempDir()
	// Canonicalize the existing temporary root before deriving any
	// fixture path: the product reports canonical paths (git /
	// EvalSymlinks), while t.TempDir can hand back the short Windows
	// alias; one shared form keeps expected and actual paths comparable.
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("canonicalize the fixture root: %v", err)
	}
	cmd := exec.Command(git, "init", "-q", root)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("upstream\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"config", "user.name", "test"}, {"config", "user.email", "test@example.com"}, {"add", "CLAUDE.md"}, {"commit", "-qm", "initial"}} {
		cmd := exec.Command(git, append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	home := t.TempDir()
	tmp := t.TempDir()
	env := isolatedHerdrEnv(t, git)
	env["HOME"] = home
	env["XDG_CONFIG_HOME"] = filepath.Join(home, ".config")
	env["TMPDIR"] = tmp
	env["SOHO_SKILL_DIR"] = filepath.Join("..", "..", "skills", "soho")
	ctx := core.LoadConfig(env, root)
	return root, env, &ctx
}

func runPlanResult(args []string, ctx *core.Config, env platform.Env, cwd string) (code int, stdout string, message string) {
	old := platform.Stdout
	var out bytes.Buffer
	platform.Stdout = &out
	defer func() { platform.Stdout = old }()
	defer func() {
		if value := recover(); value != nil {
			if e, ok := value.(*platform.ExitError); ok {
				code, message = e.Code, e.Msg
				return
			}
			panic(value)
		}
	}()
	CmdPlan(args, ctx, env, cwd)
	return 0, out.String(), ""
}

func runPlanStreams(args []string, ctx *core.Config, env platform.Env, cwd string) (code int, stdout, stderr, message string) {
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, errOut bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &errOut
	defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
	defer func() {
		if value := recover(); value != nil {
			if e, ok := value.(*platform.ExitError); ok {
				code, message = e.Code, e.Msg
				return
			}
			panic(value)
		}
	}()
	CmdPlan(args, ctx, env, cwd)
	return 0, out.String(), errOut.String(), ""
}

func TestPlanLegacyConfigTemporaryNameMatchesJavaScript(t *testing.T) { // Mutation captured: renaming the simulated project config changes the warning in stderr.
	// The simulated project config is the current .agents/soho.conf (the
	// former herdr-soho.conf is no longer read).
	root, env, _ := localPlanFixture(t)
	projectPath := filepath.Join(root, ".agents", "soho.conf")
	if err := os.MkdirAll(filepath.Dir(projectPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projectPath, []byte("lane.build.roles=scouter,implementer\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := core.LoadConfig(env, root)
	code, stdout, stderr, message := runPlanStreams([]string{"--panes", "4"}, &ctx, env, root)
	if code != 0 || message != "" || !strings.Contains(stdout, filepath.Join(root, ".agents", "soho.conf")) {
		t.Fatalf("code=%d message=%q stdout=%q", code, message, stdout)
	}
	if !strings.Contains(stderr, "proj.conf") || strings.Contains(stderr, "config-0") {
		t.Fatalf("stderr=%q; want the JS temporary name proj.conf and no config-0", stderr)
	}
}

func requireLocalRefusal(t *testing.T, root string, env platform.Env, ctx *core.Config, want string) {
	t.Helper()
	code, stdout, message := runPlanResult([]string{"--local"}, ctx, env, root)
	if code != 4 || stdout != "" || !strings.Contains(message, want) {
		t.Fatalf("code=%d stdout=%q message=%q; want refusal containing %q", code, stdout, message, want)
	}
}

func TestPlanLocalRefusals(t *testing.T) {
	t.Run(`classifyStateDir: ancestor symlink alias inside, sibling and nested work trees, root, symlink refusal and external`, func(t *testing.T) { // JS: "classifyStateDir: ancestor symlink alias inside, sibling and nested work trees, root, symlink refusal and external"
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation may require privilege on Windows")
		}
		root, env, ctx := localPlanFixture(t)
		root, err := filepath.EvalSymlinks(root)
		if err != nil {
			t.Fatalf("resolve fixture root like JS realpathSync: %v", err)
		}
		classify := func(value string) stateClassification {
			t.Helper()
			caseEnv := env.Clone()
			caseEnv["SOHO_DIR"] = value
			return classifyLocalState(root, ctx, caseEnv, root)
		}
		classifyAt := func(worktree, value string) stateClassification {
			t.Helper()
			caseEnv := env.Clone()
			caseEnv["SOHO_DIR"] = value
			return classifyLocalState(worktree, ctx, caseEnv, worktree)
		}
		check := func(label, value string, want stateClassification) {
			t.Helper()
			t.Run(label, func(t *testing.T) {
				got := classify(value)
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("classify(%q)=%+v; JS expects %+v", value, got, want)
				}
			})
		}
		base := filepath.Dir(root)
		alias := filepath.Join(base, "alias")
		if err := os.Symlink(base, alias); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		check("ancestor alias", filepath.Join(alias, filepath.Base(root), "cache"), stateClassification{kind: "inside", rel: "cache", shown: "cache/"})
		link := filepath.Join(base, "root-link")
		if err := os.Symlink(root, link); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		check("outside alias to root", filepath.Join(link, "cache"), stateClassification{kind: "inside", rel: "cache", shown: "cache/"})
		loop := filepath.Join(root, "loop")
		if err := os.Symlink(root, loop); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		check("root symlink", "loop", stateClassification{kind: "symlink", shown: loop, rootTarget: true})
		check("path through root symlink", "loop/cache", stateClassification{kind: "symlink", shown: filepath.Join(root, "loop", "cache"), link: loop})
		check("outside alias through in-repo symlink", filepath.Join(link, "loop", "cache"), stateClassification{kind: "symlink", shown: filepath.Join(link, "loop", "cache"), link: filepath.Join(link, "loop")})
		sub := filepath.Join(root, "sub")
		if err := os.Mkdir(sub, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(sub, filepath.Join(base, "sub-link")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if err := os.Symlink(root, filepath.Join(sub, "loop")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		check("interior alias through in-repo symlink", filepath.Join(base, "sub-link", "loop", "cache"), stateClassification{kind: "symlink", shown: filepath.Join(base, "sub-link", "loop", "cache"), link: filepath.Join(base, "sub-link", "loop")})
		check("benign interior alias", filepath.Join(base, "sub-link", "newstate"), stateClassification{kind: "inside", rel: filepath.Join("sub", "newstate"), shown: "sub/newstate/"})
		if err := os.Mkdir(filepath.Join(root, ".herdr-soho"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(root, ".herdr-soho"), filepath.Join(root, "s")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		check("in-repo final symlink", "s", stateClassification{kind: "symlink", shown: filepath.Join(root, "s")})
		outside := filepath.Join(base, "outside")
		if err := os.Mkdir(outside, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(root, "link2")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		check("outside-target symlink entry", "link2", stateClassification{kind: "inside", rel: "link2", shown: "link2/"})
		check("worktree root", ".", stateClassification{kind: "root", shown: root})
		check("normalized worktree root", filepath.Join("sub", ".."), stateClassification{kind: "root", shown: root})
		git, ok := platform.FindExecutable("git", env, platform.Current())
		if !ok {
			t.Skip("git is required for worktree classification")
		}
		wt := filepath.Join(base, "sibling")
		cmd := exec.Command(git, "-C", root, "worktree", "add", "-q", wt)
		cmd.Env = env.List()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git worktree add: %v: %s", err, out)
		}
		t.Cleanup(func() { _ = exec.Command(git, "-C", root, "worktree", "remove", "--force", wt).Run() })
		check("sibling worktree", filepath.Join(wt, "state"), stateClassification{kind: "sibling", rel: "state", shown: filepath.Join(wt, "state"), worktree: wt})
		check("sibling root", wt, stateClassification{kind: "root", shown: wt})
		t.Run("sibling from other side", func(t *testing.T) {
			got := classifyAt(wt, filepath.Join(root, "state"))
			want := stateClassification{kind: "sibling", rel: "state", shown: filepath.Join(root, "state"), worktree: root}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("classify(root=%q, cwd=%q, state=%q)=%+v; JS expects %+v", wt, wt, filepath.Join(root, "state"), got, want)
			}
		})
		other := filepath.Join(base, "other")
		if err := os.Mkdir(other, 0o700); err != nil {
			t.Fatal(err)
		}
		check("other repo external", filepath.Join(other, "state"), stateClassification{kind: "external", shown: filepath.Join(other, "state")})
		nested := filepath.Join(root, "nested")
		cmd = exec.Command(git, "-C", root, "worktree", "add", "-q", nested)
		cmd.Env = env.List()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("nested worktree add: %v: %s", err, out)
		}
		t.Cleanup(func() { _ = exec.Command(git, "-C", root, "worktree", "remove", "--force", nested).Run() })
		check("nested worktree", filepath.Join(nested, "state"), stateClassification{kind: "sibling", rel: "state", shown: filepath.Join(nested, "state"), worktree: nested})
		t.Run("nested worktree from itself", func(t *testing.T) {
			got := classifyAt(nested, filepath.Join(nested, "state"))
			want := stateClassification{kind: "inside", rel: "state", shown: "state/"}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("classify(root=%q, cwd=%q, state=%q)=%+v; JS expects %+v", nested, nested, filepath.Join(nested, "state"), got, want)
			}
		})
		check("nested worktree root", nested, stateClassification{kind: "root", shown: nested})
		check("external", filepath.Join(base, "elsewhere"), stateClassification{kind: "external", shown: filepath.Join(base, "elsewhere")})
		check("missing tail", filepath.Join("deep", "cache"), stateClassification{kind: "inside", rel: filepath.Join("deep", "cache"), shown: "deep/cache/"})
	})
	t.Run("unsafe state-dir glob is refused before plan output", func(t *testing.T) { // JS: "setup --plan --local refuses an unsafe SOHO_DIR (rc 4) with no output or mutation"; Mutation captured: allowing glob metacharacters can hide unrelated root files.
		root, env, ctx := localPlanFixture(t)
		env["SOHO_DIR"] = "*"
		before := setupFilesystemSnapshot(t, root)
		requireLocalRefusal(t, root, env, ctx, "refusing to ignore state path")
		if !reflect.DeepEqual(before, setupFilesystemSnapshot(t, root)) {
			t.Fatal("unsafe plan changed repository files")
		}
	})
	t.Run("state-dir trailing whitespace is refused before plan output", func(t *testing.T) { // Mutation captured: git strips the trailing space and the state directory remains visible.
		root, env, ctx := localPlanFixture(t)
		env["SOHO_DIR"] = "cache "
		requireLocalRefusal(t, root, env, ctx, "ends in whitespace")
	})
	t.Run("worktree root is refused before plan output", func(t *testing.T) { // Mutation captured: excluding the root would hide the complete worktree.
		root, env, ctx := localPlanFixture(t)
		env["SOHO_DIR"] = "."
		requireLocalRefusal(t, root, env, ctx, "root of a git work tree")
	})
	t.Run("in-repository symlink traversal is refused", func(t *testing.T) { // Mutation captured: git cannot ignore paths reached through an in-repository symlink.
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation may require privilege on Windows")
		}
		root, env, ctx := localPlanFixture(t)
		if err := os.Mkdir(filepath.Join(root, "cache"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(root, "cache"), filepath.Join(root, "alias")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		env["SOHO_DIR"] = "alias/child"
		requireLocalRefusal(t, root, env, ctx, "path traverses the symlink")
	})
	t.Run("in-repository symlink reached through an outside alias is refused", func(t *testing.T) { // Mutation captured: resolving an outside alias must still find an in-repository symlink component.
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation may require privilege on Windows")
		}
		root, env, ctx := localPlanFixture(t)
		outside := t.TempDir()
		if err := os.Symlink(root, filepath.Join(outside, "alias")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if err := os.Symlink(root, filepath.Join(root, "loop")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		value := filepath.Join(outside, "alias", "loop", "cache")
		env["SOHO_DIR"] = value
		wantLink := filepath.Join(outside, "alias", "loop")
		if code, out, msg := runPlanResult([]string{"--local"}, ctx, env, root); code != 4 || out != "" || !strings.Contains(msg, "the path traverses the symlink '") || !strings.Contains(msg, wantLink) {
			t.Fatalf("code=%d stdout=%q message=%q", code, out, msg)
		}
	})
	t.Run("final symlink to an external state directory can be excluded", func(t *testing.T) { // Mutation captured: final external symlinks name a git entry and remain supported.
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation may require privilege on Windows")
		}
		root, env, ctx := localPlanFixture(t)
		outside := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(root, "external-state")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		env["SOHO_DIR"] = filepath.Join(root, "external-state")
		code, out, msg := runPlanResult([]string{"--local"}, ctx, env, root)
		if code != 0 || msg != "" || !strings.Contains(strings.Join(localRels(root, ctx, env, root), ","), "external-state") {
			t.Fatalf("code=%d message=%q output=%q", code, msg, out)
		}
	})
	t.Run("linked worktree plan names and does not write the common exclude", func(t *testing.T) { // JS: "linked worktree: plan shows the common exclude path; setup --local ignores the block and state dir there"; Mutation captured: using a linked worktree git dir writes a dead exclude instead of the shared one.
		root, env, ctx := localPlanFixture(t)
		wt := filepath.Join(t.TempDir(), "linked")
		git, _ := platform.FindExecutable("git", env, platform.Current())
		cmd := exec.Command(git, "-C", root, "worktree", "add", "--detach", wt)
		cmd.Env = env.List()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git worktree add: %v: %s", err, out)
		}
		common := filepath.Join(root, ".git", "info", "exclude")
		excludeBefore, err := os.ReadFile(common)
		if err != nil {
			t.Fatal(err)
		}
		env["SOHO_DIR"] = filepath.Join(wt, "state")
		wtCtx := *ctx
		code, out, msg := runPlanResult([]string{"--local"}, &wtCtx, env, wt)
		if code != 0 || msg != "" || !strings.Contains(out, common) || !strings.Contains(out, "/state") {
			t.Fatalf("code=%d message=%q output=%q; want common exclude %s", code, msg, out, common)
		}
		excludeAfter, err := os.ReadFile(common)
		if err != nil || !bytes.Equal(excludeBefore, excludeAfter) {
			t.Fatalf("plan changed common exclude bytes: before=%q after=%q err=%v", excludeBefore, excludeAfter, err)
		}
		if _, err := os.Stat(filepath.Join(wt, localInstructionFile)); !os.IsNotExist(err) {
			t.Fatalf("plan wrote local instruction file: %v", err)
		}
		if info, err := os.Lstat(filepath.Join(wt, ".git")); err != nil || !info.Mode().IsRegular() {
			t.Fatalf("worktree .git pointer must remain a file: info=%v err=%v", info, err)
		}
		if err := os.MkdirAll(filepath.Join(wt, "state"), 0o700); err != nil {
			t.Fatal(err)
		}
		code, out, msg = runSetupWrite([]string{"--local", "--no-hooks"}, &wtCtx, env, wt)
		if code != 0 || !strings.Contains(out, common) || !strings.Contains(out, "state dir ignored: state/") {
			t.Fatalf("setup code=%d out=%q msg=%q", code, out, msg)
		}
		check := exec.Command(git, "-C", wt, "check-ignore", "-q", "state")
		check.Env = env.List()
		if out, err := check.CombinedOutput(); err != nil {
			t.Fatalf("git check-ignore state: %s %v", out, err)
		}
	})
	t.Run("symlinked local instruction target is refused", func(t *testing.T) { // JS: "setup --plan --local refuses a symlinked CLAUDE.local.md (rc 4) with no output or mutation"; Mutation captured: lstat must reject a link before any plan is printed.
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation may require privilege on Windows")
		}
		root, env, ctx := localPlanFixture(t)
		if err := os.Symlink("CLAUDE.md", filepath.Join(root, localInstructionFile)); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		before := setupFilesystemSnapshot(t, root)
		requireLocalRefusal(t, root, env, ctx, "is a symlink; refusing to write local instructions")
		if !reflect.DeepEqual(before, setupFilesystemSnapshot(t, root)) {
			t.Fatal("plan changed repository files after symlink refusal")
		}
	})
	t.Run("symlinked git exclude is refused", func(t *testing.T) { // Mutation captured: following an exclude symlink can overwrite tracked content.
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation may require privilege on Windows")
		}
		root, env, ctx := localPlanFixture(t)
		exclude := filepath.Join(root, ".git", "info", "exclude")
		if err := os.Remove(exclude); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(root, "CLAUDE.md"), exclude); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		requireLocalRefusal(t, root, env, ctx, "is a symlink; refusing to write local excludes")
	})
	t.Run("symlinked git info ancestor is refused", func(t *testing.T) { // Mutation captured: a redirected info directory can route the exclude into the repository.
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation may require privilege on Windows")
		}
		root, env, ctx := localPlanFixture(t)
		info := filepath.Join(root, ".git", "info")
		if err := os.RemoveAll(info); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("..", info); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		requireLocalRefusal(t, root, env, ctx, "symlink")
	})
	t.Run("exclude path that is a directory is refused", func(t *testing.T) { // Mutation captured: treating a directory as absent could claim a plan the real setup cannot write.
		root, env, ctx := localPlanFixture(t)
		exclude := filepath.Join(root, ".git", "info", "exclude")
		if err := os.Remove(exclude); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if err := os.Mkdir(exclude, 0o700); err != nil {
			t.Fatal(err)
		}
		requireLocalRefusal(t, root, env, ctx, "cannot read the git exclude file")
	})
	t.Run("state path traversing an in-repository symlink to outside is refused", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation may require privilege on Windows")
		}
		root, env, ctx := localPlanFixture(t)
		outside := t.TempDir()
		if err := os.Mkdir(filepath.Join(outside, "cache"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		env["SOHO_DIR"] = filepath.Join("link", "cache")
		requireLocalRefusal(t, root, env, ctx, "path traverses the symlink")
	})
	t.Run("backslash state name is rejected on POSIX and plain name is accepted", func(t *testing.T) {
		root, env, ctx := localPlanFixture(t)
		if runtime.GOOS == "windows" {
			return // Backslash is a path separator on Windows.
		}
		env["SOHO_DIR"] = `foo\bar`
		requireLocalRefusal(t, root, env, ctx, "contains git-ignore metacharacters")
		env["SOHO_DIR"] = "cache"
		code, out, message := runPlanResult([]string{"--local"}, ctx, env, root)
		if code != 0 || message != "" || !strings.Contains(out, "/cache") {
			t.Fatalf("plain name: code=%d stdout=%q message=%q", code, out, message)
		}
	})
	t.Run("only CLAUDE.md existing is the plan target", func(t *testing.T) {
		root, env, ctx := localPlanFixture(t)
		code, out, message := runPlanResult(nil, ctx, env, root)
		if code != 0 || message != "" || !strings.Contains(out, filepath.Join(root, "CLAUDE.md")) || strings.Contains(out, filepath.Join(root, "AGENTS.md")) {
			t.Fatalf("code=%d stdout=%q message=%q", code, out, message)
		}
	})
	t.Run("AGENTS.md remains the plan target when both exist", func(t *testing.T) {
		root, env, ctx := localPlanFixture(t)
		if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("agent rules\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, message := runPlanResult(nil, ctx, env, root)
		if code != 0 || message != "" || !strings.Contains(out, filepath.Join(root, "AGENTS.md")) || strings.Contains(out, filepath.Join(root, "CLAUDE.md")) {
			t.Fatalf("code=%d stdout=%q message=%q", code, out, message)
		}
	})
	t.Run("exclude probe respects effective write permission", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("POSIX access(2) permission case")
		}
		root, env, ctx := localPlanFixture(t)
		info := filepath.Join(root, ".git", "info")
		if err := os.Chmod(info, 0o575); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(info, 0o700) })
		requireLocalRefusal(t, root, env, ctx, "cannot write the git exclude file")
	})
	t.Run("plan refuses a symlinked git exclude without output or mutation", func(t *testing.T) { // JS: "setup --plan --local refuses a symlinked git exclude (rc 4) with no output or mutation"
		localSymlinkExcludeRefusal(t, true)
	})
	t.Run("plan refuses a symlinked git info ancestor without output or mutation", func(t *testing.T) { // JS: "setup --plan --local refuses a symlinked .git/info ancestor (rc 4) with no output or mutation"
		localSymlinkInfoRefusal(t, true)
	})
}

func TestLocalPlanAndDryRunLeaveGitMetadataUntouched(t *testing.T) {
	// JS: "setup --plan --local and setup --local --dry-run: no writeFileSync/unlinkSync (nor rename/rm) under git metadata"
	root, env, ctx := localPlanFixture(t)
	gitDir := filepath.Join(root, ".git")
	before := setupFilesystemSnapshot(t, gitDir)
	code, out, stderr, message := runPlanStreams([]string{"--local"}, ctx, env, root)
	if code != 0 || stderr != "" || message != "" || out == "" {
		t.Fatalf("plan code=%d out=%q stderr=%q message=%q", code, out, stderr, message)
	}
	if after := setupFilesystemSnapshot(t, gitDir); !reflect.DeepEqual(after, before) {
		t.Fatalf("plan changed .git metadata: before=%v after=%v", before, after)
	}
	code, out, message = runSetupWrite([]string{"--local", "--dry-run", "--no-hooks"}, ctx, env, root)
	if code != 0 || message != "" || out == "" {
		t.Fatalf("dry-run code=%d out=%q message=%q", code, out, message)
	}
	if after := setupFilesystemSnapshot(t, gitDir); !reflect.DeepEqual(after, before) {
		t.Fatalf("dry-run changed .git metadata: before=%v after=%v", before, after)
	}
}

func TestLocalFinalExcludeRenameFailureDoesNotClaimSetup(t *testing.T) {
	// JS: "setup --local --panes: a final exclude rename failure dies 4 with the tracked config byte-identical and no success claimed"
	root, env, ctx := localPlanFixture(t)
	conf := filepath.Join(root, ".agents", "soho.conf")
	if err := os.MkdirAll(filepath.Dir(conf), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(conf, []byte("max_workers=2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git, ok := platform.FindExecutable("git", env, platform.Current())
	if !ok {
		t.Skip("git is required for local setup tests")
	}
	for _, args := range [][]string{{"add", ".agents/soho.conf"}, {"commit", "-qm", "tracked setup config"}} {
		cmd := exec.Command(git, append([]string{"-C", root}, args...)...)
		cmd.Env = env.List()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	confBefore, err := os.ReadFile(conf)
	if err != nil {
		t.Fatal(err)
	}
	exclude := filepath.Join(root, ".git", "info", "exclude")
	if resolved, err := filepath.EvalSymlinks(exclude); err == nil {
		exclude = resolved
	}
	excludeBefore, err := os.ReadFile(exclude)
	if err != nil {
		t.Fatal(err)
	}
	if reflect.ValueOf(platformRenameFile).Pointer() != reflect.ValueOf(os.Rename).Pointer() {
		t.Fatal("platform rename seam does not default to os.Rename")
	}
	realRename := platformRenameFile
	platformRenameFile = func(src, dst string) error {
		if filepath.Clean(dst) == filepath.Clean(exclude) {
			return errors.New("EIO: injected final exclude rename failure")
		}
		return realRename(src, dst)
	}
	t.Cleanup(func() { platformRenameFile = realRename })
	code, out, message := runSetupWrite([]string{"--local", "--panes", "3"}, ctx, env, root)
	confAfter, confErr := os.ReadFile(conf)
	excludeAfter, excludeErr := os.ReadFile(exclude)
	if code != 4 || !strings.Contains(message, "could not write local excludes") || !bytes.Equal(confAfter, confBefore) || confErr != nil || !bytes.Equal(excludeAfter, excludeBefore) || excludeErr != nil {
		t.Fatalf("code=%d out=%q message=%q config=%q/%v exclude=%q/%v", code, out, message, confAfter, confErr, excludeAfter, excludeErr)
	}
	if strings.Contains(out, "panes=3") || strings.Contains(out, "block written") || strings.Contains(out, "local excludes updated") {
		t.Fatalf("claimed success before failed final rename: %q", out)
	}
	if _, err := os.Stat(filepath.Join(root, localInstructionFile)); !os.IsNotExist(err) {
		t.Fatalf("local block exists after failed final rename: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".claude")); !os.IsNotExist(err) {
		t.Fatalf("hooks exist after failed final rename: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(root, ".git", "info"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".exclude.") {
			t.Fatalf("temporary exclude file leaked: %s", entry.Name())
		}
	}
}

func TestSetupLocalJavaScriptHelperCases(t *testing.T) {
	tests := []struct {
		name string
		run  func(*testing.T)
	}{
		{"local mode: --local, --target override, setup_target default and exclusivity", func(t *testing.T) {
			ctx := &core.Config{Entries: map[string]core.ConfigEntry{"setup_target": {Value: "local", Source: "project"}}}
			env := platform.Env{}
			if !resolveSetupMode(false, "", ctx, env, "setup") || resolveSetupMode(false, "guide.md", ctx, env, "setup") || !resolveSetupMode(true, "", ctx, env, "setup") {
				t.Fatal("local mode resolution mismatch")
			}
			defer func() {
				if v := recover(); v == nil {
					t.Fatal("--local and --target were accepted together")
				}
			}()
			resolveSetupMode(true, "guide.md", ctx, env, "setup")
		}},
		{"exclude entries: root-anchored, no trailing slash so a missing dir still matches", func(t *testing.T) {
			if got := excludeEntry(filepath.Join("cache", "nested") + string(filepath.Separator)); got != "/cache/nested" {
				t.Fatalf("entry=%q", got)
			}
		}},
		{"gitDirFor: real git dir, .git-as-file, and outside git", func(t *testing.T) {
			root, env, _ := localPlanFixture(t)
			got, want := commonGitDir(root, env), filepath.Join(root, ".git")
			gotReal, _ := filepath.EvalSymlinks(got)
			wantReal, _ := filepath.EvalSymlinks(want)
			if gotReal != wantReal {
				t.Fatalf("git dir=%q", got)
			}
			if got := commonGitDir(t.TempDir(), env); got != "" {
				t.Fatalf("outside git dir=%q", got)
			}
		}},
		{"excludePathFor: the file git reads — plain repo and linked worktree", func(t *testing.T) {
			root, env, _ := localPlanFixture(t)
			plain := excludePath(root, env)
			plainReal, _ := filepath.EvalSymlinks(plain)
			wantReal, _ := filepath.EvalSymlinks(filepath.Join(root, ".git", "info", "exclude"))
			if plainReal != wantReal {
				t.Fatalf("plain exclude=%q", plain)
			}
			wt := filepath.Join(t.TempDir(), "linked")
			git, _ := platform.FindExecutable("git", env, platform.Current())
			cmd := exec.Command(git, "-C", root, "worktree", "add", "--detach", wt)
			cmd.Env = env.List()
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git worktree add: %v: %s", err, out)
			}
			got := excludePath(wt, env)
			gotReal, _ := filepath.EvalSymlinks(got)
			if gotReal != wantReal {
				t.Fatalf("linked exclude=%q", got)
			}
		}},
		{"localRels: the instruction file plus the state dir only when it lives under the root", func(t *testing.T) {
			root, env, ctx := localPlanFixture(t)
			env["SOHO_DIR"] = "nested/cache"
			if got := strings.Join(localRels(root, ctx, env, root), ","); got != "CLAUDE.local.md,nested/cache" {
				t.Fatalf("inside rels=%q", got)
			}
			env["SOHO_DIR"] = t.TempDir()
			if got := strings.Join(localRels(root, ctx, env, root), ","); got != "CLAUDE.local.md" {
				t.Fatalf("external rels=%q", got)
			}
		}},
		{"assertSafeLocalRels: glob metas, injection and traversal die 4; plain, !/#-leading and trailing-slash names pass", func(t *testing.T) {
			for _, rel := range []string{"cache", "!cache", "#cache", "nested/cache/"} {
				assertSafeLocalRels([]string{rel}, "setup --local", false)
			}
			for _, rel := range []string{"*", "a?b", "../escape", "cache "} {
				func() {
					defer func() {
						if recover() == nil {
							t.Errorf("accepted unsafe rel %q", rel)
						}
					}()
					assertSafeLocalRels([]string{rel}, "setup --local", false)
				}()
			}
			if runtime.GOOS == "windows" {
				assertSafeLocalRels([]string{`a\nb`}, "setup --local", false)
			} else {
				defer func() {
					if recover() == nil {
						t.Error(`accepted unsafe rel "a\nb"`)
					}
				}()
				assertSafeLocalRels([]string{`a\nb`}, "setup --local", false)
			}
		}},
	}
	for _, tc := range tests {
		t.Run("// JS: \""+tc.name+"\"", func(t *testing.T) { // Mutation captured: changing local path classification or ignore serialization changes tracked-file visibility.
			tc.run(t)
		})
	}
}

func TestPlanIgnoresFormerProjectConfig(t *testing.T) {
	root, env, ctx := localPlanFixture(t)
	legacy := core.LegacyProjectConfigPath(root)
	if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("lane.build.roles=scouter,implementer\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	old := platform.Stdout
	platform.Stdout = &out
	defer func() { platform.Stdout = old }()
	CmdPlan([]string{"--panes", "4"}, ctx, env, root)
	if strings.Contains(out.String(), "scouter,implementer") {
		t.Fatalf("plan read the former project config:\n%s", out.String())
	}
	if _, err := os.Stat(core.ConfigFileFor("project", env, root)); !os.IsNotExist(err) {
		t.Fatalf("plan wrote the new project file: %v", err)
	}
}
