//go:build !windows

package gate

import (
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

func helperPlatform(mode string, args []string) int {
	switch mode {
	case "tree":
		// args[0]: file receiving the grandchild's pid. The grandchild stays
		// in this process's group; both then sleep.
		return spawnGrandchild("sleep", args[0])
	case "tree-leader-exits":
		// The leader exits on SIGTERM while the grandchild ignores it.
		return spawnGrandchild("ignoreterm", args[0])
	case "ignoreterm":
		signal.Ignore(syscall.SIGTERM)
		time.Sleep(60 * time.Second)
		return 0
	case "self-kill":
		_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
		time.Sleep(10 * time.Second)
		return 0
	}
	return 99
}

func spawnGrandchild(mode, pidFile string) int {
	// If a failing test must kill its gate supervisor, this still-live group
	// leader owns the fallback signal. It never uses a saved, released PID.
	parent := os.Getppid()
	go func() {
		for os.Getppid() == parent {
			time.Sleep(20 * time.Millisecond)
		}
		if syscall.Getpgrp() == os.Getpid() {
			_ = syscall.Kill(-os.Getpid(), syscall.SIGKILL)
		}
	}()
	if err := publishPID(pidFile+".leader", os.Getpid()); err != nil {
		return 97
	}
	child := exec.Command(os.Args[0], helperFlag+mode)
	if err := child.Start(); err != nil {
		return 98
	}
	if err := publishPID(pidFile, child.Process.Pid); err != nil {
		_ = child.Process.Kill()
		_ = child.Wait()
		return 97
	}
	time.Sleep(60 * time.Second)
	return 0
}

func publishPID(path string, pid int) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".pid-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, writeErr := f.WriteString(strconv.Itoa(pid))
	closeErr := f.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
