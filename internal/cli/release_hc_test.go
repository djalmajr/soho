package cli

// DJA-604 2b-4/5: soho release under transport=hc. The fake-hc technique is
// the one the spawn and dispatch hc tests use (fakecli.Install on PATH plus
// SOHO_FAKECLI_CONFIG): the fake records every call in a JSONL log and
// answers with scripted JSON. A fake herdr is installed too and fails the
// test if it is ever called.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/taskreport"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

const (
	hcReleaseCaller     = `{"address":"t1","state":"live","labels":{"soho.session":"sX"}}`
	hcReleaseWorkerLive = `{"address":"t7","name":"worker","state":"live","turn":5,"reported":true,"status":{"state":"idle"}}`
)

type releaseHcFixture struct {
	root  string
	state string
	sd    string
	bin   string
	env   platform.Env
	ctx   *core.Config
}

// newReleaseHcFixture sets up a release for transport=hc in root: the
// caller terminal t1 of session sX, the fixture state dir (sd is
// <state>/sX, the session standing in for the workspace under hc), the
// worker roster row (its pane field is the hc address t7, created by this
// skill), the fake herdr, and the fake hc rules the test names.
func newReleaseHcFixture(t *testing.T, rules []fakecli.Rule) *releaseHcFixture {
	t.Helper()
	root := t.TempDir()
	state, bin := filepath.Join(root, "state"), filepath.Join(root, "bin")
	home, conf := filepath.Join(root, "home"), filepath.Join(root, "conf")
	sd := filepath.Join(state, "sX")
	for _, dir := range []string{state, filepath.Join(sd, "wait"), home, conf, bin} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(root)
	roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\tmode\targs\teffort\nworker\tt7\tcodex\timplementer\topenai\t1\t" + root + "\tnow\tgpt-5\ttask\timplementer\t\t\t--no-daemon\thigh\n"
	if err := os.WriteFile(filepath.Join(sd, "agents.tsv"), []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	all := append([]fakecli.Rule{
		{Argv: []string{"get", "t1", "--json"}, ArgvPrefix: true, Stdout: hcReleaseCaller},
	}, rules...)
	if _, err := fakecli.Install(t, bin, "hc", all); err != nil {
		t.Fatal(err)
	}
	if _, err := fakecli.Install(t, bin, "herdr", nil); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{
		"PATH": bin, "HOME": home, "USERPROFILE": home, "XDG_CONFIG_HOME": conf,
		"TMPDIR": root, "SOHO_FAKECLI_CONFIG": bin,
		"SOHO_DIR": state, "SOHO_TRANSPORT": "hc", "HC_TERMINAL_ID": "t1",
		"SOHO_LANES": "off", "SOHO_WAIT_POLL_MS": "50",
	}
	return &releaseHcFixture{root: root, state: state, sd: sd, bin: bin, env: env, ctx: &core.Config{Entries: map[string]core.ConfigEntry{}}}
}

func (f *releaseHcFixture) run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, stderr bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &stderr
	code := func() int {
		defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
		return Run(append([]string{"release"}, args...), f.env)
	}()
	return code, out.String(), stderr.String()
}

func (f *releaseHcFixture) hcCalls(t *testing.T) []fakecli.Call {
	t.Helper()
	calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "hc.calls.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	return calls
}

// assertHcNeverTouchedHerdr fails the test when the fake herdr recorded any
// call: under transport=hc the release must reach the worker only through
// hc.
func (f *releaseHcFixture) assertHcNeverTouchedHerdr(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(f.bin, "herdr.calls.jsonl")); err == nil {
		calls, _ := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
		t.Fatalf("herdr was called: %#v", calls)
	}
}

// hcCallIndexes returns the indexes of the recorded calls whose argv starts
// with the given prefix.
func hcCallIndexes(calls []fakecli.Call, prefix []string) []int {
	indexes := []int{}
	for i, c := range calls {
		if len(c.Argv) < len(prefix) {
			continue
		}
		match := true
		for j := range prefix {
			if c.Argv[j] != prefix[j] {
				match = false
				break
			}
		}
		if match {
			indexes = append(indexes, i)
		}
	}
	return indexes
}

// addReleaseHcReport records the worker's report pointer and, when not
// pointerOnly, writes the report file under it.
func addReleaseHcReport(t *testing.T, f *releaseHcFixture, pointerOnly bool) string {
	t.Helper()
	report := filepath.Join(f.sd, "reports", "worker.md")
	if err := os.MkdirAll(filepath.Dir(report), 0o700); err != nil {
		t.Fatal(err)
	}
	if !pointerOnly {
		if err := os.WriteFile(report, []byte("done\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(core.LastReportPath(f.sd, "worker"), []byte(report+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return report
}

// TestReleaseHcCloseIdleWorkerWithReport is acceptance (a): release --close
// of an idle worker with its report — kill and rm are recorded, the
// terminal line is printed, the roster row is gone, the task pointer is
// retired, and no herdr call is made.
func TestReleaseHcCloseIdleWorkerWithReport(t *testing.T) {
	f := newReleaseHcFixture(t, []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcReleaseWorkerLive},
		{Argv: []string{"kill", "t7", "--json"}, ArgvPrefix: true, Stdout: `{"address":"t7","state":"finished"}`},
		{Argv: []string{"rm", "t7", "--json"}, ArgvPrefix: true, Stdout: `{"removed":"t7"}`},
	})
	report := addReleaseHcReport(t, f, false)
	writeTaskPointer(t, f.sd, "worker", report, report)
	code, out, errText := f.run(t, "worker", "--close")
	if code != 0 {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if !strings.Contains(out, "closed terminal t7\n") {
		t.Fatalf("stdout=%q, want the closed terminal line", out)
	}
	if row := core.RosterLine(f.sd, "worker"); row != "" {
		t.Fatalf("roster row kept: %q", row)
	}
	// kill then rm, in order, no other worker call.
	kills := hcCallIndexes(f.hcCalls(t), []string{"kill", "t7"})
	rms := hcCallIndexes(f.hcCalls(t), []string{"rm", "t7"})
	if len(kills) != 1 || len(rms) != 1 || rms[0] < kills[0] {
		t.Fatalf("kill/rm calls: kills=%v rms=%v calls=%#v", kills, rms, f.hcCalls(t))
	}
	// The task pointer is retired: current cleared, the report in history.
	pointer := taskreport.ReadTaskReportPointer(f.sd, "worker")
	if pointer == nil {
		t.Fatal("task pointer was removed instead of retired")
	}
	if current, _ := pointer.Get("current"); current != "" {
		t.Fatalf("pointer current=%v, want retired (empty)", current)
	}
	f.assertHcNeverTouchedHerdr(t)
}

// TestReleaseHcCloseFailureKeepsRosterAndReport is acceptance (b): the
// close fails — exit 4 with the terminal line, the roster and the report
// kept.
func TestReleaseHcCloseFailureKeepsRosterAndReport(t *testing.T) {
	f := newReleaseHcFixture(t, []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcReleaseWorkerLive},
		{Argv: []string{"kill", "t7", "--json"}, ArgvPrefix: true, Code: 1, Stderr: `{"error":{"kind":"refused","message":"the program is busy"}}`},
		{Argv: []string{"get", "t7", "--json"}, ArgvPrefix: true, Stdout: hcReleaseWorkerLive},
	})
	addReleaseHcReport(t, f, false)
	code, out, errText := f.run(t, "worker", "--close")
	if code != 4 {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if want := "release: could not close the terminal t7 of 'worker'; the roster and report were kept; retry release --close"; !strings.Contains(errText, want) {
		t.Fatalf("stderr=%q, want %q", errText, want)
	}
	if row := core.RosterLine(f.sd, "worker"); row == "" {
		t.Fatal("roster row was removed on a failed close")
	}
	if _, err := os.ReadFile(core.LastReportPath(f.sd, "worker")); err != nil {
		t.Fatalf("last-report pointer lost: %v", err)
	}
	if _, err := os.ReadFile(filepath.Join(f.sd, "reports", "worker.md")); err != nil {
		t.Fatalf("report lost: %v", err)
	}
	if rms := hcCallIndexes(f.hcCalls(t), []string{"rm", "t7"}); len(rms) != 0 {
		t.Fatalf("rm after a failed kill: %#v", f.hcCalls(t))
	}
	f.assertHcNeverTouchedHerdr(t)
}

// TestReleaseHcFinishedRecordIsNotClosed: under hc a close is kill plus
// rm, so a terminal whose program finished but whose record was not
// removed is not closed — the release fails and keeps the roster, the
// herdr "already gone" fallback does not apply.
func TestReleaseHcFinishedRecordIsNotClosed(t *testing.T) {
	finished := `{"address":"t7","name":"worker","state":"finished","exit_code":0,"reason":"killed"}`
	for _, tc := range []struct {
		name  string
		rules []fakecli.Rule
	}{
		{name: "kill ok, rm fails", rules: []fakecli.Rule{
			{Argv: []string{"kill", "t7", "--json"}, ArgvPrefix: true, Stdout: finished},
			{Argv: []string{"rm", "t7", "--json"}, ArgvPrefix: true, Code: 1, Stderr: `{"error":{"kind":"internal","message":"rm failed"}}`},
		}},
		{name: "kill fails", rules: []fakecli.Rule{
			{Argv: []string{"kill", "t7", "--json"}, ArgvPrefix: true, Code: 1, Stderr: `{"error":{"kind":"refused","message":"the program is busy"}}`},
		}},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			rules := append([]fakecli.Rule{
				{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcReleaseWorkerLive},
			}, tc.rules...)
			rules = append(rules, fakecli.Rule{Argv: []string{"get", "t7", "--json"}, ArgvPrefix: true, Stdout: finished})
			f := newReleaseHcFixture(t, rules)
			addReleaseHcReport(t, f, false)
			code, out, errText := f.run(t, "worker", "--close")
			if code != 4 {
				t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
			}
			if strings.Contains(out, "closed terminal") {
				t.Fatalf("stdout=%q reports a close that did not remove the record", out)
			}
			if row := core.RosterLine(f.sd, "worker"); row == "" {
				t.Fatal("roster row was removed though the terminal record stayed")
			}
			if _, err := os.ReadFile(core.LastReportPath(f.sd, "worker")); err != nil {
				t.Fatalf("last-report pointer lost: %v", err)
			}
			f.assertHcNeverTouchedHerdr(t)
		})
	}
}

// TestReleaseHcWorkingWorkerWithoutReportIsRefused is acceptance (c): a
// working worker that has not written its report takes the existing
// refusal (exit 3), with no kill and no rm.
func TestReleaseHcWorkingWorkerWithoutReportIsRefused(t *testing.T) {
	working := `{"address":"t7","name":"worker","state":"live","turn":5,"reported":true,"status":{"state":"working"}}`
	f := newReleaseHcFixture(t, []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: working},
	})
	addReleaseHcReport(t, f, true)
	code, out, errText := f.run(t, "worker", "--close")
	if code != 3 {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if !strings.Contains(errText, "is still working and has not written") {
		t.Fatalf("stderr=%q, want the working refusal", errText)
	}
	if row := core.RosterLine(f.sd, "worker"); row == "" {
		t.Fatal("roster row was removed on the refusal")
	}
	for _, verb := range []string{"kill", "rm"} {
		if indexes := hcCallIndexes(f.hcCalls(t), []string{verb, "t7"}); len(indexes) != 0 {
			t.Fatalf("%s calls on the refusal: %#v", verb, f.hcCalls(t))
		}
	}
	f.assertHcNeverTouchedHerdr(t)
}

// TestReleaseHcWithoutCloseKeepsTheTerminal is acceptance (d): a release
// without --close keeps the terminal running outside the roster — no kill
// or rm, the row removed, the terminal registered for the orphan commands,
// no layout call, no herdr call.
func TestReleaseHcWithoutCloseKeepsTheTerminal(t *testing.T) {
	f := newReleaseHcFixture(t, []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcReleaseWorkerLive},
		{Argv: []string{"ls", "--label", "soho.session=sX", "--json"}, ArgvPrefix: true, Stdout: `{"terminals":[]}`},
	})
	addReleaseHcReport(t, f, false)
	code, out, errText := f.run(t, "worker")
	if code != 0 {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if row := core.RosterLine(f.sd, "worker"); row != "" {
		t.Fatalf("roster row kept: %q", row)
	}
	if !strings.Contains(errText, "soho: release: the terminal t7 stays open; close it later with: soho release worker --close") {
		t.Fatalf("stderr=%q, want the terminal stays-open line", errText)
	}
	for _, verb := range []string{"kill", "rm"} {
		if indexes := hcCallIndexes(f.hcCalls(t), []string{verb, "t7"}); len(indexes) != 0 {
			t.Fatalf("%s calls without --close: %#v", verb, f.hcCalls(t))
		}
	}
	// The terminal is registered for the orphan commands, unchanged from
	// the herdr path.
	registry, err := os.ReadFile(filepath.Join(f.sd, "released-panes.tsv"))
	if err != nil {
		t.Fatalf("released-panes registry missing: %v", err)
	}
	if !strings.Contains(string(registry), "worker\tt7\t") {
		t.Fatalf("registry=%q, want the worker terminal row", registry)
	}
	f.assertHcNeverTouchedHerdr(t)
}

// TestReleaseHcRegistryKeepsAFinishedTerminal: the released-panes
// registry prunes a row only when its terminal record is removed (hc get
// exits 3). A terminal whose program finished but whose record remains
// still holds its name and waits for its rm, so recording another release
// keeps its row.
func TestReleaseHcRegistryKeepsAFinishedTerminal(t *testing.T) {
	f := newReleaseHcFixture(t, []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcReleaseWorkerLive},
		{Argv: []string{"ls", "--label", "soho.session=sX", "--json"}, ArgvPrefix: true, Stdout: `{"terminals":[]}`},
		{Argv: []string{"get", "t9", "--json"}, ArgvPrefix: true, Stdout: `{"address":"t9","name":"older","state":"finished","exit_code":0,"reason":"exited"}`},
		{Argv: []string{"get", "t8", "--json"}, ArgvPrefix: true, Code: 3, Stderr: `{"error":{"kind":"not_found","message":"no such terminal"}}`},
	})
	registry := filepath.Join(f.sd, "released-panes.tsv")
	seed := "# name\tpane\tkind\treleased\nolder\tt9\tcodex\t2026-10-31T00:00:00Z\nremoved\tt8\tcodex\t2026-10-31T00:00:00Z\n"
	if err := os.WriteFile(registry, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	addReleaseHcReport(t, f, false)
	if code, out, errText := f.run(t, "worker"); code != 0 {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	data, err := os.ReadFile(registry)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "older\tt9\t") {
		t.Fatalf("registry=%q, want the finished terminal t9 kept", data)
	}
	if strings.Contains(string(data), "removed\tt8\t") {
		t.Fatalf("registry=%q, want the removed terminal t8 pruned", data)
	}
	if !strings.Contains(string(data), "worker\tt7\t") {
		t.Fatalf("registry=%q, want the released worker t7 recorded", data)
	}
	f.assertHcNeverTouchedHerdr(t)
}

// TestReleaseHcFailedGetIsRefused is acceptance (e): a get that fails is
// the hc-named refusal (exit 4), with the roster kept.
func TestReleaseHcFailedGetIsRefused(t *testing.T) {
	f := newReleaseHcFixture(t, []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Code: 1, Stderr: `{"error":{"kind":"permission_denied","message":"denied"}}`},
	})
	addReleaseHcReport(t, f, true)
	code, out, errText := f.run(t, "worker", "--close")
	if code != 4 {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	want := "agent 'worker': hc get failed (hc exited 1: permission_denied: denied). Refusing to release; the worker may still be live. Retry when hc answers, or pass --force."
	if !strings.Contains(errText, want) {
		t.Fatalf("stderr=%q, want %q", errText, want)
	}
	if row := core.RosterLine(f.sd, "worker"); row == "" {
		t.Fatal("roster row was removed on the refusal")
	}
	if strings.Contains(errText, "herdr agent get failed") {
		// The herdr wording must not leak into the hc refusal.
		t.Fatalf("stderr=%q, the hc refusal must name hc, not herdr", errText)
	}
	f.assertHcNeverTouchedHerdr(t)
}

// TestReleaseHcOrphanClosesTheTerminal is the orphan path (decision 6)
// under hc: a name not in the roster but in the released-panes registry,
// live in the session — release --close reads the session (List) and the
// worker (Get), closes through Runtime.Close, and forgets the registry row.
func TestReleaseHcOrphanClosesTheTerminal(t *testing.T) {
	sessionLS := `{"terminals":[` +
		`{"address":"t1","name":"orchestrator","state":"live","turn":1,"reported":true,"status":{"state":"idle"}},` +
		`{"address":"t7","name":"worker","state":"live","turn":9,"reported":true,"status":{"state":"idle"}}` +
		`]}`
	f := newReleaseHcFixture(t, []fakecli.Rule{
		{Argv: []string{"ls", "--label", "soho.session=sX", "--json"}, ArgvPrefix: true, Stdout: sessionLS},
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcReleaseWorkerLive},
		{Argv: []string{"kill", "t7", "--json"}, ArgvPrefix: true, Stdout: `{"address":"t7","state":"finished"}`},
		{Argv: []string{"rm", "t7", "--json"}, ArgvPrefix: true, Stdout: `{"removed":"t7"}`},
	})
	// The roster has no worker row: only the released-panes registry does.
	if err := os.WriteFile(filepath.Join(f.sd, "agents.tsv"), []byte("# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\tmode\targs\teffort\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.sd, "released-panes.tsv"), []byte("# name\tpane\tkind\treleased\nworker\tt7\tcodex\t2026-10-31T00:00:00Z\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errText := f.run(t, "worker", "--close")
	if code != 0 {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if want := "released worker (terminal t7 closed; it was no longer in the roster)\n"; !strings.Contains(out, want) {
		t.Fatalf("stdout=%q, want %q", out, want)
	}
	kills := hcCallIndexes(f.hcCalls(t), []string{"kill", "t7"})
	rms := hcCallIndexes(f.hcCalls(t), []string{"rm", "t7"})
	if len(kills) != 1 || len(rms) != 1 || rms[0] < kills[0] {
		t.Fatalf("kill/rm calls: kills=%v rms=%v", kills, rms)
	}
	// The registry row is forgotten.
	registry, err := os.ReadFile(filepath.Join(f.sd, "released-panes.tsv"))
	if err != nil {
		t.Fatalf("registry missing after the orphan close: %v", err)
	}
	if strings.Contains(string(registry), "worker\tt7\t") {
		t.Fatalf("registry row kept: %q", registry)
	}
	f.assertHcNeverTouchedHerdr(t)
}

// TestReleaseHcGuardedCommandsKeepTheHerdrWording is the negative control:
// under transport=herdr the same fixture's lines keep the pane wording
// (the hc branches must not leak into the herdr path). The herdr-path
// release tests in release_test.go pin the full behavior; this pins the
// two lines this slice touches.
func TestReleaseHcGuardedCommandsKeepTheHerdrWording(t *testing.T) {
	t.Run("unavailable refusal names herdr", func(t *testing.T) {
		f := newReleaseFixture(t, "", `{"error":{"code":"permission_denied","message":"not allowed"}}`)
		addReleaseReport(t, f.state, "worker", true)
		r := f.run(t, "worker", "--close")
		if r.code != 4 || !strings.Contains(r.stderr, "herdr agent get failed") || strings.Contains(r.stderr, "hc get failed") {
			t.Fatalf("code=%d stderr=%q, want the herdr-named refusal", r.code, r.stderr)
		}
	})
	t.Run("close success keeps the pane line", func(t *testing.T) {
		f := newReleaseFixture(t, "", "")
		addReleaseReport(t, f.state, "worker", false)
		r := f.run(t, "worker", "--close")
		if r.code != 0 || !strings.Contains(r.stdout, "closed pane p-worker\n") || strings.Contains(r.stdout, "closed terminal") {
			t.Fatalf("code=%d stdout=%q, want the herdr pane line", r.code, r.stdout)
		}
	})
}
