package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

// team_ops.go composes the CLI's own team commands for the job supervisor:
// the lane it guarantees, the job orchestrator it spawns, dispatches,
// watches and releases. The production adapter runs this executable's
// subcommands as argv subprocesses behind runStepIn with the job worktree
// as working directory, so the CLI's rules stay the shared implementation.
// Its errors are fixed strings that name the step; subprocess output never
// reaches them.

const defaultTeamOpsTimeout = 120 * time.Second

// teamOps is the team surface the supervisor drives from the job worktree.
type teamOps interface {
	EnsureJobLane() error
	SpawnOrchestrator() (name string, err error)
	Dispatch(name, briefPath string, amend bool) error
	Send(name, path string) error
	Status(name string) (state, reportPath string, err error)
	Release(name string) error
	ReleaseTeam() error
	GC() error
	Roster() ([]rosterRow, error)
}

// rosterRow is one rostered worker row the supervisor reads for spawn
// recovery: the identity columns it needs, in the order the roster prints
// them (NAME ROLE KIND PANE ...). Liveness is not a roster column: a
// spaced tab label (the TAB column, a padded label that may carry an
// " N" suffix) shifts the whitespace-separated columns after it, so the
// supervisor reads each candidate's state from its status instead.
type rosterRow struct {
	Name string
	Role string
	Pane string
}

// selfCLI is the production teamOps: one soho executable, the job
// worktree as working directory, one bounded timeout per command (a zero
// Timeout is 120 s) and, when set, one platform context every command runs
// under (a cancelled context ends the command the adapter started, and
// only that one).
type selfCLI struct {
	Exe     string
	Dir     string
	Env     platform.Env
	Timeout time.Duration
	// Ctx, when non-nil, is the platform context of every command the
	// adapter runs: a cancel of it ends only the subprocesses this adapter
	// opened (the platform cancels the command it started, never another
	// process tree). The supervisor sets it on the in-flight delivery the
	// pending cancel interrupts.
	Ctx context.Context
	// ceiling is the lowest bound a bounded() call asked for: no command,
	// the spawn's longer default included, runs past it. Zero is none.
	ceiling time.Duration
}

func (c selfCLI) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return defaultTeamOpsTimeout
}

// bounded is the same adapter with every command timeout lowered to at most
// d (never raised above its own bound): the terminal procedure's collaboration
// step runs its dispatches and roster reads through it, so one command
// cannot outlast the step's remaining time. A zero or negative d keeps the
// adapter's own bound: the stop path uses it for the spent-remainder case,
// where the command still has its own deadline.
func (c selfCLI) bounded(d time.Duration) teamOps {
	if d > 0 && d < c.timeout() {
		c.Timeout = d
	}
	if d > 0 && (c.ceiling <= 0 || d < c.ceiling) {
		c.ceiling = d
	}
	return c
}

// interruptible is the same adapter with its commands running under ctx: the
// platform context the supervisor cancels when a cancel arrives while a
// delivery is in flight, so the in-flight subprocess the adapter opened is
// the one that ends. A nil ctx keeps the adapter as is.
func (c selfCLI) interruptible(ctx context.Context) teamOps {
	if ctx != nil {
		c.Ctx = ctx
	}
	return c
}

// spawnTimeout bounds the spawn step. The spawn itself waits up to
// spawn_timeout for the agent to start, so the default adapter allows that
// wait on top of its own bound for the rest of the spawn; a slow machine
// raises spawn_timeout, not a fixed job constant. An explicit Timeout (a
// test) stays the bound, a bounded() ceiling caps it, and a configuration
// that cannot be read keeps the team-ops bound: the supervisor must not die
// over it.
func (c selfCLI) spawnTimeout() time.Duration {
	bound := c.Timeout
	if bound <= 0 {
		bound = configuredSpawnBound(c.Env, c.Dir)
	}
	if c.ceiling > 0 && bound > c.ceiling {
		bound = c.ceiling
	}
	return bound
}

// maxSpawnTimeoutMS caps the spawn_timeout the spawn step adds: 24 h, the
// ceiling of job_timeout, since no job outlives it. A larger value
// saturates to it, so the millisecond deadline the platform converts back
// to a time.Duration never overflows into "no deadline".
const maxSpawnTimeoutMS = int64(24 * time.Hour / time.Millisecond)

// configuredSpawnBound is the team-ops bound plus the job worktree's
// spawn_timeout.
func configuredSpawnBound(env platform.Env, dir string) (bound time.Duration) {
	defer func() {
		if value := recover(); value != nil {
			if _, ok := value.(*platform.ExitError); !ok {
				panic(value)
			}
			bound = defaultTeamOpsTimeout
		}
	}()
	cfg := core.LoadConfig(env, dir)
	ms, ok := core.ParseTimeoutMS(core.Cfg(&cfg, "spawn_timeout", "60000", env))
	if !ok || ms <= 0 {
		return defaultTeamOpsTimeout
	}
	if ms > maxSpawnTimeoutMS {
		ms = maxSpawnTimeoutMS
	}
	return defaultTeamOpsTimeout + time.Duration(ms)*time.Millisecond
}

// step runs one self subprocess and maps any failure to the fixed error of
// the operation.
func (c selfCLI) step(op string, args ...string) (string, error) {
	return c.stepWithin(op, c.timeout(), args...)
}

// stepWithin is step with its own bound.
func (c selfCLI) stepWithin(op string, timeout time.Duration, args ...string) (string, error) {
	out, detail, err := runStepDetailInCtx(c.Dir, c.Exe, args, timeout, c.Env, c.Ctx)
	if err != nil {
		e := teamOpsError(op).(*ExitError)
		e.Detail = detail
		return "", e
	}
	return out, nil
}

// runStepInCtx is runStepIn (prepare.go) with the platform context of the
// subprocess: a cancel of ctx ends only the command the runner opened. A
// nil ctx runs without one, exactly like runStepIn.
func runStepInCtx(dir, exe string, args []string, timeout time.Duration, env platform.Env, ctx context.Context) (string, error) {
	out, _, err := runStepDetailInCtx(dir, exe, args, timeout, env, ctx)
	return out, err
}

// runStepDetailInCtx is runStepInCtx plus, on failure, a short diagnostic:
// how the subprocess ended and the last lines of its stderr, for the job's
// friction log only.
func runStepDetailInCtx(dir, exe string, args []string, timeout time.Duration, env platform.Env, ctx context.Context) (string, string, error) {
	opts := platform.RunOptions{Context: ctx, Env: env, Cwd: dir, TimeoutMs: timeoutMs(timeout)}
	var result platform.RunResult
	if filepath.IsAbs(exe) {
		result = platform.RunExecutable(exe, args, opts)
	} else {
		result = platform.RunCli(exe, args, opts)
	}
	if result.NotFound || result.TimedOut || result.Status == nil || *result.Status != 0 {
		return "", stepFailureDetail(result), errSubprocess
	}
	return result.Stdout, "", nil
}

// stepFailureDetail says how a failed step ended and keeps the last three
// non-empty stderr lines, joined with " | " and cut at 400 bytes.
func stepFailureDetail(result platform.RunResult) string {
	how := "no exit status"
	switch {
	case result.NotFound:
		how = "executable not found"
	case result.TimedOut:
		how = "timed out"
	case result.Status != nil:
		how = fmt.Sprintf("exit %d", *result.Status)
	}
	var tail []string
	for _, line := range strings.Split(strings.ReplaceAll(result.Stderr, "\r\n", "\n"), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			tail = append(tail, line)
		}
	}
	if len(tail) > 3 {
		tail = tail[len(tail)-3:]
	}
	detail := how
	if len(tail) > 0 {
		detail += ": " + redactDetail(strings.Join(tail, " | "))
	}
	if len(detail) > 400 {
		detail = detail[:400]
	}
	return detail
}

// teamOpsError is the fixed error of one failed operation; the subprocess
// output stays out of it.
func teamOpsError(op string) error {
	return &ExitError{Code: ExitHerdr, Msg: "job: cannot " + op}
}

// EnsureJobLane guarantees the lane the job orchestrator runs in: the lane
// role and its single pane, and — only when the config carries an explicit
// max_workers value (a source other than defaults) — a bump of the worker
// cap by one. It is idempotent: session set rewrites the same keys.
func (c selfCLI) EnsureJobLane() error {
	if _, err := c.step("ensure the job lane", "session", "set", "lane.job.roles", "job-orchestrator"); err != nil {
		return err
	}
	if _, err := c.step("ensure the job lane", "session", "set", "lane.job.panes", "1"); err != nil {
		return err
	}
	out, err := c.step("ensure the job lane", "config")
	if err != nil {
		return err
	}
	value, explicit, err := parseMaxWorkers(out)
	if err != nil {
		return teamOpsError("ensure the job lane")
	}
	if explicit {
		if _, err := c.step("ensure the job lane", "session", "set", "max_workers", strconv.Itoa(value+1)); err != nil {
			return err
		}
	}
	return nil
}

// parseMaxWorkers reads the max_workers row of the config output: the key,
// the value and the source are whitespace-separated, and the source names
// the layer the value came from. An absent row or a non-integer value is an
// error; explicit is true when the source is not defaults.
func parseMaxWorkers(output string) (value int, explicit bool, err error) {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] != "max_workers" {
			continue
		}
		n, parseErr := strconv.Atoi(fields[1])
		if parseErr != nil {
			return 0, false, errors.New("max_workers value is not an integer")
		}
		return n, fields[2] != "defaults", nil
	}
	return 0, false, errors.New("no max_workers row")
}

// SpawnOrchestrator spawns the job orchestrator in the job worktree and
// returns its agent name: the name field of the last JSON object the spawn
// prints. The real spawn indents the object over several lines, so the
// candidate is the last line that opens an object, joined to the end.
func (c selfCLI) SpawnOrchestrator() (string, error) {
	out, err := c.stepWithin("spawn the job orchestrator", c.spawnTimeout(), "spawn", "job-orchestrator", "--cwd", c.Dir, "--approvals", "full", "--fresh")
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if !strings.HasPrefix(strings.TrimSpace(lines[i]), "{") {
			continue
		}
		object, err := parseJSONObject(strings.Join(lines[i:], "\n"))
		if err != nil {
			continue
		}
		name, _ := object["name"].(string)
		if name == "" {
			return "", teamOpsError("spawn the job orchestrator")
		}
		return name, nil
	}
	return "", teamOpsError("spawn the job orchestrator")
}

// Dispatch sends the brief to the agent without waiting; amend re-sends it
// as an amendment.
func (c selfCLI) Dispatch(name, briefPath string, amend bool) error {
	args := []string{"dispatch", name, briefPath, "--no-wait"}
	if amend {
		args = append(args, "--amend")
	}
	_, err := c.step("dispatch the job orchestrator", args...)
	return err
}

// Send delivers a note to the agent from the file at path, as the CLI's
// send with --file and no --now: the prompt is submitted for the agent's
// CLI to queue or steer. A non-zero exit is a delivery failure.
func (c selfCLI) Send(name, path string) error {
	_, err := c.step("send to the job orchestrator", "send", name, "--file", path)
	return err
}

// Status reads the agent's state: the state is the second tab-separated
// field of the first stdout line whose first field is the agent name, and
// the report path the third field. A non-zero exit still returns the
// parsed state when one is present (status exits non-zero for blocked,
// gone, quota and the like); no line is an error. The question, quota and
// provider states print a JSON object instead of a tab line, so a line that
// carries the agent in its agent field is read the same way.
func (c selfCLI) Status(name string) (string, string, error) {
	opts := platform.RunOptions{Context: c.Ctx, Env: c.Env, Cwd: c.Dir, TimeoutMs: timeoutMs(c.timeout())}
	var result platform.RunResult
	if filepath.IsAbs(c.Exe) {
		result = platform.RunExecutable(c.Exe, []string{"status", name}, opts)
	} else {
		result = platform.RunCli(c.Exe, []string{"status", name}, opts)
	}
	if result.NotFound || result.TimedOut || result.Status == nil {
		return "", "", teamOpsError("read the job orchestrator status")
	}
	for _, line := range strings.Split(result.Stdout, "\n") {
		if state, report, ok := parseStatusLine(line, name); ok {
			return state, report, nil
		}
	}
	return "", "", teamOpsError("read the job orchestrator status")
}

// parseStatusLine parses one status line for the agent: the tab form first,
// then the JSON form the question, quota and provider states print.
func parseStatusLine(line, name string) (state, report string, ok bool) {
	fields := strings.Split(line, "\t")
	if len(fields) >= 2 && fields[0] == name && fields[1] != "" {
		if len(fields) >= 3 {
			report = fields[2]
		}
		return fields[1], report, true
	}
	object, err := parseJSONObject(line)
	if err != nil {
		return "", "", false
	}
	agent, _ := object["agent"].(string)
	if agent != name {
		return "", "", false
	}
	state, _ = object["status"].(string)
	if state == "" {
		return "", "", false
	}
	report, _ = object["report"].(string)
	return state, report, true
}

// Release closes the agent's pane and force-releases it.
func (c selfCLI) Release(name string) error {
	_, err := c.step("release the job orchestrator", "release", name, "--close", "--force")
	return err
}

// ReleaseTeam releases every worker the roster lists except the job
// orchestrator, which the supervisor releases on its own: it reads the
// roster and force-releases the listed names one by one, trying every
// listed worker even after one release fails. The header row and the
// "other live agents" block are skipped; a failed roster read is the fixed
// error returned at once, and a failed release is the fixed error returned
// after the last listed worker is tried.
func (c selfCLI) ReleaseTeam() error {
	out, err := c.step("release the team workers", "roster")
	if err != nil {
		return err
	}
	var failed error
	for _, line := range strings.Split(out, "\n") {
		// The "other live agents" block opens with a comment line and runs
		// to the end of the output (including the layout line); nothing
		// after it is a rostered worker.
		if strings.HasPrefix(line, "#") {
			break
		}
		fields := strings.Fields(line)
		// The header row and the job orchestrator's row (its role column
		// is job-orchestrator) are not team workers.
		if len(fields) < 2 || fields[0] == "NAME" || fields[1] == "job-orchestrator" {
			continue
		}
		if _, err := c.step("release the team workers", "release", fields[0], "--close", "--force"); err != nil && failed == nil {
			failed = err
		}
	}
	return failed
}

// Roster reads the team roster for spawn recovery: the header row, one
// row per rostered worker, and the "other live agents" block that runs to
// the end of the output. A failed roster call or a row with fewer than
// four fields is the fixed error — the recovery never decides from a
// partial roster.
func (c selfCLI) Roster() ([]rosterRow, error) {
	out, err := c.step("read the team roster", "roster")
	if err != nil {
		return nil, err
	}
	rows, err := parseRoster(out)
	if err != nil {
		return nil, teamOpsError("read the team roster")
	}
	return rows, nil
}

// parseRoster parses the roster output: the header row starts with NAME
// and is skipped, the whitespace-separated rows are the rostered workers,
// and the block that opens with a line beginning # (the other live agents)
// runs to the end of the output. Only the identity columns are read —
// NAME (field 0), ROLE (field 1) and PANE (field 3) — and a row before
// that block with fewer than four fields is an error. They are exact for a
// job-orchestrator row: the ROLE column carries a (history) suffix only
// when the whole text fits in 18 characters, which job-orchestrator
// (16) plus the shortest suffix never does, and the name, kind and pane
// are single tokens, so only a column at field 4 or later (a spaced tab
// label, a cwd with spaces) can shift the rest of the row, never the
// first four.
func parseRoster(out string) ([]rosterRow, error) {
	var rows []rosterRow
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "#") {
			break
		}
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] == "NAME" {
			continue
		}
		if len(fields) < 4 {
			return nil, errors.New("roster row has fewer than four fields")
		}
		rows = append(rows, rosterRow{Name: fields[0], Role: fields[1], Pane: fields[3]})
	}
	return rows, nil
}

// GC runs the job's garbage collection over its registered copies and
// processes.
func (c selfCLI) GC() error {
	_, err := c.step("gc the job copies and processes", "gc", "--yes")
	return err
}

// parseJSONObject decodes one JSON object; a non-object or an undecodable
// input is an error.
func parseJSONObject(raw string) (map[string]any, error) {
	var object map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &object); err != nil || object == nil {
		return nil, errors.New("not a JSON object")
	}
	return object, nil
}
