//go:build !darwin && !linux && !windows

package platform

// procExec has no single-pid executable and argument read here.
func procExec(pid int) (string, []string, error) {
	return "", nil, errProcExecUnsupported
}

func procExecutable(pid int) (string, error) { return "", errProcExecUnsupported }

func procExecArguments(id uintptr) ([]string, error) { return nil, errProcExecUnsupported }
