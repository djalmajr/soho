package cli

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/backend"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/provider"
	"github.com/djalmajr/soho/internal/transport"
)

const modelSetUsage = "usage: soho model-set <worker> <provider/model> [--effort <level>] [--interrupt]"

// modelSetModelRE is the whole hostile-input admission for the model
// argument: it is typed as keys, so control characters and spaces never
// reach a terminal. A slash separates the provider from the model id.
var modelSetModelRE = regexp.MustCompile(`^[A-Za-z0-9._:/-]+$`)

// modelSetFooterLevelRE reads the thinking level pi prints in its footer after
// a bullet, for example `(codex) gpt-6.2-sol • medium`.
var modelSetFooterLevelRE = regexp.MustCompile(`•\s*(off|minimal|low|medium|high|xhigh|max)\b`)

var modelSetEfforts = map[string]bool{
	"off": true, "minimal": true, "low": true, "medium": true,
	"high": true, "xhigh": true, "max": true,
}

// The verification knobs are variables so the tests can use a short bound
// without changing the ten-second contract of the command.
var (
	modelSetVerifyTimeout = 10 * time.Second
	modelSetPollInterval  = 200 * time.Millisecond
	modelSetSleep         = func(d time.Duration) { time.Sleep(d) }
)

type modelSetArgs struct {
	agent     string
	model     string
	provider  string
	id        string
	effort    string
	interrupt bool
}

func parseModelSetArgs(argv []string) modelSetArgs {
	var parsed modelSetArgs
	for i := 0; i < len(argv); i++ {
		switch argv[i] {
		case "--effort":
			if i+1 >= len(argv) {
				core.DieFriction("model-set: --effort expects a level; "+modelSetUsage, 2, frictionLogPath, "model-set")
			}
			i++
			parsed.effort = argv[i]
		case "--interrupt":
			parsed.interrupt = true
		default:
			if strings.HasPrefix(argv[i], "-") {
				core.DieFriction(fmt.Sprintf("model-set: unexpected argument '%s'; %s", argv[i], modelSetUsage), 2, frictionLogPath, "model-set")
			}
			if parsed.agent == "" {
				parsed.agent = argv[i]
			} else if parsed.model == "" {
				parsed.model = argv[i]
			} else {
				core.DieFriction(fmt.Sprintf("model-set: unexpected argument '%s'; %s", argv[i], modelSetUsage), 2, frictionLogPath, "model-set")
			}
		}
	}
	if parsed.agent == "" || parsed.model == "" {
		core.DieFriction(modelSetUsage, 2, frictionLogPath, "model-set")
	}
	if !modelSetModelRE.MatchString(parsed.model) {
		core.DieFriction(fmt.Sprintf("model-set: invalid model %q; use provider/model with [A-Za-z0-9._:/-] only", parsed.model), 2, frictionLogPath, "model-set")
	}
	name, id, found := strings.Cut(parsed.model, "/")
	if !found || name == "" || id == "" {
		core.DieFriction(fmt.Sprintf("model-set: invalid model %q; use provider/model with [A-Za-z0-9._:/-] only", parsed.model), 2, frictionLogPath, "model-set")
	}
	parsed.provider, parsed.id = name, id
	if parsed.effort != "" && !modelSetEfforts[parsed.effort] {
		core.DieFriction(fmt.Sprintf("model-set: invalid effort %q (off|minimal|low|medium|high|xhigh|max)", parsed.effort), 2, frictionLogPath, "model-set")
	}
	return parsed
}

func cmdModelSet(argv []string, ctx *core.Config, env platform.Env, cwd string) int {
	args := parseModelSetArgs(argv)
	sd := core.StateDirPath(ctx, env, cwd)
	line := core.RosterLine(sd, args.agent)
	if line == "" {
		core.DieFriction(fmt.Sprintf("agent '%s' is not in this skill's roster (spawn it first, or pass a name you spawned)", args.agent), 3, frictionLogPath, "model-set")
	}
	fields := strings.Split(line, "\t")
	at := func(i int) string {
		if i < len(fields) {
			return fields[i]
		}
		return ""
	}
	kind, pane := at(2), at(1)
	if kind != "pi" {
		core.DieFriction(fmt.Sprintf("model-set: '%s' is %s; only pi switches models live; use soho reopen %s --model %s", args.agent, kind, args.agent, args.model), 2, frictionLogPath, "model-set")
	}
	if pane == "" {
		core.DieFriction(fmt.Sprintf("model-set: '%s' has no terminal in the roster; nothing was typed", args.agent), 4, frictionLogPath, "model-set")
	}

	svc := backend.New(env)
	callCtx := context.Background()
	modelSetDieOnDeadWorker(args.agent, stateFromReply(svc.Runtime.Get(callCtx, transport.Target{ID: args.agent}, transport.Call{Timeout: herdr.Timeout})))
	source := modelSetScreenSource(ctx, env)
	screen, ok := modelSetRead(svc, args.agent, source)
	if !ok {
		core.DieFriction(fmt.Sprintf("model-set: could not read the screen of '%s'; nothing was typed", args.agent), 4, frictionLogPath, "model-set")
	}
	if modelSetHasDraft(screen) {
		core.DieFriction(fmt.Sprintf("model-set: '%s' has text in its input box; nothing was typed", args.agent), 10, frictionLogPath, "model-set")
	}

	if args.interrupt && !modelSetKey(svc, pane, "esc") {
		core.DieFriction(fmt.Sprintf("model-set: could not send esc to '%s'; nothing was typed", pane), 4, frictionLogPath, "model-set")
	}
	if !modelSetType(svc, pane, "/model "+args.model) {
		core.DieFriction(fmt.Sprintf("model-set: could not type /model on '%s'; roster unchanged", pane), 4, frictionLogPath, "model-set")
	}
	if args.effort != "" && !modelSetType(svc, pane, "/thinking "+args.effort) {
		core.DieFriction(fmt.Sprintf("model-set: could not type /thinking on '%s'; roster unchanged", pane), 4, frictionLogPath, "model-set")
	}

	want := "(" + args.provider + ") " + args.id
	matched, shownEffort, lastFooter := modelSetVerify(svc, args.agent, source, want)
	if !matched {
		// A footer that still shows the old model leaves the roster as it was.
		core.DieFriction(fmt.Sprintf("model-set: '%s' never showed %s within %s (last footer: %q); roster unchanged", args.agent, want, modelSetVerifyTimeout, lastFooter), 1, frictionLogPath, "model-set")
	}
	if args.effort != "" && shownEffort != args.effort {
		core.DieFriction(fmt.Sprintf("model-set: '%s' shows thinking '%s', not '%s', for %s; roster unchanged", args.agent, shownEffort, args.effort, want), 1, frictionLogPath, "model-set")
	}
	core.RosterSetModel(sd, args.agent, args.model, shownEffort)
	_, _ = fmt.Fprintln(platform.Stdout, jsonjs.Stringify(jsonjs.O(
		"agent", args.agent, "model", args.model, "effort", shownEffort, "verified", true,
	)))
	return 0
}

func modelSetRead(svc transport.Services, agent, source string) (string, bool) {
	rep := svc.Runtime.Read(context.Background(), transport.Target{ID: agent}, transport.Read{Source: source, Lines: intPtr(compactScreenLines)}, transport.Call{Timeout: herdr.Timeout})
	if rep.Err != nil {
		return "", false
	}
	return rep.Value, true
}

// modelSetHasDraft treats any text between pi's last two input-box borders as
// a draft; an empty box has only blank lines there, and history above the box
// never counts.
func modelSetHasDraft(screen string) bool {
	start, end, ok := provider.PiInputRegion(screen)
	if !ok {
		return false
	}
	lines := strings.Split(strings.ReplaceAll(screen, "\r\n", "\n"), "\n")
	for _, line := range lines[start:end] {
		if strings.TrimSpace(line) != "" {
			return true
		}
	}
	return false
}

func modelSetType(svc transport.Services, pane, command string) bool {
	for _, r := range command {
		key := string(r)
		if r == ' ' {
			key = "space"
		}
		if !modelSetKey(svc, pane, key) {
			return false
		}
	}
	return modelSetKey(svc, pane, "Enter") && modelSetKey(svc, pane, "Enter")
}

func modelSetKey(svc transport.Services, pane, key string) bool {
	return svc.Runtime.Keys(context.Background(), transport.Target{ID: pane}, key, true, transport.Call{Timeout: herdr.Timeout}).Value
}

// modelSetVerify polls the footer for the confirmed model and returns the
// thinking level it shows plus the last footer line for the refusal message.
// It never writes the roster: that is the caller's job after a match.
func modelSetVerify(svc transport.Services, agent, source, want string) (bool, string, string) {
	deadline := platform.Now().Add(modelSetVerifyTimeout)
	lastFooter := ""
	for {
		modelSetDieOnDeadWorker(agent, stateFromReply(svc.Runtime.Get(context.Background(), transport.Target{ID: agent}, transport.Call{Timeout: herdr.Timeout})))
		screen, ok := modelSetRead(svc, agent, source)
		if !ok {
			core.DieFriction(fmt.Sprintf("model-set: could not read the footer of '%s'; roster unchanged", agent), 4, frictionLogPath, "model-set")
		}
		footer := modelSetFooterLine(screen)
		if footer != "" {
			lastFooter = footer
		}
		if strings.Contains(footer, want) {
			if m := modelSetFooterLevelRE.FindStringSubmatch(footer); m != nil {
				return true, m[1], lastFooter
			}
			return true, "", lastFooter
		}
		if !platform.Now().Before(deadline) {
			return false, "", lastFooter
		}
		modelSetSleep(modelSetPollInterval)
	}
}

// modelSetFooterLine reads only the last three non-empty screen lines: pi's
// footer sits below its input box, and history above it is not a footer.
func modelSetFooterLine(screen string) string {
	lines := strings.Split(strings.ReplaceAll(screen, "\r\n", "\n"), "\n")
	last, seen := "", 0
	for i := len(lines) - 1; i >= 0 && seen < 3; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		seen++
		if last == "" {
			last = line
		}
		if strings.Contains(line, "•") {
			return line
		}
	}
	return last
}

func modelSetDieOnDeadWorker(agent string, state stateProbe) {
	if state.State == "gone" {
		core.DieFriction(fmt.Sprintf("model-set: agent '%s' is no longer live; roster unchanged", agent), 6, frictionLogPath, "model-set")
	}
	if state.State == "unavailable" {
		cause := state.Cause
		if cause == "" {
			cause = "herdr agent get failed"
		}
		core.DieFriction(fmt.Sprintf("model-set: agent '%s' is unavailable: %s; nothing was typed", agent, cause), 4, frictionLogPath, "model-set")
	}
}

// modelSetScreenSource selects the screen source the resolved transport can
// read: herdr's recent history, or the visible screen under hc (which has no
// "recent" source but carries the footer below the input box).
func modelSetScreenSource(ctx *core.Config, env platform.Env) string {
	if core.TransportMode(ctx, env) == "hc" {
		return "visible"
	}
	return "recent"
}
