package sessionmeta

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCleanupLookupErrors(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("POSIX non-root permission-revocation probe")
	}
	t.Run("lock transaction", func(t *testing.T) {
		f := newFS(t, "s1")
		r, _, err := f.st.openSession()
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		dir := filepath.Join(f.base, f.sess)
		t.Cleanup(func() { _ = os.Chmod(dir, 0750) })
		rec := Record{Schema: 1, ProjectRoot: "/p", SessionID: "s1", Terminal: "t1", Name: "orch", Kind: "codex", StartedMS: 7, Task: "published"}
		err = withLockedRecord(r, func() error {
			data, err := marshalRecord(rec)
			if err != nil {
				return err
			}
			if err := publishRecord(r, data); err != nil {
				return err
			}
			if err := os.Chmod(dir, 0000); err != nil {
				return err
			}
			if _, lookup := r.Lstat(lockName); lookup == nil || os.IsNotExist(lookup) {
				t.Errorf("expected genuine lookup error, got %v", lookup)
			}
			return nil
		})
		if e := os.Chmod(dir, 0750); e != nil {
			t.Fatal(e)
		}
		_, lockErr := r.Lstat(lockName)
		got, ok, readErr := f.st.Read()
		if lockErr != nil || readErr != nil || !ok || got.Task != "published" {
			t.Fatal("fixture did not retain the own lock and committed record")
		}
		if err == nil {
			t.Fatal("transaction reported success despite an uncheckable unresolved own lock")
		}
	})
	t.Run("temp identity lookup", func(t *testing.T) {
		f := newFS(t, "s1")
		r, _, err := f.st.openSession()
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		dir := filepath.Join(f.base, f.sess)
		if err := r.WriteFile("owned.tmp", []byte("owned"), 0600); err != nil {
			t.Fatal(err)
		}
		saved, err := r.Lstat("owned.tmp")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0750) })
		if err := os.Chmod(dir, 0000); err != nil {
			t.Fatal(err)
		}
		if _, lookup := r.Lstat("owned.tmp"); lookup == nil || os.IsNotExist(lookup) {
			t.Fatalf("expected genuine lookup error, got %v", lookup)
		}
		cleanup := removeOwn(r, "owned.tmp", saved)
		if err := os.Chmod(dir, 0750); err != nil {
			t.Fatal(err)
		}
		data, err := r.ReadFile("owned.tmp")
		if err != nil || string(data) != "owned" {
			t.Fatal("own temp must remain unchanged when identity cannot be checked")
		}
		if cleanup == nil {
			t.Fatal("own temp identity-check failure must surface without deleting anything")
		}
	})
}
