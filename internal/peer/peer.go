package peer

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/djalmajr/soho/internal/communication"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/provider"
	"github.com/djalmajr/soho/internal/sessionref"
	textutil "github.com/djalmajr/soho/internal/text"
)

const (
	PeerPrefix           = "[soho:peer]"
	PeerLogFile          = "peer-messages.tsv"
	PromptWaitTimeoutMS  = 15_000
	DefaultSendTimeoutMS = 600_000
	HerdrCallTimeoutMS   = 30_000
)

// peerOpenMarkers are the opening markers an input recognition accepts.
var peerOpenMarkers = []string{PeerPrefix}

var controlRE = regexp.MustCompile(`[\x00-\x08\x0b-\x1f\x7f]`)
var marker200 = regexp.MustCompile("\x1b\\[200~")
var marker201 = regexp.MustCompile("\x1b\\[201~")
var peerLogSeparators = regexp.MustCompile(`[\r\n\t]+`)
var dialogPatterns = []*regexp.Regexp{
	regexp.MustCompile(`trust this workspace`), regexp.MustCompile(`trust this folder`),
	regexp.MustCompile(`do you trust`), regexp.MustCompile(`enter to confirm`),
	regexp.MustCompile(`\[y/n\]`), regexp.MustCompile(`\(y/n\)`),
}

type callResult struct {
	Code  string
	Cause string
}

type agentInfo struct {
	OK       bool
	NotFound bool
	// TimedOut is a get cut by its own timeout.
	TimedOut    bool
	Code        string
	Cause       string
	Status      string
	PaneID      string
	Cwd         string
	WorkspaceID string
	Kind        string
	Seq         string
	// SessionKind is the agent's agent_session kind ("" when the get does
	// not report an agent_session); the local transcript proof arms from it.
	SessionKind string
}

type resolvedTarget struct {
	RefShown  string
	Machine   string
	OK        bool
	TimedOut  bool
	Code      string
	Cause     string
	TargetArg string
	Status    string
	Cwd       string
	Workspace string
	Kind      string
	Seq       string
}

// transcriptProof is the session-transcript delivery proof for a local target
// that writes every taken message as a user line in a local transcript file
// (a Claude Code session transcript, a pi session file): the pre-send count
// of the user lines that hold the peer marker, and the screen queue check a
// grown count must pass before it proves. Armed only when the transcript
// resolves and its pre-send count can be read; without it the send keeps the
// screen-based path, with no new warning.
type transcriptProof struct {
	path   string
	pre    int
	armed  bool
	count  func(path, marker string) (int, bool)
	queued func(visible, id string) bool
}

func RandomPeerID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		platform.Die("send: could not create message id", 4)
	}
	return hex.EncodeToString(b)
}

// peerFollowsLine is the last line of the peer header. The header itself is
// metadata only — the sender reference, the known sender fields, the send
// time and the reply command — and carries no intent, approval or
// behavioral instruction.
const peerFollowsLine = `The message follows, each line quoted with "> ".`

// peerNow is the clock the header's send time reads; a variable so tests
// pin it to one instant (go:linkname).
var peerNow = time.Now

// PeerSender carries the sender identity into a peer header: the sender
// reference (or the bare pane in the remote variant) plus the best-effort
// name, kind, role and model. The fields arrive already cleaned through
// HeaderField; a field that is empty, "-" or "unknown" is unknown and is
// omitted from the header, never shown as a placeholder.
type PeerSender struct {
	Ref   string
	Name  string
	Kind  string
	Role  string
	Model string
}

// knownPeerField reports whether a cleaned header field is a value the
// header shows: empty, "-" and "unknown" are the unknown markers.
func knownPeerField(v string) bool { return v != "" && v != "-" && v != "unknown" }

// peerSenderLine is the header's Sender line: the known name/kind/role/model
// items, "; "-separated, ending with a period. It is "" when nothing is
// known, and the header then omits the line.
func peerSenderLine(s PeerSender) string {
	items := []string{}
	if knownPeerField(s.Name) {
		items = append(items, "name "+s.Name)
	}
	if knownPeerField(s.Kind) {
		items = append(items, "kind "+s.Kind)
	}
	if knownPeerField(s.Role) {
		items = append(items, "role "+s.Role)
	}
	if knownPeerField(s.Model) {
		items = append(items, "model "+s.Model)
	}
	if len(items) == 0 {
		return ""
	}
	return "Sender: " + strings.Join(items, "; ") + "."
}

// peerSentTime is the header's send time: the current instant in UTC,
// RFC 3339 seconds, on the opening line.
func peerSentTime() string { return peerNow().UTC().Format(time.RFC3339) }

// peerHeaderLines assembles the peer header in its fixed order: the opening
// line (carrying the send time), the Sender line (omitted when nothing is
// known), the reply line and the closing quote line. The header is at most
// four lines: on a codex composer screen the opening line must stay among
// the last 8 non-empty screen lines, or the composer is not recognized and
// the single recovery Enter is skipped.
func peerHeaderLines(s PeerSender, first, reply string) string {
	lines := []string{first}
	if sender := peerSenderLine(s); sender != "" {
		lines = append(lines, sender)
	}
	return strings.Join(append(lines, reply, peerFollowsLine), "\n")
}

// PeerHeaderFor builds the peer header for a target on the receiver's
// machine: line one carries the sender reference and the send time, and the
// reply line the exact send command back to it.
func PeerHeaderFor(s PeerSender, id string) string {
	prefix := PeerPrefix
	if id != "" {
		prefix += " #" + id
	}
	first := fmt.Sprintf("%s Message from another agent — %s, sent %s.", prefix, s.Ref, peerSentTime())
	reply := fmt.Sprintf("Reply with: soho send %s \"<your reply>\"", s.Ref)
	return peerHeaderLines(s, first, reply)
}

// senderHostname names this machine in the header of a remote peer message:
// the receiver looks it up in its own herdr machine list. It is a variable
// so tests replace it (go:linkname).
var senderHostname = func() string {
	host, err := os.Hostname()
	if err != nil {
		return ""
	}
	return host
}

// PeerHeaderRemoteFor is the peer header for a target on another machine:
// there, the sender's own "local/<pane>" label would point at the target's
// machine and the reply would fail, so line one names the pane on this
// machine's hostname and the reply line asks for this machine's name in the
// receiver's herdr machine list (the sender cannot know that label), with
// the hostname as the hint in the parenthetical. Without a hostname (empty
// or emptied by the cleaning) the " on …" part and the parenthetical are
// dropped: there is no hostname for the receiver to look up.
func PeerHeaderRemoteFor(s PeerSender, pane, hostname, id string) string {
	prefix := PeerPrefix
	if id != "" {
		prefix += " #" + id
	}
	sent := peerSentTime()
	if hostname != "" {
		first := fmt.Sprintf("%s Message from another agent — %s on %s, sent %s.", prefix, pane, hostname, sent)
		reply := fmt.Sprintf("Reply with: soho send <this machine's name in your herdr machine list>/%s \"<your reply>\" (this machine is %s)", pane, hostname)
		return peerHeaderLines(s, first, reply)
	}
	first := fmt.Sprintf("%s Message from another agent — %s, sent %s.", prefix, pane, sent)
	reply := fmt.Sprintf("Reply with: soho send <this machine's name in your herdr machine list>/%s \"<your reply>\"", pane)
	return peerHeaderLines(s, first, reply)
}

// PeerHeader builds the peer header for a target on the receiver's machine.
func PeerHeader(senderRef, senderName, senderKind, senderRole, id string) string {
	return PeerHeaderFor(PeerSender{Ref: senderRef, Name: senderName, Kind: senderKind, Role: senderRole}, id)
}

// PeerHeaderRemote is the peer header for a target on another machine.
func PeerHeaderRemote(pane, hostname, senderName, senderKind, senderRole, id string) string {
	return PeerHeaderRemoteFor(PeerSender{Name: senderName, Kind: senderKind, Role: senderRole}, pane, hostname, id)
}

// PeerHeaderAssignment is the peer header for a collaboration assignment
// delivery: the local header whose reply line is the assignment reply
// command, addressed to the counterpart's name.
func PeerHeaderAssignment(s PeerSender, replyName, assignmentID, id string) string {
	prefix := PeerPrefix
	if id != "" {
		prefix += " #" + id
	}
	first := fmt.Sprintf("%s Message from another agent — %s, sent %s.", prefix, s.Ref, peerSentTime())
	reply := fmt.Sprintf("Reply within this assignment: soho send %s --assignment %s --type review.question \"<your reply>\"", replyName, assignmentID)
	return peerHeaderLines(s, first, reply)
}

// PeerEndPrefix is the peer message's closing-line marker: the peer prefix
// closed by a slash, so an opening header line ("[soho:peer] #<id>")
// can never be read as the end of a message and an end line can never be
// read as an opening header.
const PeerEndPrefix = "[/soho:peer]"

// peerEndReceiptMarkers are the closing-line marker forms an input
// recognition accepts.
var peerEndReceiptMarkers = []string{PeerEndPrefix}

func PeerEndLine(id string) string { return fmt.Sprintf("%s #%s end of message", PeerEndPrefix, id) }

func QuotePeerBody(body string) string {
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if line == "" {
			lines[i] = ">"
		} else {
			lines[i] = "> " + line
		}
	}
	return strings.Join(lines, "\n")
}

// HeaderField cleans a value that goes inside a header line: the
// LiteralPeerText cleaning, then every run of whitespace (line breaks and
// tabs included) becomes one space, so a value can never start a new line
// of the header or forge a closing line.
func HeaderField(s string) string {
	return strings.Join(strings.Fields(LiteralPeerText(s)), " ")
}

func LiteralPeerText(s string) string {
	s = marker200.ReplaceAllString(s, "")
	s = marker201.ReplaceAllString(s, "")
	return controlRE.ReplaceAllString(s, "")
}

func SenderRefOf(env platform.Env) string {
	pane := env.Get("HERDR_PANE_ID")
	if pane == "" {
		return sessionref.LocalMachine + "/-"
	}
	return sessionref.FormatRef(sessionref.Ref{Machine: sessionref.LocalMachine, PaneID: pane})
}

func SenderInfo(ctx *core.Config, env platform.Env, cwd string, skipRosterLookup bool) (ref, name, kind, role, model string) {
	pane := env.Get("HERDR_PANE_ID")
	if pane == "" {
		return sessionref.LocalMachine + "/-", "-", "-", "-", "-"
	}
	ref = sessionref.FormatRef(sessionref.Ref{Machine: sessionref.LocalMachine, PaneID: pane})
	name, kind, role, model = "-", "-", "-", "-"
	// Sender identity is deliberately best-effort; a missing lookup never prevents delivery.
	args := append(sessionref.HerdrMachineArgs(sessionref.LocalMachine), "agent", "get", pane)
	r := platform.RunCli("herdr", args, platform.RunOptions{Env: env, TimeoutMs: HerdrCallTimeoutMS})
	if !r.NotFound && r.Status != nil && *r.Status == 0 {
		var out map[string]any
		if json.Unmarshal([]byte(r.Stdout), &out) == nil {
			result := object(out["result"])
			ag := object(result["agent"])
			if n, ok := ag["name"].(string); ok && n != "" {
				name = n
			}
			if k, ok := ag["agent"].(string); ok && k != "" {
				kind = k
			}
		}
	}
	// skipRosterLookup keeps the send from resolving the state dir while the
	// former state waits for the setup migration: the roster is absent then,
	// so the lookup would change nothing but the .gitignore could move.
	if name != "-" && !skipRosterLookup {
		line := core.RosterLine(core.StateDirPath(ctx, env, cwd), name)
		fields := strings.Split(line, "\t")
		if len(fields) > 3 && fields[3] != "" {
			role = fields[3]
		}
		// Column 9 (index 8) of agents.tsv is the model.
		if len(fields) > 8 {
			model = fields[8]
		}
	}
	return
}

func inboundPolicy(targetCwd, targetWorkspace string, env platform.Env) string {
	config, policyEnv := inboundConfiguration(targetCwd, targetWorkspace, env)
	return core.Cfg(&config, "inbound", "auto", policyEnv)
}

func inboundConfiguration(targetCwd, targetWorkspace string, env platform.Env) (core.Config, platform.Env) {
	policyEnv := env.Clone()
	skillDir := env.Get("SOHO_SKILL_DIR")
	for key := range policyEnv {
		if strings.HasPrefix(key, "SOHO_") {
			delete(policyEnv, key)
		}
	}
	// This runtime locator is not a configuration value, but Go needs it to
	// read the shared defaults file after the sender configuration is stripped.
	if skillDir != "" {
		policyEnv["SOHO_SKILL_DIR"] = skillDir
	}
	if targetWorkspace != "" {
		policyEnv["HERDR_WORKSPACE_ID"] = targetWorkspace
	} else {
		delete(policyEnv, "HERDR_WORKSPACE_ID")
		delete(policyEnv, "HERDR_ENV")
	}
	config := core.LoadConfig(policyEnv, targetCwd)
	return config, policyEnv
}

func agentGet(machine, target string, env platform.Env, timeoutMS int) agentInfo {
	args := append(sessionref.HerdrMachineArgs(machine), "agent", "get", target)
	r := platform.RunCli("herdr", args, platform.RunOptions{Env: env, TimeoutMs: timeoutMS})
	if r.NotFound {
		return agentInfo{NotFound: true, Cause: "herdr CLI not found in PATH"}
	}
	if r.TimedOut {
		return agentInfo{TimedOut: true, Cause: fmt.Sprintf("herdr agent get timed out after %ss", numberSeconds(timeoutMS))}
	}
	status := 1
	if r.Status != nil {
		status = *r.Status
	}
	if status != 0 {
		code, cause := structuredError(r.Stdout, r.Stderr, "agent get", fmt.Sprintf("failed (exit %d)", status))
		return agentInfo{Code: code, Cause: cause}
	}
	var out map[string]any
	if json.Unmarshal([]byte(r.Stdout), &out) != nil {
		return agentInfo{Cause: "agent get returned no agent"}
	}
	ag := object(object(out["result"])["agent"])
	if len(ag) == 0 {
		return agentInfo{Cause: "agent get returned no agent"}
	}
	state := ag["agent_status"]
	if state == nil || state == false || state == "" {
		return agentInfo{Cause: "agent get returned no agent_status"}
	}
	return agentInfo{OK: true, Status: valueString(state), PaneID: nonEmptyString(ag["pane_id"]),
		Cwd: stringValue(ag["cwd"]), WorkspaceID: stringValue(ag["workspace_id"]), Kind: stringValue(ag["agent"]), Seq: optionalString(ag["state_change_seq"]),
		SessionKind: stringValue(object(ag["agent_session"])["kind"])}
}

func nonEmptyString(v any) string {
	s, _ := v.(string)
	return s
}

func stringValue(v any) string {
	s, _ := v.(string)
	return s
}

func optionalString(v any) string {
	if v == nil || v == "" {
		return ""
	}
	return valueString(v)
}

func resolveTarget(target string, env platform.Env) resolvedTarget {
	return resolveTargetWithin(target, env, HerdrCallTimeoutMS)
}

// resolveTargetWithin is resolveTarget with the agent get bounded by
// timeoutMS: the agent_not_ready retries of send pass what is left of the
// send's --timeout.
func resolveTargetWithin(target string, env platform.Env, timeoutMS int) resolvedTarget {
	ref := sessionref.ParseRef(target)
	machine, paneTarget, shown := sessionref.LocalMachine, target, target
	if ref != nil {
		machine, paneTarget, shown = ref.Machine, ref.PaneID, sessionref.FormatRef(*ref)
	}
	a := agentGet(machine, paneTarget, env, timeoutMS)
	if a.NotFound {
		return resolvedTarget{RefShown: shown, Machine: machine, Cause: a.Cause}
	}
	if !a.OK {
		return resolvedTarget{RefShown: shown, Machine: machine, Code: a.Code, Cause: a.Cause, TimedOut: a.TimedOut}
	}
	targetArg := paneTarget
	if a.PaneID != "" {
		targetArg = a.PaneID
	}
	return resolvedTarget{RefShown: shown, Machine: machine, OK: true, TargetArg: targetArg,
		Status: a.Status, Cwd: a.Cwd, Workspace: a.WorkspaceID, Kind: a.Kind, Seq: a.Seq}
}

// noAgentHint builds the same-workspace suggestion for the agent_not_found 4
// of a [machine/]<workspace>:<pane> reference: the live agents of the same
// workspace on the same machine, listed the way find does (the same snapshot
// agent list, with --machine for a remote machine). At most 5, preferring the
// names that begin with "orchestrator". A failed or empty listing — and a
// target that is not such a reference (a name) — returns "": today's message
// stands, the exit code stays 4 and nothing is sent.
func noAgentHint(target, machine string, env platform.Env) string {
	ref := sessionref.ParseRef(target)
	if ref == nil {
		return ""
	}
	workspace, _, cut := strings.Cut(ref.PaneID, ":")
	if !cut || workspace == "" {
		return ""
	}
	result := FetchSessions([]string{machine}, SnapshotOptions{Env: env})
	if len(result.Failures) > 0 {
		return ""
	}
	candidates := []SessionEntry{}
	for _, e := range result.Entries {
		// A live agent of the same workspace on the same machine: a pane with
		// no agent status holds nothing to suggest, and another workspace's
		// agents are not candidates.
		if e.Machine != machine || valueString(e.Status) == "" {
			continue
		}
		if ws, _, ok := strings.Cut(e.PaneID, ":"); ok && ws == workspace {
			candidates = append(candidates, e)
		}
	}
	if len(candidates) == 0 {
		return ""
	}
	prefers := func(e SessionEntry) bool { return strings.HasPrefix(valueString(e.Name), "orchestrator") }
	sort.SliceStable(candidates, func(i, j int) bool { return prefers(candidates[i]) && !prefers(candidates[j]) })
	if len(candidates) > 5 {
		candidates = candidates[:5]
	}
	parts := make([]string, 0, len(candidates))
	for _, e := range candidates {
		name, kind := valueString(e.Name), valueString(e.Kind)
		if name == "" {
			name = "-"
		}
		if kind == "" {
			kind = "-"
		}
		parts = append(parts, fmt.Sprintf("%s (%s, %s)", e.Ref, name, kind))
	}
	return "; agents in " + workspace + ": " + strings.Join(parts, ", ")
}

func structuredError(stdout, stderr, what, rcLabel string) (string, string) {
	raw := stderr
	if raw == "" {
		raw = stdout
	}
	var parsed map[string]any
	if json.Unmarshal([]byte(raw), &parsed) == nil {
		e := object(parsed["error"])
		code := optionalString(e["code"])
		message := optionalString(e["message"])
		if code != "" {
			cause := textutil.SanitizeCause(code + ": " + message)
			if cause == "" {
				cause = "herdr " + what + " " + rcLabel
			}
			return code, cause
		}
	}
	cause := textutil.SanitizeCause(raw)
	if cause == "" {
		cause = "herdr " + what + " " + rcLabel
	}
	return "", cause
}

func waitUntilIdle(machine, pane string, timeoutMS int, env platform.Env) callResult {
	args := append(sessionref.HerdrMachineArgs(machine), "agent", "wait", pane, "--until", "idle", "--until", "done", "--timeout", fmt.Sprint(timeoutMS))
	r := platform.RunCli("herdr", args, platform.RunOptions{Env: env, TimeoutMs: timeoutMS + HerdrCallTimeoutMS})
	if r.NotFound {
		return callResult{Cause: "herdr CLI not found in PATH"}
	}
	if r.Status != nil && *r.Status == 0 {
		return callResult{Code: "settled"}
	}
	if r.TimedOut {
		return callResult{Code: "timeout", Cause: fmt.Sprintf("agent wait timed out after %ss", numberSeconds(timeoutMS))}
	}
	status := 1
	if r.Status != nil {
		status = *r.Status
	}
	code, cause := structuredError(r.Stdout, r.Stderr, "agent wait", fmt.Sprintf("failed (exit %d)", status))
	return callResult{Code: code, Cause: cause}
}

func deliverPrompt(machine, pane, body string, env platform.Env) callResult {
	args := append(sessionref.HerdrMachineArgs(machine), "agent", "prompt", pane, body, "--wait", "--until", "working", "--until", "blocked", "--until", "idle", "--until", "done", "--timeout", fmt.Sprint(PromptWaitTimeoutMS))
	r := platform.RunCli("herdr", args, platform.RunOptions{Env: env, TimeoutMs: PromptWaitTimeoutMS + HerdrCallTimeoutMS, MergeOutput: true})
	if r.NotFound {
		return callResult{Cause: "herdr CLI not found in PATH"}
	}
	if r.Status != nil && *r.Status == 0 {
		return callResult{Code: "ok"}
	}
	if r.TimedOut {
		return callResult{Cause: fmt.Sprintf("herdr agent prompt timed out after %ss", numberSeconds(PromptWaitTimeoutMS))}
	}
	status := 1
	if r.Status != nil {
		status = *r.Status
	}
	code, cause := structuredError(r.Stdout, r.Stderr, "agent prompt", fmt.Sprintf("failed (exit %d)", status))
	return callResult{Code: code, Cause: cause}
}

// floorCallBudget floors a call's time budget (in ms) at one second so a
// call can still finish when its deadline is moments away.
func floorCallBudget(ms int) int {
	if ms < 1000 {
		return 1000
	}
	return ms
}

// readScreen runs `herdr agent read`, bounded by timeoutMS. A full-screen TUI
// holds its history on the alternate screen, which Herdr captures only while
// the agent is idle: a `recent` read of a working one fails with
// agent_not_idle. The visible screen is what such an agent shows, so that
// read (without --lines) takes its place, as herdr's agentRead does; any
// other error, and a failed visible read, is not repeated. The fallback is
// bounded by the time left of the same deadline the first read was given
// (floored so it can finish), so the time the first read already consumed
// is not paid twice. A successful visible-screen read — direct or that
// fallback — is reported through onVisibleScreen when it is non-nil, so the
// caller (the send flow) records every visible screen it sees.
func readScreen(machine, pane, source string, lines int, env platform.Env, timeoutMS int, onVisibleScreen func(string)) (string, string) {
	args := append(sessionref.HerdrMachineArgs(machine), "agent", "read", pane, "--source", source)
	if lines > 0 {
		args = append(args, "--lines", fmt.Sprint(lines))
	}
	deadline := time.Now().Add(time.Duration(timeoutMS) * time.Millisecond)
	r := platform.RunCli("herdr", args, platform.RunOptions{Env: env, TimeoutMs: timeoutMS})
	if r.NotFound {
		return "", "herdr CLI not found in PATH"
	}
	if r.TimedOut {
		return "", fmt.Sprintf("herdr agent read timed out after %ss", numberSeconds(timeoutMS))
	}
	if r.Status == nil || *r.Status != 0 {
		status := 1
		if r.Status != nil {
			status = *r.Status
		}
		code, cause := structuredError(r.Stdout, r.Stderr, "agent read", fmt.Sprintf("failed (exit %d)", status))
		if code == "agent_not_idle" && strings.HasPrefix(source, "recent") {
			// The fallback is a visible read too: it reports through the same
			// hook, so the screen it shows is the one recorded.
			return readScreen(machine, pane, "visible", 0, env, floorCallBudget(int(time.Until(deadline).Milliseconds())), onVisibleScreen)
		}
		return "", cause
	}
	if source == "visible" && onVisibleScreen != nil {
		onVisibleScreen(r.Stdout)
	}
	return r.Stdout, ""
}

// SaveStalledScreen writes the last visible screen read of a stalled send to
// <stateDir>/wait/send-<id>.screen, next to spawn's shared-tree markers,
// atomically: a temp file in the wait dir renamed over the target, so a crash
// never leaves a half-written screen. It returns the path, or "" when there
// is no screen to record, when SOHO_NOWRITE is set (the CLI already
// refuses the send under it; the guard is the helper's own defense), or
// when the write fails. The screen goes only to that local file, which the
// state dir's gitignore already covers — its content never reaches stderr
// (but the path in the message), the peer log or the friction.
func SaveStalledScreen(stateDir, id, screen string, env platform.Env) string {
	if core.Nowrite(env) || stateDir == "" || screen == "" {
		return ""
	}
	waitDir := filepath.Join(stateDir, "wait")
	if err := os.MkdirAll(waitDir, 0o777); err != nil {
		return ""
	}
	dest := filepath.Join(waitDir, "send-"+id+".screen")
	tmp, err := os.CreateTemp(waitDir, ".send-*.screen")
	if err != nil {
		return ""
	}
	if _, err = tmp.WriteString(screen); err == nil {
		err = tmp.Chmod(0o666)
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), dest)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return ""
	}
	return dest
}

func sendKey(machine, pane, key string, env platform.Env) bool {
	args := append(sessionref.HerdrMachineArgs(machine), "agent", "send-keys", pane, key)
	r := platform.RunCli("herdr", args, platform.RunOptions{Env: env, TimeoutMs: HerdrCallTimeoutMS})
	return !r.NotFound && r.Status != nil && *r.Status == 0
}

func TailLines(value string, count int) []string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	lines := strings.Split(value, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimFunc(line, isJSWhitespace) != "" {
			out = append(out, line)
		}
	}
	if count < len(out) {
		out = out[len(out)-count:]
	}
	return out
}

func NormalizeScreen(value string) string {
	return strings.Map(func(r rune) rune {
		if isJSWhitespace(r) || r >= 0x2500 && r <= 0x257f {
			return -1
		}
		return r
	}, value)
}

func isJSWhitespace(r rune) bool {
	return r >= 0x9 && r <= 0xd || r == 0x20 || r == 0xa0 || r == 0x1680 || r >= 0x2000 && r <= 0x200a || r == 0x2028 || r == 0x2029 || r == 0x202f || r == 0x205f || r == 0x3000 || r == 0xfeff
}

// isDialogScreen reports whether the screen shows a dialog the send must not
// send a key into: a known trust, approval, or question UI per the shared
// provider classifier (provider.DialogUI — tried for every known kind when
// the kind is unknown, like the blocked-state rule), the startup trust
// patterns of the last 10 lines, or a blocked-state question dialog. The
// shared classifier is an additional refusal on top of the current
// protections: it matches a known dialog line anywhere on the screen and is
// not gated on the status.
func isDialogScreen(screen, kind, status string) bool {
	if screen == "" {
		return false
	}
	knownKinds := []string{kind}
	if kind == "" {
		knownKinds = []string{"codex", "claude", "opencode"}
	}
	for _, k := range knownKinds {
		if provider.DialogUI(k, screen) != "" {
			return true
		}
	}
	bottom10 := strings.Join(TailLines(screen, 10), "\n")
	bottom10 = textutil.ASCIILower(bottom10)
	for _, pattern := range dialogPatterns {
		if pattern.MatchString(bottom10) {
			return true
		}
	}
	if status == "blocked" {
		bottom20 := strings.Join(TailLines(screen, 20), "\n")
		for _, k := range knownKinds {
			if questionDialog(k, bottom20) {
				return true
			}
		}
	}
	return false
}

func questionDialog(kind, screen string) bool {
	lower := textutil.JSLower(screen)
	switch kind {
	case "codex":
		return strings.Contains(lower, "enter to submit answer") || strings.Contains(lower, "enter to submit all")
	case "claude":
		if strings.Contains(lower, "do you want to") || !strings.Contains(lower, "enter to select") {
			return false
		}
		return strings.Contains(lower, "to navigate") || strings.Contains(lower, "submit answers")
	case "opencode":
		if strings.Contains(lower, "permission required") || !strings.Contains(lower, "esc dismiss") {
			return false
		}
		return strings.Contains(lower, "enter submit") || strings.Contains(lower, "enter toggle")
	default:
		return false
	}
}

func checkIDInScreen(screen, id string) string {
	if id == "" {
		return "absent"
	}
	lines := TailLines(screen, int(^uint(0)>>1))
	end := len(lines) - 3
	if end < 0 {
		end = 0
	}
	for _, line := range lines[:end] {
		if strings.Contains(line, id) {
			return "outside"
		}
	}
	for _, line := range lines[end:] {
		if strings.Contains(line, id) {
			return "input_box"
		}
	}
	return "absent"
}

func CheckIDInScreen(screen, id string) string { return checkIDInScreen(screen, id) }

func arrivalWindowMS(env platform.Env) int {
	return boundedEnvNumber(env.Get("SOHO_SEND_WINDOW_MS"), 15_000, 15_000)
}
func arrivalPollMS(env platform.Env) int {
	return boundedEnvNumber(env.Get("SOHO_SEND_POLL_MS"), 1000, 1000)
}

func boundedEnvNumber(raw string, fallback, max int) int {
	if raw == "" {
		return fallback
	}
	n := 0
	for _, r := range raw {
		if r < '0' || r > '9' || n > max*10 {
			return fallback
		}
		n = n*10 + int(r-'0')
	}
	if n < 1 || n > max {
		return fallback
	}
	return n
}

func appendPeerLog(stateDir, from, to, result string, chars int, id string, env platform.Env) {
	appendPeerAssignmentLog(stateDir, from, to, result, chars, id, env)
}

func appendPeerAssignmentLog(stateDir, from, to, result string, chars int, id string, env platform.Env, metadata ...string) {
	if core.Nowrite(env) {
		return
	}
	// Defense: peer logs are state and never land inside the skill.
	if core.StatePathInSkill(env, stateDir) != "" {
		return
	}
	clean := func(v string) string { return peerLogSeparators.ReplaceAllString(v, " ") }
	line := fmt.Sprintf("%s\t%s\t%s\t%s\t%d\t%s\n", platform.Now().Format("2006-01-02T15:04:05"), clean(from), clean(to), clean(result), chars, clean(id))
	for _, value := range metadata {
		line = strings.TrimSuffix(line, "\n") + "\t" + clean(value) + "\n"
	}
	if os.MkdirAll(stateDir, 0o755) == nil {
		f, err := os.OpenFile(filepath.Join(stateDir, PeerLogFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o666)
		if err == nil {
			_, _ = f.WriteString(line)
			_ = f.Close()
		}
	}
}

func sleepMS(ms int) {
	if ms > 0 {
		time.Sleep(time.Duration(ms) * time.Millisecond)
	}
}

func utf16Length(s string) int { return len(utf16.Encode([]rune(s))) }

type proofResult struct {
	Proven              bool
	RecentReadSucceeded bool
	LastCause           string
}

func CmdSend(argv []string, ctx *core.Config, env platform.Env, cwd string) int {
	target, file := "", ""
	assignment, messageType := "", ""
	now, timeoutMS := false, DefaultSendTimeoutMS
	timeoutGiven, timeoutPlain := false, false
	words := []string{}
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		switch a {
		case "--now":
			now = true
		case "--file", "--timeout", "--assignment", "--type":
			if i+1 >= len(argv) || argv[i+1] == "" || strings.HasPrefix(argv[i+1], "--") {
				platform.Die("send: "+a+" expects a value", 2)
			}
			i++
			if a == "--file" {
				file = argv[i]
			} else if a == "--assignment" {
				assignment = argv[i]
			} else if a == "--type" {
				messageType = argv[i]
			} else {
				raw := argv[i]
				ms, ok := core.ParseTimeoutMS(raw)
				if ok && (ms <= 0 || ms > int64(math.MaxInt)) {
					ok = false // send keeps its positive minimum, and a plain number its int range
				}
				if !ok {
					// The JS Number() spellings (1e3, 12.0, 0x10) stay accepted
					// as plain milliseconds; a unit needs ParseTimeoutMS.
					if n, ok2 := parsePositiveInt(raw); ok2 {
						ms = int64(n)
					} else {
						platform.Die("send: --timeout expects the wait in milliseconds or a duration such as 90s, 30m or 2h", 2)
					}
				}
				timeoutMS = int(ms)
				timeoutGiven = true
				timeoutPlain = core.IsPlainTimeoutMS(raw)
			}
		default:
			if strings.HasPrefix(a, "--") {
				platform.Die(fmt.Sprintf("send: unknown option '%s'", a), 2)
			}
			if target == "" {
				target = a
			} else {
				words = append(words, a)
			}
		}
	}
	if target == "" {
		platform.Die("usage: send <ref|name> <message…> | send <ref|name> --file <path> [--now] [--timeout MS]", 2)
	}
	body := strings.Join(words, " ")
	if file != "" {
		if len(words) != 0 {
			platform.Die("send: use either the message words or --file, not both", 2)
		}
		raw, err := platform.ReadTextFile(file)
		if err != nil {
			platform.Die(fmt.Sprintf("send: cannot read --file '%s'", file), 2)
		}
		body = strings.TrimRight(raw, "\n")
	}
	if body == "" {
		platform.Die("send: empty message (pass the message words or --file <path>)", 2)
	}
	if assignment != "" || messageType != "" {
		if messageType != "review.question" {
			platform.Die("send: only review.question is free text; publish ready/findings/corrected/approved with collaborate event and a declared revision", 2)
		}
		if assignment == "" || messageType == "" {
			platform.Die("send: --assignment and --type must be passed together", 2)
		}
		result, err := SendAssignmentMessage(AssignmentMessageOptions{AssignmentID: assignment, Target: target, Type: messageType, Body: body, TimeoutMS: timeoutMS, Config: ctx, Env: env, Cwd: cwd})
		if err != nil {
			code := 2
			if errors.Is(err, communication.ErrInboundOff) {
				code = 18
			}
			if result.Status == "uncertain" {
				code = 15
			}
			platform.Die("send: "+err.Error(), code)
		}
		fmt.Fprintln(platform.Stdout, jsonjs.Stringify(jsonjs.O("status", result.Status, "assignment", assignment, "type", messageType)))
		return 0
	}
	id := RandomPeerID()
	// Decide with StateInSkill/StateRootPath before any StateDirPath/StateRoot/
	// WorkspaceID, so the refusal leaves no side effect (no .gitignore append,
	// no pane current). The path named is the state root.
	if skill := core.StateInSkill(ctx, env, cwd); skill != "" {
		platform.Die(fmt.Sprintf("the state dir '%s' would be inside the soho skill ('%s'); run soho from the project's directory (nothing was sent)", core.StateRootPath(ctx, env, cwd), skill), 2)
	}
	// The former state dir waiting for the setup migration keeps the send from
	// resolving the state dir at all: no StateDirPath (no .gitignore append,
	// no pane current), no peer-messages.tsv and no friction.log. The roster
	// is absent while it waits, so skipping the reads changes no identity or
	// exit code. The send itself, the stdout and the exit codes are
	// unchanged; the short-timeout warning still goes to stderr, only
	// without the log file.
	_, _, formerWaiting := core.FormerStateWaiting(ctx, env, cwd)
	senderRef, senderName, senderKind, senderRole, senderModel := SenderInfo(ctx, env, cwd, formerWaiting)
	if !formerWaiting {
		if _, err := os.ReadFile(filepath.Join(core.StateDirPath(ctx, env, cwd), "agents.tsv")); err != nil && !os.IsNotExist(err) {
			platform.Die("send: worker registration is unreadable; nothing was sent", 4)
		}
		// The pane registration remains authoritative if the best-effort name
		// lookup failed: an unavailable worker cannot bypass the default refusal.
		for _, row := range core.RosterRows(core.StateDirPath(ctx, env, cwd)) {
			parts := strings.Split(row, "\t")
			if len(parts) > 1 && parts[1] != "" && parts[1] == env.Get("HERDR_PANE_ID") {
				senderName, senderRole = parts[0], "unknown"
				if senderName == "" || senderName == "-" {
					senderName = "registered-worker"
				}
				if len(parts) > 3 {
					senderRole = parts[3]
				}
				// Column 9 (index 8) of agents.tsv is the model.
				if len(parts) > 8 {
					senderModel = parts[8]
				}
				break
			}
		}
	}
	// A rostered worker reports through its report file, not by message: refuse
	// before any send. The orchestrator (role -) and sub-orchestrators keep
	// sending; no HERDR_PANE_ID or a failing sender lookup leaves the send alone.
	if senderName != "-" && senderRole != "-" && senderRole != "sub-orchestrator" {
		platform.Die(fmt.Sprintf("send: '%s' is a worker of this team (role %s): a worker reports through its report file, not by message (nothing was sent)", senderName, senderRole), 2)
	}
	stateDir := ""
	if !formerWaiting {
		stateDir = core.StateDirPath(ctx, env, cwd)
	}
	// The short-timeout warning logs under the state dir, so it comes after the
	// skill refusal above, which must leave no side effect. An empty log file
	// keeps the warning on screen only (core.Warn writes nothing for it).
	if timeoutGiven && timeoutPlain {
		frictionLog := ""
		if stateDir != "" {
			frictionLog = filepath.Join(stateDir, "friction.log")
		}
		core.WarnShortTimeout("send", frictionLog, int64(timeoutMS))
	}
	log := func(from, to, result string) {
		if stateDir == "" {
			return
		}
		appendPeerLog(stateDir, from, to, result, utf16Length(body), id, env)
	}
	t := resolveTarget(target, env)
	// agent_not_ready is transient by definition: the pane's `agent start`
	// is still finishing, and the agent idles about 2 s before the start
	// returns. Poll the resolution on the send's own arrival poll until the
	// target resolves or the send's --timeout wins. Every other failure of
	// the resolution keeps today's immediate exit (agent_not_found with its
	// hint, herdr down, a timed-out get, a silent remote machine).
	notReady := false
	var deadline time.Time
	if !t.OK && t.Code == "agent_not_ready" {
		notReady = true
		deadline = time.Now().Add(time.Duration(timeoutMS) * time.Millisecond)
		for !t.OK && t.Code == "agent_not_ready" {
			left := int(time.Until(deadline).Milliseconds())
			if left <= 0 {
				break
			}
			sleepMS(min(arrivalPollMS(env), left))
			// Every retry gets only what is left of the send's --timeout,
			// checked again after the sleep.
			left = int(time.Until(deadline).Milliseconds())
			if left <= 0 {
				break
			}
			next := resolveTargetWithin(target, env, left)
			if notReadyRetryOutOfBudget(next, deadline, time.Now()) {
				// The retry ran out of the send's budget: the target is
				// still the agent_not_ready one.
				break
			}
			t = next
		}
		if !t.OK && t.Code == "agent_not_ready" {
			// The deadline passed with the agent still starting: today's
			// exit 4 and today's cause, with the waited time appended as the
			// send's --timeout in numberSeconds form.
			log(SenderRefOf(env), t.RefShown, "error")
			platform.Die(fmt.Sprintf("send: %s unavailable: %s (still not ready after %ss)", t.RefShown, t.Cause, numberSeconds(timeoutMS)), 4)
		}
	}
	if !t.OK {
		result := "error"
		if t.Code == "agent_not_found" {
			result = "no-agent"
		}
		log(SenderRefOf(env), t.RefShown, result)
		if t.Code == "agent_not_found" {
			msg := "send: no agent in " + t.RefShown
			if hint := noAgentHint(target, t.Machine, env); hint != "" {
				msg += hint
			}
			platform.Die(msg, 4)
		}
		platform.Die(fmt.Sprintf("send: %s unavailable: %s", t.RefShown, t.Cause), 4)
	}
	if t.Machine == sessionref.LocalMachine {
		policyCwd := t.Cwd
		policyTmp := ""
		if policyCwd == "" {
			var err error
			policyTmp, err = os.MkdirTemp(os.TempDir(), "ha-send-policy-")
			if err != nil {
				platform.Die("send: cannot inspect target inbound policy", 4)
			}
			policyCwd = policyTmp
		}
		policy := inboundPolicy(policyCwd, t.Workspace, env)
		if policyTmp != "" {
			_ = os.RemoveAll(policyTmp)
		}
		if policy == "off" {
			log(SenderRefOf(env), t.RefShown, "refused")
			platform.Die(fmt.Sprintf("send: %s does not accept peer messages (inbound=off)", t.RefShown), 18)
		}
	}
	currentStatus := t.Status
	if !now && (t.Status == "working" || t.Status == "blocked") {
		// The agent_not_ready wait above already spent part of the send's
		// --timeout: the busy wait gets only what is left before the same
		// deadline, so the waits never add up. With no not-ready wait the
		// budget is the full --timeout, exactly as today. No budget left
		// means the busy wait timed out already: no new wait starts.
		w := callResult{Code: "timeout"}
		if busyMS, wait := sendBusyBudgetMS(timeoutMS, notReady, deadline, time.Now()); wait {
			w = waitUntilIdle(t.Machine, t.TargetArg, busyMS, env)
		}
		if w.Code == "settled" {
			currentStatus = "idle"
		} else if w.Code == "timeout" {
			again := resolveTarget(target, env)
			status := t.Status
			if again.OK {
				status = again.Status
			}
			log(SenderRefOf(env), t.RefShown, "busy")
			// A blocked target may be showing a dialog or waiting for an
			// approval: never suggest typing over it.
			hint := "(--now sends it without waiting; the target's CLI decides whether to queue it)"
			if status == "blocked" {
				hint = "(it may be showing a dialog or waiting for an approval: read its pane before sending anything)"
			}
			platform.Die(fmt.Sprintf("send: %s is still %s after %ss; nothing was sent %s", t.RefShown, status, numberSeconds(timeoutMS), hint), 17)
		} else {
			log(SenderRefOf(env), t.RefShown, "error")
			platform.Die(fmt.Sprintf("send: %s unavailable: %s", t.RefShown, w.Cause), 4)
		}
	}
	// lastVisibleScreen holds the target's last successfully read visible
	// screen, from the pre-send read on: a stalled 15 saves it under the
	// sender state dir's wait dir, so the next occurrence can be diagnosed
	// against the screen that was on display. noteVisibleScreen is the hook
	// readScreen uses for every successful visible read of the send,
	// including the recent→visible fallback.
	var lastVisibleScreen string
	noteVisibleScreen := func(screen string) { lastVisibleScreen = screen }
	vScreen, cause := readScreen(t.Machine, t.TargetArg, "visible", 0, env, HerdrCallTimeoutMS, noteVisibleScreen)
	if cause != "" {
		log(senderRef, t.RefShown, "unreadable")
		platform.Die(fmt.Sprintf("send: could not read %s's screen (%s); nothing was sent", t.RefShown, cause), 4)
	}
	screenGet := agentGet(t.Machine, t.TargetArg, env, HerdrCallTimeoutMS)
	if !screenGet.OK {
		log(senderRef, t.RefShown, "error")
		platform.Die(fmt.Sprintf("send: %s unavailable: %s", t.RefShown, screenGet.Cause), 4)
	}
	currentStatus = screenGet.Status
	if isDialogScreen(vScreen, t.Kind, currentStatus) {
		// --now does not wait for the dialog to clear: the wait would follow
		// the --timeout budget (the rc.14 --now send stalled over 5 minutes on
		// the 600 s default while the dialog stayed on screen). The dialog
		// check below exits 17 at once, with no key; without --now the loop
		// waits until --timeout, as before.
		if !now {
			deadline := time.Now().Add(time.Duration(timeoutMS) * time.Millisecond)
			for time.Now().Before(deadline) && isDialogScreen(vScreen, t.Kind, currentStatus) {
				sleepMS(min(arrivalPollMS(env), int(time.Until(deadline).Milliseconds())))
				vScreen, cause = readScreen(t.Machine, t.TargetArg, "visible", 0, env, HerdrCallTimeoutMS, noteVisibleScreen)
				if cause != "" {
					log(senderRef, t.RefShown, "unreadable")
					platform.Die(fmt.Sprintf("send: could not read %s's screen (%s); nothing was sent", t.RefShown, cause), 4)
				}
				g := agentGet(t.Machine, t.TargetArg, env, HerdrCallTimeoutMS)
				if !g.OK {
					log(senderRef, t.RefShown, "error")
					platform.Die(fmt.Sprintf("send: %s unavailable: %s", t.RefShown, g.Cause), 4)
				}
				currentStatus = g.Status
			}
		}
		if isDialogScreen(vScreen, t.Kind, currentStatus) {
			log(senderRef, t.RefShown, "dialog")
			platform.Die(fmt.Sprintf("send: %s is showing a dialog; nothing was sent", t.RefShown), 17)
		}
	}
	preGet := agentGet(t.Machine, t.TargetArg, env, HerdrCallTimeoutMS)
	preSeq, preStatus := "", ""
	if preGet.OK {
		preSeq, preStatus = preGet.Seq, preGet.Status
		if isDialogScreen(vScreen, t.Kind, preStatus) {
			log(senderRef, t.RefShown, "dialog")
			platform.Die(fmt.Sprintf("send: %s is showing a dialog; nothing was sent", t.RefShown), 17)
		}
	}
	preScreen := vScreen
	// saveStalledScreen writes the last visible screen read to
	// <state>/wait/send-<id>.screen, next to spawn's shared-tree markers,
	// atomically: a temp file in the wait dir renamed over the target, so a
	// crash never leaves a half-written screen. It returns the path, or ""
	// when there is no read screen to record or the write fails: the exit
	// message then keeps today's form and nothing else is said. The screen
	// goes only to that local file, which the state dir's gitignore already
	// covers — never to stderr (but the path in the message), the peer log
	// or the friction.
	// saveStalledScreen writes lastVisibleScreen under the sender state
	// dir's wait dir (next to spawn's shared-tree markers) through the
	// package-level helper, whose NOWRITE guard keeps it writing nothing
	// when SOHO_NOWRITE is set — the CLI already refuses the send
	// with this flag; the guard is the helper's own defense.
	saveStalledScreen := func() string {
		return SaveStalledScreen(stateDir, id, lastVisibleScreen, env)
	}
	// stalledExitMessage appends the saved-screen note to a stalled 15
	// message when the screen was saved: the note cites the file's path, not
	// its content; a failed save keeps the message as-is.
	stalledExitMessage := func(base string) string {
		if path := saveStalledScreen(); path != "" {
			return base + "; screen saved to " + path
		}
		return base
	}
	clean := LiteralPeerText
	quotedBody := QuotePeerBody(clean(body))
	endLine := PeerEndLine(id)
	field := HeaderField
	sender := PeerSender{Ref: field(senderRef), Name: field(senderName), Kind: field(senderKind), Role: field(senderRole), Model: field(senderModel)}
	header := PeerHeaderFor(sender, id)
	if t.Machine != sessionref.LocalMachine {
		// A remote target would resolve the sender's "local/<pane>" back to
		// its own machine: name the pane on this machine's hostname instead,
		// and ask for this machine's name in the receiver's machine list.
		header = PeerHeaderRemoteFor(sender, strings.TrimPrefix(field(senderRef), sessionref.LocalMachine+"/"),
			field(senderHostname()), id)
	}
	message := header + "\n\n" + quotedBody + "\n" + endLine
	msgLines := strings.Count(message, "\n") + 1
	recentLines := msgLines + 60
	// A local Claude Code or pi target writes every taken message to its
	// session transcript as a user line that holds the peer marker, and the
	// screen can scroll the marker out of the visible area in the same turn:
	// a line the new id has not held before the send is delivery proof. The
	// id is new, so the pre-send count is zero. Without a transcript (no
	// agent_session, a session kind that is not a local file, or a file that
	// is missing or unreadable) the send keeps the screen-based path, with no
	// new warning. The transcript is local to the machine that runs the
	// session: ClaudeTranscriptPath and PiSessionPath resolve it with a local
	// `agent get` plus a local file, so the proof only applies to a local
	// target. A remote target with a pane of the same id locally would
	// otherwise be proved by another agent's transcript, so for it the proof
	// is not armed at all (no machineless get, no file read). The pi resolver
	// is consulted only when the pre-send get reports an agent_session:
	// without one the pi target keeps today's exact call sequence (no
	// transcript get, no file read).
	transcript := transcriptProof{}
	if t.Machine == sessionref.LocalMachine || t.Machine == "" {
		if t.Kind == "claude" {
			transcript = transcriptProof{path: herdr.ClaudeTranscriptPath(t.TargetArg, env), count: herdr.CountClaudeUserMarkerLines, queued: claudeQueued}
		} else if t.Kind == "pi" && preGet.OK && preGet.SessionKind != "" {
			transcript = transcriptProof{path: herdr.PiSessionPath(t.TargetArg, env), count: herdr.CountPiUserMarkerLines, queued: steeringQueued}
		}
		if transcript.path != "" {
			if pre, ok := transcript.count(transcript.path, "#"+id); ok {
				transcript.pre = pre
				transcript.armed = true
			}
		}
	}
	windowMS, pollMS := arrivalWindowMS(env), arrivalPollMS(env)
	pollWindow := func() proofResult {
		deadline := time.Now().Add(time.Duration(windowMS) * time.Millisecond)
		result := proofResult{}
		for {
			curGet := agentGet(t.Machine, t.TargetArg, env, HerdrCallTimeoutMS)
			if curGet.OK {
				preIdle := preStatus == "idle" || preStatus == "done"
				// An idle/done agent's state sequence moves only when its
				// state changes: a new seq since preGet means it just ran a
				// turn (or took the prompt into a queue). A short turn can be
				// over before the first state read, so the status no longer
				// has to be working; the visible screen read below must still
				// succeed and show no dialog.
				if preSeq != "" && curGet.Seq != "" && curGet.Seq != preSeq && preIdle {
					visible, readErr := readScreen(t.Machine, t.TargetArg, "visible", 0, env, HerdrCallTimeoutMS, noteVisibleScreen)
					if readErr == "" {
						if !isDialogScreen(visible, t.Kind, curGet.Status) {
							return proofResult{Proven: true, RecentReadSucceeded: result.RecentReadSucceeded, LastCause: result.LastCause}
						}
					} else {
						result.LastCause = readErr
					}
				}
			} else if curGet.Cause != "" {
				result.LastCause = curGet.Cause
			}
			if transcript.armed {
				if count, ok := transcript.count(transcript.path, "#"+id); ok && count > transcript.pre {
					// The target also writes the taken message to the transcript
					// while it sits unread in its open queue: the count growth is
					// not proof while the visible queue line still holds the id.
					visible, visibleErr := readScreen(t.Machine, t.TargetArg, "visible", 0, env, HerdrCallTimeoutMS, noteVisibleScreen)
					if visibleErr == "" {
						if !transcript.queued(visible, id) {
							return proofResult{Proven: true, RecentReadSucceeded: result.RecentReadSucceeded, LastCause: result.LastCause}
						}
					} else {
						result.LastCause = visibleErr
					}
				}
			}
			recent, readErr := readScreen(t.Machine, t.TargetArg, "recent-unwrapped", recentLines, env, HerdrCallTimeoutMS, noteVisibleScreen)
			if readErr == "" {
				result.RecentReadSucceeded = true
				// The recent gate requires this message's receipt marker (header
				// opening or closing line, either closing form, unquoted, exact
				// id): a bare "#id" a reply or a footer can cite is not it.
				if receiptLineInText(recent, id) {
					visible, visibleErr := readScreen(t.Machine, t.TargetArg, "visible", 0, env, HerdrCallTimeoutMS, noteVisibleScreen)
					if visibleErr == "" {
						if visible != preScreen && !isDialogScreen(visible, t.Kind, statusOr(curGet, currentStatus)) {
							// A message in pi's Steering queue, in Claude Code's queued
							// messages, or in codex's follow-up queue sits above the input
							// area but is not read yet: it is queued (reported after the
							// window), not delivered.
							if !messageStillInScreen(t.Kind, visible, endLine, id) && !steeringQueued(visible, id) && !claudeQueued(visible, id) &&
								!(t.Kind == "codex" && codexQueued(visible, id)) {
								return proofResult{Proven: true, RecentReadSucceeded: true, LastCause: result.LastCause}
							}
						}
					} else {
						result.LastCause = visibleErr
					}
				}
			} else if readErr != "" {
				result.LastCause = readErr
			}
			if !time.Now().Before(deadline) {
				break
			}
			sleepMS(min(pollMS, int(time.Until(deadline).Milliseconds())))
		}
		return result
	}
	// stalledCheckBudget caps one read or get of a stalled check at the time
	// left in the proof window, floored at one second so the call can still
	// finish: the window is at most 15s, below the 30s the calls would
	// otherwise wait, so the cap is what bounds them.
	stalledCheckBudget := func(deadline time.Time) int {
		return floorCallBudget(int(time.Until(deadline).Milliseconds()))
	}
	// stalledTaken checks, with the same rules pollWindow applies, whether a
	// stalled prompt was taken anyway: a state sequence that moved from an
	// idle baseline and a clean visible screen, or the marker in the recent
	// history and a visible screen that shows it taken — changed since the
	// pre-send read, no dialog, and the marker out of the input box and the
	// queues. The local transcript proof, like pollWindow's, is consulted
	// before the screen rules and presses no key: for a claude or pi whose
	// transcript is armed, the count of the user lines holding this id
	// growing is delivery, unless the visible queue line still holds the id
	// (queued, not taken). The stalled path never resends the text and presses
	// no key, but the target's screen can still redraw late — a slow agent
	// takes more than the prompt wait to start working and to draw the message
	// — so one check can read a screen that does not show the arrival yet: the
	// check repeats on every poll until the end of the proof window
	// (stalledWindow). A screen that cannot be read proves nothing and does
	// not interrupt the wait: the marker could still sit in the composer,
	// which is not sent. The read screen must show the marker out of the input
	// box and the queues before the moved-sequence shortcut decides: a read
	// that still holds the marker with the sequence moved is not taken.
	// Every read and get of the check is capped at the time then left in the
	// proof window (stalledCheckBudget), so a slow read cannot push the window
	// far past its deadline; a screen that fails to read within it is not a
	// proof.
	stalledTaken := func(deadline time.Time) bool {
		visible, visibleErr := readScreen(t.Machine, t.TargetArg, "visible", 0, env, stalledCheckBudget(deadline), noteVisibleScreen)
		if visibleErr != "" {
			return false
		}
		// The transcript proof, like pollWindow's, applies before the screen
		// rules and presses no key: the target writes the taken message to the
		// transcript as a user line, so the count of those lines holding this
		// id growing is delivery — unless the visible queue line still holds
		// the id, which is queued, not taken (the same growth-not-proof rule
		// the window applies).
		if transcript.armed {
			if count, ok := transcript.count(transcript.path, "#"+id); ok && count > transcript.pre {
				return !transcript.queued(visible, id)
			}
		}
		// The moved-sequence shortcut below checks only the dialog, so the
		// taken rules are required up front: a marker still in the input box or
		// in a queue on the read screen is not taken, whatever the sequence
		// says.
		if messageStillInScreen(t.Kind, visible, endLine, id) || steeringQueued(visible, id) || claudeQueued(visible, id) ||
			(t.Kind == "codex" && codexQueued(visible, id)) {
			return false
		}
		curGet := agentGet(t.Machine, t.TargetArg, env, stalledCheckBudget(deadline))
		preIdle := preStatus == "idle" || preStatus == "done"
		if preSeq != "" && curGet.Seq != "" && curGet.Seq != preSeq && preIdle {
			return !isDialogScreen(visible, t.Kind, curGet.Status)
		}
		// Like pollWindow's recent gate, the marker must be this message's
		// receipt marker (header opening or closing line, either closing form,
		// unquoted, exact id), not a bare "#id" a reply can cite.
		recent, readErr := readScreen(t.Machine, t.TargetArg, "recent-unwrapped", recentLines, env, stalledCheckBudget(deadline), noteVisibleScreen)
		if readErr != "" || !receiptLineInText(recent, id) {
			return false
		}
		return visible != preScreen && !isDialogScreen(visible, t.Kind, statusOr(curGet, currentStatus)) &&
			!messageStillInScreen(t.Kind, visible, endLine, id) && !steeringQueued(visible, id) && !claudeQueued(visible, id) &&
			!(t.Kind == "codex" && codexQueued(visible, id))
	}
	// stalledWindow repeats stalledTaken on every poll until the end of the
	// same proof window pollWindow runs (windowMS, from arrivalWindowMS):
	// the prompt call gave up, but the target can still take the message — a
	// slow agent starts working and draws it more than the prompt wait later
	// — so the check reads the screen again on every poll until it proves
	// the arrival or the window ends. The deadline is checked before every
	// new check: a check starts only while time is left in the window, and
	// each of its reads and gets is capped at the time then left
	// (stalledCheckBudget), so a slow read cannot push the window far past
	// its deadline. The first taken read is sent; the window's end without
	// proof keeps the stalled 15 below. No key is pressed and the text is
	// never resent.
	stalledWindow := func() bool {
		deadline := time.Now().Add(time.Duration(windowMS) * time.Millisecond)
		for {
			if !time.Now().Before(deadline) {
				return false
			}
			if stalledTaken(deadline) {
				return true
			}
			sleepMS(min(pollMS, int(time.Until(deadline).Milliseconds())))
		}
	}
	p := deliverPrompt(t.Machine, t.TargetArg, message, env)
	if p.Code != "ok" {
		if p.Code == "agent_prompt_stalled" {
			// agent_prompt_stalled can mean the prompt was typed but its
			// Enter never landed: the message then sits in the input box
			// while the state stays idle. Read the target's visible screen
			// (with the target's machine, as the rest of the send does)
			// and, only when this message's marker is in the box, press one
			// Enter and run the proof window. One Enter at most, no resend;
			// agent_blocked and timeout keep today's exit below.
			vis, visErr := readScreen(t.Machine, t.TargetArg, "visible", 0, env, HerdrCallTimeoutMS, noteVisibleScreen)
			if visErr == "" && idInInputBox(t.Kind, vis, id) {
				// A dialog on screen takes the Enter as its answer: press
				// nothing, as the Enter of the ok path does.
				if isDialogScreen(vis, t.Kind, "idle") {
					log(senderRef, t.RefShown, "dialog")
					platform.Die(fmt.Sprintf("send: %s is showing a dialog after the message was typed; press nothing and read its pane", t.RefShown), 17)
				}
				// The key transport reports whether the Enter actually landed
				// (herdr send-keys exited 0); a failed send is never claimed as
				// pressed, and no second Enter or resend follows.
				entered := sendKey(t.Machine, t.TargetArg, "enter", env)
				if res := pollWindow(); res.Proven {
					log(senderRef, t.RefShown, "sent")
					_, _ = fmt.Fprintf(platform.Stdout, "sent to %s\n", t.RefShown)
					return 0
				}
				log(senderRef, t.RefShown, "stalled")
				cause := "the last read showed it in its input box after one Enter"
				if !entered {
					cause = "the last read showed it in its input box, but the Enter did not send"
				}
				platform.Die(stalledExitMessage(fmt.Sprintf("send: %s did not confirm taking the message: %s; delivery is uncertain; read its pane for #%s or a reply before sending again", t.RefShown, cause, id)), 15)
			}
			// The marker is not in the input box (or the screen could not be
			// read), yet the prompt can have been taken anyway: a taken codex
			// message sits in the history above an empty composer, and the
			// box shows no marker at all — the rc.12 case exited 15 "stalled"
			// while the message was received, and the D15 case read the old
			// screen while the codex was already working. The screen can
			// redraw late, so prove the arrival over the proof window: the
			// check repeats on every poll until it proves or the window ends;
			// taken is sent, not taken by the end keeps the 15.
			if stalledWindow() {
				log(senderRef, t.RefShown, "sent")
				_, _ = fmt.Fprintf(platform.Stdout, "sent to %s\n", t.RefShown)
				return 0
			}
		}
		if p.Code == "agent_prompt_stalled" || p.Code == "agent_blocked" || p.Code == "timeout" {
			result := map[string]string{"agent_prompt_stalled": "stalled", "agent_blocked": "blocked", "timeout": "timeout"}[p.Code]
			log(senderRef, t.RefShown, result)
			cause := p.Cause
			if p.Code == "timeout" {
				cause = "timeout"
			}
			// The prompt call failed before proof ran: the text may or may not
			// sit in the target's composer, so the message says delivery is
			// uncertain and names the reconciliation (the marker or a reply),
			// never that the message was not delivered.
			msg := fmt.Sprintf("send: %s did not confirm taking the message (%s); delivery is uncertain; read its pane for #%s or a reply before sending again", t.RefShown, cause, id)
			// Only the stalled exits record the screen: agent_blocked and
			// timeout keep today's message untouched.
			if p.Code == "agent_prompt_stalled" {
				// The proof window ran and ended without proof: say so, in
				// seconds. The cause keeps Herdr's own "within ... ms" text,
				// which is part of the cause.
				msg = fmt.Sprintf("send: %s did not confirm taking the message (%s); delivery is uncertain: no proof within the %ss window; read its pane for #%s or a reply before sending again", t.RefShown, cause, numberSeconds(windowMS), id)
				msg = stalledExitMessage(msg)
			}
			platform.Die(msg, 15)
		}
		log(senderRef, t.RefShown, "error")
		platform.Die(fmt.Sprintf("send: %s unavailable: %s", t.RefShown, p.Cause), 4)
	}
	res := pollWindow()
	if res.Proven {
		log(senderRef, t.RefShown, "sent")
		_, _ = fmt.Fprintf(platform.Stdout, "sent to %s\n", t.RefShown)
		return 0
	}
	// Claude Code can hold a sent message until a long tool call ends and only
	// then write the user line to its session transcript, later than the
	// proof window: the rc.12 claude-on-a-37s-tool case exited 15 (lost) while
	// the message was received. This wait stays claude's: a working pi shows
	// its Steering queue on the screen, and the path below returns queued for
	// it. When the transcript proof is armed (a local claude with a resolved
	// transcript) and the window ends without proof while the target is still working, keep consulting the transcript until
	// the command --timeout — the same value as the busy-target wait — for any
	// window, a sub-second one included: a count growth with no queue line on
	// screen is sent, and a visible queue line is queued, before the wait's
	// laps and on every lap of it, independent of the count. No key is sent in
	// the wait, and a target that leaves working without the growth — or the
	// deadline — follows the path below, today's outcome. The busy warning
	// announces a real transcript wait: it goes once, at the entry while the
	// transcript has not shown the message yet, or at the first lap that ends
	// undecided (a grown count whose screen cannot be read); a growth whose
	// queue decision is immediate waits for nothing and says nothing.
	if t.Kind == "claude" && transcript.armed {
		if busy := agentGet(t.Machine, t.TargetArg, env, HerdrCallTimeoutMS); busy.OK && busy.Status == "working" {
			deadline := time.Now().Add(time.Duration(timeoutMS) * time.Millisecond)
			warned := false
			warnBusy := func() {
				if !warned {
					warned = true
					_, _ = fmt.Fprintf(platform.Stderr, "send: %s is busy; waiting for its transcript to show the message (up to %ss)\n", t.RefShown, numberSeconds(timeoutMS))
				}
			}
			if count, ok := herdr.CountClaudeUserMarkerLines(transcript.path, "#"+id); !(ok && count > transcript.pre) {
				warnBusy()
			}
			// The queue line, like the count growth, proves the queued outcome
			// and is due before the wait's laps and with no key: the same
			// proof the path below returns after the window.
			if visible, visibleErr := readScreen(t.Machine, t.TargetArg, "visible", 0, env, HerdrCallTimeoutMS, noteVisibleScreen); visibleErr == "" && claudeQueued(visible, id) {
				log(senderRef, t.RefShown, "queued")
				_, _ = fmt.Fprintf(platform.Stdout, "queued for %s: it takes the message when its current turn ends\n", t.RefShown)
				return 0
			}
			for time.Now().Before(deadline) {
				// The queue line is checked on every lap, independent of the
				// count growth: the same queued proof, due as soon as it is
				// visible.
				if visible, visibleErr := readScreen(t.Machine, t.TargetArg, "visible", 0, env, HerdrCallTimeoutMS, noteVisibleScreen); visibleErr == "" && claudeQueued(visible, id) {
					log(senderRef, t.RefShown, "queued")
					_, _ = fmt.Fprintf(platform.Stdout, "queued for %s: it takes the message when its current turn ends\n", t.RefShown)
					return 0
				}
				if count, ok := herdr.CountClaudeUserMarkerLines(transcript.path, "#"+id); ok && count > transcript.pre {
					// As in the window's transcript check, the count growth is
					// not proof while the visible queue line still holds the id.
					if visible, visibleErr := readScreen(t.Machine, t.TargetArg, "visible", 0, env, HerdrCallTimeoutMS, noteVisibleScreen); visibleErr == "" {
						if claudeQueued(visible, id) {
							log(senderRef, t.RefShown, "queued")
							_, _ = fmt.Fprintf(platform.Stdout, "queued for %s: it takes the message when its current turn ends\n", t.RefShown)
							return 0
						}
						log(senderRef, t.RefShown, "sent")
						_, _ = fmt.Fprintf(platform.Stdout, "sent to %s\n", t.RefShown)
						return 0
					}
				}
				if g := agentGet(t.Machine, t.TargetArg, env, HerdrCallTimeoutMS); !g.OK || g.Status != "working" {
					break
				}
				if !time.Now().Before(deadline) {
					break
				}
				// A lap that ends undecided is a real wait (a grown count whose
				// screen cannot be read yet included): say so once.
				warnBusy()
				sleepMS(min(pollMS, int(time.Until(deadline).Milliseconds())))
			}
		}
	}
	if !res.RecentReadSucceeded {
		log(senderRef, t.RefShown, "unverified")
		cause := res.LastCause
		if cause == "" {
			cause = "read failed"
		}
		platform.Die(fmt.Sprintf("send: could not confirm that %s took the message (%s); read its pane before sending again", t.RefShown, cause), 15)
	}
	preEnterVis, readErr := readScreen(t.Machine, t.TargetArg, "visible", 0, env, HerdrCallTimeoutMS, noteVisibleScreen)
	if readErr == "" && (steeringQueued(preEnterVis, id) || claudeQueued(preEnterVis, id) || (t.Kind == "codex" && codexQueued(preEnterVis, id))) {
		// pi keeps a message sent during a turn in its Steering queue, Claude
		// Code keeps it in its queued messages, and codex keeps it in its
		// follow-up queue: it is queued, not lost, and not yet read; no Enter
		// (and no ctrl+enter) goes to a busy agent.
		log(senderRef, t.RefShown, "queued")
		_, _ = fmt.Fprintf(platform.Stdout, "queued for %s: it takes the message when its current turn ends\n", t.RefShown)
		return 0
	}
	if readErr != "" {
		log(senderRef, t.RefShown, "unverified")
		platform.Die(fmt.Sprintf("send: could not confirm that %s took the message (%s); read its pane before sending again", t.RefShown, readErr), 15)
	}
	preEnterGet := agentGet(t.Machine, t.TargetArg, env, HerdrCallTimeoutMS)
	if !preEnterGet.OK {
		log(senderRef, t.RefShown, "unverified")
		cause := preEnterGet.Cause
		if cause == "" {
			cause = "status read failed"
		}
		platform.Die(fmt.Sprintf("send: could not confirm that %s took the message (%s); read its pane before sending again", t.RefShown, cause), 15)
	}
	if isDialogScreen(preEnterVis, t.Kind, preEnterGet.Status) {
		log(senderRef, t.RefShown, "dialog")
		platform.Die(fmt.Sprintf("send: %s is showing a dialog after the message was typed; press nothing and read its pane", t.RefShown), 17)
	}
	idInBox := idInInputBox(t.Kind, preEnterVis, id)
	if !idInBox {
		log(senderRef, t.RefShown, "lost")
		platform.Die(fmt.Sprintf("send: %s did not confirm taking the message (no sign of it in its state or screen); delivery is uncertain; read its pane for #%s or a reply before sending again", t.RefShown, id), 15)
	}
	// The key transport reports whether the Enter actually landed (herdr
	// send-keys exited 0); a failed send is never claimed as pressed, and no
	// second Enter or resend follows.
	entered := sendKey(t.Machine, t.TargetArg, "enter", env)
	res = pollWindow()
	if res.Proven {
		log(senderRef, t.RefShown, "sent")
		_, _ = fmt.Fprintf(platform.Stdout, "sent to %s\n", t.RefShown)
		return 0
	}
	if !res.RecentReadSucceeded {
		log(senderRef, t.RefShown, "unverified")
		cause := res.LastCause
		if cause == "" {
			cause = "read failed"
		}
		platform.Die(fmt.Sprintf("send: could not confirm that %s took the message (%s); read its pane before sending again", t.RefShown, cause), 15)
	}
	log(senderRef, t.RefShown, "lost")
	lostCause := "no sign of it in its state or screen"
	if !entered {
		lostCause = "the Enter did not send and there is no sign of it in its state or screen"
	}
	platform.Die(fmt.Sprintf("send: %s did not confirm taking the message (%s); delivery is uncertain; read its pane for #%s or a reply before sending again", t.RefShown, lostCause, id), 15)
	return 15
}

// piInputRegion returns the visible screen's lines between the last two lines
// composed only of '─' (U+2500), ignoring whitespace: pi's input box, whose
// borders are those two separator lines (chat history above, footer below).
// ok is false when the screen has fewer than two such lines; the caller then
// keeps the conservative behavior. The region rule is the shared composer
// recognition in provider (provider.ComposerRegion); this adapter hands the
// lines back.
func piInputRegion(screen string) ([]string, bool) {
	return provider.ComposerRegion("pi", screen)
}

// claudeInputRegion returns the visible screen's lines between the two box
// border lines Claude Code draws around its input box (chat history above,
// footer below), with the shared provider's box recognition: the borders are
// lines of '─' (U+2500) within the last 12 non-empty lines, where they must
// be the last two border lines, and the pair has to hold the composer — the
// first non-empty line between the borders, without its leading spaces,
// starts with ❯ and no line below the bottom border may start with it (a ❯
// below means the composer sits there and the '─' pair is a history
// separator or a table, not the box). ok is false when the pair fails the
// recognition; the caller then keeps the conservative behavior.
func claudeInputRegion(visible string) ([]string, bool) {
	return provider.ComposerRegion("claude", visible)
}

// claudeBoxScope is where a claude peer marker still counts as typed and
// not sent: the box between the two border lines (the shared provider
// recognition, claudeInputRegion) plus every line below the box's bottom
// border that carries the full peer header. Below the real box sits only the
// status footer, which never holds a peer marker; a composer drawn with
// another prompt under a history '─' pair keeps its typed marker in scope,
// so the check stays conservative.
func claudeBoxScope(visible string) ([]string, bool) {
	lines := strings.Split(strings.ReplaceAll(visible, "\r\n", "\n"), "\n")
	start, end, ok := provider.ComposerRegionBounds("claude", visible)
	if !ok {
		return nil, false
	}
	scope := append([]string{}, lines[start:end]...)
	// Below the box only a line with the full peer header counts: a bare
	// #<id> in the status footer (a branch like topic/#0a1b2c3d) is not
	// the typed message.
	for _, line := range lines[start:] {
		norm := NormalizeScreen(line)
		if strings.Contains(norm, NormalizeScreen(PeerPrefix)) {
			scope = append(scope, line)
		}
	}
	return scope, true
}

// codexComposerRegion returns the visible screen's lines of the codex
// composer input, with the shared provider's composer recognition: the
// composer starts at the last line whose text, without its left spaces,
// begins with "› " (U+203A space) or is just "›", and that line must sit
// inside the last 8 non-empty screen lines; the region's end is what the
// shared recognition reports (ComposerRegionBounds), which need not be the
// end of the screen. ok is false when no such line is there; the caller
// then keeps the conservative behavior. A taken message sits in the history
// above the composer, so the composer region is what holds the prompt until
// it is taken; this adapter hands the lines back like piInputRegion.
func codexComposerRegion(visible string) ([]string, bool) {
	return provider.ComposerRegion("codex", visible)
}

// peerEndLineIn reports whether the line is this message's closing line in
// its marker form: "[/soho:peer] #<id> end of message". The match is anchored to
// the whole line: after NormalizeScreen a real closing line is the marker
// itself (with an optional leading history "›"), so a prose line, an
// assistant bullet or a history echo that merely contains the marker is not
// it. The id must be this message's exact id: an unrelated id, or a longer
// id that only has this one as a prefix, is not this message's end. A quoted
// line ("> ...") never counts: the quoted body of a peer message is payload,
// and a closing marker quoted into it or glued onto its end without a line
// break (the rc12 shape) is never positive receipt, however the terminal
// drew it.
func peerEndLineIn(line, id string) bool {
	norm := NormalizeScreen(line)
	if strings.HasPrefix(norm, ">") {
		return false // quoted payload: a marker in it is part of the quoted body
	}
	for _, marker := range peerEndReceiptMarkers {
		open := marker + "#" + id + "endofmessage"
		if norm == open || norm == "›"+open {
			return true // the marker is the whole line (optional history "›")
		}
	}
	return false
}

// isHexRune reports whether the rune is a hex digit. The message ids are hex,
// so a hex digit right after one of them is a longer id, not the end of it.
func isHexRune(r rune) bool {
	return r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F'
}

// receiptLineIn reports whether the line carries a receipt marker of this
// message: its closing line (peerEndLineIn, anchored to the whole line) or
// the opening of its header ("[soho:peer] #<id>"), anchored the same way:
// after NormalizeScreen the real line starts with the marker (an optional
// leading history "›" stripped), and the rune after the opening id is not a
// hex digit, so a longer id that only has this one as a prefix is not it. A
// quoted line never counts: the quoted body of a peer message is payload and
// can carry a spoofed opening or closing line, no matter how the terminal
// drew it. The id must be this message's exact id in both: an unrelated id is
// not the receipt of this send. This is the marker the recent-history gate
// of the proof window and the stalled check require: a bare "#<id>" a
// reply, a footer, a prose line or another message can cite is not it.
func receiptLineIn(line, id string) bool {
	norm := NormalizeScreen(line)
	if !strings.HasPrefix(norm, ">") {
		rest := strings.TrimPrefix(norm, "›")
		for _, prefix := range peerOpenMarkers {
			open := prefix + "#" + id
			if strings.HasPrefix(rest, open) {
				after := rest[len(open):]
				if after == "" || !isHexRune(rune(after[0])) {
					return true // the header opens the line; the id is complete
				}
			}
		}
	}
	return peerEndLineIn(line, id)
}

// receiptLineInText reports whether any line of a screen or a recent read
// carries a receipt marker of this message (receiptLineIn).
func receiptLineInText(text, id string) bool {
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if receiptLineIn(line, id) {
			return true
		}
	}
	return false
}

// peerMessageInHistory reports that the visible screen shows this message's
// peer prompt as a delivered codex turn: its closing line (peerEndLineIn)
// and a codex turn line after it (working, done, or the reply
// — the `•` lines). The header is not required: a truncated screen can hold
// the closing line and the turn with the header scrolled off, and the
// closing line carries the exact id, so the end line alone is the receipt.
// On codex a user message in the history starts with `› ` just like the
// composer, and while the agent works the real composer can be absent from
// the captured screen, so the history's `›` line would otherwise pass for
// the composer and a delivered message would read as still typed in the box
// (D15c). A composer line that still holds this message's typed prompt after
// the end line means the prompt is in the box (the Enter can still deliver
// it), and a prompt with no turn after its end line — nothing but the status
// lines — keeps reading as the box.
func peerMessageInHistory(visible, id string) bool {
	lines := strings.Split(strings.ReplaceAll(visible, "\r\n", "\n"), "\n")
	headerNorms := []string{NormalizeScreen(PeerPrefix + " #" + id)}
	for i, line := range lines {
		if !peerEndLineIn(line, id) {
			continue
		}
		delivered := false
		for _, below := range lines[i+1:] {
			belowNorm := NormalizeScreen(below)
			head := strings.TrimLeft(belowNorm, " \t")
			for _, headerNorm := range headerNorms {
				if strings.HasPrefix(head, "›") && strings.Contains(belowNorm, headerNorm) {
					return false // the composer below still holds the typed prompt
				}
			}
			if strings.HasPrefix(head, "•") {
				delivered = true
			}
		}
		if delivered {
			return true
		}
	}
	return false
}

// messageStillInScreen reports whether the visible screen still holds the
// message's marker, meaning the prompt is typed but not taken yet. For a pi
// target the marker only counts inside the input box, for a claude target
// inside its box scope (claudeBoxScope), and for a codex target inside the
// composer region: the shared provider recognition of the kind's composer,
// so a taken message stays in the chat history, which is part of the visible
// screen. A peer message shown as a delivered codex turn
// (peerMessageInHistory) counts as taken, not held. Without the recognized
// composer, and for every other kind, the whole visible screen still counts:
// unknown input stays conservative and is never positive receipt.
func messageStillInScreen(kind, visible, endLine, id string) bool {
	if kind == "codex" && peerMessageInHistory(visible, id) {
		return false
	}
	var lines []string
	var ok bool
	if kind == "claude" {
		lines, ok = claudeBoxScope(visible)
	} else {
		lines, ok = provider.ComposerRegion(kind, visible)
	}
	if ok {
		region := NormalizeScreen(strings.Join(lines, "\n"))
		return strings.Contains(region, NormalizeScreen("#"+id)) || strings.Contains(region, NormalizeScreen(endLine))
	}
	visibleNorm := NormalizeScreen(visible)
	return strings.Contains(visibleNorm, NormalizeScreen(endLine)) || strings.Contains(visibleNorm, NormalizeScreen("#"+id))
}

// idInInputBox reports whether a retry Enter may go to this screen for this
// message: only a recognized actual composer (the shared provider recognition
// for the kind — pi's input box, the claude box scope, the codex composer
// region) holding this exact message's header (composerHeaderLineIn: a line
// that opens with "[soho:peer] #<id>", unquoted, the id complete — a
// hex digit right after it is a longer id, not this one) licenses the key.
// A marker in a footer or in older history sits outside the recognized
// composer; an unrecognized layout, an unknown kind, and a known
// trust/approval/question UI (provider.DialogUI) refuse it the same way.
// Uncertainty is not permission to press Enter: without the recognized
// composer there is no Enter — the last-fifteen-lines fallback is gone. A
// peer message shown as a delivered codex turn (peerMessageInHistory) is
// history, not the input area, so it never counts as in the box.
func idInInputBox(kind, visible, id string) bool {
	if id == "" {
		return false
	}
	// A known trust, approval, or question UI takes the Enter as its answer:
	// the shared classifier refuses it, as the startup and dialog checks do.
	if provider.DialogUI(kind, visible) != "" {
		return false
	}
	if kind == "codex" && peerMessageInHistory(visible, id) {
		return false
	}
	var lines []string
	var ok bool
	if kind == "claude" {
		lines, ok = claudeBoxScope(visible)
	} else {
		lines, ok = provider.ComposerRegion(kind, visible)
	}
	if !ok {
		return false
	}
	for _, line := range lines {
		if composerHeaderLineIn(line, id) {
			return true
		}
	}
	return false
}

// composerHeaderLineIn reports whether a line of a recognized composer
// region is this message's header opening: after NormalizeScreen, with an
// optional leading composer prompt glyph (the claude "❯" or the codex "›")
// stripped, the line starts with "[soho:peer] #<id>" and the rune right
// after the id is not a hex digit, so a longer id that only has this one as
// a prefix is not it. A quoted line ("> ...") never counts: the quoted body
// of a peer message is payload and can carry a spoofed header, no matter how
// the terminal drew it.
func composerHeaderLineIn(line, id string) bool {
	norm := NormalizeScreen(line)
	if strings.HasPrefix(norm, ">") {
		return false // quoted payload: a header in it is part of the quoted body
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(norm, "❯"), "›")
	for _, prefix := range peerOpenMarkers {
		open := prefix + "#" + id
		if strings.HasPrefix(rest, open) {
			after := rest[len(open):]
			if after == "" || !isHexRune(rune(after[0])) {
				return true
			}
		}
	}
	return false
}

// steeringQueued reports a visible pi queue line (`Steering: …`) that holds
// this message's id.
func steeringQueued(visible, id string) bool {
	for _, line := range strings.Split(visible, "\n") {
		head := strings.TrimLeft(NormalizeScreen(line), " \t")
		if strings.HasPrefix(head, "Steering:") && strings.Contains(head, NormalizeScreen("#"+id)) {
			return true
		}
	}
	return false
}

// claudeQueued reports a visible Claude Code queue: the screen holds this
// message's id and the line Claude Code shows while a working turn holds
// sent messages until it ends.
func claudeQueued(visible, id string) bool {
	if !strings.Contains(NormalizeScreen(visible), NormalizeScreen("#"+id)) {
		return false
	}
	for _, line := range strings.Split(visible, "\n") {
		if strings.Contains(NormalizeScreen(line), NormalizeScreen("Press up to edit queued messages")) {
			return true
		}
	}
	return false
}

// codexQueued reports a visible codex follow-up queue line above the
// composer: a line whose text, without its left spaces, begins with "↳"
// (U+21B3) and holds this message's id. The message is enqueued for a later
// turn, not delivered yet. The "above the composer" bound is the actual
// composer start from the shared recognition (ComposerRegionBounds), not a
// length derived from the region — the region need not run to the screen
// end, so only lines strictly before that start count as the queue. Without
// a recognized composer, no line proves a queued receipt.
func codexQueued(visible, id string) bool {
	lines := strings.Split(strings.ReplaceAll(visible, "\r\n", "\n"), "\n")
	start, _, ok := provider.ComposerRegionBounds("codex", visible)
	if !ok {
		start = 0
	}
	want := NormalizeScreen("#" + id)
	for _, line := range lines[:start] {
		head := strings.TrimLeft(NormalizeScreen(line), " \t")
		if strings.HasPrefix(head, "↳") && strings.Contains(head, want) {
			return true
		}
	}
	return false
}

func statusOr(info agentInfo, fallback string) string {
	if info.OK {
		return info.Status
	}
	return fallback
}

func parsePositiveInt(value string) (int, bool) {
	number, ok := textutil.ParseJSNumber(value)
	if !ok || math.IsNaN(number) || math.IsInf(number, 0) || number <= 0 || math.Trunc(number) != number || number >= float64(int(^uint(0)>>1)) {
		return 0, false
	}
	return int(number), true
}

func CmdFind(argv []string, env platform.Env) int {
	words := []string{}
	machines := []string{sessionref.LocalMachine}
	all, jsonOutput := false, false
	timeoutMS := 0
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		switch a {
		case "--all":
			all = true
		case "--json":
			jsonOutput = true
		case "--machine":
			if i+1 >= len(argv) || argv[i+1] == "" || strings.HasPrefix(argv[i+1], "--") {
				platform.DieFriction("usage: find [search words] [--machine <label>]... [--all] [--json] [--timeout MS]", 2)
			}
			i++
			if !contains(machines, argv[i]) {
				machines = append(machines, argv[i])
			}
		case "--timeout":
			// Rejected before any herdr invocation: an invalid deadline must
			// not cost a single subprocess.
			if i+1 >= len(argv) || argv[i+1] == "" || strings.HasPrefix(argv[i+1], "--") {
				platform.DieFriction("find: --timeout expects the deadline in milliseconds or a duration such as 90s, 30m or 2h", 2)
			}
			i++
			raw := argv[i]
			ms, ok := core.ParseTimeoutMS(raw)
			if ok && ms > int64(math.MaxInt) {
				ok = false // beyond the int range, as a plain number would be
			}
			if !ok {
				// The JS Number() spellings (1e3, 12.0, 0x10) stay accepted as
				// plain milliseconds; a unit needs ParseTimeoutMS.
				if n, ok2 := parsePositiveInt(raw); !ok2 {
					platform.DieFriction("find: --timeout expects the deadline in milliseconds or a duration such as 90s, 30m or 2h", 2)
				} else {
					ms = int64(n)
				}
			}
			if ms <= 0 {
				platform.DieFriction("find: --timeout expects the deadline in milliseconds or a duration such as 90s, 30m or 2h", 2)
			}
			timeoutMS = int(ms)
		default:
			if strings.HasPrefix(a, "--") {
				platform.DieFriction("usage: find [search words] [--machine <label>]... [--all] [--json] [--timeout MS]", 2)
			}
			words = append(words, a)
		}
	}
	if len(words) == 1 {
		if ref := sessionref.ParseRef(words[0]); ref != nil && ref.Machine != sessionref.LocalMachine && !contains(machines, ref.Machine) {
			machines = append(machines, ref.Machine)
		}
	}
	// Progressive discovery: the local rows are published before the
	// enumeration, each remote machine's rows as it completes, all under one
	// global deadline (DiscoverTimeoutMS without --timeout).
	result := DiscoverSessions(DiscoverOptions{Env: env, Machines: machines, All: all, TimeoutMS: timeoutMS}, func(batch SessionResult) {
		for _, failure := range batch.Failures {
			_, _ = fmt.Fprintf(platform.Stderr, "soho: find: machine '%s' unavailable: %s\n", failure.Machine, failure.Cause)
		}
		for _, e := range MatchEntries(batch.Entries, words) {
			if jsonOutput {
				_, _ = fmt.Fprintln(platform.Stdout, jsonjs.Stringify(entryJSON(e)))
			} else {
				_, _ = fmt.Fprintf(platform.Stdout, "%s\n", strings.Join([]string{tsv(e.Ref), tsv(e.Name), tsv(e.Kind), tsv(e.Status), tsv(e.WorkspaceLabel), tsv(e.TabLabel), tsv(e.Cwd)}, "\t"))
			}
		}
	})
	if result.ListCause != "" {
		_, _ = fmt.Fprintf(platform.Stderr, "soho: find: machine list failed: %s\n", result.ListCause)
	}
	for _, f := range result.Failures {
		if f.Machine == sessionref.LocalMachine {
			return 4
		}
	}
	if len(MatchEntries(result.Entries, words)) > 0 {
		return 0
	}
	return 1
}

func tsv(value any) string {
	if value == nil || value == "" {
		return "-"
	}
	return valueString(value)
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

// sendBusyBudgetMS is the busy wait's budget: the full --timeout without an
// agent_not_ready wait, else what is left before the shared deadline (never
// floored up). wait is false when nothing is left.
// notReadyRetryOutOfBudget reports whether an agent_not_ready retry that
// failed otherwise ran out of the send's budget. The retry's get is bounded
// by what was left, cut to whole milliseconds, so a get that timed out spent
// the budget even when the clock still reads a fraction of a millisecond
// before the deadline.
func notReadyRetryOutOfBudget(next resolvedTarget, deadline, now time.Time) bool {
	if next.OK || next.Code == "agent_not_ready" {
		return false
	}
	return next.TimedOut || !now.Before(deadline)
}

func sendBusyBudgetMS(timeoutMS int, notReady bool, deadline, now time.Time) (int, bool) {
	if !notReady {
		return timeoutMS, true
	}
	left := int(deadline.Sub(now).Milliseconds())
	return left, left > 0
}
