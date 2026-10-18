//go:build !windows

package platform

import (
	"strconv"
	"strings"
)

// ReadProcessRSS reads the pid's resident set size in bytes with
// `ps -o rss= -p <pid>` (ps prints KiB), with the C-locale env the other
// by-pid ps reads already use. An unreadable read (a missing or failed ps,
// a nonzero exit, or a non-numeric output) reads as not readable; the
// process's command line is never read.
func ReadProcessRSS(pid int, env Env) (int64, bool) {
	r := RunCli("ps", []string{"-o", "rss=", "-p", strconv.Itoa(pid)}, RunOptions{Env: procLocale(env), TimeoutMs: 5000})
	if r.NotFound || r.TimedOut || r.Error != "" || r.Status == nil || *r.Status != 0 {
		return 0, false
	}
	kiB, err := strconv.Atoi(strings.TrimSpace(r.Stdout))
	if err != nil || kiB < 0 {
		return 0, false
	}
	return int64(kiB) * 1024, true
}
