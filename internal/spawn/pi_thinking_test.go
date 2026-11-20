package spawn

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
	"github.com/djalmajr/soho/internal/transport"
)

type piThinkingRead struct {
	target transport.Target
	read   transport.Read
	at     time.Time
}

type fakePiThinkingTransport struct {
	transport.Transport
	screens []string
	reads   []piThinkingRead
	clock   time.Time
}

func (f *fakePiThinkingTransport) Read(_ context.Context, target transport.Target, read transport.Read, _ transport.Call) transport.Reply[string] {
	f.reads = append(f.reads, piThinkingRead{target: target, read: read, at: f.clock})
	screen := ""
	if len(f.screens) > 0 {
		screen, f.screens = f.screens[0], f.screens[1:]
	}
	return transport.Reply[string]{Value: screen, Valid: true}
}

func TestReadPiThinkingLevel(t *testing.T) {
	const name = "pi-worker"
	start := time.Date(2026, 11, 19, 20, 0, 0, 0, time.UTC)

	newFake := func(screens ...string) (*fakePiThinkingTransport, *[]time.Duration) {
		f := &fakePiThinkingTransport{screens: screens, clock: start}
		return f, &[]time.Duration{}
	}

	t.Run("one read when the footer is drawn", func(t *testing.T) {
		f, sleeps := newFake("model • high\n")
		if got := readPiThinkingLevel(f, name, func() time.Time { return f.clock }, func(time.Duration) {}); got != "high" {
			t.Fatalf("level=%q want high", got)
		}
		if len(f.reads) != 1 || len(*sleeps) != 0 {
			t.Fatalf("reads=%d sleeps=%v, want one read and no pause", len(f.reads), *sleeps)
		}
		r := f.reads[0]
		if r.target.ID != name || r.read.Source != "visible" || r.read.Pane || r.read.Lines != nil {
			t.Fatalf("read targeted %#v with %#v, want the worker's visible screen", r.target, r.read)
		}
	})

	t.Run("one more read inside the window when the footer is cut mid-draw", func(t *testing.T) {
		f, sleeps := newFake("model • hig\n", "SECRET-TOKEN-DO-NOT-ECHO • high\n")
		got := readPiThinkingLevel(f, name, func() time.Time { return f.clock }, func(d time.Duration) {
			*sleeps = append(*sleeps, d)
			f.clock = f.clock.Add(d)
		})
		if got != "high" {
			t.Fatalf("level=%q want high", got)
		}
		if len(f.reads) != 2 || len(*sleeps) != 1 {
			t.Fatalf("reads=%d sleeps=%v, want two reads and one pause", len(f.reads), *sleeps)
		}
		if (*sleeps)[0] > piThinkingReadWindow || f.reads[1].at.Sub(start) > piThinkingReadWindow {
			t.Fatalf("second read at %v, sleep %v: window %v", f.reads[1].at, (*sleeps)[0], piThinkingReadWindow)
		}
		if (*sleeps)[0] != piThinkingReadWindow {
			t.Fatalf("sleep=%v want the whole remaining window", (*sleeps)[0])
		}
	})

	t.Run("a level in scrolled history is not the footer", func(t *testing.T) {
		f, _ := newFake("old • high\nmodel • unavailable\n", "old • high\nmodel • unavailable\n")
		got := readPiThinkingLevel(f, name, func() time.Time { return f.clock }, func(d time.Duration) { f.clock = f.clock.Add(d) })
		if got != "" {
			t.Fatalf("level=%q want no reading", got)
		}
		if len(f.reads) != 2 {
			t.Fatalf("reads=%d want the one retry", len(f.reads))
		}
	})

	t.Run("a partial footer is recognized without mistaking other words", func(t *testing.T) {
		for _, tc := range []struct {
			screen string
			want   bool
		}{
			{"model • hig\n", true},
			{"model • thinking: medi\n", true},
			{"model • cutoff\n", false},
			{"model • unavailable\n", false},
			{"working\n", false},
		} {
			if got := piThinkingPartialFooter(tc.screen); got != tc.want {
				t.Fatalf("piThinkingPartialFooter(%q)=%v want %v", tc.screen, got, tc.want)
			}
		}
	})

	t.Run("no footer costs two reads and no more", func(t *testing.T) {
		f, sleeps := newFake("working\n", "working\n")
		got := readPiThinkingLevel(f, name, func() time.Time { return f.clock }, func(d time.Duration) {
			*sleeps = append(*sleeps, d)
			f.clock = f.clock.Add(d)
		})
		if got != "" || len(f.reads) != 2 || len(*sleeps) != 1 || f.reads[1].at.Sub(start) > piThinkingReadWindow {
			t.Fatalf("level=%q reads=%d sleeps=%v", got, len(f.reads), *sleeps)
		}
	})
}

func piVisibleReadArgv(name string) []string {
	return []string{"agent", "read", name, "--source", "visible"}
}

func piSpawnFixture(t *testing.T, rules ...fakecli.Rule) spawnFixture {
	t.Helper()
	f := newSpawnFixture(t, append(rules, freshSpawnRules()...))
	configureSpawnFixture(t, &f)
	f.env["SOHO_LAYOUT"] = "tab"
	return f
}

func spawnJSONField(t *testing.T, stdout, field string) (string, bool) {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &value); err != nil {
		t.Fatalf("spawn stdout is not JSON: %q: %v", stdout, err)
	}
	got, ok := value[field]
	if !ok {
		return "", false
	}
	text, ok := got.(string)
	if !ok {
		t.Fatalf("%s=%#v, want a string", field, got)
	}
	return text, true
}

const piThinkingWarn = "soho: warning: spawn: pi shows thinking 'high' for 'worker' where 'medium' was asked; this model may not offer 'medium'"

func TestSpawnPiEffectiveThinking(t *testing.T) {
	for _, tc := range []struct {
		name, screen, effort, wantLevel string
		wantWarn                        bool
	}{
		{"another level is reported", "model • high\n", "medium", "high", true},
		{"the asked level is reported", "model • high\n", "high", "high", false},
		{"no footer is silent", "working\n", "medium", "", false},
		{"history is not a footer", "old • high\nmodel • unavailable\n", "medium", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := piSpawnFixture(t, fakecli.Rule{Argv: piVisibleReadArgv("worker"), Stdout: tc.screen})
			code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"worker", "--kind", "pi", "--effort", tc.effort, "--fresh"})
			if code != 0 {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			level, present := spawnJSONField(t, stdout, "effort_effective")
			if present != (tc.wantLevel != "") || level != tc.wantLevel {
				t.Fatalf("effort_effective=%q present=%v, want %q present=%v; stdout=%s", level, present, tc.wantLevel, tc.wantLevel != "", stdout)
			}
			warned := strings.Contains(stderr, piThinkingWarn)
			if warned != tc.wantWarn {
				t.Fatalf("warning=%v want %v; stderr=%q", warned, tc.wantWarn, stderr)
			}
		})
	}
}

func TestSpawnMcpCannotLimitWarnsOncePerKey(t *testing.T) {
	t.Run("claude: same list in either order warns once, a different list warns again", func(t *testing.T) {
		f := mcpListFixture(t)
		prefix := "soho: warning: spawn: claude cannot limit its MCP servers to the requested list per process; '"
		spawns := []struct {
			name, list string
		}{{"w1", "alpha,beta"}, {"w2", "beta,alpha"}, {"w3", "alpha,gamma"}}
		total := 0
		for _, s := range spawns {
			code, _, stderr, _ := runCmdSpawn(t, f, []string{"worker", "--name", s.name, "--kind", "claude", "--effort", "high", "--mcp", s.list, "--fresh"})
			if code != 0 {
				t.Fatalf("exit=%d stderr=%q", code, stderr)
			}
			total += stdErrCount(stderr, prefix+s.name+"' keeps the user's MCP servers")
		}
		if total != 2 {
			t.Fatalf("want one warning for the first list and one for the second, got %d", total)
		}
	})

	t.Run("pi: the same list on two spawns warns once", func(t *testing.T) {
		f := mcpListFixture(t)
		if _, err := fakecli.Install(t, f.bin, "pi", []fakecli.Rule{
			{Argv: []string{"mcp", "list", "--json"}, Stdout: `{"servers":[{"name":"alpha"},{"name":"beta"}]}`},
			{AnyArgs: true},
		}); err != nil {
			t.Fatal(err)
		}
		prefix := "soho: warning: spawn: pi cannot limit its MCP servers to the requested list per process; '"
		total := 0
		for _, s := range []struct{ name, list string }{{"w1", "alpha,beta"}, {"w2", "beta,alpha"}} {
			code, _, stderr, _ := runCmdSpawn(t, f, []string{"worker", "--name", s.name, "--kind", "pi", "--effort", "high", "--mcp", s.list, "--fresh"})
			if code != 0 {
				t.Fatalf("exit=%d stderr=%q", code, stderr)
			}
			total += stdErrCount(stderr, prefix+s.name+"' keeps the user's MCP servers")
		}
		if total != 1 {
			t.Fatalf("want one warning for both spawns, got %d", total)
		}
	})
}

// TestSpawnPiThinkingWarnsOncePerModelAndEffort proves the mismatch warning is
// suppressed for another spawn of the same model and effort, but not for a
// different model. Each spawn gets its own visible screen rule and name so the
// literal warning identifies which spawn was allowed to warn.
func TestSpawnPiThinkingWarnsOncePerModelAndEffort(t *testing.T) {
	spawns := []struct{ name, model string }{{"w1", "model-a"}, {"w2", "model-a"}, {"w3", "model-b"}}
	rules := make([]fakecli.Rule, 0, len(spawns))
	for _, s := range spawns {
		rules = append(rules, fakecli.Rule{Argv: piVisibleReadArgv(s.name), Stdout: "model • high\n"})
	}
	f := piSpawnFixture(t, rules...)
	for i, s := range spawns {
		code, _, stderr, _ := runCmdSpawn(t, f, []string{"worker", "--name", s.name, "--kind", "pi", "--model", s.model, "--effort", "medium", "--fresh"})
		if code != 0 {
			t.Fatalf("%s: exit=%d stderr=%q", s.name, code, stderr)
		}
		want := "soho: warning: spawn: pi shows thinking 'high' for '" + s.name + "' where 'medium' was asked; this model may not offer 'medium'"
		if warned := strings.Contains(stderr, want); warned != (i == 0 || i == 2) {
			t.Fatalf("%s model=%s warning=%v; stderr=%q", s.name, s.model, warned, stderr)
		}
	}
}
