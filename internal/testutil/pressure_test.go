package testutil

import (
	"github.com/djalmajr/soho/internal/platform"
	"testing"
)

func TestSuitePressureDefaultsAndOverrides(t *testing.T) {
	disk, swap := platform.DiskFree, platform.SwapUsage
	t.Cleanup(func() { platform.DiskFree, platform.SwapUsage = disk, swap })
	platform.DiskFree = func(string) (int64, int64, bool) { return 0, 100, true }
	platform.SwapUsage = func(platform.Env) (int64, int64, bool) { return 100, 100, true }
	CalmPressure()
	assertCalm := func() {
		t.Helper()
		if free, total, ok := platform.DiskFree(t.TempDir()); !ok || free != 50 || total != 100 {
			t.Fatalf("suite disk: free=%d total=%d ok=%v; want 50/100, true", free, total, ok)
		}
		if used, total, ok := platform.SwapUsage(platform.Env{}); !ok || used != 0 || total != 100 {
			t.Fatalf("suite swap: used=%d total=%d ok=%v; want 0/100, true", used, total, ok)
		}
	}
	assertCalm()
	t.Run("pressure fixture can override and restore", func(t *testing.T) {
		disk, swap := platform.DiskFree, platform.SwapUsage
		t.Cleanup(func() { platform.DiskFree, platform.SwapUsage = disk, swap })
		platform.DiskFree = func(string) (int64, int64, bool) { return 0, 100, true }
		platform.SwapUsage = func(platform.Env) (int64, int64, bool) { return 100, 100, true }
		if free, _, _ := platform.DiskFree(""); free != 0 {
			t.Fatal("disk override ignored")
		}
		if used, _, _ := platform.SwapUsage(platform.Env{}); used != 100 {
			t.Fatal("swap override ignored")
		}
	})
	assertCalm()
}
