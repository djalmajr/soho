package gittree

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

// ChangedPath describes one path in the task's current Git delta.
type ChangedPath struct {
	Path    string
	ModTime time.Time
}

// ChangedPaths returns the edit task's changed tracked and untracked paths,
// restricted to its effective Owned files. A nil result also covers metadata,
// Git, scope or stat failures: callers must preserve report authority then.
// It reads through the tree root and never changes Git or task state.
func ChangedPaths(sidecar, agent string, env platform.Env) []ChangedPath {
	return changedPaths(sidecar, agent, env, func(role, cwd string) bool { return core.RoleIsEdit(role, env, cwd) })
}

// ChangedPathsReadOnly is ChangedPaths with the read-only, nonfatal role
// lookup (RoleIsEditReadOnly): the same changed-path algorithm, but the
// edit-role decision never materializes the skill bundle and never Dies,
// so an unavailable custom role simply reports nil like every other
// metadata failure.
func ChangedPathsReadOnly(sidecar, agent string, env platform.Env) []ChangedPath {
	return changedPaths(sidecar, agent, env, func(role, cwd string) bool { return core.RoleIsEditReadOnly(role, env, cwd) })
}

// changedPaths is the shared changed-path algorithm: the role gate
// (roleIsEdit) is the only decision that differs between the command and
// the inspection readers.
func changedPaths(sidecar, agent string, env platform.Env, roleIsEdit func(role, cwd string) bool) []ChangedPath {
	o := read(sidecar)
	if o == nil {
		return nil
	}
	tree, _ := stringField(o, "git_tree")
	head, _ := stringField(o, "git_head")
	role, _ := stringField(o, "role")
	if tree == "" || !diffHeadRE.MatchString(head) || role == "" || !roleIsEdit(role, tree) {
		return nil
	}
	owned, hasOwned, scopeOK := diffEffectiveOwned(sidecar, agent, o)
	if !scopeOK {
		return nil
	}
	names, ok := gitOutput(env, tree, "-c", "diff.autoRefreshIndex=false", "diff", "--numstat", "--no-renames", "-z", head, "--")
	if !ok {
		return nil
	}
	untracked, ok := gitOutput(env, tree, "ls-files", "--others", "--exclude-standard", "-z")
	if !ok {
		return nil
	}
	root, err := os.OpenRoot(tree)
	if err != nil {
		return nil
	}
	defer root.Close()
	paths := map[string]bool{}
	for _, record := range strings.Split(names, "\x00") {
		if parts := strings.SplitN(record, "\t", 3); len(parts) == 3 {
			paths[parts[2]] = true
		}
	}
	for _, name := range strings.Split(untracked, "\x00") {
		if name != "" {
			paths[name] = true
		}
	}
	// Sort the reads too, so failure handling does not depend on map order.
	ordered := make([]string, 0, len(paths))
	for name := range paths {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	out := []ChangedPath{}
	for _, name := range ordered {
		covered := !hasOwned
		for _, pattern := range owned {
			if diffOwnedMatch(pattern, name) {
				covered = true
				break
			}
		}
		if !covered {
			continue
		}
		info, err := root.Lstat(filepath.FromSlash(name))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil
		}
		out = append(out, ChangedPath{Path: name, ModTime: info.ModTime()})
	}
	return out
}
