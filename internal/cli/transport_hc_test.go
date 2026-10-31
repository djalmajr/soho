package cli

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/djalmajr/soho/internal/core"
	"github.com/djalmajr/soho/internal/platform"
	"github.com/djalmajr/soho/internal/testutil/fakecli"
)

const hcRefusalLine = "does not support transport=hc yet; set transport=herdr or see DJA-604"

// treePaths lists every path under root, relative to it ("" for root
// itself).
func treePaths(t *testing.T, root string) []string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		paths = append(paths, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

// hcGuardFixture is one temp project root with an empty bin dir on PATH
// (no hc, no herdr: a refused command must not launch anything) and the
// hc-mode environment.
type hcGuardFixture struct {
	root string
	env  platform.Env
}

func newHcGuardFixture(t *testing.T) hcGuardFixture {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	env := platform.Env{
		"HOME":           root,
		"SOHO_DIR":       filepath.Join(root, "state"),
		"TMPDIR":         root,
		"PATH":           bin,
		"SOHO_TRANSPORT": "hc",
	}
	return hcGuardFixture{root: root, env: env}
}

// run swaps the platform writers around one Run call and returns the exit
// code plus the captured stdout and stderr.
func (f hcGuardFixture) run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, errOut bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &errOut
	code := Run(append([]string{}, args...), f.env.Clone())
	platform.Stdout, platform.Stderr = oldOut, oldErr
	return code, out.String(), errOut.String()
}

// TestHcGuardRefusesEveryOtherCommand is the entry-guard table under
// SOHO_TRANSPORT=hc (DJA-604 stage 2b): every command outside the allowed
// set dies with exit 2 and the one exact line, before any read or write of
// its own (the fixture tree is unchanged and the empty PATH holds no
// binary to launch); every command in the allowed set gets past the guard
// (it runs its own path and dies, if at all, with its own message).
func TestHcGuardRefusesEveryOtherCommand(t *testing.T) {
	f := newHcGuardFixture(t)
	// Warm the process-level skill bundle cache once, before the table:
	// the first config load in this process writes $HOME/.cache/soho
	// (the bundled skill snapshot), which is a process-level artifact, not
	// a command effect. The per-command tree snapshot then measures the
	// command itself.
	f.run(t, "config")
	// The full allowed set: the read side (status, wait), the explicitly
	// allowed commands (doctor, config, explain, capabilities), the usage
	// and version invocations, and the commands that never touch the
	// transport (kinds, models, model, roles, role, lint, install, env).
	allowed := map[string]bool{
		"": true, "help": true, "-h": true, "--help": true,
		"status": true, "wait": true,
		"doctor": true, "config": true, "explain": true, "capabilities": true,
		"kinds": true, "models": true, "model": true, "roles": true, "role": true,
		"lint": true, "install": true, "env": true,
	}
	refused := []string{
		"session", "spawn", "dispatch", "find", "run", "resume", "collect",
		"send", "stats", "release", "clean", "setup", "init",
		"title", "regrid", "roster", "friction", "feedback", "tab-label",
		"layout-plan", "mutation-guard", "mutation-copy", "review-snapshot",
		"reopen", "compact", "metrics", "copies", "gc", "procs", "promote",
		"collaborate", "hook", "job", "plugin",
	}
	for _, command := range refused {
		command := command
		t.Run("refused "+command, func(t *testing.T) {
			before := treePaths(t, f.root)
			code, out, errOut := f.run(t, command)
			if code != 2 {
				t.Fatalf("exit = %d, want 2 (out=%q stderr=%q)", code, out, errOut)
			}
			want := "soho: " + command + " " + hcRefusalLine + "\n"
			if errOut != want {
				t.Fatalf("stderr = %q, want exactly %q", errOut, want)
			}
			if out != "" {
				t.Fatalf("stdout = %q, want nothing before the refusal", out)
			}
			after := treePaths(t, f.root)
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("the refusal touched the tree: before %#v after %#v", before, after)
			}
		})
	}
	t.Run("unknown command keeps its own diagnostic", func(t *testing.T) {
		code, out, errOut := f.run(t, "frobnicate")
		if code != 2 || out != "" {
			t.Fatalf("exit = %d out = %q, want exit 2 and no output", code, out)
		}
		if want := "soho: unknown command 'frobnicate'\n"; errOut != want {
			t.Fatalf("stderr = %q, want exactly %q", errOut, want)
		}
	})
	for command := range allowed {
		command := command
		t.Run("allowed "+command, func(t *testing.T) {
			code, out, errOut := f.run(t, command)
			if strings.Contains(errOut, hcRefusalLine) {
				t.Fatalf("exit = %d stderr = %q, the guard refused an allowed command", code, errOut)
			}
			switch command {
			case "", "help", "-h", "--help":
				if code != 0 || out == "" {
					t.Fatalf("exit = %d out = %q, want the usage", code, out)
				}
			case "env", "config", "roles", "kinds":
				if code != 0 {
					t.Fatalf("exit = %d stderr = %q, want the command's own result", code, errOut)
				}
			}
		})
	}
}

// TestHcGuardRefusesFromFileMode pins the guard reading the resolved mode:
// with the project file selecting transport=hc and no SOHO_TRANSPORT in
// the environment, a config-loading command (spawn) is refused by the
// guard with the exact line.
func TestHcGuardRefusesFromFileMode(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(filepath.Join(root, ".agents"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".agents", "soho.conf"), []byte("transport=hc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	env := platform.Env{
		"HOME":     root,
		"SOHO_DIR": filepath.Join(root, "state"),
		"TMPDIR":   root,
		"PATH":     bin,
	}
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, errOut bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &errOut
	code := Run([]string{"spawn"}, env)
	platform.Stdout, platform.Stderr = oldOut, oldErr
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (out=%q stderr=%q)", code, out.String(), errOut.String())
	}
	want := "soho: spawn " + hcRefusalLine + "\n"
	if errOut.String() != want {
		t.Fatalf("stderr = %q, want %q", errOut.String(), want)
	}
}

// TestTransportModeForPropagatesToTheCommandEnv pins the mode propagation
// (DJA-604 stage 2b): a project .agents/soho.conf holding transport=hc
// leaves SOHO_TRANSPORT=hc on the env the command and its helpers receive,
// and the default leaves herdr.
func TestTransportModeForPropagatesToTheCommandEnv(t *testing.T) {
	t.Run("project file hc", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, ".agents"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".agents", "soho.conf"), []byte("transport=hc\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Chdir(root)
		base := platform.Env{"HOME": root, "SOHO_DIR": filepath.Join(root, "state")}
		ctx := core.LoadConfigFor(base, root, core.UserSelection{})
		decision := base.Clone()
		if mode := transportModeFor(&ctx, base, decision); mode != "hc" {
			t.Fatalf("mode = %q, want hc", mode)
		}
		if base["SOHO_TRANSPORT"] != "hc" || decision["SOHO_TRANSPORT"] != "hc" {
			t.Fatalf("envs carry SOHO_TRANSPORT=%q / %q, want hc on both", base["SOHO_TRANSPORT"], decision["SOHO_TRANSPORT"])
		}
	})
	t.Run("default herdr", func(t *testing.T) {
		root := t.TempDir()
		t.Chdir(root)
		base := platform.Env{"HOME": root, "SOHO_DIR": filepath.Join(root, "state")}
		ctx := core.LoadConfigFor(base, root, core.UserSelection{})
		decision := base.Clone()
		if mode := transportModeFor(&ctx, base, decision); mode != "herdr" {
			t.Fatalf("mode = %q, want herdr", mode)
		}
		if base["SOHO_TRANSPORT"] != "herdr" || decision["SOHO_TRANSPORT"] != "herdr" {
			t.Fatalf("envs carry SOHO_TRANSPORT=%q / %q, want herdr on both", base["SOHO_TRANSPORT"], decision["SOHO_TRANSPORT"])
		}
	})
}

// TestStatusReadsThroughHc is the read-only status under transport=hc
// (DJA-604 stage 2b): the project file selects hc (no SOHO_TRANSPORT in
// the environment), the roster's worker pane is an hc address, and the
// fake hc serves the states. The session comes from the fake's
// soho.session label (the state dir and the ls label filter both use it),
// and the worker's idle state is read through hc.
func TestStatusReadsThroughHc(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	state := filepath.Join(root, "state")
	for _, dir := range []string{bin, filepath.Join(root, ".agents"), filepath.Join(state, "sX", "wait")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".agents", "soho.conf"), []byte("transport=hc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	roster := "# name\tpane\tkind\trole\tfamily\tcreated_pane\tcwd\tstarted\tmodel\tapprovals\troles\tlane\n" +
		"worker\tworker\tclaude\timplementer\tanthropic\t\t\t\tmodel-x\t\t\t\n"
	if err := os.WriteFile(filepath.Join(state, "sX", "agents.tsv"), []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	caller := `{"address":"t1","state":"live","labels":{"soho.session":"sX"},"status":{"state":"idle","turn":1}}`
	worker := `{"address":"worker","state":"live","status":{"state":"idle","turn":4}}`
	rosterList := `{"terminals":[{"address":"worker","name":"worker","state":"live","status":{"state":"idle","turn":4}}]}`
	if _, err := fakecli.Install(t, bin, "hc", []fakecli.Rule{
		{Argv: []string{"get", "t1", "--json"}, Stdout: caller},
		{Argv: []string{"get", "worker", "--json"}, Stdout: worker},
		{Argv: []string{"ls", "--label", "soho.session=sX", "--json"}, Stdout: rosterList},
	}); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	env := platform.Env{
		"HOME":                root,
		"SOHO_DIR":            state,
		"TMPDIR":              root,
		"PATH":                bin,
		"HC_TERMINAL_ID":      "t1",
		"SOHO_FAKECLI_CONFIG": bin,
	}
	oldOut, oldErr := platform.Stdout, platform.Stderr
	var out, errOut bytes.Buffer
	platform.Stdout, platform.Stderr = &out, &errOut
	code := Run([]string{"status", "worker"}, env)
	platform.Stdout, platform.Stderr = oldOut, oldErr
	// Idle with no report yet is the roster agent's mapped state (the
	// herdr parity rendering): the row proves the command got past the
	// guard and read the worker through hc.
	if code != 0 || out.String() != "worker\tno-report-yet\t\t-\t-\n" {
		t.Fatalf("exit = %d out = %q stderr = %q, want the idle worker's mapped row", code, out.String(), errOut.String())
	}
	calls, err := fakecli.ReadCalls(filepath.Join(bin, "hc.calls.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	// The status reader's screen probes read the worker at its own line
	// counts (recent-screen semantics), so the read argv is pattern-allowed.
	allowedArgv := map[string]bool{
		`["get" "t1" "--json"]`:                       true,
		`["get" "worker" "--json"]`:                   true,
		`["ls" "--label" "soho.session=sX" "--json"]`: true,
	}
	seen := map[string]bool{}
	for _, c := range calls {
		argv := fmt.Sprintf("%q", c.Argv)
		if isWorkerRead(c.Argv) {
			seen[argv] = true
			continue
		}
		if !allowedArgv[argv] {
			t.Fatalf("unexpected hc call %s (the status must read only through the session, the roster and the worker)", argv)
		}
		seen[argv] = true
	}
	for _, want := range []string{
		`["get" "t1" "--json"]`,
		`["get" "worker" "--json"]`,
		`["ls" "--label" "soho.session=sX" "--json"]`,
	} {
		if !seen[want] {
			t.Fatalf("hc calls %#v lack %s: the status did not read through hc (or the session label was not used)", seen, want)
		}
	}
}

// isWorkerRead reports whether argv is a read of the worker's screen at
// some line count: ["read" "worker" "--lines" N "--json"].
func isWorkerRead(argv []string) bool {
	if len(argv) != 5 || argv[0] != "read" || argv[1] != "worker" || argv[2] != "--lines" || argv[4] != "--json" {
		return false
	}
	if argv[3] == "" {
		return false
	}
	for _, r := range argv[3] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
