//go:build windows

package gate

import "testing"

func assertReaped(t *testing.T, _ int) { t.Helper() }

func assertEndedBySignal(t *testing.T, res resultRecord, _ string) {
	t.Helper()
	// Windows has no signals: the direct child is terminated and reports a
	// nonzero exit code.
	if res.Signal != "" || res.NativeExit == 0 {
		t.Fatalf("result = %+v", res)
	}
}
