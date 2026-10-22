package spawn

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

type spawnFixture struct {
	root, cwd, state, bin string
	env                   platform.Env
	ctx                   *core.Config
}

func newSpawnFixture(t *testing.T, rules []fakecli.Rule) spawnFixture {
	t.Helper()
	// The tests of this package call CmdSpawn directly, without cli.Run
	// (which resets the caller-pane memo): each fixture starts with a
	// clean memo so the assertions never depend on test order (DJA-394).
	herdr.ResetCallerCheck()
	root := t.TempDir()
	f := spawnFixture{root: root, cwd: filepath.Join(root, "repo"), state: filepath.Join(root, "state"), bin: filepath.Join(root, "bin")}
	for _, dir := range []string{f.cwd, f.state, f.bin, filepath.Join(root, "roles"), filepath.Join(root, "skill", "roles")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fakecli.Install(t, f.bin, "herdr", rules); err != nil {
		t.Fatal(err)
	}
	base := testutil.CleanEnv(t)
	f.env = platform.Env{}
	for _, item := range fakecli.Env(base, f.bin) {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			f.env[key] = value
		}
	}
	f.env["HOME"] = root
	f.env["USERPROFILE"] = root
	f.env["XDG_CONFIG_HOME"] = root
	f.env["SOHO_DIR"] = f.state
	f.env["HERDR_WORKSPACE_ID"] = "ws"
	f.env["SOHO_SKILL_DIR"] = filepath.Join(root, "skill")
	f.env["SOHO_ROLES"] = filepath.Join(root, "roles")
	f.env["SOHO_LANES"] = "off"
	f.ctx = &core.Config{Entries: map[string]core.ConfigEntry{}}
	if err := os.MkdirAll(filepath.Join(f.state, "ws"), 0o700); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f spawnFixture) roster(t *testing.T, rows ...string) {
	t.Helper()
	body := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\tburst\targs\teffort\n"
	if len(rows) > 0 {
		body += strings.Join(rows, "\n") + "\n"
	}
	if err := os.WriteFile(filepath.Join(f.state, "ws", "agents.tsv"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func spawnRosterRow(name, role, model, approvals, args string) string {
	return strings.Join([]string{name, "p-" + name, "grok", role, "xai", "1", "/tmp/work", "now", model, approvals, role, "", "", args, "high"}, "\t")
}

func idleHerdrRule(name string) fakecli.Rule {
	return fakecli.Rule{Argv: []string{"agent", "get", name}, Stdout: `{"result":{"agent":{"name":"` + name + `","agent_status":"idle"}}}`}
}

func runCmdSpawn(t *testing.T, f spawnFixture, args []string) (code int, stdout, stderr string, calls []fakecli.Call) {
	t.Helper()
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, errOut strings.Builder
	platform.Stdout, platform.Stderr = &out, &errOut
	t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
	code = 0
	func() {
		defer func() {
			if value := recover(); value != nil {
				if e, ok := value.(*platform.ExitError); ok {
					code = e.Code
					return
				}
				panic(value)
			}
		}()
		CmdSpawn(args, f.ctx, f.env, f.cwd)
	}()
	calls, _ = fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
	return code, out.String(), errOut.String(), calls
}

func freshSpawnRules() []fakecli.Rule {
	return []fakecli.Rule{
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`},
		{Argv: []string{"agent", "start"}, ArgvPrefix: true, Stdout: `{"result":{"started":true}}`},
		{Argv: []string{"agent", "get"}, ArgvPrefix: true, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
		{Argv: []string{"agent", "read"}, ArgvPrefix: true, Stdout: "working\n"},
		{Argv: []string{"pane", "list"}, ArgvPrefix: true, Stdout: `{"result":{"panes":[]}}`},
		{Argv: []string{"pane", "layout"}, ArgvPrefix: true, Stdout: `{"result":{"layout":{"area":{"width":1000,"height":700},"panes":[{"pane_id":"p1","rect":{"x":0,"y":0,"width":1000,"height":700}}]}}`},
		{Argv: []string{"pane", "split"}, ArgvPrefix: true, Stdout: `{"result":{"pane":{"pane_id":"p2"}}}`},
		{Argv: []string{"pane", "close"}, ArgvPrefix: true},
		{Argv: []string{"tab", "create"}, ArgvPrefix: true, Stdout: `{"result":{"tab":{"tab_id":"t-herd","label":"herd"},"root_pane":{"pane_id":"p-new"}}}`},
		{Argv: []string{"tab", "get"}, ArgvPrefix: true, Stdout: `{"result":{"tab":{"tab_id":"t-herd","label":"herd"},"root_pane":{"pane_id":"p-root"}}}`},
		{Argv: []string{"tab", "rename"}, ArgvPrefix: true},
	}
}

func configureSpawnFixture(t *testing.T, f *spawnFixture) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.root, "roles", "worker.md"), []byte("---\nkind: grok\nmodel: grok-4.7\neffort: high\napprovals: ask\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fakecli.Install(t, f.bin, "grok", nil); err != nil {
		t.Fatal(err)
	}
	// Read repository role fixtures relative to the package directory so the
	// cross-compiled tests do not retain a developer-machine path.
	sourceRoles := filepath.Join("..", "..", "skills", "soho", "roles")
	entries, err := os.ReadDir(sourceRoles)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(sourceRoles, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.root, "skill", "roles", entry.Name()), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f.env["SOHO_ROLES"] = filepath.Join(f.root, "roles")
	f.env["SOHO_LANES"] = "off"
	f.env["SOHO_LAYOUT"] = "split"
	f.env["SOHO_REGRID"] = "off"
	f.env["SOHO_WAIT_POLL_MS"] = "1"
	f.env["HERDR_ENV"] = "1"
}

func TestFindReusableJavaScriptCases(t *testing.T) {
	cases := []struct {
		js        string
		rows      []string
		rules     []fakecli.Rule
		role      string
		model     string
		approvals string
		want      string
	}{
		// JS: "findReusable: same model + approvals is reused across roles"
		{js: "findReusable: same model + approvals is reused across roles", rows: []string{spawnRosterRow("worker", "implementer", "grok-4.7", "full", "")}, rules: []fakecli.Rule{idleHerdrRule("worker")}, role: "scouter", model: "grok-4.7", approvals: "ask", want: "worker"},
		// JS: "findReusable: the same role wins over an earlier other role"
		{js: "findReusable: the same role wins over an earlier other role", rows: []string{spawnRosterRow("cross", "implementer", "grok-4.7", "full", ""), spawnRosterRow("same", "scouter", "grok-4.7", "ask", "")}, rules: []fakecli.Rule{idleHerdrRule("cross"), idleHerdrRule("same")}, role: "scouter", model: "grok-4.7", approvals: "ask", want: "same"},
		// JS: "findReusable: a different resolved model is not reused"
		{js: "findReusable: a different resolved model is not reused", rows: []string{spawnRosterRow("worker", "scouter", "grok-4", "full", "")}, rules: []fakecli.Rule{idleHerdrRule("worker")}, role: "scouter", model: "grok-4.7", approvals: "ask"},
		// JS: "findReusable: approvals — below never, equal and above yes, unknown request never"
		{js: "findReusable: approvals — below never, equal and above yes, unknown request never", rows: []string{spawnRosterRow("ask", "scouter", "grok-4.7", "ask", ""), spawnRosterRow("full", "scouter", "grok-4.7", "full", "")}, rules: []fakecli.Rule{idleHerdrRule("ask"), idleHerdrRule("full")}, role: "implementer", model: "grok-4.7", approvals: "full", want: "full"},
		// JS: "findReusable: edited workers never become a review role"
		{js: "findReusable: edited workers never become a review role", rows: []string{spawnRosterRow("worker", "implementer", "grok-4.7", "full", "")}, rules: []fakecli.Rule{idleHerdrRule("worker")}, role: "reviewer", model: "grok-4.7", approvals: "ask"},
		// JS: "findReusable: a frontmatter mode: edit role cannot become inspector"
		{js: "findReusable: a frontmatter mode: edit role cannot become inspector", rows: []string{spawnRosterRow("worker", "editor", "grok-4.7", "full", "")}, rules: []fakecli.Rule{idleHerdrRule("worker")}, role: "inspector", model: "grok-4.7", approvals: "ask"},
		// JS: "findReusable: old 8-column lines are only reused for the same role"
		// DJA-483 supersedes the legacy expectation: no opening effort, no reuse.
		{js: "findReusable: old 8-column lines are only reused for the same role", rows: []string{"old\tp-old\tgrok\timplementer\txai\t1\t/tmp/work\tnow"}, rules: []fakecli.Rule{idleHerdrRule("old")}, role: "implementer", model: "", approvals: "ask"},
		// JS: "findReusable: multi_role=off refuses another role, still reuses the same role"
		{js: "findReusable: multi_role=off refuses another role, still reuses the same role", rows: []string{spawnRosterRow("worker", "implementer", "grok-4.7", "full", "")}, rules: []fakecli.Rule{idleHerdrRule("worker")}, role: "scouter", model: "grok-4.7", approvals: "ask"},
		// JS: "findReusable: the roster column 14 (the args the worker opened with) gates reuse"
		{js: "findReusable: the roster column 14 (the args the worker opened with) gates reuse", rows: []string{spawnRosterRow("worker", "implementer", "grok-4.7", "full", "--old")}, rules: []fakecli.Rule{idleHerdrRule("worker")}, role: "scouter", model: "grok-4.7", approvals: "ask"},
		// JS: "findReusable: cross-role reuse requires the same recorded and requested model"
		{js: "findReusable: cross-role reuse requires the same recorded and requested model", rows: []string{spawnRosterRow("worker", "implementer", "grok-4", "full", "")}, rules: []fakecli.Rule{idleHerdrRule("worker")}, role: "scouter", model: "grok-4.7", approvals: "ask"},
		// JS: "findReusable: the same role — the recorded model and approvals must line up"
		{js: "findReusable: the same role — the recorded model and approvals must line up", rows: []string{spawnRosterRow("worker", "scouter", "grok-4", "ask", "")}, rules: []fakecli.Rule{idleHerdrRule("worker")}, role: "scouter", model: "grok-4.7", approvals: "full"},
		// JS: "findReusable: a busy same-role worker does not block another role"
		{js: "findReusable: a busy same-role worker does not block another role", rows: []string{spawnRosterRow("busy", "scouter", "grok-4.7", "full", ""), spawnRosterRow("idle", "implementer", "grok-4.7", "full", "")}, rules: []fakecli.Rule{{Argv: []string{"agent", "get", "busy"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`}, idleHerdrRule("idle")}, role: "scouter", model: "grok-4.7", approvals: "ask", want: "idle"},
		// JS: "findReusable: an unqueryable same-role match blocks (unavailable)"
		{js: "findReusable: an unqueryable same-role match blocks (unavailable)", rows: []string{spawnRosterRow("worker", "scouter", "grok-4.7", "full", "")}, rules: []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stderr: `{"error":{"code":"permission_denied","message":"unavailable"}}`, Code: 1}}, role: "scouter", model: "grok-4.7", approvals: "ask", want: "!blocked"},
		// JS: "findReusable: idle same-role sibling wins over unqueryable; dead is absence (test-status.sh)"
		{js: "findReusable: idle same-role sibling wins over unqueryable; dead is absence (test-status.sh)", rows: []string{spawnRosterRow("dead", "scouter", "grok-4.7", "full", ""), spawnRosterRow("idle", "scouter", "grok-4.7", "full", "")}, rules: []fakecli.Rule{{Argv: []string{"agent", "get", "dead"}, Stderr: `{"error":{"code":"agent_not_found","message":"gone"}}`, Code: 1}, idleHerdrRule("idle")}, role: "scouter", model: "grok-4.7", approvals: "ask", want: "idle"},
		// JS: "findReusable: done is reusable; an empty last report is not"
		{js: "findReusable: done is reusable; an empty last report is not", rows: []string{spawnRosterRow("worker", "scouter", "grok-4.7", "full", "")}, rules: []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"done"}}}`}}, role: "scouter", model: "grok-4.7", approvals: "ask", want: "worker"},
		// JS: "findReusable: a different cwd or kind is not reused"
		{js: "findReusable: a different cwd or kind is not reused", rows: []string{spawnRosterRow("worker", "scouter", "grok-4.7", "full", "")}, rules: []fakecli.Rule{idleHerdrRule("worker")}, role: "scouter", model: "grok-4.7", approvals: "ask"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.js, func(t *testing.T) {
			f := newSpawnFixture(t, tc.rules)
			f.roster(t, tc.rows...)
			if tc.js == "findReusable: multi_role=off refuses another role, still reuses the same role" {
				f.ctx.Entries["multi_role"] = core.ConfigEntry{Value: "off", Source: "user"}
			}
			if tc.js == "findReusable: a frontmatter mode: edit role cannot become inspector" {
				roleFile := filepath.Join(f.root, "roles", "editor.md")
				if err := os.WriteFile(roleFile, []byte("---\nmode: edit\nkind: grok\n---\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.js == "findReusable: edited workers never become a review role" {
				f.env["SOHO_ROLES"] = filepath.Join(f.root, "roles")
				if err := os.WriteFile(filepath.Join(f.root, "roles", "implementer.md"), []byte("---\nmode: edit\nkind: grok\n---\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.js == "findReusable: the roster column 14 (the args the worker opened with) gates reuse" {
				f.ctx.Entries["role_scouter_args"] = core.ConfigEntry{Value: "--new", Source: "user"}
			}
			if tc.js == "findReusable: a different cwd or kind is not reused" {
				f.roster(t, strings.Replace(tc.rows[0], "grok", "claude", 1))
			}
			got, unavailable, blocked := FindReusable(tc.role, "grok", "/tmp/work", "", tc.model, tc.approvals, "high", f.ctx, f.env, f.cwd)
			switch tc.want {
			case "!blocked":
				if got != "" || !blocked || unavailable == "" {
					t.Fatalf("got=%q unavailable=%q blocked=%v", got, unavailable, blocked)
				}
			case "":
				if got != "" || blocked {
					t.Fatalf("got=%q unavailable=%q blocked=%v", got, unavailable, blocked)
				}
			default:
				if got != tc.want || blocked {
					t.Fatalf("got=%q unavailable=%q blocked=%v, want %q", got, unavailable, blocked, tc.want)
				}
			}
			if tc.js == "findReusable: approvals — below never, equal and above yes, unknown request never" {
				if got, _, _ := FindReusable(tc.role, "grok", "/tmp/work", "", tc.model, "ask", "high", f.ctx, f.env, f.cwd); got != "ask" {
					t.Fatalf("equal approval candidate=%q, want ask", got)
				}
				f.roster(t, spawnRosterRow("full", "scouter", "grok-4.7", "full", ""))
				if got, _, _ := FindReusable(tc.role, "grok", "/tmp/work", "", tc.model, "ask", "high", f.ctx, f.env, f.cwd); got != "full" {
					t.Fatalf("higher approval candidate=%q, want full", got)
				}
				if got, _, _ := FindReusable(tc.role, "grok", "/tmp/work", "", tc.model, "FULL", "high", f.ctx, f.env, f.cwd); got != "" {
					t.Fatalf("unknown requested approval reused %q", got)
				}
			}
			if tc.js == "findReusable: old 8-column lines are only reused for the same role" {
				if got, _, _ := FindReusable("scouter", "grok", "/tmp/work", "", "", "ask", "high", f.ctx, f.env, f.cwd); got != "" {
					t.Fatalf("cross-role reused old 8-column row %q", got)
				}
			}
			if tc.js == "findReusable: multi_role=off refuses another role, still reuses the same role" {
				f.roster(t, spawnRosterRow("worker", "scouter", "grok-4.7", "full", ""))
				if got, _, _ := FindReusable("scouter", "grok", "/tmp/work", "", "grok-4.7", "ask", "high", f.ctx, f.env, f.cwd); got != "worker" {
					t.Fatalf("same-role reuse with multi_role=off got=%q", got)
				}
			}
			if tc.js == "findReusable: a different cwd or kind is not reused" {
				f.roster(t, strings.Join([]string{"worker", "p-worker", "grok", "scouter", "xai", "1", "/elsewhere", "now", "grok-4.7", "full", "scouter", "", "", "", "high"}, "\t"))
				if got, _, _ := FindReusable("scouter", "grok", "/tmp/work", "", "grok-4.7", "ask", "high", f.ctx, f.env, f.cwd); got != "" {
					t.Fatalf("different cwd reused %q", got)
				}
			}
			if tc.js == "findReusable: done is reusable; an empty last report is not" {
				f.roster(t, spawnRosterRow("worker", "scouter", "grok-4.7", "full", ""))
				report := filepath.Join(f.root, "empty-report.md")
				if err := os.WriteFile(report, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(f.state, "ws", "last-report-worker"), []byte(report+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if got, _, _ := FindReusable("scouter", "grok", "/tmp/work", "", "grok-4.7", "ask", "high", f.ctx, f.env, f.cwd); got != "" {
					t.Fatalf("empty report reused %q", got)
				}
				if err := os.WriteFile(report, []byte("done\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if got, _, _ := FindReusable("scouter", "grok", "/tmp/work", "", "grok-4.7", "ask", "high", f.ctx, f.env, f.cwd); got != "worker" {
					t.Fatalf("non-empty report not reusable: %q", got)
				}
			}
		})
	}
}

func TestSpawnHelperJavaScriptCases(t *testing.T) {
	t.Run("uniqueName: base, base-2, base-3… skipping the live names", func(t *testing.T) {
		// JS: "uniqueName: base, base-2, base-3… skipping the live names"
		f := newSpawnFixture(t, []fakecli.Rule{{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[{"name":"worker"},{"name":"worker-2"}]}}`}})
		if got := UniqueName("worker", f.env); got != "worker-3" {
			t.Fatalf("UniqueName()=%q", got)
		}
	})

	t.Run("ensureOrchestratorName: renames the caller, idempotent, silent without an agent", func(t *testing.T) {
		// JS: "ensureOrchestratorName: renames the caller, idempotent, silent without an agent"
		f := newSpawnFixture(t, []fakecli.Rule{
			{Argv: []string{"agent", "get", "p1"}, Call: 1, Stdout: `{"result":{"agent":{"name":"caller","agent_status":"working"}}}`},
			{Argv: []string{"agent", "get", "p1"}, Call: 2, Stdout: `{"result":{"agent":{"name":"caller","agent_status":"working"}}}`},
			{Argv: []string{"agent", "get", "p1"}, Call: 3, Stdout: `{"result":{"agent":{"name":"orchestrator","agent_status":"working"}}}`},
			{Argv: []string{"agent", "get", "p1"}, Call: 4, Stdout: `{"result":{"agent":{"name":"orchestrator","agent_status":"working"}}}`},
			{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`},
			{Argv: []string{"agent", "rename", "p1", "orchestrator"}},
		})
		f.env["HERDR_PANE_ID"] = "p1"
		f.env["HERDR_AGENT_NAME"] = "caller"
		if got := EnsureOrchestratorName(f.ctx, f.env, f.cwd, "test"); got != "orchestrator" {
			t.Fatalf("name=%q", got)
		}
		f.env["HERDR_AGENT_NAME"] = "orchestrator"
		if got := EnsureOrchestratorName(f.ctx, f.env, f.cwd, "test"); got != "orchestrator" {
			calls, _ := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
			t.Fatalf("idempotent name=%q calls=%#v", got, calls)
		}
		delete(f.env, "HERDR_PANE_ID")
		if got := EnsureOrchestratorName(f.ctx, f.env, f.cwd, "test"); got != "" {
			t.Fatalf("without pane name=%q", got)
		}
	})

	t.Run("resolvedRoleKind: config beats frontmatter, frontmatter is the floor", func(t *testing.T) {
		// JS: "resolvedRoleKind: config beats frontmatter, frontmatter is the floor"
		f := newSpawnFixture(t, nil)
		if err := os.WriteFile(filepath.Join(f.root, "roles", "worker.md"), []byte("---\nkind: grok\n---\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := ResolvedRoleKind("worker", f.ctx, f.env, f.cwd); got != "grok" {
			t.Fatalf("frontmatter kind=%q", got)
		}
		f.ctx.Entries["role_worker_kind"] = core.ConfigEntry{Value: "pi", Source: "user"}
		if got := ResolvedRoleKind("worker", f.ctx, f.env, f.cwd); got != "pi" {
			t.Fatalf("configured kind=%q", got)
		}
	})

	t.Run("resolveSpawnEffort: the chain and the kind-layer rule", func(t *testing.T) {
		// JS: "resolveSpawnEffort: the chain and the kind-layer rule"
		f := newSpawnFixture(t, nil)
		if err := os.WriteFile(filepath.Join(f.root, "roles", "worker.md"), []byte("---\neffort: high\n---\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := ResolveSpawnEffort("worker", "", "grok", 0, f.ctx, f.env, f.cwd, ""); got != "high" {
			t.Fatalf("frontmatter effort=%q", got)
		}
		f.ctx.Entries["effort_grok"] = core.ConfigEntry{Value: "low", Source: "project"}
		if got := ResolveSpawnEffort("worker", "", "grok", 0, f.ctx, f.env, f.cwd, ""); got != "low" {
			t.Fatalf("configured effort=%q", got)
		}
	})

	t.Run("resolveRoleSettings: matches what a flagless spawn records in the roster", func(t *testing.T) {
		// JS: "resolveRoleSettings: matches what a flagless spawn records in the roster"
		f := newSpawnFixture(t, nil)
		if err := os.WriteFile(filepath.Join(f.root, "roles", "worker.md"), []byte("---\nkind: grok\neffort: high\n---\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		got := core.ResolveRoleSettings("worker", f.ctx, f.env, f.cwd, core.RoleFlags{})
		if got.Kind != "grok" || got.Effort != "high" || got.Approvals != "auto" {
			t.Fatalf("settings=%#v", got)
		}
	})

	t.Run("resolveRoleSettings: flags, config layers and lane layers decide, with the source", func(t *testing.T) {
		// JS: "resolveRoleSettings: flags, config layers and lane layers decide, with the source"
		f := newSpawnFixture(t, nil)
		flags := core.ResolveRoleSettings("worker", f.ctx, f.env, f.cwd, core.RoleFlags{Kind: "pi", Model: "m1", Effort: "low", Approvals: "full"})
		if flags.Kind != "pi" || flags.KindFrom != "flag" || flags.ModelSpec != "m1" || flags.Effort != "low" || flags.Approvals != "full" {
			t.Fatalf("flag settings=%#v", flags)
		}
	})

	t.Run("resolveRoleSettings: a role model from a layer below the effective kind is dropped", func(t *testing.T) {
		// JS: "resolveRoleSettings: a role model from a layer below the effective kind is dropped"
		f := newSpawnFixture(t, nil)
		f.ctx.Entries["role_worker_kind"] = core.ConfigEntry{Value: "pi", Source: "project"}
		f.ctx.Entries["role_worker_model"] = core.ConfigEntry{Value: "stale-model", Source: "user"}
		got := core.ResolveRoleSettings("worker", f.ctx, f.env, f.cwd, core.RoleFlags{})
		if got.Kind != "pi" || got.ModelSpec == "stale-model" {
			t.Fatalf("settings=%#v", got)
		}
	})

	t.Run("resolveRoleSettings: the frontmatter model follows the kind layer rule", func(t *testing.T) {
		// JS: "resolveRoleSettings: the frontmatter model follows the kind layer rule"
		f := newSpawnFixture(t, nil)
		if err := os.WriteFile(filepath.Join(f.root, "roles", "worker.md"), []byte("---\nkind: grok\nmodel: worker-model\n---\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		base := core.ResolveRoleSettings("worker", f.ctx, f.env, f.cwd, core.RoleFlags{})
		if base.ModelSpec != "worker-model" {
			t.Fatalf("frontmatter settings=%#v", base)
		}
		f.ctx.Entries["role_worker_kind"] = core.ConfigEntry{Value: "pi", Source: "project"}
		changed := core.ResolveRoleSettings("worker", f.ctx, f.env, f.cwd, core.RoleFlags{})
		if changed.Kind != "pi" || changed.ModelSpec == "worker-model" {
			t.Fatalf("higher kind settings=%#v", changed)
		}
	})

	t.Run("resolveSpawnEffort: codex takes the ceiling of the session model", func(t *testing.T) {
		// JS: "resolveSpawnEffort: codex takes the ceiling of the session model"
		f := newSpawnFixture(t, nil)
		if err := os.MkdirAll(filepath.Join(f.root, ".codex"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.root, ".codex", "models_cache.json"), []byte(`{"models":[{"slug":"large","supported_reasoning_levels":[{"effort":"low"},{"effort":"max"}]},{"slug":"small","supported_reasoning_levels":[{"effort":"low"},{"effort":"xhigh"}]}]}`), 0o600); err != nil {
			t.Fatal(err)
		}
		f.ctx.Entries["effort_codex"] = core.ConfigEntry{Value: "max", Source: "project"}
		if got := ResolveSpawnEffort("worker", "", "codex", 0, f.ctx, f.env, f.cwd, "large"); got != "max" {
			t.Fatalf("large effort=%q", got)
		}
		if got := ResolveSpawnEffort("worker", "", "codex", 0, f.ctx, f.env, f.cwd, "small"); got != "xhigh" {
			t.Fatalf("small effort=%q", got)
		}
	})

	t.Run("spawn: the agent args order — skill args, args.<kind>, role args, then --", func(t *testing.T) {
		// JS: "spawn: the agent args order — skill args, args.<kind>, role args, then --"
		f := newSpawnFixture(t, nil)
		if err := os.WriteFile(filepath.Join(f.root, "roles", "worker.md"), []byte("---\nkind: grok\n---\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		f.ctx.Entries["args_grok"] = core.ConfigEntry{Value: "--kind-extra", Source: "project"}
		f.ctx.Entries["role_worker_args"] = core.ConfigEntry{Value: "--role-extra", Source: "project"}
		args := buildArgs("grok", "ask", "model", "high", f.ctx, f.env)
		args = append(args, strings.Fields(ConfigNativeArgs("grok", "", "worker", f.ctx, f.env, f.cwd, nil))...)
		args = append(args, "--tail", "two words")
		want := []string{"--model", "model", "--reasoning-effort", "high", "--kind-extra", "--role-extra", "--tail", "two words"}
		if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
			t.Fatalf("args=%q want=%q", args, want)
		}
	})

	t.Run("emitReuse: retargets column 4, grows the history, keeps model/approvals", func(t *testing.T) {
		// JS: "emitReuse: retargets column 4, grows the history, keeps model/approvals"
		f := newSpawnFixture(t, nil)
		f.roster(t, spawnRosterRow("worker", "scouter", "grok-4.7", "full", ""))
		oldOut := platform.Stdout
		var output strings.Builder
		platform.Stdout = &output
		t.Cleanup(func() { platform.Stdout = oldOut })
		if !EmitReuse("worker", "implementer", "grok", f.ctx, f.env, f.cwd) {
			t.Fatal("EmitReuse returned false")
		}
		data, err := os.ReadFile(filepath.Join(f.state, "ws", "agents.tsv"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "worker\tp-worker\tgrok\timplementer") || !strings.Contains(string(data), "scouter,implementer") || !strings.Contains(string(data), "grok-4.7\tfull") {
			t.Fatalf("roster=%q", data)
		}
		if !strings.Contains(output.String(), `"previous_role": "scouter"`) || !strings.Contains(output.String(), `"reused": true`) {
			t.Fatalf("JSON=%q", output.String())
		}
	})

	t.Run("emitReuse: same-role reuse leaves an 8-column line untouched", func(t *testing.T) {
		// JS: "emitReuse: same-role reuse leaves an 8-column line untouched"
		f := newSpawnFixture(t, nil)
		row := "worker\tp-worker\tgrok\timplementer\txai\t1\t/tmp/work\tnow"
		f.roster(t, row)
		oldOut := platform.Stdout
		platform.Stdout = &strings.Builder{}
		t.Cleanup(func() { platform.Stdout = oldOut })
		if !EmitReuse("worker", "implementer", "grok", f.ctx, f.env, f.cwd) {
			t.Fatal("EmitReuse returned false")
		}
		data, err := os.ReadFile(filepath.Join(f.state, "ws", "agents.tsv"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), row+"\n") {
			t.Fatalf("8-column row changed: %q", data)
		}
	})

	t.Run("emitReuse: a name without a roster row returns no JSON (rc stays 0, no output)", func(t *testing.T) {
		// JS: "emitReuse: a name without a roster row returns no JSON (rc stays 0, no output)"
		f := newSpawnFixture(t, nil)
		f.roster(t)
		oldOut := platform.Stdout
		var output strings.Builder
		platform.Stdout = &output
		t.Cleanup(func() { platform.Stdout = oldOut })
		if EmitReuse("missing", "implementer", "grok", f.ctx, f.env, f.cwd) {
			t.Fatal("missing row was emitted")
		}
		if output.Len() != 0 {
			t.Fatalf("unexpected JSON: %q", output.String())
		}
	})

	t.Run("the native args the current lane mode ignores (lanes on: role args; lanes off: lane args)", func(t *testing.T) {
		// JS: "the native args the current lane mode ignores (lanes on: role args; lanes off: lane args)"
		f := newSpawnFixture(t, nil)
		if err := os.WriteFile(filepath.Join(f.root, "roles", "worker.md"), []byte("---\nkind: grok\n---\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		f.ctx.Entries["args_grok"] = core.ConfigEntry{Value: "--kind", Source: "project"}
		f.ctx.Entries["role_worker_args"] = core.ConfigEntry{Value: "--role", Source: "project"}
		if got := ConfigNativeArgs("grok", "", "worker", f.ctx, f.env, f.cwd, nil); got != "--kind --role" {
			t.Fatalf("args=%q", got)
		}
		f.ctx.Entries["lane_build_args"] = core.ConfigEntry{Value: "--lane", Source: "project"}
		if got := ConfigNativeArgs("grok", "build", "worker", f.ctx, f.env, f.cwd, nil); got != "--kind --lane" {
			t.Fatalf("lane args=%q", got)
		}
	})
}

func TestCmdSpawnJavaScriptCases(t *testing.T) {
	t.Run("spawn: planner is 12, unknown role 3, sub-orchestrator not in a lane 3, usage 2", func(t *testing.T) {
		// JS: "spawn: planner is 12, unknown role 3, sub-orchestrator not in a lane 3, usage 2"
		f := newSpawnFixture(t, freshSpawnRules())
		configureSpawnFixture(t, &f)
		code, _, _, _ := runCmdSpawn(t, f, []string{"planner"})
		if code != 12 {
			t.Fatalf("planner exit=%d", code)
		}
		code, _, _, _ = runCmdSpawn(t, f, []string{"missing-role"})
		if code != 3 {
			t.Fatalf("unknown role exit=%d", code)
		}
		f.env["SOHO_LANES"] = "on"
		code, _, _, _ = runCmdSpawn(t, f, []string{"sub-orchestrator"})
		if code != 3 {
			t.Fatalf("lane-less role exit=%d", code)
		}
		code, _, _, _ = runCmdSpawn(t, f, nil)
		if code != 2 {
			t.Fatalf("missing role usage exit=%d", code)
		}
	})

	t.Run("spawn: --pane places the worker in a given pane (placement given)", func(t *testing.T) {
		// JS: "spawn: --pane places the worker in a given pane (placement given)"
		f := newSpawnFixture(t, freshSpawnRules())
		configureSpawnFixture(t, &f)
		code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"worker", "--pane", "w0test:p0a", "--name", "worker"})
		if code != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		var start []string
		for _, call := range calls {
			if len(call.Argv) >= 2 && call.Argv[0] == "agent" && call.Argv[1] == "start" {
				start = call.Argv
				break
			}
		}
		if !containsArgPair(start, "--pane", "w0test:p0a") {
			t.Fatalf("start argv=%q", start)
		}
	})

	t.Run("spawn: an empty --kind is a usage error", func(t *testing.T) {
		// JS: "spawn: an empty --kind is a usage error"
		f := newSpawnFixture(t, freshSpawnRules())
		configureSpawnFixture(t, &f)
		code, _, stderr, calls := runCmdSpawn(t, f, []string{"worker", "--kind", ""})
		if code != 2 {
			t.Fatalf("exit=%d stderr=%q", code, stderr)
		}
		if len(calls) != 0 {
			t.Fatalf("invalid kind touched Herdr: %#v", calls)
		}
	})

	t.Run("spawn: --tab-label forces the herd tab and pins the label (test-tab-labels.sh)", func(t *testing.T) {
		// JS: "spawn: --tab-label forces the herd tab and pins the label (test-tab-labels.sh)"
		f := newSpawnFixture(t, freshSpawnRules())
		configureSpawnFixture(t, &f)
		f.env["SOHO_LAYOUT"] = "split"
		code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"worker", "--tab-label", "task tab"})
		if code != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		found := false
		for _, call := range calls {
			if len(call.Argv) >= 2 && call.Argv[0] == "tab" && call.Argv[1] == "create" && containsArgPair(call.Argv, "--label", "task tab") {
				found = true
			}
		}
		if !found {
			t.Fatalf("tab rename missing from calls: %#v", calls)
		}
	})

	t.Run("spawn: relative --cwd records the absolute roster path and missing directory exits before pane open", func(t *testing.T) {
		// JS: "spawn: a relative --cwd is resolved to an absolute directory"
		target := filepath.Join("wt", "ai")
		f := newSpawnFixture(t, freshSpawnRules())
		configureSpawnFixture(t, &f)
		absolute := filepath.Join(f.cwd, target)
		if err := os.MkdirAll(absolute, 0o700); err != nil {
			t.Fatal(err)
		}
		code, _, stderr, calls := runCmdSpawn(t, f, []string{"worker", "--cwd", target})
		if code != 0 {
			t.Fatalf("valid --cwd exit=%d stderr=%q", code, stderr)
		}
		roster, err := os.ReadFile(filepath.Join(f.state, "ws", "agents.tsv"))
		if err != nil {
			t.Fatal(err)
		}
		rows := strings.Split(strings.TrimSpace(string(roster)), "\n")
		cols := strings.Split(rows[len(rows)-1], "\t")
		got := ""
		if len(cols) > 6 {
			got = cols[6]
		}
		if got != absolute {
			t.Fatalf("roster column 7=%q, want absolute cwd %q; roster=%q", got, absolute, roster)
		}
		before := len(calls)
		missing := filepath.Join(f.cwd, "no", "such", "dir")
		code, message, calls := runCmdSpawnExpectError(t, f, []string{"worker", "--cwd", filepath.Join("no", "such", "dir")})
		if code != 2 || message != "spawn: --cwd "+missing+" is not a directory" {
			t.Fatalf("missing --cwd exit=%d error=%q", code, message)
		}
		for _, call := range calls[before:] {
			if len(call.Argv) >= 2 && call.Argv[0] == "pane" && call.Argv[1] == "split" {
				t.Fatalf("opened pane for invalid --cwd: %#v", calls[before:])
			}
		}
	})

	t.Run("spawn: scoped args stay with their kind; resume flags are refused; a failed start closes its pane", func(t *testing.T) {
		// JS: "spawn: scoped args stay with their kind; resume flags are refused; a failed start closes its pane"
		if got := ResumeArg("claude", []string{"--continue"}); got != "--continue" {
			t.Fatalf("resume arg=%q", got)
		}
		if got := ResumeArg("codex", []string{"--continue"}); got != "" {
			t.Fatalf("codex resume arg=%q", got)
		}
		f2 := newSpawnFixture(t, []fakecli.Rule{
			{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`},
			{Argv: []string{"agent", "start"}, ArgvPrefix: true, Stderr: `{"error":{"code":"timeout","message":"timed out"}}`, Code: 1},
			{Argv: []string{"agent", "read"}, ArgvPrefix: true, Stdout: "blocked screen\n"},
			{Argv: []string{"pane", "close"}, ArgvPrefix: true},
		})
		configureSpawnFixture(t, &f2)
		code, _, stderr, calls := runCmdSpawn(t, f2, []string{"worker", "--pane", "w0test:p0a", "--fresh"})
		if code != 4 || !strings.Contains(stderr, "timed out") {
			t.Fatalf("exit=%d stderr=%q", code, stderr)
		}
		closed := false
		for _, call := range calls {
			if len(call.Argv) >= 2 && call.Argv[0] == "pane" && call.Argv[1] == "close" {
				closed = true
			}
		}
		if closed {
			t.Fatal("given pane was closed after a failed start")
		}
	})

	t.Run("spawn: the roster records the native args (column 14) the spawn used", func(t *testing.T) {
		// JS: "spawn: the roster records the native args (column 14) the spawn used"
		f := newSpawnFixture(t, freshSpawnRules())
		configureSpawnFixture(t, &f)
		f.ctx.Entries["role_worker_args"] = core.ConfigEntry{Value: "--native", Source: "project"}
		code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"worker"})
		if code != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		roster, err := os.ReadFile(filepath.Join(f.state, "ws", "agents.tsv"))
		if err != nil {
			t.Fatal(err)
		}
		fields := strings.Split(strings.TrimSpace(strings.Split(string(roster), "\n")[1]), "\t")
		if len(fields) < 15 || fields[13] != "--native" {
			t.Fatalf("roster columns=%q", fields)
		}
	})

	t.Run("spawn: the roster records the effective effort (column 15) the session opened with", func(t *testing.T) {
		// JS: "spawn: the roster records the effective effort (column 15) the session opened with"
		f := newSpawnFixture(t, freshSpawnRules())
		configureSpawnFixture(t, &f)
		code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"worker", "--effort", "high"})
		if code != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		roster, err := os.ReadFile(filepath.Join(f.state, "ws", "agents.tsv"))
		if err != nil {
			t.Fatal(err)
		}
		fields := strings.Split(strings.TrimSpace(strings.Split(string(roster), "\n")[1]), "\t")
		if len(fields) < 15 || fields[14] != "high" {
			t.Fatalf("roster columns=%q", fields)
		}
	})

	t.Run("spawn: a fresh worker (layout=tab → herd tab), roster row and start args", func(t *testing.T) {
		// JS: "spawn: a fresh worker (layout=tab → herd tab), roster row and start args"
		f := newSpawnFixture(t, freshSpawnRules())
		configureSpawnFixture(t, &f)
		f.env["SOHO_LAYOUT"] = "tab"
		code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"worker"})
		if code != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		if !strings.Contains(stdout, `"status": "ready"`) {
			t.Fatalf("stdout=%q", stdout)
		}
		var started bool
		for _, call := range calls {
			if len(call.Argv) >= 2 && call.Argv[0] == "agent" && call.Argv[1] == "start" {
				started = true
			}
		}
		if !started {
			t.Fatalf("no start call: %#v", calls)
		}
		roster, err := os.ReadFile(filepath.Join(f.state, "ws", "agents.tsv"))
		if err != nil || !strings.Contains(string(roster), "\tworker\t") {
			t.Fatalf("roster=%q err=%v", roster, err)
		}
	})
}

func runCmdSpawnExpectError(t *testing.T, f spawnFixture, args []string) (code int, message string, calls []fakecli.Call) {
	t.Helper()
	func() {
		defer func() {
			if value := recover(); value != nil {
				if e, ok := value.(*platform.ExitError); ok {
					code, message = e.Code, e.Msg
					return
				}
				panic(value)
			}
		}()
		CmdSpawn(args, f.ctx, f.env, f.cwd)
	}()
	calls, _ = fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
	return code, message, calls
}

func TestSpawnLaneJavaScriptCases(t *testing.T) {
	t.Run("spawn: the build lane — capacity 2: open build-2, busy 10 when full, --fresh, reuse", func(t *testing.T) {
		// JS: "spawn: the build lane — capacity 2: open build-2, busy 10 when full, --fresh, reuse"
		f := newSpawnFixture(t, []fakecli.Rule{
			{Argv: []string{"agent", "get", "build"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
			{Argv: []string{"agent", "get", "build-2"}, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
		})
		f.ctx.Entries["lane_build_roles"] = core.ConfigEntry{Value: "worker", Source: "project"}
		f.ctx.Entries["lane_build_kind"] = core.ConfigEntry{Value: "grok", Source: "project"}
		f.ctx.Entries["lane_build_panes"] = core.ConfigEntry{Value: "2", Source: "project"}
		f.ctx.Order = []string{"lane_build_roles", "lane_build_kind", "lane_build_panes"}
		row := func(name string) string {
			return strings.Join([]string{name, "p-" + name, "grok", "worker", "xai", "1", "/tmp/work", "now", "grok-4.7", "ask", "worker", "build"}, "\t")
		}
		f.roster(t, row("build"))
		open := core.LaneDecide(f.ctx, "build", "worker", f.env, f.cwd, true)
		if open.Decision != "open" || open.Capacity != 2 || open.N != 1 {
			t.Fatalf("one worker decision=%#v", open)
		}
		if decision := core.LaneDecide(f.ctx, "build", "worker", f.env, f.cwd, false); decision.Decision != "open" {
			t.Fatalf("--fresh lane decision=%#v", decision)
		}
		f.roster(t, row("build"), row("build-2"))
		busy := core.LaneDecide(f.ctx, "build", "worker", f.env, f.cwd, true)
		if busy.Decision != "busy" || busy.N != 2 {
			t.Fatalf("full lane decision=%#v", busy)
		}
	})

	t.Run("spawn: a gone lane worker is removed and the lane opens a new pane", func(t *testing.T) {
		// JS: "spawn: a gone lane worker is removed and the lane opens a new pane"
		f := newSpawnFixture(t, []fakecli.Rule{{Argv: []string{"agent", "get", "build"}, Stderr: `{"error":{"code":"agent_not_found","message":"gone"}}`, Code: 1}})
		f.ctx.Entries["lane_build_roles"] = core.ConfigEntry{Value: "worker", Source: "project"}
		f.ctx.Entries["lane_build_kind"] = core.ConfigEntry{Value: "grok", Source: "project"}
		f.ctx.Entries["lane_build_panes"] = core.ConfigEntry{Value: "1", Source: "project"}
		f.ctx.Order = []string{"lane_build_roles", "lane_build_kind", "lane_build_panes"}
		f.roster(t, strings.Join([]string{"build", "p-build", "grok", "worker", "xai", "1", "/tmp/work", "now", "grok-4.7", "ask", "worker", "build"}, "\t"))
		decision := core.LaneDecide(f.ctx, "build", "worker", f.env, f.cwd, true)
		if decision.Decision != "open" || len(decision.Gone) != 1 || decision.Gone[0] != "build" {
			t.Fatalf("lane decision=%#v", decision)
		}
	})

	t.Run("spawn: the temporary (burst) worker — flex only, flex_roles, capped by flex_extra", func(t *testing.T) {
		// JS: "spawn: the temporary (burst) worker — flex only, flex_roles, capped by flex_extra"
		f := newSpawnFixture(t, []fakecli.Rule{{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[{"name":"extra","pane_id":"p-extra"}]}}`}})
		f.ctx.Entries["pane_mode"] = core.ConfigEntry{Value: "flex", Source: "project"}
		f.ctx.Entries["flex_extra"] = core.ConfigEntry{Value: "1", Source: "project"}
		f.ctx.Order = []string{"pane_mode", "flex_extra"}
		f.roster(t, strings.Join([]string{"extra", "p-extra", "grok", "reviewer", "xai", "1", "/tmp/work", "now", "grok-4.7", "ask", "reviewer", "", "burst"}, "\t"))
		got := core.LiveBurstWorkers(filepath.Join(f.state, "ws"), f.env)
		if len(got) != 1 || got[0] != "extra" || core.FlexExtra(f.ctx, f.env) != 1 {
			t.Fatalf("burst=%v extra=%d", got, core.FlexExtra(f.ctx, f.env))
		}
	})

	t.Run("spawn: a model-mismatched idle on a full lane is 13 with the no-slot message", func(t *testing.T) {
		// A12: the idle is not a reuse candidate (resolved model differs), the
		// lane has no slot, so spawn exits 13 naming the idle and what it runs.
		f := newSpawnFixture(t, []fakecli.Rule{{Argv: []string{"agent", "get", "build"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`}})
		configureSpawnFixture(t, &f)
		f.env["SOHO_LANES"] = "on"
		f.ctx.Entries["lane_build_roles"] = core.ConfigEntry{Value: "worker", Source: "project"}
		f.ctx.Order = []string{"lane_build_roles"}
		// The recorded session model differs from the requested default model.
		f.roster(t, strings.Join([]string{"build", "p-build", "grok", "worker", "xai", "1", f.cwd, "now", "old-model", "ask", "worker", "build"}, "\t"))
		code, message, calls := runCmdSpawnExpectError(t, f, []string{"worker"})
		want := fmt.Sprintf("spawn: lane 'build' has no idle worker matching kind 'grok', model 'grok-4.7' and cwd '%s' (idle: 'build' runs grok old-model in %s); release it or raise the lane's panes", f.cwd, f.cwd)
		if code != 13 || message != want {
			t.Fatalf("exit=%d message=%q want %q calls=%#v", code, message, want, calls)
		}
	})

	t.Run("spawn: an explicit lane kind that matches the session is reused; a different CLI is 13", func(t *testing.T) {
		// JS: "spawn: an explicit lane kind that matches the session is reused; a different CLI is 13"
		f := newSpawnFixture(t, []fakecli.Rule{{Argv: []string{"agent", "get", "build"}, Stdout: `{"result":{"agent":{"agent_status":"idle"}}}`}})
		configureSpawnFixture(t, &f)
		f.env["SOHO_LANES"] = "on"
		f.ctx.Entries["lane_build_roles"] = core.ConfigEntry{Value: "worker", Source: "project"}
		f.ctx.Entries["lane_build_kind"] = core.ConfigEntry{Value: "grok", Source: "project"}
		f.ctx.Order = []string{"lane_build_roles", "lane_build_kind"}
		f.roster(t, strings.Join([]string{"build", "p-build", "claude", "worker", "anthropic", "1", "/tmp/work", "now", "grok-4.7", "ask", "worker", "build"}, "\t"))
		code, stdout, _, _ := runCmdSpawn(t, f, []string{"worker"})
		if code != 13 || !strings.Contains(stdout, `"status":"kind-mismatch"`) {
			calls, _ := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
			t.Fatalf("exit=%d stdout=%q calls=%#v", code, stdout, calls)
		}
	})
}

func containsArgPair(args []string, key, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key && args[i+1] == value {
			return true
		}
	}
	return false
}

func TestSpawnNameDiffersFromBase(t *testing.T) {
	const line = "soho: spawn: the new worker is 'build-2' ('build' is already live); dispatch to 'build-2'"

	newBuildFixture := func(t *testing.T, liveBuild bool) spawnFixture {
		t.Helper()
		rules := freshSpawnRules()
		if liveBuild {
			rules[0].Stdout = `{"result":{"agents":[{"name":"build"}]}}`
		}
		f := newSpawnFixture(t, rules)
		configureSpawnFixture(t, &f)
		if err := os.WriteFile(filepath.Join(f.root, "roles", "build.md"), []byte("---\nkind: grok\n---\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return f
	}

	t.Run("spawn: no --name with the base live announces the final name on stderr", func(t *testing.T) {
		// Mutation captured: dropping the stderr line leaves the orchestrator dispatching to the live base name.
		f := newBuildFixture(t, true)
		code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"build"})
		if code != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		if !strings.Contains(stderr, line) {
			t.Fatalf("stderr=%q", stderr)
		}
		if strings.Contains(stdout, line) {
			t.Fatalf("the line leaked to stdout: %q", stdout)
		}
		if !strings.Contains(stdout, `"name": "build-2"`) {
			t.Fatalf("spawn JSON=%q", stdout)
		}
	})

	t.Run("spawn: no --name with the base free stays silent", func(t *testing.T) {
		f := newBuildFixture(t, false)
		code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"build"})
		if code != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		if strings.Contains(stderr, "the new worker is") {
			t.Fatalf("stderr=%q", stderr)
		}
		if !strings.Contains(stdout, `"name": "build"`) {
			t.Fatalf("spawn JSON=%q", stdout)
		}
	})

	t.Run("spawn: a taken --name keeps only the existing warning", func(t *testing.T) {
		f := newBuildFixture(t, true)
		code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"build", "--name", "build"})
		if code != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		if !strings.Contains(stderr, "agent name 'build' is taken by another pane or workspace; using 'build-2'") {
			t.Fatalf("stderr=%q", stderr)
		}
		if strings.Contains(stderr, "the new worker is") {
			t.Fatalf("the base warning leaked on the --name path: stderr=%q", stderr)
		}
		if !strings.Contains(stdout, `"name": "build-2"`) {
			t.Fatalf("spawn JSON=%q", stdout)
		}
	})
}

func Test_init_sub_orchestrator_spawn_lazy_rename_keeps_name(t *testing.T) {
	// D12: the spawn's lazy rename of the caller must not rename a caller
	// pane that is a roster row of this project (a sub-orchestrator the
	// upper orchestrator opened): no agent rename of the caller, one stderr
	// line naming it, and the spawn still opens the worker.
	rules := []fakecli.Rule{{Argv: []string{"agent", "get", "p-sub"}, Stdout: `{"result":{"agent":{"name":"sub-orch","agent_status":"working"}}}`}}
	rules = append(rules, freshSpawnRules()...)
	f := newSpawnFixture(t, rules)
	configureSpawnFixture(t, &f)
	f.env["HERDR_PANE_ID"] = "p-sub"
	f.roster(t, "sub-orch\tp-sub\tclaude\tsub-orchestrator\tanthropic\t1\t/tmp/work\tnow\t\t\t\t")
	code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"worker"})
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, `"created_pane": true`) {
		t.Fatalf("the spawn did not open the worker:\n%s", stdout)
	}
	want := "soho: spawn: this pane is 'sub-orch' in the roster (a sub-orchestrator opened by another orchestrator); keeping its name\n"
	if !strings.Contains(stderr, want) {
		t.Fatalf("missing the roster line:\n%s", stderr)
	}
	for _, c := range calls {
		if len(c.Argv) >= 2 && c.Argv[0] == "agent" && c.Argv[1] == "rename" {
			t.Fatalf("the caller's agent was renamed: %#v", calls)
		}
	}
}
