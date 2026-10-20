package cli

import (
	"bytes"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/kinds"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func commandFakeEnv(t *testing.T, rules map[string][]fakecli.Rule) platform.Env {
	t.Helper()
	bin := t.TempDir()
	for name, items := range rules {
		if _, err := fakecli.Install(t, bin, name, items); err != nil {
			t.Fatal(err)
		}
	}
	env := platform.Env{"PATH": bin, "HOME": t.TempDir(), "USERPROFILE": t.TempDir(), "TMPDIR": t.TempDir(), "SOHO_FAKECLI_CONFIG": bin, "SOHO_SKILL_DIR": t.TempDir()}
	env["GORACE"] = fakecli.RaceOptions(env.List())
	return env
}
func TestKindsCommands(t *testing.T) {
	t.Run("kinds table uses installed fake executables", func(t *testing.T) { // JS: "kinds CLI: table with fake CLIs on PATH (installed yes/no)"
		env := commandFakeEnv(t, map[string][]fakecli.Rule{"cursor-agent": nil, "grok": nil})
		old := platform.Stdout
		var out bytes.Buffer
		platform.Stdout = &out
		t.Cleanup(func() { platform.Stdout = old })
		kinds.CmdKinds(env, platform.Current())
		if !strings.Contains(out.String(), "KIND     EXECUTABLE    FAMILY     EFFORT   INSTALLED") || !strings.Contains(out.String(), "cursor   cursor-agent  by model   xhigh    yes") {
			t.Fatal(out.String())
		}
	})
	t.Run("models listing sorts CLI ids newest first", func(t *testing.T) { // JS: "cmd_models lists ids newest first"
		env := commandFakeEnv(t, map[string][]fakecli.Rule{"grok": {{Argv: []string{"models"}, Stdout: "grok-4.6 - old\ngrok-4.7 - new\n"}}})
		oldOut, oldErr := platform.Stdout, platform.Stderr
		var out, stderr bytes.Buffer
		platform.Stdout = &out
		platform.Stderr = &stderr
		t.Cleanup(func() { platform.Stdout = oldOut; platform.Stderr = oldErr })
		if code := cmdModels([]string{"grok"}, env); code != 0 || out.String() != "grok-4.7\ngrok-4.6\n" || stderr.Len() != 0 {
			t.Fatalf("code=%d out=%q err=%q", code, out.String(), stderr.String())
		}
	})
	t.Run("model command emits model family ceiling and translated args", func(t *testing.T) { // JS: "cmd_model JSON shape and args"
		env := commandFakeEnv(t, nil)
		oldOut, oldErr := platform.Stdout, platform.Stderr
		var out, stderr bytes.Buffer
		platform.Stdout = &out
		platform.Stderr = &stderr
		t.Cleanup(func() { platform.Stdout = oldOut; platform.Stderr = oldErr })
		if code := cmdModel([]string{"pi", "openai/gpt-5", "high"}, env); code != 0 {
			t.Fatalf("code %d", code)
		}
		for _, expected := range []string{`"model": "openai/gpt-5"`, `"family": "openai"`, `"effort_ceiling": "max"`, `"agent_args": "--model openai/gpt-5 --thinking high"`} {
			if !strings.Contains(out.String(), expected) {
				t.Fatalf("missing %s in %s", expected, out.String())
			}
		}
	})
	t.Run("model usage errors return the original status and stderr", func(t *testing.T) { // JS: "model usage errors (missing kind/spec) are rc 1"
		oldErr := platform.Stderr
		var stderr bytes.Buffer
		platform.Stderr = &stderr
		t.Cleanup(func() { platform.Stderr = oldErr })
		if code := cmdModel(nil, nil); code != 1 || stderr.String() != "soho: 1: kind\n" {
			t.Fatalf("code=%d stderr=%q", code, stderr.String())
		}
	})
	t.Run("model with a kind but no spec exits 1 with an empty stdout and the spec diagnostic", func(t *testing.T) {
		env, cwd := commandFixture(t)
		code, out, stderr := runIn(t, []string{"model", "grok"}, env, cwd)
		if code != 1 || out != "" || stderr != "soho: 2: spec\n" {
			t.Fatalf("code=%d stdout=%q stderr=%q; want 1, empty stdout and the spec diagnostic", code, out, stderr)
		}
	})
	t.Run("env reports versions installed kinds runtime and effective config", func(t *testing.T) { // JS: "env: lines in the bash order (version, herdr, os, runtime, kinds, config)"
		env := commandFakeEnv(t, map[string][]fakecli.Rule{"git": {{Argv: []string{"-C", filepath.Join(os.TempDir(), "skill"), "log", "-1", "--format=%h %cs"}, Stdout: "abc1234 2026-09-24\n"}}, "herdr": {{Argv: []string{"--version"}, Stdout: "herdr 1.0\n"}}, "grok": {{Argv: []string{"--version"}, Stdout: "Grok 4\nbuild\n"}}})
		// Control SkillDir separately for the git argv contract.
		skill := filepath.Join(os.TempDir(), "skill")
		env["SOHO_SKILL_DIR"] = skill
		oldOut := platform.Stdout
		var out bytes.Buffer
		platform.Stdout = &out
		t.Cleanup(func() { platform.Stdout = oldOut })
		cmdEnv(&core.Config{Entries: map[string]core.ConfigEntry{}}, env, t.TempDir())
		for _, line := range []string{"soho: abc1234 2026-09-24", "herdr: herdr 1.0", "kind grok: Grok 4", "runtime: go ", "config: layout=split reuse_workers=on approvals=auto"} {
			if !strings.Contains(out.String(), line) {
				t.Fatalf("missing %q in %s", line, out.String())
			}
		}
	})
}
