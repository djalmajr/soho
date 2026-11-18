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
	for _, dropped := range res.Dropped {
		for _, path := range paths {
			if dropped.Malformed || dropped.What == path || inside(path, dropped.What) || inside(dropped.What, path) {
				_, _ = fmt.Fprintf(platform.Stderr, "copies release: unresolved queued registration %q (%s); nothing removed\n", dropped.What, dropped.Cause)
				return 4
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
		for _, path := range paths {
			if pending.Path == path || inside(path, pending.Path) || inside(pending.Path, path) {
				unlock()
				_, _ = fmt.Fprintf(platform.Stderr, "copies release: pending registration %q overlaps request; nothing removed\n", pending.Path)
				return 4
			}
		}
	}
	byPath := map[string]core.CopyRow{}
	for _, row := range rows {
		byPath[row.Path] = row
	}
	seen := map[string]bool{}
	for _, path := range paths {
		row, ok := byPath[path]
		reason := ""
		switch {
		case !filepath.IsAbs(path) || strings.HasPrefix(path, "-"):
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
			_, _ = fmt.Fprintf(platform.Stderr, "copies release: refusing '%s': %s; nothing removed\n", path, reason)
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
					_, _ = fmt.Fprintf(platform.Stderr, "copies release: refusing '%s': %s; nothing removed\n", path, held)
					return 2
				}
			}
		}
		seen[path] = true
	}
	outer := make([]string, 0, len(seen))
	for _, path := range paths {
		if !seen[path] {
			continue
		}
		seen[path] = false
		covered := false
		for _, other := range paths {
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
