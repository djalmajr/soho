package doctor

import (
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestDoctorFootprintWarningText(t *testing.T) {
	t.Run("the exact warning at and above the limit", func(t *testing.T) {
		fp := &core.Footprint{Copies: 2, CopiesBytes: 3 << 30, StateBytes: 1 << 30, ScratchBytes: 1 << 30, Total: 5 << 30}
		want := "footprint: 5.0 GB logical size (copies 3.0 GB in 2, state 1.0 GB, scratch 1.0 GB) is at or above footprint_warn_gb=5; run 'soho gc' or release idle workers with --close. This logical size can be much larger than physical space with clones and hard links."
		if got := doctorFootprintLine(fp, "5"); got != want {
			t.Fatalf("warning=%q\nwant=%q", got, want)
		}
		// Above the limit, with the raw value shown as configured.
		fpAbove := &core.Footprint{Copies: 1, CopiesBytes: 12 << 30, StateBytes: 0, ScratchBytes: 0, Total: 12 << 30}
		if got := doctorFootprintLine(fpAbove, "12"); got == "" || !strings.Contains(got, "is at or above footprint_warn_gb=12; run 'soho gc' or release idle workers with --close") {
			t.Fatalf("above the limit=%q", got)
		}
	})
	t.Run("nothing below the limit", func(t *testing.T) {
		fp := &core.Footprint{Copies: 2, CopiesBytes: 3<<30 - 1, StateBytes: 1 << 30, ScratchBytes: 1 << 30, Total: 5<<30 - 1}
		if got := doctorFootprintLine(fp, "5"); got != "" {
			t.Fatalf("below the limit=%q, want nothing", got)
		}
	})
	t.Run("nothing with 0", func(t *testing.T) {
		fp := &core.Footprint{Total: 100 << 30}
		if got := doctorFootprintLine(fp, "0"); got != "" {
			t.Fatalf("with a zero limit=%q, want nothing", got)
		}
	})
	t.Run("an unreadable limit is off", func(t *testing.T) {
		fp := &core.Footprint{Total: 100 << 30}
		if got := doctorFootprintLine(fp, "abc"); got != "" {
			t.Fatalf("with an unreadable limit=%q, want nothing", got)
		}
	})
}

// doctorFootprintRules answers the doctor's herdr probes minimally.
func doctorFootprintRules() []fakecli.Rule {
	return []fakecli.Rule{
		{Argv: []string{"--version"}, Stdout: "herdr 1.2.3\n"},
		{Argv: []string{"status", "server"}, Stdout: "server 1.2.3\n"},
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`},
		{Argv: []string{"pane", "list", "--workspace", "ws"}, Stdout: `{"result":{"panes":[]}}`},
	}
}

func TestDoctorCheckHasNoFootprintLineBelowTheLimit(t *testing.T) {
	// A tiny team (no copies, no processes, kilobytes of state) never trips
	// the default 5 GB limit: the doctor output does not gain a line.
	env, root := paneContextFixture(t, doctorFootprintRules())
	env["HERDR_WORKSPACE_ID"] = "ws"
	env["TMPDIR"] = t.TempDir() // keep the scratch measurement hermetic
	got := doctorCheckOutput(t, env, root)
	if strings.Contains(got, "footprint:") {
		t.Fatalf("doctor warned on a tiny team:\n%s", got)
	}
}
