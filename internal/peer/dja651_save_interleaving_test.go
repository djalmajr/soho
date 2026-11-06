package peer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

func TestDJA651ScreenDirectoryReplacedInsideSave(t *testing.T) {
	for _, stage := range []string{"before-chmod", "before-rename"} {
		t.Run(stage, func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			wait := filepath.Join(root, "wait")
			if e := os.Mkdir(wait, 0700); e != nil {
				t.Fatal(e)
			}
			marker := filepath.Join(outside, "sentinel")
			if e := os.WriteFile(marker, []byte("keep"), 0600); e != nil {
				t.Fatal(e)
			}
			before, e := os.Stat(outside)
			if e != nil {
				t.Fatal(e)
			}
			got := savePrivateScreen(root, "01020304", "private screen\n", platform.Env{}, func(s string) {
				if s != stage {
					return
				}
				if e := os.Rename(wait, filepath.Join(root, "owned-original")); e != nil {
					t.Fatal(e)
				}
				if e := os.Symlink(outside, wait); e != nil {
					t.Skipf("native symlink unavailable: %v", e)
				}
			})
			if got != "" {
				t.Errorf("replaced diagnostic directory reported an artifact: %s", got)
			}
			if entries, e := os.ReadDir(outside); e != nil || len(entries) != 1 || entries[0].Name() != "sentinel" {
				t.Errorf("outside write: %v %v", entries, e)
			}
			if raw, e := os.ReadFile(marker); e != nil || string(raw) != "keep" {
				t.Errorf("sentinel changed: %q %v", raw, e)
			}
			after, e := os.Stat(outside)
			if e != nil || after.Mode() != before.Mode() {
				t.Errorf("outside chmod: before=%v after=%v err=%v", before, after, e)
			}
		})
	}
}
