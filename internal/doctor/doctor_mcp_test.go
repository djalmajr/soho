package doctor

import (
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

// DJA-693: a role.<role>.mcp or lane.<lane>.mcp file value that is not
// inherit, none or a valid list of server names warns like the other invalid
// configuration values; the existence of the servers is checked only at the
// spawn, against the live listing.

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
