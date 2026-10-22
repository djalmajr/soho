package setup

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/setuptext"
)

func tm5LocalFixture(t *testing.T) (string, platform.Env, *core.Config) {
	t.Helper()
	root, env, ctx := setupWriteFixture(t)
	env["USERPROFILE"] = env["HOME"]
	env["HERDR_PANE_ID"], env["HERDR_WORKSPACE_ID"] = "w0test:p0a", "w0test"
	env["HERDR_SOCKET_PATH"] = filepath.Join(t.TempDir(), "missing-socket")
	return root, env, ctx
}

func TestSetupLocalTM5Cases(t *testing.T) {
	t.Run("// JS: \"setup --local adds the state dir entry when it is not ignored yet\"", func(t *testing.T) { // Mutation captured: omitting the state path from the exclude makes local state visible to git.
		root, env, ctx := tm5LocalFixture(t)
		env["SOHO_DIR"] = "inside-cache"
		code, _, msg := runSetupWrite([]string{"--local", "--no-hooks"}, ctx, env, root)
		data, err := os.ReadFile(excludePath(root, env))
		if code != 0 || err != nil || !strings.Contains(string(data), "/inside-cache\n") {
			t.Fatalf("code=%d msg=%q exclude=%q err=%v", code, msg, data, err)
		}
	})
	t.Run("// JS: \"setup --local refuses a tracked CLAUDE.local.md before any mutation (rc 4)\"", func(t *testing.T) { // Mutation captured: writing a tracked local instruction file destroys project-owned content.
		root, env, ctx := tm5LocalFixture(t)
		file := filepath.Join(root, localInstructionFile)
		if err := os.WriteFile(file, []byte("tracked upstream\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("git", "-C", root, "add", localInstructionFile)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git add: %v: %s", err, out)
		}
		before := setupFilesystemSnapshot(t, root)
		code, out, msg := runSetupWrite([]string{"--local", "--no-hooks"}, ctx, env, root)
		after, _ := os.ReadFile(file)
		if code != 4 || out != "" || !strings.Contains(msg, "tracked by git") || string(after) != "tracked upstream\n" || !reflect.DeepEqual(before, setupFilesystemSnapshot(t, root)) {
			t.Fatalf("code=%d out=%q msg=%q file=%q", code, out, msg, after)
		}
	})
	t.Run("canonical setup refuses an implicit switch from an existing local block", func(t *testing.T) { // DJA-462 intentionally replaces the former canonical-write contract with refusal.
		root, env, ctx := tm5LocalFixture(t)
		local := filepath.Join(root, localInstructionFile)
		if err := os.WriteFile(local, []byte(setuptext.SetupBlock()), 0o600); err != nil {
			t.Fatal(err)
		}
		code, _, msg := runSetupWrite([]string{"--no-hooks"}, ctx, env, root)
		canonical, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
		localAfter, localErr := os.ReadFile(local)
		if code != 2 || !strings.Contains(msg, "this project keeps the soho block in CLAUDE.local.md") || !os.IsNotExist(err) || localErr != nil || !bytes.Equal(localAfter, []byte(setuptext.SetupBlock())) {
			t.Fatalf("code=%d msg=%q canonical=%q local=%q errs=%v/%v", code, msg, canonical, localAfter, err, localErr)
		}
	})
	t.Run("// JS: \"setup --plan --local simulates the block, hooks and excludes without modifying the repo\"", func(t *testing.T) { // Mutation captured: a local plan must preview all local effects while leaving the checkout byte-identical.
		root, env, ctx := tm5LocalFixture(t)
		before := setupFilesystemSnapshot(t, root)
		code, out, _, msg := runPlanStreams([]string{"--local"}, ctx, env, root)
		if code != 0 || !strings.Contains(out, localInstructionFile) || !strings.Contains(out, "exclude") || !strings.Contains(out, "hooks") {
			t.Fatalf("code=%d msg=%q out=%q", code, msg, out)
		}
		if after := setupFilesystemSnapshot(t, root); !reflect.DeepEqual(after, before) {
			t.Fatalf("plan changed filesystem: before=%v after=%v", before, after)
		}
	})
	t.Run("// JS: \"setup --plan --local --panes on a healthy exclude still shows the panes and exclude diffs\"", func(t *testing.T) { // Mutation captured: omitting either diff hides an effect that a real local setup will perform.
		root, env, ctx := tm5LocalFixture(t)
		code, out, _, msg := runPlanStreams([]string{"--local", "--panes", "3"}, ctx, env, root)
		if code != 0 || !strings.Contains(out, "panes") || !strings.Contains(out, "exclude") {
			t.Fatalf("code=%d msg=%q out=%q", code, msg, out)
		}
	})
	t.Run("// JS: \"setup --local accepts a trailing-slash state dir; exact entry, unrelated files stay visible\"", func(t *testing.T) { // Mutation captured: retaining a trailing slash changes the git exclude pattern and can miss the directory.
		root, env, ctx := tm5LocalFixture(t)
		env["SOHO_DIR"] = "cache/"
		code, _, msg := runSetupWrite([]string{"--local", "--no-hooks"}, ctx, env, root)
		data, err := os.ReadFile(excludePath(root, env))
		if code != 0 || err != nil || !strings.Contains(string(data), "/cache\n") || strings.Contains(string(data), "/cache/\n") {
			t.Fatalf("code=%d msg=%q exclude=%q err=%v", code, msg, data, err)
		}
	})
	t.Run("// JS: \"setup --plan --local accepts a trailing-slash state dir; the diff shows the exact entry, nothing is written\"", func(t *testing.T) { // Mutation captured: planning must normalize the exclude entry but must not write it.
		root, env, ctx := tm5LocalFixture(t)
		env["SOHO_DIR"] = "cache/"
		before, _ := os.ReadFile(excludePath(root, env))
		code, out, _, msg := runPlanStreams([]string{"--local"}, ctx, env, root)
		after, _ := os.ReadFile(excludePath(root, env))
		if code != 0 || !strings.Contains(out, "/cache") || !bytes.Equal(before, after) {
			t.Fatalf("code=%d msg=%q out=%q before=%q after=%q", code, msg, out, before, after)
		}
	})
	t.Run("// JS: \"setup --local refuses a state dir with a trailing-whitespace segment (rc 4) before any mutation\"", func(t *testing.T) { // Mutation captured: Git strips trailing whitespace from excludes, so setup must refuse before writing.
		root, env, ctx := tm5LocalFixture(t)
		env["SOHO_DIR"] = "cache "
		before := setupFilesystemSnapshot(t, root)
		code, out, msg := runSetupWrite([]string{"--local", "--no-hooks"}, ctx, env, root)
		if code != 4 || out != "" || !strings.Contains(msg, "ends in whitespace") || !reflect.DeepEqual(before, setupFilesystemSnapshot(t, root)) {
			t.Fatalf("code=%d out=%q msg=%q", code, out, msg)
		}
	})
	t.Run("// JS: \"setup --plan --local refuses a trailing-whitespace state dir (rc 4) with no plan output\"", func(t *testing.T) { // Mutation captured: unsafe paths must fail before any preview is printed.
		root, env, ctx := tm5LocalFixture(t)
		env["SOHO_DIR"] = "cache "
		code, out, _, msg := runPlanStreams([]string{"--local"}, ctx, env, root)
		if code != 4 || out != "" || !strings.Contains(msg, "ends in whitespace") {
			t.Fatalf("code=%d out=%q msg=%q", code, out, msg)
		}
	})
	t.Run("// JS: \"setup --local accepts an interior-space state dir; real git ignores it and unrelated files stay visible\"", func(t *testing.T) { // Mutation captured: git must ignore the exact path with internal spaces, not a broader pattern.
		root, env, ctx := tm5LocalFixture(t)
		env["SOHO_DIR"] = "cache with space"
		if err := os.Mkdir(filepath.Join(root, env.Get("SOHO_DIR")), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "unrelated.txt"), []byte("visible"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, _, msg := runSetupWrite([]string{"--local", "--no-hooks"}, ctx, env, root)
		cmd := exec.Command("git", "-C", root, "check-ignore", "--", "cache with space")
		ignored, err := cmd.CombinedOutput()
		if code != 0 || err != nil || len(ignored) == 0 {
			t.Fatalf("code=%d msg=%q git-check-ignore=%q err=%v", code, msg, ignored, err)
		}
		cmd = exec.Command("git", "-C", root, "check-ignore", "--", "unrelated.txt")
		if out, err := cmd.CombinedOutput(); err == nil {
			t.Fatalf("unrelated file was ignored: %q", out)
		}
	})
	t.Run("// JS: \"setup --local accepts ./ and a/./b state dir spellings; real git ignores the resolved dir\"", func(t *testing.T) { // Mutation captured: resolving dot segments must preserve the intended root-anchored ignore target.
		for _, value := range []string{"./cache", "a/./b"} {
			t.Run(value, func(t *testing.T) {
				root, env, ctx := tm5LocalFixture(t)
				env["SOHO_DIR"] = value
				code, _, msg := runSetupWrite([]string{"--local", "--no-hooks"}, ctx, env, root)
				if code != 0 {
					t.Fatalf("code=%d msg=%q", code, msg)
				}
				rel := filepath.Clean(value)
				cmd := exec.Command("git", "-C", root, "check-ignore", "--", rel)
				if out, err := cmd.CombinedOutput(); err != nil || len(out) == 0 {
					t.Fatalf("git check-ignore %q: %q %v", rel, out, err)
				}
			})
		}
	})
	t.Run("// JS: \"setup --local with a relative external state dir adds no state entry and reports it outside the repository\"", func(t *testing.T) { // Mutation captured: an external relative state path must not create a misleading repository exclude.
		root, env, ctx := tm5LocalFixture(t)
		env["SOHO_DIR"] = "../outside-state"
		code, out, msg := runSetupWrite([]string{"--local", "--no-hooks"}, ctx, env, root)
		data, err := os.ReadFile(excludePath(root, env))
		if code != 0 || err != nil || strings.Contains(string(data), "outside-state") || !strings.Contains(strings.ToLower(out), "outside") {
			t.Fatalf("code=%d out=%q msg=%q exclude=%q err=%v", code, out, msg, data, err)
		}
	})
	t.Run("// JS: \"setup --plan --local and --dry-run accept the ./cache spelling without writing anything\"", func(t *testing.T) { // Mutation captured: dry-run and plan must normalize ./cache without touching disk.
		root, env, ctx := tm5LocalFixture(t)
		env["SOHO_DIR"] = "./cache"
		before := setupFilesystemSnapshot(t, root)
		code, out, msg := runSetupWrite([]string{"--local", "--dry-run", "--no-hooks"}, ctx, env, root)
		if code != 0 || !strings.Contains(out, "/cache") || !reflect.DeepEqual(before, setupFilesystemSnapshot(t, root)) {
			t.Fatalf("code=%d out=%q msg=%q", code, out, msg)
		}
	})
	t.Run("// JS: \"setup --local --dry-run shows the hooks would-line; --no-hooks omits it\"", func(t *testing.T) { // Mutation captured: the preview must reflect the hooks option without creating settings.json.
		root, env, ctx := tm5LocalFixture(t)
		code, out, msg := runSetupWrite([]string{"--local", "--dry-run"}, ctx, env, root)
		if code != 0 {
			t.Fatalf("hooks preview failed: code=%d out=%q msg=%q", code, out, msg)
		}
		if !strings.Contains(out, "# would merge into ") || !strings.Contains(out, "UserPromptSubmit + SessionStart hooks") {
			t.Fatalf("hooks preview missing the would-merge line: %q", out)
		}
		code, out, msg = runSetupWrite([]string{"--local", "--dry-run", "--no-hooks"}, ctx, env, root)
		if code != 0 || strings.Contains(out, "# would merge into ") {
			t.Fatalf("no-hooks preview: code=%d out=%q msg=%q", code, out, msg)
		}
		if !strings.Contains(out, "# would write to ") || !strings.Contains(out, setuptext.SetupStart) {
			t.Fatalf("no-hooks block preview missing: %q", out)
		}
		if _, err := os.Stat(filepath.Join(root, ".claude", "settings.json")); !os.IsNotExist(err) {
			t.Fatalf("preview wrote settings: %v", err)
		}
	})
	t.Run("// JS: \"setup --plan --local, --dry-run and canonical setup refuse a bad --lane kind before any output or write\"", func(t *testing.T) { // Mutation captured: lane validation must precede plan text and all setup writes.
		for _, args := range [][]string{{"--plan", "--local", "--panes", "3", "--lane", "build=unknown"}, {"--local", "--dry-run", "--panes", "3", "--lane", "build=unknown"}, {"--panes", "3", "--lane", "build=unknown"}} {
			t.Run(strings.Join(args, " "), func(t *testing.T) {
				root, env, ctx := tm5LocalFixture(t)
				before := setupFilesystemSnapshot(t, root)
				var code int
				var out, msg string
				if args[0] == "--plan" {
					code, out, _, msg = runPlanStreams(args[1:], ctx, env, root)
				} else {
					code, out, msg = runSetupWrite(args, ctx, env, root)
				}
				if code != 2 || out != "" || msg == "" || !reflect.DeepEqual(before, setupFilesystemSnapshot(t, root)) {
					t.Fatalf("code=%d out=%q msg=%q", code, out, msg)
				}
			})
		}
	})
	t.Run("// JS: \"setup --local refuses a symlinked CLAUDE.local.md (rc 4) before any mutation\"", func(t *testing.T) { // Mutation captured: setup must refuse to write through a symlinked instruction file.
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation may require privilege on Windows")
		}
		root, env, ctx := tm5LocalFixture(t)
		if err := os.Symlink("AGENTS.md", filepath.Join(root, localInstructionFile)); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		code, out, msg := runSetupWrite([]string{"--local", "--no-hooks"}, ctx, env, root)
		if code != 4 || out != "" || !strings.Contains(msg, "is a symlink") {
			t.Fatalf("code=%d out=%q msg=%q", code, out, msg)
		}
	})
	t.Run("// JS: \"gitCommonDirFor: the plain git dir, and the common dir of a linked worktree\"", func(t *testing.T) { // Mutation captured: returning the per-worktree git dir misroutes shared exclude operations.
		root, env, _ := tm5LocalFixture(t)
		plain := commonGitDir(root, env)
		if func() bool {
			a, _ := filepath.EvalSymlinks(plain)
			b, _ := filepath.EvalSymlinks(filepath.Join(root, ".git"))
			return a != b
		}() {
			t.Fatalf("plain common dir=%q", plain)
		}
		if err := os.WriteFile(filepath.Join(root, "seed.txt"), []byte("seed\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"-C", root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "add", "seed.txt"}, {"-C", root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "seed"}} {
			cmd := exec.Command("git", args...)
			cmd.Env = env.List()
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v: %s", args, err, out)
			}
		}
		linked := filepath.Join(t.TempDir(), "linked")
		git, ok := platform.FindExecutable("git", env, platform.Current())
		if !ok {
			t.Skip("git executable unavailable")
		}
		cmd := exec.Command(git, "-C", root, "worktree", "add", "--detach", linked)
		cmd.Env = env.List()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git worktree add: %v: %s", err, out)
		}
		got := commonGitDir(linked, env)
		gotReal, _ := filepath.EvalSymlinks(got)
		wantReal, _ := filepath.EvalSymlinks(filepath.Join(root, ".git"))
		if gotReal != wantReal {
			t.Fatalf("linked common dir=%q", got)
		}
	})
	t.Run("// JS: \"setup --local refuses unsafe SOHO_DIR values (rc 4) before any mutation\"", func(t *testing.T) { // Mutation captured: unsafe git-ignore syntax can broaden exclusions or escape the repository.
		for _, value := range []string{"*", "?", "[ab]", "cache "} {
			t.Run(value, func(t *testing.T) {
				root, env, ctx := tm5LocalFixture(t)
				env["SOHO_DIR"] = value
				before := setupFilesystemSnapshot(t, root)
				code, out, msg := runSetupWrite([]string{"--local", "--no-hooks"}, ctx, env, root)
				if code != 4 || out != "" || msg == "" || !reflect.DeepEqual(before, setupFilesystemSnapshot(t, root)) {
					t.Fatalf("value=%q code=%d out=%q msg=%q", value, code, out, msg)
				}
			})
		}
	})
	t.Run("// JS: \"setup --local accepts ordinary and !/#-leading state dirs; unrelated untracked files stay visible\"", func(t *testing.T) { // Mutation captured: leading Git pattern characters must be escaped without swallowing unrelated paths.
		for _, value := range []string{"cache", "!cache", "#cache"} {
			t.Run(value, func(t *testing.T) {
				root, env, ctx := tm5LocalFixture(t)
				env["SOHO_DIR"] = value
				if err := os.Mkdir(filepath.Join(root, value), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "visible.txt"), []byte("visible"), 0o600); err != nil {
					t.Fatal(err)
				}
				code, _, _ := runSetupWrite([]string{"--local", "--no-hooks"}, ctx, env, root)
				if code != 0 {
					t.Fatalf("state dir %q setup exit=%d", value, code)
				}
				check := exec.Command("git", "-C", root, "check-ignore", "--", value)
				if out, err := check.CombinedOutput(); err != nil || len(out) == 0 {
					t.Fatalf("state path not ignored: %q %v", out, err)
				}
				check = exec.Command("git", "-C", root, "check-ignore", "--", "visible.txt")
				if out, err := check.CombinedOutput(); err == nil {
					t.Fatalf("unrelated file hidden: %q", out)
				}
			})
		}
	})
	t.Run("// JS: \"setup --local and --plan both fail rc 4 when the exclude path is a directory\"", func(t *testing.T) { // Mutation captured: treating an exclude directory as a missing file falsely claims a successful write.
		for _, plan := range []bool{false, true} {
			name := "setup"
			if plan {
				name = "plan"
			}
			t.Run(name, func(t *testing.T) {
				root, env, ctx := tm5LocalFixture(t)
				exclude := excludePath(root, env)
				if err := os.Remove(exclude); err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				if err := os.Mkdir(exclude, 0o700); err != nil {
					t.Fatal(err)
				}
				var code int
				var out, msg string
				if plan {
					code, out, _, msg = runPlanStreams([]string{"--local"}, ctx, env, root)
				} else {
					code, out, msg = runSetupWrite([]string{"--local", "--no-hooks"}, ctx, env, root)
				}
				if code != 4 || out != "" || !strings.Contains(msg, "exclude") {
					t.Fatalf("plan=%t code=%d out=%q msg=%q", plan, code, out, msg)
				}
			})
		}
	})
}

func TestSetupLocalTM5BoundaryCases(t *testing.T) {
	t.Run(`// JS: "config: setup_target is a scalar key with canonical|local values only"`, func(t *testing.T) {
		for _, value := range []string{"canonical", "local"} {
			if !core.ConfigValueOk("setup_target", value, platform.Env{}, t.TempDir()) {
				t.Errorf("valid setup_target %q rejected", value)
			}
		}
		for _, value := range []string{"", "LOCAL", "both", "canonical,local"} {
			if core.ConfigValueOk("setup_target", value, platform.Env{}, t.TempDir()) {
				t.Errorf("invalid setup_target %q accepted", value)
			}
		}
	})
	t.Run(`// JS: "assertSafeLocalRels: any segment with trailing whitespace dies 4; interior spaces and trailing separators pass"`, func(t *testing.T) {
		for _, rel := range []string{"cache ", "nested/cache\t", "dir/name\n"} {
			func() {
				defer func() {
					if recover() == nil {
						t.Errorf("accepted trailing whitespace in %q", rel)
					}
				}()
				assertSafeLocalRels([]string{rel}, "setup")
			}()
		}
		for _, rel := range []string{"cache with spaces", "deep/cache/"} {
			assertSafeLocalRels([]string{rel}, "setup")
		}
	})
	t.Run(`// JS: "localRels + stateDirShown: ./, interior . and relative-external spellings resolve before the containment test"`, func(t *testing.T) {
		root, env, ctx := tm5LocalFixture(t)
		external, err := filepath.EvalSymlinks(filepath.Dir(root))
		if err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct{ input, shown string }{{"./cache", "cache/"}, {"deep/./cache", "deep/cache/"}, {"../outside", filepath.Join(external, "outside")}} {
			env["SOHO_DIR"] = tc.input
			got := classifyLocalState(root, ctx, env, root).shown
			if got != tc.shown {
				t.Errorf("state path %q shown as %q, want %q", tc.input, got, tc.shown)
			}
		}
	})
	t.Run(`// JS: "setup --local outside a git work tree writes the block but never claims the state dir is ignored"`, func(t *testing.T) {
		_, env, ctx := tm5LocalFixture(t)
		plain := t.TempDir()
		code, out, msg := runSetupWrite([]string{"--local", "--no-hooks"}, ctx, env, plain)
		block, err := os.ReadFile(filepath.Join(plain, localInstructionFile))
		if code != 0 || err != nil || !strings.Contains(string(block), setuptext.SetupStart) || strings.Contains(out, "state dir ignored") || !strings.Contains(out, "state dir not ignored") {
			t.Fatalf("code=%d out=%q msg=%q block=%q err=%v", code, out, msg, block, err)
		}
	})
	t.Run(`// JS: "stateDirShown: relative and trailing-slash overrides, external absolute, default preserved"`, func(t *testing.T) {
		root, env, ctx := tm5LocalFixture(t)
		for _, tc := range []struct{ value, want string }{{"cache", "cache/"}, {"cache/", "cache/"}, {"deep/my cache", "deep/my cache/"}, {filepath.Join(t.TempDir(), "external"), filepath.Join(t.TempDir(), "unused")}} {
			env["SOHO_DIR"] = tc.value
			want := tc.want
			if filepath.IsAbs(tc.value) {
				want = tc.value
			}
			if got := classifyLocalState(root, ctx, env, root).shown; got != want {
				t.Errorf("SOHO_DIR=%q shown=%q want=%q", tc.value, got, want)
			}
		}
		env["SOHO_DIR"] = ""
		if got := classifyLocalState(root, ctx, env, root).shown; got != ".soho/" {
			t.Fatalf("default shown=%q", got)
		}
	})
	t.Run(`// JS: "setup --local: the final message names the effective state dir, not the default (relative and external)"`, func(t *testing.T) {
		for _, tc := range []struct{ value, want string }{{"cache", "state dir ignored: cache/"}, {filepath.Join(t.TempDir(), "outside"), ""}} {
			root, env, ctx := tm5LocalFixture(t)
			env["SOHO_DIR"] = tc.value
			code, out, msg := runSetupWrite([]string{"--local", "--no-hooks"}, ctx, env, root)
			if code != 0 {
				t.Fatalf("code=%d msg=%q", code, msg)
			}
			want := tc.want
			if want == "" {
				want = "state dir outside the repository: " + tc.value + " (no repository Git exclusion is needed)"
			}
			if !strings.Contains(out, want) {
				t.Errorf("output lacks %q: %s", want, out)
			}
		}
	})
	t.Run(`// JS: "setup --local: the config follow-up advises setup --local --panes; following it leaves upstream instructions untouched"`, func(t *testing.T) {
		root, env, ctx := tm5LocalFixture(t)
		upstream := filepath.Join(root, "CLAUDE.md")
		if err := os.WriteFile(upstream, []byte("upstream rules\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, advice := runSetupWrite([]string{"--local", "--no-hooks"}, ctx, env, root)
		if !strings.Contains(advice, "then run 'setup --local --panes 2|3|4") {
			t.Fatalf("local follow-up=%q", advice)
		}
		code, _, msg := runSetupWrite([]string{"--local", "--no-hooks", "--panes", "3"}, ctx, env, root)
		got, err := os.ReadFile(upstream)
		if code != 0 || err != nil || string(got) != "upstream rules\n" {
			t.Fatalf("code=%d msg=%q upstream=%q err=%v", code, msg, got, err)
		}
	})
	t.Run(`// JS: "setup: the canonical config follow-up is byte-for-byte unchanged"`, func(t *testing.T) {
		root, env, ctx := tm5LocalFixture(t)
		_, _, advice := runSetupWrite([]string{"--target", "OTHER.md", "--no-hooks"}, ctx, env, root)
		if !strings.Contains(advice, "then run 'setup --panes 2|3|4") || strings.Contains(advice, "setup --local --panes") {
			t.Fatalf("canonical follow-up=%q", advice)
		}
	})
	t.Run(`// JS: "setup --local --dry-run refuses a missing exclude under an unwritable parent (rc 4) with no output"`, func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("POSIX directory permission case")
		}
		root, env, ctx := tm5LocalFixture(t)
		info := filepath.Join(root, ".git", "info")
		if err := os.Remove(filepath.Join(info, "exclude")); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(info, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(info, 0o700) })
		if dir, err := os.CreateTemp(info, "permission-probe-"); err == nil {
			_ = dir.Close()
			_ = os.Remove(dir.Name())
			t.Skip("host can write mode-0500 directory; permission fixture ineffective")
		}
		code, out, msg := runSetupWrite([]string{"--local", "--dry-run"}, ctx, env, root)
		if code != 4 || out != "" || msg == "" {
			t.Fatalf("code=%d out=%q msg=%q", code, out, msg)
		}
	})
	t.Run(`// JS: "setup --local fails rc 4 on an unreadable exclude file, preserving bytes and writing nothing"`, func(t *testing.T) {
		root, env, ctx := tm5LocalFixture(t)
		exclude := excludePath(root, env)
		before, err := os.ReadFile(exclude)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(exclude, 0); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(exclude, 0o600)
		if f, err := os.Open(exclude); err == nil {
			f.Close()
			t.Skip("host can read mode-000 files; unreadable-file fixture is ineffective")
		}
		code, out, msg := runSetupWrite([]string{"--local", "--no-hooks"}, ctx, env, root)
		if err := os.Chmod(exclude, 0o600); err != nil {
			t.Fatal(err)
		}
		after, readErr := os.ReadFile(exclude)
		localFile := filepath.Join(root, localInstructionFile)
		if code != 4 || out != "" || readErr != nil || !bytes.Equal(before, after) {
			t.Fatalf("code=%d out=%q msg=%q bytes=%q err=%v", code, out, msg, after, readErr)
		}
		if _, err := os.Lstat(localFile); !os.IsNotExist(err) {
			t.Fatalf("local instruction file appeared: %v", err)
		}
	})
	t.Run(`// JS: "setup --local --panes refuses an unreadable exclude (rc 4) before the panes preset lands in the tracked config"`, func(t *testing.T) {
		localExcludeFailureFixture(t, []string{"--local", "--panes", "3"}, false)
	})
	t.Run(`// JS: "setup --plan --local --panes refuses an unreadable exclude (rc 4) before showing a plan"`, func(t *testing.T) {
		localExcludeFailureFixture(t, []string{"--plan", "--local", "--panes", "3"}, true)
	})
	t.Run(`// JS: "setup --local and --plan both fail rc 4 when the exclude path is a directory"`, func(t *testing.T) {
		for _, plan := range []bool{false, true} {
			t.Run(map[bool]string{true: "plan", false: "write"}[plan], func(t *testing.T) {
				root, env, ctx := tm5LocalFixture(t)
				exclude := excludePath(root, env)
				if err := os.Remove(exclude); err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				if err := os.Mkdir(exclude, 0o700); err != nil {
					t.Fatal(err)
				}
				var code int
				var out, msg string
				if plan {
					code, out, _, msg = runPlanStreams([]string{"--local"}, ctx, env, root)
				} else {
					code, out, msg = runSetupWrite([]string{"--local", "--no-hooks"}, ctx, env, root)
				}
				if code != 4 || out != "" || !strings.Contains(msg, "exclude") {
					t.Fatalf("code=%d out=%q msg=%q", code, out, msg)
				}
			})
		}
	})
	t.Run(`// JS: "setup --local --panes refuses a missing exclude under an unwritable parent (rc 4) before the tracked config changes"`, func(t *testing.T) {
		localMissingExcludeFailureFixture(t, []string{"--local", "--panes", "3"}, false)
	})
	t.Run(`// JS: "setup --plan --local --panes refuses a missing exclude under an unwritable parent (rc 4) with no plan output"`, func(t *testing.T) {
		localMissingExcludeFailureFixture(t, []string{"--plan", "--local", "--panes", "3"}, true)
	})
	t.Run(`// JS: "control: a missing exclude under a writable parent still succeeds for plan and setup"`, func(t *testing.T) {
		for _, plan := range []bool{false, true} {
			root, env, ctx := tm5LocalFixture(t)
			exclude := excludePath(root, env)
			if err := os.Remove(exclude); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Dir(exclude)); err != nil {
				t.Fatal(err)
			}
			var code int
			var out, msg string
			if plan {
				code, out, _, msg = runPlanStreams([]string{"--local"}, ctx, env, root)
			} else {
				code, out, msg = runSetupWrite([]string{"--local", "--no-hooks"}, ctx, env, root)
			}
			if code != 0 || !strings.Contains(out, "exclude") {
				t.Fatalf("plan=%t code=%d out=%q msg=%q", plan, code, out, msg)
			}
			if _, err := os.Stat(exclude); plan && !os.IsNotExist(err) {
				t.Fatalf("plan created exclude: %v", err)
			}
			if _, err := os.Stat(exclude); !plan && err != nil {
				t.Fatalf("setup did not create exclude: %v", err)
			}
		}
	})
	t.Run(`// JS: "setup --plan --local refuses a trailing-whitespace state dir (rc 4) with no output or mutation"`, func(t *testing.T) {
		root, env, ctx := tm5LocalFixture(t)
		env["SOHO_DIR"] = "cache "
		before := setupFilesystemSnapshot(t, root)
		code, out, _, msg := runPlanStreams([]string{"--local"}, ctx, env, root)
		if code != 4 || out != "" || !strings.Contains(msg, "ends in whitespace") || !reflect.DeepEqual(before, setupFilesystemSnapshot(t, root)) {
			t.Fatalf("code=%d out=%q msg=%q", code, out, msg)
		}
	})
	t.Run(`// JS: "setup --local with a symlinked spelling of the in-repo state dir writes the root-anchored entry and says ignored"`, func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation may require privilege on Windows")
		}
		seed := func(t *testing.T, root string, env platform.Env) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("# Upstream instructions\n\nDo it the upstream way.\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("node_modules/\n.env\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			for _, args := range [][]string{{"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "upstream"}} {
				cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
				cmd.Env = env.List()
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v: %s", args, err, out)
				}
			}
		}
		linkTo := func(t *testing.T, root string) string {
			t.Helper()
			link := filepath.Join(t.TempDir(), "link")
			if err := os.Symlink(root, link); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}
			cmd := exec.Command("git", "-C", link, "rev-parse", "--show-toplevel")
			if out, err := cmd.Output(); err != nil || strings.TrimSpace(string(out)) != root {
				t.Fatalf("git through outside link resolved root=%q err=%v; want %q", strings.TrimSpace(string(out)), err, root)
			}
			return link
		}
		runCase := func(t *testing.T, stateValue, rel, shown string, makeState bool) {
			t.Helper()
			root, env, ctx := tm5LocalFixture(t)
			physicalRoot, err := filepath.EvalSymlinks(root)
			if err != nil {
				t.Fatal(err)
			}
			root = physicalRoot
			seed(t, root, env)
			link := linkTo(t, root)
			env["SOHO_DIR"] = filepath.Join(link, stateValue)
			if makeState {
				state := filepath.Join(root, stateValue)
				if err := os.MkdirAll(state, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(state, "secret.txt"), []byte("secret\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			code, out, msg := runSetupWrite([]string{"--local"}, ctx, env, link)
			if code != 0 {
				t.Fatalf("code=%d out=%q msg=%q", code, out, msg)
			}
			exclude, err := os.ReadFile(excludePath(root, env))
			if err != nil || !strings.Contains("\n"+string(exclude), "\n/"+rel+"\n") {
				t.Fatalf("exclude=%q err=%v; want exact /%s entry", exclude, err, rel)
			}
			cmd := exec.Command("git", "-C", root, "check-ignore", "-q", rel)
			cmd.Env = env.List()
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git check-ignore %q: %s %v", rel, out, err)
			}
			cmd = exec.Command("git", "-C", root, "status", "--porcelain")
			cmd.Env = env.List()
			status, err := cmd.Output()
			if err != nil || strings.Contains(string(status), rel) {
				t.Fatalf("git status still shows state dir: %q err=%v", status, err)
			}
			if !strings.Contains("\n"+out, "\nstate dir ignored: "+shown+"\n") {
				t.Fatalf("missing exact state-dir line %q in %q", "state dir ignored: "+shown, out)
			}
		}
		runCase(t, ".herdr-soho", ".herdr-soho", ".herdr-soho/", true)
		runCase(t, "newstate", "newstate", "newstate/", false)
	})
	t.Run(`// JS: "setup --local, --plan and --dry-run refuse an in-repo symlink state path (rc 4) before any mutation; an external-target link still works"`, func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation may require privilege on Windows")
		}
		for _, mode := range []string{"write", "plan", "dry-run", "external"} {
			t.Run(mode, func(t *testing.T) {
				root, env, ctx := tm5LocalFixture(t)
				target := filepath.Join(root, "inside")
				if mode == "external" {
					target = t.TempDir()
				}
				if err := os.MkdirAll(target, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, filepath.Join(root, "state-link")); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
				env["SOHO_DIR"] = filepath.Join(root, "state-link")
				before := setupFilesystemSnapshot(t, root)
				code, out, msg := 0, "", ""
				if mode == "plan" {
					code, out, _, msg = runPlanStreams([]string{"--local"}, ctx, env, root)
				} else if mode == "dry-run" {
					code, out, msg = runSetupWrite([]string{"--local", "--dry-run", "--no-hooks"}, ctx, env, root)
				} else {
					code, out, msg = runSetupWrite([]string{"--local", "--no-hooks"}, ctx, env, root)
				}
				if mode == "external" {
					if code != 0 {
						t.Fatalf("code=%d msg=%q", code, msg)
					}
					return
				}
				if code != 4 || out != "" || !strings.Contains(msg, "symlink") || !reflect.DeepEqual(before, setupFilesystemSnapshot(t, root)) {
					t.Fatalf("code=%d out=%q msg=%q", code, out, msg)
				}
			})
		}
	})
	t.Run(`// JS: "setup --local, --plan and --dry-run refuse an in-repo symlink reached through an outside alias (rc 4) before any mutation"`, func(t *testing.T) {
		localAliasedSymlinkRefusal(t, false)
	})
	t.Run(`// JS: "setup --local, --plan and --dry-run refuse an in-repo symlink reached through an alias to an interior directory (rc 4) before any mutation"`, func(t *testing.T) {
		localAliasedSymlinkRefusal(t, true)
	})
	t.Run(`// JS: "setup --local from the enclosing work tree with the state dir in a nested linked work tree uses the nested work tree root"`, func(t *testing.T) {
		root, env, ctx := tm5LocalFixture(t)
		makeGitCommit(t, root, env)
		linked := filepath.Join(t.TempDir(), "nested")
		git, _ := platform.FindExecutable("git", env, platform.Current())
		if out, err := exec.Command(git, "-C", root, "worktree", "add", "--detach", linked).CombinedOutput(); err != nil {
			t.Fatalf("worktree: %v %s", err, out)
		}
		env["SOHO_DIR"] = filepath.Join(linked, "state")
		got := classifyLocalState(root, ctx, env, root)
		gotWorktree, _ := filepath.EvalSymlinks(got.worktree)
		wantWorktree, _ := filepath.EvalSymlinks(linked)
		if got.kind != "sibling" || gotWorktree != wantWorktree || got.rel != "state" {
			t.Fatalf("classification=%+v", got)
		}
	})
	t.Run(`// JS: "setup --local from a linked work tree with the state dir in the main work tree uses the common exclude relative to that work tree"`, func(t *testing.T) {
		root, env, ctx := tm5LocalFixture(t)
		makeGitCommit(t, root, env)
		linked := filepath.Join(t.TempDir(), "linked")
		git, _ := platform.FindExecutable("git", env, platform.Current())
		if out, err := exec.Command(git, "-C", root, "worktree", "add", "--detach", linked).CombinedOutput(); err != nil {
			t.Fatalf("worktree: %v %s", err, out)
		}
		env["SOHO_DIR"] = filepath.Join(root, "state")
		code, out, msg := runSetupWrite([]string{"--local", "--no-hooks"}, ctx, env, linked)
		if code != 0 || !strings.Contains(out, "info"+string(filepath.Separator)+"exclude") {
			t.Fatalf("code=%d out=%q msg=%q", code, out, msg)
		}
	})
	t.Run(`// JS: "setup --local from a linked work tree treats a state dir in another repository as external (true external control)"`, func(t *testing.T) {
		root, env, ctx := tm5LocalFixture(t)
		makeGitCommit(t, root, env)
		linked := filepath.Join(t.TempDir(), "linked")
		git, _ := platform.FindExecutable("git", env, platform.Current())
		if out, err := exec.Command(git, "-C", root, "worktree", "add", "--detach", linked).CombinedOutput(); err != nil {
			t.Fatalf("worktree: %v %s", err, out)
		}
		other := t.TempDir()
		if out, err := exec.Command(git, "init", "-q", other).CombinedOutput(); err != nil {
			t.Fatalf("git init: %v %s", err, out)
		}
		env["SOHO_DIR"] = filepath.Join(other, "state")
		code, out, msg := runSetupWrite([]string{"--local", "--no-hooks"}, ctx, env, linked)
		data, err := os.ReadFile(excludePath(linked, env))
		if code != 0 || err != nil || strings.Contains(string(data), "state") || !strings.Contains(strings.ToLower(out), "outside") {
			t.Fatalf("code=%d out=%q msg=%q exclude=%q err=%v", code, out, msg, data, err)
		}
	})
	t.Run(`// JS: "setup --local and --plan refuse a worktree-root state dir (rc 4) before any mutation"`, func(t *testing.T) {
		for _, plan := range []bool{false, true} {
			root, env, ctx := tm5LocalFixture(t)
			env["SOHO_DIR"] = "."
			before := setupFilesystemSnapshot(t, root)
			var code int
			var out, msg string
			if plan {
				code, out, _, msg = runPlanStreams([]string{"--local"}, ctx, env, root)
			} else {
				code, out, msg = runSetupWrite([]string{"--local", "--no-hooks"}, ctx, env, root)
			}
			if code != 4 || out != "" || !strings.Contains(msg, "root of a git work tree") || !reflect.DeepEqual(before, setupFilesystemSnapshot(t, root)) {
				t.Fatalf("plan=%t code=%d out=%q msg=%q", plan, code, out, msg)
			}
		}
	})
	t.Run(`// JS: "setup --local refuses a symlinked git exclude (rc 4) before any mutation"`, func(t *testing.T) {
		localSymlinkExcludeRefusal(t, false)
	})
	t.Run(`// JS: "control: an ordinary (non-symlinked) git exclude keeps working with a tracked .gitignore present"`, func(t *testing.T) {
		root, env, ctx := tm5LocalFixture(t)
		tracked := filepath.Join(root, ".gitignore")
		if err := os.WriteFile(tracked, []byte("keep-me\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("git", "-C", root, "add", ".gitignore").CombinedOutput(); err != nil {
			t.Fatalf("git add: %v %s", err, out)
		}
		code, _, msg := runSetupWrite([]string{"--local", "--no-hooks"}, ctx, env, root)
		data, err := os.ReadFile(tracked)
		if code != 0 || err != nil || string(data) != "keep-me\n" {
			t.Fatalf("code=%d msg=%q gitignore=%q err=%v", code, msg, data, err)
		}
	})
	t.Run(`// JS: "setup --local refuses a symlinked .git/info ancestor (rc 4) with the tracked exclude byte-identical"`, func(t *testing.T) {
		localSymlinkInfoRefusal(t, false)
	})
	t.Run(`// JS: "control: a tracked root exclude file without a symlinked ancestor keeps working"`, func(t *testing.T) {
		root, env, ctx := tm5LocalFixture(t)
		exclude := excludePath(root, env)
		if err := os.WriteFile(exclude, []byte("/existing\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("git", "-C", root, "add", filepath.Join(".git", "info", "exclude")).CombinedOutput(); err != nil {
			t.Fatalf("git add exclude: %v %s", err, out)
		}
		code, _, msg := runSetupWrite([]string{"--local", "--no-hooks"}, ctx, env, root)
		if code != 0 {
			t.Fatalf("code=%d msg=%q", code, msg)
		}
	})
}

func localExcludeFailureFixture(t *testing.T, args []string, plan bool) {
	t.Helper()
	root, env, ctx := tm5LocalFixture(t)
	exclude := excludePath(root, env)
	beforeFile, err := os.ReadFile(exclude)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(exclude, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(exclude, 0o600)
	if f, err := os.Open(exclude); err == nil {
		f.Close()
		t.Skip("host can read mode-000 files; unreadable-file fixture is ineffective")
	}
	tracked := filepath.Join(root, "AGENTS.md")
	if err := os.WriteFile(tracked, []byte("upstream\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	beforeTracked, err := os.ReadFile(tracked)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", root, "add", "AGENTS.md").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v %s", err, out)
	}
	if plan {
		args = args[1:]
		code, out, _, msg := runPlanStreams(args, ctx, env, root)
		if code != 4 || out != "" || !strings.Contains(msg, "exclude") {
			t.Fatalf("code=%d out=%q msg=%q", code, out, msg)
		}
	} else {
		code, out, msg := runSetupWrite(args, ctx, env, root)
		if code != 4 || out != "" || !strings.Contains(msg, "exclude") {
			t.Fatalf("code=%d out=%q msg=%q", code, out, msg)
		}
	}
	if err := os.Chmod(exclude, 0o600); err != nil {
		t.Fatal(err)
	}
	afterExclude, err := os.ReadFile(exclude)
	if err != nil {
		t.Fatal(err)
	}
	afterTracked, err := os.ReadFile(tracked)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(beforeFile, afterExclude) || !bytes.Equal(beforeTracked, afterTracked) {
		t.Fatalf("writes occurred: exclude=%q tracked=%q", afterExclude, afterTracked)
	}
}

func localMissingExcludeFailureFixture(t *testing.T, args []string, plan bool) {
	t.Helper()
	root, env, ctx := tm5LocalFixture(t)
	exclude := excludePath(root, env)
	if err := os.Remove(exclude); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	parent := filepath.Dir(exclude)
	tracked := filepath.Join(root, "AGENTS.md")
	if err := os.WriteFile(tracked, []byte("upstream\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", root, "add", "AGENTS.md").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v %s", err, out)
	}
	beforeTracked, err := os.ReadFile(tracked)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(parent, 0o700)
	if f, err := os.CreateTemp(parent, "permission-probe"); err == nil {
		f.Close()
		os.Remove(f.Name())
		t.Skip("host can write mode-0500 directories; unwritable-parent fixture is ineffective")
	}
	if plan {
		code, out, _, msg := runPlanStreams(args[1:], ctx, env, root)
		if code != 4 || out != "" || !strings.Contains(msg, "cannot write") {
			t.Fatalf("code=%d out=%q msg=%q", code, out, msg)
		}
	} else {
		code, out, msg := runSetupWrite(args, ctx, env, root)
		if code != 4 || out != "" || !strings.Contains(msg, "cannot write") {
			t.Fatalf("code=%d out=%q msg=%q", code, out, msg)
		}
	}
	if err := os.Chmod(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	afterTracked, err := os.ReadFile(tracked)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(beforeTracked, afterTracked) {
		t.Fatalf("tracked config changed: %q", afterTracked)
	}
	for _, path := range []string{filepath.Join(root, localInstructionFile), filepath.Join(root, ".agents", "herdr-soho.conf"), exclude} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("refused operation left %s: %v", path, err)
		}
	}
}

func localAliasedSymlinkRefusal(t *testing.T, interior bool) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require privilege on Windows")
	}
	root, env, ctx := tm5LocalFixture(t)
	if err := os.Mkdir(filepath.Join(root, "inside"), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	alias := filepath.Join(outside, "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	aliasTarget := root
	linkPath := filepath.Join(root, "link")
	if interior {
		aliasTarget = filepath.Join(root, "inside")
		linkPath = filepath.Join(aliasTarget, "link")
	}
	if err := os.MkdirAll(filepath.Join(root, "cache"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "cache"), linkPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if interior {
		alias = filepath.Join(outside, "interior-alias")
		if err := os.Symlink(aliasTarget, alias); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
	}
	env["SOHO_DIR"] = filepath.Join(alias, "link", "child")
	code, out, msg := runSetupWrite([]string{"--local", "--no-hooks"}, ctx, env, root)
	if code != 4 || out != "" || !strings.Contains(msg, "symlink") {
		t.Fatalf("code=%d out=%q msg=%q", code, out, msg)
	}
}

func localSymlinkExcludeRefusal(t *testing.T, plan bool) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require privilege on Windows")
	}
	root, env, ctx := tm5LocalFixture(t)
	exclude := excludePath(root, env)
	target := filepath.Join(t.TempDir(), "exclude-target")
	if err := os.WriteFile(target, []byte("untouched\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(exclude); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, exclude); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	before, _ := os.ReadFile(target)
	code, out, msg := 0, "", ""
	if plan {
		code, out, _, msg = runPlanStreams([]string{"--local"}, ctx, env, root)
	} else {
		code, out, msg = runSetupWrite([]string{"--local", "--no-hooks"}, ctx, env, root)
	}
	after, err := os.ReadFile(target)
	if code != 4 || out != "" || !strings.Contains(msg, "symlink") || err != nil || !bytes.Equal(before, after) {
		t.Fatalf("code=%d out=%q msg=%q target=%q err=%v", code, out, msg, after, err)
	}
}

func localSymlinkInfoRefusal(t *testing.T, plan bool) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require privilege on Windows")
	}
	root, env, ctx := tm5LocalFixture(t)
	gitDir := filepath.Join(root, ".git")
	realInfo := filepath.Join(gitDir, "real-info")
	if err := os.Rename(filepath.Join(gitDir, "info"), realInfo); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realInfo, filepath.Join(gitDir, "info")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	exclude := filepath.Join(realInfo, "exclude")
	before, err := os.ReadFile(exclude)
	if err != nil {
		t.Fatal(err)
	}
	code, out, msg := 0, "", ""
	if plan {
		code, out, _, msg = runPlanStreams([]string{"--local"}, ctx, env, root)
	} else {
		code, out, msg = runSetupWrite([]string{"--local", "--no-hooks"}, ctx, env, root)
	}
	after, err := os.ReadFile(exclude)
	if code != 4 || out != "" || !strings.Contains(msg, "symlink") || err != nil || !bytes.Equal(before, after) {
		t.Fatalf("code=%d out=%q msg=%q exclude=%q err=%v", code, out, msg, after, err)
	}
}

func makeGitCommit(t *testing.T, root string, env platform.Env) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "seed.txt"), []byte("seed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"-C", root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "add", "seed.txt"}, {"-C", root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "seed"}} {
		cmd := exec.Command("git", args...)
		cmd.Env = env.List()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
}
