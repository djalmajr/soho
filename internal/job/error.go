package job

import (
	"strconv"
	"strings"
)

const (
	// ExitUsage is a bad id, an invalid brief, or a refused transition.
	ExitUsage = 2
	// ExitNotFound is an unknown job id.
	ExitNotFound = 3
	// ExitBlocked is the blocked status exit used by a later CLI slice.
	ExitBlocked = 7
	// ExitTimeout is a wait or job budget that expired.
	ExitTimeout = 9
	// ExitFailed is the failed status exit used by a later CLI slice.
	ExitFailed = 19
	// ExitBriefConflict is an id reused with a different brief hash.
	ExitBriefConflict = 20
	// ExitCanceled is the canceled status exit used by a later CLI slice.
	ExitCanceled = 21
	// ExitPrepare is a preparation failure exit used by a later slice.
	ExitPrepare = 22
	// ExitUnacked is close with a decision still above decisions_acked_seq.
	ExitUnacked = 24
	// MaxBriefBytes is the start stdin cap (256 KiB).
	MaxBriefBytes = 256 << 10
)

// ExitError is a contract exit code returned to the CLI slice.
type ExitError struct {
	Code int
	Msg  string
	Seqs []int
	// Detail is diagnostic context kept out of Msg (and so out of the
	// events and the report): for a failed subprocess step, its exit and
	// the last lines of its stderr. Only the job's friction log records it.
	Detail string
}

func (e *ExitError) Error() string {
	if e == nil {
		return ""
	}
	if len(e.Seqs) == 0 {
		return e.Msg
	}
	parts := make([]string, len(e.Seqs))
	for i, n := range e.Seqs {
		parts[i] = strconv.Itoa(n)
	}
	if e.Msg == "" {
		return strings.Join(parts, ", ")
	}
	return e.Msg + ": " + strings.Join(parts, ", ")
}

func errUsage(msg string) error {
	return &ExitError{Code: ExitUsage, Msg: msg}
}

func isCode(err error, code int) bool {
	exit, ok := err.(*ExitError)
	return ok && exit != nil && exit.Code == code
}
