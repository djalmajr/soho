// Package gittree records and compares task Git identities without changing Git state.
package gittree

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
)

var keys = []string{"git_head", "git_branch", "git_tree", "git_checked", "diff_checked"}

func git(env platform.Env, tree string, args ...string) (string, bool) {
	out, ok := gitOutput(env, tree, args...)
	return strings.TrimSpace(out), ok
}

// gitOutput is the shared read-only runner, preserving raw stat/path output.
func gitOutput(env platform.Env, tree string, args ...string) (string, bool) {
	clean := platform.Env{}
	for k, v := range env {
		switch strings.ToUpper(k) {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE":
			continue
		}
		clean[k] = v
	}
	clean["GIT_OPTIONAL_LOCKS"] = "0"
	r := platform.RunCli("git", append([]string{"-C", tree}, args...), platform.RunOptions{Env: clean, TimeoutMs: 5000})
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
	return jsonjs.O("git_head", head, "git_branch", branch, "git_tree", abs)
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
	return fields
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
		if _, exists := o.Get(k); exists {
			continue
		}
		if v, ok := fields.Get(k); ok {
			o.Set(k, v)
			changed = true
		}
	}
	if changed {
		_ = core.WriteScratchFile(path, jsonjs.Stringify(o)+"\n")
	}
}

// Sidecar locates the task's sidecar in state or alongside scratch reports.
func Sidecar(sd, report string) string {
	return sidecarLocate(sd, report, read)
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
		core.Warn(fmt.Sprintf("agent '%s' left a local commit in %s: HEAD moved from %s to %s during its task (briefs forbid commits) — inspect it before integrating", agent, tree, old[:7], head[:7]), logFile, command)
	}
	if local || branch != nextBranch {
		return jsonjs.O("from", old, "to", head, "branch_from", branch, "branch_to", nextBranch)
	}
	return nil
}
