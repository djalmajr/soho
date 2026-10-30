package herdr

import (
	"os"
	"path/filepath"
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
