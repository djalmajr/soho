package spawn

import (
	"fmt"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// DJA-392: reuse also requires the MCP choice a worker was opened with
// (roster column 15) to match the request; a row written before that column
// carries no choice and matches any request, as reopen treats it.

// mcpLaneRow is a12Row with the effort (column 14) and MCP (column 15)
// columns; an empty mcp leaves the row at 15 columns.
func mcpLaneRow(name, model, wdir, mcp string) string {
	row := a12Row(name, "grok", model, wdir) + "\thigh"
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
		if !strings.Contains(stdout, `"created_pane": true`) || strings.Contains(stdout, `"reused": true`) {
			t.Fatalf("the idle opened with another MCP was reused: %s", stdout)
		}
	})

	t.Run("an idle worker opened with another MCP and no slot is 13 with the MCP message", func(t *testing.T) {
		f := a12Fixture(t, []fakecli.Rule{idleHerdrRule("old")}, "1")
		f.roster(t, mcpLaneRow("old", "grok-4.7", f.cwd, "inherit"))
		code, message, _ := runCmdSpawnExpectError(t, f, []string{"worker", "--kind", "grok", "--model", "grok-4.7", "--mcp", "none"})
		want := "spawn: lane 'build' has no idle worker matching MCP 'none' (idle: 'old' was opened with MCP 'inherit'); release it or raise the lane's panes"
		if code != 13 || message != want {
			t.Fatalf("exit=%d message=%q want %q", code, message, want)
		}
	})

	t.Run("a model mismatch keeps its own message when the MCP also differs", func(t *testing.T) {
		f := a12Fixture(t, []fakecli.Rule{idleHerdrRule("old")}, "1")
		f.roster(t, mcpLaneRow("old", "old-model", f.cwd, "inherit"))
		code, message, _ := runCmdSpawnExpectError(t, f, []string{"worker", "--kind", "grok", "--model", "grok-4.7", "--mcp", "none"})
		want := fmt.Sprintf("spawn: lane 'build' has no idle worker matching kind 'grok', model 'grok-4.7' and cwd '%s' (idle: 'old' runs grok old-model in %s); release it or raise the lane's panes", f.cwd, f.cwd)
		if code != 13 || message != want {
			t.Fatalf("exit=%d message=%q want %q", code, message, want)
		}
	})

	for _, tc := range []struct{ name, mcp string }{
		{"an idle worker opened with the same MCP is reused", "none"},
		{"a row from before the MCP column is reused", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := a12Fixture(t, []fakecli.Rule{idleHerdrRule("old")}, "1")
			f.roster(t, mcpLaneRow("old", "grok-4.7", f.cwd, tc.mcp))
			code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"worker", "--kind", "grok", "--model", "grok-4.7", "--mcp", "none"})
			if code != 0 || !strings.Contains(stdout, `"name": "old"`) || !strings.Contains(stdout, `"reused": true`) {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
		})
	}

	t.Run("a matching later idle wins over a first idle opened with another MCP", func(t *testing.T) {
		f := a12Fixture(t, []fakecli.Rule{idleHerdrRule("wrong"), idleHerdrRule("right")}, "2")
		f.roster(t, mcpLaneRow("wrong", "grok-4.7", f.cwd, "inherit"), mcpLaneRow("right", "grok-4.7", f.cwd, "none"))
		code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"worker", "--kind", "grok", "--model", "grok-4.7", "--mcp", "none"})
		if code != 0 || !strings.Contains(stdout, `"name": "right"`) || !strings.Contains(stdout, `"reused": true`) {
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
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newSpawnFixture(t, []fakecli.Rule{idleHerdrRule("worker")})
			f.roster(t, row(tc.rowMcp))
			got, _, blocked := FindReusableMcp("worker", "grok", "/tmp/work", "", "grok-4.7", "ask", tc.want, f.ctx, f.env, f.cwd)
			if got != tc.reused || blocked {
				t.Fatalf("FindReusableMcp = %q blocked=%v, want %q", got, blocked, tc.reused)
			}
		})
	}
	t.Run("FindReusable ignores the MCP choice", func(t *testing.T) {
		f := newSpawnFixture(t, []fakecli.Rule{idleHerdrRule("worker")})
		f.roster(t, row("inherit"))
		if got, _, _ := FindReusable("worker", "grok", "/tmp/work", "", "grok-4.7", "ask", f.ctx, f.env, f.cwd); got != "worker" {
			t.Fatalf("FindReusable = %q, want worker", got)
		}
	})
}
