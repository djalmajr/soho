package wait

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/djalmajr/soho/internal/backend"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/transport"
)

// wakeServices builds the transport services the arming runs on; tests
// replace it to put a fake terminal.run connection behind the hc backend.
var wakeServices = backend.New

// ParentRosterHint uses the caller's live parent to suggest where to retry a
// command after its own roster lookup failed.
func ParentRosterHint(command, agent string, env platform.Env) string {
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
	if parentRecord.Err != nil || !parentRecord.Valid ||
		parentRecord.Value.NativeState != "live" || parentRecord.Value.State == "gone" {
		return ""
	}
	retry := "soho " + command + " " + agent
	if command == "wait" {
		return fmt.Sprintf("; this terminal (%s) is a child of %s: if %s started that worker, run '%s --wake' in %s's terminal (--wake creates its own child)", caller, parent, parent, retry, parent)
	}
	return fmt.Sprintf("soho: this terminal (%s) has no '%s' in its roster; it is a child of %s: if %s started that worker, run '%s' in %s's terminal", caller, agent, parent, parent, retry, parent)
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
// note) and notifies the caller on exit. A plain --wake first stops this
// caller's previous recorded wakes (kill then rm through the transport) and
// arms exactly the requested agents; --add keeps the older reconciliation,
// skipping covered agents and answering already-armed when none remain. A
// finished child of the caller with the new name is removed first, best
// effort; a live one makes the run answer name_taken, reported as
// already-armed with nothing started.
func cmdWake(agents []string, timeoutMs float64, anyDone, add bool, ctx *core.Config, env platform.Env, cwd, sd string) int {
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
	storage, storageErr := openWakeStorage(sd)
	if storageErr != nil {
		fmt.Fprintf(platform.Stderr, "soho: wait: wake storage unavailable (%v); wake is armed without duplicate detection for this call\n", storageErr)
	} else {
		defer storage.Close()
	}
	caller := env.Get("HC_TERMINAL_ID")
	var remaining []string
	var stopped wakeStops
	if add {
		var err error
		remaining, err = uncoveredWakeAgents(svc, caller, storage, agents)
		if err != nil {
			core.DieFriction("wait: --wake could not check existing wakes: "+err.Error(), 4, "", "wait")
		}
	} else {
		var err error
		stopped, err = stopPreviousWakes(svc, caller, storage)
		if err != nil {
			core.DieFriction("wait: --wake could not stop previous wakes: "+err.Error(), 4, "", "wait")
		}
		remaining = agents
	}
	replaced := stopped.terminals
	if len(remaining) == 0 {
		return 0
	}
	agents = remaining
	unwatched := unwatchedWakeAgents(stopped.agents, agents, sd)
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
	if err := recordWake(storage, rep.Value, agents); err != nil {
		core.DieFriction(fmt.Sprintf("wait: --wake started child '%s' but could not record its agents: %v; check it before arming again", rep.Value, err), 4, "", "wait")
	}
	armLine := jsonjs.O("wake", "armed", "agents", stringsAny(agents), "terminal", rep.Value, "name", name, "timeout_ms", ms, "replaced", stringsAny(replaced))
	if len(unwatched) > 0 {
		armLine.Set("unwatched", stringsAny(unwatched))
		fmt.Fprintf(platform.Stderr, "soho: wait: this wake replaced the wake(s) of %s; to keep watching them, rearm with: soho wait %s --wake --add\n", strings.Join(unwatched, ", "), strings.Join(unwatched, " "))
	}
	jsonLine(armLine)
	return 0
}

// openWakeStorage retains the checked directory for all record operations.
func openWakeStorage(sd string) (*os.Root, error) {
	path := filepath.Join(sd, "wakes")
	if err := os.Mkdir(path, 0700); err != nil && !os.IsExist(err) {
		return nil, err
	}
	if err := core.CheckPrivateStateDirectory(path, "wakes"); err != nil {
		return nil, err
	}
	before, err := os.Lstat(path)
	if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("wake directory changed")
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(before, opened) {
		_ = root.Close()
		return nil, fmt.Errorf("wake directory changed")
	}
	if err := core.CheckPrivateStateDirectoryInfo(path, "wakes", opened); err != nil {
		_ = root.Close()
		return nil, err
	}
	return root, nil
}

// wakeSnapshot is the live part of one successful Core listing: every live
// terminal address, and the subset Core reports as a child of this caller.
// No terminal is ever selected by name; recorded wakes are matched against
// this snapshot.
type wakeSnapshot struct {
	live     map[string]bool
	children map[string]bool
}

func liveWakeSnapshot(svc transport.Services, caller string) (wakeSnapshot, error) {
	rep := svc.Runtime.List(context.Background(), transport.Scope{All: true}, transport.Call{})
	if rep.Err != nil {
		return wakeSnapshot{}, rep.Err
	}
	if !rep.Valid {
		return wakeSnapshot{}, fmt.Errorf("terminal listing is invalid")
	}
	snapshot := wakeSnapshot{live: make(map[string]bool), children: make(map[string]bool)}
	for _, term := range rep.Value {
		if term.NativeState == "live" {
			snapshot.live[term.Target.ID] = true
			if term.ParentKnown && term.Parent == caller {
				snapshot.children[term.Target.ID] = true
			}
		}
	}
	return snapshot, nil
}

// uncoveredWakeAgents reconciles records only after a successful live listing.
func uncoveredWakeAgents(svc transport.Services, caller string, dir *os.Root, agents []string) ([]string, error) {
	snapshot, err := liveWakeSnapshot(svc, caller)
	if err != nil {
		return nil, err
	}
	if dir == nil {
		return agents, nil
	}
	f, err := dir.Open(".")
	if err != nil {
		return nil, err
	}
	entries, err := f.ReadDir(-1)
	_ = f.Close()
	if err != nil {
		return nil, err
	}
	covered := make(map[string]string)
	for _, entry := range entries {
		name := entry.Name()
		if !validWakeAddress(name) {
			return nil, fmt.Errorf("invalid wake record %q", name)
		}
		if !snapshot.live[name] {
			if err := dir.Remove(name); err != nil {
				return nil, err
			}
			continue
		}
		if !snapshot.children[name] {
			continue
		}
		info, err := dir.Lstat(name)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("invalid wake record %q", name)
		}
		file, err := dir.Open(name)
		if err != nil {
			return nil, err
		}
		opened, statErr := file.Stat()
		if statErr != nil || !os.SameFile(info, opened) {
			_ = file.Close()
			return nil, fmt.Errorf("wake record %q changed", name)
		}
		data, err := io.ReadAll(file)
		_ = file.Close()
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

// wakeStops is what a plain --wake stopped on this caller: the addresses
// for the arming line and the agents those wakes recorded, in record order.
type wakeStops struct {
	terminals []string
	agents    []string
}

// stopPreviousWakes closes every wake recorded in this state dir that Core
// still reports live and parented by the caller. The kill and the remove go
// through the transport's Close, never a shell, and no terminal is chosen by
// name. A recorded wake Core no longer reports live has only its stale
// record dropped; a live wake of another parent, or a live child of the
// caller that is not recorded here, is left completely alone. Each stopped
// wake's record is read for its agents (an unreadable one contributes none);
// no worker status is ever probed here.
func stopPreviousWakes(svc transport.Services, caller string, dir *os.Root) (wakeStops, error) {
	snapshot, err := liveWakeSnapshot(svc, caller)
	if err != nil {
		return wakeStops{}, err
	}
	stopped := wakeStops{terminals: []string{}, agents: []string{}}
	if dir == nil {
		return stopped, nil
	}
	f, err := dir.Open(".")
	if err != nil {
		return wakeStops{}, err
	}
	entries, err := f.ReadDir(-1)
	_ = f.Close()
	if err != nil {
		return wakeStops{}, err
	}
	for _, entry := range entries {
		name := entry.Name()
		if !validWakeAddress(name) {
			return wakeStops{}, fmt.Errorf("invalid wake record %q", name)
		}
		if !snapshot.live[name] {
			if err := dir.Remove(name); err != nil {
				return wakeStops{}, err
			}
			continue
		}
		if !snapshot.children[name] {
			continue
		}
		info, err := dir.Lstat(name)
		if err != nil {
			return wakeStops{}, err
		}
		if !info.Mode().IsRegular() {
			return wakeStops{}, fmt.Errorf("invalid wake record %q", name)
		}
		recordAgents := wakeRecordedAgents(dir, name, info)
		if rep := svc.Runtime.Close(context.Background(), transport.Target{ID: name}, transport.Call{}); rep.Err != nil {
			return wakeStops{}, fmt.Errorf("could not close wake %s: %w", name, rep.Err)
		}
		if err := dir.Remove(name); err != nil {
			return wakeStops{}, err
		}
		stopped.terminals = append(stopped.terminals, name)
		stopped.agents = append(stopped.agents, recordAgents...)
	}
	return stopped, nil
}

func validWakeAddress(terminal string) bool {
	return terminal != "" && terminal != "." && terminal != ".." && !strings.ContainsAny(terminal, "/\\\x00\r\n")
}

func recordWake(dir *os.Root, terminal string, agents []string) error {
	if !validWakeAddress(terminal) {
		return fmt.Errorf("invalid terminal address %q", terminal)
	}
	for _, agent := range agents {
		if strings.ContainsAny(agent, "\x00\r\n") {
			return fmt.Errorf("invalid agent name")
		}
	}
	if dir == nil {
		return nil
	}
	f, err := dir.OpenFile(terminal, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
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

// armWakeSignals is the observe child's signal handler. On Unix, a SIGHUP,
// SIGINT or SIGTERM prints one interrupted line with the rearm command and
// exits with 128 plus the signal number, so an orchestrator that killed the
// child learns which agents lost their watch. The names in that line are the
// validated agents this wait was given; nothing Core printed is echoed.
// Windows keeps today's behavior: no handler is installed. The returned
// function removes the handler when a wait returns without a signal.
func armWakeSignals(agents []string, timeoutMs int64, anyDone bool) func() {
	noop := func() {}
	if runtime.GOOS == "windows" {
		return noop
	}
	ch := make(chan os.Signal, 1)
	done := make(chan struct{})
	signal.Notify(ch, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		select {
		case <-done:
		case sig := <-ch:
			signal.Stop(ch)
			jsonLine(jsonjs.O("wake", "interrupted", "signal", wakeSignalName(sig), "agents", stringsAny(agents), "rearm", wakeRearmCommand(agents, timeoutMs, anyDone)))
			code := 128
			if number, ok := sig.(syscall.Signal); ok {
				code += int(number)
			}
			os.Exit(code)
		}
	}()
	return func() {
		close(done)
		signal.Stop(ch)
	}
}

func wakeSignalName(sig os.Signal) string {
	switch sig {
	case syscall.SIGHUP:
		return "SIGHUP"
	case syscall.SIGINT:
		return "SIGINT"
	case syscall.SIGTERM:
		return "SIGTERM"
	}
	return sig.String()
}

// wakeRearmCommand is the form an orchestrator copies back: the same wake
// flags the child was started with, including --any only when it was asked.
func wakeRearmCommand(agents []string, timeoutMs int64, anyDone bool) string {
	args := append([]string{"soho", "wait"}, agents...)
	args = append(args, "--wake")
	if anyDone {
		args = append(args, "--any")
	}
	args = append(args, "--timeout", strconv.FormatInt(timeoutMs, 10))
	return strings.Join(args, " ")
}

// wakeRecordedAgents reads the agents named in one wake record through the
// confined root. It is deliberately lenient: an open, stat, identity or read
// failure means the membership is unknown, contributes no agents and never
// blocks the replacement of a record already proved to be this caller's.
func wakeRecordedAgents(dir *os.Root, name string, info os.FileInfo) []string {
	file, err := dir.Open(name)
	if err != nil {
		return nil
	}
	opened, statErr := file.Stat()
	if statErr != nil || !os.SameFile(info, opened) {
		_ = file.Close()
		return nil
	}
	data, readErr := io.ReadAll(file)
	_ = file.Close()
	if readErr != nil {
		return nil
	}
	agents := []string{}
	for _, agent := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		if agent != "" {
			agents = append(agents, agent)
		}
	}
	return agents
}

// unwatchedWakeAgents names the recorded agents of stopped wakes that the
// new wake does not cover and that the current roster still accepts (the
// same RosterLine lookup CmdWait uses to accept a name). Record order is kept
// and a name repeated across records counts once. It probes no status and
// claims nothing about whether an agent finished: an --add rearm is safe
// either way, since --add only adds and a finished wake exits at once.
func unwatchedWakeAgents(recorded, armed []string, sd string) []string {
	covered := make(map[string]bool, len(armed))
	for _, agent := range armed {
		covered[agent] = true
	}
	seen := make(map[string]bool)
	unwatched := []string{}
	for _, agent := range recorded {
		if covered[agent] || seen[agent] || core.RosterLine(sd, agent) == "" {
			continue
		}
		seen[agent] = true
		unwatched = append(unwatched, agent)
	}
	return unwatched
}
