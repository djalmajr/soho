package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
	waitpkg "github.com/djalmajr/soho/internal/wait"
)

func TestDispatchCwdGoneBeforeWrites(t *testing.T) {
	for _, name := range []string{"removed", "file", "removed-compact", "present", "empty", "permission"} {
		t.Run(name, func(t *testing.T) {
			f := newBusyDispatchFixture(t)
			f.env["SOHO_PROMPT_CHECK_SECONDS"] = "0"
			f.env["SOHO_CALLER_CHECK"] = "off"
			f.install(t, []fakecli.Rule{{Argv: []string{"agent", "list"}, ArgvPrefix: true, Stdout: `{"result":{"agents":[]}}`}, {Argv: []string{"agent", "prompt"}, ArgvPrefix: true, Stdout: `{"result":{"sent":true}}`}, {AnyArgs: true, Stdout: `{"result":{}}`}})
			cwd := filepath.Join(f.root, "worker tree ; literal")
			if err := os.Mkdir(cwd, 0700); err != nil {
				t.Fatal(err)
			}
			gone := name == "removed" || name == "file" || name == "removed-compact"
			if gone {
				if err := os.Remove(cwd); err != nil {
					t.Fatal(err)
				}
			}
			if name == "file" {
				if err := os.WriteFile(cwd, []byte("not a directory\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if name == "present" || name == "permission" {
				cwd = f.root
			}
			if name == "empty" {
				cwd = ""
			}
			roster := filepath.Join(f.stateDir, "agents.tsv")
			raw, err := os.ReadFile(roster)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
			fields := strings.Split(lines[1], "\t")
			fields[6] = cwd
			lines[1] = strings.Join(fields, "\t")
			if err = os.WriteFile(roster, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if name == "permission" {
				old := waitpkg.WorkerCwdStat
				waitpkg.WorkerCwdStat = func(path string) (os.FileInfo, error) {
					if path == cwd {
						return nil, &os.PathError{Op: "stat", Path: path, Err: os.ErrPermission}
					}
					return os.Stat(path)
				}
				t.Cleanup(func() { waitpkg.WorkerCwdStat = old })
			}
			before := snapshotTree(t, f.root)
			args := []string{"worker", f.brief, "--no-wait"}
			if name == "removed-compact" {
				args = append(args, "--compact")
			}
			code, out, stderr := f.run(t, args...)
			calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "herdr.calls.jsonl"))
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			inputs, gets := 0, 0
			for _, call := range calls {
				if len(call.Argv) >= 2 && call.Argv[0] == "agent" {
					switch call.Argv[1] {
					case "prompt", "send-keys":
						inputs++
					case "get":
						gets++
					}
				}
			}
			if gone {
				want := fmt.Sprintf("soho: dispatch: 'worker' runs in %s, which no longer exists; nothing was sent. Release it with --close and spawn it again in an existing directory\n", cwd)
				if code != 4 || stderr != want || inputs != 0 {
					t.Fatalf("exit=%d inputs=%d JSON=%s stderr=%q want=%q", code, inputs, out, stderr, want)
				}
				after := snapshotTree(t, f.root)
				for path := range after {
					if strings.HasSuffix(path, "friction.log") {
						delete(after, path)
					}
				}
				for path := range before {
					if strings.HasSuffix(path, "friction.log") {
						delete(before, path)
					}
				}
				if !reflect.DeepEqual(before, after) {
					t.Fatalf("wrote task artifacts before cwd refusal: before=%v after=%v", before, after)
				}
			} else if code != 0 || inputs != 1 || strings.Contains(stderr, "which no longer exists") {
				t.Fatalf("control exit=%d inputs=%d JSON=%s stderr=%s", code, inputs, out, stderr)
			}
			if gets != 0 {
				t.Fatalf("cwd check added an agent get with prompt checking off: %v", calls)
			}
			t.Logf("case=%s exit=%d inputs=%d agent_get=%d stderr=%s", name, code, inputs, gets, stderr)
		})
	}
}
