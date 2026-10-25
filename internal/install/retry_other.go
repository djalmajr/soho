//go:build !windows

package install

func isTransientRenameError(error) bool { return false }
