package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// Separate processes keep Run's command-local globals separate, as in use.
func TestHcLifecycleCommandHelper(t *testing.T) {
	if os.Getenv("TEST_HC_LIFECYCLE_ROOT") == "" {
		return
	}
	if err := os.Chdir(os.Getenv("TEST_HC_LIFECYCLE_ROOT")); err != nil {
		t.Fatal(err)
	}
	var args []string
	if err := json.Unmarshal([]byte(os.Getenv("TEST_HC_LIFECYCLE_ARGS")), &args); err != nil {
		t.Fatal(err)
	}
	if args[0] == "release" {
		workerLifecycleLock = func(sd, agent string) (func(), error) {
			unlock, err := core.TryLockWorkerLifecycle(sd, agent)
			state := "acquired"
			if errors.Is(err, core.ErrWorkerLifecycleBusy) {
				state = "busy"
			}
			if writeErr := os.WriteFile(os.Getenv("TEST_HC_LIFECYCLE_LOCK"), []byte(state), 0o600); writeErr != nil {
				if unlock != nil {
					unlock()
				}
				return nil, writeErr
			}
			if errors.Is(err, core.ErrWorkerLifecycleBusy) {
				return core.LockWorkerLifecycle(sd, agent)
			}
			return unlock, err
		}
	} else {
		held := false
		workerLifecycleLock = func(sd, agent string) (func(), error) {
			unlock, err := core.LockWorkerLifecycle(sd, agent)
			if err != nil {
				return nil, err
			}
			held = true
			return func() { held = false; unlock() }, nil
		}
		dispatchWaitFor = func(_ []string, sd string, _ *core.Config, _ platform.Env, _ float64, _ bool, _ string) int {
			if gate := os.Getenv("TEST_HC_LIFECYCLE_WAIT_GATE"); gate != "" {
				if err := os.WriteFile(gate+".entered", nil, 0o600); err != nil {
					t.Fatal(err)
				}
				awaitLifecycle(t, func() bool { _, err := os.Stat(gate); return err == nil })
				return 0
			}
			if held {
				t.Fatal("dispatch holds lifecycle lock across report wait")
			}
			return 0
		}
	}
	env := platform.EnvFromOS()
	env["SOHO_TRANSPORT"], env["HC_TERMINAL_ID"] = "hc", "t1"
	env["SOHO_FAKECLI_CONFIG"] = os.Getenv("TEST_HC_FAKECLI_CONFIG")
	fmt.Printf("lifecycle-code=%d\n", Run(args, env))
}

type lifecycleCommand struct {
	done chan struct{}
	out  bytes.Buffer
	err  error
	cmd  *exec.Cmd
}

func startLifecycleCommand(t *testing.T, f *dispatchHcFixture, args ...string) *lifecycleCommand {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	cmd := exec.CommandContext(ctx, self, "-test.run=^TestHcLifecycleCommandHelper$", "-test.count=1")
	env := f.env.Clone()
	env["TEST_HC_FAKECLI_CONFIG"] = env["SOHO_FAKECLI_CONFIG"]
	delete(env, "SOHO_FAKECLI_CONFIG")
	encoded, _ := json.Marshal(args)
	env["TEST_HC_LIFECYCLE_ROOT"] = f.root
	env["TEST_HC_LIFECYCLE_ARGS"] = string(encoded)
	env["TEST_HC_LIFECYCLE_LOCK"] = filepath.Join(f.root, "release-lock")
	cmd.Env = env.List()
	data, err := os.ReadFile(filepath.Join(f.bin, "hc.json"))
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	var script struct {
		Rules []fakecli.Rule `json:"rules"`
	}
	if err := json.Unmarshal(data, &script); err != nil {
		cancel()
		t.Fatal(err)
	}
	var gates []string
	for _, rule := range script.Rules {
		if rule.WaitFile != "" {
			gates = append(gates, rule.WaitFile)
		}
	}
	if gate := env.Get("TEST_HC_LIFECYCLE_WAIT_GATE"); gate != "" {
		gates = append(gates, gate)
	}
	openGates := func() error {
		var errs []error
		for _, gate := range gates {
			if err := os.WriteFile(gate, nil, 0o600); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}
	// Cancellation (including the deadline) first lets fake Core return.
	// The helper then reaps its children through the ordinary synchronous
	// runner. WaitDelay kills this owned helper only if it cannot finish
	// within the grace period, and bounds waiting for its output pipes.
	cmd.Cancel = openGates
	cmd.WaitDelay = 5 * time.Second
	c := &lifecycleCommand{done: make(chan struct{}), cmd: cmd}
	cmd.Stdout, cmd.Stderr = &c.out, &c.out
	go func() { c.err = cmd.Run(); close(c.done) }()
	t.Cleanup(func() {
		if err := openGates(); err != nil {
			t.Error(err)
		}
		cancel()
		<-c.done
	})
	return c
}

func awaitLifecycle(t *testing.T, predicate func() bool) {
	t.Helper()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	for !predicate() {
		select {
		case <-ticker.C: // Poll a causal file/call barrier, never infer progress from elapsed time.
		case <-deadline.C:
			t.Fatal("lifecycle barrier was not reached")
		}
	}
}

func TestReleaseHcConcurrentDispatchDoesNotCloseWorkingWorker(t *testing.T) {
	f := newDispatchHcFixture(t, nil)
	f.env["SOHO_PROMPT_SETTLE_SECONDS"] = "0"
	row := core.RosterLine(f.sd, "worker")
	row = strings.Replace(row, "\topenai\t0\t", "\topenai\t1\t", 1)
	if err := os.WriteFile(filepath.Join(f.sd, "agents.tsv"), []byte(row+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldReport := filepath.Join(f.sd, "reports", "old.md")
	writeTaskPointer(t, f.sd, "worker", oldReport, oldReport)
	if err := os.WriteFile(core.LastReportPath(f.sd, "worker"), []byte(oldReport), 0o600); err != nil {
		t.Fatal(err)
	}
	sendGate, releaseGate := filepath.Join(f.root, "send-go"), filepath.Join(f.root, "release-go")
	rules := append(guardProbeRules(),
		fakecli.Rule{Argv: []string{"get", "t1", "--json"}, ArgvPrefix: true, Stdout: hcDispatchCaller},
		fakecli.Rule{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Call: 1, Stdout: hcWorkerGet(5, "idle")},
		fakecli.Rule{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(5, "idle"), WaitFile: releaseGate},
		fakecli.Rule{Argv: []string{"get", "t7", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(5, "idle")},
		fakecli.Rule{Argv: []string{"ls"}, ArgvPrefix: true, Stdout: hcDispatchSessionLS},
		fakecli.Rule{Argv: []string{"send", "--if", "idle", "--json", "--", "worker"}, ArgvPrefix: true, WaitFile: sendGate, Stdout: `{"started":true,"turn":6}`},
		fakecli.Rule{Argv: []string{"kill"}, ArgvPrefix: true, Stdout: `{}`},
		fakecli.Rule{Argv: []string{"rm"}, ArgvPrefix: true, Stdout: `{}`},
	)
	writeLifecycleRules(t, f.bin, rules)
	dispatch := startLifecycleCommand(t, f, "dispatch", "worker", f.brief)
	awaitLifecycle(t, func() bool {
		select {
		case <-dispatch.done:
			t.Fatalf("dispatch ended before send: %v\n%s\ncalls=%#v", dispatch.err, dispatch.out.String(), f.hcCalls(t))
		default:
		}
		return len(hcSends(f.hcCalls(t))) == 1
	})
	release := startLifecycleCommand(t, f, "release", "worker", "--close")
	marker := filepath.Join(f.root, "release-lock")
	awaitLifecycle(t, func() bool { _, err := os.Stat(marker); return err == nil })
	lockState, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if string(lockState) == "acquired" {
		// Without dispatch's guard, release has entered and loaded its stale
		// idle snapshot. Keep that process paused until the send completes.
		awaitLifecycle(t, func() bool {
			return len(hcCallIndexes(f.hcCalls(t), []string{"get", "worker"})) >= 2
		})
	}
	// The fake Core now answers working. A get already in flight retains
	// its old idle reply, just like the model's stale release observation.
	for i := range rules {
		if len(rules[i].Argv) > 1 && rules[i].Argv[0] == "get" && rules[i].Argv[1] == "worker" {
			rules[i].Stdout = hcWorkerGet(6, "working")
		}
	}
	writeLifecycleRules(t, f.bin, rules)
	if err := os.WriteFile(sendGate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	awaitLifecycle(t, func() bool {
		select {
		case <-dispatch.done:
			return true
		default:
			return false
		}
	})
	if dispatch.err != nil || !strings.Contains(dispatch.out.String(), "lifecycle-code=0") {
		t.Fatalf("dispatch: %v\n%s", dispatch.err, dispatch.out.String())
	}
	if err := os.WriteFile(releaseGate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	awaitLifecycle(t, func() bool {
		select {
		case <-release.done:
			return true
		default:
			return false
		}
	})
	if release.err != nil || !strings.Contains(release.out.String(), "lifecycle-code=3") || !strings.Contains(release.out.String(), "is still working and has not written") {
		t.Fatalf("release closed a dispatched working worker: %v\n%s", release.err, release.out.String())
	}
	for _, verb := range []string{"kill", "rm"} {
		if len(hcCallIndexes(f.hcCalls(t), []string{verb})) != 0 {
			t.Fatalf("%s reached a working worker", verb)
		}
	}
	if core.RosterLine(f.sd, "worker") == "" || core.LastReport(f.sd, "worker") == "" {
		t.Fatal("release refusal lost roster or task metadata")
	}
	locks, err := filepath.Glob(filepath.Join(f.sd, "worker-*.lock"))
	if err != nil || len(locks) != 0 {
		t.Fatalf("lifecycle locks left behind: %v %v", locks, err)
	}
	f.assertHcNeverTouchedHerdr(t)
	// Force still closes the very same working worker unconditionally.
	getsBeforeForce := len(hcCallIndexes(f.hcCalls(t), []string{"get", "worker"}))
	rf := &releaseHcFixture{root: f.root, state: f.state, sd: f.sd, bin: f.bin, env: f.env, ctx: f.ctx}
	code, out, errText := rf.run(t, "worker", "--close", "--force")
	if code != 0 || !strings.Contains(out, "closed terminal t7") {
		t.Fatalf("forced close: code=%d out=%s stderr=%s", code, out, errText)
	}
	if len(hcCallIndexes(f.hcCalls(t), []string{"get", "worker"})) != getsBeforeForce {
		t.Fatal("force queried worker status")
	}
	for _, verb := range []string{"kill", "rm"} {
		if len(hcCallIndexes(f.hcCalls(t), []string{verb})) != 1 {
			t.Fatalf("force did not call %s once", verb)
		}
	}
}

func writeLifecycleRules(t *testing.T, bin string, rules []fakecli.Rule) {
	t.Helper()
	config := filepath.Join(bin, "hc.json")
	data, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	var script map[string]json.RawMessage
	if err := json.Unmarshal(data, &script); err != nil {
		t.Fatal(err)
	}
	script["rules"], err = json.Marshal(rules)
	if err != nil {
		t.Fatal(err)
	}
	data, err = json.Marshal(script)
	if err != nil {
		t.Fatal(err)
	}
	if err := platform.AtomicWrite(config, string(data)); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseHcConcurrentReleasesCloseOnce(t *testing.T) {
	rf := newReleaseHcFixture(t, nil)
	f := &dispatchHcFixture{root: rf.root, sd: rf.sd, bin: rf.bin, env: rf.env}
	addReleaseHcReport(t, rf, false)
	gate := filepath.Join(f.root, "close-go")
	writeLifecycleRules(t, f.bin, []fakecli.Rule{
		{Argv: []string{"get", "t1", "--json"}, ArgvPrefix: true, Stdout: hcReleaseCaller},
		{Argv: []string{"kill", "t7"}, ArgvPrefix: true, WaitFile: gate, Stdout: `{}`},
		{Argv: []string{"rm", "t7"}, ArgvPrefix: true, Stdout: `{}`},
		{Argv: []string{"ls"}, ArgvPrefix: true, Stdout: `{"terminals":[]}`},
	})
	first := startLifecycleCommand(t, f, "release", "worker", "--close")
	awaitLifecycle(t, func() bool { return len(hcCallIndexes(f.hcCalls(t), []string{"kill"})) == 1 })
	second := startLifecycleCommand(t, f, "release", "worker", "--close")
	awaitLifecycle(t, func() bool {
		data, _ := os.ReadFile(filepath.Join(f.root, "release-lock"))
		select {
		case <-second.done:
			return true
		default:
		}
		return string(data) == "busy" || len(hcCallIndexes(f.hcCalls(t), []string{"kill"})) > 1
	})
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	awaitLifecycle(t, func() bool {
		select {
		case <-first.done:
			return true
		default:
			return false
		}
	})
	awaitLifecycle(t, func() bool {
		select {
		case <-second.done:
			return true
		default:
			return false
		}
	})
	if first.err != nil || !strings.Contains(first.out.String(), "lifecycle-code=0") || second.err != nil || !strings.Contains(second.out.String(), "lifecycle-code=3") || !strings.Contains(second.out.String(), "is not in the roster") {
		t.Fatalf("first: %v %s\nsecond: %v %s", first.err, first.out.String(), second.err, second.out.String())
	}
	for _, verb := range []string{"kill", "rm"} {
		if calls := hcCallIndexes(f.hcCalls(t), []string{verb}); len(calls) != 1 {
			t.Fatalf("%s calls=%v", verb, calls)
		}
	}
	locks, err := filepath.Glob(filepath.Join(f.sd, "worker-*.lock"))
	if err != nil || len(locks) != 0 {
		t.Fatalf("locks left: %v %v", locks, err)
	}
}

func TestDispatchHcDuplicateWaitAllowsForcedRelease(t *testing.T) {
	f := newDispatchHcFixture(t, nil)
	f.env["SOHO_PROMPT_SETTLE_SECONDS"] = "0"
	row := strings.Replace(core.RosterLine(f.sd, "worker"), "\topenai\t0\t", "\topenai\t1\t", 1)
	if err := os.WriteFile(filepath.Join(f.sd, "agents.tsv"), []byte(row+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeLifecycleRules(t, f.bin, append(guardProbeRules(),
		fakecli.Rule{Argv: []string{"get", "t1"}, ArgvPrefix: true, Stdout: hcDispatchCaller},
		fakecli.Rule{Argv: []string{"get", "worker"}, ArgvPrefix: true, Stdout: hcWorkerGet(5, "idle")},
		fakecli.Rule{Argv: []string{"ls"}, ArgvPrefix: true, Stdout: hcDispatchSessionLS},
		fakecli.Rule{Argv: []string{"send"}, ArgvPrefix: true, Stdout: `{"started":true,"turn":6}`},
		fakecli.Rule{Argv: []string{"kill", "t7"}, ArgvPrefix: true, Stdout: `{}`},
		fakecli.Rule{Argv: []string{"rm", "t7"}, ArgvPrefix: true, Stdout: `{}`},
	))
	first := startLifecycleCommand(t, f, "dispatch", "worker", f.brief, "--no-wait")
	awaitLifecycle(t, func() bool {
		select {
		case <-first.done:
			return true
		default:
			return false
		}
	})
	if first.err != nil || !strings.Contains(first.out.String(), "lifecycle-code=0") {
		t.Fatalf("initial dispatch: %v\n%s", first.err, first.out.String())
	}
	gate := filepath.Join(f.root, "report-wait-go")
	f.env["TEST_HC_LIFECYCLE_WAIT_GATE"] = gate
	// Even the red/mutated tree must let the duplicate observer finish.
	t.Cleanup(func() {
		if err := os.WriteFile(gate, nil, 0o600); err != nil {
			t.Error(err)
		}
	})
	duplicate := startLifecycleCommand(t, f, "dispatch", "worker", f.brief)
	awaitLifecycle(t, func() bool { _, err := os.Stat(gate + ".entered"); return err == nil })
	release := startLifecycleCommand(t, f, "release", "worker", "--close", "--force")
	awaitLifecycle(t, func() bool {
		select {
		case <-release.done:
			return true
		default:
			return false
		}
	})
	if release.err != nil || !strings.Contains(release.out.String(), "lifecycle-code=0") || !strings.Contains(release.out.String(), "closed terminal t7") {
		t.Fatalf("release blocked by duplicate report wait: %v\n%s", release.err, release.out.String())
	}
	select {
	case <-duplicate.done:
		t.Fatal("duplicate did not remain in its report wait")
	default:
	}
	if core.RosterLine(f.sd, "worker") != "" {
		t.Fatal("forced release retained worker")
	}
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	awaitLifecycle(t, func() bool {
		select {
		case <-duplicate.done:
			return true
		default:
			return false
		}
	})
	if duplicate.err != nil || !strings.Contains(duplicate.out.String(), `"duplicate_of":`) || !strings.Contains(duplicate.out.String(), "lifecycle-code=0") {
		t.Fatalf("duplicate: %v\n%s", duplicate.err, duplicate.out.String())
	}
	if len(hcSends(f.hcCalls(t))) != 1 {
		t.Fatal("duplicate resent its brief")
	}
	for _, verb := range []string{"kill", "rm"} {
		if len(hcCallIndexes(f.hcCalls(t), []string{verb})) != 1 {
			t.Fatalf("%s was not called exactly once", verb)
		}
	}
	f.assertHcNeverTouchedHerdr(t)
}

func TestReleaseHcLifecycleCleanupFailureHelper(t *testing.T) {
	marker := os.Getenv("TEST_HC_LIFECYCLE_CLEANUP_MARKER")
	if marker == "" {
		return
	}
	rf := newReleaseHcFixture(t, nil)
	f := &dispatchHcFixture{root: rf.root, sd: rf.sd, bin: rf.bin, env: rf.env}
	gate := filepath.Join(f.root, "close-go")
	otherGate := filepath.Join(f.root, "unused-send-go")
	writeLifecycleRules(t, f.bin, []fakecli.Rule{
		{Argv: []string{"get", "t1"}, ArgvPrefix: true, Stdout: hcReleaseCaller},
		{Argv: []string{"kill", "t7"}, ArgvPrefix: true, WaitFile: gate, Stdout: `{}`},
		{Argv: []string{"rm", "t7"}, ArgvPrefix: true, Stdout: `{}`},
		{Argv: []string{"send"}, ArgvPrefix: true, WaitFile: otherGate},
	})
	var child *lifecycleCommand
	t.Cleanup(func() {
		// Rescue a broken cleanup in the red/mutation run as well.
		defer func() {
			for _, path := range []string{gate, otherGate} {
				if err := os.WriteFile(path, nil, 0o600); err != nil {
					t.Error(err)
				}
			}
		}()
		for _, path := range []string{gate, otherGate} {
			if _, err := os.Stat(path); err != nil {
				t.Errorf("cleanup did not open gate: %v", err)
				return
			}
		}
		select {
		case <-child.done:
		default:
			t.Error("cleanup did not await helper")
			return
		}
		if child.cmd.ProcessState == nil || !child.cmd.ProcessState.Success() || !strings.Contains(child.out.String(), "lifecycle-code=0") {
			t.Errorf("helper killed before reaping gated Core child: %v\n%s", child.err, child.out.String())
			return
		}
		for _, verb := range []string{"kill", "rm"} {
			if len(hcCallIndexes(f.hcCalls(t), []string{verb})) != 1 {
				t.Errorf("cleanup did not complete %s", verb)
				return
			}
		}
		if err := os.WriteFile(marker, []byte("gates opened; Core calls completed; helper reaped"), 0o600); err != nil {
			t.Error(err)
		}
	})
	child = startLifecycleCommand(t, f, "release", "worker", "--close", "--force")
	awaitLifecycle(t, func() bool { return len(hcCallIndexes(f.hcCalls(t), []string{"kill"})) == 1 })
	t.Fatal("intentional assertion failure with gated Core child")
}

func TestReleaseHcLifecycleCleanupOnFailure(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "cleanup-complete")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, self, "-test.run=^TestReleaseHcLifecycleCleanupFailureHelper$", "-test.count=1")
	cmd.Env = (platform.Env{"HOME": root, "USERPROFILE": root, "XDG_CONFIG_HOME": root, "TMPDIR": root, "PATH": root,
		"TEST_HC_LIFECYCLE_CLEANUP_MARKER": marker, "GORACE": fakecli.RaceOptions(testutil.CleanEnv(t))}).List()
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "intentional assertion failure with gated Core child") {
		t.Fatalf("expected assertion failure: %v\n%s", err, output)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "gates opened; Core calls completed; helper reaped" {
		t.Fatalf("failure cleanup was incomplete: %v\n%s", err, output)
	}
}
