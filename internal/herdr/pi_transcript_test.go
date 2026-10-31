package herdr

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

const piSessionFile = "2026-10-02T06-18-06-537Z_01a0fb43-4b49-70d7-8f62-5e7a10390e69.jsonl"

// piSessionValue is the session file path the fake `agent get` reports for
// the pi agent, under the given sessions root.
func piSessionValue(root string) string {
	return filepath.Join(root, "agent", "sessions", "--tmp-pi-work--", piSessionFile)
}

// piAgentGetWithSession is an agent get response carrying the agent_session
// the way `herdr agent get` reports it for a pi agent. The body is built
// with json.Marshal so a session path with a backslash or a quote stays
// valid JSON (a path concatenated between quotes would not).
func piAgentGetWithSession(kind, value string) string {
	agent := map[string]any{"agent": "pi", "agent_status": "idle"}
	if kind != "" || value != "" {
		agent["agent_session"] = map[string]any{"source": "herdr:pi", "agent": "pi", "kind": kind, "value": value}
	}
	body, err := json.Marshal(map[string]any{"result": map[string]any{"agent": agent}})
	if err != nil {
		panic(fmt.Sprintf("piAgentGetWithSession: %v", err))
	}
	return string(body)
}

// writePiSession creates the pi session file at the given absolute path and
// returns it.
func writePiSession(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPiSessionPath(t *testing.T) {
	t.Run("a kind path session value that exists resolves to it", func(t *testing.T) {
		root := t.TempDir()
		value := piSessionValue(root)
		writePiSession(t, value, `{"type":"message","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}`+"\n")
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "get", "w"}, Stdout: piAgentGetWithSession("path", value)}})
		if got := PiSessionPath("w", env); got != value {
			t.Fatalf("path=%q want %q", got, value)
		}
	})
	t.Run("an agent_session kind that is not path yields nothing", func(t *testing.T) {
		root := t.TempDir()
		value := piSessionValue(root)
		writePiSession(t, value, `{"type":"message","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}`+"\n")
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "get", "w"}, Stdout: piAgentGetWithSession("id", value)}})
		if got := PiSessionPath("w", env); got != "" {
			t.Fatalf("path=%q want empty", got)
		}
	})
	t.Run("a missing agent_session yields nothing", func(t *testing.T) {
		root := t.TempDir()
		value := piSessionValue(root)
		writePiSession(t, value, `{"type":"message"}`+"\n")
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "get", "w"}, Stdout: `{"result":{"agent":{"agent":"pi","agent_status":"idle"}}}`}})
		if got := PiSessionPath("w", env); got != "" {
			t.Fatalf("path=%q want empty", got)
		}
	})
	t.Run("an empty session value yields nothing", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "get", "w"}, Stdout: piAgentGetWithSession("path", "")}})
		if got := PiSessionPath("w", env); got != "" {
			t.Fatalf("path=%q want empty", got)
		}
	})
	t.Run("a relative session value yields nothing even though a same-named file exists", func(t *testing.T) {
		root := t.TempDir()
		writePiSession(t, filepath.Join(root, piSessionFile), `{"type":"message"}`+"\n")
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "get", "w"}, Stdout: piAgentGetWithSession("path", piSessionFile)}})
		if got := PiSessionPath("w", env); got != "" {
			t.Fatalf("path=%q want empty", got)
		}
	})
	t.Run("a missing session file yields nothing", func(t *testing.T) {
		root := t.TempDir()
		value := piSessionValue(root)
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "get", "w"}, Stdout: piAgentGetWithSession("path", value)}})
		if got := PiSessionPath("w", env); got != "" {
			t.Fatalf("path=%q want empty", got)
		}
	})
	t.Run("a session file whose directory name holds a backslash resolves", func(t *testing.T) {
		// On Darwin and Linux a backslash is a valid directory-name character;
		// the fake get's response must carry it escaped the way JSON writes
		// it, or the resolution silently tests a malformed response.
		dir := filepath.Join(t.TempDir(), "agent\\sessions")
		value := filepath.Join(dir, piSessionFile)
		writePiSession(t, value, `{"type":"message"}`+"\n")
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "get", "w"}, Stdout: piAgentGetWithSession("path", value)}})
		if got := PiSessionPath("w", env); got != value {
			t.Fatalf("path=%q want %q", got, value)
		}
	})
	t.Run("an agent get failure yields nothing", func(t *testing.T) {
		env := newFake(t, "herdr", []fakecli.Rule{{Argv: []string{"agent", "get", "w"}, Code: 1, Stderr: `{"error":{"code":"agent_not_found","message":"gone"}}`}})
		if got := PiSessionPath("w", env); got != "" {
			t.Fatalf("path=%q want empty", got)
		}
	})
}
