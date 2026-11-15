package doctor

import (
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// nativeCoreStyleFixture is the pane-context fixture with a Core-style
// terminal address in HERDR_PANE_ID and a legacy herdr whose `pane get` fails
// for it, as under native Herdr Core.
func nativeCoreStyleFixture(t *testing.T, transport string) (map[string]string, string) {
	t.Helper()
	env, root := paneContextFixture(t, []fakecli.Rule{
		{Argv: []string{"--version"}, Stdout: "herdr 1.2.3\n"},
		{Argv: []string{"status", "server"}, Stdout: "server 1.2.3\n"},
		{Argv: []string{"pane", "get", "t256"}, Stderr: `{"error":{"code":"pane_not_found","message":"no such pane"}}`, Code: 1},
	})
	env["HERDR_PANE_ID"] = "t256"
	env["HC_TERMINAL_ID"] = "t256"
	env["SOHO_TRANSPORT"] = transport
	if transport == "hc" {
		if _, err := fakecli.Install(t, env["SOHO_FAKECLI_CONFIG"], "hc", []fakecli.Rule{{AnyArgs: true, Stderr: "unavailable", Code: 1}}); err != nil {
			t.Fatal(err)
		}
	}
	return env, root
}

func TestDoctorNativeTransportSkipsLegacyPaneContextWarnings(t *testing.T) {
	env, root := nativeCoreStyleFixture(t, "hc")
	got := doctorCheckOutput(t, env, root)
	if strings.Contains(got, "Herdr context invalid") {
		t.Fatalf("transport=hc must not print the legacy pane-context warning:\n%s", got)
	}
}

func TestDoctorLegacyTransportStillWarnsForTheSameInputs(t *testing.T) {
	env, root := nativeCoreStyleFixture(t, "herdr")
	got := doctorCheckOutput(t, env, root)
	want := "warn   Herdr context invalid: HERDR_PANE_ID=t256 is not a live pane (pane_not_found: no such pane)\n"
	if !strings.HasPrefix(got, want) {
		t.Fatalf("first line=%q, want %q:\n%s", firstLine(got), want, got)
	}
}
