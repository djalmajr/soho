//go:build unix

package transcript

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/platform"
)

func TestOMPReceiptSuffix(t *testing.T) {
	const marker = "/briefs/worker.md"
	user := `{"type":"message","message":{"role":"user","content":[{"type":"text","text":"/briefs/worker.md"}]}}` + "\n"
	metadata := `{"type":"model_usage","completion":"/briefs/worker.md"}` + "\n"
	for _, tc := range []struct {
		name, suffix string
		want         int
		ok           bool
	}{
		{"baseline only", "", 0, true},
		{"one user and metadata", metadata + user, 1, true},
		{"incomplete user line", strings.TrimSuffix(user, "\n"), 0, false},
		{"duplicate user lines", user + user, 2, true},
		{"malformed record with user line", "{broken}\n" + user, 0, false},
		{"oversize unrelated line with user line", strings.Repeat(" ", ompReceiptMaxBytes) + metadata + user, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session.jsonl")
			if err := os.WriteFile(path, []byte(user), 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			r, ok := OpenOMPReceipt(ctx, path)
			if !ok || r.offset != int64(len(user)) {
				t.Fatalf("baseline offset=%d ok=%v", r.offset, ok)
			}
			f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
			if err != nil {
				t.Fatal(err)
			}
			_, writeErr := f.WriteString(tc.suffix)
			closeErr := f.Close()
			if writeErr != nil || closeErr != nil {
				t.Fatalf("append=%v close=%v", writeErr, closeErr)
			}
			count, ok := r.Count(ctx, marker, nil)
			if ok != tc.ok || (ok && count != tc.want) {
				t.Fatalf("count=%d ok=%v, want %d/%v", count, ok, tc.want, tc.ok)
			}
		})
	}
	t.Run("incomplete baseline", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "session.jsonl")
		if err := os.WriteFile(path, []byte(strings.TrimSuffix(user, "\n")), 0o600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if _, ok := OpenOMPReceipt(ctx, path); ok {
			t.Fatal("incomplete baseline armed a receipt")
		}
	})
}

func TestOMPReceiptOpenSwap(t *testing.T) {
	for _, tc := range []struct {
		name    string
		replace func(path, saved string) error
	}{
		{"different regular inode", func(path, _ string) error { return os.WriteFile(path, []byte("other\n"), 0o600) }},
		{"symlink to original inode", func(path, saved string) error { return os.Symlink(saved, path) }},
		{"fifo", func(path, _ string) error { return syscall.Mkfifo(path, 0o600) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path, saved := filepath.Join(dir, "journal"), filepath.Join(dir, "saved")
			if err := os.WriteFile(path, []byte("original\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			expected, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			file, _ := openOMPJournal(t.Context(), path, expected, func(path string) (*os.File, error) {
				if err := os.Rename(path, saved); err != nil {
					t.Fatal(err)
				}
				if err := tc.replace(path, saved); err != nil {
					t.Fatal(err)
				}
				f, err := platform.OpenRegularFileNoFollow(path)
				if removeErr := os.Remove(path); removeErr != nil {
					t.Fatal(removeErr)
				}
				if restoreErr := os.Rename(saved, path); restoreErr != nil {
					t.Fatal(restoreErr)
				}
				return f, err
			})
			if file != nil {
				_ = file.Close()
				t.Fatal("reviewed a descriptor opened through the replaced path")
			}
		})
	}
}
