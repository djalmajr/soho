package launchprofile

import "testing"

func TestResolveTUIProfile(t *testing.T) {
	for raw, want := range map[string]string{"": "legacy", "legacy": "legacy", "v2": "v2"} {
		got, err := ResolveTUIProfile(raw)
		if got != want || err != nil {
			t.Fatalf("input %q: got %q, %v; want %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{string([]byte{0xff}), "\x00", " ", " v2", "v2 ", "Legacy", "V2", "v2\n", "v2\r", "v2\t", "v2\x1b", "\u00a0v2", "\ufffd", "synthetic-private-canary", "v2#canary"} {
		got, err := ResolveTUIProfile(raw)
		if got != "" || err != ErrInvalidTUIProfile || err.Error() != "launch profile: OpenCode TUI profile is invalid" {
			t.Fatalf("input %q: got %q, %v; want empty and fixed sentinel", raw, got, err)
		}
	}
}
