package cli

import (
	"fmt"
	"testing"
)

func TestProcsStopMissingCallerNamesContext(t *testing.T) {
	for _, pane := range []string{"foreign-pane", ""} {
		t.Run("pane-"+pane, func(t *testing.T) {
			f := newProcsFixture(t)
			f.env["HERDR_PANE_ID"] = pane
			code, out, stderr := f.run(t, "procs", "stop", "123456789")
			shown := pane
			if shown == "" {
				shown = "unset"
			}
			want := fmt.Sprintf("procs stop: refusing 123456789: no calling agent in the roster (pane %s, state dir %s)\n", shown, f.state)
			if code != 2 || out != "" || stderr != want {
				t.Fatalf("exit=%d stdout=%q stderr=%q want=%q", code, out, stderr, want)
			}
		})
	}
}
