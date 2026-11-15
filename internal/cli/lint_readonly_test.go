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

// SOH-287: `lint <brief> --role <read-only role>` reports the conflict the
// dispatch refuses (Type line and literal Owned files items, with the "None"
// exception); an edit role or no --role leaves the lint output unchanged.
func TestLintReadOnlyRoleReportsEditConflict(t *testing.T) {
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
	for _, dir := range []string{"home", "config", "state"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	env := platform.Env{
		"PATH":            testutil.HostPath(),
		"HOME":            filepath.Join(root, "home"),
		"XDG_CONFIG_HOME": filepath.Join(root, "config"),
		"SOHO_DIR":        filepath.Join(root, "state"),
		"SOHO_SKILL_DIR":  skillDir,
		"SOHO_BRIEF_LINT": "warn",
	}
	run := func(args ...string) (int, string, string) {
		var stdout, stderr bytes.Buffer
		oldOut, oldErr := platform.Stdout, platform.Stderr
		platform.Stdout, platform.Stderr = &stdout, &stderr
		defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
		rc := Run(args, env)
		return rc, stdout.String(), stderr.String()
	}
	write := func(name, owned, typeLine string) string {
		p := filepath.Join(root, name)
		body := typeLine + "# Goal\nSurvey.\n# Expected result\nDone.\n# Owned files\n" + owned + "\n# Forbidden\nNo commit/push.\n# Report\nDone.\n"
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	listed := write("listed.md", "- internal/x.go", "")
	none := write("none.md", "Nenhum. Só leitura.", "")
	typed := write("typed.md", "None.", "Type: backend\n")

	// A listed path under a read-only role: the dispatch's conflict, exit 2.
	rc, out, errText := run("lint", listed, "--role", "scouter")
	want := "soho: lint: brief " + listed + " asks for edits (Owned files) but 'scouter' is a read-only role; the dispatch would refuse it. Lint it with --role implementer to check it as an edit role, or rename the 'Owned files' title, or drop the edit sections from the brief\n"
	if rc != 2 || out != "" || errText != want {
		t.Fatalf("listed owned path: rc=%d out=%q stderr=%q", rc, out, errText)
	}
	// The Type line is the same signal the dispatch uses, and None does not hide it.
	rc, out, errText = run("lint", typed, "--role", "scouter")
	if rc != 2 || out != "" || !strings.Contains(errText, "asks for edits (Type: backend) but 'scouter' is a read-only role") || !strings.Contains(errText, "Lint it with --role implementer") || !strings.Contains(errText, "or drop the 'Type: backend' line and the edit sections from the brief") {
		t.Fatalf("typed brief: rc=%d out=%q stderr=%q", rc, out, errText)
	}
	// None: no conflict, the brief is ok.
	rc, out, errText = run("lint", none, "--role", "scouter")
	if rc != 0 || out != "brief "+none+": ok\n" || errText != "" {
		t.Fatalf("none declaration: rc=%d out=%q stderr=%q", rc, out, errText)
	}
	// An edit role or no --role: unchanged, the listed path is fine.
	for _, args := range [][]string{{"lint", listed}, {"lint", listed, "--role", "implementer"}} {
		rc, out, errText = run(args...)
		if rc != 0 || out != "brief "+args[1]+": ok\n" || errText != "" {
			t.Fatalf("%v: rc=%d out=%q stderr=%q", args, rc, out, errText)
		}
	}
	// An edit role keeps its own findings for a typed brief and gets no conflict.
	rc, out, errText = run("lint", typed, "--role", "implementer")
	if rc != 1 || out != "" || strings.Contains(errText, "asks for edits") || !strings.Contains(errText, "no 'Risk markers' section") {
		t.Fatalf("typed brief with an edit role: rc=%d out=%q stderr=%q", rc, out, errText)
	}
}
