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
