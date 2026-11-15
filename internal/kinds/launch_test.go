package kinds

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/transport"
)

// decodeConfig parses a resolved OPENCODE_CONFIG_CONTENT value back into a
// map, keeping numbers as json.Number so preservation assertions compare
// exact literals.
func decodeConfig(t *testing.T, cfg string) map[string]any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(cfg))
	dec.UseNumber()
	var parsed map[string]any
	if err := dec.Decode(&parsed); err != nil {
		t.Fatalf("resolved config is not a JSON object: %v", err)
	}
	return parsed
}

func configModel(t *testing.T, parsed map[string]any, path ...string) any {
	t.Helper()
	var cur any = parsed
	for _, key := range path {
		obj, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("config path %v: missing object at %q", path, key)
		}
		cur = obj[key]
	}
	return cur
}

// canonicalNativeEnv resolves the canonical resolver environment for the
// primary profile with the explicit inputs, so the validator cases
// compare against the resolver's own output instead of a re-stated copy
// of the policy.
func canonicalNativeEnv(t *testing.T, model, inline, configDir string) map[string]string {
	t.Helper()
	profile, err := ResolveLaunchProfileWithConfigDir("opencode", model, "", "hc", inline, configDir, nil)
	if err != nil {
		t.Fatalf("canonical resolver: unexpected refusal: %v", err)
	}
	if profile.Name != OpenCodePrimaryProfile {
		t.Fatalf("canonical resolver: expected the primary profile, got %q", profile.Name)
	}
	return profile.EnvOverrides
}

// mutateConfig re-decodes one canonical configuration with the existing
// UseNumber helper, applies the change and re-encodes it with the
// resolver's exact canonical rendering (sorted keys, no HTML escaping,
// no trailing newline), so a tampered value keeps canonical bytes
// around the one changed field.
func mutateConfig(t *testing.T, cfg string, change func(map[string]any)) string {
	t.Helper()
	parsed := decodeConfig(t, cfg)
	change(parsed)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(parsed); err != nil {
		t.Fatalf("canonical re-encode: %v", err)
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

func copyEnv(env map[string]string) map[string]string {
	out := make(map[string]string, len(env))
	for key, value := range env {
		out[key] = value
	}
	return out
}

func TestResolveLaunchProfile_NativeOpencodeExact(t *testing.T) {
	const model = "synthetic/provider-a/model-1"
	warns := []string{}
	warn := func(msg string) { warns = append(warns, msg) }

	profile, err := ResolveLaunchProfile("opencode", model, "high", "hc", "", warn)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if profile.Name != "opencode-primary-v2" {
		t.Fatalf("expected profile name opencode-primary-v2, got %q", profile.Name)
	}
	if len(profile.ModelArgs) != 0 {
		t.Fatalf("native profile must generate no model CLI args, got %v", profile.ModelArgs)
	}
	if len(profile.ExtraArgs) != 1 || profile.ExtraArgs[0] != "--standalone" {
		t.Fatalf("expected ExtraArgs [--standalone], got %v", profile.ExtraArgs)
	}
	if len(profile.EnvOverrides) != 2 {
		t.Fatalf("expected exactly two env overrides, got %d", len(profile.EnvOverrides))
	}
	if profile.EnvOverrides["OPENCODE_DISABLE_AUTOUPDATE"] != "1" {
		t.Fatalf("expected OPENCODE_DISABLE_AUTOUPDATE=1, got %q", profile.EnvOverrides["OPENCODE_DISABLE_AUTOUPDATE"])
	}
	cfg := profile.EnvOverrides["OPENCODE_CONFIG_CONTENT"]
	// Empty inline config is the empty object, so the merged config is
	// exactly the three pinned entries. Map keys marshal sorted, so the
	// exact bytes are deterministic.
	want := `{"agents":{"build":{"model":"` + model + `"}},"default_agent":"build","model":"` + model + `"}`
	if cfg != want {
		t.Fatalf("empty-inline config bytes:\n got %s\nwant %s", cfg, want)
	}
	parsed := decodeConfig(t, cfg)
	if len(parsed) != 3 {
		t.Fatalf("expected exactly 3 root entries, got %v", parsed)
	}
	if parsed["model"] != model {
		t.Fatalf("root model: expected %q, got %v", model, parsed["model"])
	}
	if parsed["default_agent"] != "build" {
		t.Fatalf("default_agent: expected build, got %v", parsed["default_agent"])
	}
	agents := configModel(t, parsed, "agents")
	if _, ok := agents.(map[string]any); !ok {
		t.Fatalf("agents must be an object, got %T", agents)
	}
	build := configModel(t, parsed, "agents", "build")
	if m, ok := build.(map[string]any); !ok || m["model"] != model {
		t.Fatalf("agents.build.model: expected %q, got %v", model, build)
	}
	if len(warns) != 0 {
		t.Fatalf("native path must not warn, got %v", warns)
	}
}

func TestResolveLaunchProfile_NativeModelGrammar(t *testing.T) {
	valid := []string{
		"prov/model",
		"prov/group/model",
		"prov//model",
		"prov/model/",
		"prov/model/deep/nested",
		"prov/model/a#va/nt",
		"prov/model#variant",
		"prov/model#v2",
		"prov/model#va-rian_1",
	}
	for _, model := range valid {
		profile, err := ResolveLaunchProfile("opencode", model, "", "hc", "", nil)
		if err != nil {
			t.Fatalf("model %q: unexpected refusal: %v", model, err)
		}
		parsed := decodeConfig(t, profile.EnvOverrides["OPENCODE_CONFIG_CONTENT"])
		// The exact requested string, variant included, is pinned as-is.
		if parsed["model"] != model {
			t.Fatalf("model %q: root model: expected exact %q, got %v", model, model, parsed["model"])
		}
		if got := configModel(t, parsed, "agents", "build", "model"); got != model {
			t.Fatalf("model %q: agents.build.model: expected exact %q, got %v", model, model, got)
		}
	}

	invalid := []string{
		"",
		"model-only",
		"prov/",
		"/model",
		"prov/model#",
		"prov/model#v#",
		"#variant",
		"a#b/model",
		"#/model",
		"prov/\x00model",
		"prov/model\x07",
		"prov/model#va\nt",
	}
	for _, model := range invalid {
		_, err := ResolveLaunchProfile("opencode", model, "", "hc", "", nil)
		if err == nil {
			t.Fatalf("model %q: expected refusal, got nil error", model)
		}
		// The refusal is a compile-time fixed message: exact equality proves
		// no model text reached the error channel.
		if err.Error() != errInvalidModelRef.Error() {
			t.Fatalf("model %q: expected the fixed value-free message %q, got %q", model, errInvalidModelRef.Error(), err.Error())
		}
	}
}

func TestResolveLaunchProfile_NativeInlineConfigMerge(t *testing.T) {
	const model = "prov/model"
	inline := `{
		"model": "stale/provider/old",
		"default_agent": "plan",
		"theme": "dark",
		"compaction": {"threshold": 0.75, "auto": true},
		"cost": {"input": 9007199254740993, "output": 1.23456789123456},
		"plugins": ["synthetic/mcp-a", 3, null],
		"agents": {
			"build": {"temperature": 0.2, "model": "stale/provider/old"},
			"review": {"model": "prov/other"}
		}
	}`

	profile, err := ResolveLaunchProfile("opencode", model, "", "hc", inline, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cfg := profile.EnvOverrides["OPENCODE_CONFIG_CONTENT"]
	parsed := decodeConfig(t, cfg)

	// The requested model wins at all three pinned locations.
	if parsed["model"] != model {
		t.Fatalf("root model: expected %q, got %v", model, parsed["model"])
	}
	if parsed["default_agent"] != "build" {
		t.Fatalf("default_agent: expected build, got %v", parsed["default_agent"])
	}
	if got := configModel(t, parsed, "agents", "build", "model"); got != model {
		t.Fatalf("agents.build.model: expected %q, got %v", model, got)
	}

	// Every unrelated field survives with its exact value.
	if parsed["theme"] != "dark" {
		t.Fatalf("theme lost: %v", parsed["theme"])
	}
	if got := configModel(t, parsed, "compaction", "threshold"); got != json.Number("0.75") {
		t.Fatalf("compaction.threshold: expected 0.75, got %v", got)
	}
	if got := configModel(t, parsed, "compaction", "auto"); got != true {
		t.Fatalf("compaction.auto: expected true, got %v", got)
	}
	if got := configModel(t, parsed, "agents", "build", "temperature"); got != json.Number("0.2") {
		t.Fatalf("agents.build.temperature: expected 0.2, got %v", got)
	}
	if got := configModel(t, parsed, "agents", "review", "model"); got != "prov/other" {
		t.Fatalf("agents.review.model lost: %v", got)
	}
	plugins := configModel(t, parsed, "plugins")
	plist, ok := plugins.([]any)
	if !ok || len(plist) != 3 || plist[0] != "synthetic/mcp-a" || plist[1] != json.Number("3") || plist[2] != nil {
		t.Fatalf("plugins lost or mangled: %v", plugins)
	}

	// UseNumber preservation: the integer above the float64 exact range and
	// the long fraction must appear as their exact literals in the output.
	if !strings.Contains(cfg, "9007199254740993") {
		t.Fatalf("integer literal collapsed through float64: %s", cfg)
	}
	if !strings.Contains(cfg, "1.23456789123456") {
		t.Fatalf("fraction literal collapsed: %s", cfg)
	}

	// Root key set is unchanged (7 entries: the 5 unrelated plus model and
	// default_agent, agents replaced in place).
	if len(parsed) != 7 {
		t.Fatalf("expected 7 root entries, got %d: %v", len(parsed), parsed)
	}
}

func TestResolveLaunchProfile_NativeInlineConfigRefusals(t *testing.T) {
	const sentinel = "SENTINEL_INLINE_VALUE_7f3a"
	invalid := []struct {
		inline string
		want   error
	}{
		{"null", errInvalidInlineCfg},
		{"[1, 2]", errInvalidInlineCfg},
		{`"a string"`, errInvalidInlineCfg},
		{"42", errInvalidInlineCfg},
		{"true", errInvalidInlineCfg},
		{"{", errInvalidInlineCfg},
		{"{}{}", errInvalidInlineCfg},
		{`{"a": 1} {"b": 2}`, errInvalidInlineCfg},
		{`{"a": 1} ` + sentinel, errInvalidInlineCfg},
		{`{"agents": "not-an-object"}`, errInvalidAgentsBuild},
		{`{"agents": null}`, errInvalidAgentsBuild},
		{`{"agents": []}`, errInvalidAgentsBuild},
		{`{"agents": {"build": "not-an-object"}}`, errInvalidAgentsBuild},
		{`{"agents": {"build": null}}`, errInvalidAgentsBuild},
	}
	for _, tc := range invalid {
		profile, err := ResolveLaunchProfile("opencode", "prov/model", "", "hc", tc.inline, nil)
		if err == nil {
			t.Fatalf("inline %q: expected refusal, got profile %+v", tc.inline, profile)
		}
		if !errors.Is(err, tc.want) {
			t.Fatalf("inline %q: expected %v, got %v", tc.inline, tc.want, err)
		}
		// The refusal is a compile-time fixed message: exact equality proves
		// no configuration content (the sentinel included) reached the
		// error channel.
		if err.Error() != tc.want.Error() {
			t.Fatalf("inline %q: expected the fixed value-free message %q, got %q", tc.inline, tc.want.Error(), err.Error())
		}
	}

	// Trailing whitespace after the single object is not a trailing value.
	profile, err := ResolveLaunchProfile("opencode", "prov/model", "", "hc", "{\"theme\":\"dark\"}  \n", nil)
	if err != nil {
		t.Fatalf("trailing whitespace must be tolerated: %v", err)
	}
	parsed := decodeConfig(t, profile.EnvOverrides["OPENCODE_CONFIG_CONTENT"])
	if parsed["theme"] != "dark" {
		t.Fatalf("unrelated field lost: %v", parsed)
	}
}

func TestResolveLaunchProfile_NativeEncodingBoundary(t *testing.T) {
	// Mutations this test kills (executed in the external copy):
	// removing the model UTF-8 refusal makes the first section pass and
	// removing the inline-input UTF-8 refusal makes the second pass.

	// Malformed raw bytes inside the model are refused with the fixed
	// value-free message before any grammar check or encoding: without
	// the refusal, encoding/json would rewrite the bytes as U+FFFD and
	// return a profile carrying a different model.
	invalidModels := []string{
		"prov/" + string([]byte{0xff, 0xfe}),
		string([]byte{0xc3, 0x28}),
		"prov/model" + string([]byte{0xff, 0xbc, 0x90}),
	}
	for _, model := range invalidModels {
		profile, err := ResolveLaunchProfile("opencode", model, "", "hc", "", nil)
		if err == nil {
			t.Fatalf("malformed model: expected refusal, got profile %+v", profile)
		}
		// The fixed message proves the raw bytes never reached the error
		// channel.
		if err.Error() != errInvalidModelRef.Error() {
			t.Fatalf("malformed model: expected the fixed value-free message %q, got %q", errInvalidModelRef.Error(), err.Error())
		}
	}

	// Malformed raw bytes inside the inline JSON string are refused with
	// the fixed value-free message before decoding: without the refusal,
	// the decoder would replace the bytes and the merged configuration
	// would carry a changed value.
	invalidInlines := []string{
		`{"note":"v` + string([]byte{0xff}) + `"}`,
		string([]byte{0xff, 0xfe}) + `{}`,
		`{"permissions":{"edit":"v` + string([]byte{0xc3, 0x28}) + `"}}`,
	}
	for _, inline := range invalidInlines {
		profile, err := ResolveLaunchProfile("opencode", "prov/model", "", "hc", inline, nil)
		if err == nil {
			t.Fatalf("malformed inline: expected refusal, got profile %+v", profile)
		}
		if err.Error() != errInvalidInlineCfg.Error() {
			t.Fatalf("malformed inline: expected the fixed value-free message %q, got %q", errInvalidInlineCfg.Error(), err.Error())
		}
	}

	// Valid Unicode and a literal U+FFFD character are valid UTF-8: they
	// must be accepted and preserved exactly at every pinned location,
	// with unknown fields, permissions and large numbers unchanged and
	// the requested valid text pinned as-is.
	const model = "prov/m\u00f6del-\u4e2d\u6587-\uFFFD"
	const inline = `{"note":"v\uFFFD-\u00e9","permissions":{"edit":"deny"},"cost":{"input":9007199254740993}}`
	profile, err := ResolveLaunchProfile("opencode", model, "", "hc", inline, nil)
	if err != nil {
		t.Fatalf("valid Unicode and literal U+FFFD must be accepted: %v", err)
	}
	cfg := profile.EnvOverrides["OPENCODE_CONFIG_CONTENT"]
	parsed := decodeConfig(t, cfg)
	if parsed["model"] != model {
		t.Fatalf("root model: expected exact %q, got %v", model, parsed["model"])
	}
	if got := configModel(t, parsed, "agents", "build", "model"); got != model {
		t.Fatalf("agents.build.model: expected exact %q, got %v", model, got)
	}
	if got := configModel(t, parsed, "note"); got != "v\uFFFD-\u00e9" {
		t.Fatalf("note: expected the exact requested text, got %v", got)
	}
	if got := configModel(t, parsed, "permissions", "edit"); got != "deny" {
		t.Fatalf("permissions.edit lost or changed: %v", got)
	}
	if !strings.Contains(cfg, "9007199254740993") {
		t.Fatalf("integer literal collapsed through float64: %s", cfg)
	}
}

func TestResolveLaunchProfile_NonNativePassthrough(t *testing.T) {
	cases := []struct {
		kind      string
		model     string
		effort    string
		transport string
		wantArgs  []string
		wantWarn  string
	}{
		{"claude", "anthropic/claude-x", "", "hc", []string{"--model", "anthropic/claude-x"}, ""},
		{"codex", "openai/gpt-x", "", "hc", []string{"-m", "openai/gpt-x"}, ""},
		{"grok", "xai/grok-x", "", "hc", []string{"--model", "xai/grok-x"}, ""},
		{"opencode", "prov/model", "", "herdr", []string{"-m", "prov/model"}, ""},
		{"opencode", "prov/model", "", "", []string{"-m", "prov/model"}, ""},
		{"opencode", "prov/model", "", "HC", []string{"-m", "prov/model"}, ""},
		{"claude", "", "high", "hc", nil, ""},
		{"cursor", "grok-x", "high", "hc", nil, ""},
		{"zzz", "prov/model", "", "hc", nil, "no model mapping for kind 'zzz'; model ignored"},
	}
	for _, tc := range cases {
		t.Run(tc.kind+"/"+tc.transport+"/"+tc.model, func(t *testing.T) {
			warns := []string{}
			warn := func(msg string) { warns = append(warns, msg) }

			profile, err := ResolveLaunchProfile(tc.kind, tc.model, tc.effort, tc.transport, "MUST_NOT_BE_READ", warn)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if profile.Name != "" {
				t.Fatalf("pass-through must carry no profile name, got %q", profile.Name)
			}
			if profile.ExtraArgs != nil {
				t.Fatalf("pass-through must carry no extra args, got %v", profile.ExtraArgs)
			}
			if profile.EnvOverrides != nil {
				t.Fatalf("pass-through must carry no env overrides, got %v", profile.EnvOverrides)
			}
			if len(profile.ModelArgs) != len(tc.wantArgs) {
				t.Fatalf("ModelArgs: expected %v, got %v", tc.wantArgs, profile.ModelArgs)
			}
			for i := range tc.wantArgs {
				if profile.ModelArgs[i] != tc.wantArgs[i] {
					t.Fatalf("ModelArgs[%d]: expected %q, got %q", i, tc.wantArgs[i], profile.ModelArgs[i])
				}
			}
			if tc.wantWarn == "" {
				if len(warns) != 0 {
					t.Fatalf("unexpected warns: %v", warns)
				}
			} else if len(warns) != 1 || warns[0] != tc.wantWarn {
				t.Fatalf("expected warn %q, got %v", tc.wantWarn, warns)
			}
		})
	}
}

func TestResolveLaunchProfile_NativeNeverReadsProcessEnv(t *testing.T) {
	// The resolver must build env from the explicit inputs only, never from
	// the process environment: pre-seed the very names it would set with
	// values that would be wrong if inherited.
	t.Setenv("OPENCODE_DISABLE_AUTOUPDATE", "0")
	t.Setenv("OPENCODE_CONFIG_CONTENT", `{"model":"env/should/not/win"}`)

	const model = "prov/model"
	profile, err := ResolveLaunchProfile("opencode", model, "", "hc", "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if profile.EnvOverrides["OPENCODE_DISABLE_AUTOUPDATE"] != "1" {
		t.Fatalf("OPENCODE_DISABLE_AUTOUPDATE inherited process env instead of 1: %q", profile.EnvOverrides["OPENCODE_DISABLE_AUTOUPDATE"])
	}
	parsed := decodeConfig(t, profile.EnvOverrides["OPENCODE_CONFIG_CONTENT"])
	if parsed["model"] != model {
		t.Fatalf("config model inherited process env: %v", parsed["model"])
	}
}

func TestResolveLaunchProfileWithConfigDir(t *testing.T) {
	const model = "prov/model"

	// Parity with the existing resolver, garbage configuration
	// directories included: every non-native combination ignores the
	// directory and keeps the exact old profile, warn behavior and error
	// outcome, and the native combination with an empty directory keeps
	// exactly the resolver's two overrides.
	parity := []struct {
		kind      string
		transport string
		configDir string
	}{
		{"claude", "hc", "/explicit/dir"},
		{"codex", "hc", ""},
		{"pi", "hc", "nul-\x00-dir"},
		{"grok", "hc", string([]byte{0xff, 0xfe})},
		{"agy", "hc", ""},
		{"opencode", "herdr", "\x00"},
		{"opencode", "", "/explicit/dir"},
		{"opencode", "HC", "ignored"},
		{"opencode", "hc", ""},
		{"gemini", "hc", "nul-\x00-dir"},
		{"cursor", "hc", ""},
		{"zzz", "hc", "/explicit/dir"},
	}
	for _, tc := range parity {
		t.Run(tc.kind+"/"+tc.transport, func(t *testing.T) {
			oldWarns, newWarns := []string{}, []string{}
			oldProfile, oldErr := ResolveLaunchProfile(tc.kind, model, "high", tc.transport, "MUST_NOT_BE_READ", func(msg string) { oldWarns = append(oldWarns, msg) })
			newProfile, newErr := ResolveLaunchProfileWithConfigDir(tc.kind, model, "high", tc.transport, "MUST_NOT_BE_READ", tc.configDir, func(msg string) { newWarns = append(newWarns, msg) })
			if (oldErr == nil) != (newErr == nil) {
				t.Fatalf("parity: old error %v, new error %v", oldErr, newErr)
			}
			if oldErr != nil {
				if !errors.Is(newErr, oldErr) {
					t.Fatalf("parity: expected the same refusal %v, got %v", oldErr, newErr)
				}
				return
			}
			if !reflect.DeepEqual(oldProfile, newProfile) {
				t.Fatalf("parity: profile drifted\n old %+v\n new %+v", oldProfile, newProfile)
			}
			if !reflect.DeepEqual(oldWarns, newWarns) {
				t.Fatalf("parity: warns drifted\n old %v\n new %v", oldWarns, newWarns)
			}
		})
	}

	// The native profile gains exactly the explicit directory override:
	// the same name, model args, extra args and configuration bytes,
	// with one third override and no default invented for an empty
	// directory.
	const dir = "/explicit/dir"
	oldProfile, oldErr := ResolveLaunchProfile("opencode", model, "", "hc", "", nil)
	if oldErr != nil {
		t.Fatalf("resolver: unexpected error: %v", oldErr)
	}
	profile, err := ResolveLaunchProfileWithConfigDir("opencode", model, "", "hc", "", dir, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if profile.Name != OpenCodePrimaryProfile {
		t.Fatalf("expected the primary profile, got %q", profile.Name)
	}
	if !reflect.DeepEqual(profile.ExtraArgs, oldProfile.ExtraArgs) {
		t.Fatalf("ExtraArgs drifted: %v", profile.ExtraArgs)
	}
	if !reflect.DeepEqual(profile.ModelArgs, oldProfile.ModelArgs) {
		t.Fatalf("ModelArgs drifted: %v", profile.ModelArgs)
	}
	if len(profile.EnvOverrides) != 3 {
		t.Fatalf("expected exactly three env overrides, got %d", len(profile.EnvOverrides))
	}
	for key, want := range oldProfile.EnvOverrides {
		if profile.EnvOverrides[key] != want {
			t.Fatalf("env override %q drifted: got %q, want %q", key, profile.EnvOverrides[key], want)
		}
	}
	if profile.EnvOverrides["OPENCODE_CONFIG_DIR"] != dir {
		t.Fatalf("OPENCODE_CONFIG_DIR: expected %q, got %q", dir, profile.EnvOverrides["OPENCODE_CONFIG_DIR"])
	}

	// A NUL or invalid UTF-8 directory on the native combination is
	// refused with the fixed value-free error before any profile is
	// returned, with no directory text in the message.
	badDirs := []string{"a\x00b", "\x00", "ok-" + string([]byte{0xff}), string([]byte{0xc3, 0x28})}
	for _, badDir := range badDirs {
		_, err := ResolveLaunchProfileWithConfigDir("opencode", model, "", "hc", "", badDir, nil)
		if err == nil {
			t.Fatalf("directory %q: expected refusal, got nil error", badDir)
		}
		if !errors.Is(err, errInvalidConfigDir) {
			t.Fatalf("directory %q: expected the fixed refusal, got %v", badDir, err)
		}
		if err.Error() != "launch profile: configuration directory is malformed" {
			t.Fatalf("directory %q: expected the fixed value-free message, got %q", badDir, err.Error())
		}
	}

	// The resolver's own refusals come first, unchanged: a malformed
	// model or inline configuration refuses before the directory is
	// inspected.
	if _, err := ResolveLaunchProfileWithConfigDir("opencode", "model-only", "", "hc", "", "bad\x00dir", nil); !errors.Is(err, errInvalidModelRef) {
		t.Fatalf("malformed model: expected the resolver's refusal, got %v", err)
	}
	if _, err := ResolveLaunchProfileWithConfigDir("opencode", model, "", "hc", "null", "bad\x00dir", nil); !errors.Is(err, errInvalidInlineCfg) {
		t.Fatalf("malformed inline: expected the resolver's refusal, got %v", err)
	}

	// The native path never warns, with a directory or not.
	warns := []string{}
	if _, err := ResolveLaunchProfileWithConfigDir("opencode", model, "high", "hc", "", dir, func(msg string) { warns = append(warns, msg) }); err != nil || len(warns) != 0 {
		t.Fatalf("native path: expected no error and no warn, got %v and %v", err, warns)
	}
}

func TestNativeStatusSupported(t *testing.T) {
	cases := []struct {
		kind    string
		profile string
		want    bool
	}{
		{"claude", "", true},
		{"claude", OpenCodePrimaryProfile, false},
		{"claude", "other-profile", false},
		{"codex", "", true},
		{"codex", OpenCodePrimaryProfile, false},
		{"codex", "other-profile", false},
		{"pi", "", true},
		{"pi", OpenCodePrimaryProfile, false},
		{"pi", "other-profile", false},
		{"grok", "", true},
		{"grok", OpenCodePrimaryProfile, false},
		{"grok", "other-profile", false},
		{"agy", "", true},
		{"agy", OpenCodePrimaryProfile, false},
		{"agy", "other-profile", false},
		{"opencode", OpenCodePrimaryProfile, true},
		{"opencode", "", false},
		{"opencode", "opencode-primary-v1", false},
		{"opencode", "OPENCODE-PRIMARY-V2", false},
		{"opencode", "primary", false},
		{"gemini", "", false},
		{"gemini", OpenCodePrimaryProfile, false},
		{"cursor", "", false},
		{"cursor", OpenCodePrimaryProfile, false},
		{"zzz", "", false},
		{"zzz", OpenCodePrimaryProfile, false},
		{"OPENCODE", OpenCodePrimaryProfile, false},
	}
	for _, tc := range cases {
		if got := NativeStatusSupported(tc.kind, tc.profile); got != tc.want {
			t.Fatalf("kind %q profile %q: expected %v, got %v", tc.kind, tc.profile, tc.want, got)
		}
	}
}

func TestValidateNativeLaunchProfile(t *testing.T) {
	const model = "prov/model"
	const dir = "/explicit/dir"
	const fixed = "native launch profile is invalid"

	// Pass-through: the five empty-profile kinds admit any argv and any
	// overrides, the pass-through profile carries no managed metadata.
	for _, kind := range []string{"claude", "codex", "pi", "grok", "agy"} {
		err := ValidateNativeLaunchProfile(kind, "", []string{"--model", "prov/x", "--effort", "high"}, map[string]string{"OPENCODE_CONFIG_CONTENT": "forged"})
		if err != nil {
			t.Fatalf("kind %q: pass-through must admit any metadata, got %v", kind, err)
		}
	}

	// Unsupported kind/profile combinations refuse with the fixed
	// value-free error, whatever the argv or environment carry.
	unsupported := []struct {
		kind    string
		profile string
	}{
		{"claude", OpenCodePrimaryProfile},
		{"codex", "other-profile"},
		{"pi", OpenCodePrimaryProfile},
		{"grok", "other-profile"},
		{"agy", OpenCodePrimaryProfile},
		{"opencode", ""},
		{"opencode", "opencode-primary-v1"},
		{"opencode", "OPENCODE-PRIMARY-V2"},
		{"gemini", ""},
		{"gemini", OpenCodePrimaryProfile},
		{"cursor", ""},
		{"zzz", ""},
		{"zzz", OpenCodePrimaryProfile},
	}
	for _, tc := range unsupported {
		err := ValidateNativeLaunchProfile(tc.kind, tc.profile, []string{"--standalone"}, map[string]string{"OPENCODE_DISABLE_AUTOUPDATE": "1"})
		if !errors.Is(err, errInvalidNativeProfile) || err.Error() != fixed {
			t.Fatalf("kind %q profile %q: expected the fixed value-free refusal, got %v", tc.kind, tc.profile, err)
		}
	}

	base := canonicalNativeEnv(t, model, "", "")
	baseDir := canonicalNativeEnv(t, model, "", dir)
	baseCfg := base["OPENCODE_CONFIG_CONTENT"]

	// Canonical unknown-field and large-number preservation: the
	// validator admits the resolver's own output with unrelated fields,
	// other agents and the integer above the float64 exact range
	// intact, and with the provider/model variant and repeated slashes
	// pinned as-is.
	const variantModel = "prov/group/model-1/v2#variant"
	const richInline = `{"theme":"dark","cost":{"input":9007199254740993},"plugins":["synthetic/mcp-a",3],"agents":{"build":{"temperature":0.2},"review":{"model":"prov/other"}},"note":"v\uFFFD-\u00e9"}`
	rich := canonicalNativeEnv(t, variantModel, richInline, dir)

	validArgs := [][]string{
		{"--standalone"},
		{"--standalone", "--auto"},
		{"--auto", "--standalone"},
	}
	for _, env := range []map[string]string{base, baseDir, rich} {
		for _, args := range validArgs {
			if err := ValidateNativeLaunchProfile("opencode", OpenCodePrimaryProfile, args, env); err != nil {
				t.Fatalf("args %v: expected the canonical environment to pass, got %v", args, err)
			}
		}
	}

	// The validator re-resolves with the candidate's own directory, so a
	// consistent explicit nonempty directory the resolver would emit
	// verbatim is canonical even when the configuration was generated
	// without one.
	forgedDir := copyEnv(base)
	forgedDir["OPENCODE_CONFIG_DIR"] = "/another/dir"
	if err := ValidateNativeLaunchProfile("opencode", OpenCodePrimaryProfile, []string{"--standalone"}, forgedDir); err != nil {
		t.Fatalf("consistent explicit directory: expected pass, got %v", err)
	}

	invalid := []struct {
		name string
		args []string
		env  map[string]string
	}{
		{"no args", nil, base},
		{"empty args", []string{}, base},
		{"auto only", []string{"--auto"}, base},
		{"duplicate standalone", []string{"--standalone", "--standalone"}, base},
		{"duplicate auto", []string{"--standalone", "--auto", "--auto"}, base},
		{"foreign token", []string{"--standalone", "--model", "prov/x"}, base},
		{"flag with value", []string{"--standalone", "--standalone=1"}, base},
		{"short form", []string{"-standalone"}, base},
		{"capitalized flag", []string{"--STANDALONE"}, base},
		{"empty token", []string{"--standalone", ""}, base},
		{"nil overrides", []string{"--standalone"}, nil},
		{"empty overrides", []string{"--standalone"}, map[string]string{}},
	}
	addEnv := func(name string, change func(map[string]string)) {
		env := copyEnv(base)
		change(env)
		invalid = append(invalid, struct {
			name string
			args []string
			env  map[string]string
		}{name, []string{"--standalone"}, env})
	}
	addEnv("missing no-update", func(env map[string]string) { delete(env, "OPENCODE_DISABLE_AUTOUPDATE") })
	addEnv("empty no-update", func(env map[string]string) { env["OPENCODE_DISABLE_AUTOUPDATE"] = "" })
	addEnv("auto-update zero", func(env map[string]string) { env["OPENCODE_DISABLE_AUTOUPDATE"] = "0" })
	addEnv("missing config content", func(env map[string]string) { delete(env, "OPENCODE_CONFIG_CONTENT") })
	addEnv("empty config content", func(env map[string]string) { env["OPENCODE_CONFIG_CONTENT"] = "" })
	addEnv("unknown env key", func(env map[string]string) { env["OPENCODE_API_KEY"] = "SENTINEL_KEY_7f3a" })
	addEnv("spaced env key", func(env map[string]string) { env["OPENCODE_CONFIG_DIR "] = dir })
	addEnv("empty config dir", func(env map[string]string) { env["OPENCODE_CONFIG_DIR"] = "" })
	addEnv("nul config dir", func(env map[string]string) { env["OPENCODE_CONFIG_DIR"] = "a\x00" })
	addEnv("invalid utf8 config dir", func(env map[string]string) { env["OPENCODE_CONFIG_DIR"] = string([]byte{0xff}) })
	addCfg := func(name, cfg string) {
		env := copyEnv(base)
		env["OPENCODE_CONFIG_CONTENT"] = cfg
		invalid = append(invalid, struct {
			name string
			args []string
			env  map[string]string
		}{name, []string{"--standalone"}, env})
	}
	buildOf := func(root map[string]any) map[string]any {
		return root["agents"].(map[string]any)["build"].(map[string]any)
	}
	addCfg("raw invalid utf8", `{"note":"v`+string([]byte{0xff})+`"}`)
	addCfg("whitespace only", "   \n")
	addCfg("null object", "null")
	addCfg("array object", "[1, 2]")
	addCfg("string object", `"a config"`)
	addCfg("trailing value", baseCfg+`{"extra":1}`)
	addCfg("missing root model", mutateConfig(t, baseCfg, func(root map[string]any) { delete(root, "model") }))
	addCfg("nonstring root model", mutateConfig(t, baseCfg, func(root map[string]any) { root["model"] = 42 }))
	addCfg("refusal root model", mutateConfig(t, baseCfg, func(root map[string]any) { root["model"] = "model-only" }))
	addCfg("nul root model", mutateConfig(t, baseCfg, func(root map[string]any) { root["model"] = "prov/\x00model" }))
	addCfg("missing default agent", mutateConfig(t, baseCfg, func(root map[string]any) { delete(root, "default_agent") }))
	addCfg("wrong default agent", mutateConfig(t, baseCfg, func(root map[string]any) { root["default_agent"] = "plan" }))
	addCfg("nonstring default agent", mutateConfig(t, baseCfg, func(root map[string]any) { root["default_agent"] = 7 }))
	addCfg("missing agents", mutateConfig(t, baseCfg, func(root map[string]any) { delete(root, "agents") }))
	addCfg("nonobject agents", mutateConfig(t, baseCfg, func(root map[string]any) { root["agents"] = "nope" }))
	addCfg("missing build", mutateConfig(t, baseCfg, func(root map[string]any) { delete(root["agents"].(map[string]any), "build") }))
	addCfg("nonobject build", mutateConfig(t, baseCfg, func(root map[string]any) { root["agents"].(map[string]any)["build"] = 1 }))
	addCfg("mismatched build model", mutateConfig(t, baseCfg, func(root map[string]any) { buildOf(root)["model"] = "prov/other" }))
	addCfg("missing build model", mutateConfig(t, baseCfg, func(root map[string]any) { delete(buildOf(root), "model") }))
	addCfg("nonstring build model", mutateConfig(t, baseCfg, func(root map[string]any) { buildOf(root)["model"] = 2 }))
	addCfg("hidden true", mutateConfig(t, baseCfg, func(root map[string]any) { buildOf(root)["hidden"] = true }))
	addCfg("hidden string", mutateConfig(t, baseCfg, func(root map[string]any) { buildOf(root)["hidden"] = "false" }))
	addCfg("disabled true", mutateConfig(t, baseCfg, func(root map[string]any) { buildOf(root)["disabled"] = true }))
	addCfg("mode review", mutateConfig(t, baseCfg, func(root map[string]any) { buildOf(root)["mode"] = "review" }))
	addCfg("mode empty", mutateConfig(t, baseCfg, func(root map[string]any) { buildOf(root)["mode"] = "" }))
	addCfg("mode nonstring", mutateConfig(t, baseCfg, func(root map[string]any) { buildOf(root)["mode"] = 1 }))
	addCfg("unsorted keys", `{"model":"prov/model","default_agent":"build","agents":{"build":{"model":"prov/model"}}}`)
	addCfg("trailing space", baseCfg+"  \n")
	addCfg("duplicate key", `{"agents":{"build":{"model":"prov/model"}},"default_agent":"build","model":"prov/model","model":"prov/model"}`)
	addCfg("sentinel note field", `{"note":"SENTINEL_CFG_7f3a"}`)
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateNativeLaunchProfile("opencode", OpenCodePrimaryProfile, tc.args, tc.env)
			if err == nil {
				t.Fatalf("expected the fixed refusal, got nil error")
			}
			if !errors.Is(err, errInvalidNativeProfile) {
				t.Fatalf("expected the fixed refusal, got %v", err)
			}
			if err.Error() != fixed {
				t.Fatalf("expected the fixed value-free message %q, got %q", fixed, err.Error())
			}
		})
	}
}

func TestOpenCodePrimaryObservation(t *testing.T) {
	// The base terminal is exactly the observation the native primary
	// profile makes: the exact profile label, the record's own reported
	// flag, the opencode reporter app and the live native lifecycle.
	base := transport.Terminal{
		Target:        transport.Target{ID: "t7"},
		Name:          "worker-a",
		LaunchProfile: OpenCodePrimaryProfile,
		Reported:      true,
		ReporterApp:   "opencode",
		NativeState:   "live",
		State:         "idle",
	}
	cases := []struct {
		name     string
		mutate   func(term *transport.Terminal)
		observed bool
		ready    bool
	}{
		{"exact observation", func(term *transport.Terminal) {}, true, true},
		{"missing profile", func(term *transport.Terminal) { term.LaunchProfile = "" }, false, false},
		{"wrong profile", func(term *transport.Terminal) { term.LaunchProfile = "opencode-primary-v1" }, false, false},
		{"case folded profile", func(term *transport.Terminal) { term.LaunchProfile = "OPENCODE-PRIMARY-V2" }, false, false},
		{"trailing space profile", func(term *transport.Terminal) { term.LaunchProfile = OpenCodePrimaryProfile + " " }, false, false},
		{"reported false", func(term *transport.Terminal) { term.Reported = false }, false, false},
		{"missing app", func(term *transport.Terminal) { term.ReporterApp = "" }, false, false},
		{"wrong app", func(term *transport.Terminal) { term.ReporterApp = "claude" }, false, false},
		{"case folded app", func(term *transport.Terminal) { term.ReporterApp = "OpenCode" }, false, false},
		{"trailing space app", func(term *transport.Terminal) { term.ReporterApp = "opencode " }, false, false},
		{"missing native state", func(term *transport.Terminal) { term.NativeState = "" }, false, false},
		{"finished native state", func(term *transport.Terminal) { term.NativeState = "finished" }, false, false},
		{"capital native state", func(term *transport.Terminal) { term.NativeState = "Live" }, false, false},
		{"state done is ready", func(term *transport.Terminal) { term.State = "done" }, true, true},
		{"state working is not ready", func(term *transport.Terminal) { term.State = "working" }, true, false},
		{"state blocked is not ready", func(term *transport.Terminal) { term.State = "blocked" }, true, false},
		{"state error is not ready", func(term *transport.Terminal) { term.State = "error" }, true, false},
		{"missing state is not ready", func(term *transport.Terminal) { term.State = "" }, true, false},
		{"unreported done is not ready", func(term *transport.Terminal) { term.Reported = false; term.State = "done" }, false, false},
		{"foreign app done is not ready", func(term *transport.Terminal) { term.ReporterApp = "claude"; term.State = "done" }, false, false},
		{"finished done is not ready", func(term *transport.Terminal) { term.NativeState = "finished"; term.State = "done" }, false, false},
		{"wrong profile idle is not ready", func(term *transport.Terminal) { term.LaunchProfile = "other" }, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			term := base
			tc.mutate(&term)
			if got := OpenCodePrimaryObserved(term); got != tc.observed {
				t.Fatalf("observed: expected %v, got %v", tc.observed, got)
			}
			if got := OpenCodePrimaryReady(term); got != tc.ready {
				t.Fatalf("ready: expected %v, got %v", tc.ready, got)
			}
		})
	}
}
