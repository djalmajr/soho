package spawn

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func trustGit(t *testing.T, cwd string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", cwd}, args...)...)
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "GIT_") && value == "" {
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %q: %v: %s", args, err, out)
	}
}

func trustTestEnv(t *testing.T) (string, platform.Env) {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git missing: real repository fixture unavailable")
	}
	root := t.TempDir()
	env := platform.Env{}
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "TMPDIR", "PATH"} {
		value := filepath.Join(root, strings.ToLower(key))
		if err := os.MkdirAll(value, 0700); err != nil {
			t.Fatal(err)
		}
		env[key] = value
	}
	if err := os.Symlink(git, filepath.Join(env["PATH"], filepath.Base(git))); err != nil {
		t.Skipf("git fixture symlink unavailable: %v", err)
	}
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "TMPDIR", "PATH"} {
		t.Setenv(key, env[key])
	}
	// Ignore the operator's Git settings and injected repository context.
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	for _, key := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_DISCOVERY_ACROSS_FILESYSTEM", "GIT_CEILING_DIRECTORIES"} {
		t.Setenv(key, "")
	}
	env["GIT_CONFIG_NOSYSTEM"] = "1"
	env["GIT_CONFIG_GLOBAL"] = os.DevNull
	return root, env
}

func trustRepo(t *testing.T, root, name string) string {
	t.Helper()
	repo := filepath.Join(root, name)
	if err := os.MkdirAll(repo, 0700); err != nil {
		t.Fatal(err)
	}
	trustGit(t, repo, "init", "-q")
	return repo
}

func TestTrustRoot(t *testing.T) {
	for _, name := range []string{"plain", "subdirectory", "linked", "symlink", "no-repo", "bare", "bare-dotgit", "submodule", "home", "home-ancestor", "jobs", "jobs-root", "quote", "git-env", "owner", "control", "no-git"} {
		t.Run(name, func(t *testing.T) {
			base, env := trustTestEnv(t)
			repo := trustRepo(t, base, "repo.with dots")
			resolved, err := filepath.EvalSymlinks(repo)
			if err != nil {
				t.Fatal(err)
			}
			cwd, want := repo, resolved
			owned := trustOwned
			switch name {
			case "subdirectory":
				cwd = filepath.Join(repo, "sub")
				if err := os.Mkdir(cwd, 0700); err != nil {
					t.Fatal(err)
				}
			case "linked":
				trustGit(t, repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "fixture")
				cwd = filepath.Join(base, "linked")
				trustGit(t, repo, "worktree", "add", "-qb", "linked", cwd)
			case "symlink":
				cwd = filepath.Join(base, "alias")
				if err := os.Symlink(repo, cwd); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			case "no-repo":
				cwd = env["HOME"]
				want = ""
			case "bare", "bare-dotgit":
				cwd = filepath.Join(base, "bare.git")
				if name == "bare-dotgit" {
					cwd = filepath.Join(base, "bare", ".git")
				}
				if err := os.MkdirAll(cwd, 0700); err != nil {
					t.Fatal(err)
				}
				trustGit(t, cwd, "init", "--bare", "-q")
				want = ""
			case "submodule":
				common := filepath.Join(repo, ".git", "modules", "sub")
				if err := os.MkdirAll(filepath.Dir(common), 0700); err != nil {
					t.Fatal(err)
				}
				cwd = filepath.Join(base, "submodule")
				if err := os.Mkdir(cwd, 0700); err != nil {
					t.Fatal(err)
				}
				trustGit(t, cwd, "init", "-q", "--separate-git-dir", common)
				want = ""
			case "home":
				env["HOME"] = repo
				want = ""
			case "home-ancestor":
				env["HOME"] = filepath.Join(repo, "home")
				if err := os.Mkdir(env["HOME"], 0700); err != nil {
					t.Fatal(err)
				}
				want = ""
			case "jobs", "jobs-root":
				jobs := filepath.Join(env["HOME"], ".herdr-jobs")
				if err := os.Mkdir(jobs, 0700); err != nil {
					t.Fatal(err)
				}
				if name == "jobs-root" {
					cwd = jobs
					trustGit(t, cwd, "init", "-q")
				} else {
					cwd = trustRepo(t, jobs, "worker")
				}
				want = ""
			case "quote":
				cwd = trustRepo(t, base, "O'Brien.with dots")
				want, _ = filepath.EvalSymlinks(cwd)
			case "git-env":
				other := trustRepo(t, base, "other")
				env["GIT_DIR"] = filepath.Join(other, ".git")
			case "owner":
				owned = func(os.FileInfo) bool { return false }
				want = ""
			case "control":
				cwd = trustRepo(t, base, "bad\npath")
				want = ""
			case "no-git":
				env["PATH"] = filepath.Join(base, "missing")
				want = ""
			}
			got, reason := trustRootEnv(cwd, env, owned)
			if got != want || (reason == "") != (want != "") {
				t.Fatalf("root=%q reason=%q want=%q cwd=%q", got, reason, want, cwd)
			}
			if name == "quote" {
				if got := trustCwdArgument(got); got != `projects={"`+want+`"={trust_level="trusted"}}` {
					t.Fatalf("argument=%q", got)
				}
			}
			if name == "plain" {
				if got, why := trustRoot(cwd); got != want || why != "" {
					t.Fatalf("trustRoot=%q %q", got, why)
				}
			}
		})
	}
}

func TestTrustCwdArgument(t *testing.T) {
	for _, tc := range []struct{ root, want string }{
		{`/repo.with dots`, `projects={'/repo.with dots'={trust_level="trusted"}}`},
		{`C:\O'Brien\a"b`, `projects={"C:\\O'Brien\\a\"b"={trust_level="trusted"}}`},
	} {
		if got := trustCwdArgument(tc.root); got != tc.want {
			t.Fatalf("got=%q want=%q", got, tc.want)
		}
	}
}

func TestSpawnTrustCwd(t *testing.T) {
	for _, tc := range []struct {
		kind, mode string
		refused    bool
	}{{"codex", "on", false}, {"codex", "off", false}, {"codex", "invalid", false}, {"codex", "on", true}, {"claude", "on", false}, {"grok", "on", false}} {
		t.Run(tc.kind+"-"+tc.mode+map[bool]string{true: "-refused"}[tc.refused], func(t *testing.T) {
			_, gitEnv := trustTestEnv(t)
			f := newSpawnFixture(t, freshSpawnRules())
			configureSpawnFixture(t, &f)
			for _, k := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "TMPDIR"} {
				f.env[k] = gitEnv[k]
			}
			f.env["PATH"] = f.bin + string(os.PathListSeparator) + gitEnv["PATH"]
			for _, k := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "TMPDIR", "PATH"} {
				t.Setenv(k, f.env[k])
			}
			f.env["GIT_CONFIG_GLOBAL"] = os.DevNull
			f.env["GIT_CONFIG_NOSYSTEM"] = "1"
			f.cwd = filepath.Join(f.cwd, "O'Brien.with spaces")
			if err := os.Mkdir(f.cwd, 0700); err != nil {
				t.Fatal(err)
			}
			trustGit(t, f.cwd, "init", "-q")
			f.env["SOHO_TRUST_CWD"] = tc.mode
			f.env["SOHO_LAYOUT"] = "tab"
			if tc.refused {
				f.env["HOME"] = f.cwd
			}
			if tc.kind == "claude" {
				if _, err := fakecli.Install(t, f.bin, tc.kind, nil); err != nil {
					t.Fatal(err)
				}
			}
			f.ctx.Entries["args_"+tc.kind] = core.ConfigEntry{Value: "--owner-config", Source: "user"}
			f.ctx.Entries["role_worker_kind"] = core.ConfigEntry{Value: tc.kind, Source: "project"}
			f.ctx.Entries["role_worker_args"] = core.ConfigEntry{Value: "--owner-role", Source: "project"}
			mcp := "inherit"
			if tc.kind == "codex" {
				installCodexMcpList(t, f, `[{"name":"fixture","enabled":true}]`)
				mcp = "none"
			}
			code, _, stderr, _ := runCmdSpawn(t, f, []string{"worker", "--name", "worker", "--kind", tc.kind, "--effort", "high", "--mcp", mcp, "--", "--owner-cli"})
			if code != 0 {
				t.Fatalf("exit=%d stderr=%q", code, stderr)
			}
			tail := startTail(t, f)
			root, _ := filepath.EvalSymlinks(f.cwd)
			// The exact trust pair must sit between generated args and owner args.
			index := -1
			for i, a := range tail {
				if strings.HasPrefix(a, "projects={") {
					index = i
				}
			}
			enabled := tc.kind == "codex" && tc.mode == "on" && !tc.refused
			if enabled {
				wantTrust := `projects={"` + root + `"={trust_level="trusted"}}`
				if index < 1 || tail[index-1] != "-c" || tail[index] != wantTrust || !reflect.DeepEqual(tail[index+1:], []string{"--owner-config", "--owner-role", "--owner-cli"}) {
					t.Fatalf("argv=%q", tail)
				}
			} else if index != -1 {
				t.Fatalf("unexpected trust argv=%q", tail)
			}
			if enabled && (index < 5 || !reflect.DeepEqual(tail[index-5:index-1], []string{"-c", `model_reasoning_effort="high"`, "-c", "mcp_servers.fixture.enabled=false"})) {
				t.Fatalf("trust must follow effort and MCP: %q", tail)
			}
			if tc.refused {
				exact := "spawn: trust_cwd: repository root is home or an ancestor of home; 'worker' starts without a trust override (codex may ask to trust the folder)\n"
				if strings.Count(stderr, exact) != 1 {
					t.Fatalf("stderr=%q want line=%q", stderr, exact)
				}
			} else if strings.Contains(stderr, "trust_cwd:") {
				t.Fatalf("unexpected warning: %q", stderr)
			}
		})
	}
}

func TestSpawnTrustCwdHerdrBlockedFolderDialog(t *testing.T) {
	screen, err := os.ReadFile("testdata/codex-folder-trust.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"off", "on"} {
		t.Run(mode, func(t *testing.T) {
			rules := []fakecli.Rule{
				{Argv: []string{"agent", "start"}, ArgvPrefix: true, Code: 1, Stdout: `{"error":{"code":"agent_not_ready"}}`},
				{Argv: grokScreenRead("worker"), Stdout: string(screen)},
			}
			f := newSpawnFixture(t, append(rules, freshSpawnRules()...))
			configureSpawnFixture(t, &f)
			if _, err := fakecli.Install(t, f.bin, "codex", nil); err != nil {
				t.Fatal(err)
			}
			f.env["SOHO_TRUST_CWD"] = mode
			code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"worker", "--kind", "codex", "--mcp", "inherit"})
			if code != 7 || !strings.Contains(stdout, `"status": "blocked_at_startup"`) || !strings.Contains(stdout, "Trust this folder?") {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			if keys := agentSendKeys(calls); len(keys) != 0 {
				t.Fatalf("typed into folder dialog: %q", keys)
			}
			if strings.Contains(strings.Join(startTail(t, f), " "), "projects={") {
				t.Fatal("off or non-repository cwd must not get trust override")
			}
		})
	}
}

func TestTrustRootVolumeAndControl(t *testing.T) {
	_, env := trustTestEnv(t)
	for _, root := range []string{filepath.VolumeName(env["HOME"]) + string(filepath.Separator), "/bad\x00path", "/bad\tpath", "/bad\x7fpath"} {
		if got, reason := trustRootPath(root, env, trustOwned); got != "" || reason == "" {
			t.Fatalf("accepted unsafe root=%q result=%q %q", root, got, reason)
		}
	}
}
