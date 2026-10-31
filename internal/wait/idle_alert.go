package wait

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/provider"
	"github.com/djalmajr/soho/internal/taskreport"
)

var idleANSI = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07]*(?:\x07|\x1b\\)`)
var idleDurationLine = regexp.MustCompile(`(?i)^(?:[•·]\s*)?(?:[0-9]+(?:\.[0-9]+)?\s*(?:ms|s|m|h)\s*)+$`)
var idleSummarySpan = regexp.MustCompile(`(?i)^(?:[•·]\s*)?(?:worked|cooked|cogitated) for\s+(?:[0-9]+(?:\.[0-9]+)?\s*(?:ms|s|m|h)\s*)+`)
var idleReconnectSpan = regexp.MustCompile(`(?i)^(?:[•·]\s*)?reconnecting(?:…|\.{3})\s*(?:[0-9]+/[0-9]+)?`)
var idleStatusClock = regexp.MustCompile(`(?i)\b[0-9]{1,2}:[0-9]{2}(?::[0-9]{2})?(?:\s*[ap]m)?\b`)

const idleDurationAtom = `[0-9]+(?:\.[0-9]+)?\s*(?:ms|s|m|h)\s*`

var idleElapsedSpan = regexp.MustCompile(`(?i)\(\s*(?:` + idleDurationAtom + `)+(?:[•·]\s*(?:esc to interrupt|↓\s*[0-9.,]+[km]?\s+tokens|thought for\s+(?:` + idleDurationAtom + `)+)\s*)*\)`)
var idleCounter = regexp.MustCompile(`(?i)\bcontext\s+[0-9]+%\s+used|\bctx\s+[0-9]+%|\b[0-9][0-9,.]*[km]?\s+tokens\b|↓\s*[0-9][0-9,.]*[km]?|\b(?:weekly|daily)\s+[0-9]+%\s+left`)
var idleClockLine = regexp.MustCompile(`(?i)^[0-9]{1,2}:[0-9]{2}(?::[0-9]{2})?(?:\s*[ap]m)?$`)
var idleModelFooter = regexp.MustCompile(`^\[[^\]]+\]\s+[█░▒▓▁-▉]+\s+[0-9]+%\s+\[(?:5h|7d):[^\]]*\](?:\s+\[[^\]]*\])*\s*$`)
var idleSpinner = regexp.MustCompile(`^[ \t]*[\x{2800}-\x{28ff}◐◑◒◓✢✳✶✻✽]+[ \t]*`)

// Status patterns apply only to the bottom UI block. History stays verbatim.
func idleFingerprint(screen, kind string) [32]byte {
	screen = strings.ReplaceAll(idleANSI.ReplaceAllString(screen, ""), "\r\n", "\n")
	lines := strings.Split(screen, "\n")
	end := len(lines)
	if start, _, ok := provider.ComposerRegionBounds(kind, screen); ok {
		// Pi and Claude bounds begin inside the box, after its top border.
		if kind != "codex" && start > 0 {
			start--
		}
		end = start
	}
	var queued []string
	for end > 0 {
		line := lines[end-1]
		if idleBottomLine(line) {
			end--
			continue
		}
		// Pi renders queued messages between its elapsed line and input box.
		// Cross them to find the status block, but retain their content verbatim.
		if strings.HasPrefix(strings.TrimSpace(line), "Steering: ") || strings.TrimSpace(line) == "↳ Option+Up to edit all queued messages" {
			queued = append(queued, line)
			end--
			continue
		}
		break
	}
	kept := append([]string(nil), lines[:end]...)
	for i := len(queued) - 1; i >= 0; i-- {
		kept = append(kept, queued[i])
	}
	return sha256.Sum256([]byte(strings.Join(kept, "\n")))
}

var idleWorkingLine = regexp.MustCompile(`(?i)^(?:[•·]\s*)?(?:working|thinking|booping|cooking|cogitating)(?:…|\.{3})?(?:\s*\([^)]*\))?(?:\s*· Running hooks)?$`)
var idleElapsedLine = regexp.MustCompile(`(?i)^Elapsed\s+(?:` + idleDurationAtom + `)+$`)

func idleBottomLine(line string) bool {
	line = strings.TrimSpace(idleSpinner.ReplaceAllString(strings.TrimSpace(line), ""))
	if line == "" || idleDurationLine.MatchString(line) || idleClockLine.MatchString(line) || idleModelFooter.MatchString(line) || idleWorkingLine.MatchString(line) || idleElapsedLine.MatchString(line) {
		return true
	}
	if idleSummarySpan.MatchString(line) || idleReconnectSpan.MatchString(line) {
		// A completed-turn summary is chrome only when no output follows it.
		rest := idleReconnectSpan.ReplaceAllString(idleSummarySpan.ReplaceAllString(line, ""), "")
		rest = idleStatusClock.ReplaceAllString(rest, "")
		rest = strings.TrimSpace(strings.Trim(rest, " •·\t"))
		return rest == "" || strings.HasPrefix(rest, "done ")
	}
	return strings.TrimSpace(idleCounter.ReplaceAllString(idleElapsedSpan.ReplaceAllString(line, ""), "")) == "" || strings.HasPrefix(line, "└ Tip: Use /fork") || strings.HasPrefix(line, "✔ Update installed")
}

type idleAlert struct {
	task, state     string
	lastRead, since time.Time
	fingerprint     [32]byte
	observed        bool
	warned          map[string]bool
	count           int
	hc              bool
	warnedMinutes   int64
}

// takeWarnedMinutes returns the minutes of the latest alert warn (0 when no
// warn fired since the last call) and clears it, so wait writes the
// <agent>.idle-alert marker (DJA-565) exactly once per warn and a later
// poll of the same task does not rewrite it.
func (a *idleAlert) takeWarnedMinutes() int64 {
	m := a.warnedMinutes
	a.warnedMinutes = 0
	return m
}

func idleAlertMinutes(ctx *core.Config, env platform.Env) int64 {
	n, err := strconv.ParseInt(core.Cfg(ctx, "idle_alert_minutes", "20", env), 10, 64)
	if err != nil || n < 0 {
		return 20
	}
	return n
}

func idleTask(sd, agent, report string) string {
	if p := taskreport.ReadTaskReportPointer(sd, agent); p != nil {
		if task, ok := p.Get("task_report"); ok && task != "" {
			return fmt.Sprint(task)
		}
	}
	return report
}

func (a *idleAlert) sampleDue(task, state string, now time.Time, minutes int64) bool {
	if now.Before(a.lastRead) {
		a.lastRead = time.Time{}
		a.observed = false
	}
	if task != a.task || state != a.state || now.Before(a.lastRead) {
		a.task, a.state, a.observed = task, state, false
	}
	return minutes > 0 && state == "working" && (a.lastRead.IsZero() || now.Sub(a.lastRead) >= time.Minute)
}

func (a *idleAlert) observe(screen, agent string, now time.Time, minutes int64, kind string) {
	a.lastRead = now
	if strings.TrimSpace(screen) == "" {
		a.observed = false
		return
	}
	fingerprint := idleFingerprint(screen, kind)
	if !a.observed || fingerprint != a.fingerprint {
		a.since, a.fingerprint, a.observed = now, fingerprint, true
	}
	elapsed := int64(now.Sub(a.since) / time.Minute)
	if elapsed >= minutes && !a.warned[a.task] {
		if a.warned == nil {
			a.warned = map[string]bool{}
		}
		a.warned[a.task] = true
		a.count++
		a.warnedMinutes = elapsed
		// DJA-565 decision 5: the hint names the transport's read command;
		// the herdr wording stays byte-identical.
		hint := "read its pane (herdr agent read " + agent + " --source visible)"
		if a.hc {
			hint = "read its terminal (hc read " + agent + ")"
		}
		sendWarning(fmt.Sprintf("'%s' has been working for %d min with no new output; %s — it may be stuck on a long command", agent, elapsed, hint))
	}
}
