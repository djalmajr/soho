package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/taskreport"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

const envDropFixtureMarker = "env-drop:v1:DROP_ONE,DROP_TWO"

func TestEnvDropCLIFresh(t *testing.T) {
	f := newEnvDropCLI(t)
	envDropCLIWriteRow(t, f, "")
	f.env["SOHO_LANES"] = "on"
	f.env["SOHO_LANE_BUILD_ROLES"] = "implementer"
	f.env["SOHO_LANE_BUILD_KIND"] = "claude"
	f.env["SOHO_LANE_BUILD_PANES"] = "2"
	workerDir := filepath.Join(f.root, "worker=fixture")
	workerExe, err := fakecli.Install(t, workerDir, "claude", nil)
	if err != nil {
		t.Fatal(err)
	}
	f.env["PATH"] = workerDir + string(filepath.ListSeparator) + f.env["PATH"]
	code, out, stderr := f.run(t, "spawn", "implementer", "--fresh", "--name", "worker", "--cwd", f.cwd,
		"--env-drop", "DROP_ONE", "--env-drop", "DROP_TWO", "--env-drop", "DROP_ONE",
		"--", "--native", "two words")
	t.Logf("exit=%d", code)
	if code != 0 || !strings.Contains(out, `"name":"worker"`) {
		t.Fatalf("stdout=%q stderr=%q", out, stderr)
	}
	argv := envDropCLIArgv(t, f)
	envDropCLIAssertHelper(t, argv, workerExe)
	if !slices.Equal(argv[len(argv)-2:], []string{"--native", "two words"}) {
		t.Fatalf("terminal.run argv=%q", argv)
	}
	row := strings.Split(core.RosterLine(f.sd, "worker"), "\t")
	if len(row) != 19 || row[1] != "t7" || row[16] != "" || row[17] != "" || row[18] != envDropFixtureMarker {
		t.Fatalf("roster=%q", row)
	}
}

func TestEnvDropCLIReopen(t *testing.T) {
	for _, marker := range []string{envDropFixtureMarker, "env-drop:v1:DROP_ONE,--PRIVATE_SENTINEL", ""} {
		t.Run(marker, func(t *testing.T) {
			f := newEnvDropCLI(t)
			row := envDropCLIWorkerRow(f, marker)
			envDropCLIWriteRow(t, f, row)
			pointer := taskreport.TaskReportPointerPath(f.sd, "worker")
			before, err := os.ReadFile(pointer)
			if err != nil {
				t.Fatal(err)
			}
			code, out, stderr := f.run(t, "reopen", "worker", "--force")
			t.Logf("exit=%d", code)
			if strings.Contains(marker, "SENTINEL") {
				after, err := os.ReadFile(pointer)
				if code != 2 || err != nil || string(after) != string(before) ||
					core.RosterLine(f.sd, "worker") != row || strings.Contains(stderr, "PRIVATE_SENTINEL") {
					t.Fatalf("invalid reopen did not fail closed: stdout=%q stderr=%q", out, stderr)
				}
				envDropCLINoEffects(t, f)
				return
			}
			if code != 0 {
				t.Fatalf("stdout=%q stderr=%q", out, stderr)
			}
			argv := envDropCLIArgv(t, f)
			if marker != "" {
				envDropCLIAssertHelper(t, argv, filepath.Join(f.bin, "claude"))
			}
			if marker == "" && (len(argv) == 0 || filepath.Base(argv[0]) != "claude") {
				t.Fatalf("legacy bare row acquired a policy: argv=%q", argv)
			}
			if !slices.Contains(argv, "old-native") || !slices.Contains(envDropCLIVerbs(t, f), "kill") {
				t.Fatalf("reopen skipped ordinary behavior: argv=%q", argv)
			}
			current := strings.Split(core.RosterLine(f.sd, "worker"), "\t")
			if marker != "" && (len(current) != 19 || current[18] != marker) || marker == "" && len(current) != 16 {
				t.Fatalf("reopen roster=%q", current)
			}
		})
	}
}

func TestEnvDropCLIReopenRejectsLockedRosterReplacement(t *testing.T) {
	for _, tc := range []struct {
		name     string
		filtered bool
		replace  func(row string) string
	}{
		{"different terminal", true, func(row string) string { return strings.Replace(row, "\tt7\t", "\tt9\t", 1) }},
		{"policy changed on same terminal", true, func(row string) string {
			return strings.Replace(row, envDropFixtureMarker, "env-drop:v1:OTHER_NAME", 1)
		}},
		{"bare row gains policy", false, func(row string) string { return row + "\t\t\t" + envDropFixtureMarker }},
		{"row removed", true, func(string) string { return "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newEnvDropCLI(t)
			marker := ""
			if tc.filtered {
				marker = envDropFixtureMarker
			}
			original := envDropCLIWorkerRow(f, marker)
			envDropCLIWriteRow(t, f, original)
			replacement := tc.replace(original)

			paths := []string{taskreport.TaskReportPointerPath(f.sd, "worker"), core.LastReportPath(f.sd, "worker"),
				core.CopiesFile(f.sd), core.ProcsFile(f.sd)}
			for _, path := range paths[2:] {
				if err := os.WriteFile(path, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			before := make(map[string][]byte, len(paths))
			for _, path := range paths {
				var err error
				before[path], err = os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
			}

			prevLock := workerLifecycleLock
			t.Cleanup(func() { workerLifecycleLock = prevLock })
			locks, callsAtLock := 0, -1
			workerLifecycleLock = func(sd, name string) (func(), error) {
				if sd != f.sd || name != "worker" || locks != 0 {
					t.Fatalf("unexpected lifecycle lock %q for %q, previous acquisitions=%d", sd, name, locks)
				}
				locks++
				unlock, err := prevLock(sd, name)
				if err != nil {
					return nil, err
				}
				giveBack := false
				defer func() {
					if !giveBack {
						unlock()
					}
				}()
				if current := core.RosterLine(sd, name); current != original {
					t.Fatal("fixture did not reach the lock with the preflighted row")
				}
				callsAtLock = len(envDropCLIVerbs(t, f))
				envDropCLIWriteRow(t, f, replacement)
				giveBack = true
				return unlock, nil
			}

			code, out, stderr := f.run(t, "reopen", "worker", "--force")
			t.Logf("exit=%d locks=%d", code, locks)
			if code != 4 || out != "" || !strings.Contains(stderr, "worker roster changed after preflight") {
				t.Fatalf("locked comparison did not refuse: stdout=%q stderr=%q", out, stderr)
			}
			if locks != 1 || len(envDropCLIVerbs(t, f)) != callsAtLock || core.RosterLine(f.sd, "worker") != replacement {
				t.Fatal("locked refusal changed the roster or reached Core after acquiring the lock")
			}
			if _, err := os.Stat(f.rpcRequest); !os.IsNotExist(err) {
				t.Fatalf("refusal submitted terminal.run: %v", err)
			}
			for _, path := range paths {
				data, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(data, before[path]) {
					t.Fatalf("refusal changed %s: %v", filepath.Base(path), err)
				}
			}
		})
	}
}

func TestEnvDropCLIAdmissionLeavesExistingWorker(t *testing.T) {
	var tooMany []string
	for i := 1; i <= 17; i++ {
		tooMany = append(tooMany, "--env-drop", fmt.Sprintf("DROP_%d", i))
	}
	for _, tc := range []struct {
		name   string
		args   []string
		detail string
	}{
		{"missing fresh", []string{"--env-drop", "KEEP_TEST"}, "requires --fresh"},
		{"reuse before fresh", []string{"--reuse", "--fresh", "--env-drop", "KEEP_TEST"}, "without --reuse"},
		{"reuse after fresh", []string{"--fresh", "--reuse", "--env-drop", "KEEP_TEST"}, "without --reuse"},
		{"given pane", []string{"--fresh", "--pane", "t7", "--env-drop", "KEEP_TEST"}, "without --reuse or --pane"},
		{"explicit empty pane", []string{"--fresh", "--pane", "", "--env-drop", "KEEP_TEST"}, "without --reuse or --pane"},
		{"missing name", []string{"--fresh", "--env-drop"}, "expects a name"},
		{"empty name", []string{"--fresh", "--env-drop", ""}, "requires portable"},
		{"option-shaped name", []string{"--fresh", "--env-drop", "--PRIVATE_SENTINEL"}, "requires portable"},
		{"reserved name", []string{"--fresh", "--env-drop", "soho_PRIVATE_SENTINEL"}, "requires portable"},
		{"case collision inside drop", []string{"--fresh", "--env-drop", "PRIVATE_SENTINEL", "--env-drop", "private_sentinel"}, "differ only by case"},
		{"case collision with from", []string{"--fresh", "--env-from", "Private_Sentinel", "--env-drop", "PRIVATE_SENTINEL"}, "conflicts with --env-from"},
		{"exact collision with from", []string{"--fresh", "--env-drop", "PRIVATE_SENTINEL", "--env-from", "PRIVATE_SENTINEL"}, "conflicts with --env-from"},
		{"PATH without resolved agent", []string{"--kind", "pi", "--fresh", "--env-drop", "PATH"}, "requires a resolved absolute worker executable"},
		{"over cap", append([]string{"--fresh"}, tooMany...), "at most 16"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newEnvDropCLI(t)
			f.env["SOHO_REUSE_WORKERS"] = "off"
			row := core.RosterLine(f.sd, "worker")
			pointer := taskreport.TaskReportPointerPath(f.sd, "worker")
			before, err := os.ReadFile(pointer)
			if err != nil {
				t.Fatal(err)
			}
			code, out, stderr := f.run(t, append([]string{"spawn", "implementer", "--name", "worker"}, tc.args...)...)
			t.Logf("exit=%d", code)
			after, err := os.ReadFile(pointer)
			if code != 2 || out != "" || !strings.Contains(stderr, tc.detail) ||
				strings.Contains(strings.ToLower(stderr), "private_sentinel") || err != nil || !bytes.Equal(before, after) ||
				core.RosterLine(f.sd, "worker") != row {
				t.Fatalf("refusal did not preserve the task/roster: stdout=%q stderr=%q", out, stderr)
			}
			envDropCLINoEffects(t, f)
		})
	}
}

func TestEnvDropCLIReopenMissingExecutable(t *testing.T) {
	f := newEnvDropCLI(t)
	row := envDropCLIWorkerRow(f, envDropFixtureMarker)
	envDropCLIWriteRow(t, f, row)
	if err := os.Remove(filepath.Join(f.bin, "claude")); err != nil {
		t.Fatal(err)
	}
	pointer := taskreport.TaskReportPointerPath(f.sd, "worker")
	before, err := os.ReadFile(pointer)
	if err != nil {
		t.Fatal(err)
	}
	code, out, stderr := f.run(t, "reopen", "worker", "--force")
	t.Logf("exit=%d", code)
	after, err := os.ReadFile(pointer)
	if code != 2 || out != "" || !strings.Contains(stderr, "requires a resolved absolute worker executable") ||
		err != nil || !bytes.Equal(before, after) ||
		core.RosterLine(f.sd, "worker") != row {
		t.Fatalf("unusable replacement did not fail closed: stdout=%q stderr=%q", out, stderr)
	}
	envDropCLINoEffects(t, f)
}

func TestEnvDropCLILegacyRefusal(t *testing.T) {
	f := newReopenFixture(t, "idle")
	original := core.RosterLine(f.state, "worker")
	oldOut, oldErr := platform.Stdout, platform.Stderr
	defer func() { platform.Stdout, platform.Stderr = oldOut, oldErr }()
	var out, stderr bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &stderr
	code := Run([]string{"spawn", "implementer", "--fresh", "--env-drop", "DROP_ONE"}, f.env)
	t.Logf("exit=%d", code)
	if code != 2 || out.Len() != 0 || !strings.Contains(stderr.String(), "--env-drop requires native Core") ||
		core.RosterLine(f.state, "worker") != original {
		t.Fatalf("legacy did not refuse the filter: stdout=%q stderr=%q", out.String(), stderr.String())
	}
	calls, err := fakecli.ReadCallsForConfig(f.configFile)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, call := range calls {
		if !slices.Equal(call.Argv, []string{"pane", "current"}) {
			t.Fatalf("legacy refusal reached unexpected command: %v", call.Argv)
		}
	}
}

func TestEnvDropCLIReuse(t *testing.T) {
	for _, lane := range []bool{false, true} {
		for _, marked := range []bool{false, true} {
			t.Run(fmt.Sprintf("lane_%t_marked_%t", lane, marked), func(t *testing.T) {
				f := newEnvDropCLI(t)
				if lane {
					f.env["SOHO_LANES"] = "on"
					f.env["SOHO_LANE_BUILD_ROLES"] = "implementer"
					f.env["SOHO_LANE_BUILD_KIND"] = "claude"
					f.env["SOHO_LANE_BUILD_PANES"] = "1"
				}
				envDropCLIWriteRow(t, f, "")
				args := []string{"spawn", "implementer", "--kind", "claude", "--name", "worker", "--cwd", f.cwd}
				code, out, stderr := f.run(t, append(slices.Clone(args), "--fresh")...)
				if code != 0 || !strings.Contains(out, `"pane_id":"t7"`) {
					t.Fatalf("bare seed spawn: code=%d stdout=%q stderr=%q", code, out, stderr)
				}
				line := core.RosterLine(f.sd, "worker")
				if len(strings.Split(line, "\t")) != 16 {
					t.Fatalf("ordinary spawn changed the historical row shape: %q", line)
				}
				if marked {
					line += "\t\t\t" + envDropFixtureMarker
					envDropCLIWriteRow(t, f, line)
				}
				f.rpcRequest = filepath.Join(f.root, "second-request.json")
				f.env["SOHO_REOPEN_RPC_REQUEST"] = f.rpcRequest
				envDropCLISeparateWorker(t, f)
				code, out, stderr = f.run(t, append(slices.Clone(args), "--reuse")...)
				t.Logf("exit=%d", code)
				if core.RosterLine(f.sd, "worker") != line {
					t.Fatal("the existing worker's row changed")
				}
				switch {
				case !marked:
					if code != 0 || !strings.Contains(out, `"reused":true`) {
						t.Fatalf("ordinary reuse: stdout=%q stderr=%q", out, stderr)
					}
					envDropCLINoEffects(t, f)
				case lane:
					if code != 10 || strings.Contains(out, `"reused":true`) {
						t.Fatalf("filtered worker reused in full lane: stdout=%q stderr=%q", out, stderr)
					}
					envDropCLINoEffects(t, f)
				default:
					if code != 0 || !strings.Contains(out, `"pane_id":"t8"`) || strings.Contains(out, `"reused":true`) {
						t.Fatalf("filtered worker reused outside a lane: stdout=%q stderr=%q", out, stderr)
					}
					argv := envDropCLIArgv(t, f)
					if len(argv) == 0 || filepath.Base(argv[0]) != "claude" {
						t.Fatalf("unfiltered replacement acquired the old worker's policy: %q", argv)
					}
				}
			})
		}
	}
}

func envDropCLISeparateWorker(t *testing.T, f *reopenHcFixture) {
	t.Helper()
	path := filepath.Join(f.bin, "hc.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Log   string         `json:"log"`
		Rules []fakecli.Rule `json:"rules"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	const scoped = `{"terminals":[{"address":"t7","name":"worker","state":"live","status":{"state":"idle"},"labels":{"soho.session":"sX"}}]}`
	config.Rules = append([]fakecli.Rule{
		{Argv: []string{"ls"}, ArgvPrefix: true, Stdout: scoped},
		{Argv: []string{"get", "t8", "--json"}, Stdout: `{"address":"t8","state":"live","reported":true,"status":{"state":"idle"}}`},
	}, config.Rules...)
	data, err = json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	f.env["SOHO_REOPEN_RPC_FRAME"] = `{"jsonrpc":"2.0","id":2,"result":{"address":"t8"}}`
}

func newEnvDropCLI(t *testing.T) *reopenHcFixture {
	t.Helper()
	if platform.Current() != "darwin" && platform.Current() != "linux" {
		t.Skip("native fixture requires macOS or Linux")
	}
	return newReopenHcFixture(t)
}

func envDropCLIWorkerRow(f *reopenHcFixture, marker string) string {
	fields := []string{"worker", "t7", "claude", "implementer", "anthropic", "1",
		f.cwd, "now", "", "ask", "implementer", "", "", "old-native", "", "inherit"}
	if marker != "" {
		fields = append(fields, "", "", marker)
	}
	return strings.Join(fields, "\t")
}

func envDropCLIWriteRow(t *testing.T, f *reopenHcFixture, row string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.sd, "agents.tsv"), []byte("# header\n"+row+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func envDropCLIArgv(t *testing.T, f *reopenHcFixture) []string {
	t.Helper()
	raw, err := os.ReadFile(f.rpcRequest)
	if err != nil {
		t.Fatal(err)
	}
	var request struct {
		Method string
		Params struct {
			Argv []string
		}
	}
	if err := json.Unmarshal(raw, &request); err != nil || request.Method != "terminal.run" {
		t.Fatalf("invalid fake Core request: %v", err)
	}
	return request.Params.Argv
}

func envDropCLIAssertHelper(t *testing.T, argv []string, workerExe string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{self, "--soho-worker-env-exec", "DROP_ONE", "DROP_TWO", "--", workerExe}
	if len(argv) < len(want) || !slices.Equal(argv[:len(want)], want) {
		t.Fatalf("terminal.run argv=%q; want prefix %q", argv, want)
	}
}

func envDropCLIVerbs(t *testing.T, f *reopenHcFixture) []string {
	t.Helper()
	calls, err := fakecli.ReadCalls(filepath.Join(f.bin, "hc.calls.jsonl"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var verbs []string
	for _, call := range calls {
		if len(call.Argv) > 0 {
			verbs = append(verbs, call.Argv[0])
		}
	}
	t.Logf("fake Core verbs=%v", verbs)
	return verbs
}

func envDropCLINoEffects(t *testing.T, f *reopenHcFixture) {
	t.Helper()
	for _, verb := range envDropCLIVerbs(t, f) {
		if verb != "get" && verb != "ls" {
			t.Fatalf("refusal reached hc %s", verb)
		}
	}
	if _, err := os.Stat(f.rpcRequest); !os.IsNotExist(err) {
		t.Fatalf("refusal reached terminal.run: %v", err)
	}
}
