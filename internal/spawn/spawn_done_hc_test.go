package spawn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/backend"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestHcDoneReviewerSpawnReuse(t *testing.T) {
	cwd, bin, state, env, ctx := hcSpawnFixture(t, "reviewer", "codex")
	env["SOHO_LANES"] = "on"
	ctx.Entries["lane_review_roles"] = core.ConfigEntry{Value: "reviewer", Source: "project"}
	ctx.Entries["lane_review_kind"] = core.ConfigEntry{Value: "codex", Source: "project"}
	ctx.Entries["lane_review_panes"] = core.ConfigEntry{Value: "1", Source: "project"}
	ctx.Order = []string{"lane_review_roles", "lane_review_kind", "lane_review_panes"}
	sd := filepath.Join(state, "sX")
	if err := os.MkdirAll(sd, 0o700); err != nil {
		t.Fatal(err)
	}
	row := strings.Join([]string{"review", "t7", "codex", "reviewer", "openai", "0", cwd, "now", "gpt-5", "full", "reviewer", "review", "", "", "high"}, "\t")
	if err := os.WriteFile(filepath.Join(sd, "agents.tsv"), []byte(row+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(sd, "previous.md")
	if err := os.WriteFile(report, []byte("done\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(core.LastReportPath(sd, "review"), []byte(report+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rules := []fakecli.Rule{
		{Argv: []string{"get", "t1", "--json"}, Stdout: hcCallerTerminal},
		{Argv: []string{"get", "review", "--json"}, Stdout: `{"address":"t7","state":"live","status":{"state":"done","app":"codex"}}`},
		{Argv: []string{"ls", "--label", "soho.session=sX", "--json"}, Stdout: `{"terminals":[{"address":"t7","name":"review","state":"live","status":{"state":"done","app":"codex"}}]}`},
	}
	if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
		t.Fatal(err)
	}
	conn := newFakeSpawnRPCConn()
	exit, out, stderr, calls := spawnHcExitWithConn(t, bin, []string{"reviewer", "--model", "gpt-5", "--effort", "high", "--approvals", "full"}, ctx, env, cwd, conn)
	if exit != nil || !spawnJSONHasField(t, out.String(), `"reused": true`) || !spawnJSONHasField(t, out.String(), `"name": "review"`) || conn.getConnectCount() != 0 {
		t.Fatalf("exit=%+v out=%s stderr=%s calls=%#v connects=%d", exit, out.String(), stderr.String(), calls, conn.getConnectCount())
	}
	assertHcOnlyReads(t, bin, calls)
}

func TestHcDoneReviewerLaneReuse(t *testing.T) {
	for _, tc := range []struct{ state, want string }{{"done", "review"}, {"idle", "review"}, {"working", ""}} {
		t.Run(tc.state, func(t *testing.T) {
			cwd, bin, state, env, _ := hcSpawnFixture(t, "reviewer", "codex")
			sd := filepath.Join(state, "sX")
			if err := os.MkdirAll(sd, 0o700); err != nil {
				t.Fatal(err)
			}
			row := strings.Join([]string{"review", "t7", "codex", "reviewer", "openai", "0", cwd, "now", "gpt-5", "full", "reviewer", "review", "", "", "high"}, "\t")
			if err := os.WriteFile(filepath.Join(sd, "agents.tsv"), []byte(row+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			report := filepath.Join(sd, "previous.md")
			if err := os.WriteFile(report, []byte("done\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(core.LastReportPath(sd, "review"), []byte(report+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := fakecli.Install(t, bin, "hc", []fakecli.Rule{{Argv: []string{"get", "review", "--json"}, Stdout: `{"address":"t7","state":"live","status":{"state":"` + tc.state + `","app":"codex"}}`}}); err != nil {
				t.Fatal(err)
			}
			request := laneReuseRequest{role: "reviewer", kind: "codex", model: "gpt-5", effort: "high", approvals: "full", mcp: "inherit", workerCwd: cwd, sd: sd, env: env, processCwd: cwd}
			var gone []string
			picked := request.pick(core.LaneWorkers(sd, "review"), backend.New(env).Runtime, nil, &gone)
			got, _, _ := strings.Cut(picked.line, "\t")
			if len(picked.differences) != 0 {
				t.Fatalf("unexpected differences: %+v", picked.differences)
			}
			if got != tc.want {
				t.Fatalf("native state=%s got=%q want=%q calls=%#v", tc.state, got, tc.want, hcCallLog(t, bin))
			}
			calls := hcCallLog(t, bin)
			if len(calls) != 1 || strings.Join(calls[0].Argv, " ") != "get review --json" {
				t.Fatalf("calls=%#v", calls)
			}
		})
	}
}
