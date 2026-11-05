package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The doctor's exact DESIGN.md lines (brief DJA-695 fatia 2, decision 1):
// the check speaks only when a regular DESIGN.md exists in the project root.
const (
	designWarnName   = "warn   DESIGN.md has no front matter with a name: fill it from the skill's templates/DESIGN.md"
	designWarnTokens = "warn   DESIGN.md declares no colors or typography tokens: without them the design layer has nothing to check"
	designWarnLayer  = "warn   VALIDATION.md has no design layer, and the project has a DESIGN.md: declare one, 'manual: <what runs it>' until it passes"
	designOK         = "ok     design: DESIGN.md"
)

func designWarnLines(n int) string {
	return fmt.Sprintf("warn   DESIGN.md has %d lines: move the detail to design/<name>.md, read only when that part is touched", n)
}

// designLines returns the design lines of a doctor run: the four DESIGN.md
// warnings, the design-layer warning and the ok line, by their exact
// prefixes. Lines that merely mention the name inside a path (such as the
// state dir line of the fixture) stay out.
func designLines(out string) []string {
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "warn   DESIGN.md") ||
			strings.HasPrefix(line, "ok     design: DESIGN.md") ||
			strings.HasPrefix(line, "warn   VALIDATION.md has no design layer") {
			lines = append(lines, line)
		}
	}
	return lines
}

// goodDesign is a DESIGN.md that satisfies every check: front matter with
// name, colors and typography, and a short body.
const goodDesign = "---\nname: acme\ndescription: the acme design\ncolors:\n  primary: \"#111\"\ntypography:\n  base: sans\n---\n# Acme\n\nprose\n"

// designValidationWith is a filled VALIDATION.md (every required layer
// filled) with the design layer in the given value form.
func designValidationWith(designValue string) string {
	return "## Test layers\n- unit: `go test ./...`\n- integration: n/a: none\n- contract: n/a: none\n- e2e: n/a: none\n- mutation: n/a: none\n- design: " + designValue + "\n"
}

// designValidationWithoutDesign is a filled VALIDATION.md with the five
// required layers and no design layer.
const designValidationWithoutDesign = "## Test layers\n- unit: `go test ./...`\n- integration: n/a: none\n- contract: n/a: none\n- e2e: n/a: none\n- mutation: n/a: none\n"

// runDesign writes the named root files (regular file contents) and runs
// the whole doctor on the temp root.
func runDesign(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return validationFixture(t, root, validationEnv(root))
}

// TestDoctorDesignIsSilentWithoutARegularFile covers scenario 1: no
// DESIGN.md, a link in place of the file and a directory in place of the
// file each leave the output without a single design line.
func TestDoctorDesignIsSilentWithoutARegularFile(t *testing.T) {
	t.Run("no DESIGN.md: no design line", func(t *testing.T) {
		out := runDesign(t, map[string]string{"VALIDATION.md": designValidationWith("`bun test`")})
		if got := designLines(out); len(got) != 0 {
			t.Fatalf("no file printed design lines: %q", got)
		}
	})

	t.Run("a link in place of the file is silence", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "target.md")
		if err := os.WriteFile(target, []byte(goodDesign), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "VALIDATION.md"), []byte(designValidationWith("`bun test`")), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(root, "DESIGN.md")); err != nil {
			t.Skipf("cannot create a symlink on this system: %v", err)
		}
		out := validationFixture(t, root, validationEnv(root))
		if got := designLines(out); len(got) != 0 {
			t.Fatalf("a link printed design lines: %q", got)
		}
	})

	t.Run("a directory in place of the file is silence", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "DESIGN.md"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "VALIDATION.md"), []byte(designValidationWith("`bun test`")), 0o600); err != nil {
			t.Fatal(err)
		}
		out := validationFixture(t, root, validationEnv(root))
		if got := designLines(out); len(got) != 0 {
			t.Fatalf("a directory printed design lines: %q", got)
		}
	})
}

// TestDoctorDesignWarningsIsolated covers scenario 2: each of the four
// warnings, isolated from the others, is exactly the brief's line.
func TestDoctorDesignWarningsIsolated(t *testing.T) {
	t.Run("no front matter or no name", func(t *testing.T) {
		out := runDesign(t, map[string]string{
			"DESIGN.md":     "# Acme\n\nprose only, no front matter\n",
			"VALIDATION.md": designValidationWith("`bun test`"),
		})
		if got := designLines(out); len(got) != 1 || got[0] != designWarnName {
			t.Fatalf("name lines = %q", got)
		}
	})

	t.Run("no colors and no typography in the front matter", func(t *testing.T) {
		out := runDesign(t, map[string]string{
			"DESIGN.md":     "---\nname: acme\ndescription: no tokens\n---\n# Acme\n",
			"VALIDATION.md": designValidationWith("`bun test`"),
		})
		if got := designLines(out); len(got) != 1 || got[0] != designWarnTokens {
			t.Fatalf("tokens lines = %q", got)
		}
	})

	t.Run("VALIDATION.md without the design layer", func(t *testing.T) {
		out := runDesign(t, map[string]string{
			"DESIGN.md":     goodDesign,
			"VALIDATION.md": designValidationWithoutDesign,
		})
		if got := designLines(out); len(got) != 1 || got[0] != designWarnLayer {
			t.Fatalf("layer lines = %q", got)
		}
	})

	t.Run("more than 400 lines", func(t *testing.T) {
		body := goodDesign
		for len(strings.Split(body, "\n"))-1 < 450 {
			body += "z\n"
		}
		if got := len(strings.Split(body, "\n")) - 1; got != 450 {
			t.Fatalf("fixture is %d lines, want 450", got)
		}
		out := runDesign(t, map[string]string{
			"DESIGN.md":     body,
			"VALIDATION.md": designValidationWith("`bun test`"),
		})
		want := designWarnLines(450)
		if got := designLines(out); len(got) != 1 || got[0] != want {
			t.Fatalf("lines warning = %q, want %q", got, want)
		}
	})

	t.Run("exactly 400 lines is not a warning", func(t *testing.T) {
		body := goodDesign
		for len(strings.Split(body, "\n"))-1 < 400 {
			body += "z\n"
		}
		out := runDesign(t, map[string]string{
			"DESIGN.md":     body,
			"VALIDATION.md": designValidationWith("`bun test`"),
		})
		if got := designLines(out); len(got) != 1 || got[0] != designOK {
			t.Fatalf("400 lines lines = %q", got)
		}
	})
}

// TestDoctorDesignOKWithTheDesignLayer covers scenario 3: a correct file
// and a VALIDATION.md that declares the design layer in any of the three
// accepted forms print the single ok line.
func TestDoctorDesignOKWithTheDesignLayer(t *testing.T) {
	for _, tc := range []struct{ name, value string }{
		{"a command", "`bun test`"},
		{"manual", "manual: the designer runs the parity audit"},
		{"n/a", "n/a: no UI in the project"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := runDesign(t, map[string]string{
				"DESIGN.md":     goodDesign,
				"VALIDATION.md": designValidationWith(tc.value),
			})
			if got := designLines(out); len(got) != 1 || got[0] != designOK {
				t.Fatalf("%s lines = %q", tc.name, got)
			}
		})
	}
}

// TestDoctorDesignIsSilentUnderSetupTargetLocal covers scenario 4: with
// setup_target=local the repository is third-party and the check prints
// nothing, not even for a broken file.
func TestDoctorDesignIsSilentUnderSetupTargetLocal(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "DESIGN.md"), []byte("# Acme\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := validationEnv(root)
	env["SOHO_SETUP_TARGET"] = "local"
	out := validationFixture(t, root, env)
	if got := designLines(out); len(got) != 0 {
		t.Fatalf("setup_target=local printed design lines: %q", got)
	}
	// Control: the same root without the key warns.
	out = validationFixture(t, root, validationEnv(root))
	if got := designLines(out); len(got) != 1 || got[0] != designWarnName {
		t.Fatalf("control lost the name warning: %q", got)
	}
}

// TestDoctorDesignFrontMatterIsFirstLevelOnly covers scenario 5: a --- that
// appears only at the end is not a front matter, and a key nested below
// another key is not a first-level key.
func TestDoctorDesignFrontMatterIsFirstLevelOnly(t *testing.T) {
	t.Run("a closing --- at the end is not a front matter", func(t *testing.T) {
		out := runDesign(t, map[string]string{
			"DESIGN.md":     "name: acme\ncolors:\n  primary: \"#111\"\n---\n# Acme\n",
			"VALIDATION.md": designValidationWith("`bun test`"),
		})
		if got := designLines(out); len(got) != 1 || got[0] != designWarnName {
			t.Fatalf("end-only --- lines = %q", got)
		}
	})

	t.Run("a nested colors and typography are not first-level keys", func(t *testing.T) {
		out := runDesign(t, map[string]string{
			"DESIGN.md":     "---\nname: acme\ntokens:\n  colors:\n    primary: \"#111\"\n  typography:\n    base: sans\n---\n# Acme\n",
			"VALIDATION.md": designValidationWith("`bun test`"),
		})
		if got := designLines(out); len(got) != 1 || got[0] != designWarnTokens {
			t.Fatalf("nested keys lines = %q", got)
		}
	})
}

// TestDoctorDesignBoundsTheRead covers scenario 6: a file above 1 MiB gets
// the lines warning with the count of the bounded read, without a panic.
// The fixture: a 44-byte header (6 lines) plus 524267 "z\n" lines —
// 1048578 bytes in full, 1048576 bytes (1 MiB) read, which ends on a line
// boundary: 6 + (1048576-44)/2 = 524272 lines.
func TestDoctorDesignBoundsTheRead(t *testing.T) {
	const header = "---\nname: acme\ncolors: x\ntypography: y\n---\n\n"
	const oversized = "warn   DESIGN.md is over 1 MiB: move the detail to design/<name>.md, read only when that part is touched"
	for name, body := range map[string]string{
		"many short lines":                     header + strings.Repeat("z\n", 524288),
		"one long line, then many short lines": header + strings.Repeat("z", 1<<20) + "\n" + strings.Repeat("z\n", 401),
	} {
		t.Run(name, func(t *testing.T) {
			out := runDesign(t, map[string]string{
				"DESIGN.md":     body,
				"VALIDATION.md": designValidationWith("`bun test`"),
			})
			if got := designLines(out); len(got) != 1 || got[0] != oversized {
				t.Fatalf("oversized lines = %q, want %q", got, oversized)
			}
		})
	}
	t.Run("exactly 1 MiB is still read", func(t *testing.T) {
		body := header + strings.Repeat("z", 1<<20-len(header))
		out := runDesign(t, map[string]string{
			"DESIGN.md":     body,
			"VALIDATION.md": designValidationWith("`bun test`"),
		})
		if got := designLines(out); len(got) != 1 || got[0] != designOK {
			t.Fatalf("1 MiB lines = %q", got)
		}
	})
}

// TestDoctorDesignEdgeCases covers the brief's edge rows: CRLF, a BOM, an
// empty file, and a malformed VALIDATION.md (its own warning stays, the
// design warning fires once, nothing panics).
func TestDoctorDesignEdgeCases(t *testing.T) {
	t.Run("CRLF is the same result", func(t *testing.T) {
		crlf := strings.ReplaceAll(goodDesign, "\n", "\r\n")
		out := runDesign(t, map[string]string{
			"DESIGN.md":     crlf,
			"VALIDATION.md": designValidationWith("`bun test`"),
		})
		if got := designLines(out); len(got) != 1 || got[0] != designOK {
			t.Fatalf("CRLF lines = %q", got)
		}
	})

	t.Run("a BOM at the start is the same result", func(t *testing.T) {
		out := runDesign(t, map[string]string{
			"DESIGN.md":     "\uFEFF" + goodDesign,
			"VALIDATION.md": designValidationWith("`bun test`"),
		})
		if got := designLines(out); len(got) != 1 || got[0] != designOK {
			t.Fatalf("BOM lines = %q", got)
		}
	})

	t.Run("an empty file is the front matter warning", func(t *testing.T) {
		out := runDesign(t, map[string]string{
			"DESIGN.md":     "",
			"VALIDATION.md": designValidationWith("`bun test`"),
		})
		if got := designLines(out); len(got) != 1 || got[0] != designWarnName {
			t.Fatalf("empty lines = %q", got)
		}
	})

	t.Run("a VALIDATION.md that is a link to a valid file is read, as validation.Check reads it", func(t *testing.T) {
		for _, tc := range []struct {
			name, contract, want string
		}{
			{"without design", "## Test layers\n- unit: `go test`\n- integration: n/a: r\n- contract: n/a: r\n- e2e: n/a: r\n- mutation: n/a: r\n", designWarnLayer},
			{"with design", designValidationWith("`bun test`"), designOK},
		} {
			t.Run(tc.name, func(t *testing.T) {
				root := t.TempDir()
				target := filepath.Join(t.TempDir(), "contract.md")
				if err := os.WriteFile(target, []byte(tc.contract), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "DESIGN.md"), []byte(goodDesign), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, filepath.Join(root, "VALIDATION.md")); err != nil {
					t.Skipf("cannot create a symlink on this system: %v", err)
				}
				out := validationFixture(t, root, validationEnv(root))
				if got := designLines(out); len(got) != 1 || got[0] != tc.want {
					t.Fatalf("design lines = %q, want %q", got, tc.want)
				}
			})
		}
	})

	t.Run("a VALIDATION.md without the section keeps its own warning, with no design question", func(t *testing.T) {
		out := runDesign(t, map[string]string{
			"DESIGN.md":     goodDesign,
			"VALIDATION.md": "# Validation\n\nno section at all\n",
		})
		if got := designLines(out); len(got) != 1 || got[0] != designOK {
			t.Fatalf("no-section design lines = %q", got)
		}
		if !strings.Contains(out, `warn   VALIDATION.md has no "## Test layers" section`) {
			t.Fatalf("the existing VALIDATION.md warning is missing:\n%s", out)
		}
	})

	t.Run("a malformed layer line keeps its own warning, with no design question", func(t *testing.T) {
		out := runDesign(t, map[string]string{
			"DESIGN.md":     goodDesign,
			"VALIDATION.md": "## Test layers\n- unit: `go test`\n- integration: n/a: r\n- contract: n/a: r\n- e2e: n/a: r\n- mutation: n/a: r\n- design: not a command\n",
		})
		if got := designLines(out); len(got) != 1 || got[0] != designOK {
			t.Fatalf("malformed design lines = %q", got)
		}
		if !strings.Contains(out, "malformed layer line") {
			t.Fatalf("the existing VALIDATION.md warning is missing:\n%s", out)
		}
	})
}

// TestDoctorDesignUnderNowrite covers scenario 7: under SOHO_NOWRITE=1 the
// check still runs (it is read-only) and creates nothing in the root.
func TestDoctorDesignUnderNowrite(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "DESIGN.md"), []byte("# Acme\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "VALIDATION.md"), []byte(designValidationWith("`bun test`")), 0o600); err != nil {
		t.Fatal(err)
	}
	env := validationEnv(root)
	env["SOHO_NOWRITE"] = "1"
	// The fixture creates the PATH bin dir inside the root; snapshot after
	// it so the before/after comparison is about the doctor run only.
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
	if got := designLines(out); len(got) != 1 || got[0] != designWarnName {
		t.Fatalf("nowrite lines = %q", got)
	}
}
