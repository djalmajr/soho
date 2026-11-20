package spawn

import (
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
)

// F3: every spawn JSON path (created pane, --pane, reuse) carries model,
// effort and agent_args; a roster value that is missing comes out as "".

func spawnJSONString(t *testing.T, raw, key string) string {
	t.Helper()
	v, err := jsonjs.Parse([]byte(raw))
	if err != nil {
		t.Fatalf("spawn JSON is not parseable: %v; %q", err, raw)
	}
	obj, ok := v.(*jsonjs.Object)
	if !ok {
		t.Fatalf("spawn JSON is not an object: %q", raw)
	}
	value, present := obj.Get(key)
	if !present {
		t.Fatalf("spawn JSON has no %q field: %s", key, raw)
	}
	s, ok := value.(string)
	if !ok {
		t.Fatalf("spawn JSON %q is not a string: %#v", key, value)
	}
	return s
}

func TestSpawnJSONCarriesModelEffortAgentArgs(t *testing.T) {
	t.Run("reuse carries the roster model, effort and agent_args", func(t *testing.T) {
		f := newSpawnFixture(t, nil)
		f.roster(t, spawnRosterRow("worker", "scouter", "grok-4.7", "full", "--old-arg"))
		oldOut := platform.Stdout
		var output strings.Builder
		platform.Stdout = &output
		t.Cleanup(func() { platform.Stdout = oldOut })
		if !EmitReuse("worker", "implementer", "grok", f.ctx, f.env, f.cwd) {
			t.Fatal("EmitReuse returned false")
		}
		t.Logf("reuse JSON:\n%s", output.String())
		if got := spawnJSONString(t, output.String(), "model"); got != "grok-4.7" {
			t.Errorf("model=%q, want grok-4.7", got)
		}
		if got := spawnJSONString(t, output.String(), "effort"); got != "high" {
			t.Errorf("effort=%q, want high", got)
		}
		if got := spawnJSONString(t, output.String(), "agent_args"); got != "--old-arg" {
			t.Errorf("agent_args=%q, want --old-arg", got)
		}
	})

	t.Run("reuse of an old 8-column roster line carries the fields as empty strings", func(t *testing.T) {
		f := newSpawnFixture(t, nil)
		f.roster(t, "worker\tp-worker\tgrok\timplementer\txai\t1\t/tmp/work\tnow")
		oldOut := platform.Stdout
		var output strings.Builder
		platform.Stdout = &output
		t.Cleanup(func() { platform.Stdout = oldOut })
		if !EmitReuse("worker", "implementer", "grok", f.ctx, f.env, f.cwd) {
			t.Fatal("EmitReuse returned false")
		}
		t.Logf("reuse JSON (8-column line):\n%s", output.String())
		for _, key := range []string{"model", "effort", "agent_args"} {
			if got := spawnJSONString(t, output.String(), key); got != "" {
				t.Errorf("%s=%q, want empty", key, got)
			}
		}
	})

	t.Run("a created pane carries the resolved model, effort and the agent args", func(t *testing.T) {
		f := newSpawnFixture(t, freshSpawnRules())
		configureSpawnFixture(t, &f)
		code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"worker"})
		if code != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		if !spawnJSONHasField(t, stdout, `"created_pane": true`) {
			t.Fatalf("not a created pane: %s", stdout)
		}
		t.Logf("created-pane JSON:\n%s", stdout)
		if got := spawnJSONString(t, stdout, "model"); got != "grok-4.7" {
			t.Errorf("model=%q, want grok-4.7", got)
		}
		if got := spawnJSONString(t, stdout, "effort"); got != "high" {
			t.Errorf("effort=%q, want high", got)
		}
		if got := spawnJSONString(t, stdout, "agent_args"); got != "--model grok-4.7 --reasoning-effort high" {
			t.Errorf("agent_args=%q, want --model grok-4.7 --reasoning-effort high", got)
		}
	})

	t.Run("--pane carries the resolved model, effort and the agent args", func(t *testing.T) {
		f := newSpawnFixture(t, freshSpawnRules())
		configureSpawnFixture(t, &f)
		code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"worker", "--pane", "w0test:p0a", "--name", "worker"})
		if code != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		if !spawnJSONHasField(t, stdout, `"placement": "given"`) {
			t.Fatalf("not the --pane path: %s", stdout)
		}
		t.Logf("--pane JSON:\n%s", stdout)
		if got := spawnJSONString(t, stdout, "model"); got != "grok-4.7" {
			t.Errorf("model=%q, want grok-4.7", got)
		}
		if got := spawnJSONString(t, stdout, "effort"); got != "high" {
			t.Errorf("effort=%q, want high", got)
		}
		if got := spawnJSONString(t, stdout, "agent_args"); got != "--model grok-4.7 --reasoning-effort high" {
			t.Errorf("agent_args=%q, want --model grok-4.7 --reasoning-effort high", got)
		}
	})
}
