package fakecli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

func TestMain(m *testing.M) {
	RunTests(m)
}

func TestReexecRecordsExactArgumentsAndDispatchesRule(t *testing.T) {
	dir := t.TempDir()
	if _, err := Install(t, dir, "probe", []Rule{{Argv: []string{"subcommand", "space and \"quote\""}, Stdout: "ok\n"}}); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{}
	for _, entry := range Env(os.Environ(), dir) {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			env[key] = value
		}
	}
	r := platform.RunCli("probe", []string{"subcommand", "space and \"quote\""}, platform.RunOptions{Env: env, Platform: platform.Current(), TimeoutMs: 5000})
	if r.Status == nil || *r.Status != 0 || r.Stdout != "ok\n" {
		t.Fatalf("result: %+v", r)
	}
	calls, err := ReadCallsForConfig(filepath.Join(dir, "probe.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || !same(calls[0].Argv, []string{"subcommand", "space and \"quote\""}) {
		t.Fatalf("calls: %#v", calls)
	}
}

// Mutation captured: case-sensitive .exe trimming cannot locate a config from a PATHEXT .EXE name.
func TestFakeCLIConfigNameHandlesWindowsExtensionCase(t *testing.T) {
	if got := configName("probe.EXE"); got != "probe" {
		t.Fatalf("configName(%q) = %q", "probe.EXE", got)
	}
	dir := t.TempDir()
	if _, err := Install(t, dir, "probe", []Rule{{Argv: []string{"ping"}, Stdout: "pong"}}); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Symlink(filepath.Join(dir, "probe"), filepath.Join(dir, "probe.EXE")); err != nil {
			t.Fatal(err)
		}
	}
	env := platform.Env{}
	for _, entry := range Env(os.Environ(), dir) {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			env[key] = value
		}
	}
	r := platform.RunCli("probe.EXE", []string{"ping"}, platform.RunOptions{Env: env, Platform: platform.Current(), TimeoutMs: 5000})
	if r.Status == nil || *r.Status != 0 || r.Stdout != "pong" {
		t.Fatalf("upper-case extension reexec: %+v", r)
	}
}

// Mutation captured: retaining the host PATH leaves a real Herdr CLI reachable after the fake directory.
func TestFakeCLIEnvironmentIsolationAndOptInSystemPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_SOHO_DIR", "inherited")
	t.Setenv("HERDR_SOCKET_PATH", "inherited.sock")
	env := Env(os.Environ(), dir)
	if got := valueFor(env, "PATH"); got != dir {
		t.Fatalf("isolated PATH = %q", got)
	}
	controlled := Env([]string{"Path=/host/bin", "PATH=/other/bin", "KEEP=yes"}, dir)
	if got := valueFor(Env([]string{"Path=/host/bin", "PATH=/other/bin"}, dir, EnvOptions{IncludeBasePath: true, SystemPath: "/bin"}), "PATH"); got != dir+string(os.PathListSeparator)+"/host/bin"+string(os.PathListSeparator)+"/bin" {
		t.Fatalf("opt-in PATH = %q", got)
	}
	if valueFor(controlled, "KEEP") != "yes" {
		t.Fatalf("non-PATH environment lost: %#v", controlled)
	}
	raceEnv := Env([]string{"GORACE=exitcode=73 halt_on_error=1 atexit_sleep_ms=1000"}, dir)
	if got := valueFor(raceEnv, "GORACE"); got != "exitcode=73 halt_on_error=1 atexit_sleep_ms=0" {
		t.Fatalf("fake CLI changed race detection options: %q", got)
	}
	if valueFor(env, "HERDR_ENV") != "" || valueFor(env, "HERDR_SOHO_DIR") != "" {
		t.Fatalf("inherited Herdr session reached fake CLI: %#v", env)
	}
	socket := valueFor(env, "HERDR_SOCKET_PATH")
	if socket == "" || socket == "inherited.sock" {
		t.Fatalf("HERDR_SOCKET_PATH = %q", socket)
	}
	if _, err := os.Stat(socket); !os.IsNotExist(err) {
		t.Fatalf("fake CLI socket path exists or could not be checked: %v", err)
	}
}

// Mutation captured: exact-only rules and dropping stdin/environment/byte capture break the fake CLI contract.
func TestFakeCLIExtendedRuleAndCaptureAPI(t *testing.T) {
	dir := t.TempDir()
	if _, err := InstallWithOptions(t, dir, "probe", []Rule{{Argv: []string{"agent", "get"}, ArgvPrefix: true, StdoutBytes: []byte{0xff}, Stderr: "diagnostic"}}, InstallOptions{CaptureStdin: true, CaptureEnv: []string{"VISIBLE"}}); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{}
	for _, entry := range Env(os.Environ(), dir) {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			env[key] = value
		}
	}
	env["VISIBLE"] = "record me"
	r := platform.RunCli("probe", []string{"agent", "get", "extra"}, platform.RunOptions{Env: env, Platform: platform.Current(), TimeoutMs: 5000, Input: "stdin payload"})
	if r.Status == nil || *r.Status != 0 || r.Stderr != "diagnostic" {
		t.Fatalf("result: %+v", r)
	}
	calls, err := ReadCallsForConfig(filepath.Join(dir, "probe.json"))
	if err != nil || len(calls) != 1 {
		t.Fatalf("calls=%#v err=%v", calls, err)
	}
	if calls[0].Stdin != "stdin payload" || calls[0].Env["VISIBLE"] != "record me" || len(calls[0].Env) != 1 {
		t.Fatalf("captured call: %#v", calls[0])
	}
	if !bytes.Contains([]byte(r.Stdout), []byte("\uFFFD")) {
		t.Fatalf("byte output was not decoded by RunCli: %q", r.Stdout)
	}
}

// Mutation captured: removing the inter-process lock lets concurrent calls reuse ordinal 1 or lose log rows.
func TestFakeCLIConcurrentCallOrdinalIsSerialized(t *testing.T) {
	dir := t.TempDir()
	if _, err := Install(t, dir, "probe", []Rule{
		{Argv: []string{"parallel"}, Call: 1, Stdout: "first"},
		{Argv: []string{"parallel"}, Stdout: "later"},
	}); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{}
	for _, entry := range Env(os.Environ(), dir) {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			env[key] = value
		}
	}
	const count = 24
	var wg sync.WaitGroup
	results := make(chan string, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := platform.RunCli("probe", []string{"parallel"}, platform.RunOptions{Env: env, Platform: platform.Current(), TimeoutMs: 10000})
			if r.Status == nil || *r.Status != 0 {
				results <- fmt.Sprintf("exit:%+v", r)
				return
			}
			results <- r.Stdout
		}()
	}
	wg.Wait()
	close(results)
	first := 0
	for result := range results {
		if result == "first" {
			first++
		} else if result != "later" {
			t.Errorf("result %q", result)
		}
	}
	if first != 1 {
		t.Fatalf("first-call rules matched %d times", first)
	}
	calls, err := ReadCallsForConfig(filepath.Join(dir, "probe.json"))
	if err != nil || len(calls) != count {
		t.Fatalf("calls=%d err=%v", len(calls), err)
	}
	for _, call := range calls {
		if !reflect.DeepEqual(call.Argv, []string{"parallel"}) {
			t.Fatalf("argv: %#v", call.Argv)
		}
	}
}

// Mutation captured: a missing rule must fail loudly instead of succeeding silently.
func TestFakeCLINoRuleReturnsDiagnosticAndFailure(t *testing.T) {
	dir := t.TempDir()
	if _, err := Install(t, dir, "probe", nil); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{}
	for _, entry := range Env(os.Environ(), dir) {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			env[key] = value
		}
	}
	r := platform.RunCli("probe", []string{"missing"}, platform.RunOptions{Env: env, Platform: platform.Current(), TimeoutMs: 5000})
	if r.Status == nil || *r.Status != 127 || !strings.Contains(r.Stderr, "fakecli: no rule for") {
		t.Fatalf("result: %+v", r)
	}
}

func valueFor(entries []string, key string) string {
	for _, entry := range entries {
		name, value, ok := strings.Cut(entry, "=")
		if ok && strings.EqualFold(name, key) {
			return value
		}
	}
	return ""
}

// Mutation captured: a fake that does not record os.Getwd leaves Cwd empty,
// and a test could not tell which directory a call ran in.
func TestReexecRecordsTheWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	if _, err := Install(t, dir, "probe", []Rule{{AnyArgs: true}}); err != nil {
		t.Fatal(err)
	}
	env := platform.Env{}
	for _, entry := range Env(os.Environ(), dir) {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			env[key] = value
		}
	}
	first, second := t.TempDir(), t.TempDir()
	for _, cwd := range []string{first, second} {
		r := platform.RunCli("probe", []string{"ping"}, platform.RunOptions{Env: env, Platform: platform.Current(), TimeoutMs: 5000, Cwd: cwd})
		if r.Status == nil || *r.Status != 0 {
			t.Fatalf("result: %+v", r)
		}
	}
	calls, err := ReadCallsForConfig(filepath.Join(dir, "probe.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatalf("calls: %#v", calls)
	}
	for i, want := range []string{first, second} {
		wantReal, _ := filepath.EvalSymlinks(want)
		gotReal, _ := filepath.EvalSymlinks(calls[i].Cwd)
		if calls[i].Cwd == "" || gotReal != wantReal {
			t.Fatalf("call %d cwd=%q, want %q", i, calls[i].Cwd, want)
		}
	}
}
