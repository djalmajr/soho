package job

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

// Machine is the per-machine job configuration.
// Orgs is empty when job_orgs is empty, which allows no organization.
// ReposRoot is absolute when the configured value starts with ~ or %USERPROFILE%.
// TimeoutMin is the default job budget in minutes.
// BlockedTimeoutMin is the default blocked budget in minutes.
// FinishingTimeoutMin is the terminal procedure's (finishing) budget in
// minutes.
type Machine struct {
	Label               string
	Orgs                []string
	ReposRoot           string
	TimeoutMin          int
	BlockedTimeoutMin   int
	FinishingTimeoutMin int
	WakeCmd             string

	// reposRootHome records that ReposRoot was expanded from the home
	// directory (a leading ~ or %USERPROFILE%): the job start then hands
	// the home variable to the supervisor so it resolves the same root.
	reposRootHome bool
}

// Machine defaults: the user configuration file's values win, and a missing
// file or key falls back to these.
const (
	defaultMachineLabel        = ""
	defaultJobOrgs             = ""
	defaultJobReposRoot        = "~/repo.git"
	defaultJobTimeout          = "120"
	defaultJobBlockedTimeout   = "1440"
	defaultJobFinishingTimeout = "10"
	defaultJobWakeCmd          = ""
)

// LoadMachine reads machine_label, job_orgs, job_repos_root, job_timeout,
// job_blocked_timeout, job_finishing_timeout and job_wake_cmd only from the
// user configuration file (the current path, else the legacy one) through
// core.ReadConfigFilePairs, falling back to the defaults above. osName
// selects the user file and the home directory used to expand a leading ~
// or %USERPROFILE%. Project and session files and SOHO_* environment
// variables never change these seven values. An unreadable user file, an
// invalid timeout, an invalid blocked timeout, an invalid finishing
// timeout, or an invalid organization token is *platform.ExitError
// with code 2. An unresolvable ~ panics with the same exit platform.HomeDir
// uses.
func LoadMachine(osName string, env platform.Env) (Machine, error) {
	return LoadMachineFor(osName, env, core.UserSelection{})
}

// MachineValues is the raw string of the seven machine keys LoadMachineFor
// reads: the defaults above overridden by the user configuration file
// (EffectiveUserConfigFile for sel), before any interpretation (no ~
// expansion, no organization split, no timeout parsing). Project and
// session files and SOHO_* environment variables never change these values.
// An unreadable user file is *platform.ExitError with code 2, the same
// failure LoadMachineFor returns.
func MachineValues(osName string, env platform.Env, sel core.UserSelection) (map[string]string, error) {
	file := core.EffectiveUserConfigFile(osName, env, sel)
	pairs, err := core.ReadConfigFilePairs(file)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, exitErr(2, "job: cannot read user configuration")
		}
	}
	values := map[string]string{
		"machine_label":         defaultMachineLabel,
		"job_orgs":              defaultJobOrgs,
		"job_repos_root":        defaultJobReposRoot,
		"job_timeout":           defaultJobTimeout,
		"job_blocked_timeout":   defaultJobBlockedTimeout,
		"job_finishing_timeout": defaultJobFinishingTimeout,
		"job_wake_cmd":          defaultJobWakeCmd,
	}
	for _, pair := range pairs {
		if _, known := values[pair.Key]; known {
			values[pair.Key] = pair.Value
		}
	}
	return values, nil
}

// LoadMachineFor is LoadMachine with an explicit user configuration
// selection: the user file comes from sel.Base and a home-relative
// job_repos_root expands from sel.Home when they are set (the job
// supervisor's view of its starter's selection); the zero selection is
// LoadMachine. The seven raw values come from MachineValues.
func LoadMachineFor(osName string, env platform.Env, sel core.UserSelection) (Machine, error) {
	values, err := MachineValues(osName, env, sel)
	if err != nil {
		return Machine{}, err
	}
	orgs, err := parseOrgs(values["job_orgs"])
	if err != nil {
		return Machine{}, err
	}
	timeout, err := parseTimeout(values["job_timeout"])
	if err != nil {
		return Machine{}, err
	}
	blockedTimeout, err := parseBlockedTimeout(values["job_blocked_timeout"])
	if err != nil {
		return Machine{}, err
	}
	finishingTimeout, err := parseFinishingTimeout(values["job_finishing_timeout"])
	if err != nil {
		return Machine{}, err
	}
	return Machine{
		Label:               values["machine_label"],
		Orgs:                orgs,
		ReposRoot:           expandReposRoot(osName, env, sel, values["job_repos_root"]),
		TimeoutMin:          timeout,
		BlockedTimeoutMin:   blockedTimeout,
		FinishingTimeoutMin: finishingTimeout,
		WakeCmd:             values["job_wake_cmd"],
		reposRootHome:       reposRootFromHome(values["job_repos_root"]),
	}, nil
}

// LabelMatches accepts an absent brief maquina. A present value must equal Label.
func (m Machine) LabelMatches(briefMachine string) error {
	if briefMachine == "" || briefMachine == m.Label {
		return nil
	}
	return exitErr(2, "job: machine_label does not match brief maquina")
}

// AllowsOrg reports whether org is listed in job_orgs.
func (m Machine) AllowsOrg(org string) error {
	for _, candidate := range m.Orgs {
		if candidate == org {
			return nil
		}
	}
	return exitErr(2, "job: organization out of scope")
}

// Admit checks brief maquina, the repository name, and organization membership.
func (m Machine) Admit(briefMachine, org, repo string) error {
	if err := m.LabelMatches(briefMachine); err != nil {
		return err
	}
	if !pathSegment(repo) {
		return exitErr(2, "job: invalid repository")
	}
	if !pathSegment(org) {
		return exitErr(2, "job: invalid organization")
	}
	return m.AllowsOrg(org)
}

func parseOrgs(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return []string{}, nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		org := strings.TrimSpace(part)
		if org == "" {
			return nil, exitErr(2, "job: empty organization in job_orgs")
		}
		if !pathSegment(org) {
			return nil, exitErr(2, "job: invalid organization in job_orgs")
		}
		out = append(out, org)
	}
	return out, nil
}

func parseTimeout(raw string) (int, error) {
	if raw == "" {
		raw = "120"
	}
	if !core.ConfigValueOk("job_timeout", raw, nil, "") {
		return 0, exitErr(2, "job: invalid job_timeout '"+raw+"'")
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, exitErr(2, "job: invalid job_timeout '"+raw+"'")
	}
	return n, nil
}

// parseBlockedTimeout is the blocked budget's machine key: 1..43200 minutes
// (config.go's jobBlockedTimeoutValueOk), the same shape as parseTimeout.
func parseBlockedTimeout(raw string) (int, error) {
	if raw == "" {
		raw = "1440"
	}
	if !core.ConfigValueOk("job_blocked_timeout", raw, nil, "") {
		return 0, exitErr(2, "job: invalid job_blocked_timeout '"+raw+"'")
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, exitErr(2, "job: invalid job_blocked_timeout '"+raw+"'")
	}
	return n, nil
}

// parseFinishingTimeout is the terminal procedure's budget machine key:
// 1..1440 minutes (config.go's jobFinishingTimeoutValueOk), the same shape
// as parseBlockedTimeout.
func parseFinishingTimeout(raw string) (int, error) {
	if raw == "" {
		raw = defaultJobFinishingTimeout
	}
	if !core.ConfigValueOk("job_finishing_timeout", raw, nil, "") {
		return 0, exitErr(2, "job: invalid job_finishing_timeout '"+raw+"'")
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, exitErr(2, "job: invalid job_finishing_timeout '"+raw+"'")
	}
	return n, nil
}

func expandReposRoot(osName string, env platform.Env, sel core.UserSelection, raw string) string {
	if raw == "" {
		raw = "~/repo.git"
	}
	if raw == "~" {
		return core.SelectedHomeDir(osName, env, sel)
	}
	if rest, ok := strings.CutPrefix(raw, "~/"); ok {
		return joinHome(osName, env, sel, rest)
	}
	if rest, ok := strings.CutPrefix(raw, `~\`); ok {
		return joinHome(osName, env, sel, rest)
	}
	if rest, ok := cutUserProfile(raw); ok {
		return joinHome(osName, env, sel, rest)
	}
	return raw
}

// reposRootFromHome reports whether expandReposRoot expands raw from the
// home directory: the same prefixes it handles, empty being the ~ default.
func reposRootFromHome(raw string) bool {
	if raw == "" || raw == "~" || strings.HasPrefix(raw, "~/") || strings.HasPrefix(raw, `~\`) {
		return true
	}
	_, ok := cutUserProfile(raw)
	return ok
}

func cutUserProfile(raw string) (string, bool) {
	const prefix = "%USERPROFILE%"
	if len(raw) < len(prefix) || !strings.EqualFold(raw[:len(prefix)], prefix) {
		return "", false
	}
	return raw[len(prefix):], true
}

func joinHome(osName string, env platform.Env, sel core.UserSelection, rest string) string {
	home := core.SelectedHomeDir(osName, env, sel)
	rest = strings.TrimLeft(rest, `/\`)
	if rest == "" {
		return home
	}
	rest = strings.ReplaceAll(rest, `\`, "/")
	parts := strings.Split(rest, "/")
	return filepath.Join(append([]string{home}, parts...)...)
}
