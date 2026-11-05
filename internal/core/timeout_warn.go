package core

import "fmt"

// WarnShortTimeout warns that a unitless --timeout is below ten seconds:
// the usual mistake is seconds typed where milliseconds are expected
// (passing --timeout 45 meaning 45 s). Callers validate the value and exclude
// explicit units and omitted options. The value is left as passed: the
// command keeps its behavior. It prints the command's usual warning line and
// records it in the command's friction log (logFile; an empty logFile keeps
// the warning on screen only).
func WarnShortTimeout(command, logFile string, ms int64) {
	if ms <= 0 || ms >= 10000 {
		return
	}
	if ms < 1000 {
		Warn(fmt.Sprintf("%s: --timeout is in milliseconds; %d is under a second (for %d seconds pass %d)", command, ms, ms, ms*1000), logFile, command)
		return
	}
	Warn(fmt.Sprintf("%s: --timeout is in milliseconds; %d means %dms; use an explicit duration such as 30m", command, ms, ms), logFile, command)
}
