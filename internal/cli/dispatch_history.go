package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/provider"
	"github.com/djalmajr/soho/internal/taskreport"
	waitpkg "github.com/djalmajr/soho/internal/wait"
)

// This file keeps the durable task link of the dispatch sidecars
// (task_report + brief_sha256) and the one-send-once guard: the same brief
// for the agent's open task is never sent twice (s72). dispatch.go only
// calls into it and parses the --resend flag.

// sha256Hex returns the lowercase hex digest of data, as sha256sum prints it.
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// taskReportAfterDispatch returns the value the agent's task report pointer
// (task-report-<agent>.json) will carry in task_report after the dispatch
// updates it: the new task's stable report for a fresh dispatch, the current
// task's one for an amendment. The field identifies the task, the same for
// the original brief and all its amendments.
func taskReportAfterDispatch(sd, report string, amend bool, prior *jsonjs.Object) string {
	if amend && prior != nil {
		if v, ok := prior.Get("task_report"); ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return filepath.Join(sd, "reports", strings.TrimSuffix(filepath.Base(report), ".md")+".current.md")
}

// dispatchDuplicate is a member of the agent's current task that already
// carries the brief the dispatch is about to send, plus the pointer's
// current: the report the worker will write, which the duplicate dispatch
// waits on.
type dispatchDuplicate struct {
	composed    string // the pointer's current member's composed brief (what the worker runs)
	report      string // the pointer's current: the task's current report
	duplicateOf string // the matched member's composed brief (the duplicate_of value)
	taskReport  string // the pointer's task_report
	amendment   bool   // the current member's brief is an amendment
}

// memberTaskPaths maps a task member's report path (from the pointer's
// history plus current) to its composed brief and its attempt sidecar. The
// report lives in <state>/reports, or in the tmp reports dir for a worker
// whose cwd is outside the project; the composed brief and the sidecar live
// next to each other in <state>/briefs or in that same tmp dir.
func memberTaskPaths(sd, member string) (composed, sidecar string, ok bool) {
	base := strings.TrimSuffix(filepath.Base(member), ".md")
	for _, dir := range []string{filepath.Join(sd, "briefs"), filepath.Dir(member)} {
		sc := filepath.Join(dir, base+".dispatch.json")
		st, err := os.Stat(sc)
		if err != nil || !st.Mode().IsRegular() {
			continue
		}
		for _, ext := range []string{".brief.md", ".md"} {
			c := filepath.Join(dir, base+ext)
			if filepath.Clean(c) == filepath.Clean(member) {
				break // A missing composed brief never turns the report into one.
			}
			if cst, cerr := os.Stat(c); cerr == nil && cst.Mode().IsRegular() {
				return c, sc, true
			}
		}
	}
	return "", "", false
}

// memberComposedPath maps a task member's report path to its composed brief
// (see memberTaskPaths), without requiring the sidecar to exist.
func memberComposedPath(sd, member string) (string, bool) {
	base := strings.TrimSuffix(filepath.Base(member), ".md")
	for _, dir := range []string{filepath.Join(sd, "briefs"), filepath.Dir(member)} {
		for _, ext := range []string{".brief.md", ".md"} {
			c := filepath.Join(dir, base+ext)
			if filepath.Clean(c) == filepath.Clean(member) {
				break
			}
			if st, err := os.Stat(c); err == nil && st.Mode().IsRegular() {
				return c, true
			}
		}
	}
	return "", false
}

// findDispatchDuplicate looks, among the members of the agent's current task
// (the pointer's history plus current, in dispatch order), for one that is a
// dispatch of the same shape (a plain brief for a plain dispatch, an
// amendment for --amend) and whose sidecar already carries the brief about
// to be sent. A sidecar without the fields (an old dispatch) never matches:
// the re-send keeps happening for it, and an already-reported member never
// matches either: its task is closed and the same brief starts a new task.
func findDispatchDuplicate(sd, agent, briefSHA, role string, amend bool) *dispatchDuplicate {
	if briefSHA == "" {
		return nil
	}
	pointer := taskreport.ReadTaskReportPointer(sd, agent)
	if pointer == nil {
		return nil
	}
	taskReport := ""
	if v, ok := pointer.Get("task_report"); ok {
		if s, ok := v.(string); ok {
			taskReport = s
		}
	}
	if taskReport == "" {
		return nil
	}
	current := ""
	if v, ok := pointer.Get("current"); ok {
		if s, ok := v.(string); ok {
			current = s
		}
	}
	if current == "" {
		return nil
	}
	members := []string{}
	if v, ok := pointer.Get("history"); ok {
		if items, ok := v.([]any); ok {
			for _, item := range items {
				if s, ok := item.(string); ok {
					members = append(members, s)
				}
			}
		}
	}
	if current != "" {
		members = append(members, current)
	}
	// A task whose current report already exists and is not empty is
	// finished: none of its members is a duplicate, and the same brief
	// starts a new task.
	if nonEmpty(current) {
		return nil
	}
	for _, member := range members {
		if nonEmpty(member) {
			continue
		}
		composed, sidecar, ok := memberTaskPaths(sd, member)
		if !ok {
			continue
		}
		raw, err := os.ReadFile(sidecar)
		if err != nil {
			continue
		}
		value, err := jsonjs.Parse(raw)
		if err != nil {
			continue
		}
		obj, ok := value.(*jsonjs.Object)
		if !ok {
			continue
		}
		field := func(key string) string {
			if v, has := obj.Get(key); has {
				if s, isStr := v.(string); isStr {
					return s
				}
			}
			return ""
		}
		if recorded := field("role"); recorded != "" && recorded != role {
			continue
		}
		if field("task_report") != taskReport || field("brief_sha256") != briefSHA || field("submission") != "accepted" || field("arrival") == "not-received" {
			continue
		}
		// A plain dispatch and an amendment are different sends: only a
		// member of the same shape duplicates this one (the orchestrator
		// re-sending the original brief's content with --amend is a new
		// amendment, not a duplicate of the plain dispatch).
		memberAmendment := false
		if body, err := platform.ReadTextFile(composed); err == nil {
			memberAmendment = strings.SplitN(body, "\n", 2)[0] == "# Amendment to your current brief"
		}
		if memberAmendment != amend {
			continue
		}
		// The duplicate waits on the task's current report (the pointer's
		// current), not the matched member's: the worker always reports on
		// the last member of the task.
		dup := &dispatchDuplicate{composed: composed, report: current, duplicateOf: composed, taskReport: taskReport}
		if current != member {
			if c, ok2 := memberComposedPath(sd, current); ok2 {
				dup.composed = c
			}
		}
		if body, err := platform.ReadTextFile(dup.composed); err == nil {
			dup.amendment = strings.SplitN(body, "\n", 2)[0] == "# Amendment to your current brief"
		}
		return dup
	}
	return nil
}

// dispatchDuplicateResult ends a dispatch whose brief the agent already has
// open: nothing is sent, no new composed brief, sidecar, metrics line or
// prompt is created. It prints the stderr line, then, with the default wait,
// the same wait (same codes and JSON, plus duplicate_of) on the task's
// current report; with --no-wait, the JSON with duplicate_of and 0.
func dispatchDuplicateResult(agent, role, kind string, dup *dispatchDuplicate, noWait bool, timeoutRaw string, sd string, ctx *core.Config, env platform.Env, cwd string) int {
	fmt.Fprintf(platform.Stderr, "soho: dispatch: %s already has this brief (%s); waiting on it without sending again (--resend sends it again)\n", agent, dup.duplicateOf)
	approved := 0
	if raw, e := platform.ReadTextFile(filepath.Join(sd, "wait", agent+".approvals")); e == nil {
		approved, _ = strconv.Atoi(strings.TrimSpace(raw))
	}
	if noWait {
		obj := jsonjs.O("wait_status", "submitted", "agent", agent, "role", role, "kind", kind, "composed_prompt", dup.composed, "report", dup.report, "task_report", dup.taskReport, "report_exists", nonEmpty(dup.report), "auto_approved", approved, "duplicate_of", dup.duplicateOf)
		markReportVolatile(obj, dup.report, sd)
		if dup.amendment {
			obj.Set("amend", true)
		}
		fmt.Fprintln(platform.Stdout, jsonjs.Stringify(obj))
		return 0
	}
	// Wait on the task's current report: last-report-<agent> holds the last
	// dispatched report, which is the pointer's current, so the ordinary wait
	// already watches it — no re-pointing.
	old := platform.Stdout
	var buf bytes.Buffer
	platform.Stdout = &buf
	waitpkg.SetFrictionLogFile(frictionLogPath)
	waitCode := dispatchWaitFor([]string{agent}, sd, ctx, env, parseTimeout(timeoutRaw), false, cwd)
	platform.Stdout = old
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	var last *jsonjs.Object
	if len(lines) > 0 {
		if value, e := jsonjs.Parse([]byte(lines[len(lines)-1])); e == nil {
			last, _ = value.(*jsonjs.Object)
		}
	}
	return emitDuplicateDispatchResult(agent, role, kind, dup, last, waitCode, sd)
}

// emitDuplicateDispatchResult mirrors emitDispatchResult for a dispatch that
// waited on a member it did not send: the same status, fields and exit codes,
// plus duplicate_of naming the member's composed brief.
func emitDuplicateDispatchResult(agent, role, kind string, dup *dispatchDuplicate, last *jsonjs.Object, waitCode int, sd string) int {
	status := "submitted"
	if last != nil {
		if v, ok := last.Get("status"); ok {
			if s, yes := v.(string); yes {
				status = s
			}
		}
	}
	out := jsonjs.O("wait_status", status, "agent", agent, "role", role, "kind", kind, "composed_prompt", dup.composed, "report", dup.report, "task_report", dup.taskReport, "duplicate_of", dup.duplicateOf)
	markReportVolatile(out, dup.report, sd)
	settled := ""
	reportPath := dup.report
	if last != nil {
		if v, ok := last.Get("report"); ok {
			if s, yes := v.(string); yes && s != "" && s != dup.report {
				settled = s
				reportPath = s
			}
		}
	}
	if settled != "" {
		out.Set("settled_report", settled)
	}
	out.Set("report_exists", nonEmpty(reportPath))
	if dup.amendment {
		out.Set("amend", true)
	}
	if last != nil {
		for _, key := range []string{"dialog", "verdict", "findings", "severity", "partial", "verdict_effective", "reference_parity"} {
			if v, ok := last.Get(key); ok {
				out.Set(key, v)
			}
		}
	}
	approved := 0
	if raw, e := platform.ReadTextFile(filepath.Join(sd, "wait", agent+".approvals")); e == nil {
		approved, _ = strconv.Atoi(strings.TrimSpace(raw))
	}
	out.Set("auto_approved", approved)
	if last != nil {
		for _, key := range []string{"question", "lane", "model", "match", "renewal", "cause", "retries", "head_moved", "diff_stat", "outside_owned", "cited_unchanged"} {
			if v, ok := last.Get(key); ok {
				out.Set(key, v)
			}
		}
	}
	fmt.Fprintln(platform.Stdout, jsonjs.Stringify(out))
	switch status {
	case "blocked":
		core.Warn(fmt.Sprintf("agent '%s' is blocked on an approval or question; run: herdr agent read %s --source recent-unwrapped --lines 80", agent, agent), frictionLogPath, "dispatch")
		return 7
	case "timeout":
		core.Warn(fmt.Sprintf("timeout waiting for the report of '%s'; it may still be working. Run: soho wait %s", agent, agent), frictionLogPath, "dispatch")
		return 9
	case "settled-no-report", "gone":
		if status == "settled-no-report" {
			core.Warn(fmt.Sprintf("agent '%s' settled without writing %s; collect will fall back to terminal output", agent, dup.report), frictionLogPath, "dispatch")
		} else {
			core.Warn(fmt.Sprintf("agent '%s' is no longer live", agent), frictionLogPath, "dispatch")
		}
		return 6
	case "unavailable":
		var cause any
		if last != nil {
			cause, _ = last.Get("error")
		}
		suffix := ""
		if cause != nil && fmt.Sprint(cause) != "" {
			suffix = ": " + fmt.Sprint(cause)
		}
		core.Warn(fmt.Sprintf("agent '%s': herdr agent get failed%s. The worker may still be live; do not spawn a replacement.", agent, suffix), frictionLogPath, "dispatch")
		return 4
	case "quota":
		match, _ := out.Get("match")
		cause := ""
		if match != nil && fmt.Sprint(match) != "" {
			cause = ": " + fmt.Sprint(match)
		}
		core.Warn(fmt.Sprintf("agent '%s' hit a quota limit%s. Ask the user: switch the lane kind/model, wait for renewal, take the slice, or pause.", agent, cause), frictionLogPath, "dispatch")
		return 11
	case "provider-error":
		cause, _ := out.Get("cause")
		if fmt.Sprint(cause) == provider.HarnessModuleCause {
			core.Warn(fmt.Sprintf("agent '%s' stopped on a harness error: its CLI could not load one of its own files (Error: Cannot find module), often after the CLI was updated while it ran. It is idle without a report; restart it (release --close, then spawn --fresh) and resend the brief.", agent), frictionLogPath, "dispatch")
			return 14
		}
		core.Warn(fmt.Sprintf("agent '%s' stopped on a provider error: %v. It is idle without a report; ask the user whether to resend the brief, switch the assistant, or wait.", agent, cause), frictionLogPath, "dispatch")
		return 14
	case "capacity":
		cause, _ := out.Get("cause")
		retries, _ := out.Get("retries")
		core.Warn(fmt.Sprintf("agent '%s' is still at provider capacity after %v continue(s): %v. Ask the user whether to wait and resend, switch the assistant, or pause.", agent, retries, cause), frictionLogPath, "dispatch")
		return 14
	default:
		return waitCode
	}
}
