package setup

import (
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/kinds"
	"github.com/djalmajr/soho/internal/platform"
)

func Detect(ctx *core.Config, env platform.Env, cwd string) *jsonjs.Object {
	kindRows := make([]any, 0, len(kinds.KnownKinds))
	for _, k := range kinds.KnownKinds {
		exe := kinds.KindExe(k)
		_, installed := platform.FindExecutable(exe, env, platform.Current())
		idsEnv := env.Clone()
		if idsEnv.Get("SOHO_MODELS_TIMEOUT") == "" {
			idsEnv["SOHO_MODELS_TIMEOUT"] = "5"
		}
		models := kinds.VersionSortDesc(kinds.ModelIDs(k, idsEnv))
		if len(models) > 3 {
			models = models[:3]
		}
		var own []any
		if k == "pi" {
			for _, m := range kinds.PiOwnModelsWithConfig(ctx, env) {
				own = append(own, ownModelJSON(m))
			}
		}
		if k == "opencode" {
			for _, m := range kinds.OpencodeOwnModels(env, cwd) {
				own = append(own, ownModelJSON(m))
			}
		}
		if own == nil {
			own = []any{}
		}
		kindRows = append(kindRows, jsonjs.O("kind", k, "executable", exe, "installed", installed, "family", kinds.KindFamilyDisplay(k), "effort_ceiling", kinds.KindEffortCeiling(k), "models", stringValues(models), "summary", kinds.KindSummary(k), "custom_models", own))
	}
	roles := detectRoles(ctx, env, cwd)
	worker := make([]any, 0, len(kinds.KnownKinds))
	for _, k := range kinds.KnownKinds {
		key := "model_" + k + "_worker"
		worker = append(worker, jsonjs.O("key", "model."+k+".worker", "value", core.Cfg(ctx, key, "", env), "source", core.CfgSource(ctx, key, env)))
	}
	effective := make([]any, 0)
	for _, lane := range core.LaneNames(ctx, env) {
		effective = append(effective, jsonjs.O("name", lane, "roles", stringValues(core.SplitRoles(core.LaneRolesCSV(ctx, lane, env))), "panes", core.LaneCapacity(ctx, lane, env), "kind", core.LaneAttr(ctx, lane, "kind", nil, env), "model", core.LaneAttr(ctx, lane, "model", nil, env), "effort", core.LaneAttr(ctx, lane, "effort", nil, env), "approvals", core.LaneAttr(ctx, lane, "approvals", nil, env)))
	}
	presets := jsonjs.O()
	mode := core.PaneMode(ctx, env)
	for _, p := range []string{"2", "3", "4"} {
		rows := make([]any, 0)
		for _, lane := range core.PresetLaneNamesFor(p, mode) {
			rows = append(rows, jsonjs.O("name", lane, "roles", stringValues(core.SplitRoles(core.PresetLaneRoles(lane, p, mode))), "panes", core.PresetLaneCapacity(lane, p, mode)))
		}
		presets.Set(p, rows)
	}
	config := jsonjs.O("max_workers", cfgEntry(ctx, "max_workers", "3", env), "multi_role", cfgEntry(ctx, "multi_role", "on", env), "reuse_workers", cfgEntry(ctx, "reuse_workers", "on", env), "panes", cfgEntry(ctx, "panes", "4", env), "lanes", cfgEntry(ctx, "lanes", "on", env), "effective_lanes", effective, "presets", presets, "role_kinds", roles, "worker_models", worker)
	return jsonjs.O("kinds", kindRows, "config", config)
}

func DetectJSON(ctx *core.Config, env platform.Env, cwd string) string {
	return jsonjs.StringifyIndent(Detect(ctx, env, cwd), 2)
}
func CmdDetect(ctx *core.Config, env platform.Env, cwd string) {
	_, _ = io.WriteString(platform.Stdout, DetectJSON(ctx, env, cwd)+"\n")
}

func ownModelJSON(m kinds.OwnModel) *jsonjs.Object {
	w := m.Warnings
	if w == nil {
		w = []string{}
	}
	return jsonjs.O("id", m.ID, "max_effort", m.MaxEffort, "warnings", w)
}
func cfgEntry(ctx *core.Config, key, def string, env platform.Env) *jsonjs.Object {
	return jsonjs.O("value", core.Cfg(ctx, key, def, env), "source", core.CfgSource(ctx, key, env))
}
func stringValues(xs []string) []any {
	out := make([]any, 0, len(xs))
	for _, x := range xs {
		out = append(out, x)
	}
	return out
}

func detectRoles(ctx *core.Config, env platform.Env, cwd string) []any {
	seen := map[string]bool{}
	out := make([]any, 0)
	for _, dir := range core.RoleDirs(env, cwd) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, ent := range entries {
			n := ent.Name()
			if ent.IsDir() || len(n) < 4 || n[0] == '.' || filepath.Ext(n) != ".md" || strings.HasSuffix(n, ".append.md") {
				continue
			}
			name := n[:len(n)-3]
			if seen[name] {
				continue
			}
			seen[name] = true
			key := "role." + name + ".kind"
			ck := strings.NewReplacer(".", "_", "-", "_").Replace("role." + name + ".kind")
			value := core.Cfg(ctx, ck, "", env)
			source := "role"
			if value == "" {
				value = core.FmGet(filepath.Join(dir, n), "kind")
			} else {
				source = core.CfgSource(ctx, ck, env)
			}
			out = append(out, jsonjs.O("key", key, "value", value, "source", source))
		}
	}
	return out
}
