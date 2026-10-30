package core

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/djalmajr/soho/internal/herdr"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	textutil "github.com/djalmajr/soho/internal/text"
	"github.com/djalmajr/soho/internal/transport"
)

var shellNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var laneNameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
var decimalLaneRE = regexp.MustCompile(`^[0-9]+$`)
var positiveLaneRE = regexp.MustCompile(`^[1-9][0-9]*$`)

var LegacyPresets = map[string]map[string]string{
	"3": {"build": "implementer,designer,tasker", "read": "scouter,researcher,reviewer,security-reviewer,ui-reviewer,inspector"},
	"4": {"build": "implementer,designer,tasker", "explore": "scouter,researcher", "review": "reviewer,security-reviewer,ui-reviewer,inspector"},
}

func ConfigExplicit(ctx *Config, key string, env platform.Env) bool {
	s := CfgSource(ctx, key, env)
	return s == "user" || s == "project" || s == "env" || s == "session"
}
func LanesEnabled(ctx *Config, env platform.Env) bool { return Cfg(ctx, "lanes", "on", env) != "off" }
func PanesValue(ctx *Config, env platform.Env) string {
	p := Cfg(ctx, "panes", "4", env)
	if p == "2" || p == "3" || p == "4" {
		return p
	}
	return "4"
}
func PaneMode(ctx *Config, env platform.Env) string {
	if Cfg(ctx, "pane_mode", "strict", env) == "flex" {
		return "flex"
	}
	return "strict"
}
func FlexExtra(ctx *Config, env platform.Env) int {
	v := Cfg(ctx, "flex_extra", "1", env)
	if !decimalLaneRE.MatchString(v) {
		return 1
	}
	n, e := strconv.Atoi(v)
	if e != nil {
		return 1
	}
	return n
}
func LaneKey(name, attr string) string {
	return "lane_" + strings.ReplaceAll(name, "-", "_") + "_" + attr
}

func laneRolesName(key, prefix, suffix string) (string, bool) {
	if len(key) < len(prefix)+len(suffix) || !strings.HasPrefix(key, prefix) || !strings.HasSuffix(key, suffix) {
		return "", false
	}
	return key[len(prefix) : len(key)-len(suffix)], true
}

// laneEnvName extracts the lane name from a SOHO_LANE_<n>_ROLES
// environment variable name; "" when k is not one.
func laneEnvName(k string) string {
	if name, ok := laneRolesName(k, "SOHO_LANE_", "_ROLES"); ok {
		return name
	}
	return ""
}

func PresetLaneNamesFor(p, mode string) []string {
	if mode == "flex" {
		return []string{"build", "review", "docs"}
	}
	if p == "2" {
		return []string{"build"}
	}
	return []string{"build", "review"}
}
func PresetLaneNames(ctx *Config, env platform.Env) []string {
	return PresetLaneNamesFor(PanesValue(ctx, env), PaneMode(ctx, env))
}
func PresetLaneCount(p, mode string) int { return len(PresetLaneNamesFor(p, mode)) }
func PresetLaneRoles(lane, p, mode string) string {
	if mode == "flex" {
		switch lane {
		case "build":
			return "implementer,designer,tasker,scouter,researcher"
		case "review":
			return "reviewer,security-reviewer,ui-reviewer,inspector"
		case "docs":
			return "documenter"
		}
		return ""
	}
	switch p + ":" + lane {
	case "2:build", "3:build", "4:build":
		return "implementer,designer,tasker,scouter,researcher,documenter"
	case "3:review", "4:review":
		return "reviewer,security-reviewer,ui-reviewer,inspector"
	}
	return ""
}
func PresetLaneCapacity(lane, p, mode string) int {
	if mode == "flex" {
		if lane == "build" {
			if p == "4" {
				return 2
			}
			return 1
		}
		if lane == "docs" {
			return 0
		}
		if lane == "review" && p == "2" {
			return 0
		}
		return 1
	}
	if p == "4" && lane == "build" {
		return 2
	}
	return 1
}
func PresetRolesFlat(p, mode string) string {
	vals := []string{}
	for _, lane := range PresetLaneNamesFor(p, mode) {
		if roles := PresetLaneRoles(lane, p, mode); roles != "" {
			vals = append(vals, roles)
		}
	}
	return strings.Join(vals, ",")
}
func LegacyPresetSignature(p string) string {
	m := LegacyPresets[p]
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	textutil.SortUTF16(keys)
	lines := make([]string, 0, len(keys))
	for _, k := range keys {
		lines = append(lines, k+"="+m[k])
	}
	return strings.Join(lines, "\n")
}

func CustomLanesPresent(ctx *Config, env platform.Env) bool {
	for _, k := range ctx.Order {
		if _, ok := laneRolesName(k, "lane_", "_roles"); ok && Cfg(ctx, k, "", env) != "" {
			return true
		}
	}
	for k, v := range env {
		if shellNameRE.MatchString(k) && v != "" {
			if laneEnvName(k) != "" {
				return true
			}
		}
	}
	return false
}
func LaneNames(ctx *Config, env platform.Env) []string {
	if !CustomLanesPresent(ctx, env) {
		return PresetLaneNames(ctx, env)
	}
	names := map[string]bool{}
	for _, k := range ctx.Order {
		n, ok := laneRolesName(k, "lane_", "_roles")
		if !ok {
			continue
		}
		if n != "" && Cfg(ctx, LaneKey(n, "roles"), "", env) != "" {
			names[n] = true
		}
	}
	for k, v := range env {
		if !shellNameRE.MatchString(k) || v == "" {
			continue
		}
		n := strings.ToLower(laneEnvName(k))
		if n != "" && Cfg(ctx, LaneKey(n, "roles"), "", env) != "" {
			names[n] = true
		}
	}
	out := make([]string, 0, len(names))
	for n := range names {
		out = append(out, n)
	}
	textutil.SortUTF16(out)
	return out
}
func LaneCount(ctx *Config, env platform.Env) int { return len(LaneNames(ctx, env)) }
func LaneRolesCSV(ctx *Config, lane string, env platform.Env) string {
	if CustomLanesPresent(ctx, env) {
		return Cfg(ctx, LaneKey(lane, "roles"), "", env)
	}
	return PresetLaneRoles(lane, PanesValue(ctx, env), PaneMode(ctx, env))
}
func LaneCapacity(ctx *Config, lane string, env platform.Env) int {
	v := Cfg(ctx, LaneKey(lane, "panes"), "", env)
	if positiveLaneRE.MatchString(v) {
		if n, e := strconv.Atoi(v); e == nil {
			return n
		}
	}
	if !CustomLanesPresent(ctx, env) {
		return PresetLaneCapacity(lane, PanesValue(ctx, env), PaneMode(ctx, env))
	}
	return 1
}
func LaneCapacitySum(ctx *Config, env platform.Env) int {
	n := 0
	for _, lane := range LaneNames(ctx, env) {
		n += LaneCapacity(ctx, lane, env)
	}
	return n
}
func EffectiveLaneSignature(ctx *Config, env platform.Env) string {
	vals := map[string]string{}
	for _, k := range ctx.Order {
		if !strings.HasPrefix(k, "lane_") || !strings.HasSuffix(k, "_roles") {
			continue
		}
		n := strings.TrimSuffix(strings.TrimPrefix(k, "lane_"), "_roles")
		if n != "" {
			if v := Cfg(ctx, k, "", env); v != "" {
				vals[n] = v
			}
		}
	}
	for k, v := range env {
		if !shellNameRE.MatchString(k) || v == "" {
			continue
		}
		n := strings.ToLower(laneEnvName(k))
		if n != "" {
			if x := Cfg(ctx, LaneKey(n, "roles"), "", env); x != "" {
				vals[n] = x
			}
		}
	}
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	textutil.SortUTF16(keys)
	lines := make([]string, 0, len(keys))
	for _, k := range keys {
		lines = append(lines, k+"="+vals[k])
	}
	return strings.Join(lines, "\n")
}

func CfgLayerRank(ctx *Config, key string, env platform.Env) int {
	switch CfgSource(ctx, key, env) {
	case "env":
		return 4
	case "session":
		return 3
	case "project":
		return 2
	case "user":
		return 1
	}
	return 0
}
func LaneKindLayer(ctx *Config, lane string, env platform.Env) int {
	key := LaneKey(lane, "kind")
	if Cfg(ctx, key, "", env) == "" {
		return -1
	}
	return CfgLayerRank(ctx, key, env)
}
func LaneAttr(ctx *Config, lane, attr string, kindLayer *int, env platform.Env) string {
	rank := -1
	if kindLayer != nil {
		rank = *kindLayer
	} else {
		rank = LaneKindLayer(ctx, lane, env)
	}
	key := LaneKey(lane, attr)
	v := Cfg(ctx, key, "", env)
	if v == "" {
		return ""
	}
	if (attr == "model" || attr == "effort") && rank >= 0 && CfgLayerRank(ctx, key, env) < rank {
		return ""
	}
	return v
}
func SpawnKindLayer(ctx *Config, role, lane string, kindFlagSet bool, env platform.Env) int {
	if kindFlagSet {
		return 5
	}
	if lane != "" && Cfg(ctx, LaneKey(lane, "kind"), "", env) != "" {
		return CfgLayerRank(ctx, LaneKey(lane, "kind"), env)
	}
	key := "role_" + strings.ReplaceAll(role, "-", "_") + "_kind"
	if Cfg(ctx, key, "", env) != "" {
		return CfgLayerRank(ctx, key, env)
	}
	return 0
}
func SplitRoles(csv string) []string {
	return strings.FieldsFunc(csv, func(r rune) bool { return r == ',' || isJSWhitespace(r) })
}
func LaneOfRole(ctx *Config, role string, env platform.Env) string {
	for _, lane := range LaneNames(ctx, env) {
		for _, r := range SplitRoles(LaneRolesCSV(ctx, lane, env)) {
			if r == role {
				return lane
			}
		}
	}
	return ""
}

func LaneWorkers(sd, lane string) []string {
	out := []string{}
	for _, line := range RosterRows(sd) {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		name := ""
		col := ""
		if len(f) > 0 {
			name = f[0]
		}
		if len(f) >= 12 {
			col = f[11]
		}
		if col == lane || (col == "" && name == lane) {
			out = append(out, line)
		}
	}
	return out
}

// RosterRename changes the name (first column) of oldName's roster line to
// newName; the rest of the line stays the same. It is the roster half of the
// spawn rename, paired with the herdr agent rename.
func RosterRename(sd, oldName, newName string) {
	WithRosterLock(sd, func() {
		lines := tsvLines(sd)
		for i, line := range lines {
			f := strings.Split(line, "\t")
			if len(f) > 0 && f[0] == oldName {
				f[0] = newName
				lines[i] = strings.Join(f, "\t")
			}
		}
		atomicRoster(filepath.Join(sd, "agents.tsv"), lines)
	})
}

type LaneDecision struct {
	Decision, Name, State, Cause string
	Gone                         []string
	Capacity, N                  int
	Occupants                    []string
	Candidate                    string
}

func LaneDecide(ctx *Config, lane, role string, env platform.Env, cwd string, reuseOn bool) LaneDecision {
	sd := StateDir(ctx, env, cwd)
	cap := LaneCapacity(ctx, lane, env)
	rows := LaneWorkers(sd, lane)
	d := LaneDecision{Capacity: cap, Gone: []string{}, Occupants: []string{}}
	unavailableName, unavailableCause := "", ""
	firstLocked := ""
	locked := 0
	svc := herdr.New(env)
	for _, line := range rows {
		f := strings.Split(line, "\t")
		nm := ""
		if len(f) > 0 {
			nm = f[0]
		}
		st := svc.Runtime.Get(nil, transport.Target{ID: nm}, transport.Call{})
		state := st.Value.State
		if state == "gone" {
			d.Gone = append(d.Gone, nm)
			continue
		}
		d.Occupants = append(d.Occupants, nm)
		if state == "unavailable" {
			if unavailableName == "" {
				unavailableName, unavailableCause = nm, st.Err.Message
			}
			continue
		}
		if state == "idle" || state == "done" {
			rep := LastReport(sd, nm)
			if rep != "" {
				info, e := os.Stat(rep)
				if e != nil || info.Size() == 0 {
					if d.Name == "" {
						d.Name = nm
						d.State = "pending-report"
					}
					continue
				}
			}
			cur, hist := "", ""
			if len(f) > 3 {
				cur = f[3]
			}
			if len(f) >= 11 {
				hist = f[10]
			}
			if IsReviewRole(role) && (RoleIsEdit(cur, env, cwd) || HistoryHasEdit(hist, env, cwd)) {
				if firstLocked == "" {
					firstLocked = nm
				}
				locked++
				continue
			}
			if d.Candidate == "" {
				d.Candidate = nm
			}
			continue
		}
		if d.Name == "" {
			d.Name = nm
			d.State = state
			if d.State == "" {
				d.State = "unknown"
			}
		}
	}
	d.N = len(d.Occupants)
	if len(rows) == 0 {
		d.Decision = "absent"
		d.Name = ""
		d.State = ""
		return d
	}
	if d.Candidate != "" && reuseOn {
		d.Decision = "reuse"
		d.Name = d.Candidate
		d.State = ""
		return d
	}
	if unavailableName != "" {
		d.Decision = "unavailable"
		d.Name = unavailableName
		d.Cause = unavailableCause
		d.State = ""
		return d
	}
	if d.N < cap {
		d.Decision = "open"
		d.Name = ""
		d.State = ""
		return d
	}
	if locked > 0 && locked == d.N {
		d.Decision = "locked"
		d.Name = firstLocked
		d.State = ""
		return d
	}
	d.Decision = "busy"
	return d
}

type LaneSpec struct{ Name, Kind, Model, Effort string }

func SetupLaneSpec(spec string) (LaneSpec, error) {
	i := strings.IndexByte(spec, '=')
	if i < 0 {
		return LaneSpec{}, &platform.ExitError{Code: 2, Msg: "setup: --lane expects name=kind[:model[:effort]]"}
	}
	name := spec[:i]
	if !laneNameRE.MatchString(name) {
		return LaneSpec{}, &platform.ExitError{Code: 2, Msg: fmt.Sprintf("setup: invalid lane name '%s'", name)}
	}
	rest := strings.SplitN(spec[i+1:], "\n", 2)[0]
	parts := strings.Split(rest, ":")
	kind, model, effort := "", "", ""
	if len(parts) > 0 {
		kind = parts[0]
	}
	if len(parts) > 1 {
		model = parts[1]
	}
	if len(parts) > 2 {
		effort = parts[2]
	}
	extra := ""
	if len(parts) > 3 {
		extra = strings.TrimRight(strings.Join(parts[3:], ":"), ":")
	}
	if extra != "" {
		return LaneSpec{}, &platform.ExitError{Code: 2, Msg: fmt.Sprintf("setup: --lane '%s' has too many ':' fields", spec)}
	}
	if kind == "" {
		return LaneSpec{}, &platform.ExitError{Code: 2, Msg: fmt.Sprintf("setup: --lane '%s' needs a kind", spec)}
	}
	if !contains(KnownKinds, kind) {
		return LaneSpec{}, &platform.ExitError{Code: 2, Msg: fmt.Sprintf("setup: unknown kind '%s'", kind)}
	}
	if effort != "" && !contains(EffortLadder, effort) {
		return LaneSpec{}, &platform.ExitError{Code: 2, Msg: fmt.Sprintf("setup: invalid effort '%s'", effort)}
	}
	if strings.Contains(kind+model+effort, "#") {
		return LaneSpec{}, &platform.ExitError{Code: 2, Msg: "setup: --lane value cannot contain #"}
	}
	return LaneSpec{name, kind, model, effort}, nil
}

func MaxWorkers(ctx *Config, env platform.Env) string {
	if LanesEnabled(ctx, env) && !ConfigExplicit(ctx, "max_workers", env) {
		n := LaneCapacitySum(ctx, env)
		if PaneMode(ctx, env) == "flex" {
			n += FlexExtra(ctx, env)
		}
		return strconv.Itoa(n)
	}
	v := Cfg(ctx, "max_workers", "3", env)
	if !decimalLaneRE.MatchString(v) {
		return "3"
	}
	return v
}
func liveAgentsByName(env platform.Env) []any {
	svc := herdr.New(env)
	// LiveWorkerNames and the burst scan still read the list as field
	// objects: rebuild them from the transport terminals with the field
	// names those reads take.
	live := svc.Runtime.List(nil, transport.Scope{}, transport.Call{}).Value
	rows := make([]any, 0, len(live))
	for _, a := range live {
		rows = append(rows, jsonjs.O("name", a.Name, "pane_id", a.PaneID, "agent_status", a.State))
	}
	return rows
}
func agentField(a any, key string) string {
	if o, ok := a.(*jsonjs.Object); ok {
		v, _ := o.Get(key)
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
func LiveWorkerNames(sd string, env platform.Env) []string {
	agents := liveAgentsByName(env)
	out := []string{}
	seen := map[string]bool{}
	for _, line := range RosterRows(sd) {
		f := strings.Split(line, "\t")
		name := ""
		pane := ""
		if len(f) > 0 {
			name = f[0]
		}
		if len(f) > 1 {
			pane = f[1]
		}
		if name == "" || seen[name] {
			continue
		}
		for _, a := range agents {
			if agentField(a, "name") == name && (pane == "" || agentField(a, "pane_id") == pane) {
				seen[name] = true
				out = append(out, name)
				break
			}
		}
	}
	return out
}
func LiveBurstWorkers(sd string, env platform.Env) []string {
	agents := liveAgentsByName(env)
	out := []string{}
	seen := map[string]bool{}
	for _, line := range RosterRows(sd) {
		f := strings.Split(line, "\t")
		name, pane, marker := "", "", ""
		if len(f) > 0 {
			name = f[0]
		}
		if len(f) > 1 {
			pane = f[1]
		}
		if len(f) > 12 {
			marker = f[12]
		}
		if name == "" || marker != "burst" || seen[name] {
			continue
		}
		for _, a := range agents {
			if agentField(a, "name") == name && (pane == "" || agentField(a, "pane_id") == pane) {
				seen[name] = true
				out = append(out, name)
				break
			}
		}
	}
	return out
}
func EnforceWorkerCap(ctx *Config, env platform.Env, cwd string) {
	capStr := MaxWorkers(ctx, env)
	cap, _ := strconv.Atoi(capStr)
	if cap <= 0 {
		return
	}
	sd := StateDir(ctx, env, cwd)
	names := LiveWorkerNames(sd, env)
	if len(names) < cap {
		return
	}
	// The calling agent does not count against its own cap: a promoted
	// sub-orchestrator keeps its roster row until it is demoted, and a worker
	// that calls spawn from its own pane is itself. The other live workers —
	// including other genuine sub-orchestrators — still count.
	svc := herdr.New(env)
	caller := ""
	if svc.Runtime.Caller().PaneID != "" {
		caller = svc.Terminal.CallerAgentName(nil, transport.Call{}).Value
	}
	if caller != "" {
		kept := make([]string, 0, len(names))
		for _, n := range names {
			if n != caller {
				kept = append(kept, n)
			}
		}
		names = kept
	}
	if len(names) < cap {
		return
	}
	platform.DieFriction(fmt.Sprintf("max_workers=%s reached (%d live: %s). Release a finished worker (release <name> --close), let spawn reuse an idle one of the same role (reuse_workers=on / --reuse), or raise max_workers.", capStr, len(names), strings.Join(names, " ")), 8)
}

func FileLaneSignature(file string) string {
	raw, err := platform.ReadTextFile(file)
	if err != nil {
		return ""
	}
	lines := []string{}
	for _, line := range splitLines(raw) {
		body, _ := splitTrailingComment(line)
		s := trimJSWhitespace(body)
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		eq := strings.IndexByte(s, '=')
		if eq < 0 {
			continue
		}
		key := trimJSWhitespace(s[:eq])
		if regexp.MustCompile(`^lane\.[A-Za-z0-9_-]+\.roles$`).MatchString(key) {
			lines = append(lines, strings.TrimSuffix(strings.TrimPrefix(key, "lane."), ".roles")+"="+trimJSWhitespace(s[eq+1:]))
		}
	}
	textutil.SortUTF16(lines)
	return strings.Join(lines, "\n")
}
func PresetSignature(p, mode string) string {
	vals := []string{}
	for _, lane := range PresetLaneNamesFor(p, mode) {
		vals = append(vals, lane+"="+PresetLaneRoles(lane, p, mode))
	}
	textutil.SortUTF16(vals)
	return strings.Join(vals, "\n")
}
func FileLanedRoles(file string) string {
	sig := FileLaneSignature(file)
	if sig == "" {
		return ""
	}
	out := []string{}
	for _, line := range strings.Split(sig, "\n") {
		i := strings.IndexByte(line, '=')
		if i >= 0 {
			out = append(out, line[i+1:])
		}
	}
	return strings.Join(out, ",")
}
func FileLaneCount(file string) int {
	sig := FileLaneSignature(file)
	if sig == "" {
		return 0
	}
	return len(strings.Split(sig, "\n"))
}

type DropLegacyOptions struct {
	DropKind, DropModel []string
	DropLanes           bool
}

func ConfigDropLegacy(file string, opts DropLegacyOptions) error {
	raw, err := platform.ReadTextFile(file)
	if err != nil {
		return &platform.ExitError{Code: 4, Msg: fmt.Sprintf("could not rewrite %s (file left untouched)", file)}
	}
	kinds, models := map[string]bool{}, map[string]bool{}
	for _, v := range opts.DropKind {
		kinds[v] = true
	}
	for _, v := range opts.DropModel {
		models[v] = true
	}
	out := []string{}
	for _, line := range splitLines(raw) {
		body, _ := splitTrailingComment(line)
		s := trimJSWhitespace(body)
		if s == "" || strings.HasPrefix(s, "#") {
			out = append(out, line)
			continue
		}
		eq := strings.IndexByte(s, '=')
		if eq < 0 {
			out = append(out, line)
			continue
		}
		key := trimJSWhitespace(s[:eq])
		drop := strings.HasPrefix(key, "role.planner.")
		if strings.HasPrefix(key, "role.") && strings.HasSuffix(key, ".kind") {
			drop = drop || kinds[strings.TrimSuffix(strings.TrimPrefix(key, "role."), ".kind")]
		} else if strings.HasPrefix(key, "role.") && strings.HasSuffix(key, ".model") {
			drop = drop || models[strings.TrimSuffix(strings.TrimPrefix(key, "role."), ".model")]
		}
		if opts.DropLanes && laneRolesRE.MatchString(key) {
			drop = true
		}
		if !drop {
			out = append(out, line)
		}
	}
	content := ""
	if len(out) > 0 {
		content = strings.Join(out, "\n") + "\n"
	}
	if err := platform.AtomicWrite(file, content); err != nil {
		return &platform.ExitError{Code: 4, Msg: fmt.Sprintf("could not rewrite %s (file left untouched)", file)}
	}
	return nil
}
func FileRoleResolved(file, role, attr string, env platform.Env, cwd string) string {
	v := FileKeyValue(file, "role."+role+"."+attr)
	if v == "" {
		if f := RoleFile(role, env, cwd); f != "" {
			v = FmGet(f, attr)
		}
	}
	return v
}

type laneMigration struct{ DropKind, DropModel, Lines []string }

func MigrateLaneAttr(file, lane, rolesCSV, attr string, acc *laneMigration, env platform.Env, cwd string) {
	existing := FileKeyValue(file, "lane."+lane+"."+attr)
	laneKind := FileKeyValue(file, "lane."+lane+".kind")
	first, have, agree := "", false, true
	vals := []string{}
	for _, role := range SplitRoles(rolesCSV) {
		if role == "planner" || role == "documenter" {
			continue
		}
		if existing != "" {
			if attr == "kind" {
				acc.DropKind = append(acc.DropKind, role)
			} else {
				acc.DropModel = append(acc.DropModel, role)
			}
			continue
		}
		value := ""
		if attr == "model" {
			if laneKind == "" {
				return
			}
			value = FileKeyValue(file, "role."+role+".model")
			if value == "" {
				if rf := RoleFile(role, env, cwd); rf != "" && FmGet(rf, "kind") == laneKind {
					value = FmGet(rf, "model")
				}
			}
			if value == "" {
				continue
			}
		} else {
			value = FileRoleResolved(file, role, attr, env, cwd)
		}
		vals = append(vals, role+"="+value)
		if !have {
			first = value
			have = true
		} else if first != value {
			agree = false
		}
	}
	if existing != "" || !have {
		return
	}
	if agree && first != "" {
		ConfigWritePair(file, "lane."+lane+"."+attr, first, env, cwd)
		acc.Lines = append(acc.Lines, "set lane."+lane+"."+attr+"="+first)
		for _, role := range SplitRoles(rolesCSV) {
			if role == "planner" || role == "documenter" {
				continue
			}
			if attr == "kind" {
				acc.DropKind = append(acc.DropKind, role)
			} else {
				acc.DropModel = append(acc.DropModel, role)
			}
		}
		return
	}
	if !agree {
		msg := fmt.Sprintf("lane '%s' %ss differ (%s). Left the role.*.%s keys in place. Orchestrator: ask the user which %s this lane should use, then run 'setup --lane %s=<kind>[:<model>[:<effort>]]'.", lane, attr, strings.Join(vals, " "), attr, attr, lane)
		_, _ = fmt.Fprintf(platform.Stderr, "soho: warning: %s\n", msg)
	}
}

var laneAttrRE = regexp.MustCompile(`^lane\.([A-Za-z0-9_-]+)\.(kind|model|effort|approvals|panes|args)$`)
var laneRolesRE = regexp.MustCompile(`^lane\.[A-Za-z0-9_-]+\.roles$`)

func fileKeyPresent(file, key string) bool {
	raw, e := platform.ReadTextFile(file)
	if e != nil {
		return false
	}
	for _, line := range splitLines(raw) {
		body, _ := splitTrailingComment(line)
		s := trimJSWhitespace(body)
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		eq := strings.IndexByte(s, '=')
		if eq >= 0 && trimJSWhitespace(s[:eq]) == key {
			return true
		}
	}
	return false
}
func dropPresetFileExtras(file, panes string, lines *[]string, mode string) {
	raw, e := platform.ReadTextFile(file)
	if e != nil {
		platform.Die(fmt.Sprintf("could not rewrite %s (file left untouched)", file), 4)
	}
	preset := map[string]bool{}
	for _, lane := range PresetLaneNamesFor(panes, mode) {
		preset[lane] = true
	}
	out := []string{}
	moved := map[string]bool{}
	for _, rawLine := range splitLines(raw) {
		body, comment := splitTrailingComment(rawLine)
		s := trimJSWhitespace(body)
		if s == "" || strings.HasPrefix(s, "#") {
			out = append(out, rawLine)
			continue
		}
		eq := strings.IndexByte(s, '=')
		if eq < 0 {
			out = append(out, rawLine)
			continue
		}
		key, value := trimJSWhitespace(s[:eq]), trimJSWhitespace(s[eq+1:])
		if laneRolesRE.MatchString(key) {
			continue
		}
		if key == "max_workers" {
			*lines = append(*lines, "removed max_workers="+value+" (derived from the lanes)")
			continue
		}
		if key == "split_max_panes" {
			*lines = append(*lines, "removed split_max_panes="+value+" (derived from panes)")
			continue
		}
		m := laneAttrRE.FindStringSubmatch(key)
		if m != nil && !preset[m[1]] {
			lane, attr := m[1], m[2]
			if lane == "read" && preset["review"] {
				target := "lane.review." + attr
				if fileKeyPresent(file, target) || moved[attr] {
					*lines = append(*lines, fmt.Sprintf("removed lane.read.%s=%s (%s is already set)", attr, value, target))
					continue
				}
				effective := FileKeyValue(file, "lane.read."+attr)
				out = append(out, target+"="+effective+comment)
				moved[attr] = true
				*lines = append(*lines, fmt.Sprintf("moved lane.read.%s=%s to %s", attr, effective, target))
				continue
			}
			if lane == "explore" {
				*lines = append(*lines, fmt.Sprintf("removed lane.explore.%s=%s (research runs on the build lane now)", attr, value))
				continue
			}
			*lines = append(*lines, fmt.Sprintf("removed lane.%s.%s=%s (no lane '%s' in the panes=%s preset)", lane, attr, value, lane, panes))
			continue
		}
		out = append(out, rawLine)
	}
	content := ""
	if len(out) > 0 {
		content = strings.Join(out, "\n") + "\n"
	}
	if e := platform.AtomicWrite(file, content); e != nil {
		platform.Die(fmt.Sprintf("could not rewrite %s (file left untouched)", file), 4)
	}
}
func ApplyLaneFile(file, panes string, env platform.Env, cwd string) []string {
	if _, e := os.Stat(file); os.IsNotExist(e) {
		if e = os.MkdirAll(filepath.Dir(file), 0o755); e != nil {
			platform.Die(fmt.Sprintf("config set: could not rewrite %s (file left untouched)", file), 4)
		}
		f, openErr := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o666)
		if openErr == nil {
			openErr = f.Close()
		}
		if openErr != nil {
			platform.Die(fmt.Sprintf("config set: could not rewrite %s (file left untouched)", file), 4)
		}
	}
	sig := FileLaneSignature(file)
	presetSigs := map[string]bool{}
	for _, p := range []string{"2", "3", "4"} {
		for _, mode := range []string{"strict", "flex"} {
			presetSigs[PresetSignature(p, mode)] = true
		}
	}
	isPreset := sig == "" || presetSigs[sig] || sig == LegacyPresetSignature("3") || sig == LegacyPresetSignature("4")
	ctx := LoadConfig(env, cwd)
	mode := PaneMode(&ctx, env)
	lines := []string{}
	if !isPreset {
		_, _ = fmt.Fprintf(platform.Stderr, "soho: warning: lane roles in %s are custom; left in place. Remove them to restore the panes=%s preset.\n", file, panes)
	} else {
		dropPresetFileExtras(file, panes, &lines, mode)
	}
	acc := laneMigration{Lines: lines}
	pairs := [][2]string{}
	if isPreset {
		for _, lane := range PresetLaneNamesFor(panes, mode) {
			pairs = append(pairs, [2]string{lane, PresetLaneRoles(lane, panes, mode)})
		}
	} else {
		for _, line := range strings.Split(sig, "\n") {
			if line == "" {
				continue
			}
			i := strings.IndexByte(line, '=')
			if i >= 0 {
				pairs = append(pairs, [2]string{line[:i], line[i+1:]})
			}
		}
	}
	for _, pair := range pairs {
		MigrateLaneAttr(file, pair[0], pair[1], "kind", &acc, env, cwd)
		MigrateLaneAttr(file, pair[0], pair[1], "model", &acc, env, cwd)
	}
	if err := ConfigDropLegacy(file, DropLegacyOptions{acc.DropKind, acc.DropModel, isPreset}); err != nil {
		panic(err)
	}
	ConfigWritePair(file, "panes", panes, env, cwd)
	lines = append(acc.Lines, "set panes="+panes)
	if !isPreset {
		sum := LaneCapacitySum(&ctx, env)
		if mode == "flex" {
			sum += FlexExtra(&ctx, env)
		}
		ConfigWritePair(file, "max_workers", strconv.Itoa(sum), env, cwd)
		lines = append(lines, "set max_workers="+strconv.Itoa(sum))
		// The caller plus the whole team, as layout.SplitCap and the doctor
		// measure it; with no team cap, panes (plus flex_extra).
		splitCap := 1 + sum
		if sum == 0 {
			splitCap, _ = strconv.Atoi(panes)
			if mode == "flex" {
				splitCap += FlexExtra(&ctx, env)
			}
		}
		ConfigWritePair(file, "split_max_panes", strconv.Itoa(splitCap), env, cwd)
		lines = append(lines, "set split_max_panes="+strconv.Itoa(splitCap))
	}
	ConfigWritePair(file, "reuse_workers", "on", env, cwd)
	lines = append(lines, "set reuse_workers=on", "removed role.planner.* and the role kind/model keys of lanes that agreed; divergent lanes kept theirs")
	return lines
}
