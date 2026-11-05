package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestTM10WorktreeCommandsReadMainState(t *testing.T) {
	t.Run(`JS: "roster, status, and wait in a linked worktree read the main checkout state"`, func(t *testing.T) {
		env, main := commandFixture(t)
		for _, args := range [][]string{{"config", "user.email", "test@example.invalid"}, {"config", "user.name", "Test"}} {
			cmd := exec.Command("git", args...)
			cmd.Dir = main
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %s: %v", args, out, err)
			}
		}
		if err := os.WriteFile(filepath.Join(main, "tracked"), []byte("fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"add", "tracked"}, {"commit", "-qm", "initial"}} {
			cmd := exec.Command("git", args...)
			cmd.Dir = main
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %s: %v", args, out, err)
			}
		}
		worker := filepath.Join(main, ".worktrees", "worker")
		if err := os.MkdirAll(filepath.Dir(worker), 0o700); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("git", "worktree", "add", "-qb", "worker", worker)
		cmd.Dir = main
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git worktree add: %s: %v", out, err)
		}
		realMain, err := filepath.EvalSymlinks(main)
		if err != nil {
			t.Fatal(err)
		}
		state := filepath.Join(realMain, ".soho", "ws")
		if err := os.MkdirAll(filepath.Join(state, "reports"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(state, "wait"), 0o700); err != nil {
			t.Fatal(err)
		}
		row := strings.Join([]string{"build", "p1", "grok", "implementer", "xai", "0", worker, "20260928T120000", "", "task", "implementer", ""}, "\t")
		roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\n" + row + "\n"
		if err := os.WriteFile(filepath.Join(state, "agents.tsv"), []byte(roster), 0o600); err != nil {
			t.Fatal(err)
		}
		report := filepath.Join(state, "reports", "build.md")
		if err := os.WriteFile(report, []byte("completed\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(state, "last-report-build"), []byte(report+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		fakeDir := t.TempDir()
		gitRules := []fakecli.Rule{
			{Argv: []string{"rev-parse", "--git-dir"}, Stdout: filepath.Join(realMain, ".git", "worktrees", "worker") + "\n"},
			{Argv: []string{"rev-parse", "--git-common-dir"}, Stdout: filepath.Join(realMain, ".git") + "\n"},
			{Argv: []string{"rev-parse", "--show-toplevel"}, Stdout: realMain + "\n"},
			{Argv: []string{"-C", realMain, "rev-parse", "--is-inside-work-tree"}, Stdout: "true\n"},
			{ArgvPrefix: true, Argv: []string{"check-ignore"}, Code: 1},
			{AnyArgs: true},
		}
		if _, err := fakecli.Install(t, fakeDir, "git", gitRules); err != nil {
			t.Fatal(err)
		}
		rules := []fakecli.Rule{{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`}, {Argv: []string{"pane", "list", "--workspace", "ws"}, Stdout: `{"result":{"panes":[]}}`}, {Argv: []string{"tab", "list", "--workspace", "ws"}, Stdout: `{"result":{"tabs":[]}}`}, {AnyArgs: true}}
		if _, err := fakecli.Install(t, fakeDir, "herdr", rules); err != nil {
			t.Fatal(err)
		}
		env = withFakeCLI(env, fakeDir)
		env["SOHO_DIR"] = ""
		env["HERDR_WORKSPACE_ID"], env["HERDR_ENV"] = "ws", "1"
		old, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chdir(worker); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chdir(old) })
		run := func(args ...string) (string, string, int) {
			t.Helper()
			oldOut, oldErr := platform.Stdout, platform.Stderr
			var out, stderr bytes.Buffer
			platform.Stdout, platform.Stderr = &out, &stderr
			code := Run(args, env)
			platform.Stdout, platform.Stderr = oldOut, oldErr
			return out.String(), stderr.String(), code
		}
		rosterOut, rosterErr, code := run("roster")
		if code != 0 || rosterErr != "" || !strings.Contains(rosterOut, "build") || !strings.Contains(rosterOut, "implementer") {
			t.Fatalf("roster code=%d out=%q err=%q", code, rosterOut, rosterErr)
		}
		statusOut, statusErr, code := run("status", "build")
		if code != 0 || statusErr != "" || !strings.Contains(statusOut, "build\tdone") {
			t.Fatalf("status code=%d out=%q err=%q", code, statusOut, statusErr)
		}
		waitOut, waitErr, code := run("wait", "build", "--timeout", "1000ms")
		if code != 0 || waitErr != "" || !strings.Contains(waitOut, `"agent":"build"`) {
			t.Fatalf("wait code=%d out=%q err=%q", code, waitOut, waitErr)
		}
		if _, err := os.Stat(filepath.Join(worker, ".soho")); !os.IsNotExist(err) {
			t.Fatalf("worker has separate state: %v", err)
		}
		if got := core.StateRoot(&core.Config{Entries: map[string]core.ConfigEntry{}}, env, worker); got != filepath.Dir(state) {
			t.Fatalf("state root=%q want=%q", got, filepath.Dir(state))
		}
	})
}

func TestTM10RosterGitProcessBound(t *testing.T) {
	t.Run(`JS: "roster resolves roots in at most eight git processes in main and linked worktrees"`, func(t *testing.T) {
		env, main := commandFixture(t)
		if err := os.WriteFile(filepath.Join(main, "tracked"), []byte("root\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"config", "user.email", "test@example.invalid"}, {"config", "user.name", "Test"}, {"add", "tracked"}, {"commit", "-qm", "initial"}} {
			cmd := exec.Command("git", args...)
			cmd.Dir = main
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %s: %v", args, out, err)
			}
		}
		linked := filepath.Join(t.TempDir(), "linked")
		cmd := exec.Command("git", "worktree", "add", "-qb", "linked", linked)
		cmd.Dir = main
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git worktree add: %s: %v", out, err)
		}
		fakeDir := t.TempDir()
		gitDir := filepath.Join(main, ".git", "worktrees", "linked")
		commonDir := filepath.Join(main, ".git")
		gitRules := []fakecli.Rule{
			{Argv: []string{"rev-parse", "--show-toplevel"}, Stdout: main + "\n"},
			{Argv: []string{"rev-parse", "--git-dir"}, Stdout: gitDir + "\n"},
			{Argv: []string{"rev-parse", "--git-common-dir"}, Stdout: commonDir + "\n"},
			{ArgvPrefix: true, Argv: []string{"check-ignore"}, Code: 1},
			{AnyArgs: true},
		}
		if _, err := fakecli.Install(t, fakeDir, "git", gitRules); err != nil {
			t.Fatal(err)
		}
		herdrRules := []fakecli.Rule{
			{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`},
			{Argv: []string{"pane", "list", "--workspace", "ws"}, Stdout: `{"result":{"panes":[]}}`},
			{Argv: []string{"tab", "list", "--workspace", "ws"}, Stdout: `{"result":{"tabs":[]}}`},
			{AnyArgs: true},
		}
		if _, err := fakecli.Install(t, fakeDir, "herdr", herdrRules); err != nil {
			t.Fatal(err)
		}
		old, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chdir(linked); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chdir(old) })
		env["PATH"] = fakeDir
		env["SOHO_FAKECLI_CONFIG"] = fakeDir
		env["HERDR_SOCKET_PATH"] = filepath.Join(t.TempDir(), "missing.sock")
		env["HERDR_ENV"], env["SOHO_DIR"], env["HERDR_WORKSPACE_ID"] = "1", filepath.Join(t.TempDir(), "state"), "ws"
		oldOut, oldErr := platform.Stdout, platform.Stderr
		var out, stderr bytes.Buffer
		platform.Stdout, platform.Stderr = &out, &stderr
		code := Run([]string{"roster"}, env)
		platform.Stdout, platform.Stderr = oldOut, oldErr
		calls, err := fakecli.ReadCalls(filepath.Join(fakeDir, "git.calls.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if code != 0 || stderr.Len() != 0 || !strings.Contains(out.String(), "NAME                 ROLE") {
			t.Fatalf("code=%d out=%q err=%q", code, out.String(), stderr.String())
		}
		if len(calls) > 8 {
			t.Fatalf("git process count=%d calls=%#v", len(calls), calls)
		}
	})
}
