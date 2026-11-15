//go:build !windows

package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
	testutil "github.com/djalmajr/soho/internal/testutil/testenv"
)

func TestTM11POSIXRunCliTimeoutDoesNotUseTreeKill(t *testing.T) {
	t.Run(`JS: "POSIX: runCli with a timeout never takes the treekill path (classic spawnSync shape, no helper artifacts)"`, func(t *testing.T) {
		bin := t.TempDir()
		if _, err := fakecli.Install(t, bin, "slowcli", []fakecli.Rule{{Argv: []string{}, Delay: 10000}}); err != nil {
			t.Fatal(err)
		}
		env := Env{}
		for _, item := range fakecli.Env(testutil.CleanEnv(t), bin) {
			key, value, ok := strings.Cut(item, "=")
			if ok {
				env[key] = value
			}
		}
		started := time.Now()
		got := RunCli("slowcli", nil, RunOptions{Env: env, TimeoutMs: 500})
		if elapsed := time.Since(started); elapsed > 3*time.Second {
			t.Fatalf("POSIX timeout took %s", elapsed)
		}
		if !got.TimedOut || got.Status != nil || got.Error != "ETIMEDOUT" {
			t.Fatalf("timeout result=%#v", got)
		}
	})
}

func TestTM11TreeKillHelperContractCases(t *testing.T) {
	t.Run(`JS: "treekill helper: a command that finishes before the timeout keeps its status, its streams and no error"`, func(t *testing.T) {
		bin := t.TempDir()
		if _, err := fakecli.Install(t, bin, "fastcli", []fakecli.Rule{{Stdout: "a-out\n", Stderr: "a-err\n", Code: 3}}); err != nil {
			t.Fatal(err)
		}
		env := Env{}
		for _, item := range fakecli.Env(testutil.CleanEnv(t), bin) {
			key, value, ok := strings.Cut(item, "=")
			if ok {
				env[key] = value
			}
		}
		got := RunCli("fastcli", nil, RunOptions{Env: env, TimeoutMs: 5000})
		if got.Status == nil || *got.Status != 3 || got.Signal != "" || got.TimedOut || got.Error != "" || got.Stdout != "a-out\n" || got.Stderr != "a-err\n" {
			t.Fatalf("RunCli result=%#v", got)
		}
	})
	t.Run(`JS: "treekill helper: past the timeout the killer receives the child pid and the result marks the timeout"`, func(t *testing.T) {
		// Go has no treekill helper process: the killer contract (a killer in
		// the temp dir gets the child pid) is proven here, and the timeout
		// shape of the same kill (status nil, SIGTERM, TimedOut, ETIMEDOUT, no
		// process of the tree left) end to end on Windows by
		// TestTM11WindowsTreeKillCases, where RunCli takes this path.
		tempDir := t.TempDir()
		killer := filepath.Join(tempDir, "killer")
		if err := os.WriteFile(killer, []byte("fake executable"), 0o700); err != nil {
			t.Fatal(err)
		}
		var gotPath string
		var gotArgs []string
		if !runTreeKillTestKiller(Env{"SOHO_TREEKILL_TEST_KILLER": killer}, tempDir, 4321, func(path string, args []string) {
			gotPath = path
			gotArgs = args
		}) {
			t.Fatal("temporary test killer was not accepted")
		}
		resolved, err := filepath.EvalSymlinks(killer)
		if err != nil {
			t.Fatal(err)
		}
		if gotPath != resolved || len(gotArgs) != 1 || gotArgs[0] != "4321" {
			t.Fatalf("killer invocation=%q %#v", gotPath, gotArgs)
		}
	})
	t.Run(`JS: "treekill helper: a killer outside the temp dir (relative, or a system executable) is ignored and taskkill runs"`, func(t *testing.T) {
		tempDir := t.TempDir()
		outside := filepath.Join(t.TempDir(), "killer")
		if err := os.WriteFile(outside, []byte("fake executable"), 0o700); err != nil {
			t.Fatal(err)
		}
		for name, value := range map[string]string{
			"relative":     filepath.Join("bin", "killer"),
			"outside temp": outside,
		} {
			t.Run(name, func(t *testing.T) {
				called := false
				if runTreeKillTestKiller(Env{"SOHO_TREEKILL_TEST_KILLER": value}, tempDir, 4321, func(string, []string) { called = true }) || called {
					t.Fatalf("unsafe test killer %q was accepted", value)
				}
			})
		}
	})
}
