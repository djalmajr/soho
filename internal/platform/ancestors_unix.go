//go:build !windows

package platform

import (
	"os"
	"strconv"
	"strings"
)

// ancestorProvider returns the per-hop parent read: one ps invocation
// per hop (each hop is a different pid, so no single call can answer the
// whole walk).
func ancestorProvider(env Env) (func(pid int) (int, error), error) {
	return func(pid int) (int, error) { return processParent(pid, env) }, nil
}

// processParent reads the parent pid of one pid with
// `ps -o ppid= -p <pid>` (LC_ALL=C, a 5 s ceiling, through the platform
// PATH): only the parent field, never a command line. When ps is not
// available at all, the fallback is the ppid field of /proc/<pid>/stat
// (Linux; on macOS the /proc read fails back into the same error).
func processParent(pid int, env Env) (int, error) {
	r := RunCli("ps", []string{"-o", "ppid=", "-p", strconv.Itoa(pid)}, RunOptions{Env: procLocale(env), TimeoutMs: 5000})
	if r.NotFound {
		return procStatParent(pid)
	}
	if r.TimedOut || r.Status == nil || *r.Status != 0 {
		return 0, errAncestorRead
	}
	fields := strings.Fields(r.Stdout)
	if len(fields) != 1 {
		return 0, errAncestorRead
	}
	parent, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, errAncestorRead
	}
	return parent, nil
}

// procStatParent reads the ppid field (the fourth field of the file,
// after the parenthesised command name, which may contain spaces) of
// /proc/<pid>/stat.
func procStatParent(pid int) (int, error) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, errAncestorRead
	}
	text := string(data)
	i := strings.LastIndexByte(text, ')')
	if i < 0 || i+2 >= len(text) {
		return 0, errAncestorRead
	}
	fields := strings.Fields(text[i+1:])
	// After the closing paren: state, ppid, pgrp, session, ...
	if len(fields) < 2 {
		return 0, errAncestorRead
	}
	parent, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, errAncestorRead
	}
	return parent, nil
}
