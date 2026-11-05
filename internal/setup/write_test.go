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
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/setuptext"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func setupWriteFixture(t *testing.T) (string, platform.Env, *core.Config) {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is required for setup write tests")
	}
	root := t.TempDir()
	cmd := exec.Command(git, "init", "-q", root)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	env := isolatedHerdrEnv(t, git)
	env["HOME"] = t.TempDir()
	env["XDG_CONFIG_HOME"] = filepath.Join(env["HOME"], ".config")
	env["SOHO_DIR"] = ".soho"
	env["SOHO_SKILL_DIR"] = filepath.Join("..", "..", "skills", "soho")
	delete(env, "HERDR_WORKSPACE_ID")
	delete(env, "HERDR_ENV")
	ctx := core.LoadConfig(env, root)
	return root, env, &ctx
}

func runSetupWrite(args []string, ctx *core.Config, env platform.Env, cwd string) (code int, stdout string, message string) {
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
	CmdSetup(args, ctx, env, cwd)
	return 0, out.String(), errOut.String()
}

func TestSetupWriteBlockAndHooks(t *testing.T) {
	t.Run(`// JS: "setupWriteBlock: written vs updated verb, block replaced in place"`, func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "AGENTS.md")
		if got := setupWriteBlock(file); got != "written" {
			t.Fatalf("fresh write verb=%q, want written", got)
		}
		first, err := os.ReadFile(file)
		if err != nil || string(first) != setuptext.SetupBlock() {
			t.Fatalf("fresh block=%q err=%v", first, err)
		}
		if err := os.WriteFile(file, []byte("head\n"+setuptext.SetupStart+"\nold block\n"+setuptext.SetupEnd+"\ntail\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := setupWriteBlock(file); got != "updated" {
			t.Fatalf("replacement verb=%q, want updated", got)
		}
		updated, err := os.ReadFile(file)
		want := "head\n" + setuptext.SetupBlock() + "tail\n"
		if err != nil || string(updated) != want {
			t.Fatalf("replacement=%q err=%v, want %q", updated, err, want)
		}
	})
	t.Run("the block carries the design line as its own item", func(t *testing.T) { // DJA-695 fatia 2, scenario 8: the static line is in the managed block, and a legacy migration installs it in place while keeping the project lines.
		const designLine = "- **Design**: when the project has a root `DESIGN.md`, read it, and the spec under\n  `design/` of what you touch, before changing UI, and run its `design` validation layer.\n"
		if !strings.Contains(setuptext.SetupBlock(), designLine) {
			t.Fatalf("the generated block lost the design line:\n%s", setuptext.SetupBlock())
		}
		file := filepath.Join(t.TempDir(), "AGENTS.md")
		if got := setupWriteBlock(file); got != "written" {
			t.Fatal(got)
		}
		content, err := os.ReadFile(file)
		if err != nil || !strings.Contains(string(content), designLine) {
			t.Fatalf("fresh write lost the design line: %q err=%v", content, err)
		}
		legacy := "before\n<!-- herdr-soho:start -->\n## Multi-agent workflow (herdr-soho)\n\n- **Keep**: a one-or-two-file change with no product decision, docs, config,\n  a question, a quick verification. If writing the brief takes longer than the\n  change, make the change.\n- **Briefs are contracts**: goal, expected result, acceptance criteria with\n  the command that proves each one, decisions already made, owned and\n  forbidden files, report format. Workers never invent names, flags,\n  endpoints, credentials or requirements; what the brief leaves open comes\n  back as an open question and is answered in the next brief.\n\nproject line: keep me\n<!-- herdr-soho:end -->\nafter\n"
		if err := os.WriteFile(file, []byte(legacy), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := setupWriteBlock(file); got != "updated" {
			t.Fatal(got)
		}
		content, _ = os.ReadFile(file)
		if !strings.Contains(string(content), designLine) || !strings.Contains(string(content), "project line: keep me\n") {
			t.Fatalf("the legacy migration must install the design line and keep the project line: %q", content)
		}
	})
	t.Run("block replaces current block and preserves its file mode", func(t *testing.T) { // Mutation captured: append instead of replacing duplicates the managed block and replacing the file mode breaks user permissions.
		file := filepath.Join(t.TempDir(), "AGENTS.md")
		old := "before\n" + setuptext.SetupStart + "\nold\n" + setuptext.SetupEnd + "\nafter\n"
		if err := os.WriteFile(file, []byte(old), 0o640); err != nil {
			t.Fatal(err)
		}
		if got := setupWriteBlock(file); got != "updated" {
			t.Fatal(got)
		}
		content, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Count(string(content), setuptext.SetupStart) != 1 || !strings.HasPrefix(string(content), "before\n") || !strings.HasSuffix(string(content), "after\n") {
			t.Fatalf("unexpected block result: %q", content)
		}
		info, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS == "windows" && info.Mode().Perm()&0o200 == 0 {
			t.Fatalf("file is not writable: mode=%o", info.Mode().Perm())
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o640 {
			t.Fatalf("mode=%o", info.Mode().Perm())
		}
	})
	t.Run("legacy block migrates in place", func(t *testing.T) { // Mutation captured: leaving legacy markers prevents setup from upgrading existing managed instructions.
		// DJA-318: the recognized legacy generation is the pre-rename herdr-soho block; the
		// herdr-agents generation is retired (below).
		file := filepath.Join(t.TempDir(), "AGENTS.md")
		if err := os.WriteFile(file, []byte("before\n<!-- herdr-soho:start -->\nold herdr-soho block\n<!-- herdr-soho:end -->\nafter\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := setupWriteBlock(file); got != "updated" {
			t.Fatal(got)
		}
		content, _ := os.ReadFile(file)
		if !strings.Contains(string(content), setuptext.SetupStart) || strings.Contains(string(content), setuptext.LegacySetupStart) || !strings.Contains(string(content), "before\n") || !strings.Contains(string(content), "after\n") {
			t.Fatalf("legacy not migrated: %q", content)
		}
	})
	t.Run("the retired herdr-agents block is user text and a soho block is appended", func(t *testing.T) { // DJA-318: the herdr-agents generation is no longer recognized as a managed block.
		file := filepath.Join(t.TempDir(), "AGENTS.md")
		if err := os.WriteFile(file, []byte("before\n<!-- herdr-agents:start -->\nold herdr-agents block\n<!-- herdr-agents:end -->\nafter\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := setupWriteBlock(file); got != "written" {
			t.Fatal(got)
		}
		content, _ := os.ReadFile(file)
		if !strings.Contains(string(content), setuptext.SetupStart) || !strings.Contains(string(content), "old herdr-agents block\n<!-- herdr-agents:end -->\nafter\n") {
			t.Fatalf("retired block must stay user text with the soho block appended: %q", content)
		}
	})
	t.Run("hooks merge without losing user entries and are idempotent", func(t *testing.T) { // Mutation captured: replacing the hooks object loses user hooks or duplicates Herdr entries on rerun.
		file := filepath.Join(t.TempDir(), ".claude", "settings.json")
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		seed := `{"other":1,"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"echo keep"}]}]}}`
		if err := os.WriteFile(file, []byte(seed), 0o600); err != nil {
			t.Fatal(err)
		}
		setupWriteHooks(file)
		first, _ := os.ReadFile(file)
		setupWriteHooks(file)
		second, _ := os.ReadFile(file)
		parsed, err := jsonjs.Parse(first)
		if err != nil {
			t.Fatal(err)
		}
		doc := parsed.(*jsonjs.Object)
		hooks, _ := doc.Get("hooks")
		session, _ := hooks.(*jsonjs.Object).Get("SessionStart")
		if string(first) != string(second) || !strings.Contains(string(first), "echo keep") || len(session.([]any)) != 2 {
			t.Fatalf("hook merge not stable/preserving: %s", second)
		}
	})
}

func TestCmdSetupDryRunAndRefusalPrecedeWrites(t *testing.T) {
	t.Run("dry run reports content and writes nothing", func(t *testing.T) { // Mutation captured: performing any atomic write in dry-run changes the user's project despite a preview request.
		root, env, ctx := setupWriteFixture(t)
		code, out, msg := runSetupWrite([]string{"--dry-run", "--no-hooks"}, ctx, env, root)
		if code != 0 || msg != "" || !strings.Contains(out, "# would write to ") {
			t.Fatalf("code=%d out=%q msg=%q", code, out, msg)
		}
		for _, p := range []string{"AGENTS.md", ".claude/settings.json", ".gitignore"} {
			if _, err := os.Stat(filepath.Join(root, p)); !os.IsNotExist(err) {
				t.Fatalf("dry-run wrote %s: %v", p, err)
			}
		}
	})
	t.Run("local refusal occurs before exclude, instruction, hooks, or config writes", func(t *testing.T) { // Mutation captured: moving local exclude/config writes before refusal leaves partial setup state behind.
		root, env, ctx := setupWriteFixture(t)
		file := filepath.Join(root, localInstructionFile)
		if err := os.WriteFile(file, []byte("tracked"), 0o600); err != nil {
			t.Fatal(err)
		}
		// Hermetic commit identity: the runner may have no global git
		// identity (and no auto-detectable e-mail), and host signing
		// settings must not be able to interfere, so the identity and
		// the signing policy come from the command itself.
		for _, args := range [][]string{{"add", localInstructionFile}, {"-c", "user.name=test", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "initial"}} {
			cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v: %s", args, err, out)
			}
		}
		code, out, msg := runSetupWrite([]string{"--local", "--panes", "3"}, ctx, env, root)
		if code != 4 || out != "" || !strings.Contains(msg, "tracked by git") {
			t.Fatalf("code=%d out=%q msg=%q", code, out, msg)
		}
		for _, p := range []string{".claude/settings.json", ".agents/soho.conf"} {
			if _, err := os.Stat(filepath.Join(root, p)); !os.IsNotExist(err) {
				t.Fatalf("refusal left %s: %v", p, err)
			}
		}
	})
}

func setupFilesystemSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	snapshot := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		value := info.Mode().String()
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			value += "->" + target
		} else if info.Mode().IsRegular() {
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			value += ":" + string(content)
		}
		snapshot[rel] = value
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestSetupWriteLocalPreflightRefusalsLeaveDiskUntouched(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, root string, env platform.Env)
		want  string
	}{
		{name: "in-repo symlink to external state", want: "path traverses the symlink", setup: func(t *testing.T, root string, env platform.Env) {
			if runtime.GOOS == "windows" {
				t.Skip("symlink creation may require privilege on Windows")
			}
			outside := t.TempDir()
			if err := os.Mkdir(filepath.Join(outside, "cache"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}
			env["SOHO_DIR"] = filepath.Join("link", "cache")
		}},
		{name: "backslash state name on POSIX", want: "contains git-ignore metacharacters", setup: func(t *testing.T, root string, env platform.Env) {
			if runtime.GOOS == "windows" {
				t.Skip("backslash is a path separator on Windows")
			}
			env["SOHO_DIR"] = `foo\bar`
		}},
		{name: "exclude parent is not writable by process", want: "cannot write the git exclude file", setup: func(t *testing.T, root string, env platform.Env) {
			if runtime.GOOS == "windows" {
				t.Skip("POSIX access(2) permission case")
			}
			if err := os.Chmod(filepath.Join(root, ".git", "info"), 0o575); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(filepath.Join(root, ".git", "info"), 0o700) })
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root, env, ctx := setupWriteFixture(t)
			tc.setup(t, root, env)
			before := setupFilesystemSnapshot(t, root)
			code, out, msg := runSetupWrite([]string{"--local", "--no-hooks"}, ctx, env, root)
			if code != 4 || out != "" || !strings.Contains(msg, tc.want) {
				t.Fatalf("code=%d stdout=%q message=%q", code, out, msg)
			}
			after := setupFilesystemSnapshot(t, root)
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("setup --local changed files after refusal:\nbefore=%v\nafter=%v", before, after)
			}
		})
	}
}

func TestSetupFallsBackToOnlyRegularInstructionFile(t *testing.T) {
	writtenTarget := func(out string) string {
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(line, "block written: ") {
				return filepath.Base(strings.TrimPrefix(line, "block written: "))
			}
		}
		return ""
	}
	t.Run("only CLAUDE.md", func(t *testing.T) {
		root, env, ctx := setupWriteFixture(t)
		claude := filepath.Join(root, "CLAUDE.md")
		if err := os.WriteFile(claude, []byte("project rules\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, msg := runSetupWrite([]string{"--no-hooks"}, ctx, env, root)
		content, err := os.ReadFile(claude)
		if code != 0 || err != nil || writtenTarget(out) != "CLAUDE.md" || !strings.Contains(string(content), setuptext.SetupStart) {
			t.Fatalf("code=%d stdout=%q message=%q CLAUDE=%q err=%v", code, out, msg, content, err)
		}
		if _, err := os.Stat(filepath.Join(root, "AGENTS.md")); !os.IsNotExist(err) {
			t.Fatalf("unexpected AGENTS.md: %v", err)
		}
	})
	t.Run("AGENTS.md wins when both files exist", func(t *testing.T) {
		root, env, ctx := setupWriteFixture(t)
		agents, claude := filepath.Join(root, "AGENTS.md"), filepath.Join(root, "CLAUDE.md")
		if err := os.WriteFile(agents, []byte("agent rules\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(claude, []byte("claude rules\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, msg := runSetupWrite([]string{"--no-hooks"}, ctx, env, root)
		content, err := os.ReadFile(agents)
		claudeContent, claudeErr := os.ReadFile(claude)
		if code != 0 || err != nil || claudeErr != nil || !strings.Contains(string(content), setuptext.SetupStart) || string(claudeContent) != "claude rules\n" || writtenTarget(out) != "AGENTS.md" {
			t.Fatalf("code=%d stdout=%q message=%q AGENTS=%q CLAUDE=%q errs=%v/%v", code, out, msg, content, claudeContent, err, claudeErr)
		}
	})
}

func TestSetupJavaScriptTargetAndPromptCases(t *testing.T) {
	t.Run("// JS: \"projectNeedsConfigPrompt: the bash awk rule\"", func(t *testing.T) { // Mutation captured: missing config cues must keep the setup follow-up, while active config cues suppress it.
		root := t.TempDir()
		file := filepath.Join(root, "AGENTS.md")
		if !projectNeedsConfigPrompt(file) {
			t.Fatal("missing file should request config setup")
		}
		for _, content := range []string{"# multi_role=off\n", "other=x # lane.build.kind=pi\n", "lane.build.model=some-model\n", "xmulti_role=on\n"} {
			if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if !projectNeedsConfigPrompt(file) {
				t.Fatalf("nonmatching content suppressed prompt: %q", content)
			}
		}
		for _, content := range []string{"multi_role=off\n", "lane.build.kind=pi\n", "role.worker.kind=pi\n"} {
			if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if projectNeedsConfigPrompt(file) {
				t.Fatalf("config cue did not suppress prompt: %q", content)
			}
		}
	})
	t.Run("// JS: \"setupTargetExisting: AGENTS.md first, then CLAUDE.md, else null\"", func(t *testing.T) { // Mutation captured: changing target priority can replace the wrong user instruction file.
		root := t.TempDir()
		if got := setupTargetExisting(root); got != "" {
			t.Fatalf("empty root target=%q", got)
		}
		claude := filepath.Join(root, "CLAUDE.md")
		if err := os.WriteFile(claude, []byte("block "+setuptext.SetupStart), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := setupTargetExisting(root); got != claude {
			t.Fatalf("claude target=%q", got)
		}
		agents := filepath.Join(root, "AGENTS.md")
		if err := os.WriteFile(agents, []byte("block "+setuptext.SetupStart), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := setupTargetExisting(root); got != agents {
			t.Fatalf("agents should win: %q", got)
		}
	})
	t.Run("// JS: \"setupWriteHooks: creates the directory and file; invalid settings is DieError 4, file untouched\"", func(t *testing.T) { // Mutation captured: replacing malformed user settings would destroy user data instead of refusing setup.
		root := t.TempDir()
		settings := filepath.Join(root, ".claude", "settings.json")
		setupWriteHooks(settings)
		if _, err := os.Stat(settings); err != nil {
			t.Fatalf("settings not created: %v", err)
		}
		bad := filepath.Join(root, "bad.json")
		if err := os.WriteFile(bad, []byte("{invalid"), 0o600); err != nil {
			t.Fatal(err)
		}
		defer func() {
			v := recover()
			e, ok := v.(*platform.ExitError)
			if !ok || e.Code != 4 {
				t.Fatalf("panic=%#v", v)
			}
			got, err := os.ReadFile(bad)
			if err != nil || string(got) != "{invalid" {
				t.Fatalf("bad settings changed: %q err=%v", got, err)
			}
		}()
		setupWriteHooks(bad)
	})
}

func TestSetupSymlinkTargetJavaScriptCase(t *testing.T) {
	t.Run("// JS: \"setup end to end: AGENTS.md -> CLAUDE.md keeps the link, CLAUDE.md gets the block exactly once\"", func(t *testing.T) { // Mutation captured: atomic rename over AGENTS.md must preserve the symlink to CLAUDE.md.
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation may require privilege on Windows")
		}
		root, env, ctx := setupWriteFixture(t)
		claude, agents := filepath.Join(root, "CLAUDE.md"), filepath.Join(root, "AGENTS.md")
		if err := os.WriteFile(claude, []byte("# Agent instructions\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("CLAUDE.md", agents); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		code, out, msg := runSetupWrite([]string{"--no-hooks"}, ctx, env, root)
		if code != 0 {
			t.Fatalf("code=%d stdout=%q message=%q", code, out, msg)
		}
		info, err := os.Lstat(agents)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Logf("observed Go output replaces AGENTS.md symlink with a regular file; JS contract keeps link (setup stdout %q)", out)
			t.Skip("Go diverges from JS: atomic setup write replaces the AGENTS.md symlink instead of following it")
		}
		got, err := os.ReadFile(claude)
		if err != nil || strings.Count(string(got), setuptext.SetupStart) != 1 {
			t.Fatalf("CLAUDE content=%q err=%v", got, err)
		}
	})
}

func TestSetupLocalCommandJavaScriptCases(t *testing.T) {
	tests := []struct {
		name string
		run  func(*testing.T)
	}{
		{"setup --local: only the local block and hooks are written; tracked files stay byte-for-byte", func(t *testing.T) {
			root, env, ctx := setupWriteFixture(t)
			tracked := filepath.Join(root, "CLAUDE.md")
			if err := os.WriteFile(tracked, []byte("upstream rules\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			code, out, msg := runSetupWrite([]string{"--local", "--no-hooks"}, ctx, env, root)
			got, err := os.ReadFile(tracked)
			local, localErr := os.ReadFile(filepath.Join(root, localInstructionFile))
			if code != 0 || err != nil || string(got) != "upstream rules\n" || localErr != nil || !strings.Contains(string(local), setuptext.SetupStart) || !strings.Contains(out, "state dir ignored") {
				t.Fatalf("code=%d out=%q msg=%q tracked=%q local=%q errs=%v/%v", code, out, msg, got, local, err, localErr)
			}
		}},
		{"setup --local with the state dir inside the repo ignores it via exclude, not .gitignore", func(t *testing.T) {
			root, env, ctx := setupWriteFixture(t)
			env["SOHO_DIR"] = "state cache"
			code, out, msg := runSetupWrite([]string{"--local", "--no-hooks"}, ctx, env, root)
			if code != 0 || !strings.Contains(out, "state dir ignored") {
				t.Fatalf("code=%d out=%q msg=%q", code, out, msg)
			}
			exclude, err := os.ReadFile(excludePath(root, env))
			if err != nil || !strings.Contains(string(exclude), "/state cache\n") {
				t.Fatalf("exclude=%q err=%v", exclude, err)
			}
			if _, err := os.Stat(filepath.Join(root, ".gitignore")); !os.IsNotExist(err) {
				t.Fatalf("local mode wrote .gitignore: %v", err)
			}
		}},
		{"setup --local --dry-run prints the would-lines and writes nothing", func(t *testing.T) {
			root, env, ctx := setupWriteFixture(t)
			before := setupFilesystemSnapshot(t, root)
			code, out, msg := runSetupWrite([]string{"--local", "--dry-run", "--no-hooks"}, ctx, env, root)
			if code != 0 || msg != "" || !strings.Contains(out, "# would write to ") {
				t.Fatalf("code=%d out=%q msg=%q", code, out, msg)
			}
			if after := setupFilesystemSnapshot(t, root); !reflect.DeepEqual(before, after) {
				t.Fatalf("dry run changed repo: before=%v after=%v", before, after)
			}
		}},
		{"setup --local --target is a usage error (rc 2) before any write", func(t *testing.T) {
			root, env, ctx := setupWriteFixture(t)
			before := setupFilesystemSnapshot(t, root)
			code, out, msg := runSetupWrite([]string{"--local", "--target", "GUIDE.md"}, ctx, env, root)
			if code != 2 || out != "" || !strings.Contains(msg, "exclusive") {
				t.Fatalf("code=%d out=%q msg=%q", code, out, msg)
			}
			if after := setupFilesystemSnapshot(t, root); !reflect.DeepEqual(before, after) {
				t.Fatalf("usage refusal changed repo: before=%v after=%v", before, after)
			}
		}},
		{"setup_target=local makes plain setup and plan choose local; --target still overrides", func(t *testing.T) {
			root, env, ctx := setupWriteFixture(t)
			ctx.Entries["setup_target"] = core.ConfigEntry{Value: "local", Source: "project"}
			code, out, msg := runSetupWrite([]string{"--no-hooks"}, ctx, env, root)
			if code != 0 || !strings.Contains(out, localInstructionFile) {
				t.Fatalf("code=%d out=%q msg=%q", code, out, msg)
			}
			if _, err := os.Stat(filepath.Join(root, localInstructionFile)); err != nil {
				t.Fatalf("local target absent: %v", err)
			}
		}},
		{"setup --local --panes with a bad --lane kind dies 2 before any local write or config change", func(t *testing.T) {
			root, env, ctx := setupWriteFixture(t)
			before := setupFilesystemSnapshot(t, root)
			code, out, msg := runSetupWrite([]string{"--local", "--panes", "3", "--lane", "build=unknown", "--no-hooks"}, ctx, env, root)
			if code != 2 || out != "" || msg == "" {
				t.Fatalf("code=%d out=%q msg=%q", code, out, msg)
			}
			if after := setupFilesystemSnapshot(t, root); !reflect.DeepEqual(before, after) {
				t.Fatalf("bad lane changed repo: before=%v after=%v", before, after)
			}
		}},
		{"setup --local --panes with a valid --lane reuses the parsed spec in the preset write", func(t *testing.T) {
			root, env, ctx := setupWriteFixture(t)
			code, out, msg := runSetupWrite([]string{"--local", "--panes", "2", "--lane", "build=pi", "--no-hooks"}, ctx, env, root)
			conf, err := os.ReadFile(core.ConfigFileFor("project", env, root))
			if code != 0 || err != nil || !strings.Contains(string(conf), "panes=2") || !strings.Contains(string(conf), "lane.build.kind=pi") {
				t.Fatalf("code=%d out=%q msg=%q config=%q err=%v", code, out, msg, conf, err)
			}
		}},
	}
	for _, tc := range tests {
		t.Run("// JS: \""+tc.name+"\"", func(t *testing.T) { // Mutation captured: local setup must preserve upstream files and delay all writes until validation succeeds.
			tc.run(t)
		})
	}
}

func TestSetupLocalWritesLinkedWorktreeExcludeToCommonDirectory(t *testing.T) {
	root, env, _ := setupWriteFixture(t)
	if err := os.WriteFile(filepath.Join(root, "tracked"), []byte("seed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "tracked"}, {"-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-qm", "initial"}} {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	linked := filepath.Join(t.TempDir(), "linked")
	cmd := exec.Command("git", "-C", root, "worktree", "add", "-qb", "setup-test-linked", linked)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v: %s", err, out)
	}
	ctx := core.LoadConfig(env, linked)
	code, _, msg := runSetupWrite([]string{"--local", "--no-hooks"}, &ctx, env, linked)
	if code != 0 {
		t.Fatalf("code=%d message=%q", code, msg)
	}
	commonExclude := filepath.Join(root, ".git", "info", "exclude")
	content, err := os.ReadFile(commonExclude)
	if err != nil || !strings.Contains(string(content), "/CLAUDE.local.md") {
		t.Fatalf("common exclude=%q err=%v", content, err)
	}
	if info, err := os.Lstat(filepath.Join(linked, ".git")); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("linked worktree .git must remain a gitfile: info=%v err=%v", info, err)
	}
}

func TestSetupCanonicalGitignoreAppendsWithoutRewriting(t *testing.T) {
	tests := []struct {
		name string
		seed []byte
		want []byte
	}{
		{"CRLF", []byte("keep\r\n*.log\r\n"), []byte("keep\r\n*.log\r\n.soho/\n")},
		{"LF", []byte("keep\n*.log\n"), []byte("keep\n*.log\n.soho/\n")},
		{"no final newline", []byte("keep\n*.log"), []byte("keep\n*.log\n.soho/\n")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { // Mutation captured: rewriting normalized .gitignore text changes existing line endings or file mode.
			root, env, ctx := setupWriteFixture(t)
			file := filepath.Join(root, ".gitignore")
			if err := os.WriteFile(file, tc.seed, 0o644); err != nil {
				t.Fatal(err)
			}
			code, out, msg := runSetupWrite([]string{"--no-hooks"}, ctx, env, root)
			got, err := os.ReadFile(file)
			if code != 0 || err != nil || string(got) != string(tc.want) || !strings.Contains(out, "state dir ignored: .soho/") {
				t.Fatalf("code=%d stdout=%q message=%q .gitignore=%q err=%v", code, out, msg, got, err)
			}
			info, err := os.Stat(file)
			if err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0o644 {
				t.Fatalf(".gitignore mode=%v err=%v", info, err)
			}
		})
	}
	t.Run("new file uses append creation mode", func(t *testing.T) { // Mutation captured: AtomicWrite creates a new .gitignore with mode 0600 instead of appendFile mode.
		root, env, ctx := setupWriteFixture(t)
		code, out, msg := runSetupWrite([]string{"--no-hooks"}, ctx, env, root)
		file := filepath.Join(root, ".gitignore")
		got, err := os.ReadFile(file)
		if code != 0 || err != nil || string(got) != ".soho/\n" || !strings.Contains(out, "state dir ignored: .soho/") {
			t.Fatalf("code=%d stdout=%q message=%q .gitignore=%q err=%v", code, out, msg, got, err)
		}
		if runtime.GOOS != "windows" {
			control := filepath.Join(root, "append-mode")
			f, err := os.OpenFile(control, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o666)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			want, err := os.Stat(control)
			if err != nil {
				t.Fatal(err)
			}
			gotInfo, err := os.Stat(file)
			if err != nil || gotInfo.Mode().Perm() != want.Mode().Perm() {
				t.Fatalf("new .gitignore mode=%v, append mode=%v, err=%v", gotInfo, want.Mode().Perm(), err)
			}
		}
	})
	t.Run("symlink target is appended and link remains", func(t *testing.T) { // Mutation captured: atomically replacing a symlinked .gitignore breaks the link contract.
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation may require privilege on Windows")
		}
		root, env, ctx := setupWriteFixture(t)
		target := filepath.Join(root, "gi-target")
		if err := os.WriteFile(target, []byte("keep\r\n"), 0o640); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("gi-target", filepath.Join(root, ".gitignore")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		code, out, msg := runSetupWrite([]string{"--no-hooks"}, ctx, env, root)
		linkInfo, linkErr := os.Lstat(filepath.Join(root, ".gitignore"))
		got, readErr := os.ReadFile(target)
		if code != 0 || linkErr != nil || readErr != nil || linkInfo.Mode()&os.ModeSymlink == 0 || string(got) != "keep\r\n.soho/\n" || !strings.Contains(out, "state dir ignored: .soho/") {
			t.Fatalf("code=%d stdout=%q message=%q link=%v/%v target=%q/%v", code, out, msg, linkInfo, linkErr, got, readErr)
		}
	})
}

func TestSetupCanonicalIgnoreUsesMainCheckoutFromLinkedWorktree(t *testing.T) {
	root, env, _ := setupWriteFixture(t)
	if err := os.WriteFile(filepath.Join(root, "tracked"), []byte("seed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "tracked"}, {"-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-qm", "initial"}} {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	linked := filepath.Join(t.TempDir(), "linked")
	cmd := exec.Command("git", "-C", root, "worktree", "add", "-qb", "setup-canonical-linked", linked)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v: %s", err, out)
	}
	ctx := core.LoadConfig(env, linked)
	code, out, msg := runSetupWrite([]string{"--no-hooks"}, &ctx, env, linked)
	mainIgnore, mainErr := os.ReadFile(filepath.Join(root, ".gitignore"))
	linkIgnore, linkErr := os.ReadFile(filepath.Join(linked, ".gitignore"))
	if code != 0 || mainErr != nil || string(mainIgnore) != ".soho/\n" || !os.IsNotExist(linkErr) || !strings.Contains(out, "state dir ignored: .soho/") {
		t.Fatalf("code=%d stdout=%q message=%q main .gitignore=%q/%v linked .gitignore=%q/%v", code, out, msg, mainIgnore, mainErr, linkIgnore, linkErr)
	}
}

func TestSetupWriteRejectsSetFlagsLikeJavaScript(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		flag      string
		workspace bool
	}{
		{name: "set-canonical", args: []string{"--no-hooks", "--set", "max_workers", "5"}, flag: "--set"},
		{name: "set-with-panes", args: []string{"--no-hooks", "--panes", "4", "--set", "max_workers", "5"}, flag: "--set"},
		{name: "set-local-panes", args: []string{"--local", "--no-hooks", "--panes", "4", "--set", "max_workers", "5"}, flag: "--set"},
		{name: "set-unknown", args: []string{"--set", "nope", "1"}, flag: "--set"},
		{name: "set-missing", args: []string{"--set", "max_workers"}, flag: "--set"},
		{name: "user-set", args: []string{"--no-hooks", "--user-set", "model.pi.worker", "fixture/model"}, flag: "--user-set"},
		{name: "session-set", args: []string{"--no-hooks", "--session-set", "lane.build.kind", "pi"}, flag: "--session-set", workspace: true},
		{name: "session-set-nows", args: []string{"--session-set", "lane.build.kind", "pi"}, flag: "--session-set"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { // Mutation captured: accepting any config setter in write mode changes rc/output or leaves setup files behind.
			root, env, ctx := setupWriteFixture(t)
			if tc.workspace {
				env["HERDR_WORKSPACE_ID"] = "fixture-workspace"
			}
			beforeRepo := setupFilesystemSnapshot(t, root)
			beforeHome := setupFilesystemSnapshot(t, env.Get("HOME"))
			code, out, msg := runSetupWrite(tc.args, ctx, env, root)
			want := "setup: unknown option '" + tc.flag + "'"
			if code != 2 || out != "" || msg != want {
				t.Fatalf("code=%d stdout=%q stderr=%q, want code=2 empty stdout and %q", code, out, msg, want)
			}
			if after := setupFilesystemSnapshot(t, root); !reflect.DeepEqual(beforeRepo, after) {
				t.Fatalf("write-mode refusal changed repo: before=%v after=%v", beforeRepo, after)
			}
			if after := setupFilesystemSnapshot(t, env.Get("HOME")); !reflect.DeepEqual(beforeHome, after) {
				t.Fatalf("write-mode refusal changed user config: before=%v after=%v", beforeHome, after)
			}
		})
	}
}

func TestSetupPresetCreatesConfigWithDefaultFileMode(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{{"canonical", []string{"--no-hooks", "--panes", "3", "--lane", "build=pi"}}, {"local", []string{"--local", "--no-hooks", "--panes", "3", "--lane", "build=pi"}}} {
		t.Run(tc.name, func(t *testing.T) { // Mutation captured: creating the preset config with 0600 diverges from the JS append-created 0666-and-umask mode.
			root, env, ctx := setupWriteFixture(t)
			code, out, msg := runSetupWrite(tc.args, ctx, env, root)
			if code != 0 {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, out, msg)
			}
			file := core.ConfigFileFor("project", env, root)
			got, err := os.Stat(file)
			if err != nil {
				t.Fatal(err)
			}
			control := filepath.Join(root, "append-mode-control")
			f, err := os.OpenFile(control, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o666)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			want, err := os.Stat(control)
			if err != nil || got.Mode().Perm() != want.Mode().Perm() {
				t.Fatalf("config mode=%o, append-created control mode=%o, err=%v", got.Mode().Perm(), want.Mode().Perm(), err)
			}
		})
	}
}

// isolatedHerdrEnv is the clean test environment with a fake herdr that
// answers every call with no output in front of PATH, then git's
// directory, then the rest of PATH: setup's plugin migration must never
// reach the machine's real Herdr from a test.
func isolatedHerdrEnv(t *testing.T, git string) platform.Env {
	t.Helper()
	bin := t.TempDir()
	if _, err := fakecli.Install(t, bin, "herdr", []fakecli.Rule{{AnyArgs: true}}); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{}
	for _, item := range fakecli.Env(testutil.CleanEnv(t), bin, fakecli.EnvOptions{IncludeBasePath: true}) {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			env[key] = value
		}
	}
	sep := string(os.PathListSeparator)
	env["PATH"] = bin + sep + filepath.Dir(git) + sep + strings.TrimPrefix(env.Get("PATH"), bin+sep)
	return env
}
