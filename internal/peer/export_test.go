package peer

import (
	"time"

	"github.com/djalmajr/soho/internal/platform"
)

// ReadScreen is a test-only export so a test can time readScreen at the phase
// where its budget starts.
var ReadScreen = readScreen

// SendBusyBudgetMS is a test-only export of the busy wait's budget after an
// agent_not_ready wait (DJA-377).
var SendBusyBudgetMS = sendBusyBudgetMS

// NotReadyRetryOutOfBudget is a test-only export of the agent_not_ready
// retry's budget check, built from the fields it reads (DJA-377).
func NotReadyRetryOutOfBudget(ok bool, code string, timedOut bool, deadline, now time.Time) bool {
	return notReadyRetryOutOfBudget(resolvedTarget{OK: ok, Code: code, TimedOut: timedOut}, deadline, now)
}

// ResolveTargetWithinTimedOut is a test-only export: whether a resolution
// bounded by timeoutMS reports its get as timed out.
func ResolveTargetWithinTimedOut(target string, env platform.Env, timeoutMS int) bool {
	return resolveTargetWithin(target, env, timeoutMS).TimedOut
}
