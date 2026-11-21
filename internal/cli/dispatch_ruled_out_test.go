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

func ruledOutDispatchBody(withSection bool) string {
	body := "findings: 0 (P0 0, P1 0, P2 0, P3 0) | verdict: pass\n\n"
	if withSection {
		body += "## Ruled out\n\n- checked cache\n- checked fixture\n"
	}
	return body
}

func assertDispatchRuledOut(t *testing.T, out string, obj map[string]any, withSection bool) {
	t.Helper()
	value, present := obj["ruled_out"]
	if withSection {
		if !present || value != 2.0 || !strings.Contains(out, `"ruled_out":2`) {
			t.Fatalf("missing ruled_out in dispatch output: %s", out)
		}
		return
	}
	if present {
		t.Fatalf("ruled_out invented for a report without the section: %s", out)
	}
}

func parseDispatchRuledOut(t *testing.T, out string) map[string]any {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal([]byte(out), &obj); err != nil {
		t.Fatalf("out=%s err=%v", out, err)
	}
	return obj
}

func TestDispatchWaitingFreshRuledOutCount(t *testing.T) {
	for _, tc := range []struct {
		name        string
		withSection bool
	}{
		{"with section", true},
		{"without section", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
					if err := os.WriteFile(p, []byte(ruledOutDispatchBody(tc.withSection)), 0600); err != nil {
						t.Fatal(err)
					}
					wrote++
				}
				return previous(agents, sd, ctx, env, timeout, any, cwd)
			}
			t.Cleanup(func() { dispatchWaitFor = previous })
			code, out, stderr := f.run(t, "worker", f.brief, "--timeout", "15000")
			obj := parseDispatchRuledOut(t, out)
			if code != 0 || wrote != 1 || obj["wait_status"] != "done" || obj["verdict"] != "pass" {
				t.Fatalf("exit=%d wrote=%d JSON=%s stderr=%s", code, wrote, out, stderr)
			}
			assertDispatchRuledOut(t, out, obj, tc.withSection)
		})
	}
}

func TestDispatchWaitingFreshRuledOutDiscardedMarkersStayQuiet(t *testing.T) {
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
			body := "findings: 0 (P0 0, P1 0, P2 0, P3 0) | verdict: pass\n\n## Ruled out\n\n" +
				"- [partial] discarded state\n" +
				"- [env] no host; host: go test\n" +
				"- [F2] [origin: impl] [class: x] discarded evidence\n"
			if err := os.WriteFile(p, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			wrote++
		}
		return previous(agents, sd, ctx, env, timeout, any, cwd)
	}
	t.Cleanup(func() { dispatchWaitFor = previous })
	code, out, stderr := f.run(t, "worker", f.brief, "--timeout", "15000")
	obj := parseDispatchRuledOut(t, out)
	if code != 0 || wrote != 1 || obj["ruled_out"] != 3.0 || obj["verdict_effective"] != "pass" {
		t.Fatalf("exit=%d wrote=%d JSON=%s stderr=%s", code, wrote, out, stderr)
	}
	for _, key := range []string{"partial", "env", "finding_marks"} {
		if _, ok := obj[key]; ok {
			t.Fatalf("ruled-out %s leaked into dispatch: %s", key, out)
		}
	}
}

func TestDispatchWaitingDuplicateRuledOutCount(t *testing.T) {
	for _, tc := range []struct {
		name        string
		withSection bool
	}{
		{"with section", true},
		{"without section", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
			original, report := firstDispatch(t, f)
			previous := dispatchWaitFor
			waited := false
			dispatchWaitFor = func(agents []string, sd string, ctx *core.Config, env platform.Env, timeout float64, any bool, cwd string) int {
				waited = true
				if err := os.WriteFile(report, []byte(ruledOutDispatchBody(tc.withSection)), 0600); err != nil {
					t.Fatal(err)
				}
				return previous(agents, sd, ctx, env, timeout, any, cwd)
			}
			t.Cleanup(func() { dispatchWaitFor = previous })
			code, out, stderr := f.run(t, "worker", f.brief, "--timeout", "15000")
			obj := parseDispatchRuledOut(t, out)
			if code != 0 || !waited || obj["wait_status"] != "done" || obj["duplicate_of"] != original {
				t.Fatalf("exit=%d waited=%v JSON=%s stderr=%s", code, waited, out, stderr)
			}
			assertDispatchRuledOut(t, out, obj, tc.withSection)
		})
	}
}
