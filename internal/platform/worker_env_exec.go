package platform

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WorkerEnvExecMode is the internal argv[1] marker of the native worker
// environment helper. It is handled by cmd/soho before configuration or
// state, never listed in the command help, and never a user-facing command.
const WorkerEnvExecMode = "--soho-worker-env-exec"

// WorkerEnvExecSupported reports whether this build can replace the process
// image in place. It is fixed per platform, so the Core transport refuses
// environment removal where no in-process exec exists.
func WorkerEnvExecSupported() bool { return workerEnvExecSupported }

// WorkerEnvExecMaxNames is the number of removal names the helper accepts.
const WorkerEnvExecMaxNames = 16

// ErrWorkerEnvExecMalformed marks an unusable helper invocation. The text is
// fixed: no environment name, value or path is echoed.
var ErrWorkerEnvExecMalformed = errors.New("worker env exec: malformed invocation")

// WorkerExecutable resolves the running soho executable to an absolute path
// and verifies it is an executable regular file, so a terminal can start the
// helper even after the worker's PATH is filtered.
func WorkerExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("worker env exec: resolve executable: %w", err)
	}
	abs, err := filepath.Abs(exe)
	if err != nil {
		return "", fmt.Errorf("worker env exec: absolute executable: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("worker env exec: stat executable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("worker env exec: executable is not a regular file")
	}
	if info.Mode().Perm()&0o111 == 0 {
		return "", errors.New("worker env exec: executable is not executable")
	}
	return abs, nil
}

// WorkerEnvExecArgv builds the helper's arguments after argv[1]: the removal
// names, the fixed delimiter, then the worker program and its argv verbatim.
func WorkerEnvExecArgv(names []string, program string, args []string) []string {
	argv := make([]string, 0, len(names)+3+len(args))
	argv = append(argv, names...)
	argv = append(argv, "--", program)
	return append(argv, args...)
}

// parseWorkerEnvExecArgs splits the helper arguments at the first fixed
// delimiter and validates the portable names and the absolute program.
func parseWorkerEnvExecArgs(args []string) ([]string, string, []string, error) {
	delimiter := -1
	for i, arg := range args {
		if arg == "--" {
			delimiter = i
			break
		}
	}
	if delimiter < 0 || delimiter+1 >= len(args) {
		return nil, "", nil, ErrWorkerEnvExecMalformed
	}
	names := args[:delimiter]
	if len(names) > WorkerEnvExecMaxNames {
		return nil, "", nil, ErrWorkerEnvExecMalformed
	}
	for _, name := range names {
		if WorkerEnvDropNameProblem(name) != WorkerEnvDropNameOK {
			return nil, "", nil, ErrWorkerEnvExecMalformed
		}
	}
	program := args[delimiter+1]
	if !filepath.IsAbs(program) {
		return nil, "", nil, ErrWorkerEnvExecMalformed
	}
	return names, program, args[delimiter+2:], nil
}

// WorkerEnvDropNameProblem classifies one requested removal name. The
// transport's preflight and the private helper share this one predicate, so a
// name accepted before terminal.run can never fail inside the helper.
type WorkerEnvDropName uint8

const (
	// WorkerEnvDropNameOK is a portable name that may be removed.
	WorkerEnvDropNameOK WorkerEnvDropName = iota
	// WorkerEnvDropNameInvalid is empty, non-portable or not an identifier.
	WorkerEnvDropNameInvalid
	// WorkerEnvDropNameReserved names Soho's own routing variables.
	WorkerEnvDropNameReserved
)

// WorkerEnvDropNameProblem reports the eligibility of one removal name: a
// portable identifier without a case-insensitive SOHO_, HC_ or HERDR_
// reservation. There is no length cap beyond that.
func WorkerEnvDropNameProblem(name string) WorkerEnvDropName {
	if name == "" {
		return WorkerEnvDropNameInvalid
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c == '_':
		case c >= 'A' && c <= 'Z':
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
			if i == 0 {
				return WorkerEnvDropNameInvalid
			}
		default:
			return WorkerEnvDropNameInvalid
		}
	}
	upper := strings.ToUpper(name)
	if strings.HasPrefix(upper, "SOHO_") || strings.HasPrefix(upper, "HC_") || strings.HasPrefix(upper, "HERDR_") {
		return WorkerEnvDropNameReserved
	}
	return WorkerEnvDropNameOK
}

// filteredWorkerEnv returns the process environment without the named
// entries, preserving every other entry and its original order and bytes.
func filteredWorkerEnv(drop map[string]bool) []string {
	environ := os.Environ()
	filtered := make([]string, 0, len(environ))
	for _, entry := range environ {
		if name, _, ok := strings.Cut(entry, "="); ok && drop[name] {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}
