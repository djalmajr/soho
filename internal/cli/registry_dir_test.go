package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

// Exercise every consumer through Run, including destination-roster ownership
// and invalid-hint fallback. The process identity here is a fake ps response;
// TestProcsWorkerState separately retains the native owned-process check.
func TestWorkerStateRegistryValues(t *testing.T) {
	for _, command := range []string{"mutation-copy", "copies", "procs"} {
		for _, value := range []string{"valid", "relative", "missing", "empty", "file", "queued"} {
			t.Run(command+"/"+value, func(t *testing.T) {
				if command == "procs" && runtime.GOOS == "windows" {
					t.Skip("the simulated ps identity is Unix-only; native process coverage is TestProcsWorkerState")
				}
				f := newCopiesFixture(t, nil)
				f.writeRoster(t, copiesRosterRow("worker", "p-worker"))
				local := f.stateDir(t)
				target := filepath.Join(f.root, "registry", "ws")
				hint := target
				if value == "relative" {
					hint = filepath.Join("relative-registry", "ws")
					target = filepath.Join(f.cwd, hint)
				}
				if err := os.MkdirAll(target, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(target, "agents.tsv"), []byte("# roster\n"+copiesRosterRow("registry-worker", "p-worker")+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
				switch value {
				case "missing":
					hint = filepath.Join(f.root, "missing-registry", "ws")
				case "empty":
					hint = ""
				case "file":
					hint = filepath.Join(f.root, "registry-file")
					if err := os.WriteFile(hint, []byte("not a directory\n"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				env := f.env.Clone()
				env["SOHO_REGISTRY_DIR"] = hint
				want, owner := local, "worker"
				if value == "valid" || value == "queued" {
					want, owner = target, "registry-worker"
				}
				copyDir := filepath.Join(f.root, "registered-copy")
				args := []string{command}
				pid := 987654321 // simulated, never inspected by native ps or killed
				switch command {
				case "mutation-copy":
					args = append(args, "--source", f.repo, "--dest", copyDir)
				case "copies":
					if err := os.MkdirAll(copyDir, 0700); err != nil {
						t.Fatal(err)
					}
					args = append(args, "add", copyDir)
				case "procs":
					fakeDir := env.Get("SOHO_FAKECLI_CONFIG")
					if _, err := fakecli.Install(t, fakeDir, "ps", []fakecli.Rule{{Argv: []string{"-o", "lstart=,state=,comm=", "-p", strconv.Itoa(pid)}, Stdout: "Wed Oct 21 10:00:00 2026 S fixture-worker\n"}}); err != nil {
						t.Fatal(err)
					}
					args = append(args, "add", strconv.Itoa(pid))
				}
				if value == "queued" {
					if command == "procs" {
						old := upsertProc
						upsertProc = func(string, core.ProcRow) error { return os.ErrPermission }
						t.Cleanup(func() { upsertProc = old })
					} else {
						refuseCopiesRegistry(t, nil)
					}
				}
				code, out, stderr := f.run(t, env, args...)
				if code != 0 {
					t.Fatalf("command=%q hint=%q want=%q code=%d out=%q stderr=%q", args, hint, want, code, out, stderr)
				}
				if value == "queued" {
					queue := core.PendingCopiesFile(env, "ws")
					if command == "procs" {
						queue = core.PendingProcsFile(env, "ws")
					}
					raw := readQueue(t, queue)
					if !strings.Contains(raw, "\n"+want+"\t") || !strings.Contains(raw, "\t"+owner+"\tp-worker\t") || !strings.Contains(stderr, "queued for the orchestrator") {
						t.Fatalf("queue=%q stderr=%q want destination=%q owner=%q", raw, stderr, want, owner)
					}
				} else if stderr != "" {
					t.Fatalf("new warning: %q", stderr)
				}
				if command == "procs" {
					rows, err := core.ReadProcs(want)
					if err != nil || (value == "queued" && len(rows) != 0) || (value != "queued" && (len(rows) != 1 || rows[0].Pid != pid || rows[0].Owner != owner || rows[0].Pane != "p-worker")) {
						t.Fatalf("procs destination=%q rows=%+v err=%v", want, rows, err)
					}
				} else {
					rows, err := core.ReadCopies(want)
					if err != nil || (value == "queued" && len(rows) != 0) || (value != "queued" && (len(rows) != 1 || rows[0].Path != copyDir || rows[0].Owner != owner || rows[0].Pane != "p-worker")) {
						t.Fatalf("copies destination=%q rows=%+v err=%v", want, rows, err)
					}
				}
				other := target
				if want == target {
					other = local
				}
				copies, ce := core.ReadCopies(other)
				procs, pe := core.ReadProcs(other)
				if ce != nil || pe != nil || len(copies) != 0 || len(procs) != 0 {
					t.Fatalf("wrong destination %q copies=%+v procs=%+v errors=%v/%v", other, copies, procs, ce, pe)
				}
				if value == "missing" {
					if _, err := os.Stat(hint); !os.IsNotExist(err) {
						t.Fatalf("invalid hint created: %q err=%v", hint, err)
					}
				}
			})
		}
	}
}
