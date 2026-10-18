package doctor

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

const callerDoctorWarn = "warn   Herdr context invalid: HERDR_PANE_ID='w0test:p0a' is live but this process does not run in it (its process tree does not reach shell pid 4000); fix the inherited HERDR_* environment\n"

// callerDoctorFixture extends the pane-context fixture with the
// process-info read of the claimed pane and a fake ps in the same bin
// dir (so the ancestry walk reads the scripted table, never a command
// line).
func callerDoctorFixture(t *testing.T, withProcessInfo bool, psTable map[string]string) (platform.Env, string) {
	t.Helper()
	rules := []fakecli.Rule{
		{Argv: []string{"--version"}, Stdout: "herdr 1.2.3\n"},
		{Argv: []string{"status", "server"}, Stdout: "server 1.2.3\n"},
		{Argv: []string{"pane", "get", "w0test:p0a"}, Stdout: `{"result":{"pane":{"title":"orchestrator","workspace_id":"ws"}}}`},
	}
	if withProcessInfo {
		rules = append(rules, fakecli.Rule{Argv: []string{"pane", "process-info", "--pane", "w0test:p0a"}, Stdout: `{"result":{"process_info":{"shell_pid":4000}}}`})
	}
	env, root := paneContextFixture(t, rules)
	bin := filepath.Join(root, "bin")
	psRules := make([]fakecli.Rule, 0, len(psTable))
	for pid, out := range psTable {
		psRules = append(psRules, fakecli.Rule{Argv: []string{"-o", "ppid=", "-p", pid}, Stdout: out})
	}
	if _, err := fakecli.Install(t, bin, "ps", psRules); err != nil {
		t.Fatal(err)
	}
	env["HERDR_PANE_ID"] = "w0test:p0a"
	env["HERDR_WORKSPACE_ID"] = "ws"
	return env, root
}

// forkedDoctorPS is an ancestry table that reaches the root without the
// pane's shell (4000).
func forkedDoctorPS() map[string]string {
	return map[string]string{strconv.Itoa(os.Getpid()): "3000\n", "3000": "1\n"}
}

// verifiedDoctorPS is an ancestry table that reaches the pane's shell.
func verifiedDoctorPS() map[string]string {
	return map[string]string{strconv.Itoa(os.Getpid()): "4000\n", "4000": "1\n"}
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
		if !strings.Contains(got, callerDoctorWarn) {
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
		if !strings.Contains(got, callerDoctorWarn) {
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
