//go:build !windows && !linux && !darwin

package setup

import "os"

// copySharedExcludeAccessPolicy has no Go standard-library ACL/xattr API on
// this Unix platform. Mode, owner and group are copied by the caller; a
// file-specific ACL is therefore not described here. This is an explicit
// residual limitation, not a claim that such ACLs are preserved.
func copySharedExcludeAccessPolicy(_, _ *os.File) error { return nil }
