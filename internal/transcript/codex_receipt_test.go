package transcript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const receiptUUID = "11111111-2222-4333-8444-555555555555"
const receiptClosing = "[/soho:peer:01020304]"

func receiptHeader() string {
	return `{"type":"session_meta","payload":{"id":"` + receiptUUID + `"}}` + "\n"
}
func receiptMessage(t *testing.T, role, text string) string {
	t.Helper()
	b, e := json.Marshal(map[string]any{"type": "response_item", "payload": map[string]any{"type": "message", "role": role, "content": []any{map[string]any{"type": "input_text", "text": text}}}})
	if e != nil {
		t.Fatal(e)
	}
	return string(b) + "\n"
}
func receiptFixture(t *testing.T, body string) (string, string) {
	t.Helper()
	home := t.TempDir()
	path := filepath.Join(home, "sessions", "2026", "11", "05", "rollout-test-"+receiptUUID+".jsonl")
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(path, []byte(body), 0600); e != nil {
		t.Fatal(e)
	}
	return home, path
}
func receiptAppend(t *testing.T, path, text string) {
	t.Helper()
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.WriteString(text)
	ce := f.Close()
	if e != nil {
		t.Fatal(e)
	}
	if ce != nil {
		t.Fatal(ce)
	}
}
func armReceipt(t *testing.T, home string) *CodexReceipt {
	t.Helper()
	r := NewCodexReceipt(home, receiptUUID, receiptClosing)
	if r == nil {
		t.Fatal("valid controlled baseline did not arm")
	}
	t.Cleanup(r.Close)
	return r
}

func TestCodexReceiptBaselineAndRecordCount(t *testing.T) {
	old := receiptHeader() + receiptMessage(t, "user", receiptClosing)
	home, path := receiptFixture(t, old)
	r := armReceipt(t, home)
	if r.Receipt(func() bool { return true }) {
		t.Fatal("old match became new receipt")
	}
	// Duplicate occurrences and duplicate text blocks still represent one record.
	line := `{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"` + receiptClosing + `\n` + receiptClosing + `"},{"type":"input_text","text":"` + receiptClosing + `"}]}}` + "\n"
	receiptAppend(t, path, line)
	if !r.Receipt(func() bool { return true }) || r.previous.count != 2 {
		t.Fatalf("new record not counted once: count=%d disabled=%v", r.previous.count, r.disabled)
	}
	if spent := r.limits.bytes - r.left; spent != int64(len(old)+2*(len(old)+len(line))+len(old)) {
		t.Errorf("whole-path read accounting=%d", spent)
	}
}

func TestCodexReceiptTypedNegativeRecords(t *testing.T) {
	for _, kind := range []string{"assistant", "tool", "quoted", "prefix", "suffix", "nested", "event", "foreign_id"} {
		t.Run(kind, func(t *testing.T) {
			home, path := receiptFixture(t, receiptHeader())
			r := armReceipt(t, home)
			text := receiptClosing
			role := "user"
			switch kind {
			case "assistant", "tool":
				role = kind
			case "quoted":
				text = "> " + text
			case "prefix":
				text = "prefix " + text
			case "suffix":
				text += " suffix"
			case "foreign_id":
				text = "[/soho:peer:010203040]"
			}
			line := receiptMessage(t, role, text)
			if kind == "nested" {
				line = `{"type":"event_msg","payload":{"quote":{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"` + receiptClosing + `"}]}}}}` + "\n"
			}
			if kind == "event" {
				line = `{"type":"event_msg","payload":{"type":"user_message","message":"` + receiptClosing + `"}}` + "\n"
			}
			receiptAppend(t, path, line)
			if r.Receipt(func() bool { return true }) {
				t.Fatal("non-user/quoted/lookalike record became receipt")
			}
		})
	}
}

func TestCodexReceiptDecodedClosingLine(t *testing.T) {
	home, path := receiptFixture(t, receiptHeader())
	r := armReceipt(t, home)
	line := strings.Replace(receiptMessage(t, "user", receiptClosing), "[/soho", `\u005b/soho`, 1)
	receiptAppend(t, path, line)
	if !r.Receipt(func() bool { return true }) {
		t.Fatal("exact decoded closing line was not recognized")
	}
}

func TestCodexReceiptInvalidBaselineAndGrowth(t *testing.T) {
	for _, bad := range []string{
		`{"type":"response_item","payload":{"type":"message","role":"user","content":"` + receiptClosing + `"}}` + "\n",
		`{"type":"response_item","payload":{"type":"message","role":"assistant","role":"user","content":[{"type":"input_text","text":"` + receiptClosing + `"}]}}` + "\n",
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_image","text":"` + receiptClosing + `"}]}}` + "\n",
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"` + receiptClosing + `"}]}}`,
		"{broken}\n", "{} {}\n", string([]byte{0xff, '\n'}),
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"\ud800\n` + receiptClosing + `"}]}}` + "\n",
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"` + receiptClosing + `"},{"type":"input_image"}]}}` + "\n",
	} {
		t.Run(strings.ReplaceAll(bad[:min(20, len(bad))], "/", "_"), func(t *testing.T) {
			home, _ := receiptFixture(t, receiptHeader()+bad)
			if r := NewCodexReceipt(home, receiptUUID, receiptClosing); r != nil {
				r.Close()
				t.Fatal("invalid baseline armed")
			}
			home, path := receiptFixture(t, receiptHeader())
			r := armReceipt(t, home)
			receiptAppend(t, path, bad)
			if r.Receipt(func() bool { return true }) || !r.disabled {
				t.Fatal("invalid growth did not permanently disable proof")
			}
		})
	}
}

func TestCodexReceiptIdentityAndDiscovery(t *testing.T) {
	for _, kind := range []string{"header", "uuid", "ambiguous", "file_link", "directory_link", "unsupported_layout", "directory_replacement"} {
		t.Run(kind, func(t *testing.T) {
			home, path := receiptFixture(t, receiptHeader())
			uuid := receiptUUID
			switch kind {
			case "header":
				if e := os.WriteFile(path, []byte(strings.ReplaceAll(receiptHeader(), receiptUUID, "aaaaaaaa-2222-4333-8444-555555555555")), 0600); e != nil {
					t.Fatal(e)
				}
			case "uuid":
				uuid = strings.ToUpper("aaaaaaaa-2222-4333-8444-555555555555")
				newPath := strings.Replace(path, receiptUUID, uuid, 1)
				if e := os.Rename(path, newPath); e != nil {
					t.Fatal(e)
				}
				if e := os.WriteFile(newPath, []byte(strings.Replace(receiptHeader(), receiptUUID, uuid, 1)), 0600); e != nil {
					t.Fatal(e)
				}
			case "ambiguous":
				if e := os.WriteFile(filepath.Join(filepath.Dir(path), "rollout-second-"+receiptUUID+".jsonl"), []byte(receiptHeader()), 0600); e != nil {
					t.Fatal(e)
				}
			case "file_link", "directory_link":
				target := path
				if kind == "directory_link" {
					target = filepath.Dir(path)
				}
				moved := target + "-owned-original"
				if e := os.Rename(target, moved); e != nil {
					t.Fatal(e)
				}
				if e := os.Symlink(moved, target); e != nil {
					t.Skipf("native symlink unavailable: %v", e)
				}
			case "unsupported_layout":
				if e := os.Rename(filepath.Join(home, "sessions", "2026"), filepath.Join(home, "sessions", "archive")); e != nil {
					t.Fatal(e)
				}
			}
			var hook func(string)
			if kind == "directory_replacement" {
				hook = func(stage string) {
					if stage == "before-open:2026" {
						hook = nil
						if e := os.Rename(filepath.Join(home, "sessions", "2026"), filepath.Join(home, "sessions", "saved")); e != nil {
							t.Fatal(e)
						}
						if e := os.WriteFile(filepath.Join(home, "sessions", "2026"), []byte("not a directory"), 0600); e != nil {
							t.Fatal(e)
						}
					}
				}
			}
			r := newCodexReceipt(home, uuid, receiptClosing, codexReceiptLimits, func(stage string) {
				if hook != nil {
					hook(stage)
				}
			})
			if r != nil {
				r.Close()
				t.Fatal("unbound/ambiguous/linked/unsupported candidate armed")
			}
		})
	}
}

func TestCodexReceiptContinuity(t *testing.T) {
	for _, kind := range []string{"rotation", "shrink", "truncate_regrow", "same_inode_prefix", "ancestor_replace", "root_replace", "before_verify_prefix", "before_verify_rotation", "after_read_prefix", "binding_window_prefix", "target_rebind"} {
		t.Run(kind, func(t *testing.T) {
			base := receiptHeader() + receiptMessage(t, "user", "old text")
			home, path := receiptFixture(t, base)
			r := armReceipt(t, home)
			newText := receiptMessage(t, "user", receiptClosing)
			refusedDirectoryReplacement := func(dir string, before os.FileInfo, renameErr error) bool {
				t.Helper()
				if runtime.GOOS != "windows" || !os.IsPermission(renameErr) {
					return false
				}
				after, err := os.Lstat(dir)
				if err != nil || !after.IsDir() || !os.SameFile(before, after) {
					t.Fatalf("refused directory replacement changed its source identity: %v", err)
				}
				if _, err := os.Lstat(dir + ".old"); !os.IsNotExist(err) {
					t.Fatalf("refused directory replacement left a destination: %v", err)
				}
				body, err := os.ReadFile(path)
				if err != nil || string(body) != base {
					t.Fatalf("refused directory replacement changed the journal: %v", err)
				}
				receiptAppend(t, path, newText)
				if !r.Receipt(func() bool { return true }) || r.disabled {
					t.Fatal("unchanged reader lost its valid receipt after native directory-replacement refusal")
				}
				t.Log("native Windows denied directory replacement; unchanged-reader receipt passed; replacement detection was not exercised")
				return true
			}
			rewrite := func() {
				t.Helper()
				if e := os.WriteFile(path, []byte(strings.ReplaceAll(base, "old text", "bad text")+newText), 0600); e != nil {
					t.Fatal(e)
				}
			}
			switch kind {
			case "rotation":
				if e := os.Rename(path, path+".old"); e != nil {
					t.Fatal(e)
				}
				if e := os.WriteFile(path, []byte(base+newText), 0600); e != nil {
					t.Fatal(e)
				}
			case "root_replace":
				sessions := filepath.Join(home, "sessions")
				before, err := os.Lstat(sessions)
				if err != nil {
					t.Fatal(err)
				}
				if e := os.Rename(sessions, sessions+".old"); e != nil {
					if refusedDirectoryReplacement(sessions, before, e) {
						return
					}
					t.Fatal(e)
				}
				if e := os.Mkdir(sessions, 0700); e != nil {
					t.Fatal(e)
				}
			case "shrink", "truncate_regrow":
				if e := os.Truncate(path, 0); e != nil {
					t.Fatal(e)
				}
				if r.Receipt(func() bool { return true }) {
					t.Fatal("shrink proved receipt")
				}
				if kind == "truncate_regrow" {
					if e := os.WriteFile(path, []byte(base+newText), 0600); e != nil {
						t.Fatal(e)
					}
				}
			case "same_inode_prefix":
				rewrite()
			case "ancestor_replace":
				parent := filepath.Dir(path)
				before, err := os.Lstat(parent)
				if err != nil {
					t.Fatal(err)
				}
				if e := os.Rename(parent, parent+".old"); e != nil {
					if refusedDirectoryReplacement(parent, before, e) {
						return
					}
					t.Fatal(e)
				}
				if e := os.Mkdir(parent, 0700); e != nil {
					t.Fatal(e)
				}
				if e := os.WriteFile(path, []byte(base+newText), 0600); e != nil {
					t.Fatal(e)
				}
			default:
				receiptAppend(t, path, newText)
				if kind != "target_rebind" && kind != "binding_window_prefix" {
					stage := "before-verify"
					if kind == "after_read_prefix" {
						stage = "after-read"
					}
					r.hook = func(s string) {
						if s == stage {
							r.hook = nil
							if kind == "before_verify_rotation" {
								if e := os.Rename(path, path+".old"); e != nil {
									t.Fatal(e)
								}
								if e := os.WriteFile(path, []byte(base+newText), 0600); e != nil {
									t.Fatal(e)
								}
							} else {
								rewrite()
							}
						}
					}
				}
			}
			if r.Receipt(func() bool {
				if kind == "binding_window_prefix" {
					rewrite()
				}
				return kind != "target_rebind"
			}) || !r.disabled {
				t.Fatalf("observed %s did not permanently disable proof", kind)
			}
			if r.Receipt(func() bool { return true }) {
				t.Fatal("disabled reader revived")
			}
		})
	}
}

func TestCodexReceiptBounds(t *testing.T) {
	for _, kind := range []string{"snapshot", "record", "records", "entries", "bytes", "work"} {
		t.Run(kind, func(t *testing.T) {
			home, _ := receiptFixture(t, receiptHeader()+receiptMessage(t, "user", "old"))
			limits := codexReceiptLimits
			switch kind {
			case "snapshot":
				limits.snapshot = 10
			case "record":
				limits.record = 10
			case "records":
				limits.records = 1
			case "entries":
				limits.entries = 1
			case "bytes":
				limits.bytes = 10
			case "work":
				limits.work = -time.Nanosecond
			}
			if r := newCodexReceipt(home, receiptUUID, receiptClosing, limits, nil); r != nil {
				r.Close()
				t.Fatalf("%s bound ignored", kind)
			}
		})
	}
	// Exhaustion includes repeated whole-prefix verification, not just growth.
	home, path := receiptFixture(t, receiptHeader())
	r := armReceipt(t, home)
	r.left = 1
	receiptAppend(t, path, receiptMessage(t, "user", receiptClosing))
	if r.Receipt(func() bool { return true }) || !r.disabled {
		t.Fatal("cumulative byte bound ignored")
	}
}
