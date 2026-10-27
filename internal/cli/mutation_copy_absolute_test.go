package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func mutationAbsoluteSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("creating symlinks requires Windows privileges: %v", err)
		}
		t.Fatal(err)
	}
}

func TestMutationCopyAbsoluteSourceLinks(t *testing.T) {
	for _, mode := range []string{"untracked", "tracked", "inside-source", "indirect-inside-source", "missing", "external-file", "explicit-override", "tracked-explicit-override", "broken-explicit-override", "explicit-inside-source-refused"} {
		t.Run(mode, func(t *testing.T) {
			f := newMutationCopyFixture(t)
			// A trailing slash ignore does not hide a directory symlink from Git.
			if err := os.WriteFile(filepath.Join(f.source, ".gitignore"), []byte("ignored.txt\nnode_modules/\n"), 0600); err != nil {
				t.Fatal(err)
			}
			external := filepath.Join(f.root, "external dependencies with spaces")
			if err := os.MkdirAll(external, 0700); err != nil {
				t.Fatal(err)
			}
			target := external
			if mode == "inside-source" || mode == "explicit-inside-source-refused" {
				target = filepath.Join(f.source, "sub")
			}
			if mode == "indirect-inside-source" {
				target = filepath.Join(f.root, "external alias")
				mutationAbsoluteSymlink(t, filepath.Join(f.source, "sub"), target)
			}
			if mode == "missing" || mode == "broken-explicit-override" {
				target = filepath.Join(f.root, "missing dependency")
			}
			if mode == "external-file" {
				target = filepath.Join(external, "package data")
				if err := os.WriteFile(target, []byte("external contents must not be copied"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			rel := filepath.Join("windows", "popover", "node_modules")
			sourceLink := filepath.Join(f.source, rel)
			mutationAbsoluteSymlink(t, target, sourceLink)
			if mode == "tracked" || mode == "tracked-explicit-override" {
				fixtureGit(t, f, f.source, "add", "-f", "--", filepath.ToSlash(rel))
			}
			listed := fixtureGit(t, f, f.source, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
			if !strings.Contains(listed, filepath.ToSlash(rel)+"\x00") {
				t.Fatalf("git did not list source link: %q", listed)
			}
			dest := filepath.Join(f.root, "absolute link copy")
			args := []string{"--source", f.source, "--dest", dest}
			explicit := strings.Contains(mode, "explicit")
			wantTarget := target
			if explicit {
				wantTarget = filepath.Join(f.root, "override dependencies")
				if err := os.MkdirAll(wantTarget, 0700); err != nil {
					t.Fatal(err)
				}
				if mode == "explicit-inside-source-refused" {
					wantTarget = filepath.Join(f.source, "sub")
				}
				args = append(args, "--link", filepath.ToSlash(rel)+"="+wantTarget)
			}
			code, out, stderr := runMutationCopyCmd(t, f, f.root, nil, args...)
			t.Logf("mode=%s exit=%d stdout=%s stderr=%s", mode, code, out, stderr)
			refused := mode == "tracked" || mode == "inside-source" || mode == "indirect-inside-source" || mode == "missing" || mode == "explicit-inside-source-refused"
			if refused {
				if code != 2 || out != "" || !strings.Contains(stderr, "refusing ") {
					t.Fatalf("expected refusal: exit=%d stdout=%s stderr=%s", code, out, stderr)
				}
				if _, err := os.Lstat(dest); !os.IsNotExist(err) {
					t.Fatalf("refused destination remains: %v", err)
				}
				return
			}
			if code != 0 {
				t.Fatalf("exit=%d stdout=%s stderr=%s", code, out, stderr)
			}
			var result struct {
				Files int `json:"files"`
				Links []struct {
					Path, Target string
					Implicit     bool `json:"implicit"`
				} `json:"links"`
			}
			if err := json.Unmarshal([]byte(out), &result); err != nil {
				t.Fatal(err)
			}
			if len(result.Links) != 1 || result.Links[0].Path != rel || result.Links[0].Target != wantTarget || result.Links[0].Implicit == explicit {
				t.Fatalf("link metadata mismatch: %s", out)
			}
			if explicit && strings.Contains(out, `"implicit"`) {
				t.Fatalf("explicit JSON shape changed: %s", out)
			}
			link := filepath.Join(dest, rel)
			if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("copied link is not a symlink: %v %v", info, err)
			}
			if actual, err := os.Readlink(link); err != nil || actual != wantTarget {
				t.Fatalf("target=%q want=%q err=%v", actual, wantTarget, err)
			}
			if actual, err := os.Readlink(sourceLink); err != nil || actual != target {
				t.Fatalf("source link changed: %q %v", actual, err)
			}
			if result.Files != 3 {
				t.Fatalf("link target contents counted as files: files=%d stdout=%s", result.Files, out)
			}
			guard := guardFixture{root: f.root, source: f.source, copy: dest, env: f.env}
			guardCode, guardOut, guardErr := runMutationGuardFixture(t, guard, []string{dest, "--source", f.source}, nil)
			if guardCode != 0 || guardErr != "" || !strings.Contains(guardOut, "ok no-symlink-into-source") {
				t.Fatalf("guard exit=%d stdout=%s stderr=%s", guardCode, guardOut, guardErr)
			}
			t.Logf("guard exit=%d stdout=%s", guardCode, guardOut)
		})
	}
}
