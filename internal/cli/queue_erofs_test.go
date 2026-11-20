package cli

import (
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/djalmajr/soho/internal/core"
)

// A Linux sandbox that binds the state dir read-only refuses the registry
// lock with EROFS, which Go's fs.ErrPermission does not match: copies add
// and procs add queue the registration as they do for EACCES/EPERM.
func TestRegistrationQueuesOnAReadOnlyFileSystem(t *testing.T) {
	t.Run("copies add", func(t *testing.T) {
		f := newCopiesFixture(t, nil)
		sd := f.stateDir(t)
		copyDir := filepath.Join(f.tmp, "erofs-copy")
		if err := os.MkdirAll(copyDir, 0o700); err != nil {
			t.Fatal(err)
		}
		refuseCopiesRegistry(t, &fs.PathError{Op: "open", Path: filepath.Join(sd, "copies.tsv.lock"), Err: syscall.EROFS})
		code, out, errOut := f.run(t, f.env, "copies", "add", copyDir)
		queue := core.PendingCopiesFile(f.env, "ws")
		want := "copies add: the state dir " + sd + " is not writable from here; the registration is queued for the orchestrator in " + queue + "\n"
		wantOut := "copies add: registered " + copyDir + " (owner -)\n"
		if code != 0 || out != wantOut || errOut != want {
			t.Fatalf("code=%d out=%q err=%q; want 0, %q and %q", code, out, errOut, wantOut, want)
		}
		if raw := readQueue(t, queue); !containsLine(raw, copyDir) {
			t.Fatalf("queue=%q; want the copy queued", raw)
		}
	})
	t.Run("procs add", func(t *testing.T) {
		f := newProcsFixture(t)
		pid := f.sleeper(t)
		refuseProcsRegistry(t, &fs.PathError{Op: "open", Path: filepath.Join(f.state, "procs.tsv.lock"), Err: syscall.EROFS})
		code, out, errOut := f.run(t, "procs", "add", strconv.Itoa(pid))
		queue := core.PendingProcsFile(f.env, "ws")
		want := "procs add: the state dir " + f.state + " is not writable from here; the registration is queued for the orchestrator in " + queue + "\n"
		if code != 0 || out != "" || errOut != want {
			t.Fatalf("code=%d out=%q err=%q; want 0 and %q", code, out, errOut, want)
		}
	})
}

func containsLine(raw, field string) bool {
	for _, line := range strings.Split(raw, "\n") {
		for _, f := range strings.Split(line, "\t") {
			if f == field {
				return true
			}
		}
	}
	return false
}
