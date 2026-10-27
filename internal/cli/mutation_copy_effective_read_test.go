package cli

import (
	"fmt"
	"github.com/djalmajr/soho/internal/platform"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestReview561Round2Boundary(t *testing.T) {
	for _, mode := range []string{"cargo-home-through-implicit-link", "case-alias-of-guard-input"} {
		t.Run(mode, func(t *testing.T) {
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
			f.env["PATH"] = bin
			f.env["CARGO_HOME"] = filepath.Join(f.root, "unused-cargo-home")
			if err := os.WriteFile(filepath.Join(f.source, "Cargo.toml"), []byte("[package]\nname = 'probe'\nversion = '0.1.0'\n"), 0600); err != nil {
				t.Fatal(err)
			}
			rel := filepath.Join(".CARGO", "config.toml")
			target := filepath.Join(f.root, "external-config.toml")
			payloadFile := target
			if mode == "cargo-home-through-implicit-link" {
				rel = "cargo-home"
				target = filepath.Join(f.root, "external-home")
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
				if mode == "case-alias-of-guard-input" {
					a, err := os.Stat(filepath.Join(f.source, ".cargo", "config.toml"))
					if err != nil {
						t.Skipf("case-sensitive fixture filesystem: %v", err)
					}
					b, err := os.Stat(filepath.Join(f.source, rel))
					if err != nil || !os.SameFile(a, b) {
						t.Skip("fixture spellings do not alias")
					}
					t.Log("metadata confirms .CARGO/config.toml and .cargo/config.toml are the same filesystem object")
				}
				dest := filepath.Join(f.root, fmt.Sprintf("copy-%d", i))
				overrides := platform.Env{}
				if mode == "cargo-home-through-implicit-link" {
					overrides["CARGO_HOME"] = filepath.Join(dest, rel)
				}
				code, out, stderr := runMutationCopyCmd(t, f, f.root, overrides, "--source", f.source, "--dest", dest)
				codes = append(codes, code)
				t.Logf("mode=%s transition=%d CARGO_HOME=%q payload=%q exit=%d stdout=%s stderr=%s", mode, i, overrides["CARGO_HOME"], payload, code, out, stderr)
				if code != 2 || out != "" {
					t.Errorf("guard input through implicit link must be refused before content read; got exit=%d stdout=%s stderr=%s", code, out, stderr)
				}
			}
			if codes[0] != codes[1] {
				t.Errorf("external contents still control the copy result: %v", codes)
			}
		})
	}
}
