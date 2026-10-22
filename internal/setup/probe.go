package setup

import (
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/kinds"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/provider"
	"github.com/djalmajr/soho/internal/text"
)

const ProbePrompt = "Reply with exactly ok"

// timeoutRetryHint is the fixed hint appended to a probe timeout cause: a
// timeout only bounds the probe, it does not prove the assistant is down
// (rc.11: codex timed out at 20 s on Windows, then answered at 60 s).
const timeoutRetryHint = " (a timeout does not prove the assistant is unavailable; retry with --timeout 90)"

// probeKindDefaultTimeout is the window a kind probes with when neither
// --timeout nor SOHO_PROBE_TIMEOUT is set: codex gets 60 s (its
// Windows cold start timed out at the 20 s window, a false negative), every
// other kind stays at 20 s.
func probeKindDefaultTimeout(kind string) int {
	if kind == "codex" {
		return 60
	}
	return 20
}

var noAuthRE = regexp.MustCompile(`(?i)not logged in|not (yet )?authenticated|please (log|sign) ?in|log ?in (to|first)|unauthorized|unauthenticated|(missing|no|invalid) (api )?key|api key (is )?(missing|required)|authentication (failed|required|error)|access denied|no (valid )?credentials`)

type Probe struct {
	Kind   string `json:"kind"`
	Model  string `json:"model"`
	Status string `json:"status"`
	Cause  string `json:"cause"`
	Source string `json:"source"`
}

func probeDefaultModel(ctx *core.Config, k string, env platform.Env, cwd string) string {
	m := core.Cfg(ctx, "model_"+k+"_worker", "", env)
	if m == "" {
		m = core.Cfg(ctx, "model_"+k, "", env)
	}
	if m != "" && (k == "codex" || k == "cursor" || k == "agy" || k == "grok") {
		resolved, e := kinds.ResolveModel(k, m, "", env, nil)
		if e != nil {
			return ""
		}
		m = resolved
	}
	return m
}
func probeArgs(k, m string) []string {
	ma := kinds.KindModelArgs(k, m, "", nil)
	switch k {
	case "claude":
		return append([]string{"-p", ProbePrompt}, ma...)
	case "codex":
		return append(append([]string{"exec"}, ma...), ProbePrompt)
	case "grok", "agy", "gemini", "cursor":
		return append(append([]string{"-p", ProbePrompt}, ma...), nil...)
	case "pi":
		return append(append([]string{"-p", "--no-session", ProbePrompt}, ma...), nil...)
	case "opencode":
		return append(append([]string{"run", ProbePrompt}, ma...), nil...)
	default:
		return []string{ProbePrompt}
	}
}

func probeOne(ctx *core.Config, k, m, source string, timeout int, env platform.Env, cwd string) Probe {
	exe := kinds.KindExe(k)
	if _, ok := platform.FindExecutable(exe, env, platform.Current()); !ok {
		return Probe{k, m, "error", "not installed", source}
	}
	r := platform.RunCli(exe, probeArgs(k, m), platform.RunOptions{Env: env, Cwd: cwd, TimeoutMs: timeout * 1000, OutputFiles: true})
	combined := r.Stdout + r.Stderr
	if r.TimedOut || (r.Status == nil && r.Signal != "") {
		return Probe{k, m, "error", fmt.Sprintf("timeout after %ds%s", timeout, timeoutRetryHint), source}
	}
	for _, line := range strings.Split(combined, "\n") {
		if noAuthRE.MatchString(line) {
			return Probe{k, m, "no-auth", "not authenticated", source}
		}
	}
	if q := provider.QuotaDetect("idle", combined); q != nil {
		cause := "quota exhausted"
		if len(q) > 1 {
			v := provider.RenewalValue(q[1])
			if v != "" {
				cause += "; renews " + text.RedactSecrets(v)
			}
		}
		return Probe{k, m, "quota", cause, source}
	}
	if r.Status != nil && *r.Status == 0 {
		return Probe{k, m, "ready", "", source}
	}
	code := 126
	if r.Status != nil {
		code = *r.Status
	} else if r.Error == "ENOENT" {
		code = 127
	}
	return Probe{k, m, "error", fmt.Sprintf("exit %d", code), source}
}

func CmdProbe(args []string, ctx *core.Config, env platform.Env, cwd string) {
	kind, model, to := "", "", ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--kind" || a == "--model" || a == "--timeout" {
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				platform.DieFriction("setup --probe: "+a+" expects a value", 2)
			}
			i++
			switch a {
			case "--kind":
				kind = args[i]
			case "--model":
				model = args[i]
			case "--timeout":
				to = args[i]
			}
		} else {
			platform.DieFriction(fmt.Sprintf("setup --probe: unknown option '%s'", a), 2)
		}
	}
	if model != "" && kind == "" {
		platform.DieFriction("setup --probe: --model needs --kind (probe one kind/model: --kind K --model M)", 2)
	}
	// SOHO_PROBE_TIMEOUT and --timeout win over the kind defaults; an
	// invalid value dies 2 before any probe CLI runs (the variable is checked
	// first, as before).
	envTimeout := 0
	if v := env.Get("SOHO_PROBE_TIMEOUT"); v != "" {
		n, e := strconv.Atoi(v)
		if e != nil || n < 1 || strconv.Itoa(n) != v {
			platform.DieFriction("setup --probe: timeout must be a whole number of seconds ≥ 1", 2)
		}
		envTimeout = n
	}
	flagTimeout := 0
	if to != "" {
		n, e := strconv.Atoi(to)
		if e != nil || n < 1 || strconv.Itoa(n) != to {
			platform.DieFriction("setup --probe: timeout must be a whole number of seconds ≥ 1", 2)
		}
		flagTimeout = n
	}
	if kind != "" {
		found := false
		for _, k := range kinds.KnownKinds {
			if kind == k {
				found = true
			}
		}
		if !found {
			platform.DieFriction(fmt.Sprintf("setup --probe: unknown kind '%s' (see: kinds)", kind), 2)
		}
		if model == "" {
			model = probeDefaultModel(ctx, kind, env, cwd)
		}
	}
	type pair struct{ k, m, s string }
	pairs := []pair{}
	skipped := []any{}
	if kind != "" {
		pairs = append(pairs, pair{kind, model, "configured"})
	} else {
		for _, k := range kinds.KnownKinds {
			pairs = append(pairs, pair{k, probeDefaultModel(ctx, k, env, cwd), "configured"})
		}
		for _, k := range []string{"pi", "opencode"} {
			if _, ok := platform.FindExecutable(k, env, platform.Current()); !ok {
				continue
			}
			own := kinds.PiOwnModels(env)
			if k == "opencode" {
				own = kinds.OpencodeOwnModels(env, cwd)
			}
			n := 0
			for _, m := range own {
				if m.ID == "" {
					continue
				}
				n++
				if n <= 5 {
					pairs = append(pairs, pair{k, m.ID, "custom"})
				} else {
					skipped = append(skipped, jsonjs.O("kind", k, "id", m.ID))
				}
			}
		}
	}
	probes := make([]any, 0, len(pairs))
	for _, p := range pairs {
		timeout := probeKindDefaultTimeout(p.k)
		if envTimeout > 0 {
			timeout = envTimeout
		}
		if flagTimeout > 0 {
			timeout = flagTimeout
		}
		result := probeOne(ctx, p.k, p.m, p.s, timeout, env, cwd)
		probes = append(probes, jsonjs.O("kind", result.Kind, "model", result.Model, "status", result.Status, "cause", result.Cause, "source", result.Source))
	}
	if skipped == nil {
		skipped = []any{}
	}
	_, _ = io.WriteString(platform.Stdout, jsonjs.StringifyIndent(jsonjs.O("probes", probes, "skipped_custom", skipped), 2)+"\n")
}
