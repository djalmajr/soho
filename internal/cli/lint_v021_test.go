package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// The pt-BR brief uses the five Portuguese headings of the shipped
// brief_lint_aliases default (Arquivos próprios is accepted only by the
// default) plus the "Sem commit" line: no mark may come out of the lint.
const ptBRLintBrief = "## Objetivo\nFazer a fatia.\n## Resultado esperado\nFeito.\n## Arquivos próprios\nsrc/a.go\n## Proibido\nSem commit.\n## Relatório\nFeito.\n"

// English brief with every section and a WORKTREE_PATH on line 11.
const placeholderLintBrief = "# Goal\nDo the slice.\n# Expected result\nDone.\n# Owned files\nsrc/a.go\n# Forbidden\nNo commit/push.\n# Report\nDone.\nworktree: WORKTREE_PATH\n"

func lintV021Fixture(t *testing.T, briefBody string) (root, brief string, env platform.Env) {
	t.Helper()
	root = t.TempDir()
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
	t.Cleanup(func() { _ = os.Chdir(oldCwd) })
	for _, dir := range []string{"home", "config", "state"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	brief = filepath.Join(root, "brief v021.md")
	if err := os.WriteFile(brief, []byte(briefBody), 0o600); err != nil {
		t.Fatal(err)
	}
	return root, brief, platform.Env{
		"PATH":            os.Getenv("PATH"),
		"HOME":            filepath.Join(root, "home"),
		"XDG_CONFIG_HOME": filepath.Join(root, "config"),
		"SOHO_DIR":        filepath.Join(root, "state"),
		"SOHO_SKILL_DIR":  skillDir,
	}
}

func TestLintCommandPortugueseDefault(t *testing.T) {
	t.Run("the five Portuguese titles and the 'Sem commit' line lint clean", func(t *testing.T) {
		_, brief, env := lintV021Fixture(t, ptBRLintBrief)
		env["SOHO_BRIEF_LINT"] = "warn"
		oldOut, oldErr := platform.Stdout, platform.Stderr
		var stdout, stderr bytes.Buffer
		platform.Stdout, platform.Stderr = &stdout, &stderr
		defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
		if code := Run([]string{"lint", brief}, env); code != 0 || stdout.String() != "brief "+brief+": ok\n" || stderr.Len() != 0 {
			t.Fatalf("clean Portuguese lint: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
		// strict agrees: the brief has no mark to refuse
		env["SOHO_BRIEF_LINT"] = "strict"
		stdout.Reset()
		stderr.Reset()
		if code := Run([]string{"lint", brief}, env); code != 0 || stderr.Len() != 0 {
			t.Fatalf("strict Portuguese lint: code=%d stderr=%q", code, stderr.String())
		}
	})
	t.Run("a project alias file replaces the shipped default", func(t *testing.T) {
		root, brief, env := lintV021Fixture(t, strings.Replace(ptBRLintBrief, "## Resultado esperado", "## Aceite", 1))
		if err := os.MkdirAll(filepath.Join(root, ".agents"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".agents", "soho.conf"), []byte("brief_lint_aliases=Goal=Task\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		env["SOHO_BRIEF_LINT"] = "warn"
		oldOut, oldErr := platform.Stdout, platform.Stderr
		var stdout, stderr bytes.Buffer
		platform.Stdout, platform.Stderr = &stdout, &stderr
		defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
		if code := Run([]string{"lint", brief}, env); code != 1 || !strings.Contains(stderr.String(), "[Expected result]") {
			t.Fatalf("project alias did not replace the default: code=%d stderr=%q", code, stderr.String())
		}
	})
}

func TestLintCommandUnfilledPlaceholder(t *testing.T) {
	t.Run("warn prints the mark with the right line and exits 1", func(t *testing.T) {
		_, brief, env := lintV021Fixture(t, placeholderLintBrief)
		env["SOHO_BRIEF_LINT"] = "warn"
		oldOut, oldErr := platform.Stdout, platform.Stderr
		var stdout, stderr bytes.Buffer
		platform.Stdout, platform.Stderr = &stdout, &stderr
		defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
		want := "warning: brief " + brief + " is missing sections: [unfilled placeholder 'WORKTREE_PATH' (line 11)]"
		if code := Run([]string{"lint", brief}, env); code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), want) {
			t.Fatalf("warn: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
	})
	t.Run("strict refuses with exit 2", func(t *testing.T) {
		_, brief, env := lintV021Fixture(t, placeholderLintBrief)
		env["SOHO_BRIEF_LINT"] = "strict"
		oldOut, oldErr := platform.Stdout, platform.Stderr
		var stdout, stderr bytes.Buffer
		platform.Stdout, platform.Stderr = &stdout, &stderr
		defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
		if code := Run([]string{"lint", brief}, env); code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "unfilled placeholder 'WORKTREE_PATH' (line 11)") || !strings.Contains(stderr.String(), "(brief_lint=strict)") {
			t.Fatalf("strict: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
	})
	t.Run("the same marker inside a fenced code block lints clean", func(t *testing.T) {
		_, brief, env := lintV021Fixture(t, strings.Replace(placeholderLintBrief, "worktree: WORKTREE_PATH", "```\nworktree: WORKTREE_PATH\n```", 1))
		env["SOHO_BRIEF_LINT"] = "strict"
		oldOut, oldErr := platform.Stdout, platform.Stderr
		var stdout, stderr bytes.Buffer
		platform.Stdout, platform.Stderr = &stdout, &stderr
		defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
		if code := Run([]string{"lint", brief}, env); code != 0 || stdout.String() != "brief "+brief+": ok\n" || stderr.Len() != 0 {
			t.Fatalf("fenced marker: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
	})
	t.Run("a project brief_lint_placeholders item produces its own mark", func(t *testing.T) {
		root, brief, env := lintV021Fixture(t, "# Goal\nslot MY_SLOT here\n# Expected result\nDone.\n# Owned files\nsrc/a.go\n# Forbidden\nNo commit/push.\n# Report\nDone.\n")
		if err := os.MkdirAll(filepath.Join(root, ".agents"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".agents", "soho.conf"), []byte("brief_lint_placeholders=MY_SLOT\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		env["SOHO_BRIEF_LINT"] = "strict"
		oldOut, oldErr := platform.Stdout, platform.Stderr
		var stdout, stderr bytes.Buffer
		platform.Stdout, platform.Stderr = &stdout, &stderr
		defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
		if code := Run([]string{"lint", brief}, env); code != 2 || !strings.Contains(stderr.String(), "unfilled placeholder 'MY_SLOT' (line 2)") {
			t.Fatalf("custom placeholder: code=%d stderr=%q", code, stderr.String())
		}
	})
}

func TestDispatchStrictRefusesUnfilledPlaceholder(t *testing.T) {
	t.Run("strict dispatch refuses the brief before sending anything", func(t *testing.T) {
		f := newDispatchArrivalFixture(t, "idle", 1, 2, "", "0")
		data, err := os.ReadFile(f.brief)
		if err != nil {
			t.Fatal(err)
		}
		// The fixture brief ends on line 15 ("done"); the marker lands on 16.
		if err := os.WriteFile(f.brief, append(data, []byte("worktree: WORKTREE_PATH\n")...), 0o600); err != nil {
			t.Fatal(err)
		}
		f.env["SOHO_BRIEF_LINT"] = "strict"
		code, _, errText := f.run(t, "worker", f.brief, "--no-wait")
		if code != 2 || !strings.Contains(errText, "unfilled placeholder 'WORKTREE_PATH' (line 16)") || !strings.Contains(errText, "(brief_lint=strict)") {
			t.Fatalf("refusal: code=%d stderr=%q", code, errText)
		}
		// Nothing was sent: the call log does not even exist (no herdr call
		// happened before the refusal) or, if it does, it holds no prompt.
		calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		for _, call := range calls {
			if len(call.Argv) > 0 && call.Argv[0] == "prompt" {
				t.Fatalf("dispatch sent a prompt before refusing: %#v", call.Argv)
			}
		}
	})
}
