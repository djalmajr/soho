package platform

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// strictRunRecorder pins the injected closure's argv and order: every call
// is recorded with a NUL join so argument boundaries stay visible.
type strictRunRecorder struct {
	calls   []string
	respond func(key string) (string, int)
}

func (r *strictRunRecorder) run(args ...string) (string, int) {
	key := strings.Join(args, "\x00")
	r.calls = append(r.calls, key)
	out, status := r.respond(key)
	return out, status
}

func strictKey(args ...string) string {
	return strings.Join(args, "\x00")
}

func TestStateProjectRootWithGitMainTree(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	rec := &strictRunRecorder{respond: func(key string) (string, int) {
		switch key {
		case strictKey("rev-parse", "--git-dir"):
			return filepath.Join(repo, ".git") + "\n", 0
		case strictKey("rev-parse", "--git-common-dir"):
			return filepath.Join(repo, ".git") + "\n", 0
		case strictKey("rev-parse", "--show-toplevel"):
			return repo + "\n", 0
		}
		return "", 128
	}}
	want, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	root, err := StateProjectRootWithGit(repo, rec.run)
	if err != nil || root != want {
		t.Fatalf("root=%q err=%v, want %q", root, err, want)
	}
	// The normal main tree takes show-toplevel through the same closure,
	// after the two required resolutions, in order.
	wantCalls := []string{
		strictKey("rev-parse", "--git-dir"),
		strictKey("rev-parse", "--git-common-dir"),
		strictKey("rev-parse", "--show-toplevel"),
	}
	if !reflect.DeepEqual(rec.calls, wantCalls) {
		t.Fatalf("calls=%q, want %q", rec.calls, wantCalls)
	}
	// The entry is uncached: a second call re-queries the closure.
	if _, err := StateProjectRootWithGit(repo, rec.run); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if len(rec.calls) != 6 {
		t.Fatalf("second call should re-query, calls=%d", len(rec.calls))
	}
}

func TestStateProjectRootWithGitLinkedWorktree(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	rec := &strictRunRecorder{respond: func(key string) (string, int) {
		switch key {
		case strictKey("rev-parse", "--git-dir"):
			return filepath.Join(repo, ".git", "worktrees", "worker") + "\n", 0
		case strictKey("rev-parse", "--git-common-dir"):
			return filepath.Join(repo, ".git") + "\n", 0
		}
		return "", 128
	}}
	want, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	root, err := StateProjectRootWithGit(filepath.Join(repo, ".worktrees", "worker"), rec.run)
	if err != nil || root != want {
		t.Fatalf("root=%q err=%v, want the main checkout %q", root, err, want)
	}
	wantCalls := []string{
		strictKey("rev-parse", "--git-dir"),
		strictKey("rev-parse", "--git-common-dir"),
	}
	if !reflect.DeepEqual(rec.calls, wantCalls) {
		t.Fatalf("calls=%q, want %q (no list or toplevel for the .git parent)", rec.calls, wantCalls)
	}
}

func TestStateProjectRootWithGitSeparateCommonDir(t *testing.T) {
	base := t.TempDir()
	common := filepath.Join(base, "container")
	worker := filepath.Join(base, "worker")
	for _, dir := range []string{common, worker} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	rec := &strictRunRecorder{respond: func(key string) (string, int) {
		switch key {
		case strictKey("rev-parse", "--git-dir"):
			return filepath.Join(common, "worktrees", "worker") + "\n", 0
		case strictKey("rev-parse", "--git-common-dir"):
			return common + "\n", 0
		case strictKey("worktree", "list", "--porcelain"):
			return "worktree " + worker + "\n", 0
		}
		return "", 128
	}}
	want, err := filepath.EvalSymlinks(worker)
	if err != nil {
		t.Fatal(err)
	}
	root, err := StateProjectRootWithGit(worker, rec.run)
	if err != nil || root != want {
		t.Fatalf("root=%q err=%v, want the first worktree %q", root, err, want)
	}
	// The first worktree sits outside the common dir, so the optional
	// core.worktree query is never issued.
	wantCalls := []string{
		strictKey("rev-parse", "--git-dir"),
		strictKey("rev-parse", "--git-common-dir"),
		strictKey("worktree", "list", "--porcelain"),
	}
	if !reflect.DeepEqual(rec.calls, wantCalls) {
		t.Fatalf("calls=%q, want %q", rec.calls, wantCalls)
	}
}

func TestStateProjectRootWithGitCoreWorktreePresent(t *testing.T) {
	base := t.TempDir()
	common := filepath.Join(base, "container")
	checkout := filepath.Join(common, "checkout")
	proj := filepath.Join(base, "proj")
	for _, dir := range []string{checkout, proj} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	rec := &strictRunRecorder{respond: func(key string) (string, int) {
		switch key {
		case strictKey("rev-parse", "--git-dir"):
			return filepath.Join(common, "worktrees", "worker") + "\n", 0
		case strictKey("rev-parse", "--git-common-dir"):
			return common + "\n", 0
		case strictKey("worktree", "list", "--porcelain"):
			return "worktree " + checkout + "\n", 0
		case strictKey("--git-dir", common, "config", "--path", "core.worktree"):
			return proj + "\n", 0
		}
		return "", 128
	}}
	want, err := filepath.EvalSymlinks(proj)
	if err != nil {
		t.Fatal(err)
	}
	root, err := StateProjectRootWithGit(checkout, rec.run)
	if err != nil || root != want {
		t.Fatalf("root=%q err=%v, want the configured core.worktree %q", root, err, want)
	}
	wantCalls := []string{
		strictKey("rev-parse", "--git-dir"),
		strictKey("rev-parse", "--git-common-dir"),
		strictKey("worktree", "list", "--porcelain"),
		strictKey("--git-dir", common, "config", "--path", "core.worktree"),
	}
	if !reflect.DeepEqual(rec.calls, wantCalls) {
		t.Fatalf("calls=%q, want %q (the contained worktree queries core.worktree through the closure)", rec.calls, wantCalls)
	}
}

func TestStateProjectRootWithGitCoreWorktreeOutcomes(t *testing.T) {
	mk := func(status int, value string) (string, error, []string) {
		base := t.TempDir()
		common := filepath.Join(base, "container")
		checkout := filepath.Join(common, "checkout")
		if err := os.MkdirAll(checkout, 0o700); err != nil {
			t.Fatal(err)
		}
		rec := &strictRunRecorder{respond: func(key string) (string, int) {
			switch key {
			case strictKey("rev-parse", "--git-dir"):
				return filepath.Join(common, "worktrees", "worker") + "\n", 0
			case strictKey("rev-parse", "--git-common-dir"):
				return common + "\n", 0
			case strictKey("worktree", "list", "--porcelain"):
				return "worktree " + checkout + "\n", 0
			case strictKey("--git-dir", common, "config", "--path", "core.worktree"):
				return value, status
			}
			return "", 128
		}}
		root, err := StateProjectRootWithGit(checkout, rec.run)
		return root, err, rec.calls
	}
	t.Run("a missing optional core.worktree with Git exit 1 is a legitimate absence, kept and refused by the guards", func(t *testing.T) {
		root, err, calls := mk(1, "")
		if err == nil || !strings.Contains(err.Error(), "not a usable checkout") || root != "" {
			t.Fatalf("root=%q err=%v, want the containment guard refusal, not a query failure", root, err)
		}
		if len(calls) != 4 {
			t.Fatalf("the absence must be queried once, calls=%q", calls)
		}
	})
	for _, tc := range []struct {
		name   string
		status int
		value  string
	}{
		{"an empty-success read is malformed data", 0, ""},
		{"a non-absence execution failure is refused", 255, "x\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, err, _ := mk(tc.status, tc.value)
			if err == nil || !strings.Contains(err.Error(), "the optional core.worktree query failed") || root != "" {
				t.Fatalf("root=%q err=%v, want the strict query failure", root, err)
			}
		})
	}
}

func TestStateProjectRootWithGitRequiredFailures(t *testing.T) {
	base := t.TempDir()
	common := filepath.Join(base, "container")
	_ = os.Mkdir(common, 0o700)
	worker := filepath.Join(base, "worker")
	_ = os.Mkdir(worker, 0o700)
	cases := []struct {
		name    string
		respond func(key string) (string, int)
		want    string
		wantMax int
	}{
		{"a failed --git-dir is a required failure", func(key string) (string, int) {
			if key == strictKey("rev-parse", "--git-common-dir") {
				return common + "\n", 0
			}
			return "", 128
		}, "the required Git resolution failed", 2},
		{"a failed --git-common-dir is a required failure", func(key string) (string, int) {
			if key == strictKey("rev-parse", "--git-dir") {
				return filepath.Join(common, "worktrees", "worker") + "\n", 0
			}
			return "", 128
		}, "the required Git resolution failed", 2},
		{"a failed main tree show-toplevel is a required failure", func(key string) (string, int) {
			switch key {
			case strictKey("rev-parse", "--git-dir"), strictKey("rev-parse", "--git-common-dir"):
				return filepath.Join(base, ".git") + "\n", 0
			}
			return "", 128
		}, "the normal main tree root resolution failed", 3},
		{"an empty main tree show-toplevel is malformed", func(key string) (string, int) {
			switch key {
			case strictKey("rev-parse", "--git-dir"), strictKey("rev-parse", "--git-common-dir"):
				return filepath.Join(base, ".git") + "\n", 0
			case strictKey("rev-parse", "--show-toplevel"):
				return "\n", 0
			}
			return "", 128
		}, "the normal main tree root resolution failed", 3},
		{"a failed worktree listing is a required failure", func(key string) (string, int) {
			switch key {
			case strictKey("rev-parse", "--git-dir"):
				return filepath.Join(common, "worktrees", "worker") + "\n", 0
			case strictKey("rev-parse", "--git-common-dir"):
				return common + "\n", 0
			}
			return "", 255
		}, "the worktree listing failed", 3},
		{"a bare first worktree is unusable", func(key string) (string, int) {
			switch key {
			case strictKey("rev-parse", "--git-dir"):
				return filepath.Join(common, "worktrees", "worker") + "\n", 0
			case strictKey("rev-parse", "--git-common-dir"):
				return common + "\n", 0
			case strictKey("worktree", "list", "--porcelain"):
				return "worktree " + worker + "\nbare\n", 0
			}
			return "", 128
		}, "no usable first worktree", 3},
		{"a listing without a worktree line is unusable", func(key string) (string, int) {
			switch key {
			case strictKey("rev-parse", "--git-dir"):
				return filepath.Join(common, "worktrees", "worker") + "\n", 0
			case strictKey("rev-parse", "--git-common-dir"):
				return common + "\n", 0
			case strictKey("worktree", "list", "--porcelain"):
				return "\n", 0
			}
			return "", 128
		}, "no usable first worktree", 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &strictRunRecorder{respond: tc.respond}
			root, err := StateProjectRootWithGit(worker, rec.run)
			if err == nil || !strings.Contains(err.Error(), tc.want) || root != "" {
				t.Fatalf("root=%q err=%v, want a %q failure with no cwd fallback", root, err, tc.want)
			}
			if len(rec.calls) > tc.wantMax {
				t.Fatalf("the failure must stop the reads, calls=%q", rec.calls)
			}
		})
	}
}

func TestStateProjectRootWithGitUnsafeRoots(t *testing.T) {
	base := t.TempDir()
	t.Run("a .git parent inside Git metadata is unsafe", func(t *testing.T) {
		outer := filepath.Join(base, ".git", "outer")
		if err := os.MkdirAll(outer, 0o700); err != nil {
			t.Fatal(err)
		}
		rec := &strictRunRecorder{respond: func(key string) (string, int) {
			switch key {
			case strictKey("rev-parse", "--git-dir"):
				return filepath.Join(outer, ".git", "worktrees", "worker") + "\n", 0
			case strictKey("rev-parse", "--git-common-dir"):
				return filepath.Join(outer, ".git") + "\n", 0
			}
			return "", 128
		}}
		root, err := StateProjectRootWithGit(outer, rec.run)
		if err == nil || !strings.Contains(err.Error(), "inside Git metadata") || root != "" {
			t.Fatalf("root=%q err=%v, want the .git segment refusal", root, err)
		}
	})
	t.Run("a listed worktree with a .git segment is unsafe", func(t *testing.T) {
		common := filepath.Join(base, "container")
		if err := os.MkdirAll(common, 0o700); err != nil {
			t.Fatal(err)
		}
		bad := filepath.Join(base, "a", ".git", "b")
		rec := &strictRunRecorder{respond: func(key string) (string, int) {
			switch key {
			case strictKey("rev-parse", "--git-dir"):
				return filepath.Join(common, "worktrees", "worker") + "\n", 0
			case strictKey("rev-parse", "--git-common-dir"):
				return common + "\n", 0
			case strictKey("worktree", "list", "--porcelain"):
				return "worktree " + bad + "\n", 0
			}
			return "", 128
		}}
		root, err := StateProjectRootWithGit(bad, rec.run)
		if err == nil || !strings.Contains(err.Error(), "not a usable checkout") || root != "" {
			t.Fatalf("root=%q err=%v, want the .git segment refusal", root, err)
		}
	})
}

func TestStateProjectRootWithGitFinalGuards(t *testing.T) {
	t.Run("a missing main tree root is refused", func(t *testing.T) {
		base := t.TempDir()
		missing := filepath.Join(base, "no-such-dir")
		rec := &strictRunRecorder{respond: func(key string) (string, int) {
			switch key {
			case strictKey("rev-parse", "--git-dir"), strictKey("rev-parse", "--git-common-dir"):
				return filepath.Join(base, ".git") + "\n", 0
			case strictKey("rev-parse", "--show-toplevel"):
				return missing + "\n", 0
			}
			return "", 128
		}}
		root, err := StateProjectRootWithGit(base, rec.run)
		if err == nil || !strings.Contains(err.Error(), "not an existing path") || root != "" {
			t.Fatalf("root=%q err=%v, want the existence refusal", root, err)
		}
	})
	t.Run("a file main tree root is refused", func(t *testing.T) {
		base := t.TempDir()
		file := filepath.Join(base, "file")
		if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		rec := &strictRunRecorder{respond: func(key string) (string, int) {
			switch key {
			case strictKey("rev-parse", "--git-dir"), strictKey("rev-parse", "--git-common-dir"):
				return filepath.Join(base, ".git") + "\n", 0
			case strictKey("rev-parse", "--show-toplevel"):
				return file + "\n", 0
			}
			return "", 128
		}}
		root, err := StateProjectRootWithGit(base, rec.run)
		if err == nil || !strings.Contains(err.Error(), "not an existing directory") || root != "" {
			t.Fatalf("root=%q err=%v, want the directory refusal", root, err)
		}
	})
	t.Run("filesystem aliases are canonicalized", func(t *testing.T) {
		base := t.TempDir()
		if err := os.Mkdir(filepath.Join(base, ".git"), 0700); err != nil {
			t.Fatal(err)
		}
		real := filepath.Join(base, "real")
		if err := os.Mkdir(real, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(base, "link")
		if err := os.Symlink(real, link); err != nil {
			skipUnavailableSymlink(t, err)
		}
		rec := &strictRunRecorder{respond: func(key string) (string, int) {
			switch key {
			case strictKey("rev-parse", "--git-dir"), strictKey("rev-parse", "--git-common-dir"):
				return filepath.Join(base, ".git") + "\n", 0
			case strictKey("rev-parse", "--show-toplevel"):
				return link + "\n", 0
			}
			return "", 128
		}}
		want, err := filepath.EvalSymlinks(link)
		if err != nil {
			t.Fatal(err)
		}
		root, err := StateProjectRootWithGit(base, rec.run)
		if err != nil || root != want {
			t.Fatalf("root=%q err=%v, want the canonical %q", root, err, want)
		}
	})
}

// cleanedGitEnv returns the process environment with every GIT_* variable
// removed and the reference-lint cleaning applied: the caller's own routing,
// never the inherited process Git state.
func cleanedGitEnv() []string {
	var out []string
	for _, kv := range os.Environ() {
		key := kv[:strings.IndexByte(kv, '=')]
		if strings.HasPrefix(strings.ToUpper(key), "GIT_") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_OPTIONAL_LOCKS=0")
}

// cleanedGitRun is the test's real-subprocess closure: git resolved once,
// run in cwd with the cleaned environment, returning (stdout, exit code).
func cleanedGitRun(t *testing.T, cwd string, args ...string) (string, int) {
	t.Helper()
	resolved, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	cmd := exec.Command(resolved, args...)
	cmd.Dir = cwd
	cmd.Env = cleanedGitEnv()
	out, err := cmd.Output()
	if err == nil {
		return string(out), 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return string(out), exitErr.ExitCode()
	}
	t.Fatalf("git execution failed: %v", err)
	return "", -1
}

func TestStateProjectRootWithGitNoGlobalState(t *testing.T) {
	resetRootCacheForTests()
	t.Cleanup(resetRootCacheForTests)
	sentinelCalls := 0
	old := gitRunner
	gitRunner = func(_ Env, _ string, _ ...string) (string, int) {
		sentinelCalls++
		return "", 128
	}
	t.Cleanup(func() { gitRunner = old })

	t.Run("the strict entry never uses the global runner or the root cache", func(t *testing.T) {
		repo := t.TempDir()
		if err := os.Mkdir(filepath.Join(repo, ".git"), 0700); err != nil {
			t.Fatal(err)
		}
		rec := &strictRunRecorder{respond: func(key string) (string, int) {
			switch key {
			case strictKey("rev-parse", "--git-dir"), strictKey("rev-parse", "--git-common-dir"):
				return filepath.Join(repo, ".git") + "\n", 0
			case strictKey("rev-parse", "--show-toplevel"):
				return repo + "\n", 0
			}
			return "", 128
		}}
		want, err := filepath.EvalSymlinks(repo)
		if err != nil {
			t.Fatal(err)
		}
		root, err := StateProjectRootWithGit(repo, rec.run)
		if err != nil || root != want {
			t.Fatalf("root=%q err=%v, want %q", root, err, want)
		}
		if sentinelCalls != 0 {
			t.Fatalf("the global gitRunner was used %d time(s)", sentinelCalls)
		}
		if len(rootCache.values) != 0 {
			t.Fatalf("the root cache gained entries: %d", len(rootCache.values))
		}
	})

	t.Run("the injected closure decides even under poisoned process GIT variables", func(t *testing.T) {
		base := t.TempDir()
		repo := filepath.Join(base, "repo")
		if err := os.Mkdir(repo, 0o700); err != nil {
			t.Fatal(err)
		}
		gitInit(t, repo)
		bogus := filepath.Join(base, "bogus")
		t.Setenv("GIT_DIR", bogus)
		t.Setenv("GIT_COMMON_DIR", bogus)
		// The cleaned closure ignores the poisoned process Git state.
		root, err := StateProjectRootWithGit(repo, func(args ...string) (string, int) {
			return cleanedGitRun(t, repo, args...)
		})
		want, syErr := filepath.EvalSymlinks(repo)
		if syErr != nil {
			t.Fatal(syErr)
		}
		if err != nil || root != want {
			t.Fatalf("cleaned closure: root=%q err=%v, want %q", root, err, want)
		}
		// The control closure inherits the poisoned process environment:
		// the same entry now fails, proving the callback decides.
		_, err = StateProjectRootWithGit(repo, func(args ...string) (string, int) {
			cmd := exec.Command("git", args...)
			cmd.Dir = repo
			cmd.Env = nil
			out, rerr := cmd.Output()
			if rerr == nil {
				return string(out), 0
			}
			var exitErr *exec.ExitError
			if errors.As(rerr, &exitErr) {
				return string(out), exitErr.ExitCode()
			}
			return string(out), -1
		})
		if err == nil {
			t.Fatal("the poisoned control closure should fail the strict entry")
		}
	})
}

func TestStateProjectRootWithGitRealRepo(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	// Fixture setup through the same cleaned subprocess routing.
	cleaned := func(args ...string) (string, int) { return cleanedGitRun(t, repo, args...) }
	if out, status := cleaned("init", "-q"); status != 0 {
		t.Fatalf("git init: %q %d", out, status)
	}
	if out, status := cleaned("config", "user.email", "test@example.invalid"); status != 0 {
		t.Fatalf("git config email: %q %d", out, status)
	}
	if out, status := cleaned("config", "user.name", "Test"); status != 0 {
		t.Fatalf("git config name: %q %d", out, status)
	}
	if err := os.WriteFile(filepath.Join(repo, "tracked"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, status := cleaned("add", "tracked"); status != 0 {
		t.Fatalf("git add: %q %d", out, status)
	}
	if out, status := cleaned("commit", "-qm", "fixture"); status != 0 {
		t.Fatalf("git commit: %q %d", out, status)
	}
	worker := filepath.Join(base, "worker")
	if out, status := cleaned("worktree", "add", "-q", "-b", "worker", worker); status != 0 {
		t.Fatalf("git worktree add: %q %d", out, status)
	}
	want, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("a linked worktree resolves the canonical main checkout root", func(t *testing.T) {
		root, err := StateProjectRootWithGit(worker, func(args ...string) (string, int) {
			return cleanedGitRun(t, worker, args...)
		})
		if err != nil || root != want {
			t.Fatalf("root=%q err=%v, want the canonical main root %q", root, err, want)
		}
	})
	t.Run("the main checkout resolves its own canonical root", func(t *testing.T) {
		root, err := StateProjectRootWithGit(repo, func(args ...string) (string, int) {
			return cleanedGitRun(t, repo, args...)
		})
		if err != nil || root != want {
			t.Fatalf("root=%q err=%v, want %q", root, err, want)
		}
	})
	t.Run("a non-git cwd is a required failure, never a cwd fallback", func(t *testing.T) {
		root, err := StateProjectRootWithGit(base, func(args ...string) (string, int) {
			return cleanedGitRun(t, base, args...)
		})
		if err == nil || root != "" {
			t.Fatalf("root=%q err=%v, want the required failure", root, err)
		}
	})
}
