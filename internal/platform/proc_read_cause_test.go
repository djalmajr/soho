//go:build !windows

package platform

import (
	"errors"
	"os"
	"strconv"
	"syscall"
	"testing"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// deniedPsEnv puts a fake ps on PATH that answers every query with the
// given output and code, as a sandbox that denies the ps read.
func deniedPsEnv(t *testing.T, stderr string, code int) Env {
	t.Helper()
	dir := t.TempDir()
	if _, err := fakecli.Install(t, dir, "ps", []fakecli.Rule{{AnyArgs: true, Stderr: stderr, Code: code}}); err != nil {
		t.Fatal(err)
	}
	return Env{"PATH": dir, "SOHO_FAKECLI_CONFIG": dir}
}

// A failed by-pid ps read that names the denial reads as unknown with
// the permission-denied cause: the sandbox case of procs add. The pid is
// this test process, so the kernel probe (kill(pid, 0)) succeeds and the
// cause must come from the ps failure itself.
func TestReadProcFullCausePermissionDeniedPs(t *testing.T) {
	for i, stderr := range []string{
		"ps: /bin/ps: Operation not permitted\n",
		"ps: cannot read process table: Permission denied\n",
		"EPERM\n",
	} {
		t.Run("case "+strconv.Itoa(i), func(t *testing.T) {
			_, _, live, cause := ReadProcFullCause(os.Getpid(), deniedPsEnv(t, stderr, 1))
			if live != ProcUnknown {
				t.Fatalf("live=%v; a failed read is unknown, not gone", live)
			}
			if !errors.Is(cause, ErrProcReadPermissionDenied) {
				t.Fatalf("cause=%v; want the permission-denied cause", cause)
			}
		})
	}
}

// A ps failure that is not a denial (a bare non-zero exit) keeps the
// unknown read without the cause.
func TestReadProcFullCauseNotPermission(t *testing.T) {
	_, _, live, cause := ReadProcFullCause(os.Getpid(), deniedPsEnv(t, "", 9))
	if live != ProcUnknown || cause != nil {
		t.Fatalf("live=%v cause=%v; want unknown without a cause", live, cause)
	}
}

// pid 1 belongs to root, so for an ordinary user kill(pid, 0) returns
// EPERM: the pid exists, and that is not a denied table read. A ps that
// failed for another reason (a bare exit, or no ps at all) keeps the
// unknown read without the cause.
func TestReadProcFullCauseForeignPidIsNotADenial(t *testing.T) {
	if err := syscall.Kill(1, 0); !errors.Is(err, syscall.EPERM) {
		t.Skipf("kill(1, 0) = %v, not EPERM (running as root?): the foreign-pid case cannot be built here", err)
	}
	for name, env := range map[string]Env{
		"nonzero ps": deniedPsEnv(t, "", 9),
		"missing ps": {"PATH": t.TempDir()},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, live, cause := ReadProcFullCause(1, env)
			if live != ProcUnknown || cause != nil {
				t.Fatalf("live=%v cause=%v; want unknown without a cause", live, cause)
			}
		})
	}
}

// RunCli stores an exec denied with EACCES as the code "EACCES".
func TestProcReadDeniedEACCES(t *testing.T) {
	if !procReadDenied(RunResult{Error: "EACCES"}) {
		t.Fatal("an EACCES exec error is a denial")
	}
}

// A pid the kernel proves gone has no unreadable read to explain, even
// when ps printed a denial.
func TestDeniedCauseOnlyForUnknown(t *testing.T) {
	denied := RunResult{Stderr: "ps: Operation not permitted\n"}
	if cause := deniedCause(ProcGone, denied); cause != nil {
		t.Fatalf("gone: cause=%v; want none", cause)
	}
	if cause := deniedCause(ProcUnknown, denied); !errors.Is(cause, ErrProcReadPermissionDenied) {
		t.Fatalf("unknown: cause=%v; want the permission-denied cause", cause)
	}
}

// The running control: the real ps reads this test process as running,
// with no cause.
func TestReadProcFullCauseRunningControl(t *testing.T) {
	_, name, live, cause := ReadProcFullCause(os.Getpid(), Env{"PATH": os.Getenv("PATH")})
	if live != ProcRunning || cause != nil || name == "" {
		t.Fatalf("live=%v name=%q cause=%v; want running without a cause", live, name, cause)
	}
	// The wrapper discards the cause and keeps the three-way result.
	if started, name2, live2 := ReadProcFull(os.Getpid(), Env{"PATH": os.Getenv("PATH")}); live2 != ProcRunning || name2 != name {
		t.Fatalf("ReadProcFull: live=%v name=%q started=%q; want the same read", live2, name2, started)
	}
}
