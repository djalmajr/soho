package core

import (
	"fmt"
	"path/filepath"

	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/platform"
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
// prefix belongs to stderr only).
func callerGateLine(res herdr.CallerCheck) string {
	return fmt.Sprintf("HERDR_PANE_ID='%s' is a live pane but this process does not run in it (its process tree does not reach the pane's shell %d); the id was inherited from another process, such as a forked session. ", res.Pane, res.ShellPID)
}

const callerGateFix = " Run soho from the pane that hosts this agent, or set caller_check=warn"

// CallerGate applies the caller_check policy before the command acts on
// the claimed pane (the commands that open, title or write in its name:
// spawn/run/reopen, send, regrid, title, init and the collaborate
// operations that use the caller pane). It is a no-op for
// SOHO_NOWRITE=1, caller_check=off, and for verified and unknown results.
// A mismatch with refuse records the line in the command's friction log
// and exits 2 without changing anything; with warn it logs the line and
// returns so the command proceeds.
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
	res := herdr.CheckCallerPane(env)
	if res.State != herdr.CallerMismatch {
		return
	}
	logFile := filepath.Join(StateDirPath(ctx, env, cwd), "friction.log")
	if mode == "refuse" {
		message := command + ": " + callerGateLine(res) + "Nothing was changed." + callerGateFix
		RecordFrictionError(message, 2, logFile, command)
		platform.Die(message, 2)
	}
	Warn(command+": "+callerGateLine(res)+"Continuing because caller_check=warn."+callerGateFix, logFile, command)
}
