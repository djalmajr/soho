//go:build darwin || linux

package platform

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The helper and probe roles travel on registered test flags, not the
// environment: a mutant that drops the whole environment must not be able to
// disable the probe.
var (
	envExecChildFlag = flag.Bool("soho-env-exec-child", false, "internal worker env exec helper child")
	envExecProbeFlag = flag.Bool("soho-env-exec-probe", false, "internal worker env exec probe child")
)

// The executed helper runs in a child test process: it replaces that child
// with an inert Go probe, so the parent can observe exact presence/absence
// without any provider, shell or interpreter.
const (
	envExecSpecVar  = "SOHO_TEST_WORKER_ENV_EXEC_SPEC"
	envExecDropName = "TEST_DROP_ME"
	envExecKeepName = "TEST_KEEP_ME"
)

// TestWorkerEnvExecHelperChild executes WorkerEnvExec from the NUL-joined
// spec in its environment. On success it never returns.
func TestWorkerEnvExecHelperChild(t *testing.T) {
	if !*envExecChildFlag {
		t.Skip("helper child only")
	}
	spec := os.Getenv(envExecSpecVar)
	if spec == "" {
		t.Fatalf("helper child has no exec spec")
	}
	if err := WorkerEnvExec(strings.Split(spec, "\x1f")); err != nil {
		t.Fatalf("worker env exec: %v", err)
	}
	t.Fatal("worker env exec returned without replacing the process")
}

// TestWorkerEnvExecProbeIsInert is the exec'd child: it asserts only the
// presence and absence of synthetic names, then exits.
func TestWorkerEnvExecProbeIsInert(t *testing.T) {
	if !*envExecProbeFlag {
		t.Skip("probe child only")
	}
	if _, present := os.LookupEnv(envExecDropName); present {
		t.Fatalf("dropped name %s survived the helper", envExecDropName)
	}
	if value, present := os.LookupEnv(envExecKeepName); !present || value != "synthetic-keep" {
		t.Fatalf("unrelated name %s was not preserved byte for byte", envExecKeepName)
	}
	if _, present := os.LookupEnv("PATH"); !present {
		t.Fatal("unrelated PATH was lost")
	}
	if value := os.Getenv("TEST_DYNAMIC_UNRELATED"); value != "/synthetic/empty-dir" {
		t.Fatalf("unrelated dynamic-loader-shaped name = %q, want it preserved", value)
	}
}

func TestWorkerEnvExecFiltersOnlyChosenNames(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		t.Fatal(err)
	}
	spec := strings.Join([]string{
		envExecDropName, "--", exe, "-test.run=^TestWorkerEnvExecProbeIsInert$", "-soho-env-exec-probe",
	}, "\x1f")

	env := append(os.Environ(),
		envExecSpecVar+"="+spec,
		envExecDropName+"=synthetic-drop",
		envExecKeepName+"=synthetic-keep",
		"TEST_DYNAMIC_UNRELATED=/synthetic/empty-dir",
	)
	cmd := exec.Command(exe, "-test.run=^TestWorkerEnvExecHelperChild$", "-soho-env-exec-child")
	cmd.Env = env
	cmd.Dir = t.TempDir()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper child failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "PASS") && !strings.Contains(string(out), "ok ") {
		t.Fatalf("exec'd probe did not report success:\n%s", out)
	}
}

func TestWorkerEnvExecRejectsMalformedInvocation(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"missing delimiter", []string{"DROP_ME", "/bin/true"}},
		{"missing program", []string{"DROP_ME", "--"}},
		{"relative program", []string{"DROP_ME", "--", "true"}},
		{"empty name", []string{"", "--", "/bin/true"}},
		{"option-shaped name", []string{"--DROP_ME", "--", "/bin/true"}},
		{"assignment-shaped name", []string{"DROP_ME=VALUE", "--", "/bin/true"}},
		{"too many names", append(strings.Split(strings.TrimSpace(strings.Repeat("DROP_ME ", WorkerEnvExecMaxNames+1)), " "), "--", "/bin/true")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := WorkerEnvExec(tc.args); err == nil {
				t.Fatal("malformed invocation was accepted")
			}
		})
	}
}

func TestWorkerEnvExecArgvKeepsPayloadVerbatim(t *testing.T) {
	args := []string{"two words", `' " $(literal)`, "--", "NAME=value", "-leading"}
	got := WorkerEnvExecArgv([]string{"DROP_ONE", "DROP_TWO"}, "/abs/worker", args)
	want := append([]string{"DROP_ONE", "DROP_TWO", "--", "/abs/worker"}, args...)
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
	if _, program, workerArgs, err := parseWorkerEnvExecArgs(got); err != nil || program != "/abs/worker" || strings.Join(workerArgs, "\x00") != strings.Join(args, "\x00") {
		t.Fatalf("round trip = %q %q %v", program, workerArgs, err)
	}
}

// A portable name longer than the helper's former 256-byte cap must be
// accepted by the shared predicate and by the argv parser, so a transport
// name cannot pass preflight and then fail inside the helper.
func TestWorkerEnvDropNameAcceptsLongPortableName(t *testing.T) {
	long := "TEST_LONG_" + strings.Repeat("A", 300)
	if got := WorkerEnvDropNameProblem(long); got != WorkerEnvDropNameOK {
		t.Fatalf("long portable name classified as %v, want OK", got)
	}
	argv := WorkerEnvExecArgv([]string{long}, "/abs/worker", []string{"arg"})
	names, program, args, err := parseWorkerEnvExecArgs(argv)
	if err != nil || len(names) != 1 || names[0] != long || program != "/abs/worker" || len(args) != 1 || args[0] != "arg" {
		t.Fatalf("long name argv round trip = %v %q %v %v", names, program, args, err)
	}
}

func TestWorkerEnvDropNameClassifications(t *testing.T) {
	for _, tc := range []struct {
		name string
		want WorkerEnvDropName
	}{
		{"PRIVATE_VALUE", WorkerEnvDropNameOK},
		{"_LEADING_UNDERSCORE", WorkerEnvDropNameOK},
		{"", WorkerEnvDropNameInvalid},
		{"9LEADING_DIGIT", WorkerEnvDropNameInvalid},
		{"HAS-DASH", WorkerEnvDropNameInvalid},
		{"HAS_DOT.ext", WorkerEnvDropNameInvalid},
		{"SOHO_SOMETHING", WorkerEnvDropNameReserved},
		{"soho_lowercase", WorkerEnvDropNameReserved},
		{"HC_TERMINAL_ID", WorkerEnvDropNameReserved},
		{"herdr_env", WorkerEnvDropNameReserved},
	} {
		if got := WorkerEnvDropNameProblem(tc.name); got != tc.want {
			t.Fatalf("name %q classified %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestWorkerExecutableIsUsable(t *testing.T) {
	path, err := WorkerExecutable()
	if err != nil {
		t.Fatalf("worker executable: %v", err)
	}
	if !filepath.IsAbs(path) {
		t.Fatalf("path = %q, want absolute", path)
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("executable %q is not a runnable regular file: %v", path, err)
	}
}
