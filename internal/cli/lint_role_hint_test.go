package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
)

func TestLintOwnedFilesRoleHint(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	for _, dir := range []string{"home", "config", "state"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// A relative path with spaces proves the hint echoes the typed argument.
	typedBrief := "brief with spaces.md"
	body := "# Goal\nDo the slice.\n# Expected result\nDone.\n# Forbidden\nNo commit/push.\n# Report\nDone.\n"
	if err := os.WriteFile(filepath.Join(root, typedBrief), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{
		"PATH":            testutil.HostPath(),
		"HOME":            filepath.Join(root, "home"),
		"USERPROFILE":     filepath.Join(root, "home"),
		"XDG_CONFIG_HOME": filepath.Join(root, "config"),
		"TMPDIR":          root,
		"SOHO_DIR":        filepath.Join(root, "state"),
		"SOHO_SKILL_DIR":  testSkillDir(t),
		"SOHO_BRIEF_LINT": "warn",
	}
	run := func(runEnv platform.Env, args ...string) (int, string, string) {
		var stdout, stderr bytes.Buffer
		oldOut, oldErr := platform.Stdout, platform.Stderr
		platform.Stdout, platform.Stderr = &stdout, &stderr
		defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
		return Run(append([]string{"lint"}, args...), runEnv), stdout.String(), stderr.String()
	}
	const hint = "; a read-only role needs none: soho lint brief with spaces.md --role <role>"

	// (a) no --role: the warning ends with the hint and echoes the typed path.
	code, out, errText := run(env, typedBrief)
	if code != 1 || out != "" || !strings.Contains(errText, "is missing sections: [Owned files]") || !strings.HasSuffix(strings.TrimSuffix(errText, "\n"), hint) {
		t.Fatalf("no --role: code=%d stdout=%q stderr=%q", code, out, errText)
	}

	// strict keeps its own refusal line, with no hint.
	strict := env.Clone()
	strict["SOHO_BRIEF_LINT"] = "strict"
	code, out, errText = run(strict, typedBrief)
	if code != 2 || out != "" || !strings.Contains(errText, "is missing sections: [Owned files]") || strings.Contains(errText, hint) || !strings.Contains(errText, "(brief_lint=strict)") {
		t.Fatalf("strict: code=%d stdout=%q stderr=%q", code, out, errText)
	}

	// (b) a read-only role needs no Owned files: no missing-sections warning.
	code, out, errText = run(env, typedBrief, "--role", "reviewer")
	if code != 0 || out != "brief "+typedBrief+": ok\n" || strings.Contains(errText, "missing sections") || strings.Contains(errText, hint) {
		t.Fatalf("reviewer: code=%d stdout=%q stderr=%q", code, out, errText)
	}

	// The decision comes from the section check, not from the message text:
	// an alias that contains the list's own wording neither hides nor adds it.
	tricky := env.Clone()
	tricky["SOHO_BRIEF_LINT_ALIASES"] = "Goal=Why this is missing sections: [Owned files] background"
	noGoal := "no goal.md"
	if err := os.WriteFile(filepath.Join(root, noGoal), []byte("# Expected result\nDone.\n# Forbidden\nNo commit/push.\n# Report\nDone.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errText = run(tricky, noGoal)
	if code != 1 || out != "" || !strings.Contains(errText, "[Goal] [Owned files]") || !strings.HasSuffix(strings.TrimSuffix(errText, "\n"), "; a read-only role needs none: soho lint no goal.md --role <role>") {
		t.Fatalf("alias with the list wording, Owned files missing: code=%d stdout=%q stderr=%q", code, out, errText)
	}
	// An alias with the list's wording but no label: the hint must stay.
	plain := env.Clone()
	plain["SOHO_BRIEF_LINT_ALIASES"] = "Goal=Why this is missing sections: background"
	code, out, errText = run(plain, noGoal)
	if code != 1 || out != "" || !strings.Contains(errText, "[Goal] [Owned files]") || !strings.HasSuffix(strings.TrimSuffix(errText, "\n"), "; a read-only role needs none: soho lint no goal.md --role <role>") {
		t.Fatalf("delimiter-only alias, Owned files missing: code=%d stdout=%q stderr=%q", code, out, errText)
	}
	withOwned := "with owned.md"
	if err := os.WriteFile(filepath.Join(root, withOwned), []byte("# Expected result\nDone.\n# Owned files\n- a.go\n# Forbidden\nNo commit/push.\n# Report\nDone.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errText = run(tricky, withOwned)
	if code != 1 || out != "" || !strings.Contains(errText, "is missing sections: [Goal]") || strings.Contains(errText, "a read-only role needs none") {
		t.Fatalf("alias with the list wording, Owned files present: code=%d stdout=%q stderr=%q", code, out, errText)
	}

	// (c) an explicit edit role keeps the original warning, without the hint.
	code, out, errText = run(env, typedBrief, "--role", "implementer")
	if code != 1 || out != "" || !strings.Contains(errText, "is missing sections: [Owned files]") || strings.Contains(errText, hint) {
		t.Fatalf("implementer: code=%d stdout=%q stderr=%q", code, out, errText)
	}
}

func TestDispatchOwnedFilesWarningHasNoLintRoleHint(t *testing.T) {
	f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
	f.env["SOHO_BRIEF_LINT"] = "warn"
	body := "# Goal\nrun\n# Expected result\nok\n# Forbidden\nno commit or push\n# Report\ndone\n"
	if err := os.WriteFile(f.brief, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	roster := "worker\tw0test:p0a\tcodex\timplementer\topenai\t0\t" + f.root + "\tnow\tgpt-5\ttask\timplementer\n"
	if err := os.WriteFile(filepath.Join(f.state, "ws", "agents.tsv"), []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
	_, _, warn := f.run(t, "worker", f.brief, "--no-wait")

	// (d) dispatch always knows the role: its warning stays byte-for-byte the
	// same message the shared lint check produces for an explicit edit role.
	want := "soho: warning: brief " + f.brief + " is missing sections: [Owned files] — workers without owned files collide — accepted headings: Owned files: Owned files, Owned, Arquivos, Arquivos próprios, Arquivos de posse; add your own with brief_lint_aliases=<Section>=<Heading>|<Heading>"
	var got []string
	for _, line := range strings.Split(strings.TrimSuffix(warn, "\n"), "\n") {
		if strings.Contains(line, "is missing sections:") {
			got = append(got, line)
		}
	}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("dispatch warning lines=%q, want exactly %q (stderr=%q)", got, want, warn)
	}
}
