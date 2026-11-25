// Package evidence assembles a read-only manifest from an explicit inventory
// of sealed receipts. It imports producer claims; it does not discover code,
// read configuration, contact a network or write files.
package evidence

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

const (
	// Schema is the only accepted inventory and manifest version.
	Schema = 1

	usageLine        = "usage: soho evidence <inventory.json>"
	publicIDPrefix   = "sha256:"
	maxInventorySize = int64(1) << 20
	maxArtifactSize  = int64(8) << 20
	maxFieldSize     = 256
)

var (
	errUnbound     = errors.New("unbound")
	errUnavailable = errors.New("unavailable")
	errTooLarge    = errors.New("too large")
)

var (
	allowedLayers = map[string]bool{
		"unit": true, "race": true, "build": true, "vet": true,
		"format": true, "contract": true, "integration": true,
		"e2e": true, "native": true, "review": true, "ci": true,
		"gate": true, "mutation": true,
	}
	allowedPlatforms  = map[string]bool{"linux": true, "darwin": true, "windows": true, "any": true}
	allowedTransports = map[string]bool{"hc": true, "herdr": true, "none": true}
	utcTimestamp      = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?Z$`)
)

// Candidate is the frozen repository revision the manifest is about. Head is
// the source revision the candidate binary was built from.
type Candidate struct {
	Repository string `json:"repository"`
	Base       string `json:"base"`
	Head       string `json:"head"`
}

// Expected names one layer that must be evidenced. Platform and transport are
// explicit identifiers, never selected from a user label.
type Expected struct {
	Layer     string `json:"layer"`
	Platform  string `json:"platform"`
	Transport string `json:"transport"`
}

// Identity is the candidate identity attested by the receipt's producer.
// Source is the producing binary's source revision; it is not a job report's
// tracked sandbox head and is never inferred from a branch or a cwd.
type Identity struct {
	Repository string `json:"repository"`
	Base       string `json:"base"`
	Source     string `json:"source"`
}

// Receipt is one imported attestation. Producer is read but never exported.
type Receipt struct {
	Layer     string   `json:"layer"`
	Platform  string   `json:"platform"`
	Transport string   `json:"transport"`
	Producer  string   `json:"producer"`
	Outcome   string   `json:"outcome"`
	ID        string   `json:"id"`
	Since     string   `json:"since"`
	Until     string   `json:"until"`
	Identity  Identity `json:"identity"`
	SHA256    string   `json:"sha256"`
	Path      string   `json:"path"`
}

// Inventory is the normalized v1 input. Artifact paths are relative to the
// directory that holds the inventory file.
type Inventory struct {
	Schema    int        `json:"schema"`
	Candidate Candidate  `json:"candidate"`
	Expected  []Expected `json:"expected"`
	Receipts  []Receipt  `json:"receipts"`
}

// Layer is one stable manifest row. Absent provenance is an empty string,
// never a reused value from another receipt.
type Layer struct {
	Layer     string `json:"layer"`
	Platform  string `json:"platform"`
	Transport string `json:"transport"`
	Status    string `json:"status"`
	ID        string `json:"id"`
	Source    string `json:"source"`
	Digest    string `json:"digest"`
	Since     string `json:"since"`
	Until     string `json:"until"`
}

// Manifest is the only JSON shape printed on stdout.
type Manifest struct {
	Schema    int       `json:"schema"`
	Candidate Candidate `json:"candidate"`
	Result    string    `json:"result"`
	Layers    []Layer   `json:"layers"`
	Unbound   int       `json:"unbound"`
}

type layerKey struct {
	layer     string
	platform  string
	transport string
}

// Run implements the CLI entry point. It exits 0 when every expected layer
// passes and unbound is zero, 1 for a non-passing manifest, and 2 for usage
// or an unusable inventory. It writes JSON to stdout only for exit 0 or 1.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		_, _ = fmt.Fprintln(stdout, usageLine)
		return 0
	}
	if len(args) != 1 || args[0] == "" || args[0] == "-" || strings.HasPrefix(args[0], "-") {
		_, _ = fmt.Fprintf(stderr, "soho: evidence: %s\n", usageLine)
		return 2
	}
	inventory, baseDir, err := load(args[0])
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "soho: evidence: %s\n", err)
		return 2
	}
	manifest := Assemble(inventory, baseDir)
	if err := json.NewEncoder(stdout).Encode(manifest); err != nil {
		_, _ = fmt.Fprintln(stderr, "soho: evidence: cannot write manifest")
		return 2
	}
	if manifest.Result == "pass" {
		return 0
	}
	return 1
}

func load(path string) (Inventory, string, error) {
	data, err := readBounded(path, maxInventorySize)
	if err != nil {
		switch {
		case errors.Is(err, errTooLarge):
			return Inventory{}, "", errors.New("input exceeds the read bound")
		case errors.Is(err, errUnbound):
			return Inventory{}, "", errors.New("input is not a regular file")
		default:
			return Inventory{}, "", errors.New("input is unreadable")
		}
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return Inventory{}, "", errors.New("input is unreadable")
	}
	baseDir, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return Inventory{}, "", errors.New("input is unreadable")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	var inventory Inventory
	if err := decoder.Decode(&inventory); err != nil {
		return Inventory{}, "", errors.New("input is not valid JSON")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Inventory{}, "", errors.New("input has trailing data")
	}
	// Inspect decoded member names before using any typed field. The first
	// decode already checked syntax, nesting and the single-value boundary.
	members := json.NewDecoder(bytes.NewReader(data))
	members.UseNumber()
	if !uniqueJSONMembers(members) {
		return Inventory{}, "", errors.New("input has ambiguous JSON object fields")
	}
	if inventory.Schema != Schema {
		return Inventory{}, "", errors.New("input schema must be 1")
	}
	if !validRepository(inventory.Candidate.Repository) || !validRevision(inventory.Candidate.Base) || !validRevision(inventory.Candidate.Head) {
		return Inventory{}, "", errors.New("input candidate is invalid")
	}
	if len(inventory.Expected) == 0 {
		return Inventory{}, "", errors.New("input needs at least one expected layer")
	}
	seen := make(map[layerKey]bool, len(inventory.Expected))
	for _, expected := range inventory.Expected {
		key := layerKey{expected.Layer, expected.Platform, expected.Transport}
		if !allowedLayers[expected.Layer] || !allowedPlatforms[expected.Platform] || !allowedTransports[expected.Transport] || seen[key] {
			return Inventory{}, "", errors.New("input expected layers are invalid or duplicated")
		}
		seen[key] = true
	}
	return inventory, baseDir, nil
}

// uniqueJSONMembers verifies one syntactically valid JSON value. Each object
// has its own member set, and JSON escapes are resolved by Decoder.Token.
func uniqueJSONMembers(decoder *json.Decoder) bool {
	token, err := decoder.Token()
	if err != nil {
		return false
	}
	switch token {
	case json.Delim('{'):
		seen := map[string]struct{}{}
		for decoder.More() {
			token, err := decoder.Token()
			name, ok := token.(string)
			if err != nil || !ok {
				return false
			}
			folded := strings.Map(foldJSONRune, name)
			if _, exists := seen[folded]; exists {
				return false
			}
			seen[folded] = struct{}{}
			if !uniqueJSONMembers(decoder) {
				return false
			}
		}
	case json.Delim('['):
		for decoder.More() {
			if !uniqueJSONMembers(decoder) {
				return false
			}
		}
	default:
		return true
	}
	_, err = decoder.Token()
	return err == nil
}

// foldJSONRune chooses the smallest rune in the simple-fold cycle, matching
// encoding/json's case-insensitive struct field comparison.
func foldJSONRune(r rune) rune {
	for {
		next := unicode.SimpleFold(r)
		if next <= r {
			return next
		}
		r = next
	}
}

// Assemble turns declarations into a manifest. Claimed identity is checked
// before artifact bytes; a failing claim is never promoted to a pass.
func Assemble(inventory Inventory, baseDir string) Manifest {
	manifest := Manifest{
		Schema:    Schema,
		Candidate: inventory.Candidate,
		Result:    "fail",
		Layers:    make([]Layer, 0, len(inventory.Expected)),
	}
	root, rootErr := os.OpenRoot(baseDir)
	if rootErr == nil {
		defer root.Close()
	}
	expected := make(map[layerKey]bool, len(inventory.Expected))
	for _, item := range inventory.Expected {
		expected[keyForExpected(item)] = true
	}
	grouped := make(map[layerKey][]Receipt)
	for _, receipt := range inventory.Receipts {
		key := keyForReceipt(receipt)
		if !expected[key] || !validKey(key) {
			manifest.Unbound++
			continue
		}
		grouped[key] = append(grouped[key], receipt)
	}
	for _, item := range inventory.Expected {
		row := Layer{Layer: item.Layer, Platform: item.Platform, Transport: item.Transport, Status: "missing"}
		receipts := grouped[keyForExpected(item)]
		switch len(receipts) {
		case 0:
		case 1:
			row = evaluate(row, receipts[0], inventory.Candidate, root, rootErr)
		default:
			row.Status = "unbound"
			manifest.Unbound += len(receipts) - 1
		}
		manifest.Layers = append(manifest.Layers, row)
	}
	sort.Slice(manifest.Layers, func(i, j int) bool {
		left, right := manifest.Layers[i], manifest.Layers[j]
		if left.Layer != right.Layer {
			return left.Layer < right.Layer
		}
		if left.Platform != right.Platform {
			return left.Platform < right.Platform
		}
		return left.Transport < right.Transport
	})
	allPass := manifest.Unbound == 0
	for _, row := range manifest.Layers {
		if row.Status != "pass" {
			allPass = false
		}
	}
	if allPass {
		manifest.Result = "pass"
	}
	return manifest
}

func evaluate(row Layer, receipt Receipt, candidate Candidate, root *os.Root, rootErr error) Layer {
	if !validLabel(receipt.ID) || !validLabel(receipt.Producer) || !validRepository(receipt.Identity.Repository) || !validKey(keyForReceipt(receipt)) {
		row.Status = "unbound"
		return row
	}
	switch receipt.Outcome {
	case "pass", "failed", "blocked":
	default:
		row.Status = "unbound"
		return row
	}
	since, sinceText, sinceOK := normalizeUTC(receipt.Since)
	until, untilText, untilOK := normalizeUTC(receipt.Until)
	if !sinceOK || !untilOK || since.After(until) {
		row.Status = "unbound"
		return row
	}
	if !validRevision(receipt.Identity.Base) || !validRevision(receipt.Identity.Source) || !validDigest(receipt.SHA256) {
		row.Status = "unbound"
		return row
	}
	row.Source = receipt.Identity.Source
	row.Digest = receipt.SHA256
	row.Since = sinceText
	row.Until = untilText
	if receipt.Identity.Repository != candidate.Repository || receipt.Identity.Base != candidate.Base || receipt.Identity.Source != candidate.Head {
		row.Status = "stale"
		return row
	}
	name, err := artifactName(receipt.Path)
	if errors.Is(err, errUnbound) {
		row.Status = "unbound"
		return row
	}
	if rootErr != nil {
		row.Status = "unavailable"
		return row
	}
	data, err := readArtifact(root, name)
	if errors.Is(err, errUnbound) {
		row.Status = "unbound"
		return row
	}
	if err != nil {
		row.Status = "unavailable"
		return row
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != receipt.SHA256 {
		row.Status = "digest_mismatch"
		return row
	}
	row.ID = publicIDPrefix + receipt.SHA256
	row.Status = receipt.Outcome
	return row
}

func readBounded(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, errUnavailable
	}
	if !info.Mode().IsRegular() {
		return nil, errUnbound
	}
	if info.Size() > limit {
		return nil, errTooLarge
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errUnavailable
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, errUnavailable
	}
	if int64(len(data)) > limit {
		return nil, errTooLarge
	}
	return data, nil
}

func artifactName(relative string) (string, error) {
	if relative == "" || strings.HasPrefix(relative, "/") || filepath.IsAbs(relative) || strings.Contains(relative, "\\") || driveRelative(relative) {
		return "", errUnbound
	}
	for _, part := range strings.Split(relative, "/") {
		if part == "" || part == "." || part == ".." {
			return "", errUnbound
		}
	}
	clean := filepath.Clean(filepath.FromSlash(relative))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", errUnbound
	}
	return clean, nil
}

func driveRelative(path string) bool {
	return len(path) >= 2 && asciiAlpha(path[0]) && path[1] == ':'
}

// readArtifact keeps every lookup under the inventory directory through an
// os.Root handle. Each component is checked with Lstat before the open, and
// the opened handle must still be the same regular file, so a replacement
// between the check and the open cannot pass as the attested artifact.
func readArtifact(root *os.Root, relative string) ([]byte, error) {
	parts := strings.Split(relative, string(os.PathSeparator))
	current := ""
	var checked os.FileInfo
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, err := root.Lstat(current)
		if err != nil {
			return nil, errUnavailable
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, errUnbound
		}
		if i == len(parts)-1 {
			if !info.Mode().IsRegular() {
				return nil, errUnbound
			}
			checked = info
			continue
		}
		if !info.IsDir() {
			return nil, errUnbound
		}
	}
	if checked.Size() > maxArtifactSize {
		return nil, errTooLarge
	}
	file, err := root.Open(relative)
	if err != nil {
		return nil, errUnavailable
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, errUnavailable
	}
	if !opened.Mode().IsRegular() || !os.SameFile(checked, opened) {
		return nil, errUnbound
	}
	data, err := io.ReadAll(io.LimitReader(file, maxArtifactSize+1))
	if err != nil {
		return nil, errUnavailable
	}
	if int64(len(data)) > maxArtifactSize {
		return nil, errTooLarge
	}
	return data, nil
}

func keyForExpected(item Expected) layerKey {
	return layerKey{item.Layer, item.Platform, item.Transport}
}

func keyForReceipt(receipt Receipt) layerKey {
	return layerKey{receipt.Layer, receipt.Platform, receipt.Transport}
}

func validKey(key layerKey) bool {
	return allowedLayers[key.layer] && allowedPlatforms[key.platform] && allowedTransports[key.transport]
}

func validRepository(value string) bool {
	parts := strings.Split(value, "/")
	return len(parts) == 2 && validRepositoryComponent(parts[0]) && validRepositoryComponent(parts[1])
}

func validRepositoryComponent(value string) bool {
	if value == "" || len(value) > maxFieldSize || !asciiAlnum(value[0]) {
		return false
	}
	for i := 1; i < len(value); i++ {
		c := value[i]
		if !asciiAlnum(c) && c != '.' && c != '_' && c != '-' {
			return false
		}
	}
	return true
}

func asciiAlnum(c byte) bool {
	return asciiAlpha(c) || (c >= '0' && c <= '9')
}

func asciiAlpha(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func validLabel(value string) bool {
	if value == "" || len(value) > maxFieldSize {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < 0x20 || value[i] == 0x7f {
			return false
		}
	}
	return true
}

func validRevision(value string) bool {
	return validHex(value, 40) || validHex(value, 64)
}

func validDigest(value string) bool {
	return validHex(value, 64)
}

func validHex(value string, size int) bool {
	if len(value) != size || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func normalizeUTC(value string) (time.Time, string, bool) {
	if !utcTimestamp.MatchString(value) {
		return time.Time{}, "", false
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil || parsed.Location() != time.UTC {
		return time.Time{}, "", false
	}
	return parsed, parsed.UTC().Format(time.RFC3339Nano), true
}
