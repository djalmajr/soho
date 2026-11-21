package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
)

func TestMutationCopySiblingPlacement(t *testing.T) {
	for _, tc := range []struct {
		name, link string
		depth      int
		printPath  bool
	}{
		{name: "single", link: "../shared", depth: 1},
		{name: "double", link: "../../x", depth: 2},
		{name: "triple", link: "../../../libs/core", depth: 3, printPath: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMutationCopyFixture(t)
			deps := filepath.Join(f.root, "external dependencies")
			probeMutationCopySiblingSymlink(t, deps)
			if err := os.WriteFile(filepath.Join(deps, "package.txt"), []byte("fixture"), 0o600); err != nil {
				t.Fatal(err)
			}
			args := []string{"--source", f.source, "--link", tc.link + "=" + deps}
			if tc.depth > 1 {
				args = append(args, "--link", "../near="+deps, "--link", "local="+deps)
			}
			if tc.printPath {
				args = append(args, "--print-path")
			}
			code, out, stderr := runMutationCopyCmd(t, f, f.root, nil, args...)
			if code != 0 {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
			}
			var copyPath, reportedRoot string
			if tc.printPath {
				copyPath = strings.TrimSuffix(out, "\n")
				if !filepath.IsAbs(copyPath) || out != copyPath+"\n" {
					t.Fatalf("--print-path stdout=%q", out)
				}
			} else {
				var result struct {
					Copy, Root string
					Links      []mutationCopyLink
				}
				if err := json.Unmarshal([]byte(out), &result); err != nil {
					t.Fatal(err)
				}
				copyPath, reportedRoot = result.Copy, result.Root
				if len(result.Links) == 0 || result.Links[0].Path != filepath.FromSlash(tc.link) || result.Links[0].Target != deps || result.Links[0].Implicit {
					t.Fatalf("links=%+v", result.Links)
				}
			}
			root, sourcePrefix := copyPath, f.source
			for i := 0; i < tc.depth; i++ {
				if filepath.Base(root) != filepath.Base(sourcePrefix) {
					t.Fatalf("copy path component %q differs from source %q", root, sourcePrefix)
				}
				root, sourcePrefix = filepath.Dir(root), filepath.Dir(sourcePrefix)
			}
			if filepath.Dir(root) != f.tmp || !strings.HasPrefix(filepath.Base(root), "soho-mutation-") || (!tc.printPath && reportedRoot != root) {
				t.Fatalf("copy=%q root=%q reported root=%q temp=%q", copyPath, root, reportedRoot, f.tmp)
			}
			for _, rel := range []string{tc.link, "../near", "local"} {
				if rel != tc.link && tc.depth == 1 {
					continue
				}
				path := filepath.Join(copyPath, rel)
				info, err := os.Lstat(path)
				if err != nil || info.Mode()&os.ModeSymlink == 0 {
					t.Fatalf("link %q: lstat=%v err=%v; want a symlink", path, info, err)
				}
				info, err = os.Stat(path)
				if err != nil || !info.IsDir() {
					t.Fatalf("link %q to %q: stat=%v err=%v; want a directory", path, deps, info, err)
				}
				if target, err := os.Readlink(path); err != nil || target != deps {
					t.Fatalf("link %q target=%q err=%v", path, target, err)
				}
				if data, err := os.ReadFile(filepath.Join(path, "package.txt")); err != nil || string(data) != "fixture" {
					t.Fatalf("read through link %q data=%q err=%v", path, data, err)
				}
			}
			if data, err := os.ReadFile(filepath.Join(copyPath, "tracked.txt")); err != nil || string(data) != "v1\n" {
				t.Fatalf("copy payload=%q err=%v", data, err)
			}
			if project, source, ok := readOriginMarker(originMarkerPath(root)); !ok || project != filepath.Join(f.source, ".git") || source != f.source {
				t.Fatalf("root marker project=%q source=%q valid=%t", project, source, ok)
			}
			assertMutationCopySiblingAbsent(t, originMarkerPath(copyPath))
			guard := guardFixture{root: f.root, source: f.source, copy: copyPath, env: f.env}
			code, out, stderr = runMutationGuardFixture(t, guard, []string{copyPath, "--source", f.source}, nil)
			if code != 0 || stderr != "" || !strings.Contains(out, "ok no-symlink-into-source\n") {
				t.Fatalf("mutation-guard exit=%d stdout=%q stderr=%q", code, out, stderr)
			}
		})
	}
}

func TestMutationCopySiblingPathRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, rel, reason string
		dest              bool
	}{
		{name: "depth-four", rel: "../../../../x"},
		{name: "interior-parent", rel: "../a/../b"},
		{name: "interior-current", rel: "../a/./b"},
		{name: "parent-alone", rel: ".."},
		{name: "empty-leaf", rel: "../"},
		{name: "empty-segment", rel: "..//shared"},
		{name: "absolute", rel: "/absolute", reason: "relpath must be relative"},
		{name: "explicit-destination", rel: "../shared", dest: true, reason: "a link above the copy needs the automatic placement (omit --dest)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMutationCopyFixture(t)
			copiesDir := filepath.Join(f.root, "not yet created", "copies")
			dest := filepath.Join(f.root, "explicit", "copy")
			target := filepath.Join(f.root, "dependencies")
			if err := os.Mkdir(target, 0o700); err != nil {
				t.Fatal(err)
			}
			raw := tc.rel + "=" + target
			args := []string{"--source", f.source, "--link", raw}
			if tc.dest {
				args = append(args, "--dest", dest)
			}
			code, out, stderr := runMutationCopyCmd(t, f, f.root, map[string]string{"SOHO_COPIES_DIR": copiesDir}, args...)
			reason := tc.reason
			if reason == "" {
				reason = "relpath must not escape with '..'"
			}
			want := fmt.Sprintf("mutation-copy: refusing link '%s': %s\n", raw, reason)
			if code != 2 || out != "" || stderr != want {
				t.Fatalf("exit=%d stdout=%q stderr=%q; want exit 2 and %q", code, out, stderr, want)
			}
			assertMutationCopySiblingAbsent(t, filepath.Dir(copiesDir), filepath.Dir(dest))
			mutationCopyPrintPathNoEntries(t, f.tmp)
		})
	}
}

func TestMutationCopySiblingRootContainment(t *testing.T) {
	root := filepath.Join(t.TempDir(), "soho-mutation-fixture")
	copyPath := filepath.Join(root, "worktree")
	for _, rel := range []string{"..", "../../outside", "../../soho-mutation-fixture-other/shared"} {
		if path, err := mutationCopyRootLink(root, copyPath, rel); path != "" || err == nil {
			t.Errorf("joined link %q returned path=%q err=%v; want containment refusal", rel, path, err)
		}
	}
	if path, err := mutationCopyRootLink(root, copyPath, "../shared"); path != "shared" || err != nil {
		t.Fatalf("contained sibling returned path=%q err=%v; want shared", path, err)
	}
}

func TestMutationCopySiblingResolvedParentContainment(t *testing.T) {
	base := t.TempDir()
	owned := filepath.Join(base, "original-name")
	moved := filepath.Join(base, "moved")
	target := filepath.Join(base, "dependencies")
	for _, path := range []string{moved, target} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// Simulate the pathname alias that could exist after a root has moved
	// while the open directory handle still refers to the same inode.
	mutationAbsoluteSymlink(t, moved, owned)
	handle, err := os.OpenRoot(moved)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	link := mutationCopyLink{Path: "shared", Target: target}
	err = mutationCopyCreateLink(handle, owned, link.Path, link)
	if err == nil || !strings.Contains(err.Error(), "link parent is outside the automatic copy root") {
		t.Fatalf("link creation at %q returned %v; want resolved-parent refusal", owned, err)
	}
	assertMutationCopySiblingAbsent(t, filepath.Join(moved, link.Path))
}

func TestMutationCopySiblingTargetRefusals(t *testing.T) {
	for _, name := range []string{"source", "home", "filesystem root", "contains root", "contains source", "missing", "file"} {
		t.Run(name, func(t *testing.T) {
			f := newMutationCopyFixture(t)
			target, reason := f.source, "target is inside the source"
			switch name {
			case "home":
				target, reason = f.home, "target is HOME or above it"
			case "filesystem root":
				target, reason = filepath.VolumeName(f.root)+string(filepath.Separator), "target is the filesystem root"
			case "contains root":
				target, reason = f.tmp, "target contains the copy"
			case "contains source":
				target, reason = f.root, "target contains the source"
				// Put HOME and the copy elsewhere to isolate source ancestry.
				f.env["TMPDIR"] = t.TempDir()
				otherHome := filepath.Join(f.env["TMPDIR"], "home")
				if err := os.Mkdir(otherHome, 0o700); err != nil {
					t.Fatal(err)
				}
				f.env["HOME"], f.env["USERPROFILE"] = otherHome, otherHome
			case "missing":
				target, reason = filepath.Join(f.root, "missing"), "target does not exist or is not a directory"
			case "file":
				target, reason = filepath.Join(f.tmp, "fixture file"), "target does not exist or is not a directory"
				if err := os.WriteFile(target, []byte("fixture"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var marker string
			oldMarker := writeOriginMarker
			writeOriginMarker = func(path, content string) error {
				marker = path
				return oldMarker(path, content)
			}
			t.Cleanup(func() { writeOriginMarker = oldMarker })
			code, out, stderr := runMutationCopyCmd(t, f, f.root, nil, "--source", f.source, "--link", "../shared="+target)
			if code != 2 || out != "" || !strings.Contains(stderr, reason) || marker == "" {
				t.Fatalf("exit=%d stdout=%q stderr=%q marker=%q; want refusal %q after root creation", code, out, stderr, marker, reason)
			}
			assertMutationCopySiblingAbsent(t, strings.TrimSuffix(marker, ".soho-origin"), marker)
			if name == "file" {
				if data, err := os.ReadFile(target); err != nil || string(data) != "fixture" {
					t.Fatalf("target file changed: %q %v", data, err)
				}
			}
		})
	}
}

func TestMutationCopySiblingFailureCleanup(t *testing.T) {
	for _, name := range []string{"copy error", "link collision", "symlink parent"} {
		t.Run(name, func(t *testing.T) {
			f := newMutationCopyFixture(t)
			deps := filepath.Join(f.root, "external")
			if name == "copy error" {
				if err := os.Mkdir(deps, 0o700); err != nil {
					t.Fatal(err)
				}
			} else {
				probeMutationCopySiblingSymlink(t, deps)
			}
			keep := filepath.Join(f.tmp, "unrelated.soho-origin")
			if err := os.WriteFile(keep, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			link := "../shared=" + deps
			if name == "link collision" {
				link = "../" + filepath.Base(f.source) + "=" + deps
			}
			var marker string
			oldMarker := writeOriginMarker
			writeOriginMarker = func(path, content string) error {
				marker = path
				if name == "copy error" {
					copyPath := filepath.Join(strings.TrimSuffix(path, ".soho-origin"), filepath.Base(f.source))
					if err := os.MkdirAll(copyPath, 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(copyPath, "sub"), []byte("cannot mkdir here"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				return oldMarker(path, content)
			}
			t.Cleanup(func() { writeOriginMarker = oldMarker })
			args := []string{"--source", f.source, "--link", link, "--print-path"}
			if name == "symlink parent" {
				args = append(args, "--link", "../shared/new/child="+deps)
			}
			code, out, stderr := runMutationCopyCmd(t, f, f.root, nil, args...)
			if code != 4 || out != "" || stderr == "" || marker == "" {
				t.Fatalf("exit=%d stdout=%q stderr=%q marker=%q; want copy/link I/O error", code, out, stderr, marker)
			}
			if name == "link collision" && !strings.Contains(stderr, "link destination already exists") {
				t.Fatalf("existing entry should be refused before symlink creation, stderr=%q", stderr)
			}
			assertMutationCopySiblingAbsent(t, marker, strings.TrimSuffix(marker, ".soho-origin"))
			mutationCopyPrintPathNoEntries(t, deps)
			if data, err := os.ReadFile(keep); err != nil || string(data) != "keep" {
				t.Fatalf("unrelated sibling changed: %q %v", data, err)
			}
		})
	}
}

func TestMutationCopySiblingLifecycle(t *testing.T) {
	for _, registered := range []bool{true, false} {
		t.Run(fmt.Sprintf("registered=%t", registered), func(t *testing.T) {
			f := gcEmptyFixture(t)
			f.cwd = f.worktree
			deps := filepath.Join(f.root, "external")
			probeMutationCopySiblingSymlink(t, deps)
			keep := filepath.Join(deps, "keep")
			if err := os.WriteFile(keep, []byte("dependency"), 0o600); err != nil {
				t.Fatal(err)
			}
			code, out, stderr := f.run(t, f.env, "mutation-copy", "--link", "../shared="+deps, "--print-path")
			if code != 0 {
				t.Fatalf("mutation-copy exit=%d stdout=%q stderr=%q", code, out, stderr)
			}
			copyPath := strings.TrimSuffix(out, "\n")
			root := filepath.Dir(copyPath)
			rows := f.copiesRows(t)
			if len(rows) != 1 || rows[0].Path != root {
				t.Fatalf("copies rows=%+v; want one for root %q", rows, root)
			}
			code, out, stderr = f.run(t, f.env, "copies")
			if code != 0 || stderr != "" || !strings.HasPrefix(out, root+"  ") || strings.Contains(out, copyPath) {
				t.Fatalf("copies exit=%d stdout=%q stderr=%q; want root %q", code, out, stderr, root)
			}
			past := time.Now().Add(-6 * time.Hour)
			if err := os.Chtimes(root, past, past); err != nil {
				t.Fatal(err)
			}
			apply := []string{"gc", "--yes"}
			if registered {
				rows[0].Created = core.FrictionISO(past)
			} else {
				rows = nil
				apply = append(apply, "--include-unregistered")
			}
			if err := core.WriteCopies(f.stateDir(t), rows); err != nil {
				t.Fatal(err)
			}
			code, out, stderr = f.run(t, f.env, "gc")
			if code != 0 || stderr != "" || strings.Contains(out, copyPath) || (registered && !strings.Contains(out, "would remove "+root+"  ")) ||
				(!registered && (!strings.Contains(out, root+"  ") || !strings.Contains(out, "this project"))) {
				t.Fatalf("gc preview exit=%d stdout=%q stderr=%q; want root %q", code, out, stderr, root)
			}
			code, out, stderr = f.run(t, f.env, apply...)
			if code != 0 || stderr != "" || !strings.Contains(out, "removed "+root+"  ") || len(f.copiesRows(t)) != 0 {
				t.Fatalf("gc apply exit=%d stdout=%q stderr=%q; want removal of %q", code, out, stderr, root)
			}
			assertMutationCopySiblingAbsent(t, root, originMarkerPath(root))
			if data, err := os.ReadFile(keep); err != nil || string(data) != "dependency" {
				t.Fatalf("external dependency changed: %q %v", data, err)
			}
		})
	}
}

func probeMutationCopySiblingSymlink(t *testing.T, target string) {
	t.Helper()
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(t.TempDir(), "symlink")
	if err := os.Symlink(target, probe); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("directory symlinks unavailable: %v", err)
		}
		t.Fatal(err)
	}
}

func assertMutationCopySiblingAbsent(t *testing.T, paths ...string) {
	t.Helper()
	for _, path := range paths {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("expected absent path %q; lstat: %v", path, err)
		}
	}
}
