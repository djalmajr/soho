//go:build windows

package sessionmeta

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

// TestWindowsReparseContainment plants a reparse point at the session
// name: the confined resolution must refuse it, leave the outside target
// untouched, and the writer must not initialize through it.
func TestWindowsReparseContainment(t *testing.T) {
	f := newFS(t, "s1")
	outside := filepath.Join(f.raw, "outside")
	if err := os.MkdirAll(outside, 0o750); err != nil {
		t.Fatalf("the outside dir cannot be made: %v", err)
	}
	real := filepath.Join(f.base, "s1")
	if err := os.Rename(real, real+"-real"); err != nil {
		t.Fatalf("the session cannot be moved aside: %v", err)
	}
	defer os.Rename(real+"-real", real)
	if err := os.Symlink(outside, real); err != nil {
		t.Skipf("reparse points unsupported here: %v", err)
	}
	st := f.st
	if rec, ok, err := st.Read(); err == nil {
		t.Fatalf("reparse session read got %+v ok=%v, want an error", rec, ok)
	}
	if _, err := st.Register(Record{Schema: Schema, ProjectRoot: "/p", SessionID: "s1", Terminal: "t", Name: "n", Kind: "k", StartedMS: 1}); err == nil {
		t.Fatalf("reparse session register, want an error")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("the outside target changed: %v %v", entries, err)
	}
}

// TestWindowsOpenReaderReplacement registers and sets the task while a
// confined reader holds the record open: os.Root.OpenFile requests
// FILE_SHARE_DELETE, so the handle-relative POSIX-semantics replace must
// succeed under the reader. The same-terminal Register keeps the
// preserved task, the matching SetTask transaction produces the new task
// bytes, and the old reader must keep seeing the bytes it opened.
func TestWindowsOpenReaderReplacement(t *testing.T) {
	f := newFS(t, "s1")
	rec := Record{Schema: Schema, ProjectRoot: "/p", SessionID: "s1", Terminal: "t1", Name: "n", Kind: "k", StartedMS: 1, Task: "old"}
	if _, err := f.st.Register(rec); err != nil {
		t.Fatalf("initial register: %v", err)
	}
	root, err := os.OpenRoot(filepath.Dir(f.recPath()))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	fh, err := root.OpenFile(RecordName, os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("the record cannot be opened as a reader: %v", err)
	}
	defer fh.Close()
	// A same-terminal Register under the open reader keeps the task rule.
	if got, err := f.st.Register(rec); err != nil || got.Task != "old" {
		t.Fatalf("same-terminal register under an open reader got %+v err=%v, want the preserved task rule", got, err)
	}
	// The matching SetTask transaction changes the task under the reader.
	if ok, err := f.st.SetTask("/p", "t1", "new"); err != nil || !ok {
		t.Fatalf("set task under an open reader ok=%v err=%v, want true nil", ok, err)
	}
	next := rec
	next.Task = "new"
	data, err := os.ReadFile(f.recPath())
	if err != nil || string(data) != wantJSON(next) {
		t.Fatalf("the replaced record bytes = %q err=%v, want %q", data, err, wantJSON(next))
	}
	kept, err := io.ReadAll(fh)
	if err != nil || string(kept) != wantJSON(rec) {
		t.Fatalf("the open reader sees %q err=%v, want the original bytes %q", kept, err, wantJSON(rec))
	}
}

func TestWindowsUnsharedReaderRefusesPublication(t *testing.T) {
	f := newFS(t, "s1")
	rec := Record{Schema: Schema, ProjectRoot: "/p", SessionID: "s1", Terminal: "t1", Name: "n", Kind: "k", StartedMS: 1, Task: "old"}
	if _, err := f.st.Register(rec); err != nil {
		t.Fatal(err)
	}
	reader, err := os.Open(f.recPath()) // The ordinary open omits FILE_SHARE_DELETE.
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	if ok, err := f.st.SetTask("/p", "t1", "new"); err == nil || ok {
		t.Fatalf("nonsharing reader: ok=%v err=%v, want a failed publication", ok, err)
	}
	f.checkBytes("failed publication preserves record", rec)
	f.requireOnlyRecord("failed publication removes its temp and lock")
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if ok, err := f.st.SetTask("/p", "t1", "new"); err != nil || !ok {
		t.Fatalf("publication after reader closes: ok=%v err=%v", ok, err)
	}
	rec.Task = "new"
	f.checkBytes("publication after reader closes", rec)
}
