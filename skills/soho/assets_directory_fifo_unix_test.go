//go:build unix

package skill

import (
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"
)

// awaitDirectoryRefusal owns the FIFO unblock and always joins its reader
// before reporting a blocked implementation. The writer is opened only
// after the read bound, so a blocking open cannot masquerade as a control.
func awaitDirectoryRefusal(t *testing.T, target string, fn func() error) (error, bool) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		return err, false
	case <-time.After(250 * time.Millisecond):
		writer, err := os.OpenFile(target, os.O_RDWR|syscall.O_NONBLOCK, 0)
		if err != nil {
			t.Errorf("owned FIFO unblock: %v", err)
			return <-done, true
		}
		defer writer.Close()
		return <-done, true
	}
}

func TestBundleCachedDirectoryFIFOs(t *testing.T) {
	for _, level := range []string{"soho", "bundled", "digest", "roles"} {
		t.Run(level, func(t *testing.T) {
			cache, bundle := verifiedCacheFixture(t)
			target := publishedDirectory(cache, bundle, level)
			if err := os.Rename(target, filepath.Join(cache, "parked-directory")); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Mkfifo(target, 0600); err != nil {
				t.Skipf("native FIFO unavailable: %v", err)
			}
			before := publishedSnapshot(t, cache)
			err, blocked := awaitDirectoryRefusal(t, target, func() error { _, err := Cached(cache); return err })
			if err == nil || blocked {
				t.Errorf("directory FIFO %s: blocked=%v err=%v", level, blocked, err)
			}
			if !reflect.DeepEqual(before, publishedSnapshot(t, cache)) {
				t.Fatal("Cached changed the FIFO fixture")
			}
		})
	}
}

func TestBundleVerifiedDirectoryFIFOAfterCheck(t *testing.T) {
	for _, level := range []string{"soho", "bundled", "digest", "roles"} {
		t.Run(level, func(t *testing.T) {
			cache, bundle := verifiedCacheFixture(t)
			target := publishedDirectory(cache, bundle, level)
			root, err := os.OpenRoot(cache)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			files, digest, err := bundledFiles()
			if err != nil {
				t.Fatal(err)
			}
			changed := false
			opener := func(parent *os.Root, name string) (*os.Root, error) {
				if !changed && name == filepath.Base(target) {
					if err := os.Rename(target, filepath.Join(cache, "parked-directory")); err != nil {
						return nil, err
					}
					if err := syscall.Mkfifo(target, 0600); err != nil {
						return nil, err
					}
					changed = true
				}
				return openBundleDirectory(parent, name)
			}
			err, blocked := awaitDirectoryRefusal(t, target, func() error {
				return verifyPublishedUsing(root, filepath.Join("soho", "bundled", digest), files, bundleTraversal{open: opener})
			})
			if !changed || err == nil || blocked {
				t.Fatalf("FIFO after plain-directory check %s: scheduled=%v blocked=%v err=%v", level, changed, blocked, err)
			}
			if info, err := os.Lstat(target); err != nil || info.Mode()&os.ModeNamedPipe == 0 {
				t.Fatalf("the FIFO was changed: %v", err)
			}
		})
	}
}
