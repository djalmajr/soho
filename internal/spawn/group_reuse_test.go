package spawn

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
)

func TestGroupReuseIdenticalOpeningEffort(t *testing.T) {
	for _, tc := range []struct {
		name, roleEffort, opening string
		explicitKind              bool
	}{
		{"role overrides group", "low", "low", false},
		{"same kind flag keeps group effort", "", "high", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := laneModelFixtureWithCapacity(t, "off", "", "2", idleHerdrRule("scouter"))
			for key, value := range map[string]string{
				"lane_build_kind": "", "lane_build_effort": "high", "role_scouter_kind": "grok",
				"role_scouter_effort": tc.roleEffort, "effort_grok": "low",
			} {
				f.ctx.Entries[key] = core.ConfigEntry{Value: value, Source: "project"}
			}
			args := []string{"scouter"}
			if tc.explicitKind {
				args = append(args, "--kind", "grok")
			}
			first, out, stderr, refusal, _ := runLaneModelSpawn(t, f, args)
			if first != 0 || !spawnJSONHasField(t, out, `"created_pane": true`) || !spawnJSONHasField(t, out, `"effort": "`+tc.opening+`"`) {
				t.Fatalf("first spawn exit=%d stdout=%q stderr=%q refusal=%q", first, out, stderr, refusal)
			}
			before := rosterBytes(t, f)
			if err := os.Remove(filepath.Join(f.bin, "herdr.calls.jsonl")); err != nil {
				t.Fatal(err)
			}
			code, out, stderr, refusal, calls := runLaneModelSpawn(t, f, args)
			if code != 0 || !spawnJSONHasField(t, out, `"reused": true`) || rosterBytes(t, f) != before || len(herdrActs(calls)) != 0 {
				t.Fatalf("second spawn exit=%d stdout=%q stderr=%q refusal=%q actions=%v roster=%q", code, out, stderr, refusal, herdrActs(calls), rosterBytes(t, f))
			}
			t.Logf("reused recorded opening effort %q without terminal actions", tc.opening)
		})
	}
}

func TestGroupReuseHistoricalOpeningEffort(t *testing.T) {
	for _, tc := range []struct {
		name     string
		columns  []string
		request  string
		wantCode int
	}{
		{"original precedence matches", nil, "high", 0},
		{"original precedence differs", nil, "low", 13},
		{"recorded empty does not reconstruct", []string{"", "inherit"}, "high", 13},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := laneModelFixtureWithCapacity(t, "off", "", "1", idleHerdrRule("existing"))
			f.ctx.Entries["lane_build_kind"] = core.ConfigEntry{Source: "project"}
			f.ctx.Entries["lane_build_effort"] = core.ConfigEntry{Value: "high", Source: "project"}
			f.ctx.Entries["role_scouter_kind"] = core.ConfigEntry{Value: "grok", Source: "project"}
			f.ctx.Entries["role_scouter_effort"] = core.ConfigEntry{Value: "low", Source: "project"}
			row := []string{"existing", "p-existing", "grok", "scouter", "xai", "1", f.cwd, "now", "m1", "auto", "scouter", "build", "", ""}
			f.roster(t, strings.Join(append(row, tc.columns...), "\t"))
			before := rosterBytes(t, f)
			code, out, stderr, _, calls := runLaneModelSpawn(t, f, []string{"scouter", "--effort", tc.request, "--model", "m1", "--mcp", "inherit"})
			if code != tc.wantCode || rosterBytes(t, f) != before || len(herdrActs(calls)) != 0 {
				t.Fatalf("exit=%d want=%d stdout=%q stderr=%q actions=%v roster=%q", code, tc.wantCode, out, stderr, herdrActs(calls), rosterBytes(t, f))
			}
			switch {
			case code == 0:
				if !spawnJSONHasField(t, out, `"reused": true`) {
					t.Fatalf("compatible historical row was not reused: %s", out)
				}
			case tc.columns == nil:
				assertGroupMismatch(t, code, out, stderr, "effort differs (high vs low)", `"session_effort": "high"`, `"requested_effort": "low"`)
			default:
				assertGroupMismatch(t, code, out, stderr, "effort differs ((empty) vs high)", `"session_effort": ""`, `"requested_effort": "high"`)
			}
			t.Logf("exit=%d stdout=%s stderr=%s", code, out, stderr)
		})
	}
}

func TestGroupReuseKindDiagnosticSources(t *testing.T) {
	for _, panes := range []string{"1", "2"} {
		t.Run("panes="+panes, func(t *testing.T) {
			f := m1Fixture(t, "grok", "idle")
			f.ctx.Entries["lane_build_kind"] = core.ConfigEntry{Value: "grok", Source: "project"}
			f.ctx.Entries["role_worker_kind"] = core.ConfigEntry{Value: "codex", Source: "user"}
			f.ctx.Entries["lane_build_panes"] = core.ConfigEntry{Value: panes, Source: "project"}
			f.roster(t, strings.Join([]string{"oldgrok", "p-oldgrok", "grok", "tasker", "", "1", f.cwd, "now", "", "ask", "tasker", "build", "", "", "high", "inherit"}, "\t"))
			before := rosterBytes(t, f)
			code, out, stderr, calls := runCmdSpawn(t, f, []string{"worker", "--effort", "high"})
			want := 0
			if panes == "1" {
				want = 13
			}
			if code != want || !strings.Contains(stderr, "kind differs (grok vs codex)") || !strings.Contains(stderr, "role.worker.kind (user)") || !strings.Contains(stderr, "lane.build.kind=grok") {
				t.Fatalf("exit=%d want=%d stdout=%q stderr=%q", code, want, out, stderr)
			}
			if want == 13 {
				assertGroupMismatch(t, code, out, stderr, "kind differs (grok vs codex)", `"session_kind": "grok"`, `"requested_kind": "codex"`)
				if rosterBytes(t, f) != before || len(herdrActs(calls)) != 0 {
					t.Fatalf("full lane changed: actions=%v roster=%q", herdrActs(calls), rosterBytes(t, f))
				}
			} else {
				assertFreshForMismatch(t, out, stderr, "oldgrok", "kind differs (grok vs codex)")
				if strings.Contains(stderr, "still holds idle 'oldgrok'") || !strings.HasPrefix(rosterBytes(t, f), before) {
					t.Fatalf("duplicate diagnostic or original worker lost: stderr=%q roster=%q", stderr, rosterBytes(t, f))
				}
			}
			t.Logf("exit=%d stdout=%s stderr=%s", code, out, stderr)
		})
	}
}

func assertGroupMismatch(t *testing.T, code int, stdout, stderr, detail string, pairs ...string) {
	t.Helper()
	if code != 13 || !strings.Contains(stdout, `"status":"kind-mismatch"`) || !strings.Contains(stderr, detail) || !strings.Contains(stderr, "raise lane.build.panes") {
		t.Fatalf("exit=%d stdout=%q stderr=%q want full-lane mismatch %q", code, stdout, stderr, detail)
	}
	for _, pair := range pairs {
		if !spawnJSONHasField(t, stdout, pair) {
			t.Fatalf("stdout=%q missing %s", stdout, pair)
		}
	}
}

func assertFreshForMismatch(t *testing.T, stdout, stderr, worker, detail string) {
	t.Helper()
	prefix := fmt.Sprintf("soho: spawn: idle worker '%s' not reused: ", worker)
	if !spawnJSONHasField(t, stdout, `"created_pane": true`) || strings.Count(stderr, prefix) != 1 || strings.Contains(stderr, "raise lane.build.panes") {
		t.Fatalf("stdout=%q stderr=%q want a fresh worker and one mismatch line", stdout, stderr)
	}
	for _, line := range strings.Split(stderr, "\n") {
		if strings.HasPrefix(line, prefix) && strings.Contains(line, detail) && strings.HasSuffix(line, "; release it to free its slot") {
			return
		}
	}
	t.Fatalf("stderr=%q missing mismatch detail %q", stderr, detail)
}

func TestGroupReuseListedModelMismatchDiagnostics(t *testing.T) {
	t.Run("second listed model opened before effort change", func(t *testing.T) {
		f := laneModelFixtureWithCapacity(t, "2", "m1,m2", "1", idleHerdrRule("scouter"), idleHerdrRule("other-a"), idleHerdrRule("other-b"))
		appendRosterRow(t, f, "other-a", "m1", "other")
		appendRosterRow(t, f, "other-b", "m1", "other")

		code, out, stderr, refusal, calls := runLaneModelSpawn(t, f, []string{"scouter", "--mcp", "none", "--effort", "low"})
		t.Logf("initial spawn exit=%d stdout=%s", code, out)
		if code != 0 || !spawnJSONHasField(t, out, `"name": "scouter"`) ||
			!spawnJSONHasField(t, out, `"created_pane": true`) || !spawnJSONHasField(t, out, `"model": "m2"`) ||
			!spawnJSONHasField(t, out, `"effort": "low"`) || !spawnJSONHasField(t, out, `"model_choice": "list"`) {
			t.Fatalf("first spawn must open m2 at low effort: exit=%d stdout=%q stderr=%q refusal=%q", code, out, stderr, refusal)
		}
		record := strings.Split(core.RosterLine(filepath.Join(f.state, "ws"), "scouter"), "\t")
		if fieldAt(record, 8) != "m2" || fieldAt(record, 14) != "low" {
			t.Fatalf("opening model or effort missing from roster: %q", record)
		}
		before := rosterBytes(t, f)
		firstActions := len(herdrActs(calls))
		if firstActions == 0 {
			t.Fatal("fixture did not start a worker")
		}

		code, out, stderr, refusal, calls = runLaneModelSpawn(t, f, []string{"scouter", "--mcp", "none", "--effort", "high"})
		t.Logf("effort change exit=%d stdout=%s stderr=%s refusal=%s", code, out, stderr, refusal)
		assertGroupMismatch(t, code, out, stderr, "effort differs (low vs high)", `"session_effort": "low"`, `"requested_effort": "high"`)
		if strings.Contains(stderr, "model differs") || strings.Contains(out, `"session_model"`) || strings.Contains(out, `"requested_model"`) {
			t.Fatalf("allowed model m2 was reported as a mismatch: stdout=%q stderr=%q", out, stderr)
		}
		if len(herdrActs(calls)) != firstActions || rosterBytes(t, f) != before {
			t.Fatalf("refusal changed the roster or started another worker: actions=%v roster=%q", herdrActs(calls), rosterBytes(t, f))
		}
	})

	t.Run("unlisted model differs from requested model", func(t *testing.T) {
		f := laneModelFixtureWithCapacity(t, "2", "m1,m2", "1", idleHerdrRule("existing"))
		appendLaneIdleRowWith(t, f, "existing", "m3", f.cwd, "high", "auto", "none", "")
		before := rosterBytes(t, f)
		code, out, stderr, refusal, calls := runLaneModelSpawn(t, f, []string{"scouter", "--mcp", "none", "--effort", "high"})
		t.Logf("unlisted model exit=%d stdout=%s stderr=%s refusal=%s", code, out, stderr, refusal)
		assertGroupMismatch(t, code, out, stderr, "model differs (m3 vs m1)", `"session_model": "m3"`, `"requested_model": "m1"`)
		if len(herdrActs(calls)) != 0 || rosterBytes(t, f) != before {
			t.Fatalf("refusal changed the roster or started a worker: actions=%v roster=%q", herdrActs(calls), rosterBytes(t, f))
		}
	})
}
