package herdr

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/djalmajr/soho/internal/launchprofile"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/transport"
)

const configuredFrameLimit = 524288
const configuredRefusal = "configured start refused"

func configuredStart(s transport.Start) bool {
	return s.LaunchProfile != "" || len(s.EnvOverrides) != 0
}

func configuredIdentifier(s string) bool {
	if len(s) == 0 || len(s) > 256 || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// configuredInputFrame freezes and validates all known input through the shared
// profile policy. Missing allocation identifiers stay empty; their serialized
// size is only a lower bound until Start binds the actual identifiers. This
// candidate is never sent by preflight and confers no launch authority.
// Only explicit overrides enter it, never backend environment or caller metadata.
func configuredInputFrame(s transport.Start) (string, bool) {
	args := append([]string{}, s.Args...)
	env := make(map[string]string, len(s.EnvOverrides))
	for k, v := range s.EnvOverrides {
		env[k] = v
	}
	if s.Kind != "opencode" || s.LaunchProfile != launchprofile.OpenCodePrimaryProfile || launchprofile.ValidateNativeLaunchProfile(s.Kind, s.LaunchProfile, args, env) != nil {
		return "", false
	}
	if (s.Name != "" && !configuredIdentifier(s.Name)) || !configuredIdentifier(s.Kind) || (s.PaneID != "" && !configuredIdentifier(s.PaneID)) {
		return "", false
	}
	// ParseUint admits a leading +; the protocol admits decimal digits only.
	for _, c := range s.Timeout {
		if c < '0' || c > '9' {
			return "", false
		}
	}
	timeout, err := strconv.ParseUint(s.Timeout, 10, 64)
	if err != nil || timeout == 0 {
		return "", false
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([][2]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, [2]string{k, env[k]})
	}
	payload := struct {
		Method string `json:"method"`
		Params struct {
			Name    string      `json:"name"`
			Kind    string      `json:"kind"`
			Profile string      `json:"launch_profile"`
			Pane    string      `json:"pane_id"`
			Timeout uint64      `json:"timeout_ms"`
			Args    []string    `json:"args"`
			Env     [][2]string `json:"env"`
		} `json:"params"`
	}{Method: "agent.start_configured"}
	payload.Params.Name = s.Name
	payload.Params.Kind = s.Kind
	// The declared profile travels as provenance only: the shared validator
	// already accepted it above, and it says nothing about the native process
	// or the effective model.
	payload.Params.Profile = s.LaunchProfile
	payload.Params.Pane = s.PaneID
	payload.Params.Timeout = timeout
	payload.Params.Args = args
	payload.Params.Env = pairs
	body, err := json.Marshal(payload)
	if err != nil || len(body)+len("HERDR_CONFIGURED_START_V1\n")+1 > configuredFrameLimit {
		return "", false
	}
	return "HERDR_CONFIGURED_START_V1\n" + string(body) + "\n", true
}

// Final admission requires bound identifiers before checking the complete frame;
// the same known-input validator runs once, now with the actual metadata size.
func configuredFrame(s transport.Start) (string, bool) {
	if s.Name == "" || s.PaneID == "" {
		return "", false
	}
	return configuredInputFrame(s)
}

func configuredContext(ctx context.Context, call transport.Call) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, timeoutFor(call))
}

func configuredFailure(r platform.RunResult, kind transport.FailureKind) transport.Reply[string] {
	var status *int
	if r.Status != nil {
		n := *r.Status
		status = &n
	}
	return transport.Reply[string]{Process: transport.ProcessResult{Status: status, NotFound: r.NotFound, TimedOut: r.TimedOut}, Err: &transport.Failure{Kind: kind, Message: configuredRefusal}}
}

// Both phases receive the same absolute context deadline. TimeoutMs is the
// remaining rounded-up budget for platform timeout accounting, never a new lease.
func (b *backend) configuredRun(ctx context.Context, args []string, input string) platform.RunResult {
	remaining := time.Duration(0)
	if deadline, ok := ctx.Deadline(); ok {
		remaining = time.Until(deadline)
	}
	if ctx.Err() != nil || remaining <= 0 {
		return platform.RunResult{TimedOut: errors.Is(ctx.Err(), context.DeadlineExceeded)}
	}
	return b.runner("herdr", args, platform.RunOptions{Env: b.env, Context: ctx, Input: input, TimeoutMs: timeoutMsUp(remaining), StrictOutputUTF8: true})
}

func (b *backend) configuredProbe(ctx context.Context) transport.Reply[string] {
	r := b.configuredRun(ctx, []string{"status", "server", "--json"}, "")
	failed := configuredFailure(r, transport.FailureUnavailable)
	if !ok(r) || r.TimedOut || r.Error != "" || r.Signal != "" || ctx.Err() != nil {
		return failed
	}
	obj, valid := configuredObject(r.Stdout)
	caps, _ := obj["capabilities"].(map[string]any)
	if !valid || obj["running"] != true || caps["native_agent_launch_v1"] != true {
		failed.Err.Kind = transport.FailureUnsupported
		return failed
	}
	failed.Valid = true
	failed.Err = nil
	return failed
}

func (b *backend) PreflightConfiguredStart(ctx context.Context, s transport.Start, call transport.Call) transport.Reply[bool] {
	if !configuredStart(s) {
		return transport.Reply[bool]{Value: true, Valid: true}
	}
	if _, valid := configuredInputFrame(s); !valid {
		return transport.Reply[bool]{Err: &transport.Failure{Kind: transport.FailureProtocol, Message: configuredRefusal}}
	}
	ctx, cancel := configuredContext(ctx, call)
	defer cancel()
	r := b.configuredProbe(ctx)
	return transport.Reply[bool]{Value: r.Valid, Valid: r.Valid, Process: r.Process, Err: r.Err}
}

// Reuse observes the prospective native AgentInfo profile field. Current native
// producers without that field refuse; neither roster data nor launch capability
// establishes the existing process's profile. This read is not a reservation.
func (b *backend) PreflightConfiguredReuse(ctx context.Context, s transport.Start, call transport.Call) transport.Reply[bool] {
	if _, valid := configuredFrame(s); !valid {
		return transport.Reply[bool]{Err: &transport.Failure{Kind: transport.FailureProtocol, Message: configuredRefusal}}
	}
	ctx, cancel := configuredContext(ctx, call)
	defer cancel()
	r := b.configuredRun(ctx, []string{"agent", "get", s.PaneID}, "")
	failed := configuredFailure(r, transport.FailureUnavailable)
	reply := transport.Reply[bool]{Process: failed.Process, Err: failed.Err}
	if !ok(r) || r.TimedOut || r.Error != "" || r.Signal != "" || ctx.Err() != nil {
		return reply
	}
	obj, valid := configuredObject(r.Stdout)
	if !valid {
		reply.Err.Kind = transport.FailureProtocol
		return reply
	}
	if _, present := obj["error"]; present {
		return reply
	}
	result, _ := obj["result"].(map[string]any)
	agent, _ := result["agent"].(map[string]any)
	terminal, _ := agent["terminal_id"].(string)
	if result["type"] != "agent_info" || agent["name"] != s.Name || agent["agent"] != s.Kind || agent["pane_id"] != s.PaneID || !configuredIdentifier(terminal) || agent["interactive_ready"] != true || agent["launch_profile"] != s.LaunchProfile {
		return reply
	}
	if pending, present := agent["launch_pending"]; present && pending != false {
		return reply
	}
	if agent["agent_status"] != "idle" && agent["agent_status"] != "done" {
		return reply
	}
	reply.Value, reply.Valid, reply.Err = true, true, nil
	return reply
}

func (b *backend) startConfigured(ctx context.Context, s transport.Start, call transport.Call) transport.Reply[string] {
	frame, valid := configuredFrame(s)
	if !valid {
		return configuredFailure(platform.RunResult{}, transport.FailureProtocol)
	}
	ctx, cancel := configuredContext(ctx, call)
	defer cancel()
	probe := b.configuredProbe(ctx)
	if !probe.Valid {
		return probe
	}
	if ctx.Err() != nil {
		return configuredFailure(platform.RunResult{TimedOut: errors.Is(ctx.Err(), context.DeadlineExceeded)}, transport.FailureCancelled)
	}
	r := b.configuredRun(ctx, []string{"agent", "start-configured", s.Name, "--kind", s.Kind, "--pane", s.PaneID, "--timeout", s.Timeout}, frame)
	failed := configuredFailure(r, transport.FailureRunOutcomeUnknown)
	// Only a lookup failure proves this invocation did not dispatch.
	if r.NotFound {
		failed.Err.Kind = transport.FailureCommand
		return failed
	}
	obj, valid := configuredObject(r.Stdout)
	if code := configuredErrorCode(obj, valid); code != "" {
		failed.Err.Kind = transport.FailureCommand
		failed.Err.Code = code
		return failed
	}
	if r.Stdout == "" {
		errObj, errValid := configuredObject(r.Stderr)
		if code := configuredErrorCode(errObj, errValid); code != "" {
			failed.Err.Kind = transport.FailureCommand
			failed.Err.Code = code
			return failed
		}
	}
	if !ok(r) || r.TimedOut || r.Error != "" || r.Signal != "" || ctx.Err() != nil || !valid || !configuredReadyAgent(obj, s) {
		return failed
	}
	failed.Valid = true
	failed.Err = nil
	failed.Value = `{"result":{"started":true}}`
	return failed
}

func configuredErrorCode(obj map[string]any, valid bool) string {
	if !valid || obj["id"] != "configured-start" {
		return ""
	}
	if _, present := obj["result"]; present {
		return ""
	}
	e, ok := obj["error"].(map[string]any)
	if !ok {
		return ""
	}
	if _, ok := e["message"].(string); !ok {
		return ""
	}
	code, _ := e["code"].(string)
	if code == "agent_not_ready" || code == "agent_pane_busy" {
		return code
	}
	return ""
}

func configuredReadyAgent(obj map[string]any, s transport.Start) bool {
	if obj["id"] != "configured-start" {
		return false
	}
	if _, present := obj["error"]; present {
		return false
	}
	result, _ := obj["result"].(map[string]any)
	if result["type"] != "agent_configured_started" {
		return false
	}
	agent, _ := result["agent"].(map[string]any)
	terminal, _ := agent["terminal_id"].(string)
	// The reported profile must be the exact accepted one, like reuse: absent,
	// null, non-string and foreign profiles all fail the string comparison.
	if agent["name"] != s.Name || agent["agent"] != s.Kind || agent["pane_id"] != s.PaneID || !configuredIdentifier(terminal) || agent["interactive_ready"] != true || agent["launch_profile"] != s.LaunchProfile {
		return false
	}
	if pending, present := agent["launch_pending"]; present && pending != false {
		return false
	}
	return agent["agent_status"] == "idle" || agent["agent_status"] == "done"
}

// Token decoding rejects duplicate keys at every depth, including escaped
// spellings, and trailing frames. The strict runner validates original bytes;
// validate strings here too for per-instance runners, accepting literal U+FFFD.
func configuredObject(raw string) (map[string]any, bool) {
	if len(raw) > configuredFrameLimit || !utf8.ValidString(raw) {
		return nil, false
	}
	d := json.NewDecoder(strings.NewReader(raw))
	d.UseNumber()
	v, err := configuredJSONValue(d, 0)
	if err != nil {
		return nil, false
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, false
	}
	obj, ok := v.(map[string]any)
	return obj, ok
}

func configuredJSONValue(d *json.Decoder, depth int) (any, error) {
	if depth > 64 {
		return nil, errors.New(configuredRefusal)
	}
	token, err := d.Token()
	if err != nil {
		return nil, err
	}
	delim, isDelim := token.(json.Delim)
	if !isDelim {
		return token, nil
	}
	switch delim {
	case '{':
		obj := map[string]any{}
		for d.More() {
			keyToken, err := d.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, errors.New(configuredRefusal)
			}
			if _, exists := obj[key]; exists {
				return nil, errors.New(configuredRefusal)
			}
			value, err := configuredJSONValue(d, depth+1)
			if err != nil {
				return nil, err
			}
			obj[key] = value
		}
		end, err := d.Token()
		if err != nil || end != json.Delim('}') {
			return nil, errors.New(configuredRefusal)
		}
		return obj, nil
	case '[':
		list := []any{}
		for d.More() {
			value, err := configuredJSONValue(d, depth+1)
			if err != nil {
				return nil, err
			}
			list = append(list, value)
		}
		end, err := d.Token()
		if err != nil || end != json.Delim(']') {
			return nil, errors.New(configuredRefusal)
		}
		return list, nil
	}
	return nil, errors.New(configuredRefusal)
}
