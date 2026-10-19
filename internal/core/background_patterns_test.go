package core

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

// shippedBackgroundPatternClaude is the default the skill ships for claude
// (DJA-405): a still screen counting a background shell or Monitor.
const shippedBackgroundPatternClaude = `\d+ (shells?|monitors?) still running|· \d+ (shells?|monitors?)( ·|$)`

func TestBackgroundPatternShippedDefault(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "skills", "soho", "config.defaults"))
	if err != nil {
		t.Fatal(err)
	}
	var values []string
	for _, line := range strings.Split(string(data), "\n") {
		if value, ok := strings.CutPrefix(line, "background_patterns.claude="); ok {
			values = append(values, value)
		}
		if strings.HasPrefix(line, "background_patterns.") && !strings.HasPrefix(line, "background_patterns.claude=") {
			t.Fatalf("a default for another kind: %q (only claude ships one)", line)
		}
	}
	if len(values) != 1 || values[0] != shippedBackgroundPatternClaude {
		t.Fatalf("shipped background_patterns.claude = %q, want exactly [%q]", values, shippedBackgroundPatternClaude)
	}
	re, err := regexp.Compile(values[0])
	if err != nil {
		t.Fatalf("the shipped default does not compile: %v", err)
	}
	for _, line := range []string{
		"✻ Cooked for 7m 37s · done 2:47 PM · 1 shell still running",
		"✻ Cogitated for 40s · done 11:47 AM · 2 monitors still running",
		"⏵⏵ bypass permissions on · PR #121 · 1 shell",
		"⏵⏵ bypass permissions on · 2 monitors · ← 1 agent",
	} {
		if !re.MatchString(line) {
			t.Errorf("default does not match %q", line)
		}
	}
	for _, line := range []string{
		"✻ Cooked for 7m 37s · done 2:47 PM",
		"⏵⏵ bypass permissions on · PR #121",
		"I ran 2 monitors and 1 shell in the test",
		"⏵⏵ bypass permissions on · 1 shellfish",
	} {
		if re.MatchString(line) {
			t.Errorf("default matches %q", line)
		}
	}
}

func TestBackgroundPatternKeyAndEnv(t *testing.T) {
	env, cwd := fixture(t)
	write(t, filepath.Join(cwd, ".agents", "soho.conf"), "background_patterns.claude=shell\n")
	ctx := LoadConfig(env, cwd)
	if got := Cfg(&ctx, BackgroundPatternKey("claude"), "", env); got != "shell" {
		t.Fatalf("cfg=%q, want shell (file layer)", got)
	}
	env["SOHO_BACKGROUND_PATTERNS_CLAUDE"] = "monitor"
	if got := Cfg(&ctx, BackgroundPatternKey("claude"), "", env); got != "monitor" {
		t.Fatalf("cfg=%q, want monitor (env wins)", got)
	}
	entries := BackgroundPatternEntries(&ctx, env)
	if len(entries) != 1 || entries["claude"].Value != "monitor" || entries["claude"].Source != "env" {
		t.Fatalf("entries=%#v", entries)
	}
	// An empty value drops the kind.
	empty := &Config{Entries: map[string]ConfigEntry{"background_patterns_codex": {Value: ""}}}
	if got := BackgroundPatternEntries(empty, platform.Env{}); len(got) != 0 {
		t.Fatalf("empty value in entries: %#v", got)
	}
}

func TestBackgroundPatternConfigKeyOkAndDottedName(t *testing.T) {
	for _, key := range []string{"background_patterns.claude", "background_patterns.codex"} {
		if !ConfigKeyOk(key) {
			t.Fatalf("ConfigKeyOk(%q)=false, want true", key)
		}
	}
	for _, key := range []string{"background_patterns", "background_patterns.", "background_pattern.claude", "Background_patterns.claude"} {
		if ConfigKeyOk(key) {
			t.Fatalf("ConfigKeyOk(%q)=true, want false", key)
		}
	}
	ctx := &Config{Entries: map[string]ConfigEntry{}}
	if got := DottedKeyName("background_patterns_claude", ctx, platform.Env{}, ""); got != "background_patterns.claude" {
		t.Fatalf("DottedKeyName=%q, want background_patterns.claude", got)
	}
}

func TestConfigListsBackgroundPatternsLikeTheOtherDottedKeys(t *testing.T) {
	env, cwd := fixture(t)
	write(t, filepath.Join(cwd, ".agents", "soho.conf"), "effort.grok=xhigh\nbackground_patterns.claude="+shippedBackgroundPatternClaude+"\n")
	ctx := LoadConfig(env, cwd)
	out, _ := capture(t, func() { CmdConfig(&ctx, env, cwd) })
	wantLine := "background_patterns.claude " + shippedBackgroundPatternClaude + " project"
	lines := strings.Split(out, "\n")
	at, effort := -1, -1
	for i, line := range lines {
		if line == wantLine {
			at = i
		}
		if strings.HasPrefix(line, "effort.grok") {
			effort = i
		}
	}
	if at < 0 {
		t.Fatalf("config lacks the line %q:\n%s", wantLine, out)
	}
	if effort < 0 || at > effort {
		t.Fatalf("background_patterns.claude (line %d) must list before effort.grok (line %d): dotted keys are sorted", at, effort)
	}
	// A key that only exists in the environment is listed under its rebuilt
	// dotted name, as effort.* and context_window.* do.
	env["SOHO_BACKGROUND_PATTERNS_CODEX"] = "busy"
	out, _ = capture(t, func() { CmdConfig(&ctx, env, cwd) })
	found := false
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "background_patterns.codex") && strings.Contains(line, "busy") && strings.HasSuffix(line, "env") {
			found = true
		}
	}
	if !found {
		t.Fatalf("env-only key not listed under its dotted name:\n%s", out)
	}
}
