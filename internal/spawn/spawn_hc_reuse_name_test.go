package spawn

// SOH-289: native Core has no terminal rename. On hc a lane reuse happens only
// with no --name or a --name equal to the reusable worker; a different --name
// opens a new worker (as --fresh would) and never calls a rename.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

const (
	hcIdleReusable   = `{"address":"t7","state":"live","reported":true,"status":{"state":"idle"}}`
	hcIdleNewWorker  = `{"address":"t8","state":"live","reported":true,"status":{"state":"idle"}}`
	rpcRunNewWorker  = `{"jsonrpc":"2.0","id":2,"result":{"address":"t8"}}`
	hcReuseWorkerRow = "a\tt7\tcodex\treviewer\topenai\t1\t%s\tnow\t\tauto\treviewer\treview\t\t\thigh"
)

func hcReuseFixture(t *testing.T, lanePanes string) (cwd, bin, state string, env platform.Env, ctx *core.Config) {
	t.Helper()
	cwd, bin, state, env, ctx = hcSpawnFixture(t, "reviewer", "codex")
	env["SOHO_LANES"] = "on"
	env["SOHO_LANE_REVIEW_PANES"] = lanePanes
	roles := env["SOHO_ROLES"]
	if err := os.WriteFile(filepath.Join(roles, "reviewer.md"), []byte("---\nkind: codex\neffort: high\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rules := append(hcFixtureRules(),
		fakecli.Rule{Argv: []string{"get", "a", "--json"}, Stdout: hcIdleReusable},
		fakecli.Rule{Argv: []string{"get", "t7", "--json"}, Stdout: hcIdleReusable},
		fakecli.Rule{Argv: []string{"get", "t8", "--json"}, Stdout: hcIdleNewWorker},
	)
	if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(state, "sX"), 0o700); err != nil {
		t.Fatal(err)
	}
	body := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\tburst\targs\teffort\n" +
		strings.Replace(hcReuseWorkerRow, "%s", cwd, 1) + "\n"
	if err := os.WriteFile(filepath.Join(state, "sX", "agents.tsv"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return cwd, bin, state, env, ctx
}

func hcRenameCalls(calls []fakecli.Call) int {
	n := 0
	for _, c := range calls {
		if len(c.Argv) > 0 && (c.Argv[0] == "rename" || len(c.Argv) > 1 && c.Argv[0] == "agent" && c.Argv[1] == "rename") {
			n++
		}
	}
	return n
}

func TestSpawnHcLaneReuseNameRule(t *testing.T) {
	t.Run("a different --name starts a new worker without a rename", func(t *testing.T) {
		cwd, bin, state, env, ctx := hcReuseFixture(t, "2")
		conn := newFakeSpawnRPCConn(rpcHelloSuccess + "\n" + rpcRunNewWorker + "\n")
		out, stderr, calls := spawnHcWithConn(t, bin, []string{"reviewer", "--name", "b"}, ctx, env, cwd, conn)
		assertHcOnlyReads(t, bin, calls)
		if n := hcRenameCalls(calls); n != 0 {
			t.Fatalf("rename calls=%d, want 0: %#v", n, calls)
		}
		reqs := parseAllTerminalRunReqs(t, conn.getWritten())
		if len(reqs) != 1 {
			t.Fatalf("terminal.run calls=%d, want 1: %v", len(reqs), reqs)
		}
		if _, params := parseTerminalRunReq(t, conn.getWritten()); params["name"] != "b" {
			t.Fatalf("terminal.run name=%v, want b", params["name"])
		}
		if !spawnJSONHasField(t, out.String(), `"name": "b"`) || spawnJSONHasField(t, out.String(), `"reused": true`) {
			t.Fatalf("stdout=%s", out.String())
		}
		want := "soho: warning: spawn: idle worker 'a' was not reused as 'b' because native Core cannot rename it; starting a new worker 'b'"
		if got := strings.Count(stderr.String(), "was not reused"); got != 1 || !strings.Contains(stderr.String(), want) {
			t.Fatalf("stderr=%q, want exactly one line %q", stderr.String(), want)
		}
		roster, err := os.ReadFile(filepath.Join(state, "sX", "agents.tsv"))
		if err != nil {
			t.Fatal(err)
		}
		if core.RosterLine(filepath.Join(state, "sX"), "a") == "" || core.RosterLine(filepath.Join(state, "sX"), "b") == "" {
			t.Fatalf("roster must keep a and add b: %q", roster)
		}
	})

	for _, tc := range []struct {
		name string
		argv []string
	}{
		{"no --name reuses the idle worker", []string{"reviewer"}},
		{"--name equal to the idle worker reuses it", []string{"reviewer", "--name", "a"}},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			cwd, bin, _, env, ctx := hcReuseFixture(t, "2")
			conn := newFakeSpawnRPCConn()
			out, stderr, calls := spawnHcWithConn(t, bin, tc.argv, ctx, env, cwd, conn)
			assertHcOnlyReads(t, bin, calls)
			if n := hcRenameCalls(calls); n != 0 {
				t.Fatalf("rename calls=%d, want 0", n)
			}
			if n := len(conn.getWritten()); n != 0 || conn.getConnectCount() != 0 {
				t.Fatalf("a reuse must not start a terminal: written=%d connects=%d", n, conn.getConnectCount())
			}
			if !spawnJSONHasField(t, out.String(), `"name": "a"`) || !spawnJSONHasField(t, out.String(), `"reused": true`) {
				t.Fatalf("stdout=%s", out.String())
			}
			if !strings.Contains(stderr.String(), "reusing idle lane 'review' worker 'a' as reviewer; existing session context is retained") || strings.Contains(stderr.String(), "was not reused") {
				t.Fatalf("stderr=%q", stderr.String())
			}
		})
	}
}

// The same fixture with a lane of one slot: the idle worker fills it, so a
// different --name is refused like --fresh (exit 10), with no rename call and
// no terminal.run.
func TestSpawnHcLaneReuseDifferentNameAtCapacityIsRefused(t *testing.T) {
	cwd, bin, state, env, ctx := hcReuseFixture(t, "1")
	conn := newFakeSpawnRPCConn()
	exit, out, stderr, calls := spawnHcExitWithConn(t, bin, []string{"reviewer", "--name", "b"}, ctx, env, cwd, conn)
	if exit == nil || exit.Code != 10 {
		t.Fatalf("exit=%+v, want code 10\nstderr: %s", exit, stderr.String())
	}
	assertHcOnlyReads(t, bin, calls)
	if n := hcRenameCalls(calls); n != 0 {
		t.Fatalf("rename calls=%d, want 0", n)
	}
	if len(conn.getWritten()) != 0 || conn.getConnectCount() != 0 {
		t.Fatalf("a refused spawn must not start a terminal")
	}
	if !strings.Contains(out.String(), `"status":"busy"`) || !strings.Contains(stderr.String(), "idle worker 'a' was not reused as 'b' because native Core cannot rename it, and lane 'review' has no room") {
		t.Fatalf("stdout=%s stderr=%q", out.String(), stderr.String())
	}
	if core.RosterLine(filepath.Join(state, "sX"), "a") == "" || core.RosterLine(filepath.Join(state, "sX"), "b") != "" {
		t.Fatalf("the roster must be unchanged")
	}
}
