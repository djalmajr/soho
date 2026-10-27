package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Port the reviewer's source-ancestor layout and controlled write path. Every
// write below an approved link stays inside this test's disposable fixture.
func TestReview561ClarifiedSourceAncestor(t *testing.T) {
	f := newMutationCopyFixture(t)
	sourceFile := filepath.Join(f.source, "tracked.txt")
	before, err := os.ReadFile(sourceFile)
	if err != nil {
		t.Fatal(err)
	}
	mutationAbsoluteSymlink(t, f.root, filepath.Join(f.source, "deps"))
	dest := filepath.Join(f.root, "copy")
	code, out, stderr := runMutationCopyCmd(t, f, f.root, nil, "--source", f.source, "--dest", dest)
	t.Logf("source=%s ancestor-target=%s copy=%s exit=%d stdout=%s stderr=%s", f.source, f.root, dest, code, out, stderr)
	if code != 2 || out != "" || !strings.Contains(stderr, "refusing symlink 'deps': target contains the source") {
		t.Errorf("expected early source-ancestor refusal: exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	if code == 0 {
		guard := guardFixture{root: f.root, source: f.source, copy: dest, env: f.env}
		gc, goOut, ge := runMutationGuardFixture(t, guard, []string{dest, "--source", f.source}, nil)
		t.Logf("public guard exit=%d stdout=%s stderr=%s", gc, goOut, ge)
		throughCopy := filepath.Join(dest, "deps", filepath.Base(f.source), "tracked.txt")
		resolved, err := filepath.EvalSymlinks(throughCopy)
		if err != nil {
			t.Fatal(err)
		}
		if resolved != sourceFile {
			t.Fatalf("fixture route=%s want=%s", resolved, sourceFile)
		}
		if gc == 0 {
			if err := os.WriteFile(throughCopy, []byte("review fixture mutation through accepted copy\n"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	after, err := os.ReadFile(sourceFile)
	if err != nil || string(after) != string(before) {
		t.Errorf("source fixture changed: before=%q after=%q err=%v", before, after, err)
	}
	if code != 0 {
		if _, err := os.Lstat(dest); !os.IsNotExist(err) {
			t.Errorf("refused destination remains: %v", err)
		}
	}
}

func TestMutationSourceAncestorExplicitAndGuard(t *testing.T) {
	for _, mode := range []string{"explicit-copy", "public-guard"} {
		t.Run(mode, func(t *testing.T) {
			f := newMutationCopyFixture(t)
			sourceFile := filepath.Join(f.source, "tracked.txt")
			before, err := os.ReadFile(sourceFile)
			if err != nil {
				t.Fatal(err)
			}
			dest := filepath.Join(f.root, "copy")
			if mode == "explicit-copy" {
				// The explicit-link HOME refusal is independent of source overlap.
				// Keep HOME outside R so this case reaches the guard's boundary.
				f.env["HOME"] = t.TempDir()
				f.env["USERPROFILE"] = f.env["HOME"]
				code, out, stderr := runMutationCopyCmd(t, f, f.root, nil, "--source", f.source, "--dest", dest, "--link", "deps="+f.root)
				t.Logf("mode=%s exit=%d stdout=%s stderr=%s", mode, code, out, stderr)
				if code != 1 || out != "" || !strings.Contains(stderr, "fail no-symlink-into-source: symlink deps -> ") {
					t.Errorf("explicit ancestor escaped guard: exit=%d stdout=%s stderr=%s", code, out, stderr)
				}
			} else {
				if err := os.Mkdir(dest, 0700); err != nil {
					t.Fatal(err)
				}
				mutationAbsoluteSymlink(t, f.root, filepath.Join(dest, "deps"))
				guard := guardFixture{root: f.root, source: f.source, copy: dest, env: f.env}
				code, out, stderr := runMutationGuardFixture(t, guard, []string{dest, "--source", f.source}, nil)
				t.Logf("mode=%s exit=%d stdout=%s stderr=%s", mode, code, out, stderr)
				if code != 1 || stderr != "" || !strings.Contains(out, "fail no-symlink-into-source: symlink deps -> ") {
					t.Errorf("public guard accepted ancestor: exit=%d stdout=%s stderr=%s", code, out, stderr)
				}
				if code == 0 {
					if err := os.WriteFile(filepath.Join(dest, "deps", filepath.Base(f.source), "tracked.txt"), []byte("unsafe guard approval\n"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			after, err := os.ReadFile(sourceFile)
			if err != nil || string(after) != string(before) {
				t.Errorf("source fixture changed: before=%q after=%q err=%v", before, after, err)
			}
		})
	}
}

func TestMutationCopyDestinationAncestorAndSibling(t *testing.T) {
	for _, mode := range []string{"copy-ancestor", "copy-self", "source-ancestor-only", "sibling"} {
		t.Run(mode, func(t *testing.T) {
			f := newMutationCopyFixture(t)
			outside := filepath.Join(f.root, "outside-source")
			if err := os.Mkdir(outside, 0700); err != nil {
				t.Fatal(err)
			}
			dest := filepath.Join(outside, "copy")
			target := outside
			if mode == "copy-self" {
				target = dest
			}
			if mode == "source-ancestor-only" {
				target = f.root
				dest = filepath.Join(t.TempDir(), "copy")
			}
			if mode == "sibling" {
				target = filepath.Join(f.root, "sibling")
				if err := os.Mkdir(target, 0700); err != nil {
					t.Fatal(err)
				}
			}
			mutationAbsoluteSymlink(t, target, filepath.Join(f.source, "deps"))
			before, err := os.ReadFile(filepath.Join(f.source, "tracked.txt"))
			if err != nil {
				t.Fatal(err)
			}
			code, out, stderr := runMutationCopyCmd(t, f, f.root, nil, "--source", f.source, "--dest", dest)
			t.Logf("mode=%s exit=%d stdout=%s stderr=%s", mode, code, out, stderr)
			if mode == "sibling" {
				if code != 0 || !strings.Contains(out, `"implicit":true`) {
					t.Errorf("sibling refused: exit=%d stdout=%s stderr=%s", code, out, stderr)
				}
				if actual, err := os.Readlink(filepath.Join(dest, "deps")); err != nil || actual != target {
					t.Errorf("sibling target=%q want=%q err=%v", actual, target, err)
				}
			} else {
				reason := "target contains the copy"
				if mode == "source-ancestor-only" {
					reason = "target contains the source"
				}
				if code != 2 || out != "" || !strings.Contains(stderr, reason) {
					t.Errorf("expected copy-ancestor refusal: exit=%d stdout=%s stderr=%s", code, out, stderr)
				}
			}
			after, err := os.ReadFile(filepath.Join(f.source, "tracked.txt"))
			if err != nil || string(before) != string(after) {
				t.Errorf("source changed: %q %v", after, err)
			}
		})
	}
}
