package plugin

import (
	"bufio"
	contextpkg "context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/djalmajr/soho/internal/peer"
	"github.com/djalmajr/soho/internal/platform"
)

// boardRefreshInterval is the team board's auto-update cadence; tests replace it.
var boardRefreshInterval = 10 * time.Second

// boardNow is the board clock; tests replace it.
var boardNow = time.Now

// boardFilterFields filters like the picker's fields, plus the task title.
var boardFilterFields = []string{"ref", "name", "kind", "status", "title", "workspace_label", "tab_label", "cwd", "machine"}

// BoardState holds the team board's view: every agent of every machine,
// grouped by machine and workspace, refreshed in place.
type BoardState struct {
	ViewState    SessionViewState
	Entries      []PickerEntry
	Query        string
	Selected     int
	Loading      int
	LoadingLocal bool
	Failures     []PickerFailure
	LastEntry    PickerEntry
	Copied       *string
	Exit         string
	TerminalKeys // shared raw-input decoder (EscPending/CSIPending)
	// NavPending is true while a navigation is in flight: Enter and
	// Ctrl+Enter are rejected and the frozen target stays in NavTarget.
	NavPending bool
	NavTarget  PickerEntry
	NavFailure *PickerFailure // action feedback survives refresh publications
	// TeamView is the open read-only team view of the selected row
	// (the same type and rules as the picker's):
	// nil while the list has the frame. While it is open the list's keys
	// never move the selection, so Esc and i return to the same row, by
	// the full ref, like the picker's selection rule.
	TeamView *pickerTeamView
	// teamLoadSeq counts team-view opens: every open (a re-open
	// included) bumps it, so a late result of a superseded open is
	// discarded.
	teamLoadSeq int
	// CopyNotice is the last c-copy confirmation ("Copied <ref>") shown in
	// the modal's bottom status area: it is set only after the copy
	// operation returned, it survives the progressive refresh
	// publications (the updates replace the list, never this field), and
	// a started navigation clears it so the navigation status takes over.
	CopyNotice string
	UpdatedAt  string
	// FinishedMachines lists the new load's machines whose find terminated
	// (success or failure, with or without rows); the loader fills it.
	FinishedMachines []string
	// flavorPicker keeps the picker command's legacy (non-TTY) display
	// contract when the picker routes into the board runtime: every pane
	// in the discovery's arrival order, the picker filter fields and the
	// "pane" nouns, instead of the board's agents-only legacy list.
	flavorPicker bool
	// FrameWidth/FrameHeight is the last drawn frame of the owned popup;
	// the page keys move by one row window of that frame, measured with
	// the shared renderer viewport math.
	FrameWidth           int
	FrameHeight          int
	Mouse                *mouseNegotiation // the TTY's temporary SGR mouse negotiation
	selectedRef          string
	refreshing           bool
	refreshOld           []PickerEntry
	viewPreferencePath   string
	viewPreferenceFailed bool
	// teamViewPreferencePath is the team view's form preference file
	// (the team-view file beside the session-view one): the same
	// directory and the same way as the session-view preference.
	teamViewPreferencePath string
	// teamViewForm is the team view's open presentation, read from the
	// preference at the state's construction (missing, unreadable or
	// invalid values read as the tree) and persisted by the v switch.
	teamViewForm string
	// teamViewPreferenceFailed records the team-view form's save
	// failure: nothing displays it (the list's message is the list's
	// own preference's), the switch and the atomic write go on as usual.
	teamViewPreferenceFailed bool
	// lastFrameSeq, lastFrameW and lastFrameH record the exact frame bytes
	// this runtime last painted and the viewport they were painted
	// against. An identical frame for the same viewport is not written
	// again (no repeated full-screen frame for a no-op key or an unchanged
	// publication); the record is per runtime state, so independent
	// sessions never share it, and a failed or short write invalidates it,
	// so a later request of the previously painted frame is written
	// completely again (a partial failed frame may be on screen).
	lastFrameSeq     string
	lastFrameText    string
	lastFrameW       int
	lastFrameH       int
	lastFramePainted bool
}

func NewBoardState() *BoardState {
	return &BoardState{Entries: []PickerEntry{}, Failures: []PickerFailure{}, ViewState: SessionViewState{View: sessionViewTable, Sort: sessionSortPane}, teamViewForm: teamFormTree}
}

// boardAgents keeps only the panes that have an agent (kind not null), in
// board order: the machine (local first, then arrival order), within it the
// workspace by workspace_label, and within the workspace the names that
// start with "orchestrator" before the rest, by name.
func boardAgents(entries []PickerEntry) []PickerEntry {
	out := make([]PickerEntry, 0, len(entries))
	for _, entry := range entries {
		if pickerString(entry["kind"]) != "" {
			out = append(out, entry)
		}
	}
	rank := map[string]int{}
	next := 1
	for _, entry := range out {
		machine := pickerString(entry["machine"])
		if _, ok := rank[machine]; !ok {
			if machine == "local" {
				rank[machine] = 0
			} else {
				rank[machine] = next
				next++
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		mi, mj := pickerString(out[i]["machine"]), pickerString(out[j]["machine"])
		if rank[mi] != rank[mj] {
			return rank[mi] < rank[mj]
		}
		wi, wj := pickerString(out[i]["workspace_label"]), pickerString(out[j]["workspace_label"])
		if wi != wj {
			return wi < wj
		}
		ni, nj := pickerString(out[i]["name"]), pickerString(out[j]["name"])
		oi, oj := strings.HasPrefix(ni, "orchestrator"), strings.HasPrefix(nj, "orchestrator")
		if oi != oj {
			return oi
		}
		return ni < nj
	})
	return out
}

// visible is the list the popup shows in the state's mode. The enhanced
// (production) runtime shows the unified sessions - every pane, the
// unknown/empty/"-" kinds as terminals - filtered by scope, status and
// the text query and ordered by the view state. Disabled, it keeps the
// legacy list of the running command: the picker's every-pane arrival
// order (flavorPicker) or the board's agents-only board order.
func (s *BoardState) visible() []PickerEntry {
	if s.ViewState.Enabled {
		return sessionVisibleEntries(sessionFilterEntries(s.Entries, s.Query, s.ViewState), s.ViewState)
	}
	if s.flavorPicker {
		return filterPickerEntries(s.Entries, s.Query, pickerFilterFields)
	}
	return filterPickerEntries(boardAgents(s.Entries), s.Query, boardFilterFields)
}

func (s *BoardState) clamp() {
	s.Selected = visibleIndex(len(s.visible()), s.Selected)
}

// visibleIndex clamps an index into a visible list of length n without
// ever returning a negative index (an empty list clamps to 0).
func visibleIndex(n, idx int) int {
	if n <= 0 {
		return 0
	}
	if idx < 0 {
		return 0
	}
	if idx > n-1 {
		return n - 1
	}
	return idx
}

// pinnedIndex is the visible index of the pinned ref while a generation is
// running; -1 when there is no pin or the ref is not in the visible list.
// syncSelectedToPin makes the navigation start from the row the cursor is
// drawn on: during a refresh the cursor follows the pinned ref, while a
// filter change can leave Selected on another index.
func (s *BoardState) syncSelectedToPin() {
	if !s.refreshing || s.selectedRef == "" {
		return
	}
	if idx := s.pinnedIndex(); idx >= 0 {
		s.Selected = idx
	}
}

func (s *BoardState) pinnedIndex() int {
	if !s.refreshing || s.selectedRef == "" {
		return -1
	}
	for i, entry := range s.visible() {
		if pickerString(entry["ref"]) == s.selectedRef {
			return i
		}
	}
	return -1
}

// pinnedMissing: the pinned ref left the visible list while the load is
// still running. No row shows the cursor, Enter copies nothing, and the
// footer says so; ↑/↓ pick a new ref.
func (s *BoardState) pinnedMissing() bool {
	return s.refreshing && s.selectedRef != "" && s.pinnedIndex() < 0
}

// pageMove moves the selection by one viewport page of selectable entries,
// clamped: the page is the shared renderer viewport math for the last
// drawn frame (sessionSelectableWindow), so the table's fixed header and
// the tree's group headings are accounted for and a tiny frame keeps at
// least one entry. It is an explicit movement, so it re-pins the new row
// safely; a missing pin never navigates to an implicit neighbor - the up
// side re-pins the first visible row and the down side the last, like the
// arrows do.
func (s *BoardState) pageMove(dir int) {
	s.syncSelectedToPin()
	list := s.visible()
	n := len(list)
	if n == 0 {
		return
	}
	if s.pinnedMissing() {
		if dir < 0 {
			s.Selected = 0
		} else {
			s.Selected = n - 1
		}
		s.selectedRef = pickerString(list[s.Selected]["ref"])
		return
	}
	frameWidth, frameHeight := s.FrameWidth, s.FrameHeight
	if frameWidth <= 0 || frameHeight <= 0 {
		// No frame drawn yet: the bounded default viewport.
		frameWidth, frameHeight = 80, 24
	}
	page := sessionSelectableWindow(unifiedSessionOptions(s, ""), frameWidth, frameHeight)
	if page < 1 {
		page = 1
	}
	if dir < 0 {
		s.Selected -= page
	} else {
		s.Selected += page
	}
	if s.Selected < 0 {
		s.Selected = 0
	}
	if s.Selected > n-1 {
		s.Selected = n - 1
	}
	s.selectedRef = pickerString(list[s.Selected]["ref"])
}

// wheelMove moves the selection three entries per wheel notch, clamped: a
// vertical SGR report is an explicit movement, so it re-pins the new row
// safely; a missing pin re-pins the first (up) or the last (down)
// visible row instead of an implicit neighbor. It copies and focuses
// nothing.
func (s *BoardState) wheelMove(steps int) {
	s.syncSelectedToPin()
	list := s.visible()
	n := len(list)
	if n == 0 {
		return
	}
	if s.pinnedMissing() {
		if steps < 0 {
			s.Selected = 0
		} else {
			s.Selected = n - 1
		}
		s.selectedRef = pickerString(list[s.Selected]["ref"])
		return
	}
	s.Selected += steps
	s.clamp()
	list = s.visible()
	s.selectedRef = pickerString(list[s.Selected]["ref"])
}

// BoardTitle strips the leading "<role>: " prefix the CLI puts on pane
// titles ("orchestrator: <task>", "<role>: <brief task>") and removes
// control characters, like the picker does.
func BoardTitle(value any) string {
	text := StripPickerControls(pickerString(value))
	idx := strings.Index(text, ": ")
	if idx > 0 && !strings.ContainsAny(text[:idx], " \t") {
		return text[idx+2:]
	}
	return text
}

func boardMarker(entry PickerEntry) string {
	switch StripPickerControls(pickerString(entry["status"])) {
	case "blocked":
		return "!"
	case "working":
		return "*"
	default:
		return " "
	}
}

// BoardEntryLine renders one agent row: name, kind, status and the title,
// cut at the width in terminal columns (wide characters count as two,
// combining marks as zero); the last column takes the remaining budget,
// ellipsized, and the total never passes the width.
func BoardEntryLine(entry PickerEntry, width int) string {
	display := func(v any) string {
		s := StripPickerControls(pickerString(v))
		if s == "" {
			return "-"
		}
		return s
	}
	cols := []string{display(entry["name"]), display(entry["kind"]), display(entry["status"]), display(BoardTitle(entry["title"]))}
	lead := strings.Join(cols[:len(cols)-1], " ")
	budget := width - displayWidth(lead) - 1
	if budget < 0 {
		budget = 0
	}
	last := cols[len(cols)-1]
	if displayWidth(last) > budget {
		last = displaySlice(last, budget)
	}
	line := lead + " " + last
	if displayWidth(line) > width {
		line = displaySlice(line, width)
	}
	return line
}

// boardTotals renders the board's top line: the session count (agents,
// or sessions when the unified list carries terminal panes), the
// per-status counts (working, blocked, idle, done, when present) and the
// local time (HH:MM:SS) of the last completed update, when there is one.
func boardTotals(agents []PickerEntry, updatedAt string) string {
	terminals := 0
	counts := map[string]int{}
	for _, entry := range agents {
		counts[StripPickerControls(pickerString(entry["status"]))]++
		if !sessionAgentOf(entry) {
			terminals++
		}
	}
	noun := "agents"
	if terminals > 0 {
		noun = "sessions"
	}
	line := fmt.Sprintf("%d %s", len(agents), noun)
	for _, status := range []string{"working", "blocked", "idle", "done"} {
		if counts[status] > 0 {
			line += fmt.Sprintf(" · %d %s", counts[status], status)
		}
	}
	if updatedAt != "" {
		line += " · " + updatedAt
	}
	return line
}

func RenderBoard(state *BoardState, width int) string {
	agents := boardAgents(state.Entries)
	display := func(v any) string {
		s := StripPickerControls(pickerString(v))
		if s == "" {
			return "-"
		}
		return s
	}
	lines := []string{boardTotals(agents, state.UpdatedAt), "> " + state.Query}
	currentMachine, currentWorkspace := "", ""
	// While the generation is running, the cursor follows the pinned ref
	// (resolved by ref); when the ref is not in the visible list there is
	// no cursor at all (pinnedIndex -1 matches no row).
	cursorIdx := state.Selected
	if state.refreshing && state.selectedRef != "" {
		cursorIdx = state.pinnedIndex()
	}
	for i, entry := range state.visible() {
		machine := pickerString(entry["machine"])
		workspace := pickerString(entry["workspace_label"])
		if machine != currentMachine || workspace != currentWorkspace {
			lines = append(lines, fmt.Sprintf("%s · %s (%s)", display(entry["machine"]), display(entry["workspace_label"]), display(entry["workspace_id"])))
			currentMachine = machine
			currentWorkspace = workspace
		}
		cursor := " "
		if i == cursorIdx {
			cursor = ">"
		}
		rowWidth := width - 3
		if rowWidth < 2 {
			rowWidth = 2
		}
		lines = append(lines, cursor+boardMarker(entry)+" "+BoardEntryLine(entry, rowWidth))
	}
	list := state.visible()
	if len(list) == 0 && len(agents) > 0 {
		lines = append(lines, "nenhum resultado para \""+state.Query+"\"")
	}
	if state.Loading > 0 {
		if state.LoadingLocal {
			lines = append(lines, "carregando local…")
		} else {
			lines = append(lines, "carregando windows…")
		}
	}
	for _, failure := range state.Failures {
		lines = append(lines, "máquina "+StripPickerControls(failure.Label)+": falhou ("+StripPickerControls(failure.Cause)+")")
	}
	if len(agents) == 0 && state.Loading == 0 && len(state.Failures) == 0 {
		lines = append(lines, "nenhum agente")
	}
	// No agent count at the bottom: the top totals line already gives it.
	if state.pinnedMissing() {
		lines = append(lines, "a sessão selecionada saiu desta carga; escolha outra com ↑/↓")
	}
	return strings.Join(lines, "\n") + "\n"
}

func (s *BoardState) ApplyKey(key string) string {
	// The read-only team view overlays the list: while
	// it is open, Esc and i go back to the list with the same selection,
	// the arrow and page keys scroll its content, v switches its
	// presentation (tree and table, the scroll resets to the top, the
	// choice is persisted and remembered for the run), Ctrl-C closes the
	// modal as before, and the list keys and the search text never reach
	// the list underneath (the view is read-only and the selection is
	// frozen for it).
	if s.TeamView != nil {
		switch key {
		case "esc", "i":
			s.TeamView = nil
			return ""
		case "ctrl-c":
			if s.Exit == "" {
				s.Exit = "esc"
			}
			return s.Exit
		case "up", "down", "page-up", "page-down":
			teamViewScrollMove(s.TeamView, key)
			return ""
		case "v":
			teamViewToggleForm(s.TeamView)
			s.teamViewForm = s.TeamView.form
			s.rememberTeamViewForm()
			return ""
		}
		return ""
	}
	switch key {
	case "enter", "ctrl-enter":
		// Enter navigates and focuses the selected pane (Ctrl+Enter stays
		// the undocumented compatibility alias); neither copies. The loop
		// runs the existing NavigateSelection safety checks and closes
		// only on a verified successful navigation.
		return s.startNavigation()
	case "c":
		// c copies the selected pane's full reference in the list focus
		// and keeps the modal open (the loop confirms it in the status
		// area); in the enhanced search focus the same key - bare or the
		// kitty encoded printable c - is ordinary search text. The copy is
		// gated while a navigation is pending, when the visible list is
		// empty and when the pinned selected ref left the load; an entry
		// without a reference fabricates nothing.
		if s.NavPending || s.Exit != "" {
			return ""
		}
		if s.pinnedMissing() {
			return ""
		}
		if s.ViewState.Enabled && s.ViewState.SearchFocused {
			s.applyBoardChar("c")
			return ""
		}
		list := s.visible()
		if len(list) == 0 {
			return ""
		}
		idx := visibleIndex(len(list), s.Selected)
		if s.refreshing && s.selectedRef != "" {
			// Resolve the pinned ref by ref, never by the numeric index.
			for i, entry := range list {
				if pickerString(entry["ref"]) == s.selectedRef {
					idx = i
					break
				}
			}
		}
		entry := list[idx]
		payload := PickerCopyPayload(entry)
		if payload == "" {
			return ""
		}
		s.LastEntry = entry
		s.Copied = &payload
		return "copy"
	case "i":
		// i opens the read-only team view of the selected row in the
		// enhanced list focus; in the enhanced search
		// focus the same key is ordinary search text, as c already is, and
		// the legacy input keeps it as query text (frozen contract).
		if s.NavPending || s.Exit != "" {
			return ""
		}
		if !s.ViewState.Enabled || s.ViewState.SearchFocused {
			s.applyBoardChar("i")
			return ""
		}
		if s.pinnedMissing() {
			// The pinned ref left the visible list while the load is
			// still running: there is no target row to open the view on.
			return ""
		}
		if s.openBoardTeamView() {
			// A local row with a valid target: the loop starts the load.
			return "team"
		}
		return ""
	case "esc", "ctrl-c":
		if s.Exit == "" {
			s.Exit = "esc"
		}
		return s.Exit
	case "tab":
		// The enhanced popup toggles the search/list focus; the legacy input
		// ignores the key, as before.
		if s.ViewState.Enabled {
			s.ViewState.SearchFocused = !s.ViewState.SearchFocused
		}
		return ""
	case "backspace":
		if s.ViewState.Enabled {
			// Backspace edits in search focus; Tab returns to the list.
			s.ViewState.SearchFocused = true
		}
		s.Query = jsSlice(s.Query, jsLength(s.Query)-1)
		s.clamp()
		return ""
	case "up":
		s.syncSelectedToPin()
		if s.pinnedMissing() {
			// The cursor is gone: ↑ picks the first visible row and makes it
			// the new pinned ref.
			if len(s.visible()) > 0 {
				s.Selected = 0
				s.selectedRef = pickerString(s.visible()[0]["ref"])
			}
			return ""
		}
		if len(s.visible()) > 0 && s.Selected > 0 {
			s.Selected--
			s.selectedRef = pickerString(s.visible()[s.Selected]["ref"])
		}
		return ""
	case "down":
		s.syncSelectedToPin()
		if s.pinnedMissing() {
			// The cursor is gone: ↓ picks the last visible row and makes it
			// the new pinned ref.
			if len(s.visible()) > 0 {
				s.Selected = len(s.visible()) - 1
				s.selectedRef = pickerString(s.visible()[s.Selected]["ref"])
			}
			return ""
		}
		if len(s.visible()) > 1 && s.Selected < len(s.visible())-1 {
			s.Selected++
			s.selectedRef = pickerString(s.visible()[s.Selected]["ref"])
		}
		return ""
	case "page-up":
		s.pageMove(-1)
		return ""
	case "page-down":
		s.pageMove(1)
		return ""
	case "wheel-up":
		s.wheelMove(-3)
		return ""
	case "wheel-down":
		s.wheelMove(3)
		return ""
	case "update":
		return "update"
	default:
		if jsLength(key) == 1 {
			r, _ := utf8.DecodeRuneInString(key)
			if r >= 0x20 && r != 0x7f {
				return s.applyBoardChar(key)
			}
		}
		return ""
	}
}

// startNavigation begins the focus navigation of the selected row: it
// freezes the target (NavPending, NavTarget) so Enter and Ctrl+Enter are
// rejected while the navigation is in flight and a second trigger is a
// no-op; with a missing pinned ref there is no target row to navigate.
// The loop runs the existing NavigateSelection safety checks and closes
// only on a verified successful navigation.
func (s *BoardState) startNavigation() string {
	if s.NavPending || s.Exit != "" {
		return ""
	}
	if s.pinnedMissing() {
		// The pinned ref left the visible list while the load is still
		// running: there is no target row to navigate.
		return ""
	}
	list := s.visible()
	if len(list) == 0 {
		return ""
	}
	idx := visibleIndex(len(list), s.Selected)
	if s.refreshing && s.selectedRef != "" {
		// Resolve the pinned ref by ref, never by the numeric index.
		for i, entry := range list {
			if pickerString(entry["ref"]) == s.selectedRef {
				idx = i
				break
			}
		}
	}
	entry := list[idx]
	s.LastEntry = entry
	s.NavPending = true
	s.NavTarget = entry
	return "navigate"
}

// openBoardTeamView opens the read-only team view on the selected row
// (the board runtime's i key): the row's workspace_id,
// pane_id and cwd build the team target and the cwd is validated like
// resolveTeamTarget does; a row of another machine opens the view with
// the remote-machine message and no CLI call at all. During a refresh the
// selection resolves by the pinned ref, like Enter and c do. Every open
// starts a new load sequence, so a late result of the previous open (or
// of another row) is discarded. It reports whether a load is needed - a
// local row with a valid target only (a remote or invalid row shows its
// message and the loop runs no CLI call).
func (s *BoardState) openBoardTeamView() bool {
	list := s.visible()
	if len(list) == 0 {
		return false
	}
	idx := visibleIndex(len(list), s.Selected)
	if s.refreshing && s.selectedRef != "" {
		// Resolve the pinned ref by ref, never by the numeric index.
		for i, entry := range list {
			if pickerString(entry["ref"]) == s.selectedRef {
				idx = i
				break
			}
		}
	}
	entry := list[idx]
	s.teamLoadSeq++
	s.TeamView = openTeamView(s.teamLoadSeq, entry)
	s.TeamView.form = teamViewFormOf(s.teamViewForm)
	return !s.TeamView.remote && s.TeamView.targetErr == ""
}

// applyBoardChar applies one printable character. r refreshes the board
// in the enhanced list focus, or with an empty legacy query. List focus also interprets the
// p/w/s/v sort/view keys and the f/t scope/status filters; everything
// else enters the search with the character. The legacy input and the
// search focus append the character to the query.
func (s *BoardState) applyBoardChar(key string) string {
	if key == "r" && ((s.ViewState.Enabled && !s.ViewState.SearchFocused) || (!s.ViewState.Enabled && s.Query == "")) {
		return "update"
	}
	if s.ViewState.Enabled {
		if !s.ViewState.SearchFocused {
			if s.applySortOrViewKey(key) {
				return ""
			}
			if s.applyScopeOrStatusKey(key) {
				return ""
			}
		}
		s.ViewState.SearchFocused = true
	}
	s.Query += key
	s.clamp()
	return ""
}

// applyScopeOrStatusKey interprets one enhanced list-focus f/t filter key:
// it cycles the scope (all/agents/terminals) or the status (all/working/
// blocked/idle/done/unknown) filter and keeps the selection on the full
// ref of the row selected before the change; when that ref leaves the
// list the clamped row is re-pinned instead. While a refresh is running
// and the pinned ref is already missing, the filter change must not
// silently adopt a row: only an explicit arrow, page or wheel re-pins. It
// reports false when the key is not a filter key.
func (s *BoardState) applyScopeOrStatusKey(key string) bool {
	s.syncSelectedToPin()
	pre := s.visible()
	ref := ""
	if len(pre) > 0 {
		ref = pickerString(pre[visibleIndex(len(pre), s.Selected)]["ref"])
	}
	switch key {
	case "f":
		s.ViewState.Scope = sessionScopeCycle(s.ViewState.Scope)
	case "t":
		s.ViewState.Status = sessionStatusCycle(s.ViewState.Status)
	default:
		return false
	}
	if s.pinnedMissing() {
		// The pinned ref left the visible list while the load is still
		// running: keep the filter change, adopt no row.
		return true
	}
	list := s.visible()
	s.Selected = visibleIndex(len(list), sessionKeepSelection(list, s.Selected, ref))
	if len(list) > 0 {
		s.selectedRef = pickerString(list[s.Selected]["ref"])
	}
	return true
}

// applySortOrViewKey interprets one enhanced list-focus p/w/s/v key: it
// changes the sort or the presentation and keeps the selection on the full
// ref of the row selected before the change. While a refresh is running
// and the pinned ref is already missing, the change must not silently
// adopt a row: only an explicit arrow, page or wheel re-pins. It reports
// false when the key is not a sort/view key.
func (s *BoardState) applySortOrViewKey(key string) bool {
	s.syncSelectedToPin()
	pre := s.visible()
	ref := ""
	if len(pre) > 0 {
		ref = pickerString(pre[visibleIndex(len(pre), s.Selected)]["ref"])
	}
	switch key {
	case "p":
		sessionApplySort(&s.ViewState, sessionSortPane)
	case "w":
		sessionApplySort(&s.ViewState, sessionSortWorkspace)
	case "s":
		sessionApplySort(&s.ViewState, sessionSortStatus)
	case "v":
		sessionToggleView(&s.ViewState)
		s.rememberSessionView()
	default:
		return false
	}
	if s.pinnedMissing() {
		// The pinned ref left the visible list while the load is still
		// running: keep the sort/view change, adopt no row.
		return true
	}
	s.Selected = visibleIndex(len(s.visible()), sessionKeepSelection(s.visible(), s.Selected, ref))
	return true
}

// FeedChunk parses raw input with the shared decoder and applies keys in
// order; it stops at the first action, like the terminal loop. Both the
// legacy encodings (CR, 0x03, 0x08/0x7f, 0x12, bare ESC, CSI A/B) and the
// enhanced ones enabled by the keyboard-protocol push (CSI 13u, CSI 27u,
// CSI 99;5u, CSI 114;5u, CSI 13;5u, CSI 27;5;13~) are recognized; query
// replies, key-release events and unknown CSI are ignored, and a pending
// sequence is bounded.
func (s *BoardState) FeedChunk(chunk string) string {
	for _, r := range chunk {
		key := s.TerminalKeys.Feed(r)
		if key == "" {
			continue
		}
		if action := s.ApplyKey(key); action != "" {
			return action
		}
	}
	return s.Exit
}

// boardTeamLoad is the session loop's one in-flight team-view load: the
// cancel of its reads and the sequence of the open it belongs to.
type boardTeamLoad struct {
	cancel  contextpkg.CancelFunc
	seq     int
	workers sync.WaitGroup
}

// run tracks every load, including a superseded load still unwinding.
func (l *boardTeamLoad) run(work func()) {
	l.workers.Add(1)
	go func() {
		defer l.workers.Done()
		work()
	}()
}

// shutdown cancels the current load and joins all reads before the loop
// returns, so a child cannot keep writing after its caller tears down.
func (l *boardTeamLoad) shutdown() {
	l.stop()
	l.workers.Wait()
}

// start supersedes the in-flight load (its reads are cancelled) and
// returns the context of the new load for the open with sequence seq.
func (l *boardTeamLoad) start(seq int) contextpkg.Context {
	l.stop()
	ctx, cancel := contextpkg.WithCancel(contextpkg.Background())
	l.cancel, l.seq = cancel, seq
	return ctx
}

// finish retires the cancel only for the current load's result: a late
// result of a superseded open must not drop the cancel of the load that
// replaced it, or closing the modal would leave that load running.
func (l *boardTeamLoad) finish(res pickerTeamViewResult) {
	if res.seq == l.seq {
		l.stop()
	}
}

// stop cancels the in-flight load, if any (the loop's exit).
func (l *boardTeamLoad) stop() {
	if l.cancel != nil {
		l.cancel()
		l.cancel = nil
	}
}

// shutdown ends the modal the way Esc on the list does, closing an open
// team view first: EOF and a shutdown signal end the popup, never only
// the team view over it.
func (s *BoardState) shutdown() {
	s.TeamView = nil
	s.ApplyKey("esc")
}

func (s *BoardState) FlushEsc() string {
	if s.TerminalKeys.FlushEsc() == "" {
		return ""
	}
	s.ApplyKey("esc")
	return s.Exit
}

func cloneBoardState(source *BoardState) *BoardState {
	clone := *source
	clone.Entries = append([]PickerEntry(nil), source.Entries...)
	clone.Failures = append([]PickerFailure(nil), source.Failures...)
	clone.FinishedMachines = append([]string(nil), source.FinishedMachines...)
	clone.refreshOld = append([]PickerEntry(nil), source.refreshOld...)
	return &clone
}

// loadBoardEntries runs one board refresh through the shared discovery
// engine: the local snapshot publishes before the machine enumeration and
// the remote snapshots, at most four remote queries run concurrently, and
// one global deadline covers the whole refresh. It publishes after the
// initial state, after every per-machine batch (filling FinishedMachines
// with the machines whose batch terminated, success or failure), and once
// the discovery has settled, like the picker's load.
func loadBoardEntries(ctx contextpkg.Context, state *BoardState, env platform.Env, platformName string, publish func()) {
	state.LoadingLocal = true
	state.Loading = 1
	publish()
	final := peer.DiscoverSessions(peer.DiscoverOptions{
		Env:         env,
		All:         true,
		TimeoutMS:   pickerDiscoveryTimeoutMS,
		Concurrency: peer.DiscoverMaxConcurrency,
		Context:     ctx,
		Run:         pickerDiscoveryRun(env, platformName),
	}, func(batch peer.SessionResult) {
		if entries := pickerEntriesFromBatch(batch); len(entries) > 0 {
			state.Entries = appendUniquePicker(state.Entries, entries)
		}
		for _, failure := range batch.Failures {
			state.Failures = append(state.Failures, PickerFailure{Label: failure.Machine, Cause: failure.Cause})
		}
		for _, machine := range batchMachines(batch) {
			state.FinishedMachines = append(state.FinishedMachines, machine)
		}
		if state.LoadingLocal {
			// The first batch is always the local snapshot; after it the
			// machine list and the remote queries are in flight.
			state.LoadingLocal = false
			state.Loading = 1
		}
		publish()
	})
	if final.ListCause != "" {
		state.Failures = append(state.Failures, PickerFailure{Label: "máquinas", Cause: final.ListCause})
	}
	state.LoadingLocal = false
	state.Loading = 0
	publish()
}

// applyBoardUpdate merges one publication of the load generation into the
// live state. While the load is incomplete the previous list stays on
// screen: the new load's rows when that machine's find has terminated
// (success or failure, with or without rows), the previous rows - all of
// them - otherwise. The complete load (UpdatedAt set) publishes only the
// new result: machines that left the machine list and vanished agents are
// gone. The selected ref is pinned across the whole generation and falls
// to the first row only once the load is complete and the ref is gone.
func applyBoardUpdate(state, update *BoardState) {
	query, exit, copied, lastEntry, keys, navPending, navTarget := state.Query, state.Exit, state.Copied, state.LastEntry, state.TerminalKeys, state.NavPending, state.NavTarget
	if !state.refreshing {
		// First publication of the generation: keep the previous list and
		// the selected ref before anything is replaced.
		state.refreshing = true
		state.refreshOld = state.Entries
		if list := state.visible(); state.Selected < len(list) {
			state.selectedRef = pickerString(list[state.Selected]["ref"])
		}
	}
	if update.UpdatedAt == "" {
		state.Entries = mergeBoardEntries(state.refreshOld, update.Entries, update.FinishedMachines)
	} else {
		// The load is complete: only the new result - no old rows survive.
		state.Entries = append([]PickerEntry(nil), update.Entries...)
	}
	state.Failures = append([]PickerFailure(nil), update.Failures...)
	if state.NavFailure != nil {
		state.Failures = append(state.Failures, *state.NavFailure)
	}
	state.Loading = update.Loading
	state.LoadingLocal = update.LoadingLocal
	if ref := state.selectedRef; ref != "" {
		for i, entry := range state.visible() {
			if pickerString(entry["ref"]) == ref {
				state.Selected = i
				break
			}
		}
		// If the ref is not there yet, the selection stays where it is.
	}
	state.Query, state.Exit, state.Copied, state.LastEntry, state.TerminalKeys, state.NavPending, state.NavTarget = query, exit, copied, lastEntry, keys, navPending, navTarget
	state.clamp()
	if update.UpdatedAt == "" {
		return
	}
	// The whole load is complete: swap the bookkeeping and only now drop
	// the selection to the first row if the ref truly disappeared.
	state.UpdatedAt = update.UpdatedAt
	state.refreshing = false
	state.refreshOld = nil
	if state.selectedRef != "" {
		stillThere := false
		for _, entry := range state.visible() {
			if pickerString(entry["ref"]) == state.selectedRef {
				stillThere = true
				break
			}
		}
		if !stillThere {
			state.Selected = 0
		}
	}
	state.selectedRef = ""
	state.clamp()
}

// mergeBoardEntries keeps all of the previous list's rows for the machines
// whose find has not terminated yet and only the new load's rows for the
// ones that did (new order first, then the old-only machines in the old
// order).
func mergeBoardEntries(old, fresh []PickerEntry, finished []string) []PickerEntry {
	out := append([]PickerEntry(nil), fresh...)
	done := map[string]bool{}
	for _, machine := range finished {
		done[machine] = true
	}
	for _, entry := range old {
		if done[pickerString(entry["machine"])] {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// requestBoardUpdate queues one refresh on the loader. The loader runs
// loads one at a time (it only starts the next after the current one
// ends), and the one-slot buffer coalesces requests made while a load is
// in progress into the single refresh that follows it.
func requestBoardUpdate(updateReq chan<- struct{}) {
	select {
	case updateReq <- struct{}{}:
	default:
	}
}

// redrawSession draws one frame of the owned popup from the shared
// runtime state and records the drawn frame (the page keys measure their
// step against it with the shared renderer viewport math). When the mouse
// negotiation has settled without being able to deliver the wheel, the
// frame says so honestly instead of pretending the control works.
//
// The shared transport paints the complete frame as one bounded write
// without clearing the viewport before the paint: each row is erased at
// its addressed start, immediately before its own content (a trailing
// erase would drop the last column of a full-width row on terminals with
// pending auto-wrap), so no stale cell survives; stale rows left below a
// shorter frame (a list or dimension change) are cleared after the paint,
// from the first row the new frame does not paint, never by a newline
// scroll of the final viewport row. An identical frame for the same
// viewport is skipped (per runtime, not across independent sessions). A
// failed or short write may leave a partial frame on screen, so it
// invalidates the painted cache - the next draw, even of the previously
// painted frame, is written completely again - while a successful draw is
// the only thing ever recorded as painted. The single write removes the
// old separate clear and per-row writes; it is not pixel-level proof that
// a terminal never shows intermediate processing of the frame.
func redrawSession(w io.Writer, state *BoardState, width int) {
	width, height := modalDimensions(w, width)
	state.FrameWidth, state.FrameHeight = width, height
	notice := ""
	switch {
	case state.NavPending:
		// The navigation status supersedes every earlier feedback.
		notice = "Focusing selected pane… Esc cancels"
	case state.CopyNotice != "":
		notice = state.CopyNotice
	case state.viewPreferenceFailed:
		notice = "View changed here, but could not be saved"
	case state.Mouse != nil && state.Mouse.decided && !state.Mouse.wheelAvailable():
		notice = "mouse wheel unavailable: SGR mouse modes unconfirmed"
	}
	frame := renderSessionModal(state, width, height, notice)
	seq := buildModalFrame(frame)
	if rows := modalFrameRows(frame); rows < height {
		// Stale rows of a taller previous frame: clear them, after the
		// paint, from the first row the new frame does not paint. When the
		// frame fills the viewport there is nothing below it to clear.
		seq += fmt.Sprintf("\x1b[%d;1H\x1b[J", rows+1)
	}
	if state.lastFramePainted && state.lastFrameW == width && state.lastFrameH == height && state.lastFrameSeq == seq {
		return // identical frame for the same viewport: nothing to repaint
	}
	paint := seq
	if state.lastFramePainted && state.lastFrameW == width && state.lastFrameH == height {
		paint = buildModalPatch(state.lastFrameText, frame)
	}
	if err := writeModalFrame(w, paint); err != nil {
		// A failed or short write may have left a partial frame on screen:
		// invalidate the painted cache so the next draw - even of the
		// previously painted frame - is written completely again.
		state.lastFramePainted = false
		return
	}
	state.lastFrameSeq, state.lastFrameW, state.lastFrameH, state.lastFramePainted = seq, width, height, true
	state.lastFrameText = frame
}

func RunBoard(env platform.Env, platformName, executable string) (code int) {
	stdin, stdout := os.Stdin, os.Stdout
	isTTY := false
	if info, err := stdin.Stat(); err == nil && info.Mode()&os.ModeCharDevice != 0 {
		isTTY = true
	}
	shutdownSignals := make(chan os.Signal, 1)
	signal.Notify(shutdownSignals, pickerShutdownSignals()...)
	defer signal.Stop(shutdownSignals)
	state := newSessionState(env, platformName, isTTY)
	return withPickerTerminalMouse(isTTY, func() (func() error, error) { return setPickerRaw(stdin) }, stdout, state, func() int {
		return runSessionLoopWithSignals(env, platformName, executable, stdin, stdout, isTTY, shutdownSignals, false, state)
	})
}

// runBoardLoopWithSignals is the board command's entry into the one
// board-derived session runtime; it is a thin adapter, not a second
// loop.
func runBoardLoopWithSignals(env platform.Env, platformName, executable string, stdin, stdout *os.File, isTTY bool, shutdownSignals <-chan os.Signal) int {
	return runSessionLoopWithSignals(env, platformName, executable, stdin, stdout, isTTY, shutdownSignals, false, NewBoardState())
}

// runSessionLoopWithSignals is the one production session-list runtime:
// the board-derived loop that both public commands route into. It owns
// the progressive discovery with per-batch publications, the automatic
// ten-second refresh coalesced to one load, the pinned-ref selection
// safety, the task-title search and detail, the status totals and the
// unified session display (every pane, the unknown/empty/"-" kinds as
// terminals, the scope/status filters, the page and wheel navigation and
// the temporary SGR mouse tracking negotiated by the caller). flavorPicker
// keeps the picker command's non-TTY display contract.
func runSessionLoopWithSignals(env platform.Env, platformName, executable string, stdin, stdout *os.File, isTTY bool, shutdownSignals <-chan os.Signal, flavorPicker bool, state *BoardState) int {
	state.flavorPicker = flavorPicker
	state.ViewState.Enabled = isTTY
	state.LoadingLocal = true
	state.Loading = 1
	if isTTY {
		_, _ = fmt.Fprint(stdout, "\x1b[?25l")
	}
	width := 80
	if isTTY {
		if n, err := pickerTerminalWidth(stdout.Fd()); err == nil && n > 0 {
			width = n
		}
	}
	redrawSession(stdout, state, width)
	loadCtx, cancelLoad := contextpkg.WithCancel(contextpkg.Background())
	updates := make(chan *BoardState, 16)
	updateReq := make(chan struct{}, 1)
	loadDone := make(chan struct{})
	go func() {
		defer close(updates)
		defer close(loadDone)
		for {
			loaded := NewBoardState()
			loaded.LoadingLocal = true
			loaded.Loading = 1
			publish := func() {
				select {
				case updates <- cloneBoardState(loaded):
				case <-loadCtx.Done():
					return
				}
			}
			loadBoardEntries(loadCtx, loaded, env, platformName, publish)
			loaded.UpdatedAt = boardNow().Format("15:04:05")
			select {
			case updates <- cloneBoardState(loaded):
			case <-loadCtx.Done():
				return
			}
			select {
			case <-updateReq:
			case <-loadCtx.Done():
				return
			}
		}
	}()
	defer func() { cancelLoad(); <-loadDone }()
	type inputResult struct {
		value string
		err   error
	}
	input := make(chan inputResult)
	doneInput := make(chan struct{})
	defer close(doneInput)
	go func() {
		reader := bufio.NewReader(stdin)
		for {
			r, _, err := reader.ReadRune()
			select {
			case input <- inputResult{value: string(r), err: err}:
			case <-doneInput:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	refresh := time.NewTicker(boardRefreshInterval)
	defer refresh.Stop()
	// One in-flight navigation at a time; its context is cancelled when
	// the loop exits for any reason.
	navResults := make(chan NavigationResult, 1)
	var navCancelActive contextpkg.CancelFunc
	defer func() {
		if navCancelActive != nil {
			navCancelActive()
		}
	}()
	// One in-flight team-view load at a time: a new
	// open cancels the previous in-flight load, and the loop's exit - for
	// any reason - cancels the current one, so a closed modal never
	// leaves a CLI read in flight.
	teamResults := make(chan pickerTeamViewResult, 1)
	var teamLoad boardTeamLoad
	defer teamLoad.shutdown()
	var escTimer <-chan time.Time
	var resize <-chan time.Time
	lastWidth, lastHeight := modalDimensions(stdout, width)
	if isTTY {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		resize = ticker.C
	}
	var updateChannel <-chan *BoardState = updates
	for {
		select {
		case <-resize:
			cols, rows := modalDimensions(stdout, width)
			if cols != lastWidth || rows != lastHeight {
				lastWidth, lastHeight = cols, rows
				redrawSession(stdout, state, width)
			}
			if state.pollMouse(stdout) {
				redrawSession(stdout, state, width)
			}
		case <-shutdownSignals:
			state.shutdown()
		case <-refresh.C:
			requestBoardUpdate(updateReq)
		case update, ok := <-updateChannel:
			if !ok {
				updateChannel = nil
				continue
			}
			applyBoardUpdate(state, update)
			redrawSession(stdout, state, width)
		case item := <-input:
			if item.err != nil {
				if item.err == io.EOF {
					if state.EscPending {
						state.FlushEsc()
					}
					if state.Exit == "" {
						state.shutdown()
					}
					break
				}
				return 1
			}
			// The DECRQM answers of the mouse negotiation arrive on this
			// same reader: poll the bounded decision right after they
			// pass through the decoder, and only while the popup is
			// still open (an exit must not be followed by an enable).
			if state.Exit == "" && state.pollMouse(stdout) {
				redrawSession(stdout, state, width)
			}
			// Protocol parameters and replies are not visible changes. Decode
			// before applying so a nine-mode query does not redraw the whole
			// modal for every reply byte and delay its own bounded handshake.
			key := state.TerminalKeys.Feed([]rune(item.value)[0])
			action := ""
			if key != "" {
				action = state.ApplyKey(key)
			}
			if action == "copy" {
				// c copied without closing: the payload goes to the
				// clipboard and the confirmation is set only after CopyText
				// returned - a native tool acknowledged the write, the OSC
				// 52 fallback is described honestly because its delivery
				// cannot be acknowledged. No Herdr notification call: the
				// modal stays open, the status area carries the feedback and
				// the frame is redrawn so the confirmation is visible; the
				// input loop keeps running for the next key.
				state.CopyNotice = copyNoticeFor(CopyText(*state.Copied, env, platformName), *state.Copied)
				redrawSession(stdout, state, width)
			}
			if action == "navigate" {
				state.CopyNotice = "" // the navigation takes over the status area
				redrawSession(stdout, state, width)
				navCtx, navCancel := contextpkg.WithCancel(contextpkg.Background())
				navCancelActive = navCancel
				target := state.NavTarget
				go func() {
					res := NavigateSelection(navCtx, target, env, platformName)
					select {
					case navResults <- res:
					case <-navCtx.Done():
					}
				}()
			}
			if action == "team" {
				// i opened the team view on a local row:
				// the loop's worker runs the two NOWRITE reads off the key
				// path (the discovery-load pattern) and only the finished
				// result comes back to the loop's goroutine, which applies
				// it - a late result of another row or of a superseded open
				// is discarded there. A new open supersedes the in-flight
				// load: it is cancelled, so the closed row's reads stop
				// running.
				view := state.TeamView
				teamCtx := teamLoad.start(view.loadSeq)
				redrawSession(stdout, state, width)
				teamLoad.run(func() {
					runPickerTeamViewLoad(teamCtx, executable, env, platformName, view, func(res pickerTeamViewResult) {
						select {
						case teamResults <- res:
						case <-teamCtx.Done():
						}
					})
				})
			}
			if action == "update" {
				requestBoardUpdate(updateReq)
				redrawSession(stdout, state, width)
				break
			}
			// The answers of this chunk have just passed the decoder:
			// the bounded decision can happen before the next input or
			// tick, still inside the one input loop.
			if state.Exit == "" && state.pollMouse(stdout) {
				redrawSession(stdout, state, width)
			}
			if action != "" {
				break
			}
			if state.EscPending {
				escTimer = time.After(50 * time.Millisecond)
			} else {
				escTimer = nil
			}
			if key != "" {
				redrawSession(stdout, state, width)
			}
		case <-escTimer:
			if state.FlushEsc() != "" {
				break
			}
			redrawSession(stdout, state, width)

		case res := <-navResults:
			navCancelActive = nil
			if res.OK && res.Remote {
				bin := env.Get("HERDR_BIN_PATH")
				if bin == "" {
					bin = "herdr"
				}
				_ = pickerRun(contextpkg.Background(), bin, []string{"notification", "show", "soho", "--body", res.Note, "--sound", "none"}, env, platformName, 30_000, "")
			}
			boardNavApplyResult(state, res)
			redrawSession(stdout, state, width)
		case res := <-teamResults:
			teamLoad.finish(res)
			boardApplyTeamResult(state, res)
			redrawSession(stdout, state, width)
		}
		if state.Exit != "" {
			break
		}
	}
	if state.Exit == "copy" || state.Exit == "esc" || state.Exit == "navigate" {
		return 0
	}
	return 1
}

// pollMouse runs the bounded SGR mouse negotiation inside the one input
// loop (the DECRQM answers arrive on the same reader the keys arrive
// on): it writes nothing once decided and is a no-op off the TTY. It
// reports whether it just decided, so the caller can redraw the honest
// wheel guidance.
func (s *BoardState) pollMouse(w io.Writer) bool {
	if s.Mouse == nil {
		return false
	}
	return s.Mouse.poll(w)
}

// copyNoticeFor is the modal status-area confirmation of one c copy: the
// concise "Copied <ref>" when the clipboard tool acknowledged the write,
// and the honest OSC 52 wording when only the fallback ran, whose
// delivery cannot be acknowledged.
func copyNoticeFor(result ClipboardResult, payload string) string {
	if result.Path == "osc52" {
		return "Copied via OSC 52 (unconfirmed): " + payload
	}
	return "Copied " + payload
}

// boardNavApplyResult folds one navigation outcome into the board state:
// a success closes; a failure stays visible with the query, selection and
// pinned ref preserved (an actionable modal) and nothing copied.
func boardNavApplyResult(state *BoardState, res NavigationResult) {
	machine := pickerString(state.NavTarget["machine"])
	state.NavPending = false
	state.NavTarget = nil
	if res.OK {
		state.Exit = "navigate"
		return
	}
	state.NavFailure = &PickerFailure{Label: machine, Cause: res.Cause}
	state.Failures = append(state.Failures, *state.NavFailure)
}

// boardApplyTeamResult folds one finished team-view load into the board
// state on the loop's goroutine: the loop's worker
// published the result and only the loop touches the live state; a
// result of another row or of a superseded open is discarded.
func boardApplyTeamResult(state *BoardState, res pickerTeamViewResult) bool {
	return applyTeamView(state.TeamView, res)
}
