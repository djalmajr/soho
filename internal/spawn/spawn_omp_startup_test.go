package spawn

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

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

// ompFullStartupAnimation is a retained Herdr 0.9.5 macOS OMP 19.1.1 visible capture.
const ompFullStartupAnimation = `
    ✧   ✧                    ·

         ·                     ✦
                                    ·       ·
                                           ·
                ████████████████████████
                ████████████████████████
                      ████    ████     ✧
           ✦          ████    ████                      ✧
                ·     ████    ████
                      ████   ·████             ·
 ✧      ✦             ▒▒▒▒    ████                    ·
                      ▒▒▒▒    ████             ·
                ·             ████
                              ████
      ░░░▒▒▒▒░░░       ░░░▒▒▒▒▒▒▒▒▒▒▒▒░░░░░░░░░░ ░
       ░░░▒░░░░░       ░░▒▒▒▒▒▒▒▒▒▒░░░ ░░░░░░░░░
        ░░░▒▒▒░░░░  ░░░░░▒▒▒▒▒▒▒░░░░░  ░░░░░▒▒░░░
         ░░░▒░▒▒░░░░░░░▒▒▒▒▒▒▒▒▒░░░   ░░░▒▒▒▒░░░░
           ░▒▒▒▒▒▒▒▒▒░░▒▒▒▒▒▒▒▒░░░░  ░░░░▒▒▒▒▒░░
           ░░▒▒▒▒▓▓▒▒▒▒▒▒▒▒▒▓▒▒░░  ░░░░▒▒▒▒▒▒░░░
            ░▒▒▒▓▓▓▓▓▓▓▒▒▒▒▓▒▒▒░░░░░░▒▒▒▒▒▒▒▒░░░
            ░░▒▒▓▓▓▓▓▓▓▓▒▒▒▒▒▒▒░░▒▒▒▒▒▒▒▒▒▒░░░
             ░░▒▒▒▓▓▓▓▓▒▒▒▒▒▒▒▒░▒▒▒▒▒▓▒▒▒▒░░
              ░░░▒▒▒▒▒▒▒▒▒░▒▒▒▒▒▒▒▒▒▒▒▒▒▒░░
                ░░░▒░░▒░░░░░░░░░░▒▒▒▒▒▒░░░
                     ░ ░  ░  ░░░▒▒▒▒░░░
                              ░░░░░░░
                             ░░░░░░░
                            ░░░░░░░
                         ░░░░░░░░
                        ░░░░▒▒░░░░
              ░ ░░░░░░░░░░▒▒▒▒▒░░░
              ░░░░░░░░░▒▒▒▒▒▒▒▒░░  ░  ░░░░░
               ░░░▒░▒░░▒░░▒▒▒▒░░░  ░░░░░░░ ░
               ░░░░▒▒▒░░░░░░░░░░   ░ ░░ ░░░
                ░░░░ press ⏎ to skip  ░
`

// ompComposerAfterStartupAnimation is a retained Herdr 0.9.5 macOS OMP 19.1.1 visible capture.
const ompComposerAfterStartupAnimation = `
             ████████████
                ██  ██       ▄▀▀▄ █▀▄▀▄ █▀▀▄
                ██  ██       ▀▄▄▀ █ █ █ █▄▄▀
                ▒▒  ██                  █
                    ██       v19.1.1

 Tip: Say ` + "`" + `workflowz` + "`" + ` in your message to drive the task
 with parallel subagents in eval — watch it glow as you
                          type


 π > ◒ Delivery Canary > 🗑 …/project > ⑂ main ▶─╎─┃──32K─
╰─                           ⇧⇥ to change thinking effort
`

// ompLinuxFullStartupAnimation is a retained Linux Herdr 0.9.5 / OMP 19.2.0 capture.
const ompLinuxFullStartupAnimation = `  ·
                    ✦                       ·
                                        ✦
                         ·
                       ·
                ████████████████████████
                ████████████████████████
                      ████    ████
                      ████    ████
                      ████    ████
                      ████    ████
                      ▒▒▒▒    ████
                      ▒▒▒▒    ████
                              ████
                              ████
   ·    ·
                                      ✦
           ✦                        ·
✧                       ░       ✧
   ✦                 ░ ░
                      ░    ░
░                     ░░         ·
░                     ░░
░                    ░ ░  ░
░ ·                   ░  ░                       ·
░░                     ✦         ✦
░░                     ·    ✦          ✦               ░░
                                             ░ ░ ░░░░▒░░░
                ✧     ✦              ░ ░░░░░▒▒▒▒▒▒▒▒▒▒▒░░
   ✧✦               ·                ░▒▒░▓▓▓▓▓▓▓█▓▓█▓▓▒▒▒
                       ✦            ░░▒▒▓█▓██████████▓█▓▒
             ·             ✧       ░░░▓▓███████████████▓▒
                                   ░░▒▓▓████████████████▓
                             ✦      ░▒▓▓███████████████▓▓
                                    ░░▓▓██████████████▓▓▒
                                    ░░▒▓██████████████▓▓▒
                                     ░▒▓▓████████████▓▓▓▒
                     press ⏎ to skip
`

// ompLinuxWizardAfterStartupAnimation is a retained Linux Herdr 0.9.5 / OMP 19.2.0 capture.
const ompLinuxWizardAfterStartupAnimation = `
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
    │   Anthropic (Claude Pro/Max, 1-year token)  │ │
    │   Z.AI (GLM Coding Plan)                    │ │
    │   Z.AI (GLM Coding Plan · Sign in)          │ │
    │   Kimi Code                                 │ │
    │   OpenRouter                                │ │
    │   GitHub Copilot                            │ │
    │   Cursor (Claude, GPT, etc.)                │ │
    │   Devin                                     │ │
░░                     ·    ✦                          ░░
░                                                 ░░ ░░░░
 ░                                       ░  ░░░░░░░▒▒▒▒░░
                    ·                ░░▒░▒▒▒▒▒▓▒▓▓▓▓▓▓▒▒▒
                                    ░░▒▒▓▓▓▓▓▓▓▓██▓▓▓▓▓▒▒
                    ✧      ✧       ░░░▒▒▓▓█████████████▓▒
                                   ░░▒▓▓███████████████▓▒
                                    ░▒▓▓███████████████▓▓
                                   ░░░▓▓██████████████▓▓▒
                                    ░░▓▓██████████████▓▓▒
                                    ░░▓▓▓████████████▓▓▓▒
                     press ⏎ to skip
`

// ompLinuxComposerAfterStartupAnimation is a retained Linux Herdr 0.9.5 / OMP 19.2.0 capture.
const ompLinuxComposerAfterStartupAnimation = `
             ████████████
                ██  ██       ▄▀▀▄ █▀▄▀▄ █▀▀▄
                ██  ██       ▀▄▄▀ █ █ █ █▄▄▀
                ▒▒  ██                  █
                    ██       v19.2.0

  Tip: ` + "`" + `/force read` + "`" + ` pins the next turn to one specific
  tool when the model keeps reaching for the wrong one


 π > ◒ Delivery Canary > 🗑 …roject > ⑂ master ▶─╎─┃──32K─
╰─                           ⇧⇥ to change thinking effort
`

// ompSparklyStartupAnimation is the retained macOS capture with sparkles beside both beams.
const ompSparklyStartupAnimation = `

                       ✦                      ✧
                          · ✦            ✧

                        ✦
                ████████████████████████  ✧      ·
                ████████████████████████          ·
             ✦        ████    ████
                      ████    ████
                      ████    ████
                      ████    ████                  ✦
                      ▒▒▒▒    ████
                      ▒▒▒▒    ████
                              ████
                              ████
      ░░░▒▒▒▒░░░       ░░░▒▒▒▒▒▒▒▒▒▒▒▒░░░░░░░░░░ ░
      ░░░░▒░░░░        ░░▒▒▒▒▒▒▒▒▒▒░░░  ░░░░░░░░░
       ░░░░▒▒░░░     ░░░░▒▒▒▒▒▒▒░░░░░   ░░░░░▒░░░
         ░░░▒░░░░░░ ░░░▒▒▒▒▒▒▒▒▒░░░     ░░▒░░░░░░
          ░░▒▒▒▒▒▒░▒▒░░▒▒▒▒▒▒▒▒░░░░   ░░░░▒▒▒▒░░
           ░░▒▒▒▒▒▒▒▒▒▒▒▒▒▒▒▓▒▒░░  ░ ░░░▒▒▒▒▒▒░░
           ░░▒▒▒▓▓▓▓▓▓▓▒▒▒▒▓▒▒▒░░░░░░░▒▒▒▒▒▒▒░░░
            ░▒▒▒▓▓▓▓▓▓▓▒▒▒▓▒▒▒▒░░░░▒▒▒▒▒▒▒▒▒░░
             ░░▒▓▓▓▓▓▓▓▒▒▒▒▒▒▒▒░▒░▒▒▒▒▒▒▒▒▒░░
              ░░▒▒▒▓▓▓▒▒▒▒░▒▒▒▒▒▒▒▒▒▒▒▒▒▒░░
               ░░░░▒▒▒▒░░░░░░░░░░▒▒▒▒▒▒░░░
                    ░░░░░ ░░ ░░░▒▒▒▒▒▒░░
                              ░░░▒░░░░
                             ░░░░░░░░
                            ░░░░░░░░
                          ░░░░░░░░
                        ░░░░░▒░░░░
              ░     ░░░░░░▒▒▒▒░░░░
              ░░░░░░░░░▒▒░▒▒▒▒▒░░      ░░░░
               ░░░▒░▒░░▒░░▒▒▒▒░░░  ░░░ ░ ░ ░
               ░░░░▒▒▒░░░░░░▒░░░░  ░ ░░ ░░░
                ░░░░ press ⏎ to skip  ░
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

func TestSpawnOMPStartupAnimation(t *testing.T) {
	const pane = "w1:p2"
	const unrelated = "ordinary output\n░░░░ press ⏎ to skip  ░\n"
	for _, tc := range []struct {
		name       string
		first      string
		next       []string
		replaced   bool
		persistent bool
		code       int
	}{
		{name: "splash then wizard", first: ompFullStartupAnimation, next: []string{ompWizardScreen}, code: 7},
		{name: "splash then composer", first: ompFullStartupAnimation, next: []string{ompComposerAfterStartupAnimation}},
		{name: "macOS sparkly splash then wizard", first: ompSparklyStartupAnimation, next: []string{ompWizardScreen}, code: 7},
		{name: "macOS sparkly splash then composer", first: ompSparklyStartupAnimation, next: []string{ompComposerAfterStartupAnimation}},
		{name: "macOS sparkly art with split beam", first: strings.Replace(ompSparklyStartupAnimation, strings.Repeat("█", 24), strings.Repeat("█", 12)+"✧"+strings.Repeat("█", 12), 1)},
		{name: "macOS sparkly art with wider columns", first: strings.Replace(ompSparklyStartupAnimation, "████    ████", "█████    ████", 1)},
		{name: "macOS sparkly art with ordinary text", first: "ordinary output\n" + ompSparklyStartupAnimation},
		{name: "Linux splash then wizard", first: ompLinuxFullStartupAnimation, next: []string{ompLinuxWizardAfterStartupAnimation}, code: 7},
		{name: "Linux splash then composer", first: ompLinuxFullStartupAnimation, next: []string{ompLinuxComposerAfterStartupAnimation}},
		{name: "Linux splash then unrelated picker body then composer", first: ompLinuxFullStartupAnimation, next: []string{
			strings.Replace(ompLinuxWizardAfterStartupAnimation, "│   ", "ordinary output ", 1),
			ompLinuxComposerAfterStartupAnimation,
		}},
		{name: "Linux standalone footer without logo", first: "✧·\n░▒▓█\n✦░\n·░\npress ⏎ to skip\n"},
		{name: "Linux arbitrary Enter wording with logo", first: strings.Replace(ompLinuxFullStartupAnimation, "press ⏎ to skip", "press Enter to continue", 1)},
		{name: "splash then unrelated then composer", first: ompFullStartupAnimation, next: []string{unrelated, ompComposerAfterStartupAnimation}},
		{name: "persistent splash", first: ompFullStartupAnimation, persistent: true, code: 4},
		{name: "changed identity before reread", first: ompFullStartupAnimation, next: []string{ompWizardScreen}, replaced: true, code: 4},
		{name: "unrelated text without splash", first: unrelated},
		{name: "unrelated art without OMP logo", first: "✧ ✦ ░▒▓█\n░░░░ press ⏎ to skip  ░\n"},
		{name: "quoted full splash", first: "> " + strings.ReplaceAll(ompFullStartupAnimation, "\n", "\n> ")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var rules []fakecli.Rule
			for i, screen := range tc.next {
				rules = append(rules, fakecli.Rule{
					Argv: []string{"agent", "read", pane, "--source", "visible"}, Call: i + 2, Stdout: screen,
				})
			}
			if tc.replaced {
				rules = append(rules, fakecli.Rule{
					Argv: []string{"agent", "get", pane}, Call: 3,
					Stdout: ompStartupRecord(pane, "term_omp_start", "omp-session-replacement"),
				})
			}
			f := newOMPSpawnFixture(t, append(rules, ompStartupRules(pane, tc.first)...)...)
			code, out, stderr, refusal, calls := runLaneModelSpawn(t, f, ompStartupArgs(pane))
			if code != tc.code {
				t.Fatalf("exit=%d want=%d stdout=%q stderr=%q refusal=%q", code, tc.code, out, stderr, refusal)
			}
			row := core.RosterLine(filepath.Join(f.state, "ws"), "worker")
			switch tc.code {
			case 7:
				line, screen, _ := strings.Cut(out, "\n")
				if !spawnJSONHasField(t, line, `"status": "blocked_at_startup"`) ||
					!spawnJSONHasField(t, line, `"reason": "setup-required"`) ||
					!spawnJSONHasField(t, line, `"source": "visible"`) ||
					screen != tc.next[len(tc.next)-1] || !strings.Contains(stderr, "complete OMP setup") {
					t.Fatalf("wrong wizard outcome: stdout=%q stderr=%q", out, stderr)
				}
			case 4:
				reason := "identity or state changed"
				if tc.persistent {
					reason = "startup animation did not resolve"
				}
				if out != "" || row != "" || !strings.Contains(refusal, "could not verify OMP startup") || !strings.Contains(refusal, reason) {
					t.Fatalf("wrong unverified outcome: stdout=%q refusal=%q roster=%q", out, refusal, row)
				}
			default:
				if !spawnJSONHasField(t, out, `"status": "ready"`) || strings.Contains(out, "startup_evidence") || strings.Contains(stderr, "complete OMP setup") {
					t.Fatalf("wrong ready outcome: stdout=%q stderr=%q", out, stderr)
				}
			}
			if tc.code != 4 {
				fields := strings.Split(row, "\t")
				if fieldAt(fields, 1) != pane || fieldAt(fields, 5) != "0" || fieldAt(fields, 8) != ompFixtureModel {
					t.Fatalf("wrong managed worker after startup: %q", row)
				}
			}

			observed := ompStartupCalls(t, calls, pane)
			want := []string{"start", "get", "read", "get"}
			for range tc.next {
				want = append(want, "get", "read", "get")
			}
			switch {
			case tc.persistent:
				if len(observed) < len(want)+3 || len(observed) > 1+3*(1+int(startPollWindow/(500*time.Millisecond))) {
					t.Fatalf("unresolved animation was not polled within the startup window: %q", observed)
				}
				for len(want) < len(observed) {
					want = append(want, "get", "read", "get")
				}
				want = want[:len(observed)]
			case tc.replaced:
				want = want[:len(want)-2]
			}
			if !slices.Equal(observed, want) {
				t.Fatalf("launch and observation sequence=%q want=%q", observed, want)
			}
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

func ompStartupCalls(t *testing.T, calls []fakecli.Call, pane string) []string {
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
	return observed
}

func assertOMPStartupCalls(t *testing.T, calls []fakecli.Call, pane string, read, postGet bool) {
	t.Helper()
	observed := ompStartupCalls(t, calls, pane)
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
