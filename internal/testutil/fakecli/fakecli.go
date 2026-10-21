// Package fakecli provides re-executed Go test binaries as deterministic CLI
// fakes. Call Install from a test and Main first in package TestMain.
package fakecli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const configEnv = "SOHO_FAKECLI_CONFIG"

type Rule struct {
	Argv        []string `json:"argv"`
	ArgvPrefix  bool     `json:"argv_prefix,omitempty"`
	AnyArgs     bool     `json:"any_args,omitempty"`
	Call        int      `json:"call,omitempty"`
	Stdout      string   `json:"stdout,omitempty"`
	StdoutBytes []byte   `json:"stdout_bytes,omitempty"`
	Stderr      string   `json:"stderr,omitempty"`
	StderrBytes []byte   `json:"stderr_bytes,omitempty"`
	Code        int      `json:"code,omitempty"`
	Delay       int      `json:"delay_ms,omitempty"`
	WaitFile    string   `json:"wait_file,omitempty"`
	Signal      string   `json:"signal,omitempty"`
}

// Call is one recorded run of the fake. Cwd is the working directory the
// fake ran in, empty when it could not be read.
type Call struct {
	Argv  []string          `json:"argv"`
	Stdin string            `json:"stdin,omitempty"`
	Env   map[string]string `json:"env,omitempty"`
	Cwd   string            `json:"cwd,omitempty"`
}

type script struct {
	Log          string   `json:"log"`
	Rules        []Rule   `json:"rules"`
	CaptureStdin bool     `json:"capture_stdin,omitempty"`
	CaptureEnv   []string `json:"capture_env,omitempty"`
}

type InstallOptions struct {
	CaptureStdin bool
	CaptureEnv   []string
}

type EnvOptions struct {
	IncludeBasePath bool
	SystemPath      string
}

var preparedBinary string
var prepareOnce sync.Once
var prepareErr error

// waitFileTimeoutEnv carries a shorter cap than the 60 s default into the
// re-executed fake, so tests do not wait for the timeout. The variable would
// not cross the re-execution, hence the environment.
const waitFileTimeoutEnv = "FAKECLI_WAIT_FILE_TIMEOUT_MS"

const waitFileDefaultTimeout = 60 * time.Second

// waitFileTimeout returns the WaitFile cap: the injected value in
// milliseconds when valid, else the 60 s default.
func waitFileTimeout() time.Duration {
	if raw := os.Getenv(waitFileTimeoutEnv); raw != "" {
		if ms, err := strconv.Atoi(raw); err == nil && ms > 0 {
			return time.Duration(ms) * time.Millisecond
		}
	}
	return waitFileDefaultTimeout
}

// waitForFile polls every 20 ms until the file exists or the timeout passes.
func waitForFile(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("wait_file %s never appeared", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// RunTests handles fake CLI re-execution, runs the package tests, and cleans up
// a lazily prepared Windows binary after the package finishes.
func RunTests(m *testing.M, after ...func()) {
	Main()
	GuardSuiteRun()
	code := m.Run()
	if preparedBinary != "" {
		_ = os.RemoveAll(filepath.Dir(preparedBinary))
	}
	for _, callback := range after {
		callback()
	}
	os.Exit(code)
}

// suiteRunRefusal reports whether this process must refuse to start the test
// suite: it is a fakecli re-execution called without SOHO_FAKECLI_CONFIG.
// The suite still starts for a normal test invocation, which always carries
// a -test. flag (go test passes several), and for a hand-run test binary
// whose name ends in .test (a go test -c binary), in any case and with the
// .exe suffix stripped.
func suiteRunRefusal(config string, args []string) (string, bool) {
	if config != "" {
		return "", false
	}
	if len(args) == 0 {
		return "", true
	}
	for _, arg := range args[1:] {
		if strings.HasPrefix(arg, "-test.") {
			return "", false
		}
	}
	name := filepath.Base(args[0])
	base := strings.TrimSuffix(strings.ToLower(name), ".exe")
	if strings.HasSuffix(base, ".test") {
		return "", false
	}
	return name, true
}

// GuardSuiteRun refuses to start the test suite when this process is a
// re-executed fake called without SOHO_FAKECLI_CONFIG. Without the guard the
// fake would run the whole package suite, and the suite's own fake calls
// (through a PATH that still points at the fakes) would re-execute the fake
// again and again, chaining (a single setup test run chained into 4.565
// stuck git processes). Call it at the point where the suite would start,
// after every TestMain helper dispatch, so the helpers keep dispatching
// before the guard.
func GuardSuiteRun() {
	if name, refused := suiteRunRefusal(os.Getenv(configEnv), os.Args); refused {
		fmt.Fprintf(os.Stderr, "fakecli: %s ran without SOHO_FAKECLI_CONFIG; refusing to run the test suite as a fake command\n", name)
		os.Exit(126)
	}
}

// warmPrepared pays the cold first-execution cost of the prepared Windows
// image: the cold first execution is paid once per process that prepares
// the image, during the first Install (fixture setup, outside any
// production-call deadline), never by re-executed children that do not
// install fakes (they never prepare an image). A fresh image's first exec
// can take longer than a production CLI deadline, and the package's first
// fake call must not pay it. The no-op child is the prepared image
// itself and dispatches through Main's existing routing via configEnv in
// file form. On any failure it
// returns the error so the caller reports one line and continues: the
// warm-up is setup, not a test assertion, and the prepared copy stays
// valid (a real problem still surfaces when a fixture executes it). On
// non-Windows the fake is a symlink to the already-running test binary, so
// there is nothing to warm.
func warmPrepared(prepared string) (err error) {
	if runtime.GOOS != "windows" {
		return nil
	}
	dir, err := os.MkdirTemp("", "herdr-fakecli-warm-")
	if err != nil {
		return err
	}
	// The child runs the prepared image itself (its config is named by
	// path, not by an executable name in this directory), so the directory
	// only ever holds the script and the call log, both closed once the
	// child has been waited for: nothing in it can still be held by the
	// child's image when it is removed, and a removal failure is returned
	// (joined with the run's own error), never left behind silently.
	defer func() {
		if rerr := os.RemoveAll(dir); rerr != nil {
			err = errors.Join(err, fmt.Errorf("remove the warm directory: %w", rerr))
		}
	}()
	config := filepath.Join(dir, "fakecli-warm.json")
	scriptJSON, err := json.Marshal(script{Log: filepath.Join(dir, "fakecli-warm.calls.jsonl"), Rules: []Rule{{Argv: []string{"__fakecli_warm__"}}}})
	if err != nil {
		return err
	}
	if err := os.WriteFile(config, scriptJSON, 0o600); err != nil {
		return err
	}
	// The child must dispatch the warm script, not an inherited fixture
	// config, and must not pay the race exit pause itself.
	env := os.Environ()
	for i, entry := range env {
		if key, _, ok := strings.Cut(entry, "="); ok && strings.EqualFold(key, configEnv) {
			env = append(env[:i:i], env[i+1:]...)
			break
		}
	}
	env = append(env, configEnv+"="+config, "GORACE="+RaceOptions(os.Environ()))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, prepared, "__fakecli_warm__")
	cmd.Env = env
	// CombinedOutput waits for the child, so it has exited and been
	// reaped before the directory is removed.
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("warm run timed out after 60s: %s", out)
	}
	if err != nil {
		return fmt.Errorf("warm run: %v: %s", err, out)
	}
	return nil
}

func prepareBinary() error {
	prepareOnce.Do(func() {
		bin, err := os.Executable()
		if err != nil {
			prepareErr = err
			return
		}
		dir, err := os.MkdirTemp("", "herdr-fakecli-")
		if err != nil {
			prepareErr = err
			return
		}
		target := filepath.Join(dir, "fakecli.exe")
		data, err := os.ReadFile(bin)
		if err != nil {
			_ = os.RemoveAll(dir)
			prepareErr = err
			return
		}
		if err = os.WriteFile(target, data, 0o700); err != nil {
			_ = os.RemoveAll(dir)
			prepareErr = err
			return
		}
		preparedBinary = target
		// Warm the image now, in the process that prepared it (the first
		// Install): re-executed children that go through RunTests but never
		// install a fake must not pay this cold start. A warm failure only
		// reports one line; prepareErr stays nil because the prepared copy
		// is still valid.
		if err := warmPrepared(target); err != nil {
			fmt.Fprintf(os.Stderr, "fakecli: warming the prepared image failed: %v\n", err)
		}
	})
	return prepareErr
}

// Install writes a JSON script and places a re-executable entry for the test
// binary in dir. Tests should prepend dir to PATH. On Windows the entry is a
// hard link to the package's prepared executable; on Unix it is a symlink
// where supported. Windows preparation happens lazily when the package uses
// Install, so each test package does not need its own setup code.
func Install(t testing.TB, dir, name string, rules []Rule) (string, error) {
	return InstallWithOptions(t, dir, name, rules, InstallOptions{})
}

func InstallWithOptions(t testing.TB, dir, name string, rules []Rule, options InstallOptions) (string, error) {
	t.Helper()
	if runtime.GOOS == "windows" {
		if err := prepareBinary(); err != nil {
			return "", fmt.Errorf("fakecli: prepare test binary: %w", err)
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	log := filepath.Join(dir, name+".calls.jsonl")
	config := filepath.Join(dir, name+".json")
	b, err := json.Marshal(script{Log: log, Rules: rules, CaptureStdin: options.CaptureStdin, CaptureEnv: options.CaptureEnv})
	if err != nil {
		return "", err
	}
	if err = os.WriteFile(config, b, 0o600); err != nil {
		return "", err
	}
	bin, err := os.Executable()
	if err != nil {
		return "", err
	}
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	target := filepath.Join(dir, name+ext)
	if _, statErr := os.Lstat(target); statErr == nil {
		return "", fmt.Errorf("fakecli: %s already exists", target)
	}
	if runtime.GOOS == "windows" {
		err = os.Link(preparedBinary, target)
	} else {
		err = os.Symlink(bin, target)
		if err != nil {
			data, readErr := os.ReadFile(bin)
			if readErr != nil {
				return "", readErr
			}
			err = os.WriteFile(target, data, 0o755)
		}
	}
	if err != nil {
		return "", err
	}
	return target, nil
}

// RaceOptions returns the GORACE value a re-executed fake or fixture child
// should run with: the detector options found in base's GORACE entries
// (any key case), without any atexit_sleep_ms option, followed by
// atexit_sleep_ms=0. Race-instrumented test binaries otherwise pause for
// one second on exit; every other detector option is kept.
func RaceOptions(base []string) string {
	raceOptions := []string{}
	for _, v := range base {
		key, value, ok := strings.Cut(v, "=")
		if !ok || strings.ToUpper(key) != "GORACE" {
			continue
		}
		for _, option := range strings.Fields(value) {
			if !strings.HasPrefix(option, "atexit_sleep_ms=") {
				raceOptions = append(raceOptions, option)
			}
		}
	}
	return strings.Join(append(raceOptions, "atexit_sleep_ms=0"), " ")
}

// Env returns a copy of base with the fake enabled and its directory first on PATH.
func Env(base []string, dir string, options ...EnvOptions) []string {
	option := EnvOptions{}
	if len(options) > 0 {
		option = options[0]
	}
	out := make([]string, 0, len(base)+3)
	inherited := sameEnvironment(base, os.Environ())
	for _, v := range base {
		key, _, ok := strings.Cut(v, "=")
		if ok {
			upperKey := strings.ToUpper(key)
			if upperKey == "GORACE" || upperKey == "PATH" || strings.EqualFold(key, configEnv) || upperKey == "HERDR_SOCKET_PATH" || (inherited && strings.HasPrefix(upperKey, "HERDR_")) {
				continue
			}
		}
		if !ok || key != "" {
			out = append(out, v)
		}
	}
	path := dir
	if option.IncludeBasePath {
		path += string(os.PathListSeparator) + pathValue(base)
	}
	if option.SystemPath != "" {
		path += string(os.PathListSeparator) + option.SystemPath
	}
	// Race-instrumented test binaries otherwise pause for one second on
	// exit, which changes the short-lived CLI contract. Keep race detection
	// and the caller's other detector options; remove only that exit delay.
	out = append(out, "PATH="+path, configEnv+"="+dir, "HERDR_SOCKET_PATH="+filepath.Join(dir, "herdr.sock"), "GORACE="+RaceOptions(base))
	return out
}

func sameEnvironment(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func pathValue(env []string) string {
	for _, v := range env {
		if strings.HasPrefix(strings.ToUpper(v), "PATH=") {
			return v[len("PATH="):]
		}
	}
	return ""
}

// Main assumes the fake CLI role when Install's config environment variable is set.
// Call at the start of TestMain; it exits the re-executed test process after dispatch.
func Main() {
	configPath := os.Getenv(configEnv)
	if configPath == "" {
		return
	}
	started := time.Now()
	if info, statErr := os.Stat(configPath); statErr == nil && info.IsDir() {
		name := configName(filepath.Base(os.Args[0]))
		configPath = filepath.Join(configPath, name+".json")
	}
	b, err := os.ReadFile(configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(126)
	}
	var cfg script
	if err = json.Unmarshal(b, &cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(126)
	}
	argv := append([]string(nil), os.Args[1:]...)
	call := Call{Argv: argv}
	if wd, wdErr := os.Getwd(); wdErr == nil {
		call.Cwd = wd
	}
	if cfg.CaptureStdin {
		stdin, readErr := io.ReadAll(os.Stdin)
		if readErr != nil {
			fmt.Fprintln(os.Stderr, "fakecli: read stdin:", readErr)
			os.Exit(126)
		}
		call.Stdin = string(stdin)
	}
	if len(cfg.CaptureEnv) > 0 {
		call.Env = make(map[string]string, len(cfg.CaptureEnv))
		for _, key := range cfg.CaptureEnv {
			if value, ok := os.LookupEnv(key); ok {
				call.Env[key] = value
			}
		}
	}
	unlock, err := lock(cfg.Log + ".lock")
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakecli: lock call log:", err)
		os.Exit(126)
	}
	line, _ := json.Marshal(call)
	f, err := os.OpenFile(cfg.Log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err == nil {
		_, err = f.Write(append(line, '\n'))
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
	}
	if err != nil {
		unlock()
		fmt.Fprintln(os.Stderr, err)
		os.Exit(126)
	}
	recordStart(cfg.Log, started)
	callNumber := 0
	if calls, readErr := ReadCalls(cfg.Log); readErr == nil {
		for _, c := range calls {
			if same(argv, c.Argv) {
				callNumber++
			}
		}
	}
	unlock()
	for _, rule := range cfg.Rules {
		if !matches(argv, rule) || (rule.Call != 0 && rule.Call != callNumber) {
			continue
		}
		if rule.WaitFile != "" {
			if err := waitForFile(rule.WaitFile, waitFileTimeout()); err != nil {
				fmt.Fprintf(os.Stderr, "fakecli: %v\n", err)
				os.Exit(124)
			}
		}
		if rule.Delay > 0 {
			time.Sleep(time.Duration(rule.Delay) * time.Millisecond)
		}
		_, _ = os.Stdout.Write(append([]byte(rule.Stdout), rule.StdoutBytes...))
		_, _ = os.Stderr.Write(append([]byte(rule.Stderr), rule.StderrBytes...))
		if rule.Signal != "" {
			terminate(rule.Signal, rule.Code)
		}
		os.Exit(rule.Code)
	}
	fmt.Fprintf(os.Stderr, "fakecli: no rule for %q\n", argv)
	os.Exit(127)
}

// startsFile is the file beside the call log that holds the start time of
// each call, one line per log line and in the log's order.
func startsFile(log string) string { return log + ".starts" }

// recordStart appends the time the fake began running (UnixNano) to the
// starts file. It runs inside the call log's lock, right after the call's log
// line, so both files list the calls in the same order. It is best effort: a
// failed write leaves the starts short, which ReadCallStarts callers see as
// fewer starts than calls.
func recordStart(log string, started time.Time) {
	f, err := os.OpenFile(startsFile(log), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = f.WriteString(strconv.FormatInt(started.UnixNano(), 10) + "\n")
	_ = f.Close()
}

func configName(name string) string {
	if strings.EqualFold(filepath.Ext(name), ".exe") {
		return name[:len(name)-len(filepath.Ext(name))]
	}
	return name
}

func matches(argv []string, rule Rule) bool {
	if rule.AnyArgs {
		return true
	}
	if rule.ArgvPrefix {
		return len(argv) >= len(rule.Argv) && same(argv[:len(rule.Argv)], rule.Argv)
	}
	return same(argv, rule.Argv)
}

// ReadCalls decodes the ordered JSONL call log.
func ReadCalls(file string) ([]Call, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var calls []Call
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var c Call
		if err = json.Unmarshal([]byte(line), &c); err != nil {
			return nil, err
		}
		calls = append(calls, c)
	}
	return calls, nil
}

// ReadCallStarts returns when each call of the log began running, in the log's
// order: the fake takes the time as soon as the re-executed process reaches
// Main, so it is the call's own start and does not depend on when, or how
// late, the test reads the log.
func ReadCallStarts(log string) ([]time.Time, error) {
	b, err := os.ReadFile(startsFile(log))
	if err != nil {
		return nil, err
	}
	var starts []time.Time
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		nano, err := strconv.ParseInt(line, 10, 64)
		if err != nil {
			return nil, err
		}
		starts = append(starts, time.Unix(0, nano))
	}
	return starts, nil
}

// ReadCallsForConfig locates and reads the log created from Install's config file.
func ReadCallsForConfig(configFile string) ([]Call, error) {
	b, err := os.ReadFile(configFile)
	if err != nil {
		return nil, err
	}
	var cfg script
	if err = json.Unmarshal(b, &cfg); err != nil {
		return nil, err
	}
	return ReadCalls(cfg.Log)
}

func same(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
