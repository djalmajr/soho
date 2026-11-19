package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func gcInUseWorkerGets(t *testing.T, g *idleGc) int {
	t.Helper()
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(g.f.env["SOHO_FAKECLI_CONFIG"], "herdr.json"))
	if err != nil {
		t.Fatal(err)
	}
	return len(callsTo(calls, "agent", "get", "worker"))
}

// R575: the in-use count adds no liveness read, so a later row is decided
// on the same read as on main. Young working copy first, old closed copy
// second; three working replies, then idle: three reads, both rows kept.
func TestGcInUseKeepsLivenessReadSequence(t *testing.T) {
	argv := []string{"agent", "get", "worker"}
	g := newIdleGc(t, []fakecli.Rule{
		{Argv: argv, Call: 1, Stdout: agentStatusJSON("working")},
		{Argv: argv, Call: 2, Stdout: agentStatusJSON("working")},
		{Argv: argv, Call: 3, Stdout: agentStatusJSON("working")},
		{Argv: argv, Stdout: agentStatusJSON("idle")},
	})
	recent := gcInUseSecondRow(t, g, "recent-working-copy")
	recent.Created = core.FrictionISO(platform.Now().Add(-time.Hour))
	g.f.writeCopies(t, []core.CopyRow{recent, g.row})
	before := g.f.copiesRows(t)
	if len(before) != 2 || before[0].Path != recent.Path || before[1].Path != g.copy {
		t.Fatalf("expected young row first and old row second: %v", before)
	}

	code, out, stderr := g.f.run(t, g.f.env, "gc", "--yes")
	var oldExists bool
	if _, err := os.Stat(g.copy); err == nil {
		oldExists = true
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if _, err := os.Stat(recent.Path); err != nil {
		t.Fatalf("recent copy not kept: %v", err)
	}
	after := g.f.copiesRows(t)
	t.Logf("gc --yes: code=%d stderr=%q stdout=%q worker_gets=%d old_copy_exists=%t rows_after=%d", code, stderr, out, gcInUseWorkerGets(t, g), oldExists, len(after))
	if code != 0 || stderr != "" || !oldExists || len(after) != 2 || gcInUseWorkerGets(t, g) != 3 {
		t.Errorf("expected three reads and both rows kept, as on main")
	}
}

// R575: an owner read idle by the idle check gets no further read.
func TestGcInUseNoExtraReadAfterIdle(t *testing.T) {
	g := newIdleGc(t, idleRules("idle"))
	if err := os.Remove(g.report); err != nil {
		t.Fatal(err)
	}
	g.register(t)
	code, out, stderr := g.f.run(t, g.f.env, "gc")
	gets := gcInUseWorkerGets(t, g)
	t.Logf("gc: code=%d stderr=%q stdout=%q worker_gets=%d", code, stderr, out, gets)
	if code != 0 || stderr != "" || gets != 2 {
		t.Errorf("expected classification and idle-eligibility reads, with no fallback after an observed idle state")
	}
}
