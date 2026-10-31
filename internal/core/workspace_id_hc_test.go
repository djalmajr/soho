package core

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// hcWorkspaceFixture builds a temp tree with a fake hc on PATH (the only
// binary there) and the hc-mode environment: SOHO_TRANSPORT=hc and, when
// set, HC_TERMINAL_ID. The label record is the soho.session label the fake
// serves for the caller terminal, empty when the fallback id is under test.
type hcWorkspaceFixture struct {
	env platform.Env
}

// hcLabelRecord is the terminal record the fake hc serves for the caller
// terminal: the soho.session label when set, else none (the fallback case).
func hcLabelRecord(label string) string {
	if label != "" {
		return `{"address":"t1","state":"live","labels":{"soho.session":"` + label + `"},"status":{"state":"idle","turn":1}}`
	}
	return `{"address":"t1","state":"live","status":{"state":"idle","turn":1}}`
}

func newHcWorkspaceFixture(t *testing.T, inTerminal bool, label string) hcWorkspaceFixture {
	t.Helper()
	base := t.TempDir()
	bin := filepath.Join(base, "bin")
	rules := []fakecli.Rule{}
	if inTerminal {
		rules = append(rules, fakecli.Rule{
			Argv:   []string{"get", "t1", "--json"},
			Stdout: hcLabelRecord(label),
		})
	}
	if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{
		"PATH":                bin,
		"SOHO_TRANSPORT":      "hc",
		"SOHO_FAKECLI_CONFIG": bin,
		"SOHO_DIR":            filepath.Join(base, "state"),
	}
	if inTerminal {
		env["HC_TERMINAL_ID"] = "t1"
	}
	return hcWorkspaceFixture{env: env}
}

// hcCalls returns the ordered argv of every fake hc call; a missing log
// file is no call at all (the log is created on the first launch).
func (f hcWorkspaceFixture) calls(t *testing.T) []string {
	t.Helper()
	calls, err := fakecli.ReadCalls(filepath.Join(f.env["PATH"], "hc.calls.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	out := make([]string, 0, len(calls))
	for _, c := range calls {
		out = append(out, joinArgv(c.Argv))
	}
	return out
}

func joinArgv(argv []string) string {
	out := ""
	for i, a := range argv {
		if i > 0 {
			out += "\x00"
		}
		out += a
	}
	return out
}

// dieIn runs f, which must die with a *platform.ExitError (platform.Die's
// panic), and returns the code and message it died with.
func dieIn(t *testing.T, f func()) (int, string) {
	t.Helper()
	var code int
	var message string
	func() {
		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("the call returned instead of dying")
			}
			exitErr, ok := r.(*platform.ExitError)
			if !ok {
				t.Fatalf("die panic %T, want *platform.ExitError", r)
			}
			code = exitErr.Code
			message = exitErr.Msg
		}()
		f()
	}()
	if message == "" && code == 0 {
		t.Fatal("the call returned instead of dying")
	}
	return code, message
}

// DJA-604 stage 2b: under transport=hc the workspace id is the session —
// the soho.session label when the record names one — and no herdr call is
// spent: the fake hc is the only binary on PATH, and the log must hold
// exactly the one session read.
func TestWorkspaceIDHcModeReturnsTheSession(t *testing.T) {
	f := newHcWorkspaceFixture(t, true, "sX")
	cwd := t.TempDir()
	if got := WorkspaceID(nil, f.env, cwd); got != "sX" {
		t.Fatalf("WorkspaceID = %q, want the soho.session label sX", got)
	}
	want := []string{"get\x00t1\x00--json"}
	if got := f.calls(t); len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("hc calls = %#v, want exactly %#v", got, want)
	}
}

// DJA-604 stage 2b: under transport=hc without the label the workspace id
// is the hc-<address> fallback.
func TestWorkspaceIDHcModeFallsBackToTheAddress(t *testing.T) {
	f := newHcWorkspaceFixture(t, true, "")
	cwd := t.TempDir()
	if got := WorkspaceID(nil, f.env, cwd); got != "hc-t1" {
		t.Fatalf("WorkspaceID = %q, want the hc-t1 fallback", got)
	}
}

// DJA-604 stage 2b: under transport=hc outside an hc terminal the process
// dies with the hc message (never a Herdr error), and the failed session
// read dies with its own message.
func TestWorkspaceIDHcModeDiesOutsideAnHcTerminal(t *testing.T) {
	f := newHcWorkspaceFixture(t, false, "")
	cwd := t.TempDir()
	code, message := dieIn(t, func() { WorkspaceID(nil, f.env, cwd) })
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if message != "transport=hc: not inside an hc terminal (HC_TERMINAL_ID is not set); run soho from a terminal started with hc" {
		t.Fatalf("message = %q", message)
	}
	if calls := f.calls(t); len(calls) != 0 {
		t.Fatalf("hc calls = %#v, want none", calls)
	}

	// Inside a terminal whose record cannot be read: the session-read
	// message, not the not-inside one, and still not a Herdr error.
	failing := t.TempDir()
	bin := filepath.Join(failing, "bin")
	if _, err := fakecli.Install(t, bin, "hc", []fakecli.Rule{
		{Argv: []string{"get", "t1", "--json"}, Code: 1, Stderr: `{"error":{"code":"internal","message":"boom"}}`},
	}); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{
		"PATH":                bin,
		"SOHO_TRANSPORT":      "hc",
		"SOHO_FAKECLI_CONFIG": bin,
		"HC_TERMINAL_ID":      "t1",
	}
	code, message = dieIn(t, func() { WorkspaceID(nil, env, cwd) })
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if message != "transport=hc: the caller terminal's soho session could not be read (hc get failed for HC_TERMINAL_ID); check that hc is reachable" {
		t.Fatalf("message = %q", message)
	}
}

// DJA-604 stage 2b: WorkspaceIDOrError answers the session under hc and
// returns the hc error outside a terminal (never a Herdr error).
func TestWorkspaceIDOrErrorHcMode(t *testing.T) {
	f := newHcWorkspaceFixture(t, true, "sX")
	id, err := WorkspaceIDOrError(f.env)
	if err != nil || id != "sX" {
		t.Fatalf("WorkspaceIDOrError = %q, %v; want sX, nil", id, err)
	}

	outside := newHcWorkspaceFixture(t, false, "")
	if _, err := WorkspaceIDOrError(outside.env); err == nil || err.Error() != "transport=hc: not inside an hc terminal (HC_TERMINAL_ID is not set)" {
		t.Fatalf("error = %v, want the hc not-inside error", err)
	}
}

// DJA-604 stage 2b: SessionConfPath under hc is the state root's
// <session>/session.conf when the session resolves, and "" without one —
// the herdr pane current fallback is skipped, never run.
func TestSessionConfPathHcMode(t *testing.T) {
	f := newHcWorkspaceFixture(t, true, "sX")
	cwd := t.TempDir()
	want := filepath.Join(f.env["SOHO_DIR"], "sX", "session.conf")
	if got := SessionConfPath(nil, f.env, cwd); got != want {
		t.Fatalf("SessionConfPath = %q, want %q", got, want)
	}

	outside := newHcWorkspaceFixture(t, false, "")
	if got := SessionConfPath(nil, outside.env, cwd); got != "" {
		t.Fatalf("SessionConfPath outside an hc terminal = %q, want empty", got)
	}
	if calls := outside.calls(t); len(calls) != 0 {
		t.Fatalf("hc calls = %#v, want none", calls)
	}
}

// DJA-604 stage 2b: the herdr default is unchanged — a HERDR_WORKSPACE_ID
// still wins and no binary is launched (the fixture's PATH holds only the
// fake hc, so any herdr call would fail to resolve).
func TestWorkspaceIDHerdrDefaultUnchanged(t *testing.T) {
	base := t.TempDir()
	bin := filepath.Join(base, "bin")
	if _, err := fakecli.Install(t, bin, "hc", nil); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{
		"PATH":               bin,
		"HERDR_ENV":          "1",
		"HERDR_WORKSPACE_ID": "ws9",
	}
	cwd := t.TempDir()
	if got := WorkspaceID(nil, env, cwd); got != "ws9" {
		t.Fatalf("WorkspaceID = %q, want the HERDR_WORKSPACE_ID ws9", got)
	}
	if calls, err := fakecli.ReadCalls(filepath.Join(bin, "hc.calls.jsonl")); err == nil && len(calls) != 0 {
		t.Fatalf("hc calls = %d, want none", len(calls))
	}
}
