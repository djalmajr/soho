package cli

// SOH-474: a busy amendment refusal is not a submission. It keeps the
// accepted task's wait (and any earlier real failure's marker) untouched.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/taskreport"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// setBusyAmendWorker installs the fake hc for one worker state. An accepted
// guarded send proves its started turn; a busy one refuses with hc exit 10.
func setBusyAmendWorker(t *testing.T, f *dispatchHcFixture, state string, busy bool) {
	t.Helper()
	bin := t.TempDir()
	send := fakecli.Rule{Argv: []string{"send", "--if", "idle", "--json", "--", "worker"}, ArgvPrefix: true, Stdout: `{"turn":7,"started":true}`}
	if busy {
		send.Code = 10
		send.Stdout = ""
		send.Stderr = `{"error":{"kind":"status_mismatch","message":"busy"}}`
	}
	rules := append([]fakecli.Rule{
		{Argv: []string{"get", "t1", "--json"}, ArgvPrefix: true, Stdout: hcDispatchCaller},
		{Argv: []string{"ls", "--label", "soho.session=sX", "--json"}, ArgvPrefix: true, Stdout: hcDispatchSessionLS},
	}, guardProbeRules()...)
	rules = append(rules, fakecli.Rule{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(7, state)}, send)
	if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
		t.Fatal(err)
	}
	f.env["PATH"], f.env["SOHO_FAKECLI_CONFIG"] = bin, bin
}

func busyAmendDecode(t *testing.T, out string) map[string]any {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &obj); err != nil {
		t.Fatalf("output=%q: %v", out, err)
	}
	return obj
}

// waitReportField returns the decoded "report" field of wait's JSON line, so
// a path compares the same on every OS (JSON doubles Windows backslashes).
func waitReportField(out string) string {
	for _, line := range strings.Split(out, "\n") {
		var obj map[string]any
		if json.Unmarshal([]byte(strings.TrimSpace(line)), &obj) != nil {
			continue
		}
		if report, ok := obj["report"].(string); ok {
			return report
		}
	}
	return ""
}

func TestDispatchHcBusyAmendLeavesAcceptedWaitUntouched(t *testing.T) {
	f := newDispatchHcFixture(t, nil)
	clock := platform.Now
	var offset time.Duration
	platform.Now = func() time.Time { return clock().Add(offset) }
	t.Cleanup(func() { platform.Now = clock })
	marker := filepath.Join(f.sd, "wait", "worker.submit-failed")

	setBusyAmendWorker(t, f, "idle", false)
	code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
	if code != 0 {
		t.Fatalf("first dispatch: exit=%d out=%q stderr=%q", code, out, stderr)
	}
	first := busyAmendDecode(t, out)
	firstReport, ok := first["report"].(string)
	if !ok || firstReport == "" || first["wait_status"] != "submitted" {
		t.Fatalf("first=%v", first)
	}
	if err := os.WriteFile(firstReport, []byte("# First task report\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Fatalf("accepted dispatch wrote a submit-failed marker: %v", err)
	}

	offset += 5 * time.Second
	setBusyAmendWorker(t, f, "working", true)
	ptrBefore, err := os.ReadFile(taskreport.TaskReportPointerPath(f.sd, "worker"))
	if err != nil {
		t.Fatal(err)
	}
	code, out, stderr = f.run(t, "worker", f.brief, "--amend", "--no-wait")
	if code != 4 || !strings.Contains(out, "worker worker is still working; amendment was not sent") || !strings.Contains(out, "without --amend") {
		t.Fatalf("busy amendment: exit=%d out=%q stderr=%q", code, out, stderr)
	}
	if !strings.Contains(stderr, "worker worker is still working") || strings.Contains(stderr, "prompt submission failed") {
		t.Fatalf("busy amendment warning: %q", stderr)
	}
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Fatalf("busy amendment wrote a submit-failed marker: %v", err)
	}
	ptrAfter, err := os.ReadFile(taskreport.TaskReportPointerPath(f.sd, "worker"))
	if err != nil || !bytes.Equal(ptrBefore, ptrAfter) {
		t.Fatalf("busy amendment changed the task pointer: %q (err %v)", ptrAfter, err)
	}
	// ReadDir, not Glob: a literal backslash or bracket in the temporary
	// directory would be read as a pattern escape.
	var sidecars []string
	entries, err := os.ReadDir(filepath.Join(f.sd, "briefs"))
	for _, entry := range entries {
		if name := entry.Name(); strings.HasPrefix(name, "worker-") && strings.HasSuffix(name, ".dispatch.json") {
			sidecars = append(sidecars, filepath.Join(f.sd, "briefs", name))
		}
	}
	if err != nil || len(sidecars) != 2 {
		t.Fatalf("attempt sidecars=%#v (err %v), want the accepted dispatch and the refused amendment", sidecars, err)
	}
	failed := 0
	for _, path := range sidecars {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte(`"submission":"failed"`)) {
			failed++
		}
	}
	if failed != 1 {
		t.Fatalf("attempt sidecars=%#v, want exactly one failed amendment record", sidecars)
	}

	offset += 5 * time.Second
	setBusyAmendWorker(t, f, "idle", false)
	var waitOut, waitErr bytes.Buffer
	oldOut, oldErr := platform.Stdout, platform.Stderr
	platform.Stdout, platform.Stderr = &waitOut, &waitErr
	waitCode := func() int {
		defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
		return Run([]string{"wait", "worker", "--timeout", "5s"}, f.env)
	}()
	if waitCode != 0 || !strings.Contains(waitOut.String(), `"status":"done"`) || waitReportField(waitOut.String()) != firstReport {
		t.Fatalf("wait exit=%d out=%q stderr=%q", waitCode, waitOut.String(), waitErr.String())
	}
	if strings.Contains(waitOut.String(), "not-received") || strings.Contains(waitOut.String(), "submission-failed") || strings.Contains(waitErr.String(), "previous task") {
		t.Fatalf("wait reported the accepted task as failed: out=%q stderr=%q", waitOut.String(), waitErr.String())
	}
}

func TestDispatchHcBusyAmendKeepsEarlierSubmitFailedMarker(t *testing.T) {
	f := newDispatchHcFixture(t, append(guardProbeRules(), []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(7, "working")},
		{Argv: []string{"send", "--if", "idle", "--json", "--", "worker"}, ArgvPrefix: true, Code: 10, Stderr: `{"error":{"kind":"status_mismatch","message":"busy"}}`},
	}...))
	marker := filepath.Join(f.sd, "wait", "worker.submit-failed")
	other := filepath.Join(f.sd, "wait", "other.submit-failed")
	old := filepath.Join(f.sd, "reports", "previous.md")
	markerValue := fmt.Sprintf("%d %s %s\n", 1700000000, strings.Repeat("a", 64), old)
	otherValue := fmt.Sprintf("%d %s %s\n", 1700000001, strings.Repeat("b", 64), old)
	if err := os.WriteFile(marker, []byte(markerValue), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte(otherValue), 0o600); err != nil {
		t.Fatal(err)
	}
	pointer := []byte(fmt.Sprintf(`{"version":1,"task_report":%q,"current":%q,"history":[],"role":"implementer","pane":"t7"}`, old, old) + "\n")
	if err := os.WriteFile(taskreport.TaskReportPointerPath(f.sd, "worker"), pointer, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.sd, "last-report-worker"), []byte(old+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, out, stderr := f.run(t, "worker", f.brief, "--amend", "--no-wait", "--timeout", "1s")
	if code != 4 || !strings.Contains(out, "worker worker is still working; amendment was not sent") {
		t.Fatalf("busy amendment: exit=%d out=%q stderr=%q", code, out, stderr)
	}
	for path, want := range map[string]string{marker: markerValue, other: otherValue} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("marker %s = %q (err %v), want %q", path, got, err, want)
		}
	}
}

// countWorkerGets counts the fake hc state reads of the worker in one call
// log; the wait-phase fixture blocks its second read until the report exists.
func countWorkerGets(calls []fakecli.Call) int {
	n := 0
	for _, c := range calls {
		if len(c.Argv) >= 2 && c.Argv[0] == "get" && c.Argv[1] == "worker" {
			n++
		}
	}
	return n
}

// TestDispatchHcBusyAmendWaitStartsWhileWorking is the active-wait case:
// the report does not exist yet when wait starts, hc reports the worker
// working on its first poll, and only then does the fixture write the
// accepted task's report and settle the worker. No sleep drives the switch.
func TestDispatchHcBusyAmendWaitStartsWhileWorking(t *testing.T) {
	f := newDispatchHcFixture(t, nil)
	setBusyAmendWorker(t, f, "idle", false)
	code, out, stderr := f.run(t, "worker", f.brief, "--no-wait")
	if code != 0 {
		t.Fatalf("first dispatch: exit=%d out=%q stderr=%q", code, out, stderr)
	}
	first := busyAmendDecode(t, out)
	firstReport, ok := first["report"].(string)
	if !ok || firstReport == "" || first["wait_status"] != "submitted" {
		t.Fatalf("first=%v", first)
	}
	marker := filepath.Join(f.sd, "wait", "worker.submit-failed")
	if _, err := os.Lstat(firstReport); !os.IsNotExist(err) {
		t.Fatalf("the first report already exists before the refusal: %v", err)
	}

	setBusyAmendWorker(t, f, "working", true)
	code, out, stderr = f.run(t, "worker", f.brief, "--amend", "--no-wait")
	if code != 4 || !strings.Contains(out, "worker worker is still working; amendment was not sent") {
		t.Fatalf("busy amendment: exit=%d out=%q stderr=%q", code, out, stderr)
	}
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Fatalf("busy amendment wrote a submit-failed marker: %v", err)
	}
	if _, err := os.Lstat(firstReport); !os.IsNotExist(err) {
		t.Fatalf("the first report exists before wait starts: %v", err)
	}

	// A fresh fake hc starts its call numbering at one: poll 1 sees a
	// working worker without a report, poll 2 blocks on the report file and
	// answers idle, and every later poll answers idle.
	bin := t.TempDir()
	getWorker := []string{"get", "worker", "--json"}
	rules := []fakecli.Rule{
		{Argv: []string{"get", "t1", "--json"}, ArgvPrefix: true, Stdout: hcDispatchCaller},
		{Argv: []string{"ls", "--label", "soho.session=sX", "--json"}, ArgvPrefix: true, Stdout: hcDispatchSessionLS},
		{Argv: getWorker, ArgvPrefix: true, Call: 1, Stdout: hcWorkerGet(7, "working")},
		{Argv: getWorker, ArgvPrefix: true, Call: 2, WaitFile: firstReport, Stdout: hcWorkerGet(7, "idle")},
		{Argv: getWorker, ArgvPrefix: true, Stdout: hcWorkerGet(7, "idle")},
	}
	if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
		t.Fatal(err)
	}
	if _, err := fakecli.Install(t, bin, "herdr", nil); err != nil {
		t.Fatal(err)
	}
	f.env["PATH"], f.env["SOHO_FAKECLI_CONFIG"] = bin, bin
	logPath := filepath.Join(bin, "hc.calls.jsonl")

	var waitOut, waitErr bytes.Buffer
	oldOut, oldErr := platform.Stdout, platform.Stderr
	platform.Stdout, platform.Stderr = &waitOut, &waitErr
	defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
	done := make(chan int, 1)
	go func() { done <- Run([]string{"wait", "worker", "--timeout", "5s"}, f.env) }()

	var seen []fakecli.Call
	deadline := time.Now().Add(10 * time.Second)
	for {
		var err error
		seen, err = fakecli.ReadCalls(logPath)
		if err == nil && countWorkerGets(seen) >= 2 {
			break
		}
		if time.Now().After(deadline) {
			waitCode := <-done
			t.Fatalf("no working poll before the report: code=%d gets=%d out=%q stderr=%q", waitCode, countWorkerGets(seen), waitOut.String(), waitErr.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Logf("wait polls before the report: %d worker reads", countWorkerGets(seen))
	if err := os.WriteFile(firstReport, []byte("# Active task report\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var waitCode int
	select {
	case waitCode = <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("wait did not finish after the report was written")
	}
	if waitCode != 0 || !strings.Contains(waitOut.String(), `"status":"done"`) || waitReportField(waitOut.String()) != firstReport {
		t.Fatalf("wait exit=%d out=%q stderr=%q", waitCode, waitOut.String(), waitErr.String())
	}
	if strings.Contains(waitOut.String(), "not-received") || strings.Contains(waitOut.String(), "submission-failed") {
		t.Fatalf("wait reported the accepted task as failed: out=%q stderr=%q", waitOut.String(), waitErr.String())
	}
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Fatalf("busy amendment wrote a submit-failed marker: %v", err)
	}
}
