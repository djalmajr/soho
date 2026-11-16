package cli

import "testing"

// SOH-307: help <command> prints what <command> --help prints; an unknown
// command after help is the unknown-command error.
func TestHelpCommandPrintsTheCommandLines(t *testing.T) {
	env, cwd := commandFixture(t)
	for _, command := range []string{"gc", "friction", "spawn", "kinds", "gate", "mutation-guard"} {
		code, out, errOut := runIn(t, []string{"help", command}, env, cwd)
		wantCode, want, _ := runIn(t, []string{command, "--help"}, env, cwd)
		if code != 0 || wantCode != 0 || out != want || out == usage || errOut != "" {
			t.Fatalf("help %s: code=%d out=%q err=%q; want %q", command, code, out, errOut, want)
		}
	}
	code, out, errOut := runIn(t, []string{"help", "nope"}, env, cwd)
	if code != 2 || out != "" || errOut != "soho: unknown command 'nope'\n" {
		t.Fatalf("help nope: code=%d out=%q err=%q", code, out, errOut)
	}
}
