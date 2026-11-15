package platform

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// runCliFixtureRoleEnv marks the re-executed fixture program: the RunCli
// tests install a copy of this test binary on the fixture PATH and the
// production code under test execs it with its own argv (no -test flags
// reach the re-execution), so the role rides in the environment and is
// handled here before any test runs.
const runCliFixtureRoleEnv = "HERDR_SOHO_RUNCLI_FIXTURE_ROLE"

// runCliFixturePidfileEnv names the file a fixture parent publishes with
// the owned record of its grandchild (pid, start identity, name), so the
// test owns (and stops) every process the fixture started.
const runCliFixturePidfileEnv = "HERDR_SOHO_RUNCLI_FIXTURE_PIDFILE"

// runCliFixturePsPathEnv carries the trusted ps lookup PATH the test
// explicitly provides: the fixture role process runs with an isolated
// PATH containing only the fixture program, so an ambient ps lookup from
// inside the role finds nothing; the role uses exactly this value, and
// nothing else, to read the grandchild's identity with the native
// platform ProcInfo behavior.
const runCliFixturePsPathEnv = "HERDR_SOHO_RUNCLI_FIXTURE_PS_PATH"

func TestMain(m *testing.M) {
	if role := os.Getenv(runCliFixtureRoleEnv); role != "" {
		os.Exit(runRunCliFixtureRole(role))
	}
	fakecli.RunTests(m)
}

// runRunCliFixtureRole is the fixture role inside the re-executed test
// binary. Every role keeps the default signal handling unless it says
// otherwise, so the production timeout/stop paths stop it exactly as they
// stop any child; the observable streams, exit codes, signal behavior and
// pipe holding are the same the old sh scripts gave (the program is named
// for what it replaces, not an interpreter standing behind the name).
func runRunCliFixtureRole(role string) int {
	switch role {
	case "out-err-5":
		os.Stdout.WriteString("out-line\n")
		os.Stderr.WriteString("err-line\n")
		return 5
	case "out-err":
		os.Stdout.WriteString("out\n")
		os.Stderr.WriteString("err\n")
		return 0
	case "cat-err":
		if _, err := io.Copy(os.Stdout, os.Stdin); err != nil {
			os.Stderr.WriteString("err")
			return 6
		}
		os.Stderr.WriteString("err")
		return 7
	case "pwd":
		cwd, err := os.Getwd()
		if err != nil {
			return 6
		}
		os.Stdout.WriteString(cwd + "\n")
		return 0
	case "self-signal":
		// os.Process.Kill is SIGKILL on Unix (the shape the test asserts);
		// the role is only reached from the POSIX subtests.
		if p, err := os.FindProcess(os.Getpid()); err == nil {
			_ = p.Kill()
		}
		return 0 // unreachable: the signal stops the process
	case "hold-300", "hold-5000", "hold-12000":
		ms, err := strconv.Atoi(role[len("hold-"):])
		if err != nil {
			return 127
		}
		time.Sleep(time.Duration(ms) * time.Millisecond)
		return 0
	case "tree-hold":
		// Parent plus a grandchild that holds the inherited pipes: the
		// owned stand-in for the old `sleep 5 & sleep 5` (the grandchild
		// outlives the parent's default SIGTERM and bounds the wait
		// delay on its own).
		child := runCliFixtureGrandchild("hold-5000")
		if path := os.Getenv(runCliFixturePidfileEnv); path != "" {
			_ = publishFixtureOwnedRecord(path, child)
		}
		_ = child.Wait()
		return 0
	case "pidfile-parent":
		// The grandchild holds the pipes while the parent exits at once:
		// the owned stand-in for `sleep 12 & echo $! > $PID_FILE; exit 0`.
		child := runCliFixtureGrandchild("hold-12000")
		path := os.Getenv("PID_FILE")
		if path != "" {
			if err := publishFixtureOwnedRecord(path, child); err != nil {
				return 6
			}
		}
		return 0
	case "daemonize":
		// A grandchild inherits the pipes and outlives the parent:
		// the owned stand-in for `sleep 0.3 & printf 'daemonized\n'`.
		child := runCliFixtureGrandchild("hold-300")
		if path := os.Getenv(runCliFixturePidfileEnv); path != "" {
			_ = publishFixtureOwnedRecord(path, child)
		}
		os.Stdout.WriteString("daemonized\n")
		return 0
	case "trap-term-0", "trap-term-3":
		// Traps SIGTERM, prints and exits with the named code after the
		// 20 ms the node scripts used: the owned stand-in for the
		// `process.on('SIGTERM', …)` node cases.
		code, err := strconv.Atoi(role[len("trap-term-"):])
		if err != nil {
			return 127
		}
		term := make(chan os.Signal, 1)
		signal.Notify(term, syscall.SIGTERM)
		<-term
		os.Stdout.WriteString("caught\n")
		time.Sleep(20 * time.Millisecond)
		os.Exit(code)
	case "badutf":
		// The exact bytes the old printf wrote, on the same streams.
		os.Stdout.Write([]byte{0x61, 0xFF, 0x62, 0xC0, 0x80, 0x63, 0xED, 0xA0, 0x80, 0x64, 0xF0, 0x90, 0x80, 0x65, 0xE2, 0x82})
		os.Stderr.Write([]byte{0x78, 0xFE, 0x79, 0xF2, 0x80})
		return 0
	}
	os.Stderr.WriteString("fixture: unknown role " + role + "\n")
	return 127
}

// runCliFixtureGrandchild starts the re-executed test binary in another
// fixture role and swaps only the role marker. The stdout and stderr are
// assigned explicitly to the parent's own stdout/stderr (the pipes the
// production wait reads): the exec default would point them at the
// dev-null, and a grandchild that does not hold the pipes cannot delay
// the wait the delay is meant to bound. The stdin keeps the exec default
// (/dev/null): the grandchild roles only write, they never read.
func runCliFixtureGrandchild(role string) *exec.Cmd {
	exe, err := os.Executable()
	if err != nil {
		os.Exit(1)
	}
	cmd := exec.Command(exe)
	items := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if ok && key == runCliFixtureRoleEnv {
			continue
		}
		items = append(items, entry)
	}
	items = append(items, runCliFixtureRoleEnv+"="+role)
	cmd.Env = items
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		os.Exit(1)
	}
	return cmd
}

// runCliFixtureProgram places an independent copy of the test binary under
// dir with the given name: the native fixture program the RunCli tests
// exec, replacing the old sh scripts. A hard link when supported (Unix), a
// byte copy otherwise, and on Windows always a byte copy. A loaded parent
// image can keep a hard-linked fixture in use on Windows Server CI; an
// independent copy separates cleanup from the parent's lifetime.
func runCliFixtureProgram(t *testing.T, dir, name string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locating the test binary: %v", err)
	}
	target := filepath.Join(dir, name)
	if runtime.GOOS == "windows" {
		// A loaded parent image can prevent hard-link cleanup on Windows.
		// Keep this fixture independent of the running parent image.
		if data, readErr := os.ReadFile(exe); readErr != nil {
			t.Fatalf("reading the test binary: %v", readErr)
		} else if writeErr := os.WriteFile(target, data, 0o755); writeErr != nil {
			t.Fatalf("writing the fixture program: %v", writeErr)
		}
		return
	}
	if err := os.Link(exe, target); err != nil {
		data, readErr := os.ReadFile(exe)
		if readErr != nil {
			t.Fatalf("reading the test binary: %v", readErr)
		}
		if writeErr := os.WriteFile(target, data, 0o755); writeErr != nil {
			t.Fatalf("writing the fixture program: %v", writeErr)
		}
	}
}

// fixtureOwnedRecord is the readiness/PID record a fixture parent
// publishes about its grandchild: the pid, the exact native start
// identity the platform read (the unix lstart, the windows creation
// ticks) and the command base name, captured while the parent still owns
// the live exec.Cmd and before the record is published. The record is
// the only basis the cleanup uses to stop the grandchild.
type fixtureOwnedRecord struct {
	Pid     int    `json:"pid"`
	Started string `json:"started"`
	Name    string `json:"name"`
}

// fixtureChildIdentity reads one grandchild's start identity and command
// base name with the native platform ProcInfo behavior, through the
// trusted ps lookup PATH the test provided (the role's own PATH is the
// isolated fixture bin and would not find ps; on windows the read is a
// native process API and the PATH is inert); an unreadable read names
// nothing, and nothing is fabricated.
func fixtureChildIdentity(pid int) (string, string) {
	path := os.Getenv(runCliFixturePsPathEnv)
	if path == "" {
		return "", ""
	}
	started, name, ok := ProcInfo(pid, Env{"PATH": path})
	if !ok {
		return "", ""
	}
	return started, name
}

// publishFixtureOwnedRecord captures the pid, start identity and name
// from the live exec.Cmd the caller still owns and publishes the JSON
// record: the identity is read before the record exists, so the cleanup
// can later validate the original identity against it.
func publishFixtureOwnedRecord(path string, cmd *exec.Cmd) error {
	started, name := fixtureChildIdentity(cmd.Process.Pid)
	record := fixtureOwnedRecord{Pid: cmd.Process.Pid, Started: started, Name: name}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// readFixtureOwnedRecord waits (bounded) for the published record and
// parses it; a record without a positive pid names no owned process.
func readFixtureOwnedRecord(t *testing.T, path string) fixtureOwnedRecord {
	t.Helper()
	until := time.Now().Add(5 * time.Second)
	for {
		if data, err := os.ReadFile(path); err == nil {
			var record fixtureOwnedRecord
			if json.Unmarshal(data, &record) == nil && record.Pid > 0 {
				return record
			}
		}
		if time.Now().After(until) {
			t.Fatalf("the fixture never published a usable owned record at %s", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// fixturePsEnv is the trusted ps lookup environment the test-side
// identity reads use: the harness's ambient PATH, nothing else (the
// fixture children's isolated PATH would not find ps).
func fixturePsEnv() Env {
	return Env{"PATH": os.Getenv("PATH")}
}

// stopOwnedFixture stops the grandchild one owned record describes, with
// the identity validation the registry stop applies: the original start
// identity is validated immediately before the single owned signal,
// through the platform's identity-aware read. A proven absence needs no
// signal; an unreadable read is never signalled (unknown is not an
// absence); a running pid whose start identity no longer matches the
// record — or a record that captured no identity — is refused, so a
// mismatched process is not signalled. After the single signal the
// absence is confirmed by the identity-aware read poll (bounded); the
// helper never repeats the kill. It reports "absent", "unknown",
// "mismatch" or "stopped".
func stopOwnedFixture(t *testing.T, record fixtureOwnedRecord, env Env) string {
	t.Helper()
	started, live := ReadProc(record.Pid, env)
	switch {
	case live == ProcGone:
		return "absent"
	case live == ProcUnknown:
		return "unknown"
	case record.Started == "":
		return "unknown"
	case record.Name == "":
		return "unknown"
	case !SameStarted(started, record.Started):
		return "mismatch"
	}
	currentStarted, currentName, ok := ProcInfo(record.Pid, env)
	if !ok {
		return "unknown"
	}
	if !SameStarted(currentStarted, record.Started) || currentName != record.Name {
		return "mismatch"
	}
	process, err := os.FindProcess(record.Pid)
	if err != nil {
		t.Fatalf("find fixture pid %d: %v", record.Pid, err)
	}
	if err := process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("the single owned signal to fixture pid %d: %v", record.Pid, err)
	}
	until := time.Now().Add(5 * time.Second)
	for {
		_, live = ReadProc(record.Pid, env)
		if live == ProcGone {
			return "stopped"
		}
		if live == ProcUnknown {
			t.Fatalf("fixture pid %d is unreadable after the owned signal: absence unconfirmed and no second signal is sent", record.Pid)
		}
		if time.Now().After(until) {
			t.Fatalf("fixture pid %d still running after the single owned signal", record.Pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

type windowsReference struct {
	Version int `json:"version"`
	Roots   map[string]struct {
		ProjectRoot      string `json:"projectRoot"`
		StateProjectRoot string `json:"stateProjectRoot"`
	} `json:"roots"`
	WindowsExe struct {
		ResolvedBase string `json:"resolvedBase"`
		Status       int    `json:"status"`
		Stdout       string `json:"stdout"`
		Stderr       string `json:"stderr"`
	} `json:"windowsExe"`
}

func readWindowsReference(t *testing.T) windowsReference {
	t.Helper()
	data, err := os.ReadFile("testdata/windows-roots.json")
	if err != nil {
		t.Skip("waiting for JS Windows reference from /tmp/hs-go/build/js-win-ref.mjs")
	}
	var reference windowsReference
	if err := json.Unmarshal(data, &reference); err != nil {
		t.Fatalf("decode Windows JS reference: %v", err)
	}
	if reference.Version != 1 {
		t.Fatalf("Windows JS reference version=%d want 1", reference.Version)
	}
	return reference
}

func assertRootReference(t *testing.T, reference *windowsReference, name string, env Env, cwd, fixtureRoot, nonWindowsWant string, state bool) {
	t.Helper()
	want := nonWindowsWant
	if reference != nil {
		roots, ok := reference.Roots[name]
		if !ok {
			t.Fatalf("Windows JS reference is missing root case %q", name)
		}
		want = roots.ProjectRoot
		if state {
			want = roots.StateProjectRoot
		}
	}
	got := ProjectRoot(env, cwd)
	if state {
		got = StateProjectRoot(env, cwd)
	}
	if reference != nil {
		var err error
		fixtureRoot, err = filepath.EvalSymlinks(fixtureRoot)
		if err != nil {
			t.Fatalf("canonical fixture root: %v", err)
		}
		got, err = filepath.EvalSymlinks(got)
		if err != nil {
			t.Fatalf("canonical %s root: %v", name, err)
		}
		got, err = filepath.Rel(fixtureRoot, got)
		if err != nil {
			t.Fatalf("relative %s root: %v", name, err)
		}
		if got == "." {
			got = ""
		}
	}
	if got != want {
		t.Fatalf("%s got %q want %q", name, got, want)
	}
}

func TestUserConfigPath(t *testing.T) {
	t.Run("userConfigPath: XDG_CONFIG_HOME wins on every platform (decision 4)", func(t *testing.T) { // JS: "userConfigPath: XDG_CONFIG_HOME wins on every platform (decision 4)"
		for _, osName := range []string{"linux", "darwin", "win32"} {
			if got := UserConfigPath(osName, Env{"XDG_CONFIG_HOME": "/cfg/home"}); got != filepath.Join("/cfg/home", "soho", "config") {
				t.Errorf("%s: %q", osName, got)
			}
		}
	})
	t.Run("userConfigPath: win32 without XDG uses %APPDATA%\\herdr-soho\\config", func(t *testing.T) { // JS: "userConfigPath: win32 without XDG uses %APPDATA%\\herdr-soho\\config"
		if got := UserConfigPath("win32", Env{"APPDATA": `C:\Users\dev\AppData\Roaming`}); got != filepath.Join(`C:\Users\dev\AppData\Roaming`, "soho", "config") {
			t.Errorf("APPDATA: %q", got)
		}
		if got := UserConfigPath("win32", Env{"USERPROFILE": `C:\Users\dev`}); got != filepath.Join(`C:\Users\dev`, "AppData", "Roaming", "soho", "config") {
			t.Errorf("fallback: %q", got)
		}
	})
	t.Run("userConfigPath: Unix without XDG uses ~/.config/herdr-soho/config", func(t *testing.T) { // JS: "userConfigPath: Unix without XDG uses ~/.config/herdr-soho/config"
		if got := UserConfigPath("linux", Env{"HOME": "/home/dev"}); got != filepath.Join("/home/dev", ".config", "soho", "config") {
			t.Errorf("linux: %q", got)
		}
		if got := UserConfigPath("darwin", Env{"HOME": "/Users/dev"}); got != filepath.Join("/Users/dev", ".config", "soho", "config") {
			t.Errorf("darwin: %q", got)
		}
		if got := UserConfigPath("linux", Env{"XDG_CONFIG_HOME": "", "HOME": "/home/dev"}); got != filepath.Join("/home/dev", ".config", "soho", "config") {
			t.Errorf("empty XDG: %q", got)
		}
	})
}

func TestHomeDir(t *testing.T) {
	t.Run("homeDir: USERPROFILE on win32, HOME elsewhere", func(t *testing.T) { // JS: "homeDir: USERPROFILE on win32, HOME elsewhere"
		if got := HomeDir("win32", Env{"USERPROFILE": `C:\Users\dev`}); got != `C:\Users\dev` {
			t.Errorf("win32: %q", got)
		}
		if got := HomeDir("linux", Env{"HOME": "/home/dev"}); got != "/home/dev" {
			t.Errorf("linux: %q", got)
		}
	})
	t.Run("homeDir falls back to the OS user and dies clearly when unresolved", func(t *testing.T) {
		old := currentUserHome
		defer func() { currentUserHome = old }()
		currentUserHome = func() (string, error) { return "/resolved/home", nil }
		if got := HomeDir("linux", Env{}); got != "/resolved/home" {
			t.Fatalf("fallback home = %q", got)
		}
		currentUserHome = func() (string, error) { return "", errors.New("no passwd entry") }
		for _, tc := range []struct{ platform, variable string }{{"linux", "HOME"}, {"win32", "USERPROFILE"}} {
			value := capturePanic(func() { HomeDir(tc.platform, Env{}) })
			exitErr, ok := value.(*ExitError)
			want := "cannot resolve the home directory; set " + tc.variable
			if !ok || exitErr.Code != 2 || exitErr.Msg != want {
				t.Errorf("%s unresolved home panic = %#v, want %q", tc.platform, value, want)
			}
		}
	})
}

func TestSkillDirRequiresRolesDirectory(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte("skill"), 0o600); err != nil {
		t.Fatal(err)
	}
	previous := currentExecutable
	currentExecutable = func() (string, error) { return filepath.Join(bin, "herdr-soho"), nil }
	defer func() { currentExecutable = previous }()
	// No home/cache values: a valid bundled fallback must not hide a
	// missing roles directory in the executable's incomplete skill tree.
	env := Env{}
	got := capturePanic(func() { SkillDir(env) }) // Mutation captured: removing the roles/ validation accepts an incomplete skill tree.
	exit, ok := got.(*ExitError)
	if !ok || exit.Code != 2 {
		t.Fatalf("SkillDir panic=%#v; want ExitError code 2 when roles is missing", got)
	}
}

func capturePanic(run func()) (value any) {
	defer func() { value = recover() }()
	run()
	return nil
}

// TestLauncherPath pins the deliberate binary-only contract: the
// configured SOHO_BIN override (validated and symlink-resolved) or
// the current executable; never a skill scripts launcher, never a
// Node/Bun/Bash search, and every invalid override fails closed.
func TestLauncherPath(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locating the test binary: %v", err)
	}
	want, err := filepath.EvalSymlinks(exe)
	if err != nil {
		t.Fatalf("resolving the test binary: %v", err)
	}
	t.Run("the SOHO_BIN override names the native binary through symlinks and hostile spacing", func(t *testing.T) {
		// A symlinked binary inside a directory with a space and a quote:
		// the hostile spacing must not defeat the resolution, and the
		// skill directory must play no part.
		quoteName := `qu"ote`
		if runtime.GOOS == "windows" {
			quoteName = "qu'ote"
		} // A double quote is not a legal Windows filename.
		weird := filepath.Join(t.TempDir(), "sp ace", quoteName)
		if err := os.MkdirAll(weird, 0o755); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(weird, "soho")
		if err := os.Symlink(exe, link); err != nil {
			t.Fatal(err)
		}
		env := Env{"SOHO_BIN": link, "PATH": ""}
		if got := LauncherPath(env); got != want {
			t.Fatalf("LauncherPath() = %q, want the resolved native binary %q", got, want)
		}
	})
	t.Run("the fallback is the current executable, not PATH and not skill/scripts", func(t *testing.T) {
		previous := currentExecutable
		currentExecutable = func() (string, error) { return exe, nil }
		defer func() { currentExecutable = previous }()
		// An empty or blank override means no override: the fallback holds.
		for _, blank := range []string{"", "   "} {
			env := Env{"SOHO_BIN": blank, "PATH": ""}
			if got := LauncherPath(env); got != exe {
				t.Fatalf("LauncherPath() with %q override = %q, want the current executable %q", blank, got, exe)
			}
		}
		got := LauncherPath(Env{"PATH": ""})
		if got != exe {
			t.Fatalf("LauncherPath() = %q, want the current executable %q", got, exe)
		}
		if strings.Contains(got, "scripts") || strings.HasSuffix(got, ".cmd") {
			t.Fatalf("LauncherPath() = %q names a scripts launcher; want the native binary", got)
		}
	})
	t.Run("refusals fail closed with code 2, never a fallback", func(t *testing.T) {
		tmp := t.TempDir()
		for _, tc := range []struct {
			name    string
			bin     string
			setup   func(t *testing.T) string
			want    string
			skipWin bool
		}{
			{name: "relative override", bin: "soho", want: "SOHO_BIN must be an absolute path"},
			{name: "missing override", bin: filepath.Join(tmp, "missing"), want: "SOHO_BIN is missing or not a regular file"},
			{name: "directory override", bin: tmp, want: "SOHO_BIN is missing or not a regular file"},
			{
				name: "shebang script override", bin: filepath.Join(tmp, "script"), want: "SOHO_BIN is not a native binary",
				setup: func(t *testing.T) string {
					f := filepath.Join(tmp, "script")
					if err := os.WriteFile(f, []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
						t.Fatal(err)
					}
					return f
				},
			},
			{
				name: ".cmd batch override", bin: filepath.Join(tmp, "run.cmd"), want: "SOHO_BIN is not a native binary",
				setup: func(t *testing.T) string {
					f := filepath.Join(tmp, "run.cmd")
					if err := os.WriteFile(f, []byte("@echo off\nnode soho.mjs\n"), 0o755); err != nil {
						t.Fatal(err)
					}
					return f
				},
			},
			{
				name: "symlink to a script resolves to the script", bin: filepath.Join(tmp, "link"), want: "SOHO_BIN is not a native binary",
				setup: func(t *testing.T) string {
					target := filepath.Join(tmp, "real-script")
					if err := os.WriteFile(target, []byte("#!/usr/bin/env node\n"), 0o755); err != nil {
						t.Fatal(err)
					}
					link := filepath.Join(tmp, "link")
					if err := os.Symlink(target, link); err != nil {
						t.Fatal(err)
					}
					return link
				},
			},
			{
				name: "non-executable native binary", bin: filepath.Join(tmp, "noperm"), want: "SOHO_BIN is not executable", skipWin: true,
				setup: func(t *testing.T) string {
					f := filepath.Join(tmp, "noperm")
					data, err := os.ReadFile(exe)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(f, data, 0o600); err != nil {
						t.Fatal(err)
					}
					return f
				},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				if tc.skipWin && runtime.GOOS == "windows" {
					t.Skip("the mode bit is a unix contract; the windows round proves the PE path")
				}
				bin := tc.bin
				if tc.setup != nil {
					bin = tc.setup(t)
				}
				got := capturePanic(func() { LauncherPath(Env{"SOHO_BIN": bin}) })
				exit, ok := got.(*ExitError)
				if !ok || exit.Code != 2 || !strings.Contains(exit.Msg, tc.want) {
					t.Fatalf("LauncherPath panic=%#v; want ExitError code 2 naming %q", got, tc.want)
				}
			})
		}
	})
}

// DJA-318: the herdr-soho generation's env names are no longer read at
// the platform layer. Defined alone, each of them is ignored; the SOHO_ name
// keeps deciding.
func TestLegacyEnvNamesIgnored(t *testing.T) {
	t.Run("HERDR_SOHO_NOWRITE=1 alone does not open the read-only gate", func(t *testing.T) {
		if nowriteGate(Env{"HERDR_SOHO_NOWRITE": "1"}) {
			t.Fatal("the legacy NOWRITE name opened the read-only gate")
		}
		if !nowriteGate(Env{"SOHO_NOWRITE": "1"}) {
			t.Fatal("the SOHO_ name must still open the read-only gate")
		}
	})
	t.Run("HERDR_SOHO_SKILL_DIR alone is not a skill source", func(t *testing.T) {
		home := t.TempDir()
		bin := t.TempDir()
		previous := currentExecutable
		currentExecutable = func() (string, error) { return filepath.Join(bin, "soho"), nil }
		defer func() { currentExecutable = previous }()
		configured := t.TempDir()
		if dir, origin := SkillDirSourceOrEmpty(Env{"HERDR_SOHO_SKILL_DIR": configured, "HOME": home, "USERPROFILE": home}); dir != "" || origin != "" {
			t.Fatalf("SkillDirSourceOrEmpty() = %q %q; the legacy configured name must be ignored (want empty)", dir, origin)
		}
		// The SOHO_ name keeps deciding (control).
		if dir, origin := SkillDirSourceOrEmpty(Env{"SOHO_SKILL_DIR": configured, "HOME": home, "USERPROFILE": home}); dir != configured || origin != "SOHO_SKILL_DIR" {
			t.Fatalf("SkillDirSourceOrEmpty() = %q %q; want %q SOHO_SKILL_DIR", dir, origin, configured)
		}
	})
	t.Run("HERDR_SOHO_BIN alone is not a launcher override", func(t *testing.T) {
		exe, err := os.Executable()
		if err != nil {
			t.Fatalf("locating the test binary: %v", err)
		}
		previous := currentExecutable
		currentExecutable = func() (string, error) { return exe, nil }
		defer func() { currentExecutable = previous }()
		// A valid native binary at another path: the legacy override must
		// not pick it up, the fallback is the current executable.
		override := filepath.Join(t.TempDir(), "soho-copy")
		data, err := os.ReadFile(exe)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(override, data, 0o755); err != nil {
			t.Fatal(err)
		}
		if got := LauncherPath(Env{"HERDR_SOHO_BIN": override}); got != exe {
			t.Fatalf("LauncherPath() = %q; the legacy override must be ignored (want the current executable %q)", got, exe)
		}
		// The SOHO_ override keeps deciding (control).
		resolvedOverride, err := filepath.EvalSymlinks(override)
		if err != nil {
			t.Fatal(err)
		}
		if got := LauncherPath(Env{"SOHO_BIN": override}); got != resolvedOverride {
			t.Fatalf("LauncherPath() = %q; want the SOHO_ override %q", got, resolvedOverride)
		}
	})
}

// TestIsNativeExecutableHeader pins the header words the override
// validation accepts (Mach-O either byte order, ELF, PE) and the hostile
// text it must refuse (shebang, .cmd batch, JSON, a bare MZ without the PE
// signature, an out-of-window e_lfanew, a short file).
func TestIsNativeExecutableHeader(t *testing.T) {
	pe := make([]byte, 0x40)
	pe[0], pe[1] = 'M', 'Z'
	pe[0x3C] = 0x38 // e_lfanew: the PE signature sits at offset 0x38
	copy(pe[0x38:], "PE\x00\x00")
	for _, tc := range []struct {
		name   string
		header []byte
		want   bool
	}{
		{name: "Mach-O 64", header: append([]byte{0xFE, 0xED, 0xFA, 0xCF}, make([]byte, 8)...), want: true},
		{name: "Mach-O 32", header: []byte{0xFE, 0xED, 0xFA, 0xCE, 0, 0, 0, 0}, want: true},
		{name: "Mach-O 64 swapped", header: []byte{0xCF, 0xFA, 0xED, 0xFE, 0, 0, 0, 0}, want: true},
		{name: "Mach-O 32 swapped", header: []byte{0xCE, 0xFA, 0xED, 0xFE, 0, 0, 0, 0}, want: true},
		{name: "Mach-O universal", header: []byte{0xCA, 0xFE, 0xBA, 0xBE, 0, 1, 0, 0}, want: true},
		{name: "Mach-O universal swapped", header: []byte{0xBE, 0xBA, 0xFE, 0xCA, 0, 1, 0, 0}, want: true},
		{name: "ELF", header: []byte{0x7F, 'E', 'L', 'F', 2, 1, 1, 0}, want: true},
		{name: "PE with the signature at e_lfanew", header: pe, want: true},
		{name: "shebang script", header: []byte("#!/bin/sh\necho hi\n"), want: false},
		{name: ".cmd batch", header: []byte("@echo off\nnode soho.mjs\n"), want: false},
		{name: "JSON", header: []byte(`{"a":1}`), want: false},
		{name: "MZ without the PE signature", header: append([]byte("MZ"), make([]byte, 0x40-2)...), want: false},
		{name: "MZ with an out-of-window e_lfanew", header: func() []byte {
			bad := make([]byte, 0x40)
			bad[0], bad[1], bad[0x3C], bad[0x3D] = 'M', 'Z', 0xFF, 0xFF
			return bad
		}(), want: false},
		{name: "a two-byte file", header: []byte{'M', 'Z'}, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isNativeExecutableHeader(tc.header); got != tc.want {
				t.Fatalf("isNativeExecutableHeader(%x) = %v, want %v", tc.header[:min(8, len(tc.header))], got, tc.want)
			}
		})
	}
}

func TestReadTextFile(t *testing.T) {
	t.Run("readTextFile: CRLF lines are normalized to LF (decision 7)", func(t *testing.T) { // JS: "readTextFile: CRLF lines are normalized to LF (decision 7)"
		file := filepath.Join(t.TempDir(), "a.txt")
		_ = os.WriteFile(file, []byte("a=1\r\nb=2 # c\r\n"), 0o600)
		if got, err := ReadTextFile(file); err != nil || got != "a=1\nb=2 # c\n" {
			t.Fatalf("CRLF: %q %v", got, err)
		}
		_ = os.WriteFile(file, []byte("x=1\ny=2\n"), 0o600)
		if got, err := ReadTextFile(file); err != nil || got != "x=1\ny=2\n" {
			t.Fatalf("LF: %q %v", got, err)
		}
	})
	t.Run("ReadTextFile matches Node UTF-8 replacement and newline normalization", func(t *testing.T) {
		var fixture struct {
			Version int `json:"version"`
			Cases   []struct {
				Hex      string `json:"hex"`
				Expected string `json:"expected"`
			} `json:"cases"`
		}
		data, err := os.ReadFile("testdata/readtext.json")
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(data, &fixture); err != nil {
			t.Fatal(err)
		}
		if fixture.Version != 1 || len(fixture.Cases) < 2003 {
			t.Fatalf("fixture version=%d cases=%d", fixture.Version, len(fixture.Cases))
		}
		for index, testCase := range fixture.Cases {
			raw, err := hex.DecodeString(testCase.Hex)
			if err != nil {
				t.Fatalf("case %d hex: %v", index, err)
			}
			file := filepath.Join(t.TempDir(), "bytes.txt")
			if err = os.WriteFile(file, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := ReadTextFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if got != testCase.Expected {
				t.Fatalf("case %d got %q want %q (input %s)", index, got, testCase.Expected, hex.EncodeToString(raw))
			}
		}
	})
}

func TestFindExecutable(t *testing.T) {
	// Mutation captured: dropping the Unix execute-bit check admits a plain file.
	t.Run("findExecutable: finds node on PATH, returns null for unknown names", func(t *testing.T) { // JS: "findExecutable: finds node on PATH, returns null for unknown names"
		if runtime.GOOS == "windows" {
			t.Skip("Unix execute-bit lookup is not available on Windows")
		}
		bin := t.TempDir()
		node := filepath.Join(bin, "node")
		_ = os.WriteFile(node, []byte("#!/bin/sh\n"), 0o700)
		if got, ok := FindExecutable("node", Env{"PATH": bin}, "linux"); !ok || got != node {
			t.Fatalf("found: %q %v", got, ok)
		}
		if _, ok := FindExecutable("definitely-not-a-real-command-xyz", Env{"PATH": bin}, "linux"); ok {
			t.Fatal("unexpected executable")
		}
		plain := filepath.Join(bin, "plain")
		if err := os.WriteFile(plain, []byte("not executable"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got, ok := FindExecutable("plain", Env{"PATH": bin}, "linux"); ok || got != "" {
			t.Fatalf("non-executable file resolved as %q", got)
		}
	})
	// Mutation captured: retaining empty PATHEXT entries resolves a Windows file without an extension.
	t.Run("findExecutable: simulated win32 honors PATHEXT and discards empty extensions", func(t *testing.T) { // JS: "findExecutable: simulated win32 honors PATHEXT (.CMD); darwin does not"
		bin := t.TempDir()
		bare := filepath.Join(bin, "npm")
		shim := filepath.Join(bin, "tool.CMD")
		if err := os.WriteFile(bare, []byte("script"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(shim, []byte("batch"), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, pathext := range []string{".EXE;.CMD", ".EXE;.CMD;", ";.CMD", ".CMD;;.EXE"} {
			got, ok := FindExecutable("npm", Env{"PATH": bin, "PATHEXT": pathext}, "win32")
			if ok || got != "" {
				t.Fatalf("PATHEXT=%q resolved extensionless npm as %q", pathext, got)
			}
		}
		if got, ok := FindExecutable("tool", Env{"PATH": bin, "PATHEXT": ".CMD"}, "win32"); !ok || got != shim {
			t.Fatalf("PATHEXT lookup = %q, %v", got, ok)
		}
		lowerExe := filepath.Join(bin, "lower.exe")
		if err := os.WriteFile(lowerExe, []byte("binary"), 0o600); err != nil {
			t.Fatal(err)
		}
		caseVariant := filepath.Join(bin, "lower.EXE")
		wantLower := lowerExe
		if _, err := os.Stat(caseVariant); err == nil {
			wantLower = caseVariant
		}
		if got, ok := FindExecutable("lower", Env{"PATH": bin, "PATHEXT": ".EXE"}, "win32"); !ok || got != wantLower {
			t.Fatalf("case-insensitive PATHEXT lookup = %q, %v", got, ok)
		}
		if got, ok := FindExecutable("lower.EXE", Env{"PATH": bin, "PATHEXT": ".exe"}, "win32"); !ok || got != wantLower {
			t.Fatalf("explicit mixed-case extension lookup = %q, %v", got, ok)
		}
		joined := filepath.Join(t.TempDir(), "later")
		if err := os.MkdirAll(joined, 0o700); err != nil {
			t.Fatal(err)
		}
		later := filepath.Join(joined, "other.CMD")
		if err := os.WriteFile(later, []byte("batch"), 0o600); err != nil {
			t.Fatal(err)
		}
		pathValue := bin + string(os.PathListSeparator) + joined
		if got, ok := FindExecutable("other", Env{"PATH": pathValue, "PATHEXT": ".CMD"}, "win32"); !ok || got != later {
			t.Fatalf("host PATH split = %q, %v", got, ok)
		}
		if _, ok := FindExecutable("tool", Env{"PATH": bin, "PATHEXT": ".CMD"}, "linux"); ok {
			t.Fatal("linux lookup accepted a non-executable extension shim")
		}
	})
	// Mutation captured: requiring ReadDir before Stat misses an exact Win32 path in an unlistable directory.
	t.Run("simulated win32 stats an exact candidate before listing its directory", func(t *testing.T) {
		bin := t.TempDir()
		file := filepath.Join(bin, "grok.CMD")
		if err := os.WriteFile(file, []byte("batch"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(bin, 0o111); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(bin, 0o700) })
		if got, ok := FindExecutable("grok", Env{"PATH": bin, "PATHEXT": ".CMD"}, "win32"); !ok || got != file {
			t.Fatalf("unlistable exact candidate = %q, %v; want %q", got, ok, file)
		}
	})
}

func TestCmdInvocation(t *testing.T) {
	t.Run("command path equals delimiter is escaped", func(t *testing.T) {
		inv := CmdInvocation(`C:\strict=true\wrapper.cmd`, []string{"x& echo INJECTED", "a=b"}, Env{})
		want := `"C:\strict^=true\wrapper.cmd ^"x^&^ echo^ INJECTED^" ^"a=b^""`
		if inv.Args[3] != want {
			t.Fatalf("command line=%q, want %q", inv.Args[3], want)
		}
	})
	// Mutation captured: omitting the second escaping pass breaks npm .cmd shims.
	t.Run("models.test.mjs: win32 cmdInvocation escapes batch arguments", func(t *testing.T) { // JS: "runCli: a win32 .cmd/.bat target runs through cmd.exe with every argument escaped; notFound and timeout"
		inv := CmdInvocation(`C:\tools\grok.CMD`, []string{"models", "has space", "x& echo INJECTED", "50%", `a"b`}, Env{"COMSPEC": `C:\Windows\system32\cmd.exe`})
		if inv.Command != `C:\Windows\system32\cmd.exe` || !inv.WindowsVerbatimArguments || len(inv.Args) != 4 {
			t.Fatalf("invocation = %#v", inv)
		}
		line := inv.Args[3]
		for _, part := range []string{`^"models^"`, `^"has^ space^"`, `^"x^&^ echo^ INJECTED^"`, `^"50^%^"`, `^"a\^"b^"`} {
			if !strings.Contains(line, part) {
				t.Errorf("missing %q in %q", part, line)
			}
		}
		if strings.Contains(line, "& echo") {
			t.Fatalf("unescaped command metacharacter in %q", line)
		}
		shim := CmdInvocation(`C:\p\node_modules\.bin\tool.cmd`, []string{"a&b"}, Env{})
		if !strings.Contains(shim.Args[3], `^^^"a^^^&b^^^"`) {
			t.Fatalf("npm shim escaping = %q", shim.Args[3])
		}
	})
}

func TestWindowsPlatformMatchesJSFixtures(t *testing.T) {
	var fixture struct {
		Version int `json:"version"`
		Find    []struct {
			Name     string  `json:"name"`
			PathSpec string  `json:"pathSpec"`
			PATHEXT  string  `json:"pathext"`
			Basename *string `json:"basename"`
		} `json:"find"`
		Invocations []struct {
			Resolved string              `json:"resolved"`
			Args     []string            `json:"args"`
			Env      map[string]string   `json:"env"`
			Expected CmdInvocationResult `json:"expected"`
		} `json:"invocations"`
		Resolve []struct {
			Base     string `json:"base"`
			Value    string `json:"value"`
			Expected string `json:"expected"`
		} `json:"resolve"`
	}
	data, err := os.ReadFile("testdata/windows.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Version != 1 || len(fixture.Find) != 5 || len(fixture.Invocations) != 2 {
		t.Fatalf("fixture version=%d find=%d invocations=%d", fixture.Version, len(fixture.Find), len(fixture.Invocations))
	}
	root := t.TempDir()
	first, second := filepath.Join(root, "first"), filepath.Join(root, "second")
	if err := os.MkdirAll(first, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(second, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(first, "npm"), []byte("extensionless"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(second, "tool.CMD"), []byte("batch"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range fixture.Find {
		t.Run("findExecutable "+tc.PATHEXT, func(t *testing.T) {
			pathValue := first
			if tc.PathSpec == "second" {
				pathValue = second
			}
			got, ok := FindExecutable(tc.Name, Env{"PATH": pathValue, "PATHEXT": tc.PATHEXT}, "win32")
			var basename *string
			if ok {
				value := filepath.Base(got)
				basename = &value
			}
			if (basename == nil) != (tc.Basename == nil) || (basename != nil && *basename != *tc.Basename) {
				t.Fatalf("basename=%v JS=%v", basename, tc.Basename)
			}
		})
	}
	for _, tc := range fixture.Invocations {
		t.Run("cmdInvocation "+tc.Resolved, func(t *testing.T) {
			got := CmdInvocation(tc.Resolved, tc.Args, Env(tc.Env))
			if got.Command != tc.Expected.Command || strings.Join(got.Args, "\x00") != strings.Join(tc.Expected.Args, "\x00") || got.WindowsVerbatimArguments != tc.Expected.WindowsVerbatimArguments {
				t.Fatalf("Go=%#v JS=%#v", got, tc.Expected)
			}
		})
	}
	if runtime.GOOS == "windows" {
		for _, tc := range fixture.Resolve {
			t.Run("resolveFrom "+tc.Value, func(t *testing.T) {
				if got := resolveFrom(tc.Base, tc.Value); got != tc.Expected {
					t.Fatalf("Go=%q JS=%q", got, tc.Expected)
				}
			})
		}
	}
}

func TestRunCli(t *testing.T) {
	// The fixture program is a re-executed copy of this test binary (no
	// shell behind the name): the role selected by the environment
	// reproduces each old sh script's observable streams, exit codes,
	// signal behavior and pipe holding. The subtests that pin a POSIX
	// exec/errno/signal contract keep their build boundary; the generic
	// stream/exit/cwd/tmpdir subtests run on every platform.
	bin := t.TempDir()
	runCliFixtureProgram(t, bin, runCliFixtureName())
	env := Env{"PATH": bin, "TMPDIR": t.TempDir(), runCliFixtureRoleEnv: "out-err-5", runCliFixturePsPathEnv: os.Getenv("PATH")}
	for _, tc := range []struct {
		name string
		mode RunOptions
		out  string
		err  string
	}{
		{"default", RunOptions{}, "out-line\n", "err-line\n"},
		{"merge output", RunOptions{MergeOutput: true}, "out-line\nerr-line\n", ""},
		{"output files", RunOptions{OutputFiles: true}, "out-line\n", "err-line\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.mode.Env = env
			r := RunCli("fixture", nil, tc.mode)
			if r.Status == nil || *r.Status != 5 || r.Stdout != tc.out || r.Stderr != tc.err || r.Signal != "" || r.TimedOut {
				t.Fatalf("result = %#v", r)
			}
		})
	}
	t.Run("stdin is passed through and exit codes are preserved", func(t *testing.T) {
		env[runCliFixtureRoleEnv] = "cat-err"
		r := RunCli("fixture", nil, RunOptions{Env: env, Input: "stdin body"})
		if r.Status == nil || *r.Status != 7 || r.Stdout != "stdin body" || r.Stderr != "err" {
			t.Fatalf("result = %#v", r)
		}
	})
	t.Run("RunOptions.Cwd controls the child working directory", func(t *testing.T) { // Mutation captured: removing cmd.Dir ignores the caller's working directory.
		env[runCliFixtureRoleEnv] = "pwd"
		cwd := t.TempDir()
		canonicalCwd, err := filepath.EvalSymlinks(cwd)
		if err != nil {
			t.Fatal(err)
		}
		r := RunCli("fixture", nil, RunOptions{Env: env, Cwd: cwd})
		actualCwd, actualErr := filepath.EvalSymlinks(strings.TrimSpace(r.Stdout))
		if r.Status == nil || *r.Status != 0 || actualErr != nil || actualCwd != canonicalCwd {
			t.Fatalf("cwd result=%#v want=%q", r, canonicalCwd)
		}
	})
	t.Run("temporary output files use Env.TMPDIR and are removed", func(t *testing.T) { // Mutations captured: ignoring Env.TMPDIR or leaving output files behind changes observable filesystem state.
		env[runCliFixtureRoleEnv] = "out-err"
		customTemp := t.TempDir()
		missingTemp := filepath.Join(t.TempDir(), "missing")
		created := RunCli("fixture", nil, RunOptions{Env: Env{"PATH": bin, "TMPDIR": missingTemp, runCliFixtureRoleEnv: "out-err"}, OutputFiles: true})
		if created.Status == nil || *created.Status != 0 || created.Error != "" {
			t.Fatalf("a missing Env.TMPDIR is created (mkdirSync recursive in the JS): %#v", created)
		}
		if entries, err := os.ReadDir(missingTemp); err != nil || len(entries) != 0 {
			t.Fatalf("the created Env.TMPDIR must be used and left empty: %v, %v", entries, err)
		}
		for _, options := range []RunOptions{{MergeOutput: true}, {OutputFiles: true}} {
			options.Env = Env{"PATH": bin, "TMPDIR": customTemp, runCliFixtureRoleEnv: "out-err"}
			r := RunCli("fixture", nil, options)
			entries, err := os.ReadDir(customTemp)
			if err != nil || len(entries) != 0 {
				t.Fatalf("temporary output files remain: %v, %v", entries, err)
			}
			wantOut, wantErr := "out\n", "err\n"
			if options.MergeOutput {
				wantOut, wantErr = "out\nerr\n", ""
			}
			if r.Status == nil || *r.Status != 0 || r.Stdout != wantOut || r.Stderr != wantErr {
				t.Fatalf("stream result=%#v", r)
			}
		}
	})
	t.Run("a signaled child reports Node signal shape", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("the Node signal shape is a POSIX exec contract; a Windows termination reports no signal")
		}
		env[runCliFixtureRoleEnv] = "self-signal"
		r := RunCli("fixture", nil, RunOptions{Env: env})
		if r.Status != nil || r.Signal != "SIGKILL" || !r.TimedOut || r.Error != "" {
			t.Fatalf("result = %#v", r)
		}
	})
	t.Run("spawn errors map errno codes", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("the shebang errno mapping (ENOENT for a missing interpreter, ENOEXEC for a non-executable) is a POSIX kernel contract")
		}
		// Inert rejection data: the exec fails before any engine runs.
		bad := filepath.Join(bin, "bad-shebang")
		if err := os.WriteFile(bad, []byte("#!/definitely/missing/interpreter\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		r := RunCli("bad-shebang", nil, RunOptions{Env: env})
		if r.Error != "ENOENT" {
			t.Fatalf("error = %q (%#v)", r.Error, r)
		}
		if got := errorCode(&os.PathError{Op: "exec", Path: "x", Err: syscall.ENOEXEC}); got != "ENOEXEC" {
			t.Fatalf("ENOEXEC mapped as %q", got)
		}
	})
	t.Run("timeout wait delay of 1 s bounds the wait when a grandchild holds pipes", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("the result asserts a non-empty POSIX signal from the SIGTERM cancel; Windows termination reports no signal")
		}
		env[runCliFixtureRoleEnv] = "tree-hold"
		pidFile := filepath.Join(t.TempDir(), "grandchild.json")
		env[runCliFixturePidfileEnv] = pidFile
		start := time.Now()
		r := RunCli("fixture", nil, RunOptions{Env: env, TimeoutMs: 100})
		elapsed := time.Since(start)
		// The grandchild is owned before any result assertion: a failure
		// below must not leak it.
		record := readFixtureOwnedRecord(t, pidFile)
		psEnv := fixturePsEnv()
		t.Cleanup(func() {
			if outcome := stopOwnedFixture(t, record, psEnv); outcome != "absent" && outcome != "stopped" {
				t.Errorf("the cleanup could not stop the grandchild: %s", outcome)
			}
		})
		if !r.TimedOut || r.Status != nil || r.Signal == "" || elapsed > 1500*time.Millisecond {
			t.Fatalf("result=%#v elapsed=%v", r, elapsed)
		}
		// Causal lower bound: the grandchild held the inherited pipes for
		// 5 s, so the wait consumed the 1 s wait delay; an immediate exit
		// (pipes not held) would return in well under 500 ms.
		if elapsed < 500*time.Millisecond {
			t.Fatalf("elapsed=%v is too fast for the 1 s wait delay over the pipes the grandchild inherited", elapsed)
		}
		// The grandchild outlived the parent's SIGTERM and is owned here:
		// validated against the original identity, stopped with the
		// single owned signal and confirmed absent, exactly like the old
		// fixture's orphaned sleep is not left behind.
		if outcome := stopOwnedFixture(t, record, psEnv); outcome != "stopped" && outcome != "absent" {
			t.Fatalf("stop the owned grandchild: %s (record %#v)", outcome, record)
		}
	})
}

// runCliFixtureName is the fixture program's file name: extensionless on
// Unix, .exe on Windows (the PATHEXT resolution).
func runCliFixtureName() string {
	if runtime.GOOS == "windows" {
		return "fixture.exe"
	}
	return "fixture"
}

func TestRunCliWindowsTreeKillSelection(t *testing.T) {
	for _, tc := range []struct {
		name      string
		platform  string
		resolved  string
		timeoutMs int
		want      bool
	}{
		{"Windows command script with timeout", "win32", "fake.CMD", 1, true},
		{"Windows batch script with timeout", "win32", "fake.BaT", 1, true},
		{"Windows executable with timeout", "win32", "fake.exe", 1, false},
		{"Windows command script without timeout", "win32", "fake.cmd", 0, false},
		{"POSIX command script with timeout", "darwin", "fake.cmd", 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldUseWindowsCmdTreeKill(tc.platform, tc.resolved, tc.timeoutMs); got != tc.want {
				t.Fatalf("shouldUseWindowsCmdTreeKill(%q, %q, %d) = %v, want %v", tc.platform, tc.resolved, tc.timeoutMs, got, tc.want)
			}
		})
	}
}

func TestRunCliWindowsTreeKillAssignsBeforeResume(t *testing.T) {
	var steps []string
	assigned, err := assignThenResume(func() error {
		steps = append(steps, "job")
		return nil
	}, func() error {
		steps = append(steps, "resume")
		return nil
	})
	if err != nil || !assigned || strings.Join(steps, ",") != "job,resume" {
		t.Fatalf("assignment sequence = %v, assigned=%v, err=%v", steps, assigned, err)
	}

	steps = nil
	assigned, err = assignThenResume(func() error {
		steps = append(steps, "job")
		return syscall.EINVAL
	}, func() error {
		steps = append(steps, "resume")
		return nil
	})
	if err != nil || assigned || strings.Join(steps, ",") != "job,resume" {
		t.Fatalf("fallback sequence = %v, assigned=%v, err=%v", steps, assigned, err)
	}
}

func TestForceWindowsTreeKillFallback(t *testing.T) { // Mutation captured: ignoring the test-only flag leaves the Job Object path selected.
	if forceWindowsTreeKillFallback(Env{}) {
		t.Fatal("normal environment selected the fallback")
	}
	if !forceWindowsTreeKillFallback(Env{treeKillForceFallbackEnv: "1"}) {
		t.Fatal("test environment did not select the fallback")
	}
	if forceWindowsTreeKillFallback(Env{treeKillForceFallbackEnv: "true"}) {
		t.Fatal("non-exact test value selected the fallback")
	}
}

func TestRunCliWindowsTreeKillTimeoutShape(t *testing.T) {
	status := 3
	result := RunResult{Resolved: "fixture.cmd", Status: &status, Stdout: "partial output", Stderr: "partial error"}
	setRunTimeoutResult(&result)
	if result.Status != nil || result.Signal != "SIGTERM" || !result.TimedOut || result.Error != "ETIMEDOUT" || result.Stdout != "partial output" || result.Stderr != "partial error" {
		t.Fatalf("timeout result = %#v", result)
	}
}

func TestRunTreeKillTestKiller(t *testing.T) {
	tempDir := t.TempDir()
	killerDir := filepath.Join(tempDir, "bin")
	if err := os.MkdirAll(killerDir, 0o700); err != nil {
		t.Fatal(err)
	}
	currentExe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	killer := filepath.Join(killerDir, "killer.exe")
	data, err := os.ReadFile(currentExe)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(killer, data, 0o700); err != nil {
		t.Fatal(err)
	}
	var gotPath string
	var gotArgs []string
	run := func(path string, args []string) {
		gotPath = path
		gotArgs = args
	}
	if !runTreeKillTestKiller(Env{"SOHO_TREEKILL_TEST_KILLER": killer}, tempDir, 1234, run) {
		t.Fatal("temporary test killer was not accepted")
	}
	resolvedKiller, err := filepath.EvalSymlinks(killer)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != resolvedKiller || len(gotArgs) != 1 || gotArgs[0] != "1234" {
		t.Fatalf("killer invocation = %q %#v", gotPath, gotArgs)
	}
	for name, value := range map[string]string{
		"relative":     filepath.Join("bin", filepath.Base(killer)),
		"outside temp": os.Args[0],
	} {
		t.Run(name, func(t *testing.T) {
			called := false
			if runTreeKillTestKiller(Env{"SOHO_TREEKILL_TEST_KILLER": value}, tempDir, 1234, func(string, []string) { called = true }) || called {
				t.Fatalf("unsafe test killer %q was accepted", value)
			}
		})
	}
}

func TestRunCliWaitDelay(t *testing.T) {
	// The fixture program is the re-executed test binary in the
	// pidfile-parent role (no shell): the grandchild holds the inherited
	// pipes and outlives the parent, so the 1 s wait delay bounds the
	// wait on every platform. The bounds are causal: the lower bound
	// proves the wait actually waited on the pipes the grandchild
	// inherited (an immediate exit would return in milliseconds), the
	// upper bound proves the 1 s delay capped the wait instead of the
	// 12 s the grandchild would hold.
	bin := t.TempDir()
	runCliFixtureProgram(t, bin, runCliFixtureName())
	pidFile := filepath.Join(t.TempDir(), "grandchild.json")
	const grandchildDelay = 12 * time.Second
	env := Env{"PATH": bin, "PID_FILE": pidFile, runCliFixtureRoleEnv: "pidfile-parent", runCliFixturePsPathEnv: os.Getenv("PATH")}
	start := time.Now()
	r := RunCli("fixture", nil, RunOptions{Env: env})
	elapsed := time.Since(start)
	// The grandchild is owned before any result assertion: a failure
	// below must not leak it.
	record := readFixtureOwnedRecord(t, pidFile)
	psEnv := fixturePsEnv()
	t.Cleanup(func() {
		if outcome := stopOwnedFixture(t, record, psEnv); outcome != "absent" && outcome != "stopped" {
			t.Errorf("the cleanup could not stop the grandchild: %s", outcome)
		}
	})
	if r.Status == nil || *r.Status != 0 || r.Error != "" {
		t.Fatalf("result=%#v elapsed=%v", r, elapsed)
	}
	if elapsed < 700*time.Millisecond {
		t.Fatalf("elapsed=%v is too fast for the 1 s wait delay over the pipes the grandchild inherited", elapsed)
	}
	if elapsed >= grandchildDelay {
		t.Fatalf("elapsed=%v reached the grandchild's hold: the wait delay did not cap the wait", elapsed)
	}
	if outcome := stopOwnedFixture(t, record, psEnv); outcome != "stopped" && outcome != "absent" {
		t.Fatalf("stop the owned grandchild: %s (record %#v)", outcome, record)
	}
}

func TestRunExecutableUsesExactPathInsteadOfPATH(t *testing.T) {
	targetDir, pathDir := t.TempDir(), t.TempDir()
	target, err := fakecli.Install(t, targetDir, "fixture", []fakecli.Rule{{Argv: []string{"identity"}, Stdout: "target\n"}})
	if err != nil {
		t.Fatal(err)
	}
	decoy, err := fakecli.Install(t, pathDir, "fixture", []fakecli.Rule{{Argv: []string{"identity"}, Stdout: "decoy\n"}})
	if err != nil {
		t.Fatal(err)
	}
	env := EnvFromOS()
	env["PATH"] = pathDir
	env["SOHO_FAKECLI_CONFIG"] = filepath.Join(targetDir, "fixture.json")

	got := RunExecutable(target, []string{"identity"}, RunOptions{Env: env})
	if got.NotFound || got.Resolved != target || got.Status == nil || *got.Status != 0 || got.Stdout != "target\n" {
		t.Fatalf("exact executable result=%#v target=%q decoy=%q", got, target, decoy)
	}
	pathResult := RunCli("fixture", []string{"identity"}, RunOptions{Env: env})
	if !strings.EqualFold(pathResult.Resolved, decoy) {
		t.Fatalf("PATH lookup resolved %q, want decoy %q", pathResult.Resolved, decoy)
	}
}

func TestRunCliWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("requires native Windows process and cmd.exe behavior")
	}
	reference := readWindowsReference(t)
	dir := t.TempDir()
	exe, err := fakecli.Install(t, dir, "fakecli", []fakecli.Rule{
		{Argv: []string{"space here", `quote"inside`, "50%", "caret^", "bang!", "café"}, Stdout: "exe-out\n", Stderr: "exe-err\n", Code: 7},
		{Argv: []string{"space here", `quote"inside`, "50%", "caret", "bang!", "café"}, Stdout: "exe-out\n", Stderr: "exe-err\n", Code: 7},
		{Argv: []string{"stdin", "large"}, Stdout: strings.Repeat("x", 128*1024), Code: 0},
		{Argv: []string{"timeout"}, Delay: 5000, Code: 0},
	})
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "fakecli.json")
	baseEnv := EnvFromOS()
	setTestPath(baseEnv, dir+";"+testPath(baseEnv))
	baseEnv["PATHEXT"] = ".EXE;.CMD;.BAT;.COM"
	baseEnv["SOHO_FAKECLI_CONFIG"] = config
	baseEnv["TMPDIR"] = dir
	if baseEnv.Get("COMSPEC") == "" {
		baseEnv["COMSPEC"] = `C:\Windows\System32\cmd.exe`
	}
	args := []string{"space here", `quote"inside`, "50%", "caret^", "bang!", "café"}
	got := RunCli("fakecli", args, RunOptions{Platform: "win32", Env: baseEnv})
	if got.Status == nil || *got.Status != reference.WindowsExe.Status || got.Stdout != reference.WindowsExe.Stdout || got.Stderr != reference.WindowsExe.Stderr || !strings.EqualFold(filepath.Base(got.Resolved), reference.WindowsExe.ResolvedBase) {
		t.Fatalf(".exe result = %#v", got)
	}
	calls, err := fakecli.ReadCalls(filepath.Join(dir, "fakecli.calls.jsonl"))
	if err != nil || len(calls) != 1 || strings.Join(calls[0].Argv, "\x00") != strings.Join(args, "\x00") {
		t.Fatalf(".exe argv = %#v, %v", calls, err)
	}

	shim := filepath.Join(dir, "fixture.cmd")
	if err := os.WriteFile(shim, []byte("@echo off\r\n\"%FAKECLI%\" %*\r\nexit /b %errorlevel%\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	shimArgs := []string{"space here", `quote"inside`, "50%", "caret^", "bang!", "café"}
	shimEnv := baseEnv.Clone()
	shimEnv["PATHEXT"] = ".CMD;.EXE;.BAT;.COM"
	shimEnv["FAKECLI"] = exe
	got = RunCli("fixture", shimArgs, RunOptions{Platform: "win32", Env: shimEnv})
	if got.Status == nil || *got.Status != 7 || got.Stdout != "exe-out\n" || got.Stderr != "exe-err\n" || !strings.EqualFold(got.Resolved, shim) {
		t.Fatalf(".cmd result = %#v", got)
	}
	calls, err = fakecli.ReadCalls(filepath.Join(dir, "fakecli.calls.jsonl"))
	wantShimArgs := append([]string(nil), shimArgs...)
	wantShimArgs[3] = "caret"
	wantShimArgs[len(wantShimArgs)-1] = "café"
	if err != nil || len(calls) != 2 || strings.Join(calls[1].Argv, "\x00") != strings.Join(wantShimArgs, "\x00") {
		t.Fatalf(".cmd argv = %#v, %v", calls, err)
	}
	stdinCmd := filepath.Join(dir, "stdin.cmd")
	if err := os.WriteFile(stdinCmd, []byte("@echo off\r\nmore\r\nexit /b 9\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdin := RunCli("stdin", nil, RunOptions{Platform: "win32", Env: baseEnv, Input: "stdin payload\r\n"})
	if stdin.Status == nil || *stdin.Status != 9 || stdin.Stdout != "stdin payload\r\n\r\n" {
		t.Fatalf(".cmd stdin = %#v; child env PATH=%q Path=%q PATHEXT=%q COMSPEC=%q ComSpec=%q SystemRoot=%q SystemDrive=%q", stdin, baseEnv["PATH"], baseEnv["Path"], baseEnv["PATHEXT"], baseEnv["COMSPEC"], baseEnv["ComSpec"], baseEnv["SystemRoot"], baseEnv["SystemDrive"])
	}

	large := RunCli("fakecli", []string{"stdin", "large"}, RunOptions{Platform: "win32", Env: baseEnv, Input: "stdin payload"})
	if large.Status == nil || *large.Status != 0 || large.Stdout != strings.Repeat("x", 128*1024) {
		t.Fatalf("stdin/large output = status %v stdout bytes %d", large.Status, len(large.Stdout))
	}
	timeout := RunCli("fakecli", []string{"timeout"}, RunOptions{Platform: "win32", Env: baseEnv, TimeoutMs: 100})
	if !timeout.TimedOut || timeout.Status != nil {
		t.Fatalf("timeout = %#v", timeout)
	}
}

func testPath(env Env) string {
	for key, value := range env {
		if strings.EqualFold(key, "PATH") {
			return value
		}
	}
	return ""
}

func setTestPath(env Env, value string) {
	key := ""
	var existingKeys []string
	for existing := range env {
		if strings.EqualFold(existing, "PATH") {
			existingKeys = append(existingKeys, existing)
			if key == "" || existing == "PATH" {
				key = existing
			}
		}
	}
	for _, existing := range existingKeys {
		delete(env, existing)
	}
	if key == "" {
		key = "PATH"
	}
	env[key] = value
}

func skipUnavailableSymlink(t *testing.T, err error) {
	t.Helper()
	if runtime.GOOS == "windows" && strings.Contains(err.Error(), "A required privilege is not held by the client") {
		t.Skip("Windows symlink privilege unavailable: " + err.Error())
	}
	t.Fatal(err)
}

// Mutation captured: losing the named result drops streams read by temp-file defers.
func TestRunCliMatchesJSFixtures(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the oracle rows pin POSIX shebang, errno and signal shapes; the Windows tier covers the runCLI process contract in TestRunCliWindows")
	}
	var fixture struct {
		Version int `json:"version"`
		Cases   []struct {
			Name      string   `json:"name"`
			Command   string   `json:"command"`
			Args      []string `json:"args"`
			Mode      string   `json:"mode"`
			TimeoutMs int      `json:"timeoutMs"`
			Script    string   `json:"script"`
			Result    struct {
				NotFound bool    `json:"notFound"`
				Status   *int    `json:"status"`
				Signal   *string `json:"signal"`
				Stdout   string  `json:"stdout"`
				Stderr   string  `json:"stderr"`
				TimedOut bool    `json:"timedOut"`
				Error    *string `json:"error"`
			} `json:"result"`
		} `json:"cases"`
	}
	data, err := os.ReadFile("testdata/runcli.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Version != 1 || len(fixture.Cases) < 7 {
		t.Fatalf("fixture version=%d cases=%d", fixture.Version, len(fixture.Cases))
	}
	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			bin, tmp := t.TempDir(), t.TempDir()
			// The native stand-in is this test binary, so a race build must
			// retain detection without the detector's artificial exit delay.
			env := Env{"PATH": bin, "TMPDIR": tmp, "GORACE": "atexit_sleep_ms=0", runCliFixturePsPathEnv: os.Getenv("PATH")}
			if tc.Mode != "missing" {
				if role := runCliFixtureRoleForCase(tc.Name); role != "" {
					// The native stand-in for the row's script (or node
					// case): a re-executed copy of this test binary named
					// for the row's command, selected by the role
					// environment.
					installName := tc.Command
					if installName == "" {
						installName = tc.Name
					}
					runCliFixtureProgram(t, bin, installName)
					env[runCliFixtureRoleEnv] = role
					if tc.Name == "daemonize-no-timeout" {
						env[runCliFixturePidfileEnv] = filepath.Join(tmp, "daemonize.json")
					}
				} else if tc.Command == "" {
					// Inert rejection data (bad-shebang, no-shebang): the
					// exec fails before any engine runs (missing
					// interpreter / not executable), so the script text is
					// never executed.
					if err := os.WriteFile(filepath.Join(bin, tc.Name), []byte(tc.Script), 0o700); err != nil {
						t.Fatal(err)
					}
				}
			}
			options := RunOptions{Env: env}
			switch tc.Mode {
			case "mergeOutput":
				options.MergeOutput = true
			case "outputFiles":
				options.OutputFiles = true
			}
			options.TimeoutMs = tc.TimeoutMs
			if tc.Mode == "timeout" && options.TimeoutMs == 0 {
				options.TimeoutMs = 100
			}
			// D17: the fixture records the deadline firing (error ETIMEDOUT)
			// while the child still exits on its own (status set, signal and
			// timedOut absent): the recorded result does not depend on the
			// deadline's value, only on it firing before the child exits on
			// its own. The node scripts parked on an interval, and the
			// fixture's 300 ms was not enough for node to start and install
			// its SIGTERM trap on a loaded host — the deadline fired
			// mid-startup and the child died on the signal, or was SIGKILLed
			// by the 1 s WaitDelay — so the Go test gives exactly those
			// cases (the fixture rows that need a live child to trap the
			// timeout) a generous deadline; the native trap role parks the
			// same way (blocked until the SIGTERM arrives). The comparison
			// with the fixture is unchanged: status, signal, stdout,
			// timedOut and error are identical for any deadline the child
			// reaches.
			if tc.Result.Error != nil && !tc.Result.TimedOut && tc.Result.Status != nil {
				options.TimeoutMs = 5000
			}
			command := tc.Command
			if command == "" {
				command = tc.Name
			}
			got := RunCli(command, tc.Args, options)
			// The daemonized grandchild is owned before the result
			// assertion: a failure below must not leak it.
			var daemonRecord fixtureOwnedRecord
			var daemonPath string
			if tc.Name == "daemonize-no-timeout" {
				daemonPath = filepath.Join(tmp, "daemonize.json")
			}
			if daemonPath != "" {
				daemonRecord = readFixtureOwnedRecord(t, daemonPath)
				psEnv := fixturePsEnv()
				t.Cleanup(func() {
					if outcome := stopOwnedFixture(t, daemonRecord, psEnv); outcome != "absent" && outcome != "stopped" {
						t.Errorf("the cleanup could not stop the daemonized grandchild: %s", outcome)
					}
				})
			}
			if got.NotFound != tc.Result.NotFound || !equalIntPointer(got.Status, tc.Result.Status) || !equalStringPointer(got.Signal, tc.Result.Signal) || got.Stdout != tc.Result.Stdout || got.Stderr != tc.Result.Stderr || got.TimedOut != tc.Result.TimedOut || !equalStringPointer(got.Error, tc.Result.Error) {
				t.Fatalf("Go=%#v JS=%#v", got, tc.Result)
			}
			if daemonPath != "" {
				// The daemonized grandchild is owned through its published
				// record: validated against the original identity, stopped
				// with the single owned signal and confirmed absent, so no
				// fixture process is left behind.
				psEnv := fixturePsEnv()
				if outcome := stopOwnedFixture(t, daemonRecord, psEnv); outcome != "stopped" && outcome != "absent" {
					t.Fatalf("stop the owned grandchild: %s (record %#v)", outcome, daemonRecord)
				}
			}
		})
	}
}

// runCliFixtureRoleForCase maps an oracle row to its native fixture role
// (empty when the row needs no executed fixture: the missing executable
// and the inert rejection data).
func runCliFixtureRoleForCase(name string) string {
	switch name {
	case "default", "merge", "files":
		return "out-err-5"
	case "signal":
		return "self-signal"
	case "timeout":
		return "hold-5000"
	case "trap-exit-0":
		return "trap-term-0"
	case "trap-exit-3":
		return "trap-term-3"
	case "daemonize-no-timeout":
		return "daemonize"
	case "badutf":
		return "badutf"
	}
	return ""
}

func equalIntPointer(left, right *int) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func equalStringPointer(left string, right *string) bool {
	if right == nil {
		return left == ""
	}
	return left == *right
}

func TestAtomicWrite(t *testing.T) {
	t.Run("atomic rename defaults to os.Rename", func(t *testing.T) {
		if reflect.ValueOf(renameFile).Pointer() != reflect.ValueOf(os.Rename).Pointer() {
			t.Fatal("renameFile no longer defaults to os.Rename")
		}
	})
	t.Run("atomicWrite preserves the destination and removes its temp when rename returns EIO", func(t *testing.T) { // Mutation captured: removing temp cleanup leaves an extra file after the injected rename error.
		root := t.TempDir()
		dest := filepath.Join(root, "config")
		if err := os.WriteFile(dest, []byte("original\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		realRename := renameFile
		renameFile = func(string, string) error { return errors.New("injected EIO") }
		t.Cleanup(func() { renameFile = realRename })
		if err := AtomicWrite(dest, "replacement\n"); err == nil {
			t.Fatal("expected injected rename failure")
		}
		got, err := os.ReadFile(dest)
		entries, readErr := os.ReadDir(root)
		if err != nil || string(got) != "original\n" || readErr != nil || len(entries) != 1 || entries[0].Name() != "config" {
			t.Fatalf("destination=%q entries=%v readErr=%v fileErr=%v", got, entries, readErr, err)
		}
	})
	t.Run("atomicWrite removes its temporary file when rename fails", func(t *testing.T) { // Mutation captured: omitting cleanup leaves a temp after a failed atomic replacement.
		root := t.TempDir()
		dest := filepath.Join(root, "directory")
		if err := os.Mkdir(dest, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := AtomicWrite(dest, "content"); err == nil {
			t.Fatal("expected replacing a directory to fail")
		}
		entries, err := os.ReadDir(root)
		if err != nil || len(entries) != 1 || entries[0].Name() != "directory" {
			t.Fatalf("directory entries after failed write=%v err=%v", entries, err)
		}
	})
	t.Run("atomicWrite: a new file uses mode 0600", func(t *testing.T) { // Mutation captured: creating a new file with 0644 breaks private task/config state.
		file := filepath.Join(t.TempDir(), "new-file")
		if err := AtomicWrite(file, "new\n"); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(file)
		if err != nil || (runtime.GOOS == "windows" && info.Mode().Perm()&0o200 == 0) || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
			t.Fatalf("mode=%v err=%v", info, err)
		}
	})
	t.Run("atomicWrite: a symlink to an existing file writes through, keeps the link and the target mode", func(t *testing.T) { // JS: "atomicWrite: a symlink to an existing file writes through, keeps the link and the target mode"
		root := t.TempDir()
		target, link := filepath.Join(root, "CLAUDE.md"), filepath.Join(root, "AGENTS.md")
		if err := os.WriteFile(target, []byte("seed\n"), 0o640); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, link); err != nil {
			skipUnavailableSymlink(t, err)
		}
		if err := AtomicWrite(link, "through\n"); err != nil {
			t.Fatal(err)
		}
		info, err := os.Lstat(link)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("link lost: %v %v", info, err)
		}
		got, _ := os.ReadFile(target)
		stat, _ := os.Stat(target)
		if string(got) != "through\n" {
			t.Fatalf("content=%q", got)
		}
		// Windows does not expose POSIX file modes: the write-through and link
		// assertions above stand, the target's mode is unobservable there.
		if runtime.GOOS != "windows" && stat.Mode().Perm() != 0o640 {
			t.Fatalf("content=%q mode=%o", got, stat.Mode().Perm())
		}
	})
	t.Run("atomicWrite: a relative link to a file in another directory resolves to that file", func(t *testing.T) { // JS: "atomicWrite: a relative link to a file in another directory resolves to that file"
		root := t.TempDir()
		other := filepath.Join(root, "other")
		_ = os.Mkdir(other, 0o700)
		target := filepath.Join(other, "CLAUDE.md")
		_ = os.WriteFile(target, []byte("seed\n"), 0o600)
		link := filepath.Join(root, "AGENTS.md")
		if err := os.Symlink(filepath.Join("other", "CLAUDE.md"), link); err != nil {
			skipUnavailableSymlink(t, err)
		}
		if err := AtomicWrite(link, "relative\n"); err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(target)
		if string(got) != "relative\n" {
			t.Fatalf("%q", got)
		}
		entries, _ := os.ReadDir(root)
		if len(entries) != 2 {
			t.Fatalf("temp left: %v", entries)
		}
	})
	t.Run("atomicWrite: a broken chain creates the file at its missing end, links stay links", func(t *testing.T) { // JS: "atomicWrite: a broken chain creates the file at its missing end, links stay links"
		root := t.TempDir()
		a, b, missing := filepath.Join(root, "a.md"), filepath.Join(root, "b.md"), filepath.Join(root, "missing.txt")
		c := filepath.Join(root, "c.md")
		if err := os.Symlink("b.md", a); err != nil {
			skipUnavailableSymlink(t, err)
		}
		if err := os.Symlink("c.md", b); err != nil {
			skipUnavailableSymlink(t, err)
		}
		if err := os.Symlink("./missing.txt", c); err != nil {
			skipUnavailableSymlink(t, err)
		}
		if err := AtomicWrite(a, "created\n"); err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(missing)
		info, _ := os.Lstat(a)
		infoB, _ := os.Lstat(b)
		infoC, _ := os.Lstat(c)
		if string(got) != "created\n" || info.Mode()&os.ModeSymlink == 0 || infoB.Mode()&os.ModeSymlink == 0 || infoC.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("content=%q link=%v", got, info)
		}
	})
	t.Run("atomicWrite: a symlink cycle throws the realpath ELOOP error, leaves the links and no temp behind", func(t *testing.T) { // JS: "atomicWrite: a symlink cycle throws the realpath ELOOP error, leaves the links and no temp behind"
		root := t.TempDir()
		a, b := filepath.Join(root, "a.md"), filepath.Join(root, "b.md")
		if err := os.Symlink("b.md", a); err != nil {
			skipUnavailableSymlink(t, err)
		}
		if err := os.Symlink("a.md", b); err != nil {
			skipUnavailableSymlink(t, err)
		}
		err := AtomicWrite(a, "x\n")
		if err == nil || !strings.Contains(err.Error(), "ELOOP") || !errors.Is(err, syscall.ELOOP) {
			t.Fatalf("got %v", err)
		}
		entries, _ := os.ReadDir(root)
		if len(entries) != 2 {
			t.Fatalf("temp left: %v", entries)
		}
	})
}

func TestStateProjectRoot(t *testing.T) {
	var reference *windowsReference
	if runtime.GOOS == "windows" {
		loaded := readWindowsReference(t)
		reference = &loaded
	}
	t.Run("state root preserves the main checkout and non-git path behavior", func(t *testing.T) { // JS: "state root preserves the main checkout and non-git path behavior"
		root := t.TempDir()
		repo := filepath.Join(root, "repo")
		outside := filepath.Join(root, "outside")
		_ = os.Mkdir(repo, 0o700)
		_ = os.Mkdir(outside, 0o700)
		gitInit(t, repo)
		realRepo, _ := filepath.EvalSymlinks(repo)
		env := EnvFromOS()
		assertRootReference(t, reference, "repo", env, repo, root, realRepo, false)
		assertRootReference(t, reference, "repo", env, repo, root, realRepo, true)
		realOutside, _ := filepath.EvalSymlinks(outside)
		assertRootReference(t, reference, "outside", env, realOutside, root, realOutside, false)
		assertRootReference(t, reference, "outside", env, realOutside, root, realOutside, true)
	})
	t.Run("a main checkout inside a .git path does not become the linked worktree state root", func(t *testing.T) { // Mutation captured: removing the .git segment guard selects metadata inside .git.
		root := t.TempDir()
		repo := filepath.Join(root, ".git", "cont", "proj")
		if err := os.MkdirAll(repo, 0o700); err != nil {
			t.Fatal(err)
		}
		gitInit(t, repo)
		worker := filepath.Join(root, "worker")
		gitRun(t, repo, "worktree", "add", "-q", "-b", "worker", worker)
		resetRootCacheForTests()
		got := StateProjectRoot(EnvFromOS(), worker)
		canonicalWorker, err := filepath.EvalSymlinks(worker)
		if err != nil {
			t.Fatal(err)
		}
		if got != canonicalWorker {
			t.Fatalf("state root=%q want linked worktree %q", got, canonicalWorker)
		}
	})
	t.Run("a separate git dir containing its listed worktree is not used as the state root", func(t *testing.T) { // Mutation captured: ignoring isWithin selects the common git directory as a checkout.
		root := t.TempDir()
		common := filepath.Join(root, "container")
		repo := filepath.Join(common, "repo")
		if err := os.MkdirAll(repo, 0o700); err != nil {
			t.Fatal(err)
		}
		gitRun(t, root, "init", "-q", "--separate-git-dir="+common, repo)
		gitRun(t, repo, "config", "user.email", "test@example.invalid")
		gitRun(t, repo, "config", "user.name", "Test")
		if err := os.WriteFile(filepath.Join(repo, "tracked"), []byte("fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		gitRun(t, repo, "add", "tracked")
		gitRun(t, repo, "commit", "-qm", "fixture")
		worker := filepath.Join(root, "worker")
		gitRun(t, repo, "worktree", "add", "-q", "-b", "worker", worker)
		resetRootCacheForTests()
		canonicalWorker, err := filepath.EvalSymlinks(worker)
		if err != nil {
			t.Fatal(err)
		}
		if got := StateProjectRoot(EnvFromOS(), worker); got != canonicalWorker {
			t.Fatalf("state root=%q want worker %q", got, canonicalWorker)
		}
	})
	t.Run("roster, status, and wait in a linked worktree read the main checkout state", func(t *testing.T) { // JS: "roster, status, and wait in a linked worktree read the main checkout state"
		root := t.TempDir()
		repo := filepath.Join(root, "repo")
		_ = os.Mkdir(repo, 0o700)
		gitInit(t, repo)
		worker := filepath.Join(repo, ".worktrees", "worker")
		_ = os.MkdirAll(filepath.Dir(worker), 0o700)
		gitRun(t, repo, "worktree", "add", "-q", "-b", "worker", worker)
		realRepo, _ := filepath.EvalSymlinks(repo)
		realWorker, _ := filepath.EvalSymlinks(worker)
		env := EnvFromOS()
		assertRootReference(t, reference, "linked-worker", env, worker, repo, realWorker, false)
		assertRootReference(t, reference, "linked-worker", env, worker, repo, realRepo, true)
	})
	t.Run("a linked worktree of a bare repository keeps state in that worktree", func(t *testing.T) { // JS: "a linked worktree of a bare repository keeps state in that worktree"
		root := t.TempDir()
		source := filepath.Join(root, "source")
		_ = os.Mkdir(source, 0o700)
		gitInit(t, source)
		bare := filepath.Join(root, "origin.git")
		gitRun(t, root, "clone", "--bare", "-q", source, bare)
		worker := filepath.Join(root, "worker")
		gitRun(t, root, "--git-dir", bare, "worktree", "add", "-q", "-b", "worker", worker, "HEAD")
		realWorker, _ := filepath.EvalSymlinks(worker)
		env := EnvFromOS()
		assertRootReference(t, reference, "bare-linked-worker", env, worker, root, realWorker, false)
		assertRootReference(t, reference, "bare-linked-worker", env, worker, root, realWorker, true)
	})
	t.Run("linked and ordinary submodule checkouts use the submodule checkout as state root", func(t *testing.T) { // JS: "linked and ordinary submodule checkouts use the submodule checkout as state root"
		root := t.TempDir()
		super, src := filepath.Join(root, "super"), filepath.Join(root, "src")
		_ = os.Mkdir(super, 0o700)
		_ = os.Mkdir(src, 0o700)
		gitInit(t, super)
		gitInit(t, src)
		gitRun(t, super, "-c", "protocol.file.allow=always", "submodule", "add", "-q", src, "submodule")
		gitRun(t, super, "add", ".gitmodules", "submodule")
		gitRun(t, super, "commit", "-qm", "submodule")
		sub := filepath.Join(super, "submodule")
		realSub, _ := filepath.EvalSymlinks(sub)
		env := EnvFromOS()
		assertRootReference(t, reference, "submodule", env, sub, root, realSub, false)
		assertRootReference(t, reference, "submodule", env, sub, root, realSub, true)
		linked := filepath.Join(root, "sub-linked")
		gitRun(t, sub, "worktree", "add", "-q", "-b", "linked", linked, "HEAD")
		realLinked, _ := filepath.EvalSymlinks(linked)
		assertRootReference(t, reference, "linked-submodule", env, linked, root, realLinked, false)
		assertRootReference(t, reference, "linked-submodule", env, linked, root, realSub, true)
	})
	t.Run("core.worktree under the superproject .git falls back to project root", func(t *testing.T) { // JS: "core.worktree that resolves up to the superproject .git falls back to projectRoot"
		// Mutation captured: accepting a .git path segment stores state inside Git metadata.
		for index, target := range []string{"../..", filepath.Join("..", "..", "..", "..", "repo", ".git")} {
			root := t.TempDir()
			super, src := filepath.Join(root, "repo"), filepath.Join(root, "src")
			_ = os.Mkdir(super, 0o700)
			_ = os.Mkdir(src, 0o700)
			gitInit(t, super)
			gitInit(t, src)
			gitRun(t, super, "-c", "protocol.file.allow=always", "submodule", "add", "-q", src, "submodule")
			gitRun(t, super, "add", ".gitmodules", "submodule")
			gitRun(t, super, "commit", "-qm", "submodule")
			sub := filepath.Join(super, "submodule")
			worker := filepath.Join(root, "sub-worktree")
			gitRun(t, sub, "worktree", "add", "-q", "-b", "linked", worker, "HEAD")
			common, status := runGit(EnvFromOS(), worker, "rev-parse", "--git-common-dir")
			if status != 0 {
				t.Fatal("cannot resolve fixture common directory")
			}
			common = resolveFrom(worker, strings.TrimSpace(common))
			configured := target
			if filepath.IsAbs(target) || strings.Contains(target, "repo") {
				configured = filepath.Join(super, ".git")
			}
			gitRun(t, worker, "--git-dir", common, "config", "core.worktree", configured)
			resetRootCacheForTests()
			realWorker, _ := filepath.EvalSymlinks(worker)
			env := EnvFromOS()
			key := "core-worktree-dotdot"
			if index == 1 {
				key = "core-worktree-git"
			}
			assertRootReference(t, reference, key, env, worker, root, realWorker, false)
			assertRootReference(t, reference, key, env, worker, root, realWorker, true)
			got := StateProjectRoot(env, worker)
			if hasGitPathSegment(got) {
				t.Fatalf("target=%q state root=%q contains .git path segment", configured, got)
			}
		}
	})
	t.Run("Win32 metadata guard recognizes .git path segments", func(t *testing.T) { // JS: "state-root metadata guard recognizes .git path segments on Win32"
		if !hasGitPathSegment(`C:\repo\.git\worktrees\worker`) || hasGitPathSegment(`C:\repo\.gitmodules\worktrees\worker`) {
			t.Fatal("Win32 .git path-segment recognition mismatch")
		}
	})
}

func TestProjectRootCache(t *testing.T) {
	// Mutation captured: bypassing the cache repeats git calls for the same cwd and env.
	resetRootCacheForTests()
	old := gitRunner
	defer func() { gitRunner = old; resetRootCacheForTests() }()
	calls := 0
	gitRunner = func(_ Env, cwd string, args ...string) (string, int) {
		calls++
		if len(args) == 1 && args[0] == "--show-toplevel" {
			return cwd + "\n", 0
		}
		return "", 1
	}
	cwd := filepath.Join(t.TempDir(), "project")
	if first, second := ProjectRoot(Env{"GIT_DIR": "one"}, cwd), ProjectRoot(Env{"GIT_DIR": "one"}, cwd); first != second || calls != 1 {
		t.Fatalf("same key results=%q/%q calls=%d", first, second, calls)
	}
	ProjectRoot(Env{"GIT_DIR": "two"}, cwd)
	if calls != 2 {
		t.Fatalf("different GIT_DIR should use a distinct key, calls=%d", calls)
	}
	resetRootCacheForTests()
	calls = 0
	repoRoot := filepath.Join(t.TempDir(), "repo")
	commonDir := filepath.Join(repoRoot, ".git")
	gitRunner = func(_ Env, _ string, args ...string) (string, int) {
		calls++
		switch strings.Join(args, " ") {
		case "rev-parse --git-dir":
			return filepath.Join(commonDir, "worktrees", "worker") + "\n", 0
		case "rev-parse --git-common-dir":
			return commonDir + "\n", 0
		default:
			return "", 1
		}
	}
	stateCwd := filepath.Join(t.TempDir(), "worker")
	first := StateProjectRoot(Env{"GIT_WORK_TREE": "worker"}, stateCwd)
	second := StateProjectRoot(Env{"GIT_WORK_TREE": "worker"}, stateCwd)
	wantRoot := repoRoot
	if first != wantRoot || second != first || calls != 2 {
		t.Fatalf("state root=%q/%q calls=%d", first, second, calls)
	}
}

func resetRootCacheForTests() {
	rootCache.Lock()
	rootCache.values = make(map[string]string)
	rootCache.Unlock()
}

func gitRun(t *testing.T, cwd string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}
func gitInit(t *testing.T, root string) {
	t.Helper()
	gitRun(t, root, "init", "-q")
	gitRun(t, root, "config", "user.email", "test@example.invalid")
	gitRun(t, root, "config", "user.name", "Test")
	_ = os.WriteFile(filepath.Join(root, "tracked"), []byte("fixture\n"), 0o600)
	gitRun(t, root, "add", "tracked")
	gitRun(t, root, "commit", "-qm", "fixture")
}

func TestRunCliTempDirMatchesJSTempDirFor(t *testing.T) { // JS: "send reports a temporary-file error when TMPDIR is a file"
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Run("a file on the TMPDIR path is EEXIST on stderr, and nothing runs", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "not-a-directory")
		if err := os.WriteFile(file, []byte("file"), 0o600); err != nil {
			t.Fatal(err)
		}
		env := EnvFromOS()
		env["TMPDIR"] = file
		r := RunExecutable(exe, []string{"-test.run=^$"}, RunOptions{Env: env, TimeoutMs: 30000, MergeOutput: true})
		if r.Status != nil || r.Error != "EEXIST" || r.Stdout != "" || r.Stderr != "soho: cannot write temporary files: EEXIST\n" {
			t.Fatalf("result = %#v", r)
		}
	})
	t.Run("a missing TMPDIR is created, as mkdirSync recursive does", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "a", "b")
		env := EnvFromOS()
		env["TMPDIR"] = dir
		r := RunExecutable(exe, []string{"-test.run=^$"}, RunOptions{Env: env, TimeoutMs: 30000, OutputFiles: true})
		if r.Status == nil || *r.Status != 0 || r.Error != "" {
			t.Fatalf("result = %#v", r)
		}
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Fatalf("TMPDIR not created: %v", err)
		}
	})
}
