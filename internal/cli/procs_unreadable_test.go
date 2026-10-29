package cli

import (
	"os"
	"strconv"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

// injectUnreadableReads swaps the by-pid identity read of procs add for
// the injected liveness and cause; the cleanup restores the real read
// (also when an assertion failed).
func injectUnreadableReads(t *testing.T, live platform.ProcLiveness, cause error) {
	t.Helper()
	old := readProcFull
	readProcFull = func(pid int, env platform.Env) (string, string, platform.ProcLiveness, error) {
		return "", "", live, cause
	}
	t.Cleanup(func() { readProcFull = old })
}

// DJA-588: an unreadable read the platform attributes to a permission
// denial exits 4, registers nothing, and appends the probable cause and
// the ways out; the hint carries the --agent name when it was passed.
func TestProcsAddUnreadablePermissionDenied(t *testing.T) {
	for _, tc := range []struct {
		name  string
		agent []string
		hint  string
	}{
		{"without --agent the hint keeps --agent <name>", nil, "--agent <name>"},
		{"with --agent the hint carries the name", []string{"--agent", "other"}, "--agent other"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newProcsFixture(t)
			pid := f.sleeper(t)
			injectUnreadableReads(t, platform.ProcUnknown, platform.ErrProcReadPermissionDenied)
			code, out, errOut := f.run(t, append([]string{"procs", "add", strconv.Itoa(pid)}, tc.agent...)...)
			if code != 4 || out != "" {
				t.Fatalf("code=%d out=%q; want 4 with no stdout", code, out)
			}
			want := "soho: procs add: the identity of pid " + strconv.Itoa(pid) +
				" is unreadable (the system state read failed and the kernel did not report it gone); not registered" +
				": the process table is not readable from this shell (a sandbox may deny it); run soho procs add outside the sandbox (an approved command), or ask the orchestrator to register the pid with " +
				tc.hint + "\n"
			if errOut != want {
				t.Fatalf("stderr=%q; want %q", errOut, want)
			}
			if _, err := os.Stat(f.registryPath()); !os.IsNotExist(err) {
				t.Fatalf("the registry exists after the denial: %v", err)
			}
			if rows := f.procsRows(t); len(rows) != 0 {
				t.Fatalf("registry rows = %v; nothing is registered on an unreadable read", rows)
			}
		})
	}
}

// A failure that is not a permission denial keeps today's text exactly,
// with the same exit and no registration.
func TestProcsAddUnreadableWithoutPermission(t *testing.T) {
	f := newProcsFixture(t)
	pid := f.sleeper(t)
	injectUnreadableReads(t, platform.ProcUnknown, nil)
	code, out, errOut := f.run(t, "procs", "add", strconv.Itoa(pid))
	if code != 4 || out != "" {
		t.Fatalf("code=%d out=%q; want 4 with no stdout", code, out)
	}
	want := "soho: procs add: the identity of pid " + strconv.Itoa(pid) +
		" is unreadable (the system state read failed and the kernel did not report it gone); not registered\n"
	if errOut != want {
		t.Fatalf("stderr=%q; want exactly today's text %q", errOut, want)
	}
	if rows := f.procsRows(t); len(rows) != 0 {
		t.Fatalf("registry rows = %v; nothing is registered on an unreadable read", rows)
	}
}
