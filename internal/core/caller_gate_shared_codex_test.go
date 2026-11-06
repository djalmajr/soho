package core

import (
	"errors"
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

const sharedGateSession = "66bf36b5-9b48-4b58-88a3-5da128ff288b"
const sharedGateDescription = "HERDR_PANE_ID='w1:p9' cannot identify this Codex executor: its complete ancestry contains at least two Codex executables, so it may carry another pane's identity. "
const sharedGateSuffix = " For intentional nested codex exec, caller_check=warn is available."

func TestCallerGateSharedCodex(t *testing.T) {
	for _, tc := range []struct {
		name, mode, session string
		two, delegated      bool
	}{
		{"refuse", "refuse", sharedGateSession, true, false},
		{"warn", "warn", sharedGateSession, true, false},
		{"off", "off", sharedGateSession, true, false},
		{"absent-session", "refuse", "", true, false},
		{"invalid-session", "refuse", "bad;resume", true, false},
		{"single", "refuse", sharedGateSession, false, false},
		{"delegated", "refuse", sharedGateSession, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			herdr.ResetCallerCheck()
			t.Cleanup(herdr.ResetCallerCheck)
			env, cwd := fixture(t)
			bin := t.TempDir()
			shell, _ := testutil.CallerShellPIDs()
			rules := []fakecli.Rule{
				{Argv: []string{"pane", "process-info", "--pane", "w1:p9"}, Stdout: fmt.Sprintf(`{"result":{"process_info":{"shell_pid":%d}}}`, shell)},
				{Argv: []string{"pane", "list"}, Stdout: `{"result":{"panes":[{"pane_id":"w2:p8","agent_session":{"agent":"codex","kind":"id","value":"` + sharedGateSession + `","source":"herdr:codex"}}]}}`},
			}
			if _, err := fakecli.Install(t, bin, "herdr", rules); err != nil {
				t.Fatal(err)
			}
			env["PATH"], env["SOHO_FAKECLI_CONFIG"] = bin, bin
			env["HERDR_ENV"], env["HERDR_PANE_ID"], env["CODEX_THREAD_ID"] = "1", "w1:p9", tc.session
			env["SOHO_CALLER_CHECK"] = tc.mode
			env["USERPROFILE"] = env["HOME"]
			env["TMPDIR"] = t.TempDir()
			if tc.delegated {
				env["SOHO_DELEGATED_PANE"] = "w1:p9"
			}
			t.Cleanup(platform.OverrideAncestorParents(map[int]int{os.Getpid(): shell, shell: 1}))
			reads := 0
			t.Cleanup(platform.OverrideProcExecutableRunner(func(pid int) (string, error) {
				reads++
				if pid == os.Getpid() || (pid == shell && tc.two) {
					return filepath.Join(bin, "codex"), nil
				}
				return "", errors.New("synthetic unreadable executable")
			}))
			state := StateDir(&Config{}, env, cwd)
			before, err := os.ReadFile(filepath.Join(state, "agents.tsv"))
			if err != nil {
				t.Fatal(err)
			}
			var failure *platform.ExitError
			_, stderr := capture(t, func() {
				defer func() {
					if r := recover(); r != nil {
						var ok bool
						failure, ok = r.(*platform.ExitError)
						if !ok {
							panic(r)
						}
					}
				}()
				CallerGate("init", &Config{}, env, cwd)
			})
			blocked := tc.two && !tc.delegated && tc.mode != "off"
			if !blocked {
				if failure != nil || stderr != "" {
					t.Fatalf("accepted control failed: %v %q", failure, stderr)
				}
			} else {
				id := tc.session
				if id == "" || tc.name == "invalid-session" {
					id = "<session>"
				}
				description := sharedGateDescription
				if id != "<session>" {
					description += "This Codex session is shown in pane 'w2:p8'. "
				}
				description += "Resume this agent in the intended pane with 'codex resume --no-daemon " + id + "'. "
				message := "init: " + description
				if tc.mode == "warn" {
					message += "Continuing because caller_check=warn." + sharedGateSuffix
					if failure != nil || stderr != "soho: warning: "+message+"\n" {
						t.Fatalf("warn result=%v stderr=%q want=%q", failure, stderr, message)
					}
				} else {
					message += "Nothing was changed." + sharedGateSuffix
					if failure == nil || failure.Code != 2 || failure.Msg != message {
						t.Fatalf("refusal=%#v want=%q", failure, message)
					}
				}
				friction, err := os.ReadFile(filepath.Join(state, "friction.log"))
				if err != nil || strings.Count(string(friction), "\n") != 1 || !strings.Contains(string(friction), message) {
					t.Fatalf("friction=%q err=%v", friction, err)
				}
				if strings.Contains(message, "Claude") || strings.Contains(message, "does not reach") {
					t.Fatalf("false identity description: %s", message)
				}
			}
			after, err := os.ReadFile(filepath.Join(state, "agents.tsv"))
			if err != nil || string(before) != string(after) {
				t.Fatal("gate changed roster")
			}
			if !blocked {
				if _, err := os.Stat(filepath.Join(state, "friction.log")); !os.IsNotExist(err) {
					t.Fatalf("accepted control friction: %v", err)
				}
			}
			if (tc.mode == "off" || tc.delegated) && reads != 0 {
				t.Fatalf("bypass performed %d executable reads", reads)
			}
		})
	}
}
