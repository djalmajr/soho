package collaboration

// F2 collaboration inspection seams: ActiveForSource reads the store
// through the source's retained root (a renamed or replaced state dir
// does not change the decision, and a capped read refuses instead of
// reporting no collaboration), and CheckParticipantsForRoster resolves
// the participants against the supplied roster rows (the inspection's
// acquired snapshot) instead of reopening the state roster.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// collabSourceFixture builds one valid active collaboration under a
// state root with a roster row for each participant and the fake
// herdr's matching agent records, and returns the fixture's pieces.
func collabSourceFixture(t *testing.T) (src *core.SessionSource, sd, base, repo string, env platform.Env, a Assignment) {
	t.Helper()
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("canonicalize the fixture repo: %v", err)
	}
	base = t.TempDir()
	sd = filepath.Join(base, "ws")
	if err := os.MkdirAll(sd, 0o700); err != nil {
		t.Fatal(err)
	}
	core.RosterAppend(sd, []string{"author", "p-author", "claude", "implementer", "anthropic", "pane-0", repo, "2026-10-04T12:00:00Z", "claude-sonnet-4-5", "", "implementer", ""})
	core.RosterAppend(sd, []string{"reviewer", "p-reviewer", "grok", "reviewer", "xai", "pane-0", repo, "2026-10-04T12:01:00Z", "grok-4", "", "reviewer", ""})
	agent := func(name, pane, kind, session string) string {
		return `{"result":{"agent":{"name":"` + name + `","pane_id":"` + pane + `","agent":"` + kind + `","workspace_id":"ws-1","cwd":` + jsonString(t, repo) + `,"agent_session":{"kind":"claude","value":"` + session + `"}}}}`
	}
	bin := t.TempDir()
	if _, err := fakecli.Install(t, bin, "herdr", []fakecli.Rule{
		{Argv: []string{"agent", "get", "author"}, Stdout: agent("author", "p-author", "claude", "sess-1")},
		{Argv: []string{"agent", "get", "reviewer"}, Stdout: agent("reviewer", "p-reviewer", "grok", "sess-2")},
	}); err != nil {
		t.Fatal(err)
	}
	env = platform.Env{}
	for _, pair := range fakecli.Env(testutil.CleanEnv(t), bin) {
		if k, v, ok := strings.Cut(pair, "="); ok {
			env[k] = v
		}
	}
	a = Assignment{
		Author: Participant{
			Name: "author", Pane: "p-author", Kind: "claude", Role: "implementer", Family: "anthropic",
			Started: "2026-10-04T12:00:00Z", Model: "claude-sonnet-4-5",
			Session: Hash([]byte("claude\x00sess-1")), Cwd: repo,
		},
		Reviewer: Participant{
			Name: "reviewer", Pane: "p-reviewer", Kind: "grok", Role: "reviewer", Family: "xai",
			Started: "2026-10-04T12:01:00Z", Model: "grok-4",
			Session: Hash([]byte("claude\x00sess-2")), Cwd: repo,
		},
		Workspace: "ws-1",
		Root:      platform.StateProjectRoot(env, repo),
		BriefHash: "0",
		Paths:     []string{repo},
		MaxRounds: 3,
	}
	created, err := Store{StateDir: sd}.Create(a)
	if err != nil {
		t.Fatalf("cannot seed the assignment: %v", err)
	}
	a = created
	src, err = core.OpenSessionSource(base, "ws", "")
	if err != nil {
		t.Fatalf("OpenSessionSource: %v", err)
	}
	t.Cleanup(func() { _ = src.Close() })
	return src, sd, base, repo, env, a
}

// TestActiveForSourcePinnedRoot: the source-bound store read finds the
// active collaboration before the swap; after the state session dir is
// renamed and replaced by a link to a decoy with no collaboration dir,
// the source-bound read keeps the retained collaboration while the raw
// store reader follows the link and sees none.
func TestActiveForSourcePinnedRoot(t *testing.T) {
	src, sd, _, _, _, a := collabSourceFixture(t)
	got, err := ActiveForSource(src, "p-author")
	if err != nil || got == nil || got.ID != a.ID {
		t.Fatalf("control ActiveForSource = %v, %v; want the active collaboration", got, err)
	}
	raw, err := Store{StateDir: sd}.ActiveFor("p-author")
	if err != nil || raw == nil || raw.ID != a.ID {
		t.Fatalf("control raw ActiveFor = %v, %v; want the same collaboration", raw, err)
	}
	// Rename and swap the session dir; the decoy has no collaboration
	// dir.
	if err := os.Rename(sd, sd+"-retained"); err != nil {
		t.Fatalf("cannot rename the session dir: %v", err)
	}
	decoy := filepath.Join(filepath.Dir(sd), "decoy")
	if err := os.MkdirAll(decoy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(decoy, sd); err != nil {
		t.Skipf("the test user cannot create symlinks: %v", err)
	}
	got, err = ActiveForSource(src, "p-author")
	if err != nil || got == nil || got.ID != a.ID {
		t.Fatalf("pinned ActiveForSource = %v, %v; want the retained collaboration (the decoy's absence must not apply)", got, err)
	}
	raw, err = Store{StateDir: sd}.ActiveFor("p-author")
	if err != nil || raw != nil {
		t.Fatalf("raw ActiveFor = %v, %v; want no collaboration (the raw path follows the link to the decoy)", raw, err)
	}
}

// TestActiveForSourceBounds: a record over the inspection cap refuses
// through the source's confined read (an error, never nil — the store
// failure stands), and a directory over the enumeration cap refuses
// the same way.
func TestActiveForSourceBounds(t *testing.T) {
	src, sd, _, _, _, _ := collabSourceFixture(t)
	// (a) The seeded record is well under the cap: found.
	if _, err := ActiveForSource(src, "p-author"); err != nil {
		t.Fatalf("control ActiveForSource: %v", err)
	}
	// (b) A second member with an overlong record refuses.
	record := filepath.Join(sd, "collaboration", "c-0123456789abcdef", "assignment.json")
	if err := os.MkdirAll(filepath.Dir(record), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(record, []byte(strings.Repeat("x", activeSourceRecordMax+4096)), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := ActiveForSource(src, "p-author"); err == nil || got != nil {
		t.Fatalf("overlong record: ActiveForSource = %v, %v; want a refusal that is not a found collaboration", got, err)
	} else if errors.Is(err, os.ErrNotExist) {
		t.Fatalf("overlong record collapsed to absence: %v", err)
	}
	// (c) The enumeration cap: a store dir over the 4096-entry cap
	// refuses.
	src2, sd2, _, _, _, _ := collabSourceFixture(t)
	for i := 0; i < 4097; i++ {
		if err := os.MkdirAll(filepath.Join(sd2, "collaboration", itoaPad4(i)), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ActiveForSource(src2, "p-author"); err == nil {
		t.Fatalf("over-cap store dir was accepted")
	}
}

// TestCheckParticipantsForRosterRows: the roster-row participant check
// resolves each participant against the supplied rows (the snapshot)
// and never reopens the state roster: the same valid assignment passes
// with the source's snapshot rows, and a row lookup that reports no row
// fails with the registration refusal — even though the state roster
// still carries both rows.
func TestCheckParticipantsForRosterRows(t *testing.T) {
	src, sd, _, _, env, a := collabSourceFixture(t)
	rowFor := func(name string) []string {
		row, ok := src.Roster().Row(name)
		if !ok {
			return nil
		}
		return row.Fields
	}
	if err := CheckParticipantsForRoster(a, rowFor, "ws-1", env); err != nil {
		t.Fatalf("snapshot rows: the valid collaboration must verify against its own snapshot rows: %v", err)
	}
	// The state roster is dropped: the snapshot rows still verify, the
	// state-roster reader cannot.
	if err := os.Remove(filepath.Join(sd, "agents.tsv")); err != nil {
		t.Fatal(err)
	}
	if err := CheckParticipantsForRoster(a, rowFor, "ws-1", env); err != nil {
		t.Fatalf("snapshot rows after the roster change: %v (the acquired rows must stay the authority)", err)
	}
	if err := CheckParticipantsForWorkspace(a, sd, "ws-1", env); err == nil {
		t.Fatalf("the state roster is gone; the state-roster reader must fail")
	}
	// A row lookup that reports no row is the registration refusal.
	none := func(string) []string { return nil }
	if err := CheckParticipantsForRoster(a, none, "ws-1", env); err == nil {
		t.Fatalf("the missing snapshot rows must refuse the participant check")
	}
}

// itoaPad4 renders an index as a four-digit collaboration-id-style name
// without an extra import.
func itoaPad4(i int) string {
	const digits = "0123456789"
	if i == 0 {
		return "c-0000"
	}
	var b [4]byte
	for pos := 3; pos >= 0; pos-- {
		b[pos] = digits[i%10]
		i /= 10
	}
	return "c-" + string(b[:])
}
