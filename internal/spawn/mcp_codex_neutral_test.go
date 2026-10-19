package spawn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

// mkConfig creates dir/.codex/config.toml (empty: only its existence counts).
func mkConfig(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".codex", "config.toml"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func mkDir(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A listing is neutral only when no directory at or above it holds a project
// .codex/config.toml: codex reads such a file as a project layer for any
// process below it. codex's own home (CODEX_HOME, or ~/.codex) is the user
// layer, read anyway, and does not count.
func TestCodexProjectAbove(t *testing.T) {
	root := t.TempDir()
	home := mkDir(t, filepath.Join(root, "home"))
	mkConfig(t, home) // ~/.codex/config.toml: the user layer
	project := mkDir(t, filepath.Join(home, "proj"))
	mkConfig(t, project)
	other := mkDir(t, filepath.Join(root, "other", "tmp"))
	env := platform.Env{"HOME": home, "USERPROFILE": home}

	for _, tc := range []struct {
		name string
		dir  string
		env  platform.Env
		want bool
	}{
		{"inside the project", mkDir(t, filepath.Join(project, "tmp", "soho-codex-mcp-1")), env, true},
		{"the project itself", project, env, true},
		{"under the home, outside the project: the user layer does not count", mkDir(t, filepath.Join(home, "tmp", "soho-codex-mcp-2")), env, false},
		{"outside any project", other, env, false},
		{"CODEX_HOME names the user layer", mkDir(t, filepath.Join(home, "tmp", "x")), platform.Env{"CODEX_HOME": filepath.Join(home, ".codex")}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := codexProjectAbove(tc.dir, tc.env); got != tc.want {
				t.Fatalf("codexProjectAbove(%q) = %v, want %v", tc.dir, got, tc.want)
			}
		})
	}

	t.Run("a symlink into the project resolves to the project", func(t *testing.T) {
		link := filepath.Join(root, "link")
		if err := os.Symlink(project, link); err != nil {
			t.Skipf("symlink: %v", err)
		}
		if !codexProjectAbove(mkDir(t, filepath.Join(link, "tmp2")), env) {
			t.Fatal("a path through a symlink into the project was taken as neutral")
		}
	})

	t.Run("a config.toml that is a directory is not a config file", func(t *testing.T) {
		odd := mkDir(t, filepath.Join(root, "odd"))
		mkDir(t, filepath.Join(odd, ".codex", "config.toml"))
		if codexProjectAbove(mkDir(t, filepath.Join(odd, "tmp")), env) {
			t.Fatal("a directory named config.toml counted as a project layer")
		}
	})
}

// A TMPDIR inside the worker's project would make the neutral listing see
// the project's own servers too and turn them off. The spawn takes the
// failure path instead: one warning, no overrides, and no neutral listing.
func TestCmdSpawnMcpCodexNeutralListingRefusesATempDirInsideAProject(t *testing.T) {
	f := codexMcpFixture(t)
	project := t.TempDir()
	mkConfig(t, project)
	t.Setenv("TMPDIR", mkDir(t, filepath.Join(project, "tmp")))
	installCodexMcpScript(t, f, projectListRules(codexListing("SECRET-DJA418", "a", "p1"), codexListing("SECRET-DJA418", "a", "p1"))...)
	code, _, stderr := spawnCodexNone(t, f)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	reason := "neutral listing: the temporary directory is inside a project with its own .codex/config.toml"
	if got := stdErrCount(stderr, codexMcpWarn(reason, "worker")); got != 1 {
		t.Fatalf("the exact listing-failure warning is not on stderr once: %q", stderr)
	}
	if strings.Contains(stderr, "cannot turn off MCP") {
		t.Fatalf("a refused neutral listing still warned about servers that stay on: %q", stderr)
	}
	wantTail(t, f, codexMcpBaseArgv)
	wantCodexCalls(t, f, codexListArgv)
	entries, err := os.ReadDir(filepath.Join(project, "tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("the refused temporary directory was left behind: %v", entries)
	}
}
