package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
