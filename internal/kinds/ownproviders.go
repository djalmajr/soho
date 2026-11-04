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
		// ownObjectValue turns null into an empty object; a null provider is
		// malformed, not a provider without models.
		if value, _ := providers.Get(p); value == nil {
			return nil
		}
		provider := ownObjectValue(providers, p)
		if provider == nil {
			return nil
		}
		// A provider with only modelOverrides declares no model of its own.
		raw, ok := provider.Get("models")
		if !ok {
			continue
		}
		list, ok := raw.([]any)
		if !ok {
			return nil
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
				return nil
			}
			id := ownStringValue(m, "id")
			if id == "" {
				continue
			}
			level, best := "", 0
			if rawLevels, ok := m.Get("thinkingLevelMap"); ok && rawLevels != nil {
				levels := ownObject(rawLevels)
				if levels == nil {
					return nil
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

// OpencodeOwnModels scans project, explicit, XDG, then home config, preserving first declarations.
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
	var out []OwnModel
	for _, file := range files {
		if st, e := os.Stat(file); e != nil || !st.Mode().IsRegular() {
			continue
		}
		root := parseOwnFile(file)
		provider := ownObjectValue(root, "provider")
		if provider == nil {
			continue
		}
		for _, p := range provider.Keys() {
			po := ownObjectValue(provider, p)
			if po == nil {
				continue
			}
			models := ownObjectValue(po, "models")
			if models == nil {
				continue
			}
			opts := ownObjectValue(po, "options")
			apiKey, present := opts.Get("apiKey")
			literal := present && apiKey != nil
			if s, ok := apiKey.(string); ok && opencodeEnvKeyRE.MatchString(s) {
				literal = false
			}
			for _, id := range models.Keys() {
				key := p + "/" + id
				if seen[key] {
					continue
				}
				seen[key] = true
				warnings := []string{}
				if literal {
					warnings = append(warnings, fmt.Sprintf("own provider '%s' (opencode) has a literal apiKey in %s; use an environment reference (pi: \"$MY_API_KEY\", opencode: \"{env:MY_API_KEY}\")", p, file))
				}
				model := ownObjectValue(models, id)
				mopts := ownObjectValue(model, "options")
				budget, has := mopts.Get("thinking_token_budget")
				_, numeric := budget.(float64)
				if !has || !numeric {
					warnings = append(warnings, fmt.Sprintf("opencode model %s/%s has no thinking_token_budget in its options; the skill's effort is dropped and the server default applies", p, id))
				}
				out = append(out, OwnModel{ID: key, Warnings: warnings})
			}
		}
	}
	return out
}
