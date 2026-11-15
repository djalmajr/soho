package plugin_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/plugin"
	"github.com/djalmajr/soho/internal/testutil"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestMain(m *testing.M) {
	if marker := os.Getenv("HERDR_SOHO_PLUGIN_CWD_MARKER"); marker != "" {
		if cwd, err := os.Getwd(); err == nil {
			_ = os.WriteFile(marker, []byte(cwd), 0o600)
		}
	}
	fakecli.RunTests(m)
}

func envWithFake(t *testing.T, dir string, values map[string]string) platform.Env {
	t.Helper()
	entries := fakecli.Env(testutil.CleanEnv(t), dir, fakecli.EnvOptions{IncludeBasePath: true})
	env := platform.Env{}
	for _, entry := range entries {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			env[key] = value
		}
	}
	for key, value := range values {
		env[key] = value
	}
	return env
}

func installBridgeFakes(t *testing.T, paneJSON string, paneCode int, cliCode int, stdout, stderr string) (platform.Env, string, string, string) {
	t.Helper()
	dir := t.TempDir()
	cwd := t.TempDir()
	if paneJSON == "__default__" {
		data, _ := json.Marshal(map[string]any{"result": map[string]any{"pane": map[string]any{"workspace_id": "ws-a", "cwd": cwd}}})
		paneJSON = string(data)
	}
	herdr, err := fakecli.Install(t, dir, "herdr", []fakecli.Rule{
		{Argv: []string{"pane", "get", "pane-a"}, Stdout: paneJSON, Code: paneCode},
		{Argv: plugin.PickerArguments(), Stdout: "opened\n"},
		{Argv: []string{"plugin", "pane", "open"}, ArgvPrefix: true, Stdout: "opened\n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	cliBin, err := fakecli.InstallWithOptions(t, dir, "cli", []fakecli.Rule{{Argv: []string{"doctor"}, Stdout: stdout, Stderr: stderr, Code: cliCode}}, fakecli.InstallOptions{CaptureEnv: []string{"HERDR_WORKSPACE_ID", "HERDR_TAB_ID", "HERDR_PANE_ID", "SOHO_NOWRITE", "PATH"}})
	if err != nil {
		t.Fatal(err)
	}
	env := envWithFake(t, dir, map[string]string{
		"HERDR_BIN_PATH":            herdr,
		"HERDR_PLUGIN_CONTEXT_JSON": `{"workspace_id":"ws-a","tab_id":"tab-a","focused_pane_id":"pane-a"}`,
		"HERDR_WORKSPACE_ID":        "shell-ws",
		"HERDR_TAB_ID":              "shell-tab",
		"HERDR_PANE_ID":             "shell-pane",
	})
	return env, cliBin, cwd, paneJSON
}

func TestBridgeReadOnlyAndFocusedTarget(t *testing.T) {
	// The read-only actions now open the team panel: the bridge validates the
	// focused target (context + `herdr pane get`) and then runs `plugin pane
	// open` - it no longer runs the CLI with its output going to the log.
	env, cliBin, _, _ := installBridgeFakes(t, "__default__", 0, 0, "unused\n", "")
	for _, action := range []string{"team", "roster", "doctor"} {
		r, err := plugin.Bridge(action, env, platform.Current(), cliBin)
		if err != nil || r.Code != 0 || !strings.Contains(r.Out, "target workspace=ws-a pane=pane-a") || !strings.Contains(r.Out, "opened") {
			t.Fatalf("bridge %s: result=%+v err=%v", action, r, err)
		}
	}
	// The old path is gone: the CLI fake was never invoked for any of them.
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(cliBin), "cli.calls.jsonl")); !os.IsNotExist(statErr) {
		t.Fatalf("CLI invoked for a read-only action: %v", statErr)
	}
	herdrCalls, err := fakecli.ReadCallsForConfig(filepath.Join(filepath.Dir(cliBin), "herdr.json"))
	if err != nil || len(herdrCalls) != 6 {
		t.Fatalf("Herdr calls=%#v err=%v", herdrCalls, err)
	}
	for i, action := range []string{"team", "roster", "doctor"} {
		if !reflect.DeepEqual(herdrCalls[i*2].Argv, []string{"pane", "get", "pane-a"}) {
			t.Fatalf("%s pane get argv=%#v", action, herdrCalls[i*2].Argv)
		}
		want := plugin.TeamArguments()
		if action == "doctor" {
			want = plugin.TeamDoctorArguments()
		}
		if !reflect.DeepEqual(herdrCalls[i*2+1].Argv, want) {
			t.Fatalf("%s pane open argv=%#v want %#v", action, herdrCalls[i*2+1].Argv, want)
		}
	}
}

func TestBridgePaneOpenUsesExactHerdrBinary(t *testing.T) {
	// Mutation captured: resolving the herdr binary through PATH instead of
	// HERDR_BIN_PATH selects the decoy instead of the exact fake.
	env, _, _, _ := installBridgeFakes(t, "__default__", 0, 0, "", "")
	pathDir := t.TempDir()
	if _, err := fakecli.Install(t, pathDir, "herdr", []fakecli.Rule{{AnyArgs: true, Stdout: "decoy\n"}}); err != nil {
		t.Fatal(err)
	}
	env["PATH"] = pathDir
	result, err := plugin.Bridge("roster", env, platform.Current(), "")
	if err != nil || result.Code != 0 || strings.Contains(result.Out, "decoy\n") {
		t.Fatalf("bridge pane open result=%+v err=%v", result, err)
	}
	herdrCalls, err := fakecli.ReadCallsForConfig(filepath.Join(filepath.Dir(env.Get("HERDR_BIN_PATH")), "herdr.json"))
	if err != nil || len(herdrCalls) != 2 || !reflect.DeepEqual(herdrCalls[1].Argv, plugin.TeamArguments()) {
		t.Fatalf("HERDR_BIN_PATH call log=%#v err=%v", herdrCalls, err)
	}
	if !filepath.IsAbs(env.Get("HERDR_BIN_PATH")) || strings.HasPrefix(env.Get("HERDR_BIN_PATH"), pathDir) {
		t.Fatalf("fixture is not separate from PATH: Herdr=%q PATH=%q", env.Get("HERDR_BIN_PATH"), pathDir)
	}
}

func TestBridgeRejectsMalformedContextBeforeHerdr(t *testing.T) {
	// JS: "context malformed: fails without invoking the CLI"
	// Mutation captured: accepting invalid context would produce Herdr or CLI calls.
	env, cliBin, _, _ := installBridgeFakes(t, "__default__", 0, 0, "", "")
	env["HERDR_PLUGIN_CONTEXT_JSON"] = `{"workspace_id":"ws-a",`
	_, err := plugin.Bridge("doctor", env, platform.Current(), cliBin)
	bridgeErr, ok := err.(*plugin.BridgeError)
	if !ok || bridgeErr.Code != 2 || !strings.Contains(bridgeErr.Message, "malformed JSON") {
		t.Fatalf("error=%v", err)
	}
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(cliBin), "herdr.calls.jsonl")); !os.IsNotExist(statErr) {
		t.Fatalf("Herdr invoked for invalid context: %v", statErr)
	}
}

func TestBridgeRejectsWorkspaceDivergence(t *testing.T) {
	// JS: "workspace divergence: the pane belongs to another workspace"
	// Mutation captured: dropping the workspace comparison invokes the CLI on the wrong project.
	cwd := t.TempDir()
	data, _ := json.Marshal(map[string]any{"result": map[string]any{"pane": map[string]any{"workspace_id": "ws-b", "cwd": cwd}}})
	env, cliBin, _, _ := installBridgeFakes(t, string(data), 0, 0, "", "")
	_, err := plugin.Bridge("doctor", env, platform.Current(), cliBin)
	bridgeErr, ok := err.(*plugin.BridgeError)
	if !ok || bridgeErr.Code != 2 || !strings.Contains(bridgeErr.Message, "workspace divergence") {
		t.Fatalf("error=%v", err)
	}
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(cliBin), "cli.calls.jsonl")); !os.IsNotExist(statErr) {
		t.Fatalf("CLI invoked on divergence: %v", statErr)
	}
}

func TestBridgeHerdrFailureAndUnknownAction(t *testing.T) {
	// JS: "pane get non-zero exit: fails without invoking the CLI"
	// Mutation captured: treating a failed validation as success would pass the guard and invoke the CLI.
	env, cliBin, _, _ := installBridgeFakes(t, "", 9, 0, "", "pane not found")
	_, err := plugin.Bridge("doctor", env, platform.Current(), cliBin)
	bridgeErr, ok := err.(*plugin.BridgeError)
	if !ok || bridgeErr.Code != 4 || !strings.Contains(bridgeErr.Message, "pane get failed") {
		t.Fatalf("error=%v", err)
	}
	_, err = plugin.Bridge("dispatch", env, platform.Current(), cliBin)
	bridgeErr, ok = err.(*plugin.BridgeError)
	if !ok || bridgeErr.Code != 2 || !strings.Contains(bridgeErr.Message, "unknown subcommand") {
		t.Fatalf("unknown action error=%v", err)
	}
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(cliBin), "cli.calls.jsonl")); !os.IsNotExist(statErr) {
		t.Fatalf("CLI invoked on pane validation failure: %v", statErr)
	}
}

func TestBridgePickUsesExpectedArguments(t *testing.T) {
	// JS: "pick: opens the picker pane with the exact arguments and does not run the CLI"
	// Mutation captured: changing a picker argument breaks the recorded Herdr argv contract.
	env, cliBin, _, _ := installBridgeFakes(t, "__default__", 0, 0, "", "")
	var out, errOut bytes.Buffer
	oldOut, oldErr := platform.Stdout, platform.Stderr
	platform.Stdout, platform.Stderr = &out, &errOut
	t.Cleanup(func() { platform.Stdout, platform.Stderr = oldOut, oldErr })
	code := plugin.PluginCommand([]string{"bridge", "pick"}, env, platform.Current(), cliBin, "")
	if code != 0 || !strings.Contains(out.String(), "target workspace=ws-a pane=pane-a") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(filepath.Dir(cliBin), "herdr.json"))
	if err != nil || len(calls) != 2 || !reflect.DeepEqual(calls[1].Argv, plugin.PickerArguments()) {
		t.Fatalf("Herdr calls=%#v err=%v", calls, err)
	}
}

func TestClipboardLinuxOrderFallbackAndOSC52(t *testing.T) {
	// JS: "clipboard: linux tries wl-copy, then xclip, then xsel — a failing tool yields the next"
	// Mutation captured: changing candidate order or not advancing after failure changes the selected path.
	want := [][]string{{"wl-copy"}, {"xclip", "-selection", "clipboard"}, {"xsel", "--clipboard", "--input"}}
	if got := plugin.ClipboardCandidates("linux"); !reflect.DeepEqual(got, want) {
		t.Fatalf("linux order=%#v", got)
	}
	dir := t.TempDir()
	_, _ = fakecli.Install(t, dir, "wl-copy", []fakecli.Rule{{AnyArgs: true, Code: 1}})
	_, err := fakecli.InstallWithOptions(t, dir, "xclip", []fakecli.Rule{{Argv: []string{"-selection", "clipboard"}}}, fakecli.InstallOptions{CaptureStdin: true})
	if err != nil {
		t.Fatal(err)
	}
	env := envWithFake(t, dir, map[string]string{})
	env["PATH"] = dir
	oldOut := platform.Stdout
	var out bytes.Buffer
	platform.Stdout = &out
	t.Cleanup(func() { platform.Stdout = oldOut })
	if runtime.GOOS == "windows" {
		noTool := platform.Env{"PATH": filepath.Join(dir, "missing")}
		fallback := plugin.CopyText("olá referência", noTool, "linux")
		if fallback.Path != "osc52" || out.String() != "\x1b]52;c;b2zDoSByZWZlcsOqbmNpYQ==\x07" {
			t.Fatalf("fallback result=%+v output=%q", fallback, out.String())
		}
		t.Skip("Linux clipboard subprocess fixtures require a Linux host")
	}
	r := plugin.CopyText("linux text", env, "linux")
	if r.Path != "xclip" {
		t.Fatalf("copy result=%+v", r)
	}
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(dir, "xclip.json"))
	if err != nil || len(calls) != 1 || calls[0].Stdin != "linux text" {
		t.Fatalf("xclip calls=%#v err=%v", calls, err)
	}
	noTool := platform.Env{"PATH": filepath.Join(dir, "missing")}
	r = plugin.CopyText("olá referência", noTool, "linux")
	if r.Path != "osc52" || out.String() != "\x1b]52;c;b2zDoSByZWZlcsOqbmNpYQ==\x07" {
		t.Fatalf("fallback result=%+v output=%q", r, out.String())
	}
	if got := plugin.OSC52Sequence("olá referência"); got != "\x1b]52;c;b2zDoSByZWZlcsOqbmNpYQ==\x07" {
		t.Fatalf("OSC52=%q", got)
	}
	if plugin.ClipboardCandidates("darwin")[0][0] != "pbcopy" || plugin.ClipboardCandidates("win32")[0][0] != "powershell" {
		t.Fatalf("platform candidates darwin=%v windows=%v", plugin.ClipboardCandidates("darwin"), plugin.ClipboardCandidates("win32"))
	}
}

func TestClipboardWindowsUsesPowerShellAndExactArguments(t *testing.T) {
	// JS: "clipboard: windows uses a powershell that reads the stdin — exact argv, hostile text"
	// Mutation captured: using clip.exe or changing the UTF-8 ReadToEnd command changes the fake process argv.
	dir := t.TempDir()
	powershell, err := fakecli.InstallWithOptions(t, dir, "powershell", []fakecli.Rule{{AnyArgs: true}}, fakecli.InstallOptions{CaptureStdin: true})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Rename(powershell, filepath.Join(dir, "powershell.exe")); err != nil {
			t.Fatal(err)
		}
	}
	env := envWithFake(t, dir, map[string]string{})
	r := plugin.CopyText("price is $5 and \"quoted\" and 'single' and não\n", env, "win32")
	if r.Path != "powershell" {
		t.Fatalf("clipboard path=%q", r.Path)
	}
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(dir, "powershell.json"))
	wantArgs := []string{"-NoProfile", "-Command", "[Console]::InputEncoding=[Text.Encoding]::UTF8; Set-Clipboard -Value ([Console]::In.ReadToEnd())"}
	if err != nil || len(calls) != 1 || !reflect.DeepEqual(calls[0].Argv, wantArgs) || calls[0].Stdin != "price is $5 and \"quoted\" and 'single' and não\n" {
		t.Fatalf("PowerShell calls=%#v err=%v", calls, err)
	}
}

func TestClipboardDarwinUsesInputAndFallbackBytes(t *testing.T) {
	// JS: "clipboard: darwin uses pbcopy from a controlled PATH, text on stdin"
	// Mutation captured: dropping stdin or changing the OSC 52 terminator changes captured data/bytes.
	dir := t.TempDir()
	_, err := fakecli.InstallWithOptions(t, dir, "pbcopy", []fakecli.Rule{{AnyArgs: true}}, fakecli.InstallOptions{CaptureStdin: true})
	if err != nil {
		t.Fatal(err)
	}
	env := envWithFake(t, dir, map[string]string{})
	env["PATH"] = dir
	oldOut := platform.Stdout
	var out bytes.Buffer
	platform.Stdout = &out
	t.Cleanup(func() { platform.Stdout = oldOut })
	seq := plugin.OSC52Sequence("olá")
	if seq != "\x1b]52;c;b2zDoQ==\x07" || len([]byte(seq)) != 16 {
		t.Fatalf("OSC52 sequence=%q bytes=%d", seq, len([]byte(seq)))
	}
	if runtime.GOOS == "windows" {
		t.Skip("Darwin clipboard subprocess fixture requires a Darwin host")
	}
	r := plugin.CopyText("hello clip", env, "darwin")
	if r.Path != "pbcopy" {
		t.Fatalf("clipboard path=%q", r.Path)
	}
	calls, err := fakecli.ReadCallsForConfig(filepath.Join(dir, "pbcopy.json"))
	if err != nil || len(calls) != 1 || calls[0].Stdin != "hello clip" {
		t.Fatalf("clipboard calls=%#v err=%v", calls, err)
	}
}
