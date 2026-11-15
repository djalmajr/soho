package plugin

import (
	contextpkg "context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestBoardPTYProbeEntrypoint(t *testing.T) {
	executable := os.Getenv("HERDR_SOHO_BOARD_PTY_EXE")
	if executable == "" {
		return
	}
	os.Exit(RunBoard(platform.EnvFromOS(), platform.Current(), executable))
}

func boardEntry(ref, machine, wsID, wsLabel, name, kind, status, title string) PickerEntry {
	entry := PickerEntry{
		"ref":             ref,
		"machine":         machine,
		"workspace_id":    wsID,
		"workspace_label": wsLabel,
		"tab_id":          wsID + ":t1",
		"pane_id":         ref,
		"name":            name,
	}
	if kind != "" {
		entry["kind"] = kind
	}
	if status != "" {
		entry["status"] = status
	}
	if title != "" {
		entry["title"] = title
	}
	return entry
}

func boardEnv(t *testing.T, dir, herdr string) platform.Env {
	t.Helper()
	env := platform.Env{}
	for _, item := range fakecli.Env(testutil.CleanEnv(t), dir, fakecli.EnvOptions{IncludeBasePath: true}) {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			env[key] = value
		}
	}
	env["HERDR_BIN_PATH"] = herdr
	return env
}

// boardSnapshotRow is one agent pane of a fake `herdr api snapshot` answer.
type boardSnapshotRow struct {
	paneID string
	name   string
	agent  string
	status string
	title  string
}

// boardSnapshotJSON renders one `herdr api snapshot` answer: the given rows
// as agents and panes under one workspace.
func boardSnapshotJSON(wsID, wsLabel string, rows []boardSnapshotRow) string {
	agents := make([]string, 0, len(rows))
	panes := make([]string, 0, len(rows))
	for _, row := range rows {
		agent := fmt.Sprintf(`{"pane_id":%q,"name":%q,"agent":%q,"agent_status":%q`, row.paneID, row.name, row.agent, row.status)
		if row.title != "" {
			agent = fmt.Sprintf(`%s,"title":%q`, agent, row.title)
		}
		agents = append(agents, agent+"}")
		panes = append(panes, fmt.Sprintf(`{"pane_id":%q,"workspace_id":%q,"tab_id":%q}`, row.paneID, wsID, wsID+":t1"))
	}
	return fmt.Sprintf(`{"result":{"snapshot":{"workspaces":[{"workspace_id":%q,"label":%q}],"tabs":[{"tab_id":%q}],"agents":[%s],"panes":[%s]}}}`,
		wsID, wsLabel, wsID+":t1", strings.Join(agents, ","), strings.Join(panes, ","))
}

func TestBoardOrderGroupsAndOrchestratorFirst(t *testing.T) {
	// Machines arrive with the remote first; local must still come first,
	// workspaces sort by workspace_label, and within a workspace the
	// orchestrator names come before the rest, by name.
	entries := []PickerEntry{
		boardEntry("win/p1", "win", "ws-p", "pinar", "implementer-3", "codex", "blocked", "implementer: fix the login flow"),
		boardEntry("local/a1", "local", "ws-a", "alpha", "orchestrator-10", "claude", "working", "orchestrator: ship the board"),
		boardEntry("win/p2", "win", "ws-p", "pinar", "orchestrator", "claude", "working", "orchestrator: oversee"),
		boardEntry("local/z1", "local", "ws-z", "zeta", "worker-b", "grok", "idle", "research: perf"),
		boardEntry("local/a2", "local", "ws-a", "alpha", "researcher", "codex", "idle", ""),
		boardEntry("local/z2", "local", "ws-z", "zeta", "orchestrator-1", "codex", "done", ""),
		boardEntry("local/a3", "local", "ws-a", "alpha", "", "", "", ""), // no agent: excluded
	}
	got := make([]string, 0, len(entries))
	for _, entry := range boardAgents(entries) {
		got = append(got, pickerString(entry["ref"]))
	}
	want := []string{"local/a1", "local/a2", "local/z2", "local/z1", "win/p2", "win/p1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("board order=%#v want %#v", got, want)
	}
	state := NewBoardState()
	state.Entries = entries
	render := RenderBoard(state, 100)
	lines := strings.Split(strings.TrimSuffix(render, "\n"), "\n")
	headerIndex := map[string]int{}
	for i, line := range lines {
		if strings.HasPrefix(line, "· ") || strings.Contains(line, " (ws-") {
			headerIndex[line] = i
		}
	}
	wantHeaders := []string{"local · alpha (ws-a)", "local · zeta (ws-z)", "win · pinar (ws-p)"}
	var positions []int
	for _, header := range wantHeaders {
		pos, ok := headerIndex[header]
		if !ok {
			t.Fatalf("missing group header %q in:\n%s", header, render)
		}
		positions = append(positions, pos)
	}
	if positions[0] > positions[1] || positions[1] > positions[2] {
		t.Fatalf("group headers out of order:\n%s", render)
	}
	wantRows := map[string]bool{
		">* orchestrator-10 claude working ship the board":  true, // selected: cursor column + marker
		"   researcher codex idle -":                        true, // cursor + idle marker are both spaces
		"   orchestrator-1 codex done -":                    true,
		"   worker-b grok idle perf":                        true,
		" * orchestrator claude working oversee":            true,
		" ! implementer-3 codex blocked fix the login flow": true,
	}
	for _, line := range lines {
		delete(wantRows, line)
	}
	if len(wantRows) > 0 {
		t.Fatalf("missing or cursorless board rows %#v in:\n%s", wantRows, render)
	}
	if strings.Contains(render, "agentes") {
		t.Fatalf("the count footer line is still there:\n%s", render)
	}
	if !strings.HasPrefix(lines[0], "6 agents") {
		t.Fatalf("totals line=%q want the 6-agent count first", lines[0])
	}
}

func TestBoardFilterMatchesTitle(t *testing.T) {
	state := NewBoardState()
	state.Entries = []PickerEntry{
		boardEntry("local/a1", "local", "ws-a", "alpha", "scouter-1", "codex", "working", "board: explore the api"),
		boardEntry("local/a2", "local", "ws-a", "alpha", "implementer-1", "claude", "idle", "fix the login flow"),
		boardEntry("local/a3", "local", "ws-a", "alpha", "orchestrator", "codex", "working", "ship the board"),
	}
	state.Query = "board api"
	got := make([]string, 0)
	for _, entry := range state.visible() {
		got = append(got, pickerString(entry["ref"]))
	}
	if !reflect.DeepEqual(got, []string{"local/a1"}) {
		t.Fatalf("title filter=%#v want [local/a1]", got)
	}
	state.Query = "claude idle"
	got = got[:0]
	for _, entry := range state.visible() {
		got = append(got, pickerString(entry["ref"]))
	}
	if !reflect.DeepEqual(got, []string{"local/a2"}) {
		t.Fatalf("picker-field filter still works: got=%#v want [local/a2]", got)
	}
	state.Query = ""
	if n := len(state.visible()); n != 3 {
		t.Fatalf("empty query shows all agents: got=%d want 3", n)
	}
}

func TestBoardMarkersTotalsAndClock(t *testing.T) {
	entries := []PickerEntry{
		boardEntry("local/a1", "local", "ws-a", "alpha", "orchestrator-1", "claude", "working", "one"),
		boardEntry("local/a2", "local", "ws-a", "alpha", "implementer-1", "codex", "blocked", "two"),
		boardEntry("local/a3", "local", "ws-a", "alpha", "scouter-1", "grok", "idle", "three"),
		boardEntry("local/a4", "local", "ws-a", "alpha", "worker-1", "codex", "done", "four"),
		boardEntry("local/a5", "local", "ws-a", "alpha", "worker-2", "codex", "working", "five"),
	}
	state := NewBoardState()
	state.Entries = entries
	render := RenderBoard(state, 100)
	lines := strings.Split(strings.TrimSuffix(render, "\n"), "\n")
	if lines[0] != "5 agents · 2 working · 1 blocked · 1 idle · 1 done" {
		t.Fatalf("totals line=%q", lines[0])
	}
	// Rows carry the cursor column (only the selected row) before the
	// status marker: line 0 totals, line 1 query, line 2 the group header.
	wantRows := []string{
		">* orchestrator-1 claude working one",
		" ! implementer-1 codex blocked two",
		"   scouter-1 grok idle three", // cursor + idle marker are both spaces
		"   worker-1 codex done four",
		" * worker-2 codex working five",
	}
	for i, want := range wantRows {
		if lines[3+i] != want {
			t.Fatalf("row %d=%q want %q", i, lines[3+i], want)
		}
	}
	// The cursor follows up/down.
	state.ApplyKey("down")
	render = RenderBoard(state, 100)
	if !strings.Contains(render, ">! implementer-1") || strings.Contains(render, ">* orchestrator-1") {
		t.Fatalf("cursor did not follow down:\n%s", render)
	}
	state.ApplyKey("up")
	render = RenderBoard(state, 100)
	if !strings.Contains(render, ">* orchestrator-1") {
		t.Fatalf("cursor did not follow up:\n%s", render)
	}
	// The count footer is gone: the top totals line already gives it.
	if strings.Contains(render, "agentes") {
		t.Fatalf("the count footer is still there:\n%s", render)
	}
	// Zero-count statuses stay out of the line.
	idle := NewBoardState()
	idle.Entries = []PickerEntry{boardEntry("local/a1", "local", "ws-a", "alpha", "orchestrator-1", "claude", "working", "one")}
	if render := RenderBoard(idle, 100); strings.Contains(render, "1 agente") || !strings.Contains(render, ">* orchestrator-1 claude working one") {
		t.Fatalf("idle board render:\n%s", render)
	}
	if line := strings.SplitN(RenderBoard(idle, 100), "\n", 2)[0]; line != "1 agents · 1 working" {
		t.Fatalf("totals line without zero statuses=%q", line)
	}
	// The last-update time is the clock's HH:MM:SS, local.
	oldNow := boardNow
	boardNow = func() time.Time { return time.Date(2026, 10, 3, 9, 45, 30, 0, time.Local) }
	defer func() { boardNow = oldNow }()
	state.UpdatedAt = boardNow().Format("15:04:05")
	if line := strings.SplitN(RenderBoard(state, 100), "\n", 2)[0]; line != "5 agents · 2 working · 1 blocked · 1 idle · 1 done · 09:45:30" {
		t.Fatalf("totals line with time=%q", line)
	}
	// The cut width subtracts the cursor column: the agent lines still fit
	// the board width (cursor + marker + space = 3 prefix columns).
	narrow := strings.Split(strings.TrimSuffix(RenderBoard(state, 30), "\n"), "\n")
	for i := 3; i < 8; i++ {
		if jsLength(narrow[i]) > 30 {
			t.Fatalf("agent line wider than the 30-column board: %q", narrow[i])
		}
	}
}

func TestBoardTitleStripsRolePrefixAndControls(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"orchestrator: ship the board", "ship the board"},
		{"implementer: fix: the login", "fix: the login"}, // only the role prefix goes
		{"security-reviewer: scan the api", "scan the api"},
		{"no prefix title", "no prefix title"},
		{"a b: c d", "a b: c d"}, // the segment before ": " has a space: not a role
		{"orchestrator: \x1b[31mred\x1b[0m task\x00x", "red taskx"},
		{"", ""},
	} {
		if got := BoardTitle(tc.in); got != tc.want {
			t.Fatalf("BoardTitle(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
	entry := boardEntry("local/a1", "local", "ws-a", "alpha", "implementer-3", "codex", "blocked", "implementer: a very long task title that must be cut")
	if line := BoardEntryLine(entry, 40); line != "implementer-3 codex blocked a very long…" {
		t.Fatalf("cut row=%q", line)
	}
}

func TestBoardUpdateKeepsSelectionByRef(t *testing.T) {
	live := NewBoardState()
	live.Entries = []PickerEntry{
		boardEntry("local/a1", "local", "ws-a", "alpha", "orchestrator-1", "claude", "working", "one"),
		boardEntry("local/a2", "local", "ws-a", "alpha", "implementer-1", "codex", "idle", "two"),
		boardEntry("local/a3", "local", "ws-a", "alpha", "scouter-1", "grok", "idle", "three"),
	}
	live.Selected = 1 // local/a2
	// A fresh load reorders the entries and adds one; the selection must
	// stay on local/a2.
	update := NewBoardState()
	update.Entries = []PickerEntry{
		boardEntry("local/a3", "local", "ws-a", "alpha", "scouter-1", "grok", "idle", "three"),
		boardEntry("local/a2", "local", "ws-a", "alpha", "implementer-1", "codex", "blocked", "two"),
		boardEntry("local/a4", "local", "ws-a", "alpha", "worker-9", "codex", "idle", "four"),
		boardEntry("local/a1", "local", "ws-a", "alpha", "orchestrator-1", "claude", "working", "one"),
	}
	update.FinishedMachines = []string{"local"}
	applyBoardUpdate(live, update)
	selected := pickerString(live.visible()[live.Selected]["ref"])
	if selected != "local/a2" {
		t.Fatalf("selection after update=%q want local/a2 (visible=%#v)", selected, live.visible())
	}
	// The ref disappears mid-generation: the selection stays where it is -
	// only the complete load may fall back to the top.
	update2 := NewBoardState()
	update2.Entries = []PickerEntry{
		boardEntry("local/a1", "local", "ws-a", "alpha", "orchestrator-1", "claude", "working", "one"),
		boardEntry("local/a4", "local", "ws-a", "alpha", "worker-9", "codex", "idle", "four"),
	}
	update2.FinishedMachines = []string{"local"}
	applyBoardUpdate(live, update2)
	if live.Selected != 1 {
		t.Fatalf("selection mid-generation after the ref disappeared=%d want 1 (the load is not complete yet)", live.Selected)
	}
	// Only the complete load (UpdatedAt set) drops the selection to the top.
	update3 := NewBoardState()
	update3.UpdatedAt = "12:00:00"
	update3.Entries = append([]PickerEntry(nil), update2.Entries...)
	update3.FinishedMachines = []string{"local"}
	applyBoardUpdate(live, update3)
	if live.Selected != 0 {
		t.Fatalf("selection after the ref disappeared at the end of the load=%d want 0", live.Selected)
	}
	// The query is kept across updates.
	live.Query = "two"
	update4 := NewBoardState()
	update4.Entries = []PickerEntry{boardEntry("local/a2", "local", "ws-a", "alpha", "implementer-1", "codex", "idle", "two")}
	update4.FinishedMachines = []string{"local"}
	applyBoardUpdate(live, update4)
	if live.Query != "two" {
		t.Fatalf("query after update=%q want two", live.Query)
	}
}

// TestBoardPendingMachineKeepsAllOldRows: a machine that has not answered
// the new load keeps ALL of its old rows - not just the first - already on
// the refresh's first publication, and the selection keeps working on them.
func TestBoardPendingMachineKeepsAllOldRows(t *testing.T) {
	s := NewBoardState()
	s.Entries = []PickerEntry{
		boardEntry("middle/a", "middle", "w", "w", "a", "codex", "idle", "a"),
		boardEntry("middle/b", "middle", "w", "w", "b", "codex", "idle", "b"),
	}
	s.Selected = 1
	u := NewBoardState()
	u.LoadingLocal = true
	u.Loading = 1
	applyBoardUpdate(s, u)
	if got := boardRefs(s.Entries); len(got) != 2 || got[0] != "middle/a" || got[1] != "middle/b" {
		t.Fatalf("pending machine rows=%v want both old rows of the middle machine", got)
	}
	s.ApplyKey("c")
	if got := pickerString(s.LastEntry["ref"]); got != "middle/b" {
		t.Fatalf("c copied %q want middle/b (the second pending row)", got)
	}
}

// The second load's snapshots all succeed without agents. Each answered
// machine drops its old rows even while another machine is still loading.
// The complete empty generation leaves no old refs and resets selection.
func TestBoardEmptyGenerationDropsOldRefsAtCompletion(t *testing.T) {
	envA, _, release, logA, _ := boardStreamFixture(t, "middle/w1:p1", "middle")
	live := NewBoardState()
	gateA := boardRemoteGate(t, release, "a", []string{"slow", "fast", "middle"}, live, logA)
	loadBoardEntries(contextpkg.Background(), live, envA, platform.Current(), gateA)
	if len(live.visible()) == 0 {
		t.Fatalf("the first load left the board empty: %#v", live.Entries)
	}
	live.Selected = 1 // a non-top row (the board lists local first)
	dir := t.TempDir()
	empty := boardSnapshotJSON("w1", "alpha", nil)
	exeEmpty, err := fakecli.Install(t, dir, "herdr-empty", []fakecli.Rule{
		{Argv: []string{"api", "snapshot"}, Stdout: empty},
		{Argv: []string{"--machine", "slow", "api", "snapshot"}, Stdout: empty},
		{Argv: []string{"--machine", "fast", "api", "snapshot"}, Stdout: empty},
		{Argv: []string{"--machine", "middle", "api", "snapshot"}, Stdout: empty},
		{Argv: []string{"machine", "list", "--json"}, Stdout: `[{"label":"slow","enabled":true},{"label":"fast","enabled":true},{"label":"middle","enabled":true}]`},
	})
	if err != nil {
		t.Fatal(err)
	}
	loaded := NewBoardState()
	loadBoardEntries(contextpkg.Background(), loaded, boardEnv(t, dir, exeEmpty), platform.Current(), func() {
		applyBoardUpdate(live, cloneBoardState(loaded))
	})
	// Every machine has answered by now, though the loop has not yet
	// attached UpdatedAt. A successful empty answer must remove stale refs.
	if got := len(boardRefs(live.Entries)); got != 0 {
		t.Fatalf("answered empty machines kept %d stale rows: %v", got, boardRefs(live.Entries))
	}
	loaded.UpdatedAt = boardNow().Format("15:04:05")
	applyBoardUpdate(live, cloneBoardState(loaded))
	if len(live.Entries) != 0 {
		t.Fatalf("the completed empty load kept old rows: %v", boardRefs(live.Entries))
	}
	if live.Selected != 0 {
		t.Fatalf("selection after the empty completion=%d want 0", live.Selected)
	}
}

// TestBoardMachineLeavingTheListDisappearsAtCompletion: a machine that is no
// longer in the machine list keeps its old rows while the new load is in
// flight (its find never runs, so it never finishes), but the complete load
// publishes only the new result, so its rows disappear and the selection
// falls to the top.
func TestBoardMachineLeavingTheListDisappearsAtCompletion(t *testing.T) {
	localRow := boardEntry("local/a1", "local", "w", "alpha", "orchestrator-1", "claude", "working", "one")
	live := NewBoardState()
	live.Entries = []PickerEntry{localRow, boardEntry("gone/g1", "gone", "w", "beta", "worker-1", "codex", "idle", "two")}
	live.Selected = 1
	update := NewBoardState()
	update.Entries = []PickerEntry{localRow}
	update.FinishedMachines = []string{"local"}
	applyBoardUpdate(live, update)
	if got := boardRefs(live.Entries); len(got) != 2 || got[1] != "gone/g1" {
		t.Fatalf("mid-load rows=%v want the gone machine's row to stay until the load completes", got)
	}
	update2 := NewBoardState()
	update2.UpdatedAt = boardNow().Format("15:04:05")
	update2.Entries = []PickerEntry{localRow}
	update2.FinishedMachines = []string{"local"}
	applyBoardUpdate(live, update2)
	if got := boardRefs(live.Entries); len(got) != 1 || got[0] != "local/a1" {
		t.Fatalf("final rows=%v want only the new load's rows (the gone machine disappears)", got)
	}
	if live.Selected != 0 {
		t.Fatalf("selection after the machine left=%d want 0", live.Selected)
	}
}

// TestBoardFailedMachineSwapsOldRowsForFailureLine: a machine whose find
// fails in the new load terminates its find, so its old rows drop out (even
// mid-load) and today's failure line takes their place.
func TestBoardFailedMachineSwapsOldRowsForFailureLine(t *testing.T) {
	localRow := boardEntry("local/a1", "local", "w", "alpha", "orchestrator-1", "claude", "working", "one")
	live := NewBoardState()
	live.Entries = []PickerEntry{localRow,
		boardEntry("slow/s1", "slow", "w", "beta", "worker-1", "codex", "idle", "two"),
		boardEntry("slow/s2", "slow", "w", "beta", "worker-2", "codex", "idle", "three")}
	live.Selected = 2
	update := NewBoardState()
	update.Entries = []PickerEntry{localRow}
	update.FinishedMachines = []string{"local", "slow"}
	update.Failures = []PickerFailure{{Label: "slow", Cause: "exit 127: no rule"}}
	applyBoardUpdate(live, update)
	if got := boardRefs(live.Entries); len(got) != 1 || got[0] != "local/a1" {
		t.Fatalf("mid-load rows=%v want the failed machine's old rows swapped out", got)
	}
	if render := RenderBoard(live, 80); !strings.Contains(render, "máquina slow: falhou") {
		t.Fatalf("the failure line is missing from the render:\n%s", render)
	}
	update2 := NewBoardState()
	update2.UpdatedAt = boardNow().Format("15:04:05")
	update2.Entries = []PickerEntry{localRow}
	update2.FinishedMachines = []string{"local", "slow"}
	update2.Failures = []PickerFailure{{Label: "slow", Cause: "exit 127: no rule"}}
	applyBoardUpdate(live, update2)
	if got := boardRefs(live.Entries); len(got) != 1 || got[0] != "local/a1" {
		t.Fatalf("final rows=%v want only the new load's rows", got)
	}
}

// boardPinnedMissingFixture puts the board in the pinned-missing state:
// middle/b is selected, a refresh publication has local and middle finished
// (middle without the selected ref) and slow is still pending - the numeric
// index now points at slow/c.
func boardPinnedMissingFixture(t *testing.T) *BoardState {
	t.Helper()
	s := NewBoardState()
	s.Entries = []PickerEntry{
		boardEntry("local/a", "local", "w", "w", "a", "codex", "idle", "a"),
		boardEntry("middle/b", "middle", "w", "w", "b", "codex", "idle", "b"),
		boardEntry("slow/c", "slow", "w", "w", "c", "codex", "idle", "c"),
	}
	s.Selected = 1 // middle/b
	u := NewBoardState()
	u.Loading = 1
	u.Entries = []PickerEntry{s.Entries[0]}
	u.FinishedMachines = []string{"local", "middle"}
	applyBoardUpdate(s, u)
	if !s.pinnedMissing() {
		t.Fatal("the fixture must leave the board in the pinned-missing state")
	}
	return s
}

// boardCursorLines counts the agent rows carrying the cursor (every line
// starting with "> " except the query line, which is line 1).
func boardCursorLines(render string) int {
	n := 0
	for i, line := range strings.Split(strings.TrimSuffix(render, "\n"), "\n") {
		if i == 1 {
			continue
		}
		if strings.HasPrefix(line, "> ") {
			n++
		}
	}
	return n
}

// TestBoardCDoesNotCopyDifferentSessionWhileSelectedRefGone is the
// review's probe: with middle/b selected, middle's find finishes without it
// and slow is still pending, so the numeric index points at slow/c; c
// must not copy that other session and the board stays open.
func TestBoardCDoesNotCopyDifferentSessionWhileSelectedRefGone(t *testing.T) {
	s := boardPinnedMissingFixture(t)
	action := s.ApplyKey("c")
	if action == "copy" {
		t.Fatalf("c copied %q while the pinned ref middle/b is gone (action=%s)", pickerString(s.LastEntry["ref"]), action)
	}
	if s.Copied != nil || s.Exit != "" {
		t.Fatalf("the board copied (Copied!=nil) or closed (exit=%q) although c must do nothing here", s.Exit)
	}
	if got := boardRefs(s.Entries); len(got) != 2 || got[0] != "local/a" || got[1] != "slow/c" {
		t.Fatalf("merged rows=%v want local/a + slow/c (middle finished without middle/b)", got)
	}
}

// TestBoardPinnedMissingShowsNoCursorAndFooter: in the pinned-missing state
// no row shows the cursor and the last line is the footer message; before
// the publication the cursor was on the selected row.
func TestBoardPinnedMissingShowsNoCursorAndFooter(t *testing.T) {
	s := NewBoardState()
	s.Entries = []PickerEntry{
		boardEntry("local/a", "local", "w", "w", "a", "codex", "idle", "a"),
		boardEntry("middle/b", "middle", "w", "w", "b", "codex", "idle", "b"),
		boardEntry("slow/c", "slow", "w", "w", "c", "codex", "idle", "c"),
	}
	s.Selected = 1
	if got := boardCursorLines(RenderBoard(s, 80)); got != 1 {
		t.Fatalf("before the refresh the render must show exactly one cursor row, got %d", got)
	}
	u := NewBoardState()
	u.Loading = 1
	u.Entries = []PickerEntry{s.Entries[0]}
	u.FinishedMachines = []string{"local", "middle"}
	applyBoardUpdate(s, u)
	render := RenderBoard(s, 80)
	if got := boardCursorLines(render); got != 0 {
		t.Fatalf("the pinned-missing render has %d cursor rows, want none:\n%s", got, render)
	}
	want := "a sessão selecionada saiu desta carga; escolha outra com ↑/↓"
	lines := strings.Split(strings.TrimSuffix(render, "\n"), "\n")
	if lines[len(lines)-1] != want {
		t.Fatalf("last line=%q want %q", lines[len(lines)-1], want)
	}
}

// TestBoardPinnedMissingArrowRepinsAndCCopies: ↑/↓ in the
// pinned-missing state pick a visible row (↑ the first, ↓ the last) and it
// becomes the new pinned ref; c then copies that row, resolved by ref.
func TestBoardPinnedMissingArrowRepinsAndCCopies(t *testing.T) {
	s := boardPinnedMissingFixture(t)
	// visible = [local/a, slow/c]; ↓ picks the last row (slow/c), which is
	// the first-and-only candidate from the cursor's point of view... and
	// on a single visible row it is the first row too:
	s2 := NewBoardState()
	s2.Entries = []PickerEntry{
		boardEntry("local/a", "local", "w", "w", "a", "codex", "idle", "a"),
		boardEntry("middle/b", "middle", "w", "w", "b", "codex", "idle", "b"),
	}
	s2.Selected = 1
	u2 := NewBoardState()
	u2.Loading = 0
	u2.Entries = []PickerEntry{s2.Entries[0]}
	u2.FinishedMachines = []string{"local", "middle"}
	applyBoardUpdate(s2, u2)
	if !s2.pinnedMissing() {
		t.Fatal("the single-row fixture must be in the pinned-missing state")
	}
	if action := s2.ApplyKey("down"); action != "" {
		t.Fatalf("down returned %q want nothing", action)
	}
	if got := pickerString(s2.visible()[s2.Selected]["ref"]); got != "local/a" {
		t.Fatalf("down picked %q want the first visible row local/a", got)
	}
	if action := s2.ApplyKey("c"); action != "copy" {
		t.Fatalf("c after re-pinning returned %q want copy", action)
	}
	if got := pickerString(s2.LastEntry["ref"]); got != "local/a" {
		t.Fatalf("c copied %q want local/a (the row picked by ↓)", got)
	}
	// On the two-row fixture: ↑ picks the first row, ↓ picks the last.
	if action := s.ApplyKey("up"); action != "" {
		t.Fatalf("up returned %q want nothing", action)
	}
	if got := pickerString(s.visible()[s.Selected]["ref"]); got != "local/a" {
		t.Fatalf("up picked %q want the first visible row local/a", got)
	}
	if action := s.ApplyKey("c"); action != "copy" || pickerString(s.LastEntry["ref"]) != "local/a" {
		t.Fatalf("c after ↑ returned %q/%q want copy local/a", action, pickerString(s.LastEntry["ref"]))
	}
}

// TestBoardPinnedMissingFallsToTopWhenTheLoadCompletes: the pinned-missing
// state ends with the load - the complete publication publishes only the
// new result, the selection falls to the top, the pin is cleared and the
// footer message goes away.
func TestBoardPinnedMissingFallsToTopWhenTheLoadCompletes(t *testing.T) {
	s := boardPinnedMissingFixture(t)
	final := NewBoardState()
	final.UpdatedAt = boardNow().Format("15:04:05")
	final.Entries = []PickerEntry{
		boardEntry("local/a", "local", "w", "w", "a", "codex", "idle", "a"),
		boardEntry("slow/c", "slow", "w", "w", "c", "codex", "idle", "c"),
	}
	final.FinishedMachines = []string{"local", "middle", "slow"}
	applyBoardUpdate(s, final)
	if s.Selected != 0 {
		t.Fatalf("selection after the load completed=%d want 0 (the ref is gone)", s.Selected)
	}
	if s.selectedRef != "" || s.refreshing {
		t.Fatalf("the pin survived the complete load (ref=%q refreshing=%v)", s.selectedRef, s.refreshing)
	}
	if lines := strings.Split(strings.TrimSuffix(RenderBoard(s, 80), "\n"), "\n"); lines[len(lines)-1] == "a sessão selecionada saiu desta carga; escolha outra com ↑/↓" {
		t.Fatal("the footer message survived the complete load")
	}
	if got := boardCursorLines(RenderBoard(s, 80)); got != 1 {
		t.Fatalf("the cursor must be back on the top row, got %d cursor rows", got)
	}
}

func TestBoardRefreshKeyRules(t *testing.T) {
	state := NewBoardState()
	state.Entries = []PickerEntry{boardEntry("local/a1", "local", "ws-a", "alpha", "orchestrator-1", "claude", "working", "one")}
	// r with an empty filter refreshes and does not enter the filter.
	if action := state.FeedChunk("r"); action != "update" || state.Query != "" {
		t.Fatalf("r with empty filter action=%q query=%q", action, state.Query)
	}
	// r with a non-empty filter is plain text.
	state.Query = "on"
	if action := state.FeedChunk("r"); action != "" || state.Query != "onr" {
		t.Fatalf("r with a filter action=%q query=%q", action, state.Query)
	}
	// Ctrl-R (0x12) refreshes at any time, keeping the filter.
	if action := state.FeedChunk(string(rune(0x12))); action != "update" || state.Query != "onr" {
		t.Fatalf("ctrl-r action=%q query=%q", action, state.Query)
	}
}

func TestBoardLoadMachineFailureBecomesStatusLine(t *testing.T) {
	dir := t.TempDir()
	local := boardSnapshotJSON("w1", "appliance", []boardSnapshotRow{{paneID: "w1:p1", name: "orchestrator", agent: "claude", status: "working", title: "orchestrator: run"}})
	herdr, err := fakecli.Install(t, dir, "herdr", []fakecli.Rule{
		{Argv: []string{"api", "snapshot"}, Stdout: local},
		{Argv: []string{"--machine", "win", "api", "snapshot"}, Code: 1, Stderr: "boom"},
		{Argv: []string{"machine", "list", "--json"}, Stdout: `[{"label":"win","enabled":true}]`},
	})
	if err != nil {
		t.Fatal(err)
	}
	state := NewBoardState()
	loadBoardEntries(contextpkg.Background(), state, boardEnv(t, dir, herdr), "darwin", func() {})
	if len(state.Failures) != 1 || state.Failures[0].Label != "win" || state.Failures[0].Cause != "boom" {
		t.Fatalf("failures=%#v", state.Failures)
	}
	if len(state.Entries) != 1 || pickerString(state.Entries[0]["ref"]) != "local/w1:p1" {
		t.Fatalf("entries=%#v", state.Entries)
	}
	if !strings.Contains(RenderBoard(state, 100), "máquina win: falhou (boom)") {
		t.Fatalf("board render without the failure line:\n%s", RenderBoard(state, 100))
	}
}

func TestBoardRefreshDuringLoadWaitsAndRunsOnce(t *testing.T) {
	dir := t.TempDir()
	local := boardSnapshotJSON("w1", "alpha", []boardSnapshotRow{
		{paneID: "w1:p1", name: "orchestrator", agent: "claude", status: "working", title: "orchestrator: run"},
		{paneID: "w1:p2", name: "implementer-1", agent: "codex", status: "idle", title: "implementer: task"},
	})
	m := boardSnapshotJSON("w9", "pinar", []boardSnapshotRow{{paneID: "p9", name: "researcher", agent: "grok", status: "working", title: "research: x"}})
	herdr, err := fakecli.Install(t, dir, "herdr", []fakecli.Rule{
		{Argv: []string{"api", "snapshot"}, Stdout: local, Delay: 400},
		{Argv: []string{"--machine", "m", "api", "snapshot"}, Stdout: m, Delay: 400},
		{Argv: []string{"machine", "list", "--json"}, Stdout: `[{"label":"m","enabled":true}]`},
	})
	if err != nil {
		t.Fatal(err)
	}
	env := boardEnv(t, dir, herdr)
	oldNow := boardNow
	boardNow = func() time.Time { return time.Date(2026, 10, 3, 9, 45, 30, 0, time.Local) }
	defer func() { boardNow = oldNow }()
	oldInterval := boardRefreshInterval
	boardRefreshInterval = time.Hour // the 10 s ticker must not interfere
	defer func() { boardRefreshInterval = oldInterval }()

	input, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := os.CreateTemp(t.TempDir(), "board-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = input.Close()
		_ = writer.Close()
		_ = output.Close()
	})
	shutdown := make(chan os.Signal, 1)
	finished := make(chan int, 1)
	go func() { finished <- runBoardLoopWithSignals(env, "darwin", herdr, input, output, false, shutdown) }()

	snapshotCalls := func() int {
		calls, err := fakecli.ReadCalls(filepath.Join(dir, "herdr.calls.jsonl"))
		if err != nil {
			return 0
		}
		n := 0
		for _, call := range calls {
			if reflect.DeepEqual(call.Argv, []string{"api", "snapshot"}) {
				n++
			}
		}
		return n
	}

	// The first load takes at least 800 ms (400 ms local + 400 ms remote);
	// this r lands while it is in progress.
	time.Sleep(150 * time.Millisecond)
	if _, err := writer.Write([]byte("r")); err != nil {
		t.Fatal(err)
	}
	// The in-flight load is not started again; the queued refresh runs
	// exactly once after it ends: local snapshots go 1 -> 2, and stop there.
	deadline := time.Now().Add(30 * time.Second) // generous: Windows process starts can be slow
	for time.Now().Before(deadline) && snapshotCalls() < 2 {
		select {
		case code := <-finished:
			t.Fatalf("board exited while refreshing, code=%d", code)
		default:
		}
		time.Sleep(20 * time.Millisecond)
	}
	if snapshotCalls() != 2 {
		t.Fatalf("local snapshots after a refresh during a load=%d want 2 (one coalesced, no duplicate)", snapshotCalls())
	}
	// The single r is consumed: before the counts are judged again, the
	// coalesced refresh must have drawn the fresh data. A fixed sleep is
	// not enough on a slow Windows CI, so wait for the content with the
	// same generous deadline as the snapshot wait above.
	fresh := func() bool {
		data, err := os.ReadFile(output.Name())
		if err != nil {
			return false
		}
		return strings.Contains(string(data), "3 agents · 2 working · 1 idle · 09:45:30") &&
			strings.Contains(string(data), "m/pinar/-") &&
			strings.Contains(string(data), "researcher")
	}
	freshDeadline := time.Now().Add(30 * time.Second) // generous: Windows process starts can be slow
	for time.Now().Before(freshDeadline) && !fresh() {
		select {
		case code := <-finished:
			t.Fatalf("board exited while refreshing, code=%d", code)
		default:
		}
		time.Sleep(20 * time.Millisecond)
	}
	data, _ := os.ReadFile(output.Name())
	if !strings.Contains(string(data), "3 agents · 2 working · 1 idle · 09:45:30") || !strings.Contains(string(data), "m/pinar/-") || !strings.Contains(string(data), "researcher") {
		t.Fatalf("board after the coalesced refresh missing the fresh data in:\n%s", data)
	}
	if snapshotCalls() != 2 {
		t.Fatalf("local snapshots after the fresh data drew=%d want 2 (the queued refresh must not stack)", snapshotCalls())
	}
	time.Sleep(300 * time.Millisecond) // the single r is consumed: nothing more follows
	if snapshotCalls() != 2 {
		t.Fatalf("local snapshots settled at %d want 2 (the queued refresh must not stack)", snapshotCalls())
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

func TestBridgeBoardOpensBoardPane(t *testing.T) {
	dir := t.TempDir()
	cwd := t.TempDir()
	data, _ := json.Marshal(map[string]any{"result": map[string]any{"pane": map[string]any{"workspace_id": "ws-a", "cwd": cwd}}})
	herdr, err := fakecli.Install(t, dir, "herdr", []fakecli.Rule{
		{Argv: []string{"pane", "get", "pane-a"}, Stdout: string(data)},
		{Argv: BoardArguments(), Stdout: "opened\n"},
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
	env["HERDR_PLUGIN_CONTEXT_JSON"] = `{"workspace_id":"ws-a","tab_id":"tab-a","focused_pane_id":"pane-a"}`
	result, err := Bridge("board", env, platform.Current(), "")
	if err != nil || result.Code != 0 || !strings.Contains(result.Out, "target workspace=ws-a pane=pane-a") {
		t.Fatalf("bridge board result=%+v err=%v", result, err)
	}
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(dir, "herdr.json"))
	if err != nil || len(calls) != 2 {
		t.Fatalf("herdr calls=%#v err=%v", calls, err)
	}
	// The board action is the compatibility alias of the unified picker:
	// the supplied bridge opens the picker entrypoint for both, under the
	// soho id (the fixture does not set HERDR_PLUGIN_ID).
	want := PickerArguments()
	if !reflect.DeepEqual(calls[1].Argv, want) {
		t.Fatalf("pane open argv=%#v want %#v", calls[1].Argv, want)
	}
}

func TestBridgeUnknownActionListsBoard(t *testing.T) {
	_, err := Bridge("frobnicate", platform.Env{}, "darwin", "")
	bridgeErr, ok := err.(*BridgeError)
	if !ok || bridgeErr.Code != ExitInvalidTarget {
		t.Fatalf("error=%v", err)
	}
	if !strings.Contains(bridgeErr.Message, "(use 'doctor', 'roster', 'board' or 'pick')") {
		t.Fatalf("unknown action message=%q", bridgeErr.Message)
	}
}

// boardStreamFixture installs two fake herdr CLIs (one per load generation,
// "a" and "b"); the machine list is the same for both. Each remote snapshot
// of a generation blocks on its own release file (a WaitFile gate named
// after the generation and the machine), so the test - not the wall clock -
// fixes each load's arrival order. Load b's middle row uses the ref and name
// given by the test. It returns each generation's environment (HERDR_BIN_PATH
// selects the generation's fake) and its call log.
func boardStreamFixture(t *testing.T, middleBRef, middleBName string) (platform.Env, platform.Env, func(string, string) error, string, string) {
	t.Helper()
	dir := t.TempDir()
	gates := filepath.Join(dir, "gates")
	if err := os.MkdirAll(gates, 0o755); err != nil {
		t.Fatal(err)
	}
	localRows := []boardSnapshotRow{{paneID: "w1:p1", name: "local", agent: "codex", status: "idle"}}
	mkRules := func(load string) []fakecli.Rule {
		rules := []fakecli.Rule{{Argv: []string{"api", "snapshot"}, Stdout: boardSnapshotJSON("w1", "alpha", localRows)}}
		for _, machine := range []string{"slow", "fast", "middle"} {
			ref, name := machine+"/w1:p1", machine
			if machine == "middle" && load == "b" {
				ref, name = middleBRef, middleBName
			}
			row := boardSnapshotRow{paneID: strings.TrimPrefix(ref, machine+"/"), name: name, agent: "codex", status: "idle"}
			rules = append(rules, fakecli.Rule{
				Argv:     []string{"--machine", machine, "api", "snapshot"},
				WaitFile: filepath.Join(gates, load+"-"+machine),
				Stdout:   boardSnapshotJSON("w1", "alpha", []boardSnapshotRow{row}),
			})
		}
		rules = append(rules, fakecli.Rule{Argv: []string{"machine", "list", "--json"}, Stdout: `[{"label":"slow","enabled":true},{"label":"fast","enabled":true},{"label":"middle","enabled":true}]`})
		return rules
	}
	exeA, err := fakecli.Install(t, dir, "herdr-a", mkRules("a"))
	if err != nil {
		t.Fatal(err)
	}
	exeB, err := fakecli.Install(t, dir, "herdr-b", mkRules("b"))
	if err != nil {
		t.Fatal(err)
	}
	release := func(load, machine string) error {
		return os.WriteFile(filepath.Join(gates, load+"-"+machine), []byte("go"), 0o644)
	}
	t.Cleanup(func() {
		// Unblock snapshots still waiting after the test (a failed wait) so
		// the fakes exit instead of polling their release file forever.
		for _, load := range []string{"a", "b"} {
			for _, machine := range []string{"slow", "fast", "middle"} {
				_ = os.WriteFile(filepath.Join(gates, load+"-"+machine), []byte("go"), 0o644)
			}
		}
	})
	return boardEnv(t, dir, exeA), boardEnv(t, dir, exeB), release, filepath.Join(dir, "herdr-a.calls.jsonl"), filepath.Join(dir, "herdr-b.calls.jsonl")
}

// boardRemoteGate returns the publish closure that starts the load's gate
// releases once the load's first batch (the local snapshot) publishes. It
// then waits until every remote snapshot has started (the generation's call
// log holds the local snapshot, the machine list and the three remotes), so
// the releases - not the spawn race - impose the arrival order; the
// releases follow 250 ms apart in `order`.
func boardRemoteGate(t *testing.T, release func(string, string) error, load string, order []string, loaded *BoardState, callsLog string) func() {
	t.Helper()
	started := false
	return func() {
		if started || loaded.LoadingLocal || loaded.Loading != 1 || len(loaded.Entries) == 0 {
			return
		}
		started = true
		stop := make(chan struct{})
		t.Cleanup(func() { close(stop) })
		go func() {
			// Five calls in the log: the local snapshot, the machine list
			// and the three remote snapshots.
			deadline := time.Now().Add(30 * time.Second)
			for time.Now().Before(deadline) {
				calls, err := fakecli.ReadCalls(callsLog)
				if err == nil && len(calls) >= 5 {
					break
				}
				select {
				case <-stop:
					return
				case <-time.After(20 * time.Millisecond):
				}
			}
			for i, machine := range order {
				if i > 0 {
					select {
					case <-stop:
						return
					case <-time.After(250 * time.Millisecond):
					}
				}
				if err := release(load, machine); err != nil {
					select {
					case <-stop:
						return
					default:
						t.Error(err)
					}
					return
				}
			}
		}()
	}
}

func boardRefs(list []PickerEntry) []string {
	refs := make([]string, len(list))
	for i, entry := range list {
		refs[i] = pickerString(entry["ref"])
	}
	return refs
}

// TestBoardStreamingSelectionKeepsSelectedRef is the review's selection
// probe as a test: two real loads through the fakes, every publication of the
// second applied by the production consumer. The selection follows the ref -
// not a row index, load b re-arriving the machines in another order - across
// the whole refresh, and Enter still copies it.
func TestBoardStreamingSelectionKeepsSelectedRef(t *testing.T) {
	envA, envB, release, logA, logB := boardStreamFixture(t, "middle/w1:p1", "middle")
	// Load 1 (open): arrival fast, middle, slow.
	live := NewBoardState()
	gateA := boardRemoteGate(t, release, "a", []string{"fast", "middle", "slow"}, live, logA)
	loadBoardEntries(contextpkg.Background(), live, envA, platform.Current(), gateA)
	list := live.visible()
	middle := -1
	for i, entry := range list {
		if pickerString(entry["ref"]) == "middle/w1:p1" {
			middle = i
		}
	}
	if middle < 0 {
		t.Fatalf("the first load has no middle/w1:p1 row: %#v", list)
	}
	live.Selected = middle
	before := pickerString(list[live.Selected]["ref"])
	// Load 2 (refresh): arrival slow, fast, middle - middle re-arrives last
	// (the old row must stay on screen until then) and ends up on another
	// row than in load 1.
	loaded := NewBoardState()
	pubs := 0
	gateB := boardRemoteGate(t, release, "b", []string{"slow", "fast", "middle"}, loaded, logB)
	loadBoardEntries(contextpkg.Background(), loaded, envB, platform.Current(), func() {
		pubs++
		gateB()
		applyBoardUpdate(live, cloneBoardState(loaded))
		list := live.visible()
		if len(list) == 0 {
			t.Fatalf("publication %d left the board empty", pubs)
		}
		if ref := pickerString(list[live.Selected]["ref"]); ref != "middle/w1:p1" {
			t.Fatalf("publication %d: the selection left middle/w1:p1 (ref=%q entries=%#v)", pubs, ref, live.Entries)
		}
	})
	// The loader stamps the clock and publishes the final state (like the
	// production loop does after loadBoardEntries returns).
	loaded.UpdatedAt = boardNow().Format("15:04:05")
	applyBoardUpdate(live, cloneBoardState(loaded))
	wantIdx := -1
	for i, entry := range live.visible() {
		if pickerString(entry["ref"]) == "middle/w1:p1" {
			wantIdx = i
		}
	}
	if wantIdx < 0 {
		t.Fatalf("the second load lost middle/w1:p1: %#v", live.visible())
	}
	if wantIdx == middle {
		t.Fatalf("the fixture must move the middle row (load 1=%d, load 2=%d); the test would be vacuous", middle, wantIdx)
	}
	if live.Selected != wantIdx {
		t.Fatalf("selection=%d want %d (the ref moved across rows; the selection must follow the ref)", live.Selected, wantIdx)
	}
	live.ApplyKey("c")
	after := pickerString(live.LastEntry["ref"])
	if before != "middle/w1:p1" || after != "middle/w1:p1" {
		t.Fatalf("c copied %q want middle/w1:p1 (before=%q)", after, before)
	}
}

// TestBoardSelectionFallsToTopOnlyWhenTheLoadIsComplete: the selected ref
// disappears mid-load (the middle machine now returns a different agent). The
// selection must not jump to the top while the load is incomplete; only the
// complete load may do it.
func TestBoardSelectionFallsToTopOnlyWhenTheLoadIsComplete(t *testing.T) {
	envA, envB, release, logA, logB := boardStreamFixture(t, "middle/w2:p2", "middle-2")
	live := NewBoardState()
	gateA := boardRemoteGate(t, release, "a", []string{"fast", "middle", "slow"}, live, logA)
	loadBoardEntries(contextpkg.Background(), live, envA, platform.Current(), gateA)
	list := live.visible()
	middle := -1
	for i, entry := range list {
		if pickerString(entry["ref"]) == "middle/w1:p1" {
			middle = i
		}
	}
	if middle <= 0 {
		t.Fatalf("the first load must have middle/w1:p1 on a non-top row: %#v", list)
	}
	live.Selected = middle
	loaded := NewBoardState()
	pubs := 0
	goneAt := -1
	gateB := boardRemoteGate(t, release, "b", []string{"slow", "fast", "middle"}, loaded, logB)
	loadBoardEntries(contextpkg.Background(), loaded, envB, platform.Current(), func() {
		pubs++
		gateB()
		applyBoardUpdate(live, cloneBoardState(loaded))
		list := live.visible()
		if len(list) == 0 {
			t.Fatalf("publication %d left the board empty", pubs)
		}
		present := false
		for _, entry := range list {
			if pickerString(entry["ref"]) == "middle/w1:p1" {
				present = true
				break
			}
		}
		if present {
			if ref := pickerString(list[live.Selected]["ref"]); ref != "middle/w1:p1" {
				t.Fatalf("publication %d: the selection left middle/w1:p1 (ref=%q)", pubs, ref)
			}
			return
		}
		if goneAt < 0 {
			goneAt = pubs
		}
		if live.Selected == 0 {
			t.Fatalf("publication %d: the selection fell to the top before the load completed (gone at %d)", pubs, goneAt)
		}
	})
	// The loader stamps the clock and publishes the final state: only now may
	// the selection fall to the top (the ref is gone).
	loaded.UpdatedAt = boardNow().Format("15:04:05")
	applyBoardUpdate(live, cloneBoardState(loaded))
	if goneAt <= 0 {
		t.Fatalf("the fixture never showed the ref disappearing mid-load (publications=%d); the test would be vacuous", pubs)
	}
	if live.Selected != 0 {
		t.Fatalf("selection=%d after the complete load want 0 (the ref is gone)", live.Selected)
	}
}

// TestBoardCutMeasuresDisplayColumns: titles with CJK characters (two
// columns each), an emoji (the U+1F300-U+1FAFF block, two columns) and
// combining marks (zero columns) must never make the agent row pass the
// requested width - measured in display columns - and the cut must never
// split a rune.
func TestBoardCutMeasuresDisplayColumns(t *testing.T) {
	titles := []string{
		strings.Repeat("界", 30),                          // CJK: two columns per character
		"task " + "\U0001F600" + strings.Repeat("x", 40), // emoji: two columns
		strings.Repeat("a\u0300", 25),                    // combining mark: zero columns
	}
	for _, title := range titles {
		for _, width := range []int{10, 20, 30} {
			s := NewBoardState()
			s.Entries = []PickerEntry{boardEntry("local/p", "local", "ws", "alpha", "agent", "codex", "idle", title)}
			lines := strings.Split(strings.TrimSuffix(RenderBoard(s, width), "\n"), "\n")
			row := lines[3] // totals, query, header, agent row
			if w := displayWidth(row); w > width {
				t.Fatalf("width %d, title %q: row %q has display width %d", width, title, row, w)
			}
			if !utf8.ValidString(row) {
				t.Fatalf("width %d, title %q: the cut split a rune: %q", width, title, row)
			}
		}
	}
}

// During a refresh the cursor follows the pinned ref, so after a filter is
// typed and erased the arrows must start from the row the cursor is drawn
// on, not from the index the filter clamped (r87d).
func TestBoardArrowsStartFromThePinnedCursorAfterAFilter(t *testing.T) {
	cursorRef := func(s *BoardState) string {
		for _, line := range strings.Split(RenderBoard(s, 80), "\n") {
			if !strings.HasPrefix(line, ">") {
				continue
			}
			fields := strings.Fields(line[1:])
			for _, entry := range s.visible() {
				// The row is "<marker> <name> <kind> …"; a blank marker leaves
				// the name first.
				if len(fields) > 0 && (pickerString(entry["name"]) == fields[0] || len(fields) > 1 && pickerString(entry["name"]) == fields[1]) {
					return pickerString(entry["ref"])
				}
			}
		}
		return ""
	}
	filtered := func() *BoardState {
		s := NewBoardState()
		s.Entries = []PickerEntry{
			boardEntry("local/a", "local", "w", "w", "a", "codex", "idle", "a"),
			boardEntry("local/b", "local", "w", "w", "b", "codex", "idle", "b"),
			boardEntry("local/c", "local", "w", "w", "c", "codex", "idle", "z"),
		}
		s.Selected = 2
		loading := NewBoardState()
		loading.Loading = 1
		loading.LoadingLocal = true
		applyBoardUpdate(s, loading)
		s.FeedChunk("z")
		s.FeedChunk("\x7f")
		return s
	}
	for _, tc := range []struct {
		name, seq, want string
	}{{"up", "\x1b[A", "local/b"}, {"down", "\x1b[B", "local/c"}} {
		t.Run(tc.name, func(t *testing.T) {
			s := filtered()
			if got := cursorRef(s); got != "local/c" {
				t.Fatalf("cursor before the arrow on %q, want local/c", got)
			}
			s.FeedChunk(tc.seq)
			if got := cursorRef(s); got != tc.want {
				t.Fatalf("cursor after %s on %q, want %q", tc.name, got, tc.want)
			}
			s.FeedChunk("c")
			if got := pickerString(s.LastEntry["ref"]); got != tc.want {
				t.Fatalf("c copied %q, want %q", got, tc.want)
			}
		})
	}
}

// --- Navigation: state -----------------------------------------------------

// TestBoardCtrlEnterNavigatesThePinnedRef: while a generation is running,
// Ctrl+Enter must target the pinned ref - never the row the cursor index
// happens to point at.
func TestBoardCtrlEnterNavigatesThePinnedRef(t *testing.T) {
	live := NewBoardState()
	live.Entries = []PickerEntry{
		boardEntry("local/a1", "local", "ws-a", "alpha", "orchestrator-1", "claude", "working", "one"),
		boardEntry("local/a2", "local", "ws-a", "alpha", "implementer-1", "codex", "idle", "two"),
	}
	live.Selected = 0 // pin local/a1

	update := NewBoardState()
	update.Entries = []PickerEntry{
		boardEntry("local/a3", "local", "ws-a", "alpha", "scouter-1", "grok", "idle", "three"),
		boardEntry("local/a2", "local", "ws-a", "alpha", "implementer-1", "codex", "idle", "two"),
		boardEntry("local/a1", "local", "ws-a", "alpha", "orchestrator-1", "claude", "working", "one"),
	}
	update.FinishedMachines = []string{"local"}
	applyBoardUpdate(live, update)

	if got := live.ApplyKey("ctrl-enter"); got != "navigate" || !live.NavPending {
		t.Fatalf("ctrl-enter action=%q pending=%v", got, live.NavPending)
	}
	if ref := pickerString(live.NavTarget["ref"]); ref != "local/a1" {
		t.Fatalf("nav target=%q want the pinned ref local/a1 (last row, not the first)", ref)
	}
	// Duplicate actions are rejected while the navigation is pending.
	if got := live.ApplyKey("ctrl-enter"); got != "" {
		t.Fatalf("second ctrl-enter while pending=%q", got)
	}
	if got := live.ApplyKey("enter"); got != "" || live.Copied != nil {
		t.Fatalf("enter while pending: action=%q copied=%v", got, live.Copied)
	}
	// A refresh that lands while the navigation is pending keeps the
	// frozen target.
	update2 := NewBoardState()
	update2.Entries = []PickerEntry{
		boardEntry("local/a4", "local", "ws-b", "beta", "reviewer-1", "codex", "idle", "four"),
	}
	update2.FinishedMachines = []string{"local"}
	applyBoardUpdate(live, update2)
	if !live.NavPending || pickerString(live.NavTarget["ref"]) != "local/a1" {
		t.Fatalf("refresh changed the frozen nav target: pending=%v target=%v", live.NavPending, live.NavTarget)
	}
}

// TestBoardCtrlEnterPinnedRefGone: a pinned ref that left the visible list
// while the load is running has no target row - no navigation, no exit.
func TestBoardCtrlEnterPinnedRefGone(t *testing.T) {
	live := NewBoardState()
	live.Entries = []PickerEntry{
		boardEntry("local/a1", "local", "ws-a", "alpha", "orchestrator-1", "claude", "working", "one"),
		boardEntry("local/a2", "local", "ws-a", "alpha", "implementer-1", "codex", "idle", "two"),
	}
	live.Selected = 1 // pin local/a2

	update := NewBoardState()
	update.Entries = []PickerEntry{
		boardEntry("local/a1", "local", "ws-a", "alpha", "orchestrator-1", "claude", "working", "one"),
	}
	update.FinishedMachines = []string{"local"}
	applyBoardUpdate(live, update)

	if got := live.ApplyKey("ctrl-enter"); got != "" || live.NavPending || live.Exit != "" {
		t.Fatalf("ctrl-enter with pinned ref gone: action=%q pending=%v exit=%q", got, live.NavPending, live.Exit)
	}
}

func TestBoardFeedChunkNavigationKeys(t *testing.T) {
	two := func() *BoardState {
		s := NewBoardState()
		s.Entries = []PickerEntry{
			boardEntry("local/a1", "local", "ws-a", "alpha", "orchestrator-1", "claude", "working", "one"),
			boardEntry("local/a2", "local", "ws-a", "alpha", "implementer-1", "codex", "idle", "two"),
		}
		return s
	}
	if got := two().FeedChunk("\x1b[13;5u"); got != "navigate" {
		t.Fatalf("kitty ctrl+enter=%q", got)
	}
	s := two()
	s.FeedChunk("\x1b[27;5;1")
	if got := s.FeedChunk("3~"); got != "navigate" {
		t.Fatalf("modifyOtherKeys ctrl+enter=%q", got)
	}
	if got := two().FeedChunk("\r"); got != "navigate" {
		t.Fatalf("legacy enter=%q", got)
	}
	if got := two().FeedChunk("\x12"); got != "update" {
		t.Fatalf("legacy ctrl+r=%q", got)
	}
	if got := two().FeedChunk("\x1b[114;5u"); got != "update" {
		t.Fatalf("enhanced ctrl+r=%q", got)
	}
	s = two()
	s.FeedChunk("a")
	if got := s.FeedChunk("\x1b[99;5u"); got != "esc" || s.Query != "a" {
		t.Fatalf("ctrl+c: action=%q query=%q", got, s.Query)
	}
	q := two()
	q.FeedChunk("a")
	if got := q.FeedChunk("\x1b[?0u"); got != "" || q.Query != "a" {
		t.Fatalf("kitty query reply: action=%q query=%q", got, q.Query)
	}
}
