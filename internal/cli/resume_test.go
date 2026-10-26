package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/taskreport"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func resumeRun(t *testing.T, f *busyDispatchFixture, args ...string) (int, string, string) {
	t.Helper()
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, errOut bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &errOut
	defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
	code := Run(args, f.env)
	return code, out.String(), errOut.String()
}
func resumeSeed(t *testing.T, f *busyDispatchFixture, agent, stamp string, reported bool) string {
	t.Helper()
	report := filepath.Join(f.stateDir, "reports", agent+"-"+stamp+".md")
	brief := filepath.Join(f.stateDir, "briefs", agent+"-"+stamp+".md")
	for _, dir := range []string{filepath.Dir(report), filepath.Dir(brief)} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	pointer, _ := json.Marshal(map[string]any{"version": 1, "current": report, "task_report": strings.TrimSuffix(report, ".md") + ".current.md", "history": []any{}})
	for p, body := range map[string]string{taskreport.TaskReportPointerPath(f.stateDir, agent): string(pointer), filepath.Join(f.stateDir, "last-report-"+agent): report + "\n", brief: "# Original brief\nKeep the worktree.\n"} {
		if err := os.WriteFile(p, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if reported {
		if err := os.WriteFile(report, []byte("done\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return brief
}
func resumeFixture(t *testing.T, state string) (*busyDispatchFixture, time.Time) {
	t.Helper()
	f := newBusyDispatchFixture(t)
	// Dispatch stamps and boot fixtures use local time. Keep the current
	// clock local too: 13:00 after-boot tasks must already exist at 15:26.
	priorNow := platform.Now
	clock := time.Date(2026, 10, 3, 15, 26, 48, 0, time.Local)
	platform.Now = func() time.Time { now := clock; clock = clock.Add(500 * time.Millisecond); return now }
	t.Cleanup(func() { platform.Now = priorNow })
	f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
	f.env["SOHO_CALLER_CHECK"] = "off"
	f.install(t, []fakecli.Rule{{Argv: []string{"agent", "get", "worker"}, Stdout: busyStateJSON(state, 1)}, {Argv: []string{"agent", "list"}, ArgvPrefix: true, Stdout: `{"result":{"agents":[]}}`}, {Argv: []string{"agent", "prompt"}, ArgvPrefix: true, Stdout: `{"result":{"sent":true}}`}, {AnyArgs: true, Stdout: `{"result":{}}`}})
	boot := time.Date(2026, 10, 3, 12, 0, 0, 0, time.Local)
	original := platform.BootTime
	platform.BootTime = func() (time.Time, error) { return boot, nil }
	t.Cleanup(func() { platform.BootTime = original })
	return f, boot
}
func resumePrompts(t *testing.T, f *busyDispatchFixture) []fakecli.Call {
	t.Helper()
	calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	prompts := []fakecli.Call{}
	for _, call := range calls {
		if len(call.Argv) >= 2 && call.Argv[0] == "agent" && call.Argv[1] == "prompt" {
			prompts = append(prompts, call)
		}
	}
	return prompts
}

func TestResumeStatusReusesHerdrState(t *testing.T) {
	f, boot := resumeFixture(t, "idle")
	resumeSeed(t, f, "worker", "20261003T100000", false)
	platform.BootTime = func() (time.Time, error) { return time.Time{}, errors.New("unknown") }
	code, out, msg := resumeRun(t, f, "status", "worker")
	if code != 0 || !strings.Contains(out, "\tno-report-yet\t") {
		t.Fatalf("baseline=%d out=%s stderr=%s", code, out, msg)
	}
	calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"agent", "get", "worker"}, {"agent", "read", "worker", "--source", "visible", "--lines", "20"}, {"agent", "read", "worker", "--source", "recent-unwrapped", "--lines", "40"}}
	var baseline [][]string
	for _, call := range calls {
		baseline = append(baseline, call.Argv)
	}
	if !reflect.DeepEqual(baseline, want) {
		t.Fatalf("baseline calls=%v want=%v", baseline, want)
	}
	platform.BootTime = func() (time.Time, error) { return boot, nil }
	code, out, msg = resumeRun(t, f, "status", "worker")
	calls, err = fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var actual [][]string
	for _, call := range calls[len(baseline):] {
		actual = append(actual, call.Argv)
	}
	if code != 0 || !strings.Contains(out, "\tno-report-yet\t") || !strings.Contains(msg, "run soho resume worker") || !reflect.DeepEqual(actual, want) {
		t.Fatalf("restart status=%d out=%s stderr=%s calls=%v want=%v", code, out, msg, actual, want)
	}
	t.Logf("interrupted missing report: status argv=%v; exactly one agent get, identical to baseline", actual)
}
func TestResumeRestartCases(t *testing.T) {
	for _, name := range []string{"idle", "done", "after-boot", "working", "blocked", "reported", "not-open", "dry-run", "unknown-boot"} {
		t.Run(name, func(t *testing.T) {
			state := "idle"
			if name == "working" || name == "blocked" || name == "done" {
				state = name
			}
			f, boot := resumeFixture(t, state)
			stamp := "20261003T100000"
			if name == "after-boot" {
				stamp = "20261003T130000"
			}
			brief := resumeSeed(t, f, "worker", stamp, name == "reported")
			if name == "not-open" {
				if err := taskreport.RetireTaskReportPointer(f.stateDir, "worker"); err != nil {
					t.Fatal(err)
				}
			}
			if name == "unknown-boot" {
				platform.BootTime = func() (time.Time, error) { return time.Time{}, errors.New("unavailable") }
			}
			expectedNotice := name == "idle" || name == "done" || name == "dry-run"
			notice := fmt.Sprintf("'worker': its task was dispatched before the machine restarted (%s); run soho resume worker", boot.Local().Format(time.RFC3339))
			_, _, statusErr := resumeRun(t, f, "status", "worker")
			if strings.Contains(statusErr, notice) != expectedNotice {
				t.Fatalf("status notice=%v stderr=%s", expectedNotice, statusErr)
			}
			_, doctorOut, doctorErr := resumeRun(t, f, "doctor")
			if strings.Contains(doctorOut, notice) || strings.Contains(doctorErr, notice) != expectedNotice {
				t.Fatalf("doctor notice=%v stdout=%s stderr=%s", expectedNotice, doctorOut, doctorErr)
			}
			args := []string{"resume", "worker"}
			if name == "dry-run" {
				args = append(args, "--dry-run")
			}
			code, out, stderr := resumeRun(t, f, args...)
			wantCode := 0
			switch name {
			case "after-boot", "reported", "not-open":
				wantCode = 2
			case "working", "blocked":
				wantCode = 10
			case "unknown-boot":
				wantCode = 4
			}
			if code != wantCode {
				t.Fatalf("exit=%d want=%d stdout=%s stderr=%s", code, wantCode, out, stderr)
			}
			var results []resumeResult
			if err := json.Unmarshal([]byte(out), &results); err != nil {
				t.Fatalf("JSON=%s err=%v", out, err)
			}
			prompts := resumePrompts(t, f)
			wantPrompts := 0
			if name == "idle" || name == "done" {
				wantPrompts = 1
			}
			if len(prompts) != wantPrompts {
				t.Fatalf("prompts=%v want=%d", prompts, wantPrompts)
			}
			if wantPrompts == 1 {
				if len(results) != 1 || results[0].Status != "resumed" {
					t.Fatalf("results=%v", results)
				}
				text := prompts[0].Argv[3]
				path := strings.TrimPrefix(text, "Read the file ")
				path, _, ok := strings.Cut(path, " in full")
				if !ok {
					t.Fatalf("prompt=%s", text)
				}
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				note := "The machine restarted while you worked on this task: your processes died and /tmp was wiped. Do not revert the worktree. Re-create any temporary artifacts you need, check what is already done, and continue the same task. The original brief is " + brief + "; write your report to the report path this message gives."
				if !strings.Contains(string(data), note) || !strings.Contains(text, "It amends the brief") {
					t.Fatalf("missing exact recovery amendment: prompt=%s body=%s", text, data)
				}
				pointer := taskreport.ReadTaskReportPointer(f.stateDir, "worker")
				stable, _ := pointer.Get("task_report")
				current, _ := pointer.Get("current")
				wantStable := filepath.Join(f.stateDir, "reports", "worker-"+stamp+".current.md")
				if stable != wantStable {
					t.Fatalf("durable task report changed: %v want %s", stable, wantStable)
				}
				t.Logf("existing amendment behavior: durable=%s old current=%s new current=%v", wantStable, f.openReport, current)
			}
			if name == "dry-run" && (len(results) != 1 || results[0].Status != "would-resume" || !strings.Contains(results[0].Reason, brief)) {
				t.Fatalf("dry-run=%v", results)
			}
			t.Logf("case=%s notice=%v exit=%d prompts=%d JSON=%s", name, expectedNotice, code, len(prompts), out)
		})
	}
}
func TestResumeAllMix(t *testing.T) {
	f, _ := resumeFixture(t, "idle")
	raw, err := os.ReadFile(filepath.Join(f.stateDir, "agents.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	row := lines[1]
	for _, agent := range []string{"late", "busy", "reported"} {
		raw = append(raw, []byte(strings.Replace(row, "worker", agent, -1)+"\n")...)
	}
	if err = os.WriteFile(filepath.Join(f.stateDir, "agents.tsv"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, agent := range []string{"worker", "late", "busy", "reported"} {
		stamp := "20261003T100000"
		if agent == "late" {
			stamp = "20261003T130000"
		}
		resumeSeed(t, f, agent, stamp, agent == "reported")
	}
	f.bin = filepath.Join(f.root, "mix-bin")
	if err := os.Mkdir(f.bin, 0700); err != nil {
		t.Fatal(err)
	}
	f.env["PATH"], f.env["SOHO_FAKECLI_CONFIG"] = f.bin, f.bin
	f.install(t, []fakecli.Rule{{Argv: []string{"agent", "get", "busy"}, Stdout: busyStateJSON("working", 1)}, {Argv: []string{"agent", "get"}, ArgvPrefix: true, Stdout: busyStateJSON("idle", 1)}, {Argv: []string{"agent", "prompt"}, ArgvPrefix: true, Stdout: `{"result":{"sent":true}}`}, {Argv: []string{"agent", "list"}, ArgvPrefix: true, Stdout: `{"result":{"agents":[]}}`}, {AnyArgs: true, Stdout: `{"result":{}}`}})
	code, out, stderr := resumeRun(t, f, "resume", "--all")
	if code != 0 {
		t.Fatalf("exit=%d out=%s err=%s", code, out, stderr)
	}
	var results []resumeResult
	if err = json.Unmarshal([]byte(out), &results); err != nil || len(results) != 4 {
		t.Fatalf("results=%s err=%v", out, err)
	}
	for _, result := range results {
		want := "skipped"
		if result.Agent == "worker" {
			want = "resumed"
		}
		if result.Status != want {
			t.Fatalf("result=%v", result)
		}
	}
	prompts := resumePrompts(t, f)
	if len(prompts) != 1 || prompts[0].Argv[2] != "worker" {
		t.Fatalf("prompts=%v", prompts)
	}
	if strings.Count(stderr, "soho: resume:") != 3 {
		t.Fatalf("skip lines=%s", stderr)
	}
	t.Logf("exit=%d prompts=%d JSON=%s stderr=%s", code, len(prompts), out, stderr)
}
func TestResumeRestartGoneProcNotice(t *testing.T) {
	f, boot := resumeFixture(t, "idle")
	if _, err := fakecli.Install(t, f.bin, "ps", []fakecli.Rule{{AnyArgs: true, Code: 1}}); err != nil {
		t.Fatal(err)
	}
	started := boot.Add(-time.Hour).Format(time.ANSIC)
	if runtime.GOOS == "windows" {
		started = strconv.FormatInt(boot.Add(-time.Hour).Unix()*10000000+116444736000000000, 10)
	}
	row := core.ProcRow{Pid: 2147483647, Started: started, Name: "fixture-worker", Owner: "worker", Pane: "-", Created: boot.Add(-time.Hour).Format(time.RFC3339)}
	if err := core.WriteProcs(f.stateDir, []core.ProcRow{row}); err != nil {
		t.Fatal(err)
	}
	want := "proc 2147483647 of 'worker' (fixture-worker) ended with the restart"
	_, _, stderr := resumeRun(t, f, "status", "worker")
	if !strings.Contains(stderr, want) {
		t.Fatalf("status=%s", stderr)
	}
	_, out, errOut := resumeRun(t, f, "doctor")
	if strings.Contains(out, want) || !strings.Contains(errOut, want) {
		t.Fatalf("doctor=%s stderr=%s", out, errOut)
	}
	t.Log(want)
}

func TestResumeRefusesBeforeSending(t *testing.T) {
	for _, name := range []string{"after-boot", "working", "blocked"} {
		t.Run(name, func(t *testing.T) {
			state := "idle"
			if name != "after-boot" {
				state = name
			}
			f, _ := resumeFixture(t, state)
			stamp := "20261003T100000"
			want := 10
			if name == "after-boot" {
				stamp = "20261003T130000"
				want = 2
			}
			resumeSeed(t, f, "worker", stamp, false)
			before := snapshotTree(t, f.stateDir)
			code, out, stderr := resumeRun(t, f, "resume", "worker")
			prompts := resumePrompts(t, f)
			if code != want || len(prompts) != 0 {
				t.Fatalf("exit=%d want=%d prompts=%d out=%s stderr=%s", code, want, len(prompts), out, stderr)
			}
			if !reflect.DeepEqual(before, snapshotTree(t, f.stateDir)) { // The standard refusal may append friction; task artifacts must still remain.
				for path, hash := range before {
					if strings.HasSuffix(path, "friction.log") {
						continue
					}
					if after := snapshotTree(t, f.stateDir)[path]; after != hash {
						t.Fatalf("refusal changed task artifact %s", path)
					}
				}
			}
			t.Logf("exit=%d prompts=%d", code, len(prompts))
		})
	}
}
func TestResumeStatusStdoutAndEmptyReport(t *testing.T) {
	f, _ := resumeFixture(t, "idle")
	resumeSeed(t, f, "worker", "20261003T100000", false)
	if err := os.WriteFile(f.openReport, nil, 0600); err != nil {
		t.Fatal(err)
	}
	fixed := platform.Now()
	platform.Now = func() time.Time { return fixed }
	_, before, stderr := resumeRun(t, f, "status", "worker")
	if !strings.Contains(stderr, "run soho resume worker") {
		t.Fatalf("empty report notice=%s", stderr)
	}
	platform.BootTime = func() (time.Time, error) { return time.Time{}, errors.New("unknown") }
	_, after, stderr := resumeRun(t, f, "status", "worker")
	if before != after || strings.Contains(stderr, "machine restarted") {
		t.Fatalf("stdout changed: before=%s after=%s stderr=%s", before, after, stderr)
	}
	t.Log("empty report is interrupted; status stdout identical with known/unknown boot time")
}
