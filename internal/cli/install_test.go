package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/install"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// The install command is tested hermetically through the real production
// entry (Run): an httptest release serving either static fixture bytes or
// a real native fakecli executable, temporary roots, and
// zero-requests/zero-effects proofs for every invalid call. Every test
// points SOHO_RELEASE_BASE at the httptest server; the production
// default base is a core constant that no test touches. The default
// install never executes the installed bytes or touches the user PATH;
// --verify-version and --add-to-path are opt-in, and their registry access
// goes through the injected seam (in-memory stores only — no test ever
// calls the real registry).

const installFixtureTag = "v1.2.3"

func installFixtureName() string {
	name := "soho_" + runtime.GOOS + "_" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

func installBinaryName() string {
	name := "soho"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

// hermeticReleaseServer serves one release (artifact + SHA256SUMS) and
// counts every request it receives.
func hermeticReleaseServer(t *testing.T, tag, osName, arch string, payload []byte) (*httptest.Server, *int32) {
	t.Helper()
	var reqs int32
	name := "soho_" + osName + "_" + arch
	if osName == "windows" {
		name += ".exe"
	}
	sum := sha256.Sum256(payload)
	sums := hex.EncodeToString(sum[:]) + "  " + name + "\n"
	mux := http.NewServeMux()
	mux.HandleFunc("/releases/download/"+tag+"/"+name, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&reqs, 1)
		_, _ = w.Write(payload)
	})
	mux.HandleFunc("/releases/download/"+tag+"/SHA256SUMS", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&reqs, 1)
		_, _ = w.Write([]byte(sums))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &reqs
}

func installTestEnv(t *testing.T, home string) platform.Env {
	t.Helper()
	return platform.Env{
		"PATH":            t.TempDir(),
		"HOME":            home,
		"XDG_CONFIG_HOME": t.TempDir(),
	}
}

// runInstallCLI invokes the production entry (Run) with argv (leading
// "install") and env from a fresh temporary cwd, capturing the
// stdout/stderr the command prints.
func runInstallCLI(t *testing.T, env platform.Env, argv ...string) (int, string, string) {
	t.Helper()
	oldCwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCwd) })
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, errOut strings.Builder
	platform.Stdout, platform.Stderr = &out, &errOut
	t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
	code := Run(append([]string{"install"}, argv...), env)
	return code, out.String(), errOut.String()
}

func assertZeroRequests(t *testing.T, reqs *int32) {
	t.Helper()
	if n := atomic.LoadInt32(reqs); n != 0 {
		t.Fatalf("%d requests before the effect proof, want 0", n)
	}
}

func assertNoDir(t *testing.T, dir string) {
	t.Helper()
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatalf("install dir %s was created (err=%v)", dir, err)
	}
}

// TestInstallCommandInstallsFromHermeticRelease drives the real CLI to a
// hermetic release: exact bytes at the installed soho file, exactly two
// requests, the exact installed path/digest/release lines, PATH advice,
// and no temp residue.
func TestInstallCommandInstallsFromHermeticRelease(t *testing.T) {
	payload := []byte("soho native fixture binary " + installFixtureTag + "\n")
	srv, reqs := hermeticReleaseServer(t, installFixtureTag, runtime.GOOS, runtime.GOARCH, payload)
	home := t.TempDir()
	dir := filepath.Join(t.TempDir(), "bin") // does not exist yet
	env := installTestEnv(t, home)
	env["SOHO_RELEASE_BASE"] = srv.URL + "/releases"

	code, out, errOut := runInstallCLI(t, env, "--version", installFixtureTag, "--dir", dir)
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	name := installBinaryName()
	got, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil || string(got) != string(payload) {
		t.Fatalf("installed %s bytes = %q err=%v", name, got, err)
	}
	if n := atomic.LoadInt32(reqs); n != 2 {
		t.Fatalf("requests = %d, want exactly 2 (artifact + SHA256SUMS)", n)
	}
	sum := sha256.Sum256(payload)
	for _, line := range []string{
		"installed " + filepath.Join(dir, name),
		"sha256 " + hex.EncodeToString(sum[:]),
		"release " + srv.URL + "/releases/download/" + installFixtureTag,
		"PATH: " + dir + " is not in PATH; add it to your shell profile (no automatic edits)",
	} {
		if !strings.Contains(out, line) {
			t.Fatalf("stdout missing %q:\n%s", line, out)
		}
	}
	// The output lists the installed path first.
	if first := strings.Index(out, "installed "); first < 0 || !strings.HasPrefix(out[first:], "installed "+filepath.Join(dir, name)) {
		t.Fatalf("the first installed line must name the soho path:\n%s", out)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != name {
		t.Fatalf("dir entries = %v, want only the soho binary (no temp residue)", entries)
	}
}

// TestInstallCommandDefaultDirResolution checks the precedence: --dir beats
// SOHO_INSTALL_DIR, the env var beats the OS default, the OS default
// is explicit (HOME/.local/bin on POSIX), and a missing required variable
// fails clearly before any request or effect.
func TestInstallCommandDefaultDirResolution(t *testing.T) {
	payload := []byte("fixture binary\n")

	t.Run("flag beats env var", func(t *testing.T) {
		srv, reqs := hermeticReleaseServer(t, installFixtureTag, runtime.GOOS, runtime.GOARCH, payload)
		env := installTestEnv(t, t.TempDir())
		env["SOHO_INSTALL_DIR"] = filepath.Join(t.TempDir(), "env-dir")
		env["SOHO_RELEASE_BASE"] = srv.URL + "/releases"
		dir := filepath.Join(t.TempDir(), "flag-dir")
		code, _, errOut := runInstallCLI(t, env, "--version", installFixtureTag, "--dir", dir)
		if code != 0 {
			t.Fatalf("code=%d err=%q", code, errOut)
		}
		if _, err := os.Stat(filepath.Join(dir, installBinaryName())); err != nil {
			t.Fatalf("binary in --dir dir: %v", err)
		}
		if _, err := os.Lstat(env["SOHO_INSTALL_DIR"]); !os.IsNotExist(err) {
			t.Fatalf("env-var dir was created: %v", err)
		}
		if n := atomic.LoadInt32(reqs); n != 2 {
			t.Fatalf("requests = %d, want 2", n)
		}
	})

	t.Run("Windows LOCALAPPDATA default", func(t *testing.T) {
		if runtime.GOOS != "windows" {
			t.Skip("the Windows OS default is not exercised off-Windows")
		}
		srv, reqs := hermeticReleaseServer(t, installFixtureTag, runtime.GOOS, runtime.GOARCH, payload)
		local := t.TempDir()
		env := installTestEnv(t, t.TempDir())
		env["LOCALAPPDATA"] = local
		env["SOHO_RELEASE_BASE"] = srv.URL + "/releases"
		code, _, errOut := runInstallCLI(t, env, "--version", installFixtureTag)
		if code != 0 {
			t.Fatalf("code=%d err=%q", code, errOut)
		}
		// the documented native default is %LOCALAPPDATA%\Programs\soho
		// plus the native binary name (expected path built independently)
		if _, err := os.Stat(filepath.Join(local, "Programs", "soho", installBinaryName())); err != nil {
			t.Fatalf("binary in the LOCALAPPDATA default dir: %v", err)
		}
		if n := atomic.LoadInt32(reqs); n != 2 {
			t.Fatalf("requests = %d, want 2", n)
		}
	})

	t.Run("env var beats OS default", func(t *testing.T) {
		srv, reqs := hermeticReleaseServer(t, installFixtureTag, runtime.GOOS, runtime.GOARCH, payload)
		home := t.TempDir()
		env := installTestEnv(t, home)
		dir := filepath.Join(t.TempDir(), "env-dir")
		env["SOHO_INSTALL_DIR"] = dir
		env["SOHO_RELEASE_BASE"] = srv.URL + "/releases"
		code, _, errOut := runInstallCLI(t, env, "--version", installFixtureTag)
		if code != 0 {
			t.Fatalf("code=%d err=%q", code, errOut)
		}
		if _, err := os.Stat(filepath.Join(dir, installBinaryName())); err != nil {
			t.Fatalf("binary in env dir: %v", err)
		}
		if n := atomic.LoadInt32(reqs); n != 2 {
			t.Fatalf("requests = %d, want 2", n)
		}
		homeEntries, err := os.ReadDir(home)
		if err != nil {
			t.Fatal(err)
		}
		if len(homeEntries) != 0 {
			t.Fatalf("HOME default was used despite the env var: %v", homeEntries)
		}
	})

	t.Run("POSIX home default", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("POSIX default is not exercised on Windows")
		}
		srv, reqs := hermeticReleaseServer(t, installFixtureTag, runtime.GOOS, runtime.GOARCH, payload)
		home := t.TempDir()
		env := installTestEnv(t, home)
		env["SOHO_RELEASE_BASE"] = srv.URL + "/releases"
		code, _, errOut := runInstallCLI(t, env, "--version", installFixtureTag)
		if code != 0 {
			t.Fatalf("code=%d err=%q", code, errOut)
		}
		want := filepath.Join(home, ".local", "bin", "soho")
		if _, err := os.Stat(want); err != nil {
			t.Fatalf("binary at the HOME default: %v", err)
		}
		if n := atomic.LoadInt32(reqs); n != 2 {
			t.Fatalf("requests = %d, want 2", n)
		}
	})

	t.Run("missing required variable fails clearly", func(t *testing.T) {
		srv, reqs := hermeticReleaseServer(t, installFixtureTag, runtime.GOOS, runtime.GOARCH, payload)
		home := t.TempDir()
		env := installTestEnv(t, home)
		env["HOME"] = ""
		env["LOCALAPPDATA"] = ""
		env["SOHO_RELEASE_BASE"] = srv.URL + "/releases"
		code, _, errOut := runInstallCLI(t, env, "--version", installFixtureTag)
		if code != 2 {
			t.Fatalf("code=%d, want 2 (clear failure)", code)
		}
		required := "HOME"
		if runtime.GOOS == "windows" {
			required = "LOCALAPPDATA"
		}
		if !strings.Contains(errOut, required+" is not set") {
			t.Fatalf("stderr %q does not name the missing variable", errOut)
		}
		assertZeroRequests(t, reqs)
		assertNoDir(t, filepath.Join(home, ".local", "bin"))
		homeEntries, err := os.ReadDir(home)
		if err != nil {
			t.Fatal(err)
		}
		if len(homeEntries) != 0 {
			t.Fatalf("HOME changed despite the refusal: %v", homeEntries)
		}
	})
}

// TestInstallCommandRejectsBadInvocationBeforeEffects proves that unknown
// flags, positional arguments, and a missing flag value are refused before
// any network request or filesystem effect.
func TestInstallCommandRejectsBadInvocationBeforeEffects(t *testing.T) {
	cases := []struct {
		name string
		argv []string
	}{
		{"unknown flag", []string{"--dir", "x", "--bogus"}},
		{"positional argument", []string{"--dir", "x", "extra"}},
		{"missing flag value", []string{"--dir", "x", "--version"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, reqs := hermeticReleaseServer(t, installFixtureTag, runtime.GOOS, runtime.GOARCH, []byte("fixture"))
			cwd := t.TempDir()
			dir := filepath.Join(cwd, "x")
			env := installTestEnv(t, t.TempDir())
			env["SOHO_RELEASE_BASE"] = srv.URL + "/releases"
			code, _, errOut := runInstallCLI(t, env, tc.argv...)
			if code != 2 {
				t.Fatalf("code=%d, want 2 (err=%q)", code, errOut)
			}
			assertZeroRequests(t, reqs)
			assertNoDir(t, dir)
		})
	}
}

// TestInstallCommandNowriteRefusedBeforeEffects checks that install is
// refused by the existing native SOHO_NOWRITE read-only gate before any
// network request or filesystem effect.
func TestInstallCommandNowriteRefusedBeforeEffects(t *testing.T) {
	srv, reqs := hermeticReleaseServer(t, installFixtureTag, runtime.GOOS, runtime.GOARCH, []byte("fixture"))
	dir := filepath.Join(t.TempDir(), "bin")
	env := installTestEnv(t, t.TempDir())
	env["SOHO_RELEASE_BASE"] = srv.URL + "/releases"
	env["SOHO_NOWRITE"] = "1"
	code, _, errOut := runInstallCLI(t, env, "--version", installFixtureTag, "--dir", dir)
	if code != 2 {
		t.Fatalf("code=%d, want 2 (err=%q)", code, errOut)
	}
	if !strings.Contains(errOut, "SOHO_NOWRITE") {
		t.Fatalf("stderr %q does not name the read-only refusal", errOut)
	}
	assertZeroRequests(t, reqs)
	assertNoDir(t, dir)
}

// TestInstallCommandVersionValidationIsTheSharedRunRule checks that an
// invalid --version is refused by the core's shared validation before any
// request or filesystem effect.
func TestInstallCommandVersionValidationIsTheSharedRunRule(t *testing.T) {
	for _, bad := range []string{"not-a-tag", "v 1.2.3", "v../x"} {
		t.Run(bad, func(t *testing.T) {
			srv, reqs := hermeticReleaseServer(t, installFixtureTag, runtime.GOOS, runtime.GOARCH, []byte("fixture"))
			dir := filepath.Join(t.TempDir(), "bin")
			env := installTestEnv(t, t.TempDir())
			env["SOHO_RELEASE_BASE"] = srv.URL + "/releases"
			code, _, errOut := runInstallCLI(t, env, "--version", bad, "--dir", dir)
			if code != 1 {
				t.Fatalf("code=%d, want 1 (err=%q)", code, errOut)
			}
			if !strings.Contains(errOut, "install:") || !strings.Contains(errOut, bad) {
				t.Fatalf("stderr %q does not name the invalid version", errOut)
			}
			assertZeroRequests(t, reqs)
			assertNoDir(t, dir)
		})
	}
}

// TestInstallCommandReplacesExistingDestination checks the core's update
// path through the CLI: the existing binary is replaced by the verified
// bytes, the sibling file is untouched, and no temp file leaks.
func TestInstallCommandReplacesExistingDestination(t *testing.T) {
	payload := []byte("new fixture bytes\n")
	srv, reqs := hermeticReleaseServer(t, installFixtureTag, runtime.GOOS, runtime.GOARCH, payload)
	dir := t.TempDir()
	name := installBinaryName()
	old := []byte("old installed binary\n")
	if err := os.WriteFile(filepath.Join(dir, name), old, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := installTestEnv(t, t.TempDir())
	env["SOHO_RELEASE_BASE"] = srv.URL + "/releases"
	code, _, errOut := runInstallCLI(t, env, "--version", installFixtureTag, "--dir", dir)
	if code != 0 {
		t.Fatalf("code=%d, want 0 (err=%q)", code, errOut)
	}
	got, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil || string(got) != string(payload) {
		t.Fatalf("%s not replaced with the verified bytes: %q err=%v", name, got, err)
	}
	if n := atomic.LoadInt32(reqs); n != 2 {
		t.Fatalf("requests = %d, want 2", n)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("dir entries = %v, want the binary and keep.txt (no temp leak)", entries)
	}
}

// TestInstallCommandPreservesExistingDestinationOnBadSum checks the
// preserve-on-failure contract through the CLI: a corrupted SHA256SUMS
// entry fails the run and no file is written — the existing binary keeps
// its exact old bytes.
func TestInstallCommandPreservesExistingDestinationOnBadSum(t *testing.T) {
	payload := []byte("new fixture bytes\n")
	name := "soho_" + runtime.GOOS + "_" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	dir := t.TempDir()
	old := []byte("old installed binary\n")
	if err := os.WriteFile(filepath.Join(dir, installBinaryName()), old, 0o755); err != nil {
		t.Fatal(err)
	}
	var reqs int32
	mux := http.NewServeMux()
	mux.HandleFunc("/releases/download/"+installFixtureTag+"/"+name, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&reqs, 1)
		_, _ = w.Write(payload)
	})
	mux.HandleFunc("/releases/download/"+installFixtureTag+"/SHA256SUMS", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&reqs, 1)
		_, _ = w.Write([]byte(strings.Repeat("0", 64) + "  " + name + "\n"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	env := installTestEnv(t, t.TempDir())
	env["SOHO_RELEASE_BASE"] = srv.URL + "/releases"
	code, _, errOut := runInstallCLI(t, env, "--version", installFixtureTag, "--dir", dir)
	if code != 1 {
		t.Fatalf("code=%d, want 1 (err=%q)", code, errOut)
	}
	if !strings.Contains(errOut, "sha256") {
		t.Fatalf("stderr %q does not name the digest mismatch", errOut)
	}
	got, err := os.ReadFile(filepath.Join(dir, installBinaryName()))
	if err != nil || string(got) != string(old) {
		t.Fatalf("old binary changed on failed verification: %q err=%v", got, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("dir entries = %v, want only the old binary (no temp leak)", entries)
	}
}

// TestInstallCommandNonWritableRootRefuses checks that a non-writable
// install root is refused with no requests and no temp leak.
func TestInstallCommandNonWritableRootRefuses(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores permission bits")
	}
	if runtime.GOOS == "windows" {
		t.Skip("permission-bit refusal is not portable to Windows; the file-destination refusal covers it portably")
	}
	srv, reqs := hermeticReleaseServer(t, installFixtureTag, runtime.GOOS, runtime.GOARCH, []byte("fixture"))
	base := t.TempDir()
	t.Cleanup(func() { os.Chmod(base, 0o755) })
	if err := os.Chmod(base, 0); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "bin")
	env := installTestEnv(t, t.TempDir())
	env["SOHO_RELEASE_BASE"] = srv.URL + "/releases"
	code, _, errOut := runInstallCLI(t, env, "--version", installFixtureTag, "--dir", dir)
	if code != 1 {
		t.Fatalf("code=%d, want 1 (err=%q)", code, errOut)
	}
	assertZeroRequests(t, reqs)
	// Lstat is inconclusive while base is still unreadable; the restored
	// listing below proves nothing was created.
	if err := os.Chmod(base, 0o755); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("base has entries after the refusal: %v", entries)
	}
}

// TestInstallCommandDestIsAFileRefusesPortably proves the portable
// filesystem refusal: when the install destination exists as a regular
// file, the install is refused (the core cannot create a directory over
// it) before any remote request, with the sentinel file byte for byte
// unchanged and no temp leak. Unlike the permission-bit refusal this is
// meaningful on every OS.
func TestInstallCommandDestIsAFileRefusesPortably(t *testing.T) {
	srv, reqs := hermeticReleaseServer(t, installFixtureTag, runtime.GOOS, runtime.GOARCH, []byte("fixture"))
	base := t.TempDir()
	dest := filepath.Join(base, "bin") // a regular file, not a directory
	sentinel := []byte("sentinel file content\n")
	if err := os.WriteFile(dest, sentinel, 0o644); err != nil {
		t.Fatal(err)
	}
	env := installTestEnv(t, t.TempDir())
	env["SOHO_RELEASE_BASE"] = srv.URL + "/releases"
	code, _, errOut := runInstallCLI(t, env, "--version", installFixtureTag, "--dir", dest)
	if code != 1 {
		t.Fatalf("code=%d, want 1 (err=%q)", code, errOut)
	}
	if !strings.Contains(errOut, "install:") {
		t.Fatalf("stderr=%q, want the install refusal", errOut)
	}
	assertZeroRequests(t, reqs)
	got, err := os.ReadFile(dest)
	if err != nil || !bytes.Equal(got, sentinel) {
		t.Fatalf("sentinel file modified: %v %q", err, got)
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("base entries = %v, want only the sentinel file (no temp leak)", entries)
	}
}

// TestInstallCommandPATHAdviceIsConditional checks that the PATH note is
// printed only when the install directory is not already on PATH.
func TestInstallCommandPATHAdviceIsConditional(t *testing.T) {
	payload := []byte("fixture binary\n")
	srv, reqs := hermeticReleaseServer(t, installFixtureTag, runtime.GOOS, runtime.GOARCH, payload)
	dir := filepath.Join(t.TempDir(), "bin")
	env := installTestEnv(t, t.TempDir())
	env["PATH"] = dir + string(filepath.ListSeparator) + filepath.Join(t.TempDir(), "else")
	env["SOHO_RELEASE_BASE"] = srv.URL + "/releases"
	code, out, errOut := runInstallCLI(t, env, "--version", installFixtureTag, "--dir", dir)
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	if strings.Contains(out, "is not in PATH") {
		t.Fatalf("PATH note printed although the dir is on PATH:\n%s", out)
	}
	if n := atomic.LoadInt32(reqs); n != 2 {
		t.Fatalf("requests = %d, want 2", n)
	}

	// Windows positive control: the existing dir is on PATH with a
	// trailing backslash, a trailing slash and a different case; the
	// Windows comparison (trim + EqualFold) must recognize it, print no
	// advice, and the default install must leave the user PATH untouched.
	t.Run("Windows trailing separator and case variants match", func(t *testing.T) {
		if runtime.GOOS != "windows" {
			t.Skip("the Windows PATH comparison is not exercised off-Windows")
		}
		srv, reqs := hermeticReleaseServer(t, installFixtureTag, runtime.GOOS, runtime.GOARCH, payload)
		dir := filepath.Join(t.TempDir(), "bin")
		env := installTestEnv(t, t.TempDir())
		env["PATH"] = strings.ToUpper(dir) + `\` + string(filepath.ListSeparator) + strings.ToLower(dir) + `/`
		env["SOHO_RELEASE_BASE"] = srv.URL + "/releases"
		pathTouched := false
		old := userPathStoreFactory
		userPathStoreFactory = func() (install.UserPathStore, error) { pathTouched = true; return nil, nil }
		t.Cleanup(func() { userPathStoreFactory = old })
		code, out, errOut := runInstallCLI(t, env, "--version", installFixtureTag, "--dir", dir)
		if code != 0 {
			t.Fatalf("code=%d err=%q", code, errOut)
		}
		if strings.Contains(out, "is not in PATH") {
			t.Fatalf("erroneous PATH advice for case/separator variants:\n%s", out)
		}
		if pathTouched {
			t.Fatalf("the default install touched the user PATH on the positive control")
		}
		if n := atomic.LoadInt32(reqs); n != 2 {
			t.Fatalf("requests = %d, want 2", n)
		}
	})
}

// TestInstallCommandHelpDocumentsThePortableUsage checks that
// `install --help` prints the documented portable usage line.
func TestInstallCommandHelpDocumentsThePortableUsage(t *testing.T) {
	env := installTestEnv(t, t.TempDir())
	code, out, errOut := runInstallCLI(t, env, "--help")
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	if !strings.Contains(out, "soho install [--version vTAG] [--dir PATH]") {
		t.Fatalf("help output missing the install usage line:\n%s", out)
	}
}

// fakeUserPathStore is the in-memory registry seam for the user-PATH
// controls: it records every read/write and never touches the real
// registry.
type fakeUserPathStore struct {
	value, kind string
	reads       int
	writes      int
	readErr     error
	writeErr    error
}

func (s *fakeUserPathStore) Read() (string, string, error) {
	s.reads++
	if s.readErr != nil {
		return "", "", s.readErr
	}
	if s.kind == "" {
		s.kind = install.UserPathREGSZ
	}
	return s.value, s.kind, nil
}

func (s *fakeUserPathStore) Write(value, kind string) error {
	s.writes++
	if s.writeErr != nil {
		return s.writeErr
	}
	s.value, s.kind = value, kind
	return nil
}

// TestUserPathUpdatePure checks the Windows user-PATH pure update: append
// only when absent, presence ignoring case and trailing slash, and
// byte-for-byte preservation of the existing entries.
func TestUserPathUpdatePure(t *testing.T) {
	cases := []struct {
		current, dir string
		want         string
		changed      bool
	}{
		{"", `C:\bin`, `C:\bin`, true},
		{`C:\a`, `C:\b`, `C:\a;C:\b`, true},
		{`C:\a;D:\B`, `C:\c`, `C:\a;D:\B;C:\c`, true},
		{`C:\a`, `C:\a`, `C:\a`, false},
		{`C:\a`, `c:\A`, `C:\a`, false},
		{`C:\a\`, `C:\a`, `C:\a\`, false},
		{`C:\a`, `C:\a\`, `C:\a`, false},
		{`C:\a;`, `C:\b`, `C:\a;;C:\b`, true},
		{`C:\a\\`, `C:\a`, `C:\a\\`, false},
		{`C:\a;D:\b`, `d:\B\`, `C:\a;D:\b`, false},
		{`$USER_HOME\tools`, `C:\bin`, `$USER_HOME\tools;C:\bin`, true},
	}
	for _, tc := range cases {
		got, changed := install.UpdateUserPath(tc.current, tc.dir)
		if got != tc.want || changed != tc.changed {
			t.Fatalf("UpdateUserPath(%q, %q) = (%q, %v), want (%q, %v)", tc.current, tc.dir, got, changed, tc.want, tc.changed)
		}
	}
}

// TestUpdateUserPathValueSeam checks the orchestration over the seam: no
// write when unchanged, append preserving kind and entries, and
// read/write error propagation.
func TestUpdateUserPathValueSeam(t *testing.T) {
	t.Run("no write when unchanged", func(t *testing.T) {
		s := &fakeUserPathStore{value: `C:\a;C:\bin`, kind: install.UserPathREGExpandSZ}
		changed, err := install.UpdateUserPathValue(s, `C:\bin`)
		if err != nil || changed {
			t.Fatalf("changed=%v err=%v, want unchanged without error", changed, err)
		}
		if s.writes != 0 || s.value != `C:\a;C:\bin` {
			t.Fatalf("store mutated: writes=%d value=%q", s.writes, s.value)
		}
	})

	t.Run("append preserves kind and entries", func(t *testing.T) {
		s := &fakeUserPathStore{value: `C:\a;$USER_HOME\tools`, kind: install.UserPathREGExpandSZ}
		changed, err := install.UpdateUserPathValue(s, `C:\bin`)
		if err != nil || !changed {
			t.Fatalf("changed=%v err=%v", changed, err)
		}
		if s.writes != 1 || s.value != `C:\a;$USER_HOME\tools;C:\bin` || s.kind != install.UserPathREGExpandSZ {
			t.Fatalf("store = writes %d value %q kind %q", s.writes, s.value, s.kind)
		}
	})

	t.Run("append to empty value", func(t *testing.T) {
		s := &fakeUserPathStore{}
		changed, err := install.UpdateUserPathValue(s, `C:\bin`)
		if err != nil || !changed {
			t.Fatalf("changed=%v err=%v", changed, err)
		}
		if s.writes != 1 || s.value != `C:\bin` || s.kind != install.UserPathREGSZ {
			t.Fatalf("store = writes %d value %q kind %q", s.writes, s.value, s.kind)
		}
	})

	t.Run("propagates read error", func(t *testing.T) {
		s := &fakeUserPathStore{readErr: errors.New("registry read failed")}
		_, err := install.UpdateUserPathValue(s, `C:\bin`)
		if err == nil || !strings.Contains(err.Error(), "reading") || !strings.Contains(err.Error(), "registry read failed") {
			t.Fatalf("err = %v", err)
		}
		if s.writes != 0 {
			t.Fatalf("wrote despite the read failure: %d", s.writes)
		}
	})

	t.Run("propagates write error", func(t *testing.T) {
		s := &fakeUserPathStore{value: `C:\a`, writeErr: errors.New("registry write failed")}
		_, err := install.UpdateUserPathValue(s, `C:\bin`)
		if err == nil || !strings.Contains(err.Error(), "writing") || !strings.Contains(err.Error(), "registry write failed") {
			t.Fatalf("err = %v", err)
		}
		if s.value != `C:\a` {
			t.Fatalf("value changed on write failure: %q", s.value)
		}
	})
}

// TestInstallCommandAddToPathRejectedBeforeEffectsOnNonWindows proves the
// opt-in flag is refused before any network request or filesystem effect
// on non-Windows hosts.
func TestInstallCommandAddToPathRejectedBeforeEffectsOnNonWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the refusal is for non-Windows hosts")
	}
	srv, reqs := hermeticReleaseServer(t, installFixtureTag, runtime.GOOS, runtime.GOARCH, []byte("fixture"))
	dir := filepath.Join(t.TempDir(), "bin")
	env := installTestEnv(t, t.TempDir())
	env["SOHO_RELEASE_BASE"] = srv.URL + "/releases"
	code, _, errOut := runInstallCLI(t, env, "--add-to-path", "--version", installFixtureTag, "--dir", dir)
	if code != 2 {
		t.Fatalf("code=%d, want 2 (err=%q)", code, errOut)
	}
	if !strings.Contains(errOut, "--add-to-path") || !strings.Contains(errOut, "Windows") {
		t.Fatalf("stderr %q does not name the refusal", errOut)
	}
	assertZeroRequests(t, reqs)
	assertNoDir(t, dir)
}

// TestInstallCommandDefaultInstallDoesNotTouchUserPath proves the default
// install (no --add-to-path) never resolves or edits the user PATH: the
// store factory is not called at all.
func TestInstallCommandDefaultInstallDoesNotTouchUserPath(t *testing.T) {
	var calls int
	old := userPathStoreFactory
	userPathStoreFactory = func() (install.UserPathStore, error) {
		calls++
		return nil, errors.New("factory must not run on the default install")
	}
	t.Cleanup(func() { userPathStoreFactory = old })

	srv, reqs := hermeticReleaseServer(t, installFixtureTag, runtime.GOOS, runtime.GOARCH, []byte("fixture binary\n"))
	dir := filepath.Join(t.TempDir(), "bin")
	env := installTestEnv(t, t.TempDir())
	env["SOHO_RELEASE_BASE"] = srv.URL + "/releases"
	code, _, errOut := runInstallCLI(t, env, "--version", installFixtureTag, "--dir", dir)
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	if calls != 0 {
		t.Fatalf("user PATH store factory called %d times on the default install", calls)
	}
	if n := atomic.LoadInt32(reqs); n != 2 {
		t.Fatalf("requests = %d, want 2", n)
	}
}

// nativeArtifactServer serves a real native fakecli executable (with its
// exact bytes and SHA256SUMS) as the release artifact and returns the
// fake's config/calls directory.
func nativeArtifactServer(t *testing.T, rules []fakecli.Rule, captureEnv []string) (*httptest.Server, *int32, string, []byte) {
	t.Helper()
	binDir := t.TempDir()
	// The fake resolves its config <name>.json from its own argv[0] once
	// installed, so the fixture identity is the installed binary's base
	// name (no .exe suffix), not the old name.
	nativePath, err := fakecli.InstallWithOptions(t, binDir, "soho", rules, fakecli.InstallOptions{CaptureEnv: captureEnv})
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := os.ReadFile(nativePath)
	if err != nil {
		t.Fatal(err)
	}
	srv, reqs := hermeticReleaseServer(t, installFixtureTag, runtime.GOOS, runtime.GOARCH, artifact)
	return srv, reqs, binDir, artifact
}

// TestInstallCommandVerifyVersionOptIn proves the opt-in probe: the
// installed native binary's --version runs exactly once under the explicit
// env clone, its bounded output is printed, and the verified binary stays
// in place.
func TestInstallCommandVerifyVersionOptIn(t *testing.T) {
	srv, reqs, binDir, artifact := nativeArtifactServer(t, []fakecli.Rule{
		{Argv: []string{"--version"}, Stdout: "herdr-soho v1.2.3-fixture\n"},
	}, []string{"SOHO_BIN"})
	dir := filepath.Join(t.TempDir(), "bin")
	env := installTestEnv(t, t.TempDir())
	env["SOHO_RELEASE_BASE"] = srv.URL + "/releases"
	env["SOHO_FAKECLI_CONFIG"] = binDir
	code, out, errOut := runInstallCLI(t, env, "--version", installFixtureTag, "--dir", dir, "--verify-version")
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	if !strings.Contains(out, "version check: herdr-soho v1.2.3-fixture") {
		t.Fatalf("stdout missing the bounded version line:\n%s", out)
	}
	got, err := os.ReadFile(filepath.Join(dir, installBinaryName()))
	if err != nil || len(got) != len(artifact) {
		t.Fatalf("installed binary state: %q err=%v", got[:min(len(got), 40)], err)
	}
	if n := atomic.LoadInt32(reqs); n != 2 {
		t.Fatalf("requests = %d, want 2", n)
	}
	calls, err := fakecli.ReadCalls(filepath.Join(binDir, "soho.calls.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("the probe ran %d times, want exactly 1", len(calls))
	}
	if len(calls[0].Argv) != 1 || calls[0].Argv[0] != "--version" {
		t.Fatalf("probe argv = %v, want exactly [--version]", calls[0].Argv)
	}
	if calls[0].Env["SOHO_BIN"] != filepath.Join(dir, installBinaryName()) {
		t.Fatalf("probe env SOHO_BIN = %q, want the installed destination", calls[0].Env["SOHO_BIN"])
	}
}

// TestInstallCommandVerifyVersionNotRunByDefault proves the default
// install never executes the installed bytes: no probe, no call record.
func TestInstallCommandVerifyVersionNotRunByDefault(t *testing.T) {
	srv, _, binDir, _ := nativeArtifactServer(t, []fakecli.Rule{
		{Argv: []string{"--version"}, Stdout: "herdr-soho v1.2.3-fixture\n"},
	}, nil)
	dir := filepath.Join(t.TempDir(), "bin")
	env := installTestEnv(t, t.TempDir())
	env["SOHO_RELEASE_BASE"] = srv.URL + "/releases"
	env["SOHO_FAKECLI_CONFIG"] = binDir
	code, _, errOut := runInstallCLI(t, env, "--version", installFixtureTag, "--dir", dir)
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	if _, err := os.Stat(filepath.Join(binDir, "soho.calls.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("the installed binary was executed on the default install (err=%v)", err)
	}
}

// TestInstallCommandVerifyVersionNonZeroExitKeepsBinary proves a nonzero
// probe exit propagates clearly and the verified binary is not deleted.
func TestInstallCommandVerifyVersionNonZeroExitKeepsBinary(t *testing.T) {
	srv, _, binDir, artifact := nativeArtifactServer(t, []fakecli.Rule{
		{Argv: []string{"--version"}, Code: 3, Stderr: "fixture probe failure\n"},
	}, nil)
	dir := filepath.Join(t.TempDir(), "bin")
	env := installTestEnv(t, t.TempDir())
	env["SOHO_RELEASE_BASE"] = srv.URL + "/releases"
	env["SOHO_FAKECLI_CONFIG"] = binDir
	code, _, errOut := runInstallCLI(t, env, "--version", installFixtureTag, "--dir", dir, "--verify-version")
	if code != 1 {
		t.Fatalf("code=%d, want 1 (err=%q)", code, errOut)
	}
	if !strings.Contains(errOut, "exited with code 3") || !strings.Contains(errOut, "left at") {
		t.Fatalf("stderr %q does not propagate the failure clearly", errOut)
	}
	got, err := os.ReadFile(filepath.Join(dir, installBinaryName()))
	if err != nil || len(got) != len(artifact) {
		t.Fatalf("verified binary missing after the failed probe: %v", err)
	}
}

// TestInstallCommandVerifyVersionTimeoutKeepsBinary proves the bounded
// probe times out clearly (the child is killed) and the verified binary
// stays in place.
func TestInstallCommandVerifyVersionTimeoutKeepsBinary(t *testing.T) {
	srv, _, binDir, artifact := nativeArtifactServer(t, []fakecli.Rule{
		{Argv: []string{"--version"}, Delay: 30000},
	}, nil)
	dir := filepath.Join(t.TempDir(), "bin")
	env := installTestEnv(t, t.TempDir())
	env["SOHO_RELEASE_BASE"] = srv.URL + "/releases"
	env["SOHO_FAKECLI_CONFIG"] = binDir
	code, _, errOut := runInstallCLI(t, env, "--version", installFixtureTag, "--dir", dir, "--verify-version")
	if code != 1 {
		t.Fatalf("code=%d, want 1 (err=%q)", code, errOut)
	}
	if !strings.Contains(errOut, "timed out") {
		t.Fatalf("stderr %q does not name the timeout", errOut)
	}
	got, err := os.ReadFile(filepath.Join(dir, installBinaryName()))
	if err != nil || len(got) != len(artifact) {
		t.Fatalf("verified binary missing after the timeout: %v", err)
	}
}

// TestInstallCommandVerifyVersionRefusesNonNativeHeader proves the
// destination is validated through the shared native-header check before
// anything executes: a script artifact is never run.
func TestInstallCommandVerifyVersionRefusesNonNativeHeader(t *testing.T) {
	payload := []byte("#!/bin/sh\necho not native\n")
	srv, reqs := hermeticReleaseServer(t, installFixtureTag, runtime.GOOS, runtime.GOARCH, payload)
	dir := filepath.Join(t.TempDir(), "bin")
	env := installTestEnv(t, t.TempDir())
	env["SOHO_RELEASE_BASE"] = srv.URL + "/releases"
	code, _, errOut := runInstallCLI(t, env, "--version", installFixtureTag, "--dir", dir, "--verify-version")
	if code != 2 {
		t.Fatalf("code=%d, want 2 (fail-closed header refusal, err=%q)", code, errOut)
	}
	if !strings.Contains(errOut, "not a native binary") {
		t.Fatalf("stderr %q does not name the header refusal", errOut)
	}
	got, err := os.ReadFile(filepath.Join(dir, installBinaryName()))
	if err != nil || string(got) != string(payload) {
		t.Fatalf("installed state after the refusal: %q err=%v", got, err)
	}
	if n := atomic.LoadInt32(reqs); n != 2 {
		t.Fatalf("requests = %d, want 2", n)
	}
}

// TestPathContainsPOSIXCaseSensitive proves the PATH advice comparison is
// case-sensitive on POSIX (a case-variant entry does not count).
func TestPathContainsPOSIXCaseSensitive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX case sensitivity is not exercised on Windows")
	}
	payload := []byte("fixture binary\n")
	srv, reqs := hermeticReleaseServer(t, installFixtureTag, runtime.GOOS, runtime.GOARCH, payload)
	dir := filepath.Join(t.TempDir(), "bin")
	env := installTestEnv(t, t.TempDir())
	env["PATH"] = strings.ToUpper(dir) // distinct on a case-sensitive FS
	env["SOHO_RELEASE_BASE"] = srv.URL + "/releases"
	code, out, errOut := runInstallCLI(t, env, "--version", installFixtureTag, "--dir", dir)
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	if !strings.Contains(out, "is not in PATH") {
		t.Fatalf("PATH note missing although the case-variant must not match on POSIX:\n%s", out)
	}
	if n := atomic.LoadInt32(reqs); n != 2 {
		t.Fatalf("requests = %d, want 2", n)
	}
}

// TestInstallCommandAddToPathWindowsRouting proves the real Run flag
// routing on Windows against the in-memory store: --add-to-path updates
// the user-PATH store exactly once (exact separator+dir append, original
// kind preserved) and prints the line; the default install never touches
// the store. The real registry is never opened.
func TestInstallCommandAddToPathWindowsRouting(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the Windows --add-to-path routing is not exercised off-Windows")
	}
	srv, reqs := hermeticReleaseServer(t, installFixtureTag, runtime.GOOS, runtime.GOARCH, []byte("fixture binary\n"))
	store := &fakeUserPathStore{value: `C:\Users\tester\AppData\Roaming\Existing`, kind: install.UserPathREGExpandSZ}
	old := userPathStoreFactory
	userPathStoreFactory = func() (install.UserPathStore, error) { return store, nil }
	t.Cleanup(func() { userPathStoreFactory = old })
	dir := filepath.Join(t.TempDir(), "bin")
	env := installTestEnv(t, t.TempDir())
	env["SOHO_RELEASE_BASE"] = srv.URL + "/releases"
	code, out, errOut := runInstallCLI(t, env, "--version", installFixtureTag, "--dir", dir, "--add-to-path")
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	if store.writes != 1 {
		t.Fatalf("store writes = %d, want 1", store.writes)
	}
	if want := `C:\Users\tester\AppData\Roaming\Existing;` + dir; store.value != want {
		t.Fatalf("value = %q, want the exact separator+dir append %q", store.value, want)
	}
	if store.kind != install.UserPathREGExpandSZ {
		t.Fatalf("kind = %q, want the original kind preserved", store.kind)
	}
	if !strings.Contains(out, "user PATH: added "+dir) {
		t.Fatalf("stdout missing the user PATH line:\n%s", out)
	}
	// default install: the store must never be consulted or written
	calls := 0
	userPathStoreFactory = func() (install.UserPathStore, error) { calls++; return store, nil }
	code, _, errOut = runInstallCLI(t, env, "--version", installFixtureTag, "--dir", dir)
	if code != 0 {
		t.Fatalf("default code=%d err=%q", code, errOut)
	}
	if calls != 0 || store.writes != 1 {
		t.Fatalf("default install touched the user PATH: calls=%d writes=%d", calls, store.writes)
	}
	if n := atomic.LoadInt32(reqs); n != 4 {
		t.Fatalf("requests = %d, want 4 (two installs)", n)
	}
}

// versionProbePhases tells a failed probe's phases apart: the installed
// fake records when its --version run reached its own code, so a probe that
// timed out before that record spent the 10 s budget opening the freshly
// installed binary (the runner), and one that timed out after it spent the
// budget on the output (the drain the test guards).
func versionProbePhases(binDir string, began, ended time.Time) string {
	starts, err := fakecli.ReadCallStarts(filepath.Join(binDir, "soho.calls.jsonl"))
	if err != nil || len(starts) == 0 {
		return fmt.Sprintf("the installed binary's --version never reached the fake within the install's %v (start record: %v): its process start took the budget", ended.Sub(began).Round(time.Millisecond), err)
	}
	last := starts[len(starts)-1]
	return fmt.Sprintf("the install ran %v; its --version reached the fake %v after the install began and ran %v more", ended.Sub(began).Round(time.Millisecond), last.Sub(began).Round(time.Millisecond), ended.Sub(last).Round(time.Millisecond))
}

// TestInstallCommandVerifyVersionLargeOutputDrains proves the bounded
// stdout fix with a REAL native helper: the probe prints its version line
// and then floods several megabytes — far beyond any OS pipe capacity.
// The exec-managed drain must let the child finish promptly (no spurious
// timeout) while the captured output stays bounded to the first line.
func TestInstallCommandVerifyVersionLargeOutputDrains(t *testing.T) {
	// The probe's deadline also covers the start of the freshly installed
	// binary (a new exe on Windows): widen it so the runner's speed does
	// not decide the test; a pipe that stops draining still fails it.
	oldTimeout := verifyVersionTimeout
	verifyVersionTimeout = 60 * time.Second
	t.Cleanup(func() { verifyVersionTimeout = oldTimeout })
	flood := "herdr-soho v1.2.3-big\n" + strings.Repeat("X", 2_000_000) + "\n"
	srv, reqs, binDir, _ := nativeArtifactServer(t, []fakecli.Rule{
		{Argv: []string{"--version"}, Stdout: flood},
	}, nil)
	dir := filepath.Join(t.TempDir(), "bin")
	env := installTestEnv(t, t.TempDir())
	env["SOHO_RELEASE_BASE"] = srv.URL + "/releases"
	env["SOHO_FAKECLI_CONFIG"] = binDir
	began := time.Now()
	code, out, errOut := runInstallCLI(t, env, "--version", installFixtureTag, "--dir", dir, "--verify-version")
	if code != 0 {
		t.Fatalf("code=%d err=%q — the large output must drain to completion, not time out (%s)", code, errOut, versionProbePhases(binDir, began, time.Now()))
	}
	if !strings.Contains(out, "version check: herdr-soho v1.2.3-big") {
		t.Fatalf("stdout missing the bounded version line:\n%s", out)
	}
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "version check: ") && len(l) > versionProbeOutputLimit+16 {
			t.Fatalf("captured version line exceeds the bound: %d bytes", len(l))
		}
	}
	if _, err := os.Stat(filepath.Join(dir, installBinaryName())); err != nil {
		t.Fatalf("installed binary missing after the probe: %v", err)
	}
	if n := atomic.LoadInt32(reqs); n != 2 {
		t.Fatalf("requests = %d, want 2", n)
	}
}
