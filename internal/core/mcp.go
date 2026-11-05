package core

import (
	"regexp"
	"strings"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/text"
)

// mcpNameRE is the shape of one MCP server name in a list choice (DJA-693):
// 1 to 64 letters, digits, underscores or dashes.
var mcpNameRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// McpValueError reports why an mcp value is not a choice soho accepts
// (DJA-693), or "" when it is. The accepted values are inherit, none, and a
// comma-separated list of server names: only the spaces around each name are
// trimmed, so a tab or a line break fails the name rule and can never split a
// roster row or a configuration line; each name must match mcpNameRE, none and
// inherit are the whole value only, never names inside a list, and an empty
// part is a missing name. Duplicates are accepted; they count once (McpNames).
func McpValueError(value string) string {
	if value == "inherit" || value == "none" {
		return ""
	}
	for _, part := range strings.Split(value, ",") {
		name := strings.Trim(part, " ")
		if name == "" {
			return "empty name in the list"
		}
		if name == "none" || name == "inherit" {
			return "'" + name + "' is the whole value, not a list name"
		}
		if !mcpNameRE.MatchString(name) {
			return "'" + text.SanitizeCause(name) + "' is not a server name (^[A-Za-z0-9_-]{1,64}$)"
		}
	}
	return ""
}

// McpNames is the names of a valid list value, in list order, duplicates
// counted once. inherit, none and an invalid value return nil.
func McpNames(value string) []string {
	if value == "inherit" || value == "none" || McpValueError(value) != "" {
		return nil
	}
	seen := map[string]bool{}
	var names []string
	for _, part := range strings.Split(value, ",") {
		name := strings.Trim(part, " ")
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	return names
}

// McpCanonical is the one form of a valid value that travels on: the roster
// column, the reuse comparison and the reopen flag. inherit and none stay as
// they are; a list is its names joined by a comma, so "a, b" equals "a,b". An
// invalid value comes back unchanged: the caller has refused it already.
func McpCanonical(value string) string {
	names := McpNames(value)
	if names == nil {
		return value
	}
	return strings.Join(names, ",")
}

// FmGetMcp is FmGet for the mcp key only (DJA-693): the same leading ---
// block, the same key match, the same trim — but the value's commas stay,
// because the list choice is comma-separated while FmGet's value rules
// collapse commas to spaces for the other keys. A value's surrounding
// double quotes are stripped, as in FmGet.
func FmGetMcp(file string) string {
	if strings.HasSuffix(file, ".append.md") {
		return ""
	}
	text, err := platform.ReadTextFile(file)
	if err != nil {
		return ""
	}
	lines := splitLines(text)
	if len(lines) == 0 || lines[0] != "---" {
		return ""
	}
	for _, line := range lines[1:] {
		if line == "---" {
			break
		}
		colon := strings.IndexByte(line, ':')
		if colon < 0 || trimJSWhitespace(line[:colon]) != "mcp" {
			continue
		}
		value := trimJSWhitespace(line[colon+1:])
		if len(value) >= 2 && strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`) {
			value = value[1 : len(value)-1]
		}
		return value
	}
	return ""
}
