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
		fakecli.Rule{Argv: []string{"get", "b", "--json"}, Stdout: hcIdleNewWorker},
		fakecli.Rule{Argv: []string{"get", "c", "--json"}, Stdout: hcIdleNewWorker},
		fakecli.Rule{Argv: []string{"get", "t9", "--json"}, Stdout: hcIdleNewWorker},
		fakecli.Rule{Argv: []string{"get", "d", "--json"}, Stdout: hcIdleNewWorker},
		fakecli.Rule{Argv: []string{"get", "td", "--json"}, Stdout: hcIdleNewWorker},
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

// SOH-411: the native rename note is one per state directory and lane, even
// when the second spawn asks for a different new name.
func TestSpawnHcIdleRenameNoteOncePerLane(t *testing.T) {
	cwd, bin, state, env, ctx := hcReuseFixture(t, "3")
	for i, name := range []string{"b", "c"} {
		conn := newFakeSpawnRPCConn(rpcHelloSuccess + "\n" + rpcRunNewWorker + "\n")
		out, stderr, calls := spawnHcWithConn(t, bin, []string{"reviewer", "--name", name}, ctx, env, cwd, conn)
		if !spawnJSONHasField(t, out.String(), `"name": "`+name+`"`) {
			t.Fatalf("spawn %d: stdout=%s", i, out.String())
		}
		if n := hcRenameCalls(calls); n != 0 {
			t.Fatalf("spawn %d: rename calls=%d", i, n)
		}
		if n := strings.Count(stderr.String(), "was not reused"); n != 1-i {
			t.Fatalf("spawn %d: note count=%d want=%d stderr=%q", i, n, 1-i, stderr.String())
		}
		log, err := os.ReadFile(filepath.Join(state, "sX", "friction.log"))
		if err != nil {
			t.Fatal(err)
		}
		if n := strings.Count(string(log), "was not reused"); n != 1 {
			t.Fatalf("spawn %d: friction note count=%d want=1 friction=%q", i, n, log)
		}
	}
	// A different lane has its own marker and prints its own note.
	roles := env["SOHO_ROLES"]
	if err := os.WriteFile(filepath.Join(roles, "writer.md"), []byte("---\nkind: codex\neffort: high\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env["SOHO_LANE_DOCS_PANES"] = "2"
	env["SOHO_LANE_DOCS_ROLES"] = "writer"
	env["SOHO_LANE_DOCS_KIND"] = "codex"
	roster := filepath.Join(state, "sX", "agents.tsv")
	body, err := os.ReadFile(roster)
	if err != nil {
		t.Fatal(err)
	}
	row := "d\ttd\tcodex\twriter\topenai\t1\t" + cwd + "\tnow\t\tauto\twriter\tdocs\t\t\thigh\n"
	if err := os.WriteFile(roster, append(body, []byte(row)...), 0o600); err != nil {
		t.Fatal(err)
	}
	conn := newFakeSpawnRPCConn(rpcHelloSuccess + "\n" + rpcRunNewWorker + "\n")
	_, stderr, _ := spawnHcWithConn(t, bin, []string{"writer", "--name", "e"}, ctx, env, cwd, conn)
	want := "spawn: idle worker 'd' was not reused as 'e' because native Core cannot rename it; starting a new worker 'e'"
	if n := strings.Count(stderr.String(), want); n != 1 {
		t.Fatalf("docs lane note count=%d stderr=%q", n, stderr.String())
	}
	log, err := os.ReadFile(filepath.Join(state, "sX", "friction.log"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(log), "was not reused"); n != 2 {
		t.Fatalf("docs lane friction note count=%d want=2 friction=%q", n, log)
	}
}

// The capacity refusal is a different message on a different path: it stays
// on every attempt.
func TestSpawnHcIdleRenameRefusalEveryTime(t *testing.T) {
	cwd, bin, _, env, ctx := hcReuseFixture(t, "1")
	want := "idle worker 'a' was not reused as 'b' because native Core cannot rename it, and lane 'review' has no room"
	for i := 0; i < 2; i++ {
		conn := newFakeSpawnRPCConn()
		exit, out, stderr, _ := spawnHcExitWithConn(t, bin, []string{"reviewer", "--name", "b"}, ctx, env, cwd, conn)
		if exit == nil || exit.Code != 10 {
			t.Fatalf("attempt %d: exit=%+v out=%s stderr=%s", i, exit, out.String(), stderr.String())
		}
		if n := strings.Count(stderr.String(), want); n != 1 {
			t.Fatalf("attempt %d: refusal count=%d stderr=%q", i, n, stderr.String())
		}
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

func TestHcNameAdmissionBeforeModelCap(t *testing.T) {
	for _, tc := range []struct {
		name, panes, requested, global string
		stale                          bool
		code                           int
	}{
		{"unnamed", "1", "", "0", true, 0},
		{"same_name", "1", "a", "0", true, 0},
		{"new_name_full", "1", "wanted", "0", false, 10},
		{"stale_name_full", "1", "wanted", "0", true, 10},
		{"stale_name_spare", "2", "wanted", "0", true, 13},
		{"stale_name_global_full", "2", "wanted", "1", true, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cwd, bin, root, env, ctx := hcSpawnFixture(t, "reviewer", "codex")
			env["SOHO_LANES"] = "on"
			env["SOHO_LANE_REVIEW_PANES"] = tc.panes
			env["SOHO_LANE_REVIEW_KIND"] = "codex"
			env["SOHO_LANE_REVIEW_MODELS"] = "m1"
			env["SOHO_MODEL_CAP"] = "1"
			env["SOHO_MAX_WORKERS"] = tc.global
			rules := []fakecli.Rule{
				{Argv: []string{"ls", "--label", "soho.session=sX", "--json"}, Stdout: `{"terminals":[{"name":"a","address":"t7","state":"live","status":{"state":"idle"}}]}`},
			}
			rules = append(append(rules, hcFixtureRules()...),
				fakecli.Rule{Argv: []string{"get", "a", "--json"}, Stdout: hcIdleReusable},
				fakecli.Rule{Argv: []string{"get", "t7", "--json"}, Stdout: hcIdleReusable},
				fakecli.Rule{Argv: []string{"get", "wanted", "--json"}, Code: 3, Stderr: `{"error":{"kind":"not_found","message":"no such terminal"}}`},
			)
			if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
				t.Fatal(err)
			}
			sd := filepath.Join(root, "sX")
			if err := os.MkdirAll(sd, 0700); err != nil {
				t.Fatal(err)
			}
			core.RosterAppend(sd, []string{"a", "t7", "codex", "reviewer", "openai", "1", cwd, "now", "m1", "auto", "reviewer", "review", "", "", "high", "inherit"})
			if tc.stale {
				core.RosterAppend(sd, []string{"wanted", "t8", "codex", "reviewer", "openai", "1", cwd, "now", "m1", "auto", "reviewer", "review", "", "", "high", "inherit"})
			}
			argv := []string{"reviewer", "--mcp", "inherit", "--effort", "high"}
			if tc.requested != "" {
				argv = append(argv, "--name", tc.requested)
			}
			conn := newFakeSpawnRPCConn()
			exit, out, stderr, _ := spawnHcExitWithConn(t, bin, argv, ctx, env, cwd, conn)
			code, msg := 0, ""
			if exit != nil {
				code, msg = exit.Code, exit.Msg
			}
			t.Logf("code=%d message=%q stdout=%s stderr=%s native_writes=%d", code, msg, out.String(), stderr.String(), len(conn.getWritten()))
			if len(conn.getWritten()) != 0 {
				t.Fatal("expected this fixture to leave native terminal state alone")
			}
			if code != tc.code {
				t.Fatalf("exit=%d want=%d: %q", code, tc.code, msg)
			}
			if code == 10 && (!spawnJSONHasField(t, out.String(), `"status": "busy"`) || !strings.Contains(stderr.String(), "native Core cannot rename")) {
				t.Fatalf("wrong busy result: stdout=%s stderr=%s", out, stderr)
			}
			if tc.global == "1" && !strings.Contains(stderr.String(), "max_workers=1 is at capacity") {
				t.Fatalf("busy reason did not name the global cap: %s", stderr)
			}
			if code == 13 && !strings.Contains(msg, "at the cap") {
				t.Fatalf("wrong model cap result: %q", msg)
			}
			if code == 0 && !spawnJSONHasField(t, out.String(), `"reused": true`) {
				t.Fatalf("expected reuse: %s", out)
			}
		})
	}
}
