package wait

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestWaitShortTimeoutWarnsOnceAndProceeds(t *testing.T) { // --timeout 45 means 45 ms: one warning with the seconds fix, then the wait loop still runs
	f := newQueuedProbeFixture(t, "working", "5", "Busy on the task 30%\n", nil)
	f.env["SOHO_DIR"] = filepath.Dir(f.sd)
	f.env["HERDR_WORKSPACE_ID"] = filepath.Base(f.sd)
	SetFrictionLogFile(filepath.Join(f.sd, "friction.log"))
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, stderr bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &stderr
	t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
	code := CmdWait([]string{"worker", "--timeout", "45"}, f.ctx, f.env, "")
	if code != 9 || !strings.Contains(out.String(), `"status":"timeout"`) {
		t.Fatalf("code=%d out=%q stderr=%q; want the 45 ms wait loop running until its timeout", code, out.String(), stderr.String())
	}
	if n := strings.Count(stderr.String(), "wait: --timeout is in milliseconds; 45 is under a second (for 45 seconds pass 45000)"); n != 1 {
		t.Fatalf("warning count=%d stderr=%q", n, stderr.String())
	}
	log, err := os.ReadFile(filepath.Join(f.sd, "friction.log"))
	if err != nil || !strings.Contains(string(log), "warning\twait\twait: --timeout is in milliseconds; 45 is under a second (for 45 seconds pass 45000)") {
		t.Fatalf("friction log=%q err=%v", log, err)
	}
}

func TestWaitUnitlessTimeoutGuidance(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		ms   int64
		warn bool
	}{
		{"1", 1, true}, {"999", 999, true}, {"1000", 1000, true},
		{"1800", 1800, true}, {"9999", 9999, true}, {"10000", 10000, false},
		{"1800ms", 1800, false}, {"30m", 1800000, false}, {"1800000", 1800000, false},
		{"2s", 2000, false}, {"2h", 7200000, false}, {"0", 0, false},
		{"omitted", 1800, false},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			f := newQueuedProbeFixture(t, "working", "5", "Busy on the task 30%\n", nil)
			base := filepath.Dir(filepath.Dir(f.sd))
			f.env["HOME"], f.env["USERPROFILE"] = filepath.Join(base, "home"), filepath.Join(base, "home")
			f.env["XDG_CONFIG_HOME"], f.env["TMPDIR"] = filepath.Join(base, "config"), base
			f.env["SOHO_ROLES"] = filepath.Join(base, "roles")
			if err := os.MkdirAll(f.env["SOHO_ROLES"], 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(f.env["SOHO_ROLES"], "implementer.md"), []byte("---\nname: Implementer\nmode: edit\n---\n"), 0600); err != nil {
				t.Fatal(err)
			}
			f.env["SOHO_DISPATCH_TIMEOUT"] = "1800"
			oldNow, oldFriction, oldOut, oldErr := platform.Now, frictionLogFile, platform.Stdout, platform.Stderr
			core.SetFrictionCommand("wait")
			SetFrictionLogFile(filepath.Join(f.sd, "friction.log"))
			// First probe: one millisecond before the expected deadline. Second
			// probe: exactly on it. A rescaled budget fails after two probes.
			platform.Now = func() time.Time {
				calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
				if err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				probes := 0
				for _, call := range calls {
					if len(call.Argv) >= 2 && call.Argv[0] == "agent" && call.Argv[1] == "get" {
						probes++
					}
				}
				var elapsed int64
				switch probes {
				case 0:
				case 1:
					elapsed = max(0, tc.ms-1)
				case 2:
					elapsed = tc.ms
				default:
					t.Fatalf("--timeout %s did not expire at %dms (probes=%d)", tc.raw, tc.ms, probes)
				}
				return time.Unix(1700000000, 0).Add(time.Duration(elapsed) * time.Millisecond)
			}
			t.Cleanup(func() {
				platform.Now, platform.Stdout, platform.Stderr = oldNow, oldOut, oldErr
				SetFrictionLogFile(oldFriction)
				core.SetFrictionCommand("")
			})
			args := []string{"worker"}
			if tc.raw != "omitted" {
				args = append(args, "--timeout", tc.raw)
			}
			code, out, stderr := runCmdWait(t, f, args)
			var result struct {
				Status  string `json:"status"`
				Elapsed int64  `json:"elapsed_ms"`
			}
			if err := json.Unmarshal([]byte(out), &result); err != nil || code != 9 || result.Status != "timeout" || result.Elapsed != tc.ms {
				t.Fatalf("raw=%q code=%d observed=%+v out=%q stderr=%q err=%v", tc.raw, code, result, out, stderr, err)
			}
			want := 0
			if tc.warn {
				want = 1
			}
			log, _ := os.ReadFile(filepath.Join(f.sd, "friction.log"))
			for name, text := range map[string]string{"stderr": stderr, "friction": string(log)} {
				if got := strings.Count(text, "wait: --timeout is in milliseconds;"); got != want {
					t.Fatalf("%s warning count=%d, want %d: %q", name, got, want, text)
				}
			}
			if tc.raw == "1800" && (!strings.Contains(stderr, "1800 means 1800ms") || !strings.Contains(stderr, "30m") || strings.Contains(stderr, "under a second")) {
				t.Fatalf("inaccurate advisory: %q", stderr)
			}
		})
	}
}

func TestWaitInvalidTimeoutHasNoAdvisory(t *testing.T) {
	for _, raw := range []string{"", "-1", "1h30m", " 1800", "1.8s", "1800MS", "9223372036854775808", "2562047788016h"} {
		t.Run(raw, func(t *testing.T) {
			f := newQueuedProbeFixture(t, "working", "5", "Busy on the task 30%\n", nil)
			oldOut, oldErr := platform.Stdout, platform.Stderr
			var out, stderr bytes.Buffer
			platform.Stdout, platform.Stderr = &out, &stderr
			t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
			var exit *platform.ExitError
			func() {
				defer func() {
					if value := recover(); value != nil {
						var ok bool
						exit, ok = value.(*platform.ExitError)
						if !ok {
							panic(value)
						}
					}
				}()
				CmdWait([]string{"worker", "--timeout", raw}, f.ctx, f.env, "")
			}()
			if exit == nil || exit.Code != 2 || !strings.Contains(exit.Msg, "expects milliseconds or a duration") || out.Len() != 0 || strings.Contains(stderr.String(), "--timeout is in milliseconds;") {
				t.Fatalf("raw=%q exit=%+v out=%q stderr=%q", raw, exit, out.String(), stderr.String())
			}
			if calls := f.calls(); len(calls) != 0 {
				t.Fatalf("invalid timeout made calls=%v", calls)
			}
		})
	}
}
