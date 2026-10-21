package cli

import (
	"encoding/json"
	"github.com/djalmajr/soho/internal/peerauth"
	"strings"
	"testing"
)

func runCodexHook(t *testing.T, h *hookFix, prompt string) (int, string, string) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"cwd": h.cwd, "hook_event_name": "UserPromptSubmit", "model": "fixture-model", "permission_mode": "default", "prompt": prompt, "session_id": "fixture-session", "transcript_path": "unused.jsonl", "turn_id": "fixture-turn"})
	if err != nil {
		t.Fatal(err)
	}
	return runCodexHookInput(t, h, string(raw))
}
func runCodexHookInput(t *testing.T, h *hookFix, input string) (int, string, string) {
	t.Helper()
	old := hookStdin
	hookStdin = strings.NewReader(input)
	defer func() { hookStdin = old }()
	return runIn(t, []string{"hook", "inbound", "--codex"}, h.env, h.cwd)
}
func TestHookCodexInboundBlockJSON(t *testing.T) {
	h := newHookFix(t, "enforce", "allowed")
	id := nextID()
	message := remoteMessage(t, id, "denied", h.key("denied"))
	code, out, _ := runCodexHook(t, h, message)
	reason := "[soho:peer-refused] #" + id + " refused-not-allowed\nThe message did not reach the model; nothing needs clearing."
	raw, _ := json.Marshal(map[string]string{"decision": "block", "reason": reason})
	if code != 0 || out != string(raw)+"\n" {
		t.Fatalf("code=%d output=%q want=%s", code, out, raw)
	}
	var block map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &block); err != nil || len(block) != 2 || block["hookSpecificOutput"] != nil {
		t.Fatalf("invalid Codex block: %s (%v)", out, err)
	}
}
func TestHookCodexInboundPasses(t *testing.T) {
	for _, name := range []string{"valid", "plain", "auth off"} {
		t.Run(name, func(t *testing.T) {
			mode := "enforce"
			if name == "auth off" {
				mode = "off"
			}
			h := newHookFix(t, mode)
			message := "ordinary prompt"
			if name == "valid" {
				message = localMessage(t, nextID(), h.key("local"))
			}
			if name == "auth off" {
				message = localMessage(t, nextID(), nil)
			}
			code, out, stderr := runCodexHook(t, h, message)
			if code != 0 || out != "" || stderr != "" {
				t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
			}
		})
	}
}
func TestHookCodexInboundProjectOffMatchesClaude(t *testing.T) {
	h := newHookFix(t, "off")
	h.env["SOHO_INBOUND"] = "off"
	id := nextID()
	message := localMessage(t, id, nil)
	code, out, _ := runCodexHook(t, h, message)
	if code != 0 || !strings.Contains(out, string(peerauth.RefusedInboundOff)) {
		t.Fatalf("project off: code=%d out=%s", code, out)
	}
	_, claude, _ := h.run("inbound", message)
	if !strings.Contains(claude, string(peerauth.RefusedInboundOff)) {
		t.Fatalf("Claude control: %s", claude)
	}
}
func TestHookCodexInboundTooLarge(t *testing.T) {
	h := newHookFix(t, "enforce")
	code, out, _ := runCodexHookInput(t, h, strings.Repeat("x", hookInputLimit+1))
	want := map[string]string{"decision": "block", "reason": "[soho:peer-refused] prompt-too-large\nThe prompt is larger than 4 MiB, so its peer messages could not be checked; send it in smaller parts.\nThe message did not reach the model; nothing needs clearing."}
	raw, _ := json.Marshal(want)
	if code != 0 || out != string(raw)+"\n" {
		t.Fatalf("code=%d out=%q", code, out)
	}
}
