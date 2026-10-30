package herdr

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/djalmajr/soho/internal/platform"
)

// ClaudeTranscriptPath returns the path of the agent's Claude Code session
// transcript (<config>/projects/<encoded cwd>/<session>.jsonl), or "" when any
// data is missing: the agent get call fails, the agent has no agent_session,
// the session kind is not "id", the agent has no cwd, the config root cannot
// be resolved (CLAUDE_CONFIG_DIR, else HOME/USERPROFILE plus .claude), or
// the transcript file does not exist.
func ClaudeTranscriptPath(agent string, env platform.Env, callCtx ...context.Context) string {
	r := run([]string{"agent", "get", agent}, env, callCtx...)
	if !ok(r) {
		return ""
	}
	agentObj := get(parsed(r.Stdout), "result", "agent")
	session := get(agentObj, "agent_session")
	if str(get(session, "kind")) != "id" {
		return ""
	}
	value := str(get(session, "value"))
	if value == "" {
		return ""
	}
	cwd := str(get(agentObj, "cwd"))
	if cwd == "" {
		return ""
	}
	root := env.Get("CLAUDE_CONFIG_DIR")
	if root == "" {
		home := env.Get("HOME")
		if home == "" {
			home = env.Get("USERPROFILE")
		}
		if home == "" {
			return ""
		}
		root = filepath.Join(home, ".claude")
	}
	path := filepath.Join(root, "projects", ClaudeProjectDir(cwd), value+".jsonl")
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	return path
}

// ClaudeProjectDir encodes the agent cwd the way Claude Code names its
// projects directory: every character that is not an ASCII letter or digit
// becomes one "-" (each "." or "_" in the cwd encodes as "-", as does the
// "/" that separates the parts).
func ClaudeProjectDir(cwd string) string {
	var out strings.Builder
	for _, r := range cwd {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			out.WriteRune(r)
		default:
			out.WriteByte('-')
		}
	}
	return out.String()
}
