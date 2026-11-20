package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

// runUnknownWord drives the same entry point the shell uses, with the same
// environment the other unknown-command tests use.
func runUnknownWord(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	oldOut, oldErr := platform.Stdout, platform.Stderr
	platform.Stdout, platform.Stderr = &out, &errOut
	code := Run(args, platform.Env{})
	platform.Stdout, platform.Stderr = oldOut, oldErr
	return code, out.String(), errOut.String()
}

// SOH-406: a mistyped verb keeps exit 2 and the quoted word, and names the
// command that replaces it. The alias map is the source of these exact words.
func TestUnknownCommandSuggestsRetiredVerb(t *testing.T) {
	aliases := []struct{ word, want string }{
		{"stop", "release <name> --close"},
		{"kill", "release <name> --close"},
		{"close", "release <name> --close"},
		{"rm", "release <name> --close"},
		{"start", "spawn <role>"},
		{"open", "spawn <role>"},
		{"new", "spawn <role>"},
		{"send", "dispatch <name> <brief>"},
		{"prompt", "dispatch <name> <brief>"},
		{"ls", "roster"},
		{"list", "roster"},
		{"ps", "roster"},
	}
	for _, tc := range aliases {
		t.Run(tc.word, func(t *testing.T) {
			// send is itself a known command, so the dispatcher never reaches
			// the refusal for it; the shared builder still owns its alias.
			if tc.word == "send" {
				if got := unknownCommandMessage(tc.word); got != "unknown command 'send'; did you mean: "+tc.want {
					t.Fatalf("message=%q", got)
				}
				return
			}
			code, out, errOut := runUnknownWord(t, tc.word, "x")
			want := "soho: unknown command '" + tc.word + "'; did you mean: " + tc.want + "\n"
			if code != 2 || out != "" || errOut != want {
				t.Fatalf("code=%d out=%q err=%q, want %q", code, out, errOut, want)
			}
		})
	}
}

// SOH-406 round 2: a near miss needs at least three characters, distance 1
// at lengths 3-4 and distance 2 above that, with exactly one nearest command.
// A transposition is two edits, so `stauts` (same length as `status`) still
// names status while the shorter `stats` does not win.
func TestUnknownCommandEditDistanceAndHostileWords(t *testing.T) {
	tests := []struct {
		word, want string
	}{
		{"x", "soho: unknown command 'x'\n"},                           // length 2: below the floor
		{"ab", "soho: unknown command 'ab'\n"},                         // length 2
		{"jov", "soho: unknown command 'jov'; did you mean: job\n"},    // length 3, distance 1
		{"wair", "soho: unknown command 'wair'; did you mean: wait\n"}, // length 4, distance 1
		{"rost", "soho: unknown command 'rost'\n"},                     // length 4, distance 2: no
		{"nope", "soho: unknown command 'nope'\n"},                     // length 4, distance 2: no
		{"stauts", "soho: unknown command 'stauts'; did you mean: status\n"},
		{"colelct", "soho: unknown command 'colelct'; did you mean: collect\n"}, // length 7, distance 2
		{"fint", "soho: unknown command 'fint'\n"},                              // tie: find and lint
		{"satus", "soho: unknown command 'satus'\n"},                            // tie: setup and stats
		{"frobnicate", "soho: unknown command 'frobnicate'\n"},
		{"stop;rm -rf /", "soho: unknown command 'stop;rm -rf /'\n"},
		{"STOP", "soho: unknown command 'STOP'\n"},
	}
	for _, tc := range tests {
		t.Run(tc.word, func(t *testing.T) {
			code, out, errOut := runUnknownWord(t, tc.word)
			if code != 2 || out != "" || errOut != tc.want {
				t.Fatalf("code=%d out=%q err=%q, want %q", code, out, errOut, tc.want)
			}
			if strings.Contains(errOut, "did you mean") != strings.Contains(tc.want, "did you mean") {
				t.Fatalf("unexpected suggestion: %q", errOut)
			}
		})
	}
}

// All three refusals share the builder: the form after `help` uses it too.
func TestUnknownCommandAfterHelpSuggests(t *testing.T) {
	code, out, errOut := runUnknownWord(t, "help", "stop")
	if code != 2 || out != "" || errOut != "soho: unknown command 'stop'; did you mean: release <name> --close\n" {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
}
