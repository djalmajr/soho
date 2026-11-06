package cli

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
)

// Pin the public JSON bytes independently of mutationCopyResult and decodedCopy.
func TestMutationCopyPrintPathDefaultJSONContract(t *testing.T) {
	f := newMutationCopyFixture(t)
	dest := filepath.Join(f.root, "json-copy")
	code, out, stderr := runMutationCopyCmd(t, f, f.root, nil, "--source", f.source, "--dest", dest)
	want := fmt.Sprintf("{\"copy\":%q,\"source\":%q,\"files\":3,\"excluded\":0,\"links\":[]}\n", dest, f.source)
	if code != 0 || out != want {
		t.Fatalf("exit=%d stdout=%q stderr=%q; want exact JSON %q", code, out, stderr, want)
	}
}

func TestMutationCopyPrintPathCanonicalConsumer(t *testing.T) {
	for _, alias := range []bool{false, true} {
		t.Run(fmt.Sprintf("symlink=%t", alias), func(t *testing.T) {
			f := newMutationCopyFixture(t)
			parent := filepath.Join(f.root, "cópia [literal] $x; & `")
			if err := os.Mkdir(parent, 0o700); err != nil {
				t.Fatal(err)
			}
			rawParent := filepath.Base(parent)
			if alias {
				rawParent = "alias"
				mutationAbsoluteSymlink(t, parent, filepath.Join(f.root, rawParent))
			}
			raw := filepath.Join(rawParent, "copy")
			dest := filepath.Join(parent, "copy")
			before := mutationCopyPrintPathSnapshot(t, f.source)
			code, out, stderr := runMutationCopyCmd(t, f, f.root, nil, "--source", f.source, "--dest", raw, "--print-path")
			if code != 0 || out != dest+"\n" {
				t.Fatalf("GOOS=%s raw=%q exit=%d stdout=%q stderr=%q; want %q", runtime.GOOS, raw, code, out, stderr, dest+"\n")
			}
			captured := strings.TrimSuffix(out, "\n")
			// Native Go consumer: use the capture itself, not the expected path.
			if err := os.Chdir(captured); err != nil {
				t.Fatal(err)
			}
			if data, err := os.ReadFile("tracked.txt"); err != nil || string(data) != "v1\n" {
				t.Fatalf("captured-directory sentinel=%q err=%v", data, err)
			}
			if err := os.WriteFile("tracked.txt", []byte("copy edited\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			mutationCopyPrintPathUnchanged(t, f.source, before)
			// The default JSON consumer reports exactly the same canonical path.
			if err := os.Chdir(f.root); err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(dest); err != nil { // fixture-owned directory only
				t.Fatal(err)
			}
			code, out, stderr = runMutationCopyCmd(t, f, f.root, nil, "--source", f.source, "--dest", raw)
			if code != 0 || decodedCopy(t, out).Copy != captured {
				t.Fatalf("JSON/path mismatch: exit=%d stdout=%q stderr=%q", code, out, stderr)
			}
		})
	}
}

func TestMutationCopyPrintPathUsage(t *testing.T) {
	for name, flags := range map[string][]string{
		"duplicate":     {"--print-path", "--print-path"},
		"value":         {"--print-path", "true"},
		"earlier-value": {"true", "--print-path"},
		"equals":        {"--print-path=true"},
		"empty-equals":  {"--print-path="},
	} {
		t.Run(name, func(t *testing.T) {
			f := newMutationCopyFixture(t)
			before := mutationCopyPrintPathSnapshot(t, f.source)
			dest := filepath.Join(f.root, "must-not-exist")
			args := append([]string{"--source", f.source, "--dest", dest}, flags...)
			code, out, stderr := runMutationCopyCmd(t, f, f.root, nil, args...)
			if code != 2 || out != "" || !strings.Contains(stderr, "usage: mutation-copy") {
				t.Fatalf("argv=%q exit=%d stdout=%q stderr=%q", args, code, out, stderr)
			}
			if _, err := os.Stat(dest); !os.IsNotExist(err) {
				t.Fatalf("usage created destination: %v", err)
			}
			mutationCopyPrintPathUnchanged(t, f.source, before)
		})
	}
}

func TestMutationCopyPrintPathFailures(t *testing.T) {
	for _, pathMode := range []bool{false, true} {
		for _, kind := range []string{"usage", "source", "destination", "io", "guard", "repository"} {
			t.Run(fmt.Sprintf("path=%t/%s", pathMode, kind), func(t *testing.T) {
				f := newMutationCopyFixture(t)
				sourceRoot := f.source
				registry := t.TempDir()
				f.env["SOHO_REGISTRY_DIR"] = registry
				dest := filepath.Join(f.root, "refused-copy")
				args := []string{"--source", f.source, "--dest", dest}
				wantCode := 2
				switch kind {
				case "usage":
					args = append(args, "--exclude", ":bad")
				case "source":
					args[1] = filepath.Join(f.root, "missing")
				case "destination":
					if err := os.Mkdir(dest, 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(dest, "sentinel"), []byte("caller\n"), 0o600); err != nil {
						t.Fatal(err)
					}
				case "io":
					// A valid file and a requested link at the same path fail with EEXIST.
					external := t.TempDir()
					args = append(args, "--link", "tracked.txt="+external)
					wantCode = 4
				case "guard":
					f.env["CARGO_TARGET_DIR"] = filepath.Join(f.source, "target")
					wantCode = 1
				case "repository":
					dest = filepath.Join(f.source, "nested-copy")
					args[3] = dest
					f.source = filepath.Join(f.source, "sub")
					args[1] = f.source
					wantCode = 1
				}
				before := mutationCopyPrintPathSnapshot(t, sourceRoot)
				if pathMode {
					args = append(args, "--print-path")
				}
				code, out, stderr := runMutationCopyCmd(t, f, f.root, nil, args...)
				if code != wantCode || out != "" || stderr == "" {
					t.Fatalf("argv=%q exit=%d stdout=%q stderr=%q; want exit=%d and empty stdout", args, code, out, stderr, wantCode)
				}
				if kind == "destination" {
					if data, err := os.ReadFile(filepath.Join(dest, "sentinel")); err != nil || string(data) != "caller\n" {
						t.Fatalf("caller sentinel=%q err=%v", data, err)
					}
				} else if _, err := os.Stat(dest); !os.IsNotExist(err) {
					t.Fatalf("created destination remains: %v", err)
				}
				mutationCopyPrintPathNoEntries(t, registry)
				mutationCopyPrintPathUnchanged(t, sourceRoot, before)
			})
		}
	}
}

func TestMutationCopyPrintPathNewlines(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows disallows CR/LF filenames; printable-path validation is tested separately")
	}
	for _, newline := range []struct{ name, value string }{{"CR", "\r"}, {"LF", "\n"}} {
		for _, ownership := range []string{"created", "caller", "temporary", "resolved-alias"} {
			t.Run(newline.name+"/"+ownership, func(t *testing.T) {
				f := newMutationCopyFixture(t)
				registry := t.TempDir()
				f.env["SOHO_REGISTRY_DIR"] = registry
				dest := filepath.Join(f.root, "copy"+newline.value+"name")
				args := []string{"--source", f.source, "--print-path"}
				if ownership == "temporary" {
					f.tmp = filepath.Join(f.root, "tmp"+newline.value+"name")
					if err := os.Mkdir(f.tmp, 0o700); err != nil {
						t.Fatal(err)
					}
					f.env["TMPDIR"] = f.tmp
				} else {
					if ownership == "caller" || ownership == "resolved-alias" {
						if err := os.Mkdir(dest, 0o700); err != nil {
							t.Fatal(err)
						}
					}
					raw := dest
					if ownership == "resolved-alias" {
						raw = filepath.Join(f.root, "alias")
						mutationAbsoluteSymlink(t, dest, raw)
					}
					args = append(args, "--dest", raw)
				}
				before := mutationCopyPrintPathSnapshot(t, f.source)
				markerCalls := 0
				oldMarker := writeOriginMarker
				writeOriginMarker = func(path, content string) error { markerCalls++; return oldMarker(path, content) }
				t.Cleanup(func() { writeOriginMarker = oldMarker })
				code, out, stderr := runMutationCopyCmd(t, f, f.root, nil, args...)
				if code != 2 || out != "" || !strings.Contains(stderr, "CR or LF") {
					t.Fatalf("argv=%q exit=%d stdout=%q stderr=%q; want newline refusal", args, code, out, stderr)
				}
				if markerCalls != 0 {
					t.Fatalf("origin marker attempted before refusal: %d", markerCalls)
				}
				if ownership == "caller" || ownership == "resolved-alias" {
					mutationCopyPrintPathNoEntries(t, dest)
				} else if ownership == "created" {
					if _, err := os.Stat(dest); !os.IsNotExist(err) {
						t.Fatalf("command-created destination remains: %v", err)
					}
				}
				mutationCopyPrintPathNoEntries(t, registry)
				mutationCopyPrintPathNoEntries(t, f.tmp)
				mutationCopyPrintPathUnchanged(t, f.source, before)
			})
		}
		t.Run(newline.name+"/JSON-compatible", func(t *testing.T) {
			f := newMutationCopyFixture(t)
			dest := filepath.Join(f.root, "json"+newline.value+"copy")
			code, out, stderr := runMutationCopyCmd(t, f, f.root, nil, "--source", f.source, "--dest", dest)
			var got map[string]json.RawMessage
			if code != 0 || json.Unmarshal([]byte(out), &got) != nil || decodedCopy(t, out).Copy != dest {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
			}
			if data, err := os.ReadFile(filepath.Join(dest, "tracked.txt")); err != nil || string(data) != "v1\n" {
				t.Fatalf("JSON copy sentinel=%q err=%v", data, err)
			}
		})
	}
}

func TestMutationCopyPrintPathBestEffortRegistration(t *testing.T) {
	f := newMutationCopyFixture(t)
	f.env["SOHO_REGISTRY_DIR"] = t.TempDir()
	old := upsertCopies
	calls := 0
	upsertCopies = func(sd string, row core.CopyRow) error {
		calls++
		if row.Source != f.source || row.Origin != "mutation-copy" {
			t.Fatalf("registration row=%+v", row)
		}
		return errors.New("fixture registry unavailable")
	}
	t.Cleanup(func() { upsertCopies = old })
	dest := filepath.Join(f.root, "registered-copy")
	code, out, stderr := runMutationCopyCmd(t, f, f.root, nil, "--source", f.source, "--dest", dest, "--print-path")
	if code != 0 || out != dest+"\n" || calls != 1 || !strings.Contains(stderr, "copy not registered (fixture registry unavailable)") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
}

func TestMutationCopyPrintPathRegistrationAndPolicies(t *testing.T) {
	for _, pathMode := range []bool{false, true} {
		t.Run(fmt.Sprintf("path=%t", pathMode), func(t *testing.T) {
			f := newMutationCopyFixture(t)
			registry, dependency := t.TempDir(), t.TempDir()
			f.env["SOHO_REGISTRY_DIR"] = registry
			dest := filepath.Join(f.root, "policy-copy")
			args := []string{"--source", f.source, "--dest", dest, "--exclude", "tracked.txt", "--link", "deps=" + dependency}
			if pathMode {
				args = append(args, "--print-path")
			}
			code, out, stderr := runMutationCopyCmd(t, f, f.root, nil, args...)
			if code != 0 {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
			}
			if pathMode {
				if out != dest+"\n" {
					t.Fatalf("stdout=%q", out)
				}
			} else {
				var result struct {
					Copy     string `json:"copy"`
					Files    int    `json:"files"`
					Excluded int    `json:"excluded"`
					Links    []struct {
						Path     string `json:"path"`
						Target   string `json:"target"`
						Implicit bool   `json:"implicit"`
					} `json:"links"`
				}
				if err := json.Unmarshal([]byte(out), &result); err != nil {
					t.Fatal(err)
				}
				resolvedDependency, err := filepath.EvalSymlinks(dependency)
				if err != nil {
					t.Fatal(err)
				}
				if result.Copy != dest || result.Files != 2 || result.Excluded != 1 || len(result.Links) != 1 || result.Links[0].Path != "deps" || result.Links[0].Target != resolvedDependency || result.Links[0].Implicit {
					t.Fatalf("result=%+v", result)
				}
			}
			rows, err := core.ReadCopies(registry)
			if err != nil || len(rows) != 1 {
				t.Fatalf("registry rows=%+v err=%v", rows, err)
			}
			if rows[0].Path != dest || rows[0].Source != f.source || rows[0].Origin != "mutation-copy" {
				t.Fatalf("registry row=%+v", rows[0])
			}
			if _, err := os.Stat(filepath.Join(dest, "tracked.txt")); !os.IsNotExist(err) {
				t.Fatalf("excluded file copied: %v", err)
			}
			if info, err := os.Lstat(filepath.Join(dest, "deps")); err != nil || info.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("dependency link=%v err=%v", info, err)
			}
		})
	}
}

func TestMutationCopyPrintPathMarkerWarning(t *testing.T) {
	f := newMutationCopyFixture(t)
	old := writeOriginMarker
	writeOriginMarker = func(path, content string) error { return errors.New("fixture marker unavailable") }
	t.Cleanup(func() { writeOriginMarker = old })
	code, out, stderr := runMutationCopyCmd(t, f, f.root, nil, "--source", f.source, "--print-path")
	entries, err := os.ReadDir(f.tmp)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary entries=%v err=%v", entries, err)
	}
	dest := filepath.Join(f.tmp, entries[0].Name())
	if code != 0 || out != dest+"\n" || !strings.Contains(stderr, "origin marker not written (fixture marker unavailable)") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	if data, err := os.ReadFile(filepath.Join(strings.TrimSuffix(out, "\n"), "tracked.txt")); err != nil || string(data) != "v1\n" {
		t.Fatalf("copy sentinel=%q err=%v", data, err)
	}
}

// Windows rejects these filenames before directory creation. Exercise the
// same validation predicate without weakening the native filesystem rules.
func TestMutationCopyPrintPathValidation(t *testing.T) {
	for _, path := range []string{"copy\rname", "copy\nname", "\ncopy", "copy\r", "copy\r\nname"} {
		if mutationCopyPrintablePath(path) {
			t.Errorf("accepted %q", path)
		}
	}
	if !mutationCopyPrintablePath("cópia [literal] $x; & `") {
		t.Fatal("refused representable path")
	}
}

func mutationCopyPrintPathSnapshot(t *testing.T, root string) map[string][sha256.Size]byte {
	t.Helper()
	result := map[string][sha256.Size]byte{}
	gitMetadata := filepath.Join(root, ".git")
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Snapshot working files, not Git's transient locks and metadata.
		// Only the fixture root's metadata entry is excluded; nested .git
		// entries and every other dotfile remain part of the assertion.
		if path == gitMetadata {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		result[path] = sha256.Sum256(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func mutationCopyPrintPathUnchanged(t *testing.T, root string, before map[string][sha256.Size]byte) {
	t.Helper()
	if after := mutationCopyPrintPathSnapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("source fixture bytes changed")
	}
}

func mutationCopyPrintPathNoEntries(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("expected existing empty directory %q: entries=%v err=%v", dir, entries, err)
	}
}
