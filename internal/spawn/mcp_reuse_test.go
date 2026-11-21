package spawn

import (
	"testing"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// DJA-392: reuse also requires the MCP choice a worker was opened with
// (roster column 15) to match the request. A row from before that column
// retains inherited MCP in a lane.

// mcpLaneRow is a12Row with the MCP (column 15)
// column; an empty mcp leaves the row at 15 columns.
func mcpLaneRow(name, model, wdir, mcp string) string {
	row := a12Row(name, "grok", model, wdir)
	if mcp != "" {
		row += "\t" + mcp
	}
	return row
}

func TestSpawnLaneReuseRespectsMcp(t *testing.T) {
	t.Run("an idle worker opened with another MCP is not reused; the lane opens a new pane when it has a slot", func(t *testing.T) {
		f := a12Fixture(t, append([]fakecli.Rule{idleHerdrRule("old")}, freshSpawnRules()...), "2")
		f.roster(t, mcpLaneRow("old", "grok-4.7", f.cwd, "inherit"))
		code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"worker", "--kind", "grok", "--model", "grok-4.7", "--mcp", "none"})
		if code != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		if !spawnJSONHasField(t, stdout, `"created_pane": true`) || spawnJSONHasField(t, stdout, `"reused": true`) {
			t.Fatalf("the idle opened with another MCP was reused: %s", stdout)
		}
	})

	t.Run("an idle worker opened with another MCP and no slot is 13 with the MCP message", func(t *testing.T) {
		f := a12Fixture(t, []fakecli.Rule{idleHerdrRule("old")}, "1")
		f.roster(t, mcpLaneRow("old", "grok-4.7", f.cwd, "inherit"))
		code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"worker", "--kind", "grok", "--model", "grok-4.7", "--mcp", "none"})
		assertGroupMismatch(t, code, stdout, stderr, "mcp differs (inherit vs none)", `"session_mcp": "inherit"`, `"requested_mcp": "none"`)
		assertNoStart(t, calls)
	})

	t.Run("model and MCP differences appear together", func(t *testing.T) {
		f := a12Fixture(t, []fakecli.Rule{idleHerdrRule("old")}, "1")
		f.roster(t, mcpLaneRow("old", "old-model", f.cwd, "inherit"))
		code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"worker", "--kind", "grok", "--model", "grok-4.7", "--mcp", "none"})
		assertGroupMismatch(t, code, stdout, stderr, "model differs (old-model vs grok-4.7), mcp differs (inherit vs none)", `"session_model": "old-model"`, `"requested_model": "grok-4.7"`, `"session_mcp": "inherit"`, `"requested_mcp": "none"`)
		assertNoStart(t, calls)
	})

	for _, tc := range []struct{ name, mcp, request string }{
		{"an idle worker opened with the same MCP is reused", "none", "none"},
		{"a row from before the MCP column matches inherit", "", "inherit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := a12Fixture(t, []fakecli.Rule{idleHerdrRule("old")}, "1")
			f.roster(t, mcpLaneRow("old", "grok-4.7", f.cwd, tc.mcp))
			code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"worker", "--kind", "grok", "--model", "grok-4.7", "--mcp", tc.request})
			if code != 0 || !spawnJSONHasField(t, stdout, `"name": "old"`) || !spawnJSONHasField(t, stdout, `"reused": true`) {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
		})
	}

	t.Run("a row from before the MCP column differs from none", func(t *testing.T) {
		f := a12Fixture(t, []fakecli.Rule{idleHerdrRule("old")}, "1")
		f.roster(t, mcpLaneRow("old", "grok-4.7", f.cwd, ""))
		code, stdout, stderr, calls := runCmdSpawn(t, f, []string{"worker", "--kind", "grok", "--model", "grok-4.7", "--mcp", "none"})
		assertGroupMismatch(t, code, stdout, stderr, "mcp differs ((empty) vs none)", `"session_mcp": ""`, `"requested_mcp": "none"`)
		assertNoStart(t, calls)
	})

	t.Run("a matching later idle wins over a first idle opened with another MCP", func(t *testing.T) {
		f := a12Fixture(t, []fakecli.Rule{idleHerdrRule("wrong"), idleHerdrRule("right")}, "2")
		f.roster(t, mcpLaneRow("wrong", "grok-4.7", f.cwd, "inherit"), mcpLaneRow("right", "grok-4.7", f.cwd, "none"))
		code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"worker", "--kind", "grok", "--model", "grok-4.7", "--mcp", "none"})
		if code != 0 || !spawnJSONHasField(t, stdout, `"name": "right"`) || !spawnJSONHasField(t, stdout, `"reused": true`) {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
	})
}

func TestFindReusableMcp(t *testing.T) {
	row := func(mcp string) string {
		r := spawnRosterRow("worker", "worker", "grok-4.7", "full", "")
		if mcp != "" {
			r += "\t" + mcp
		}
		return r
	}
	cases := []struct {
		name, rowMcp, want, reused string
	}{
		{"another MCP is not reused", "inherit", "none", ""},
		{"the same MCP is reused", "none", "none", "worker"},
		{"a row from before the MCP column is reused", "", "none", "worker"},
		{"an empty request ignores the MCP", "inherit", "", "worker"},
		{"the same list is reused", "a,b", "a,b", "worker"},
		{"another list is not reused", "a,b", "a", ""},
		{"none is not reused for a list", "none", "a,b", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newSpawnFixture(t, []fakecli.Rule{idleHerdrRule("worker")})
			f.roster(t, row(tc.rowMcp))
			got, _, blocked := FindReusableMcp("worker", "grok", "/tmp/work", "", "grok-4.7", "ask", tc.want, "high", f.ctx, f.env, f.cwd)
			if got != tc.reused || blocked {
				t.Fatalf("FindReusableMcp = %q blocked=%v, want %q", got, blocked, tc.reused)
			}
		})
	}
	t.Run("FindReusable ignores the MCP choice", func(t *testing.T) {
		f := newSpawnFixture(t, []fakecli.Rule{idleHerdrRule("worker")})
		f.roster(t, row("inherit"))
		if got, _, _ := FindReusable("worker", "grok", "/tmp/work", "", "grok-4.7", "ask", "high", f.ctx, f.env, f.cwd); got != "worker" {
			t.Fatalf("FindReusable = %q, want worker", got)
		}
	})
}
