package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDispatchProviderErrorResendHintAndDuplicateGuard(t *testing.T) {
	const cause = "Error: 401 Unauthorized: Incorrect API key provided"
	f := newDispatchArrivalFixture(t, "idle", 1, 2, cause+"\n", "0")
	f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
	f.env["SOHO_WAIT_POLL_MS"] = "1"
	// The hint must use literal placeholders even for a path with shell syntax.
	f.brief = filepath.Join(f.root, "brief ' $(unused).md")
	if err := os.WriteFile(f.brief, []byte(tm3bFullBrief()), 0600); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := f.run(t, "worker", f.brief, "--timeout", "1s")
	if code != 14 || dispatchOutputStatus(t, out) != "provider-error" || !strings.Contains(out, `"cause":"`+cause+`"`) || !strings.Contains(out, `"auto_approved":0`) || !strings.Contains(out, `"report_exists":false`) {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	const hint = "soho dispatch <agent> <brief> --resend"
	if strings.Count(stderr, hint) != 1 || !strings.Contains(stderr, "ask the user whether to resend the brief, switch the assistant, or wait") || strings.Contains(stderr, f.brief) {
		t.Fatalf("provider warning must retain the choices and one safe explicit command: %q", stderr)
	}
	log, err := os.ReadFile(filepath.Join(f.state, "ws", "friction.log"))
	if err != nil || strings.Count(string(log), hint) != 1 {
		t.Fatalf("friction=%q err=%v", log, err)
	}
	if got := countDispatchCalls(mustCalls(t, f), "prompt"); got != 1 {
		t.Fatalf("provider error sent %d prompts, want one", got)
	}
	code, duplicate, stderr := f.run(t, "worker", f.brief, "--no-wait")
	if code != 0 || !strings.Contains(duplicate, `"duplicate_of":`) || countDispatchCalls(mustCalls(t, f), "prompt") != 1 {
		t.Fatalf("duplicate resent the prompt: code=%d out=%q stderr=%q", code, duplicate, stderr)
	}
	code, resent, stderr := f.run(t, "worker", f.brief, "--no-wait", "--resend")
	if code != 0 || dispatchOutputStatus(t, resent) != "submitted" || strings.Contains(resent, `"duplicate_of":`) || countDispatchCalls(mustCalls(t, f), "prompt") != 2 {
		t.Fatalf("explicit --resend did not send exactly one further prompt: code=%d out=%q stderr=%q", code, resent, stderr)
	}
}
