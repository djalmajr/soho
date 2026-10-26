package cli

import (
	"encoding/json"
	"github.com/djalmajr/soho/internal/provider"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	"path/filepath"
	"strings"
	"testing"
)

func TestProviderRefusalCommands(t *testing.T) {
	for _, tc := range []struct{ name, state, screen, status, cause string }{
		{"refusal", "idle", "■ We take extra care with some cybersecurity requests", "provider-refusal", "We take extra care with some cybersecurity requests"},
		{"refusal blocked", "blocked", "■ We take extra care with some cybersecurity requests", "provider-refusal", "We take extra care with some cybersecurity requests"},
		{"lost", "working", "■ Server connection could not be restored", "provider-error", provider.LostConnectionCause},
	} {
		for _, cmd := range []string{"wait", "status"} {
			t.Run(tc.name+"/"+cmd, func(t *testing.T) {
				row := "worker\tp1\tcodex\timplementer\topenai\t1\t/work\tnow\tmodel-x\tfull\timplementer\tbuild\n"
				f := newWaitStatusFixture(t, row, []fakecli.Rule{
					{Argv: []string{"agent", "read", "worker", "--source", "visible"}, Stdout: tc.screen},
					{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"` + tc.state + `"}}}`},
					{Argv: []string{"agent", "read", "worker", "--source", "recent-unwrapped", "--lines", "40"}, Stdout: tc.screen},
				})
				args := []string{cmd, "worker"}
				if cmd == "wait" {
					args = append(args, "--timeout", "3000")
				}
				rc, out, stderr := f.runCommand(t, args...)
				var got map[string]any
				if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &got); err != nil {
					t.Fatalf("exit=%d out=%q stderr=%q err=%v", rc, out, stderr, err)
				}
				for key, want := range map[string]any{"status": tc.status, "cause": tc.cause, "lane": "build", "kind": "codex", "model": "model-x"} {
					if got[key] != want {
						t.Errorf("%s=%v want=%v output=%s", key, got[key], want, out)
					}
				}
				if rc != 14 {
					t.Fatalf("exit=%d output=%s", rc, out)
				}
			})
		}
	}
}

func TestProviderRefusalDispatch(t *testing.T) {
	for _, tc := range []struct{ name, state, line, status, cause string }{
		{"refusal", "idle", "■ We take extra care with some cybersecurity requests", "provider-refusal", "We take extra care with some cybersecurity requests"},
		{"lost working", "working", "■ Server connection could not be restored", "provider-error", provider.LostConnectionCause},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newR11Fixture(t, r11Case{initial: "Welcome to the worker", post: tc.line, recent: tc.line, mode: tc.state, check: "1", authScreen: true})
			f.env["SOHO_WAIT_POLL_MS"] = "1"
			rc, out, stderr := f.run(t, "worker", f.brief, "--timeout", "3000")
			var got map[string]any
			if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &got); err != nil {
				t.Fatalf("exit=%d out=%q stderr=%q err=%v", rc, out, stderr, err)
			}
			if rc != 14 || got["wait_status"] != tc.status || got["cause"] != tc.cause {
				t.Fatalf("exit=%d out=%q stderr=%q", rc, out, stderr)
			}
			for _, key := range []string{"lane", "kind", "model"} {
				if _, ok := got[key]; !ok {
					t.Errorf("missing %s: %s", key, out)
				}
			}
			calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			if countDispatchCalls(calls, "prompt") != 1 || countDispatchCalls(calls, "send-keys") != 0 {
				t.Fatalf("retry: %#v", calls)
			}
		})
	}
}

func TestProviderRefusalArrival(t *testing.T) {
	for _, line := range []string{"■ We take extra care with some cybersecurity requests", "■ Server connection could not be restored"} {
		t.Run(line, func(t *testing.T) {
			// No new state sequence acknowledges receipt; the terminal screen
			// must end dispatch before any composer recovery can resend it.
			f := newR11Fixture(t, r11Case{initial: line, mode: "idle", check: "1", cursorPlan: true})
			rc, out, stderr := f.run(t, "worker", f.brief, "--timeout", "3000")
			want := "provider-refusal"
			if strings.Contains(line, "Server connection") {
				want = "provider-error"
			}
			var got map[string]any
			if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &got); err != nil {
				t.Fatal(err)
			}
			if rc != 14 || got["wait_status"] != want || got["cause"] == nil {
				t.Fatalf("exit=%d out=%s stderr=%s", rc, out, stderr)
			}
			calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			if countDispatchCalls(calls, "prompt") != 1 || countDispatchCalls(calls, "send-keys") != 0 {
				t.Fatalf("retry: %#v", calls)
			}
		})
	}
}
