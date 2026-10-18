package doctor

import (
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
)

const backgroundPatternsInvalidWarn = "warn   config: background_patterns.claude: invalid regular expression (ignored): "

func TestDoctorWarnsAnInvalidBackgroundPattern(t *testing.T) {
	env, cwd, ctx := contextWindowDoctorFixture(t)
	ctx.Entries["background_patterns_claude"] = core.ConfigEntry{Value: `(shell`, Source: "project"}
	out := doctorContextWindowOut(t, env, cwd, ctx)
	if !strings.Contains(out, backgroundPatternsInvalidWarn+"error parsing regexp: missing closing ): `(shell`") {
		t.Fatalf("doctor output missing the invalid expression warning:\n%s", out)
	}
}

func TestDoctorWarnsAnInvalidBackgroundPatternFromTheEnvironment(t *testing.T) {
	env, cwd, ctx := contextWindowDoctorFixture(t)
	env["SOHO_BACKGROUND_PATTERNS_CODEX"] = `[`
	out := doctorContextWindowOut(t, env, cwd, ctx)
	if !strings.Contains(out, "warn   config: background_patterns.codex: invalid regular expression (ignored): ") {
		t.Fatalf("doctor output missing the warning for the environment value:\n%s", out)
	}
}

func TestDoctorStaysQuietForAValidOrEmptyBackgroundPattern(t *testing.T) {
	env, cwd, ctx := contextWindowDoctorFixture(t)
	ctx.Entries["background_patterns_claude"] = core.ConfigEntry{Value: `\d+ shells? still running|· \d+ monitors?( ·|$)`, Source: "defaults"}
	ctx.Entries["background_patterns_codex"] = core.ConfigEntry{Value: "", Source: "project"}
	out := doctorContextWindowOut(t, env, cwd, ctx)
	if strings.Contains(out, "background_patterns") {
		t.Fatalf("doctor warned about a valid or empty expression:\n%s", out)
	}
}
