package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

var copiesReleaseLock = core.LockCopies
var copiesReleaseRead = core.ReadCopies
var copiesReleaseDrop = core.DropCopiesLinesHeld
var copiesReleasePending = core.ReadPendingCopiesFor

// restoreDiscard restores the original entry and marker through the checked
// parent after a registry-write failure. Never overwrite an existing entry.
func restoreDiscard(discard, original string, parent fs.FileInfo) error {
	root, err := openVerifiedParent(discard, parent)
	if err != nil {
		return err
	}
	defer root.Close()
	base, old := filepath.Base(discard), filepath.Base(original)
	if _, err := root.Lstat(old); !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("original path is occupied or cannot be checked")
	}
	if err := root.Rename(base, old); err != nil {
		return err
	}
	if info, err := root.Lstat(originMarkerPath(base)); err == nil && info.Mode().IsRegular() {
		if _, err := root.Lstat(originMarkerPath(old)); errors.Is(err, fs.ErrNotExist) {
			if err := root.Rename(originMarkerPath(base), originMarkerPath(old)); err != nil {
				return fmt.Errorf("copy restored but marker not restored: %w", err)
			}
		} else {
			return fmt.Errorf("copy restored but original marker is occupied or cannot be checked")
		}
	}
	return nil
}

// copiesRelease validates the entire request under the registry lock before
// renaming any entry. Removal happens only after the registry is unlocked.
func copiesRelease(paths []string, ctx *core.Config, env platform.Env, cwd string) int {
	if len(paths) == 0 {
		_, _ = fmt.Fprintln(platform.Stderr, "soho: usage: soho copies release <path>...")
		return 2
	}
	sd := registryDestination(env)
	if sd == "" {
		sd = core.StateDir(ctx, env, cwd)
	}
	caller := env.Get("SOHO_AGENT")
	orch := core.Cfg(ctx, "orchestrator_name", "orchestrator", env)
	if caller == "" || caller == orch {
		_, _ = fmt.Fprintln(platform.Stderr, "copies release: caller must be a copy owner; orchestrators use soho release <agent> or soho gc")
		return 2
	}
	res, err := incorporateCopies(sd, filepath.Base(sd), env, func(row core.CopyRow) (core.CopyRow, string) {
		resolved, cause := checkCopyPath(row.Path, env, cwd)
		if cause != "" {
			return row, cause
		}
		if !isExistingDir(resolved) {
			return row, "not an existing directory"
		}
		row.Path = resolved
		return row, ""
	})
	if err != nil {
		_, _ = fmt.Fprintf(platform.Stderr, "copies release: cannot incorporate pending registrations (%v); nothing removed\n", err)
		return 4
	}
	pendingResultLines("copies release", "copy", res, nil)
	// Each requested path is resolved once (SOH-464): the queued-registration
	// checks below and the row selection under the lock use this same result,
	// so a parent link that changes in between cannot make them disagree.
	canonicals := make([]string, len(paths))
	spellings := make([][]string, len(paths))
	for i, path := range paths {
		spellings[i] = []string{path}
		if !filepath.IsAbs(path) {
			continue
		}
		if canonical, ok := canonicalCopyPath(path); ok {
			canonicals[i] = canonical
			if canonical != path {
				spellings[i] = append(spellings[i], canonical)
			}
		}
	}
	for _, dropped := range res.Dropped {
		for i := range paths {
			for _, path := range spellings[i] {
				if dropped.Malformed || dropped.What == path || inside(path, dropped.What) || inside(dropped.What, path) {
					_, _ = fmt.Fprintf(platform.Stderr, "copies release: unresolved queued registration %q (%s); nothing removed\n", dropped.What, dropped.Cause)
					return 4
				}
			}
		}
	}
	unlock, err := copiesReleaseLock(sd)
	if err != nil {
		_, _ = fmt.Fprintf(platform.Stderr, "copies release: cannot lock registry (%v); nothing removed\n", err)
		return 4
	}
	rows, err := copiesReleaseRead(sd)
	if err != nil {
		unlock()
		_, _ = fmt.Fprintf(platform.Stderr, "copies release: cannot read registry (%v); nothing removed\n", err)
		return 4
	}
	pendingRows, err := copiesReleasePending(sd, filepath.Base(sd), env)
	if err != nil {
		unlock()
		_, _ = fmt.Fprintf(platform.Stderr, "copies release: cannot read pending registrations (%v); nothing removed\n", err)
		return 4
	}
	for _, pending := range pendingRows {
		for i := range paths {
			for _, path := range spellings[i] {
				if pending.Path == path || inside(path, pending.Path) || inside(pending.Path, path) {
					unlock()
					_, _ = fmt.Fprintf(platform.Stderr, "copies release: pending registration %q overlaps request; nothing removed\n", pending.Path)
					return 4
				}
			}
		}
	}
	byPath := map[string]core.CopyRow{}
	for _, row := range rows {
		byPath[row.Path] = row
	}
	// A row can hold a path resolved through a symlinked parent (SOH-464).
	// When the path as typed misses, look up its canonical spelling and use
	// the registered path for every later step. The typed path stays in the
	// refusal lines, with the resolved spelling when one was computed.
	effective := make([]string, len(paths))
	resolvedNote := make([]string, len(paths))
	for i, typed := range paths {
		effective[i] = typed
		if _, ok := byPath[typed]; ok {
			continue
		}
		canonical := canonicals[i]
		if canonical == "" {
			continue
		}
		if canonical != typed {
			resolvedNote[i] = " (resolved: " + canonical + ")"
		}
		if _, ok := byPath[canonical]; ok {
			effective[i] = canonical
		}
	}
	seen := map[string]bool{}
	for i, typed := range paths {
		path := effective[i]
		row, ok := byPath[path]
		reason := ""
		switch {
		case !filepath.IsAbs(typed) || strings.HasPrefix(typed, "-"):
			reason = "not an absolute path"
		case !ok:
			reason = "not registered"
		case row.Owner == "" || row.Owner == "-":
			reason = "ownerless copy; use soho gc"
		case caller != row.Owner:
			reason = "owned by another agent"
		}
		if reason == "" {
			for _, nested := range rows {
				if nested.Path != path && inside(path, nested.Path) && nested.Owner != row.Owner {
					reason = "contains a copy owned by another agent"
					break
				}
			}
		}
		if reason != "" {
			unlock()
			_, _ = fmt.Fprintf(platform.Stderr, "copies release: refusing '%s': %s%s; nothing removed\n", typed, reason, resolvedNote[i])
			return 2
		}
		if _, err := os.Lstat(path); err == nil {
			if _, _, cause := checkedCopy(path, env, cwd); cause == "" {
				held, guardErr := copyDeletionGuard(path, row.Owner, sd, env, cwd)
				if guardErr != nil {
					unlock()
					_, _ = fmt.Fprintf(platform.Stderr, "%s: kept: %v\n", path, guardErr)
					return 4
				}
				if held != "" {
					unlock()
					_, _ = fmt.Fprintf(platform.Stderr, "copies release: refusing '%s': %s%s; nothing removed\n", typed, held, resolvedNote[i])
					return 2
				}
			}
		}
		seen[path] = true
	}
	outer := make([]string, 0, len(seen))
	for _, path := range effective {
		if !seen[path] {
			continue
		}
		seen[path] = false
		covered := false
		for _, other := range effective {
			if other != path && inside(other, path) {
				covered = true
				break
			}
		}
		if !covered {
			outer = append(outer, path)
		}
	}
	type removal struct {
		path, discard string
		missing       bool
		parent        fs.FileInfo
		children      []string
	}
	removals := []removal{}
	failed := false
	for _, path := range outer {
		children := []string{}
		for _, row := range rows {
			if row.Path != path && inside(path, row.Path) {
				children = append(children, row.Path)
			}
		}
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			removals = append(removals, removal{path: path, missing: true, children: children})
			continue
		}
		resolved, parent, cause := checkedCopy(path, env, cwd)
		if cause != "" {
			gcKeptGuardLine(path, cause)
			failed = true
			continue
		}
		held, guardErr := copyDeletionGuard(path, byPath[path].Owner, sd, env, cwd)
		if guardErr != nil {
			unlock()
			_, _ = fmt.Fprintf(platform.Stderr, "%s: kept: %v\n", path, guardErr)
			return 4
		}
		if held != "" {
			_, _ = fmt.Fprintf(platform.Stderr, "%s: kept: %s\n", path, held)
			failed = true
			continue
		}
		discard, err := renameToDiscard(resolved, parent)
		if err != nil {
			keptCopyError(path, err)
			failed = true
			continue
		}
		removals = append(removals, removal{path: path, discard: discard, parent: parent, children: children})
	}

	drop := map[string]bool{}
	for _, r := range removals {
		drop[r.path] = true
		for _, child := range r.children {
			drop[child] = true
		}
	}
	if len(drop) > 0 {
		if err := copiesReleaseDrop(sd, drop); err != nil {
			for i := len(removals) - 1; i >= 0; i-- {
				r := removals[i]
				if r.discard != "" {
					if restoreErr := restoreDiscard(r.discard, r.path, r.parent); restoreErr != nil {
						gcDiscardLeft(r.discard, "registry update failed; restore failed: "+restoreErr.Error(), env, cwd)
					} else {
						_, _ = fmt.Fprintf(platform.Stdout, "kept %s (registry update failed; restored)\n", r.path)
					}
				}
			}
			unlock()
			_, _ = fmt.Fprintf(platform.Stderr, "copies release: cannot update registry (%v)\n", err)
			return 4
		}
	}
	unlock()
	for _, r := range removals {
		size := int64(0)
		if !r.missing {
			resolved, parent, cause := checkedCopy(r.discard, env, cwd)
			if cause != "" {
				gcDiscardLeft(r.discard, cause, env, cwd)
				failed = true
				continue
			}
			held, guardErr := copyDeletionGuard(r.discard, byPath[r.path].Owner, sd, env, cwd)
			if guardErr != nil {
				_, _ = fmt.Fprintf(platform.Stderr, "%s: kept: %v\n", r.discard, guardErr)
				failed = true
				continue
			}
			if held != "" {
				_, _ = fmt.Fprintf(platform.Stderr, "%s: kept: %s\n", r.discard, held)
				failed = true
				continue
			}
			size = copyDirSize(resolved)
			if err := removeCopy(resolved, parent); err != nil {
				gcDiscardLeft(r.discard, copyErrorCause(err), env, cwd)
				failed = true
				continue
			}
		}
		if r.missing {
			_, _ = fmt.Fprintf(platform.Stdout, "removed %s  %s (already gone from disk)\n", r.path, humanSize(0))
		} else {
			_, _ = fmt.Fprintf(platform.Stdout, "removed %s  %s\n", r.path, humanSize(size))
		}
		for _, child := range r.children {
			_, _ = fmt.Fprintf(platform.Stdout, "removed %s (with %s)\n", child, r.path)
		}
	}
	if failed {
		return 4
	}
	return 0
}

// canonicalCopyPath is the registry spelling of a requested path, resolved
// the way copies add resolves the path it registers (SOH-464): a path that
// is itself a symlink has none, since add refuses a symlink as the copy and a
// link named like a copy must never redirect a release to its target;
// otherwise filepath.EvalSymlinks of the whole path, exactly the value add
// stores, so `link/../copy`, `copy/.` and `copy/inner/..` name the row add
// wrote. A copy that is already gone cannot be resolved whole; its parent is
// then cut from the text as typed, before any cleaning, resolved, and joined
// with the last element, which may not be "." or "..". A parent that cannot
// be resolved has no canonical form.
func canonicalCopyPath(path string) (string, bool) {
	if isFinalSymlink(path) {
		return "", false
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved, true
	}
	trimmed := withoutTrailingSeparators(path)
	volume := filepath.VolumeName(trimmed)
	cut := -1
	for i := len(trimmed) - 1; i >= len(volume); i-- {
		if os.IsPathSeparator(trimmed[i]) {
			cut = i
			break
		}
	}
	if cut < 0 {
		return "", false
	}
	base := trimmed[cut+1:]
	if base == "" || base == "." || base == ".." {
		return "", false
	}
	parentText := trimmed[:cut]
	if len(parentText) <= len(volume) {
		parentText = trimmed[:cut+1]
	}
	parent, err := filepath.EvalSymlinks(parentText)
	if err != nil {
		return "", false
	}
	return filepath.Join(parent, base), true
}
