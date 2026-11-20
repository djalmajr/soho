package cli

import (
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/jsonjs"
)

// spawnJSONHasFieldCLI parses the first JSON line (the spawn result) before
// checking a field slice. reopen and run put more JSON after it, and the
// blocked paths put a screen after it, so a whole-stdout substring check no
// longer describes the spawn object.
func spawnJSONHasFieldCLI(t *testing.T, raw, field string) bool {
	t.Helper()
	line, _, _ := strings.Cut(raw, "\n")
	if line == "" {
		return false
	}
	value, err := jsonjs.Parse([]byte(line))
	if err != nil {
		return false
	}
	obj, ok := value.(*jsonjs.Object)
	if !ok {
		return false
	}
	return strings.Contains(jsonjs.StringifyIndent(obj, 2), field)
}

func TestSpawnCLIOutputIsOneLine(t *testing.T) {
	env, cwd := commandFixture(t)
	code, out, errOut := runIn(t, []string{"spawn", "--help"}, env, cwd)
	if code != 0 || errOut != "" || !strings.Contains(out, "prints its result as one JSON line on stdout") {
		t.Fatalf("spawn usage: code=%d out=%q err=%q", code, out, errOut)
	}
	if lines := strings.Count(strings.TrimSuffix(out, "\n"), "\n") + 1; lines < 6 {
		t.Fatalf("spawn help has %d lines, want its full block: %q", lines, out)
	}
}
