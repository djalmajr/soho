package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/backend"
	"github.com/djalmajr/soho/internal/collaboration"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/spawn"
	"github.com/djalmajr/soho/internal/transport"
)

var promoteProcRowState = core.ProcRowState

// promote's exit codes:
//
//	0 promoted (or already promoted, verified)
//	2 usage error, or no herdr environment (promote takes no arguments at
//	  all: no target, no bypass flags)
//	3 the caller is refused: a foreign caller, a roster row of another role,
//	  an active collaboration, an unresolved assigned task or a report
//	  pointer that does not name a readable nonempty regular report, or
//	  caller-owned resources (copies still present, processes still running)
//	4 the scope is unavailable, stale or unverifiable; the roster is
//	  corrupt, unreadable or changed between the check and the write; an
//	  assigned task file or report pointer is unreadable; the resource
//	  registries are unreadable, a copy cannot be proved missing, or a
//	  process state is unreadable; an obligation fails the revalidation run
//	  inside the roster lock immediately before the removal
//	6 partial: the worker row is gone but the rename or the title failed;
//	  retry promote (never a silent success)
const (
	promoteRefused = 3
	promoteScope   = 4
	promotePartial = 6
)

// cmdPromote promotes the calling agent to the orchestrator of its
// workspace, only when it is registered as a sub-orchestrator in that
// workspace's roster. The caller's identity comes from the native server,
// not from the inherited environment: herdr.PaneCurrent resolves the
// actual pane, tab and workspace before any roster or state decision,
// because the HERDR_* variables of a moved process keep naming the pane's
// old id. The roster is then read strictly and boundedly (an existing but
// unreadable roster refuses fail closed, an absent roster is an empty
// roster), the live agent is cross-checked against the roster row and the
// project scope (fail closed), and the open obligations — an active or
// unreadable collaboration, the assigned task/report, caller-owned copies
// and processes — are checked and rechecked inside the roster lock
// immediately before the locked removal. The exactly-matching worker row
// is removed under the roster lock with the full row revalidated, the
// agent is renamed to the configured orchestrator name when its name is
// generic (the existing native unique-name rule), and the pane gets the
// orchestrator title by the existing title rule.
//
// It never releases, closes, moves or stops the caller or any other
// resource: caller-owned copies must be finished or handed off first
// (removing the row would turn them into GC orphans), as must any
// caller-owned running process. Working state is not a refusal: the agent
// status is read for the report only.
func cmdPromote(argv []string, ctx *core.Config, env platform.Env, cwd string) int {
	if len(argv) != 0 {
		platform.Die("promote: acts on the calling sub-orchestrator and takes no arguments (no target, no bypass flags)", 2)
	}
	herdr.RequireEnv(env, platform.Current(), os.Getpid(), nil)
	svc := backend.New(env)

	// Canonical caller scope: the native server's view of this process's
	// pane, not the inherited HERDR_* ids (they stay stale for a moved
	// pane). Unavailable or missing context is a refusal before any
	// mutation: no guessing.
	rep := svc.Runtime.Current(context.Background(), transport.Call{})
	if rep.Err != nil {
		platform.Die("promote: cannot resolve the native caller context (herdr pane current failed); nothing was changed", promoteScope)
	}
	if rep.Value.PaneID == "" || rep.Value.WorkspaceID == "" {
		platform.Die("promote: the native caller context has no pane id or workspace id; nothing was changed", promoteScope)
	}
	if inheritedWs := svc.Runtime.Caller().WorkspaceID; inheritedWs != "" && inheritedWs != rep.Value.WorkspaceID {
		platform.Die(fmt.Sprintf("promote: stale cross-workspace context: the inherited workspace '%s' does not match the native workspace '%s' of pane '%s'; finish or hand off the work in both workspaces before promoting; nothing was changed", inheritedWs, rep.Value.WorkspaceID, rep.Value.PaneID), promoteScope)
	}
	pane, tab, ws := rep.Value.PaneID, rep.Value.TabID, rep.Value.WorkspaceID

	sd := filepath.Join(core.StateRootPath(ctx, env, cwd), ws)
	want := core.Cfg(ctx, "orchestrator_name", "orchestrator", env)

	// Roster case: the roster is read strictly and boundedly — an absent
	// agents.tsv is an empty roster, but an existing unreadable one
	// refuses before the live agent, the obligations, the title and any
	// success JSON (fail closed: it is never an empty roster, never
	// already-promoted). The caller's pane must hold at most one row (more
	// than one is corrupt), and if it holds one, that row must be a
	// sub-orchestrator row. No row means the caller already holds (or
	// claims) the orchestrator identity and is verified below.
	lines, rerr := core.StrictRosterLines(sd)
	if rerr != nil {
		platform.Die(fmt.Sprintf("promote: %v; nothing was changed", rerr), promoteScope)
	}
	var row string
	matches := 0
	for _, line := range lines {
		if strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) > 1 && f[1] == pane {
			matches++
			row = line
		}
	}
	if matches > 1 {
		platform.Die(fmt.Sprintf("promote: the roster holds %d rows for pane %s; the roster is corrupt; nothing was changed", matches, pane), promoteScope)
	}
	registeredName := ""
	if matches == 1 {
		rf := strings.Split(row, "\t")
		registeredName = rf[0]
		if registeredName == "" {
			platform.Die(fmt.Sprintf("promote: the roster row for pane %s has no name; the roster is corrupt; nothing was changed", pane), promoteScope)
		}
		role := ""
		if len(rf) > 3 {
			role = rf[3]
		}
		if role != "sub-orchestrator" {
			platform.Die(fmt.Sprintf("promote: the caller pane %s is registered as '%s' with role '%s'; promote acts only on the calling sub-orchestrator", pane, registeredName, role), promoteRefused)
		}
	}

	// Live identity and scope, fail closed: the live agent must answer for
	// the actual pane, the actual workspace, and this invocation's project
	// root.
	liveName, liveStatus := promoteLiveAgent(pane, ws, cwd, env, svc)
	if matches == 1 {
		if liveName != registeredName {
			platform.Die(fmt.Sprintf("promote: sub-orchestrator identity changed: the roster row holds '%s' but the live agent is '%s'; nothing was changed; inspect the pane", registeredName, liveName), promoteScope)
		}
	} else if !promoteNameIsOrchestrator(liveName, want) {
		platform.Die(fmt.Sprintf("promote: the live agent '%s' is not the orchestrator ('%s' or a '%s'-N name) and holds no sub-orchestrator row; nothing was changed", liveName, want, want), promoteRefused)
	}

	// Refusals, all before any mutation: an active or unreadable
	// collaboration, an unresolved assigned task or report, and the
	// resource preflight (removing the row would turn the caller's
	// registered copies into orphans and its running processes into gc
	// orphans while they still hold). The same obligations are revalidated
	// inside the roster lock immediately before the locked removal, so
	// changes observed before that final check refuse promotion. The roster
	// lock does not synchronize writers in the other registries, so the
	// cross-registry window is reduced, not closed. Working state is not a
	// refusal (liveStatus is reported,
	// not branched on).
	// The queued registrations for this state dir join the registries
	// before the copy and process preflights read them (DJA-323).
	incorporatePendingCopies("promote", sd, env, cwd)
	incorporatePendingProcs("promote", sd, env)
	if refusal, code := promoteObligations(sd, liveName, pane, env); refusal != "" {
		platform.Die(refusal, code)
	}

	rowResult := "absent"
	if matches == 1 {
		removed, remErr := core.RosterRemoveIf(sd, pane, row, func() (string, int) {
			return promoteObligations(sd, liveName, pane, env)
		})
		var refusal *core.RosterRemoveRefusal
		if errors.As(remErr, &refusal) {
			platform.Die(refusal.Msg, refusal.Code)
		}
		if remErr != nil {
			platform.Die("promote: "+remErr.Error()+"; the roster keeps its previous content (atomic write); nothing was promoted; retry", promoteScope)
		}
		if !removed {
			platform.Die(fmt.Sprintf("promote: the roster row for pane %s changed or vanished between the check and the write; nothing was written; retry", pane), promoteScope)
		}
		rowResult = "removed"
	}

	name := liveName
	if !promoteNameIsOrchestrator(name, want) {
		newName := spawn.UniqueName(want, env)
		ren := svc.Legacy.Rename(context.Background(), transport.Target{ID: pane}, newName, transport.Call{Timeout: herdr.Timeout})
		if ren.Err != nil {
			cause := strings.TrimSpace(ren.Process.Stderr)
			if cause == "" {
				cause = strings.TrimSpace(ren.Process.Stdout)
			}
			if cause == "" {
				cause = "herdr agent rename failed"
			}
			platform.Die(fmt.Sprintf("promote: partial — the worker roster row was removed, but the rename to '%s' failed (%s); the pane is no longer a worker. Run soho init in this pane (or rename the agent to '%s' or a '%s'-N name) and run promote again for the title; nothing was closed or moved", newName, cause, want, want), promotePartial)
		}
		name = newName
	}

	title, kept := promoteApplyOrchestratorTitle(pane, cwd, rowResult, env, svc)
	out := jsonjs.O("pane_id", pane, "tab_id", tab, "name", name, "status", liveStatus, "role", "orchestrator", "roster_row", rowResult, "title", title, "title_kept", kept)
	if matches == 0 {
		out.Set("already_promoted", true)
	}
	_, _ = fmt.Fprintln(platform.Stdout, jsonjs.StringifyIndent(out, 2))
	return 0
}

// promoteObligations checks every open obligation the promotion must not
// outlive, in the preflight order: an active or unreadable collaboration,
// the assigned task and report, then the copy and process preflights.
// It returns the first refusal (message and exit code) or an empty message
// when every obligation holds. It is run twice: as the preflight before
// any mutation, and again as the narrow callback inside the roster lock
// immediately before the locked removal (the copies and procs registries
// have their own locks, which the roster lock does not hold, so the
// revalidation runs as close to the write as the lock allows).
func promoteObligations(sd, liveName, pane string, env platform.Env) (string, int) {
	active, stateErr := (collaboration.Store{StateDir: sd}).ActiveFor(pane)
	if stateErr != nil {
		return "promote: collaboration state is unreadable: " + stateErr.Error() + "; nothing was changed", promoteScope
	}
	if active != nil {
		return fmt.Sprintf("promote: the caller has active collaboration %s (phase %s); finish it before promoting", active.ID, active.Phase), promoteRefused
	}
	if refusal, code := promoteUnresolvedTask(sd, liveName); refusal != "" {
		return refusal, code
	}
	if refusal, code := promoteCopyPreflight(sd, liveName, pane); refusal != "" {
		return refusal, code
	}
	if refusal, code := promoteProcPreflight(sd, liveName, pane, env); refusal != "" {
		return refusal, code
	}
	return "", 0
}

// promoteCopyPreflight reads the copy registry (read-only, no lock is
// needed for a refusal preflight: the removal that would orphan the copies
// happens later under its own lock, and the obligations are revalidated
// there). It returns a refusal when a
// caller-owned copy still exists (exit 3: a refusal of the caller's state)
// or when a copy cannot be proved missing / the registry is unreadable
// (exit 4: fail closed). Missing copies may keep their historical
// records. An empty refusal means the preflight passed.
func promoteCopyPreflight(sd, liveName, pane string) (string, int) {
	rows, err := core.ReadCopies(sd)
	if err != nil {
		return fmt.Sprintf("promote: the copies registry is unreadable: %v; nothing was changed", err), promoteScope
	}
	var present []string
	for _, row := range rows {
		if row.Owner != liveName && row.Pane != pane {
			continue
		}
		_, statErr := os.Stat(row.Path)
		if statErr == nil {
			present = append(present, row.Path)
			continue
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return fmt.Sprintf("promote: the caller-owned copy %s cannot be proved missing (%v); nothing was changed", row.Path, statErr), promoteScope
		}
	}
	if len(present) > 0 {
		return fmt.Sprintf("promote: the caller-owned copies still exist: %s; finish or hand them off before promoting", strings.Join(present, ", ")), promoteRefused
	}
	return "", 0
}

// promoteProcPreflight reads the process registry (read-only). It returns a
// refusal when a caller-owned process is still running (exit 3: a refusal
// of the caller's state) or its state is unreadable (exit 4: fail closed).
// Proven gone or reused lines may keep their historical records.
func promoteProcPreflight(sd, liveName, pane string, env platform.Env) (string, int) {
	rows, err := core.ReadProcs(sd)
	if err != nil {
		return fmt.Sprintf("promote: the procs registry is unreadable: %v; nothing was changed", err), promoteScope
	}
	var running, unreadable []string
	for _, row := range rows {
		if row.Owner != liveName && row.Pane != pane {
			continue
		}
		switch promoteProcRowState(row, env) {
		case "running":
			running = append(running, fmt.Sprintf("%d (%s)", row.Pid, row.Name))
		case "unknown":
			unreadable = append(unreadable, fmt.Sprintf("%d", row.Pid))
		}
	}
	if len(unreadable) > 0 {
		return fmt.Sprintf("promote: the caller-owned processes %s are unreadable; their state cannot be verified; nothing was changed", strings.Join(unreadable, ", ")), promoteScope
	}
	if len(running) > 0 {
		return fmt.Sprintf("promote: the caller-owned processes are still running: %s; finish or hand them off before promoting", strings.Join(running, ", ")), promoteRefused
	}
	return "", 0
}

// promoteLiveAgent returns the live agent's name and status for pane,
// failing closed when the agent cannot be read or reports a different pane,
// workspace or project root (an unavailable, stale or mismatched scope).
func promoteLiveAgent(pane, ws, cwd string, env platform.Env, svc transport.Services) (name, status string) {
	r := svc.Legacy.AgentGetRaw(context.Background(), transport.Target{ID: pane}, transport.Call{Timeout: 5 * time.Second})
	if r.Process.NotFound {
		platform.Die("herdr CLI not found in PATH", 2)
	}
	if r.Process.TimedOut || r.Err != nil {
		platform.Die(fmt.Sprintf("promote: cannot verify the live agent of pane %s (herdr agent get failed); nothing was changed; retry", pane), promoteScope)
	}
	var payload struct {
		Result struct {
			Agent struct {
				Name      string `json:"name"`
				Pane      string `json:"pane_id"`
				Workspace string `json:"workspace_id"`
				Cwd       string `json:"cwd"`
				Status    string `json:"agent_status"`
			} `json:"agent"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(r.Value), &payload); err != nil {
		platform.Die(fmt.Sprintf("promote: cannot parse the live agent of pane %s (unparseable agent get output); nothing was changed", pane), promoteScope)
	}
	ag := payload.Result.Agent
	if ag.Pane != pane {
		platform.Die(fmt.Sprintf("promote: the live agent reports pane %s, not the caller pane %s (stale scope); nothing was changed", ag.Pane, pane), promoteScope)
	}
	if ag.Workspace != ws {
		platform.Die(fmt.Sprintf("promote: the live agent reports workspace %s, not %s (stale scope); nothing was changed", ag.Workspace, ws), promoteScope)
	}
	if !filepath.IsAbs(ag.Cwd) || platform.StateProjectRoot(env, ag.Cwd) != platform.StateProjectRoot(env, cwd) {
		platform.Die(fmt.Sprintf("promote: the live agent works in a different project root than this invocation (mismatched scope); nothing was changed"), promoteScope)
	}
	return ag.Name, ag.Status
}

// promoteNameIsOrchestrator is the existing native name rule (the same
// pattern the collaboration orchestrator check applies): the configured
// orchestrator name, or that name plus a numeric unique-name suffix.
func promoteNameIsOrchestrator(name, want string) bool {
	if name == want {
		return true
	}
	suffix := strings.TrimPrefix(name, want+"-")
	if suffix == name {
		return false
	}
	n, err := strconv.Atoi(suffix)
	return err == nil && n > 0
}

// promoteReadLimit bounds the task and report-pointer files the promote
// reader accepts: a larger file is not a task or a pointer, and the read
// fails closed instead of parsing an unbounded file.
const promoteReadLimit int64 = 1 << 20

// boundedRead reads file exactly once, bounded by its stat size (at most
// info.Size()+1 bytes; reading more proves the file grew between the stat
// and the read). An absent file returns os.ErrNotExist; any other stat,
// open or read error propagates — a read failure is never swallowed.
func boundedRead(file string, limit int64) ([]byte, error) {
	info, err := os.Stat(file)
	if err != nil {
		return nil, err
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("%s holds %d bytes (limit %d)", filepath.Base(file), info.Size(), limit)
	}
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, info.Size()+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > info.Size() {
		return nil, fmt.Errorf("%s grew while it was read", filepath.Base(file))
	}
	return raw, nil
}

// promoteUnresolvedTask applies the existing taskreport/completion rules to
// the caller's assigned task, fail closed: an absent task file means no
// assigned task; an existing but unreadable task file refuses (exit 4),
// never "no task". A task file is resolved only when the completion marker
// (the one core.MarkTaskDone writes) is present and the last-report pointer
// names a report (promoteReportPointer); an unreadable pointer file
// refuses (exit 4), whitespace, relative, missing, empty, non-regular or
// unreadable pointer targets do not resolve the task (exit 3).
func promoteUnresolvedTask(sd, agent string) (string, int) {
	raw, err := boundedRead(filepath.Join(sd, "task-"+agent), promoteReadLimit)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", 0
		}
		return fmt.Sprintf("promote: the assigned task file of '%s' is unreadable: %v; nothing was changed", agent, err), promoteScope
	}
	text := strings.TrimRight(string(raw), "\n")
	title := text
	if i := strings.IndexByte(title, '\n'); i >= 0 {
		title = title[:i]
	}
	resolved := strings.HasSuffix(text, " ✓")
	report, refusal, pcode := promoteReportPointer(sd, agent)
	if refusal != "" {
		return refusal, pcode
	}
	switch {
	case resolved && report != "":
		return "", 0
	case !resolved && report == "":
		return fmt.Sprintf("promote: the assigned task '%s' is not resolved (no completion marker, no report); finish it before promoting", title), promoteRefused
	case !resolved:
		return fmt.Sprintf("promote: the assigned task '%s' has a report but no completion marker; finish it before promoting", title), promoteRefused
	default:
		return fmt.Sprintf("promote: the assigned task '%s' is done but its report pointer is empty; deliver the report before promoting", title), promoteRefused
	}
}

// promoteReportPointer reads the last-report pointer for agent, strictly:
// an absent pointer file means an empty pointer; an existing but unreadable
// pointer file is a fail-closed refusal (exit 4). A nonempty pointer must
// name an absolute, existing, readable, nonempty, regular file (the same
// rule reportEmpty and the collaboration ValidReport apply to a delivered
// report); anything else — a whitespace-only pointer, a relative path, a
// missing file, a non-regular target, an unreadable or empty target — is
// an unresolved-task refusal (exit 3). The target's readability is proved
// by reading one byte of a nonempty report. Opening alone does not prove
// data is readable, for example when another handle holds a byte-range lock.
func promoteReportPointer(sd, agent string) (pointer string, refusal string, code int) {
	raw, err := boundedRead(core.LastReportPath(sd, agent), promoteReadLimit)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", "", 0
		}
		return "", fmt.Sprintf("promote: the report pointer of the assigned task '%s' is unreadable: %v; nothing was changed", agent, err), promoteScope
	}
	pointer = strings.TrimSpace(string(raw))
	if pointer == "" {
		return "", "", 0
	}
	if !filepath.IsAbs(pointer) {
		return pointer, fmt.Sprintf("promote: the assigned task '%s' report pointer %q is not an absolute path; deliver the report before promoting", agent, pointer), promoteRefused
	}
	info, err := os.Stat(pointer)
	if err != nil {
		return pointer, fmt.Sprintf("promote: the assigned task '%s' report pointer %q does not name an existing file; deliver the report before promoting", agent, pointer), promoteRefused
	}
	if !info.Mode().IsRegular() {
		return pointer, fmt.Sprintf("promote: the assigned task '%s' report pointer %q is not a regular file; deliver the report before promoting", agent, pointer), promoteRefused
	}
	target, err := os.Open(pointer)
	if err != nil {
		return pointer, fmt.Sprintf("promote: the assigned task '%s' report pointer %q is not readable; deliver the report before promoting", agent, pointer), promoteRefused
	}
	if info.Size() == 0 {
		_ = target.Close()
		return pointer, fmt.Sprintf("promote: the assigned task '%s' report pointer %q names an empty report; deliver the report before promoting", agent, pointer), promoteRefused
	}
	var firstByte [1]byte
	_, readErr := target.Read(firstByte[:])
	closeErr := target.Close()
	if readErr != nil || closeErr != nil {
		return pointer, fmt.Sprintf("promote: the assigned task '%s' report pointer %q is not readable; deliver the report before promoting", agent, pointer), promoteRefused
	}
	return pointer, "", 0
}

// promoteTitle applies the orchestrator title rule to the existing title:
// a blank title gets the project name (the existing init rule); an existing
// "orchestrator: " title is kept exactly as it is; a "sub-orchestrator: "
// title keeps its objective with the role prefix replaced by
// "orchestrator: "; another nonempty custom title is preserved as the
// objective under "orchestrator: ". The objective passes the existing
// normalizeObjective length and newline rules. changed is false only when
// the existing title is kept untouched.
func promoteTitle(existing, projectName string) (title string, changed bool) {
	blank := "orchestrator: " + projectName
	if existing == "" {
		return blank, true
	}
	if strings.HasPrefix(existing, "orchestrator: ") {
		return existing, false
	}
	objective := existing
	switch {
	case existing == "sub-orchestrator:" || existing == "sub-orchestrator: ":
		objective = ""
	case strings.HasPrefix(existing, "sub-orchestrator: "):
		objective = existing[len("sub-orchestrator: "):]
	}
	objective = normalizeObjective(objective)
	if objective == "" {
		return blank, true
	}
	return "orchestrator: " + objective, true
}

// promoteApplyOrchestratorTitle reads the pane title and applies
// promoteTitle. A read or set failure is reported precisely as a partial,
// retryable action and never closes the caller.
func promoteApplyOrchestratorTitle(pane, cwd, rowResult string, env platform.Env, svc transport.Services) (title string, kept bool) {
	projectName := filepath.Base(platform.ProjectRoot(env, cwd))
	r := svc.Legacy.PaneGetRaw(context.Background(), transport.Target{ID: pane}, transport.Call{Timeout: herdr.Timeout})
	if r.Err != nil {
		platform.Die(fmt.Sprintf("promote: partial — the roster row is %s, but the pane title could not be read (herdr pane get failed); retry promote to set the title; nothing was closed or moved", rowResult), promotePartial)
	}
	var payload struct {
		Result struct {
			Pane struct {
				Title string `json:"title"`
			} `json:"pane"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(r.Value), &payload); err != nil {
		platform.Die(fmt.Sprintf("promote: partial — the roster row is %s, but the pane title could not be read (unparseable pane get output); retry promote to set the title; nothing was closed or moved", rowResult), promotePartial)
	}
	existing := payload.Result.Pane.Title
	next, changed := promoteTitle(existing, projectName)
	if !changed {
		return next, true
	}
	if !svc.Window.Title(context.Background(), pane, &next, transport.Call{}).Value {
		platform.Die(fmt.Sprintf("promote: partial — the roster row is %s and the name is set, but the title set failed (herdr pane report-metadata); retry promote; nothing was closed or moved", rowResult), promotePartial)
	}
	return next, false
}
