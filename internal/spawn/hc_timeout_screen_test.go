package spawn

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// This fixture speaks the two RPC responses when the real CLI launches fake hc.
func init() {
	if len(os.Args) != 3 || os.Args[1] != "connect" || os.Args[2] != "--json" {
		return
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(os.Args[0]), "screen-rpc-marker")); err != nil {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	if scanner.Scan() {
		fmt.Fprintln(os.Stdout, rpcHelloSuccess)
	}
	if scanner.Scan() {
		fmt.Fprintln(os.Stdout, rpcRunSuccess)
	}
	os.Exit(0)
}

// TestSpawnHcNeverReadyNamesTheScreenBeforeRemoval: a native worker that
// never reports ready ends spawn with exit 4 and a message that carries the
// last visible line of its screen. The screen read is the Core call before
// the terminal's removal (kill, then rm), and spawn sends no input.
func TestSpawnHcNeverReadyNamesTheScreenBeforeRemoval(t *testing.T) {
	cwd, bin, _, env, ctx := hcSpawnFixture(t, "worker", "pi")
	rules := append(hcFixtureRules(),
		fakecli.Rule{Argv: []string{"get", "t7", "--json"}, Stdout: `{"address":"t7","state":"live","reported":false,"status":{"state":"working"}}`},
		fakecli.Rule{Argv: []string{"read", "t7", "--lines", "0", "--json"}, Stdout: `{"text":"\u001b[31mTrust project folder?\u001b[0m\n  1. Yes, trust\n  2. No, exit\n\n","complete":true}`},
		fakecli.Rule{Argv: []string{"kill", "t7", "--json"}, Stdout: `{"address":"t7","state":"finished"}`},
		fakecli.Rule{Argv: []string{"rm", "t7", "--json"}, Stdout: `{"address":"t7"}`},
	)
	if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
		t.Fatal(err)
	}
	conn := newFakeSpawnRPCConn(rpcHelloSuccess + "\n" + rpcRunSuccess + "\n")
	exit, _, _, calls := spawnHcExitWithConn(t, bin, []string{"worker", "--kind", "pi", "--timeout", "300"}, ctx, env, cwd, conn)
	want := `agent worker still not ready after 0.3s; last visible line: "2. No, exit"`
	if exit == nil || exit.Code != 4 || !strings.HasPrefix(exit.Msg, want+"; screen saved to ") {
		t.Fatalf("exit=%+v, want exit 4 with %q", exit, want)
	}
	path := strings.TrimPrefix(exit.Msg, want+"; screen saved to ")
	dir := filepath.Join(core.StateDirPath(ctx, env, cwd), "spawn-screens")
	if filepath.Dir(path) != dir {
		t.Fatalf("path=%q, want under %q", path, dir)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "Trust project folder?\n  1. Yes, trust\n  2. No, exit\n\n" {
		t.Fatalf("screen=%q err=%v", data, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("file mode: %v %v", info, err)
		}
		dinfo, err := os.Stat(dir)
		if err != nil || dinfo.Mode().Perm() != 0o700 {
			t.Fatalf("dir mode: %v %v", dinfo, err)
		}
	}
	read, kill, rm, reads := -1, -1, -1, 0
	for i, c := range calls {
		switch c.Argv[0] {
		case "read":
			read = i
			reads++
		case "kill":
			kill = i
		case "rm":
			rm = i
		case "send", "keys":
			t.Fatalf("spawn sent input to the terminal: %v", c.Argv)
		}
	}
	if reads != 1 || read < 0 || kill < 0 || rm < 0 || !(read < kill && kill < rm) {
		t.Fatalf("read=%d kill=%d rm=%d reads=%d, want exactly one read before kill before rm: %v", read, kill, rm, reads, calls)
	}
	if strings.Count(exit.Msg, "last visible line") != 1 {
		t.Fatalf("message %q repeats the line", exit.Msg)
	}
}

func TestSpawnHcTimeoutScreenReadOnlyState(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("chmod does not enforce the fixture on Windows or as root")
	}
	cwd, bin, _, env, ctx := hcSpawnFixture(t, "worker", "pi")
	rules := append(hcFixtureRules(),
		fakecli.Rule{Argv: []string{"get", "t7", "--json"}, Stdout: `{"address":"t7","state":"live","reported":false,"status":{"state":"working"}}`},
		fakecli.Rule{Argv: []string{"read", "t7", "--lines", "0", "--json"}, Stdout: `{"text":"dialog\nfooter","complete":true}`},
		fakecli.Rule{Argv: []string{"kill", "t7", "--json"}, Stdout: `{"address":"t7","state":"finished"}`},
		fakecli.Rule{Argv: []string{"rm", "t7", "--json"}, Stdout: `{"address":"t7"}`},
	)
	if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
		t.Fatal(err)
	}
	state := core.StateDirPath(ctx, env, cwd)
	t.Cleanup(func() {
		if err := os.Chmod(state, 0o700); err != nil {
			t.Errorf("restore permissions: %v", err)
		}
	})
	conn := newFakeSpawnRPCConn(rpcHelloSuccess + "\n" + rpcRunSuccess + "\n")
	var chmodErr error
	chmodAttempted := false
	conn.beforeRead = func(req []byte) {
		if strings.Contains(string(req), `"method":"terminal.run"`) {
			chmodAttempted = true
			chmodErr = os.Chmod(state, 0o500)
		}
	}
	exit, _, _, calls := spawnHcExitWithConn(t, bin, []string{"worker", "--kind", "pi", "--timeout", "300"}, ctx, env, cwd, conn)
	if !chmodAttempted || chmodErr != nil {
		t.Fatalf("chmod %s: %v", state, chmodErr)
	}
	want := `agent worker still not ready after 0.3s; last visible line: "footer"`
	if exit == nil || exit.Code != 4 || exit.Msg != want {
		t.Fatalf("exit=%+v, want exit 4 %q", exit, want)
	}
	if _, err := os.Stat(filepath.Join(state, "spawn-screens")); !os.IsNotExist(err) {
		t.Fatalf("screen directory: %v", err)
	}
	removed := false
	for _, c := range calls {
		if len(c.Argv) > 0 && c.Argv[0] == "rm" {
			removed = true
		}
	}
	if !removed {
		t.Fatalf("terminal not removed: %v", calls)
	}
}

func TestSpawnHcTimeoutScreenLineLimit(t *testing.T) {
	cwd, bin, _, env, ctx := hcSpawnFixture(t, "worker", "pi")
	screen := strings.Repeat("\x1b[31mline\x1b[0m\n", 220) + "tail"
	body, err := json.Marshal(map[string]any{"text": screen, "complete": true})
	if err != nil {
		t.Fatal(err)
	}
	rules := append(hcFixtureRules(),
		fakecli.Rule{Argv: []string{"get", "t7", "--json"}, Stdout: `{"address":"t7","state":"live","reported":false,"status":{"state":"working"}}`},
		fakecli.Rule{Argv: []string{"read", "t7", "--lines", "0", "--json"}, Stdout: string(body)},
		fakecli.Rule{Argv: []string{"kill", "t7", "--json"}, Stdout: `{"address":"t7","state":"finished"}`},
		fakecli.Rule{Argv: []string{"rm", "t7", "--json"}, Stdout: `{"address":"t7"}`},
	)
	if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
		t.Fatal(err)
	}
	exit, _, _, _ := spawnHcExitWithConn(t, bin, []string{"worker", "--kind", "pi", "--timeout", "300"}, ctx, env, cwd, newFakeSpawnRPCConn(rpcHelloSuccess+"\n"+rpcRunSuccess+"\n"))
	prefix := `agent worker still not ready after 0.3s; last visible line: "tail"; screen saved to `
	if exit == nil || exit.Code != 4 || !strings.HasPrefix(exit.Msg, prefix) {
		t.Fatalf("exit=%+v, want %q", exit, prefix)
	}
	data, err := os.ReadFile(strings.TrimPrefix(exit.Msg, prefix))
	if err != nil || string(data) != strings.TrimSuffix(strings.Repeat("line\n", 200), "\n") {
		t.Fatalf("screen len=%d lines=%d err=%v", len(data), strings.Count(string(data), "\n")+1, err)
	}
}

func TestSpawnHcTimeoutScreenFrictionCLI(t *testing.T) {
	cwd, bin, _, env, _ := hcSpawnFixture(t, "worker", "pi")
	rules := append(hcFixtureRules(),
		fakecli.Rule{Argv: []string{"get", "t7", "--json"}, Stdout: `{"address":"t7","state":"live","reported":false,"status":{"state":"working"}}`},
		fakecli.Rule{Argv: []string{"read", "t7", "--lines", "0", "--json"}, Stdout: `{"text":"dialog\nfooter","complete":true}`},
		fakecli.Rule{Argv: []string{"kill", "t7", "--json"}, Stdout: `{"address":"t7","state":"finished"}`},
		fakecli.Rule{Argv: []string{"rm", "t7", "--json"}, Stdout: `{"address":"t7"}`},
	)
	if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "screen-rpc-marker"), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(env["SOHO_DIR"], "sX")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	goexe, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("go executable required for CLI consumer: %v", err)
	}
	cache, err := exec.Command(goexe, "env", "GOCACHE").Output()
	if err != nil {
		t.Fatalf("go env GOCACHE: %v", err)
	}
	env["PATH"] = bin + string(os.PathListSeparator) + testutil.HostPath()
	env["USERPROFILE"] = env["HOME"]
	env["XDG_CONFIG_HOME"] = filepath.Join(env["HOME"], "config")
	env["GOENV"] = "off"
	env["GOWORK"] = "off"
	env["GOCACHE"] = strings.TrimSpace(string(cache))
	cmd := exec.Command(goexe, "run", "./cmd/soho", "spawn", "worker", "--kind", "pi", "--timeout", "300", "--cwd", cwd)
	cmd.Dir = filepath.Join("..", "..")
	cmd.Env = env.List()
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "exit status 4") || !strings.Contains(string(output), `agent worker still not ready after 0.3s; last visible line: "footer"; screen saved to `) {
		t.Fatalf("CLI output=%q err=%v", output, err)
	}
	log, err := os.ReadFile(filepath.Join(state, "friction.log"))
	if err != nil || !strings.Contains(string(log), "screen saved to ") {
		t.Fatalf("friction=%q err=%v; CLI output=%q", log, err, output)
	}
	line := string(log)
	start := strings.Index(line, "screen saved to ") + len("screen saved to ")
	path := strings.TrimSpace(line[start:])
	if !strings.Contains(string(output), "screen saved to "+path+"\n") || !strings.Contains(line, `error(exit 4)`+"\tspawn\t"+`agent worker still not ready after 0.3s; last visible line: "footer"; screen saved to `+path) {
		t.Fatalf("friction=%q CLI=%q", line, output)
	}
	if filepath.Dir(path) != filepath.Join(state, "spawn-screens") {
		t.Fatalf("friction path=%q state=%q", path, state)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "dialog\nfooter" {
		t.Fatalf("screen=%q err=%v", data, err)
	}
}

func TestSpawnHcTimeoutScreenWriteUnavailable(t *testing.T) {
	for _, mode := range []string{"nowrite", "unwritable"} {
		t.Run(mode, func(t *testing.T) {
			cwd, bin, _, env, ctx := hcSpawnFixture(t, "worker", "pi")
			rules := append(hcFixtureRules(),
				fakecli.Rule{Argv: []string{"get", "t7", "--json"}, Stdout: `{"address":"t7","state":"live","reported":false,"status":{"state":"working"}}`},
				fakecli.Rule{Argv: []string{"read", "t7", "--lines", "0", "--json"}, Stdout: `{"text":"dialog\nfooter","complete":true}`},
				fakecli.Rule{Argv: []string{"kill", "t7", "--json"}, Stdout: `{"address":"t7","state":"finished"}`},
				fakecli.Rule{Argv: []string{"rm", "t7", "--json"}, Stdout: `{"address":"t7"}`},
			)
			if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(core.StateDirPath(ctx, env, cwd), "spawn-screens")
			if mode == "nowrite" {
				env["SOHO_NOWRITE"] = "1"
			} else {
				if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(dir, []byte("block"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			conn := newFakeSpawnRPCConn(rpcHelloSuccess + "\n" + rpcRunSuccess + "\n")
			exit, _, _, calls := spawnHcExitWithConn(t, bin, []string{"worker", "--kind", "pi", "--timeout", "300"}, ctx, env, cwd, conn)
			want := `agent worker still not ready after 0.3s; last visible line: "footer"`
			if exit == nil || exit.Code != 4 || exit.Msg != want {
				t.Fatalf("exit=%+v, want %q", exit, want)
			}
			found := false
			for _, call := range calls {
				if len(call.Argv) > 0 && call.Argv[0] == "rm" {
					found = true
				}
			}
			if !found {
				t.Fatalf("terminal not removed: %v", calls)
			}
			if mode == "nowrite" {
				if _, err := os.Stat(dir); !os.IsNotExist(err) {
					t.Fatalf("screen directory exists: %v", err)
				}
			}
		})
	}
}

// Drives the real start failure consumer with an OSC spanning a newline.
func TestR436MultilineOSCDoesNotPersistHiddenPayload(t *testing.T) {
	cwd, bin, _, env, ctx := hcSpawnFixture(t, "worker", "pi")
	rules := append(hcFixtureRules(),
		fakecli.Rule{Argv: []string{"get", "t7", "--json"}, Stdout: `{"address":"t7","state":"live","reported":false,"status":{"state":"working"}}`},
		fakecli.Rule{Argv: []string{"read", "t7", "--lines", "0", "--json"}, Stdout: `{"text":"visible\u001b]0;title\nhidden-startup-metadata\u0007after\nfooter","complete":true}`},
		fakecli.Rule{Argv: []string{"kill", "t7", "--json"}, Stdout: `{"address":"t7","state":"finished"}`},
		fakecli.Rule{Argv: []string{"rm", "t7", "--json"}, Stdout: `{"address":"t7"}`},
	)
	if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
		t.Fatal(err)
	}
	exit, _, _, _ := spawnHcExitWithConn(t, bin, []string{"worker", "--kind", "pi", "--timeout", "300"}, ctx, env, cwd, newFakeSpawnRPCConn(rpcHelloSuccess+"\n"+rpcRunSuccess+"\n"))
	prefix := `agent worker still not ready after 0.3s; last visible line: "footer"; screen saved to `
	if exit == nil || exit.Code != 4 || !strings.HasPrefix(exit.Msg, prefix) {
		t.Fatalf("exit=%+v", exit)
	}
	data, err := os.ReadFile(strings.TrimPrefix(exit.Msg, prefix))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "hidden-startup-metadata") {
		t.Fatalf("OSC payload persisted in screen file: %q", data)
	}
}

func TestR436TimeoutScreenRedactsAdmittedEnv(t *testing.T) {
	cwd, bin, _, env, ctx := hcSpawnFixture(t, "worker", "pi")
	env["SELECTED_CANARY"] = "FAKE-selected-private-canary"
	rules := append(hcFixtureRules(),
		fakecli.Rule{Argv: []string{"get", "t7", "--json"}, Stdout: `{"address":"t7","state":"live","reported":false,"status":{"state":"working"}}`},
		fakecli.Rule{Argv: []string{"read", "t7", "--lines", "0", "--json"}, Stdout: `{"text":"startup diagnostic: FAKE-selected-private-canary\nfooter","complete":true}`},
		fakecli.Rule{Argv: []string{"kill", "t7", "--json"}, Stdout: `{"address":"t7","state":"finished"}`},
		fakecli.Rule{Argv: []string{"rm", "t7", "--json"}, Stdout: `{"address":"t7"}`},
	)
	if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
		t.Fatal(err)
	}
	exit, _, _, _ := spawnHcExitWithConn(t, bin, []string{"worker", "--fresh", "--env-from", "SELECTED_CANARY", "--timeout", "300"}, ctx, env, cwd, newFakeSpawnRPCConn(rpcHelloSuccess+"\n"+rpcRunSuccess+"\n"))
	prefix := `agent worker still not ready after 0.3s; last visible line: "footer"; screen saved to `
	if exit == nil || exit.Code != 4 || !strings.HasPrefix(exit.Msg, prefix) {
		t.Fatalf("exit=%+v", exit)
	}
	data, err := os.ReadFile(strings.TrimPrefix(exit.Msg, prefix))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), env["SELECTED_CANARY"]) {
		t.Fatal("admitted private environment value persisted verbatim in screen file")
	}
}
