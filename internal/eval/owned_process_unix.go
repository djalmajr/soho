//go:build !windows

package eval

import (
	"context"
	"fmt"
	"os"
)

// ownedOutputAppendFlag keeps the runner's and the child's separate file
// offsets from overwriting each other: every write lands at the end.
const ownedOutputAppendFlag = os.O_APPEND

// runOwnedProcess runs the owned process group (the Unix half of
// RunOwnedProcess). The semantics mirror runOwnedGoProcess: the whole tree
// is in a new process group, cancellation of ctx kills that entire group
// with SIGKILL, and a green run re-kills the group and proves it empty —
// cmd.Wait reaps only the leader, and a detached grandchild (a
// backgrounded worker re-parented out of the tree) would otherwise
// outlive the run.
func runOwnedProcess(ctx context.Context, executable string, args []string, opts OwnedProcessOptions) (OwnedProcessResult, error) {
	if opts.Output == nil {
		return OwnedProcessResult{}, fmt.Errorf("runOwnedProcess: Output is required")
	}
	// The child's stdout and stderr are the same destination: the bytes
	// land there in the order the child writes them, and the runner never
	// buffers them.
	group := ownedGroupOptions{
		dir: opts.Dir, stdout: opts.Output, stderr: opts.Output,
		verify: verifyOwnedGroup,
	}
	if len(opts.Env) > 0 {
		group.env = opts.Env
	}
	if opts.WaitDelay > 0 {
		group.waitDelay = opts.WaitDelay
	}
	res, err, verifyErr := runOwnedGroup(ctx, executable, args, group)
	res.CleanupErr = verifyErr
	return res, err
}

// verifyOwnedGroup is the group-gone proof; a test replaces it to make the
// proof fail deterministically.
var verifyOwnedGroup = verifyOwnedGroupGone
