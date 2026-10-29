package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
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

// callerShellPIDs is testutil.CallerShellPIDs: the scripted shell and
// fork pids, kept off this process and pid 1 (DJA-569).
func callerShellPIDs() (shell, fork int) { return testutil.CallerShellPIDs() }

// scriptCallerTree scripts the ancestry the caller-pane check walks
// (platform.OverrideAncestorParents: the same test point on every system,
// no ps, no process snapshot): a forked tree never reaches the claimed
// pane's shell; a verified one does.
func scriptCallerTree(t *testing.T, forked bool) {
	t.Helper()
	shell, fork := callerShellPIDs()
	parents := map[int]int{os.Getpid(): shell, shell: 1}
	if forked {
		parents = map[int]int{os.Getpid(): fork, fork: 1}
	}
	t.Cleanup(platform.OverrideAncestorParents(parents))
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
	shell, _ := callerShellPIDs()
	// DJA-569: the shell pid is this run's collision-free scripted value,
	// never the walk's start pid (the test process's own pid).
	refuseLine := fmt.Sprintf("soho: title: HERDR_PANE_ID='%s' is a live pane but this process does not run in it (its process tree does not reach the pane's shell %d); the id was inherited from another process, such as a forked session. Nothing was changed. Run soho from the pane that hosts this agent (a Claude Code background session runs with HERDR_PANE_ID set to the pane that shows it), or set caller_check=warn\n", pane, shell)
	warnLine := fmt.Sprintf("soho: warning: title: HERDR_PANE_ID='%s' is a live pane but this process does not run in it (its process tree does not reach the pane's shell %d); the id was inherited from another process, such as a forked session. Continuing because caller_check=warn. Run soho from the pane that hosts this agent (a Claude Code background session runs with HERDR_PANE_ID set to the pane that shows it), or set caller_check=warn", pane, shell)

	install := func(t *testing.T, forked bool) (platform.Env, string, string) {
		t.Helper()
		dir := t.TempDir()
		herdrRules := []fakecli.Rule{
			{Argv: []string{"pane", "process-info", "--pane", pane}, Stdout: fmt.Sprintf(`{"result":{"process_info":{"shell_pid":%d}}}`, shell)},
			{Argv: []string{"pane", "report-metadata", pane, "--source", "soho", "--title", "orchestrator: goal"}},
		}
		if _, err := fakecli.Install(t, dir, "herdr", herdrRules); err != nil {
			t.Fatal(err)
		}
		scriptCallerTree(t, forked)
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
		if !strings.Contains(errOut, warnLine) {
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

// TestTitleCallerPaneCheckOwnPidIsThePaneShell pins the DJA-569 mechanism
// through the seam, on any OS: the ancestry walk always includes its start
// pid (the test process, internal/platform/ancestors.go), and the check
// reads verified when the claimed shell pid is anywhere in the chain
// (internal/herdr/caller_check.go). When the Windows CI test binary was
// pid 4000, the cli fixtures' scripted shell pid (4000) was the walk's
// start pid, so the scripted forked mismatch read as verified and the
// command proceeded without the check's line — the CI signature: the
// refuse subtest saw the target's agent get (code 4, no rule), the warn
// subtest its missing line. The fixtures now move their scripted pids out
// of the way of the start pid (callerShellPIDs); this test keeps the
// mechanism itself visible.
func TestTitleCallerPaneCheckOwnPidIsThePaneShell(t *testing.T) {
	const pane = "w1:p9"
	herdr.ResetCallerCheck()
	dir := t.TempDir()
	rules := []fakecli.Rule{
		{Argv: []string{"pane", "process-info", "--pane", pane}, Stdout: fmt.Sprintf(`{"result":{"process_info":{"shell_pid":%d}}}`, os.Getpid())},
		{Argv: []string{"pane", "report-metadata", pane, "--source", "soho", "--title", "orchestrator: goal"}},
	}
	if _, err := fakecli.Install(t, dir, "herdr", rules); err != nil {
		t.Fatal(err)
	}
	// The scripted tree itself is irrelevant: the start pid is the shell.
	scriptCallerTree(t, true)
	env, cwd := commandFixture(t)
	env["PATH"] = dir
	env["SOHO_FAKECLI_CONFIG"] = dir
	env["HERDR_ENV"] = "1"
	env["HERDR_PANE_ID"] = pane
	code, _, errOut := runIn(t, []string{"title", "goal"}, env, cwd)
	if code != 0 || errOut != "" {
		t.Fatalf("code=%d err=%q want the check to read verified (own pid is the pane's shell): no refusal, no warn line", code, errOut)
	}
	if n := titleCallCount(t, dir, "pane", "report-metadata"); n != 1 {
		t.Fatalf("report-metadata calls=%d want 1 (the command proceeded)", n)
	}
	if n := titleCallCount(t, dir, "pane", "process-info"); n != 1 {
		t.Fatalf("process-info calls=%d want 1 (the check ran and read the claim)", n)
	}
}
