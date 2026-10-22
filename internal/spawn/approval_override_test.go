package spawn

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestSpawnCodexArgsOverrideApprovals(t *testing.T) {
	for _, tc := range []struct {
		name, kind, mode, config, flag string
		tail, want                     []string
	}{
		{"config-auto", "codex", "auto", "-s danger-full-access", "-s", nil, []string{"-s", "danger-full-access"}},
		{"tail-full", "codex", "full", "", "-a", []string{"-a", "never"}, []string{"-a", "never"}},
		{"config-edits", "codex", "edits", "--full-auto", "--full-auto", nil, []string{"--full-auto"}},
		{"config-unsandboxed", "codex", "unsandboxed", "--ask-for-approval=on-request", "--ask-for-approval", nil, []string{"--ask-for-approval=on-request"}},
		{"config-equals", "codex", "auto", "--sandbox=danger-full-access", "--sandbox", nil, []string{"--sandbox=danger-full-access"}},
		{"combined", "codex", "auto", "--verbose", "--approve-for-me", []string{"--approve-for-me"}, []string{"--verbose", "--approve-for-me"}},
		{"no-override", "codex", "auto", "", "", nil, []string{"--approve-for-me"}},
		{"ordinary-args", "codex", "auto", "--verbose", "", nil, []string{"--approve-for-me", "--verbose"}},
		{"claude", "claude", "auto", "-s danger-full-access", "", nil, []string{"--permission-mode", "auto", "-s", "danger-full-access"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSpawnFixture(t, freshSpawnRules())
			configureSpawnFixture(t, &f)
			f.env["HOME"], f.env["USERPROFILE"], f.env["XDG_CONFIG_HOME"], f.env["TMPDIR"] = f.root, f.root, f.root, f.root
			if _, err := fakecli.Install(t, f.bin, tc.kind, nil); err != nil {
				t.Fatal(err)
			}
			front := fmt.Sprintf("---\nkind: %s\napprovals: %s\n---\n", tc.kind, tc.mode)
			if err := os.WriteFile(filepath.Join(f.root, "roles", "worker.md"), []byte(front), 0600); err != nil {
				t.Fatal(err)
			}
			f.env["SOHO_ARGS_"+strings.ToUpper(tc.kind)] = tc.config
			args := []string{"worker", "--name", "worker", "--fresh"}
			if len(tc.tail) > 0 {
				args = append(append(args, "--"), tc.tail...)
			}
			code, stdout, stderr, calls := runCmdSpawn(t, f, args)
			assertSpawnReady(t, code, stdout, stderr)
			var got []string
			for _, call := range calls {
				if len(call.Argv) > 2 && call.Argv[0] == "agent" && call.Argv[1] == "start" {
					for i, arg := range call.Argv {
						if arg == "--" {
							got = call.Argv[i+1:]
							break
						}
					}
				}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("agent argv=%q want=%q stderr=%q", got, tc.want, stderr)
			}
			friction, err := os.ReadFile(filepath.Join(f.state, "ws", "friction.log"))
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if tc.flag != "" {
				want := fmt.Sprintf("spawn: codex args already set the sandbox or approvals (%s); approvals=%s adds nothing for 'worker'", tc.flag, tc.mode)
				if strings.Count(stderr, want) != 1 || strings.Count(string(friction), want) != 1 {
					t.Fatalf("want one warning %q stderr=%q friction=%q", want, stderr, friction)
				}
			} else if strings.Contains(stderr, "args already set") || strings.Contains(string(friction), "args already set") {
				t.Fatalf("unexpected override warning stderr=%q friction=%q", stderr, friction)
			}
		})
	}
}
