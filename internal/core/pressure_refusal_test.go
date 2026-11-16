package core_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

// DJA-392: the harder pressure limits (pressure_refuse_disk_free_percent,
// pressure_refuse_swap_percent) turn the spawn warning into a refusal (exit
// 25) before any new pane is opened. These tests cover the parts rule; the
// spawn-side refusal, --force and the golden adaptation are tested in the
// spawn and cli packages.

// refusePartDisk renders the disk part of a 3 GB of 100 GB machine (3%
// free, below the 5 default).
func refusePartDisk(stateDir string) string {
	return "disk " + stateDir + " has 3% free (3 GB of 100 GB)"
}

func TestPressureRefusePartsFollowTheWarningText(t *testing.T) {
	t.Run("disk under the 5 default refuses with the disk part", func(t *testing.T) {
		ctx, env, stateDir := pressureFixture(t)
		fakeMachine(t, int64(3)*1024*1024*1024, int64(100)*1024*1024*1024, true, 0, 0, false)
		parts := core.PressureRefuseParts(ctx, env, "")
		want := []string{refusePartDisk(stateDir)}
		if strings.Join(parts, ";") != strings.Join(want, ";") {
			t.Fatalf("parts=%q, want %q", parts, want)
		}
	})

	t.Run("swap above the 95 default refuses with the swap part", func(t *testing.T) {
		ctx, env, _ := pressureFixture(t)
		sused, stotal := swapPressure()
		fakeMachine(t, int64(90)*1024*1024*1024, int64(100)*1024*1024*1024, true, sused, stotal, true)
		parts := core.PressureRefuseParts(ctx, env, "")
		if len(parts) != 1 || parts[0] != swapPart {
			t.Fatalf("parts=%q, want %q", parts, []string{swapPart})
		}
	})

	t.Run("both limits passed refuse with both parts in the warning order", func(t *testing.T) {
		ctx, env, stateDir := pressureFixture(t)
		sused, stotal := swapPressure()
		fakeMachine(t, int64(3)*1024*1024*1024, int64(100)*1024*1024*1024, true, sused, stotal, true)
		parts := core.PressureRefuseParts(ctx, env, "")
		want := []string{refusePartDisk(stateDir), swapPart}
		if strings.Join(parts, ";") != strings.Join(want, ";") {
			t.Fatalf("parts=%q, want %q", parts, want)
		}
	})

	t.Run("below the refusal limits the warning band never refuses", func(t *testing.T) {
		ctx, env, _ := pressureFixture(t)
		// 9% free is under the 15 warning limit, above the 5 refusal
		// limit; 96% swap is above the 80 warning limit... so lower it into
		// the band: 90% swap warns (80) and does not refuse (95).
		gb := int64(1024 * 1024 * 1024)
		fakeMachine(t, 9*gb, 100*gb, true, 9*gb, 10*gb, true)
		parts := core.PressureRefuseParts(ctx, env, "")
		if len(parts) != 0 {
			t.Fatalf("the warning band refused: %q", parts)
		}
	})

	t.Run("a limit of 0 disables the part even under extreme pressure", func(t *testing.T) {
		ctx, env, _ := pressureFixture(t)
		env["SOHO_PRESSURE_REFUSE_DISK_FREE_PERCENT"] = "0"
		env["SOHO_PRESSURE_REFUSE_SWAP_PERCENT"] = "0"
		dfree, dtotal := diskPressure()
		sused, stotal := swapPressure()
		fakeMachine(t, dfree, dtotal, true, sused, stotal, true)
		parts := core.PressureRefuseParts(ctx, env, "")
		if len(parts) != 0 {
			t.Fatalf("zero limits refused: %q", parts)
		}
	})

	t.Run("equality is strict: 5 percent free and 95 percent swap do not refuse", func(t *testing.T) {
		ctx, env, _ := pressureFixture(t)
		gb := int64(1024 * 1024 * 1024)
		fakeMachine(t, 5*gb, 100*gb, true, 95*gb, 100*gb, true)
		parts := core.PressureRefuseParts(ctx, env, "")
		if len(parts) != 0 {
			t.Fatalf("at the exact limits the machine refused: %q", parts)
		}
	})

	t.Run("a measurement failure never refuses", func(t *testing.T) {
		ctx, env, _ := pressureFixture(t)
		fakeMachine(t, 0, 0, false, 0, 0, false)
		parts := core.PressureRefuseParts(ctx, env, "")
		if len(parts) != 0 {
			t.Fatalf("a failed measurement refused: %q", parts)
		}
	})

	t.Run("a zero swap total never refuses", func(t *testing.T) {
		ctx, env, _ := pressureFixture(t)
		fakeMachine(t, int64(50)*1024*1024*1024, int64(100)*1024*1024*1024, true, 0, 0, true)
		parts := core.PressureRefuseParts(ctx, env, "")
		if len(parts) != 0 {
			t.Fatalf("a machine without swap refused: %q", parts)
		}
	})

	t.Run("an invalid value uses the default with the project warning", func(t *testing.T) {
		ctx, env, _ := pressureFixture(t)
		env["SOHO_PRESSURE_REFUSE_SWAP_PERCENT"] = "abc"
		sused, stotal := swapPressure()
		fakeMachine(t, int64(90)*1024*1024*1024, int64(100)*1024*1024*1024, true, sused, stotal, true)
		stderr := captureStderr(t, func() {
			parts := core.PressureRefuseParts(ctx, env, "")
			if len(parts) != 1 || parts[0] != swapPart {
				t.Fatalf("the invalid value fell back to a limit that does not refuse: %q", parts)
			}
		})
		want := "soho: warning: invalid value 'abc' for pressure_refuse_swap_percent; using the default 95\n"
		if stderr != want {
			t.Fatalf("stderr=%q, want %q", stderr, want)
		}
	})

	t.Run("a negative value uses the default like the warning keys", func(t *testing.T) {
		ctx, env, _ := pressureFixture(t)
		env["SOHO_PRESSURE_REFUSE_DISK_FREE_PERCENT"] = "-1"
		fakeMachine(t, int64(3)*1024*1024*1024, int64(100)*1024*1024*1024, true, 0, 0, false)
		stderr := captureStderr(t, func() {
			parts := core.PressureRefuseParts(ctx, env, "")
			if len(parts) != 1 {
				t.Fatalf("the negative value fell back to a limit that does not refuse: %q", parts)
			}
		})
		if !strings.Contains(stderr, "invalid value '-1' for pressure_refuse_disk_free_percent; using the default 5") {
			t.Fatalf("stderr=%q, want the default fallback warning", stderr)
		}
	})

	t.Run("under SOHO_NOWRITE nothing refuses", func(t *testing.T) {
		ctx, env, _ := pressureFixture(t)
		env["SOHO_NOWRITE"] = "1"
		_, dtotal := diskPressure()
		sused, stotal := swapPressure()
		fakeMachine(t, int64(3)*1024*1024*1024, dtotal, true, sused, stotal, true)
		parts := core.PressureRefuseParts(ctx, env, "")
		if len(parts) != 0 {
			t.Fatalf("NOWRITE refused: %q", parts)
		}
	})

	t.Run("without a workspace there is no state dir to measure", func(t *testing.T) {
		ctx, env, _ := pressureFixture(t)
		env["HERDR_WORKSPACE_ID"] = ""
		_, dtotal := diskPressure()
		fakeMachine(t, int64(3)*1024*1024*1024, dtotal, true, 0, 0, false)
		parts := core.PressureRefuseParts(ctx, env, "")
		if len(parts) != 0 {
			t.Fatalf("no workspace refused: %q", parts)
		}
	})
}

func TestPressureRefusalKeysFollowSwapPercentInTheScalarList(t *testing.T) {
	index := -1
	for i, key := range core.ConfigScalarKeys {
		if key == "pressure_swap_percent" {
			index = i
		}
	}
	if index < 0 || index+3 >= len(core.ConfigScalarKeys) ||
		core.ConfigScalarKeys[index+1] != "pressure_refuse_disk_free_percent" ||
		core.ConfigScalarKeys[index+2] != "pressure_refuse_disk_free_min_gb" ||
		core.ConfigScalarKeys[index+3] != "pressure_refuse_swap_percent" {
		t.Fatalf("the refusal keys do not follow pressure_swap_percent: %v", core.ConfigScalarKeys)
	}
}

// The new keys validate exactly like the pressure_* keys of today: a
// non-negative decimal integer only. 90 and 0 set and reach the file; -1
// and abc refuse with exit 2 and leave the file alone.
func TestConfigSetPressureRefusalValues(t *testing.T) {
	set := func(t *testing.T, ctx *core.Config, env platform.Env, cwd, key, value string) int {
		t.Helper()
		code := 0
		func() {
			defer func() {
				if v := recover(); v != nil {
					if e, ok := v.(*platform.ExitError); ok {
						code = e.Code
						return
					}
					panic(v)
				}
			}()
			core.CmdConfigSet([]string{key, value}, ctx, env, cwd)
		}()
		return code
	}

	ctx, env, root := pressureFixture(t)
	for _, key := range []string{"pressure_refuse_swap_percent", "pressure_refuse_disk_free_percent"} {
		if !core.ConfigValueOk(key, "90", env, root) {
			t.Fatalf("%s: '90' rejected, want accepted", key)
		}
		for _, value := range []string{"-1", "abc", "1.5"} {
			if core.ConfigValueOk(key, value, env, root) {
				t.Fatalf("%s: %q accepted, want rejected like the pressure_* keys", key, value)
			}
		}
	}

	if code := set(t, ctx, env, root, "pressure_refuse_swap_percent", "90"); code != 0 {
		t.Fatalf("config set 90: exit=%d, want 0", code)
	}
	file := filepath.Join(root, ".agents", "soho.conf")
	content, err := os.ReadFile(file)
	if err != nil || string(content) != "pressure_refuse_swap_percent=90\n" {
		t.Fatalf("file=%q err=%v, want the key at 90", content, err)
	}
	if code := set(t, ctx, env, root, "pressure_refuse_swap_percent", "0"); code != 0 {
		t.Fatalf("config set 0: exit=%d, want 0", code)
	}
	content, err = os.ReadFile(file)
	if err != nil || string(content) != "pressure_refuse_swap_percent=0\n" {
		t.Fatalf("file=%q err=%v, want the key at 0", content, err)
	}
	if code := set(t, ctx, env, root, "pressure_refuse_swap_percent", "-1"); code != 2 {
		t.Fatalf("config set -1: exit=%d, want 2", code)
	}
	if code := set(t, ctx, env, root, "pressure_refuse_swap_percent", "abc"); code != 2 {
		t.Fatalf("config set abc: exit=%d, want 2", code)
	}
	content, err = os.ReadFile(file)
	if err != nil || string(content) != "pressure_refuse_swap_percent=0\n" {
		t.Fatalf("the refusals rewrote the file: %q err=%v", content, err)
	}
}
