package peer

import "testing"

// refusalLineIn accepts both the current refusal marker a new receiver
// prints, `[soho:peer-refused:<id>] <result>`, and the previous
// generation's `[soho:peer-refused] #<id> <result>`, which an older
// receiver still prints: the sender refuses what the receiver refused,
// in either spelling.
func TestRefusalLinePiPrefix(t *testing.T) {
	const id = "01020304"
	for _, tc := range []struct {
		name, line string
		want       string // "" when the line is not the refusal
	}{
		{"previous: error", " Error: [soho:peer-refused] #" + id + " refused-unsigned", "refused-unsigned"},
		{"previous: composer-error", " › Error: [soho:peer-refused] #" + id + " refused-unsigned", "refused-unsigned"},
		{"previous: bare", "[soho:peer-refused] #" + id + " refused-unsigned", "refused-unsigned"},
		{"previous: composer-bare", " › [soho:peer-refused] #" + id + " refused-unsigned", "refused-unsigned"},
		{"previous: bare without a named result", "[soho:peer-refused] #" + id, "refused"},
		{"previous: quoted", "> Error: [soho:peer-refused] #" + id + " refused-unsigned", ""},
		{"previous: composer-quoted", "› > Error: [soho:peer-refused] #" + id + " refused-unsigned", ""},
		{"previous: error-quoted", "Error: > [soho:peer-refused] #" + id + " refused-unsigned", ""},
		{"previous: warning", "Warning: [soho:peer-refused] #" + id + " refused-unsigned", ""},
		{"previous: lowercase-error", "error: [soho:peer-refused] #" + id + " refused-unsigned", ""},
		{"previous: other-prefix", "notice Error: [soho:peer-refused] #" + id + " refused-unsigned", ""},
		{"previous: repeated-error", "Error: Error: [soho:peer-refused] #" + id + " refused-unsigned", ""},
		{"previous: other-id", "Error: [soho:peer-refused] #a1020304 refused-unsigned", ""},
		{"previous: id-prefix", "Error: [soho:peer-refused] #" + id + "a refused-unsigned", ""},
		{"current: error", " Error: [soho:peer-refused:" + id + "] refused-unsigned", "refused-unsigned"},
		{"current: composer-error", " › Error: [soho:peer-refused:" + id + "] refused-unsigned", "refused-unsigned"},
		{"current: bare", "[soho:peer-refused:" + id + "] refused-unsigned", "refused-unsigned"},
		{"current: composer-bare", " › [soho:peer-refused:" + id + "] refused-unsigned", "refused-unsigned"},
		{"current: bare without a named result", "[soho:peer-refused:" + id + "]", "refused"},
		{"current: quoted", "> Error: [soho:peer-refused:" + id + "] refused-unsigned", ""},
		{"current: warning", "Warning: [soho:peer-refused:" + id + "] refused-unsigned", ""},
		{"current: other-id", "Error: [soho:peer-refused:a1020304] refused-unsigned", ""},
		{"current: longer id", "Error: [soho:peer-refused:" + id + "a] refused-unsigned", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, accepted := refusalLineIn(tc.line, id)
			if tc.want == "" && accepted {
				t.Fatalf("line=%q is not this message's refusal, got %q", tc.line, result)
			}
			if tc.want != "" && (!accepted || result != tc.want) {
				t.Fatalf("line=%q accepted=%v result=%q want %q", tc.line, accepted, result, tc.want)
			}
		})
	}
}
