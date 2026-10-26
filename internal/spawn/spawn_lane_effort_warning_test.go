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

func TestSpawnLaneEffortWarning(t *testing.T) {
	cases := []struct {
		name, front, configured, kindDefault, kind, max, flag, laneEffort, wantSource, wantEffort string
		noLane, reuse                                                                             bool
	}{
		{name: "role-config", configured: "xhigh", wantSource: "role.security-reviewer.effort", wantEffort: "high"},
		{name: "role-file", front: "xhigh", wantSource: "role file", wantEffort: "high"},
		{name: "config-before-file", configured: "xhigh", front: "medium", wantSource: "role.security-reviewer.effort", wantEffort: "high"},
		{name: "explicit-flag", configured: "xhigh", flag: "xhigh", wantEffort: "xhigh"},
		{name: "no-request", wantEffort: "high"},
		{name: "kind-default-only", kindDefault: "xhigh", wantEffort: "high"},
		{name: "same-request", front: "high", wantEffort: "high"},
		{name: "kind-ceiling", front: "xhigh", kind: "gemini", wantEffort: "high"},
		{name: "max-effort-ceiling", configured: "xhigh", max: "high", wantEffort: "high"},
		{name: "codex-model-ceiling", configured: "max", laneEffort: "xhigh", wantEffort: "xhigh"},
		{name: "no-lane", configured: "xhigh", noLane: true, wantEffort: "xhigh"},
		{name: "reuse", front: "xhigh", reuse: true, wantSource: "role file", wantEffort: "high"},
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
				f.roster(t, laneReuseRow("audit", f.cwd))
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
			want := "spawn: 'audit' runs security-reviewer at the lane's effort high; the role asks for xhigh (" + tc.wantSource + ") — pass --effort xhigh or set lane.review.effort"
			if tc.wantSource != "" {
				if strings.Count(stderr, "the role asks for") != 1 || !strings.Contains(stderr, "soho: warning: "+want+"\n") {
					t.Fatalf("stderr=%q want exact warning=%q", stderr, want)
				}
				if !strings.Contains(frictionLog(t, f), want) {
					t.Fatalf("warning absent from friction: %s", frictionLog(t, f))
				}
			} else if strings.Contains(stderr, "the role asks for") {
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
			t.Logf("effective=%s reused=%v warning=%v stderr=%s", tc.wantEffort, obj["reused"], tc.wantSource != "", stderr)
		})
	}
}
