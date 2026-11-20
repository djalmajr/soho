package doctor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/setuptext"
)

func implicitLocalRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved // git reports the resolved spelling on macOS
	}
	if out, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %s: %v", out, err)
	}
	return root
}

func implicitLocalEnv(t *testing.T, root string) platform.Env {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("git is required for the doctor local setup test: %v", err)
	}
	return platform.Env{
		"HOME": root, "USERPROFILE": root,
		"SOHO_SKILL_DIR": root, "SOHO_DIR": t.TempDir(),
		"PATH": filepath.Dir(git),
	}
}

func runImplicitLocalDoctor(t *testing.T, root string, env platform.Env, ctx *core.Config) string {
	t.Helper()
	var out strings.Builder
	DoctorCheck(ctx, env, root, &out)
	return out.String()
}

func implicitLocalHooksLine(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "no soho hooks in .claude/settings.json") {
			return line
		}
	}
	return ""
}

// A local setup written because nothing is tracked counts as local for the
// doctor's third-party checks (SOH-463). Each case starts its own repository.
func TestDoctorImplicitLocalSetup(t *testing.T) {
	const validationWarn = "warn   no VALIDATION.md in the project root"
	for _, tc := range []struct {
		name, hooksArg string
		wantWarnings   bool
		setup          func(t *testing.T, root string) *core.Config
	}{
		{
			name:         "nothing tracked with the local block",
			hooksArg:     "setup --local",
			wantWarnings: false,
			setup: func(t *testing.T, root string) *core.Config {
				if err := os.WriteFile(filepath.Join(root, "CLAUDE.local.md"), []byte(setuptext.SetupBlock()), 0o600); err != nil {
					t.Fatal(err)
				}
				return &core.Config{Entries: map[string]core.ConfigEntry{}}
			},
		},
		{
			name:         "tracked AGENTS.md with the block",
			hooksArg:     "setup'",
			wantWarnings: true,
			setup: func(t *testing.T, root string) *core.Config {
				if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte(setuptext.SetupBlock()), 0o600); err != nil {
					t.Fatal(err)
				}
				if out, err := exec.Command("git", "-C", root, "add", "AGENTS.md").CombinedOutput(); err != nil {
					t.Fatalf("git add: %s: %v", out, err)
				}
				return &core.Config{Entries: map[string]core.ConfigEntry{}}
			},
		},
		{
			name:         "nothing tracked without a local block",
			hooksArg:     "setup'",
			wantWarnings: true,
			setup: func(t *testing.T, root string) *core.Config {
				return &core.Config{Entries: map[string]core.ConfigEntry{}}
			},
		},
		{
			name:         "explicit canonical with nothing tracked and a local block",
			hooksArg:     "setup --local",
			wantWarnings: true,
			setup: func(t *testing.T, root string) *core.Config {
				if err := os.WriteFile(filepath.Join(root, "CLAUDE.local.md"), []byte(setuptext.SetupBlock()), 0o600); err != nil {
					t.Fatal(err)
				}
				return &core.Config{Entries: map[string]core.ConfigEntry{
					"setup_target": {Value: "canonical", Source: "project"},
				}}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := implicitLocalRoot(t)
			ctx := tc.setup(t, root)
			// A malformed DESIGN.md makes the design warning observable.
			if err := os.WriteFile(filepath.Join(root, "DESIGN.md"), []byte("# Acme\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			out := runImplicitLocalDoctor(t, root, implicitLocalEnv(t, root), ctx)
			hasValidation := strings.Contains(out, validationWarn)
			hasDesign := strings.Contains(out, "warn   DESIGN.md has no front matter")
			if hasValidation != tc.wantWarnings || hasDesign != tc.wantWarnings {
				t.Fatalf("validation=%t design=%t, want both %t:\n%s", hasValidation, hasDesign, tc.wantWarnings, out)
			}
			hooks := implicitLocalHooksLine(out)
			if !strings.Contains(hooks, tc.hooksArg) {
				t.Fatalf("hooks warning %q does not contain %q:\n%s", hooks, tc.hooksArg, out)
			}
		})
	}
}
