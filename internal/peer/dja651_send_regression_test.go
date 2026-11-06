package peer_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/djalmajr/soho/internal/peer"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

const dja651Session = "11111111-2222-4333-8444-555555555555"

func dja651Isolate(f *fixture) {
	f.env["PATH"] = filepath.Dir(f.bin)
	f.env["USERPROFILE"] = f.env.Get("HOME")
	f.env["CODEX_HOME"] = filepath.Join(f.dir, "codex")
	f.env["SOHO_SEND_WINDOW_MS"], f.env["SOHO_SEND_POLL_MS"] = "1", "1"
}

func dja651StateDir(f *fixture) string {
	return filepath.Join(f.env.Get("SOHO_DIR"), f.env.Get("HERDR_WORKSPACE_ID"))
}

func dja651Log(t *testing.T, stateDir, status, id, from, to string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(stateDir, peer.PeerLogFile))
	if err != nil {
		t.Fatal(err)
	}
	rows := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(rows) != 1 {
		t.Fatalf("want exactly one TSV delivery row, got %q", raw)
	}
	cols := strings.Split(rows[0], "\t")
	if len(cols) < 6 {
		t.Fatalf("incomplete TSV row: %q", raw)
	}
	if cols[1] != from || cols[2] != to || cols[3] != status || cols[5] != id {
		t.Errorf("TSV from=%q to=%q status=%q id=%q; want %q %q %q %q", cols[1], cols[2], cols[3], cols[5], from, to, status, id)
	}
	return cols
}

func dja651Submission(t *testing.T, f *fixture, prompt string, wantEnters int) {
	t.Helper()
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(filepath.Dir(f.bin), "herdr.json"))
	if err != nil {
		t.Fatal(err)
	}
	prompts, enters := 0, 0
	for _, c := range calls {
		for i := 0; i+1 < len(c.Argv); i++ {
			if c.Argv[i] != "agent" {
				continue
			}
			switch c.Argv[i+1] {
			case "prompt":
				prompts++
				if i+3 >= len(c.Argv) || c.Argv[i+3] != prompt {
					t.Errorf("submitted prompt differs: %q", c.Argv)
				}
			case "send-keys":
				if i+3 >= len(c.Argv) || c.Argv[i+3] != "enter" {
					t.Errorf("unexpected key: %q", c.Argv)
				}
				enters++
			}
		}
	}
	if prompts != 1 || enters != wantEnters {
		t.Errorf("prompts=%d enters=%d; want one prompt and %d Enter, without resend", prompts, enters, wantEnters)
	}
}

func dja651Screen(t *testing.T, stateDir, id, screen, out, stderr string, saved bool) {
	t.Helper()
	path := filepath.Join(stateDir, "wait", "send-"+id+".screen")
	if saved {
		raw, err := os.ReadFile(path)
		if err != nil || string(raw) != screen {
			t.Errorf("saved visible screen: bytes=%q err=%v; want %q at %s", raw, err, screen, path)
		}
		if strings.Count(stderr, path) != 1 || strings.Contains(out, path) {
			t.Errorf("diagnostic must report exactly the saved path on stderr: out=%q stderr=%q", out, stderr)
		}
	} else {
		if _, err := os.Stat(path); !os.IsNotExist(err) && !errors.Is(err, syscall.ENOTDIR) {
			t.Errorf("invented screen artifact at %s: %v", path, err)
		}
		if strings.Contains(out+stderr, path) || strings.Contains(out+stderr, "screen saved") || strings.Contains(out+stderr, ".screen") {
			t.Errorf("unavailable screen must name no artifact: out=%q stderr=%q", out, stderr)
		}
	}
	if screen != "" && strings.Contains(out+stderr, screen) {
		t.Errorf("visible screen body leaked to command output: out=%q stderr=%q", out, stderr)
	}
	for _, name := range []string{peer.PeerLogFile, "friction.log"} {
		raw, err := os.ReadFile(filepath.Join(stateDir, name))
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if screen != "" && strings.Contains(string(raw), screen) {
			t.Errorf("visible screen body leaked to %s", name)
		}
	}
}

func dja651PromptRule(prompt string) fakecli.Rule {
	return fakecli.Rule{Argv: []string{"agent", "prompt", "w0test:p0a", prompt, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", "15000"}}
}

func TestDJA651LostBranchesUseUncertain(t *testing.T) {
	for _, afterEnter := range []bool{false, true} {
		name := "without_marker"
		if afterEnter {
			name = "after_one_enter"
		}
		t.Run(name, func(t *testing.T) {
			id, prompt := stalledID(t)
			pre := codexPreScreen(t)
			last := pre
			if afterEnter {
				last = strings.Replace(pre, "› Ask Codex to do anything", "› "+peer.PeerOpenTag(id)+" pending", 1)
			}
			rules := []fakecli.Rule{
				{Argv: []string{"agent", "get", "w0test:p0a"}, Stdout: agentJSONKind("codex", "working", "1")},
				{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Call: 1, Stdout: pre},
				{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Stdout: last},
				{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Stdout: "earlier history without receipt\n"},
				dja651PromptRule(prompt),
				{Argv: []string{"agent", "send-keys", "w0test:p0a", "enter"}},
			}
			f := newFixture(t, rules)
			dja651Isolate(f)
			code, out, stderr := f.run([]string{"send", "w0test:p0a", "--now", "--timeout", "1s", "hello"})
			if code != 15 || out != "" {
				t.Errorf("unproved delivery: code=%d out=%q stderr=%q; want exit 15", code, out, stderr)
			}
			want := "soho: send: local/w0test:p0a did not confirm taking the message (no sign of it in its state or screen); delivery is uncertain; read its pane for #" + id + " or a reply before sending again; screen saved to " + filepath.Join(dja651StateDir(f), "wait", "send-"+id+".screen") + "\n"
			if stderr != want {
				t.Errorf("uncertain diagnostic=%q; want %q", stderr, want)
			}
			dja651Log(t, dja651StateDir(f), "uncertain", id, "local/-", "local/w0test:p0a")
			dja651Screen(t, dja651StateDir(f), id, last, out, stderr, true)
			enters := 0
			if afterEnter {
				enters = 1
			}
			dja651Submission(t, f, prompt, enters)
		})
	}
}

func TestDJA651Exit15SavesAvailableScreen(t *testing.T) {
	for _, kind := range []string{"blocked", "timeout", "unverified", "save_failure"} {
		t.Run(kind, func(t *testing.T) {
			id, prompt := stalledID(t)
			screen := codexPreScreen(t)
			p := dja651PromptRule(prompt)
			status, cause := kind, ""
			recent := fakecli.Rule{Argv: []string{"agent", "read", "w0test:p0a", "--source", "recent-unwrapped"}, ArgvPrefix: true, Code: 1, Stderr: "history unavailable"}
			switch kind {
			case "blocked", "save_failure":
				p.Code, p.Stderr = 1, `{"error":{"code":"agent_blocked","message":"fixture approval pending"}}`
				status, cause = "blocked", "agent_blocked: fixture approval pending"
			case "timeout":
				p.Code, p.Stderr = 1, `{"error":{"code":"timeout","message":"fixture deadline"}}`
				cause = "timeout"
			}
			rules := []fakecli.Rule{
				{Argv: []string{"agent", "get", "w0test:p0a"}, Stdout: agentJSONKind("codex", "working", "1")},
				{Argv: []string{"agent", "read", "w0test:p0a", "--source", "visible"}, Stdout: screen},
				recent, p,
			}
			f := newFixture(t, rules)
			dja651Isolate(f)
			sd := dja651StateDir(f)
			if kind == "save_failure" {
				if err := os.MkdirAll(sd, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(sd, "wait"), []byte("not a directory"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			code, out, stderr := f.run([]string{"send", "w0test:p0a", "--now", "--timeout", "1s", "hello"})
			want := "soho: send: local/w0test:p0a did not confirm taking the message (" + cause + "); delivery is uncertain; read its pane for #" + id + " or a reply before sending again"
			if kind == "unverified" {
				want = "soho: send: could not confirm that local/w0test:p0a took the message (history unavailable); read its pane before sending again"
			}
			saved := kind != "save_failure"
			if saved {
				want += "; screen saved to " + filepath.Join(sd, "wait", "send-"+id+".screen")
			}
			if code != 15 || out != "" || stderr != want+"\n" {
				t.Errorf("code=%d out=%q stderr=%q; want exit15, empty stdout, %q", code, out, stderr, want+"\n")
			}
			dja651Log(t, sd, status, id, "local/-", "local/w0test:p0a")
			dja651Screen(t, sd, id, screen, out, stderr, saved)
			dja651Submission(t, f, prompt, 0)
		})
	}
}

func dja651Agent(session bool) string {
	if !session {
		return agentJSONKind("codex", "working", "1")
	}
	return fmt.Sprintf(`{"result":{"agent":{"pane_id":"w0test:p0a","agent":"codex","agent_status":"working","state_change_seq":"1","agent_session":{"agent":"codex","kind":"id","source":"herdr:codex","value":%q}}}}`, dja651Session)
}

func dja651Record(t *testing.T, role, text string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"type": "response_item", "payload": map[string]any{"type": "message", "role": role, "content": []any{map[string]any{"type": "input_text", "text": text}}}})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func dja651Journal(t *testing.T, f *fixture, baseline string) string {
	t.Helper()
	path := filepath.Join(f.env.Get("CODEX_HOME"), "sessions", "2026", "11", "05", "rollout-2026-11-05T20-55-51-"+dja651Session+".jsonl")
	writeClaudeTranscript(t, path, baseline)
	return path
}

// Reuse the existing prompt-log-triggered append and always join it, even if
// a product assertion fails. No shell helper or arbitrary sleep selects time.
func dja651Append(t *testing.T, f *fixture, path, id, line string) func() {
	t.Helper()
	done := appendAfterPrompt(t, filepath.Join(filepath.Dir(f.bin), "herdr.calls.jsonl"), path, id, line)
	var once sync.Once
	join := func() {
		once.Do(func() {
			if !<-done {
				t.Error("synthetic journal append did not finish after the actual prompt")
			}
		})
	}
	t.Cleanup(join)
	return join
}

func TestDJA651CodexNewTypedReceiptIsSent(t *testing.T) {
	dja651CodexCase(t, "new_user")
}

// Kept separate from the red groups so the host can run the conservative
// controls on the unchanged production implementation.
func TestDJA651CodexConservativeControls(t *testing.T) {
	for _, kind := range []string{"old_match", "assistant", "tool", "quoted_user", "queue", "composer", "remote", "no_metadata"} {
		t.Run(kind, func(t *testing.T) { dja651CodexCase(t, kind) })
	}
}

func dja651CodexCase(t *testing.T, kind string) {
	t.Helper()
	id, prompt := stalledID(t)
	target, shown := "w0test:p0a", "local/w0test:p0a"
	prefix := []string{}
	if kind == "remote" {
		setSenderHostname(t, "receipt-fixture.invalid")
		target, shown = "windows/w0test:p0a", "windows/w0test:p0a"
		prefix = []string{"--machine", "windows"}
		prompt = peer.PeerHeaderRemote("-", "receipt-fixture.invalid", "-", "-", "-", id) + "\n\n> hello\n" + peer.PeerEndLine(id)
	}
	argv := func(args ...string) []string { return append(append([]string{}, prefix...), args...) }
	pre, screen := codexPreScreen(t), codexPreScreen(t)
	if kind == "queue" {
		screen = strings.Replace(pre, "› Ask Codex to do anything", "↳ "+peer.PeerOpenTag(id)+" pending\n\n› Ask Codex to do anything", 1)
	}
	if kind == "composer" {
		screen = strings.Replace(pre, "› Ask Codex to do anything", "› "+peer.PeerOpenTag(id)+" pending", 1)
	}
	p := dja651PromptRule(prompt)
	p.Argv = append(append([]string{}, prefix...), p.Argv...)
	// Existing appendAfterPrompt fixture pattern: prompt remains in flight
	// while its logged invocation triggers the Go writer.
	p.Delay = 100
	// Stalled negatives have an existing stable TSV status and saved-screen
	// contract, independent of the two lost -> uncertain regressions.
	stalled := kind != "new_user" && kind != "queue"
	if stalled {
		p.Code, p.Stderr = 1, stalledPromptErr
	}
	rules := []fakecli.Rule{
		{Argv: argv("agent", "get", "w0test:p0a"), Stdout: dja651Agent(kind != "no_metadata")},
		{Argv: argv("agent", "read", "w0test:p0a", "--source", "visible"), Call: 1, Stdout: pre},
		{Argv: argv("agent", "read", "w0test:p0a", "--source", "visible"), Stdout: screen},
		{Argv: argv("agent", "read", "w0test:p0a", "--source", "recent-unwrapped"), ArgvPrefix: true, Stdout: "earlier history without receipt\n"},
		p,
		{Argv: argv("agent", "send-keys", "w0test:p0a", "enter")},
	}
	f := newFixture(t, rules)
	dja651Isolate(f)
	baseline := fmt.Sprintf("{\"type\":\"session_meta\",\"payload\":{\"id\":%q}}\n", dja651Session) + dja651Record(t, "user", "earlier conversation") + "\n"
	line := dja651Record(t, "user", prompt)
	switch kind {
	case "old_match":
		baseline += line + "\n"
	case "assistant", "tool":
		line = dja651Record(t, kind, prompt)
	case "quoted_user":
		line = dja651Record(t, "user", "> "+peer.PeerEndLine(id))
	}
	path := dja651Journal(t, f, baseline)
	if kind != "old_match" {
		join := dja651Append(t, f, path, id, line)
		defer join()
		code, out, stderr := f.run([]string{"send", target, "--now", "--timeout", "1s", "hello"})
		join()
		dja651CodexOutcome(t, f, kind, id, prompt, shown, screen, code, out, stderr)
		actual, err := os.ReadFile(path)
		if err != nil || string(actual) != baseline+line+"\n" {
			t.Errorf("want exactly one complete new record after prompt; bytes=%q err=%v", actual, err)
		}
	} else {
		code, out, stderr := f.run([]string{"send", target, "--now", "--timeout", "1s", "hello"})
		dja651CodexOutcome(t, f, kind, id, prompt, shown, screen, code, out, stderr)
	}
	if kind == "remote" && countLocalAgentGets(t, f) != 0 {
		t.Error("remote metadata must not trigger a machineless local lookup")
	}
}

func dja651CodexOutcome(t *testing.T, f *fixture, kind, id, prompt, shown, screen string, code int, out, stderr string) {
	t.Helper()
	status, enters := "stalled", 0
	sd := dja651StateDir(f)
	switch kind {
	case "new_user":
		status = "sent"
		if code != 0 || out != "sent to "+shown+"\n" || stderr != "" {
			t.Errorf("new complete typed target user receipt must be sent with unchanged working state/screen: code=%d out=%q stderr=%q", code, out, stderr)
		}
	case "queue":
		status = "queued"
		if code != 0 || out != "queued for "+shown+": it takes the message when its current turn ends\n" || stderr != "" {
			t.Errorf("a journaled queue is only queued: code=%d out=%q stderr=%q", code, out, stderr)
		}
	default:
		readHint := "read its pane"
		if kind == "remote" {
			readHint += " (herdr --machine windows agent read w0test:p0a --source visible)"
		}
		want := "soho: send: " + shown + " did not confirm taking the message (agent_prompt_stalled: stalled); delivery is uncertain: no proof within the 0.001s window; " + readHint + " for #" + id + " or a reply before sending again"
		if kind == "composer" {
			enters = 1
			want = "soho: send: " + shown + " did not confirm taking the message: the last read showed it in its input box after one Enter; delivery is uncertain; " + readHint + " for #" + id + " or a reply before sending again"
		}
		want += "; screen saved to " + filepath.Join(sd, "wait", "send-"+id+".screen") + "\n"
		if code != 15 || out != "" || stderr != want {
			t.Errorf("conservative %s: code=%d out=%q stderr=%q; want exit15 and %q", kind, code, out, stderr, want)
		}
		dja651Screen(t, sd, id, screen, out, stderr, true)
	}
	dja651Log(t, sd, status, id, "local/-", shown)
	dja651Submission(t, f, prompt, enters)
}
