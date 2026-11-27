package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// DJA-693: a role.<role>.mcp or lane.<lane>.mcp file value that is not
// inherit, none or a valid list of server names warns like the other invalid
// configuration values; the existence of the servers is checked only at the
// spawn, against the live listing.

// #282: a legacy doctor can only see its own pi; a negative is a warning,
// never a spawn refusal and never a change to the recorded MCP choice.
func TestDoctorPiCapabilityWarningScope(t *testing.T) {
	piEnv := func(t *testing.T, help string, transport string) platform.Env {
		t.Helper()
		bin := t.TempDir()
		if _, err := fakecli.Install(t, bin, "pi", []fakecli.Rule{
			{Argv: []string{"--help"}, Stdout: help},
			{AnyArgs: true},
		}); err != nil {
			t.Fatal(err)
		}
		env := platform.Env{"PATH": bin, "SOHO_FAKECLI_CONFIG": bin, "TMPDIR": t.TempDir()}
		if transport != "" {
			env["SOHO_TRANSPORT"] = transport
		}
		env["SOHO_LANE_BUILDERS_KIND"] = "pi"
		return env
	}
	const oldPi = "Usage: pi [options]\n  --model <id>  Choose a model\n"
	const newPi = "Usage: pi [options]\n  --no-mcp      Disable MCP servers\n"

	for _, tc := range []struct {
		name, help, transport string
		want                  bool
	}{
		{"legacy old pi warns", oldPi, "", true},
		{"legacy new pi stays quiet", newPi, "", false},
		{"native core has no legacy warning", oldPi, "hc", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out strings.Builder
			doctorPiCapabilityWarning(piEnv(t, tc.help, tc.transport), t.TempDir(), &Say{Out: &out})
			got := strings.Contains(out.String(), "does not document --no-mcp")
			if got != tc.want {
				t.Fatalf("warning=%q, want %t", out.String(), tc.want)
			}
		})
	}
}

// The advisory must reach the real doctor entry point, not only the helper.
func TestDoctorCheckPiCapabilityAdvisory(t *testing.T) {
	env, root := paneContextFixture(t, nil)
	env["SOHO_LANE_BUILDERS_KIND"] = "pi"
	bin := env.Get("SOHO_FAKECLI_CONFIG")
	if _, err := fakecli.Install(t, bin, "pi", []fakecli.Rule{
		{Argv: []string{"--help"}, Stdout: "Usage: pi [options]\n  --model <id>  Choose a model\n"},
		{AnyArgs: true},
	}); err != nil {
		t.Fatal(err)
	}
	out := doctorCheckOutput(t, env, root)
	if !strings.Contains(out, "does not document --no-mcp") {
		t.Fatalf("doctor did not carry the capability advisory: %s", out)
	}
	if got := piHelpCalls(t, env.Get("SOHO_FAKECLI_CONFIG")); got != 1 {
		t.Fatalf("pi --help calls=%d, want one bounded probe", got)
	}

	// A project that effectively uses only Codex/Claude must not run Pi.
	other, otherRoot := paneContextFixture(t, nil)
	otherBin := other.Get("SOHO_FAKECLI_CONFIG")
	if _, err := fakecli.Install(t, otherBin, "pi", []fakecli.Rule{
		{Argv: []string{"--help"}, Stdout: "Usage: pi [options]\n  --model <id>  Choose a model\n"},
		{AnyArgs: true},
	}); err != nil {
		t.Fatal(err)
	}
	other["SOHO_LANE_BUILDERS_KIND"] = "codex"
	out = doctorCheckOutput(t, other, otherRoot)
	if strings.Contains(out, "does not document --no-mcp") {
		t.Fatalf("non-Pi doctor printed the capability advisory: %s", out)
	}
	if got := piHelpCalls(t, otherBin); got != 0 {
		t.Fatalf("non-Pi doctor made %d pi --help calls, want none", got)
	}

	// Lanes off: a configured Pi documenter can still spawn with the role's
	// default mcp: none, so it must receive the same one-shot advice.
	docEnv, docRoot := paneContextFixture(t, nil)
	docBin := docEnv.Get("SOHO_FAKECLI_CONFIG")
	if _, err := fakecli.Install(t, docBin, "pi", []fakecli.Rule{
		{Argv: []string{"--help"}, Stdout: "Usage: pi [options]\n  --model <id>  Choose a model\n"},
		{AnyArgs: true},
	}); err != nil {
		t.Fatal(err)
	}
	docEnv["SOHO_LANES"] = "off"
	docEnv["SOHO_ROLE_DOCUMENTER_KIND"] = "pi"
	delete(docEnv, "SOHO_LANE_BUILDERS_KIND")
	out = doctorCheckOutput(t, docEnv, docRoot)
	if !strings.Contains(out, "does not document --no-mcp") {
		t.Fatalf("lanes-off Pi documenter did not receive the advisory: %s", out)
	}
	if got := piHelpCalls(t, docBin); got != 1 {
		t.Fatalf("documenter case made %d pi --help calls, want one", got)
	}
}

func piHelpCalls(t *testing.T, configDir string) int {
	t.Helper()
	calls, err := fakecli.ReadCalls(filepath.Join(configDir, "pi.calls.jsonl"))
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, call := range calls {
		if len(call.Argv) == 1 && call.Argv[0] == "--help" {
			n++
		}
	}
	return n
}

func TestDoctorMcpInvalidValue(t *testing.T) {
	t.Run("an invalid role mcp value warns with the detail", func(t *testing.T) {
		ctx := core.Config{Entries: map[string]core.ConfigEntry{
			"role_worker_mcp": {Value: "a b", Source: "project", Original: "role.worker.mcp"},
		}}
		var out strings.Builder
		doctorMcp(&ctx, platform.Env{}, &Say{Out: &out})
		want := "config: role.worker.mcp=a b: 'a b' is not a server name (^[A-Za-z0-9_-]{1,64}$)"
		if !strings.Contains(out.String(), want) {
			t.Fatalf("the exact warning is missing:\n%s", out.String())
		}
	})

	t.Run("an invalid lane mcp value warns", func(t *testing.T) {
		ctx := core.Config{Entries: map[string]core.ConfigEntry{
			"lane_build_mcp": {Value: ",", Source: "project", Original: "lane.build.mcp"},
		}}
		var out strings.Builder
		doctorMcp(&ctx, platform.Env{}, &Say{Out: &out})
		want := "config: lane.build.mcp=,: empty name in the list"
		if !strings.Contains(out.String(), want) {
			t.Fatalf("the exact warning is missing:\n%s", out.String())
		}
	})

	t.Run("inherit, none and a valid list stay silent", func(t *testing.T) {
		for _, value := range []string{"inherit", "none", "a,b", "playwright,chrome-devtools"} {
			ctx := core.Config{Entries: map[string]core.ConfigEntry{
				"role_worker_mcp": {Value: value, Source: "project", Original: "role.worker.mcp"},
			}}
			var out strings.Builder
			doctorMcp(&ctx, platform.Env{}, &Say{Out: &out})
			if out.Len() != 0 {
				t.Fatalf("value %q warned: %s", value, out.String())
			}
		}
	})

	t.Run("an environment-only value does not warn (the spawn exits 2 for it)", func(t *testing.T) {
		ctx := core.Config{Entries: map[string]core.ConfigEntry{}}
		var out strings.Builder
		doctorMcp(&ctx, platform.Env{"SOHO_ROLE_WORKER_MCP": "a b"}, &Say{Out: &out})
		if out.Len() != 0 {
			t.Fatalf("an env-only value warned: %s", out.String())
		}
	})

	t.Run("the check reads the effective value through Cfg", func(t *testing.T) {
		ctx := core.Config{Entries: map[string]core.ConfigEntry{
			"role_worker_mcp": {Value: "a,b", Source: "session", Original: "role.worker.mcp"},
		}}
		var out strings.Builder
		doctorMcp(&ctx, platform.Env{}, &Say{Out: &out})
		if out.Len() != 0 {
			t.Fatalf("the effective value is valid and warned: %s", out.String())
		}
	})
}

// An empty value is no choice: the spawn skips that layer, so the doctor has
// nothing to warn about. A value it does warn about never reaches the terminal
// with control characters.
func TestDoctorMcpEmptyAndFilteredValues(t *testing.T) {
	t.Run("an empty value does not warn", func(t *testing.T) {
		ctx := core.Config{Entries: map[string]core.ConfigEntry{
			"role_worker_mcp": {Value: "", Source: "project", Original: "role.worker.mcp"},
		}}
		var out strings.Builder
		doctorMcp(&ctx, platform.Env{}, &Say{Out: &out})
		if out.String() != "" {
			t.Fatalf("an empty value warned:\n%s", out.String())
		}
	})
	t.Run("the original key is filtered too", func(t *testing.T) {
		ctx := core.Config{Entries: map[string]core.ConfigEntry{
			"role_worker_mcp": {Value: "a b", Source: "project", Original: "role.\x1b[31mworker.mcp"},
		}}
		var out strings.Builder
		doctorMcp(&ctx, platform.Env{}, &Say{Out: &out})
		if out.String() == "" || strings.ContainsRune(out.String(), 0x1b) {
			t.Fatalf("output=%q", out.String())
		}
	})
	t.Run("a key without a role or lane name is not an mcp choice", func(t *testing.T) {
		ctx := core.Config{Entries: map[string]core.ConfigEntry{
			"role_mcp": {Value: "a b", Source: "project", Original: "role.mcp"},
			"lane_mcp": {Value: "a b", Source: "project", Original: "lane.mcp"},
		}}
		var out strings.Builder
		doctorMcp(&ctx, platform.Env{}, &Say{Out: &out})
		if out.String() != "" {
			t.Fatalf("output=%q", out.String())
		}
	})
	t.Run("the warning filters control characters", func(t *testing.T) {
		ctx := core.Config{Entries: map[string]core.ConfigEntry{
			"role_worker_mcp": {Value: "a\x1b[31mb", Source: "project", Original: "role.worker.mcp"},
		}}
		var out strings.Builder
		doctorMcp(&ctx, platform.Env{}, &Say{Out: &out})
		if !strings.Contains(out.String(), "config: role.worker.mcp=") || strings.ContainsRune(out.String(), 0x1b) {
			t.Fatalf("output=%q", out.String())
		}
	})
}
