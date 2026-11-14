package core

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/launchprofile"
	"github.com/djalmajr/soho/internal/platform"
)

func TestOpenCodeTUIRead(t *testing.T) {
	for _, ctx := range []*Config{nil, {}, {Entries: map[string]ConfigEntry{"opencode_tui_profile": {Value: ""}}}} {
		got, err := OpenCodeTUIProfile(ctx, nil)
		if got != "legacy" || err != nil {
			t.Fatalf("default: %q %v", got, err)
		}
	}
	for _, raw := range []string{"legacy", "v2", "", "V2", " v2", "v2\n", "\x00", string([]byte{0xff}), "private-canary"} {
		for _, fromEnv := range []bool{false, true} {
			ctx := &Config{Entries: map[string]ConfigEntry{"opencode_tui_profile": {Value: raw, Source: "session"}}}
			env := platform.Env{}
			if fromEnv {
				env["SOHO_OPENCODE_TUI_PROFILE"] = raw
				ctx = nil
			}
			before := env.Clone()
			got, err := OpenCodeTUIProfile(ctx, env)
			want := raw
			if raw == "" {
				want = "legacy"
			}
			if raw == "" || raw == "legacy" || raw == "v2" {
				if got != want || err != nil {
					t.Fatalf("read %q: %q %v", raw, got, err)
				}
			} else if got != "" || err != launchprofile.ErrInvalidTUIProfile {
				t.Fatalf("refusal %q: %q %v", raw, got, err)
			}
			if !reflect.DeepEqual(env, before) {
				t.Fatal("environment mutated")
			}
		}
	}
}

func TestOpenCodeTUILayerPrecedence(t *testing.T) {
	root := t.TempDir()
	ctx := Config{Entries: map[string]ConfigEntry{}}
	for i, source := range []string{"defaults", "user", "project", "session"} {
		value := []string{"v2", "legacy", "v2", "legacy"}[i]
		file := filepath.Join(root, source+".conf")
		if err := os.WriteFile(file, []byte("opencode_tui_profile="+value+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		loadConfigFile(file, source, &ctx)
		before, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		got, err := OpenCodeTUIProfile(&ctx, nil)
		if got != value || err != nil || CfgSource(&ctx, "opencode_tui_profile", nil) != source {
			t.Fatalf("%s: %q %v", source, got, err)
		}
		after, err := os.ReadFile(file)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("reader changed file")
		}
	}
	for _, raw := range []string{"v2", "bad-canary", ""} {
		env := platform.Env{"SOHO_OPENCODE_TUI_PROFILE": raw}
		got, err := OpenCodeTUIProfile(&ctx, env)
		if raw == "bad-canary" {
			if got != "" || err != launchprofile.ErrInvalidTUIProfile {
				t.Fatalf("env refusal: %q %v", got, err)
			}
		} else {
			want := raw
			if raw == "" {
				want = "legacy"
			}
			if got != want || err != nil {
				t.Fatalf("env precedence: %q %v", got, err)
			}
		}
	}
}

func TestOpenCodeTUISetRefusesBeforeEffects(t *testing.T) {
	for _, command := range []string{"config set", "session set"} {
		for _, raw := range []string{"V2", "Legacy", " v2", "v2 ", "\x00", "v2\nprivate-canary", "v2\t", "v2#canary", "\x1b", string([]byte{0xff}), "private-canary"} {
			t.Run(command+"/"+raw, func(t *testing.T) {
				f := newHcWorkspaceFixture(t, true, "fixture-session")
				root := t.TempDir()
				// An explicitly supplied fixture skill avoids materializing the bundled skill.
				f.env["SOHO_SKILL_DIR"] = root
				f.env["HOME"], f.env["USERPROFILE"], f.env["XDG_CONFIG_HOME"], f.env["TMPDIR"] = root, root, root, root
				ctx := &Config{Entries: map[string]ConfigEntry{}}
				oldWrite := writeConfigAtomic
				writes := 0
				writeConfigAtomic = func(string, string) error { writes++; return nil }
				t.Cleanup(func() { writeConfigAtomic = oldWrite })
				code, msg := dieIn(t, func() {
					if command == "config set" {
						CmdConfigSet([]string{"opencode_tui_profile", raw}, ctx, f.env, root)
					} else {
						CmdSessionSet([]string{"opencode_tui_profile", raw}, ctx, f.env, root)
					}
				})
				if code != 2 || msg != command+": launch profile: OpenCode TUI profile is invalid" {
					t.Fatalf("exit %d %q", code, msg)
				}
				if writes != 0 || len(f.calls(t)) != 0 {
					t.Fatalf("effects: writes=%d hc=%v", writes, f.calls(t))
				}
				entries, err := os.ReadDir(root)
				if err != nil || len(entries) != 0 {
					t.Fatalf("destination effects: %v %v", entries, err)
				}
				if _, err := os.Stat(f.env["SOHO_DIR"]); !os.IsNotExist(err) {
					t.Fatalf("state created: %v", err)
				}
			})
		}
	}
}

func TestOpenCodeTUISetWrites(t *testing.T) {
	for _, command := range []string{"project", "user", "session"} {
		t.Run(command, func(t *testing.T) {
			f := newHcWorkspaceFixture(t, true, "fixture-session")
			root := t.TempDir()
			f.env["HOME"], f.env["USERPROFILE"], f.env["XDG_CONFIG_HOME"], f.env["TMPDIR"] = root, root, root, root
			f.env["SOHO_SKILL_DIR"] = t.TempDir()
			ctx := &Config{Entries: map[string]ConfigEntry{}}
			var out bytes.Buffer
			oldOut := platform.Stdout
			platform.Stdout = &out
			t.Cleanup(func() { platform.Stdout = oldOut })
			dest := filepath.Join(root, ".agents", "soho.conf")
			if command == "user" {
				dest = filepath.Join(root, "soho", "config")
			}
			if command == "session" {
				dest = filepath.Join(f.env["SOHO_DIR"], "fixture-session", "session.conf")
			}
			if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(dest, []byte("# keep\ntransport=hc\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			for _, value := range []string{"v2", "legacy"} {
				out.Reset()
				if command == "session" {
					CmdSessionSet([]string{"opencode_tui_profile=" + value}, ctx, f.env, root)
				} else {
					CmdConfigSet([]string{"--" + command, "opencode_tui_profile", value}, ctx, f.env, root)
				}
				data, err := os.ReadFile(dest)
				if err != nil || string(data) != "# keep\ntransport=hc\nopencode_tui_profile="+value+"\n" {
					t.Fatalf("write: %q %v", data, err)
				}
				want := "set opencode_tui_profile=" + value + " in " + dest
				if command == "session" {
					want += " (session: this Herdr workspace only; above project and user, below flags and SOHO_*)"
				}
				if out.String() != want+"\n" {
					t.Fatalf("output: %q want %q", out.String(), want+"\n")
				}
			}
			if command != "session" && len(f.calls(t)) != 0 {
				t.Fatal("config setter consulted session")
			}
			code, msg := dieIn(t, func() {
				if command == "session" {
					CmdSessionSet([]string{"opencode_tui_profile", ""}, ctx, f.env, root)
				} else {
					CmdConfigSet([]string{"opencode_tui_profile", ""}, ctx, f.env, root)
				}
			})
			prefix := "config set"
			if command == "session" {
				prefix = "session set"
			}
			if code != 2 || msg != prefix+": empty value" {
				t.Fatalf("empty: %d %q", code, msg)
			}
		})
	}
}

func TestOpenCodeTUIKeyAndListing(t *testing.T) {
	if !ConfigKeyOk("opencode_tui_profile") || ConfigKeyOk("OPENCODE_TUI_PROFILE") {
		t.Fatal("key acceptance changed")
	}
	for _, key := range ConfigScalarKeys {
		if key == "opencode_tui_profile" {
			t.Fatal("frozen listing extended")
		}
	}
	for _, key := range []string{"transport", "hooks_review"} {
		code, msg := dieIn(t, func() { CmdConfigSet([]string{key, "bad-canary"}, nil, nil, "") })
		if code != 2 || msg != "config set: invalid value 'bad-canary' for "+key {
			t.Fatalf("old diagnostic: %d %q", code, msg)
		}
	}
	if !TransportValueOk("hc") || !TransportValueOk("herdr") || TransportValueOk("v2") || strings.Contains(strings.Join(ConfigScalarKeys, ","), "opencode_tui_profile") {
		t.Fatal("transport/listing contract changed")
	}
}
