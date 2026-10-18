package cli

import (
	"bytes"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/collaboration"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/dispatch"
	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/kinds"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/provider"
	"github.com/djalmajr/soho/internal/spawn"
	"github.com/djalmajr/soho/internal/startup"
	"github.com/djalmajr/soho/internal/stats"
	"github.com/djalmajr/soho/internal/taskreport"
	waitpkg "github.com/djalmajr/soho/internal/wait"
)

var dispatchWaitFiles = []string{"size", "screen", "since", "blocked", "approvals", "quota", "provider", "provider-cause", "capacity-retries", "capacity-at", "question", "stuck-hash", "stuck-since", "stuck-warned", "activity-at", "probe-at", "not-received", "queued", "enter-retry", "approve-screen"}
var dispatchWaitFor = waitpkg.WaitFor
var writeDispatchSidecar = dispatch.WriteSidecar

func cmdDispatch(argv []string, ctx *core.Config, env platform.Env, cwd string) int {
	if len(argv) < 1 || argv[0] == "" {
		fmt.Fprintln(platform.Stderr, "soho: 1: agent")
		return 1
	}
	if len(argv) < 2 || argv[1] == "" {
		fmt.Fprintln(platform.Stderr, "soho: 2: brief.md")
		return 1
	}
	agent, brief := argv[0], argv[1]
	role, timeoutRaw := "", ""
	noWait, allow, amend, compact, resend, queue := false, false, false, false, false, false
	var forValue *string
	for i := 2; i < len(argv); i++ {
		a := argv[i]
		switch a {
		case "--role", "--timeout", "--for":
			if i+1 >= len(argv) {
				core.DieFriction("dispatch: "+a+" expects a value", 2, frictionLogPath, "dispatch")
			}
			i++
			v := argv[i]
			switch a {
			case "--role":
				role = v
			case "--timeout":
				timeoutRaw = v
				if _, ok := core.ParseTimeoutMS(v); !ok {
					core.DieFriction(fmt.Sprintf("dispatch: --timeout expects milliseconds or a duration such as 90s, 30m or 2h, got '%s'", v), 2, frictionLogPath, "dispatch")
				}
				// run forwards its --timeout here, so this is the single warning
				// site: a unit names the intent, so only a unitless integer warns.
				if core.IsPlainTimeoutMS(v) {
					core.WarnShortTimeout("dispatch", frictionLogPath, int64(parseTimeout(v)))
				}
			case "--for":
				forValue = &v
			}
		case "--no-wait":
			noWait = true
		case "--allow-same-family":
			allow = true
		case "--amend":
			amend = true
		case "--compact":
			compact = true
		case "--resend":
			resend = true
		case "--queue":
			queue = true
		case "--cwd":
			// dispatch never opens a worker: its cwd is fixed when spawn opens it.
			core.DieFriction("dispatch: unknown option --cwd (the worker's directory is set when it is opened: spawn <role> --cwd <dir>, then dispatch to it)", 2, frictionLogPath, "dispatch")
		default:
			core.DieFriction("dispatch: unknown option "+a, 2, frictionLogPath, "dispatch")
		}
	}
	if amend && role != "" {
		core.DieFriction("dispatch: --amend keeps the current role; drop --role", 2, frictionLogPath, "dispatch")
	}
	if st, err := os.Stat(brief); err != nil || !st.Mode().IsRegular() {
		core.DieFriction("brief not found: "+brief, 2, frictionLogPath, "dispatch")
	}
	// Resolve the native command before any compact step, task state,
	// collaboration cleanup or interaction with the worker.
	platform.LauncherPath(env)
	sd := core.StateDirPath(ctx, env, cwd)
	line := core.RosterLine(sd, agent)
	if line == "" {
		core.DieFriction(fmt.Sprintf("agent '%s' is not in this skill's roster (spawn it first, or pass a name you spawned)", agent), 3, frictionLogPath, "dispatch")
	}
	if amend {
		if _, err := os.Stat(filepath.Join(sd, "last-report-"+agent)); err != nil {
			core.DieFriction(fmt.Sprintf("dispatch: --amend needs an earlier dispatch to '%s' (nothing to amend)", agent), 2, frictionLogPath, "dispatch")
		}
	}
	cols := strings.Split(line, "\t")
	at := func(i int) string {
		if i < len(cols) {
			return cols[i]
		}
		return ""
	}
	if role == "" {
		role = at(3)
	}
	active, stateErr := (collaboration.Store{StateDir: sd}).ActiveFor(at(1))
	if stateErr != nil {
		core.DieFriction("dispatch: collaboration state is unreadable: "+stateErr.Error(), 4, frictionLogPath, "dispatch")
	}
	if active != nil {
		if env.Get("HERDR_WORKSPACE_ID") == "" {
			env = env.Clone()
			env["HERDR_WORKSPACE_ID"] = core.WorkspaceID(ctx, env, cwd)
		}
		member, _ := active.Member(at(1))
		body, readErr := os.ReadFile(brief)
		if amend || role != member.Role || active.Phase != collaboration.Preparing || readErr != nil || collaboration.Hash(body) != active.BriefHash {
			core.DieFriction("dispatch: active collaboration "+active.ID+" keeps its contract and participant roles; stop and finalize it before another dispatch", 10, frictionLogPath, "dispatch")
		}
		if err := collaboration.CheckParticipants(*active, sd, env); err != nil {
			core.DieFriction("dispatch: "+err.Error(), 4, frictionLogPath, "dispatch")
		}
	}
	kind, family := at(2), at(4)
	roleFile := core.RoleFile(role, env, cwd)
	if roleFile == "" {
		core.DieFriction(fmt.Sprintf("unknown role '%s' (run: soho roles)", role), 3, frictionLogPath, "dispatch")
	}
	// F11: before any state change or send, the recorded compact phase decides
	// the dispatch: a live or unverifiable owner for this pane refuses the
	// send, and a proven-dead owner's or a switched-pane's record is removed.
	// The cursor/Windows startup preflight (issue #59) sits before it: before
	// any prompt, compaction or report/sidecar mutation — independent of
	// prompt_check_seconds, --queue, --resend and --amend — a blocked (the
	// verified workspace trust dialog) or unverified (blank, unreadable or
	// launch-only screen) cursor start on Windows refuses the dispatch with
	// the existing blocked result (exit 7, the cause naming the reason):
	// nothing is typed and no task pointer, report or sidecar is touched.
	// Every other kind or platform skips the check without an extra herdr
	// read, and a safe screen proceeds to the duplicate handling and the
	// send below, unchanged.
	if startup.Enabled(kind) {
		st, screen, screenOK := cursorStartupScreen(agent, env)
		ev := startup.Evaluate(kind, startup.CurrentPlatform(), st.State, screen, screenOK)
		if ev.Refuses() {
			return dispatchCursorStartupRefusal(agent, role, kind, "", "", "", st.State, ev, sd)
		}
	}
	core.CompactPhaseCheck(sd, agent, at(1), env)
	forAuthors, forUnknown := []string{}, []string{}
	var forEntries []any
	if forValue != nil {
		for _, spec := range strings.Split(*forValue, ",") {
			spec = strings.TrimSpace(spec)
			fam, err := dispatch.ForSpecFamily(spec, sd, env, cwd, ctx)
			if err != nil {
				core.DieFriction(err.Error(), 2, frictionLogPath, "dispatch")
			}
			if fam != "" && fam != "unknown" {
				forAuthors = append(forAuthors, spec+" ("+fam+")")
			} else {
				forUnknown = append(forUnknown, spec)
			}
			// The sidecar's "for" array carries the anonymized author (issue
			// #39, part 2): the metrics line copies it, the name never does.
			forEntries = append(forEntries, dispatchForAuthor(spec, sd, fam))
		}
		if !containsWord(core.ReviewRoles, role) {
			core.DieFriction("dispatch: --for applies to a reviewer dispatch", 2, frictionLogPath, "dispatch")
		}
	}
	workerCwd := at(6)
	findings := dispatch.BriefLintFindings(brief, ctx, env, dispatch.BriefLintOptions{ReadOnly: !core.RoleIsEdit(role, env, cwd), WorkerCwd: workerCwd, OrchestratorCwd: cwd})
	if amend {
		findings = dispatch.BriefLintFindings(brief, ctx, env, dispatch.BriefLintOptions{ReadOnly: !core.RoleIsEdit(role, env, cwd), PathsOnly: true, WorkerCwd: workerCwd, OrchestratorCwd: cwd})
	}
	if findings.Mode != "off" {
		for _, w := range findings.Warnings {
			if findings.Mode == "strict" && strings.Contains(w, "which the worker in ") {
				core.DieFriction(w, 2, frictionLogPath, "dispatch")
			}
			core.Warn(w, frictionLogPath, "dispatch")
		}
		if findings.MissingMessage != "" {
			if findings.Mode == "strict" {
				core.DieFriction(findings.MissingMessage+" (brief_lint=strict)", 2, frictionLogPath, "dispatch")
			}
			core.Warn(findings.MissingMessage, frictionLogPath, "dispatch")
		}
	}
	if timeoutRaw == "" {
		timeoutRaw = strconv.FormatInt(core.RoleTimeoutMs(role, ctx, env, cwd), 10)
	}
	familyCheck := core.Cfg(ctx, "family_check", "strict", env)
	if !amend && containsWord(core.ReviewRoles, role) && familyCheck != "off" {
		authorHit := false
		for _, a := range forAuthors {
			if strings.HasSuffix(a, " ("+family+")") {
				authorHit = true
			}
		}
		var scan []string
		if forValue == nil || len(forUnknown) > 0 {
			scan = dispatch.FamilyConflicts(sd, family, env, cwd)
		}
		for _, spec := range forUnknown {
			core.Warn(fmt.Sprintf("dispatch: --for '%s': the author's family is unknown; checked against every edit agent instead", spec), frictionLogPath, "dispatch")
		}
		if authorHit || len(scan) > 0 {
			if allow || familyCheck == "warn" {
				who := strings.Join(scan, " ")
				if authorHit {
					who = strings.Join(forAuthors, ", ")
				}
				core.Warn(fmt.Sprintf("reviewer '%s' shares model family '%s' with: %s", agent, family, who), frictionLogPath, "dispatch")
			} else if authorHit {
				core.DieFriction(fmt.Sprintf("reviewer '%s' (%s, %s) shares a model family with the slice's author(s): %s. Spawn the reviewer with another --kind, pass --allow-same-family, or set family_check=warn.", agent, kind, family, strings.Join(forAuthors, ", ")), 5, frictionLogPath, "dispatch")
			} else {
				hint := "Pass --for <author> when the slice was written by another family"
				if forValue != nil {
					hint = "Name the author's family with --for <family> (anthropic|openai|xai|google|alibaba) to narrow the check"
				}
				core.DieFriction(fmt.Sprintf("reviewer '%s' (%s, %s) shares a model family with edit agents: %s. %s, spawn the reviewer with another --kind, pass --allow-same-family, or set family_check=warn.", agent, kind, family, strings.Join(scan, " "), hint), 5, frictionLogPath, "dispatch")
			}
		}
	}
	if !amend {
		warnOwnedOverlap(brief, role, agent, sd, ctx, env, cwd, workerCwd)
	}
	core.StateDir(ctx, env, cwd)
	now := platform.Now()
	stamp := core.NowStamp(now)
	reportsDir := filepath.Join(sd, "briefs")
	reportDir := filepath.Join(sd, "reports")
	// The report and the composed prompt follow the state dir only when the
	// worker can reach it: a sandboxed worker writes inside its own cwd, so
	// with a non-empty worker cwd the files go to the soho scratch dir
	// whenever sd is not inside that cwd — the caller's project root no
	// longer decides. Comparing the worker's cwd with the caller's root
	// routed a worktree caller's report into the main checkout's sd, which
	// the worker there cannot write (DJA-379).
	if workerCwd != "" && !dispatch.PathWithin(sd, workerCwd, platform.Current()) {
		tmp := env.Get("TMPDIR")
		if tmp == "" {
			tmp = os.TempDir()
		}
		reportsDir = filepath.Join(tmp, "soho", core.WorkspaceID(ctx, env, cwd), "reports")
		reportDir = reportsDir
	}
	composedAt := func(s string) string {
		ext := ".md"
		if reportsDir != filepath.Join(sd, "briefs") {
			ext = ".brief.md"
		}
		return filepath.Join(reportsDir, agent+"-"+stamp+s+ext)
	}
	reportAt := func(s string) string { return filepath.Join(reportDir, agent+"-"+stamp+s+".md") }
	lastPath := filepath.Join(sd, "last-report-"+agent)
	lastData, _ := os.ReadFile(lastPath)
	oldReport := strings.TrimSpace(string(lastData))
	suffix := dispatch.DispatchPairSuffix(composedAt, reportAt, func(p string) bool { _, e := os.Stat(p); return e == nil }, oldReport)
	composed, report := composedAt(suffix), reportAt(suffix)
	if err := os.MkdirAll(filepath.Dir(composed), 0o777); err != nil {
		panic(err)
	}
	model, effort, args, lane, session := "", "", "", "", ""
	if len(cols) > 8 {
		model = cols[8]
	}
	if len(cols) > 7 {
		session = cols[7]
	}
	if len(cols) > 14 {
		effort = cols[14]
	}
	if len(cols) > 13 {
		args = cols[13]
	}
	if len(cols) > 11 {
		lane = cols[11]
	}
	// Compact before any task state is written: a busy worker or a timeout
	// leaves no pointer to a report that will never come. compactRun ends
	// some failures (a busy or dead worker, a failed send, a /compact that
	// stayed in the codex composer after two Enters) in a DieFriction panic,
	// which would skip the note below; compactDispatchStep prints the note
	// there and re-raises, so the CLI boundary still prints the compact
	// message and records the friction line.
	if compact {
		if compactProof(kind) == "" {
			core.Warn(fmt.Sprintf("dispatch: --compact skipped: kind '%s' has no verified compact command", kind), frictionLogPath, "dispatch")
		} else if code := compactPhaseDispatch(compactPhaseDispatchOptions{
			Agent: agent, Pane: at(1), Kind: kind, Role: role, Lane: lane, Model: model,
			Brief: brief, SD: sd, TimeoutMS: compactDefaultTimeoutMS, Ctx: ctx, Env: env, Cwd: cwd,
		}); code != 0 {
			// The compaction phase did not finish: say so, and that this brief was
			// not sent, so the orchestrator does not look for it on the screen.
			fmt.Fprintf(platform.Stderr, "soho: dispatch: the compact step of '%s' did not finish (exit %d); the brief was not sent\n", agent, code)
			return code
		}
	}
	// Context check before any task state is written: at long context opencode
	// can end its turn empty. The footer read is opencode-only for now.
	if kind == "opencode" {
		warnPercent := 60
		if raw := core.Cfg(ctx, "context_warn_percent", "60", env); raw != "60" {
			if v, e := strconv.Atoi(raw); e == nil {
				warnPercent = v
			}
		}
		if warnPercent > 0 {
			if pct := dispatchContextPercent(herdr.AgentRead(env, agent, "visible", nil)); pct >= warnPercent {
				core.Warn(fmt.Sprintf("dispatch: '%s' is at %d%% of its context; a long context can end its turn empty (opencode): release --close it and spawn a fresh worker for a new task", agent, pct), frictionLogPath, "dispatch")
			}
		}
	}
	// The one-send-once guard (s72): the same brief for the agent's open task
	// is never sent twice. The sidecar's brief_sha256 is the digest of the
	// brief's raw input bytes, read before the decision, so nothing is
	// written when a member already carries the brief; the normalized text
	// still serves the prompt composition.
	briefRaw, err := platform.ReadTextFile(brief)
	if err != nil {
		panic(err)
	}
	rawBrief, err := os.ReadFile(brief)
	if err != nil {
		panic(err)
	}
	briefSHA := sha256Hex(rawBrief)
	if !resend {
		if dup := findDispatchDuplicate(sd, agent, briefSHA, amend); dup != nil {
			return dispatchDuplicateResult(agent, role, kind, dup, noWait, timeoutRaw, sd, ctx, env, cwd)
		}
	}
	// prompt_check_seconds gates the arrival check below; it also decides
	// whether the dispatch makes its pre-send agent get (a 0 dispatch
	// sends one prompt and probes nothing, as the ported behavior keeps).
	checkRaw := core.Cfg(ctx, "prompt_check_seconds", "15", env)
	checkSecs, checkErr := strconv.Atoi(checkRaw)
	checkOn := checkErr == nil && checkSecs > 0
	// D25: a fresh brief typed while the agent is still on an open task
	// makes its CLI read the brief mid-turn and drop the open task: refuse
	// (exit 10) before typing anything or writing any task state, naming
	// the escapes. --amend amends the open task and --queue opts into
	// typing now (today's queued behavior); a failed agent get
	// (unavailable) and a closed task (the report exists) keep today's
	// behavior. The check rides the dispatch's pre-send state read, which
	// is taken here, before the first sidecar write.
	var before herdr.AgentStateResult
	if checkOn {
		before = herdr.AgentState(agent, env, herdr.Timeout, nil)
		if !amend && !queue && (before.State == "working" || before.State == "blocked") {
			if ptr := taskreport.ReadTaskReportPointer(sd, agent); ptr != nil {
				if cur, ok := ptr.Get("current"); ok {
					if s, isStr := cur.(string); isStr && s != "" && !nonEmpty(s) {
						core.DieFriction(fmt.Sprintf("dispatch: '%s' is still working on its open task (no report at %s); wait for it (soho wait %s), send an amendment (--amend), or pass --queue to type this brief now (its CLI may read it mid-turn and drop the open task)", agent, s, agent), 10, frictionLogPath, "dispatch")
					}
				}
			}
		}
	}
	sidecar := dispatch.DispatchSidecar(composed)
	// priorPointer and taskReport are read before the first sidecar write: the
	// sidecar's task_report is the pointer's value after this dispatch
	// updates it (the new task's stable report, or the current one for an
	// amendment).
	priorPointer := taskreport.ReadTaskReportPointer(sd, agent)
	taskReport := taskReportAfterDispatch(sd, report, amend, priorPointer)
	if err := writeDispatchSidecarFor(forEntries, sidecar, kind, model, effort, taskReport, briefSHA, "attempted", "", session); err != nil {
		return writeDispatchError(agent, role, kind, composed, report, taskReport, "couldn't write the attempt sidecar: "+sanitizeDispatchCause(err.Error()), 4,
			fmt.Sprintf("could not write the attempt sidecar %s: %s", sidecar, sanitizeDispatchCause(err.Error())))
	}
	shared := false
	if live := safeLiveAgents(env); len(live) > 0 {
		shared = sharedTreeEditor(core.RosterRows(sd), live, agent, workerCwd, env, cwd)
	}
	prompt := dispatch.ComposePrompt(roleFile, role, agent, briefRaw, report, ctx, env, kind, args, shared)
	if amend {
		prompt = dispatch.ComposeAmendment(briefRaw, report, ctx, env, kind, args, shared, agent)
	}
	if err := os.WriteFile(composed, []byte(prompt), 0o666); err != nil {
		panic(err)
	}
	// priorPointer and taskReport stand from before the first sidecar write.
	history := []any{}
	if amend {
		if priorPointer != nil {
			if v, ok := priorPointer.Get("history"); ok {
				if xs, ok := v.([]any); ok {
					history = append(history, xs...)
				}
			}
			if v, ok := priorPointer.Get("current"); ok {
				if x, ok := v.(string); ok {
					history = append(history, x)
				}
			}
		} else if oldReport != "" {
			history = append(history, oldReport)
		}
	}
	pointer := jsonjs.O("version", 1, "task_report", taskReport, "current", report, "history", history)
	if err := taskreport.WriteTaskReportPointer(sd, agent, pointer); err != nil {
		panic(err)
	}
	if _, err := taskreport.SyncTaskReport(sd, agent); err != nil {
		panic(err)
	}
	if err := os.WriteFile(lastPath, []byte(report+"\n"), 0o666); err != nil {
		panic(err)
	}
	for _, name := range dispatchWaitFiles {
		_ = os.Remove(filepath.Join(sd, "wait", agent+"."+name))
	}
	text := fmt.Sprintf("Read the file %s in full and execute it. It contains your role, your brief, and your report contract. When finished, write your report to %s and reply with exactly that path and nothing else.", composed, report)
	if amend {
		text = fmt.Sprintf("Read the file %s in full and execute it. It amends the brief you are working on. When finished, write your report to %s and reply with exactly that path and nothing else.", composed, report)
	}
	// before and checkOn stand from before the first sidecar write (D25).
	wasWorking := checkOn && before.State == "working"
	settleSecs := 20
	rawSettle := core.Cfg(ctx, "prompt_settle_seconds", "20", env)
	var settleErr error
	settleSecs, settleErr = strconv.Atoi(rawSettle)
	if settleErr != nil {
		settleSecs = 20
	}
	if settleSecs > 0 && !wasWorking {
		prev := herdr.AgentRead(env, agent, "visible", nil)
		settled := false
		deadline := time.Now().Add(time.Duration(settleSecs) * time.Second)
		for time.Now().Add(500 * time.Millisecond).Before(deadline) {
			time.Sleep(500 * time.Millisecond)
			ready := isInteractiveReady(agent, env)
			curr := herdr.AgentRead(env, agent, "visible", nil)
			if ready && curr == prev {
				settled = true
				break
			}
			prev = curr
		}
		if !settled {
			core.Warn(fmt.Sprintf("prompt to '%s' did not settle in %ds; sending anyway", agent, settleSecs), frictionLogPath, "dispatch")
		}
	}
	preState := before
	if !wasWorking && settleSecs > 0 {
		preState = herdr.AgentState(agent, env, herdr.Timeout, nil)
	}
	H0, preSeq := "", ""
	priorAuth := map[string]bool{}
	if checkOn {
		H0 = strconv.FormatUint(uint64(waitpkg.CksumField(herdr.AgentRead(env, agent, "visible", nil))), 10)
		preSeq = seqString(preState.Seq)
		for _, cause := range authCauses(herdr.AgentRead(env, agent, "recent-unwrapped", intPtr(40))) {
			priorAuth[cause] = true
		}
	}
	// A Claude Code or pi target writes every prompt it takes into its local
	// session transcript as a user line, and a working target can show none
	// of the prompt on screen (the alternate screen scrolls, and the long
	// composed path is split over lines): when the transcript resolves, a
	// rise in the count of those lines holding this prompt's path, read
	// before the send, is arrival proof within the check window.
	// ClaudeTranscriptPath and PiSessionPath resolve with a local `agent get`
	// plus a local file, so a target without a local session file (no
	// agent_session, a kind that is not a local file, a missing file) resolves
	// nothing and keeps the screen-based path, with no new warning. The
	// count is the only thing read from the transcript: no line content is
	// retained, logged, or returned.
	transcriptPath := ""
	transcriptPre := 0
	transcriptArmed := false
	transcriptCount := func(path, marker string) (int, bool) { return 0, false }
	if checkOn && (kind == "claude" || kind == "pi") {
		switch kind {
		case "claude":
			transcriptCount = herdr.CountClaudeUserMarkerLines
		case "pi":
			transcriptCount = herdr.CountPiUserMarkerLines
		}
		var path string
		if kind == "claude" {
			path = herdr.ClaudeTranscriptPath(agent, env)
		} else {
			path = herdr.PiSessionPath(agent, env)
		}
		if path != "" {
			if pre, ok := transcriptCount(path, transcriptPathMarker(composed)); ok {
				transcriptPath = path
				transcriptPre = pre
				transcriptArmed = true
			}
		}
	}
	transcriptArrival := func() bool {
		if !transcriptArmed {
			return false
		}
		count, ok := transcriptCount(transcriptPath, transcriptPathMarker(composed))
		return ok && count > transcriptPre
	}
	// The dialog can land after the preflight (for example during the
	// settle): a recheck right before the actual input — no AgentPrompt and
	// no arrival/retry Enter go to the cursor workspace trust dialog. The
	// send was already claimed (pointer, last-report, the attempted
	// sidecar): restore the task state, like the failed-send path, and end
	// with the blocked result.
	if blocked, ev, st := cursorStartupRefusal(kind, agent, env); blocked {
		_ = restoreTaskDispatch(sd, agent, lastPath, lastData, priorPointer)
		return dispatchCursorStartupRefusal(agent, role, kind, composed, report, taskReport, st.State, ev, sd)
	}
	p := herdr.AgentPrompt(agent, text, env)
	if !p.Ok {
		_ = restoreTaskDispatch(sd, agent, lastPath, lastData, priorPointer)
		// The failed sidecar records the pointer's task_report after the
		// restoration — the task the pointer actually keeps; without a
		// restored pointer the field is absent.
		restoredReport := ""
		if restored := taskreport.ReadTaskReportPointer(sd, agent); restored != nil {
			if v, ok := restored.Get("task_report"); ok {
				if s, isStr := v.(string); isStr {
					restoredReport = s
				}
			}
		}
		if err := writeDispatchSidecarFor(forEntries, sidecar, kind, model, effort, restoredReport, briefSHA, "failed", "", session); err != nil {
			core.Warn(fmt.Sprintf("could not record the failed submission in the attempt sidecar %s: %s", sidecar, dispatchCauseOrUnknown(err)), frictionLogPath, "dispatch")
		}
		return writeDispatchError(agent, role, kind, composed, report, taskReport, p.Raw, 4,
			"prompt submission failed; inspect with: herdr agent get "+agent+" && herdr agent read "+agent+". Do not resend blindly.")
	}
	if err := writeDispatchSidecarFor(forEntries, sidecar, kind, model, effort, taskReport, briefSHA, "accepted", "", session); err != nil {
		core.Warn(fmt.Sprintf("could not record the accepted submission in the attempt sidecar %s: %s; the prompt went out", sidecar, dispatchCauseOrUnknown(err)), frictionLogPath, "dispatch")
	}
	status, resent, enterSent := "submitted", false, false
	if checkOn {
		window := time.Duration(checkSecs) * time.Second
		arrived := func() bool {
			if nonEmpty(report) {
				return true
			}
			st := herdr.AgentState(agent, env, herdr.Timeout, nil)
			return (st.State == "working" || st.State == "blocked") && preSeq != "" && seqString(st.Seq) != "" && seqString(st.Seq) != preSeq
		}
		promptEvidence := func() bool {
			screen := herdr.AgentRead(env, agent, "recent-unwrapped", intPtr(40))
			return provider.PromptEvidence(screen, composed)
		}
		if !wasWorking && !waitDispatchArrivalExtra(window, env, arrived, transcriptArrival) {
			screen := herdr.AgentRead(env, agent, "visible", nil)
			if firstEnterReady(kind, screen, composed) {
				// A recognized composer still holds this exact composed prompt
				// path: the prompt is typed and not sent. A report, a read, the
				// history or a dialog never authorizes the Enter on an empty or
				// unrelated draft; an unrecognized region is not positive proof.
				if blocked, ev, st := cursorStartupRefusal(kind, agent, env); blocked {
					// The dialog appeared after the send: the Enter would be
					// its input — refuse with the blocked result, no key sent.
					return dispatchCursorStartupRefusal(agent, role, kind, composed, report, taskReport, st.State, ev, sd)
				}
				if ui := provider.DialogUI(kind, screen); ui != "" {
					// A known trust/approval/question UI is up: the Enter would
					// be its input — no key, delivery not-received.
					return dispatchNotReceived(agent, role, kind, composed, report, taskReport, sd, sidecar, lane, model, effort, session, briefSHA, forEntries, env, wasWorking, "its "+ui+" dialog")
				}
				if herdr.AgentSendKeys(agent, "enter", env) {
					enterSent = true
					core.Warn(fmt.Sprintf("prompt to '%s' sat in the input box; sent Enter", agent), frictionLogPath, "dispatch")
					if !waitDispatchArrival(window, env, arrived) {
						return dispatchNotReceived(agent, role, kind, composed, report, taskReport, sd, sidecar, lane, model, effort, session, briefSHA, forEntries, env, wasWorking, "an Enter on the text left in its input box")
					}
				} else {
					// The send-key call failed: the input is failed and
					// unverified — do not claim the Enter sent.
					core.Warn(fmt.Sprintf("prompt to '%s' sat in the input box; its Enter failed", agent), frictionLogPath, "dispatch")
					return dispatchNotReceived(agent, role, kind, composed, report, taskReport, sd, sidecar, lane, model, effort, session, briefSHA, forEntries, env, wasWorking, "a failed Enter on the text left in its input box")
				}
			} else if staleAuthBlock(agent, env, H0, preSeq, enterSent, resent) {
				return dispatchNotReceived(agent, role, kind, composed, report, taskReport, sd, sidecar, lane, model, effort, session, briefSHA, forEntries, env, wasWorking, "its block on a provider auth error")
			} else {
				// The text can land in the composer between the first arrival
				// check and this retry: re-read the actual input region right
				// before the retry and act only on a recognized composer.
				resendScreen, resendScreenOK := herdr.AgentReadOK(env, agent, "visible", nil)
				H0 = strconv.FormatUint(uint64(waitpkg.CksumField(resendScreen)), 10)
				resendState := herdr.AgentState(agent, env, herdr.Timeout, nil)
				if blocked, ev, st := cursorStartupRefusal(kind, agent, env); blocked {
					// The dialog appeared before the resend: the resend would
					// type into it — refuse with the blocked result.
					return dispatchCursorStartupRefusal(agent, role, kind, composed, report, taskReport, st.State, ev, sd)
				}
				if ui := provider.DialogUI(kind, resendScreen); ui != "" {
					// A known trust/approval/question UI on the screen: it
					// takes no Enter from here (the Enter would answer it)
					// and no repeated prompt — keep the delivery uncertain and
					// leave the pane to be read; the wait phase owns the dialog
					// (question result or the opt-in auto-approve).
					return dispatchNotReceived(agent, role, kind, composed, report, taskReport, sd, sidecar, lane, model, effort, session, briefSHA, forEntries, env, wasWorking, "its "+ui+" dialog")
				}
				if resendState.State == "blocked" && !(preSeq != "" && seqString(resendState.Seq) != "" && preSeq != seqString(resendState.Seq)) {
					// A blocked target is on a dialog the screen rules could
					// not name: it takes no Enter from here and no repeated
					// prompt — end not-received and leave the pane to be read.
					return dispatchNotReceived(agent, role, kind, composed, report, taskReport, sd, sidecar, lane, model, effort, session, briefSHA, forEntries, env, wasWorking, "its block on a dialog")
				}
				region, recognized := provider.ComposerRegion(kind, resendScreen)
				if !resendScreenOK {
					return dispatchNotReceived(agent, role, kind, composed, report, taskReport, sd, sidecar, lane, model, effort, session, briefSHA, forEntries, env, wasWorking, "an unreadable input region")
				}
				if nonEmpty(report) || transcriptArrival() || ((resendState.State == "working" || resendState.State == "blocked") && preSeq != "" && seqString(resendState.Seq) != "" && preSeq != seqString(resendState.Seq)) {
					// Receipt can arrive between the deadline and this final read.
					// Preserve the original sequence baseline; never resend it.
				} else if recognized && provider.ComposerHoldsPath(region, composed) {
					// This exact composed prompt path is in the recognized
					// composer: the text landed after the arrival check. Send
					// only the bounded Enter and never duplicate the text.
					if herdr.AgentSendKeys(agent, "enter", env) {
						enterSent = true
						core.Warn(fmt.Sprintf("prompt to '%s' reached its input after the arrival check; sent Enter without resending", agent), frictionLogPath, "dispatch")
						if !waitDispatchArrival(window, env, arrived) {
							return dispatchNotReceived(agent, role, kind, composed, report, taskReport, sd, sidecar, lane, model, effort, session, briefSHA, forEntries, env, wasWorking, "an Enter on the text left in its input box")
						}
					} else {
						// The send-key call failed: the input is failed and
						// unverified — do not claim the Enter sent.
						core.Warn(fmt.Sprintf("prompt to '%s' reached its input after the arrival check; its Enter failed", agent), frictionLogPath, "dispatch")
						return dispatchNotReceived(agent, role, kind, composed, report, taskReport, sd, sidecar, lane, model, effort, session, briefSHA, forEntries, env, wasWorking, "a failed Enter on the text left in its input box")
					}
				} else if provider.ScreenShowsPath(resendScreen, composed) || !recognized || !dispatchComposerEmpty(kind, region) || (resendState.State != "idle" && resendState.State != "done") {
					// This path is visible on the screen but outside the
					// recognized composer — in the history above an empty or
					// unrelated composer, or on a screen whose input region is
					// not recognized: the delivery is uncertain. No Enter and
					// no repeated text; read the pane instead.
					return dispatchNotReceived(agent, role, kind, composed, report, taskReport, sd, sidecar, lane, model, effort, session, briefSHA, forEntries, env, wasWorking, "an unconfirmed input region")
				} else {
					// Repeating text requires a readable, recognized empty
					// composer and no trace in the readable recent screen.
					// Failed or clipped reads never prove nondelivery.
					recent, recentOK := herdr.AgentReadOK(env, agent, "recent-unwrapped", intPtr(40))
					if !recentOK || provider.ScreenShowsPath(recent, composed) {
						return dispatchNotReceived(agent, role, kind, composed, report, taskReport, sd, sidecar, lane, model, effort, session, briefSHA, forEntries, env, wasWorking, "an unconfirmed recent screen")
					}
					core.Warn(fmt.Sprintf("prompt to '%s' did not arrive; sending it once more", agent), frictionLogPath, "dispatch")
					if herdr.AgentPrompt(agent, text, env).Ok {
						resent = true
						if !waitDispatchArrivalExtra(window, env, arrived, transcriptArrival) {
							return dispatchNotReceived(agent, role, kind, composed, report, taskReport, sd, sidecar, lane, model, effort, session, briefSHA, forEntries, env, wasWorking, "one resend")
						}
					} else {
						return dispatchNotReceived(agent, role, kind, composed, report, taskReport, sd, sidecar, lane, model, effort, session, briefSHA, forEntries, env, wasWorking, "one resend")
					}
				}
			}
		}
		if wasWorking {
			ok := waitDispatchArrivalExtra(window, env, arrived, func() bool {
				return promptEvidence() || transcriptArrival()
			})
			if !ok && settleSecs > 0 {
				// Some CLIs (Cursor) hold a prompt sent during a turn in a queue they do
				// not show: wait up to prompt_settle_seconds for that turn to end, then
				// look for the arrival once more. After the turn only a new working turn
				// or the report counts: a path in the scrollback of an idle agent is not
				// a queued prompt (the next wait would read it as stuck in the input).
				ok = waitWorkingTurnEnd(agent, preSeq, time.Duration(settleSecs)*time.Second, env) &&
					waitDispatchArrival(window, env, arrived)
			}
			if !ok {
				return dispatchNotReceived(agent, role, kind, composed, report, taskReport, sd, sidecar, lane, model, effort, session, briefSHA, forEntries, env, true, "the working target showed no prompt evidence")
			}
			st := herdr.AgentState(agent, env, herdr.Timeout, nil)
			if !(nonEmpty(report) || ((st.State == "working" || st.State == "blocked") && preSeq != "" && seqString(st.Seq) != "" && preSeq != seqString(st.Seq))) {
				status = "queued"
				if err := writeDispatchSidecarFor(forEntries, sidecar, kind, model, effort, taskReport, briefSHA, "accepted", "queued", session); err != nil {
					core.Warn(fmt.Sprintf("could not record the queued arrival in the attempt sidecar %s: %s; the dispatch result stands", sidecar, dispatchCauseOrUnknown(err)), frictionLogPath, "dispatch")
				}
				seq := seqString(st.Seq)
				if seq == "" {
					seq = "-"
				}
				_ = os.WriteFile(filepath.Join(sd, "wait", agent+".queued"), []byte(fmt.Sprintf("%d %s %s\n", platform.Now().Unix(), seq, composed)), 0o666)
				fmt.Fprintf(platform.Stderr, "soho: prompt queued: '%s' is working; it takes the prompt when its turn ends\n", agent)
			}
		}
	}
	title := fmt.Sprintf("%s: %s", role, core.BriefTask(brief))
	if amend {
		if b, e := platform.ReadTextFile(filepath.Join(sd, "task-"+agent)); e == nil {
			b = strings.TrimRight(b, "\n")
			b = strings.TrimSuffix(b, " ✓")
			if b != "" {
				title = b
			} else {
				title = fmt.Sprintf("%s: %s", role, core.BriefTask(brief))
			}
		}
	}
	if err := os.WriteFile(filepath.Join(sd, "task-"+agent), []byte(title+"\n"), 0o666); err != nil {
		panic(err)
	}
	core.PaneTaskTitle(sd, agent, &title, env)
	var last *jsonjs.Object
	if noWait && checkOn && status != "queued" {
		status, last = noWaitObservation(agent, role, kind, lane, model, report, env, sd, ctx, priorAuth)
	}
	waitCode := 0
	if !noWait {
		old := platform.Stdout
		var buf bytes.Buffer
		platform.Stdout = &buf
		waitpkg.SetFrictionLogFile(frictionLogPath)
		func() {
			defer func() {
				value := recover()
				if value == nil {
					return
				}
				if exitErr, ok := value.(*platform.ExitError); ok && !exitErr.Friction {
					if exitErr.Msg == "" {
						core.RecordFrictionError("", exitErr.Code, frictionLogPath, "dispatch")
						_, _ = fmt.Fprint(platform.Stderr, "soho: \n")
					} else {
						exitErr.Friction = true
					}
				}
				panic(value)
			}()
			waitCode = dispatchWaitFor([]string{agent}, sd, ctx, env, parseTimeout(timeoutRaw), false, cwd)
		}()
		platform.Stdout = old
		lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
		if len(lines) > 0 {
			if value, e := jsonjs.Parse([]byte(lines[len(lines)-1])); e == nil {
				last, _ = value.(*jsonjs.Object)
			}
		}
	}
	resultCode := emitDispatchResult(agent, role, kind, composed, report, taskReport, status, amend, last, waitCode, sd, enterSent, resent)
	return resultCode
}

func containsWord(list, word string) bool {
	for _, item := range strings.Fields(list) {
		if item == word {
			return true
		}
	}
	return false
}

// compactDispatchStep runs the compact step behind dispatch --compact.
// compactRun reports some failures (a busy or dead worker, a failed send,
// a /compact that stayed in the codex composer after two Enters) with a
// DieFriction panic, which would skip the dispatch's own "did not finish"
// note on its way to the CLI boundary; the panic is re-raised only after
// the note is printed, so the boundary still prints the compact message and
// records the friction line.
func compactDispatchStep(agent, pane, kind, role, lane, model string, timeoutMS int64, ctx *core.Config, env platform.Env, cwd string) int {
	defer func() {
		value := recover()
		if value == nil {
			return
		}
		exitErr, ok := value.(*platform.ExitError)
		if !ok {
			panic(value)
		}
		fmt.Fprintf(platform.Stderr, "soho: dispatch: the compact step of '%s' did not finish (exit %d); the brief was not sent\n", agent, exitErr.Code)
		panic(value)
	}()
	return compactRun(agent, pane, kind, role, lane, model, timeoutMS, ctx, env, cwd, true)
}

// compactPhaseDispatchOptions names the F11 wrapper's parameters: the
// dispatch's own fields plus the state dir the marker lives in.
type compactPhaseDispatchOptions struct {
	Agent     string
	Pane      string
	Kind      string
	Role      string
	Lane      string
	Model     string
	Brief     string
	SD        string
	TimeoutMS int64
	Ctx       *core.Config
	Env       platform.Env
	Cwd       string
}

// compactPhaseDispatch wraps compactDispatchStep with the F11 marker: the
// claim stands before the phase sends anything, and the claimed token is
// removed when the phase exits (a success, a failure and the re-raised panic
// included), before any task state is written or the brief is sent. Only a
// kill that skips the cleanup (SIGKILL) leaves the record, where status
// reads it as compact-interrupted. A phase that succeeded but whose marker
// could not be removed (a later token, an unreadable file, a lock panic)
// aborts with 4 before the brief: the standing marker still denies the send,
// and a different token never authorizes this dispatch's send.
func compactPhaseDispatch(opts compactPhaseDispatchOptions) int {
	absBrief := opts.Brief
	if p, err := filepath.Abs(opts.Brief); err == nil {
		absBrief = p
	}
	token := core.CompactPhaseClaim(core.CompactPhaseClaimOptions{SD: opts.SD, Agent: opts.Agent, Pane: opts.Pane, Brief: absBrief, Env: opts.Env})
	fmt.Fprintf(platform.Stderr, "soho: dispatch: compacting '%s' before sending the brief; status shows this phase\n", opts.Agent)
	// removeMarker captures the cleanup's outcome, a lock panic included
	// (recorded, never allowed to replace the phase's own error on the panic
	// path).
	removeMarker := func() error {
		return func() (err error) {
			defer func() {
				if v := recover(); v != nil {
					err = fmt.Errorf("cleanup panic: %v", v)
				}
			}()
			return core.CompactPhaseRemove(opts.SD, opts.Agent, token)
		}()
	}
	code := func() int {
		defer func() {
			if r := recover(); r != nil {
				// A failed phase keeps its own error; the cleanup failure is
				// only recorded.
				if err := removeMarker(); err != nil {
					core.Warn(fmt.Sprintf("dispatch: the compact-phase marker of '%s' was not removed: %v", opts.Agent, err), frictionLogPath, "dispatch")
				}
				panic(r)
			}
		}()
		return compactDispatchStep(opts.Agent, opts.Pane, opts.Kind, opts.Role, opts.Lane, opts.Model, opts.TimeoutMS, opts.Ctx, opts.Env, opts.Cwd)
	}()
	if err := removeMarker(); err != nil {
		core.Warn(fmt.Sprintf("dispatch: the compact-phase marker of '%s' was not removed: %v", opts.Agent, err), frictionLogPath, "dispatch")
		if code == 0 {
			core.DieFriction(fmt.Sprintf("dispatch: the compact phase of '%s' finished, but its marker could not be removed (%v); the brief was not sent", opts.Agent, err), 4, frictionLogPath, "dispatch")
		}
	}
	return code
}

// writeDispatchSidecar writes the attempt sidecar and, when the dispatch
// resolved --for authors, adds their anonymized "for" array so the metrics
// line can carry it (issue #39, part 2). It also sets the durable task_report
// and brief_sha256 fields (s72). The sidecar is rewritten in place on every
// submission update, so those go on at every write.
func writeDispatchSidecarFor(entries []any, path, kind, model, effort, taskReport, briefSHA, submission, arrival, session string) error {
	if err := writeDispatchSidecar(path, kind, model, effort, submission, arrival, session); err != nil {
		return err
	}
	return addForToSidecar(path, entries, taskReport, briefSHA)
}

// addForToSidecar re-writes the sidecar dispatch.WriteSidecar just wrote with
// the "for" array set: the wait's metrics line copies it to the review line
// and the author names stay out of both. It also sets the durable
// task_report and brief_sha256 fields when present, so every sidecar rewrite
// carries them; an old sidecar without them stays valid.
func addForToSidecar(path string, entries []any, taskReport, briefSHA string) error {
	if len(entries) == 0 && taskReport == "" && briefSHA == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	value, err := jsonjs.Parse(raw)
	if err != nil {
		return err
	}
	obj, ok := value.(*jsonjs.Object)
	if !ok {
		return fmt.Errorf("the sidecar %s is not a JSON object", path)
	}
	if taskReport != "" {
		obj.Set("task_report", taskReport)
	}
	if briefSHA != "" {
		obj.Set("brief_sha256", briefSHA)
	}
	if len(entries) > 0 {
		obj.Set("for", entries)
	}
	return platform.AtomicWrite(path, jsonjs.Stringify(obj)+"\n")
}

// dispatchForAuthor builds the anonymized --for entry for the sidecar
// (issue #39, part 2): a roster agent carries its kind/model/effort/family
// columns, only the non-empty ones, in that order; a kind with a fixed
// family carries kind and family; a family or a released agent carries the
// family the dispatch resolved for the spec. The author's name never
// appears.
func dispatchForAuthor(spec, sd, family string) *jsonjs.Object {
	if line := core.RosterLine(sd, spec); line != "" {
		cols := strings.Split(line, "\t")
		at := func(i int) string {
			if i < len(cols) {
				return cols[i]
			}
			return ""
		}
		entry := jsonjs.O()
		if v := at(2); v != "" {
			entry.Set("kind", v)
		}
		if v := at(8); v != "" {
			entry.Set("model", v)
		}
		if v := at(14); v != "" {
			entry.Set("effort", v)
		}
		if v := at(4); v != "" {
			entry.Set("family", v)
		}
		return entry
	}
	if fam := kinds.KindFamily(spec); fam != "unknown" {
		return jsonjs.O("kind", spec, "family", fam)
	}
	if family != "" && family != "unknown" {
		return jsonjs.O("family", family)
	}
	return jsonjs.O()
}
func nonEmpty(path string) bool { st, e := os.Stat(path); return e == nil && st.Size() > 0 }
func seqString(value any) string {
	if value == nil {
		return ""
	}
	switch v := value.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	}
	return ""
}

// parseTimeout reads the --timeout flag in milliseconds: a plain integer or
// a duration with ms, s, m or h (core.ParseTimeoutMS); it still returns 0 for
// a value it cannot read, as before the unit syntax.
func parseTimeout(raw string) float64 {
	if ms, ok := core.ParseTimeoutMS(raw); ok {
		return float64(ms)
	}
	v, e := strconv.ParseFloat(raw, 64)
	if e != nil {
		return 0
	}
	return v
}
func waitDispatchArrival(window time.Duration, env platform.Env, arrived func() bool) bool {
	return waitDispatchArrivalExtra(window, env, arrived, nil)
}
func waitDispatchArrivalExtra(window time.Duration, env platform.Env, arrived, extra func() bool) bool {
	deadline := platform.Now().Add(window)
	for {
		if arrived() {
			return true
		}
		if extra != nil && extra() {
			return true
		}
		if !platform.Now().Before(deadline) {
			return arrived() || (extra != nil && extra())
		}
		poll := time.Duration(spawn.PollIntervalMs(env)) * time.Millisecond
		if poll <= 0 {
			poll = 100 * time.Millisecond
		}
		time.Sleep(poll)
	}
}

// waitWorkingTurnEnd polls a target that was working when the prompt went out
// until its turn ends (it leaves working and blocked) or a new turn starts (its
// state_change_seq moves while working), up to window. It reports whether that
// happened in time.
func waitWorkingTurnEnd(agent, preSeq string, window time.Duration, env platform.Env) bool {
	deadline := platform.Now().Add(window)
	for {
		st := herdr.AgentState(agent, env, herdr.Timeout, nil)
		if st.State != "working" && st.State != "blocked" {
			return st.State == "idle" || st.State == "done"
		}
		if preSeq != "" && seqString(st.Seq) != "" && seqString(st.Seq) != preSeq {
			return true
		}
		if !platform.Now().Before(deadline) {
			return false
		}
		poll := time.Duration(spawn.PollIntervalMs(env)) * time.Millisecond
		if poll <= 0 {
			poll = 100 * time.Millisecond
		}
		time.Sleep(poll)
	}
}
func intPtr(v int) *int { return &v }

// transcriptPathMarker returns a path the way a session transcript (a
// Claude Code session transcript, a pi session file) writes it inside a JSON
// string: a backslash (the Windows path separator) is written as two and a
// quote escaped, as JSON writes them. A path without those characters comes
// back unchanged, so the Unix composed path matches the transcript line
// verbatim.
func transcriptPathMarker(path string) string {
	var out strings.Builder
	for _, r := range path {
		switch r {
		case '\\':
			out.WriteString(`\\`)
		case '"':
			out.WriteString(`\"`)
		default:
			out.WriteRune(r)
		}
	}
	return out.String()
}

func sanitizeDispatchCause(raw string) string { return core.FrictionSafe(strings.TrimSpace(raw)) }
func dispatchCauseOrUnknown(err error) string {
	if cause := sanitizeDispatchCause(err.Error()); cause != "" {
		return cause
	}
	return "unknown error"
}
func restoreTaskDispatch(sd, agent, lastPath string, priorLast []byte, prior *jsonjs.Object) error {
	if prior == nil {
		_ = os.Remove(taskreport.TaskReportPointerPath(sd, agent))
	} else {
		_ = taskreport.WriteTaskReportPointer(sd, agent, prior)
		_, _ = taskreport.SyncTaskReport(sd, agent)
	}
	if len(priorLast) == 0 {
		_ = os.Remove(lastPath)
		return nil
	}
	return os.WriteFile(lastPath, priorLast, 0o666)
}
func writeDispatchError(agent, role, kind, composed, report, taskReport, raw string, code int, warning string) int {
	obj := jsonjs.O("wait_status", "error", "agent", agent, "role", role, "kind", kind, "composed_prompt", composed, "report", report, "task_report", taskReport, "report_exists", false, "raw", raw)
	fmt.Fprintln(platform.Stdout, jsonjs.Stringify(obj))
	core.Warn(warning, frictionLogPath, "dispatch")
	return code
}

// cursorStartupScreen reads the agent's state and the visible screen once
// for the cursor/Windows startup checks (issue #59): the shared runner's
// calls, the same reads the consumers already spend on safe screens.
func cursorStartupScreen(agent string, env platform.Env) (herdr.AgentStateResult, string, bool) {
	st := herdr.AgentState(agent, env, herdr.Timeout, nil)
	screen, screenOK := herdr.AgentReadOK(env, agent, "visible", nil)
	return st, screen, screenOK
}

// firstEnterReady reports whether the screen authorizes the first Enter: a
// recognized composer (the roster kind) holding the exact composed prompt
// path. A report, a read, the history or a dialog never authorizes the
// Enter on an empty or unrelated draft, and an unrecognized region is not
// positive composer proof.
func firstEnterReady(kind, screen, composed string) bool {
	region, recognized := provider.ComposerRegion(kind, screen)
	return recognized && provider.ComposerHoldsPath(region, composed)
}

// dispatchComposerEmpty accepts only whitespace after the provider's input
// prompt, never another draft. It is a retry-text prerequisite, not receipt.
func dispatchComposerEmpty(kind string, region []string) bool {
	for i, line := range region {
		line = strings.TrimSpace(line)
		if i == 0 {
			switch kind {
			case "codex":
				line = strings.TrimSpace(strings.TrimPrefix(line, "›"))
			case "claude":
				line = strings.TrimSpace(strings.TrimPrefix(line, "❯"))
			}
		}
		if line != "" {
			return false
		}
	}
	return true
}

// cursorStartupRefusal reports whether the current visible screen is the
// cursor workspace trust dialog or unverified startup, under the shared gate (kind and platform
// checked inside): the recheck right before an actual input (the
// AgentPrompt or an arrival/retry Enter). A dialog that appears during the
// settle must receive no input. Non-target kinds or platforms return
// false without any herdr read.
func cursorStartupRefusal(kind, agent string, env platform.Env) (bool, startup.Evaluation, herdr.AgentStateResult) {
	if !startup.Enabled(kind) {
		return false, startup.Evaluation{}, herdr.AgentStateResult{}
	}
	st, screen, screenOK := cursorStartupScreen(agent, env)
	ev := startup.Evaluate(kind, startup.CurrentPlatform(), st.State, screen, screenOK)
	return ev.Refuses(), ev, st
}

// warnCursorStartupRefusal prints the precise refusal warning: the reason
// the dispatch was refused before any input (the verified workspace trust
// dialog, or the unverified screen the assessment ended on) and that
// nothing was typed or sent — the pane stays open for the user to read
// and answer.
func warnCursorStartupRefusal(agent, kind, state string, ev startup.Evaluation) {
	what := "the startup is unverified"
	switch {
	case ev.Verdict == startup.VerdictBlocked:
		what = "it is on the cursor workspace trust dialog"
	case ev.Reason == startup.ReasonEmptyScreen:
		what = "the visible screen is empty (the TUI did not draw)"
	case ev.Reason == startup.ReasonLaunchOnly:
		what = "the visible screen shows only the shell prompt/launch line"
	case ev.Reason == startup.ReasonScreenUnavailable:
		what = "the visible screen could not be read"
	case ev.Reason == startup.ReasonStateUnavailable:
		what = "the agent state could not be read"
	}
	core.Warn(fmt.Sprintf("dispatch: '%s' (%s) startup is not ready: %s (state %s); nothing was typed or sent — read the pane (herdr agent read %s --source visible) and answer it yourself, then dispatch again", agent, kind, what, state, agent), frictionLogPath, "dispatch")
}

// dispatchCursorStartupRefusal ends the dispatch without typing anything:
// the existing blocked result (wait_status "blocked", exit 7) with the
// cause naming the startup reason and the startup_evidence carrying the
// state, the reason and the visible source. On the initial preflight
// refusal the composed prompt, report and task report do not exist yet
// (empty); the task pointer and last-report are restored by the caller
// when the send was already claimed.
func dispatchCursorStartupRefusal(agent, role, kind, composed, report, taskReport, state string, ev startup.Evaluation, sd string) int {
	warnCursorStartupRefusal(agent, kind, state, ev)
	last := jsonjs.O("cause", startup.EvidencePrefix+ev.Reason, "startup_evidence", jsonjs.O(startup.FieldState, state, startup.FieldReason, ev.Reason, startup.FieldSource, ev.Source))
	return emitDispatchResult(agent, role, kind, composed, report, taskReport, "blocked", false, last, 0, sd, false, false)
}
func dispatchNotReceived(agent, role, kind, composed, report, taskReport, sd, sidecar, lane, model, effort, session, briefSHA string, forEntries []any, env platform.Env, wasWorking bool, why string) int {
	_ = lane
	if err := writeDispatchSidecarFor(forEntries, sidecar, kind, model, effort, taskReport, briefSHA, "accepted", "not-received", session); err != nil {
		core.Warn(fmt.Sprintf("could not record the not-received arrival in the attempt sidecar %s: %s; the dispatch result stands", sidecar, dispatchCauseOrUnknown(err)), frictionLogPath, "dispatch")
	}
	seq := seqString(herdr.AgentState(agent, env, herdr.Timeout, nil).Seq)
	marker := strconv.FormatInt(platform.Now().Unix(), 10)
	if wasWorking {
		// The prompt path goes in the marker (as in .queued): the next wait then
		// sends Enter only when this prompt sits in the last lines (the input),
		// never for a path it sees in the scrollback of the earlier turn.
		if seq == "" {
			seq = "-"
		}
		marker += " " + seq + " " + composed
	} else if seq != "" {
		marker += " " + seq
	}
	marker += "\n"
	_ = os.WriteFile(filepath.Join(sd, "wait", agent+".not-received"), []byte(marker), 0o666)
	obj := jsonjs.O("wait_status", "not-received", "agent", agent, "role", role, "kind", kind, "composed_prompt", composed, "report", report, "task_report", taskReport, "report_exists", nonEmpty(report))
	fmt.Fprintln(platform.Stdout, jsonjs.Stringify(obj))
	warning := fmt.Sprintf("prompt to '%s' was not received after %s; read the pane (herdr agent read %s --source visible) before sending anything else", agent, why, agent)
	if wasWorking {
		warning = fmt.Sprintf("prompt to '%s' not confirmed: it was working and shows no sign of the prompt; no key was sent. Read the pane before sending anything else.", agent)
	}
	core.Warn(warning, frictionLogPath, "dispatch")
	return 15
}

func emitDispatchResult(agent, role, kind, composed, report, taskReport, status string, amend bool, last *jsonjs.Object, waitCode int, sd string, enterSent, resent bool) int {
	if last != nil {
		if v, ok := last.Get("status"); ok {
			if s, yes := v.(string); yes {
				status = s
			}
		}
	}
	out := jsonjs.O("wait_status", status, "agent", agent, "role", role, "kind", kind, "composed_prompt", composed, "report", report, "task_report", taskReport)
	settled := ""
	reportPath := report
	if last != nil {
		if v, ok := last.Get("report"); ok {
			if s, yes := v.(string); yes && s != "" && s != report {
				settled = s
				reportPath = s
			}
		}
	}
	if settled != "" {
		out.Set("settled_report", settled)
	}
	out.Set("report_exists", nonEmpty(reportPath))
	if amend {
		out.Set("amend", true)
	}
	if last != nil {
		for _, key := range []string{"dialog", "verdict", "findings", "severity", "partial", "verdict_effective"} {
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
		for _, key := range []string{"question", "lane", "model", "match", "renewal", "cause", "retries", "startup_evidence"} {
			if v, ok := last.Get(key); ok {
				out.Set(key, v)
			}
		}
	}
	if enterSent {
		out.Set("enter_sent", true)
	}
	if resent {
		out.Set("resent", true)
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
			core.Warn(fmt.Sprintf("agent '%s' settled without writing %s; collect will fall back to terminal output", agent, report), frictionLogPath, "dispatch")
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

func warnOwnedOverlap(brief, role, agent, sd string, ctx *core.Config, env platform.Env, cwd, workerCwd string) {
	aliases, _ := dispatch.ParseBriefLintAliases(core.Cfg(ctx, "brief_lint_aliases", "", env))
	body, _ := platform.ReadTextFile(brief)
	mine := ownedPaths(body, aliases)
	if len(mine) == 0 {
		return
	}
	for _, row := range core.RosterRows(sd) {
		cols := strings.Split(row, "\t")
		if len(cols) == 0 || cols[0] == "" || cols[0] == agent {
			continue
		}
		at := func(i int) string {
			if i < len(cols) {
				return cols[i]
			}
			return ""
		}
		if !dispatch.SamePath(at(6), workerCwd, platform.Current()) {
			continue
		}
		report := core.LastReport(sd, cols[0])
		if report == "" || nonEmpty(report) {
			continue
		}
		pending := pendingBriefPath(report)
		if pending == "" {
			continue
		}
		theirs := ownedPaths(pendingBriefSection(pending), aliases)
		hits := []string{}
		for _, p := range mine {
			for _, q := range theirs {
				if pathsCross(p, q) {
					hits = append(hits, p)
					break
				}
			}
		}
		if len(hits) == 0 {
			continue
		}
		if len(hits) > 5 {
			hits = hits[:5]
		}
		message := fmt.Sprintf("brief %s owns files that '%s' is still editing: %s", brief, cols[0], strings.Join(hits, ", "))
		if !core.RoleIsEdit(role, env, cwd) {
			message = fmt.Sprintf("reviewing files that '%s' is still editing: %s", cols[0], strings.Join(hits, ", "))
		}
		core.Warn(message, frictionLogPath, "dispatch")
	}
}

var dispatchContextFooterRE = regexp.MustCompile(`\d+(?:\.\d+)?K \((\d+)%\)\s+ctrl\+p commands$`)

// dispatchContextPercent scans the visible screen bottom-up over the last
// non-empty lines and returns the context percent the opencode footer shows
// (<number>K (NN%) on the line ending in ctrl+p commands); -1 when there is
// no such line or the pattern does not match.
func dispatchContextPercent(screen string) int {
	lines := strings.Split(strings.ReplaceAll(screen, "\r\n", "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if !strings.HasSuffix(line, "ctrl+p commands") {
			continue
		}
		if m := dispatchContextFooterRE.FindStringSubmatch(line); m != nil {
			if pct, e := strconv.Atoi(m[1]); e == nil {
				return pct
			}
		}
		return -1
	}
	return -1
}

var ownershipStopwords = map[string]bool{"nenhum": true, "none": true}
var ownershipExclusions = map[string]bool{"nenhum": true, "nenhuma": true, "nao": true, "nunca": true, "exceto": true, "not": true, "never": true, "none": true, "except": true, "excluding": true, "outside": true}

// ownedSpanRE matches a backtick-quoted span with its contents; ownedAssignRE
// matches a bare ASCII identifier right before "=" with optional whitespace
// in between. The character before the identifier must not be a letter,
// digit or underscore in any script, so the ASCII tail of a longer token
// ("ÁB", "BÁC") is never taken for an identifier. Callers blank the
// backtick spans first, so an "=" inside a backtick-quoted filename (for
// example `a=b.go`) is never seen as assignment syntax.
var ownedSpanRE = regexp.MustCompile("`([^`]+)`")
var ownedAssignRE = regexp.MustCompile(`(?:^|[^\p{L}\p{N}_])([A-Za-z_][A-Za-z0-9_]*)[ \t\r]*=`)

// ownedAssignmentValues reports, for each backtick span on the line, whether
// it is the value of an explicit bare ASCII identifier assignment, and
// whether the line ends on such an assignment still waiting for its
// backtick-quoted value on the next line. Only whitespace may sit between
// "=" and the value: a backtick span on the same line ("B = `/base`") or
// nothing at all ("B =" at end of line, value on the next line). "B == x",
// "B = /base" (unquoted text) and any "=" inside backticks are not
// assignments.
func ownedAssignmentValues(line string) (values []bool, pending bool) {
	spans := ownedSpanRE.FindAllStringSubmatchIndex(line, -1)
	values = make([]bool, len(spans))
	blanked := make([]byte, len(line))
	copy(blanked, line)
	for _, sp := range spans {
		for i := sp[0]; i < sp[1]; i++ {
			blanked[i] = 0
		}
	}
	spanAt := func(i int) (int, bool) {
		for k, sp := range spans {
			if sp[0] == i {
				return k, true
			}
		}
		return -1, false
	}
	for _, m := range ownedAssignRE.FindAllIndex(blanked, -1) {
		eq := m[1] - 1
		if eq+1 < len(line) && line[eq+1] == '=' {
			continue // "==" compares, it does not assign
		}
		gap := line[eq+1:]
		j := 0
		for j < len(gap) && (gap[j] == ' ' || gap[j] == '\t' || gap[j] == '\r') {
			j++
		}
		if j == len(gap) {
			pending = true
			continue
		}
		if gap[j] == '`' {
			if k, ok := spanAt(eq + 1 + j); ok {
				values[k] = true
			}
		}
	}
	return values, pending
}

func ownedPaths(body string, aliases map[string][]string) []string {
	lines := strings.Split(body, "\n")
	start, level := -1, 0
	for i, line := range lines {
		m := regexp.MustCompile(`^(#{1,3}) +(.+)$`).FindStringSubmatch(line)
		if len(m) == 0 {
			continue
		}
		title := m[2]
		if dispatch.BriefSectionTitleMatches(title, "Owned files", aliases) {
			start, level = i, len(m[1])
			break
		}
	}
	if start < 0 {
		return nil
	}
	out, seen := []string{}, map[string]bool{}
	push := func(raw string) {
		p := strings.TrimSpace(raw)
		for strings.HasPrefix(p, "./") {
			p = strings.TrimPrefix(p, "./")
		}
		// The trailing slash is kept: it marks a directory owned in the brief,
		// and pathsCross decides whether a glob can reach a file inside it.
		trimmed := strings.TrimRight(p, ".,;:")
		if trimmed == "" || strings.Contains(trimmed, " ") || ownershipStopwords[strings.ToLower(trimmed)] || !(strings.ContainsAny(trimmed, "/.")) || seen[strings.TrimRight(trimmed, "/")] {
			return
		}
		seen[strings.TrimRight(trimmed, "/")] = true
		out = append(out, trimmed)
	}
	pendingValue := false
	for _, line := range lines[start+1:] {
		if m := regexp.MustCompile(`^(#{1,6}) `).FindStringSubmatch(line); len(m) > 0 && len(m[1]) <= level {
			break
		}
		item := strings.TrimSpace(regexp.MustCompile(`^\s*(?:[-*+]|\d+[.)])\s+`).ReplaceAllString(line, ""))
		words := strings.Fields(item)
		if len(words) > 0 && ownershipExclusions[strings.ToLower(words[0])] {
			pendingValue = false
			continue
		}
		spans := ownedSpanRE.FindAllStringSubmatchIndex(line, -1)
		skip := make([]bool, len(spans))
		lineValues, nextPending := ownedAssignmentValues(line)
		copy(skip, lineValues)
		if pendingValue {
			// The pending value must open the very next line: a list item or a
			// heading line does not take it, so the pending assignment can
			// neither swallow a real list item nor cross a section heading.
			if t := strings.TrimLeft(line, " \t\r"); len(t) > 0 && t[0] == '`' && len(spans) > 0 && spans[0][0] == len(line)-len(t) {
				skip[0] = true
			}
		}
		pendingValue = nextPending
		for i, m := range spans {
			if skip[i] {
				continue
			}
			push(line[m[0]+1 : m[1]-1])
		}
		if regexp.MustCompile(`^\s*(?:[-*+]|\d+[.)])\s+[^` + "`" + `].*$`).MatchString(line) {
			push(item)
		}
	}
	return out
}

func pendingBriefPath(report string) string {
	name := filepath.Base(report)
	if !strings.HasSuffix(name, ".md") {
		return ""
	}
	base := strings.TrimSuffix(name, ".md")
	for _, candidate := range []string{filepath.Join(filepath.Dir(report), "..", "briefs", base+".md"), filepath.Join(filepath.Dir(report), base+".brief.md")} {
		if st, err := os.Stat(candidate); err == nil && st.Mode().IsRegular() {
			return candidate
		}
	}
	return ""
}

func composedBriefSection(raw string) string {
	lines := strings.Split(raw, "\n")
	start := -1
	for i, line := range lines {
		if line == "# Brief" {
			start = i
			break
		}
	}
	if start < 0 {
		return ""
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if lines[i] == "# Report contract" {
			end = i
			break
		}
	}
	return strings.Join(lines[start:end], "\n")
}

func pendingBriefSection(file string) string {
	raw, err := platform.ReadTextFile(file)
	if err != nil {
		return ""
	}
	if section := composedBriefSection(raw); section != "" {
		return section
	}
	name := filepath.Base(file)
	isTmp := strings.HasSuffix(name, ".brief.md")
	base := strings.TrimSuffix(strings.TrimSuffix(name, ".brief.md"), ".md")
	re := regexp.MustCompile(`^(.+)-(\d{8}T\d{6})(?:-(\d+))?$`)
	match := re.FindStringSubmatch(base)
	if len(match) == 0 {
		return ""
	}
	agent, stamp, suffix := match[1], match[2], 1
	if match[3] != "" {
		suffix, _ = strconv.Atoi(match[3])
	}
	entries, err := os.ReadDir(filepath.Dir(file))
	if err != nil {
		return ""
	}
	bestStamp, bestSuffix, best := "", 0, ""
	for _, entry := range entries {
		candidate := entry.Name()
		candidateTmp := strings.HasSuffix(candidate, ".brief.md")
		if candidateTmp != isTmp {
			continue
		}
		candidateBase := strings.TrimSuffix(strings.TrimSuffix(candidate, ".brief.md"), ".md")
		m := re.FindStringSubmatch(candidateBase)
		if len(m) == 0 || m[1] != agent || m[2] > stamp {
			continue
		}
		n := 1
		if m[3] != "" {
			n, _ = strconv.Atoi(m[3])
		}
		if m[2] == stamp && n >= suffix {
			continue
		}
		content, e := platform.ReadTextFile(filepath.Join(filepath.Dir(file), candidate))
		if e != nil {
			continue
		}
		section := composedBriefSection(content)
		if section == "" {
			continue
		}
		if m[2] > bestStamp || (m[2] == bestStamp && n > bestSuffix) {
			bestStamp, bestSuffix, best = m[2], n, section
		}
	}
	return best
}

func pathsCross(a, b string) bool {
	inside := func(p, q string) bool { return p == q || strings.HasPrefix(p, q+"/") || strings.HasPrefix(q, p+"/") }
	hasGlob := func(p string) bool { return strings.ContainsAny(p, "*?[") }
	glob := func(pattern, value string) bool {
		var out strings.Builder
		for i := 0; i < len(pattern); i++ {
			c := pattern[i]
			switch c {
			case '*':
				if i+1 < len(pattern) && pattern[i+1] == '*' {
					i++
					if i+1 < len(pattern) && pattern[i+1] == '/' {
						i++
						out.WriteString("(?:.*/)?")
					} else {
						out.WriteString(".*")
					}
				} else {
					out.WriteString("[^/]*")
				}
			case '?':
				out.WriteString("[^/]")
			case '[':
				j := strings.IndexByte(pattern[i+1:], ']')
				if j >= 0 {
					cl := pattern[i+1 : i+1+j]
					if strings.HasPrefix(cl, "!") {
						cl = "^" + cl[1:]
					}
					out.WriteString("[" + cl + "]")
					i += j + 1
				} else {
					out.WriteString(`\[`)
				}
			default:
				if strings.ContainsRune(`.+(){}^$|\`, rune(c)) {
					out.WriteByte('\\')
				}
				out.WriteByte(c)
			}
		}
		re, err := regexp.Compile("^" + out.String() + "$")
		return err == nil && re.MatchString(value)
	}
	// span returns the literal text before the first glob token and after the
	// last one, walking whole tokens: a [class] counts up to its `]` inclusive,
	// so class members are never treated as literal anchors.
	span := func(s string) (pre, suf string, has bool) {
		first, last := -1, 0
		for i := 0; i < len(s); {
			switch s[i] {
			case '*':
				j := i + 1
				if j < len(s) && s[j] == '*' {
					j++
				}
				if first < 0 {
					first = i
				}
				last = j
				i = j
			case '?':
				if first < 0 {
					first = i
				}
				last = i + 1
				i++
			case '[':
				if k := strings.IndexByte(s[i+1:], ']'); k >= 0 {
					if first < 0 {
						first = i
					}
					last = i + k + 2
					i += k + 2
				} else {
					i++ // an unclosed class is a literal `[` in the matcher
				}
			default:
				i++
			}
		}
		if first < 0 {
			return s, s, false
		}
		return s[:first], s[last:], true
	}
	// classMembers lists single characters a class token can stand for: the
	// written members of a plain class (ranges reduced to their endpoints, `-`
	// skipped) or one character outside a negated class.
	classMembers := func(cl string) []string {
		body := cl[1 : len(cl)-1]
		neg := false
		if strings.HasPrefix(body, "!") || strings.HasPrefix(body, "^") {
			neg = true
			body = body[1:]
		}
		if !neg {
			var out []string
			for _, r := range body {
				if r == '-' {
					continue
				}
				out = append(out, string(r))
				if len(out) >= 8 {
					return out
				}
			}
			if len(out) == 0 {
				return []string{"x"}
			}
			return out
		}
		for _, r := range []rune{'z', 'Z', '0', '9', 'a', 'b', 'c'} {
			if !strings.ContainsRune(body, r) {
				return []string{string(r)}
			}
		}
		return []string{"x"}
	}
	// midFills lists candidate texts for the middle of s (between its literal
	// prefix and suffix): the base with every non-class token at its first
	// representative, plus one variant per character-class member.
	midFills := func(s string) []string {
		pre, suf, has := span(s)
		if !has {
			return nil
		}
		mid := s[len(pre) : len(s)-len(suf)]
		type cls struct {
			pos  int
			wid  int
			reps []string
		}
		var classes []cls
		var base []byte
		for i := 0; i < len(mid); {
			switch mid[i] {
			case '*':
				j := i + 1
				if j < len(mid) && mid[j] == '*' {
					j++
				}
				i = j // the first representative of `*` / `**` is empty
			case '?':
				base = append(base, 'x')
				i++
			case '[':
				if k := strings.IndexByte(mid[i+1:], ']'); k >= 0 {
					reps := classMembers(mid[i : i+k+2])
					classes = append(classes, cls{len(base), len([]byte(reps[0])), reps})
					base = append(base, []byte(reps[0])...)
					i += k + 2
				} else {
					base = append(base, '[')
					i++
				}
			default:
				base = append(base, mid[i])
				i++
			}
		}
		fills := []string{string(base)}
		for _, c := range classes {
			for _, m := range c.reps[1:] {
				fills = append(fills, string(base[:c.pos])+m+string(base[c.pos+c.wid:]))
			}
		}
		return fills
	}
	// classChars collects the member characters of every class token of s, as
	// standalone candidates: a common file may pin them even when the pattern's
	// prefix or a leading `**` leaves its middle unanchored.
	classChars := func(s string) []string {
		var out []string
		for i := 0; i < len(s); {
			if s[i] == '[' {
				if k := strings.IndexByte(s[i+1:], ']'); k >= 0 {
					out = append(out, classMembers(s[i:i+k+2])...)
					i += k + 2
					continue
				}
			}
			i++
		}
		return out
	}
	da := strings.HasSuffix(a, "/") && a != "/"
	db := strings.HasSuffix(b, "/") && b != "/"
	a = strings.TrimRight(a, "/")
	b = strings.TrimRight(b, "/")
	ga, gb := hasGlob(a), hasGlob(b)
	// A plain path that is a directory (the trailing slash in the brief) crosses
	// a glob when a file inside it could match the glob.
	crossDir := func(g, d string) bool {
		p, s, _ := span(g)
		p = strings.TrimRight(p, "/")
		if p == "" {
			// An unanchored glob may match at any depth: it can reach into d.
			return true
		}
		if p == d || strings.HasPrefix(p, d+"/") {
			// Everything the glob matches lives inside d.
			return true
		}
		for _, f := range []string{"", "x", "x/y"} {
			if glob(g, d+"/"+f) {
				return true
			}
			if s != "" && glob(g, d+"/"+f+s) {
				return true
			}
		}
		return false
	}
	crossPlain := func(g, t string) bool {
		if glob(g, t) {
			return true
		}
		p, _, has := span(g)
		if !has {
			return inside(g, t)
		}
		// A glob that starts with a wildcard has no literal prefix to anchor
		// a directory relation: it crosses a path only when it matches it.
		p = strings.TrimRight(p, "/")
		return p != "" && inside(p, t)
	}
	// dirTreeCross: g is a directory glob (a tree) and d a literal directory.
	// They cross when d is, or sits inside, a directory g matches (an
	// ancestor of d matches g), or when d contains a directory g could match
	// (d's segments match g's leading segments, a ** segment matching the
	// rest). A sibling tree under the same prefix crosses neither way.
	dirTreeCross := func(g, d string) bool {
		for p := d; p != "" && p != "." && p != "/"; p = path.Dir(p) {
			if glob(g, p) {
				return true
			}
		}
		gs, ds := strings.Split(g, "/"), strings.Split(d, "/")
		if len(ds) > len(gs) {
			return false
		}
		for i, seg := range ds {
			if gs[i] == "**" {
				return true
			}
			// The same glob dialect as the rest of pathsCross ([!..] negates).
			if !glob(gs[i], seg) {
				return false
			}
		}
		return true
	}
	switch {
	case da && db && !ga && !gb:
		return inside(a, b)
	case da && db && ga && !gb:
		return dirTreeCross(a, b)
	case da && db && gb && !ga:
		return dirTreeCross(b, a)
	case da && db:
		// Two directory globs: the glob x glob comparison below.
	case da && gb:
		return crossDir(b, a)
	case db && ga:
		return crossDir(a, b)
	case ga && !gb:
		return crossPlain(a, b)
	case gb && !ga:
		return crossPlain(b, a)
	}
	if !ga || !gb {
		return inside(a, b)
	}
	x, u, _ := span(a)
	y, v, _ := span(b)
	if !strings.HasPrefix(x, y) && !strings.HasPrefix(y, x) {
		return false
	}
	if !strings.HasSuffix(u, v) && !strings.HasSuffix(v, u) {
		return false
	}
	// The literal anchors agree; the globs cross only when a file could
	// match both. Each candidate that matches both is a real common file, so
	// this never warns without a possible common edit.
	longer := func(s, t string) string {
		if len(s) >= len(t) {
			return s
		}
		return t
	}
	p, s := longer(x, y), longer(u, v)
	fills := map[string]bool{
		"": true, "x": true, "x/y": true, "x/": true, "x/y/z": true,
	}
	for _, f := range midFills(a) {
		fills[f] = true
	}
	for _, f := range midFills(b) {
		fills[f] = true
	}
	for _, f := range classChars(a) {
		fills[f] = true
	}
	for _, f := range classChars(b) {
		fills[f] = true
	}
	for f := range fills {
		if c := p + f + s; glob(a, c) && glob(b, c) {
			return true
		}
	}
	return false
}
func sharedTreeEditor(rows []string, live []any, self, selfCwd string, env platform.Env, cwd string) bool {
	if selfCwd == "" {
		return false
	}
	for _, row := range rows {
		f := strings.Split(row, "\t")
		at := func(i int) string {
			if i < len(f) {
				return f[i]
			}
			return ""
		}
		if at(0) == "" || at(0) == self || at(6) != selfCwd || !core.RoleIsEdit(at(3), env, cwd) {
			continue
		}
		for _, entry := range live {
			obj, ok := entry.(*jsonjs.Object)
			if !ok {
				continue
			}
			name, _ := obj.Get("name")
			pane, _ := obj.Get("pane_id")
			if name == at(0) && (pane == nil || pane == at(1)) {
				return true
			}
		}
	}
	return false
}

func safeLiveAgents(env platform.Env) (agents []any) {
	defer func() {
		if recover() != nil {
			agents = nil
		}
	}()
	return herdr.LiveAgents(env, herdr.Timeout)
}

func isInteractiveReady(agent string, env platform.Env) bool {
	r := platform.RunCli("herdr", []string{"agent", "get", agent}, platform.RunOptions{Env: env, TimeoutMs: int(herdr.Timeout / time.Millisecond)})
	if r.Status == nil || *r.Status != 0 || r.Stdout == "" {
		return true
	}
	value, err := jsonjs.Parse([]byte(r.Stdout))
	if err != nil {
		return true
	}
	root, ok := value.(*jsonjs.Object)
	if !ok {
		return true
	}
	result, _ := root.Get("result")
	resultObject, ok := result.(*jsonjs.Object)
	if !ok {
		return true
	}
	agentValue, _ := resultObject.Get("agent")
	agentObject, ok := agentValue.(*jsonjs.Object)
	if !ok {
		return true
	}
	ready, found := agentObject.Get("interactive_ready")
	return !found || ready == nil || ready == true
}

func authCauses(screen string) []string {
	causes := []string{}
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.ReplaceAll(screen, "\r\n", "\n"), "\n") {
		d := provider.ProviderDetect("idle", line)
		if d != nil && d.Status == "provider-error" && d.Auth && !seen[d.Cause] {
			seen[d.Cause] = true
			causes = append(causes, d.Cause)
		}
	}
	return causes
}

func staleAuthBlock(agent string, env platform.Env, h0, preSeq string, enterSent, resent bool) bool {
	if enterSent || resent {
		return false
	}
	state := herdr.AgentState(agent, env, herdr.Timeout, nil)
	if state.State != "blocked" {
		return false
	}
	if preSeq != "" && seqString(state.Seq) != "" && seqString(state.Seq) != preSeq {
		return false
	}
	screen := herdr.AgentRead(env, agent, "visible", nil)
	if strconv.FormatUint(uint64(waitpkg.CksumField(screen)), 10) != h0 {
		return false
	}
	d := provider.ProviderDetect(state.State, screen)
	return d != nil && d.Status == "provider-error" && d.Auth
}

func noWaitObservation(agent, role, kind, lane, model, report string, env platform.Env, sd string, ctx *core.Config, priorAuth map[string]bool) (string, *jsonjs.Object) {
	if nonEmpty(report) {
		return "submitted", nil
	}
	state := herdr.AgentState(agent, env, herdr.Timeout, nil)
	if state.State == "working" || state.State == "unavailable" {
		return "submitted", nil
	}
	visible20 := herdr.AgentRead(env, agent, "visible", intPtr(20))
	if q := provider.QuotaDetect(state.State, visible20); q != nil {
		if lane == "" {
			lane = core.LaneOfRole(ctx, role, env)
		}
		return "quota", jsonjs.O("status", "quota", "lane", lane, "model", model, "match", q[0], "renewal", q[1])
	}
	visible40 := herdr.AgentRead(env, agent, "visible", intPtr(40))
	if provider.DialogKind(kind, visible40) == "question" {
		return "submitted", nil
	}
	recent := herdr.AgentRead(env, agent, "recent-unwrapped", intPtr(40))
	d := provider.ProviderDetect(state.State, recent)
	if d != nil && d.Status == "provider-error" && d.Auth && !priorAuth[d.Cause] {
		if lane == "" {
			lane = core.LaneOfRole(ctx, role, env)
		}
		return "provider-error", jsonjs.O("status", "provider-error", "lane", lane, "model", model, "cause", d.Cause)
	}
	_ = sd
	return "submitted", nil
}

func cmdRun(argv []string, ctx *core.Config, env platform.Env, cwd string) int {
	if len(argv) < 1 || argv[0] == "" {
		fmt.Fprintln(platform.Stderr, "soho: 1: role")
		return 1
	}
	if len(argv) < 2 || argv[1] == "" {
		fmt.Fprintln(platform.Stderr, "soho: 2: brief.md")
		return 1
	}
	role, brief := argv[0], argv[1]
	platform.LauncherPath(env)
	spawnArgs, dispatchArgs, noWait := splitRunArgs(argv[2:])
	oldOut := platform.Stdout
	var spawnOut strings.Builder
	platform.Stdout = &spawnOut
	var spawnErr any
	func() {
		defer func() { spawnErr = recover(); platform.Stdout = oldOut }()
		spawn.CmdSpawn(append([]string{role}, spawnArgs...), ctx, env, cwd)
	}()
	if spawnErr != nil {
		panic(spawnErr)
	}
	spawned := strings.TrimRight(spawnOut.String(), "\n")
	value, err := jsonjs.Parse([]byte(spawned))
	name := "null"
	if err == nil {
		if obj, ok := value.(*jsonjs.Object); ok {
			if v, ok := obj.Get("name"); ok && v != nil {
				name = fmt.Sprint(v)
			}
		}
	}
	fmt.Fprintln(platform.Stdout, spawned)
	_ = cmdDispatch(append([]string{name, brief}, dispatchArgs...), ctx, env, cwd)
	if !noWait {
		_ = stats.CmdCollect([]string{name}, stats.CommandContext{Config: ctx, Env: env, Cwd: cwd, FrictionLog: frictionLogPath})
	}
	return 0
}

func splitRunArgs(argv []string) ([]string, []string, bool) {
	spawnArgs, dispatchArgs := []string{}, []string{}
	noWait := false
	valueFlags := map[string]bool{"--name": true, "--kind": true, "--direction": true, "--ratio": true, "--cwd": true, "--pane": true, "--effort": true, "--model": true, "--approvals": true, "--tab-label": true, "--mcp": true}
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if a == "--" {
			spawnArgs = append(spawnArgs, argv[i:]...)
			break
		}
		if valueFlags[a] {
			if i+1 >= len(argv) {
				core.DieFriction("run: "+a+" expects a value", 2, frictionLogPath, "run")
			}
			spawnArgs = append(spawnArgs, a, argv[i+1])
			i++
			continue
		}
		switch a {
		case "--reuse", "--fresh":
			spawnArgs = append(spawnArgs, a)
		case "--force":
			// DJA-392: the spawn's pressure-refusal skip (the warning line
			// still goes out); run never applies it to anything else.
			spawnArgs = append(spawnArgs, a)
		case "--timeout":
			if i+1 >= len(argv) {
				core.DieFriction("run: --timeout expects a value", 2, frictionLogPath, "run")
			}
			// Refused before the spawn: run opens the pane before dispatch, so
			// an invalid duration must not cost a pane.
			if _, ok := core.ParseTimeoutMS(argv[i+1]); !ok {
				core.DieFriction(fmt.Sprintf("dispatch: --timeout expects milliseconds or a duration such as 90s, 30m or 2h, got '%s'", argv[i+1]), 2, frictionLogPath, "dispatch")
			}
			dispatchArgs = append(dispatchArgs, a, argv[i+1])
			i++
		case "--allow-same-family":
			dispatchArgs = append(dispatchArgs, a)
		case "--no-wait":
			dispatchArgs = append(dispatchArgs, a)
			noWait = true
		default:
			core.DieFriction("run: unknown option "+a, 2, frictionLogPath, "run")
		}
	}
	return spawnArgs, dispatchArgs, noWait
}
