package plugin

import (
	"fmt"
	"strings"

	"github.com/djalmajr/soho/internal/sessionref"
)

// Theme-neutral SGR styles for the session popups: bold marks the machine
// group headers, dim marks the chrome (header, separator, workspace
// headers, placeholders, detail). The selection band reuses the legacy
// modal highlight (reverse video), so no private theme API or fixed RGB
// palette is involved.
const (
	sessionStyleBold  = "\x1b[1m"
	sessionStyleDim   = "\x1b[2m"
	sessionStyleReset = modalHighlightOff
)

// SessionRenderOptions is the single input to the shared session popup
// renderer. Entries arrive already filtered and sorted by the input
// worker; the renderer only presents them and never re-sorts, re-filters
// or keeps its own selection state.
type SessionRenderOptions struct {
	View          string // "table" or "tree"; empty defaults to table
	Sort          string // active sort field; empty defaults to pane ascending
	Descending    bool
	SearchFocused bool
	Entries       []PickerEntry // the visible entries, in input order
	Total         int           // unfiltered entry count; 0 = len(Entries)
	Selected      int           // index into Entries; -1 = no cursor
	PinnedMissing bool          // board pin left the load: no cursor + guidance
	Query         string
	Loading       int
	LoadingLocal  bool
	Failures      []PickerFailure
	Notice        string // consumer-resolved feedback for the fixed bottom status line (copy confirmation or the navigation/failure guidance that outranks it; empty = Ready)
	Totals        string // board totals line (counts + update time); "" = count from Entries
	Noun          string // singular selection noun ("pane"/"agent"); default "session"
	Board         bool   // board flavor: r Refresh in the footer, task title in the detail
	Scope         string // scope filter disclosed in the footer (always, incl. all)
	Status        string // status filter disclosed in the footer (always, incl. all)
	// TeamKey discloses the "i Team" shortcut in the footer's action
	// line: the board-derived session runtime binds i to the read-only
	// team view in the enhanced list focus, so
	// the unified frame discloses it - the legacy frames (non-enhanced
	// picker and board) leave it off, and the footer never advertises a
	// key the frame does not bind.
	TeamKey bool
	// TeamView is the session popup's open read-only team view (shared
	// by the picker's and the board's states):
	// when it is set, renderSessionPopup replaces the row window with
	// the view inside the same frame.
	TeamView *pickerTeamView
}

func (o *SessionRenderOptions) normalize() {
	if o.View == "" {
		o.View = "table"
	}
	if o.Sort == "" {
		o.Sort = "pane"
	}
	if o.Noun == "" {
		o.Noun = "session"
	}
	if o.Selected > len(o.Entries)-1 {
		o.Selected = len(o.Entries) - 1
	}
	if o.Selected < -1 {
		o.Selected = -1
	}
}

func (o *SessionRenderOptions) selectedEntry() PickerEntry {
	if o.Selected < 0 || o.Selected >= len(o.Entries) {
		return nil
	}
	return o.Entries[o.Selected]
}

// sessionArrow is the direction marker for the active sort.
func sessionArrow(descending bool) string {
	if descending {
		return "↓"
	}
	return "↑"
}

// sessionViewLabel is the sanitized, display name of the active view
// ("Table"/"Tree"; unknown values pass through sanitized).
func sessionViewLabel(o *SessionRenderOptions) string {
	switch StripPickerControls(o.View) {
	case "table":
		return "Table"
	case "tree":
		return "Tree"
	default:
		return StripPickerControls(o.View)
	}
}

// sessionSortLabel is the sanitized, display name of the active sort
// field ("Pane"/"Workspace"/"Status"; unknown values pass through
// sanitized).
func sessionSortLabel(o *SessionRenderOptions) string {
	switch StripPickerControls(o.Sort) {
	case "pane":
		return "Pane"
	case "workspace":
		return "Workspace"
	case "status":
		return "Status"
	default:
		return StripPickerControls(o.Sort)
	}
}

// sessionHeaderLine is the meta line: view name, active sort with its
// direction arrow, the count (board totals, or the visible count) and
// the loading state. Every external part is sanitized before it is
// wrapped in the owned styles. There is no popup title of its own: the
// host border carries it, so no redundant inner "Pick pane" line.
func sessionHeaderLine(o *SessionRenderOptions) string {
	count := StripPickerControls(o.Totals)
	if count == "" {
		n := len(o.Entries)
		count = fmt.Sprintf("%d %s", n, o.Noun)
		if n != 1 {
			count += "s"
		}
	}
	line := fmt.Sprintf("%s · sort %s %s", sessionViewLabel(o), sessionSortLabel(o), sessionArrow(o.Descending))
	if o.Loading > 0 {
		state := "loading remote…"
		if o.LoadingLocal {
			state = "loading local…"
		}
		line += " · " + state
	}
	return line + " · " + count
}

// sessionHighlightBand pads content to the full available width and wraps
// it in reverse video: the selection and the focused search are bands
// across the whole popup width, never partial text spans.
func sessionHighlightBand(content string, avail int) string {
	if avail <= 0 {
		return ""
	}
	if w := displayWidth(content); w < avail {
		content += strings.Repeat(" ", avail-w)
	} else if w > avail {
		content = displaySlice(content, avail)
	}
	return modalHighlightOn + content + modalHighlightOff
}

// sessionSearchParts returns the visible search text and whether it is
// the dim placeholder: the query is shown sanitized, and a focused
// search is a full-width reverse band, visibly distinct from the plain
// prompt.
func sessionSearchParts(o *SessionRenderOptions) (visible string, placeholder bool) {
	query := StripPickerControls(o.Query)
	if query == "" {
		return "> Search…", true
	}
	return "> " + query, false
}

// sessionFooterSegments is the exact control guidance, three lines from
// top to bottom; the last segment of each line is the one that must
// survive a tiny width, so Esc Close wins. The scope and status filters
// always join the sort line - including the default "all" - so the footer
// discloses the unified session list it documents. The PgUp/PgDn page
// keys and the wheel join the navigation line only at a normal-sized
// viewport: when the full guidance still fits, they are disclosed; in a
// narrow one they drop out as a unit so the pinned keys keep their room.
func sessionFooterSegments(o *SessionRenderOptions, avail int) [][]string {
	other := "Tree"
	if o.View == "tree" {
		other = "Table"
	}
	nav := []string{"↑↓ Select", "Tab Search/List", "v " + other}
	if o.Board {
		nav = append(nav, "r Refresh")
	}
	withPage := append(append([]string{}, nav...), "PgUp/PgDn Page", "Wheel Scroll")
	if displayWidth(strings.Join(withPage, " · ")) <= avail {
		nav = withPage
	}
	filters := []string{"p Pane", "w Workspace", "s Status", "f " + sessionScopeLabel(o.Scope), "t " + sessionStatusLabel(o.Status)}
	actions := []string{"c Copy", "Enter Focus", "Esc Close"}
	if o.TeamKey {
		actions = append([]string{"i Team"}, actions...)
	}
	return [][]string{
		nav,
		filters,
		actions,
	}
}

// sessionStatusCore is the bottom-anchored feedback, ordered from the
// least to the most essential so a tight popup trims from the front: the
// no-results/empty line, the machine failures (a navigation failure
// rides along in Failures) and the board pin-missing guidance. The
// navigation/copy notice has its own fixed strip below the footer
// (sessionStatusStripParts), so it never changes the core's reserved rows.
func sessionStatusCore(o *SessionRenderOptions) []string {
	var core []string
	n := len(o.Entries)
	total := o.Total
	if total <= 0 {
		total = n
	}
	if n == 0 {
		if total > 0 {
			core = append(core, fmt.Sprintf("no results for %q", StripPickerControls(o.Query)))
		} else if o.Loading == 0 && len(o.Failures) == 0 {
			core = append(core, "no "+o.Noun+"s")
		}
	}
	for _, failure := range o.Failures {
		core = append(core, "machine "+StripPickerControls(failure.Label)+": failed ("+StripPickerControls(failure.Cause)+")")
	}
	if o.PinnedMissing {
		core = append(core, "selected session left this load; pick another with ↑/↓")
	}
	return core
}

// sessionStatusStripRows is the reserved fixed status strip below the
// footer for a frame of height h: at ordinary heights the theme-neutral
// divider and the one-line status (the operator's fixed copy-
// confirmation strip, kept below the shortcuts); at tiny heights the
// deterministic degradation - first the divider, then the status line -
// so the frame never overflows and the footer and at least one
// selectable row keep their room.
func sessionStatusStripRows(height int) int {
	switch {
	case height >= 10:
		return 2
	case height >= 7:
		return 1
	default:
		return 0
	}
}

// sessionStatusStripLabel is the theme-neutral label of the fixed bottom
// status line at ordinary widths: "STATUS" plus the two-column gap that
// separates it from the content.
const sessionStatusStripLabel = "STATUS  "

// sessionStatusStripParts is the content of the fixed bottom status
// line and its emphasis: "Ready" while idle, or the consumer-resolved
// notice (the copy confirmation with its truthful wording, or the
// navigation/failure guidance that outranks it) sanitized like every
// other display value. The content keeps normal emphasis; the confirmed
// native copy is bold so the acknowledgement stays visible, while the
// unconfirmed OSC 52 wording (which must never read as confirmed) and
// every other guidance stay plain. The emphasis keys off the truthful
// wording the consumer owns (copyNoticeFor): the confirmed copy reads
// "Copied <ref>", the fallback keeps "(unconfirmed)" in the text.
func sessionStatusStripParts(o *SessionRenderOptions) (content string, bold bool) {
	content = "Ready"
	if note := StripPickerControls(o.Notice); note != "" {
		content = note
		bold = strings.HasPrefix(content, "Copied ") && !strings.Contains(content, "(unconfirmed)")
	}
	return content, bold
}

// sessionStatusSliceTail is the tail-keeping counterpart of
// displaySlice for the fixed status line: a whole-rune suffix of text
// that fits width display columns, starting with a single ellipsis when
// the text does not fit, so a clipped notice keeps its end - the useful
// pane reference - instead of its head.
func sessionStatusSliceTail(text string, width int) string {
	if width <= 0 {
		return ""
	}
	if displayWidth(text) <= width {
		return text
	}
	runes := []rune(text)
	start := len(runes)
	used := 0
	for i := len(runes) - 1; i >= 0; i-- {
		w := displayRuneWidth(runes[i])
		if used+w > width-1 { // keep one column for the ellipsis
			break
		}
		used += w
		start = i
	}
	return "\u2026" + string(runes[start:])
}

// sessionStatusStripLine is the fixed bottom status line: the dim
// "STATUS" label and the content at ordinary widths, and the content
// alone at very narrow widths (the notice outranks the label, and a
// clipped notice keeps its tail - the pane reference - instead of its
// head). The label is dim chrome; the content keeps normal emphasis and
// the confirmed copy is bold.
func sessionStatusStripLine(o *SessionRenderOptions, avail, pad int) string {
	if avail <= 0 {
		return ""
	}
	content, bold := sessionStatusStripParts(o)
	contentStyle := ""
	if bold {
		contentStyle = sessionStyleBold
	}
	line := ""
	if displayWidth(sessionStatusStripLabel)+displayWidth(content) <= avail {
		line = sessionStyleDim + sessionStatusStripLabel + sessionStyleReset + contentStyle + content
	} else {
		if displayWidth(content) > avail {
			content = sessionStatusSliceTail(content, avail)
		}
		line = contentStyle + content
	}
	if contentStyle != "" {
		line += sessionStyleReset
	}
	if pad > 0 {
		line = strings.Repeat(" ", pad) + line
	}
	return line
}

// sessionDetailLine is the selected entry's metadata: the full ref, the
// tab label and the cwd, joined with " · ". The board adds the task
// title only when the whole line still fits.
func sessionDetailLine(o *SessionRenderOptions, avail int) string {
	entry := o.selectedEntry()
	if entry == nil {
		return ""
	}
	parts := []string{}
	if ref := StripPickerControls(pickerString(entry["ref"])); ref != "" {
		parts = append(parts, ref)
	}
	if tab := StripPickerControls(pickerString(entry["tab_label"])); tab != "" {
		parts = append(parts, tab)
	}
	if cwd := StripPickerControls(pickerString(entry["cwd"])); cwd != "" {
		parts = append(parts, cwd)
	}
	if o.Board {
		if title := BoardTitle(entry["title"]); title != "" {
			candidate := strings.Join(append(append([]string{}, parts...), title), " · ")
			if displayWidth(candidate) <= avail {
				parts = append(parts, title)
			}
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return displaySlice(strings.Join(parts, " · "), avail)
}

func displayMax(cells []string) int {
	max := 1
	for _, cell := range cells {
		if w := displayWidth(cell); w > max {
			max = w
		}
	}
	return max
}

// fitDisplayWidths shrinks the natural column widths into budget display
// columns: every column keeps at least one, and the spare columns go to
// the most needed columns first (the weighted column — the table's main
// pane cell — first), so the essential cells stay readable.
func fitDisplayWidths(natural []int, weights []int, budget int) []int {
	n := len(natural)
	widths := make([]int, n)
	for i := range widths {
		widths[i] = 1
	}
	if budget < n {
		for i := budget; i < n; i++ {
			widths[i] = 0
		}
		return widths
	}
	left := budget - n
	weight := func(i int) int {
		if i < len(weights) && weights[i] > 0 {
			return weights[i]
		}
		return 1
	}
	for left > 0 {
		best := -1
		for i := range widths {
			if natural[i] <= widths[i] {
				continue
			}
			if best == -1 || (natural[i]-widths[i])*weight(i) > (natural[best]-widths[best])*weight(best) {
				best = i
			}
		}
		if best == -1 {
			break
		}
		widths[best]++
		left--
	}
	return widths
}

// sessionTableHeaders is the visible aligned column-header row, in the
// broad-to-specific order. The header text takes part in the column
// sizing and the agent label is omitted with the agent column at narrow
// widths.
var sessionTableHeaders = []string{"Machine", "Workspace", "Pane", "ID", "Agent", "Status"}

// sessionPaneID is the pane identifier both views show: the entry's
// pane_id, or the pane parsed from its full reference; empty when neither
// names one.
func sessionPaneID(entry PickerEntry) string {
	if id := StripPickerControls(pickerString(entry["pane_id"])); id != "" {
		return id
	}
	if ref := sessionref.ParseRef(StripPickerControls(pickerString(entry["ref"]))); ref != nil {
		return ref.PaneID
	}
	return ""
}

// sessionTableWorkspaceLabels disambiguates equal visible labels within
// a machine without confusing display sanitization with group identity.
func sessionTableWorkspaceLabels(entries []PickerEntry) []string {
	type labelKey struct{ machine, label string }
	identities := map[labelKey]map[string]bool{}
	labels := make([]string, len(entries))
	for i, entry := range entries {
		label := modalText(entry["workspace_label"])
		labels[i] = label
		key := labelKey{sessionMachineOf(entry), label}
		if identities[key] == nil {
			identities[key] = map[string]bool{}
		}
		identities[key][sessionWorkspaceKey(entry)] = true
	}
	for i, entry := range entries {
		key := labelKey{sessionMachineOf(entry), labels[i]}
		if len(identities[key]) > 1 {
			labels[i] += " · " + modalText(entry["workspace_id"])
		}
	}
	return labels
}

// sessionTableWidths sizes the columns into avail display columns,
// counting the one-column selection marker and letting the header
// labels participate in the sizing. The agent column is dropped before
// any essential column is compressed, so the pane column keeps as much
// room as the window allows. The identifier is never cut, because a
// partial one can read as another pane: it stays whole while the other
// columns keep at least one cell each, and leaves the row otherwise (the
// detail line still carries the full reference).
func sessionTableWidths(cols [][]string, avail int) (widths []int, useAgent, useID bool) {
	if avail <= 0 {
		return make([]int, len(sessionTableHeaders)), false, false
	}
	natural := make([]int, len(sessionTableHeaders))
	sum := 0
	for i := range natural {
		natural[i] = displayMax(cols[i])
		if h := displayWidth(sessionTableHeaders[i]); h > natural[i] {
			natural[i] = h
		}
		sum += natural[i]
	}
	if 2+sum+10 <= avail {
		return natural, true, true
	}
	essential := []int{natural[0], natural[1], natural[2], natural[5]}
	weights := []int{1, 1, 2, 1}
	if budget := avail - 10 - natural[3]; budget >= len(essential) {
		w := fitDisplayWidths(essential, weights, budget)
		return []int{w[0], w[1], w[2], natural[3], 0, w[3]}, false, true
	}
	budget := avail - 8
	if budget < 0 {
		budget = 0
	}
	w := fitDisplayWidths(essential, weights, budget)
	return []int{w[0], w[1], w[2], 0, 0, w[3]}, false, false
}

// sessionTableLines renders the aligned, padded table: the fixed column
// header row first, then one row per entry in the broad-to-specific
// order machine, workspace, pane (the main cell via sessionPaneName, so
// shell panes fall back to title/pane/ref), identifier, agent and status,
// each with a two-column selection marker ("> " on the selected row). The selected
// row is a full-width reverse band; the other rows stay plain so the
// band reads as the marker.
func sessionTableLines(entries []PickerEntry, sel int, avail int) []string {
	if avail <= 0 || len(entries) == 0 {
		return nil
	}
	cells := make([][]string, len(sessionTableHeaders))
	for i := range cells {
		cells[i] = make([]string, len(entries))
	}
	workspaceLabels := sessionTableWorkspaceLabels(entries)
	for i, entry := range entries {
		cells[0][i] = modalText(sessionMachineOf(entry))
		cells[1][i] = workspaceLabels[i]
		cells[2][i] = sessionPaneName(entry)
		cells[3][i] = modalText(sessionPaneID(entry))
		cells[4][i] = sessionAgentLabel(entry)
		cells[5][i] = modalText(entry["status"])
	}
	widths, useAgent, useID := sessionTableWidths(cells, avail)
	pad := func(cell string, width int) string {
		if width <= 0 {
			return ""
		}
		if displayWidth(cell) > width {
			cell = displaySlice(cell, width)
		}
		return cell + strings.Repeat(" ", width-displayWidth(cell))
	}
	row := func(marker string, col []string, banded bool) string {
		parts := []string{
			pad(col[0], widths[0]),
			pad(col[1], widths[1]),
			pad(col[2], widths[2]),
		}
		if useID {
			parts = append(parts, pad(col[3], widths[3]))
		}
		if useAgent {
			parts = append(parts, pad(col[4], widths[4]))
		}
		parts = append(parts, pad(col[5], widths[5]))
		line := marker + strings.Join(parts, "  ")
		if displayWidth(line) > avail {
			line = displaySlice(line, avail)
		}
		if banded {
			line = sessionHighlightBand(line, avail)
		}
		return line
	}
	header := append([]string{}, sessionTableHeaders...)
	lines := []string{row("  ", header, false)}
	for i := range entries {
		marker := "  "
		if i == sel {
			marker = "> "
		}
		rowCells := []string{cells[0][i], cells[1][i], cells[2][i], cells[3][i], cells[4][i], cells[5][i]}
		lines = append(lines, row(marker, rowCells, i == sel))
	}
	return lines
}

const (
	sessionRowMachine = iota
	sessionRowWorkspace
	sessionRowLeaf
)

type sessionTreeRow struct {
	text   string
	kind   int
	entry  int // leaf: the entry index; -1 for the group headers
	parent int // leaf: the row of its workspace header; -1 otherwise
}

// sessionTreeNodes lays the visible entries out as machine > workspace >
// leaf. The exact incoming order of state.visible() is preserved: group
// headers are formed in first-occurrence order (the input worker
// delivers already-grouped rows, so no group keys are re-sorted here),
// leaves keep their input order within a group, the groups are keyed by
// machine plus workspace ID (the sessionWorkspaceKey fallback) so
// equally named workspaces with different IDs stay distinct, and every
// group header carries the count of the filtered entries under it.
// Missing machine values read "local", consistently with the input
// helper. Visible branch connectors show the machine > workspace > leaf
// nesting. leafPos maps each entry index to its leaf row.
func sessionTreeNodes(entries []PickerEntry, avail int) (rows []sessionTreeRow, leafPos []int) {
	type groupKey struct{ machine, wsKey string }
	type group struct {
		machine, wsLabel, wsID string
		items                  []int
	}
	groups := map[groupKey]*group{}
	var order []groupKey
	machineTotal := map[string]int{}
	for i, entry := range entries {
		machine := sessionMachineOf(entry)
		key := groupKey{machine, sessionWorkspaceKey(entry)}
		g := groups[key]
		if g == nil {
			g = &group{machine: machine}
			groups[key] = g
			order = append(order, key)
		}
		if g.wsLabel == "" && g.wsID == "" {
			g.wsLabel = StripPickerControls(pickerString(entry["workspace_label"]))
			g.wsID = StripPickerControls(pickerString(entry["workspace_id"]))
		}
		g.items = append(g.items, i)
		machineTotal[machine]++
	}
	lastWsOfMachine := map[string]int{}
	for gi, key := range order {
		lastWsOfMachine[groups[key].machine] = gi
	}
	leafPos = make([]int, len(entries))
	for i := range leafPos {
		leafPos[i] = -1
	}
	lastMachine := ""
	for gi, key := range order {
		g := groups[key]
		if g.machine != lastMachine {
			machineText := fmt.Sprintf("%s (%d)", StripPickerControls(g.machine), machineTotal[g.machine])
			if displayWidth(machineText) > avail {
				machineText = displaySlice(machineText, avail)
			}
			rows = append(rows, sessionTreeRow{
				text:  sessionStyleBold + machineText + sessionStyleReset,
				kind:  sessionRowMachine,
				entry: -1,
			})
			lastMachine = g.machine
		}
		wsConnect := "├─ "
		continuation := "│  "
		if lastWsOfMachine[g.machine] == gi {
			wsConnect = "└─ "
			continuation = "   "
		}
		label := g.wsLabel
		if label == "" {
			label = "-"
		}
		ws := label
		if g.wsID != "" {
			ws += " · " + g.wsID
		}
		wsRow := len(rows)
		wsText := wsConnect + fmt.Sprintf("%s (%d)", ws, len(g.items))
		if displayWidth(wsText) > avail {
			wsText = displaySlice(wsText, avail)
		}
		rows = append(rows, sessionTreeRow{
			text:  sessionStyleDim + wsText + sessionStyleReset,
			kind:  sessionRowWorkspace,
			entry: -1,
		})
		for li, idx := range g.items {
			entry := entries[idx]
			prefix := continuation + "├─ "
			if li == len(g.items)-1 {
				prefix = continuation + "└─ "
			}
			pane := sessionPaneName(entry)
			status := StripPickerControls(pickerString(entry["status"]))
			paneID := sessionPaneID(entry)
			suffix := ""
			if paneID != "" {
				suffix = " (" + paneID + ")"
				if displayWidth(prefix)+displayWidth(suffix) > avail {
					prefix = displaySlice(prefix, max(0, avail-displayWidth(suffix)))
				}
			}
			budget := max(0, avail-displayWidth(prefix)-displayWidth(suffix))
			body := displaySlice(pane, budget)
			if status != "" {
				paneBudget := budget - displayWidth(status) - 3
				if paneBudget > 0 {
					body = displaySlice(pane, paneBudget) + " · " + status
				}
			}
			line := prefix + body + suffix
			if displayWidth(line) > avail {
				line = displaySlice(line, avail)
			}
			rows = append(rows, sessionTreeRow{
				text:   line,
				kind:   sessionRowLeaf,
				entry:  idx,
				parent: wsRow,
			})
			leafPos[idx] = len(rows) - 1
		}
	}
	return rows, leafPos
}

func sessionTreeWindow(rows []sessionTreeRow, leafPos []int, sel, rowsCap int) []sessionTreeRow {
	n := len(rows)
	if rowsCap <= 0 {
		return nil
	}
	if rowsCap >= n {
		return rows
	}
	selPos := -1
	if sel >= 0 && sel < len(leafPos) {
		selPos = leafPos[sel]
	}
	start := 0
	if selPos >= 0 {
		start = selPos - rowsCap/2
		if start < 0 {
			start = 0
		}
		if start > n-rowsCap {
			start = n - rowsCap
		}
	}
	// Preserve the parent context: snap the window up to the group's
	// headers when it opens mid-group and at least one leaf still fits.
	if rows[start].kind == sessionRowLeaf && rows[start].parent >= 0 && rows[start].parent < start {
		parent := rows[start].parent
		ctxLen := 1
		if parent >= 1 && rows[parent-1].kind == sessionRowMachine {
			ctxLen = 2
		}
		if rowsCap-ctxLen >= 1 {
			start = parent - (ctxLen - 1)
		}
	}
	for i := 0; i < 2 && selPos >= 0 && start+rowsCap <= selPos; i++ {
		start = selPos - rowsCap + 1
		if start < 0 {
			start = 0
		}
		if start > n-rowsCap {
			start = n - rowsCap
		}
	}
	end := start + rowsCap
	if end > n {
		end = n
	}
	return rows[start:end]
}

// sessionTopRows is the reserved top of a frame of height h: the meta
// header (needs four rows so a row and the footer still fit) and the
// search row (needs two).
func sessionTopRows(height int) int {
	top := 0
	if height >= 4 {
		top++
	}
	if height >= 2 {
		top++
	}
	return top
}

// sessionFooterRows is the reserved footer block for a frame of height
// h: Esc Close always, the other two lines when the middle can spare
// them.
func sessionFooterRows(height int) int {
	middle := height - sessionTopRows(height)
	f := 1
	if middle >= 4 {
		f = 2
	}
	if middle >= 6 {
		f = 3
	}
	return f
}

// sessionFrameMiddle is the shared raw budget of one frame of width×
// height: the middle rows left after the top chrome, the control footer
// and the fixed status strip, and whether the selected-metadata line is
// drawable there. The frame caps and the detail dividers both derive
// from it, so the page keys, the row window and the dividers always
// agree on the reserved rows.
func sessionFrameMiddle(o *SessionRenderOptions, width, height int) (cap int, detailOn bool) {
	o.normalize()
	if width <= 0 || height <= 0 {
		return 0, false
	}
	pad := 0
	if width >= 6 {
		pad = 1
	}
	avail := width - pad
	// The same styled line the renderer draws: a styled empty detail is
	// still a non-empty line, so the slot is reserved with that rule.
	detail := sessionStyleDim + sessionDetailLine(o, avail) + sessionStyleReset
	if pad > 0 {
		detail = strings.Repeat(" ", pad) + detail
	}
	top := sessionTopRows(height)
	middle := height - top
	if middle < 0 {
		middle = 0
	}
	f := sessionFooterRows(height)
	strip := sessionStatusStripRows(height)
	cap = middle - f - strip
	if cap < 0 {
		cap = 0
	}
	if len(o.Entries) <= 0 || cap < 1 {
		return 0, false
	}
	return cap, o.selectedEntry() != nil && detail != ""
}

// sessionFrameCaps is the shared viewport math of one frame of width×
// height: the row-window capacity (rowsCap) and whether the
// selected-metadata row is reserved above the footer (detailSlot). The
// renderer and the page keys both consume it, so a page always matches
// the selectable rows the renderer fits in its window; the feedback,
// detail (with its dividers, sessionDetailDividers) and fixed status-
// strip reservations leave the window at least one row with entries, so
// a tiny viewport still shows one item.
func sessionFrameCaps(o SessionRenderOptions, width, height int) (rowsCap int, detailSlot bool) {
	o.normalize()
	cap, detailOn := sessionFrameMiddle(&o, width, height)
	if cap < 1 {
		return 0, false
	}
	core := sessionStatusCore(&o)
	detailSlot = detailOn && cap >= 2
	// The dividers around the reserved detail count in the same budget:
	// at ordinary sizes both fit and reserve two rows with the detail;
	// on a tiny height the optional dividers drop first so the content
	// (the window and the detail row) keeps its room.
	detailDividers := sessionDetailDividers(&o, width, height)
	statusCap := len(core)
	if detailSlot {
		if statusCap > cap-2-detailDividers {
			statusCap = cap - 2 - detailDividers
		}
	} else {
		if statusCap > cap-1 {
			statusCap = cap - 1
		}
	}
	rowsCap = cap - statusCap
	if detailSlot {
		rowsCap--
		rowsCap -= detailDividers
	}
	return rowsCap, detailSlot
}

// sessionDetailDividers is the number of theme-neutral dividers reserved
// around the selected-metadata row for one frame of width×height: 2 at
// ordinary sizes when the diagnostic core fits, 0 when they must yield
// to content. It is the same budget term sessionFrameCaps subtracts
// from the row window, so the renderer and the page keys agree.
func sessionDetailDividers(o *SessionRenderOptions, width, height int) int {
	o.normalize()
	cap, detailOn := sessionFrameMiddle(o, width, height)
	// Optional chrome yields before diagnostic text when the viewport
	// cannot fit the core, one selectable row and the metadata row.
	if detailOn && cap >= 4 && len(sessionStatusCore(o)) <= cap-4 {
		return 2
	}
	return 0
}

// sessionSelectableWindow counts the selectable entries the renderer fits
// in the row window of one frame of width×height: the table's data rows
// (its fixed column header consumes one window row, and the tiny window
// keeps one row) or the tree's leaf rows (the group headings consume
// their rows from the window). It is the shared viewport math the page
// keys move the selection by, clamped by the caller.
func sessionSelectableWindow(o SessionRenderOptions, width, height int) int {
	rowsCap, _ := sessionFrameCaps(o, width, height)
	n := len(o.Entries)
	if rowsCap <= 0 || n == 0 {
		return 0
	}
	if o.View == "tree" {
		pad := 0
		if width >= 6 {
			pad = 1
		}
		tree, leafPos := sessionTreeNodes(o.Entries, width-pad)
		window := sessionTreeWindow(tree, leafPos, o.Selected, rowsCap)
		leaves := 0
		for _, row := range window {
			if row.kind == sessionRowLeaf {
				leaves++
			}
		}
		return leaves
	}
	if rowsCap <= 1 {
		return 1
	}
	page := rowsCap - 1
	if page > n {
		page = n
	}
	return page
}

// renderSessionPopup renders one frame of the shared session popup for
// the picker and the board: the meta header, the search row, a dim
// separator, the row window (table or tree), the bottom-anchored status
// core and the selected-metadata block (the metadata row between two
// theme-neutral dividers at ordinary sizes), the control footer and -
// below the footer - the fixed status strip (the divider and the
// STATUS/notice line, reserved so copying never moves a row). The host
// viewport is consumed as supplied (the popup is already 80% of the
// terminal, so it is never multiplied again), every line is cut to width
// display columns and the frame is exactly height lines, so the bottom
// block always sits on the last rows even with few results.
func renderSessionPopup(o SessionRenderOptions, width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	if o.TeamView != nil {
		// The picker's read-only team view replaces the row window inside
		// the same frame; the list is drawn by the
		// caller only while the view is closed.
		return renderPickerTeamFrame(&o, width, height)
	}
	o.normalize()
	pad := 0
	if width >= 6 {
		pad = 1
	}
	avail := width - pad
	lineAt := func(content string) string {
		if displayWidth(content) > avail {
			content = displaySlice(content, avail)
		}
		if pad > 0 {
			content = strings.Repeat(" ", pad) + content
		}
		return content
	}
	styled := func(style, visible string) string {
		if displayWidth(visible) > avail {
			visible = displaySlice(visible, avail)
		}
		if pad > 0 {
			return strings.Repeat(" ", pad) + style + visible + sessionStyleReset
		}
		return style + visible + sessionStyleReset
	}
	blank := strings.Repeat(" ", pad)
	separator := styled(sessionStyleDim, strings.Repeat("─", avail))

	header := styled(sessionStyleDim, sessionHeaderLine(&o))
	searchVisible, searchPlaceholder := sessionSearchParts(&o)
	var search string
	if o.SearchFocused {
		search = blank + sessionHighlightBand(searchVisible, avail)
	} else if searchPlaceholder {
		search = styled(sessionStyleDim, searchVisible)
	} else {
		search = lineAt(searchVisible)
	}
	footerLines := make([]string, 0, 3)
	for _, seg := range sessionFooterSegments(&o, avail) {
		footerLines = append(footerLines, lineAt(modalFooterLine(seg, avail)))
	}
	core := sessionStatusCore(&o)
	detail := styled(sessionStyleDim, sessionDetailLine(&o, avail))

	lines := make([]string, 0, height)
	top := sessionTopRows(height)
	if top >= 2 {
		lines = append(lines, header)
	}
	if height >= 2 {
		lines = append(lines, search)
	}
	middle := height - top
	if middle < 0 {
		middle = 0
	}
	f := sessionFooterRows(height)
	strip := sessionStatusStripRows(height)
	cap := middle - f - strip
	if cap < 0 {
		cap = 0
	}
	n := len(o.Entries)
	statusCap := 0
	sep := false
	// The shared frame math (sessionFrameCaps) gives the row-window
	// capacity and the reserved detail slot, so the page keys move by
	// exactly what the renderer fits.
	rowsCap, detailSlot := sessionFrameCaps(o, width, height)
	detailDividers := sessionDetailDividers(&o, width, height)
	if n > 0 {
		if cap >= 1 {
			// The selected-metadata row is reserved before the rows
			// fill the window, so it does not disappear merely because
			// a large registry fills the viewport; it anchors directly
			// above the footer. At tiny sizes the list keeps at least
			// one leaf, then the feedback, then the detail.
			if detailSlot {
				statusCap = len(core)
				if statusCap > cap-2-detailDividers {
					statusCap = cap - 2 - detailDividers
				}
			} else {
				statusCap = len(core)
				if statusCap > cap-1 {
					statusCap = cap - 1
				}
			}
			shown := sessionRowsWindow(&o, avail, rowsCap)
			free := rowsCap - len(shown)
			if free > 0 {
				sep = true
				free--
			}
			if sep {
				lines = append(lines, separator)
			}
			for _, line := range shown {
				lines = append(lines, blank+line)
			}
			for i := 0; i < free; i++ {
				lines = append(lines, blank)
			}
		}
	} else {
		statusCap = len(core)
		if statusCap > cap {
			statusCap = cap
		}
		free := cap - statusCap
		if free > 0 {
			sep = true
			free--
			lines = append(lines, separator)
		}
		for i := 0; i < free; i++ {
			lines = append(lines, blank)
		}
	}
	status := core[len(core)-statusCap:]
	for _, line := range status {
		lines = append(lines, lineAt(line))
	}
	if detailSlot {
		// The selected-metadata block: the metadata row, drawn exactly
		// once, between the two theme-neutral dividers at ordinary sizes
		// (the dividers degrade before content on tiny heights).
		if detailDividers >= 2 {
			lines = append(lines, separator)
		}
		lines = append(lines, detail)
		if detailDividers >= 2 {
			lines = append(lines, separator)
		}
	}
	lines = append(lines, footerLines[len(footerLines)-f:]...)
	// The fixed status strip below the shortcuts: the theme-neutral
	// divider and the one-line status (the dim STATUS label with the
	// Ready/notice content, the confirmed copy in bold), reserved by the
	// shared frame math so the strip's row count and position never
	// change when copying.
	if strip >= 2 {
		lines = append(lines, separator)
	}
	if strip >= 1 {
		lines = append(lines, sessionStatusStripLine(&o, avail, pad))
	}
	return strings.Join(lines, "\n") + "\n"
}

// sessionRowsWindow returns the rowsCap rows of the current view around
// the selected entry: the table window is centered on the selection, the
// tree window keeps the selected leaf visible and preserves its parent
// context when space permits.
func sessionRowsWindow(o *SessionRenderOptions, avail, rowsCap int) []string {
	if rowsCap <= 0 {
		return nil
	}
	if o.View == "tree" {
		tree, leafPos := sessionTreeNodes(o.Entries, avail)
		window := sessionTreeWindow(tree, leafPos, o.Selected, rowsCap)
		lines := make([]string, 0, len(window))
		for _, row := range window {
			line := row.text
			if row.kind == sessionRowLeaf && row.entry == o.Selected {
				line = sessionHighlightBand(line, avail)
			}
			lines = append(lines, line)
		}
		return lines
	}
	all := sessionTableLines(o.Entries, o.Selected, avail)
	if len(all) == 0 {
		return nil
	}
	header, data := all[0], all[1:]
	if rowsCap <= 1 {
		// Single row: at tiny sizes the selected entry (one leaf) wins
		// over the fixed column header.
		if len(data) == 0 {
			return nil
		}
		if o.Selected >= 0 && o.Selected < len(data) {
			return data[o.Selected : o.Selected+1]
		}
		return data[0:1]
	}
	if rowsCap >= len(all) {
		return all
	}
	dataCap := rowsCap - 1
	start := 0
	if o.Selected >= 0 {
		start = o.Selected - dataCap/2
		if start < 0 {
			start = 0
		}
		if start > len(data)-dataCap {
			start = len(data) - dataCap
		}
	}
	window := data[start : start+dataCap]
	lines := make([]string, 0, 1+len(window))
	lines = append(lines, header)
	lines = append(lines, window...)
	return lines
}

// pickerTeamViewOptions builds the picker's frame options with the
// read-only team view attached: the same unified
// session options the picker's list frame uses, with the "i Team"
// shortcut disclosed in the footer and the open view attached so
// renderSessionPopup draws it in the row window's place.
func pickerTeamViewOptions(state *PickerState, notice string) SessionRenderOptions {
	o := pickerEnhancedOptions(state, notice)
	o.TeamKey = true
	o.TeamView = state.TeamView
	return o
}

// pickerTeamTitle is the view's frame title: "TEAM  <workspace> ·
// <machine>", sanitized like every display value; an empty workspace
// label reads "-" (the modal fields' convention).
func pickerTeamTitle(v *pickerTeamView) string {
	label := StripPickerControls(v.workspaceLabel)
	if label == "" {
		label = "-"
	}
	return fmt.Sprintf("TEAM  %s · %s", label, StripPickerControls(v.machine))
}

// pickerTeamContent is the team view's unscrolled content at avail
// display columns: the title, and - for a valid local target - the
// loaded data in the open form (the tree by default, the table after
// the v switch). A failed or undecodable read shows one "<name>: <cause>"
// line in its own block and the other block keeps its content. A remote
// row shows the remote-machine message and an invalid cwd its cause.
// Every line is cut to the width, never wrapped.
func pickerTeamContent(v *pickerTeamView, avail int) []string {
	if v == nil {
		return nil
	}
	title := teamViewLine(avail, teamViewSeg{sessionStyleBold, pickerTeamTitle(v)})
	if v.remote {
		return []string{title, teamCut("team view: not available yet for remote machines", avail)}
	}
	if v.targetErr != "" {
		return []string{title, teamCut(StripPickerControls(v.targetErr), avail)}
	}
	if !v.loaded {
		return []string{title, teamCut("loading team…", avail)}
	}
	if teamViewFormOf(v.form) == teamFormTable {
		return teamViewTable(v, avail)
	}
	return teamViewTree(v, avail)
}

// renderPickerTeamFrame draws the picker's read-only team view inside
// the picker's own modal frame: the frame chrome is the picker's - the
// meta header (the view's, "Team · Tree" or "Team · Table", in dim), the
// blank search row (the frame's height does not change) and the footer
// with the view's three segments (the list's line count, the list's cut
// when the height shrinks) - and the row window (the list area) is
// replaced by the view. The lines are cut to the width, the whole
// content scrolls with scrollLines (the arrow and page keys) and the
// frame stays exactly height lines, like the popup.
func renderPickerTeamFrame(o *SessionRenderOptions, width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	o.normalize()
	v := o.TeamView
	pad := 0
	if width >= 6 {
		pad = 1
	}
	avail := width - pad
	lineAt := func(content string) string {
		if displayWidth(content) > avail {
			content = displaySlice(content, avail)
		}
		if pad > 0 {
			content = strings.Repeat(" ", pad) + content
		}
		return content
	}
	styled := func(style, visible string) string {
		if displayWidth(visible) > avail {
			visible = displaySlice(visible, avail)
		}
		if pad > 0 {
			return strings.Repeat(" ", pad) + style + visible + sessionStyleReset
		}
		return style + visible + sessionStyleReset
	}
	blank := strings.Repeat(" ", pad)
	separator := styled(sessionStyleDim, strings.Repeat("─", avail))

	headerText := sessionHeaderLine(o)
	var search string
	searchVisible, searchPlaceholder := sessionSearchParts(o)
	if v != nil {
		// The view replaces the list chrome: its own header (the open
		// form, in dim) and the blank search row (the frame's height
		// does not change).
		form := teamViewFormOf(v.form)
		headerText = "Team · Tree"
		if form == teamFormTable {
			headerText = "Team · Table"
		}
		search = blank
	}
	header := styled(sessionStyleDim, headerText)
	if v == nil {
		if o.SearchFocused {
			search = blank + sessionHighlightBand(searchVisible, avail)
		} else if searchPlaceholder {
			search = styled(sessionStyleDim, searchVisible)
		} else {
			search = lineAt(searchVisible)
		}
	}
	footerSegs := sessionFooterSegments(o, avail)
	if v != nil {
		// The view's own control guidance: the list's footer keeps its
		// line count, and the cut (the last lines win) is the list's.
		footerSegs = teamViewFooterSegments(teamViewFormOf(v.form))
	}
	footerLines := make([]string, 0, 3)
	for _, seg := range footerSegs {
		footerLines = append(footerLines, lineAt(modalFooterLine(seg, avail)))
	}

	content := scrollLines(pickerTeamContent(v, avail), v.scroll)

	lines := make([]string, 0, height)
	top := sessionTopRows(height)
	if top >= 2 {
		lines = append(lines, header)
	}
	if height >= 2 {
		lines = append(lines, search)
	}
	middle := height - top
	if middle < 0 {
		middle = 0
	}
	f := sessionFooterRows(height)
	strip := sessionStatusStripRows(height)
	cap := middle - f - strip
	if cap < 0 {
		cap = 0
	}
	// The view takes the whole row window: one dim separator when there is
	// room, then the content from the top (the scroll offset already
	// applied by scrollLines) and blank rows filling the rest, so the
	// frame stays exactly height lines, like the popup.
	windowCap := cap
	if cap >= 2 {
		lines = append(lines, separator)
		windowCap = cap - 1
	}
	shown := content
	if len(shown) > windowCap {
		shown = shown[:windowCap]
	}
	for _, line := range shown {
		lines = append(lines, blank+line)
	}
	for i := len(shown); i < windowCap; i++ {
		lines = append(lines, blank)
	}
	lines = append(lines, footerLines[len(footerLines)-f:]...)
	if strip >= 2 {
		lines = append(lines, separator)
	}
	if strip >= 1 {
		lines = append(lines, sessionStatusStripLine(o, avail, pad))
	}
	return strings.Join(lines, "\n") + "\n"
}
