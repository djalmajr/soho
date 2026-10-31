// Package codexenv parses Codex shell environment policy and diagnoses whether
// Codex's process ancestry belongs to a Herdr pane.
package codexenv

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
)

type Policy struct {
	Inherit     *string  `json:"inherit"`
	IncludeOnly []string `json:"include_only"`
	Exclude     []string `json:"exclude"`
	Set         []string `json:"set"`
}
type Evaluation struct {
	Drops  bool   `json:"drops"`
	Reason string `json:"reason"`
}
type Process struct {
	PID  int    `json:"pid"`
	Name string `json:"name"`
}

func StripTomlComment(line string) string {
	d, s := false, false
	for i := 0; i < len(line); i++ {
		c := line[i]
		if c == '"' && !s && (i == 0 || line[i-1] != '\\') {
			d = !d
		} else if c == '\'' && !d {
			s = !s
		} else if c == '#' && !d && !s {
			return line[:i]
		}
	}
	return line
}

func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

// GlobMatch implements the JS ASCII case-insensitive * and ? matcher.
func GlobMatch(pattern, value string) bool {
	p := asciiLower(pattern)
	s := asciiLower(value)
	pi, si, star, mark := 0, 0, -1, 0
	for si < len(s) {
		if pi < len(p) && (p[pi] == '?' || p[pi] == s[si]) {
			pi++
			si++
		} else if pi < len(p) && p[pi] == '*' {
			star = pi
			pi++
			mark = si
		} else if star >= 0 {
			pi = star + 1
			mark++
			si = mark
		} else {
			return false
		}
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}

func EvaluateCodexPolicy(p *Policy) Evaluation {
	if p == nil {
		return Evaluation{}
	}
	inherit := ""
	if p.Inherit != nil {
		inherit = strings.ToLower(strings.TrimSpace(*p.Inherit))
	}
	set := map[string]bool{}
	for _, s := range p.Set {
		set[s] = true
	}
	required := []string{"HERDR_ENV", "HERDR_PANE_ID", "HERDR_WORKSPACE_ID"}
	for _, name := range required {
		present := true
		reason := ""
		if inherit == "core" || inherit == "none" {
			present = false
			reason = `inherit="` + inherit + `"`
		}
		if present {
			for _, pat := range p.Exclude {
				if GlobMatch(pat, name) {
					present = false
					reason = "exclude matches " + name
					break
				}
			}
		}
		if set[name] {
			present = true
			reason = ""
		}
		if len(p.IncludeOnly) > 0 {
			match := false
			for _, pat := range p.IncludeOnly {
				match = match || GlobMatch(pat, name)
			}
			if !match && present {
				present = false
				reason = "include_only does not match " + name
			}
		}
		if !present {
			return Evaluation{true, reason}
		}
	}
	return Evaluation{}
}

func ReadCodexPolicy(env platform.Env, goos string) *Policy {
	home := env.Get("CODEX_HOME")
	if home == "" {
		home = platform.HomeDir(goos, env)
		home = filepath.Join(home, ".codex")
	}
	b, e := os.ReadFile(filepath.Join(home, "config.toml"))
	if e != nil {
		return nil
	}
	return ParseCodexPolicy(strings.ToValidUTF8(string(b), "�"))
}

func DoctorCodexPolicyWarnings(env platform.Env, goos string, warn func(string)) {
	p := ReadCodexPolicy(env, goos)
	e := EvaluateCodexPolicy(p)
	if e.Drops {
		warn(fmt.Sprintf(`codex: shell_environment_policy drops HERDR_* (%s): commands Codex runs cannot see Herdr; see the Codex section of docs/guide.md`, e.Reason))
	}
}

func GetProcessAncestors(start int, env platform.Env, goos string) []Process {
	var out []Process
	curr := start
	for depth := 0; depth < 64 && curr > 1; depth++ {
		ppid := 0
		comm := ""
		haveParent := false
		unrepresentableParent := false
		r := platform.RunCli("ps", []string{"-o", "ppid=,comm=", "-p", strconv.Itoa(curr)}, platform.RunOptions{Env: env, Platform: goos, TimeoutMs: 10000})
		if r.Status != nil && *r.Status == 0 && r.Stdout != "" {
			fields := strings.Fields(strings.Split(strings.TrimSpace(r.Stdout), "\n")[0])
			if len(fields) >= 2 {
				if parsed, err := strconv.Atoi(fields[0]); err == nil {
					ppid = parsed
					comm = strings.Join(fields[1:], " ")
					haveParent = true
				} else {
					unrepresentableParent = true
				}
			}
		}
		if !haveParent && !unrepresentableParent && goos == "linux" {
			if b, e := os.ReadFile(fmt.Sprintf("/proc/%d/stat", curr)); e == nil {
				re := regexp.MustCompile(`^([0-9]+)\s+\((.+)\)\s+\S+\s+([0-9]+)`)
				m := re.FindStringSubmatch(string(b))
				if m != nil {
					comm = m[2]
					if parsed, err := strconv.Atoi(m[3]); err == nil {
						ppid = parsed
						haveParent = true
					} else {
						unrepresentableParent = true
					}
				}
			}
		}
		if !haveParent || comm == "" {
			break
		}
		name := strings.TrimPrefix(filepath.Base(comm), "-")
		if curr != start {
			out = append(out, Process{curr, name})
		}
		if ppid == 0 {
			break
		}
		if ppid <= 1 || ppid == curr {
			if ppid == 1 && curr != 1 {
				out = append(out, Process{1, "init"})
			}
			break
		}
		curr = ppid
	}
	return out
}

// DiagnoseRunners is the two raw herdr operations DiagnoseOutsideHerdr runs:
// the api snapshot and one pane process-info per candidate pane. A caller on
// the transport interface fills it from its transport.Services; when none
// is passed the raw herdr CLI runs with the same argv, the same 10-second
// bound and the same Platform as the injected path.
type DiagnoseRunners struct {
	Snapshot    func() platform.RunResult
	ProcessInfo func(paneID string) platform.RunResult
}

func DiagnoseOutsideHerdr(base string, env platform.Env, goos string, pid int, ancestors []Process, runners ...DiagnoseRunners) string {
	if goos == "win32" {
		return base
	}
	if _, ok := platform.FindExecutable("herdr", env, goos); !ok {
		return base
	}
	if ancestors == nil {
		ancestors = GetProcessAncestors(pid, env, goos)
	}
	has := false
	for _, a := range ancestors {
		if strings.EqualFold(a.Name, "codex") {
			has = true
		}
	}
	if !has {
		return base
	}
	var runner DiagnoseRunners
	if len(runners) > 0 {
		runner = runners[0]
	}
	var snap platform.RunResult
	if runner.Snapshot != nil {
		snap = runner.Snapshot()
	} else {
		snap = platform.RunCli("herdr", []string{"api", "snapshot"}, platform.RunOptions{Env: env, Platform: goos, TimeoutMs: 10000})
	}
	if snap.Status == nil || *snap.Status != 0 || snap.Stdout == "" {
		return base
	}
	snapData, err := jsonjs.Parse([]byte(snap.Stdout))
	if err != nil {
		return base
	}
	panesValue := getJSON(snapData, "result", "snapshot", "panes")
	if panesValue == nil {
		panesValue = getJSON(snapData, "result", "panes")
	}
	panes, isArray := panesValue.([]any)
	if !isArray {
		return base
	}
	ids := map[int]bool{}
	for _, a := range ancestors {
		ids[a.PID] = true
	}
	matches := []any{}
	for _, pane := range panes {
		if pane == nil {
			continue
		}
		agent := getJSON(pane, "agent")
		if agent == nil {
			agent = ""
		}
		agentName, isString := agent.(string)
		if !isString {
			panic("TypeError: (p.agent ?? '').toLowerCase is not a function")
		}
		paneIDValue := getJSON(pane, "pane_id")
		if !jsTruthyCodex(paneIDValue) || asciiLower(agentName) != "codex" || jsTruthyCodex(getJSON(pane, "remote")) || jsTruthyCodex(getJSON(pane, "machine")) {
			continue
		}
		paneID := jsonJSString(paneIDValue)
		var r platform.RunResult
		if runner.ProcessInfo != nil {
			r = runner.ProcessInfo(paneID)
		} else {
			r = platform.RunCli("herdr", []string{"pane", "process-info", "--pane", paneID}, platform.RunOptions{Env: env, Platform: goos, TimeoutMs: 10000})
		}
		if r.Status == nil || *r.Status != 0 || r.Stdout == "" {
			continue
		}
		info, err := jsonjs.Parse([]byte(r.Stdout))
		if err != nil {
			continue
		}
		foreground, ok := getJSON(info, "result", "process_info", "foreground_processes").([]any)
		if !ok {
			continue
		}
		for _, proc := range foreground {
			if pid, valid := numberAsPID(getJSON(proc, "pid")); valid && ids[pid] {
				matches = append(matches, paneIDValue)
				break
			}
		}
	}
	if len(matches) == 1 {
		id := jsonJSString(matches[0])
		if !strings.HasPrefix(id, "local/") {
			id = "local/" + id
		}
		return fmt.Sprintf("this command runs under Codex in Herdr pane %s (matched by process ancestry), but Codex's shell_environment_policy does not pass HERDR_*; allow them (see soho doctor) and restart Codex", id)
	}
	return base + " (a Codex ancestor was found, but no single Herdr pane matched it)"
}

func getJSON(value any, keys ...string) any {
	for _, key := range keys {
		object, ok := value.(*jsonjs.Object)
		if !ok {
			return nil
		}
		value, _ = object.Get(key)
	}
	return value
}

func jsTruthyCodex(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return v != ""
	case json.Number:
		number, err := strconv.ParseFloat(string(v), 64)
		return err != nil || number != 0
	case float64:
		return v != 0
	default:
		return true
	}
}

func jsonJSString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case bool:
		if v {
			return "true"
		}
		return "false"
	case *jsonjs.Object:
		return "[object Object]"
	case []any:
		parts := make([]string, len(v))
		for i := range v {
			parts[i] = jsonJSString(v[i])
		}
		return strings.Join(parts, ",")
	default:
		return jsonjs.Stringify(value)
	}
}

func numberAsPID(value any) (int, bool) {
	var number float64
	switch v := value.(type) {
	case json.Number:
		parsed, err := strconv.ParseFloat(string(v), 64)
		if err != nil {
			return 0, false
		}
		number = parsed
	case float64:
		number = v
	default:
		return 0, false
	}
	pid := int(number)
	return pid, number == float64(pid)
}
