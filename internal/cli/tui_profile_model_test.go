package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func tuiModelDirect(t *testing.T, env platform.Env, args ...string) (int, string, string) {
	t.Helper()
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, stderr bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &stderr
	defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
	code := cmdModel(args, env)
	return code, out.String(), stderr.String()
}

func assertTUIModel(t *testing.T, code int, out, stderr string, primary bool) {
	t.Helper()
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out, stderr)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"kind": "opencode", "spec": "provider/model", "effort": "", "model": "provider/model", "family": "unknown", "effort_ceiling": "", "agent_args": "-m provider/model"}
	if primary {
		want["agent_args"] = "--standalone"
		want["launch_profile"] = "opencode-primary-v2"
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("public metadata got=%v want=%v", got, want)
	}
}

func TestOpenCodeTUIModelDirect(t *testing.T) {
	for _, transport := range []string{"", "herdr", "hc"} {
		for _, selector := range []string{"", "legacy", "v2", "private-canary", "V2", " v2", "v2\n", "\x00", string([]byte{0xff})} {
			t.Run(transport+"/"+selector, func(t *testing.T) {
				env := platform.Env{"SOHO_TRANSPORT": transport, "SOHO_OPENCODE_TUI_PROFILE": selector, "OPENCODE_CONFIG_CONTENT": `{"private":"inline-canary"}`, "OPENCODE_CONFIG_DIR": "/synthetic/private-directory-canary"}
				before := env.Clone()
				code, out, stderr := tuiModelDirect(t, env, "opencode", "provider/model")
				if transport != "hc" && selector != "" && selector != "legacy" && selector != "v2" {
					if code != 2 || out != "" || stderr != "soho: launch profile: OpenCode TUI profile is invalid\n" {
						t.Fatalf("code=%d stdout=%q stderr=%q", code, out, stderr)
					}
				} else {
					assertTUIModel(t, code, out, stderr, transport == "hc" || selector == "v2")
				}
				if !reflect.DeepEqual(env, before) {
					t.Fatal("caller environment mutated")
				}
			})
		}
	}
}

func TestOpenCodeTUIModelRefusals(t *testing.T) {
	t.Run("malformed-model", func(t *testing.T) {
		env, cwd := commandFixture(t)
		env["SOHO_OPENCODE_TUI_PROFILE"] = "v2"
		for _, spec := range []string{"private-model-canary", "/private-model-canary", "provider/\x00private-canary"} {
			code, out, stderr := runIn(t, []string{"model", "opencode", spec}, env, cwd)
			if code != 2 || out != "" || stderr != "soho: launch profile: model reference is malformed\n" {
				t.Fatalf("model refusal: %d %q %q", code, out, stderr)
			}
		}
		// The shared grammar splits only the first slash; preserve the model remainder.
		code, out, stderr := runIn(t, []string{"model", "opencode", "provider/model/extra"}, env, cwd)
		var value map[string]any
		if err := json.Unmarshal([]byte(out), &value); err != nil {
			t.Fatal(err)
		}
		if code != 0 || stderr != "" || value["model"] != "provider/model/extra" || value["spec"] != "provider/model/extra" || value["agent_args"] != "--standalone" || value["launch_profile"] != "opencode-primary-v2" || len(value) != 8 {
			t.Fatalf("slash control: %d %q %q", code, out, stderr)
		}
	})
	for _, tc := range []struct{ key, value, message string }{
		{"OPENCODE_CONFIG_CONTENT", "[]", "inline configuration is not a single non-null JSON object"},
		{"OPENCODE_CONFIG_CONTENT", `{"agents":"private-canary"}`, "agents or agents.build is not a JSON object"},
		{"OPENCODE_CONFIG_DIR", "private-canary\x00", "configuration directory is malformed"},
		{"OPENCODE_CONFIG_DIR", string([]byte{0xff}), "configuration directory is malformed"},
	} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			env, cwd := commandFixture(t)
			env["SOHO_OPENCODE_TUI_PROFILE"] = "v2"
			env[tc.key] = tc.value
			for _, direct := range []bool{false, true} {
				var code int
				var out, stderr string
				if direct {
					code, out, stderr = tuiModelDirect(t, env, "opencode", "provider/model")
				} else {
					code, out, stderr = runIn(t, []string{"model", "opencode", "provider/model"}, env, cwd)
				}
				if code != 2 || out != "" || stderr != "soho: launch profile: "+tc.message+"\n" {
					t.Fatalf("direct=%v code=%d stdout=%q stderr=%q", direct, code, out, stderr)
				}
			}
		})
	}
}

func TestOpenCodeTUIModelRoutingAndGet(t *testing.T) {
	env, cwd := commandFixture(t)
	user := platform.UserConfigPath(platform.Current(), env)
	project := filepath.Join(cwd, ".agents", "soho.conf")
	session := filepath.Join(env.Get("SOHO_DIR"), "ws", "session.conf")
	for _, step := range []struct{ file, body, selector string }{
		{"", "", "legacy"},
		{user, "opencode_tui_profile=v2\n", "v2"},
		{project, "opencode_tui_profile=legacy\n", "legacy"},
		{session, "opencode_tui_profile=v2\n", "v2"},
	} {
		if step.file != "" {
			writeConfigFile(t, step.file, step.body)
		}
		before := inventoryCLITree(t, filepath.Dir(cwd))
		code, out, stderr := runIn(t, []string{"model", "opencode", "provider/model"}, env, cwd)
		assertTUIModel(t, code, out, stderr, step.selector == "v2")
		code, out, stderr = runIn(t, []string{"config", "get", "opencode_tui_profile"}, env, cwd)
		if code != 0 || out != step.selector+"\n" || stderr != "" {
			t.Fatalf("get: %d %q %q", code, out, stderr)
		}
		if after := inventoryCLITree(t, filepath.Dir(cwd)); !reflect.DeepEqual(before, after) {
			t.Fatal("metadata/get changed fixture tree")
		}
	}
	for _, selector := range []string{"legacy", "v2", "private-canary"} {
		env["SOHO_OPENCODE_TUI_PROFILE"] = selector
		code, out, stderr := runIn(t, []string{"model", "opencode", "provider/model"}, env, cwd)
		if selector == "private-canary" {
			if code != 2 || out != "" || stderr != "soho: launch profile: OpenCode TUI profile is invalid\n" {
				t.Fatalf("invalid: %d %q %q", code, out, stderr)
			}
		} else {
			assertTUIModel(t, code, out, stderr, selector == "v2")
		}
		code, out, stderr = runIn(t, []string{"config", "get", "opencode_tui_profile"}, env, cwd)
		if selector == "private-canary" {
			if code != 2 || out != "" || stderr != "soho: config get: launch profile: OpenCode TUI profile is invalid\n" {
				t.Fatalf("invalid get: %d %q %q", code, out, stderr)
			}
		} else if code != 0 || out != selector+"\n" || stderr != "" {
			t.Fatalf("get: %d %q %q", code, out, stderr)
		}
	}
	delete(env, "SOHO_OPENCODE_TUI_PROFILE")
	writeConfigFile(t, session, "opencode_tui_profile=private-file-canary\n")
	before := inventoryCLITree(t, filepath.Dir(cwd))
	code, out, stderr := runIn(t, []string{"model", "opencode", "provider/model"}, env, cwd)
	if code != 2 || out != "" || stderr != "soho: launch profile: OpenCode TUI profile is invalid\n" {
		t.Fatalf("file refusal: %d %q %q", code, out, stderr)
	}
	code, out, stderr = runIn(t, []string{"config", "get", "opencode_tui_profile"}, env, cwd)
	if code != 2 || out != "" || stderr != "soho: config get: launch profile: OpenCode TUI profile is invalid\n" {
		t.Fatalf("file get refusal: %d %q %q", code, out, stderr)
	}
	if after := inventoryCLITree(t, filepath.Dir(cwd)); !reflect.DeepEqual(before, after) {
		t.Fatal("refusals changed fixture tree")
	}
}

func TestOpenCodeTUIModelUsageAndOtherKind(t *testing.T) {
	env, cwd := commandFixture(t)
	env["SOHO_OPENCODE_TUI_PROFILE"] = "private-canary"
	for _, tc := range []struct {
		args    []string
		message string
	}{
		{[]string{"model"}, "soho: 1: kind\n"},
		{[]string{"model", "opencode"}, "soho: 2: spec\n"},
		{[]string{"model", "opencode", ""}, "soho: 2: spec\n"},
	} {
		code, out, stderr := runIn(t, tc.args, env, cwd)
		if code != 1 || out != "" || stderr != tc.message {
			t.Fatalf("usage: %d %q %q", code, out, stderr)
		}
	}
	code, out, stderr := runIn(t, []string{"model", "claude", "literal-model"}, env, cwd)
	delete(env, "SOHO_OPENCODE_TUI_PROFILE")
	wantCode, wantOut, wantErr := runIn(t, []string{"model", "claude", "literal-model"}, env, cwd)
	if code != wantCode || out != wantOut || stderr != wantErr || code != 0 {
		t.Fatalf("other kind changed: %d %q %q", code, out, stderr)
	}
}

func TestOpenCodeTUIModelDiscovery(t *testing.T) {
	for _, mode := range []string{"herdr", "hc"} {
		t.Run(mode, func(t *testing.T) {
			env, cwd := commandFixture(t)
			bin := t.TempDir()
			for _, name := range []string{"git", "herdr", "hc", "opencode", "codex", "claude"} {
				var rules []fakecli.Rule
				if name == "git" {
					rules = []fakecli.Rule{{AnyArgs: true, Code: 1}}
				}
				if name == "herdr" {
					rules = []fakecli.Rule{{Argv: []string{"pane", "current", "--current"}, Stdout: `{"result":{"pane":{"workspace_id":"fixture-session"}}}`}}
				}
				if name == "hc" {
					rules = []fakecli.Rule{{Argv: []string{"get", "fixture-terminal", "--json"}, Stdout: `{"address":"fixture-terminal","labels":{"soho.session":"fixture-session"}}`}}
				}
				if _, err := fakecli.Install(t, bin, name, rules); err != nil {
					t.Fatal(err)
				}
			}
			env["PATH"] = bin
			env["SOHO_FAKECLI_CONFIG"] = bin
			env["SOHO_TRANSPORT"] = mode
			delete(env, "HERDR_WORKSPACE_ID")
			if mode == "hc" {
				env["HC_TERMINAL_ID"] = "fixture-terminal"
			} else {
				env["HERDR_ENV"] = "1"
			}
			writeConfigFile(t, filepath.Join(env.Get("SOHO_DIR"), "fixture-session", "session.conf"), "opencode_tui_profile=v2\n")
			// HC must bypass a hostile selector yet discover the actual fixture session.
			if mode == "hc" {
				env["SOHO_OPENCODE_TUI_PROFILE"] = "private-canary"
			}
			root := filepath.Dir(cwd)
			before := inventoryCLITree(t, root)
			original := env.Clone()
			processSelector, hadProcessSelector := os.LookupEnv("SOHO_OPENCODE_TUI_PROFILE")
			code, out, stderr := runIn(t, []string{"model", "opencode", "provider/model"}, env, cwd)
			assertTUIModel(t, code, out, stderr, true)
			if !reflect.DeepEqual(original, env) {
				t.Fatal("routing changed caller map")
			}
			if value, present := os.LookupEnv("SOHO_OPENCODE_TUI_PROFILE"); value != processSelector || present != hadProcessSelector {
				t.Fatal("routing changed process selector")
			}
			// Repeated Caller() calls use the same backend session cache: one actual lookup.
			calls := inventoryCLICalls(t, bin, mode)
			want := []string{"pane", "current", "--current"}
			if mode == "hc" {
				want = []string{"get", "fixture-terminal", "--json"}
			}
			if len(calls) != 1 || !reflect.DeepEqual(calls[0].Argv, want) {
				t.Fatalf("session calls=%v want one %v", calls, want)
			}
			for _, name := range []string{"opencode", "codex", "claude"} {
				if calls := inventoryCLICalls(t, bin, name); len(calls) != 0 {
					t.Fatalf("unexpected discovery %s: %v", name, calls)
				}
			}
			other := "hc"
			if mode == "hc" {
				other = "herdr"
			}
			if calls := inventoryCLICalls(t, bin, other); len(calls) != 0 {
				t.Fatalf("foreign transport calls=%v", calls)
			}
			gitCalls := inventoryCLICalls(t, bin, "git")
			wantGit := [][]string{{"rev-parse", "--show-toplevel"}, {"rev-parse", "--git-dir"}, {"rev-parse", "--git-common-dir"}}
			var gotGit [][]string
			for _, call := range gitCalls {
				gotGit = append(gotGit, call.Argv)
			}
			if !reflect.DeepEqual(gotGit, wantGit) {
				t.Fatalf("Git discovery calls=%v want=%v", gotGit, wantGit)
			}
			if after := inventoryCLITree(t, root); !reflect.DeepEqual(before, after) {
				t.Fatal("model changed configuration/state/cache tree")
			}
			delete(env, "SOHO_OPENCODE_TUI_PROFILE")
			code, out, stderr = runIn(t, []string{"config", "get", "opencode_tui_profile"}, env, cwd)
			if code != 0 || out != "v2\n" || stderr != "" {
				t.Fatalf("get discovery: %d %q %q", code, out, stderr)
			}
			if calls := inventoryCLICalls(t, bin, mode); len(calls) != 2 {
				t.Fatalf("get must add one session discovery, got=%v", calls)
			}
			if calls := inventoryCLICalls(t, bin, "git"); len(calls) != 3 {
				t.Fatalf("get added uncached git discovery: %v", calls)
			}
			for _, name := range []string{"opencode", "codex", "claude"} {
				if calls := inventoryCLICalls(t, bin, name); len(calls) != 0 {
					t.Fatalf("get invoked client discovery: %s %v", name, calls)
				}
			}
			if after := inventoryCLITree(t, root); !reflect.DeepEqual(before, after) {
				t.Fatal("get changed fixture tree")
			}
		})
	}
}
