package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMutationCopyEffectiveReadControls(t *testing.T) {
	for _, mode := range []string{"unset", "elsewhere", "relative-inside", "relative-elsewhere", "config-file", "case-alias", "prefix-sibling"} {
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
			delete(f.env, "CARGO_HOME")
			if err := os.WriteFile(filepath.Join(f.source, "Cargo.toml"), []byte("[package]\nname='fixture'\nversion='0.1.0'\n"), 0600); err != nil {
				t.Fatal(err)
			}
			external := filepath.Join(f.root, "external cargo home")
			if err := os.Mkdir(external, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(external, "config.toml"), []byte(fmt.Sprintf("[build]\ntarget-dir = %q\n", filepath.Join(f.source, "target"))), 0600); err != nil {
				t.Fatal(err)
			}
			dest := filepath.Join(f.root, "copy")
			rel, target := "cargo-home", external
			want := 0
			switch mode {
			case "elsewhere":
				f.env["CARGO_HOME"] = filepath.Join(f.root, "unused-home")
			case "relative-inside":
				f.env["CARGO_HOME"] = filepath.Join("copy", "cargo-home")
				want = 2
			case "relative-elsewhere":
				f.env["CARGO_HOME"] = "unused-home"
			case "config-file":
				rel = filepath.Join("cargo-home", "config.toml")
				target = filepath.Join(external, "config.toml")
				f.env["CARGO_HOME"] = filepath.Join(dest, "cargo-home")
				want = 2
			case "case-alias":
				rel = "Cargo-Home"
				f.env["CARGO_HOME"] = filepath.Join(dest, "cargo-home")
				if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
					want = 2
				}
			case "prefix-sibling":
				rel = "cargo-home.backup"
				f.env["CARGO_HOME"] = filepath.Join(dest, "cargo-home")
			}
			mutationAbsoluteSymlink(t, target, filepath.Join(f.source, rel))
			code, out, stderr := runMutationCopyCmd(t, f, f.root, nil, "--source", f.source, "--dest", dest)
			t.Logf("mode=%s CARGO_HOME=%q exit=%d stdout=%s stderr=%s", mode, f.env["CARGO_HOME"], code, out, stderr)
			if code != want {
				t.Fatalf("exit=%d want=%d stdout=%s stderr=%s", code, want, out, stderr)
			}
			if want == 2 {
				if out != "" || stderr != "mutation-copy: refusing symlink '"+filepath.ToSlash(rel)+"': a path the guard reads\n" {
					t.Fatalf("wrong refusal: stdout=%s stderr=%s", out, stderr)
				}
				if _, err := os.Lstat(dest); !os.IsNotExist(err) {
					t.Fatalf("destination remains: %v", err)
				}
			} else {
				if !strings.Contains(out, `"implicit":true`) {
					t.Fatalf("implicit metadata missing: %s", out)
				}
				if actual, err := os.Readlink(filepath.Join(dest, rel)); err != nil || actual != target {
					t.Fatalf("link=%q want=%q err=%v", actual, target, err)
				}
			}
		})
	}
}

func TestMutationGuardEffectiveReadPaths(t *testing.T) {
	f := newMutationCopyFixture(t)
	t.Chdir(f.root)
	copyPath := filepath.Join(f.root, "copy")
	for _, mode := range []string{"absolute", "relative", "home-fallback"} {
		t.Run(mode, func(t *testing.T) {
			env := f.env.Clone()
			wantHome := filepath.Join(f.root, "cargo-home")
			switch mode {
			case "absolute":
				env["CARGO_HOME"] = wantHome
			case "relative":
				env["CARGO_HOME"] = "cargo-home"
			case "home-fallback":
				delete(env, "CARGO_HOME")
				wantHome = filepath.Join(env["HOME"], ".cargo")
			}
			reads, err := mutationGuardReadPaths(copyPath, env)
			if err != nil {
				t.Fatal(err)
			}
			if reads.Manifest != filepath.Join(copyPath, "Cargo.toml") {
				t.Fatalf("manifest=%q", reads.Manifest)
			}
			for i, name := range []string{"config", "config.toml"} {
				if reads.CargoHome.Files[i] != filepath.Join(wantHome, name) {
					t.Fatalf("mode=%s home paths=%v want=%s", mode, reads.CargoHome.Files, wantHome)
				}
			}
			for _, path := range reads.paths() {
				if !filepath.IsAbs(path) {
					t.Fatalf("non-absolute read path %q", path)
				}
			}
		})
	}
}
