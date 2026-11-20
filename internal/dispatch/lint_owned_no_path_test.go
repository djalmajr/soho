package dispatch

// SOH-415: an edit brief whose Owned files section names no path leaves the
// audit with no scope. The warning also becomes a brief_lint=strict refusal;
// a listed path, an inherit directive and an explicit no-files declaration
// are the valid escapes. An empty section warns like a prose-only one.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

func TestBriefLintOwnedFilesNamesNoPath(t *testing.T) {
	root := t.TempDir()
	brief := filepath.Join(root, "brief.md")
	want := "brief " + brief + ": Owned files names no path; list paths or globs, write '- inherit: previous brief', or write '- None.' when the slice edits no file"
	write := func(section string) {
		t.Helper()
		body := "# Goal\nDo it.\n# Expected result\nDone.\n# Owned files\n" + section + "\n# Forbidden\nNo commit or push.\n# Report\nDone.\n"
		if err := os.WriteFile(brief, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	findings := func(mode string) BriefLintResult {
		t.Helper()
		env := platform.Env{"SOHO_BRIEF_LINT": mode, "HOME": root, "USERPROFILE": root, "XDG_CONFIG_HOME": root, "TMPDIR": root}
		got := BriefLintFindings(brief, &core.Config{Entries: map[string]core.ConfigEntry{}}, env, BriefLintOptions{WorkerCwd: root})
		return got
	}
	count := func(values []string) int {
		n := 0
		for _, value := range values {
			if value == want {
				n++
			}
		}
		return n
	}

	write("Os da rodada 12.")
	warn := findings("warn")
	if warn.Mode != "warn" || count(warn.Warnings) != 1 || count(warn.UnseenPaths) != 1 {
		t.Fatalf("warn findings = %#v, want one warning and one refusal entry", warn)
	}
	strict := findings("strict")
	if strict.Mode != "strict" || count(strict.Warnings) != 1 || count(strict.UnseenPaths) != 1 {
		t.Fatalf("strict findings = %#v, want the warning refused", strict)
	}

	for _, name := range []string{"listed path", "inherit directive", "no-files declaration"} {
		t.Run(name, func(t *testing.T) {
			switch name {
			case "listed path":
				write("- `src/a.go`")
			case "inherit directive":
				write("- inherit: previous brief")
			default:
				write("- None.")
			}
			got := findings("strict")
			if count(got.Warnings) != 0 || count(got.UnseenPaths) != 0 {
				t.Fatalf("%s still warns: %#v", name, got)
			}
		})
	}

	write("")
	empty := findings("strict")
	if count(empty.Warnings) != 1 || count(empty.UnseenPaths) != 1 {
		t.Fatalf("an empty Owned files section must warn and be refused under strict: %#v", empty)
	}

	write("- Forbidden:\n- inherit: previous brief")
	excluded := findings("strict")
	if count(excluded.Warnings) != 1 || count(excluded.UnseenPaths) != 1 {
		t.Fatalf("inherit under Forbidden must not count: %#v", excluded)
	}
}
