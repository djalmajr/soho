package setup

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

const untrackedNoteSuffix = " is untracked; commit it to share the hooks, or run 'setup --local' to keep them out of the repository"

func untrackedNoteLines(stdout string) []string {
	var lines []string
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(line, "note: ") && strings.HasSuffix(line, untrackedNoteSuffix) {
			lines = append(lines, line)
		}
	}
	return lines
}

func hookFilesOf(t *testing.T, root string) (settings, codex string) {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(resolved, ".claude", "settings.json"), filepath.Join(resolved, ".codex", "hooks.json")
}

func gitIn(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func TestSetupNotesUntrackedHookFilesOncePerFile(t *testing.T) {
	root, env, ctx := setupWriteFixture(t)
	code, stdout, msg := runSetupWrite(nil, ctx, env, root)
	if code != 0 {
		t.Fatalf("code=%d message=%q", code, msg)
	}
	settings, codex := hookFilesOf(t, root)
	want := []string{"note: " + settings + untrackedNoteSuffix, "note: " + codex + untrackedNoteSuffix}
	got := untrackedNoteLines(stdout)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("notes=%q want=%q\n%s", got, want, stdout)
	}
	// Each note directly follows the hooks-written line of its own file.
	for i, file := range []string{settings, codex} {
		idx := strings.Index(stdout, "hooks written: "+file+" ")
		note := want[i] + "\n"
		lineEnd := idx + strings.Index(stdout[idx:], "\n") + 1
		if idx < 0 || !strings.HasPrefix(stdout[lineEnd:], note) {
			t.Fatalf("note for %s must follow its hooks written line:\n%s", file, stdout)
		}
	}
}

func TestSetupNoUntrackedNoteForTrackedHookFiles(t *testing.T) {
	root, env, ctx := setupWriteFixture(t)
	if code, _, msg := runSetupWrite(nil, ctx, env, root); code != 0 {
		t.Fatalf("first run code=%d message=%q", code, msg)
	}
	gitIn(t, root, "add", ".claude/settings.json", ".codex/hooks.json")
	code, stdout, msg := runSetupWrite(nil, ctx, env, root)
	if code != 0 {
		t.Fatalf("second run code=%d message=%q", code, msg)
	}
	if !strings.Contains(stdout, "unchanged: ") {
		t.Fatalf("control: the hooks must be unchanged:\n%s", stdout)
	}
	if got := untrackedNoteLines(stdout); len(got) != 0 {
		t.Fatalf("tracked files must not be noted: %q", got)
	}
}

func TestSetupNoUntrackedNoteForIgnoredHookFiles(t *testing.T) {
	root, env, ctx := setupWriteFixture(t)
	exclude := filepath.Join(root, ".git", "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(exclude), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exclude, []byte("/.claude/\n/.codex/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, stdout, msg := runSetupWrite(nil, ctx, env, root)
	if code != 0 {
		t.Fatalf("code=%d message=%q", code, msg)
	}
	if !strings.Contains(stdout, "hooks written: ") {
		t.Fatalf("control: the hooks must still be written:\n%s", stdout)
	}
	if got := untrackedNoteLines(stdout); len(got) != 0 {
		t.Fatalf("ignored files must not be noted: %q", got)
	}
}

func TestSetupNoUntrackedNoteInLocalMode(t *testing.T) {
	root, env, ctx := setupWriteFixture(t)
	code, stdout, msg := runSetupWrite([]string{"--local"}, ctx, env, root)
	if code != 0 {
		t.Fatalf("code=%d message=%q", code, msg)
	}
	if !strings.Contains(stdout, "hooks written: ") {
		t.Fatalf("control: the hooks must still be written:\n%s", stdout)
	}
	if strings.Contains(stdout, "is untracked") {
		t.Fatalf("--local must not print the note:\n%s", stdout)
	}
}

func TestSetupNoUntrackedNoteOutsideGitRepository(t *testing.T) {
	_, env, _ := setupWriteFixture(t)
	plain := t.TempDir()
	ctx := core.LoadConfig(env, plain)
	code, stdout, msg := runSetupWrite(nil, &ctx, env, plain)
	if code != 0 {
		t.Fatalf("code=%d message=%q", code, msg)
	}
	if !strings.Contains(stdout, "hooks written: ") {
		t.Fatalf("control: the hooks must still be written:\n%s", stdout)
	}
	if strings.Contains(stdout, "is untracked") {
		t.Fatalf("no Git work tree must print no note:\n%s", stdout)
	}
}

func TestSetupNoUntrackedNoteInDryRunAndPlan(t *testing.T) {
	for _, flag := range []string{"--dry-run", "--plan"} {
		t.Run(flag, func(t *testing.T) {
			root, env, ctx := setupWriteFixture(t)
			code, stdout, msg := runSetupWrite([]string{flag}, ctx, env, root)
			if code != 0 {
				t.Fatalf("code=%d message=%q", code, msg)
			}
			if strings.Contains(stdout, "is untracked") {
				t.Fatalf("%s must not print the note:\n%s", flag, stdout)
			}
		})
	}
}

func TestNoteUntrackedHookFileStaysSilentWhenGitFails(t *testing.T) {
	root, env, _ := setupWriteFixture(t)
	file := filepath.Join(root, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	capture := func(env platform.Env) string {
		old := platform.Stdout
		var out bytes.Buffer
		platform.Stdout = &out
		defer func() { platform.Stdout = old }()
		noteUntrackedHookFile(file, env, false)
		return out.String()
	}
	if got := capture(env); !strings.Contains(got, file+untrackedNoteSuffix) {
		t.Fatalf("control: an untracked file must be noted, got %q", got)
	}
	noGit := env.Clone()
	noGit["PATH"] = t.TempDir()
	if got := capture(noGit); got != "" {
		t.Fatalf("a missing git must print nothing, got %q", got)
	}
}
