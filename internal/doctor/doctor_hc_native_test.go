package doctor

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// nativeDoctorOutput runs DoctorCheck as cli.Run would for the transport:
// the resolved transport is both a configuration entry and SOHO_TRANSPORT
// (which selects the backend), a fake herdr and a fake hc are on PATH, and
// entries adds project configuration (key -> value). It returns the doctor
// output, the root and the bin directory holding the fakes.
func nativeDoctorOutput(t *testing.T, transport string, entries map[string]string) (string, string) {
	t.Helper()
	bin := t.TempDir()
	if _, err := fakecli.Install(t, bin, "herdr", []fakecli.Rule{
		{Argv: []string{"--version"}, Stdout: "herdr 0.9.1\n"},
		{Argv: []string{"status", "server"}, Stdout: "server 0.9.1\n"},
		{AnyArgs: true},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fakecli.Install(t, bin, "hc", []fakecli.Rule{
		{Argv: []string{"--version", "--json"}, Stdout: "hc 0.1.14\n"},
		{Argv: []string{"server", "status", "--json"}, Stdout: doctorHcStatusJSON + "\n"},
		{Argv: []string{"connect", "--json"}, Stdout: doctorHcHelloOK + "\n"},
	}); err != nil {
		t.Fatal(err)
	}
	env, root := doctorHcEnv(t, bin)
	env["SOHO_TRANSPORT"] = transport
	env["SOHO_SKILL_DIR"] = filepath.Join(root, "skill")
	ctx := doctorHcCtx(transport)
	for key, value := range entries {
		ctx.Entries[key] = core.ConfigEntry{Value: value, Source: "project"}
	}
	var out strings.Builder
	DoctorCheck(ctx, env, root, &out)
	return out.String(), bin
}

// paneAdviceMarkers are fragments of the legacy layout advice: the pane
// count to ask the user for, and the split_max_panes alignment.
var paneAdviceMarkers = []string{
	"Ask the user for 2, 3 or 4 panes",
	"split_max_panes=9 is greater than 1 + max_workers",
	"leaves no room for the whole team",
	"leaves no room next to the caller",
}

// TestDoctorPaneAdviceIsLegacyOnly: on the legacy transport the doctor asks
// for the pane count and aligns split_max_panes; on native Core, where
// workers have no panel, it prints none of that advice.
func TestDoctorPaneAdviceIsLegacyOnly(t *testing.T) {
	cases := []struct {
		name    string
		entries map[string]string
		want    string
	}{
		{"panes not chosen", nil, "Ask the user for 2, 3 or 4 panes"},
		{"split_max_panes above the team", map[string]string{"split_max_panes": "9", "max_workers": "3"}, "split_max_panes=9 is greater than 1 + max_workers"},
		{"split_max_panes below the team", map[string]string{"split_max_panes": "3", "max_workers": "5"}, "leaves no room for the whole team"},
		{"split_max_panes leaves no room", map[string]string{"split_max_panes": "1"}, "leaves no room next to the caller"},
	}
	for _, tc := range cases {
		t.Run("legacy: "+tc.name, func(t *testing.T) {
			out, _ := nativeDoctorOutput(t, "herdr", tc.entries)
			if !strings.Contains(out, tc.want) {
				t.Fatalf("legacy doctor lacks %q:\n%s", tc.want, out)
			}
		})
		t.Run("native: "+tc.name, func(t *testing.T) {
			out, _ := nativeDoctorOutput(t, "hc", tc.entries)
			for _, marker := range paneAdviceMarkers {
				if strings.Contains(out, marker) {
					t.Fatalf("native doctor printed pane advice %q:\n%s", marker, out)
				}
			}
			if strings.Contains(out, "doctor --fix --panes <n>") || strings.Contains(out, "config: panes=") {
				t.Fatalf("native doctor printed a panes line:\n%s", out)
			}
			// The control: the rest of the configuration check still runs.
			if !strings.Contains(out, "config: max_workers=") {
				t.Fatalf("native doctor lost its other config lines:\n%s", out)
			}
		})
	}
}

// TestDoctorHerdrLine: the legacy transport prints the herdr client version
// the probe reports; native Core, whose backend runs no legacy probe, never
// prints a herdr line with an empty version, and its version line is the hc
// client's, with the version.
func TestDoctorHerdrLine(t *testing.T) {
	t.Run("legacy prints the herdr version", func(t *testing.T) {
		out, _ := nativeDoctorOutput(t, "herdr", nil)
		if !strings.Contains(out, "\nok     herdr 0.9.1\n") && !strings.HasPrefix(out, "ok     herdr 0.9.1\n") {
			t.Fatalf("legacy doctor lacks the herdr version line:\n%s", out)
		}
	})
	t.Run("native names the hc client and its version", func(t *testing.T) {
		out, bin := nativeDoctorOutput(t, "hc", nil)
		for _, line := range strings.Split(out, "\n") {
			rest := strings.TrimPrefix(strings.TrimPrefix(line, "ok     "), "warn   ")
			if rest == "herdr" || strings.HasPrefix(rest, "herdr ") {
				t.Fatalf("native doctor printed a herdr client line: %q\n%s", line, out)
			}
		}
		if !strings.Contains(out, "ok     hc 0.1.14, core run2biz running, protocol generation 1\n") {
			t.Fatalf("native doctor lacks the hc version line:\n%s", out)
		}
		if strings.Contains(out, "herdr CLI not in PATH") {
			t.Fatalf("native doctor warned about the herdr CLI:\n%s", out)
		}
		calls, err := fakecli.ReadCalls(filepath.Join(bin, "herdr.calls.jsonl"))
		if err == nil {
			for _, call := range calls {
				if len(call.Argv) > 0 && (call.Argv[0] == "--version" || call.Argv[0] == "status") {
					t.Fatalf("native doctor ran a legacy herdr probe: %v", call.Argv)
				}
			}
		}
	})
	t.Run("native without herdr on PATH does not warn", func(t *testing.T) {
		bin := t.TempDir()
		env, root := doctorHcEnv(t, bin)
		env["SOHO_TRANSPORT"] = "hc"
		env["SOHO_SKILL_DIR"] = filepath.Join(root, "skill")
		var out strings.Builder
		DoctorCheck(doctorHcCtx("hc"), platform.Env(env), root, &out)
		if strings.Contains(out.String(), "herdr CLI not in PATH") {
			t.Fatalf("native doctor warned about the herdr CLI:\n%s", out.String())
		}
	})
}
