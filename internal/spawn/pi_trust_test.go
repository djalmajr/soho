package spawn

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

const piTrustScreen = `────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────

 Trust project folder?
 /private/tmp/pi-trust-probe.oJTqzi

 This allows pi to load .pi settings and resources, install missing project packages, and execute project extensions.

 → Trust
   Trust parent folder (/private/tmp)
   Trust (this session only)
   Do not trust
   Do not trust (this session only)

 ↑↓ navigate  enter select  escape/ctrl+c cancel

────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
`

func TestPiTrustDialog(t *testing.T) {
	for _, tc := range []struct {
		name, screen string
		want         bool
	}{
		{"real", piTrustScreen, true},
		{"title alone", "Trust project folder?", false},
		{"no cursor", strings.Replace(piTrustScreen, "→ ", "", 1), true},
		{"option alone", "Trust", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isPiTrustDialog(tc.screen); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestSpawnHcPiTrustDialog(t *testing.T) {
	for _, tc := range []struct {
		name, kind, screen, status string
		exit                       int
	}{
		{"blocked", "pi", piTrustScreen, `{"address":"t7","state":"live","reported":true,"status":null}`, 7},
		{"ready", "pi", "ready", hcReadyWorker, 0},
		{"codex", "codex", piTrustScreen, hcReadyWorker, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cwd, bin, _, env, cfg := hcSpawnFixture(t, "reviewer", tc.kind)
			body, _ := json.Marshal(map[string]string{"text": tc.screen})
			rules := append(hcFixtureRules(),
				fakecli.Rule{Argv: []string{"get", "t7", "--json"}, Stdout: tc.status},
				fakecli.Rule{Argv: []string{"read", "t7", "--lines", "0", "--json"}, Stdout: string(body)},
				fakecli.Rule{Argv: []string{"read", "t7", "--lines", "40", "--json"}, Stdout: string(body)},
			)
			if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
				t.Fatal(err)
			}
			exit, out, stderr, calls := spawnHcExitWithLaunch(t, bin, []string{"reviewer", "--timeout", "60000"}, cfg, env, cwd, newFakeSpawnRPCConn(), nil)
			code := 0
			if exit != nil {
				code = exit.Code
			}
			if code != tc.exit {
				t.Fatalf("exit=%d want=%d stderr=%s out=%s", code, tc.exit, stderr, out)
			}
			if tc.exit == 7 {
				if !spawnJSONHasField(t, out.String(), `"status": "blocked_at_startup"`) || !strings.Contains(out.String(), piTrustScreen) {
					t.Fatalf("missing status or screen: %s", out)
				}
				warning := fmt.Sprintf("spawn: pi 'reviewer' is waiting at its \"Trust project folder?\" dialog for %s; answer it in that terminal, or pass --approve after -- to trust project-local files for this run", cwd)
				if !strings.Contains(stderr.String(), warning) {
					t.Fatalf("missing warning %q: %s", warning, stderr)
				}
				for _, call := range calls {
					switch call.Argv[0] {
					case "send", "keys", "kill", "rm":
						t.Fatalf("unexpected call: %v", call.Argv)
					}
				}
			} else if !spawnJSONHasField(t, out.String(), `"status": "ready"`) {
				t.Fatalf("not ready: %s", out)
			}
		})
	}
}
