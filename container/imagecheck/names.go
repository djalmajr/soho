// names.go holds the pure name, link-target and metadata helpers of the
// imagecheck scanner: deny-token coverage of entry names, symlink and
// hard-link targets, tar owner names and PAX records, whiteout
// recognition, and redaction of deny literals and built-in rule tokens
// out of printed output.
// The scanner calls these; they never do I/O.

package main

import (
	"archive/tar"
	"bytes"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
)

// lowerASCIIString maps A-Z to a-z and leaves every other byte
// unchanged, so deny-token and rule comparisons stay strictly
// ASCII case-insensitive.
func lowerASCIIString(s string) string {
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b[i] = c
	}
	return string(b)
}

// normalizeArchiveName turns a raw tar member name into the path an
// extractor writes: strip every leading "/" and "./", path.Clean, then
// drop leading ".." segments ("../../root/x" -> "root/x", ".." -> "/").
// The empty result is "/".
func normalizeArchiveName(raw string) string {
	s := raw
	for {
		if strings.HasPrefix(s, "/") {
			s = s[1:]
		} else if strings.HasPrefix(s, "./") {
			s = s[2:]
		} else {
			break
		}
	}
	c := path.Clean(s)
	if c == "" || c == "." || c == "/" {
		return "/"
	}
	comps := strings.Split(c, "/")
	i := 0
	for i < len(comps) && comps[i] == ".." {
		i++
	}
	if i == len(comps) {
		return "/"
	}
	if i > 0 {
		return strings.Join(comps[i:], "/")
	}
	return c
}

// linkTargetName returns the normalized archive path a link points at:
// for tar.TypeSymlink an absolute Linkname is normalizeArchiveName(Linkname),
// a relative one is normalizeArchiveName(path.Join(path.Dir(normName), Linkname));
// for tar.TypeLink (hard link) it is normalizeArchiveName(Linkname);
// for every other type, or an empty Linkname, it is "".
func linkTargetName(normName string, h *tar.Header) string {
	if h == nil || h.Linkname == "" {
		return ""
	}
	switch h.Typeflag {
	case tar.TypeSymlink:
		if strings.HasPrefix(h.Linkname, "/") {
			return normalizeArchiveName(h.Linkname)
		}
		return normalizeArchiveName(path.Join(path.Dir(normName), h.Linkname))
	case tar.TypeLink:
		return normalizeArchiveName(h.Linkname)
	}
	return ""
}

// metaField is one metadata string of a tar entry.
type metaField struct {
	Field string // "name", "link", "uname", "gname" or "pax"
	Value string
}

// headerMetaFields lists, in this order: {"name", h.Name} (raw, always),
// {"link", h.Linkname} when non-empty, {"uname", h.Uname} when non-empty,
// {"gname", h.Gname} when non-empty, then for every PAX record sorted by
// key: {"pax", key} and {"pax", value}.
func headerMetaFields(h *tar.Header) []metaField {
	if h == nil {
		return nil
	}
	out := []metaField{{Field: "name", Value: h.Name}}
	if h.Linkname != "" {
		out = append(out, metaField{Field: "link", Value: h.Linkname})
	}
	if h.Uname != "" {
		out = append(out, metaField{Field: "uname", Value: h.Uname})
	}
	if h.Gname != "" {
		out = append(out, metaField{Field: "gname", Value: h.Gname})
	}
	keys := make([]string, 0, len(h.PAXRecords))
	for k := range h.PAXRecords {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, metaField{Field: "pax", Value: k})
		out = append(out, metaField{Field: "pax", Value: h.PAXRecords[k]})
	}
	return out
}

// denyHitLines returns the line numbers of the tokens that occur in s,
// ASCII case-insensitively (both sides lower-cased with lowerASCIIString),
// unique, in token order.
func denyHitLines(s string, toks []denyToken) []int {
	ls := lowerASCIIString(s)
	var out []int
	seen := make(map[int]bool)
	for _, t := range toks {
		needle := lowerASCIIString(string(t.needle))
		if needle == "" || seen[t.line] {
			continue
		}
		if strings.Contains(ls, needle) {
			seen[t.line] = true
			out = append(out, t.line)
		}
	}
	return out
}

// isWhiteoutMarker reports a genuine OCI/AUFS whiteout: a regular file
// (tar.TypeReg or the legacy '\x00' type, the same constant) of size 0
// whose base name of normName starts with ".wh.". A non-empty file, a
// directory, a symlink or a link with a ".wh." name is not a whiteout
// marker.
func isWhiteoutMarker(h *tar.Header, normName string) bool {
	if h == nil || h.Typeflag != tar.TypeReg || h.Size != 0 {
		return false
	}
	return strings.HasPrefix(path.Base(normName), ".wh.")
}

// contentRuleHits returns the names of the rules in rules whose pattern
// matches anywhere in s (the aws-access-key match equal to awsDocsExample
// does not count), unique, in rules order. Used on link targets and PAX
// values, which are not file content.
func contentRuleHits(s string, rules []scanRule) []string {
	var out []string
	seen := make(map[string]bool)
	for _, r := range rules {
		if seen[r.name] {
			continue
		}
		for _, m := range r.pattern.FindAllString(s, -1) {
			if r.name == "aws-access-key" && m == awsDocsExample {
				continue
			}
			seen[r.name] = true
			out = append(out, r.name)
			break
		}
	}
	return out
}

// contextLinkName returns the normalized slash path a context symlink at
// rel points at: an absolute target -> normalizeArchiveName(target);
// a relative one -> normalizeArchiveName(path.Join(path.Dir(rel),
// target)). target is a slash-separated link target, as passed by the
// caller from slashLinkTarget.
func contextLinkName(rel, target string) string {
	if target == "" {
		return ""
	}
	if strings.HasPrefix(target, "/") {
		return normalizeArchiveName(target)
	}
	return normalizeArchiveName(path.Join(path.Dir(rel), target))
}

// slashLinkTarget converts a symlink target read with os.Readlink on a
// separator-based filesystem to slash form: when sep is not "/" every
// sep byte is replaced with "/" (on Windows Readlink returns targets
// with backslashes); otherwise target is returned unchanged, because on
// Unix a backslash is a legal name byte and must stay.
func slashLinkTarget(target string, sep byte) string {
	if sep == '/' {
		return target
	}
	return strings.ReplaceAll(target, string(sep), "/")
}

// redactDenyTokens replaces every ASCII case-insensitive occurrence of
// every token in s with "[deny-token:<line>]", longest tokens first (ties:
// lower line first), so the literal never survives in output or error
// text. The placeholder itself, or a placeholder joined to the text next
// to it, can spell a token (a token such as "deny", "token" or
// "deny-token:1"), so the result is checked again: while any token still
// occurs, every occurrence is replaced by a single "?". Tokens are at
// least 4 bytes long, so each such pass shortens the text and the loop
// ends with no token left. A string with no occurrence is returned
// unchanged.
func redactDenyTokens(s string, toks []denyToken) string {
	items := make([]redTok, 0, len(toks))
	for _, t := range toks {
		n := lowerASCIIString(string(t.needle))
		if n == "" {
			continue
		}
		items = append(items, redTok{line: t.line, needle: n})
	}
	if len(items) == 0 {
		return s
	}
	sort.Slice(items, func(i, j int) bool {
		if len(items[i].needle) != len(items[j].needle) {
			return len(items[i].needle) > len(items[j].needle)
		}
		return items[i].line < items[j].line
	})
	out, changed := redactPass(s, items, func(line int) string {
		return "[deny-token:" + strconv.Itoa(line) + "]"
	})
	if !changed {
		return s
	}
	for {
		next, again := redactPass(out, items, func(int) string { return "?" })
		if !again {
			return out
		}
		out = next
	}
}

// redTok is one lower-cased deny needle with its deny-file line.
type redTok struct {
	line   int
	needle string
}

// redactPass makes one left-to-right pass over s and replaces each
// occurrence of a needle (the first of items that matches at a
// position wins) by placeholder(line). It reports whether anything was
// replaced.
func redactPass(s string, items []redTok, placeholder func(line int) string) (string, bool) {
	ls := lowerASCIIString(s)
	var b strings.Builder
	b.Grow(len(s))
	changed := false
	i := 0
	for i < len(s) {
		matched := false
		for _, it := range items {
			if len(ls)-i >= len(it.needle) && ls[i:i+len(it.needle)] == it.needle {
				changed = true
				b.WriteString(placeholder(it.line))
				i += len(it.needle)
				matched = true
				break
			}
		}
		if !matched {
			b.WriteByte(s[i])
			i++
		}
	}
	if !changed {
		return s, false
	}
	return b.String(), true
}

// redactedDisplay is the printed path of an entry whose name holds a deny
// token: prefix + "entry#" + strconv.Itoa(ordinal), for example
// "layer3:entry#17" or "entry#4" (context, empty prefix).
func redactedDisplay(prefix string, ordinal int) string {
	return prefix + "entry#" + strconv.Itoa(ordinal)
}

// maskRuleTokens replaces every match of every built-in content rule in s
// with "[deny-token:<rule>]" (the aws-access-key match equal to
// awsDocsExample does not count), so a token-shaped string held by a name,
// a path or an error message never survives in output. The marker itself,
// or a marker joined to the text next to it, can spell a token, so the
// result is checked again: while any rule still occurs, every occurrence
// is replaced by a single "?". Every match is longer than one byte, so
// each such pass shortens the text and the loop ends with no rule left. A
// line with no occurrence is returned byte for byte unchanged.
func maskRuleTokens(s string) string {
	out := s
	changed := false
	for _, r := range scanRules {
		out = r.pattern.ReplaceAllStringFunc(out, func(m string) string {
			if r.name == "aws-access-key" && m == awsDocsExample {
				return m
			}
			changed = true
			return "[deny-token:" + r.name + "]"
		})
	}
	if !changed {
		return s
	}
	for {
		again := false
		for _, r := range scanRules {
			out = r.pattern.ReplaceAllStringFunc(out, func(m string) string {
				if r.name == "aws-access-key" && m == awsDocsExample {
					return m
				}
				again = true
				return "?"
			})
		}
		if !again {
			return out
		}
	}
}

// ruleMaskWriter wraps an output writer so that every message passes
// through maskRuleTokens before it reaches the destination: a
// token-shaped string held by a name, a path or an error message never
// reaches stdout or stderr, with or without a deny file. The masked
// unit is everything buffered up to the last line break: fmt.Fprintf
// delivers a whole message in a single Write, so a message holding
// several lines (a private-key block in a name) reaches the rules
// together and a multi-line match is not cut at its internal breaks,
// and a token split across several small writes is still masked. The
// rest, without a line break, stays buffered; Flush writes the final
// partial unit, so output without a trailing newline is not lost. A
// multi-line match that a caller splits across writes at an internal
// break, or that has no line break after it in its Write, is not masked
// whole; every message of this package is one Write ending in a line
// break.
type ruleMaskWriter struct {
	w       io.Writer
	pending []byte
}

// Write appends b to the buffer and masks and forwards every unit it
// forms, where a unit runs from the start of the buffer to its last
// line break.
func (r *ruleMaskWriter) Write(b []byte) (int, error) {
	r.pending = append(r.pending, b...)
	for {
		i := bytes.LastIndexByte(r.pending, '\n')
		if i < 0 {
			break
		}
		unit := r.pending[:i+1]
		if _, err := r.w.Write([]byte(maskRuleTokens(string(unit)))); err != nil {
			return len(b), err
		}
		r.pending = r.pending[i+1:]
	}
	return len(b), nil
}

// Flush masks and writes the partial unit left in the buffer, if any.
func (r *ruleMaskWriter) Flush() error {
	if len(r.pending) == 0 {
		return nil
	}
	unit := string(r.pending)
	r.pending = r.pending[:0]
	_, err := r.w.Write([]byte(maskRuleTokens(unit)))
	return err
}
