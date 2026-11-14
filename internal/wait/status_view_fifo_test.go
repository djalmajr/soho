//go:build !windows

package wait

import (
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/backend"
	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// fifoOpenReceipt is the control-only handshake of the deliberate blocking
// FIFO control: the control's own read-side open reports its actual result,
// so the release writer is never closed before that open returned. A nil
// receipt (every other consumer) makes report a no-op: the ordinary helper
// cannot tell the control that a reader is open.
type fifoOpenReceipt struct {
	opened chan error
}

func newFIFOOpenReceipt() *fifoOpenReceipt {
	return &fifoOpenReceipt{opened: make(chan error, 1)}
}

// report publishes the outcome of the control's real read-side open, after it
// returned and before the control reads: only this receipt releases the
// writer, never a sleep or a guessed scheduling delay.
func (r *fifoOpenReceipt) report(err error) {
	if r == nil {
		return
	}
	r.opened <- err
}

// observeFIFOConsumer keeps the existing 250ms consumer deadline and one
// shared 1s release bound, which covers both the control's actual open
// receipt and the consumer's completion after the release. A successful
// nonblocking writer proves a waiting FIFO reader; ENXIO instead identifies
// latency elsewhere in the consumer. The owned writer stays open until
// receipt reports that the control's read-side open returned, because a
// writer closed before that open completes leaves the reader blocked and its
// read without EOF; the receipt then lets the writer close so the pending
// read receives EOF and the consumer joins. Either missed deadline still
// fails the consumer assertion.
func observeFIFOConsumer(t *testing.T, path string, receipt *fifoOpenReceipt, consume func() AgentStatusView) (view AgentStatusView, delayed, readerWaiting bool) {
	t.Helper()
	done := make(chan AgentStatusView, 1)
	go func() { done <- consume() }()
	select {
	case view = <-done:
		return view, false, false
	case <-time.After(250 * time.Millisecond):
	}
	release := time.After(time.Second)
	writer, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0)
	readerWaiting = err == nil
	if writer != nil {
		if receipt != nil {
			select {
			case openErr := <-receipt.opened:
				if openErr != nil {
					t.Errorf("the blocking control's owned FIFO read-side open failed: %v", openErr)
				}
			case <-release:
				if err := writer.Close(); err != nil {
					t.Errorf("close the owned FIFO release writer: %v", err)
				}
				t.Fatalf("the blocking control's owned FIFO read-side open did not return within the shared release bound")
			}
		}
		if err := writer.Close(); err != nil {
			t.Errorf("close the owned FIFO release writer: %v", err)
		}
	}
	select {
	case view = <-done:
		return view, true, readerWaiting
	case <-release:
		t.Fatalf("FIFO consumer did not join within the existing release bound; waiting reader=%v writer error=%v", readerWaiting, err)
		return AgentStatusView{}, true, readerWaiting
	}
}

func inspectFIFOPromptly(t *testing.T, path string, consume func() AgentStatusView) AgentStatusView {
	t.Helper()
	view, delayed, readerWaiting := observeFIFOConsumer(t, path, nil, consume)
	if delayed {
		t.Errorf("consumer exceeded 250ms; a FIFO reader was waiting=%v (false means latency outside the FIFO acquisition)", readerWaiting)
	}
	return view
}

func TestStatusViewFIFOBlockingControl(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owned-fifo")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	view, delayed, waiting := observeFIFOConsumer(t, path, nil, func() AgentStatusView {
		_, err := platform.ReadRegularFile(path)
		if err == nil {
			return AgentStatusView{State: "accepted"}
		}
		return AgentStatusView{State: "refused"}
	})
	if delayed || waiting || view.State != "refused" {
		t.Fatalf("nonblocking control=%+v delayed=%v reader=%v", view, delayed, waiting)
	}
	// The blocking positive control owns its reader: it reports the actual
	// result of os.Open, reads until the released writer's EOF, and closes the
	// reader before returning joined, so the join is the real consumer
	// completion rather than a guessed delay.
	receipt := newFIFOOpenReceipt()
	view, delayed, waiting = observeFIFOConsumer(t, path, receipt, func() AgentStatusView {
		f, err := os.Open(path) // deliberately blocking owned FIFO control
		receipt.report(err)
		if err != nil {
			return AgentStatusView{State: "read-error"}
		}
		defer func() { _ = f.Close() }()
		if _, err := io.ReadAll(f); err != nil {
			return AgentStatusView{State: "read-error"}
		}
		return AgentStatusView{State: "joined"}
	})
	if !delayed || !waiting || view.State != "joined" {
		t.Fatalf("blocking control=%+v delayed=%v reader=%v", view, delayed, waiting)
	}
	t.Log("nonblocking refusal met 250ms; blocking FIFO crossed 250ms, writer held until the reader's actual open receipt and closed, reader reached EOF and joined")
}

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
	view := inspectFIFOPromptly(t, p, func() AgentStatusView {
		return InspectAgentStatus(AgentStatusRequest{
			StateDir: f.sd, SessionID: "ws", Agent: "worker", Cwd: f.cwd,
			Config: f.ctx, Env: f.env, Services: backend.New(f.env),
		})
	})
	if view.State != "compact-unknown" {
		t.Fatalf("FIFO marker view=%+v, want the existing compact-unknown inspection status", view)
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
	view := inspectFIFOPromptly(t, p, func() AgentStatusView {
		return InspectAgentStatus(AgentStatusRequest{
			StateDir: f.sd, SessionID: "ws", Agent: "worker", Cwd: f.cwd,
			Config: f.ctx, Env: f.env, Services: backend.New(f.env), Source: src,
		})
	})
	if view.State != "unknown" || view.TerminalState != "idle" {
		t.Fatalf("FIFO marker view=%+v, want the conservative unknown over the idle probe (never the absence)", view)
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
