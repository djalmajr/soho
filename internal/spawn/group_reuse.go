package spawn

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/dispatch"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/kinds"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/text"
	"github.com/djalmajr/soho/internal/transport"
)

// Group admission compares the resolved request to each worker's opening settings.
type laneReuseRequest struct {
	role, lane, kind, model, effort, approvals, mcp, workerCwd, sd, nativeArgs string
	kindFlag                                                                   bool
	ctx                                                                        *core.Config
	env                                                                        platform.Env
	processCwd                                                                 string
}

func newLaneReuseRequest(o spawnOptions, res core.RoleSettings, sd, nativeArgs string, ctx *core.Config, env platform.Env, cwd string) laneReuseRequest {
	return laneReuseRequest{
		role: o.role, lane: res.Lane, kind: res.Kind, model: o.model, effort: o.effort, approvals: o.approvals, mcp: o.mcp,
		workerCwd: o.cwd, sd: sd, nativeArgs: nativeArgs, kindFlag: o.kind != "", ctx: ctx, env: env, processCwd: cwd,
	}
}

func (r laneReuseRequest) forModel(model string) laneReuseRequest {
	r.model = model
	if r.kind == "codex" && r.effort != "" {
		r.effort = kinds.ClampTo(r.effort, kinds.CodexEffortCeiling(model, r.env))
	}
	return r
}

func (r laneReuseRequest) eligible(f []string, state string) bool {
	if state != "idle" && state != "done" {
		return false
	}
	if report := core.LastReport(r.sd, fieldAt(f, 0)); report != "" {
		info, err := os.Stat(report)
		if err != nil || info.Size() == 0 {
			return false
		}
	}
	return !core.IsReviewRole(r.role) || !core.RoleIsEdit(fieldAt(f, 3), r.env, r.processCwd) && !core.HistoryHasEdit(fieldAt(f, 10), r.env, r.processCwd)
}

type reuseDifference struct {
	field, session, requested string
}

func (r laneReuseRequest) openingEffort(f []string) string {
	if len(f) > 14 {
		return f[14]
	}
	kindLayer := core.SpawnKindLayer(r.ctx, r.role, r.lane, r.kindFlag, r.env)
	return legacyLaneSessionEffort(fieldAt(f, 3), r.lane, fieldAt(f, 2), kindLayer, r.ctx, r.env, r.processCwd, fieldAt(f, 8))
}

func (r laneReuseRequest) differences(f []string) []reuseDifference {
	var differences []reuseDifference
	add := func(field, session, requested string) {
		if session != requested {
			differences = append(differences, reuseDifference{field, session, requested})
		}
	}
	add("kind", fieldAt(f, 2), r.kind)
	add("model", fieldAt(f, 8), r.model)
	add("effort", r.openingEffort(f), r.effort)
	add("approvals", fieldAt(f, 9), r.approvals)
	// A row from before the MCP column represents inheritance, while the
	// diagnostic still shows the recorded value.
	mcp := fieldAt(f, 15)
	heldMcp := core.McpCanonical(mcp)
	if len(f) <= 15 {
		heldMcp = "inherit"
	}
	if heldMcp != r.mcp {
		differences = append(differences, reuseDifference{"mcp", mcp, r.mcp})
	}
	if held := fieldAt(f, 6); !dispatch.SamePath(held, r.workerCwd, platform.Current()) {
		differences = append(differences, reuseDifference{"cwd", held, r.workerCwd})
	}
	add("args", fieldAt(f, 13), r.nativeArgs)
	return differences
}

// pick inspects the current eligible rows once for both fixed and listed
// requests. The first eligible mismatch is kept in case the lane is full.
func (r laneReuseRequest) pick(rows []string, rt transport.Transport, models []string, gone *[]string) (match laneCandidate) {
	for _, candidate := range rows {
		f := strings.Split(candidate, "\t")
		name := fieldAt(f, 0)
		if name == "" {
			continue
		}
		state, _ := agentGetState(rt, name)
		if state == "gone" {
			*gone = append(*gone, candidate)
		}
		if !r.eligible(f, state) {
			continue
		}
		spec, diff := r.listedModelDifferences(models, f)
		if len(diff) == 0 {
			return laneCandidate{line: candidate, modelSpec: spec}
		}
		if match.line == "" {
			match = laneCandidate{line: candidate, differences: diff}
		}
	}
	return match
}

type laneCandidate struct {
	line, modelSpec string
	differences     []reuseDifference
}

func reuseConfigSource(from, role, lane, field, laneField string) string {
	if strings.HasPrefix(from, "role config ") {
		return "role." + role + "." + field + strings.TrimPrefix(from, "role config")
	}
	if prefix := "lane " + lane + " "; strings.HasPrefix(from, prefix) {
		return "lane." + lane + "." + laneField + strings.TrimPrefix(from, strings.TrimSpace(prefix))
	}
	return from
}

type laneReuseSources struct {
	mcp, cwd, args string
}

func (sources laneReuseSources) requested(field, role string, res core.RoleSettings) string {
	laneField := field
	var from string
	switch field {
	case "kind":
		from = res.KindFrom
	case "model":
		from = res.ModelFrom
		if len(res.LaneModels) != 0 {
			laneField = "models"
		}
	case "effort":
		from = res.EffortFrom
	case "approvals":
		from = res.ApprovalsFrom
	case "mcp":
		return sources.mcp
	case "cwd":
		return sources.cwd
	case "args":
		return sources.args
	}
	return reuseConfigSource(from, role, res.Lane, field, laneField)
}

// changeSetting names the source that an operator can edit for this request.
func (sources laneReuseSources) changeSetting(field, role string, res core.RoleSettings) string {
	source := sources.requested(field, role, res)
	switch source {
	case "flag", "default", "":
		return "--" + field
	case "role file":
		return field + " in the role file for " + role
	default:
		return source
	}
}

func (r laneReuseRequest) reportMismatch(o spawnOptions, res core.RoleSettings, sources laneReuseSources, row string, diff []reuseDifference, full bool) {
	f := strings.Split(row, "\t")
	name := fieldAt(f, 0)
	obj := jsonjs.O("status", "kind-mismatch", "lane", r.lane, "name", name)
	// Keep the kind/model/effort pairs supplied by the older untyped-group
	// response, even when one of those fields matches.
	if core.LaneAttr(r.ctx, r.lane, "kind", nil, r.env) == "" {
		for _, d := range diff {
			if d.field == "kind" || d.field == "effort" {
				obj.Set("session_kind", fieldAt(f, 2))
				obj.Set("requested_kind", r.kind)
				obj.Set("session_model", fieldAt(f, 8))
				obj.Set("requested_model", r.model)
				obj.Set("session_effort", r.openingEffort(f))
				obj.Set("requested_effort", r.effort)
				break
			}
		}
	}
	var differences, origins, configured []string
	for _, d := range diff {
		obj.Set("session_"+d.field, d.session)
		obj.Set("requested_"+d.field, d.requested)
		label := d.field
		if label == "args" {
			label = "native args"
		}
		differences = append(differences, fmt.Sprintf("%s differs (%s vs %s)", label, diagnosticValue(d.session), diagnosticValue(d.requested)))
		origins = append(origins, label+" from "+sources.requested(d.field, r.role, res))
		if d.field == "cwd" {
			continue
		}
		laneField := d.field
		key := core.LaneKey(r.lane, laneField)
		if laneField == "model" {
			_, key, _ = core.LaneModelConfig(r.ctx, r.lane, r.env)
			laneField = strings.TrimPrefix(key, core.LaneKey(r.lane, ""))
		}
		if value := core.Cfg(r.ctx, key, "", r.env); value != "" {
			configured = append(configured, "lane."+r.lane+"."+laneField+"="+diagnosticValue(value))
		}
	}
	detail := " (request: " + strings.Join(origins, ", ")
	if len(configured) != 0 {
		detail += "; group config: " + strings.Join(configured, ", ")
	}
	msg := fmt.Sprintf("idle worker '%s' not reused: %s%s); release it to free its slot", name, strings.Join(differences, ", "), detail)
	if full {
		_, _ = fmt.Fprintln(platform.Stdout, jsonjs.Stringify(o.outputObject(obj)))
		Warn(o.outputText(msg+" or raise lane."+r.lane+".panes"), r.ctx, r.env, "spawn")
		platform.Die("", 13)
	}
	_, _ = fmt.Fprintln(platform.Stderr, o.outputText("soho: spawn: "+msg))
}

func diagnosticValue(value string) string {
	if value == "" {
		return "(empty)"
	}
	return text.SanitizeCause(value)
}

// listedModelDifferences compares with the first configured spec that
// resolves to the worker's model. If no entry resolves to it, compare with
// the default request so an unlisted model is included in the differences.
func (r laneReuseRequest) listedModelDifferences(models []string, fields []string) (string, []reuseDifference) {
	recordedModel := fieldAt(fields, 8)
	for _, spec := range models {
		resolved, _ := kinds.ResolveModel(r.kind, spec, r.effort, r.env, func(string) {})
		if resolved == recordedModel {
			return spec, r.forModel(resolved).differences(fields)
		}
	}
	return "", r.forModel(r.model).differences(fields)
}

type laneSpawnPlan struct {
	decision           core.LaneDecision
	candidate          laneCandidate
	action             string
	busyName, busyHint string
	renameRefused      bool
	burst              bool
}

// planLaneSpawn applies the lane and transport admission rules before model
// rotation. The global cap is also used to choose rename versus creation.
func (r laneReuseRequest) planLaneSpawn(o spawnOptions, models []string, rt transport.Transport) laneSpawnPlan {
	d := core.LaneDecideAt(r.ctx, r.lane, r.role, r.env, r.sd, r.processCwd, o.reuse == "on")
	p := laneSpawnPlan{decision: d, action: d.Decision}
	switch d.Decision {
	case "reuse":
		var rows, gone []string
		var namedRow string
		for _, row := range core.LaneWorkers(r.sd, r.lane) {
			name, _, _ := strings.Cut(row, "\t")
			if !has(d.Occupants, name) {
				continue
			}
			if name == o.name {
				namedRow = row
			} else {
				rows = append(rows, row)
			}
		}
		if namedRow != "" {
			p.candidate = r.pick([]string{namedRow}, rt, models, &gone)
		}
		// An explicit name that is still live owns its slot, even when
		// it cannot serve this role. A newly gone row releases the name.
		named := namedRow != "" && len(gone) == 0
		if !named {
			p.candidate = r.pick(rows, rt, models, &gone)
		}
		for _, row := range gone {
			name, _, _ := strings.Cut(row, "\t")
			p.decision.Gone = append(p.decision.Gone, name)
			p.decision.GoneRows = append(p.decision.GoneRows, row)
			p.decision.Occupants = slices.DeleteFunc(p.decision.Occupants, func(held string) bool { return held == name })
		}
		p.decision.N = len(p.decision.Occupants)
		full := p.decision.N >= d.Capacity
		p.action = "open"
		if p.candidate.line == "" {
			if named || full {
				p.action = "busy"
				p.busyName = d.Name
				if named {
					p.busyName = o.name
				}
			}
			return p
		}
		if len(p.candidate.differences) != 0 {
			if full {
				p.action = "mismatch"
			}
			return p
		}
		selected, _, _ := strings.Cut(p.candidate.line, "\t")
		if o.name == "" || o.name == selected {
			p.action = "reuse"
		} else {
			p.action = "different-name"
		}
	case "busy", "absent":
		// Flex is an existing alternative creation path for these cases.
		p.burst = core.PaneMode(r.ctx, r.env) == "flex" &&
			has(core.SplitRoles(core.Cfg(r.ctx, "flex_roles", "reviewer,documenter", r.env)), r.role) &&
			(d.Decision == "busy" || d.Capacity == 0) &&
			len(core.LiveBurstWorkers(r.sd, r.env)) < core.FlexExtra(r.ctx, r.env)
		if p.burst || d.Decision == "absent" && d.Capacity > 0 {
			p.action = "open"
			break
		}
		p.action, p.busyName = "busy", d.Name
		if d.Candidate != "" && o.reuse != "on" {
			p.busyName = d.Candidate
			p.busyHint = fmt.Sprintf("lane '%s' already has idle worker '%s'. Release it before --fresh, or dispatch on it. Run 'wait %s', then dispatch.", r.lane, d.Candidate, d.Candidate)
		} else if d.Capacity != 0 {
			p.busyHint = fmt.Sprintf("lane '%s' is full (%d of %d: %s). Run 'wait %s', then dispatch.", r.lane, d.N, d.Capacity, strings.Join(d.Occupants, " "), d.Name)
		}
	}
	return p
}

// resolveName decides if an otherwise compatible worker can serve an
// explicitly different name, or if that request needs a new terminal.
func (p *laneSpawnPlan) resolveName(o spawnOptions, r laneReuseRequest, capacity core.WorkerCapacity) {
	full := p.decision.N >= p.decision.Capacity || capacity.Full
	p.action = "open"
	if r.env.Get("SOHO_TRANSPORT") == "hc" {
		if full {
			p.action = "busy"
			p.busyName, _, _ = strings.Cut(p.candidate.line, "\t")
			p.renameRefused = true
		}
		return
	}
	if full || core.RosterLine(r.sd, o.name) != "" && !has(p.decision.Gone, o.name) {
		p.action = "reuse"
	}
}
