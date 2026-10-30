package doctor

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

// callerDoctorWarn is the mismatch warning for the scripted shell pid,
// kept off this process and pid 1 (testutil.CallerShellPIDs, DJA-569).
func callerDoctorWarn() string {
	shell, _ := testutil.CallerShellPIDs()
	return fmt.Sprintf("warn   Herdr context invalid: HERDR_PANE_ID='w0test:p0a' is live but this process does not run in it (its process tree does not reach shell pid %d); fix the inherited HERDR_* environment\n", shell)
}

// callerDoctorFixture extends the pane-context fixture with the
// process-info read of the claimed pane and the scripted ancestry
// (platform.OverrideAncestorParents: no ps, no process snapshot).
func callerDoctorFixture(t *testing.T, withProcessInfo bool, parents map[int]int) (platform.Env, string) {
	t.Helper()
	rules := []fakecli.Rule{
		{Argv: []string{"--version"}, Stdout: "herdr 1.2.3\n"},
		{Argv: []string{"status", "server"}, Stdout: "server 1.2.3\n"},
		{Argv: []string{"pane", "get", "w0test:p0a"}, Stdout: `{"result":{"pane":{"title":"orchestrator","workspace_id":"ws"}}}`},
	}
	if withProcessInfo {
		shell, _ := testutil.CallerShellPIDs()
		rules = append(rules, fakecli.Rule{Argv: []string{"pane", "process-info", "--pane", "w0test:p0a"}, Stdout: fmt.Sprintf(`{"result":{"process_info":{"shell_pid":%d}}}`, shell)})
	}
	env, root := paneContextFixture(t, rules)
	t.Cleanup(platform.OverrideAncestorParents(parents))
	env["HERDR_PANE_ID"] = "w0test:p0a"
	env["HERDR_WORKSPACE_ID"] = "ws"
	return env, root
}

// forkedDoctorPS is an ancestry table that reaches the root without the
// pane's shell.
func forkedDoctorPS() map[int]int {
	_, fork := testutil.CallerShellPIDs()
	return map[int]int{os.Getpid(): fork, fork: 1}
}

// verifiedDoctorPS is an ancestry table that reaches the pane's shell.
func verifiedDoctorPS() map[int]int {
	shell, _ := testutil.CallerShellPIDs()
	return map[int]int{os.Getpid(): shell, shell: 1}
}

// doctorProcessInfoCalls counts the fake herdr's process-info calls.
func doctorProcessInfoCalls(t *testing.T, root string) int {
	t.Helper()
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(root, "bin", "herdr.json"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	n := 0
	for _, call := range calls {
		if len(call.Argv) >= 3 && call.Argv[0] == "pane" && call.Argv[1] == "process-info" && call.Argv[2] == "--pane" {
			n++
		}
	}
	return n
}

func TestDoctorCallerPaneWarning(t *testing.T) {
	t.Run("mismatch warns under refuse (the default)", func(t *testing.T) {
		herdr.ResetCallerCheck()
		env, root := callerDoctorFixture(t, true, forkedDoctorPS())
		got := doctorCheckOutput(t, env, root)
		if !strings.Contains(got, callerDoctorWarn()) {
			t.Fatalf("doctor output missing the warning:\n%s", got)
		}
		if n := doctorProcessInfoCalls(t, root); n != 1 {
			t.Fatalf("process-info calls=%d want 1", n)
		}
	})
	t.Run("mismatch warns under warn too", func(t *testing.T) {
		herdr.ResetCallerCheck()
		env, root := callerDoctorFixture(t, true, forkedDoctorPS())
		env["SOHO_CALLER_CHECK"] = "warn"
		got := doctorCheckOutput(t, env, root)
		if !strings.Contains(got, callerDoctorWarn()) {
			t.Fatalf("doctor output missing the warning:\n%s", got)
		}
	})
	t.Run("mismatch stays quiet and makes no process-info call under off", func(t *testing.T) {
		herdr.ResetCallerCheck()
		env, root := callerDoctorFixture(t, true, forkedDoctorPS())
		env["SOHO_CALLER_CHECK"] = "off"
		got := doctorCheckOutput(t, env, root)
		if strings.Contains(got, "Herdr context invalid: HERDR_PANE_ID='w0test:p0a' is live but this process does not run in it") {
			t.Fatalf("off must not warn:\n%s", got)
		}
		if n := doctorProcessInfoCalls(t, root); n != 0 {
			t.Fatalf("off made %d process-info calls", n)
		}
	})
	t.Run("verified stays quiet", func(t *testing.T) {
		herdr.ResetCallerCheck()
		env, root := callerDoctorFixture(t, true, verifiedDoctorPS())
		got := doctorCheckOutput(t, env, root)
		if strings.Contains(got, "Herdr context invalid: HERDR_PANE_ID='w0test:p0a' is live but this process does not run in it") {
			t.Fatalf("verified must not warn:\n%s", got)
		}
	})
	t.Run("unknown (process-info fails) stays quiet", func(t *testing.T) {
		herdr.ResetCallerCheck()
		env, root := callerDoctorFixture(t, false, forkedDoctorPS())
		got := doctorCheckOutput(t, env, root)
		if strings.Contains(got, "Herdr context invalid: HERDR_PANE_ID='w0test:p0a' is live but this process does not run in it") {
			t.Fatalf("unknown must not warn:\n%s", got)
		}
	})
}
