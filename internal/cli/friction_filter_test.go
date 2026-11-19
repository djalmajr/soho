package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

const frictionFilterLog = "2026-09-30T10:00:00\twarning\twait\tquota: agent 'alpha' lane=build\n" +
	"2026-09-30T11:30:00\terror(exit 4)\tcollect\tverify failed for 'beta'\n" +
	"2026-09-30T11:31:00\terror(exit 16)\tcollect\tanother failure for 'beta'\n" +
	"2026-09-30T12:00:00\tnote\tfriction\tmanual note for 'alpha'\n" +
	"2026-10-01T09:00:00\twarning\tdispatch\tlate entry for 'gamma'\n" +
	"garbage with no tabs\n" +
	"only\ttwo\n" +
	"three\tcolumns\tx\n"

func frictionFilterFixture(t *testing.T, log string) (platform.Env, string) {
	t.Helper()
	env, _ := commandFixture(t)
	fakeDir := t.TempDir()
	if _, err := fakecli.Install(t, fakeDir, "herdr", nil); err != nil {
		t.Fatal(err)
	}
	env = withFakeCLI(env, fakeDir)
	env["HERDR_ENV"] = "1"
	logPath := filepath.Join(env.Get("SOHO_DIR"), "ws", "friction.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if log != "" {
		if err := os.WriteFile(logPath, []byte(log), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return env, logPath
}

func runFriction(t *testing.T, env platform.Env, args ...string) (int, string, string) {
	t.Helper()
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, stderr bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &stderr
	code := Run(append([]string{"friction"}, args...), env)
	platform.Stdout, platform.Stderr = oldOut, oldErr
	return code, out.String(), stderr.String()
}

func TestFrictionFilterOptions(t *testing.T) {
	header := func(logPath string) string {
		return "friction log (" + logPath + "): timestamp, level, command, message\n"
	}
	t.Run("friction: no options keeps the header and the whole file exactly", func(t *testing.T) {
		env, logPath := frictionFilterFixture(t, frictionFilterLog)
		code, out, stderr := runFriction(t, env)
		if code != 0 || stderr != "" || out != header(logPath)+frictionFilterLog {
			t.Fatalf("code=%d stderr=%q out=%q", code, stderr, out)
		}
	})
	t.Run("friction: --since with a date keeps the lines from that day on", func(t *testing.T) {
		env, logPath := frictionFilterFixture(t, frictionFilterLog)
		code, out, stderr := runFriction(t, env, "--since", "2026-10-01")
		want := header(logPath) + "2026-10-01T09:00:00\twarning\tdispatch\tlate entry for 'gamma'\n"
		if code != 0 || stderr != "" || out != want {
			t.Fatalf("code=%d stderr=%q out=%q", code, stderr, out)
		}
	})
	t.Run("friction: --since with an ISO instant keeps the equal instant and after", func(t *testing.T) {
		env, logPath := frictionFilterFixture(t, frictionFilterLog)
		code, out, stderr := runFriction(t, env, "--since", "2026-09-30T11:30:00")
		want := header(logPath) +
			"2026-09-30T11:30:00\terror(exit 4)\tcollect\tverify failed for 'beta'\n" +
			"2026-09-30T11:31:00\terror(exit 16)\tcollect\tanother failure for 'beta'\n" +
			"2026-09-30T12:00:00\tnote\tfriction\tmanual note for 'alpha'\n" +
			"2026-10-01T09:00:00\twarning\tdispatch\tlate entry for 'gamma'\n"
		if code != 0 || stderr != "" || out != want {
			t.Fatalf("code=%d stderr=%q out=%q", code, stderr, out)
		}
	})
	t.Run("friction: --since 50m keeps a 30-minute-old line and drops a 70-minute-old line", func(t *testing.T) {
		now := platform.Now()
		recent := now.Add(-30 * time.Minute).Format("2006-01-02T15:04:05")
		old := now.Add(-70 * time.Minute).Format("2006-01-02T15:04:05")
		log := old + "\twarning\twait\ttoo old\n" + recent + "\twarning\twait\twithin the window\n"
		env, logPath := frictionFilterFixture(t, log)
		code, out, stderr := runFriction(t, env, "--since", "50m")
		want := header(logPath) + recent + "\twarning\twait\twithin the window\n"
		if code != 0 || stderr != "" || out != want {
			t.Fatalf("code=%d stderr=%q out=%q", code, stderr, out)
		}
	})
	t.Run("friction: --level error matches every error(exit N) and nothing else", func(t *testing.T) {
		env, logPath := frictionFilterFixture(t, frictionFilterLog)
		code, out, stderr := runFriction(t, env, "--level", "error")
		want := header(logPath) +
			"2026-09-30T11:30:00\terror(exit 4)\tcollect\tverify failed for 'beta'\n" +
			"2026-09-30T11:31:00\terror(exit 16)\tcollect\tanother failure for 'beta'\n"
		if code != 0 || stderr != "" || out != want {
			t.Fatalf("code=%d stderr=%q out=%q", code, stderr, out)
		}
	})
	t.Run("friction: --command and --agent filter on the third column and the quoted name", func(t *testing.T) {
		env, logPath := frictionFilterFixture(t, frictionFilterLog)
		code, out, _ := runFriction(t, env, "--command", "collect")
		if code != 0 || !strings.Contains(out, "error(exit 4)") || !strings.Contains(out, "error(exit 16)") || strings.Contains(out, "note\tfriction") {
			t.Fatalf("command: code=%d out=%q", code, out)
		}
		code, out, _ = runFriction(t, env, "--agent", "beta")
		if code != 0 || !strings.Contains(out, "'beta'") || strings.Contains(out, "'alpha'") {
			t.Fatalf("agent beta: code=%d out=%q", code, out)
		}
		code, out, _ = runFriction(t, env, "--agent", "alpha")
		if code != 0 || !strings.Contains(out, "quota: agent 'alpha'") || !strings.Contains(out, "manual note for 'alpha'") || strings.Contains(out, "'beta'") {
			t.Fatalf("agent alpha: code=%d out=%q", code, out)
		}
		_ = logPath
	})
	t.Run("friction: --agent matches only the single-quoted name, not a bare occurrence", func(t *testing.T) {
		env, logPath := frictionFilterFixture(t, "2026-09-30T10:00:00\tnote\twait\tretry sent to alpha\n"+
			"2026-09-30T10:01:00\tnote\twait\tdispatched for 'alpha' again\n")
		code, out, stderr := runFriction(t, env, "--agent", "alpha")
		want := header(logPath) + "2026-09-30T10:01:00\tnote\twait\tdispatched for 'alpha' again\n"
		if code != 0 || stderr != "" || out != want {
			t.Fatalf("code=%d stderr=%q out=%q", code, stderr, out)
		}
	})
	t.Run("friction: the filters AND together", func(t *testing.T) {
		env, logPath := frictionFilterFixture(t, frictionFilterLog)
		code, out, stderr := runFriction(t, env, "--since", "2026-09-30T11:31:00", "--command", "collect")
		want := header(logPath) + "2026-09-30T11:31:00\terror(exit 16)\tcollect\tanother failure for 'beta'\n"
		if code != 0 || stderr != "" || out != want {
			t.Fatalf("code=%d stderr=%q out=%q", code, stderr, out)
		}
	})
	t.Run("friction: filters without a match exit 0 with the no-matches line", func(t *testing.T) {
		env, logPath := frictionFilterFixture(t, frictionFilterLog)
		code, out, stderr := runFriction(t, env, "--level", "note", "--command", "wait")
		if code != 0 || stderr != "" || out != "no friction matches under "+logPath+"\n" {
			t.Fatalf("code=%d stderr=%q out=%q", code, stderr, out)
		}
	})
	t.Run("friction: filters with a missing log exit 0 with the no-matches line", func(t *testing.T) {
		env, logPath := frictionFilterFixture(t, "")
		code, out, stderr := runFriction(t, env, "--level", "error")
		if code != 0 || stderr != "" || out != "no friction matches under "+logPath+"\n" {
			t.Fatalf("code=%d stderr=%q out=%q", code, stderr, out)
		}
	})
	t.Run("friction: --summary counts the filtered lines and the malformed ones as ?", func(t *testing.T) {
		env, _ := frictionFilterFixture(t, frictionFilterLog)
		code, out, stderr := runFriction(t, env, "--summary")
		want := "count  level  command\n" +
			"2  ?  ?\n" +
			"1  ?  x\n" +
			"1  error(exit 16)  collect\n" +
			"1  error(exit 4)  collect\n" +
			"1  note  friction\n" +
			"1  warning  dispatch\n" +
			"1  warning  wait\n"
		if code != 0 || stderr != "" || out != want {
			t.Fatalf("code=%d stderr=%q out=%q", code, stderr, out)
		}
	})
	t.Run("friction: --summary with other filters still counts the malformed lines", func(t *testing.T) {
		env, _ := frictionFilterFixture(t, frictionFilterLog)
		code, out, stderr := runFriction(t, env, "--summary", "--level", "note")
		want := "count  level  command\n" +
			"2  ?  ?\n" +
			"1  ?  x\n" +
			"1  note  friction\n"
		if code != 0 || stderr != "" || out != want {
			t.Fatalf("code=%d stderr=%q out=%q", code, stderr, out)
		}
	})
	t.Run("friction: --summary without any counted line exits 0 with the no-matches line", func(t *testing.T) {
		env, logPath := frictionFilterFixture(t, "2026-09-30T10:00:00\twarning\twait\tonly line\n")
		code, out, stderr := runFriction(t, env, "--summary", "--command", "nosuch")
		if code != 0 || stderr != "" || out != "no friction matches under "+logPath+"\n" {
			t.Fatalf("code=%d stderr=%q out=%q", code, stderr, out)
		}
	})
	t.Run("friction: an invalid or absent option value dies 2", func(t *testing.T) {
		cases := []struct {
			args []string
			want string
		}{
			{[]string{"--since", "not-a-date"}, "friction: --since expects AAAA-MM-DD, an ISO date or a duration such as 50m, 12h, 1d or 2w (got 'not-a-date')"},
			{[]string{"--level", "bogus"}, "friction: --level expects warning, note or error (got 'bogus')"},
			{[]string{"--command"}, "friction: --command expects a value"},
			{[]string{"--agent", "--summary"}, "friction: --agent expects a value"},
			{[]string{"--bogus"}, "friction: unknown option --bogus"},
		}
		for _, tc := range cases {
			env, _ := frictionFilterFixture(t, frictionFilterLog)
			code, out, stderr := runFriction(t, env, tc.args...)
			if code != 2 || out != "" || !strings.Contains(stderr, tc.want) {
				t.Fatalf("%v: code=%d stderr=%q want %q", tc.args, code, stderr, tc.want)
			}
		}
	})
	t.Run("friction: the filter options stay off of friction add", func(t *testing.T) {
		cases := []struct {
			args []string
			want string
		}{
			{[]string{"--summary", "add", "a note"}, "friction: unknown option --summary"},
			{[]string{"--since", "2026-01-01", "add", "a note"}, "friction: unknown option --since"},
		}
		for _, tc := range cases {
			env, logPath := frictionFilterFixture(t, frictionFilterLog)
			code, out, stderr := runFriction(t, env, tc.args...)
			if code != 2 || out != "" || !strings.Contains(stderr, tc.want) {
				t.Fatalf("%v: code=%d stderr=%q want %q", tc.args, code, stderr, tc.want)
			}
			if data, err := os.ReadFile(logPath); err == nil && strings.Contains(string(data), "a note") {
				t.Fatalf("%v: add wrote the log: %q", tc.args, data)
			}
		}
	})
}
