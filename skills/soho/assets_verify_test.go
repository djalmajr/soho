package skill

// Bounded existing-cache discovery regressions (DJA-611 F2): the shared
// Cached/verifyPublished dependency must refuse malformed, oversized,
// unknown, symlinked or FIFO content with bounded nonblocking regular
// handle reads, known embedded resource lengths and digest checks — never
// an unbounded body read or an unbounded enumeration of the published
// tree.

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
)

// TestBundleCachedRefusesOversizedResourceBeforeReadingBody: a published
// cache whose config.defaults is a raw 16MiB file must be refused without
// allocating or reading the oversized body: the known embedded resource
// length is checked before any body allocation or consumption.
func TestBundleCachedRefusesOversizedResourceBeforeReadingBody(t *testing.T) {
	cache := t.TempDir()
	dir, err := Materialize(cache)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.defaults"), make([]byte, 16<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	got, err := Cached(cache)
	runtime.ReadMemStats(&m1)
	if err == nil || os.IsNotExist(err) || got != "" {
		t.Fatalf("oversized cache: %q %v; want a fail-closed refusal, not a not-exist error", got, err)
	}
	allocated := m1.TotalAlloc - m0.TotalAlloc
	if allocated >= 4<<20 {
		t.Fatalf("Cached allocated %d bytes for the oversized cache; the 16MiB body must not be read", allocated)
	}
	data, err := os.ReadFile(filepath.Join(dir, "config.defaults"))
	if err != nil || len(data) != 16<<20 {
		t.Fatalf("the oversized fixture changed: %v %d", err, len(data))
	}
}

// TestBundleCachedRefusesUnknownEntriesBounded: entries beyond the known
// resource tree are still refused (the old unknown-entry behavior is
// kept) by a directory read bounded to the known entry count plus one,
// not by an unbounded enumeration of attacker-added entries.
func TestBundleCachedRefusesUnknownEntriesBounded(t *testing.T) {
	cache := t.TempDir()
	dir, err := Materialize(cache)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 512; i++ {
		if err := os.WriteFile(filepath.Join(dir, "junk-"+strconv.Itoa(i)), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Cached(cache); err == nil {
		t.Fatal("unknown entries beyond the known tree were accepted")
	}
}

// TestBundleCachedRefusesSameLengthForeignBytes: a resource of exactly the
// known length with foreign bytes passes the length check and must be
// refused by the bounded read plus digest verification.
func TestBundleCachedRefusesSameLengthForeignBytes(t *testing.T) {
	cache := t.TempDir()
	dir, err := Materialize(cache)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "config.defaults")
	data, err := os.ReadFile(file)
	if err != nil || len(data) < 1 {
		t.Fatal(err)
	}
	foreign := make([]byte, len(data))
	copy(foreign, data)
	foreign[0] ^= 1
	if err := os.WriteFile(file, foreign, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Cached(cache); err == nil {
		t.Fatal("same-length foreign bytes were accepted; the digest check must refuse")
	}
	if got, err := os.ReadFile(file); err != nil || !bytesEqual(got, foreign) {
		t.Fatalf("Cached repaired the cache: %q %v", got, err)
	}
}

// TestBundleCachedRefusesSymlinkedResource: a known resource replaced by
// a symlink (even to same-length content) is refused by the entry's
// non-symlink regular check, never followed.
func TestBundleCachedRefusesSymlinkedResource(t *testing.T) {
	cache, outside := t.TempDir(), t.TempDir()
	dir, err := Materialize(cache)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "config.defaults")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "config.defaults"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "config.defaults"), file); err != nil {
		t.Skipf("native symlink creation denied: %v", err)
	}
	if _, err := Cached(cache); err == nil {
		t.Fatal("a symlinked resource was accepted")
	}
	if got, err := os.ReadFile(filepath.Join(outside, "config.defaults")); err != nil || !bytesEqual(got, data) {
		t.Fatalf("the symlink target changed: %q %v", got, err)
	}
}

func bytesEqual(a, b []byte) bool {
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
