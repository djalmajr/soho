package plugin

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	contextpkg "context"

	"github.com/djalmajr/soho/internal/platform"
)

// teamCallTimeoutMs is the deadline for every CLI call the panel makes.
const teamCallTimeoutMs = 60_000

// teamViewScrollPage is how many lines PgUp/PgDn move.
const teamViewScrollPage = 10

const (
	teamViewTeam      = 1
	teamViewDoctor    = 2
	teamViewResources = 3
	teamViewFriction  = 4
)

var teamViewNames = map[int]string{
	teamViewTeam:      "equipe",
	teamViewDoctor:    "doctor",
	teamViewResources: "recursos",
	teamViewFriction:  "friction",
}

// teamLoad kinds: what the loader runs for the panel.
const (
	teamLoadView1 = iota + 1
	teamLoadView2
	teamLoadView3
	teamLoadView4
	teamLoadCollect
)

type teamTarget struct {
	WorkspaceID string
	PaneID      string
	TabID       string
	Cwd         string
}

type teamLoad struct {
	kind      int
	agent     string
	doRelease bool
	doGCYes   bool
}

// teamLoadResult is one finished load; the loader goroutine builds it and
// the main loop applies it, so the panel's live state is only touched by
// the main goroutine.
type teamLoadResult struct {
	load teamLoad

	explainLines    []string
	explainFailure  string
	workers         []string
	rosterFailure   string
	collaborations  map[string]teamCollaboration
	statusFailure   string
	doctorLines     []string
	doctorFailure   string
	gcLines         []string
	gcFailure       string
	frictionLines   []string
	frictionFailure string
	collectLines    []string
	collectFailure  string
	releaseOut      []string
	releaseFailure  string
	gcYesOut        []string
	gcYesFailure    string
}

// TeamState is the panel's live state (main goroutine only).
type TeamState struct {
	target    teamTarget
	targetErr string // non-empty: invalid target; the panel shows the cause and Esc closes

	view   int
	scroll int

	// view 1 (equipe)
	explainLines   []string
	explainFailure string
	workers        []string
	rosterFailure  string
	collaborations map[string]teamCollaboration
	statusFailure  string
	selected       int
	// confirmed release, shown in the equipe view until the view changes
	releaseAgent   string
	releaseOut     []string
	releaseFailure string
	// view 3
	gcLines      []string
	gcFailure    string
	gcYesOut     []string
	gcYesFailure string
	// views 2 and 4
	doctorLines     []string
	doctorFailure   string
	frictionLines   []string
	frictionFailure string

	viewLoaded [4]bool

	// subviews
	subview        string // "" | "collect" | "confirm-release" | "confirm-gc"
	confirmAgent   string
	collectAgent   string
	collectLines   []string
	collectLoaded  bool
	collectFailure string

	// pending confirmed actions, consumed by the next load of the view
	pendingRelease string
	pendingGCYes   bool
	// confirmArmAt: a write confirmation accepts its `y` only after a quiet
	// window with no input since it opened (see FeedChunk).
	confirmArmAt time.Time

	Exit       string
	EscPending bool
	CSIPending bool
	csiBuf     string
	// inPaste is true between the bracketed-paste markers (ESC[200~ and
	// ESC[201~): every key in between is discarded, in every view.
	inPaste bool
}

// NewTeamState builds the panel state on the given initial view.
func NewTeamState(view int) *TeamState {
	return &TeamState{view: view}
}

// teamInitialView parses the pane's arguments: no flag means the equipe
// view, and --view accepts only "doctor".
func teamInitialView(args []string) (int, error) {
	view := teamViewTeam
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--view" {
			if i+1 >= len(args) {
				return 0, fmt.Errorf("team: --view expects a value (only 'doctor' is supported)")
			}
			i++
			if args[i] != "doctor" {
				return 0, fmt.Errorf("team: unknown --view %q (only 'doctor' is supported)", args[i])
			}
			view = teamViewDoctor
			continue
		}
		return 0, fmt.Errorf("team: unknown argument %q", arg)
	}
	return view, nil
}

// resolveTeamTarget resolves and validates the focused target exactly like
// the bridge does: context from HERDR_PLUGIN_CONTEXT_JSON, the focused pane
// via `herdr pane get` (a workspace divergence is rejected) and a cwd that
// exists. On failure it returns the cause, which the panel shows on one
// line.
func resolveTeamTarget(env platform.Env, platformName string) (teamTarget, string) {
	ctx, err := parseContext(env)
	if err != nil {
		return teamTarget{}, err.(*BridgeError).Message
	}
	herdrBin := env.Get("HERDR_BIN_PATH")
	if herdrBin == "" {
		return teamTarget{}, "HERDR_BIN_PATH missing; the action must run inside Herdr"
	}
	focused, err := getPane(herdrBin, ctx.PaneID, env, platformName)
	if err != nil {
		return teamTarget{}, err.(*BridgeError).Message
	}
	if focused.WorkspaceID != ctx.WorkspaceID {
		return teamTarget{}, fmt.Sprintf("workspace divergence: the context points to '%s' and pane %s belongs to '%s'; target rejected", ctx.WorkspaceID, ctx.PaneID, focused.WorkspaceID)
	}
	info, statErr := os.Stat(focused.Cwd)
	if statErr != nil {
		return teamTarget{}, "pane cwd does not exist: " + focused.Cwd
	}
	if !info.IsDir() {
		return teamTarget{}, "pane cwd is not a directory: " + focused.Cwd
	}
	return teamTarget{WorkspaceID: ctx.WorkspaceID, PaneID: ctx.PaneID, TabID: ctx.TabID, Cwd: focused.Cwd}, ""
}

// teamCall runs one CLI call (the panel's own executable in production) in
// the target's cwd with the target's HERDR_* ids. Reads carry
// SOHO_NOWRITE=1; writes run only after an on-screen confirmation
// and never carry it. A failure (non-zero exit, timeout, spawn error) is
// returned as a description for the view - the panel never crashes on it.
func teamCall(ctx contextpkg.Context, exe string, env platform.Env, platformName string, target teamTarget, args []string, nowrite bool) ([]string, string) {
	childEnv := env.Clone()
	childEnv["HERDR_WORKSPACE_ID"] = target.WorkspaceID
	childEnv["HERDR_PANE_ID"] = target.PaneID
	// The panel acts deliberately in the name of the focused pane (the
	// only soho runner that does): the caller-pane check (DJA-394)
	// trusts the delegation and skips the ancestry climb for it. A
	// script that wants to act on purpose for a pane sets the same
	// variable itself.
	childEnv["SOHO_DELEGATED_PANE"] = target.PaneID
	if target.TabID != "" {
		childEnv["HERDR_TAB_ID"] = target.TabID
	} else {
		delete(childEnv, "HERDR_TAB_ID")
	}
	if nowrite {
		childEnv["SOHO_NOWRITE"] = "1"
	} else {
		// A write must run ungated: strip the gate name the current read
		// calls inject.
		delete(childEnv, "SOHO_NOWRITE")
	}
	if bin := env.Get("HERDR_BIN_PATH"); bin != "" {
		childEnv = withPath(childEnv, filepath.Dir(bin), platformName)
	}
	run := platform.RunExecutable(exe, args, platform.RunOptions{Context: ctx, Env: childEnv, Platform: platformName, Cwd: target.Cwd, TimeoutMs: teamCallTimeoutMs})
	return teamCallResult(args, run)
}

type teamCollaboration struct {
	Agent      string `json:"agent"`
	Status     string `json:"status"`
	Assignment string `json:"assignment"`
	Phase      string `json:"phase"`
	Round      int    `json:"round"`
	Revision   string `json:"revision"`
	Delivery   string `json:"delivery"`
	Cause      string `json:"cause"`
}

func teamCollaborationRows(lines []string) map[string]teamCollaboration {
	rows := make(map[string]teamCollaboration)
	for _, line := range lines {
		var row teamCollaboration
		if json.Unmarshal([]byte(line), &row) == nil && row.Agent != "" && (row.Status == "collaborating" || strings.HasPrefix(row.Status, "collaboration-")) {
			rows[row.Agent] = row
		}
	}
	return rows
}

func teamCallResult(args []string, run platform.RunResult) ([]string, string) {
	what := args[0]
	if run.TimedOut {
		return nil, fmt.Sprintf("%s timed out after %ds", what, teamCallTimeoutMs/1000)
	}
	if run.Error != "" {
		return nil, fmt.Sprintf("%s spawn failed: %s", what, run.Error)
	}
	if run.NotFound {
		return nil, what + " not found"
	}
	code := -1
	if run.Status != nil {
		code = *run.Status
	}
	if code == 0 || (what == "status" && code >= 0) {
		// Split first, then strip per line: StripPickerControls flattens
		// newlines, so stripping the whole blob would destroy the table
		// structure (the roster's blank-line sections and its rows).
		lines := strings.Split(run.Stdout, "\n")
		out := make([]string, 0, len(lines))
		for _, line := range lines {
			out = append(out, StripPickerControls(line))
		}
		for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
			out = out[:len(out)-1]
		}
		if code == 4 {
			return out, "status has unavailable or disabled participants; inspect their causes"
		}
		if code != 0 {
			return out, fmt.Sprintf("status has participants requiring attention (exit %d); inspect their causes", code)
		}
		return out, ""
	}
	line := fmt.Sprintf("%s failed (exit %d)", what, code)
	if stderr := strings.TrimSpace(StripPickerControls(run.Stderr)); stderr != "" {
		line += ": " + stderr
	}
	return nil, line
}

// loadTeamView runs the load's CLI calls (sequentially, each with its own
// deadline) and fills the result. It does not touch the live state.
func loadTeamView(ctx contextpkg.Context, exe string, env platform.Env, platformName string, target teamTarget, load teamLoad, result *teamLoadResult) {
	call := func(args []string, nowrite bool) ([]string, string) {
		if ctx.Err() != nil {
			return nil, ""
		}
		return teamCall(ctx, exe, env, platformName, target, args, nowrite)
	}
	switch load.kind {
	case teamLoadView1:
		if load.doRelease {
			result.releaseOut, result.releaseFailure = call([]string{"release", load.agent, "--close"}, false)
		}
		result.explainLines, result.explainFailure = call([]string{"explain"}, true)
		lines, failure := call([]string{"roster"}, true)
		result.workers = teamWorkers(lines)
		result.rosterFailure = failure
		statusLines, statusFailure := call([]string{"status"}, true)
		result.collaborations, result.statusFailure = teamCollaborationRows(statusLines), statusFailure
	case teamLoadView2:
		result.doctorLines, result.doctorFailure = call([]string{"doctor"}, true)
	case teamLoadView3:
		if load.doGCYes {
			result.gcYesOut, result.gcYesFailure = call([]string{"gc", "--yes"}, false)
		}
		result.gcLines, result.gcFailure = call([]string{"gc"}, true)
	case teamLoadView4:
		result.frictionLines, result.frictionFailure = call([]string{"friction", "--summary"}, true)
	case teamLoadCollect:
		result.collectLines, result.collectFailure = call([]string{"collect", load.agent, "--lines", "60"}, true)
	}
}

// applyTeamLoad merges one finished load into the live state.
func applyTeamLoad(state *TeamState, result teamLoadResult) {
	switch result.load.kind {
	case teamLoadView1:
		if result.load.doRelease {
			state.releaseAgent = result.load.agent
			state.releaseOut = result.releaseOut
			state.releaseFailure = result.releaseFailure
		}
		state.explainLines, state.explainFailure = result.explainLines, result.explainFailure
		state.workers, state.rosterFailure = result.workers, result.rosterFailure
		state.collaborations, state.statusFailure = result.collaborations, result.statusFailure
		if state.selected >= len(state.workers) {
			state.selected = len(state.workers) - 1
		}
		if state.selected < 0 {
			state.selected = 0
		}
		state.viewLoaded[teamViewTeam-1] = true
	case teamLoadView2:
		state.doctorLines, state.doctorFailure = result.doctorLines, result.doctorFailure
		state.viewLoaded[teamViewDoctor-1] = true
	case teamLoadView3:
		if result.load.doGCYes {
			state.gcYesOut = result.gcYesOut
			state.gcYesFailure = result.gcYesFailure
		}
		state.gcLines, state.gcFailure = result.gcLines, result.gcFailure
		state.viewLoaded[teamViewResources-1] = true
	case teamLoadView4:
		state.frictionLines, state.frictionFailure = result.frictionLines, result.frictionFailure
		state.viewLoaded[teamViewFriction-1] = true
	case teamLoadCollect:
		state.collectLines, state.collectFailure = result.collectLines, result.collectFailure
		state.collectLoaded = true
	}
}

// pendingLoad builds the load request for the panel's current position.
func (s *TeamState) pendingLoad() teamLoad {
	if s.subview == "collect" && s.collectAgent != "" {
		return teamLoad{kind: teamLoadCollect, agent: s.collectAgent}
	}
	load := teamLoad{kind: s.view}
	if s.view == teamViewTeam && s.pendingRelease != "" {
		load.doRelease = true
		load.agent = s.pendingRelease
		s.pendingRelease = ""
	}
	if s.view == teamViewResources && s.pendingGCYes {
		load.doGCYes = true
		s.pendingGCYes = false
	}
	return load
}

// teamWorkers extracts the first table of the roster output: the lines
// after the NAME header until the first empty line. The `# other live
// agents` section (and everything after the blank line) stays out.
func teamWorkers(lines []string) []string {
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "NAME") {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return nil
	}
	var workers []string
	for _, line := range lines[start:] {
		if strings.TrimSpace(line) == "" {
			break
		}
		workers = append(workers, line)
	}
	return workers
}

// teamAgent is the first field of a roster worker line (the NAME column).
func teamAgent(line string) string {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// teamWrap wraps text into lines of at most width display columns without
// splitting a rune: words break on spaces when possible, and a word wider
// than the width is hard-cut into pieces that always consume at least one
// character, so the loop cannot stall on a wide rune.
func teamWrap(text string, width int) []string {
	var out []string
	for _, raw := range strings.Split(text, "\n") {
		words := strings.Fields(raw)
		if len(words) == 0 {
			out = append(out, "")
			continue
		}
		line := ""
		flush := func() {
			out = append(out, line)
			line = ""
		}
		for _, word := range words {
			fit := 0
			if line != "" {
				fit = 1
			}
			if displayWidth(line)+fit+displayWidth(word) <= width {
				if line != "" {
					line += " "
				}
				line += word
				continue
			}
			if line != "" {
				flush()
			}
			if displayWidth(word) <= width {
				line = word
				continue
			}
			// A word wider than the width: hard-cut it, one piece per loop;
			// each piece consumes at least one rune, so the loop advances.
			for displayWidth(word) > width {
				piece, consumed := teamHardPiece(word, width)
				out = append(out, piece)
				word = word[consumed:]
			}
			line = word
		}
		if line != "" {
			flush()
		}
	}
	return out
}

// teamHardPiece returns one hard-cut line of `word` at `width` display
// columns and how many bytes of `word` it consumed. A rune wider than the
// width becomes the ellipsis (one column); the step always consumes at
// least one rune, so the wrap loop advances even when nothing fits.
func teamHardPiece(word string, width int) (string, int) {
	used := 0
	for i, r := range word {
		w := displayRuneWidth(r)
		if used+w > width {
			// Zero-width runes before r (combining marks) add no width, so
			// used can still be 0 here with i > 0: the step consumes them
			// and r whole, never a byte inside r.
			consumed := i + utf8.RuneLen(r)
			if used == 0 {
				// The first rune does not fit: the ellipsis (one column,
				// when it fits) takes its place and the rune is consumed.
				if width >= 1 {
					return "…", consumed
				}
				return "", consumed
			}
			return word[:i], i
		}
		used += w
	}
	return word, len(word)
}

// teamCut cuts text to at most width display columns, without splitting a
// rune. When the first rune does not fit, the result is the ellipsis (one
// column), so a wide character is never left unrepresented.
func teamCut(text string, width int) string {
	if width <= 0 {
		return ""
	}
	var b strings.Builder
	used := 0
	for _, r := range text {
		w := displayRuneWidth(r)
		if used+w > width {
			if used == 0 {
				return "…"
			}
			break
		}
		b.WriteRune(r)
		used += w
	}
	return b.String()
}

func teamViewBar(s *TeamState) string {
	cells := make([]string, 0, 4)
	for i := teamViewTeam; i <= teamViewFriction; i++ {
		cell := strconv.Itoa(i) + " " + teamViewNames[i]
		if i == s.view {
			cell = ">" + cell
		}
		cells = append(cells, cell)
	}
	return strings.Join(cells, " · ")
}

// RenderTeam renders the panel. Every line fits the width (display
// columns), and ANSI controls are stripped from all CLI output.
func RenderTeam(s *TeamState, width int) string {
	if s.targetErr != "" {
		return teamCut(StripPickerControls(s.targetErr), width) + "\n"
	}
	lines := []string{
		displaySlice("soho · "+s.target.WorkspaceID+" · "+s.target.Cwd, width),
		displaySlice(teamViewBar(s), width),
	}
	content, footer := s.renderContent(width)
	lines = append(lines, content...)
	lines = append(lines, displaySlice(footer, width))
	return strings.Join(lines, "\n") + "\n"
}

// renderContent returns the view's content lines (already scrolled and cut
// to the width) and its footer.
func (s *TeamState) renderContent(width int) ([]string, string) {
	switch s.subview {
	case "collect":
		var content []string
		title := "report: " + s.collectAgent + " (collect " + s.collectAgent + " --lines 60)"
		content = append(content, displaySlice(StripPickerControls(title), width))
		if !s.collectLoaded {
			content = append(content, displaySlice("carregando…", width))
		} else if s.collectFailure != "" {
			content = append(content, displaySlice(StripPickerControls(s.collectFailure), width))
		} else {
			content = append(content, teamTextBlock(s.collectLines, width)...)
		}
		content = scrollLines(content, s.scroll)
		return content, "↑/↓ PgUp/PgDn rolar · Esc voltar"
	case "confirm-release":
		prompt := fmt.Sprintf("release --close %s in %s (%s)? y/N", s.confirmAgent, s.target.WorkspaceID, s.target.Cwd)
		return []string{displaySlice(StripPickerControls(prompt), width)}, "y executa · n/Esc cancela"
	case "confirm-gc":
		prompt := fmt.Sprintf("gc --yes in %s? y/N", s.target.Cwd)
		return []string{displaySlice(StripPickerControls(prompt), width)}, "y executa · n/Esc cancela"
	}
	switch s.view {
	case teamViewTeam:
		var content []string
		if !s.viewLoaded[teamViewTeam-1] {
			content = append(content, displaySlice("carregando…", width))
		} else {
			if s.explainFailure != "" {
				content = append(content, displaySlice(StripPickerControls(s.explainFailure), width))
			} else {
				content = append(content, teamTextBlock(s.explainLines, width)...)
			}
			content = append(content, "")
			if s.rosterFailure != "" {
				content = append(content, displaySlice(StripPickerControls(s.rosterFailure), width))
			} else if len(s.workers) == 0 {
				content = append(content, displaySlice("sem workers", width))
			} else {
				for i, worker := range s.workers {
					cursor := " "
					if i == s.selected {
						cursor = ">"
					}
					content = append(content, displaySlice(cursor+" "+StripPickerControls(worker), width))
					if c, ok := s.collaborations[teamAgent(worker)]; ok {
						text := fmt.Sprintf("  %s · %s · rodada %d · versão %s · entrega %s", c.Assignment, c.Phase, c.Round, c.Revision, c.Delivery)
						if c.Status != "collaborating" {
							text = "  " + c.Status + ": " + c.Cause
						}
						content = append(content, teamWrap(StripPickerControls(text), width)...)
					}
				}
			}
			if s.statusFailure != "" {
				content = append(content, teamWrap(StripPickerControls(s.statusFailure), width)...)
			}
			if s.releaseAgent != "" {
				content = append(content, "")
				content = append(content, displaySlice("release --close "+s.releaseAgent+":", width))
				if s.releaseFailure != "" {
					content = append(content, displaySlice(StripPickerControls(s.releaseFailure), width))
				} else {
					content = append(content, teamTextBlock(s.releaseOut, width)...)
				}
			}
		}
		content = scrollLines(content, s.scroll)
		return content, "↑/↓ escolher · Enter relatório · x release --close · r recarregar · 1-4/Tab vistas · Esc sair"
	case teamViewDoctor:
		content := teamViewText(s.viewLoaded, teamViewDoctor, s.doctorFailure, s.doctorLines, width, &s.scroll)
		return content, "↑/↓ PgUp/PgDn rolar · r recarregar · 1-4/Tab vistas · Esc sair"
	case teamViewResources:
		var content []string
		if !s.viewLoaded[teamViewResources-1] {
			content = append(content, displaySlice("carregando…", width))
		} else {
			if s.gcFailure != "" {
				content = append(content, displaySlice(StripPickerControls(s.gcFailure), width))
			} else {
				content = append(content, teamTextBlock(s.gcLines, width)...)
			}
			if len(s.gcYesOut) > 0 || s.gcYesFailure != "" {
				content = append(content, "")
				content = append(content, displaySlice("gc --yes:", width))
				if s.gcYesFailure != "" {
					content = append(content, displaySlice(StripPickerControls(s.gcYesFailure), width))
				} else {
					content = append(content, teamTextBlock(s.gcYesOut, width)...)
				}
			}
		}
		content = scrollLines(content, s.scroll)
		return content, "g gc --yes · ↑/↓ PgUp/PgDn rolar · r recarregar · 1-4/Tab vistas · Esc sair"
	case teamViewFriction:
		content := teamViewText(s.viewLoaded, teamViewFriction, s.frictionFailure, s.frictionLines, width, &s.scroll)
		return content, "↑/↓ PgUp/PgDn rolar · r recarregar · 1-4/Tab vistas · Esc sair"
	}
	return nil, ""
}

func teamViewText(loaded [4]bool, view int, failure string, lines []string, width int, scroll *int) []string {
	if !loaded[view-1] {
		return []string{displaySlice("carregando…", width)}
	}
	var content []string
	if failure != "" {
		content = append(content, displaySlice(StripPickerControls(failure), width))
	} else {
		content = append(content, teamTextBlock(lines, width)...)
	}
	return scrollLines(content, *scroll)
}

// teamTextBlock renders stored CLI output lines (wrapped to the width); an
// empty output shows a placeholder.
func teamTextBlock(lines []string, width int) []string {
	if len(lines) == 0 {
		return []string{displaySlice("(sem saída)", width)}
	}
	var out []string
	for _, line := range lines {
		out = append(out, teamWrap(StripPickerControls(line), width)...)
	}
	return out
}

// scrollLines drops the first `offset` lines (the terminal shows the rest
// from the top).
func scrollLines(lines []string, offset int) []string {
	if offset <= 0 {
		return lines
	}
	if offset >= len(lines) {
		return []string{""}
	}
	return lines[offset:]
}

// teamConfirmQuiet is the quiet window a write confirmation waits before it
// accepts a `y`; teamNow is the clock (both replaceable in tests).
var (
	teamConfirmQuiet = 400 * time.Millisecond
	teamNow          = time.Now
)

// FeedChunk consumes one stdin read block and returns an action for the
// loop: "close" (the panel is exiting) or "load" (a load must be
// requested); "" means redraw only. Bracketed paste (ESC[200~ … ESC[201~)
// is discarded as a whole: no key inside it becomes an action in any view.
// A write confirmation accepts only a `y` that is the whole block: `xy` or
// `gy` (the key that opened it in the same block) and `yy`/`y\r` never
// confirm - the confirmation is a deliberate keypress on its own.
func (s *TeamState) FeedChunk(chunk string) string {
	confirming := s.subview == "confirm-release" || s.subview == "confirm-gc"
	action := s.feedChunk(chunk)
	if confirming && (s.subview == "confirm-release" || s.subview == "confirm-gc") {
		// The block reached the confirmation and did not confirm, for any
		// reason (a key, a paste, a `y` taken by an escape sequence): it
		// restarts the quiet window.
		s.confirmArmAt = teamNow().Add(teamConfirmQuiet)
	}
	return action
}

func (s *TeamState) feedChunk(chunk string) string {
	// A lone `y` confirms only when the parser is not inside an escape
	// sequence or a paste, which would take it as their own byte.
	soloY := chunk == "y" && !s.CSIPending && !s.EscPending && !s.inPaste
	// Without bracketed paste a long paste reaches us as several read
	// blocks, and its last block can be a lone `y`. So a confirmation also
	// needs a quiet window: a block that arrives before teamConfirmQuiet has
	// passed since it opened, or since the last block that could not
	// confirm, cannot confirm. A paste arrives in one burst and
	// never confirms; a `y` typed after reading the confirmation does.
	if s.subview == "confirm-release" || s.subview == "confirm-gc" {
		now := teamNow()
		if now.Before(s.confirmArmAt) {
			soloY = false
		}
	}
	for _, r := range chunk {
		ch := string(r)
		if s.CSIPending {
			if r >= 0x40 && r <= 0x7e {
				seq := s.csiBuf + ch
				s.CSIPending = false
				s.csiBuf = ""
				switch {
				case !s.inPaste && seq == "200~":
					s.inPaste = true
				case s.inPaste && seq == "201~":
					s.inPaste = false
				case !s.inPaste:
					switch seq {
					case "A":
						s.ApplyKey("up")
					case "B":
						s.ApplyKey("down")
					case "5~":
						s.ApplyKey("pgup")
					case "6~":
						s.ApplyKey("pgdn")
					}
				}
			} else {
				s.csiBuf += ch
			}
			if s.Exit != "" {
				return s.Exit
			}
			continue
		}
		if s.EscPending {
			s.EscPending = false
			if r == '[' {
				s.CSIPending = true
				continue
			}
			if !s.inPaste {
				if action := s.ApplyKey("esc"); action != "" {
					return action
				}
			}
			if s.Exit != "" {
				return s.Exit
			}
			continue
		}
		if s.inPaste {
			// Paste content: discarded; only the end marker is watched.
			if r == 0x1b {
				s.EscPending = true
			}
			continue
		}
		switch r {
		case 0x1b:
			s.EscPending = true
		case '\r', '\n':
			if action := s.ApplyKey("enter"); action != "" {
				return action
			}
		case 0x7f, 0x08:
			// No filter in the panel: backspace is ignored.
		case 0x03:
			if action := s.ApplyKey("ctrl-c"); action != "" {
				return action
			}
		case 0x09:
			if action := s.ApplyKey("tab"); action != "" {
				return action
			}
		case 'y':
			// A confirmation executes only on a `y` that is the whole read
			// block (see the FeedChunk doc).
			if soloY && (s.subview == "confirm-release" || s.subview == "confirm-gc") {
				if action := s.ApplyKey("y"); action != "" {
					return action
				}
			}
		default:
			if r >= 0x20 && r <= 0x7e {
				if action := s.ApplyKey(string(r)); action != "" {
					return action
				}
			}
		}
	}
	return ""
}

// FlushEsc resolves a pending Esc at end of input (the same contract as
// the board and the picker).
func (s *TeamState) FlushEsc() string {
	if !s.EscPending {
		return ""
	}
	s.EscPending = false
	if action := s.ApplyKey("esc"); action != "" {
		return action
	}
	return s.Exit
}

func (s *TeamState) ApplyKey(key string) string {
	if s.targetErr != "" {
		if key == "esc" {
			s.Exit = "close"
			return s.Exit
		}
		return ""
	}
	switch key {
	case "esc":
		switch s.subview {
		case "collect", "confirm-release", "confirm-gc":
			s.subview = ""
			return ""
		default:
			s.Exit = "close"
			return s.Exit
		}
	case "ctrl-c":
		s.Exit = "close"
		return s.Exit
	case "q":
		if s.subview == "" {
			s.Exit = "close"
			return s.Exit
		}
		return ""
	case "enter":
		if s.subview == "" && s.view == teamViewTeam && len(s.workers) > 0 {
			agent := teamAgent(s.workers[s.selected])
			if agent == "" {
				return ""
			}
			s.subview = "collect"
			s.collectAgent = agent
			s.collectLoaded = false
			s.collectLines = nil
			s.collectFailure = ""
			s.scroll = 0
			return "load"
		}
		return ""
	case "1", "2", "3", "4":
		view, _ := strconv.Atoi(key)
		if view == s.view {
			return ""
		}
		s.switchView(view)
		return "load"
	case "tab":
		next := s.view + 1
		if next > teamViewFriction {
			next = teamViewTeam
		}
		s.switchView(next)
		return "load"
	case "r":
		if s.subview == "collect" {
			s.collectLoaded = false
			s.scroll = 0
			return "load"
		}
		if s.subview == "" {
			s.viewLoaded[s.view-1] = false
			s.scroll = 0
			return "load"
		}
		return ""
	case "up", "down":
		if s.subview == "collect" || (s.subview == "" && s.view != teamViewTeam) {
			if key == "up" {
				s.scroll--
			} else {
				s.scroll++
			}
			if s.scroll < 0 {
				s.scroll = 0
			}
			return ""
		}
		if s.subview == "" && s.view == teamViewTeam && len(s.workers) > 0 {
			if key == "up" && s.selected > 0 {
				s.selected--
			}
			if key == "down" && s.selected < len(s.workers)-1 {
				s.selected++
			}
		}
		return ""
	case "pgup", "pgdn":
		if s.subview == "confirm-release" || s.subview == "confirm-gc" {
			return ""
		}
		if key == "pgup" {
			s.scroll -= teamViewScrollPage
		} else {
			s.scroll += teamViewScrollPage
		}
		if s.scroll < 0 {
			s.scroll = 0
		}
		return ""
	case "x":
		if s.subview == "" && s.view == teamViewTeam && len(s.workers) > 0 {
			if agent := teamAgent(s.workers[s.selected]); agent != "" {
				if _, active := s.collaborations[agent]; active {
					s.releaseAgent = agent
					s.releaseFailure = "colaboração ativa ou estado indisponível: o orquestrador deve inspecionar e finalizar antes de release"
					return ""
				}
				s.subview = "confirm-release"
				s.confirmAgent = agent
				s.confirmArmAt = teamNow().Add(teamConfirmQuiet)
			}
		}
		return ""
	case "g":
		if s.subview == "" && s.view == teamViewResources {
			s.subview = "confirm-gc"
			s.confirmArmAt = teamNow().Add(teamConfirmQuiet)
		}
		return ""
	case "y":
		switch s.subview {
		case "confirm-release":
			s.pendingRelease = s.confirmAgent
			s.subview = ""
			s.viewLoaded[teamViewTeam-1] = false
			s.scroll = 0
			return "load"
		case "confirm-gc":
			s.pendingGCYes = true
			s.subview = ""
			s.viewLoaded[teamViewResources-1] = false
			s.scroll = 0
			return "load"
		}
		return ""
	case "n":
		if s.subview == "confirm-release" || s.subview == "confirm-gc" {
			s.subview = ""
		}
		return ""
	default:
		return ""
	}
}

func (s *TeamState) switchView(view int) {
	s.view = view
	s.subview = ""
	s.scroll = 0
	s.viewLoaded[view-1] = false
}

// RunTeam opens the team panel: it resolves the focused target (like the
// bridge) and renders the four views over CLI calls. In production the CLI
// is the panel's own executable; tests pass a fake.
func RunTeam(env platform.Env, platformName, cliExe string, args []string) (code int) {
	view, err := teamInitialView(args)
	if err != nil {
		_, _ = fmt.Fprintf(platform.Stderr, "soho plugin: %s\n", err)
		return 2
	}
	stdin, stdout := os.Stdin, os.Stdout
	isTTY := false
	if info, err := stdin.Stat(); err == nil && info.Mode()&os.ModeCharDevice != 0 {
		isTTY = true
	}
	return withPickerTerminal(isTTY, func() (func() error, error) { return setPickerRaw(stdin) }, stdout, func() int {
		return runTeamLoop(env, platformName, cliExe, view, stdin, stdout, isTTY)
	})
}

func runTeamLoop(env platform.Env, platformName, cliExe string, initialView int, stdin, stdout *os.File, isTTY bool) int {
	shutdownSignals := make(chan os.Signal, 1)
	signal.Notify(shutdownSignals, pickerShutdownSignals()...)
	defer signal.Stop(shutdownSignals)
	return runTeamLoopWithSignals(env, platformName, cliExe, initialView, stdin, stdout, isTTY, shutdownSignals)
}

func runTeamLoopWithSignals(env platform.Env, platformName, cliExe string, initialView int, stdin, stdout *os.File, isTTY bool, shutdownSignals <-chan os.Signal) int {
	// Bracketed paste is on while the panel is open and off on every exit
	// path: the first output is the enable, the last the disable.
	_, _ = fmt.Fprint(stdout, "\x1b[?2004h")
	defer func() { _, _ = fmt.Fprint(stdout, "\x1b[?2004l") }()
	state := NewTeamState(initialView)
	target, cause := resolveTeamTarget(env, platformName)
	if cause != "" {
		state.targetErr = cause
	}
	if isTTY {
		_, _ = fmt.Fprint(stdout, "\x1b[?25l")
	}
	width := 80
	if isTTY {
		if n, err := pickerTerminalWidth(stdout.Fd()); err == nil && n > 0 {
			width = n
		}
	}
	redrawTeam(stdout, state, width)
	if state.targetErr != "" {
		// No valid target: show the cause on one line; Esc closes.
		return teamWaitForClose(stdin)
	}
	state.target = target
	teamCtx, cancelTeam := contextpkg.WithCancel(contextpkg.Background())
	loads := make(chan teamLoad, 1)
	results := make(chan teamLoadResult, 16)
	loadDone := make(chan struct{})
	go func() {
		defer close(results)
		defer close(loadDone)
		for {
			load, ok := <-loads
			if !ok {
				return
			}
			result := teamLoadResult{load: load}
			loadTeamView(teamCtx, cliExe, env, platformName, target, load, &result)
			select {
			case results <- result:
			case <-teamCtx.Done():
			}
		}
	}()
	defer func() {
		close(loads)
		cancelTeam()
		<-loadDone
	}()
	type inputResult struct {
		chunk string
		err   error
	}
	doneInput := make(chan struct{})
	defer close(doneInput)
	input := make(chan inputResult)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := stdin.Read(buf)
			select {
			case input <- inputResult{chunk: string(buf[:n]), err: err}:
			case <-doneInput:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	requestTeamLoad(loads, state.pendingLoad())
	var escTimer <-chan time.Time
	for {
		select {
		case <-shutdownSignals:
			state.ApplyKey("esc")
		case res, ok := <-results:
			if !ok {
				continue
			}
			applyTeamLoad(state, res)
			redrawTeam(stdout, state, width)
		case item := <-input:
			if len(item.chunk) > 0 {
				if state.FeedChunk(item.chunk) == "load" {
					requestTeamLoad(loads, state.pendingLoad())
				}
				if state.EscPending {
					escTimer = time.After(50 * time.Millisecond)
				} else {
					escTimer = nil
				}
				redrawTeam(stdout, state, width)
			}
			if item.err != nil {
				if item.err == io.EOF {
					// End of input closes the panel for good, from any view,
					// subview or confirmation; the children are ended like a
					// normal close.
					state.Exit = "close"
					break
				}
				return 1
			}
		case <-escTimer:
			if state.FlushEsc() != "" {
				break
			}
			redrawTeam(stdout, state, width)
		}
		if state.Exit != "" {
			break
		}
	}
	if state.Exit == "close" {
		return 0
	}
	return 1
}

// requestTeamLoad queues one load; if a load is already queued or running
// the queued one is replaced by the latest request (single producer: the
// main loop).
func requestTeamLoad(loads chan teamLoad, load teamLoad) {
	select {
	case loads <- load:
	default:
		select {
		case <-loads:
		default:
		}
		select {
		case loads <- load:
		default:
		}
	}
}

func redrawTeam(w io.Writer, state *TeamState, width int) {
	redrawArea(w, RenderTeam(state, width))
}

// teamWaitForClose keeps the invalid-target panel open until Esc (or end of
// input); arrow sequences are ignored and bracketed paste is discarded
// like in the views.
func teamWaitForClose(stdin *os.File) int {
	type inputResult struct {
		chunk string
		err   error
	}
	input := make(chan inputResult)
	done := make(chan struct{})
	defer close(done)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := stdin.Read(buf)
			select {
			case input <- inputResult{chunk: string(buf[:n]), err: err}:
			case <-done:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	escPending := false
	csiPending := false
	csiBuf := ""
	inPaste := false
	for item := range input {
		for _, r := range item.chunk {
			if csiPending {
				if r >= 0x40 && r <= 0x7e {
					seq := csiBuf + string(r)
					csiPending = false
					csiBuf = ""
					if !inPaste && seq == "200~" {
						inPaste = true
					} else if inPaste && seq == "201~" {
						inPaste = false
					}
				} else {
					csiBuf += string(r)
				}
				continue
			}
			if escPending {
				escPending = false
				if r == '[' {
					csiPending = true
					continue
				}
				if !inPaste {
					return 0 // Esc (plus anything) closes
				}
				continue
			}
			if inPaste {
				if r == 0x1b {
					escPending = true
				}
				continue
			}
			if r == 0x1b {
				escPending = true
			}
		}
		if item.err != nil {
			if item.err == io.EOF {
				return 0
			}
			return 1
		}
	}
	return 0
}
