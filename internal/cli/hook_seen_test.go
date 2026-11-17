package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/platform"
)

func TestHookDoctorSeen(t *testing.T) {
	env, cwd := commandFixture(t)
	env["HERDR_ENV"], env["PATH"] = "1", t.TempDir()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	old := platform.Now
	platform.Now = func() time.Time { return now }
	t.Cleanup(func() { platform.Now = old })
	run := func(args ...string) string {
		t.Helper()
		code, out, stderr := runIn(t, args, env, cwd)
		if code != 0 || stderr != "" {
			t.Fatalf("%v: code=%d stderr=%q", args, code, stderr)
		}
		return out
	}
	first := run("hook", "doctor")
	lines := strings.Split(strings.TrimSpace(first), "\n")
	if len(lines) < 2 || strings.Contains(first, "are hidden") {
		t.Fatalf("first: %q", first)
	}
	second := run("hook", "doctor")
	want := fmt.Sprintf("soho doctor: %d warning(s) shown in the last 24h are hidden; run 'soho doctor' to see all\n", len(lines))
	if second != want {
		t.Fatalf("second: %q want %q", second, want)
	}
	configDir := filepath.Join(cwd, ".agents")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "soho.conf"), []byte("panes=9\n"), 0600); err != nil {
		t.Fatal(err)
	}
	third := run("hook", "doctor")
	if !strings.Contains(third, "panes='9'") || !strings.Contains(third, "are hidden") {
		t.Fatalf("new warning: %q", third)
	}
	direct := run("doctor")
	if !strings.Contains(direct, "panes='9'") || !strings.Contains(direct, "warn") {
		t.Fatalf("direct doctor: %q", direct)
	}
	if err := os.Remove(filepath.Join(configDir, "soho.conf")); err != nil {
		t.Fatal(err)
	}
	now = now.Add(24 * time.Hour)
	again := run("hook", "doctor")
	for _, line := range lines {
		if !strings.Contains(again, line+"\n") {
			t.Fatalf("expired warning %q missing in %q", line, again)
		}
	}
	if strings.Contains(again, "are hidden") {
		t.Fatalf("expired: %q", again)
	}
}

func TestHookDoctorSeenFailures(t *testing.T) {
	for _, mode := range []string{"nowrite", "unwritable", "readonly"} {
		t.Run(mode, func(t *testing.T) {
			env, cwd := commandFixture(t)
			env["HERDR_ENV"], env["PATH"] = "1", t.TempDir()
			if mode == "nowrite" {
				env["SOHO_NOWRITE"] = "1"
			} else if mode == "readonly" {
				root := t.TempDir()
				if err := os.Chmod(root, 0500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(root, 0700) })
				env["TMPDIR"] = root
			} else {
				// A regular file as the scratch root deterministically refuses creation.
				root := filepath.Join(t.TempDir(), "file")
				if err := os.WriteFile(root, []byte("x"), 0600); err != nil {
					t.Fatal(err)
				}
				env["TMPDIR"] = root
			}
			for i := 0; i < 2; i++ {
				code, out, stderr := runIn(t, []string{"hook", "doctor"}, env, cwd)
				if code != 0 || stderr != "" || !strings.Contains(out, "soho doctor: ") || strings.Contains(out, "are hidden") {
					t.Fatalf("%s: %d %q %q", mode, code, out, stderr)
				}
			}
			if mode == "nowrite" {
				entries, err := os.ReadDir(filepath.Join(env["TMPDIR"], "soho"))
				if !os.IsNotExist(err) && (err != nil || len(entries) != 0) {
					t.Fatalf("nowrite scratch: %v %v", entries, err)
				}
			}
		})
	}
}
