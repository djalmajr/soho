package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// DJA-392 fatia 2 R2 (P2): the roster carries the worker's effective MCP
// choice as the last field (inherit or none), written by the spawn. reopen
// reads it and forwards it to the respawn as --mcp <value>, so a worker
// keeps the MCP it was opened with. A row without the field (written before
// the field existed) gets no --mcp: the normal resolution (config and
// frontmatter) decides, exactly like a fresh open.
//
// The first subtest ports the reviewer's probe
// (review-5-20261017T165145.probe.reopen_test.go): a pi implementer opened
// with --mcp inherit, reopened, keeps inherit and never gains --no-mcp.
// The probe's 15-field row is ported as the 16-field row the fixed spawn
// writes for --mcp inherit; the bare 15-field case is covered separately.

func reopenMcpRow(t *testing.T, f *releaseFixture, kind, mcp string) {
	t.Helper()
	row := []string{"worker", "p-worker", kind, "implementer", "alibaba", "1", f.cwd, "now", "", "ask", "implementer", "build", "", "--flag val", "high"}
	if mcp != "" {
		row = append(row, mcp)
	}
	if err := os.WriteFile(filepath.Join(f.state, "agents.tsv"), []byte("# header\n"+strings.Join(row, "\t")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func reopenWithReport(t *testing.T, f *releaseFixture) {
	t.Helper()
	report := filepath.Join(f.state, "reports", "worker.md")
	if err := os.MkdirAll(filepath.Dir(report), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(report, []byte("done\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(core.LastReportPath(f.state, "worker"), []byte(report+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func reopenStartArgv(run releaseRun) []string {
	for _, call := range run.calls {
		if len(call.Argv) >= 2 && call.Argv[0] == "agent" && call.Argv[1] == "start" {
			return call.Argv
		}
	}
	return nil
}

func setReopenPiMcpRules(t *testing.T, f *releaseFixture) {
	t.Helper()
	// The old row and its report refer to p-worker. The replacement
	// starts in the new tab's root pane and reports its own session.
	setReopenRulesWith(t, f, []fakecli.Rule{{
		Argv:   []string{"agent", "get", "worker"},
		Stdout: `{"result":{"agent":{"name":"worker","agent":"pi","pane_id":"p-new","agent_status":"idle","agent_session":{"source":"herdr:pi","agent":"pi","kind":"path","value":"/sessions/reopened-worker.jsonl"}}}}`,
	}})
}

func assertReopenPiLaunch(t *testing.T, run releaseRun, wantGets int) []string {
	t.Helper()
	start := reopenStartArgv(run)
	starts := callsTo(run.calls, start...)
	closes := callsTo(run.calls, "pane", "close", "p-worker")
	gets := callsTo(run.calls, "agent", "get", "worker")
	if !containsReopenArgPair(start, "--pane", "p-new") || len(starts) != 1 || len(closes) != 1 ||
		len(gets) != wantGets || closes[0] >= starts[0] || gets[0] <= starts[0] {
		t.Fatalf("reopen close calls=%v, start calls=%v argv=%q, replacement lookups=%v, want close, launch in p-new, then %d lookups",
			closes, starts, start, gets, wantGets)
	}
	return start
}

func reopenFinalRow(t *testing.T, f *releaseFixture) string {
	t.Helper()
	roster, err := os.ReadFile(filepath.Join(f.state, "agents.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(roster)), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "#") {
		t.Fatalf("the respawned row is missing: %q", roster)
	}
	return lines[1]
}

func TestReopenKeepsThePersistedMcpChoice(t *testing.T) {
	t.Run("a pi implementer opened with --mcp inherit keeps inherit (reviewer's probe)", func(t *testing.T) {
		f := newReopenFixture(t, "idle")
		reopenMcpRow(t, f, "pi", "inherit")
		reopenWithReport(t, f)
		setReopenPiMcpRules(t, f)
		if _, err := fakecli.Install(t, f.fakeDir, "pi", nil); err != nil {
			t.Fatal(err)
		}
		run := f.reopen(t, "worker")
		if run.code != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", run.code, run.stdout, run.stderr)
		}
		start := assertReopenPiLaunch(t, run, 1)
		for _, arg := range start {
			if arg == "--no-mcp" {
				t.Fatalf("reopen changed inherited MCP to none: %#v", start)
			}
		}
		if strings.Contains(run.stderr, "MCP servers") {
			t.Fatalf("an inherited choice must warn nothing: %q", run.stderr)
		}
		if fields := strings.Split(reopenFinalRow(t, f), "\t"); len(fields) != 16 || fields[15] != "inherit" {
			t.Fatalf("the respawned row lost the choice: %q", fields)
		}
	})

	t.Run("a persisted none travels to the kind's off flag", func(t *testing.T) {
		f := newReopenFixture(t, "idle")
		reopenMcpRow(t, f, "pi", "none")
		reopenWithReport(t, f)
		setReopenPiMcpRules(t, f)
		if _, err := fakecli.Install(t, f.fakeDir, "pi", nil); err != nil {
			t.Fatal(err)
		}
		run := f.reopen(t, "worker")
		if run.code != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", run.code, run.stdout, run.stderr)
		}
		start := assertReopenPiLaunch(t, run, 3)
		found := false
		for _, arg := range start {
			if arg == "--no-mcp" {
				found = true
			}
		}
		if !found {
			t.Fatalf("the persisted none never reached the argv: %#v", start)
		}
		if fields := strings.Split(reopenFinalRow(t, f), "\t"); len(fields) != 16 || fields[15] != "none" {
			t.Fatalf("the respawned row lost the choice: %q", fields)
		}
	})

	t.Run("an old row without the field passes no --mcp: the frontmatter decides", func(t *testing.T) {
		// The 15-field row predates the field: reopen must not pass --mcp,
		// so the normal resolution applies (implementer's mcp: none) and
		// the pi respawn carries the off flag.
		f := newReopenFixture(t, "idle")
		reopenMcpRow(t, f, "pi", "")
		reopenWithReport(t, f)
		setReopenPiMcpRules(t, f)
		if _, err := fakecli.Install(t, f.fakeDir, "pi", nil); err != nil {
			t.Fatal(err)
		}
		run := f.reopen(t, "worker")
		if run.code != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", run.code, run.stdout, run.stderr)
		}
		start := assertReopenPiLaunch(t, run, 3)
		found := false
		for _, arg := range start {
			if arg == "--no-mcp" {
				found = true
			}
		}
		if !found {
			t.Fatalf("the frontmatter mcp: none never reached the argv: %#v", start)
		}
		if fields := strings.Split(reopenFinalRow(t, f), "\t"); len(fields) != 16 || fields[15] != "none" {
			t.Fatalf("the respawned row must record the resolved choice: %q", fields)
		}
	})
}
