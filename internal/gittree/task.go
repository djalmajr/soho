// Package gittree records and compares task Git identities without changing Git state.
package gittree

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/text"
)

// SnapshotMaxBytes caps the serialized snapshot; tests may lower it.
var SnapshotMaxBytes = 16 << 20

const snapshotReadLimit = 16 << 20

var keys = []string{"git_head", "git_branch", "git_tree", "git_snapshot", "git_previous_owned", "git_checked", "diff_checked"}

func git(env platform.Env, tree string, args ...string) (string, bool) {
	out, ok := gitOutput(env, tree, args...)
	return strings.TrimSpace(out), ok
}

// CleanGitEnv drops the repository-targeting selectors from a child Git
// environment and disables optional locks. Exported so read-only Git callers
// outside this package share one cleaned environment with gitOutput.
func CleanGitEnv(env platform.Env) platform.Env {
	clean := platform.Env{}
	for k, v := range env {
		switch strings.ToUpper(k) {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE":
			continue
		}
		clean[k] = v
	}
	clean["GIT_OPTIONAL_LOCKS"] = "0"
	return clean
}

// gitOutput is the shared read-only runner, preserving raw stat/path output.
func gitOutput(env platform.Env, tree string, args ...string) (string, bool) {
	r := platform.RunCli("git", append([]string{"-C", tree}, args...), platform.RunOptions{Env: CleanGitEnv(env), TimeoutMs: 5000})
	return r.Stdout, r.Status != nil && *r.Status == 0 && !r.TimedOut && r.Error == ""
}

func identity(env platform.Env, tree string) (head, branch string, ok bool) {
	head, ok = git(env, tree, "rev-parse", "HEAD")
	if !ok || head == "" {
		return "", "", false
	}
	branch, ok = git(env, tree, "symbolic-ref", "-q", "--short", "HEAD")
	if !ok {
		// A detached HEAD is expected to make symbolic-ref fail; distinguish
		// it from a broken command or repository before recording it.
		ref, detached := git(env, tree, "rev-parse", "--abbrev-ref", "HEAD")
		if !detached || ref != "HEAD" {
			return "", "", false
		}
		branch = ""
	}
	return head, branch, true
}

// Snapshot returns no fields when Git cannot supply a complete identity.
func Snapshot(env platform.Env, tree string) *jsonjs.Object {
	abs, err := filepath.Abs(tree)
	if err != nil {
		return nil
	}
	head, branch, ok := identity(env, abs)
	if !ok {
		return nil
	}
	fields := jsonjs.O("git_head", head, "git_branch", branch, "git_tree", abs)
	if snapshot, ok := contentSnapshot(env, abs, head); ok {
		if len(jsonjs.Stringify(snapshot))+1 > SnapshotMaxBytes {
			fields.Set("git_snapshot_over_limit", true)
		} else {
			fields.Set("git_snapshot", snapshot)
		}
	}
	return fields
}

// contentSnapshot records the worktree content for every path differing from
// the dispatch HEAD, including deletions and untracked files.
func contentSnapshot(env platform.Env, tree, head string) (*jsonjs.Object, bool) {
	names, ok := gitOutput(env, tree, "-c", "diff.autoRefreshIndex=false", "diff", "--numstat", "--no-renames", "-z", head, "--")
	if !ok {
		return nil, false
	}
	untracked, ok := gitOutput(env, tree, "ls-files", "--others", "--exclude-standard", "-z")
	if !ok {
		return nil, false
	}
	root, err := os.OpenRoot(tree)
	if err != nil {
		return nil, false
	}
	defer root.Close()
	result := jsonjs.O()
	paths := strings.Split(untracked, "\x00")
	for _, record := range strings.Split(names, "\x00") {
		if parts := strings.SplitN(record, "\t", 3); len(parts) == 3 {
			paths = append(paths, parts[2])
		}
	}
	for _, name := range paths {
		if name == "" {
			continue
		}
		hash, ok := contentHashForGit(env, tree, root, name)
		if !ok {
			return nil, false
		}
		result.Set(name, hash)
	}
	return result, true
}

func contentHashForGit(env platform.Env, tree string, root *os.Root, name string) (string, bool) {
	info, err := root.Lstat(filepath.FromSlash(name))
	if err != nil || !info.IsDir() {
		return contentHash(root, name)
	}
	entry, ok := gitOutput(env, tree, "--literal-pathspecs", "ls-files", "-s", "--", name)
	if !ok {
		return "", false
	}
	if strings.HasPrefix(entry, "160000 ") {
		path := filepath.Join(tree, filepath.FromSlash(name))
		head, ok := git(env, path, "rev-parse", "--verify", "HEAD")
		if !ok {
			return "", false
		}
		dirty, ok := gitOutput(env, path, "status", "--porcelain", "-z", "--untracked-files=normal")
		if !ok {
			return "", false
		}
		return fmt.Sprintf("gitlink:%s:%t", head, dirty != ""), true
	}
	return contentHash(root, name)
}

func contentHash(root *os.Root, name string) (string, bool) {
	info, err := root.Lstat(filepath.FromSlash(name))
	if os.IsNotExist(err) {
		return "missing", true
	}
	if err != nil {
		return "unreadable", true
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := root.Readlink(filepath.FromSlash(name))
		if err != nil {
			return "unreadable-link", true
		}
		return fmt.Sprintf("link:%x", sha256.Sum256([]byte(target))), true
	}
	if !info.Mode().IsRegular() {
		return fmt.Sprintf("special:%s", info.Mode().Type()), true
	}
	f, err := root.Open(filepath.FromSlash(name))
	if err != nil {
		return "unreadable-file", true
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "unreadable-file", true
	}
	return fmt.Sprintf("file:%t:%x", info.Mode()&0100 != 0, h.Sum(nil)), true
}

func read(path string) *jsonjs.Object {
	raw, err := core.ReadScratchReport(path)
	if err != nil {
		return nil
	}
	v, err := jsonjs.Parse(raw)
	o, _ := v.(*jsonjs.Object)
	if err != nil {
		return nil
	}
	return o
}

// sidecarInspectLimit is the D7 inspection cap of one dispatch sidecar:
// the inspection seam never reads more of a sidecar's metadata than this
// and refuses an overlong sidecar (it is not truncated).
const sidecarInspectLimit = 1 << 20

// readSidecarInspect is the inspection's sidecar read: the same existing
// scratch read guarantees as read (a regular nonlinked file of the
// current user, nonblocking, identity-verified) plus the capped bound.
func readSidecarInspect(path string) *jsonjs.Object {
	raw, err := core.ReadScratchReportLimited(path, sidecarInspectLimit)
	if err != nil {
		return nil
	}
	v, err := jsonjs.Parse(raw)
	o, _ := v.(*jsonjs.Object)
	if err != nil {
		return nil
	}
	return o
}

// snapshotFromSidecar supports both inline version-1 snapshots and the new companion.
func snapshotFromSidecar(path string, o *jsonjs.Object) (*jsonjs.Object, bool) {
	if v, ok := o.Get("git_snapshot"); ok {
		snapshot, ok := v.(*jsonjs.Object)
		return snapshot, ok
	}
	marker, _ := o.Get("git_snapshot_file")
	if marker != true {
		return nil, false
	}
	raw, err := core.ReadScratchReportLimited(path+".snapshot", snapshotReadLimit)
	if err != nil {
		return nil, false
	}
	value, err := jsonjs.Parse(raw)
	snapshot, ok := value.(*jsonjs.Object)
	return snapshot, err == nil && ok
}

// Fields reads only the task's Git metadata, retaining a previous once marker.
func Fields(path string) *jsonjs.Object {
	o := read(path)
	if o == nil {
		return nil
	}
	fields := jsonjs.O()
	for _, k := range keys {
		if v, ok := o.Get(k); ok {
			fields.Set(k, v)
		}
	}
	if snapshot, ok := snapshotFromSidecar(path, o); ok {
		fields.Set("git_snapshot", snapshot)
	}
	return fields
}

// GitTree returns the worker's recorded project root from its sidecar: the
// git_tree field the dispatch writes (the composed brief's Worktree:
// declaration when it resolves, else the worker's cwd), and "" when the
// sidecar is missing or over the inspection limit, or has no such field. A
// relative value also has no root: Snapshot records absolute paths, and an old
// relative one would resolve against the caller, possibly another project.
func GitTree(sd, report string) string {
	o := readSidecarInspect(SidecarInspect(sd, report))
	if o == nil {
		return ""
	}
	tree, _ := stringField(o, "git_tree")
	if !filepath.IsAbs(tree) {
		return ""
	}
	return tree
}

// Preserve merges missing Git fields through the protected sidecar writer.
// Existing fields, including a check performed during blocking dispatch, win.
func Preserve(path string, fields *jsonjs.Object) {
	if fields == nil {
		return
	}
	o := read(path)
	if o == nil {
		return
	}
	changed := false
	for _, k := range keys {
		if k == "git_snapshot" {
			continue
		}
		if _, exists := o.Get(k); exists {
			continue
		}
		if v, ok := fields.Get(k); ok {
			o.Set(k, v)
			changed = true
		}
	}
	if _, inline := o.Get("git_snapshot"); !inline {
		if marker, _ := o.Get("git_snapshot_file"); marker != true {
			if v, ok := fields.Get("git_snapshot"); ok {
				if err := core.WriteScratchFile(path+".snapshot", jsonjs.Stringify(v)+"\n"); err == nil {
					o.Set("git_snapshot_file", true)
					changed = true
				}
			}
		}
	}
	if changed {
		_ = core.WriteScratchFile(path, jsonjs.Stringify(o)+"\n")
	}
}

// ReplaceBaseline persists a fresh task baseline after a refused guarded send.
func ReplaceBaseline(path string, fields *jsonjs.Object) error {
	o := read(path)
	if o == nil {
		return fmt.Errorf("cannot read dispatch sidecar")
	}
	o.Delete("git_snapshot_file")
	for _, k := range []string{"git_head", "git_branch", "git_tree", "git_snapshot"} {
		o.Delete(k)
		if k == "git_snapshot" || fields == nil {
			continue
		}
		if v, ok := fields.Get(k); ok {
			o.Set(k, v)
		}
	}
	if fields != nil {
		if v, ok := fields.Get("git_snapshot"); ok {
			if err := core.WriteScratchFile(path+".snapshot", jsonjs.Stringify(v)+"\n"); err != nil {
				return err
			}
			o.Set("git_snapshot_file", true)
		} else if err := os.Remove(path + ".snapshot"); err != nil && !os.IsNotExist(err) {
			return err
		}
	} else if err := os.Remove(path + ".snapshot"); err != nil && !os.IsNotExist(err) {
		return err
	}
	return core.WriteScratchFile(path, jsonjs.Stringify(o)+"\n")
}

// Sidecar locates the task's sidecar in state or alongside scratch reports.
func Sidecar(sd, report string) string {
	return sidecarLocateComplete(sd, report)
}

// SidecarInspect is the inspection's sidecar location: the same two
// candidate probes as Sidecar, through the capped sidecar read, so an
// overlong sidecar refuses instead of being read into memory.
func SidecarInspect(sd, report string) string {
	return sidecarLocate(sd, report, readSidecarInspect)
}

// SidecarInspectSource is the sidecar location with a session source: the
// same two candidate probes, through the source's confined bounded read
// (the per-sidecar cap), so the candidates are never reopened by raw
// path and an overlong sidecar refuses.
func SidecarInspectSource(src *core.SessionSource, sd, report string) string {
	return sidecarLocate(sd, report, sourceSidecarParse(src))
}

// sidecarLocate is the shared sidecar probe: state briefs first, then
// alongside the report; the sidecar read is the only decision that
// differs between the command and inspection callers.
func sidecarLocate(sd, report string, sidecarRead func(string) *jsonjs.Object) string {
	base := strings.TrimSuffix(filepath.Base(report), ".md") + ".dispatch.json"
	for _, path := range []string{filepath.Join(sd, "briefs", base), filepath.Join(filepath.Dir(report), base)} {
		if sidecarRead(path) != nil {
			return path
		}
	}
	return ""
}

func stringField(o *jsonjs.Object, k string) (string, bool) {
	v, ok := o.Get(k)
	s, typed := v.(string)
	return s, ok && typed
}

// Check compares once and returns a JSON value only when a warning applies.
// The permanent exclusive marker permits at most one comparison attempt.
func Check(path, agent string, env platform.Env, logFile, command string) *jsonjs.Object {
	if path == "" {
		return nil
	}
	o := read(path)
	if o == nil {
		return nil
	}
	old, h := stringField(o, "git_head")
	branch, b := stringField(o, "git_branch")
	tree, tr := stringField(o, "git_tree")
	if !h || !b || !tr || len(old) < 7 || tree == "" {
		return nil
	}
	// A sidecar that already records the comparison is done: the state copy
	// that wait mirrors from a scratch sidecar carries git_checked but not
	// the scratch marker, and must not authorize a second attempt.
	if checked, ok := o.Get("git_checked"); ok && checked == true {
		return nil
	}
	marker, err := os.OpenFile(path+".git-checked", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil
	}
	if marker.Close() != nil {
		return nil
	}
	head, nextBranch, ok := identity(env, tree)
	if !ok || len(head) < 7 {
		return nil
	}
	local := false
	if head != old {
		refs, ok := git(env, tree, "for-each-ref", "--contains", head, "refs/remotes", "--count=1", "--format=x")
		if !ok {
			return nil
		}
		local = refs == ""
	}
	o.Set("git_checked", true)
	if core.WriteScratchFile(path, jsonjs.Stringify(o)+"\n") != nil {
		return nil
	}
	if branch != nextBranch {
		core.Warn(fmt.Sprintf("agent '%s' switched %s from branch %s to %s during its task; the brief owns git — check the tree before integrating", agent, tree, branch, nextBranch), logFile, command)
	}
	if local {
		// The worker and the orchestrator commit with the same Git identity,
		// so nothing proves who made the commit (SOH-297): name it by its
		// subject and let the orchestrator recognize its own.
		named := ""
		if subject, ok := git(env, tree, "log", "-1", "--format=%s", head); ok {
			if subject = text.SanitizeCause(strings.TrimSpace(subject)); subject != "" {
				if len(subject) > 72 {
					subject = subject[:72] + "..."
				}
				named = fmt.Sprintf(" (%q)", subject)
			}
		}
		core.Warn(fmt.Sprintf("HEAD moved from %s to %s in %s with a local commit%s during the task of agent '%s'; if you did not make it, the agent left a local commit (briefs forbid commits) — inspect it before integrating", old[:7], head[:7], tree, named, agent), logFile, command)
	}
	if local || branch != nextBranch {
		return jsonjs.O("from", old, "to", head, "branch_from", branch, "branch_to", nextBranch)
	}
	return nil
}

func sidecarLocateComplete(sd, report string) string {
	base := strings.TrimSuffix(filepath.Base(report), ".md") + ".dispatch.json"
	paths := []string{filepath.Join(sd, "briefs", base), filepath.Join(filepath.Dir(report), base)}
	incomplete := ""
	for _, path := range paths {
		if o := read(path); o != nil {
			if marker, _ := o.Get("git_snapshot_file"); marker == true {
				if _, ok := snapshotFromSidecar(path, o); !ok {
					if incomplete == "" {
						incomplete = path
					}
					continue
				}
			}
			return path
		}
	}
	return incomplete
}
