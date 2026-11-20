package spawn

import (
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
)

// SOH-423: every print site of the spawn result emits one compact JSON line,
// so `soho spawn … | tail -1` carries the whole object. The blocked reuse
// print is followed by the visible screen; only its first line is the JSON.

func parseOneSpawnJSONLine(t *testing.T, stdout string) *jsonjs.Object {
	t.Helper()
	if !strings.HasSuffix(stdout, "\n") {
		t.Fatalf("spawn stdout has no trailing newline: %q", stdout)
	}
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("spawn stdout has %d lines, want 1: %q", len(lines), stdout)
	}
	return parseSpawnJSONLineText(t, lines[0])
}

func parseSpawnJSONLineText(t *testing.T, line string) *jsonjs.Object {
	t.Helper()
	value, err := jsonjs.Parse([]byte(line))
	if err != nil {
		t.Fatalf("spawn JSON line does not parse: %v; %q", err, line)
	}
	obj, ok := value.(*jsonjs.Object)
	if !ok {
		t.Fatalf("spawn JSON line is not an object: %q", line)
	}
	if got := jsonjs.Stringify(obj); got != line {
		t.Fatalf("spawn JSON line is not compact: %q, want %q", line, got)
	}
	return obj
}

// spawnJSONHasField parses the JSON line before checking a field slice.
// Older assertions spelled fields with indentation; they now go through
// this helper so the original stdout layout cannot decide their outcome.
func spawnJSONHasField(t *testing.T, raw, field string) bool {
	t.Helper()
	line, _, _ := strings.Cut(raw, "\n")
	value, err := jsonjs.Parse([]byte(line))
	if err != nil || line == "" {
		return false
	}
	obj, ok := value.(*jsonjs.Object)
	if !ok {
		return false
	}
	return strings.Contains(jsonjs.StringifyIndent(obj, 2), field)
}

func spawnOneLineField(t *testing.T, obj *jsonjs.Object, key string, want any) {
	t.Helper()
	got, present := obj.Get(key)
	if !present || got != want {
		t.Fatalf("spawn JSON %s=%#v (present=%t), want %#v; %s", key, got, present, want, jsonjs.Stringify(obj))
	}
}

func TestSpawnReuseJSONIsOneLine(t *testing.T) {
	f := newSpawnFixture(t, nil)
	f.roster(t, spawnRosterRow("worker", "scouter", "grok-4.7", "full", "--old-arg"))
	oldOut := platform.Stdout
	var output strings.Builder
	platform.Stdout = &output
	t.Cleanup(func() { platform.Stdout = oldOut })
	if !EmitReuse("worker", "implementer", "grok", f.ctx, f.env, f.cwd) {
		t.Fatal("EmitReuse returned false")
	}
	obj := parseOneSpawnJSONLine(t, output.String())
	want := `{"name":"worker","pane_id":"p-worker","kind":"grok","role":"implementer","family":"xai","reused":true,"previous_role":"scouter","effort":"high","model":"grok-4.7","model_choice":"default","agent_args":"--old-arg","status":"ready"}`
	if got := jsonjs.Stringify(obj); got != want {
		t.Fatalf("reuse JSON=%s, want %s", got, want)
	}
}

func TestSpawnBlockedReuseJSONIsOneLine(t *testing.T) {
	setStartupPlatform(t, "win32")
	f, _ := cursorReuseFixture(t, cursorNativeTrustScreen(t), false)
	code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"implementer", "--kind", "cursor"})
	if code != 7 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	line, screen, ok := strings.Cut(stdout, "\n")
	if !ok {
		t.Fatalf("blocked reuse printed no screen after the JSON: %q", stdout)
	}
	obj := parseSpawnJSONLineText(t, line)
	spawnOneLineField(t, obj, "name", "implementer")
	spawnOneLineField(t, obj, "pane_id", "p-implementer")
	spawnOneLineField(t, obj, "kind", "cursor")
	spawnOneLineField(t, obj, "reused", true)
	spawnOneLineField(t, obj, "status", "blocked_at_startup")
	if !strings.Contains(screen, "Do you trust the contents of this directory?") {
		t.Fatalf("the screen after the JSON line is missing: %q", screen)
	}
}

func TestSpawnMainJSONIsOneLine(t *testing.T) {
	f := newSpawnFixture(t, freshSpawnRules())
	configureSpawnFixture(t, &f)
	code, stdout, stderr, _ := runCmdSpawn(t, f, []string{"worker"})
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	obj := parseOneSpawnJSONLine(t, stdout)
	spawnOneLineField(t, obj, "name", "worker")
	spawnOneLineField(t, obj, "kind", "grok")
	spawnOneLineField(t, obj, "role", "worker")
	spawnOneLineField(t, obj, "status", "ready")
	spawnOneLineField(t, obj, "created_pane", true)
}
