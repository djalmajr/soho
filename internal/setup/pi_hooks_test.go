package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/djalmajr/soho/internal/setuptext"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func piFile(root string) string { return filepath.Join(root, ".pi", "extensions", "soho-inbound.ts") }

func TestSetupPiInstallAndRefresh(t *testing.T) {
	for _, old := range []string{"", setuptext.PiInboundMarker + "\nold content\n", setuptext.PiInboundMarker, setuptext.PiInboundMarker + "\r\nold\r\n"} {
		t.Run(old, func(t *testing.T) {
			root, env, ctx := codexSetupFixture(t)
			file := piFile(root)
			if old != "" {
				if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, []byte(old), 0600); err != nil {
					t.Fatal(err)
				}
			}
			for i := 0; i < 2; i++ {
				code, _, warning := runSetupWrite(nil, ctx, env, root)
				got, err := os.ReadFile(file)
				if code != 0 || err != nil || string(got) != setuptext.PiInboundExtension() || !strings.HasPrefix(string(got), setuptext.PiInboundMarker+"\n") {
					t.Fatalf("run=%d code=%d read=%v content=%s warning=%s", i, code, err, got, warning)
				}
			}
		})
	}
}

func TestSetupPiUnmarkedPreserved(t *testing.T) {
	for _, original := range []string{"", "export default function () {}\n", "\n" + setuptext.PiInboundMarker + "\n", setuptext.PiInboundMarker + " edited\n"} {
		t.Run(original, func(t *testing.T) {
			root, env, ctx := codexSetupFixture(t)
			file := piFile(root)
			if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte(original), 0600); err != nil {
				t.Fatal(err)
			}
			code, out, warning := runSetupWrite(nil, ctx, env, root)
			got, err := os.ReadFile(file)
			if code != 0 || err != nil || string(got) != original || !strings.Contains(warning, "file has no soho marker and was left untouched") || !strings.Contains(warning, file) || strings.Contains(out, "extension ready: "+file) {
				t.Fatalf("code=%d read=%v original=%q got=%q out=%s warning=%s", code, err, original, got, out, warning)
			}
		})
	}
}

func TestSetupPiPlanNoHooksAndLocal(t *testing.T) {
	for _, arg := range []string{"--plan", "--dry-run", "--no-hooks"} {
		t.Run(arg, func(t *testing.T) {
			root, env, ctx := codexSetupFixture(t)
			code, out, warning := runSetupWrite([]string{arg}, ctx, env, root)
			if code != 0 || (arg != "--no-hooks" && !strings.Contains(out, "# would write "+root+"/.pi/extensions/soho-inbound.ts: input extension (pi)")) {
				t.Fatalf("code=%d out=%s warning=%s", code, out, warning)
			}
			if _, err := os.Stat(piFile(root)); !os.IsNotExist(err) {
				t.Fatalf("unexpected extension: %v", err)
			}
		})
	}
	t.Run("local", func(t *testing.T) {
		root, env, ctx := codexSetupFixture(t)
		code, _, warning := runSetupWrite([]string{"--local", "--no-hooks"}, ctx, env, root)
		if code != 0 {
			t.Fatalf("baseline code=%d warning=%s", code, warning)
		}
		exclude := filepath.Join(root, ".git", "info", "exclude")
		before, err := os.ReadFile(exclude)
		if err != nil {
			t.Fatal(err)
		}
		code, _, warning = runSetupWrite([]string{"--local"}, ctx, env, root)
		after, err := os.ReadFile(exclude)
		generated, readErr := os.ReadFile(piFile(root))
		if code != 0 || err != nil || readErr != nil || !bytes.Equal(before, after) || string(generated) != setuptext.PiInboundExtension() {
			t.Fatalf("code=%d err=%v read=%v warning=%s", code, err, readErr, warning)
		}
		if _, err := os.Stat(filepath.Join(root, ".gitignore")); !os.IsNotExist(err) {
			t.Fatalf("local changed tracked ignore: %v", err)
		}
	})
}

// The harness wraps the real spawn only to record argv/stdin and retain child
// objects for cleanup, including failed assertions and the watchdog path.
const piRuntimeHarness = `
import cp from "node:child_process";
import { syncBuiltinESMExports } from "node:module";
import { pathToFileURL } from "node:url";
const realSpawn = cp.spawn;
const calls = [], children = [];
cp.spawn = (...args) => {
  const call = { executable: args[0], argv: args[1], options: args[2], stdin: "" };
  calls.push(call);
  const child = realSpawn(...args);
  children.push(child);
  child.closedForTest = false;
  child.on("error", () => {});
  child.on("close", () => { child.closedForTest = true; });
  const end = child.stdin.end.bind(child.stdin);
  child.stdin.end = (data, ...rest) => { call.stdin += data; return end(data, ...rest); };
  return child;
};
syncBuiltinESMExports();
async function cleanup() {
  for (const child of children) {
    if (child.closedForTest) continue;
    const closed = new Promise((resolve) => child.once("close", resolve));
    if (child.exitCode === null && child.signalCode === null) child.kill("SIGKILL");
    await closed;
  }
}
const watchdog = setTimeout(async () => { await cleanup(); process.exit(3); }, 5000);
try {
  const { default: install } = await import(pathToFileURL(process.argv[2]).href);
  let handler;
  install({ on: (event, callback) => { if (event !== "input" || handler) throw Error("unexpected registration"); handler = callback; } });
  if (!handler) throw Error("missing input handler");
  const notifications = [];
  const result = await handler(JSON.parse(process.argv[3]), { ui: { notify: (message, type) => notifications.push({ message, type }) } });
  const closedAtReturn = children.every(child => child.closedForTest);
  await cleanup();
  process.stdout.write(JSON.stringify({ result, notifications, calls, closedAtReturn, closed: children.every(child => child.closedForTest) }));
} finally { clearTimeout(watchdog); await cleanup(); }
`

func TestSetupPiExtensionRuntime(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skipf("node not in PATH: %v", err)
	}
	for _, tc := range []struct{ name, mode, source string }{
		{"block", "block", "interactive"}, {"block-rpc", "block", "rpc"}, {"block-extension", "block", "extension"},
		{"plain", "plain", "interactive"}, {"absent", "absent", "interactive"}, {"timeout", "timeout", "rpc"},
		{"nonzero", "nonzero", "extension"}, {"pass", "pass", "rpc"},
		{"malformed-json", "malformed-json", "interactive"}, {"null-output", "null-output", "rpc"},
		{"array-output", "array-output", "extension"}, {"nested-array-output", "nested-array-output", "interactive"},
		{"number-output", "number-output", "rpc"}, {"string-output", "string-output", "extension"},
		{"boolean-output", "boolean-output", "interactive"}, {"spaces", "spaces", "rpc"},
		{"object", "object", "interactive"}, {"object-allow", "object-allow", "extension"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, env, ctx := codexSetupFixture(t)
			code, _, warning := runSetupWrite(nil, ctx, env, root)
			if code != 0 {
				t.Fatalf("setup=%d warning=%s", code, warning)
			}
			file := piFile(root)
			if tc.mode == "timeout" {
				// Only this test copy lowers the timeout; production stays at ten seconds.
				const testTimeoutMS = 150
				raw, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Count(string(raw), "const timeoutMS = 10000;") != 1 {
					t.Fatal("missing timeout constant")
				}
				raw = []byte(strings.Replace(string(raw), "const timeoutMS = 10000;", fmt.Sprintf("const timeoutMS = %d;", testTimeoutMS), 1))
				if err := os.WriteFile(file, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			bin := filepath.Join(t.TempDir(), "bin with spaces")
			if err := os.MkdirAll(bin, 0700); err != nil {
				t.Fatal(err)
			}
			response := `{"decision":"block","reason":"[soho:peer-refused] #12345678 refused-not-allowed\nThe message did not reach the model; nothing needs clearing."}`
			outputs := map[string]string{
				"malformed-json": "{broken", "null-output": "null", "array-output": "[]",
				"nested-array-output": "[{}]", "number-output": "42", "string-output": `"text"`,
				"boolean-output": "false", "spaces": " \n\t\u00a0", "object": "{}", "object-allow": `{"decision":"allow"}`,
			}
			if output, ok := outputs[tc.mode]; ok {
				response = output
			}
			if tc.mode != "absent" {
				if runtime.GOOS == "windows" {
					rule := fakecli.Rule{Argv: []string{"hook", "inbound", "--pi"}, Stdout: response}
					if tc.mode == "timeout" {
						rule.Delay = 2000
					}
					if tc.mode == "nonzero" {
						rule.Code = 7
					}
					if tc.mode == "pass" {
						rule.Stdout = ""
					}
					if _, err := fakecli.Install(t, bin, "soho", []fakecli.Rule{rule}); err != nil {
						t.Fatal(err)
					}
				} else {
					responseJSON, _ := json.Marshal(response)
					script := "#!" + node + "\nrequire('node:fs').readFileSync(0,'utf8');\nprocess.stderr.write('must not reach pi');\n"
					switch tc.mode {
					case "timeout":
						script += "setTimeout(()=>process.exit(0),2000);\n"
					case "nonzero":
						script += "process.exit(7);\n"
					case "pass":
						script += "process.exit(0);\n"
					default:
						script += "process.stdout.write(" + string(responseJSON) + ");\n"
					}
					if err := os.WriteFile(filepath.Join(bin, "soho"), []byte(script), 0700); err != nil {
						t.Fatal(err)
					}
				}
			}
			harness := filepath.Join(t.TempDir(), "pi harness.mjs")
			if err := os.WriteFile(harness, []byte(piRuntimeHarness), 0600); err != nil {
				t.Fatal(err)
			}
			text := "quotes ' \" and newline\n[soho:peer] #12345678 $(never-run)"
			if tc.mode == "plain" {
				text = "ordinary input"
			}
			event, _ := json.Marshal(map[string]string{"type": "input", "source": tc.source, "text": text})
			commandCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			command := exec.CommandContext(commandCtx, node, harness, file, string(event))
			command.Dir = root
			command.Env = []string{"HOME=" + env["HOME"], "USERPROFILE=" + env["HOME"], "XDG_CONFIG_HOME=" + env["XDG_CONFIG_HOME"], "TMPDIR=" + env["TMPDIR"], "PATH=" + bin, "SOHO_FAKECLI_CONFIG=" + bin}
			if runtime.GOOS == "windows" {
				command.Env = append(command.Env, "SystemRoot="+os.Getenv("SystemRoot"))
			}
			var stderr bytes.Buffer
			command.Stderr = &stderr
			started := time.Now()
			output, err := command.Output()
			elapsed := time.Since(started)
			if err != nil || stderr.Len() != 0 {
				t.Fatalf("node=%s err=%v elapsed=%v stdout=%s stderr=%s", node, err, elapsed, output, stderr.String())
			}
			var got struct {
				Result struct {
					Action string `json:"action"`
				} `json:"result"`
				Notifications []struct{ Message, Type string } `json:"notifications"`
				Calls         []struct {
					Executable string
					Argv       []string
					Stdin      string
					Options    struct{ Shell bool }
				} `json:"calls"`
				Closed         bool `json:"closed"`
				ClosedAtReturn bool `json:"closedAtReturn"`
			}
			if err := json.Unmarshal(output, &got); err != nil || !got.Closed {
				t.Fatalf("result=%s parse=%v", output, err)
			}
			if tc.mode != "absent" && !got.ClosedAtReturn {
				t.Fatalf("handler returned with a running helper before fixture cleanup: %s", output)
			}
			wantAction := "continue"
			if tc.mode == "block" {
				wantAction = "handled"
			}
			if got.Result.Action != wantAction {
				t.Fatalf("action=%s want=%s result=%s", got.Result.Action, wantAction, output)
			}
			if tc.mode == "plain" {
				if len(got.Calls) != 0 || len(got.Notifications) != 0 {
					t.Fatalf("plain input ran check: %s", output)
				}
			} else {
				if len(got.Calls) != 1 || got.Calls[0].Executable != "soho" || !reflect.DeepEqual(got.Calls[0].Argv, []string{"hook", "inbound", "--pi"}) || got.Calls[0].Options.Shell {
					t.Fatalf("spawn=%s", output)
				}
				var payload map[string]string
				if err := json.Unmarshal([]byte(got.Calls[0].Stdin), &payload); err != nil || !reflect.DeepEqual(payload, map[string]string{"hook_event_name": "UserPromptSubmit", "prompt": text, "cwd": root}) {
					t.Fatalf("stdin=%q parse=%v", got.Calls[0].Stdin, err)
				}
			}
			switch tc.mode {
			case "block":
				var block map[string]string
				_ = json.Unmarshal([]byte(response), &block)
				if len(got.Notifications) != 1 || got.Notifications[0].Type != "error" || got.Notifications[0].Message != block["reason"] {
					t.Fatalf("notification=%s", output)
				}
			case "absent", "timeout", "nonzero", "malformed-json", "null-output", "array-output", "nested-array-output", "number-output", "string-output", "boolean-output":
				cause := map[string]string{"absent": "ENOENT", "timeout": "timeout", "nonzero": "exit 7"}[tc.mode]
				if cause == "" {
					cause = "unreadable output"
				}
				want := "soho: peer check did not run (" + cause + "); the message was not checked"
				if len(got.Notifications) != 1 || got.Notifications[0].Type != "warning" || got.Notifications[0].Message != want {
					t.Fatalf("notification=%s", output)
				}
				if tc.mode == "timeout" && elapsed >= 2*time.Second {
					t.Fatalf("deadline did not stop child: %v result=%s", elapsed, output)
				}
			case "plain", "pass", "spaces", "object", "object-allow":
				if len(got.Notifications) != 0 {
					t.Fatalf("unexpected notification=%s", output)
				}
			}
		})
	}
}
