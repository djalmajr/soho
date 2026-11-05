package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

// DJA-685: the per-`Type:` section warnings are warnings only. Under
// brief_lint=strict the dispatch refuses only the missing-sections message,
// so a brief whose only finding is the new warning still dispatches.

func TestDispatchTypeSectionWarningsNeverRefuse(t *testing.T) {
	h := newTM3bHarness(t, tm3bFullBrief()+"Type: backend\n")
	h.env["SOHO_BRIEF_LINT"] = "strict"
	code, out, stderr := h.run(t, "build", h.brief, "--no-wait")
	if code != 0 {
		t.Fatalf("dispatch refused the type-section warnings: code=%d stderr=%s", code, stderr)
	}
	if status, ok := dispatchOutputField(t, out, "wait_status"); !ok || status != "submitted" {
		t.Fatalf("dispatch did not continue: status=%q out=%s", status, out)
	}
	for _, want := range []string{
		"soho: warning: brief " + h.brief + " has Type: backend and no 'Risk markers' section: nothing makes the slice state its risks",
		"soho: warning: brief " + h.brief + " has Type: backend and no 'Mutations' section: name the mutation and the case that must fail, for each rule",
	} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr is missing %q:\n%s", want, stderr)
		}
	}
	if strings.Contains(stderr, "(brief_lint=strict)") {
		t.Fatalf("the new warnings must not refuse under strict:\n%s", stderr)
	}
}

func TestLintCommandTypeSectionWarningsExitModes(t *testing.T) {
	root := t.TempDir()
	skillDir, err := filepath.Abs(filepath.Join(packageDir, "..", "..", "skills", "soho"))
	if err != nil {
		t.Fatal(err)
	}
	oldCwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldCwd) }()
	if err := os.Mkdir(filepath.Join(root, "home"), 0o700); err != nil {
		t.Fatal(err)
	}
	brief := filepath.Join(root, "brief.md")
	// Every standard section is present; only the new check speaks.
	if err := os.WriteFile(brief, []byte("# Goal\nDo the slice.\n# Expected result\nDone.\n# Owned files\nsrc/a.go\n# Forbidden\nNo commit/push.\n# Report\nDone.\nType: backend\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{
		"PATH":            os.Getenv("PATH"),
		"HOME":            filepath.Join(root, "home"),
		"XDG_CONFIG_HOME": filepath.Join(root, "home"),
		"SOHO_DIR":        filepath.Join(root, "state"),
		"SOHO_SKILL_DIR":  skillDir,
	}
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var stdout, stderr bytes.Buffer
	platform.Stdout, platform.Stderr = &stdout, &stderr
	defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
	run := func() (int, string, string) {
		stdout.Reset()
		stderr.Reset()
		code := Run([]string{"lint", brief}, env)
		return code, stdout.String(), stderr.String()
	}
	env["SOHO_BRIEF_LINT"] = "strict"
	code, out, errOut := run()
	if code != 1 || out != "" {
		t.Fatalf("strict lint with only the new warnings: code=%d stdout=%q stderr=%q (want code 1, no stdout)", code, out, errOut)
	}
	for _, want := range []string{
		"soho: warning: brief " + brief + " has Type: backend and no 'Risk markers' section: nothing makes the slice state its risks",
		"soho: warning: brief " + brief + " has Type: backend and no 'Mutations' section: name the mutation and the case that must fail, for each rule",
	} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("strict stderr is missing %q:\n%s", want, errOut)
		}
	}
	if strings.Contains(errOut, "(brief_lint=strict)") {
		t.Fatalf("the new warnings must not refuse under strict:\n%s", errOut)
	}
	env["SOHO_BRIEF_LINT"] = "off"
	code, out, errOut = run()
	if code != 0 || out != "brief "+brief+": lint off (brief_lint=off)\n" || errOut != "" {
		t.Fatalf("off lint: code=%d stdout=%q stderr=%q", code, out, errOut)
	}
}
