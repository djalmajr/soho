package hcbackend

import (
	"context"
	"strings"
	"testing"
	"unicode"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
	"github.com/djalmajr/soho/internal/transport"
)

// neverReady is a terminal record that keeps the readiness poll waiting
// until the startup deadline.
const neverReady = `{"address":"t7","state":"live","reported":false,"status":{"state":"working"}}`

// readRule answers the timeout failure's one screen read of t7.
func readRule(text string) fakecli.Rule {
	return fakecli.Rule{Argv: []string{"read", "t7", "--lines", "0", "--json"}, Stdout: `{"text":` + quoteJSON(text) + `,"complete":true}`}
}

func quoteJSON(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20:
			b.WriteString(`\u00`)
			b.WriteString(string("0123456789abcdef"[r>>4]))
			b.WriteString(string("0123456789abcdef"[r&0xf]))
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// TestStartTimeoutNamesTheLastVisibleLine: when the readiness deadline
// expires the failure message ends with the last non-empty visible line of
// the terminal's screen, read once through hc read before Start returns (so
// before the caller removes the terminal); a failed read or a blank screen
// leaves the message as it was.
func TestStartTimeoutNamesTheLastVisibleLine(t *testing.T) {
	long := strings.Repeat("x", 300)
	for _, tc := range []struct {
		name  string
		rules []fakecli.Rule
		want  string
	}{
		{
			name:  "dialog prompt after blank lines",
			rules: []fakecli.Rule{{Argv: []string{"get", "t7", "--json"}, Stdout: neverReady}, readRule("Do you trust this folder?\n\n  Trust project folder? (y/n)  \n\n   \n")},
			want:  `agent w7 still not ready after 0.3s; last visible line: "Trust project folder? (y/n)"`,
		},
		{
			name:  "the read fails: message unchanged",
			rules: []fakecli.Rule{{Argv: []string{"get", "t7", "--json"}, Stdout: neverReady}, {Argv: []string{"read", "t7", "--lines", "0", "--json"}, Code: 1, Stderr: `{"error":{"kind":"core_unreachable","message":"gone"}}`}},
			want:  "agent w7 still not ready after 0.3s",
		},
		{
			name:  "the read is not a read record: message unchanged",
			rules: []fakecli.Rule{{Argv: []string{"get", "t7", "--json"}, Stdout: neverReady}, {Argv: []string{"read", "t7", "--lines", "0", "--json"}, Stdout: "not json"}},
			want:  "agent w7 still not ready after 0.3s",
		},
		{
			name:  "blank screen: message unchanged",
			rules: []fakecli.Rule{{Argv: []string{"get", "t7", "--json"}, Stdout: neverReady}, readRule(" \n\t\n\r\n\x1b[2J\n")},
			want:  "agent w7 still not ready after 0.3s",
		},
		{
			name:  "an escape-only last line is skipped for the line before it",
			rules: []fakecli.Rule{{Argv: []string{"get", "t7", "--json"}, Stdout: neverReady}, readRule("Claude in Chrome extension detected\n\x1b[0m\x1b[?25l\n")},
			want:  `agent w7 still not ready after 0.3s; last visible line: "Claude in Chrome extension detected"`,
		},
		{
			name:  "ANSI sequences, a carriage return and 300 characters: cleaned and cut",
			rules: []fakecli.Rule{{Argv: []string{"get", "t7", "--json"}, Stdout: neverReady}, readRule("old\n\x1b[1;31mAB\x1b[0m\rC\x1b]0;title\x07D\x1b]8;;http://x\x1b\\" + long + "\n")},
			want:  `agent w7 still not ready after 0.3s; last visible line: "ABCD` + strings.Repeat("x", 196) + `…"`,
		},
		{
			name:  "quotes and backslashes cannot break out of the quoted line",
			rules: []fakecli.Rule{{Argv: []string{"get", "t7", "--json"}, Stdout: neverReady}, readRule("say \"hi\" \\n done\n")},
			want:  `agent w7 still not ready after 0.3s; last visible line: "say \"hi\" \\n done"`,
		},
		{
			name:  "with a get error the order is base, get error, last line",
			rules: []fakecli.Rule{{Argv: []string{"get", "t7", "--json"}, Code: 1, Stderr: `{"error":{"kind":"core_unreachable","message":"connection refused"}}`}, readRule("Trust project folder?\n")},
			want:  `agent w7 still not ready after 0.3s: hc exited 1: core_unreachable: connection refused; last visible line: "Trust project folder?"`,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env, conn, calls := startEnv(t, tc.rules)
			rep := newBackend(env, conn).Start(context.Background(), transport.Start{Name: "w7", Kind: "claude", Timeout: "300"}, transport.Call{})
			if rep.Err == nil || rep.Err.Kind != transport.FailureCommand || rep.Value != "t7" {
				t.Fatalf("rep: %+v want FailureCommand with address t7", rep)
			}
			if rep.Err.Message != tc.want {
				t.Fatalf("message %q want %q", rep.Err.Message, tc.want)
			}
			for _, r := range rep.Err.Message {
				if unicode.IsControl(r) {
					t.Fatalf("message %q holds control character %U", rep.Err.Message, r)
				}
			}
			all := calls()
			reads := 0
			for i, c := range all {
				switch c.Argv[0] {
				case "read":
					reads++
					if i != len(all)-1 {
						t.Fatalf("the read is call %d of %d: it must be the last Core call of the failing Start: %v", i, len(all), all)
					}
				case "send", "keys", "kill", "rm":
					t.Fatalf("Start sent %v: the backend only reads, the caller removes the terminal", c.Argv)
				}
			}
			if reads != 1 {
				t.Fatalf("read calls = %d, want exactly 1: %v", reads, all)
			}
		})
	}
}

// TestStartTimeoutReadsOnlyOnTheDeadlinePath: a start that becomes ready
// never reads the screen.
func TestStartTimeoutReadsOnlyOnTheDeadlinePath(t *testing.T) {
	env, conn, calls := startEnv(t, []fakecli.Rule{
		{Argv: []string{"get", "t7", "--json"}, Call: 1, Stdout: startReadyIdle},
		readRule("should not be read\n"),
	})
	rep := newBackend(env, conn).Start(context.Background(), transport.Start{Name: "w7", Kind: "claude", Timeout: "3000"}, transport.Call{})
	if rep.Err != nil || !rep.Valid {
		t.Fatalf("rep: %+v", rep)
	}
	for _, c := range calls() {
		if c.Argv[0] == "read" {
			t.Fatalf("a ready start read the screen: %v", c.Argv)
		}
	}
}

// TestLastVisibleLineCleaning: the escape and control-character cases the
// screen can hold, one by one.
func TestLastVisibleLineCleaning(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"empty", "", ""},
		{"plain", "a\nb", "b"},
		{"CSI", "\x1b[38;5;196mred\x1b[0m", "red"},
		{"CSI with private marker", "\x1b[?25lx\x1b[?25h", "x"},
		{"8-bit CSI", "\u009b31mred", "red"},
		{"OSC ended by BEL", "\x1b]0;title\x07after", "after"},
		{"OSC ended by ST", "\x1b]8;;http://x\x1b\\link", "link"},
		{"unterminated OSC drops to the end", "keep\x1b]0;title never ends", "keep"},
		{"DCS string", "\x1bPq#0;2;0;0;0\x1b\\after", "after"},
		{"charset designation", "\x1b(Btext", "text"},
		{"lone trailing escape", "text\x1b", "text"},
		{"unterminated CSI", "text\x1b[12", "text"},
		{"carriage return and bell", "a\rb\x07c", "abc"},
		{"tab becomes a space", "a\tb", "a b"},
		{"NUL and DEL", "a\x00b\x7fc", "abc"},
		{"C1 controls", "a\u0085b\u0080c", "abc"},
		{"surrounding whitespace", "   \u00a0 x \t ", "x"},
		{"exactly 200 runes is not cut", strings.Repeat("é", 200), strings.Repeat("é", 200)},
		{"201 runes is cut", strings.Repeat("é", 201), strings.Repeat("é", 200) + "…"},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if got := lastVisibleLine(tc.in); got != tc.want {
				t.Fatalf("lastVisibleLine(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
