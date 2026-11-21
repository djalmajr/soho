package core

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/djalmajr/soho/internal/platform"
)

type RoleFlags struct{ Kind, Model, Effort, Approvals string }

// RoleSettings describes the effective values before CLI-specific normalization.
type RoleSettings struct {
	Lane                      string
	Kind                      string
	KindFrom                  string
	ModelSpec                 string
	ModelFrom                 string
	Effort                    string
	EffortFrom                string
	Approvals                 string
	ApprovalsFrom             string
	LaneModels                []string // Populated only when the lane list supplies ModelSpec.
	DiscardedFrontmatterModel string   // Grouped role model excluded by a different effective kind.
}

// ResolvedRoleKind reads a role's own kind from configuration or frontmatter.
// Call ResolveRoleSettings for a worker that can inherit lane defaults.
func ResolvedRoleKind(role string, ctx *Config, env platform.Env, cwd string) string {
	key := "role_" + strings.ReplaceAll(role, "-", "_") + "_kind"
	if value := Cfg(ctx, key, "", env); value != "" {
		return value
	}
	if file := RoleFile(role, env, cwd); file != "" {
		return FmGet(file, "kind")
	}
	return ""
}

// roleOrLaneSetting resolves one configured override before any fallback. The
// role and lane keys each retain their normal configuration layer precedence.
func roleOrLaneSetting(role, lane, attr string, ctx *Config, env platform.Env) (string, string) {
	if value, from := roleSetting(role, attr, ctx, env); value != "" {
		return value, from
	}
	if lane != "" {
		key := LaneKey(lane, attr)
		if value := Cfg(ctx, key, "", env); value != "" {
			return value, fmt.Sprintf("lane %s (%s)", lane, CfgSource(ctx, key, env))
		}
	}
	return "", "default"
}

func roleSetting(role, attr string, ctx *Config, env platform.Env) (string, string) {
	key := "role_" + strings.ReplaceAll(role, "-", "_") + "_" + attr
	if value := Cfg(ctx, key, "", env); value != "" {
		return value, fmt.Sprintf("role config (%s)", CfgSource(ctx, key, env))
	}
	return "", "default"
}

func ResolveRoleSettings(role string, ctx *Config, env platform.Env, cwd string, flags RoleFlags) RoleSettings {
	file := RoleFile(role, env, cwd)
	lane := ""
	if LanesEnabled(ctx, env) {
		lane = LaneOfRole(ctx, role, env)
	}
	r := RoleSettings{Lane: lane, Kind: flags.Kind, KindFrom: "flag", ModelSpec: flags.Model, ModelFrom: "flag", Effort: flags.Effort, EffortFrom: "flag", Approvals: flags.Approvals, ApprovalsFrom: "flag"}
	if r.Kind == "" {
		r.Kind, r.KindFrom = roleOrLaneSetting(role, lane, "kind", ctx, env)
		if r.Kind == "" {
			if value := FmGet(file, "kind"); value != "" {
				r.Kind, r.KindFrom = value, "role file"
			}
		}
	}
	if r.Effort == "" {
		r.Effort, r.EffortFrom = resolveRoleEffort(role, lane, r.Kind, file, ctx, env)
	}
	if r.ModelSpec == "" {
		r.ModelSpec, r.ModelFrom, r.LaneModels, r.DiscardedFrontmatterModel = resolveRoleModel(role, lane, r.Kind, file, flags.Kind != "", ctx, env)
	}
	if r.Approvals == "" {
		r.Approvals, r.ApprovalsFrom = roleOrLaneSetting(role, lane, "approvals", ctx, env)
		if r.Approvals == "" {
			if value := FmGet(file, "approvals"); value != "" {
				r.Approvals, r.ApprovalsFrom = value, "role file"
			} else if value := Cfg(ctx, "approvals", "", env); value != "" {
				r.Approvals, r.ApprovalsFrom = value, fmt.Sprintf("approvals (%s)", CfgSource(ctx, "approvals", env))
			} else {
				r.Approvals = "auto"
			}
		}
	}
	return r
}

// ConfiguredRoleEffort resolves effort for a worker whose kind is already
// known, including workers opened before the current configuration.
func ConfiguredRoleEffort(role, lane, kind string, ctx *Config, env platform.Env, cwd string) string {
	effort, _ := resolveRoleEffort(role, lane, kind, RoleFile(role, env, cwd), ctx, env)
	return effort
}

func resolveRoleEffort(role, lane, kind, file string, ctx *Config, env platform.Env) (string, string) {
	if value, from := roleOrLaneSetting(role, lane, "effort", ctx, env); value != "" {
		return value, from
	}
	key := "effort_" + kind
	if value := Cfg(ctx, key, "", env); value != "" {
		return value, fmt.Sprintf("effort.%s (%s)", kind, CfgSource(ctx, key, env))
	}
	if value := FmGet(file, "effort"); value != "" {
		return value, "role file"
	}
	return "", "default"
}

func resolveRoleModel(role, lane, kind, file string, kindFlag bool, ctx *Config, env platform.Env) (spec, from string, models []string, discardedFrontmatter string) {
	if !kindFlag {
		// Roles outside lanes keep their existing kind-layer rule.
		roleModelKey := "role_" + strings.ReplaceAll(role, "-", "_") + "_model"
		kindLayer := SpawnKindLayer(ctx, role, "", false, env)
		if value, from := roleSetting(role, "model", ctx, env); value != "" && (lane != "" || CfgLayerRank(ctx, roleModelKey, env) >= kindLayer) {
			return value, from, nil, ""
		}
		if lane != "" && kind == Cfg(ctx, LaneKey(lane, "kind"), "", env) {
			if value, key, models := LaneModelConfig(ctx, lane, env); value != "" {
				return value, fmt.Sprintf("lane %s (%s)", lane, CfgSource(ctx, key, env)), models, ""
			}
		}
		frontKindMatches := kind == FmGet(file, "kind")
		if lane == "" {
			frontKindMatches = kindLayer == 0
		}
		if value := FmGet(file, "model"); value != "" {
			if frontKindMatches {
				return value, "role file", nil, ""
			}
			if lane != "" {
				discardedFrontmatter = value
			}
		}
	}
	position := "worker"
	if role == "sub-orchestrator" {
		position = "orchestrator"
	}
	for _, source := range []struct{ key, display string }{
		{"model_" + kind + "_" + position, "model." + kind + "." + position},
		{"model_" + kind, "model." + kind},
	} {
		if value := Cfg(ctx, source.key, "", env); value != "" {
			return value, fmt.Sprintf("%s (%s)", source.display, CfgSource(ctx, source.key, env)), nil, discardedFrontmatter
		}
	}
	return "", "default", nil, discardedFrontmatter
}

func RoleTimeoutMs(role string, ctx *Config, env platform.Env, cwd string) int64 {
	file := RoleFile(role, env, cwd)
	raw := ""
	if file != "" {
		raw = FmGet(file, "timeout")
	}
	base, err := strconv.ParseFloat(raw, 64)
	if raw == "" || err != nil || math.IsNaN(base) || math.IsInf(base, 0) || base < 0 {
		base, _ = strconv.ParseFloat(Cfg(ctx, "dispatch_timeout", "900000", env), 64)
	}
	if math.IsNaN(base) || math.IsInf(base, 0) || base < 0 {
		base = 900000
	}
	effort := ResolveRoleSettings(role, ctx, env, cwd, RoleFlags{}).Effort
	if effort == "max" {
		base *= 2
	} else if effort == "xhigh" {
		base *= 1.5
	}
	return int64(math.Floor(base + 0.5))
}
