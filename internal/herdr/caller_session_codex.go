package herdr

import (
	"encoding/json"

	"github.com/djalmajr/soho/internal/platform"
)

// codexShownIn suggests a single other pane for a diagnostic session identity.
// It never verifies a pane claim or reads transcripts. The same RunCli runner
// and per-call timeout as the Claude pane-list read bound the CLI invocation;
// a failed read or malformed response discards every candidate.
func codexShownIn(claimed, session string, env platform.Env) string {
	if !validSessionID(session) {
		return ""
	}
	r := platform.RunCli("herdr", []string{"pane", "list"}, platform.RunOptions{Env: env, TimeoutMs: callerSessionTimeoutMs})
	if r.NotFound || r.TimedOut || r.Status == nil || *r.Status != 0 {
		return ""
	}
	var payload struct {
		Error  json.RawMessage `json:"error"`
		Result struct {
			Panes []*struct {
				PaneID       string `json:"pane_id"`
				AgentSession *struct {
					Agent string `json:"agent"`
					Kind  string `json:"kind"`
					Value string `json:"value"`
				} `json:"agent_session"`
			} `json:"panes"`
		} `json:"result"`
	}
	if json.Unmarshal([]byte(r.Stdout), &payload) != nil || (len(payload.Error) != 0 && string(payload.Error) != "null") {
		return ""
	}
	found := ""
	for _, pane := range payload.Result.Panes {
		if pane == nil {
			return ""
		}
		if pane.PaneID == claimed || !paneIDPattern.MatchString(pane.PaneID) || pane.AgentSession == nil {
			continue
		}
		id := pane.AgentSession
		if id.Agent != "codex" || id.Kind != "id" || !validSessionID(id.Value) || id.Value != session {
			continue
		}
		if found != "" {
			return ""
		}
		found = pane.PaneID
	}
	return found
}
