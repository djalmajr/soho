package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// A12: `herdr-soho reopen <name>` is release <name> --close (same refusals,
// --force passed through) followed by spawn <role> --fresh with the roster's
// kind, model, effort, cwd and native args, called as functions. The output
// is the spawn JSON.

func reopenRow(t *testing.T, state, agent, wdir string) {
	t.Helper()
	row := strings.Join([]string{agent, "p-" + agent, "grok", "implementer", "xai", "1", wdir, "now", "grok-4.7", "ask", "implementer", "build", "", "--flag val", "high"}, "\t")
	if err := os.WriteFile(filepath.Join(state, "agents.tsv"), []byte("# header\n"+row+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func newReopenFixture(t *testing.T, agentStatus string) *releaseFixture {
	t.Helper()
	f := newReleaseFixture(t, agentStatus, "")
	// The release fixture writes a worker row on /tmp/work; reopen must
	// respawn in the spawn's own cwd, so point the row at the fixture repo.
	reopenRow(t, f.state, "worker", f.cwd)
	return f
}

func reopenRules(t *testing.T, f *releaseFixture) []fakecli.Rule {
	t.Helper()
	return []fakecli.Rule{
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`},
		{Argv: []string{"pane", "close", "p-worker"}},
		{Argv: []string{"agent", "start"}, ArgvPrefix: true, Stdout: `{"result":{"started":true}}`},
		{Argv: []string{"agent", "get"}, ArgvPrefix: true, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
		{Argv: []string{"agent", "read"}, ArgvPrefix: true, Stdout: "ready\n"},
		{Argv: []string{"pane", "list"}, ArgvPrefix: true, Stdout: `{"result":{"panes":[]}}`},
		{Argv: []string{"pane", "layout"}, ArgvPrefix: true, Stdout: `{"result":{"layout":{"area":{"width":1000,"height":700},"panes":[{"pane_id":"p1","rect":{"x":0,"y":0,"width":1000,"height":700}}]}}}`},
		{Argv: []string{"pane", "split"}, ArgvPrefix: true, Stdout: `{"result":{"pane":{"pane_id":"p2"}}}`},
		{Argv: []string{"tab", "create"}, ArgvPrefix: true, Stdout: `{"result":{"tab":{"tab_id":"t-herd","label":"herd"},"root_pane":{"pane_id":"p-new"}}}`},
		{Argv: []string{"tab", "get"}, ArgvPrefix: true, Stdout: `{"result":{"tab":{"tab_id":"t-herd","label":"herd"},"root_pane":{"pane_id":"p-root"}}}`},
		{Argv: []string{"tab", "rename"}, ArgvPrefix: true},
	}
}

// setReopenRules rewrites the fake herdr rules config (newReleaseFixture
// already installed the fake binary itself, so a second Install would fail).
func setReopenRules(t *testing.T, f *releaseFixture) {
	t.Helper()
	config := struct {
		Log   string         `json:"log"`
		Rules []fakecli.Rule `json:"rules"`
	}{Log: filepath.Join(f.fakeDir, "herdr.calls.jsonl"), Rules: reopenRules(t, f)}
	b, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.configFile, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f *releaseFixture) reopen(t *testing.T, args ...string) releaseRun {
	t.Helper()
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, stderr bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &stderr
	code := Run(append([]string{"reopen"}, args...), f.env)
	platform.Stdout, platform.Stderr = oldOut, oldErr
	calls, err := fakecli.ReadCallsForConfig(f.configFile)
	if os.IsNotExist(err) {
		calls = []fakecli.Call{}
	} else if err != nil {
		t.Fatal(err)
	}
	return releaseRun{code, out.String(), stderr.String(), calls, f.state, f.configFile}
}

func containsReopenArgPair(args []string, key, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key && args[i+1] == value {
			return true
		}
	}
	return false
}

func TestReopenClosesAndReopensWithRosterValues(t *testing.T) {
	f := newReopenFixture(t, "idle")
	// The report exists and is non-empty: release does not refuse.
	report := filepath.Join(f.state, "reports", "worker.md")
	if err := os.MkdirAll(filepath.Dir(report), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(report, []byte("# done\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(core.LastReportPath(f.state, "worker"), []byte(report+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	setReopenRules(t, f)
	f.env["SOHO_LAYOUT"] = "split"

	run := f.reopen(t, "worker")
	if run.code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", run.code, run.stdout, run.stderr)
	}
	// The output is the spawn JSON only: the release half's stdout is gone.
	if strings.Contains(run.stdout, "released") {
		t.Fatalf("release stdout leaked into the reopen output: %q", run.stdout)
	}
	for _, want := range []string{`"name": "worker"`, `"kind": "grok"`, `"model": "grok-4.7"`, `"effort": "high"`, `"created_pane": true`, `"status": "ready"`, `"role": "implementer"`} {
		if !strings.Contains(run.stdout, want) {
			t.Fatalf("spawn JSON missing %s: %s", want, run.stdout)
		}
	}
	var start []string
	for _, call := range run.calls {
		if len(call.Argv) >= 2 && call.Argv[0] == "agent" && call.Argv[1] == "start" {
			start = call.Argv
		}
		if len(call.Argv) >= 2 && call.Argv[0] == "agent" && call.Argv[1] == "rename" {
			t.Fatalf("reopen renamed a worker: %#v", call)
		}
	}
	// The respawn carries the roster's model, effort and native args.
	if !containsReopenArgPair(start, "--model", "grok-4.7") || !containsReopenArgPair(start, "--reasoning-effort", "high") || !containsReopenArgPair(start, "--flag", "val") {
		t.Fatalf("agent start argv=%#v", start)
	}
	closed := false
	for _, call := range run.calls {
		if len(call.Argv) >= 2 && call.Argv[0] == "pane" && call.Argv[1] == "close" {
			closed = true
		}
	}
	if !closed {
		t.Fatalf("release --close did not close the pane: %#v", run.calls)
	}
	// The release row was replaced by the respawned worker's row; the CLI
	// `--` args go to the herdr argv (checked above), while the roster's
	// native-args column stores the configured args (none here).
	roster, err := os.ReadFile(filepath.Join(f.state, "agents.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(roster), "\n") != 2 || !strings.Contains(string(roster), "worker\tp-new\tgrok\timplementer") || !strings.Contains(string(roster), "\tgrok-4.7\t") || !strings.HasSuffix(strings.TrimSpace(string(roster)), "high\tnone") {
		// DJA-392 fatia 2 R2: the row ends with the effective MCP choice.
		// The 15-field row predates it, so reopen passes no --mcp and the
		// implementer frontmatter (mcp: none) decides: the row records none.
		t.Fatalf("roster=%q", roster)
	}
}

func TestReopenRefusesAWorkingWorkerWithoutReport(t *testing.T) {
	f := newReopenFixture(t, "working")
	// A report pointer exists but the report was never written.
	report := filepath.Join(f.state, "reports", "worker.md")
	if err := os.MkdirAll(filepath.Dir(report), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(core.LastReportPath(f.state, "worker"), []byte(report+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	setReopenRules(t, f)
	run := f.reopen(t, "worker")
	if run.code != 3 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", run.code, run.stdout, run.stderr)
	}
	if !strings.Contains(run.stderr, "is still working and has not written") || !strings.Contains(run.stderr, "release --close --force") {
		t.Fatalf("stderr=%q", run.stderr)
	}
	started := false
	for _, call := range run.calls {
		if len(call.Argv) >= 2 && call.Argv[0] == "agent" && call.Argv[1] == "start" {
			started = true
		}
	}
	if started {
		t.Fatalf("a refused reopen still spawned: %#v", run.calls)
	}
	roster, err := os.ReadFile(filepath.Join(f.state, "agents.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(roster), "worker\tp-worker") {
		t.Fatalf("the refused reopen touched the roster: %q", roster)
	}
}

func TestReopenForcePassesThroughTheReleaseRefusal(t *testing.T) {
	f := newReopenFixture(t, "working")
	report := filepath.Join(f.state, "reports", "worker.md")
	if err := os.MkdirAll(filepath.Dir(report), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(core.LastReportPath(f.state, "worker"), []byte(report+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	setReopenRules(t, f)
	run := f.reopen(t, "worker", "--force")
	if run.code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", run.code, run.stdout, run.stderr)
	}
	if !strings.Contains(run.stdout, `"name": "worker"`) || !strings.Contains(run.stdout, `"created_pane": true`) {
		t.Fatalf("spawn JSON=%s", run.stdout)
	}
}

func TestReopenUsageAndUnknownName(t *testing.T) {
	f := newReopenFixture(t, "idle")
	setReopenRules(t, f)
	if run := f.reopen(t); run.code != 2 {
		t.Fatalf("missing name exit=%d stderr=%q", run.code, run.stderr)
	}
	if run := f.reopen(t, "worker", "--bogus"); run.code != 2 {
		t.Fatalf("unknown option exit=%d stderr=%q", run.code, run.stderr)
	}
	if run := f.reopen(t, "ghost"); run.code != 3 || !strings.Contains(run.stderr, "agent 'ghost' is not in the roster") {
		t.Fatalf("unknown name exit=%d stderr=%q", run.code, run.stderr)
	}
}
