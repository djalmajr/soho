package core

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/platform"
)

// UnregisteredCopy is a mutation directory absent from the supplied registry.
type UnregisteredCopy struct {
	Path string
	Age  time.Duration
}

// UnregisteredCopies shares gc's read-only directory and registry scan.
func UnregisteredCopies(dirs []string, rows []CopyRow, now time.Time, threshold time.Duration) []UnregisteredCopy {
	registered := map[string]bool{}
	for _, row := range rows {
		registered[row.Path] = true
		if resolved, err := filepath.EvalSymlinks(row.Path); err == nil {
			registered[resolved] = true
		}
	}
	var out []UnregisteredCopy
	VisitCopyScanEntries(dirs, func(path, resolved string, info os.FileInfo) bool {
		if registered[resolved] {
			return true
		}
		age := now.Sub(info.ModTime())
		if age <= threshold {
			return true
		}
		out = append(out, UnregisteredCopy{Path: path, Age: age})
		return true
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// VisitCopyScanEntries visits direct soho-mutation-* entries that resolve
// to directories. The callback receives the scanned path, resolved path
// and target metadata. Returning false stops the scan.
func VisitCopyScanEntries(dirs []string, visit func(path, resolved string, info os.FileInfo) bool) {
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !strings.HasPrefix(entry.Name(), "soho-mutation-") {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			info, err := os.Stat(path)
			if err != nil || !info.IsDir() {
				continue
			}
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				continue
			}
			if !visit(path, resolved, info) {
				return
			}
		}
	}
}

// ReadCopyOriginMarker validates the marker used by gc and doctor.
func ReadCopyOriginMarker(markerPath string) (project, source string, ok bool) {
	info, err := os.Lstat(markerPath)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", "", false
	}
	data, err := os.ReadFile(markerPath)
	if err != nil {
		return "", "", false
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != 3 {
		return "", "", false
	}
	project, p := strings.CutPrefix(lines[0], "project=")
	source, q := strings.CutPrefix(lines[1], "source=")
	_, r := strings.CutPrefix(lines[2], "created=")
	if !p || !q || !r || project == "" || source == "" {
		return "", "", false
	}
	return project, source, true
}

// CopiesScanDirs returns gc's deduplicated temporary and configured roots.
func CopiesScanDirs(env platform.Env, cwd string) []string {
	tmp := env.Get("TMPDIR")
	if tmp == "" {
		tmp = os.TempDir()
	}
	dirs := []string{filepath.Clean(tmp)}
	ctx := LoadConfig(env, cwd)
	configured, set, err := CopiesParent(&ctx, env, cwd)
	if err != nil || !set {
		return dirs
	}
	configured = filepath.Clean(configured)
	if configured == dirs[0] {
		return dirs
	}
	if a, e := os.Stat(dirs[0]); e == nil {
		if b, e := os.Stat(configured); e == nil && os.SameFile(a, b) {
			return dirs
		}
	}
	return append(dirs, configured)
}
