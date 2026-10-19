// Package herdr is the only Go client for the Herdr CLI.
package herdr

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/codexenv"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/text"
)

const Timeout = 30 * time.Second

var DefaultRetryPauses = []time.Duration{time.Second, 2 * time.Second}

const notRunning = "not running inside Herdr (HERDR_ENV != 1); refusing to control a session from outside"

type AgentStateResult struct {
	State string
	Cause string
	Seq   any
}
type PaneLayoutResult = platform.RunResult
type TabGetResult struct {
	Ok    bool
	Label string
	Root  string
}
type TabCreateResult struct {
	Ok   bool
	Tab  string
	Root string
}
type PaneSplitResult struct {
	Ok   bool
	Pane string
}
type PaneMoveResult struct {
	Ok   bool
	Pane string
	Tab  string
}
type AgentPromptResult struct {
	Ok  bool
	Raw string
}

func run(args []string, env platform.Env) platform.RunResult {
	return runWithTimeout(args, env, Timeout)
}
func runWithTimeout(args []string, env platform.Env, timeout time.Duration) platform.RunResult {
	return platform.RunCli("herdr", args, platform.RunOptions{Env: env, TimeoutMs: int(timeout / time.Millisecond)})
}
func parsed(s string) any { v, _ := jsonjs.Parse([]byte(s)); return v }
func get(v any, keys ...string) any {
	for _, k := range keys {
		o, ok := v.(*jsonjs.Object)
		if !ok {
			return nil
		}
		v, _ = o.Get(k)
	}
	return v
}
func str(v any) string {
	if v == false {
		return ""
	}
	return jsString(v)
}
func ok(r platform.RunResult) bool { return !r.NotFound && r.Status != nil && *r.Status == 0 }
func dieError(r platform.RunResult) int {
	if r.NotFound {
		return 127
	}
	if r.Status != nil {
		return *r.Status
	}
	if r.Signal != "" {
		return 128 + signalNumber(r.Signal)
	}
	return 1
}
func timeoutMsg(what string, d time.Duration) string {
	return fmt.Sprintf("herdr %s timed out after %gs", what, d.Seconds())
}

// RequireEnv verifies the Herdr marker and CLI, using the Codex ancestry
// diagnostic when the marker was stripped by shell_environment_policy.
func RequireEnv(env platform.Env, goos string, pid int, ancestors []codexenv.Process) {
	if env.Get("HERDR_ENV") != "1" {
		platform.Die(codexenv.DiagnoseOutsideHerdr(notRunning, env, goos, pid, ancestors), 2)
	}
	if _, found := platform.FindExecutable("herdr", env, goos); !found {
		platform.Die("herdr CLI not found in PATH", 2)
	}
}

func LiveAgents(env platform.Env, timeout time.Duration) []any {
	if timeout <= 0 {
		timeout = Timeout
	}
	r := runWithTimeout([]string{"agent", "list"}, env, timeout)
	if r.NotFound {
		platform.DieFriction("herdr CLI not found in PATH", 2)
	}
	if r.TimedOut {
		platform.DieFriction(timeoutMsg("agent list", timeout), 4)
	}
	if !ok(r) {
		if r.Stdout != "" {
			_, _ = platform.Stdout.Write([]byte(r.Stdout))
		}
		if r.Stderr != "" {
			_, _ = platform.Stderr.Write([]byte(r.Stderr))
		}
		platform.Die("", dieError(r))
	}
	agents := get(parsed(r.Stdout), "result", "agents")
	if arr, yes := agents.([]any); yes {
		return arr
	}
	platform.DieFriction("herdr agent list returned no agent list", 4)
	return nil
}
func PaneList(env platform.Env, ws string) []any {
	r := run([]string{"pane", "list", "--workspace", ws}, env)
	if !ok(r) {
		return []any{}
	}
	if a, yes := get(parsed(r.Stdout), "result", "panes").([]any); yes {
		return a
	}
	return []any{}
}
func PaneListAll(env platform.Env) []any {
	r := run([]string{"pane", "list"}, env)
	if !ok(r) {
		return []any{}
	}
	if a, yes := get(parsed(r.Stdout), "result", "panes").([]any); yes {
		return a
	}
	return []any{}
}
func TabList(env platform.Env, ws string) []any {
	r := run([]string{"tab", "list", "--workspace", ws}, env)
	if !ok(r) {
		return []any{}
	}
	if a, yes := get(parsed(r.Stdout), "result", "tabs").([]any); yes {
		return a
	}
	return []any{}
}

// AgentReadOK is AgentRead that also reports whether the read succeeded, for
// callers that must tell an empty screen from a failed read.
func AgentReadOK(env platform.Env, agent, source string, lines *int) (string, bool) {
	r := agentRead(env, agent, source, lines)
	if !ok(r) {
		return "", false
	}
	return r.Stdout, true
}

// agentRead runs `herdr agent read`. A full-screen TUI (opencode) keeps its
// history on the alternate screen, which Herdr captures only while the agent
// is idle: a `recent` read of a working one fails with agent_not_idle. The
// visible screen is what such an agent shows, so that read takes its place;
// without it every check on a working opencode read an empty screen (the
// prompt of a dispatch or amendment, a quota or auth error).
func agentRead(env platform.Env, agent, source string, lines *int) platform.RunResult {
	args := []string{"agent", "read", agent, "--source", source}
	if lines != nil {
		args = append(args, "--lines", fmt.Sprint(*lines))
	}
	r := run(args, env)
	if ok(r) || !strings.HasPrefix(source, "recent") {
		return r
	}
	raw := r.Stderr
	if raw == "" {
		raw = r.Stdout
	}
	if code, _ := errorInfo(raw); code != "agent_not_idle" {
		return r
	}
	return run([]string{"agent", "read", agent, "--source", "visible"}, env)
}

func AgentRead(env platform.Env, agent, source string, lines *int) string {
	r := agentRead(env, agent, source, lines)
	if !ok(r) {
		return ""
	}
	return r.Stdout
}

// PaneRead runs `herdr pane read <pane> --source <source> [--lines N]`. It
// reads the pane itself, so it works before (or without) a registered agent:
// a start that failed has no agent to read. ok is false when the read fails.
func PaneRead(env platform.Env, pane, source string, lines *int) (string, bool) {
	args := []string{"pane", "read", pane, "--source", source}
	if lines != nil {
		args = append(args, "--lines", fmt.Sprint(*lines))
	}
	r := run(args, env)
	if !ok(r) {
		return "", false
	}
	return r.Stdout, true
}
func AgentState(target string, env platform.Env, timeout time.Duration, retryPauses []time.Duration) AgentStateResult {
	if timeout <= 0 {
		timeout = Timeout
	}
	if retryPauses == nil {
		retryPauses = DefaultRetryPauses
	}
	var r platform.RunResult
	for attempt := 0; ; attempt++ {
		r = runWithTimeout([]string{"agent", "get", target}, env, timeout)
		if attempt >= len(retryPauses) || !transientKill(r) {
			break
		}
		time.Sleep(retryPauses[attempt])
	}
	empty := AgentStateResult{Seq: ""}
	if r.Error == "ETIMEDOUT" {
		return AgentStateResult{"unavailable", timeoutMsg("agent get", timeout), ""}
	}
	rc := dieError(r)
	raw := r.Stderr
	if raw == "" {
		raw = r.Stdout
	}
	code, msg := errorInfo(raw)
	if code == "agent_not_found" {
		return AgentStateResult{"gone", "", ""}
	}
	if rc == 0 && code == "" {
		v := parsed(r.Stdout)
		ag := get(v, "result", "agent")
		status := str(get(ag, "agent_status"))
		empty.Seq = jsSequence(get(ag, "state_change_seq"))
		if status != "" {
			empty.State = status
			return empty
		}
		raw = "agent get returned no agent_status"
	}
	if code != "" {
		cause := text.SanitizeCause(code + ": " + msg)
		if cause == "" {
			cause = fmt.Sprintf("herdr agent get failed (exit %d)", rc)
		}
		return AgentStateResult{"unavailable", cause, empty.Seq}
	}
	if r.Signal != "" || rc >= 128 {
		signal := r.Signal
		if signal == "" && rc > 128 {
			signal = signalName(rc - 128)
		}
		if signal != "" {
			return AgentStateResult{"unavailable", fmt.Sprintf("herdr agent get was killed (exit %d, %s: memory pressure or an external kill)", rc, signal), empty.Seq}
		}
	}
	if raw == "" {
		raw = fmt.Sprintf("herdr agent get failed (exit %d)", rc)
	}
	cause := text.SanitizeCause(raw)
	if cause == "" {
		cause = fmt.Sprintf("herdr agent get failed (exit %d)", rc)
	}
	return AgentStateResult{"unavailable", cause, empty.Seq}
}
func transientKill(r platform.RunResult) bool {
	if r.NotFound || r.Error == "ETIMEDOUT" {
		return false
	}
	killed := r.Signal != "" || (r.Status != nil && *r.Status >= 128)
	if !killed {
		return false
	}
	for _, s := range []string{r.Stderr, r.Stdout} {
		if jsTruthy(getErrorCode(s)) {
			return false
		}
	}
	return true
}
func errorInfo(raw string) (string, string) {
	v, err := jsonjs.Parse([]byte(raw))
	if err != nil {
		return "", ""
	}
	code := get(v, "error", "code")
	if code == nil || code == false {
		return "", ""
	}
	return jsString(code), jsString(get(v, "error", "message"))
}
func jsSequence(value any) any {
	var number float64
	switch v := value.(type) {
	case json.Number:
		parsed, err := strconv.ParseFloat(string(v), 64)
		if err != nil {
			return ""
		}
		number = parsed
	case float64:
		number = v
	default:
		return ""
	}
	if math.IsNaN(number) || math.IsInf(number, 0) || math.Trunc(number) != number {
		return ""
	}
	if number >= math.MinInt64 && number < math.MaxInt64 {
		return int64(number)
	}
	return number
}
func getErrorCode(raw string) any {
	v, err := jsonjs.Parse([]byte(raw))
	if err != nil {
		return nil
	}
	return get(v, "error", "code")
}
func jsTruthy(v any) bool {
	switch value := v.(type) {
	case nil:
		return false
	case bool:
		return value
	case string:
		return value != ""
	case json.Number:
		number, err := strconv.ParseFloat(string(value), 64)
		return err == nil && number != 0
	case float64:
		return value != 0
	default:
		return true
	}
}
func jsString(v any) string {
	switch value := v.(type) {
	case nil:
		return ""
	case string:
		return value
	case bool:
		if value {
			return "true"
		}
		return "false"
	case *jsonjs.Object:
		return "[object Object]"
	case []any:
		parts := make([]string, len(value))
		for i, item := range value {
			parts[i] = jsString(item)
		}
		return strings.Join(parts, ",")
	default:
		return jsonjs.Stringify(v)
	}
}
func PaneTitle(pane string, title *string, env platform.Env) bool {
	args := []string{"pane", "report-metadata", pane, "--source", "soho"}
	if title == nil {
		args = append(args, "--clear-title")
	} else {
		args = append(args, "--title", *title)
	}
	return ok(run(args, env))
}
func PaneLayout(env platform.Env, pane string) PaneLayoutResult {
	args := []string{"pane", "layout", "--current"}
	if pane != "" {
		args = []string{"pane", "layout", "--pane", pane}
	}
	return run(args, env)
}
func TabGet(tab string, env platform.Env) TabGetResult {
	r := run([]string{"tab", "get", tab}, env)
	v := parsed(r.Stdout)
	return TabGetResult{ok(r), str(get(v, "result", "tab", "label")), str(get(v, "result", "root_pane", "pane_id"))}
}
func TabRename(tab, label string, env platform.Env) bool {
	return ok(run([]string{"tab", "rename", tab, label}, env))
}
func TabCreate(env platform.Env, ws, cwd, label string) TabCreateResult {
	r := run([]string{"tab", "create", "--workspace", ws, "--cwd", cwd, "--label", label, "--no-focus"}, env)
	v := parsed(r.Stdout)
	return TabCreateResult{ok(r), str(get(v, "result", "tab", "tab_id")), str(get(v, "result", "root_pane", "pane_id"))}
}
func PaneSplit(anchor, direction, cwd string, env platform.Env, ratio *string) PaneSplitResult {
	args := []string{"pane", "split", anchor, "--direction", direction, "--cwd", cwd, "--no-focus"}
	if ratio != nil {
		args = append(args, "--ratio", *ratio)
	}
	r := run(args, env)
	return PaneSplitResult{ok(r), str(get(parsed(r.Stdout), "result", "pane", "pane_id"))}
}

// PaneMove runs a pane move with the caller's arguments unchanged. The
// result accepts both move_result.pane and the older result.pane response.
func PaneMove(args []string, env platform.Env) PaneMoveResult {
	r := run(append([]string{"pane", "move"}, args...), env)
	if !ok(r) {
		return PaneMoveResult{}
	}
	if strings.TrimSpace(r.Stdout) == "" {
		return PaneMoveResult{Ok: true}
	}
	v, err := jsonjs.Parse([]byte(r.Stdout))
	if err != nil {
		return PaneMoveResult{}
	}
	first, valid := jqPath(v, "result", "move_result", "pane", "pane_id")
	if !valid {
		return PaneMoveResult{}
	}
	id := first
	if id == nil || id == false {
		id, valid = jqPath(v, "result", "pane", "pane_id")
		if !valid {
			return PaneMoveResult{}
		}
	}
	if id == nil || id == false {
		id = ""
	}
	pane, valid := jqPath(v, "result", "move_result", "pane")
	if !valid {
		return PaneMoveResult{}
	}
	tabValue := get(pane, "tab_id")
	tab := jqString(tabValue)
	if tabValue == false {
		tab = "false"
	}
	return PaneMoveResult{Ok: true, Pane: jqString(id), Tab: tab}
}

func jqPath(value any, keys ...string) (any, bool) {
	for _, key := range keys {
		if value == nil {
			return nil, true
		}
		object, ok := value.(*jsonjs.Object)
		if !ok {
			return nil, false
		}
		value, _ = object.Get(key)
	}
	return value, true
}

func jqString(value any) string {
	if value == nil || value == false {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return jsonjs.StringifyIndent(value, 2)
}
func CallerAgentName(env platform.Env) string {
	return str(get(parsed(func() string {
		r := run([]string{"agent", "get", env.Get("HERDR_PANE_ID")}, env)
		if !ok(r) {
			return ""
		}
		return r.Stdout
	}()), "result", "agent", "name"))
}
func AgentFocus(agent string, env platform.Env) bool {
	return ok(run([]string{"agent", "focus", agent}, env))
}
func PaneFocusBack(direction, pane string, env platform.Env) bool {
	return ok(run([]string{"pane", "focus", "--direction", direction, "--pane", pane}, env))
}
func AgentSendKeys(agent, key string, env platform.Env) bool {
	return ok(run([]string{"agent", "send-keys", agent, key}, env))
}
func NotificationShow(title, body string, env platform.Env) {
	_ = run([]string{"notification", "show", title, "--body", body, "--sound", "done"}, env)
}
func PaneClose(pane string, env platform.Env) bool {
	return ok(run([]string{"pane", "close", pane}, env))
}

// PaneGetGone re-queries the pane with `herdr pane get` after a failed close:
// true only when the query fails with the pane_not_found code (the pane is
// already gone). A present pane, or a failure with any other cause, reports
// false so the caller keeps its "still open" report.
func PaneGetGone(pane string, env platform.Env) bool {
	r := run([]string{"pane", "get", pane}, env)
	if ok(r) {
		return false
	}
	raw := r.Stderr
	if raw == "" {
		raw = r.Stdout
	}
	code, _ := errorInfo(raw)
	return code == "pane_not_found"
}
func PaneSendText(pane, text string, env platform.Env) bool {
	return ok(run([]string{"pane", "send-text", pane, text}, env))
}
func PaneSendKeys(pane, keys string, env platform.Env) bool {
	return ok(run([]string{"pane", "send-keys", pane, keys}, env))
}
func AgentPrompt(agent, prompt string, env platform.Env) AgentPromptResult {
	r := platform.RunCli("herdr", []string{"agent", "prompt", agent, prompt}, platform.RunOptions{Env: env, TimeoutMs: int(Timeout / time.Millisecond), MergeOutput: true})
	raw := strings.TrimRight(r.Stdout, "\n")
	if r.TimedOut {
		return AgentPromptResult{false, timeoutMsg("agent prompt", Timeout)}
	}
	return AgentPromptResult{ok(r), raw}
}
