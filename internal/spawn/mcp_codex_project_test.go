package spawn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// DJA-418: codex 0.163.0 takes -c mcp_servers.<name>.enabled=false for a
// server defined in the user's config.toml, but its TUI dies at bootstrap
// when the server is defined only in the project's .codex/config.toml, while
// `codex mcp list` accepts the same flags. The spawn lists the servers twice:
// in the worker's cwd (the project listing) and in an empty directory (the
// neutral listing). A server enabled in the first and absent from the second
// is the project's own: it stays out of the overrides and of the check, and
// stays on.

// projectListRules answers the first `codex mcp list --json` (the project
// listing) with project and the second (the neutral listing) with neutral.
func projectListRules(project, neutral string) []fakecli.Rule {
	return []fakecli.Rule{
		{Argv: codexListArgv, Call: 1, Stdout: project},
		{Argv: codexListArgv, Call: 2, Stdout: neutral},
	}
}

func codexProjectOnlyWarn(names, agent string) string {
	return "spawn: codex cannot turn off MCP server(s) " + names + " for '" + agent + "' (they are defined only in the project's .codex/config.toml); they stay on, the others are off"
}

// codexCallsWithCwd returns every codex call the spawn made, with its cwd.
func codexCallsWithCwd(t *testing.T, f spawnFixture) []fakecli.Call {
	t.Helper()
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(f.bin, "codex.json"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return calls
}

func realPath(t *testing.T, path string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", path, err)
	}
	return real
}

func TestCmdSpawnMcpCodexKeepsTheProjectsOwnServersOn(t *testing.T) {
	const secret = "SECRET-DJA418"

	t.Run("the Cinzel case: p1 and p2 are the project's own, only a is turned off and checked", func(t *testing.T) {
		f := codexMcpFixture(t)
		installCodexMcpScript(t, f, append(projectListRules(codexListing(secret, "a", "p1", "p2"), codexListing(secret, "a")), checkOK("a"))...)
		code, _, stderr := spawnCodexNone(t, f)
		if code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, stderr)
		}
		wantTail(t, f, codexMcpBaseArgv+" -c mcp_servers.a.enabled=false")
		wantCodexCalls(t, f, codexListArgv, codexListArgv, codexCheckArgv("a"))
		warn := codexProjectOnlyWarn("p1, p2", "worker")
		if got := stdErrCount(stderr, "soho: warning: "+warn); got != 1 {
			t.Fatalf("the exact project-only warning is not on stderr once: %q", stderr)
		}
		if got := strings.Count(codexFriction(t, f), warn); got != 1 {
			t.Fatalf("friction.log lacks the project-only warning once:\n%s", codexFriction(t, f))
		}
		if strings.Contains(stderr, "not defined in its config.toml") || strings.Contains(stderr, "could not list") {
			t.Fatalf("another MCP warning came with the project-only one: %q", stderr)
		}
	})

	t.Run("every server the project's own: no overrides, no check, the warning lists them all", func(t *testing.T) {
		f := codexMcpFixture(t)
		installCodexMcpScript(t, f, projectListRules(codexListing(secret, "p1", "p2"), `[]`)...)
		code, _, stderr := spawnCodexNone(t, f)
		if code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, stderr)
		}
		wantTail(t, f, codexMcpBaseArgv)
		wantCodexCalls(t, f, codexListArgv, codexListArgv)
		if got := stdErrCount(stderr, "soho: warning: "+codexProjectOnlyWarn("p1, p2", "worker")); got != 1 {
			t.Fatalf("the exact project-only warning is not on stderr once: %q", stderr)
		}
	})

	t.Run("a project server and a plugin server: both warnings, each once", func(t *testing.T) {
		f := codexMcpFixture(t)
		installCodexMcpScript(t, f, append(projectListRules(codexListing(secret, "a", "b", "p1"), codexListing(secret, "a", "b")),
			checkNaming("b", secret, "a", "b"),
			checkOK("a"),
		)...)
		code, _, stderr := spawnCodexNone(t, f)
		if code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, stderr)
		}
		wantTail(t, f, codexMcpBaseArgv+" -c mcp_servers.a.enabled=false")
		wantCodexCalls(t, f, codexListArgv, codexListArgv, codexCheckArgv("a", "b"), codexCheckArgv("a"))
		for _, warn := range []string{codexProjectOnlyWarn("p1", "worker"), codexStayOnWarn("b", "worker")} {
			if got := stdErrCount(stderr, "soho: warning: "+warn); got != 1 {
				t.Fatalf("%q is not on stderr once: %q", warn, stderr)
			}
			if got := strings.Count(codexFriction(t, f), warn); got != 1 {
				t.Fatalf("friction.log lacks %q once:\n%s", warn, codexFriction(t, f))
			}
		}
	})

	t.Run("a server in both listings is turned off as today, even when the neutral one has it disabled", func(t *testing.T) {
		f := codexMcpFixture(t)
		installCodexMcpScript(t, f, append(projectListRules(codexListing(secret, "a", "playwright"), `[{"name":"a","enabled":true},{"name":"playwright","enabled":false}]`),
			checkOK("a", "playwright"),
		)...)
		code, _, stderr := spawnCodexNone(t, f)
		if code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, stderr)
		}
		wantTail(t, f, codexMcpBaseArgv+" -c mcp_servers.a.enabled=false -c mcp_servers.playwright.enabled=false")
		wantCodexCalls(t, f, codexListArgv, codexListArgv, codexCheckArgv("a", "playwright"))
		if strings.Contains(stderr, "cannot turn off MCP") || strings.Contains(stderr, "could not list") {
			t.Fatalf("servers in both listings warned: %q", stderr)
		}
	})

	for _, tc := range []struct {
		name   string
		rule   fakecli.Rule
		reason string
	}{
		{"exit 1", fakecli.Rule{Argv: codexListArgv, Call: 2, Code: 1, Stderr: "Error: " + secret + "\n"}, "neutral listing exit 1"},
		{"a timeout", fakecli.Rule{Argv: codexListArgv, Call: 2, Delay: 10500}, "neutral listing timed out after 10 s"},
		{"non-JSON output", fakecli.Rule{Argv: codexListArgv, Call: 2, Stdout: "not json " + secret}, "neutral listing non-JSON output"},
	} {
		t.Run("the neutral listing fails with "+tc.name+": the listing-failure path, no overrides", func(t *testing.T) {
			f := codexMcpFixture(t)
			installCodexMcpScript(t, f, fakecli.Rule{Argv: codexListArgv, Call: 1, Stdout: codexListing(secret, "a", "p1")}, tc.rule)
			code, stdout, stderr := spawnCodexNone(t, f)
			if code != 0 {
				t.Fatalf("exit=%d stderr=%q", code, stderr)
			}
			if got := stdErrCount(stderr, codexMcpWarn(tc.reason, "worker")); got != 1 {
				t.Fatalf("the exact neutral-failure warning is not on stderr once: %q", stderr)
			}
			if strings.Contains(stderr, "cannot turn off MCP") {
				t.Fatalf("a failed neutral listing also warned about servers that stay on: %q", stderr)
			}
			wantTail(t, f, codexMcpBaseArgv)
			wantCodexCalls(t, f, codexListArgv, codexListArgv)
			if strings.Contains(stdout+stderr+codexFriction(t, f), secret) {
				t.Fatalf("the neutral listing's output leaked: %q %q", stdout, stderr)
			}
		})
	}

	for _, tc := range []struct {
		name   string
		cwdArg string
	}{
		{"without --cwd: the spawn's cwd", ""},
		{"with --cwd: the worker's cwd", "other"},
	} {
		t.Run("the project listing runs in "+tc.name+", the neutral one in an empty directory removed afterwards", func(t *testing.T) {
			f := codexMcpFixture(t)
			worker := f.cwd
			args := []string{"worker", "--kind", "codex", "--model", "gpt-5", "--effort", "high", "--mcp", "none"}
			if tc.cwdArg != "" {
				worker = filepath.Join(f.root, tc.cwdArg)
				if err := os.MkdirAll(worker, 0o700); err != nil {
					t.Fatal(err)
				}
				args = append(args, "--cwd", worker)
			}
			// The neutral listing waits on a file; while it waits the test
			// reads its cwd from the call log and lists that directory.
			release := filepath.Join(f.root, "neutral-release")
			installCodexMcpScript(t, f,
				fakecli.Rule{Argv: codexListArgv, Call: 1, Stdout: codexListing(secret, "a", "p1")},
				fakecli.Rule{Argv: codexListArgv, Call: 2, Stdout: codexListing(secret, "a"), WaitFile: release},
				checkOK("a"),
			)
			type seen struct {
				cwd     string
				entries int
				err     error
			}
			during := make(chan seen, 1)
			go func() {
				defer func() { _ = os.WriteFile(release, nil, 0o600) }()
				deadline := time.Now().Add(30 * time.Second)
				for time.Now().Before(deadline) {
					calls, _ := fakecli.ReadCallsForConfig(filepath.Join(f.bin, "codex.json"))
					if len(calls) >= 2 {
						entries, err := os.ReadDir(calls[1].Cwd)
						during <- seen{calls[1].Cwd, len(entries), err}
						return
					}
					time.Sleep(10 * time.Millisecond)
				}
				during <- seen{err: os.ErrDeadlineExceeded}
			}()
			code, _, stderr, _ := runCmdSpawn(t, f, args)
			if code != 0 {
				t.Fatalf("exit=%d stderr=%q", code, stderr)
			}
			s := <-during
			if s.err != nil || s.entries != 0 {
				t.Fatalf("the neutral cwd %q while listing: entries=%d err=%v", s.cwd, s.entries, s.err)
			}
			calls := codexCallsWithCwd(t, f)
			if len(calls) != 3 {
				t.Fatalf("codex calls=%#v", calls)
			}
			if got, want := realPath(t, calls[0].Cwd), realPath(t, worker); got != want {
				t.Fatalf("the project listing ran in %q, want the worker's cwd %q", got, want)
			}
			neutral := calls[1].Cwd
			if neutral == "" || neutral != s.cwd || neutral == calls[0].Cwd || strings.HasPrefix(realPath(t, worker)+string(filepath.Separator), neutral+string(filepath.Separator)) {
				t.Fatalf("the neutral listing ran in %q (worker %q)", neutral, worker)
			}
			if _, err := os.Stat(neutral); !os.IsNotExist(err) {
				t.Fatalf("the neutral directory %q is still there after the spawn: %v", neutral, err)
			}
			wantTail(t, f, codexMcpBaseArgv+" -c mcp_servers.a.enabled=false")
		})
	}

	t.Run("the neutral directory is removed when the neutral listing fails", func(t *testing.T) {
		f := codexMcpFixture(t)
		installCodexMcpScript(t, f,
			fakecli.Rule{Argv: codexListArgv, Call: 1, Stdout: codexListing(secret, "a")},
			fakecli.Rule{Argv: codexListArgv, Call: 2, Code: 1},
		)
		code, _, stderr := spawnCodexNone(t, f)
		if code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, stderr)
		}
		calls := codexCallsWithCwd(t, f)
		if len(calls) != 2 || calls[1].Cwd == "" {
			t.Fatalf("codex calls=%#v", calls)
		}
		if _, err := os.Stat(calls[1].Cwd); !os.IsNotExist(err) {
			t.Fatalf("the neutral directory %q is still there after a failed listing: %v", calls[1].Cwd, err)
		}
	})

	t.Run("no transport from either listing leaks into stdout, stderr, friction, herdr argv or roster", func(t *testing.T) {
		f := codexMcpFixture(t)
		installCodexMcpScript(t, f, append(projectListRules(codexListing(secret, "a", "b", "p1"), codexListing(secret, "a", "b")),
			checkNaming("b", secret, "a", "b"),
			checkOK("a"),
		)...)
		code, stdout, stderr := spawnCodexNone(t, f)
		if code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, stderr)
		}
		if !strings.Contains(stderr, codexProjectOnlyWarn("p1", "worker")) {
			t.Fatalf("the path under test did not warn: %q", stderr)
		}
		roster, _ := os.ReadFile(filepath.Join(f.state, "ws", "agents.tsv"))
		all := strings.Join([]string{stdout, stderr, codexFriction(t, f), strings.Join(herdrCallArgv(t, f), " "), string(roster)}, "\n")
		for _, leaked := range []string{secret, "--api-key", "/opt/", `"transport"`} {
			if strings.Contains(all, leaked) {
				t.Fatalf("%q leaked into stdout/stderr/friction/argv/roster:\n%s", leaked, all)
			}
		}
	})
}
