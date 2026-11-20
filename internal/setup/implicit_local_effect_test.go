package setup

import (
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/setuptext"
)

// The doctor asks this predicate in a repository that may be a third-party
// fork. The same four fixtures as the doctor test pin the shared answer.
func TestImplicitLocalSetupInEffect(t *testing.T) {
	for _, tc := range []struct {
		name string
		want bool
		run  func(t *testing.T) (string, *core.Config, platform.Env)
	}{
		{
			name: "nothing tracked with local block",
			want: true,
			run: func(t *testing.T) (string, *core.Config, platform.Env) {
				root, env, ctx := setupWriteUntrackedFixture(t)
				writeLocalSetupFixtureFile(t, root, localInstructionFile, setuptext.SetupBlock())
				return root, ctx, env
			},
		},
		{
			name: "tracked AGENTS.md with the block",
			want: false,
			run: func(t *testing.T) (string, *core.Config, platform.Env) {
				root, env, ctx := setupWriteUntrackedFixture(t)
				writeLocalSetupFixtureFile(t, root, "AGENTS.md", setuptext.SetupBlock())
				gitIn(t, root, "add", "AGENTS.md")
				writeLocalSetupFixtureFile(t, root, localInstructionFile, setuptext.SetupBlock())
				return root, ctx, env
			},
		},
		{
			name: "nothing tracked without a local block",
			want: false,
			run: func(t *testing.T) (string, *core.Config, platform.Env) {
				root, env, ctx := setupWriteUntrackedFixture(t)
				return root, ctx, env
			},
		},
		{
			name: "explicit canonical with nothing tracked and a local block",
			want: false,
			run: func(t *testing.T) (string, *core.Config, platform.Env) {
				root, env, ctx := setupWriteUntrackedFixture(t)
				writeLocalSetupFixtureFile(t, root, localInstructionFile, setuptext.SetupBlock())
				ctx.Entries["setup_target"] = core.ConfigEntry{Value: "canonical", Source: "project"}
				return root, ctx, env
			},
		},
		{
			name: "outside git",
			want: false,
			run: func(t *testing.T) (string, *core.Config, platform.Env) {
				root := t.TempDir()
				ctx := core.Config{Entries: map[string]core.ConfigEntry{}}
				writeLocalSetupFixtureFile(t, root, localInstructionFile, setuptext.SetupBlock())
				return root, &ctx, platform.Env{}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, ctx, env := tc.run(t)
			if got := ImplicitLocalSetupInEffect(root, ctx, env); got != tc.want {
				t.Fatalf("ImplicitLocalSetupInEffect=%t, want %t", got, tc.want)
			}
		})
	}
}

func TestImplicitLocalSetupInEffectExplicitCanonicalEnv(t *testing.T) {
	root, env, ctx := setupWriteUntrackedFixture(t)
	writeLocalSetupFixtureFile(t, root, localInstructionFile, setuptext.SetupBlock())
	env["SOHO_SETUP_TARGET"] = "canonical"
	if ImplicitLocalSetupInEffect(root, ctx, env) {
		t.Fatal("an explicit canonical target in the environment must keep the configured choice")
	}
}
