package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

// TestGateHelperExit is the program the gate tests run: the test binary
// itself, selected with -test.run, exiting with the code the test sets.
func TestGateHelperExit(t *testing.T) {
	code := os.Getenv("SOHO_GATE_CLI_TEST_EXIT")
	if code == "" {
		t.Skip("only a program run by the gate tests")
	}
	if code == "7" {
		os.Exit(7)
	}
	os.Exit(0)
}

func gateProgram() []string {
	return []string{os.Args[0], "-test.run=^TestGateHelperExit$", "-test.count=1"}
}

func listNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func TestGateRunsOutsideHerdrAndUnderEveryTransport(t *testing.T) {
	envs := map[string]platform.Env{
		"no herdr context": {"PATH": os.Getenv("PATH")},
		"transport hc":     {"PATH": os.Getenv("PATH"), "SOHO_TRANSPORT": "hc", "HC_TERMINAL_ID": "not-a-real-terminal"},
		"transport herdr":  {"PATH": os.Getenv("PATH"), "SOHO_TRANSPORT": "herdr", "HERDR_ENV": "1", "HERDR_PANE_ID": "p_1"},
	}
	for name, env := range envs {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("SOHO_GATE_CLI_TEST_EXIT", "7")
			prefix := filepath.Join(dir, "run")
			code, out, errOut := runIn(t, append([]string{"gate", prefix, "1m", "--"}, gateProgram()...), env, dir)
			if code != 7 || errOut != "" {
				t.Fatalf("code=%d stdout=%q stderr=%q; want the program's exit code 7", code, out, errOut)
			}
			data, err := os.ReadFile(prefix + ".result.json")
			if err != nil {
				t.Fatal(err)
			}
			var res struct {
				PID        int  `json:"pid"`
				NativeExit int  `json:"native_exit"`
				Expired    bool `json:"context_expired"`
				Joined     bool `json:"wait_joined"`
			}
			if err := json.Unmarshal(data, &res); err != nil {
				t.Fatal(err)
			}
			if res.NativeExit != 7 || res.Expired || !res.Joined || res.PID <= 0 {
				t.Fatalf("result = %s", data)
			}
			if !strings.HasPrefix(out, "pid=") || !strings.HasSuffix(out, " native_exit=7 context_expired=false wait_joined=true\n") {
				t.Fatalf("stdout %q", out)
			}
			if want := []string{"run.command.json", "run.result.json", "run.stderr", "run.stdout"}; len(listNames(t, dir)) != len(want) {
				t.Fatalf("artifacts = %v", listNames(t, dir))
			}
		})
	}
}

func TestGateExitZeroAndMissingProgramThroughRun(t *testing.T) {
	env := platform.Env{"PATH": os.Getenv("PATH")}
	dir := t.TempDir()
	t.Setenv("SOHO_GATE_CLI_TEST_EXIT", "0")
	code, out, _ := runIn(t, append([]string{"gate", filepath.Join(dir, "ok"), "1m", "--"}, gateProgram()...), env, dir)
	if code != 0 || !strings.Contains(out, " native_exit=0 context_expired=false wait_joined=true") {
		t.Fatalf("code=%d stdout=%q", code, out)
	}
	code, out, _ = runIn(t, []string{"gate", filepath.Join(dir, "missing"), "1m", "--", filepath.Join(dir, "no-such-program")}, env, dir)
	if code != 125 || out != "pid=0 native_exit=-1 context_expired=false wait_joined=false\n" {
		t.Fatalf("code=%d stdout=%q; want 125 and the start-failure line", code, out)
	}
}

func TestGateUsageErrorsExit2AndWriteNothing(t *testing.T) {
	env := platform.Env{"PATH": os.Getenv("PATH")}
	dir := t.TempDir()
	prefix := filepath.Join(dir, "run")
	for name, args := range map[string][]string{
		"no arguments":      {"gate"},
		"short duration":    {"gate", prefix, "500ms", "--", "x"},
		"long duration":     {"gate", prefix, "61m", "--", "x"},
		"missing --":        {"gate", prefix, "5s", "x"},
		"missing program":   {"gate", prefix, "5s", "--"},
		"missing parent":    {"gate", filepath.Join(dir, "nope", "run"), "5s", "--", "x"},
		"unknown flag-like": {"gate", "--bogus"},
	} {
		t.Run(name, func(t *testing.T) {
			code, out, errOut := runIn(t, args, env, dir)
			if code != 2 || out != "" || !strings.HasPrefix(errOut, "soho: gate: ") {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, out, errOut)
			}
			if names := listNames(t, dir); len(names) != 0 {
				t.Fatalf("created %v", names)
			}
		})
	}
}

func TestGateIsRefusedUnderNowriteBeforeAnythingStarts(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "marker")
	env := platform.Env{"PATH": os.Getenv("PATH"), "SOHO_NOWRITE": "1"}
	code, out, errOut := runIn(t, []string{"gate", filepath.Join(dir, "run"), "5s", "--", "touch", marker}, env, dir)
	if code != 2 || out != "" || !strings.Contains(errOut, "SOHO_NOWRITE=1 is read-only") || !strings.Contains(errOut, "rejected: gate (5 extra arguments not allowed)") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out, errOut)
	}
	if names := listNames(t, dir); len(names) != 0 {
		t.Fatalf("created %v", names)
	}
}

func TestGateHelpPrintsItsOwnBlock(t *testing.T) {
	const want = "  soho gate <prefix> <duration> -- <program> [args…]\n" +
		"                                             # run one program directly (no shell) under a bound of 1s to 1h and record <prefix>.command.json (argv, cwd, bound, started; never the environment), <prefix>.stdout, <prefix>.stderr and <prefix>.result.json (pid, native_exit, signal, context_expired, wait_joined, wait_error, start_error), all mode 0600; an existing artifact or a missing parent directory refuses (exit 2) before anything starts; on expiry it terminates the started process and its process group, waits 5 seconds, then kills (Windows: the direct child only); needs no Herdr context; prints pid=<pid> native_exit=<n> context_expired=<bool> wait_joined=<bool>; exits with the program's code, 124 when the bound expired, 125 when it could not start or was killed by a signal, 2 on usage\n"
	dir := t.TempDir()
	for _, flag := range []string{"--help", "-h"} {
		code, out, errOut := runIn(t, []string{"gate", flag}, platform.Env{"PATH": os.Getenv("PATH")}, dir)
		if code != 0 || out != want || errOut != "" {
			t.Fatalf("gate %s: code=%d out=%q err=%q", flag, code, out, errOut)
		}
	}
	if !reflect.DeepEqual(listNames(t, dir), []string(nil)) {
		t.Fatal("help wrote files")
	}
}
