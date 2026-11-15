// Package launchprofile is the neutral, standard-library-only home of the
// shared native launch policy: the launch profile type and the native
// primary profile constant, the value-free refusal errors, the native
// OpenCode model grammar and inline configuration parsing with canonical
// rendering, the explicit configuration-directory behavior, the native
// status policy, the complete native launch profile validator, and the
// primary observation and readiness predicates on primitive inputs. It
// imports only the standard library, so no soho package — kinds, core,
// backend, hcbackend or transport among them — can form a dependency cycle
// through it.
package launchprofile

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Profile is the shared launch resolution for one kind, model, effort and
// transport combination: the profile name a backend may admit, the model
// CLI args the caller passes, the extra native args the profile adds, and
// the explicit per-child environment overrides the backend merges into the
// child. Every non-native combination resolves to an empty Name, no
// ExtraArgs and no EnvOverrides, while ModelArgs carries exactly the
// existing KindModelArgs output, so zero-value callers and all current
// KindModelArgs consumers keep their exact behavior.
type Profile struct {
	Name         string
	ModelArgs    []string
	ExtraArgs    []string
	EnvOverrides map[string]string
}

// OpenCodePrimaryProfile is the launch profile of the native hc+opencode
// combination: the shared resolver's profile name for it, the only profile
// the shared native status policy admits for the kind, the label the hc
// backend's start request carries and the record field its readiness
// qualification requires.
const OpenCodePrimaryProfile = "opencode-primary-v2"

// The value-free refusal errors the resolver returns. They never carry the
// caller's model or inline configuration text, so a bad reference or config
// cannot be echoed back through the error channel.
var (
	ErrInvalidModelRef      = errors.New("launch profile: model reference is malformed")
	ErrInvalidInlineCfg     = errors.New("launch profile: inline configuration is not a single non-null JSON object")
	ErrInvalidAgentsBuild   = errors.New("launch profile: agents or agents.build is not a JSON object")
	ErrInvalidConfigDir     = errors.New("launch profile: configuration directory is malformed")
	ErrInvalidNativeProfile = errors.New("native launch profile is invalid")
)

// ResolveOpenCode builds the native hc+opencode launch profile for one
// explicit model reference and inline configuration, with the optional
// explicit configuration directory: the --standalone extra arg, no
// generated model CLI arg, and exactly two per-child environment overrides
// (OPENCODE_DISABLE_AUTOUPDATE=1 and OPENCODE_CONFIG_CONTENT carrying the
// merged configuration that pins the exact requested provider/model at
// root model, default_agent and agents.build.model), plus
// OPENCODE_CONFIG_DIR set to the caller's explicit configuration directory
// when it is nonempty. Malformed UTF-8 in the model or the inline
// configuration is refused with the fixed value-free errors before any
// grammar or JSON decoding, because encoding/json would silently rewrite
// the invalid bytes as U+FFFD and change the explicit input; valid Unicode
// and a literal U+FFFD character are valid UTF-8 and are preserved. A
// configuration directory that holds a NUL or invalid UTF-8 is refused
// with the fixed value-free error, and a malformed model or configuration
// refusal happens before the directory is examined; an empty directory
// adds nothing, so the profile keeps exactly the two overrides and no
// default is invented.
//
// The function is pure: it reads no files, process environment, config,
// auth or provider endpoints, and it mutates no caller input.
func ResolveOpenCode(model, inlineConfig, configDir string) (Profile, error) {
	// Refuse malformed UTF-8 in the explicit inputs before any grammar
	// check or JSON decoding: encoding/json would silently rewrite the
	// invalid bytes as U+FFFD and change the requested model or the
	// caller's configuration. Valid Unicode and a literal U+FFFD
	// character are valid UTF-8 and are preserved exactly.
	if !utf8.ValidString(model) {
		return Profile{}, ErrInvalidModelRef
	}
	if err := validateModelRef(model); err != nil {
		return Profile{}, err
	}
	if !utf8.ValidString(inlineConfig) {
		return Profile{}, ErrInvalidInlineCfg
	}
	config, err := buildOpencodeConfig(model, inlineConfig)
	if err != nil {
		return Profile{}, err
	}
	profile := Profile{
		Name:      OpenCodePrimaryProfile,
		ExtraArgs: []string{"--standalone"},
		EnvOverrides: map[string]string{
			"OPENCODE_DISABLE_AUTOUPDATE": "1",
			"OPENCODE_CONFIG_CONTENT":     string(config),
		},
	}
	if !utf8.ValidString(configDir) || strings.ContainsRune(configDir, '\x00') {
		return Profile{}, ErrInvalidConfigDir
	}
	if configDir != "" {
		profile.EnvOverrides["OPENCODE_CONFIG_DIR"] = configDir
	}
	return profile, nil
}

// validateModelRef accepts the explicit provider/model reference the native
// profile requires, with the same grammar as the retained config/model
// Short schema: the provider is split from the remaining model at the
// first slash only; the provider and the model id must be nonempty; an
// optional single nonempty #variant may follow; and any control character
// is refused. Slashes inside the model remainder (repeated or trailing)
// are preserved; a second # or a # inside the provider is refused.
func validateModelRef(model string) error {
	provider, rest, ok := strings.Cut(model, "/")
	if !ok || provider == "" || strings.ContainsRune(provider, '#') {
		return ErrInvalidModelRef
	}
	modelID, variant, hasVariant := strings.Cut(rest, "#")
	if modelID == "" || (hasVariant && (variant == "" || strings.ContainsRune(variant, '#'))) {
		return ErrInvalidModelRef
	}
	for _, r := range model {
		if unicode.IsControl(r) {
			return ErrInvalidModelRef
		}
	}
	return nil
}

// buildOpencodeConfig renders the OPENCODE_CONFIG_CONTENT value: the caller's
// inline configuration (an empty string is the empty object) merged with the
// exact requested model at root model, default_agent and
// agents.build.model. Every unrelated root, agents and build field — including
// compaction, plugins, other agents and existing build settings — survives in
// value; numbers stay json.Number literals so unknown data is never collapsed
// through a float round-trip. A non-object or trailing inline value, or a
// non-object agents or agents.build, is refused with a value-free error
// rather than erased.
func buildOpencodeConfig(model, inlineConfig string) ([]byte, error) {
	root, err := decodeInlineConfig(inlineConfig)
	if err != nil {
		return nil, err
	}
	root["model"] = model
	root["default_agent"] = "build"
	build, err := agentsBuildObject(root)
	if err != nil {
		return nil, err
	}
	build["model"] = model

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(root); err != nil {
		return nil, ErrInvalidInlineCfg
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// decodeInlineConfig decodes the caller's inline configuration as exactly one
// non-null JSON object with no trailing value, using UseNumber so unknown
// numbers keep their exact literal. An empty input is the empty object; any
// other absent or non-object value is refused.
func decodeInlineConfig(inlineConfig string) (map[string]any, error) {
	if inlineConfig == "" {
		return map[string]any{}, nil
	}
	dec := json.NewDecoder(strings.NewReader(inlineConfig))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, ErrInvalidInlineCfg
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return nil, ErrInvalidInlineCfg
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return nil, ErrInvalidInlineCfg
	}
	return obj, nil
}

// agentsBuildObject returns the root's agents.build object, creating the
// agents or build object when absent and refusing a non-object agents or
// build rather than erasing the caller's data.
func agentsBuildObject(root map[string]any) (map[string]any, error) {
	agentsValue, hasAgents := root["agents"]
	var agents map[string]any
	if hasAgents {
		obj, ok := agentsValue.(map[string]any)
		if !ok {
			return nil, ErrInvalidAgentsBuild
		}
		agents = obj
	} else {
		agents = map[string]any{}
		root["agents"] = agents
	}
	buildValue, hasBuild := agents["build"]
	var build map[string]any
	if hasBuild {
		obj, ok := buildValue.(map[string]any)
		if !ok {
			return nil, ErrInvalidAgentsBuild
		}
		build = obj
	} else {
		build = map[string]any{}
	}
	agents["build"] = build
	return build, nil
}

// NativeStatusSupported reports whether a launch profile is supported for
// native status under transport=hc: the five kinds whose executable
// reports the 7501 status the hc backend observes carry the empty
// (pass-through) profile, and opencode carries exactly the primary
// profile. Every other combination is unsupported.
func NativeStatusSupported(kind, profile string) bool {
	switch kind {
	case "claude", "codex", "pi", "grok", "agy":
		return profile == ""
	case "opencode":
		return profile == OpenCodePrimaryProfile
	default:
		return false
	}
}

// ValidateNativeLaunchProfile refuses a launch profile the native hc
// backend would not have generated. An unsupported kind/profile
// combination refuses outright, and the five empty-profile kinds pass
// through unchanged. The opencode primary profile additionally admits
// only the exact native argv and the exact canonical resolver
// environment: exactly one --standalone and at most one --auto, in
// either order, and no other token; OPENCODE_DISABLE_AUTOUPDATE=1 and
// the generated OPENCODE_CONFIG_CONTENT, plus the optional explicit
// nonempty OPENCODE_CONFIG_DIR, and no other override. The generated
// configuration must decode, with the resolver's UseNumber helper, to
// the resolver's own pinned shape for an explicit valid provider/model:
// the model at root model, default_agent build, agents.build.model
// equal to the root model, hidden or disabled only bool false and mode
// only primary or all, and a canonical re-resolution of the decoded
// model, configuration and optional directory must return exactly the
// candidate environment map. Every refusal is the one fixed value-free
// error, so no argument, configuration or directory text reaches the
// error channel.
func ValidateNativeLaunchProfile(kind, profile string, args []string, overrides map[string]string) error {
	if !NativeStatusSupported(kind, profile) {
		return ErrInvalidNativeProfile
	}
	if kind != "opencode" {
		return nil
	}
	standalone, auto := 0, 0
	for _, arg := range args {
		switch arg {
		case "--standalone":
			standalone++
		case "--auto":
			auto++
		default:
			return ErrInvalidNativeProfile
		}
	}
	if standalone != 1 || auto > 1 {
		return ErrInvalidNativeProfile
	}
	if overrides["OPENCODE_DISABLE_AUTOUPDATE"] != "1" {
		return ErrInvalidNativeProfile
	}
	config, hasConfig := overrides["OPENCODE_CONFIG_CONTENT"]
	if !hasConfig || !utf8.ValidString(config) {
		return ErrInvalidNativeProfile
	}
	configDir, hasConfigDir := overrides["OPENCODE_CONFIG_DIR"]
	if hasConfigDir && configDir == "" {
		return ErrInvalidNativeProfile
	}
	for key := range overrides {
		switch key {
		case "OPENCODE_DISABLE_AUTOUPDATE", "OPENCODE_CONFIG_CONTENT", "OPENCODE_CONFIG_DIR":
		default:
			return ErrInvalidNativeProfile
		}
	}
	root, err := decodeInlineConfig(config)
	if err != nil {
		return ErrInvalidNativeProfile
	}
	model, isModel := root["model"].(string)
	if !isModel || validateModelRef(model) != nil {
		return ErrInvalidNativeProfile
	}
	if root["default_agent"] != "build" {
		return ErrInvalidNativeProfile
	}
	agents, isAgents := root["agents"].(map[string]any)
	if !isAgents {
		return ErrInvalidNativeProfile
	}
	build, isBuild := agents["build"].(map[string]any)
	if !isBuild || build["model"] != model {
		return ErrInvalidNativeProfile
	}
	for _, key := range []string{"hidden", "disabled"} {
		if value, present := build[key]; present {
			if flag, isBool := value.(bool); !isBool || flag {
				return ErrInvalidNativeProfile
			}
		}
	}
	if value, present := build["mode"]; present {
		mode, isMode := value.(string)
		if !isMode || (mode != "primary" && mode != "all") {
			return ErrInvalidNativeProfile
		}
	}
	resolved, err := ResolveOpenCode(model, config, configDir)
	if err != nil {
		return ErrInvalidNativeProfile
	}
	if len(resolved.EnvOverrides) != len(overrides) {
		return ErrInvalidNativeProfile
	}
	for key, want := range overrides {
		if resolved.EnvOverrides[key] != want {
			return ErrInvalidNativeProfile
		}
	}
	return nil
}

// OpenCodePrimaryObserved reports whether one terminal observation is
// exactly the one the native hc+opencode primary profile makes: the
// exact primary profile as the launch profile label, the record's own
// reported flag true, the reporter application opencode, and the native
// lifecycle live.
//
// The inputs are the strict primitive fields a backend reads from the
// terminal record (the hc backend's Get populates its transport
// projection from them), so the policy stays on standard library types
// and needs no transport import.
func OpenCodePrimaryObserved(profile string, reported bool, app, nativeState string) bool {
	return profile == OpenCodePrimaryProfile &&
		reported &&
		app == "opencode" &&
		nativeState == "live"
}

// OpenCodePrimaryReady reports whether the observed primary terminal is
// ready: its reported agent activity is idle or done.
func OpenCodePrimaryReady(profile string, reported bool, app, nativeState, state string) bool {
	if !OpenCodePrimaryObserved(profile, reported, app, nativeState) {
		return false
	}
	return state == "idle" || state == "done"
}
