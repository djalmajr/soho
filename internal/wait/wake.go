package wait

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/djalmajr/soho/internal/backend"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/transport"
)

// wakeServices builds the transport services the arming runs on; tests
// replace it to put a fake terminal.run connection behind the hc backend.
var wakeServices = backend.New

// parentRosterHint is advisory: an unreadable caller or parent leaves the
// original missing-roster error unchanged.
func parentRosterHint(agent string, cfg *core.Config, env platform.Env, cwd string) string {
	caller := env.Get("HC_TERMINAL_ID")
	if env.Get("SOHO_TRANSPORT") != "hc" || caller == "" {
		return ""
	}
	svc := backend.New(env)
	child := svc.Runtime.Get(context.Background(), transport.Target{ID: caller}, transport.Call{})
	if child.Err != nil || !child.Valid || !child.Value.ParentKnown || child.Value.Parent == "" {
		return ""
	}
	parent := child.Value.Parent
	parentRecord := svc.Runtime.Get(context.Background(), transport.Target{ID: parent}, transport.Call{})
	session := parentRecord.Value.WorkspaceID
	if parentRecord.Err != nil || !parentRecord.Valid || session == "" || session == "." || session == ".." || strings.ContainsAny(session, "/\\") {
		return ""
	}
	parentDir := filepath.Join(core.StateRootPath(cfg, env, cwd), session)
	if core.RosterLine(parentDir, agent) == "" {
		return ""
	}
	return fmt.Sprintf("; this terminal (%s) is a child of %s, whose roster holds '%s': run 'soho wait %s --wake' in %s's terminal (--wake creates its own child)", caller, parent, agent, agent, parent)
}

// WakeName is the terminal name of the child that `wait <agents> --wake`
// arms. A single agent is `wake-<agent>` when that satisfies the native
// terminal name rule; every other set (several agents, or a name that does
// not fit) is `wake-` and the first 8 hex digits of the SHA-256 of the
// sorted agent names joined by ",". The name is deterministic, so a second
// arming for the same agents meets the live child's name_taken answer.
func WakeName(agents []string) string {
	if len(agents) == 1 {
		if name := "wake-" + agents[0]; transport.ValidTerminalName(name) {
			return name
		}
	}
	sorted := slices.Clone(agents)
	slices.Sort(sorted)
	sum := sha256.Sum256([]byte(strings.Join(sorted, ",")))
	return "wake-" + hex.EncodeToString(sum[:])[:8]
}

// wakeChildArgs is the argv after the launcher of the child that runs the
// non-settling wait: never --wake (the child would arm another child), always
// --timeout with the explicit millisecond value and --observe.
func wakeChildArgs(agents []string, timeoutMs int64, anyDone bool) []string {
	args := append([]string{"wait"}, agents...)
	args = append(args, "--timeout", strconv.FormatInt(timeoutMs, 10))
	if anyDone {
		args = append(args, "--any")
	}
	return append(args, "--observe")
}

// cmdWake is `soho wait <agent>... --wake`, after the arguments were
// validated like a foreground wait. It arms one child terminal of the caller
// that runs the non-settling wait (`--observe`) and exits when the agents
// settle, so the exit note Core types into the caller's terminal wakes an
// orchestrator whose turn has ended. It never probes the worker. Core's
// answer to the run is the only thing it waits for.
//
// The child ends with the caller's program (no one else could receive its
// note) and notifies the caller on exit. A finished child of the caller with
// the same name is removed first, best effort; a live one makes the run
// answer name_taken, reported as already-armed with nothing started.
func cmdWake(agents []string, timeoutMs float64, anyDone bool, ctx *core.Config, env platform.Env, cwd, sd string) int {
	if env.Get("SOHO_TRANSPORT") != "hc" {
		core.DieFriction("wait: --wake needs the native transport (SOHO_TRANSPORT=hc)", 2, "", "wait")
	}
	if math.IsNaN(timeoutMs) || math.IsInf(timeoutMs, 0) || timeoutMs < 0 {
		core.DieFriction("wait: --wake needs a finite --timeout", 2, "", "wait")
	}
	stateDir := wakeStateDir(ctx, env, cwd)
	svc := wakeServices(env)
	var runner transport.ChildRunner
	for _, candidate := range []any{svc.Terminal, svc.Runtime} {
		if r, ok := candidate.(transport.ChildRunner); ok {
			runner = r
			break
		}
	}
	if runner == nil {
		core.DieFriction("wait: --wake needs the native transport (SOHO_TRANSPORT=hc)", 2, "", "wait")
	}
	remaining, err := uncoveredWakeAgents(svc, env.Get("HC_TERMINAL_ID"), sd, agents)
	if err != nil {
		core.DieFriction("wait: --wake could not check existing wakes: "+err.Error(), 4, "", "wait")
	}
	if len(remaining) == 0 {
		return 0
	}
	agents = remaining
	ms := int64(timeoutMs)
	name := WakeName(agents)
	runCtx := context.Background()
	// A finished child of this name still holds it until its record is
	// removed. A removal that fails changes nothing: the run answers
	// name_taken if the name is still held, and that is reported as is.
	_ = runner.RemoveFinishedChild(runCtx, name, transport.Call{})
	rep := runner.RunChild(runCtx, transport.ChildRun{
		Name:          name,
		Program:       platform.LauncherPath(env),
		Args:          wakeChildArgs(agents, ms, anyDone),
		Cwd:           cwd,
		NotifyCaller:  true,
		OutliveCaller: false,
		StateDir:      stateDir,
	}, transport.Call{})
	if rep.Err != nil {
		if rep.Err.Kind == transport.FailureConflict {
			jsonLine(jsonjs.O("wake", "already-armed", "agents", stringsAny(agents), "name", name))
			return 0
		}
		message := "wait: --wake could not start its child terminal '" + name + "': " + rep.Err.Error()
		if rep.Err.Kind == transport.FailureRunOutcomeUnknown {
			message += fmt.Sprintf("; the child may be running: check `hc get %s` before arming again", name)
		}
		core.DieFriction(message, 4, "", "wait")
	}
	if err := recordWake(sd, rep.Value, agents); err != nil {
		core.DieFriction(fmt.Sprintf("wait: --wake started child '%s' but could not record its agents: %v; check it before arming again", rep.Value, err), 4, "", "wait")
	}
	jsonLine(jsonjs.O("wake", "armed", "agents", stringsAny(agents), "terminal", rep.Value, "name", name, "timeout_ms", ms))
	return 0
}

// uncoveredWakeAgents reconciles records only after a successful live listing.
func uncoveredWakeAgents(svc transport.Services, caller, sd string, agents []string) ([]string, error) {
	rep := svc.Runtime.List(context.Background(), transport.Scope{All: true}, transport.Call{})
	if rep.Err != nil {
		return nil, rep.Err
	}
	if !rep.Valid {
		return nil, fmt.Errorf("terminal listing is invalid")
	}
	live := make(map[string]bool)
	for _, term := range rep.Value {
		if term.ParentKnown && term.Parent == caller && term.NativeState == "live" {
			live[term.Target.ID] = true
		}
	}
	dir := filepath.Join(sd, "wakes")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return agents, nil
	}
	if err != nil {
		return nil, err
	}
	covered := make(map[string]string)
	for _, entry := range entries {
		name := entry.Name()
		if !live[name] {
			if err := os.Remove(filepath.Join(dir, name)); err != nil {
				return nil, err
			}
			continue
		}
		if !entry.Type().IsRegular() {
			return nil, fmt.Errorf("invalid wake record %q", name)
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		for _, agent := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
			if agent != "" {
				if _, exists := covered[agent]; !exists {
					covered[agent] = name
				}
			}
		}
	}
	remaining := make([]string, 0, len(agents))
	for _, agent := range agents {
		if terminal, ok := covered[agent]; ok {
			fmt.Fprintf(platform.Stderr, "soho: wait: a wake for '%s' is already armed (%s); not arming another for it\n", agent, terminal)
		} else {
			remaining = append(remaining, agent)
		}
	}
	return remaining, nil
}

func recordWake(sd, terminal string, agents []string) error {
	if terminal == "" || terminal == "." || terminal == ".." || strings.ContainsAny(terminal, "/\\\x00\r\n") {
		return fmt.Errorf("invalid terminal address %q", terminal)
	}
	for _, agent := range agents {
		if strings.ContainsAny(agent, "\x00\r\n") {
			return fmt.Errorf("invalid agent name")
		}
	}
	dir := filepath.Join(sd, "wakes")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, terminal), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.WriteString(strings.Join(agents, "\n") + "\n")
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

// stringsAny is the JSON array of the agents, in the order they were given.
func stringsAny(values []string) []any {
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = v
	}
	return out
}

// wakeStateDir is the state root the armed child must resolve: the caller's
// own resolved root (`core.StateRootPath`, so a relative SOHO_STATE_DIR is
// joined to the project root and `SOHO_DIR` keeps its precedence) when the
// caller's environment sets SOHO_STATE_DIR or SOHO_DIR, and "" otherwise,
// where the child resolves the project's `state_dir` or the default from its
// own cwd, the caller's. The child receives the root as SOHO_STATE_DIR; no
// other SOHO_* variable of the caller is forwarded. A root that is not
// absolute or holds NUL, CR or LF exits 2 before any terminal starts.
func wakeStateDir(ctx *core.Config, env platform.Env, cwd string) string {
	if env.Get("SOHO_STATE_DIR") == "" && env.Get("SOHO_DIR") == "" {
		return ""
	}
	root := core.StateRootPath(ctx, env, cwd)
	if strings.ContainsAny(root, "\x00\r\n") || !filepath.IsAbs(root) {
		core.DieFriction("wait: --wake cannot forward the state root as SOHO_STATE_DIR: it must be an absolute path without NUL, CR or LF", 2, "", "wait")
	}
	return root
}
