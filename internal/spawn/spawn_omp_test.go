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

func newOMPSpawnFixture(t *testing.T) spawnFixture {
	t.Helper()
	f := newSpawnFixture(t, append([]fakecli.Rule{idleHerdrRule("worker")}, freshSpawnRules()...))
	configureSpawnFixture(t, &f)
	f.env["PATH"] = f.bin
	f.env["TMPDIR"] = f.root
	f.env["SOHO_CALLER_CHECK"] = "off"
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
			args := []string{"worker", "--kind", "omp", "--mcp", "inherit", "--approvals", tc.approvals, "--model", ompFixtureModel, "--effort", "xhigh"}
			code, out, stderr, calls := runCmdSpawn(t, f, args)
			if code != 0 || !spawnJSONHasField(t, out, `"status": "ready"`) {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
			}
			want := []string{"--approval-mode", tc.mode, "--model", ompFixtureModel, "--thinking", "xhigh"}
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
			if fieldAt(row, 9) != tc.approvals || fieldAt(row, 15) != "inherit" || fieldAt(row, 17) != "--approval-mode "+tc.mode {
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
	for _, tc := range []struct{ name, approvals, receipt string }{
		{"old row", "ask", ""}, {"mismatched receipt", "ask", "--approval-mode yolo"}, {"broader worker", "full", "--approval-mode yolo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newOMPSpawnFixture(t)
			row := []string{"worker", "p2", "omp", "worker", "anthropic", "1", f.cwd, "now", ompFixtureModel, tc.approvals, "worker", "", "false", "", "high", "inherit"}
			if tc.receipt != "" {
				row = append(row, "", tc.receipt)
			}
			f.roster(t, strings.Join(row, "\t"))
			args := []string{"worker", "--kind", "omp", "--mcp", "inherit", "--approvals", "ask", "--model", ompFixtureModel, "--effort", "high"}
			assertOMPSpawnRefusal(t, f, args, "no matching verified launch approval and MCP record")
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
