// Package reviewctx builds the read-only context pack a reviewer reads
// beside a committed diff. All repository access goes through GitRunner, so
// the pack can be tested against a fixture repository or a fake runner.
package reviewctx

import (
	"fmt"
	"strings"

	"github.com/djalmajr/soho/internal/gittree"
	"github.com/djalmajr/soho/internal/platform"
)

// CommandError is the Git failure a runner reports. Status 1 from git grep
// means "no matches" and is handled as an empty result by the pack; every
// other failure stops it.
type CommandError struct {
	Args   []string
	Status int
	Stderr string
}

func (e *CommandError) Error() string {
	return fmt.Sprintf("git %s: %s", strings.Join(e.Args, " "), e.Stderr)
}

// GitRunner runs one read-only Git command and can be rebound to the
// repository root discovered by Pack. Callers pass explicit revisions: no
// working tree or index content is read.
type GitRunner interface {
	Run(args ...string) (string, error)
	At(dir, gitDir string) GitRunner
}

// NewRunner returns the Git runner used by the command. It reuses the
// gittree-cleaned environment and platform's 5-second CLI timeout helper.
// Review context also isolates object lookup, injected Git configuration and
// pathspec interpretation from ambient overrides.
func NewRunner(env platform.Env, cwd string) GitRunner {
	clean := gittree.CleanGitEnv(env)
	for key := range clean {
		upper := strings.ToUpper(key)
		switch upper {
		case "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_NAMESPACE",
			"GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT", "GIT_LITERAL_PATHSPECS", "GIT_GLOB_PATHSPECS",
			"GIT_NOGLOB_PATHSPECS", "GIT_ICASE_PATHSPECS", "GIT_IMPLICIT_WORK_TREE", "GIT_ATTR_SOURCE",
			"GIT_ATTR_NOSYSTEM", "GIT_GRAFT_FILE", "GIT_SHALLOW_FILE":
			delete(clean, key)
		default:
			if strings.HasPrefix(upper, "GIT_CONFIG_KEY_") || strings.HasPrefix(upper, "GIT_CONFIG_VALUE_") {
				delete(clean, key)
			}
		}
	}
	clean["GIT_ATTR_NOSYSTEM"] = "1"
	return runner{env: clean, cwd: cwd}
}

type runner struct {
	env    platform.Env
	cwd    string
	gitDir string
}

func (r runner) At(dir, gitDir string) GitRunner {
	r.cwd, r.gitDir = dir, gitDir
	return r
}

func (r runner) Run(args ...string) (string, error) {
	prefix := []string{"--no-replace-objects", "--no-optional-locks"}
	dir := r.cwd
	if r.gitDir != "" {
		prefix = append(prefix, "--git-dir="+r.gitDir)
		if r.cwd != "" {
			prefix = append(prefix, "--work-tree="+r.cwd)
		} else {
			dir = r.gitDir // a bare repository: no working tree
		}
	}
	result := platform.RunCli("git", append(prefix, args...), platform.RunOptions{Env: r.env, Cwd: dir, TimeoutMs: 5000})
	if result.Status != nil && *result.Status == 0 && result.Error == "" && !result.TimedOut {
		return result.Stdout, nil
	}
	status := -1
	if result.Status != nil {
		status = *result.Status
	}
	return "", &CommandError{Args: args, Status: status, Stderr: strings.TrimSpace(strings.Join([]string{result.Stderr, result.Error}, " ")) + gitTimeoutSuffix(result.TimedOut)}
}

func gitTimeoutSuffix(timedOut bool) string {
	if timedOut {
		return " (timed out)"
	}
	return ""
}
