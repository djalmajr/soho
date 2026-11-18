package cli

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

// copyIdentityInside requires the held path itself to exist, then compares
// each existing ancestor with the target by filesystem identity.
func copyIdentityInside(target fs.FileInfo, held string) bool {
	if _, err := os.Stat(held); err != nil {
		return false
	}
	for p := filepath.Clean(held); ; p = filepath.Dir(p) {
		info, err := os.Stat(p)
		if err != nil {
			return false
		}
		if os.SameFile(target, info) {
			return true
		}
		if p == filepath.Dir(p) {
			return false
		}
	}
}

// copyGuardRoot maps a configured scan root to its resolved spelling once.
// Registered paths outside those roots are checked from the filesystem root.
// When roots overlap (a copies_dir link inside TMPDIR), the most specific
// root that holds the path wins, so a configured root's own link is never
// judged as an ancestor below a wider root.
func copyGuardRoot(path string, env platform.Env, cwd string) (string, string) {
	bestRoot, bestPath, bestLen := "", "", -1
	for _, raw := range core.CopiesScanDirs(env, cwd) {
		resolved, err := filepath.EvalSymlinks(raw)
		if err != nil {
			continue
		}
		for _, base := range []string{filepath.Clean(raw), resolved} {
			rel, err := filepath.Rel(base, path)
			if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && len(base) > bestLen {
				bestRoot, bestPath, bestLen = resolved, filepath.Join(resolved, rel), len(base)
			}
		}
	}
	if bestLen >= 0 {
		return bestRoot, bestPath
	}
	// Resolving system aliases above the scan root is necessary for legacy
	// registered paths. Other ancestor links remain visible below the root.
	p := filepath.Clean(path)
	for _, alias := range []string{"/var", "/tmp"} {
		if p == alias || strings.HasPrefix(p, alias+string(filepath.Separator)) {
			if resolved, err := filepath.EvalSymlinks(alias); err == nil && resolved == "/private"+alias {
				p = resolved + strings.TrimPrefix(p, alias)
				break
			}
		}
	}
	return filepath.VolumeName(p) + string(filepath.Separator), p
}

func copyAncestorGuard(path string, env platform.Env, cwd string) string {
	root, p := copyGuardRoot(path, env, cwd)
	if p == root {
		root = filepath.VolumeName(p) + string(filepath.Separator)
	}
	for p = filepath.Dir(p); p != root; p = filepath.Dir(p) {
		if p == filepath.Dir(p) {
			return "cannot verify the copy root"
		}
		info, err := os.Lstat(p)
		if err != nil {
			return "cannot verify the copy path"
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Sprintf("an ancestor is a symbolic link (%s)", p)
		}
	}
	return ""
}

// copyDeletionGuard runs after main's path check and before a destructive
// action. Registry read errors are distinct from ordinary refusals.
func copyDeletionGuard(path, owner, sd string, env platform.Env, cwd string) (string, error) {
	if cause := copyAncestorGuard(path, env, cwd); cause != "" {
		return cause, nil
	}
	target, err := os.Stat(path)
	if err != nil {
		return "cannot verify the copy path", nil
	}
	dirs := []string{sd}
	if hint := registryDestination(env); hint != "" {
		a, ae := os.Stat(sd)
		b, be := os.Stat(hint)
		if filepath.Clean(sd) != filepath.Clean(hint) && (ae != nil || be != nil || !os.SameFile(a, b)) {
			dirs = append(dirs, hint)
		}
	}
	for _, dir := range dirs {
		rows, err := core.ReadCopies(dir)
		if err != nil {
			return "", fmt.Errorf("cannot read copy registry %s (%v); fix it and retry", core.CopiesFile(dir), err)
		}
		for _, row := range rows {
			if row.Owner != owner && copyIdentityInside(target, row.Path) {
				held, err := os.Stat(row.Path)
				if err == nil && !os.SameFile(target, held) {
					return fmt.Sprintf("holds %s registered by %s", row.Path, row.Owner), nil
				}
			}
		}
		snapshots, err := readReviewSnapshots(dir)
		if err != nil {
			return "", fmt.Errorf("cannot read snapshot registry %s (%v); fix it and retry", filepath.Join(dir, "snapshots.tsv"), err)
		}
		for _, row := range snapshots {
			for _, held := range []string{row[1], row[3]} {
				if copyIdentityInside(target, held) {
					return fmt.Sprintf("holds %s registered by %s", held, row[0]), nil
				}
			}
		}
	}
	return "", nil
}
