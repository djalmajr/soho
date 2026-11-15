package plugin

import (
	contextpkg "context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestPickerPTYProbeEntrypoint(t *testing.T) {
	executable := os.Getenv("HERDR_SOHO_PICKER_PTY_EXE")
	if executable == "" {
		return
	}
	os.Exit(RunPicker(platform.EnvFromOS(), platform.Current(), executable))
}

func TestPickerRunExecutesAbsoluteCmdThroughPlatformInvocation(t *testing.T) {
	// Mutation captured: invoking the absolute .cmd file directly bypasses platform.CmdInvocation and loses Windows command-shell argument handling.
	dir := t.TempDir()
	targetDir := filepath.Join(dir, "exact path")
	pathDir := filepath.Join(dir, "path")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(pathDir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(targetDir, "tool.cmd")
	if err := os.WriteFile(target, []byte("@echo off\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fakecli.Install(t, pathDir, "tool.cmd", []fakecli.Rule{{AnyArgs: true}}); err != nil {
		t.Fatal(err)
	}
	cmd, err := fakecli.Install(t, pathDir, "cmd", []fakecli.Rule{{AnyArgs: true}})
	if err != nil {
		t.Fatal(err)
	}
	entries := fakecli.Env(testutil.CleanEnv(t), pathDir)
	env := platform.Env{"COMSPEC": cmd}
	for _, entry := range entries {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			env[key] = value
		}
	}
	got := pickerRun(contextpkg.Background(), target, []string{"find", "has space"}, env, "win32", 5000, "")
	if got.NotFound || got.Status == nil || *got.Status != 0 || got.Resolved != target {
		t.Fatalf("pickerRun result=%#v, want successful exact-path run", got)
	}
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(pathDir, "cmd.json"))
	if err != nil || len(calls) != 1 {
		t.Fatalf("cmd invocations=%#v err=%v", calls, err)
	}
	invocation := platform.CmdInvocation(target, []string{"find", "has space"}, env)
	if len(invocation.Args) != 4 || !invocation.WindowsVerbatimArguments || !strings.Contains(invocation.Args[3], `exact^ path\tool.cmd`) || !strings.Contains(invocation.Args[3], `^"find^"`) || !strings.Contains(invocation.Args[3], `^"has^ space^"`) {
		t.Fatalf("raw cmd invocation=%#v, want escaped exact .cmd path and arguments", invocation)
	}
	if len(calls[0].Argv) < 3 || calls[0].Argv[0] != "/d" || calls[0].Argv[1] != "/s" || calls[0].Argv[2] != "/c" {
		t.Fatalf("cmd process argv=%q; raw cmd invocation argv=%q", calls[0].Argv, invocation.Args)
	}
}
