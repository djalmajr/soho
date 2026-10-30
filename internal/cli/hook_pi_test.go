package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/peerauth"
)

func runPiHook(t *testing.T, h *hookFix, prompt string) (int, string, string) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"cwd": h.cwd, "hook_event_name": "UserPromptSubmit", "model": "fixture-model", "permission_mode": "default", "prompt": prompt, "session_id": "fixture-session", "transcript_path": "unused.jsonl", "turn_id": "fixture-turn"})
	if err != nil {
		t.Fatal(err)
	}
	return runPiHookInput(t, h, string(raw))
}
func runPiHookInput(t *testing.T, h *hookFix, input string) (int, string, string) {
	t.Helper()
	old := hookStdin
	hookStdin = strings.NewReader(input)
	defer func() { hookStdin = old }()
	return runIn(t, []string{"hook", "inbound", "--pi"}, h.env, h.cwd)
}
func TestHookPiInboundProjectOffBlocks(t *testing.T) {
	h := newHookFix(t)
	h.projectOff()
	id := nextID()
	message := localMessage(id)
	code, out, _ := runPiHook(t, h, message)
	reason := refusedLine(id, peerauth.RefusedInboundOff) + "\nThe message did not reach the model; nothing needs clearing."
	raw, _ := json.Marshal(map[string]string{"decision": "block", "reason": reason})
	if code != 0 || out != string(raw)+"\n" {
		t.Fatalf("code=%d output=%q want=%s", code, out, raw)
	}
	var block map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &block); err != nil || len(block) != 2 || block["hookSpecificOutput"] != nil {
		t.Fatalf("invalid Pi block: %s (%v)", out, err)
	}
}
func TestHookPiInboundPasses(t *testing.T) {
	for _, name := range []string{"peer message", "plain"} {
		t.Run(name, func(t *testing.T) {
			h := newHookFix(t)
			message := "ordinary prompt"
			if name == "peer message" {
				message = localMessage(nextID())
			}
			code, out, stderr := runPiHook(t, h, message)
			if code != 0 || out != "" || stderr != "" {
				t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
			}
		})
	}
}
func TestHookPiInboundProjectOffMatchesClaude(t *testing.T) {
	h := newHookFix(t)
	h.env["SOHO_INBOUND"] = "off"
	id := nextID()
	message := localMessage(id)
	code, out, _ := runPiHook(t, h, message)
	if code != 0 || !strings.Contains(out, string(peerauth.RefusedInboundOff)) {
		t.Fatalf("project off: code=%d out=%s", code, out)
	}
	_, claude, _ := h.run("inbound", message)
	if !strings.Contains(claude, string(peerauth.RefusedInboundOff)) {
		t.Fatalf("Claude control: %s", claude)
	}
}
func TestHookPiInboundTooLarge(t *testing.T) {
	h := newHookFix(t)
	code, out, _ := runPiHookInput(t, h, strings.Repeat("x", hookInputLimit+1))
	want := map[string]string{"decision": "block", "reason": "[soho:peer-refused] prompt-too-large\nThe prompt is larger than 4 MiB, so its peer messages could not be checked; send it in smaller parts.\nThe message did not reach the model; nothing needs clearing."}
	raw, _ := json.Marshal(want)
	if code != 0 || out != string(raw)+"\n" {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

func TestHookPiExclusiveFlags(t *testing.T) {
	h := newHookFix(t)
	for _, args := range [][]string{{"hook", "inbound", "--codex", "--pi"}, {"hook", "inbound", "--pi", "--codex"}, {"hook", "inbound", "--pi", "extra"}} {
		code, out, stderr := runIn(t, args, h.env, h.cwd)
		if code != 2 || out != "" || !strings.Contains(stderr, "[--codex|--pi]") {
			t.Fatalf("args=%q code=%d out=%q stderr=%q", args, code, out, stderr)
		}
	}
}
