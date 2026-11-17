package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// Every command the JS entry used to know has a native case since R1 retired
// the JS fallback, so a known command without a native case dies 2 with the
// unknown-command diagnostic instead of probing Node, Bun or
// HERDR_SOHO_JS_RUNTIME. The test binary registers such a command to reach
// that path.
func init() { knownCommands["not-ported"] = true }

// hostileRuntimeEnv makes native Go poisons discoverable on every platform.
// Every invocation, including a version probe, is recorded before responding.
func hostileRuntimeEnv(t *testing.T, override string) (platform.Env, []string) {
	t.Helper()
	bin := t.TempDir()
	// Also poison probes that accidentally inherit the test process env.
	t.Setenv("SOHO_FAKECLI_CONFIG", bin)
	var records []string
	var custom string
	for _, name := range []string{"node", "bun", "custom-node"} {
		path, err := fakecli.Install(t, bin, name, []fakecli.Rule{
			{Argv: []string{"-e"}, ArgvPrefix: true, Stdout: "20"},
			{AnyArgs: true, Code: 17},
		})
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, filepath.Join(bin, name+".calls.jsonl"))
		if name == "custom-node" {
			custom = path
		}
	}
	env := platform.Env{
		"PATH":                bin,
		"PATHEXT":             ".EXE;.CMD;.BAT;.COM",
		"SOHO_FAKECLI_CONFIG": bin,
		"SOHO_SKILL_DIR":      "/skill",
		"HOME":                t.TempDir(),
		"XDG_CONFIG_HOME":     t.TempDir(),
		"USERPROFILE":         t.TempDir(),
	}
	if runtime.GOOS == "windows" {
		env["SystemRoot"] = testutil.HostSystemRoot()
	}
	switch override {
	case "native":
		override = custom
	case "non-executable":
		override = filepath.Join(bin, "plain")
		if err := os.WriteFile(override, []byte("not an executable"), 0o600); err != nil {
			t.Fatal(err)
		}
	case "directory":
		override = t.TempDir()
	}
	if override != "" {
		env["HERDR_SOHO_JS_RUNTIME"] = override
	}
	env["GORACE"] = fakecli.RaceOptions(env.List())
	return env, records
}

// assertNoRuntimeCalled proves absence of a probed or executed runtime from
// the missing invocation record (not from a call count).
func assertNoRuntimeCalled(t *testing.T, records []string) {
	t.Helper()
	for _, record := range records {
		if _, err := os.Stat(record); !os.IsNotExist(err) {
			t.Fatalf("a runtime was called: %s exists (%v)", record, err)
		}
	}
}

func TestRuntimePoisonRecordsVersionProbe(t *testing.T) {
	env, records := hostileRuntimeEnv(t, "native")
	for i, name := range []string{"node", "bun", "custom-node"} {
		path, ok := platform.FindExecutable(name, env, platform.Current())
		if !ok {
			t.Fatalf("native %s poison is not discoverable", name)
		}
		cmd := exec.Command(path, "-e", "1")
		cmd.Env = env.List()
		out, err := cmd.Output()
		if err != nil || string(out) != "20" {
			t.Fatalf("%s probe: out=%q err=%v", name, out, err)
		}
		calls, err := fakecli.ReadCalls(records[i])
		if err != nil || len(calls) != 1 || strings.Join(calls[0].Argv, " ") != "-e 1" {
			t.Fatalf("%s did not record its version probe: calls=%v err=%v", name, calls, err)
		}
	}
}

func TestKnownCommandWithoutNativeCaseDiesNatively(t *testing.T) {
	// Replaces the fallback tests: the old runJS probed HERDR_SOHO_JS_RUNTIME
	// then node then bun for a known command without a Go case and inherited
	// the child's exit code. R1 dies 2 natively with the exact
	// unknown-command diagnostic and never runs a runtime.
	overrides := []struct {
		name, value string
	}{
		{"no override", ""},
		{"non-absolute override is ignored", "node"},
		{"relative override is ignored", "./node"},
		{"hostile executable override is not run", "native"},
		{"non-executable regular file override is ignored", "non-executable"},
		{"directory override is ignored", "directory"},
	}
	for _, o := range overrides {
		t.Run(o.name, func(t *testing.T) {
			env, records := hostileRuntimeEnv(t, o.value)
			var out, errOut bytes.Buffer
			oldOut, oldErr := platform.Stdout, platform.Stderr
			platform.Stdout, platform.Stderr = &out, &errOut
			code := Run([]string{"not-ported"}, env)
			platform.Stdout, platform.Stderr = oldOut, oldErr
			if code != 2 || out.Len() != 0 || errOut.String() != "soho: unknown command 'not-ported'\n" {
				t.Fatalf("code=%d out=%q err=%q", code, out.String(), errOut.String())
			}
			assertNoRuntimeCalled(t, records)
		})
	}
}

func TestUnknownCommandHostileInput(t *testing.T) {
	// Unknown input dies 2 natively, names the command verbatim, and probes
	// no runtime even when hostile runtime env is present.
	for _, name := range []string{"nope", "nope;rm -rf /", "$(reboot)", `back\slash`, "new\nline", "-x"} {
		env, records := hostileRuntimeEnv(t, "node")
		var out, errOut bytes.Buffer
		oldOut, oldErr := platform.Stdout, platform.Stderr
		platform.Stdout, platform.Stderr = &out, &errOut
		code := Run([]string{name}, env)
		platform.Stdout, platform.Stderr = oldOut, oldErr
		if code != 2 || out.Len() != 0 || errOut.String() != "soho: unknown command '"+name+"'\n" {
			t.Fatalf("name=%q code=%d out=%q err=%q", name, code, out.String(), errOut.String())
		}
		assertNoRuntimeCalled(t, records)
	}
}

func TestNativeKindsRunsUnderHostileRuntimeEnv(t *testing.T) {
	// A real native consumer: with hostile runtime env and a restricted PATH
	// a real command runs end to end (the actual table, exit 0) and nothing
	// is probed or executed.
	env, records := hostileRuntimeEnv(t, "native")
	var out, errOut bytes.Buffer
	oldOut, oldErr := platform.Stdout, platform.Stderr
	platform.Stdout, platform.Stderr = &out, &errOut
	code := Run([]string{"kinds"}, env)
	platform.Stdout, platform.Stderr = oldOut, oldErr
	if code != 0 || errOut.Len() != 0 || !strings.HasPrefix(out.String(), "KIND     EXECUTABLE") {
		t.Fatalf("code=%d out=%q err=%q", code, out.String(), errOut.String())
	}
	assertNoRuntimeCalled(t, records)
}

func TestHelpAndUnknown(t *testing.T) {
	t.Run("help variants print the usage", func(t *testing.T) { // JS: "parity: help, -h, --help and no command print the usage (rc 0)"
		for _, args := range [][]string{{"help"}, {"-h"}, {"--help"}, {}} {
			var out, errOut bytes.Buffer
			oldOut, oldErr := platform.Stdout, platform.Stderr
			platform.Stdout, platform.Stderr = &out, &errOut
			code := Run(args, platform.Env{})
			platform.Stdout, platform.Stderr = oldOut, oldErr
			if code != 0 || out.String() != usage || errOut.Len() != 0 {
				t.Errorf("args=%v code=%d err=%q", args, code, errOut.String())
			}
		}
	})
	t.Run("unknown command uses the Bash-compatible message", func(t *testing.T) { // JS: "parity: an unknown command dies 2 with the bash message"
		// Mutation captured: dropping or changing the unknown-command diagnostic changes both its exit and exact stderr.
		var errOut bytes.Buffer
		oldErr := platform.Stderr
		platform.Stderr = &errOut
		code := Run([]string{"nope"}, platform.Env{})
		platform.Stderr = oldErr
		if code != 2 || errOut.String() != "soho: unknown command 'nope'\n" {
			t.Fatalf("code=%d err=%q", code, errOut.String())
		}
	})
	if !strings.Contains(usage, "soho — role-agent layer") {
		t.Fatal("missing usage text")
	}
}

func TestNowriteAndFormerEnv(t *testing.T) {
	// Mutation captured: allowing extra arguments after doctor or roster bypasses NOWRITE.
	var errOut bytes.Buffer
	oldErr := platform.Stderr
	platform.Stderr = &errOut
	code := Run([]string{"kinds"}, platform.Env{"SOHO_NOWRITE": "1", "HERDR_SOHO_SOCKET_PATH": "legacy"})
	platform.Stderr = oldErr
	if code != 2 || !strings.Contains(errOut.String(), "SOHO_NOWRITE=1 is read-only") || !strings.Contains(errOut.String(), "rejected: kinds") {
		t.Fatalf("code=%d err=%q", code, errOut.String())
	}
	for _, args := range [][]string{{"doctor", "--fix"}, {"roster", "extra"}} {
		var stderr bytes.Buffer
		oldErr = platform.Stderr
		platform.Stderr = &stderr
		got := Run(args, nowriteRoutingEnv())
		platform.Stderr = oldErr
		if got != 2 || !strings.Contains(stderr.String(), "extra argument") {
			t.Errorf("NOWRITE args=%v code=%d stderr=%q", args, got, stderr.String())
		}
	}
	var stderr bytes.Buffer
	oldErr = platform.Stderr
	platform.Stderr = &stderr
	got := Run([]string{"kinds"}, platform.Env{"HERDR_SOHO_NOWRITE": "1"})
	platform.Stderr = oldErr
	if got != 0 || stderr.String() != "" {
		t.Errorf("former NOWRITE code=%d stderr=%q", got, stderr.String())
	}
}

func nowriteRoutingEnv() platform.Env {
	return isolatedUserEnv(platform.Env{"SOHO_NOWRITE": "1"})
}

func TestStatePathFileComponent(t *testing.T) {
	env, repo := commandFixture(t)
	env["HERDR_ENV"] = "1"
	blocked := filepath.Join(repo, "blocked")
	if err := os.WriteFile(blocked, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	env["SOHO_DIR"] = filepath.Join("blocked", "state")
	code, out, stderr := runIn(t, []string{"status"}, env, repo)
	if code != 4 || !strings.Contains(stderr, blocked) || strings.Contains(stderr, "panic") {
		t.Fatalf("status SOHO_DIR=%q code=%d stdout=%q stderr=%q", env["SOHO_DIR"], code, out, stderr)
	}
}

func TestReadOnlyGitignoreStateRecovery(t *testing.T) {
	env, repo := commandFixture(t)
	// doctor probes codex once per process (spawn.CodexSupportsNoDaemon): a
	// fake codex keeps the host's real one from deciding it for later tests.
	bin := t.TempDir()
	for _, name := range []string{"herdr", "codex"} {
		if _, err := fakecli.Install(t, bin, name, nil); err != nil {
			t.Fatal(err)
		}
	}
	// Keep the host PATH after the fakes: the ignore update needs git.
	faked := platform.Env{}
	for _, item := range fakecli.Env(env.List(), bin, fakecli.EnvOptions{IncludeBasePath: true}) {
		if key, value, ok := strings.Cut(item, "="); ok {
			faked[key] = value
		}
	}
	env = faked
	env["HERDR_ENV"] = "1"
	ignore := filepath.Join(repo, ".gitignore")
	if err := os.WriteFile(ignore, []byte("# fixture\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ignore, 0o400); err != nil {
		t.Skipf("cannot make .gitignore read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(ignore, 0o600) })
	f, err := os.OpenFile(ignore, os.O_WRONLY|os.O_APPEND, 0)
	if err == nil {
		_ = f.Close()
		t.Skip("OS or effective user permits writing read-only .gitignore")
	}
	env["SOHO_DIR"] = "local-state"
	state := filepath.Join(repo, "local-state")
	if err := os.MkdirAll(filepath.Join(state, "ws"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "ws", "fixture"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := runIn(t, []string{"status"}, env, repo)
	if code != 4 || !strings.Contains(stderr, ignore) || strings.Contains(stderr, "panic") {
		t.Fatalf("status SOHO_DIR=%q code=%d stdout=%q stderr=%q", env["SOHO_DIR"], code, out, stderr)
	}
	code, out, stderr = runIn(t, []string{"doctor"}, env, repo)
	if code != 0 || !strings.Contains(out, "warn   state ignore update failed:") || !strings.Contains(out, ignore) || !strings.Contains(out, "first_run:") || !strings.Contains(out, "state dir writable: "+state) {
		t.Fatalf("doctor SOHO_DIR=%q code=%d stdout=%q stderr=%q", env["SOHO_DIR"], code, out, stderr)
	}
}

func TestUnwritableUninitializedState(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod does not enforce Unix directory permissions on Windows")
	}
	env, repo := commandFixture(t)
	bin := t.TempDir()
	if _, err := fakecli.Install(t, bin, "herdr", nil); err != nil {
		t.Fatal(err)
	}
	env = withFakeCLI(env, bin)
	env["HERDR_ENV"] = "1"
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
	if err := os.Mkdir(filepath.Join(parent, "probe"), 0o700); err == nil {
		_ = os.Remove(filepath.Join(parent, "probe"))
		t.Skip("effective user can write despite chmod")
	}
	state := filepath.Join(parent, "state")
	env["SOHO_DIR"] = state
	code, out, stderr := runIn(t, []string{"status"}, env, repo)
	if code != 4 || !strings.Contains(stderr, state) || !strings.Contains(stderr, "permission denied") || strings.Count(stderr, "\n") != 1 || strings.Contains(stderr, "panic") {
		t.Fatalf("status code=%d stdout=%q stderr=%q", code, out, stderr)
	}
	code, out, stderr = runIn(t, []string{"doctor"}, env, repo)
	if code != 0 || !strings.Contains(out, "warn   state dir not writable: "+state) {
		t.Fatalf("doctor code=%d stdout=%q stderr=%q", code, out, stderr)
	}
	code, out, stderr = runIn(t, []string{"help"}, env, repo)
	if code != 0 {
		t.Fatalf("help code=%d stdout=%q stderr=%q", code, out, stderr)
	}
}
