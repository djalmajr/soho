package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/provider"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestDispatchProviderCursorPlan(t *testing.T) {
	for _, message := range []struct{ name, line string }{
		{"plain", "Error: Model unavailable on Plus. Plus includes Composer 2.5, Grok 4.7, Grok 4.6, and Grok 4.5 with fixed settings"},
		{"pipe", "Error: Model unavailable on Plus. Available models: Composer | Grok"},
	} {
		for _, noWait := range []bool{false, true} {
			name := "wait"
			if noWait {
				name = "no-wait"
			}
			t.Run(message.name+"/"+name, func(t *testing.T) {
				line := message.line
				f := newR11Fixture(t, r11Case{initial: "Welcome to the worker", post: line, recent: line, mode: "idle", check: "1", authScreen: true, cursorPlan: true})
				for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME"} {
					f.env[key] = f.root
				}
				f.env["SOHO_WAIT_POLL_MS"] = "1"
				f.env["SOHO_PROVIDER_RETRIES"] = "3"
				f.env["SOHO_PROVIDER_RETRY_DELAY"] = "0"
				args := []string{"worker", f.brief, "--timeout", "3000"}
				if noWait {
					args = append(args, "--no-wait")
				}
				code, out, stderr := f.run(t, args...)
				var got map[string]any
				if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &got); err != nil {
					t.Fatalf("code=%d out=%q stderr=%q: %v", code, out, stderr, err)
				}
				if code != 14 || got["wait_status"] != "provider-error" || got["cause"] != line {
					t.Fatalf("code=%d out=%q stderr=%q, want provider-error/14 and %q", code, out, stderr, line)
				}
				calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
				if err != nil {
					t.Fatal(err)
				}
				if countDispatchCalls(calls, "prompt") != 1 || countDispatchCalls(calls, "send-keys") != 0 {
					t.Fatalf("unexpected retry: %#v", calls)
				}
			})
		}
	}
}

func TestDispatchProviderPlanAfterReceipt(t *testing.T) {
	for _, line := range []string{"Error: Model unavailable on Pro", "Error: Model unavailable on Plus. Available models: Composer | Grok"} {
		t.Run(line, func(t *testing.T) {
			// The existing receipt fixture models an accepted turn followed by idle.
			// This covers the no-wait observation after arrival, separately from the
			// Cursor startup reads that take the pre-resend refusal path above.
			verifyR11Case(t, r11Case{initial: "Welcome to the worker", post: line, recent: line, mode: "idle", check: "1", authScreen: true, noWait: true, exit: 14, status: "provider-error", cause: line})

		})
	}
}

const harnessModulePost = "Error: Cannot find module\n" +
	" '/private/tmp/hs-go/orch/pi-broken/dist/bundle/chunks/openai-completions-JXDDPZ23.js' imported\n" +
	" from /private/tmp/hs-go/orch/pi-broken/dist/bundle/chunks/chunk-3YAHQSW6.js\n" +
	" If this looks like a pi bug, /bug sends a report to the developers.\n" +
	"────────────────────────────────────────────────────────────────────────────────────────────────────────\n" +
	"────────────────────────────────────────────────────────────────────────────────────────────────────────\n" +
	"/private/tmp/hs-go/orch\n" +
	"3.2%/262k (auto)                                                          (prov) model-x • low"

func TestDispatchDoneHarnessModuleEndsProviderError(t *testing.T) { // JS: "dispatch: a done pi with a harness module error ends provider-error, never not-received"
	// A done pi that cannot load one of its own files (the brief path still
	// visible in its scrollback, outside the input): the dispatch ends
	// provider-error with the harness message, exit 14, the prompt sent once.
	tc := r11Case{
		title:      "dispatch: a done pi with a harness module error ends provider-error",
		initial:    "Welcome to the worker",
		post:       harnessModulePost,
		recent:     harnessModulePost,
		mode:       "done",
		check:      "1",
		timeout:    "5000",
		exit:       14,
		status:     "provider-error",
		cause:      provider.HarnessModuleCause,
		authScreen: true,
	}
	f := newR11Fixture(t, tc)
	f.env["SOHO_WAIT_POLL_MS"] = "1"
	code, stdout, stderr := f.run(t, "worker", f.brief, "--timeout", tc.timeout)
	var got map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &got); err != nil {
		t.Fatalf("code=%d stderr=%q output=%q: %v", code, stderr, stdout, err)
	}
	if code != tc.exit {
		t.Fatalf("code=%d, want %d; stderr=%q", code, tc.exit, stderr)
	}
	if got["wait_status"] != "provider-error" {
		t.Fatalf("wait_status=%v, want provider-error; stderr=%q", got["wait_status"], stderr)
	}
	if got["cause"] != provider.HarnessModuleCause {
		t.Fatalf("cause=%v, want exactly %q", got["cause"], provider.HarnessModuleCause)
	}
	if got["report_exists"] != false {
		t.Fatalf("report_exists=%v, want false", got["report_exists"])
	}
	wantMessage := "agent 'worker' stopped on a harness error: its CLI could not load one of its own files (Error: Cannot find module), often after the CLI was updated while it ran. It is idle without a report; restart it (release --close, then spawn --fresh) and resend the brief."
	if !strings.Contains(stderr, wantMessage) {
		t.Fatalf("stderr=%q, want the harness message", stderr)
	}
	if strings.Contains(stderr, "stopped on a provider error:") {
		t.Fatalf("stderr=%q, the generic provider message must not replace the harness one", stderr)
	}
	if _, err := os.Stat(filepath.Join(f.state, "ws", "wait", "worker.not-received")); !os.IsNotExist(err) {
		t.Fatalf("not-received marker written: err=%v", err)
	}
	value, err := jsonjs.Parse([]byte(strings.TrimSpace(stdout)))
	if err != nil {
		t.Fatalf("dispatch JSON parse: %v", err)
	}
	obj, ok := value.(*jsonjs.Object)
	if !ok {
		t.Fatalf("dispatch JSON has type %T", value)
	}
	wantKeys := []string{"wait_status", "agent", "role", "kind", "composed_prompt", "report", "task_report", "report_exists", "auto_approved", "lane", "model", "cause"}
	if !reflect.DeepEqual(obj.Keys(), wantKeys) {
		t.Fatalf("JSON keys=%v want=%v", obj.Keys(), wantKeys)
	}
	calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	prompts, enters := countDispatchCalls(calls, "prompt"), countDispatchCalls(calls, "send-keys")
	if prompts != 1 {
		t.Fatalf("prompts=%d, want 1 (no resend): %#v", prompts, calls)
	}
	if enters != 0 {
		t.Fatalf("enters=%d, want 0: %#v", enters, calls)
	}
}
