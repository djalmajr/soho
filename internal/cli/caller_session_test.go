package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// TestTitleCallerSessionCheck: the session half of the caller_check
// (DJA-406) through a real command. The tree never reaches the pane's
// shell; the pane's Claude Code session either leads to this process's
// session (the command acts, with no line in stderr or in the friction
// log) or does not (refusal or warning with the session sentences, and the
// pane that shows this session when there is exactly one). The
// environment is built by the test: the CLAUDE_CODE_SESSION_ID of the
// process that runs the tests never reaches it.
func TestTitleCallerSessionCheck(t *testing.T) {
	shell, _ := callerShellPIDs()
	const (
		pane     = "w1:p9"
		own      = "66bf36b5-9b48-4b58-88a3-5da128ff288b" // S
		hosted   = "d630d58c-2c56-4147-9bdb-206cba98b68e" // A, hosted by the claimed pane
		stranger = "33333333-3333-4333-8333-333333333333"
		linkless = "This process is Claude Code session " + own + "; the pane hosts session " + hosted + ", and no continued-in record links them. "
		fix      = " Run soho from the pane that hosts this agent (a Claude Code background session runs with HERDR_PANE_ID set to the pane that shows it), or set caller_check=warn"
	)
	// DJA-569: the shell pid is this run's collision-free scripted value,
	// never the walk's start pid (the test process's own pid).
	prefix := fmt.Sprintf("HERDR_PANE_ID='w1:p9' is a live pane but this process does not run in it (its process tree does not reach the pane's shell %d); the id was inherited from another process, such as a forked session. ", shell)

	paneGet := func(session string) fakecli.Rule {
		return fakecli.Rule{Argv: []string{"pane", "get", pane}, Stdout: fmt.Sprintf(`{"result":{"pane":{"pane_id":%q,"agent_session":{"agent":"claude","kind":"id","source":"herdr:claude","value":%q}}}}`, pane, session)}
	}
	paneList := func(entries ...[2]string) fakecli.Rule {
		panes := make([]string, 0, len(entries))
		for _, e := range entries {
			panes = append(panes, fmt.Sprintf(`{"pane_id":%q,"agent":"claude","agent_session":{"agent":"claude","kind":"id","source":"herdr:claude","value":%q}}`, e[0], e[1]))
		}
		return fakecli.Rule{Argv: []string{"pane", "list"}, Stdout: `{"result":{"panes":[` + strings.Join(panes, ",") + `]}}`}
	}
	continued := func(from, to string) string {
		return fmt.Sprintf(`{"type":"continued-in","sessionId":%q,"continuedInSessionId":%q}`+"\n", from, to)
	}

	install := func(t *testing.T, rules ...fakecli.Rule) (platform.Env, string, string, string) {
		t.Helper()
		herdr.ResetCallerCheck()
		dir := t.TempDir()
		rules = append([]fakecli.Rule{
			{Argv: []string{"pane", "process-info", "--pane", pane}, Stdout: fmt.Sprintf(`{"result":{"process_info":{"shell_pid":%d}}}`, shell)},
			{Argv: []string{"pane", "report-metadata", pane, "--source", "soho", "--title", "orchestrator: goal"}},
		}, rules...)
		if _, err := fakecli.Install(t, dir, "herdr", rules); err != nil {
			t.Fatal(err)
		}
		scriptCallerTree(t, true)
		env, cwd := commandFixture(t)
		env["PATH"] = dir
		env["SOHO_FAKECLI_CONFIG"] = dir
		env["HERDR_ENV"] = "1"
		env["HERDR_PANE_ID"] = pane
		claude := t.TempDir()
		env["CLAUDE_CONFIG_DIR"] = claude
		env["CLAUDE_CODE_SESSION_ID"] = own
		return env, cwd, dir, claude
	}
	transcript := func(t *testing.T, claude, session, body string) {
		t.Helper()
		dir := filepath.Join(claude, "projects", "-edger")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, session+".jsonl"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	friction := func(env platform.Env) string {
		raw, err := os.ReadFile(filepath.Join(env["SOHO_DIR"], "ws", "friction.log"))
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		return string(raw)
	}

	t.Run("refuse names both sessions and the one pane that shows this session", func(t *testing.T) {
		env, cwd, dir, _ := install(t, paneGet(hosted), paneList([2]string{pane, hosted}, [2]string{"w2:p3", own}, [2]string{"w2:p4", stranger}))
		code, out, errOut := runIn(t, []string{"title", "goal"}, env, cwd)
		want := "soho: title: " + prefix + linkless + "This Claude Code session is shown in pane 'w2:p3'; run with HERDR_PANE_ID=w2:p3. Nothing was changed." + fix + "\n"
		if code != 2 || errOut != want {
			t.Fatalf("code=%d out=%q\nstderr=%q\nwant   %q", code, out, errOut, want)
		}
		if n := titleCallCount(t, dir, "pane", "report-metadata"); n != 0 {
			t.Fatalf("report-metadata was called %d times", n)
		}
		if got := friction(env); !strings.Contains(got, linkless+"This Claude Code session is shown in pane 'w2:p3'; run with HERDR_PANE_ID=w2:p3. Nothing was changed.") {
			t.Fatalf("friction.log=%q", got)
		}
	})

	t.Run("two candidate panes give the sessions sentence and no suggestion", func(t *testing.T) {
		env, cwd, _, _ := install(t, paneGet(hosted), paneList([2]string{"w2:p3", own}, [2]string{"w2:p5", own}))
		code, _, errOut := runIn(t, []string{"title", "goal"}, env, cwd)
		want := "soho: title: " + prefix + linkless + "Nothing was changed." + fix + "\n"
		if code != 2 || errOut != want {
			t.Fatalf("code=%d\nstderr=%q\nwant   %q", code, errOut, want)
		}
	})

	t.Run("without S the line is the old one, with the new fix", func(t *testing.T) {
		env, cwd, dir, _ := install(t, paneGet(hosted))
		delete(env, "CLAUDE_CODE_SESSION_ID")
		code, _, errOut := runIn(t, []string{"title", "goal"}, env, cwd)
		want := "soho: title: " + prefix + "Nothing was changed." + fix + "\n"
		if code != 2 || errOut != want {
			t.Fatalf("code=%d\nstderr=%q\nwant   %q", code, errOut, want)
		}
		if n := titleCallCount(t, dir, "pane", "get") + titleCallCount(t, dir, "pane", "list"); n != 0 {
			t.Fatalf("pane get/list calls=%d without S", n)
		}
	})

	t.Run("warn carries the same sentences and continues", func(t *testing.T) {
		env, cwd, _, _ := install(t, paneGet(hosted), paneList([2]string{"w2:p3", own}))
		env["SOHO_CALLER_CHECK"] = "warn"
		code, out, errOut := runIn(t, []string{"title", "goal"}, env, cwd)
		want := "soho: warning: title: " + prefix + linkless + "This Claude Code session is shown in pane 'w2:p3'; run with HERDR_PANE_ID=w2:p3. Continuing because caller_check=warn." + fix + "\n"
		if code != 0 || !strings.Contains(out, `"pane_id": "w1:p9"`) || !strings.Contains(errOut, want) {
			t.Fatalf("code=%d out=%q\nstderr=%q\nwant   %q", code, out, errOut, want)
		}
		if got := friction(env); !strings.Contains(got, linkless+"This Claude Code session is shown in pane 'w2:p3'; run with HERDR_PANE_ID=w2:p3. Continuing because caller_check=warn.") {
			t.Fatalf("friction.log=%q", got)
		}
	})

	t.Run("a pane whose session continued in S acts with no line anywhere", func(t *testing.T) {
		env, cwd, dir, claude := install(t, paneGet(hosted))
		transcript(t, claude, hosted, `{"type":"assistant"}`+"\n"+continued(hosted, own))
		code, out, errOut := runIn(t, []string{"title", "goal"}, env, cwd)
		if code != 0 || errOut != "" || !strings.Contains(out, `"pane_id": "w1:p9"`) {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, errOut)
		}
		if got := friction(env); strings.Contains(got, "does not run in it") || strings.Contains(got, "caller_check") {
			t.Fatalf("friction.log=%q", got)
		}
		if n := titleCallCount(t, dir, "pane", "report-metadata"); n != 1 {
			t.Fatalf("report-metadata calls=%d want 1", n)
		}
		if n := titleCallCount(t, dir, "pane", "list"); n != 0 {
			t.Fatalf("a verified session read the pane list (%d calls)", n)
		}
	})

	t.Run("a pane whose session is S acts with no line anywhere, under warn too", func(t *testing.T) {
		env, cwd, _, _ := install(t, paneGet(own))
		env["SOHO_CALLER_CHECK"] = "warn"
		code, _, errOut := runIn(t, []string{"title", "goal"}, env, cwd)
		if code != 0 || errOut != "" {
			t.Fatalf("code=%d stderr=%q", code, errOut)
		}
		if got := friction(env); strings.Contains(got, "caller_check") {
			t.Fatalf("friction.log=%q", got)
		}
	})
}
