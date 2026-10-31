package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/djalmajr/soho/internal/backend"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/transport"
)

const rosterHeader = "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\n"

var currentFrictionCommand string

func SetFrictionCommand(command string) { currentFrictionCommand = command }

func NowStamp(t time.Time) string { return t.Format("20060102T150405") }
func NowISO(t time.Time) string   { return t.Format("2006-01-02T15:04:05") }

// FrictionISO renders a friction.log timestamp in UTC with the Z suffix. The
// legacy zone-less lines keep meaning local time on read, so a new line must
// not depend on the host's zone for a --since comparison to be unambiguous.
func FrictionISO(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05Z") }

func FrictionSafe(value string) string {
	var out strings.Builder
	separator := false
	for _, r := range value {
		if r == '\r' || r == '\n' || r == '\t' {
			if !separator {
				out.WriteByte(' ')
			}
			separator = true
			continue
		}
		separator = false
		out.WriteRune(r)
	}
	return out.String()
}

func frictionLog(file, fallbackCommand, level, message string) {
	if file == "" {
		return
	}
	command := currentFrictionCommand
	if command == "" {
		command = fallbackCommand
	}
	if command == "" {
		command = "?"
	}
	line := fmt.Sprintf("%s\t%s\t%s\t%s\n", FrictionISO(platform.Now()), level, FrictionSafe(command), FrictionSafe(message))
	f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o666)
	if err != nil {
		return
	}
	_, _ = f.WriteString(line)
	_ = f.Close()
}

func Warn(message, logFile string, fallbackCommand ...string) {
	_, _ = fmt.Fprintf(platform.Stderr, "soho: warning: %s\n", message)
	legacyCommand := ""
	if len(fallbackCommand) > 0 {
		legacyCommand = fallbackCommand[0]
	}
	frictionLog(logFile, legacyCommand, "warning", message)
}

func DieFriction(message string, code int, _, _ string) {
	platform.DieFriction(message, code)
}

func RecordFrictionError(message string, code int, logFile string, fallbackCommand ...string) {
	legacyCommand := ""
	if len(fallbackCommand) > 0 {
		legacyCommand = fallbackCommand[0]
	}
	frictionLog(logFile, legacyCommand, fmt.Sprintf("error(exit %d)", code), message)
}

func WorkspaceID(ctx *Config, env platform.Env, cwd string) string {
	svc := backend.New(env)
	if id := svc.Runtime.Caller().WorkspaceID; id != "" {
		return id
	}
	if env.Get("SOHO_TRANSPORT") == "hc" {
		// hc has no pane current: the workspace is the session, and when it
		// cannot be resolved the process dies here with the hc message,
		// never a Herdr error (DJA-604 stage 2b).
		if svc.Runtime.Caller().Inside {
			platform.Die("transport=hc: the caller terminal's soho session could not be read (hc get failed for HC_TERMINAL_ID); check that hc is reachable", 2)
		}
		platform.Die("transport=hc: not inside an hc terminal (HC_TERMINAL_ID is not set); run soho from a terminal started with hc", 2)
	}
	rep := svc.Legacy.PaneCurrentRaw(nil, transport.Call{Timeout: 30 * time.Second})
	if rep.Process.NotFound {
		platform.Die("herdr CLI not found in PATH", 2)
	}
	if rep.Process.Status == nil || *rep.Process.Status != 0 {
		if rep.Process.Stdout != "" {
			_, _ = platform.Stdout.Write([]byte(rep.Process.Stdout))
		}
		if rep.Process.Stderr != "" {
			_, _ = platform.Stderr.Write([]byte(rep.Process.Stderr))
		}
		code := 1
		if rep.Process.Status != nil {
			code = *rep.Process.Status
		}
		platform.Die("", code)
	}
	v, err := jsonjs.Parse([]byte(rep.Value))
	if err != nil {
		platform.Die("", 2)
	}
	root, ok := v.(*jsonjs.Object)
	if !ok {
		platform.Die("herdr pane current returned a non-object result", 2)
	}
	result, _ := root.Get("result")
	resultObject, ok := result.(*jsonjs.Object)
	if !ok {
		platform.Die("herdr pane current returned a non-object result", 2)
	}
	pane, _ := resultObject.Get("pane")
	paneObject, ok := pane.(*jsonjs.Object)
	if !ok {
		platform.Die("herdr pane current returned a non-object pane", 2)
	}
	id, ok := paneObject.Get("workspace_id")
	if !ok || id == nil {
		return "null"
	}
	if s, ok := id.(string); ok {
		return s
	}
	switch id.(type) {
	case *jsonjs.Object, []any:
		platform.Die("herdr pane current returned an unsupported workspace_id type", 2)
	}
	return jsString(id)
}

func jsString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case bool:
		if v {
			return "true"
		}
		return "false"
	case []any:
		parts := make([]string, len(v))
		for i, item := range v {
			if item != nil {
				parts[i] = jsString(item)
			}
		}
		return strings.Join(parts, ",")
	case *jsonjs.Object:
		return "[object Object]"
	default:
		return jsonjs.Stringify(value)
	}
}

func StateDirPath(ctx *Config, env platform.Env, cwd string) string {
	return filepath.Join(StateRoot(ctx, env, cwd), WorkspaceID(ctx, env, cwd))
}

// StateDir decides with StateInSkill and FormerStateWaiting — both use only
// StateRootPath — before touching StateDirPath/StateRoot/WorkspaceID, so a
// refusal leaves no side effect (no .gitignore append, no pane current, no
// mkdir). The paths named in the messages are the state root and the
// former/target dirs, not the full state dir.
func StateDir(ctx *Config, env platform.Env, cwd string) string {
	if !Nowrite(env) {
		if skill := StateInSkill(ctx, env, cwd); skill != "" {
			platform.Die(fmt.Sprintf("the state dir '%s' would be inside the soho skill ('%s'); run soho from the project's directory (nothing was written)", StateRootPath(ctx, env, cwd), skill), 2)
		}
		// The former state dir waiting for the setup move is refused before
		// any side effect: creating .soho/<ws> would block the migration's
		// empty-target rule, and the .gitignore append would point at a dir
		// setup has not created yet.
		if former, target, waiting := FormerStateWaiting(ctx, env, cwd); waiting {
			platform.Die(fmt.Sprintf("the former state dir '%s' is waiting for 'soho setup': run 'soho setup' first; it moves it to '%s' (nothing was written)", former, target), 2)
		}
	}
	dir := StateDirPath(ctx, env, cwd)
	if Nowrite(env) {
		return dir
	}
	for _, name := range []string{"briefs", "reports", "wait"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o777); err != nil {
			panic(err)
		}
	}
	roster := filepath.Join(dir, "agents.tsv")
	if _, err := os.Stat(roster); os.IsNotExist(err) {
		if err := os.WriteFile(roster, []byte(rosterHeader), 0o666); err != nil {
			panic(err)
		}
	}
	return dir
}

func tsvLines(dir string) []string {
	raw, err := platform.ReadTextFile(filepath.Join(dir, "agents.tsv"))
	if err != nil {
		return []string{}
	}
	lines := strings.Split(raw, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func RosterRows(dir string) []string {
	out := []string{}
	for _, line := range tsvLines(dir) {
		if !strings.HasPrefix(line, "#") {
			out = append(out, line)
		}
	}
	return out
}

func RosterLine(dir, name string) string {
	found := ""
	for _, line := range RosterRows(dir) {
		fields := strings.Split(line, "\t")
		if fields[0] == name {
			found = line
		}
	}
	return found
}

func WithRosterLock(dir string, fn func()) {
	lock := filepath.Join(dir, "agents.lock")
	tries := 0
	for {
		err := os.Mkdir(lock, 0o700)
		if err == nil {
			break
		}
		removed := false
		if info, statErr := os.Lstat(lock); statErr == nil && platform.Now().Sub(info.ModTime()) > time.Minute && info.IsDir() {
			if os.Remove(lock) == nil {
				removed = true
			}
		}
		if removed {
			continue
		}
		tries++
		if tries >= 200 {
			platform.DieFriction(fmt.Sprintf("roster lock %s held for too long; remove it if no soho command is running", lock), 4)
		}
		time.Sleep(50 * time.Millisecond)
	}
	defer func() { _ = os.Remove(lock) }()
	fn()
}

// RosterE2EPort reads the optional trailing column without changing old rows.
func RosterE2EPort(fields []string) string {
	if len(fields) <= 16 {
		return ""
	}
	port, err := strconv.Atoi(fields[16])
	if err != nil || port < 1024 || port > 65535 {
		return ""
	}
	return strconv.Itoa(port)
}

func RosterAppend(dir string, fields []string) {
	WithRosterLock(dir, func() {
		f, err := os.OpenFile(filepath.Join(dir, "agents.tsv"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o666)
		if err != nil {
			panic(err)
		}
		// Append a schema comment rather than rewrite any existing row/header.
		// Off-mode keeps the original bytes and column count exactly.
		if RosterE2EPort(fields) != "" {
			hasHeader, hasPortHeader := false, false
			for _, line := range tsvLines(dir) {
				if strings.HasPrefix(line, "#") {
					hasHeader = true
					if strings.HasSuffix(line, "\te2e_port") {
						hasPortHeader = true
					}
				}
			}
			if hasHeader && !hasPortHeader {
				_, err = f.WriteString("# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\tburst\targs\teffort\tmcp\te2e_port\n")
			}
		}
		if err == nil {
			_, err = f.WriteString(strings.Join(fields, "\t") + "\n")
		}
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			panic(err)
		}
	})
}

func atomicRoster(file string, lines []string) {
	content := ""
	if len(lines) != 0 {
		content = strings.Join(lines, "\n") + "\n"
	}
	if err := platform.AtomicWrite(file, content); err != nil {
		panic(err)
	}
}

func RosterRemove(dir, name string) {
	WithRosterLock(dir, func() {
		lines := tsvLines(dir)
		out := make([]string, 0, len(lines))
		for _, line := range lines {
			if strings.Split(line, "\t")[0] != name {
				out = append(out, line)
			}
		}
		atomicRoster(filepath.Join(dir, "agents.tsv"), out)
	})
}

func RosterSetRole(dir, name, role string) {
	WithRosterLock(dir, func() {
		lines := tsvLines(dir)
		for i, line := range lines {
			f := strings.Split(line, "\t")
			if f[0] != name {
				continue
			}
			previous := ""
			if len(f) > 3 {
				previous = f[3]
			}
			history := ""
			if len(f) >= 11 {
				history = f[10]
			}
			if history == "" {
				history = previous
			} else if !contains(strings.Split(history, ","), previous) {
				history += "," + previous
			}
			if previous != role {
				if history == "" {
					history = role
				} else {
					history += "," + role
				}
			}
			for len(f) < 11 {
				f = append(f, "")
			}
			f[3], f[10] = role, history
			lines[i] = strings.Join(f, "\t")
		}
		atomicRoster(filepath.Join(dir, "agents.tsv"), lines)
	})
}

func RosterReplacePane(dir, oldPane, newPane string) {
	if oldPane == newPane || newPane == "" {
		return
	}
	WithRosterLock(dir, func() {
		lines := tsvLines(dir)
		for i, line := range lines {
			f := strings.Split(line, "\t")
			if len(f) > 1 && f[1] == oldPane {
				f[1] = newPane
			}
			lines[i] = strings.Join(f, "\t")
		}
		atomicRoster(filepath.Join(dir, "agents.tsv"), lines)
	})
}

func LastReportPath(dir, agent string) string { return filepath.Join(dir, "last-report-"+agent) }
func LastReport(dir, agent string) string {
	// The marker is a regular file only, opened without blocking: a FIFO
	// planted at its path reads as no marker instead of holding the reader.
	raw, err := platform.ReadRegularTextFile(LastReportPath(dir, agent))
	if err != nil {
		return ""
	}
	return strings.TrimRight(raw, "\n")
}
