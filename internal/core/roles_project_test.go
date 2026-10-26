package core

import (
	"encoding/json"
	"github.com/djalmajr/soho/internal/platform"
	"path/filepath"
	"strings"
	"testing"
)

func TestAudit542RoleBodyExplicitBaseAvoidsSkillLookup(t *testing.T) {
	env, repo := fixture(t)
	delete(env, "SOHO_SKILL_DIR")
	env["SOHO_NOWRITE"] = "1" // An unwanted lookup must not materialize a bundle.
	env["PATH"] = t.TempDir()
	env["SOHO_BIN"] = filepath.Join(repo, "missing-soho")
	base := filepath.Join(repo, "explicit.md")
	write(t, base, "explicit body\n")
	write(t, filepath.Join(repo, ".agents", "soho-roles", "custom.append.md"), "append body\n")
	if body := RoleBodyFor("custom", env, repo, base); body != "explicit body\n\nappend body\n" {
		t.Fatalf("explicit body=%q", body)
	}
	if body := RoleBodyFor("custom", env, repo, ""); body != "" {
		t.Fatalf("explicit empty base fell back: %q", body)
	}
}

func TestAudit542AppendKeepsBaseMetadata(t *testing.T) {
	env, repo := fixture(t)
	env["SOHO_SKILL_DIR"] = t.TempDir()
	env["SOHO_LANES"] = "off"
	env["USERPROFILE"] = env["HOME"]
	env["TMPDIR"] = t.TempDir()
	role := "audit-reader"
	base := filepath.Join(env["SOHO_SKILL_DIR"], "roles", role+".md")
	write(t, base, "---\nname: Base Reader\nkind: codex\nmode: read\ntimeout: 7000\n---\nBASE RULE\n")
	ctx := &Config{Entries: map[string]ConfigEntry{}}
	before := RoleTimeoutMs(role, ctx, env, repo)
	if RoleIsEdit(role, env, repo) || ResolvedRoleKind(role, ctx, env, repo) != "codex" {
		t.Fatal("base fixture metadata was not read")
	}
	extension := filepath.Join(repo, ".agents", "soho-roles", role+".append.md")
	write(t, extension, "---\nname: Ignored Name\nkind: grok\nmode: edit\ntimeout: 1\n---\nAPPEND RULE\n")
	body := RoleBodyFor(role, env, repo)
	out, stderr := capture(t, func() { CmdRolesJSON(env, repo) })
	if RoleFile(role, env, repo) != base || RoleIsEdit(role, env, repo) || ResolvedRoleKind(role, ctx, env, repo) != "codex" || RoleTimeoutMs(role, ctx, env, repo) != before {
		t.Fatal("append changed a metadata consumer")
	}
	if !strings.Contains(body, "BASE RULE\n\nAPPEND RULE") || strings.Contains(body, "Ignored Name") || FmGet(extension, "mode") != "" {
		t.Fatal("append frontmatter entered the body or settings")
	}
	if stderr != "" || !strings.Contains(out, `"mode":"read"`) || !strings.Contains(out, `"source":"skill+append"`) {
		t.Fatalf("roles consumer lost metadata: %s %s", out, stderr)
	}
}

func TestProjectRoleResolutionAndAppend(t *testing.T) {
	for _, tc := range []struct{ name, project, append, source, base string }{
		{"skill", "", "", "skill", "skill body"},
		{"project", "project body", "", "project", "project body"},
		{"skill append", "", "extra body", "skill+append", "skill body"},
		{"project append", "project body", "extra body", "project+append", "project body"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, repo := fixture(t)
			env["SOHO_SKILL_DIR"] = t.TempDir()
			env["USERPROFILE"] = env["HOME"]
			env["TMPDIR"] = t.TempDir()
			base := filepath.Join(env["SOHO_SKILL_DIR"], "roles", "slice-role.md")
			write(t, base, "---\nname: Slice\nmode: edit\n---\nskill body\n")
			project := filepath.Join(repo, ".agents", "soho-roles")
			if tc.project != "" {
				base = filepath.Join(project, "slice-role.md")
				write(t, base, "---\nmode: edit\n---\n"+tc.project+"\n")
			}
			if tc.append != "" {
				write(t, filepath.Join(project, "slice-role.append.md"), "---\nkind: forbidden-metadata\nmode: read\n---\n"+tc.append+"\n")
			}
			if got := RoleFile("slice-role", env, repo); got != base {
				t.Fatalf("base=%s want=%s", got, base)
			}
			body := RoleBodyFor("slice-role", env, repo)
			if !strings.HasPrefix(body, tc.base) {
				t.Fatalf("body=%q", body)
			}
			if tc.append != "" && !strings.Contains(body, tc.base+"\n\n"+tc.append) {
				t.Fatalf("ordered bodies=%q", body)
			}
			if strings.Contains(body, "forbidden-metadata") {
				t.Fatalf("append frontmatter leaked: %q", body)
			}
			out, _ := capture(t, func() { CmdRolesJSON(env, repo) })
			var doc struct {
				Roles []struct{ Role, Source string }
			}
			if err := json.Unmarshal([]byte(out), &doc); err != nil {
				t.Fatal(err)
			}
			if len(doc.Roles) != 1 || doc.Roles[0].Role != "slice-role" || doc.Roles[0].Source != tc.source {
				t.Fatalf("roles=%s", out)
			}
			out, _ = capture(t, func() { CmdRoles(env, repo) })
			if !strings.Contains(out, "source: "+tc.source+";") {
				t.Fatalf("text=%s", out)
			}
		})
	}
}

func TestProjectRoleOldDirectoryAndUnknownAppend(t *testing.T) {
	env, repo := fixture(t)
	write(t, filepath.Join(repo, ".agents", "herdr-roles", "old-only.md"), "old body\n")
	write(t, filepath.Join(repo, ".agents", "soho-roles", "unknown.append.md"), "extra body\n")
	for _, role := range []string{"old-only", "unknown", "unknown.append"} {
		t.Run(role, func(t *testing.T) {
			if file := RoleFile(role, env, repo); file != "" {
				t.Fatalf("unexpected base=%s", file)
			}
			if body := RoleBodyFor(role, env, repo); body != "" {
				t.Fatalf("standalone append body=%q", body)
			}
			defer func() {
				p := recover()
				e, ok := p.(*platform.ExitError)
				if !ok || e.Code != 3 || !strings.Contains(e.Msg, "unknown role '") {
					t.Fatalf("panic=%#v", p)
				}
			}()
			ResolveRole(role, env, repo)
		})
	}
}

func TestProjectRoleAppendOnSOHORoles(t *testing.T) {
	env, repo := fixture(t)
	env["SOHO_ROLES"] = t.TempDir()
	base := filepath.Join(env["SOHO_ROLES"], "slice-role.md")
	write(t, base, "external base\n")
	appendFile := filepath.Join(repo, ".agents", "soho-roles", "slice-role.append.md")
	write(t, appendFile, "---\nmode: edit\n---\nproject addition\n")
	if got := RoleBodyFor("slice-role", env, repo); got != "external base\n\nproject addition\n" {
		t.Fatalf("body=%q", got)
	}
	if got := FmGet(appendFile, "mode"); got != "" {
		t.Fatalf("append metadata=%q", got)
	}
}
