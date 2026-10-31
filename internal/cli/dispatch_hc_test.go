package cli

// DJA-604 2b-3a: dispatch through hc. The fake-hc technique is the one the
// spawn hc tests use (fakecli.Install on PATH plus SOHO_FAKECLI_CONFIG): the
// fake records every call in a JSONL log and answers with scripted JSON. A
// fake herdr is installed too and fails the test if it is ever called.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/taskreport"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

const (
	hcDispatchCaller    = `{"address":"t1","state":"live","labels":{"soho.session":"sX"}}`
	hcDispatchSessionLS = `{"terminals":[{"address":"t7","name":"worker","state":"live","turn":5,"reported":true,"status":{"state":"idle"}}]}`
)

type dispatchHcFixture struct {
	root  string
	state string
	sd    string
	bin   string
	brief string
	env   platform.Env
	ctx   *core.Config
}

// hcWorkerGet is one state read of the fixture's worker: live, the given
// turn, and the given 7501 state word.
func hcWorkerGet(turn int, state string) string {
	return `{"address":"t7","name":"worker","state":"live","turn":` + strconv.Itoa(turn) + `,"reported":true,"status":{"state":"` + state + `"}}`
}

// newDispatchHcFixture sets up a dispatch for transport=hc in root: the
// caller terminal t1 of session sX, the fixture state dir (sd is
// <state>/sX, the session standing in for the workspace under hc), the
// worker roster row (its pane field is the hc address t7), the brief, the
// fake herdr, and the fake hc rules the test names. The composed brief and
// its report take the pinned stamp's bare names (nothing exists under them
// before the run).
func newDispatchHcFixture(t *testing.T, rules []fakecli.Rule) *dispatchHcFixture {
	t.Helper()
	root := t.TempDir()
	state, bin := filepath.Join(root, "state"), filepath.Join(root, "bin")
	roles, home, conf := filepath.Join(root, "roles"), filepath.Join(root, "home"), filepath.Join(root, "conf")
	sd := filepath.Join(state, "sX")
	for _, dir := range []string{state, filepath.Join(sd, "wait"), filepath.Join(sd, "briefs"), filepath.Join(sd, "reports"), roles, bin, home, conf} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(root)
	if err := os.WriteFile(filepath.Join(roles, "implementer.md"), []byte("---\nname: Implementer\nmode: edit\n---\nRole body.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	brief := filepath.Join(root, "brief.md")
	if err := os.WriteFile(brief, []byte("# Goal\n\nDispatch a prompt.\n# Expected result\n\nObserved.\n# Owned files\n\nnone\n# Forbidden\n\nNo commit or push.\n# Report\n\ndone\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\tmode\targs\teffort\nworker\tt7\tcodex\timplementer\topenai\t0\t\tnow\tgpt-5\ttask\timplementer\t\t\t--no-daemon\thigh\n"
	if err := os.WriteFile(filepath.Join(sd, "agents.tsv"), []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	pinComposedStamp(t)
	all := append([]fakecli.Rule{
		{Argv: []string{"get", "t1", "--json"}, ArgvPrefix: true, Stdout: hcDispatchCaller},
		{Argv: []string{"ls", "--label", "soho.session=sX", "--json"}, ArgvPrefix: true, Stdout: hcDispatchSessionLS},
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
		"SOHO_SKILL_DIR": testSkillDir(t), "SOHO_ROLES": roles,
		"SOHO_DIR": state, "SOHO_TRANSPORT": "hc", "HC_TERMINAL_ID": "t1",
		"SOHO_LANES": "off", "SOHO_BRIEF_LINT": "off",
		"SOHO_PROMPT_CHECK_SECONDS": "1", "SOHO_WAIT_POLL_MS": "50",
	}
	return &dispatchHcFixture{root: root, state: state, sd: sd, bin: bin, brief: brief, env: env, ctx: &core.Config{Entries: map[string]core.ConfigEntry{}}}
}

func (f *dispatchHcFixture) run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, stderr bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &stderr
	code := func() int {
		defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
		return Run(append([]string{"dispatch"}, args...), f.env)
	}()
	return code, out.String(), stderr.String()
}

func (f *dispatchHcFixture) hcCalls(t *testing.T) []fakecli.Call {
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
// call: under transport=hc the dispatch must reach the worker only through
// hc.
func (f *dispatchHcFixture) assertHcNeverTouchedHerdr(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(f.bin, "herdr.calls.jsonl")); err == nil {
		calls, _ := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
		t.Fatalf("herdr was called: %#v", calls)
	}
}

// hcSends returns the recorded send calls, in order.
func hcSends(calls []fakecli.Call) []fakecli.Call {
	var out []fakecli.Call
	for _, c := range calls {
		if len(c.Argv) > 0 && c.Argv[0] == "send" {
			out = append(out, c)
		}
	}
	return out
}

func dispatchHcOutputStatus(t *testing.T, out string) string {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &value); err != nil {
		t.Fatalf("dispatch JSON %q: %v", out, err)
	}
	status, _ := value["wait_status"].(string)
	return status
}

// TestDispatchHcIdleWorkerAccepted is acceptance (a): an idle worker, the
// exact send argv, started true, the accepted sidecar, no screen read and no
// key around the send, and no herdr call.
func TestDispatchHcIdleWorkerAccepted(t *testing.T) {
	f := newDispatchHcFixture(t, []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, Call: 1, Stdout: hcWorkerGet(7, "idle")},
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(7, "idle")},
		{Argv: []string{"send", "--if", "idle", "--json", "--", "worker"}, ArgvPrefix: true, Stdout: `{"turn":7,"started":true}`},
		{Argv: []string{"read", "worker"}, ArgvPrefix: true},
	})
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	if code != 0 {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if status := dispatchHcOutputStatus(t, out); status != "submitted" {
		t.Fatalf("status=%q, want submitted (out=%s)", status, out)
	}
	sends := hcSends(f.hcCalls(t))
	if len(sends) != 1 {
		t.Fatalf("sends=%d, want exactly one: %#v", len(sends), sends)
	}
	argv := sends[0].Argv
	want := []string{"send", "--if", "idle", "--json", "--", "worker"}
	if len(argv) != len(want)+1 {
		t.Fatalf("argv=%#v, want %v plus the text", argv, want)
	}
	for i := range want {
		if argv[i] != want[i] {
			t.Fatalf("argv[%d]=%q, want %q (argv=%#v)", i, argv[i], want[i], argv)
		}
	}
	// The send text is the pointer prompt: the composed brief's path and the
	// report's path, the same text the herdr path types into the terminal.
	stamp := composedStampOf(f, t)
	composedPath := filepath.Join(f.sd, "briefs", "worker-"+stamp+".md")
	reportPath := filepath.Join(f.sd, "reports", "worker-"+stamp+".md")
	wantText := fmt.Sprintf("Read the file %s in full and execute it. It contains your role, your brief, and your report contract. When finished, write your report to %s and reply with exactly that path and nothing else.", composedPath, reportPath)
	if argv[6] != wantText {
		t.Fatalf("send text = %q, want %q", argv[6], wantText)
	}
	assertNoReadsOrKeysAroundTheSend(t, f.hcCalls(t))
	f.assertHcNeverTouchedHerdr(t)
	sidecar, err := os.ReadFile(filepath.Join(f.sd, "briefs", "worker-"+composedStampOf(f, t)+".dispatch.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sidecar), `"submission":"accepted"`) {
		t.Fatalf("sidecar = %s, want submission accepted", sidecar)
	}
}

// assertNoReadsOrKeysAroundTheSend pins the skipped step set: under hc the
// dispatch sends no key at all and reads no screen before the send (the
// settle, H0, prior-auth and composer reads the herdr path sends); the
// --no-wait observation phase may read the screen after the send, as the
// herdr path's no-wait phase does.
func assertNoReadsOrKeysAroundTheSend(t *testing.T, calls []fakecli.Call) {
	t.Helper()
	sendIndex := -1
	for i, c := range calls {
		if len(c.Argv) > 0 && c.Argv[0] == "send" {
			sendIndex = i
			break
		}
	}
	if sendIndex < 0 {
		t.Fatal("no send recorded, want the send under test")
	}
	for i, c := range calls {
		if len(c.Argv) == 0 {
			continue
		}
		if c.Argv[0] == "keys" {
			t.Fatalf("a key was sent under hc: %#v", c)
		}
		if c.Argv[0] == "read" && i < sendIndex {
			t.Fatalf("a screen read before the send under hc: %#v", c)
		}
	}
}

// TestDispatchHcTurnRiseArrives is acceptance (b): started false, then a get
// with a greater turn is arrival proof inside the check window.
func TestDispatchHcTurnRiseArrives(t *testing.T) {
	f := newDispatchHcFixture(t, []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, Call: 1, Stdout: hcWorkerGet(7, "idle")},
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(8, "idle")},
		{Argv: []string{"send", "--if", "idle", "--json", "--", "worker"}, ArgvPrefix: true, Stdout: `{"turn":7,"started":false}`},
		{Argv: []string{"read", "worker"}, ArgvPrefix: true},
	})
	// The window is generous so the acceptance does not depend on the fake
	// process's start time under host load; the test ends as soon as the
	// turn rise is observed.
	f.env["SOHO_PROMPT_CHECK_SECONDS"] = "30"
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	if code != 0 {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if status := dispatchHcOutputStatus(t, out); status != "submitted" {
		t.Fatalf("status=%q, want submitted (out=%s)", status, out)
	}
	if len(hcSends(f.hcCalls(t))) != 1 {
		t.Fatalf("sends: %#v, want exactly one", hcSends(f.hcCalls(t)))
	}
	// The arrival probe is the second get (the first is the pre-send idle
	// check); it saw turn 8 > the send's turn 7.
	gets := 0
	for _, c := range f.hcCalls(t) {
		if len(c.Argv) == 3 && c.Argv[0] == "get" && c.Argv[1] == "worker" {
			gets++
		}
	}
	if gets < 2 {
		t.Fatalf("worker gets=%d, want the pre-send check and the arrival probe", gets)
	}
	f.assertHcNeverTouchedHerdr(t)
}

// TestDispatchHcNoTurnRiseIsNotReceived is acceptance (c): started false and
// the turn never moves, so the not-received path with the reason, and exactly
// one send (no resend under hc).
func TestDispatchHcNoTurnRiseIsNotReceived(t *testing.T) {
	f := newDispatchHcFixture(t, []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(7, "idle")},
		{Argv: []string{"send", "--if", "idle", "--json", "--", "worker"}, ArgvPrefix: true, Stdout: `{"turn":7,"started":false}`},
	})
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	if code != 15 {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if status := dispatchHcOutputStatus(t, out); status != "not-received" {
		t.Fatalf("status=%q, want not-received (out=%s)", status, out)
	}
	// The hc not-received line is pinned exactly: under hc it names `hc
	// read`, never the herdr pane read (the herdr wording stays as the
	// herdr tests pin it).
	wantLine := "soho: warning: prompt to 'worker' was not received after no turn started under hc; read the terminal (hc read worker) before sending anything else"
	if !strings.Contains(errText, wantLine) {
		t.Fatalf("stderr=%q, want the exact hc not-received line %q", errText, wantLine)
	}
	if len(hcSends(f.hcCalls(t))) != 1 {
		t.Fatalf("sends: %#v, want exactly one (no resend under hc)", hcSends(f.hcCalls(t)))
	}
	f.assertHcNeverTouchedHerdr(t)
}

// TestDispatchHcBlockedRestoresThePointer is acceptance (d): a send that
// exits 9 takes the blocked result with cause hc-blocked, and the task
// pointer is restored like the codex-hooks-review block.
func TestDispatchHcBlockedRestoresThePointer(t *testing.T) {
	f := newDispatchHcFixture(t, []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(7, "idle")},
		{Argv: []string{"send", "--if", "idle", "--json", "--", "worker"}, ArgvPrefix: true, Code: 9, Stderr: `{"error":{"kind":"blocked","message":"worker is blocked"}}`},
	})
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	if code != 7 {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	var value map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &value); err != nil {
		t.Fatalf("dispatch JSON %q: %v", out, err)
	}
	if value["wait_status"] != "blocked" {
		t.Fatalf("wait_status=%v, want blocked (out=%s)", value["wait_status"], out)
	}
	cause, _ := value["cause"].(string)
	if cause != "hc-blocked" {
		t.Fatalf("cause=%q, want hc-blocked (out=%s)", cause, out)
	}
	// The task pointer and the last-report pointer are restored: the
	// fixture had no prior dispatch, so both are gone.
	if _, err := os.Stat(taskreport.TaskReportPointerPath(f.sd, "worker")); err == nil {
		t.Fatal("task pointer was not restored (still present)")
	}
	if _, err := os.ReadFile(filepath.Join(f.sd, "last-report-worker")); err == nil {
		t.Fatal("last-report pointer was not restored (still present)")
	}
	f.assertHcNeverTouchedHerdr(t)
}

// TestDispatchHcBusyRetriesOnceWhenIdle is acceptance (e): a send that exits
// 10 (the worker went working), then a get that settles to idle, then a
// second send that succeeds: two sends, accepted.
func TestDispatchHcBusyRetriesOnceWhenIdle(t *testing.T) {
	f := newDispatchHcFixture(t, []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, Call: 1, Stdout: hcWorkerGet(7, "idle")},
		{Argv: []string{"get", "worker", "--json"}, Call: 2, Stdout: hcWorkerGet(7, "working")},
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(8, "idle")},
		{Argv: []string{"send", "--if", "idle", "--json", "--", "worker"}, ArgvPrefix: true, Call: 1, Code: 10, Stderr: `{"error":{"kind":"status_mismatch","message":"the terminal is working"}}`},
		{Argv: []string{"send", "--if", "idle", "--json", "--", "worker"}, ArgvPrefix: true, Call: 2, Stdout: `{"turn":8,"started":true}`},
		{Argv: []string{"read", "worker"}, ArgvPrefix: true},
	})
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait")
	if code != 0 {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if status := dispatchHcOutputStatus(t, out); status != "submitted" {
		t.Fatalf("status=%q, want submitted (out=%s)", status, out)
	}
	sends := hcSends(f.hcCalls(t))
	if len(sends) != 2 {
		t.Fatalf("sends=%d, want exactly two (the busy retry): %#v", len(sends), sends)
	}
	sidecar, err := os.ReadFile(filepath.Join(f.sd, "briefs", "worker-"+composedStampOf(f, t)+".dispatch.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sidecar), `"submission":"accepted"`) {
		t.Fatalf("sidecar = %s, want submission accepted", sidecar)
	}
	f.assertHcNeverTouchedHerdr(t)
}

// TestDispatchHcQueueDropsTheIdleGuard is acceptance (f): --queue sends
// without --if, so the text goes in even while the worker works.
func TestDispatchHcQueueDropsTheIdleGuard(t *testing.T) {
	f := newDispatchHcFixture(t, []fakecli.Rule{
		{Argv: []string{"get", "worker", "--json"}, ArgvPrefix: true, Stdout: hcWorkerGet(7, "working")},
		{Argv: []string{"send", "--json", "--", "worker"}, ArgvPrefix: true, Stdout: `{"turn":8,"started":true}`},
	})
	code, out, errText := f.run(t, "worker", f.brief, "--no-wait", "--queue")
	if code != 0 {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if status := dispatchHcOutputStatus(t, out); status != "submitted" {
		t.Fatalf("status=%q, want submitted (out=%s)", status, out)
	}
	sends := hcSends(f.hcCalls(t))
	if len(sends) != 1 {
		t.Fatalf("sends=%d, want exactly one: %#v", len(sends), sends)
	}
	argv := sends[0].Argv
	if len(argv) < 2 || argv[1] != "--json" {
		t.Fatalf("argv=%#v, want the --queue send to carry no --if guard", argv)
	}
	for _, a := range argv {
		if a == "--if" || a == "idle" {
			t.Fatalf("argv=%#v, --queue must drop the --if idle guard", argv)
		}
	}
	f.assertHcNeverTouchedHerdr(t)
}

// TestDispatchHcCompactRefusedBeforeSend is acceptance (g): --compact is
// refused with exit 2 and the exact line, before anything is sent.
func TestDispatchHcCompactRefusedBeforeSend(t *testing.T) {
	f := newDispatchHcFixture(t, nil)
	code, out, errText := f.run(t, "worker", f.brief, "--compact", "--no-wait")
	if code != 2 {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errText)
	}
	if want := "soho: dispatch: --compact is not supported under transport=hc yet\n"; errText != want {
		t.Fatalf("stderr=%q, want exactly %q", errText, want)
	}
	if out != "" {
		t.Fatalf("stdout=%q, want nothing", out)
	}
	if sends := hcSends(f.hcCalls(t)); len(sends) != 0 {
		t.Fatalf("sends before the refusal: %#v", sends)
	}
	f.assertHcNeverTouchedHerdr(t)
}

// composedStampOf recovers the composed brief's stamp from the fixture's
// briefs dir (one dispatch, one composed brief).
func composedStampOf(f *dispatchHcFixture, t *testing.T) string {
	t.Helper()
	glob := filepath.Join(f.sd, "briefs", "worker-*.md")
	files, err := filepath.Glob(glob)
	if err != nil || len(files) != 1 {
		t.Fatalf("composed briefs = %#v (err %v), want exactly one", files, err)
	}
	name := filepath.Base(files[0])
	if !strings.HasPrefix(name, "worker-") || !strings.HasSuffix(name, ".md") {
		t.Fatalf("composed brief %q, want worker-<stamp>.md", name)
	}
	return strings.TrimSuffix(strings.TrimPrefix(name, "worker-"), ".md")
}
