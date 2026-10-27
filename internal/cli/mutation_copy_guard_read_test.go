package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Port the reviewer's real-Git/public-copy content transition; both external
// payloads must now be refused before the copy can invoke the guard.
func TestReview561ExternalConfigRead(t *testing.T) {
	for _, rel := range []string{filepath.Join(".cargo", "config.toml"), ".cargo", filepath.Join(".cargo", "config"), "Cargo.toml"} {
		t.Run(rel, func(t *testing.T) {
			f := newMutationCopyFixture(t)
			git, err := exec.LookPath("git")
			if err != nil {
				t.Fatal(err)
			}
			bin := filepath.Join(f.root, "git-only")
			if err := os.Mkdir(bin, 0700); err != nil {
				t.Fatal(err)
			}
			mutationAbsoluteSymlink(t, git, filepath.Join(bin, filepath.Base(git)))
			f.env["PATH"], f.env["CARGO_HOME"] = bin, filepath.Join(f.root, "cargo-home")
			if rel != "Cargo.toml" {
				if err := os.WriteFile(filepath.Join(f.source, "Cargo.toml"), []byte("[package]\nname = 'probe'\nversion = '0.1.0'\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			target := filepath.Join(f.root, "external-config.toml")
			payloadFile := target
			if rel == ".cargo" {
				target = filepath.Join(f.root, "external-cargo")
				if err := os.Mkdir(target, 0700); err != nil {
					t.Fatal(err)
				}
				payloadFile = filepath.Join(target, "config.toml")
			}
			mutationAbsoluteSymlink(t, target, filepath.Join(f.source, rel))
			var codes []int
			for i, targetDir := range []string{filepath.Join(f.root, "outside-build"), filepath.Join(f.source, "target")} {
				payload := fmt.Sprintf("[build]\ntarget-dir = %q\n", targetDir)
				if err := os.WriteFile(payloadFile, []byte(payload), 0600); err != nil {
					t.Fatal(err)
				}
				dest := filepath.Join(f.root, fmt.Sprintf("config-copy-%d", i))
				code, out, stderr := runMutationCopyCmd(t, f, f.root, nil, "--source", f.source, "--dest", dest)
				codes = append(codes, code)
				t.Logf("path=%s external-content-transition=%d payload=%q exit=%d stdout=%s stderr=%s", rel, i, payload, code, out, stderr)
				want := "mutation-copy: refusing symlink '" + filepath.ToSlash(rel) + "': a path the guard reads\n"
				if code != 2 || out != "" || stderr != want {
					t.Errorf("want exit=2, no JSON, guard-read refusal; exit=%d stdout=%q stderr=%q", code, out, stderr)
				}
				if _, err := os.Lstat(dest); !os.IsNotExist(err) {
					t.Errorf("destination survives refused copy: %v", err)
				}
			}
			if codes[0] != codes[1] {
				t.Errorf("copy result depends on external content: exits=%v", codes)
			}
		})
	}
}

func TestMutationCopyGuardReadSiblingAllowed(t *testing.T) {
	f := newMutationCopyFixture(t)
	target := filepath.Join(f.root, "external-config")
	if err := os.WriteFile(target, []byte("arbitrary data"), 0600); err != nil {
		t.Fatal(err)
	}
	rel := filepath.Join(".cargo", "config.toml.backup")
	mutationAbsoluteSymlink(t, target, filepath.Join(f.source, rel))
	code, out, stderr := runMutationCopyCmd(t, f, f.root, nil, "--source", f.source, "--dest", filepath.Join(f.root, "sibling-copy"))
	if code != 0 || !strings.Contains(out, `"implicit":true`) {
		t.Fatalf("non-reader sibling rejected: exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
}
