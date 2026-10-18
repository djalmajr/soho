package cli

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// TestConfigSetCallerCheck: the key accepts its three values, refuses the
// invalid ones with exit 2, and leaves the file untouched on refusal.
func TestConfigSetCallerCheck(t *testing.T) {
	env, cwd := commandFixture(t)
	for _, value := range []string{"refuse", "warn", "off"} {
		code, out, errOut := runIn(t, []string{"config", "set", "caller_check=" + value}, env, cwd)
		if code != 0 || errOut != "" {
			t.Fatalf("config set caller_check=%s code=%d out=%q err=%q", value, code, out, errOut)
		}
		content, err := os.ReadFile(filepath.Join(cwd, ".agents", "soho.conf"))
		if err != nil || string(content) != "caller_check="+value+"\n" {
			t.Fatalf("file=%q err=%v (value=%s)", content, err, value)
		}
	}
	before, _ := os.ReadFile(filepath.Join(cwd, ".agents", "soho.conf"))
	for _, value := range []string{"on", "abc"} {
		code, _, errOut := runIn(t, []string{"config", "set", "caller_check=" + value}, env, cwd)
		if code != 2 || errOut == "" {
			t.Fatalf("config set caller_check=%s code=%d err=%q", value, code, errOut)
		}
		after, _ := os.ReadFile(filepath.Join(cwd, ".agents", "soho.conf"))
		if string(before) != string(after) {
			t.Fatalf("the refusal changed the file: %q -> %q", before, after)
		}
	}
}

// titlePSRules returns the fake ps rules for a forked tree (the claimed
// pane's shell 4000 is never reached) and for a verified tree.
func titlePSRules(t *testing.T, forked bool) []fakecli.Rule {
	t.Helper()
	self := strconv.Itoa(os.Getpid())
	middle := "3000"
	if forked {
		return []fakecli.Rule{
			{Argv: []string{"-o", "ppid=", "-p", self}, Stdout: middle + "\n"},
			{Argv: []string{"-o", "ppid=", "-p", middle}, Stdout: "1\n"},
		}
	}
	return []fakecli.Rule{
		{Argv: []string{"-o", "ppid=", "-p", self}, Stdout: "4000\n"},
		{Argv: []string{"-o", "ppid=", "-p", "4000"}, Stdout: "1\n"},
	}
}

func titleCallCount(t *testing.T, dir string, argv ...string) int {
	t.Helper()
	calls, err := fakecli.ReadCalls(filepath.Join(dir, "herdr.calls.jsonl"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	n := 0
	for _, call := range calls {
		if len(call.Argv) < len(argv) {
			continue
		}
		same := true
		for i, want := range argv {
			if call.Argv[i] != want {
				same = false
				break
			}
		}
		if same {
			n++
		}
	}
	return n
}

// TestTitleCallerPaneCheck: a forked caller tree (the inherited
// HERDR_PANE_ID is live, but this process is not in it) makes `title`
// refuse before `pane report-metadata`, continue with a warning under
// caller_check=warn, and skip the check entirely under off.
func TestTitleCallerPaneCheck(t *testing.T) {
	const pane = "w1:p9"
	const refuseLine = "soho: title: HERDR_PANE_ID='w1:p9' is a live pane but this process does not run in it (its process tree does not reach the pane's shell 4000); the id was inherited from another process, such as a forked session. Nothing was changed. Run soho from the pane that hosts this agent, or set caller_check=warn\n"

	install := func(t *testing.T, forked bool) (platform.Env, string, string) {
		t.Helper()
		dir := t.TempDir()
		herdrRules := []fakecli.Rule{
			{Argv: []string{"pane", "process-info", "--pane", pane}, Stdout: `{"result":{"process_info":{"shell_pid":4000}}}`},
			{Argv: []string{"pane", "report-metadata", pane, "--source", "soho", "--title", "orchestrator: goal"}},
		}
		if _, err := fakecli.Install(t, dir, "herdr", herdrRules); err != nil {
			t.Fatal(err)
		}
		if _, err := fakecli.Install(t, dir, "ps", titlePSRules(t, forked)); err != nil {
			t.Fatal(err)
		}
		env, cwd := commandFixture(t)
		env["PATH"] = dir
		// The fake CLI config (and its call log) must live in the fixture
		// dir: commandFixture pointed it at its own dir.
		env["SOHO_FAKECLI_CONFIG"] = dir
		env["HERDR_ENV"] = "1"
		env["HERDR_PANE_ID"] = pane
		return env, cwd, dir
	}

	t.Run("mismatch with refuse exits 2 before pane report-metadata", func(t *testing.T) {
		herdr.ResetCallerCheck()
		env, cwd, dir := install(t, true)
		code, out, errOut := runIn(t, []string{"title", "goal"}, env, cwd)
		if code != 2 {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
		if errOut != refuseLine {
			t.Fatalf("stderr=%q want %q", errOut, refuseLine)
		}
		if n := titleCallCount(t, dir, "pane", "report-metadata"); n != 0 {
			t.Fatalf("report-metadata was called %d times", n)
		}
		if n := titleCallCount(t, dir, "pane", "process-info", "--pane", pane); n != 1 {
			t.Fatalf("process-info calls=%d want 1", n)
		}
	})
	t.Run("mismatch with warn continues and logs the friction line", func(t *testing.T) {
		herdr.ResetCallerCheck()
		env, cwd, _ := install(t, true)
		env["SOHO_CALLER_CHECK"] = "warn"
		code, out, errOut := runIn(t, []string{"title", "goal"}, env, cwd)
		if code != 0 || !strings.Contains(out, `"pane_id": "w1:p9"`) {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
		if !strings.Contains(errOut, "soho: warning: title: HERDR_PANE_ID='w1:p9' is a live pane but this process does not run in it (its process tree does not reach the pane's shell 4000); the id was inherited from another process, such as a forked session. Continuing because caller_check=warn. Run soho from the pane that hosts this agent, or set caller_check=warn") {
			t.Fatalf("stderr missing the warn line: %q", errOut)
		}
		friction, err := os.ReadFile(filepath.Join(env["SOHO_DIR"], "ws", "friction.log"))
		if err != nil || !strings.Contains(string(friction), "Continuing because caller_check=warn.") {
			t.Fatalf("friction.log=%q err=%v", friction, err)
		}
	})
	t.Run("a verified tree adds no line", func(t *testing.T) {
		herdr.ResetCallerCheck()
		env, cwd, _ := install(t, false)
		code, _, errOut := runIn(t, []string{"title", "goal"}, env, cwd)
		if code != 0 || errOut != "" {
			t.Fatalf("code=%d err=%q", code, errOut)
		}
	})
	t.Run("off makes no process-info call", func(t *testing.T) {
		herdr.ResetCallerCheck()
		env, cwd, dir := install(t, true)
		env["SOHO_CALLER_CHECK"] = "off"
		code, _, errOut := runIn(t, []string{"title", "goal"}, env, cwd)
		if code != 0 || errOut != "" {
			t.Fatalf("code=%d err=%q", code, errOut)
		}
		if n := titleCallCount(t, dir, "pane", "process-info"); n != 0 {
			t.Fatalf("off still made %d process-info calls", n)
		}
	})
}
