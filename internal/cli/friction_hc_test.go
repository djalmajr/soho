package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
)

// SOH-291: the CLI entry supplies the same existing session log to each
// warning producer and to the friction reader, without initializing it.
func TestHcCommandFrictionWarnings(t *testing.T) {
	env, repo := commandFixture(t)
	env, _ = hcFixture(t, env, repo, "t1")
	env["HERDR_WORKSPACE_ID"] = "other"
	sd := filepath.Join(env["SOHO_DIR"], "ws")
	other := filepath.Join(env["SOHO_DIR"], "other")
	for _, dir := range []string{sd, other} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		args    []string
		command string
	}{
		{[]string{"dispatch", "worker", "missing", "--timeout", "45"}, "dispatch"},
		{[]string{"wait", "worker", "--timeout", "45", "--invalid"}, "wait"},
	} {
		code, _, stderr := runIn(t, tc.args, env, repo)
		if code != 2 || !strings.Contains(stderr, tc.command+shortTimeoutWarning45) {
			t.Fatalf("%v: code=%d stderr=%q", tc.args, code, stderr)
		}
	}
	code, _, stderr := runIn(t, []string{"collect", "worker", "--invalid"}, env, repo)
	if code != 2 || !strings.Contains(stderr, "collect: unknown option --invalid") {
		t.Fatalf("collect code=%d stderr=%q", code, stderr)
	}
	log, err := os.ReadFile(filepath.Join(sd, "friction.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"warning\tdispatch\tdispatch" + shortTimeoutWarning45, "warning\twait\twait" + shortTimeoutWarning45, "error(exit 2)\tcollect\tcollect: unknown option --invalid"} {
		if strings.Count(string(log), want) != 1 {
			t.Fatalf("log=%q, want exactly one %q", log, want)
		}
	}
	if _, err := os.Stat(filepath.Join(other, "friction.log")); !os.IsNotExist(err) {
		t.Fatalf("other session log: %v", err)
	}
	code, out, stderr := runIn(t, []string{"friction"}, env, repo)
	if code != 0 || stderr != "" || !strings.Contains(out, string(log)) {
		t.Fatalf("friction code=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestHcCommandFrictionBoundaries(t *testing.T) {
	for _, kind := range []string{"absent", "file", "symlink", "nowrite"} {
		t.Run(kind, func(t *testing.T) {
			env, repo := commandFixture(t)
			env, _ = hcFixture(t, env, repo, "t1")
			sd := filepath.Join(env["SOHO_DIR"], "ws")
			switch kind {
			case "file":
				if err := os.WriteFile(sd, []byte("sentinel"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := filepath.Join(t.TempDir(), "target")
				if err := os.Mkdir(target, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, sd); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			case "nowrite":
				if err := os.Mkdir(sd, 0700); err != nil {
					t.Fatal(err)
				}
				env["SOHO_NOWRITE"] = "1"
			}
			code, _, stderr := runIn(t, []string{"dispatch", "worker", "missing", "--timeout", "45"}, env, repo)
			if code != 2 {
				t.Fatalf("code=%d stderr=%q", code, stderr)
			}
			if kind != "nowrite" && !strings.Contains(stderr, "dispatch"+shortTimeoutWarning45) {
				t.Fatalf("no warning: %q", stderr)
			}
			if kind == "symlink" {
				log, err := os.ReadFile(filepath.Join(sd, "friction.log"))
				if err != nil || !strings.Contains(string(log), "warning\tdispatch\t") {
					t.Fatalf("log=%q err=%v", log, err)
				}
			} else if kind == "file" {
				data, err := os.ReadFile(sd)
				if err != nil || string(data) != "sentinel" {
					t.Fatalf("state file=%q err=%v", data, err)
				}
			} else if _, err := os.Stat(filepath.Join(sd, "friction.log")); !os.IsNotExist(err) {
				t.Fatalf("unexpected log: %v", err)
			}
			if kind == "nowrite" {
				code, _, stderr := runIn(t, []string{"collect", "worker", "--lines", "0"}, env, repo)
				if code != 4 || !strings.Contains(stderr, "no report file yet for 'worker'") {
					t.Fatalf("nowrite collect code=%d stderr=%q", code, stderr)
				}
				if _, err := os.Stat(filepath.Join(sd, "friction.log")); !os.IsNotExist(err) {
					t.Fatalf("nowrite warning wrote log: %v", err)
				}
			}
			if kind == "absent" {
				if _, err := os.Stat(sd); !os.IsNotExist(err) {
					t.Fatalf("state created: %v", err)
				}
				code, out, stderr := runIn(t, []string{"title", "goal"}, env, repo)
				if code != 4 || out != "" || !strings.Contains(stderr, "no initialized orchestrator record") {
					t.Fatalf("title code=%d out=%q stderr=%q", code, out, stderr)
				}
				if _, err := os.Stat(sd); !os.IsNotExist(err) {
					t.Fatalf("refused title created state: %v", err)
				}
			}
		})
	}
}

func TestHcFrictionPathDoesNotInitializeRelativeState(t *testing.T) {
	env, repo := commandFixture(t)
	env, _ = hcFixture(t, env, repo, "t1")
	env["SOHO_DIR"] = ".state with 'quotes'"
	code, _, stderr := runIn(t, []string{"dispatch", "worker", "missing", "--timeout", "45"}, env, repo)
	if code != 2 || !strings.Contains(stderr, "dispatch"+shortTimeoutWarning45) {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	for _, path := range []string{filepath.Join(repo, env["SOHO_DIR"]), filepath.Join(repo, ".gitignore")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("preflight wrote %s: %v", path, err)
		}
	}
}

func TestHcFrictionUnresolvedCallerPreservesCommandError(t *testing.T) {
	env, repo := commandFixture(t)
	env, _ = hcFixture(t, env, repo, "t1")
	env["HC_TERMINAL_ID"] = "missing"
	sd := filepath.Join(env["SOHO_DIR"], "ws")
	if err := os.Mkdir(sd, 0700); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runIn(t, []string{"dispatch", "worker", "missing", "--timeout", "45"}, env, repo)
	if code != 2 || !strings.Contains(stderr, "dispatch"+shortTimeoutWarning45) || !strings.Contains(stderr, "brief not found: missing") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(sd, "friction.log")); !os.IsNotExist(err) {
		t.Fatalf("unresolved caller logged to another session: %v", err)
	}
}

func TestHcFrictionPathUsesStateDirOverride(t *testing.T) {
	env, repo := commandFixture(t)
	env, _ = hcFixture(t, env, repo, "t1")
	delete(env, "SOHO_DIR")
	env["SOHO_STATE_DIR"] = filepath.Join(t.TempDir(), "state with 'quotes'")
	sd := filepath.Join(env["SOHO_STATE_DIR"], "ws")
	if err := os.MkdirAll(sd, 0700); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runIn(t, []string{"dispatch", "worker", "missing", "--timeout", "45"}, env, repo)
	if code != 2 || !strings.Contains(stderr, "dispatch"+shortTimeoutWarning45) {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	log, err := os.ReadFile(filepath.Join(sd, "friction.log"))
	if err != nil || !strings.Contains(string(log), "warning\tdispatch\t") {
		t.Fatalf("override log=%q err=%v", log, err)
	}
}

func TestHcReadCommandsDoNotInitializeState(t *testing.T) {
	for _, args := range [][]string{{"status"}, {"roster"}, {"friction"}, {"collect", "missing"}, {"wait", "missing", "--observe"}} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			env, repo := commandFixture(t)
			env, _ = hcFixture(t, env, repo, "t1")
			delete(env, "SOHO_DIR")
			sd := filepath.Join(repo, ".soho", "ws")
			code, out, stderr := runIn(t, args, env, repo)
			if _, err := os.Stat(sd); !os.IsNotExist(err) {
				t.Fatalf("%v created state %s: %v", args, sd, err)
			}
			if _, err := os.Stat(filepath.Join(repo, ".gitignore")); !os.IsNotExist(err) {
				t.Fatalf("unexpected gitignore: %v", err)
			}
			for _, name := range []string{"briefs", "reports", "wait"} {
				if err := os.MkdirAll(filepath.Join(sd, name), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(sd, "agents.tsv"), []byte("# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\n"), 0600); err != nil {
				t.Fatal(err)
			}
			baselineCode, baselineOut, baselineErr := runIn(t, args, env, repo)
			if code != baselineCode || out != baselineOut || stderr != baselineErr {
				t.Fatalf("%v absent=(%d,%q), existing=(%d,%q)", args, code, out, baselineCode, baselineOut)
			}
		})
	}
}

func TestHcFrictionReadsExistingState(t *testing.T) {
	env, repo := commandFixture(t)
	env, _ = hcFixture(t, env, repo, "t1")
	sd := filepath.Join(env["SOHO_DIR"], "ws")
	if err := os.MkdirAll(sd, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sd, "friction.log"), []byte("existing-entry\n"), 0600); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := runIn(t, []string{"friction"}, env, repo)
	if code != 0 || !strings.Contains(out, "existing-entry\n") {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestHcReadRejectsStateFile(t *testing.T) {
	env, repo := commandFixture(t)
	env, _ = hcFixture(t, env, repo, "t1")
	sd := filepath.Join(env["SOHO_DIR"], "ws")
	if err := os.WriteFile(sd, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runIn(t, []string{"status"}, env, repo)
	if code != 4 || strings.Count(stderr, "\n") != 1 || !strings.Contains(stderr, sd) {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

// R553: a dangling symlink at the session path is not an absent state.
func TestHcReadRejectsDanglingStateLink(t *testing.T) {
	env, repo := commandFixture(t)
	env, _ = hcFixture(t, env, repo, "t1")
	sd := filepath.Join(env["SOHO_DIR"], "ws")
	if err := os.Symlink(filepath.Join(env["SOHO_DIR"], "missing-volume"), sd); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runIn(t, []string{"status"}, env, repo)
	if code != 4 || strings.Count(stderr, "\n") != 1 || !strings.Contains(stderr, sd) {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(env["SOHO_DIR"], "missing-volume")); !os.IsNotExist(err) {
		t.Fatalf("the link target was created: %v", err)
	}
}

func TestHcExistingStateUnwritableGitignore(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses file permissions")
	}
	env, repo := commandFixture(t)
	env, _ = hcFixture(t, env, repo, "t1")
	env["SOHO_DIR"] = ".soho"
	if err := os.MkdirAll(filepath.Join(repo, ".soho", "ws"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("sentinel\n"), 0444); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runIn(t, []string{"status"}, env, repo)
	if code != 4 || strings.Count(stderr, "\n") != 1 || !strings.Contains(stderr, ".gitignore") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestLegacyCollectMissingInitializesFriction(t *testing.T) {
	env, repo := fakeHerdrCodexFixture(t)
	env["HERDR_ENV"] = "1"
	code, _, stderr := runIn(t, []string{"collect", "missing"}, env, repo)
	log, err := os.ReadFile(filepath.Join(env["SOHO_DIR"], "ws", "friction.log"))
	if err != nil || !strings.Contains(string(log), "no report file yet") {
		t.Fatalf("code=%d stderr=%q log=%q err=%v", code, stderr, log, err)
	}
}

func TestHcFreshStatusPressure(t *testing.T) {
	env, repo := commandFixture(t)
	env, _ = hcFixture(t, env, repo, "t1")
	delete(env, "SOHO_DIR")
	env["SOHO_PRESSURE_DISK_FREE_PERCENT"] = "15"
	env["SOHO_PRESSURE_DISK_FREE_MIN_GB"] = "0"
	env["SOHO_PRESSURE_SWAP_PERCENT"] = "0"
	oldDisk, oldSwap := platform.DiskFree, platform.SwapUsage
	platform.DiskFree = func(string) (int64, int64, bool) { return 9, 100, true }
	platform.SwapUsage = func(platform.Env) (int64, int64, bool) { return 0, 0, false }
	t.Cleanup(func() { platform.DiskFree, platform.SwapUsage = oldDisk, oldSwap })
	sd := filepath.Join(repo, ".soho", "ws")
	marker := filepath.Join(sd, "wait", "pressure-warned")
	code, _, stderr := runIn(t, []string{"status"}, env, repo)
	if code != 0 || !strings.Contains(stderr, "resource pressure") {
		t.Fatalf("absent code=%d stderr=%q", code, stderr)
	}
	if _, err := os.Stat(sd); !os.IsNotExist(err) {
		t.Fatalf("state created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".gitignore")); !os.IsNotExist(err) {
		t.Fatalf("gitignore created: %v", err)
	}
	if err := os.MkdirAll(sd, 0700); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = runIn(t, []string{"status"}, env, repo)
	if code != 0 || !strings.Contains(stderr, "resource pressure") {
		t.Fatalf("existing code=%d stderr=%q", code, stderr)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("marker: %v", err)
	}
	code, _, stderr = runIn(t, []string{"status"}, env, repo)
	if code != 0 || strings.Contains(stderr, "resource pressure") {
		t.Fatalf("throttle code=%d stderr=%q", code, stderr)
	}
}
