//go:build !windows

package wait

// SOH-457: the non-settling wait the wake child runs turns a signal that
// killed it into one interrupted line, with the exact command to rearm. The
// helper is the real CmdWait --observe entry point in a child test process:
// the parent sends the signal only after the fake hc sees the first probe,
// which is after the observe path installed its handler.

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// TestWakeObserveSignalHelper runs only as the child of the test below.
func TestWakeObserveSignalHelper(t *testing.T) {
	if os.Getenv("SOHO_WAKE_SIGNAL_HELPER") != "1" {
		return
	}
	f := newHcWakeFixture(t, "1", "none")
	if bin := os.Getenv("SOHO_WAKE_SIGNAL_BIN"); bin != "" {
		if err := os.MkdirAll(bin, 0o700); err != nil {
			t.Fatal(err)
		}
		f.bin = bin
		f.env["PATH"] = bin + string(os.PathListSeparator) + testutil.HostPath()
		f.env["SOHO_FAKECLI_CONFIG"] = bin
	}
	if err := os.WriteFile(filepath.Join(f.sd, "agents.tsv"), []byte("w1\tp0a\tclaude\tnorole\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The hostile Core name must never reach the interrupted line: the
	// rearm uses only the validated agent from the caller's argv.
	probe := `{"address":"w1","name":"evil-from-core","state":"live","status":{"state":"working"}}`
	f.installHc(t, fakecli.Rule{
		Argv:     []string{"get", "w1", "--json"},
		Stdout:   probe,
		WaitFile: os.Getenv("SOHO_WAKE_SIGNAL_READY"),
		Delay:    60000,
	})
	os.Exit(CmdWait([]string{"w1", "--observe", "--timeout", "600000"}, f.ctx, f.env, f.base))
}

func TestObserveWaitInterruptedBySignal(t *testing.T) {
	for _, tc := range []struct {
		name   string
		signal syscall.Signal
		code   int
	}{
		{"SIGHUP", syscall.SIGHUP, 129},
		{"SIGTERM", syscall.SIGTERM, 143},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			bin := filepath.Join(base, "bin")
			ready := filepath.Join(base, "ready")
			cmd := exec.Command(os.Args[0], "-test.run=^TestWakeObserveSignalHelper$")
			cmd.Env = []string{
				"PATH=" + bin + string(os.PathListSeparator) + testutil.HostPath(),
				"HOME=" + base, "USERPROFILE=" + base, "TMPDIR=" + base, "XDG_CONFIG_HOME=" + base,
				"SOHO_WAKE_SIGNAL_HELPER=1", "SOHO_WAKE_SIGNAL_BIN=" + bin, "SOHO_WAKE_SIGNAL_READY=" + ready,
			}
			var out, errOut bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &errOut
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			stopped := false
			defer func() {
				if !stopped {
					_ = cmd.Process.Kill()
					_, _ = cmd.Process.Wait()
				}
			}()
			// The fake writes its call line before it blocks on the ready
			// file, so seeing this probe proves the wait loop is running and
			// the handler is already installed.
			deadline := time.Now().Add(20 * time.Second)
			seen := false
			for time.Now().Before(deadline) {
				calls, err := fakecli.ReadCalls(filepath.Join(bin, "hc.calls.jsonl"))
				if err == nil {
					for _, call := range calls {
						if len(call.Argv) == 3 && call.Argv[0] == "get" && call.Argv[1] == "w1" {
							seen = true
							break
						}
					}
				}
				if seen {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if !seen {
				t.Fatalf("the observe child never probed w1: stderr=%q", errOut.String())
			}
			if err := os.WriteFile(ready, []byte("go\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Process.Signal(tc.signal); err != nil {
				t.Fatal(err)
			}
			waited := make(chan error, 1)
			go func() { waited <- cmd.Wait() }()
			var waitErr error
			select {
			case waitErr = <-waited:
				stopped = true
			case <-time.After(20 * time.Second):
				t.Fatalf("the observe child did not exit after %s: stderr=%q", tc.name, errOut.String())
			}
			code := 0
			if waitErr != nil {
				if exit, ok := waitErr.(*exec.ExitError); ok {
					code = exit.ExitCode()
				} else {
					t.Fatalf("wait: %v", waitErr)
				}
			}
			if code != tc.code {
				t.Fatalf("exit=%d want %d; stdout=%q stderr=%q", code, tc.code, out.String(), errOut.String())
			}
			lines := strings.Split(strings.TrimSpace(out.String()), "\n")
			if len(lines) != 1 {
				t.Fatalf("stdout %q, want exactly one line", out.String())
			}
			var line map[string]any
			if err := json.Unmarshal([]byte(lines[0]), &line); err != nil {
				t.Fatalf("decode %q: %v", lines[0], err)
			}
			want := map[string]any{
				"wake": "interrupted", "signal": tc.name,
				"agents": []any{"w1"}, "rearm": "soho wait w1 --wake --timeout 600000",
			}
			if !reflect.DeepEqual(line, want) {
				t.Fatalf("line=%v want %v", line, want)
			}
			if strings.Contains(out.String(), "evil-from-core") {
				t.Fatalf("Core output reached the interrupted line: %q", out.String())
			}
		})
	}
}

// The rearm mirrors the child's flags: --any is present only when the wake
// asked for it. This direct check needs no subprocess.
func TestWakeRearmCommand(t *testing.T) {
	if got := wakeRearmCommand([]string{"w1", "w2"}, 600000, false); got != "soho wait w1 w2 --wake --timeout 600000" {
		t.Fatalf("rearm=%q", got)
	}
	if got := wakeRearmCommand([]string{"w1", "w2"}, 90000, true); got != "soho wait w1 w2 --wake --any --timeout 90000" {
		t.Fatalf("rearm with any=%q", got)
	}
	if got := wakeSignalName(syscall.SIGTERM); got != "SIGTERM" {
		t.Fatalf("signal name=%q", got)
	}
}
