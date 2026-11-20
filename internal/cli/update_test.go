package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	skillassets "github.com/djalmajr/soho/skills"
)

// updateFixture is one release list plus the artifacts of every tag, so a
// check and a real install run against the same server. Every request is
// counted: the tests pin the requests each invocation may make, and the
// release downloads none when nothing is installed.
type updateFixture struct {
	srv     *httptest.Server
	apiReqs int32
	dlReqs  int32
	payload map[string][]byte
}

// updateFixtureServer serves the releases API at /api and one verified
// release per tag at /releases/download/<tag>/.
func updateFixtureServer(t *testing.T, tags ...string) *updateFixture {
	t.Helper()
	f := &updateFixture{payload: make(map[string][]byte, len(tags))}
	name := "soho_" + runtime.GOOS + "_" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&f.apiReqs, 1)
		items := make([]string, 0, len(tags))
		for _, tag := range tags {
			published := "2026-11-20T10:00:00Z"
			if strings.Contains(tag, "-rc.") {
				published = "2026-11-20T10:00:00Z"
			}
			items = append(items, fmt.Sprintf(`{"tag_name":%q,"draft":false,"prerelease":%t,"published_at":%q}`, tag, strings.Contains(tag, "-"), published))
		}
		_, _ = fmt.Fprintf(w, "[%s]", strings.Join(items, ","))
	})
	for _, tag := range tags {
		payload := []byte("soho fixture binary " + tag + "\n")
		f.payload[tag] = payload
		sum := sha256.Sum256(payload)
		sums := hex.EncodeToString(sum[:]) + "  " + name + "\n"
		mux.HandleFunc("/releases/download/"+tag+"/"+name, func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&f.dlReqs, 1)
			_, _ = w.Write(payload)
		})
		mux.HandleFunc("/releases/download/"+tag+"/SHA256SUMS", func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&f.dlReqs, 1)
			_, _ = w.Write([]byte(sums))
		})
	}
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *updateFixture) env(t *testing.T, home string) platform.Env {
	t.Helper()
	env := installTestEnv(t, home)
	env["SOHO_RELEASES_API"] = f.srv.URL + "/api"
	env["SOHO_RELEASE_BASE"] = f.srv.URL + "/releases"
	return env
}

func (f *updateFixture) downloads() int { return int(atomic.LoadInt32(&f.dlReqs)) }
func (f *updateFixture) apiChecks() int { return int(atomic.LoadInt32(&f.apiReqs)) }

// setBuildVersion pins the running binary's version for one test.
func setBuildVersion(t *testing.T, v string) {
	t.Helper()
	old := version
	version = v
	t.Cleanup(func() { version = old })
}

// setSelfExecutable points update's "running binary" at a fixture path.
func setSelfExecutable(t *testing.T, path string) {
	t.Helper()
	old := selfExecutable
	selfExecutable = func() (string, error) { return path, nil }
	t.Cleanup(func() { selfExecutable = old })
}

// runUpdateCLI invokes the production entry with argv (leading "update").
func runUpdateCLI(t *testing.T, env platform.Env, argv ...string) (int, string, string) {
	t.Helper()
	return runCommandCLI(t, env, "update", argv...)
}

// runCommandCLI runs one command through Run from a fresh cwd, capturing the
// output the command prints.
func runCommandCLI(t *testing.T, env platform.Env, command string, argv ...string) (int, string, string) {
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
	code := Run(append([]string{command}, argv...), env)
	return code, out.String(), errOut.String()
}

// closedUpdatePortURL is an address nothing listens on.
func closedUpdatePortURL(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return "http://" + addr + "/api"
}

func TestUpdateCheckText(t *testing.T) {
	f := updateFixtureServer(t, "v0.3.0", "v0.3.1-rc.18", "v0.3.1-rc.19")
	env := f.env(t, t.TempDir())

	t.Run("an rc build is told the newest rc", func(t *testing.T) {
		setBuildVersion(t, "v0.3.1-rc.18")
		code, out, errOut := runUpdateCLI(t, env, "--check")
		if code != 0 || errOut != "" {
			t.Fatalf("code=%d stderr=%q", code, errOut)
		}
		want := "soho v0.3.1-rc.18 · newest rc: v0.3.1-rc.19 (2026-11-20) · run: soho update\n"
		if out != want {
			t.Fatalf("stdout=%q\nwant%q", out, want)
		}
		if f.downloads() != 0 {
			t.Fatalf("--check downloaded %d artifacts; it installs nothing", f.downloads())
		}
	})

	t.Run("a stable build ignores the pre-release", func(t *testing.T) {
		setBuildVersion(t, "v0.3.0")
		code, out, _ := runUpdateCLI(t, env, "--check")
		if code != 0 {
			t.Fatalf("code=%d", code)
		}
		want := "soho v0.3.0 · up to date (stable)\n"
		if out != want {
			t.Fatalf("stdout=%q\nwant%q", out, want)
		}
	})

	t.Run("the newest of the channel is up to date", func(t *testing.T) {
		setBuildVersion(t, "v0.3.1-rc.19")
		code, out, _ := runUpdateCLI(t, env, "--check")
		if code != 0 {
			t.Fatalf("code=%d", code)
		}
		want := "soho v0.3.1-rc.19 · up to date (rc)\n"
		if out != want {
			t.Fatalf("stdout=%q\nwant%q", out, want)
		}
	})

	t.Run("--channel stable overrides an rc build", func(t *testing.T) {
		setBuildVersion(t, "v0.3.1-rc.18")
		code, out, _ := runUpdateCLI(t, env, "--check", "--channel", "stable")
		if code != 0 {
			t.Fatalf("code=%d", code)
		}
		want := "soho v0.3.1-rc.18 · up to date (stable)\n"
		if out != want {
			t.Fatalf("stdout=%q\nwant%q", out, want)
		}
	})
}

func TestUpdateCheckJSON(t *testing.T) {
	f := updateFixtureServer(t, "v0.3.1-rc.18", "v0.3.1-rc.19")
	env := f.env(t, t.TempDir())
	setBuildVersion(t, "v0.3.1-rc.18")

	code, out, errOut := runUpdateCLI(t, env, "--check", "--json")
	if code != 0 || errOut != "" {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	want := `{"schema":1,"current":"v0.3.1-rc.18","channel":"rc","newest":"v0.3.1-rc.19","published":"2026-11-20","update_available":true,"checked":true,"error":null}` + "\n"
	if out != want {
		t.Fatalf("stdout=%q\nwant%q", out, want)
	}

	// Up to date: the same document with update_available false.
	setBuildVersion(t, "v0.3.1-rc.19")
	code, out, _ = runUpdateCLI(t, env, "--check", "--json")
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	want = `{"schema":1,"current":"v0.3.1-rc.19","channel":"rc","newest":"v0.3.1-rc.19","published":"2026-11-20","update_available":false,"checked":true,"error":null}` + "\n"
	if out != want {
		t.Fatalf("stdout=%q\nwant%q", out, want)
	}
}

func TestUpdateCheckNetworkFailureIsNotChecked(t *testing.T) {
	// The releases API answers 500: the check is "could not check", exit 1,
	// and never "up to date".
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	env := installTestEnv(t, t.TempDir())
	env["SOHO_RELEASES_API"] = srv.URL
	setBuildVersion(t, "v0.3.1-rc.18")

	code, out, errOut := runUpdateCLI(t, env, "--check")
	if code != 1 {
		t.Fatalf("code=%d, want 1; stderr=%q", code, errOut)
	}
	want := "soho v0.3.1-rc.18 · could not check for updates: update: the releases API answered 500 Internal Server Error\n"
	if out != want {
		t.Fatalf("stdout=%q\nwant%q", out, want)
	}
	if strings.Contains(out, "up to date") {
		t.Fatalf("a failed check reported up to date: %q", out)
	}

	code, out, _ = runUpdateCLI(t, env, "--check", "--json")
	if code != 1 {
		t.Fatalf("json code=%d, want 1", code)
	}
	for _, want := range []string{
		`"current":"v0.3.1-rc.18"`, `"channel":"rc"`, `"newest":null`, `"published":null`,
		`"update_available":false`, `"checked":false`, `"error":"update: the releases API answered 500 Internal Server Error"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("JSON %q misses %q", out, want)
		}
	}
}

func TestUpdateInstallsTheChannelNewest(t *testing.T) {
	f := updateFixtureServer(t, "v0.3.0", "v0.3.1-rc.18", "v0.3.1-rc.19")
	dir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	running := filepath.Join(dir, installBinaryName())
	if err := os.WriteFile(running, []byte("old running binary\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	setSelfExecutable(t, running)
	setBuildVersion(t, "v0.3.1-rc.18")

	// A home skill that differs from the embedded one: the advice line names
	// the installed tag and is printed only (PATH holds no npx).
	home := t.TempDir()
	skillDir := filepath.Join(home, ".agents", "skills", "soho")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("stale skill\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := f.env(t, home)

	code, out, errOut := runUpdateCLI(t, env)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	got, err := os.ReadFile(running)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(f.payload["v0.3.1-rc.19"]) {
		t.Fatalf("installed bytes = %q, want the v0.3.1-rc.19 artifact", got)
	}
	if f.downloads() != 2 {
		t.Fatalf("downloads = %d, want exactly the artifact and SHA256SUMS", f.downloads())
	}
	sum := sha256.Sum256(f.payload["v0.3.1-rc.19"])
	for _, line := range []string{
		"installed " + running,
		"sha256 " + hex.EncodeToString(sum[:]),
		"release " + f.srv.URL + "/releases/download/v0.3.1-rc.19",
		"next: run 'soho setup --local' in each project to refresh the instruction block",
		`update the home skill to this release: npx -y skills add "djalmajr/soho#v0.3.1-rc.19" --global --skill soho -y`,
	} {
		if !strings.Contains(out, line) {
			t.Fatalf("stdout misses %q:\n%s", line, out)
		}
	}
}

// TestUpdateSkillAdviceFollowsTheHomeSkillPresence is the round-2 F4 rule: the
// advice no longer compares with the still-running process's embedded skill,
// so an existing home skill is always told to move to the installed tag, and a
// missing one says nothing.
func TestUpdateSkillAdviceFollowsTheHomeSkillPresence(t *testing.T) {
	const want = `update the home skill to this release: npx -y skills add "djalmajr/soho#v0.3.1-rc.19" --global --skill soho -y`
	t.Run("an existing home skill is told to update, even when it matches the running bundle", func(t *testing.T) {
		f := updateFixtureServer(t, "v0.3.1-rc.19")
		setBuildVersion(t, "v0.3.1-rc.18")
		setSelfExecutable(t, filepath.Join(t.TempDir(), installBinaryName()))
		home := t.TempDir()
		parent := filepath.Join(home, ".agents", "skills")
		if err := os.MkdirAll(parent, 0o755); err != nil {
			t.Fatal(err)
		}
		bundled, err := skillassets.Materialize(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		skillDir := filepath.Join(parent, "soho")
		if err := os.Symlink(bundled, skillDir); err != nil {
			t.Fatal(err)
		}
		if same, err := skillassets.MatchesEmbedded(skillDir); err != nil || !same {
			t.Fatalf("invalid fixture: the home skill matches the running bundle (%t, %v)", same, err)
		}
		code, out, errOut := runUpdateCLI(t, f.env(t, home))
		if code != 0 {
			t.Fatalf("code=%d stderr=%q", code, errOut)
		}
		if !strings.Contains(out, want) {
			t.Fatalf("stdout misses %q:\n%s", want, out)
		}
	})
	t.Run("no home skill prints nothing about it", func(t *testing.T) {
		f := updateFixtureServer(t, "v0.3.1-rc.19")
		setBuildVersion(t, "v0.3.1-rc.18")
		setSelfExecutable(t, filepath.Join(t.TempDir(), installBinaryName()))
		code, out, errOut := runUpdateCLI(t, f.env(t, t.TempDir()))
		if code != 0 {
			t.Fatalf("code=%d stderr=%q", code, errOut)
		}
		if strings.Contains(out, "skills add") {
			t.Fatalf("the advice line printed without a home skill:\n%s", out)
		}
	})
}

func TestUpdateAlreadyUpToDateInstallsNothing(t *testing.T) {
	f := updateFixtureServer(t, "v0.3.1-rc.18")
	running := filepath.Join(t.TempDir(), installBinaryName())
	setSelfExecutable(t, running)
	setBuildVersion(t, "v0.3.1-rc.18")
	env := f.env(t, t.TempDir())

	code, out, errOut := runUpdateCLI(t, env)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	want := "soho v0.3.1-rc.18 · already up to date (rc)\n"
	if out != want {
		t.Fatalf("stdout=%q\nwant%q", out, want)
	}
	if f.downloads() != 0 {
		t.Fatalf("downloads = %d, want 0 when already up to date", f.downloads())
	}
	if _, err := os.Lstat(running); !os.IsNotExist(err) {
		t.Fatalf("the destination changed when already up to date: %v", err)
	}
}

func TestUpdateRefusesADowngrade(t *testing.T) {
	f := updateFixtureServer(t, "v0.3.0")
	dir := t.TempDir()
	running := filepath.Join(dir, installBinaryName())
	setSelfExecutable(t, running)
	setBuildVersion(t, "v0.3.1")
	env := f.env(t, t.TempDir())

	code, _, errOut := runUpdateCLI(t, env)
	if code != 2 {
		t.Fatalf("code=%d, want 2 (the downgrade refusal)", code)
	}
	if !strings.Contains(errOut, "the newest stable release v0.3.0 is lower than the running v0.3.1") ||
		!strings.Contains(errOut, "soho update --version v0.3.0") {
		t.Fatalf("stderr=%q does not explain the refusal and the way out", errOut)
	}
	if f.downloads() != 0 {
		t.Fatalf("downloads = %d, want 0 before the refusal", f.downloads())
	}
	if _, err := os.Lstat(running); !os.IsNotExist(err) {
		t.Fatalf("the refused update wrote the destination: %v", err)
	}
}

func TestUpdateExplicitVersionInstallsIt(t *testing.T) {
	f := updateFixtureServer(t, "v0.3.0")
	dir := t.TempDir()
	running := filepath.Join(dir, installBinaryName())
	setSelfExecutable(t, running)
	setBuildVersion(t, "v0.3.1")
	env := f.env(t, t.TempDir())

	code, out, errOut := runUpdateCLI(t, env, "--version", "v0.3.0")
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	got, err := os.ReadFile(running)
	if err != nil || string(got) != string(f.payload["v0.3.0"]) {
		t.Fatalf("installed bytes = %q, err=%v; want the v0.3.0 artifact", got, err)
	}
	if f.apiChecks() != 0 {
		t.Fatalf("--version read the releases API %d times; it names the release", f.apiChecks())
	}
	if !strings.Contains(out, "release "+f.srv.URL+"/releases/download/v0.3.0") {
		t.Fatalf("stdout=%q does not name the installed release", out)
	}
}

func TestUpdateRejectsBadInvocationBeforeTheNetwork(t *testing.T) {
	for _, tc := range []struct {
		name string
		argv []string
		want string
	}{
		{"--check with --version", []string{"--check", "--version", "v0.3.0"}, "--check installs nothing"},
		{"--json without --check", []string{"--json"}, "--json applies to --check"},
		{"--channel with --version", []string{"--version", "v0.3.0", "--channel", "stable"}, "--version names the release"},
		{"an unknown channel", []string{"--check", "--channel", "beta"}, "unknown channel"},
		{"an invalid --version tag", []string{"--version", "not-a-tag"}, "must begin with v"},
		{"a positional argument", []string{"extra"}, "unexpected arguments"},
		{"an unknown flag", []string{"--bogus"}, "flag provided but not defined"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := updateFixtureServer(t, "v0.3.0")
			env := f.env(t, t.TempDir())
			setBuildVersion(t, "v0.3.1-rc.18")
			setSelfExecutable(t, filepath.Join(t.TempDir(), installBinaryName()))
			code, _, errOut := runUpdateCLI(t, env, tc.argv...)
			if code != 2 {
				t.Fatalf("code=%d, want 2; stderr=%q", code, errOut)
			}
			if !strings.Contains(errOut, tc.want) {
				t.Fatalf("stderr=%q misses %q", errOut, tc.want)
			}
			if f.apiChecks() != 0 || f.downloads() != 0 {
				t.Fatalf("requests = %d api, %d downloads; want none before the refusal", f.apiChecks(), f.downloads())
			}
		})
	}
}

func TestUpdateNowriteAdmitsOnlyTheCheck(t *testing.T) {
	f := updateFixtureServer(t, "v0.3.1-rc.19")
	setBuildVersion(t, "v0.3.1-rc.18")
	setSelfExecutable(t, filepath.Join(t.TempDir(), installBinaryName()))

	t.Run("update --check runs read-only", func(t *testing.T) {
		env := f.env(t, t.TempDir())
		env["SOHO_NOWRITE"] = "1"
		code, out, errOut := runUpdateCLI(t, env, "--check")
		if code != 0 || errOut != "" {
			t.Fatalf("code=%d stderr=%q", code, errOut)
		}
		if !strings.Contains(out, "newest rc: v0.3.1-rc.19") {
			t.Fatalf("stdout=%q", out)
		}
	})
	t.Run("update --check --json --channel rc runs read-only", func(t *testing.T) {
		env := f.env(t, t.TempDir())
		env["SOHO_NOWRITE"] = "1"
		code, out, _ := runUpdateCLI(t, env, "--check", "--json", "--channel", "rc")
		if code != 0 || !strings.Contains(out, `"checked":true`) {
			t.Fatalf("code=%d stdout=%q", code, out)
		}
	})
	t.Run("update installs nothing under NOWRITE", func(t *testing.T) {
		env := f.env(t, t.TempDir())
		env["SOHO_NOWRITE"] = "1"
		before := f.apiChecks()
		code, _, errOut := runUpdateCLI(t, env)
		if code != 2 || !strings.Contains(errOut, "SOHO_NOWRITE") {
			t.Fatalf("code=%d stderr=%q", code, errOut)
		}
		if f.apiChecks() != before {
			t.Fatalf("the refused update reached the network")
		}
	})
}

func TestUpdateRunsUnderTransportHc(t *testing.T) {
	// update is in the hc allowlist: it touches no transport.
	if !hcCommandAllowed("update") {
		t.Fatal("hcCommandAllowed(update) = false; update runs outside the transport")
	}
	if !knownCommands["update"] {
		t.Fatal("update is not in knownCommands")
	}
}

func TestUpdateHelpPrintsItsLines(t *testing.T) {
	env := installTestEnv(t, t.TempDir())
	code, out, errOut := runUpdateCLI(t, env, "--help")
	if code != 0 || errOut != "" {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	// The three accepted forms are separate synopses (round-2 F7): the flags
	// are not presented as independently optional. help update prints the
	// whole block, continuation lines included.
	for _, line := range []string{
		"soho update --check [--json] [--channel stable|rc]",
		"soho update [--channel stable|rc]",
		"soho update --version vTAG",
		"update available: <tag> (soho update)",
		"update_check=off",
	} {
		if !strings.Contains(out, line) {
			t.Fatalf("update --help misses %q:\n%s", line, out)
		}
	}
	if strings.Contains(out, "soho update [--check] [--json] [--channel stable|rc] [--version vTAG]") {
		t.Fatalf("update --help still presents the flags as independently optional:\n%s", out)
	}
}

func TestInstallWithoutVersionFollowsTheChannel(t *testing.T) {
	t.Run("an rc build resolves the newest rc", func(t *testing.T) {
		f := updateFixtureServer(t, "v0.3.0", "v0.3.1-rc.19")
		dir := filepath.Join(t.TempDir(), "bin")
		setBuildVersion(t, "v0.3.1-rc.18")
		code, out, errOut := runInstallCLI(t, f.env(t, t.TempDir()), "--dir", dir)
		if code != 0 {
			t.Fatalf("code=%d stderr=%q", code, errOut)
		}
		if !strings.Contains(out, "tag v0.3.1-rc.19 (channel rc)\n") {
			t.Fatalf("stdout=%q does not name the chosen tag", out)
		}
		got, err := os.ReadFile(filepath.Join(dir, installBinaryName()))
		if err != nil || string(got) != string(f.payload["v0.3.1-rc.19"]) {
			t.Fatalf("installed bytes = %q, err=%v", got, err)
		}
	})

	t.Run("a stable build resolves the newest stable", func(t *testing.T) {
		f := updateFixtureServer(t, "v0.3.1", "v0.3.1-rc.19")
		dir := filepath.Join(t.TempDir(), "bin")
		setBuildVersion(t, "v0.3.0")
		code, out, errOut := runInstallCLI(t, f.env(t, t.TempDir()), "--dir", dir)
		if code != 0 {
			t.Fatalf("code=%d stderr=%q", code, errOut)
		}
		if !strings.Contains(out, "tag v0.3.1 (channel stable)\n") {
			t.Fatalf("stdout=%q does not name the chosen tag", out)
		}
		got, err := os.ReadFile(filepath.Join(dir, installBinaryName()))
		if err != nil || string(got) != string(f.payload["v0.3.1"]) {
			t.Fatalf("installed bytes = %q, err=%v", got, err)
		}
	})

	t.Run("a fresh install from dev takes the newest stable", func(t *testing.T) {
		f := updateFixtureServer(t, "v0.9.0", "v1.0.0-rc.1")
		dir := filepath.Join(t.TempDir(), "bin")
		setBuildVersion(t, "dev")
		code, out, errOut := runInstallCLI(t, f.env(t, t.TempDir()), "--dir", dir)
		if code != 0 {
			t.Fatalf("code=%d stderr=%q", code, errOut)
		}
		if !strings.Contains(out, "tag v0.9.0 (channel stable)\n") {
			t.Fatalf("stdout=%q does not name the chosen tag", out)
		}
		got, err := os.ReadFile(filepath.Join(dir, installBinaryName()))
		if err != nil || string(got) != string(f.payload["v0.9.0"]) {
			t.Fatalf("installed bytes = %q, err=%v", got, err)
		}
	})

	t.Run("a lower tag needs --version", func(t *testing.T) {
		f := updateFixtureServer(t, "v0.8.0")
		dir := filepath.Join(t.TempDir(), "bin")
		setBuildVersion(t, "v0.9.0")
		code, _, errOut := runInstallCLI(t, f.env(t, t.TempDir()), "--dir", dir)
		if code != 2 {
			t.Fatalf("code=%d, want 2 (the downgrade refusal)", code)
		}
		if !strings.Contains(errOut, "lower than the running v0.9.0") || !strings.Contains(errOut, "soho install --version v0.8.0") {
			t.Fatalf("stderr=%q does not explain the refusal and the way out", errOut)
		}
		if f.downloads() != 0 {
			t.Fatalf("downloads = %d, want 0 before the refusal", f.downloads())
		}
	})
}

func TestInstallWithVersionNeverReadsTheReleaseList(t *testing.T) {
	f := updateFixtureServer(t, installFixtureTag)
	setBuildVersion(t, "v9.9.9")
	dir := filepath.Join(t.TempDir(), "bin")
	code, _, errOut := runInstallCLI(t, f.env(t, t.TempDir()), "--version", installFixtureTag, "--dir", dir)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	if f.apiChecks() != 0 {
		t.Fatalf("install --version read the releases API %d times", f.apiChecks())
	}
}

func TestInstallWithoutVersionReportsAnUnreachableReleaseList(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bin")
	env := installTestEnv(t, t.TempDir())
	env["SOHO_RELEASES_API"] = closedUpdatePortURL(t)
	setBuildVersion(t, "v0.9.0")
	code, _, errOut := runInstallCLI(t, env, "--dir", dir)
	if code != 1 {
		t.Fatalf("code=%d, want 1", code)
	}
	if !strings.Contains(errOut, "could not resolve the newest stable release") {
		t.Fatalf("stderr=%q", errOut)
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatalf("the install dir was created after a failed resolution: %v", err)
	}
}

func TestUpdateCheckConfigKey(t *testing.T) {
	// The key is settable and readable: ConfigKeyOk accepts it, the embedded
	// defaults ship it as on, and an explicit layer wins.
	if !core.ConfigKeyOk("update_check") {
		t.Fatal("ConfigKeyOk(update_check) = false; config set would refuse the key")
	}
	env := installTestEnv(t, t.TempDir())
	env["SOHO_SKILL_DIR"] = testSkillDir(t)
	code, out, errOut := runCommandCLI(t, env, "config", "get", "update_check")
	if code != 0 || errOut != "" || strings.TrimSpace(out) != "on" {
		t.Fatalf("config get update_check: code=%d out=%q err=%q", code, out, errOut)
	}
	env["SOHO_UPDATE_CHECK"] = "off"
	code, out, errOut = runCommandCLI(t, env, "config", "get", "update_check")
	if code != 0 || errOut != "" || strings.TrimSpace(out) != "off" {
		t.Fatalf("config get update_check with the env layer: code=%d out=%q err=%q", code, out, errOut)
	}
}

func TestUpdateChannelComesFromTheBinaryNotTheEnvironment(t *testing.T) {
	// [identity] The channel is the running version's own suffix: an
	// environment variable cannot move an rc build to the stable channel (or
	// the reverse). Only --channel can, and it is an explicit flag.
	f := updateFixtureServer(t, "v0.3.0", "v0.3.1-rc.19")
	env := f.env(t, t.TempDir())
	env["SOHO_CHANNEL"] = "stable"
	env["CHANNEL"] = "stable"
	setBuildVersion(t, "v0.3.1-rc.18")
	code, out, errOut := runUpdateCLI(t, env, "--check")
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	if !strings.Contains(out, "newest rc: v0.3.1-rc.19") {
		t.Fatalf("the environment changed the channel: %q", out)
	}

	// The reverse: a stable build stays stable with the env naming rc.
	env["SOHO_CHANNEL"] = "rc"
	setBuildVersion(t, "v0.3.0")
	code, out, _ = runUpdateCLI(t, env, "--check")
	if code != 0 || !strings.Contains(out, "up to date (stable)") {
		t.Fatalf("the environment changed the channel: code=%d out=%q", code, out)
	}
}

// TestUpdateCheckAdviceUsesTheSelectedChannel is the round-2 F3 rule: the
// printed command re-selects the channel the check used, and following it
// installs the advertised tag.
func TestUpdateCheckAdviceUsesTheSelectedChannel(t *testing.T) {
	f := updateFixtureServer(t, "v0.3.0", "v0.3.1-rc.19")
	setBuildVersion(t, "v0.3.0")
	dir := t.TempDir()
	running := filepath.Join(dir, installBinaryName())
	if err := os.WriteFile(running, []byte("old running binary\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	setSelfExecutable(t, running)
	env := f.env(t, t.TempDir())

	code, out, errOut := runUpdateCLI(t, env, "--check", "--channel", "rc")
	if code != 0 || errOut != "" {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	want := "soho v0.3.0 · newest rc: v0.3.1-rc.19 (2026-11-20) · run: soho update --channel rc\n"
	if out != want {
		t.Fatalf("stdout=%q\nwant%q", out, want)
	}
	// Follow the printed command the way a user would.
	_, advice, ok := strings.Cut(strings.TrimSpace(out), " · run: ")
	if !ok {
		t.Fatalf("stdout=%q carries no advice", out)
	}
	fields := strings.Fields(advice)
	if len(fields) != 4 || fields[0] != "soho" || fields[1] != "update" {
		t.Fatalf("advice = %q, want `soho update --channel rc`", advice)
	}
	code, installed, errOut := runUpdateCLI(t, env, fields[2:]...)
	if code != 0 || errOut != "" {
		t.Fatalf("following %q: code=%d stderr=%q", advice, code, errOut)
	}
	if !strings.Contains(installed, "installed "+running) {
		t.Fatalf("stdout=%q does not name the installed destination", installed)
	}
	got, err := os.ReadFile(running)
	if err != nil || string(got) != string(f.payload["v0.3.1-rc.19"]) {
		t.Fatalf("installed bytes = %q, err=%v; want the v0.3.1-rc.19 artifact", got, err)
	}
}

// TestUpdateRefusesANonCanonicalRunningBinary is the round-2 F5 rule: a
// renamed binary next to a canonical installation is refused before any
// request, and neither file is replaced.
func TestUpdateRefusesANonCanonicalRunningBinary(t *testing.T) {
	f := updateFixtureServer(t, "v0.3.1")
	setBuildVersion(t, "v0.3.0")
	dir := t.TempDir()
	running := filepath.Join(dir, "custom-"+installBinaryName())
	canonical := filepath.Join(dir, installBinaryName())
	if err := os.WriteFile(running, []byte("running binary\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(canonical, []byte("separate installation\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(running)
	if err != nil {
		t.Fatal(err)
	}
	resolvedDir := filepath.Dir(resolved)
	setSelfExecutable(t, running)
	env := f.env(t, t.TempDir())

	code, out, errOut := runUpdateCLI(t, env)
	if code != 2 {
		t.Fatalf("code=%d, want 2; stdout=%q stderr=%q", code, out, errOut)
	}
	want := fmt.Sprintf("update: the running binary is %s, not %s; run soho install --dir %s or rename it",
		resolved, filepath.Join(resolvedDir, installBinaryName()), resolvedDir)
	if !strings.Contains(errOut, want) {
		t.Fatalf("stderr=%q misses %q", errOut, want)
	}
	if f.downloads() != 0 || f.apiChecks() != 0 {
		t.Fatalf("requests = %d api, %d downloads; want none before the refusal", f.apiChecks(), f.downloads())
	}
	if got, err := os.ReadFile(running); err != nil || string(got) != "running binary\n" {
		t.Fatalf("the renamed binary changed: %q, %v", got, err)
	}
	if got, err := os.ReadFile(canonical); err != nil || string(got) != "separate installation\n" {
		t.Fatalf("the sibling installation changed: %q, %v", got, err)
	}
}
