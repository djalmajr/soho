package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

func TestConfigTrustCwd(t *testing.T) {
	writePolicy := func(t *testing.T, path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	defaults, err := os.ReadFile(filepath.Join("..", "..", "skills", "soho", "config.defaults"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(defaults), "\ntrust_cwd=on\n") {
		t.Fatal("distributable default missing")
	}
	root := t.TempDir()
	cwd := filepath.Join(root, "repo")
	env := platform.Env{"SOHO_SKILL_DIR": filepath.Join(root, "skill"), "SOHO_DIR": filepath.Join(root, "state"), "HERDR_WORKSPACE_ID": "ws"}
	for _, k := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "TMPDIR", "PATH"} {
		env[k] = filepath.Join(root, k)
		if err := os.MkdirAll(env[k], 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv(k, env[k])
	}
	if err := os.MkdirAll(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	writePolicy(t, filepath.Join(env["SOHO_SKILL_DIR"], "config.defaults"), "trust_cwd=on\n")
	ctx := LoadConfig(env, cwd)
	if TrustCwdMode(&ctx, env) != "on" {
		t.Fatal("default must be on")
	}
	for _, tc := range []struct{ layer, value string }{{"user", "off"}, {"project", "on"}, {"session", "off"}} {
		path := ConfigFileFor(tc.layer, env, cwd)
		if tc.layer == "session" {
			path = SessionConfPath(&ctx, env, cwd)
		}
		writePolicy(t, path, "trust_cwd="+tc.value+"\n")
		ctx = LoadConfig(env, cwd)
		if got := TrustCwdMode(&ctx, env); got != tc.value || CfgSource(&ctx, "trust_cwd", env) != tc.layer {
			t.Fatalf("layer=%s value=%s source=%s", tc.layer, got, CfgSource(&ctx, "trust_cwd", env))
		}
	}
	env["SOHO_TRUST_CWD"] = "on"
	if TrustCwdMode(&ctx, env) != "on" || CfgSource(&ctx, "trust_cwd", env) != "env" {
		t.Fatal("env must win")
	}
	env["SOHO_TRUST_CWD"] = "invalid"
	if TrustCwdMode(&ctx, env) != "off" || ConfigValueOk("trust_cwd", "invalid", env, cwd) {
		t.Fatal("invalid must fail closed")
	}
	if !ConfigKeyOk("trust_cwd") || !ConfigValueOk("trust_cwd", "on", env, cwd) || !ConfigValueOk("trust_cwd", "off", env, cwd) {
		t.Fatal("missing scalar validation")
	}
	delete(env, "SOHO_TRUST_CWD")
	ctx.Entries["trust_cwd"] = ConfigEntry{Value: "invalid", Source: "session"}
	if TrustCwdMode(&ctx, env) != "off" {
		t.Fatal("invalid file must fail closed")
	}
}
