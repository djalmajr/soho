package spawn

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestSpawnLaneEffortPrecedence(t *testing.T) {
	cases := []struct {
		name, front, configured, kindDefault, kind, max, flag, laneEffort, wantEffort string
		noLane, reuse                                                                 bool
	}{
		{name: "role-config", configured: "xhigh", wantEffort: "xhigh"},
		{name: "role-file", front: "xhigh", wantEffort: "high"},
		{name: "config-before-file", configured: "xhigh", front: "medium", wantEffort: "xhigh"},
		{name: "explicit-flag", configured: "xhigh", flag: "xhigh", wantEffort: "xhigh"},
		{name: "no-request", wantEffort: "high"},
		{name: "kind-default-only", kindDefault: "xhigh", wantEffort: "high"},
		{name: "same-request", front: "high", wantEffort: "high"},
		{name: "kind-ceiling", front: "xhigh", kind: "gemini", wantEffort: "high"},
		{name: "max-effort-ceiling", configured: "xhigh", max: "high", wantEffort: "high"},
		{name: "codex-model-ceiling", configured: "max", laneEffort: "xhigh", wantEffort: "xhigh"},
		{name: "opencode-no-effort-mapping", configured: "xhigh", kind: "opencode", wantEffort: "xhigh"},
		{name: "no-lane", configured: "xhigh", noLane: true, wantEffort: "xhigh"},
		{name: "reuse", front: "xhigh", reuse: true, wantEffort: "high"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rules := freshSpawnRules()
			if tc.reuse {
				rules = append([]fakecli.Rule{idleHerdrRule("audit")}, rules...)
			}
			f := newSpawnFixture(t, rules)
			configureSpawnFixture(t, &f)
			kind := tc.kind
			if kind == "" {
				kind = "codex"
			}
			if _, err := fakecli.Install(t, f.bin, kind, nil); err != nil {
				t.Fatal(err)
			}
			front := "---\nkind: " + kind + "\napprovals: auto\n"
			if tc.front != "" {
				front += "effort: " + tc.front + "\n"
			}
			front += "---\n"
			if err := os.WriteFile(filepath.Join(f.root, "roles", "security-reviewer.md"), []byte(front), 0600); err != nil {
				t.Fatal(err)
			}
			f.env["HERDR_PANE_ID"] = "p1"
			f.env["SOHO_LANES"] = "on"
			if tc.noLane {
				f.env["SOHO_LANES"] = "off"
			}
			laneEffort := tc.laneEffort
			if laneEffort == "" {
				laneEffort = "high"
			}
			for key, value := range map[string]string{"lane_review_roles": "security-reviewer", "lane_review_kind": kind, "lane_review_effort": laneEffort, "lane_review_panes": "1", "role_security_reviewer_effort": tc.configured, "effort_" + kind: tc.kindDefault, "max_effort": tc.max} {
				if value != "" {
					f.ctx.Entries[key] = core.ConfigEntry{Value: value, Source: "project"}
					f.ctx.Order = append(f.ctx.Order, key)
				}
			}
			if tc.reuse {
				f.roster(t, strings.TrimSuffix(laneReuseRow("audit", f.cwd), "\tnone")+"\tinherit")
			}
			args := []string{"security-reviewer", "--name", "audit"}
			if tc.flag != "" {
				args = append(args, "--effort", tc.flag)
			}
			code, out, stderr, calls := runCmdSpawn(t, f, args)
			if code != 0 {
				t.Fatalf("exit=%d out=%s stderr=%s calls=%v", code, out, stderr, calls)
			}
			var obj map[string]any
			if err := json.Unmarshal([]byte(out), &obj); err != nil {
				t.Fatalf("JSON %s: %v", out, err)
			}
			if obj["effort"] != tc.wantEffort {
				t.Fatalf("effort=%v want=%s stderr=%s", obj["effort"], tc.wantEffort, stderr)
			}
			if strings.Contains(stderr, "the role asks for") || strings.Contains(frictionLog(t, f), "the role asks for") {
				t.Fatalf("unexpected effort warning: %s", stderr)
			}
			if tc.reuse {
				if obj["reused"] != true {
					t.Fatalf("reuse branch not reached: %s", out)
				}
				for _, call := range calls {
					if len(call.Argv) >= 2 && (call.Argv[0]+" "+call.Argv[1] == "agent start" || call.Argv[0]+" "+call.Argv[1] == "pane split") {
						t.Fatalf("reuse opened worker: %v", call)
					}
				}
			}
			t.Logf("effective=%s reused=%v stderr=%s", tc.wantEffort, obj["reused"], stderr)
		})
	}
}
