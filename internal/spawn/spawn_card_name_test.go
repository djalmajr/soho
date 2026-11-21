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

// A worker actually named by a configured lane is a default name, not an
// explicit one: it must never draw the naming advisory.
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
	if !spawnJSONHasField(t, stdout, `"name": "build"`) {
		t.Fatalf("spawn JSON=%q", stdout)
	}
	if strings.Contains(stderr, "pass --card <KEY>") || strings.Contains(stderr, "is not <subject>-<function>") || strings.Contains(stderr, "reserved prefix") {
		t.Fatalf("lane default drew the naming advisory: %q", stderr)
	}
	if names := spawnRosterNames(t, f); len(names) != 1 || names[0] != "build" {
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
