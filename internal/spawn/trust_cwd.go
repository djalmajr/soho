package spawn

import (
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/djalmajr/soho/internal/platform"
)

// trustRoot uses only Git's common directory, never the worktree top-level.
func trustRoot(cwd string) (string, string) {
	env := platform.Env{}
	for _, pair := range os.Environ() {
		k, v, _ := strings.Cut(pair, "=")
		env[k] = v
	}
	return trustRootEnv(cwd, env, trustOwned)
}

func trustRootEnv(cwd string, env platform.Env, owned func(os.FileInfo) bool) (string, string) {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return "", "cannot resolve cwd"
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return "", "cannot resolve cwd"
	}
	gitEnv := env.Clone()
	for key := range gitEnv {
		switch strings.ToUpper(key) {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_DISCOVERY_ACROSS_FILESYSTEM", "GIT_CEILING_DIRECTORIES":
			delete(gitEnv, key)
		}
	}
	result := platform.RunCli("git", []string{"-C", abs, "rev-parse", "--path-format=absolute", "--git-common-dir"}, platform.RunOptions{Env: gitEnv, TimeoutMs: 5000, Cwd: abs})
	if result.NotFound || result.Status == nil || *result.Status != 0 {
		return "", "cannot resolve repository common directory"
	}
	// Remove only Git's line terminator; whitespace can belong to a path.
	common := strings.TrimSuffix(strings.TrimSuffix(result.Stdout, "\n"), "\r")
	if !filepath.IsAbs(common) {
		return "", "repository common directory is not absolute"
	}
	common, err = filepath.EvalSymlinks(common)
	if err != nil {
		return "", "cannot resolve repository common directory"
	}
	st, err := os.Stat(common)
	if err != nil || !st.IsDir() || filepath.Base(common) != ".git" {
		return "", "unsupported repository layout"
	}
	bare := platform.RunCli("git", []string{"-C", abs, "rev-parse", "--is-bare-repository"}, platform.RunOptions{Env: gitEnv, TimeoutMs: 5000, Cwd: abs})
	if bare.Status == nil || *bare.Status != 0 || strings.TrimSpace(bare.Stdout) != "false" {
		return "", "unsupported repository layout"
	}
	root := filepath.Dir(common)
	return trustRootPath(root, env, owned)
}

func trustRootPath(root string, env platform.Env, owned func(os.FileInfo) bool) (string, string) {
	if filepath.Dir(root) == root {
		return "", "repository root is a volume root"
	}
	for _, r := range root {
		if unicode.IsControl(r) {
			return "", "repository root contains control characters"
		}
	}
	home := env.Get("HOME")
	if home == "" {
		home = env.Get("USERPROFILE")
	}
	home, err := filepath.EvalSymlinks(home)
	if err != nil || !filepath.IsAbs(home) {
		return "", "cannot resolve home directory"
	}
	if trustWithin(root, home) {
		return "", "repository root is home or an ancestor of home"
	}
	jobs := filepath.Join(home, ".herdr-jobs")
	if resolved, err := filepath.EvalSymlinks(jobs); err == nil {
		jobs = resolved
	}
	if trustWithin(jobs, root) {
		return "", "repository root is inside the jobs directory"
	}
	st, err := os.Stat(root)
	if err != nil || !owned(st) {
		return "", "repository root is not owned by the current user"
	}
	return root, ""
}

func trustWithin(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func trustCwdArgument(root string) string {
	key := "'" + root + "'"
	if strings.Contains(root, "'") {
		key = `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(root) + `"`
	}
	return "projects={" + key + `={trust_level="trusted"}}`
}
