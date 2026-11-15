package fakecli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/testutil"
)

// guardProbeEnv marks the probe child of
// TestGuardSuiteRunRefusesAConfiglessFake: when the guard is missing, the
// re-executed fake runs this package's suite and the test sees the marker
// and skips, so a guard-less mutant runs at most one level deep.
const guardProbeEnv = "SOHO_FAKECLI_GUARD_PROBE"

// A fake called without SOHO_FAKECLI_CONFIG must refuse to start the test
// suite (exit 126, one stderr line, no test output) instead of running the
// whole package suite, whose own fake calls would re-execute the fake again
// and chain (the 4.565 stuck `git` processes of DJA-390 came from exactly
// this). The probe runs the installed fake with arguments that carry no
// -test. flag, in an environment without the config variable, and proves
// the refusal.
func TestGuardSuiteRunRefusesAConfiglessFake(t *testing.T) {
	if os.Getenv(guardProbeEnv) == "1" {
		t.Skip("guard probe child: this process is the re-executed fake running the suite without the guard")
	}
	dir := t.TempDir()
	target, err := Install(t, dir, "probe", []Rule{{Argv: []string{"init", "-q"}, Stdout: "ok\n"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, target, "init", "-q")
	// A bare environment without SOHO_FAKECLI_CONFIG, the shape of a real
	// `git init -q` that finds the fake on PATH.
	cmd.Env = []string{
		"HOME=" + dir,
		"TMPDIR=" + dir,
		"PATH=" + testutil.HostPath(),
		guardProbeEnv + "=1",
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("the probe timed out after 30s: a config-less fake must exit at once (stdout=%q stderr=%q)", stdout.String(), stderr.String())
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("the probe run: %v (stdout=%q stderr=%q)", err, stdout.String(), stderr.String())
	}
	if exitErr.ExitCode() != 126 {
		t.Fatalf("exit = %d, want 126 (stdout=%q stderr=%q)", exitErr.ExitCode(), stdout.String(), stderr.String())
	}
	// The message names the fake as it was invoked: probe.exe on Windows.
	name := "probe"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	want := "fakecli: " + name + " ran without SOHO_FAKECLI_CONFIG; refusing to run the test suite as a fake command\n"
	if stderr.String() != want {
		t.Fatalf("stderr = %q, want %q", stderr.String(), want)
	}
	for _, marker := range []string{"PASS", "ok", "=== RUN"} {
		if strings.Contains(stdout.String(), marker) {
			t.Fatalf("stdout contains test output %q: %q", marker, stdout.String())
		}
	}
}

// The decision itself: the guard refuses only a config-less invocation whose
// argv carries no -test. flag and whose argv[0] does not look like a test
// binary, with the .exe suffix stripped and the .test suffix matched without
// case sensitivity.
func TestSuiteRunRefusalDecision(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config string
		args   []string
		want   string
		refuse bool
	}{{
		name:   "a fake called without config is refused with its name",
		config: "", args: []string{"/tmp/fakes/git", "init", "-q"}, want: "git", refuse: true,
	}, {
		name:   "with the config variable the suite always runs",
		config: "/tmp/fakes", args: []string{"/tmp/fakes/git", "init", "-q"}, want: "", refuse: false,
	}, {
		name:   "a -test. flag anywhere in argv is a test invocation",
		config: "", args: []string{"/x/fakecli.test", "-test.paniconexit0=true"}, want: "", refuse: false,
	}, {
		name:   "a later -test. flag counts too",
		config: "", args: []string{"git", "init", "-test.timeout=10s"}, want: "", refuse: false,
	}, {
		name:   "an argument containing -test. that does not start with it is not a flag",
		config: "", args: []string{"git", "-v", "x-test.v"}, want: "git", refuse: true,
	}, {
		name:   "a test binary name is not refused",
		config: "", args: []string{"/x/fakecli.test"}, want: "", refuse: false,
	}, {
		name:   "the .exe suffix is stripped before the .test check",
		config: "", args: []string{"C:/x/fakecli.test.exe", "-test.v"}, want: "", refuse: false,
	}, {
		name:   "an upper-case .EXE is stripped too",
		config: "", args: []string{"C:/x/FAKECLI.TEST.EXE"}, want: "", refuse: false,
	}, {
		name:   "a .Test name without flags is a test binary (case-insensitive)",
		config: "", args: []string{"C:/x/Probe.Test"}, want: "", refuse: false,
	}, {
		name:   "a fake with a .exe name is refused with its name kept",
		config: "", args: []string{"C:/fakes/git.exe", "init", "-q"}, want: "git.exe", refuse: true,
	}, {
		name:   "an upper-case fake name is refused",
		config: "", args: []string{"C:/fakes/GIT", "init"}, want: "GIT", refuse: true,
	}, {
		name:   "a bare name like go is refused",
		config: "", args: []string{"go", "build"}, want: "go", refuse: true,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			name, refused := suiteRunRefusal(tc.config, tc.args)
			if refused != tc.refuse || (refused && name != tc.want) {
				t.Fatalf("suiteRunRefusal(%q, %#v) = (%q, %v), want (%q, %v)", tc.config, tc.args, name, refused, tc.want, tc.refuse)
			}
		})
	}
}
