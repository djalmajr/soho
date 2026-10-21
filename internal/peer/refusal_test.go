package peer

import "testing"

func TestRefusalLinePiPrefix(t *testing.T) {
	const id = "01020304"
	marker := "[soho:peer-refused] #" + id + " refused-unsigned"
	for _, tc := range []struct {
		name, line string
		accepted   bool
	}{
		{"error", " Error: " + marker, true},
		{"composer-error", " › Error: " + marker, true},
		{"bare", marker, true},
		{"composer-bare", " › " + marker, true},
		{"quoted", "> Error: " + marker, false},
		{"composer-quoted", "› > Error: " + marker, false},
		{"error-quoted", "Error: > " + marker, false},
		{"warning", "Warning: " + marker, false},
		{"lowercase-error", "error: " + marker, false},
		{"other-prefix", "notice Error: " + marker, false},
		{"repeated-error", "Error: Error: " + marker, false},
		{"other-id", "Error: [soho:peer-refused] #a1020304 refused-unsigned", false},
		{"id-prefix", "Error: [soho:peer-refused] #" + id + "a refused-unsigned", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, accepted := refusalLineIn(tc.line, id)
			want := ""
			if tc.accepted {
				want = "refused-unsigned"
			}
			if accepted != tc.accepted || result != want {
				t.Fatalf("line=%q id=%q accepted=%v result=%q wantAccepted=%v wantResult=%q", tc.line, id, accepted, result, tc.accepted, want)
			}
		})
	}
}
