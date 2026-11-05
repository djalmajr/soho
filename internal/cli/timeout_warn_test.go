package cli

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	waitpkg "github.com/djalmajr/soho/internal/wait"
)

const shortTimeoutWarning45 = ": --timeout is in milliseconds; 45 is under a second (for 45 seconds pass 45000)"

func TestTimeoutWarnOncePerCommand(t *testing.T) {
	t.Run("dispatch: one warning, the dispatch proceeds", func(t *testing.T) {
		h := newTM3bHarness(t, tm3bFullBrief())
		oldFriction := frictionLogPath
		frictionLogPath = filepath.Join(h.state, "ws", "friction.log") // cmdDispatch is called directly, not through Run
		t.Cleanup(func() { frictionLogPath = oldFriction })
		core.SetFrictionCommand("dispatch") // Run would set it; cmdDispatch is called directly here
		code, out, stderr := h.run(t, "build", h.brief, "--timeout", "45", "--no-wait")
		if code != 0 {
			t.Fatalf("code=%d out=%s stderr=%s; want the dispatch to proceed", code, out, stderr)
		}
		if n := strings.Count(stderr, "dispatch"+shortTimeoutWarning45); n != 1 {
			t.Fatalf("warning count=%d stderr=%s", n, stderr)
		}
		if strings.Contains(stderr, "spawn"+shortTimeoutWarning45) {
			t.Fatalf("a spawn warning leaked into dispatch: %s", stderr)
		}
		log, err := os.ReadFile(filepath.Join(h.state, "ws", "friction.log"))
		if err != nil || !strings.Contains(string(log), "warning\tdispatch\tdispatch"+shortTimeoutWarning45) {
			t.Fatalf("friction log=%q err=%v", log, err)
		}
	})
	t.Run("run: one warning (the dispatch's), no spawn warning, the run proceeds", func(t *testing.T) {
		h := newTM3bHarness(t, tm3bFullBrief())
		prepareRunHarness(t, &h)
		code, out, stderr := runForTest(t, h, "--no-wait", "--pane", "w0test:p0b", "--timeout", "45")
		if code != 0 {
			t.Fatalf("code=%d out=%s stderr=%s; want the run to proceed", code, out, stderr)
		}
		if n := strings.Count(stderr, "dispatch"+shortTimeoutWarning45); n != 1 {
			t.Fatalf("dispatch warning count=%d stderr=%s", n, stderr)
		}
		if strings.Contains(stderr, "spawn: --timeout is in milliseconds") {
			t.Fatalf("the run warned through spawn too: %s", stderr)
		}
	})
	t.Run("compact: one warning, the compact runs to its 45 ms timeout", func(t *testing.T) {
		f := newCompactFixture(t, "claude", []fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: compactStateJSON("idle", 1)},
			{Argv: []string{"pane", "send-text", "p1", "/compact"}, Stdout: `{"result":{}}`},
			{Argv: []string{"pane", "send-keys", "p1", "Enter"}, Stdout: `{"result":{}}`},
			{Argv: compactReadArgv("worker"), ArgvPrefix: true, Stdout: compactScreens.noProof},
			// The polls without a proof also check the worker is alive.
			{Argv: []string{"agent", "get", "worker"}, Stdout: compactStateJSON("idle", 2)},
		})
		code, out, errText := f.run(t, "compact", "worker", "--timeout", "45")
		if code != 9 {
			t.Fatalf("code=%d out=%s stderr=%s; want the 45 ms compact timeout", code, out, errText)
		}
		if n := strings.Count(errText, "compact"+shortTimeoutWarning45); n != 1 {
			t.Fatalf("warning count=%d stderr=%s", n, errText)
		}
		log, err := os.ReadFile(filepath.Join(f.root, "state", "ws", "friction.log"))
		if err != nil || !strings.Contains(string(log), "warning\tcompact\tcompact"+shortTimeoutWarning45) {
			t.Fatalf("friction log=%q err=%v", log, err)
		}
	})
}

func TestDispatchUnitlessTimeoutGuidance(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		ms   float64
		warn bool
	}{
		{"1", 1, true}, {"999", 999, true}, {"1000", 1000, true},
		{"1800", 1800, true}, {"9999", 9999, true}, {"10000", 10000, false},
		{"1800ms", 1800, false}, {"30m", 1800000, false}, {"1800000", 1800000, false},
		{"2s", 2000, false}, {"2h", 7200000, false}, {"0", 0, false},
		{"omitted", 1800, false},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
			f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
			f.env["SOHO_DISPATCH_TIMEOUT"] = "1800" // even a short configured default stays quiet
			previous := dispatchWaitFor
			waits := 0
			dispatchWaitFor = func(agents []string, sd string, ctx *core.Config, env platform.Env, timeout float64, any bool, cwd string) int {
				waits++
				if timeout != tc.ms {
					t.Fatalf("--timeout %s forwarded %vms, want %vms", tc.raw, timeout, tc.ms)
				}
				report := strings.TrimSpace(mustRead(t, filepath.Join(sd, "last-report-worker")))
				if err := os.WriteFile(report, []byte("Completed.\n"), 0600); err != nil {
					t.Fatal(err)
				}
				// Observe and settle the completed report before applying even a
				// zero/1ms budget; report stability itself requires two samples.
				if got := waitpkg.ProbeAgent(sd, "worker", report, ctx, env); got != "pending" {
					t.Fatalf("first report sample=%q, want pending", got)
				}
				if got := waitpkg.ProbeAgent(sd, "worker", report, ctx, env); got != "done" {
					t.Fatalf("settled report=%q, want done", got)
				}
				return previous(agents, sd, ctx, env, timeout, any, cwd)
			}
			t.Cleanup(func() { dispatchWaitFor = previous })
			args := []string{"worker", f.brief}
			if tc.raw != "omitted" {
				args = append(args, "--timeout", tc.raw)
			}
			code, out, stderr := f.run(t, args...)
			if code != 0 || dispatchOutputStatus(t, out) != "done" || waits != 1 || strings.Count(strings.TrimSpace(out), "\n") != 0 {
				t.Fatalf("code=%d waits=%d out=%q stderr=%q", code, waits, out, stderr)
			}
			want := 0
			if tc.warn {
				want = 1
			}
			log, _ := os.ReadFile(filepath.Join(f.state, "ws", "friction.log"))
			for name, text := range map[string]string{"stderr": stderr, "friction": string(log)} {
				if got := strings.Count(text, "dispatch: --timeout is in milliseconds;"); got != want {
					t.Fatalf("%s warning count=%d, want %d: %q", name, got, want, text)
				}
			}
			if tc.raw == "1800" && (!strings.Contains(stderr, "1800 means 1800ms") || !strings.Contains(stderr, "30m") || strings.Contains(stderr, "under a second")) {
				t.Fatalf("inaccurate advisory: %q", stderr)
			}
		})
	}
}

func TestDispatchInvalidTimeoutHasNoAdvisory(t *testing.T) {
	for _, raw := range []string{"", "-1", "1h30m", " 1800", "1.8s", "1800MS", "9223372036854775808", "2562047788016h"} {
		t.Run(raw, func(t *testing.T) {
			f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
			code, out, stderr := f.run(t, "worker", f.brief, "--timeout", raw)
			if code != 2 || out != "" || !strings.Contains(stderr, timeoutDurationPhrase) || strings.Contains(stderr, "--timeout is in milliseconds;") {
				t.Fatalf("raw=%q code=%d out=%q stderr=%q", raw, code, out, stderr)
			}
			if calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl")); !os.IsNotExist(err) && (err != nil || len(calls) != 0) {
				t.Fatalf("invalid timeout made calls=%v err=%v", calls, err)
			}
		})
	}
}

func TestRunUnitlessTimeoutGuidanceOnce(t *testing.T) {
	for _, raw := range []string{"1800", "30m", "1800000"} {
		t.Run(raw, func(t *testing.T) {
			h := newTM3bHarness(t, tm3bFullBrief())
			prepareRunHarness(t, &h)
			h.env["TMPDIR"] = h.root
			code, out, stderr := runForTest(t, h, "--no-wait", "--pane", "w0test:p0b", "--timeout", raw)
			decoder := json.NewDecoder(strings.NewReader(out))
			var spawned struct {
				Name   string `json:"name"`
				PaneID string `json:"pane_id"`
				Kind   string `json:"kind"`
				Role   string `json:"role"`
				Status string `json:"status"`
			}
			if err := decoder.Decode(&spawned); err != nil {
				t.Fatalf("first run document (spawn): %v; out=%q", err, out)
			}
			if spawned.Name != "implementer" || spawned.PaneID != "w0test:p0b" || spawned.Kind != "grok" || spawned.Role != "implementer" || spawned.Status != "ready" {
				t.Fatalf("first run document=%+v, want the ready implementer spawn in w0test:p0b; out=%q", spawned, out)
			}
			var dispatched struct {
				Agent      string `json:"agent"`
				Role       string `json:"role"`
				Kind       string `json:"kind"`
				WaitStatus string `json:"wait_status"`
			}
			if err := decoder.Decode(&dispatched); err != nil {
				t.Fatalf("second run document (dispatch): %v; out=%q", err, out)
			}
			if dispatched.Agent != "implementer" || dispatched.Role != "implementer" || dispatched.Kind != "grok" || dispatched.WaitStatus != "submitted" {
				t.Fatalf("second run document=%+v, want the submitted implementer dispatch; out=%q", dispatched, out)
			}
			var extra json.RawMessage
			if err := decoder.Decode(&extra); err != io.EOF {
				t.Fatalf("run stdout must end after two JSON objects: extra=%q err=%v out=%q", extra, err, out)
			}
			want := 0
			if raw == "1800" {
				want = 1
			}
			if code != 0 || strings.Count(stderr, "--timeout is in milliseconds;") != want || strings.Contains(stderr, "spawn: --timeout is in milliseconds;") {
				t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
			}
			log, _ := os.ReadFile(filepath.Join(h.ws, "friction.log"))
			if strings.Count(string(log), "--timeout is in milliseconds;") != want {
				t.Fatalf("friction log=%q, want %d advisories", log, want)
			}
		})
	}
}
