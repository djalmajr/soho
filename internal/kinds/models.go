package kinds

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/platform"
	textutil "github.com/djalmajr/soho/internal/text"
)

var ansiSGR = regexp.MustCompile("\\x1b\\[[0-9;]*m")
var effortSuffixRE = regexp.MustCompile("-(minimal|low|medium|high|xhigh|max)(-fast)?$")
var claudeDottedAliasRE = regexp.MustCompile(`(?i)^(opus|sonnet|haiku|fable)-([0-9]+)\.([0-9]+)$`)

func ParseCursorModels(stdout string) []string {
	var ids []string
	re := regexp.MustCompile("^([a-z0-9.-]+) - ")
	for _, line := range strings.Split(ansiSGR.ReplaceAllString(stdout, ""), "\n") {
		if m := re.FindStringSubmatch(line); m != nil {
			ids = append(ids, m[1])
		}
	}
	return ids
}
func ParseAgyModels(stdout string) []string {
	var ids []string
	valid := regexp.MustCompile("^[a-z0-9.-]+$")
	for _, line := range strings.Split(ansiSGR.ReplaceAllString(stdout, ""), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && valid.MatchString(fields[0]) {
			ids = append(ids, fields[0])
		}
	}
	return ids
}

// ParsePiModels reads provider/model ids from pi --list-models, whose table
// has a header row followed by provider, model and capability columns.
func ParsePiModels(stdout string) []string {
	var ids []string
	token := regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)
	for _, line := range strings.Split(ansiSGR.ReplaceAllString(stdout, ""), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 || (fields[0] == "provider" && fields[1] == "model") {
			continue
		}
		if !token.MatchString(fields[0]) || !token.MatchString(fields[1]) {
			continue
		}
		ids = append(ids, fields[0]+"/"+fields[1])
	}
	return ids
}

// ParseOpenCodeModels reads the provider/model ids printed one per line by
// opencode models; anything that is not a provider/model id is ignored.
func ParseOpenCodeModels(stdout string) []string {
	var ids []string
	idRE := regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._+:-]*$`)
	for _, line := range strings.Split(ansiSGR.ReplaceAllString(stdout, ""), "\n") {
		id := strings.TrimSpace(line)
		if idRE.MatchString(id) {
			ids = append(ids, id)
		}
	}
	return ids
}

func ParseGrokModels(stdout string) []string {
	re := regexp.MustCompile("grok-[0-9][0-9a-z.-]*")
	set := map[string]bool{}
	for _, line := range strings.Split(ansiSGR.ReplaceAllString(stdout, ""), "\n") {
		for _, id := range re.FindAllString(line, -1) {
			set[id] = true
		}
	}
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	textutil.SortUTF16(ids)
	return ids
}
func ModelsCacheFile(kind string, env platform.Env) string {
	dir := env.Get("TMPDIR")
	if dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "soho-models-"+kind+".txt")
}

// ModelIDs reads the model list of the kind, honoring the 1-hour copy in
// TMPDIR for the kinds whose source is a CLI. pi and opencode have no source
// in resolution: their specs pass through and only ListModels runs their CLI.
// codex never uses the copy:
// its source is the local ~/.codex/models_cache.json, which is cheap to
// re-read, and a stale copy could hide a model Codex just added (the doctor
// then reported "does not resolve" until the copy expired).
func ModelIDs(kind string, env platform.Env) []string {
	ids, _, _ := modelIDs(kind, env, false)
	return ids
}

// ModelListError is a failed CLI-backed model listing. Reason is one of
// "failed (exit N)", "timed out", "not found" or "listed no models".
type ModelListError struct {
	Kind    string
	Command string
	Reason  string
}

func (e *ModelListError) Error() string { return e.Command + " " + e.Reason }

// ListModels serves the explicit `soho models` command. pi and opencode
// always run their listing CLI here: no models cache is read or written.
// Other kinds keep ModelIDs' existing behavior.
func ListModels(kind string, env platform.Env) ([]string, error) {
	if kind == "pi" || kind == "opencode" {
		return listCLIModels(kind, env)
	}
	ids, _, err := modelIDs(kind, env, false)
	return ids, err
}

func modelListCommand(kind string) string {
	if kind == "pi" {
		return "pi --list-models"
	}
	return "opencode models"
}

// modelListFallbackMs is the pi and opencode listing timeout when
// SOHO_MODELS_TIMEOUT is unset or unusable; tests lower it.
var modelListFallbackMs = 20000

// listCLIModels runs an always-fresh pi or opencode listing. It is reachable
// only from ListModels: resolution and spawn never call it. No models cache
// file is read, created or rewritten, even when a stale one is present.
func listCLIModels(kind string, env platform.Env) ([]string, error) {
	shortRaw, short := env.Lookup("SOHO_MODELS_TIMEOUT")
	short = short && shortRaw != ""
	// An unset, unparsable, non-positive or sub-millisecond override keeps
	// the fallback: RunCli applies no deadline at all for 0 ms.
	timeout := modelListFallbackMs
	if short {
		if n, err := strconv.ParseFloat(shortRaw, 64); err == nil && n > 0 {
			if ms := int(n * 1000); ms > 0 {
				timeout = ms
			}
		}
	}
	fail := func(reason string) ([]string, error) {
		return nil, &ModelListError{Kind: kind, Command: modelListCommand(kind), Reason: reason}
	}
	exe, args := "opencode", []string{"models"}
	if kind == "pi" {
		exe, args = "pi", []string{"--list-models"}
	}
	r := platform.RunCli(exe, args, platform.RunOptions{Env: env, TimeoutMs: timeout})
	if r.NotFound {
		return fail("not found")
	}
	if r.TimedOut {
		return fail("timed out")
	}
	if r.Status == nil || *r.Status != 0 {
		code := 1
		if r.Status != nil {
			code = *r.Status
		}
		return fail(fmt.Sprintf("failed (exit %d)", code))
	}
	var out []string
	if kind == "pi" {
		out = ParsePiModels(r.Stdout)
	} else {
		out = ParseOpenCodeModels(r.Stdout)
	}
	if len(out) == 0 {
		return fail("listed no models")
	}
	return out, nil
}

// hasModelSource reports whether the kind's list in modelIDs comes from a
// source that can change behind the 1-hour copy (the cursor/agy/grok CLI
// listing). codex reads its source directly (no copy); the other kinds
// (claude, pi, opencode, ...) have no source here — a copy, when present, is
// all the list there is, and pi/opencode never read one.
func hasModelSource(kind string) bool {
	return kind == "cursor" || kind == "agy" || kind == "grok"
}

// modelIDs reads the model list of the kind from its source. skipCopy
// ignores the 1-hour copy read (the copy is still rewritten on success);
// fromCopy reports whether the returned list came from that copy.
func modelIDs(kind string, env platform.Env, skipCopy bool) ([]string, bool, error) {
	return lookupModelIDs(kind, env, skipCopy, nil)
}

const agyModelListTimeout = 30 * time.Second

type agyProbeLookup struct {
	ctx context.Context
	run func(string, []string, platform.RunOptions) platform.RunResult
}

func lookupModelIDs(kind string, env platform.Env, skipCopy bool, probe *agyProbeLookup) ([]string, bool, error) {
	if kind == "pi" || kind == "opencode" || kind == "omp" {
		return nil, false, nil
	}
	file := ModelsCacheFile(kind, env)
	shortRaw, short := env.Lookup("SOHO_MODELS_TIMEOUT")
	short = short && shortRaw != ""
	if kind != "codex" && !skipCopy {
		if st, err := os.Stat(file); err == nil && st.Size() > 0 && st.ModTime().After(platform.Now().Add(-time.Hour)) {
			if data, err := os.ReadFile(file); err == nil {
				return nonemptyLines(string(data)), true, nil
			}
		}
	}
	seconds := func(fallback float64) (int, bool) {
		if !short {
			return int(fallback * 1000), true
		}
		n, err := strconv.ParseFloat(shortRaw, 64)
		return int(n * 1000), err == nil && n >= 0
	}
	var out []string
	switch kind {
	case "codex":
		name := filepath.Join(platform.HomeDir(platform.Current(), env), ".codex", "models_cache.json")
		var data struct {
			Models []struct {
				Slug any `json:"slug"`
			} `json:"models"`
		}
		if b, err := os.ReadFile(name); err == nil && json.Unmarshal(b, &data) == nil {
			for _, model := range data.Models {
				if model.Slug == nil {
					continue
				}
				value := fmt.Sprint(model.Slug)
				if value != "" {
					out = append(out, value)
				}
			}
		}
	case "cursor", "agy", "grok":
		timeout, ok := seconds(20)
		if kind == "agy" {
			timeout, ok = seconds(agyModelListTimeout.Seconds())
		}
		if ok {
			exe, args := kind, []string{"models"}
			if kind == "cursor" {
				exe, args = "cursor-agent", []string{"--list-models"}
			}
			run := platform.RunCli
			opts := platform.RunOptions{Env: env, TimeoutMs: timeout}
			if probe != nil {
				deadline, bounded := probe.ctx.Deadline()
				remaining := time.Until(deadline).Milliseconds()
				if !bounded || probe.ctx.Err() != nil || remaining <= 0 {
					return nil, false, nil
				}
				// Zero and sub-millisecond overrides can otherwise
				// disable RunCli's timeout. Use the bounded default.
				if timeout <= 0 {
					timeout = int(agyModelListTimeout.Milliseconds())
				}
				run = probe.run
				opts.Context = probe.ctx
				opts.TimeoutMs = int(min(int64(timeout), remaining))
			}
			r := run(exe, args, opts)
			if r.Status != nil && *r.Status == 0 {
				switch kind {
				case "cursor":
					out = ParseCursorModels(r.Stdout)
				case "agy":
					out = ParseAgyModels(r.Stdout)
				case "grok":
					out = ParseGrokModels(r.Stdout)
				}
			}
		}
	}
	if kind != "codex" && !short && len(out) > 0 {
		_ = platform.AtomicWrite(file, strings.Join(out, "\n")+"\n")
	}
	return out, false, nil
}
func nonemptyLines(data string) []string {
	var out []string
	for _, line := range strings.Split(data, "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

type versionKey struct{ id, key string }

func VersionSortDesc(ids []string) []string {
	keyed := make([]versionKey, 0, len(ids))
	partsRE := regexp.MustCompile("[0-9]+")
	for _, id := range ids {
		parts := partsRE.FindAllString(id, -1)
		var b strings.Builder
		for _, part := range parts {
			b.WriteString(strings.Repeat("0", max(0, 6-len(part))))
			b.WriteString(part)
			b.WriteByte('.')
		}
		keyed = append(keyed, versionKey{id: id, key: b.String()})
	}
	sort.SliceStable(keyed, func(i, j int) bool {
		if keyed[i].key != keyed[j].key {
			return keyed[i].key > keyed[j].key
		}
		return keyed[i].id < keyed[j].id
	})
	out := make([]string, 0, len(ids))
	for _, item := range keyed {
		out = append(out, item.id)
	}
	return out
}
func ERERegExp(pattern string) (*regexp.Regexp, bool) {
	if strings.Contains(pattern, "(?") {
		return nil, false
	}
	classes := map[string]string{"alpha": "A-Za-z", "digit": "0-9", "alnum": "A-Za-z0-9", "upper": "A-Z", "lower": "a-z", "xdigit": "0-9A-Fa-f", "space": " \\t\\n\\r\\f\\v", "blank": " \\t", "punct": "!-\\/:-@\\[-`{-~"}
	classRE := regexp.MustCompile("\\[:([a-z]+):\\]")
	pattern = classRE.ReplaceAllStringFunc(pattern, func(match string) string {
		name := match[2 : len(match)-2]
		if value, ok := classes[name]; ok {
			return value
		}
		return match
	})
	pattern = strings.ReplaceAll(pattern, "\\d", "[0-9]")
	pattern = strings.ReplaceAll(pattern, "\\w", "[A-Za-z0-9_]")
	pattern = strings.ReplaceAll(pattern, "\\s", "[\\t\\n\\r\\f\\v ]")
	pattern = asciiFoldPattern(pattern)
	re, err := regexp.Compile(pattern)
	return re, err == nil
}
func asciiFoldPattern(pattern string) string {
	var b strings.Builder
	inClass, escaped := false, false
	runes := []rune(pattern)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if escaped {
			b.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' {
			b.WriteRune(r)
			escaped = true
			continue
		}
		if r == '[' {
			inClass = true
			b.WriteRune(r)
			continue
		}
		if r == ']' {
			inClass = false
			b.WriteRune(r)
			continue
		}
		if inClass {
			// An inverted letter range ([a-Z], [z-a]) is a syntax error in the
			// JS RegExp, so ereRegExp returns null; folding each letter on its
			// own would hide it behind a valid range (aA-zZ is A-z). Keep it
			// as written so Compile fails the same way.
			if isASCIILetter(r) && i+2 < len(runes) && runes[i+1] == '-' && isASCIILetter(runes[i+2]) && r > runes[i+2] {
				b.WriteRune(r)
				b.WriteRune('-')
				b.WriteRune(runes[i+2])
				i += 2
				continue
			}
			if r >= 'a' && r <= 'z' && i+2 < len(runes) && runes[i+1] == '-' && runes[i+2] >= 'a' && runes[i+2] <= 'z' {
				b.WriteRune(r)
				b.WriteRune('-')
				b.WriteRune(runes[i+2])
				b.WriteRune(r - ('a' - 'A'))
				b.WriteRune('-')
				b.WriteRune(runes[i+2] - ('a' - 'A'))
				i += 2
				continue
			}
			if r >= 'A' && r <= 'Z' && i+2 < len(runes) && runes[i+1] == '-' && runes[i+2] >= 'A' && runes[i+2] <= 'Z' {
				b.WriteRune(r + ('a' - 'A'))
				b.WriteRune('-')
				b.WriteRune(runes[i+2] + ('a' - 'A'))
				b.WriteRune(r)
				b.WriteRune('-')
				b.WriteRune(runes[i+2])
				i += 2
				continue
			}
			if r >= 'a' && r <= 'z' {
				b.WriteRune(r)
				b.WriteRune(r - ('a' - 'A'))
				continue
			}
			if r >= 'A' && r <= 'Z' {
				b.WriteRune(r + ('a' - 'A'))
				b.WriteRune(r)
				continue
			}
			b.WriteRune(r)
			continue
		}
		if r >= 'a' && r <= 'z' {
			b.WriteByte('[')
			b.WriteRune(r)
			b.WriteRune(r - ('a' - 'A'))
			b.WriteByte(']')
			continue
		}
		if r >= 'A' && r <= 'Z' {
			b.WriteByte('[')
			b.WriteRune(r + ('a' - 'A'))
			b.WriteRune(r)
			b.WriteByte(']')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
func ResolveModel(kind, spec, effort string, env platform.Env, warn WarnFunc) (string, error) {
	return resolveModel(kind, spec, effort, env, warn, nil)
}

// ResolveAgyProbeModel passes concrete effort IDs through directly and uses
// the normal catalog within the probe deadline for other selectors.
// A bare word is a catalog search. Other selectors match only exact IDs or
// literal effort bases, so a native ID that is absent stays unchanged.
func ResolveAgyProbeModel(ctx context.Context, spec string, env platform.Env, run func(string, []string, platform.RunOptions) platform.RunResult) string {
	if effortSuffixRE.MatchString(spec) {
		return spec
	}
	model, err := resolveModel("agy", spec, "high", env, func(string) {}, &agyProbeLookup{ctx: ctx, run: run})
	if err != nil || model == "" {
		return spec
	}
	return model
}

func resolveModel(kind, spec, effort string, env platform.Env, warn WarnFunc, probe *agyProbeLookup) (string, error) {
	if spec == "" {
		return "", nil
	}
	if warn == nil {
		warn = DefaultWarn
	}
	ids, fromCopy, _ := lookupModelIDs(kind, env, false, probe)
	if len(ids) == 0 {
		if kind == "claude" {
			if m := claudeDottedAliasRE.FindStringSubmatch(spec); m != nil {
				return "claude-" + strings.ToLower(m[1]) + "-" + m[2] + "-" + m[3], nil
			}
		}
		return spec, nil
	}
	match := resolveModelInList
	if probe != nil {
		match = resolveAgyProbeModelInList
	}
	if resolved, matched := match(kind, spec, effort, ids); matched {
		return resolved, nil
	}
	// A list that came from the 1-hour copy may be stale (the source gained
	// a model since the copy was written): re-read the source once, ignoring
	// the copy (and rewriting it), before declaring the spec unmatched. Only
	// the kinds with a real source are re-read — the copy of a kind without
	// one is all the list there is, and a now-empty source passes the spec
	// through unchanged, as with a cold list.
	if fromCopy && hasModelSource(kind) {
		fresh, _, _ := lookupModelIDs(kind, env, true, probe)
		if len(fresh) == 0 {
			return spec, nil
		}
		if resolved, matched := match(kind, spec, effort, fresh); matched {
			return resolved, nil
		}
	}
	if kind == "cursor" {
		return "", &platform.ExitError{Code: 2, Msg: fmt.Sprintf("no cursor model matches '%s'; cursor-agent rejects model ids absent from --list-models (context overrides are only usable when that model/account exposes them)", spec)}
	}
	warn(fmt.Sprintf("no %s model matches '%s'; passing it through unchanged", kind, spec))
	return spec, nil
}

func resolveAgyProbeModelInList(kind, spec, effort string, ids []string) (string, bool) {
	if strings.IndexFunc(spec, func(r rune) bool { return !isASCIILetter(r) }) < 0 {
		return resolveModelInList(kind, spec, effort, ids)
	}
	for _, id := range ids {
		if id == spec {
			return id, true
		}
	}
	return resolveModelInList(kind, "^"+regexp.QuoteMeta(spec)+"$", effort, ids)
}

// resolveModelInList tries the spec against one concrete list — exact id,
// dotted alias, or case-insensitive ERE over the ids, with `a|b`
// alternatives in order — and reports whether it matched. For cursor/agy a
// matched base with no listed id resolves to "" (the CLI's own default),
// which also counts as matched.
func resolveModelInList(kind, spec, effort string, ids []string) (string, bool) {
	for _, raw := range strings.Split(spec, "|") {
		alt := strings.TrimSpace(raw)
		if alt == "" {
			continue
		}
		for _, id := range ids {
			if id == alt {
				return alt, true
			}
		}
		if strings.Contains(alt, ".") {
			n := strings.ReplaceAll(alt, ".", "-")
			var candidates []string
			for _, id := range ids {
				if id == n || strings.HasSuffix(id, "-"+n) {
					candidates = append(candidates, id)
				}
			}
			if sorted := VersionSortDesc(candidates); len(sorted) > 0 {
				return sorted[0], true
			}
		}
		re, ok := ERERegExp(alt)
		if kind == "cursor" || kind == "agy" {
			seen := map[string]bool{}
			var bases []string
			for _, id := range ids {
				base := effortSuffixRE.ReplaceAllString(id, "")
				if !seen[base] {
					seen[base] = true
					bases = append(bases, base)
				}
			}
			var matched []string
			if ok {
				for _, base := range bases {
					if re.MatchString(base) && !strings.HasSuffix(base, "-fast") {
						matched = append(matched, base)
					}
				}
			}
			sorted := VersionSortDesc(matched)
			base := ""
			if len(sorted) > 0 {
				base = sorted[0]
			}
			if base == "" {
				continue
			}
			if effort != "" {
				for _, id := range ids {
					if id == base+"-"+effort {
						return id, true
					}
				}
			}
			for _, id := range ids {
				if id == base {
					return id, true
				}
			}
			for _, candidate := range []string{"max", "xhigh", "high", "medium", "low", "minimal"} {
				if effort != "" && EffortRank(candidate) > EffortRank(effort) {
					continue
				}
				for _, id := range ids {
					if id == base+"-"+candidate {
						return id, true
					}
				}
			}
			prefix, err := regexp.Compile("^" + base + "-")
			for _, id := range ids {
				if err == nil && prefix.MatchString(id) {
					return id, true
				}
				if err != nil && strings.HasPrefix(id, base+"-") {
					return id, true
				}
			}
			return "", true
		}
		if ok {
			var matched []string
			for _, id := range ids {
				if re.MatchString(id) {
					matched = append(matched, id)
				}
			}
			sorted := VersionSortDesc(matched)
			if len(sorted) > 0 {
				return sorted[0], true
			}
		}
	}
	return "", false
}

type cachedCodexModel struct {
	Slug   string `json:"slug"`
	Levels []struct {
		Effort string `json:"effort"`
	} `json:"supported_reasoning_levels"`
}

func codexModels(env platform.Env) []cachedCodexModel {
	var data struct {
		Models []cachedCodexModel `json:"models"`
	}
	file := filepath.Join(platform.HomeDir(platform.Current(), env), ".codex", "models_cache.json")
	b, err := os.ReadFile(file)
	if err != nil || json.Unmarshal(b, &data) != nil {
		return nil
	}
	return data.Models
}
func CodexModelCeiling(model string, env platform.Env) string {
	best, rank := "", 0
	for _, entry := range codexModels(env) {
		if entry.Slug != model {
			continue
		}
		for _, level := range entry.Levels {
			r := EffortRank(level.Effort)
			if r > rank {
				best, rank = level.Effort, r
			}
		}
	}
	return best
}
func CodexEffortCeiling(model string, env platform.Env) string {
	if ceiling := CodexModelCeiling(model, env); ceiling != "" {
		return ceiling
	}
	return "xhigh"
}
func CursorModelWithEffort(model, effort string, env platform.Env, warn WarnFunc) string {
	if warn == nil {
		warn = DefaultWarn
	}
	suffix := regexp.MustCompile("-(low|medium|high|xhigh|max|none)$").FindStringSubmatch(model)
	if suffix != nil {
		if suffix[1] == effort || effort == "" {
			return model
		}
		warn(fmt.Sprintf("cursor model '%s' already encodes effort '%s'; --effort %s ignored", model, suffix[1], effort))
		return model
	}
	r := platform.RunCli("cursor-agent", []string{"--list-models"}, platform.RunOptions{Env: env, TimeoutMs: 20000})
	ids := ParseCursorModels(r.Stdout)
	target := model + "-" + effort
	for _, id := range ids {
		if id == target {
			return target
		}
	}
	for _, id := range ids {
		if id == model {
			warn(fmt.Sprintf("cursor has no '%s'; using '%s' (effort = model default)", target, model))
			return model
		}
	}
	warn(fmt.Sprintf("cursor model '%s' not in --list-models; passing it through unchanged", model))
	return model
}

func isASCIILetter(r rune) bool { return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') }
