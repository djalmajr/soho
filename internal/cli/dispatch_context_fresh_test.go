package cli

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func contextPiScreen(history, box, footer string) string {
	sep := strings.Repeat("─", 66)
	return history + "\n\n" + sep + "\n" + box + "\n" + sep + "\n~/project\n" + footer + "\n"
}

func TestDispatchContextPercentFor(t *testing.T) {
	real := "↑5.2M ↓43k 40.3%/262k (auto)           (provider) model • high"
	for _, tc := range []struct {
		name, kind, screen string
		want               int
	}{
		{"pi fixture", "pi", contextPiScreen("history", "", real), 40},
		{"pi high", "pi", contextPiScreen("history", "", "↑1M ↓4k 95.0%/262k (auto)"), 95},
		{"pi truncates", "pi", contextPiScreen("", "", "69.999999999999999999%/1M"), 69},
		{"pi uppercase K", "pi", contextPiScreen("", "", "70%/262K"), 70},
		{"pi lowercase m CRLF", "pi", strings.ReplaceAll(contextPiScreen("", "", "70.1%/2m"), "\n", "\r\n"), 70},
		{"pi history only", "pi", contextPiScreen("95.0%/262k", "", "no usage"), -1},
		{"pi input only", "pi", contextPiScreen("", "95.0%/262k", "no usage"), -1},
		{"pi history higher than footer", "pi", contextPiScreen("95.0%/262k", "", real), 40},
		{"pi no box", "pi", "95.0%/262k", -1},
		{"pi one border", "pi", strings.Repeat("─", 66) + "\n95.0%/262k", -1},
		{"pi invalid denominator", "pi", contextPiScreen("", "", "95.0%/262"), -1},
		{"pi invalid percent", "pi", contextPiScreen("", "", "-95.0%/262k"), -1},
		{"pi malformed token", "pi", contextPiScreen("", "", "95.0%/262kb"), -1},
		{"pi percent overflow", "pi", contextPiScreen("", "", strings.Repeat("9", 100)+"%/262k"), -1},
		{"codex never parses", "codex", contextPiScreen("", "", real), -1},
		{"claude never parses", "claude", "34K (99%) ctrl+p commands", -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := dispatchContextPercentFor(tc.kind, tc.screen); got != tc.want {
				t.Fatalf("kind=%s screen=%q got=%d want=%d", tc.kind, tc.screen, got, tc.want)
			}
		})
	}
	// Existing opencode cases remain unchanged and also exercise the new switch.
	for _, screen := range []string{"34K (99%) ctrl+p commands", "bad ctrl+p commands", "12K (75%) ctrl+p commands\n\n", "no footer"} {
		if got, want := dispatchContextPercentFor("opencode", screen), dispatchContextPercent(screen); got != want {
			t.Fatalf("opencode screen=%q got=%d want=%d", screen, got, want)
		}
	}
}

func contextStateFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() == "friction.log" {
			return nil
		}
		b, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		rel, e := filepath.Rel(root, path)
		if e != nil {
			return e
		}
		result[rel] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestDispatchContextFresh(t *testing.T) {
	pi85 := contextPiScreen("history", "", "↑1M ↓4k 85.0%/262k (auto)")
	const refusal = "soho: dispatch: 'worker' is at 85% of its context (context_fresh_percent=70); nothing was sent. Release it with --close and spawn a fresh worker, or pass --allow-full-context to send anyway\n"
	for _, tc := range []struct {
		name, kind, screen, fresh, warn string
		flags                           []string
		refuse, warning, readError      bool
	}{
		// With --compact the compaction runs first and the post-compact footer
		// decides; without it the current footer refuses before any task state.
		{name: "pi refuses", kind: "pi", screen: pi85, fresh: "70", warn: "60", refuse: true},
		{name: "pi override still warns", kind: "pi", screen: pi85, fresh: "70", warn: "60", flags: []string{"--allow-full-context"}, warning: true},
		{name: "pi forty sends", kind: "pi", screen: contextPiScreen("", "", "40.3%/262k"), fresh: "70", warn: "60"},
		{name: "opencode refuses", kind: "opencode", screen: "200K (85%) ctrl+p commands", fresh: "70", warn: "60", refuse: true},
		{name: "codex unaffected", kind: "codex", screen: pi85, fresh: "70", warn: "60"},
		{name: "claude unaffected", kind: "claude", screen: "200K (85%) ctrl+p commands", fresh: "70", warn: "60"},
		{name: "unreadable sends", kind: "pi", screen: "unreadable", fresh: "70", warn: "60"},
		{name: "transport read failure sends", kind: "pi", fresh: "70", warn: "60", readError: true},
		{name: "history cannot refuse", kind: "pi", screen: contextPiScreen("95.0%/262k", "", "unknown"), fresh: "70", warn: "60"},
		{name: "zero off still warns", kind: "pi", screen: pi85, fresh: "0", warn: "60", warning: true},
		{name: "zero and warning off", kind: "pi", screen: pi85, fresh: "0", warn: "0"},
		{name: "refusal without warning enabled", kind: "pi", screen: pi85, fresh: "70", warn: "0", refuse: true},
		{name: "threshold inclusive", kind: "pi", screen: pi85, fresh: "85", warn: "0", refuse: true},
		{name: "override does not enable warning", kind: "pi", screen: pi85, fresh: "70", warn: "0", flags: []string{"--allow-full-context"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rule := fakecli.Rule{Argv: []string{"agent", "read", "worker", "--source", "visible"}, ArgvPrefix: true, Stdout: tc.screen}
			if tc.readError {
				rule.Code = 1
			}
			f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0", rule)
			f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
			f.env["SOHO_CONTEXT_FRESH_PERCENT"] = tc.fresh
			f.env["SOHO_CONTEXT_WARN_PERCENT"] = tc.warn
			roster := filepath.Join(f.state, "ws", "agents.tsv")
			body, err := os.ReadFile(roster)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(roster, []byte(strings.Replace(string(body), "\tcodex\t", "\t"+tc.kind+"\t", 1)), 0600); err != nil {
				t.Fatal(err)
			}
			before := contextStateFiles(t, f.state)
			args := append([]string{"worker", f.brief, "--no-wait"}, tc.flags...)
			// Put the override before positional arguments to cover both option passes.
			if len(tc.flags) > 0 && tc.flags[0] == "--allow-full-context" {
				args = append([]string{"--allow-full-context", "worker", f.brief, "--no-wait"}, tc.flags[1:]...)
			}
			code, out, stderr := f.run(t, args...)
			calls, err := fakecli.ReadCallsForConfig(filepath.Join(f.bin, "herdr.json"))
			if err != nil {
				t.Fatal(err)
			}
			prompts := 0
			for _, call := range calls {
				if len(call.Argv) > 1 && call.Argv[0] == "agent" && call.Argv[1] == "prompt" {
					prompts++
				}
				if len(call.Argv) > 1 && (call.Argv[1] == "close" || call.Argv[1] == "stop" || call.Argv[1] == "restart" || call.Argv[1] == "send-keys") {
					t.Fatalf("unexpected lifecycle/compact action: %v", call.Argv)
				}
			}
			if tc.refuse {
				want := refusal
				if tc.fresh == "85" {
					want = strings.Replace(want, "percent=70", "percent=85", 1)
				}
				if code != 10 || out != "" || stderr != want || prompts != 0 {
					t.Fatalf("code=%d out=%q stderr=%q prompts=%d want=%q", code, out, stderr, prompts, want)
				}
				if after := contextStateFiles(t, f.state); !reflect.DeepEqual(before, after) {
					t.Fatalf("task state changed: before=%v after=%v", before, after)
				}
			} else {
				if code != 0 || dispatchOutputStatus(t, out) != "submitted" || prompts != 1 {
					t.Fatalf("code=%d out=%q stderr=%q prompts=%d", code, out, stderr, prompts)
				}
				for _, name := range []string{"last-report-worker", "task-report-worker.json"} {
					if _, err := os.Stat(filepath.Join(f.state, "ws", name)); err != nil {
						t.Fatal(err)
					}
				}
				matches, err := filepath.Glob(filepath.Join(f.state, "ws", "briefs", "*.dispatch.json"))
				if err != nil {
					t.Fatal(err)
				}
				if len(matches) != 1 {
					t.Fatalf("dispatch sidecars=%v", matches)
				}
			}
			hasWarning := strings.Contains(stderr, "a long context can end its turn empty ("+tc.kind+")")
			if hasWarning != tc.warning {
				t.Fatalf("warning=%t want=%t stderr=%q", hasWarning, tc.warning, stderr)
			}
			if strings.Contains(stderr, "fakecli: no rule") {
				t.Fatalf("unmatched fake: %s", stderr)
			}
			t.Logf("kind=%s fresh=%s code=%d prompts=%d state-preserved=%t warning=%t", tc.kind, tc.fresh, code, prompts, tc.refuse, hasWarning)
		})
	}
}

func TestConfigContextFreshCLI(t *testing.T) {
	env, cwd := commandFixture(t)
	code, out, stderr := runIn(t, []string{"config", "get", "context_fresh_percent"}, env, cwd)
	if code != 0 || strings.TrimSpace(out) != "0" || stderr != "" {
		t.Fatalf("default: code=%d out=%q stderr=%q", code, out, stderr)
	}
	for _, args := range [][]string{{"config", "set", "context_fresh_percent", "70"}, {"session", "set", "context_fresh_percent", "80"}} {
		code, out, stderr = runIn(t, args, env, cwd)
		if code != 0 {
			t.Fatalf("%v: code=%d out=%q stderr=%q", args, code, out, stderr)
		}
	}
	code, out, stderr = runIn(t, []string{"config", "get", "context_fresh_percent"}, env, cwd)
	if code != 0 || strings.TrimSpace(out) != "80" {
		t.Fatalf("session: code=%d out=%q stderr=%q", code, out, stderr)
	}
	env["SOHO_CONTEXT_FRESH_PERCENT"] = "90"
	code, out, stderr = runIn(t, []string{"config", "get", "context_fresh_percent"}, env, cwd)
	if code != 0 || strings.TrimSpace(out) != "90" {
		t.Fatalf("env: code=%d out=%q stderr=%q", code, out, stderr)
	}
	for _, bad := range []string{"-1", "1.5", "garbage"} {
		code, out, stderr = runIn(t, []string{"config", "set", "context_fresh_percent", bad}, env, cwd)
		if code != 2 {
			t.Fatalf("invalid %q: code=%d out=%q stderr=%q", bad, code, out, stderr)
		}
	}
}
