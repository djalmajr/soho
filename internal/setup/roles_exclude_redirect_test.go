package setup

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestSetupRolesExcludeThroughSymlinkedInfo: with .git/info redirected to an
// outside directory, setup --roles still moves the roles but leaves the
// redirected exclude file untouched and says why.
func TestSetupRolesExcludeThroughSymlinkedInfo(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory symlinks need a privilege on Windows")
	}
	root, env, ctx := codexSetupFixture(t)
	mustWriteFile(t, filepath.Join(root, ".agents", "herdr-roles", "custom.md"), "owned role\n")
	outside, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(outside, "exclude")
	before := "# external sentinel\n.agents/herdr-roles/\n"
	mustWriteFile(t, file, before)
	info := filepath.Join(root, ".git", "info")
	if err := os.Rename(info, info+"-original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, info); err != nil {
		t.Fatal(err)
	}
	code, out, msg := runSetupWrite([]string{"--roles"}, ctx, env, root)
	if code != 0 {
		t.Fatalf("exit=%d out=%s stderr=%s", code, out, msg)
	}
	if after := mustReadFile(t, file); after != before {
		t.Fatalf("the redirected exclude was written: %q", after)
	}
	if !strings.Contains(out, "migrate: kept the exclude:") || !strings.Contains(out, "redirected through a symlink") {
		t.Fatalf("missing kept-exclude line: %s", out)
	}
	if body := mustReadFile(t, filepath.Join(root, ".agents", "soho-roles", "custom.md")); body != "owned role\n" {
		t.Fatalf("role not moved: %q", body)
	}
}
