package transcript

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/djalmajr/soho/internal/platform"
)

type codexLimits struct {
	snapshot, record, records, entries int
	bytes                              int64
	work                               time.Duration
}

var codexReceiptLimits = codexLimits{32 << 20, 1 << 20, 200000, 4096, 128 << 20, 2 * time.Second}

type codexNode struct {
	path string
	info os.FileInfo
}
type codexSnapshot struct {
	size   int64
	digest [32]byte
	count  int
	info   os.FileInfo
}

// CodexReceipt is opportunistic input-persistence evidence, not model receipt.
// It retains identities/counts/digests, never message bodies. Failure permanently
// disables this per-send reader. Work bounds are cooperative, not an I/O SLA.
type CodexReceipt struct {
	root                *os.Root
	rootPath            string
	rootInfo            os.FileInfo
	file                *os.File
	nodes               []codexNode
	path, uuid, closing string
	previous            codexSnapshot
	baseline            int
	limits              codexLimits
	left                int64
	work                time.Duration
	deadline            time.Time
	disabled            bool
	// Per-instance deterministic test seam, nil in production.
	hook func(string)
}

func CanonicalCodexUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// NewCodexReceipt arms only on one regular candidate in home/sessions/YYYY/MM/DD.
// A nil result is an ordinary fallback, with no content-bearing diagnostic.
func NewCodexReceipt(home, uuid, closing string) *CodexReceipt {
	return newCodexReceipt(home, uuid, closing, codexReceiptLimits, nil)
}

func newCodexReceipt(home, uuid, closing string, limits codexLimits, hook func(string)) *CodexReceipt {
	if home == "" || !CanonicalCodexUUID(uuid) || closing == "" {
		return nil
	}
	r := &CodexReceipt{uuid: uuid, closing: closing, limits: limits, left: limits.bytes, work: limits.work, hook: hook}
	start := r.begin()
	defer r.end(start)
	path := filepath.Join(home, "sessions")
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() {
		return nil
	}
	// Establish the configured home capability once; even the sessions
	// directory is opened relatively and nonblocking before type validation.
	homeRoot, err := os.OpenRoot(home)
	if err != nil {
		return nil
	}
	defer homeRoot.Close()
	r.step("before-open:sessions")
	directory, err := homeRoot.OpenFile("sessions", platform.ReadOnlyNonBlocking, 0)
	if err != nil {
		return nil
	}
	defer directory.Close()
	opened, err := directory.Stat()
	named, namedErr := homeRoot.Lstat("sessions")
	if err != nil || namedErr != nil || !opened.IsDir() || !named.IsDir() || !os.SameFile(info, opened) || !os.SameFile(info, named) {
		return nil
	}
	r.root, err = homeRoot.OpenRoot("sessions")
	if err != nil {
		return nil
	}
	r.rootPath, r.rootInfo = path, info
	ok := false
	defer func() {
		if !ok {
			r.Close()
		}
	}()
	r.nodes = append(r.nodes, codexNode{".", info})
	if !r.identities() || !r.discover(".", 0, new(int)) || r.path == "" {
		return nil
	}
	r.file = r.open(r.path, false)
	if r.file == nil {
		return nil
	}
	snap, valid := r.snapshot()
	if !valid {
		return nil
	}
	r.previous, r.baseline = snap, snap.count
	ok = true
	return r
}

func (r *CodexReceipt) Close() {
	if r == nil {
		return
	}
	if r.file != nil {
		_ = r.file.Close()
		r.file = nil
	}
	if r.root != nil {
		_ = r.root.Close()
		r.root = nil
	}
	r.disabled = true
}

func (r *CodexReceipt) begin() time.Time    { now := time.Now(); r.deadline = now.Add(r.work); return now }
func (r *CodexReceipt) end(start time.Time) { r.work -= time.Since(start) }
func (r *CodexReceipt) budget() bool {
	return r.work > 0 && time.Now().Before(r.deadline) && r.left >= 0
}
func (r *CodexReceipt) step(stage string) {
	if r.hook != nil {
		r.hook(stage)
	}
}

// Every open beneath the capability is nonblocking before descriptor type
// validation. Lstat alone does not close a replacement/open race.
func (r *CodexReceipt) open(path string, directory bool) *os.File {
	if !r.budget() {
		return nil
	}
	before, err := r.root.Lstat(path)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || directory != before.IsDir() || !directory && !before.Mode().IsRegular() {
		return nil
	}
	r.step("before-open:" + path)
	f, err := r.root.OpenFile(path, platform.ReadOnlyNonBlocking, 0)
	if err != nil {
		return nil
	}
	info, err := f.Stat()
	after, statErr := r.root.Lstat(path)
	if err != nil || statErr != nil || !os.SameFile(before, info) || !os.SameFile(info, after) || after.Mode()&os.ModeSymlink != 0 || directory != info.IsDir() || !directory && !info.Mode().IsRegular() {
		_ = f.Close()
		return nil
	}
	return f
}

func codexDatePart(name string, depth int) bool {
	want := 2
	if depth == 0 {
		want = 4
	}
	if len(name) != want {
		return false
	}
	for _, c := range name {
		if c < '0' || c > '9' {
			return false
		}
	}
	return depth == 0 || depth == 1 && name >= "01" && name <= "12" || depth == 2 && name >= "01" && name <= "31"
}

func (r *CodexReceipt) discover(path string, depth int, entries *int) bool {
	dir := r.open(path, true)
	if dir == nil {
		return false
	}
	defer dir.Close()
	for {
		if !r.budget() {
			return false
		}
		batch, err := dir.ReadDir(64)
		*entries += len(batch)
		if *entries > r.limits.entries {
			return false
		}
		for _, entry := range batch {
			rel := filepath.Join(path, entry.Name())
			if depth < 3 {
				if !codexDatePart(entry.Name(), depth) {
					continue
				}
				info, e := r.root.Lstat(rel)
				if e != nil || !info.IsDir() {
					return false
				}
				r.nodes = append(r.nodes, codexNode{rel, info})
				if !r.discover(rel, depth+1, entries) {
					return false
				}
			} else if strings.HasPrefix(entry.Name(), "rollout-") && strings.HasSuffix(entry.Name(), "-"+r.uuid+".jsonl") {
				if _, e := time.Parse("2006/01/02", filepath.ToSlash(path)); e != nil {
					return false
				}
				if r.path != "" {
					return false
				}
				r.path = rel
			}
		}
		if err == io.EOF {
			return r.identities()
		}
		if err != nil {
			return false
		}
	}
}

func (r *CodexReceipt) identities() bool {
	current, err := os.Lstat(r.rootPath)
	if err != nil || !current.IsDir() || !os.SameFile(r.rootInfo, current) {
		return false
	}
	for _, node := range r.nodes {
		if !r.budget() {
			return false
		}
		info, err := r.root.Lstat(node.path)
		if err != nil || !info.IsDir() || !os.SameFile(node.info, info) || !info.ModTime().Equal(node.info.ModTime()) {
			return false
		}
	}
	if r.file != nil {
		info, err := r.file.Stat()
		pathInfo, pathErr := r.root.Lstat(r.path)
		if err != nil || pathErr != nil || !pathInfo.Mode().IsRegular() || !os.SameFile(info, pathInfo) {
			return false
		}
	}
	return true
}

func (r *CodexReceipt) snapshot() (codexSnapshot, bool) {
	var zero codexSnapshot
	if !r.budget() || !r.identities() {
		return zero, false
	}
	info, err := r.file.Stat()
	if err != nil || info.Size() < r.previous.size || info.Size() > int64(r.limits.snapshot) || info.Size() > r.left {
		return zero, false
	}
	data := make([]byte, int(info.Size()))
	for at := 0; at < len(data); {
		if !r.budget() {
			return zero, false
		}
		n, e := r.file.ReadAt(data[at:min(at+64*1024, len(data))], int64(at))
		r.left -= int64(n)
		at += n
		if e != nil || n == 0 {
			return zero, false
		}
	}
	r.step("after-read")
	after, err := r.file.Stat()
	if err != nil || !os.SameFile(info, after) || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) || !r.identities() {
		return zero, false
	}
	if r.previous.size > 0 && sha256.Sum256(data[:r.previous.size]) != r.previous.digest {
		return zero, false
	}
	count, valid := r.count(data)
	if !valid || !r.budget() {
		return zero, false
	}
	return codexSnapshot{int64(len(data)), sha256.Sum256(data), count, after}, true
}

// Receipt verifies all previously observed bytes and rereads the complete
// observed prefix before success. validate rechecks the actual target binding.
// A rewrite completely restored between observations is not observable.
func (r *CodexReceipt) Receipt(validate func() bool) bool {
	if r == nil || r.disabled {
		return false
	}
	start := r.begin()
	defer func() {
		if !start.IsZero() {
			r.end(start)
		}
	}()
	first, ok := r.snapshot()
	if !ok {
		r.Close()
		return false
	}
	r.previous = first
	if first.count <= r.baseline {
		return false
	}
	r.step("before-verify")
	if validate == nil || !r.budget() {
		r.Close()
		return false
	}
	// Binding/screen validation belongs to the caller's transport budget,
	// not this reader's cumulative work budget. Charge local work so far,
	// pause for the callback, then resume before the final full reread.
	r.end(start)
	start = time.Time{}
	valid := validate()
	start = r.begin()
	if !valid {
		r.Close()
		return false
	}
	final, ok := r.snapshot()
	if !ok || final.size != first.size || final.digest != first.digest || !os.SameFile(first.info, final.info) {
		r.Close()
		return false
	}
	r.previous = final
	return true
}

func (r *CodexReceipt) count(data []byte) (int, bool) {
	if len(data) == 0 || data[len(data)-1] != '\n' {
		return 0, false
	}
	count, records := 0, 0
	for len(data) > 0 {
		if !r.budget() {
			return 0, false
		}
		end := bytes.IndexByte(data, '\n')
		if end < 0 || end+1 > r.limits.record || records >= r.limits.records || !utf8.Valid(data[:end]) || !codexUnicode(data[:end]) {
			return 0, false
		}
		dec := json.NewDecoder(bytes.NewReader(data[:end]))
		dec.UseNumber()
		value, err := codexJSON(dec, 0)
		if err != nil {
			return 0, false
		}
		if _, err = dec.Token(); err != io.EOF {
			return 0, false
		}
		obj, ok := value.(map[string]any)
		if !ok {
			return 0, false
		}
		typ, ok := obj["type"].(string)
		if !ok {
			return 0, false
		}
		payload, _ := obj["payload"].(map[string]any)
		if records == 0 {
			if typ != "session_meta" || payload["id"] != r.uuid {
				return 0, false
			}
		} else if typ == "session_meta" {
			return 0, false
		} else if typ == "response_item" {
			if payload == nil {
				return 0, false
			}
			if _, ok := payload["type"].(string); !ok {
				return 0, false
			}
			if payload["type"] == "message" {
				if _, ok := payload["role"].(string); !ok {
					return 0, false
				}
			}
			if payload["type"] == "message" && payload["role"] == "user" {
				blocks, ok := payload["content"].([]any)
				if !ok || len(blocks) == 0 {
					return 0, false
				}
				matched := false
				for _, block := range blocks {
					b, ok := block.(map[string]any)
					if !ok || b["type"] != "input_text" {
						return 0, false
					}
					text, ok := b["text"].(string)
					if !ok {
						return 0, false
					}
					for _, line := range strings.Split(text, "\n") {
						if line == r.closing {
							matched = true
						}
					}
				}
				if matched {
					count++
				}
			}
		}
		records++
		data = data[end+1:]
	}
	return count, true
}

// encoding/json replaces lone UTF-16 surrogates; ambiguous text is not proof.
func codexUnicode(data []byte) bool {
	for i := 0; i < len(data); i++ {
		if data[i] != '\\' {
			continue
		}
		i++
		if i >= len(data) {
			return false
		}
		if data[i] != 'u' {
			continue
		}
		if i+4 >= len(data) {
			return false
		}
		n, e := strconv.ParseUint(string(data[i+1:i+5]), 16, 16)
		if e != nil {
			return false
		}
		i += 4
		if n >= 0xd800 && n <= 0xdbff {
			if i+6 >= len(data) || data[i+1] != '\\' || data[i+2] != 'u' {
				return false
			}
			low, e := strconv.ParseUint(string(data[i+3:i+7]), 16, 16)
			if e != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		} else if n >= 0xdc00 && n <= 0xdfff {
			return false
		}
	}
	return true
}

// Reject duplicate keys and excessive nesting instead of JSON's last-key-wins
// interpretation of an ambiguous typed record.
func codexJSON(d *json.Decoder, depth int) (any, error) {
	if depth > 64 {
		return nil, io.ErrUnexpectedEOF
	}
	t, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch t {
	case json.Delim('{'):
		m := map[string]any{}
		for d.More() {
			key, e := d.Token()
			if e != nil {
				return nil, e
			}
			k, ok := key.(string)
			if !ok {
				return nil, io.ErrUnexpectedEOF
			}
			if _, exists := m[k]; exists {
				return nil, io.ErrUnexpectedEOF
			}
			v, e := codexJSON(d, depth+1)
			if e != nil {
				return nil, e
			}
			m[k] = v
		}
		end, e := d.Token()
		if e != nil || end != json.Delim('}') {
			return nil, io.ErrUnexpectedEOF
		}
		return m, nil
	case json.Delim('['):
		var a []any
		for d.More() {
			v, e := codexJSON(d, depth+1)
			if e != nil {
				return nil, e
			}
			a = append(a, v)
		}
		end, e := d.Token()
		if e != nil || end != json.Delim(']') {
			return nil, io.ErrUnexpectedEOF
		}
		return a, nil
	default:
		if _, ok := t.(json.Delim); ok {
			return nil, io.ErrUnexpectedEOF
		}
		return t, nil
	}
}
