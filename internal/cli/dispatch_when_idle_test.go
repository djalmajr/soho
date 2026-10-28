package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestDispatchWhenIdleSettlesBeforeSend(t *testing.T) {
	for _, state := range []string{"done", "idle"} {
		for _, noWait := range []bool{false, true} {
			t.Run(state+"/no_wait="+map[bool]string{true: "true", false: "false"}[noWait], func(t *testing.T) {
				f := newBusyDispatchFixture(t)
				t.Logf("fixture_root=%s", f.root)
				f.openPointer(t)
				rules := f.queuedRules()
				// Both a working poll and a settled re-read must precede typing.
				reads := []fakecli.Rule{}
				for i, s := range []string{"working", "working", state, state} {
					reads = append(reads, fakecli.Rule{Argv: []string{"agent", "get", "worker"}, Call: i + 1, Stdout: busyStateJSON(s, 1)})
				}
				for i := range rules {
					if reflect.DeepEqual(rules[i].Argv, []string{"agent", "get", "worker"}) {
						rules[i].Stdout = busyStateJSON("working", 2)
					}
				}
				f.install(t, append(reads, rules...))
				oldWait := dispatchWaitFor
				waited := false
				dispatchWaitFor = func(agents []string, sd string, ctx *core.Config, env platform.Env, timeout float64, any bool, cwd string) int {
					waited = true
					return 0
				}
				t.Cleanup(func() { dispatchWaitFor = oldWait })
				args := []string{"worker", f.brief, "--when-idle", "--timeout", "20s"}
				if noWait {
					args = append(args, "--no-wait")
				}
				code, out, stderr := f.run(t, args...)
				if code != 0 || dispatchOutputStatus(t, out) != "submitted" {
					t.Fatalf("code=%d out=%s stderr=%s", code, out, stderr)
				}
				want := "soho: dispatch: 'worker' is still working; waiting for it to finish before sending (--when-idle)\n"
				if stderr != want || waited == noWait {
					t.Fatalf("stderr=%q waited=%v noWait=%v", stderr, waited, noWait)
				}
				calls := f.calls(t)
				gets, prompts := 0, 0
				for _, c := range calls {
					if reflect.DeepEqual(c.Argv, []string{"agent", "get", "worker"}) {
						gets++
					}
					if len(c.Argv) >= 2 && reflect.DeepEqual(c.Argv[:2], []string{"agent", "prompt"}) {
						prompts++
						if gets != 4 {
							t.Fatalf("sent before settled re-read: gets=%d", gets)
						}
					}
				}
				if prompts != 1 {
					t.Fatalf("prompts=%d", prompts)
				}
			})
		}
	}
}

func TestDispatchWhenIdleTimeoutWritesNoTask(t *testing.T) {
	for _, pointer := range []bool{false, true} {
		t.Run(map[bool]string{false: "no_pointer", true: "open_task"}[pointer], func(t *testing.T) {
			f := newBusyDispatchFixture(t)
			t.Logf("fixture_root=%s", f.root)
			if pointer {
				f.openPointer(t)
			}
			before := snapshotTree(t, f.stateDir)
			f.install(t, []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: busyStateJSON("working", 1)}})
			code, out, stderr := f.run(t, "worker", f.brief, "--when-idle", "--no-wait", "--timeout", "2s")
			if code != 9 || out != "" || !strings.Contains(stderr, "dispatch: 'worker' was still working after ") || !strings.Contains(stderr, "; nothing was sent (--when-idle)") {
				t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
			}
			for _, c := range f.calls(t) {
				if len(c.Argv) > 1 && c.Argv[0] == "agent" && (c.Argv[1] == "prompt" || c.Argv[1] == "send-keys") {
					t.Fatalf("typed on timeout: %v", c.Argv)
				}
			}
			after := snapshotTree(t, f.stateDir)
			delete(after, "friction.log")
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("task state changed: before=%v after=%v", before, after)
			}
			if _, err := os.Stat(f.sidecarPath()); !os.IsNotExist(err) {
				t.Fatalf("sidecar: %v", err)
			}
		})
	}
}

func TestDispatchWhenIdleBlockedRefuses(t *testing.T) {
	for _, initiallyWorking := range []bool{false, true} {
		t.Run(map[bool]string{false: "initially blocked", true: "blocked during wait"}[initiallyWorking], func(t *testing.T) {
			f := newBusyDispatchFixture(t)
			t.Logf("fixture_root=%s", f.root)
			f.openPointer(t)
			rules := []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: busyStateJSON("blocked", 1)}}
			if initiallyWorking {
				rules = append([]fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: busyStateJSON("working", 1)}}, rules...)
			}
			f.install(t, rules)
			code, _, stderr := f.run(t, "worker", f.brief, "--when-idle", "--timeout", "2s")
			if code != 10 || strings.Contains(stderr, "waiting for it to finish") != initiallyWorking || !strings.Contains(stderr, "still working on its open task") {
				t.Fatalf("code=%d stderr=%q", code, stderr)
			}
			wantCalls := 1
			if initiallyWorking {
				wantCalls = 2
			}
			if len(f.calls(t)) != wantCalls {
				t.Fatalf("blocked pane was polled or typed: %v", f.calls(t))
			}
		})
	}
}

func TestDispatchWhenIdleUsesRecordedRoleTimeout(t *testing.T) {
	f := newBusyDispatchFixture(t)
	t.Logf("fixture_root=%s", f.root)
	if err := os.WriteFile(filepath.Join(f.env["SOHO_ROLES"], "slow.md"), []byte("---\nname: Slow\nmode: edit\ntimeout: 1000\n---\nRole\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f.env["SOHO_ROLE_SLOW_EFFORT"] = "max"
	f.openPointer(t)
	path := filepath.Join(f.stateDir, "task-report-worker.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var pointer map[string]any
	if err = json.Unmarshal(raw, &pointer); err != nil {
		t.Fatal(err)
	}
	pointer["role"] = "slow"
	raw, err = json.Marshal(pointer)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	// The third read would settle if dispatch used its much longer new-role
	// default. The old task's scaled 2000ms budget must win first.
	f.install(t, []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Call: 3, Stdout: busyStateJSON("idle", 1)}, {Argv: []string{"agent", "get", "worker"}, Stdout: busyStateJSON("working", 1)}})
	code, out, stderr := f.run(t, "worker", f.brief, "--when-idle", "--no-wait", "--role", "implementer")
	if code != 9 || out != "" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	if n := countArgvPrefix(f.calls(t), []string{"agent", "get", "worker"}); n != 2 {
		t.Fatalf("reads=%d; want initial + one poll within 2s recorded role budget", n)
	}
}

func TestDispatchWhenIdleAlreadySettled(t *testing.T) {
	for _, state := range []string{"idle", "done"} {
		t.Run(state, func(t *testing.T) {
			f := newBusyDispatchFixture(t)
			t.Logf("fixture_root=%s", f.root)
			f.openPointer(t)
			rules := f.queuedRules()
			for i := range rules {
				if reflect.DeepEqual(rules[i].Argv, []string{"agent", "get", "worker"}) {
					rules[i].Stdout = busyStateJSON("working", 2)
				}
			}
			f.install(t, append([]fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: busyStateJSON(state, 1)}}, rules...))
			code, out, stderr := f.run(t, "worker", f.brief, "--when-idle", "--no-wait")
			if code != 0 || stderr != "" || dispatchOutputStatus(t, out) != "submitted" {
				t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
			}
			gets := 0
			for _, c := range f.calls(t) {
				if reflect.DeepEqual(c.Argv, []string{"agent", "get", "worker"}) {
					gets++
				}
				if len(c.Argv) > 1 && c.Argv[0] == "agent" && c.Argv[1] == "prompt" && gets != 1 {
					t.Fatalf("unnecessary pre-send polls=%d", gets)
				}
			}
		})
	}
}

func TestDispatchWhenIdleConflictingFlags(t *testing.T) {
	for _, flag := range []string{"--amend", "--queue"} {
		t.Run(flag, func(t *testing.T) {
			f := newBusyDispatchFixture(t)
			t.Logf("fixture_root=%s", f.root)
			f.install(t, nil)
			code, _, stderr := f.run(t, "worker", f.brief, "--when-idle", flag)
			if code != 2 || !strings.Contains(stderr, "--when-idle") || !strings.Contains(stderr, flag) {
				t.Fatalf("code=%d stderr=%q", code, stderr)
			}
			if _, err := os.Stat(filepath.Join(f.bin, "herdr.calls.jsonl")); !os.IsNotExist(err) {
				t.Fatalf("usage called Herdr: %v", err)
			}
		})
	}
}

// The confirming read after the pane settles is bounded by the remaining
// budget: a slow read past the deadline exits 9 with nothing sent.
func TestDispatchWhenIdleConfirmationKeepsTheDeadline(t *testing.T) {
	f := newBusyDispatchFixture(t)
	f.openPointer(t)
	f.env["SOHO_WAIT_POLL_MS"] = "1"
	oldNow := platform.Now
	base, start := time.Date(2026, 10, 3, 15, 26, 48, 0, time.UTC), time.Now()
	platform.Now = func() time.Time { return base.Add(time.Since(start)) }
	t.Cleanup(func() { platform.Now = oldNow })
	rules := []fakecli.Rule{
		{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: busyStateJSON("working", 1)},
		{Argv: []string{"agent", "get", "worker"}, Call: 2, Stdout: busyStateJSON("done", 1)},
		{Argv: []string{"agent", "get", "worker"}, Call: 3, Delay: 600, Stdout: busyStateJSON("done", 1)},
		{Argv: []string{"agent", "get", "worker"}, Stdout: busyStateJSON("working", 2)},
	}
	f.install(t, append(rules, f.queuedRules()...))
	before := snapshotTree(t, f.stateDir)
	code, out, stderr := f.run(t, "worker", f.brief, "--when-idle", "--no-wait", "--timeout", "200ms")
	after := snapshotTree(t, f.stateDir)
	delete(after, "friction.log")
	if prompts := countArgvPrefix(f.calls(t), []string{"agent", "prompt"}); code != 9 || prompts != 0 || !reflect.DeepEqual(before, after) {
		t.Fatalf("code=%d prompts=%d state_unchanged=%v stdout=%q stderr=%q", code, prompts, reflect.DeepEqual(before, after), out, stderr)
	}
}
