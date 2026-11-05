package skill

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestBundlePublishesExactResourcesWithoutEngines(t *testing.T) {
	cache := t.TempDir()
	dir, err := Materialize(cache)
	if err != nil {
		t.Fatal(err)
	}
	files, _, err := bundledFiles()
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(file.name)))
		if err != nil || !bytes.Equal(data, file.data) {
			t.Fatalf("resource %s: err=%v", file.name, err)
		}
	}
	for _, name := range []string{"scripts", "assets.go", "assets_test.go"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("bundle contains an engine or build source %s: %v", name, err)
		}
	}
	again, err := Materialize(cache)
	if err != nil || again != dir {
		t.Fatalf("verified reuse: %q %v", again, err)
	}
}

func TestBundleRefusesCorruptionWithoutOverwriting(t *testing.T) {
	for _, kind := range []string{"changed", "missing", "extra", "extra-directory"} {
		t.Run(kind, func(t *testing.T) {
			cache := t.TempDir()
			dir, err := Materialize(cache)
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(dir, "config.defaults")
			switch kind {
			case "changed":
				err = os.WriteFile(file, []byte("foreign content"), 0o600)
			case "missing":
				err = os.Remove(file)
			case "extra":
				err = os.WriteFile(filepath.Join(dir, "foreign"), []byte("keep"), 0o600)
			case "extra-directory":
				err = os.Mkdir(filepath.Join(dir, "foreign"), 0o700)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Materialize(cache); err == nil {
				t.Fatal("corrupt cache was accepted")
			}
			if kind == "changed" {
				data, err := os.ReadFile(file)
				if err != nil || string(data) != "foreign content" {
					t.Fatalf("foreign content overwritten: %q %v", data, err)
				}
			}
		})
	}
}

func TestBundleRefusesSymlinkedCacheChild(t *testing.T) {
	cache, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(cache, "soho")); err != nil {
		// Native Windows symlinks need an OS permission; this is a platform
		// capability result, never permission to weaken the production check.
		t.Skipf("native symlink creation denied: %v", err)
	}
	if _, err := Materialize(cache); err == nil {
		t.Fatal("symlinked cache child accepted")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("wrote outside cache: %v %v", entries, err)
	}
}

func TestBundleShipsTheValidationTemplateWithItsHeadings(t *testing.T) {
	// DJA-695: templates/VALIDATION.md is part of the bundle and keeps the
	// section order and the exact headings the delivery contract defines.
	data, err := resources.ReadFile("templates/VALIDATION.md")
	if err != nil {
		t.Fatalf("bundle is missing templates/VALIDATION.md: %v", err)
	}
	lines := strings.Split(string(data), "\n")
	headings := []string{"# Validation", "## Scope", "## Preconditions", "## Test layers", "## Items", "## Evidence", "## Results", "## What this does not prove"}
	next := -1
	for _, heading := range headings {
		i := -1
		for j, line := range lines {
			if j > next && line == heading {
				i = j
				break
			}
		}
		if i < 0 {
			t.Fatalf("template lost the heading %q (or lost it out of order)", heading)
		}
		next = i
	}
	for _, layer := range []string{"- unit:", "- integration:", "- contract:", "- e2e:", "- mutation:"} {
		if !strings.Contains(string(data), layer) {
			t.Fatalf("template lost the %s layer line", layer)
		}
	}
}

func TestCriticalImplementerKeepsTheImplementerBlocks(t *testing.T) {
	// DJA-686: the shipped critical-implementer keeps the implementer's
	// blocks byte-for-byte (the test fails if they drift) and adds the
	// critical self-review; it names no project, machine path, team or model.
	base := roleBody(t, "implementer.md")
	crit := roleBody(t, "critical-implementer.md")
	if !strings.Contains(crit, base) {
		t.Fatal("critical-implementer lost or altered an implementer block")
	}
	if !strings.Contains(crit, "<critical>") || !strings.Contains(crit, "</critical>") {
		t.Fatal("missing the critical self-review block")
	}
	for _, needle := range []string{"interleaves", "idempotency", "swallowed", "The red test existed before the fix"} {
		if !strings.Contains(crit, needle) {
			t.Fatalf("critical block missing %q", needle)
		}
	}
	for _, banned := range []string{"Skedly", "Kashes", "zommehq", "/Users/", "gpt-"} {
		if strings.Contains(crit, banned) {
			t.Fatalf("critical-implementer names %q (machine- or team-specific)", banned)
		}
	}
}

// roleBody returns the markdown body of a shipped role file (everything
// after its closing frontmatter ---).
func roleBody(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("roles", name))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 || lines[0] != "---" {
		t.Fatalf("%s: missing frontmatter", name)
	}
	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" {
			return strings.Join(lines[i+1:], "\n")
		}
	}
	t.Fatalf("%s: unterminated frontmatter", name)
	return ""
}

func TestBundleConcurrentPublishCleansOwnedStages(t *testing.T) {
	cache := t.TempDir()
	var wg sync.WaitGroup
	results := make(chan string, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dir, err := Materialize(cache)
			if err != nil {
				t.Errorf("concurrent materialize: %v", err)
			}
			results <- dir
		}()
	}
	wg.Wait()
	close(results)
	var want string
	for dir := range results {
		if want == "" {
			want = dir
		} else if dir != want {
			t.Errorf("different published paths: %q / %q", dir, want)
		}
	}
	entries, err := os.ReadDir(filepath.Join(cache, "soho", "bundled"))
	if err != nil || len(entries) != 1 || strings.HasPrefix(entries[0].Name(), ".staging-") {
		t.Fatalf("left owned staging directories: %v %v", entries, err)
	}
}

func TestBundleInvalidCacheDoesNotWrite(t *testing.T) {
	for _, path := range []string{"", "relative-cache"} {
		if _, err := Materialize(path); err == nil {
			t.Fatalf("accepted invalid cache %q", path)
		}
	}
}

func TestBundleCachedIsStrictlyReadOnly(t *testing.T) {
	t.Run("a missing cache or bundle is a not-exist error", func(t *testing.T) {
		cache := t.TempDir()
		if _, err := Cached(filepath.Join(cache, "missing")); !os.IsNotExist(err) {
			t.Fatalf("missing cache: err=%v; want os.IsNotExist", err)
		}
		if _, err := Cached(cache); !os.IsNotExist(err) {
			t.Fatalf("cache without a bundle: err=%v; want os.IsNotExist", err)
		}
	})
	t.Run("a completed bundle is found without creating or repairing anything", func(t *testing.T) {
		cache := t.TempDir()
		dir, err := Materialize(cache)
		if err != nil {
			t.Fatal(err)
		}
		got, err := Cached(cache)
		if err != nil || got != dir {
			t.Fatalf("Cached() = %q %v; want %q", got, err, dir)
		}
		// The lookup must not write: with the cache read-only it still
		// succeeds, while any mkdir/write/repair would fail.
		if err := os.Chmod(cache, 0o500); err != nil {
			t.Skip("the cache cannot be made read-only on this platform")
		}
		t.Cleanup(func() { _ = os.Chmod(cache, 0o700) })
		if _, err := Cached(cache); err != nil {
			t.Fatalf("Cached on a read-only cache: %v; the lookup must not write", err)
		}
	})
	t.Run("corrupt content fails closed without repair", func(t *testing.T) {
		cache := t.TempDir()
		dir, err := Materialize(cache)
		if err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(dir, "config.defaults")
		if err := os.WriteFile(file, []byte("foreign"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Cached(cache); err == nil || os.IsNotExist(err) {
			t.Fatalf("corrupt cache: err=%v; want a fail-closed error, not not-exist", err)
		}
		data, err := os.ReadFile(file)
		if err != nil || string(data) != "foreign" {
			t.Fatalf("Cached repaired the cache: %q %v", data, err)
		}
	})
	t.Run("a symlinked bundle is refused", func(t *testing.T) {
		cache, outside := t.TempDir(), t.TempDir()
		good, err := Materialize(outside)
		if err != nil {
			t.Fatal(err)
		}
		_, digest, err := bundledFiles()
		if err != nil {
			t.Fatal(err)
		}
		parent := filepath.Join(cache, "soho", "bundled")
		if err := os.MkdirAll(parent, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(good, filepath.Join(parent, digest)); err != nil {
			// Native Windows symlinks need an OS permission; this is a
			// platform capability result, never permission to weaken the check.
			t.Skipf("native symlink creation denied: %v", err)
		}
		if _, err := Cached(cache); err == nil {
			t.Fatal("symlinked bundle accepted")
		}
	})
}

// DJA-693: the shipped mcp choices of the roles. ui-reviewer,
// sub-orchestrator and planner carry mcp: none; designer carries the
// open-pencil list and inspector the two browser servers. job-orchestrator
// keeps inheriting (it reads memory only when the brief lists the scopes).
func TestShippedRolesCarryTheMcpChoice(t *testing.T) {
	want := map[string]string{
		"ui-reviewer":      "none",
		"sub-orchestrator": "none",
		"planner":          "none",
		"designer":         "open-pencil",
		"inspector":        "playwright,chrome-devtools",
	}
	for role, value := range want {
		if got := roleFm(t, role+".md", "mcp"); got != value {
			t.Fatalf("%s mcp frontmatter=%q, want %q", role, got, value)
		}
	}
	if got := roleFm(t, "job-orchestrator.md", "mcp"); got != "" {
		t.Fatalf("job-orchestrator gained a mcp key %q, want none (it inherits)", got)
	}
}

// roleFm returns one frontmatter key of a shipped role file ("" when absent).
func roleFm(t *testing.T, file, key string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("roles", file))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 || lines[0] != "---" {
		t.Fatalf("%s: missing frontmatter", file)
	}
	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" {
			return ""
		}
		if line, ok := strings.CutPrefix(lines[i], key+": "); ok {
			return strings.TrimSpace(line)
		}
	}
	t.Fatalf("%s: unterminated frontmatter", file)
	return ""
}
