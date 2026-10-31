package collaboration

// DJA-611: CheckParticipantsForWorkspace is the read-only explicit-workspace
// twin of CheckParticipants: the assignment's workspace is compared to the
// known supplied workspace, and each participant's record is verified
// against it, instead of resolving the caller's workspace. The writer
// paths (CheckParticipants and ResolveParticipant) keep their original
// caller-derived checks; these tests A/B both sides on the same fixtures
// with the fake herdr / fake hc recording every call.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/kinds"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// explicitWsFixture is one state dir with the two-participant roster (the
// pane column is the herdr pane id), the assignment in workspace ws and
// the fake herdr answering `agent get <name>` with the given
// workspace_id. envCaller is the caller's HERDR_WORKSPACE_ID the writer
// paths resolve.
func explicitWsFixture(t *testing.T, repo, envCaller, agentWorkspace string) (string, Assignment, platform.Env, string) {
	t.Helper()
	repo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	core.RosterAppend(stateDir, []string{"author", "p1", "claude", "implementer", "anthropic", "0", repo, "now", "claude-sonnet-4-5", "ask", "implementer", "build"})
	core.RosterAppend(stateDir, []string{"reviewer", "p2", "grok", "reviewer", "xai", "0", repo, "now", "grok-4", "ask", "reviewer", "build"})
	bin := t.TempDir()
	agent := func(name, pane, kind, session string) string {
		return `{"result":{"agent":{"name":"` + name + `","pane_id":"` + pane + `","agent":"` + kind + `","workspace_id":"` + agentWorkspace + `","cwd":` + jsonString(t, repo) + `,"agent_session":{"kind":"pi","value":"` + session + `"}}}}`
	}
	if _, err := fakecli.Install(t, bin, "herdr", []fakecli.Rule{
		{Argv: []string{"agent", "get", "author"}, Stdout: agent("author", "p1", "claude", "s-1")},
		{Argv: []string{"agent", "get", "reviewer"}, Stdout: agent("reviewer", "p2", "grok", "s-2")},
	}); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{}
	for _, pair := range fakecli.Env(testutil.CleanEnv(t), bin) {
		if k, v, ok := strings.Cut(pair, "="); ok {
			env[k] = v
		}
	}
	if envCaller != "" {
		env["HERDR_WORKSPACE_ID"] = envCaller
	}
	a := Assignment{
		Author:    Participant{Name: "author", Pane: "p1", Role: "implementer", Kind: "claude", Family: kinds.AgentFamily("claude", "claude-sonnet-4-5"), Started: "now", Model: "claude-sonnet-4-5", Session: Hash([]byte("pi\x00s-1")), Cwd: repo, Lane: "build"},
		Reviewer:  Participant{Name: "reviewer", Pane: "p2", Role: "reviewer", Kind: "grok", Family: kinds.AgentFamily("grok", "grok-4"), Started: "now", Model: "grok-4", Session: Hash([]byte("pi\x00s-2")), Cwd: repo, Lane: "build"},
		Workspace: "ws",
		Root:      platform.StateProjectRoot(env, repo),
		BriefHash: "0",
		Paths:     []string{repo},
		MaxRounds: 3,
	}
	return stateDir, a, env, bin
}

// herdrCalls returns the ordered argv of the fake herdr calls.
func herdrCalls(t *testing.T, bin string) []string {
	t.Helper()
	calls, err := fakecli.ReadCalls(filepath.Join(bin, "herdr.calls.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	out := []string{}
	for _, c := range calls {
		out = append(out, strings.Join(c.Argv, "\x00"))
	}
	return out
}

// assertExactHerdrCalls asserts the exact ordered participant reads.
func assertExactHerdrCalls(t *testing.T, bin string, want ...string) {
	t.Helper()
	got := herdrCalls(t, bin)
	if strings.Join(got, " | ") != strings.Join(want, " | ") {
		t.Fatalf("herdr calls=%#v, want exactly %#v", got, want)
	}
}

// TestCheckParticipantsForWorkspaceMatchesTheKnownWorkspace: the explicit
// workspace verifies the participants against itself even when the
// caller's env carries a conflicting HERDR_WORKSPACE_ID; the writer
// control (CheckParticipants with the caller-derived workspace) refuses
// on the same fixture, so the fixture is valid and only the expected
// workspace distinguishes the two paths.
func TestCheckParticipantsForWorkspaceMatchesTheKnownWorkspace(t *testing.T) {
	stateDir, a, env, bin := explicitWsFixture(t, t.TempDir(), "wrong", "ws")
	if err := CheckParticipantsForWorkspace(a, stateDir, "ws", env); err != nil {
		t.Fatalf("the matching known workspace must verify: %v", err)
	}
	assertExactHerdrCalls(t, bin, "agent\x00get\x00author", "agent\x00get\x00reviewer")

	// The writer control keeps the caller-derived check: the caller's
	// conflicting workspace refuses before any participant read.
	stateDir2, a2, env2, bin2 := explicitWsFixture(t, t.TempDir(), "wrong", "ws")
	if err := CheckParticipants(a2, stateDir2, env2); err == nil || err.Error() != "collaboration belongs to another workspace" {
		t.Fatalf("writer control err = %v, want the caller workspace refusal", err)
	}
	assertExactHerdrCalls(t, bin2)
}

// TestCheckParticipantsForWorkspaceMismatchRefusesBeforeAnyRead: a known
// workspace that does not match the assignment refuses before any
// participant read.
func TestCheckParticipantsForWorkspaceMismatchRefusesBeforeAnyRead(t *testing.T) {
	stateDir, a, env, bin := explicitWsFixture(t, t.TempDir(), "ws", "ws")
	err := CheckParticipantsForWorkspace(a, stateDir, "other", env)
	if err == nil || err.Error() != "collaboration belongs to another workspace" {
		t.Fatalf("err = %v, want the known-workspace refusal", err)
	}
	assertExactHerdrCalls(t, bin)
}

// TestCheckParticipantsForWorkspaceIdentityChanged: a participant record
// whose workspace no longer matches the known workspace takes the
// identity-changed refusal, and the second participant is not read.
func TestCheckParticipantsForWorkspaceIdentityChanged(t *testing.T) {
	stateDir, a, env, bin := explicitWsFixture(t, t.TempDir(), "ws", "elsewhere")
	err := CheckParticipantsForWorkspace(a, stateDir, "ws", env)
	if err == nil || err.Error() != "participant pane, workspace or session no longer matches its registration" {
		t.Fatalf("err = %v, want the identity-changed refusal", err)
	}
	// The author read fails the check; the reviewer is never probed.
	assertExactHerdrCalls(t, bin, "agent\x00get\x00author")
}

// hcExplicitWsFixture is the transport=hc twin: the two-participant roster
// (the pane column is the hc address), the assignment in workspace ws and
// the fake hc answering `get <name> --json` with live records; the fake
// herdr sentinel fails the test if the hc path reaches herdr.
func hcExplicitWsFixture(t *testing.T, repo, callerID, callerSession string) (string, Assignment, platform.Env, string) {
	t.Helper()
	repo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	core.RosterAppend(stateDir, []string{"author", "t4", "codex", "implementer", "openai", "0", repo, "now", "gpt-5.4", "ask", "implementer", "build"})
	core.RosterAppend(stateDir, []string{"reviewer", "t5", "grok", "reviewer", "xai", "0", repo, "now", "grok-4", "ask", "reviewer", "build"})
	bin := t.TempDir()
	record := func(address, name string) string {
		return `{"address":"` + address + `","name":"` + name + `","state":"live","cwd":` + jsonString(t, repo) + `,"status":{"state":"working"}}`
	}
	rules := []fakecli.Rule{
		{Argv: []string{"get", "author", "--json"}, Stdout: record("t4", "author")},
		{Argv: []string{"get", "reviewer", "--json"}, Stdout: record("t5", "reviewer")},
	}
	if callerID != "" {
		rules = append(rules, fakecli.Rule{Argv: []string{"get", callerID, "--json"}, Stdout: `{"address":"` + callerID + `","state":"live","labels":{"soho.session":"` + callerSession + `"}}`})
	}
	if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
		t.Fatal(err)
	}
	if _, err := fakecli.Install(t, bin, "herdr", nil); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{}
	for _, pair := range fakecli.Env(testutil.CleanEnv(t), bin) {
		if k, v, ok := strings.Cut(pair, "="); ok {
			env[k] = v
		}
	}
	env["SOHO_TRANSPORT"] = "hc"
	if callerID != "" {
		env["HC_TERMINAL_ID"] = callerID
	}
	a := Assignment{
		Author:    Participant{Name: "author", Pane: "t4", Role: "implementer", Kind: "codex", Family: kinds.AgentFamily("codex", "gpt-5.4"), Started: "now", Model: "gpt-5.4", Session: Hash([]byte("hc\x00t4")), Cwd: repo, Lane: "build"},
		Reviewer:  Participant{Name: "reviewer", Pane: "t5", Role: "reviewer", Kind: "grok", Family: kinds.AgentFamily("grok", "grok-4"), Started: "now", Model: "grok-4", Session: Hash([]byte("hc\x00t5")), Cwd: repo, Lane: "build"},
		Workspace: "ws",
		Root:      platform.StateProjectRoot(env, repo),
		BriefHash: "0",
		Paths:     []string{repo},
		MaxRounds: 3,
	}
	return stateDir, a, env, bin
}

// assertNoHcCallerGet fails when the fake hc recorded a get of the
// inherited caller terminal or a label-scoped listing.
func assertNoHcCallerGet(t *testing.T, bin, callerID string) {
	t.Helper()
	calls, err := fakecli.ReadCalls(filepath.Join(bin, "hc.calls.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		t.Fatal(err)
	}
	for _, c := range calls {
		if len(c.Argv) == 3 && c.Argv[0] == "get" && c.Argv[1] == callerID {
			t.Fatalf("the explicit-workspace path resolved the caller: %v", c.Argv)
		}
		for _, a := range c.Argv {
			if strings.HasPrefix(a, "soho.session=") {
				t.Fatalf("the explicit-workspace path ran a caller-scoped listing: %v", c.Argv)
			}
		}
	}
}

// TestCheckParticipantsForWorkspaceHcNeverResolvesTheCaller: under
// transport=hc the explicit-workspace check verifies the participants by
// their actual assigned names and never resolves the inherited caller
// terminal — even a stale foreign HC_TERMINAL_ID with a matching fake
// rule — and never reaches herdr; the writer control (CheckParticipants)
// resolves the caller first on the same fixture.
func TestCheckParticipantsForWorkspaceHcNeverResolvesTheCaller(t *testing.T) {
	stateDir, a, env, bin := hcExplicitWsFixture(t, t.TempDir(), "t9", "sT9")
	if err := CheckParticipantsForWorkspace(a, stateDir, "ws", env); err != nil {
		t.Fatalf("the matching known workspace must verify under hc: %v", err)
	}
	calls := herdrHcCalls(t, bin)
	if strings.Join(calls, " | ") != strings.Join([]string{"get\x00author\x00--json", "get\x00reviewer\x00--json"}, " | ") {
		t.Fatalf("hc calls=%#v, want exactly the two participant gets", calls)
	}
	assertNoHcCallerGet(t, bin, "t9")
	assertNoHerdrSentinel(t, bin)

	// The writer control keeps the caller-derived check: with the caller's
	// session label matching the workspace it resolves the caller first,
	// then the participants.
	stateDir2, a2, env2, bin2 := hcExplicitWsFixture(t, t.TempDir(), "t9", "ws")
	if err := CheckParticipants(a2, stateDir2, env2); err != nil {
		t.Fatalf("writer control: the matching caller workspace must verify: %v", err)
	}
	got := herdrHcCalls(t, bin2)
	want := []string{"get\x00t9\x00--json", "get\x00author\x00--json", "get\x00reviewer\x00--json"}
	if strings.Join(got, " | ") != strings.Join(want, " | ") {
		t.Fatalf("writer control hc calls=%#v, want the caller get then the participants", got)
	}
}

// TestCheckParticipantsForWorkspaceHcMismatchRefusesBeforeAnyRead: under
// hc a known workspace that does not match the assignment refuses before
// any participant or caller read.
func TestCheckParticipantsForWorkspaceHcMismatchRefusesBeforeAnyRead(t *testing.T) {
	stateDir, a, env, bin := hcExplicitWsFixture(t, t.TempDir(), "t9", "sT9")
	err := CheckParticipantsForWorkspace(a, stateDir, "other", env)
	if err == nil || err.Error() != "collaboration belongs to another workspace" {
		t.Fatalf("err = %v, want the known-workspace refusal", err)
	}
	assertNoHcCallerGet(t, bin, "t9")
	if calls := herdrHcCalls(t, bin); len(calls) != 0 {
		t.Fatalf("hc calls=%#v, want none before the refusal", calls)
	}
	assertNoHerdrSentinel(t, bin)
}

// herdrHcCalls is the ordered argv of the fake hc calls.
func herdrHcCalls(t *testing.T, bin string) []string {
	t.Helper()
	calls, err := fakecli.ReadCalls(filepath.Join(bin, "hc.calls.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	out := []string{}
	for _, c := range calls {
		out = append(out, strings.Join(c.Argv, "\x00"))
	}
	return out
}

// assertNoHerdrSentinel fails when the fake herdr sentinel recorded a call.
func assertNoHerdrSentinel(t *testing.T, bin string) {
	t.Helper()
	if calls := herdrCalls(t, bin); len(calls) != 0 {
		t.Fatalf("the hc path reached herdr: %#v", calls)
	}
}
