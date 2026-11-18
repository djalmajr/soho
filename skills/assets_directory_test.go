package skill

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func verifiedCacheFixture(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cache := t.TempDir()
	bundle, err := Materialize(cache)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := Cached(cache); err != nil || got != bundle {
		t.Fatalf("valid Cached: %q %v", got, err)
	}
	return cache, bundle
}

func publishedDirectory(cache, bundle, level string) string {
	switch level {
	case "soho":
		return filepath.Join(cache, "soho")
	case "bundled":
		return filepath.Join(cache, "soho", "bundled")
	case "digest":
		return bundle
	default:
		return filepath.Join(bundle, "roles")
	}
}

func publishedSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		value := fmt.Sprintf("%v %d %d", info.Mode(), info.Size(), info.ModTime().UnixNano())
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			value += fmt.Sprintf(" %x", sha256.Sum256(data))
		} else if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			value += target
		}
		out[p] = value
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestBundleCachedDirectoryKinds(t *testing.T) {
	for _, level := range []string{"soho", "bundled", "digest", "roles"} {
		for _, kind := range []string{"link", "file"} {
			t.Run(level+"/"+kind, func(t *testing.T) {
				cache, bundle := verifiedCacheFixture(t)
				target := publishedDirectory(cache, bundle, level)
				parked := filepath.Join(cache, "parked-directory")
				if err := os.Rename(target, parked); err != nil {
					t.Fatal(err)
				}
				if kind == "link" {
					if err := os.Symlink(parked, target); err != nil {
						t.Skipf("native symlink unavailable: %v", err)
					}
				} else if err := os.WriteFile(target, []byte("not a directory"), 0600); err != nil {
					t.Fatal(err)
				}
				before := publishedSnapshot(t, cache)
				if got, err := Cached(cache); err == nil || got != "" {
					t.Fatalf("%s %s accepted: %q %v", level, kind, got, err)
				}
				if !reflect.DeepEqual(before, publishedSnapshot(t, cache)) {
					t.Fatal("Cached modified the refused fixture")
				}
			})
		}
	}
}

func TestBundleVerifiedDirectoryReplacement(t *testing.T) {
	for _, level := range []string{"soho", "bundled", "digest", "roles"} {
		for _, window := range []string{"before-open", "after-open"} {
			t.Run(level+"/"+window, func(t *testing.T) {
				cache, bundle := verifiedCacheFixture(t)
				other := t.TempDir()
				replacement, err := Materialize(other)
				if err != nil {
					t.Fatal(err)
				}
				target := publishedDirectory(cache, bundle, level)
				source := publishedDirectory(other, replacement, level)
				files, digest, err := bundledFiles()
				if err != nil {
					t.Fatal(err)
				}
				root, err := os.OpenRoot(cache)
				if err != nil {
					t.Fatal(err)
				}
				defer root.Close()
				changed := false
				var snapshot map[string]string
				swap := func() error {
					if err := os.Rename(target, filepath.Join(cache, "parked-directory")); err != nil {
						return err
					}
					if err := os.Rename(source, target); err != nil {
						return err
					}
					changed = true
					snapshot = publishedSnapshot(t, cache)
					return nil
				}
				opener := func(parent *os.Root, name string) (*os.Root, error) {
					if changed || name != filepath.Base(target) {
						return openBundleDirectory(parent, name)
					}
					if window == "before-open" {
						if err := swap(); err != nil {
							return nil, err
						}
						return openBundleDirectory(parent, name)
					}
					child, err := openBundleDirectory(parent, name)
					if err != nil {
						return nil, err
					}
					if err := swap(); err != nil {
						child.Close()
						return nil, err
					}
					return child, nil
				}
				err = verifyPublishedUsing(root, filepath.Join("soho", "bundled", digest), files, bundleTraversal{open: opener})
				if !changed || err == nil {
					t.Fatalf("replacement %s %s: scheduled=%v err=%v", level, window, changed, err)
				}
				if !reflect.DeepEqual(snapshot, publishedSnapshot(t, cache)) {
					t.Fatal("verifier modified replacement/original bytes")
				}
			})
		}
	}
}

func TestBundleVerifiedDirectoryRetention(t *testing.T) {
	for _, level := range []string{"soho", "bundled", "digest", "roles"} {
		t.Run(level, func(t *testing.T) {
			cache, bundle := verifiedCacheFixture(t)
			other := t.TempDir()
			replacement, err := Materialize(other)
			if err != nil {
				t.Fatal(err)
			}
			files, digest, err := bundledFiles()
			if err != nil {
				t.Fatal(err)
			}
			foreign := "config.defaults"
			if level == "roles" {
				for _, file := range files {
					if strings.HasPrefix(file.name, "roles/") {
						foreign = file.name
						break
					}
				}
			}
			data, err := os.ReadFile(filepath.Join(replacement, filepath.FromSlash(foreign)))
			if err != nil || len(data) == 0 {
				t.Fatalf("foreign fixture: %v", err)
			}
			data[0] ^= 1
			if err := os.WriteFile(filepath.Join(replacement, filepath.FromSlash(foreign)), data, 0600); err != nil {
				t.Fatal(err)
			}
			target := publishedDirectory(cache, bundle, level)
			label, err := filepath.Rel(cache, target)
			if err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(cache)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			changed := false
			var snapshot map[string]string
			traversal := bundleTraversal{open: openBundleDirectory, verified: func(got string) {
				if changed || got != filepath.ToSlash(label) {
					return
				}
				if err := os.Rename(target, filepath.Join(cache, "parked-directory")); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(publishedDirectory(other, replacement, level), target); err != nil {
					t.Fatal(err)
				}
				changed = true
				snapshot = publishedSnapshot(t, cache)
			}}
			if err := verifyPublishedUsing(root, filepath.Join("soho", "bundled", digest), files, traversal); err != nil || !changed {
				t.Fatalf("retained original %s: scheduled=%v err=%v", level, changed, err)
			}
			if !reflect.DeepEqual(snapshot, publishedSnapshot(t, cache)) {
				t.Fatal("verifier wrote the retained or foreign directory")
			}
		})
	}
}

func TestBundleCachedTrustedBaseAlias(t *testing.T) {
	cache, bundle := verifiedCacheFixture(t)
	alias := filepath.Join(t.TempDir(), "cache-alias")
	if err := os.Symlink(cache, alias); err != nil {
		t.Skipf("native base alias unavailable: %v", err)
	}
	before := publishedSnapshot(t, cache)
	rel, err := filepath.Rel(cache, bundle)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := Cached(alias); err != nil || got != filepath.Join(alias, rel) {
		t.Fatalf("trusted base alias: %q %v", got, err)
	}
	if !reflect.DeepEqual(before, publishedSnapshot(t, cache)) {
		t.Fatal("Cached wrote through the trusted alias")
	}
}
