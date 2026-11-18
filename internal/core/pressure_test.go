package core_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/spawn"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func pressureFixture(t *testing.T) (ctx *core.Config, env platform.Env, stateDir string) {
	t.Helper()
	root := t.TempDir()
	state := filepath.Join(root, "state")
	if err := os.MkdirAll(filepath.Join(state, "ws"), 0o700); err != nil {
		t.Fatal(err)
	}
	base := testutil.CleanEnv(t)
	env = platform.Env{}
	for _, item := range fakecli.Env(base, filepath.Join(root, "bin")) {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			env[key] = value
		}
	}
	env["HOME"] = root
	env["USERPROFILE"] = root
	env["SOHO_DIR"] = state
	env["HERDR_WORKSPACE_ID"] = "ws"
	// Explicit limits: CleanEnv ships the machine-state warning disabled
	// (0/0), so the tests that expect a warning set their limits themselves.
	env["SOHO_PRESSURE_DISK_FREE_PERCENT"] = "15"
	env["SOHO_PRESSURE_SWAP_PERCENT"] = "80"
	ctx = &core.Config{Entries: map[string]core.ConfigEntry{}}
	return ctx, env, filepath.Join(state, "ws")
}

// fakeMachine injects the platform measurements and restores them after
// the test, so no test ever reads the real disk or swap.
func fakeMachine(t *testing.T, diskFree, diskTotal int64, diskOK bool, swapUsed, swapTotal int64, swapOK bool) {
	t.Helper()
	oldDisk, oldSwap := platform.DiskFree, platform.SwapUsage
	platform.DiskFree = func(string) (int64, int64, bool) { return diskFree, diskTotal, diskOK }
	platform.SwapUsage = func(platform.Env) (int64, int64, bool) { return swapUsed, swapTotal, swapOK }
	t.Cleanup(func() { platform.DiskFree, platform.SwapUsage = oldDisk, oldSwap })
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := platform.Stderr
	var buf bytes.Buffer
	platform.Stderr = &buf
	t.Cleanup(func() { platform.Stderr = old })
	fn()
	return buf.String()
}

const swapPart = "swap 96% used (24 GB of 25 GB)"

func pressureLine(stateDir string, parts ...string) string {
	joined := ""
	for i, part := range parts {
		if i > 0 {
			joined += "; "
		}
		joined += part
	}
	return "soho: resource pressure: " + joined + "; run 'soho gc' and close idle panes before opening more\n"
}

func diskPressure() (int64, int64) {
	return int64(9) * 1024 * 1024 * 1024, int64(100) * 1024 * 1024 * 1024
}
func swapPressure() (int64, int64) {
	return int64(24) * 1024 * 1024 * 1024, int64(25) * 1024 * 1024 * 1024
}

func TestPressureWarnsWithThePartsThatPassedTheirLimits(t *testing.T) {
	t.Run("disk 9 percent free against a 15 limit warns with the disk part", func(t *testing.T) {
		ctx, env, stateDir := pressureFixture(t)
		free, total := diskPressure()
		fakeMachine(t, free, total, true, 0, 0, false)
		stderr := captureStderr(t, func() { core.PressureWarn(ctx, env, "") })
		want := pressureLine(stateDir, sprintfPart(stateDir))
		if stderr != want {
			t.Fatalf("stderr=%q, want %q", stderr, want)
		}
	})

	t.Run("swap 96 percent against an 80 limit warns with the swap part", func(t *testing.T) {
		ctx, env, _ := pressureFixture(t)
		fused, ftotal := swapPressure()
		fakeMachine(t, int64(5)*1024*1024*1024, int64(10)*1024*1024*1024, true, fused, ftotal, true)
		stderr := captureStderr(t, func() { core.PressureWarn(ctx, env, "") })
		want := "soho: resource pressure: " + swapPart + "; run 'soho gc' and close idle panes before opening more\n"
		if stderr != want {
			t.Fatalf("stderr=%q, want %q", stderr, want)
		}
	})

	t.Run("both limits passed warns with both parts", func(t *testing.T) {
		ctx, env, stateDir := pressureFixture(t)
		dfree, dtotal := diskPressure()
		sused, stotal := swapPressure()
		fakeMachine(t, dfree, dtotal, true, sused, stotal, true)
		stderr := captureStderr(t, func() { core.PressureWarn(ctx, env, "") })
		want := pressureLine(stateDir, sprintfPart(stateDir), swapPart)
		if stderr != want {
			t.Fatalf("stderr=%q, want %q", stderr, want)
		}
	})

	t.Run("below the limits, zero thresholds, and a failed measurement never warn", func(t *testing.T) {
		t.Run("below the limits", func(t *testing.T) {
			ctx, env, _ := pressureFixture(t)
			fakeMachine(t, int64(5)*1024*1024*1024, int64(10)*1024*1024*1024, true, int64(5)*1024*1024*1024, int64(10)*1024*1024*1024, true)
			stderr := captureStderr(t, func() { core.PressureWarn(ctx, env, "") })
			if stderr != "" {
				t.Fatalf("stderr=%q, want no warning", stderr)
			}
		})
		t.Run("zero thresholds disable the parts", func(t *testing.T) {
			ctx, env, _ := pressureFixture(t)
			env["SOHO_PRESSURE_DISK_FREE_PERCENT"] = "0"
			env["SOHO_PRESSURE_SWAP_PERCENT"] = "0"
			dfree, dtotal := diskPressure()
			sused, stotal := swapPressure()
			fakeMachine(t, dfree, dtotal, true, sused, stotal, true)
			stderr := captureStderr(t, func() { core.PressureWarn(ctx, env, "") })
			if stderr != "" {
				t.Fatalf("stderr=%q, want no warning with zero thresholds", stderr)
			}
		})
		t.Run("a measurement error is silent", func(t *testing.T) {
			ctx, env, _ := pressureFixture(t)
			fakeMachine(t, 0, 0, false, 0, 0, false)
			stderr := captureStderr(t, func() { core.PressureWarn(ctx, env, "") })
			if stderr != "" {
				t.Fatalf("stderr=%q, want no warning for a failed measurement", stderr)
			}
		})
	})

	t.Run("a zero swap total never warns", func(t *testing.T) {
		ctx, env, _ := pressureFixture(t)
		fakeMachine(t, int64(5)*1024*1024*1024, int64(10)*1024*1024*1024, true, 0, 0, true)
		stderr := captureStderr(t, func() { core.PressureWarn(ctx, env, "") })
		if stderr != "" {
			t.Fatalf("stderr=%q, want no warning for a machine without swap", stderr)
		}
	})

	t.Run("an invalid threshold uses the default with the project warning", func(t *testing.T) {
		ctx, env, stateDir := pressureFixture(t)
		env["SOHO_PRESSURE_DISK_FREE_PERCENT"] = "abc"
		free, total := diskPressure()
		fakeMachine(t, free, total, true, 0, 0, false)
		stderr := captureStderr(t, func() { core.PressureWarn(ctx, env, "") })
		want := "soho: warning: invalid value 'abc' for pressure_disk_free_percent; using the default 15\n" +
			pressureLine(stateDir, sprintfPart(stateDir))
		if stderr != want {
			t.Fatalf("stderr=%q, want %q", stderr, want)
		}
	})
}

func TestPressureNegativeThresholdUsesTheDefault(t *testing.T) {
	ctx, env, stateDir := pressureFixture(t)
	env["SOHO_PRESSURE_SWAP_PERCENT"] = "-1"
	gb := int64(1024 * 1024 * 1024)
	fakeMachine(t, 50*gb, 100*gb, true, 96*gb, 100*gb, true)
	stderr := captureStderr(t, func() { core.PressureWarn(ctx, env, "") })
	want := "soho: warning: invalid value '-1' for pressure_swap_percent; using the default 80\n" +
		pressureLine(stateDir, "swap 96% used (96 GB of 100 GB)")
	if stderr != want {
		t.Fatalf("stderr=%q, want %q (a negative value must not turn the warning off)", stderr, want)
	}
}

func sprintfPart(stateDir string) string {
	return "disk " + stateDir + " has 9% free (9 GB of 100 GB)"
}

func TestPressureThrottleWarnsOncePerTenMinutes(t *testing.T) {
	ctx, env, stateDir := pressureFixture(t)
	dfree, dtotal := diskPressure()
	fakeMachine(t, dfree, dtotal, true, 0, 0, false)
	want := pressureLine(stateDir, sprintfPart(stateDir))
	stamp := filepath.Join(stateDir, "wait", "pressure-warned")

	first := captureStderr(t, func() { core.PressureWarnThrottled(ctx, env, "") })
	if first != want {
		t.Fatalf("first stderr=%q, want %q", first, want)
	}
	if _, err := os.Stat(stamp); err != nil {
		t.Fatalf("the stamp was not written: %v", err)
	}

	second := captureStderr(t, func() { core.PressureWarnThrottled(ctx, env, "") })
	if second != "" {
		t.Fatalf("second stderr=%q, want no warning within the interval", second)
	}

	// A stamp older than the 10 minute interval warns again.
	old := platform.Now().Add(-11 * time.Minute)
	if err := os.Chtimes(stamp, old, old); err != nil {
		t.Fatal(err)
	}
	third := captureStderr(t, func() { core.PressureWarnThrottled(ctx, env, "") })
	if third != want {
		t.Fatalf("third stderr=%q, want the warning after the stale stamp", third)
	}
}

func TestPressureLimitsAreStrictOnEquality(t *testing.T) {
	t.Run("disk free percent equal to the limit does not warn", func(t *testing.T) {
		ctx, env, _ := pressureFixture(t)
		// 15% free against a 15 limit: the limit is strict (free% < limit).
		fakeMachine(t, int64(15)*1024*1024*1024, int64(100)*1024*1024*1024, true, 0, 0, false)
		stderr := captureStderr(t, func() { core.PressureWarn(ctx, env, "") })
		if stderr != "" {
			t.Fatalf("stderr=%q, want no warning at the limit itself", stderr)
		}
	})
	t.Run("swap used percent equal to the limit does not warn", func(t *testing.T) {
		ctx, env, _ := pressureFixture(t)
		// 80% used against an 80 limit: the limit is strict (used% > limit).
		fakeMachine(t, 0, 0, false, int64(80)*1024*1024*1024, int64(100)*1024*1024*1024, true)
		stderr := captureStderr(t, func() { core.PressureWarn(ctx, env, "") })
		if stderr != "" {
			t.Fatalf("stderr=%q, want no warning at the limit itself", stderr)
		}
	})
}

func TestConfigSetRejectsNonIntegerPressureValues(t *testing.T) {
	ctx, env, root := pressureFixture(t)
	for _, key := range []string{"pressure_swap_percent", "pressure_disk_free_percent"} {
		if core.ConfigValueOk(key, "abc", env, root) {
			t.Fatalf("%s: 'abc' accepted, want rejected like the other integer keys", key)
		}
		if !core.ConfigValueOk(key, "80", env, root) {
			t.Fatalf("%s: '80' rejected, want accepted", key)
		}
		code := 0
		func() {
			defer func() {
				if value := recover(); value != nil {
					if e, ok := value.(*platform.ExitError); ok {
						code = e.Code
						return
					}
					panic(value)
				}
			}()
			core.CmdConfigSet([]string{key, "abc"}, ctx, env, root)
		}()
		if code != 2 {
			t.Fatalf("config set %s abc: exit=%d, want 2", key, code)
		}
	}
}

func TestPressureNowriteWritesNothingAndWarnsNothing(t *testing.T) {
	ctx, env, stateDir := pressureFixture(t)
	env["SOHO_NOWRITE"] = "1"
	dfree, dtotal := diskPressure()
	sused, stotal := swapPressure()
	fakeMachine(t, dfree, dtotal, true, sused, stotal, true)

	stderr := captureStderr(t, func() {
		core.PressureWarn(ctx, env, "")
		core.PressureWarnThrottled(ctx, env, "")
	})
	if stderr != "" {
		t.Fatalf("stderr=%q, want no warning under NOWRITE", stderr)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "wait")); !os.IsNotExist(err) {
		t.Fatalf("NOWRITE wrote the wait dir: %v", err)
	}
}

// TestPressureSpawnWarnsAndKeepsTheStdoutJSON drives the real spawn command
// with the fake Herdr/grok of the spawn tests (no real panes): under
// pressure the warning goes to stderr and the stdout JSON stays byte-
// identical to a calm run.
func TestPressureSpawnWarnsAndKeepsTheStdoutJSON(t *testing.T) {
	run := func(t *testing.T, diskFree, diskTotal int64) (stdout, stderr string) {
		root := t.TempDir()
		cwd := filepath.Join(root, "repo")
		state := filepath.Join(root, "state")
		roles := filepath.Join(root, "roles")
		bin := filepath.Join(root, "bin")
		for _, dir := range []string{cwd, state, roles, bin, filepath.Join(root, "skill", "roles"), filepath.Join(state, "ws")} {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(roles, "worker.md"), []byte("---\nkind: grok\nmodel: grok-4.7\neffort: high\napprovals: ask\n---\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		rules := []fakecli.Rule{
			{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`},
			{Argv: []string{"agent", "start"}, ArgvPrefix: true, Stdout: `{"result":{"started":true}}`},
			{Argv: []string{"agent", "get"}, ArgvPrefix: true, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
			{Argv: []string{"agent", "read"}, ArgvPrefix: true, Stdout: "working\n"},
			{Argv: []string{"pane", "list"}, ArgvPrefix: true, Stdout: `{"result":{"panes":[]}}`},
			{Argv: []string{"pane", "layout"}, ArgvPrefix: true, Stdout: `{"result":{"layout":{"area":{"width":1000,"height":700},"panes":[{"pane_id":"p1","rect":{"x":0,"y":0,"width":1000,"height":700}}]}}`},
			{Argv: []string{"pane", "split"}, ArgvPrefix: true, Stdout: `{"result":{"pane":{"pane_id":"p2"}}}`},
		}
		if _, err := fakecli.Install(t, bin, "herdr", rules); err != nil {
			t.Fatal(err)
		}
		if _, err := fakecli.Install(t, bin, "grok", nil); err != nil {
			t.Fatal(err)
		}
		base := testutil.CleanEnv(t)
		env := platform.Env{}
		for _, item := range fakecli.Env(base, bin) {
			key, value, ok := strings.Cut(item, "=")
			if ok {
				env[key] = value
			}
		}
		env["HOME"] = root
		env["USERPROFILE"] = root
		env["SOHO_DIR"] = state
		env["HERDR_WORKSPACE_ID"] = "ws"
		env["SOHO_SKILL_DIR"] = filepath.Join(root, "skill")
		env["SOHO_ROLES"] = roles
		env["SOHO_LANES"] = "off"
		env["HERDR_ENV"] = "1"
		env["HERDR_PANE_ID"] = "p1"
		env["SOHO_WAIT_POLL_MS"] = "1"
		env["SOHO_PRESSURE_DISK_FREE_PERCENT"] = "15"
		env["SOHO_PRESSURE_SWAP_PERCENT"] = "80"
		fakeMachine(t, diskFree, diskTotal, true, 0, 0, false)
		ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
		oldOut, oldErr := platform.Stdout, platform.Stderr
		var out, errOut strings.Builder
		platform.Stdout, platform.Stderr = &out, &errOut
		code := 0
		func() {
			defer func() {
				if value := recover(); value != nil {
					if e, ok := value.(*platform.ExitError); ok {
						code = e.Code
						return
					}
					panic(value)
				}
			}()
			spawn.CmdSpawn([]string{"worker"}, ctx, env, cwd)
		}()
		platform.Stdout, platform.Stderr = oldOut, oldErr
		if code != 0 {
			t.Fatalf("spawn exit=%d stderr=%q", code, errOut.String())
		}
		return out.String(), errOut.String()
	}

	dfree, dtotal := diskPressure()
	outPressure, errPressure := run(t, dfree, dtotal)
	outCalm, errCalm := run(t, int64(50)*1024*1024*1024, int64(100)*1024*1024*1024)
	if outPressure != outCalm {
		t.Fatalf("the pressure warning changed the stdout JSON:\ncalm:\n%s\nunder pressure:\n%s", outCalm, outPressure)
	}
	if !strings.Contains(errPressure, "soho: resource pressure: disk ") {
		t.Fatalf("the spawn stderr has no pressure warning: %q", errPressure)
	}
	if strings.Contains(errCalm, "resource pressure") {
		t.Fatalf("the calm spawn warned: %q", errCalm)
	}
}

func TestPressureWarnAbsentThenExistingState(t *testing.T) {
	ctx, env, stateDir := pressureFixture(t)
	missing := filepath.Join(filepath.Dir(stateDir), "fresh")
	env["HERDR_WORKSPACE_ID"] = "fresh"
	free, total := diskPressure()
	fakeMachine(t, free, total, true, 0, 0, false)
	marker := filepath.Join(missing, "wait", "pressure-warned")
	first := captureStderr(t, func() { core.PressureWarnThrottled(ctx, env, "") })
	if !strings.Contains(first, "resource pressure") {
		t.Fatalf("first=%q", first)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("absent state changed: %v", err)
	}
	if err := os.MkdirAll(missing, 0700); err != nil {
		t.Fatal(err)
	}
	second := captureStderr(t, func() { core.PressureWarnThrottled(ctx, env, "") })
	if !strings.Contains(second, "resource pressure") {
		t.Fatalf("second=%q", second)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("marker: %v", err)
	}
	third := captureStderr(t, func() { core.PressureWarnThrottled(ctx, env, "") })
	if third != "" {
		t.Fatalf("throttle=%q", third)
	}
}
