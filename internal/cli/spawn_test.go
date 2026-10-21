package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestSpawnEntryCatchJavaScriptCases(t *testing.T) {
	t.Run("entry catch: DieError with a message dies (max_workers → 8)", func(t *testing.T) {
		// JS: "entry catch: DieError with a message dies (max_workers → 8)"
		root := t.TempDir()
		bin, state := filepath.Join(root, "bin"), filepath.Join(root, "state")
		if err := os.MkdirAll(filepath.Join(state, "ws"), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := fakecli.Install(t, bin, "herdr", []fakecli.Rule{{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[{"name":"live","pane_id":"p-live","agent_status":"working"}]}}`}, {Argv: []string{"agent", "get", "live"}, Stdout: `{"result":{"agent":{"name":"live","agent_status":"working"}}}`}}); err != nil {
			t.Fatal(err)
		}
		if _, err := fakecli.Install(t, bin, "grok", nil); err != nil {
			t.Fatal(err)
		}
		row := "live\tp-live\tgrok\timplementer\txai\t1\t/tmp/work\tnow"
		if err := os.WriteFile(filepath.Join(state, "ws", "agents.tsv"), []byte(row+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		env := commandSpawnEnv(t, root, state, bin)
		env["SOHO_LANES"] = "off"
		env["SOHO_MAX_WORKERS"] = "1"
		oldOut, oldErr := platform.Stdout, platform.Stderr
		var out, errOut strings.Builder
		platform.Stdout, platform.Stderr = &out, &errOut
		t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
		code := Run([]string{"spawn", "implementer"}, env)
		if code != 8 || !strings.Contains(errOut.String(), "max_workers=1 reached") {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
		}
	})

	t.Run("entry catch: empty-message DieError exits with the code only (herdr passthrough)", func(t *testing.T) {
		// JS: "entry catch: empty-message DieError exits with the code only (herdr passthrough)"
		root := t.TempDir()
		bin, state := filepath.Join(root, "bin"), filepath.Join(root, "state")
		if err := os.MkdirAll(filepath.Join(state, "ws"), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := fakecli.Install(t, bin, "herdr", []fakecli.Rule{{Argv: []string{"agent", "list"}, Code: 29}}); err != nil {
			t.Fatal(err)
		}
		if _, err := fakecli.Install(t, bin, "grok", nil); err != nil {
			t.Fatal(err)
		}
		env := commandSpawnEnv(t, root, state, bin)
		env["SOHO_LANES"] = "off"
		oldOut, oldErr := platform.Stdout, platform.Stderr
		var out, errOut strings.Builder
		platform.Stdout, platform.Stderr = &out, &errOut
		t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
		code := Run([]string{"spawn", "implementer"}, env)
		if code != 29 || errOut.Len() != 0 || out.Len() != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
		}
	})
}

func commandSpawnEnv(t *testing.T, root, state, bin string) platform.Env {
	t.Helper()
	env := platform.Env{}
	for _, item := range fakecli.Env(testutil.CleanEnv(t), bin) {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			env[key] = value
		}
	}
	env["HOME"] = root
	env["HERDR_ENV"] = "1"
	env["HERDR_WORKSPACE_ID"] = "ws"
	env["SOHO_DIR"] = state
	env["SOHO_SKILL_DIR"] = filepath.Join(packageDir, "..", "..", "skills", "soho")
	env["SOHO_WAIT_POLL_MS"] = "1"
	return env
}
