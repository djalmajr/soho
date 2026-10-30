package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestReopenTrustCwd(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git missing: repository fixture unavailable")
	}
	f := newReopenFixture(t, "idle")
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "TMPDIR"} {
		t.Setenv(key, f.env.Get(key))
	}
	if err := os.Symlink(git, filepath.Join(f.fakeDir, filepath.Base(git))); err != nil {
		t.Skipf("git symlink unavailable: %v", err)
	}
	f.env["PATH"] = f.fakeDir
	t.Setenv("PATH", f.fakeDir)
	f.env["GIT_CONFIG_NOSYSTEM"] = "1"
	f.env["GIT_CONFIG_GLOBAL"] = os.DevNull
	cmd := exec.Command(git, "-C", f.cwd, "init", "-q")
	cmd.Env = f.env.List()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	reopenMcpRow(t, f, "codex", "inherit")
	reopenWithReport(t, f)
	setReopenRules(t, f)
	if _, err := fakecli.Install(t, f.fakeDir, "codex", nil); err != nil {
		t.Fatal(err)
	}
	run := f.reopen(t, "worker")
	if run.code != 0 {
		t.Fatalf("exit=%d stderr=%q", run.code, run.stderr)
	}
	root, _ := filepath.EvalSymlinks(f.cwd)
	want := "projects={'" + root + `'={trust_level='trusted'}}`
	if !containsReopenArgPair(reopenStartArgv(run), "-c", want) {
		t.Fatalf("missing trust pair: %q stderr=%q", reopenStartArgv(run), run.stderr)
	}
	if strings.Contains(reopenFinalRow(t, f), "projects={") {
		t.Fatal("trust override leaked to roster")
	}
}
