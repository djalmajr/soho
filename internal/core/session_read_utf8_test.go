package core

// The source's byte-to-text boundary: raw invalid UTF-8 is refused with
// an ordinary error before any decoding (never repaired with
// replacement characters), and valid input keeps DecodeText's WHATWG
// decoding and CRLF normalization, through the actual
// ReadMetadata/ReadFile seam.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionSourceInvalidUTF8Refusal(t *testing.T) {
	base := t.TempDir()
	sd := filepath.Join(base, "s1")
	if err := os.MkdirAll(filepath.Join(sd, "wait"), 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(name string, data []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(sd, "wait", name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	src, err := OpenSessionSource(base, "s1", "")
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	// The literal invalid byte 0xff is refused at the boundary: an
	// ordinary error, never a repaired string with a replacement
	// character.
	write("worker.invalid", []byte{0x61, 0xff, 0x62})
	raw, err := src.ReadMetadata("wait", "worker.invalid")
	if err == nil || strings.ContainsRune(raw, '\uFFFD') {
		t.Fatalf("ReadMetadata = %q, %v; want the invalid-UTF-8 refusal, never the repaired string", raw, err)
	}
	// The ReadFile form refuses the same bytes.
	raw, err = src.ReadFile(64<<10, "wait", "worker.invalid")
	if err == nil || strings.ContainsRune(raw, '\uFFFD') {
		t.Fatalf("ReadFile = %q, %v; want the invalid-UTF-8 refusal", raw, err)
	}
	// Valid Unicode passes the same boundary unchanged.
	write("worker.unicode", []byte("caf\xc3\xa9 \xc3\xbcn\xc3\xafcode \xe2\x9c\x93\n"))
	got, err := src.ReadMetadata("wait", "worker.unicode")
	if err != nil || got != "café ünïcode ✓\n" {
		t.Fatalf("valid Unicode = %q, %v; want the bytes decoded unchanged", got, err)
	}
	// CRLF normalization stays with the decode for valid input.
	write("worker.crlf", []byte("line\r\n"))
	got, err = src.ReadMetadata("wait", "worker.crlf")
	if err != nil || got != "line\n" {
		t.Fatalf("CRLF = %q, %v; want the normalized line", got, err)
	}
}
