// Package transcript counts the local session files of coding agents: the
// lines of a Claude Code session transcript and the message records of a pi
// session that carry a peer marker, the message's exact closing line, or a
// compact boundary. Every counter reads one local file and runs no Herdr
// process; the functions that resolve a session's file (they run an agent
// get) stay in the herdr package.
package transcript

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"strings"

	"github.com/djalmajr/soho/internal/jsonjs"
)

// claudeCompactBoundaryMarker is the JSON Claude Code appends to the session
// transcript when a /compact finishes; the screen shows no such confirmation.
const claudeCompactBoundaryMarker = `"subtype":"compact_boundary"`

var claudeCompactBoundaryMarkerBytes = []byte(claudeCompactBoundaryMarker)

// claudeTranscriptUserType marks the transcript records that hold a message
// taken from Claude Code's input box; the delivered peer message lands there
// with its #id marker, and later records only cite it.
const claudeTranscriptUserType = `"type":"user"`

var claudeTranscriptUserTypeBytes = []byte(claudeTranscriptUserType)

// CountClaudeUserMarkerLines counts the transcript's user records that
// contain the given marker. The raw user-type and marker substrings are a
// prefilter; a candidate counts only when its own root type is user, since
// "type":"user" may sit in a nested object of an assistant or tool record.
// Like CountClaudeCompactBoundaries it streams the file line by line and
// returns only the number: it does not retain, log, or return the content
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
		line := scanner.Bytes()
		if !bytes.Contains(line, claudeTranscriptUserTypeBytes) || !bytes.Contains(line, markerBytes) {
			continue
		}
		if str(get(parsed(string(line)), "type")) == "user" {
			n++
		}
	}
	if scanner.Err() != nil {
		return 0, false
	}
	return n, true
}

// peerClosingLineInText reports whether one of the text's lines is the
// message's exact closing line: after trimming the line is the closing line
// itself, or the same line under a leading history "›". A quoted line
// ("> " plus the marker), the marker glued to other text, a previous-format
// line and a longer id are payload, never the receipt — the same rule the
// screen and history receipt checks apply.
func peerClosingLineInText(text, closing string) bool {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == closing || line == "›"+closing {
			return true
		}
	}
	return false
}

// peerClosingUserContent reports, as 1 or 0, whether a user record's content
// carries the message's exact closing line as one of its own lines
// (peerClosingLineInText), in the content string or in one of its text
// blocks. The opening line alone — and a quoted, previous-format, or glued
// closing line — is not a receipt and leaves the count unchanged.
func peerClosingUserContent(content any, closing string) int {
	switch c := content.(type) {
	case string:
		if peerClosingLineInText(c, closing) {
			return 1
		}
	case []any:
		for _, item := range c {
			obj, isObj := item.(*jsonjs.Object)
			if !isObj {
				continue
			}
			if t, ok := obj.Get("text"); ok {
				if s, isStr := t.(string); isStr && peerClosingLineInText(s, closing) {
					return 1
				}
			}
			if cc, ok := obj.Get("content"); ok {
				if s, isStr := cc.(string); isStr && peerClosingLineInText(s, closing) {
					return 1
				}
			}
		}
	}
	return 0
}

// CountClaudeUserClosingLines counts the transcript's user records that
// carry the message's exact closing line as one of the record's own content
// lines (peerClosingUserContent). The opening line alone is not a receipt
// and is not counted. Like CountClaudeUserMarkerLines it streams the file
// line by line and returns only the number: it does not retain, log, or
// return the content of any line, matching or not. ok is false when the file
// cannot be read.
func CountClaudeUserClosingLines(path, closing string) (int, bool) {
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
		line := scanner.Bytes()
		if !bytes.Contains(line, claudeTranscriptUserTypeBytes) {
			continue
		}
		// The raw substring is only a prefilter: "type":"user" may sit in
		// a nested object of an assistant or tool record, so the record
		// counts only when its own root type is user.
		entry := parsed(string(line))
		if str(get(entry, "type")) != "user" {
			continue
		}
		if content := get(get(entry, "message"), "content"); content != nil {
			n += peerClosingUserContent(content, closing)
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
// or returned. A cancelled per-call context stops the count with ok false,
// like an unreadable file. ok is false when the file cannot be read.
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

// parsed decodes one session line as JSON, or nil when it does not decode.
func parsed(s string) any { v, _ := jsonjs.Parse([]byte(s)); return v }

// get walks a decoded value by key path, or nil when any step is missing or
// not an object.
func get(v any, keys ...string) any {
	for _, k := range keys {
		o, ok := v.(*jsonjs.Object)
		if !ok {
			return nil
		}
		v, _ = o.Get(k)
	}
	return v
}

// str renders a decoded JSON value as its string form, the way the counters
// read it: missing and false render as "".
func str(v any) string {
	if v == false {
		return ""
	}
	return jsString(v)
}

// jsString renders a decoded JSON value the way the counters display it.
func jsString(v any) string {
	switch value := v.(type) {
	case nil:
		return ""
	case string:
		return value
	case bool:
		if value {
			return "true"
		}
		return "false"
	case *jsonjs.Object:
		return "[object Object]"
	case []any:
		parts := make([]string, len(value))
		for i, item := range value {
			parts[i] = jsString(item)
		}
		return strings.Join(parts, ",")
	default:
		return jsonjs.Stringify(v)
	}
}
