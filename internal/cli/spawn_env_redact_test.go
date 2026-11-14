package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// This native Go fixture speaks the real duplex RPC handshake. It runs only
// in the re-executed test image installed as hc, before fakecli's dispatcher.
func init() {
	p := os.Getenv("SOHO_REDACT_RPC_RESPONSE")
	if p == "" || len(os.Args) != 3 || os.Args[1] != "connect" || os.Args[2] != "--json" {
		return
	}
	decoder := json.NewDecoder(os.Stdin)
	var request struct {
		Method string `json:"method"`
		Params struct {
			Env map[string]string `json:"env"`
		} `json:"params"`
	}
	if decoder.Decode(&request) != nil || request.Method != "hello" {
		os.Exit(90)
	}
	fmt.Fprintln(os.Stdout, `{"jsonrpc":"2.0","id":1,"result":{"generation":1}}`)
	if decoder.Decode(&request) != nil || request.Method != "terminal.run" || request.Params.Env["SELECTED_CANARY"] != "s3cr3t-VALUE-0123456789" {
		os.Exit(91)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		os.Exit(92)
	}
	fmt.Fprintln(os.Stdout, string(b))
	os.Exit(0)
}

func TestSpawnEnvPublicCLIOutput(t *testing.T) {
	const value = "s3cr3t-VALUE-0123456789"
	for _, mode := range []string{"message", "hint", "blocked-diagnostic", "blocked-screen", "ready-screen"} {
		t.Run(mode, func(t *testing.T) {
			f := newHcGuardFixture(t)
			bin := f.env["PATH"]
			roles := filepath.Join(f.root, "roles")
			if err := os.MkdirAll(roles, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(roles, "worker.md"), []byte("---\nkind: pi\n---\n"), 0600); err != nil {
				t.Fatal(err)
			}
			f.env["SOHO_ROLES"], f.env["SOHO_LANES"], f.env["HC_TERMINAL_ID"] = roles, "off", "t1"
			f.env["XDG_CONFIG_HOME"], f.env["SELECTED_CANARY"] = f.root, value
			frame := `{"jsonrpc":"2.0","id":2,"result":{"address":"t7"}}`
			wantCode := 7
			status := `{"address":"t7","state":"live","reported":true,"status":{"state":"blocked","msg":"` + value + `"}}`
			read := fakecli.Rule{Argv: []string{"read", "t7", "--lines", "0", "--json"}, Stdout: `{"text":"screen ` + value + `\n"}`}
			if mode == "message" || mode == "hint" {
				message, hint := value, "ordinary"
				if mode == "hint" {
					message, hint = "rejected", value
				}
				frame = `{"jsonrpc":"2.0","id":2,"error":{"code":-32000,"message":"` + message + `","data":{"kind":"internal","hint":"` + hint + `"}}}`
				wantCode = 4
			}
			if mode == "blocked-diagnostic" {
				read.Stdout, read.Code = "", 1
			}
			if mode == "ready-screen" {
				status, wantCode = `{"address":"t7","state":"live","reported":true,"status":{"state":"idle"}}`, 0
			}
			response := filepath.Join(f.root, "rpc-response.json")
			if err := os.WriteFile(response, []byte(frame), 0600); err != nil {
				t.Fatal(err)
			}
			f.env["SOHO_REDACT_RPC_RESPONSE"] = response
			rules := []fakecli.Rule{
				{Argv: []string{"get", "t1", "--json"}, Stdout: `{"address":"t1","state":"live","labels":{"soho.session":"sX"}}`},
				{Argv: []string{"ls", "--label", "soho.session=sX", "--json"}, Stdout: `{"terminals":[]}`},
				{Argv: []string{"get", "t7", "--json"}, Stdout: status}, read,
			}
			for _, exe := range []string{"hc", "pi", "herdr"} {
				if _, err := fakecli.Install(t, bin, exe, rules); err != nil {
					t.Fatal(err)
				}
			}
			f.env["SOHO_FAKECLI_CONFIG"] = bin
			code, out, stderr := f.run(t, "spawn", "worker", "--fresh", "--env-from", "SELECTED_CANARY")
			if code != wantCode {
				t.Fatalf("exit=%d want=%d stdout=%q stderr=%q", code, wantCode, out, stderr)
			}
			public := out + stderr
			err := filepath.WalkDir(f.env["SOHO_DIR"], func(p string, d os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if !d.IsDir() {
					b, err := os.ReadFile(p)
					if err != nil {
						return err
					}
					public += string(b)
				}
				return nil
			})
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if strings.Contains(public, value) {
				t.Error("selected value leaked through public CLI/state")
			}
			if mode != "ready-screen" && !strings.Contains(public, "[redacted:SELECTED_CANARY]") {
				t.Error("missing replacement token")
			}
			calls, err := fakecli.ReadCalls(filepath.Join(bin, "hc.calls.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			for _, call := range calls {
				if call.Argv[0] == "kill" || call.Argv[0] == "rm" {
					t.Fatal("unexpected cleanup")
				}
			}
			if wantCode == 7 && !strings.Contains(out, `"status": "blocked_at_startup"`) {
				t.Error("lost blocked class")
			}
			if wantCode == 0 && !strings.Contains(out, `"status": "ready"`) {
				t.Error("lost ready class")
			}
		})
	}
}
