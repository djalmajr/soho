package validation

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestExercisedNothing covers decision 5 of DJA-695 fatia 3 (amended to
// read the evidence log line by line from an io.Reader): the log carries
// the command header, the merged output and the trailing exit/duration
// lines, and the check reports "exercised nothing" when no line that ran
// appears in the body — a non-empty line other than the header and the
// tail, that is not a marker line. A marker line carries "[no tests to
// run]" or "[no test files]" (today the check covers Go only; for another
// tool the tester writes a command that fails when nothing runs).
func TestExercisedNothing(t *testing.T) {
	const header = "command: sh -c 'go test ./...'\n"
	const tail = "\nexit: 0\nduration: 412ms\n"
	cases := []struct {
		name    string
		log     string
		want    bool
		wantErr bool
	}{
		{"the marker on an ok line is nothing", header + "ok  example.com 0.1s [no tests to run]\n" + tail, true, false},
		{"the no-test-files marker is nothing", header + "?   example.com [no test files]\n" + tail, true, false},
		{"go test -v with no match is nothing", header + "testing: warning: no tests to run\nPASS\nok  example.com 0.1s [no tests to run]\n" + tail, true, false},
		{"a download line does not count as a test", header + "go: downloading example.com/dep v1.0.0\nok  example.com 0.1s [no tests to run]\n" + tail, true, false},
		{"a package that ran alongside the marker is something", header + "ok  example.com 0.3s\nok  example.com/other 0.1s [no tests to run]\n" + tail, false, false},
		{"a cached package that ran is something", header + "ok  example.com (cached)\n?   example.com/none [no test files]\n" + tail, false, false},
		{"a silent run is not nothing", header + tail, false, false},
		{"output without a marker is not nothing", header + "lint passed\n" + tail, false, false},
		{"a package line without a marker is not nothing", header + "ok  example.com 0.2s\n" + tail, false, false},
		{"an empty log is not nothing", "", false, false},
		{"a marker in the command header does not count", "command: sh -c 'echo [no tests to run]'\nhello\n" + tail, false, false},
		{"a long line does not stop the scan", header + strings.Repeat("x", 1<<20) + "\nok  example.com 0.1s [no tests to run]\n" + tail, true, false},
		{"the rest of a long line is not read as a new line", header + strings.Repeat("x", 1<<20) + "ok  example.com 0.1s\n?   example.com/none [no test files]\n" + tail, true, false},
		{"a read error is returned, not folded into a verdict", "", false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var r io.Reader = strings.NewReader(c.log)
			if c.wantErr {
				r = errorReader{err: errors.New("disk full")}
			}
			got, err := ExercisedNothing(r)
			if c.wantErr {
				if err == nil || got {
					t.Fatalf("ExercisedNothing(read error) = %v, %v; want the error", got, err)
				}
				return
			}
			if err != nil || got != c.want {
				t.Fatalf("ExercisedNothing(log) = %v, %v; want %v, nil", got, err, c.want)
			}
		})
	}
}

type errorReader struct{ err error }

func (r errorReader) Read(p []byte) (int, error) { return 0, r.err }

const checkLayerFile = "## Test layers\n" +
	"- unit: `go test -timeout=30m ./...`\n" +
	"- integration: n/a: no separate integration layer\n" +
	"- contract: n/a: covered by the smoke\n" +
	"- e2e: manual: the procedure of the docs\n" +
	"- mutation: `go test -race ./...`\n"

// TestCheckProblemTexts covers the read-level and parse-level problem texts
// that the doctor and the validate command share: each is the doctor's
// exact text, and the check reads the file without writing anything.
func TestCheckProblemTexts(t *testing.T) {
	t.Run("an absent file is the absent problem", func(t *testing.T) {
		root := t.TempDir()
		got := Check(filepath.Join(root, "VALIDATION.md"))
		if len(got.Problems) != 1 || got.Problems[0] != "no VALIDATION.md in the project root: the tester creates it from the skill's templates/VALIDATION.md" {
			t.Fatalf("absent problems = %q", got.Problems)
		}
	})

	t.Run("a broken link is the absent problem", func(t *testing.T) {
		root := t.TempDir()
		p := filepath.Join(root, "VALIDATION.md")
		if err := os.Symlink(filepath.Join(root, "gone.md"), p); err != nil {
			t.Skipf("cannot create a symlink on this system: %v", err)
		}
		got := Check(p)
		if len(got.Problems) != 1 || !strings.HasPrefix(got.Problems[0], "no VALIDATION.md in the project root") {
			t.Fatalf("broken link problems = %q", got.Problems)
		}
	})

	t.Run("a symlink loop is a refusal, not absent", func(t *testing.T) {
		root := t.TempDir()
		p := filepath.Join(root, "VALIDATION.md")
		if err := os.Symlink(p, p); err != nil {
			t.Skipf("cannot create a self-referencing symlink on this system: %v", err)
		}
		got := Check(p)
		if len(got.Problems) != 1 || !strings.HasPrefix(got.Problems[0], "could not check VALIDATION.md: ") {
			t.Fatalf("loop problems = %q", got.Problems)
		}
		if strings.Contains(got.Problems[0], p) {
			t.Fatalf("the problem leaked the path: %q", got.Problems[0])
		}
	})

	t.Run("a directory in place of the file is not a regular file", func(t *testing.T) {
		root := t.TempDir()
		p := filepath.Join(root, "VALIDATION.md")
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
		got := Check(p)
		if len(got.Problems) != 1 || got.Problems[0] != "could not check VALIDATION.md: not a regular file" {
			t.Fatalf("directory problems = %q", got.Problems)
		}
	})

	t.Run("a file above 1 MiB is oversized", func(t *testing.T) {
		root := t.TempDir()
		p := filepath.Join(root, "VALIDATION.md")
		if err := os.WriteFile(p, make([]byte, 1<<20+10), 0o600); err != nil {
			t.Fatal(err)
		}
		got := Check(p)
		if len(got.Problems) != 1 || got.Problems[0] != "could not check VALIDATION.md: exceeds 1 MiB" {
			t.Fatalf("oversized problems = %q", got.Problems)
		}
	})

	t.Run("no read permission is a refusal with the short reason", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Windows chmod 000 sets the read-only attribute, it does not deny read access")
		}
		if os.Geteuid() == 0 {
			t.Skip("running as root: mode 000 does not deny read access")
		}
		root := t.TempDir()
		p := filepath.Join(root, "VALIDATION.md")
		if err := os.WriteFile(p, []byte(checkLayerFile), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(p, 0o600) })
		got := Check(p)
		if len(got.Problems) != 1 || got.Problems[0] != "could not check VALIDATION.md: permission denied" {
			t.Fatalf("permission problems = %q", got.Problems)
		}
	})

	t.Run("a file without the section is the no-section problem", func(t *testing.T) {
		root := t.TempDir()
		p := filepath.Join(root, "VALIDATION.md")
		if err := os.WriteFile(p, []byte("# Validation\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		got := Check(p)
		if len(got.Problems) != 1 || got.Problems[0] != `VALIDATION.md has no "## Test layers" section: the tester adds it (see the skill's templates/VALIDATION.md)` {
			t.Fatalf("no section problems = %q", got.Problems)
		}
	})

	t.Run("malformed, placeholder and missing layers join in the doctor order", func(t *testing.T) {
		root := t.TempDir()
		p := filepath.Join(root, "VALIDATION.md")
		content := "## Test layers\n- unit: `go test ./...`\n- e2e: oops not a form\n"
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		got := Check(p)
		want := []string{
			"VALIDATION.md:3: malformed layer line (use \"- <layer>: `<command>`\", \"- <layer>: n/a: <reason>\" or \"- <layer>: manual: <what runs it>\")",
			"VALIDATION.md: layer integration is not declared (give its command, n/a with the reason, or manual)",
			"VALIDATION.md: layer contract is not declared (give its command, n/a with the reason, or manual)",
			"VALIDATION.md: layer e2e is not declared (give its command, n/a with the reason, or manual)",
			"VALIDATION.md: layer mutation is not declared (give its command, n/a with the reason, or manual)",
		}
		if !equalStrings(got.Problems, want) {
			t.Fatalf("problems = %q, want %q", got.Problems, want)
		}
	})

	t.Run("a filled file has no problems and the ok counts", func(t *testing.T) {
		root := t.TempDir()
		p := filepath.Join(root, "VALIDATION.md")
		if err := os.WriteFile(p, []byte(checkLayerFile), 0o600); err != nil {
			t.Fatal(err)
		}
		got := Check(p)
		if len(got.Problems) != 0 || got.OK != "VALIDATION.md: 2 commands, 2 n/a, 1 manual" {
			t.Fatalf("filled problems = %q ok = %q", got.Problems, got.OK)
		}
		if len(got.Doc.Layers) != 5 {
			t.Fatalf("the check lost the parsed layers: %d", len(got.Doc.Layers))
		}
	})

	t.Run("the check writes nothing", func(t *testing.T) {
		root := t.TempDir()
		p := filepath.Join(root, "VALIDATION.md")
		if err := os.WriteFile(p, []byte(checkLayerFile), 0o600); err != nil {
			t.Fatal(err)
		}
		before := mustDirNames(t, root)
		Check(p)
		Check(p)
		after := mustDirNames(t, root)
		if !equalStrings(before, after) {
			t.Fatalf("the root changed across the runs: %v -> %v", before, after)
		}
	})
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

func mustDirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}
