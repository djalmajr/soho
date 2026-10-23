package testutil

import "github.com/djalmajr/soho/internal/platform"

// CalmPressure fixes machine measurements for suites that exercise spawn.
// Call once in TestMain; pressure fixtures may override the existing hooks
// and restore this default with t.Cleanup. Production measurements stay real.
func CalmPressure() {
	platform.DiskFree = func(string) (int64, int64, bool) { return 50, 100, true }
	platform.SwapUsage = func(platform.Env) (int64, int64, bool) { return 0, 100, true }
}
