package spawn

import (
	"context"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/text"
	"github.com/djalmajr/soho/internal/transport"
)

var piThinkingLevels = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}

// piThinkingPartialFooter reports a footer caught mid-draw: the last bullet
// line ends in a proper prefix of a level. A word such as `cutoff` or a
// footer with no level yet is not a partial drawing.
func piThinkingPartialFooter(screen string) bool {
	lines := strings.Split(strings.ReplaceAll(screen, "\r\n", "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if !strings.Contains(line, "•") {
			continue
		}
		tail := line
		if boundary := strings.LastIndexAny(line, " \t•:"); boundary >= 0 {
			tail = strings.TrimSpace(line[boundary+1:])
		}
		for _, level := range piThinkingLevels {
			if len(tail) > 0 && len(tail) < len(level) && strings.HasPrefix(level, tail) {
				return true
			}
		}
		return false
	}
	return false
}

// piThinkingReadWindow bounds the extra screen read a fresh pi spawn makes
// after it is ready. A footer drawn after the first read gets at most one
// more read inside this window; an absent footer costs only that bounded
// retry.
const piThinkingReadWindow = 2 * time.Second

// readPiThinkingLevel reads the new worker's own visible screen for the
// thinking level in its footer. It never writes, targets another terminal or
// waits for a level: at most two reads are made, the second only when the
// first had no footer and only inside piThinkingReadWindow. now and sleep are
// parameters so the timing is exercised without a real two-second pause.
func readPiThinkingLevel(rt transport.Transport, name string, now func() time.Time, sleep func(time.Duration)) string {
	read := func() string {
		reply := rt.Read(context.Background(), transport.Target{ID: name}, transport.Read{Source: "visible"}, transport.Call{})
		return text.ThinkingFooterLevel(reply.Value)
	}
	start := now()
	if level := read(); level != "" {
		return level
	}
	remaining := piThinkingReadWindow - now().Sub(start)
	if remaining <= 0 {
		return ""
	}
	// Only the first read starts the window; sleep is bounded by what is
	// left of it after that read.
	sleep(remaining)
	return read()
}
