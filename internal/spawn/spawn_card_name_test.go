package spawn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

func cardSpawnFixture(t *testing.T, liveName string) spawnFixture {
	t.Helper()
	rules := freshSpawnRules()
	if liveName != "" {
		rules[0].Stdout = `{"result":{"agents":[{"name":"` + liveName + `"}]}}`
	}
	f := newSpawnFixture(t, rules)
	configureSpawnFixture(t, &f)
	return f
}

func spawnRosterNames(t *testing.T, f spawnFixture) []string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(f.state, "ws", "agents.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		names = append(names, strings.SplitN(line, "\t", 2)[0])
	}
	return names
}

func TestSpawnCardRecordsDerivedName(t *testing.T) {
	f := cardSpawnFixture(t, "")
	code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"implementer", "--card", "SOH-427"})
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !spawnJSONHasField(t, stdout, `"name": "soh427-impl"`) {
		t.Fatalf("spawn JSON=%q", stdout)
	}
	if names := spawnRosterNames(t, f); len(names) != 1 || names[0] != "soh427-impl" {
		t.Fatalf("roster=%q", names)
	}
	if strings.Contains(stderr, "is not <subject>-<function>") || strings.Contains(stderr, "reserved prefix") {
		t.Fatalf("derived name drew the advisory: %q", stderr)
	}
}

func TestSpawnPresetNamesWorkerByRole(t *testing.T) {
	f := cardSpawnFixture(t, "implementer")
	f.env["SOHO_LANES"] = "on"
	code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"implementer", "--kind", "grok"})
	if code != 0 || !spawnJSONHasField(t, stdout, `"name": "implementer-2"`) {
		t.Fatalf("exit=%d stdout=%q stderr=%q, want implementer-2", code, stdout, stderr)
	}
	if names := spawnRosterNames(t, f); len(names) != 1 || names[0] != "implementer-2" {
		t.Fatalf("roster names=%v", names)
	}
}

func TestSpawnPresetCountsLegacyBuilderSlot(t *testing.T) {
	for _, tc := range []struct{ stored, group, role, panes string }{
		{"build", "builders", "implementer", "2"},
		{"critical", "builders", "implementer", "2"},
		{"review", "reviewers", "reviewer", "3"},
	} {
		t.Run(tc.stored, func(t *testing.T) {
			f := cardSpawnFixture(t, "")
			f.env["SOHO_LANES"], f.env["SOHO_PANES"] = "on", tc.panes
			f.roster(t, strings.Join([]string{"oldworker", "p-old", "grok", tc.role, "", "1", f.cwd, "now", "", "ask", tc.role, tc.stored}, "\t"))
			before := rosterBytes(t, f)
			code, stdout, stderr, calls := runCmdSpawn(t, f, []string{tc.role, "--kind", "grok"})
			if code != 10 || !strings.Contains(stdout, `"lane":"`+tc.group+`"`) || rosterBytes(t, f) != before || len(herdrActs(calls)) != 0 {
				t.Fatalf("exit=%d stdout=%q stderr=%q roster=%q actions=%v, want a busy %s slot", code, stdout, stderr, rosterBytes(t, f), herdrActs(calls), tc.group)
			}
			t.Logf("exit=%d stdout=%s", code, stdout)
		})
	}
}

func TestSpawnPresetPlannerAndReviewerAdmission(t *testing.T) {
	f := cardSpawnFixture(t, "")
	f.env["SOHO_LANES"], f.env["SOHO_PANES"] = "on", "2"
	for _, tc := range []struct {
		role, message string
		exit          int
	}{
		{"reviewer", "with panes=2 the orchestrator reviews", 3},
		{"planner", "the orchestrator is the planner and does not open a pane", 12},
	} {
		code, message, _ := runCmdSpawnExpectError(t, f, []string{tc.role})
		if code != tc.exit || !strings.Contains(message, tc.message) {
			t.Fatalf("%s exit=%d message=%q, want %d / %q", tc.role, code, message, tc.exit, tc.message)
		}
		t.Logf("%s exit=%d message=%s", tc.role, code, message)
	}
	code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"sub-orchestrator", "--kind", "grok"})
	if code != 0 || !spawnJSONHasField(t, stdout, `"name": "sub-orchestrator"`) || !strings.Contains(rosterBytes(t, f), "\tsub-orchestrator\tplanners\t") {
		t.Fatalf("first planner exit=%d stdout=%q stderr=%q roster=%q", code, stdout, stderr, rosterBytes(t, f))
	}
	code, stdout, stderr, _ = runCmdSpawn(t, f, []string{"implementer", "--kind", "grok"})
	if code != 0 || !strings.Contains(rosterBytes(t, f), "\timplementer\tbuilders\t") {
		t.Fatalf("builder exit=%d stdout=%q stderr=%q roster=%q", code, stdout, stderr, rosterBytes(t, f))
	}
	code, stdout, stderr, _ = runCmdSpawn(t, f, []string{"sub-orchestrator", "--kind", "grok"})
	if code != 10 || !strings.Contains(stdout, `"lane":"planners"`) {
		t.Fatalf("second planner exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	t.Logf("second planner exit=%d stdout=%s", code, stdout)
}

func TestSpawnWarnsOnlyForAffectedPresetGroupSettings(t *testing.T) {
	f := cardSpawnFixture(t, "")
	f.env["SOHO_LANES"] = "on"
	root := filepath.Join(f.cwd, ".agents")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "soho.conf"), []byte("panes=4\nlane.build.kind=pi\nlane.review.kind=claude\nlane.critical.args=\x1b[0m\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := core.LoadConfig(f.env, f.cwd)
	f.ctx = &ctx
	code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"implementer", "--kind", "grok"})
	if code != 0 || strings.Count(stderr, "lane.build.kind no longer applies. 'soho doctor --fix' moves it to lane.builders.kind.") != 1 ||
		strings.Contains(stderr, "lane.review.kind") || strings.Contains(stderr, "lane.critical.args") || strings.Contains(stderr, "\x1b") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestSpawnCardTakenNameGetsSuffix(t *testing.T) {
	f := cardSpawnFixture(t, "soh427-impl")
	code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"implementer", "--card", "SOH-427"})
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !spawnJSONHasField(t, stdout, `"name": "soh427-impl-2"`) {
		t.Fatalf("spawn JSON=%q", stdout)
	}
	if names := spawnRosterNames(t, f); len(names) != 1 || names[0] != "soh427-impl-2" {
		t.Fatalf("roster=%q", names)
	}
	if !strings.Contains(stderr, "agent name 'soh427-impl' is taken by another pane or workspace; using 'soh427-impl-2'") {
		t.Fatalf("stderr=%q", stderr)
	}
	if strings.Contains(stderr, "is not <subject>-<function>") {
		t.Fatalf("derived name drew the advisory: %q", stderr)
	}
}

func TestSpawnCardRejectsNameAndBadKey(t *testing.T) {
	f := cardSpawnFixture(t, "")
	code, message, calls := runCmdSpawnExpectError(t, f, []string{"implementer", "--card", "SOH-427", "--name", "soh427-impl"})
	if code != 2 || message != "spawn: pass --card or --name, not both" {
		t.Fatalf("exit=%d message=%q calls=%#v", code, message, calls)
	}
	if len(calls) != 0 {
		t.Fatalf("conflicting flags called Herdr: %#v", calls)
	}
	f = cardSpawnFixture(t, "")
	code, message, calls = runCmdSpawnExpectError(t, f, []string{"implementer", "--card", "427-SOH"})
	if code != 2 || message != "spawn: --card '427-SOH' gives no usable name; expected a card key such as SOH-427" {
		t.Fatalf("exit=%d message=%q calls=%#v", code, message, calls)
	}
	if len(calls) != 0 {
		t.Fatalf("bad card key called Herdr: %#v", calls)
	}
}

func TestSpawnNameAdvisoryOnlyForExplicitName(t *testing.T) {
	const advisory = "soho: warning: spawn: agent name 'build' is not <subject>-<function>[-<n>] (for example soh427-impl); pass --card <KEY> to derive it"
	t.Run("explicit name warns", func(t *testing.T) {
		f := cardSpawnFixture(t, "")
		code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"implementer", "--name", "build"})
		if code != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		if !spawnJSONHasField(t, stdout, `"name": "build"`) || strings.Count(stderr, advisory) != 1 {
			t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
		}
	})
	t.Run("default name stays silent", func(t *testing.T) {
		f := cardSpawnFixture(t, "")
		code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"implementer"})
		if code != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		if !spawnJSONHasField(t, stdout, `"name": "implementer"`) || strings.Contains(stderr, "is not <subject>-<function>") {
			t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
		}
	})
	t.Run("reserved prefix warns", func(t *testing.T) {
		f := cardSpawnFixture(t, "")
		code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"implementer", "--name", "wake-x"})
		if code != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		want := "soho: warning: spawn: agent name 'wake-x' uses the reserved prefix 'wake-' (soho's own wake and helper terminals); name workers <subject>-<function>, for example soh427-impl, or pass --card <KEY>"
		if !spawnJSONHasField(t, stdout, `"name": "wake-x"`) || !strings.Contains(stderr, want) {
			t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
		}
	})
}

// --card is tracked by presence: an explicitly empty value is still a card,
// and the conflict with --name is decided by both flags being present.
func TestSpawnCardFlagPresence(t *testing.T) {
	const bad = "spawn: --card '%s' gives no usable name; expected a card key such as SOH-427"
	const conflict = "spawn: pass --card or --name, not both"
	for _, tc := range []struct {
		label string
		args  []string
		want  string
	}{
		{"missing operand", []string{"--card"}, "spawn: --card expects a value"},
		{"empty card", []string{"--card", ""}, strings.Replace(bad, "%s", "", 1)},
		{"whitespace card", []string{"--card", "  "}, strings.Replace(bad, "%s", "  ", 1)},
		{"punctuation card", []string{"--card", "!!!"}, strings.Replace(bad, "%s", "!!!", 1)},
		{"digit-leading card", []string{"--card", "427-SOH"}, strings.Replace(bad, "%s", "427-SOH", 1)},
		{"overlong derived name", []string{"--card", strings.Repeat("A", 28)}, "invalid agent name '" + strings.Repeat("a", 28) + "-impl' (must match [a-z][a-z0-9_-]{0,31})"},
		{"conflict", []string{"--card", "SOH-427", "--name", "build"}, conflict},
		{"empty name", []string{"--name", "", "--card", "SOH-427"}, conflict},
		{"bad card with name", []string{"--card", "!!!", "--name", "build"}, conflict},
		{"empty card with name", []string{"--card", "", "--name", "build"}, conflict},
		{"name then empty card", []string{"--name", "build", "--card", ""}, conflict},
		{"both empty", []string{"--name", "", "--card", ""}, conflict},
		{"repeated card", []string{"--card", "SOH-427", "--card", "KAS-309"}, "spawn: --card given more than once"},
		{"repeated empty card", []string{"--card", "", "--card", ""}, "spawn: --card given more than once"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			f := cardSpawnFixture(t, "")
			code, message, calls := runCmdSpawnExpectError(t, f, append([]string{"implementer"}, tc.args...))
			if code != 2 || message != tc.want || len(calls) != 0 {
				t.Fatalf("exit=%d message=%q transport calls=%d; want exit=2 message=%q calls=0", code, message, len(calls), tc.want)
			}
		})
	}
}

// --name keeps its own presence marker: an explicitly empty name is warned
// about and still resolves to the lane/role default, with no refusal.
func TestSpawnEmptyExplicitNameKeepsNamePresence(t *testing.T) {
	f := cardSpawnFixture(t, "")
	code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"implementer", "--name", ""})
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !spawnJSONHasField(t, stdout, `"name": "implementer"`) || !strings.Contains(stderr, "pass --card <KEY>") {
		t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
	}
}

// A role-based name in a custom group is automatic and draws no naming advisory.
func TestSpawnLaneDefaultNameStaysSilent(t *testing.T) {
	f := cardSpawnFixture(t, "")
	f.env["SOHO_LANES"] = "on"
	f.ctx.Entries["lane_build_roles"] = core.ConfigEntry{Value: "implementer", Source: "project"}
	f.ctx.Entries["lane_build_kind"] = core.ConfigEntry{Value: "grok", Source: "project"}
	f.ctx.Entries["lane_build_panes"] = core.ConfigEntry{Value: "1", Source: "project"}
	f.ctx.Order = []string{"lane_build_roles", "lane_build_kind", "lane_build_panes"}
	code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"implementer"})
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !spawnJSONHasField(t, stdout, `"name": "implementer"`) {
		t.Fatalf("spawn JSON=%q", stdout)
	}
	if strings.Contains(stderr, "pass --card <KEY>") || strings.Contains(stderr, "is not <subject>-<function>") || strings.Contains(stderr, "reserved prefix") {
		t.Fatalf("role default drew the naming advisory: %q", stderr)
	}
	if names := spawnRosterNames(t, f); len(names) != 1 || names[0] != "implementer" {
		t.Fatalf("roster=%q", names)
	}
}

func TestSpawnCardAdmissionOnlyStaysSilent(t *testing.T) {
	f := cardSpawnFixture(t, "")
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, errOut strings.Builder
	platform.Stdout, platform.Stderr = &out, &errOut
	t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
	PreflightFreshSpawn([]string{"implementer", "--name", "build"}, f.ctx, f.env, f.cwd)
	if errOut.Len() != 0 || out.Len() != 0 {
		t.Fatalf("admission-only out=%q stderr=%q", out.String(), errOut.String())
	}
}

func TestSpawnReportsActivePredecessorGroupSetting(t *testing.T) {
	f := cardSpawnFixture(t, "")
	f.env["SOHO_LANES"] = "on"
	root := filepath.Join(f.cwd, ".agents")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	input := "panes=4\nlane.build.kind=grok\n" +
		"lane.build.roles=implementer,designer,tasker,scouter,researcher,documenter,tester\n" +
		"lane.critical.roles=critical-implementer\n" +
		"lane.review.roles=reviewer,security-reviewer,ui-reviewer,inspector,validator\n"
	if err := os.WriteFile(filepath.Join(root, "soho.conf"), []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := core.LoadConfig(f.env, f.cwd)
	f.ctx = &ctx
	code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"implementer"})
	if code != 0 || !strings.Contains(stderr, "lane.build.kind still applies") ||
		!strings.Contains(stderr, "'soho doctor --fix' moves it to lane.builders.kind.") || !spawnJSONHasField(t, stdout, `"kind": "grok"`) {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}
