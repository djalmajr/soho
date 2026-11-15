package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Test environment reads are references, not just calls: importing os under
// another name, using a dot import, or saving an accessor as a function value
// must not bypass the boundary. This is a source boundary, not interprocedural
// proof that production code or generated source never reads the environment.
// The parser's object links distinguish locally shadowed names from imports.
type testEnvRead struct {
	file, function, symbol string
	line                   int
}

type testEnvException struct {
	file, function, symbol, reason string
}

// No directory is exempt, including testutil and platform-tagged sources.
// Every exception is a single file/function/symbol with a reviewed reason.
var testEnvExceptions = []testEnvException{
	{"cmd/soho-release/main_test.go", "runSlowGoHelper", "Getenv", "Re-executed fake Go tool reads the SIGNAL_TEST_PIDFILE explicitly supplied by the signal fixture."},
	{"container/dockerfetch_test.go", "skipUnlessCurlAvailable", "Getenv", "CI is an explicit runner prerequisite policy: missing curl fails CI and skips local runs; no agent identity or state is consumed."},
	{"internal/cli/commands_state_test.go", "TestMain", "Getenv", "Dispatch only the four enumerated re-exec helper roles before suite isolation; parent fixtures supply these role keys."},
	{"internal/cli/dispatch_compact_phase_test.go", "TestCompactPhaseLivenessChild", "Getenv", "Read the duration supplied by compactPhaseLivenessChild to this re-executed sleeper."},
	{"internal/cli/dispatch_compact_phase_test.go", "TestF11CompactPhaseChild", "Getenv", "Read the scratch root explicitly supplied by the parent dispatch interruption fixture."},
	{"internal/cli/dispatch_compact_phase_test.go", "TestDispatchCompactSigKILLKeepsTheInterruptedMarker", "Getenv", "Detect the explicit F11 child role to avoid recursive execution; the child environment is built with CleanEnv."},
	{"internal/cli/dispatch_git_round2_test.go", "init", "Environ", "The re-executed review338-collect command reconstructs its explicitly supplied parent fixture environment before TestMain."},
	{"internal/cli/dispatch_remaining_test.go", "init", "Getenv", "The re-executed prompt fake selects the role explicitly supplied by its parent fixture."},
	{"internal/cli/fixture_isolation_test.go", "TestFixtureIsolationAuxiliaryProcesses", "Environ", "Capture the environment deliberately contaminated by this test to prove auxiliary children scrub session variables."},
	{"internal/cli/fixture_isolation_test.go", "isolatedUserEnv", "Getenv", "Read only HOME, USERPROFILE and XDG_CONFIG_HOME after TestMain has assigned private suite directories."},
	{"internal/cli/fixture_isolation_test.go", "TestFixtureIsolation", "Getenv", "Assert environment values assigned by TestMain remain isolated; environment handling is the test subject."},
	{"internal/cli/footprint_explain_test.go", "TestExplainFootprintHelper", "Getenv", "Select the footprint helper role explicitly assigned by the parent process fixture."},
	{"internal/cli/gate_test.go", "TestGateHelperExit", "Getenv", "Read the exit code explicitly assigned by the gate subprocess fixture."},
	{"internal/cli/inventory_test.go", "TestInventoryCLIRegisteredTeamSample", "Getenv", "The test sets SOHO_TEST_INVENTORY_SAMPLE to its TempDir before reading; captured evidence cannot target an inherited path."},
	{"internal/cli/job_partial_public_test.go", "init", "Getenv", "The re-executed fake Herdr selects the HERDR_FAKE role explicitly supplied by the fixture."},
	{"internal/cli/job_partial_public_test.go", "partialPublicFakeHerdrSoho", "Getenv", "Read the report path explicitly assigned to the fake child by the fixture."},
	{"internal/cli/job_private_launch_test.go", "managedCLIHelperMain", "Getenv", "Managed subprocess fixture dispatches explicit roles and forwards or inspects its fixture-owned launch environment."},
	{"internal/cli/job_private_launch_test.go", "managedCLIHelperMain", "Environ", "Managed subprocess fixture dispatches explicit roles and forwards or inspects its fixture-owned launch environment."},
	{"internal/cli/procs_stop_test.go", "TestProcsStopExitChild", "Getenv", "Select the short-lived process role explicitly supplied by the procs stop fixture."},
	{"internal/cli/procs_test.go", "TestProcFixtureHelper", "Getenv", "Read the role and two arguments explicitly assigned by native process fixtures."},
	{"internal/cli/procs_test.go", "runProcsHelperRole", "Environ", "Forward the controlled child environment when the explicitly selected process role starts its descendant."},
	{"internal/cli/release_dispatch_lifecycle_hc_test.go", "TestHcLifecycleCommandHelper", "Getenv", "Read only lifecycle root, args, lock, gate and fake config explicitly assigned by the parent lifecycle fixture."},
	{"internal/cli/release_dispatch_lifecycle_hc_test.go", "TestReleaseHcLifecycleCleanupFailureHelper", "Getenv", "Read the cleanup marker explicitly supplied to the re-executed failure helper."},
	{"internal/cli/review_snapshot_timeout_unix_test.go", "review545TimeoutHelper", "Getenv", "Re-executed Git helper consumes explicit role, marker and real Git path and forwards the controlled child environment."},
	{"internal/cli/review_snapshot_timeout_unix_test.go", "review545TimeoutHelper", "Environ", "Re-executed Git helper consumes explicit role, marker and real Git path and forwards the controlled child environment."},
	{"internal/cli/spawn_env_redact_test.go", "init", "Getenv", "The re-executed RPC fake reads the synthetic response explicitly supplied by the redaction test."},
	{"internal/cli/tui_profile_model_test.go", "TestOpenCodeTUIModelDiscovery", "LookupEnv", "Read back a process selector explicitly set by this test to prove routing does not mutate it."},
	{"internal/cli/worker_state_test.go", "init", "Getenv", "Re-executed worker fake consumes its parent-supplied role before TestMain."},
	{"internal/core/compact_phase_test.go", "TestCompactPhaseLivenessChild", "Getenv", "Read the sleeper duration explicitly supplied by the liveness fixture."},
	{"internal/core/footprint_test.go", "TestFootprintHelper", "Getenv", "Dispatch the process role explicitly supplied by the footprint fixture."},
	{"internal/core/procs_test.go", "TestProcsSleeperHelper", "Getenv", "Dispatch the sleeper role explicitly supplied by the process fixture."},
	{"internal/core/state_test.go", "TestRosterAppendProcess", "Getenv", "Read the roster child directory and name explicitly supplied by the concurrent roster fixture."},
	{"internal/core/worker_lifecycle_test.go", "TestWorkerLifecycleCrashHelper", "Getenv", "Read the scratch state root explicitly supplied by the lock-lifetime fixture."},
	{"internal/eval/eval_test.go", "hermeticEnv", "Environ", "Snapshot for native Go builds; session families are removed before toolchain overrides and child execution."},
	{"internal/eval/fixture_controls_test.go", "goExecutableForTest", "Getenv", "EVAL_GO_BIN is an explicit native toolchain override validated as an absolute existing executable, otherwise native Go is resolved."},
	{"internal/eval/lifecycle_regression_test.go", "lifecycleScratchEnv", "Environ", "Snapshot for scratch native Go builds; session families are removed before scratch toolchain overrides."},
	{"internal/eval/probe_compile_once_test.go", "init", "Getenv", "Re-executed compile spy reads its explicit run, Go binary and log controls and forwards its controlled toolchain environment."},
	{"internal/eval/probe_compile_once_test.go", "init", "Environ", "Re-executed compile spy reads its explicit run, Go binary and log controls and forwards its controlled toolchain environment."},
	{"internal/eval/reviewer_fixture_test.go", "childToolchainEnv", "Environ", "Snapshot for relocated native Go aliases; session families are removed before restricted toolchain overrides."},
	{"internal/gate/gate_unix_test.go", "TestInterruptingTheGateEndsTheChildGroup", "Getenv", "Read the failure marker and kill mode explicitly supplied by the gate interruption parent fixture."},
	{"internal/gate/helper_test.go", "helper", "Getenv", "Echo the GATE_PROBE value explicitly assigned by the stdin/environment consumer fixture."},
	{"internal/hc/terminal_run_test.go", "init", "Getenv", "Dispatch the fake HC role explicitly assigned by the parent fixture before TestMain."},
	{"internal/hc/terminal_run_test.go", "runTestHcHelper", "Getenv", "Read the HC helper mode explicitly assigned by the parent fixture."},
	{"internal/job/managed_helper_test.go", "managedHelperMain", "Getenv", "Dispatch explicit managed helper roles and inspect the controlled child environment for the environment propagation contract."},
	{"internal/job/managed_helper_test.go", "managedHelperMain", "Environ", "Dispatch explicit managed helper roles and inspect the controlled child environment for the environment propagation contract."},
	{"internal/job/managed_helper_test.go", "managedHelperEnv", "Environ", "Decode the fixture-owned managed child environment, including launch locators whose propagation is the test subject."},
	{"internal/job/own_checkout_query_test.go", "init", "Getenv", "Re-executed fake Git consumes explicit mode/PID-file controls and forwards its controlled environment to its descendant."},
	{"internal/job/own_checkout_query_test.go", "init", "Environ", "Re-executed fake Git consumes explicit mode/PID-file controls and forwards its controlled environment to its descendant."},
	{"internal/peer/peer_find_stream_test.go", "init", "Getenv", "Re-executed remote fake records its PID at the path explicitly supplied by the parent streaming fixture."},
	{"internal/peer/peer_test.go", "TestSend", "Getenv", "Explicit operator opt-in for the isolated-socket real-Herdr guard; ordinary runs skip it and no session identity is read."},
	{"internal/platform/platform_test.go", "TestMain", "Getenv", "Dispatch the native CLI role explicitly supplied by the parent fixture before suite initialization."},
	{"internal/platform/platform_test.go", "runRunCliFixtureRole", "Getenv", "Read only the PID-file controls explicitly assigned by the native process fixture."},
	{"internal/platform/platform_test.go", "runCliFixtureGrandchild", "Environ", "Forward the fixture-owned child environment to its native grandchild, replacing only the helper role."},
	{"internal/platform/platform_test.go", "fixtureChildIdentity", "Getenv", "Use the explicit parent-supplied native ps path for child identity verification."},
	{"internal/platform/proc_exec_test.go", "TestProcExecHelper", "Getenv", "Dispatch the role explicitly supplied by the process executable fixture."},
	{"internal/platform/proc_flags_unix_test.go", "TestProcFlagsSessionSleeper", "Getenv", "Dispatch the sleeper role explicitly assigned by the native session fixture."},
	{"internal/platform/process_fixture_test.go", "TestProcFixtureHelper", "Getenv", "Read the role and two arguments explicitly assigned by the native process fixture."},
	{"internal/platform/process_fixture_test.go", "procFixtureChild", "Environ", "Forward the controlled process fixture environment to its child, replacing role and argument keys."},
	{"internal/platform/rss_test.go", "TestRSSHelper", "Getenv", "Dispatch the role explicitly assigned by the native RSS fixture."},
	{"internal/platform/strict_output_windows_test.go", "strictWindowsFixture", "Getenv", "Read only SystemRoot, WINDIR and COMSPEC to launch the native Windows command wrapper; PATH and user paths are fixture-owned."},
	{"internal/platform/waitdelay_test.go", "TestWaitDelaySigtermHelper", "Getenv", "Dispatch the role explicitly assigned by the wait-delay fixture."},
	{"internal/plugin/board_test.go", "TestBoardPTYProbeEntrypoint", "Getenv", "Explicit external PTY harness entrypoint supplies the executable; ordinary tests return without starting it."},
	{"internal/plugin/picker_run_test.go", "TestPickerPTYProbeEntrypoint", "Getenv", "Explicit external PTY harness entrypoint supplies the executable; ordinary tests return without starting it."},
	{"internal/plugin/plugin_test.go", "TestMain", "Getenv", "The child CWD marker is explicitly supplied by the caller-check fixture before TestMain."},
	{"internal/plugin/session_render_test.go", "TestSessionRenderFrames", "Getenv", "The test sets its evidence directory to TempDir before reading, so rendering cannot write to inherited session paths."},
	{"internal/release/release_test.go", "runHelper", "Getenv", "Re-executed fake release tools consume explicitly assigned log/failure/output controls and assert the GOOS/GOARCH/CGO environment passed by the release builder."},
	{"internal/testutil/testenv/env.go", "CleanEnv", "Environ", "The sanctioned fixture environment builder reads the host once and removes session families before assigning private temp/socket paths."},
	{"internal/testutil/fakecli/fakecli.go", "waitFileTimeout", "Getenv", "Read the bounded wait-file timeout explicitly supplied to a re-executed fake."},
	{"internal/testutil/fakecli/fakecli.go", "GuardSuiteRun", "Getenv", "Read the explicit fake config role to refuse executing an unconfigured fake as the whole test suite."},
	{"internal/testutil/fakecli/fakecli.go", "warmPrepared", "Environ", "Prepare a Windows test image with an explicit warm fake configuration and preserved race detector options; no live backend is invoked."},
	{"internal/testutil/fakecli/fakecli.go", "Env", "Environ", "Compare the supplied base to the process snapshot to retain the inherited-Herdr scrub compatibility contract; this comparison is the environment handling subject."},
	{"internal/testutil/fakecli/fakecli.go", "Main", "Getenv", "Read the explicit fake configuration and only capture keys named by that fixture configuration for environment assertions."},
	{"internal/testutil/fakecli/fakecli.go", "Main", "LookupEnv", "Read the explicit fake configuration and only capture keys named by that fixture configuration for environment assertions."},
	{"internal/testutil/fakecli/fakecli_test.go", "TestReexecRecordsExactArgumentsAndDispatchesRule", "Environ", "Pass the current snapshot to exercise fakecli.Env inherited environment handling with explicit fake configuration."},
	{"internal/testutil/fakecli/fakecli_test.go", "TestFakeCLIConfigNameHandlesWindowsExtensionCase", "Environ", "Pass the current snapshot to exercise fakecli.Env inherited environment handling with explicit fake configuration."},
	{"internal/testutil/fakecli/fakecli_test.go", "TestFakeCLIEnvironmentIsolationAndOptInSystemPath", "Environ", "The test explicitly contaminates environment values and exercises the inherited-base scrub and PATH opt-in contract."},
	{"internal/testutil/fakecli/fakecli_test.go", "TestFakeCLIExtendedRuleAndCaptureAPI", "Environ", "Pass the current snapshot to exercise fakecli.Env inherited environment handling with explicit fake configuration."},
	{"internal/testutil/fakecli/fakecli_test.go", "TestFakeCLIConcurrentCallOrdinalIsSerialized", "Environ", "Pass the current snapshot to exercise fakecli.Env inherited environment handling with explicit fake configuration."},
	{"internal/testutil/fakecli/fakecli_test.go", "TestFakeCLINoRuleReturnsDiagnosticAndFailure", "Environ", "Pass the current snapshot to exercise fakecli.Env inherited environment handling with explicit fake configuration."},
	{"internal/testutil/fakecli/fakecli_test.go", "TestReexecRecordsTheWorkingDirectory", "Environ", "Pass the current snapshot to exercise fakecli.Env inherited environment handling with explicit fake configuration."},
	{"internal/testutil/fakecli/fakecli_test.go", "TestHelperNativeEnvClearedInRunner", "Getenv", "The child reads explicit helper selection and verifies the runner removed the parent-injected HC/SOHO context."},
	{"internal/testutil/fakecli/fakecli_test.go", "TestRunTestsClearsInheritedNativeContext", "Environ", "Build the deliberately contaminated parent snapshot whose HC/SOHO values must be scrubbed by RunTests."},
	{"internal/testutil/fakecli/fakecli_waitfile_test.go", "waitFileEnvFor", "Environ", "Pass the current snapshot to exercise fakecli.Env inherited environment handling before supplying explicit wait-file controls."},
	{"internal/testutil/fakecli/fakecli_waitfile_test.go", "waitFileTimeoutFor", "Getenv", "Save and restore a test-assigned timeout while exercising timeout parsing; environment handling is the subject."},
	{"internal/testutil/fakecli/guard_test.go", "TestGuardSuiteRunRefusesAConfiglessFake", "Getenv", "Select the explicit re-executed guard-probe role to prove an unconfigured fake is refused."},
	{"internal/testutil/testenv/host_env.go", "HostPath", "Getenv", "Sanctioned fixed-key native tool discovery accessor; never accepts a variable name."},
	{"internal/testutil/testenv/host_env.go", "HostSystemRoot", "Getenv", "Sanctioned fixed-key Windows loader accessor; never accepts a variable name."},
	{"internal/testutil/testenv/host_env.go", "HostRaceOptions", "Getenv", "Sanctioned fixed-key race detector accessor; never accepts a variable name."},
	{"internal/testutil/testenv/host_env.go", "HostGoCache", "Getenv", "Sanctioned fixed-key native build cache accessor; never accepts a variable name."},
	{"internal/testutil/testenv/host_env.go", "HostGoRoot", "Getenv", "Sanctioned fixed-key native toolchain root accessor; never accepts a variable name."},
	{"internal/testutil/testenv/host_env.go", "HostRustupHome", "Getenv", "Sanctioned fixed-key installed Cargo toolchain accessor; never accepts a variable name."},
	{"internal/testutil/testenv/host_env.go", "HostTempDir", "Getenv", "Sanctioned fixed-key native scratch root accessor; never accepts a variable name."},
	{"internal/wait/status_view_test.go", "TestStatusViewFakeChildRaceOptions", "LookupEnv", "The test explicitly sets GORACE before snapshotting, then verifies the child preserves options and the parent remains unchanged."},
	{"internal/wait/status_view_test.go", "TestStatusViewFakeChildRaceOptions", "Environ", "The test explicitly sets GORACE before snapshotting, then verifies the child preserves options and the parent remains unchanged."},
}

func testEnvSourcePath(rel string) bool {
	return strings.HasSuffix(rel, "_test.go") || strings.HasPrefix(rel, "internal/testutil/") && strings.HasSuffix(rel, ".go")
}

func testEnvReads(rel string, src []byte) ([]testEnvRead, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, rel, src, 0)
	if err != nil {
		return nil, err
	}
	aliases := map[string]bool{}
	dot := false
	for _, imp := range file.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			return nil, err
		}
		if path != "os" {
			continue
		}
		name := "os"
		if imp.Name != nil {
			name = imp.Name.Name
		}
		if name == "." {
			dot = true
		} else if name != "_" {
			aliases[name] = true
		}
	}
	restricted := func(name string) bool { return name == "Getenv" || name == "LookupEnv" || name == "Environ" }
	var reads []testEnvRead
	for _, decl := range file.Decls {
		function := "<package-level>"
		if fn, ok := decl.(*ast.FuncDecl); ok {
			function = fn.Name.Name
		}
		// Selector members are not unqualified dot-import references, including
		// selectors whose receiver is a local value or another imported package.
		members := map[*ast.Ident]bool{}
		ast.Inspect(decl, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.SelectorExpr:
				members[n.Sel] = true
			case *ast.Field:
				// A declared field or parameter name is not a reference.
				for _, name := range n.Names {
					members[name] = true
				}
			case *ast.CompositeLit:
				// A key of a struct literal names a field. A key of a map
				// literal is an expression and stays checked.
				if _, isMap := n.Type.(*ast.MapType); isMap {
					break
				}
				for _, elt := range n.Elts {
					if kv, ok := elt.(*ast.KeyValueExpr); ok {
						if key, ok := kv.Key.(*ast.Ident); ok {
							members[key] = true
						}
					}
				}
			}
			return true
		})
		ast.Inspect(decl, func(n ast.Node) bool {
			symbol := ""
			switch n := n.(type) {
			case *ast.SelectorExpr:
				if id, ok := n.X.(*ast.Ident); ok && id.Obj == nil && aliases[id.Name] && restricted(n.Sel.Name) {
					symbol = n.Sel.Name
				}
			case *ast.Ident:
				if dot && n.Obj == nil && !members[n] && restricted(n.Name) {
					symbol = n.Name
				}
			}
			if symbol != "" {
				reads = append(reads, testEnvRead{rel, function, symbol, fset.Position(n.Pos()).Line})
			}
			return true
		})
	}
	return reads, nil
}

func testEnvViolations(reads []testEnvRead, exceptions []testEnvException) []string {
	matched := make([]bool, len(exceptions))
	var violations []string
	keys := map[string]bool{}
	for _, site := range exceptions {
		key := site.file + "/" + site.function + "/" + site.symbol
		if strings.TrimSpace(site.reason) == "" {
			violations = append(violations, "empty exception reason: "+key)
		}
		if keys[key] {
			violations = append(violations, "duplicate exception: "+key)
		}
		keys[key] = true
	}
	for _, read := range reads {
		allowed := false
		for i, site := range exceptions {
			if site.file == read.file && site.function == read.function && site.symbol == read.symbol && strings.TrimSpace(site.reason) != "" {
				matched[i], allowed = true, true
			}
		}
		if !allowed {
			violations = append(violations, fmt.Sprintf("%s:%d in %s: os.%s reference outside the test environment boundary", read.file, read.line, read.function, read.symbol))
		}
	}
	for i, site := range exceptions {
		if !matched[i] {
			violations = append(violations, fmt.Sprintf("stale environment allowlist entry: %s / %s / %s", site.file, site.function, site.symbol))
		}
	}
	return violations
}

func TestTestEnvironmentBoundary(t *testing.T) {
	root := moduleRoot(t)
	var reads []testEnvRead
	count := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && (d.Name() == "vendor" || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !testEnvSourcePath(rel) {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		found, err := testEnvReads(rel, src)
		if err != nil {
			return err
		}
		reads = append(reads, found...)
		count++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, violation := range testEnvViolations(reads, testEnvExceptions) {
		t.Error(violation)
	}
	t.Logf("checked %d test/support files, %d environment references", count, len(reads))
}

func TestTestEnvironmentChecker(t *testing.T) {
	for _, tc := range []struct {
		name, src string
		count     int
		function  string
	}{
		{"direct", `package p; import "os"; func f(){ os.Getenv("X") }`, 1, "f"},
		{"alias", `package p; import host "os"; func f(){ host.LookupEnv("X") }`, 1, "f"},
		{"function value", `package p; import "os"; var read = os.Getenv`, 1, "<package-level>"},
		{"dot function value", `package p; import . "os"; var read = Environ`, 1, "<package-level>"},
		{"dot call", `package p; import . "os"; func f(){ Getenv("X") }`, 1, "f"},
		{"closure", `package p; import "os"; func f(){ _ = func(){ os.Environ() } }`, 1, "f"},
		{"other import", `package p; import os "example.org/os"; func f(){ os.Getenv("X") }`, 0, ""},
		{"shadow alias", `package p; import "os"; func f(){ os := struct{Getenv func(string)string}{}; os.Getenv("X") }`, 0, ""},
		{"shadow dot", `package p; import . "os"; func f(){ Getenv := func(string)string{return ""}; Getenv("X") }`, 0, ""},
		{"dot other member", `package p; import . "os"; func f(){ value.Getenv("X") }`, 0, ""},
		{"dot struct key", `package p; import . "os"; type T struct{ Getenv int }; func f(){ _ = Getpid(); _ = T{Getenv: 1} }`, 0, ""},
		{"dot struct key value", `package p; import . "os"; type T struct{ Read func(string) string }; func f(){ _ = T{Read: Getenv} }`, 1, "f"},
		{"dot map key", `package p; import . "os"; func f(){ _ = map[any]int{&Environ: 1} }`, 1, "f"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads, err := testEnvReads("fixture_windows_test.go", []byte(tc.src))
			if err != nil {
				t.Fatal(err)
			}
			if len(reads) != tc.count {
				t.Fatalf("reads=%+v, want %d", reads, tc.count)
			}
			if tc.count == 0 {
				return
			}
			if reads[0].function != tc.function {
				t.Fatalf("enclosing function=%q, want %q", reads[0].function, tc.function)
			}
			if failures := testEnvViolations(reads, nil); len(failures) != tc.count {
				t.Fatalf("unclassified reads accepted: %v", failures)
			}
			site := testEnvException{reads[0].file, tc.function, reads[0].symbol, "controlled source fixture"}
			if failures := testEnvViolations(reads, []testEnvException{site}); len(failures) != 0 {
				t.Fatal(failures)
			}
			site.function = "removedFunction"
			failures := strings.Join(testEnvViolations(reads, []testEnvException{site}), "\n")
			if !strings.Contains(failures, "fixture_windows_test.go:1") || !strings.Contains(failures, "stale environment allowlist entry") {
				t.Fatal(failures)
			}
		})
	}
	for _, path := range []string{"p/a_windows_test.go", "p/a_unix_test.go", "internal/testutil/fakecli/a.go", "p/testdata/a_test.go"} {
		if !testEnvSourcePath(path) {
			t.Errorf("source omitted: %s", path)
		}
	}
	if testEnvSourcePath("internal/platform/process.go") {
		t.Fatal("production included")
	}
	for _, reason := range []string{"", " "} {
		failures := testEnvViolations(nil, []testEnvException{{"removed_test.go", "missing", "Getenv", reason}})
		if len(failures) != 2 {
			t.Fatalf("empty/stale exception accepted: %v", failures)
		}
	}
}
