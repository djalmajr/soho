package plugin

import (
	contextpkg "context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// pickerTeamFixture installs the fake soho CLI (roster, roles) with
// env capture, plus the fake herdr (which the view must never call: the
// target comes from the selected row, not the focused pane). The env
// carries the shell HERDR_* decoys that the target ids must win over.
// It returns the env, the CLI path and the local row's cwd.
func pickerTeamFixture(t *testing.T, rules []fakecli.Rule) (platform.Env, string, string) {
	t.Helper()
	dir := t.TempDir()
	cwd := t.TempDir()
	cli, err := fakecli.InstallWithOptions(t, dir, "cli", rules, fakecli.InstallOptions{
		CaptureEnv: []string{"HERDR_WORKSPACE_ID", "HERDR_PANE_ID", "HERDR_TAB_ID", "SOHO_NOWRITE"},
	})
	if err != nil {
		t.Fatal(err)
	}
	herdr, err := fakecli.Install(t, dir, "herdr", nil)
	if err != nil {
		t.Fatal(err)
	}
	env := platform.Env{}
	for _, entry := range fakecli.Env(testutil.CleanEnv(t), dir, fakecli.EnvOptions{IncludeBasePath: true}) {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			env[key] = value
		}
	}
	env["HERDR_BIN_PATH"] = herdr
	env["HERDR_WORKSPACE_ID"] = "shell-ws"
	env["HERDR_PANE_ID"] = "shell-pane"
	env["HERDR_TAB_ID"] = "shell-tab"
	env["SOHO_NOWRITE"] = "0"
	return env, cli, cwd
}

// The view's fake reads answer the JSON contract (DJA-375 B): roster
// --json and roles --json, exactly.
const pickerTeamRosterFixture = `{"workspace":"w1","scope":"workspace","settings":{"layout":"split","reuse_workers":"on","multi_role":"on","auto_approve":"off"},"agents":[{"name":"worker-1","role":"implementer","previous_role":"","kind":"codex","pane":"w1:p1","tab":"1","state":"working","report":"pending","cwd":"/x/w1","task":"implement"}],"others":[]}`

const pickerTeamRolesFixture = `{"roles":[{"role":"planner","kind":"claude","model":"grok","effort":"","mode":"read-only","lane":"","kind_from":"","model_from":"","effort_from":"","file":"/x/planner.md"},{"role":"implementer","kind":"codex","model":"gpt-6","effort":"low","mode":"edit","lane":"","kind_from":"","model_from":"","effort_from":"","file":"/x/implementer.md"}]}`

func pickerTeamFixtureOK(t *testing.T) (platform.Env, string, string) {
	return pickerTeamFixture(t, []fakecli.Rule{
		{Argv: []string{"roster", "--json"}, Stdout: pickerTeamRosterFixture},
		{Argv: []string{"roles", "--json"}, Stdout: pickerTeamRolesFixture},
	})
}

// pickerTeamEntries is the picker's list for the tests: a local row (the
// view's target) and a remote row (the slice-B message).
func pickerTeamEntries(t *testing.T, cwd string) []PickerEntry {
	t.Helper()
	return []PickerEntry{
		{
			"ref": "local/w1:p1", "machine": "local",
			"workspace_id": "w1", "workspace_label": "soho-ws",
			"tab_id": "w1:t1", "tab_label": "1", "pane_id": "p1",
			"name": "first", "kind": "codex", "status": "working",
			"cwd": cwd,
		},
		{
			"ref": "windows/w2:p1", "machine": "windows",
			"workspace_id": "w2", "workspace_label": "remote-ws",
			"tab_id": "w2:t1", "tab_label": "1", "pane_id": "p1",
			"name": "second", "kind": "claude", "status": "idle",
			"cwd": "C:\\repo",
		},
	}
}

func pickerTeamTestState(entries []PickerEntry) *PickerState {
	s := NewPickerState()
	s.ViewState.Enabled = true
	s.Entries = entries
	return s
}

// pickerTeamLoadAndApply runs the open view's load off the key path (the
// loop's seam: runPickerTeamViewLoad in its worker) and applies the
// finished result on the test's goroutine (the loop's goroutine role).
func pickerTeamLoadAndApply(t *testing.T, s *PickerState, env platform.Env, cli string) {
	t.Helper()
	v := s.TeamView
	results := make(chan pickerTeamViewResult, 1)
	go runPickerTeamViewLoad(contextpkg.Background(), cli, env, platform.Current(), v, func(res pickerTeamViewResult) {
		results <- res
	})
	select {
	case res := <-results:
		if !s.applyPickerTeamView(res) {
			t.Fatal("the finished load was not applied to the open view")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("timeout waiting for the team-view load to publish its result")
	}
}

// pickerTeamCalls reads the fake CLI's call log (or the fake herdr's):
// a missing log means the fake was never called.
func pickerTeamCalls(t *testing.T, bin string) []fakecli.Call {
	t.Helper()
	// fakecli names the config after the binary without its .exe suffix.
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(filepath.Dir(bin), strings.TrimSuffix(filepath.Base(bin), ".exe")+".json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("call log for %s: %v", bin, err)
	}
	return calls
}

func TestPickerTeamViewIOpensLocalRowWithNowriteReads(t *testing.T) {
	env, cli, cwd := pickerTeamFixtureOK(t)
	s := pickerTeamTestState(pickerTeamEntries(t, cwd))
	if action := s.ApplyKey("i"); action != "" {
		t.Fatalf("i on the list action=%q want empty (the view stays open)", action)
	}
	v := s.TeamView
	if v == nil {
		t.Fatal("i on the list did not open the team view")
	}
	if v.remote || v.targetErr != "" || v.loaded {
		t.Fatalf("view just opened: remote=%v targetErr=%q loaded=%v", v.remote, v.targetErr, v.loaded)
	}
	if v.target.WorkspaceID != "w1" || v.target.PaneID != "p1" || v.target.Cwd != cwd {
		t.Fatalf("target=%+v want the row's workspace w1, pane p1 and cwd %s", v.target, cwd)
	}
	pickerTeamLoadAndApply(t, s, env, cli)
	if !v.loaded {
		t.Fatal("the applied load did not mark the view loaded")
	}
	frame := renderSessionPopup(pickerTeamViewOptions(s, ""), 80, 24)
	for _, want := range []string{"TEAM  soho-ws · local", "layout split", "AGENTS (1)", "worker-1", "OUTSIDE LANES", "planner"} {
		if !strings.Contains(frame, want) {
			t.Errorf("frame missing %q:\n%s", want, frame)
		}
	}
	// The two reads, exactly, in order, each a NOWRITE read with the
	// row's target ids (the shell decoys must not leak in).
	calls := pickerTeamCalls(t, cli)
	if len(calls) != 2 || !reflect.DeepEqual(calls[0].Argv, []string{"roster", "--json"}) || !reflect.DeepEqual(calls[1].Argv, []string{"roles", "--json"}) {
		t.Fatalf("CLI calls=%#v want exactly roster --json then roles --json", calls)
	}
	for i, call := range calls {
		if call.Env["SOHO_NOWRITE"] != "1" {
			t.Errorf("call %d %v: SOHO_NOWRITE=%q want 1", i, call.Argv, call.Env["SOHO_NOWRITE"])
		}
		if call.Env["HERDR_WORKSPACE_ID"] != "w1" || call.Env["HERDR_PANE_ID"] != "p1" {
			t.Errorf("call %d: target ids=%q want the row's w1/p1, not the shell decoys", i, call.Env)
		}
		if tab, ok := call.Env["HERDR_TAB_ID"]; ok {
			t.Errorf("call %d: HERDR_TAB_ID=%q want unset (the reads need no tab id)", i, tab)
		}
	}
	// The focused pane is not consulted: no herdr call at all.
	if herdrCalls := pickerTeamCalls(t, filepath.Join(filepath.Dir(cli), "herdr")); len(herdrCalls) != 0 {
		t.Fatalf("herdr calls=%#v want none (the target comes from the row)", herdrCalls)
	}
}

func TestPickerTeamViewISearchTextInSearchFocus(t *testing.T) {
	_, _, cwd := pickerTeamFixtureOK(t)
	s := pickerTeamTestState(pickerTeamEntries(t, cwd))
	s.ViewState.SearchFocused = true
	s.Query = "ab"
	if action := s.ApplyKey("i"); action != "" {
		t.Fatalf("i in the search focus action=%q", action)
	}
	if s.Query != "abi" || !s.ViewState.SearchFocused || s.TeamView != nil {
		t.Fatalf("query=%q focused=%v view=%v want i appended to the search", s.Query, s.ViewState.SearchFocused, s.TeamView)
	}
	// The legacy input keeps i as query text (the frozen contract the
	// picker oracle pins).
	legacy := NewPickerState()
	legacy.Entries = pickerTeamEntries(t, cwd)
	legacy.Query = "p"
	legacy.ApplyKey("i")
	if legacy.Query != "pi" || legacy.TeamView != nil {
		t.Fatalf("legacy query=%q view=%v want i appended to the query", legacy.Query, legacy.TeamView)
	}
}

func TestPickerTeamViewRolesFailureKeepsRoster(t *testing.T) {
	env, cli, cwd := pickerTeamFixture(t, []fakecli.Rule{
		{Argv: []string{"roster", "--json"}, Stdout: pickerTeamRosterFixture},
		{Argv: []string{"roles", "--json"}, Stderr: "roles boom", Code: 1},
	})
	s := pickerTeamTestState(pickerTeamEntries(t, cwd))
	s.ApplyKey("i")
	pickerTeamLoadAndApply(t, s, env, cli)
	v := s.TeamView
	if v.rosterFailure != "" || v.rolesFailure == "" {
		t.Fatalf("failures roster=%q roles=%q want only the roles failure", v.rosterFailure, v.rolesFailure)
	}
	frame := renderSessionPopup(pickerTeamViewOptions(s, ""), 80, 24)
	for _, want := range []string{"AGENTS (1)", "worker-1", "roles: roles failed (exit 1): roles boom"} {
		if !strings.Contains(frame, want) {
			t.Errorf("frame missing %q:\n%s", want, frame)
		}
	}
	if strings.Contains(frame, "planner") {
		t.Errorf("the failed roles block kept its roles:\n%s", frame)
	}
}

func TestPickerTeamViewEmptyRosterShowsInvalidOutput(t *testing.T) {
	// An empty roster read is not valid JSON: the view shows the
	// invalid-output cause in its own block (the settings line is gone
	// with the roster) and the roles block keeps its content.
	env, cli, cwd := pickerTeamFixture(t, []fakecli.Rule{
		{Argv: []string{"roster", "--json"}, Stdout: ""},
		{Argv: []string{"roles", "--json"}, Stdout: pickerTeamRolesFixture},
	})
	s := pickerTeamTestState(pickerTeamEntries(t, cwd))
	s.ApplyKey("i")
	pickerTeamLoadAndApply(t, s, env, cli)
	v := s.TeamView
	if !v.loaded || v.rosterFailure == "" {
		t.Fatalf("view loaded=%v rosterFailure=%q want the failure", v.loaded, v.rosterFailure)
	}
	frame := renderSessionPopup(pickerTeamViewOptions(s, ""), 80, 24)
	for _, want := range []string{"roster: invalid output", "OUTSIDE LANES", "planner"} {
		if !strings.Contains(frame, want) {
			t.Errorf("frame missing %q:\n%s", want, frame)
		}
	}
	if strings.Contains(frame, "layout split") {
		t.Errorf("the failed roster block kept the settings line:\n%s", frame)
	}
}

func TestPickerTeamViewMissingCwdShowsCauseWithoutCLI(t *testing.T) {
	_, cli, cwd := pickerTeamFixtureOK(t)
	entries := pickerTeamEntries(t, cwd)
	entries[0]["cwd"] = cwd + "/missing"
	s := pickerTeamTestState(entries)
	if action := s.ApplyKey("i"); action != "" {
		t.Fatalf("i action=%q", action)
	}
	v := s.TeamView
	if v == nil || v.targetErr == "" || v.loaded {
		t.Fatalf("view=%+v want the invalid cwd cause", v)
	}
	if !strings.Contains(v.targetErr, "pane cwd does not exist: "+cwd+"/missing") {
		t.Fatalf("cause=%q want the missing cwd cause", v.targetErr)
	}
	frame := renderSessionPopup(pickerTeamViewOptions(s, ""), 80, 24)
	if !strings.Contains(frame, "pane cwd does not exist:") {
		t.Fatalf("frame missing the cause:\n%s", frame)
	}
	if calls := pickerTeamCalls(t, cli); len(calls) != 0 {
		t.Fatalf("CLI calls=%#v want none for an invalid cwd", calls)
	}
	// The view still closes back to the list with the same selection.
	if s.ApplyKey("esc") != "" || s.TeamView != nil || s.Selected != 0 {
		t.Fatalf("esc with view=%v selected=%d want back to the list", s.TeamView, s.Selected)
	}
}

func TestPickerTeamViewRemoteRowShowsSliceBMessageWithoutCLI(t *testing.T) {
	_, cli, cwd := pickerTeamFixtureOK(t)
	s := pickerTeamTestState(pickerTeamEntries(t, cwd))
	s.Selected = 1
	if action := s.ApplyKey("i"); action != "" {
		t.Fatalf("i action=%q", action)
	}
	v := s.TeamView
	if v == nil || !v.remote || v.targetErr != "" || v.loaded {
		t.Fatalf("view=%+v want the remote view", v)
	}
	frame := renderSessionPopup(pickerTeamViewOptions(s, ""), 80, 24)
	if !strings.Contains(frame, "team view: not available yet for remote machines") {
		t.Fatalf("frame missing the slice-B message:\n%s", frame)
	}
	if calls := pickerTeamCalls(t, cli); len(calls) != 0 {
		t.Fatalf("CLI calls=%#v want none for a remote row", calls)
	}
	if herdrCalls := pickerTeamCalls(t, filepath.Join(filepath.Dir(cli), "herdr")); len(herdrCalls) != 0 {
		t.Fatalf("herdr calls=%#v want none for a remote row", herdrCalls)
	}
}

func TestPickerTeamViewEscReturnsToTheSameSelection(t *testing.T) {
	env, cli, cwd := pickerTeamFixtureOK(t)
	cwdB := t.TempDir()
	entries := pickerTeamEntries(t, cwd)
	entries[0] = PickerEntry{
		"ref": "local/w1:p1", "machine": "local",
		"workspace_id": "w1", "workspace_label": "soho-ws",
		"tab_id": "w1:t1", "tab_label": "1", "pane_id": "p1",
		"name": "first", "kind": "codex", "status": "working",
		"cwd": cwd,
	}
	entries[1] = PickerEntry{
		"ref": "local/w3:p2", "machine": "local",
		"workspace_id": "w3", "workspace_label": "zzz-ws",
		"tab_id": "w3:t1", "tab_label": "1", "pane_id": "p2",
		"name": "third", "kind": "claude", "status": "idle",
		"cwd": cwdB,
	}
	s := pickerTeamTestState(entries)
	s.Selected = 1
	s.ApplyKey("i")
	pickerTeamLoadAndApply(t, s, env, cli)
	// The view is read-only: the list's keys do not act underneath it.
	if action := s.ApplyKey("enter"); action != "" || s.NavPending {
		t.Fatalf("enter with the view open action=%q navPending=%v want no navigation", action, s.NavPending)
	}
	if action := s.ApplyKey("c"); action != "" || s.Copied != nil {
		t.Fatalf("c with the view open action=%q copied=%v want nothing", action, s.Copied)
	}
	// Esc goes back to the list with the same selection, by the full ref.
	if s.ApplyKey("esc") != "" || s.TeamView != nil {
		t.Fatalf("esc exit=%q view=%v want the list back", s.Exit, s.TeamView)
	}
	if s.Selected != 1 || pickerString(s.visible()[s.Selected]["ref"]) != "local/w3:p2" {
		t.Fatalf("selection=%d ref=%q want the row the view opened on", s.Selected, pickerString(s.visible()[s.Selected]["ref"]))
	}
	// i is the toggle: it re-opens the same row's view.
	s.ApplyKey("i")
	if v := s.TeamView; v == nil || v.entryRef != "local/w3:p2" {
		t.Fatalf("re-open view=%+v want the same row", v)
	}
	// Ctrl-C closes the modal even with the view open.
	if action := s.ApplyKey("ctrl-c"); action != "esc" || s.Exit != "esc" {
		t.Fatalf("ctrl-c action=%q exit=%q want the modal closed", action, s.Exit)
	}
}

func TestPickerTeamViewStaleResultFromAnotherRowIsDiscarded(t *testing.T) {
	// The fake answers differently per call (Call counter), so a stale
	// result is visible in the frame: row A's roster and row B's roster
	// are distinct agents.
	cwdA, cwdB := t.TempDir(), t.TempDir()
	env, cli, _ := pickerTeamFixture(t, []fakecli.Rule{
		// The Call counter is 1-based (the current call is logged before
		// the match), so the first call is Call:1 and the second Call:2.
		{Argv: []string{"roster", "--json"}, Call: 1, Stdout: `{"agents":[{"name":"roster-of-row-a","kind":"codex","state":"working"}]}`},
		{Argv: []string{"roster", "--json"}, Call: 2, Stdout: `{"agents":[{"name":"roster-of-row-b","kind":"codex","state":"working"}]}`},
		{Argv: []string{"roles", "--json"}, Call: 1, Stdout: `{"roles":[{"role":"roles-a","kind":"claude","mode":"edit"}]}`},
		{Argv: []string{"roles", "--json"}, Call: 2, Stdout: `{"roles":[{"role":"roles-b","kind":"claude","mode":"edit"}]}`},
	})
	entries := pickerTeamEntries(t, cwdA)
	entries[1] = PickerEntry{
		"ref": "local/w9:p2", "machine": "local",
		"workspace_id": "w9", "workspace_label": "zzz-ws",
		"tab_id": "w9:t1", "tab_label": "1", "pane_id": "p2",
		"name": "ninth", "kind": "claude", "status": "idle",
		"cwd": cwdB,
	}
	s := pickerTeamTestState(entries)

	s.ApplyKey("i") // row A, sequence 1
	if v := s.TeamView; v == nil || v.loadSeq != 1 || v.entryRef != "local/w1:p1" {
		t.Fatalf("first open view=%+v", v)
	}
	var resA pickerTeamViewResult
	runPickerTeamViewLoad(contextpkg.Background(), cli, env, platform.Current(), s.TeamView, func(res pickerTeamViewResult) { resA = res })

	// The selection moves to row B and the view re-opens (sequence 2):
	// A's in-flight result is stale.
	s.ApplyKey("esc")
	s.ApplyKey("down")
	s.ApplyKey("i")
	if v := s.TeamView; v == nil || v.loadSeq != 2 || v.entryRef != "local/w9:p2" {
		t.Fatalf("second open view=%+v", v)
	}
	if s.applyPickerTeamView(resA) {
		t.Fatal("the stale result of another row was applied")
	}
	if s.TeamView.loaded {
		t.Fatal("row B's view took row A's result")
	}
	var resB pickerTeamViewResult
	runPickerTeamViewLoad(contextpkg.Background(), cli, env, platform.Current(), s.TeamView, func(res pickerTeamViewResult) { resB = res })
	if !s.applyPickerTeamView(resB) {
		t.Fatal("row B's own result was not applied")
	}
	frame := renderSessionPopup(pickerTeamViewOptions(s, ""), 80, 24)
	if !strings.Contains(frame, "roster-of-row-b") || strings.Contains(frame, "roster-of-row-a") {
		t.Fatalf("frame shows the wrong row's roster:\n%s", frame)
	}

	// Re-opening the same row supersedes its own earlier result: the
	// sequence check, not just the ref, discards it.
	s.ApplyKey("esc")
	s.ApplyKey("i") // row B again, sequence 3
	if v := s.TeamView; v == nil || v.loadSeq != 3 {
		t.Fatalf("re-open view=%+v", v)
	}
	if s.applyPickerTeamView(resB) {
		t.Fatal("the result of a superseded open was applied")
	}
	if s.TeamView.loaded {
		t.Fatal("the re-opened view took the earlier result")
	}
}

func TestPickerTeamViewFooterDisclosesTheIShortcut(t *testing.T) {
	_, _, cwd := pickerTeamFixtureOK(t)
	s := pickerTeamTestState(pickerTeamEntries(t, cwd))
	listFrame := renderSessionPopup(pickerTeamViewOptions(s, ""), 80, 24)
	if !strings.Contains(listFrame, "i Team") {
		t.Fatalf("picker list frame missing the i Team shortcut:\n%s", listFrame)
	}
	// The board does not bind i: its frame never discloses the shortcut.
	board := SessionRenderOptions{Board: true, Entries: []PickerEntry{}}
	if frame := renderSessionPopup(board, 80, 24); strings.Contains(frame, "i Team") {
		t.Fatalf("board frame discloses the picker's shortcut:\n%s", frame)
	}
}

func TestPickerTeamViewScrollsArrowsAndPages(t *testing.T) {
	// The roster read carries 15 agents: the view's content is the title
	// section, the AGENTS (15) section (15 lines) and the roles' OUTSIDE
	// LANES section.
	var roster strings.Builder
	roster.WriteString(`{"agents":[`)
	for i := 1; i <= 15; i++ {
		if i > 1 {
			roster.WriteString(",")
		}
		fmt.Fprintf(&roster, `{"name":"r%02d","role":"tasker","kind":"codex","pane":"w1:p%d","state":"working","task":"task"}`, i, i)
	}
	roster.WriteString(`],"others":[]}`)
	env, cli, cwd := pickerTeamFixture(t, []fakecli.Rule{
		{Argv: []string{"roster", "--json"}, Stdout: roster.String()},
		{Argv: []string{"roles", "--json"}, Stdout: pickerTeamRolesFixture},
	})
	s := pickerTeamTestState(pickerTeamEntries(t, cwd))
	s.ApplyKey("i")
	pickerTeamLoadAndApply(t, s, env, cli)
	frame := func() string { return renderSessionPopup(pickerTeamViewOptions(s, ""), 80, 24) }

	if got := frame(); !strings.Contains(got, "AGENTS (15)") || !strings.Contains(got, "r01") {
		t.Fatalf("fresh view missing its content:\n%s", got)
	}
	s.ApplyKey("down") // scroll 1: the title line drops out
	if got := frame(); strings.Contains(got, "TEAM  soho-ws · local") || !strings.Contains(got, "r01") {
		t.Fatalf("scroll 1 kept the title or lost the first line:\n%s", got)
	}
	s.ApplyKey("down") // scroll 2: the settings line drops out
	if got := frame(); strings.Contains(got, "layout split") || !strings.Contains(got, "AGENTS (15)") || !strings.Contains(got, "r01") {
		t.Fatalf("scroll 2 wrong:\n%s", got)
	}
	s.ApplyKey("page-down") // scroll 12: the early agents are out, the roles section is in
	if got := frame(); strings.Contains(got, "r01") || !strings.Contains(got, "r13") || !strings.Contains(got, "OUTSIDE LANES") {
		t.Fatalf("page-down did not jump ten lines:\n%s", got)
	}
	s.ApplyKey("page-up") // scroll 2: the first line is back
	if got := frame(); !strings.Contains(got, "r01") {
		t.Fatalf("page-up left the view at the wrong offset:\n%s", got)
	}
	for i := 0; i < 5; i++ {
		s.ApplyKey("up") // clamps at 0
	}
	if got := frame(); !strings.Contains(got, "r01") {
		t.Fatalf("up at the top did not clamp:\n%s", got)
	}
	if v := s.TeamView; v.scroll != 0 {
		t.Fatalf("scroll=%d want 0 at the top", v.scroll)
	}
}
