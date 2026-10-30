package peer

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/sessionref"
	textutil "github.com/djalmajr/soho/internal/text"
	"github.com/djalmajr/soho/internal/transport"
)

const SnapshotTimeoutMS = 30_000

// DiscoverTimeoutMS is the default global deadline for DiscoverSessions; it
// covers the local snapshot, the machine list and every remote query.
const DiscoverTimeoutMS = 30_000

// DiscoverMaxConcurrency bounds the number of concurrent remote queries.
const DiscoverMaxConcurrency = 4

var matchFields = []string{"ref", "machine", "workspace_id", "workspace_label", "tab_id", "tab_label", "pane_id", "name", "kind", "status", "cwd", "title"}

type SessionEntry struct {
	Ref            string `json:"ref"`
	Machine        string `json:"machine"`
	WorkspaceID    any    `json:"workspace_id"`
	WorkspaceLabel any    `json:"workspace_label"`
	TabID          any    `json:"tab_id"`
	TabLabel       any    `json:"tab_label"`
	PaneID         string `json:"pane_id"`
	Name           any    `json:"name"`
	Kind           any    `json:"kind"`
	Status         any    `json:"status"`
	Cwd            any    `json:"cwd"`
	Title          any    `json:"title"`
	Focused        bool   `json:"focused"`
}

type MachineFailure struct {
	Machine string
	Cause   string
}

type SessionResult struct {
	// Machine identifies a published batch even when it contains no panes.
	// The final aggregate leaves it empty.
	Machine  string
	Entries  []SessionEntry
	Failures []MachineFailure
	// ListCause is set on the final result when the machine list (the
	// enumeration behind All) failed; it stays empty on per-machine batches
	// and when the enumeration succeeds or was not requested.
	ListCause string
}

type Machine struct {
	Label   string
	Enabled bool
}

type SnapshotOptions struct {
	Env       platform.Env
	TimeoutMS int
	Context   context.Context
	Run       func(string, []string, platform.RunOptions) platform.RunResult
}

// DiscoverOptions selects the machines of a progressive discovery and its
// global deadline. Machines already contains the remote labels the caller
// resolved (explicit --machine values and the machine of a reference); All
// additionally enumerates the enabled machines. The deadline counts from the
// call to DiscoverSessions and covers every subprocess of the discovery.
type DiscoverOptions struct {
	Env         platform.Env
	Machines    []string
	All         bool
	TimeoutMS   int
	Concurrency int
	Context     context.Context
	Run         func(string, []string, platform.RunOptions) platform.RunResult
}

// DiscoverSessions fetches the local sessions first, before any enumeration,
// so a slow machine list never delays the local rows. It then queries the
// remote machines (the ones already in options.Machines plus, with All, the
// enabled ones) with at most DiscoverMaxConcurrency concurrent subprocesses.
// The global deadline, DiscoverTimeoutMS when options.TimeoutMS is not a
// positive value, covers the local snapshot, the machine list and every
// remote query; each subprocess gets the smaller of SnapshotTimeoutMS and
// the time still left, and a machine not started by the deadline reports a
// deadline cause instead of being silently dropped. publish is called
// serially on the calling goroutine, once per machine (local first, then the
// remotes in completion order) with that machine's batch; the final result
// aggregates every batch and carries the enumeration failure, if any.
func DiscoverSessions(options DiscoverOptions, publish func(SessionResult)) SessionResult {
	budget := options.TimeoutMS
	if budget <= 0 {
		budget = DiscoverTimeoutMS
	}
	concurrency := options.Concurrency
	if concurrency <= 0 || concurrency > DiscoverMaxConcurrency {
		concurrency = DiscoverMaxConcurrency
	}
	parent := options.Context
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(budget)*time.Millisecond)
	defer cancel()
	options.Context = ctx
	deadline, _ := ctx.Deadline()
	final := SessionResult{Entries: []SessionEntry{}, Failures: []MachineFailure{}}
	record := func(batch SessionResult) {
		final.Entries = append(final.Entries, batch.Entries...)
		final.Failures = append(final.Failures, batch.Failures...)
		if publish != nil {
			publish(batch)
		}
	}
	local := machineBatch(sessionref.LocalMachine, options, deadline)
	record(local)
	machines := discoverMachineList(options.Machines)
	if options.All {
		remaining := deadline.Sub(time.Now())
		if ctx.Err() != nil && remaining > 0 {
			final.ListCause = "discovery canceled"
		} else if remaining <= 0 {
			final.ListCause = "deadline reached before listing machines"
		} else {
			list, cause := listMachines(discoverCallTimeout(remaining), SnapshotOptions{Env: options.Env, Context: ctx, Run: options.Run})
			if cause != "" {
				final.ListCause = cause
			} else {
				for _, m := range list {
					if m.Enabled && !contains(machines, m.Label) {
						machines = append(machines, m.Label)
					}
				}
			}
		}
	}
	remotes := machines[1:]
	if len(remotes) > 0 {
		results := make(chan SessionResult, len(remotes))
		tasks := make(chan string, len(remotes))
		for i := 0; i < concurrency; i++ {
			go func() {
				for machine := range tasks {
					results <- machineBatch(machine, options, deadline)
				}
			}()
		}
		for _, machine := range remotes {
			tasks <- machine
		}
		close(tasks)
		for i := 0; i < len(remotes); i++ {
			record(<-results)
		}
	}
	return final
}

// discoverMachineList normalizes the requested machines: the local machine
// first, duplicates dropped, the remaining order preserved.
func discoverMachineList(machines []string) []string {
	out := []string{sessionref.LocalMachine}
	seen := map[string]bool{sessionref.LocalMachine: true}
	for _, machine := range machines {
		if machine == "" || seen[machine] {
			continue
		}
		seen[machine] = true
		out = append(out, machine)
	}
	return out
}

// discoverCallTimeout bounds one subprocess of a discovery: the smaller of
// SnapshotTimeoutMS and the whole remaining deadline, and never below 1 ms.
func discoverCallTimeout(remaining time.Duration) int {
	ms := int(remaining / time.Millisecond)
	if ms < 1 {
		ms = 1
	}
	if ms > SnapshotTimeoutMS {
		ms = SnapshotTimeoutMS
	}
	return ms
}

// machineBatch fetches one machine's sessions under the discovery deadline:
// a machine not started by the deadline reports a deadline cause without
// spawning a subprocess.
func machineBatch(machine string, options DiscoverOptions, deadline time.Time) SessionResult {
	if options.Context != nil && options.Context.Err() != nil && time.Now().Before(deadline) {
		return SessionResult{Machine: machine, Failures: []MachineFailure{{Machine: machine, Cause: "discovery canceled"}}}
	}
	remaining := deadline.Sub(time.Now())
	if remaining <= 0 {
		return SessionResult{Machine: machine, Entries: []SessionEntry{}, Failures: []MachineFailure{{Machine: machine, Cause: "deadline reached before querying"}}}
	}
	snapshot, cause := oneSnapshot(machine, SnapshotOptions{Env: options.Env, TimeoutMS: discoverCallTimeout(remaining), Context: options.Context, Run: options.Run})
	result := SessionResult{Machine: machine, Entries: []SessionEntry{}, Failures: []MachineFailure{}}
	if cause != "" {
		result.Failures = append(result.Failures, MachineFailure{Machine: machine, Cause: cause})
		return result
	}
	result.Entries = SessionEntries(snapshot, machine)
	if result.Entries == nil {
		result.Entries = []SessionEntry{}
	}
	return result
}

func valueString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	if _, ok := v.(float64); ok {
		return jsonjs.Stringify(v)
	}
	return fmt.Sprint(v)
}

func strOrNull(v any) any {
	if v == nil || v == "" {
		return nil
	}
	if s, ok := v.(string); ok {
		return s
	}
	if _, ok := v.(float64); ok {
		return jsonjs.Stringify(v)
	}
	return fmt.Sprint(v)
}

func object(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func array(v any) []any {
	a, _ := v.([]any)
	return a
}

func prop(m map[string]any, key string) any { return m[key] }

func SessionEntries(snapshot any, machine string) []SessionEntry {
	s, ok := snapshot.(map[string]any)
	if !ok {
		return nil
	}
	panes, ok := s["panes"].([]any)
	if !ok {
		return nil
	}
	if machine == "" {
		machine = sessionref.LocalMachine
	}
	workspaces, tabs, agents := map[string]map[string]any{}, map[string]map[string]any{}, map[string]map[string]any{}
	for _, v := range array(s["workspaces"]) {
		w := object(v)
		if id, ok := w["workspace_id"].(string); ok {
			workspaces[id] = w
		}
	}
	for _, v := range array(s["tabs"]) {
		t := object(v)
		if id, ok := t["tab_id"].(string); ok {
			tabs[id] = t
		}
	}
	for _, v := range array(s["agents"]) {
		a := object(v)
		if id, ok := a["pane_id"].(string); ok {
			agents[id] = a
		}
	}
	out := make([]SessionEntry, 0, len(panes))
	for _, v := range panes {
		p := object(v)
		paneID, ok := p["pane_id"].(string)
		if !ok {
			continue
		}
		workspaceID, tabID := valueString(p["workspace_id"]), valueString(p["tab_id"])
		w, t, a := workspaces[workspaceID], tabs[tabID], agents[paneID]
		merged := make(map[string]any, len(p)+len(a))
		for k, val := range p {
			merged[k] = val
		}
		for k, val := range a {
			if val != nil {
				merged[k] = val
			}
		}
		cwd := strOrNull(merged["foreground_cwd"])
		if cwd == nil {
			cwd = strOrNull(merged["cwd"])
		}
		title := strOrNull(merged["title"])
		if title == nil {
			title = strOrNull(merged["terminal_title_stripped"])
		}
		out = append(out, SessionEntry{
			Ref: sessionref.FormatRef(sessionref.Ref{Machine: machine, PaneID: paneID}), Machine: machine,
			WorkspaceID: strOrNull(p["workspace_id"]), WorkspaceLabel: strOrNull(prop(w, "label")),
			TabID: strOrNull(p["tab_id"]), TabLabel: strOrNull(prop(t, "label")), PaneID: paneID,
			Name: strOrNull(prop(a, "name")), Kind: strOrNull(prop(a, "agent")),
			Status: strOrNull(merged["agent_status"]), Cwd: cwd, Title: title, Focused: p["focused"] == true,
		})
	}
	return out
}

func MatchEntries(entries []SessionEntry, words []string) []SessionEntry {
	if len(words) == 0 {
		return append([]SessionEntry(nil), entries...)
	}
	if len(words) == 1 {
		if ref := sessionref.ParseRef(words[0]); ref != nil {
			out := make([]SessionEntry, 0, 1)
			for _, e := range entries {
				if e.Machine == ref.Machine && e.PaneID == ref.PaneID {
					out = append(out, e)
				}
			}
			return out
		}
	}
	out := make([]SessionEntry, 0, len(entries))
	for _, e := range entries {
		values := sessionValues(e)
		matched := true
		for _, word := range words {
			needle := textutil.JSLower(word)
			any := false
			for _, field := range matchFields {
				if values[field] != nil && strings.Contains(textutil.JSLower(valueString(values[field])), needle) {
					any = true
					break
				}
			}
			if !any {
				matched = false
				break
			}
		}
		if matched {
			out = append(out, e)
		}
	}
	return out
}

func sessionValues(e SessionEntry) map[string]any {
	return map[string]any{"ref": e.Ref, "machine": e.Machine, "workspace_id": e.WorkspaceID, "workspace_label": e.WorkspaceLabel,
		"tab_id": e.TabID, "tab_label": e.TabLabel, "pane_id": e.PaneID, "name": e.Name, "kind": e.Kind,
		"status": e.Status, "cwd": e.Cwd, "title": e.Title}
}

func oneSnapshot(machine string, opts SnapshotOptions) (any, string) {
	timeout := opts.TimeoutMS
	if timeout == 0 {
		timeout = SnapshotTimeoutMS
	}
	// The test seam (opts.Run) keeps its direct CLI shape; the default path
	// runs the same argv through the backend with the same context and
	// timeout.
	var r platform.RunResult
	if opts.Run != nil {
		r = opts.Run("herdr", append(sessionref.HerdrMachineArgs(machine), "api", "snapshot"), platform.RunOptions{Env: opts.Env, Context: opts.Context, TimeoutMs: timeout})
	} else {
		rep := herdr.New(opts.Env).Legacy.SnapshotRaw(opts.Context, transport.Target{Machine: machine}, transport.Call{Timeout: time.Duration(timeout) * time.Millisecond})
		r = runResultFromProcess(rep.Process)
	}
	if r.NotFound {
		return nil, "herdr CLI not found in PATH"
	}
	if r.TimedOut {
		return nil, fmt.Sprintf("herdr api snapshot timed out after %ss", numberSeconds(timeout))
	}
	status := 1
	if r.Status != nil {
		status = *r.Status
	}
	if status != 0 {
		raw := strings.TrimSpace(r.Stderr)
		if raw == "" {
			raw = strings.TrimSpace(r.Stdout)
		}
		return nil, sanitizeCause(raw, fmt.Sprintf("herdr api snapshot failed (exit %d)", status))
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(r.Stdout), &decoded); err != nil {
		return nil, "herdr api snapshot returned no JSON"
	}
	result := object(decoded["result"])
	snapshot := result["snapshot"]
	snap, ok := snapshot.(map[string]any)
	if !ok {
		return nil, "herdr api snapshot returned no snapshot"
	}
	if _, ok := snap["panes"].([]any); !ok {
		return nil, "herdr api snapshot returned no snapshot"
	}
	return snapshot, ""
}

// runResultFromProcess mirrors the backend's raw process outcome into the
// platform result the snapshot parsing reads: the fields are the same, so
// the parsing keeps reading exactly what the direct CLI call used to.
func runResultFromProcess(p transport.ProcessResult) platform.RunResult {
	return platform.RunResult{
		NotFound: p.NotFound,
		Resolved: p.Resolved,
		Status:   p.Status,
		Signal:   p.Signal,
		Stdout:   p.Stdout,
		Stderr:   p.Stderr,
		TimedOut: p.TimedOut,
		Error:    p.Error,
	}
}

func FetchSessions(machines []string, opts SnapshotOptions) SessionResult {
	out := SessionResult{Entries: []SessionEntry{}, Failures: []MachineFailure{}}
	for _, machine := range machines {
		snapshot, cause := oneSnapshot(machine, opts)
		if cause != "" {
			out.Failures = append(out.Failures, MachineFailure{Machine: machine, Cause: cause})
			continue
		}
		out.Entries = append(out.Entries, SessionEntries(snapshot, machine)...)
	}
	return out
}

func MachineList(opts SnapshotOptions) ([]Machine, string) {
	timeout := opts.TimeoutMS
	if timeout == 0 {
		timeout = SnapshotTimeoutMS
	}
	return listMachines(timeout, opts)
}

func listMachines(timeoutMS int, opts SnapshotOptions) ([]Machine, string) {
	// The test seam (opts.Run) keeps its direct CLI shape; the default path
	// runs the same argv through the backend with the same context and
	// timeout.
	var r platform.RunResult
	if opts.Run != nil {
		r = opts.Run("herdr", []string{"machine", "list", "--json"}, platform.RunOptions{Env: opts.Env, Context: opts.Context, TimeoutMs: timeoutMS})
	} else {
		rep := herdr.New(opts.Env).Legacy.MachineListRaw(opts.Context, transport.Call{Timeout: time.Duration(timeoutMS) * time.Millisecond})
		r = runResultFromProcess(rep.Process)
	}
	if r.NotFound {
		return nil, "herdr CLI not found in PATH"
	}
	if r.TimedOut {
		return nil, fmt.Sprintf("herdr machine list timed out after %ss", numberSeconds(timeoutMS))
	}
	status := 1
	if r.Status != nil {
		status = *r.Status
	}
	if status != 0 {
		raw := strings.TrimSpace(r.Stderr)
		if raw == "" {
			raw = strings.TrimSpace(r.Stdout)
		}
		return nil, sanitizeCause(raw, fmt.Sprintf("herdr machine list failed (exit %d)", status))
	}
	var rows []any
	if err := json.Unmarshal([]byte(r.Stdout), &rows); err != nil || rows == nil {
		return nil, "herdr machine list returned no machine list"
	}
	out := make([]Machine, 0, len(rows))
	for _, row := range rows {
		fields, ok := row.(map[string]any)
		if !ok {
			continue
		}
		label, ok := fields["label"].(string)
		if ok {
			out = append(out, Machine{Label: label, Enabled: fields["enabled"] == true})
		}
	}
	return out, ""
}

func numberSeconds(ms int) string {
	if ms%1000 == 0 {
		return fmt.Sprint(ms / 1000)
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.3f", float64(ms)/1000), "0"), ".")
}

func sanitizeCause(raw, fallback string) string {
	raw = textutil.SanitizeCause(raw)
	if raw == "" {
		return fallback
	}
	return raw
}

func entryJSON(e SessionEntry) *jsonjs.Object {
	return jsonjs.O("ref", e.Ref, "machine", e.Machine, "workspace_id", e.WorkspaceID, "workspace_label", e.WorkspaceLabel,
		"tab_id", e.TabID, "tab_label", e.TabLabel, "pane_id", e.PaneID, "name", e.Name, "kind", e.Kind,
		"status", e.Status, "cwd", e.Cwd, "title", e.Title, "focused", e.Focused)
}
