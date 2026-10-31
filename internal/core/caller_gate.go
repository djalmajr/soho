package core

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/djalmajr/soho/internal/backend"
	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/transport"
)

// callerCheckModeOk reports whether a caller_check value is one of the
// three the key accepts (refuse, warn, off).
func callerCheckModeOk(value string) bool {
	return value == "refuse" || value == "warn" || value == "off"
}

// CallerCheckMode resolves the caller_check policy (DJA-394): refuse by
// default, then warn or off. The SOHO_CALLER_CHECK environment layer wins
// over the file layers; an invalid value in either layer counts as
// refuse.
func CallerCheckMode(ctx *Config, env platform.Env) string {
	if value := env.Get("SOHO_CALLER_CHECK"); value != "" {
		if callerCheckModeOk(value) {
			return value
		}
		return "refuse"
	}
	if ctx != nil {
		if entry, ok := ctx.Entries["caller_check"]; ok && entry.Value != "" {
			if callerCheckModeOk(entry.Value) {
				return entry.Value
			}
		}
	}
	return "refuse"
}

// callerGateLine is the shared mismatch text (the command prefix is
// added by the caller; the friction line is the same text, the `soho: `
// prefix belongs to stderr only). After the fixed sentence it says what the
// session check (DJA-406) found: the two Claude Code sessions when no
// continued-in record links them, and the one other pane that shows this
// process's session. The suggestion is only text; no pane is ever switched.
func callerGateLine(res herdr.CallerCheck) string {
	line := fmt.Sprintf("HERDR_PANE_ID='%s' is a live pane but this process does not run in it (its process tree does not reach the pane's shell %d); the id was inherited from another process, such as a forked session. ", res.Pane, res.ShellPID)
	if res.Session != "" && res.PaneSession != "" {
		line += fmt.Sprintf("This process is Claude Code session %s; the pane hosts session %s, and no continued-in record links them. ", res.Session, res.PaneSession)
	}
	if res.ShownIn != "" {
		line += fmt.Sprintf("This Claude Code session is shown in pane '%s'; run with HERDR_PANE_ID=%s. ", res.ShownIn, res.ShownIn)
	}
	return line
}

const callerGateFix = " Run soho from the pane that hosts this agent (a Claude Code background session runs with HERDR_PANE_ID set to the pane that shows it), or set caller_check=warn"

// CallerGate applies the caller_check policy before the command acts on
// the claimed pane (the commands that open, title or write in its name:
// spawn/run/reopen, send, regrid, title, init and the collaborate
// operations that use the caller pane). It is a no-op for
// SOHO_NOWRITE=1, caller_check=off, and for verified and unknown results
// (a pane whose Claude Code session is this process's, or continued in it,
// is verified: no line in stderr or in the friction log).
// A mismatch with refuse records the line in the command's friction log
// and exits 2 without changing anything; with warn it logs the line and
// returns so the command proceeds.
//
// The refusal comes after the state refusals (the state dir inside the
// skill, the former state dir waiting for the setup): with either one the
// line goes to stderr only, without resolving the state dir or writing
// the friction log. Otherwise the log directory is created if it is
// missing (a first invocation in a workspace without state) so the
// refusal is recorded; the roster and every other state file are left
// alone. The `cli.Run` state skeleton (briefs, reports, wait and the
// roster header) that some commands get before their handler predates
// the gate and is outside this guarantee: a refusal never appends a
// roster line, event, delivery or message, nor changes a pane or a state
// that already existed.
func CallerGate(command string, ctx *Config, env platform.Env, cwd string) {
	if ctx == nil {
		ctx = &Config{}
	}
	if Nowrite(env) {
		return
	}
	mode := CallerCheckMode(ctx, env)
	if mode == "off" {
		return
	}
	svc := backend.New(env)
	res := svc.Runtime.CheckCaller(nil, transport.Call{})
	check := herdr.CallerCheck{
		State:       herdr.CallerState(res.Value.State),
		Pane:        res.Value.Pane,
		ShellPID:    res.Value.ShellPID,
		Reason:      res.Value.Reason,
		Session:     res.Value.Session,
		PaneSession: res.Value.PaneSession,
		ShownIn:     res.Value.ShownIn,
	}
	if check.State != herdr.CallerMismatch {
		return
	}
	logFile := ""
	if !callerGateStateless(ctx, env, cwd) {
		logFile = filepath.Join(StateDirPath(ctx, env, cwd), "friction.log")
		// Only the directory that holds the log: no roster, no other file.
		_ = os.MkdirAll(filepath.Dir(logFile), 0o777)
	}
	if mode == "refuse" {
		message := command + ": " + callerGateLine(check) + "Nothing was changed." + callerGateFix
		RecordFrictionError(message, 2, logFile, command)
		platform.Die(message, 2)
	}
	Warn(command+": "+callerGateLine(check)+"Continuing because caller_check=warn."+callerGateFix, logFile, command)
}

// callerGateStateless reports the two cases where the state dir is not
// resolved or written: inside the soho skill, or the former state dir
// still waiting for `soho setup`.
func callerGateStateless(ctx *Config, env platform.Env, cwd string) bool {
	if StateInSkill(ctx, env, cwd) != "" {
		return true
	}
	_, _, waiting := FormerStateWaiting(ctx, env, cwd)
	return waiting
}
