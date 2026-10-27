package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRoleLegacyHintCLI(t *testing.T) {
	for _, command := range []string{"dispatch", "lint"} {
		for _, legacy := range []bool{false, true} {
			t.Run(command+"/"+map[bool]string{false: "missing", true: "legacy"}[legacy], func(t *testing.T) {
				f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
				role := "critical-implementer"
				if legacy {
					path := filepath.Join(f.root, ".agents", "herdr-roles", role+".md")
					if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
						t.Fatal(e)
					}
					if e := os.WriteFile(path, []byte("legacy role"), 0600); e != nil {
						t.Fatal(e)
					}
				}
				args := []string{"dispatch", "worker", f.brief, "--role", role, "--no-wait"}
				want := "soho: unknown role 'critical-implementer' (run: soho roles)"
				if command == "lint" {
					args = []string{"lint", f.brief, "--role", role}
					want = "soho: lint: unknown role 'critical-implementer'"
				}
				if legacy {
					want += "; .agents/herdr-roles/critical-implementer.md is the old location: run soho setup --roles to move it to .agents/soho-roles/"
				}
				code, out, msg := reopenDispatchRun(t, f.env, args...)
				if code != 3 || out != "" || strings.TrimSuffix(msg, "\n") != want {
					t.Fatalf("exit=%d stdout=%q stderr=%q want=%q", code, out, msg, want)
				}
				t.Logf("exit=%d stderr=%s", code, msg)
			})
		}
	}
}
