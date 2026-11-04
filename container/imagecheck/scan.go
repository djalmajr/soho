package main

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Exit codes shared by the scan subcommands: 0 nothing found, 1
// findings present, 2 usage error, 4 I/O error.
const (
	exitOK       = 0
	exitFindings = 1
	exitUsage    = 2
	exitIO       = 4
)

// scanBlock is the streaming block size and scanOverlap the bytes of
// the previous block kept in front of the next one, so a match that
// crosses a block boundary stays visible.
const (
	scanBlock   = 1 << 20
	scanOverlap = 4 << 10
)

// maxPEMCarry is the most bytes the scanner may keep between windows
// to complete an open PEM private-key marker; an open region longer
// than this fails closed instead of being carried.
var maxPEMCarry = 16 << 20

// scanRule is one named content rule: a rule name and a byte regex.
type scanRule struct {
	name    string
	pattern *regexp.Regexp
}

// scanRules are the built-in content rules. private-key requires a
// PEM body (optional header lines plus at least 64 base64 characters)
// so that bare format strings in binaries and docs do not count.
var scanRules = []scanRule{
	{name: "private-key", pattern: regexp.MustCompile(`-----BEGIN (RSA |EC |OPENSSH |DSA |ENCRYPTED )?PRIVATE KEY-----(?:\r?\n(?:[A-Za-z-]+: [^\r\n]*\r?\n)*(?:\r?\n)?[A-Za-z0-9+/=\r\n]{64,})`)},
	{name: "anthropic-key", pattern: regexp.MustCompile(`sk-ant-[a-z]{3}[0-9]{2}-[A-Za-z0-9_-]{80,}`)},
	{name: "openai-key", pattern: regexp.MustCompile(`sk-(proj|svcacct|admin)-[A-Za-z0-9_-]{40,}`)},
	{name: "xai-key", pattern: regexp.MustCompile(`xai-[A-Za-z0-9]{60,}`)},
	{name: "github-token", pattern: regexp.MustCompile(`(gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{60,})`)},
	{name: "npm-token", pattern: regexp.MustCompile(`npm_[A-Za-z0-9]{36}`)},
	{name: "aws-access-key", pattern: regexp.MustCompile(`AKIA[0-9A-Z]{16}`)},
	{name: "slack-token", pattern: regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]{10,}`)},
	{name: "google-api-key", pattern: regexp.MustCompile(`AIza[0-9A-Za-z_-]{35}`)},
}

// hostPathRules run only over the image config bytes. The Windows form
// matches the JSON-escaped backslashes of a path such as C:\Users\x.
var hostPathRules = []scanRule{
	{name: "host-path", pattern: regexp.MustCompile(`/Users/[^/\s"]+`)},
	{name: "host-path", pattern: regexp.MustCompile(`[A-Za-z]:\\\\Users\\\\`)},
	{name: "host-path", pattern: regexp.MustCompile(`/home/[^/\s"]+`)},
}

// awsDocsExample is the AWS documentation key; it is an example, not a
// credential, so it never counts as a finding. It is assembled from
// parts so the matchable literal does not live in the source.
const awsDocsExample = "AKIA" + "IOSFODNN7EXAMPLE"

// forbiddenComponents is the exact component table of the
// forbidden-name rule; every comparison is case-insensitive.
// .soho and .herdr-soho are the state directories, the new name and the
// legacy name of the compatibility window.
var forbiddenComponents = []string{
	".git", ".soho", ".herdr-soho", ".agents", ".worktrees",
	".claude", ".codex", ".grok", ".pi", ".ssh", ".aws", ".gnupg",
	".docker", ".npmrc", ".netrc", ".env", "auth.json",
	".credentials.json", "credentials.json", "credentials",
	".git-credentials",
}

// forbiddenPrefixes and forbiddenSuffixes are the partial component
// forms of forbidden-name.
var (
	forbiddenPrefixes = []string{".env.", "id_rsa", "id_ecdsa", "id_ed25519"}
	forbiddenSuffixes = []string{".pem", ".key", ".p12", ".pfx"}
)

// imageCredentialPaths is the remainder (after root/ or home/<user>/)
// that triggers credential-path in an image layer.
var imageCredentialPaths = []string{
	".claude/.credentials.json", ".claude.json", ".codex/auth.json",
	".grok/auth.json", ".pi/agent/auth.json", ".npmrc", ".netrc",
	".git-credentials", ".aws/credentials", ".docker/config.json",
	".config/gh/hosts.yml",
}

// imageSshPrefix: anything under it in the remainder triggers
// credential-path.
const imageSshPrefix = ".ssh/"

// envSecretKeywords: an image Env name whose upper-cased form contains
// any of these triggers config-env-secret.
var envSecretKeywords = []string{
	"TOKEN", "SECRET", "PASSWORD", "PASSWD", "API_KEY", "APIKEY",
	"ACCESS_KEY", "PRIVATE_KEY", "CREDENTIAL", "AUTH",
}

// denyToken holds one --deny-file token with the line number it came
// from. The token itself never leaves this process except as a search
// needle; the rule name is deny-token:<line>.
type denyToken struct {
	line   int
	needle []byte
}

// finding is one result line of a run: a name-rule finding (offset
// -1), a deny-token finding, a metadata finding localized in a name,
// link, uname, gname or pax field (never acceptable), or a per-match
// content finding that carries the match length and the sha256 of the
// file it came from.
type finding struct {
	rule    string
	display string // printed path: relative (context), layer<N>:name (image), "config", "archive:<name>"
	key     string // match identity: relative path, entry name, "config", "archive:<name>"
	off     int64  // -1 for name rules and metadata
	length  int
	sha     [32]byte
	deny    bool
	meta    bool
	field   string // metadata field: "name", "link", "uname", "gname" or "pax"
	accept  bool
}

// scanFindings collects the per-match findings and the summary
// counters of one subcommand run.
type scanFindings struct {
	order     []finding
	seenName  map[string]bool // (rule, key) for name rules
	seenMeta  map[string]bool // (rule, display, field) for metadata
	files     int
	bytes     int64
	unmatched []string
}

// newFindings starts an empty collector.
func newFindings() *scanFindings {
	return &scanFindings{seenName: map[string]bool{}, seenMeta: map[string]bool{}}
}

// addName records one name-rule finding; the first of each
// (rule, key) wins.
func (f *scanFindings) addName(rule, key string) {
	k := rule + "\x00" + key
	if f.seenName[k] {
		return
	}
	f.seenName[k] = true
	f.order = append(f.order, finding{rule: rule, display: key, key: key, off: -1})
}

// addContent records one content match of the file with key path,
// after the file was fully read. Within one file the scanner already
// deduplicated the same (rule, offset) and kept the larger length,
// so every call is one finding. There is no cross-file deduplication:
// the same key path in different layers, or a tar layer holding two
// same-name entries, is different file content and must stay
// separate. The match length and the file sha256 ride along so an
// accept can bind to the exact reviewed bytes and the exact match.
func (f *scanFindings) addContent(rule, display, key string, off int64, length int, sha [32]byte) {
	f.order = append(f.order, finding{rule: rule, display: display, key: key, off: off, length: length, sha: sha})
}

// addDeny records the first deny-token hit of one file; the line
// carries only the token's line number and the offset, never the
// literal.
func (f *scanFindings) addDeny(rule, display, key string, off int64) {
	f.order = append(f.order, finding{rule: rule, display: display, key: key, off: off, deny: true})
}

// addMeta records one finding localized in a metadata field (name,
// link, uname, gname or pax) of the entry named by display: the first
// of each (rule, display, field) wins. Metadata findings are never
// acceptable: an accept entry has no way to name them.
func (f *scanFindings) addMeta(rule, display, field string) {
	k := rule + "\x00" + display + "\x00@" + field
	if f.seenMeta[k] {
		return
	}
	f.seenMeta[k] = true
	f.order = append(f.order, finding{rule: rule, display: display, off: -1, meta: true, field: field})
}

// print writes one line per finding (in discovery order; the matches
// of one file in ascending offset, then rule name), then the
// unmatched-accept lines in input order, then the summary line. It
// never prints the matched content.
func (f *scanFindings) print(w io.Writer) {
	nf, na := 0, 0
	for _, x := range f.order {
		if x.accept {
			na++
		} else {
			nf++
		}
		switch {
		case x.meta:
			fmt.Fprintf(w, "finding\t%s\t%s\t@%s\n", x.rule, x.display, x.field)
		case x.deny:
			fmt.Fprintf(w, "finding\t%s\t%s\t%d\n", x.rule, x.display, x.off)
		case x.off < 0:
			fmt.Fprintf(w, "%s\t%s\t%s\t-\n", kindOf(x), x.rule, x.display)
		default:
			fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\tsha256:%x\n", kindOf(x), x.rule, x.display, x.off, x.length, x.sha)
		}
	}
	for _, u := range f.unmatched {
		fmt.Fprintln(w, u)
	}
	fmt.Fprintf(w, "summary\tfiles=%d\tbytes=%d\tfindings=%d\taccepted=%d\tunmatched=%d\n",
		f.files, f.bytes, nf, na, len(f.unmatched))
}

// kindOf is the printed kind of one finding.
func kindOf(x finding) string {
	if x.accept {
		return "accepted"
	}
	return "finding"
}

// scanOpts are the parsed flags of one subcommand run.
type scanOpts struct {
	allow         []string // context only; normalized without a trailing "/"
	denyFile      string
	acceptFile    string // at most one
	acceptEntries []acceptEntry
}

// parseScanFlags splits the subcommand arguments into the positional
// args and the shared flags. Every flag may come before or after the
// positional argument. Unknown flags and malformed --accept specs are
// usage errors.
func parseScanFlags(args []string, opts *scanOpts) (pos []string, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		var v string
		switch {
		case a == "--allow":
			if i+1 >= len(args) {
				return nil, errors.New("--allow requires a value")
			}
			i++
			v = args[i]
			addAllow(opts, v)
		case strings.HasPrefix(a, "--allow="):
			addAllow(opts, a[len("--allow="):])
		case a == "--deny-file":
			if i+1 >= len(args) {
				return nil, errors.New("--deny-file requires a value")
			}
			i++
			opts.denyFile = args[i]
		case strings.HasPrefix(a, "--deny-file="):
			opts.denyFile = a[len("--deny-file="):]
		case a == "--accept-file":
			if i+1 >= len(args) {
				return nil, errors.New("--accept-file requires a value")
			}
			i++
			if opts.acceptFile != "" {
				return nil, errors.New("--accept-file may only be given once")
			}
			opts.acceptFile = args[i]
		case strings.HasPrefix(a, "--accept-file="):
			if opts.acceptFile != "" {
				return nil, errors.New("--accept-file may only be given once")
			}
			opts.acceptFile = a[len("--accept-file="):]
		case a == "--accept":
			if i+1 >= len(args) {
				return nil, errors.New("--accept requires a value")
			}
			i++
			v = args[i]
		case strings.HasPrefix(a, "--accept="):
			v = a[len("--accept="):]
		case strings.HasPrefix(a, "-") && a != "-":
			return nil, fmt.Errorf("unknown flag at argument %d", i+1)
		default:
			pos = append(pos, a)
		}
		if a == "--accept" || strings.HasPrefix(a, "--accept=") {
			e, err := parseAcceptEntry(v)
			if err != nil {
				return nil, fmt.Errorf("--accept %d: %v", len(opts.acceptEntries)+1, err)
			}
			opts.acceptEntries = append(opts.acceptEntries, e)
		}
	}
	return pos, nil
}

// addAllow normalizes one --allow value and records it; a value that
// normalizes to empty (such as "") is not a prefix and is dropped.
func addAllow(opts *scanOpts, v string) {
	if v = normalizeAllow(v); v != "" {
		opts.allow = append(opts.allow, v)
	}
}

// normalizeAllow trims surrounding whitespace and the trailing "/" so
// a prefix covers the path equal to it and everything below it.
func normalizeAllow(v string) string {
	return strings.TrimSuffix(strings.TrimSpace(v), "/")
}

// acceptableRules are the only content rules an accept entry may
// bind to; deny tokens, name rules and config-env-secret can never
// be accepted.
var acceptableRules = map[string]bool{
	"private-key":    true,
	"anthropic-key":  true,
	"openai-key":     true,
	"xai-key":        true,
	"github-token":   true,
	"npm-token":      true,
	"aws-access-key": true,
	"slack-token":    true,
	"google-api-key": true,
	"host-path":      true,
}

// acceptEntry is one parsed --accept value or --accept-file line in
// the <rule> <offset> <length> sha256:<hex> <path> form: it binds to
// the individual match of the individual file bytes, so a match can
// only be accepted against the exact reviewed bytes.
type acceptEntry struct {
	rule   string
	offset int64
	length int64
	sha    [32]byte
	path   string
}

// entrySource is one accept entry with the place it came from, used
// for the usage errors and the unmatched-accept lines: a 1-based line
// of the --accept-file or a 1-based --accept flag.
type entrySource struct {
	entry acceptEntry
	kind  int
	index int
}

const (
	entryFromFile = iota
	entryFromFlag
)

// layerPrefixRe matches the old layer-index path form (layer<N>:...).
var layerPrefixRe = regexp.MustCompile(`^layer[0-9]+:`)

// parseAcceptEntry validates one accept entry. Every message names
// only what is wrong, never the entry text.
func parseAcceptEntry(line string) (acceptEntry, error) {
	parts := splitAcceptFields(line)
	if len(parts) != 5 {
		return acceptEntry{}, fmt.Errorf("expected 5 fields, got %d: the format is <rule> <offset> <length> sha256:<file-sha256> <path>", len(parts))
	}
	rule := parts[0]
	if _, ok := acceptableRules[rule]; !ok {
		return acceptEntry{}, errors.New("rule field cannot be accepted")
	}
	off, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || off < 0 {
		return acceptEntry{}, fmt.Errorf("offset must be a decimal integer >= 0")
	}
	length, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || length < 1 {
		return acceptEntry{}, fmt.Errorf("length must be a decimal integer >= 1")
	}
	shaField := parts[3]
	if !strings.HasPrefix(shaField, "sha256:") || len(shaField) != len("sha256:")+64 {
		return acceptEntry{}, fmt.Errorf("sha256 must be the prefix sha256: followed by exactly 64 lowercase hex characters")
	}
	var sha [32]byte
	for i := 7; i < len(shaField); i++ {
		c := shaField[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return acceptEntry{}, fmt.Errorf("sha256 must be the prefix sha256: followed by exactly 64 lowercase hex characters")
		}
	}
	if _, err := hex.Decode(sha[:], []byte(shaField[7:])); err != nil {
		return acceptEntry{}, fmt.Errorf("sha256 must be the prefix sha256: followed by exactly 64 lowercase hex characters")
	}
	if parts[4] == "" {
		return acceptEntry{}, fmt.Errorf("path must not be empty")
	}
	if layerPrefixRe.MatchString(parts[4]) {
		return acceptEntry{}, fmt.Errorf("path must not use the old layer-index form")
	}
	return acceptEntry{rule: rule, offset: off, length: length, sha: sha, path: parts[4]}, nil
}

// splitAcceptFields splits one accept entry on runs of spaces and
// tabs; the path is the remainder of the line after the fourth
// separator run and may contain spaces.
func splitAcceptFields(line string) []string {
	var parts []string
	i, n := 0, len(line)
	for f := 0; f < 4 && i < n; f++ {
		for i < n && (line[i] == ' ' || line[i] == '\t') {
			i++
		}
		start := i
		for i < n && line[i] != ' ' && line[i] != '\t' {
			i++
		}
		parts = append(parts, line[start:i])
	}
	for i < n && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	parts = append(parts, line[i:])
	return parts
}

// readAcceptEntries loads a --accept-file: one accept entry per line
// in the <rule> <offset> <length> sha256:<hex> <path> form. Lines are
// trimmed at both ends; blank lines and lines starting with "#" are
// ignored. A malformed line is a usage error and the message cites
// the line number only, never the entry text. Each entry keeps the
// 1-based line number it came from.
func readAcceptEntries(p string) ([]entrySource, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var out []entrySource
	for i, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		e, err := parseAcceptEntry(line)
		if err != nil {
			return nil, fmt.Errorf("accept-file %s: line %d: %v", p, i+1, err)
		}
		out = append(out, entrySource{entry: e, kind: entryFromFile, index: i + 1})
	}
	return out, nil
}

// posLabel renders one entry origin for the usage errors.
func posLabel(kind, index int) string {
	if kind == entryFromFile {
		return "line " + strconv.Itoa(index)
	}
	return "flag " + strconv.Itoa(index)
}

// checkDuplicateEntries rejects the same five fields twice, whether
// they come from the accept file, the flags or both. The message
// cites the entry origins only, never the entry text.
func checkDuplicateEntries(entries []entrySource) error {
	seen := map[string]entrySource{}
	for _, s := range entries {
		k := s.entry.rule + "\x00" + strconv.FormatInt(s.entry.offset, 10) +
			"\x00" + strconv.FormatInt(s.entry.length, 10) +
			"\x00" + hex.EncodeToString(s.entry.sha[:]) + "\x00" + s.entry.path
		if prev, ok := seen[k]; ok {
			return fmt.Errorf("duplicate accept entry: %s and %s", posLabel(prev.kind, prev.index), posLabel(s.kind, s.index))
		}
		seen[k] = s
	}
	return nil
}

// collectEntries gathers the accept entries in input order — the
// --accept-file lines first, then the --accept flags — and rejects
// duplicates. A missing or unreadable accept file is an I/O error;
// a malformed entry or a duplicate is a usage error.
func collectEntries(opts scanOpts) ([]entrySource, error) {
	var entries []entrySource
	if opts.acceptFile != "" {
		var err error
		entries, err = readAcceptEntries(opts.acceptFile)
		if err != nil {
			return nil, err
		}
	}
	for i, e := range opts.acceptEntries {
		entries = append(entries, entrySource{entry: e, kind: entryFromFlag, index: i + 1})
	}
	return entries, checkDuplicateEntries(entries)
}

// readDenyFile loads the --deny-file tokens. Blank lines and lines
// starting with "#" are ignored; spaces at both ends are removed. A
// token shorter than 4 bytes is a usage error and the message cites
// the line number only, never the token. Read failures become static
// messages without the path: they are printed to the raw stderr
// before the redacting writer exists. Each needle is stored
// ASCII-lower-cased so the matching is ASCII case-insensitive.
func readDenyFile(p string) ([]denyToken, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, denyFileReadError(err)
	}
	var toks []denyToken
	for i, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if len(line) < 4 {
			return nil, &denyLoadError{msg: fmt.Sprintf("deny-file: line %d: token shorter than 4 bytes", i+1)}
		}
		toks = append(toks, denyToken{line: i + 1, needle: asciiLower([]byte(line))})
	}
	return toks, nil
}

// denyFileReadError maps a --deny-file read failure to a static
// message and the I/O exit class: a missing file, a permission
// denial, a path that is a directory, or any other read failure. The
// message names no path and no other user-controlled value.
func denyFileReadError(err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return &denyLoadError{msg: "deny-file: not found", io: true}
	case errors.Is(err, fs.ErrPermission):
		return &denyLoadError{msg: "deny-file: permission denied", io: true}
	default:
		return &denyLoadError{msg: "deny-file: cannot be read", io: true}
	}
}

// asciiLower lower-cases ASCII letters only: the bytes A-Z become
// a-z and every other byte stays unchanged. It is not bytes.ToLower,
// which would apply Unicode folding.
func asciiLower(b []byte) []byte {
	out := make([]byte, len(b))
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out[i] = c
	}
	return out
}

// forbiddenName reports whether any path component of rel triggers the
// forbidden-name rule. rel uses "/" separators.
func forbiddenName(rel string) bool {
	for _, c := range strings.Split(rel, "/") {
		if nameForbidden(c) {
			return true
		}
	}
	return false
}

// nameForbidden is the case-insensitive component test of
// forbidden-name.
func nameForbidden(c string) bool {
	lc := strings.ToLower(c)
	for _, x := range forbiddenComponents {
		if lc == x {
			return true
		}
	}
	for _, x := range forbiddenPrefixes {
		if strings.HasPrefix(lc, x) {
			return true
		}
	}
	for _, x := range forbiddenSuffixes {
		if strings.HasSuffix(lc, x) {
			return true
		}
	}
	return false
}

// notAllowlisted reports whether rel (a file or symlink path, "/"
// separators) is covered by no allow prefix. A prefix covers the path
// equal to it and everything below it.
func notAllowlisted(rel string, allow []string) bool {
	for _, p := range allow {
		if rel == p || strings.HasPrefix(rel, p+"/") {
			return false
		}
	}
	return true
}

// contentMatch is one match of the stream at its absolute start
// offset; deny-token hits and fail-closed PEM findings carry no
// length.
type contentMatch struct {
	rule   string
	offset int64
	length int
	deny   bool
}

// contentScanner streams r in scanBlock chunks with a tail of the
// previous chunk kept in front of the next one. Content rules are
// reported per match, at their absolute start offset, deduplicated by
// (rule, offset) with the larger length kept; the overlap makes a
// match that crosses a block boundary visible. Deny tokens are
// reported at most once per file and matched ASCII case-insensitively
// (each window against an ASCII-lower-cased copy of itself). The
// scanner also hashes the whole stream it read (every byte exactly
// once, the overlap not hashed twice).
type contentScanner struct {
	r       io.Reader
	pos     int64 // absolute offset of the start of buf
	buf     []byte
	first   bool
	rules   []scanRule
	toks    []denyToken
	done    map[string]bool // deny token names already found
	seen    map[string]int  // (rule, offset) -> index into matches
	matches []contentMatch
	denyOff []contentMatch
	failPEM []contentMatch
	failSet map[int64]bool // fail-closed PEM marker offsets
	overlap int            // default tail kept between windows
	carry   int64          // start of the tail for the next window
	hash    hash.Hash
}

// newContentScanner wraps r.
func newContentScanner(r io.Reader, rules []scanRule, toks []denyToken) *contentScanner {
	done := map[string]bool{}
	for _, t := range toks {
		done["deny-token:"+strconv.Itoa(t.line)] = false
	}
	// The tail must hold all but the last byte of the longest deny
	// token, so a literal longer than scanOverlap that crosses a block
	// boundary still lies whole inside one window.
	overlap := scanOverlap
	for _, t := range toks {
		if n := len(t.needle) - 1; n > overlap {
			overlap = n
		}
	}
	return &contentScanner{r: r, first: true, rules: rules, toks: toks, done: done, seen: map[string]int{}, failSet: map[int64]bool{}, overlap: overlap}
}

// scan runs the stream to the end and returns how many new bytes it
// read (the overlap kept between windows is not counted twice).
func (s *contentScanner) scan() (int64, error) {
	var total int64
	for {
		var chunk, nb []byte
		var err error
		if s.first {
			nb, err = readBlock(s.r)
			chunk = nb
		} else {
			// The tail starts at s.carry, the position carryFor picked
			// in the previous window.
			tail := s.buf
			if len(tail) > int(s.carry) {
				tail = tail[int(s.carry):]
			}
			nb, err = readBlock(s.r)
			if len(nb) == 0 {
				if err != nil && err != io.EOF && !errors.Is(err, io.ErrUnexpectedEOF) {
					return total, err
				}
				break // nothing beyond the overlap
			}
			if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
				return total, err
			}
			// The new window starts at the start of the overlap:
			// the previous window minus the bytes before the tail.
			s.pos += int64(len(s.buf) - len(tail))
			chunk = append(append([]byte{}, tail...), nb...)
		}
		s.first = false
		s.buf = chunk
		total += int64(len(nb))
		if s.hash != nil {
			s.hash.Write(nb)
		}
		s.scanBuf()
		if err == nil {
			s.carry = s.carryFor()
			continue
		}
		if err == io.EOF || errors.Is(err, io.ErrUnexpectedEOF) {
			return total, nil
		}
		return total, err
	}
	return total, nil
}

// carryFor picks where the next window must start: the next window
// must start no later than an open PEM marker, and deny tokens keep
// their current rule (longest token minus one). The later start wins,
// i.e. the shorter tail. An open marker whose tail would exceed
// maxPEMCarry is not carried: a fail-closed private-key finding is
// recorded at the marker instead.
func (s *contentScanner) carryFor() int64 {
	pos := int64(len(s.buf)) - int64(s.overlap)
	if pos < 0 {
		pos = 0 // a deny token longer than the window must not carry a negative tail
	}
	if pos > int64(len(s.buf)) {
		pos = int64(len(s.buf))
	}
	// Walk every open marker in ascending order, not only the
	// earliest: each marker whose open tail exceeds maxPEMCarry
	// fails closed at the marker (once per absolute offset), and
	// the first marker whose tail fits sets the carry; the later
	// markers lie inside the carried tail.
	for _, m := range pemOpenMarkers(s.buf) {
		if int64(len(s.buf)-m) > int64(maxPEMCarry) {
			abs := s.pos + int64(m)
			if !s.failSet[abs] {
				s.failSet[abs] = true
				s.failPEM = append(s.failPEM, contentMatch{rule: "private-key", offset: abs, length: len(s.buf) - m})
			}
			continue
		}
		if int64(m) < pos {
			pos = int64(m)
		}
		break
	}
	return pos
}

// scanBuf reports every match of every content rule and the first
// occurrence of every deny token inside the current window.
func (s *contentScanner) scanBuf() {
	for _, r := range s.rules {
		for _, m := range r.pattern.FindAllIndex(s.buf, -1) {
			seg := s.buf[m[0]:m[1]]
			if r.name == "aws-access-key" && string(seg) == awsDocsExample {
				continue // the documentation example is not a finding
			}
			if r.name == "host-path" && string(seg) == "/home/agent" {
				continue // the image's own home is not a host path
			}
			s.noteMatch(r.name, s.pos+int64(m[0]), m[1]-m[0])
		}
	}
	if len(s.toks) == 0 {
		return
	}
	bufLower := asciiLower(s.buf)
	for _, t := range s.toks {
		name := "deny-token:" + strconv.Itoa(t.line)
		if s.done[name] {
			continue
		}
		if i := bytes.Index(bufLower, t.needle); i >= 0 {
			s.done[name] = true
			s.denyOff = append(s.denyOff, contentMatch{rule: name, offset: s.pos + int64(i), deny: true})
		}
	}
}

// noteMatch records one content match at its absolute start offset;
// the same (rule, offset) seen again keeps the larger length.
func (s *contentScanner) noteMatch(rule string, offset int64, length int) {
	k := rule + "\x00" + strconv.FormatInt(offset, 10)
	if i, ok := s.seen[k]; ok {
		if length > s.matches[i].length {
			s.matches[i].length = length
		}
		return
	}
	s.seen[k] = len(s.matches)
	s.matches = append(s.matches, contentMatch{rule: rule, offset: offset, length: length})
}

// results returns the file-level findings: the content matches and
// the deny-token hits at their absolute offsets, ascending offset then
// rule name. The caller adds them only after the file is fully read,
// so each content finding can carry the file's sha256.
func (s *contentScanner) results() []contentMatch {
	out := make([]contentMatch, 0, len(s.matches)+len(s.denyOff)+len(s.failPEM))
	out = append(out, s.matches...)
	out = append(out, s.denyOff...)
	out = append(out, s.failPEM...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].offset != out[j].offset {
			return out[i].offset < out[j].offset
		}
		return out[i].rule < out[j].rule
	})
	return out
}

// digest returns the sha256 of the whole stream read so far.
func (s *contentScanner) digest() [32]byte {
	var d [32]byte
	if s.hash != nil {
		copy(d[:], s.hash.Sum(nil))
	}
	return d
}

// pemMarkerRe matches a PEM private-key BEGIN marker.
var pemMarkerRe = regexp.MustCompile(`-----BEGIN (?:RSA |EC |OPENSSH |DSA |ENCRYPTED )?PRIVATE KEY-----`)

// pemOpenMarkers returns the offsets of every BEGIN marker in buf
// whose PEM grammar is still open at the end of buf — the end falls
// inside the line break after the marker, a header line, the optional
// blank line or the base64 run — in ascending order.
func pemOpenMarkers(buf []byte) []int {
	var out []int
	for _, m := range pemMarkerRe.FindAllIndex(buf, -1) {
		if s := pemOpenFrom(buf, m[0], m[1]); s >= 0 {
			out = append(out, m[0])
		}
	}
	return out
}

// pemOpenFrom walks the PEM grammar from the BEGIN marker that starts
// at markerOff and ends at markerEnd. It returns 0 when the grammar
// is still open at the end of buf (the end lies in a position that
// may still grow into a body) and -1 when it is closed or invalid
// before the end.
func pemOpenFrom(buf []byte, markerOff, markerEnd int) int {
	i := markerEnd
	if i == len(buf) {
		return 0 // the end is inside the line break after the marker
	}
	switch buf[i] {
	case '\n':
		i++
	case '\r':
		if i+1 == len(buf) {
			return 0 // a lone trailing \r is open
		}
		if buf[i+1] != '\n' {
			return -1 // a \r followed by a byte other than \n is closed
		}
		i += 2
	default:
		return -1 // no body follows this marker in the grammar
	}
	for {
		// Header line: [A-Za-z-]+: [^\r\n]* followed by a line break,
		// consumed whole ("\n" or "\r\n") so a CRLF line break does not
		// leave its second half to be mistaken for the blank line.
		j := i
		for j < len(buf) && ((buf[j] >= 'A' && buf[j] <= 'Z') || (buf[j] >= 'a' && buf[j] <= 'z') || buf[j] == '-') {
			j++
		}
		if j == i || j == len(buf) || buf[j] != ':' {
			break
		}
		j++ // the ':'
		for j < len(buf) && buf[j] != '\r' && buf[j] != '\n' {
			j++
		}
		if j == len(buf) {
			return 0 // the end is inside a header line
		}
		if buf[j] == '\r' {
			if j+1 == len(buf) {
				return 0 // the end is inside the CRLF line break after the header line
			}
			if buf[j+1] != '\n' {
				return -1
			}
			j += 2
		} else {
			j++
		}
		i = j
	}
	// Optional blank line.
	if i < len(buf) && (buf[i] == '\n' || buf[i] == '\r') {
		if buf[i] == '\r' {
			if i+1 == len(buf) {
				return 0 // the end is inside the blank line
			}
			if buf[i+1] != '\n' {
				return -1
			}
			i++
		}
		i++ // consume the blank line
	}
	// Base64 run: the end inside the run is open (the run may grow
	// past the window, and the next window finds the full match with
	// its full length); a run that ended before the end of the buffer
	// is closed.
	for i < len(buf) && isPEMBodyChar(buf[i]) {
		i++
	}
	if i == len(buf) {
		return 0
	}
	return -1
}

// isPEMBodyChar reports whether c is part of the base64 run: base64
// characters or the line breaks inside it.
func isPEMBodyChar(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
		c == '+' || c == '/' || c == '=' || c == '\r' || c == '\n'
}

// readBlock reads up to scanBlock bytes; a short read at the end of
// the stream is io.ErrUnexpectedEOF and a clean end is io.EOF.
func readBlock(r io.Reader) ([]byte, error) {
	buf := make([]byte, scanBlock)
	n, err := io.ReadFull(r, buf)
	if n == 0 && err == nil {
		err = io.EOF
	}
	return buf[:n], err
}

// applyAccept marks every content finding that an accept entry binds
// to exactly — same acceptable rule, key path, offset, length and
// file sha256 — as accepted, and records every entry that binds to
// nothing as an unmatched accept, in input order (accept-file lines
// first, then the --accept flags). One entry may accept several
// findings only when they are the same match of byte-identical files
// at the same key path in different layers.
func applyAccept(f *scanFindings, entries []entrySource) {
	for _, s := range entries {
		matched := false
		for i := range f.order {
			x := &f.order[i]
			if x.accept || x.deny || x.meta || x.off < 0 {
				continue
			}
			if x.rule != s.entry.rule || x.key != s.entry.path || x.off != s.entry.offset ||
				int64(x.length) != s.entry.length || x.sha != s.entry.sha {
				continue
			}
			x.accept = true
			matched = true
		}
		if !matched {
			kind := "accept-file"
			if s.kind == entryFromFlag {
				kind = "accept"
			}
			f.unmatched = append(f.unmatched, "unmatched-accept\t"+kind+"\t"+strconv.Itoa(s.index))
		}
	}
}

// finishScan maps the collected state to an exit code: any finding or
// any unmatched accept entry fails the run.
func finishScan(f *scanFindings) int {
	if len(f.unmatched) > 0 {
		return exitFindings
	}
	for _, x := range f.order {
		if !x.accept {
			return exitFindings
		}
	}
	return exitOK
}

// scanContent streams the content of one file or layer entry through
// the scanner and, only after the file was fully read, records the
// per-match findings — each content finding carrying the match length
// and the sha256 of the whole file — plus the deny-token hits. key is
// the match identity (relative path, entry name or "config"); display
// is the printed path.
func scanContent(r io.Reader, rules []scanRule, key, display string, toks []denyToken, f *scanFindings) (int64, error) {
	s := newContentScanner(r, rules, toks)
	s.hash = sha256.New()
	n, err := s.scan()
	if err != nil {
		return n, err
	}
	d := s.digest()
	for _, m := range s.results() {
		if m.deny {
			f.addDeny(m.rule, display, key, m.offset)
		} else {
			f.addContent(m.rule, display, key, m.offset, m.length, d)
		}
	}
	return n, nil
}

// denyLoadError is a --deny-file load error whose message is static
// (no path, no other user-controlled value), so it may be printed to
// the raw stderr before the redacting writer exists. io selects the
// exit class: true for I/O, false for usage.
type denyLoadError struct {
	msg string
	io  bool
}

func (e *denyLoadError) Error() string { return e.msg }

// reportLoadError maps a --deny-file or --accept-file load error to an
// exit code: a missing or unreadable file is an I/O error, a short
// token or a malformed entry is a usage error. Deny-file errors carry
// their class on the error itself; accept-file errors keep the
// *fs.PathError test.
func reportLoadError(w io.Writer, err error) int {
	fmt.Fprintf(w, "imagecheck: %v\n", err)
	var dl *denyLoadError
	if errors.As(err, &dl) {
		if dl.io {
			return exitIO
		}
		return exitUsage
	}
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return exitIO
	}
	return exitUsage
}

// redactingWriter wraps an output writer so that every unit passes
// through maskRuleTokens and then redactDenyTokens before it reaches
// the destination: a deny literal held by a path or an error message
// never reaches stdout or stderr. The rule mask runs first because its
// marker can itself hold a deny literal: the deny pass must see the
// marker or a redacted literal would survive in it. The redacted unit
// is everything buffered up to the last line break: fmt.Fprintf
// delivers a whole message in a single Write, so a message holding
// several lines reaches the passes together, and a literal or a token
// split across several small writes is still handled. The rest, without
// a line break, stays buffered, and this writer has no Flush: every
// message of this package ends in a line break and leaves no rest.
type redactingWriter struct {
	w       io.Writer
	toks    []denyToken
	pending []byte
}

// Write appends b to the buffer and, for every unit it forms (from
// the start of the buffer to its last line break), masks the built-in
// rule tokens and then redacts the deny literals before forwarding
// it.
func (r *redactingWriter) Write(b []byte) (int, error) {
	if len(r.toks) == 0 {
		return r.w.Write(b)
	}
	r.pending = append(r.pending, b...)
	for {
		i := bytes.LastIndexByte(r.pending, '\n')
		if i < 0 {
			break
		}
		unit := r.pending[:i+1]
		if _, err := r.w.Write([]byte(redactDenyTokens(maskRuleTokens(string(unit)), r.toks))); err != nil {
			return len(b), err
		}
		r.pending = r.pending[i+1:]
	}
	return len(b), nil
}

// runContext scans a build context directory for secrets, credential
// files and denied tokens. It prints one line per finding and a
// summary line; the matched content is never printed.
func runContext(args []string, stdout, stderr io.Writer) int {
	var opts scanOpts
	pos, err := parseScanFlags(args, &opts)
	if err != nil {
		fmt.Fprintf(stderr, "imagecheck: %v\n", err)
		usage(stderr)
		return exitUsage
	}
	if len(pos) != 1 {
		fmt.Fprintf(stderr, "imagecheck: context needs exactly one <dir> argument\n")
		usage(stderr)
		return exitUsage
	}
	dir := pos[0]
	if len(opts.allow) == 0 {
		fmt.Fprintln(stderr, "imagecheck: context needs at least one --allow <prefix>")
		usage(stderr)
		return exitUsage
	}
	var toks []denyToken
	if opts.denyFile != "" {
		toks, err = readDenyFile(opts.denyFile)
		if err != nil {
			return reportLoadError(stderr, err)
		}
	}
	// From here on every line written to stdout or stderr passes
	// through redactDenyTokens, so a deny literal held by a path or an
	// error message never reaches the output.
	out := &redactingWriter{w: stdout, toks: toks}
	errOut := &redactingWriter{w: stderr, toks: toks}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		if err == nil {
			err = fmt.Errorf("%s: not a directory", dir)
		}
		fmt.Fprintf(errOut, "imagecheck: %v\n", err)
		return exitIO
	}
	entries, err := collectEntries(opts)
	if err != nil {
		return reportLoadError(errOut, err)
	}
	f := newFindings()
	if err := walkContext(dir, opts.allow, toks, f); err != nil {
		fmt.Fprintf(errOut, "imagecheck: %v\n", err)
		return exitIO
	}
	applyAccept(f, entries)
	f.print(out)
	return finishScan(f)
}

// walkContext walks dir in lexical order. Every entry gets the
// forbidden-name check and the deny-token and content-rule checks on
// its path; files and symlinks also get not-allowlisted; a symlink's
// target text gets the deny-token, content-rule and forbidden-name
// checks (the link is never followed or read, avoiding cycles and
// duplicate content); only regular files are content-scanned. An entry
// whose path holds a deny literal or matches a built-in content rule
// is printed under its ordinal in the walk, which no accept entry can
// name.
func walkContext(dir string, allow []string, toks []denyToken, f *scanFindings) error {
	ordinal := 0
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		defer func() { ordinal++ }()
		display := rel
		key := rel
		lines := denyHitLines(rel, toks)
		hits := contentRuleHits(rel, scanRules)
		if len(lines) > 0 || len(hits) > 0 {
			display = redactedDisplay("", ordinal)
			key = display
			for _, ln := range lines {
				f.addMeta("deny-token:"+strconv.Itoa(ln), display, "name")
			}
			for _, rule := range hits {
				f.addMeta(rule, display, "name")
			}
		}
		if d.IsDir() {
			if forbiddenName(rel) {
				f.addName("forbidden-name", display)
			}
			return nil
		}
		f.files++
		if forbiddenName(rel) {
			f.addName("forbidden-name", display)
		}
		if notAllowlisted(rel, allow) {
			f.addName("not-allowlisted", display)
		}
		if d.Type().IsRegular() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			if info.Size() == 0 {
				return nil
			}
			file, err := os.Open(p)
			if err != nil {
				return err
			}
			defer file.Close()
			n, err := scanContent(file, scanRules, key, display, toks, f)
			if err != nil {
				return err
			}
			f.bytes += n
			return nil
		}
		if d.Type()&fs.ModeSymlink == 0 {
			// Fifos and other special entries: name rules only.
			return nil
		}
		// Symlink: the target text is checked, the link itself is
		// never followed or read.
		target, err := os.Readlink(p)
		if err != nil {
			return err
		}
		checkContextLink(rel, target, filepath.Separator, display, toks, f)
		return nil
	})
}

// checkContextLink applies the symlink-target rules to one context
// link: the deny-token checks and the content rules run on the raw
// os.Readlink target and, when sep is not "/", on its slash form
// (slashLinkTarget, where a backslash is the OS separator); the
// resolved name gets the deny-token checks, the content rules and the
// forbidden-name check. All findings are recorded at @link; addMeta
// de-duplicates.
// On Unix (sep == "/") the slash form equals the raw target, so the
// findings and their order are exactly the historical ones.
func checkContextLink(rel, target string, sep byte, display string, toks []denyToken, f *scanFindings) {
	slashed := slashLinkTarget(target, sep)
	resolved := contextLinkName(rel, slashed)
	for _, ln := range denyHitLines(target, toks) {
		f.addMeta("deny-token:"+strconv.Itoa(ln), display, "link")
	}
	if slashed != target {
		for _, ln := range denyHitLines(slashed, toks) {
			f.addMeta("deny-token:"+strconv.Itoa(ln), display, "link")
		}
	}
	for _, ln := range denyHitLines(resolved, toks) {
		f.addMeta("deny-token:"+strconv.Itoa(ln), display, "link")
	}
	for _, rule := range contentRuleHits(target, scanRules) {
		f.addMeta(rule, display, "link")
	}
	if slashed != target {
		for _, rule := range contentRuleHits(slashed, scanRules) {
			f.addMeta(rule, display, "link")
		}
	}
	for _, rule := range contentRuleHits(resolved, scanRules) {
		f.addMeta(rule, display, "link")
	}
	if forbiddenName(resolved) {
		f.addMeta("forbidden-name", display, "link")
	}
}

// runImage scans a docker save archive: every layer entry and the
// image config for secrets, credential paths, denied tokens and host
// paths. The matched content is never printed.
func runImage(args []string, stdout, stderr io.Writer) int {
	var opts scanOpts
	pos, err := parseScanFlags(args, &opts)
	if err != nil {
		fmt.Fprintf(stderr, "imagecheck: %v\n", err)
		usage(stderr)
		return exitUsage
	}
	if len(pos) != 1 {
		fmt.Fprintf(stderr, "imagecheck: image needs exactly one <archive> argument\n")
		usage(stderr)
		return exitUsage
	}
	archive := pos[0]
	if len(opts.allow) > 0 {
		fmt.Fprintln(stderr, "imagecheck: image does not take --allow")
		usage(stderr)
		return exitUsage
	}
	var toks []denyToken
	if opts.denyFile != "" {
		toks, err = readDenyFile(opts.denyFile)
		if err != nil {
			return reportLoadError(stderr, err)
		}
	}
	// From here on every line written to stdout or stderr passes
	// through redactDenyTokens, so a deny literal held by a path or an
	// error message never reaches the output.
	out := &redactingWriter{w: stdout, toks: toks}
	errOut := &redactingWriter{w: stderr, toks: toks}
	if info, err := os.Stat(archive); err != nil || !info.Mode().IsRegular() {
		if err == nil {
			err = fmt.Errorf("%s: not a regular file", archive)
		}
		fmt.Fprintf(errOut, "imagecheck: %v\n", err)
		return exitIO
	}
	entries, err := collectEntries(opts)
	if err != nil {
		return reportLoadError(errOut, err)
	}
	img, err := openSaved(archive)
	if err != nil {
		fmt.Fprintf(errOut, "imagecheck: %v\n", err)
		return exitIO
	}
	f := newFindings()
	walkErr := walkLayers(archive, img, func(index int, _ string, layer io.Reader) error {
		return scanLayer(layer, index, toks, f)
	})
	if walkErr != nil {
		fmt.Fprintf(errOut, "imagecheck: %v\n", walkErr)
		return exitIO
	}
	// The archive's index files (manifest.json and the other outer
	// files) are scanned after the layers with the content rules and
	// the deny tokens, like any file content: their findings are
	// acceptable under the archive: display.
	for i, o := range img.Others {
		display := "archive:" + o.Name
		key := display
		lines := denyHitLines(o.Name, toks)
		hits := contentRuleHits(o.Name, scanRules)
		if len(lines) > 0 || len(hits) > 0 {
			display = redactedDisplay("archive:", i)
			key = display
			for _, ln := range lines {
				f.addMeta("deny-token:"+strconv.Itoa(ln), display, "name")
			}
			for _, rule := range hits {
				f.addMeta(rule, display, "name")
			}
		}
		f.files++
		n, err := scanContent(bytes.NewReader(o.Data), scanRules, key, display, toks, f)
		if err != nil {
			fmt.Fprintf(errOut, "imagecheck: %v\n", err)
			return exitIO
		}
		f.bytes += n
	}
	// The config is scanned after the layers; its content rules also
	// cover history/created_by, plus the config-only rules.
	if err := scanImageConfig(img, toks, f); err != nil {
		fmt.Fprintf(errOut, "imagecheck: %v\n", err)
		return exitIO
	}
	applyAccept(f, entries)
	f.print(out)
	return finishScan(f)
}

// scanLayer scans one layer tar. Every entry gets the deny-token and
// content-rule checks on its metadata (name, link target, owner
// names, PAX records), the image name rules on its normalized name and
// on its link target (a genuine whiteout marker only records a
// deletion and is skipped by those two rules), and regular files are
// content-scanned. An entry whose raw or normalized name holds a deny
// literal or matches a built-in content rule is printed under its
// ordinal in the layer, which no accept entry can name; a link target
// never changes the display.
func scanLayer(layer io.Reader, index int, toks []denyToken, f *scanFindings) error {
	prefix := "layer" + strconv.Itoa(index) + ":"
	ordinal := 0
	return walkTar(layer, func(h *tar.Header, content io.Reader) error {
		defer func() { ordinal++ }()
		name := normalizeArchiveName(h.Name)
		display := prefix + name
		key := name
		rawHits := contentRuleHits(h.Name, scanRules)
		normHits := contentRuleHits(name, scanRules)
		if len(denyHitLines(h.Name, toks)) > 0 || len(denyHitLines(name, toks)) > 0 ||
			len(rawHits) > 0 || len(normHits) > 0 {
			display = redactedDisplay(prefix, ordinal)
			key = display
		}
		// Deny literals and content rules over every metadata field.
		for _, mf := range headerMetaFields(h) {
			for _, ln := range denyHitLines(mf.Value, toks) {
				f.addMeta("deny-token:"+strconv.Itoa(ln), display, mf.Field)
			}
			for _, rule := range contentRuleHits(mf.Value, scanRules) {
				f.addMeta(rule, display, mf.Field)
			}
		}
		// The raw fields can split a literal that a reader joins: the
		// normalized name and the resolved link target are what get
		// extracted ("dir/./x" and "dir//x" are "dir/x").
		for _, ln := range denyHitLines(name, toks) {
			f.addMeta("deny-token:"+strconv.Itoa(ln), display, "name")
		}
		for _, rule := range normHits {
			f.addMeta(rule, display, "name")
		}
		if target := linkTargetName(name, h); target != "" {
			for _, ln := range denyHitLines(target, toks) {
				f.addMeta("deny-token:"+strconv.Itoa(ln), display, "link")
			}
			for _, rule := range contentRuleHits(target, scanRules) {
				f.addMeta(rule, display, "link")
			}
		}
		// A genuine whiteout records a deletion: its name is not
		// credential or git material; a .wh. entry that is not a
		// marker (non-empty, or not a regular file) gets everything,
		// content included.
		whiteout := isWhiteoutMarker(h, name)
		if !whiteout && name != "/" {
			if credentialPath(name) {
				f.addName("credential-path", display)
			}
			if gitDir(name) {
				f.addName("git-dir", display)
			}
		}
		if target := linkTargetName(name, h); target != "" && !whiteout {
			if credentialPath(target) {
				f.addMeta("credential-path", display, "link")
			}
			if gitDir(target) {
				f.addMeta("git-dir", display, "link")
			}
		}
		// Every entry that carries data is content-scanned, whatever its
		// type: a contiguous file ('7'), a sparse file or an unknown type
		// with a body is extracted with that body by some reader, so the
		// type alone never skips the scan. Directories, symlinks and hard
		// links carry no data.
		if h.Typeflag != tar.TypeReg && h.Size == 0 {
			return nil
		}
		f.files++
		if h.Size == 0 {
			return nil
		}
		n, err := scanContent(content, scanRules, key, display, toks, f)
		if err != nil {
			return err
		}
		f.bytes += n
		return nil
	})
}

// credentialPath reports whether the remainder of name below root/ or
// home/<user>/ is a known credential path or anything under .ssh/,
// comparing the ASCII-folded remainder against the table (the fold of
// the deny matcher).
func credentialPath(name string) bool {
	rest, ok := credentialRemainder(name)
	if !ok {
		return false
	}
	folded := lowerASCIIString(rest)
	if strings.HasPrefix(folded, imageSshPrefix) {
		return true
	}
	for _, p := range imageCredentialPaths {
		if folded == p {
			return true
		}
	}
	return false
}

// credentialRemainder strips the root/ or home/<user>/ prefix and
// returns the remainder below it. The prefix is compared against the
// ASCII-folded copy of name (root and home match case-insensitively,
// the user segment is any non-empty segment); only the comparison is
// folded, never the stored or printed name.
func credentialRemainder(name string) (string, bool) {
	folded := lowerASCIIString(name)
	if strings.HasPrefix(folded, "root/") {
		return name[len("root/"):], true
	}
	if !strings.HasPrefix(folded, "home/") {
		return "", false
	}
	rest := name[len("home/"):]
	i := strings.IndexByte(rest, '/')
	if i < 0 {
		return "", false
	}
	return rest[i+1:], true
}

// gitDir reports whether any component of name is .git, comparing
// ASCII case-insensitively (the fold of the deny matcher).
func gitDir(name string) bool {
	for _, c := range strings.Split(name, "/") {
		if lowerASCIIString(c) == ".git" {
			return true
		}
	}
	return false
}

// scanImageConfig scans the config: the Env names, then the content
// rules (which cover history/created_by), the deny tokens and the
// host-path rule. The matched content is never printed.
func scanImageConfig(img *savedImage, toks []denyToken, f *scanFindings) error {
	f.files++
	f.bytes += int64(len(img.Config))
	rules := make([]scanRule, 0, len(scanRules)+len(hostPathRules))
	rules = append(rules, scanRules...)
	rules = append(rules, hostPathRules...)
	if _, err := scanContent(bytes.NewReader(img.Config), rules, "config", "config", toks, f); err != nil {
		return err
	}
	// A config that does not parse cannot be checked for secret Env
	// entries, so it fails the scan instead of passing unread.
	cfg, err := img.parseConfig()
	if err != nil {
		return err
	}
	for _, e := range cfg.Config.Env {
		name, value, ok := strings.Cut(e, "=")
		if !ok || value == "" {
			continue
		}
		if envSecretName(name) {
			f.addName("config-env-secret:"+name, "config")
		}
	}
	return nil
}

// envSecretName reports whether the upper-cased Env name contains one
// of the secret keywords.
func envSecretName(name string) bool {
	up := strings.ToUpper(name)
	for _, k := range envSecretKeywords {
		if strings.Contains(up, k) {
			return true
		}
	}
	return false
}
