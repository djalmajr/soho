package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/djalmajr/soho/internal/collaboration"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/layout"
	"github.com/djalmajr/soho/internal/platform"
)

func cmdRelease(argv []string, ctx *core.Config, env platform.Env, cwd string) int {
	if len(argv) == 0 {
		platform.Die("agent: Parameter not set", 1)
	}
	agent := argv[0]
	closePane, force, keepCopies, keepProcs := false, false, false, false
	for _, arg := range argv[1:] {
		switch arg {
		case "--close":
			closePane = true
		case "--force":
			force = true
		case "--keep-copies":
			keepCopies = true
		case "--keep-procs":
			keepProcs = true
		default:
			core.DieFriction("release: unknown option "+arg, 2, frictionLogPath, "release")
		}
	}

	sd := core.StateDir(ctx, env, cwd)
	line := core.RosterLine(sd, agent)
	if line == "" {
		if closePane && releaseOrphan(agent, force, ctx, env, cwd) {
			return 0
		}
		core.DieFriction(fmt.Sprintf("agent '%s' is not in the roster (state dir: %s)", agent, sd), 3, frictionLogPath, "release")
	}
	fields := strings.Split(line, "\t")
	field := func(index int) string {
		if index >= len(fields) {
			return ""
		}
		return fields[index]
	}
	pane, created, wdir := field(1), field(5), field(6)
	burst := field(12) == "burst"
	closes := closePane || burst && created == "1"
	report := core.LastReport(sd, agent)
	// A participant of an active collaboration is never released — not with
	// --force: the cycle only ends through an explicit stop and finalize
	// verdict, and a release must not pretend that outcome. Agents outside
	// the roster (the orphan path above) are not participants and skip the
	// check, as do panes with no active collaboration.
	if pane != "" {
		if code, message := collaborationReleaseRefusal(sd, agent, pane, ctx, env, cwd); code != 0 {
			core.DieFriction(message, code, frictionLogPath, "release")
		}
	}
	if !force && reportEmpty(report) {
		state := herdr.AgentState(agent, env, herdr.Timeout, nil)
		if state.State == "unavailable" {
			core.DieFriction(fmt.Sprintf("agent '%s': herdr agent get failed (%s). Refusing to release; the worker may still be live. Retry when herdr answers, or pass --force.", agent, state.Cause), 4, frictionLogPath, "release")
		}
		if closes && report != "" && state.State == "working" {
			core.DieFriction(fmt.Sprintf("agent '%s' is still working and has not written %s; closing now discards its work. Run 'wait %s' first, or release --close --force", agent, report, agent), 3, frictionLogPath, "release")
		}
	}

	if closes {
		if created == "1" {
			if herdr.PaneClose(pane, env) {
				suffix := ""
				if burst {
					suffix = " (temporary)"
				}
				_, _ = fmt.Fprintf(platform.Stdout, "closed pane %s%s\n", pane, suffix)
			}
		} else {
			core.Warn(fmt.Sprintf("pane %s was not created by this skill; not closing it", pane), frictionLogPath, "release")
		}
	} else {
		core.PaneTaskTitle(sd, agent, nil, env)
	}

	core.RosterRemove(sd, agent)
	releaseCopies(sd, agent, keepCopies, env, cwd)
	releaseProcs(sd, agent, keepProcs, env)
	_ = os.Remove(core.LastReportPath(sd, agent))
	_ = os.Remove(filepath.Join(sd, "task-"+agent))
	waitDir := filepath.Join(sd, "wait")
	if entries, err := os.ReadDir(waitDir); err == nil {
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), agent+".") {
				_ = os.Remove(filepath.Join(waitDir, entry.Name()))
			}
		}
	}

	if closes && core.Cfg(ctx, "regrid", "on", env) == "on" {
		releaseAutoRegrid(ctx, env, cwd)
	} else {
		func() {
			defer func() {
				if recover() != nil {
					core.Warn("relabel of the herd tabs failed (see friction)", frictionLogPath, "release")
				}
			}()
			layout.HerdTabsRelabel(ctx, env, cwd)
		}()
	}
	if !closes && pane != "" && created == "1" {
		// The pane stays alive outside the roster: register it, like the
		// other state files, so the orphan commands know this project
		// released it. A pane the skill did not create (spawn --pane) is
		// never registered: release --close must never close it later.
		core.ReleasedPanesRecord(ctx, env, cwd, agent, pane, field(2))
		_, _ = fmt.Fprintf(platform.Stderr, "soho: release: the pane %s stays open; close it later with: soho release %s --close\n", pane, agent)
	}

	worktrees := exec.Command("git", "-C", wdir, "worktree", "list")
	worktrees.Env = env.List()
	if output, err := worktrees.Output(); err == nil {
		leftovers := []string{}
		for _, row := range strings.Split(string(output), "\n") {
			if strings.Contains(row, "/.worktrees/") {
				leftovers = append(leftovers, row)
			}
		}
		if len(leftovers) > 0 {
			_, _ = fmt.Fprintln(platform.Stdout, "leftover worktrees (not removed):")
			for _, row := range leftovers {
				_, _ = fmt.Fprintln(platform.Stdout, row)
			}
		}
	}
	_, _ = fmt.Fprintf(platform.Stdout, "released %s\n", agent)
	return 0
}

// releaseOrphan closes the pane of an agent this project released without
// --close (its pane and name both match a row of the released-panes
// registry): a live agent outside the roster and outside the orchestrator's
// own pane. Only a confirmed-idle orphan is closed; a working, blocked or
// unqueryable one is refused with 10 unless --force is passed. A failed pane
// close exits 4 and keeps the registry row: the pane is still open, and the
// release must not claim otherwise.
func releaseOrphan(name string, force bool, ctx *core.Config, env platform.Env, cwd string) bool {
	orphan, ok := core.FindOrphan(name, ctx, env, cwd)
	if !ok {
		return false
	}
	state := ""
	if !force {
		state = herdr.AgentState(name, env, herdr.Timeout, nil).State
	}
	if !force && state != "idle" && state != "done" {
		core.DieFriction(fmt.Sprintf("agent '%s' is not in the roster and is %s; pass --force to close its pane", name, state), 10, frictionLogPath, "release")
	}
	if herdr.PaneClose(orphan.Pane, env) {
		core.ReleasedPanesForget(ctx, env, cwd, orphan.Pane)
		_, _ = fmt.Fprintf(platform.Stdout, "released %s (pane %s closed; it was no longer in the roster)\n", name, orphan.Pane)
	} else {
		core.DieFriction(fmt.Sprintf("release: could not close the pane %s of '%s'; close it by hand with: herdr pane close %s", orphan.Pane, name, orphan.Pane), 4, frictionLogPath, "release")
	}
	return true
}

func releaseAutoRegrid(ctx *core.Config, env platform.Env, cwd string) {
	message := "regrid after release failed; panes left as they are (see friction)"
	oldOut, oldErr := platform.Stdout, platform.Stderr
	failed := (*platform.ExitError)(nil)
	func() {
		defer func() {
			platform.Stdout, platform.Stderr = oldOut, oldErr
			if value := recover(); value != nil {
				exitErr, ok := value.(*platform.ExitError)
				if !ok {
					panic(value)
				}
				failed = exitErr
			}
		}()
		platform.Stdout, platform.Stderr = releaseDiscardWriter{}, releaseDiscardWriter{}
		layout.CmdRegrid(nil, ctx, env, cwd)
	}()
	if failed != nil {
		core.RecordFrictionError(failed.Msg, failed.Code, frictionLogPath, "release")
		core.Warn(message, frictionLogPath, "release")
	}
}

type releaseDiscardWriter struct{}

func (releaseDiscardWriter) Write(value []byte) (int, error) { return len(value), nil }

// releaseProcs stops the released agent's registered processes at the same
// point the roster is cleaned. A held registry lock, a stop error and a
// registry re-write failure all become warnings and keep the registry
// line: the release never fails for this (like the copies), and the line
// stays for gc.
func releaseProcs(sd, agent string, keepProcs bool, env platform.Env) {
	if keepProcs {
		return
	}
	rows, err := core.ReadProcs(sd)
	if err != nil {
		core.Warn(fmt.Sprintf("release: the processes of '%s' were not stopped (%v)", agent, err), frictionLogPath, "release")
		return
	}
	if unlock, lockErr := core.LockProcs(sd); lockErr != nil {
		core.Warn(fmt.Sprintf("release: the processes of '%s' were not stopped (%v); run 'soho gc' later", agent, lockErr), frictionLogPath, "release")
		return
	} else {
		unlock()
	}
	for _, row := range rows {
		if row.Owner != agent {
			continue
		}
		res := core.StopProcRow(sd, row, env)
		if res.Line != "" {
			_, _ = fmt.Fprintln(platform.Stdout, res.Line)
		}
		if res.Err != nil {
			core.Warn(fmt.Sprintf("release: could not stop process %d of '%s' (%v); the registry line was kept", row.Pid, agent, res.Err), frictionLogPath, "release")
		}
	}
}

func reportEmpty(report string) bool {
	if report == "" {
		return true
	}
	info, err := os.Stat(report)
	return err != nil || info.Size() == 0
}

// collaborationReleaseRefusal refuses to release a participant of an active
// collaboration: code 10 (the lane is busy) even with --force — the
// orchestrator stops the collaboration and finalizes it explicitly first,
// because a release must not fake the cycle's outcome. A registration that
// cannot be read, or whose participants cannot be verified against the live
// roster and Herdr, is code 4: inspect it before releasing anything.
func collaborationReleaseRefusal(sd, agent, pane string, ctx *core.Config, env platform.Env, cwd string) (int, string) {
	store := collaboration.Store{StateDir: sd}
	a, err := store.ActiveFor(pane)
	if err != nil {
		return 4, fmt.Sprintf("release: the collaboration state of pane %s is unreadable (%v); inspect it before releasing %s", pane, err, agent)
	}
	if a == nil {
		return 0, ""
	}
	if env.Get("HERDR_WORKSPACE_ID") == "" {
		env = env.Clone()
		env["HERDR_WORKSPACE_ID"] = core.WorkspaceID(ctx, env, cwd)
	}
	if err := collaboration.CheckParticipants(*a, sd, env); err != nil {
		return 4, fmt.Sprintf("release: the participants of collaboration %s cannot be verified (%v); inspect before releasing %s", a.ID, err, agent)
	}
	return 10, fmt.Sprintf("release: %s is a participant of active collaboration %s (phase %s, round %d); stop it and finalize it explicitly first (soho collaborate stop %s, then soho collaborate finalize %s --verdict accept|reject): release never fakes the cycle's outcome", agent, a.ID, a.Phase, a.Round, a.ID, a.ID)
}

// releaseCopies removes the released agent's registered copies at the same
// point the roster is cleaned. A copy that fails the decision-7 check or
// that cannot be removed keeps its registry line, with a warning; the
// release's messages, order and exit code never change. With keepCopies it
// leaves every line: the copies become orphans for gc. The lock gates the
// destructive phase and covers the registry re-read and re-write, not the
// removals. The roster is already cleaned when this runs, so a registry held
// by another soho never fails the release: the copies stay registered,
// with a warning, and become orphans for gc.
func releaseCopies(sd, agent string, keepCopies bool, env platform.Env, cwd string) {
	if keepCopies {
		return
	}
	rows, err := core.ReadCopies(sd)
	if err != nil {
		_, _ = fmt.Fprintf(platform.Stderr, "release: kept copies registry (%v)\n", err)
		return
	}
	if unlock, lockErr := core.LockCopies(sd); lockErr != nil {
		core.Warn(fmt.Sprintf("release: the copies of '%s' were not removed (%v); run 'soho gc' later", agent, lockErr), frictionLogPath, "release")
		return
	} else {
		unlock()
	}
	removed := map[string]bool{}
	for _, row := range rows {
		if row.Owner != agent {
			continue
		}
		// The worker already deleted this copy (its role asks it to): the
		// line only leaves the registry, as gc would drop it (G2).
		if _, err := os.Lstat(row.Path); errors.Is(err, fs.ErrNotExist) {
			removed[row.Path] = true
			_, _ = fmt.Fprintf(platform.Stdout, "dropped missing copy %s\n", row.Path)
			continue
		}
		resolved, parentInfo, cause := checkedCopy(row.Path, env, cwd)
		if cause != "" {
			_, _ = fmt.Fprintf(platform.Stderr, "release: kept copy %s (%s)\n", row.Path, cause)
			continue
		}
		if err := removeCopy(resolved, parentInfo); err != nil {
			if errors.Is(err, errCopyChanged) {
				_, _ = fmt.Fprintf(platform.Stderr, "release: kept copy %s (changed after the check)\n", row.Path)
			} else {
				_, _ = fmt.Fprintf(platform.Stderr, "release: kept copy %s (%v)\n", row.Path, err)
			}
			continue
		}
		removed[row.Path] = true
		_, _ = fmt.Fprintf(platform.Stdout, "removed copy %s\n", row.Path)
	}
	if len(removed) == 0 {
		return
	}
	if err := core.DropCopiesLines(sd, removed); err != nil {
		// The removed copies' lines read as missing and gc drops them.
		_, _ = fmt.Fprintf(platform.Stderr, "release: could not update the copy registry (%v); 'soho gc' drops the removed lines\n", err)
	}
}
