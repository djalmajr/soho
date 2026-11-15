package platform

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"

	testutil "github.com/djalmajr/soho/internal/testutil/testenv"
)

// procExecReady is the line TestProcExecHelper writes to stdout once it is
// running, before it blocks on stdin.
const procExecReady = "ready"

// TestProcExec reads a child's executable and exact arguments, and fails
// for a pid that exited.
//
// The child announces that it is running (procExecReady on its stdout) and
// the test reads that line before the first ProcExec. This removes an
// unsynchronized read: ProcExec used to be called right after Start
// returned, with nothing telling the test that the child had finished
// starting. That is not a diagnosis. Ubuntu CI failed this test with
// "malformed process arguments" before the handshake existed, and the
// cause of that failure is not proved. If ProcExec still fails, the
// failure message carries what /proc held for the child at that moment on
// Linux (the raw cmdline and the state field of stat), captured before the
// child is released and reaped, so the next run can tell an empty read from
// an unterminated one.
func TestProcExec(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self, "-test.run=^TestProcExecHelper$", "--", "job", "supervise", "--id", "job 1")
	cmd.Env = append(testutil.CleanEnv(t), "PROC_EXEC_HELPER=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	t.Cleanup(func() {
		if !waited {
			_ = stdin.Close()
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	pid := cmd.Process.Pid
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != procExecReady+"\n" {
		t.Fatalf("the child did not announce that it runs: line %q, err %v", line, err)
	}
	exe, argv, readErr := ProcExec(pid)
	diag := ""
	if readErr != nil {
		diag = procExecDiagnostic(pid)
	}
	_ = stdin.Close()
	waited = true
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if readErr != nil {
		t.Fatalf("ProcExec(%d): %v%s", pid, readErr, diag)
	}
	want := []string{"-test.run=^TestProcExecHelper$", "--", "job", "supervise", "--id", "job 1"}
	if len(argv) != len(want)+1 {
		t.Fatalf("argv = %q, want %q after argv[0]", argv, want)
	}
	for i, arg := range want {
		if argv[i+1] != arg {
			t.Fatalf("argv = %q, want %q after argv[0]", argv, want)
		}
	}
	a, errA := os.Stat(exe)
	b, errB := os.Stat(self)
	if errA != nil || errB != nil || !os.SameFile(a, b) {
		t.Fatalf("exe = %q, want the file %q (%v %v)", exe, self, errA, errB)
	}
	if _, _, err := ProcExec(pid); err == nil {
		t.Fatalf("ProcExec of the exited pid %d succeeded", pid)
	}
	if _, _, err := ProcExec(0); err == nil {
		t.Fatal("ProcExec(0) succeeded")
	}
}

// procExecDiagnostic describes, for the failure message of TestProcExec,
// what Linux holds for pid: the raw /proc/<pid>/cmdline (quoted, at most
// 4096 bytes shown, with its real length) and the state field of
// /proc/<pid>/stat. It is empty on other platforms, and it only reads.
func procExecDiagnostic(pid int) string {
	if runtime.GOOS != "linux" {
		return ""
	}
	dir := "/proc/" + strconv.Itoa(pid)
	var msg strings.Builder
	cmdline, err := os.ReadFile(dir + "/cmdline")
	shown := cmdline
	if len(shown) > 4096 {
		shown = shown[:4096]
	}
	fmt.Fprintf(&msg, "; diagnostic: /proc cmdline = %q (len %d, read error %v)", shown, len(cmdline), err)
	stat, err := os.ReadFile(dir + "/stat")
	state := ""
	if i := bytes.LastIndexByte(stat, ')'); i >= 0 {
		if fields := strings.Fields(string(stat[i+1:])); len(fields) > 0 {
			state = fields[0]
		}
	}
	fmt.Fprintf(&msg, ", stat state = %q (read error %v)", state, err)
	return msg.String()
}

// TestProcExecHelper announces itself and blocks on stdin when run as
// TestProcExec's child.
func TestProcExecHelper(t *testing.T) {
	if os.Getenv("PROC_EXEC_HELPER") != "1" {
		return
	}
	_, _ = os.Stdout.WriteString(procExecReady + "\n")
	buf := make([]byte, 1)
	_, _ = os.Stdin.Read(buf)
}
