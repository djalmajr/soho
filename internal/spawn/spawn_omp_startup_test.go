package spawn

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// ompWizardScreen is the retained Herdr 0.9.5 macOS OMP 19.1.1 visible capture.
const ompWizardScreen = `
                      ████████████
                         ██  ██
                         ██  ██
                         ▒▒  ██
                             ██
                           omp
                    Setup step 1 of 6

    Sign in to your providers
    Sign in to one or more providers. Press ⎋ when you'r…

    Pick a provider to sign in — you can connect more
    than one.

    ╭─ Select provider to login ────────────────────╮
    │ ❯ ChatGPT Plus/Pro (Codex Subscription)     █ │
    │   Anthropic (Claude Pro/Max)                │ │
    │   Z.AI (GLM Coding Plan)                    │ │
    │   Z.AI (GLM Coding Plan · Sign in)          │ │
    │   Kimi Code                                 │ │
    │   OpenRouter                                │ │
    │   GitHub Copilot                            │ │
    │   Cursor (Claude, GPT, etc.)                │ │
    │   Devin                                     │ │
    │   Devin (adopt CLI credentials)             │ │
    │ Type to search                                │
    ╰───────────────────────────────────────────────╯









     ↑/↓ select · ⏎ confirm · ⎋ skip · ⌃C exit setup
`

// ompReadyScreen is the retained Herdr 0.9.5 macOS OMP 19.1.1 visible capture.
const ompReadyScreen = `
             ████████████
                ██  ██       ▄▀▀▄ █▀▄▀▄ █▀▀▄
                ██  ██       ▀▄▄▀ █ █ █ █▄▄▀
                ▒▒  ██                  █
                    ██       v19.1.1

   Tip: Lint/type errors piling up? ` + "`" + `omp cleanse` + "`" + ` (or
   /cleanse right here) hunts project diagnostics and
     fixes them with parallel subagents — ⎋ cancels

─────────────────────────────────────────────────────────
 Update Available
 New version 19.2.1 is available. Run: omp update
─────────────────────────────────────────────────────────

 π > ◒ Delivery Canary > 🗑 …/project > ⑂ main ▶─╎─┃──32K─
╰─                           ⇧⇥ to change thinking effort
`

// ompLinuxWizardScreen is the retained Herdr 0.9.5 Linux OMP 19.2.0 visible capture.
const ompLinuxWizardScreen = `
                      ████████████
                         ██  ██
                         ██  ██
                         ▒▒  ██
                             ██
                           omp
                    Setup step 1 of 6

    Sign in to your providers
    Sign in to one or more providers. Press ⎋ when you'r…

    Pick a provider to sign in — you can connect more
    than one.

    ╭─ Select provider to login ────────────────────╮
                     ░                ✦
           ✦         ░              ·
✧                       ░       ✧
   ✦                ░░ ░░
                    ░░░░░  ░
                   ░ ░░░         ·
░                  ░░░░░░
░                    ░░░░ ░
░                     ░  ░░                      ·
░░                   ░ ░   ░
░░                     ·                               ░░
░                                                 ░░ ░░░░
                                         ░  ░░▒▒░░▒▒▒▒▒░░
                    ·                ░░▒░▒▒▒▒▒▓▒▓▓▓▓▓▓▒▒▒
                                    ░░▒▒▓▓▓▓▓▓████▓▓▓▓▓▒▒
                    ✧      ✧       ░░░▒▒▓▓█████████████▓▒
                                   ░░▒▓▓███████████████▓▒
                                    ░▒▓▓███████████████▓▓
                                   ░░░▓▓██████████████▓▓▒
                                    ░░▓▓██████████████▓▓▒
                                    ░░▓▓▓████████████▓▓▓▒
                     press ⏎ to skip
`

// ompLinuxReadyScreen is the retained Herdr 0.9.5 Linux OMP 19.2.0 visible capture.
const ompLinuxReadyScreen = `
             ████████████
                ██  ██       ▄▀▀▄ █▀▄▀▄ █▀▀▄
                ██  ██       ▀▄▄▀ █ █ █ █▄▄▀
                ▒▒  ██                  █
                    ██       v19.2.0

    Tip: Pair up live: ` + "`" + `/collab` + "`" + ` shares your session
 through an end-to-end encrypted relay link — a teammate
   runs ` + "`" + `/join <link>` + "`" + ` to watch tool calls stream and
           prompt the agent from their own omp


 π > ◒ Delivery Canary > 🗑 …roject > ⑂ master ▶─╎─┃──32K─
╰─                           ⇧⇥ to change thinking effort
`

func ompStartupRecord(pane, terminal, session string) string {
	return fmt.Sprintf(`{"result":{"agent":{"name":"worker","agent":"omp","pane_id":%q,"terminal_id":%q,"agent_status":"idle","state_change_seq":1,"interactive_ready":true,"agent_session":{"agent":"omp","kind":"path","source":"herdr:omp","value":%q}}}}`, pane, terminal, session)
}

func ompStartupRules(pane, screen string) []fakecli.Rule {
	record := ompStartupRecord(pane, "term_omp_start", "omp-session-start")
	return []fakecli.Rule{
		{Argv: []string{"agent", "start", "worker"}, ArgvPrefix: true, Stdout: record},
		{Argv: []string{"agent", "get", pane}, Stdout: record},
		{Argv: []string{"agent", "read", pane, "--source", "visible"}, Stdout: screen},
		// The red control also shows the exact frame through the original
		// name-addressed read, so its ready result cannot be a fixture miss.
		{Argv: []string{"agent", "get", "worker"}, Stdout: record},
		{Argv: []string{"agent", "read", "worker", "--source", "visible"}, ArgvPrefix: true, Stdout: screen},
	}
}

func TestSpawnOMPStartup(t *testing.T) {
	for _, tc := range []struct {
		name, screen, pane string
		blocked            bool
	}{
		{"wizard on new pane", ompWizardScreen, "", true},
		{"wizard on given pane", ompWizardScreen, "w1:p2", true},
		{"configured composer", ompReadyScreen, "", false},
		{"stale frame above composer", ompWizardScreen + ompReadyScreen, "", false},
		{"quoted frame", "Read this transcript:\n> " + strings.ReplaceAll(ompWizardScreen, "\n", "\n> "), "", false},
		{"quoted first-run words", "omp\nSetup step 1 of 6\nSign in to your providers\n", "", false},
		{"Linux wizard under splash", ompLinuxWizardScreen, "", true},
		{"Linux configured editor", ompLinuxReadyScreen, "", false},
		{"Linux stale wizard above editor", ompLinuxWizardScreen + ompLinuxReadyScreen, "", false},
		{"Linux quoted wizard", "> " + strings.ReplaceAll(ompLinuxWizardScreen, "\n", "\n> "), "", false},
		{"Linux splash without wizard", strings.SplitN(ompLinuxWizardScreen, "╮\n", 2)[1], "", false},
		{"Linux unrelated content over picker", strings.Replace(ompLinuxWizardScreen, "✦", "ordinary output", 1), "", false},
		{"Linux arbitrary Enter wording", strings.Replace(ompLinuxWizardScreen, "press ⏎ to skip", "press Enter to continue", 1), "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pane := tc.pane
			if pane == "" {
				pane = "p2"
			}
			f := newOMPSpawnFixture(t, ompStartupRules(pane, tc.screen)...)
			code, out, stderr, refusal, calls := runLaneModelSpawn(t, f, ompStartupArgs(tc.pane))
			expectedCode, expectedStatus := 0, "ready"
			if tc.blocked {
				expectedCode, expectedStatus = 7, "blocked_at_startup"
			}
			if code != expectedCode {
				t.Fatalf("exit=%d want=%d stdout=%q stderr=%q refusal=%q", code, expectedCode, out, stderr, refusal)
			}
			line, printed, _ := strings.Cut(out, "\n")
			var result struct {
				Status      string `json:"status"`
				Pane        string `json:"pane_id"`
				Kind        string `json:"kind"`
				Model       string `json:"model"`
				Placement   string `json:"placement"`
				CreatedPane bool   `json:"created_pane"`
				Evidence    *struct {
					State  string `json:"state"`
					Reason string `json:"reason"`
					Source string `json:"source"`
				} `json:"startup_evidence"`
			}
			if err := json.Unmarshal([]byte(line), &result); err != nil || result.Status != expectedStatus || result.Pane != pane || result.Kind != "omp" || result.Model != ompFixtureModel || result.CreatedPane != (tc.pane == "") || tc.pane != "" && result.Placement != "given" {
				t.Fatalf("result=%+v parse=%v stdout=%q", result, err, out)
			}
			if tc.blocked {
				if result.Evidence == nil || result.Evidence.State != "idle" || result.Evidence.Reason != "setup-required" || result.Evidence.Source != "visible" {
					t.Fatalf("missing OMP startup evidence: %s", line)
				}
				if printed != tc.screen || !strings.Contains(stderr, "complete OMP setup in the pane before sending tasks") || !strings.Contains(stderr, pane) {
					t.Fatalf("wrong screen or operator warning: stdout=%q stderr=%q", out, stderr)
				}
			} else if result.Evidence != nil || printed != "" || strings.Contains(stderr, "complete OMP setup") {
				t.Fatalf("configured or historical screen was misreported: stdout=%q stderr=%q", out, stderr)
			}
			owned := "1"
			if tc.pane != "" {
				owned = "0"
			}
			row := strings.Split(core.RosterLine(filepath.Join(f.state, "ws"), "worker"), "\t")
			if fieldAt(row, 1) != pane || fieldAt(row, 2) != "omp" || fieldAt(row, 8) != ompFixtureModel || fieldAt(row, 15) != "inherit" || fieldAt(row, 5) != owned {
				t.Fatalf("roster changed OMP identity or ownership: %q", row)
			}
			assertOMPStartupCalls(t, calls, pane, true, true)
		})
	}
}

func TestSpawnOMPStartupIdentityAndDeadline(t *testing.T) {
	const pane = "w1:p2"
	for _, tc := range []struct {
		name    string
		rule    fakecli.Rule
		read    bool
		postGet bool
	}{
		{"changed terminal before observation", fakecli.Rule{Argv: []string{"agent", "get", pane}, Call: 1, Stdout: ompStartupRecord(pane, "term_replacement", "omp-session-start")}, false, false},
		{"changed session during observation", fakecli.Rule{Argv: []string{"agent", "get", pane}, Call: 2, Stdout: ompStartupRecord(pane, "term_omp_start", "omp-session-replacement")}, true, true},
		{"agent gone after read", fakecli.Rule{Argv: []string{"agent", "get", pane}, Call: 2, Code: 1, Stderr: `{"error":{"code":"agent_not_found"}}`}, true, true},
		{"screen arrives beyond the window", fakecli.Rule{Argv: []string{"agent", "read", pane, "--source", "visible"}, Delay: 7000, Stdout: ompWizardScreen}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newOMPSpawnFixture(t, append([]fakecli.Rule{tc.rule}, ompStartupRules(pane, ompWizardScreen)...)...)
			code, out, stderr, refusal, calls := runLaneModelSpawn(t, f, ompStartupArgs(pane))
			if code != 4 || out != "" || !strings.Contains(refusal, "could not verify OMP startup") || strings.Contains(stderr+refusal, "Sign in to your providers") {
				t.Fatalf("exit=%d stdout=%q stderr=%q refusal=%q", code, out, stderr, refusal)
			}
			if row := core.RosterLine(filepath.Join(f.state, "ws"), "worker"); row != "" {
				t.Fatalf("unverified identity written to the roster: %q", row)
			}
			assertOMPStartupCalls(t, calls, pane, tc.read, tc.postGet)
		})
	}
}

func ompStartupArgs(pane string) []string {
	args := []string{"worker", "--fresh", "--kind", "omp", "--mcp", "inherit", "--approvals", "ask", "--model", ompFixtureModel, "--effort", "high"}
	if pane != "" {
		args = append(args, "--pane", pane)
	}
	return args
}

func assertOMPStartupCalls(t *testing.T, calls []fakecli.Call, pane string, read, postGet bool) {
	t.Helper()
	var observed []string
	for _, call := range calls {
		if len(call.Argv) < 2 {
			continue
		}
		command := strings.Join(call.Argv[:2], " ")
		switch command {
		case "agent start":
			if !containsArgPair(call.Argv, "--pane", pane) {
				t.Fatalf("start targeted the wrong pane: %q", call.Argv)
			}
			observed = append(observed, "start")
		case "agent get":
			if slices.Equal(call.Argv, []string{"agent", "get", pane}) {
				observed = append(observed, "get")
			}
		case "agent read":
			if !slices.Equal(call.Argv, []string{"agent", "read", pane, "--source", "visible"}) {
				t.Fatalf("read was not the full visible frame of the launched pane: %q", call.Argv)
			}
			observed = append(observed, "read")
		case "pane read", "agent send-keys", "agent prompt", "agent release", "pane send-text", "pane send-keys", "pane run", "pane close":
			t.Fatalf("unexpected terminal action: %q", call.Argv)
		}
	}
	want := []string{"start", "get"}
	if read {
		want = append(want, "read")
	}
	if postGet {
		want = append(want, "get")
	}
	if !slices.Equal(observed, want) {
		t.Fatalf("launch and observation sequence=%q want=%q", observed, want)
	}
}
