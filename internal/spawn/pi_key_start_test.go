package spawn

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestHcPiKeyStartup(t *testing.T) {
	cases := []struct {
		name, kind, screen               string
		readFail, fail, login, closeFail bool
	}{
		{name: "missing", kind: "pi", screen: "Error: No API key found for opencode-go.\n", fail: true},
		{name: "resolve", kind: "pi", screen: "Error: API key auth failed for provider console-anthropic: Failed to resolve API key for provider\n", fail: true},
		{name: "auth", kind: "pi", screen: "Error: API key auth failed for provider example\n", fail: true},
		{name: "login shell", kind: "pi", screen: "Failed to resolve API key\n", fail: true, login: true},
		{name: "close failure", kind: "pi", screen: "No API key found for provider\n", fail: true, closeFail: true},
		{name: "clean", kind: "pi", screen: "ready\n"},
		{name: "empty", kind: "pi"},
		{name: "wrapped", kind: "pi", screen: "No API key found\n for provider\n"},
		{name: "case sensitive", kind: "pi", screen: "no API key found for provider\n"},
		{name: "long", kind: "pi", screen: strings.Repeat("safe\n", 1000)},
		{name: "codex", kind: "codex", screen: "No API key found for provider\n"},
		{name: "read failed", kind: "pi", readFail: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.login && runtime.GOOS == "windows" {
				t.Skip("login_shell=on is refused on Windows")
			}
			cwd, bin, state, env, ctx := hcSpawnFixture(t, "worker", tc.kind)
			if tc.login {
				env["SOHO_LOGIN_SHELL"] = "on"
				env["SHELL"] = "/bin/sh"
			}
			payload, err := json.Marshal(map[string]any{"text": tc.screen, "complete": true})
			if err != nil {
				t.Fatal(err)
			}
			read := fakecli.Rule{Argv: []string{"read", "t7", "--lines", "0", "--json"}, Stdout: string(payload)}
			if tc.readFail {
				read.Code = 1
				read.Stderr = `{"error":{"kind":"internal","message":"read failed"}}`
			}
			kill := fakecli.Rule{Argv: []string{"kill", "t7", "--json"}, Stdout: `{"address":"t7","state":"finished"}`}
			if tc.closeFail {
				kill.Code = 1
				kill.Stderr = `{"error":{"kind":"internal","message":"close failed"}}`
			}
			rules := append(hcFixtureRules(), fakecli.Rule{Argv: []string{"get", "t7", "--json"}, Stdout: hcReadyWorker}, read, kill, fakecli.Rule{Argv: []string{"rm", "t7", "--json"}, Stdout: `{"address":"t7"}`})
			if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
				t.Fatal(err)
			}
			exit, out, stderr, calls := spawnHcExitWithConn(t, bin, []string{"worker"}, ctx, env, cwd, newFakeSpawnRPCConn())
			reads, readAt, kills, rms, killAt, rmAt := 0, -1, 0, 0, -1, -1
			for i, c := range calls {
				switch c.Argv[0] {
				case "read":
					reads++
					readAt = i
					if c.Argv[1] != "t7" {
						t.Fatalf("wrong read target: %v", c.Argv)
					}
				case "kill":
					kills++
					killAt = i
					if c.Argv[1] != "t7" {
						t.Fatalf("wrong kill target: %v", c.Argv)
					}
				case "rm":
					rms++
					rmAt = i
					if c.Argv[1] != "t7" {
						t.Fatalf("wrong rm target: %v", c.Argv)
					}
				}
			}
			if tc.kind == "pi" && reads != 1 {
				t.Fatalf("reads=%d calls=%v", reads, calls)
			}
			if tc.kind == "codex" && reads != 0 {
				t.Fatalf("codex read=%d calls=%v", reads, calls)
			}
			if tc.fail {
				if exit == nil || exit.Code != 7 {
					t.Fatalf("exit=%+v out=%q stderr=%q", exit, out, stderr)
				}
				for _, s := range []string{"could not load", "Core environment", "soho config set login_shell on", "--user", "shell"} {
					if !strings.Contains(stderr.String(), s) {
						t.Errorf("warning missing %q: %s", s, stderr)
					}
				}
				if out.String() != tc.screen {
					t.Errorf("screen got %q want %q", out.String(), tc.screen)
				}
				if kills != 1 || killAt <= readAt || (!tc.closeFail && (rms != 1 || rmAt <= killAt)) || (tc.closeFail && rms != 0) {
					t.Errorf("close calls=%v", calls)
				}
				if tc.closeFail && !strings.Contains(stderr.String(), "could not close hc terminal t7") {
					t.Errorf("missing close warning: %s", stderr)
				}
				b, err := os.ReadFile(filepath.Join(state, "sX", "agents.tsv"))
				if err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				for _, line := range strings.Split(string(b), "\n") {
					if line != "" && !strings.HasPrefix(line, "#") {
						t.Fatalf("surviving roster row %q", line)
					}
				}
			} else {
				if exit != nil || !strings.Contains(out.String(), `"status": "ready"`) {
					t.Fatalf("exit=%+v out=%q stderr=%q", exit, out, stderr)
				}
				if killAt >= 0 || rmAt >= 0 {
					t.Fatalf("unexpected close: %v", calls)
				}
				_ = rosterRow(t, state, "sX")
			}
		})
	}
}

// TestHcPiEffectiveThinking exercises the hc path that reuses the screen read
// taken during terminal start: a drawn footer needs no second read, a footer
// caught mid-draw spends the bounded retry, and neither a hostile screen nor
// a failed read invents an effort_effective value.
func TestHcPiEffectiveThinking(t *testing.T) {
	const otherLevelWarn = "soho: warning: spawn: pi shows thinking 'low' for 'worker' where 'high' was asked; this model may not offer 'high'"
	cases := []struct {
		name, screen, retryScreen, effort, wantLevel string
		readFail                                     bool
		wantReads                                    int
		wantWarn                                     bool
	}{
		{name: "drawn footer", screen: "model • high\n", retryScreen: "working\n", effort: "high", wantLevel: "high", wantReads: 1},
		{name: "footer cut mid-draw", screen: "model • hig\n", retryScreen: "model • high\n", effort: "high", wantLevel: "high", wantReads: 2},
		{name: "footer absent", screen: "ready\n", retryScreen: "working\n", effort: "medium", wantReads: 1},
		{name: "another level", screen: "model • low\n", retryScreen: "working\n", effort: "high", wantLevel: "low", wantReads: 1, wantWarn: true},
		{name: "failed start read", retryScreen: "model • high\n", effort: "medium", readFail: true, wantReads: 1},
		{name: "level word in model id", screen: "model-high • unavailable\n", retryScreen: "working\n", effort: "medium", wantReads: 1},
		{name: "control characters in footer", screen: "model\x00 • high\x00\n", retryScreen: "working\n", effort: "medium", wantReads: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cwd, bin, _, env, ctx := hcSpawnFixture(t, "worker", "pi")
			screenJSON := func(s string) string {
				t.Helper()
				payload, err := json.Marshal(map[string]any{"text": s, "complete": true})
				if err != nil {
					t.Fatal(err)
				}
				return string(payload)
			}
			startRead := fakecli.Rule{Argv: []string{"read", "t7", "--lines", "0", "--json"}, Call: 1, Stdout: screenJSON(tc.screen)}
			if tc.readFail {
				startRead.Code = 1
				startRead.Stderr = `{"error":{"kind":"internal","message":"read failed"}}`
			}
			rules := append(hcFixtureRules(),
				fakecli.Rule{Argv: []string{"get", "t7", "--json"}, Stdout: hcReadyWorker},
				startRead,
				fakecli.Rule{Argv: []string{"read", "t7", "--lines", "0", "--json"}, Call: 2, Stdout: screenJSON(tc.retryScreen)},
				fakecli.Rule{Argv: []string{"read", "t7", "--lines", "0", "--json"}, Call: 3, Stdout: screenJSON("working\n")},
				fakecli.Rule{Argv: []string{"kill", "t7", "--json"}, Stdout: `{"address":"t7","state":"finished"}`},
				fakecli.Rule{Argv: []string{"rm", "t7", "--json"}, Stdout: `{"address":"t7"}`},
			)
			if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
				t.Fatal(err)
			}
			exit, out, stderr, calls := spawnHcExitWithConn(t, bin, []string{"worker", "--kind", "pi", "--model", "model-a", "--effort", tc.effort}, ctx, env, cwd, newFakeSpawnRPCConn())
			if exit != nil {
				t.Fatalf("exit=%+v out=%q stderr=%q", exit, out, stderr)
			}
			level, present := spawnJSONField(t, out.String(), "effort_effective")
			if present != (tc.wantLevel != "") || level != tc.wantLevel {
				t.Fatalf("effort_effective=%q present=%v, want %q present=%v; stdout=%s", level, present, tc.wantLevel, tc.wantLevel != "", out)
			}
			reads := 0
			for _, c := range calls {
				if c.Argv[0] != "read" {
					continue
				}
				reads++
				if len(c.Argv) < 2 || c.Argv[1] != "t7" {
					t.Fatalf("wrong read target: %v", c.Argv)
				}
			}
			if reads != tc.wantReads {
				t.Fatalf("reads=%d want %d: calls=%v", reads, tc.wantReads, calls)
			}
			if warned := strings.Contains(stderr.String(), otherLevelWarn); warned != tc.wantWarn {
				t.Fatalf("warning=%v want %v; stderr=%q", warned, tc.wantWarn, stderr)
			}
		})
	}
}
