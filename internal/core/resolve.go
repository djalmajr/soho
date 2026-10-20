package core

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/djalmajr/soho/internal/platform"
)

type RoleFlags struct{ Kind, Model, Effort, Approvals string }
type RoleSettings struct {
	Lane          string
	Kind          string
	KindFrom      string
	ModelSpec     string
	ModelFrom     string
	Effort        string
	EffortFrom    string
	Approvals     string
	ApprovalsFrom string
	KindLayer     int
}

// ResolvedRoleKind is the spawn default: role config (any layer), else role frontmatter.
// It intentionally does not consult lane configuration.
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

func ResolveRoleSettings(role string, ctx *Config, env platform.Env, cwd string, flags RoleFlags) RoleSettings {
	rk := strings.ReplaceAll(role, "-", "_")
	file := RoleFile(role, env, cwd)
	front := func(key string) string {
		if file == "" {
			return ""
		}
		return FmGet(file, key)
	}
	lane := ""
	if LanesEnabled(ctx, env) {
		lane = LaneOfRole(ctx, role, env)
	}
	kindLayer := SpawnKindLayer(ctx, role, lane, flags.Kind != "", env)
	r := RoleSettings{Lane: lane, Kind: flags.Kind, KindFrom: "flag", ModelSpec: flags.Model, ModelFrom: "flag", Effort: flags.Effort, EffortFrom: "flag", Approvals: flags.Approvals, ApprovalsFrom: "flag", KindLayer: kindLayer}
	if r.Kind == "" && lane != "" {
		key := LaneKey(lane, "kind")
		if v := LaneAttr(ctx, lane, "kind", nil, env); v != "" {
			r.Kind = v
			r.KindFrom = fmt.Sprintf("lane %s (%s)", lane, CfgSource(ctx, key, env))
		}
	}
	if r.Kind == "" {
		key := "role_" + rk + "_kind"
		if v := Cfg(ctx, key, "", env); v != "" {
			r.Kind = v
			r.KindFrom = fmt.Sprintf("role config (%s)", CfgSource(ctx, key, env))
		}
	}
	if r.Kind == "" {
		if v := front("kind"); v != "" {
			r.Kind = v
			r.KindFrom = "role file"
		}
	}
	if r.Kind == "" {
		r.KindFrom = "default"
	}
	if r.Effort == "" && lane != "" {
		key := LaneKey(lane, "effort")
		if v := LaneAttr(ctx, lane, "effort", &kindLayer, env); v != "" {
			r.Effort = v
			r.EffortFrom = fmt.Sprintf("lane %s (%s)", lane, CfgSource(ctx, key, env))
		}
	}
	if r.Effort == "" {
		key := "role_" + rk + "_effort"
		if v := Cfg(ctx, key, "", env); v != "" {
			r.Effort = v
			r.EffortFrom = fmt.Sprintf("role config (%s)", CfgSource(ctx, key, env))
		}
	}
	if r.Effort == "" {
		key := "effort_" + r.Kind
		if v := Cfg(ctx, key, "", env); v != "" {
			r.Effort = v
			r.EffortFrom = fmt.Sprintf("effort.%s (%s)", r.Kind, CfgSource(ctx, key, env))
		}
	}
	if r.Effort == "" {
		if v := front("effort"); v != "" {
			r.Effort = v
			r.EffortFrom = "role file"
		}
	}
	if r.Effort == "" {
		r.EffortFrom = "default"
	}
	position := "worker"
	if role == "sub-orchestrator" {
		position = "orchestrator"
	}
	if r.ModelSpec == "" && lane != "" {
		key := LaneKey(lane, "model")
		if v := LaneAttr(ctx, lane, "model", &kindLayer, env); v != "" {
			r.ModelSpec = v
			r.ModelFrom = fmt.Sprintf("lane %s (%s)", lane, CfgSource(ctx, key, env))
		}
	}
	roleModelKey := "role_" + rk + "_model"
	if r.ModelSpec == "" {
		v := Cfg(ctx, roleModelKey, "", env)
		if v != "" && CfgLayerRank(ctx, roleModelKey, env) >= kindLayer {
			r.ModelSpec = v
			r.ModelFrom = fmt.Sprintf("role config (%s)", CfgSource(ctx, roleModelKey, env))
		}
	}
	if r.ModelSpec == "" {
		if v := front("model"); v != "" && kindLayer == 0 {
			r.ModelSpec = v
			r.ModelFrom = "role file"
		}
	}
	modelSources := []struct{ key, display string }{{"model_" + r.Kind + "_" + position, "model." + r.Kind + "." + position}, {"model_" + r.Kind, "model." + r.Kind}}
	for _, source := range modelSources {
		if r.ModelSpec == "" {
			if v := Cfg(ctx, source.key, "", env); v != "" {
				r.ModelSpec = v
				r.ModelFrom = fmt.Sprintf("%s (%s)", source.display, CfgSource(ctx, source.key, env))
			}
		}
	}
	if r.ModelSpec == "" {
		r.ModelFrom = "default"
	}
	if r.Approvals == "" && lane != "" {
		key := LaneKey(lane, "approvals")
		if v := LaneAttr(ctx, lane, "approvals", nil, env); v != "" {
			r.Approvals = v
			r.ApprovalsFrom = fmt.Sprintf("lane %s (%s)", lane, CfgSource(ctx, key, env))
		}
	}
	if r.Approvals == "" {
		key := "role_" + rk + "_approvals"
		if v := Cfg(ctx, key, "", env); v != "" {
			r.Approvals = v
			r.ApprovalsFrom = fmt.Sprintf("role config (%s)", CfgSource(ctx, key, env))
		}
	}
	if r.Approvals == "" {
		if v := front("approvals"); v != "" {
			r.Approvals = v
			r.ApprovalsFrom = "role file"
		}
	}
	if r.Approvals == "" {
		v := Cfg(ctx, "approvals", "", env)
		if v != "" {
			r.Approvals = v
			r.ApprovalsFrom = fmt.Sprintf("approvals (%s)", CfgSource(ctx, "approvals", env))
		} else {
			r.Approvals = "auto"
			r.ApprovalsFrom = "default"
		}
	}
	return r
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
