package job

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

// segmentNameRE is the contract class for a repository name. Organization
// tokens use it too: a checkout lives at <root>/<org>/<repo>, and the brief
// repo is <org>/<repo>.
var segmentNameRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)

// laneRolesKeyRE matches a lane.<name>.roles key accepted by core.ConfigKeyOk.
var laneRolesKeyRE = regexp.MustCompile(`^lane\.[a-z][a-z0-9_-]*\.roles$`)

// Team is a resolved machine team after brief equipe overrides.
// Source is the report field equipe.fonte. OverrideKeys are the brief keys
// in sorted order. Pairs keep file order; new override keys follow, sorted.
type Team struct {
	Source       string
	File         string
	Pairs        []TeamPair
	OverrideKeys []string
}

// TeamPair is one session.conf key. Keys keep the dotted spelling.
type TeamPair struct {
	Key   string
	Value string
}

type teamCandidate struct {
	source string
	file   string
	roles  bool
}

// ResolveTeam reads teams/<org>/<repo>.conf, then teams/default.conf, then
// <repoRoot>/.agents/soho.conf when that file sets lane.*.roles. Each team
// file is read from the soho user configuration directory. The source
// keeps the same relative name.
// osName is the platform name platform.UserConfigPath expects.
// overrides is the brief equipe map; nil means none. Keys and values are
// checked with core.ConfigKeyOk and core.ConfigValueOk, the session set
// parser. A missing team, an unknown key, or an invalid value is
// *platform.ExitError with code 2. Pane presets are never applied.
func ResolveTeam(osName string, env platform.Env, repoRoot, org, repo string, overrides map[string]string) (Team, error) {
	if !pathSegment(org) {
		return Team{}, exitErr(2, "team: invalid organization")
	}
	if !pathSegment(repo) {
		return Team{}, exitErr(2, "team: invalid repository")
	}
	userDir := filepath.Dir(platform.UserConfigPath(osName, env))
	candidates := []teamCandidate{
		{source: "teams/" + org + "/" + repo + ".conf", file: filepath.Join(userDir, "teams", org, repo+".conf")},
		{source: "teams/default.conf", file: filepath.Join(userDir, "teams", "default.conf")},
		{source: ".agents/soho.conf", file: filepath.Join(repoRoot, ".agents", "soho.conf"), roles: true},
	}
	var chosen teamCandidate
	var pairs []TeamPair
	found := false
	for _, candidate := range candidates {
		regular, err := regularTeamFile(candidate.file)
		if err != nil {
			return Team{}, err
		}
		if !regular {
			continue
		}
		loaded, err := loadTeamPairs(candidate.file)
		if err != nil {
			return Team{}, exitErr(2, "team: cannot read team file")
		}
		if candidate.roles && !hasLaneRoles(loaded) {
			continue
		}
		if err := validatePairs(loaded, env, repoRoot); err != nil {
			return Team{}, err
		}
		chosen = candidate
		pairs = loaded
		found = true
		break
	}
	if !found {
		return Team{}, exitErr(2, "team: no team configuration")
	}
	merged, keys, err := applyOverrides(pairs, overrides, env, repoRoot)
	if err != nil {
		return Team{}, err
	}
	if !hasLaneRoles(merged) {
		return Team{}, exitErr(2, "team: no lane.*.roles")
	}
	return Team{Source: chosen.source, File: chosen.file, Pairs: merged, OverrideKeys: keys}, nil
}

func pathSegment(name string) bool {
	return segmentNameRE.MatchString(name) && name != "." && name != ".."
}

func regularTeamFile(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, exitErr(2, "team: cannot read team file")
	}
	if !info.Mode().IsRegular() {
		return false, exitErr(2, "team: team path is not a file")
	}
	return true, nil
}

// loadTeamPairs reads one team file with the shared single-file config
// parser (core.ReadConfigFilePairs), keeping the raw dotted key spelling so
// validation and reporting behave exactly like session set.
func loadTeamPairs(file string) ([]TeamPair, error) {
	parsed, err := core.ReadConfigFilePairs(file)
	if err != nil {
		return nil, err
	}
	pairs := make([]TeamPair, 0, len(parsed))
	for _, item := range parsed {
		pairs = append(pairs, TeamPair{Key: item.Original, Value: item.Value})
	}
	return pairs, nil
}

func validatePairs(pairs []TeamPair, env platform.Env, cwd string) error {
	for _, pair := range pairs {
		if err := validatePair(pair.Key, pair.Value, env, cwd); err != nil {
			return err
		}
	}
	return nil
}

func validatePair(key, value string, env platform.Env, cwd string) error {
	if value == "" {
		return exitErr(2, "team: empty value for "+key)
	}
	if !core.ConfigKeyOk(key) {
		return exitErr(2, "team: unknown key '"+key+"'")
	}
	if !core.ConfigValueOk(key, value, env, cwd) {
		return exitErr(2, "team: invalid value '"+value+"' for "+key)
	}
	return nil
}

func applyOverrides(pairs []TeamPair, overrides map[string]string, env platform.Env, cwd string) ([]TeamPair, []string, error) {
	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := validatePair(key, overrides[key], env, cwd); err != nil {
			return nil, nil, err
		}
		replaced := false
		for i := range pairs {
			if pairs[i].Key == key {
				pairs[i].Value = overrides[key]
				replaced = true
				break
			}
		}
		if !replaced {
			pairs = append(pairs, TeamPair{Key: key, Value: overrides[key]})
		}
	}
	return pairs, keys, nil
}

func hasLaneRoles(pairs []TeamPair) bool {
	for _, pair := range pairs {
		if laneRolesKeyRE.MatchString(pair.Key) && pair.Value != "" {
			return true
		}
	}
	return false
}

func exitErr(code int, msg string) error {
	return &platform.ExitError{Code: code, Msg: msg}
}

var teamRoleKnobs = [...]string{"kind", "model", "effort", "approvals"}

// teamSessionView resolves the selected session over base defaults while
// excluding actor role keys that could hide the team's group settings.
func teamSessionView(pairs []core.ConfigPair, roles []string, base *core.Config, env platform.Env, cwd string) teamView {
	ctx := core.Config{Entries: maps.Clone(base.Entries), Order: slices.Clone(base.Order)}
	for _, role := range roles {
		for _, knob := range teamRoleKnobs {
			delete(ctx.Entries, core.NormalizeKey("role."+role+"."+knob))
		}
	}
	for _, pair := range pairs {
		ctx.Entries[pair.Key] = core.ConfigEntry{Value: pair.Value, Source: "session", Original: pair.Original}
		ctx.Order = append(ctx.Order, pair.Key)
	}
	return resolveTeamView(roles, &ctx, env, cwd)
}

// actorGroupConflicts reports each effective base-layer role assignment
// that would replace a group setting in the selected team.
func actorGroupConflicts(roles []string, actor *core.Config, team teamView) []string {
	var conflicts []string
	for _, role := range roles {
		res := team.roles[role]
		sources := [...]string{res.KindFrom, res.ModelFrom, res.EffortFrom, res.ApprovalsFrom}
		for field, knob := range teamRoleKnobs {
			key := "role." + role + "." + knob
			entry := actor.Entries[core.NormalizeKey(key)]
			if entry.Value != "" && strings.HasPrefix(sources[field], "lane ") {
				conflicts = append(conflicts, fmt.Sprintf("job: %s in %s config overrides the team's lane.%s.%s; remove it or set the override in the team", key, entry.Source, res.Lane, knob))
			}
		}
	}
	return conflicts
}
