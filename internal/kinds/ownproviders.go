package kinds

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
)

type OwnModel struct {
	ID        string   `json:"id"`
	MaxEffort string   `json:"max_effort"`
	Warnings  []string `json:"warnings"`
}

// OwnProviderDoctorLines returns safe configuration diagnostics for local providers.
func OwnProviderDoctorLines(ctx *core.Config, env platform.Env, cwd string) (bool, []string) {
	models := append(PiOwnModelsWithConfig(ctx, env), OpencodeOwnModels(env, cwd)...)
	seen := map[string]bool{}
	warnings := []string{}
	for _, model := range models {
		for _, warning := range model.Warnings {
			if strings.HasPrefix(warning, "own provider '") {
				if at := strings.Index(warning, " has a literal apiKey in "); at >= 0 {
					key := warning[:at]
					if seen[key] {
						continue
					}
					seen[key] = true
				}
			}
			warnings = append(warnings, warning)
		}
	}
	return len(models) > 0, warnings
}

func ownObject(v any) *jsonjs.Object { o, _ := v.(*jsonjs.Object); return o }
func ownObjectValue(o *jsonjs.Object, key string) *jsonjs.Object {
	if o == nil {
		return nil
	}
	v, _ := o.Get(key)
	if v == nil {
		return &jsonjs.Object{}
	}
	return ownObject(v)
}
func ownStringValue(o *jsonjs.Object, key string) string {
	if o == nil {
		return ""
	}
	v, _ := o.Get(key)
	s, _ := v.(string)
	return s
}
func effortRank(s string) int {
	for i, v := range []string{"low", "medium", "high", "xhigh", "max"} {
		if s == v {
			return i + 1
		}
	}
	return 0
}
func parseOwnFile(file string) *jsonjs.Object {
	b, e := os.ReadFile(file)
	if e != nil {
		return nil
	}
	v, e := jsonjs.Parse(b)
	if e != nil {
		return nil
	}
	return ownObject(v)
}

// PiOwnModels returns only public model identifiers and thinking ceilings; provider credentials are never copied.
func PiOwnModels(env platform.Env) []OwnModel { return PiOwnModelsWithConfig(nil, env) }
func PiOwnModelsWithConfig(ctx *core.Config, env platform.Env) []OwnModel {
	file := filepath.Join(platform.HomeDir(platform.Current(), env), ".pi", "agent", "models.json")
	root := parseOwnFile(file)
	providers := ownObjectValue(root, "providers")
	if providers == nil {
		return nil
	}
	var out []OwnModel
	for _, p := range providers.Keys() {
		// ownObjectValue turns null into an empty object; a null or non-object
		// provider is malformed and is skipped, the other providers stay
		// listed (DJA-655 decision 5).
		if value, _ := providers.Get(p); value == nil {
			continue
		}
		provider := ownObjectValue(providers, p)
		if provider == nil {
			continue
		}
		// A provider with only modelOverrides declares no model of its own.
		raw, ok := provider.Get("models")
		if !ok {
			continue
		}
		list, ok := raw.([]any)
		if !ok {
			continue
		}
		apiKey, keyPresent := provider.Get("apiKey")
		literal := keyPresent && apiKey != nil
		// pi resolves "$NAME" from the environment and runs "!command" for the key:
		// both are references, not a key written in the file.
		if s, ok := apiKey.(string); ok && (strings.HasPrefix(s, "$") || (strings.HasPrefix(s, "!") && strings.TrimSpace(s[1:]) != "")) {
			literal = false
		}
		for _, entry := range list {
			m := ownObject(entry)
			if m == nil {
				continue
			}
			id := ownStringValue(m, "id")
			if id == "" {
				continue
			}
			level, best := "", 0
			if rawLevels, ok := m.Get("thinkingLevelMap"); ok && rawLevels != nil {
				levels := ownObject(rawLevels)
				if levels == nil {
					continue
				}
				for _, key := range levels.Keys() {
					value, _ := levels.Get(key)
					if value == nil {
						continue
					}
					r := effortRank(key)
					if r >= best {
						best, level = r, key
					}
				}
			}
			if best == 0 {
				level = ""
			}
			warnings := []string{}
			if literal {
				warnings = append(warnings, fmt.Sprintf("own provider '%s' (pi) has a literal apiKey in %s; use an environment reference (pi: \"$MY_API_KEY\", opencode: \"{env:MY_API_KEY}\")", p, file))
			}
			if ctx != nil {
				budgetLevel := core.Cfg(ctx, "effort_pi", "high", env)
				defaults := map[string]float64{"minimal": 1024, "low": 2048, "medium": 8192, "high": 16384}
				budget, known := defaults[budgetLevel]
				settings := parseOwnFile(filepath.Join(platform.HomeDir(platform.Current(), env), ".pi", "agent", "settings.json"))
				if tb := ownObjectValue(settings, "thinkingBudgets"); tb != nil {
					if v, ok := tb.Get(budgetLevel); ok {
						if n, ok := v.(float64); ok {
							budget, known = n, true
						}
					}
				}
				if rawTokens, ok := m.Get("maxTokens"); ok {
					if n, ok := rawTokens.(float64); ok && known && n < budget+8192 {
						warnings = append(warnings, fmt.Sprintf("pi model %s/%s: maxTokens %v leaves less than 8192 tokens over the %s reasoning budget (%v); answers and tool calls get truncated. Set maxTokens to at least %v", p, id, n, budgetLevel, budget, budget+8192))
					}
				}
			}
			out = append(out, OwnModel{ID: p + "/" + id, MaxEffort: level, Warnings: warnings})
		}
	}
	return out
}

// opencodeEnvKeyRE is an opencode key reference: "{env:NAME}" or
// "{file:path}" (opencode reads the key from that file).
var opencodeEnvKeyRE = regexp.MustCompile(`^(\{env:[A-Za-z_][A-Za-z0-9_]*\}|\{file:[^{}\s][^{}]*\})$`)

// ownKeyLiteral reports whether the apiKey at the format's key location
// ("options" in the deprecated V1 provider, "settings" in the current V2
// providers) is a key written in the file rather than an {env:NAME}/
// {file:path} reference.
func ownKeyLiteral(provider *jsonjs.Object, where string) bool {
	apiKey, present := ownObjectValue(provider, where).Get("apiKey")
	literal := present && apiKey != nil
	if s, ok := apiKey.(string); ok && opencodeEnvKeyRE.MatchString(s) {
		literal = false
	}
	return literal
}

// ownOpenProvider appends the models of one opencode provider declaration to
// out, counting each provider/model once across every declaration and
// keeping the first. When the declaration carries a literal apiKey the
// warning goes to its models and, when every model of it was already counted
// by another declaration, to the first of those (decision: the key check
// applies per provider of each format, so the warning is never lost behind
// the declaration that won the model); the warning never names the key
// value. budgetWarn limits the missing thinking_token_budget warning to the
// format that declares the field (V1; the V2 schema has no such field).
func ownOpenProvider(p string, models *jsonjs.Object, literal, budgetWarn bool, file string, seen map[string]bool, idx map[string]int, out []OwnModel) []OwnModel {
	warn := ""
	if literal {
		warn = fmt.Sprintf("own provider '%s' (opencode) has a literal apiKey in %s; use an environment reference (pi: \"$MY_API_KEY\", opencode: \"{env:MY_API_KEY}\")", p, file)
	}
	attached := false
	for _, id := range models.Keys() {
		key := p + "/" + id
		if seen[key] {
			continue
		}
		seen[key] = true
		idx[key] = len(out)
		warnings := []string{}
		if warn != "" {
			warnings = append(warnings, warn)
			attached = true
		}
		if budgetWarn {
			model := ownObjectValue(models, id)
			mopts := ownObjectValue(model, "options")
			budget, has := mopts.Get("thinking_token_budget")
			_, numeric := budget.(float64)
			if !has || !numeric {
				warnings = append(warnings, fmt.Sprintf("opencode model %s/%s has no thinking_token_budget in its options; the skill's effort is dropped and the server default applies", p, id))
			}
		}
		out = append(out, OwnModel{ID: key, Warnings: warnings})
	}
	if warn != "" && !attached {
		// Every model of this declaration was counted by another declaration
		// (format or file); attach the warning to the first of them, only if
		// it does not already carry that exact text. Two formats literal at
		// once in the same file produce the same text; different files
		// produce different texts and both stay (DJA-655 amendment P3).
		for _, id := range models.Keys() {
			if i, ok := idx[p+"/"+id]; ok {
				already := false
				for _, w := range out[i].Warnings {
					if w == warn {
						already = true
						break
					}
				}
				if !already {
					out[i].Warnings = append(out[i].Warnings, warn)
				}
				break
			}
		}
	}
	return out
}

// ownOpenSection reads one format section of one config root ("provider",
// the deprecated V1, or "providers", the current V2). A null or non-object
// provider and a provider without an object models map lists nothing; the
// key location and the budget warning differ per format (see
// ownOpenProvider).
func ownOpenSection(root *jsonjs.Object, key, where string, budgetWarn bool, file string, seen map[string]bool, idx map[string]int, out []OwnModel) []OwnModel {
	section := ownObjectValue(root, key)
	if section == nil {
		return out
	}
	for _, p := range section.Keys() {
		if value, _ := section.Get(p); value == nil {
			continue
		}
		provider := ownObjectValue(section, p)
		if provider == nil {
			continue
		}
		models := ownObjectValue(provider, "models")
		if models == nil {
			continue
		}
		out = ownOpenProvider(p, models, ownKeyLiteral(provider, where), budgetWarn, file, seen, idx, out)
	}
	return out
}

// OpencodeOwnModels scans project, explicit, XDG, then home config, preserving first declarations. Each file is read in both formats: the deprecated provider (V1, options.apiKey, per-model thinking_token_budget) and the current providers (V2, settings.apiKey); within one file the V2 models count first, so a model present in both keeps the V2 entry, and across files the first declaration still wins.
func OpencodeOwnModels(env platform.Env, cwd string) []OwnModel {
	home := platform.HomeDir(platform.Current(), env)
	files := []string{filepath.Join(platform.ProjectRoot(env, cwd), "opencode.json")}
	if p := env.Get("OPENCODE_CONFIG"); p != "" {
		files = append(files, p)
	}
	xdg := env.Get("XDG_CONFIG_HOME")
	if xdg == "" {
		xdg = filepath.Join(home, ".config")
	}
	files = append(files, filepath.Join(xdg, "opencode", "opencode.json"), filepath.Join(home, ".opencode", "opencode.json"))
	seen := map[string]bool{}
	idx := map[string]int{}
	var out []OwnModel
	for _, file := range files {
		if st, e := os.Stat(file); e != nil || !st.Mode().IsRegular() {
			continue
		}
		root := parseOwnFile(file)
		out = ownOpenSection(root, "providers", "settings", false, file, seen, idx, out)
		out = ownOpenSection(root, "provider", "options", true, file, seen, idx, out)
	}
	return out
}
