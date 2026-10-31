package job

import (
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/backend"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/transport"
)

// ExitHerdr is a Herdr failure in the job family: a workspace create or
// close, an unreachable server, or a refused or failed pane command.
const ExitHerdr = 4

const defaultHerdrTimeout = 30 * time.Second

// herdrWorkspaces is the job's view of Herdr workspaces. The production
// adapter shells out to the herdr CLI; tests use an in-memory fake.
type herdrWorkspaces interface {
	Create(cwd, label string, env map[string]string) (workspaceID, rootPaneID string, err error)
	Close(workspaceID string) error
	ServerReachable() bool
	List() ([]herdrWorkspace, error)
}

// herdrWorkspace is one row of `herdr workspace list`: the id, the label
// and the pane count. Crash recovery reads it to find the job workspace
// by its label and to count its panes.
type herdrWorkspace struct {
	ID        string
	Label     string
	PaneCount int
}

// herdrPane is one row of `herdr pane list`: the pane id and the pane's
// revision, which the server advances as the pane changes. The automatic
// close watch (auto_close.go) reads both.
type herdrPane struct {
	PaneID   string
	Revision int
}

// herdrPanes is the job's view of Herdr panes.
type herdrPanes interface {
	Run(paneID string, argv []string) error
}

// herdrCLI implements herdrWorkspaces and herdrPanes through the herdr CLI:
// every call is one raw job step on the transport backend (the exact argv,
// the round-up context deadline, one attempt, and an error that never
// carries subprocess output). A zero Timeout means 30 s. A nil Friction drops
// friction messages and a nil RawFriction drops raw friction lines.
type herdrCLI struct {
	Env         platform.Env
	Timeout     time.Duration
	Friction    func(message string)
	RawFriction func(message string)
}

func newHerdrCLI(env platform.Env, friction func(string)) *herdrCLI {
	return &herdrCLI{Env: env, Friction: friction}
}

var (
	_ herdrWorkspaces = (*herdrCLI)(nil)
	_ herdrPanes      = (*herdrCLI)(nil)
)

// legacy returns the job's one raw job-step surface: the transport backend
// built on the adapter's env, whose methods run the exact one-step herdr
// calls the adapter used to make through runStep.
func (c *herdrCLI) legacy() transport.LegacyTransport {
	return backend.New(c.Env).Legacy
}

func (c *herdrCLI) timeout() time.Duration {
	if c.Timeout <= 0 {
		return defaultHerdrTimeout
	}
	return c.Timeout
}

func herdrExit(msg string) error {
	return &ExitError{Code: ExitHerdr, Msg: msg}
}

// Create opens the job's Herdr workspace:
// `herdr workspace create --cwd <cwd> --label <label> [--env KEY=VALUE ...]
// --no-focus`, with the --env pairs sorted by key. A non-zero exit, a
// timeout, invalid JSON, or a missing id or pane is exit 4.
func (c *herdrCLI) Create(cwd, label string, env map[string]string) (string, string, error) {
	rep := c.legacy().WorkspaceCreateRaw(context.Background(), cwd, label, env, transport.Call{Timeout: c.timeout()})
	if rep.Err != nil {
		return "", "", herdrExit("job: herdr workspace create failed")
	}
	workspaceID, rootPaneID, ok := parseWorkspaceCreateResult(rep.Value)
	if !ok {
		// The raw envelope goes only to the raw friction sink, sanitized
		// and cut; when that sink is absent it is dropped. It never
		// reaches stdout, stderr, the error message, or the normal
		// friction callback.
		if c.RawFriction != nil {
			c.RawFriction("job: herdr workspace create returned an unknown result: " + cutFriction(core.FrictionSafe(rep.Value)))
		}
		return "", "", herdrExit("job: herdr workspace create returned an unknown result")
	}
	return workspaceID, rootPaneID, nil
}

// parseWorkspaceCreateResult reads the workspace id from
// result.workspace.workspace_id, else result.workspace_id, and the root pane
// from result.root_pane.pane_id, else result.workspace.root_pane.pane_id,
// else result.tab.root_pane.pane_id (the same fallback idea as
// herdr.TabCreate).
//
// TODO(DJA-194): verify the herdr workspace create result shape on a real server (premise 1).
func parseWorkspaceCreateResult(out string) (string, string, bool) {
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		return "", "", false
	}
	result, ok := doc["result"].(map[string]any)
	if !ok {
		return "", "", false
	}
	workspace, _ := result["workspace"].(map[string]any)
	tab, _ := result["tab"].(map[string]any)
	workspaceID := herdrMapString(workspace, "workspace_id")
	if workspaceID == "" {
		workspaceID = herdrString(result["workspace_id"])
	}
	rootPaneID := herdrRootPaneID(result)
	if rootPaneID == "" {
		rootPaneID = herdrRootPaneID(workspace)
	}
	if rootPaneID == "" {
		rootPaneID = herdrRootPaneID(tab)
	}
	if workspaceID == "" || rootPaneID == "" {
		return "", "", false
	}
	return workspaceID, rootPaneID, true
}

// validWorkspaceID reports whether id is one safe path segment for the job
// state root and one safe argument for `herdr workspace close`: non-empty,
// at most 128 bytes, not "." or "..", with no path separator ("/", "\",
// ":" cover separators, rooted, drive and UNC forms, and alternate data
// streams), no byte below 0x20 or equal to 0x7f (NUL included), no leading
// "-" (a flag to the close argv), no leading or trailing space and no
// trailing "." (Windows strips them, aliasing another name), and local to
// the filesystem (not absolute, rooted, or a reserved device name such as
// NUL or CON on Windows).
func validWorkspaceID(id string) bool {
	if id == "" || len(id) > 128 || id == "." || id == ".." {
		return false
	}
	if strings.ContainsAny(id, `/\\:`) {
		return false
	}
	for i := 0; i < len(id); i++ {
		if id[i] < 0x20 || id[i] == 0x7f {
			return false
		}
	}
	if id[0] == '-' || id[0] == ' ' || id[len(id)-1] == ' ' || id[len(id)-1] == '.' {
		return false
	}
	return filepath.IsLocal(id)
}

// Close ends the job's Herdr workspace. An id that is not one safe path
// segment — in particular the empty id — refuses with exit 2 before
// anything runs.
func (c *herdrCLI) Close(workspaceID string) error {
	if !validWorkspaceID(workspaceID) {
		return errUsage("job: invalid workspace id")
	}
	if rep := c.legacy().WorkspaceCloseRaw(context.Background(), workspaceID, transport.Call{Timeout: c.timeout()}); rep.Err != nil {
		return herdrExit("job: herdr workspace close failed")
	}
	return nil
}

// ServerReachable queries only `herdr workspace list` and reports true when
// it exits 0 and the stdout parses as a JSON object with a result key. It
// never runs `herdr server` or any other command that could start a
// server.
//
// TODO(DJA-194): verify on Windows that an SSH session reaches the desktop-session server and that workspace list never starts one (premise 1).
func (c *herdrCLI) ServerReachable() bool {
	rep := c.legacy().WorkspaceListRaw(context.Background(), transport.Call{Timeout: c.timeout()})
	if rep.Err != nil {
		return false
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(rep.Value), &doc); err != nil {
		return false
	}
	_, ok := doc["result"]
	return ok
}

// List reads `herdr workspace list`: result.workspaces[], each with
// workspace_id, label and pane_count. A non-zero exit, a timeout, invalid
// JSON, a missing workspaces array, or a row without a string
// workspace_id, a string label or a non-negative integer pane_count is
// exit 4: a row the job cannot read makes the whole list unreadable, so
// recovery never decides from a partial list. Like ServerReachable, it
// never runs a command that could start a server.
func (c *herdrCLI) List() ([]herdrWorkspace, error) {
	rep := c.legacy().WorkspaceListRaw(context.Background(), transport.Call{Timeout: c.timeout()})
	if rep.Err != nil {
		return nil, herdrExit("job: herdr workspace list failed")
	}
	rows, ok := parseWorkspaceList(rep.Value)
	if !ok {
		return nil, herdrExit("job: herdr workspace list returned an unknown result")
	}
	return rows, nil
}

func parseWorkspaceList(out string) ([]herdrWorkspace, bool) {
	var doc struct {
		Result *struct {
			Workspaces *[]map[string]any `json:"workspaces"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil || doc.Result == nil || doc.Result.Workspaces == nil {
		return nil, false
	}
	rows := make([]herdrWorkspace, 0, len(*doc.Result.Workspaces))
	for _, item := range *doc.Result.Workspaces {
		id, idOK := item["workspace_id"].(string)
		label, labelOK := item["label"].(string)
		count, countOK := item["pane_count"].(float64)
		if !idOK || id == "" || !labelOK || !countOK || count < 0 || count != float64(int(count)) {
			return nil, false
		}
		rows = append(rows, herdrWorkspace{ID: id, Label: label, PaneCount: int(count)})
	}
	return rows, true
}

// Panes lists a workspace's panes:
// `herdr pane list --workspace <workspaceID>`, reading result.panes[],
// each with a string pane_id and a non-negative integer revision. An id
// that is not one safe path segment — in particular the empty id —
// refuses with exit 2 before anything runs, like Close. A non-zero exit,
// a timeout, invalid JSON, a missing panes array, or a row without a
// string pane_id or a non-negative integer revision is exit 4: a row the
// job cannot read makes the whole list unreadable, so the watch never
// decides from a partial list.
func (c *herdrCLI) Panes(workspaceID string) ([]herdrPane, error) {
	if !validWorkspaceID(workspaceID) {
		return nil, errUsage("job: invalid workspace id")
	}
	rep := c.legacy().PaneListRaw(context.Background(), workspaceID, transport.Call{Timeout: c.timeout()})
	if rep.Err != nil {
		return nil, herdrExit("job: herdr pane list failed")
	}
	rows, ok := parsePanes(rep.Value)
	if !ok {
		return nil, herdrExit("job: herdr pane list returned an unknown result")
	}
	return rows, nil
}

// parsePanes reads result.panes[]: a string pane_id and a non-negative
// integer revision per row, every other field ignored. Anything else —
// invalid JSON, a missing result or panes, or a row missing either field
// or with a bad type — makes the whole list unreadable.
func parsePanes(out string) ([]herdrPane, bool) {
	var doc struct {
		Result *struct {
			Panes *[]map[string]any `json:"panes"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil || doc.Result == nil || doc.Result.Panes == nil {
		return nil, false
	}
	rows := make([]herdrPane, 0, len(*doc.Result.Panes))
	for _, item := range *doc.Result.Panes {
		id, idOK := item["pane_id"].(string)
		revision, revisionOK := item["revision"].(float64)
		if !idOK || id == "" || !revisionOK || revision < 0 || revision != float64(int(revision)) {
			return nil, false
		}
		rows = append(rows, herdrPane{PaneID: id, Revision: int(revision)})
	}
	return rows, true
}

// safePaneRunArg matches one `herdr pane run` argument or pane id: a
// non-empty sequence of letters, digits, and . _ / : \ = -, plus 8.3
// short-name tildes (a letter or digit, then ~, then one or more digits),
// which Windows uses in executable paths such as C:\Users\USER~1\.... A ~
// outside that form is refused: shell tilde expansion only happens at the
// start of a word (POSIX and PowerShell), and cmd.exe reads ~ only inside
// %...%, which stays refused.
var safePaneRunArg = regexp.MustCompile(`^(?:[A-Za-z0-9._/:\\=-]|[A-Za-z0-9]~[0-9]+)+$`)

// Run starts an argv in an existing pane:
// `herdr pane run <paneID> <argv...>`. It refuses an empty paneID or any
// empty or unsafe argument with exit 4 before anything runs, because
// whether pane run re-reads the command through the pane's shell is
// unverified.
//
// TODO(DJA-194): verify that herdr pane run passes argv to a pane at a shell prompt without shell re-interpretation (premise 3).
func (c *herdrCLI) Run(paneID string, argv []string) error {
	if !safePaneRunArg.MatchString(paneID) {
		return herdrExit("job: herdr pane run refused an unsafe argument")
	}
	for _, arg := range argv {
		if !safePaneRunArg.MatchString(arg) {
			return herdrExit("job: herdr pane run refused an unsafe argument")
		}
	}
	if rep := c.legacy().PaneRunRaw(context.Background(), paneID, argv, transport.Call{Timeout: c.timeout()}); rep.Err != nil {
		return herdrExit("job: herdr pane run failed")
	}
	return nil
}

// herdrString reports value as a string; anything else is the empty string.
func herdrString(value any) string {
	s, _ := value.(string)
	return s
}

func herdrMapString(obj map[string]any, key string) string {
	if obj == nil {
		return ""
	}
	return herdrString(obj[key])
}

func herdrRootPaneID(obj map[string]any) string {
	root, ok := obj["root_pane"].(map[string]any)
	if !ok {
		return ""
	}
	return herdrMapString(root, "pane_id")
}

// cutFriction caps a friction fragment at 2000 bytes.
func cutFriction(s string) string {
	if len(s) > 2000 {
		return s[:2000]
	}
	return s
}
