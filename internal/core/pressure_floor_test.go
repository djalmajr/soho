package core_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
)

func TestPressureDiskFloorDecisionTable(t *testing.T) {
	const gib int64 = 1024 * 1024 * 1024
	cases := []struct {
		name                                               string
		free, total                                        int64
		ok                                                 bool
		warnFloor, refuseFloor, warnPercent, refusePercent string
		warn, refuse                                       bool
	}{
		{"large disk", 75, 926, true, "", "", "", "", false, false},
		{"ten gib", 10, 125, true, "", "", "", "", true, false},
		{"thirty gib", 30, 1000, true, "", "", "", "", false, false},
		{"four gib", 4, 125, true, "", "", "", "", true, true},
		{"floor disabled", 30, 1000, true, "0", "0", "", "", true, true},
		{"percent disabled", 4, 125, true, "", "", "0", "0", false, false},
		{"warning floor equal", 20, 1000, true, "", "", "", "", false, false},
		{"refusal floor equal", 5, 1000, true, "", "", "", "", true, false},
		{"warning percent equal", 15, 100, true, "", "", "", "", false, false},
		{"refusal percent equal", 4, 80, true, "", "", "", "", true, false},
		{"zero total", 0, 0, true, "", "", "", "", false, false},
		{"measurement fails", 4, 125, false, "", "", "", "", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, env, _ := pressureFixture(t)
			env["SOHO_PRESSURE_SWAP_PERCENT"] = "0"
			env["SOHO_PRESSURE_REFUSE_SWAP_PERCENT"] = "0"
			for key, val := range map[string]string{"SOHO_PRESSURE_DISK_FREE_MIN_GB": tc.warnFloor, "SOHO_PRESSURE_REFUSE_DISK_FREE_MIN_GB": tc.refuseFloor, "SOHO_PRESSURE_DISK_FREE_PERCENT": tc.warnPercent, "SOHO_PRESSURE_REFUSE_DISK_FREE_PERCENT": tc.refusePercent} {
				if val != "" {
					env[key] = val
				}
			}
			fakeMachine(t, tc.free*gib, tc.total*gib, tc.ok, 0, 0, false)
			line := captureStderr(t, func() { core.PressureWarn(ctx, env, "") })
			if got := strings.Contains(line, "soho: resource pressure: disk "); got != tc.warn {
				t.Fatalf("warning=%q, want warning=%v", line, tc.warn)
			}
			parts := core.PressureRefuseParts(ctx, env, "")
			if got := len(parts) > 0; got != tc.refuse {
				t.Fatalf("refusal=%q, want refusal=%v", parts, tc.refuse)
			}
		})
	}
}

func TestPressureDiskFloorProjectAndEnvironmentPrecedence(t *testing.T) {
	const gib int64 = 1024 * 1024 * 1024
	ctx, env, root := pressureFixture(t)
	if err := os.MkdirAll(filepath.Join(root, ".agents"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".agents", "soho.conf"), []byte("pressure_disk_free_min_gb=+1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	*ctx = core.LoadConfig(env, root)
	env["SOHO_PRESSURE_SWAP_PERCENT"] = "0"
	fakeMachine(t, 10*gib, 125*gib, true, 0, 0, false)
	stderr := captureStderr(t, func() { core.PressureWarn(ctx, env, root) })
	if !strings.HasPrefix(stderr, "soho: warning: invalid value '+1' for pressure_disk_free_min_gb; using the default 20\n") || !strings.Contains(stderr, "soho: resource pressure: disk ") {
		t.Fatalf("project value: %q", stderr)
	}
	env["SOHO_PRESSURE_DISK_FREE_MIN_GB"] = "5"
	stderr = captureStderr(t, func() { core.PressureWarn(ctx, env, root) })
	if stderr != "" {
		t.Fatalf("environment override: %q", stderr)
	}
}

func TestPressureDiskFloorInvalidFallbackAndValidation(t *testing.T) {
	const gib int64 = 1024 * 1024 * 1024
	for _, tc := range []struct {
		key, envKey, fallback string
		refuse                bool
	}{
		{"pressure_disk_free_min_gb", "SOHO_PRESSURE_DISK_FREE_MIN_GB", "20", false},
		{"pressure_refuse_disk_free_min_gb", "SOHO_PRESSURE_REFUSE_DISK_FREE_MIN_GB", "5", true},
	} {
		for _, bad := range []string{"abc", "-1", "+1", "1.5", "1 0", "8589934592", "999999999999999999999999"} {
			t.Run(tc.key+"/"+bad, func(t *testing.T) {
				ctx, env, root := pressureFixture(t)
				if core.ConfigValueOk(tc.key, bad, env, root) {
					t.Fatalf("accepted %q", bad)
				}
				if !core.ConfigValueOk(tc.key, "8589934591", env, root) || !core.ConfigValueOk(tc.key, "0", env, root) {
					t.Fatal("valid boundary rejected")
				}
				env[tc.envKey] = bad
				env["SOHO_PRESSURE_SWAP_PERCENT"] = "0"
				env["SOHO_PRESSURE_REFUSE_SWAP_PERCENT"] = "0"
				fakeMachine(t, 4*gib, 125*gib, true, 0, 0, false)
				stderr := captureStderr(t, func() {
					if tc.refuse {
						if len(core.PressureRefuseParts(ctx, env, "")) != 1 {
							t.Fatal("default did not refuse")
						}
					} else {
						core.PressureWarn(ctx, env, "")
					}
				})
				want := fmt.Sprintf("soho: warning: invalid value '%s' for %s; using the default %s\n", bad, tc.key, tc.fallback)
				if !strings.HasPrefix(stderr, want) {
					t.Fatalf("stderr=%q, want prefix %q", stderr, want)
				}
				if !tc.refuse && !strings.Contains(stderr, "soho: resource pressure: disk ") {
					t.Fatalf("default did not warn: %q", stderr)
				}
			})
		}
	}
}
