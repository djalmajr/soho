package cli

import (
	"fmt"
	"strings"

	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/kinds"
	"github.com/djalmajr/soho/internal/platform"
)

func cmdModels(argv []string, env platform.Env) int {
	if len(argv) == 0 || argv[0] == "" {
		_, _ = fmt.Fprintln(platform.Stderr, "soho: 1: kind")
		return 1
	}
	ids := kinds.ModelIDs(argv[0], env)
	sorted := kinds.VersionSortDesc(ids)
	if len(sorted) == 0 {
		_, _ = fmt.Fprintln(platform.Stdout)
		return 0
	}
	for _, id := range sorted {
		_, _ = fmt.Fprintln(platform.Stdout, id)
	}
	return 0
}

func cmdModel(argv []string, env platform.Env) int {
	kind, spec, effort := "", "", ""
	if len(argv) > 0 {
		kind = argv[0]
	}
	if len(argv) > 1 {
		spec = argv[1]
	}
	if len(argv) > 2 {
		effort = argv[2]
	}
	if kind == "" {
		_, _ = fmt.Fprintln(platform.Stderr, "soho: 1: kind")
		return 1
	}
	if spec == "" {
		_, _ = fmt.Fprintln(platform.Stderr, "soho: 2: spec")
		return 1
	}
	model, err := kinds.ResolveModel(kind, spec, effort, env, func(string) {})
	if err != nil {
		if exit, ok := err.(*platform.ExitError); ok {
			if exit.Friction {
				platform.DieFriction(exit.Msg, exit.Code)
			}
			platform.Die(exit.Msg, exit.Code)
		}
		panic(err)
	}
	// The same shared resolver the launch path uses, with the session's
	// explicit inline configuration and configuration directory consumed
	// from the command's environment (no ambient process read and no
	// default). A profile or configuration refusal prints the resolver's
	// fixed value-free error with the normal soho prefix and returns 2:
	// no panic and no raw configuration echo. Every non-native
	// combination ignores both inputs and resolves to the existing
	// KindModelArgs output with no profile name, so its JSON stays exact.
	profile, err := kinds.ResolveLaunchProfileWithConfigDir(kind, model, effort, env.Get("SOHO_TRANSPORT"), env.Get("OPENCODE_CONFIG_CONTENT"), env.Get("OPENCODE_CONFIG_DIR"), func(string) {})
	if err != nil {
		_, _ = fmt.Fprintln(platform.Stderr, "soho: "+err.Error())
		return 2
	}
	args := make([]string, 0, len(profile.ModelArgs)+len(profile.ExtraArgs)+4)
	args = append(args, profile.ModelArgs...)
	args = append(args, profile.ExtraArgs...)
	args = append(args, kinds.KindEffortArgs(kind, effort, model, env, func(string) {})...)
	ceiling := kinds.KindEffortCeiling(kind)
	if kind == "codex" {
		ceiling = kinds.CodexEffortCeiling(model, env)
	}
	fields := []any{
		"kind", kind,
		"spec", spec,
		"effort", effort,
		"model", model,
		"family", kinds.AgentFamily(kind, model),
		"effort_ceiling", ceiling,
		"agent_args", strings.Join(args, " "),
	}
	// Only the public profile name joins the output, and only when one was
	// resolved: never the environment overrides, the inline configuration
	// or a configuration directory.
	if profile.Name != "" {
		fields = append(fields, "launch_profile", profile.Name)
	}
	output := jsonjs.O(fields...)
	_, _ = platform.Stdout.Write([]byte(jsonjs.StringifyIndent(output, 2) + "\n"))
	return 0
}
