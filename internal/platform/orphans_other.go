//go:build !darwin && !linux

package platform

func userOrphanProcesses() ([]OrphanProc, error) { return nil, ErrOrphansNotDetected }

func procCwd(pid int) (string, error) { return "", ErrOrphansNotDetected }
