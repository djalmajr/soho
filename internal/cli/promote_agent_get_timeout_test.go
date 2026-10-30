package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// TestPromoteLiveAgentKeepsTheFiveSecondAgentGet: the promote live-agent
// read is one agent get bounded by 5 s. The fixture answers only after 7 s,
// so the 5 s bound must refuse it as a failed read (fail closed), the way
// the unrefactored call did. A 30 s bound would let the 7 s answer through
// and the read would succeed (a different refusal, or none); a retry would
// issue a second call the fixture answers immediately. Both mutants die
// here.
func TestPromoteLiveAgentKeepsTheFiveSecondAgentGet(t *testing.T) {
	// The payload only matters to a mutant: on the control the 5 s bound
	// cuts the 7 s answer before the body is parsed.
	valid := `{"result":{"agent":{"name":"orchestrator","pane_id":"ws:p9","workspace_id":"ws","cwd":"/repo","agent_status":"idle"}}}`
	f := newCopiesFixture(t, []fakecli.Rule{
		{Argv: []string{"agent", "get", "ws:p9"}, Call: 1, Delay: 7000, Stdout: valid},
		{Argv: []string{"agent", "get", "ws:p9"}, Call: 2, Stdout: valid},
	})
	svc := herdr.New(f.env)
	code, msg := dieCodeMsg(t, func() {
		promoteLiveAgent("ws:p9", "ws", f.cwd, f.env, svc)
	})
	if code != 4 || !strings.Contains(msg, "cannot verify the live agent of pane ws:p9") {
		t.Fatalf("code=%d msg=%q; the 5 s agent get must time out fail-closed", code, msg)
	}
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(f.env.Get("SOHO_FAKECLI_CONFIG"), "herdr.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("one agent get expected, got %#v", calls)
	}
}
