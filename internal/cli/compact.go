package cli

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/provider"
	"github.com/djalmajr/soho/internal/spawn"
	"github.com/djalmajr/soho/internal/text"
)

// compactPollInterval is the pause between the compact's screen reads.
const compactPollInterval = 2 * time.Second

// compactComposerWindow is how long the composer confirm waits, after each
// Enter, for the /compact line to leave the codex composer, one poll per
// second, before a second Enter is tried.
const compactComposerWindow = 5 * time.Second

// compactComposerPoll is the pause between the composer re-reads while
// waiting for the /compact line to leave the codex composer.
const compactComposerPoll = time.Second

const compactScreenLines = 40

// compactThinkingTaps caps the shift+tab presses in one direction while the
// compact turns pi's thinking off before the /compact or restores it after.
const compactThinkingTaps = 8

// compactProofByKind holds the compaction proof each supported kind prints
// after `/compact`; a kind without an entry has no verified compact command.
// The proof only counts below the line where `/compact` was sent: an earlier
// compaction on the screen is not proof of this one.
// compactOpenCodeMenuWindow is how long the compact waits for opencode's
// command menu to show up in the visible screen after the /compact text is
// sent, before treating the menu as absent.
const compactOpenCodeMenuWindow = 2 * time.Second

// compactOpenCodeMenuPoll is the pause between visible-screen reads while
// waiting for opencode's command menu.
const compactOpenCodeMenuPoll = 200 * time.Millisecond

// compactPiRunning is what pi shows while its compaction is still running.
const compactPiRunning = "Compacting context"

// compactOpenCodeProofMarker is a non-empty proof marker for opencode: its
// compaction proof is the line regex compactOpenCodeDoneRE, not a substring,
// so compactRun routes opencode to the regex. Keeping the marker non-empty
// lets the "verified compact command" check pass for opencode (cmdCompact and
// dispatch) while a kind without an entry still gets the no-verified message.
const compactOpenCodeProofMarker = "opencode: compactOpenCodeDoneRE"

// compactOpenCodeDoneRE matches the opencode line that closes a compaction,
// with the leading spaces trimmed away:
// `▣ Compaction · <model> · <duration>`. A line without the duration (the
// compaction still running) is not proof.
var compactOpenCodeDoneRE = regexp.MustCompile(`^\x{25A3}\s+Compaction\s+\x{B7}\s+.+\s+\x{B7}\s+\d\S*(\s+\d\S*)*\s*$`)

// compactOpenCodeMenuLineRE matches a line of opencode's command menu, the
// popup above the input box after / is typed: a box border, one space, and
// the command name. The input box itself pads with two spaces, so the text
// typed there does not match.
var compactOpenCodeMenuLineRE = regexp.MustCompile(`^[│┃] /(\S+)`)

// compactProofByKind holds the compaction proof each supported kind prints
// after `/compact`; a kind without an entry has no verified compact command.
// For the kinds that echo the command (claude, codex) the proof only counts
// when it is new since the pre-send read (a new line, a grown count, or below
// a grown echo count): an earlier compaction on the screen is not proof of
// this one. pi and opencode do not echo it, so their proof counts a line that
// was not on screen before the send, and opencode also when its count of
// proof lines grows.
var compactProofByKind = map[string]string{
	"claude":   "Compacted",
	"codex":    "Context compacted",
	"pi":       "Compacted from",
	"opencode": compactOpenCodeProofMarker,
}

// compactThinkingFooterRE matches a thinking level at the end of a footer
// line only when a boundary precedes it (start of line, whitespace, • or :),
// so a word like `cutoff` does not read as `off`.
var compactThinkingFooterRE = regexp.MustCompile(`(^|[\s•:])(off|minimal|low|medium|high|xhigh|max)\s*$`)

func compactProof(kind string) string {
	return compactProofByKind[kind]
}

// compactEnding is a screen line that ends the /compact wait without the
// kind's proof: a conversation with nothing to compact (a success without
// compaction) and a compaction that failed.
type compactEnding struct{ status, text string }

// compactEndingsByKind holds the ending texts next to the kind's proof, in
// the order they must be checked. pi lists the nothing-to-compact line first
// because that line also contains `Compaction failed:`; claude lists the
// failure first, so a failure on screen is the outcome reported.
var compactEndingsByKind = map[string][]compactEnding{
	"claude": {
		{status: "failed", text: "Error compacting conversation"},
		{status: "nothing-to-compact", text: "Not enough messages to compact."},
		{status: "nothing-to-compact", text: "No messages to compact"},
	},
	"pi": {
		{status: "nothing-to-compact", text: "Nothing to compact"},
		{status: "failed", text: "Compaction failed:"},
	},
}

func compactEndings(kind string) []compactEnding {
	return compactEndingsByKind[kind]
}

func cmdCompact(argv []string, ctx *core.Config, env platform.Env, cwd string) int {
	agent := ""
	// Without --timeout the compact_timeout key sets the limit; the flag
	// below overrides it.
	timeoutMS := core.CompactTimeoutMS(ctx, env)
	for i := 0; i < len(argv); i++ {
		switch argv[i] {
		case "--timeout":
			if i+1 >= len(argv) {
				core.DieFriction("compact: --timeout expects a value", 2, frictionLogPath, "compact")
			}
			i++
			raw := argv[i]
			value, ok := core.ParseTimeoutMS(raw)
			if !ok || value <= 0 {
				core.DieFriction(fmt.Sprintf("compact: --timeout expects milliseconds or a duration such as 90s, 30m or 2h, got '%s'", raw), 2, frictionLogPath, "compact")
			}
			timeoutMS = value
			// A unit names the intent: only a unitless integer warns.
			if core.IsPlainTimeoutMS(raw) {
				core.WarnShortTimeout("compact", frictionLogPath, value)
			}
		default:
			if agent != "" {
				core.DieFriction(fmt.Sprintf("compact: unexpected argument '%s'", argv[i]), 2, frictionLogPath, "compact")
			}
			agent = argv[i]
		}
	}
	if agent == "" {
		core.DieFriction("compact: <agent> [--timeout MS]", 2, frictionLogPath, "compact")
	}
	sd := core.StateDirPath(ctx, env, cwd)
	line := core.RosterLine(sd, agent)
	if line == "" {
		core.DieFriction(fmt.Sprintf("agent '%s' is not in this skill's roster (spawn it first, or pass a name you spawned)", agent), 3, frictionLogPath, "compact")
	}
	cols := strings.Split(line, "\t")
	at := func(i int) string {
		if i < len(cols) {
			return cols[i]
		}
		return ""
	}
	kind := at(2)
	if compactProof(kind) == "" {
		core.DieFriction(fmt.Sprintf("compact: kind '%s' has no verified compact command; release --close and spawn --fresh instead", kind), 2, frictionLogPath, "compact")
	}
	return compactRun(agent, at(1), kind, at(3), at(11), at(8), timeoutMS, ctx, env, cwd, false)
}

// compactRun sends /compact once to a rostered worker through its pane and
// waits for a fresh proof of the compaction (claude, codex and pi: a proof
// line unseen since the pre-send read, a proof-line count that grew since
// the pre-send read, or a proof below the last /compact echo when the echo
// count grew since the pre-send read; a claude also counts when its session
// transcript gains a compact_boundary line, because the screen shows no
// confirmation; an opencode counts its dated done line), then for the worker
// back at idle or done. It is the shared step behind the `compact`
// command and `dispatch --compact`. The caller must have checked the roster
// and the kind. A pi worker whose footer shows a thinking level gets it
// turned off with shift+tab before the send and the level back after the
// compaction (a timeout or a failure included); its result JSON then carries
// thinking_restored. A busy or unreachable worker dies (10, 4, 6); on
// success it
// prints the result JSON (a stderr line inside a dispatch) and returns 0,
// on timeout it prints the timeout JSON and returns 9; a claude or a pi with
// nothing to compact returns 0 with the nothing-to-compact result (a stderr
// line inside a dispatch), and a claude or a pi that reports a failed
// compaction returns 9 with the failed result and a warning that cites the
// failing screen line. An opencode worker gets its command menu checked
// before the Enter: only /compact on the first menu line runs it; a menu
// without /compact (an empty session) clears the box and reports
// nothing-to-compact (0), and /compact not on top or a missing menu clears
// the box and exits 4. A codex gets its composer checked right after the
// Enter, on the first proof poll (up to five seconds, one poll per second):
// a /compact that sits in the box, with no proof below the last /compact,
// means the Enter did not submit it (it can
// land before the TUI draws /compact and its command popup), so before any
// proof is trusted the line must leave the box; a second Enter gets the same
// check, and a line that stays in the box after both is cleared with ctrl+u
// and exits 4 (nothing was compacted). When the deadline runs out without proof or ending,
// one last read runs the same proofs (/compact is never sent again) and a
// claude timeout JSON carries a transcript field (counted, missing or
// unreadable) saying what the session transcript gave. It never uses `agent
// prompt` and never resends.
func compactRun(agent, pane, kind, role, lane, model string, timeoutMS int64, ctx *core.Config, env platform.Env, cwd string, inDispatch bool) int {
	state := herdr.AgentState(agent, env, herdr.Timeout, nil)
	if state.State == "working" || state.State == "blocked" {
		core.DieFriction(fmt.Sprintf("compact: agent '%s' is %s; compact only an idle worker", agent, state.State), 10, frictionLogPath, "compact")
	}
	compactDieOnDeadWorker(agent, state)
	proof := compactProof(kind)
	// The proof lines already on screen before the send: pi does not echo the
	// /compact it runs, so a proof counts when it sits below the echoed command
	// (claude, codex) or when it is a line that was not there before the send.
	before, readOK := herdr.AgentReadOK(env, agent, "recent", intPtr(compactScreenLines))
	if !readOK {
		// Without the screen before the send, an earlier compaction still on screen
		// would read as this one's proof: send nothing.
		core.DieFriction(fmt.Sprintf("compact: could not read the screen of '%s' before sending; nothing was sent", agent), 4, frictionLogPath, "compact")
	}
	// opencode proves the compaction with the done-line regex instead of a
	// substring, and counts a screen whose count of proof lines grew (it does
	// not echo the /compact it runs); claude, codex and pi compare each screen
	// against the pre-send baseline: a fresh proof line, a grown proof-line
	// count, or a grown echo count with the proof below the last echo. A stale
	// proof below a stale echo on an otherwise unchanged screen is not proof.
	var proofNew func(screen string) bool
	if kind == "opencode" {
		seen := compactOpenCodeProofLines(before)
		proofNew = func(screen string) bool { return compactOpenCodeProofNew(screen, before, seen) }
	} else {
		base := compactProofBaseline{screen: before, proof: proof, seen: compactProofLines(before, proof)}
		proofNew = func(screen string) bool { return compactProofNew(screen, base) }
	}
	endings := compactEndings(kind)
	endSeen := make([]map[string]bool, len(endings))
	for i := range endings {
		endSeen[i] = compactProofLines(before, endings[i].text)
	}
	// claude: the /compact confirmation never reaches the screen (the real
	// screen keeps showing the older conversation), but the session
	// transcript gains a compact_boundary line when the compaction finishes;
	// the count of those lines before the send is the baseline. A transcript
	// that is missing or unreadable leaves the screen proofs as the only
	// evidence, with no new warning.
	var transcriptPath string
	transcriptBefore := 0
	transcriptOK := false
	if kind == "claude" {
		transcriptPath = herdr.ClaudeTranscriptPath(agent, env)
		if transcriptPath != "" {
			transcriptBefore, transcriptOK = herdr.CountClaudeCompactBoundaries(transcriptPath)
		}
	}
	// transcriptRisen reports the transcript proof: a compact_boundary line
	// past the baseline counts the compaction done even though the screen
	// shows nothing.
	transcriptRisen := func() bool {
		if !transcriptOK {
			return false
		}
		n, ok := herdr.CountClaudeCompactBoundaries(transcriptPath)
		return ok && n > transcriptBefore
	}
	thinkingBefore := ""
	thinkingChanged := false
	restoreDone := false
	// A failure after the thinking went off (a send that fails, a worker that
	// becomes unavailable or dies) exits through DieFriction; the level still
	// goes back first, so the next brief does not run with thinking off.
	defer func() {
		if thinkingChanged && !restoreDone {
			restoreDone = true
			if shown := compactThinkingLevel(agent, pane, env, thinkingBefore); shown != thinkingBefore {
				core.Warn(fmt.Sprintf("compact: could not restore '%s' thinking to '%s' (shows '%s'); set it by hand before the next brief", agent, thinkingBefore, shown), frictionLogPath, "compact")
			}
		}
	}()
	if kind == "pi" {
		// pi cuts its summary at a high thinking level: turn it off before the
		// /compact and give the level back after; a footer without a level or
		// one already off is left as is.
		thinkingBefore = compactThinkingFooter(before)
		if thinkingBefore != "" && thinkingBefore != "off" {
			thinkingChanged = true
			if level := compactThinkingLevel(agent, pane, env, "off"); level != "off" {
				core.Warn(fmt.Sprintf("compact: could not turn '%s' thinking off before compacting (still '%s'); compacting anyway", agent, level), frictionLogPath, "compact")
			}
		}
	}
	if !herdr.PaneSendText(pane, "/compact", env) {
		core.DieFriction(fmt.Sprintf("compact: could not send /compact to pane '%s'; release --close and spawn --fresh instead", pane), 4, frictionLogPath, "compact")
	}
	// opencode pops up a command menu above the input box when the /compact
	// text is sent; Enter runs the top item, so only press it when /compact is
	// on top.
	endStatus := ""
	endLine := ""
	compacted := false
	// proofObserved latches the first positive evidence of this attempt — a
	// screen proof or the claude transcript — for the rest of the run,
	// including the last read after the deadline: a screen that scrolls (or a
	// transient transcript error) while the idle wait is still running must
	// not lose evidence already seen. The latch never skips the return to idle.
	proofObserved := false
	proceedToWait := true
	if kind == "opencode" {
		switch compactOpenCodeMenuSelection(agent, env) {
		case openCodeMenuNothing:
			// The menu has no /compact (an empty session): pressing Enter would
			// run the top item (e.g. /review); clear the box and report nothing
			// to compact, like claude and pi.
			herdr.PaneSendKeys(pane, "ctrl+u", env)
			endStatus = "nothing-to-compact"
			proceedToWait = false
		case openCodeMenuMissing:
			// /compact is not on top, or the menu never appeared: clear the box
			// and stop; nothing was compacted.
			herdr.PaneSendKeys(pane, "ctrl+u", env)
			core.DieFriction(fmt.Sprintf("compact: could not select /compact in the command menu of '%s'; nothing was compacted", agent), 4, frictionLogPath, "compact")
		}
	}
	start := platform.Now()
	if proceedToWait {
		if !herdr.PaneSendKeys(pane, "Enter", env) {
			core.DieFriction(fmt.Sprintf("compact: could not send Enter to pane '%s'; release --close and spawn --fresh instead", pane), 4, frictionLogPath, "compact")
		}
		deadline := start.Add(time.Duration(timeoutMS) * time.Millisecond)
		for {
			if !compacted && endStatus == "" {
				screen := herdr.AgentRead(env, agent, "recent", intPtr(compactScreenLines))
				if kind == "codex" && compactCodexComposerStuck(screen, proof) && !proofNew(screen) {
					// A /compact on the composer line, with no proof below the last
					// /compact, means the command never ran: an Enter that lands before
					// the TUI draws /compact and its command popup does not submit the
					// box, and no proof can then appear (codex has no endings), so
					// without this check the wait only ends at the full deadline. A
					// delivered /compact instead leaves its echo in the history above a
					// now-empty or busy composer, with the proof below the last echo:
					// the composer is not stuck then, fresh or stale proof alike, and
					// the screen goes to the proof check — a stale proof with no new
					// evidence ends at the deadline timeout, with no second Enter and no
					// clear. When the composer is really stuck: confirm the line left the
					// box (five seconds, one poll per second) before any proof is
					// trusted; one more Enter gets the same confirm, and a line that
					// stays in the box after both is cleared and reported (4, nothing
					// was compacted).
					screen = compactCodexComposerRetry(agent, pane, kind, proof, env)
				}
				if proofNew(screen) || transcriptRisen() {
					proofObserved = true
				}
				if proofObserved {
					compacted = compactWaitIdle(agent, env, deadline)
				} else {
					// An ending counts only when it belongs to this attempt: below a
					// /compact the screen gained since the pre-send read, on a line
					// not there before the send, or when its line count on screen
					// grew (a CLI that does not echo the command); it never waits
					// for the worker back at idle.
					for i := range endings {
						if line, ok := compactEndingNew(screen, before, endings[i].text, endSeen[i]); ok {
							endStatus = endings[i].status
							endLine = line
							break
						}
					}
					if endStatus == "" {
						// No proof yet: a worker that died meanwhile stops the wait now (6, 4)
						// instead of running out the deadline.
						compactDieOnDeadWorker(agent, herdr.AgentState(agent, env, herdr.Timeout, nil))
					}
				}
			}
			if compacted || endStatus != "" || !platform.Now().Before(deadline) {
				break
			}
			time.Sleep(compactPollInterval)
		}
	}
	// The deadline ran out without proof or ending: the compaction can finish
	// right after it. One last read runs the same proofs (the screen, the
	// claude transcript, the opencode regex and the endings); /compact is
	// never sent again. A proof observed earlier (proofObserved) is not
	// re-evaluated on a screen that may have scrolled: it still gets the
	// capped idle wait that follows.
	if !compacted && endStatus == "" {
		screen := herdr.AgentRead(env, agent, "recent", intPtr(compactScreenLines))
		if proofObserved || proofNew(screen) || transcriptRisen() {
			// The idle wait that follows gets a short cap of its own: ten poll
			// intervals from now.
			compacted = compactWaitIdle(agent, env, platform.Now().Add(compactPollInterval*10))
		} else {
			for i := range endings {
				if line, ok := compactEndingNew(screen, before, endings[i].text, endSeen[i]); ok {
					endStatus = endings[i].status
					endLine = line
					break
				}
			}
		}
	}
	elapsed := platform.Now().Sub(start).Milliseconds()
	thinkingRestored := false
	if thinkingChanged {
		restoreDone = true
		// The /compact ran with the thinking off: give the level back before
		// any exit, a timeout or a failure included; the result reports
		// whether it came back.
		if shown := compactThinkingLevel(agent, pane, env, thinkingBefore); shown == thinkingBefore {
			thinkingRestored = true
		} else {
			core.Warn(fmt.Sprintf("compact: could not restore '%s' thinking to '%s' (shows '%s'); set it by hand before the next brief", agent, thinkingBefore, shown), frictionLogPath, "compact")
		}
	}
	// The result JSON gains thinking_restored only when the level was changed;
	// extra pairs (the claude timeout's transcript field) come after
	// elapsed_ms.
	resultJSON := func(status string, extra ...any) string {
		pairs := []any{"agent", agent, "kind", kind, "status", status, "elapsed_ms", elapsed}
		pairs = append(pairs, extra...)
		if thinkingChanged {
			pairs = append(pairs, "thinking_restored", thinkingRestored)
		}
		return jsonjs.Stringify(jsonjs.O(pairs...))
	}
	if endStatus != "" {
		if endStatus == "failed" {
			// Cite the screen line that matched the failure (claude and pi),
			// sanitized and redacted, so the warning shows what the CLI reported.
			core.Warn(fmt.Sprintf("compact: '%s' reported %q; nothing was compacted", agent, text.SanitizeCause(text.RedactSecrets(endLine))), frictionLogPath, "compact")
		}
		if inDispatch && endStatus == "nothing-to-compact" {
			// The dispatch keeps one JSON line on stdout: its own result.
			fmt.Fprintf(platform.Stderr, "soho: dispatch: '%s' had nothing to compact\n", agent)
			return 0
		}
		fmt.Fprintln(platform.Stdout, resultJSON(endStatus))
		if endStatus == "failed" {
			return 9
		}
		return 0
	}
	if !compacted {
		// pi shows "Compacting context..." while its compaction runs: a timeout
		// then is the observer giving up, not the compaction failing.
		if kind == "pi" && strings.Contains(herdr.AgentRead(env, agent, "recent", intPtr(compactScreenLines)), compactPiRunning) {
			core.Warn(fmt.Sprintf("compact: '%s' is still compacting (%q on screen); do not send /compact again: wait and read its screen before the next brief", agent, compactPiRunning), frictionLogPath, "compact")
			pairs := []any{"agent", agent, "kind", kind, "status", "timeout", "elapsed_ms", elapsed, "still_compacting", true}
			if thinkingChanged {
				pairs = append(pairs, "thinking_restored", thinkingRestored)
			}
			fmt.Fprintln(platform.Stdout, jsonjs.Stringify(jsonjs.O(pairs...)))
			return 9
		}
		if kind == "claude" {
			// The timeout says what the claude session transcript gave, without
			// a path, a session id or content: counted when the file was found
			// and counted without a new compact_boundary, missing when there is
			// no id session or the file does not exist, unreadable when the
			// file was found but could not be read.
			fmt.Fprintln(platform.Stdout, resultJSON("timeout", "transcript", compactClaudeTranscriptState(agent, env)))
			return 9
		}
		fmt.Fprintln(platform.Stdout, resultJSON("timeout"))
		return 9
	}
	compactThinkingWarning(agent, kind, role, lane, model, ctx, env, cwd)
	if inDispatch {
		// The dispatch keeps one JSON line on stdout: its own result.
		fmt.Fprintf(platform.Stderr, "soho: dispatch: compacted '%s' in %d ms\n", agent, elapsed)
		return 0
	}
	fmt.Fprintln(platform.Stdout, resultJSON("compacted"))
	return 0
}

// compactDieOnDeadWorker exits 4 (unavailable) or 6 (gone) with the same
// message before the send and during the idle wait; it returns when the
// worker is neither.
func compactDieOnDeadWorker(agent string, state herdr.AgentStateResult) {
	if state.State == "unavailable" {
		cause := state.Cause
		if cause == "" {
			cause = "herdr agent get failed"
		}
		core.DieFriction(fmt.Sprintf("compact: agent '%s' is unavailable: %s", agent, cause), 4, frictionLogPath, "compact")
	}
	if state.State == "gone" {
		core.DieFriction(fmt.Sprintf("compact: agent '%s' is no longer live", agent), 6, frictionLogPath, "compact")
	}
}

// compactClaudeTranscriptState reports, for the claude timeout JSON only, what
// the session transcript gave: "counted" when the file was found and counted
// (a timeout then has no new compact_boundary), "missing" when agent get has
// no id session or the file does not exist, and "unreadable" when the file
// was found but could not be read. No path, session id or content ever
// reaches the JSON.
func compactClaudeTranscriptState(agent string, env platform.Env) string {
	path := herdr.ClaudeTranscriptPath(agent, env)
	if path == "" {
		return "missing"
	}
	if _, ok := herdr.CountClaudeCompactBoundaries(path); !ok {
		return "unreadable"
	}
	return "counted"
}

// compactWaitIdle polls the agent state until it is idle or done or the
// deadline passes; a Codex worker stays working for a few seconds after the
// proof, so this keeps waiting instead of treating working as a failure. A
// worker that dies in the meantime (gone, unavailable) exits right away (6,
// 4) instead of waiting out the deadline as a timeout.
func compactWaitIdle(agent string, env platform.Env, deadline time.Time) bool {
	for {
		state := herdr.AgentState(agent, env, herdr.Timeout, nil)
		if state.State == "idle" || state.State == "done" {
			return true
		}
		compactDieOnDeadWorker(agent, state)
		if !platform.Now().Before(deadline) {
			return false
		}
		time.Sleep(compactPollInterval)
	}
}

// compactCodexComposerLine returns the codex composer line and whether it
// was found, with the peer composer region rule: the last line (scanned from
// the end) whose left-trimmed text is or starts with '› ', within the last
// eight non-empty lines. A /compact the box still holds sits on that line;
// an executed command leaves the box (the echo goes to the history, above
// the now-empty composer).
func compactCodexComposerLine(screen string) (string, bool) {
	lines := compactLines(screen)
	nonEmpty := 0
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) == "" {
			continue
		}
		nonEmpty++
		head := strings.TrimLeft(lines[i], " \t")
		if head == "›" || strings.HasPrefix(head, "› ") {
			return lines[i], nonEmpty <= 8
		}
		if nonEmpty >= 8 {
			break
		}
	}
	return "", false
}

// compactCodexComposerStuck reports whether the /compact still sits in the
// codex composer: the composer line is the sent command itself, with no
// proof below the last /compact. A proof below the last /compact (fresh or
// stale) means the command was delivered and its echo sits in the history
// above an empty or busy composer: not stuck. A stale proof is never success
// by itself — the fresh-proof rule decides that; here it only keeps the
// retry protocol (a second Enter and a clear) from firing on a delivered
// command, for every consumer of the check (the proof poll, the confirm and
// the re-read before the second Enter). A screen without a composer line (a
// read before the TUI drew the box, or a frozen worker) is not stuck: the
// proof wait and its liveness checks take over.
func compactCodexComposerStuck(screen, proof string) bool {
	if compactProofBelow(screen, proof) {
		return false
	}
	line, ok := compactCodexComposerLine(screen)
	if !ok {
		return false
	}
	return compactCommandLine(line)
}

// compactComposerCleared waits, up to compactComposerWindow with one poll
// per second, for the /compact line to leave the codex composer after an
// Enter; a worker that dies in the meantime stops the wait at once (6, 4),
// like the proof wait. It returns the first screen whose composer no longer
// holds /compact, or "" when the line is still there at the end of the
// window.
func compactComposerCleared(agent, proof string, env platform.Env) string {
	deadline := platform.Now().Add(compactComposerWindow)
	for {
		screen := herdr.AgentRead(env, agent, "recent", intPtr(compactScreenLines))
		if !compactCodexComposerStuck(screen, proof) {
			return screen
		}
		compactDieOnDeadWorker(agent, herdr.AgentState(agent, env, herdr.Timeout, nil))
		if !platform.Now().Before(deadline) {
			return ""
		}
		time.Sleep(compactComposerPoll)
	}
}

// compactCodexComposerRetry is the stuck-composer protocol after the first
// Enter (already sent by the caller): it confirms the line left the box
// (five seconds, one poll per second), then — right before a second Enter —
// re-reads the state and the screen so the extra key is never sent to a
// worker that is blocked on a question (a dialog) or that is no longer
// idle/done (it would answer the question instead of running /compact). It
// sends one more Enter with the same confirm, and a line that stays in the
// box after both is cleared with ctrl+u and reported (4, nothing was
// compacted). It returns the first screen whose composer no longer holds
// /compact, so the proof check runs on a screen past the stuck state.
func compactCodexComposerRetry(agent, pane, kind, proof string, env platform.Env) string {
	if screen := compactComposerCleared(agent, proof, env); screen != "" {
		return screen
	}
	// Right before the second Enter: re-read the state and the screen. A
	// worker that is blocked on a question (a dialog the provider recognizes)
	// must not get an extra Enter — it would answer the question, not run
	// /compact. A worker that is neither idle nor done (dialog or not) is
	// also not pressed: in both branches no key is sent, not even the clear.
	state := herdr.AgentState(agent, env, herdr.Timeout, nil)
	compactDieOnDeadWorker(agent, state)
	screen := herdr.AgentRead(env, agent, "recent", intPtr(compactScreenLines))
	if provider.DialogKind(kind, screen) == "question" {
		core.DieFriction(fmt.Sprintf("compact: '/compact' stayed in the composer of '%s' after the first Enter; showing a dialog after /compact; pressed nothing, nothing was compacted", agent), 4, frictionLogPath, "compact")
	}
	if state.State != "idle" && state.State != "done" {
		core.DieFriction(fmt.Sprintf("compact: '/compact' stayed in the composer of '%s' after the first Enter and the worker is %s; pressed nothing, nothing was compacted", agent, state.State), 4, frictionLogPath, "compact")
	}
	// The box can have changed since the confirm gave up (/compact ran late,
	// or another command is in it now): the second Enter goes only to a
	// composer that still holds /compact; any other screen goes back to the
	// proof check with no key.
	if !compactCodexComposerStuck(screen, proof) {
		return screen
	}
	herdr.PaneSendKeys(pane, "Enter", env)
	if screen := compactComposerCleared(agent, proof, env); screen != "" {
		return screen
	}
	herdr.PaneSendKeys(pane, "ctrl+u", env)
	core.DieFriction(fmt.Sprintf("compact: '/compact' stayed in the composer of '%s' after two Enters; cleared it, nothing was compacted", agent), 4, frictionLogPath, "compact")
	return ""
}

// compactCommandLine reports whether the line is the sent /compact command
// itself: exactly /compact once surrounding whitespace and a leading prompt
// marker (>, ›, ❯, │ or $) are removed. A line that merely mentions /compact
// inside other text is not the anchor.
func compactCommandLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	for _, marker := range []string{">", "›", "❯", "│", "$"} {
		if strings.HasPrefix(trimmed, marker) {
			trimmed = strings.TrimSpace(trimmed[len(marker):])
			break
		}
	}
	return trimmed == "/compact"
}

// compactProofBelow reports whether the kind's proof appears below the last
// line of the screen that is the sent /compact command; proof above it (an
// earlier compaction) does not count, and a screen without the sent command
// has no proof.
// compactProofLines returns the trimmed screen lines that hold the proof.
func compactProofLines(screen, proof string) map[string]bool {
	out := map[string]bool{}
	for _, line := range strings.Split(strings.ReplaceAll(screen, "\r\n", "\n"), "\n") {
		if strings.Contains(line, proof) {
			out[strings.TrimSpace(line)] = true
		}
	}
	return out
}

// compactProofBaseline is the pre-send screen state the fresh-proof rule
// compares a later screen against: the baseline screen, the kind's proof
// text, and the trimmed proof lines already on screen before the send.
type compactProofBaseline struct {
	screen string
	proof  string
	seen   map[string]bool
}

// compactProofNew reports a proof of this /compact for the kinds that echo
// the command or print a fresh line (claude, codex, pi): a proof line unseen
// since the baseline, OR a proof-line count that grew since the baseline, OR
// a proof below the last /compact echo when the echo count grew since the
// baseline. A screen that only redrew (clock, context, a working/idle line,
// state_change_seq) leaves the proof text and the counts unchanged, so a
// stale proof below a stale echo is not proof, even after a scroll that
// shifts the lines; a new echo without a proof is not proof either. The
// opencode regex rule and the claude transcript rule are separate and
// unchanged.
func compactProofNew(screen string, base compactProofBaseline) bool {
	for line := range compactProofLines(screen, base.proof) {
		if !base.seen[line] {
			return true
		}
	}
	if compactLineCount(screen, base.proof) > compactLineCount(base.screen, base.proof) {
		return true
	}
	if compactCommandCount(screen) > compactCommandCount(base.screen) && compactProofBelow(screen, base.proof) {
		return true
	}
	return false
}

func compactProofBelow(screen, proof string) bool {
	lines := strings.Split(strings.ReplaceAll(screen, "\r\n", "\n"), "\n")
	marker := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if compactCommandLine(lines[i]) {
			marker = i
			break
		}
	}
	if marker < 0 {
		return false
	}
	for i := marker + 1; i < len(lines); i++ {
		if strings.Contains(lines[i], proof) {
			return true
		}
	}
	return false
}

// compactCommandCount returns how many lines of the screen are the sent
// /compact command itself.
func compactCommandCount(screen string) int {
	n := 0
	for _, line := range strings.Split(strings.ReplaceAll(screen, "\r\n", "\n"), "\n") {
		if compactCommandLine(line) {
			n++
		}
	}
	return n
}

// compactLines returns the screen's lines with \r\n normalized to \n.
func compactLines(screen string) []string {
	return strings.Split(strings.ReplaceAll(screen, "\r\n", "\n"), "\n")
}

// compactLineCount returns how many lines of the screen contain text.
func compactLineCount(screen, text string) int {
	n := 0
	for _, line := range compactLines(screen) {
		if strings.Contains(line, text) {
			n++
		}
	}
	return n
}

// compactFirstLineBelow returns the first screen line below the last sent
// /compact that contains text, trimmed, or "" when there is no sent /compact
// or no such line below it.
func compactFirstLineBelow(screen, text string) string {
	lines := compactLines(screen)
	marker := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if compactCommandLine(lines[i]) {
			marker = i
			break
		}
	}
	if marker < 0 {
		return ""
	}
	for i := marker + 1; i < len(lines); i++ {
		if strings.Contains(lines[i], text) {
			return strings.TrimSpace(lines[i])
		}
	}
	return ""
}

// compactLastLineWith returns the last screen line that contains text,
// trimmed, or "" when none does.
func compactLastLineWith(screen, text string) string {
	last := ""
	for _, line := range compactLines(screen) {
		if strings.Contains(line, text) {
			last = strings.TrimSpace(line)
		}
	}
	return last
}

// compactOpenCodeProofLines returns the trimmed screen lines that are the
// opencode compaction-done line (the proof, with a duration).
func compactOpenCodeProofLines(screen string) map[string]bool {
	out := map[string]bool{}
	for _, line := range compactLines(screen) {
		trimmed := strings.TrimSpace(line)
		if compactOpenCodeDoneRE.MatchString(trimmed) {
			out[trimmed] = true
		}
	}
	return out
}

// compactOpenCodeProofLineCount returns how many lines of the screen are the
// opencode compaction-done line.
func compactOpenCodeProofLineCount(screen string) int {
	n := 0
	for _, line := range compactLines(screen) {
		if compactOpenCodeDoneRE.MatchString(strings.TrimSpace(line)) {
			n++
		}
	}
	return n
}

// compactOpenCodeProofNew reports an opencode proof that belongs to this
// /compact: a proof line not on screen before the send, or a screen whose
// count of proof lines grew past the pre-send read (opencode does not echo
// the /compact it runs, so an identical later line still counts). The line
// without the duration is not proof.
func compactOpenCodeProofNew(screen, before string, seen map[string]bool) bool {
	if compactOpenCodeProofLineCount(screen) > compactOpenCodeProofLineCount(before) {
		return true
	}
	for line := range compactOpenCodeProofLines(screen) {
		if !seen[line] {
			return true
		}
	}
	return false
}

// compactEndingNew reports an ending that belongs to this /compact attempt,
// returning the screen line that matched (for the failure warning): one below
// a /compact the screen gained since the pre-send read (its answer may repeat
// a line already on screen), one whose line count on screen grew past the
// pre-send read (a CLI that does not echo the command, so a second attempt
// prints an identical line), or one on a line that was not on screen before
// the send. An ending that sat below an older /compact on the unchanged
// screen does not count.
func compactEndingNew(screen, before, text string, seen map[string]bool) (string, bool) {
	if compactCommandCount(screen) > compactCommandCount(before) {
		if line := compactFirstLineBelow(screen, text); line != "" {
			return line, true
		}
	}
	if compactLineCount(screen, text) > compactLineCount(before, text) {
		if line := compactLastLineWith(screen, text); line != "" {
			return line, true
		}
	}
	for _, line := range compactLines(screen) {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(line, text) && !seen[trimmed] {
			return trimmed, true
		}
	}
	return "", false
}

// compactThinkingWarning warns, for pi only, when the last footer line of the
// screen containing a bullet ends with a thinking different from the
// worker's effective effort after compaction; it never changes the exit code.
func compactThinkingWarning(agent, kind, role, lane, model string, ctx *core.Config, env platform.Env, cwd string) {
	if kind != "pi" {
		return
	}
	screen := herdr.AgentRead(env, agent, "recent", intPtr(compactScreenLines))
	thinking := compactThinkingFooter(screen)
	if thinking == "" {
		return
	}
	effort := spawn.ResolveSpawnEffort(role, lane, kind, core.SpawnKindLayer(ctx, role, lane, false, env), ctx, env, cwd, model)
	if effort == "" || thinking == effort {
		return
	}
	core.Warn(fmt.Sprintf("compact: '%s' shows thinking '%s' after compaction; its effort is '%s'", agent, thinking, effort), frictionLogPath, "compact")
}

// compactThinkingLevel presses shift+tab on the pane toward the wanted footer
// level: one press, the poll pause, and a footer re-read after each press, up
// to compactThinkingTaps presses; it reports the level the footer still shows,
// the wanted one when it was reached. A failed key press stops the presses;
// a failed read shows no level.
func compactThinkingLevel(agent, pane string, env platform.Env, wanted string) string {
	shown := ""
	for i := 0; i < compactThinkingTaps; i++ {
		if !herdr.PaneSendKeys(pane, "shift+tab", env) {
			break
		}
		time.Sleep(compactPollInterval)
		shown = compactThinkingFooter(herdr.AgentRead(env, agent, "recent", intPtr(compactScreenLines)))
		if shown == wanted {
			return shown
		}
	}
	return shown
}

// compactThinkingFooter returns the thinking value the last footer line of
// the screen (the last line containing a bullet) ends with, or "" when that
// line does not end with a level.
func compactThinkingFooter(screen string) string {
	lines := strings.Split(strings.ReplaceAll(screen, "\r\n", "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if !strings.Contains(line, "•") {
			continue
		}
		if m := compactThinkingFooterRE.FindStringSubmatch(line); m != nil {
			return m[2]
		}
		return ""
	}
	return ""
}

// openCodeMenuResult reports what the opencode command-menu selection found.
type openCodeMenuResult int

const (
	// openCodeMenuCompact is /compact on the first menu line: Enter runs it.
	openCodeMenuCompact openCodeMenuResult = iota
	// openCodeMenuNothing is a menu without /compact (an empty session):
	// clear the box and report nothing to compact.
	openCodeMenuNothing
	// openCodeMenuMissing is /compact not on the first line, or a menu that
	// never appeared: clear the box and stop.
	openCodeMenuMissing
)

// compactOpenCodeMenu returns the command names of the screen's command-menu
// lines, in order (the menu pops up above the input box when / is typed; the
// input box itself pads with two spaces and does not match).
func compactOpenCodeMenu(screen string) []string {
	var out []string
	for _, line := range compactLines(screen) {
		if m := compactOpenCodeMenuLineRE.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			out = append(out, m[1])
		}
	}
	return out
}

// compactOpenCodeMenuSelection reads the visible screen for up to
// compactOpenCodeMenuWindow after the /compact text was sent and reports which
// top menu item an Enter would run: /compact on top runs it, a menu without
// /compact is an empty session (nothing to compact), and /compact not on top
// (or a menu that never appears) cannot be selected. A recent read of a
// working opencode fails with agent_not_idle and herdr.AgentRead already
// falls back to the visible screen.
func compactOpenCodeMenuSelection(agent string, env platform.Env) openCodeMenuResult {
	deadline := platform.Now().Add(compactOpenCodeMenuWindow)
	for {
		menu := compactOpenCodeMenu(herdr.AgentRead(env, agent, "visible", nil))
		if len(menu) > 0 {
			if menu[0] == "compact" {
				return openCodeMenuCompact
			}
			for _, cmd := range menu {
				if cmd == "compact" {
					return openCodeMenuMissing
				}
			}
			return openCodeMenuNothing
		}
		if !platform.Now().Before(deadline) {
			return openCodeMenuMissing
		}
		time.Sleep(compactOpenCodeMenuPoll)
	}
}
