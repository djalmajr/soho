package core_test

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

func TestCopiesPressureWarningLocations(t *testing.T) {
	ctx, env, state := pressureFixture(t)
	parent := t.TempDir()
	env["TMPDIR"] = t.TempDir()
	env["SOHO_COPIES_DIR"] = parent
	oldDisk, oldDev, oldSwap := platform.DiskFree, platform.DiskDevice, platform.SwapUsage
	t.Cleanup(func() { platform.DiskFree, platform.DiskDevice, platform.SwapUsage = oldDisk, oldDev, oldSwap })
	same, fail := false, false
	var measured []string
	platform.DiskDevice = func(path string) (string, bool) {
		if path == parent && !same {
			return "copies", true
		}
		return "state", true
	}
	platform.DiskFree = func(path string) (int64, int64, bool) {
		measured = append(measured, path)
		if path == parent {
			return 1, 100, !fail
		}
		return 50, 100, true
	}
	platform.SwapUsage = func(platform.Env) (int64, int64, bool) { return 0, 0, false }
	expected := fmt.Sprintf("disk %s (mutation copies) has 1%% free (1 B of 100 B)", parent)
	got := captureStderr(t, func() { core.PressureWarn(ctx, env, filepath.Dir(state)) })
	if !strings.Contains(got, expected) {
		t.Fatalf("warning %q missing %q, measured %v", got, expected, measured)
	}
	if len(measured) != 2 || measured[1] != parent {
		t.Fatalf("measured=%v; configured parent=%s", measured, parent)
	}
	same = true
	measured = nil
	if got = captureStderr(t, func() { core.PressureWarn(ctx, env, "") }); got != "" || len(measured) != 1 {
		t.Fatalf("same device warning=%q measured=%v", got, measured)
	}
	same = false
	fail = true
	if got = captureStderr(t, func() { core.PressureWarn(ctx, env, "") }); got != "" {
		t.Fatalf("failed measurement warning=%q", got)
	}
	fail = false
	platform.DiskDevice = func(path string) (string, bool) {
		if path == parent {
			return "", false
		}
		return "state", true
	}
	if got = captureStderr(t, func() { core.PressureWarn(ctx, env, "") }); got != "" {
		t.Fatalf("failed identity warning=%q", got)
	}
	delete(env, "SOHO_COPIES_DIR")
	fallback := env["TMPDIR"]
	platform.DiskDevice = func(path string) (string, bool) {
		if path == fallback {
			return "copies", true
		}
		return "state", true
	}
	platform.DiskFree = func(path string) (int64, int64, bool) {
		if path == fallback {
			return 1, 100, true
		}
		return 50, 100, true
	}
	if got = captureStderr(t, func() { core.PressureWarn(ctx, env, "") }); !strings.Contains(got, "disk "+fallback+" (mutation copies)") {
		t.Fatalf("TMPDIR fallback warning=%q", got)
	}
}

func TestCopiesDeviceMissingParent(t *testing.T) {
	root := t.TempDir()
	old := platform.DiskDevice
	t.Cleanup(func() { platform.DiskDevice = old })
	platform.DiskDevice = func(path string) (string, bool) {
		if path == root {
			return "volume", true
		}
		return "", false
	}
	got, ok := core.CopiesDevice(filepath.Join(root, "missing", "copies"))
	if !ok || got != "volume" {
		t.Fatalf("device=%q ok=%v for missing parent below %s", got, ok, root)
	}
}
