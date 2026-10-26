package doctor

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/setuptext"
)

func TestDoctorLocalAndSharedHooks(t *testing.T) {
	for _, tc := range []struct{ name, shared, local, ok, warning string }{
		{name: "local-only", local: setuptext.SetupHookDoctor(), ok: ".claude/settings.local.json"},
		{name: "shared-only", shared: setuptext.SetupHookDoctor(), ok: ".claude/settings.json"},
		{name: "both", shared: setuptext.SetupHookDoctor(), local: setuptext.SetupHookDoctor(), ok: ".claude/settings.json, .claude/settings.local.json"},
		{name: "neither"},
		{name: "old-local", local: setuptext.PreviousHookDoctor(), warning: "old herdr-soho shell hooks in .claude/settings.local.json"},
		{name: "native-shared-old-local", shared: setuptext.SetupHookDoctor(), local: setuptext.PreviousHookDoctor(), ok: ".claude/settings.json", warning: "old herdr-soho shell hooks in .claude/settings.local.json"},
		{name: "old-shared-native-local", shared: setuptext.PreviousHookDoctor(), local: setuptext.SetupHookDoctor(), ok: ".claude/settings.local.json", warning: "old herdr-soho shell hooks in .claude/settings.json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newInboundDoctor(t)
			d.env["TMPDIR"] = t.TempDir()
			d.user("inbound_auth=warn\n")
			d.key("local")
			for file, command := range map[string]string{"settings.json": tc.shared, "settings.local.json": tc.local} {
				if command != "" {
					d.write(filepath.Join(d.cwd, ".claude", file), `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":`+quoteCommand(command)+`}]}],"UserPromptSubmit":[{"hooks":[{"type":"command","command":"soho hook reminder"}]}]}}`, 0600)
				}
			}
			out := d.out()
			if tc.ok != "" && !strings.Contains(out, "ok     Claude hooks present in "+tc.ok+"\n") {
				t.Fatalf("missing OK: %s", out)
			}
			if tc.warning != "" && !strings.Contains(out, tc.warning) {
				t.Fatalf("missing old warning: %s", out)
			}
			if tc.name == "neither" {
				if !strings.Contains(out, "no soho hooks") || !strings.Contains(out, filepath.Join(d.cwd, ".claude/settings.json")+" and "+filepath.Join(d.cwd, ".claude/settings.local.json")+" have no") {
					t.Fatalf("missing warnings: %s", out)
				}
			} else if strings.Contains(out, "no soho hooks") || strings.Contains(out, "do not check peer messages") {
				t.Fatalf("false missing warning: %s", out)
			}
			t.Logf("case=%s", tc.name)
		})
	}
}
