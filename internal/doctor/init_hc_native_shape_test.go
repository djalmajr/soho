package doctor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/sessionmeta"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// nativeCallerListJSON formats the immutable native-shaped caller summary
// matching caller-core-list-sanitized-20261107.json.
func nativeCallerListJSON(cwd string) string {
	return fmt.Sprintf(`{"terminals":[{
  "address": "t2c",
  "name": null,
  "state": "live",
  "status": "working",
  "cwd": %q,
  "foreground": {
    "name": null,
    "pid": null,
    "cwd": null
  }
}]}`, cwd)
}

// nativeCallerGetJSON formats the immutable native-shaped caller detail
// matching caller-core-get-sanitized-20261107.json.
func nativeCallerGetJSON(cwd string) string {
	return fmt.Sprintf(`{
  "address": "t2c",
  "name": null,
  "state": "live",
  "pid": 54449,
  "cwd": %q,
  "status": {
    "app": "codex",
    "state": "working"
  },
  "foreground": {
    "name": "codex",
    "pid": 54449,
    "cwd": %q
  }
}`, cwd, cwd)
}

// knownKindUnnamedCallerListJSON formats caller summary that has recognized program
// hints (e.g. app "codex") but empty/null name.
func knownKindUnnamedCallerListJSON(cwd string) string {
	return fmt.Sprintf(`{"terminals":[{
  "address": "t2c",
  "name": null,
  "state": "live",
  "status": {"app": "codex", "state": "working"},
  "cwd": %q,
  "foreground": {
    "name": "codex",
    "pid": 54449,
    "cwd": %q
  }
}]}`, cwd, cwd)
}

func initHcTestFixture(t *testing.T, rulesFn func(root string) []fakecli.Rule) (platform.Env, string) {
	t.Helper()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	bin := filepath.Join(root, "bin")
	state := filepath.Join(root, "state")
	for _, dir := range []string{home, bin, state} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	init := exec.Command("git", "init", "-q")
	init.Dir = root
	if out, err := init.CombinedOutput(); err != nil {
		t.Fatalf("git init: %s: %v", out, err)
	}
	rules := rulesFn(root)
	if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
		t.Fatal(err)
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("host has no git: %v", err)
	}
	env := platform.Env{
		"HOME":                home,
		"USERPROFILE":         home,
		"PATH":                bin + string(filepath.ListSeparator) + filepath.Dir(gitPath),
		"XDG_CONFIG_HOME":     filepath.Join(home, "config"),
		"TMPDIR":              root,
		"SOHO_TRANSPORT":      "hc",
		"HC_TERMINAL_ID":      "t2c",
		"SOHO_DIR":            state,
		"SOHO_FAKECLI_CONFIG": bin,
	}
	return env, root
}

func runCmdInitCapture(t *testing.T, env platform.Env, root string) (string, string, any) {
	t.Helper()
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var outBuf, errBuf strings.Builder
	platform.Stdout, platform.Stderr = &outBuf, &errBuf
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		CmdInit(&core.Config{Entries: map[string]core.ConfigEntry{}}, env, root)
	}()
	platform.Stdout, platform.Stderr = oldOut, oldErr
	return outBuf.String(), errBuf.String(), recovered
}

func assertNoStateWrite(t *testing.T, env platform.Env, repoRoot string, session string) {
	t.Helper()
	stateBase := core.StateRootPathForProject(&core.Config{Entries: map[string]core.ConfigEntry{}}, env, repoRoot)
	sessionDir := filepath.Join(stateBase, session)
	if _, err := os.Stat(sessionDir); err == nil {
		t.Fatalf("state directory %s was created on failure", sessionDir)
	}
	store := sessionmeta.Store{StateBase: stateBase, SessionID: session}
	if _, ok, err := store.Read(); err == nil && ok {
		t.Fatalf("orchestrator record was registered on failure")
	}
}

// TestInitHcNativeShape_Success verifies that when the matching caller
// summary in hc ls lacks program identity, detailed hc get is queried,
// resolving kind codex and setting private display name t2c.
func TestInitHcNativeShape_Success(t *testing.T) {
	env, repoRoot := initHcTestFixture(t, func(root string) []fakecli.Rule {
		return []fakecli.Rule{
			{Argv: []string{"get", "t2c", "--json"}, Stdout: nativeCallerGetJSON(root)},
			{Argv: []string{"ls", "--json"}, Stdout: nativeCallerListJSON(root)},
		}
	})

	stdout, stderr, recovered := runCmdInitCapture(t, env, repoRoot)
	if recovered != nil {
		if exitErr, ok := recovered.(*platform.ExitError); ok {
			t.Fatalf("CmdInit died unexpectedly: code=%d msg=%q\nstderr=%s", exitErr.Code, exitErr.Msg, stderr)
		}
		t.Fatalf("CmdInit panicked: %#v", recovered)
	}

	v, err := jsonjs.Parse([]byte(stdout))
	if err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout=%s", err, stdout)
	}
	obj, ok := v.(*jsonjs.Object)
	if !ok {
		t.Fatalf("stdout JSON is not an object: %s", stdout)
	}
	if orch, ok := obj.Get("orchestrator"); !ok || orch != "t2c" {
		t.Fatalf("orchestrator=%v want t2c", orch)
	}
	if pane, ok := obj.Get("pane_id"); !ok || pane != "t2c" {
		t.Fatalf("pane_id=%v want t2c", pane)
	}
	if ws, ok := obj.Get("workspace_id"); !ok || ws != "hc-t2c" {
		t.Fatalf("workspace_id=%v want hc-t2c", ws)
	}

	stateBase := core.StateRootPathForProject(&core.Config{Entries: map[string]core.ConfigEntry{}}, env, repoRoot)
	store := sessionmeta.Store{StateBase: stateBase, SessionID: "hc-t2c"}
	rec, ok, err := store.Read()
	if err != nil {
		t.Fatalf("store.Read error: %v", err)
	}
	if !ok {
		t.Fatal("store.Read returned ok=false; no record registered")
	}
	if rec.Kind != "codex" {
		t.Fatalf("registered kind=%q want codex", rec.Kind)
	}
	if rec.Name != "t2c" {
		t.Fatalf("registered name=%q want t2c", rec.Name)
	}
	if rec.Terminal != "t2c" {
		t.Fatalf("registered terminal=%q want t2c", rec.Terminal)
	}
}

// TestInitHcNativeShape_NegativeAddressMismatch asserts refusal and no-write
// when the detailed record reports a different address than caller.
func TestInitHcNativeShape_NegativeAddressMismatch(t *testing.T) {
	env, repoRoot := initHcTestFixture(t, func(root string) []fakecli.Rule {
		mismatchGet := fmt.Sprintf(`{"address":"t99","name":null,"state":"live","cwd":%q,"status":{"app":"codex","state":"working"},"foreground":{"name":"codex","cwd":%q}}`, root, root)
		return []fakecli.Rule{
			{Argv: []string{"get", "t2c", "--json"}, Call: 1, Stdout: nativeCallerGetJSON(root)},
			{Argv: []string{"get", "t2c", "--json"}, Call: 2, Stdout: mismatchGet},
			{Argv: []string{"ls", "--json"}, Stdout: nativeCallerListJSON(root)},
		}
	})

	_, _, recovered := runCmdInitCapture(t, env, repoRoot)
	exitErr, ok := recovered.(*platform.ExitError)
	if !ok {
		t.Fatalf("expected ExitError, got %#v", recovered)
	}
	if exitErr.Code != 2 {
		t.Fatalf("exit code=%d want 2", exitErr.Code)
	}
	assertNoStateWrite(t, env, repoRoot, "hc-t2c")
}

// TestInitHcNativeShape_NegativeSessionMismatch asserts refusal and no-write
// when detailed record carries a different session.
func TestInitHcNativeShape_NegativeSessionMismatch(t *testing.T) {
	env, repoRoot := initHcTestFixture(t, func(root string) []fakecli.Rule {
		sessionMismatchGet := fmt.Sprintf(`{"address":"t2c","name":null,"state":"live","labels":{"soho.session":"other-session"},"cwd":%q,"status":{"app":"codex","state":"working"},"foreground":{"name":"codex","cwd":%q}}`, root, root)
		return []fakecli.Rule{
			{Argv: []string{"get", "t2c", "--json"}, Call: 1, Stdout: nativeCallerGetJSON(root)},
			{Argv: []string{"get", "t2c", "--json"}, Call: 2, Stdout: sessionMismatchGet},
			{Argv: []string{"ls", "--json"}, Stdout: nativeCallerListJSON(root)},
		}
	})

	_, _, recovered := runCmdInitCapture(t, env, repoRoot)
	exitErr, ok := recovered.(*platform.ExitError)
	if !ok {
		t.Fatalf("expected ExitError, got %#v", recovered)
	}
	if exitErr.Code != 2 {
		t.Fatalf("exit code=%d want 2", exitErr.Code)
	}
	if !strings.Contains(exitErr.Msg, "detailed caller session does not match") {
		t.Fatalf("msg=%q want to contain 'detailed caller session does not match'", exitErr.Msg)
	}
	calls, err := fakecli.ReadCalls(filepath.Join(repoRoot, "bin", "hc.calls.jsonl"))
	if err != nil {
		t.Fatalf("ReadCalls error: %v", err)
	}
	var getCalls int
	for _, c := range calls {
		if len(c.Argv) >= 2 && c.Argv[0] == "get" && c.Argv[1] == "t2c" {
			getCalls++
		}
	}
	if getCalls < 2 {
		t.Fatalf("expected at least 2 get calls (initial Caller lookup and detailed lookup), got %d (calls: %#v)", getCalls, calls)
	}
	assertNoStateWrite(t, env, repoRoot, "hc-t2c")
	assertNoStateWrite(t, env, repoRoot, "other-session")
}

// TestInitHcNativeShape_NegativeRootMismatch asserts refusal and no-write
// when detailed record reports a cwd outside project root.
func TestInitHcNativeShape_NegativeRootMismatch(t *testing.T) {
	otherDir := t.TempDir()
	env, repoRoot := initHcTestFixture(t, func(root string) []fakecli.Rule {
		rootMismatchGet := fmt.Sprintf(`{"address":"t2c","name":null,"state":"live","cwd":%q,"status":{"app":"codex","state":"working"},"foreground":{"name":"codex","cwd":%q}}`, otherDir, otherDir)
		return []fakecli.Rule{
			{Argv: []string{"get", "t2c", "--json"}, Call: 1, Stdout: nativeCallerGetJSON(root)},
			{Argv: []string{"get", "t2c", "--json"}, Call: 2, Stdout: rootMismatchGet},
			{Argv: []string{"ls", "--json"}, Stdout: nativeCallerListJSON(root)},
		}
	})

	_, _, recovered := runCmdInitCapture(t, env, repoRoot)
	exitErr, ok := recovered.(*platform.ExitError)
	if !ok {
		t.Fatalf("expected ExitError, got %#v", recovered)
	}
	if exitErr.Code != 2 {
		t.Fatalf("exit code=%d want 2", exitErr.Code)
	}
	if !strings.Contains(exitErr.Msg, "detailed caller root does not match") {
		t.Fatalf("msg=%q want to contain 'detailed caller root does not match'", exitErr.Msg)
	}
	assertNoStateWrite(t, env, repoRoot, "hc-t2c")
}

// TestInitHcNativeShape_NegativeUnknownKind asserts refusal and no-write
// when detailed record names an unknown program.
func TestInitHcNativeShape_NegativeUnknownKind(t *testing.T) {
	env, repoRoot := initHcTestFixture(t, func(root string) []fakecli.Rule {
		unknownKindGet := fmt.Sprintf(`{"address":"t2c","name":null,"state":"live","cwd":%q,"status":{"app":"custom-shell","state":"working"},"foreground":{"name":"custom-shell","cwd":%q}}`, root, root)
		return []fakecli.Rule{
			{Argv: []string{"get", "t2c", "--json"}, Call: 1, Stdout: nativeCallerGetJSON(root)},
			{Argv: []string{"get", "t2c", "--json"}, Call: 2, Stdout: unknownKindGet},
			{Argv: []string{"ls", "--json"}, Stdout: nativeCallerListJSON(root)},
		}
	})

	_, _, recovered := runCmdInitCapture(t, env, repoRoot)
	exitErr, ok := recovered.(*platform.ExitError)
	if !ok {
		t.Fatalf("expected ExitError, got %#v", recovered)
	}
	if exitErr.Code != 2 {
		t.Fatalf("exit code=%d want 2", exitErr.Code)
	}
	if !strings.Contains(exitErr.Msg, "names no known kind") {
		t.Fatalf("msg=%q want to contain 'names no known kind'", exitErr.Msg)
	}
	assertNoStateWrite(t, env, repoRoot, "hc-t2c")
}

// TestInitHcNativeShape_NegativeNumericLifecycle asserts refusal and no-write
// when detailed record reports a numeric state (not string "live").
func TestInitHcNativeShape_NegativeNumericLifecycle(t *testing.T) {
	env, repoRoot := initHcTestFixture(t, func(root string) []fakecli.Rule {
		numericStateGet := fmt.Sprintf(`{"address":"t2c","state":123,"cwd":%q,"status":{"app":"codex","state":"working"},"foreground":{"name":"codex","cwd":%q}}`, root, root)
		return []fakecli.Rule{
			{Argv: []string{"get", "t2c", "--json"}, Call: 1, Stdout: nativeCallerGetJSON(root)},
			{Argv: []string{"get", "t2c", "--json"}, Call: 2, Stdout: numericStateGet},
			{Argv: []string{"ls", "--json"}, Stdout: nativeCallerListJSON(root)},
		}
	})

	_, _, recovered := runCmdInitCapture(t, env, repoRoot)
	exitErr, ok := recovered.(*platform.ExitError)
	if !ok {
		t.Fatalf("expected ExitError, got %#v", recovered)
	}
	if exitErr.Code != 2 {
		t.Fatalf("exit code=%d want 2", exitErr.Code)
	}
	if !strings.Contains(exitErr.Msg, "caller terminal is not live") {
		t.Fatalf("msg=%q want to contain 'caller terminal is not live'", exitErr.Msg)
	}
	assertNoStateWrite(t, env, repoRoot, "hc-t2c")
}

// TestInitHcNativeShape_NegativeMissingState asserts refusal and no-write
// when detailed record completely omits state.
func TestInitHcNativeShape_NegativeMissingState(t *testing.T) {
	env, repoRoot := initHcTestFixture(t, func(root string) []fakecli.Rule {
		missingStateGet := fmt.Sprintf(`{"address":"t2c","cwd":%q,"status":{"app":"codex","state":"working"},"foreground":{"name":"codex","cwd":%q}}`, root, root)
		return []fakecli.Rule{
			{Argv: []string{"get", "t2c", "--json"}, Call: 1, Stdout: nativeCallerGetJSON(root)},
			{Argv: []string{"get", "t2c", "--json"}, Call: 2, Stdout: missingStateGet},
			{Argv: []string{"ls", "--json"}, Stdout: nativeCallerListJSON(root)},
		}
	})

	_, _, recovered := runCmdInitCapture(t, env, repoRoot)
	exitErr, ok := recovered.(*platform.ExitError)
	if !ok {
		t.Fatalf("expected ExitError, got %#v", recovered)
	}
	if exitErr.Code != 2 {
		t.Fatalf("exit code=%d want 2", exitErr.Code)
	}
	if !strings.Contains(exitErr.Msg, "caller terminal is not live") {
		t.Fatalf("msg=%q want to contain 'caller terminal is not live'", exitErr.Msg)
	}
	assertNoStateWrite(t, env, repoRoot, "hc-t2c")
}

// TestInitHcNativeShape_NegativeNullLifecycle asserts refusal and no-write
// when detailed record has state: null.
func TestInitHcNativeShape_NegativeNullLifecycle(t *testing.T) {
	env, repoRoot := initHcTestFixture(t, func(root string) []fakecli.Rule {
		nullStateGet := fmt.Sprintf(`{"address":"t2c","state":null,"cwd":%q,"status":{"app":"codex","state":"working"},"foreground":{"name":"codex","cwd":%q}}`, root, root)
		return []fakecli.Rule{
			{Argv: []string{"get", "t2c", "--json"}, Call: 1, Stdout: nativeCallerGetJSON(root)},
			{Argv: []string{"get", "t2c", "--json"}, Call: 2, Stdout: nullStateGet},
			{Argv: []string{"ls", "--json"}, Stdout: nativeCallerListJSON(root)},
		}
	})

	_, _, recovered := runCmdInitCapture(t, env, repoRoot)
	exitErr, ok := recovered.(*platform.ExitError)
	if !ok {
		t.Fatalf("expected ExitError, got %#v", recovered)
	}
	if exitErr.Code != 2 {
		t.Fatalf("exit code=%d want 2", exitErr.Code)
	}
	if !strings.Contains(exitErr.Msg, "caller terminal is not live") {
		t.Fatalf("msg=%q want to contain 'caller terminal is not live'", exitErr.Msg)
	}
	assertNoStateWrite(t, env, repoRoot, "hc-t2c")
}

// TestInitHcNativeShape_NegativeUnknownLifecycle asserts refusal and no-write
// when detailed record has an unrecognized lifecycle state word.
func TestInitHcNativeShape_NegativeUnknownLifecycle(t *testing.T) {
	env, repoRoot := initHcTestFixture(t, func(root string) []fakecli.Rule {
		unknownStateGet := fmt.Sprintf(`{"address":"t2c","state":"paused","cwd":%q,"status":{"app":"codex","state":"working"},"foreground":{"name":"codex","cwd":%q}}`, root, root)
		return []fakecli.Rule{
			{Argv: []string{"get", "t2c", "--json"}, Call: 1, Stdout: nativeCallerGetJSON(root)},
			{Argv: []string{"get", "t2c", "--json"}, Call: 2, Stdout: unknownStateGet},
			{Argv: []string{"ls", "--json"}, Stdout: nativeCallerListJSON(root)},
		}
	})

	_, _, recovered := runCmdInitCapture(t, env, repoRoot)
	exitErr, ok := recovered.(*platform.ExitError)
	if !ok {
		t.Fatalf("expected ExitError, got %#v", recovered)
	}
	if exitErr.Code != 2 {
		t.Fatalf("exit code=%d want 2", exitErr.Code)
	}
	if !strings.Contains(exitErr.Msg, "caller terminal is not live") {
		t.Fatalf("msg=%q want to contain 'caller terminal is not live'", exitErr.Msg)
	}
	assertNoStateWrite(t, env, repoRoot, "hc-t2c")
}

// TestInitHcNativeShape_NegativeLiveMissingActivity asserts refusal and no-write
// when detailed record is live but lacks status/activity.
func TestInitHcNativeShape_NegativeLiveMissingActivity(t *testing.T) {
	env, repoRoot := initHcTestFixture(t, func(root string) []fakecli.Rule {
		missingActivityGet := fmt.Sprintf(`{"address":"t2c","state":"live","cwd":%q,"foreground":{"name":"codex","cwd":%q}}`, root, root)
		return []fakecli.Rule{
			{Argv: []string{"get", "t2c", "--json"}, Call: 1, Stdout: nativeCallerGetJSON(root)},
			{Argv: []string{"get", "t2c", "--json"}, Call: 2, Stdout: missingActivityGet},
			{Argv: []string{"ls", "--json"}, Stdout: nativeCallerListJSON(root)},
		}
	})

	_, _, recovered := runCmdInitCapture(t, env, repoRoot)
	exitErr, ok := recovered.(*platform.ExitError)
	if !ok {
		t.Fatalf("expected ExitError, got %#v", recovered)
	}
	if exitErr.Code != 2 {
		t.Fatalf("exit code=%d want 2", exitErr.Code)
	}
	if !strings.Contains(exitErr.Msg, "activity is not known") {
		t.Fatalf("msg=%q want to contain 'activity is not known'", exitErr.Msg)
	}
	assertNoStateWrite(t, env, repoRoot, "hc-t2c")
}

// TestInitHcNativeShape_NegativeLiveUnknownActivity asserts refusal and no-write
// when detailed record is live but has an unapproved activity word.
func TestInitHcNativeShape_NegativeLiveUnknownActivity(t *testing.T) {
	env, repoRoot := initHcTestFixture(t, func(root string) []fakecli.Rule {
		unknownActivityGet := fmt.Sprintf(`{"address":"t2c","state":"live","cwd":%q,"status":{"app":"codex","state":"starting"},"foreground":{"name":"codex","cwd":%q}}`, root, root)
		return []fakecli.Rule{
			{Argv: []string{"get", "t2c", "--json"}, Call: 1, Stdout: nativeCallerGetJSON(root)},
			{Argv: []string{"get", "t2c", "--json"}, Call: 2, Stdout: unknownActivityGet},
			{Argv: []string{"ls", "--json"}, Stdout: nativeCallerListJSON(root)},
		}
	})

	_, _, recovered := runCmdInitCapture(t, env, repoRoot)
	exitErr, ok := recovered.(*platform.ExitError)
	if !ok {
		t.Fatalf("expected ExitError, got %#v", recovered)
	}
	if exitErr.Code != 2 {
		t.Fatalf("exit code=%d want 2", exitErr.Code)
	}
	if !strings.Contains(exitErr.Msg, "activity is not known") {
		t.Fatalf("msg=%q want to contain 'activity is not known'", exitErr.Msg)
	}
	assertNoStateWrite(t, env, repoRoot, "hc-t2c")
}

// TestInitHcNativeShape_NegativeAddressOnlyDetail asserts refusal and no-write
// when detailed record is a bare address-only object.
func TestInitHcNativeShape_NegativeAddressOnlyDetail(t *testing.T) {
	env, repoRoot := initHcTestFixture(t, func(root string) []fakecli.Rule {
		return []fakecli.Rule{
			{Argv: []string{"get", "t2c", "--json"}, Call: 1, Stdout: nativeCallerGetJSON(root)},
			{Argv: []string{"get", "t2c", "--json"}, Call: 2, Stdout: `{"address":"t2c"}`},
			{Argv: []string{"ls", "--json"}, Stdout: nativeCallerListJSON(root)},
		}
	})

	_, _, recovered := runCmdInitCapture(t, env, repoRoot)
	exitErr, ok := recovered.(*platform.ExitError)
	if !ok {
		t.Fatalf("expected ExitError, got %#v", recovered)
	}
	if exitErr.Code != 2 {
		t.Fatalf("exit code=%d want 2", exitErr.Code)
	}
	assertNoStateWrite(t, env, repoRoot, "hc-t2c")
}

// TestInitHcNativeShape_NegativeGone asserts refusal and no-write
// when detailed record reports gone/finished terminal.
func TestInitHcNativeShape_NegativeGone(t *testing.T) {
	env, repoRoot := initHcTestFixture(t, func(root string) []fakecli.Rule {
		return []fakecli.Rule{
			{Argv: []string{"get", "t2c", "--json"}, Call: 1, Stdout: nativeCallerGetJSON(root)},
			{Argv: []string{"get", "t2c", "--json"}, Call: 2, Stdout: fmt.Sprintf(`{"address":"t2c","state":"finished","cwd":%q}`, root)},
			{Argv: []string{"ls", "--json"}, Stdout: nativeCallerListJSON(root)},
		}
	})

	_, _, recovered := runCmdInitCapture(t, env, repoRoot)
	exitErr, ok := recovered.(*platform.ExitError)
	if !ok {
		t.Fatalf("expected ExitError, got %#v", recovered)
	}
	if exitErr.Code != 2 {
		t.Fatalf("exit code=%d want 2", exitErr.Code)
	}
	assertNoStateWrite(t, env, repoRoot, "hc-t2c")
}

// TestInitHcNativeShape_NegativeDetailMalformed asserts refusal and no-write
// when detailed record returns malformed JSON or error.
func TestInitHcNativeShape_NegativeDetailMalformed(t *testing.T) {
	env, repoRoot := initHcTestFixture(t, func(root string) []fakecli.Rule {
		return []fakecli.Rule{
			{Argv: []string{"get", "t2c", "--json"}, Call: 1, Stdout: nativeCallerGetJSON(root)},
			{Argv: []string{"get", "t2c", "--json"}, Call: 2, Stdout: "bad-json"},
			{Argv: []string{"ls", "--json"}, Stdout: nativeCallerListJSON(root)},
		}
	})

	_, _, recovered := runCmdInitCapture(t, env, repoRoot)
	exitErr, ok := recovered.(*platform.ExitError)
	if !ok {
		t.Fatalf("expected ExitError, got %#v", recovered)
	}
	if exitErr.Code != 2 {
		t.Fatalf("exit code=%d want 2", exitErr.Code)
	}
	assertNoStateWrite(t, env, repoRoot, "hc-t2c")
}

// TestInitHcNativeShape_KnownKindUnnamed_Success verifies that when caller summary
// has recognized program hints but no name, detailed lookup verifies the terminal
// and registers with address fallback display name.
func TestInitHcNativeShape_KnownKindUnnamed_Success(t *testing.T) {
	env, repoRoot := initHcTestFixture(t, func(root string) []fakecli.Rule {
		return []fakecli.Rule{
			{Argv: []string{"get", "t2c", "--json"}, Stdout: nativeCallerGetJSON(root)},
			{Argv: []string{"ls", "--json"}, Stdout: knownKindUnnamedCallerListJSON(root)},
		}
	})

	stdout, stderr, recovered := runCmdInitCapture(t, env, repoRoot)
	if recovered != nil {
		t.Fatalf("CmdInit died unexpectedly: %v\nstderr=%s", recovered, stderr)
	}

	v, err := jsonjs.Parse([]byte(stdout))
	if err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout=%s", err, stdout)
	}
	obj, ok := v.(*jsonjs.Object)
	if !ok {
		t.Fatalf("stdout JSON is not an object: %s", stdout)
	}
	if orch, ok := obj.Get("orchestrator"); !ok || orch != "t2c" {
		t.Fatalf("orchestrator=%v want t2c", orch)
	}
	if ws, ok := obj.Get("workspace_id"); !ok || ws != "hc-t2c" {
		t.Fatalf("workspace_id=%v want hc-t2c", ws)
	}

	stateBase := core.StateRootPathForProject(&core.Config{Entries: map[string]core.ConfigEntry{}}, env, repoRoot)
	store := sessionmeta.Store{StateBase: stateBase, SessionID: "hc-t2c"}
	rec, ok, err := store.Read()
	if err != nil || !ok {
		t.Fatalf("store.Read err=%v ok=%v", err, ok)
	}
	if rec.Kind != "codex" {
		t.Fatalf("registered kind=%q want codex", rec.Kind)
	}
	if rec.Name != "t2c" {
		t.Fatalf("registered name=%q want t2c", rec.Name)
	}
}

// TestInitHcNativeShape_KnownKindUnnamed_NegativeSessionMismatch asserts refusal and no-write
// when caller summary has known kind but no name, and detailed lookup reports a different session.
func TestInitHcNativeShape_KnownKindUnnamed_NegativeSessionMismatch(t *testing.T) {
	env, repoRoot := initHcTestFixture(t, func(root string) []fakecli.Rule {
		sessionMismatchGet := fmt.Sprintf(`{"address":"t2c","name":null,"state":"live","labels":{"soho.session":"other-session"},"cwd":%q,"status":{"app":"codex","state":"working"},"foreground":{"name":"codex","cwd":%q}}`, root, root)
		return []fakecli.Rule{
			{Argv: []string{"get", "t2c", "--json"}, Call: 1, Stdout: nativeCallerGetJSON(root)},
			{Argv: []string{"get", "t2c", "--json"}, Call: 2, Stdout: sessionMismatchGet},
			{Argv: []string{"ls", "--json"}, Stdout: knownKindUnnamedCallerListJSON(root)},
		}
	})

	_, _, recovered := runCmdInitCapture(t, env, repoRoot)
	exitErr, ok := recovered.(*platform.ExitError)
	if !ok {
		t.Fatalf("expected ExitError, got %#v", recovered)
	}
	if exitErr.Code != 2 {
		t.Fatalf("exit code=%d want 2", exitErr.Code)
	}
	if !strings.Contains(exitErr.Msg, "detailed caller session does not match") {
		t.Fatalf("msg=%q want to contain 'detailed caller session does not match'", exitErr.Msg)
	}
	calls, err := fakecli.ReadCalls(filepath.Join(repoRoot, "bin", "hc.calls.jsonl"))
	if err != nil {
		t.Fatalf("ReadCalls error: %v", err)
	}
	var getCalls int
	for _, c := range calls {
		if len(c.Argv) >= 2 && c.Argv[0] == "get" && c.Argv[1] == "t2c" {
			getCalls++
		}
	}
	if getCalls < 2 {
		t.Fatalf("expected at least 2 get calls (initial Caller lookup and detailed lookup), got %d (calls: %#v)", getCalls, calls)
	}
	assertNoStateWrite(t, env, repoRoot, "hc-t2c")
	assertNoStateWrite(t, env, repoRoot, "other-session")
}

// TestInitHcNativeShape_KnownKindUnnamed_NegativeAddressMismatch asserts refusal and no-write
// when caller summary has known kind but no name, and detailed lookup reports a different address.
func TestInitHcNativeShape_KnownKindUnnamed_NegativeAddressMismatch(t *testing.T) {
	env, repoRoot := initHcTestFixture(t, func(root string) []fakecli.Rule {
		mismatchGet := fmt.Sprintf(`{"address":"t99","name":null,"state":"live","cwd":%q,"status":{"app":"codex","state":"working"},"foreground":{"name":"codex","cwd":%q}}`, root, root)
		return []fakecli.Rule{
			{Argv: []string{"get", "t2c", "--json"}, Call: 1, Stdout: nativeCallerGetJSON(root)},
			{Argv: []string{"get", "t2c", "--json"}, Call: 2, Stdout: mismatchGet},
			{Argv: []string{"ls", "--json"}, Stdout: knownKindUnnamedCallerListJSON(root)},
		}
	})

	_, _, recovered := runCmdInitCapture(t, env, repoRoot)
	exitErr, ok := recovered.(*platform.ExitError)
	if !ok {
		t.Fatalf("expected ExitError, got %#v", recovered)
	}
	if exitErr.Code != 2 {
		t.Fatalf("exit code=%d want 2", exitErr.Code)
	}
	if !strings.Contains(exitErr.Msg, "detailed caller address does not match") {
		t.Fatalf("msg=%q want to contain 'detailed caller address does not match'", exitErr.Msg)
	}
	assertNoStateWrite(t, env, repoRoot, "hc-t2c")
}

// TestInitHcNativeShape_KnownKindUnnamed_NegativeRootMismatch asserts refusal and no-write
// when caller summary has known kind but no name, and detailed lookup reports a cwd outside project root.
func TestInitHcNativeShape_KnownKindUnnamed_NegativeRootMismatch(t *testing.T) {
	otherDir := t.TempDir()
	env, repoRoot := initHcTestFixture(t, func(root string) []fakecli.Rule {
		rootMismatchGet := fmt.Sprintf(`{"address":"t2c","name":null,"state":"live","cwd":%q,"status":{"app":"codex","state":"working"},"foreground":{"name":"codex","cwd":%q}}`, otherDir, otherDir)
		return []fakecli.Rule{
			{Argv: []string{"get", "t2c", "--json"}, Call: 1, Stdout: nativeCallerGetJSON(root)},
			{Argv: []string{"get", "t2c", "--json"}, Call: 2, Stdout: rootMismatchGet},
			{Argv: []string{"ls", "--json"}, Stdout: knownKindUnnamedCallerListJSON(root)},
		}
	})

	_, _, recovered := runCmdInitCapture(t, env, repoRoot)
	exitErr, ok := recovered.(*platform.ExitError)
	if !ok {
		t.Fatalf("expected ExitError, got %#v", recovered)
	}
	if exitErr.Code != 2 {
		t.Fatalf("exit code=%d want 2", exitErr.Code)
	}
	if !strings.Contains(exitErr.Msg, "detailed caller root does not match") {
		t.Fatalf("msg=%q want to contain 'detailed caller root does not match'", exitErr.Msg)
	}
	assertNoStateWrite(t, env, repoRoot, "hc-t2c")
}

// TestInitHcNativeShape_KnownKindUnnamed_NegativeUnknownKind asserts refusal and no-write
// when caller summary has known kind but no name, and detailed lookup reports an unknown program kind.
func TestInitHcNativeShape_KnownKindUnnamed_NegativeUnknownKind(t *testing.T) {
	env, repoRoot := initHcTestFixture(t, func(root string) []fakecli.Rule {
		unknownKindGet := fmt.Sprintf(`{"address":"t2c","name":null,"state":"live","cwd":%q,"status":{"app":"custom-shell","state":"working"},"foreground":{"name":"custom-shell","cwd":%q}}`, root, root)
		return []fakecli.Rule{
			{Argv: []string{"get", "t2c", "--json"}, Call: 1, Stdout: nativeCallerGetJSON(root)},
			{Argv: []string{"get", "t2c", "--json"}, Call: 2, Stdout: unknownKindGet},
			{Argv: []string{"ls", "--json"}, Stdout: knownKindUnnamedCallerListJSON(root)},
		}
	})

	_, _, recovered := runCmdInitCapture(t, env, repoRoot)
	exitErr, ok := recovered.(*platform.ExitError)
	if !ok {
		t.Fatalf("expected ExitError, got %#v", recovered)
	}
	if exitErr.Code != 2 {
		t.Fatalf("exit code=%d want 2", exitErr.Code)
	}
	if !strings.Contains(exitErr.Msg, "names no known kind") {
		t.Fatalf("msg=%q want to contain 'names no known kind'", exitErr.Msg)
	}
	assertNoStateWrite(t, env, repoRoot, "hc-t2c")
}

// TestInitHcNativeShape_KnownKindUnnamed_NegativeDetailMalformed asserts refusal and no-write
// when caller summary has known kind but no name, and detailed lookup returns malformed JSON or read error.
func TestInitHcNativeShape_KnownKindUnnamed_NegativeDetailMalformed(t *testing.T) {
	env, repoRoot := initHcTestFixture(t, func(root string) []fakecli.Rule {
		return []fakecli.Rule{
			{Argv: []string{"get", "t2c", "--json"}, Call: 1, Stdout: nativeCallerGetJSON(root)},
			{Argv: []string{"get", "t2c", "--json"}, Call: 2, Stdout: "bad-json"},
			{Argv: []string{"ls", "--json"}, Stdout: knownKindUnnamedCallerListJSON(root)},
		}
	})

	_, _, recovered := runCmdInitCapture(t, env, repoRoot)
	exitErr, ok := recovered.(*platform.ExitError)
	if !ok {
		t.Fatalf("expected ExitError, got %#v", recovered)
	}
	if exitErr.Code != 2 {
		t.Fatalf("exit code=%d want 2", exitErr.Code)
	}
	assertNoStateWrite(t, env, repoRoot, "hc-t2c")
}

// TestInitHcNativeShape_KnownKindUnnamed_NegativeNumericLifecycle asserts refusal and no-write
// when caller summary has known kind and empty name, and detailed record has a numeric lifecycle.
func TestInitHcNativeShape_KnownKindUnnamed_NegativeNumericLifecycle(t *testing.T) {
	env, repoRoot := initHcTestFixture(t, func(root string) []fakecli.Rule {
		numericStateGet := fmt.Sprintf(`{"address":"t2c","state":123,"cwd":%q,"status":{"app":"codex","state":"working"},"foreground":{"name":"codex","cwd":%q}}`, root, root)
		return []fakecli.Rule{
			{Argv: []string{"get", "t2c", "--json"}, Call: 1, Stdout: nativeCallerGetJSON(root)},
			{Argv: []string{"get", "t2c", "--json"}, Call: 2, Stdout: numericStateGet},
			{Argv: []string{"ls", "--json"}, Stdout: knownKindUnnamedCallerListJSON(root)},
		}
	})

	_, _, recovered := runCmdInitCapture(t, env, repoRoot)
	exitErr, ok := recovered.(*platform.ExitError)
	if !ok {
		t.Fatalf("expected ExitError, got %#v", recovered)
	}
	if exitErr.Code != 2 {
		t.Fatalf("exit code=%d want 2", exitErr.Code)
	}
	if !strings.Contains(exitErr.Msg, "caller terminal is not live") {
		t.Fatalf("msg=%q want to contain 'caller terminal is not live'", exitErr.Msg)
	}
	assertNoStateWrite(t, env, repoRoot, "hc-t2c")
}

// TestInitHcNativeShape_KnownKindUnnamed_NegativeMissingState asserts refusal and no-write
// when caller summary has known kind and empty name, and detailed record omits state.
func TestInitHcNativeShape_KnownKindUnnamed_NegativeMissingState(t *testing.T) {
	env, repoRoot := initHcTestFixture(t, func(root string) []fakecli.Rule {
		missingStateGet := fmt.Sprintf(`{"address":"t2c","cwd":%q,"status":{"app":"codex","state":"working"},"foreground":{"name":"codex","cwd":%q}}`, root, root)
		return []fakecli.Rule{
			{Argv: []string{"get", "t2c", "--json"}, Call: 1, Stdout: nativeCallerGetJSON(root)},
			{Argv: []string{"get", "t2c", "--json"}, Call: 2, Stdout: missingStateGet},
			{Argv: []string{"ls", "--json"}, Stdout: knownKindUnnamedCallerListJSON(root)},
		}
	})

	_, _, recovered := runCmdInitCapture(t, env, repoRoot)
	exitErr, ok := recovered.(*platform.ExitError)
	if !ok {
		t.Fatalf("expected ExitError, got %#v", recovered)
	}
	if exitErr.Code != 2 {
		t.Fatalf("exit code=%d want 2", exitErr.Code)
	}
	if !strings.Contains(exitErr.Msg, "caller terminal is not live") {
		t.Fatalf("msg=%q want to contain 'caller terminal is not live'", exitErr.Msg)
	}
	assertNoStateWrite(t, env, repoRoot, "hc-t2c")
}

// TestInitHcNativeShape_KnownKindUnnamed_NegativeNullLifecycle asserts refusal and no-write
// when caller summary has known kind and empty name, and detailed record has state: null.
func TestInitHcNativeShape_KnownKindUnnamed_NegativeNullLifecycle(t *testing.T) {
	env, repoRoot := initHcTestFixture(t, func(root string) []fakecli.Rule {
		nullStateGet := fmt.Sprintf(`{"address":"t2c","state":null,"cwd":%q,"status":{"app":"codex","state":"working"},"foreground":{"name":"codex","cwd":%q}}`, root, root)
		return []fakecli.Rule{
			{Argv: []string{"get", "t2c", "--json"}, Call: 1, Stdout: nativeCallerGetJSON(root)},
			{Argv: []string{"get", "t2c", "--json"}, Call: 2, Stdout: nullStateGet},
			{Argv: []string{"ls", "--json"}, Stdout: knownKindUnnamedCallerListJSON(root)},
		}
	})

	_, _, recovered := runCmdInitCapture(t, env, repoRoot)
	exitErr, ok := recovered.(*platform.ExitError)
	if !ok {
		t.Fatalf("expected ExitError, got %#v", recovered)
	}
	if exitErr.Code != 2 {
		t.Fatalf("exit code=%d want 2", exitErr.Code)
	}
	if !strings.Contains(exitErr.Msg, "caller terminal is not live") {
		t.Fatalf("msg=%q want to contain 'caller terminal is not live'", exitErr.Msg)
	}
	assertNoStateWrite(t, env, repoRoot, "hc-t2c")
}

// TestInitHcNativeShape_KnownKindUnnamed_NegativeUnknownLifecycle asserts refusal and no-write
// when caller summary has known kind and empty name, and detailed record has an unknown lifecycle word.
func TestInitHcNativeShape_KnownKindUnnamed_NegativeUnknownLifecycle(t *testing.T) {
	env, repoRoot := initHcTestFixture(t, func(root string) []fakecli.Rule {
		unknownStateGet := fmt.Sprintf(`{"address":"t2c","state":"paused","cwd":%q,"status":{"app":"codex","state":"working"},"foreground":{"name":"codex","cwd":%q}}`, root, root)
		return []fakecli.Rule{
			{Argv: []string{"get", "t2c", "--json"}, Call: 1, Stdout: nativeCallerGetJSON(root)},
			{Argv: []string{"get", "t2c", "--json"}, Call: 2, Stdout: unknownStateGet},
			{Argv: []string{"ls", "--json"}, Stdout: knownKindUnnamedCallerListJSON(root)},
		}
	})

	_, _, recovered := runCmdInitCapture(t, env, repoRoot)
	exitErr, ok := recovered.(*platform.ExitError)
	if !ok {
		t.Fatalf("expected ExitError, got %#v", recovered)
	}
	if exitErr.Code != 2 {
		t.Fatalf("exit code=%d want 2", exitErr.Code)
	}
	if !strings.Contains(exitErr.Msg, "caller terminal is not live") {
		t.Fatalf("msg=%q want to contain 'caller terminal is not live'", exitErr.Msg)
	}
	assertNoStateWrite(t, env, repoRoot, "hc-t2c")
}

// TestInitHcNativeShape_KnownKindUnnamed_NegativeLiveMissingActivity asserts refusal and no-write
// when caller summary has known kind and empty name, and detailed record is live but lacks status/activity.
func TestInitHcNativeShape_KnownKindUnnamed_NegativeLiveMissingActivity(t *testing.T) {
	env, repoRoot := initHcTestFixture(t, func(root string) []fakecli.Rule {
		missingActivityGet := fmt.Sprintf(`{"address":"t2c","state":"live","cwd":%q,"foreground":{"name":"codex","cwd":%q}}`, root, root)
		return []fakecli.Rule{
			{Argv: []string{"get", "t2c", "--json"}, Call: 1, Stdout: nativeCallerGetJSON(root)},
			{Argv: []string{"get", "t2c", "--json"}, Call: 2, Stdout: missingActivityGet},
			{Argv: []string{"ls", "--json"}, Stdout: knownKindUnnamedCallerListJSON(root)},
		}
	})

	_, _, recovered := runCmdInitCapture(t, env, repoRoot)
	exitErr, ok := recovered.(*platform.ExitError)
	if !ok {
		t.Fatalf("expected ExitError, got %#v", recovered)
	}
	if exitErr.Code != 2 {
		t.Fatalf("exit code=%d want 2", exitErr.Code)
	}
	if !strings.Contains(exitErr.Msg, "activity is not known") {
		t.Fatalf("msg=%q want to contain 'activity is not known'", exitErr.Msg)
	}
	assertNoStateWrite(t, env, repoRoot, "hc-t2c")
}

// TestInitHcNativeShape_KnownKindUnnamed_NegativeLiveUnknownActivity asserts refusal and no-write
// when caller summary has known kind and empty name, and detailed record has an unapproved activity word.
func TestInitHcNativeShape_KnownKindUnnamed_NegativeLiveUnknownActivity(t *testing.T) {
	env, repoRoot := initHcTestFixture(t, func(root string) []fakecli.Rule {
		unknownActivityGet := fmt.Sprintf(`{"address":"t2c","state":"live","cwd":%q,"status":{"app":"codex","state":"starting"},"foreground":{"name":"codex","cwd":%q}}`, root, root)
		return []fakecli.Rule{
			{Argv: []string{"get", "t2c", "--json"}, Call: 1, Stdout: nativeCallerGetJSON(root)},
			{Argv: []string{"get", "t2c", "--json"}, Call: 2, Stdout: unknownActivityGet},
			{Argv: []string{"ls", "--json"}, Stdout: knownKindUnnamedCallerListJSON(root)},
		}
	})

	_, _, recovered := runCmdInitCapture(t, env, repoRoot)
	exitErr, ok := recovered.(*platform.ExitError)
	if !ok {
		t.Fatalf("expected ExitError, got %#v", recovered)
	}
	if exitErr.Code != 2 {
		t.Fatalf("exit code=%d want 2", exitErr.Code)
	}
	if !strings.Contains(exitErr.Msg, "activity is not known") {
		t.Fatalf("msg=%q want to contain 'activity is not known'", exitErr.Msg)
	}
	assertNoStateWrite(t, env, repoRoot, "hc-t2c")
}

// TestInitHcNativeShape_KnownKindUnnamed_NegativeAddressOnlyDetail asserts refusal and no-write
// when caller summary has known kind and empty name, and detailed record is a bare address-only object.
func TestInitHcNativeShape_KnownKindUnnamed_NegativeAddressOnlyDetail(t *testing.T) {
	env, repoRoot := initHcTestFixture(t, func(root string) []fakecli.Rule {
		return []fakecli.Rule{
			{Argv: []string{"get", "t2c", "--json"}, Call: 1, Stdout: nativeCallerGetJSON(root)},
			{Argv: []string{"get", "t2c", "--json"}, Call: 2, Stdout: `{"address":"t2c"}`},
			{Argv: []string{"ls", "--json"}, Stdout: knownKindUnnamedCallerListJSON(root)},
		}
	})

	_, _, recovered := runCmdInitCapture(t, env, repoRoot)
	exitErr, ok := recovered.(*platform.ExitError)
	if !ok {
		t.Fatalf("expected ExitError, got %#v", recovered)
	}
	if exitErr.Code != 2 {
		t.Fatalf("exit code=%d want 2", exitErr.Code)
	}
	assertNoStateWrite(t, env, repoRoot, "hc-t2c")
}

// TestInitHcNativeShape_BackwardsCompatibleNamedCaller verifies that
// when the caller record carries an explicit name, that name is preserved.
func TestInitHcNativeShape_BackwardsCompatibleNamedCaller(t *testing.T) {
	env, repoRoot := initHcTestFixture(t, func(root string) []fakecli.Rule {
		namedGet := fmt.Sprintf(`{
  "address": "t2c",
  "name": "my-orch",
  "state": "live",
  "pid": 54449,
  "cwd": %q,
  "status": {
    "app": "codex",
    "state": "working"
  },
  "foreground": {
    "name": "codex",
    "pid": 54449,
    "cwd": %q
  }
}`, root, root)
		namedList := fmt.Sprintf(`{"terminals":[{
  "address": "t2c",
  "name": "my-orch",
  "state": "live",
  "status": {"state": "working", "app": "codex"},
  "cwd": %q
}]}`, root)
		return []fakecli.Rule{
			{Argv: []string{"get", "t2c", "--json"}, Stdout: namedGet},
			{Argv: []string{"ls", "--json"}, Stdout: namedList},
		}
	})

	stdout, stderr, recovered := runCmdInitCapture(t, env, repoRoot)
	if recovered != nil {
		t.Fatalf("CmdInit died: %v\nstderr=%s", recovered, stderr)
	}

	v, err := jsonjs.Parse([]byte(stdout))
	if err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout=%s", err, stdout)
	}
	obj := v.(*jsonjs.Object)
	if orch, ok := obj.Get("orchestrator"); !ok || orch != "my-orch" {
		t.Fatalf("orchestrator=%v want my-orch", orch)
	}

	stateBase := core.StateRootPathForProject(&core.Config{Entries: map[string]core.ConfigEntry{}}, env, repoRoot)
	store := sessionmeta.Store{StateBase: stateBase, SessionID: "hc-t2c"}
	rec, ok, err := store.Read()
	if err != nil || !ok {
		t.Fatalf("store.Read err=%v ok=%v", err, ok)
	}
	if rec.Name != "my-orch" {
		t.Fatalf("registered name=%q want my-orch", rec.Name)
	}
	if rec.Kind != "codex" {
		t.Fatalf("registered kind=%q want codex", rec.Kind)
	}
}

// TestInitHcNativeShape_NamedCaller_NegativeFreshLookupFailed verifies that
// when the summary has a recognized kind and non-empty name, but the fresh
// CallerAgentName lookup fails or disappears, init refuses with exit 2 and
// writes no state.
func TestInitHcNativeShape_NamedCaller_NegativeFreshLookupFailed(t *testing.T) {
	env, repoRoot := initHcTestFixture(t, func(root string) []fakecli.Rule {
		namedGet := fmt.Sprintf(`{
  "address": "t2c",
  "name": "my-orch",
  "state": "live",
  "cwd": %q,
  "status": {"app": "codex"}
}`, root)
		namedList := fmt.Sprintf(`{"terminals":[{
  "address": "t2c",
  "name": "my-orch",
  "state": "live",
  "status": {"app": "codex"},
  "cwd": %q
}]}`, root)
		return []fakecli.Rule{
			{Argv: []string{"get", "t2c", "--json"}, Call: 1, Stdout: namedGet},
			{Argv: []string{"get", "t2c", "--json"}, Call: 2, Code: 1, Stderr: "terminal closed"},
			{Argv: []string{"ls", "--json"}, Stdout: namedList},
		}
	})

	_, _, recovered := runCmdInitCapture(t, env, repoRoot)
	exitErr, ok := recovered.(*platform.ExitError)
	if !ok {
		t.Fatalf("expected ExitError, got %#v", recovered)
	}
	if exitErr.Code != 2 {
		t.Fatalf("exit code=%d want 2", exitErr.Code)
	}
	if !strings.Contains(exitErr.Msg, "the caller terminal's name could not be read") {
		t.Fatalf("msg=%q want to contain 'the caller terminal's name could not be read'", exitErr.Msg)
	}
	assertNoStateWrite(t, env, repoRoot, "hc-t2c")
}
