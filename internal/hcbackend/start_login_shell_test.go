package hcbackend

import (
	"context"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
	"github.com/djalmajr/soho/internal/transport"
)

// TestStartLoginShellArgvShape: with Start.LoginShell set the terminal.run
// program is the shell and the argv is `-l -c 'exec "$0" "$@"'` followed by
// the resolved agent executable and the agent's own args, element by element;
// the rest of the request is the same as without a shell.
func TestStartLoginShellArgvShape(t *testing.T) {
	shell := filepath.Join(t.TempDir(), "zsh")
	if !filepath.IsAbs(shell) {
		t.Fatalf("fixture shell %q is not absolute", shell)
	}
	args := []string{"--model", "m", "two words", `it's "q" $(id)`, "a\nb"}
	start := func(t *testing.T, loginShell string) (map[string]any, string) {
		t.Helper()
		agentDir := filepath.Join(t.TempDir(), "agents")
		exe := installFakeAgentExecutable(t, agentDir, "claude")
		env, conn, calls := startEnv(t, []fakecli.Rule{
			{Argv: []string{"get", "t7", "--json"}, Call: 1, Stdout: startReadyIdle},
		})
		env["PATH"] = agentDir + string(filepath.ListSeparator) + env["PATH"]
		if runtime.GOOS == "windows" {
			env["PATHEXT"] = ".exe"
		}
		b := newBackend(env, conn)
		rep := b.Start(context.Background(), transport.Start{Name: "w7", Kind: "claude", Cwd: "/tmp/w7", Timeout: "3000", Args: args, LoginShell: loginShell}, transport.Call{})
		if rep.Err != nil || !rep.Valid || rep.Value != "t7" {
			t.Fatalf("rep: %+v", rep)
		}
		wantArgv(t, calls(), [][]string{{"get", "t1", "--json"}, {"get", "t7", "--json"}})
		_, params := parseTerminalRunReq(t, conn.getWritten())
		return params, exe
	}

	t.Run("on", func(t *testing.T) {
		params, exe := start(t, shell)
		want := append([]any{shell, "-l", "-c", `exec "$0" "$@"`, exe}, toAnySlice(args)...)
		if !reflect.DeepEqual(params["argv"], want) {
			t.Fatalf("argv: %#v want %#v", params["argv"], want)
		}
		if params["cwd"] != "/tmp/w7" || params["name"] != "w7" || params["caller"] != "t1" ||
			params["no_notify"] != true || params["outlive_parent"] != true || params["wait"] != false {
			t.Fatalf("request changed by the shell: %v", params)
		}
		if !reflect.DeepEqual(params["env"], map[string]any{"SOHO_TRANSPORT": "hc"}) ||
			!reflect.DeepEqual(params["labels"], map[string]any{"soho.session": "sX"}) {
			t.Fatalf("env/labels changed by the shell: %v / %v", params["env"], params["labels"])
		}
	})
	t.Run("off keeps the plain argv", func(t *testing.T) {
		params, exe := start(t, "")
		want := append([]any{exe}, toAnySlice(args)...)
		if !reflect.DeepEqual(params["argv"], want) {
			t.Fatalf("argv: %#v want %#v", params["argv"], want)
		}
	})
}

// TestStartLoginShellRefusesRelativeShell: the backend's only check of the
// shell; a relative value fails with a FailureCommand before Core is reached.
func TestStartLoginShellRefusesRelativeShell(t *testing.T) {
	for _, shell := range []string{"zsh", "bin/zsh", "./zsh"} {
		t.Run(shell, func(t *testing.T) {
			env, conn, calls := startEnv(t, nil)
			b := newBackend(env, conn)
			rep := b.Start(context.Background(), transport.Start{Name: "w7", Kind: "claude", Timeout: "3000", LoginShell: shell}, transport.Call{})
			if rep.Err == nil || rep.Err.Kind != transport.FailureCommand || rep.Valid {
				t.Fatalf("rep: %+v, want an invalid FailureCommand", rep)
			}
			if n := conn.getConnectCount(); n != 0 {
				t.Fatalf("Core connected %d time(s)", n)
			}
			if got := calls(); len(got) != 0 {
				t.Fatalf("hc calls before the refusal: %v", got)
			}
		})
	}
}
