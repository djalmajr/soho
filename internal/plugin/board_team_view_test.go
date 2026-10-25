package plugin

import (
	contextpkg "context"
	"encoding/json"
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

// boardTeamState is the board runtime state for the team-view tests (DJA-317
// slice B): the enhanced (production) display over the slice A's fixture
// rows, on a fresh NewBoardState - the runtime the picker command runs on.
func boardTeamState(entries []PickerEntry) *BoardState {
	s := NewBoardState()
	s.ViewState.Enabled = true
	s.Entries = entries
	return s
}

// boardTeamLoadAndApply runs the open view's load off the key path (the
// loop's seam: runPickerTeamViewLoad in its worker) and applies the
// finished result on the test's goroutine (the loop's goroutine role).
func boardTeamLoadAndApply(t *testing.T, s *BoardState, env platform.Env, cli string) {
	t.Helper()
	v := s.TeamView
	results := make(chan pickerTeamViewResult, 1)
	go runPickerTeamViewLoad(contextpkg.Background(), cli, env, platform.Current(), v, func(res pickerTeamViewResult) {
		results <- res
	})
	select {
	case res := <-results:
		if !boardApplyTeamResult(s, res) {
			t.Fatal("the finished load was not applied to the open view")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("timeout waiting for the team-view load to publish its result")
	}
}

func TestBoardTeamViewIOpensLocalRowWithNowriteReads(t *testing.T) {
	env, cli, cwd := pickerTeamFixtureOK(t)
	s := boardTeamState(pickerTeamEntries(t, cwd))
	if action := s.ApplyKey("i"); action != "team" {
		t.Fatalf("i on the list action=%q want team (the loop starts the load)", action)
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
	// The loop's seam: the load runs off the key path and only the
	// finished result is applied on the state's goroutine.
	boardTeamLoadAndApply(t, s, env, cli)
	if !v.loaded {
		t.Fatal("the applied load did not mark the view loaded")
	}
	frame := renderSessionModal(s, 80, 24, "")
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

func TestBoardTeamViewISearchTextAndGates(t *testing.T) {
	_, _, cwd := pickerTeamFixtureOK(t)
	// In the enhanced search focus, i is ordinary search text.
	s := boardTeamState(pickerTeamEntries(t, cwd))
	s.ViewState.SearchFocused = true
	s.Query = "ab"
	if action := s.ApplyKey("i"); action != "" {
		t.Fatalf("i in the search focus action=%q", action)
	}
	if s.Query != "abi" || !s.ViewState.SearchFocused || s.TeamView != nil {
		t.Fatalf("query=%q focused=%v view=%v want i appended to the search", s.Query, s.ViewState.SearchFocused, s.TeamView)
	}
	// The legacy input keeps i as query text (the frozen contract).
	legacy := NewBoardState()
	legacy.Entries = pickerTeamEntries(t, cwd)
	legacy.Query = "p"
	if action := legacy.ApplyKey("i"); action != "" {
		t.Fatalf("legacy i action=%q", action)
	}
	if legacy.Query != "pi" || legacy.TeamView != nil {
		t.Fatalf("legacy query=%q view=%v want i appended to the query", legacy.Query, legacy.TeamView)
	}
	// Nothing while a navigation is pending (the picker's rule).
	pending := boardTeamState(pickerTeamEntries(t, cwd))
	pending.NavPending = true
	if action := pending.ApplyKey("i"); action != "" || pending.TeamView != nil {
		t.Fatalf("pending i action=%q view=%v want nothing", action, pending.TeamView)
	}
	// Nothing once the modal is closing.
	closing := boardTeamState(pickerTeamEntries(t, cwd))
	closing.Exit = "esc"
	if action := closing.ApplyKey("i"); action != "" || closing.TeamView != nil {
		t.Fatalf("closing i action=%q view=%v want nothing", action, closing.TeamView)
	}
}

func TestBoardTeamViewEscReturnsToTheSameSelection(t *testing.T) {
	env, cli, cwd := pickerTeamFixtureOK(t)
	cwdB := t.TempDir()
	entries := pickerTeamEntries(t, cwd)
	entries[1] = PickerEntry{
		"ref": "local/w3:p2", "machine": "local",
		"workspace_id": "w3", "workspace_label": "zzz-ws",
		"tab_id": "w3:t1", "tab_label": "1", "pane_id": "p2",
		"name": "third", "kind": "claude", "status": "idle",
		"cwd": cwdB,
	}
	s := boardTeamState(entries)
	s.Selected = 1
	if action := s.ApplyKey("i"); action != "team" {
		t.Fatalf("i on the list action=%q want team", action)
	}
	boardTeamLoadAndApply(t, s, env, cli)
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
	if action := s.ApplyKey("i"); action != "team" {
		t.Fatalf("re-open action=%q want team", action)
	}
	if v := s.TeamView; v == nil || v.entryRef != "local/w3:p2" {
		t.Fatalf("re-open view=%+v want the same row", v)
	}
	// Ctrl-C closes the modal even with the view open.
	if action := s.ApplyKey("ctrl-c"); action != "esc" || s.Exit != "esc" {
		t.Fatalf("ctrl-c action=%q exit=%q want the modal closed", action, s.Exit)
	}
}

func TestBoardTeamViewStaleResultFromAnotherRowIsDiscarded(t *testing.T) {
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
	s := boardTeamState(entries)

	if action := s.ApplyKey("i"); action != "team" {
		t.Fatalf("first open action=%q want team", action)
	}
	if v := s.TeamView; v == nil || v.loadSeq != 1 || v.entryRef != "local/w1:p1" {
		t.Fatalf("first open view=%+v", v)
	}
	var resA pickerTeamViewResult
	runPickerTeamViewLoad(contextpkg.Background(), cli, env, platform.Current(), s.TeamView, func(res pickerTeamViewResult) { resA = res })

	// The selection moves to row B and the view re-opens (sequence 2):
	// A's in-flight result is stale.
	s.ApplyKey("esc")
	s.ApplyKey("down")
	if action := s.ApplyKey("i"); action != "team" {
		t.Fatalf("second open action=%q want team", action)
	}
	if v := s.TeamView; v == nil || v.loadSeq != 2 || v.entryRef != "local/w9:p2" {
		t.Fatalf("second open view=%+v", v)
	}
	if boardApplyTeamResult(s, resA) {
		t.Fatal("the stale result of another row was applied")
	}
	if s.TeamView.loaded {
		t.Fatal("row B's view took row A's result")
	}
	var resB pickerTeamViewResult
	runPickerTeamViewLoad(contextpkg.Background(), cli, env, platform.Current(), s.TeamView, func(res pickerTeamViewResult) { resB = res })
	if !boardApplyTeamResult(s, resB) {
		t.Fatal("row B's own result was not applied")
	}
	frame := renderSessionModal(s, 80, 24, "")
	if !strings.Contains(frame, "roster-of-row-b") || strings.Contains(frame, "roster-of-row-a") {
		t.Fatalf("frame shows the wrong row's roster:\n%s", frame)
	}

	// Re-opening the same row supersedes its own earlier result: the
	// sequence check, not just the ref, discards it.
	s.ApplyKey("esc")
	if action := s.ApplyKey("i"); action != "team" {
		t.Fatalf("re-open action=%q want team", action)
	}
	if v := s.TeamView; v == nil || v.loadSeq != 3 {
		t.Fatalf("re-open view=%+v", v)
	}
	if boardApplyTeamResult(s, resB) {
		t.Fatal("the result of a superseded open was applied")
	}
	if s.TeamView.loaded {
		t.Fatal("the re-opened view took the earlier result")
	}
}

func TestBoardTeamViewRenderSessionModalDrawsTheTeamTitle(t *testing.T) {
	env, cli, cwd := pickerTeamFixtureOK(t)
	s := boardTeamState(pickerTeamEntries(t, cwd))
	if action := s.ApplyKey("i"); action != "team" {
		t.Fatalf("i action=%q", action)
	}
	// The title is drawn while the load is still in flight.
	frame := renderSessionModal(s, 80, 24, "")
	if !strings.Contains(frame, "TEAM  soho-ws · local") || !strings.Contains(frame, "loading team…") {
		t.Fatalf("frame missing the team title while loading:\n%s", frame)
	}
	boardTeamLoadAndApply(t, s, env, cli)
	frame = renderSessionModal(s, 80, 24, "")
	for _, want := range []string{"TEAM  soho-ws · local", "AGENTS (1)", "worker-1"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("frame missing %q after the load:\n%s", want, frame)
		}
	}
}

func TestBoardTeamViewFooterDisclosesIShortcut(t *testing.T) {
	_, _, cwd := pickerTeamFixtureOK(t)
	s := boardTeamState(pickerTeamEntries(t, cwd))
	// The board runtime's enhanced frame discloses the shortcut, in the
	// same footer action line as the other shortcuts.
	frame := renderSessionModal(s, 80, 24, "")
	if !strings.Contains(frame, "i Team") {
		t.Fatalf("board runtime frame missing the i Team shortcut:\n%s", frame)
	}
	// The legacy (non-enhanced) frame never discloses the shortcut.
	legacy := NewBoardState()
	legacy.flavorPicker = true
	legacy.Entries = pickerTeamEntries(t, cwd)
	if frame := renderSessionModal(legacy, 80, 24, ""); strings.Contains(frame, "i Team") {
		t.Fatalf("legacy frame discloses the enhanced shortcut:\n%s", frame)
	}
	// The view's frame discloses the view's own footer (i/Esc goes back
	// to the list, where the i Team shortcut is back too).
	s.ApplyKey("i")
	if frame := renderSessionModal(s, 80, 24, ""); !strings.Contains(frame, "i/Esc Back · Ctrl-C Close") {
		t.Fatalf("team view frame missing the view's footer segments:\n%s", frame)
	}
	s.ApplyKey("esc")
	if frame := renderSessionModal(s, 80, 24, ""); !strings.Contains(frame, "i Team") {
		t.Fatalf("the list back from the view lost the i Team shortcut:\n%s", frame)
	}
}

// boardTeamLoopSnapshot is the discovery snapshot for the loop test: one
// local agent pane whose cwd is the fixture directory (the view's target
// must be valid for the loop to start the load).
func boardTeamLoopSnapshot(cwd string) string {
	// The cwd is JSON-encoded: a Windows path carries backslashes.
	quoted, _ := json.Marshal(cwd)
	return `{"result":{"snapshot":{"workspaces":[{"workspace_id":"w1","label":"alpha"}],"tabs":[{"tab_id":"w1:t1"}],"agents":[{"pane_id":"w1:p1","name":"orchestrator","agent":"codex","agent_status":"working","title":"orchestrator: run"}],"panes":[{"pane_id":"w1:p1","workspace_id":"w1","tab_id":"w1:t1","cwd":` + string(quoted) + `}]}}}`
}

// TestBoardTeamViewLoopOpensLoadsAndReturns drives the one production
// session runtime (the picker command's loop) end to end: i opens the
// team view, the loop's worker runs the NOWRITE reads off the key path
// and applies the finished result on the loop's goroutine, the frame
// carries the ROSTER and ROLES blocks with the board's "i Team" footer,
// and the two Escs - back to the list, then close - exit with code 0.
func TestBoardTeamViewLoopOpensLoadsAndReturns(t *testing.T) {
	cwd := t.TempDir()
	dir := t.TempDir()
	herdr, err := fakecli.Install(t, dir, "herdr", []fakecli.Rule{
		{Argv: []string{"api", "snapshot"}, Stdout: boardTeamLoopSnapshot(cwd)},
	})
	if err != nil {
		t.Fatal(err)
	}
	cli, err := fakecli.InstallWithOptions(t, dir, "cli", []fakecli.Rule{
		{Argv: []string{"roster", "--json"}, Stdout: pickerTeamRosterFixture},
		{Argv: []string{"roles", "--json"}, Stdout: pickerTeamRolesFixture},
	}, fakecli.InstallOptions{
		CaptureEnv: []string{"HERDR_WORKSPACE_ID", "HERDR_PANE_ID", "HERDR_TAB_ID", "SOHO_NOWRITE"},
	})
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
	env["HERDR_WORKSPACE_ID"] = "shell-ws" // the row's target must win over the shell decoy
	env["HERDR_PANE_ID"] = "shell-pane"
	env["SOHO_NOWRITE"] = "0"
	input, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := os.CreateTemp(t.TempDir(), "board-")
	if err != nil {
		_ = input.Close()
		_ = writer.Close()
		t.Fatal(err)
	}
	oldInterval := boardRefreshInterval
	boardRefreshInterval = time.Hour // the 10 s ticker must not interfere
	shutdown := make(chan os.Signal, 1)
	finished := make(chan int, 1)
	loopDone := make(chan struct{})
	t.Cleanup(func() {
		select {
		case shutdown <- os.Interrupt:
		default:
		}
		_ = writer.Close()
		select {
		case <-loopDone:
		case <-time.After(5 * time.Second):
			t.Error("board loop did not stop during cleanup")
			return // keep its descriptors and shared configuration alive
		}
		_ = input.Close()
		_ = output.Close()
		boardRefreshInterval = oldInterval
	})
	state := NewBoardState()
	go func() {
		defer close(loopDone)
		finished <- withPickerTerminalMouse(true, func() (func() error, error) { return func() error { return nil }, nil }, output, state, func() int {
			return runSessionLoopWithSignals(env, "darwin", cli, input, output, true, shutdown, true, state)
		})
	}()

	// The unified display lists the row (the local agent pane).
	waitOutput(t, output.Name(), "w1:p1")
	// i opens the view: the frame shows the title while the load runs,
	// and the loop's worker applies the finished result - the frame then
	// carries the ROSTER and the ROLES blocks.
	if _, err := writer.Write([]byte("i")); err != nil {
		t.Fatal(err)
	}
	waitOutput(t, output.Name(), "TEAM  alpha · local")
	waitOutput(t, output.Name(), "AGENTS (1)")
	waitOutput(t, output.Name(), "worker-1")
	waitOutput(t, output.Name(), "OUTSIDE LANES")
	waitOutput(t, output.Name(), "planner")
	dat, _ := os.ReadFile(output.Name())
	if !strings.Contains(lastFrame(t, dat), "TEAM  alpha · local") {
		t.Fatalf("the last frame is not the team view:\n%s", lastFrame(t, dat))
	}
	if frameBandedLineContains(t, dat, "orchestrator") {
		t.Fatalf("the team view frame kept the list's banded row:\n%s", lastFrame(t, dat))
	}
	if !strings.Contains(lastFrame(t, dat), "i/Esc Back · Ctrl-C Close") {
		t.Fatalf("the team view frame's footer missing the view's segments:\n%s", lastFrame(t, dat))
	}
	// The two NOWRITE reads with the row's target ids (the shell decoys
	// must not leak in).
	calls := pickerTeamCalls(t, cli)
	if len(calls) != 2 || !reflect.DeepEqual(calls[0].Argv, []string{"roster", "--json"}) || !reflect.DeepEqual(calls[1].Argv, []string{"roles", "--json"}) {
		t.Fatalf("CLI calls=%#v want exactly roster --json then roles --json", calls)
	}
	for i, call := range calls {
		if call.Env["SOHO_NOWRITE"] != "1" {
			t.Errorf("call %d %v: SOHO_NOWRITE=%q want 1", i, call.Argv, call.Env["SOHO_NOWRITE"])
		}
		if call.Env["HERDR_WORKSPACE_ID"] != "w1" || call.Env["HERDR_PANE_ID"] != "w1:p1" {
			t.Errorf("call %d: target ids=%q want the row's w1/w1:p1, not the shell decoys", i, call.Env)
		}
	}
	// The first Esc goes back to the list (the view closes, the modal
	// stays open); the second closes the modal with code 0. The row
	// bands its agent name (the ref lives in the detail line).
	if _, err := writer.Write([]byte("\x1b")); err != nil {
		t.Fatal(err)
	}
	waitForBandedLine(t, output.Name(), "orchestrator")
	dat, _ = os.ReadFile(output.Name())
	if strings.Contains(lastFrame(t, dat), "TEAM  alpha · local") {
		t.Fatalf("the first Esc did not return to the list:\n%s", lastFrame(t, dat))
	}
	if _, err := writer.Write([]byte("\x1b")); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	select {
	case code := <-finished:
		if code != 0 {
			t.Fatalf("board code=%d want 0", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("board did not close after Esc")
	}
}

// TestBoardTeamViewIIsSearchTextWhileThePinIsMissing pins the order of
// the i key's gates: in the search focus i is search text even during a
// refresh whose pinned selection left the list; only the list-focus open
// is refused while the pin is missing.
func TestBoardTeamViewIIsSearchTextWhileThePinIsMissing(t *testing.T) {
	_, _, cwd := pickerTeamFixtureOK(t)
	s := boardTeamState(pickerTeamEntries(t, cwd))
	s.refreshing = true
	s.selectedRef = "local/w9:p9"
	if !s.pinnedMissing() {
		t.Fatal("fixture: the pinned selection should be missing during the refresh")
	}
	s.ViewState.SearchFocused = true
	s.Query = "ab"
	if action := s.ApplyKey("i"); action != "" {
		t.Fatalf("i in the search focus action=%q", action)
	}
	if s.Query != "abi" || s.TeamView != nil {
		t.Fatalf("query=%q view=%v want i appended to the search while the pin is missing", s.Query, s.TeamView)
	}
	s.ViewState.SearchFocused = false
	if action := s.ApplyKey("i"); action != "" || s.TeamView != nil {
		t.Fatalf("list-focus i with a missing pin action=%q view=%v want nothing", action, s.TeamView)
	}
}

// TestBoardTeamLoadKeepsTheCurrentCancelOnAStaleResult pins the loop's
// load bookkeeping: a late result of a superseded open does not retire
// the cancel of the load that replaced it, so closing the modal still
// cancels that load; the current load's own result retires it.
func TestBoardTeamLoadKeepsTheCurrentCancelOnAStaleResult(t *testing.T) {
	var load boardTeamLoad
	first := load.start(1)
	second := load.start(2)
	if first.Err() == nil {
		t.Fatal("a new open did not cancel the superseded load")
	}
	load.finish(pickerTeamViewResult{seq: 1})
	if second.Err() != nil {
		t.Fatal("the stale result cancelled the current load")
	}
	load.stop()
	if second.Err() == nil {
		t.Fatal("closing the modal after a stale result did not cancel the current load")
	}
	third := load.start(3)
	load.finish(pickerTeamViewResult{seq: 3})
	if third.Err() == nil || load.cancel != nil {
		t.Fatal("the current load's own result did not retire its cancel")
	}
}

// Shutdown joins both current and superseded writers, even after cancellation.
func TestBoardTeamViewShutdownJoinsCancelledLoads(t *testing.T) {
	dir := t.TempDir()
	var load boardTeamLoad
	release := make(chan struct{})
	cancelled := make(chan struct{}, 2)
	writesDone := make(chan struct{}, 2)
	finished := make(chan struct{})
	t.Cleanup(func() {
		close(release)
		// Join independently of shutdown so a broken shutdown also leaves
		// no writer behind when this regression test fails.
		for i := 0; i < 2; i++ {
			select {
			case <-writesDone:
			case <-time.After(5 * time.Second):
				t.Error("released team-view writer did not finish")
				return
			}
		}
		select {
		case <-finished:
			for _, name := range []string{"1", "2"} {
				data, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil || string(data) != "late write" {
					t.Errorf("joined writer %s: data=%q err=%v", name, data, err)
				}
			}
		case <-time.After(5 * time.Second):
			t.Error("team-view shutdown did not join its workers")
		}
	})
	for seq := 1; seq <= 2; seq++ {
		ctx := load.start(seq)
		name := filepath.Join(dir, string(rune('0'+seq)))
		load.run(func() {
			defer func() { writesDone <- struct{}{} }()
			<-ctx.Done()
			cancelled <- struct{}{}
			<-release
			if err := os.WriteFile(name, []byte("late write"), 0o600); err != nil {
				t.Error(err)
			}
		})
	}
	go func() {
		load.shutdown()
		close(finished)
	}()
	for i := 0; i < 2; i++ {
		select {
		case <-cancelled:
		case <-time.After(5 * time.Second):
			t.Fatal("shutdown did not cancel the current and superseded loads")
		}
	}
	select {
	case <-finished:
		t.Fatal("shutdown returned with team-view writers still running")
	case <-time.After(100 * time.Millisecond):
		// Both workers are deliberately held after cancellation. Cleanup
		// releases them and waits before TempDir removes their directory.
	}
}

// startBoardTeamLoop runs the production session loop over the team-view
// fixture with the view-opening row listed, and returns the input writer,
// the output path, the shutdown channel and the loop's exit code channel.
func startBoardTeamLoop(t *testing.T) (*os.File, string, chan os.Signal, chan int) {
	t.Helper()
	cwd := t.TempDir()
	dir := t.TempDir()
	herdr, err := fakecli.Install(t, dir, "herdr", []fakecli.Rule{
		{Argv: []string{"api", "snapshot"}, Stdout: boardTeamLoopSnapshot(cwd)},
	})
	if err != nil {
		t.Fatal(err)
	}
	cli, err := fakecli.Install(t, dir, "cli", []fakecli.Rule{
		{Argv: []string{"roster", "--json"}, Stdout: pickerTeamRosterFixture},
		{Argv: []string{"roles", "--json"}, Stdout: pickerTeamRolesFixture},
	})
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
	input, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := os.CreateTemp(t.TempDir(), "board-")
	if err != nil {
		_ = input.Close()
		_ = writer.Close()
		t.Fatal(err)
	}
	oldInterval := boardRefreshInterval
	boardRefreshInterval = time.Hour
	shutdown := make(chan os.Signal, 1)
	finished := make(chan int, 1)
	loopDone := make(chan struct{})
	t.Cleanup(func() {
		select {
		case shutdown <- os.Interrupt:
		default:
		}
		_ = writer.Close()
		select {
		case <-loopDone:
		case <-time.After(5 * time.Second):
			t.Error("board loop did not stop during cleanup")
			return
		}
		_ = input.Close()
		_ = output.Close()
		boardRefreshInterval = oldInterval
	})
	state := NewBoardState()
	go func() {
		defer close(loopDone)
		finished <- withPickerTerminalMouse(true, func() (func() error, error) { return func() error { return nil }, nil }, output, state, func() int {
			return runSessionLoopWithSignals(env, "darwin", cli, input, output, true, shutdown, true, state)
		})
	}()
	waitOutput(t, output.Name(), "w1:p1")
	if _, err := writer.Write([]byte("i")); err != nil {
		t.Fatal(err)
	}
	waitOutput(t, output.Name(), "TEAM  alpha · local")
	return writer, output.Name(), shutdown, finished
}

// TestBoardTeamViewEOFClosesTheModalWithTheViewOpen: the end of the input
// ends the popup even while the team view is open, instead of closing only
// the view and leaving the loop waiting for input that never comes.
func TestBoardTeamViewEOFClosesTheModalWithTheViewOpen(t *testing.T) {
	writer, _, _, finished := startBoardTeamLoop(t)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-finished:
		if code != 0 {
			t.Fatalf("exit code=%d want 0 at EOF", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("EOF with the team view open did not end the modal")
	}
}

// TestBoardTeamViewShutdownSignalClosesTheModalWithTheViewOpen: a
// shutdown signal ends the popup even while the team view is open.
func TestBoardTeamViewShutdownSignalClosesTheModalWithTheViewOpen(t *testing.T) {
	_, _, shutdown, finished := startBoardTeamLoop(t)
	shutdown <- os.Interrupt
	select {
	case code := <-finished:
		if code != 0 {
			t.Fatalf("exit code=%d want 0 on the shutdown signal", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the shutdown signal with the team view open did not end the modal")
	}
}
