package core

import (
	"fmt"
	"github.com/djalmajr/soho/internal/platform"
	"os"
	"path/filepath"
	"testing"
)

func TestRoleLegacyHint(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprint(legacy), func(t *testing.T) {
			env, repo := fixture(t)
			role := "critical-implementer"
			env["SOHO_SKILL_DIR"] = t.TempDir()
			if legacy {
				path := filepath.Join(repo, ".agents", "herdr-roles", role+".md")
				if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
					t.Fatal(e)
				}
				if e := os.WriteFile(path, []byte("must not resolve this legacy role"), 0000); e != nil {
					t.Fatal(e)
				}
			}
			if RoleFile(role, env, repo) != "" {
				t.Fatal("legacy role resolved")
			}
			defer func() {
				e, ok := recover().(*platform.ExitError)
				if !ok {
					t.Fatal("expected exit")
				}
				want := "unknown role 'critical-implementer' (run: soho roles)"
				if legacy {
					want += "; .agents/herdr-roles/critical-implementer.md is the old location: run soho setup --roles to move it to .agents/soho-roles/"
				}
				if e.Code != 3 || e.Msg != want {
					t.Fatalf("exit=%+v want=%s", e, want)
				}
				t.Logf("code=%d message=%s", e.Code, e.Msg)
			}()
			ResolveRole(role, env, repo)
		})
	}
}
