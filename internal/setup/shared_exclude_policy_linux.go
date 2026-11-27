//go:build linux

package setup

import (
	"bytes"
	"errors"
	"os"
	"syscall"
)

// copySharedExcludeAccessPolicy carries the live file's extended attributes,
// including a POSIX ACL, onto the staged descriptor before publication, and
// removes staged-only entries that would broaden access. No interpreter or
// CLI is involved; unsupported filesystems with no xattrs are a no-op.
func copySharedExcludeAccessPolicy(source, staged *os.File) error {
	sourcePath, stagedPath := source.Name(), staged.Name()
	srcNames, err := listSharedXattrs(sourcePath)
	if err != nil {
		return err
	}
	stagedNames, err := listSharedXattrs(stagedPath)
	if err != nil {
		return err
	}
	keep := make(map[string]bool, len(srcNames))
	for _, name := range srcNames {
		keep[name] = true
	}
	for _, name := range stagedNames {
		if keep[name] {
			continue
		}
		if err := syscall.Removexattr(stagedPath, name); err != nil && !errors.Is(err, syscall.ENODATA) {
			return err
		}
	}
	for _, name := range srcNames {
		size, err := syscall.Getxattr(sourcePath, name, nil)
		if err != nil {
			return err
		}
		value := make([]byte, size)
		got, err := syscall.Getxattr(sourcePath, name, value)
		if err != nil {
			return err
		}
		if err := syscall.Setxattr(stagedPath, name, value[:got], 0); err != nil {
			return err
		}
	}
	return nil
}

func listSharedXattrs(path string) ([]string, error) {
	size, err := syscall.Listxattr(path, nil)
	if err != nil {
		if errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EOPNOTSUPP) {
			return nil, nil
		}
		return nil, err
	}
	if size == 0 {
		return nil, nil
	}
	buf := make([]byte, size)
	n, err := syscall.Listxattr(path, buf)
	if err != nil {
		return nil, err
	}
	names := []string{}
	for rest := buf[:n]; len(rest) > 0; {
		end := bytes.IndexByte(rest, 0)
		if end < 0 {
			break
		}
		names = append(names, string(rest[:end]))
		rest = rest[end+1:]
	}
	return names, nil
}
