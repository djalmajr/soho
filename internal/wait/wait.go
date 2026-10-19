package wait

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/collaboration"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/provider"
	"github.com/djalmajr/soho/internal/reportscan"
	"github.com/djalmajr/soho/internal/startup"
	"github.com/djalmajr/soho/internal/taskreport"
)

const enterRetryLimit = 3

var uintPattern = regexp.MustCompile(`^[0-9]+$`)
var frictionLogFile string

func SetFrictionLogFile(file string) { frictionLogFile = file }

func readWaitFile(sd, agent, name string) (string, bool) {
	data, err := platform.ReadTextFile(filepath.Join(sd, "wait", agent+"."+name))
	if err != nil {
		return "", false
	}
	return strings.TrimRight(data, "\n"), true
}

func writeWaitFile(sd, agent, name, value string) {
	if err := os.WriteFile(filepath.Join(sd, "wait", agent+"."+name), []byte(value), 0o666); err != nil {
		panic(err)
	}
}

func removeWaitFile(sd, agent, name string) { _ = os.Remove(filepath.Join(sd, "wait", agent+"."+name)) }

func reportNonEmpty(file string) bool {
	if file == "" {
		return false
	}
	info, err := os.Stat(file)
	return err == nil && info.Size() > 0
}

func CksumField(value string) uint32 {
	data := []byte(value)
	n := uint64(len(data))
	for {
		data = append(data, byte(n))
		n >>= 8
		if n == 0 {
			break
		}
	}
	crc := uint32(0)
	for _, b := range data {
		crc ^= uint32(b) << 24
		for i := 0; i < 8; i++ {
			if crc&0x80000000 != 0 {
				crc = (crc << 1) ^ 0x04c11db7
			} else {
				crc <<= 1
			}
		}
	}
	return ^crc
}

func NormalizeScreen(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	digitRuns := regexp.MustCompile(`[0-9]+`).ReplaceAllString(value, "#")
	progress := regexp.MustCompile(`[\x{2800}-\x{28ff}\x{25d0}-\x{25d3}\x{2588}\x{258c}\x{2590}\x{2591}]`)
	return progress.ReplaceAllString(digitRuns, "*")
}

// ActivityAgeSeconds reports the age, in seconds, of the last observed
// visible-screen change and whether the visible screen changed since the
// last probe's recorded hash (stuck-hash). The age is nil when nothing
// proves it: no recorded hash, a failed (empty) screen read, a recorded
// empty-screen hash, or a missing or invalid activity-at. A different
// screen only proves movement since the last probe, never an age: it
// returns a nil age with changed true.
func ActivityAgeSeconds(sd, agent, screen string, now int64) (*int64, bool) {
	hashRaw, ok := readWaitFile(sd, agent, "stuck-hash")
	if !ok || strings.TrimSpace(hashRaw) == "" || strings.TrimSpace(screen) == "" || strings.TrimSpace(hashRaw) == strconv.FormatUint(uint64(CksumField(NormalizeScreen(""))), 10) {
		return nil, false
	}
	if strconv.FormatUint(uint64(CksumField(NormalizeScreen(screen))), 10) != strings.TrimSpace(hashRaw) {
		return nil, true
	}
	at, valid := positiveWaitInt(sd, agent, "activity-at")
	if !valid {
		return nil, false
	}
	age := now - at
	return &age, false
}

// activityChangeCeiling is how long ago, at most, a screen that changed since
// the last probe changed: the time since that probe (probe-at, else
// stuck-since). It decides a checkpoint; it is never published as an age.
func activityChangeCeiling(sd, agent string, now int64) *int64 {
	since, valid := positiveWaitInt(sd, agent, "probe-at")
	if !valid {
		since, valid = positiveWaitInt(sd, agent, "stuck-since")
	}
	if !valid {
		return nil
	}
	ceiling := now - since
	return &ceiling
}

func positiveWaitInt(sd, agent, name string) (int64, bool) {
	raw, ok := readWaitFile(sd, agent, name)
	if !ok {
		return 0, false
	}
	raw = strings.TrimSpace(raw)
	if !uintPattern.MatchString(raw) {
		return 0, false
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	return n, err == nil && n > 0
}

func NormalizeApproveScreen(value string) string { return NormalizeScreen(value) }

func WaitRank(code int) int {
	switch code {
	case 4:
		return 6
	case 11:
		return 5
	case 14:
		return 4
	case 15:
		return 3
	case 7:
		return 2
	case 6:
		return 1
	default:
		return 0
	}
}

func PollIntervalMs(env platform.Env) int {
	raw := env.Get("SOHO_WAIT_POLL_MS")
	if !uintPattern.MatchString(raw) {
		return 3000
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > 3000 {
		return 3000
	}
	return n
}

func markerEpoch(marker string, fallback int64) int64 {
	parts := strings.Fields(marker)
	if len(parts) == 0 || !uintPattern.MatchString(parts[0]) {
		return fallback
	}
	n, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return fallback
	}
	return n
}

func promptWindow(ctx *core.Config, env platform.Env) int64 {
	raw := core.Cfg(ctx, "prompt_check_seconds", "15", env)
	if !uintPattern.MatchString(raw) {
		return 0
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

func sendWarning(message string) { core.Warn(message, frictionLogFile, "wait") }

func TryAutoApprove(sd, agent string, ctx *core.Config, env platform.Env) bool {
	if core.Cfg(ctx, "auto_approve", "off", env) != "on" {
		return false
	}
	maxRaw := core.Cfg(ctx, "max_auto_approvals", "20", env)
	max, err := strconv.Atoi(maxRaw)
	if err != nil {
		max = 0
	}
	n := 0
	if raw, ok := readWaitFile(sd, agent, "approvals"); ok {
		trimmed := strings.TrimSpace(raw)
		if !uintPattern.MatchString(trimmed) {
			n = math.MaxInt32
		} else if parsed, e := strconv.Atoi(trimmed); e == nil {
			n = parsed
		} else {
			n = math.MaxInt32
		}
	}
	if n >= max {
		sendWarning(fmt.Sprintf("auto_approve: %s reached max_auto_approvals=%s; leaving it blocked", agent, maxRaw))
		return false
	}
	line := strings.Split(core.RosterLine(sd, agent), "\t")
	kind := ""
	if len(line) > 2 {
		kind = line[2]
	}
	key := "enter"
	if kind == "codex" {
		key = "y"
	}
	if !herdr.AgentSendKeys(agent, key, env) {
		return false
	}
	next := n + 1
	writeWaitFile(sd, agent, "approvals", fmt.Sprintf("%d\n", next))
	log, err := os.OpenFile(filepath.Join(sd, "wait", agent+".approvals.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o666)
	if err == nil {
		_, _ = fmt.Fprintf(log, "%s auto-approved dialog #%d\n", core.NowStamp(platform.Now()), next)
		_ = log.Close()
	}
	sendWarning(fmt.Sprintf("auto_approve: answered dialog #%d for '%s' with its default option", next, agent))
	removeWaitFile(sd, agent, "blocked")
	return true
}

func retryNotReceived(sd, agent, marker string, ctx *core.Config, env platform.Env, st herdr.AgentStateResult) string {
	window := promptWindow(ctx, env)
	lastEpoch := markerEpoch(marker, 0)
	attempts := 0
	if retryRaw, ok := readWaitFile(sd, agent, "enter-retry"); ok {
		parts := strings.Fields(strings.TrimSpace(retryRaw))
		if len(parts) != 2 || !uintPattern.MatchString(parts[0]) || !uintPattern.MatchString(parts[1]) {
			return "not-received"
		}
		attempts, _ = strconv.Atoi(parts[0])
		lastEpoch, _ = strconv.ParseInt(parts[1], 10, 64)
	}
	now := platform.Now().Unix()
	screen := herdr.AgentRead(env, agent, "recent-unwrapped", intPtr(40))
	// The retry Enter needs the same composer proof as the dispatch: a
	// recognized composer (the roster kind) holding the stored composed
	// path, and no known trust/approval/question UI. A missing
	// identity/path or an unrecognized screen keeps the delivery uncertain
	// without keys — the marker stays, and only positive evidence settles
	// it.
	inInput := inputRegionHolds(rosterKind(sd, agent), screen, QueuedPromptPath(marker, sd, agent))
	if !inInput {
		return "not-received"
	}
	if now-lastEpoch < window {
		return "working"
	}
	if attempts < enterRetryLimit && provider.DialogUI(rosterKind(sd, agent), screen) == "" {
		next := attempts + 1
		writeWaitFile(sd, agent, "enter-retry", fmt.Sprintf("%d %d\n", next, now))
		if herdr.AgentSendKeys(agent, "enter", env) {
			sendWarning(fmt.Sprintf("prompt to '%s' was still in its input box; sent Enter again (%d of %d)", agent, next, enterRetryLimit))
			return "working"
		}
		// A failed transport call still consumes the bounded attempt and
		// starts its retry interval; it does not prove that a key landed.
		sendWarning(fmt.Sprintf("prompt to '%s' was still in its input box; Enter retry %d of %d did not send", agent, next, enterRetryLimit))
		return "working"
	}
	return "not-received"
}

func intPtr(value int) *int { return &value }

// inputRegionHolds reports whether the screen authorizes a retry Enter for
// the stored composed path: a recognized composer (the roster kind) holding
// the exact path, and no known trust/approval/question UI. A missing
// identity/path, an unknown kind, or an unrecognized screen is not positive
// proof — the delivery keeps uncertain without keys.
func inputRegionHolds(kind, screen, composed string) bool {
	if composed == "" || provider.DialogUI(kind, screen) != "" {
		return false
	}
	region, recognized := provider.ComposerRegion(kind, screen)
	return recognized && provider.ComposerHoldsPath(region, composed)
}

// rosterKind reports the agent's provider kind from the roster (field 2),
// or "" when the roster does not say.
func rosterKind(sd, agent string) string {
	return field(strings.Split(core.RosterLine(sd, agent), "\t"), 2)
}

func ProbeAgent(sd, agent, report string, ctx *core.Config, env platform.Env) string {
	grace, _ := strconv.ParseFloat(core.Cfg(ctx, "settled_grace", "45", env), 64)
	if reportNonEmpty(report) {
		removeWaitFile(sd, agent, "queued")
		removeWaitFile(sd, agent, "not-received")
		removeWaitFile(sd, agent, "enter-retry")
		info, _ := os.Stat(report)
		size := strconv.FormatInt(info.Size(), 10)
		prev, ok := readWaitFile(sd, agent, "size")
		writeWaitFile(sd, agent, "size", wcSize(info.Size())+"\n")
		if ok && strings.TrimSpace(prev) == size {
			return "done"
		}
		return "pending"
	}
	st := herdr.AgentState(agent, env, herdr.Timeout, nil)
	nrFile := filepath.Join(sd, "wait", agent+".not-received")
	queuedFile := filepath.Join(sd, "wait", agent+".queued")
	retryFile := filepath.Join(sd, "wait", agent+".enter-retry")
	if st.State == "gone" {
		_ = os.Remove(queuedFile)
		_ = os.Remove(retryFile)
		return "gone"
	}
	if st.State == "unavailable" {
		return "unavailable\t" + st.Cause
	}
	// The cursor/Windows startup guard (issue #59): the verified workspace
	// trust dialog takes no approval key — not the auto-approve default and
	// not the not-received Enter retry — whatever herdr classifies the
	// state (native idle or blocked). It sits after the authoritative
	// finished-report early return and the gone/unavailable handling, and
	// before the not-received Enter retry and the blocked-branch auto
	// approve; it returns the existing blocked result (the wait prints the
	// dialog and exits 7). A launch-only or blank screen does not block the
	// wait: the guard acts only on the active dialog, so no TUI banner is
	// demanded and ordinary in-progress output is never inferred away.
	probeKind := field(strings.Split(core.RosterLine(sd, agent), "\t"), 2)
	if startup.Enabled(probeKind) {
		cursorScreen, cursorScreenOK := herdr.AgentReadOK(env, agent, "visible", intPtr(40))
		if ev := startup.Evaluate(probeKind, startup.CurrentPlatform(), st.State, cursorScreen, cursorScreenOK); ev.Verdict == startup.VerdictBlocked || ev.Reason == startup.ReasonScreenIncomplete {
			sendWarning(fmt.Sprintf("agent '%s' is on the cursor workspace trust dialog; no approval key is sent — read the pane and answer it yourself (herdr agent send-keys %s <keys>)", agent, agent))
			return "blocked"
		}
	}
	queuedText, queuedExists := readWaitFile(sd, agent, "queued")
	queuedPending, queuedTerminal := false, false
	if queuedExists {
		if st.State == "working" {
			if MarkerSeqChanged(queuedText, st.Seq) {
				_ = os.Remove(queuedFile)
				_ = os.Remove(retryFile)
				queuedExists = false
			}
		} else if st.State == "blocked" {
			queuedTerminal = true
		} else {
			queuedPending = true
		}
	}
	if !queuedPending {
		if mark, ok := readWaitFile(sd, agent, "not-received"); ok {
			if st.State == "working" || st.State == "blocked" || MarkerSeqChanged(mark, st.Seq) {
				_ = os.Remove(nrFile)
				_ = os.Remove(retryFile)
			} else {
				return retryNotReceived(sd, agent, mark, ctx, env, st)
			}
		}
	}
	if st.State == "blocked" {
		_ = os.Remove(retryFile)
		clearQueued := func() {
			if queuedTerminal {
				_ = os.Remove(queuedFile)
				_ = os.Remove(retryFile)
			}
		}
		if _, ok := readWaitFile(sd, agent, "blocked"); ok {
			line := strings.Split(core.RosterLine(sd, agent), "\t")
			kind := ""
			if len(line) > 2 {
				kind = line[2]
			}
			visible := herdr.AgentRead(env, agent, "visible", intPtr(40))
			if provider.DialogKind(kind, visible) == "question" {
				writeWaitFile(sd, agent, "question", provider.QuestionText(visible)+"\n")
				clearQueued()
				return "question"
			}
			h := strconv.FormatUint(uint64(CksumField(NormalizeApproveScreen(visible))), 10)
			count := 1
			if raw, ok := readWaitFile(sd, agent, "approve-screen"); ok {
				parts := strings.Split(raw, "\t")
				if len(parts) > 1 && parts[1] == h {
					if n, e := strconv.Atoi(parts[0]); e == nil && n > 0 {
						count = n + 1
					}
				}
			}
			if count >= 3 {
				sendWarning(fmt.Sprintf("auto_approve: the same dialog came back 3 times for '%s'; leaving it blocked", agent))
				clearQueued()
				return "blocked"
			}
			if !TryAutoApprove(sd, agent, ctx, env) {
				clearQueued()
				return "blocked"
			}
			writeWaitFile(sd, agent, "approve-screen", fmt.Sprintf("%d\t%s\n", count, h))
			clearQueued()
			return "working"
		}
		writeWaitFile(sd, agent, "blocked", "")
		return "working"
	}
	removeWaitFile(sd, agent, "blocked")
	pfile := filepath.Join(sd, "wait", agent+".provider")
	clearProvider := func() { _ = os.Remove(pfile); removeWaitFile(sd, agent, "capacity-at") }
	if st.State == "working" {
		clearProvider()
	}
	var screen string
	screenRead := false
	visible := func() string {
		if !screenRead {
			screen = herdr.AgentRead(env, agent, "visible", nil)
			screenRead = true
		}
		return screen
	}
	if st.State == "working" {
		text := visible()
		hash := strconv.FormatUint(uint64(CksumField(NormalizeScreen(text))), 10)
		now := platform.Now().Unix()
		prev, hasPrev := readWaitFile(sd, agent, "stuck-hash")
		if !hasPrev || prev != hash {
			prevProbe, ok := positiveWaitInt(sd, agent, "probe-at")
			if !ok {
				prevProbe, ok = positiveWaitInt(sd, agent, "stuck-since")
			}
			if hasPrev && prev != "" && prev != strconv.FormatUint(uint64(CksumField(NormalizeScreen(""))), 10) && strings.TrimSpace(text) != "" && ok {
				writeWaitFile(sd, agent, "activity-at", fmt.Sprintf("%d\n", prevProbe))
			}
			writeWaitFile(sd, agent, "stuck-hash", hash+"\n")
			writeWaitFile(sd, agent, "stuck-since", fmt.Sprintf("%d\n", now))
			removeWaitFile(sd, agent, "stuck-warned")
		} else if limit, e := strconv.ParseFloat(core.Cfg(ctx, "stuck_warn_minutes", "20", env), 64); e == nil && limit > 0 {
			if _, warned := readWaitFile(sd, agent, "stuck-warned"); !warned {
				raw, ok := readWaitFile(sd, agent, "stuck-since")
				since, e := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
				if !ok || e != nil || since <= 0 {
					writeWaitFile(sd, agent, "stuck-since", fmt.Sprintf("%d\n", now))
				} else if float64(now-since) >= limit*60 {
					mins := (now - since) / 60
					sendWarning(fmt.Sprintf("agent '%s' has shown the same screen (apart from counters) for %d min while working; it may be stuck in one tool call. Inspect: herdr agent read %s --source recent-unwrapped --lines 60", agent, mins, agent))
					writeWaitFile(sd, agent, "stuck-warned", "")
				}
			}
		}
		if strings.TrimSpace(text) != "" {
			writeWaitFile(sd, agent, "probe-at", fmt.Sprintf("%d\n", now))
		}
	}
	if st.State != "working" {
		if quota := provider.QuotaDetect(st.State, herdr.AgentRead(env, agent, "visible", intPtr(20))); quota != nil {
			_ = os.Remove(queuedFile)
			_ = os.Remove(retryFile)
			content := quota[0] + "\n"
			if quota[1] != "" {
				content += quota[1] + "\n"
			}
			writeWaitFile(sd, agent, "quota", content)
			clearProvider()
			return "quota"
		}
		ptext := herdr.AgentRead(env, agent, "recent-unwrapped", intPtr(40))
		p := provider.ProviderDetectTexts(st.State, ptext, core.Cfg(ctx, "provider_capacity_texts", "", env), core.Cfg(ctx, "provider_error_texts", "", env))
		if p != nil {
			if p.Status == "provider-error" && p.Auth {
				_ = os.Remove(queuedFile)
				_ = os.Remove(retryFile)
				clearProvider()
				writeWaitFile(sd, agent, "provider-cause", p.Cause+"\n")
				return "provider-error"
			}
			target := fmt.Sprintf("%d\n%s", CksumField(ptext), p.Status)
			prev, _ := readWaitFile(sd, agent, "provider")
			if prev != target {
				_ = os.Remove(pfile)
				writeWaitFile(sd, agent, "provider", target+"\n")
				return "working"
			}
			writeWaitFile(sd, agent, "provider-cause", p.Cause+"\n")
			if p.Status == "provider-error" {
				_ = os.Remove(queuedFile)
				_ = os.Remove(retryFile)
				return "provider-error"
			}
			used := 0
			if raw, ok := readWaitFile(sd, agent, "capacity-retries"); ok {
				if !uintPattern.MatchString(strings.TrimSpace(raw)) {
					_ = os.Remove(queuedFile)
					_ = os.Remove(retryFile)
					return "capacity"
				}
				used, _ = strconv.Atoi(strings.TrimSpace(raw))
			}
			limit, e := strconv.Atoi(core.Cfg(ctx, "provider_retries", "3", env))
			if e != nil || used >= limit {
				_ = os.Remove(queuedFile)
				_ = os.Remove(retryFile)
				return "capacity"
			}
			at, ok := readWaitFile(sd, agent, "capacity-at")
			if !ok {
				writeWaitFile(sd, agent, "capacity-at", fmt.Sprintf("%d\n", platform.Now().Unix()))
				return "working"
			}
			delay, e := strconv.ParseInt(core.Cfg(ctx, "provider_retry_delay", "60", env), 10, 64)
			last, e2 := strconv.ParseInt(strings.TrimSpace(at), 10, 64)
			now := platform.Now().Unix()
			if e != nil || e2 != nil || now-last < delay {
				return "working"
			}
			report := core.LastReport(sd, agent)
			prompt := fmt.Sprintf("The model provider was at capacity and your last request failed. Continue the task from where you stopped; do not redo finished steps. When finished, write your report to %s and reply with only that path.", report)
			sent := herdr.AgentPrompt(agent, prompt, env)
			if !sent.Ok {
				sendWarning("provider capacity: failed to send the continue to '" + agent + "': " + sent.Raw + "; acting as exhausted")
				_ = os.Remove(queuedFile)
				_ = os.Remove(retryFile)
				return "capacity"
			}
			writeWaitFile(sd, agent, "capacity-retries", fmt.Sprintf("%d\n", used+1))
			removeWaitFile(sd, agent, "capacity-at")
			_ = os.Remove(pfile)
			sendWarning(fmt.Sprintf("provider capacity: sent continue #%d of %d to '%s': %s", used+1, limit, agent, p.Cause))
			_ = os.Remove(queuedFile)
			_ = os.Remove(retryFile)
			return "working"
		}
		clearProvider()
	}
	if queuedPending {
		recent := herdr.AgentRead(env, agent, "recent-unwrapped", intPtr(40))
		promptPath := QueuedPromptPath(queuedText, sd, agent)
		// The queued retry Enter needs the same composer proof as the
		// dispatch: a recognized composer holding the stored path, and no
		// known dialog UI. Unrecognized screens keep the delivery uncertain
		// without keys.
		inInput := inputRegionHolds(rosterKind(sd, agent), recent, promptPath)
		if promptPath == "" {
			promptPath = "-"
		}
		seq := jsString(st.Seq)
		if seq == "" {
			seq = "-"
		}
		if !inInput {
			writeWaitFile(sd, agent, "not-received", fmt.Sprintf("%d %s %s\n", platform.Now().Unix(), seq, promptPath))
			_ = os.Remove(queuedFile)
			_ = os.Remove(retryFile)
			return "not-received"
		}
		epoch := markerEpoch(queuedText, platform.Now().Unix())
		writeWaitFile(sd, agent, "not-received", fmt.Sprintf("%d %s %s\n", epoch, seq, promptPath))
		_ = os.Remove(queuedFile)
		now := platform.Now().Unix()
		attempts := 0
		last := epoch
		if raw, ok := readWaitFile(sd, agent, "enter-retry"); ok {
			parts := strings.Fields(strings.TrimSpace(raw))
			if len(parts) != 2 || !uintPattern.MatchString(parts[0]) || !uintPattern.MatchString(parts[1]) {
				return "not-received"
			}
			attempts, _ = strconv.Atoi(parts[0])
			last, _ = strconv.ParseInt(parts[1], 10, 64)
		}
		if now-last < promptWindow(ctx, env) {
			return "working"
		}
		if attempts < enterRetryLimit && provider.DialogUI(rosterKind(sd, agent), recent) == "" {
			next := attempts + 1
			writeWaitFile(sd, agent, "enter-retry", fmt.Sprintf("%d %d\n", next, now))
			if herdr.AgentSendKeys(agent, "enter", env) {
				sendWarning(fmt.Sprintf("prompt to '%s' was still in its input box; sent Enter again (%d of %d)", agent, next, enterRetryLimit))
				return "working"
			}
			// Failed calls consume the attempt and its interval too, without
			// claiming that the key reached the agent.
			sendWarning(fmt.Sprintf("prompt to '%s' was still in its input box; Enter retry %d of %d did not send", agent, next, enterRetryLimit))
			return "working"
		}
		return "not-received"
	}
	hash := strconv.FormatUint(uint64(CksumField(visible())), 10)
	last, ok := readWaitFile(sd, agent, "screen")
	now := platform.Now().Unix()
	if st.State == "working" || !ok || hash != last {
		writeWaitFile(sd, agent, "screen", hash+"\n")
		writeWaitFile(sd, agent, "since", fmt.Sprintf("%d\n", now))
		return "working"
	}
	sinceRaw, ok := readWaitFile(sd, agent, "since")
	since, e := strconv.ParseFloat(strings.TrimSpace(sinceRaw), 64)
	if !ok || e != nil {
		since = float64(now)
	}
	if float64(now)-since >= grace {
		// A still screen that counts a background shell or Monitor is an
		// agent that resumes on its own when the task ends: keep waiting
		// for its report instead of settling.
		if snippet := backgroundTaskMatch(rosterKind(sd, agent), visible(), ctx, env); snippet != "" {
			if _, noted := readWaitFile(sd, agent, "background-noted"); !noted {
				writeWaitFile(sd, agent, "background-noted", "")
				sendWarning(fmt.Sprintf("'%s' is idle with a background task still running (\"%s\"); waiting for its report", agent, snippet))
			}
			return "working"
		}
		return "settled"
	}
	return "working"
}

// backgroundTaskLines is how many of the last non-empty lines of the visible
// screen background_patterns.<kind> is matched against.
const backgroundTaskLines = 15

// backgroundTaskSnippetMax caps the matched text quoted in the notice.
const backgroundTaskSnippetMax = 80

// backgroundTaskMatch reports the text of the kind's background_patterns
// match in the last lines of the screen, or "" when the kind has no pattern,
// the pattern does not compile, or no line matches.
func backgroundTaskMatch(kind, screen string, ctx *core.Config, env platform.Env) string {
	if kind == "" {
		return ""
	}
	pattern := core.Cfg(ctx, core.BackgroundPatternKey(kind), "", env)
	if pattern == "" {
		return ""
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return ""
	}
	for _, line := range LastNonEmptyLines(screen, backgroundTaskLines) {
		line = strings.TrimSpace(line)
		if loc := re.FindStringIndex(line); loc != nil {
			snippet := []rune(line[loc[0]:loc[1]])
			if len(snippet) > backgroundTaskSnippetMax {
				snippet = snippet[:backgroundTaskSnippetMax]
			}
			return string(snippet)
		}
	}
	return ""
}

func wcSize(size int64) string {
	s := strconv.FormatInt(size, 10)
	w := 8
	if len(s) == 9 {
		w = 10
	} else if len(s) > 9 {
		w = len(s)
	}
	return strings.Repeat(" ", max(0, w-len(s))) + s
}

func jsonLine(value *jsonjs.Object) {
	_, _ = platform.Stdout.Write([]byte(jsonjs.Stringify(value) + "\n"))
}

func WaitFor(agents []string, sd string, ctx *core.Config, env platform.Env, timeoutMs float64, anyDone bool, cwd string) int {
	for _, agent := range agents {
		removeWaitFile(sd, agent, "size")
		removeWaitFile(sd, agent, "background-noted")
	}
	started := platform.Now()
	deadline := started.Add(time.Duration(timeoutMs * float64(time.Millisecond)))
	remaining := append([]string(nil), agents...)
	last := map[string]string{}
	rc := 0
	workspaceEnv := derivedWorkspaceEnv(ctx, env, cwd)
	// The phase last announced per agent, so a phase change is reported once
	// and a stable phase is not repeated on every poll.
	seenCollab := map[string]string{}
	for {
		pending := []string{}
		for _, agent := range remaining {
			report := core.LastReport(sd, agent)
			// An active collaboration owns the pane: the stale report is an
			// artifact only, and the probe is skipped — no retry, no provider
			// resume, no keys or prompts sent while the pane is idle.
			if cs, ok := readCollaboration(sd, field(strings.Split(core.RosterLine(sd, agent), "\t"), 1), ctx, workspaceEnv); ok {
				switch {
				case cs.state == "collaboration-unavailable" || cs.state == "collaboration-disabled":
					// The active assignment cannot be verified, or the policy is
					// off or unreadable: terminal, like unavailable — a stale
					// report never becomes done.
					printCollaborationStatus(agent, cs, report)
					if WaitRank(cs.code) > WaitRank(rc) {
						rc = cs.code
					}
					continue
				case cs.assign.Phase == collaboration.AwaitingOrchestrator || cs.assign.Phase == collaboration.Escalated:
					// Actionable: the orchestrator has to decide; the wait ends
					// here with 10, not with the timeout.
					printCollaborationStatus(agent, cs, report)
					return 10
				default:
					// preparing, reviewing and fixing stay pending under the
					// existing timeout; each phase change is reported once.
					if seenCollab[agent] != cs.assign.Phase {
						printCollaborationStatus(agent, cs, report)
						seenCollab[agent] = cs.assign.Phase
					}
					last[agent] = "collaborating"
					pending = append(pending, agent)
				}
				continue
			}
			state := ProbeAgent(sd, agent, report, ctx, env)
			tag := strings.SplitN(state, "\t", 2)[0]
			switch tag {
			case "done":
				text, _ := platform.ReadTextFile(report)
				partial := reportscan.PartialCount(text)
				header := reportscan.ReviewHeader(text)
				line := jsonjs.O("agent", agent, "status", "done", "report", report)
				pointer := taskreport.ReadTaskReportPointer(sd, agent)
				if pointer != nil {
					current, _ := pointer.Get("current")
					stable, _ := pointer.Get("task_report")
					if current == report {
						synced, e := taskreport.SyncTaskReport(sd, agent)
						if e == nil && synced != "" {
							line.Set("task_report", stable)
						}
					}
				}
				if header != nil {
					line.Set("verdict", header.Verdict)
					line.Set("findings", header.Findings)
					line.Set("severity", jsonjs.O("P0", header.Severity["P0"], "P1", header.Severity["P1"], "P2", header.Severity["P2"], "P3", header.Severity["P3"]))
				}
				if partial > 0 {
					line.Set("partial", partial)
				}
				if header != nil || partial > 0 {
					effective := "pass"
					if partial > 0 || header.Verdict == "fail" || header.Severity["P0"]+header.Severity["P1"]+header.Severity["P2"] > 0 {
						effective = "fail"
					}
					line.Set("verdict_effective", effective)
				}
				jsonLine(line)
				if partial > 0 {
					prev, _ := readWaitFile(sd, agent, "partial-warned")
					if prev != report {
						sendWarning(fmt.Sprintf("report of '%s' marks %d item(s) partial: a partial item is not a pass; read them before commit, push or release", agent, partial))
						writeWaitFile(sd, agent, "partial-warned", report+"\n")
					}
				}
				recordMetrics(sd, agent, report, text, header, partial, ctx, env)
				if header != nil && header.Verdict == "pass" {
					open := header.Severity["P0"] + header.Severity["P1"] + header.Severity["P2"]
					if open > 0 {
						sendWarning(fmt.Sprintf("report of '%s' says verdict pass with %s open P0-P2 finding(s): read them before commit, push or release", agent, numberString(open)))
					}
				}
				if header != nil {
					sum := header.Severity["P0"] + header.Severity["P1"] + header.Severity["P2"] + header.Severity["P3"]
					if sum != header.Findings {
						sendWarning(fmt.Sprintf("report of '%s': findings %s but P0..P3 add up to %s", agent, numberString(header.Findings), numberString(sum)))
					}
				} else {
					role := field(strings.Split(core.RosterLine(sd, agent), "\t"), 3)
					if core.IsReviewRole(role) {
						sendWarning(fmt.Sprintf("report of '%s' has no 'findings: N (P0 a, P1 b, P2 c, P3 d) | verdict: pass|fail' first line", agent))
					}
				}
				if core.Cfg(ctx, "notify", "off", env) == "on" {
					herdr.NotificationShow("soho: "+agent+" finished", report, env)
				}
				_ = core.MarkTaskDone(sd, agent, env)
				mirrorReport(sd, agent, report, ctx, env, cwd)
				if anyDone {
					return 0
				}
			case "blocked":
				dialog := lastNonEmpty(herdr.AgentRead(env, agent, "visible", nil), 20)
				jsonLine(jsonjs.O("agent", agent, "status", "blocked", "report", report, "dialog", dialog))
				if WaitRank(7) > WaitRank(rc) {
					rc = 7
				}
			case "question":
				q, _ := readWaitFile(sd, agent, "question")
				jsonLine(jsonjs.O("agent", agent, "status", "question", "report", report, "question", q))
				sendWarning("agent '" + agent + "' asked a question; nobody answers it automatically. Ask the user, then answer with herdr agent send-keys/prompt, or release the worker.")
				if WaitRank(7) > WaitRank(rc) {
					rc = 7
				}
			case "gone":
				jsonLine(jsonjs.O("agent", agent, "status", "gone", "report", report))
				if WaitRank(6) > WaitRank(rc) {
					rc = 6
				}
			case "settled":
				jsonLine(jsonjs.O("agent", agent, "status", "settled-no-report", "report", report))
				if WaitRank(6) > WaitRank(rc) {
					rc = 6
				}
			case "unavailable":
				cause := strings.TrimPrefix(state, "unavailable\t")
				jsonLine(jsonjs.O("agent", agent, "status", "unavailable", "report", report, "error", cause))
				sendWarning(fmt.Sprintf("agent '%s': herdr agent get failed: %s", agent, cause))
				if WaitRank(4) > WaitRank(rc) {
					rc = 4
				}
			case "quota":
				match, renewal := readQuotaFile(sd, agent)
				fields := strings.Split(core.RosterLine(sd, agent), "\t")
				kind, model, lane, role := field(fields, 2), field(fields, 8), field(fields, 11), field(fields, 3)
				if lane == "" {
					lane = core.LaneOfRole(ctx, role, env)
				}
				jsonLine(jsonjs.O("agent", agent, "status", "quota", "report", report, "lane", lane, "kind", kind, "model", model, "match", match, "renewal", renewal))
				sendWarning(fmt.Sprintf("quota: agent '%s' lane=%s kind=%s model=%s : %s%s", agent, showOr(lane, "?"), kind, showOr(model, "?"), match, renewalSuffix(renewal)))
				if WaitRank(11) > WaitRank(rc) {
					rc = 11
				}
			case "not-received":
				jsonLine(jsonjs.O("agent", agent, "status", "not-received", "report", report))
				sendWarning(fmt.Sprintf("prompt to '%s' never reached it: read the pane (herdr agent read %s --source visible), then dispatch again", agent, agent))
				if WaitRank(15) > WaitRank(rc) {
					rc = 15
				}
			case "provider-error", "capacity":
				cause, _ := readWaitFile(sd, agent, "provider-cause")
				fields := strings.Split(core.RosterLine(sd, agent), "\t")
				kind, model, lane, role := field(fields, 2), field(fields, 8), field(fields, 11), field(fields, 3)
				if lane == "" {
					lane = core.LaneOfRole(ctx, role, env)
				}
				if tag == "provider-error" {
					jsonLine(jsonjs.O("agent", agent, "status", tag, "report", report, "lane", lane, "kind", kind, "model", model, "cause", cause))
					sendWarning(fmt.Sprintf("provider error: agent '%s' lane=%s kind=%s model=%s : %s", agent, showOr(lane, "?"), kind, showOr(model, "?"), cause))
				} else {
					retriesRaw, _ := readWaitFile(sd, agent, "capacity-retries")
					retries, _ := strconv.Atoi(strings.TrimSpace(retriesRaw))
					jsonLine(jsonjs.O("agent", agent, "status", tag, "report", report, "lane", lane, "kind", kind, "model", model, "cause", cause, "retries", retries))
					sendWarning(fmt.Sprintf("provider capacity: agent '%s' lane=%s kind=%s model=%s : %s", agent, showOr(lane, "?"), kind, showOr(model, "?"), cause))
				}
				if WaitRank(14) > WaitRank(rc) {
					rc = 14
				}
			default:
				last[agent] = tag
				pending = append(pending, agent)
			}
		}
		remaining = pending
		if len(remaining) == 0 {
			return rc
		}
		if !platform.Now().Before(deadline) {
			now := platform.Now().Unix()
			win, _ := strconv.ParseFloat(core.Cfg(ctx, "stuck_warn_minutes", "20", env), 64)
			if win <= 0 {
				win = 20
			}
			for _, agent := range remaining {
				tag := last[agent]
				if tag == "" {
					tag = "working"
				}
				screen := herdr.AgentRead(env, agent, "visible", nil)
				age, changed := ActivityAgeSeconds(sd, agent, screen, now)
				active := tag == "working" && age != nil && float64(*age) < win*60
				// A screen that changed since the last probe proves movement within
				// that interval: it is a checkpoint when the interval is inside the
				// stuck window, with no age published.
				var ceiling *int64
				if changed {
					ceiling = activityChangeCeiling(sd, agent, now)
					active = tag == "working" && ceiling != nil && float64(*ceiling) < win*60
				}
				var ageValue any
				if age != nil {
					ageValue = *age
				}
				line := jsonjs.O("agent", agent, "status", "timeout", "elapsed_ms", platform.Now().Sub(started).Milliseconds(), "state", tag, "checkpoint", active, "activity_age_s", ageValue)
				if changed {
					line.Set("activity_changed", true)
				}
				if tag == "working" {
					// A retry is not a stop: the state stays working and the
					// exit code 9 is unchanged; only the line names the retry.
					if retryLine := provider.RetryLine(tag, screen); retryLine != "" {
						line.Set("retrying", retryLine)
					}
				}
				jsonLine(line)
				if active {
					suffix := ""
					if !math.IsNaN(timeoutMs) && !math.IsInf(timeoutMs, 0) {
						suffix = fmt.Sprintf(" --timeout %s", core.FormatTimeoutMS(int64(timeoutMs)))
					}
					if ceiling != nil {
						_, _ = fmt.Fprintf(platform.Stderr, "soho: checkpoint: '%s' is still working (screen changed within the last %ds); wait again: soho wait %s%s\n", agent, *ceiling, agent, suffix)
					} else {
						_, _ = fmt.Fprintf(platform.Stderr, "soho: checkpoint: '%s' is still working (screen changed %ds ago); wait again: soho wait %s%s\n", agent, *age, agent, suffix)
					}
				} else if !math.IsNaN(timeoutMs) && !math.IsInf(timeoutMs, 0) {
					sendWarning(fmt.Sprintf("timeout waiting for '%s'; it may still be working (state: %s). Run: soho wait %s --timeout %s", agent, tag, agent, core.FormatTimeoutMS(int64(timeoutMs*2))))
				} else {
					sendWarning(fmt.Sprintf("timeout waiting for '%s'; it may still be working (state: %s)", agent, tag))
				}
			}
			return 9
		}
		time.Sleep(time.Duration(PollIntervalMs(env)) * time.Millisecond)
	}
}

func field(fields []string, i int) string {
	if i < len(fields) {
		return fields[i]
	}
	return ""
}
func showOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
func renewalSuffix(value string) string {
	if value != "" {
		return "; renewal: " + value
	}
	return ""
}

func numberString(value float64) string { return strconv.FormatFloat(value, 'f', -1, 64) }
func lastNonEmpty(screen string, n int) string {
	lines := LastNonEmptyLines(screen, n)
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}
	return strings.Join(lines, "\n")
}
func readQuotaFile(sd, agent string) (string, string) {
	raw, ok := readWaitFile(sd, agent, "quota")
	if !ok {
		return "", ""
	}
	parts := strings.Split(raw, "\n")
	return field(parts, 0), field(parts, 1)
}

func mirrorReport(sd, agent, report string, ctx *core.Config, env platform.Env, cwd string) {
	tmp := env.Get("TMPDIR")
	if tmp == "" {
		tmp = os.TempDir()
	}
	dir := filepath.Join(tmp, "soho", core.WorkspaceID(ctx, env, cwd), "reports")
	if !strings.HasPrefix(report, dir+string(os.PathSeparator)) {
		return
	}
	base := filepath.Base(report)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	jobs := [][3]string{{report, filepath.Join(sd, "reports", base), ""}, {filepath.Join(filepath.Dir(report), stem+".brief.md"), filepath.Join(sd, "briefs", stem+".md"), ""}, {filepath.Join(filepath.Dir(report), stem+".dispatch.json"), filepath.Join(sd, "briefs", stem+".dispatch.json"), "optional"}}
	for _, job := range jobs {
		src, dst := job[0], job[1]
		if job[2] != "" {
			if _, err := os.Stat(src); err != nil {
				continue
			}
		}
		data, err := os.ReadFile(src)
		if err != nil {
			sendWarning("mirror: could not copy " + filepath.Base(src) + " of '" + agent + "' to the state dir: " + err.Error())
			continue
		}
		if existing, e := os.ReadFile(dst); e == nil {
			if string(existing) != string(data) {
				sendWarning("kept " + dst + ": it differs from " + src + ", which was not copied over it")
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o777); err == nil {
			err = os.WriteFile(dst, data, 0o666)
		}
		if err != nil {
			sendWarning("mirror: could not copy " + filepath.Base(src) + " of '" + agent + "' to the state dir: " + err.Error())
		}
	}
}

func CmdWait(argv []string, ctx *core.Config, env platform.Env, cwd string) int {
	agents := []string{}
	timeout := float64(math.NaN())
	any := false
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		switch arg {
		case "--timeout":
			if i+1 >= len(argv) {
				core.DieFriction("wait: --timeout expects a value", 2, "", "wait")
			}
			raw := argv[i+1]
			ms, ok := core.ParseTimeoutMS(raw)
			if !ok {
				core.DieFriction("wait: --timeout expects milliseconds or a duration such as 90s, 30m or 2h, got '"+raw+"'", 2, "", "wait")
			}
			timeout = float64(ms)
			// A unit names the intent: only a unitless integer warns.
			if core.IsPlainTimeoutMS(raw) {
				core.WarnShortTimeout("wait", frictionLogFile, ms)
			}
			i++
		case "--any":
			any = true
		default:
			if strings.HasPrefix(arg, "--") {
				core.DieFriction("wait: unknown option "+arg, 2, "", "wait")
			}
			agents = append(agents, arg)
		}
	}
	if len(agents) == 0 {
		core.DieFriction("wait: give at least one agent name", 2, "", "wait")
	}
	sd := core.StateDir(ctx, env, cwd)
	for _, agent := range agents {
		if core.RosterLine(sd, agent) == "" {
			core.DieFriction("agent '"+agent+"' is not in the roster (state dir: "+sd+")", 3, "", "wait")
		}
	}
	if math.IsNaN(timeout) {
		for _, agent := range agents {
			role := field(strings.Split(core.RosterLine(sd, agent), "\t"), 3)
			if ms := core.RoleTimeoutMs(role, ctx, env, cwd); float64(ms) > timeout || math.IsNaN(timeout) {
				timeout = float64(ms)
			}
		}
	}
	// cmdWait turns every DieError out of waitFor into dieFriction, the
	// empty one included: die prints "soho: " and the log gets an
	// error(exit N) line with no text.
	defer func() {
		value := recover()
		if value == nil {
			return
		}
		if exitErr, ok := value.(*platform.ExitError); ok && !exitErr.Friction {
			if exitErr.Msg == "" {
				core.RecordFrictionError("", exitErr.Code, frictionLogFile, "wait")
				_, _ = fmt.Fprint(platform.Stderr, "soho: \n")
			} else {
				exitErr.Friction = true
			}
		}
		panic(value)
	}()
	return WaitFor(agents, sd, ctx, env, timeout, any, cwd)
}
