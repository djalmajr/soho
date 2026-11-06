package core_test

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
)

func TestWarnShortTimeoutCases(t *testing.T) {
	run := func(t *testing.T, command string, ms int64, logFile string) (string, string) {
		t.Helper()
		core.SetFrictionCommand("") // a fresh command: the helper's command is the friction command
		oldErr := platform.Stderr
		var stderr bytes.Buffer
		platform.Stderr = &stderr
		t.Cleanup(func() { platform.Stderr = oldErr })
		core.WarnShortTimeout(command, logFile, ms)
		log := ""
		if data, err := os.ReadFile(logFile); err == nil {
			log = string(data)
		}
		return stderr.String(), log
	}
	t.Run("45: the warning carries the exact sentence with 45000", func(t *testing.T) {
		logFile := filepath.Join(t.TempDir(), "friction.log")
		stderr, log := run(t, "wait", 45, logFile)
		if want := "soho: warning: wait: --timeout is in milliseconds; 45 is under a second (for 45 seconds pass 45000)\n"; stderr != want {
			t.Fatalf("stderr=%q, want %q", stderr, want)
		}
		if n := strings.Count(log, "warning\twait\twait: --timeout is in milliseconds; 45 is under a second (for 45 seconds pass 45000)"); n != 1 {
			t.Fatalf("friction log=%q, want exactly one warning line for wait", log)
		}
	})
	t.Run("999: the last sub-second value still warns", func(t *testing.T) {
		logFile := filepath.Join(t.TempDir(), "friction.log")
		stderr, log := run(t, "send", 999, logFile)
		if !strings.Contains(stderr, "send: --timeout is in milliseconds; 999 is under a second (for 999 seconds pass 999000)") {
			t.Fatalf("stderr=%q", stderr)
		}
		if !strings.Contains(log, "warning\tsend\t") {
			t.Fatalf("friction log=%q", log)
		}
	})
	for _, ms := range []int64{1, 1000, 1800, 9999} {
		t.Run(fmt.Sprint(ms), func(t *testing.T) {
			logFile := filepath.Join(t.TempDir(), "friction.log")
			stderr, log := run(t, "dispatch", ms, logFile)
			if strings.Count(stderr, "dispatch: --timeout is in milliseconds;") != 1 || strings.Count(log, "warning\tdispatch\t") != 1 {
				t.Fatalf("ms=%d stderr=%q log=%q; want one warning", ms, stderr, log)
			}
			if ms >= 1000 && (!strings.Contains(stderr, fmt.Sprintf("%d means %dms", ms, ms)) || !strings.Contains(stderr, "30m") || strings.Contains(stderr, "under a second")) {
				t.Fatalf("ms=%d stderr=%q; want accurate milliseconds and an explicit duration", ms, stderr)
			}
		})
	}
	t.Run("10000 and larger values: no warning, no friction log", func(t *testing.T) {
		logFile := filepath.Join(t.TempDir(), "friction.log")
		for _, ms := range []int64{10000, 60000, 1800000, math.MaxInt64} {
			stderr, log := run(t, "dispatch", ms, logFile)
			if stderr != "" || log != "" {
				t.Fatalf("ms=%d stderr=%q log=%q", ms, stderr, log)
			}
		}
	})
	t.Run("0 and negative values: no warning", func(t *testing.T) {
		logFile := filepath.Join(t.TempDir(), "friction.log")
		for _, ms := range []int64{0, -5} {
			stderr, log := run(t, "compact", ms, logFile)
			if stderr != "" || log != "" {
				t.Fatalf("ms=%d stderr=%q log=%q", ms, stderr, log)
			}
		}
	})
	t.Run("empty logFile: the warning stays on screen only", func(t *testing.T) {
		core.SetFrictionCommand("")
		oldErr := platform.Stderr
		var stderr bytes.Buffer
		platform.Stderr = &stderr
		t.Cleanup(func() { platform.Stderr = oldErr })
		core.WarnShortTimeout("wait", "", 45)
		if !strings.Contains(stderr.String(), "wait: --timeout is in milliseconds; 45 is under a second") {
			t.Fatalf("stderr=%q", stderr)
		}
	})
}
