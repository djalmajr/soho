package core

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/djalmajr/soho/internal/platform"
	textutil "github.com/djalmajr/soho/internal/text"
)

var writeConfigAtomic = platform.AtomicWrite

type ConfigEntry struct {
	Value    string
	Source   string
	Original string
}

// ConfigLayer is one config layer read as a unit by LoadConfig: the source
// label plus every entry the file contributed, keyed by normalized key. A
// reader of the layers (for example the worker_messages policy) resolves a
// named entry as a whole from the highest layer that names it.
type ConfigLayer struct {
	Source       string
	File         string
	ReadError    error
	Entries      map[string]ConfigEntry
	OriginalKeys []string
}

// Config records the normalized key values and preserves first insertion order.
type Config struct {
	Entries map[string]ConfigEntry
	Order   []string
	Sources []string
	Layers  []ConfigLayer
	// User is the explicit user configuration selection the config was
	// loaded with (LoadConfigFor); zero for LoadConfig.
	User UserSelection
}

// ConfigLayerPath names one file LoadConfig reads, with its source label.
type ConfigLayerPath struct {
	Source string // defaults | user | project | session
	File   string
}

// ConfigLayerPaths returns only the paths captured during loading. A fresh
// config has no paths; resolving a session requires the loaded base layers.
func ConfigLayerPaths(ctx *Config, env platform.Env, cwd string) []ConfigLayerPath {
	if ctx == nil {
		return nil
	}
	paths := make([]ConfigLayerPath, 0, len(ctx.Layers))
	for _, layer := range ctx.Layers {
		paths = append(paths, ConfigLayerPath{Source: layer.Source, File: layer.File})
	}
	return paths
}

func baseConfigLayerPaths(env platform.Env, cwd string, sel UserSelection) []ConfigLayerPath {
	paths := []ConfigLayerPath{
		{Source: "defaults", File: filepath.Join(platform.SkillDir(env), "config.defaults")},
		{Source: "user", File: EffectiveUserConfigFile(platform.Current(), env, sel)},
	}
	if file := ProjectConfigFileUsed(env, cwd); file != "" {
		paths = append(paths, ConfigLayerPath{Source: "project", File: file})
	}
	return paths
}

var ConfigScalarKeys = []string{
	"orchestrator_name", "layout", "regrid", "max_workers", "split_max_panes",
	"split_min_pane", "herd_label", "herd_label_max", "reuse_workers",
	"multi_role", "panes", "lanes", "pane_mode", "flex_extra", "flex_roles",
	"worker_context", "brief_lint", "brief_lint_aliases", "brief_lint_placeholders", "approvals",
	"auto_approve", "max_auto_approvals", "max_effort", "family_check",
	"settled_grace", "spawn_timeout", "dispatch_timeout", "provider_retries",
	"provider_retry_delay", "provider_capacity_texts", "provider_error_texts",
	"prompt_check_seconds", "prompt_settle_seconds", "stuck_warn_minutes", "context_warn_percent", "state_dir",
	"report_language", "notify", "feedback", "feedback_repo", "feedback_dir", "feedback_to",
	"setup_target", "inbound", "worker_messages", "metrics",
	"pressure_disk_free_percent", "pressure_swap_percent",
	"pressure_refuse_disk_free_percent", "pressure_refuse_swap_percent",
}

// jobMachineKeys are known to ConfigKeyOk. They stay out of ConfigScalarKeys
// so `soho config` keeps the row order pinned by the frozen parity oracle.
var jobMachineKeys = []string{
	"machine_label", "job_orgs", "job_repos_root", "job_timeout",
	"job_blocked_timeout", "job_finishing_timeout", "job_auto_cleanup",
	"job_human_input_minutes", "job_wake_cmd",
}

var KnownKinds = []string{"claude", "codex", "grok", "agy", "gemini", "cursor", "pi", "opencode"}
var EffortLadder = []string{"low", "medium", "high", "xhigh", "max"}

var dottedKeyRE = regexp.MustCompile(`^(?:role\.[a-z][a-z0-9_-]*\.(?:kind|model|effort|mcp|args)|lane\.[a-z][a-z0-9_-]*\.(?:roles|kind|model|effort|approvals|panes|args|mcp)|model\.[a-z][a-z0-9_.-]+|effort\.[a-z][a-z0-9_-]+|context_window\.[a-z][a-z0-9_-]+|args\.[a-z][a-z0-9_-]+|worker_messages\.rules\.[a-z][a-z0-9_]*\.(?:from|to|types|scope|enabled))$`)
var decimalRE = regexp.MustCompile(`^[0-9]+$`)
var positiveDecimalRE = regexp.MustCompile(`^[1-9][0-9]*$`)

func NormalizeKey(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		if isJSWhitespace(r) {
			continue
		}
		if r == '.' || r == '-' {
			b.WriteByte('_')
			continue
		}
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func isJSWhitespace(r rune) bool {
	return r == '\t' || r == '\n' || r == '\v' || r == '\f' || r == '\r' || r == ' ' || unicode.Is(unicode.Zs, r) || r == '\u2028' || r == '\u2029' || r == '\ufeff'
}

func LoadConfig(env platform.Env, cwd string) Config {
	return LoadConfigFor(env, cwd, UserSelection{})
}

// LoadConfigFor is LoadConfig with an explicit user configuration
// selection: only the user layer's file comes from sel (UserConfigFiles);
// every other layer, and every git or herdr call the load makes, uses env
// unchanged.
func LoadConfigFor(env platform.Env, cwd string, sel UserSelection) Config {
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	ctx := Config{Entries: make(map[string]ConfigEntry), Order: make([]string, 0), Sources: make([]string, 0, 4), User: sel}
	for _, layer := range baseConfigLayerPaths(env, cwd, sel) {
		loadConfigFile(layer.File, layer.Source, &ctx)
	}
	if problem := projectConfigProblem(env, cwd); problem != nil {
		// Legacy values still use their existing fallback. New policy reads
		// must see an invalid or inaccessible preferred layer rather than
		// silently inheriting permissions from a lower one.
		ctx.Layers = append(ctx.Layers, *problem)
	}
	// state_dir may be supplied by any of the three preceding layers.
	if file := SessionConfPath(&ctx, env, cwd); file != "" {
		loadConfigFile(file, "session", &ctx)
	}
	return ctx
}

func projectConfigProblem(env platform.Env, cwd string) *ConfigLayer {
	roots := []string{platform.ProjectRoot(env, cwd), platform.StateProjectRoot(env, cwd)}
	seen := map[string]bool{}
	for _, root := range roots {
		if seen[root] {
			continue
		}
		seen[root] = true
		for _, file := range []string{filepath.Join(root, ".agents", "soho.conf")} {
			info, err := os.Lstat(file)
			if os.IsNotExist(err) {
				continue
			}
			if err == nil && info.Mode()&os.ModeSymlink != 0 {
				info, err = os.Stat(file)
			}
			if err == nil && info.Mode().IsRegular() {
				return nil
			}
			if err == nil {
				err = fmt.Errorf("configuration path is not a regular file")
			}
			return &ConfigLayer{Source: "project", File: file, ReadError: err, Entries: map[string]ConfigEntry{}}
		}
	}
	return nil
}

func loadConfigFile(file, label string, ctx *Config) {
	if absolute, err := filepath.Abs(file); err == nil {
		file = absolute
	}
	raw, err := platform.ReadTextFile(file)
	layer := make(map[string]ConfigEntry)
	record := ConfigLayer{Source: label, File: file, Entries: layer}
	if err != nil {
		if !os.IsNotExist(err) {
			record.ReadError = err
		}
		ctx.Layers = append(ctx.Layers, record)
		return
	}
	ctx.Sources = append(ctx.Sources, label)
	applyConfigText(raw, &record, ctx)
	ctx.Layers = append(ctx.Layers, record)
}

// applyConfigText parses one file's decoded text into ctx and the file's
// layer record, as loadConfigFile reads every layer.
func applyConfigText(raw string, record *ConfigLayer, ctx *Config) {
	layer := record.Entries
	for _, rawLine := range splitLines(raw) {
		if hash := strings.IndexByte(rawLine, '#'); hash >= 0 {
			rawLine = rawLine[:hash]
		}
		line := trimJSWhitespace(rawLine)
		if line == "" {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		rawKey := trimJSWhitespace(line[:eq])
		record.OriginalKeys = append(record.OriginalKeys, rawKey)
		key := NormalizeKey(rawKey)
		value := trimJSWhitespace(line[eq+1:])
		if len(value) >= 2 && strings.HasPrefix(value, "\"") && strings.HasSuffix(value, "\"") {
			value = value[1 : len(value)-1]
		}
		if key == "" {
			continue
		}
		if _, exists := ctx.Entries[key]; !exists {
			ctx.Order = append(ctx.Order, key)
		}
		ctx.Entries[key] = ConfigEntry{Value: value, Source: record.Source, Original: rawKey}
		layer[key] = ctx.Entries[key]
	}
}

// ConfigPair is one key/value read from a config file: the normalized key,
// the raw spelling of the last occurrence (JS-whitespace trimmed, so a
// leading BOM does not survive), and the entry value.
type ConfigPair struct {
	Key      string
	Original string
	Value    string
}

// ConfigTextPairs parses bytes already read from one config file with the
// shared parser (DecodeText, then applyConfigText): one pair per key in
// first-occurrence order, the last occurrence of a key winning.
func ConfigTextPairs(data []byte) []ConfigPair {
	ctx := Config{Entries: make(map[string]ConfigEntry), Order: make([]string, 0), Sources: make([]string, 0)}
	record := ConfigLayer{Source: "file", Entries: make(map[string]ConfigEntry)}
	applyConfigText(platform.DecodeText(data), &record, &ctx)
	pairs := make([]ConfigPair, 0, len(ctx.Order))
	for _, key := range ctx.Order {
		entry := ctx.Entries[key]
		pairs = append(pairs, ConfigPair{Key: key, Original: entry.Original, Value: entry.Value})
	}
	return pairs
}

// ReadConfigFilePairs reads one file with the shared single-file config
// parser (loadConfigFile) and returns one pair per key in first-occurrence
// order; the last occurrence of a key wins. An absent file is an error
// matching os.IsNotExist; a read failure is the parser's ReadError.
func ReadConfigFilePairs(file string) ([]ConfigPair, error) {
	if _, err := os.Stat(file); err != nil {
		return nil, err
	}
	ctx := Config{Entries: make(map[string]ConfigEntry), Order: make([]string, 0), Sources: make([]string, 0)}
	loadConfigFile(file, "file", &ctx)
	layer := ctx.Layers[len(ctx.Layers)-1]
	if layer.ReadError != nil {
		return nil, layer.ReadError
	}
	pairs := make([]ConfigPair, 0, len(ctx.Order))
	for _, key := range ctx.Order {
		entry := ctx.Entries[key]
		pairs = append(pairs, ConfigPair{Key: key, Original: entry.Original, Value: entry.Value})
	}
	return pairs, nil
}

func splitLines(text string) []string {
	if text == "" {
		return nil
	}
	if strings.HasSuffix(text, "\n") {
		text = text[:len(text)-1]
	}
	return strings.Split(text, "\n")
}

func trimJSWhitespace(value string) string {
	return strings.TrimFunc(value, isJSWhitespace)
}

func Cfg(ctx *Config, key, fallback string, env platform.Env) string {
	if value := env.Get("SOHO_" + strings.ToUpper(key)); value != "" {
		return value
	}
	if entry, ok := ctx.Entries[key]; ok && entry.Value != "" {
		return entry.Value
	}
	return fallback
}

func CfgSource(ctx *Config, key string, env platform.Env) string {
	if env.Get("SOHO_"+strings.ToUpper(key)) != "" {
		return "env"
	}
	if entry, ok := ctx.Entries[key]; ok {
		return entry.Source
	}
	return "builtin"
}

func ConfigKeyOk(key string) bool {
	for _, scalar := range ConfigScalarKeys {
		if scalar == key {
			return true
		}
	}
	for _, scalar := range jobMachineKeys {
		if scalar == key {
			return true
		}
	}
	return dottedKeyRE.MatchString(key)
}

func ConfigRolesOk(raw string, env platform.Env, cwd string) bool {
	if raw == "" {
		return false
	}
	for _, role := range strings.Split(raw, ",") {
		role = trimJSWhitespace(role)
		if role == "" || RoleFile(role, env, cwd) == "" {
			return false
		}
	}
	return true
}

func ConfigValueOk(key, value string, env platform.Env, cwd string) bool {
	if strings.ContainsAny(value, "\n\t#") {
		return false
	}
	switch {
	case key == "approvals" || strings.HasPrefix(key, "lane.") && strings.HasSuffix(key, ".approvals"):
		return value == "ask" || value == "edits" || value == "full"
	case key == "max_workers" || key == "provider_retries" || key == "provider_retry_delay" || key == "prompt_check_seconds" || key == "prompt_settle_seconds" || key == "stuck_warn_minutes" || key == "pressure_disk_free_percent" || key == "pressure_swap_percent" || key == "pressure_refuse_disk_free_percent" || key == "pressure_refuse_swap_percent":
		return decimalRE.MatchString(value)
	case key == "multi_role" || key == "reuse_workers" || key == "lanes":
		return value == "on" || value == "off"
	case key == "setup_target":
		return value == "canonical" || value == "local"
	case key == "inbound":
		return value == "auto" || value == "off"
	case key == "worker_messages":
		return value == "off" || value == "policy"
	case key == "panes":
		return value == "2" || value == "3" || value == "4"
	case key == "pane_mode":
		return value == "strict" || value == "flex"
	case key == "flex_extra":
		return decimalRE.MatchString(value)
	case key == "flex_roles":
		return ConfigRolesOk(value, env, cwd)
	case strings.HasPrefix(key, "lane.") && strings.HasSuffix(key, ".panes"):
		return positiveDecimalRE.MatchString(value)
	case strings.HasPrefix(key, "role.") && strings.HasSuffix(key, ".kind"), strings.HasPrefix(key, "lane.") && strings.HasSuffix(key, ".kind"):
		return contains(KnownKinds, value)
	case strings.HasPrefix(key, "lane.") && strings.HasSuffix(key, ".roles"):
		return ConfigRolesOk(value, env, cwd)
	case strings.HasPrefix(key, "lane.") && strings.HasSuffix(key, ".effort"):
		return contains(EffortLadder, value)
	case strings.HasPrefix(key, "role.") && strings.HasSuffix(key, ".mcp"), strings.HasPrefix(key, "lane.") && strings.HasSuffix(key, ".mcp"):
		return value == "inherit" || value == "none"
	case key == "job_timeout":
		return jobTimeoutValueOk(value)
	case key == "job_blocked_timeout":
		return jobBlockedTimeoutValueOk(value)
	case key == "job_finishing_timeout":
		return jobFinishingTimeoutValueOk(value)
	case key == "job_auto_cleanup":
		return value == "on" || value == "off"
	case key == "job_human_input_minutes":
		return jobHumanInputMinutesValueOk(value)
	default:
		return true
	}
}

// jobTimeoutValueOk is the contract range for the default job budget: 1..1440 minutes.
func jobTimeoutValueOk(value string) bool {
	if !decimalRE.MatchString(value) {
		return false
	}
	n, err := strconv.Atoi(value)
	return err == nil && n >= 1 && n <= 1440
}

// jobFinishingTimeoutValueOk is the contract range for the terminal
// procedure's (finishing) budget: 1..1440 minutes.
func jobFinishingTimeoutValueOk(value string) bool {
	if !decimalRE.MatchString(value) {
		return false
	}
	n, err := strconv.Atoi(value)
	return err == nil && n >= 1 && n <= 1440
}

// jobHumanInputMinutesValueOk is the contract range for the human input
// wait: 1..1440 minutes.
func jobHumanInputMinutesValueOk(value string) bool {
	if !decimalRE.MatchString(value) {
		return false
	}
	n, err := strconv.Atoi(value)
	return err == nil && n >= 1 && n <= 1440
}

// jobBlockedTimeoutValueOk is the contract range for the default blocked
// budget: 1..43200 minutes (one minute to 30 days).
func jobBlockedTimeoutValueOk(value string) bool {
	if !decimalRE.MatchString(value) {
		return false
	}
	n, err := strconv.Atoi(value)
	return err == nil && n >= 1 && n <= 43200
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

var contextWindowValueRE = regexp.MustCompile(`^(?:[1-9][0-9]*k|[1-9][0-9]*)$`)

// ContextWindowValueOk reports whether value is a context_window.<kind>
// value: a positive integer token count, optionally with a k suffix (500k).
func ContextWindowValueOk(value string) bool {
	return contextWindowValueRE.MatchString(value)
}

// ContextWindowKey is the normalized config key context_window.<kind>.
func ContextWindowKey(kind string) string {
	return "context_window_" + kind
}

// ContextWindowEntries reports the effective context_window.<kind> values,
// the same layers as Cfg (environment first, then the config files): for
// every kind present in a config layer or in the environment, kind →
// (value, source), empty values dropped.
func ContextWindowEntries(ctx *Config, env platform.Env) map[string]ConfigEntry {
	kinds := make(map[string]bool)
	for key := range ctx.Entries {
		if strings.HasPrefix(key, "context_window_") {
			kinds[strings.TrimPrefix(key, "context_window_")] = true
		}
	}
	for name := range env {
		if strings.HasPrefix(name, "SOHO_CONTEXT_WINDOW_") {
			kinds[strings.ToLower(NormalizeKey(strings.TrimPrefix(name, "SOHO_CONTEXT_WINDOW_")))] = true
		}
	}
	out := make(map[string]ConfigEntry, len(kinds))
	for kind := range kinds {
		key := ContextWindowKey(kind)
		if value := Cfg(ctx, key, "", env); value != "" {
			out[kind] = ConfigEntry{Value: value, Source: CfgSource(ctx, key, env)}
		}
	}
	return out
}

// ProjectConfigFileUsed returns the file the project layer reads for cwd:
// the checkout's .agents/soho.conf when it exists, or, when cwd is a
// linked worktree without one, the main checkout's (the StateProjectRoot
// root's) file; "" when no project config exists, so the layer
// contributes nothing.
func ProjectConfigFileUsed(env platform.Env, cwd string) string {
	root := platform.ProjectRoot(env, cwd)
	if file := filepath.Join(root, ".agents", "soho.conf"); isFile(file) {
		return file
	}
	stateRoot := platform.StateProjectRoot(env, cwd)
	if stateRoot == root {
		return ""
	}
	main := filepath.Join(stateRoot, ".agents", "soho.conf")
	if isFile(main) {
		return main
	}
	return ""
}

func ConfigFileFor(where string, env platform.Env, cwd string) string {
	if where == "user" {
		return platform.UserConfigPath(platform.Current(), env)
	}
	root := platform.ProjectRoot(env, cwd)
	newProject := filepath.Join(root, ".agents", "soho.conf")
	if isFile(newProject) {
		return newProject
	}
	stateRoot := platform.StateProjectRoot(env, cwd)
	if stateRoot == root {
		return newProject
	}
	return filepath.Join(stateRoot, ".agents", "soho.conf")
}

func StateRootPath(ctx *Config, env platform.Env, cwd string) string {
	root := platform.StateProjectRoot(env, cwd)
	dir := env.Get("SOHO_DIR")
	if dir == "" {
		dir = StateDirSetting(ctx, env, cwd)
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(root, dir)
	}
	return filepath.Clean(dir)
}

// SessionStateRootPath is the state root under which a command run from
// cwd with env looks up its session file: StateRootPath over the layers
// LoadConfig reads before the session (defaults, the environment's user
// file and the project file), exactly the root SessionConfPath joins the
// workspace to.
func SessionStateRootPath(env platform.Env, cwd string) string {
	ctx := Config{Entries: make(map[string]ConfigEntry), Order: make([]string, 0), Sources: make([]string, 0, 3)}
	for _, layer := range baseConfigLayerPaths(env, cwd, UserSelection{}) {
		loadConfigFile(layer.File, layer.Source, &ctx)
	}
	return StateRootPath(&ctx, env, cwd)
}

func StateDirSetting(ctx *Config, env platform.Env, cwd string) string {
	if value := env.Get("SOHO_STATE_DIR"); value != "" {
		return value
	}
	if entry, ok := ctx.Entries["state_dir"]; ok && entry.Source != "defaults" && entry.Value != "" {
		return entry.Value
	}
	return ".soho"
}

// Nowrite reads the read-only gate: SOHO_NOWRITE=1 decides.
func Nowrite(env platform.Env) bool {
	return env.Get("SOHO_NOWRITE") == "1"
}

func RelativeStatePath(root, statePath string) string {
	rel, err := filepath.Rel(root, statePath)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return ""
	}
	return rel
}

func StateGitignoreRel(root, statePath string) string {
	return filepath.ToSlash(RelativeStatePath(root, statePath))
}

func StateRoot(ctx *Config, env platform.Env, cwd string) string {
	root := platform.StateProjectRoot(env, cwd)
	dir := StateRootPath(ctx, env, cwd)
	rel := StateGitignoreRel(root, dir)
	// The append below writes root/.gitignore: when the state root lives
	// inside the installed skill (a skill that is a git checkout), that would
	// create or change the .gitignore inside the skill. Read-only callers
	// (layout-plan resolves the state path through StateRoot/StateDirPath)
	// must stay side-effect free there, so the append is skipped for a state
	// path inside the skill; StateRoot still returns the path.
	if rel != "" && !Nowrite(env) && StatePathInSkill(env, dir) == "" {
		result := platform.RunCli("git", []string{"-C", root, "rev-parse", "--is-inside-work-tree"}, platform.RunOptions{Env: env, Cwd: root})
		if result.Status != nil && *result.Status == 0 && GitignoreNeeds(root, rel, env) {
			file := filepath.Join(root, ".gitignore")
			text, err := platform.ReadTextFile(file)
			if err != nil {
				text = ""
			}
			appendText := GitignoreAfter(text, rel)[len(text):]
			if appendText != "" {
				f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o666)
				if err != nil {
					panic(err)
				}
				if _, err := f.WriteString(appendText); err != nil {
					_ = f.Close()
					panic(err)
				}
				if err := f.Close(); err != nil {
					panic(err)
				}
			}
		}
	}
	return dir
}

func GitignoreNeeds(root, rel string, env platform.Env) bool {
	result := platform.RunCli("git", []string{"-C", root, "check-ignore", "-q", rel}, platform.RunOptions{Env: env, Cwd: root, TimeoutMs: 30_000})
	if result.Status == nil || *result.Status != 1 {
		return false
	}
	text, err := platform.ReadTextFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		return true
	}
	names := map[string]bool{rel: true, rel + "/": true, "/" + rel: true, "/" + rel + "/": true}
	for _, line := range strings.Split(text, "\n") {
		if names[strings.TrimSuffix(line, "\r")] {
			return false
		}
	}
	return true
}

func GitignoreAfter(text, rel string) string {
	sep := ""
	if text != "" && !strings.HasSuffix(text, "\n") {
		sep = "\n"
	}
	return text + sep + rel + "/\n"
}

func ConfigWritePair(dest, key, value string, env platform.Env, cwd string) {
	configWritePair(dest, key, value, env, cwd, "config set")
}

func configWritePair(dest, key, value string, env platform.Env, cwd, command string) {
	raw := ""
	if _, err := os.Stat(dest); err == nil {
		var readErr error
		raw, readErr = platform.ReadTextFile(dest)
		if readErr != nil {
			platform.Die(fmt.Sprintf("%s: could not rewrite %s (file left untouched)", command, dest), 4)
		}
	}
	out := make([]string, 0)
	found := false
	for _, rawLine := range splitLines(raw) {
		body, comment := splitTrailingComment(rawLine)
		stripped := trimJSWhitespace(body)
		if stripped == "" || strings.HasPrefix(stripped, "#") {
			out = append(out, rawLine)
			continue
		}
		eq := strings.IndexByte(stripped, '=')
		if eq < 0 {
			out = append(out, rawLine)
			continue
		}
		k := trimJSWhitespace(stripped[:eq])
		if k == key {
			if !found {
				out = append(out, key+"="+value+comment)
			}
			found = true
			continue
		}
		out = append(out, rawLine)
	}
	if !found {
		out = append(out, key+"="+value)
	}
	content := strings.Join(out, "\n") + "\n"
	if !strings.Contains(content, key+"=") {
		platform.Die(fmt.Sprintf("%s: rewrite of %s dropped %s (file left untouched)", command, dest, key), 4)
	}
	if err := writeConfigAtomic(dest, content); err != nil {
		platform.Die(fmt.Sprintf("%s: could not rewrite %s (file left untouched)", command, dest), 4)
	}
}

func splitTrailingComment(line string) (string, string) {
	idx := -1
	for i := 0; i+1 < len(line); i++ {
		if (line[i] == ' ' || line[i] == '\t') && line[i+1] == '#' && !strings.ContainsAny(line[i:], "\r\u2028\u2029") {
			idx = i
			break
		}
	}
	if idx < 0 {
		return line, ""
	}
	return line[:idx], line[idx:]
}

func FileKeyValue(file, key string) string {
	raw, err := platform.ReadTextFile(file)
	if err != nil {
		return ""
	}
	value := ""
	for _, rawLine := range splitLines(raw) {
		body, _ := splitTrailingComment(rawLine)
		stripped := trimJSWhitespace(body)
		if stripped == "" || strings.HasPrefix(stripped, "#") {
			continue
		}
		eq := strings.IndexByte(stripped, '=')
		if eq < 0 {
			continue
		}
		if trimJSWhitespace(stripped[:eq]) == key {
			value = trimJSWhitespace(stripped[eq+1:])
		}
	}
	return value
}

func ConfigClearKey(file, key string) {
	raw, err := platform.ReadTextFile(file)
	if err != nil {
		platform.Die(fmt.Sprintf("session clear: could not rewrite %s (file left untouched)", file), 4)
	}
	out := make([]string, 0)
	for _, rawLine := range splitLines(raw) {
		body, _ := splitTrailingComment(rawLine)
		stripped := trimJSWhitespace(body)
		if stripped != "" && !strings.HasPrefix(stripped, "#") {
			if eq := strings.IndexByte(stripped, '='); eq >= 0 && trimJSWhitespace(stripped[:eq]) == key {
				continue
			}
		}
		out = append(out, rawLine)
	}
	content := strings.Join(out, "\n")
	if len(out) > 0 {
		content += "\n"
	}
	if err := writeConfigAtomic(file, content); err != nil {
		platform.Die(fmt.Sprintf("session clear: could not rewrite %s (file left untouched)", file), 4)
	}
}

func DottedKeyName(key string, ctx *Config, env platform.Env, cwd string) string {
	parts := strings.Split(key, "_")
	if len(parts) == 0 {
		return key
	}
	head := parts[0]
	if head == "args" || head == "effort" {
		return strings.Join(parts, ".")
	}
	if head == "context" && len(parts) >= 3 && parts[1] == "window" {
		return "context_window." + strings.Join(parts[2:], ".")
	}
	if head == "worker" && len(parts) >= 5 && parts[1] == "messages" && parts[2] == "rules" {
		// worker_messages.rules.<name>.<field>: the name may hold underscores,
		// so the field is the longest known suffix of the joined tail.
		rest := strings.Join(parts[3:], "_")
		for _, field := range []string{"enabled", "types", "scope", "from", "to"} {
			suffix := "_" + field
			if strings.HasSuffix(rest, suffix) && len(rest) > len(suffix) && ruleNameRE.MatchString(strings.TrimSuffix(rest, suffix)) {
				return "worker_messages.rules." + strings.TrimSuffix(rest, suffix) + "." + field
			}
		}
		return strings.Join(parts, ".")
	}
	if head == "model" {
		position := parts[len(parts)-1]
		if len(parts) >= 3 && (position == "worker" || position == "orchestrator") {
			return "model." + strings.Join(parts[1:len(parts)-1], ".") + "." + position
		}
		return strings.Join(parts, ".")
	}
	if (head == "role" || head == "lane") && len(parts) >= 3 {
		attr := parts[len(parts)-1]
		middle := strings.Join(parts[1:len(parts)-1], "_")
		known := make(map[string]string)
		if head == "role" {
			for _, dir := range RoleDirs(env, cwd) {
				entries, err := os.ReadDir(dir)
				if err != nil {
					continue
				}
				for _, entry := range entries {
					name := entry.Name()
					if strings.HasSuffix(name, ".md") {
						role := strings.TrimSuffix(name, ".md")
						norm := strings.ReplaceAll(role, "-", "_")
						if _, exists := known[norm]; !exists {
							known[norm] = role
						}
					}
				}
			}
		} else {
			originalRE := regexp.MustCompile(`^lane\.([A-Za-z0-9_-]+)\.`)
			for _, k := range ctx.Order {
				if !strings.HasPrefix(k, "lane_") {
					continue
				}
				entry := ctx.Entries[k]
				if match := originalRE.FindStringSubmatch(entry.Original); match != nil {
					spelling := match[1]
					norm := strings.ReplaceAll(spelling, "-", "_")
					if _, exists := known[norm]; !exists {
						known[norm] = spelling
					}
				}
			}
			for _, lane := range LaneNames(ctx, env) {
				norm := strings.ReplaceAll(lane, "-", "_")
				if _, exists := known[norm]; !exists {
					known[norm] = lane
				}
			}
		}
		if spelling, ok := known[strings.ReplaceAll(middle, "-", "_")]; ok {
			return head + "." + spelling + "." + attr
		}
		return head + "." + middle + "." + attr
	}
	return strings.Join(parts, ".")
}

func CmdConfig(ctx *Config, env platform.Env, cwd string) {
	row := func(key, value, source string) string {
		return padUTF16(key, 18) + " " + padUTF16(value, 30) + " " + source
	}
	lines := []string{row("KEY", "VALUE", "SOURCE")}
	for _, key := range ConfigScalarKeys {
		value := Cfg(ctx, key, "", env)
		if key == "state_dir" && CfgSource(ctx, key, env) == "defaults" {
			value = StateDirSetting(ctx, env, cwd)
		}
		lines = append(lines, row(key, value, CfgSource(ctx, key, env)))
	}
	dotted := map[string]bool{}
	for _, key := range ctx.Order {
		if isDottedNormalizedKey(key) {
			dotted[key] = true
		}
	}
	for name, value := range env {
		if !strings.HasPrefix(name, "SOHO_") || value == "" {
			continue
		}
		key := strings.ToLower(NormalizeKey(strings.TrimPrefix(name, "SOHO_")))
		if isDottedNormalizedKey(key) {
			dotted[key] = true
		}
	}
	keys := make([]string, 0, len(dotted))
	for key := range dotted {
		keys = append(keys, key)
	}
	textutil.SortUTF16(keys)
	for _, key := range keys {
		entry := ctx.Entries[key]
		name := entry.Original
		if name == "" {
			name = DottedKeyName(key, ctx, env, cwd)
		}
		lines = append(lines, row(name, Cfg(ctx, key, "", env), CfgSource(ctx, key, env)))
	}
	layers := " (none)"
	if len(ctx.Sources) != 0 {
		layers = " " + strings.Join(ctx.Sources, " ")
	}
	lines = append(lines, "\nlayers read:"+layers)
	var userSel UserSelection
	if ctx != nil {
		userSel = ctx.User
	}
	userFile := UserConfigFiles(platform.Current(), env, userSel)
	lines = append(lines, "user file:    "+userFile)
	projectFile := ProjectConfigFileUsed(env, cwd)
	if projectFile == "" {
		projectFile = filepath.Join(platform.ProjectRoot(env, cwd), ".agents", "soho.conf")
	}
	lines = append(lines, "project file: "+projectFile)
	sessionFile := SessionConfPath(ctx, env, cwd)
	if sessionFile == "" {
		sessionFile = "(no workspace here)"
	}
	lines = append(lines, "session file: "+sessionFile)
	fmt.Fprintln(platform.Stdout, strings.Join(lines, "\n"))
}

var ruleNameRE = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func isDottedNormalizedKey(key string) bool {
	return strings.HasPrefix(key, "args_") || strings.HasPrefix(key, "role_") || strings.HasPrefix(key, "model_") || strings.HasPrefix(key, "effort_") || strings.HasPrefix(key, "lane_") || strings.HasPrefix(key, "context_window_") || strings.HasPrefix(key, "worker_messages_rules_")
}

func padUTF16(value string, width int) string {
	length := 0
	for _, r := range value {
		if r > 0xffff {
			length += 2
		} else {
			length++
		}
	}
	if length < width {
		return value + strings.Repeat(" ", width-length)
	}
	return value
}

func SplitPairArg(key, value string, sawValue bool, cmd string) (string, string, bool) {
	if sawValue || key == "" {
		return key, value, sawValue
	}
	if eq := strings.IndexByte(key, '='); eq > 0 {
		return key[:eq], key[eq+1:], true
	}
	if strings.IndexFunc(key, isJSWhitespace) >= 0 {
		platform.Die(fmt.Sprintf(`%s: '%s' arrived as one argument; pass the key and the value as two arguments or as key=value (zsh does not split "$var": use ${=var})`, cmd, key), 2)
	}
	return key, value, false
}

func CmdConfigSet(argv []string, ctx *Config, env platform.Env, cwd string) {
	key, value, where, sawValue := "", "", "project", false
	// A value may begin with hyphens (lane.review.args --add-dir /x): only an
	// argument before the key, or after a value that does not begin with a
	// hyphen, is an unknown option, and a "--" ends option parsing (--project
	// and --user included) for what follows.
	noOptions := false
	for _, arg := range argv {
		if !noOptions && arg == "--" {
			noOptions = true
			continue
		}
		if !noOptions && (arg == "--project" || arg == "--user") {
			where = strings.TrimPrefix(arg, "--")
			continue
		}
		if !noOptions && strings.HasPrefix(arg, "--") && (key == "" || (sawValue && !strings.HasPrefix(value, "-"))) {
			platform.Die("config set: unknown option '"+arg+"'", 2)
		}
		if key == "" {
			if eq := strings.IndexByte(arg, '='); eq > 0 {
				key, value = arg[:eq], arg[eq+1:]
				sawValue = value != ""
			} else {
				key = arg
			}
			continue
		}
		if !sawValue {
			value, sawValue = arg, true
		} else if strings.HasPrefix(value, "-") {
			value += " " + arg
		} else {
			platform.Die("config set: unexpected argument '"+arg+"'", 2)
		}
	}
	key, value, sawValue = SplitPairArg(key, value, sawValue, "config set")
	if key == "" || !sawValue {
		platform.Die("usage: config set <key> <value> | <key>=<value> [--project|--user]", 2)
	}
	if value == "" {
		platform.Die("config set: empty value", 2)
	}
	if !ConfigKeyOk(key) {
		platform.Die("config set: unknown key '"+key+"'", 2)
	}
	if !ConfigValueOk(key, value, env, cwd) {
		platform.Die("config set: invalid value '"+value+"' for "+key, 2)
	}
	dest := ConfigFileFor(where, env, cwd)
	// The project file lands under the project root; refuse it inside the
	// skill before MkdirAll/rewrite. The user file stays valid from inside.
	if where == "project" {
		root := platform.ProjectRoot(env, cwd)
		if skill := StatePathInSkill(env, root); skill != "" {
			platform.Die(StateInSkillMessage("config set", root, skill), 2)
		}
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o777); err != nil {
		platform.Die(fmt.Sprintf("config set: could not rewrite %s (file left untouched)", dest), 4)
	}
	ConfigWritePair(dest, key, value, env, cwd)
	fmt.Fprintf(platform.Stdout, "set %s=%s in %s\n", key, value, dest)
}
