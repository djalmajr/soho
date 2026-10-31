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
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
)

// ChangedPath describes one path in the task's current Git delta.
type ChangedPath struct {
	Path    string
	ModTime time.Time
}

// ChangedPaths is the command reader of the edit task's changed tracked
// and untracked paths, restricted to its effective Owned files: the
// shared algorithm with the ordinary role gate and the ordinary sidecar
// read. A nil result also covers metadata, Git, scope or stat failures:
// callers must preserve report authority then. It reads through the tree
// root and never changes Git or task state.
func ChangedPaths(sidecar, agent string, env platform.Env) []ChangedPath {
	return changedPathsRead(sidecar, agent, env, func(role, cwd string) bool { return core.RoleIsEdit(role, env, cwd) }, read, commandOwnedSeams())
}

// ChangedPathsReadOnly is ChangedPaths with the read-only, nonfatal role
// lookup (RoleIsEditReadOnly) and the inspection's capped sidecar read:
// the same shared algorithm, but the edit-role decision never
// materializes the skill bundle and never Dies, and an overlong sidecar
// refuses instead of being read whole, so an unavailable custom role or
// hostile sidecar simply reports nil like every other metadata failure.
func ChangedPathsReadOnly(sidecar, agent string, env platform.Env) []ChangedPath {
	return changedPathsRead(sidecar, agent, env, func(role, cwd string) bool { return core.RoleIsEditReadOnly(role, env, cwd) }, readSidecarInspect, readOnlyOwnedSeams())
}

// ChangedPathsInspectSource is the read-only changed paths with a session
// source: the same shared algorithm, but the sidecar, the composed
// briefs, the task-report pointer and the historical sidecars are the
// source's confined bounded reads (the D7 caps) instead of raw path
// reads — a renamed or replaced state dir does not change the decision,
// and a capped or refused read reports nil like every other metadata
// failure (report authority is preserved).
func ChangedPathsInspectSource(src *core.SessionSource, sidecar, agent string, env platform.Env) []ChangedPath {
	return changedPathsRead(sidecar, agent, env, func(role, cwd string) bool { return core.RoleIsEditReadOnly(role, env, cwd) }, sourceSidecarParse(src), sourceOwnedSeams(src))
}

// sourceSidecarParse is the inspection's sidecar parse over the source's
// confined bounded read: the same candidate guarantee as read
// (a regular nonlinked file of the current user, nonblocking,
// identity-verified) at the inspection's per-sidecar cap.
func sourceSidecarParse(src *core.SessionSource) func(string) *jsonjs.Object {
	return func(p string) *jsonjs.Object {
		raw, err := src.ReadAuxFile(p, sidecarInspectLimit)
		return parseObjectBytes([]byte(raw), err)
	}
}

// changedPathsRead is the shared changed-path algorithm: the role gate,
// the sidecar read and the owned-scope read seams are the only decisions
// that differ between the command, the read-only and the
// inspection-with-source callers.
func changedPathsRead(sidecar, agent string, env platform.Env, roleIsEdit func(role, cwd string) bool, sidecarRead func(string) *jsonjs.Object, seams ownedReadSeams) []ChangedPath {
	o := sidecarRead(sidecar)
	if o == nil {
		return nil
	}
	tree, _ := stringField(o, "git_tree")
	head, _ := stringField(o, "git_head")
	role, _ := stringField(o, "role")
	if tree == "" || !diffHeadRE.MatchString(head) || role == "" || !roleIsEdit(role, tree) {
		return nil
	}
	owned, hasOwned, scopeOK := diffEffectiveOwnedRead(sidecar, agent, o, seams)
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
