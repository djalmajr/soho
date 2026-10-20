package cli

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestMutationGuardCargoExecutableFindsWindowsPath(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("cargo.exe lookup through Path requires native Windows")
	}
	bin := t.TempDir()
	if _, err := fakecli.Install(t, bin, "cargo", nil); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{"Path": bin}
	want := filepath.Join(bin, "cargo.exe")
	got, ok := cargoExecutable(env)
	if !ok || got != want {
		t.Fatalf("Path-only cargo lookup: got=%q ok=%t want=%q env=%v", got, ok, want, env)
	}
}

type guardFixture struct {
	root, source, copy string
	env                platform.Env
}

func newGuardFixture(t *testing.T) guardFixture {
	t.Helper()
	root, err := os.MkdirTemp("", "ha mutation guard ")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	source, copyDir := filepath.Join(root, "source"), filepath.Join(root, "copy")
	if err = os.MkdirAll(filepath.Join(source, "target"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(copyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(source, "source.txt"), []byte("source stays unchanged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(source, "target", "existing.o"), []byte("build artifact\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(source, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	env := make(platform.Env)
	pathValue := ""
	for _, entry := range testutil.CleanEnv(t) {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if strings.EqualFold(key, "PATH") {
			pathValue = value
			continue
		}
		env[key] = value
	}
	if pathValue != "" {
		env["PATH"] = pathValue
	}
	env["HOME"] = filepath.Join(root, "home")
	env["XDG_CONFIG_HOME"] = filepath.Join(root, "config")
	delete(env, "CARGO_TARGET_DIR")
	delete(env, "CARGO_BUILD_TARGET_DIR")
	return guardFixture{root: root, source: source, copy: copyDir, env: env}
}

func runMutationGuardFixture(t *testing.T, fixture guardFixture, args []string, overrides platform.Env, workDirs ...string) (int, string, string) {
	t.Helper()
	oldCwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	workDir := fixture.source
	if len(workDirs) > 0 {
		workDir = workDirs[0]
	}
	if err = os.Chdir(workDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCwd) })
	env := fixture.env.Clone()
	for key, value := range overrides {
		env[key] = value
	}
	var stdout, stderr bytes.Buffer
	oldOut, oldErr := platform.Stdout, platform.Stderr
	platform.Stdout, platform.Stderr = &stdout, &stderr
	t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
	code := Run(append([]string{"mutation-guard"}, args...), env)
	return code, stdout.String(), stderr.String()
}

func TestMutationGuard(t *testing.T) {
	t.Run("Cargo metadata rejects every supported target-dir form inside source", func(t *testing.T) {
		if _, err := exec.LookPath("cargo"); err != nil {
			t.Skip("cargo is not available on PATH")
		}
		s := newGuardFixture(t)
		// The fixture isolates HOME, so the toolchain rustup installed has
		// to be named explicitly: an inherited RUSTUP_HOME wins, else the
		// host user's ~/.rustup.
		rustupHome := os.Getenv("RUSTUP_HOME")
		if rustupHome == "" {
			hostHome, _ := os.UserHomeDir()
			rustupHome = filepath.Join(hostHome, ".rustup")
		}
		s.env["RUSTUP_HOME"] = rustupHome
		s.env["CARGO_HOME"] = filepath.Join(s.root, "cargo-home")
		if err := os.MkdirAll(s.env.Get("CARGO_HOME"), 0o700); err != nil {
			t.Fatal(err)
		}
		// A cargo that cannot run in the fixture's environment (a rustup
		// proxy whose toolchain is not under RUSTUP_HOME, as under a test
		// run with an isolated HOME) cannot answer cargo metadata: that is
		// the host, not the guard, so the case is skipped like a missing
		// cargo.
		probe := exec.Command("cargo", "--version")
		probe.Env = s.env.List()
		probe.Dir = s.root
		if out, err := probe.CombinedOutput(); err != nil {
			t.Skipf("cargo cannot run in the fixture environment (RUSTUP_HOME=%s): %v: %s", rustupHome, err, strings.TrimSpace(string(out)))
		}
		manifest := "[package]\nname = \"mutation-guard-fixture\"\nversion = \"0.1.0\"\nedition = \"2021\"\n"
		if err := os.WriteFile(filepath.Join(s.copy, "Cargo.toml"), []byte(manifest), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(s.copy, "src"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(s.copy, "src", "lib.rs"), []byte("pub fn fixture() {}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		target := fmt.Sprintf("%q", filepath.Join(s.source, "target-x"))
		cases := []struct{ name, config string }{
			{"spaced dotted key", "build . target-dir = " + target + "\n"},
			{"quoted dotted table", "\"build\".\"target-dir\" = " + target + "\n"},
			{"quoted dotted key", "build.\"target-dir\" = " + target + "\n"},
			{"unicode escaped key", "[build]\n\"target\\u002ddir\" = " + target + "\n"},
			{"multiline basic", "[build]\ntarget-dir = \"\"\"" + filepath.ToSlash(filepath.Join(s.source, "target-x")) + "\"\"\"\n"},
			{"multiline literal", "[build]\ntarget-dir = '''" + filepath.Join(s.source, "target-x") + "'''\n"},
			{"inline table key first", "build = { jobs = 2, target-dir = " + target + " }\n"},
			{"inline table key last", "build = { target-dir = " + target + ", jobs = 2 }\n"},
			{"profile table first", "[profile.x]\ntarget-dir = \"ignored\"\n[build]\ntarget-dir = " + target + "\n"},
			{"foo table first", "[foo]\ntarget-dir = \"ignored\"\n[build]\ntarget-dir = " + target + "\n"},
		}
		cargoDir := filepath.Join(s.copy, ".cargo")
		if err := os.Mkdir(cargoDir, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				if err := os.WriteFile(filepath.Join(cargoDir, "config.toml"), []byte(tc.config), 0o600); err != nil {
					t.Fatal(err)
				}
				code, out, stderr := runMutationGuardFixture(t, s, []string{s.copy, "--source", s.source}, nil)
				if code != 1 || !strings.Contains(out, "fail cargo-config: cargo metadata puts target_directory inside the source tree") || stderr != "" {
					t.Fatalf("config=%q code=%d stdout=%q stderr=%q", tc.config, code, out, stderr)
				}
			})
		}
		if err := os.Remove(filepath.Join(cargoDir, "config.toml")); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(filepath.Join(s.copy, ".cargo")); err != nil {
			t.Fatal(err)
		}
		ancestor := filepath.Join(s.root, ".cargo")
		if err := os.Mkdir(ancestor, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(ancestor, "config.toml"), []byte("[build]\ntarget-dir = "+target+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		var code int
		var out, stderr string
		code, out, stderr = runMutationGuardFixture(t, s, []string{s.copy, "--source", s.source}, nil)
		if code != 1 || !strings.Contains(out, "fail cargo-config: cargo metadata puts target_directory inside the source tree") || stderr != "" {
			t.Fatalf("ancestor code=%d stdout=%q stderr=%q", code, out, stderr)
		}
		if err := os.RemoveAll(ancestor); err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct{ name, home, value string }{
			{"CARGO_HOME parent relative", filepath.Join(s.root, "home", ".cargo"), "../source/target-x"},
			{"CARGO_HOME explicit relative", filepath.Join(s.root, "ch"), "source/target-x"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				if err := os.MkdirAll(tc.home, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(tc.home, "config.toml"), []byte("[build]\ntarget-dir = "+fmt.Sprintf("%q", tc.value)+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				code, out, stderr := runMutationGuardFixture(t, s, []string{s.copy, "--source", s.source}, platform.Env{"CARGO_HOME": tc.home})
				if code != 1 || !strings.Contains(out, "fail cargo-config: cargo metadata puts target_directory inside the source tree") || stderr != "" {
					t.Fatalf("code=%d stdout=%q stderr=%q", code, out, stderr)
				}
			})
		}
		if err := os.MkdirAll(cargoDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cargoDir, "config"), []byte("[build]\ntarget-dir = "+target+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cargoDir, "config.toml"), []byte("[build]\ntarget-dir = \"isolated\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, stderr = runMutationGuardFixture(t, s, []string{s.copy, "--source", s.source}, nil)
		if code != 1 || !strings.Contains(out, "fail cargo-config: cargo metadata puts target_directory inside the source tree") || stderr != "" {
			t.Fatalf("config precedence code=%d stdout=%q stderr=%q", code, out, stderr)
		}
		if err := os.WriteFile(filepath.Join(cargoDir, "config"), []byte("[build]\ntarget-dir = \"isolated\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cargoDir, "config.toml"), []byte("[build]\ntarget-dir = "+target+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, stderr = runMutationGuardFixture(t, s, []string{s.copy, "--source", s.source}, nil)
		if code != 0 || !strings.Contains(out, "ok cargo-config") || stderr != "" {
			t.Fatalf("config.toml must be ignored when config exists: code=%d stdout=%q stderr=%q", code, out, stderr)
		}
	})
	t.Run("cargo metadata without target_directory fails closed", func(t *testing.T) {
		s := newGuardFixture(t)
		if err := os.WriteFile(filepath.Join(s.copy, "Cargo.toml"), []byte("[package]\nname = \"fixture\"\nversion = \"0.1.0\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		fakeBin := t.TempDir()
		_, err := fakecli.Install(t, fakeBin, "cargo", []fakecli.Rule{{Argv: []string{"metadata", "--offline", "--no-deps", "--format-version", "1"}, Stdout: "{}"}})
		if err != nil {
			t.Fatal(err)
		}
		env := envFrom(fakecli.Env(os.Environ(), fakeBin))
		code, out, stderr := runMutationGuardFixture(t, s, []string{s.copy, "--source", s.source}, env)
		if code != 1 || !strings.Contains(out, "fail cargo-config: cargo metadata failed; target-dir cannot be resolved safely") || stderr != "" {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, out, stderr)
		}
	})
	t.Run("cargo metadata target_directory inside and outside source (fake cargo, no toolchain)", func(t *testing.T) {
		// The cargo metadata success path used to be reachable only through a
		// real toolchain (the skipped matrix above); a deterministic fake
		// cargo covers it here, with a PATH that contains nothing but the
		// fake, so an absent or different real cargo cannot influence the
		// result.
		s := newGuardFixture(t)
		if err := os.WriteFile(filepath.Join(s.copy, "Cargo.toml"), []byte("[package]\nname = \"fixture\"\nversion = \"0.1.0\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		install := func(rules []fakecli.Rule) platform.Env {
			t.Helper()
			bin := t.TempDir()
			if _, err := fakecli.Install(t, bin, "cargo", rules); err != nil {
				t.Fatal(err)
			}
			return envFrom(fakecli.Env(os.Environ(), bin))
		}
		metadata := func(target string) string { return fmt.Sprintf(`{"target_directory":%q}`, target) }
		for _, tc := range []struct {
			name       string
			rules      []fakecli.Rule
			code       int
			stdoutPart string
		}{
			{
				"absolute target_directory inside source fails", []fakecli.Rule{{Argv: []string{"metadata", "--offline", "--no-deps", "--format-version", "1"}, Stdout: metadata(filepath.Join(s.source, "target-x"))}},
				1, "fail cargo-config: cargo metadata puts target_directory inside the source tree",
			},
			{
				"absolute target_directory outside source passes", []fakecli.Rule{{Argv: []string{"metadata", "--offline", "--no-deps", "--format-version", "1"}, Stdout: metadata(filepath.Join(s.copy, "target"))}},
				0, "ok cargo-config",
			},
			{
				// Relative to the copy, like cargo reports it.
				"relative target_directory outside source passes", []fakecli.Rule{{Argv: []string{"metadata", "--offline", "--no-deps", "--format-version", "1"}, Stdout: metadata("target")}},
				0, "ok cargo-config",
			},
			{
				"relative target_directory inside source fails", []fakecli.Rule{{Argv: []string{"metadata", "--offline", "--no-deps", "--format-version", "1"}, Stdout: metadata("../source/target-x")}},
				1, "fail cargo-config: cargo metadata puts target_directory inside the source tree",
			},
			{
				"cargo metadata exit failure fails closed", []fakecli.Rule{{Argv: []string{"metadata", "--offline", "--no-deps", "--format-version", "1"}, Code: 1, Stderr: "error: could not find `Cargo.toml`\n"}},
				1, "fail cargo-config: cargo metadata failed; target-dir cannot be resolved safely",
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				env := install(tc.rules)
				code, out, stderr := runMutationGuardFixture(t, s, []string{s.copy, "--source", s.source}, env)
				if code != tc.code || !strings.Contains(out, tc.stdoutPart) || (tc.code == 0 && stderr != "") {
					t.Fatalf("code=%d stdout=%q stderr=%q want code %d containing %q", code, out, stderr, tc.code, tc.stdoutPart)
				}
			})
		}
	})
	t.Run("non-executable cargo on PATH fails closed when metadata cannot spawn", func(t *testing.T) { // Mutation captured: treating a regular non-executable cargo file as absent returns textual-config success.
		s := newGuardFixture(t)
		if err := os.WriteFile(filepath.Join(s.copy, "Cargo.toml"), []byte("[package]\nname = \"fixture\"\nversion = \"0.1.0\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		fakeBin := t.TempDir()
		cargo := filepath.Join(fakeBin, "cargo")
		if err := os.WriteFile(cargo, []byte("not executable\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, stderr := runMutationGuardFixture(t, s, []string{s.copy, "--source", s.source}, platform.Env{"PATH": fakeBin})
		if code != 1 || !strings.Contains(out, "fail cargo-config: cargo metadata failed; target-dir cannot be resolved safely") || stderr != "" {
			info, _ := os.Stat(cargo)
			t.Fatalf("mode=%#o code=%d stdout=%q stderr=%q", info.Mode().Perm(), code, out, stderr)
		}
	})
	t.Run("text fallback fails closed without cargo and ignores user config for non-Cargo copies", func(t *testing.T) {
		s := newGuardFixture(t)
		if err := os.WriteFile(filepath.Join(s.copy, "Cargo.toml"), []byte("[package]\nname = \"fixture\"\nversion = \"0.1.0\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		noCargo := t.TempDir()
		s.env["PATH"] = noCargo
		cargoDir := filepath.Join(s.copy, ".cargo")
		if err := os.Mkdir(cargoDir, 0o700); err != nil {
			t.Fatal(err)
		}
		target := fmt.Sprintf("%q", filepath.Join(s.source, "target"))
		if err := os.WriteFile(filepath.Join(cargoDir, "config.toml"), []byte("build.target-dir = "+target+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, stderr := runMutationGuardFixture(t, s, []string{s.copy, "--source", s.source}, nil)
		if code != 1 || !strings.Contains(out, "fail cargo-config: "+filepath.Join(".cargo", "config.toml")+" target-dir cannot be resolved safely") || stderr != "" {
			t.Fatalf("dotted target-dir: code=%d stdout=%q stderr=%q", code, out, stderr)
		}
		for _, config := range []string{
			fmt.Sprintf("[build]\ntarget-dir = '%s'\n", filepath.Join(s.source, "target")),
			"build = { jobs = 2, target-dir = " + target + " }\n",
			"[build]\ntarget-dir = \"\"\"" + filepath.Join(s.source, "target") + "\"\"\"\n",
			"[foo]\ntarget-dir = \"ignored\"\n[build]\ntarget-dir = " + target + "\n",
			"[build]\n\"target\\u002ddir\" = " + target + "\n",
			"[build]\ntarget-dir =\u00a0" + target + "\n",
			"[build]\ntarget-dir = " + target + "\u3000# comment\n",
		} {
			if err := os.WriteFile(filepath.Join(cargoDir, "config.toml"), []byte(config), 0o600); err != nil {
				t.Fatal(err)
			}
			code, out, stderr := runMutationGuardFixture(t, s, []string{s.copy, "--source", s.source}, nil)
			if code != 1 || !strings.Contains(out, "fail cargo-config:") || !strings.Contains(out, filepath.Join(".cargo", "config.toml")) || stderr != "" {
				t.Fatalf("config=%q code=%d stdout=%q stderr=%q", config, code, out, stderr)
			}
		}
		if err := os.WriteFile(filepath.Join(cargoDir, "config.toml"), []byte("[build]\ntarget-dir = 'isolated-target'\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, stderr = runMutationGuardFixture(t, s, []string{s.copy, "--source", s.source}, nil)
		if code != 0 || !strings.Contains(out, "ok cargo-config") || stderr != "" {
			t.Fatalf("safe single-quoted target-dir: code=%d stdout=%q stderr=%q", code, out, stderr)
		}
		if err := os.RemoveAll(cargoDir); err != nil {
			t.Fatal(err)
		}
		ancestor := filepath.Join(s.root, ".cargo")
		if err := os.Mkdir(ancestor, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(ancestor, "config"), []byte("[build]\ntarget-dir = \"source/target\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, stderr = runMutationGuardFixture(t, s, []string{s.copy, "--source", s.source}, nil)
		if code != 1 || !strings.Contains(filepath.ToSlash(out), "../.cargo/config sets target-dir inside the source tree") || stderr != "" {
			t.Fatalf("ancestor config: code=%d stdout=%q stderr=%q", code, out, stderr)
		}
		if err := os.RemoveAll(ancestor); err != nil {
			t.Fatal(err)
		}
		cargoHome := filepath.Join(s.root, "ch")
		if err := os.Mkdir(cargoHome, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cargoHome, "config"), []byte("[build]\ntarget-dir = \"source/target\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cargoHome, "config.toml"), []byte("[build]\ntarget-dir = \"isolated\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, stderr = runMutationGuardFixture(t, s, []string{s.copy, "--source", s.source}, platform.Env{"CARGO_HOME": cargoHome})
		if code != 1 || !strings.Contains(out, "config sets target-dir inside the source tree") || stderr != "" {
			t.Fatalf("CARGO_HOME config precedence: code=%d stdout=%q stderr=%q", code, out, stderr)
		}
		if err := os.WriteFile(filepath.Join(cargoHome, "config"), []byte("[build]\ntarget-dir = \"isolated\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cargoHome, "config.toml"), []byte("[build]\ntarget-dir = \"source/target\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, stderr = runMutationGuardFixture(t, s, []string{s.copy, "--source", s.source}, platform.Env{"CARGO_HOME": cargoHome})
		if code != 0 || !strings.Contains(out, "ok cargo-config") || stderr != "" {
			t.Fatalf("CARGO_HOME config.toml must be ignored: code=%d stdout=%q stderr=%q", code, out, stderr)
		}
		if err := os.Mkdir(cargoDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cargoDir, "config"), []byte("[build]\ntarget-dir = "+target+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cargoDir, "config.toml"), []byte("[build]\ntarget-dir = \"isolated\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, _ = runMutationGuardFixture(t, s, []string{s.copy, "--source", s.source}, nil)
		if code != 1 || !strings.Contains(filepath.ToSlash(out), "fail cargo-config: .cargo/config sets target-dir inside the source tree") {
			t.Fatalf("precedence code=%d stdout=%q", code, out)
		}
		if err := os.WriteFile(filepath.Join(cargoDir, "config"), []byte("[build]\ntarget-dir = \"isolated\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cargoDir, "config.toml"), []byte("[build]\ntarget-dir = "+target+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, stderr = runMutationGuardFixture(t, s, []string{s.copy, "--source", s.source}, nil)
		if code != 0 || !strings.Contains(out, "ok cargo-config") || stderr != "" {
			t.Fatalf("copy config.toml must be ignored behind isolated config: code=%d stdout=%q stderr=%q", code, out, stderr)
		}
		if err := os.Remove(filepath.Join(s.copy, "Cargo.toml")); err != nil {
			t.Fatal(err)
		}
		s.env["CARGO_HOME"] = filepath.Join(s.root, "user-cargo")
		if err := os.MkdirAll(s.env.Get("CARGO_HOME"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(s.env.Get("CARGO_HOME"), "config.toml"), []byte("[build]\ntarget-dir = "+target+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, _ = runMutationGuardFixture(t, s, []string{s.copy, "--source", s.source}, nil)
		if code != 0 || !strings.Contains(out, "ok cargo-config") {
			t.Fatalf("non-Cargo user config: code=%d stdout=%q", code, out)
		}
	})
	t.Run("Cargo target-dir accepts all supported forms and checks ancestry and Cargo home", func(t *testing.T) { // JS: "rejects Cargo target-dir syntaxes from the copy, its ancestors, and CARGO_HOME"
		// Mutation captured: dropping any accepted config form allows Cargo to write into source.
		s := newGuardFixture(t)
		if err := os.WriteFile(filepath.Join(s.copy, "Cargo.toml"), []byte("[package]\nname = \"fixture\"\nversion = \"0.1.0\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		s.env["PATH"] = t.TempDir()
		cargo := filepath.Join(s.copy, ".cargo")
		if err := os.Mkdir(cargo, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, config := range []string{
			fmt.Sprintf("build.target-dir = %q\n", filepath.Join(s.source, "target")),
			fmt.Sprintf("build = { target-dir = %q }\n", filepath.Join(s.source, "target")),
			fmt.Sprintf("[build]\n\"target-dir\" = '%s'\n", filepath.Join(s.source, "target")),
		} {
			if err := os.WriteFile(filepath.Join(cargo, "config.toml"), []byte(config), 0o600); err != nil {
				t.Fatal(err)
			}
			code, out, _ := runMutationGuardFixture(t, s, []string{s.copy, "--source", s.source}, nil)
			if code != 1 || !strings.Contains(out, "fail cargo-config:") {
				t.Fatalf("config=%q code=%d out=%q", config, code, out)
			}
		}
		if err := os.RemoveAll(cargo); err != nil {
			t.Fatal(err)
		}
		ancestorCargo := filepath.Join(s.root, ".cargo")
		if err := os.Mkdir(ancestorCargo, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(ancestorCargo, "config.toml"), []byte(fmt.Sprintf("build.target-dir = %q\n", filepath.Join(s.source, "target"))), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, _ := runMutationGuardFixture(t, s, []string{s.copy, "--source", s.source}, nil)
		if code != 1 || !strings.Contains(filepath.ToSlash(out), filepath.ToSlash(filepath.Join("..", ".cargo", "config.toml"))+" target-dir cannot be resolved safely") {
			t.Fatalf("ancestor: code=%d out=%q", code, out)
		}
		if err := os.RemoveAll(ancestorCargo); err != nil {
			t.Fatal(err)
		}
		cargoHome := filepath.Join(s.root, "cargo-home")
		if err := os.Mkdir(cargoHome, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cargoHome, "config.toml"), []byte(fmt.Sprintf("[build]\ntarget-dir = %q\n", filepath.Join(s.source, "target"))), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, _ = runMutationGuardFixture(t, s, []string{s.copy, "--source", s.source}, platform.Env{"CARGO_HOME": cargoHome})
		if code != 1 || !strings.Contains(out, filepath.Join(cargoHome, "config.toml")+" sets target-dir inside the source tree") {
			t.Fatalf("CARGO_HOME: code=%d out=%q", code, out)
		}
		defaultCargoHome := filepath.Join(s.env.Get("HOME"), ".cargo")
		if err := os.MkdirAll(defaultCargoHome, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(defaultCargoHome, "config.toml"), []byte(fmt.Sprintf("[build]\ntarget-dir = %q\n", filepath.Join(s.source, "target"))), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, _ = runMutationGuardFixture(t, s, []string{s.copy, "--source", s.source}, platform.Env{"CARGO_HOME": ""})
		if code != 1 || !strings.Contains(out, filepath.Join(defaultCargoHome, "config.toml")+" sets target-dir inside the source tree") {
			t.Fatalf("default Cargo home: code=%d out=%q", code, out)
		}
		if err := os.Mkdir(cargo, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cargo, "config.toml"), []byte(fmt.Sprintf("[build]\ntarget-dir = %q\n", filepath.Join(s.copy, "target"))), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, _ = runMutationGuardFixture(t, s, []string{s.copy, "--source", s.source}, platform.Env{"CARGO_HOME": cargoHome})
		if code != 0 {
			t.Fatalf("copy config must override CARGO_HOME: code=%d out=%q", code, out)
		}
	})
	t.Run("ENOTDIR in a build destination is treated as a missing tail", func(t *testing.T) { // JS: "treats ENOTDIR like a missing tail and reports symlinks in UTF-16 order"
		// Mutation captured: rejecting ENOTDIR makes Go diverge from JS for a destination below a file.
		s := newGuardFixture(t)
		if err := os.WriteFile(filepath.Join(s.copy, "not-dir"), []byte("file"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, stderr := runMutationGuardFixture(t, s, []string{s.copy, "--source", s.source}, platform.Env{"CARGO_TARGET_DIR": filepath.Join(s.copy, "not-dir", "target")})
		if code != 0 || !strings.Contains(out, "ok build-env") || stderr != "" {
			t.Fatalf("code=%d out=%q err=%q", code, out, stderr)
		}
	})
	t.Run("symlink findings sort by descending UTF-16 code units", func(t *testing.T) { // JS: "treats ENOTDIR like a missing tail and reports symlinks in UTF-16 order"
		// Mutation captured: byte ordering selects a different first symlink for non-BMP names.
		s := newGuardFixture(t)
		if err := os.Symlink(filepath.Join(s.source, "target"), filepath.Join(s.copy, "a")); err != nil {
			t.Skip("symlink creation unavailable")
		}
		if err := os.Symlink(filepath.Join(s.source, "target"), filepath.Join(s.copy, "😀")); err != nil {
			t.Skip("symlink creation unavailable")
		}
		if err := os.Symlink(filepath.Join(s.source, "target"), filepath.Join(s.copy, "～")); err != nil {
			t.Skip("symlink creation unavailable")
		}
		code, out, _ := runMutationGuardFixture(t, s, []string{s.copy, "--source", s.source}, nil)
		if code != 1 || !strings.Contains(out, "symlink ～ -> ") {
			t.Fatalf("code=%d out=%q", code, out)
		}
	})
	t.Run("source symlinks report in reverse code-unit directory order", func(t *testing.T) { // JS: "reports source symlinks in reverse code-unit directory order"
		// Mutation captured: ascending, locale, or UTF-8 ordering reports a/target instead of B/target.
		s := newGuardFixture(t)
		for _, name := range []string{"B", "a"} {
			if err := os.Mkdir(filepath.Join(s.source, name), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(s.copy, name), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(s.source, name), filepath.Join(s.copy, name, "target")); err != nil {
				t.Skipf("symlink creation unavailable: %v", err)
			}
		}
		code, out, stderr := runMutationGuardFixture(t, s, []string{s.copy, "--source", s.source}, nil)
		// The guard prints the relative symlink path with the platform separator.
		if code != 1 || stderr != "" || !strings.Contains(out, "symlink "+filepath.Join("B", "target")+" -> ") || strings.Contains(out, "symlink "+filepath.Join("a", "target")) {
			t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
		}
	})
	t.Run("project root is used when source is omitted", func(t *testing.T) {
		s := newGuardFixture(t)
		if err := os.MkdirAll(filepath.Join(s.source, "nested", "work"), 0o700); err != nil {
			t.Fatal(err)
		}
		copyInside := filepath.Join(s.source, "isolated-copy")
		if err := os.Mkdir(copyInside, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := exec.Command("git", "-C", s.source, "init", "-q").Run(); err != nil {
			t.Skip("git is not available to create the project-root fixture")
		}
		code, out, stderr := runMutationGuardFixture(t, s, []string{copyInside}, nil, filepath.Join(s.source, "nested", "work"))
		if code != 1 || !strings.Contains(out, "fail copy-outside-source: copy and source directories overlap") || stderr != "" {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, out, stderr)
		}
	})
	t.Run("rejects a target symlink into the source before any mutation", func(t *testing.T) { // JS: "rejects a target symlink into the source before any mutation"
		s := newGuardFixture(t)
		if err := os.Symlink(filepath.Join(s.source, "target"), filepath.Join(s.copy, "target")); err != nil {
			t.Skip("symlink creation is unavailable on this host")
		}
		code, out, stderr := runMutationGuardFixture(t, s, []string{s.copy, "--source", s.source}, nil)
		if code == 0 {
			if err := os.WriteFile(filepath.Join(s.copy, "target", "mutation-marker"), []byte("mutated"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if code != 1 || !strings.Contains(out, "fail no-symlink-into-source: symlink target -> ") || stderr != "" {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, out, stderr)
		}
		if _, err := os.Stat(filepath.Join(s.source, "target", "mutation-marker")); !os.IsNotExist(err) {
			t.Fatalf("guard allowed source mutation: %v", err)
		}
	})
	t.Run("rejects build output environment paths inside the source without echoing values", func(t *testing.T) { // JS: "rejects build output environment paths inside the source without echoing values"
		s := newGuardFixture(t)
		secretPath := filepath.Join(s.source, "target")
		code, out, stderr := runMutationGuardFixture(t, s, []string{s.copy, "--source", s.source, "--env", "MUTATION_TARGET"}, platform.Env{"CARGO_BUILD_TARGET_DIR": secretPath, "CARGO_TARGET_DIR": secretPath, "MUTATION_TARGET": secretPath})
		if code != 1 || !strings.Contains(out, "fail build-env: CARGO_TARGET_DIR points into the source tree") || !strings.Contains(out, "CARGO_BUILD_TARGET_DIR points into the source tree") || !strings.Contains(out, "MUTATION_TARGET points into the source tree") || strings.Contains(out, secretPath) || stderr != "" {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, out, stderr)
		}
		code, out, stderr = runMutationGuardFixture(t, s, []string{s.copy, "--source", s.source}, platform.Env{"CARGO_TARGET_DIR": ""})
		if code != 0 || !strings.Contains(out, "ok build-env") || stderr != "" {
			t.Fatalf("empty build env: code=%d stdout=%q stderr=%q", code, out, stderr)
		}
	})
	t.Run("resolves environment paths through symlinks before containment checks", func(t *testing.T) {
		s := newGuardFixture(t)
		link := filepath.Join(s.copy, "source-link")
		if err := os.Symlink(s.source, link); err != nil {
			t.Skip("symlink creation unavailable")
		}
		code, out, stderr := runMutationGuardFixture(t, s, []string{s.copy, "--source", s.source}, platform.Env{"CARGO_TARGET_DIR": filepath.Join(link, "target")})
		if code != 1 || !strings.Contains(out, "fail build-env: CARGO_TARGET_DIR points into the source tree") || stderr != "" {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, out, stderr)
		}
	})
	t.Run("rejects absolute cargo target-dir values inside the source", func(t *testing.T) { // JS: "rejects absolute cargo target-dir values inside the source"
		s := newGuardFixture(t)
		if err := os.WriteFile(filepath.Join(s.copy, "Cargo.toml"), []byte("[package]\nname = \"fixture\"\nversion = \"0.1.0\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		s.env["PATH"] = t.TempDir()
		cargo := filepath.Join(s.copy, ".cargo")
		if err := os.Mkdir(cargo, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cargo, "config.toml"), []byte(fmt.Sprintf("[build]\ntarget-dir = %q\n", filepath.Join(s.source, "target"))), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, stderr := runMutationGuardFixture(t, s, []string{s.copy, "--source", s.source}, nil)
		if code != 1 || !strings.Contains(out, "fail cargo-config: "+filepath.Join(".cargo", "config.toml")+" sets target-dir inside the source tree") || stderr != "" {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, out, stderr)
		}
	})
	t.Run("relative and single-quoted build destinations are resolved against the copy", func(t *testing.T) { // JS: "relative and single-quoted build destinations are resolved against the copy"
		s := newGuardFixture(t)
		if err := os.WriteFile(filepath.Join(s.copy, "Cargo.toml"), []byte("[package]\nname = \"fixture\"\nversion = \"0.1.0\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		s.env["PATH"] = t.TempDir()
		cargo := filepath.Join(s.copy, ".cargo")
		if err := os.Mkdir(cargo, 0o700); err != nil {
			t.Fatal(err)
		}
		cfg := filepath.Join(cargo, "config.toml")
		write := func(text string) {
			t.Helper()
			if err := os.WriteFile(cfg, []byte(text), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		write("[build]\ntarget-dir = \"../source/target\"\n")
		code, out, _ := runMutationGuardFixture(t, s, []string{s.copy, "--source", s.source}, nil)
		if code != 1 || !strings.Contains(out, "fail cargo-config: "+filepath.Join(".cargo", "config.toml")+" sets target-dir inside the source tree") {
			t.Fatalf("relative: code=%d out=%q", code, out)
		}
		write(fmt.Sprintf("[build]\ntarget-dir = '%s'\n", filepath.Join(s.source, "target")))
		code, out, _ = runMutationGuardFixture(t, s, []string{s.copy, "--source", s.source}, nil)
		if code != 1 || !strings.Contains(out, "fail cargo-config") {
			t.Fatalf("literal: code=%d out=%q", code, out)
		}
		write("[build]\ntarget-dir = 'target'\n")
		code, out, _ = runMutationGuardFixture(t, s, []string{s.copy, "--source", s.source}, platform.Env{"CARGO_TARGET_DIR": "../source/target"})
		if code != 1 || !strings.Contains(out, "ok cargo-config") || !strings.Contains(out, "fail build-env: CARGO_TARGET_DIR points into the source tree") || strings.Contains(out, "../source/target") {
			t.Fatalf("env-relative: code=%d out=%q", code, out)
		}
		code, out, _ = runMutationGuardFixture(t, s, []string{s.copy, "--source", s.source}, platform.Env{"CARGO_TARGET_DIR": "target"})
		if code != 0 {
			t.Fatalf("isolated env: code=%d out=%q", code, out)
		}
	})
	t.Run("accepts an isolated copy, ignores broken links and does not descend into .git", func(t *testing.T) { // JS: "accepts an isolated copy, ignores broken links and does not descend into .git"
		s := newGuardFixture(t)
		if err := os.MkdirAll(filepath.Join(s.copy, "target"), 0o700); err != nil {
			t.Fatal(err)
		}
		cargo := filepath.Join(s.copy, ".cargo")
		if err := os.Mkdir(cargo, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cargo, "config.toml"), []byte(fmt.Sprintf("target-dir = %q\n", filepath.Join(s.copy, "target"))), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(s.root, "missing-target"), filepath.Join(s.copy, "broken-link")); err != nil {
			t.Skip("symlink creation is unavailable on this host")
		}
		if err := os.Mkdir(filepath.Join(s.copy, ".git"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(s.source, "target"), filepath.Join(s.copy, ".git", "ignored-link")); err != nil {
			t.Skip("symlink creation is unavailable on this host")
		}
		code, out, stderr := runMutationGuardFixture(t, s, []string{s.copy}, platform.Env{"CARGO_TARGET_DIR": filepath.Join(s.copy, "target")})
		want := "ok copy-outside-source\nok no-symlink-into-source\nok build-env\nok cargo-config\n"
		if code != 0 || out != want || stderr != "" {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, out, stderr)
		}
	})
	t.Run("finds symlinks into source in nested copy directories", func(t *testing.T) {
		s := newGuardFixture(t)
		nested := filepath.Join(s.copy, "nested", "deeper")
		if err := os.MkdirAll(nested, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(s.source, "target"), filepath.Join(nested, "source-link")); err != nil {
			t.Skip("symlink creation unavailable")
		}
		code, out, stderr := runMutationGuardFixture(t, s, []string{s.copy, "--source", s.source}, nil)
		if code != 1 || !strings.Contains(out, "fail no-symlink-into-source: symlink "+filepath.Join("nested", "deeper", "source-link")+" -> ") || stderr != "" {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, out, stderr)
		}
	})
	t.Run("resolves a symlink chain that ends in source", func(t *testing.T) {
		s := newGuardFixture(t)
		bridge := filepath.Join(s.root, "bridge")
		if err := os.Symlink(filepath.Join(s.source, "target"), bridge); err != nil {
			t.Skip("symlink creation unavailable")
		}
		if err := os.Symlink(bridge, filepath.Join(s.copy, "indirect-link")); err != nil {
			t.Skip("symlink creation unavailable")
		}
		code, out, stderr := runMutationGuardFixture(t, s, []string{s.copy, "--source", s.source}, nil)
		if code != 1 || !strings.Contains(out, "fail no-symlink-into-source: symlink indirect-link -> ") || stderr != "" {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, out, stderr)
		}
	})
	t.Run("isolated-copy mutation leaves every source file and build artifact unchanged", func(t *testing.T) { // JS: "isolated-copy mutation leaves every source file and build artifact unchanged"
		s := newGuardFixture(t)
		if err := os.MkdirAll(filepath.Join(s.copy, "target"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(s.copy, "source.txt"), []byte("source stays unchanged\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		sourceBefore := treeHash(s.source)
		buildBefore := treeHash(filepath.Join(s.source, "target"))
		code, out, stderr := runMutationGuardFixture(t, s, []string{s.copy}, platform.Env{"CARGO_TARGET_DIR": filepath.Join(s.copy, "target")})
		if code != 0 || out != "ok copy-outside-source\nok no-symlink-into-source\nok build-env\nok cargo-config\n" || stderr != "" {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, out, stderr)
		}
		if err := os.WriteFile(filepath.Join(s.copy, "source.txt"), []byte("temporary mutation\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(s.copy, "target", "build-output.o"), []byte("isolated build output\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if treeHash(s.source) != sourceBefore || treeHash(filepath.Join(s.source, "target")) != buildBefore {
			t.Fatal("source files or build artifact changed")
		}
		if data, _ := os.ReadFile(filepath.Join(s.copy, "source.txt")); string(data) != "temporary mutation\n" {
			t.Fatalf("copy file=%q", data)
		}
		if _, err := os.Stat(filepath.Join(s.copy, "target", "build-output.o")); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("rejects copy/source containment in either direction", func(t *testing.T) { // JS: "rejects copy/source containment in either direction"
		s := newGuardFixture(t)
		nested := filepath.Join(s.source, "nested-copy")
		if err := os.Mkdir(nested, 0o700); err != nil {
			t.Fatal(err)
		}
		code, out, stderr := runMutationGuardFixture(t, s, []string{nested, "--source", s.source}, nil)
		if code != 1 || !strings.Contains(out, "fail copy-outside-source:") || stderr != "" {
			t.Fatalf("nested: code=%d out=%q err=%q", code, out, stderr)
		}
		code, out, stderr = runMutationGuardFixture(t, s, []string{s.root, "--source", s.source}, nil)
		if code != 1 || !strings.Contains(out, "fail copy-outside-source:") || stderr != "" {
			t.Fatalf("contains: code=%d out=%q err=%q", code, out, stderr)
		}
		code, out, stderr = runMutationGuardFixture(t, s, []string{s.source, "--source", s.source}, nil)
		if code != 1 || !strings.Contains(out, "fail copy-outside-source:") || stderr != "" {
			t.Fatalf("equal roots: code=%d out=%q err=%q", code, out, stderr)
		}
	})
	t.Run("invalid arguments and non-directory copies exit 2", func(t *testing.T) { // JS: "invalid arguments and non-directory copies exit 2"
		s := newGuardFixture(t)
		args := [][]string{{}, {"--source", s.source}, {s.copy, "--source"}, {s.copy, "--source", "--env", "X"}, {s.copy, "--env"}, {s.copy, "--env", "--source", s.source}, {filepath.Join(s.root, "missing-copy")}, {filepath.Join(s.source, "source.txt")}, {s.copy, "--unknown"}}
		for _, argv := range args {
			code, out, stderr := runMutationGuardFixture(t, s, argv, nil)
			if code != 2 || out != "" || !strings.Contains(stderr, "usage: mutation-guard") {
				t.Errorf("args=%q code=%d out=%q err=%q", argv, code, out, stderr)
			}
		}
	})
	t.Run("default source inside the copy refuses with exit 2 and no check lines", func(t *testing.T) {
		s := newGuardFixture(t)
		sub := filepath.Join(s.copy, "sub")
		if err := os.MkdirAll(sub, 0o700); err != nil {
			t.Fatal(err)
		}
		want := func(copyArg string) string {
			return "mutation-guard: without --source the source is the project of the current directory, which is the copy itself (" + copyArg + "); run it from the source worktree or pass --source <dir>\n"
		}
		// From the copy root, as `soho mutation-guard .`.
		code, out, stderr := runMutationGuardFixture(t, s, []string{"."}, nil, s.copy)
		if code != 2 || out != "" || stderr != want(s.copy) {
			t.Fatalf("copy root: code=%d stdout=%q stderr=%q", code, out, stderr)
		}
		// From a subdirectory, with the copy root as the copy argument.
		code, out, stderr = runMutationGuardFixture(t, s, []string{s.copy}, nil, sub)
		if code != 2 || out != "" || stderr != want(s.copy) {
			t.Fatalf("subdirectory: code=%d stdout=%q stderr=%q", code, out, stderr)
		}
	})
	t.Run("default source from a git-repo copy refuses the same way", func(t *testing.T) {
		s := newGuardFixture(t)
		if err := exec.Command("git", "-C", s.copy, "init", "-q").Run(); err != nil {
			t.Skip("git is not available to create the project-root fixture")
		}
		sub := filepath.Join(s.copy, "sub")
		if err := os.MkdirAll(sub, 0o700); err != nil {
			t.Fatal(err)
		}
		want := "mutation-guard: without --source the source is the project of the current directory, which is the copy itself (" + s.copy + "); run it from the source worktree or pass --source <dir>\n"
		code, out, stderr := runMutationGuardFixture(t, s, []string{"."}, nil, s.copy)
		if code != 2 || out != "" || stderr != want {
			t.Fatalf("copy root: code=%d stdout=%q stderr=%q", code, out, stderr)
		}
		code, out, stderr = runMutationGuardFixture(t, s, []string{s.copy}, nil, sub)
		if code != 2 || out != "" || stderr != want {
			t.Fatalf("subdirectory: code=%d stdout=%q stderr=%q", code, out, stderr)
		}
	})
	t.Run("explicit source from inside the copy keeps the current behavior", func(t *testing.T) {
		s := newGuardFixture(t)
		// The correct source: exit 0, as today.
		code, out, stderr := runMutationGuardFixture(t, s, []string{s.copy, "--source", s.source}, nil, s.copy)
		want := "ok copy-outside-source\nok no-symlink-into-source\nok build-env\nok cargo-config\n"
		if code != 0 || out != want || stderr != "" {
			t.Fatalf("correct source: code=%d stdout=%q stderr=%q", code, out, stderr)
		}
		// A source pointing at the copy itself: still exit 1 by overlap.
		code, out, stderr = runMutationGuardFixture(t, s, []string{s.copy, "--source", s.copy}, nil, s.copy)
		if code != 1 || !strings.Contains(out, "fail copy-outside-source: copy and source directories overlap") || !strings.Contains(out, "ok no-symlink-into-source") || stderr != "" {
			t.Fatalf("source is the copy: code=%d stdout=%q stderr=%q", code, out, stderr)
		}
	})
	t.Run("default source from the project root still passes", func(t *testing.T) {
		s := newGuardFixture(t)
		code, out, stderr := runMutationGuardFixture(t, s, []string{s.copy}, nil, s.source)
		want := "ok copy-outside-source\nok no-symlink-into-source\nok build-env\nok cargo-config\n"
		if code != 0 || out != want || stderr != "" {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, out, stderr)
		}
	})
}

func treeHash(root string) [32]byte {
	hash := sha256.New()
	var visit func(string, string) error
	visit = func(dir, rel string) error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, entry := range entries {
			if rel == "" && entry.Name() == ".git" {
				continue
			}
			name := filepath.Join(rel, entry.Name())
			full := filepath.Join(dir, entry.Name())
			info, err := os.Lstat(full)
			if err != nil {
				return err
			}
			kind := "f"
			if info.IsDir() {
				kind = "d"
			} else if info.Mode()&os.ModeSymlink != 0 {
				kind = "l"
			}
			_, _ = fmt.Fprintf(hash, "%s:%s\x00", kind, name)
			if info.IsDir() {
				if err = visit(full, name); err != nil {
					return err
				}
			} else if info.Mode()&os.ModeSymlink != 0 {
				target, e := os.Readlink(full)
				if e != nil {
					return e
				}
				_, _ = hash.Write([]byte(target))
			} else {
				content, e := os.ReadFile(full)
				if e != nil {
					return e
				}
				_, _ = hash.Write(content)
			}
		}
		return nil
	}
	_ = visit(root, "")
	var sum [32]byte
	copy(sum[:], hash.Sum(nil))
	return sum
}
