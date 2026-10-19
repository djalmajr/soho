package herdr

import (
	"encoding/json"
	"os"
	"sync"

	"github.com/djalmajr/soho/internal/platform"
)

// CallerState is the outcome of the caller-pane check (DJA-394): whether
// this process actually runs in the pane HERDR_PANE_ID claims.
type CallerState string

const (
	// CallerVerified: the process tree reaches the claimed pane's shell
	// (or the pane was delegated on purpose, below).
	CallerVerified CallerState = "verified"
	// CallerMismatch: the tree was read to the root without passing
	// through the pane's shell: the id was inherited from another
	// process (a forked session).
	CallerMismatch CallerState = "mismatch"
	// CallerUnknown: no claim, an unreadable shell_pid, or a failed
	// ancestry read. Commands proceed without the check's line.
	CallerUnknown CallerState = "unknown"
)

// CallerCheck is the result of CheckCallerPane.
type CallerCheck struct {
	State    CallerState
	Pane     string
	ShellPID int
	Reason   string // short

	// Session, PaneSession and ShownIn describe a mismatch for the refusal
	// text (DJA-406). Session is this process's Claude Code session
	// (CLAUDE_CODE_SESSION_ID), PaneSession the one herdr reports for the
	// claimed pane, ShownIn the single other pane that shows this process's
	// session. Each is "" when it is not known or not valid.
	Session     string
	PaneSession string
	ShownIn     string
}

// callerCheckMemo is the per-command memo (decision 8: one check per
// command and claim; cli.Run resets it). The key is (pane, SOHO_DELEGATED_PANE) so a process
// that only ever claims one pane computes it once.
var callerCheckMemo struct {
	mu    sync.Mutex
	byKey map[string]CallerCheck
}

// callerCheckMemoLock guards the memo and creates the map on first use.
func callerCheckMemoLock() {
	callerCheckMemo.mu.Lock()
	if callerCheckMemo.byKey == nil {
		callerCheckMemo.byKey = map[string]CallerCheck{}
	}
}

// ResetCallerCheck clears the memo. cli.Run calls it at the start of every
// command, so the memo holds one check per command: a process runs one
// command in production, and in-process test runs never see an earlier
// command's result.
func ResetCallerCheck() {
	callerCheckMemo.mu.Lock()
	callerCheckMemo.byKey = map[string]CallerCheck{}
	callerCheckMemo.mu.Unlock()
}

// CheckCallerPane verifies that this process runs in the pane HERDR_PANE_ID
// claims, in this order:
//  1. without HERDR_PANE_ID there is nothing to check: unknown;
//  2. SOHO_DELEGATED_PANE equal to HERDR_PANE_ID is a deliberate
//     delegation (the plugin acting in the name of the focused pane):
//     verified, without any Herdr call;
//  3. `herdr pane process-info --pane <HERDR_PANE_ID>` reads
//     result.process_info.shell_pid; a failure, timeout, or an absent or
//     non-positive shell_pid gives unknown;
//  4. the parent chain of this process (platform.ProcessAncestors) that
//     includes the shell_pid is verified; a chain read to the root
//     (pid 1, 0 or a parentless pid) without it is a mismatch; a failed
//     read midway is unknown;
//  5. only for that mismatch, the session check (caller_session.go): the
//     pane's Claude Code session is this process's session, or continued
//     in it, is verified. Any failure there leaves the mismatch.
//
// The climb never treats the Herdr server as a match: only the pane's
// shell_pid counts.
func CheckCallerPane(env platform.Env) CallerCheck {
	pane := env.Get("HERDR_PANE_ID")
	if pane == "" {
		return CallerCheck{State: CallerUnknown, Reason: "HERDR_PANE_ID is unset"}
	}
	delegated := env.Get("SOHO_DELEGATED_PANE")
	key := pane + "\x00" + delegated
	callerCheckMemoLock()
	if res, ok := callerCheckMemo.byKey[key]; ok {
		callerCheckMemo.mu.Unlock()
		return res
	}
	callerCheckMemo.mu.Unlock()
	res := checkCallerPane(pane, delegated, env)
	callerCheckMemoLock()
	callerCheckMemo.byKey[key] = res
	callerCheckMemo.mu.Unlock()
	return res
}

func checkCallerPane(pane, delegated string, env platform.Env) CallerCheck {
	if delegated != "" && delegated == pane {
		return CallerCheck{State: CallerVerified, Pane: pane, Reason: "delegated pane"}
	}
	shellPID, ok := paneShellPID(pane, env)
	if !ok {
		return CallerCheck{State: CallerUnknown, Pane: pane, Reason: "shell_pid unavailable"}
	}
	chain, err := platform.ProcessAncestors(os.Getpid(), env)
	if err != nil {
		return CallerCheck{State: CallerUnknown, Pane: pane, ShellPID: shellPID, Reason: "ancestry read failed"}
	}
	for _, pid := range chain {
		if pid == shellPID {
			return CallerCheck{State: CallerVerified, Pane: pane, ShellPID: shellPID, Reason: "tree reaches the pane's shell"}
		}
	}
	// The tree missed the shell. A Claude Code background session that
	// continued the pane's session runs outside it: the session check can
	// only turn this mismatch into verified (caller_session.go).
	session, verified := verifiedBySession(pane, ownSession(env), env)
	if verified {
		return CallerCheck{State: CallerVerified, Pane: pane, ShellPID: shellPID, Reason: session.Reason}
	}
	return CallerCheck{State: CallerMismatch, Pane: pane, ShellPID: shellPID, Reason: "tree does not reach the pane's shell",
		Session: session.Session, PaneSession: session.PaneSession, ShownIn: session.ShownIn}
}

// paneShellPID reads result.process_info.shell_pid from
// `herdr pane process-info --pane <pane>`.
func paneShellPID(pane string, env platform.Env) (int, bool) {
	r := platform.RunCli("herdr", []string{"pane", "process-info", "--pane", pane}, platform.RunOptions{Env: env, TimeoutMs: 10000})
	if r.NotFound || r.TimedOut || r.Status == nil || *r.Status != 0 {
		return 0, false
	}
	var payload struct {
		Result struct {
			ProcessInfo struct {
				ShellPID int `json:"shell_pid"`
			} `json:"process_info"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(r.Stdout), &payload); err != nil {
		return 0, false
	}
	if payload.Result.ProcessInfo.ShellPID <= 0 {
		return 0, false
	}
	return payload.Result.ProcessInfo.ShellPID, true
}
