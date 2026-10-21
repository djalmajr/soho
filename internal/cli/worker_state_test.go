package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

const workerStateProcessEnv = "SOHO_WORKER_STATE_FIXTURE"

// Re-execute this test binary as the real CLI process, before suite isolation
// changes cwd. The permission seam is installed only in this disposable child.
func init() {
	mode := os.Getenv(workerStateProcessEnv)
	if mode == "" {
		return
	}
	if mode == "denied" {
		upsertCopies = func(string, core.CopyRow) error { return os.ErrPermission }
	}
	os.Exit(Run(os.Args[1:], platform.EnvFromOS()))
}

func TestMutationWorkerStateProcess(t *testing.T) {
	for _, queued := range []bool{false, true} {
		name := "direct"
		if queued {
			name = "queued"
		}
		t.Run(name, func(t *testing.T) {
			f, _, env, dest := mutationQueueSetup(t)
			orchestrator := filepath.Join(f.root, "orchestrator repo")
			gitRepoAll(t, f, orchestrator, "file.txt", "orchestrator\n")
			// Both a project setting and inherited relative SOHO_DIR must leave
			// resource registrations pinned, without changing configuration.
			gitRepoAll(t, f, f.source, filepath.Join(".agents", "soho.conf"), "state_dir=foreign-state\n")
			env["SOHO_DIR"] = "relative-state"
			root := filepath.Join(orchestrator, "state 'shared'")
			sd := filepath.Join(root, "ws")
			env["SOHO_REGISTRY_DIR"] = sd
			if err := os.MkdirAll(sd, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(sd, "agents.tsv"), []byte("# roster\n"+releaseRow("worker", "p-worker", "1", "implementer", false, f.source)+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			workerEnv := f.env.Clone()
			for k, v := range env {
				workerEnv[k] = v
			}
			workerEnv[workerStateProcessEnv] = "direct"
			if queued {
				workerEnv[workerStateProcessEnv] = "denied"
			}
			self, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(self, "mutation-copy", "--source", f.source, "--dest", dest)
			cmd.Dir = f.source
			cmd.Env = workerEnv.List()
			var out, stderr bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &stderr
			if err = cmd.Run(); err != nil {
				t.Fatalf("worker cwd=%q argv=%q envState=%q error=%v stdout=%s stderr=%s", cmd.Dir, cmd.Args, root, err, &out, &stderr)
			}
			if got := decodedCopy(t, out.String()); got.Copy != dest || got.Source != f.source {
				t.Fatalf("result=%+v", got)
			}
			rows, err := core.ReadCopies(sd)
			if err != nil {
				t.Fatal(err)
			}
			queue := core.PendingCopiesFile(workerEnv, "ws")
			if queued {
				if len(rows) != 0 {
					t.Fatalf("denied registry was written: %+v", rows)
				}
				raw := readQueue(t, queue)
				if !strings.Contains(raw, "\n"+sd+"\t"+dest+"\tworker\tp-worker\t") {
					t.Fatalf("wrong queue destination/owner: %q", raw)
				}
				if !strings.Contains(stderr.String(), "queued for the orchestrator") {
					t.Fatalf("stderr=%q", stderr.String())
				}
			} else if len(rows) != 1 || rows[0].Owner != "worker" || rows[0].Path != dest || rows[0].Pane != "p-worker" || stderr.Len() != 0 {
				t.Fatalf("rows=%+v stderr=%q", rows, stderr.String())
			}
			if _, err = os.Stat(filepath.Join(f.source, "relative-state", "ws", "copies.tsv")); !os.IsNotExist(err) {
				t.Fatalf("worker-local registry exists: %v", err)
			}
			fakeDir := t.TempDir()
			_, err = fakecli.Install(t, fakeDir, "herdr", []fakecli.Rule{
				{Argv: []string{"agent", "get", "worker"}, Stdout: agentIdleJSON},
				{Argv: []string{"pane", "close", "p-worker"}},
				{Argv: []string{"pane", "report-metadata"}, ArgvPrefix: true},
				{Argv: []string{"pane", "list"}, ArgvPrefix: true, Stdout: `{"result":{"panes":[]}}`},
			})
			if err != nil {
				t.Fatal(err)
			}
			orchestratorEnv := withFakeCLI(workerEnv, fakeDir)
			delete(orchestratorEnv, workerStateProcessEnv)
			delete(orchestratorEnv, "SOHO_REGISTRY_DIR")
			orchestratorEnv["SOHO_DIR"] = root
			orchestratorEnv["HERDR_ENV"] = "1"
			orchestratorEnv["SOHO_REGRID"] = "off"
			code, releaseOut, releaseErr := runIn(t, []string{"release", "worker", "--close", "--force"}, orchestratorEnv, orchestrator)
			if code != 0 {
				t.Fatalf("release code=%d out=%s err=%s", code, releaseOut, releaseErr)
			}
			if _, err = os.Stat(dest); !os.IsNotExist(err) {
				t.Fatalf("release left copy: %v; out=%s err=%s", err, releaseOut, releaseErr)
			}
			rows, err = core.ReadCopies(sd)
			if err != nil || len(rows) != 0 {
				t.Fatalf("registry after release=%+v err=%v", rows, err)
			}
			if queued {
				if _, err = os.Stat(queue); !os.IsNotExist(err) {
					t.Fatalf("queue not incorporated: %v", err)
				}
			}
		})
	}
}

func TestMutationWorkerStateSameProject(t *testing.T) {
	f := newMutationCopyFixture(t)
	env := f.env.Clone()
	env["SOHO_SKILL_DIR"] = testSkillDir(t)
	env["HERDR_WORKSPACE_ID"] = "ws"
	env["SOHO_NOWRITE"] = "1"
	delete(env, "SOHO_DIR")
	delete(env, "SOHO_STATE_DIR")
	pre := core.SessionStateRootPath(env, f.source)
	session := filepath.Join(pre, "ws", "session.conf")
	if err := os.MkdirAll(filepath.Dir(session), 0700); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(f.root, "session-state")
	if err := os.WriteFile(session, []byte("state_dir="+moved+"\napprovals=ask\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := core.LoadConfig(env, f.source)
	without, err := core.StateDirResolved(&ctx, env, f.source)
	if err != nil {
		t.Fatal(err)
	}
	code, approvals, stderr := runIn(t, []string{"config", "get", "approvals"}, env, f.source)
	if code != 0 || strings.TrimSpace(approvals) != "ask" || stderr != "" {
		t.Fatalf("control code=%d approvals=%q stderr=%q", code, approvals, stderr)
	}
	if without != filepath.Join(moved, "ws") {
		t.Fatalf("session state=%q want=%q", without, filepath.Join(moved, "ws"))
	}
	if err := os.MkdirAll(without, 0700); err != nil {
		t.Fatal(err)
	}
	env["SOHO_REGISTRY_DIR"] = without
	ctx = core.LoadConfig(env, f.source)
	with, err := core.StateDirResolved(&ctx, env, f.source)
	if err != nil || with != without {
		t.Fatalf("same project: without=%q with=%q err=%v", without, with, err)
	}
	code, after, stderr := runIn(t, []string{"config", "get", "approvals"}, env, f.source)
	if code != 0 || after != approvals || stderr != "" {
		t.Fatalf("with registry=%q code=%d approvals before=%q after=%q stderr=%q", without, code, approvals, after, stderr)
	}
}

func TestProcsWorkerState(t *testing.T) {
	f := newProcsFixture(t)
	f.env["SOHO_DIR"] = "relative-state"
	f.env["SOHO_REGISTRY_DIR"] = f.state
	other := t.TempDir()
	cmd := exec.Command("git", "init", "-q", other)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, output)
	}
	f.cwd = other
	pid := f.sleeper(t)
	code, out, stderr := f.run(t, "procs", "add", strconv.Itoa(pid))
	if code != 0 {
		t.Fatalf("foreign worker procs add pid=%d code=%d out=%s err=%s", pid, code, out, stderr)
	}
	rows := f.procsRows(t)
	if len(rows) != 1 || rows[0].Pid != pid || rows[0].Owner != "worker" || rows[0].Pane != "p-worker" {
		t.Fatalf("orchestrator registry=%+v", rows)
	}
}

func TestReopenWorkerState(t *testing.T) {
	f := newReopenFixture(t, "idle")
	setReopenRules(t, f)
	run := f.reopen(t, "worker", "--force")
	if run.code != 0 {
		t.Fatalf("code=%d out=%s err=%s", run.code, run.stdout, run.stderr)
	}
	assertWorkerStateCreation(t, run.calls, f.state)
}

func TestRunWorkerState(t *testing.T) {
	for _, given := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "given"}[given], func(t *testing.T) {
			h := newTM3bHarness(t, tm3bFullBrief())
			prepareRunHarness(t, &h)
			configPath := filepath.Join(h.root, "bin", "herdr.json")
			var config struct {
				Log   string         `json:"log"`
				Rules []fakecli.Rule `json:"rules"`
			}
			b, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(b, &config); err != nil {
				t.Fatal(err)
			}
			config.Rules = append([]fakecli.Rule{{Argv: []string{"pane", "list"}, ArgvPrefix: true, Stdout: `{"result":{"panes":[]}}`}, {Argv: []string{"tab", "create"}, ArgvPrefix: true, Stdout: `{"result":{"tab":{"tab_id":"t-herd"},"root_pane":{"pane_id":"p-new"}}}`}}, config.Rules...)
			b, err = json.Marshal(config)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(configPath, b, 0600); err != nil {
				t.Fatal(err)
			}
			args := []string{"--no-wait"}
			if given {
				args = append(args, "--pane", "p-existing")
			}
			code, out, stderr := runForTest(t, h, args...)
			if code != 0 {
				t.Fatalf("code=%d out=%s err=%s", code, out, stderr)
			}
			for _, call := range mustRunCalls(t, h) {
				for _, arg := range call.Argv {
					if given && arg == "--env" {
						t.Fatalf("existing pane changed: %q", call.Argv)
					}
				}
			}
			if !given {
				assertWorkerStateCreation(t, mustRunCalls(t, h), h.ws)
			}
		})
	}
}

func assertWorkerStateCreation(t *testing.T, calls []fakecli.Call, root string) {
	t.Helper()
	n := 0
	for _, call := range calls {
		a := call.Argv
		if len(a) < 2 {
			continue
		}
		if (a[0] == "pane" && a[1] == "split") || (a[0] == "tab" && a[1] == "create") {
			n++
			if !containsReopenArgPair(a, "--env", "SOHO_REGISTRY_DIR="+root) {
				t.Fatalf("creation missing state: %q want=%q", a, root)
			}
		}
	}
	if n == 0 {
		t.Fatalf("no creation: %+v", calls)
	}
}
