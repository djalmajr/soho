package core

import (
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/herdr"
)

func TestCallerGateLineSession(t *testing.T) {
	const base = "HERDR_PANE_ID='w1:p9' is a live pane but this process does not run in it (its process tree does not reach the pane's shell 4000); the id was inherited from another process, such as a forked session. "
	const s = "66bf36b5-9b48-4b58-88a3-5da128ff288b"
	const a = "d630d58c-2c56-4147-9bdb-206cba98b68e"
	res := herdr.CallerCheck{State: herdr.CallerMismatch, Pane: "w1:p9", ShellPID: 4000}
	if got := callerGateLine(res); got != base {
		t.Fatalf("plain mismatch = %q", got)
	}
	// Only S: the sessions sentence needs both.
	res.Session = s
	if got := callerGateLine(res); got != base {
		t.Fatalf("S only = %q", got)
	}
	res.PaneSession = a
	want := base + "This process is Claude Code session " + s + "; the pane hosts session " + a + ", and no continued-in record links them. "
	if got := callerGateLine(res); got != want {
		t.Fatalf("both sessions = %q", got)
	}
	res.ShownIn = "w24:p1"
	want += "This Claude Code session is shown in pane 'w24:p1'; run with HERDR_PANE_ID=w24:p1. "
	if got := callerGateLine(res); got != want {
		t.Fatalf("with suggestion = %q", got)
	}
	// The suggestion stands without the pane's own session (pane get failed).
	res.PaneSession = ""
	if got := callerGateLine(res); got != base+"This Claude Code session is shown in pane 'w24:p1'; run with HERDR_PANE_ID=w24:p1. " {
		t.Fatalf("suggestion only = %q", got)
	}
	if !strings.Contains(callerGateFix, "a Claude Code background session runs with HERDR_PANE_ID set to the pane that shows it") {
		t.Fatalf("callerGateFix = %q", callerGateFix)
	}
}
