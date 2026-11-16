package cli

import (
	"fmt"
	"strings"

	"github.com/djalmajr/soho/internal/backend"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

// registrantUnverifiedPane is the pane literal of a registration whose
// terminal could not be identified on native Core. terminalOwnership treats it
// as unknown, so gc classifies the row as unverified and never removes or
// stops it.
const registrantUnverifiedPane = "unverified"

// registrantPane is the caller lookup of the three registration paths
// (mutation-copy, copies add, procs add). On the native transport a lookup
// that dies, or that finds no terminal identity (HC_TERMINAL_ID unset), is not
// fatal: it gives pane "" and failed true, and the registration continues. On
// any other transport the lookup's exit is re-raised, so the commands behave
// as they did before. Only the exit the lookup raises is caught; any other
// panic propagates.
func registrantPane(env platform.Env) (pane string, failed bool) {
	defer func() {
		if value := recover(); value != nil {
			if _, ok := value.(*platform.ExitError); ok && env.Get("SOHO_TRANSPORT") == "hc" {
				pane, failed = "", true
				return
			}
			panic(value)
		}
	}()
	pane, identified := registrantCaller(env)
	return pane, !identified
}

// registrantCaller is the caller lookup itself; tests swap it to inject a
// lookup that dies or one that succeeds with an empty pane. identified is
// false only on the native transport when the terminal has no identity.
var registrantCaller = func(env platform.Env) (string, bool) {
	caller := backend.New(env).Runtime.Caller()
	if env.Get("SOHO_TRANSPORT") == "hc" && caller.PaneID == "" {
		return "", false
	}
	return caller.PaneID, true
}

// registrantFromEnv resolves the owner and pane of a registration that names
// no agent, in this order: (a) the roster agent whose pane is the caller's;
// (b) when SOHO_AGENT names a live row of this state dir's roster, that
// agent, with the pane of its row (the caller's pane stays when the row has
// none); (c) owner "-" with the caller's pane, or "-" when the lookup
// succeeded with none. When the lookup failed, whatever has no pane is
// "unverified", never "-". SOHO_AGENT is a hint from the environment: it only
// selects an existing roster row, it never creates one and never builds a
// path.
func registrantFromEnv(sd string, env platform.Env, callerPane string, lookupFailed bool) (owner, pane string) {
	pane = callerPane
	owner = rosterAgentForPane(sd, callerPane)
	if owner == "" {
		if name := env.Get("SOHO_AGENT"); strings.TrimSpace(name) != "" {
			if line := core.RosterLine(sd, name); line != "" {
				owner = name
				if fields := strings.Split(line, "\t"); len(fields) > 1 && fields[1] != "" {
					pane = fields[1]
				}
			}
		}
	}
	if owner == "" {
		owner = "-"
	}
	return owner, registrantPaneOrUnknown(pane, lookupFailed)
}

// registrantPaneOrUnknown fills a missing pane: "unverified" after a failed
// lookup, "-" otherwise.
func registrantPaneOrUnknown(pane string, lookupFailed bool) string {
	switch {
	case pane != "":
		return pane
	case lookupFailed:
		return registrantUnverifiedPane
	}
	return "-"
}

// registrantOwnerlessLine is the one stderr line of a registration that ends
// with owner "-" and no terminal on the native transport. Pane "unverified"
// (the lookup failed): gc keeps the row because its owner cannot be verified.
// Pane "-" (the lookup succeeded with no pane): gc treats the row as an
// orphan. The legacy transport prints nothing, as before. The removal paths
// named use only what exists: deleting or stopping the resource yourself, then
// `soho gc --yes` drops the missing or gone row.
func registrantOwnerlessLine(cmd, owner, pane, what string, env platform.Env) {
	if env.Get("SOHO_TRANSPORT") != "hc" || owner != "-" {
		return
	}
	var verdict string
	switch pane {
	case registrantUnverifiedPane:
		verdict = "gc keeps it because its owner cannot be verified"
	case "-":
		verdict = "gc treats it as an orphan and removes or stops it with `soho gc --yes`"
	default:
		return
	}
	var removal string
	if what == "copy" {
		removal = "to remove it, delete the directory yourself; `soho gc --yes` then drops the missing row"
	} else {
		removal = "to remove it, stop the process yourself; `soho gc --yes` then drops the gone line"
	}
	_, _ = fmt.Fprintf(platform.Stderr, "%s: registered without an owner (this terminal could not be identified; SOHO_AGENT did not name a roster agent); %s; %s\n", cmd, verdict, removal)
}
