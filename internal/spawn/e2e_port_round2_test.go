package spawn

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestReview550ExistingHerdDoesNotNeedPort(t *testing.T) {
	for _, mode := range []string{"off", "enabled", "given"} {
		t.Run(mode, func(t *testing.T) {
			probes := 0
			injectE2EHost(t, func(int) error { probes++; return syscall.EADDRINUSE })
			f := e2eFixture(t, "tab")
			if mode == "off" {
				delete(f.env, "SOHO_E2E_PORT_BASE")
			}
			sd := filepath.Join(f.state, "ws")
			if e := os.WriteFile(filepath.Join(sd, "herd-tab"), []byte("t-herd\therd\tauto\n"), 0600); e != nil {
				t.Fatal(e)
			}
			args := []string{"worker", "--name", "worker", "--fresh"}
			if mode == "given" {
				args = append(args, "--pane", "p-root")
			}
			code, out, stderr, calls := runCmdSpawn(t, f, args)
			t.Logf("mode=%s exit=%d host_probes=%d stdout=%q stderr=%q", mode, code, probes, out, stderr)
			assertE2ECreatePorts(t, calls)
			if code != 0 || probes != 0 || strings.Contains(out, `"e2e_port"`) {
				t.Errorf("an existing herd shell requires no newly exported port: exit=%d probes=%d", code, probes)
			}
		})
	}
}

func TestReview550OffSpawnOutput(t *testing.T) {
	for _, mode := range []string{"split", "tab"} {
		t.Run(mode, func(t *testing.T) {
			injectE2EHost(t, func(int) error { t.Fatal("off host probe"); return nil })
			f := e2eFixture(t, mode)
			delete(f.env, "SOHO_E2E_PORT_BASE")
			args := []string{"worker", "--name", "worker", "--fresh"}
			if mode == "tab" {
				args = append(args, "--tab-label", "worker")
			} else {
				args = append(args, "--direction", "right")
			}
			code, out, stderr, calls := runCmdSpawn(t, f, args)
			if code != 0 {
				t.Fatalf("exit=%d out=%s err=%s", code, out, stderr)
			}
			t.Logf("off-%s-json=%q", mode, strings.ReplaceAll(out, f.root, "$ROOT"))
			for _, call := range calls {
				if len(call.Argv) > 1 && ((call.Argv[0] == "pane" && call.Argv[1] == "split") || (call.Argv[0] == "tab" && call.Argv[1] == "create")) {
					t.Logf("off-%s-argv=%q", mode, strings.ReplaceAll(strings.Join(call.Argv, "\x00"), f.root, "$ROOT"))
				}
			}
			raw, e := os.ReadFile(filepath.Join(f.state, "ws", "agents.tsv"))
			if e != nil {
				t.Fatal(e)
			}
			lines := strings.Split(string(raw), "\n")
			for i, line := range lines {
				if strings.HasPrefix(line, "worker\t") {
					fields := strings.Split(line, "\t")
					if len(fields) != 16 {
						t.Fatal(fields)
					}
					fields[7] = "STAMP"
					lines[i] = strings.Join(fields, "\t")
				}
			}
			t.Logf("off-%s-roster=%q", mode, strings.ReplaceAll(strings.Join(lines, "\n"), f.root, "$ROOT"))
		})
	}
}
