package wait

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/provider"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// The narrow startup causes are terminal on the first observation of their
// own CLI kind. They must not send a key, retry a prompt or wait for a second
// identical provider line.
func TestWaitNarrowStartupProviderStop(t *testing.T) {
	const agy = "⚠ failed to construct executor: plan model not specified; you must specify a valid model\n" +
		"⚠ Warning\n" +
		"  ⎿  model claude-sonnet-5-5-medium is not recognized as a known model or custom model in settings. Ignoring the flag.\n"
	const codex = "■ Failed to start turn: turn/start failed in TUI: turn/start failed: failed to load configuration:\n" +
		"invalid transport\n" +
		"in `mcp_servers.old-server`\n" +
		" (code -32600)\n"

	for _, tc := range []struct {
		name, kind, screen, cause string
	}{
		{name: "agy model", kind: "agy", screen: agy, cause: provider.AgyModelCause},
		{name: "codex config", kind: "codex", screen: codex, cause: provider.CodexConfigCause},
		{name: "codex screen on claude", kind: "claude", screen: codex},
		{name: "agy screen on claude", kind: "claude", screen: agy},
		{name: "codex stale screen", kind: "codex", screen: codex + "ready for work\n"},
		{name: "codex healthy", kind: "codex", screen: "ready for work\n"},
		{name: "codex previously seen while working", kind: "codex", screen: codex},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newQueuedProbeFixture(t, "idle", "5", tc.screen, nil)
			isolateProviderPlanFixture(t, f)
			roster := filepath.Join(f.sd, "agents.tsv")
			if err := os.WriteFile(roster, []byte("worker\tp0a\t"+tc.kind+"\timplementer\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if tc.cause == "" {
				if tc.name == "codex previously seen while working" {
					f.ctx.Entries["settled_grace"] = core.ConfigEntry{Value: "45"}
					f.bin = t.TempDir()
					f.env["PATH"], f.env["SOHO_FAKECLI_CONFIG"] = f.bin, f.bin
					if _, err := fakecli.Install(t, f.bin, "herdr", []fakecli.Rule{
						{Argv: []string{"agent", "get", "worker"}, Call: 1, Stdout: `{"result":{"agent":{"agent_status":"working","state_change_seq":5}}}`},
						{Argv: []string{"agent", "get", "worker"}, Stdout: `{"result":{"agent":{"agent_status":"idle","state_change_seq":5}}}`},
						{Argv: []string{"agent", "read", "worker"}, ArgvPrefix: true, Stdout: tc.screen},
					}); err != nil {
						t.Fatal(err)
					}
					if state := f.probe(""); state != "working" {
						t.Fatalf("before idle: state=%q", state)
					}
				}
				for range 2 {
					if state := f.probe(""); state == "provider-error" {
						t.Fatalf("narrow stop reported for %s", tc.name)
					}
					if _, err := os.Stat(f.marker("provider")); !os.IsNotExist(err) {
						t.Fatalf("narrow stop reached generic debounce: %v", err)
					}
				}
				return
			}
			var out bytes.Buffer
			old := platform.Stdout
			platform.Stdout = &out
			t.Cleanup(func() { platform.Stdout = old })
			start := time.Now()
			rc := WaitFor([]string{"worker"}, f.sd, f.ctx, f.env, 3000, false, t.TempDir())
			if rc != 14 || !strings.Contains(out.String(), `"status":"provider-error"`) || !strings.Contains(out.String(), tc.cause) {
				t.Fatalf("exit=%d output=%s", rc, out.String())
			}
			if time.Since(start) > 2*time.Second {
				t.Fatalf("first-observation detection waited %s", time.Since(start))
			}
			calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			gets := 0
			for _, c := range calls {
				if len(c.Argv) > 1 {
					if c.Argv[1] == "prompt" || c.Argv[1] == "send-keys" {
						t.Fatalf("automatic retry: %#v", c)
					}
					if c.Argv[1] == "get" {
						gets++
					}
				}
			}
			if gets != 1 {
				t.Fatalf("observations=%d want the first observation", gets)
			}
		})
	}
}
