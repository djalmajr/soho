package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// Fixture and assertion helpers for the shared session popup renderer.
// The renderer is private, so the tests live in the package.

// sessionTestEntries builds a spread fixture: three machines (local
// first), four workspace labels (two of them equally named with distinct
// IDs, plus an ID-less local one) and rotating statuses.
func sessionTestEntries(n int) []PickerEntry {
	machines := []string{"local", "remote-a", "remote-b"}
	workspaces := []struct{ label, id string }{
		{"alpha", ""},
		{"beta", "ws-777"},
		{"shared", "id-1"},
		{"shared", "id-2"},
	}
	statuses := []string{"working", "idle", "blocked", "done"}
	entries := make([]PickerEntry, 0, n)
	for i := 1; i <= n; i++ {
		m := machines[(i-1)%3]
		w := workspaces[(i-1)%4]
		entries = append(entries, PickerEntry{
			"ref":             fmt.Sprintf("ref-%02d", i),
			"name":            fmt.Sprintf("agent-%02d", i),
			"kind":            "claude",
			"status":          statuses[(i-1)%4],
			"workspace_label": w.label,
			"workspace_id":    w.id,
			"tab_label":       "main",
			"cwd":             fmt.Sprintf("/tmp/proj-%02d", i),
			"machine":         m,
			"title":           fmt.Sprintf("planner: task %02d", i),
		})
	}
	return entries
}

// stripSessionSGR removes the renderer's own style escapes (bold, dim,
// reverse, reset) so the visible cells of a line can be measured.
func stripSessionSGR(line string) string {
	for _, seq := range []string{sessionStyleBold, sessionStyleDim, modalHighlightOn, modalHighlightOff} {
		line = strings.ReplaceAll(line, seq, "")
	}
	return line
}

// sessionEscapesAllowed fails the test when a line carries any escape
// byte other than the renderer's own style set: no OSC, CSI or raw
// control escape may leak from external fields.
func sessionEscapesAllowed(t *testing.T, line string) {
	t.Helper()
	allowed := map[string]bool{sessionStyleBold: true, sessionStyleDim: true, modalHighlightOn: true, modalHighlightOff: true}
	for i := 0; i < len(line); i++ {
		if line[i] != 0x1b {
			continue
		}
		matched := false
		for seq := range allowed {
			if strings.HasPrefix(line[i:], seq) {
				i += len(seq) - 1
				matched = true
				break
			}
		}
		if !matched {
			t.Fatalf("unexpected escape sequence in session popup line %q", line)
		}
	}
}

// assertSessionBounded checks the whole contract on one frame: trailing
// newline, at most height logical lines, every line at most width
// display columns, valid UTF-8, no CR and no escape leak. It returns
// the lines.
func assertSessionBounded(t *testing.T, out string, width, height int) []string {
	t.Helper()
	if out == "" {
		t.Fatal("empty session popup output")
	}
	if !strings.HasSuffix(out, "\n") {
		t.Fatalf("missing trailing newline: %q", out)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) > height {
		t.Fatalf("frame has %d logical lines, viewport height is %d:\n%s", len(lines), height, out)
	}
	if !utf8.ValidString(out) {
		t.Fatalf("frame is not valid UTF-8: %q", out)
	}
	for _, line := range lines {
		visible := stripSessionSGR(line)
		if strings.ContainsRune(visible, '\n') || strings.ContainsRune(visible, '\r') {
			t.Fatalf("line wraps or carries a carriage return: %q", line)
		}
		if w := displayWidth(visible); w > width {
			t.Fatalf("line exceeds the viewport width %d (visible %d): %q", width, w, line)
		}
		sessionEscapesAllowed(t, line)
	}
	return lines
}

// sessionHighlightedLines returns the lines wrapped in the selection
// highlight; the popup must never highlight more than one row.
func sessionHighlightedLines(t *testing.T, lines []string) []string {
	t.Helper()
	var out []string
	for _, line := range lines {
		if strings.Contains(line, modalHighlightOn) {
			out = append(out, line)
		}
	}
	return out
}

// renderSessionTestFrame tests presentation with the supplied row order.
// Input sorting has separate tests; wrapper integration is checked below.
func renderSessionTestFrame(entries []PickerEntry, sel, width, height int, board, tree bool, query string) string {
	o := SessionRenderOptions{Entries: FilterPickerEntries(entries, query), Total: len(entries), Selected: sel, Query: query, Noun: "pane", Board: board}
	if tree {
		o.View = "tree"
	}
	if board {
		o.Entries = filterPickerEntries(boardAgents(entries), query, boardFilterFields)
		o.Noun = "agent"
		o.Totals = boardTotals(boardAgents(entries), "")
	}
	return renderSessionPopup(o, width, height)
}

func TestSessionRenderSortedWrapperSelection(t *testing.T) {
	for _, tree := range []bool{false, true} {
		p, b := NewPickerState(), NewBoardState()
		p.Entries, b.Entries = sessionViewEntries(), sessionViewEntries()
		p.ViewState.Enabled, b.ViewState.Enabled = true, true
		p.Selected, b.Selected = 3, 3 // local/w6:p6 in the grouped pane order
		if tree {
			p.ViewState.View, b.ViewState.View = "tree", "tree"
		}
		for name, out := range map[string]string{"picker": renderPickerModal(p, 90, 20, ""), "board": renderBoardModal(b, 90, 20, "")} {
			lines := assertSessionBounded(t, out, 90, 20)
			highlighted := sessionHighlightedLines(t, lines)
			if len(highlighted) != 1 || !strings.Contains(stripSessionSGR(highlighted[0]), "alpha-six") || !strings.Contains(out, "local/w6:p6") {
				t.Fatalf("%s tree=%v: selection does not follow the grouped index:\n%s", name, tree, out)
			}
		}
	}
}

func TestSessionRenderLongTreePaneKeepsStatus(t *testing.T) {
	entry := sessionTestEntries(1)[0]
	entry["name"] = strings.Repeat("部署e\u0301", 30)
	entry["status"] = "blocked"
	for _, width := range []int{24, 40, 60} {
		out := renderSessionTestFrame([]PickerEntry{entry}, 0, width, 14, false, true, "")
		lines := assertSessionBounded(t, out, width, 14)
		highlighted := sessionHighlightedLines(t, lines)
		if len(highlighted) != 1 || !strings.Contains(stripSessionSGR(highlighted[0]), "… · blocked") {
			t.Fatalf("width=%d: a long pane hid the tree status:\n%s", width, out)
		}
	}
}

func TestSessionRenderTreePaneIDsAtEnd(t *testing.T) {
	entries := []PickerEntry{
		{"name": "build-capacity-alerts", "status": "working", "pane_id": "w12:p3C", "ref": "local/w12:p3C", "machine": "local", "workspace_id": "w12", "workspace_label": "appliance"},
		{"name": "review", "status": "idle", "ref": "windows/w3:pX", "machine": "windows", "workspace_id": "w3", "workspace_label": "appliance"},
		{"name": "shell", "status": "unknown", "pane_id": "w14:p\x1b[31m81", "machine": "local", "workspace_id": "w14"},
	}
	for _, width := range []int{24, 40, 100} {
		for selected, want := range []string{"(w12:p3C)", "(w3:pX)", "(w14:p81)"} {
			out := renderSessionTestFrame(entries, selected, width, 16, false, true, "")
			lines := assertSessionBounded(t, out, width, 16)
			highlighted := sessionHighlightedLines(t, lines)
			if len(highlighted) != 1 || !strings.HasSuffix(strings.TrimSpace(stripSessionSGR(highlighted[0])), want) {
				t.Fatalf("width=%d selection=%d: pane ID missing from row end:\n%s", width, selected, out)
			}
			if width == 100 && selected == 0 && !strings.Contains(stripSessionSGR(highlighted[0]), "build-capacity-alerts · working (w12:p3C)") {
				t.Fatalf("wide row lost pane name or status: %q", highlighted[0])
			}
		}
	}
	entry := entries[0]
	entry["name"] = strings.Repeat("部署e\u0301", 30)
	out := renderSessionTestFrame([]PickerEntry{entry}, 0, 40, 14, false, true, "")
	lines := assertSessionBounded(t, out, 40, 14)
	selected := strings.TrimSpace(stripSessionSGR(sessionHighlightedLines(t, lines)[0]))
	if !strings.HasSuffix(selected, "… · working (w12:p3C)") {
		t.Fatalf("a long Unicode name hid status or identity: %q", selected)
	}
}

func TestSessionRenderLongQueryStaysBoundedAfterTab(t *testing.T) {
	for _, focused := range []bool{false, true} {
		for _, width := range []int{24, 60} {
			o := SessionRenderOptions{Entries: sessionTestEntries(1), Noun: "pane", Query: strings.Repeat("部署e\u0301", 50), SearchFocused: focused}
			out := renderSessionPopup(o, width, 14)
			assertSessionBounded(t, out, width, 14)
			if !strings.Contains(stripSessionSGR(out), "> 部署") {
				t.Fatalf("focused=%v width=%d: query disappeared:\n%s", focused, width, out)
			}
		}
	}
}

func TestSessionRenderNarrowBoardKeepsLoadingState(t *testing.T) {
	o := SessionRenderOptions{Entries: sessionTestEntries(1), Noun: "agent", Board: true, Loading: 1, Totals: "100000 agents · 10000 working · 10000 idle · 10000 blocked · 10000 done"}
	for _, local := range []bool{false, true} {
		o.LoadingLocal = local
		out := renderSessionPopup(o, 60, 14)
		lines := assertSessionBounded(t, out, 60, 14)
		state := "loading remote…"
		if local {
			state = "loading local…"
		}
		if !strings.Contains(stripSessionSGR(lines[0]), state) {
			t.Fatalf("loading state hidden by board totals:\n%s", out)
		}
	}
}

func TestSessionRenderMachineDisplayDoesNotMergeIdentities(t *testing.T) {
	entries := sessionTestEntries(2)
	for i := range entries {
		entries[i]["machine"] = "local"
		entries[i]["workspace_id"] = "shared"
		entries[i]["workspace_label"] = "project"
	}
	entries[1]["machine"] = "local\x1b[31m"
	out := renderSessionTestFrame(entries, 0, 90, 20, false, true, "")
	assertSessionBounded(t, out, 90, 20)
	if got := strings.Count(stripSessionSGR(out), "local (1)"); got != 2 {
		t.Fatalf("different raw machine identities merged: headers=%d:\n%s", got, out)
	}
}

func TestSessionRenderTableDistinguishesWorkspaceLabelCollisions(t *testing.T) {
	entries := sessionTestEntries(4)
	for i, entry := range entries {
		entry["machine"], entry["workspace_label"] = "local", "shared"
		entry["workspace_id"] = "w1"
		if i == 1 {
			entry["workspace_id"] = "w2\x1b[31m"
		}
		if i == 3 {
			entry["machine"], entry["workspace_id"] = "remote", "w3"
		}
	}
	out := renderSessionTestFrame(entries, 1, 90, 20, false, false, "")
	lines := assertSessionBounded(t, out, 90, 20)
	plain := stripSessionSGR(out)
	if strings.Count(plain, "shared · w1") != 2 || strings.Count(plain, "shared · w2") != 1 || strings.Contains(plain, "shared · w3") {
		t.Fatalf("same-machine labels not disambiguated, or remote-only label was changed:\n%s", out)
	}
	highlighted := sessionHighlightedLines(t, lines)
	if len(highlighted) != 1 || !strings.Contains(stripSessionSGR(highlighted[0]), "shared · w2") {
		t.Fatalf("disambiguation moved the selected pane:\n%s", out)
	}
}

func TestSessionRenderTableAlignedColumnsAndOrder(t *testing.T) {
	entries := sessionTestEntries(8)
	out := renderSessionTestFrame(entries, 2, 90, 20, false, false, "")
	lines := assertSessionBounded(t, out, 90, 20)
	// Column geometry: pad(1) + marker(2) + machine(8) + 2 +
	// workspace(13, including colliding workspace IDs) + 2 + pane(8) + 2 +
	// id(2) + 2 + agent(6) + 2 + status(7).
	const (
		wsCol     = 13
		paneCol   = 28
		idCol     = 38
		agentCol  = 42
		statusCol = 50
	)
	foundRows := 0
	for _, line := range lines {
		if strings.Contains(line, modalHighlightOn) || !strings.Contains(line, "agent-") {
			continue // skip chrome, blanks, detail, the header and the selected band
		}
		visible := stripSessionSGR(line)
		// Column geometry: pad(1) + marker(2) + machine(8) + 2 +
		// workspace(13) + 2 + pane(8) + 2 + id(2) + 2 + agent(6) + 2 +
		// status(7). The cell padding never doubles a space, so the column gaps are the
		// only "  " runs at these offsets, which pins the alignment of
		// every column.
		runes := []rune(visible) // all fixture runes, including the middle dot, have width 1
		if string(runes[11:13]) != "  " || string(runes[26:28]) != "  " || string(runes[36:38]) != "  " || string(runes[40:42]) != "  " || string(runes[48:50]) != "  " {
			t.Fatalf("columns not aligned: %q", visible)
		}
		// Broad-to-specific order: machine < workspace < pane < agent <
		// status, at the pinned offsets.
		if i := strings.Index(visible, "agent-"); i < 0 || displayWidth(visible[:i]) != paneCol {
			t.Fatalf("pane column not at %d: %q", paneCol, visible)
		}
		if i := strings.Index(visible, "claude"); i < 0 || displayWidth(visible[:i]) != agentCol {
			t.Fatalf("agent column not at %d: %q", agentCol, visible)
		}
		statusText := strings.TrimRight(string(runes[statusCol:]), " ")
		if statusText == "" || strings.Contains(statusText, " ") {
			t.Fatalf("status column not at %d: %q", statusCol, visible)
		}
		foundRows++
	}
	if foundRows < 3 {
		t.Fatalf("expected at least 3 data rows to check, got %d:\n%s", foundRows, out)
	}
	// The fixed column header row is visible and aligned with the data
	// columns.
	foundHeader := false
	for _, line := range lines {
		visible := stripSessionSGR(line)
		if !strings.Contains(visible, "Machine") || !strings.Contains(visible, "Status") {
			continue
		}
		foundHeader = true
		if strings.Contains(line, modalHighlightOn) {
			t.Fatalf("the column header must not be banded: %q", line)
		}
		for word, col := range map[string]int{"Machine": 3, "Workspace": wsCol, "Pane": paneCol, "ID": idCol, "Agent": agentCol, "Status": statusCol} {
			if i := strings.Index(visible, word); i != col {
				t.Fatalf("header word %q not at column %d: %q", word, col, visible)
			}
		}
	}
	if !foundHeader {
		t.Fatalf("column header row missing:\n%s", out)
	}
	// The selected row is a full-width band, carries the ">" marker, and
	// is the only highlighted one.
	highlighted := sessionHighlightedLines(t, lines)
	if len(highlighted) != 1 {
		t.Fatalf("expected exactly one highlighted row, got %d:\n%s", len(highlighted), out)
	}
	if !strings.Contains(stripSessionSGR(highlighted[0]), "agent-03") {
		t.Fatalf("selected pane not in the highlighted row: %q", highlighted[0])
	}
	if !strings.HasPrefix(strings.TrimLeft(stripSessionSGR(highlighted[0]), " "), "> ") {
		t.Fatalf("selected band missing the > marker: %q", highlighted[0])
	}
	if w := displayWidth(stripSessionSGR(highlighted[0])); w != 90 {
		t.Fatalf("selection band is not full width: %d columns, want 90: %q", w, highlighted[0])
	}
	// The detail line carries the full ref, tab and cwd of the selection.
	foundDetail := false
	for _, line := range lines {
		if strings.TrimSpace(stripSessionSGR(line)) == "ref-03 · main · /tmp/proj-03" {
			foundDetail = true
		}
	}
	if !foundDetail {
		t.Fatalf("detail line missing:\n%s", out)
	}
}

func TestSessionRenderHeaderSortArrowAndCounts(t *testing.T) {
	entries := sessionTestEntries(8)
	t.Run("default pane ascending", func(t *testing.T) {
		out := renderSessionTestFrame(entries, 0, 80, 10, false, false, "")
		lines := assertSessionBounded(t, out, 80, 10)
		header := stripSessionSGR(lines[0])
		if !strings.Contains(header, "Table · sort Pane ↑ · 8 panes") {
			t.Fatalf("header missing the view, sort arrow and count: %q", header)
		}
	})
	t.Run("descending and custom field", func(t *testing.T) {
		state := NewPickerState()
		state.Entries = entries
		state.ViewState.Enabled = true
		state.ViewState.Sort = "workspace"
		state.ViewState.Descending = true
		out := renderPickerModal(state, 80, 10, "")
		lines := assertSessionBounded(t, out, 80, 10)
		if !strings.Contains(stripSessionSGR(lines[0]), "sort Workspace ↓") {
			t.Fatalf("header missing the descending sort: %q", lines[0])
		}
	})
	t.Run("singular count", func(t *testing.T) {
		out := renderSessionTestFrame(entries[:1], 0, 80, 10, false, false, "")
		lines := assertSessionBounded(t, out, 80, 10)
		if !strings.Contains(stripSessionSGR(lines[0]), "1 pane") || strings.Contains(stripSessionSGR(lines[0]), "1 panes") {
			t.Fatalf("singular count wrong: %q", lines[0])
		}
	})
	t.Run("board totals and update time", func(t *testing.T) {
		state := NewBoardState()
		state.Entries = entries
		state.ViewState.Enabled = true
		state.UpdatedAt = "12:00:00"
		out := renderBoardModal(state, 120, 10, "")
		lines := assertSessionBounded(t, out, 120, 10)
		header := stripSessionSGR(lines[0])
		if !strings.Contains(header, "Table · sort Pane ↑ · 8 agents") || !strings.Contains(header, "12:00:00") {
			t.Fatalf("board header missing totals and update time: %q", header)
		}
	})
	t.Run("loading state in the header", func(t *testing.T) {
		state := NewPickerState()
		state.Entries = entries
		state.ViewState.Enabled = true
		state.Loading, state.LoadingLocal = 1, true
		out := renderPickerModal(state, 100, 10, "")
		if !strings.Contains(stripSessionSGR(strings.Split(out, "\n")[0]), "loading local…") {
			t.Fatalf("local loading state missing from the header:\n%s", out)
		}
		state.LoadingLocal = false
		state.Loading = 2
		out = renderPickerModal(state, 100, 10, "")
		if !strings.Contains(stripSessionSGR(strings.Split(out, "\n")[0]), "loading remote…") {
			t.Fatalf("remote loading state missing from the header:\n%s", out)
		}
	})
}

func TestSessionRenderFullWidthHighlightBothViews(t *testing.T) {
	entries := sessionTestEntries(12)
	for _, tree := range []bool{false, true} {
		for _, width := range []int{24, 60, 110} {
			out := renderSessionTestFrame(entries, 5, width, 16, false, tree, "")
			lines := assertSessionBounded(t, out, width, 16)
			highlighted := sessionHighlightedLines(t, lines)
			if len(highlighted) != 1 {
				t.Fatalf("tree=%v width=%d: expected one highlighted row, got %d:\n%s", tree, width, len(highlighted), out)
			}
			if w := displayWidth(stripSessionSGR(highlighted[0])); w != width {
				t.Fatalf("tree=%v width=%d: band is %d columns, want %d: %q", tree, width, w, width, highlighted[0])
			}
			if width >= 60 && !strings.Contains(stripSessionSGR(highlighted[0]), "agent-06") {
				t.Fatalf("tree=%v width=%d: selected leaf missing from the band: %q", tree, width, highlighted[0])
			}
			if !tree {
				// The table selection keeps the > marker inside the band.
				if !strings.HasPrefix(strings.TrimLeft(stripSessionSGR(highlighted[0]), " "), "> ") {
					t.Fatalf("tree=%v width=%d: table band missing the > marker: %q", tree, width, highlighted[0])
				}
			}
		}
	}
}

func TestSessionRenderTreeGroupingCountsAndDuplicateLabels(t *testing.T) {
	// Contiguous machine > workspace groups, as the input worker
	// delivers them.
	ws := []struct{ label, id string }{
		{"alpha", ""}, {"shared", "id-1"}, {"beta", "ws-777"},
		{"shared", "id-1"}, {"shared", "id-2"}, {"alpha", ""},
		{"shared", "id-2"}, {"beta", "ws-777"},
	}
	machine := []string{"local", "local", "local", "remote-a", "remote-a", "remote-a", "remote-b", "remote-b"}
	entries := make([]PickerEntry, 0, len(ws))
	for i := range ws {
		entries = append(entries, PickerEntry{
			"ref":             fmt.Sprintf("ref-%02d", i+1),
			"name":            fmt.Sprintf("agent-%02d", i+1),
			"kind":            "claude",
			"status":          "working",
			"workspace_label": ws[i].label,
			"workspace_id":    ws[i].id,
			"tab_label":       "main",
			"cwd":             fmt.Sprintf("/tmp/proj-%02d", i+1),
			"machine":         machine[i],
			"title":           fmt.Sprintf("planner: task %02d", i+1),
		})
	}
	out := renderSessionTestFrame(entries, 0, 90, 30, false, true, "")
	lines := assertSessionBounded(t, out, 90, 30)
	machineLines := []string{}
	workspaceLines := []string{}
	leafLines := 0
	for _, line := range lines {
		visible := stripSessionSGR(line)
		switch {
		case strings.Contains(line, sessionStyleBold):
			machineLines = append(machineLines, line)
		case strings.Contains(line, sessionStyleDim) && (strings.Contains(visible, "├─") || strings.Contains(visible, "└─")):
			workspaceLines = append(workspaceLines, line)
		case strings.Contains(visible, "├─") || strings.Contains(visible, "└─"):
			leafLines++
		}
	}
	if len(machineLines) != 3 {
		t.Fatalf("expected 3 machine headers, got %d:\n%s", len(machineLines), out)
	}
	if leafLines != 8 {
		t.Fatalf("expected 8 leaves, got %d:\n%s", leafLines, out)
	}
	// Machine counts reflect the filtered entries.
	if !strings.Contains(stripSessionSGR(machineLines[0]), "local (3)") ||
		!strings.Contains(stripSessionSGR(machineLines[1]), "remote-a (3)") ||
		!strings.Contains(stripSessionSGR(machineLines[2]), "remote-b (2)") {
		t.Fatalf("machine counts wrong:\n%s", strings.Join(machineLines, "\n"))
	}
	// The local machine is the first group in the incoming order.
	if !strings.Contains(stripSessionSGR(machineLines[0]), "local (3)") {
		t.Fatalf("local machine should be the first group: %q", machineLines[0])
	}
	// Equally named workspaces with distinct IDs stay separate groups,
	// the ID shown in the dim header.
	shared := []string{}
	for _, line := range workspaceLines {
		if strings.Contains(stripSessionSGR(line), "shared") {
			shared = append(shared, line)
		}
	}
	if len(shared) != 4 {
		t.Fatalf("expected 4 'shared' workspace groups, got %d:\n%s", len(shared), strings.Join(workspaceLines, "\n"))
	}
	if !strings.Contains(strings.Join(shared, "\n"), "shared · id-1") || !strings.Contains(strings.Join(shared, "\n"), "shared · id-2") {
		t.Fatalf("duplicate labels must be told apart by ID:\n%s", strings.Join(shared, "\n"))
	}
	// Workspace headers are dim, never bold, and carry a count.
	for _, line := range workspaceLines {
		if strings.Contains(line, sessionStyleBold) {
			t.Fatalf("workspace header must not be bold: %q", line)
		}
		if !strings.Contains(stripSessionSGR(line), " (1)") {
			t.Fatalf("workspace header missing its count: %q", line)
		}
	}
	// A filter re-counts: only the remote-b machine and its two
	// groups remain.
	out = renderSessionTestFrame(entries, 1, 90, 30, false, true, "remote-b")
	lines = assertSessionBounded(t, out, 90, 30)
	text := strings.Join(lines, "\n")
	if strings.Contains(stripSessionSGR(text), "alpha") || strings.Contains(stripSessionSGR(text), "local (") || strings.Contains(stripSessionSGR(text), "remote-a") {
		t.Fatalf("filtered groups must disappear:\n%s", text)
	}
	if !strings.Contains(stripSessionSGR(text), "remote-b (2)") {
		t.Fatalf("filtered machine count wrong:\n%s", text)
	}
	if !strings.Contains(stripSessionSGR(text), "shared · id-2") || !strings.Contains(stripSessionSGR(text), "beta · ws-777") {
		t.Fatalf("the remote-b groups must keep their filtered counts:\n%s", text)
	}
}

func TestSessionRenderTreeHeadersAreNotSelectable(t *testing.T) {
	entries := sessionTestEntries(8)
	for _, sel := range []int{0, 3, 7} {
		out := renderSessionTestFrame(entries, sel, 90, 30, false, true, "")
		lines := assertSessionBounded(t, out, 90, 30)
		highlighted := sessionHighlightedLines(t, lines)
		if len(highlighted) != 1 {
			t.Fatalf("sel=%d: expected one highlighted leaf, got %d:\n%s", sel, len(highlighted), out)
		}
		line := highlighted[0]
		if strings.Contains(line, sessionStyleBold) {
			t.Fatalf("sel=%d: a machine header was highlighted: %q", sel, line)
		}
		visible := stripSessionSGR(line)
		// A leaf row carries the branch connector ("├─" or "└─") after
		// the left pad; machine and chrome rows never do.
		trimmed := strings.TrimLeft(visible, " ")
		if !strings.Contains(trimmed, "├─") && !strings.Contains(trimmed, "└─") {
			t.Fatalf("sel=%d: the highlight is not on a leaf: %q", sel, line)
		}
		if strings.Contains(line, sessionStyleDim) {
			t.Fatalf("sel=%d: a dim header was highlighted: %q", sel, line)
		}
	}
}

func TestSessionRenderTreeSelectedLeafVisibleWithParentContext(t *testing.T) {
	add := func(i int, machine, label, id string) PickerEntry {
		return PickerEntry{
			"ref":             fmt.Sprintf("ref-%02d", i),
			"name":            fmt.Sprintf("agent-%02d", i),
			"kind":            "claude",
			"status":          "working",
			"workspace_label": label,
			"workspace_id":    id,
			"tab_label":       "main",
			"cwd":             fmt.Sprintf("/tmp/proj-%02d", i),
			"machine":         machine,
			"title":           fmt.Sprintf("planner: task %02d", i),
		}
	}
	// Contiguous machine > workspace groups, as the input worker
	// delivers them.
	entries := make([]PickerEntry, 0, 64)
	for i := 1; i <= 8; i++ {
		entries = append(entries, add(i, "local", "alpha", "a1"))
	}
	for i := 9; i <= 16; i++ {
		entries = append(entries, add(i, "local", "shared", "id-2"))
	}
	for i := 17; i <= 24; i++ {
		entries = append(entries, add(i, "local", "beta", "ws-777"))
	}
	for i := 25; i <= 32; i++ {
		entries = append(entries, add(i, "local", "shared", "id-1"))
	}
	for i := 33; i <= 40; i++ {
		entries = append(entries, add(i, "remote-a", "alpha", "a1"))
	}
	for i := 41; i <= 48; i++ {
		entries = append(entries, add(i, "remote-a", "beta", "ws-777"))
	}
	for i := 49; i <= 56; i++ {
		entries = append(entries, add(i, "remote-b", "shared", "id-1"))
	}
	for i := 57; i <= 64; i++ {
		entries = append(entries, add(i, "remote-b", "shared", "id-2"))
	}
	for _, sel := range []int{0, 40, 61} {
		out := renderSessionTestFrame(entries, sel, 40, 18, false, true, "")
		lines := assertSessionBounded(t, out, 40, 18)
		if len(lines) != 18 {
			t.Fatalf("sel=%d: frame must fill the viewport, got %d lines:\n%s", sel, len(lines), out)
		}
		highlighted := sessionHighlightedLines(t, lines)
		if len(highlighted) != 1 {
			t.Fatalf("sel=%d: expected one visible selected leaf, got %d:\n%s", sel, len(highlighted), out)
		}
		if !strings.Contains(stripSessionSGR(highlighted[0]), fmt.Sprintf("agent-%02d", sel+1)) {
			t.Fatalf("sel=%d: selected leaf not visible: %q", sel, highlighted[0])
		}
	}
	// Scrolling into the middle of a group keeps the parent context on
	// top: the window's first line is the workspace header (or the
	// machine header above it), never a bare leaf without its parent.
	// Entry 62 (index 61) is the sixth leaf of the remote-b 'shared
	// · id-2' group, so the header fits above the window and the
	// leaf stays visible.
	out := renderSessionTestFrame(entries, 61, 40, 18, false, true, "")
	lines := assertSessionBounded(t, out, 40, 18)
	window := lines[2:] // header and search on top; a full window takes no separator
	if len(window) == 0 {
		t.Fatalf("no window lines:\n%s", out)
	}
	first := window[0]
	if !strings.Contains(first, sessionStyleDim) || !strings.Contains(stripSessionSGR(first), "shared · id-2") {
		t.Fatalf("scrolled tree must keep the parent context on top: %q", first)
	}
	highlighted := sessionHighlightedLines(t, lines)
	if len(highlighted) != 1 || !strings.Contains(stripSessionSGR(highlighted[0]), "agent-62") {
		t.Fatalf("selected leaf lost with the parent context:\n%s", out)
	}
}

func TestSessionRenderSearchLoadingFailureAndPinStates(t *testing.T) {
	entries := sessionTestEntries(6)
	t.Run("search no results", func(t *testing.T) {
		out := renderSessionTestFrame(entries, 0, 60, 12, false, false, "zzz")
		if !strings.Contains(out, `no results for "zzz"`) {
			t.Fatalf("no-results state missing:\n%s", out)
		}
	})
	t.Run("empty picker and board", func(t *testing.T) {
		if out := renderSessionTestFrame(nil, -1, 60, 12, false, false, ""); !strings.Contains(out, "no panes") {
			t.Fatalf("empty picker state missing:\n%s", out)
		}
		if out := renderSessionTestFrame(nil, -1, 60, 12, true, false, ""); !strings.Contains(out, "no agents") {
			t.Fatalf("empty board state missing:\n%s", out)
		}
	})
	t.Run("failures trim from the front", func(t *testing.T) {
		state := NewPickerState()
		state.Entries = entries
		state.ViewState.Enabled = true
		state.Selected = 0
		state.Failures = []PickerFailure{
			{Label: "m1", Cause: "c1"},
			{Label: "m2", Cause: "c2"},
			{Label: "m3", Cause: "c3"},
			{Label: "m4", Cause: "c4"},
		}
		lines := assertSessionBounded(t, renderPickerModal(state, 60, 10, ""), 60, 10)
		text := strings.Join(lines, "\n")
		if strings.Contains(text, "m1") {
			t.Fatalf("earliest failure should be trimmed:\n%s", text)
		}
		if !strings.Contains(text, "machine m4: failed (c4)") {
			t.Fatalf("latest failure should be kept:\n%s", text)
		}
	})
	t.Run("board pin missing draws no cursor", func(t *testing.T) {
		state := NewBoardState()
		state.Entries = entries
		state.ViewState.Enabled = true
		state.refreshing = true
		state.selectedRef = "gone"
		state.Selected = 2
		lines := assertSessionBounded(t, renderBoardModal(state, 60, 14, ""), 60, 14)
		if highlighted := sessionHighlightedLines(t, lines); len(highlighted) != 0 {
			t.Fatalf("a missing pin must not highlight any row:\n%s", strings.Join(lines, "\n"))
		}
		if !strings.Contains(strings.Join(lines, "\n"), "selected session left this load; pick another with ↑/↓") {
			t.Fatalf("pin-missing guidance missing:\n%s", strings.Join(lines, "\n"))
		}
	})
	t.Run("board pin follows the ref during refresh", func(t *testing.T) {
		state := NewBoardState()
		state.Entries = entries
		state.ViewState.Enabled = true
		state.refreshing = true
		state.selectedRef = "ref-05"
		state.Selected = 1
		lines := assertSessionBounded(t, renderBoardModal(state, 60, 14, ""), 60, 14)
		highlighted := sessionHighlightedLines(t, lines)
		if len(highlighted) != 1 || !strings.Contains(stripSessionSGR(highlighted[0]), "agent-05") {
			t.Fatalf("cursor must follow the pinned ref:\n%s", strings.Join(lines, "\n"))
		}
	})
	t.Run("navigation notice", func(t *testing.T) {
		state := NewPickerState()
		state.Entries = entries
		state.ViewState.Enabled = true
		state.Selected = 1
		lines := assertSessionBounded(t, renderPickerModal(state, 60, 14, "Focusing selected pane… Esc cancels"), 60, 14)
		if !strings.Contains(strings.Join(lines, "\n"), "Focusing selected pane… Esc cancels") {
			t.Fatalf("navigation notice missing:\n%s", strings.Join(lines, "\n"))
		}
	})
}

func TestSessionRenderFooterAboveTheFixedStatusStrip(t *testing.T) {
	entries := sessionTestEntries(1)
	for height := 1; height <= 30; height++ {
		out := renderSessionTestFrame(entries, 0, 70, height, false, false, "")
		lines := assertSessionBounded(t, out, 70, height)
		if len(lines) != height {
			t.Fatalf("height %d: frame must fill the viewport, got %d lines:\n%s", height, len(lines), out)
		}
		last := stripSessionSGR(lines[len(lines)-1])
		switch {
		case height >= 10:
			// The fixed status strip ends the frame: the divider and the
			// Ready status line below the shortcuts, in order.
			if !strings.HasSuffix(last, "Ready") {
				t.Fatalf("height %d: status line must close the frame: %q", height, last)
			}
			if div := stripSessionSGR(lines[len(lines)-2]); !strings.Contains(div, "\u2500") {
				t.Fatalf("height %d: divider missing above the status line: %q", height, div)
			}
			if !strings.HasSuffix(stripSessionSGR(lines[len(lines)-3]), "Esc Close") {
				t.Fatalf("height %d: footer lost above the divider: %q", height, lines[len(lines)-3])
			}
		case height >= 7:
			// Degraded strip: the status line keeps its row, the divider
			// drops, the footer sits directly above.
			if !strings.HasSuffix(last, "Ready") {
				t.Fatalf("height %d: status line must close the frame: %q", height, last)
			}
			if !strings.HasSuffix(stripSessionSGR(lines[len(lines)-2]), "Esc Close") {
				t.Fatalf("height %d: footer lost above the status line: %q", height, lines[len(lines)-2])
			}
		default:
			// No strip room: the footer keeps its bottom anchor.
			if !strings.Contains(last, "Esc") {
				t.Fatalf("height %d: Esc guidance lost: %q", height, last)
			}
		}
	}
	// The full three-line footer, in order, above the strip.
	for height := 10; height <= 30; height += 5 {
		out := renderSessionTestFrame(entries, 0, 70, height, false, false, "")
		lines := assertSessionBounded(t, out, 70, height)
		a := stripSessionSGR(lines[len(lines)-5])
		b := stripSessionSGR(lines[len(lines)-4])
		c := stripSessionSGR(lines[len(lines)-3])
		if !strings.Contains(a, "↑↓ Select") || !strings.Contains(a, "v Tree") {
			t.Fatalf("height %d: footer line 1 wrong: %q", height, a)
		}
		if !strings.Contains(b, "p Pane") || !strings.Contains(b, "s Status") {
			t.Fatalf("height %d: footer line 2 wrong: %q", height, b)
		}
		if !strings.Contains(c, "c Copy") || !strings.Contains(c, "Enter Focus") || !strings.Contains(c, "Esc Close") {
			t.Fatalf("height %d: footer line 3 wrong: %q", height, c)
		}
	}
	// The board footer gains r Refresh in the remaining space, and the
	// view toggle flips with the view.
	board := renderSessionTestFrame(sessionTestEntries(3), 0, 70, 16, true, false, "")
	boardLines := strings.Split(strings.TrimSuffix(board, "\n"), "\n")
	if !strings.Contains(stripSessionSGR(boardLines[len(boardLines)-5]), "r Refresh") {
		t.Fatalf("board footer missing r Refresh:\n%s", board)
	}
	tree := renderSessionTestFrame(sessionTestEntries(3), 0, 70, 16, false, true, "")
	treeLines := strings.Split(strings.TrimSuffix(tree, "\n"), "\n")
	if !strings.Contains(stripSessionSGR(treeLines[len(treeLines)-5]), "v Table") {
		t.Fatalf("tree footer must offer v Table:\n%s", tree)
	}
}

func TestSessionRenderResizeStaysBounded(t *testing.T) {
	entries := sessionTestEntries(40)
	for width := 1; width <= 120; width += 7 {
		for height := 1; height <= 40; height += 3 {
			for _, tree := range []bool{false, true} {
				out := renderSessionTestFrame(entries, 17, width, height, false, tree, "")
				assertSessionBounded(t, out, width, height)
			}
		}
	}
	for width := 1; width <= 60; width += 5 {
		for height := 1; height <= 20; height += 2 {
			state := NewBoardState()
			state.Entries = entries
			state.ViewState.Enabled = true
			state.UpdatedAt = "09:41:00"
			assertSessionBounded(t, renderBoardModal(state, width, height, "reloading"), width, height)
		}
	}
}

func TestSessionRenderHostileAndCombiningData(t *testing.T) {
	entries := []PickerEntry{
		{
			"ref":             "\x1b[31mref\x1b[0m-01",
			"name":            "bad\x1b]0;evil\x07name\ttab\nnewline",
			"kind":            "clau\x01de",
			"status":          "wor\x02k\x01ing",
			"workspace_label": strings.Repeat("long-workspace-", 10),
			"tab_label":       "main",
			"cwd":             "/tmp/\x1b]52;c;UEFO\x07copy",
			"machine":         "local",
		},
		{
			"ref":             "r2",
			"name":            "部署-部署-部署-部署-部署-部署",
			"kind":            "pi",
			"status":          "working",
			"workspace_label": "ws",
			"tab_label":       "t",
			"cwd":             "/tmp/部署",
			"machine":         "local",
		},
		{
			"ref":             "r3",
			"name":            "café\u0301 " + strings.Repeat("n", 60),
			"kind":            "pi",
			"status":          "idle",
			"workspace_label": "ws",
			"tab_label":       "t",
			"cwd":             strings.Repeat("/a/b/c/", 20),
			"machine":         "local",
		},
	}
	for _, width := range []int{10, 24, 48, 100} {
		for _, tree := range []bool{false, true} {
			for sel := range entries {
				out := renderSessionTestFrame(entries, sel, width, 12, false, tree, "")
				lines := assertSessionBounded(t, out, width, 12)
				text := strings.Join(lines, "\n")
				if strings.Contains(text, "evil") {
					t.Fatalf("width=%d tree=%v: OSC payload leaked:\n%s", width, tree, out)
				}
				if strings.ContainsRune(text, '\x07') || strings.ContainsRune(text, 0x01) || strings.ContainsRune(text, 0x02) {
					t.Fatalf("width=%d tree=%v: control byte leaked:\n%q", width, tree, text)
				}
				for _, line := range lines {
					if strings.Contains(line, "\x1b[31m") {
						t.Fatalf("width=%d tree=%v: CSI leaked: %q", width, tree, line)
					}
				}
				// Cuts end with an ellipsis, never a split rune.
				if width < 100 {
					found := false
					for _, line := range lines {
						if strings.HasSuffix(stripSessionSGR(line), "…") {
							found = true
						}
					}
					if !found {
						t.Fatalf("width=%d tree=%v: expected an ellipsized cut:\n%s", width, tree, out)
					}
				}
			}
		}
	}
}

func TestSessionRenderTinyViewports(t *testing.T) {
	entries := sessionTestEntries(50)
	for _, width := range []int{1, 3, 6, 8, 12} {
		for _, height := range []int{1, 2, 3, 4, 5} {
			for _, tree := range []bool{false, true} {
				out := renderSessionTestFrame(entries, 49, width, height, false, tree, "")
				lines := assertSessionBounded(t, out, width, height)
				if len(lines) != height {
					t.Fatalf("%dx%d tree=%v: frame must fill the viewport:\n%s", width, height, tree, out)
				}
				// The Esc guidance is the first thing that must survive.
				if width >= 10 && !strings.Contains(stripSessionSGR(lines[len(lines)-1]), "Esc") {
					t.Fatalf("%dx%d tree=%v: Esc guidance lost: %q", width, height, tree, lines[len(lines)-1])
				}
			}
		}
	}
	// At 12x5 there is room for a header, a search row and a selected
	// leaf band (the pane name itself cannot fit in 12 columns).
	out := renderSessionTestFrame(entries, 49, 12, 5, false, false, "")
	lines := assertSessionBounded(t, out, 12, 5)
	if !strings.Contains(stripSessionSGR(lines[1]), ">") {
		t.Fatalf("search row missing at 12x5:\n%s", out)
	}
	if highlighted := sessionHighlightedLines(t, lines); len(highlighted) != 1 {
		t.Fatalf("selected leaf missing at 12x5:\n%s", out)
	}
	// At 40x5 workspace IDs take some space, but the selected row and
	// its full ref must remain visible even when the pane name is cut.
	out = renderSessionTestFrame(entries, 49, 40, 5, false, false, "")
	lines = assertSessionBounded(t, out, 40, 5)
	if highlighted := sessionHighlightedLines(t, lines); len(highlighted) != 1 || !strings.Contains(stripSessionSGR(highlighted[0]), "agent-") || !strings.Contains(out, "ref-50") {
		t.Fatalf("selected leaf missing at 40x5:\n%s", out)
	}
	out = renderSessionTestFrame(entries, 49, 48, 5, false, false, "")
	lines = assertSessionBounded(t, out, 48, 5)
	if highlighted := sessionHighlightedLines(t, lines); len(highlighted) != 1 || !strings.Contains(stripSessionSGR(highlighted[0]), "agent-50") {
		t.Fatalf("selected pane name missing with sufficient space:\n%s", out)
	}
}

func TestSessionRenderAgentColumnDropsBeforeCompression(t *testing.T) {
	entries := []PickerEntry{
		{"ref": "r1", "name": "short-1", "kind": "longagentname1", "status": "working", "workspace_label": "ws-1", "tab_label": "t", "cwd": "/tmp/a", "machine": "local"},
		{"ref": "r2", "name": "short-2", "kind": "longagentname2", "status": "blocked", "workspace_label": "ws-2", "tab_label": "t", "cwd": "/tmp/b", "machine": "local"},
	}
	// Wide: the agent column is present and aligned.
	wide := renderSessionTestFrame(entries, 0, 80, 14, false, false, "")
	if !strings.Contains(wide, "longagentname1") || !strings.Contains(wide, "longagentname2") {
		t.Fatalf("wide window must keep the agent column:\n%s", wide)
	}
	// Narrow: the agent column is dropped before the essential columns
	// are compressed, and the pane stays readable (the header labels
	// Machine/Workspace/Status take part in the sizing).
	narrow := renderSessionTestFrame(entries, 0, 40, 14, false, false, "")
	lines := assertSessionBounded(t, narrow, 40, 14)
	text := strings.Join(lines, "\n")
	if strings.Contains(text, "longagentname") {
		t.Fatalf("agent column must be dropped at 40 columns:\n%s", narrow)
	}
	for _, pane := range []string{"short-1", "short-2"} {
		found := false
		for _, line := range lines {
			if strings.Contains(stripSessionSGR(line), pane) {
				found = true
			}
		}
		if !found {
			t.Fatalf("pane %q must stay useful at 40 columns:\n%s", pane, narrow)
		}
	}
}

func TestSessionRenderSearchFocusDistinguishable(t *testing.T) {
	entries := sessionTestEntries(3)
	state := NewPickerState()
	state.Entries = entries
	state.ViewState.Enabled = true
	state.Selected = -1
	plain := renderPickerModal(state, 70, 10, "")
	state.ViewState.SearchFocused = true
	focused := renderPickerModal(state, 70, 10, "")
	if plain == focused {
		t.Fatalf("focused search must be visibly distinguishable:\n%s", focused)
	}
	lines := assertSessionBounded(t, focused, 70, 10)
	searchLine := lines[1]
	if !strings.Contains(searchLine, modalHighlightOn) {
		t.Fatalf("focused search row must carry the focus band: %q", searchLine)
	}
	plainLines := assertSessionBounded(t, plain, 70, 10)
	if strings.Contains(plainLines[1], modalHighlightOn) {
		t.Fatalf("unfocused search row must not be banded: %q", plainLines[1])
	}
	// The placeholder is visible and dim when the query is empty.
	if !strings.Contains(stripSessionSGR(lines[1]), "Search…") {
		t.Fatalf("placeholder missing: %q", searchLine)
	}
	state.Query = "abc"
	focused = renderPickerModal(state, 70, 10, "")
	lines = assertSessionBounded(t, focused, 70, 10)
	if !strings.Contains(stripSessionSGR(lines[1]), "> abc") {
		t.Fatalf("query missing from the search row: %q", lines[1])
	}
}

func TestSessionRenderBoardDetailTitleWhenRoomPermits(t *testing.T) {
	entries := sessionTestEntries(4)
	state := NewBoardState()
	state.Entries = entries
	state.ViewState.Enabled = true
	state.Selected = 0
	// Wide: the task title joins the detail line.
	wide := renderBoardModal(state, 100, 12, "")
	if !strings.Contains(wide, "ref-01 · main · /tmp/proj-01 · task 01") {
		t.Fatalf("board detail missing the task title:\n%s", wide)
	}
	// Narrow: the title drops out, the ref/tab/cwd stay.
	narrow := renderBoardModal(state, 34, 12, "")
	lines := assertSessionBounded(t, narrow, 34, 12)
	found := false
	for _, line := range lines {
		visible := stripSessionSGR(line)
		if strings.Contains(visible, "ref-01") && !strings.Contains(visible, "task 01") {
			found = true
		}
	}
	if !found {
		t.Fatalf("narrow board detail should keep the ref without the title:\n%s", narrow)
	}
}

func TestSessionRenderEnabledGateKeepsLegacyContract(t *testing.T) {
	entries := sessionTestEntries(10)
	t.Run("legacy picker", func(t *testing.T) {
		state := NewPickerState()
		state.Entries = entries
		state.Selected = 2
		out := renderPickerModal(state, 60, 20, "")
		lines := assertSessionBounded(t, out, 60, 20)
		if lines[0] != "Pick pane" || !strings.HasPrefix(lines[1], "> ") {
			t.Fatalf("legacy header changed: %q / %q", lines[0], lines[1])
		}
		if !strings.Contains(lines[len(lines)-1], "c Copy · Enter Focus · Esc Close") {
			t.Fatalf("legacy footer changed: %q", lines[len(lines)-1])
		}
	})
	t.Run("legacy board", func(t *testing.T) {
		state := NewBoardState()
		state.Entries = entries
		state.UpdatedAt = "10:00:00"
		out := renderBoardModal(state, 60, 15, "")
		lines := assertSessionBounded(t, out, 60, 15)
		if !strings.HasPrefix(lines[0], "10 agents") {
			t.Fatalf("legacy board totals changed: %q", lines[0])
		}
	})
	t.Run("new picker drops the inner title", func(t *testing.T) {
		state := NewPickerState()
		state.Entries = entries
		state.ViewState.Enabled = true
		state.Selected = 2
		out := renderPickerModal(state, 60, 20, "")
		if strings.Contains(out, "Pick pane") {
			t.Fatalf("the shared renderer must not repeat the popup title:\n%s", out)
		}
		lines := assertSessionBounded(t, out, 60, 20)
		// The shortcuts sit above the fixed status strip (divider + line).
		if !strings.HasSuffix(stripSessionSGR(lines[len(lines)-3]), "Esc Close") {
			t.Fatalf("new footer missing: %q", lines[len(lines)-3])
		}
	})
	t.Run("new picker shows the copy notice in the fixed strip", func(t *testing.T) {
		state := NewPickerState()
		state.Entries = entries
		state.ViewState.Enabled = true
		state.Selected = 2
		out := renderPickerModal(state, 60, 20, "Copied local/w3:p")
		lines := assertSessionBounded(t, out, 60, 20)
		if got := strings.TrimLeft(stripSessionSGR(lines[len(lines)-1]), " "); got != "STATUS  Copied local/w3:p" {
			t.Fatalf("copy notice missing from the fixed status line: %q", got)
		}
		if !strings.Contains(lines[len(lines)-1], sessionStyleBold+"Copied local/w3:p"+sessionStyleReset) {
			t.Fatalf("the confirmed copy must be bold: %q", lines[len(lines)-1])
		}
		if !strings.HasSuffix(stripSessionSGR(lines[len(lines)-3]), "Esc Close") {
			t.Fatalf("shortcuts must stay above the status line: %q", lines[len(lines)-3])
		}
	})
}

func TestSessionRenderTreeIncomingOrderAndConnectors(t *testing.T) {
	// The incoming order is deliberately not alphabetical: the remote
	// machine comes first and the workspace labels are reversed. The
	// renderer must form the group headers in first-occurrence order,
	// not re-sort the group keys.
	entries := []PickerEntry{
		{"ref": "r1", "name": "agent-01", "kind": "claude", "status": "working", "workspace_label": "zeta", "workspace_id": "z1", "tab_label": "main", "cwd": "/tmp/z", "machine": "remote-x"},
		{"ref": "r3", "name": "agent-02", "kind": "claude", "status": "idle", "workspace_label": "zeta", "workspace_id": "z1", "tab_label": "main", "cwd": "/tmp/z", "machine": "remote-x"},
		{"ref": "r2", "name": "agent-03", "kind": "claude", "status": "working", "workspace_label": "alpha", "workspace_id": "a1", "tab_label": "main", "cwd": "/tmp/a", "machine": "local"},
		{"ref": "r5", "name": "agent-05", "kind": "claude", "status": "working", "workspace_label": "alpha", "workspace_id": "a1", "tab_label": "main", "cwd": "/tmp/a", "machine": "local"},
		{"ref": "r4", "name": "agent-04", "kind": "claude", "status": "blocked", "workspace_label": "mid", "workspace_id": "m1", "tab_label": "main", "cwd": "/tmp/m", "machine": "local"},
	}
	out := renderSessionTestFrame(entries, 0, 90, 30, false, true, "")
	lines := assertSessionBounded(t, out, 90, 30)
	text := strings.Join(lines, "\n")
	// First-occurrence order: the remote-x machine (and its zeta group)
	// comes before the local machine; alpha comes before mid.
	iRemote := strings.Index(text, "remote-x (2)")
	iLocal := strings.Index(text, "local (3)")
	iZeta := strings.Index(text, "zeta · z1")
	iAlpha := strings.Index(text, "alpha · a1")
	iMid := strings.Index(text, "mid · m1")
	if iRemote == -1 || iLocal == -1 || iRemote > iLocal {
		t.Fatalf("machine groups must keep the incoming order: remote-x(%d) local(%d):\n%s", iRemote, iLocal, text)
	}
	if iZeta == -1 || iAlpha == -1 || iMid == -1 {
		t.Fatalf("workspace groups missing: zeta(%d) alpha(%d) mid(%d):\n%s", iZeta, iAlpha, iMid, text)
	}
	if iZeta > iAlpha || iAlpha > iMid {
		t.Fatalf("workspace groups must keep the incoming order: zeta(%d) alpha(%d) mid(%d):\n%s", iZeta, iAlpha, iMid, text)
	}
	// Visible branch connectors make the machine > workspace > leaf
	// nesting explicit.
	if !strings.Contains(text, "\u251c\u2500 ") || !strings.Contains(text, "\u2514\u2500 ") || !strings.Contains(text, "\u2502") {
		t.Fatalf("tree must draw branch connectors:\n%s", text)
	}
	// A missing machine value reads "local" and groups with it.
	missing := []PickerEntry{
		{"ref": "r1", "name": "agent-01", "kind": "claude", "status": "working", "workspace_label": "alpha", "workspace_id": "a1", "tab_label": "main", "cwd": "/tmp/a"},
		{"ref": "r2", "name": "agent-02", "kind": "claude", "status": "idle", "workspace_label": "alpha", "workspace_id": "a1", "tab_label": "main", "cwd": "/tmp/a", "machine": "local"},
	}
	out = renderSessionTestFrame(missing, 0, 90, 14, false, true, "")
	lines = assertSessionBounded(t, out, 90, 14)
	if !strings.Contains(stripSessionSGR(strings.Join(lines, "\n")), "local (2)") {
		t.Fatalf("a missing machine must read local:\n%s", out)
	}
}

// sessionPaneIDEntries has a pane_id, a pane known only by its full
// reference, and a pane with no identifier at all.
func sessionPaneIDEntries() []PickerEntry {
	base := func(name string) PickerEntry {
		return PickerEntry{"name": name, "kind": "claude", "status": "idle", "workspace_label": "alpha", "workspace_id": "a1", "tab_label": "main", "cwd": "/tmp/a", "machine": "local"}
	}
	withID := base("with-pane-id")
	withID["ref"] = "local/wAY:p1"
	withID["pane_id"] = "wAY:p1"
	fromRef := base("from-ref")
	fromRef["ref"] = "local/w6S:pAY"
	noID := base("no-id")
	noID["ref"] = "not-a-ref"
	return []PickerEntry{withID, fromRef, noID}
}

// sessionRowWith is the visible row carrying word, or "" without one.
func sessionRowWith(lines []string, word string) string {
	for _, line := range lines {
		if visible := stripSessionSGR(line); strings.Contains(visible, word) {
			return visible
		}
	}
	return ""
}

func TestSessionRenderTableShowsThePaneIDOfTheTree(t *testing.T) {
	entries := sessionPaneIDEntries()
	table := assertSessionBounded(t, renderSessionTestFrame(entries, 0, 90, 20, false, false, ""), 90, 20)
	tree := assertSessionBounded(t, renderSessionTestFrame(entries, 0, 90, 20, false, true, ""), 90, 20)

	header := sessionRowWith(table, "Machine")
	idCol := strings.Index(header, " ID ") + 1
	if idCol == 0 || strings.Index(header, "Pane") >= idCol || idCol >= strings.Index(header, "Agent") {
		t.Fatalf("the ID column must sit between Pane and Agent: %q", header)
	}
	for name, id := range map[string]string{"with-pane-id": "wAY:p1", "from-ref": "w6S:pAY"} {
		row := sessionRowWith(table, name)
		if got := strings.Index(row, id); got != idCol {
			t.Fatalf("%s: id %q at column %d, want %d: %q", name, id, got, idCol, row)
		}
		if leaf := sessionRowWith(tree, name); !strings.Contains(leaf, "("+id+")") {
			t.Fatalf("%s: the tree must show the same id %q: %q", name, id, leaf)
		}
	}
	row := sessionRowWith(table, "no-id")
	if got := strings.TrimSpace(string([]rune(row)[idCol : idCol+2])); got != "-" {
		t.Fatalf("a pane with no identifier must read -, got %q: %q", got, row)
	}
	if leaf := sessionRowWith(tree, "no-id"); strings.Contains(leaf, "(") {
		t.Fatalf("the tree must add no suffix without an identifier: %q", leaf)
	}
}

func TestSessionTableWidthsIDStaysWholeOrLeaves(t *testing.T) {
	entries := sessionPaneIDEntries()
	// One long row keeps the essential columns wider than the narrow
	// budgets, so the identifier competes with them for room.
	entries[0]["name"] = strings.Repeat("long-pane-name-", 6)
	sawNoAgent, sawNoID := false, false
	for avail := 1; avail <= 140; avail++ {
		lines := sessionTableLines(entries, 0, avail)
		text := strings.Join(lines, "\n")
		if strings.Contains(text, "wAY:") && !strings.Contains(text, "wAY:p1") {
			t.Fatalf("avail %d: the identifier was cut:\n%s", avail, text)
		}
		if strings.Contains(text, "w6S:") && !strings.Contains(text, "w6S:pAY") {
			t.Fatalf("avail %d: the identifier was cut:\n%s", avail, text)
		}
		hasAgent := strings.Contains(lines[0], "Agent")
		hasID := strings.Contains(text, "w6S:pAY")
		if hasAgent && !hasID {
			t.Fatalf("avail %d: Agent must leave before the identifier:\n%s", avail, text)
		}
		sawNoAgent = sawNoAgent || (!hasAgent && hasID)
		sawNoID = sawNoID || !hasID
		for i, line := range lines {
			if w := displayWidth(stripSessionSGR(line)); w > avail {
				t.Fatalf("avail %d: line %d is %d columns: %q", avail, i, w, line)
			}
		}
	}
	if !sawNoAgent || !sawNoID {
		t.Fatalf("the sweep must reach both narrow forms: no agent %v, no id %v", sawNoAgent, sawNoID)
	}

	// Without the identifier the row takes back its gap: a window that
	// cannot hold it keeps the four essential columns in the same room.
	widths, useAgent, useID := sessionTableWidths([][]string{{"local"}, {"alpha"}, {"pane"}, {"wAY:p1"}, {"claude"}, {"idle"}}, 14)
	if useAgent || useID || widths[3] != 0 || widths[4] != 0 {
		t.Fatalf("a window of 14 cannot hold the identifier: %v agent %v id %v", widths, useAgent, useID)
	}
	if got := widths[0] + widths[1] + widths[2] + widths[5]; got != 14-8 {
		t.Fatalf("the four columns must take the whole budget: %v", widths)
	}
}

func TestSessionRenderTablePaneIDIsSanitized(t *testing.T) {
	entries := sessionPaneIDEntries()
	entries[0]["pane_id"] = "wAY\x1b]0;evil\x07:p\x1b[31m1"
	text := strings.Join(stripAll(sessionTableLines(entries, 1, 90)), "\n")
	if strings.ContainsAny(text, "\x1b\x07") {
		t.Fatalf("control bytes reached the table: %q", text)
	}
}

func TestSessionRenderTableHeaderPinnedAboveScroll(t *testing.T) {
	entries := sessionTestEntries(60)
	out := renderSessionTestFrame(entries, 40, 90, 20, false, false, "")
	lines := assertSessionBounded(t, out, 90, 20)
	// The fixed column header row sits directly above the scrolled rows
	// (header, search, then the table header).
	if header := stripSessionSGR(lines[2]); !strings.Contains(header, "Machine") || !strings.Contains(header, "Status") {
		t.Fatalf("column header row must be pinned above the rows: %q", header)
	}
	text := strings.Join(stripAll(lines), "\n")
	// The window scrolled to the selection with the strip- and
	// detail-reserved budget (9 data rows at 90x20): early rows are out
	// of view, the selected row is inside the window, the header did not
	// scroll.
	if strings.Contains(text, "agent-01") {
		t.Fatalf("the header must stay pinned while the rows scroll:\n%s", out)
	}
	if !strings.Contains(text, "agent-37") || !strings.Contains(text, "agent-45") {
		t.Fatalf("scrolled window missing its rows:\n%s", out)
	}
	if strings.Contains(text, "agent-35") || strings.Contains(text, "agent-47") {
		t.Fatalf("the reserved budget must shrink the window:\n%s", out)
	}
	highlighted := sessionHighlightedLines(t, lines)
	if len(highlighted) != 1 || !strings.Contains(stripSessionSGR(highlighted[0]), "agent-41") || !strings.HasPrefix(strings.TrimLeft(stripSessionSGR(highlighted[0]), " "), "> ") {
		t.Fatalf("selected row must scroll with the > marker:\n%s", out)
	}
	if !strings.Contains(text, "ref-41 · main · /tmp/proj-41") {
		t.Fatalf("reserved detail missing with a scrolled window:\n%s", out)
	}
}

func stripAll(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, stripSessionSGR(line))
	}
	return out
}

func TestSessionRenderDetailReservedWithLargeRegistry(t *testing.T) {
	entries := sessionTestEntries(100)
	for _, tree := range []bool{false, true} {
		// A full registry fills the list window; the selected-metadata
		// row is reserved above the footer anyway.
		out := renderSessionTestFrame(entries, 49, 90, 20, false, tree, "")
		lines := assertSessionBounded(t, out, 90, 20)
		text := stripAll(lines)
		found := false
		for _, line := range text {
			if strings.Contains(line, "ref-50 · main · /tmp/proj-50") {
				found = true
			}
		}
		if !found {
			t.Fatalf("tree=%v: detail must be reserved with a full registry:\n%s", tree, out)
		}
	}
}

func TestSessionRenderHostileGroupAndLabelFields(t *testing.T) {
	entries := []PickerEntry{
		{
			"ref":             "r1",
			"name":            "agent-01",
			"kind":            "claude",
			"status":          "working",
			"workspace_label": "ws\x1b]0;evil-ws\x07",
			"workspace_id":    "id\x1b[31m",
			"tab_label":       "main",
			"cwd":             "/tmp/p",
			"machine":         "\x1b]1337;evil-m\x07remote",
		},
		{
			"ref":             "r2",
			"name":            "agent-02",
			"kind":            "claude",
			"status":          "idle",
			"workspace_label": "plain",
			"workspace_id":    "p1",
			"tab_label":       "main",
			"cwd":             "/tmp/q",
			"machine":         "remote",
		},
	}
	for _, tree := range []bool{false, true} {
		state := NewPickerState()
		state.Entries = entries
		state.ViewState.Enabled = true
		if tree {
			state.ViewState.View = "tree"
		}
		state.ViewState.Sort = "pane\x1b[31m"
		state.Selected = 0
		out := renderPickerModal(state, 90, 14, "")
		lines := assertSessionBounded(t, out, 90, 14)
		text := strings.Join(lines, "\n")
		for _, payload := range []string{"evil-ws", "evil-m", "1337"} {
			if strings.Contains(text, payload) {
				t.Fatalf("tree=%v: hostile payload leaked to the output:\n%s", tree, text)
			}
		}
		if strings.Contains(text, "\x1b[31m") {
			t.Fatalf("tree=%v: a foreign CSI reached the output:\n%s", tree, text)
		}
		// Machine labels sanitize for display without merging identities.
		if tree {
			if strings.Count(stripSessionSGR(text), "remote (1)") != 2 {
				t.Fatalf("tree: sanitized machine group missing:\n%s", text)
			}
		}
		// Hostile sort labels sanitize before display.
		if !strings.Contains(stripSessionSGR(lines[0]), "sort Pane ↑") {
			t.Fatalf("tree=%v: sort label not sanitized: %q", tree, lines[0])
		}
	}
}

// TestSessionRenderFrames writes the representative plain-text frames
// (100x30 and 60x16, table and tree) to the run evidence directory.
func TestSessionRenderFrames(t *testing.T) {
	dir := os.Getenv("HERDR_SOHO_RENDER_EVIDENCE_DIR")
	if dir == "" {
		dir = t.TempDir()
	} else if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("cannot write requested evidence: %v", err)
	}
	entries := sessionTestEntries(40)
	frames := []struct {
		name   string
		width  int
		height int
		board  bool
		tree   bool
		sel    int
	}{
		{"picker-table-100x30", 100, 30, false, false, 7},
		{"picker-tree-100x30", 100, 30, false, true, 7},
		{"board-table-60x16", 60, 16, true, false, 3},
		{"board-tree-60x16", 60, 16, true, true, 3},
	}
	for _, f := range frames {
		var out string
		if f.board {
			state := NewBoardState()
			state.Entries = entries
			state.ViewState.Enabled = true
			state.ViewState.View = viewFor(f.tree)
			state.UpdatedAt = "10:20:16"
			state.Selected = f.sel
			out = renderBoardModal(state, f.width, f.height, "")
		} else {
			state := NewPickerState()
			state.Entries = entries
			state.ViewState.Enabled = true
			state.ViewState.View = viewFor(f.tree)
			state.Selected = f.sel
			out = renderPickerModal(state, f.width, f.height, "")
		}
		plain := strings.Join(func() []string {
			lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
			for i, line := range lines {
				lines[i] = stripSessionSGR(line)
			}
			return lines
		}(), "\n")
		path := filepath.Join(dir, f.name+".txt")
		if err := os.WriteFile(path, []byte(plain+"\n"), 0o644); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
		t.Logf("frame %s:\n%s", f.name, plain)
	}
}

func viewFor(tree bool) string {
	if tree {
		return "tree"
	}
	return ""
}
