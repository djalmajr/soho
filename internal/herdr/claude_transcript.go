package herdr

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/djalmajr/soho/internal/platform"
)

// claudeCompactBoundaryMarker is the JSON Claude Code appends to the session
// transcript when a /compact finishes; the screen shows no such confirmation.
const claudeCompactBoundaryMarker = `"subtype":"compact_boundary"`

var claudeCompactBoundaryMarkerBytes = []byte(claudeCompactBoundaryMarker)

// ClaudeTranscriptPath returns the path of the agent's Claude Code session
// transcript (<config>/projects/<encoded cwd>/<session>.jsonl), or "" when any
// data is missing: the agent get call fails, the agent has no agent_session,
// the session kind is not "id", the agent has no cwd, the config root cannot
// be resolved (CLAUDE_CONFIG_DIR, else HOME/USERPROFILE plus .claude), or
// the transcript file does not exist.
func ClaudeTranscriptPath(agent string, env platform.Env, callCtx ...context.Context) string {
	r := run([]string{"agent", "get", agent}, env, callCtx...)
	if !ok(r) {
		return ""
	}
	agentObj := get(parsed(r.Stdout), "result", "agent")
	session := get(agentObj, "agent_session")
	if str(get(session, "kind")) != "id" {
		return ""
	}
	value := str(get(session, "value"))
	if value == "" {
		return ""
	}
	cwd := str(get(agentObj, "cwd"))
	if cwd == "" {
		return ""
	}
	root := env.Get("CLAUDE_CONFIG_DIR")
	if root == "" {
		home := env.Get("HOME")
		if home == "" {
			home = env.Get("USERPROFILE")
		}
		if home == "" {
			return ""
		}
		root = filepath.Join(home, ".claude")
	}
	path := filepath.Join(root, "projects", ClaudeProjectDir(cwd), value+".jsonl")
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	return path
}

// ClaudeProjectDir encodes the agent cwd the way Claude Code names its
// projects directory: every character that is not an ASCII letter or digit
// becomes one "-" (each "." or "_" in the cwd encodes as "-", as does the
// "/" that separates the parts).
func ClaudeProjectDir(cwd string) string {
	var out strings.Builder
	for _, r := range cwd {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			out.WriteRune(r)
		default:
			out.WriteByte('-')
		}
	}
	return out.String()
}

// claudeTranscriptUserType marks the transcript records that hold a message
// taken from Claude Code's input box; the delivered peer message lands there
// with its #id marker, and later records only cite it.
const claudeTranscriptUserType = `"type":"user"`

var claudeTranscriptUserTypeBytes = []byte(claudeTranscriptUserType)

// CountClaudeUserMarkerLines counts the transcript lines that contain the
// user record marker and the given peer marker (#id). Like
// CountClaudeCompactBoundaries it streams the file line by line and returns
// only the number: it does not read into, retain, log, or return the content
// of any line, matching or not. ok is false when the file cannot be read.
func CountClaudeUserMarkerLines(path, marker string) (int, bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	// Transcript lines hold whole messages and run far past the scanner's
	// 64 KiB default token, so the buffer may grow before a line counts.
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	markerBytes := []byte(marker)
	n := 0
	for scanner.Scan() {
		if bytes.Contains(scanner.Bytes(), claudeTranscriptUserTypeBytes) && bytes.Contains(scanner.Bytes(), markerBytes) {
			n++
		}
	}
	if scanner.Err() != nil {
		return 0, false
	}
	return n, true
}

// CountClaudeCompactBoundaries counts the transcript lines that contain the
// compact boundary marker. It streams the file line by line and returns only
// the number: the content of no line, matching or not, is retained, logged,
// or returned. ok is false when the file cannot be read.
func CountClaudeCompactBoundaries(path string, callCtx ...context.Context) (int, bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	// Transcript lines hold whole messages and run far past the scanner's
	// 64 KiB default token, so the buffer may grow before a line counts.
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	n := 0
	for scanner.Scan() {
		if len(callCtx) > 0 && callCtx[0] != nil && callCtx[0].Err() != nil {
			return 0, false
		}
		if bytes.Contains(scanner.Bytes(), claudeCompactBoundaryMarkerBytes) {
			n++
		}
	}
	if scanner.Err() != nil {
		return 0, false
	}
	return n, true
}
