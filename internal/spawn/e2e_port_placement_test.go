package spawn

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

func TestE2EPortPlacementBeforeReservation(t *testing.T) {
	for _, placement := range []string{"split", "new-tab", "herd-split", "labelled-herd-split"} {
		for _, busy := range []bool{false, true} {
			name := placement + "-available"
			if busy {
				name = placement + "-exhausted"
			}
			t.Run(name, func(t *testing.T) {
				probes := []int{}
				injectE2EHost(t, func(port int) error {
					probes = append(probes, port)
					if busy {
						return syscall.EADDRINUSE
					}
					return nil
				})
				mode := "tab"
				if placement == "split" {
					mode = "split"
				}
				f := e2eFixture(t, mode)
				sd := filepath.Join(f.state, "ws")
				args := []string{"worker", "--name", "worker", "--fresh"}
				switch placement {
				case "split":
					args = append(args, "--direction", "right")
				case "new-tab":
					args = append(args, "--tab-label", "new-worker")
				default:
					if err := os.WriteFile(filepath.Join(sd, "herd-tab"), []byte("t-herd\therd\tauto\n"), 0600); err != nil {
						t.Fatal(err)
					}
					rules := []fakecli.Rule{{Argv: []string{"pane", "list"}, ArgvPrefix: true, Stdout: `{"result":{"panes":[{"pane_id":"p-root","tab_id":"t-herd"}]}}`}}
					writeWorkerStateRules(t, f, append(rules, freshSpawnRules()...))
					if placement == "labelled-herd-split" {
						args = append(args, "--tab-label", "herd")
					}
				}
				if busy {
					code, message, calls := runCmdSpawnExpectError(t, f, args)
					want := "spawn: no free E2E port in the 200-port range from e2e_port_base=47000"
					if code != 4 || message != want || len(probes) != 200 || probes[0] != 47000 || probes[199] != 47199 {
						t.Fatalf("code=%d message=%q probes=%v", code, message, probes)
					}
					assertE2ECreatePorts(t, calls)
					assertNoStart(t, calls)
					if core.RosterLine(sd, "worker") != "" {
						t.Fatal("exhausted placement registered a worker")
					}
					t.Logf("placement=%s exit=%d probes=%d message=%q; zero pane creation/start calls", placement, code, len(probes), message)
				} else {
					code, out, stderr, calls := runCmdSpawn(t, f, args)
					if code != 0 || len(probes) != 1 || probes[0] != 47000 || !strings.Contains(out, `"e2e_port": "47000"`) {
						t.Fatalf("code=%d out=%s stderr=%s probes=%v", code, out, stderr, probes)
					}
					assertE2ECreatePorts(t, calls, "47000")
					fields := strings.Split(core.RosterLine(sd, "worker"), "\t")
					if len(fields) != 17 || fields[16] != "47000" {
						t.Fatalf("roster=%v", fields)
					}
					t.Logf("placement=%s exit=0 probes=1; exported 47000 and recorded column 17", placement)
				}
				if _, err := os.Stat(filepath.Join(sd, "e2e-ports.lock")); !os.IsNotExist(err) {
					t.Fatalf("reservation lock cleanup: %v", err)
				}
			})
		}
	}
}
