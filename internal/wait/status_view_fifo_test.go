//go:build !windows

package wait

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/backend"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// snapSkip is treeSnapshot with one path recorded by metadata only: the
// FIFO itself is never read (a read of it blocks), so the assertion keeps
// the byte-level comparison for everything else and the kind check for the
// FIFO.
func snapSkip(t *testing.T, skip string, roots ...string) map[string]snapEntry {
	t.Helper()
	snap := map[string]snapEntry{}
	for _, root := range roots {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			if info.IsDir() {
				return nil
			}
			if path == skip {
				snap[path] = snapEntry{size: info.Size(), mod: info.ModTime(), mode: info.Mode()}
				return nil
			}
			data, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			snap[path] = snapEntry{size: info.Size(), mod: info.ModTime(), mode: info.Mode(), data: data}
			return nil
		})
		if err != nil {
			t.Fatalf("snapshot of %s: %v", root, err)
		}
	}
	return snap
}

// F4 (review-2): a FIFO at the compact marker must be refused by the
// inspection's bounded nonblocking read without waiting for a writer — the
// view promptly returns the existing compact-unknown inspection status
// silently, with the target and the fixture unchanged and no helper writer
// needed to unblock it. The owned writer phase below only proves the
// refusal did not consume the FIFO: had the reader blocked, it would join
// after the release and fail.
func TestStatusViewCompactFIFO(t *testing.T) {
	f := newStatusViewFixture(t, "worker\tp1\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n",
		[]fakecli.Rule{{AnyArgs: true}})
	p := core.CompactPendingPath(f.sd, "worker")
	before := f.fixtureBefore(t)
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan AgentStatusView, 1)
	go func() {
		done <- InspectAgentStatus(AgentStatusRequest{
			StateDir: f.sd, SessionID: "ws", Agent: "worker", Cwd: f.cwd,
			Config: f.ctx, Env: f.env, Services: backend.New(f.env),
		})
	}()
	select {
	case view := <-done:
		if view.State != "compact-unknown" {
			t.Fatalf("FIFO marker view=%+v, want the existing compact-unknown inspection status", view)
		}
	case <-time.After(250 * time.Millisecond):
		// The reader is still blocked: release it with an owned writer and
		// show that it was waiting, not refusing.
		w, err := os.OpenFile(p, os.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte("malformed")); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		select {
		case view := <-done:
			t.Fatalf("InspectAgentStatus blocked on the nonregular compact marker until an owned writer released it; joined result=%+v", view)
		case <-time.After(time.Second):
			t.Fatal("the owned reader failed to join after the release")
		}
	}
	info, err := os.Lstat(p)
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("the FIFO marker is gone or no longer a FIFO after the inspection: %v %v", info, err)
	}
	// The before snapshot predates the FIFO: compare everything else by
	// bytes and the FIFO by its kind.
	after := snapSkip(t, p, f.state, f.cwd, f.tmp, f.env["HOME"])
	afterCopy := map[string]snapEntry{}
	for path, e := range after {
		if path != p {
			afterCopy[path] = e
		}
	}
	assertTreeUnchanged(t, "FIFO compact marker inspection", before, afterCopy)
}

// TestStatusViewSourceFIFOMarker: a FIFO standing where the required
// not-received marker should be is a nonregular entry: the inspection
// refuses it through the retained source without reading it (a read of
// the FIFO would block the test) and renders the conservative unknown
// with the probe still running, never the absence the marker would
// otherwise read as (no-report-yet). The target and the fixture stay
// unchanged.
func TestStatusViewSourceFIFOMarker(t *testing.T) {
	f := newStatusViewFixture(t, "worker\tp1\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n",
		[]fakecli.Rule{
			{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle","state_change_seq":7}}}`},
			{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`},
		})
	src, err := core.OpenSessionSource(f.state, "ws", f.env["TMPDIR"])
	if err != nil {
		t.Fatalf("OpenSessionSource: %v", err)
	}
	defer src.Close()
	p := filepath.Join(f.sd, "wait", "worker.not-received")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	// The before snapshot predates the FIFO (a read of the FIFO blocks): it
	// compares everything by bytes and the FIFO by its kind.
	before := f.fixtureBefore(t)
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Skipf("the test user cannot create FIFOs: %v", err)
	}
	done := make(chan AgentStatusView, 1)
	go func() {
		done <- InspectAgentStatus(AgentStatusRequest{
			StateDir: f.sd, SessionID: "ws", Agent: "worker", Cwd: f.cwd,
			Config: f.ctx, Env: f.env, Services: backend.New(f.env), Source: src,
		})
	}()
	select {
	case view := <-done:
		if view.State != "unknown" || view.TerminalState != "idle" {
			t.Fatalf("FIFO marker view=%+v, want the conservative unknown over the idle probe (never the absence)", view)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("the inspection blocked on the FIFO marker (it must refuse the nonregular entry without reading it)")
	}
	info, err := os.Lstat(p)
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("the FIFO marker is gone or no longer a FIFO after the inspection: %v %v", info, err)
	}
	after := snapSkip(t, p, f.state, f.cwd, f.tmp, f.env["HOME"])
	afterCopy := map[string]snapEntry{}
	for path, e := range after {
		if path != p {
			afterCopy[path] = e
		}
	}
	assertTreeUnchanged(t, "FIFO required-marker inspection", before, afterCopy)
}
