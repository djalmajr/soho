package transcript

import (
	"bufio"
	"bytes"
	"os"

	"github.com/djalmajr/soho/internal/jsonjs"
)

// CountPiUserMarkerLines counts the session lines that hold the user
// message record for the given peer marker (#id). A line counts only when
// it decodes as a "type":"message" entry whose message role is "user" and
// whose message content holds the marker: the role is read from the
// message itself, never located as a substring, so an assistant reply or a
// tool result that carries a nested "role":"user" in its own data (a tool
// call's arguments quoting the record) does not count. Like
// CountClaudeUserMarkerLines it streams the file line by line and returns
// only the number: it does not read into, retain, log, or return the
// content of any line, matching or not. The text pi writes inside the
// record is JSON-escaped, so a marker with a backslash or a quote must be
// passed escaped the same way (see transcriptPathMarker in the cli
// package); the content check re-encodes the message's content the way
// JSON writes it, so the escaped marker matches whether it sits in a text
// block, in a tool call's arguments, or in a tool result's nested content.
// A line that does not hold the marker is never decoded, and a line that
// does not decode does not count. ok is false when the file cannot be read.
func CountPiUserMarkerLines(path, marker string) (int, bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	// Session lines hold whole messages and run far past the scanner's
	// 64 KiB default token, so the buffer may grow before a line counts.
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	markerBytes := []byte(marker)
	n := 0
	for scanner.Scan() {
		line := scanner.Bytes()
		if !bytes.Contains(line, markerBytes) {
			continue
		}
		entry := parsed(string(line))
		if str(get(entry, "type")) != "message" {
			continue
		}
		message := get(entry, "message")
		if str(get(message, "role")) != "user" {
			continue
		}
		content := get(message, "content")
		if content != nil && bytes.Contains([]byte(jsonjs.Stringify(content)), markerBytes) {
			n++
		}
	}
	if scanner.Err() != nil {
		return 0, false
	}
	return n, true
}

// CountPiUserClosingLines counts the session's user records that carry the
// message's exact closing line as one of the record's own content lines
// (peerClosingUserContent, on the decoded content). A line counts only when
// it decodes as a "type":"message" entry whose message role is "user", like
// CountPiUserMarkerLines: the role is read from the message itself, never
// located as a substring. The opening line alone — and a quoted,
// previous-format, or glued closing line — is not a receipt and is not
// counted. ok is false when the file cannot be read.
func CountPiUserClosingLines(path, closing string) (int, bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	// Session lines hold whole messages and run far past the scanner's
	// 64 KiB default token, so the buffer may grow before a line counts.
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	closingBytes := []byte(closing)
	n := 0
	for scanner.Scan() {
		line := scanner.Bytes()
		if !bytes.Contains(line, closingBytes) {
			continue
		}
		entry := parsed(string(line))
		if str(get(entry, "type")) != "message" {
			continue
		}
		message := get(entry, "message")
		if str(get(message, "role")) != "user" {
			continue
		}
		if content := get(message, "content"); content != nil {
			n += peerClosingUserContent(content, closing)
		}
	}
	if scanner.Err() != nil {
		return 0, false
	}
	return n, true
}
