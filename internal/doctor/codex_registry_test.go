package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

func TestDoctorCodexRegistryPolicy(t *testing.T) {
	const sentinel = "PRIVATE_SENTINEL_MUST_NOT_APPEAR"
	for _, tc := range []struct {
		name, config, mode string
		warn, invalid      bool
	}{
		{name: "drops", config: "[shell_environment_policy]\ninclude_only = [\"PATH\", \"HERDR_*\"]", warn: true},
		{name: "allows", config: "[shell_environment_policy]\ninclude_only = [\"SOHO_*\"]"},
		{name: "lowercase", config: "[shell_environment_policy]\ninclude_only = ['soho_*']"},
		{name: "exact", config: "[shell_environment_policy]\ninclude_only = ['SOHO_REGISTRY_DIR']"},
		{name: "all", config: "[shell_environment_policy]\ninclude_only = ['*']"},
		{name: "multiline", config: "[shell_environment_policy]\ninclude_only = [\n 'HERDR_*', # comment\n \"SOHO_*\",\n]"},
		{name: "empty", config: "[shell_environment_policy]\ninclude_only = []", warn: true},
		{name: "no-table", config: "model = 'unrelated'"},
		{name: "no-key", config: "[shell_environment_policy]\ninherit = 'all'"},
		{name: "missing", mode: "missing"},
		{name: "unreadable-directory", mode: "directory"},
		{name: "no-codex", config: "[shell_environment_policy]\ninclude_only = ['PATH']", mode: "grok"},
		{name: "lane", config: "[shell_environment_policy]\ninclude_only = ['PATH']", mode: "lane", warn: true},
		{name: "fallback-home", config: "[shell_environment_policy]\ninclude_only = ['PATH']", mode: "fallback", warn: true},
		{name: "broken-array", config: "secret = '" + sentinel + "'\n[shell_environment_policy]\ninclude_only = [\"PATH\"", invalid: true},
		{name: "wrong-type", config: "[shell_environment_policy]\ninclude_only = '" + sentinel + "'", invalid: true},
		{name: "missing-comma", config: "[shell_environment_policy]\ninclude_only = ['PATH' '" + sentinel + "']", invalid: true},
		{name: "unrelated-garbage", config: sentinel},
		{name: "TestReview5InvalidUnrelatedValue", config: "token = '" + sentinel + "' trailing-garbage\n[shell_environment_policy]\ninclude_only=['SOHO_*']"},
		{name: "unrelated-garbage-blocked", config: "token = '" + sentinel + "' trailing-garbage\n[shell_environment_policy]\ninclude_only=['HERDR_*']", warn: true},
		{name: "unrelated-missing-value", config: "token =\n[shell_environment_policy]\nother =\ninclude_only=['SOHO_*']"},
		{name: "unrelated-missing-assignment", config: sentinel + "\n[shell_environment_policy]\nother garbage\ninclude_only=['SOHO_*']"},
		{name: "non-string-item", config: "secret = '" + sentinel + "'\n[shell_environment_policy]\ninclude_only = [123]", invalid: true},
		{name: "missing-assignment", config: "[shell_environment_policy]\ninclude_only ['SOHO_*']", invalid: true},
		{name: "duplicate-table", config: "secret = '" + sentinel + "'\n[shell_environment_policy]\ninclude_only=['SOHO_*']\n[shell_environment_policy]", invalid: true},
		{name: "open-literal-multiline", config: "secret = '''" + sentinel + "\n[shell_environment_policy]\ninclude_only=['SOHO_*']", invalid: true},
		{name: "open-basic-multiline", config: "secret = \"\"\"" + sentinel + "\n[shell_environment_policy]\ninclude_only=['SOHO_*']", invalid: true},
		{name: "duplicate-key", config: "[shell_environment_policy]\ninclude_only = ['PATH']\ninclude_only = ['SOHO_*']", invalid: true},
		{name: "quoted-decoy", config: "note = '''\n[shell_environment_policy]\ninclude_only = ['PATH']\n'''"},
		{name: "unrelated-table", config: "[other]\ninclude_only = ['PATH']"},
		{name: "TestReview5R2DottedPolicy", config: "credential='" + sentinel + "'\nshell_environment_policy.include_only=['PATH']", warn: true},
		{name: "dotted-allowed", config: "shell_environment_policy.include_only=['PATH','SOHO_*']"},
		{name: "dotted-quoted-spaces", config: `"shell_environment_policy" . 'include_only' = ['PATH']`, warn: true},
		{name: "dotted-quoted-spaces-allowed", config: `'shell_environment_policy' . "include_only" = ['PATH','SOHO_*']`},
		{name: "quoted-header", config: "[ 'shell_environment_policy' ]\n\"include_only\"=['PATH']", warn: true},
		{name: "quoted-header-allowed", config: "[ \"shell_environment_policy\" ]\n'include_only'=['PATH','SOHO_*']"},
		{name: "inline", config: "shell_environment_policy = { include_only = ['PATH'] }", warn: true},
		{name: "inline-allowed", config: "shell_environment_policy = { include_only = ['PATH','SOHO_*'] }"},
		{name: "inline-quoted", config: `"shell_environment_policy" = { 'include_only' = ['PATH'], set = { private = 'a,b=SECRET' } }`, warn: true},
		{name: "inline-decoy", config: "other = { shell_environment_policy = { include_only = ['PATH'] } }"},
		{name: "dotted-under-other", config: "[other]\nshell_environment_policy.include_only=['PATH']"},
		{name: "quoted-dot-decoy", config: "'shell_environment_policy.include_only'=['PATH']"},
		{name: "array-table-decoy", config: "[[shell_environment_policy]]\ninclude_only=['PATH']"},
		{name: "inline-unclosed", config: "secret='" + sentinel + "'\nshell_environment_policy = { include_only = ['PATH']", invalid: true},
		{name: "inline-ambiguous", config: "shell_environment_policy = { include_only = ['SOHO_*'] broken }", invalid: true},
		{name: "inline-non-string", config: "shell_environment_policy = { include_only = [true] }", invalid: true},
		{name: "duplicate-dotted-header", config: "shell_environment_policy.include_only=['SOHO_*']\n[shell_environment_policy]\ninclude_only=['PATH']", invalid: true},
		{name: "duplicate-dotted-inline", config: "shell_environment_policy.include_only=['SOHO_*']\nshell_environment_policy={include_only=['PATH']}", invalid: true},
		{name: "duplicate-inline-header", config: "shell_environment_policy={include_only=['SOHO_*']}\n[shell_environment_policy]\ninclude_only=['PATH']", invalid: true},
		{name: "duplicate-inline-keys", config: "shell_environment_policy={include_only=['SOHO_*'], 'include_only'=['PATH']}", invalid: true},
		{name: "duplicate-dotted-keys", config: "shell_environment_policy.include_only=['SOHO_*']\n'shell_environment_policy' . \"include_only\"=['PATH']", invalid: true},
		{name: "dotted-then-redeclared-table", config: "shell_environment_policy.include_only=['SOHO_*']\n[shell_environment_policy]\ninherit='all'", invalid: true},
		{name: "sealed-inline-then-dotted-key", config: "shell_environment_policy={inherit='all'}\nshell_environment_policy.include_only=['SOHO_*']", invalid: true},
		{name: "mix-dotted-inline", config: "shell_environment_policy.inherit='all'\nshell_environment_policy={include_only=['SOHO_*']}", invalid: true},
		{name: "mix-inline-header", config: "shell_environment_policy={inherit='all'}\n[shell_environment_policy]\ninclude_only=['SOHO_*']", invalid: true},
		{name: "repeat-inline-without-key", config: "shell_environment_policy={inherit='all'}\nshell_environment_policy={include_only=['SOHO_*']}", invalid: true},
		{name: "dotted-only-many", config: "shell_environment_policy.inherit='all'\nshell_environment_policy.set.token='" + sentinel + "'\nshell_environment_policy.include_only=['SOHO_*']"},
		{name: "header-relative-dotted", config: "[shell_environment_policy]\nset.token='" + sentinel + "'\ninclude_only=['SOHO_*']"},
		{name: "header-then-relative-policy-dotted", config: "[shell_environment_policy]\ninclude_only=['SOHO_*']\nshell_environment_policy.inherit='all'"},
		{name: "header-then-relative-policy-inline", config: "[shell_environment_policy]\ninclude_only=['SOHO_*']\nshell_environment_policy={inherit='all'}"},
		{name: "subtable-then-header", config: "[shell_environment_policy.set]\ntoken='" + sentinel + "'\n[shell_environment_policy]\ninclude_only=['SOHO_*']"},
		{name: "array-table-then-header", config: "[[shell_environment_policy.other]]\ntoken='" + sentinel + "'\n[shell_environment_policy]\ninclude_only=['SOHO_*']"},
		{name: "header-then-subtable", config: "[shell_environment_policy]\ninclude_only=['SOHO_*']\n[shell_environment_policy.set]\ntoken='" + sentinel + "'"},
		{name: "header-then-array-table", config: "[shell_environment_policy]\ninclude_only=['SOHO_*']\n[[shell_environment_policy.other]]\ntoken='" + sentinel + "'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, cwd, ctx := contextWindowDoctorFixture(t)
			env["CODEX_HOME"] = filepath.Join(cwd, "codex home 'quoted'")
			env["TMPDIR"] = t.TempDir()
			env["SOHO_ROLES"] = filepath.Join(cwd, "roles")
			if err := os.MkdirAll(env["SOHO_ROLES"], 0700); err != nil {
				t.Fatal(err)
			}
			kind := "codex"
			if tc.mode == "grok" {
				kind = "grok"
			}
			if err := os.WriteFile(filepath.Join(env["SOHO_ROLES"], "worker.md"), []byte("---\nkind: "+kind+"\n---\nWorker\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if tc.mode == "lane" {
				env["SOHO_LANES"] = "on"
				env["SOHO_LANE_BUILD_ROLES"] = "worker"
				env["SOHO_LANE_BUILD_KIND"] = "codex"
			}
			if tc.mode == "fallback" {
				delete(env, "CODEX_HOME")
			}
			home := env["CODEX_HOME"]
			if home == "" {
				home = filepath.Join(cwd, ".codex")
			}
			if err := os.MkdirAll(home, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(home, "config.toml")
			if tc.mode == "directory" {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			} else if tc.mode != "missing" {
				if err := os.WriteFile(path, []byte(tc.config), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var out, stderr strings.Builder
			oldErr, oldOut := platform.Stderr, platform.Stdout
			platform.Stderr, platform.Stdout = &stderr, &out
			t.Cleanup(func() { platform.Stderr, platform.Stdout = oldErr, oldOut })
			DoctorCheck(ctx, env, cwd, &out)
			got := out.String()
			want := fmt.Sprintf(`codex: [shell_environment_policy] include_only in %s drops SOHO_* variables, so codex workers do not see SOHO_REGISTRY_DIR and register copies and processes in the wrong state dir; add "SOHO_*" to include_only`, path)
			if strings.Contains(got, want) != tc.warn || strings.Contains(got, "codex: invalid TOML in ") != tc.invalid {
				t.Fatalf("case=%s want warning=%v invalid=%v path=%q stdout=%s stderr=%s", tc.name, tc.warn, tc.invalid, path, got, stderr.String())
			}
			if strings.Contains(got+stderr.String(), sentinel) {
				t.Fatal("private config content leaked")
			}
			if tc.mode != "missing" && tc.mode != "directory" {
				after, err := os.ReadFile(path)
				if err != nil || string(after) != tc.config {
					t.Fatal("doctor changed Codex configuration")
				}
			}
		})
	}
}

// TOML cannot switch back to root after a header. Exercise all six state
// transitions directly as well as the reachable document orders above.
func TestPolicyTableIntroductionForms(t *testing.T) {
	forms := []struct {
		name string
		form policyTableForm
	}{{"header", policyHeader}, {"dotted", policyDotted}, {"inline", policyInline}}
	for _, first := range forms {
		for _, second := range forms {
			t.Run(first.name+"-then-"+second.name, func(t *testing.T) {
				var state policyTableForm
				if !state.introduce(first.form) {
					t.Fatal("first introduction refused")
				}
				want := first.form == policyDotted && second.form == policyDotted
				if got := state.introduce(second.form); got != want {
					t.Fatalf("first=%s second=%s allowed=%v want=%v", first.name, second.name, got, want)
				}
				if state != first.form {
					t.Fatal("refused introduction changed the table origin")
				}
			})
		}
	}
}
