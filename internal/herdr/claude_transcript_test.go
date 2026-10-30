package herdr

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

const transcriptSessionUUID = "3f2b8c1a-9d4e-4c7a-b1f0-5e6d7c8b9a0f"

// agentGetWithSession is an agent get response carrying the agent's cwd and
// the agent_session the way `herdr agent get` reports it for a claude agent.
func agentGetWithSession(cwd, value, kind string) string {
	body := `{"result":{"agent":{"agent_status":"idle"`
	if cwd != "" {
		body += `,"cwd":"` + cwd + `"`
	}
	if value != "" || kind != "" {
		body += `,"agent_session":{"source":"herdr","agent":"claude","kind":"` + kind + `","value":"` + value + `"}`
	}
	return body + `}}}`
}

// writeTranscript creates the transcript file under a CLAUDE_CONFIG_DIR
// layout for the given cwd and returns its path.
func writeTranscript(t *testing.T, configRoot, cwd, content string) string {
	t.Helper()
	path := filepath.Join(configRoot, "projects", ClaudeProjectDir(cwd), transcriptSessionUUID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// claudeTranscriptEnv builds a fake herdr env with the home and config
// variables cleared so only what the test sets decides the config root.
func claudeTranscriptEnv(t *testing.T, rules []fakecli.Rule, configRoot string) platform.Env {
	t.Helper()
	env := newFake(t, "herdr", rules)
	delete(env, "CLAUDE_CONFIG_DIR")
	delete(env, "HOME")
	delete(env, "USERPROFILE")
	if configRoot != "" {
		env["CLAUDE_CONFIG_DIR"] = configRoot
	}
	return env
}

func TestClaudeTranscriptPath(t *testing.T) {
	t.Run("CLAUDE_CONFIG_DIR wins and the cwd encodes every non-ASCII-letter-or-digit", func(t *testing.T) {
		root := t.TempDir()
		cwd := `/Users/x/Developer/edger/.worktrees/fix-a`
		want := filepath.Join(root, "projects", "-Users-x-Developer-edger--worktrees-fix-a", transcriptSessionUUID+".jsonl")
		path := writeTranscript(t, root, cwd, `{"type":"user","content":"hi"}\n`)
		if path != want {
			t.Fatalf("fixture path=%s want %s", path, want)
		}
		env := claudeTranscriptEnv(t, []fakecli.Rule{{Argv: []string{"agent", "get", "w"}, Stdout: agentGetWithSession(cwd, transcriptSessionUUID, "id")}}, root)
		if got := ClaudeTranscriptPath("w", env); got != want {
			t.Fatalf("path=%q want %q", got, want)
		}
	})
	t.Run("a cwd with . and _ encodes one - per character", func(t *testing.T) {
		root := t.TempDir()
		cwd := "/tmp/foo_bar.baz"
		want := filepath.Join(root, "projects", "-tmp-foo-bar-baz", transcriptSessionUUID+".jsonl")
		writeTranscript(t, root, cwd, `{"type":"user","content":"hi"}\n`)
		env := claudeTranscriptEnv(t, []fakecli.Rule{{Argv: []string{"agent", "get", "w"}, Stdout: agentGetWithSession(cwd, transcriptSessionUUID, "id")}}, root)
		if got := ClaudeTranscriptPath("w", env); got != want {
			t.Fatalf("path=%q want %q", got, want)
		}
	})
	t.Run("without CLAUDE_CONFIG_DIR the path falls back to HOME/.claude", func(t *testing.T) {
		home := t.TempDir()
		cwd := "/tmp/work"
		writeTranscript(t, filepath.Join(home, ".claude"), cwd, `{"type":"user","content":"hi"}\n`)
		env := claudeTranscriptEnv(t, []fakecli.Rule{{Argv: []string{"agent", "get", "w"}, Stdout: agentGetWithSession(cwd, transcriptSessionUUID, "id")}}, "")
		env["HOME"] = home
		want := filepath.Join(home, ".claude", "projects", "-tmp-work", transcriptSessionUUID+".jsonl")
		if got := ClaudeTranscriptPath("w", env); got != want {
			t.Fatalf("path=%q want %q", got, want)
		}
	})
	t.Run("USERPROFILE stands in for HOME", func(t *testing.T) {
		profile := t.TempDir()
		cwd := "/tmp/work"
		writeTranscript(t, filepath.Join(profile, ".claude"), cwd, `{"type":"user","content":"hi"}\n`)
		env := claudeTranscriptEnv(t, []fakecli.Rule{{Argv: []string{"agent", "get", "w"}, Stdout: agentGetWithSession(cwd, transcriptSessionUUID, "id")}}, "")
		env["USERPROFILE"] = profile
		want := filepath.Join(profile, ".claude", "projects", "-tmp-work", transcriptSessionUUID+".jsonl")
		if got := ClaudeTranscriptPath("w", env); got != want {
			t.Fatalf("path=%q want %q", got, want)
		}
	})
	t.Run("a missing transcript file yields nothing", func(t *testing.T) {
		env := claudeTranscriptEnv(t, []fakecli.Rule{{Argv: []string{"agent", "get", "w"}, Stdout: agentGetWithSession("/tmp/work", transcriptSessionUUID, "id")}}, t.TempDir())
		if got := ClaudeTranscriptPath("w", env); got != "" {
			t.Fatalf("path=%q want empty", got)
		}
	})
	t.Run("no agent_session yields nothing", func(t *testing.T) {
		writeTranscript(t, t.TempDir(), "/tmp/work", `{"type":"user","content":"hi"}\n`)
		env := claudeTranscriptEnv(t, []fakecli.Rule{{Argv: []string{"agent", "get", "w"}, Stdout: `{"result":{"agent":{"agent_status":"idle","cwd":"/tmp/work"}}}`}}, t.TempDir())
		if got := ClaudeTranscriptPath("w", env); got != "" {
			t.Fatalf("path=%q want empty", got)
		}
	})
	t.Run("an agent_session kind that is not id yields nothing", func(t *testing.T) {
		root := t.TempDir()
		cwd := "/tmp/work"
		writeTranscript(t, root, cwd, `{"type":"user","content":"hi"}\n`)
		env := claudeTranscriptEnv(t, []fakecli.Rule{{Argv: []string{"agent", "get", "w"}, Stdout: agentGetWithSession(cwd, transcriptSessionUUID, "path")}}, root)
		if got := ClaudeTranscriptPath("w", env); got != "" {
			t.Fatalf("path=%q want empty", got)
		}
	})
	t.Run("an empty session value yields nothing", func(t *testing.T) {
		env := claudeTranscriptEnv(t, []fakecli.Rule{{Argv: []string{"agent", "get", "w"}, Stdout: agentGetWithSession("/tmp/work", "", "id")}}, t.TempDir())
		if got := ClaudeTranscriptPath("w", env); got != "" {
			t.Fatalf("path=%q want empty", got)
		}
	})
	t.Run("a missing cwd yields nothing", func(t *testing.T) {
		env := claudeTranscriptEnv(t, []fakecli.Rule{{Argv: []string{"agent", "get", "w"}, Stdout: `{"result":{"agent":{"agent_status":"idle","agent_session":{"agent":"claude","kind":"id","value":"` + transcriptSessionUUID + `"}}}}`}}, t.TempDir())
		if got := ClaudeTranscriptPath("w", env); got != "" {
			t.Fatalf("path=%q want empty", got)
		}
	})
	t.Run("an agent get failure yields nothing", func(t *testing.T) {
		env := claudeTranscriptEnv(t, []fakecli.Rule{{Argv: []string{"agent", "get", "w"}, Code: 1, Stderr: `{"error":{"code":"agent_not_found","message":"gone"}}`}}, t.TempDir())
		if got := ClaudeTranscriptPath("w", env); got != "" {
			t.Fatalf("path=%q want empty", got)
		}
	})
	t.Run("no home and no CLAUDE_CONFIG_DIR yield nothing", func(t *testing.T) {
		env := claudeTranscriptEnv(t, []fakecli.Rule{{Argv: []string{"agent", "get", "w"}, Stdout: agentGetWithSession("/tmp/work", transcriptSessionUUID, "id")}}, "")
		if got := ClaudeTranscriptPath("w", env); got != "" {
			t.Fatalf("path=%q want empty", got)
		}
	})
}

func TestCountClaudeUserMarkerLines(t *testing.T) {
	marker := "#01020304"
	userLine := `{"type":"user","message":{"content":"[herdr-soho:peer] ` + marker + ` end of message"}}`
	t.Run("only the user lines that hold the marker count", func(t *testing.T) {
		// The assistant line models Claude citing the id back in its own
		// reply: a marker without the user record type is not delivery.
		dir := t.TempDir()
		path := filepath.Join(dir, "session.jsonl")
		content := strings.Join([]string{
			`{"type":"assistant","message":{"content":"did you mean ` + marker + `? I am still working"}}`,
			userLine,
			`{"type":"user","message":{"content":"an earlier message without the marker"}}`,
			`{"type":"system","subtype":"file_history_snapshot","content":"` + marker + ` inside system data"}}`,
			`{"type":"user","message":{"content":"[herdr-soho:peer] #99999999 end of message"}}`,
			userLine,
		}, "\n") + "\n"
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		n, ok := CountClaudeUserMarkerLines(path, marker)
		if !ok || n != 2 {
			t.Fatalf("count=(%d,%v) want (2,true)", n, ok)
		}
	})
	t.Run("a line that holds the user type and the marker twice still counts once", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "session.jsonl")
		content := `{"type":"user","message":{"content":"` + marker + ` ` + marker + `"}}` + "\n"
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		n, ok := CountClaudeUserMarkerLines(path, marker)
		if !ok || n != 1 {
			t.Fatalf("count=(%d,%v) want (1,true)", n, ok)
		}
	})
	t.Run("a matching line without a trailing newline still counts", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "session.jsonl")
		content := `{"type":"user","message":{"content":"earlier"}}` + "\n" + `{"type":"user","message":{"content":"` + marker + `"}}`
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		n, ok := CountClaudeUserMarkerLines(path, marker)
		if !ok || n != 1 {
			t.Fatalf("count=(%d,%v) want (1,true)", n, ok)
		}
	})
	t.Run("a line far past the default 64 KiB token still counts", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "session.jsonl")
		long := `{"type":"user","message":{"content":"` + strings.Repeat("a", 200*1024) + marker + `"}}` + "\n"
		if err := os.WriteFile(path, []byte(long), 0o600); err != nil {
			t.Fatal(err)
		}
		n, ok := CountClaudeUserMarkerLines(path, marker)
		if !ok || n != 1 {
			t.Fatalf("count=(%d,%v) want (1,true)", n, ok)
		}
	})
	t.Run("an empty file counts zero", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "session.jsonl")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		n, ok := CountClaudeUserMarkerLines(path, marker)
		if !ok || n != 0 {
			t.Fatalf("count=(%d,%v) want (0,true)", n, ok)
		}
	})
	t.Run("an unreadable file reports not ok with no count", func(t *testing.T) {
		n, ok := CountClaudeUserMarkerLines(filepath.Join(t.TempDir(), "missing.jsonl"), marker)
		if ok || n != 0 {
			t.Fatalf("count=(%d,%v) want (0,false)", n, ok)
		}
	})
	t.Run("the function returns no line text: its result is only the number", func(t *testing.T) {
		// The signature is the contract (int, bool): no line content leaves
		// the function. Run it on a transcript whose matching lines hold
		// distinctive sentinel payloads and check only the count comes back.
		dir := t.TempDir()
		path := filepath.Join(dir, "session.jsonl")
		content := strings.Join([]string{
			`{"type":"user","message":{"content":"USER-SENTINEL-1 ` + marker + `"}}`,
			`{"type":"user","message":{"content":"USER-SENTINEL-2 ` + marker + `"}}`,
			`{"type":"assistant","message":{"content":"ASSISTANT-SENTINEL ` + marker + `"}}`,
		}, "\n") + "\n"
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		n, ok := CountClaudeUserMarkerLines(path, marker)
		if !ok || n != 2 {
			t.Fatalf("count=(%d,%v) want (2,true)", n, ok)
		}
	})
}

func TestClaudeProjectDirEncoding(t *testing.T) {
	cases := []struct{ cwd, want string }{
		{"/Users/x/Developer/edger/.worktrees/fix-a", "-Users-x-Developer-edger--worktrees-fix-a"},
		{"/tmp/foo_bar.baz", "-tmp-foo-bar-baz"},
		{"/", "-"},
		{"plain", "plain"},
	}
	for _, tc := range cases {
		if got := ClaudeProjectDir(tc.cwd); got != tc.want {
			t.Fatalf("ClaudeProjectDir(%q)=%q want %q", tc.cwd, got, tc.want)
		}
	}
}

func TestCountClaudeCompactBoundaries(t *testing.T) {
	t.Run("only the boundary lines count, whatever the other lines hold", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "session.jsonl")
		content := strings.Join([]string{
			`{"type":"user","content":"secret old conversation line"}`,
			`{"type":"system","subtype":"compact_boundary","content":"compact summary that must not surface"}`,
			`{"type":"assistant","content":"another secret line"}`,
			`{"type":"system","subtype":"compact_boundary","compact_metadata":{"x":1}}`,
			`{"type":"user","content":"the marker as prose subtype compact_boundary without quotes"}`,
		}, "\n") + "\n"
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		n, ok := CountClaudeCompactBoundaries(path)
		if !ok || n != 2 {
			t.Fatalf("count=(%d,%v) want (2,true)", n, ok)
		}
	})
	t.Run("a line that holds the marker twice still counts once", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "session.jsonl")
		content := `{"type":"system","subtype":"compact_boundary"}{"type":"system","subtype":"compact_boundary"}` + "\n"
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		n, ok := CountClaudeCompactBoundaries(path)
		if !ok || n != 1 {
			t.Fatalf("count=(%d,%v) want (1,true)", n, ok)
		}
	})
	t.Run("a boundary line without a trailing newline still counts", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "session.jsonl")
		if err := os.WriteFile(path, []byte(`{"type":"user","content":"x"}
{"type":"system","subtype":"compact_boundary"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		n, ok := CountClaudeCompactBoundaries(path)
		if !ok || n != 1 {
			t.Fatalf("count=(%d,%v) want (1,true)", n, ok)
		}
	})
	t.Run("a line far past the default 64 KiB token still counts", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "session.jsonl")
		long := `{"type":"assistant","content":"` + strings.Repeat("a", 200*1024) + `,"subtype":"compact_boundary"}` + "\n"
		if err := os.WriteFile(path, []byte(long), 0o600); err != nil {
			t.Fatal(err)
		}
		n, ok := CountClaudeCompactBoundaries(path)
		if !ok || n != 1 {
			t.Fatalf("count=(%d,%v) want (1,true)", n, ok)
		}
	})
	t.Run("an empty file counts zero", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "session.jsonl")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		n, ok := CountClaudeCompactBoundaries(path)
		if !ok || n != 0 {
			t.Fatalf("count=(%d,%v) want (0,true)", n, ok)
		}
	})
	t.Run("an unreadable file reports not ok with no count", func(t *testing.T) {
		n, ok := CountClaudeCompactBoundaries(filepath.Join(t.TempDir(), "missing.jsonl"))
		if ok || n != 0 {
			t.Fatalf("count=(%d,%v) want (0,false)", n, ok)
		}
	})
	t.Run("the function returns no line text: its result is only the number", func(t *testing.T) {
		// The signature is the contract (int, bool): no line content leaves
		// the function. Run it on a transcript whose boundary lines hold
		// distinctive summaries and check only the count comes back.
		dir := t.TempDir()
		path := filepath.Join(dir, "session.jsonl")
		content := `{"type":"system","subtype":"compact_boundary","summary":"SUMMARY-SENTINEL-1"}
{"type":"system","subtype":"compact_boundary","summary":"SUMMARY-SENTINEL-2"}` + "\n"
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		n, ok := CountClaudeCompactBoundaries(path)
		if !ok || n != 2 {
			t.Fatalf("count=(%d,%v) want (2,true)", n, ok)
		}
	})
}

// TestCountClaudeUserClosingLines pins the receipt contract of the closing
// line counter (DJA-610 part B amendment, F2): a user record counts only
// when it carries the message's exact closing line as one of its own lines.
// The opening line alone, the previous format's closing line, a quoted
// line and a marker glued to other text are payload, not a receipt.
func TestCountClaudeUserClosingLines(t *testing.T) {
	const closing = "[/soho:peer:01020304]"
	cases := []struct {
		name   string
		record string
		want   int
	}{
		{"the complete prompt", `{"type":"user","message":{"content":"line one\n` + closing + `\nlast line"}}`, 1},
		{"the whole content", `{"type":"user","message":{"content":"` + closing + `"}}`, 1},
		{"the history-prefixed line", `{"type":"user","message":{"content":"` + "›" + closing + `"}}`, 1},
		{"a text block", `{"type":"user","message":{"content":[{"type":"text","text":"x\n` + closing + `"}]}}`, 1},
		{"a tool result", `{"type":"user","message":{"content":[{"type":"tool_result","content":"` + closing + `"}]}}`, 1},
		{"the opening line alone", `{"type":"user","message":{"content":"[soho:peer:01020304]"}}`, 0},
		{"the previous format's closing line", `{"type":"user","message":{"content":"[/soho:peer] #01020304 end of message"}}`, 0},
		{"a quoted closing line", `{"type":"user","message":{"content":"> ` + closing + `"}}`, 0},
		{"a closing line glued to other text", `{"type":"user","message":{"content":"status ok."` + closing + `"}}`, 0},
		{"a longer id", `{"type":"user","message":{"content":"[/soho:peer:01020304abcd]"}}`, 0},
		{"an assistant reply citing the closing line", `{"type":"assistant","message":{"content":"done` + "\n" + closing + `"}}`, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "session.jsonl")
			content := `{"type":"user","message":{"content":"earlier conversation"}}` + "\n" + c.record + "\n"
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			n, ok := CountClaudeUserClosingLines(path, closing)
			if !ok || n != c.want {
				t.Fatalf("count=(%d,%v) want (%d,true)", n, ok, c.want)
			}
		})
	}
	t.Run("a record missing or unreadable", func(t *testing.T) {
		if n, ok := CountClaudeUserClosingLines(filepath.Join(t.TempDir(), "absent.jsonl"), closing); ok || n != 0 {
			t.Fatalf("count=(%d,%v) want (0,false)", n, ok)
		}
	})
}
