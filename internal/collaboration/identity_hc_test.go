package collaboration

// DJA-604 follow-up: ResolveParticipant under transport=hc trusts the
// session's own terminal record (the name, the address and the cwd of the
// `hc get` reply) instead of a herdr agent record; the kind, model, role,
// lane and start time stay the roster row's, and the Session is a stable
// stand-in over the address. A missing, finished or mismatching terminal
// returns the same errors the herdr branch returns. The fake hc is
// installed on PATH with the fakecli technique; a fake herdr sentinel
// fails the test if the hc path ever reaches herdr.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// hcIdentityFixture is one state dir with the participant roster row (its
// pane column is the hc address addr) and the fake hc answering
// `get <name> --json` with the given record (or failing the call). The
// fake herdr sentinel records any call.
func hcIdentityFixture(t *testing.T, repo, addr, record string, exit int) (string, platform.Env, string) {
	t.Helper()
	stateDir := t.TempDir()
	core.RosterAppend(stateDir, []string{"worker", addr, "codex", "implementer", "openai", "0", repo, "now", "gpt-5.4", "ask", "implementer", "build"})
	bin := t.TempDir()
	rule := fakecli.Rule{Argv: []string{"get", "worker", "--json"}}
	if exit != 0 {
		rule.Code = exit
	} else {
		rule.Stdout = record
	}
	if _, err := fakecli.Install(t, bin, "hc", []fakecli.Rule{rule}); err != nil {
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
	env["HC_TERMINAL_ID"] = "t1"
	return stateDir, env, bin
}

// assertNoHerdrCall fails when the fake herdr sentinel recorded a call.
func assertNoHerdrCall(t *testing.T, bin string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(bin, "herdr.calls.jsonl")); err == nil {
		t.Fatal("the hc path reached herdr")
	}
}

// TestResolveParticipantHcResolvesTheSessionTerminal is the happy path:
// the record's name, address and cwd match the roster row and the
// participant resolves with the row's fields, the EvalSymlinks'd cwd and
// the address stand-in Session.
func TestResolveParticipantHcResolvesTheSessionTerminal(t *testing.T) {
	repo := t.TempDir()
	repo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	record := `{"address":"t4","name":"worker","state":"live","cwd":` + jsonString(t, repo) + `,"status":{"state":"working","turn":1}}`
	stateDir, env, bin := hcIdentityFixture(t, repo, "t4", record, 0)
	live, err := ResolveParticipant(stateDir, "worker", env)
	if err != nil {
		t.Fatalf("the matching session terminal must resolve: %v", err)
	}
	if live.Name != "worker" || live.Pane != "t4" || live.Kind != "codex" || live.Role != "implementer" || live.Model != "gpt-5.4" || live.Started != "now" || live.Lane != "build" {
		t.Fatalf("participant = %#v, want the roster row's fields", live)
	}
	if live.Cwd != repo {
		t.Fatalf("live.Cwd = %q, want the EvalSymlinks'd registered cwd %q", live.Cwd, repo)
	}
	if want := Hash([]byte("hc\x00t4")); live.Session != want {
		t.Fatalf("live.Session = %q, want the address stand-in %q", live.Session, want)
	}
	assertNoHerdrCall(t, bin)
}

// TestResolveParticipantHcAddressMismatchIsIdentityChanged: the record's
// address no longer matches the roster's pane column — the existing
// identity-changed refusal.
func TestResolveParticipantHcAddressMismatchIsIdentityChanged(t *testing.T) {
	repo := t.TempDir()
	repo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	record := `{"address":"t9","name":"worker","state":"live","cwd":` + jsonString(t, repo) + `,"status":{"state":"working","turn":1}}`
	stateDir, env, bin := hcIdentityFixture(t, repo, "t4", record, 0)
	_, err = ResolveParticipant(stateDir, "worker", env)
	if err == nil || err.Error() != "participant pane, workspace or session no longer matches its registration" {
		t.Fatalf("err = %v, want the identity-changed refusal", err)
	}
	assertNoHerdrCall(t, bin)
}

// TestResolveParticipantHcNameMismatchIsIdentityChanged: the record's
// name no longer matches the roster name.
func TestResolveParticipantHcNameMismatchIsIdentityChanged(t *testing.T) {
	repo := t.TempDir()
	repo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	record := `{"address":"t4","name":"other","state":"live","cwd":` + jsonString(t, repo) + `,"status":{"state":"working","turn":1}}`
	stateDir, env, bin := hcIdentityFixture(t, repo, "t4", record, 0)
	_, err = ResolveParticipant(stateDir, "worker", env)
	if err == nil || err.Error() != "participant pane, workspace or session no longer matches its registration" {
		t.Fatalf("err = %v, want the identity-changed refusal", err)
	}
	assertNoHerdrCall(t, bin)
}

// TestResolveParticipantHcMissingOrFinishedTerminal: a missing terminal
// (the hc not-found exit) and a finished terminal both take the
// cannot-verify refusal.
func TestResolveParticipantHcMissingOrFinishedTerminal(t *testing.T) {
	repo := t.TempDir()
	repo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("missing", func(t *testing.T) {
		stateDir, env, bin := hcIdentityFixture(t, repo, "t4", "", 3)
		_, err := ResolveParticipant(stateDir, "worker", env)
		if err == nil || err.Error() != "cannot verify live collaboration participant worker" {
			t.Fatalf("err = %v, want the cannot-verify refusal", err)
		}
		assertNoHerdrCall(t, bin)
	})
	t.Run("finished", func(t *testing.T) {
		record := `{"address":"t4","name":"worker","state":"finished","cwd":` + jsonString(t, repo) + `}`
		stateDir, env, bin := hcIdentityFixture(t, repo, "t4", record, 0)
		_, err := ResolveParticipant(stateDir, "worker", env)
		if err == nil || err.Error() != "cannot verify live collaboration participant worker" {
			t.Fatalf("err = %v, want the cannot-verify refusal", err)
		}
		assertNoHerdrCall(t, bin)
	})
}

// TestResolveParticipantHcCwdChangedIsRefused: the record's cwd is no
// longer the registered cwd — the working-directory refusal.
func TestResolveParticipantHcCwdChangedIsRefused(t *testing.T) {
	repo := t.TempDir()
	repo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "else")
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	other, err = filepath.EvalSymlinks(other)
	if err != nil {
		t.Fatal(err)
	}
	record := `{"address":"t4","name":"worker","state":"live","cwd":` + jsonString(t, other) + `,"status":{"state":"working","turn":1}}`
	stateDir, env, bin := hcIdentityFixture(t, repo, "t4", record, 0)
	_, err = ResolveParticipant(stateDir, "worker", env)
	if err == nil || err.Error() != "participant working directory changed" {
		t.Fatalf("err = %v, want the working-directory refusal", err)
	}
	assertNoHerdrCall(t, bin)
}
