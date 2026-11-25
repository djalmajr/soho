package spawn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func dja659WaitForCallInFlight(t *testing.T, log string, argv []string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		calls, err := fakecli.ReadCalls(log)
		if err == nil && len(calls) > 0 {
			last := calls[len(calls)-1]
			if dja659SameArgv(last.Argv, argv) {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the native call %v was never recorded in flight (calls=%#v err=%v)", argv, calls, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func dja659SameArgv(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestGroupedSpawnKeepsReplacedGoneRow(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "release")
	get := []string{"agent", "get", "stale"}
	f := laneModelFixtureWithCapacity(t, "off", "m1", "1",
		fakecli.Rule{Argv: get, WaitFile: marker, Code: 1, Stderr: `{"error":{"code":"agent_not_found","message":"gone"}}`})
	appendLaneIdleRow(t, f, "stale", "m1")
	path := filepath.Join(f.state, "ws", "agents.tsv")
	original := rosterBytes(t, f)
	replacement := strings.Replace(original, "stale\tp-stale\t", "stale\tp-replacement\t", 1)
	if replacement == original {
		t.Fatalf("replacement failed to change fixture: %s", original)
	}
	f.env["FAKECLI_WAIT_FILE_TIMEOUT_MS"] = "20000"
	type outcome struct {
		code                    int
		message, stdout, stderr string
		calls                   []fakecli.Call
	}
	outcomes := make(chan outcome, 1)
	joined := make(chan struct{})
	t.Cleanup(func() {
		if _, err := os.Stat(marker); os.IsNotExist(err) {
			if err := os.WriteFile(marker, nil, 0o600); err != nil {
				t.Errorf("release blocked get: %v", err)
			}
		}
		<-joined
	})
	go func() {
		defer close(joined)
		code, message, stdout, stderr, calls := runCmdSpawnWithExitMessage(t, f, []string{"scouter"})
		outcomes <- outcome{code: code, message: message, stdout: stdout, stderr: stderr, calls: calls}
	}()
	dja659WaitForCallInFlight(t, filepath.Join(f.bin, "herdr.calls.jsonl"), get)
	if err := os.WriteFile(path, []byte(replacement), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	out := <-outcomes
	if got := rosterBytes(t, f); got != replacement || out.code != 4 || !strings.Contains(out.message, "roster changed while checking stale worker 'stale'") ||
		out.stdout != "" || len(herdrActs(out.calls)) != 0 {
		t.Fatalf("replacement must survive without spawn: code=%d message=%q stdout=%q stderr=%q roster=%q", out.code, out.message, out.stdout, out.stderr, got)
	}
}
