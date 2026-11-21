package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
)

type reviewContextFixture struct {
	repo, outside string
	env           platform.Env
}

func newReviewContextFixture(t *testing.T) reviewContextFixture {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "docs", "formal"), 0o700); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{
		"HOME": root, "USERPROFILE": root, "TMPDIR": root, "PATH": testutil.HostPath(),
		"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_SYSTEM": os.DevNull,
		"GIT_AUTHOR_NAME": "fixture", "GIT_AUTHOR_EMAIL": "f@example.invalid",
		"GIT_COMMITTER_NAME": "fixture", "GIT_COMMITTER_EMAIL": "f@example.invalid",
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = repo, env.List()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, filepath.FromSlash(path)), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q")
	git("config", "--local", "maintenance.auto", "false")
	git("config", "--local", "gc.auto", "0")
	write("code.go", "package p\n\nfunc Work() int {\n\treturn 1\n}\n")
	git("add", "-A")
	git("commit", "-qm", "base")
	write("code.go", "package p\n\nfunc Work() int {\n\treturn 2\n}\n")
	write("docs/formal/guide.md", "See code.go for Work.\n")
	git("add", "-A")
	git("commit", "-qm", "head")
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	return reviewContextFixture{repo: repo, outside: outside, env: env}
}

func TestReviewContextCommandStdoutAndCwd(t *testing.T) {
	f := newReviewContextFixture(t)
	code, out, errOut := runIn(t, []string{"review-context", "HEAD~1..HEAD", "--cwd", f.repo}, f.env, f.outside)
	if code != 0 || errOut != "" {
		t.Fatalf("code=%d err=%q out=%s", code, errOut, out)
	}
	for _, heading := range []string{"# Review context ", "## Changed files\n", "## Changed Go symbols\n", "## Direct references\n", "## Docs citing the changed files\n"} {
		if !strings.Contains(out, heading) {
			t.Errorf("missing %q in:\n%s", heading, out)
		}
	}
	if !strings.Contains(out, "- code.go:3-5 Work\n") {
		t.Errorf("changed function missing:\n%s", out)
	}
	if !strings.Contains(out, "- docs/formal/guide.md:1 code.go: See code.go for Work.\n") {
		t.Errorf("doc citation missing:\n%s", out)
	}
	nested := filepath.Join(f.repo, "docs")
	code, nestedOut, errOut := runIn(t, []string{"review-context", "HEAD~1..HEAD"}, f.env, nested)
	if code != 0 || errOut != "" || nestedOut != out {
		t.Fatalf("nested cwd without --cwd: code=%d err=%q; pack differs:\n%s", code, errOut, nestedOut)
	}
}

func TestReviewContextCommandOutRefusesExistingEntries(t *testing.T) {
	f := newReviewContextFixture(t)
	dest := filepath.Join(f.outside, "pack.md")
	code, out, errOut := runIn(t, []string{"review-context", "HEAD~1", "--cwd", f.repo, "--out", dest}, f.env, f.outside)
	if code != 0 || errOut != "" || out != "" {
		t.Fatalf("new --out: code=%d out=%q err=%q", code, out, errOut)
	}
	written, err := os.ReadFile(dest)
	if err != nil || !strings.Contains(string(written), "## Direct references\n") {
		t.Fatalf("--out file=%q err=%v", written, err)
	}

	before, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	code, out, errOut = runIn(t, []string{"review-context", "HEAD~1", "--cwd", f.repo, "--out", dest}, f.env, f.outside)
	if code != 2 || out != "" || !strings.Contains(errOut, "refusing to replace existing entry") || len(strings.Split(strings.TrimSuffix(errOut, "\n"), "\n")) != 1 {
		t.Fatalf("existing file: code=%d out=%q err=%q", code, out, errOut)
	}
	after, err := os.ReadFile(dest)
	if err != nil || string(before) != string(after) {
		t.Fatalf("existing file changed: %v", err)
	}

	dir := filepath.Join(f.outside, "directory")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	code, out, errOut = runIn(t, []string{"review-context", "HEAD~1", "--cwd", f.repo, "--out", dir}, f.env, f.outside)
	if code != 2 || out != "" || !strings.Contains(errOut, "refusing to replace existing entry") || len(strings.Split(strings.TrimSuffix(errOut, "\n"), "\n")) != 1 {
		t.Fatalf("existing directory: code=%d out=%q err=%q", code, out, errOut)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("destination directory changed: %v %v", entries, err)
	}

	link := filepath.Join(f.outside, "dangling")
	if err := os.Symlink(filepath.Join(f.outside, "missing-target"), link); err != nil {
		t.Fatal(err)
	}
	code, out, errOut = runIn(t, []string{"review-context", "HEAD~1", "--cwd", f.repo, "--out", link}, f.env, f.outside)
	if code != 2 || out != "" || !strings.Contains(errOut, "refusing to replace existing entry") || len(strings.Split(strings.TrimSuffix(errOut, "\n"), "\n")) != 1 {
		t.Fatalf("dangling link: code=%d out=%q err=%q", code, out, errOut)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("link changed: %v", err)
	}
}

func TestReviewContextCommandUsageAndNowrite(t *testing.T) {
	f := newReviewContextFixture(t)
	bad := [][]string{
		{"review-context"},
		{"review-context", "no-such-revision", "--cwd", f.repo},
		{"review-context", "HEAD~1", "--cwd"},
		{"review-context", "HEAD~1", "--unknown"},
		{"review-context", "HEAD~1.."},
		{"review-context", "HEAD~1", "--out", ""},
		{"review-context", "HEAD~1", "--cwd", ""},
	}
	for _, args := range bad {
		code, out, errOut := runIn(t, args, f.env, f.outside)
		if code != 2 || out != "" || len(strings.Split(strings.TrimSuffix(errOut, "\n"), "\n")) != 1 {
			t.Errorf("%v: code=%d out=%q err=%q", args, code, out, errOut)
		}
	}
	nowrite := platform.Env{}
	for k, v := range f.env {
		nowrite[k] = v
	}
	nowrite["SOHO_NOWRITE"] = "1"
	code, out, errOut := runIn(t, []string{"review-context", "HEAD~1..HEAD", "--cwd", f.repo}, nowrite, f.outside)
	if code != 0 || errOut != "" || !strings.Contains(out, "## Changed files\n") {
		t.Fatalf("nowrite read: code=%d out=%q err=%q", code, out, errOut)
	}
	dest := filepath.Join(f.outside, "refused.md")
	code, out, errOut = runIn(t, []string{"review-context", "HEAD~1..HEAD", "--cwd", f.repo, "--out", dest}, nowrite, f.outside)
	if code != 2 || out != "" || len(strings.Split(strings.TrimSuffix(errOut, "\n"), "\n")) != 1 {
		t.Fatalf("nowrite --out: code=%d out=%q err=%q", code, out, errOut)
	}
	if _, err := os.Lstat(dest); !os.IsNotExist(err) {
		t.Fatalf("nowrite --out created the file: %v", err)
	}
}

func TestReviewContextCommandHcAdmission(t *testing.T) {
	f := newReviewContextFixture(t)
	hc := platform.Env{}
	for k, v := range f.env {
		hc[k] = v
	}
	hc["SOHO_TRANSPORT"] = "hc"
	code, hcOut, errOut := runIn(t, []string{"review-context", "HEAD~1..HEAD", "--cwd", f.repo}, hc, f.outside)
	if code != 0 || errOut != "" || !strings.Contains(hcOut, "## Direct references\n") {
		t.Fatalf("hc read: code=%d err=%q out=%s", code, errOut, hcOut)
	}
	hc["SOHO_NOWRITE"] = "1"
	var out string
	code, out, errOut = runIn(t, []string{"review-context", "HEAD~1..HEAD", "--cwd", f.repo}, hc, f.outside)
	if code != 0 || errOut != "" || out != hcOut {
		t.Fatalf("hc nowrite read: code=%d err=%q out=%s", code, errOut, out)
	}
	dest := filepath.Join(f.outside, "hc-refused.md")
	code, out, errOut = runIn(t, []string{"review-context", "HEAD~1..HEAD", "--cwd", f.repo, "--out", dest}, hc, f.outside)
	if code != 2 || out != "" || len(strings.Split(strings.TrimSuffix(errOut, "\n"), "\n")) != 1 {
		t.Fatalf("hc nowrite --out: code=%d out=%q err=%q", code, out, errOut)
	}
	if _, err := os.Lstat(dest); !os.IsNotExist(err) {
		t.Fatalf("hc nowrite --out created the file: %v", err)
	}
}
