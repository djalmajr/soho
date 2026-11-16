package stats

import (
	"testing"
	"time"
)

// SOH-306: a --since flag takes a duration back from now as well as a date.
func TestParseSinceFlagDurations(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	for value, want := range map[string]time.Time{
		"12h": now.Add(-12 * time.Hour),
		"1d":  now.Add(-24 * time.Hour),
		"2w":  now.Add(-14 * 24 * time.Hour),
	} {
		got, ok := ParseSinceFlag(value, now)
		if !ok || !got.Equal(want) {
			t.Fatalf("%s: got %v ok=%v; want %v", value, got, ok, want)
		}
	}
	if got, ok := ParseSinceFlag("2026-11-01", now); !ok || got.Year() != 2026 || got.Day() != 1 {
		t.Fatalf("a date must still parse: %v ok=%v", got, ok)
	}
	for _, bad := range []string{"0d", "1m", "-1d", "1.5d", "d", "100000d", "1 d", "1D", ""} {
		if _, ok := ParseSinceFlag(bad, now); ok {
			t.Fatalf("%q must be refused", bad)
		}
	}
}
