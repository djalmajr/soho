package spawn

import (
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

const codexUpdateScreen = "  Update available · 0.163.0 → 0.164.0\nRelease notes:\nhttps://github.com/openai/codex/releases/latest\n\n› 1. Update now (runs `npm install -g @openai/codex`)\n  2. Skip\n  3. Skip until next version\n\n  enter continue · esc skip\n"

func TestSpawnCodexUpdatePrompt(t *testing.T) {
	for _, tc := range []struct {
		name, first, next  string
		startBlocked       bool
		wantCode, wantKeys int
	}{
		{"skip", codexUpdateScreen, codexPromptScreen, false, 0, 1},
		{"start not ready", codexUpdateScreen, codexPromptScreen, true, 0, 1},
		{"persistent", codexUpdateScreen, codexUpdateScreen, false, 7, 1},
		{"history", codexUpdateScreen + codexPromptScreen, codexPromptScreen, false, 0, 0},
		{"title only", "Update available · 0.163.0 → 0.164.0\n› Ask Codex\n", codexPromptScreen, false, 0, 0},
		{"skip option", strings.TrimSuffix(codexUpdateScreen, "\n  enter continue · esc skip\n"), codexPromptScreen, false, 0, 1},
		{"unreadable after escape", codexUpdateScreen, "", false, 7, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCodexHooksFixture(t, []string{tc.first}, tc.next)
			if tc.startBlocked {
				var rules []fakecli.Rule
				rules = append(rules, fakecli.Rule{Argv: []string{"agent", "start"}, ArgvPrefix: true, Code: 1, Stderr: "agent_not_ready"})
				rules = append(rules, fakecli.Rule{Argv: grokScreenRead("worker"), Call: 1, Stdout: tc.first}, fakecli.Rule{Argv: grokScreenRead("worker"), Stdout: tc.next})
				rules = append(rules, fakecli.Rule{Argv: []string{"agent", "send-keys", "worker"}, ArgvPrefix: true})
				rules = append(rules, freshSpawnRules()...)
				writeWorkerStateRules(t, f, rules)
			}
			code, out, stderr, calls := runCmdSpawn(t, f, []string{"worker"})
			status := "ready"
			if tc.wantCode == 7 {
				status = "blocked_at_startup"
			}
			if code != tc.wantCode || !strings.Contains(out, `"status": "`+status+`"`) {
				t.Fatalf("exit=%d out=%q stderr=%q", code, out, stderr)
			}
			want := []string{}
			if tc.wantKeys == 1 {
				want = append(want, "Escape")
			}
			if got := agentSendKeys(calls); !reflect.DeepEqual(got, want) {
				t.Fatalf("keys=%v want=%v", got, want)
			}
			for _, c := range calls {
				if len(c.Argv) >= 2 && c.Argv[1] == "send-text" {
					t.Fatalf("unexpected text: %v", c.Argv)
				}
			}
			if tc.wantKeys == 1 {
				key := lastCallIndex(calls, "agent", "send-keys", "worker", "Escape")
				read := lastCallIndex(calls, grokScreenRead("worker")...)
				if key < 0 || read <= key {
					t.Fatalf("read=%d escape=%d", read, key)
				}
			}
			warning := "soho: spawn: skipped codex's update prompt for 'worker' (0.163.0 → 0.164.0); update codex yourself when no codex agent is running"
			if tc.wantCode == 0 && tc.wantKeys == 1 && strings.Count(stderr, warning) != 1 {
				t.Fatalf("warning=%q", stderr)
			}
			if tc.wantKeys == 0 && strings.Contains(stderr, "skipped codex's update") {
				t.Fatalf("unexpected warning=%q", stderr)
			}
		})
	}
}

func TestCodexUpdateVersions(t *testing.T) {
	cases := []struct {
		name, screen, want string
	}{
		{"title line", codexUpdateScreen, " (0.163.0 → 0.164.0)"},
		{"target wrapped onto the next line", "Update available · 0.163.0 →\n0.164.0\n", " (0.163.0 → 0.164.0)"},
		{"no versions", "Update available\nesc skip\n", ""},
		{"missing target", "Update available · 0.163.0 →\n\nesc skip\n", ""},
		{"prose after the target", "Update available · 0.163.0 → 0.164.0 please run npm\n", ""},
		{"control bytes", "Update available · 0.163.0 → 0.164.0\x1b[31m\n", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := codexUpdateVersions(tc.screen); got != tc.want {
				t.Fatalf("codexUpdateVersions = %q, want %q", got, tc.want)
			}
		})
	}
}
