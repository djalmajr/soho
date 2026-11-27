package spawn

import (
	"errors"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/kinds"
	"github.com/djalmajr/soho/internal/platform"
)

const spawnEnvRefusal = "spawn: invalid explicit environment handoff"

var spawnEnvNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

const (
	envDropColumn     = 18 // After e2e_port and the OMP approval column.
	envDropPrefix     = "env-drop:v1:"
	envDropMaxNames   = 16
	envDropMarkerSize = 4096
)

func validSpawnEnvName(name string) bool {
	upper := strings.ToUpper(name)
	return spawnEnvNameRE.MatchString(name) && !strings.HasPrefix(upper, "SOHO_") &&
		!strings.HasPrefix(upper, "HC_") && !strings.HasPrefix(upper, "HERDR_")
}

func refuseSpawnEnvDrop(message string) {
	platform.Die("spawn: "+message, 2)
}

func normalizeEnvDrop(names []string) ([]string, error) {
	var normalized []string
	spellings := make(map[string]string)
	size := len(envDropPrefix)
	for _, name := range names {
		if len(name) > envDropMarkerSize || !validSpawnEnvName(name) {
			return nil, errors.New("--env-drop requires portable non-routing names within the policy size limit")
		}
		folded := strings.ToUpper(name)
		if previous, exists := spellings[folded]; exists {
			if previous != name {
				return nil, errors.New("--env-drop names must not differ only by case")
			}
			continue
		}
		if len(normalized) == envDropMaxNames {
			return nil, errors.New("--env-drop accepts at most 16 distinct names")
		}
		if len(normalized) != 0 {
			size++
		}
		size += len(name)
		if size > envDropMarkerSize {
			return nil, errors.New("--env-drop exceeds the policy size limit")
		}
		spellings[folded] = name
		normalized = append(normalized, name)
	}
	return normalized, nil
}

func prepareSpawnEnvDrop(o *spawnOptions, env platform.Env) {
	if len(o.envDrop) == 0 {
		return
	}
	if o.reuse != "off" || o.reuseSet || o.paneSet {
		refuseSpawnEnvDrop("--env-drop requires --fresh without --reuse or --pane")
	}
	if env.Get("SOHO_TRANSPORT") != "hc" || !platform.WorkerEnvExecSupported() {
		refuseSpawnEnvDrop("--env-drop requires native Core on macOS or Linux")
	}
	names, err := normalizeEnvDrop(o.envDrop)
	if err != nil {
		refuseSpawnEnvDrop(err.Error())
	}
	for _, name := range names {
		for _, from := range o.envFrom {
			if strings.EqualFold(name, from) {
				refuseSpawnEnvDrop("--env-drop conflicts with --env-from")
			}
		}
	}
	if _, err := platform.WorkerExecutable(); err != nil {
		refuseSpawnEnvDrop("--env-drop requires a usable absolute soho executable on the Core host")
	}
	o.envDrop = names
}

func preflightSpawnEnvDrop(o spawnOptions, kind string, profile kinds.LaunchProfile, env platform.Env) {
	if len(o.envDrop) == 0 {
		return
	}
	if !kinds.NativeStatusSupported(kind, profile.Name) {
		refuseSpawnEnvDrop("--env-drop requires a supported native Core worker kind")
	}
	for _, name := range o.envDrop {
		for override := range profile.EnvOverrides {
			if strings.EqualFold(name, override) {
				refuseSpawnEnvDrop("--env-drop conflicts with a launch profile environment override")
			}
		}
	}
	exe, found := platform.FindExecutable(kinds.KindExe(kind), env, platform.Current())
	if !found {
		refuseSpawnEnvDrop("--env-drop requires a resolved absolute worker executable")
	}
	if _, err := filepath.Abs(exe); err != nil {
		refuseSpawnEnvDrop("--env-drop requires a resolved absolute worker executable")
	}
}

func envDropMarker(fields []string) string { return fieldAt(fields, envDropColumn) }

// RosterEnvDrop reads the canonical names from the optional roster column.
// Missing columns describe historical workers with ordinary inheritance.
func RosterEnvDrop(fields []string) ([]string, error) {
	marker := envDropMarker(fields)
	if marker == "" {
		return nil, nil
	}
	invalid := errors.New("invalid stored --env-drop policy")
	raw, ok := strings.CutPrefix(marker, envDropPrefix)
	if !ok || raw == "" || len(marker) > envDropMarkerSize {
		return nil, invalid
	}
	names, err := normalizeEnvDrop(strings.Split(raw, ","))
	if err != nil || strings.Join(names, ",") != raw {
		return nil, invalid
	}
	return names, nil
}

func refuseSpawnEnv() {
	// No friction flag: the outer CLI must not persist a refusal record.
	panic(&platform.ExitError{Code: 2, Msg: spawnEnvRefusal})
}

func prepareSpawnEnv(o *spawnOptions, ctx *core.Config, env platform.Env, cwd string) {
	if len(o.envFrom) == 0 {
		return
	}
	if env.Get("SOHO_TRANSPORT") != "hc" || o.reuse != "off" {
		refuseSpawnEnv()
	}
	res := core.ResolveRoleSettings(o.role, ctx, env, cwd, core.RoleFlags{Kind: o.kind, Model: o.model, Effort: o.effort, Approvals: o.approvals})
	if res.Kind != "pi" {
		refuseSpawnEnv()
	}
	snapshot := make(map[string]string)
	spellings := make(map[string]string)
	for _, name := range o.envFrom {
		upper := strings.ToUpper(name)
		// Mirror internal/hc's private portable-name and reserved-prefix
		// policy; the RPC consumer independently validates it again.
		if !validSpawnEnvName(name) {
			refuseSpawnEnv()
		}
		if _, exists := snapshot[name]; exists {
			continue
		}
		if previous, exists := spellings[upper]; exists && previous != name {
			refuseSpawnEnv()
		}
		spellings[upper] = name
		if len(snapshot) == 16 {
			refuseSpawnEnv()
		}
		value := env.Get(name)
		if value == "" || len(value) > 65536 || strings.ContainsRune(value, 0) || !utf8.ValidString(value) {
			refuseSpawnEnv()
		}
		if len(value) < 4 {
			panic(&platform.ExitError{Code: 2, Msg: "spawn: --env-from " + name + ": value too short to protect in output"})
		}
		snapshot[name] = value
	}
	o.envSnapshot = snapshot
}

// outputText belongs to this spawn's admitted snapshot only. Replace in a
// single pass so replacement tokens are never reinterpreted as input. Ties
// have a stable name order; overlapping values prefer the longest match.
// This intentionally does not recognize encoded or split representations.
func (o spawnOptions) outputText(s string) string {
	if len(o.envSnapshot) == 0 {
		return s
	}
	names := make([]string, 0, len(o.envSnapshot))
	for name := range o.envSnapshot {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		a, b := o.envSnapshot[names[i]], o.envSnapshot[names[j]]
		if len(a) != len(b) {
			return len(a) > len(b)
		}
		return names[i] < names[j]
	})
	pairs := make([]string, 0, 2*len(names))
	for _, name := range names {
		pairs = append(pairs, o.envSnapshot[name], "[redacted:"+name+"]")
	}
	return strings.NewReplacer(pairs...).Replace(s)
}

// Redact strings before serialization, which can escape bytes in a value.
// Keep the schema keys and non-string values intact.
func (o spawnOptions) outputObject(obj *jsonjs.Object) *jsonjs.Object {
	result := jsonjs.O()
	for _, key := range obj.Keys() {
		v, _ := obj.Get(key)
		switch value := v.(type) {
		case string:
			v = o.outputText(value)
		case *jsonjs.Object:
			v = o.outputObject(value)
		}
		result.Set(key, v)
	}
	return result
}

func warnSpawnOutput(output []spawnOptions, message string, ctx *core.Config, env platform.Env) {
	if len(output) != 0 {
		message = output[0].outputText(message)
	}
	Warn(message, ctx, env, "spawn")
}
