package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

func TestDispatchWaitingDuplicateEnvCount(t *testing.T) {
	f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
	original, report := firstDispatch(t, f)
	previous := dispatchWaitFor
	waited := false
	dispatchWaitFor = func(agents []string, sd string, ctx *core.Config, env platform.Env, timeout float64, any bool, cwd string) int {
		waited = true
		// Complete the report only after the command selected duplicate waiting.
		if err := os.WriteFile(report, []byte("findings: 0 (P0 0, P1 0, P2 0, P3 0) | verdict: pass\n\n- [env] no ports; host: go test ./...\n- [env] no Windows; host: Windows CI\n"), 0600); err != nil {
			t.Fatal(err)
		}
		return previous(agents, sd, ctx, env, timeout, any, cwd)
	}
	t.Cleanup(func() { dispatchWaitFor = previous })
	code, out, stderr := f.run(t, "worker", f.brief, "--timeout", "15000")
	var obj map[string]any
	if err := json.Unmarshal([]byte(out), &obj); err != nil {
		t.Fatalf("out=%s err=%v", out, err)
	}
	if code != 0 || !waited || obj["env"] != 2.0 || obj["verdict_effective"] != "pass" || obj["duplicate_of"] != original || obj["wait_status"] != "done" {
		t.Fatalf("exit=%d waited=%v JSON=%s stderr=%s", code, waited, out, stderr)
	}
	if countDispatchCalls(mustCalls(t, f), "prompt") != 1 {
		t.Fatal("waiting duplicate sent a second prompt")
	}
	t.Logf("exit=%d waited=%v JSON=%s stderr=%s", code, waited, out, stderr)
}

// A fresh waiting dispatch forwards the env count of the settled report, like
// the duplicate path above.
func TestDispatchWaitingFreshEnvCount(t *testing.T) {
	f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
	f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
	previous := dispatchWaitFor
	wrote := 0
	dispatchWaitFor = func(agents []string, sd string, ctx *core.Config, env platform.Env, timeout float64, any bool, cwd string) int {
		raw, err := os.ReadFile(filepath.Join(sd, "last-report-worker"))
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{strings.TrimSpace(string(raw))} {
			if err := os.WriteFile(p, []byte("findings: 0 (P0 0, P1 0, P2 0, P3 0) | verdict: pass\n\n- [env] no ports; host: go test ./...\n- [env] no Windows; host: Windows CI\n"), 0600); err != nil {
				t.Fatal(err)
			}
			wrote++
		}
		return previous(agents, sd, ctx, env, timeout, any, cwd)
	}
	t.Cleanup(func() { dispatchWaitFor = previous })
	code, out, stderr := f.run(t, "worker", f.brief, "--timeout", "15000")
	var obj map[string]any
	if err := json.Unmarshal([]byte(out), &obj); err != nil {
		t.Fatalf("out=%s err=%v", out, err)
	}
	if code != 0 || wrote != 1 || obj["env"] != 2.0 || obj["verdict_effective"] != "pass" || obj["wait_status"] != "done" {
		t.Fatalf("exit=%d wrote=%d JSON=%s stderr=%s", code, wrote, out, stderr)
	}
}
