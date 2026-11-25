package setup

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/setuptext"
)

func TestSetupGitPathParity(t *testing.T) {
	t.Run("Windows paths from Git and setup targets use native separators", func(t *testing.T) {
		if runtime.GOOS != "windows" {
			t.Skip("Windows filepath semantics require a Windows host")
		}
		root := `C:\Users\test\repo`
		if got, want := setupTargetPath(root, "OTHER.md"), `C:\Users\test\repo\OTHER.md`; got != want {
			t.Fatalf("setup target=%q, want %q", got, want)
		}
		if got, want := setupTargetPath(root, `deep\guide.md`), `C:\Users\test\repo\deep\guide.md`; got != want {
			t.Fatalf("nested setup target=%q, want %q", got, want)
		}
	})

	t.Run("stateDirRelShown uses Git separators for Windows relative state paths", func(t *testing.T) {
		if runtime.GOOS != "windows" {
			t.Skip("Windows filepath semantics require a Windows host")
		}
		root, env, ctx := localPlanFixture(t)
		parts := strings.Split(`a\b`, `\`)
		env["SOHO_DIR"] = filepath.Join(append([]string{root}, parts...)...)
		state := classifyLocalState(root, ctx, env, root)
		if state.kind != "inside" || state.rel != "a/b" || state.shown != "a/b/" {
			t.Fatalf("state classification=%+v, want inside a/b/", state)
		}
	})

	t.Run("Git exclude entries convert Windows separators on every host", func(t *testing.T) {
		if got, want := excludeEntry(`deep\cache`), "/deep/cache"; got != want {
			t.Fatalf("exclude entry=%q, want %q", got, want)
		}
	})
}

func TestInitLocalStateExclude(t *testing.T) {
	t.Run("explicit local state path goes to the common exclude", func(t *testing.T) {
		root, env, ctx := localPlanFixture(t)
		env["SOHO_DIR"] = "state cache"
		ctx.Entries["setup_target"] = core.ConfigEntry{Value: "local", Source: "project"}
		if !LocalStateSetupInEffect(root, ctx, env) {
			t.Fatal("explicit local setup was not detected")
		}
		if !EnsureLocalStateExclude(root, ctx, env, root, "init") {
			t.Fatal("init local exclude did not apply")
		}
		data, err := os.ReadFile(excludePath(root, env))
		if err != nil || !strings.Contains(string(data), "/state cache\n") {
			t.Fatalf("exclude=%q err=%v", data, err)
		}
		if _, err := os.Stat(filepath.Join(root, ".gitignore")); !os.IsNotExist(err) {
			t.Fatalf("local init wrote .gitignore: %v", err)
		}
	})

	t.Run("implicit local setup protects the state path", func(t *testing.T) {
		root, env, ctx := setupWriteUntrackedFixture(t)
		root, err := filepath.EvalSymlinks(root)
		if err != nil {
			t.Fatal(err)
		}
		env["SOHO_DIR"] = "state cache"
		writeLocalSetupFixtureFile(t, root, localInstructionFile, setuptext.SetupBlock())
		if !LocalStateSetupInEffect(root, ctx, env) {
			t.Fatal("implicit local setup was not detected")
		}
		if !EnsureLocalStateExclude(root, ctx, env, root, "init") {
			t.Fatal("init local exclude did not apply")
		}
		data, err := os.ReadFile(excludePath(root, env))
		if err != nil || !strings.Contains(string(data), "/state cache\n") {
			t.Fatalf("exclude=%q err=%v", data, err)
		}
	})

	t.Run("a missing common info directory is created after a safe lock", func(t *testing.T) {
		root, env, ctx := localPlanFixture(t)
		env["SOHO_DIR"] = "state cache"
		ctx.Entries["setup_target"] = core.ConfigEntry{Value: "local", Source: "project"}
		info := filepath.Join(commonGitDir(root, env), "info")
		if err := os.RemoveAll(info); err != nil {
			t.Fatal(err)
		}
		if !EnsureLocalStateExclude(root, ctx, env, root, "init") {
			t.Fatal("local exclude did not apply with no info directory")
		}
		data, err := os.ReadFile(excludePath(root, env))
		if err != nil || !strings.Contains(string(data), "/state cache\n") {
			t.Fatalf("exclude=%q err=%v", data, err)
		}
		status, ok := checkedExcludeStatus(root, "state cache", env)
		if !ok || status != 0 {
			t.Fatalf("check-ignore status=%d ok=%v", status, ok)
		}
	})

	t.Run("a tracked negation refuses before state is created", func(t *testing.T) {
		root, env, ctx := localPlanFixture(t)
		env["SOHO_DIR"] = "state cache"
		ctx.Entries["setup_target"] = core.ConfigEntry{Value: "local", Source: "project"}
		tracked := filepath.Join(root, ".gitignore")
		original := []byte("/state cache/\n!/state cache/\n")
		if err := os.WriteFile(tracked, original, 0o600); err != nil {
			t.Fatal(err)
		}
		func() {
			defer func() {
				value := recover()
				exit, ok := value.(*platform.ExitError)
				if !ok || exit.Code != 4 || !strings.Contains(exit.Msg, "tracked negation") {
					t.Fatalf("refusal=%#v, want exit 4 naming the negation", value)
				}
			}()
			EnsureLocalStateExclude(root, ctx, env, root, "init")
			t.Fatal("init accepted state hidden only by a tracked negation")
		}()
		data, err := os.ReadFile(tracked)
		if err != nil || !bytes.Equal(data, original) {
			t.Fatalf("tracked ignore changed: %q err=%v", data, err)
		}
	})

	t.Run("canonical setup keeps its existing behavior", func(t *testing.T) {
		root, env, ctx := localPlanFixture(t)
		env["SOHO_DIR"] = "state cache"
		if LocalStateSetupInEffect(root, ctx, env) {
			t.Fatal("canonical setup was detected as local")
		}
		exclude := excludePath(root, env)
		before, beforeErr := os.ReadFile(exclude)
		if beforeErr != nil && !os.IsNotExist(beforeErr) {
			t.Fatal(beforeErr)
		}
		if EnsureLocalStateExclude(root, ctx, env, root, "init") {
			t.Fatal("init local exclude applied outside local setup")
		}
		after, afterErr := os.ReadFile(exclude)
		if (afterErr != nil && !os.IsNotExist(afterErr)) || os.IsNotExist(beforeErr) != os.IsNotExist(afterErr) || string(before) != string(after) {
			t.Fatalf("canonical init changed the exclude: before=%q after=%q err=%v", before, after, afterErr)
		}
	})
}

// A linked checkout can ignore a path differently from the main checkout.
// The local init helper must evaluate the checkout that holds the state,
// while still writing the shared common-dir info/exclude.
// A shared repository can keep the common Git metadata read-only while its
// existing info directory is writable. The lock must live inside info.
func TestInitLocalExcludeWithReadOnlyCommonMetadata(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("Unix permission bits with a non-root user are required")
	}
	root, env, ctx := localPlanFixture(t)
	common := commonGitDir(root, env)
	info := filepath.Join(common, "info")
	if err := os.MkdirAll(info, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(common, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(common, 0o755) })
	env["SOHO_DIR"] = "state cache"
	ctx.Entries["setup_target"] = core.ConfigEntry{Value: "local", Source: "project"}
	if !EnsureLocalStateExclude(root, ctx, env, root, "init") {
		t.Fatal("local init exclude did not apply with a read-only common dir")
	}
	data, err := os.ReadFile(excludePath(root, env))
	if err != nil || !strings.Contains(string(data), "/state cache\n") {
		t.Fatalf("exclude=%q err=%v", data, err)
	}
}

// The real local consumer must merge exact CRLF bytes and keep the
// file's line ending when it appends a new state entry.
func TestLocalExcludeConsumerKeepsCRLF(t *testing.T) {
	root, env, ctx := localPlanFixture(t)
	env["SOHO_DIR"] = "state cache"
	ctx.Entries["setup_target"] = core.ConfigEntry{Value: "local", Source: "project"}
	exclude := excludePath(root, env)
	seed := "# rules\r\n/kept/\r\n"
	if err := os.WriteFile(exclude, []byte(seed), 0o660); err != nil {
		t.Fatal(err)
	}
	if !EnsureLocalStateExclude(root, ctx, env, root, "init") {
		t.Fatal("local exclude did not apply to a CRLF exclude")
	}
	data, err := os.ReadFile(exclude)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), seed) || !strings.Contains(string(data), "/state cache\r\n") {
		t.Fatalf("exclude=%q; want the original CRLF bytes plus a CRLF entry", data)
	}
}

func TestSharedExcludeLockPermissionsAndStaleFile(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("Unix group permissions with a non-root user are required")
	}
	root, env, ctx := localPlanFixture(t)
	env["SOHO_DIR"] = "state cache"
	ctx.Entries["setup_target"] = core.ConfigEntry{Value: "local", Source: "project"}
	info := filepath.Join(commonGitDir(root, env), "info")
	if err := os.MkdirAll(info, 0o700); err != nil {
		t.Fatal(err)
	}
	// MkdirAll does not change an existing directory; set the shared mode
	// explicitly so the group-writable branch really runs.
	if err := os.Chmod(info, 0o770); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(info, 0o755) })
	dirInfo, err := os.Stat(info)
	if err != nil || dirInfo.Mode().Perm()&0o070 != 0o070 {
		t.Fatalf("info mode=%v err=%v; group read/write required", dirInfo, err)
	}

	if !EnsureLocalStateExclude(root, ctx, env, root, "init") {
		t.Fatal("shared local exclude did not apply")
	}
	// The directory itself is the lock identity: no lock inode is published,
	// and a released lock is immediately reusable by the next writer.
	holder, err := core.TryLockSharedPath(info, 0o600)
	if err != nil {
		t.Fatalf("directory lock: %v", err)
	}
	holder()
	env["SOHO_DIR"] = "state cache2"
	if !EnsureLocalStateExclude(root, ctx, env, root, "init") {
		t.Fatal("a stale shared lock file blocked the next writer")
	}
	data, err := os.ReadFile(excludePath(root, env))
	if err != nil || !strings.Contains(string(data), "/state cache2\n") {
		t.Fatalf("stale-path retry did not write the new rule: %q err=%v", data, err)
	}
}

func TestExplicitGitLayoutWithoutProbeRefuses(t *testing.T) {
	_, env, ctx := localPlanFixture(t)
	root := t.TempDir()
	env["SOHO_DIR"] = "state cache"
	ctx.Entries["setup_target"] = core.ConfigEntry{Value: "local", Source: "project"}
	env["GIT_DIR"] = filepath.Join(t.TempDir(), "missing-git-dir")
	env["GIT_WORK_TREE"] = root
	func() {
		defer func() {
			value := recover()
			exit, ok := value.(*platform.ExitError)
			if !ok || exit.Code != 4 || !strings.Contains(exit.Msg, "could not verify the Git work tree") {
				t.Fatalf("refusal=%#v, want exit 4 naming the failed worktree probe", value)
			}
		}()
		EnsureLocalStateExclude(root, ctx, env, root, "init")
		t.Fatal("an explicit Git layout with a failed probe was treated as non-Git")
	}()
}

func TestInitLocalExcludeUsesStateCheckoutRules(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is required for linked worktree tests")
	}
	env := isolatedHerdrEnv(t, git)
	home := t.TempDir()
	env["HOME"] = home
	env["USERPROFILE"] = home
	env["XDG_CONFIG_HOME"] = filepath.Join(home, ".config")
	env["GIT_AUTHOR_NAME"] = "test"
	env["GIT_AUTHOR_EMAIL"] = "test@example.com"
	env["GIT_COMMITTER_NAME"] = "test"
	env["GIT_COMMITTER_EMAIL"] = "test@example.com"
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command(git, append([]string{"-C", dir}, args...)...)
		cmd.Env = env.List()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}

	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "main")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	run(root, "init", "-q")
	if err := os.WriteFile(filepath.Join(root, "seed"), []byte("seed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(root, "add", "seed")
	run(root, "commit", "-qm", "initial")
	linked := filepath.Join(parent, "linked")
	run(root, "worktree", "add", "-q", "-b", "linked", linked)

	// Main ignores the relative state path; the linked checkout does not.
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("/state-cache/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env["SOHO_SETUP_TARGET"] = "local"
	env["SOHO_DIR"] = filepath.Join(linked, "state-cache")
	cfg := core.LoadConfig(env, root)
	ctx := &cfg
	state := classifyLocalState(root, ctx, env, linked)
	if state.kind != "sibling" || state.worktree != linked {
		t.Fatalf("state classification=%+v, want sibling in %s", state, linked)
	}
	if !EnsureLocalStateExclude(root, ctx, env, linked, "init") {
		t.Fatal("local exclude did not apply to the sibling checkout")
	}
	exclude, err := os.ReadFile(excludePath(root, env))
	if err != nil || !strings.Contains(string(exclude), "/state-cache\n") {
		t.Fatalf("common exclude=%q err=%v", exclude, err)
	}
	cmd := exec.Command(git, "-C", linked, "check-ignore", "-q", "state-cache")
	cmd.Env = env.List()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("linked checkout still shows state as unignored: %v: %s", err, out)
	}
}
