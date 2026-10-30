package herdr

import (
	"os"
	"path/filepath"

	"github.com/djalmajr/soho/internal/platform"
)

// PiSessionPath returns the path of the agent's pi session file (the
// agent_session value herdr reports for a pi agent, for example
// <home>/.pi/agent/sessions/--Users-x-proj--/<timestamp>_<uuid>.jsonl), or
// "" when any data is missing: the agent get call fails, the agent has no
// agent_session, the session kind is not "path", the value is empty or not
// an absolute path, or the file does not exist locally.
func PiSessionPath(agent string, env platform.Env) string {
	r := run([]string{"agent", "get", agent}, env)
	if !ok(r) {
		return ""
	}
	agentObj := get(parsed(r.Stdout), "result", "agent")
	session := get(agentObj, "agent_session")
	if str(get(session, "kind")) != "path" {
		return ""
	}
	value := str(get(session, "value"))
	if value == "" || !filepath.IsAbs(value) {
		return ""
	}
	if _, err := os.Stat(value); err != nil {
		return ""
	}
	return value
}
