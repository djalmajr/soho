package kinds

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestResolveLaunchTUIV2Exact(t *testing.T) {
	for _, transport := range []string{"", "herdr", "hc"} {
		t.Run(transport, func(t *testing.T) {
			req := LaunchRequest{Kind: "opencode", Model: "供应商/模型/nested#变体", Effort: "high", Transport: transport, TUIProfile: "v2", InlineConfig: `{"number":900719925474099312345,"plugins":["keep"],"agents":{"other":{"model":"keep/other"},"build":{"temperature":0.25}}}`, ConfigDir: "/explicit/config space"}
			before := req
			var warns []string
			got, err := ResolveLaunch(req, func(s string) { warns = append(warns, s) })
			if err != nil {
				t.Fatal(err)
			}
			want := LaunchProfile{Name: "opencode-primary-v2", ExtraArgs: []string{"--standalone"}, EnvOverrides: map[string]string{
				"OPENCODE_DISABLE_AUTOUPDATE": "1", "OPENCODE_CONFIG_DIR": "/explicit/config space",
				"OPENCODE_CONFIG_CONTENT": `{"agents":{"build":{"model":"供应商/模型/nested#变体","temperature":0.25},"other":{"model":"keep/other"}},"default_agent":"build","model":"供应商/模型/nested#变体","number":900719925474099312345,"plugins":["keep"]}`,
			}}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("profile = %#v; want %#v", got, want)
			}
			if req != before || len(warns) != 0 {
				t.Fatalf("request changed or warning emitted: %#v %v", req, warns)
			}
			parsed := decodeConfig(t, got.EnvOverrides["OPENCODE_CONFIG_CONTENT"])
			if parsed["number"] != json.Number("900719925474099312345") {
				t.Fatal("numeric value changed")
			}
			got.EnvOverrides["OPENCODE_CONFIG_CONTENT"] = "tampered"
			again, err := ResolveLaunch(req, nil)
			if err != nil || !reflect.DeepEqual(again, want) {
				t.Fatalf("result aliases mutable state: %#v %v", again, err)
			}
		})
	}
}

func TestResolveLaunchTUIRefusals(t *testing.T) {
	badUTF8 := string([]byte{0xff})
	for _, selector := range []string{"V2", "Legacy", "v2 ", " v2", "unknown-canary", "\x00", badUTF8} {
		for _, transport := range []string{"", "herdr", "HC", "unknown-canary"} {
			req := LaunchRequest{Kind: "opencode", Model: "invalid-model-canary", Transport: transport, TUIProfile: selector, InlineConfig: "config-canary", ConfigDir: "dir-canary\x00"}
			got, err := ResolveLaunch(req, nil)
			if err != errInvalidTUIProfile || err.Error() != "launch profile: OpenCode TUI profile is invalid" || !reflect.DeepEqual(got, LaunchProfile{}) {
				t.Fatalf("selector refusal: %#v %v", got, err)
			}
		}
	}
	for _, transport := range []string{"HC", "Herdr", "herdr ", "unknown-canary", "\x00", badUTF8} {
		got, err := ResolveLaunch(LaunchRequest{Kind: "opencode", Model: "p/m", Transport: transport, TUIProfile: "v2"}, nil)
		if err != errInvalidTUIProfile || !reflect.DeepEqual(got, LaunchProfile{}) {
			t.Fatalf("transport refusal: %#v %v", got, err)
		}
	}
	cases := []struct {
		model, config, dir string
		want               error
	}{
		{"", "config-canary", "dir\x00", errInvalidModelRef},
		{"canary", "config-canary", "dir\x00", errInvalidModelRef},
		{"p/m#", "", "", errInvalidModelRef},
		{"p/m#one#two", "", "", errInvalidModelRef},
		{"p/m\ncanary", "", "", errInvalidModelRef},
		{"p/" + badUTF8, "", "", errInvalidModelRef},
		{"p/m", badUTF8, "dir\x00", errInvalidInlineCfg},
		{"p/m", `null`, "dir\x00", errInvalidInlineCfg},
		{"p/m", `[]`, "", errInvalidInlineCfg},
		{"p/m", `{} {"canary":true}`, "", errInvalidInlineCfg},
		{"p/m", `{"canary":`, "", errInvalidInlineCfg},
		{"p/m", `{"agents":null}`, "", errInvalidAgentsBuild},
		{"p/m", `{"agents":{"build":[]}}`, "", errInvalidAgentsBuild},
		{"p/m", `{}`, "dir-canary\x00", errInvalidConfigDir},
		{"p/m", `{}`, badUTF8, errInvalidConfigDir},
	}
	for _, tc := range cases {
		for _, transport := range []string{"", "herdr", "hc"} {
			req := LaunchRequest{Kind: "opencode", Model: tc.model, Transport: transport, TUIProfile: "v2", InlineConfig: tc.config, ConfigDir: tc.dir}
			before := req
			var warns []string
			got, err := ResolveLaunch(req, func(s string) { warns = append(warns, s) })
			if err != tc.want || !reflect.DeepEqual(got, LaunchProfile{}) || req != before || len(warns) != 0 {
				t.Fatalf("refusal on %q: %#v %v; want %v; warnings %v", transport, got, err, tc.want, warns)
			}
		}
	}
}

func TestResolveLaunchTUICompatibility(t *testing.T) {
	invalid := string([]byte{0xff}) + "\x00canary"
	for _, kind := range []string{"opencode", "claude", "codex", "pi", "grok", "agy", "cursor", "zzz", ""} {
		for _, transport := range []string{"", "herdr", "hc", "HC", "unknown"} {
			for _, selector := range []string{"", "legacy", "v2", invalid} {
				if kind == "opencode" && transport != "hc" && selector != "" && selector != "legacy" {
					continue
				}
				t.Run(kind+"/"+transport+"/"+selector, func(t *testing.T) {
					req := LaunchRequest{Kind: kind, Model: "p/m", Effort: "high", Transport: transport, TUIProfile: selector, InlineConfig: invalid, ConfigDir: invalid}
					var warns, wantWarns []string
					got, err := ResolveLaunch(req, func(s string) { warns = append(warns, s) })
					var want LaunchProfile
					var wantErr error
					if kind == "opencode" && transport == "hc" {
						wantErr = errInvalidInlineCfg
					} else {
						want.ModelArgs = KindModelArgs(kind, "p/m", "high", func(s string) { wantWarns = append(wantWarns, s) })
					}
					if err != wantErr || !reflect.DeepEqual(got, want) || !reflect.DeepEqual(warns, wantWarns) {
						t.Fatalf("compatibility: %#v %v %v; want %#v %v %v", got, err, warns, want, wantErr, wantWarns)
					}
				})
			}
		}
	}
	// HC ignores even hostile selectors, including on valid input.
	for _, selector := range []string{"", "legacy", "v2", invalid} {
		got, err := ResolveLaunch(LaunchRequest{Kind: "opencode", Model: "p/m", Transport: "hc", TUIProfile: selector}, nil)
		want := LaunchProfile{Name: "opencode-primary-v2", ExtraArgs: []string{"--standalone"}, EnvOverrides: map[string]string{"OPENCODE_DISABLE_AUTOUPDATE": "1", "OPENCODE_CONFIG_CONTENT": `{"agents":{"build":{"model":"p/m"}},"default_agent":"build","model":"p/m"}`}}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("HC selector isolation: %#v %v", got, err)
		}
	}
	for _, transport := range []string{"", "herdr", "hc", "HC", "unknown"} {
		for _, dir := range []string{"", invalid} {
			req := LaunchRequest{Kind: "opencode", Model: "p/m", Transport: transport, ConfigDir: dir}
			got, err := ResolveLaunch(req, nil)
			old, oldErr := ResolveLaunchProfileWithConfigDir(req.Kind, req.Model, req.Effort, req.Transport, req.InlineConfig, dir, nil)
			if err != oldErr || !reflect.DeepEqual(got, old) {
				t.Fatal("config-dir wrapper diverged")
			}
			if dir == "" {
				old, oldErr = ResolveLaunchProfile(req.Kind, req.Model, req.Effort, req.Transport, req.InlineConfig, nil)
				if err != oldErr || !reflect.DeepEqual(got, old) {
					t.Fatal("old wrapper diverged")
				}
			}
		}
	}
	// Literal historical warning and argv remain stable, even with irrelevant invalid inputs.
	var warns []string
	got, err := ResolveLaunch(LaunchRequest{Kind: "zzz", Model: "p/m", TUIProfile: invalid, InlineConfig: invalid, ConfigDir: invalid}, func(s string) { warns = append(warns, s) })
	if err != nil || !reflect.DeepEqual(got, LaunchProfile{}) || !reflect.DeepEqual(warns, []string{"no model mapping for kind 'zzz'; model ignored"}) {
		t.Fatalf("warning contract: %#v %v %v", got, err, warns)
	}
	got, err = ResolveLaunch(LaunchRequest{Kind: "opencode", Model: invalid, Transport: "unknown", TUIProfile: "legacy", InlineConfig: invalid, ConfigDir: invalid}, nil)
	if err != nil || !reflect.DeepEqual(got, LaunchProfile{ModelArgs: []string{"-m", invalid}}) {
		t.Fatalf("legacy invalid model passthrough: %#v %v", got, err)
	}
}
