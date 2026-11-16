package dispatch

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/djalmajr/soho/internal/platform"
)

// RepositoryRelation is the result of comparing two Git common directories.
type RepositoryRelation uint8

const (
	RepositoryUnknown RepositoryRelation = iota
	RepositorySame
	RepositoryDifferent
)

// SameRepository distinguishes linked worktrees from separate repositories.
// A missing directory, unavailable Git, or failed Git lookup is unknown.
func SameRepository(env platform.Env, left, right string) RepositoryRelation {
	common := func(dir string) (string, bool) {
		if dir == "" {
			return "", false
		}
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			return "", false
		}
		result := ReferenceGit(env, "-C", dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if result.Status == nil || *result.Status != 0 || result.TimedOut || result.Error != "" {
			return "", false
		}
		path := strings.TrimSuffix(strings.TrimSuffix(result.Stdout, "\n"), "\r")
		if !filepath.IsAbs(path) {
			return "", false
		}
		return filepath.Clean(path), true
	}
	a, ok := common(left)
	if !ok {
		return RepositoryUnknown
	}
	b, ok := common(right)
	if !ok {
		return RepositoryUnknown
	}
	if SamePath(a, b, platform.Current()) {
		return RepositorySame
	}
	return RepositoryDifferent
}
