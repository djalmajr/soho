package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
)

type mutationCopyFixture struct {
	root, source, home, tmp string
	env                     platform.Env
}

func newMutationCopyFixture(t *testing.T) mutationCopyFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available on PATH")
	}
	root, err := os.MkdirTemp("", "ha mutation copy ")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })

	home := filepath.Join(root, "home")
	tmp := filepath.Join(root, "tmp")
	source := filepath.Join(root, "source")
	for _, dir := range []string{home, tmp, source} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	env := make(platform.Env)
	for _, entry := range testutil.CleanEnv(t) {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			env[key] = value
		}
	}
	env["HOME"] = home
	env["USERPROFILE"] = home
	env["XDG_CONFIG_HOME"] = filepath.Join(root, "config")
	env["TMPDIR"] = tmp
	delete(env, "CARGO_TARGET_DIR")
	delete(env, "CARGO_BUILD_TARGET_DIR")
	env["GIT_CONFIG_GLOBAL"] = os.DevNull
	env["GIT_CONFIG_SYSTEM"] = os.DevNull
	env["GIT_AUTHOR_NAME"] = "mutation-copy fixture"
	env["GIT_AUTHOR_EMAIL"] = "fixture@example.invalid"
	env["GIT_COMMITTER_NAME"] = "mutation-copy fixture"
	env["GIT_COMMITTER_EMAIL"] = "fixture@example.invalid"

	fixture := mutationCopyFixture{root: root, source: source, home: home, tmp: tmp, env: env}
	gitRepoAll(t, fixture, source, ".gitignore", "ignored.txt\n", "tracked.txt", "v1\n", filepath.Join("sub", "deep.txt"), "deep\n")
	return fixture
}

// gitRepoAll inits a repository at dir and commits the given name/content pairs.
func gitRepoAll(t *testing.T, fixture mutationCopyFixture, dir string, files ...string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(files); i += 2 {
		path := filepath.Join(dir, files[i])
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(files[i+1]), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fixtureGit(t, fixture, dir, "init")
	fixtureGit(t, fixture, dir, "add", "-A")
	fixtureGit(t, fixture, dir, "commit", "-m", "fixture")
}

func fixtureGit(t *testing.T, fixture mutationCopyFixture, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = fixture.env.List()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(output)
}

func runMutationCopyCmd(t *testing.T, fixture mutationCopyFixture, workDir string, overrides platform.Env, args ...string) (int, string, string) {
	t.Helper()
	oldCwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(workDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCwd) })
	env := fixture.env.Clone()
	for key, value := range overrides {
		env[key] = value
	}
	var stdout, stderr bytes.Buffer
	oldOut, oldErr := platform.Stdout, platform.Stderr
	platform.Stdout, platform.Stderr = &stdout, &stderr
	t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
	code := Run(append([]string{"mutation-copy"}, args...), env)
	return code, stdout.String(), stderr.String()
}

func decodedCopy(t *testing.T, out string) struct {
	Copy   string             `json:"copy"`
	Source string             `json:"source"`
	Files  int                `json:"files"`
	Links  []mutationCopyLink `json:"links"`
} {
	t.Helper()
	var got struct {
		Copy   string             `json:"copy"`
		Source string             `json:"source"`
		Files  int                `json:"files"`
		Links  []mutationCopyLink `json:"links"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &got); err != nil {
		t.Fatalf("stdout is not the result JSON: %v\n%s", err, out)
	}
	return got
}

func TestMutationCopy(t *testing.T) {
	t.Run("copies the modified tracked file and the untracked one from the disk, skips the ignored one", func(t *testing.T) {
		f := newMutationCopyFixture(t)
		if err := os.WriteFile(filepath.Join(f.source, "tracked.txt"), []byte("v2\n"), 0o640); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(f.source, "tracked.txt"), 0o640); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.source, "untracked.txt"), []byte("new\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.source, "ignored.txt"), []byte("skip\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		// 0666 on disk: the copy must preserve the mode, not the umask result.
		if err := os.WriteFile(filepath.Join(f.source, "open.txt"), []byte("open\n"), 0o666); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(f.source, "open.txt"), 0o666); err != nil {
			t.Fatal(err)
		}
		dest := filepath.Join(f.root, "copy")
		if err := os.Mkdir(dest, 0o700); err != nil { // an existing empty destination is accepted
			t.Fatal(err)
		}
		code, out, errOut := runMutationCopyCmd(t, f, f.root, nil, "--source", f.source, "--dest", dest)
		if code != 0 {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
		got := decodedCopy(t, out)
		if got.Source != f.source || got.Copy != dest {
			t.Fatalf("source=%q copy=%q; want %q / %q", got.Source, got.Copy, f.source, dest)
		}
		if got.Files != 5 {
			t.Fatalf("files=%d; want 5 (.gitignore, sub/deep.txt, tracked.txt, untracked.txt, open.txt)\n%s", got.Files, out)
		}
		if content, err := os.ReadFile(filepath.Join(dest, "tracked.txt")); err != nil || string(content) != "v2\n" {
			t.Fatalf("tracked.txt was not copied from the disk: %q %v", content, err)
		}
		if runtime.GOOS != "windows" {
			// On Windows the permission bits are not preserved by the copy.
			if info, err := os.Stat(filepath.Join(dest, "tracked.txt")); err != nil || info.Mode().Perm() != 0o640 {
				t.Fatalf("tracked.txt mode not preserved: %v %v", info, err)
			}
		}
		if content, err := os.ReadFile(filepath.Join(dest, "open.txt")); err != nil || string(content) != "open\n" {
			t.Fatalf("open.txt missing: %q %v", content, err)
		}
		if runtime.GOOS != "windows" {
			// On Windows the permission bits are not preserved by the copy.
			if info, err := os.Stat(filepath.Join(dest, "open.txt")); err != nil || info.Mode().Perm() != 0o666 {
				t.Fatalf("open.txt mode not preserved: %v %v", info, err)
			}
		}
		if content, err := os.ReadFile(filepath.Join(dest, "untracked.txt")); err != nil || string(content) != "new\n" {
			t.Fatalf("untracked.txt missing: %q %v", content, err)
		}
		if content, err := os.ReadFile(filepath.Join(dest, "sub", "deep.txt")); err != nil || string(content) != "deep\n" {
			t.Fatalf("sub/deep.txt missing: %q %v", content, err)
		}
		if _, err := os.Stat(filepath.Join(dest, "ignored.txt")); !os.IsNotExist(err) {
			t.Fatal("the ignored file was copied")
		}
		if _, err := os.Stat(filepath.Join(dest, ".git")); !os.IsNotExist(err) {
			t.Fatal(".git was copied")
		}
	})

	t.Run("refuses the empty, the root and the HOME sources and creates nothing", func(t *testing.T) {
		f := newMutationCopyFixture(t)
		missing := filepath.Join(f.root, "must-not-exist")
		code, out, errOut := runMutationCopyCmd(t, f, f.root, nil, "--source", "", "--dest", missing)
		if code != 2 || !strings.Contains(errOut, "usage: mutation-copy") {
			t.Fatalf("empty source: code=%d out=%q err=%q; want usage refusal", code, out, errOut)
		}
		code, out, errOut = runMutationCopyCmd(t, f, f.root, nil, "--source", "/", "--dest", missing)
		if code != 2 || !strings.Contains(errOut, "mutation-copy: refusing source '/'") || !strings.Contains(errOut, "the filesystem root") {
			t.Fatalf("root source: code=%d out=%q err=%q", code, out, errOut)
		}
		code, out, errOut = runMutationCopyCmd(t, f, f.root, nil, "--source", f.home, "--dest", missing)
		if code != 2 || !strings.Contains(errOut, "mutation-copy: refusing source '") || !strings.Contains(errOut, "HOME or above it") {
			t.Fatalf("HOME source: code=%d out=%q err=%q", code, out, errOut)
		}
		code, out, errOut = runMutationCopyCmd(t, f, f.root, nil, "--source", f.root, "--dest", missing)
		if code != 2 || !strings.Contains(errOut, "HOME or above it") {
			t.Fatalf("source above HOME: code=%d out=%q err=%q", code, out, errOut)
		}
		plain := filepath.Join(f.root, "plain")
		if err := os.Mkdir(plain, 0o700); err != nil {
			t.Fatal(err)
		}
		code, out, errOut = runMutationCopyCmd(t, f, f.root, nil, "--source", plain, "--dest", missing)
		if code != 2 || !strings.Contains(errOut, "not a git worktree") {
			t.Fatalf("non-worktree source: code=%d out=%q err=%q", code, out, errOut)
		}
		// Without --source the refusal names the path actually attempted (the
		// git toplevel or the current directory), not an empty value.
		gitRepoAll(t, f, f.home, "home-repo.txt", "h\n")
		code, out, errOut = runMutationCopyCmd(t, f, f.home, nil, "--dest", missing)
		if code != 2 || !strings.Contains(errOut, "mutation-copy: refusing source '"+f.home+"'") || !strings.Contains(errOut, "HOME or above it") {
			t.Fatalf("default source refusal value: code=%d out=%q err=%q; want the attempted path in the message", code, out, errOut)
		}
		if _, err := os.Stat(missing); !os.IsNotExist(err) {
			t.Fatal("a destination was created for a refused source")
		}
		if entries, err := os.ReadDir(f.tmp); err != nil || len(entries) != 0 {
			t.Fatalf("a temp destination was created for a refused source: %v %v", entries, err)
		}
	})

	t.Run("refuses the root and the HOME destinations and creates nothing", func(t *testing.T) {
		f := newMutationCopyFixture(t)
		missing := filepath.Join(f.root, "must-not-exist")
		root := filepath.VolumeName(".") + string(filepath.Separator)
		code, out, errOut := runMutationCopyCmd(t, f, f.root, nil, "--source", f.source, "--dest", root)
		if code != 2 || !strings.Contains(errOut, "mutation-copy: refusing destination '"+root+"'") || !strings.Contains(errOut, "the filesystem root") {
			t.Fatalf("root destination: code=%d out=%q err=%q", code, out, errOut)
		}
		code, out, errOut = runMutationCopyCmd(t, f, f.root, nil, "--source", f.source, "--dest", f.home)
		if code != 2 || !strings.Contains(errOut, "mutation-copy: refusing destination '") || !strings.Contains(errOut, "HOME or above it") {
			t.Fatalf("HOME destination: code=%d out=%q err=%q", code, out, errOut)
		}
		code, out, errOut = runMutationCopyCmd(t, f, f.root, nil, "--source", f.source, "--dest", f.root)
		if code != 2 || !strings.Contains(errOut, "mutation-copy: refusing destination '") || !strings.Contains(errOut, "HOME or above it") {
			t.Fatalf("destination above HOME: code=%d out=%q err=%q", code, out, errOut)
		}
		if _, err := os.Stat(missing); !os.IsNotExist(err) {
			t.Fatal("a destination was created for a refused destination")
		}
	})

	t.Run("refuses a destination inside the source and a source inside the destination", func(t *testing.T) {
		f := newMutationCopyFixture(t)
		code, out, errOut := runMutationCopyCmd(t, f, f.root, nil, "--source", f.source, "--dest", filepath.Join(f.source, "nested"))
		if code != 2 || !strings.Contains(errOut, "mutation-copy: refusing destination '") || !strings.Contains(errOut, "destination is inside the source") {
			t.Fatalf("dest inside source: code=%d out=%q err=%q", code, out, errOut)
		}
		inner := filepath.Join(f.root, "outer", "repo")
		gitRepoAll(t, f, inner, "file.txt", "x\n")
		code, out, errOut = runMutationCopyCmd(t, f, f.root, nil, "--source", inner, "--dest", filepath.Join(f.root, "outer"))
		if code != 2 || !strings.Contains(errOut, "mutation-copy: refusing destination '") || !strings.Contains(errOut, "source is inside the destination") {
			t.Fatalf("source inside dest: code=%d out=%q err=%q", code, out, errOut)
		}
		if entries, err := os.ReadDir(filepath.Join(f.root, "outer")); err != nil || len(entries) != 1 || entries[0].Name() != "repo" {
			t.Fatalf("the refused destination was touched by the command: %v %v", entries, err)
		}
	})

	t.Run("refuses an existing non-empty destination", func(t *testing.T) {
		f := newMutationCopyFixture(t)
		busy := filepath.Join(f.root, "busy")
		if err := os.MkdirAll(busy, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(busy, "stale.txt"), []byte("old\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, errOut := runMutationCopyCmd(t, f, f.root, nil, "--source", f.source, "--dest", busy)
		if code != 2 || !strings.Contains(errOut, "mutation-copy: refusing destination '") || !strings.Contains(errOut, "not empty") {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
		if content, err := os.ReadFile(filepath.Join(busy, "stale.txt")); err != nil || string(content) != "old\n" {
			t.Fatal("the pre-existing destination was touched")
		}
	})

	t.Run("links a dependency from outside the worktree and refuses a link into the source", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlinks need elevation on Windows without developer mode")
		}
		f := newMutationCopyFixture(t)
		deps := filepath.Join(f.root, "deps")
		if err := os.MkdirAll(deps, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(deps, "pkg.json"), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		dest := filepath.Join(f.root, "linkcopy")
		code, out, errOut := runMutationCopyCmd(t, f, f.root, nil, "--source", f.source, "--dest", dest, "--link", "node_modules="+deps)
		if code != 0 {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
		got := decodedCopy(t, out)
		if len(got.Links) != 1 || got.Links[0].Path != "node_modules" || got.Links[0].Target != deps {
			t.Fatalf("links=%#v; want one node_modules link to %q", got.Links, deps)
		}
		info, err := os.Lstat(filepath.Join(dest, "node_modules"))
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("node_modules is not a symlink: %v %v", info, err)
		}
		if target, err := os.Readlink(filepath.Join(dest, "node_modules")); err != nil || target != deps {
			t.Fatalf("node_modules target=%q %v; want %q", target, err, deps)
		}
		if content, err := os.ReadFile(filepath.Join(dest, "node_modules", "pkg.json")); err != nil || string(content) != "{}\n" {
			t.Fatalf("the link does not reach the dependency: %q %v", content, err)
		}

		missing := filepath.Join(f.root, "must-not-exist")
		code, out, errOut = runMutationCopyCmd(t, f, f.root, nil, "--source", f.source, "--dest", missing, "--link", "node_modules="+f.source)
		if code != 2 || !strings.Contains(errOut, "mutation-copy: refusing link '") || !strings.Contains(errOut, "target is inside the source; install the dependencies in the copy instead (an offline install from the package manager's store)") {
			t.Fatalf("link into source: code=%d out=%q err=%q", code, out, errOut)
		}
		if _, err := os.Stat(missing); !os.IsNotExist(err) {
			t.Fatal("the created destination was not removed for a refused link")
		}
		code, out, errOut = runMutationCopyCmd(t, f, f.root, nil, "--source", f.source, "--dest", missing, "--link", "../up="+deps)
		if code != 2 || !strings.Contains(errOut, "refusing link") || !strings.Contains(errOut, "'..'") {
			t.Fatalf("escaping relpath: code=%d out=%q err=%q", code, out, errOut)
		}
		code, out, errOut = runMutationCopyCmd(t, f, f.root, nil, "--source", f.source, "--dest", missing, "--link", "/abs="+deps)
		if code != 2 || !strings.Contains(errOut, "refusing link") || !strings.Contains(errOut, "relpath must be relative") {
			t.Fatalf("absolute relpath: code=%d out=%q err=%q", code, out, errOut)
		}
	})

	t.Run("refuses a repository symlink that escapes the copy and removes the created destination", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlinks need elevation on Windows without developer mode")
		}
		f := newMutationCopyFixture(t)
		absLink := filepath.Join(f.source, "abslink")
		if err := os.Symlink("/nonexistent/absolute-target", absLink); err != nil {
			// The Windows skip above is the only sanctioned skip; on any other
			// system a symlink that cannot be created is a fixture failure.
			t.Fatalf("cannot create a symlink in the fixture: %v", err)
		}
		dest := filepath.Join(f.root, "symlinkcopy")
		code, out, errOut := runMutationCopyCmd(t, f, f.root, nil, "--source", f.source, "--dest", dest)
		if code != 2 || !strings.Contains(errOut, "mutation-copy: refusing symlink 'abslink': absolute symlink") {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
		if _, err := os.Stat(dest); !os.IsNotExist(err) {
			t.Fatal("the created destination was not removed for a refused symlink")
		}

		if err := os.Remove(absLink); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("../outside-target", filepath.Join(f.source, "rellink")); err != nil {
			t.Fatal(err)
		}
		code, out, errOut = runMutationCopyCmd(t, f, f.root, nil, "--source", f.source, "--dest", filepath.Join(f.root, "symlinkcopy2"))
		if code != 2 || !strings.Contains(errOut, "mutation-copy: refusing symlink 'rellink': relative target outside the copy") {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}

		// A relative symlink whose target lands inside the copy is recreated.
		if err := os.Remove(filepath.Join(f.source, "rellink")); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(f.source, "sub"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("../tracked.txt", filepath.Join(f.source, "sub", "link.txt")); err != nil {
			t.Fatal(err)
		}
		okDest := filepath.Join(f.root, "symlinkcopy3")
		code, out, errOut = runMutationCopyCmd(t, f, f.root, nil, "--source", f.source, "--dest", okDest)
		if code != 0 {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
		info, err := os.Lstat(filepath.Join(okDest, "sub", "link.txt"))
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("the in-copy symlink was not recreated: %v %v", info, err)
		}
		if target, err := os.Readlink(filepath.Join(okDest, "sub", "link.txt")); err != nil || target != "../tracked.txt" {
			t.Fatalf("symlink target=%q %v; want the same relative target", target, err)
		}
		if content, err := os.ReadFile(filepath.Join(okDest, "sub", "link.txt")); err != nil || string(content) != "v1\n" {
			t.Fatalf("the in-copy symlink does not resolve: %q %v", content, err)
		}
	})

	t.Run("prints the exact JSON line", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlinks need elevation on Windows without developer mode")
		}
		f := newMutationCopyFixture(t)
		deps := filepath.Join(f.root, "deps")
		if err := os.MkdirAll(deps, 0o700); err != nil {
			t.Fatal(err)
		}
		dest := filepath.Join(f.root, "json")
		code, out, errOut := runMutationCopyCmd(t, f, f.root, nil, "--source", f.source, "--dest", dest, "--link", "nm="+deps)
		if code != 0 {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
		want := fmt.Sprintf(`{"copy":%q,"source":%q,"files":3,"excluded":0,"links":[{"path":"nm","target":%q}]}`+"\n", dest, f.source, deps)
		if out != want {
			t.Fatalf("exact JSON mismatch:\n got %s\nwant %s", out, want)
		}

		plain := filepath.Join(f.root, "json2")
		code, out, errOut = runMutationCopyCmd(t, f, f.root, nil, "--source", f.source, "--dest", plain)
		if code != 0 {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
		want = fmt.Sprintf(`{"copy":%q,"source":%q,"files":3,"excluded":0,"links":[]}`+"\n", plain, f.source)
		if out != want {
			t.Fatalf("exact JSON mismatch without links:\n got %s\nwant %s", out, want)
		}
	})

	t.Run("refuses a link with an empty, a root and a HOME target", func(t *testing.T) {
		f := newMutationCopyFixture(t)
		missing := filepath.Join(f.root, "must-not-exist")
		root := filepath.VolumeName(".") + string(filepath.Separator)
		code, out, errOut := runMutationCopyCmd(t, f, f.root, nil, "--source", f.source, "--dest", missing, "--link", "nm=")
		if code != 2 || !strings.Contains(errOut, "mutation-copy: refusing link 'nm='") || !strings.Contains(errOut, "target is empty") {
			t.Fatalf("empty link target: code=%d out=%q err=%q", code, out, errOut)
		}
		code, out, errOut = runMutationCopyCmd(t, f, f.root, nil, "--source", f.source, "--dest", missing, "--link", "nm="+root)
		if code != 2 || !strings.Contains(errOut, "mutation-copy: refusing link '") || !strings.Contains(errOut, "target is the filesystem root") {
			t.Fatalf("root link target: code=%d out=%q err=%q", code, out, errOut)
		}
		code, out, errOut = runMutationCopyCmd(t, f, f.root, nil, "--source", f.source, "--dest", missing, "--link", "nm="+f.home)
		if code != 2 || !strings.Contains(errOut, "mutation-copy: refusing link '") || !strings.Contains(errOut, "target is HOME or above it") {
			t.Fatalf("HOME link target: code=%d out=%q err=%q", code, out, errOut)
		}
		if _, err := os.Stat(missing); !os.IsNotExist(err) {
			t.Fatal("the created destination was not removed for a refused link target")
		}
	})

	t.Run("refuses a symlinked directory on a listed path and removes the created destination", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlinks need elevation on Windows without developer mode")
		}
		f := newMutationCopyFixture(t)
		outside := filepath.Join(f.root, "outside")
		if err := os.MkdirAll(outside, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(outside, "file.txt"), []byte("SECRET_OUTSIDE\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		// dir/file.txt is committed before dir goes into the .gitignore, and the
		// worktree's dir is then replaced by a symlink outside the repository.
		// The name-only pattern hides the symlink from the untracked listing
		// (a trailing-slash pattern does not, on some gits), so ls-files emits
		// dir/file.txt and not dir: the case the component check must cover.
		gitRepoAll(t, f, f.source, "dir/file.txt", "tracked\n")
		if err := os.WriteFile(filepath.Join(f.source, ".gitignore"), []byte("dir\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		listed := fixtureGit(t, f, f.source, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
		if !strings.Contains(listed, "dir/file.txt") {
			t.Fatalf("the fixture does not list dir/file.txt: %q", listed)
		}
		for _, name := range strings.Split(listed, "\x00") {
			if name == "dir" {
				t.Fatalf("the fixture lists the symlink dir itself; the premise changed: %q", listed)
			}
		}
		if err := os.RemoveAll(filepath.Join(f.source, "dir")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(f.source, "dir")); err != nil {
			t.Fatalf("cannot create a symlink in the fixture: %v", err)
		}
		dest := filepath.Join(f.root, "symlinkdir-abs")
		code, out, errOut := runMutationCopyCmd(t, f, f.root, nil, "--source", f.source, "--dest", dest)
		if code != 2 || !strings.Contains(errOut, "mutation-copy: refusing symlink 'dir': symlinked directory on a listed path") {
			t.Fatalf("absolute: code=%d out=%q err=%q", code, out, errOut)
		}
		if _, err := os.Stat(dest); !os.IsNotExist(err) {
			t.Fatal("the created destination was not removed for a symlinked directory on a listed path")
		}
		if content, err := os.ReadFile(filepath.Join(outside, "file.txt")); err != nil || string(content) != "SECRET_OUTSIDE\n" {
			t.Fatalf("the outside file was touched: %q %v", content, err)
		}

		// The same case with a relative symlink out of the repository.
		if err := os.RemoveAll(filepath.Join(f.source, "dir")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("../outside", filepath.Join(f.source, "dir")); err != nil {
			t.Fatalf("cannot create a symlink in the fixture: %v", err)
		}
		dest = filepath.Join(f.root, "symlinkdir-rel")
		code, out, errOut = runMutationCopyCmd(t, f, f.root, nil, "--source", f.source, "--dest", dest)
		if code != 2 || !strings.Contains(errOut, "mutation-copy: refusing symlink 'dir': symlinked directory on a listed path") {
			t.Fatalf("relative: code=%d out=%q err=%q", code, out, errOut)
		}
		if _, err := os.Stat(dest); !os.IsNotExist(err) {
			t.Fatal("the created destination was not removed for a symlinked directory on a listed path")
		}
	})

	t.Run("the default source is the worktree root and the default destination the TMPDIR temp dir", func(t *testing.T) {
		f := newMutationCopyFixture(t)
		code, out, errOut := runMutationCopyCmd(t, f, f.source, nil)
		if code != 0 {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
		got := decodedCopy(t, out)
		if got.Source != f.source {
			t.Fatalf("source=%q; want the worktree root %q", got.Source, f.source)
		}
		prefix := filepath.Join(f.tmp, "soho-mutation-")
		if !strings.HasPrefix(got.Copy, prefix) {
			t.Fatalf("copy=%q; want under %q", got.Copy, prefix)
		}
		if info, err := os.Stat(got.Copy); err != nil || !info.IsDir() {
			t.Fatalf("the created copy is missing: %v %v", info, err)
		}
	})

	t.Run("a failing guard removes the created destination and exits with the guard's code", func(t *testing.T) {
		f := newMutationCopyFixture(t)
		dest := filepath.Join(f.root, "guardcopy")
		code, out, errOut := runMutationCopyCmd(t, f, f.root, nil, "--source", f.source, "--dest", dest)
		if code != 0 {
			t.Fatalf("control run failed: code=%d out=%q err=%q", code, out, errOut)
		}
		if err := os.RemoveAll(dest); err != nil {
			t.Fatal(err)
		}
		code, out, errOut = runMutationCopyCmd(t, f, f.root, platform.Env{"CARGO_TARGET_DIR": filepath.Join(f.source, "target")}, "--source", f.source, "--dest", dest)
		if code != 1 {
			t.Fatalf("code=%d out=%q err=%q; want the guard's code 1", code, out, errOut)
		}
		if !strings.Contains(errOut, "fail build-env:") {
			t.Fatalf("the guard's failing check was not reported: %q", errOut)
		}
		if _, err := os.Stat(dest); !os.IsNotExist(err) {
			t.Fatal("the created destination was not removed for a failing guard")
		}
	})
}
