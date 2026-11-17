package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestDoctorRoutesThroughGoAndReportsFirstRun(t *testing.T) { // JS: "doctor first-run detection (test-friendly.sh)"
	env, repo := commandFixture(t)
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, stderr bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &stderr
	t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
	if code := Run([]string{"doctor"}, env); code != 0 || stderr.Len() != 0 || !strings.Contains(out.String(), "first_run: true\n") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), stderr.String())
	}
	settings := filepath.Join(repo, ".claude", "settings.json")
	if !strings.Contains(out.String(), "no soho hooks in .claude/settings.json") {
		t.Fatalf("missing hook diagnosis: %s", out.String())
	}
	_ = settings
}

func TestInitRenamesTakenOrchestratorNameThroughFakeHerdr(t *testing.T) { // JS: "init: when orchestrator is live the rename takes the unique suffix"
	env, repo := commandFixture(t)
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	fakeDir := t.TempDir()
	rules := []fakecli.Rule{
		{Argv: []string{"--version"}, Stdout: "herdr 1.2.3\n"},
		{Argv: []string{"status", "server"}, Stdout: "server 1.2.3\n"},
		{Argv: []string{"agent", "get", "p1"}, Stdout: `{"result":{"agent":{"name":"reviewer"}}}`},
		{Argv: []string{"agent", "get", "p1"}, Stdout: `{"result":{"agent":{"name":"reviewer"}}}`},
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[{"name":"orchestrator"}]}}`},
		{Argv: []string{"agent", "rename", "p1", "orchestrator-2"}},
		{Argv: []string{"pane", "get", "p1"}, Stdout: `{"result":{"pane":{"title":"current task","workspace_id":"ws"}}}`},
	}
	if _, err = fakecli.Install(t, fakeDir, "herdr", rules); err != nil {
		t.Fatal(err)
	}
	env = withFakeCLI(env, fakeDir)
	env["HERDR_ENV"] = "1"
	env["HERDR_PANE_ID"] = "p1"
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, stderr bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &stderr
	t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
	if code := Run([]string{"init"}, env); code != 0 || !strings.Contains(out.String(), `"orchestrator": "orchestrator-2"`) || !strings.Contains(out.String(), `"title": "current task"`) {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), stderr.String())
	}
}

func TestDoctorCodexHooksWindowsCommands(t *testing.T) {
	env, repo := tm6CLIEnv(t)
	bin := t.TempDir()
	_, err := fakecli.Install(t, bin, "herdr", []fakecli.Rule{
		{Argv: []string{"agent", "list"}, Stdout: `{"result":{"agents":[{"name":"review","pane_id":"p2"}]}}`},
		{AnyArgs: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	env = withFakeCLI(env, bin)
	env["HERDR_ENV"] = "1"
	env["HERDR_WORKSPACE_ID"] = "ws"
	tm6Write(t, filepath.Join(env.Get("SOHO_DIR"), "ws", "agents.tsv"), "# name\tpane\tkind\trole\nreview\tp2\tcodex\treviewer\n")
	tm6Write(t, filepath.Join(repo, ".claude", "settings.json"), `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"soho hook reminder"}]}],"SessionStart":[{"hooks":[{"type":"command","command":"soho hook doctor"}]}]}}`)
	// A missing Codex hook must trigger the warning, proving that the live roster was inspected.
	code, out, stderr := runIn(t, []string{"doctor"}, env, repo)
	if code != 0 || !strings.Contains(out, "review (codex) is not protected") {
		t.Fatalf("control code=%d out=%s stderr=%s", code, out, stderr)
	}
	path := filepath.Join(repo, ".codex", "hooks.json")
	for _, tc := range []struct{ name, reminder, doctor string }{
		{"guarded", `sh -c 'command -v soho >/dev/null 2>&1 || exit 0; exec soho hook reminder --codex'`, `sh -c 'command -v soho >/dev/null 2>&1 || exit 0; exec soho hook doctor --codex'`},
		{"plain", `soho hook reminder --codex`, `soho hook doctor --codex`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := map[string]any{"hooks": map[string]any{}}
			hooks := doc["hooks"].(map[string]any)
			for _, event := range []struct{ name, cmd, windows string }{{"UserPromptSubmit", tc.reminder, "soho hook reminder --codex"}, {"SessionStart", tc.doctor, "soho hook doctor --codex"}} {
				hooks[event.name] = []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": event.cmd, "commandWindows": event.windows}}}}
			}
			raw, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			tm6Write(t, path, string(raw))
			code, out, stderr := runIn(t, []string{"doctor"}, env, repo)
			for _, line := range strings.Split(out+"\n"+stderr, "\n") {
				if strings.Contains(line, "warn") && strings.Contains(strings.ToLower(line), "hook") {
					t.Fatalf("unexpected hook warning: %s\n%s", line, out)
				}
			}
			if code != 0 || strings.Contains(out, "review (codex) is not protected") || strings.Contains(out, "review (codex) is missing") || strings.Contains(stderr, "review (codex)") {
				t.Fatalf("%s code=%d out=%s stderr=%s", tc.name, code, out, stderr)
			}
		})
	}
}
