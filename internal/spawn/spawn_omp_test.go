package spawn

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

const ompFixtureModel = "openrouter/anthropic/claude-test"
const ompFixturePolicy = "retry:\n  modelFallback: false\ntask:\n  maxRecursionDepth: 0\n"
const ompFixtureOldPolicy = "retry:\n  modelFallback: false\ntools:\n  approval:\n    task: deny\n"

func ompFixturePolicyPath(skill string) string {
	return filepath.Join(skill, "templates", "omp-managed-v2.yml")
}

func newOMPSpawnFixture(t *testing.T, overrides ...fakecli.Rule) spawnFixture {
	t.Helper()
	rules := append(overrides, ompStartupRules("p2", ompReadyScreen)...)
	rules = append(rules,
		idleHerdrRule("worker"),
		fakecli.Rule{Argv: []string{"tab", "create"}, ArgvPrefix: true, Stdout: `{"result":{"tab":{"tab_id":"t-herd","label":"herd"},"root_pane":{"pane_id":"p2"}}}`},
	)
	f := newSpawnFixture(t, freshSpawnRules(rules...))
	configureSpawnFixture(t, &f)
	f.env["PATH"] = f.bin
	f.env["TMPDIR"] = f.root
	f.env["SOHO_CALLER_CHECK"] = "off"
	policy := ompFixturePolicyPath(f.env["SOHO_SKILL_DIR"])
	packaged, err := os.ReadFile(ompFixturePolicyPath(filepath.Join("..", "..", "skills", "soho")))
	if err != nil || string(packaged) != ompFixturePolicy {
		t.Fatalf("packaged OMP policy = %q, err=%v, want %q", packaged, err, ompFixturePolicy)
	}
	if err := os.MkdirAll(filepath.Dir(policy), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(policy, packaged, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fakecli.Install(t, f.bin, "omp", nil); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestSpawnOMPVerifiedLaunchAndReuse(t *testing.T) {
	for _, tc := range []struct{ approvals, mode string }{
		{"ask", "always-ask"}, {"edits", "write"}, {"full", "yolo"},
	} {
		t.Run(tc.approvals, func(t *testing.T) {
			f := newOMPSpawnFixture(t)
			old := filepath.Join(f.env["SOHO_SKILL_DIR"], "templates", "omp-managed-v1.yml")
			if err := os.WriteFile(old, []byte(ompFixtureOldPolicy), 0o600); err != nil {
				t.Fatal(err)
			}
			args := []string{"worker", "--kind", "omp", "--mcp", "inherit", "--approvals", tc.approvals, "--model", ompFixtureModel, "--effort", "xhigh"}
			code, out, stderr, calls := runCmdSpawn(t, f, args)
			if code != 0 || !spawnJSONHasField(t, out, `"status": "ready"`) {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
			}
			policy, err := filepath.EvalSymlinks(ompFixturePolicyPath(f.env["SOHO_SKILL_DIR"]))
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"--approval-mode", tc.mode, "--model", ompFixtureModel, "--thinking", "xhigh", "--config", policy}
			starts := 0
			for _, call := range calls {
				if len(call.Argv) < 2 || !slices.Equal(call.Argv[:2], []string{"agent", "start"}) {
					continue
				}
				starts++
				i := slices.Index(call.Argv, "--")
				if i < 0 || !slices.Equal(call.Argv[i+1:], want) {
					t.Errorf("OMP launch = %q, want native argv %q", call.Argv, want)
				}
			}
			if starts != 1 {
				t.Fatalf("OMP launch count = %d, want 1", starts)
			}
			sd := filepath.Join(f.state, "ws")
			before := core.RosterLine(sd, "worker")
			row := strings.Split(before, "\t")
			receipt := fieldAt(row, 19)
			if fieldAt(row, 9) != tc.approvals || fieldAt(row, 13) != "" || fieldAt(row, 15) != "inherit" ||
				fieldAt(row, 17) != "--approval-mode "+tc.mode || !strings.HasPrefix(receipt, "omp-policy:v2:") ||
				len(receipt) != len("omp-policy:v2:")+64 {
				t.Fatalf("OMP opening settings missing from row: %q", before)
			}
			code, out, stderr, calls = runCmdSpawn(t, f, args)
			if code != 0 || !spawnJSONHasField(t, out, `"reused": true`) || !spawnJSONHasField(t, out, `"status": "ready"`) {
				t.Fatalf("reuse exit=%d stdout=%q stderr=%q", code, out, stderr)
			}
			count := 0
			for _, call := range calls {
				if len(call.Argv) >= 2 && slices.Equal(call.Argv[:2], []string{"agent", "start"}) {
					count++
				}
			}
			if count != starts || core.RosterLine(sd, "worker") != before {
				t.Fatalf("reuse changed the launch count or roster row, launches=%d", count)
			}
		})
	}
}

func TestSpawnOMPRefusalsBeforeEffects(t *testing.T) {
	for _, tc := range []struct {
		name, mcp, approvals, model, configured, want string
		native                                        []string
	}{
		{name: "none", mcp: "none", want: "explicit MCP inherit"},
		{name: "server list", mcp: "one,two", want: "explicit MCP inherit"},
		{name: "implicit inherit", want: "explicit MCP inherit"},
		{name: "auto approval", mcp: "inherit", approvals: "auto", want: "omp requires approvals"},
		{name: "unsandboxed", mcp: "inherit", approvals: "unsandboxed", want: "omp requires approvals"},
		{name: "approval setting", mcp: "inherit", configured: "--approval-mode=yolo", want: "does not accept extra native args"},
		{name: "native alias", mcp: "inherit", native: []string{"--yolo"}, want: "does not accept extra native args"},
		{name: "native overlay", mcp: "inherit", native: []string{"--config", "overlay.yml"}, want: "does not accept extra native args"},
		{name: "fuzzy model", mcp: "inherit", model: "opus", want: "exact provider/model ID"},
		{name: "flag as model", mcp: "inherit", model: "-hidden/model", want: "exact provider/model ID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newOMPSpawnFixture(t)
			model, approvals := tc.model, tc.approvals
			if model == "" {
				model = ompFixtureModel
			}
			if approvals == "" {
				approvals = "ask"
			}
			args := []string{"worker", "--kind", "omp", "--model", model, "--effort", "high", "--approvals", approvals}
			if tc.mcp != "" {
				args = append(args, "--mcp", tc.mcp)
			}
			if tc.configured != "" {
				f.env["SOHO_ARGS_OMP"] = tc.configured
			}
			if len(tc.native) > 0 {
				args = append(append(args, "--"), tc.native...)
			}
			assertOMPSpawnRefusal(t, f, args, tc.want)
		})
	}
}

func TestSpawnOMPRefusesUnverifiedReuse(t *testing.T) {
	for _, tc := range []struct {
		name, approvals, receipt string
		pathOnly                 bool
	}{
		{name: "old row", approvals: "ask"},
		{name: "mismatched receipt", approvals: "ask", receipt: "--approval-mode yolo"},
		{name: "broader worker", approvals: "full", receipt: "--approval-mode yolo"},
		{name: "historical approval but no policy", approvals: "ask", receipt: "--approval-mode always-ask"},
		{name: "historical path-only policy", approvals: "ask", receipt: "--approval-mode always-ask", pathOnly: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newOMPSpawnFixture(t)
			row := []string{"worker", "p2", "omp", "worker", "anthropic", "1", f.cwd, "now", ompFixtureModel, tc.approvals, "worker", "", "false", "", "high", "inherit"}
			if tc.receipt != "" {
				row = append(row, "", tc.receipt)
			}
			if tc.pathOnly {
				policy, err := filepath.EvalSymlinks(ompFixturePolicyPath(f.env["SOHO_SKILL_DIR"]))
				if err != nil {
					t.Fatal(err)
				}
				row = append(row, "", "omp-policy:"+filepath.Join(filepath.Dir(policy), "omp-managed-v1.yml"))
			}
			f.roster(t, strings.Join(row, "\t"))
			args := []string{"worker", "--kind", "omp", "--name", "worker", "--mcp", "inherit", "--approvals", "ask", "--model", ompFixtureModel, "--effort", "high"}
			assertOMPSpawnRefusal(t, f, args, "no matching verified managed OMP launch")
		})
	}
}

func TestSpawnOMPManagedPolicyRefusesBeforePaneEffects(t *testing.T) {
	for _, tc := range []struct{ name, body, diagnostic string }{
		{"missing", "", "is unavailable"},
		{"invalid", strings.Replace(ompFixturePolicy, "maxRecursionDepth: 0", "maxRecursionDepth: 1", 1), "is invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newOMPSpawnFixture(t)
			policy := ompFixturePolicyPath(f.env["SOHO_SKILL_DIR"])
			old := filepath.Join(filepath.Dir(policy), "omp-managed-v1.yml")
			if err := os.WriteFile(old, []byte(ompFixtureOldPolicy), 0o600); err != nil {
				t.Fatal(err)
			}
			if tc.body == "" {
				if err := os.Remove(policy); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(policy, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			args := []string{"worker", "--fresh", "--kind", "omp", "--mcp", "inherit", "--approvals", "full", "--model", ompFixtureModel, "--effort", "high"}
			assertOMPSpawnRefusal(t, f, args, "managed OMP policy omp-managed-v2.yml "+tc.diagnostic)
		})
	}
}

func TestSpawnOMPManagedReuseIdentity(t *testing.T) {
	const pane, terminal, session = "p2", "term_omp_start", "omp-session-start"
	for _, tc := range []struct {
		name, approval, changedReply string
		call, code, delay            int
		sessionReportedAfterLaunch   bool
	}{
		{name: "true match"},
		{name: "session first reported during startup observation", sessionReportedAfterLaunch: true},
		{name: "same name moved to another pane", call: 3, changedReply: ompStartupRecord("p3", terminal, session), code: 10},
		{name: "replacement in the same pane", call: 3, changedReply: ompStartupRecord(pane, terminal, "new-session"), code: 10},
		{name: "replacement terminal with the recorded session", call: 3, changedReply: ompStartupRecord(pane, "new-terminal", session), code: 10},
		{name: "missing session identity", call: 3, changedReply: ompStartupRecord(pane, terminal, ""), code: 10},
		{name: "slow identity read stays bounded", call: 3, changedReply: ompStartupRecord(pane, terminal, session), delay: 1800, code: 10},
		{name: "identity changes before commit", call: 4, changedReply: ompStartupRecord(pane, terminal, "new-session"), code: 2},
		{name: "full lane reports recorded and requested approvals", approval: "edits", code: 13},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var rules []fakecli.Rule
			if tc.sessionReportedAfterLaunch {
				rules = append(rules, fakecli.Rule{
					Argv: []string{"agent", "start", "worker"}, ArgvPrefix: true,
					Stdout: ompStartupRecord(pane, terminal, ""),
				})
			}
			if tc.call != 0 {
				rules = append(rules, fakecli.Rule{Argv: []string{"agent", "get", pane}, Call: tc.call, Stdout: tc.changedReply, Delay: tc.delay})
			}
			if tc.name == "same name moved to another pane" {
				rules = append(rules, fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Stdout: tc.changedReply})
			}
			f := newOMPSpawnFixture(t, rules...)
			f.env["SOHO_LANES"] = "on"
			for key, value := range map[string]string{"lane_build_roles": "worker,scouter", "lane_build_panes": "1"} {
				f.ctx.Entries[key] = core.ConfigEntry{Value: value, Source: "project"}
				f.ctx.Order = append(f.ctx.Order, key)
			}
			common := []string{"--kind", "omp", "--mcp", "inherit", "--model", ompFixtureModel, "--effort", "high"}
			initial := append([]string{"worker", "--fresh", "--approvals", "ask"}, common...)
			code, out, stderr, calls := runCmdSpawn(t, f, initial)
			if code != 0 || !spawnJSONHasField(t, out, "\"status\": \"ready\"") {
				t.Fatalf("launch exit=%d stdout=%q stderr=%q", code, out, stderr)
			}
			callsBefore := len(calls)
			before := rosterBytes(t, f)
			if tc.code != 0 {
				f.env["HERDR_PANE_ID"] = "p-caller"
			}
			approval := tc.approval
			if approval == "" {
				approval = "ask"
			}
			args := append([]string{"scouter", "--approvals", approval}, common...)
			code, out, stderr, refusal, calls := runLaneModelSpawn(t, f, args)
			afterCalls := calls[callsBefore:]
			if code != tc.code || len(herdrActs(afterCalls)) != 0 || renameCalls(afterCalls) != 0 {
				t.Fatalf("reuse exit=%d want=%d stdout=%q stderr=%q refusal=%q actions=%+v", code, tc.code, out, stderr, refusal, afterCalls)
			}
			paneReads := 0
			for _, call := range afterCalls {
				if slices.Equal(call.Argv, []string{"agent", "get", pane}) {
					paneReads++
				}
			}
			wantReads := 1
			if code == 0 || tc.call == 4 {
				wantReads = 2
			}
			if paneReads != wantReads {
				t.Fatalf("pane-addressed identity reads=%d want=%d calls=%+v", paneReads, wantReads, afterCalls)
			}
			if code == 0 {
				if !spawnJSONHasField(t, out, "\"name\": \"worker\"") || !spawnJSONHasField(t, out, "\"reused\": true") ||
					fieldAt(strings.Split(core.RosterLine(filepath.Join(f.state, "ws"), "worker"), "\t"), 3) != "scouter" {
					t.Fatalf("verified identity did not commit role and emit reuse: out=%q roster=%q", out, rosterBytes(t, f))
				}
				return
			}
			if rosterBytes(t, f) != before || strings.Contains(out, "\"reused\":true") {
				t.Fatalf("refusal changed roster or emitted reuse: out=%q roster=%q", out, rosterBytes(t, f))
			}
			switch code {
			case 2:
				if out != "" || !strings.Contains(refusal, "no matching verified managed OMP launch") {
					t.Fatalf("wrong verification refusal: out=%q refusal=%q", out, refusal)
				}
			case 10:
				if !spawnJSONHasField(t, out, "\"status\": \"busy\"") {
					t.Fatalf("invalid OMP identity should still occupy its slot: %q", out)
				}
			case 13:
				if !spawnJSONHasField(t, out, "\"status\": \"kind-mismatch\"") ||
					!spawnJSONHasField(t, out, "\"session_approvals\": \"ask\"") ||
					!spawnJSONHasField(t, out, "\"requested_approvals\": \"edits\"") ||
					!strings.Contains(stderr, "release it to free its slot") {
					t.Fatalf("lost approval mismatch: stdout=%q stderr=%q", out, stderr)
				}
			}
		})
	}
}

func TestSpawnOMPManagedPolicyReuseAndCapacity(t *testing.T) {
	for _, tc := range []struct {
		name, requested                       string
		lane, compatible, replaced, oldPolicy bool
		wantCode                              int
	}{
		{name: "ungrouped skips historical row", compatible: true},
		{name: "ungrouped skips replaced live worker", compatible: true, replaced: true},
		{name: "lane skips historical row", lane: true, compatible: true},
		{name: "lane preserves an explicit historical name with room", lane: true, compatible: true, requested: "worker", wantCode: 2},
		{name: "global cap still counts historical worker", wantCode: 8},
		{name: "lane cap still counts historical worker", lane: true, wantCode: 10},
		{name: "global cap still counts v1 worker", oldPolicy: true, wantCode: 8},
		{name: "lane cap still counts v1 worker", lane: true, oldPolicy: true, wantCode: 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := "replacement-session"
			if tc.oldPolicy {
				session = "original-session"
			}
			live := strings.Replace(ompStartupRecord("p-good", "term_compatible", "session-compatible"), `"name":"worker"`, `"name":"compatible"`, 1)
			f := newOMPSpawnFixture(t, idleHerdrRule("compatible"),
				fakecli.Rule{Argv: []string{"agent", "get", "p-good"}, Stdout: live},
				fakecli.Rule{Argv: []string{"agent", "get", "p-old"}, Stdout: ompStartupRecord("p-old", "term_old", session)},
				fakecli.Rule{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[{"name":"worker","pane_id":"p-old","agent_status":"idle"}]}}`})
			group := ""
			if tc.lane {
				group = "build"
				f.env["SOHO_LANES"] = "on"
				limit := "1"
				if tc.compatible {
					limit = "3"
				}
				for key, value := range map[string]string{"lane_build_roles": "worker", "lane_build_panes": limit} {
					f.ctx.Entries[key] = core.ConfigEntry{Value: value, Source: "project"}
					f.ctx.Order = append(f.ctx.Order, key)
				}
			} else if !tc.compatible {
				f.env["SOHO_MAX_WORKERS"] = "1"
			}
			row := []string{"worker", "p-old", "omp", "worker", "anthropic", "1", f.cwd, "now", ompFixtureModel, "ask", "worker", group, "", "", "high", "inherit", "", "--approval-mode always-ask"}
			if tc.oldPolicy {
				policy, err := filepath.EvalSymlinks(ompFixturePolicyPath(f.env["SOHO_SKILL_DIR"]))
				if err != nil {
					t.Fatal(err)
				}
				opening := decodeOMPStartupAgent(ompStartupRecord("p-old", "term_old", session))
				v1Path := filepath.Join(filepath.Dir(policy), "omp-managed-v1.yml")
				row = append(row, "", ompPolicyReceipt(v1Path, "ask", opening))
			}
			rows := []string{strings.Join(row, "\t")}
			if tc.compatible {
				policy, err := filepath.EvalSymlinks(ompFixturePolicyPath(f.env["SOHO_SKILL_DIR"]))
				if err != nil {
					t.Fatal(err)
				}
				if tc.replaced {
					opening := decodeOMPStartupAgent(ompStartupRecord("p-old", "term_old", "original-session"))
					rows[0] = strings.Join(append(slices.Clone(row), "", ompPolicyReceipt(policy, "ask", opening)), "\t")
				}
				row[0], row[1] = "compatible", "p-good"
				rows = append(rows, strings.Join(append(row, "", ompPolicyReceipt(policy, "ask", decodeOMPStartupAgent(live))), "\t"))
			}
			f.roster(t, rows...)
			before := rosterBytes(t, f)
			args := []string{"worker", "--kind", "omp", "--mcp", "inherit", "--approvals", "ask", "--model", ompFixtureModel, "--effort", "high"}
			if tc.requested != "" {
				args = append(args, "--name", tc.requested)
			}
			code, out, stderr, refusal, calls := runLaneModelSpawn(t, f, args)
			if code != tc.wantCode || len(herdrActs(calls)) != 0 || rosterBytes(t, f) != before {
				t.Fatalf("exit=%d want=%d out=%q stderr=%q refusal=%q terminal effects=%q", code, tc.wantCode, out, stderr, refusal, herdrActs(calls))
			}
			switch tc.wantCode {
			case 0:
				if !spawnJSONHasField(t, out, `"name": "compatible"`) || !spawnJSONHasField(t, out, `"reused": true`) {
					t.Fatalf("compatible worker did not receive reuse: %q", out)
				}
			case 2:
				if !strings.Contains(refusal, "no matching verified managed OMP launch") {
					t.Fatalf("wrong same-name refusal: %q", refusal)
				}
			case 8:
				if !strings.Contains(refusal, "max_workers=1 reached") {
					t.Fatalf("wrong global cap refusal: %q", refusal)
				}
			case 10:
				if !spawnJSONHasField(t, out, `"status": "busy"`) {
					t.Fatalf("wrong lane cap result: %q", out)
				}
			}
		})
	}
}

func assertOMPSpawnRefusal(t *testing.T, f spawnFixture, args []string, diagnostic string) string {
	t.Helper()
	path := filepath.Join(f.state, "ws", "agents.tsv")
	before, beforeErr := os.ReadFile(path)
	code, out, stderr, refusal, calls := runLaneModelSpawn(t, f, args)
	if code != 2 || out != "" || !strings.Contains(refusal, diagnostic) {
		t.Fatalf("exit=%d stdout=%q stderr=%q refusal=%q, want exit 2 and %q", code, out, stderr, refusal, diagnostic)
	}
	after, afterErr := os.ReadFile(path)
	if (beforeErr == nil) != (afterErr == nil) || string(after) != string(before) {
		t.Fatalf("refusal changed the roster: before=%q (%v), after=%q (%v)", before, beforeErr, after, afterErr)
	}
	for _, call := range calls {
		if len(call.Argv) >= 2 {
			switch strings.Join(call.Argv[:2], " ") {
			case "agent start", "agent rename", "agent prompt", "agent send-keys", "pane split", "pane send-text", "tab create":
				t.Fatalf("refusal produced a terminal effect: %q", call.Argv)
			}
		}
	}
	return refusal
}

func TestSpawnOMPLaneAndCoreAdmission(t *testing.T) {
	t.Run("lane inherit launches and reuses", func(t *testing.T) {
		f := newOMPSpawnFixture(t)
		f.env["SOHO_LANES"] = "on"
		for key, value := range map[string]string{
			"lane_build_roles": "worker", "lane_build_panes": "2",
			"lane_build_kind": "omp", "lane_build_mcp": "inherit",
			"lane_build_model": ompFixtureModel, "lane_build_effort": "max", "lane_build_approvals": "ask",
		} {
			f.ctx.Entries[key] = core.ConfigEntry{Value: value, Source: "project"}
			f.ctx.Order = append(f.ctx.Order, key)
		}
		args := []string{"worker"}
		code, out, stderr, refusal, _ := runLaneModelSpawn(t, f, args)
		if code != 0 || !spawnJSONHasField(t, out, `"status": "ready"`) {
			t.Fatalf("lane launch exit=%d stdout=%q stderr=%q refusal=%q", code, out, stderr, refusal)
		}
		code, out, stderr, refusal, _ = runLaneModelSpawn(t, f, args)
		if code != 0 || !spawnJSONHasField(t, out, `"reused": true`) {
			t.Fatalf("lane reuse exit=%d stdout=%q stderr=%q refusal=%q", code, out, stderr, refusal)
		}
		f.ctx.Entries["role_worker_mcp"] = core.ConfigEntry{Value: "none", Source: "project"}
		f.ctx.Order = append(f.ctx.Order, "role_worker_mcp")
		// Preserve the eligible row while isolating the refusal call log.
		fresh := newOMPSpawnFixture(t)
		fresh.ctx = f.ctx
		fresh.env["SOHO_LANES"] = "on"
		row := strings.Split(core.RosterLine(filepath.Join(f.state, "ws"), "worker"), "\t")
		row[6] = fresh.cwd
		fresh.roster(t, strings.Join(row, "\t"))
		assertOMPSpawnRefusal(t, fresh, args, "explicit MCP inherit")
	})
	t.Run("Core refused with otherwise admitted settings", func(t *testing.T) {
		f := newOMPSpawnFixture(t)
		f.env["SOHO_TRANSPORT"] = "hc"
		args := []string{"worker", "--kind", "omp", "--mcp", "inherit", "--approvals", "ask", "--model", ompFixtureModel, "--effort", "high"}
		if _, err := fakecli.Install(t, f.bin, "hc", nil); err != nil {
			t.Fatal(err)
		}
		const want = "spawn: managed OMP workers require the legacy Herdr transport. Use transport=herdr"
		if got := assertOMPSpawnRefusal(t, f, args, want); got != want {
			t.Fatalf("refusal=%q, want %q", got, want)
		}
		if _, err := os.Stat(filepath.Join(f.bin, "hc.calls.jsonl")); !os.IsNotExist(err) {
			t.Fatalf("hc call log exists or cannot be checked: %v", err)
		}
	})
}
