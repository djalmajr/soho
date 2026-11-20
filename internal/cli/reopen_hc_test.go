package cli

// Native reopen under hc: the CLI's own fake-hc runner (fakecli on PATH with
// SOHO_FAKECLI_CONFIG) serves the read/close/send calls and a Go RPC bridge
// answers terminal.run. The binary is re-executed as `hc connect --json`, so
// this init runs before fakecli's dispatcher and speaks the real handshake.
import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/taskreport"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func init() {
	frame := os.Getenv("SOHO_REOPEN_RPC_FRAME")
	request := os.Getenv("SOHO_REOPEN_RPC_REQUEST")
	if frame == "" || request == "" || len(os.Args) != 3 || os.Args[1] != "connect" || os.Args[2] != "--json" {
		return
	}
	decoder := json.NewDecoder(os.Stdin)
	var hello struct {
		Method string `json:"method"`
	}
	if decoder.Decode(&hello) != nil || hello.Method != "hello" {
		os.Exit(90)
	}
	fmt.Fprintln(os.Stdout, `{"jsonrpc":"2.0","id":1,"result":{"generation":1}}`)
	var run map[string]any
	if decoder.Decode(&run) != nil || run["method"] != "terminal.run" {
		os.Exit(91)
	}
	data, err := json.Marshal(run)
	if err != nil || os.WriteFile(request, data, 0o600) != nil {
		os.Exit(92)
	}
	fmt.Fprintln(os.Stdout, frame)
	os.Exit(0)
}

type reopenHcFixture struct {
	root, cwd, state, sd, bin, rpcRequest string
	env                                   platform.Env
}

func newReopenHcFixture(t *testing.T) *reopenHcFixture {
	t.Helper()
	root := t.TempDir()
	cwd, state, bin, roles := filepath.Join(root, "repo"), filepath.Join(root, "state"), filepath.Join(root, "bin"), filepath.Join(root, "roles")
	sd, home, conf := filepath.Join(state, "sX"), filepath.Join(root, "home"), filepath.Join(root, "conf")
	for _, dir := range []string{cwd, state, sd, bin, roles, home, conf, filepath.Join(sd, "wait"), filepath.Join(sd, "briefs"), filepath.Join(sd, "reports")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(root)
	if err := os.WriteFile(filepath.Join(roles, "implementer.md"), []byte("---\n---\nRole body.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	row := strings.Join([]string{"worker", "t7", "codex", "implementer", "openai", "1", cwd, "now", "gpt-5", "ask", "implementer", "", "", "--no-daemon", "high"}, "\t")
	if err := os.WriteFile(filepath.Join(sd, "agents.tsv"), []byte("# header\n"+row+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := "worker-20261119T210000"
	composed := filepath.Join(sd, "briefs", base+".md")
	if err := os.WriteFile(composed, []byte("# Role: implementer\n\n# Brief\n\n# Goal\n\nRedispatch this brief.\n\n# Report contract\n\nwrite it\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(sd, "reports", base+".md")
	if err := os.WriteFile(report, []byte("# done\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(core.LastReportPath(sd, "worker"), []byte(report+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pointer := fmt.Sprintf(`{"version":1,"task_report":%q,"current":%q,"history":[]}`, filepath.Join(sd, "reports", base+".current.md"), report)
	if err := os.WriteFile(taskreport.TaskReportPointerPath(sd, "worker"), []byte(pointer+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rules := []fakecli.Rule{
		{Argv: []string{"get", "t1", "--json"}, Stdout: `{"address":"t1","state":"live","labels":{"soho.session":"sX"}}`},
		{Argv: []string{"get", "worker", "--json"}, Stdout: `{"address":"t7","name":"worker","state":"live","turn":5,"reported":true,"status":{"state":"idle"}}`},
		{Argv: []string{"get", "t7", "--json"}, Stdout: `{"address":"t7","name":"worker","state":"live","turn":6,"reported":true,"status":{"state":"idle"}}`},
		{Argv: []string{"kill", "t7", "--json"}, Stdout: `{"address":"t7","state":"finished"}`},
		{Argv: []string{"rm", "t7", "--json"}, Stdout: `{"removed":"t7"}`},
		{Argv: []string{"ls", "--label", "soho.session=sX", "--json"}, Stdout: `{"terminals":[]}`},
		{Argv: []string{"read", "t7", "--lines", "0", "--json"}, Stdout: `{"text":"ready\n"}`},
	}
	rules = append(rules, guardProbeRules()...)
	rules = append(rules, fakecli.Rule{Argv: []string{"send"}, ArgvPrefix: true, Stdout: `{"result":{"submitted":true}}`})
	if _, err := fakecli.Install(t, bin, "hc", rules); err != nil {
		t.Fatal(err)
	}
	if _, err := fakecli.Install(t, bin, "herdr", []fakecli.Rule{{AnyArgs: true, Code: 99, Stderr: "legacy transport must not be called"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := fakecli.Install(t, bin, "claude", nil); err != nil {
		t.Fatal(err)
	}
	rpcRequest := filepath.Join(root, "rpc-request.json")
	env := platform.Env{
		"PATH": bin, "HOME": home, "USERPROFILE": home, "XDG_CONFIG_HOME": conf,
		"TMPDIR": root, "SOHO_FAKECLI_CONFIG": bin, "SOHO_DIR": state,
		"SOHO_TRANSPORT": "hc", "HC_TERMINAL_ID": "t1", "SOHO_LANES": "off",
		"SOHO_ROLES": roles, "SOHO_PROMPT_CHECK_SECONDS": "0", "SOHO_WAIT_POLL_MS": "50",
		"SOHO_REOPEN_RPC_FRAME":   `{"jsonrpc":"2.0","id":2,"result":{"address":"t7"}}`,
		"SOHO_REOPEN_RPC_REQUEST": rpcRequest,
	}
	return &reopenHcFixture{root: root, cwd: cwd, state: state, sd: sd, bin: bin, rpcRequest: rpcRequest, env: env}
}

func (f *reopenHcFixture) run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, stderr bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &stderr
	code := Run(args, f.env)
	platform.Stdout, platform.Stderr = oldOut, oldErr
	return code, out.String(), stderr.String()
}

func TestReopenRunsReleaseSpawnAndRedispatchUnderHc(t *testing.T) {
	f := newReopenHcFixture(t)
	code, out, errText := f.run(t, "reopen", "worker", "--kind", "claude", "--redispatch")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, errText)
	}
	if strings.Contains(errText, hcRefusalLine) {
		t.Fatalf("the hc entry guard refused reopen: %q", errText)
	}
	for _, want := range []string{`"name": "worker"`, `"kind": "claude"`, `"created_pane": true`} {
		if !strings.Contains(out, want) {
			t.Fatalf("spawn output=%q, missing %s", out, want)
		}
	}
	dispatchAt := strings.Index(out, `"agent":"worker"`)
	if dispatchAt <= strings.Index(out, `"created_pane": true`) {
		t.Fatalf("the dispatch JSON did not follow the spawn JSON: %q", out)
	}
	want := "soho: reopen: 'worker' was a codex worker; its native args are not passed to claude\n"
	if strings.Count(errText, want) != 1 {
		t.Fatalf("stderr=%q, want exactly one %q", errText, want)
	}
	calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "hc.calls.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	verbs := []string{}
	for _, call := range calls {
		verbs = append(verbs, strings.Join(call.Argv, " "))
	}
	for _, want := range []string{"get worker --json", "kill t7 --json", "rm t7 --json", "ls --label soho.session=sX --json"} {
		if !strings.Contains(strings.Join(verbs, "\n"), want) {
			t.Fatalf("hc calls=%q, missing %s", verbs, want)
		}
	}
	sent := false
	for _, call := range calls {
		if len(call.Argv) >= 2 && call.Argv[0] == "send" && call.Argv[1] != "--help" {
			sent = true
		}
	}
	if !sent {
		t.Fatalf("no prompt was sent under hc: %q", verbs)
	}
	if _, err := os.Stat(filepath.Join(f.bin, "herdr.calls.jsonl")); err == nil {
		t.Fatal("a legacy herdr call was made under hc")
	}
	request, err := os.ReadFile(f.rpcRequest)
	if err != nil {
		t.Fatalf("no terminal.run request: %v", err)
	}
	run := string(request)
	if !strings.Contains(run, "terminal.run") || strings.Contains(run, "--no-daemon") {
		t.Fatalf("terminal.run request=%s", run)
	}
	roster, err := os.ReadFile(filepath.Join(f.sd, "agents.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(roster), "worker\tt7\tclaude\timplementer") {
		t.Fatalf("roster=%q", roster)
	}
}

func TestReopenHcFailedSpawnPrintsRecoveryCommands(t *testing.T) {
	for _, tc := range []struct {
		name       string
		redispatch bool
	}{
		{"without redispatch", false},
		{"with redispatch", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newReopenHcFixture(t)
			f.env["SOHO_REOPEN_RPC_FRAME"] = `{"jsonrpc":"2.0","id":2,"error":{"code":-32000,"message":"start_failed","data":{"kind":"internal"}}}`
			args := []string{"reopen", "worker", "--kind", "claude"}
			if tc.redispatch {
				args = append(args, "--redispatch")
			}
			code, out, errText := f.run(t, args...)
			if code == 0 {
				t.Fatalf("a failed spawn returned 0: stdout=%q stderr=%q", out, errText)
			}
			want := "soho: reopen: to recover, run: soho spawn implementer --name worker --fresh --kind claude --cwd " + reopenShellQuote(f.cwd) + " --effort high\n"
			if !strings.Contains(errText, want) {
				t.Fatalf("stderr=%q, want the literal recovery line %q", errText, want)
			}
			if !tc.redispatch {
				if strings.Contains(errText, "then run: soho dispatch") {
					t.Fatalf("stderr=%q, want no dispatch recovery line", errText)
				}
			} else {
				dispatchPath := filepath.Join(f.sd, "briefs", "worker-20261119T210000.md")
				dispatchWant := "soho: reopen: then run: soho dispatch worker " + reopenShellQuote(dispatchPath) + "\n"
				if strings.Count(errText, dispatchWant) != 1 {
					t.Fatalf("stderr=%q, want exactly one %q", errText, dispatchWant)
				}
				if body, err := os.ReadFile(dispatchPath); err != nil || !strings.Contains(string(body), "Redispatch this brief.") {
					t.Fatalf("the printed brief path is not usable: %q err=%v", body, err)
				}
			}
			calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "hc.calls.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			killed := false
			for _, call := range calls {
				if len(call.Argv) >= 2 && call.Argv[0] == "kill" {
					killed = true
				}
			}
			if !killed {
				t.Fatalf("the recovery lines printed without an hc close: %q", calls)
			}
			if _, err := os.Stat(filepath.Join(f.bin, "herdr.calls.jsonl")); err == nil {
				t.Fatal("a legacy herdr call was made under hc")
			}
		})
	}
}

func TestReopenStaysRefusedUnderNowriteOnHc(t *testing.T) {
	f := newReopenHcFixture(t)
	f.env["SOHO_NOWRITE"] = "1"
	before, err := os.ReadFile(filepath.Join(f.sd, "agents.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	code, out, errText := f.run(t, "reopen", "worker", "--kind", "claude", "--redispatch")
	if code != 2 || out != "" || !strings.Contains(errText, "SOHO_NOWRITE=1 is read-only") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, errText)
	}
	if strings.Contains(errText, hcRefusalLine) {
		t.Fatalf("the refusal came from the hc guard, not NOWRITE: %q", errText)
	}
	after, err := os.ReadFile(filepath.Join(f.sd, "agents.tsv"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("NOWRITE changed the roster: before=%q after=%q err=%v", before, after, err)
	}
	if calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "hc.calls.jsonl")); err == nil {
		for _, call := range calls {
			if len(call.Argv) > 0 && call.Argv[0] != "get" {
				t.Fatalf("NOWRITE reached a mutating hc call: %q", calls)
			}
		}
	}
	if _, err := os.Stat(f.rpcRequest); !os.IsNotExist(err) {
		t.Fatalf("NOWRITE started a worker: %v", err)
	}
}
