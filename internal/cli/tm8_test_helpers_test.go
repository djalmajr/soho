package cli

import (
	"os"
	"path/filepath"

	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func tm8CommandFixture(t *testing.T) (platform.Env, string) {
	t.Helper()
	_, repo := commandFixture(t)
	root := filepath.Dir(repo)
	bin := t.TempDir()
	for _, name := range []string{"herdr", "codex", "claude", "cursor", "grok", "gemini", "pi", "opencode"} {
		if _, err := fakecli.Install(t, bin, name, []fakecli.Rule{{AnyArgs: true, Stdout: ""}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fakecli.Install(t, bin, "git", []fakecli.Rule{
		{Argv: []string{"-C", repo, "rev-parse", "--is-inside-work-tree"}, Stdout: "true\n"},
		{Argv: []string{"-C", repo, "check-ignore", "-q", ".soho"}, Code: 1},
		{AnyArgs: true},
	}); err != nil {
		t.Fatal(err)
	}
	clean := envFrom(testutil.CleanEnv(t))
	for key := range clean {
		if strings.HasPrefix(strings.ToUpper(key), "HERDR_") || strings.HasPrefix(key, "SOHO_") || key == "PATH" {
			delete(clean, key)
		}
	}
	tmp := filepath.Join(root, "tmp")
	for _, dir := range []string{tmp, filepath.Join(root, "home"), filepath.Join(root, "conf"), filepath.Join(root, "state")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	packageDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	skillDir, err := filepath.Abs(filepath.Join(packageDir, "..", "..", "skills", "soho"))
	if err != nil {
		t.Fatal(err)
	}
	clean = withFakeCLI(clean, bin)
	clean["HOME"] = filepath.Join(root, "home")
	clean["USERPROFILE"] = clean["HOME"]
	clean["XDG_CONFIG_HOME"] = filepath.Join(root, "conf")
	clean["TMPDIR"] = tmp
	clean["SOHO_DIR"] = filepath.Join(root, "state")
	clean["SOHO_SKILL_DIR"] = skillDir
	clean["HERDR_WORKSPACE_ID"] = "ws"
	clean["HERDR_SOCKET_PATH"] = filepath.Join(root, "missing.sock")
	clean["HERDR_ENV"] = "1"
	return clean, repo
}

func tm8File(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
