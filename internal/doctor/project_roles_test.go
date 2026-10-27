package doctor

import (
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDoctorProjectRoles(t *testing.T) {
	for _, tc := range []struct {
		name, role, body       string
		age                    time.Duration
		old, pair, front, warn bool
	}{
		{name: "old directory", old: true, warn: true},
		{name: "stale override", role: "reviewer", body: "changed", age: -time.Hour, warn: true},
		{name: "equal content", role: "reviewer", body: "skill", age: -time.Hour},
		{name: "newer override", role: "reviewer", body: "changed", age: time.Hour},
		{name: "custom role", role: "critical-implementer", body: "custom", age: -time.Hour},
		{name: "redundant pair", role: "reviewer", body: "skill", pair: true, warn: true},
		{name: "append frontmatter", role: "reviewer", front: true, warn: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			skill := t.TempDir()
			now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
			env := platform.Env{"HOME": root, "USERPROFILE": root, "XDG_CONFIG_HOME": root, "TMPDIR": root, "PATH": "", "SOHO_SKILL_DIR": skill, "SOHO_DIR": filepath.Join(root, "state"), "SOHO_LANES": "off"}
			write := func(p, s string) {
				t.Helper()
				if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
					t.Fatal(e)
				}
				if e := os.WriteFile(p, []byte(s), 0600); e != nil {
					t.Fatal(e)
				}
			}
			skillFile := filepath.Join(skill, "roles", "reviewer.md")
			write(skillFile, "skill")
			if e := os.Chtimes(skillFile, now, now); e != nil {
				t.Fatal(e)
			}
			project := filepath.Join(root, ".agents", "soho-roles")
			if tc.body != "" {
				p := filepath.Join(project, tc.role+".md")
				write(p, tc.body)
				when := now.Add(tc.age)
				if e := os.Chtimes(p, when, when); e != nil {
					t.Fatal(e)
				}
			}
			if tc.old {
				if e := os.MkdirAll(filepath.Join(root, ".agents", "herdr-roles"), 0700); e != nil {
					t.Fatal(e)
				}
			}
			if tc.pair || tc.front {
				body := "extra"
				if tc.front {
					body = "---\nkind: ignored\n---\nextra"
				}
				write(filepath.Join(project, tc.role+".append.md"), body)
			}
			var out strings.Builder
			say := &Say{Out: &out}
			doctorProjectRoles(env, root, say)
			if (say.Warn > 0) != tc.warn {
				t.Fatalf("warnings=%d out=%s", say.Warn, out.String())
			}
			if tc.old && !strings.Contains(out.String(), ".agents/herdr-roles/ is the old project roles directory; run soho setup --roles to move it to .agents/soho-roles/") {
				t.Fatal(out.String())
			}
			if tc.name == "stale override" && !strings.Contains(out.String(), "project role reviewer overrides the skill's and is older than it (2026-10-20T11:00:00Z < 2026-10-20T12:00:00Z); diff them or move your additions to .agents/soho-roles/reviewer.append.md") {
				t.Fatal(out.String())
			}
			if tc.pair && !strings.Contains(out.String(), "pair is redundant") {
				t.Fatal(out.String())
			}
			if tc.front && !strings.Contains(out.String(), "frontmatter; it is ignored") {
				t.Fatal(out.String())
			}
			// Exercise the actual doctor consumer as well as the narrow checks.
			out.Reset()
			ctx := core.Config{Entries: map[string]core.ConfigEntry{}}
			DoctorCheck(&ctx, env, root, &out)
			if tc.warn && !strings.Contains(out.String(), map[bool]string{true: "old project roles directory", false: "project role"}[tc.old]) {
				t.Fatalf("DoctorCheck omitted role diagnostic: %s", out.String())
			}
		})
	}
}
