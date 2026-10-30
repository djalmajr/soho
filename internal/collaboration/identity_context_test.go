package collaboration

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestMain(m *testing.M) { fakecli.RunTests(m) }

// TestResolveParticipantForwardsThePerCallContext: the participant's agent
// get runs under the caller's per-call context. A pre-cancelled context
// fails the check before the CLI answers; the control run (no context)
// resolves the same registration, so the fixture is valid and only the
// context forwarding distinguishes the two runs. Dropping the Context from
// the identity path would let the cancelled run reach the (valid) CLI reply
// and the test fails.
func TestResolveParticipantForwardsThePerCallContext(t *testing.T) {
	repo := t.TempDir()
	repo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatalf("canonicalize the fixture repo: %v", err)
	}
	stateDir := t.TempDir()
	core.RosterAppend(stateDir, []string{"worker", "w1:p9", "grok", "implementer", "xai", "pane-1", repo, "now", "grok-4.7", "task", "implementer", "review"})
	agent := `{"result":{"agent":{"name":"worker","pane_id":"w1:p9","agent":"grok","workspace_id":"ws-1","cwd":` + jsonString(t, repo) + `,"agent_session":{"kind":"claude","value":"sess-1"}}}}`
	bin := t.TempDir()
	if _, err := fakecli.Install(t, bin, "herdr", []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: agent}}); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{}
	for _, pair := range fakecli.Env(testutil.CleanEnv(t), bin) {
		if k, v, ok := strings.Cut(pair, "="); ok {
			env[k] = v
		}
	}
	env["HERDR_WORKSPACE_ID"] = "ws-1"

	// Control: without a context the same registration resolves.
	live, err := ResolveParticipant(stateDir, "worker", env)
	if err != nil {
		t.Fatalf("control: the valid registration must resolve without a context: %v", err)
	}
	if live.Name != "worker" || live.Pane != "w1:p9" || live.Kind != "grok" || live.Lane != "review" {
		t.Fatalf("control participant = %#v", live)
	}

	// The discriminating run: a pre-cancelled context must fail the agent
	// get, because the context reaches the CLI call.
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ResolveParticipant(stateDir, "worker", env, cancelled); err == nil {
		t.Fatal("a pre-cancelled per-call context must fail the participant check; the context is not forwarded to the agent get")
	}
}

// jsonString quotes s as a JSON string: a Windows path holds backslashes.
func jsonString(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
