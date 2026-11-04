package doctor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

// validationFixture runs DoctorCheck on a temp project root and returns its
// output. The state dir lives outside the root so the run cannot add files
// to the project root.
func validationFixture(t *testing.T, root string, env platform.Env) string {
	t.Helper()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	env["PATH"] = bin
	env["SOHO_DIR"] = t.TempDir()
	ctx := core.Config{Entries: map[string]core.ConfigEntry{}}
	var out strings.Builder
	DoctorCheck(&ctx, env, root, &out)
	return out.String()
}

func validationEnv(root string) platform.Env {
	return platform.Env{"HOME": root, "USERPROFILE": root, "SOHO_SKILL_DIR": root}
}

func validationLines(out string) []string {
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "VALIDATION.md") {
			lines = append(lines, line)
		}
	}
	return lines
}

const (
	warnAbsent    = "warn   no VALIDATION.md in the project root: the tester creates it from the skill's templates/VALIDATION.md"
	warnNoSection = `warn   VALIDATION.md has no "## Test layers" section: the tester adds it (see the skill's templates/VALIDATION.md)`
)

func malformedLine(n int) string {
	return fmt.Sprintf("warn   VALIDATION.md:%d: malformed layer line (use \"- <layer>: `<command>`\", \"- <layer>: n/a: <reason>\" or \"- <layer>: manual: <what runs it>\")", n)
}

func placeholderLine(n int, name string) string {
	return fmt.Sprintf("warn   VALIDATION.md:%d: layer %s still has the template placeholder", n, name)
}

func missingLine(name string) string {
	return fmt.Sprintf("warn   VALIDATION.md: layer %s is not declared (give its command, n/a with the reason, or manual)", name)
}

// TestDoctorValidationMessages covers the exact warning and ok lines
// (brief DJA-695 fatia 2, decision 6, scenario 4).
func TestDoctorValidationMessages(t *testing.T) {
	t.Run("absent file warns with the creation hint", func(t *testing.T) {
		root := t.TempDir()
		out := validationFixture(t, root, validationEnv(root))
		if got := validationLines(out); len(got) != 1 || got[0] != warnAbsent {
			t.Fatalf("absent file lines = %q", got)
		}
	})

	t.Run("a file without the section warns once", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "VALIDATION.md"), []byte("# Validation\n\n## Scope\n\nnothing else\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		out := validationFixture(t, root, validationEnv(root))
		if got := validationLines(out); len(got) != 1 || got[0] != warnNoSection {
			t.Fatalf("no section lines = %q", got)
		}
	})

	t.Run("a malformed layer line warns with its number", func(t *testing.T) {
		root := t.TempDir()
		content := "## Test layers\n- unit: `go test ./...`\n- integration: not a form\n"
		if err := os.WriteFile(filepath.Join(root, "VALIDATION.md"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		out := validationFixture(t, root, validationEnv(root))
		// integration is not declared: its only line is the malformed one, so
		// the missing-layer warnings join the malformed warning.
		want := []string{malformedLine(3), missingLine("integration"), missingLine("contract"), missingLine("e2e"), missingLine("mutation")}
		if got := validationLines(out); len(got) != len(want) {
			t.Fatalf("malformed lines = %q, want %d lines", got, len(want))
		}
		if got := validationLines(out); got[0] != want[0] {
			t.Fatalf("first line = %q, want %q", got[0], want[0])
		}
	})

	t.Run("a placeholder layer warns with its name", func(t *testing.T) {
		root := t.TempDir()
		content := "## Test layers\n- unit: `<command>`\n- integration: n/a: <reason>\n- contract: `go test ./...`\n- e2e: `playwright test`\n- mutation: `soho mutation-copy`\n"
		if err := os.WriteFile(filepath.Join(root, "VALIDATION.md"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		out := validationFixture(t, root, validationEnv(root))
		want := []string{placeholderLine(2, "unit"), placeholderLine(3, "integration")}
		if got := validationLines(out); len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
			t.Fatalf("placeholder lines = %q, want %q", got, want)
		}
	})

	t.Run("missing required layers warn in the required order", func(t *testing.T) {
		root := t.TempDir()
		content := "## Test layers\n- unit: `go test ./...`\n"
		if err := os.WriteFile(filepath.Join(root, "VALIDATION.md"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		out := validationFixture(t, root, validationEnv(root))
		want := []string{missingLine("integration"), missingLine("contract"), missingLine("e2e"), missingLine("mutation")}
		if got := validationLines(out); len(got) != len(want) {
			t.Fatalf("missing lines = %q, want %d lines", got, len(want))
		}
		for i := range want {
			if got := validationLines(out); got[i] != want[i] {
				t.Fatalf("line %d = %q, want %q", i, got[i], want[i])
			}
		}
	})

	t.Run("a filled file is one ok line with the counts", func(t *testing.T) {
		root := t.TempDir()
		content := "## Test layers\n- unit: `go test -timeout=30m ./...`\n- integration: n/a: no separate integration layer\n- contract: n/a: covered by the job smoke\n- e2e: manual: the procedure of the docs\n- mutation: `go test -race ./...`\n"
		if err := os.WriteFile(filepath.Join(root, "VALIDATION.md"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		out := validationFixture(t, root, validationEnv(root))
		want := "ok     VALIDATION.md: 2 commands, 2 n/a, 1 manual"
		if got := validationLines(out); len(got) != 1 || got[0] != want {
			t.Fatalf("filled file lines = %q, want %q", got, want)
		}
	})
}

// TestDoctorValidationIsSilentUnderSetupTargetLocal covers scenario 5: with
// setup_target=local the file is not ours to demand — no line about it, not
// even the absent-file warning.
func TestDoctorValidationIsSilentUnderSetupTargetLocal(t *testing.T) {
	root := t.TempDir()
	env := validationEnv(root)
	env["SOHO_SETUP_TARGET"] = "local"
	out := validationFixture(t, root, env)
	if got := validationLines(out); len(got) != 0 {
		t.Fatalf("setup_target=local printed VALIDATION.md lines: %q", got)
	}
	// Control: the same root without the key does warn.
	out = validationFixture(t, root, validationEnv(root))
	if got := validationLines(out); len(got) != 1 || got[0] != warnAbsent {
		t.Fatalf("control lost the absent-file warning: %q", got)
	}
}

// TestDoctorValidationRefusesNonRegularAndOversizedFiles covers scenario 6:
// a directory in place of the file and a file above 1 MiB warn with the
// short reason and never panic.
func TestDoctorValidationRefusesNonRegularAndOversizedFiles(t *testing.T) {
	t.Run("a directory in place of the file", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "VALIDATION.md"), 0o755); err != nil {
			t.Fatal(err)
		}
		out := validationFixture(t, root, validationEnv(root))
		if got := validationLines(out); len(got) != 1 || got[0] != "warn   could not check VALIDATION.md: not a regular file" {
			t.Fatalf("directory lines = %q", got)
		}
	})

	t.Run("a file above 1 MiB", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "VALIDATION.md"), make([]byte, 1<<20+10), 0o600); err != nil {
			t.Fatal(err)
		}
		out := validationFixture(t, root, validationEnv(root))
		if got := validationLines(out); len(got) != 1 || got[0] != "warn   could not check VALIDATION.md: exceeds 1 MiB" {
			t.Fatalf("oversized lines = %q", got)
		}
	})

	t.Run("a FIFO in place of the file is refused without blocking", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("no FIFOs on Windows")
		}
		root := t.TempDir()
		if out, err := exec.Command("mkfifo", filepath.Join(root, "VALIDATION.md")).CombinedOutput(); err != nil {
			t.Skipf("mkfifo unavailable: %v (%s)", err, strings.TrimSpace(string(out)))
		}
		done := make(chan string, 1)
		go func() { done <- validationFixture(t, root, validationEnv(root)) }()
		select {
		case out := <-done:
			if got := validationLines(out); len(got) != 1 || got[0] != "warn   could not check VALIDATION.md: not a regular file" {
				t.Fatalf("fifo lines = %q", got)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("the check blocked on the FIFO: no result within the deadline")
		}
	})

	t.Run("a symlink loop is a check refusal, not an absent file", func(t *testing.T) {
		root := t.TempDir()
		p := filepath.Join(root, "VALIDATION.md")
		if err := os.Symlink(p, p); err != nil {
			t.Skipf("cannot create a self-referencing symlink on this system: %v", err)
		}
		out := validationFixture(t, root, validationEnv(root))
		got := validationLines(out)
		if len(got) != 1 || !strings.HasPrefix(got[0], "warn   could not check VALIDATION.md: ") {
			t.Fatalf("loop lines = %q", got)
		}
		if strings.Contains(got[0], p) {
			t.Fatalf("the warning leaked the path: %q", got[0])
		}
	})
}

// TestDoctorValidationDoesNotEchoFileContent covers scenario 7 and decision
// 7: a hostile line never reaches the output — only the line number and the
// validated layer name do.
func TestDoctorValidationDoesNotEchoFileContent(t *testing.T) {
	root := t.TempDir()
	content := "## Test layers\n- unit: SENTINEL_MALFORMED_X7 `oops`\n- e2e: `<command> SENTINEL_PLACEHOLDER_X8`\n"
	if err := os.WriteFile(filepath.Join(root, "VALIDATION.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	out := validationFixture(t, root, validationEnv(root))
	if strings.Contains(out, "SENTINEL_") {
		t.Fatalf("the doctor output echoed file content:\n%s", out)
	}
	// unit is not declared (its line is the malformed one), so the six
	// problems are capped: four lines, then the count of the rest.
	want := []string{malformedLine(2), placeholderLine(3, "e2e"), missingLine("unit"), missingLine("integration"), "warn   VALIDATION.md: 2 more problems"}
	if got := validationLines(out); len(got) != len(want) {
		t.Fatalf("hostile lines = %q, want %d lines", got, len(want))
	}
	for i := range want {
		if got := validationLines(out); got[i] != want[i] {
			t.Fatalf("hostile line %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestDoctorValidationUnderNowrite covers scenario 8: under SOHO_NOWRITE=1
// the check still runs (the doctor is accepted) and creates nothing in the
// project root.
func TestDoctorValidationUnderNowrite(t *testing.T) {
	root := t.TempDir()
	content := "## Test layers\n- unit: `go test ./...`\n- integration: n/a: no separate integration layer\n- contract: n/a: covered by the job smoke\n- e2e: manual: the procedure of the docs\n- mutation: `go test -race ./...`\n"
	if err := os.WriteFile(filepath.Join(root, "VALIDATION.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	env := validationEnv(root)
	env["SOHO_NOWRITE"] = "1"
	// The fixture creates the PATH bin dir inside the root; snapshot after it
	// so the before/after comparison is about the doctor run only.
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	out := validationFixture(t, root, env)
	after, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if namesBefore, namesAfter := dirNames(before), dirNames(after); !equalStrings(namesBefore, namesAfter) {
		t.Fatalf("the root changed under SOHO_NOWRITE: %v -> %v", namesBefore, namesAfter)
	}
	if got := validationLines(out); len(got) != 1 || got[0] != "ok     VALIDATION.md: 2 commands, 2 n/a, 1 manual" {
		t.Fatalf("nowrite lines = %q", got)
	}
}

func dirNames(entries []os.DirEntry) []string {
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestDoctorValidationCapsAtFiveWarnings covers scenario 9: with eight
// problems the output is five lines, the last one counting the rest.
func TestDoctorValidationCapsAtFiveWarnings(t *testing.T) {
	root := t.TempDir()
	content := "## Test layers\n" +
		"- unit: `<command>`\n" +
		"- e2e: `<command>`\n" +
		"- integration: no backticks here\n" +
		"- contract: n/a:\n" +
		"- MUTATION: `go test`\n"
	if err := os.WriteFile(filepath.Join(root, "VALIDATION.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	out := validationFixture(t, root, validationEnv(root))
	want := []string{
		malformedLine(4),
		malformedLine(5),
		malformedLine(6),
		placeholderLine(2, "unit"),
		"warn   VALIDATION.md: 4 more problems",
	}
	if got := validationLines(out); len(got) != len(want) {
		t.Fatalf("capped lines = %q, want %d lines", got, len(want))
	}
	for i := range want {
		if got := validationLines(out); got[i] != want[i] {
			t.Fatalf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestDoctorValidationFailureMatrix covers the brief's failure matrix with
// one executable fixture per marker. The clock marker needs no fixture: the
// matrix itself says nothing depends on the clock.
func TestDoctorValidationFailureMatrix(t *testing.T) {
	t.Run("nothing is written, so a crash leaves nothing to clean", func(t *testing.T) {
		// The check only reads: the project root keeps exactly its entries
		// across the run, which is what makes the crash case clean.
		root := t.TempDir()
		content := "## Test layers\n- unit: `go test ./...`\n- integration: n/a: no separate integration layer\n- contract: n/a: covered by the smoke\n- e2e: manual: the procedure of the docs\n- mutation: `go test -race ./...`\n"
		if err := os.WriteFile(filepath.Join(root, "VALIDATION.md"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		env := validationEnv(root)
		// The fixture creates the PATH bin dir inside the root; snapshot after
		// it so the comparison is about the doctor run only.
		if err := os.MkdirAll(filepath.Join(root, "bin"), 0o700); err != nil {
			t.Fatal(err)
		}
		before := dirNames(mustReadDir(t, root))
		out := validationFixture(t, root, env)
		after := dirNames(mustReadDir(t, root))
		if !equalStrings(before, after) {
			t.Fatalf("the root changed across the run: %v -> %v", before, after)
		}
		if got := validationLines(out); len(got) != 1 || got[0] != "ok     VALIDATION.md: 2 commands, 2 n/a, 1 manual" {
			t.Fatalf("lines = %q", got)
		}
	})

	t.Run("a retry prints the same output", func(t *testing.T) {
		root := t.TempDir()
		bin := filepath.Join(root, "bin")
		if err := os.MkdirAll(bin, 0o700); err != nil {
			t.Fatal(err)
		}
		env := validationEnv(root)
		env["PATH"] = bin
		env["SOHO_DIR"] = t.TempDir()
		ctx := core.Config{Entries: map[string]core.ConfigEntry{}}
		var first, second strings.Builder
		DoctorCheck(&ctx, env, root, &first)
		DoctorCheck(&ctx, env, root, &second)
		if first.String() != second.String() {
			t.Fatalf("retry differs:\nfirst:\n%s\nsecond:\n%s", first.String(), second.String())
		}
	})

	t.Run("an empty file has no section", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "VALIDATION.md"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		out := validationFixture(t, root, validationEnv(root))
		if got := validationLines(out); len(got) != 1 || got[0] != warnNoSection {
			t.Fatalf("empty file lines = %q", got)
		}
	})

	t.Run("a section without items warns the five missing layers", func(t *testing.T) {
		root := t.TempDir()
		content := "## Test layers\n\nnothing else declared\n"
		if err := os.WriteFile(filepath.Join(root, "VALIDATION.md"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		out := validationFixture(t, root, validationEnv(root))
		want := []string{missingLine("unit"), missingLine("integration"), missingLine("contract"), missingLine("e2e"), missingLine("mutation")}
		if got := validationLines(out); len(got) != len(want) {
			t.Fatalf("empty section lines = %q, want exactly five (within the cap)", got)
		}
		for i := range want {
			if got := validationLines(out); got[i] != want[i] {
				t.Fatalf("line %d = %q, want %q", i, got[i], want[i])
			}
		}
	})

	t.Run("a broken link is absent", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Symlink(filepath.Join(root, "gone.md"), filepath.Join(root, "VALIDATION.md")); err != nil {
			t.Skipf("cannot create a symlink on this system: %v", err)
		}
		out := validationFixture(t, root, validationEnv(root))
		if got := validationLines(out); len(got) != 1 || got[0] != warnAbsent {
			t.Fatalf("broken link lines = %q", got)
		}
	})

	t.Run("no read permission", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Windows chmod 000 sets the read-only attribute, it does not deny read access")
		}
		if os.Geteuid() == 0 {
			t.Skip("running as root: mode 000 does not deny read access")
		}
		root := t.TempDir()
		p := filepath.Join(root, "VALIDATION.md")
		if err := os.WriteFile(p, []byte("## Test layers\n- unit: `go test ./...`\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(p, 0o600) })
		out := validationFixture(t, root, validationEnv(root))
		if got := validationLines(out); len(got) != 1 || got[0] != "warn   could not check VALIDATION.md: permission denied" {
			t.Fatalf("permission lines = %q", got)
		}
	})
}

func mustReadDir(t *testing.T, path string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}
