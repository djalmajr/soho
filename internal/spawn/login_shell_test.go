package spawn

// R316: login_shell=on starts a native worker through the caller's login
// shell: `<SHELL> -l -c 'exec "$0" "$@"' <agent executable> <agent args…>`.
// These tests run the real spawn against the package's fake Core: the wrapped
// argv is read from the terminal.run params, the refusals are asserted before
// any connection to Core exists, and the roster and JSON keep the agent's own
// argv.

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
	"github.com/djalmajr/soho/internal/transport"
)

const loginShellScript = `exec "$0" "$@"`

// loginShellFixture is the hc pi implementer fixture with the fake hc
// installed and an executable shell named `name` in the fixture's bin dir.
func loginShellFixture(t *testing.T, name string) (cwd, bin, state string, env platform.Env, ctx *core.Config, shell string) {
	t.Helper()
	cwd, bin, state, env, ctx = hcSpawnFixture(t, "implementer", "pi")
	rules := append(hcFixtureRules(),
		fakecli.Rule{Argv: []string{"get", "t7", "--json"}, Stdout: hcReadyWorker},
	)
	if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
		t.Fatal(err)
	}
	shell = filepath.Join(filepath.Dir(bin), "shells", name)
	if err := os.MkdirAll(filepath.Dir(shell), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shell, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return cwd, bin, state, env, ctx, shell
}

func runArgv(t *testing.T, params map[string]any) []string {
	t.Helper()
	got, ok := params["argv"].([]any)
	if !ok {
		t.Fatalf("argv not a slice: %v", params["argv"])
	}
	var out []string
	for _, a := range got {
		s, ok := a.(string)
		if !ok {
			t.Fatalf("argv element %v is not a string", a)
		}
		out = append(out, s)
	}
	return out
}

func skipLoginShellOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("login_shell=on is refused on Windows; TestSpawnLoginShellWindowsRefused covers it")
	}
}

func TestSpawnLoginShellOffKeepsTheAgentArgv(t *testing.T) {
	// Mutation (default off): treating an unset/off/invalid key as on wraps the argv.
	for _, value := range []string{"", "off", "yes"} {
		t.Run("login_shell="+value, func(t *testing.T) {
			skipLoginShellOnWindows(t)
			cwd, bin, _, env, ctx, shell := loginShellFixture(t, "zsh")
			env["SHELL"] = shell
			if value != "" {
				env["SOHO_LOGIN_SHELL"] = value
			}
			res := core.ResolveRoleSettings("implementer", ctx, env, cwd, core.RoleFlags{})
			agent := hcAgentArgs(t, "implementer", res, ctx, env, cwd)
			conn := newFakeSpawnRPCConn(rpcHelloSuccess + "\n" + rpcRunSuccess + "\n")
			spawnHcWithConn(t, bin, []string{"implementer"}, ctx, env, cwd, conn)
			_, params := parseTerminalRunReq(t, conn.getWritten())
			want := append([]string{filepath.Join(bin, "pi")}, agent...)
			if got := runArgv(t, params); strings.Join(got, "\x00") != strings.Join(want, "\x00") {
				t.Fatalf("argv=%q, want the unwrapped %q", got, want)
			}
		})
	}
}

func TestSpawnLoginShellOnWrapsTheAgentArgvAndKeepsTheRosterUnwrapped(t *testing.T) {
	// Mutations: (argv shape) dropping "-l" or changing the script; (roster
	// unwrapped) recording the wrapped argv in o/args; (hc-only) see the
	// legacy test.
	skipLoginShellOnWindows(t)
	for _, shellName := range []string{"zsh", "bash", "ksh", "sh"} {
		t.Run(shellName, func(t *testing.T) {
			cwd, bin, state, env, ctx, shell := loginShellFixture(t, shellName)
			env["SHELL"] = shell
			env["SOHO_LOGIN_SHELL"] = "on"
			res := core.ResolveRoleSettings("implementer", ctx, env, cwd, core.RoleFlags{})
			agent := hcAgentArgs(t, "implementer", res, ctx, env, cwd)
			conn := newFakeSpawnRPCConn(rpcHelloSuccess + "\n" + rpcRunSuccess + "\n")
			out, _, calls := spawnHcWithConn(t, bin, []string{"implementer"}, ctx, env, cwd, conn)
			assertHcOnlyReads(t, bin, calls)
			_, params := parseTerminalRunReq(t, conn.getWritten())
			want := append([]string{shell, "-l", "-c", loginShellScript, filepath.Join(bin, "pi")}, agent...)
			if got := runArgv(t, params); strings.Join(got, "\x00") != strings.Join(want, "\x00") {
				t.Fatalf("argv=%q, want %q", got, want)
			}
			// Everything else in the request is unchanged.
			if params["name"] != "implementer" || params["cwd"] != cwd || params["caller"] != "t1" {
				t.Fatalf("name/cwd/caller changed: %v", params)
			}
			if params["no_notify"] != true || params["outlive_parent"] != true || params["wait"] != false {
				t.Fatalf("flags changed: %v", params)
			}
			if childEnv, _ := params["env"].(map[string]any); childEnv["SOHO_TRANSPORT"] != "hc" || len(childEnv) != 1 {
				t.Fatalf("env=%v, want exactly SOHO_TRANSPORT=hc", params["env"])
			}
			// The JSON and the roster keep the agent's own argv.
			if got := spawnJSONString(t, out.String(), "agent_args"); got != strings.Join(agent, " ") {
				t.Fatalf("agent_args=%q, want the unwrapped %q", got, strings.Join(agent, " "))
			}
			row := strings.Join(rosterRow(t, state, "sX"), "\t")
			if strings.Contains(row, shell) || strings.Contains(row, loginShellScript) || strings.Contains(row, "-l -c") {
				t.Fatalf("roster row records the wrapped argv: %q", row)
			}
			if !strings.Contains(row, strings.Join(agent, " ")) {
				t.Fatalf("roster row %q lacks the agent's own args %q", row, strings.Join(agent, " "))
			}
		})
	}
}

func TestSpawnLoginShellPassesTheShellInTheStartRequest(t *testing.T) {
	// The spawn layer's contract with the backend: Start.LoginShell is the
	// shell path with login_shell=on and empty otherwise, Start.Args stays
	// the agent's own args (the wrapping is the backend's).
	skipLoginShellOnWindows(t)
	capture := func(loginShell string) (transport.Start, string, []string) {
		cwd, bin, _, env, ctx, shell := loginShellFixture(t, "zsh")
		env["SHELL"] = shell
		if loginShell != "" {
			env["SOHO_LOGIN_SHELL"] = loginShell
		}
		res := core.ResolveRoleSettings("implementer", ctx, env, cwd, core.RoleFlags{})
		agent := hcAgentArgs(t, "implementer", res, ctx, env, cwd)
		start, _ := captureSpawn(t, bin, env, ctx, cwd, []string{"implementer"})
		return start, shell, agent
	}
	off, _, _ := capture("")
	if off.LoginShell != "" {
		t.Fatalf("default off: Start.LoginShell=%q, want empty", off.LoginShell)
	}
	on, shell, agent := capture("on")
	if on.LoginShell != shell {
		t.Fatalf("on: Start.LoginShell=%q, want %q", on.LoginShell, shell)
	}
	if strings.Join(on.Args, "\x00") != strings.Join(agent, "\x00") {
		t.Fatalf("on: Start.Args=%q, want the agent's own %q", on.Args, agent)
	}
}

func TestSpawnLoginShellAgentArgumentsReachTheArgvByteForByte(t *testing.T) {
	// Argument injection: nothing is interpolated into the command string.
	skipLoginShellOnWindows(t)
	cwd, bin, _, env, ctx, shell := loginShellFixture(t, "bash")
	env["SHELL"] = shell
	env["SOHO_LOGIN_SHELL"] = "on"
	hostile := []string{
		"two words",
		`it's "quoted" and \back`,
		"$(touch /tmp/pwned) `id` ${HOME} $0 $@ ; | & > <",
		"line one\nline two\n",
		"--flag=with space",
		"",
	}
	conn := newFakeSpawnRPCConn(rpcHelloSuccess + "\n" + rpcRunSuccess + "\n")
	argv := append([]string{"implementer", "--"}, hostile...)
	spawnHcWithConn(t, bin, argv, ctx, env, cwd, conn)
	_, params := parseTerminalRunReq(t, conn.getWritten())
	got := runArgv(t, params)
	if len(got) < 5+len(hostile) {
		t.Fatalf("argv too short: %q", got)
	}
	if got[0] != shell || got[1] != "-l" || got[2] != "-c" || got[3] != loginShellScript || got[4] != filepath.Join(bin, "pi") {
		t.Fatalf("wrapper prefix=%q", got[:5])
	}
	tail := got[len(got)-len(hostile):]
	if strings.Join(tail, "\x00") != strings.Join(hostile, "\x00") {
		t.Fatalf("agent arguments changed on the way: got %q, want %q", tail, hostile)
	}
	for i, a := range got[:4] {
		if i > 0 && strings.Contains(a, "touch") {
			t.Fatalf("an agent argument leaked into the shell prefix: %q", got[:4])
		}
	}
}

func TestSpawnLoginShellRefusals(t *testing.T) {
	// Mutation (shell validation): dropping the absolute, base-name, mode or
	// regular-file check lets the matching row through to a terminal.
	skipLoginShellOnWindows(t)
	const want = "spawn: login_shell=on needs SHELL to be an absolute path to zsh, bash, ksh or sh"
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, dir string, env platform.Env)
	}{
		{"SHELL unset", func(t *testing.T, dir string, env platform.Env) { delete(env, "SHELL") }},
		{"SHELL empty", func(t *testing.T, dir string, env platform.Env) { env["SHELL"] = "" }},
		{"relative name", func(t *testing.T, dir string, env platform.Env) { env["SHELL"] = "zsh" }},
		{"relative name that exists in the working directory", func(t *testing.T, dir string, env platform.Env) {
			writeShell(t, filepath.Join(dir, "zsh"), 0o700)
			t.Chdir(dir)
			env["SHELL"] = "zsh"
		}},
		{"relative path", func(t *testing.T, dir string, env platform.Env) { env["SHELL"] = "shells/zsh" }},
		{"fish", func(t *testing.T, dir string, env platform.Env) {
			writeShell(t, filepath.Join(dir, "fish"), 0o700)
			env["SHELL"] = filepath.Join(dir, "fish")
		}},
		{"zsh with a suffix", func(t *testing.T, dir string, env platform.Env) {
			writeShell(t, filepath.Join(dir, "zsh-5.9"), 0o700)
			env["SHELL"] = filepath.Join(dir, "zsh-5.9")
		}},
		{"not executable", func(t *testing.T, dir string, env platform.Env) {
			writeShell(t, filepath.Join(dir, "zsh"), 0o600)
			env["SHELL"] = filepath.Join(dir, "zsh")
		}},
		{"missing file", func(t *testing.T, dir string, env platform.Env) {
			env["SHELL"] = filepath.Join(dir, "zsh")
		}},
		{"directory", func(t *testing.T, dir string, env platform.Env) {
			if err := os.Mkdir(filepath.Join(dir, "zsh"), 0o700); err != nil {
				t.Fatal(err)
			}
			env["SHELL"] = filepath.Join(dir, "zsh")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cwd, bin, _, env, ctx, _ := loginShellFixture(t, "unused")
			env["SOHO_LOGIN_SHELL"] = "on"
			dir := t.TempDir()
			tc.setup(t, dir, env)
			conn := newFakeSpawnRPCConn(rpcHelloSuccess + "\n" + rpcRunSuccess + "\n")
			exit, _, stderr, calls := spawnHcExitWithConn(t, bin, []string{"implementer"}, ctx, env, cwd, conn)
			assertRefusedBeforeTerminal(t, exit, want, conn, calls, stderr.String())
		})
	}
}

func TestSpawnLoginShellOpencodeRefused(t *testing.T) {
	// Mutation: removing the opencode refusal lets the wrapped launch through.
	skipLoginShellOnWindows(t)
	cwd, bin, _, env, ctx := hcSpawnFixture(t, "writer", "opencode")
	if _, err := fakecli.Install(t, bin, "hc", hcFixtureRules()); err != nil {
		t.Fatal(err)
	}
	shell := filepath.Join(t.TempDir(), "zsh")
	writeShell(t, shell, 0o700)
	env["SHELL"] = shell
	env["SOHO_LOGIN_SHELL"] = "on"
	conn := newFakeSpawnRPCConn(rpcHelloSuccess + "\n" + rpcRunSuccess + "\n")
	exit, _, stderr, calls := spawnHcExitWithConn(t, bin, []string{"writer", "--model", opencodePrimaryModel, "--approvals", "full"}, ctx, env, cwd, conn)
	assertRefusedBeforeTerminal(t, exit, "spawn: login_shell=on is not supported for opencode workers", conn, calls, stderr.String())
}

func TestSpawnLoginShellWindowsRefused(t *testing.T) {
	// Mutation: removing the platform check lets a Windows spawn through.
	setStartupPlatform(t, "win32")
	cwd, bin, _, env, ctx, shell := loginShellFixture(t, "zsh")
	env["SHELL"] = shell
	env["SOHO_LOGIN_SHELL"] = "on"
	conn := newFakeSpawnRPCConn(rpcHelloSuccess + "\n" + rpcRunSuccess + "\n")
	exit, _, stderr, calls := spawnHcExitWithConn(t, bin, []string{"implementer"}, ctx, env, cwd, conn)
	assertRefusedBeforeTerminal(t, exit, "spawn: login_shell=on is not supported on Windows", conn, calls, stderr.String())
	// Control: with the key off the same simulated platform is not refused.
	env["SOHO_LOGIN_SHELL"] = "off"
	conn = newFakeSpawnRPCConn(rpcHelloSuccess + "\n" + rpcRunSuccess + "\n")
	exit, _, _, _ = spawnHcExitWithConn(t, bin, []string{"implementer"}, ctx, env, cwd, conn)
	if exit != nil {
		t.Fatalf("login_shell=off on the simulated Windows platform exited: %+v", exit)
	}
}

func writeShell(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// assertRefusedBeforeTerminal pins one login_shell refusal: exit 2 with the
// exact one-line message, and no terminal: Core was never connected, no
// terminal.run went out and the fake hc saw only reads.
func assertRefusedBeforeTerminal(t *testing.T, exit *platform.ExitError, wantMsg string, conn *fakeSpawnRPCConn, calls []fakecli.Call, stderr string) {
	t.Helper()
	if exit == nil {
		t.Fatalf("spawn did not exit; want exit 2 %q", wantMsg)
	}
	if exit.Code != 2 || exit.Msg != wantMsg {
		t.Fatalf("exit=%d msg=%q, want exit 2 msg %q (stderr %q)", exit.Code, exit.Msg, wantMsg, stderr)
	}
	if n := conn.getConnectCount(); n != 0 {
		t.Fatalf("Core connected %d time(s) before the refusal", n)
	}
	if len(parseAllTerminalRunReqs2(conn.getWritten())) != 0 {
		t.Fatalf("a terminal.run went out: %q", conn.getWritten())
	}
	for _, c := range calls {
		switch c.Argv[0] {
		case "get", "ls":
		default:
			t.Fatalf("hc call %q after a refusal; want reads only", c.Argv)
		}
	}
}

func parseAllTerminalRunReqs2(written []byte) []string {
	var runs []string
	for _, line := range strings.Split(string(written), "\n") {
		if strings.Contains(line, `"terminal.run"`) {
			runs = append(runs, line)
		}
	}
	return runs
}

func TestSpawnLoginShellIsIgnoredOnTheLegacyTransport(t *testing.T) {
	// Mutation (hc-only): dropping the transport condition makes the invalid
	// SHELL below refuse a legacy spawn. The agent start argv stays the plain
	// one: the legacy start has no program to wrap.
	root := t.TempDir()
	cwd := filepath.Join(root, "repo")
	state := filepath.Join(root, "state")
	roles := filepath.Join(root, "roles")
	bin := filepath.Join(root, "bin")
	for _, dir := range []string{cwd, state, roles, bin, filepath.Join(root, "skill", "roles")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(roles, "worker.md"), []byte("---\nkind: grok\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rules := []fakecli.Rule{
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[]}}`},
		{Argv: []string{"agent", "start"}, ArgvPrefix: true, Stdout: `{"result":{"started":true}}`},
		{Argv: []string{"agent", "get"}, ArgvPrefix: true, Stdout: `{"result":{"agent":{"agent_status":"working"}}}`},
		{Argv: []string{"agent", "read"}, ArgvPrefix: true, Stdout: "ready\n"},
		{Argv: []string{"pane", "list"}, ArgvPrefix: true, Stdout: `{"result":{"panes":[]}}`},
		{Argv: []string{"pane", "layout"}, ArgvPrefix: true, Stdout: `{"result":{"layout":{"area":{"width":1000,"height":700},"panes":[{"pane_id":"p1","rect":{"x":0,"y":0,"width":1000,"height":700}}]}}`},
		{Argv: []string{"pane", "split"}, ArgvPrefix: true, Stdout: `{"result":{"pane":{"pane_id":"p2"}}}`},
	}
	if _, err := fakecli.Install(t, bin, "herdr", rules); err != nil {
		t.Fatal(err)
	}
	if _, err := fakecli.Install(t, bin, "grok", nil); err != nil {
		t.Fatal(err)
	}
	run := func(loginShell string) []string {
		env := platform.Env{
			"PATH": bin, "HOME": root, "TMPDIR": root, "SOHO_FAKECLI_CONFIG": bin,
			"SOHO_SKILL_DIR": filepath.Join(root, "skill"), "SOHO_ROLES": roles,
			"SOHO_DIR": state, "HERDR_WORKSPACE_ID": "ws", "HERDR_ENV": "1",
			"HERDR_PANE_ID": "p1", "SOHO_LANES": "off", "SOHO_LAYOUT": "split",
			"SOHO_REGRID": "off", "SOHO_WAIT_POLL_MS": "1", "SHELL": "fish",
			"SOHO_REUSE_WORKERS": "off",
		}
		if loginShell != "" {
			env["SOHO_LOGIN_SHELL"] = loginShell
		}
		ctx := &core.Config{Entries: map[string]core.ConfigEntry{}}
		oldOut, oldErr := platform.Stdout, platform.Stderr
		var out, stderr strings.Builder
		platform.Stdout, platform.Stderr = &out, &stderr
		defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
		before, _ := fakecli.ReadCalls(filepath.Join(bin, "herdr.calls.jsonl"))
		CmdSpawn([]string{"worker", "--", "--tail", "two words"}, ctx, env, cwd)
		calls, err := fakecli.ReadCalls(filepath.Join(bin, "herdr.calls.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		for _, call := range calls[len(before):] {
			if len(call.Argv) >= 2 && call.Argv[0] == "agent" && call.Argv[1] == "start" {
				return call.Argv
			}
		}
		t.Fatalf("no agent start call (login_shell=%q); stderr %q", loginShell, stderr.String())
		return nil
	}
	plain := run("")
	if len(plain) == 0 || plain[len(plain)-1] != "two words" {
		t.Fatalf("plain agent start argv=%q", plain)
	}
	if got := run("on"); strings.Join(got, "\x00") != strings.Join(plain, "\x00") {
		t.Fatalf("login_shell=on changed the legacy agent start argv: got %q, want %q", got, plain)
	}
}
