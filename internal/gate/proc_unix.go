//go:build !windows

package gate

import (
	"fmt"
	"os/exec"
	"strconv"
	"syscall"
	"unsafe"
)

// chmodMayFail is false on Unix: the mode of every artifact must be 0600.
const chmodMayFail = false

// prepare gives the child a process group of its own (PGID == PID), so that
// the bound can end the program and everything it started in that group.
func prepare(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func signalTerminate(cmd *exec.Cmd) { signalGroup(cmd, syscall.SIGTERM) }

func signalKill(cmd *exec.Cmd) { signalGroup(cmd, syscall.SIGKILL) }

var groupSignal = syscall.Kill

// observeExit waits without reaping. The unreaped leader reserves its PID
// through the final group signal, including when it exited before SIGTERM.
func observeExit(cmd *exec.Cmd) error { return observeExitOptions(cmd, 0) }

func reap(cmd *exec.Cmd, observationErr error) error {
	waitErr := cmd.Wait()
	if observationErr != nil {
		return fmt.Errorf("observe exit: %w (wait: %v)", observationErr, waitErr)
	}
	return waitErr
}

func observeExitOptions(cmd *exec.Cmd, options int) error {
	// siginfo_t is at most 128 bytes on Linux and Darwin; no fields are read.
	var info [16]uint64
	for {
		_, _, err := syscall.Syscall6(syscall.SYS_WAITID, 1 /* P_PID */, uintptr(cmd.Process.Pid), uintptr(unsafe.Pointer(&info[0])), uintptr(syscall.WEXITED|syscall.WNOWAIT|options), 0, 0)
		if err == syscall.EINTR {
			continue
		}
		if err != 0 {
			return err
		}
		return nil
	}
}

// signalGroup signals only the group this command created for the process it
// started. Never signal -1 (every process) or 0 (the caller's own group).
func signalGroup(cmd *exec.Cmd, sig syscall.Signal) {
	if cmd.Process == nil || cmd.Process.Pid <= 1 {
		return
	}
	_ = groupSignal(-cmd.Process.Pid, sig)
}

func signalName(state interface{ Sys() any }) string {
	status, ok := state.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() {
		return ""
	}
	sig := status.Signal()
	if name, known := signalNames[sig]; known {
		return name
	}
	return "SIG" + strconv.Itoa(int(sig))
}

var signalNames = map[syscall.Signal]string{
	syscall.SIGHUP: "SIGHUP", syscall.SIGINT: "SIGINT", syscall.SIGQUIT: "SIGQUIT",
	syscall.SIGILL: "SIGILL", syscall.SIGTRAP: "SIGTRAP", syscall.SIGABRT: "SIGABRT",
	syscall.SIGBUS: "SIGBUS", syscall.SIGFPE: "SIGFPE", syscall.SIGKILL: "SIGKILL",
	syscall.SIGUSR1: "SIGUSR1", syscall.SIGSEGV: "SIGSEGV", syscall.SIGUSR2: "SIGUSR2",
	syscall.SIGPIPE: "SIGPIPE", syscall.SIGALRM: "SIGALRM", syscall.SIGTERM: "SIGTERM",
	syscall.SIGSTOP: "SIGSTOP", syscall.SIGTSTP: "SIGTSTP", syscall.SIGCONT: "SIGCONT",
	syscall.SIGTTIN: "SIGTTIN", syscall.SIGTTOU: "SIGTTOU", syscall.SIGXCPU: "SIGXCPU",
	syscall.SIGXFSZ: "SIGXFSZ", syscall.SIGVTALRM: "SIGVTALRM", syscall.SIGPROF: "SIGPROF",
	syscall.SIGSYS: "SIGSYS",
}
