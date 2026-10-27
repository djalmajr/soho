package dispatch

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

func TestDeclaredReferences(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo with spaces")
	body := fmt.Sprintf("Reference: %s@abcdef0:a.go,src/b.go\nReference: %s@1234567\n```\nReference: /ignored@abcdef0:x\n```\n", repo, repo)
	want := []Reference{{Repo: repo, Commit: "abcdef0", Paths: []string{"a.go", "src/b.go"}}, {Repo: repo, Commit: "1234567"}}
	if got := DeclaredReferences(body); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
	for _, raw := range []string{"relative@abcdef0", "/repo@abc", "/repo@abcdefg", "/repo@abcdef0:", "/repo@abcdef0:../escape", "/repo@abcdef0:/abs", "/repo@abcdef0:a,,b", "/repo@abcdef0:C:/escape", "/repo@abcdef0:a\\b", "/repo@abcdef0:-leading.go", "/repo@abcdef0:--option", "/repo@abcdef0:a,-b", "/repo@abcdef0:a//b"} {
		refs, bad := declaredReferences("Reference: " + raw)
		if len(refs) != 0 || len(bad) != 1 {
			t.Errorf("malformed %q: refs=%v bad=%v", raw, refs, bad)
		}
	}
}

func referenceRepo(t *testing.T) (string, string, platform.Env) {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	repo := filepath.Join(root, "reference repo")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{"PATH": filepath.Dir(git), "HOME": root, "USERPROFILE": root, "XDG_CONFIG_HOME": root, "TMPDIR": root, "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.DevNull, "GIT_AUTHOR_NAME": "Fixture", "GIT_AUTHOR_EMAIL": "fixture@example.invalid", "GIT_COMMITTER_NAME": "Fixture", "GIT_COMMITTER_EMAIL": "fixture@example.invalid"}
	run := func(input string, args ...string) string {
		c := exec.Command(git, append([]string{"-C", repo}, args...)...)
		c.Env = env.List()
		c.Stdin = strings.NewReader(input)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("", "init", "--quiet")
	blob := run("old\n", "hash-object", "-w", "--stdin")
	tree := run("100644 blob "+blob+"\ta.go\n", "mktree")
	sha := run("fixture\n", "commit-tree", tree)
	return repo, sha, env
}

func TestReferenceLintRealGit(t *testing.T) {
	repo, sha, env := referenceRepo(t)
	const full = "# Goal\nMirror.\n# Expected result\nParity.\n# Owned files\na.go\n# Forbidden\nNo commit/push.\n# Report\nParity.\n"
	for _, tc := range []struct{ name, suffix, want string }{{"good", sha + ":a.go", ""}, {"missing-commit", strings.Repeat("0", 40), "commit does not exist"}, {"missing-path", sha + ":absent.go", "path does not exist"}, {"malformed", "bad", "malformed Reference"}} {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "brief.md")
			if err := os.WriteFile(file, []byte(full+"Reference: "+repo+"@"+tc.suffix+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			// Poison all three routing variables: validation must still use -C.
			for _, key := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE"} {
				env[key] = filepath.Join(t.TempDir(), "poison")
			}
			for _, mode := range []string{"warn", "strict"} {
				env["SOHO_BRIEF_LINT"] = mode
				got := BriefLintFindings(file, &core.Config{Entries: map[string]core.ConfigEntry{}}, env, BriefLintOptions{})
				if got.Mode != mode || (tc.want == "" && got.MissingMessage != "") || (tc.want != "" && !strings.Contains(got.MissingMessage, tc.want)) {
					t.Fatalf("%s: %#v", mode, got)
				}
			}
		})
	}
}
