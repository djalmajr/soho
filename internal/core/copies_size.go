package core

import (
	"context"
	"github.com/djalmajr/soho/internal/platform"
	"io/fs"
	"path/filepath"
)

// CopiesDirSize counts logical file sizes without following symlinks; cancellation marks the result unknown.

func CopiesDirSize(ctx context.Context, root string) (int64, bool) {
	var total int64
	seen := map[[2]uint64]bool{}
	err := filepath.WalkDir(root, func(_ string, entry fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil || entry.Type()&fs.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return nil
		}
		if info, e := entry.Info(); e == nil {
			if key, linked := platform.HardlinkIdentity(info); linked {
				if seen[key] {
					return nil
				}
				seen[key] = true
			}
			total += info.Size()
		}
		return nil
	})
	return total, err == nil && ctx.Err() == nil
}
