package core

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/launchprofile"
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
	"split_min_pane", "pane_columns", "pane_overflow", "pane_column_rows", "herd_label", "herd_label_max", "reuse_workers",
	"multi_role", "panes", "lanes", "pane_mode", "flex_extra", "flex_roles",
	"worker_context", "brief_lint", "brief_lint_aliases", "brief_lint_placeholders", "approvals",
	"auto_approve", "max_auto_approvals", "max_effort",
	"settled_grace", "report_working_max", "idle_alert_minutes", "e2e_port_base", "trust_cwd", "spawn_timeout", "dispatch_timeout", "compact_timeout", "provider_retries",
	"provider_retry_delay", "provider_capacity_texts", "provider_error_texts",
	"prompt_check_seconds", "prompt_settle_seconds", "stuck_warn_minutes", "context_warn_percent", "context_fresh_percent", "state_dir",
	"report_language", "notify", "feedback", "feedback_repo", "feedback_dir", "feedback_to",
	"setup_target", "inbound", "caller_check", "worker_messages", "metrics",
	"pressure_disk_free_percent", "pressure_disk_free_min_gb", "pressure_swap_percent",
	"pressure_refuse_disk_free_percent", "pressure_refuse_disk_free_min_gb", "pressure_refuse_swap_percent",
	"footprint_warn_gb", "login_shell", "copies_dir",
}

// jobMachineKeys are known to ConfigKeyOk. They stay out of ConfigScalarKeys
// so `soho config` keeps the row order pinned by the frozen parity oracle.
var jobMachineKeys = []string{
	"machine_label", "job_orgs", "job_repos_root", "job_timeout",
	"job_blocked_timeout", "job_finishing_timeout", "job_auto_cleanup",
	"job_human_input_minutes", "job_wake_cmd",
}

// startupDialogKeys are the keys of the startup dialogs soho answers
// (DJA-434: hooks_review). Like jobMachineKeys they stay out of
// ConfigScalarKeys, so `soho config` keeps the row order pinned by the
// frozen parity oracle; config set and config get accept them.
var startupDialogKeys = []string{"hooks_review"}

// transportKeys selects soho's worker transport (DJA-603 stage 2a): herdr,
// the default, or hc, the herdr-core CLI. Like startupDialogKeys it stays
// out of ConfigScalarKeys, so `soho config` keeps the row order pinned by
// the frozen parity oracle; config set and config get accept it.
var transportKeys = []string{"transport"}

// Inventory keys stay outside the frozen configuration listing order.
var inventoryKeys = []string{"inventory", "inventory_name"}

// Fallback assistant keys stay outside the frozen configuration listing order.
var fallbackKeys = []string{"fallback_edit", "fallback_review"}

var KnownKinds = []string{"claude", "codex", "grok", "agy", "gemini", "cursor", "pi", "opencode"}
var EffortLadder = []string{"low", "medium", "high", "xhigh", "max"}

var dottedKeyRE = regexp.MustCompile(`^(?:role\.[a-z][a-z0-9_-]*\.(?:kind|model|effort|approvals|mcp|args)|lane\.[a-z][a-z0-9_-]*\.(?:roles|kind|model|effort|approvals|panes|args|mcp)|model\.[a-z][a-z0-9_.-]+|effort\.[a-z][a-z0-9_-]+|context_window\.[a-z][a-z0-9_-]+|background_patterns\.[a-z][a-z0-9_-]+|args\.[a-z][a-z0-9_-]+|worker_messages\.rules\.[a-z][a-z0-9_]*\.(?:from|to|types|scope|enabled))$`)
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

// LoadConfigThrough loads only the layers at or below the named destination.
func LoadConfigThrough(env platform.Env, cwd, through string) Config {
	if through == "session" {
		return LoadConfig(env, cwd)
	}
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	ctx := Config{Entries: make(map[string]ConfigEntry)}
	for _, layer := range baseConfigLayerPaths(env, cwd, UserSelection{}) {
		if through == "user" && layer.Source == "project" {
			break
		}
		loadConfigFile(layer.File, layer.Source, &ctx)
	}
	return ctx
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

func configEnvName(key string) string { return "SOHO_" + strings.ToUpper(key) }

// LayoutCapacityOverride reports whether an environment key overrides layout or capacity configuration.
func LayoutCapacityOverride(name string) bool {
	name = strings.ToUpper(name)
	if strings.HasPrefix(name, configEnvName("lane_")) {
		return true
	}
	for _, key := range []string{"panes", "pane_mode", "max_workers", "flex_extra", "flex_roles", "split_max_panes"} {
		if name == configEnvName(key) {
			return true
		}
	}
	return false
}

func Cfg(ctx *Config, key, fallback string, env platform.Env) string {
	if value := env.Get(configEnvName(key)); value != "" {
		return value
	}
	if entry, ok := ctx.Entries[key]; ok && entry.Value != "" {
		return entry.Value
	}
	return fallback
}

// OpenCodeTUIProfile reads the explicit selector with the existing config
// precedence, defaulting absent or empty values to legacy. It is nil-safe
// and performs no file, runtime or process environment access or mutation.
func OpenCodeTUIProfile(ctx *Config, env platform.Env) (string, error) {
	if ctx == nil {
		ctx = &Config{}
	}
	return launchprofile.ResolveTUIProfile(Cfg(ctx, "opencode_tui_profile", "", env))
}

// HooksReviewModeOk reports whether a hooks_review value is one of the two
// the key accepts (trust, block).
func HooksReviewModeOk(value string) bool {
	return value == "trust" || value == "block"
}

// TransportValueOk reports whether a transport value is one of the two the
// key accepts (herdr, hc).
func TransportValueOk(value string) bool {
	return value == "herdr" || value == "hc"
}

// TrustCwdMode fails closed on invalid config or environment values.
func TrustCwdMode(ctx *Config, env platform.Env) string {
	if Cfg(ctx, "trust_cwd", "on", env) == "on" {
		return "on"
	}
	return "off"
}

// HooksReviewMode resolves the hooks_review policy (DJA-434): trust by
// default — spawn, dispatch and send approve codex's "Hooks need review"
// screen — or block, which leaves it to the operator. The
// SOHO_HOOKS_REVIEW environment layer wins over the file layers; an invalid
// value in either layer counts as block, the safer mode.
func HooksReviewMode(ctx *Config, env platform.Env) string {
	if value := env.Get("SOHO_HOOKS_REVIEW"); value != "" {
		if HooksReviewModeOk(value) {
			return value
		}
		return "block"
	}
	if ctx != nil {
		if entry, ok := ctx.Entries["hooks_review"]; ok && entry.Value != "" {
			if HooksReviewModeOk(entry.Value) {
				return entry.Value
			}
			return "block"
		}
	}
	return "trust"
}

// TransportMode resolves the worker transport (DJA-603 stage 2a): herdr by
// default — an absent or invalid value counts as herdr — or hc, the
// herdr-core CLI. The SOHO_TRANSPORT environment layer wins over the file
// layers, the way Cfg reads every key. Only doctor reads it so far: spawn,
// dispatch and wait still use herdr.
func TransportMode(ctx *Config, env platform.Env) string {
	value := "herdr"
	if ctx != nil {
		value = Cfg(ctx, "transport", "herdr", env)
	} else if raw := env.Get("SOHO_TRANSPORT"); raw != "" {
		value = raw
	}
	if value == "hc" {
		return "hc"
	}
	return "herdr"
}

// TransportModeAt is TransportMode for cwd without the defaults layer, whose
// transport is the default anyway: finding that layer needs the skill
// directory, which dies when there is none and materializes the bundled
// skill otherwise, and capabilities --json must neither fail nor write. The
// user, project and session layers load as LoadConfig loads them.
func TransportModeAt(env platform.Env, cwd string) string {
	ctx := LoadMetadataConfigFor(env, cwd, UserSelection{})
	return TransportMode(&ctx, env)
}

// LoadMetadataConfigFor reads the existing transport metadata layers without
// materializing defaults or state. The same snapshot can resolve both transport
// and the explicit TUI selector, preserving session discovery and layer order.
func LoadMetadataConfigFor(env platform.Env, cwd string, sel UserSelection) Config {
	ctx := Config{Entries: make(map[string]ConfigEntry), Order: make([]string, 0), User: sel}
	loadConfigFile(EffectiveUserConfigFile(platform.Current(), env, sel), "user", &ctx)
	if file := ProjectConfigFileUsed(env, cwd); file != "" {
		loadConfigFile(file, "project", &ctx)
	}
	if file := SessionConfPath(&ctx, env, cwd); file != "" {
		loadConfigFile(file, "session", &ctx)
	}
	return ctx
}

// CompactTimeoutDefaultMS is the compact phase's timeout when the
// compact_timeout key is absent or holds a value the reader cannot use.
const CompactTimeoutDefaultMS int64 = 300000

// CompactTimeoutMS is the effective compact phase timeout in milliseconds,
// shared by the dispatch's --compact phase and soho compact without
// --timeout: the compact_timeout key read the usual way (the SOHO_
// environment over the file layers) in the --timeout grammar (milliseconds,
// or a number with an ms, s, m or h unit via ParseTimeoutMS). An invalid or
// <= 0 value from any layer falls back to the default.
func CompactTimeoutMS(ctx *Config, env platform.Env) int64 {
	if ms, ok := ParseTimeoutMS(Cfg(ctx, "compact_timeout", "", env)); ok && ms > 0 {
		return ms
	}
	return CompactTimeoutDefaultMS
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
	// Keep the frozen config listing order while accepting the copy policy.
	if key == "mutation_copy_exclude" || key == "opencode_tui_profile" {
		return true
	}
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
	for _, scalar := range startupDialogKeys {
		if scalar == key {
			return true
		}
	}
	for _, scalar := range transportKeys {
		if scalar == key {
			return true
		}
	}
	for _, scalar := range inventoryKeys {
		if scalar == key {
			return true
		}
	}
	for _, scalar := range fallbackKeys {
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
	if key == "opencode_tui_profile" {
		_, err := launchprofile.ResolveTUIProfile(value)
		return err == nil
	}
	if strings.ContainsAny(value, "\n\t#") {
		return false
	}
	switch {
	case key == "copies_dir":
		return value == "" || filepath.IsAbs(value) || strings.HasPrefix(value, "~/")
	case key == "context_fresh_percent":
		_, err := strconv.ParseInt(value, 10, 64)
		return decimalRE.MatchString(value) && err == nil
	case key == "e2e_port_base":
		if value == "" {
			return true
		}
		port, err := strconv.Atoi(value)
		return decimalRE.MatchString(value) && err == nil && port >= 1024 && port <= 65000
	case key == "pressure_disk_free_min_gb" || key == "pressure_refuse_disk_free_min_gb":
		_, ok := pressureDiskFloorValue(value)
		return ok
	case key == "report_working_max" || key == "idle_alert_minutes":
		// The wait readers parse int64 values: refuse values they cannot honor.
		_, err := strconv.ParseInt(value, 10, 64)
		return decimalRE.MatchString(value) && err == nil
	case key == "approvals" || (strings.HasPrefix(key, "lane.") || strings.HasPrefix(key, "role.")) && strings.HasSuffix(key, ".approvals"):
		return value == "ask" || value == "edits" || value == "auto" || value == "full" || value == "unsandboxed"
	case key == "max_workers" || key == "provider_retries" || key == "provider_retry_delay" || key == "prompt_check_seconds" || key == "prompt_settle_seconds" || key == "stuck_warn_minutes" || key == "pressure_disk_free_percent" || key == "pressure_swap_percent" || key == "pressure_refuse_disk_free_percent" || key == "pressure_refuse_swap_percent" || key == "footprint_warn_gb":
		return decimalRE.MatchString(value)
	case key == "multi_role" || key == "reuse_workers" || key == "lanes" || key == "trust_cwd" || key == "login_shell":
		return value == "on" || value == "off"
	case key == "setup_target":
		return value == "canonical" || value == "local"
	case key == "inbound":
		return value == "auto" || value == "off"
	case key == "caller_check":
		return callerCheckModeOk(value)
	case key == "hooks_review":
		return HooksReviewModeOk(value)
	case key == "transport":
		return TransportValueOk(value)
	case key == "fallback_edit" || key == "fallback_review":
		return fallbackValueOk(value)
	case key == "inventory":
		return value == "off" || value == "hidden" || value == "show"
	case key == "inventory_name":
		return utf8.ValidString(value) && !strings.ContainsFunc(value, unicode.IsControl)
	case key == "worker_messages":
		return value == "off" || value == "policy"
	case key == "panes":
		return value == "2" || value == "3" || value == "4"
	case key == "pane_columns":
		return paneColumnsValueOk(value)
	case key == "pane_column_rows":
		return len(value) == 1 && value[0] >= '1' && value[0] <= '9'
	case key == "pane_overflow":
		return value == "balance" || value == "last-column" || value == "new-column" || value == "tab"
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
	case strings.HasPrefix(key, "lane.") && strings.HasSuffix(key, ".effort"), strings.HasPrefix(key, "role.") && strings.HasSuffix(key, ".effort"):
		return contains(EffortLadder, value)
	case strings.HasPrefix(key, "role.") && strings.HasSuffix(key, ".mcp"), strings.HasPrefix(key, "lane.") && strings.HasSuffix(key, ".mcp"):
		return McpValueError(value) == ""
	case key == "job_timeout":
		return jobTimeoutValueOk(value)
	case key == "compact_timeout":
		// The --timeout grammar (ParseTimeoutMS) with the key's own bound:
		// a timeout of zero or less is a refusal, not a value.
		ms, ok := ParseTimeoutMS(value)
		return ok && ms > 0
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

// fallbackValueOk accepts the empty default or exactly three fields:
// a known kind, a model id and an effort from the ladder.
func fallbackValueOk(value string) bool {
	if value == "" {
		return true
	}
	fields := strings.Fields(value)
	if len(fields) != 3 || !contains(KnownKinds, fields[0]) || fields[1] == "" || !contains(EffortLadder, fields[2]) {
		return false
	}
	return true
}

// jobTimeoutValueOk is the contract range for the default job budget: 1..1440 minutes.
func jobTimeoutValueOk(value string) bool {
	if !decimalRE.MatchString(value) {
		return false
	}
	n, err := strconv.Atoi(value)
	return err == nil && n >= 1 && n <= 1440
}

// paneColumnsValueOk accepts auto or a declared caller-tab shape: 1 to 6 single-digit
// row counts (1 to 9) separated by commas, no spaces. The empty value is
// valid too: it is the default grid (an empty pane_columns in a config
// file resolves to the empty default through the normal empty-value
// fallback; `config set` still refuses empty values for every key).
var paneColumnsRE = regexp.MustCompile(`^[1-9](,[1-9]){0,5}$`)

func paneColumnsValueOk(value string) bool {
	return value == "" || value == "auto" || paneColumnsRE.MatchString(value)
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
	return kindEntries(ctx, env, "context_window_")
}

// BackgroundPatternKey is the normalized config key background_patterns.<kind>.
func BackgroundPatternKey(kind string) string {
	return "background_patterns_" + kind
}

// BackgroundPatternEntries reports the effective background_patterns.<kind>
// values, the same layers as Cfg: for every kind present in a config layer or
// in the environment, kind → (value, source), empty values dropped.
func BackgroundPatternEntries(ctx *Config, env platform.Env) map[string]ConfigEntry {
	return kindEntries(ctx, env, "background_patterns_")
}

// kindEntries is the effective value of every <prefix><kind> key (prefix in
// its normalized form, like "context_window_"), by kind.
func kindEntries(ctx *Config, env platform.Env, prefix string) map[string]ConfigEntry {
	envPrefix := "SOHO_" + strings.ToUpper(prefix)
	kinds := make(map[string]bool)
	for key := range ctx.Entries {
		if strings.HasPrefix(key, prefix) {
			kinds[strings.TrimPrefix(key, prefix)] = true
		}
	}
	for name := range env {
		if strings.HasPrefix(name, envPrefix) {
			kinds[strings.ToLower(NormalizeKey(strings.TrimPrefix(name, envPrefix)))] = true
		}
	}
	out := make(map[string]ConfigEntry, len(kinds))
	for kind := range kinds {
		key := prefix + kind
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
	return StateRootPathForProject(ctx, env, platform.StateProjectRoot(env, cwd))
}

// StateRootPathForProject is StateRootPath over an already known project
// root: SOHO_DIR first, otherwise the current state_dir setting, relative
// to that root. It is pure path arithmetic: no Git lookup, no cwd and no
// state access, so the explicit read-only loader can use its supplied
// canonical root. StateRootPath delegates to it after resolving the
// existing StateProjectRoot, with no behavior change.
func StateRootPathForProject(ctx *Config, env platform.Env, projectRoot string) string {
	dir := env.Get("SOHO_DIR")
	if dir == "" {
		dir = StateDirSetting(ctx, env, "")
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(projectRoot, dir)
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

// configWritePair rewrites dest with key=value under the caller's lock. The
// optional display is the path its error messages print, when the caller acts on
// a resolved destination that differs from the path the user named.
func configWritePair(dest, key, value string, env platform.Env, cwd, command string, display ...string) {
	shown := dest
	if len(display) > 0 && display[0] != "" {
		shown = display[0]
	}
	raw := ""
	if _, err := os.Stat(dest); err == nil {
		var readErr error
		raw, readErr = platform.ReadTextFile(dest)
		if readErr != nil {
			platform.Die(fmt.Sprintf("%s: could not rewrite %s (file left untouched)", command, shown), 4)
		}
	}
	out := make([]string, 0)
	found := false
	for _, rawLine := range splitLines(raw) {
		_, comment := splitTrailingComment(rawLine)
		body, _, _ := strings.Cut(rawLine, "#")
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
		if NormalizeKey(k) == NormalizeKey(key) {
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
		platform.Die(fmt.Sprintf("%s: rewrite of %s dropped %s (file left untouched)", command, shown, key), 4)
	}
	if err := writeConfigAtomic(dest, content); err != nil {
		platform.Die(fmt.Sprintf("%s: could not rewrite %s (file left untouched)", command, shown), 4)
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
	pairs, err := ReadConfigFilePairs(file)
	if err != nil {
		return ""
	}
	for _, pair := range pairs {
		if pair.Key == NormalizeKey(key) {
			return pair.Value
		}
	}
	return ""
}

func ConfigClearKey(file, key string) {
	raw, err := platform.ReadTextFile(file)
	if err != nil {
		platform.Die(fmt.Sprintf("session clear: could not rewrite %s (file left untouched)", file), 4)
	}
	out := make([]string, 0)
	for _, rawLine := range splitLines(raw) {
		body, _, _ := strings.Cut(rawLine, "#")
		stripped := trimJSWhitespace(body)
		if stripped != "" && !strings.HasPrefix(stripped, "#") {
			if eq := strings.IndexByte(stripped, '='); eq >= 0 && NormalizeKey(trimJSWhitespace(stripped[:eq])) == NormalizeKey(key) {
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
	if head == "background" && len(parts) >= 3 && parts[1] == "patterns" {
		return "background_patterns." + strings.Join(parts[2:], ".")
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
					if strings.HasSuffix(name, ".md") && !strings.HasSuffix(name, ".append.md") {
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
		value, source := configListingValueSource(ctx, key, env, cwd)
		lines = append(lines, row(key, value, source))
	}
	dotted := configListingKeys(ctx, env, cwd)
	listedLanes := listedLaneKeys(ctx, dotted, env, cwd)
	for _, item := range dotted {
		lines = append(lines, row(item.name, Cfg(ctx, item.key, "", env), CfgSource(ctx, item.key, env)))
	}
	if LanesEnabled(ctx, env) {
		for _, lane := range LaneNames(ctx, env) {
			if !listedLanes[NormalizeKey(lane)] {
				value, source := presetLaneRolesRow(ctx, lane, env)
				lines = append(lines, row("lane."+lane+".roles", value, source))
			}
		}
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

// configListingValueSource is one listing row's VALUE and SOURCE, computed in
// one place: the lane-derived max_workers case names "lanes" exactly as the
// listing does, and a lane with no configured key of its own shows the skill
// default's roles row, the same synthesized row the listing prints. The
// dry-run preview reads it too, so its previous.effective and
// previous.effective_source are the listing's projection, never a second
// one (F3, F6).
func configListingValueSource(ctx *Config, key string, env platform.Env, cwd string) (string, string) {
	if value, source, ok := presetLaneRolesValue(ctx, key, env, cwd); ok {
		return value, source
	}
	source := CfgSource(ctx, key, env)
	if key == "max_workers" && LanesEnabled(ctx, env) && !ConfigExplicit(ctx, key, env) {
		source = "lanes"
	}
	return ConfigScalarValue(ctx, key, env, cwd), source
}

// configListingKey is one dotted listing key with the name its row prints: the
// key's own spelling from the file, else its dotted name.
type configListingKey struct{ key, name string }

// configListingKeys is the listing's dotted keys in the frozen order: every
// dotted file key, plus the environment's dotted keys the files do not set.
func configListingKeys(ctx *Config, env platform.Env, cwd string) []configListingKey {
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
	out := make([]configListingKey, 0, len(keys))
	for _, key := range keys {
		name := ctx.Entries[key].Original
		if name == "" {
			name = DottedKeyName(key, ctx, env, cwd)
		}
		out = append(out, configListingKey{key: key, name: name})
	}
	return out
}

// presetLaneRolesRow is the listing's synthesized row for a lane that has no
// configured key: its roles come from the skill default.
func presetLaneRolesRow(ctx *Config, lane string, env platform.Env) (string, string) {
	return PresetLaneRoles(lane, PanesValue(ctx, env), PaneMode(ctx, env)), "skill default"
}

// listedLaneKeys is the set of lanes (normalized) that have a dotted key of
// their own among the listing keys, matched by the normalized key's dotted
// name, so a file that spells it lane_build_roles counts like
// lane.build.roles. The listing and the dry-run preview share it (review
// round 3, F2).
func listedLaneKeys(ctx *Config, keys []configListingKey, env platform.Env, cwd string) map[string]bool {
	listed := map[string]bool{}
	for _, item := range keys {
		parts := strings.Split(DottedKeyName(item.key, ctx, env, cwd), ".")
		if len(parts) == 3 && parts[0] == "lane" {
			listed[NormalizeKey(parts[1])] = true
		}
	}
	return listed
}

// presetLaneRolesValue answers the listing's synthesized row for a
// lane.<lane>.roles key: the lane must exist, lanes must be on and the lane
// must have no dotted key of its own (that is the listing's own rule for
// adding the row).
func presetLaneRolesValue(ctx *Config, key string, env platform.Env, cwd string) (string, string, bool) {
	if ctx == nil || !LanesEnabled(ctx, env) {
		return "", "", false
	}
	parts := strings.Split(DottedKeyName(key, ctx, env, cwd), ".")
	if len(parts) != 3 || parts[0] != "lane" || parts[2] != "roles" {
		return "", "", false
	}
	listed := listedLaneKeys(ctx, configListingKeys(ctx, env, cwd), env, cwd)
	for _, lane := range LaneNames(ctx, env) {
		if NormalizeKey(lane) != NormalizeKey(parts[1]) || listed[NormalizeKey(lane)] {
			continue
		}
		value, source := presetLaneRolesRow(ctx, lane, env)
		return value, source, true
	}
	return "", "", false
}

// ConfigScalarValue is the listing's VALUE column for one scalar key: the
// Cfg value over ctx and env, with effective state_dir and lane-derived
// max_workers special cases.
// CmdConfig and `soho config get` both read it, so the listing and the get
// command never diverge.
func ConfigScalarValue(ctx *Config, key string, env platform.Env, cwd string) string {
	if key == "max_workers" && LanesEnabled(ctx, env) && !ConfigExplicit(ctx, key, env) {
		return MaxWorkers(ctx, env)
	}
	value := Cfg(ctx, key, "", env)
	if key == "state_dir" && CfgSource(ctx, key, env) == "defaults" {
		value = StateDirSetting(ctx, env, cwd)
	}
	return value
}

var ruleNameRE = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func isDottedNormalizedKey(key string) bool {
	return strings.HasPrefix(key, "args_") || strings.HasPrefix(key, "role_") || strings.HasPrefix(key, "model_") || strings.HasPrefix(key, "effort_") || strings.HasPrefix(key, "lane_") || strings.HasPrefix(key, "context_window_") || strings.HasPrefix(key, "background_patterns_") || strings.HasPrefix(key, "worker_messages_rules_")
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

// configSetPlan is the parsed, validated shape shared by config set and
// its dry-run preview. Parsing resolves the destination but never writes it.
type configSetPlan struct {
	Key, Value, Where, Dest string
	DryRun, JSON            bool
}

// splitPairArgPlan is SplitPairArg's grammar without its Die: the preview
// needs the same message on stdout as JSON before the caller turns it into
// the same exit-2 diagnostic set has always printed.
func splitPairArgPlan(key, value string, sawValue bool) (string, string, bool, string) {
	if sawValue || key == "" {
		return key, value, sawValue, ""
	}
	if eq := strings.IndexByte(key, '='); eq > 0 {
		return key[:eq], key[eq+1:], true, ""
	}
	if strings.IndexFunc(key, isJSWhitespace) >= 0 {
		return key, value, false, fmt.Sprintf(`config set: '%s' arrived as one argument; pass the key and the value as two arguments or as key=value (zsh does not split "$var": use ${=var})`, key)
	}
	return key, value, false, ""
}

// configSetArgs is the argument-only shape of config set: the grammar shared
// by the command's parser and the read-only admission gate, before any key,
// value or destination validation.
type configSetArgs struct {
	Key, Value, Where string
	DryRun, JSON      bool
	SawValue          bool
	// Inline records the key=<value> spelling: it supplies the value even when
	// empty, so an empty inline assignment is refused with F1's message while
	// an explicit empty second argument keeps the base refusal.
	Inline        bool
	ScopeSeen     bool
	ScopeConflict bool
}

// scanConfigSetArgs walks argv with the one config set grammar. It returns a
// grammar error message (never a validation one): CmdConfigSet's parser and
// the read-only gate both classify the same shape through this function.
func scanConfigSetArgs(argv []string) (configSetArgs, string) {
	args := configSetArgs{Where: "project"}
	// A value may begin with hyphens (lane.review.args --add-dir /x): only an
	// argument before the key, or after a value that does not begin with a
	// hyphen, is an unknown option, and a "--" ends option parsing (--project
	// and --user included) for what follows. A preview flag counts only where
	// a preview flag can be: before the key, or once the value is known, so a
	// lone token after the key is the value even when it is "--dry-run" or
	// "--json" (F12).
	noOptions := false
	message := ""
	// A grammar error does not stop the walk: --dry-run and --json decide the
	// response mode wherever they appear, so an early return would render an
	// error for an invocation whose real mode is JSON (F7). The first error
	// wins; later tokens only fill the mode flags.
	for _, arg := range argv {
		if !noOptions && arg == "--" {
			noOptions = true
			continue
		}
		if !noOptions && (arg == "--project" || arg == "--user") {
			if args.ScopeSeen {
				args.ScopeConflict = true
			}
			args.ScopeSeen = true
			args.Where = strings.TrimPrefix(arg, "--")
			continue
		}
		if !noOptions && (arg == "--dry-run" || arg == "--json") && (args.Key == "" || args.SawValue) {
			if arg == "--dry-run" {
				args.DryRun = true
			} else {
				args.JSON = true
			}
			continue
		}
		if !noOptions && strings.HasPrefix(arg, "--") && (args.Key == "" || (args.SawValue && !strings.HasPrefix(args.Value, "-"))) {
			if message == "" {
				message = "config set: unknown option '" + arg + "'"
			}
			continue
		}
		if args.Key == "" {
			if eq := strings.IndexByte(arg, '='); eq > 0 {
				args.Key, args.Value = arg[:eq], arg[eq+1:]
				// An inline assignment supplies the value even when it is empty:
				// the tokens after it are options, never a replacement value (F1).
				args.SawValue = true
				args.Inline = true
			} else {
				args.Key = arg
			}
			continue
		}
		if !args.SawValue {
			args.Value, args.SawValue = arg, true
		} else if strings.HasPrefix(args.Value, "-") {
			args.Value += " " + arg
		} else if message == "" {
			message = "config set: unexpected argument '" + arg + "'"
		}
	}
	return args, message
}

// ConfigSetPreviewShape reports whether argv (the words after "config set")
// is the write-free preview form: the command's own grammar with --dry-run
// and no grammar error. The SOHO_NOWRITE gate admits exactly this shape, so
// the gate and the command can never disagree about what a preview is.
func ConfigSetPreviewShape(argv []string) bool {
	args, message := scanConfigSetArgs(argv)
	return message == "" && args.DryRun
}

// parseConfigSet is CmdConfigSet's argument grammar and validation, with no
// side effects. On failure it returns the partial key and value alongside
// the exact message the command prints, so text and --json errors agree.
func parseConfigSet(argv []string, env platform.Env, cwd string) (configSetPlan, string) {
	args, message := scanConfigSetArgs(argv)
	// The response mode is decided before any parse error: JSON on stdout only
	// for a real preview, --dry-run and --json both present wherever they
	// appear (F7). Every other invocation keeps stderr only.
	plan := configSetPlan{Key: args.Key, Value: args.Value, Where: args.Where, DryRun: args.DryRun, JSON: args.JSON && args.DryRun}
	fail := func(message string) (configSetPlan, string) {
		plan.Key, plan.Value = args.Key, args.Value
		return plan, message
	}
	if message != "" {
		return fail(message)
	}
	if args.ScopeConflict {
		// A second scope flag has no meaning and used to win silently; the
		// preview's JSON error is rendered like every other invalid input.
		return fail("config set: only one scope option (--project or --user) may be given")
	}
	if args.JSON && !args.DryRun {
		// --json belongs to the preview: without --dry-run this invocation
		// was and stays an unknown option on the writing command, reported on
		// stderr alone (F12: no JSON object on stdout).
		return fail("config set: unknown option '--json'")
	}
	key, value, sawValue, message := splitPairArgPlan(args.Key, args.Value, args.SawValue)
	if message != "" {
		return fail(message)
	}
	// inline is the key=<value> spelling of the scan, so an empty inline
	// assignment is told apart from an explicit empty second argument.
	inline := args.Inline
	if key == "" || !sawValue {
		return fail("usage: config set <key> <value> | <key>=<value> [--project|--user] (default --project; for this session only: soho session set)")
	}
	if value == "" {
		if inline {
			// An inline assignment that carries nothing is the usage error it
			// always was; the tokens after it stay options, so a following
			// flag never becomes the value (F1).
			return fail("usage: config set <key> <value> | <key>=<value> [--project|--user] (default --project; for this session only: soho session set)")
		}
		return fail("config set: empty value")
	}
	if !ConfigKeyOk(key) {
		return fail("config set: unknown key '" + key + "'")
	}
	if !ConfigValueOk(key, value, env, cwd) {
		if key == "opencode_tui_profile" {
			return fail("config set: " + launchprofile.ErrInvalidTUIProfile.Error())
		}
		return fail("config set: invalid value '" + value + "' for " + key)
	}
	plan.Key, plan.Value = key, value
	plan.Dest = ConfigFileFor(plan.Where, env, cwd)
	// The project file lands under the project root; refuse it inside the
	// skill before MkdirAll/rewrite. The user file stays valid from inside.
	if plan.Where == "project" {
		root := platform.ProjectRoot(env, cwd)
		if skill := StatePathInSkill(env, root); skill != "" {
			return fail(StateInSkillMessage("config set", root, skill))
		}
	}
	return plan, ""
}

// configSetAppliesToRoles is the panel's "who is affected" projection: the
// lane's configured roles for a lane key, the named role for a role key.
func configSetAppliesToRoles(key string, ctx *Config, env platform.Env) []any {
	parts := strings.Split(key, ".")
	out := []any{}
	if len(parts) < 2 {
		return out
	}
	switch parts[0] {
	case "lane":
		for _, role := range SplitRoles(LaneRolesCSV(ctx, parts[1], env)) {
			out = append(out, role)
		}
	case "role":
		out = append(out, parts[1])
	}
	return out
}

// configValueShellToken renders key=value as the one argv token the preview's
// command is replayed with through a POSIX shell. A value holding only
// [A-Za-z0-9._/:@%+=,-] and not starting with "-" stays bare; everything
// else (spaces, quotes, $, a leading dash) is single-quoted, with each
// embedded single quote escaped for the shell.
func configValueShellToken(key, value string) string {
	token := key + "=" + value
	if value != "" && !strings.HasPrefix(value, "-") && strings.IndexFunc(token, func(r rune) bool {
		return !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._/:@%+=,-", r)
	}) < 0 {
		return token
	}
	return "'" + strings.ReplaceAll(token, "'", `'\''`) + "'"
}

// cmdConfigSetDryRun prints the write plan and nothing else: no MkdirAll, no
// rewrite and no file creation, including when the destination is absent.
func cmdConfigSetDryRun(plan configSetPlan, ctx *Config, env platform.Env, cwd string) {
	if !plan.JSON {
		fmt.Fprintf(platform.Stdout, "would set %s=%s in %s\n", plan.Key, plan.Value, plan.Dest)
		return
	}
	// previous.in_file keeps the parser's distinction: a present assignment
	// with an empty value is "", an absent one is null (F3).
	var inFile any
	if pairs, err := ReadConfigFilePairs(plan.Dest); err == nil {
		normalized := NormalizeKey(plan.Key)
		for _, pair := range pairs {
			if pair.Key == normalized {
				inFile = pair.Value
				break
			}
		}
	}
	normalized := NormalizeKey(plan.Key)
	effective, source := "", "builtin"
	if ctx != nil {
		effective, source = configListingValueSource(ctx, normalized, env, cwd)
	}
	fmt.Fprintln(platform.Stdout, jsonjs.Stringify(jsonjs.O(
		"schema", 1,
		"key", plan.Key,
		"value", plan.Value,
		"scope", plan.Where,
		"file", plan.Dest,
		"command", "soho config set "+configValueShellToken(plan.Key, plan.Value)+" --"+plan.Where,
		"valid", true,
		"previous", jsonjs.O("in_file", inFile, "effective", effective, "effective_source", source),
		"applies_to_roles", configSetAppliesToRoles(plan.Key, ctx, env),
	)))
}

func CmdConfigSet(argv []string, ctx *Config, env platform.Env, cwd string) {
	plan, message := parseConfigSet(argv, env, cwd)
	if message != "" {
		if plan.JSON {
			fmt.Fprintln(platform.Stdout, jsonjs.Stringify(jsonjs.O("schema", 1, "key", plan.Key, "value", plan.Value, "valid", false, "error", message)))
		}
		platform.Die(message, 2)
	}
	if plan.DryRun {
		cmdConfigSetDryRun(plan, ctx, env, cwd)
		return
	}
	// The destination is resolved once (symlinks followed) and reused for the
	// directory, the lock, the read, the change and the rename, so two names
	// for one file serialize on the file itself (F2). The path the user's
	// config names stays what the messages print.
	target := resolveConfigDestination(plan.Dest)
	if err := os.MkdirAll(filepath.Dir(target), 0o777); err != nil {
		platform.Die(fmt.Sprintf("config set: could not rewrite %s (file left untouched)", plan.Dest), 4)
	}
	// The set holds the destination lock across its own read, change and
	// rename, the same lock config unset takes (F6).
	unlock := lockConfigDestination(target, "config set", plan.Dest)
	defer unlock()
	configWritePair(target, plan.Key, plan.Value, env, cwd, "config set", plan.Dest)
	fmt.Fprintf(platform.Stdout, "set %s=%s in %s\n", plan.Key, plan.Value, plan.Dest)
}

// resolveConfigDestination is the one destination config set and config unset
// act on: the file the atomic writer renames to, with every symlink on the way
// followed. A destination the platform cannot resolve is left as it is, so the
// ordinary read or write error names the path the user's config set.
func resolveConfigDestination(dest string) string {
	target, err := platform.WriteTarget(dest)
	if err != nil {
		return dest
	}
	return target
}

// lockConfigDestination takes the shared destination lock next to a config
// file. The exclusive-create file lock is the registries' lock helper, with
// its retry, stale and timeout rules, so config set and config unset
// serialize across processes as well as goroutines. lockPath is the resolved
// destination (the lock's identity); display is the path errors print, which
// defaults to lockPath.
func lockConfigDestination(lockPath, command string, display ...string) func() {
	shown := lockPath
	if len(display) > 0 && display[0] != "" {
		shown = display[0]
	}
	dir, name := filepath.Dir(lockPath), "."+filepath.Base(lockPath)+".lock"
	unlock, err := lockRegistry(dir, name, "config")
	if err != nil {
		platform.Die(fmt.Sprintf("%s: could not lock %s (file left untouched)", command, shown), 4)
	}
	return unlock
}

// splitRawLines splits raw file bytes into lines that each keep their own
// terminator: "\n" or "\r\n". A last line without a terminator stays
// without one, so concatenating the returned slices reproduces the input.
func splitRawLines(raw []byte) [][]byte {
	lines := make([][]byte, 0)
	for len(raw) > 0 {
		if i := bytes.IndexByte(raw, '\n'); i >= 0 {
			lines = append(lines, raw[:i+1])
			raw = raw[i+1:]
			continue
		}
		lines = append(lines, raw)
		break
	}
	return lines
}

// CmdConfigUnset removes exactly the key's assignment lines from the selected
// file. Every other byte — CRLF endings, comments, blank lines and a last
// line without a newline — is copied unchanged: the removed line takes only
// its own terminator with it (F5). The rewrite goes through the same atomic
// writer config set uses, under the same destination lock.
func CmdConfigUnset(argv []string, env platform.Env, cwd string) {
	key, where := "", "project"
	scopeSeen, conflict := false, false
	noOptions := false
	for _, arg := range argv {
		if !noOptions && arg == "--" {
			noOptions = true
			continue
		}
		if !noOptions && (arg == "--project" || arg == "--user") {
			if scopeSeen {
				conflict = true
			}
			scopeSeen = true
			where = strings.TrimPrefix(arg, "--")
			continue
		}
		if !noOptions && strings.HasPrefix(arg, "--") {
			platform.Die("config unset: unknown option '"+arg+"'", 2)
		}
		if key == "" {
			key = arg
			continue
		}
		platform.Die("config unset: unexpected argument '"+arg+"'", 2)
	}
	if conflict {
		platform.Die("config unset: only one scope option (--project or --user) may be given", 2)
	}
	if key == "" {
		platform.Die("usage: config unset <key> [--project|--user] (default --project)", 2)
	}
	if !ConfigKeyOk(key) {
		platform.Die("config unset: unknown key '"+key+"'", 2)
	}
	// The project destination carries the same guard as config set, before
	// any project file is touched (F7); the user file stays valid from inside
	// the skill.
	if where == "project" {
		root := platform.ProjectRoot(env, cwd)
		if skill := StatePathInSkill(env, root); skill != "" {
			platform.Die(StateInSkillMessage("config unset", root, skill), 2)
		}
	}
	file := ConfigFileFor(where, env, cwd)
	// A genuinely missing file is a no-op; any other read failure is an I/O
	// error, never a false "not set" (F8).
	if _, err := os.Stat(file); err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(platform.Stdout, "not set in %s\n", file)
			return
		}
		platform.Die(fmt.Sprintf("config unset: could not rewrite %s (file left untouched)", file), 4)
	}
	// The resolved destination is the lock's identity, the file read, changed
	// and renamed, so a project and a user config that are two links to one
	// file serialize on that file (F2). Messages keep the user's path.
	target := resolveConfigDestination(file)
	unlock := lockConfigDestination(target, "config unset", file)
	defer unlock()
	raw, err := os.ReadFile(target)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(platform.Stdout, "not set in %s\n", file)
			return
		}
		platform.Die(fmt.Sprintf("config unset: could not rewrite %s (file left untouched)", file), 4)
	}
	normalized := NormalizeKey(key)
	kept := make([]byte, 0, len(raw))
	removed := false
	for _, rawLine := range splitRawLines(raw) {
		line := strings.TrimSuffix(platform.DecodeText(rawLine), "\n")
		body, _, _ := strings.Cut(line, "#")
		stripped := trimJSWhitespace(body)
		if stripped != "" && !strings.HasPrefix(stripped, "#") {
			if eq := strings.IndexByte(stripped, '='); eq >= 0 && NormalizeKey(trimJSWhitespace(stripped[:eq])) == normalized {
				removed = true
				continue
			}
		}
		kept = append(kept, rawLine...)
	}
	if !removed {
		fmt.Fprintf(platform.Stdout, "not set in %s\n", file)
		return
	}
	if err := writeConfigAtomic(target, string(kept)); err != nil {
		platform.Die(fmt.Sprintf("config unset: could not rewrite %s (file left untouched)", file), 4)
	}
	fmt.Fprintf(platform.Stdout, "unset %s in %s\n", key, file)
}

// LoadLocalConfigReadOnly loads the configuration a caller resolves its
// state directory with when the caller is the local checkout and not a
// foreign project: the same layer files, in the same order and with the same
// parser as LoadConfigFor (defaults, the environment's user file, the project
// file and the session file of the caller's workspace), with one difference —
// the defaults layer never materializes or repairs the skill, so a read from
// a checkout without an installed skill answers instead of dying at the skill
// lookup (the embedded config.defaults is used when no skill source exists).
//
// A config file that is a symlink to a regular file is read, as the ordinary
// loader reads it. A missing file is an ordinary empty layer. A layer path
// that exists and cannot be read — a symlink whose target is gone, a symlink
// to a directory, a FIFO or another non-regular file, an unreadable file — is
// returned as an error, so that a caller resolving a state directory fails
// instead of silently resolving it from the lower layers.
//
// The load writes nothing: no file, directory, lock, cache entry or
// environment value, and the defaults read is the read-only skill lookup.
func LoadLocalConfigReadOnly(env platform.Env, cwd string) (Config, error) {
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	ctx := Config{Entries: make(map[string]ConfigEntry), Order: make([]string, 0), Sources: make([]string, 0, 4)}
	loadReadOnlyDefaults(&ctx, env, nil)
	loadLocalConfigLayer(&ctx, EffectiveUserConfigFile(platform.Current(), env, UserSelection{}), "user")
	if file := localProjectConfigFile(env, cwd); file != "" {
		loadLocalConfigLayer(&ctx, file, "project")
	}
	// The session layer sits above the three base layers it reads its own
	// location from, exactly the layer LoadConfigFor reads through
	// SessionConfPath; its state_dir may redirect the state root afterwards.
	if file := SessionConfPath(&ctx, env, cwd); file != "" {
		loadLocalConfigLayer(&ctx, file, "session")
	}
	return ctx, localConfigReadError(ctx)
}

// localProjectConfigFile names the project layer: the checkout's own
// .agents/soho.conf, else, when cwd is a linked worktree, the main
// checkout's. Only a candidate that does not exist lets the search move on to
// the main checkout: an entry of any kind, or a lookup that failed for
// another reason (an unsearchable .agents, EACCES), is returned so that the
// layer reads it or reports it as unreadable, instead of the state directory
// being taken silently from another root's configuration.
func localProjectConfigFile(env platform.Env, cwd string) string {
	seen := map[string]bool{}
	for _, root := range []string{platform.ProjectRoot(env, cwd), platform.StateProjectRoot(env, cwd)} {
		if root == "" || seen[root] {
			continue
		}
		seen[root] = true
		file := filepath.Join(root, ".agents", "soho.conf")
		if _, err := os.Lstat(file); err == nil || !os.IsNotExist(err) {
			return file
		}
	}
	return ""
}

// loadLocalConfigLayer records one layer of the local read-only load with
// loadConfigFile's parser, and with one deliberate difference: a path that
// exists and cannot be read is the layer's ReadError rather than a missing
// layer. It applies the same kind rule as projectConfigProblem — a symlink to
// a regular file is read, a symlink to a directory or to a missing target is
// not a regular file. A symlink whose target is gone is inspected with Lstat
// because the read reports it as os.IsNotExist.
func loadLocalConfigLayer(ctx *Config, file, label string) {
	if file == "" {
		return
	}
	if absolute, err := filepath.Abs(file); err == nil {
		file = absolute
	}
	record := ConfigLayer{Source: label, File: file, Entries: make(map[string]ConfigEntry)}
	info, err := os.Stat(file)
	switch {
	case err != nil && !os.IsNotExist(err):
		record.ReadError = err
	case err != nil:
		if _, entryErr := os.Lstat(file); entryErr == nil {
			record.ReadError = fmt.Errorf("read %s: %v", file, err)
		}
	case !info.Mode().IsRegular():
		record.ReadError = fmt.Errorf("read %s: not a regular file", file)
	default:
		raw, readErr := platform.ReadTextFile(file)
		if readErr != nil {
			record.ReadError = readErr
			break
		}
		ctx.Sources = append(ctx.Sources, label)
		applyConfigText(raw, &record, ctx)
	}
	ctx.Layers = append(ctx.Layers, record)
}

// localConfigReadError reports the first layer of the local read-only load
// that exists and could not be read. An empty layer (missing file) is not an
// error.
func localConfigReadError(ctx Config) error {
	for _, layer := range ctx.Layers {
		if layer.ReadError != nil {
			return fmt.Errorf("%s configuration layer %s: %v", layer.Source, layer.File, layer.ReadError)
		}
	}
	return nil
}
